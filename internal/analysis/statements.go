package analysis

import (
	"context"
	"slices"
	"strings"

	"github.com/goccy/go-googlesql"

	"github.com/paulourio/bqlang/internal/syntax"
)

// StatementKind classifies a top-level BigQuery SQL statement into a coarse category.
type StatementKind string

// Supported coarse statement classifications.
const (
	StatementKindQuery  StatementKind = "QUERY"
	StatementKindDML    StatementKind = "DML"
	StatementKindDDL    StatementKind = "DDL"
	StatementKindScript StatementKind = "SCRIPT"
	StatementKindCall   StatementKind = "CALL"
	StatementKindExport StatementKind = "EXPORT"
	StatementKindLoad   StatementKind = "LOAD"
	StatementKindOther  StatementKind = "OTHER"
)

// StatementInfo describes a single top-level SQL statement in a document.
type StatementInfo struct {
	Range syntax.Range  `json:"range"`
	Kind  StatementKind `json:"kind"`
	Text  string        `json:"text"`
}

// Statements returns top-level statements for the open document uri.
// It uses parsed AST classification when available and falls back to resilient
// lexical splitting and keyword classification for documents with syntax errors
// or unrendered Jinja templates.
func (e *Engine) Statements(ctx context.Context, uri string) ([]StatementInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	_, snap, err := e.ensureSnapshot(uri)
	if err != nil {
		return nil, err
	}

	return extractStatements(snap), nil
}

func extractStatements(snap *DocumentSnapshot) []StatementInfo {
	if snap == nil || snap.Text == "" {
		return []StatementInfo{}
	}

	spans := syntax.SplitStatements(snap.Text, snap.Tokens)
	if len(spans) == 0 {
		return []StatementInfo{}
	}

	out := make([]StatementInfo, 0, len(spans))
	for _, sp := range spans {
		start, end := trimStatementBounds(snap.Text, snap.Tokens, sp)
		if start >= end {
			continue
		}

		kind := classifyStatementSpan(snap, start, end)
		out = append(out, StatementInfo{
			Range: snap.LineIndex.RangeAt(start, end),
			Kind:  kind,
			Text:  snap.Text[start:end],
		})
	}

	return out
}

func trimStatementBounds(src string, tokens []syntax.Token, sp syntax.StatementSpan) (int, int) {
	rawStart := max(0, min(sp.Start, len(src)))
	rawEnd := max(rawStart, min(sp.End, len(src)))

	if sp.HasSemicolon && rawEnd > rawStart && src[rawEnd-1] == ';' {
		rawEnd--
	}

	startIdx := findFirstTokenAtOrAfter(tokens, rawStart)
	endIdx := findLastTokenEndingAtOrBefore(tokens, rawEnd)
	if startIdx < 0 || endIdx < startIdx {
		return 0, 0
	}

	// If SplitStatements stopped before a closing Jinja block tag that closes a
	// block opened inside this statement (when there was no trailing ';'),
	// extend endIdx through the matching closing tag.
	if !sp.HasSemicolon {
		endIdx = extendForUnclosedJinjaBlocks(src, tokens, startIdx, endIdx)
	}

	for startIdx <= endIdx && tokens[startIdx].IsTrivia() {
		startIdx++
	}

	for endIdx >= startIdx && tokens[endIdx].IsTrivia() {
		endIdx--
	}

	if startIdx > endIdx {
		return 0, 0
	}

	return tokens[startIdx].Start, tokens[endIdx].End
}

func findFirstTokenAtOrAfter(tokens []syntax.Token, offset int) int {
	lo, hi := 0, len(tokens)
	for lo < hi {
		mid := lo + (hi-lo)/2
		if tokens[mid].End <= offset {
			lo = mid + 1
		} else {
			hi = mid
		}
	}

	if lo >= len(tokens) {
		return -1
	}

	return lo
}

func findLastTokenEndingAtOrBefore(tokens []syntax.Token, offset int) int {
	lo, hi := 0, len(tokens)
	for lo < hi {
		mid := lo + (hi-lo)/2
		if tokens[mid].Start < offset {
			lo = mid + 1
		} else {
			hi = mid
		}
	}

	return lo - 1
}

var stmtJinjaEndToOpen = map[string]string{
	"endif":     "if",
	"endfor":    "for",
	"endmacro":  "macro",
	"endcall":   "call",
	"endfilter": "filter",
	"endwith":   "with",
	"endraw":    "raw",
	"endset":    "set",
}

func extendForUnclosedJinjaBlocks(src string, tokens []syntax.Token, startIdx, endIdx int) int {
	var stack []string
	for i := startIdx; i <= endIdx; i++ {
		if tokens[i].Kind != syntax.TokenJinjaBlockOpen {
			continue
		}

		closeIdx, kw, hasAssign := inspectJinjaBlockTag(src, tokens, i)
		stack = stepJinjaBlockStack(stack, kw, hasAssign)
		if closeIdx > i {
			i = closeIdx
		}
	}

	if len(stack) == 0 {
		return endIdx
	}

	extended := endIdx
	for i := endIdx + 1; i < len(tokens) && len(stack) > 0; i++ {
		tok := tokens[i]
		if tok.Mode == syntax.ModeSQL && tok.Kind == syntax.TokenPunct && src[tok.Start] == ';' {
			break
		}

		if tok.Kind != syntax.TokenJinjaBlockOpen {
			continue
		}

		closeIdx, kw, hasAssign := inspectJinjaBlockTag(src, tokens, i)
		prevLen := len(stack)
		stack = stepJinjaBlockStack(stack, kw, hasAssign)
		if len(stack) < prevLen && closeIdx >= i {
			extended = closeIdx
		}

		if closeIdx > i {
			i = closeIdx
		}
	}

	return extended
}

func inspectJinjaBlockTag(src string, tokens []syntax.Token, openIdx int) (closeIdx int, kw string, hasAssign bool) {
	closeIdx = openIdx
	for i := openIdx + 1; i < len(tokens); i++ {
		tok := tokens[i]
		if tok.Kind == syntax.TokenJinjaBlockClose {
			closeIdx = i
			break
		}

		if tok.Mode != syntax.ModeJinjaTag {
			closeIdx = i - 1
			break
		}

		if tok.Kind == syntax.TokenJinjaTagKeyword && kw == "" {
			kw = tok.Text(src)
		} else if tok.Kind == syntax.TokenJinjaOperator && tok.Text(src) == "=" {
			hasAssign = true
		}

		closeIdx = i
	}

	return closeIdx, kw, hasAssign
}

func stepJinjaBlockStack(stack []string, kw string, hasAssign bool) []string {
	switch kw {
	case "if", "for", "macro", "call", "filter", "with", "raw":
		return append(stack, kw)
	case "set":
		if !hasAssign {
			return append(stack, kw)
		}

		return stack
	default:
		opener, isEnd := stmtJinjaEndToOpen[kw]
		if !isEnd {
			return stack
		}

		for i, item := range slices.Backward(stack) {
			if item == opener {
				return stack[:i]
			}
		}

		return stack
	}
}

func classifyStatementSpan(snap *DocumentSnapshot, start, end int) StatementKind {
	var (
		bestKind    StatementKind
		bestOverlap int
	)

	for _, st := range snap.Statements {
		if st == nil || st.ParseErr != nil || st.StmtKind == "" {
			continue
		}

		ovStart := max(start, st.OrigSpan.Start)
		ovEnd := min(end, st.OrigSpan.End)
		if ovEnd > ovStart && (ovEnd-ovStart) > bestOverlap {
			bestOverlap = ovEnd - ovStart
			bestKind = st.StmtKind
		}
	}

	if bestKind != "" {
		return bestKind
	}

	return classifyStatementFromTokens(snap.Text, snap.Tokens, start, end)
}

func classifyASTStatement(astStmt googlesql.ASTStatementNode) StatementKind {
	if isNilNode(astStmt) {
		return StatementKindOther
	}

	if hinted, ok := astStmt.(*googlesql.ASTHintedStatement); ok {
		if inner, err := hinted.Statement(); err == nil && !isNilNode(inner) {
			astStmt = inner
		}
	}

	switch astStmt.(type) {
	case *googlesql.ASTQueryStatement,
		*googlesql.ASTStatementWithPipeOperators,
		*googlesql.ASTSubpipelineStatement:
		return StatementKindQuery
	case *googlesql.ASTInsertStatement,
		*googlesql.ASTUpdateStatement,
		*googlesql.ASTDeleteStatement,
		*googlesql.ASTMergeStatement,
		*googlesql.ASTTruncateStatement:
		return StatementKindDML
	case *googlesql.ASTCallStatement:
		return StatementKindCall
	case *googlesql.ASTExportDataStatement,
		*googlesql.ASTExportModelStatement,
		*googlesql.ASTExportMetadataStatement:
		return StatementKindExport
	case *googlesql.ASTAuxLoadDataStatement:
		return StatementKindLoad
	case *googlesql.ASTGrantStatement,
		*googlesql.ASTRevokeStatement,
		*googlesql.ASTRenameStatement,
		*googlesql.ASTUndropStatement:
		return StatementKindDDL
	case *googlesql.ASTBeginStatement,
		*googlesql.ASTCommitStatement,
		*googlesql.ASTRollbackStatement,
		*googlesql.ASTExecuteImmediateStatement:
		return StatementKindScript
	default:
	}

	if isDDL, err := astStmt.IsDdlStatement(); err == nil && isDDL {
		return StatementKindDDL
	}

	if isScript, err := astStmt.IsScriptStatement(); err == nil && isScript {
		return StatementKindScript
	}

	return StatementKindOther
}

func classifyStatementFromTokens(src string, tokens []syntax.Token, start, end int) StatementKind {
	startIdx := findFirstTokenAtOrAfter(tokens, start)
	endIdx := findLastTokenEndingAtOrBefore(tokens, end)
	if startIdx < 0 || endIdx < startIdx {
		return StatementKindOther
	}

	i := skipLeadingStatementPrefixes(src, tokens, startIdx, endIdx)
	if i > endIdx {
		return StatementKindOther
	}

	tok := tokens[i]
	if tok.Kind == syntax.TokenPunct && src[tok.Start] == '(' {
		return StatementKindQuery
	}

	if tok.Kind != syntax.TokenIdent {
		return StatementKindOther
	}

	return classifyLeadingKeyword(src, tokens, i, endIdx)
}

func skipLeadingStatementPrefixes(src string, tokens []syntax.Token, startIdx, endIdx int) int {
	for i := startIdx; i <= endIdx; {
		tok := tokens[i]
		if tok.IsTrivia() || tok.Mode != syntax.ModeSQL {
			i++
			continue
		}

		// Skip leading statement hint @{ ... }.
		if tok.Kind == syntax.TokenPunct && src[tok.Start] == '@' {
			next := nextSQLTokenIndex(tokens, i+1, endIdx)
			if next <= endIdx && tokens[next].Kind == syntax.TokenPunct && src[tokens[next].Start] == '{' {
				i = skipBalancedBraces(src, tokens, next, endIdx) + 1
				continue
			}
		}

		// Skip leading block label (e.g., my_loop: LOOP ...).
		if tok.Kind == syntax.TokenIdent || tok.Kind == syntax.TokenBacktickIdent {
			next := nextSQLTokenIndex(tokens, i+1, endIdx)
			if next <= endIdx && tokens[next].Kind == syntax.TokenPunct && src[tokens[next].Start] == ':' {
				i = next + 1
				continue
			}
		}

		return i
	}

	return endIdx + 1
}

func nextSQLTokenIndex(tokens []syntax.Token, from, endIdx int) int {
	for i := from; i <= endIdx; i++ {
		if !tokens[i].IsTrivia() && tokens[i].Mode == syntax.ModeSQL {
			return i
		}
	}

	return endIdx + 1
}

func skipBalancedBraces(src string, tokens []syntax.Token, openIdx, endIdx int) int {
	depth := 0
	for i := openIdx; i <= endIdx; i++ {
		tok := tokens[i]
		if tok.Mode != syntax.ModeSQL || tok.Kind != syntax.TokenPunct {
			continue
		}

		switch src[tok.Start] {
		case '{':
			depth++
		case '}':
			depth--
			if depth <= 0 {
				return i
			}
		}
	}

	return endIdx
}

func classifyLeadingKeyword(src string, tokens []syntax.Token, idx, endIdx int) StatementKind {
	upper := strings.ToUpper(tokens[idx].Text(src))
	switch upper {
	case "SELECT", "WITH", "FROM", "VALUES", "TABLE":
		return StatementKindQuery
	case "INSERT", "UPDATE", "DELETE", "MERGE", "TRUNCATE":
		return StatementKindDML
	case "CREATE", "ALTER", "DROP", "UNDROP", "RENAME", "GRANT", "REVOKE":
		return StatementKindDDL
	case "SET":
		next := nextSQLTokenIndex(tokens, idx+1, endIdx)
		if next <= endIdx && tokens[next].Kind == syntax.TokenIdent &&
			strings.EqualFold(tokens[next].Text(src), "TRANSACTION") {
			return StatementKindOther
		}

		return StatementKindScript
	case "DECLARE", "BEGIN", "IF", "CASE", "LOOP", "WHILE", "REPEAT", "FOR",
		"EXECUTE", "RAISE", "RETURN", "BREAK", "LEAVE", "CONTINUE", "ITERATE",
		"COMMIT", "ROLLBACK":
		return StatementKindScript
	case "CALL":
		return StatementKindCall
	case "EXPORT":
		return StatementKindExport
	case "LOAD":
		return StatementKindLoad
	default:
		return StatementKindOther
	}
}
