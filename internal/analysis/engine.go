package analysis

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/goccy/go-googlesql"

	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/internal/bqconfig"
	"github.com/paulourio/bqlang/internal/catalog"
	"github.com/paulourio/bqlang/internal/syntax"
)

const (
	maxStmtCacheEntries = 256
	maxDeltaHistory     = 4
)

// wasmMu serializes all calls into go-googlesql's shared WASM runtime and AST/ResolvedAST wrappers.
var wasmMu sync.Mutex

var googleSQLErrLocRe = regexp.MustCompile(`\[at (\d+):(\d+)\]`)

// Engine performs incremental syntax, AST, and ResolvedAST analysis per document.
type Engine struct {
	defaultStore *catalog.Store
	defaultOpts  *bqconfig.LSPOptions
	parserOpts   *googlesql.ParserOptions

	catalogGen atomic.Uint64
	costGen    atomic.Uint64
	resultSeq  atomic.Uint64

	docsMu sync.RWMutex
	docs   map[string]*docEntry
}

type docEntry struct {
	mu      sync.Mutex
	uri     string
	version int
	text    string
	cfg     DocumentConfig

	lineIdx   *syntax.LineIndex
	tokens    []syntax.Token
	projected syntax.ProjectedSQL

	astCache      map[[32]byte]*cachedAST
	astOrder      [][32]byte
	resolvedCache map[resolvedCacheKey]*cachedResolved
	resolvedOrder []resolvedCacheKey

	analyzedGen     uint64
	analyzedCostGen uint64
	snapshot        *DocumentSnapshot
	lkgTokens       []ByteSemanticToken
	deltaHistory    []tokenDeltaEntry
}

type tokenDeltaEntry struct {
	resultID string
	data     []uint32
}

type cachedAST struct {
	sql            string
	parserOut      *googlesql.ParserOutput
	astStmt        googlesql.ASTStatementNode
	parseErr       error
	errLine        int
	errCol         int
	errMsg         string
	parserTokens   []ByteSemanticToken
	dryRunEligible bool
	astTableRefs   [][]string
}

type resolvedCacheKey struct {
	stmtHash       [32]byte
	catalogGen     uint64
	overlayDigest  [32]byte
	defaultProject string
	defaultDataset string
}

type cachedResolved struct {
	analyzerOut        *googlesql.AnalyzerOutput
	analyzerOuts       []*googlesql.AnalyzerOutput
	catalog            *catalog.Catalog
	resolvedStmt       googlesql.ResolvedStatementNode
	resolvedStmts      []googlesql.ResolvedStatementNode
	innerResults       []InnerAnalyzeResult
	analyzeErr         error
	errLine            int
	errCol             int
	errMsg             string
	resolvedTokens     []ByteSemanticToken
	scanLintIssues     []ScanLintIssue
	scannedTables      []TableCostBreakdown
	unresolvedRefs     []catalog.TableRef
	pendingRefs        []catalog.TableRef
	declaredVars       []DeclaredVarInfo
	tempTables         []TempTableInfo
	hasExprPlaceholder bool
}

// InnerAnalyzeResult records per-statement analysis errors and ref resolution
// within a top-level statement or procedural script block.
type InnerAnalyzeResult struct {
	AnalyzeErr         error
	ErrLine            int
	ErrCol             int
	ErrMsg             string
	UnresolvedRefs     []catalog.TableRef
	PendingRefs        []catalog.TableRef
	HasExprPlaceholder bool
}

// DeclaredVarInfo records a DECLARE variable extracted from a script statement.
type DeclaredVarInfo struct {
	Name       string
	TypeSQL    string
	TypeName   string
	LocalStart int
	LocalEnd   int
	OrigSpan   syntax.ByteSpan
}

// TempTableInfo records a CREATE [TEMP] TABLE schema extracted from a statement.
type TempTableInfo struct {
	Ref      catalog.TableRef
	Meta     *catalog.TableMeta
	OrigSpan syntax.ByteSpan
}

// ScanLintIssue records a partition or clustering lint finding on a table scan.
type ScanLintIssue struct {
	Code      string
	LocalSpan syntax.ByteSpan
	Message   string
}

// AnalyzedStatement holds cached AST and ResolvedAST products for one statement.
type AnalyzedStatement struct {
	ShadowSpan         syntax.ByteSpan
	OrigSpan           syntax.ByteSpan
	SQL                string
	Hash               [32]byte
	ASTStmt            googlesql.ASTStatementNode
	ParseErr           error
	ParseErrLine       int
	ParseErrCol        int
	ParseErrMsg        string
	ResolvedStmt       googlesql.ResolvedStatementNode
	ResolvedStmts      []googlesql.ResolvedStatementNode
	InnerResults       []InnerAnalyzeResult
	Catalog            *catalog.Catalog
	AnalyzeErr         error
	AnalyzeErrLine     int
	AnalyzeErrCol      int
	AnalyzeErrMsg      string
	ParserTokens       []ByteSemanticToken
	ResolvedTokens     []ByteSemanticToken
	ScanLintIssues     []ScanLintIssue
	ScannedTables      []TableCostBreakdown
	ASTTableRefs       [][]string
	DryRunEligible     bool
	UnresolvedRefs     []catalog.TableRef
	PendingRefs        []catalog.TableRef
	DeclaredVars       []DeclaredVarInfo
	TempTables         []TempTableInfo
	HasExprPlaceholder bool
}

// BranchAnalysis holds secondary {% elif %} / {% else %} AST tokens and syntax errors.
type BranchAnalysis struct {
	Branch       syntax.BranchSpan
	OrigTokens   []ByteSemanticToken
	ParseErrMsg  string
	ParseErrOrig int
	HasParseErr  bool
}

// DocumentSnapshot is an immutable analysis snapshot for a single document version.
type DocumentSnapshot struct {
	URI               string
	Version           int
	Text              string
	Options           *bqconfig.LSPOptions
	Store             *catalog.Store
	LineIndex         *syntax.LineIndex
	Tokens            []syntax.Token
	Projected         syntax.ProjectedSQL
	Statements        []*AnalyzedStatement
	Branches          []BranchAnalysis
	DeclaredVars      []DeclaredVarInfo
	TempTables        []TempTableInfo
	HasPendingTables  bool
	HasUnresolvedRefs bool
	AllByteTokens     []ByteSemanticToken
	AbsTokens         []AbsSemanticToken
	TokensEncoded     []uint32
	Diagnostics       []Diagnostic
}

// NewEngine creates an incremental analysis Engine using defaultStore and defaultOpts.
func NewEngine(defaultStore *catalog.Store, defaultOpts *bqconfig.LSPOptions) (*Engine, error) {
	opts := cloneLSPOptions(defaultOpts)
	if defaultStore == nil {
		var err error
		defaultStore, err = catalog.NewStore(opts)
		if err != nil {
			return nil, fmt.Errorf("NewEngine store: %w", err)
		}
	}

	if err := bqlang.Init(); err != nil {
		return nil, fmt.Errorf("NewEngine bqlang.Init: %w", err)
	}

	wasmMu.Lock()
	parserOpts, err := googlesql.NewParserOptions()
	if err == nil {
		err = parserOpts.SetLanguageOptions(catalog.DefaultLSPLanguageOptions())
	}
	wasmMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("NewEngine parser options: %w", err)
	}

	return &Engine{
		defaultStore: defaultStore,
		defaultOpts:  opts,
		parserOpts:   parserOpts,
		docs:         make(map[string]*docEntry),
	}, nil
}

func cloneLSPOptions(opts *bqconfig.LSPOptions) *bqconfig.LSPOptions {
	if opts == nil {
		return bqconfig.DefaultLSPOptions()
	}

	cp := *opts
	cp.SchemaDirs = slices.Clone(opts.SchemaDirs)
	cp.ProjectCacheTTLSeconds = maps.Clone(opts.ProjectCacheTTLSeconds)

	return &cp
}

func (e *Engine) normalizeConfig(cfg *DocumentConfig) DocumentConfig {
	out := DocumentConfig{
		Store:   e.defaultStore,
		Options: e.defaultOpts,
	}
	if cfg == nil {
		return out
	}

	if cfg.Store != nil {
		out.Store = cfg.Store
	}

	if cfg.Options != nil {
		out.Options = cloneLSPOptions(cfg.Options)
	}

	return out
}

// OpenDocument registers or replaces an open document and runs initial analysis.
func (e *Engine) OpenDocument(uri, text string, version int, cfg *DocumentConfig) {
	docCfg := e.normalizeConfig(cfg)
	lineIdx := syntax.NewLineIndex(text)
	tokens := syntax.Scan(text)
	proj := syntax.ProjectSQL(text, tokens)

	entry := &docEntry{
		uri:           uri,
		version:       version,
		text:          text,
		cfg:           docCfg,
		lineIdx:       lineIdx,
		tokens:        tokens,
		projected:     proj,
		astCache:      make(map[[32]byte]*cachedAST),
		resolvedCache: make(map[resolvedCacheKey]*cachedResolved),
	}

	entry.mu.Lock()
	e.docsMu.Lock()
	e.docs[uri] = entry
	e.docsMu.Unlock()

	e.analyzeEntryLocked(entry)
	entry.mu.Unlock()
}

// ChangeDocument applies incremental or full text changes and re-analyzes the document.
func (e *Engine) ChangeDocument(uri string, version int, changes []syntax.TextChange) error {
	entry, ok := e.lookupEntry(uri)
	if !ok {
		return ErrDocumentNotOpen
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()

	curText := entry.text
	curIdx := entry.lineIdx
	lkg := entry.lkgTokens

	for _, ch := range changes {
		curText, curIdx, lkg = applySingleChange(curText, curIdx, lkg, ch)
	}

	entry.version = version
	entry.text = curText
	entry.lineIdx = curIdx
	entry.lkgTokens = lkg
	entry.tokens = syntax.Scan(curText)
	entry.projected = syntax.ProjectSQL(curText, entry.tokens)
	entry.snapshot = nil

	e.analyzeEntryLocked(entry)

	return nil
}

func applySingleChange(
	curText string,
	curIdx *syntax.LineIndex,
	lkg []ByteSemanticToken,
	ch syntax.TextChange,
) (string, *syntax.LineIndex, []ByteSemanticToken) {
	if curIdx == nil {
		curIdx = syntax.NewLineIndex(curText)
	}

	if ch.Range != nil {
		start, end := curIdx.ByteRange(*ch.Range)
		lkg = shiftByteTokens(lkg, start, end, len(ch.Text))
		nextText := curText[:start] + ch.Text + curText[end:]

		return nextText, syntax.NewLineIndex(nextText), lkg
	}

	editStart, editEnd, insLen := diffReplacementBounds(curText, ch.Text)
	lkg = shiftByteTokens(lkg, editStart, editEnd, insLen)

	return ch.Text, syntax.NewLineIndex(ch.Text), lkg
}

func diffReplacementBounds(oldText, newText string) (editStart, editEnd, insLen int) {
	minLen := min(len(oldText), len(newText))
	prefix := 0
	for prefix < minLen && oldText[prefix] == newText[prefix] {
		prefix++
	}

	oldRem := len(oldText) - prefix
	newRem := len(newText) - prefix
	maxSuffix := min(oldRem, newRem)
	suffix := 0
	for suffix < maxSuffix && oldText[len(oldText)-1-suffix] == newText[len(newText)-1-suffix] {
		suffix++
	}

	return prefix, len(oldText) - suffix, len(newText) - prefix - suffix
}

// UpdateDocumentConfig updates the catalog store or LSP options for an open document.
func (e *Engine) UpdateDocumentConfig(uri string, cfg *DocumentConfig) error {
	entry, ok := e.lookupEntry(uri)
	if !ok {
		return ErrDocumentNotOpen
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()

	entry.cfg = e.normalizeConfig(cfg)
	clear(entry.resolvedCache)
	entry.resolvedOrder = nil
	entry.snapshot = nil

	e.analyzeEntryLocked(entry)

	return nil
}

// CloseDocument removes uri and its cached analysis state from the engine.
func (e *Engine) CloseDocument(uri string) {
	e.docsMu.Lock()
	delete(e.docs, uri)
	e.docsMu.Unlock()
}

// DocumentText returns the latest source text for uri.
func (e *Engine) DocumentText(uri string) (string, bool) {
	entry, ok := e.lookupEntry(uri)
	if !ok {
		return "", false
	}

	entry.mu.Lock()
	text := entry.text
	entry.mu.Unlock()

	return text, true
}

// LineIndex returns the latest UTF-16 LineIndex for uri.
func (e *Engine) LineIndex(uri string) (*syntax.LineIndex, bool) {
	entry, ok := e.lookupEntry(uri)
	if !ok {
		return nil, false
	}

	entry.mu.Lock()
	idx := entry.lineIdx
	entry.mu.Unlock()

	return idx, true
}

// InvalidateCatalog notifies the engine that cached catalog metadata changed.
func (e *Engine) InvalidateCatalog(_ catalog.TableRef) {
	e.catalogGen.Add(1)
}

// InvalidateCost notifies the engine that cached dry-run cost estimates changed
// so cost diagnostics can refresh without re-running WASM SQL analysis.
func (e *Engine) InvalidateCost() {
	e.costGen.Add(1)
}

// UnresolvedTableRefs returns deduplicated TableRefs that missed catalog lookup
// during the latest analysis of uri (useful for CLI tools awaiting async fetches).
func (e *Engine) UnresolvedTableRefs(uri string) ([]catalog.TableRef, error) {
	_, snap, err := e.ensureSnapshot(uri)
	if err != nil {
		return nil, err
	}

	var refs []catalog.TableRef
	for _, st := range snap.Statements {
		refs = appendUniqueTableRefs(refs, st.UnresolvedRefs)
		refs = appendUniqueTableRefs(refs, st.PendingRefs)
	}

	return refs, nil
}

func (e *Engine) lookupEntry(uri string) (*docEntry, bool) {
	e.docsMu.RLock()
	entry, ok := e.docs[uri]
	e.docsMu.RUnlock()

	return entry, ok
}

func (e *Engine) ensureSnapshot(uri string) (*docEntry, *DocumentSnapshot, error) {
	entry, ok := e.lookupEntry(uri)
	if !ok {
		return nil, nil, ErrDocumentNotOpen
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()

	gen := e.catalogGen.Load()
	costGen := e.costGen.Load()
	if entry.snapshot == nil || entry.analyzedGen != gen {
		e.analyzeEntryLocked(entry)
	} else if entry.analyzedCostGen != costGen {
		cp := *entry.snapshot
		cp.Diagnostics = computeDiagnostics(&cp)
		entry.snapshot = &cp
		entry.analyzedCostGen = costGen
	}

	return entry, entry.snapshot, nil
}

type scriptState struct {
	store        *catalog.Store
	tf           *googlesql.TypeFactory
	overlay      *catalog.ScriptOverlay
	analyzerOpts *googlesql.AnalyzerOptions
	cat          *catalog.Catalog
	declaredVars map[string]DeclaredVarInfo
	allDeclVars  []DeclaredVarInfo
	allTempTbls  []TempTableInfo
	overlayHash  [32]byte
}

func newScriptState(store *catalog.Store) (*scriptState, error) {
	tf, err := googlesql.NewTypeFactory()
	if err != nil {
		return nil, err
	}

	overlay, err := catalog.NewScriptOverlay(tf)
	if err != nil {
		return nil, err
	}

	analyzerOpts, err := googlesql.NewAnalyzerOptions(catalog.DefaultLSPLanguageOptions())
	if err != nil {
		return nil, err
	}

	if err = analyzerOpts.SetRecordParseLocations(true); err != nil {
		return nil, err
	}

	if err = analyzerOpts.SetCreateNewColumnForEachProjectedOutput(true); err != nil {
		return nil, err
	}

	if err = analyzerOpts.SetErrorMessageMode(googlesql.ErrorMessageModeErrorMessageMultiLineWithCaret); err != nil {
		return nil, err
	}

	if err = analyzerOpts.SetAllowUndeclaredParameters(true); err != nil {
		return nil, err
	}

	if err = analyzerOpts.SetParameterMode(googlesql.ParameterModeParameterNamed); err != nil {
		return nil, err
	}

	if err = catalog.RegisterBigQuerySystemVariables(analyzerOpts, tf); err != nil {
		return nil, err
	}

	cat, err := catalog.NewCatalogForAnalysis(store, catalog.DefaultLSPLanguageOptions(), overlay)
	if err != nil {
		return nil, err
	}

	return &scriptState{
		store:        store,
		tf:           tf,
		overlay:      overlay,
		analyzerOpts: analyzerOpts,
		cat:          cat,
		declaredVars: make(map[string]DeclaredVarInfo),
	}, nil
}

func (s *scriptState) rebuildCatalog() error {
	cat, err := catalog.NewCatalogForAnalysis(s.store, catalog.DefaultLSPLanguageOptions(), s.overlay)
	if err != nil {
		return err
	}

	s.cat = cat

	return nil
}

func (s *scriptState) updateOverlayDigest() {
	h := sha256.New()
	for _, dv := range s.allDeclVars {
		h.Write([]byte("V:"))
		h.Write([]byte(strings.ToLower(dv.Name)))
		h.Write([]byte("="))
		h.Write([]byte(dv.TypeSQL))
		h.Write([]byte{0})
	}

	for _, tt := range s.allTempTbls {
		h.Write([]byte("T:"))
		h.Write([]byte(strings.ToLower(tt.Ref.FullName())))
		if tt.Meta != nil {
			for i := range tt.Meta.Schema {
				h.Write([]byte(":"))
				h.Write([]byte(strings.ToLower(tt.Meta.Schema[i].Name)))
				h.Write([]byte("/"))
				h.Write([]byte(tt.Meta.Schema[i].SQLType()))
			}
		}
		h.Write([]byte{0})
	}

	var countBuf [8]byte
	binary.LittleEndian.PutUint64(countBuf[:], uint64(len(s.allDeclVars)+len(s.allTempTbls)))
	h.Write(countBuf[:])
	copy(s.overlayHash[:], h.Sum(nil))
}

func (e *Engine) analyzeEntryLocked(entry *docEntry) {
	gen := e.catalogGen.Load()
	proj := entry.projected
	spans := syntax.SplitStatements(proj.SQL, nil)

	wasmMu.Lock()
	ss, err := newScriptState(entry.cfg.Store)
	stmts := make([]*AnalyzedStatement, 0, len(spans))
	if err == nil {
		stmts = e.analyzeStatementsLocked(entry, proj, spans, gen, ss)
	}
	prewarmBrokenStatementTables(entry.cfg.Store, stmts)
	branches := e.analyzeBranchesLocked(entry, proj)
	hasPending, hasUnresolved := collectCatalogStatus(ss, stmts)
	wasmMu.Unlock()

	snap := &DocumentSnapshot{
		URI:               entry.uri,
		Version:           entry.version,
		Text:              entry.text,
		Options:           entry.cfg.Options,
		Store:             entry.cfg.Store,
		LineIndex:         entry.lineIdx,
		Tokens:            entry.tokens,
		Projected:         proj,
		Statements:        stmts,
		Branches:          branches,
		DeclaredVars:      ssVars(ss),
		TempTables:        ssTables(ss),
		HasPendingTables:  hasPending,
		HasUnresolvedRefs: hasUnresolved,
	}

	buildSemanticTokens(snap, entry.lkgTokens)
	snap.Diagnostics = computeDiagnostics(snap)

	if len(snap.AllByteTokens) > 0 && allStatementsParsed(stmts) {
		entry.lkgTokens = slicesCloneByteTokens(snap.AllByteTokens)
	} else if len(snap.AllByteTokens) > 0 && len(entry.lkgTokens) == 0 {
		entry.lkgTokens = slicesCloneByteTokens(snap.AllByteTokens)
	}

	entry.analyzedGen = gen
	entry.analyzedCostGen = e.costGen.Load()
	entry.snapshot = snap
}

func ssVars(ss *scriptState) []DeclaredVarInfo {
	if ss == nil {
		return nil
	}

	return append([]DeclaredVarInfo(nil), ss.allDeclVars...)
}

func ssTables(ss *scriptState) []TempTableInfo {
	if ss == nil {
		return nil
	}

	return append([]TempTableInfo(nil), ss.allTempTbls...)
}

func collectCatalogStatus(ss *scriptState, stmts []*AnalyzedStatement) (pending, unresolved bool) {
	if ss != nil && ss.cat != nil {
		pending = ss.cat.HasPendingTables()
		unresolved = len(ss.cat.UnresolvedRefs()) > 0
	}

	for _, st := range stmts {
		if len(st.PendingRefs) > 0 {
			pending = true
		}

		if len(st.UnresolvedRefs) > 0 {
			unresolved = true
		}
	}

	return pending, unresolved
}

func allStatementsParsed(stmts []*AnalyzedStatement) bool {
	for _, st := range stmts {
		if st.ParseErr != nil {
			return false
		}
	}

	return true
}

func slicesCloneByteTokens(in []ByteSemanticToken) []ByteSemanticToken {
	if len(in) == 0 {
		return nil
	}

	out := make([]ByteSemanticToken, len(in))
	copy(out, in)

	return out
}

func (e *Engine) analyzeStatementsLocked(
	entry *docEntry,
	proj syntax.ProjectedSQL,
	spans []syntax.StatementSpan,
	gen uint64,
	ss *scriptState,
) []*AnalyzedStatement {
	stmts := make([]*AnalyzedStatement, 0, len(spans))
	for _, sp := range spans {
		stmtSQL := proj.SQL[sp.Start:sp.End]
		if strings.TrimSpace(stmtSQL) == "" {
			continue
		}

		stmtHash := sha256.Sum256([]byte(stmtSQL))
		cachedAST := e.getOrParseASTLocked(entry, stmtSQL, stmtHash)
		origStart, origEnd := shadowSpanToBestOrig(proj.OffsetMap, sp.Start, sp.End)

		st := &AnalyzedStatement{
			ShadowSpan:     syntax.ByteSpan{Start: sp.Start, End: sp.End},
			OrigSpan:       syntax.ByteSpan{Start: origStart, End: origEnd},
			SQL:            stmtSQL,
			Hash:           stmtHash,
			ASTStmt:        cachedAST.astStmt,
			ParseErr:       cachedAST.parseErr,
			ParseErrLine:   cachedAST.errLine,
			ParseErrCol:    cachedAST.errCol,
			ParseErrMsg:    cachedAST.errMsg,
			ParserTokens:   cachedAST.parserTokens,
			DryRunEligible: cachedAST.dryRunEligible,
			ASTTableRefs:   cachedAST.astTableRefs,
		}

		if cachedAST.parseErr == nil && !isNilNode(cachedAST.astStmt) {
			e.analyzeCachedStatementLocked(entry, proj.OffsetMap, sp.Start, st, gen, ss)
		}

		stmts = append(stmts, st)
	}

	return stmts
}

func shadowSpanToBestOrig(om *syntax.OffsetMap, sStart, sEnd int) (int, int) {
	if om == nil {
		return sStart, sEnd
	}

	os, _ := om.ShadowByteToOrig(sStart)
	oe, _ := om.ShadowByteToOrig(sEnd)
	if oe < os {
		oe = os
	}

	return os, oe
}

func (e *Engine) getOrParseASTLocked(entry *docEntry, stmtSQL string, stmtHash [32]byte) *cachedAST {
	if c, ok := entry.astCache[stmtHash]; ok {
		return c
	}

	out, astStmt, parseErr := e.parseSingleStatementWASM(stmtSQL)
	var (
		errLine, errCol int
		errMsg          string
		parserTokens    []ByteSemanticToken
		dryRunEligible  bool
		astTableRefs    [][]string
	)

	if parseErr != nil {
		errLine, errCol, errMsg = parseGoogleSQLError(parseErr)
	} else if !isNilNode(astStmt) {
		parserTokens = extractASTTokens(stmtSQL, astStmt)
		dryRunEligible = isDryRunEligibleAST(astStmt)
		astTableRefs = collectASTTablePartsList(astStmt)
	}

	c := &cachedAST{
		sql:            stmtSQL,
		parserOut:      out,
		astStmt:        astStmt,
		parseErr:       parseErr,
		errLine:        errLine,
		errCol:         errCol,
		errMsg:         errMsg,
		parserTokens:   parserTokens,
		dryRunEligible: dryRunEligible,
		astTableRefs:   astTableRefs,
	}

	putASTCacheLocked(entry, stmtHash, c)

	return c
}

func (e *Engine) parseSingleStatementWASM(stmtSQL string) (*googlesql.ParserOutput, googlesql.ASTStatementNode, error) {
	resume, err := googlesql.NewParseResumeLocationFromString(stmtSQL)
	if err != nil {
		return nil, nil, err
	}

	out, err := googlesql.ParseNextScriptStatement(resume, e.parserOpts)
	if err != nil {
		if !googleSQLErrLocRe.MatchString(err.Error()) {
			if _, scriptErr := googlesql.ParseScript(stmtSQL, e.parserOpts, bqlang.DefaultErrorMessageOptions()); scriptErr != nil {
				return nil, nil, scriptErr
			}
		}

		return nil, nil, err
	}

	stmt, stmtErr := out.Statement()
	if stmtErr != nil || isNilNode(stmt) {
		return out, nil, stmtErr
	}

	return out, stmt, nil
}

func putASTCacheLocked(entry *docEntry, key [32]byte, val *cachedAST) {
	if _, exists := entry.astCache[key]; !exists {
		if len(entry.astOrder) >= maxStmtCacheEntries {
			evict := entry.astOrder[0]
			entry.astOrder = entry.astOrder[1:]
			delete(entry.astCache, evict)
		}

		entry.astOrder = append(entry.astOrder, key)
	}

	entry.astCache[key] = val
}

func (e *Engine) analyzeCachedStatementLocked(
	entry *docEntry,
	om *syntax.OffsetMap,
	shadowStart int,
	st *AnalyzedStatement,
	gen uint64,
	ss *scriptState,
) {
	key := resolvedCacheKey{
		stmtHash:       st.Hash,
		catalogGen:     gen,
		overlayDigest:  ss.overlayHash,
		defaultProject: entry.cfg.Options.DefaultProject,
		defaultDataset: entry.cfg.Options.DefaultDataset,
	}

	if cached, ok := entry.resolvedCache[key]; ok && !anyUnresolvedNowCached(ss.store, cached.unresolvedRefs) {
		applyCachedResolvedToStmt(st, cached)
		replayOverlayFromCache(ss, cached)

		return
	}

	if strings.Contains(st.SQL, "__bqpls_expr_") {
		st.HasExprPlaceholder = true

		return
	}

	res := &cachedResolved{}
	e.processParsedStatementWASM(st.SQL, om, shadowStart, st.ASTStmt, ss, res)
	putResolvedCacheLocked(entry, key, res)
	applyCachedResolvedToStmt(st, res)
}

func anyUnresolvedNowCached(store *catalog.Store, refs []catalog.TableRef) bool {
	if store == nil || len(refs) == 0 {
		return false
	}

	for _, ref := range refs {
		if _, ok := store.GetTable(ref); ok {
			return true
		}
	}

	return false
}

func applyCachedResolvedToStmt(st *AnalyzedStatement, res *cachedResolved) {
	st.ResolvedStmt = res.resolvedStmt
	st.ResolvedStmts = res.resolvedStmts
	st.InnerResults = res.innerResults
	st.Catalog = res.catalog
	st.AnalyzeErr = res.analyzeErr
	st.AnalyzeErrLine = res.errLine
	st.AnalyzeErrCol = res.errCol
	st.AnalyzeErrMsg = res.errMsg
	st.ResolvedTokens = res.resolvedTokens
	st.ScanLintIssues = res.scanLintIssues
	st.ScannedTables = res.scannedTables
	st.UnresolvedRefs = res.unresolvedRefs
	st.PendingRefs = res.pendingRefs
	st.DeclaredVars = res.declaredVars
	st.TempTables = res.tempTables
	st.HasExprPlaceholder = res.hasExprPlaceholder
}

func replayOverlayFromCache(ss *scriptState, res *cachedResolved) {
	replayScriptOverlay(ss, res.declaredVars, res.tempTables)
}

func replayScriptOverlay(ss *scriptState, vars []DeclaredVarInfo, tables []TempTableInfo) {
	changedVar := false
	for _, dv := range vars {
		typNode, err := googlesql.AnalyzeType(dv.TypeSQL, ss.analyzerOpts, ss.cat.GoogleSQLCatalog(), ss.tf)
		if err == nil && !isNilNode(typNode) {
			if addErr := ss.overlay.AddVariable(dv.Name, typNode); addErr == nil {
				ss.declaredVars[strings.ToLower(dv.Name)] = dv
				ss.allDeclVars = append(ss.allDeclVars, dv)
				changedVar = true
			}
		}
	}

	for _, tt := range tables {
		if tt.Meta != nil {
			_ = ss.overlay.AddTableMeta(tt.Meta)
			ss.allTempTbls = append(ss.allTempTbls, tt)
		}
	}

	if len(vars) > 0 || len(tables) > 0 {
		ss.updateOverlayDigest()
	}

	if changedVar {
		_ = ss.rebuildCatalog()
	}
}

func putResolvedCacheLocked(entry *docEntry, key resolvedCacheKey, val *cachedResolved) {
	if _, exists := entry.resolvedCache[key]; !exists {
		if len(entry.resolvedOrder) >= maxStmtCacheEntries {
			evict := entry.resolvedOrder[0]
			entry.resolvedOrder = entry.resolvedOrder[1:]
			delete(entry.resolvedCache, evict)
		}

		entry.resolvedOrder = append(entry.resolvedOrder, key)
	}

	entry.resolvedCache[key] = val
}

func (e *Engine) processParsedStatementWASM(
	stmtSQL string,
	om *syntax.OffsetMap,
	shadowStart int,
	astStmt googlesql.ASTStatementNode,
	ss *scriptState,
	res *cachedResolved,
) {
	if isNilNode(astStmt) {
		return
	}

	if varDecl, ok := astStmt.(*googlesql.ASTVariableDeclaration); ok {
		handleVariableDeclaration(stmtSQL, om, shadowStart, varDecl, ss, res)

		return
	}

	if handled := e.recurseScriptControlStmtWASM(stmtSQL, om, shadowStart, astStmt, ss, res); handled {
		return
	}

	isSQL, err := astStmt.IsSqlStatement()
	if err != nil || !isSQL {
		return
	}

	analyzeSQLStatementWASM(stmtSQL, om, shadowStart, astStmt, ss, res)
}

func (e *Engine) recurseScriptControlStmtWASM(
	stmtSQL string,
	om *syntax.OffsetMap,
	shadowStart int,
	astStmt googlesql.ASTStatementNode,
	ss *scriptState,
	res *cachedResolved,
) bool {
	switch s := astStmt.(type) {
	case *googlesql.ASTBeginEndBlock:
		e.walkBeginEndBlock(stmtSQL, om, shadowStart, s, ss, res)
		return true
	case *googlesql.ASTIfStatement:
		e.walkIfStatement(stmtSQL, om, shadowStart, s, ss, res)
		return true
	case *googlesql.ASTCaseStatement:
		e.walkCaseStatement(stmtSQL, om, shadowStart, s, ss, res)
		return true
	case *googlesql.ASTWhileStatement:
		body, _ := s.Body()
		e.walkStatementList(stmtSQL, om, shadowStart, body, ss, res)
		return true
	case *googlesql.ASTRepeatStatement:
		body, _ := s.Body()
		e.walkStatementList(stmtSQL, om, shadowStart, body, ss, res)
		return true
	case *googlesql.ASTForInStatement:
		body, _ := s.Body()
		e.walkStatementList(stmtSQL, om, shadowStart, body, ss, res)
		return true
	case *googlesql.ASTLoopStatement:
		body, _ := s.Body()
		e.walkStatementList(stmtSQL, om, shadowStart, body, ss, res)
		return true
	default:
		return false
	}
}

func (e *Engine) walkBeginEndBlock(
	stmtSQL string,
	om *syntax.OffsetMap,
	shadowStart int,
	blk *googlesql.ASTBeginEndBlock,
	ss *scriptState,
	res *cachedResolved,
) {
	sl, _ := blk.StatementListNode()
	e.walkStatementList(stmtSQL, om, shadowStart, sl, ss, res)

	hl, _ := blk.HandlerList()
	if isNilNode(hl) {
		return
	}

	n := astNumChildren(hl)
	for i := range n {
		if h, ok := astChild(hl, i).(*googlesql.ASTExceptionHandler); ok && !isNilNode(h) {
			hsl, _ := h.StatementList()
			e.walkStatementList(stmtSQL, om, shadowStart, hsl, ss, res)
		}
	}
}

func (e *Engine) walkIfStatement(
	stmtSQL string,
	om *syntax.OffsetMap,
	shadowStart int,
	ifStmt *googlesql.ASTIfStatement,
	ss *scriptState,
	res *cachedResolved,
) {
	thenList, _ := ifStmt.ThenList()
	e.walkStatementList(stmtSQL, om, shadowStart, thenList, ss, res)

	elseifs, _ := ifStmt.ElseifClauses()
	if !isNilNode(elseifs) {
		n := astNumChildren(elseifs)
		for i := range n {
			if ec, ok := astChild(elseifs, i).(*googlesql.ASTElseifClause); ok && !isNilNode(ec) {
				body, _ := ec.Body()
				e.walkStatementList(stmtSQL, om, shadowStart, body, ss, res)
			}
		}
	}

	elseList, _ := ifStmt.ElseList()
	e.walkStatementList(stmtSQL, om, shadowStart, elseList, ss, res)
}

func (e *Engine) walkCaseStatement(
	stmtSQL string,
	om *syntax.OffsetMap,
	shadowStart int,
	caseStmt *googlesql.ASTCaseStatement,
	ss *scriptState,
	res *cachedResolved,
) {
	whens, _ := caseStmt.WhenThenClauses()
	if !isNilNode(whens) {
		n := astNumChildren(whens)
		for i := range n {
			if wc, ok := astChild(whens, i).(*googlesql.ASTWhenThenClause); ok && !isNilNode(wc) {
				body, _ := wc.Body()
				e.walkStatementList(stmtSQL, om, shadowStart, body, ss, res)
			}
		}
	}

	elseList, _ := caseStmt.ElseList()
	e.walkStatementList(stmtSQL, om, shadowStart, elseList, ss, res)
}

func (e *Engine) walkStatementList(
	stmtSQL string,
	om *syntax.OffsetMap,
	shadowStart int,
	sl *googlesql.ASTStatementList,
	ss *scriptState,
	res *cachedResolved,
) {
	if isNilNode(sl) {
		return
	}

	n := astNumChildren(sl)
	for i := range n {
		if childStmt, ok := astChild(sl, i).(googlesql.ASTStatementNode); ok && !isNilNode(childStmt) {
			e.processParsedStatementWASM(stmtSQL, om, shadowStart, childStmt, ss, res)
		}
	}
}

func handleVariableDeclaration(
	stmtSQL string,
	om *syntax.OffsetMap,
	shadowStart int,
	decl *googlesql.ASTVariableDeclaration,
	ss *scriptState,
	res *cachedResolved,
) {
	varList, err := decl.VariableList()
	if err != nil || isNilNode(varList) {
		return
	}

	typeSQL, typeName, resolvedType := resolveDeclaredVarType(stmtSQL, decl, ss)
	added := false
	n := astNumChildren(varList)
	for i := range n {
		id, ok := astChild(varList, i).(*googlesql.ASTIdentifier)
		if !ok || isNilNode(id) {
			continue
		}

		if registerSingleDeclaredVar(om, shadowStart, id, typeSQL, typeName, resolvedType, ss, res) {
			added = true
		}
	}

	if added {
		ss.updateOverlayDigest()
		_ = ss.rebuildCatalog()
	}
}

func resolveDeclaredVarType(
	stmtSQL string,
	decl *googlesql.ASTVariableDeclaration,
	ss *scriptState,
) (typeSQL, typeName string, resolvedType googlesql.Googlesql_TypeNode) {
	typeNode, err := decl.Type()
	if err == nil && !isNilNode(typeNode) {
		if ts, te, ok := astNodeByteRange(typeNode); ok && te <= len(stmtSQL) {
			typeSQL = strings.TrimSpace(stmtSQL[ts:te])
		}
	}

	if typeSQL != "" {
		rt, rtErr := googlesql.AnalyzeType(typeSQL, ss.analyzerOpts, ss.cat.GoogleSQLCatalog(), ss.tf)
		if rtErr == nil && !isNilNode(rt) {
			resolvedType = rt
			typeName = formatGoogleSQLType(rt)
		}
	}
	if typeName == "" {
		typeName = typeSQL
	}

	return typeSQL, typeName, resolvedType
}

func registerSingleDeclaredVar(
	om *syntax.OffsetMap,
	shadowStart int,
	id *googlesql.ASTIdentifier,
	typeSQL, typeName string,
	resolvedType googlesql.Googlesql_TypeNode,
	ss *scriptState,
	res *cachedResolved,
) bool {
	name, err := id.GetAsString()
	if err != nil || name == "" {
		return false
	}

	ls, le, _ := astNodeByteRange(id)
	os, oe := shadowSpanToBestOrig(om, shadowStart+ls, shadowStart+le)
	info := DeclaredVarInfo{
		Name:       name,
		TypeSQL:    typeSQL,
		TypeName:   typeName,
		LocalStart: ls,
		LocalEnd:   le,
		OrigSpan:   syntax.ByteSpan{Start: os, End: oe},
	}

	added := false
	if !isNilNode(resolvedType) {
		if addErr := ss.overlay.AddVariable(name, resolvedType); addErr == nil {
			added = true
		}
	}

	ss.declaredVars[strings.ToLower(name)] = info
	ss.allDeclVars = append(ss.allDeclVars, info)
	res.declaredVars = append(res.declaredVars, info)

	return added
}

func analyzeSQLStatementWASM(
	stmtSQL string,
	om *syntax.OffsetMap,
	shadowStart int,
	astStmt googlesql.ASTStatementNode,
	ss *scriptState,
	res *cachedResolved,
) {
	beforeUnresolved := tableRefKeySet(ss.cat.UnresolvedRefs())
	beforePending := tableRefKeySet(ss.cat.PendingRefs())

	out, err := googlesql.AnalyzeStatementFromParserAST(
		astStmt,
		ss.analyzerOpts,
		stmtSQL,
		ss.cat.GoogleSQLCatalog(),
		ss.tf,
	)

	stmtRefs := collectStatementTableRefKeys(astStmt, ss.store.DefaultProject(), ss.store.DefaultDataset())
	stmtUnresolved := diffOrReferencedRefs(ss.cat.UnresolvedRefs(), beforeUnresolved, stmtRefs)
	stmtPending := diffOrReferencedRefs(ss.cat.PendingRefs(), beforePending, stmtRefs)
	res.unresolvedRefs = appendUniqueTableRefs(res.unresolvedRefs, stmtUnresolved)
	res.pendingRefs = appendUniqueTableRefs(res.pendingRefs, stmtPending)

	if err != nil {
		line, col, msg := parseGoogleSQLError(err)
		if res.analyzeErr == nil {
			res.analyzeErr = err
			res.errLine = line
			res.errCol = col
			res.errMsg = msg
		}

		res.innerResults = append(res.innerResults, InnerAnalyzeResult{
			AnalyzeErr:         err,
			ErrLine:            line,
			ErrCol:             col,
			ErrMsg:             msg,
			UnresolvedRefs:     stmtUnresolved,
			PendingRefs:        stmtPending,
			HasExprPlaceholder: strings.Contains(msg, "__bqpls_expr_"),
		})

		return
	}

	res.innerResults = append(res.innerResults, InnerAnalyzeResult{
		UnresolvedRefs: stmtUnresolved,
		PendingRefs:    stmtPending,
	})
	res.analyzerOut = out
	res.analyzerOuts = append(res.analyzerOuts, out)
	res.catalog = ss.cat

	rs, rsErr := out.ResolvedStatement()
	if rsErr != nil || isNilNode(rs) {
		return
	}

	res.resolvedStmt = rs
	res.resolvedStmts = append(res.resolvedStmts, rs)
	res.resolvedTokens = append(res.resolvedTokens, extractResolvedTokens(stmtSQL, rs, ss.cat)...)
	res.scanLintIssues = append(res.scanLintIssues, collectScanLintIssues(stmtSQL, astStmt, rs, ss.cat)...)
	res.scannedTables = append(res.scannedTables, collectResolvedScannedTables(rs, ss.cat)...)
	registerTempTableFromResolved(om, shadowStart, astStmt, rs, ss, res)
}

func tableRefKeySet(refs []catalog.TableRef) map[string]struct{} {
	if len(refs) == 0 {
		return nil
	}

	out := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		out[strings.ToLower(ref.FullName())] = struct{}{}
	}

	return out
}

func collectStatementTableRefKeys(astStmt googlesql.ASTNode, defProj, defDS string) map[string]struct{} {
	if isNilNode(astStmt) {
		return nil
	}

	keys := make(map[string]struct{})
	walkASTNodes(astStmt, func(n googlesql.ASTNode) bool {
		tp, ok := n.(*googlesql.ASTTablePathExpression)
		if !ok || isNilNode(tp) {
			return true
		}

		pe, err := tp.PathExpr()
		if err != nil || isNilNode(pe) {
			return true
		}

		parts, _ := pe.ToIdentifierVector()
		ref := tableRefFromPath(normalizeASTTableParts(parts), defProj, defDS)
		if ref.Table != "" {
			keys[strings.ToLower(ref.FullName())] = struct{}{}
		}

		return true
	})

	return keys
}

func normalizeASTTableParts(parts []string) []string {
	cleaned := make([]string, 0, len(parts))
	for _, raw := range parts {
		if seg := strings.Trim(raw, "`"); seg != "" {
			cleaned = append(cleaned, seg)
		}
	}

	if len(cleaned) != 1 {
		return cleaned
	}

	var split []string
	for seg := range strings.SplitSeq(cleaned[0], ".") {
		if seg = strings.Trim(seg, "`"); seg != "" {
			split = append(split, seg)
		}
	}

	if len(split) > 3 {
		return []string{
			strings.Join(split[:len(split)-2], "."),
			split[len(split)-2],
			split[len(split)-1],
		}
	}

	return split
}

func diffOrReferencedRefs(
	allRefs []catalog.TableRef,
	beforeSet map[string]struct{},
	stmtRefs map[string]struct{},
) []catalog.TableRef {
	var out []catalog.TableRef
	for _, ref := range allRefs {
		key := strings.ToLower(ref.FullName())
		_, existed := beforeSet[key]
		_, inStmt := stmtRefs[key]
		if !existed || inStmt {
			out = append(out, ref)
		}
	}

	return out
}

func appendUniqueTableRefs(dst, add []catalog.TableRef) []catalog.TableRef {
	if len(add) == 0 {
		return dst
	}

	seen := tableRefKeySet(dst)
	if seen == nil {
		seen = make(map[string]struct{}, len(add))
	}

	for _, ref := range add {
		key := strings.ToLower(ref.FullName())
		if _, exists := seen[key]; exists {
			continue
		}

		seen[key] = struct{}{}
		dst = append(dst, ref)
	}

	return dst
}

func registerTempTableFromResolved(
	om *syntax.OffsetMap,
	shadowStart int,
	astStmt googlesql.ASTStatementNode,
	rs googlesql.ResolvedStatementNode,
	ss *scriptState,
	res *cachedResolved,
) {
	var namePath []string
	var cols []catalog.OverlayColumn

	switch stmt := rs.(type) {
	case *googlesql.ResolvedCreateTableAsSelectStmt:
		namePath, _ = stmt.NamePath()
		outCols, _ := stmt.OutputColumnList()
		for _, oc := range outCols {
			if isNilNode(oc) {
				continue
			}
			nm, _ := oc.Name()
			rc, _ := oc.Column()
			if !isNilNode(rc) {
				tp, _ := rc.Type()
				cols = append(cols, catalog.OverlayColumn{Name: nm, Type: tp})
			}
		}
	case *googlesql.ResolvedCreateTableStmt:
		namePath, _ = stmt.NamePath()
		colDefs, _ := stmt.ColumnDefinitionList()
		for _, cd := range colDefs {
			if isNilNode(cd) {
				continue
			}
			nm, _ := cd.Name()
			tp, _ := cd.Type()
			cols = append(cols, catalog.OverlayColumn{Name: nm, Type: tp})
		}
	default:
		return
	}

	ref := tableRefFromPath(namePath, ss.store.DefaultProject(), ss.store.DefaultDataset())
	if ref.Table == "" {
		return
	}

	if err := ss.overlay.AddTempTable(ref, cols); err != nil {
		return
	}

	meta := buildTempTableMeta(ref, cols)
	var origSpan syntax.ByteSpan
	if nameNode := ddlTargetNameNode(astStmt); !isNilNode(nameNode) {
		if ls, le, ok := astNodeByteRange(nameNode); ok {
			os, oe := shadowSpanToBestOrig(om, shadowStart+ls, shadowStart+le)
			origSpan = syntax.ByteSpan{Start: os, End: oe}
		}
	}

	info := TempTableInfo{Ref: ref, Meta: meta, OrigSpan: origSpan}
	ss.allTempTbls = append(ss.allTempTbls, info)
	res.tempTables = append(res.tempTables, info)
	ss.updateOverlayDigest()
}

func ddlTargetNameNode(astStmt googlesql.ASTStatementNode) googlesql.ASTNode {
	if ct, ok := astStmt.(*googlesql.ASTCreateTableStatement); ok && !isNilNode(ct) {
		n, _ := ct.Name()

		return n
	}

	return astStmt
}

func tableRefFromPath(parts []string, defaultProj, defaultDS string) catalog.TableRef {
	norm := normalizeASTTableParts(parts)
	if len(norm) > 3 {
		norm = []string{
			strings.Join(norm[:len(norm)-2], "."),
			norm[len(norm)-2],
			norm[len(norm)-1],
		}
	}

	switch len(norm) {
	case 1:
		return catalog.TableRef{Project: defaultProj, Dataset: defaultDS, Table: norm[0]}
	case 2:
		return catalog.TableRef{Project: defaultProj, Dataset: norm[0], Table: norm[1]}
	case 3:
		return catalog.TableRef{Project: norm[0], Dataset: norm[1], Table: norm[2]}
	default:
		return catalog.TableRef{}
	}
}

func prewarmBrokenStatementTables(store *catalog.Store, stmts []*AnalyzedStatement) {
	if store == nil {
		return
	}

	defProj := store.DefaultProject()
	defDS := store.DefaultDataset()

	for _, st := range stmts {
		if st == nil || st.ParseErr == nil || st.SQL == "" {
			continue
		}

		for _, ref := range extractTableRefsFromTokens(st.SQL, defProj, defDS) {
			_, _ = store.GetTable(ref)
		}
	}
}

func extractTableRefsFromTokens(sql, defProj, defDS string) []catalog.TableRef {
	toks := syntax.Scan(sql)
	nonTrivia := collectNonTriviaTokens(toks)
	var refs []catalog.TableRef

	for i := range nonTrivia {
		kw := strings.ToUpper(nonTrivia[i].Text(sql))
		if kw != "FROM" && kw != "JOIN" {
			continue
		}

		if i+1 >= len(nonTrivia) {
			continue
		}

		nextTok := nonTrivia[i+1]
		if nextTok.Kind == syntax.TokenBacktickIdent {
			raw := nextTok.Text(sql)
			if len(raw) < 2 || !strings.HasSuffix(raw, "`") {
				continue
			}
		}

		parts, _ := readDottedIdentFromTokens(sql, nonTrivia, i+1)
		if len(parts) == 0 {
			continue
		}

		ref := tableRefFromPath(parts, defProj, defDS)
		if ref.Table != "" {
			refs = append(refs, ref)
		}
	}

	return refs
}

func buildTempTableMeta(ref catalog.TableRef, cols []catalog.OverlayColumn) *catalog.TableMeta {
	meta := &catalog.TableMeta{
		Ref:  ref,
		Type: catalog.TableTypeTable,
	}

	for _, col := range cols {
		meta.Schema = append(meta.Schema, googleSQLTypeToColumnMeta(col.Name, col.Type))
	}

	return meta
}

func googleSQLTypeToColumnMeta(name string, t googlesql.Googlesql_TypeNode) catalog.ColumnMeta {
	col := catalog.ColumnMeta{Name: name, Type: "STRING", Mode: catalog.ModeNullable}
	if isNilNode(t) {
		return col
	}

	if ok, err := t.IsArray(); err == nil && ok {
		if elem, elemErr := t.GetElementType(); elemErr == nil {
			inner := googleSQLTypeToColumnMeta(name, elem)
			inner.Mode = catalog.ModeRepeated

			return inner
		}
	}

	if ok, err := t.IsStruct(); err == nil && ok {
		return structTypeToColumnMeta(name, t)
	}

	if ok, err := t.IsRange(); err == nil && ok {
		return rangeTypeToColumnMeta(name, t)
	}

	if s := formatGoogleSQLType(t); s != "" && s != "UNKNOWN" {
		col.Type = strings.ToUpper(s)
	}

	return col
}

func structTypeToColumnMeta(name string, t googlesql.Googlesql_TypeNode) catalog.ColumnMeta {
	col := catalog.ColumnMeta{Name: name, Type: "STRUCT", Mode: catalog.ModeNullable}
	st, err := t.AsStruct()
	if err != nil || st == nil {
		return col
	}

	fields, err := st.Fields()
	if err != nil {
		return col
	}

	for _, f := range fields {
		col.Fields = append(col.Fields, googleSQLTypeToColumnMeta(f.Name, f.Type_))
	}

	return col
}

func rangeTypeToColumnMeta(name string, t googlesql.Googlesql_TypeNode) catalog.ColumnMeta {
	col := catalog.ColumnMeta{Name: name, Type: "RANGE", Mode: catalog.ModeNullable}
	rt, err := t.AsRange()
	if err != nil || rt == nil {
		return col
	}

	elem, err := rt.ElementType()
	if err != nil {
		return col
	}

	elemMeta := googleSQLTypeToColumnMeta("", elem)
	col.RangeElementType = &catalog.RangeElementType{Type: elemMeta.Type}

	return col
}

func (e *Engine) analyzeBranchesLocked(entry *docEntry, proj syntax.ProjectedSQL) []BranchAnalysis {
	if len(proj.Branches) == 0 {
		return nil
	}

	out := make([]BranchAnalysis, 0, len(proj.Branches))
	for _, br := range proj.Branches {
		out = append(out, e.analyzeSingleBranchLocked(entry, br))
	}

	return out
}

func (e *Engine) analyzeSingleBranchLocked(entry *docEntry, br syntax.BranchSpan) BranchAnalysis {
	brProj := syntax.ProjectBranch(entry.text, entry.tokens, br)
	brSpans := syntax.SplitStatements(brProj.SQL, nil)

	ba := BranchAnalysis{Branch: br}
	shadowBodyStart := brProj.OffsetMap.OrigByteToShadow(br.BodyStart)
	shadowBodyEnd := brProj.OffsetMap.OrigByteToShadow(br.BodyEnd)

	for _, sp := range brSpans {
		if sp.End <= shadowBodyStart || sp.Start >= shadowBodyEnd {
			continue
		}

		stmtSQL := brProj.SQL[sp.Start:sp.End]
		if strings.TrimSpace(stmtSQL) == "" {
			continue
		}

		cachedAST := e.getOrParseASTLocked(entry, stmtSQL, sha256.Sum256([]byte(stmtSQL)))
		appendBranchTokens(&ba, brProj.OffsetMap, sp.Start, br, cachedAST.parserTokens)
		recordBranchParseError(&ba, brProj.OffsetMap, sp.Start, br, stmtSQL, cachedAST)
	}

	return ba
}

func appendBranchTokens(
	ba *BranchAnalysis,
	om *syntax.OffsetMap,
	shadowStart int,
	br syntax.BranchSpan,
	parserTokens []ByteSemanticToken,
) {
	for _, pt := range parserTokens {
		os, oe, ok := shadowSpanToOrig(om, shadowStart+pt.Start, shadowStart+pt.End)
		if ok && os >= br.BodyStart && oe <= br.BodyEnd {
			nt := pt
			nt.Start = os
			nt.End = oe
			ba.OrigTokens = append(ba.OrigTokens, nt)
		}
	}
}

func recordBranchParseError(
	ba *BranchAnalysis,
	om *syntax.OffsetMap,
	shadowStart int,
	br syntax.BranchSpan,
	stmtSQL string,
	cachedAST *cachedAST,
) {
	if cachedAST.parseErr == nil || ba.HasParseErr || cachedAST.errLine <= 0 {
		return
	}

	li := syntax.NewLineIndex(stmtSQL)
	localOff := li.ByteOffsetFromGoogleSQL(cachedAST.errLine, cachedAST.errCol)
	origOff, ok := om.ShadowByteToOrig(shadowStart + localOff)
	if ok && origOff >= br.BodyStart && origOff < br.BodyEnd && !strings.Contains(cachedAST.errMsg, "__bqpls_expr_") {
		ba.HasParseErr = true
		ba.ParseErrMsg = cachedAST.errMsg
		ba.ParseErrOrig = origOff
	}
}

func parseGoogleSQLError(err error) (line, col int, msg string) {
	if err == nil {
		return 0, 0, ""
	}

	raw := strings.TrimSpace(err.Error())
	if m := googleSQLErrLocRe.FindStringSubmatchIndex(raw); len(m) >= 6 {
		line, _ = strconv.Atoi(raw[m[2]:m[3]])
		col, _ = strconv.Atoi(raw[m[4]:m[5]])
	}

	firstLine, _, _ := strings.Cut(raw, "\n")

	return line, col, strings.TrimSpace(firstLine)
}

// StatementAtByte returns the AnalyzedStatement whose OrigSpan contains origOff.
func (s *DocumentSnapshot) StatementAtByte(origOff int) (*AnalyzedStatement, bool) {
	if s == nil {
		return nil, false
	}

	for _, st := range s.Statements {
		if origOff >= st.OrigSpan.Start && origOff <= st.OrigSpan.End {
			return st, true
		}
	}

	return nil, false
}
