package analysis

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/goccy/go-googlesql"

	"github.com/paulourio/bqlang/catalog"
	"github.com/paulourio/bqlang/internal/funcdocs"
	"github.com/paulourio/bqlang/internal/syntax"
)

// Hover returns Markdown hover documentation for the symbol at pos in uri,
// or (nil, nil) when pos is on whitespace, comments, or literals.
func (e *Engine) Hover(ctx context.Context, uri string, pos syntax.Position) (*Hover, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	entry, snap, err := e.ensureSnapshot(uri)
	if err != nil {
		return nil, err
	}

	origOff := snap.LineIndex.ByteOffset(pos)
	tok, origOff, ok := hoverTokenAndOffset(snap.Tokens, origOff, len(snap.Text))
	if !ok || isNonHoverableToken(tok) {
		return nil, nil //nolint:nilnil // LSP hover returns null on whitespace/comments/literals
	}

	if tok.Mode == syntax.ModeJinjaExpr || tok.Mode == syntax.ModeJinjaTag {
		return hoverInJinja(snap, tok), nil
	}

	if br, inBranch := findBranchAtOffset(snap.Branches, origOff); inBranch {
		entry.mu.Lock()
		defer entry.mu.Unlock()

		wasmMu.Lock()
		defer wasmMu.Unlock()

		return e.hoverInBranchWASM(snap, br, tok, origOff), nil
	}

	shadowOff := snap.Projected.OffsetMap.OrigByteToShadow(origOff)
	st, hasStmt := snap.StatementAtByte(origOff)
	if !hasStmt {
		return nil, nil //nolint:nilnil // no statement at cursor
	}

	localOff := shadowOff - st.ShadowSpan.Start
	if localOff < 0 || localOff > len(st.SQL) {
		return nil, nil //nolint:nilnil // offset outside statement SQL
	}

	h := evalStatementHoverWASM(entry, snap, st, tok, localOff)
	needsResolved := len(st.ResolvedStmts) == 0 && isNilNode(st.ResolvedStmt)
	if (h == nil || needsResolved) && waitPendingCompletionTables(ctx, snap, st.SQL) {
		e.InvalidateCatalog(catalog.TableRef{})
		return e.retryHoverAfterFetch(uri, pos)
	}

	return h, nil
}

func evalStatementHoverWASM(
	entry *docEntry,
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	tok syntax.Token,
	localOff int,
) *Hover {
	entry.mu.Lock()
	defer entry.mu.Unlock()

	wasmMu.Lock()
	defer wasmMu.Unlock()

	return hoverInStatementWASM(snap, st, tok, localOff)
}

func (e *Engine) retryHoverAfterFetch(uri string, pos syntax.Position) (*Hover, error) {
	entry, snap, err := e.ensureSnapshot(uri)
	if err != nil {
		return nil, err
	}

	origOff := snap.LineIndex.ByteOffset(pos)
	tok, origOff, ok := hoverTokenAndOffset(snap.Tokens, origOff, len(snap.Text))
	if !ok || isNonHoverableToken(tok) {
		return nil, nil //nolint:nilnil // LSP hover returns null on whitespace/comments/literals
	}

	shadowOff := snap.Projected.OffsetMap.OrigByteToShadow(origOff)
	st, hasStmt := snap.StatementAtByte(origOff)
	if !hasStmt {
		return nil, nil //nolint:nilnil // no statement at cursor
	}

	localOff := shadowOff - st.ShadowSpan.Start
	if localOff < 0 || localOff > len(st.SQL) {
		return nil, nil //nolint:nilnil // offset outside statement SQL
	}

	return evalStatementHoverWASM(entry, snap, st, tok, localOff), nil
}

func (e *Engine) hoverInBranchWASM(
	snap *DocumentSnapshot,
	br syntax.BranchSpan,
	tok syntax.Token,
	origOff int,
) *Hover {
	brProj := syntax.ProjectBranch(snap.Text, snap.Tokens, br)
	brSpans := syntax.SplitStatements(brProj.SQL, nil)
	shadowOff := brProj.OffsetMap.OrigByteToShadow(origOff)

	sp, ok := statementSpanForBranchOffset(brProj.SQL, brSpans, shadowOff)
	if !ok {
		return nil
	}

	localOff := shadowOff - sp.Start
	if localOff < 0 || localOff > sp.End-sp.Start {
		return nil
	}

	branchSnap := *snap
	branchSnap.Projected = brProj
	brSt := e.buildBranchHoverStmtWASM(snap, brProj, sp)

	return hoverInStatementWASM(&branchSnap, brSt, tok, localOff)
}

func (e *Engine) buildBranchHoverStmtWASM(
	snap *DocumentSnapshot,
	brProj syntax.ProjectedSQL,
	sp syntax.StatementSpan,
) *AnalyzedStatement {
	origStart, origEnd := shadowSpanToBestOrig(brProj.OffsetMap, sp.Start, sp.End)
	brSt := &AnalyzedStatement{
		ShadowSpan: syntax.ByteSpan{Start: sp.Start, End: sp.End},
		OrigSpan:   syntax.ByteSpan{Start: origStart, End: origEnd},
		SQL:        brProj.SQL[sp.Start:sp.End],
	}

	_, astStmt, parseErr := e.parseSingleStatementWASM(brSt.SQL)
	if parseErr != nil || isNilNode(astStmt) {
		brSt.ParseErr = parseErr

		return brSt
	}

	brSt.ASTStmt = astStmt
	if strings.Contains(brSt.SQL, "__bqpls_expr_") {
		return brSt
	}

	if ss, err := newScriptState(snap.Store); err == nil {
		replayScriptOverlay(ss, snap.DeclaredVars, snap.TempTables)
		res := &cachedResolved{}
		e.processParsedStatementWASM(brSt.SQL, brProj.OffsetMap, sp.Start, astStmt, ss, res)
		applyCachedResolvedToStmt(brSt, res)
	}

	return brSt
}

func hoverTokenAndOffset(tokens []syntax.Token, off, textLen int) (tok syntax.Token, effOff int, ok bool) {
	if t, idx, found := syntax.TokenAt(tokens, off); found {
		if t.Kind == syntax.TokenNewline && idx > 0 {
			prev := tokens[idx-1]
			if prev.End == off && isIdentLikeToken(prev) {
				return prev, prev.End - 1, true
			}
		}

		return t, off, true
	}

	if off == textLen && off > 0 {
		if prev, _, found := syntax.TokenBefore(tokens, off); found && prev.End == off {
			return prev, prev.End - 1, true
		}
	}

	return syntax.Token{}, off, false
}

func hoverTokenAt(tokens []syntax.Token, off, textLen int) (syntax.Token, bool) {
	if tok, _, ok := syntax.TokenAt(tokens, off); ok {
		return tok, true
	}

	if off == textLen {
		if tok, _, ok := syntax.TokenBefore(tokens, off); ok {
			return tok, true
		}
	}

	return syntax.Token{}, false
}

func isNonHoverableToken(tok syntax.Token) bool {
	switch tok.Kind {
	case syntax.TokenInvalid,
		syntax.TokenWhitespace,
		syntax.TokenNewline,
		syntax.TokenComment,
		syntax.TokenJinjaComment,
		syntax.TokenJinjaRawBody,
		syntax.TokenString,
		syntax.TokenBytes,
		syntax.TokenNumber,
		syntax.TokenJinjaString,
		syntax.TokenJinjaNumber:
		return true
	default:
		return false
	}
}

func hoverInJinja(snap *DocumentSnapshot, tok syntax.Token) *Hover {
	if tok.Kind != syntax.TokenJinjaIdent && tok.Kind != syntax.TokenJinjaTagKeyword {
		return nil
	}

	name := tok.Text(snap.Text)
	r := snap.LineIndex.RangeAt(tok.Start, tok.End)

	if desc, ok := dbtSymbolDocs[name]; ok {
		return &Hover{
			Contents: MarkupContent{Kind: "markdown", Value: desc},
			Range:    &r,
		}
	}

	for _, sym := range scanJinjaSymbols(snap.Text, snap.Tokens) {
		if sym.name == name {
			return &Hover{
				Contents: MarkupContent{
					Kind:  "markdown",
					Value: fmt.Sprintf("```jinja\n%s %s\n```\nDefined in this template (line %d).", sym.kindLabel, sym.name, snap.LineIndex.PositionAt(sym.nameStart).Line+1),
				},
				Range: &r,
			}
		}
	}

	return nil
}

var dbtSymbolDocs = map[string]string{
	"ref":            "**`ref(model_name)`** — dbt model reference macro",
	"source":         "**`source(source_name, table_name)`** — dbt source table reference macro",
	"config":         "**`config(...)`** — dbt model configuration block",
	"var":            "**`var(name, default=None)`** — dbt project/CLI variable lookup",
	"is_incremental": "**`is_incremental()`** — returns `true` during incremental dbt runs",
	"this":           "**`this`** — `Relation` representing the current dbt model table",
	"target":         "**`target`** — active dbt profile target metadata",
	"env_var":        "**`env_var(name, default=None)`** — environment variable lookup",
}

func hoverInStatementWASM(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	tok syntax.Token,
	localOff int,
) *Hover {
	rsList := st.ResolvedStmts
	if len(rsList) == 0 && !isNilNode(st.ResolvedStmt) {
		rsList = []googlesql.ResolvedStatementNode{st.ResolvedStmt}
	}

	for _, rs := range rsList {
		if h := hoverFromResolvedAST(snap, st, rs, localOff); h != nil {
			return h
		}
	}

	return hoverFromParserAST(snap, st, tok, localOff)
}

type resolvedCandidate struct {
	node  googlesql.ResolvedNode
	start int
	end   int
	rank  int
}

func hoverFromResolvedAST(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	rs googlesql.ResolvedStatementNode,
	localOff int,
) *Hover {
	if isNilNode(rs) {
		return nil
	}

	cat := st.Catalog
	if cat == nil {
		var err error
		cat, err = catalogForHover(snap)
		if err != nil {
			return nil
		}
	}

	var candidates []resolvedCandidate
	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		s, e, ok := resolvedNodeByteRange(n)
		if !ok || localOff < s || localOff >= e {
			return true
		}

		if r := candidateSpecificityRank(n); r > 0 {
			candidates = append(candidates, resolvedCandidate{node: n, start: s, end: e, rank: r})
		}

		return true
	})

	origins := buildColumnOriginMap(rs, cat)
	lineage := buildResolvedLineage(rs, origins)
	if h := hoverIfOnAliasPrefix(snap, st, rs, candidates, origins, cat, localOff); h != nil {
		return h
	}

	if h := hoverResultAliasFromResolvedAST(snap, st, rs, lineage, cat, localOff); h != nil {
		return h
	}

	if len(candidates) == 0 {
		return nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		lenI := candidates[i].end - candidates[i].start
		lenJ := candidates[j].end - candidates[j].start
		if lenI != lenJ {
			return lenI < lenJ
		}

		return candidates[i].rank > candidates[j].rank
	})

	for _, c := range candidates {
		if h := renderResolvedHover(snap, st, c.node, lineage, cat, localOff); h != nil {
			return h
		}
	}

	return nil
}

func catalogForHover(snap *DocumentSnapshot) (*catalog.Catalog, error) {
	tf, err := googlesql.NewTypeFactory()
	if err != nil {
		return nil, err
	}

	overlay, err := catalog.NewScriptOverlay(tf)
	if err != nil {
		return nil, err
	}

	for _, tt := range snap.TempTables {
		if tt.Meta != nil {
			_ = overlay.AddTableMeta(tt.Meta)
		}
	}

	return catalog.NewCatalogForAnalysis(snap.Store, catalog.DefaultLSPLanguageOptions(), overlay)
}

func candidateSpecificityRank(n googlesql.ResolvedNode) int {
	switch n.(type) {
	case *googlesql.ResolvedGetStructField:
		return 6
	case *googlesql.ResolvedColumnRef:
		return 5
	case *googlesql.ResolvedConstant, *googlesql.ResolvedSystemVariable, *googlesql.ResolvedParameter, *googlesql.ResolvedOrderByItem:
		return 4
	case *googlesql.ResolvedFunctionCall, *googlesql.ResolvedAggregateFunctionCall, *googlesql.ResolvedAnalyticFunctionCall, *googlesql.ResolvedTVFScan:
		return 3
	case *googlesql.ResolvedTableScan, *googlesql.ResolvedWithRefScan:
		return 2
	default:
		return 0
	}
}

func renderResolvedHover(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	n googlesql.ResolvedNode,
	lineage *resolvedLineage,
	cat *catalog.Catalog,
	localOff int,
) *Hover {
	switch node := n.(type) {
	case *googlesql.ResolvedGetStructField:
		return hoverResolvedStructField(snap, st, node, lineage.origins)
	case *googlesql.ResolvedColumnRef:
		return hoverResolvedColumnRef(snap, st, node, lineage, cat)
	case *googlesql.ResolvedOrderByItem:
		return hoverResolvedOrderByItem(snap, st, node, lineage, cat, localOff)
	case *googlesql.ResolvedTableScan:
		return hoverResolvedTableScan(snap, st, node, cat)
	case *googlesql.ResolvedWithRefScan:
		return hoverResolvedWithRef(snap, st, node)
	case *googlesql.ResolvedConstant:
		return hoverResolvedConstant(snap, st, node)
	case *googlesql.ResolvedSystemVariable:
		return hoverResolvedSysVar(snap, st, node)
	case *googlesql.ResolvedParameter:
		return hoverResolvedParam(snap, st, node)
	case *googlesql.ResolvedTVFScan:
		return hoverResolvedTVFScan(snap, st, node)
	default:
		return hoverResolvedFunction(snap, st, n)
	}
}

func hoverIfOnAliasPrefix(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	rs googlesql.ResolvedStatementNode,
	candidates []resolvedCandidate,
	origins map[int32]colOrigin,
	cat *catalog.Catalog,
	localOff int,
) *Hover {
	if isNilNode(st.ASTStmt) {
		return nil
	}

	pe := findPathExprAtOffset(st.ASTStmt, localOff)
	if isNilNode(pe) {
		return nil
	}

	num, err := pe.NumNames()
	if err != nil || num < 2 {
		return nil
	}

	firstID, err := pe.Name(0)
	if err != nil || isNilNode(firstID) {
		return nil
	}

	s, e, ok := astNodeByteRange(firstID)
	if !ok || localOff < s || localOff >= e {
		return nil
	}

	// Cursor is on the qualifier `u` of `u.col`: check if `u` is a table/CTE/UNNEST alias.
	aliasName, _ := firstID.GetAsString()
	if aliasName == "" {
		return nil
	}

	return hoverAliasByName(snap, st, rs, candidates, origins, pe, aliasName, s, e, cat)
}

func hoverAliasByName(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	rs googlesql.ResolvedStatementNode,
	candidates []resolvedCandidate,
	origins map[int32]colOrigin,
	pe *googlesql.ASTPathExpression,
	aliasName string,
	localStart, localEnd int,
	cat *catalog.Catalog,
) *Hover {
	if h := hoverEnclosingScopeAlias(snap, st, rs, candidates, origins, pe, aliasName, localStart, localEnd); h != nil {
		return h
	}

	if !isNilNode(rs) {
		if h := hoverTableScanAlias(snap, st, rs, aliasName, localStart, localEnd, cat); h != nil {
			return h
		}
	}

	if !isNilNode(st.ASTStmt) {
		if cols := findCTEOrSubqueryColsForAlias(st.ASTStmt, aliasName); len(cols) > 0 {
			return makeHoverAtLocalSpan(
				snap, st,
				fmt.Sprintf("### Alias `%s`\n\nColumns: %s", aliasName, strings.Join(cols, ", ")),
				localStart, localEnd,
			)
		}
	}

	return nil
}

func hoverEnclosingScopeAlias(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	rs googlesql.ResolvedStatementNode,
	candidates []resolvedCandidate,
	origins map[int32]colOrigin,
	fromNode googlesql.ASTNode,
	aliasName string,
	localStart, localEnd int,
) *Hover {
	for cur := fromNode; !isNilNode(cur); cur = astParent(cur) {
		sel, ok := cur.(*googlesql.ASTSelect)
		if !ok || isNilNode(sel) {
			continue
		}

		fc, _ := sel.FromClause()
		if h := hoverAliasInFromClause(snap, st, rs, candidates, origins, fc, aliasName, localStart, localEnd); h != nil {
			return h
		}
	}

	return nil
}

func hoverAliasInFromClause(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	rs googlesql.ResolvedStatementNode,
	candidates []resolvedCandidate,
	origins map[int32]colOrigin,
	fromClause googlesql.ASTNode,
	aliasName string,
	localStart, localEnd int,
) *Hover {
	if isNilNode(fromClause) {
		return nil
	}

	var matched *Hover
	walkASTNodes(fromClause, func(n googlesql.ASTNode) bool {
		if matched != nil {
			return false
		}

		h, descend := hoverFromItemNode(snap, st, rs, candidates, origins, n, aliasName, localStart, localEnd)
		if h != nil {
			matched = h
			return false
		}

		return descend
	})

	return matched
}

func hoverFromItemNode(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	rs googlesql.ResolvedStatementNode,
	candidates []resolvedCandidate,
	origins map[int32]colOrigin,
	n googlesql.ASTNode,
	aliasName string,
	localStart, localEnd int,
) (h *Hover, descend bool) {
	switch node := n.(type) {
	case *googlesql.ASTTablePathExpression:
		if !tablePathMatchesAlias(node, aliasName) {
			return nil, true
		}

		if unnest, _ := node.UnnestExpr(); !isNilNode(unnest) {
			return hoverUnnestAlias(snap, st, rs, candidates, origins, aliasName, localStart, localEnd), false
		}

		if tpPath, _ := node.PathExpr(); !isNilNode(tpPath) {
			parts, _ := tpPath.ToIdentifierVector()
			return hoverTableOrCTEByPath(snap, st, parts, localStart, localEnd), false
		}

		return nil, false
	case *googlesql.ASTTableSubquery:
		if al, _ := node.Alias(); !isNilNode(al) {
			if id, _ := al.Identifier(); matchesIdentName(id, aliasName) {
				q, _ := node.Subquery()
				if cols := extractSelectAliasesFromQuery(q); len(cols) > 0 {
					return makeHoverAtLocalSpan(
						snap, st,
						fmt.Sprintf("### Alias `%s`\n\nColumns: %s", aliasName, strings.Join(cols, ", ")),
						localStart, localEnd,
					), false
				}
			}
		}

		return nil, false
	default:
		return nil, true
	}
}

func hoverUnnestAlias(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	rs googlesql.ResolvedStatementNode,
	candidates []resolvedCandidate,
	origins map[int32]colOrigin,
	aliasName string,
	localStart, localEnd int,
) *Hover {
	if h := hoverUnnestFromCandidates(snap, st, candidates, origins, aliasName, localStart, localEnd); h != nil {
		return h
	}

	return hoverUnnestFromArrayScan(snap, st, rs, origins, aliasName, localStart, localEnd)
}

func hoverUnnestFromCandidates(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	candidates []resolvedCandidate,
	origins map[int32]colOrigin,
	aliasName string,
	localStart, localEnd int,
) *Hover {
	for _, c := range candidates {
		ref := unwrapCandidateColumnRef(c.node)
		rc := resolvedRefColumn(ref)
		if isNilNode(rc) {
			continue
		}

		colName, _ := rc.Name()
		if !strings.EqualFold(colName, aliasName) {
			continue
		}

		return hoverUnnestColumn(snap, st, rc, origins, aliasName, localStart, localEnd)
	}

	return nil
}

func hoverUnnestColumn(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	rc *googlesql.ResolvedColumn,
	origins map[int32]colOrigin,
	aliasName string,
	localStart, localEnd int,
) *Hover {
	colID, _ := rc.ColumnId()
	if orig, ok := origins[colID]; ok && orig.meta != nil {
		if colMeta, found := orig.meta.FindField(orig.path...); found {
			return makeColumnHover(snap, st, colMeta, strings.Join(orig.path, "."), orig.meta, orig.isPseudo, localStart, localEnd)
		}
	}

	typ, _ := rc.Type()

	return makeSimpleTypeHover(snap, st, aliasName, formatGoogleSQLType(typ), localStart, localEnd)
}

func resolvedRefColumn(ref *googlesql.ResolvedColumnRef) *googlesql.ResolvedColumn {
	if isNilNode(ref) {
		return nil
	}

	rc, _ := ref.Column()

	return rc
}

func hoverUnnestFromArrayScan(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	rs googlesql.ResolvedStatementNode,
	origins map[int32]colOrigin,
	aliasName string,
	localStart, localEnd int,
) *Hover {
	var matched *Hover
	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		if matched != nil {
			return false
		}

		arr, ok := n.(*googlesql.ResolvedArrayScan)
		if !ok || isNilNode(arr) {
			return true
		}

		elemCol, _ := arr.ElementColumn()
		if isNilNode(elemCol) {
			return true
		}

		nm, _ := elemCol.Name()
		if strings.EqualFold(nm, aliasName) {
			matched = hoverUnnestColumn(snap, st, elemCol, origins, aliasName, localStart, localEnd)
			return false
		}

		return true
	})

	return matched
}

func unwrapCandidateColumnRef(n googlesql.ResolvedNode) *googlesql.ResolvedColumnRef {
	switch node := n.(type) {
	case *googlesql.ResolvedColumnRef:
		return node
	case *googlesql.ResolvedGetStructField:
		_, base, ok := unwrapStructFieldIndices(node)
		if ok {
			if ref, isRef := base.(*googlesql.ResolvedColumnRef); isRef {
				return ref
			}
		}
	default:
	}

	return nil
}

func hoverTableScanAlias(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	rs googlesql.ResolvedStatementNode,
	aliasName string,
	localStart, localEnd int,
	cat *catalog.Catalog,
) *Hover {
	var matched *Hover
	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		if matched != nil {
			return false
		}
		ts, ok := n.(*googlesql.ResolvedTableScan)
		if !ok || isNilNode(ts) {
			return true
		}

		al, _ := ts.Alias()
		tbl, _ := ts.Table()
		meta, found := cat.LookupTable(tbl)
		if found && (strings.EqualFold(al, aliasName) || (al == "" && strings.EqualFold(meta.Ref.Table, aliasName))) {
			matched = makeTableHover(snap, st, meta, localStart, localEnd)
			return false
		}

		return true
	})

	return matched
}

func hoverResolvedStructField(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	outer *googlesql.ResolvedGetStructField,
	origins map[int32]colOrigin,
) *Hover {
	fieldIdxs, cur, ok := unwrapStructFieldIndices(outer)
	if !ok {
		return nil
	}

	s, e, _ := resolvedNodeByteRange(outer)
	typ, _ := outer.Type()
	sqlType := formatGoogleSQLType(typ)

	if ref, isRef := cur.(*googlesql.ResolvedColumnRef); isRef && !isNilNode(ref) {
		if h := hoverStructFieldFromOrigin(snap, st, ref, fieldIdxs, origins, s, e); h != nil {
			return h
		}
	}

	return makeSimpleTypeHover(snap, st, "field", sqlType, s, e)
}

func unwrapStructFieldIndices(outer *googlesql.ResolvedGetStructField) ([]int32, googlesql.ResolvedExprNode, bool) {
	var fieldIdxs []int32
	var cur googlesql.ResolvedExprNode = outer
	for !isNilNode(cur) {
		gsf, ok := cur.(*googlesql.ResolvedGetStructField)
		if !ok {
			break
		}

		idx, err := gsf.FieldIdx()
		if err != nil {
			return nil, nil, false
		}

		fieldIdxs = append(fieldIdxs, idx)
		cur, _ = gsf.Expr()
	}

	return fieldIdxs, cur, true
}

func hoverStructFieldFromOrigin(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	ref *googlesql.ResolvedColumnRef,
	fieldIdxs []int32,
	origins map[int32]colOrigin,
	s, e int,
) *Hover {
	rc, _ := ref.Column()
	if isNilNode(rc) {
		return nil
	}

	colID, _ := rc.ColumnId()
	orig, hasOrig := origins[colID]
	if !hasOrig || orig.meta == nil {
		return nil
	}

	reversed := append([]int32(nil), fieldIdxs...)
	reverseInt32s(reversed)
	fullPath := resolveFieldPathFromIndices(orig, reversed)
	if fullPath == "" {
		return nil
	}

	fieldMeta, found := orig.meta.FindField(strings.Split(fullPath, ".")...)
	if !found {
		return nil
	}

	return makeColumnHover(snap, st, fieldMeta, fullPath, orig.meta, orig.isPseudo, s, e)
}

func reverseInt32s(s []int32) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func hoverResolvedColumnRef(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	ref *googlesql.ResolvedColumnRef,
	lineage *resolvedLineage,
	cat *catalog.Catalog,
) *Hover {
	s, e, _ := resolvedNodeByteRange(ref)

	return hoverResolvedColumnRefAtSpan(snap, st, ref, lineage, cat, s, e)
}

func hoverResolvedColumnRefAtSpan(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	ref *googlesql.ResolvedColumnRef,
	lineage *resolvedLineage,
	cat *catalog.Catalog,
	s, e int,
) *Hover {
	rc, err := ref.Column()
	if err != nil || isNilNode(rc) {
		return nil
	}

	colID, _ := rc.ColumnId()
	colName, _ := rc.Name()

	if orig, ok := lineage.origins[colID]; ok && orig.meta != nil {
		if h := makeOriginColumnHover(snap, st, orig, s, e); h != nil {
			return h
		}
	}

	if h := hoverComputedColumnRef(snap, st, lineage, cat, rc, s, e); h != nil {
		return h
	}

	typ, _ := rc.Type()

	return makeSimpleTypeHover(snap, st, colName, formatGoogleSQLType(typ), s, e)
}

func pseudoColumnSQLType(name string) string {
	switch strings.ToUpper(name) {
	case "_PARTITIONTIME":
		return "TIMESTAMP"
	case "_PARTITIONDATE":
		return "DATE"
	case "_TABLE_SUFFIX":
		return "STRING"
	default:
		return ""
	}
}

func hoverResolvedTableScan(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	scan *googlesql.ResolvedTableScan,
	cat *catalog.Catalog,
) *Hover {
	tbl, err := scan.Table()
	if err != nil || isNilNode(tbl) {
		return nil
	}

	meta, ok := cat.LookupTable(tbl)
	if !ok {
		return nil
	}

	s, e, _ := resolvedNodeByteRange(scan)

	return makeTableHover(snap, st, meta, s, e)
}

func hoverResolvedWithRef(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	ref *googlesql.ResolvedWithRefScan,
) *Hover {
	qname, _ := ref.WithQueryName()
	cols, _ := ref.ColumnList()
	s, e, _ := resolvedNodeByteRange(ref)

	return renderCTEHoverCard(snap, st, qname, cols, s, e)
}

func hoverResolvedConstant(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	c *googlesql.ResolvedConstant,
) *Hover {
	cn, err := c.Constant()
	if err != nil || isNilNode(cn) {
		return nil
	}

	name, _ := cn.FullName()
	typ, _ := cn.Type()
	s, e, _ := resolvedNodeByteRange(c)

	return makeSimpleTypeHover(snap, st, name, formatGoogleSQLType(typ), s, e)
}

func hoverResolvedSysVar(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	sv *googlesql.ResolvedSystemVariable,
) *Hover {
	parts, _ := sv.NamePath()
	typ, _ := sv.Type()
	s, e, _ := resolvedNodeByteRange(sv)

	return makeSimpleTypeHover(snap, st, "@@"+strings.Join(parts, "."), formatGoogleSQLType(typ), s, e)
}

func hoverResolvedParam(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	p *googlesql.ResolvedParameter,
) *Hover {
	name, _ := p.Name()
	typ, _ := p.Type()
	s, e, _ := resolvedNodeByteRange(p)

	return makeSimpleTypeHover(snap, st, "@"+name, formatGoogleSQLType(typ), s, e)
}

func hoverResolvedTVFScan(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	scan *googlesql.ResolvedTVFScan,
) *Hover {
	tvf, err := scan.Tvf()
	if err != nil || isNilNode(tvf) {
		return nil
	}

	name, _ := tvf.FullName(false)
	if name == "" {
		return nil
	}

	s, e, _ := resolvedNodeByteRange(scan)
	if doc, isSafe, found := funcdocs.LookupWithSafe(name); found {
		return makeHoverAtLocalSpan(snap, st, funcdocs.FormatHoverMarkdown(doc, isSafe, ""), s, e)
	}

	return makeHoverAtLocalSpan(snap, st, fmt.Sprintf("### Table Function `%s`", name), s, e)
}

func hoverResolvedFunction(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	n googlesql.ResolvedNode,
) *Hover {
	fnNode, ok := n.(interface {
		Function() (*googlesql.Function, error)
	})
	if !ok {
		return nil
	}

	fn, err := fnNode.Function()
	if err != nil || isNilNode(fn) {
		return nil
	}

	s, e, _ := resolvedNodeByteRange(n)
	name, _ := fn.SQLName()
	sigs, _ := fn.GetSupportedSignaturesUserFacingText(
		catalog.DefaultLSPLanguageOptions(),
		googlesql.NamePrintingStyleKIfNamedOnly,
		false,
	)

	isSafeCall := s >= 0 && e <= len(st.SQL) && strings.HasPrefix(strings.ToUpper(st.SQL[s:e]), "SAFE.")
	if doc, docSafe, found := funcdocs.LookupWithSafe(name); found {
		return makeHoverAtLocalSpan(snap, st, funcdocs.FormatHoverMarkdown(doc, isSafeCall || docSafe, sigs), s, e)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "### Function `%s`\n", name)
	if sigs != "" {
		fmt.Fprintf(&b, "\n```sql\n%s\n```", sigs)
	}

	return makeHoverAtLocalSpan(snap, st, b.String(), s, e)
}

func hoverFromParserAST(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	tok syntax.Token,
	localOff int,
) *Hover {
	if !isNilNode(st.ASTStmt) {
		if h := hoverFromStatementASTNodes(snap, st, localOff); h != nil {
			return h
		}
	}

	if h := hoverTableTokenFallback(snap, st, tok, localOff); h != nil {
		return h
	}

	if h := hoverFunctionTokenFallback(snap, st, tok, localOff); h != nil {
		return h
	}

	word := tok.Text(snap.Text)
	if word != "" {
		for _, dv := range snap.DeclaredVars {
			if strings.EqualFold(dv.Name, word) {
				return makeHoverAtLocalSpan(
					snap, st,
					fmt.Sprintf("```sql\nDECLARE %s %s\n```", dv.Name, dv.TypeName),
					localOff, localOff,
				)
			}
		}
	}

	return nil
}

func hoverTableTokenFallback(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	tok syntax.Token,
	localOff int,
) *Hover {
	if snap.Store == nil || (tok.Kind != syntax.TokenIdent && tok.Kind != syntax.TokenBacktickIdent) {
		return nil
	}

	fullName, s, e := dottedNameAroundOffset(st.SQL, localOff)
	if fullName == "" {
		return nil
	}

	defProj, defDS := storeDefaultProjectDataset(snap)
	ref := tableRefFromPath([]string{fullName}, defProj, defDS)
	if meta, ok := snap.Store.GetTable(ref); ok {
		return makeTableHover(snap, st, meta, s, e)
	}

	// Also support hovering a table alias (e.g., `a` in `SELECT a. FROM ... AS a`) in incomplete SQL.
	if !strings.Contains(fullName, ".") {
		allToks := syntax.Scan(st.SQL)
		if parts := findTablePartsForAliasInTokens(st.SQL, allToks, fullName); len(parts) > 0 {
			aliasRef := tableRefFromPath(parts, defProj, defDS)
			if meta, ok := snap.Store.GetTable(aliasRef); ok {
				return makeTableHover(snap, st, meta, s, e)
			}
		}
	}

	return nil
}

func hoverFunctionTokenFallback(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	tok syntax.Token,
	localOff int,
) *Hover {
	if tok.Kind != syntax.TokenIdent && tok.Kind != syntax.TokenBacktickIdent {
		return nil
	}

	fullName, s, e := dottedNameAroundOffset(st.SQL, localOff)
	if fullName == "" {
		return nil
	}

	doc, isSafe, found := funcdocs.LookupWithSafe(fullName)
	if !found {
		return nil
	}

	if !isFollowedByOpenParen(st.SQL, e) && !hasParenlessOverload(doc) {
		return nil
	}

	return makeHoverAtLocalSpan(snap, st, funcdocs.FormatHoverMarkdown(doc, isSafe, ""), s, e)
}

func isFollowedByOpenParen(sql string, byteEnd int) bool {
	if byteEnd < 0 || byteEnd >= len(sql) {
		return false
	}

	for _, t := range syntax.Scan(sql[byteEnd:]) {
		if t.IsTrivia() {
			continue
		}

		return t.Kind == syntax.TokenPunct && t.Text(sql[byteEnd:]) == "("
	}

	return false
}

func hasParenlessOverload(doc *funcdocs.FuncDoc) bool {
	if doc == nil {
		return false
	}

	for _, sig := range doc.Signatures {
		if !strings.Contains(sig.Label, "(") {
			return true
		}
	}

	return false
}

func dottedNameAroundOffset(sql string, off int) (name string, start, end int) {
	if off < 0 || off > len(sql) {
		return "", 0, 0
	}

	toks := syntax.Scan(sql)
	curIdx := -1
	for i, t := range toks {
		if off >= t.Start && off < t.End {
			curIdx = i
			break
		}
	}
	if curIdx < 0 || !isIdentLikeToken(toks[curIdx]) {
		return "", 0, 0
	}

	lo := curIdx
	for lo >= 2 &&
		toks[lo-1].Kind == syntax.TokenPunct && toks[lo-1].Text(sql) == "." &&
		isIdentLikeToken(toks[lo-2]) {
		lo -= 2
	}

	hi := curIdx
	for hi+2 < len(toks) &&
		toks[hi+1].Kind == syntax.TokenPunct && toks[hi+1].Text(sql) == "." &&
		isIdentLikeToken(toks[hi+2]) {
		hi += 2
	}

	var parts []string
	for i := lo; i <= hi; i += 2 {
		parts = append(parts, strings.Trim(toks[i].Text(sql), "`"))
	}

	return strings.Join(parts, "."), toks[lo].Start, toks[hi].End
}

func hoverFromStatementASTNodes(snap *DocumentSnapshot, st *AnalyzedStatement, localOff int) *Hover {
	if tp := findTablePathAtOffset(st.ASTStmt, localOff); !isNilNode(tp) {
		if pe, _ := tp.PathExpr(); !isNilNode(pe) {
			parts, _ := pe.ToIdentifierVector()
			s, e, _ := astNodeByteRange(pe)
			if h := hoverTableOrCTEByPath(snap, st, parts, s, e); h != nil {
				return h
			}
		}
	}

	if fn := findFuncCallAtOffset(st.ASTStmt, localOff); !isNilNode(fn) {
		if pe, _ := fn.Function(); !isNilNode(pe) {
			parts, _ := pe.ToIdentifierVector()
			full := strings.ToUpper(strings.Join(parts, "."))
			s, e, _ := astNodeByteRange(pe)
			if doc, isSafe, found := funcdocs.LookupWithSafe(full); found {
				return makeHoverAtLocalSpan(snap, st, funcdocs.FormatHoverMarkdown(doc, isSafe, ""), s, e)
			}
			if _, ok := builtinFuncSet[full]; ok {
				return makeHoverAtLocalSpan(snap, st, fmt.Sprintf("### Function `%s`\n\nBuilt-in BigQuery function.", full), s, e)
			}
		}
	}

	if h := hoverResultAliasFromParserAST(snap, st, localOff); h != nil {
		return h
	}

	if h := hoverColumnRefFromParserAST(snap, st, localOff); h != nil {
		return h
	}

	return nil
}

func hoverTableOrCTEByPath(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	parts []string,
	s, e int,
) *Hover {
	if len(parts) == 0 {
		return nil
	}

	if len(parts) == 1 && !isNilNode(st.ASTStmt) {
		if cteCols := findCTEColumnsInAST(st.ASTStmt, parts[0]); len(cteCols) > 0 {
			return makeHoverAtLocalSpan(
				snap, st,
				fmt.Sprintf("### CTE `%s`\n\nColumns: %s", parts[0], strings.Join(cteCols, ", ")),
				s, e,
			)
		}
	}

	for _, tt := range snap.TempTables {
		if strings.EqualFold(tt.Ref.Table, parts[len(parts)-1]) && tt.Meta != nil {
			return makeTableHover(snap, st, tt.Meta, s, e)
		}
	}

	if snap.Store != nil {
		defProj, defDS := storeDefaultProjectDataset(snap)
		ref := tableRefFromPath(parts, defProj, defDS)
		if meta, ok := snap.Store.GetTable(ref); ok {
			return makeTableHover(snap, st, meta, s, e)
		}
	}

	return nil
}

func findPathExprAtOffset(root googlesql.ASTNode, off int) *googlesql.ASTPathExpression {
	var best *googlesql.ASTPathExpression
	bestLen := int(^uint(0) >> 1)

	walkASTNodes(root, func(n googlesql.ASTNode) bool {
		pe, ok := n.(*googlesql.ASTPathExpression)
		if !ok || isNilNode(pe) {
			return true
		}

		s, e, has := astNodeByteRange(pe)
		if has && off >= s && off < e && (e-s) < bestLen {
			best = pe
			bestLen = e - s
		}

		return true
	})

	return best
}

func findTablePathAtOffset(root googlesql.ASTNode, off int) *googlesql.ASTTablePathExpression {
	var best *googlesql.ASTTablePathExpression
	bestLen := int(^uint(0) >> 1)

	walkASTNodes(root, func(n googlesql.ASTNode) bool {
		tp, ok := n.(*googlesql.ASTTablePathExpression)
		if !ok || isNilNode(tp) {
			return true
		}

		s, e, has := astNodeByteRange(tp)
		if has && off >= s && off < e && (e-s) < bestLen {
			best = tp
			bestLen = e - s
		}

		return true
	})

	return best
}

func findFuncCallAtOffset(root googlesql.ASTNode, off int) *googlesql.ASTFunctionCall {
	var best *googlesql.ASTFunctionCall
	bestLen := int(^uint(0) >> 1)

	walkASTNodes(root, func(n googlesql.ASTNode) bool {
		fc, ok := n.(*googlesql.ASTFunctionCall)
		if !ok || isNilNode(fc) {
			return true
		}

		pe, _ := fc.Function()
		s, e, has := astNodeByteRange(pe)
		if has && off >= s && off < e && (e-s) < bestLen {
			best = fc
			bestLen = e - s
		}

		return true
	})

	return best
}

func findCTEColumnsInAST(root googlesql.ASTNode, cteName string) []string {
	var cols []string
	walkASTNodes(root, func(n googlesql.ASTNode) bool {
		aq, ok := n.(*googlesql.ASTAliasedQuery)
		if !ok || isNilNode(aq) {
			return true
		}

		al, _ := aq.Alias()
		if isNilNode(al) {
			return true
		}

		name, _ := al.GetAsString()
		if !strings.EqualFold(name, cteName) {
			return true
		}

		q, _ := aq.Query()
		cols = extractSelectAliasesFromQuery(q)

		return false
	})

	return cols
}

func makeTableHover(snap *DocumentSnapshot, st *AnalyzedStatement, meta *catalog.TableMeta, s, e int) *Hover {
	var b strings.Builder
	kind := string(meta.Type)
	if kind == "" {
		kind = "TABLE"
	}

	fmt.Fprintf(&b, "### `%s` (`%s`)\n", meta.Ref.FullName(), kind)
	if meta.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", meta.Description)
	}

	if pi := meta.EffectivePartitioning(); pi != nil {
		partTarget := pi.Field
		if partTarget == "" {
			partTarget = "_PARTITIONTIME"
		}

		partUnit := pi.Type
		if partUnit == "" {
			partUnit = string(pi.Kind)
		}

		req := ""
		if meta.EffectiveRequirePartitionFilter() {
			req = " (`require_partition_filter = true`)"
		}
		fmt.Fprintf(&b, "\n**Partitioned by:** `%s(%s)`%s\n", partUnit, partTarget, req)
	}

	if meta.Clustering != nil && len(meta.Clustering.Fields) > 0 {
		fmt.Fprintf(&b, "\n**Clustered by:** `%s`\n", strings.Join(meta.Clustering.Fields, "`, `"))
	}

	if meta.NumRows != nil || meta.NumBytes != nil {
		writeTableHoverStats(&b, snap.Options, meta)
	}

	if len(meta.Schema) > 0 {
		b.WriteString("\n| Column | Type | Mode | Attributes | Description |\n|---|---|---|---|---|\n")
		for i := range meta.Schema {
			col := &meta.Schema[i]
			attrs := columnAttributeBadges(meta, col.Name, false)
			desc := strings.ReplaceAll(col.Description, "\n", " ")
			fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s | %s |\n", col.Name, col.SQLType(), col.EffectiveMode(), attrs, desc)
		}
	}

	return makeHoverAtLocalSpan(snap, st, b.String(), s, e)
}

func makeColumnHover(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	col *catalog.ColumnMeta,
	colPath string,
	meta *catalog.TableMeta,
	isPseudo bool,
	s, e int,
) *Hover {
	var b strings.Builder
	fmt.Fprintf(&b, "**`%s`** : `%s` (`%s`)\n", colPath, col.SQLType(), col.EffectiveMode())
	if meta != nil && meta.Ref.FullName() != "" {
		fmt.Fprintf(&b, "\nTable: `%s`\n", meta.Ref.FullName())
	}

	if badges := columnAttributeBadges(meta, colPath, isPseudo); badges != "" {
		fmt.Fprintf(&b, "\nAttributes: %s\n", badges)
	}

	if col.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", col.Description)
	}

	return makeHoverAtLocalSpan(snap, st, b.String(), s, e)
}

func makeSimpleTypeHover(snap *DocumentSnapshot, st *AnalyzedStatement, name, sqlType string, s, e int) *Hover {
	if sqlType == "" {
		sqlType = "UNKNOWN"
	}

	val := fmt.Sprintf("**`%s`** : `%s`", name, sqlType)

	return makeHoverAtLocalSpan(snap, st, val, s, e)
}

func columnAttributeBadges(meta *catalog.TableMeta, colPath string, isPseudo bool) string {
	var badges []string
	if isPseudo || strings.EqualFold(colPath, "_PARTITIONTIME") ||
		strings.EqualFold(colPath, "_PARTITIONDATE") ||
		strings.EqualFold(colPath, "_TABLE_SUFFIX") {
		badges = append(badges, "`[PSEUDO]`")
	}

	if meta != nil && meta.IsPartitionColumn(colPath) {
		badges = append(badges, "`[PARTITION]`")
	}

	if meta != nil {
		if rank, ok := meta.ClusterRank(colPath); ok {
			badges = append(badges, fmt.Sprintf("`[CLUSTER #%d]`", rank))
		}
	}

	return strings.Join(badges, " ")
}

func makeHoverAtLocalSpan(snap *DocumentSnapshot, st *AnalyzedStatement, md string, s, e int) *Hover {
	var rngPtr *syntax.Range
	if s >= 0 && e > s {
		if os, oe, ok := shadowSpanToOrig(snap.Projected.OffsetMap, st.ShadowSpan.Start+s, st.ShadowSpan.Start+e); ok {
			r := snap.LineIndex.RangeAt(os, oe)
			rngPtr = &r
		}
	}

	return &Hover{
		Contents: MarkupContent{
			Kind:  "markdown",
			Value: strings.TrimSpace(md),
		},
		Range: rngPtr,
	}
}

func formatGoogleSQLType(t googlesql.Googlesql_TypeNode) string {
	if isNilNode(t) {
		return "UNKNOWN"
	}

	if tnNode, ok := t.(interface {
		TypeName(googlesql.ProductMode) (string, error)
	}); ok {
		if tn, err := tnNode.TypeName(googlesql.ProductModeProductExternal); err == nil && tn != "" {
			return tn
		}
	}

	if dbg, err := t.DebugString(false); err == nil && dbg != "" {
		return dbg
	}

	return "UNKNOWN"
}
