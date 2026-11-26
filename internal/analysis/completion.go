package analysis

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-googlesql"

	"github.com/paulourio/bqlang/catalog"
	"github.com/paulourio/bqlang/formatter"
	"github.com/paulourio/bqlang/internal/funcdocs"
	"github.com/paulourio/bqlang/internal/syntax"
)

const (
	phantomCursorIdent         = "__bqpls_cursor__"
	completionFetchWaitTimeout = 3 * time.Second
)

// Complete returns context-aware autocomplete suggestions at pos in uri.
func (e *Engine) Complete(ctx context.Context, uri string, pos syntax.Position) (*CompletionList, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	entry, snap, err := e.ensureSnapshot(uri)
	if err != nil {
		return nil, err
	}

	origOff := snap.LineIndex.ByteOffset(pos)
	if isRawOrCommentCursor(snap.Tokens, origOff) {
		return &CompletionList{IsIncomplete: false, Items: []CompletionItem{}}, nil
	}

	if inJinja, mode := cursorJinjaMode(snap.Tokens, origOff); inJinja {
		items := completeJinjaMode(snap.Text, snap.Tokens, origOff, mode)
		return finalizeCompletions(items, false), nil
	}

	branchSnap, stmtSQL, localOff := completionBranchAndStatementSlice(snap, origOff)

	if tpCtx, isTablePath := detectTablePathContext(stmtSQL, localOff); isTablePath {
		return completeTablePathContext(ctx, branchSnap, stmtSQL, tpCtx), nil
	}

	items := e.runSQLCompletion(entry, branchSnap, stmtSQL, localOff)
	if len(items) == 0 && waitPendingCompletionTables(ctx, branchSnap, stmtSQL) {
		items = e.runSQLCompletion(entry, branchSnap, stmtSQL, localOff)
	}

	isIncomplete := len(items) == 0 && hasPendingCompletionTables(branchSnap, stmtSQL)

	return finalizeCompletions(items, isIncomplete), nil
}

func (e *Engine) runSQLCompletion(
	entry *docEntry,
	branchSnap *DocumentSnapshot,
	stmtSQL string,
	localOff int,
) []CompletionItem {
	entry.mu.Lock()
	defer entry.mu.Unlock()

	wasmMu.Lock()
	defer wasmMu.Unlock()

	if pipePrefix, isAtPipe := detectPipeCompletionPrefix(stmtSQL, localOff); isAtPipe {
		return e.completeAfterPipeWASM(branchSnap, pipePrefix)
	}

	pipeCols := e.latestPipeStageColumnsWASM(branchSnap, stmtSQL, localOff)

	if items, ok := e.completeViaPhantomASTWASM(branchSnap, stmtSQL, localOff, pipeCols); ok {
		return items
	}

	return completeTokenFallback(branchSnap, stmtSQL, localOff, pipeCols)
}

func pendingCompletionTableRefs(snap *DocumentSnapshot, stmtSQL string) []catalog.TableRef {
	if snap == nil || snap.Store == nil || stmtSQL == "" {
		return nil
	}

	defProj, defDS := storeDefaultProjectDataset(snap)
	refs := extractTableRefsFromTokens(stmtSQL, defProj, defDS)
	var pending []catalog.TableRef
	for _, ref := range refs {
		if snap.Store.IsPending(ref) {
			pending = append(pending, ref)
		}
	}

	return pending
}

func hasPendingCompletionTables(snap *DocumentSnapshot, stmtSQL string) bool {
	return len(pendingCompletionTableRefs(snap, stmtSQL)) > 0
}

func waitPendingCompletionTables(ctx context.Context, snap *DocumentSnapshot, stmtSQL string) bool {
	pending := pendingCompletionTableRefs(snap, stmtSQL)
	if len(pending) == 0 {
		return false
	}

	resolvedAny := false
	deadline := time.Now().Add(completionFetchWaitTimeout)
	for _, ref := range pending {
		rem := time.Until(deadline)
		if rem <= 0 {
			break
		}

		if _, ok := snap.Store.WaitForTable(ctx, ref, rem); ok {
			resolvedAny = true
		}
	}

	return resolvedAny
}

func tokenAtPrev(tokens []syntax.Token, off int) (syntax.Token, bool) {
	if off <= 0 {
		return syntax.Token{}, false
	}

	tok, _, ok := syntax.TokenAt(tokens, off-1)

	return tok, ok
}

func isRawOrCommentToken(tok syntax.Token) bool {
	return tok.Mode == syntax.ModeJinjaRaw ||
		tok.Kind == syntax.TokenComment ||
		tok.Kind == syntax.TokenJinjaComment
}

func isRawOrCommentCursor(tokens []syntax.Token, off int) bool {
	tokAt, _, hasAt := syntax.TokenAt(tokens, off)
	tokPrev, hasPrev := tokenAtPrev(tokens, off)

	if hasAt && isRawOrCommentToken(tokAt) {
		return true
	}

	if hasPrev && tokPrev.Mode == syntax.ModeJinjaRaw {
		return true
	}

	if !hasAt && hasPrev && isRawOrCommentToken(tokPrev) {
		return true
	}

	if hasAt && tokAt.Kind == syntax.TokenNewline && hasPrev && tokPrev.Kind == syntax.TokenComment {
		return true
	}

	return false
}

func cursorJinjaMode(tokens []syntax.Token, off int) (bool, syntax.ScannerMode) {
	if tokAt, _, ok := syntax.TokenAt(tokens, off); ok {
		if (tokAt.Mode == syntax.ModeJinjaExpr || tokAt.Mode == syntax.ModeJinjaTag) &&
			tokAt.Kind != syntax.TokenJinjaExprOpen && tokAt.Kind != syntax.TokenJinjaBlockOpen {
			return true, tokAt.Mode
		}
	}

	if tokPrev, ok := tokenAtPrev(tokens, off); ok {
		if (tokPrev.Mode == syntax.ModeJinjaExpr || tokPrev.Mode == syntax.ModeJinjaTag) &&
			tokPrev.Kind != syntax.TokenJinjaExprClose && tokPrev.Kind != syntax.TokenJinjaBlockClose {
			return true, tokPrev.Mode
		}
	}

	return false, syntax.ModeSQL
}

func completeJinjaMode(src string, tokens []syntax.Token, off int, mode syntax.ScannerMode) []CompletionItem {
	if isAfterLoopDot(src, tokens, off) {
		return loopPropertyCompletions()
	}

	if mode == syntax.ModeJinjaTag && isJinjaTagKeywordPosition(tokens, off) {
		return jinjaTagKeywordCompletions()
	}

	items := make([]CompletionItem, 0, 32)
	for name, doc := range dbtSymbolDocs {
		items = append(items, CompletionItem{
			Label:         name,
			Kind:          CompletionKindFunction,
			Detail:        "dbt / Jinja2",
			Documentation: &MarkupContent{Kind: "markdown", Value: doc},
			SortText:      "0_" + name,
		})
	}

	for _, name := range []string{"execute", "adapter", "exceptions", "log", "schema", "database"} {
		items = append(items, CompletionItem{
			Label:    name,
			Kind:     CompletionKindVariable,
			Detail:   "dbt context",
			SortText: "1_" + name,
		})
	}

	for _, sym := range scanJinjaSymbols(src, tokens) {
		if sym.nameStart >= off && sym.kindLabel == "set" {
			continue
		}

		kind := CompletionKindVariable
		if sym.kindLabel == "macro" {
			kind = CompletionKindFunction
		}

		items = append(items, CompletionItem{
			Label:    sym.name,
			Kind:     kind,
			Detail:   "local " + sym.kindLabel,
			SortText: "0_" + sym.name,
		})
	}

	return items
}

func isAfterLoopDot(src string, tokens []syntax.Token, off int) bool {
	idx := tokenIndexBeforeOffset(tokens, off)
	if idx < 0 {
		return false
	}

	if tokens[idx].Kind == syntax.TokenJinjaIdent {
		idx--
	}

	for idx >= 0 && tokens[idx].IsTrivia() {
		idx--
	}

	if idx < 1 || tokens[idx].Kind != syntax.TokenJinjaPunct || tokens[idx].Text(src) != "." {
		return false
	}

	idx--
	for idx >= 0 && tokens[idx].IsTrivia() {
		idx--
	}

	return idx >= 0 && tokens[idx].Kind == syntax.TokenJinjaIdent && tokens[idx].Text(src) == "loop"
}

func isJinjaTagKeywordPosition(tokens []syntax.Token, off int) bool {
	idx := tokenIndexBeforeOffset(tokens, off)
	if idx >= 0 && (tokens[idx].Kind == syntax.TokenJinjaIdent || tokens[idx].Kind == syntax.TokenJinjaTagKeyword) {
		idx--
	}

	for idx >= 0 && tokens[idx].IsTrivia() {
		idx--
	}

	return idx >= 0 && tokens[idx].Kind == syntax.TokenJinjaBlockOpen
}

func tokenIndexBeforeOffset(tokens []syntax.Token, off int) int {
	for i, tok := range slices.Backward(tokens) {
		if tok.Start < off {
			return i
		}
	}

	return -1
}

func loopPropertyCompletions() []CompletionItem {
	props := []string{
		"index", "index0", "revindex", "revindex0",
		"first", "last", "length", "cycle",
		"depth", "depth0", "previtem", "nextitem", "changed",
	}

	items := make([]CompletionItem, 0, len(props))
	for _, p := range props {
		items = append(items, CompletionItem{
			Label:    p,
			Kind:     CompletionKindProperty,
			Detail:   "Jinja2 loop property",
			SortText: "0_" + p,
		})
	}

	return items
}

func jinjaTagKeywordCompletions() []CompletionItem {
	tags := []string{
		"if", "elif", "else", "endif", "for", "endfor",
		"set", "endset", "macro", "endmacro", "call", "endcall",
		"do", "raw", "endraw", "block", "endblock", "with", "endwith",
		"filter", "endfilter",
	}

	items := make([]CompletionItem, 0, len(tags))
	for _, t := range tags {
		items = append(items, CompletionItem{
			Label:    t,
			Kind:     CompletionKindKeyword,
			Detail:   "Jinja2 tag",
			SortText: "0_" + t,
		})
	}

	return items
}

func completionBranchAndStatementSlice(snap *DocumentSnapshot, origOff int) (*DocumentSnapshot, string, int) {
	if br, inBranch := findBranchAtOffset(snap.Branches, origOff); inBranch {
		brProj := syntax.ProjectBranch(snap.Text, snap.Tokens, br)
		branchSnap := *snap
		branchSnap.Projected = brProj
		stmtSQL, localOff := completionStatementSlice(&branchSnap, origOff)

		return &branchSnap, stmtSQL, localOff
	}

	stmtSQL, localOff := completionStatementSlice(snap, origOff)

	return snap, stmtSQL, localOff
}

func completionStatementSlice(snap *DocumentSnapshot, origOff int) (stmtSQL string, localOff int) {
	shadowOff := snap.Projected.OffsetMap.OrigByteToShadow(origOff)
	spans := syntax.SplitStatements(snap.Projected.SQL, nil)

	for i, sp := range spans {
		endLimit := sp.End
		if !sp.HasSemicolon {
			if i+1 < len(spans) {
				endLimit = spans[i+1].Start
			} else {
				endLimit = len(snap.Projected.SQL)
			}
		}

		if shadowOff >= sp.Start && shadowOff <= endLimit {
			sliceEnd := max(sp.End, shadowOff)
			return snap.Projected.SQL[sp.Start:sliceEnd], shadowOff - sp.Start
		}
	}

	return "", 0
}

func detectPipeCompletionPrefix(stmtSQL string, localOff int) (string, bool) {
	if localOff <= 0 || localOff > len(stmtSQL) {
		return "", false
	}

	prefixTokens := syntax.Scan(stmtSQL[:localOff])
	i := len(prefixTokens) - 1
	if i >= 0 && prefixTokens[i].Kind == syntax.TokenIdent {
		i--
	}

	for i >= 0 && prefixTokens[i].IsTrivia() {
		i--
	}

	if i >= 0 && prefixTokens[i].Kind == syntax.TokenOperator && prefixTokens[i].Text(stmtSQL[:localOff]) == "|>" {
		return stmtSQL[:prefixTokens[i].Start], true
	}

	return "", false
}

func (e *Engine) latestPipeStageColumnsWASM(snap *DocumentSnapshot, stmtSQL string, localOff int) []pipeColumnInfo {
	if localOff <= 0 || localOff > len(stmtSQL) {
		return nil
	}

	toks := syntax.Scan(stmtSQL[:localOff])
	for _, tok := range slices.Backward(toks) {
		if tok.Kind == syntax.TokenOperator && tok.Text(stmtSQL[:localOff]) == "|>" {
			return e.analyzePipePrefixColumnsWASM(snap, stmtSQL[:tok.Start])
		}
	}

	return nil
}

func (e *Engine) completeAfterPipeWASM(snap *DocumentSnapshot, pipePrefix string) []CompletionItem {
	cols := e.analyzePipePrefixColumnsWASM(snap, pipePrefix)
	items := make([]CompletionItem, 0, len(cols)+len(pipeOperatorKeywords))

	for i, c := range cols {
		var ld *CompletionItemLabelDetails
		if c.typ != "" {
			ld = &CompletionItemLabelDetails{Description: c.typ}
		}

		items = append(items, CompletionItem{
			Label:        c.name,
			LabelDetails: ld,
			Kind:         CompletionKindField,
			Detail:       c.typ,
			SortText:     fmt.Sprintf("0_%03d_%s", i, c.name),
		})
	}

	for _, kw := range pipeOperatorKeywords {
		items = append(items, CompletionItem{
			Label:    kw,
			Kind:     CompletionKindKeyword,
			Detail:   "Pipe operator",
			SortText: "1_" + kw,
		})
	}

	return items
}

var pipeOperatorKeywords = []string{
	"AGGREGATE", "AS", "ASSERT", "CALL", "DESCRIBE", "DISTINCT", "DROP",
	"EXTEND", "FORK", "IF", "INNER JOIN", "JOIN", "LEFT JOIN", "LIMIT",
	"LOG", "ORDER BY", "PIVOT", "RENAME", "SELECT", "SET", "STATIC_DESCRIBE",
	"TABLESAMPLE", "TEE", "UNPIVOT", "WHERE", "WINDOW", "WITH",
}

type pipeColumnInfo struct {
	name string
	typ  string
}

func (e *Engine) analyzePipePrefixColumnsWASM(snap *DocumentSnapshot, prefixSQL string) []pipeColumnInfo {
	if strings.TrimSpace(prefixSQL) == "" {
		return nil
	}

	out, stmt, err := e.parseSingleStatementWASM(prefixSQL)
	if err != nil || isNilNode(stmt) {
		return nil
	}
	_ = out

	ss, err := newScriptState(snap.Store)
	if err != nil {
		return nil
	}

	replayScriptOverlay(ss, snap.DeclaredVars, snap.TempTables)

	resOut, aErr := googlesql.AnalyzeStatementFromParserAST(
		stmt,
		ss.analyzerOpts,
		prefixSQL,
		ss.cat.GoogleSQLCatalog(),
		ss.tf,
	)
	if aErr != nil || resOut == nil {
		return nil
	}

	rs, rErr := resOut.ResolvedStatement()
	if rErr != nil || isNilNode(rs) {
		return nil
	}

	return extractPipeOutputColumns(rs)
}

func extractPipeOutputColumns(rs googlesql.ResolvedStatementNode) []pipeColumnInfo {
	switch s := rs.(type) {
	case *googlesql.ResolvedQueryStmt:
		outCols, _ := s.OutputColumnList()
		return convertResolvedOutputColumns(outCols)
	case *googlesql.ResolvedGeneralizedQueryStmt:
		schema, _ := s.OutputSchema()
		if !isNilNode(schema) {
			outCols, _ := schema.OutputColumnList()
			return convertResolvedOutputColumns(outCols)
		}
	case *googlesql.ResolvedSubpipelineStmt:
		schema, _ := s.OutputSchema()
		if !isNilNode(schema) {
			outCols, _ := schema.OutputColumnList()
			return convertResolvedOutputColumns(outCols)
		}
	case *googlesql.ResolvedStatementWithPipeOperatorsStmt:
		inner, _ := s.Statement()
		return extractPipeOutputColumns(inner)
	default:
	}

	return nil
}

func convertResolvedOutputColumns(outCols []*googlesql.ResolvedOutputColumn) []pipeColumnInfo {
	cols := make([]pipeColumnInfo, 0, len(outCols))
	for _, oc := range outCols {
		if isNilNode(oc) {
			continue
		}
		name, _ := oc.Name()
		rc, _ := oc.Column()
		typStr := ""
		if !isNilNode(rc) {
			tp, _ := rc.Type()
			typStr = formatGoogleSQLType(tp)
		}
		if name != "" {
			cols = append(cols, pipeColumnInfo{name: name, typ: typStr})
		}
	}

	return cols
}

func (e *Engine) completeViaPhantomASTWASM(
	snap *DocumentSnapshot,
	stmtSQL string,
	localOff int,
	pipeCols []pipeColumnInfo,
) ([]CompletionItem, bool) {
	if localOff < 0 || localOff > len(stmtSQL) {
		return nil, false
	}

	prefixStart := localOff
	for prefixStart > 0 && isIdentChar(stmtSQL[prefixStart-1]) {
		prefixStart--
	}

	spliced := stmtSQL[:prefixStart] + phantomCursorIdent + stmtSQL[localOff:]
	out, astStmt, parseErr := e.parseSingleStatementWASM(spliced)
	if parseErr != nil || isNilNode(astStmt) {
		closed := closeOpenDelimiters(spliced)
		if closed == spliced {
			return nil, false
		}

		out, astStmt, parseErr = e.parseSingleStatementWASM(closed)
		if parseErr != nil || isNilNode(astStmt) {
			return nil, false
		}
		spliced = closed
	}
	_ = out

	cursorID := findPhantomIdentifier(astStmt, prefixStart)
	if isNilNode(cursorID) {
		return nil, false
	}

	return completeFromPhantomNode(snap, astStmt, cursorID, spliced, pipeCols), true
}

func closeOpenDelimiters(sql string) string {
	toks := syntax.Scan(sql)
	var stack []byte
	for _, t := range toks {
		if t.Kind != syntax.TokenPunct || t.End-t.Start != 1 {
			continue
		}

		switch sql[t.Start] {
		case '(':
			stack = append(stack, ')')
		case '[':
			stack = append(stack, ']')
		case ')', ']':
			if len(stack) > 0 && stack[len(stack)-1] == sql[t.Start] {
				stack = stack[:len(stack)-1]
			}
		default:
		}
	}

	if len(stack) == 0 {
		return sql
	}

	var b strings.Builder
	b.Grow(len(sql) + len(stack))
	b.WriteString(sql)
	for _, ch := range slices.Backward(stack) {
		b.WriteByte(ch)
	}

	return b.String()
}

func findPhantomIdentifier(root googlesql.ASTNode, targetStart int) *googlesql.ASTIdentifier {
	var best *googlesql.ASTIdentifier
	walkASTNodes(root, func(n googlesql.ASTNode) bool {
		if best != nil {
			return false
		}

		id, ok := n.(*googlesql.ASTIdentifier)
		if !ok || isNilNode(id) {
			return true
		}

		name, err := id.GetAsString()
		if err != nil || name != phantomCursorIdent {
			return true
		}

		s, _, hasLoc := astNodeByteRange(id)
		if hasLoc && (s == targetStart || best == nil) {
			best = id
			return false
		}

		return true
	})

	return best
}

func completeTokenFallback(
	snap *DocumentSnapshot,
	stmtSQL string,
	localOff int,
	pipeCols []pipeColumnInfo,
) []CompletionItem {
	prefix := ""
	if localOff > 0 && localOff <= len(stmtSQL) {
		prefix = stmtSQL[:localOff]
	}

	if items, ok := completeQualifiedFromTokens(snap, stmtSQL, prefix); ok {
		return items
	}

	if isAfterTableKeyword(prefix) {
		return completeTablePaths(snap, nil, nil)
	}

	var items []CompletionItem
	for _, c := range pipeCols {
		var ld *CompletionItemLabelDetails
		if c.typ != "" {
			ld = &CompletionItemLabelDetails{Description: c.typ}
		}

		items = append(items, CompletionItem{
			Label:        c.name,
			LabelDetails: ld,
			Kind:         CompletionKindField,
			Detail:       c.typ,
			SortText:     "0_" + c.name,
		})
	}

	items = append(items, completeVariablesAndFunctions(snap)...)
	items = append(items, clauseKeywordCompletions()...)

	return items
}

func completeVariablesAndFunctions(snap *DocumentSnapshot) []CompletionItem {
	docs := funcdocs.All()
	funcs := formatter.BigQueryBuiltinFunctions()
	items := make([]CompletionItem, 0, len(snap.DeclaredVars)+len(docs)+len(funcs))

	for _, dv := range snap.DeclaredVars {
		var ld *CompletionItemLabelDetails
		if dv.TypeName != "" {
			ld = &CompletionItemLabelDetails{Description: dv.TypeName}
		}

		items = append(items, CompletionItem{
			Label:        dv.Name,
			LabelDetails: ld,
			Kind:         CompletionKindVariable,
			Detail:       "DECLARE " + dv.TypeName,
			SortText:     "1_" + dv.Name,
		})
	}

	seen := make(map[string]struct{}, len(docs))
	for _, doc := range docs {
		seen[doc.Name] = struct{}{}
		items = append(items, funcDocCompletionItem(doc.Name, doc, "3_"))
	}

	for _, fn := range funcs {
		if _, exists := seen[strings.ToUpper(fn)]; exists {
			continue
		}

		items = append(items, CompletionItem{
			Label:    fn,
			Kind:     CompletionKindFunction,
			Detail:   "BigQuery function",
			SortText: "3_" + fn,
		})
	}

	return items
}

func funcDocCompletionItem(label string, doc *funcdocs.FuncDoc, sortPrefix string) CompletionItem {
	detail := funcdocs.PrimaryDetail(doc)
	if detail == "" {
		detail = "BigQuery function"
	}

	var md *MarkupContent
	if docText := funcdocs.FormatCompletionDoc(doc); docText != "" {
		md = &MarkupContent{Kind: "markdown", Value: docText}
	}

	return CompletionItem{
		Label:         label,
		Kind:          CompletionKindFunction,
		Detail:        detail,
		Documentation: md,
		SortText:      sortPrefix + label,
	}
}

func clauseKeywordCompletions() []CompletionItem {
	kws := []string{
		"SELECT", "FROM", "WHERE", "GROUP BY", "HAVING", "QUALIFY",
		"ORDER BY", "LIMIT", "OFFSET", "JOIN", "LEFT JOIN", "INNER JOIN",
		"RIGHT JOIN", "FULL JOIN", "CROSS JOIN", "ON", "USING", "UNION ALL",
		"WITH", "AS", "AND", "OR", "NOT", "IN", "IS NULL", "IS NOT NULL",
		"BETWEEN", "CASE", "WHEN", "THEN", "ELSE", "END", "OVER", "PARTITION BY",
	}

	items := make([]CompletionItem, 0, len(kws))
	for _, kw := range kws {
		items = append(items, CompletionItem{
			Label:    kw,
			Kind:     CompletionKindKeyword,
			Detail:   "SQL keyword",
			SortText: "4_" + kw,
		})
	}

	return items
}

func finalizeCompletions(items []CompletionItem, isIncomplete bool) *CompletionList {
	if len(items) == 0 {
		return &CompletionList{IsIncomplete: isIncomplete, Items: []CompletionItem{}}
	}

	seen := make(map[string]int, len(items))
	dedup := make([]CompletionItem, 0, len(items))
	for _, item := range items {
		if item.Label == "" || item.Label == phantomCursorIdent {
			continue
		}

		key := strings.ToLower(item.Label)
		if idx, exists := seen[key]; exists {
			if item.SortText < dedup[idx].SortText {
				dedup[idx] = item
			}

			continue
		}

		seen[key] = len(dedup)
		dedup = append(dedup, item)
	}

	sort.Slice(dedup, func(i, j int) bool {
		if dedup[i].SortText != dedup[j].SortText {
			return dedup[i].SortText < dedup[j].SortText
		}

		return dedup[i].Label < dedup[j].Label
	})

	return &CompletionList{
		IsIncomplete: isIncomplete,
		Items:        dedup,
	}
}
