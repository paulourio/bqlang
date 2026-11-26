package analysis

import (
	"errors"
	"math"
	"reflect"

	"github.com/goccy/go-googlesql"

	"github.com/paulourio/bqlang/bqconfig"
	"github.com/paulourio/bqlang/catalog"
	"github.com/paulourio/bqlang/internal/syntax"
)

// ErrDocumentNotOpen is returned when an analysis query targets an unopened URI.
var ErrDocumentNotOpen = errors.New("document not open")

// SemanticTokenType indexes into SemanticTokensLegend.TokenTypes.
type SemanticTokenType uint32

// Canonical semantic token type indices (0..13).
const (
	TokenNamespace SemanticTokenType = iota
	TokenClass
	TokenFunction
	TokenVariable
	TokenProperty
	TokenParameter
	TokenKeyword
	TokenType
	TokenString
	TokenNumber
	TokenComment
	TokenOperator
	TokenMacro
	TokenEnumMember
)

// SemanticTokenModifier is a bitmask indexing into SemanticTokensLegend.TokenModifiers.
type SemanticTokenModifier uint32

// Canonical semantic token modifier bitmasks (1<<0..1<<6).
const (
	ModDeclaration SemanticTokenModifier = 1 << iota
	ModReadonly
	ModDefaultLibrary
	ModPartitioned
	ModClustered
	ModPseudo
	ModModification
)

// SemanticTokensLegend describes the token types and modifiers emitted by the engine.
type SemanticTokensLegend struct {
	TokenTypes     []string `json:"tokenTypes"`
	TokenModifiers []string `json:"tokenModifiers"`
}

// DefaultSemanticTokensLegend returns the canonical 14-type / 7-modifier legend.
func DefaultSemanticTokensLegend() SemanticTokensLegend {
	return SemanticTokensLegend{
		TokenTypes: []string{
			"namespace",
			"class",
			"function",
			"variable",
			"property",
			"parameter",
			"keyword",
			"type",
			"string",
			"number",
			"comment",
			"operator",
			"macro",
			"enumMember",
		},
		TokenModifiers: []string{
			"declaration",
			"readonly",
			"defaultLibrary",
			"partitioned",
			"clustered",
			"pseudo",
			"modification",
		},
	}
}

// SemanticTokensResult holds full or ranged relative-encoded LSP semantic tokens.
type SemanticTokensResult struct {
	ResultID string   `json:"resultId,omitempty"`
	Data     []uint32 `json:"data"`
}

// SemanticTokensEdit describes a single splice into a previous semantic token slice.
type SemanticTokensEdit struct {
	Start       uint32   `json:"start"`
	DeleteCount uint32   `json:"deleteCount"`
	Data        []uint32 `json:"data,omitempty"`
}

// SemanticTokensDeltaResult represents either delta edits (IsDelta == true) or a
// full token payload fallback (IsDelta == false).
type SemanticTokensDeltaResult struct {
	ResultID string               `json:"resultId,omitempty"`
	Edits    []SemanticTokensEdit `json:"edits"`
	Data     []uint32             `json:"data,omitempty"`
	IsDelta  bool                 `json:"-"`
}

// DiagnosticSeverity matches LSP 3.17 DiagnosticSeverity values.
type DiagnosticSeverity int

// LSP 3.17 diagnostic severity constants.
const (
	SeverityError       DiagnosticSeverity = 1
	SeverityWarning     DiagnosticSeverity = 2
	SeverityInformation DiagnosticSeverity = 3
	SeverityHint        DiagnosticSeverity = 4
)

// Canonical diagnostic codes emitted by the analysis engine.
const (
	DiagCodeSyntax                 = "syntax"
	DiagCodeSemantic               = "semantic"
	DiagCodeRequirePartitionFilter = "require_partition_filter"
	DiagCodeMissingPartitionFilter = "missing_partition_filter"
	DiagCodeClusteringFilter       = "clustering_filter"
	DiagCodeEstimatedCost          = "estimated_cost"
)

// Diagnostic represents a syntax, semantic, cost, or storage-layout lint diagnostic.
type Diagnostic struct {
	Range    syntax.Range       `json:"range"`
	Severity DiagnosticSeverity `json:"severity,omitempty"`
	Code     string             `json:"code,omitempty"`
	Source   string             `json:"source,omitempty"`
	Message  string             `json:"message"`
}

// MarkupContent holds formatted hover or completion documentation.
type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Hover represents an LSP hover card result.
type Hover struct {
	Contents MarkupContent `json:"contents"`
	Range    *syntax.Range `json:"range,omitempty"`
}

// ParameterInformation represents a single parameter in an LSP SignatureInformation.
// Label is either a string substring of SignatureInformation.Label or a [2]uint32
// UTF-16 offset pair when the client advertises labelOffsetSupport.
type ParameterInformation struct {
	Label         any            `json:"label"`
	Documentation *MarkupContent `json:"documentation,omitempty"`
}

// SignatureInformation represents a single callable signature overload in LSP 3.17.
type SignatureInformation struct {
	Label           string                 `json:"label"`
	Documentation   *MarkupContent         `json:"documentation,omitempty"`
	Parameters      []ParameterInformation `json:"parameters,omitempty"`
	ActiveParameter *int                   `json:"activeParameter,omitempty"`
}

// SignatureHelp represents an LSP 3.17 textDocument/signatureHelp response.
type SignatureHelp struct {
	Signatures      []SignatureInformation `json:"signatures"`
	ActiveSignature *int                   `json:"activeSignature,omitempty"`
	ActiveParameter *int                   `json:"activeParameter,omitempty"`
}

// CompletionItemKind matches LSP 3.17 CompletionItemKind values.
type CompletionItemKind int

// LSP 3.17 completion item kind constants.
const (
	CompletionKindText       CompletionItemKind = 1
	CompletionKindMethod     CompletionItemKind = 2
	CompletionKindFunction   CompletionItemKind = 3
	CompletionKindField      CompletionItemKind = 5
	CompletionKindVariable   CompletionItemKind = 6
	CompletionKindClass      CompletionItemKind = 7
	CompletionKindModule     CompletionItemKind = 9
	CompletionKindProperty   CompletionItemKind = 10
	CompletionKindUnit       CompletionItemKind = 11
	CompletionKindKeyword    CompletionItemKind = 14
	CompletionKindSnippet    CompletionItemKind = 15
	CompletionKindEnumMember CompletionItemKind = 20
	CompletionKindConstant   CompletionItemKind = 21
	CompletionKindStruct     CompletionItemKind = 22
	CompletionKindOperator   CompletionItemKind = 24
)

// CompletionItemLabelDetails provides additional inline details rendered next to
// a CompletionItem's label in LSP 3.17 clients (for example, field data types).
type CompletionItemLabelDetails struct {
	Detail      string `json:"detail,omitempty"`
	Description string `json:"description,omitempty"`
}

// CompletionItem represents a single autocomplete suggestion.
type CompletionItem struct {
	Label         string                      `json:"label"`
	LabelDetails  *CompletionItemLabelDetails `json:"labelDetails,omitempty"`
	Kind          CompletionItemKind          `json:"kind,omitempty"`
	Detail        string                      `json:"detail,omitempty"`
	Documentation *MarkupContent              `json:"documentation,omitempty"`
	SortText      string                      `json:"sortText,omitempty"`
	FilterText    string                      `json:"filterText,omitempty"`
	InsertText    string                      `json:"insertText,omitempty"`
}

// CompletionList holds completion results returned by Complete.
type CompletionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []CompletionItem `json:"items"`
}

// Location identifies a symbol target in a document URI.
type Location struct {
	URI   string       `json:"uri"`
	Range syntax.Range `json:"range"`
}

// SymbolKind matches LSP 3.17 SymbolKind values.
type SymbolKind int

// LSP 3.17 symbol kind constants.
const (
	SymbolKindModule    SymbolKind = 2
	SymbolKindNamespace SymbolKind = 3
	SymbolKindClass     SymbolKind = 5
	SymbolKindMethod    SymbolKind = 6
	SymbolKindProperty  SymbolKind = 7
	SymbolKindField     SymbolKind = 8
	SymbolKindInterface SymbolKind = 11
	SymbolKindFunction  SymbolKind = 12
	SymbolKindVariable  SymbolKind = 13
	SymbolKindConstant  SymbolKind = 14
	SymbolKindStruct    SymbolKind = 23
)

// DocumentSymbol represents a hierarchical symbol entry in an outline.
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           SymbolKind       `json:"kind"`
	Range          syntax.Range     `json:"range"`
	SelectionRange syntax.Range     `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// Command represents an LSP 3.17 command attached to a CodeLens.
type Command struct {
	Title     string `json:"title"`
	Command   string `json:"command"`
	Arguments []any  `json:"arguments,omitempty"`
}

// CodeLens represents an LSP 3.17 code lens anchored to a source range.
type CodeLens struct {
	Range   syntax.Range `json:"range"`
	Command *Command     `json:"command,omitempty"`
}

// DocumentConfig specifies per-document catalog and LSP options overrides.
type DocumentConfig struct {
	Store   *catalog.Store
	Options *bqconfig.LSPOptions
}

// isNilNode guards optional go-googlesql interface values against typed-nil pointers.
func isNilNode(v any) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)

	return rv.Kind() == reflect.Pointer && rv.IsNil()
}

func astNodeByteRange(n googlesql.ASTNode) (start, end int, ok bool) {
	if isNilNode(n) {
		return 0, 0, false
	}

	loc, err := n.Location()
	if err != nil || loc == nil {
		return 0, 0, false
	}

	sp, err := loc.Start()
	if err != nil || sp == nil {
		return 0, 0, false
	}

	ep, err := loc.End()
	if err != nil || ep == nil {
		return 0, 0, false
	}

	sOff, err := sp.GetByteOffset()
	if err != nil || sOff < 0 {
		return 0, 0, false
	}

	eOff, err := ep.GetByteOffset()
	if err != nil || eOff < sOff {
		return 0, 0, false
	}

	return int(sOff), int(eOff), true
}

func resolvedNodeByteRange(n googlesql.ResolvedNode) (start, end int, ok bool) {
	if isNilNode(n) {
		return 0, 0, false
	}

	loc, err := n.GetParseLocationRangeOrNULL()
	if err != nil || loc == nil {
		return 0, 0, false
	}

	sp, err := loc.Start()
	if err != nil || sp == nil {
		return 0, 0, false
	}

	ep, err := loc.End()
	if err != nil || ep == nil {
		return 0, 0, false
	}

	sOff, err := sp.GetByteOffset()
	if err != nil || sOff < 0 {
		return 0, 0, false
	}

	eOff, err := ep.GetByteOffset()
	if err != nil || eOff < sOff {
		return 0, 0, false
	}

	return int(sOff), int(eOff), true
}

func astChild(n googlesql.ASTNode, idx int) googlesql.ASTNode {
	if isNilNode(n) || idx < 0 || idx > math.MaxInt32 {
		return nil
	}

	c, err := n.Child(int32(idx))
	if err != nil || isNilNode(c) {
		return nil
	}

	return c
}

func astNumChildren(n googlesql.ASTNode) int {
	if isNilNode(n) {
		return 0
	}

	num, err := n.NumChildren()
	if err != nil || num < 0 {
		return 0
	}

	return int(num)
}

func astParent(n googlesql.ASTNode) googlesql.ASTNode {
	if isNilNode(n) {
		return nil
	}

	p, err := n.Parent()
	if err != nil || isNilNode(p) {
		return nil
	}

	return p
}

func shadowSpanToOrig(om *syntax.OffsetMap, sStart, sEnd int) (oStart, oEnd int, ok bool) {
	if om == nil {
		return sStart, sEnd, true
	}

	os, okStart := om.ShadowByteToOrig(sStart)
	if !okStart {
		return 0, 0, false
	}

	oe, okEnd := om.ShadowByteToOrig(sEnd)
	if !okEnd || oe < os {
		return 0, 0, false
	}

	return os, oe, true
}

func scanColumnList(scan googlesql.ResolvedScanNode) []*googlesql.ResolvedColumn {
	if isNilNode(scan) {
		return nil
	}

	if cl, ok := scan.(interface {
		ColumnList() ([]*googlesql.ResolvedColumn, error)
	}); ok {
		cols, _ := cl.ColumnList()
		return cols
	}

	cols, _ := scan.MutableColumnList()

	return cols
}
