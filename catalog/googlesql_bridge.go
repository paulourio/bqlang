package catalog

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/goccy/go-googlesql"

	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/formatter"
)

const maxStubArity = 8

var lspLanguageFeatures = []googlesql.LanguageFeature{
	googlesql.LanguageFeatureFeatureAnnotationFramework,
	googlesql.LanguageFeatureFeatureRangeType,
	googlesql.LanguageFeatureFeatureV14UuidType,
	googlesql.LanguageFeatureFeatureEncryption,
	googlesql.LanguageFeatureFeatureJsonArrayFunctions,
	googlesql.LanguageFeatureFeatureJsonValueExtractionFunctions,
	googlesql.LanguageFeatureFeatureJsonLaxValueExtractionFunctions,
	googlesql.LanguageFeatureFeatureJsonConstructorFunctions,
	googlesql.LanguageFeatureFeatureJsonMutatorFunctions,
	googlesql.LanguageFeatureFeatureJsonContainsFunction,
	googlesql.LanguageFeatureFeatureJsonKeysFunction,
	googlesql.LanguageFeatureFeatureJsonQueryLax,
	googlesql.LanguageFeatureFeatureJsonFlattenFunction,
	googlesql.LanguageFeatureFeatureJsonArrayValueExtractionFunctions,
	googlesql.LanguageFeatureFeatureJsonMoreValueExtractionFunctions,
	googlesql.LanguageFeatureFeatureArrayAggregationFunctions,
	googlesql.LanguageFeatureFeatureArrayFindFunctions,
	googlesql.LanguageFeatureFeatureArrayZip,
	googlesql.LanguageFeatureFeatureArrayDistinct,
	googlesql.LanguageFeatureFeatureArrayGreatestLeast,
	googlesql.LanguageFeatureFeatureEnableEditDistanceBytes,
	googlesql.LanguageFeatureFeatureRoundWithRoundingMode,
	googlesql.LanguageFeatureFeatureCbrtFunctions,
	googlesql.LanguageFeatureFeatureDateTimeConstructors,
	googlesql.LanguageFeatureFeatureDateArithmetics,
	googlesql.LanguageFeatureFeatureAdditionalStringFunctions,
	googlesql.LanguageFeatureFeatureTimeBucketFunctions,
	googlesql.LanguageFeatureFeatureNullifzeroZeroifnull,
}

// DefaultLSPLanguageOptions returns LanguageOptions extending
// bqlang.DefaultLanguageOptions with ANNOTATION_FRAMEWORK, RANGE, UUID,
// encryption, vector search, and extended BigQuery JSON/array/math/date features.
//
// Important: googlesql.ParserOptions.SetLanguageOptions moves/consumes the
// passed *googlesql.LanguageOptions handle. Callers creating both ParserOptions
// and AnalyzerOptions must construct separate instances (for example, calling
// DefaultLSPLanguageOptions separately for each) rather than sharing one pointer.
func DefaultLSPLanguageOptions() *googlesql.LanguageOptions {
	lang := bqlang.DefaultLanguageOptions()
	for _, feat := range lspLanguageFeatures {
		if err := lang.EnableLanguageFeature(feat); err != nil {
			panic(fmt.Errorf("DefaultLSPLanguageOptions: enable feature %d: %w", feat, err))
		}
	}

	return lang
}

// RegisterBigQuerySystemVariables registers BigQuery @@system variables on opts.
func RegisterBigQuerySystemVariables(opts *googlesql.AnalyzerOptions, tf *googlesql.TypeFactory) error {
	if opts == nil {
		return errors.New("RegisterBigQuerySystemVariables: nil AnalyzerOptions")
	}

	if tf == nil {
		var err error
		tf, err = googlesql.NewTypeFactory()
		if err != nil {
			return fmt.Errorf("RegisterBigQuerySystemVariables: %w", err)
		}
	}

	strType, err := tf.GetString()
	if err != nil {
		return fmt.Errorf("RegisterBigQuerySystemVariables string type: %w", err)
	}

	int64Type, err := tf.GetInt64()
	if err != nil {
		return fmt.Errorf("RegisterBigQuerySystemVariables int64 type: %w", err)
	}

	tsType, err := tf.GetTimestamp()
	if err != nil {
		return fmt.Errorf("RegisterBigQuerySystemVariables timestamp type: %w", err)
	}

	vars := []struct {
		path []string
		typ  googlesql.Googlesql_TypeNode
	}{
		{[]string{"project_id"}, strType},
		{[]string{"dataset_id"}, strType},
		{[]string{"time_zone"}, strType},
		{[]string{"max_bytes_billed"}, int64Type},
		{[]string{"query_label"}, strType},
		{[]string{"row_count"}, int64Type},
		{[]string{"script", "creation_time"}, tsType},
		{[]string{"script", "bytes_billed"}, int64Type},
		{[]string{"script", "bytes_processed"}, int64Type},
		{[]string{"script", "bytes_scanned"}, int64Type},
		{[]string{"script", "job_id"}, strType},
		{[]string{"script", "label"}, strType},
		{[]string{"script", "statement_text"}, strType},
		{[]string{"error", "message"}, strType},
		{[]string{"error", "stack_trace"}, strType},
		{[]string{"error", "statement_text"}, strType},
		{[]string{"error", "formatted_stack_trace"}, strType},
	}

	for _, v := range vars {
		if err := opts.AddSystemVariable(v.path, v.typ); err != nil {
			return fmt.Errorf("RegisterBigQuerySystemVariables %v: %w", v.path, err)
		}
	}

	return nil
}

// AnalyzeType resolves a GoogleSQL type expression against the shared read-only
// BigQuery built-ins catalog. Using the built-ins catalog instead of a per-script
// MultiCatalog ensures the returned TypeNode never pins a script Catalog or
// ScriptOverlay in a reference cycle.
func AnalyzeType(typeSQL string, opts *googlesql.AnalyzerOptions, tf *googlesql.TypeFactory) (googlesql.Googlesql_TypeNode, error) {
	builtinsCat, err := getDefaultBuiltinsCatalog()
	if err != nil {
		return nil, err
	}

	return googlesql.AnalyzeType(typeSQL, opts, builtinsCat, tf)
}

// OverlayColumn defines a column for a script-scoped temporary table.
type OverlayColumn struct {
	Name string
	Type googlesql.Googlesql_TypeNode
}

type overlayTableEntry struct {
	ref  TableRef
	meta *TableMeta
}

// ScriptOverlay holds script-scoped DECLARE variables and CREATE [TEMP] TABLE
// definitions accumulated across prior statements in a BigQuery script.
type ScriptOverlay struct {
	tf        *googlesql.TypeFactory
	variables map[string]*googlesql.SimpleConstant
	tables    map[string]overlayTableEntry
}

// NewScriptOverlay creates an empty ScriptOverlay bound to tf (or a new TypeFactory).
func NewScriptOverlay(tf *googlesql.TypeFactory) (*ScriptOverlay, error) {
	if tf == nil {
		var err error
		tf, err = googlesql.NewTypeFactory()
		if err != nil {
			return nil, fmt.Errorf("NewScriptOverlay: %w", err)
		}
	}

	return &ScriptOverlay{
		tf:        tf,
		variables: make(map[string]*googlesql.SimpleConstant),
		tables:    make(map[string]overlayTableEntry),
	}, nil
}

// AddVariable registers a script DECLARE variable with the given GoogleSQL type.
func (o *ScriptOverlay) AddVariable(name string, typeNode googlesql.Googlesql_TypeNode) error {
	if o == nil || name == "" || typeNode == nil {
		return errors.New("ScriptOverlay.AddVariable: invalid variable or type")
	}

	val, err := googlesql.NewValueNull(typeNode)
	if err != nil {
		return fmt.Errorf("ScriptOverlay.AddVariable value %q: %w", name, err)
	}

	constant, err := googlesql.NewSimpleConstantCreate([]string{name}, val)
	if err != nil {
		return fmt.Errorf("ScriptOverlay.AddVariable constant %q: %w", name, err)
	}

	o.variables[strings.ToLower(name)] = constant

	return nil
}

// AddTempTable registers a script-scoped CREATE [TEMP] TABLE using resolved OverlayColumns.
func (o *ScriptOverlay) AddTempTable(ref TableRef, cols []OverlayColumn) error {
	if o == nil || ref.Table == "" {
		return errors.New("ScriptOverlay.AddTempTable: empty table name")
	}

	meta := &TableMeta{
		Ref:  ref,
		Type: TableTypeTable,
	}

	for _, col := range cols {
		meta.Schema = append(meta.Schema, googlesqlTypeToColumnMeta(col.Name, col.Type))
	}

	o.tables[strings.ToLower(ref.FullName())] = overlayTableEntry{
		ref:  ref,
		meta: meta,
	}

	return nil
}

func googlesqlTypeToColumnMeta(name string, t googlesql.Googlesql_TypeNode) ColumnMeta {
	col := ColumnMeta{Name: name, Type: "STRING", Mode: ModeNullable}
	if isTypedNil(t) {
		return col
	}

	if ok, err := t.IsArray(); err == nil && ok {
		if elem, elemErr := t.GetElementType(); elemErr == nil {
			inner := googlesqlTypeToColumnMeta(name, elem)
			inner.Mode = ModeRepeated

			return inner
		}
	}

	if ok, err := t.IsStruct(); err == nil && ok {
		return structToColumnMeta(name, t)
	}

	if ok, err := t.IsRange(); err == nil && ok {
		return rangeToColumnMeta(name, t)
	}

	if tnNode, ok := t.(interface {
		TypeName(googlesql.ProductMode) (string, error)
	}); ok {
		if tn, err := tnNode.TypeName(googlesql.ProductModeProductExternal); err == nil && tn != "" {
			col.Type = normalizeTypeName(tn)

			return col
		}
	}

	if dbg, err := t.DebugString(false); err == nil && dbg != "" {
		col.Type = normalizeTypeName(dbg)
	}

	return col
}

func structToColumnMeta(name string, t googlesql.Googlesql_TypeNode) ColumnMeta {
	col := ColumnMeta{Name: name, Type: "STRUCT", Mode: ModeNullable}
	st, err := t.AsStruct()
	if err != nil {
		return col
	}

	fields, err := st.Fields()
	if err != nil {
		return col
	}

	for _, f := range fields {
		col.Fields = append(col.Fields, googlesqlTypeToColumnMeta(f.Name, f.Type_))
	}

	return col
}

func rangeToColumnMeta(name string, t googlesql.Googlesql_TypeNode) ColumnMeta {
	col := ColumnMeta{Name: name, Type: "RANGE", Mode: ModeNullable}
	rt, err := t.AsRange()
	if err != nil {
		return col
	}

	elem, err := rt.ElementType()
	if err != nil {
		return col
	}

	elemMeta := googlesqlTypeToColumnMeta("", elem)
	col.RangeElementType = &RangeElementType{Type: elemMeta.Type}

	return col
}

// AddTableMeta registers a TableMeta in the script overlay.
func (o *ScriptOverlay) AddTableMeta(meta *TableMeta) error {
	if o == nil || meta == nil || meta.Ref.Table == "" {
		return errors.New("ScriptOverlay.AddTableMeta: invalid table metadata")
	}

	cp := meta.Clone()
	cp.Normalize(TableRef{})

	o.tables[strings.ToLower(cp.Ref.FullName())] = overlayTableEntry{
		ref:  cp.Ref,
		meta: cp,
	}

	return nil
}

func (o *ScriptOverlay) buildConstantsCatalog() (*googlesql.SimpleCatalog, error) {
	if o == nil || len(o.variables) == 0 {
		return nil, nil //nolint:nilnil // nil SimpleCatalog indicates no overlay constants to include
	}

	cat, err := googlesql.NewSimpleCatalog("script_overlay", o.tf)
	if err != nil {
		return nil, fmt.Errorf("buildConstantsCatalog: %w", err)
	}

	for _, c := range o.variables {
		if err := cat.AddConstant(c); err != nil {
			return nil, fmt.Errorf("buildConstantsCatalog add constant: %w", err)
		}
	}

	return cat, nil
}

func (o *ScriptOverlay) lookup(parts []string, defaultProj, defaultDS string) (overlayTableEntry, bool) {
	if o == nil || len(o.tables) == 0 || len(parts) == 0 {
		return overlayTableEntry{}, false
	}

	rawKey := strings.ToLower(strings.Join(parts, "."))
	if entry, ok := o.tables[rawKey]; ok {
		return entry, true
	}

	ref := normalizeTableParts(parts, defaultProj, defaultDS)
	if entry, ok := o.tables[strings.ToLower(ref.FullName())]; ok {
		return entry, true
	}

	return overlayTableEntry{}, false
}

var (
	defaultBuiltinsMu  sync.Mutex
	defaultBuiltinsCat *googlesql.SimpleCatalog
	sharedBQFuncSet    = sync.OnceValue(buildBigQueryFuncSet)
)

func getDefaultBuiltinsCatalog() (*googlesql.SimpleCatalog, error) {
	defaultBuiltinsMu.Lock()
	defer defaultBuiltinsMu.Unlock()

	if defaultBuiltinsCat != nil {
		return defaultBuiltinsCat, nil
	}

	cat, err := googlesql.NewSimpleCatalog("bigquery_builtins", nil)
	if err != nil {
		return nil, fmt.Errorf("NewCatalogForAnalysis builtins catalog: %w", err)
	}

	if addErr := cat.AddBuiltinFunctionsAndTypes(&googlesql.BuiltinFunctionOptions{
		LanguageOptions: DefaultLSPLanguageOptions(),
	}); addErr != nil {
		return nil, fmt.Errorf("NewCatalogForAnalysis AddBuiltinFunctionsAndTypes: %w", addErr)
	}

	defaultBuiltinsCat = cat

	return defaultBuiltinsCat, nil
}

// Catalog wraps a GoogleSQL MultiCatalog backed by Store, built-in BigQuery
// functions, script overlays, and CatalogCallback table/function resolution.
//
// Concurrency hazard: go-googlesql acquires an internal WASM Module.mu lock
// during AnalyzeStatement / AnalyzeStatementFromParserAST and re-enters
// CatalogCallback on the calling goroutine. Callers MUST serialize analysis
// calls (including concurrent use of Catalog / GoogleSQLCatalog across
// goroutines) on a shared WASM runtime using an external mutex or dedicated
// analysis worker. Additionally, Store.OnUpdate / WithOnUpdate callbacks run on
// Store worker goroutines and MUST NOT call Store.Close synchronously.
type Catalog struct {
	state    *catalogState
	multi    *googlesql.MultiCatalog
	keepCats []googlesql.CatalogNode
}

// catalogState holds callback-accessible state separately from Catalog.multi and
// Catalog.keepCats so go-googlesql's global callback table (which pins
// catalogCallbackAdapter until cbCat's finalizer runs UnregisterCallback) never
// forms a strong reference cycle back to cbCat or multi.
type catalogState struct {
	store   *Store
	tf      *googlesql.TypeFactory
	overlay *ScriptOverlay

	nextID atomic.Int64

	mu               sync.Mutex
	tablesByFullName map[string]*TableMeta
	tablesByLower    map[string]*TableMeta
	tablesByID       map[int64]*TableMeta
	builtTables      map[string]*googlesql.SimpleTable
	keepTables       []*googlesql.SimpleTable
	keepCols         []*googlesql.SimpleColumn
	keepFuncs        []*googlesql.Function
	keepTVFs         []*googlesql.TableValuedFunction
	keepModels       []*googlesql.SimpleModel
	unresolved       map[string]TableRef
	pending          map[string]TableRef
	bqFuncs          map[string]struct{}
}

// NewCatalogForAnalysis builds a GoogleSQL catalog layer combining optional
// ScriptOverlay, BigQuery built-ins, and Store-backed CatalogCallback.
//
// When lang is nil, a shared read-only built-ins catalog configured with
// DefaultLSPLanguageOptions is reused to avoid re-allocating BigQuery built-ins
// in WASM memory on every analysis pass.
func NewCatalogForAnalysis(store *Store, lang *googlesql.LanguageOptions, overlay *ScriptOverlay) (*Catalog, error) {
	if store == nil {
		return nil, errors.New("NewCatalogForAnalysis: nil store")
	}

	var tf *googlesql.TypeFactory
	if overlay != nil && overlay.tf != nil {
		tf = overlay.tf
	} else {
		var err error
		tf, err = googlesql.NewTypeFactory()
		if err != nil {
			return nil, fmt.Errorf("NewCatalogForAnalysis TypeFactory: %w", err)
		}
	}

	state := &catalogState{
		store:            store,
		tf:               tf,
		overlay:          overlay,
		tablesByFullName: make(map[string]*TableMeta),
		tablesByLower:    make(map[string]*TableMeta),
		tablesByID:       make(map[int64]*TableMeta),
		builtTables:      make(map[string]*googlesql.SimpleTable),
		unresolved:       make(map[string]TableRef),
		pending:          make(map[string]TableRef),
		bqFuncs:          sharedBQFuncSet(),
	}

	c := &Catalog{state: state}

	var layers []googlesql.CatalogNode

	if overlayCat, err := overlay.buildConstantsCatalog(); err != nil {
		return nil, err
	} else if overlayCat != nil {
		layers = append(layers, overlayCat)
		c.keepCats = append(c.keepCats, overlayCat)
	}

	builtinsCat, err := resolveBuiltinsCatalog(lang, tf)
	if err != nil {
		return nil, err
	}

	layers = append(layers, builtinsCat)
	c.keepCats = append(c.keepCats, builtinsCat)

	cbCat, err := googlesql.NewCatalogFromImpl(&catalogCallbackAdapter{state: state})
	if err != nil {
		return nil, fmt.Errorf("NewCatalogForAnalysis callback catalog: %w", err)
	}

	layers = append(layers, cbCat)
	c.keepCats = append(c.keepCats, cbCat)

	multi, err := googlesql.NewMultiCatalogCreate("bqpls_root", layers)
	if err != nil {
		return nil, fmt.Errorf("NewCatalogForAnalysis MultiCatalog: %w", err)
	}

	c.multi = multi

	return c, nil
}

func resolveBuiltinsCatalog(lang *googlesql.LanguageOptions, tf *googlesql.TypeFactory) (*googlesql.SimpleCatalog, error) {
	if lang == nil {
		return getDefaultBuiltinsCatalog()
	}

	builtinsCat, err := googlesql.NewSimpleCatalog("bigquery_builtins", tf)
	if err != nil {
		return nil, fmt.Errorf("NewCatalogForAnalysis builtins catalog: %w", err)
	}

	if addErr := builtinsCat.AddBuiltinFunctionsAndTypes(&googlesql.BuiltinFunctionOptions{
		LanguageOptions: lang,
	}); addErr != nil {
		return nil, fmt.Errorf("NewCatalogForAnalysis AddBuiltinFunctionsAndTypes: %w", addErr)
	}

	return builtinsCat, nil
}

// GoogleSQLCatalog returns the root CatalogNode for AnalyzerOptions / AnalyzeStatement.
func (c *Catalog) GoogleSQLCatalog() googlesql.CatalogNode {
	return c.multi
}

// TypeFactory returns the Catalog's TypeFactory.
func (c *Catalog) TypeFactory() *googlesql.TypeFactory {
	return c.state.tf
}

// LookupTable resolves a GoogleSQL TableNode (including fresh wrappers from
// ResolvedTableScan.Table()) back to its canonical *TableMeta.
func (c *Catalog) LookupTable(node googlesql.TableNode) (*TableMeta, bool) {
	if c == nil || c.state == nil || isTypedNil(node) {
		return nil, false
	}

	st := c.state
	st.mu.Lock()
	defer st.mu.Unlock()

	if id, err := node.GetSerializationId(); err == nil && id > 0 {
		if meta, ok := st.tablesByID[id]; ok {
			return meta, true
		}
	}

	if fullName, err := node.FullName(); err == nil && fullName != "" {
		if meta, ok := st.tablesByFullName[fullName]; ok {
			return meta, true
		}

		if meta, ok := st.tablesByLower[strings.ToLower(fullName)]; ok {
			return meta, true
		}
	}

	return nil, false
}

// UnresolvedRefs returns a sorted slice of TableRefs that missed during FindTable.
func (c *Catalog) UnresolvedRefs() []TableRef {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()

	return sortedRefsFromMap(c.state.unresolved)
}

// PendingRefs returns a sorted slice of TableRefs that missed during FindTable
// and are currently being fetched asynchronously by Store.
func (c *Catalog) PendingRefs() []TableRef {
	c.state.mu.Lock()
	refs := sortedRefsFromMap(c.state.pending)
	c.state.mu.Unlock()

	out := make([]TableRef, 0, len(refs))
	for _, ref := range refs {
		if c.state.store.IsPending(ref) {
			out = append(out, ref)
		}
	}

	return out
}

// HasPendingTables reports whether any missed table from analysis is currently
// pending an asynchronous fetch.
func (c *Catalog) HasPendingTables() bool {
	c.state.mu.Lock()
	refs := sortedRefsFromMap(c.state.pending)
	c.state.mu.Unlock()

	return slices.ContainsFunc(refs, c.state.store.IsPending)
}

func sortedRefsFromMap(m map[string]TableRef) []TableRef {
	out := make([]TableRef, 0, len(m))
	for _, ref := range m {
		out = append(out, ref)
	}

	slices.SortFunc(out, func(a, b TableRef) int {
		if cmp := strings.Compare(a.FullName(), b.FullName()); cmp != 0 {
			return cmp
		}

		if cmp := strings.Compare(a.Project, b.Project); cmp != 0 {
			return cmp
		}

		if cmp := strings.Compare(a.Dataset, b.Dataset); cmp != 0 {
			return cmp
		}

		return strings.Compare(a.Table, b.Table)
	})

	return out
}

type catalogCallbackAdapter struct {
	googlesql.CatalogCallbackDefaults
	state *catalogState
}

func (a *catalogCallbackAdapter) FullName() (string, error) {
	return "bqpls_catalog", nil
}

func (a *catalogCallbackAdapter) FindTable(path []string, _ *googlesql.FindOptions) (googlesql.TableNode, error) {
	return a.state.findTable(path)
}

func (a *catalogCallbackAdapter) SuggestTable(mistypedPath []string) (string, error) {
	parts := normalizePathParts(mistypedPath)

	return a.state.store.SuggestTableName(parts), nil
}

func (a *catalogCallbackAdapter) FindFunction(path []string, _ *googlesql.FindOptions) (*googlesql.Function, error) {
	return a.state.findFunction(path)
}

func (a *catalogCallbackAdapter) FindTableValuedFunction(
	path []string,
	_ *googlesql.FindOptions,
) (*googlesql.TableValuedFunction, error) {
	return a.state.findTVF(path)
}

func (a *catalogCallbackAdapter) FindModel(path []string, _ *googlesql.FindOptions) (googlesql.ModelNode, error) {
	return a.state.findModel(path)
}

func (a *catalogCallbackAdapter) FindType(
	path []string,
	_ *googlesql.FindOptions,
) (googlesql.Googlesql_TypeNode, error) {
	return nil, fmt.Errorf("type not found: %s", strings.Join(path, "."))
}

func (a *catalogCallbackAdapter) FindProcedure(
	path []string,
	_ *googlesql.FindOptions,
) (*googlesql.Procedure, error) {
	return nil, fmt.Errorf("procedure not found: %s", strings.Join(path, "."))
}

func (a *catalogCallbackAdapter) FindPropertyGraph2(
	path []string,
	_ *googlesql.FindOptions,
) (googlesql.PropertyGraphNode, error) {
	return nil, fmt.Errorf("property graph not found: %s", strings.Join(path, "."))
}

func (a *catalogCallbackAdapter) FindConnection(
	path []string,
	_ *googlesql.FindOptions,
) (googlesql.ConnectionNode, error) {
	return nil, fmt.Errorf("connection not found: %s", strings.Join(path, "."))
}

func (a *catalogCallbackAdapter) FindSequence(
	path []string,
	_ *googlesql.FindOptions,
) (googlesql.SequenceNode, error) {
	return nil, fmt.Errorf("sequence not found: %s", strings.Join(path, "."))
}

func (c *catalogState) findTable(path []string) (googlesql.TableNode, error) {
	parts := normalizePathParts(path)
	if len(parts) == 0 || len(parts) > 3 {
		return nil, fmt.Errorf("table not found: %s", strings.Join(path, "."))
	}

	if entry, ok := c.overlay.lookup(parts, c.store.DefaultProject(), c.store.DefaultDataset()); ok {
		return c.materializeOverlayTable(entry)
	}

	ref := normalizeTableParts(parts, c.store.DefaultProject(), c.store.DefaultDataset())
	if strings.HasSuffix(ref.Table, "*") {
		return c.findWildcardTable(ref, parts)
	}

	key := ref.FullName()

	c.mu.Lock()
	if tbl, ok := c.builtTables[key]; ok {
		c.mu.Unlock()

		return tbl, nil
	}
	c.mu.Unlock()

	meta, ok := c.store.GetTable(ref)
	if !ok {
		c.recordMiss(ref)

		return nil, c.tableNotFoundError(parts)
	}

	return c.buildAndCacheSimpleTable(meta, key, false)
}

func (c *catalogState) findWildcardTable(ref TableRef, parts []string) (googlesql.TableNode, error) {
	prefix := strings.TrimSuffix(ref.Table, "*")
	cacheKey := "wildcard:" + ref.FullName()

	c.mu.Lock()
	if tbl, ok := c.builtTables[cacheKey]; ok {
		c.mu.Unlock()

		return tbl, nil
	}
	c.mu.Unlock()

	meta, ok := c.store.MatchWildcardTable(ref.Project, ref.Dataset, prefix)
	if !ok {
		c.recordMiss(ref)

		return nil, c.tableNotFoundError(parts)
	}

	cp := meta.Clone()
	cp.Ref = ref

	return c.buildAndCacheSimpleTable(cp, cacheKey, true)
}

func (c *catalogState) tableNotFoundError(parts []string) error {
	name := strings.Join(parts, ".")
	if suggestion := c.store.SuggestTableName(parts); suggestion != "" {
		return fmt.Errorf("table not found: %s; did you mean %s?", name, suggestion)
	}

	return fmt.Errorf("table not found: %s", name)
}

func (c *catalogState) recordMiss(ref TableRef) {
	key := ref.FullName()
	isPending := c.store.IsPending(ref)

	c.mu.Lock()
	c.unresolved[key] = ref
	if isPending {
		c.pending[key] = ref
	}
	c.mu.Unlock()
}

func (c *catalogState) materializeOverlayTable(entry overlayTableEntry) (googlesql.TableNode, error) {
	key := "overlay:" + strings.ToLower(entry.ref.FullName())

	c.mu.Lock()
	if tbl, ok := c.builtTables[key]; ok {
		c.mu.Unlock()

		return tbl, nil
	}
	c.mu.Unlock()

	return c.buildAndCacheSimpleTable(entry.meta, key, false)
}

func (c *catalogState) buildAndCacheSimpleTable(
	meta *TableMeta,
	cacheKey string,
	isWildcard bool,
) (*googlesql.SimpleTable, error) {
	id := c.nextID.Add(1)
	fullName := meta.Ref.FullName()
	shortName := meta.Ref.Table
	if shortName == "" {
		shortName = fullName
	}

	tbl, err := googlesql.NewSimpleTable(shortName, id)
	if err != nil {
		return nil, fmt.Errorf("NewSimpleTable %q: %w", fullName, err)
	}

	if nameErr := tbl.SetFullName(fullName); nameErr != nil {
		return nil, fmt.Errorf("SetFullName %q: %w", fullName, nameErr)
	}

	cols, err := c.populateTableColumns(tbl, fullName, meta, isWildcard)
	if err != nil {
		return nil, err
	}

	c.registerBuiltTable(cacheKey, id, fullName, meta, tbl, cols)

	return tbl, nil
}

func (c *catalogState) populateTableColumns(
	tbl *googlesql.SimpleTable,
	fullName string,
	meta *TableMeta,
	isWildcard bool,
) ([]*googlesql.SimpleColumn, error) {
	cols := make([]*googlesql.SimpleColumn, 0, len(meta.Schema)+3)

	for i := range meta.Schema {
		colMeta := meta.Schema[i]
		colType, err := c.columnTypeToGoogleSQL(colMeta)
		if err != nil {
			return nil, fmt.Errorf("column %s.%s: %w", fullName, colMeta.Name, err)
		}

		sc, err := googlesql.NewSimpleColumn(fullName, colMeta.Name, colType, false, true)
		if err != nil {
			return nil, fmt.Errorf("NewSimpleColumn %s.%s: %w", fullName, colMeta.Name, err)
		}

		if err := tbl.AddColumn2(sc, false); err != nil {
			return nil, fmt.Errorf("AddColumn2 %s.%s: %w", fullName, colMeta.Name, err)
		}

		cols = append(cols, sc)
	}

	pseudoCols, err := c.buildPseudoColumns(tbl, fullName, meta, isWildcard)
	if err != nil {
		return nil, err
	}

	cols = append(cols, pseudoCols...)

	return cols, nil
}

func (c *catalogState) buildPseudoColumns(
	tbl *googlesql.SimpleTable,
	fullName string,
	meta *TableMeta,
	isWildcard bool,
) ([]*googlesql.SimpleColumn, error) {
	var cols []*googlesql.SimpleColumn

	if meta.IsIngestionTimePartitioned() {
		ingestionCols, err := c.addIngestionPseudoCols(tbl, fullName, meta)
		if err != nil {
			return nil, err
		}

		cols = append(cols, ingestionCols...)
	}

	if isWildcard {
		sc, err := c.addWildcardPseudoCol(tbl, fullName, meta)
		if err != nil {
			return nil, err
		}

		if sc != nil {
			cols = append(cols, sc)
		}
	}

	return cols, nil
}

func (c *catalogState) addIngestionPseudoCols(
	tbl *googlesql.SimpleTable,
	fullName string,
	meta *TableMeta,
) ([]*googlesql.SimpleColumn, error) {
	var cols []*googlesql.SimpleColumn

	if _, exists := meta.FindColumn("_PARTITIONTIME"); !exists {
		tsType, err := c.tf.GetTimestamp()
		if err != nil {
			return nil, fmt.Errorf("pseudo _PARTITIONTIME type: %w", err)
		}

		sc, err := addPseudoCol(tbl, fullName, "_PARTITIONTIME", tsType)
		if err != nil {
			return nil, err
		}

		cols = append(cols, sc)
	}

	if _, exists := meta.FindColumn("_PARTITIONDATE"); !exists {
		dateType, err := c.tf.GetDate()
		if err != nil {
			return nil, fmt.Errorf("pseudo _PARTITIONDATE type: %w", err)
		}

		sc, err := addPseudoCol(tbl, fullName, "_PARTITIONDATE", dateType)
		if err != nil {
			return nil, err
		}

		cols = append(cols, sc)
	}

	return cols, nil
}

func (c *catalogState) addWildcardPseudoCol(
	tbl *googlesql.SimpleTable,
	fullName string,
	meta *TableMeta,
) (*googlesql.SimpleColumn, error) {
	if _, exists := meta.FindColumn("_TABLE_SUFFIX"); exists {
		return nil, nil //nolint:nilnil // nil SimpleColumn indicates _TABLE_SUFFIX already exists
	}

	strType, err := c.tf.GetString()
	if err != nil {
		return nil, fmt.Errorf("pseudo _TABLE_SUFFIX type: %w", err)
	}

	return addPseudoCol(tbl, fullName, "_TABLE_SUFFIX", strType)
}

func addPseudoCol(
	tbl *googlesql.SimpleTable,
	fullName, name string,
	typ googlesql.Googlesql_TypeNode,
) (*googlesql.SimpleColumn, error) {
	sc, err := googlesql.NewSimpleColumn(fullName, name, typ, true, false)
	if err != nil {
		return nil, fmt.Errorf("NewSimpleColumn %s.%s: %w", fullName, name, err)
	}

	if err := tbl.AddColumn2(sc, false); err != nil {
		return nil, fmt.Errorf("AddColumn2 %s.%s: %w", fullName, name, err)
	}

	return sc, nil
}

func (c *catalogState) registerBuiltTable(
	cacheKey string,
	id int64,
	fullName string,
	meta *TableMeta,
	tbl *googlesql.SimpleTable,
	cols []*googlesql.SimpleColumn,
) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.builtTables[cacheKey] = tbl
	c.keepTables = append(c.keepTables, tbl)
	c.keepCols = append(c.keepCols, cols...)

	if meta != nil {
		c.tablesByID[id] = meta
		c.tablesByFullName[fullName] = meta
		c.tablesByLower[strings.ToLower(fullName)] = meta
	}
}

func (c *catalogState) columnTypeToGoogleSQL(col ColumnMeta) (googlesql.Googlesql_TypeNode, error) {
	base, err := c.baseColumnTypeToGoogleSQL(col)
	if err != nil {
		return nil, err
	}

	if col.EffectiveMode() == ModeRepeated {
		arrType, err := c.tf.MakeArrayType2(base)
		if err != nil {
			return nil, fmt.Errorf("MakeArrayType2 %q: %w", col.Name, err)
		}

		return arrType, nil
	}

	return base, nil
}

func (c *catalogState) baseColumnTypeToGoogleSQL(col ColumnMeta) (googlesql.Googlesql_TypeNode, error) {
	typ := normalizeTypeName(col.Type)

	switch typ {
	case "STRUCT":
		fields := make([]*googlesql.StructField, 0, len(col.Fields))
		for i := range col.Fields {
			ft, err := c.columnTypeToGoogleSQL(col.Fields[i])
			if err != nil {
				return nil, err
			}

			fields = append(fields, &googlesql.StructField{
				Name:  col.Fields[i].Name,
				Type_: ft,
			})
		}

		return c.tf.MakeStructType2(fields)
	case "RANGE":
		elemName := "DATE"
		if col.RangeElementType != nil && col.RangeElementType.Type != "" {
			elemName = normalizeTypeName(col.RangeElementType.Type)
		}

		elemType, err := c.scalarTypeToGoogleSQL(elemName)
		if err != nil {
			return nil, fmt.Errorf("RANGE element %q: %w", elemName, err)
		}

		return c.tf.MakeRangeType3(elemType)
	default:
		return c.scalarTypeToGoogleSQL(typ)
	}
}

func (c *catalogState) scalarTypeToGoogleSQL(typ string) (googlesql.Googlesql_TypeNode, error) {
	switch typ {
	case "INT64":
		return c.tf.GetInt64()
	case "STRING":
		return c.tf.GetString()
	case "BYTES":
		return c.tf.GetBytes()
	case "FLOAT64", "FLOAT", "DOUBLE":
		return c.tf.GetDouble()
	case "NUMERIC":
		return c.tf.GetNumeric()
	case "BIGNUMERIC":
		return c.tf.GetBignumeric()
	case "BOOL":
		return c.tf.GetBool()
	case "TIMESTAMP":
		return c.tf.GetTimestamp()
	case "DATE":
		return c.tf.GetDate()
	case "TIME":
		return c.tf.GetTime()
	case "DATETIME":
		return c.tf.GetDatetime()
	case "GEOGRAPHY":
		return c.tf.GetGeography()
	case "JSON":
		return c.tf.GetJson()
	case "INTERVAL":
		return c.tf.GetInterval()
	case "UUID":
		return c.tf.GetUuid()
	default:
		return c.tf.GetString()
	}
}

func (c *catalogState) findFunction(path []string) (*googlesql.Function, error) {
	if len(path) == 0 {
		return nil, errors.New("function not found: empty name")
	}

	upper := strings.ToUpper(strings.Join(path, "."))
	if _, ok := c.bqFuncs[upper]; !ok {
		return nil, fmt.Errorf("function not found: %s", strings.Join(path, "."))
	}

	mode, fnOpts := functionModeAndOptions(upper)

	var (
		sigs []*googlesql.FunctionSignature
		err  error
	)

	if preservesFirstArgType(upper) {
		sigs, err = buildVariadicAny1Signatures(maxStubArity)
	} else {
		retType, retErr := c.stubFunctionReturnType(upper)
		if retErr != nil {
			return nil, retErr
		}

		sigs, err = buildVariadicArbitrarySignatures(retType, maxStubArity)
	}

	if err != nil {
		return nil, err
	}

	fn, err := googlesql.NewFunction(path, "BigQuery", mode, sigs, fnOpts)
	if err != nil {
		return nil, fmt.Errorf("NewFunction %q: %w", upper, err)
	}

	c.mu.Lock()
	c.keepFuncs = append(c.keepFuncs, fn)
	c.mu.Unlock()

	return fn, nil
}

func functionModeAndOptions(upperName string) (googlesql.FunctionEnums_Mode, *googlesql.FunctionOptions) {
	if isBigQueryAggregateFunc(upperName) {
		return googlesql.FunctionEnums_ModeAggregate, &googlesql.FunctionOptions{
			SupportsOverClause:    true,
			SupportsSafeErrorMode: true,
		}
	}

	if isBigQueryAnalyticFunc(upperName) {
		return googlesql.FunctionEnums_ModeAnalytic, &googlesql.FunctionOptions{}
	}

	return googlesql.FunctionEnums_ModeScalar, &googlesql.FunctionOptions{
		SupportsSafeErrorMode: true,
	}
}

func isBigQueryAggregateFunc(upperName string) bool {
	switch upperName {
	case "MAX_BY",
		"MIN_BY",
		"ANY_VALUE",
		"ARRAY_AGG",
		"ARRAY_CONCAT_AGG",
		"AVG",
		"BIT_AND",
		"BIT_OR",
		"BIT_XOR",
		"COUNT",
		"COUNTIF",
		"GROUPING",
		"LOGICAL_AND",
		"LOGICAL_OR",
		"MAX",
		"MIN",
		"STRING_AGG",
		"SUM",
		"CORR",
		"COVAR_POP",
		"COVAR_SAMP",
		"STDDEV",
		"STDDEV_POP",
		"STDDEV_SAMP",
		"VAR_POP",
		"VAR_SAMP",
		"VARIANCE",
		"APPROX_COUNT_DISTINCT",
		"APPROX_QUANTILES",
		"APPROX_TOP_COUNT",
		"APPROX_TOP_SUM",
		"HLL_COUNT.EXTRACT",
		"HLL_COUNT.INIT",
		"HLL_COUNT.MERGE",
		"HLL_COUNT.MERGE_PARTIAL",
		"ST_CENTROID_AGG",
		"ST_UNION_AGG",
		"AGG":
		return true
	default:
		return false
	}
}

func isBigQueryAnalyticFunc(upperName string) bool {
	switch upperName {
	case "CUME_DIST",
		"DENSE_RANK",
		"NTILE",
		"PERCENT_RANK",
		"RANK",
		"ROW_NUMBER",
		"FIRST_VALUE",
		"LAG",
		"LAST_VALUE",
		"LEAD",
		"NTH_VALUE",
		"PERCENTILE_CONT",
		"PERCENTILE_DISC":
		return true
	default:
		return false
	}
}

func preservesFirstArgType(upperName string) bool {
	switch upperName {
	case "MAX_BY", "MIN_BY", "ANY_VALUE", "MAX", "MIN", "FIRST_VALUE", "LAST_VALUE", "LAG", "LEAD", "NTH_VALUE":
		return true
	default:
		return false
	}
}

func buildVariadicAny1Signatures(maxArgs int) ([]*googlesql.FunctionSignature, error) {
	retArg, err := googlesql.NewFunctionArgumentType4(
		googlesql.SignatureArgumentKindArgTypeAny1,
		-1,
	)
	if err != nil {
		return nil, err
	}

	sigs := make([]*googlesql.FunctionSignature, 0, maxArgs)
	for n := 1; n <= maxArgs; n++ {
		args := make([]*googlesql.FunctionArgumentType, 0, n)

		firstArg, firstErr := googlesql.NewFunctionArgumentType4(
			googlesql.SignatureArgumentKindArgTypeAny1,
			-1,
		)
		if firstErr != nil {
			return nil, firstErr
		}

		args = append(args, firstArg)

		for range n - 1 {
			arg, argErr := googlesql.NewFunctionArgumentType4(
				googlesql.SignatureArgumentKindArgTypeArbitrary,
				-1,
			)
			if argErr != nil {
				return nil, argErr
			}

			args = append(args, arg)
		}

		sig, sigErr := googlesql.NewFunctionSignature3(retArg, args, int64(n))
		if sigErr != nil {
			return nil, sigErr
		}

		sigs = append(sigs, sig)
	}

	return sigs, nil
}

func (c *catalogState) stubFunctionReturnType(upperName string) (googlesql.Googlesql_TypeNode, error) {
	switch upperName {
	case "AI.GENERATE_BOOL", "SEARCH":
		return c.tf.GetBool()
	case "AI.GENERATE_INT":
		return c.tf.GetInt64()
	case "AI.GENERATE_DOUBLE":
		return c.tf.GetDouble()
	case "AI.GENERATE":
		strType, err := c.tf.GetString()
		if err != nil {
			return nil, err
		}

		jsonType, err := c.tf.GetJson()
		if err != nil {
			return nil, err
		}

		return c.tf.MakeStructType2([]*googlesql.StructField{
			{Name: "result", Type_: strType},
			{Name: "full_response", Type_: jsonType},
			{Name: "status", Type_: strType},
		})
	default:
		return c.tf.GetString()
	}
}

func (c *catalogState) findTVF(path []string) (*googlesql.TableValuedFunction, error) {
	if len(path) == 0 {
		return nil, errors.New("table-valued function not found: empty name")
	}

	upper := strings.ToUpper(strings.Join(path, "."))
	if !isBigQueryTVF(upper) {
		return nil, fmt.Errorf("table-valued function not found: %s", strings.Join(path, "."))
	}

	strType, err := c.tf.GetString()
	if err != nil {
		return nil, err
	}

	doubleType, err := c.tf.GetDouble()
	if err != nil {
		return nil, err
	}

	relSchema, err := googlesql.NewTVFRelation([]*googlesql.TVFSchemaColumn{
		{Name: "result", Type_: strType},
		{Name: "content", Type_: strType},
		{Name: "distance", Type_: doubleType},
	})
	if err != nil {
		return nil, fmt.Errorf("NewTVFRelation %q: %w", upper, err)
	}

	sigs, err := buildTVFSignatures(upper)
	if err != nil {
		return nil, err
	}

	tvf, err := googlesql.NewFixedOutputSchemaTVF3(path, sigs, relSchema, nil)
	if err != nil {
		return nil, fmt.Errorf("NewFixedOutputSchemaTVF3 %q: %w", upper, err)
	}

	base := tvf.TableValuedFunction

	c.mu.Lock()
	c.keepTVFs = append(c.keepTVFs, base)
	c.mu.Unlock()

	return base, nil
}

func (c *catalogState) findModel(path []string) (googlesql.ModelNode, error) {
	if len(path) == 0 {
		return nil, errors.New("model not found: empty name")
	}

	strType, err := c.tf.GetString()
	if err != nil {
		return nil, err
	}

	name := strings.Join(normalizePathParts(path), ".")

	inCol, err := googlesql.NewSimpleColumn(name, "input", strType, false, true)
	if err != nil {
		return nil, err
	}

	outCol, err := googlesql.NewSimpleColumn(name, "predicted_label", strType, false, false)
	if err != nil {
		return nil, err
	}

	id := c.nextID.Add(1)

	model, err := googlesql.NewSimpleModel(
		name,
		[]googlesql.Googlesql_ColumnNode{inCol},
		[]googlesql.Googlesql_ColumnNode{outCol},
		false,
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("NewSimpleModel %q: %w", name, err)
	}

	c.mu.Lock()
	c.keepCols = append(c.keepCols, inCol, outCol)
	c.keepModels = append(c.keepModels, model)
	c.mu.Unlock()

	return model, nil
}

func buildVariadicArbitrarySignatures(
	retType googlesql.Googlesql_TypeNode,
	maxArgs int,
) ([]*googlesql.FunctionSignature, error) {
	retArg, err := googlesql.NewFunctionArgumentType3(retType, -1)
	if err != nil {
		return nil, err
	}

	sigs := make([]*googlesql.FunctionSignature, 0, maxArgs+1)
	for n := 0; n <= maxArgs; n++ {
		args := make([]*googlesql.FunctionArgumentType, 0, n)
		for range n {
			arg, argErr := googlesql.NewFunctionArgumentType4(
				googlesql.SignatureArgumentKindArgTypeArbitrary,
				-1,
			)
			if argErr != nil {
				return nil, argErr
			}

			args = append(args, arg)
		}

		sig, sigErr := googlesql.NewFunctionSignature3(retArg, args, int64(n))
		if sigErr != nil {
			return nil, sigErr
		}

		sigs = append(sigs, sig)
	}

	return sigs, nil
}

func buildTVFSignatures(upperName string) ([]*googlesql.FunctionSignature, error) {
	retArg, err := googlesql.NewFunctionArgumentTypeAnyRelation()
	if err != nil {
		return nil, err
	}

	var patterns [][]googlesql.SignatureArgumentKind
	switch upperName {
	case "ML.PREDICT", "ML.EVALUATE", "AI.GENERATE_TABLE", "AI.GENERATE_TEXT":
		patterns = append(patterns, []googlesql.SignatureArgumentKind{
			googlesql.SignatureArgumentKindArgTypeModel,
			googlesql.SignatureArgumentKindArgTypeArbitrary,
		})
		for extra := 0; extra <= maxStubArity-2; extra++ {
			pat := make([]googlesql.SignatureArgumentKind, 0, 2+extra)
			pat = append(pat,
				googlesql.SignatureArgumentKindArgTypeModel,
				googlesql.SignatureArgumentKindArgTypeRelation,
			)
			for range extra {
				pat = append(pat, googlesql.SignatureArgumentKindArgTypeArbitrary)
			}

			patterns = append(patterns, pat)
		}
	default:
		for extra := range maxStubArity {
			pat := make([]googlesql.SignatureArgumentKind, 0, 1+extra)
			pat = append(pat, googlesql.SignatureArgumentKindArgTypeRelation)
			for range extra {
				pat = append(pat, googlesql.SignatureArgumentKindArgTypeArbitrary)
			}

			patterns = append(patterns, pat)
		}
	}

	sigs := make([]*googlesql.FunctionSignature, 0, len(patterns))
	for i, pat := range patterns {
		args, err := buildTVFArgs(pat)
		if err != nil {
			return nil, err
		}

		sig, err := googlesql.NewFunctionSignature3(retArg, args, int64(i))
		if err != nil {
			return nil, err
		}

		sigs = append(sigs, sig)
	}

	return sigs, nil
}

func buildTVFArgs(kinds []googlesql.SignatureArgumentKind) ([]*googlesql.FunctionArgumentType, error) {
	args := make([]*googlesql.FunctionArgumentType, 0, len(kinds))
	for _, kind := range kinds {
		switch kind {
		case googlesql.SignatureArgumentKindArgTypeModel:
			arg, err := googlesql.NewFunctionArgumentTypeAnyModel()
			if err != nil {
				return nil, err
			}

			args = append(args, arg)
		case googlesql.SignatureArgumentKindArgTypeRelation:
			arg, err := googlesql.NewFunctionArgumentTypeAnyRelation()
			if err != nil {
				return nil, err
			}

			args = append(args, arg)
		default:
			arg, err := googlesql.NewFunctionArgumentType4(kind, -1)
			if err != nil {
				return nil, err
			}

			args = append(args, arg)
		}
	}

	return args, nil
}

func isBigQueryTVF(upperName string) bool {
	switch upperName {
	case "ML.PREDICT",
		"ML.EVALUATE",
		"AI.GENERATE_TABLE",
		"AI.GENERATE_TEXT",
		"AI.FORECAST",
		"AI.SEARCH",
		"VECTOR_SEARCH",
		"EXTERNAL_OBJECT_TRANSFORM":
		return true
	default:
		return false
	}
}

func buildBigQueryFuncSet() map[string]struct{} {
	list := formatter.BigQueryBuiltinFunctions()
	set := make(map[string]struct{}, len(list)+12)
	for _, name := range list {
		set[strings.ToUpper(name)] = struct{}{}
	}

	extra := []string{
		"AI.GENERATE",
		"AI.GENERATE_TEXT",
		"AI.GENERATE_BOOL",
		"AI.GENERATE_INT",
		"AI.GENERATE_DOUBLE",
		"NET.FORMAT_IP",
		"NET.PARSE_IP",
		"NET.FORMAT_PACKED_IP",
		"NET.PARSE_PACKED_IP",
		"NET.IP_IN_NET",
		"NET.MAKE_NET",
	}
	for _, name := range extra {
		set[name] = struct{}{}
	}

	return set
}

func normalizePathParts(path []string) []string {
	cleaned := make([]string, 0, len(path))
	for _, raw := range path {
		seg := strings.Trim(raw, "`")
		if seg != "" {
			cleaned = append(cleaned, seg)
		}
	}

	if len(cleaned) == 0 {
		return nil
	}

	// Multi-element paths from GoogleSQL (e.g., `example.com:my-proj`.ds.tbl)
	// already carry distinct identifier segments; preserve dots inside each
	// segment instead of splitting them.
	if len(cleaned) > 1 {
		return cleaned
	}

	var parts []string
	for seg := range strings.SplitSeq(cleaned[0], ".") {
		seg = strings.Trim(seg, "`")
		if seg != "" {
			parts = append(parts, seg)
		}
	}

	// Collapse dotted project prefixes (e.g., `example.com:my-proj.ds.tbl`)
	// back into the leading Project segment, matching store.go:600-607.
	if len(parts) > 3 {
		return []string{
			strings.Join(parts[:len(parts)-2], "."),
			parts[len(parts)-2],
			parts[len(parts)-1],
		}
	}

	return parts
}

func normalizeTableParts(parts []string, defaultProject, defaultDataset string) TableRef {
	switch len(parts) {
	case 1:
		return TableRef{Project: defaultProject, Dataset: defaultDataset, Table: parts[0]}
	case 2:
		return TableRef{Project: defaultProject, Dataset: parts[0], Table: parts[1]}
	case 3:
		return TableRef{Project: parts[0], Dataset: parts[1], Table: parts[2]}
	default:
		return TableRef{}
	}
}

func isTypedNil(v any) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Slice, reflect.Map, reflect.Func, reflect.Chan:
		return rv.IsNil()
	default:
		return false
	}
}
