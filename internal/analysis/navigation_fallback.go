package analysis

import (
	"maps"
	"slices"
	"strings"

	"github.com/goccy/go-googlesql"

	"github.com/paulourio/bqlang/internal/catalog"
	"github.com/paulourio/bqlang/internal/syntax"
)

type sqlInsertEdit struct {
	origPos int
	length  int
}

type defASTContext struct {
	uri     string
	snap    *DocumentSnapshot
	st      *AnalyzedStatement
	astStmt googlesql.ASTStatementNode
	unmap   func(s, e int) (int, int, bool)
}

func identitySpan(s, e int) (int, int, bool) {
	return s, e, s < e
}

func resolvedStmtList(st *AnalyzedStatement) []googlesql.ResolvedStatementNode {
	if st == nil {
		return nil
	}

	if len(st.ResolvedStmts) > 0 {
		return st.ResolvedStmts
	}

	if !isNilNode(st.ResolvedStmt) {
		return []googlesql.ResolvedStatementNode{st.ResolvedStmt}
	}

	return nil
}

func isRangeVarMakeStructInList(rsList []googlesql.ResolvedStatementNode, idNode *googlesql.ASTIdentifier) bool {
	for _, rs := range rsList {
		if isRangeVarMakeStructAtNode(rs, idNode) {
			return true
		}
	}

	return false
}

func isResolvedColumnRefInList(rsList []googlesql.ResolvedStatementNode, idNode *googlesql.ASTIdentifier) bool {
	matched, _ := classifyResolvedColumnRefInList(rsList, idNode)

	return matched
}

func classifyResolvedColumnRefInList(
	rsList []googlesql.ResolvedStatementNode,
	idNode *googlesql.ASTIdentifier,
) (matchedCR, isOffsetCol bool) {
	_, _, _, matched, isOff := classifyResolvedColumnRefInListWithID(rsList, idNode)

	return matched, isOff
}

func classifyResolvedColumnRefInListWithID(
	rsList []googlesql.ResolvedStatementNode,
	idNode *googlesql.ASTIdentifier,
) (rs googlesql.ResolvedStatementNode, colID int32, hasColID, matchedCR, isOffsetCol bool) {
	s, e, ok := astNodeByteRange(idNode)
	if !ok || s >= e {
		return nil, 0, false, false, false
	}

	for _, stmt := range rsList {
		id, hasID, matched, isOff := classifyResolvedColumnRefInStmt(stmt, s, e)
		if matched {
			return stmt, id, hasID, true, isOff
		}
	}

	return nil, 0, false, false, false
}

func classifyResolvedColumnRefInStmt(
	rs googlesql.ResolvedStatementNode,
	s, e int,
) (colID int32, hasColID, matchedCR, isOffsetCol bool) {
	if isNilNode(rs) {
		return 0, false, false, false
	}

	var foundCR *googlesql.ResolvedColumnRef
	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		if !isNilNode(foundCR) {
			return false
		}

		cr, isCR := n.(*googlesql.ResolvedColumnRef)
		if !isCR || isNilNode(cr) {
			return true
		}

		crS, crE, hasLoc := resolvedNodeByteRange(cr)
		if hasLoc && crS == s && crE == e {
			foundCR = cr
			return false
		}

		return true
	})

	if isNilNode(foundCR) {
		return 0, false, false, false
	}

	col, err := foundCR.Column()
	if err != nil || isNilNode(col) {
		return 0, false, true, false
	}

	id, err := col.ColumnId()
	if err != nil {
		return 0, false, true, false
	}

	return id, true, true, stmtHasArrayOffsetColumnID(rs, id)
}

func isColumnRefScriptSymbol(
	ctx defASTContext,
	rs googlesql.ResolvedStatementNode,
	colID int32,
	pe *googlesql.ASTPathExpression,
	name string,
) bool {
	if isSelectAliasRefInGroupOrOrderBy(ctx.snap, pe, name) {
		return false
	}

	symExpr, ok := traceResolvedColScriptSymbol(rs, colID)
	if !ok {
		return false
	}

	return isScriptSymbolInQueryScope(ctx.astStmt, pe, symExpr)
}

func traceResolvedColScriptSymbol(
	rs googlesql.ResolvedStatementNode,
	colID int32,
) (googlesql.ResolvedNode, bool) {
	if isNilNode(rs) {
		return nil, false
	}

	ccByID, setOpSrc := collectColLineageByID(rs)
	visited := make(map[int32]bool)
	for !visited[colID] {
		visited[colID] = true
		if srcID, isSetOp := setOpSrc[colID]; isSetOp {
			colID = srcID
			continue
		}

		cc, ok := ccByID[colID]
		if !ok || isNilNode(cc) {
			return nil, false
		}

		expr, _ := cc.Expr()
		if isResolvedScriptSymbolNode(expr) {
			return expr, true
		}

		nextCR, isCR := expr.(*googlesql.ResolvedColumnRef)
		if !isCR || isNilNode(nextCR) {
			return nil, false
		}
		nextCol, err := nextCR.Column()
		if err != nil || isNilNode(nextCol) {
			return nil, false
		}
		nextID, idErr := nextCol.ColumnId()
		if idErr != nil {
			return nil, false
		}
		colID = nextID
	}

	return nil, false
}

func collectComputedColumnsByID(rs googlesql.ResolvedStatementNode) map[int32]*googlesql.ResolvedComputedColumn {
	ccByID, _ := collectColLineageByID(rs)

	return ccByID
}

func collectColLineageByID(rs googlesql.ResolvedStatementNode) (map[int32]*googlesql.ResolvedComputedColumn, map[int32]int32) {
	ccByID := make(map[int32]*googlesql.ResolvedComputedColumn)
	setOpSrc := make(map[int32]int32)

	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		switch node := n.(type) {
		case *googlesql.ResolvedComputedColumn:
			if !isNilNode(node) {
				if id, ok := resolvedComputedColID(node); ok {
					ccByID[id] = node
				}
			}
		case *googlesql.ResolvedSetOperationScan:
			recordSetOpColMappings(node, setOpSrc)
		default:
		}
		return true
	})

	return ccByID, setOpSrc
}

func resolvedComputedColID(cc *googlesql.ResolvedComputedColumn) (int32, bool) {
	col, err := cc.Column()
	if err != nil || isNilNode(col) {
		return 0, false
	}
	id, idErr := col.ColumnId()

	return id, idErr == nil
}

func recordSetOpColMappings(scan *googlesql.ResolvedSetOperationScan, setOpSrc map[int32]int32) {
	if isNilNode(scan) {
		return
	}

	colList, err1 := scan.ColumnList()
	items, err2 := scan.InputItemList()
	if err1 != nil || err2 != nil || len(items) == 0 || isNilNode(items[0]) {
		return
	}

	inCols, err3 := items[0].OutputColumnList()
	if err3 != nil {
		return
	}

	limit := min(len(colList), len(inCols))
	for k := range limit {
		outCol := colList[k]
		inCol := inCols[k]
		if isNilNode(outCol) || isNilNode(inCol) {
			continue
		}
		outID, errOut := outCol.ColumnId()
		inID, errIn := inCol.ColumnId()
		if errOut == nil && errIn == nil {
			setOpSrc[outID] = inID
		}
	}
}

func isScriptSymbolInQueryScope(
	astStmt googlesql.ASTStatementNode,
	pe *googlesql.ASTPathExpression,
	symExpr googlesql.ResolvedNode,
) bool {
	if isNilNode(astStmt) {
		return true
	}

	symS, symE, hasLoc := resolvedNodeByteRange(symExpr)
	if !hasLoc {
		return true
	}

	boundary := enclosingSubqueryOrWithAt(astStmt, symS, symE)
	if isNilNode(boundary) {
		return true
	}

	return astNodeContains(boundary, pe)
}

func enclosingSubqueryOrWithAt(root googlesql.ASTNode, s, e int) googlesql.ASTNode {
	var best googlesql.ASTNode
	walkASTNodes(root, func(n googlesql.ASTNode) bool {
		if isNilNode(n) {
			return false
		}
		ns, ne, ok := astNodeByteRange(n)
		if !ok || s < ns || ne < e {
			return false
		}
		switch n.(type) {
		case *googlesql.ASTTableSubquery, *googlesql.ASTWithClauseEntry:
			best = n
		default:
		}
		return true
	})

	return best
}

func astNodeContains(ancestor, desc googlesql.ASTNode) bool {
	for cur := desc; !isNilNode(cur); cur = astParent(cur) {
		if isSameASTNode(cur, ancestor) {
			return true
		}
	}

	return false
}

func resolvedOrderOrGroupColDef(
	ctx defASTContext,
	rsList []googlesql.ResolvedStatementNode,
	pe *googlesql.ASTPathExpression,
	idNode *googlesql.ASTIdentifier,
	name string,
) ([]Location, bool) {
	isOrderExpr := isBareOrderByItemExpr(pe)
	isGroupExpr := !isOrderExpr && isBareGroupByItemExpr(pe)
	if !isOrderExpr && !isGroupExpr {
		return nil, false
	}

	s, e, ok := astNodeByteRange(idNode)
	if !ok || s >= e {
		return nil, false
	}

	if hasResolvedScriptSymbolAt(rsList, s, e) {
		return nil, false
	}

	if isOrderExpr {
		rs, obi, found := findResolvedOrderByItemAt(rsList, s, e)
		if !found {
			return nil, false
		}
		if isOrderByItemScriptSymbol(ctx.astStmt, rs, obi, pe) && !isSelectAliasRefInGroupOrOrderBy(ctx.snap, pe, name) {
			return nil, false
		}
		return []Location{}, true
	}

	if len(ctx.st.InnerResults) > len(rsList) {
		return nil, false
	}

	if isGroupByScriptSymbol(ctx.astStmt, rsList, pe, name) && !isSelectAliasRefInGroupOrOrderBy(ctx.snap, pe, name) {
		return nil, false
	}

	return []Location{}, true
}

func hasResolvedScriptSymbolAt(rsList []googlesql.ResolvedStatementNode, s, e int) bool {
	for _, rs := range rsList {
		if isNilNode(rs) {
			continue
		}
		found := false
		walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
			if found {
				return false
			}
			if !isResolvedScriptSymbolNode(n) {
				return true
			}
			ns, ne, hasLoc := resolvedNodeByteRange(n)
			if hasLoc && ns == s && ne == e {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}

	return false
}

func isResolvedScriptSymbolNode(n googlesql.ResolvedNode) bool {
	switch node := n.(type) {
	case *googlesql.ResolvedParameter:
		return !isNilNode(node)
	case *googlesql.ResolvedConstant:
		return !isNilNode(node)
	case *googlesql.ResolvedSystemVariable:
		return !isNilNode(node)
	default:
		return false
	}
}

func findResolvedOrderByItemAt(
	rsList []googlesql.ResolvedStatementNode,
	s, e int,
) (googlesql.ResolvedStatementNode, *googlesql.ResolvedOrderByItem, bool) {
	for _, rs := range rsList {
		if isNilNode(rs) {
			continue
		}
		var matched *googlesql.ResolvedOrderByItem
		walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
			if !isNilNode(matched) {
				return false
			}
			obi, ok := n.(*googlesql.ResolvedOrderByItem)
			if !ok || isNilNode(obi) {
				return true
			}
			os, oe, hasLoc := resolvedNodeByteRange(obi)
			if hasLoc && os <= s && e <= oe {
				matched = obi
				return false
			}
			return true
		})
		if !isNilNode(matched) {
			return rs, matched, true
		}
	}

	return nil, nil, false
}

func isOrderByItemScriptSymbol(
	astStmt googlesql.ASTStatementNode,
	rs googlesql.ResolvedStatementNode,
	obi *googlesql.ResolvedOrderByItem,
	pe *googlesql.ASTPathExpression,
) bool {
	if isNilNode(rs) || isNilNode(obi) {
		return false
	}

	cr, err := obi.ColumnRef()
	if err != nil || isNilNode(cr) {
		return false
	}

	col, err := cr.Column()
	if err != nil || isNilNode(col) {
		return false
	}

	targetID, err := col.ColumnId()
	if err != nil {
		return false
	}

	symExpr, ok := traceResolvedColScriptSymbol(rs, targetID)
	if !ok {
		return false
	}

	return isScriptSymbolInQueryScope(astStmt, pe, symExpr)
}

func isGroupByScriptSymbol(
	astStmt googlesql.ASTStatementNode,
	rsList []googlesql.ResolvedStatementNode,
	pe *googlesql.ASTPathExpression,
	name string,
) bool {
	enclosingSel := enclosingSelectForGroupBy(pe)
	if isNilNode(enclosingSel) {
		return false
	}

	for _, rs := range rsList {
		if isNilNode(rs) {
			continue
		}
		if stmtHasGroupByScriptSymbol(astStmt, rs, enclosingSel, name) {
			return true
		}
	}

	return false
}

func enclosingSelectForGroupBy(pe *googlesql.ASTPathExpression) *googlesql.ASTSelect {
	for cur := astParent(pe); !isNilNode(cur); cur = astParent(cur) {
		if sel, ok := cur.(*googlesql.ASTSelect); ok && !isNilNode(sel) {
			return sel
		}
	}

	return nil
}

func stmtHasGroupByScriptSymbol(
	astStmt googlesql.ASTStatementNode,
	rs googlesql.ResolvedStatementNode,
	enclosingSel *googlesql.ASTSelect,
	name string,
) bool {
	_ = astStmt
	ccByID := collectComputedColumnsByID(rs)
	found := false
	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		if found {
			return false
		}
		agg, ok := n.(*googlesql.ResolvedAggregateScan)
		if !ok || isNilNode(agg) {
			return true
		}
		gbList, err := agg.GroupByList()
		if err != nil {
			return true
		}
		for _, cc := range gbList {
			if isGroupByComputedColScriptSymbol(ccByID, enclosingSel, cc, name) {
				found = true
				return false
			}
		}
		return true
	})

	return found
}

func isGroupByComputedColScriptSymbol(
	ccByID map[int32]*googlesql.ResolvedComputedColumn,
	enclosingSel *googlesql.ASTSelect,
	cc *googlesql.ResolvedComputedColumn,
	name string,
) bool {
	if isNilNode(cc) {
		return false
	}

	symExpr := resolveGroupByComputedExpr(ccByID, cc)
	if isNilNode(symExpr) {
		return false
	}

	if !matchesGroupByScriptSymbolName(cc, symExpr, name) {
		return false
	}

	symS, symE, hasLoc := resolvedNodeByteRange(symExpr)
	if !hasLoc {
		return false
	}

	return isByteRangeDirectlyInSelect(enclosingSel, symS, symE)
}

func resolveGroupByComputedExpr(
	ccByID map[int32]*googlesql.ResolvedComputedColumn,
	cc *googlesql.ResolvedComputedColumn,
) googlesql.ResolvedNode {
	expr, _ := cc.Expr()
	if isResolvedScriptSymbolNode(expr) {
		return expr
	}

	visited := make(map[int32]bool)
	for {
		cr, ok := expr.(*googlesql.ResolvedColumnRef)
		if !ok || isNilNode(cr) {
			return nil
		}
		col, err := cr.Column()
		if err != nil || isNilNode(col) {
			return nil
		}
		colID, idErr := col.ColumnId()
		if idErr != nil || visited[colID] {
			return nil
		}
		visited[colID] = true
		nextCC, ok := ccByID[colID]
		if !ok || isNilNode(nextCC) {
			return nil
		}
		expr, _ = nextCC.Expr()
		if isResolvedScriptSymbolNode(expr) {
			return expr
		}
	}
}

func matchesGroupByScriptSymbolName(
	cc *googlesql.ResolvedComputedColumn,
	symExpr googlesql.ResolvedNode,
	name string,
) bool {
	col, err := cc.Column()
	if err == nil && !isNilNode(col) {
		if colName, nErr := col.Name(); nErr == nil && strings.EqualFold(colName, name) {
			return true
		}
	}

	if rc, ok := symExpr.(*googlesql.ResolvedConstant); ok && !isNilNode(rc) {
		if cn, cErr := rc.Constant(); cErr == nil && !isNilNode(cn) {
			if cnName, nErr := cn.Name(); nErr == nil && strings.EqualFold(cnName, name) {
				return true
			}
		}
	}

	return false
}

func isByteRangeDirectlyInSelect(sel *googlesql.ASTSelect, symS, symE int) bool {
	selS, selE, ok := astNodeByteRange(sel)
	if !ok || symS < selS || selE < symE {
		return false
	}

	nested := false
	walkASTNodes(sel, func(n googlesql.ASTNode) bool {
		if nested {
			return false
		}
		if isSameASTNode(n, sel) {
			return true
		}
		ns, ne, hasLoc := astNodeByteRange(n)
		if !hasLoc || symS < ns || ne < symE {
			return false
		}
		switch n.(type) {
		case *googlesql.ASTSelect, *googlesql.ASTQuery:
			nested = true
			return false
		default:
			return true
		}
	})

	return !nested
}

func stmtHasArrayOffsetColumnID(rs googlesql.ResolvedStatementNode, targetID int32) bool {
	found := false
	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		if found {
			return false
		}

		if offID, ok := arrayOffsetColumnID(n); ok && offID == targetID {
			found = true
			return false
		}

		return true
	})

	return found
}

func arrayOffsetColumnID(n googlesql.ResolvedNode) (int32, bool) {
	var holder *googlesql.ResolvedColumnHolder
	switch node := n.(type) {
	case *googlesql.ResolvedArrayScan:
		holder, _ = node.ArrayOffsetColumn()
	case *googlesql.ResolvedDeleteStmt:
		holder, _ = node.ArrayOffsetColumn()
	case *googlesql.ResolvedUpdateStmt:
		holder, _ = node.ArrayOffsetColumn()
	default:
		return 0, false
	}

	if isNilNode(holder) {
		return 0, false
	}

	col, err := holder.Column()
	if err != nil || isNilNode(col) {
		return 0, false
	}

	id, err := col.ColumnId()
	if err != nil {
		return 0, false
	}

	return id, true
}

func isRangeVarMakeStructAtNode(rs googlesql.ResolvedStatementNode, idNode *googlesql.ASTIdentifier) bool {
	if isNilNode(rs) {
		return false
	}

	s, e, ok := astNodeByteRange(idNode)
	if !ok || s >= e {
		return false
	}

	found := false
	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		if found {
			return false
		}

		ms, isMS := n.(*googlesql.ResolvedMakeStruct)
		if !isMS || isNilNode(ms) {
			return true
		}

		msS, msE, hasLoc := resolvedNodeByteRange(ms)
		if hasLoc && msS == s && msE == e {
			found = true
			return false
		}

		return true
	})

	return found
}

func isIdentOrBacktick(tok syntax.Token) bool {
	return tok.Kind == syntax.TokenIdent || tok.Kind == syntax.TokenBacktickIdent
}

func (e *Engine) definitionFromRepairedASTWASM(
	uri string,
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	localOff int,
) ([]Location, bool) {
	repairedSQL, edits, changed := repairSQLForDefinition(st.SQL)
	if !changed {
		return nil, false
	}

	_, astStmt, parseErr := e.parseSingleStatementWASM(repairedSQL)
	if parseErr != nil || isNilNode(astStmt) {
		return nil, false
	}

	repairedOff := mapOrigToRepaired(localOff, edits)
	ctx := defASTContext{
		uri:     uri,
		snap:    snap,
		st:      st,
		astStmt: astStmt,
		unmap:   makeRepairedSpanUnmapper(len(st.SQL), edits),
	}

	return definitionFromAST(ctx, repairedOff)
}

func repairSQLForDefinition(stmtSQL string) (repaired string, edits []sqlInsertEdit, changed bool) {
	tokens := syntax.Scan(stmtSQL)
	nonTrivia := collectNonTriviaTokens(tokens)

	for i, t := range nonTrivia {
		if isDanglingDotForDef(stmtSQL, nonTrivia, i, t) {
			edits = append(edits, sqlInsertEdit{
				origPos: t.End,
				length:  len(phantomCursorIdent),
			})
		}
	}

	if len(edits) == 0 {
		closed := closeOpenDelimiters(stmtSQL)

		return closed, nil, closed != stmtSQL
	}

	var b strings.Builder
	b.Grow(len(stmtSQL) + len(edits)*len(phantomCursorIdent))

	cursor := 0
	for _, ed := range edits {
		b.WriteString(stmtSQL[cursor:ed.origPos])
		b.WriteString(phantomCursorIdent)
		cursor = ed.origPos
	}
	b.WriteString(stmtSQL[cursor:])

	closed := closeOpenDelimiters(b.String())

	return closed, edits, true
}

func collectNonTriviaTokens(tokens []syntax.Token) []syntax.Token {
	nonTrivia := make([]syntax.Token, 0, len(tokens))
	for _, t := range tokens {
		if !t.IsTrivia() {
			nonTrivia = append(nonTrivia, t)
		}
	}

	return nonTrivia
}

func isDanglingDotForDef(src string, nonTrivia []syntax.Token, i int, t syntax.Token) bool {
	if t.Kind != syntax.TokenPunct || t.Text(src) != "." || i == 0 {
		return false
	}

	if !isIdentOrBacktick(nonTrivia[i-1]) {
		return false
	}

	if i+1 >= len(nonTrivia) {
		return true
	}

	next := nonTrivia[i+1]
	if next.Kind == syntax.TokenOperator && next.Text(src) == "*" {
		return false
	}

	if !isIdentOrBacktick(next) {
		return true
	}

	if next.Start > t.End && next.Kind == syntax.TokenIdent {
		_, isKW := sqlKeywordSet[strings.ToUpper(next.Text(src))]

		return isKW
	}

	return false
}

func mapOrigToRepaired(localOff int, edits []sqlInsertEdit) int {
	repairedOff := localOff
	for _, ed := range edits {
		if ed.origPos <= localOff {
			repairedOff += ed.length
		}
	}

	return repairedOff
}

func makeRepairedSpanUnmapper(origLen int, edits []sqlInsertEdit) func(s, e int) (int, int, bool) {
	totalInserted := 0
	for _, ed := range edits {
		totalInserted += ed.length
	}
	baseLimit := origLen + totalInserted

	return func(repS, repE int) (int, int, bool) {
		if repS < 0 || repS >= repE || repE > baseLimit {
			return 0, 0, false
		}

		origS, origE := repS, repE
		cumShift := 0
		for _, ed := range edits {
			edStart := ed.origPos + cumShift
			edEnd := edStart + ed.length
			cumShift += ed.length

			if repS < edEnd && edStart < repE {
				return 0, 0, false
			}

			if edEnd <= repS {
				origS -= ed.length
				origE -= ed.length
			}
		}

		if origS < 0 || origS >= origE || origE > origLen {
			return 0, 0, false
		}

		return origS, origE, true
	}
}

type tokenScope struct {
	depth        int
	start        int
	end          int
	isCallClause bool
}

type tokenDecl struct {
	name         string
	nameStart    int
	nameEnd      int
	scope        tokenScope
	scopeID      int
	explicit     bool
	structFields []catalog.ColumnMeta
}

type parenScopeEntry struct {
	openIdx      int
	innerScopeID int
}

type pipeIfState struct {
	parentScopeID    int
	hasElse          bool
	branchTotal      int
	branchDone       int
	branchOutputs    [][]tokenDecl
	branchRangeVars  []map[string][]tokenDecl
	branchUnknownRVs []map[string]bool
}

type tableSubqueryMeta struct {
	alias            string
	ownerScopeID     int
	isSelectFrom     bool
	pipeJoinOnSpan   syntax.ByteSpan
	extraJoinOnSpans []syntax.ByteSpan
}

type cteOpenMeta struct {
	name  string
	scope tokenScope
}

type cteAliasEntry struct {
	cols  []tokenDecl
	scope tokenScope
}

type selectPostWhereSpans struct {
	joinOnSpans        []syntax.ByteSpan
	whereSpans         []syntax.ByteSpan
	clauseSpans        []syntax.ByteSpan
	groupBySubqSpans   []syntax.ByteSpan
	qualifyWindowSpans []syntax.ByteSpan
	allWindowSpans     []syntax.ByteSpan
	setOpOrderSpan     syntax.ByteSpan
	hasSetOpOrder      bool
}

type aliasedProjCol struct {
	alias string
	parts []string
}

type pendingSelectProjection struct {
	isFirstBranch bool
	hasUnqualStar bool
	exceptCols    []string
	replaceCols   []string
	dotStarQuals  [][]string
	bareCols      []string
	dottedCols    [][]string
	aliasedCols   []aliasedProjCol
}

type tokenAliasCollector struct {
	snap                   *DocumentSnapshot
	src                    string
	nonTrivia              []syntax.Token
	scopes                 []tokenScope
	tokScopeIDs            []int
	decls                  []tokenDecl
	withOffsetDecls        []tokenDecl
	selectAliasDecls       []tokenDecl
	loopVarDecls           []tokenDecl
	graphVarDecls          []tokenDecl
	mergeTargetDecls       []tokenDecl
	pipeSelectAliases      map[int][]tokenDecl
	pipeRangeVarCols       map[int]map[string][]tokenDecl
	pipeUnknownRangeVars   map[int]map[string]bool
	pipeIfByOpen           map[int]*pipeIfState
	tableSubqueryOpens     map[int]tableSubqueryMeta
	withCTEOpenToInfo      map[int]cteOpenMeta
	cteSelectAliases       map[string][]cteAliasEntry
	pendingSelect          map[int]*pendingSelectProjection
	pendingSelectPostWhere map[int]selectPostWhereSpans
	seenSelectInChain      map[int]bool
	parenStack             []parenScopeEntry
	funcDefRanges          []syntax.ByteSpan
	tablePos               map[int]bool
	colTargetPos           map[int]bool
	colTargetDecls         map[int][]tokenDecl
	nonRefAliasPos         map[int]bool
}

type tokenAliasInfo struct {
	aliases          []tokenDecl
	withOffsetDecls  []tokenDecl
	selectAliasDecls []tokenDecl
	loopVarDecls     []tokenDecl
	graphVarDecls    []tokenDecl
	funcDefRanges    []syntax.ByteSpan
	tablePos         map[int]bool
	colTargetPos     map[int]bool
	colTargetDecls   map[int][]tokenDecl
	nonRefAliasPos   map[int]bool
}

type updateSetState struct {
	parenDepth   int
	bracketDepth int
	caseDepth    int
	expectLHS    bool
}

func definitionFromTokens(
	uri string,
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	localOff int,
) []Location {
	nonTrivia := collectNonTriviaTokens(syntax.Scan(st.SQL))
	tokIdx, ok := findNonTriviaAtOffset(nonTrivia, localOff, len(st.SQL))
	if !ok || !isIdentOrBacktick(nonTrivia[tokIdx]) {
		return []Location{}
	}

	curTok := nonTrivia[tokIdx]
	targetName := strings.Trim(curTok.Text(st.SQL), "`")
	if targetName == "" {
		return []Location{}
	}

	if tokIdx > 0 && nonTrivia[tokIdx-1].Kind == syntax.TokenPunct && nonTrivia[tokIdx-1].Text(st.SQL) == "." {
		return []Location{}
	}

	scopes, tokScopeIDs := computeTokenScopes(st.SQL, nonTrivia)
	ctes := collectCTEDeclsFromTokens(st.SQL, nonTrivia, scopes, tokScopeIDs)
	info := collectAliasDeclsFromTokens(snap, st.SQL, nonTrivia, scopes, tokScopeIDs)

	if isDeclTokenAt(ctes, curTok) ||
		isExplicitAliasDeclAt(info.aliases, curTok) ||
		isExplicitAliasDeclAt(info.withOffsetDecls, curTok) ||
		info.nonRefAliasPos[tokIdx] {
		return []Location{}
	}

	followedByDot := tokIdx+1 < len(nonTrivia) && nonTrivia[tokIdx+1].Kind == syntax.TokenPunct && nonTrivia[tokIdx+1].Text(st.SQL) == "."
	if info.colTargetPos[tokIdx] {
		return resolveColTargetTokenDef(uri, snap, st, curTok, targetName, followedByDot, info.colTargetDecls[tokIdx])
	}

	afterTableKW := info.tablePos[tokIdx] || isPrecededByTableKeyword(st.SQL, nonTrivia, scopes, tokScopeIDs, tokIdx)
	if followedByDot {
		return resolveQualifiedTokenDef(uri, snap, st, curTok, targetName, afterTableKW, info)
	}

	return resolveStandaloneTokenDef(uri, snap, st, curTok, targetName, afterTableKW, ctes, info)
}

func resolveColTargetTokenDef(
	uri string,
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	curTok syntax.Token,
	targetName string,
	followedByDot bool,
	targetAliases []tokenDecl,
) []Location {
	if !followedByDot || len(targetAliases) == 0 {
		return []Location{}
	}

	if decl, ok := bestMatchingDecl(targetAliases, targetName, curTok, false, false); ok {
		if loc, mapped := locationForTokenSpan(uri, snap, st, decl.nameStart, decl.nameEnd); mapped {
			return []Location{loc}
		}
	}

	return []Location{}
}

func resolveQualifiedTokenDef(
	uri string,
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	curTok syntax.Token,
	targetName string,
	afterTableKW bool,
	info tokenAliasInfo,
) []Location {
	if afterTableKW {
		return []Location{}
	}

	if decl, ok := bestMatchingDecl(info.aliases, targetName, curTok, false, false); ok {
		if loc, mapped := locationForTokenSpan(uri, snap, st, decl.nameStart, decl.nameEnd); mapped {
			return []Location{loc}
		}
	}

	if _, isOff := bestMatchingDecl(info.withOffsetDecls, targetName, curTok, true, false); isOff {
		return []Location{}
	}

	return resolveLoopOrScriptTokenDef(uri, snap, st, curTok, targetName, info)
}

func resolveStandaloneTokenDef(
	uri string,
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	curTok syntax.Token,
	targetName string,
	afterTableKW bool,
	ctes []tokenDecl,
	info tokenAliasInfo,
) []Location {
	cteDecl, hasCTE := bestMatchingDecl(ctes, targetName, curTok, false, true)

	if afterTableKW {
		if hasCTE {
			if loc, mapped := locationForTokenSpan(uri, snap, st, cteDecl.nameStart, cteDecl.nameEnd); mapped {
				return []Location{loc}
			}
		}

		if locs := lookupTempTableDefinition(uri, snap, targetName); len(locs) > 0 {
			return locs
		}

		return []Location{}
	}

	explicitDecl, hasExplicit := bestExplicitScopeDecl(info.aliases, info.withOffsetDecls, targetName, curTok)
	if hasExplicit && (!hasCTE || explicitDecl.scope.depth >= cteDecl.scope.depth) {
		if loc, mapped := locationForTokenSpan(uri, snap, st, explicitDecl.nameStart, explicitDecl.nameEnd); mapped {
			return []Location{loc}
		}
	}

	if hasCTE {
		if loc, mapped := locationForTokenSpan(uri, snap, st, cteDecl.nameStart, cteDecl.nameEnd); mapped {
			return []Location{loc}
		}
	}

	return resolveLoopOrScriptTokenDef(uri, snap, st, curTok, targetName, info)
}

func resolveLoopOrScriptTokenDef(
	uri string,
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	curTok syntax.Token,
	targetName string,
	info tokenAliasInfo,
) []Location {
	if _, isGraphVar := bestMatchingDecl(info.graphVarDecls, targetName, curTok, true, true); isGraphVar {
		return []Location{}
	}

	if decl, ok := bestMatchingDecl(info.loopVarDecls, targetName, curTok, true, true); ok {
		if loc, mapped := locationForTokenSpan(uri, snap, st, decl.nameStart, decl.nameEnd); mapped {
			return []Location{loc}
		}
	}

	if _, isSelAlias := bestMatchingDecl(info.selectAliasDecls, targetName, curTok, true, true); isSelAlias {
		return []Location{}
	}

	if isOffsetInByteSpans(info.funcDefRanges, curTok.Start) {
		return []Location{}
	}

	if locs := lookupScriptSymbolDefinition(uri, snap, targetName, 0); len(locs) > 0 {
		return locs
	}

	return []Location{}
}

func isOffsetInByteSpans(spans []syntax.ByteSpan, off int) bool {
	for _, sp := range spans {
		if off >= sp.Start && off < sp.End {
			return true
		}
	}

	return false
}

func bestExplicitScopeDecl(
	aliases []tokenDecl,
	withOffsetDecls []tokenDecl,
	targetName string,
	curTok syntax.Token,
) (tokenDecl, bool) {
	aliasDecl, hasAlias := bestMatchingDecl(aliases, targetName, curTok, true, false)
	offDecl, hasOff := bestMatchingDecl(withOffsetDecls, targetName, curTok, true, false)

	switch {
	case hasAlias && hasOff:
		if offDecl.scope.depth > aliasDecl.scope.depth {
			return offDecl, true
		}
		return aliasDecl, true
	case hasAlias:
		return aliasDecl, true
	case hasOff:
		return offDecl, true
	default:
		return tokenDecl{}, false
	}
}

func findNonTriviaAtOffset(nonTrivia []syntax.Token, off, sqlLen int) (int, bool) {
	for i, t := range nonTrivia {
		if off >= t.Start && off < t.End {
			return i, true
		}
	}

	if off == sqlLen && len(nonTrivia) > 0 && nonTrivia[len(nonTrivia)-1].End == off {
		return len(nonTrivia) - 1, true
	}

	return -1, false
}

func computeTokenScopes(src string, nonTrivia []syntax.Token) ([]tokenScope, []int) {
	scopes := []tokenScope{{depth: 0, start: 0, end: len(src)}}
	tokScopeIDs := make([]int, len(nonTrivia))
	stack := []int{0}

	for i, t := range nonTrivia {
		curID := stack[len(stack)-1]
		if t.Kind != syntax.TokenPunct {
			tokScopeIDs[i] = curID
			continue
		}

		switch t.Text(src) {
		case "(":
			tokScopeIDs[i] = curID
			isCall := i > 0 && isSpecialClauseCallKeyword(src, nonTrivia[i-1])
			newID := len(scopes)
			scopes = append(scopes, tokenScope{
				depth:        len(stack),
				start:        t.Start,
				end:          len(src),
				isCallClause: isCall,
			})
			stack = append(stack, newID)
		case ")":
			if len(stack) > 1 {
				popped := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				scopes[popped].end = t.End
			}
			tokScopeIDs[i] = stack[len(stack)-1]
		case ";":
			for len(stack) > 1 {
				popped := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				scopes[popped].end = t.Start
			}
			tokScopeIDs[i] = stack[0]
		default:
			tokScopeIDs[i] = curID
		}
	}

	return scopes, tokScopeIDs
}

func isSpecialClauseCallKeyword(src string, tok syntax.Token) bool {
	if tok.Kind != syntax.TokenIdent {
		return false
	}

	switch strings.ToUpper(tok.Text(src)) {
	case "EXTRACT", "TRIM", "SUBSTRING":
		return true
	default:
		return false
	}
}

func isPrecededByTableKeyword(
	src string,
	nonTrivia []syntax.Token,
	scopes []tokenScope,
	tokScopeIDs []int,
	tokIdx int,
) bool {
	if tokIdx == 0 {
		return false
	}

	prev := nonTrivia[tokIdx-1]
	if prev.Kind != syntax.TokenIdent {
		return false
	}

	switch strings.ToUpper(prev.Text(src)) {
	case "FROM":
		return !scopes[tokScopeIDs[tokIdx-1]].isCallClause
	case "JOIN", "INTO", "TABLE", "UPDATE", "MERGE", "DELETE", "INSERT":
		return true
	default:
		return false
	}
}

func isDeclTokenAt(decls []tokenDecl, curTok syntax.Token) bool {
	for _, d := range decls {
		if d.nameStart <= curTok.Start && curTok.End <= d.nameEnd {
			return true
		}
	}

	return false
}

func isExplicitAliasDeclAt(decls []tokenDecl, curTok syntax.Token) bool {
	for _, d := range decls {
		if d.explicit && d.nameStart <= curTok.Start && curTok.End <= d.nameEnd {
			return true
		}
	}

	return false
}

func bestMatchingDecl(
	decls []tokenDecl,
	targetName string,
	curTok syntax.Token,
	explicitOnly bool,
	mustPrecede bool,
) (tokenDecl, bool) {
	var best tokenDecl
	found := false

	for _, d := range decls {
		if explicitOnly && !d.explicit {
			continue
		}
		if !strings.EqualFold(d.name, targetName) {
			continue
		}
		if d.scope.start > curTok.Start || curTok.End > d.scope.end {
			continue
		}
		if mustPrecede && d.nameStart >= curTok.Start {
			continue
		}
		if !found || d.scope.depth > best.scope.depth ||
			(mustPrecede && d.scope.depth == best.scope.depth && d.nameStart > best.nameStart) {
			best = d
			found = true
		}
	}

	return best, found
}

func locationForTokenSpan(
	uri string,
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	s, e int,
) (Location, bool) {
	if s < 0 || s >= e || e > len(st.SQL) {
		return Location{}, false
	}

	os, oe, mapped := shadowSpanToOrig(snap.Projected.OffsetMap, st.ShadowSpan.Start+s, st.ShadowSpan.Start+e)
	if !mapped {
		return Location{}, false
	}

	return Location{
		URI:   uri,
		Range: snap.LineIndex.RangeAt(os, oe),
	}, true
}

var (
	tablePathReservedKeywords = buildTablePathReservedKeywords()
	bareAliasExcludedKeywords = buildBareAliasExcludedKeywords()
)

func buildTablePathReservedKeywords() map[string]struct{} {
	words := []string{
		"WHERE", "GROUP", "HAVING", "QUALIFY", "WINDOW", "ORDER", "LIMIT",
		"UNION", "EXCEPT", "INTERSECT", "SET", "VALUES", "INTO", "SELECT",
		"FROM", "AS", "ON", "USING", "JOIN", "CROSS", "INNER", "LEFT",
		"RIGHT", "FULL", "OUTER", "NATURAL", "WHEN", "THEN", "ELSE", "END",
		"BY", "AND", "OR", "NOT", "NULL", "TRUE", "FALSE", "CASE", "CAST",
		"SAFE_CAST", "EXISTS", "IN", "IS", "LIKE", "BETWEEN", "FOR", "PIVOT",
		"UNPIVOT", "TABLESAMPLE", "WITH", "RECURSIVE", "CREATE", "DROP",
		"ALTER", "INSERT", "UPDATE", "DELETE", "MERGE", "DECLARE", "BEGIN",
		"IF", "WHILE", "LOOP", "REPEAT", "RETURN", "CALL", "UNNEST",
	}
	out := make(map[string]struct{}, len(words))
	for _, w := range words {
		out[w] = struct{}{}
	}

	return out
}

func buildBareAliasExcludedKeywords() map[string]struct{} {
	words := []string{
		// GoogleSQL reserved keywords (plus conditionally reserved QUALIFY, MATCH_RECOGNIZE, GRAPH_TABLE).
		"ALL", "AND", "ANY", "ARRAY", "AS", "ASC", "ASSERT_ROWS_MODIFIED", "AT",
		"BETWEEN", "BY", "CASE", "CAST", "COLLATE", "CONTAINS", "CREATE", "CROSS",
		"CUBE", "CURRENT", "DEFAULT", "DEFINE", "DESC", "DISTINCT", "ELSE", "END",
		"ENUM", "ESCAPE", "EXCEPT", "EXCLUDE", "EXISTS", "EXTRACT", "FALSE",
		"FETCH", "FOLLOWING", "FOR", "FROM", "FULL", "GRAPH_TABLE", "GROUP",
		"GROUPING", "GROUPS", "HASH", "HAVING", "IF", "IGNORE", "IN", "INNER",
		"INTERSECT", "INTERVAL", "INTO", "IS", "JOIN", "LATERAL", "LEFT", "LIKE",
		"LIMIT", "LOOKUP", "MATCH_RECOGNIZE", "MERGE", "NATURAL", "NEW", "NO",
		"NOT", "NULL", "NULLS", "OF", "ON", "OR", "ORDER", "OUTER", "OVER",
		"PARTITION", "PRECEDING", "PROTO", "QUALIFY", "RANGE", "RECURSIVE",
		"RESPECT", "RIGHT", "ROLLUP", "ROWS", "SELECT", "SET", "SOME", "STRUCT",
		"TABLESAMPLE", "THEN", "TO", "TREAT", "TRUE", "UNBOUNDED", "UNION",
		"UNNEST", "USING", "WHEN", "WHERE", "WINDOW", "WITH", "WITHIN",
		// Unreserved clause/statement keywords that must not act as bare aliases without AS.
		"OFFSET", "PIVOT", "UNPIVOT", "RETURNING", "SYSTEM_TIME", "CONNECT",
		"START", "PREWHERE", "VALUES", "TRUNCATE", "DROP", "ALTER", "INSERT",
		"UPDATE", "DELETE", "DECLARE", "BEGIN", "WHILE", "LOOP", "REPEAT",
		"RETURN", "RETURNS", "CALL", "BREAK", "CONTINUE", "ITERATE", "LEAVE",
		"DO", "ELSEIF", "ELSIF", "UNTIL", "ASSERT", "RAISE", "EXPORT", "LOAD",
		"CLONE", "COPY", "DESCRIBE", "TABLE", "REPLACE", "SAFE_CAST",
	}
	out := make(map[string]struct{}, len(words))
	for _, w := range words {
		out[w] = struct{}{}
	}

	return out
}

func isTablePathReservedKeyword(src string, tok syntax.Token) bool {
	if tok.Kind != syntax.TokenIdent {
		return false
	}

	_, isReserved := tablePathReservedKeywords[strings.ToUpper(tok.Text(src))]

	return isReserved
}

func isTablePathIdent(src string, tok syntax.Token) bool {
	if tok.Kind == syntax.TokenBacktickIdent {
		return strings.Trim(tok.Text(src), "`") != ""
	}

	if tok.Kind != syntax.TokenIdent {
		return false
	}

	return !isTablePathReservedKeyword(src, tok)
}

func isBareAliasIdent(src string, tok syntax.Token) bool {
	if tok.Kind == syntax.TokenBacktickIdent {
		return strings.Trim(tok.Text(src), "`") != ""
	}

	if tok.Kind != syntax.TokenIdent {
		return false
	}

	_, excluded := bareAliasExcludedKeywords[strings.ToUpper(tok.Text(src))]

	return !excluded
}

func collectCTEDeclsFromTokens(
	src string,
	nonTrivia []syntax.Token,
	scopes []tokenScope,
	tokScopeIDs []int,
) []tokenDecl {
	var decls []tokenDecl
	for i, t := range nonTrivia {
		if t.Kind != syntax.TokenIdent || !strings.EqualFold(t.Text(src), "WITH") {
			continue
		}

		decls = append(decls, parseWithClauseCTEs(src, nonTrivia, scopes, tokScopeIDs, i)...)
	}

	return decls
}

func parseWithClauseCTEs(
	src string,
	nonTrivia []syntax.Token,
	scopes []tokenScope,
	tokScopeIDs []int,
	withIdx int,
) []tokenDecl {
	var decls []tokenDecl
	j := withIdx + 1
	isRecursive := false
	if j < len(nonTrivia) && nonTrivia[j].Kind == syntax.TokenIdent && strings.EqualFold(nonTrivia[j].Text(src), "RECURSIVE") {
		isRecursive = true
		j++
	}

	for j < len(nonTrivia) {
		if !isTablePathIdent(src, nonTrivia[j]) {
			break
		}

		nameTok := nonTrivia[j]
		nameScopeID := tokScopeIDs[j]
		nameScope := scopes[nameScopeID]
		j++

		if j < len(nonTrivia) && nonTrivia[j].Kind == syntax.TokenPunct && nonTrivia[j].Text(src) == "(" {
			j = skipBalancedParens(src, nonTrivia, j)
		}

		if j+1 >= len(nonTrivia) || nonTrivia[j].Kind != syntax.TokenIdent || !strings.EqualFold(nonTrivia[j].Text(src), "AS") {
			break
		}
		if nonTrivia[j+1].Kind != syntax.TokenPunct || nonTrivia[j+1].Text(src) != "(" {
			break
		}

		closeIdx, closed := balancedParenCloseIdx(src, nonTrivia, j+1)
		bodyEndIdx := len(nonTrivia)
		if closed {
			bodyEndIdx = closeIdx + 1
		}
		cteScope := nonRecursiveCTEScope(nonTrivia, nameScope, closeIdx, closed, isRecursive)

		decls = append(decls, tokenDecl{
			name:      strings.Trim(nameTok.Text(src), "`"),
			nameStart: nameTok.Start,
			nameEnd:   nameTok.End,
			scope:     cteScope,
			scopeID:   nameScopeID,
			explicit:  true,
		})

		j = bodyEndIdx
		if j >= len(nonTrivia) || nonTrivia[j].Kind != syntax.TokenPunct || nonTrivia[j].Text(src) != "," {
			break
		}
		j++
	}

	return decls
}

func nonRecursiveCTEScope(
	nonTrivia []syntax.Token,
	nameScope tokenScope,
	closeIdx int,
	closed, isRecursive bool,
) tokenScope {
	if isRecursive {
		return nameScope
	}
	if closed {
		nameScope.start = nonTrivia[closeIdx].End
		return nameScope
	}

	nameScope.start = nameScope.end

	return nameScope
}

func balancedParenCloseIdx(src string, nonTrivia []syntax.Token, openIdx int) (int, bool) {
	if openIdx >= len(nonTrivia) || nonTrivia[openIdx].Kind != syntax.TokenPunct || nonTrivia[openIdx].Text(src) != "(" {
		return -1, false
	}

	depth := 0
	for i := openIdx; i < len(nonTrivia); i++ {
		if nonTrivia[i].Kind != syntax.TokenPunct {
			continue
		}
		switch nonTrivia[i].Text(src) {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return i, true
			}
		default:
		}
	}

	return -1, false
}

func collectAliasDeclsFromTokens(
	snap *DocumentSnapshot,
	src string,
	nonTrivia []syntax.Token,
	scopes []tokenScope,
	tokScopeIDs []int,
) tokenAliasInfo {
	c := &tokenAliasCollector{
		snap:                   snap,
		src:                    src,
		nonTrivia:              nonTrivia,
		scopes:                 scopes,
		tokScopeIDs:            tokScopeIDs,
		pipeSelectAliases:      make(map[int][]tokenDecl),
		pipeRangeVarCols:       make(map[int]map[string][]tokenDecl),
		pipeUnknownRangeVars:   make(map[int]map[string]bool),
		pipeIfByOpen:           make(map[int]*pipeIfState),
		tableSubqueryOpens:     make(map[int]tableSubqueryMeta),
		withCTEOpenToInfo:      make(map[int]cteOpenMeta),
		cteSelectAliases:       make(map[string][]cteAliasEntry),
		pendingSelect:          make(map[int]*pendingSelectProjection),
		pendingSelectPostWhere: make(map[int]selectPostWhereSpans),
		seenSelectInChain:      make(map[int]bool),
		tablePos:               make(map[int]bool),
		colTargetPos:           make(map[int]bool),
		colTargetDecls:         make(map[int][]tokenDecl),
		nonRefAliasPos:         make(map[int]bool),
	}

	for i, t := range nonTrivia {
		switch t.Kind {
		case syntax.TokenIdent:
			c.handleKeyword(i, strings.ToUpper(t.Text(src)))
		case syntax.TokenOperator:
			if t.Text(src) == "|>" {
				c.handlePipeOperator(i)
			}
		case syntax.TokenPunct:
			c.handlePunctToken(i, t)
		default:
		}
	}

	return tokenAliasInfo{
		aliases:          c.decls,
		withOffsetDecls:  c.withOffsetDecls,
		selectAliasDecls: c.selectAliasDecls,
		loopVarDecls:     c.loopVarDecls,
		graphVarDecls:    c.graphVarDecls,
		funcDefRanges:    c.funcDefRanges,
		tablePos:         c.tablePos,
		colTargetPos:     c.colTargetPos,
		colTargetDecls:   c.colTargetDecls,
		nonRefAliasPos:   c.nonRefAliasPos,
	}
}

func (c *tokenAliasCollector) handlePunctToken(idx int, tok syntax.Token) {
	switch tok.Text(c.src) {
	case "(":
		innerID := -1
		if idx+1 < len(c.nonTrivia) && c.scopes[c.tokScopeIDs[idx+1]].depth > c.scopes[c.tokScopeIDs[idx]].depth {
			innerID = c.tokScopeIDs[idx+1]
		}
		c.parenStack = append(c.parenStack, parenScopeEntry{openIdx: idx, innerScopeID: innerID})
	case ")":
		if len(c.parenStack) == 0 {
			return
		}
		entry := c.parenStack[len(c.parenStack)-1]
		c.parenStack = c.parenStack[:len(c.parenStack)-1]
		if entry.innerScopeID >= 0 {
			c.handleCloseParen(idx, entry.openIdx, entry.innerScopeID)
		}
	case ";":
		c.parenStack = c.parenStack[:0]
		c.handleStatementTerminator(idx, tok.Start)
	default:
	}
}

func (c *tokenAliasCollector) handleCloseParen(closeIdx, openIdx, innerScopeID int) {
	if state, ok := c.pipeIfByOpen[openIdx]; ok {
		c.finishPipeIfBranch(state, innerScopeID)
		return
	}
	if info, ok := c.withCTEOpenToInfo[openIdx]; ok {
		key := strings.ToLower(info.name)
		c.cteSelectAliases[key] = append(c.cteSelectAliases[key], cteAliasEntry{
			cols:  cloneDeclsForScope(c.pipeSelectAliases[innerScopeID], 0),
			scope: info.scope,
		})
		return
	}
	if subq, ok := c.tableSubqueryOpens[openIdx]; ok {
		c.finishTableSubqueryClose(closeIdx, innerScopeID, subq)
		return
	}

	parentScopeID := c.tokScopeIDs[closeIdx]
	if c.seenSelectInChain[parentScopeID] || !c.isParenthesizedQueryStart(openIdx, closeIdx) {
		return
	}

	afterIdx := closeIdx + 1
	if isKeywordAt(c.src, c.nonTrivia, openIdx-1, "FROM") {
		if pipeAfter, ok := c.fromSubqueryPipeAfterIdx(closeIdx + 1); ok {
			afterIdx = pipeAfter
		}
	}
	if afterIdx >= len(c.nonTrivia) {
		return
	}

	isCloseParen := isPunctAt(c.src, c.nonTrivia, afterIdx, ")") && c.scopes[parentScopeID].depth > 0
	if !isCloseParen && c.tokScopeIDs[afterIdx] != parentScopeID {
		return
	}

	isOrder := isKeywordAt(c.src, c.nonTrivia, afterIdx, "ORDER")
	isPipe := isPipeOpAt(c.src, c.nonTrivia, afterIdx)
	isSetOp := isSetOpKeywordAt(c.src, c.nonTrivia, afterIdx)
	if !isOrder && !isPipe && !isSetOp && !isCloseParen {
		return
	}

	aliases := c.pipeSelectAliases[innerScopeID]
	c.seenSelectInChain[parentScopeID] = true
	if len(aliases) == 0 {
		return
	}

	copied := cloneDeclsForScope(aliases, parentScopeID)
	c.pipeSelectAliases[parentScopeID] = append(c.pipeSelectAliases[parentScopeID], copied...)

	if isOrder {
		orderEnd := nextPipeOrderByEndInScope(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, closeIdx, parentScopeID)
		c.emitSelectAliasDeclsFromDecls(copied, c.nonTrivia[afterIdx].Start, orderEnd, parentScopeID)
	} else if isSetOp {
		if orderStart, orderEnd, ok := c.findSetOpTrailingOrderRange(afterIdx, parentScopeID); ok {
			c.emitSelectAliasDeclsFromDecls(copied, orderStart, orderEnd, parentScopeID)
		}
	}
}

func (c *tokenAliasCollector) finishPipeIfBranch(state *pipeIfState, innerScopeID int) {
	copied := cloneDeclsForScope(c.pipeSelectAliases[innerScopeID], state.parentScopeID)
	state.branchOutputs = append(state.branchOutputs, copied)
	state.branchRangeVars = append(state.branchRangeVars, cloneRangeVarColsForScope(c.pipeRangeVarCols[innerScopeID], state.parentScopeID))
	state.branchUnknownRVs = append(state.branchUnknownRVs, maps.Clone(c.pipeUnknownRangeVars[innerScopeID]))
	state.branchDone++
	if state.branchDone < state.branchTotal {
		return
	}

	c.mergePipeIfState(state)
}

func (c *tokenAliasCollector) mergePipeIfState(state *pipeIfState) {
	pid := state.parentScopeID
	var merged []tokenDecl
	for _, branch := range state.branchOutputs {
		merged = appendOrMergeStructDecls(merged, branch)
	}
	if !state.hasElse {
		merged = appendOrMergeStructDecls(merged, c.pipeSelectAliases[pid])
	}
	c.pipeSelectAliases[pid] = merged

	mergedRVs := make(map[string][]tokenDecl)
	for _, br := range state.branchRangeVars {
		mergeRangeVarColsMap(mergedRVs, br)
	}
	mergedUnk := make(map[string]bool)
	for _, bu := range state.branchUnknownRVs {
		maps.Copy(mergedUnk, bu)
	}

	if !state.hasElse {
		mergeRangeVarColsMap(mergedRVs, c.pipeRangeVarCols[pid])
		maps.Copy(mergedUnk, c.pipeUnknownRangeVars[pid])
	}

	c.assignPipeIfMergedMaps(pid, mergedRVs, mergedUnk)
}

func (c *tokenAliasCollector) assignPipeIfMergedMaps(
	pid int,
	mergedRVs map[string][]tokenDecl,
	mergedUnk map[string]bool,
) {
	if len(mergedRVs) > 0 {
		c.pipeRangeVarCols[pid] = mergedRVs
	} else {
		delete(c.pipeRangeVarCols, pid)
	}
	if len(mergedUnk) > 0 {
		c.pipeUnknownRangeVars[pid] = mergedUnk
	} else {
		delete(c.pipeUnknownRangeVars, pid)
	}
}

func (c *tokenAliasCollector) finishTableSubqueryClose(closeIdx, innerScopeID int, subq tableSubqueryMeta) {
	parentScopeID := subq.ownerScopeID
	aliases := c.pipeSelectAliases[innerScopeID]
	if len(aliases) == 0 {
		return
	}

	copied := cloneDeclsForScope(aliases, parentScopeID)
	if subq.isSelectFrom {
		if proj := c.pendingSelect[parentScopeID]; proj != nil {
			anchorTok := c.nonTrivia[closeIdx]
			filtered := filterTableSubqueryDeclsByProj(copied, subq.alias, proj, anchorTok, parentScopeID)
			if proj.isFirstBranch {
				c.pipeSelectAliases[parentScopeID] = appendUniqueDecls(c.pipeSelectAliases[parentScopeID], filtered)
				if aliasedDecls := resolveAliasedProjDecls(copied, subq.alias, proj, anchorTok, parentScopeID); len(aliasedDecls) > 0 {
					c.pipeSelectAliases[parentScopeID] = mergeStructFieldsIntoDecls(c.pipeSelectAliases[parentScopeID], aliasedDecls)
				}
			}
			if pw, ok := c.pendingSelectPostWhere[parentScopeID]; ok {
				c.emitDeclsOverPostWhereSpans(copied, filtered, pw, parentScopeID)
			}
		}
		return
	}

	if subq.alias != "" {
		c.setPipeRangeVarCols(parentScopeID, subq.alias, copied)
	}

	c.pipeSelectAliases[parentScopeID] = appendUniqueDecls(c.pipeSelectAliases[parentScopeID], copied)
	if subq.pipeJoinOnSpan.Start < subq.pipeJoinOnSpan.End {
		c.emitSelectAliasDeclsFromDecls(copied, subq.pipeJoinOnSpan.Start, subq.pipeJoinOnSpan.End, parentScopeID)
	}
	for _, sp := range subq.extraJoinOnSpans {
		if sp.Start < sp.End {
			c.emitSelectAliasDeclsFromDecls(copied, sp.Start, sp.End, parentScopeID)
		}
	}
}

func (c *tokenAliasCollector) emitDeclsOverPostWhereSpans(
	allCols, projCols []tokenDecl,
	pw selectPostWhereSpans,
	scopeID int,
) {
	for _, sp := range pw.joinOnSpans {
		c.emitSelectAliasDeclsFromDecls(allCols, sp.Start, sp.End, scopeID)
	}
	for _, sp := range pw.whereSpans {
		c.emitSelectAliasDeclsFromDecls(allCols, sp.Start, sp.End, scopeID)
	}
	for _, sp := range pw.clauseSpans {
		c.emitSelectAliasDeclsFromDecls(allCols, sp.Start, sp.End, scopeID)
	}
	for _, sp := range pw.groupBySubqSpans {
		c.emitSelectAliasDeclsFromDecls(allCols, sp.Start, sp.End, scopeID)
	}
	for _, sp := range pw.qualifyWindowSpans {
		c.emitSelectAliasDeclsFromDecls(allCols, sp.Start, sp.End, scopeID)
	}
	for _, sp := range pw.allWindowSpans {
		c.emitSelectAliasDeclsFromDecls(allCols, sp.Start, sp.End, scopeID)
	}
	if pw.hasSetOpOrder {
		c.emitSelectAliasDeclsFromDecls(projCols, pw.setOpOrderSpan.Start, pw.setOpOrderSpan.End, scopeID)
	}
}

func cloneDeclsForScope(decls []tokenDecl, scopeID int) []tokenDecl {
	if len(decls) == 0 {
		return nil
	}

	copied := make([]tokenDecl, len(decls))
	for i, d := range decls {
		d.scopeID = scopeID
		copied[i] = d
	}

	return copied
}

func appendUniqueDecls(dst, src []tokenDecl) []tokenDecl {
	for _, d := range src {
		if !containsDeclName(dst, d.name) {
			dst = append(dst, d)
		}
	}

	return dst
}

func appendOrMergeStructDecls(dst, src []tokenDecl) []tokenDecl {
	for _, d := range src {
		idx := findDeclIndexByName(dst, d.name)
		if idx < 0 {
			dst = append(dst, d)
			continue
		}
		if len(dst[idx].structFields) == 0 && len(d.structFields) > 0 {
			dst[idx].structFields = d.structFields
		}
	}

	return dst
}

func mergeStructFieldsIntoDecls(dst, src []tokenDecl) []tokenDecl {
	for _, d := range src {
		if len(d.structFields) == 0 {
			continue
		}
		if idx := findDeclIndexByName(dst, d.name); idx >= 0 && len(dst[idx].structFields) == 0 {
			dst[idx].structFields = d.structFields
		}
	}

	return dst
}

func (c *tokenAliasCollector) setPipeRangeVarCols(scopeID int, aliasName string, cols []tokenDecl) {
	if aliasName == "" {
		return
	}

	m := c.pipeRangeVarCols[scopeID]
	if m == nil {
		m = make(map[string][]tokenDecl)
		c.pipeRangeVarCols[scopeID] = m
	}
	m[strings.ToLower(aliasName)] = cloneDeclsForScope(cols, scopeID)
}

func (c *tokenAliasCollector) setPipeUnknownRangeVar(scopeID int, aliasName string) {
	if aliasName == "" {
		return
	}

	m := c.pipeUnknownRangeVars[scopeID]
	if m == nil {
		m = make(map[string]bool)
		c.pipeUnknownRangeVars[scopeID] = m
	}
	m[strings.ToLower(aliasName)] = true
}

func (c *tokenAliasCollector) clearPipeRangeVars(scopeID int) {
	delete(c.pipeRangeVarCols, scopeID)
	delete(c.pipeUnknownRangeVars, scopeID)
}

func cloneRangeVarColsForScope(src map[string][]tokenDecl, scopeID int) map[string][]tokenDecl {
	if len(src) == 0 {
		return nil
	}

	out := make(map[string][]tokenDecl, len(src))
	for k, v := range src {
		out[k] = cloneDeclsForScope(v, scopeID)
	}

	return out
}

func mergeRangeVarColsMap(dst, src map[string][]tokenDecl) {
	for k, v := range src {
		dst[k] = appendOrMergeStructDecls(dst[k], v)
	}
}

func (c *tokenAliasCollector) fromSubqueryPipeAfterIdx(startIdx int) (int, bool) {
	idx := skipHintIfPresent(c.src, c.nonTrivia, startIdx)
	if isPipeOpAt(c.src, c.nonTrivia, idx) {
		return idx, true
	}
	if isKeywordAt(c.src, c.nonTrivia, idx, "AS") &&
		idx+1 < len(c.nonTrivia) &&
		isIdentOrBacktick(c.nonTrivia[idx+1]) &&
		isPipeOpAt(c.src, c.nonTrivia, idx+2) {
		return idx + 2, true
	}
	if idx < len(c.nonTrivia) &&
		isBareAliasIdent(c.src, c.nonTrivia[idx]) &&
		isPipeOpAt(c.src, c.nonTrivia, idx+1) {
		return idx + 1, true
	}

	return 0, false
}

func (c *tokenAliasCollector) isParenthesizedQueryStart(openIdx, closeIdx int) bool {
	if openIdx == 0 {
		return true
	}

	prev := c.nonTrivia[openIdx-1]
	if prev.Kind == syntax.TokenPunct {
		switch prev.Text(c.src) {
		case ";", "(":
			return true
		case ")":
			return !c.seenSelectInChain[c.tokScopeIDs[openIdx]]
		default:
		}
	}
	if isKeywordAt(c.src, c.nonTrivia, openIdx-1, "AS") {
		return true
	}
	if isKeywordAt(c.src, c.nonTrivia, openIdx-1, "FROM") {
		_, isPipe := c.fromSubqueryPipeAfterIdx(closeIdx + 1)
		return isPipe
	}

	return false
}

func (c *tokenAliasCollector) handleStatementTerminator(idx, pos int) {
	scopeID := c.tokScopeIDs[idx]
	c.clampDeclsAtBarrier(scopeID, pos)
	delete(c.pipeSelectAliases, scopeID)
	c.clearPipeRangeVars(scopeID)
	delete(c.pendingSelect, scopeID)
	delete(c.pendingSelectPostWhere, scopeID)
	delete(c.seenSelectInChain, scopeID)
	if scopeID == 0 {
		c.mergeTargetDecls = nil
		clear(c.pipeSelectAliases)
		clear(c.pipeRangeVarCols)
		clear(c.pipeUnknownRangeVars)
		clear(c.pipeIfByOpen)
		clear(c.tableSubqueryOpens)
		clear(c.withCTEOpenToInfo)
		clear(c.cteSelectAliases)
		clear(c.pendingSelect)
		clear(c.pendingSelectPostWhere)
		clear(c.seenSelectInChain)
	}
}

func (c *tokenAliasCollector) handleKeyword(idx int, kw string) {
	switch kw {
	case "WITH":
		c.recordWithClauseOpenParens(idx)
	case "FROM":
		scopeID := c.tokScopeIDs[idx]
		if !c.scopes[scopeID].isCallClause {
			c.handleFromKeyword(idx+1, scopeID)
		}
	case "SELECT":
		c.handleSelectKeyword(idx)
	case "UPDATE":
		if !isInsertModeUpdate(c.src, c.nonTrivia, idx) {
			c.handleUpdateKeyword(idx + 1)
		}
	case "DELETE":
		if idx+1 < len(c.nonTrivia) && !strings.EqualFold(c.nonTrivia[idx+1].Text(c.src), "FROM") {
			decls, _ := c.parseTableFactorAlias(idx + 1)
			c.decls = append(c.decls, decls...)
		}
	case "MERGE":
		c.decls = append(c.decls, c.parseMergeAliases(idx+1)...)
	case "INSERT":
		c.markInsertTargetAndCols(idx + 1)
	case "EXCEPT":
		c.markStarExceptCols(idx)
	case "REPLACE":
		c.markStarReplaceAliases(idx)
	case "TABLESAMPLE":
		if idx+1 < len(c.nonTrivia) && isIdentOrBacktick(c.nonTrivia[idx+1]) {
			c.nonRefAliasPos[idx+1] = true
		}
	case "CALL":
		c.markDottedIdentTokensNonRef(idx + 1)
	case "FOR":
		c.handleForInKeyword(idx)
	case "CREATE":
		c.handleCreateKeyword(idx)
	case "GRAPH_TABLE":
		c.handleGraphTableKeyword(idx)
	default:
	}
}

func (c *tokenAliasCollector) recordWithClauseOpenParens(withIdx int) {
	withScope := c.scopes[c.tokScopeIDs[withIdx]]
	j := withIdx + 1
	if isKeywordAt(c.src, c.nonTrivia, j, "RECURSIVE") {
		j++
	}

	for j < len(c.nonTrivia) && isTablePathIdent(c.src, c.nonTrivia[j]) {
		cteName := strings.Trim(c.nonTrivia[j].Text(c.src), "`")
		j++
		if isPunctAt(c.src, c.nonTrivia, j, "(") {
			j = skipBalancedParens(c.src, c.nonTrivia, j)
		}
		if !isKeywordAt(c.src, c.nonTrivia, j, "AS") || !isPunctAt(c.src, c.nonTrivia, j+1, "(") {
			break
		}
		c.withCTEOpenToInfo[j+1] = cteOpenMeta{name: cteName, scope: withScope}
		j = skipBalancedParens(c.src, c.nonTrivia, j+1)
		if !isPunctAt(c.src, c.nonTrivia, j, ",") {
			break
		}
		j++
	}
}

type graphMatchScanState struct {
	afterParen     int
	graphEnd       int
	graphScope     tokenScope
	inMatch        bool
	elemWhereDepth int
	st             updateSetState
}

func (c *tokenAliasCollector) handleGraphTableKeyword(graphIdx int) {
	if !isPunctAt(c.src, c.nonTrivia, graphIdx+1, "(") {
		return
	}

	openTok := c.nonTrivia[graphIdx+1]
	scopeID := c.tokScopeIDs[graphIdx+1]
	graphDepth := c.scopes[scopeID].depth + 1
	afterParen := skipBalancedParens(c.src, c.nonTrivia, graphIdx+1)
	graphEnd := c.graphTableEndOffset(graphIdx, scopeID, afterParen)
	state := graphMatchScanState{
		afterParen:     afterParen,
		graphEnd:       graphEnd,
		graphScope:     tokenScope{depth: graphDepth, start: openTok.Start, end: graphEnd},
		elemWhereDepth: -1,
	}

	for k := graphIdx + 2; k < afterParen; k++ {
		if !c.stepGraphTableToken(k, &state) {
			break
		}
	}
}

func (c *tokenAliasCollector) stepGraphTableToken(k int, state *graphMatchScanState) bool {
	tok := c.nonTrivia[k]
	if tok.Start >= state.graphEnd || (tok.Kind == syntax.TokenPunct && tok.Text(c.src) == ";") {
		return false
	}

	txt := tok.Text(c.src)
	if state.elemWhereDepth >= 0 && state.st.parenDepth+state.st.bracketDepth < state.elemWhereDepth {
		state.elemWhereDepth = -1
	}

	if state.st.parenDepth == 0 && state.st.bracketDepth == 0 {
		state.inMatch = updateGraphMatchClauseState(tok, txt, state.inMatch)
		if state.inMatch && state.elemWhereDepth < 0 {
			c.recordGraphPathVar(k, state.afterParen, state.graphEnd, state.graphScope)
		}
	} else if state.elemWhereDepth < 0 && isKeywordAt(c.src, c.nonTrivia, k, "WHERE") {
		state.elemWhereDepth = state.st.parenDepth + state.st.bracketDepth
	}

	if state.inMatch && state.elemWhereDepth < 0 && (isPunctAt(c.src, c.nonTrivia, k, "(") || isPunctAt(c.src, c.nonTrivia, k, "[")) {
		c.recordGraphElemVar(k+1, state.afterParen, state.graphScope)
	}
	updateSetNesting(tok, txt, &state.st)

	return true
}

func (c *tokenAliasCollector) recordGraphPathVar(candIdx, limit, graphEnd int, graphScope tokenScope) {
	if candIdx+1 >= limit {
		return
	}

	cand := c.nonTrivia[candIdx]
	eqTok := c.nonTrivia[candIdx+1]
	if eqTok.Start >= graphEnd || eqTok.Kind != syntax.TokenOperator || eqTok.Text(c.src) != "=" {
		return
	}
	if !isBareAliasIdent(c.src, cand) {
		return
	}

	c.nonRefAliasPos[candIdx] = true
	c.graphVarDecls = append(c.graphVarDecls, tokenDecl{
		name:      strings.Trim(cand.Text(c.src), "`"),
		nameStart: cand.Start,
		nameEnd:   cand.End,
		scope:     graphScope,
		scopeID:   c.tokScopeIDs[candIdx],
		explicit:  true,
	})
}

func (c *tokenAliasCollector) graphTableEndOffset(graphIdx, scopeID, afterParen int) int {
	graphEnd := c.scopes[scopeID].end
	if graphIdx+2 < len(c.nonTrivia) {
		if innerEnd := c.scopes[c.tokScopeIDs[graphIdx+2]].end; innerEnd < graphEnd {
			graphEnd = innerEnd
		}
	}
	if afterParen > graphIdx+2 && afterParen <= len(c.nonTrivia) && isPunctAt(c.src, c.nonTrivia, afterParen-1, ")") {
		if parenEnd := c.nonTrivia[afterParen-1].End; parenEnd < graphEnd {
			graphEnd = parenEnd
		}
	}
	if stmtEnd := nextStmtEndInScope(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, graphIdx, scopeID, graphEnd); stmtEnd < graphEnd {
		graphEnd = stmtEnd
	}

	return graphEnd
}

func updateGraphMatchClauseState(tok syntax.Token, txt string, inMatch bool) bool {
	if tok.Kind != syntax.TokenIdent {
		return inMatch
	}

	switch strings.ToUpper(txt) {
	case "MATCH":
		return true
	case "WHERE", "COLUMNS", "RETURN":
		return false
	default:
		return inMatch
	}
}

func (c *tokenAliasCollector) recordGraphElemVar(candIdx, limit int, graphScope tokenScope) {
	if candIdx+1 >= limit {
		return
	}

	cand := c.nonTrivia[candIdx]
	if !isBareAliasIdent(c.src, cand) || !isGraphElemVarFollower(c.src, c.nonTrivia[candIdx+1]) {
		return
	}

	c.nonRefAliasPos[candIdx] = true
	c.graphVarDecls = append(c.graphVarDecls, tokenDecl{
		name:      strings.Trim(cand.Text(c.src), "`"),
		nameStart: cand.Start,
		nameEnd:   cand.End,
		scope:     graphScope,
		scopeID:   c.tokScopeIDs[candIdx],
		explicit:  true,
	})
}

func isGraphElemVarFollower(src string, tok syntax.Token) bool {
	if tok.Kind == syntax.TokenPunct {
		switch tok.Text(src) {
		case ")", "]", ":", "{":
			return true
		default:
			return false
		}
	}

	if tok.Kind == syntax.TokenIdent {
		return strings.EqualFold(tok.Text(src), "IS") || strings.EqualFold(tok.Text(src), "WHERE")
	}

	return false
}

func (c *tokenAliasCollector) markDottedIdentTokensNonRef(idx int) int {
	for idx < len(c.nonTrivia) {
		if !isIdentOrBacktick(c.nonTrivia[idx]) {
			return idx
		}
		c.nonRefAliasPos[idx] = true
		idx++
		if !isPunctAt(c.src, c.nonTrivia, idx, ".") {
			return idx
		}
		idx++
	}

	return idx
}

func (c *tokenAliasCollector) handleSelectKeyword(selectIdx int) {
	if isPipeOpAt(c.src, c.nonTrivia, selectIdx-1) {
		return
	}

	scopeID := c.tokScopeIDs[selectIdx]
	isFirstBranch := !c.seenSelectInChain[scopeID]
	c.seenSelectInChain[scopeID] = true

	topAliases, clauseIdx := c.scanSelectListAsAliases(selectIdx, scopeID)
	proj := c.scanSelectProjection(selectIdx, clauseIdx, scopeID)
	proj.isFirstBranch = isFirstBranch
	projFromDecls, allFromDecls, aliasedStructs := c.resolveSelectFromTableDecls(clauseIdx, scopeID, proj)

	topAliasDecls := c.tokenDeclsFromIndices(scopeID, topAliases)
	attachStructFieldsToDecls(topAliasDecls, aliasedStructs)
	pipeDecls := appendUniqueDecls(topAliasDecls, projFromDecls)
	aliasAndProjDecls := appendUniqueDecls(topAliasDecls, projFromDecls)
	allWindowDecls := appendUniqueDecls(projFromDecls, allFromDecls)
	hasFrom := isKeywordAt(c.src, c.nonTrivia, clauseIdx, "FROM")

	if isFirstBranch {
		c.pipeSelectAliases[scopeID] = appendUniqueDecls(c.pipeSelectAliases[scopeID], pipeDecls)
	}
	if hasFrom {
		c.pendingSelect[scopeID] = proj
	} else {
		delete(c.pendingSelect, scopeID)
		delete(c.pendingSelectPostWhere, scopeID)
	}

	if len(aliasAndProjDecls) == 0 && len(allFromDecls) == 0 && !hasFrom {
		return
	}

	spans, stoppedAtSetOp := c.findSelectPostWhereRanges(selectIdx, clauseIdx, scopeID, !isFirstBranch)
	if isFirstBranch && stoppedAtSetOp {
		if orderStart, orderEnd, ok := c.findSetOpTrailingOrderRange(clauseIdx, scopeID); ok {
			spans.setOpOrderSpan = syntax.ByteSpan{Start: orderStart, End: orderEnd}
			spans.hasSetOpOrder = true
		}
	}

	if hasFrom {
		c.pendingSelectPostWhere[scopeID] = spans
	}

	c.emitSelectPostWhereDecls(aliasAndProjDecls, allFromDecls, allWindowDecls, pipeDecls, spans, scopeID)
}

func (c *tokenAliasCollector) emitSelectPostWhereDecls(
	aliasAndProjDecls, allFromDecls, allWindowDecls, pipeDecls []tokenDecl,
	spans selectPostWhereSpans,
	scopeID int,
) {
	postWhereDecls := appendUniqueDecls(aliasAndProjDecls, allFromDecls)
	for _, sp := range spans.joinOnSpans {
		c.emitSelectAliasDeclsFromDecls(allFromDecls, sp.Start, sp.End, scopeID)
	}
	for _, sp := range spans.whereSpans {
		c.emitSelectAliasDeclsFromDecls(allFromDecls, sp.Start, sp.End, scopeID)
	}
	for _, sp := range spans.clauseSpans {
		c.emitSelectAliasDeclsFromDecls(postWhereDecls, sp.Start, sp.End, scopeID)
	}
	for _, sp := range spans.groupBySubqSpans {
		c.emitSelectAliasDeclsFromDecls(allFromDecls, sp.Start, sp.End, scopeID)
	}
	for _, sp := range spans.qualifyWindowSpans {
		c.emitSelectAliasDeclsFromDecls(postWhereDecls, sp.Start, sp.End, scopeID)
	}
	for _, sp := range spans.allWindowSpans {
		c.emitSelectAliasDeclsFromDecls(allWindowDecls, sp.Start, sp.End, scopeID)
	}
	if spans.hasSetOpOrder {
		c.emitSelectAliasDeclsFromDecls(pipeDecls, spans.setOpOrderSpan.Start, spans.setOpOrderSpan.End, scopeID)
	}
}

func (c *tokenAliasCollector) emitSelectAliasDeclsFromDecls(decls []tokenDecl, startOff, endOff, scopeID int) {
	if len(decls) == 0 || startOff >= endOff {
		return
	}

	sc := tokenScope{depth: c.scopes[scopeID].depth, start: startOff, end: endOff}
	for _, d := range decls {
		d.scope = sc
		d.scopeID = scopeID
		c.selectAliasDecls = append(c.selectAliasDecls, d)
	}
}

func (c *tokenAliasCollector) scanSelectListAsAliases(selectIdx, scopeID int) ([]int, int) {
	var topAliases []int
	st := updateSetState{}
	for j := selectIdx + 1; j < len(c.nonTrivia); j++ {
		tok := c.nonTrivia[j]
		txt := tok.Text(c.src)
		if c.scopes[c.tokScopeIDs[j]].depth < c.scopes[scopeID].depth {
			return topAliases, j
		}
		if st.parenDepth == 0 && st.bracketDepth == 0 && st.caseDepth == 0 && isSelectListTerminator(c.src, c.nonTrivia, j) {
			return topAliases, j
		}
		if isKeywordAt(c.src, c.nonTrivia, j, "MATCH_RECOGNIZE") && isPunctAt(c.src, c.nonTrivia, j+1, "(") {
			j = skipBalancedParens(c.src, c.nonTrivia, j+1) - 1
			continue
		}
		if nextJ, aliases, ok := c.tryConsumeStarReplaceAliases(j, &st); ok {
			topAliases = append(topAliases, aliases...)
			j = nextJ - 1
			continue
		}
		if nextJ, recorded, handled := c.tryConsumeSelectAsAlias(selectIdx, j, &st); handled {
			if recorded >= 0 {
				topAliases = append(topAliases, recorded)
			}
			j = nextJ - 1
			continue
		}

		updateSetNesting(tok, txt, &st)
		if st.parenDepth < 0 || st.bracketDepth < 0 {
			return topAliases, j
		}
	}

	return topAliases, len(c.nonTrivia)
}

func (c *tokenAliasCollector) tryConsumeStarReplaceAliases(j int, st *updateSetState) (int, []int, bool) {
	if st.parenDepth != 0 || st.bracketDepth != 0 || st.caseDepth != 0 {
		return j, nil, false
	}
	if !isKeywordAt(c.src, c.nonTrivia, j, "REPLACE") ||
		!isStarModifierReplace(c.src, c.nonTrivia, j) ||
		!isPunctAt(c.src, c.nonTrivia, j+1, "(") {
		return j, nil, false
	}

	afterRep := skipBalancedParens(c.src, c.nonTrivia, j+1)
	repEnd := afterRep
	if repEnd > j+2 && isPunctAt(c.src, c.nonTrivia, repEnd-1, ")") {
		repEnd--
	}

	return afterRep, c.markTopLevelAsAliases(j+2, repEnd), true
}

func (c *tokenAliasCollector) tryConsumeSelectAsAlias(selectIdx, j int, st *updateSetState) (int, int, bool) {
	if st.caseDepth != 0 || !isKeywordAt(c.src, c.nonTrivia, j, "AS") || j+1 >= len(c.nonTrivia) {
		return j, -1, false
	}

	nextTok := c.nonTrivia[j+1]
	if st.parenDepth == 0 && j == selectIdx+1 &&
		(isKeywordAt(c.src, c.nonTrivia, j+1, "STRUCT") || isKeywordAt(c.src, c.nonTrivia, j+1, "VALUE")) {
		return j + 2, -1, true
	}
	if !isIdentOrBacktick(nextTok) {
		return j, -1, false
	}

	c.nonRefAliasPos[j+1] = true
	if st.parenDepth == 0 && st.bracketDepth == 0 {
		return j + 2, j + 1, true
	}

	return j + 2, -1, true
}

func isSelectListTerminator(src string, nonTrivia []syntax.Token, idx int) bool {
	tok := nonTrivia[idx]
	txt := tok.Text(src)
	if tok.Kind == syntax.TokenPunct && (txt == ";" || txt == ")") {
		return true
	}
	if tok.Kind == syntax.TokenOperator && txt == "|>" {
		return true
	}
	if tok.Kind != syntax.TokenIdent {
		return false
	}

	switch strings.ToUpper(txt) {
	case "EXCEPT":
		return !isStarOperatorAt(src, nonTrivia, idx-1)
	case "FROM", "WHERE", "GROUP", "HAVING", "QUALIFY", "WINDOW", "ORDER", "LIMIT",
		"FETCH", "UNION", "INTERSECT", "INTO", "SET", "VALUES":
		return true
	default:
		return false
	}
}

type postWhereScanState struct {
	postWhereStart   int
	whereStart       int
	onStart          int
	inFromClause     bool
	seenJoin         bool
	queryEnd         int
	queryEndIdx      int
	qualifyStartIdx  int
	qualifyEndIdx    int
	inGroupBy        bool
	groupBySubqSpans []syntax.ByteSpan
	joinOnSpans      []syntax.ByteSpan
	windowStarts     []int
	whereRanges      []syntax.ByteSpan
	ranges           []syntax.ByteSpan
	stoppedAtSetOp   bool
}

func (c *tokenAliasCollector) findSelectPostWhereRanges(
	selectIdx, startIdx, scopeID int,
	stopAtOrder bool,
) (spans selectPostWhereSpans, stoppedAtSetOp bool) {
	st := postWhereScanState{
		postWhereStart:  -1,
		whereStart:      -1,
		onStart:         -1,
		inFromClause:    isKeywordAt(c.src, c.nonTrivia, startIdx, "FROM"),
		queryEnd:        c.scopes[scopeID].end,
		queryEndIdx:     len(c.nonTrivia),
		qualifyStartIdx: -1,
		qualifyEndIdx:   -1,
	}
	scopeDepth := c.scopes[scopeID].depth

	for k := startIdx; k < len(c.nonTrivia); k++ {
		if c.scopes[c.tokScopeIDs[k]].depth < scopeDepth {
			st.queryEnd = c.nonTrivia[k].Start
			st.queryEndIdx = k
			break
		}
		if st.inGroupBy && isPunctAt(c.src, c.nonTrivia, k, "(") && c.isSubqueryParen(k) {
			afterParen := skipBalancedParens(c.src, c.nonTrivia, k)
			endTok := min(afterParen-1, len(c.nonTrivia)-1)
			if endTok >= k {
				st.groupBySubqSpans = append(st.groupBySubqSpans, syntax.ByteSpan{
					Start: c.nonTrivia[k].Start,
					End:   c.nonTrivia[endTok].End,
				})
			}
		}
		if c.tokScopeIDs[k] != scopeID {
			continue
		}
		if c.stepPostWhereToken(k, stopAtOrder, &st) {
			break
		}
	}

	if st.qualifyStartIdx >= 0 && st.qualifyEndIdx < 0 {
		st.qualifyEndIdx = st.queryEndIdx
	}
	closeJoinOnRange(st.queryEnd, &st)
	closeWhereRange(st.queryEnd, &st)
	if st.postWhereStart >= 0 && st.postWhereStart < st.queryEnd {
		st.ranges = append(st.ranges, syntax.ByteSpan{Start: st.postWhereStart, End: st.queryEnd})
	}

	spans.joinOnSpans = st.joinOnSpans
	spans.whereSpans = st.whereRanges
	spans.groupBySubqSpans = clampByteSpansEnd(st.groupBySubqSpans, st.queryEnd)
	spans.clauseSpans = subtractByteSpans(st.ranges, spans.groupBySubqSpans)
	if len(st.windowStarts) > 0 {
		spans.allWindowSpans = c.collectAllWindowRanges(st.windowStarts, st.queryEndIdx)
		if st.qualifyStartIdx >= 0 {
			spans.qualifyWindowSpans = c.collectQualifyWindowRanges(selectIdx, startIdx, st.qualifyStartIdx, st.qualifyEndIdx, st.windowStarts, st.queryEndIdx)
		}
	}

	return spans, st.stoppedAtSetOp
}

func closeJoinOnRange(endOff int, st *postWhereScanState) {
	if st.onStart >= 0 && st.onStart < endOff {
		st.joinOnSpans = append(st.joinOnSpans, syntax.ByteSpan{Start: st.onStart, End: endOff})
	}
	st.onStart = -1
}

func closeWhereRange(endOff int, st *postWhereScanState) {
	if st.whereStart >= 0 && st.whereStart < endOff {
		st.whereRanges = append(st.whereRanges, syntax.ByteSpan{Start: st.whereStart, End: endOff})
	}
	st.whereStart = -1
}

func clampByteSpansEnd(spans []syntax.ByteSpan, maxEnd int) []syntax.ByteSpan {
	if len(spans) == 0 {
		return nil
	}

	out := make([]syntax.ByteSpan, 0, len(spans))
	for _, sp := range spans {
		end := min(sp.End, maxEnd)
		if sp.Start < end {
			out = append(out, syntax.ByteSpan{Start: sp.Start, End: end})
		}
	}

	return out
}

func (c *tokenAliasCollector) stepPostWhereToken(k int, stopAtOrder bool, st *postWhereScanState) bool {
	tok := c.nonTrivia[k]
	txt := tok.Text(c.src)
	if isSelectQueryEndToken(c.src, c.nonTrivia, k, stopAtOrder) {
		closeJoinOnRange(tok.Start, st)
		closeWhereRange(tok.Start, st)
		st.queryEnd = tok.Start
		st.queryEndIdx = k
		st.stoppedAtSetOp = isSetOpKeywordAt(c.src, c.nonTrivia, k)
		st.inFromClause = false
		st.seenJoin = false
		st.inGroupBy = false
		return true
	}

	if st.inFromClause {
		c.updateFromJoinOnBounds(tok, txt, st)
	}

	if tok.Kind == syntax.TokenIdent && strings.EqualFold(txt, "WINDOW") {
		closeWhereRange(tok.Start, st)
		if st.qualifyStartIdx >= 0 && st.qualifyEndIdx < 0 {
			st.qualifyEndIdx = k
		}
		if st.postWhereStart >= 0 && st.postWhereStart < tok.Start {
			st.ranges = append(st.ranges, syntax.ByteSpan{Start: st.postWhereStart, End: tok.Start})
		}
		st.windowStarts = append(st.windowStarts, k)
		st.postWhereStart = -1
		st.inGroupBy = false
		return false
	}

	c.updatePostWhereClauseBounds(k, tok, txt, st)

	return false
}

func (c *tokenAliasCollector) updateFromJoinOnBounds(tok syntax.Token, txt string, st *postWhereScanState) {
	if tok.Kind == syntax.TokenPunct && txt == "," {
		closeJoinOnRange(tok.Start, st)
		st.seenJoin = false
		return
	}
	if tok.Kind != syntax.TokenIdent {
		return
	}

	upper := strings.ToUpper(txt)
	if upper == "JOIN" {
		closeJoinOnRange(tok.Start, st)
		st.seenJoin = true
		return
	}
	if upper == "ON" {
		if st.seenJoin && st.onStart < 0 {
			st.onStart = tok.End
		}
		return
	}
	if _, stop := fromTerminatorKeywords[upper]; stop {
		closeJoinOnRange(tok.Start, st)
		st.inFromClause = false
		st.seenJoin = false
	}
}

func (c *tokenAliasCollector) updatePostWhereClauseBounds(k int, tok syntax.Token, txt string, st *postWhereScanState) {
	if tok.Kind == syntax.TokenIdent && strings.EqualFold(txt, "QUALIFY") && st.qualifyStartIdx < 0 {
		st.qualifyStartIdx = k
	} else if st.qualifyStartIdx >= 0 && st.qualifyEndIdx < 0 && isSelectPostWhereClauseKeyword(tok, txt) {
		st.qualifyEndIdx = k
	}
	if isSelectPostWhereClauseKeyword(tok, txt) {
		closeWhereRange(tok.Start, st)
		st.inGroupBy = strings.EqualFold(txt, "GROUP")
		if st.postWhereStart < 0 {
			st.postWhereStart = tok.Start
		}
		return
	}
	if tok.Kind == syntax.TokenIdent && strings.EqualFold(txt, "WHERE") && st.whereStart < 0 && st.postWhereStart < 0 {
		st.whereStart = tok.Start
	}
}

func (c *tokenAliasCollector) isSubqueryParen(openIdx int) bool {
	idx := openIdx + 1
	for idx < len(c.nonTrivia) && isPunctAt(c.src, c.nonTrivia, idx, "(") {
		afterInner := skipBalancedParens(c.src, c.nonTrivia, idx)
		if afterInner >= len(c.nonTrivia) || !isSubqueryContinuationToken(c.src, c.nonTrivia, afterInner) {
			return false
		}
		idx++
	}
	if idx >= len(c.nonTrivia) {
		return false
	}

	return isKeywordAt(c.src, c.nonTrivia, idx, "SELECT") ||
		isKeywordAt(c.src, c.nonTrivia, idx, "WITH") ||
		isKeywordAt(c.src, c.nonTrivia, idx, "FROM")
}

func isSubqueryContinuationToken(src string, nonTrivia []syntax.Token, idx int) bool {
	if idx >= len(nonTrivia) {
		return false
	}
	if isPunctAt(src, nonTrivia, idx, ")") {
		return true
	}

	return isSetOpKeywordAt(src, nonTrivia, idx) ||
		isKeywordAt(src, nonTrivia, idx, "ORDER") ||
		isKeywordAt(src, nonTrivia, idx, "LIMIT")
}

func subtractByteSpans(spans, holes []syntax.ByteSpan) []syntax.ByteSpan {
	if len(holes) == 0 || len(spans) == 0 {
		return spans
	}

	out := spans
	for _, h := range holes {
		var next []syntax.ByteSpan
		for _, sp := range out {
			if h.End <= sp.Start || h.Start >= sp.End {
				next = append(next, sp)
				continue
			}
			if sp.Start < h.Start {
				next = append(next, syntax.ByteSpan{Start: sp.Start, End: h.Start})
			}
			if h.End < sp.End {
				next = append(next, syntax.ByteSpan{Start: h.End, End: sp.End})
			}
		}
		out = next
	}

	return out
}

func (c *tokenAliasCollector) collectAllWindowRanges(windowStarts []int, queryEndIdx int) []syntax.ByteSpan {
	var spans []syntax.ByteSpan
	for _, winKW := range windowStarts {
		spans = append(spans, c.scanWindowClauseAllSpans(winKW+1, queryEndIdx)...)
	}

	return spans
}

func (c *tokenAliasCollector) scanWindowClauseAllSpans(startIdx, queryEndIdx int) []syntax.ByteSpan {
	var spans []syntax.ByteSpan
	k := startIdx
	for k < queryEndIdx {
		if !isIdentOrBacktick(c.nonTrivia[k]) || !isKeywordAt(c.src, c.nonTrivia, k+1, "AS") || !isPunctAt(c.src, c.nonTrivia, k+2, "(") {
			break
		}
		afterParen := skipBalancedParens(c.src, c.nonTrivia, k+2)
		endTokIdx := min(afterParen-1, queryEndIdx-1, len(c.nonTrivia)-1)
		if endTokIdx >= k+2 {
			spans = append(spans, syntax.ByteSpan{
				Start: c.nonTrivia[k+2].Start,
				End:   c.nonTrivia[endTokIdx].End,
			})
		}
		if afterParen >= queryEndIdx || !isPunctAt(c.src, c.nonTrivia, afterParen, ",") {
			break
		}
		k = afterParen + 1
	}

	return spans
}

func (c *tokenAliasCollector) collectQualifyWindowRanges(
	selectIdx, selectEndIdx, qualifyStartIdx, qualifyEndIdx int,
	windowStarts []int,
	queryEndIdx int,
) []syntax.ByteSpan {
	usedInSelect := c.collectOverWindowNames(selectIdx, selectEndIdx)
	usedInQualify := c.collectOverWindowNames(qualifyStartIdx, qualifyEndIdx)
	if len(usedInQualify) == 0 {
		return nil
	}

	var spans []syntax.ByteSpan
	for _, winKW := range windowStarts {
		spans = append(spans, c.scanWindowClauseQualifySpans(winKW+1, queryEndIdx, usedInSelect, usedInQualify)...)
	}

	return spans
}

func (c *tokenAliasCollector) scanWindowClauseQualifySpans(
	startIdx, queryEndIdx int,
	usedInSelect, usedInQualify map[string]bool,
) []syntax.ByteSpan {
	var spans []syntax.ByteSpan
	k := startIdx
	for k < queryEndIdx {
		if !isIdentOrBacktick(c.nonTrivia[k]) || !isKeywordAt(c.src, c.nonTrivia, k+1, "AS") || !isPunctAt(c.src, c.nonTrivia, k+2, "(") {
			break
		}
		winName := strings.ToLower(strings.Trim(c.nonTrivia[k].Text(c.src), "`"))
		afterParen := skipBalancedParens(c.src, c.nonTrivia, k+2)
		if usedInQualify[winName] && !usedInSelect[winName] {
			endTokIdx := min(afterParen-1, queryEndIdx-1, len(c.nonTrivia)-1)
			if endTokIdx >= k+2 {
				spans = append(spans, syntax.ByteSpan{
					Start: c.nonTrivia[k+2].Start,
					End:   c.nonTrivia[endTokIdx].End,
				})
			}
		}
		if afterParen >= queryEndIdx || !isPunctAt(c.src, c.nonTrivia, afterParen, ",") {
			break
		}
		k = afterParen + 1
	}

	return spans
}

func (c *tokenAliasCollector) collectOverWindowNames(startIdx, endIdx int) map[string]bool {
	out := make(map[string]bool)
	limit := min(endIdx, len(c.nonTrivia))
	for i := startIdx; i < limit; i++ {
		if !isKeywordAt(c.src, c.nonTrivia, i, "OVER") {
			continue
		}
		if name, ok := c.windowRefAfterOver(i+1, limit); ok {
			out[strings.ToLower(name)] = true
		}
	}

	return out
}

func (c *tokenAliasCollector) windowRefAfterOver(idx, limit int) (string, bool) {
	if idx >= limit {
		return "", false
	}
	if isIdentOrBacktick(c.nonTrivia[idx]) {
		return strings.Trim(c.nonTrivia[idx].Text(c.src), "`"), true
	}
	if isPunctAt(c.src, c.nonTrivia, idx, "(") && idx+1 < limit && isIdentOrBacktick(c.nonTrivia[idx+1]) {
		cand := strings.Trim(c.nonTrivia[idx+1].Text(c.src), "`")
		switch strings.ToUpper(cand) {
		case "PARTITION", "ORDER", "ROWS", "RANGE", "GROUPS":
			return "", false
		default:
			return cand, true
		}
	}

	return "", false
}

func (c *tokenAliasCollector) findSetOpTrailingOrderRange(startIdx, scopeID int) (orderStart, orderEnd int, ok bool) {
	scopeDepth := c.scopes[scopeID].depth
	seenSetOp := false

	for k := startIdx; k < len(c.nonTrivia); k++ {
		if c.scopes[c.tokScopeIDs[k]].depth < scopeDepth {
			break
		}
		if c.tokScopeIDs[k] != scopeID {
			continue
		}
		tok := c.nonTrivia[k]
		if (tok.Kind == syntax.TokenPunct && tok.Text(c.src) == ";") ||
			(tok.Kind == syntax.TokenOperator && tok.Text(c.src) == "|>") {
			break
		}
		if isSetOpKeywordAt(c.src, c.nonTrivia, k) {
			seenSetOp = true
			continue
		}
		if seenSetOp && isKeywordAt(c.src, c.nonTrivia, k, "ORDER") {
			orderStart = tok.Start
			orderEnd = nextPipeOrderByEndInScope(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, k, scopeID)
			return orderStart, orderEnd, orderStart < orderEnd
		}
	}

	return 0, 0, false
}

func isSetOpKeywordAt(src string, nonTrivia []syntax.Token, idx int) bool {
	if idx < 0 || idx >= len(nonTrivia) || nonTrivia[idx].Kind != syntax.TokenIdent {
		return false
	}

	switch strings.ToUpper(nonTrivia[idx].Text(src)) {
	case "UNION", "INTERSECT":
		return true
	case "EXCEPT":
		return !isStarOperatorAt(src, nonTrivia, idx-1)
	default:
		return false
	}
}

func isSelectQueryEndToken(src string, nonTrivia []syntax.Token, idx int, stopAtOrder bool) bool {
	tok := nonTrivia[idx]
	txt := tok.Text(src)
	if tok.Kind == syntax.TokenPunct && txt == ";" {
		return true
	}
	if tok.Kind == syntax.TokenOperator && txt == "|>" {
		return true
	}
	if tok.Kind != syntax.TokenIdent {
		return false
	}

	switch strings.ToUpper(txt) {
	case "EXCEPT":
		return !isStarOperatorAt(src, nonTrivia, idx-1)
	case "ORDER":
		return stopAtOrder
	case "UNION", "INTERSECT", "LIMIT", "FETCH", "INTO":
		return true
	default:
		return false
	}
}

func isSelectPostWhereClauseKeyword(tok syntax.Token, txt string) bool {
	if tok.Kind != syntax.TokenIdent {
		return false
	}

	switch strings.ToUpper(txt) {
	case "GROUP", "HAVING", "QUALIFY", "ORDER":
		return true
	default:
		return false
	}
}

func (c *tokenAliasCollector) handleForInKeyword(forIdx int) {
	if forIdx+3 >= len(c.nonTrivia) || !isIdentOrBacktick(c.nonTrivia[forIdx+1]) ||
		!isKeywordAt(c.src, c.nonTrivia, forIdx+2, "IN") || !isPunctAt(c.src, c.nonTrivia, forIdx+3, "(") {
		return
	}

	afterQuery := skipBalancedParens(c.src, c.nonTrivia, forIdx+3)
	if !isKeywordAt(c.src, c.nonTrivia, afterQuery, "DO") {
		return
	}

	varTok := c.nonTrivia[forIdx+1]
	c.nonRefAliasPos[forIdx+1] = true
	scopeID := c.tokScopeIDs[forIdx+1]
	bodyEnd := findForInBodyEnd(c.src, c.nonTrivia, afterQuery+1, c.scopes[scopeID].end)
	c.loopVarDecls = append(c.loopVarDecls, tokenDecl{
		name:      strings.Trim(varTok.Text(c.src), "`"),
		nameStart: varTok.Start,
		nameEnd:   varTok.End,
		scope: tokenScope{
			depth: c.scopes[scopeID].depth,
			start: c.nonTrivia[afterQuery].End,
			end:   bodyEnd,
		},
		scopeID:  scopeID,
		explicit: true,
	})
}

func findForInBodyEnd(src string, nonTrivia []syntax.Token, startIdx, fallbackEnd int) int {
	depth := 1
	for k := startIdx; k < len(nonTrivia); k++ {
		if isForInLoopHeader(src, nonTrivia, k) {
			depth++
			continue
		}
		if isKeywordAt(src, nonTrivia, k, "END") && isKeywordAt(src, nonTrivia, k+1, "FOR") {
			depth--
			if depth == 0 {
				return nonTrivia[k].Start
			}
		}
	}

	return fallbackEnd
}

func isForInLoopHeader(src string, nonTrivia []syntax.Token, k int) bool {
	if !isKeywordAt(src, nonTrivia, k, "FOR") || k+3 >= len(nonTrivia) {
		return false
	}
	if !isIdentOrBacktick(nonTrivia[k+1]) || !isKeywordAt(src, nonTrivia, k+2, "IN") || !isPunctAt(src, nonTrivia, k+3, "(") {
		return false
	}

	afterQ := skipBalancedParens(src, nonTrivia, k+3)

	return isKeywordAt(src, nonTrivia, afterQ, "DO")
}

func (c *tokenAliasCollector) handleCreateKeyword(createIdx int) {
	j := skipCreateModifiers(c.src, c.nonTrivia, createIdx+1)
	if j >= len(c.nonTrivia) {
		return
	}

	switch {
	case isKeywordAt(c.src, c.nonTrivia, j, "VIEW"):
		c.handleCreateViewHeader(j + 1)
	case isKeywordAt(c.src, c.nonTrivia, j, "PROCEDURE"),
		isKeywordAt(c.src, c.nonTrivia, j, "FUNCTION"):
		c.recordFuncOrProcDefRange(createIdx, j+1)
	case isKeywordAt(c.src, c.nonTrivia, j, "TABLE") && isKeywordAt(c.src, c.nonTrivia, j+1, "FUNCTION"):
		c.recordFuncOrProcDefRange(createIdx, j+2)
	default:
	}
}

func skipCreateModifiers(src string, nonTrivia []syntax.Token, j int) int {
	if isKeywordAt(src, nonTrivia, j, "OR") && isKeywordAt(src, nonTrivia, j+1, "REPLACE") {
		j += 2
	}

	for j < len(nonTrivia) {
		switch {
		case isKeywordAt(src, nonTrivia, j, "TEMP"),
			isKeywordAt(src, nonTrivia, j, "TEMPORARY"),
			isKeywordAt(src, nonTrivia, j, "PUBLIC"),
			isKeywordAt(src, nonTrivia, j, "PRIVATE"),
			isKeywordAt(src, nonTrivia, j, "AGGREGATE"),
			isKeywordAt(src, nonTrivia, j, "MATERIALIZED"):
			j++
		default:
			return j
		}
	}

	return j
}

func skipIfNotExists(src string, nonTrivia []syntax.Token, j int) int {
	if isKeywordAt(src, nonTrivia, j, "IF") &&
		isKeywordAt(src, nonTrivia, j+1, "NOT") &&
		isKeywordAt(src, nonTrivia, j+2, "EXISTS") {
		return j + 3
	}

	return j
}

func (c *tokenAliasCollector) handleCreateViewHeader(j int) {
	j = skipIfNotExists(c.src, c.nonTrivia, j)
	j = c.markDottedIdentTokensNonRef(j)
	if !isPunctAt(c.src, c.nonTrivia, j, "(") {
		return
	}

	c.markViewColumnListNonRef(j)
}

func (c *tokenAliasCollector) markViewColumnListNonRef(openIdx int) {
	expectCol := true
	depth := 0
	for k := openIdx + 1; k < len(c.nonTrivia); k++ {
		tok := c.nonTrivia[k]
		if tok.Kind == syntax.TokenPunct {
			switch tok.Text(c.src) {
			case "(":
				depth++
			case ")":
				if depth == 0 {
					return
				}
				depth--
			case ",":
				if depth == 0 {
					expectCol = true
				}
			default:
			}
			continue
		}
		if depth == 0 && expectCol && isIdentOrBacktick(tok) {
			c.nonRefAliasPos[k] = true
			expectCol = false
		}
	}
}

func (c *tokenAliasCollector) recordFuncOrProcDefRange(createIdx, nameStartIdx int) {
	j := skipIfNotExists(c.src, c.nonTrivia, nameStartIdx)
	c.markDottedIdentTokensNonRef(j)

	scopeID := c.tokScopeIDs[createIdx]
	stmtEnd := findCreateRoutineStmtEnd(c.src, c.nonTrivia, c.tokScopeIDs, createIdx, scopeID, c.scopes[scopeID].end)
	c.funcDefRanges = append(c.funcDefRanges, syntax.ByteSpan{
		Start: c.nonTrivia[createIdx].Start,
		End:   stmtEnd,
	})
}

func findCreateRoutineStmtEnd(
	src string,
	nonTrivia []syntax.Token,
	tokScopeIDs []int,
	startIdx, scopeID, fallbackEnd int,
) int {
	beginDepth := 0
	caseDepth := 0
	for k := startIdx + 1; k < len(nonTrivia); k++ {
		if tokScopeIDs[k] != scopeID {
			continue
		}
		tok := nonTrivia[k]
		if tok.Kind == syntax.TokenPunct && tok.Text(src) == ";" && beginDepth == 0 {
			return tok.Start
		}
		if isPunctAt(src, nonTrivia, k-1, ".") {
			continue
		}
		beginDepth, caseDepth = stepRoutineBlockDepth(src, nonTrivia, k, beginDepth, caseDepth)
	}

	return fallbackEnd
}

func stepRoutineBlockDepth(src string, nonTrivia []syntax.Token, k, beginDepth, caseDepth int) (int, int) {
	switch {
	case isKeywordAt(src, nonTrivia, k, "BEGIN"):
		return beginDepth + 1, caseDepth
	case isKeywordAt(src, nonTrivia, k, "CASE"):
		return beginDepth, caseDepth + 1
	case isKeywordAt(src, nonTrivia, k, "END") && !isScriptEndModifier(src, nonTrivia, k+1):
		if caseDepth > 0 {
			return beginDepth, caseDepth - 1
		}
		if beginDepth > 0 {
			return beginDepth - 1, caseDepth
		}
	default:
	}

	return beginDepth, caseDepth
}

func isScriptEndModifier(src string, nonTrivia []syntax.Token, nextIdx int) bool {
	return isKeywordAt(src, nonTrivia, nextIdx, "IF") ||
		isKeywordAt(src, nonTrivia, nextIdx, "FOR") ||
		isKeywordAt(src, nonTrivia, nextIdx, "WHILE") ||
		isKeywordAt(src, nonTrivia, nextIdx, "LOOP") ||
		isKeywordAt(src, nonTrivia, nextIdx, "REPEAT")
}

func (c *tokenAliasCollector) markStarExceptCols(exceptIdx int) {
	if exceptIdx == 0 || !isStarOperatorAt(c.src, c.nonTrivia, exceptIdx-1) || !isPunctAt(c.src, c.nonTrivia, exceptIdx+1, "(") {
		return
	}

	for _, colIdx := range collectParenColumnListIndices(c.src, c.nonTrivia, exceptIdx+1) {
		c.nonRefAliasPos[colIdx] = true
	}
}

func (c *tokenAliasCollector) markStarReplaceAliases(replaceIdx int) {
	if !isStarModifierReplace(c.src, c.nonTrivia, replaceIdx) || !isPunctAt(c.src, c.nonTrivia, replaceIdx+1, "(") {
		return
	}

	c.markTopLevelAsAliases(replaceIdx+2, len(c.nonTrivia))
}

func isStarModifierReplace(src string, nonTrivia []syntax.Token, replaceIdx int) bool {
	if replaceIdx == 0 {
		return false
	}
	if isStarOperatorAt(src, nonTrivia, replaceIdx-1) {
		return true
	}
	if !isPunctAt(src, nonTrivia, replaceIdx-1, ")") {
		return false
	}

	depth := 0
	for k := replaceIdx - 1; k >= 0; k-- {
		if nonTrivia[k].Kind != syntax.TokenPunct {
			continue
		}
		switch nonTrivia[k].Text(src) {
		case ")":
			depth++
		case "(":
			depth--
			if depth == 0 {
				return k >= 2 && isKeywordAt(src, nonTrivia, k-1, "EXCEPT") && isStarOperatorAt(src, nonTrivia, k-2)
			}
		default:
		}
	}

	return false
}

func isStarOperatorAt(src string, nonTrivia []syntax.Token, idx int) bool {
	return idx >= 0 && idx < len(nonTrivia) &&
		nonTrivia[idx].Kind == syntax.TokenOperator &&
		nonTrivia[idx].Text(src) == "*"
}

func isInsertModeUpdate(src string, nonTrivia []syntax.Token, idx int) bool {
	if idx >= 1 && isKeywordAt(src, nonTrivia, idx-1, "INSERT") {
		return true
	}

	return idx >= 2 && isKeywordAt(src, nonTrivia, idx-2, "INSERT") && isKeywordAt(src, nonTrivia, idx-1, "OR")
}

func (c *tokenAliasCollector) markInsertTargetAndCols(idx int) {
	idx = skipInsertModeAndInto(c.src, c.nonTrivia, idx)
	lastName, _, _, _, nextIdx, _ := readDottedIdentSpan(c.src, c.nonTrivia, c.tokScopeIDs, idx)
	if lastName != "" {
		c.tablePos[idx] = true
		idx = skipHintIfPresent(c.src, c.nonTrivia, nextIdx)
	}

	if isPunctAt(c.src, c.nonTrivia, idx, "(") {
		for _, colIdx := range collectParenColumnListIndices(c.src, c.nonTrivia, idx) {
			c.colTargetPos[colIdx] = true
		}
	}
}

func skipInsertModeAndInto(src string, nonTrivia []syntax.Token, idx int) int {
	if idx < len(nonTrivia) && nonTrivia[idx].Kind == syntax.TokenIdent {
		upper := strings.ToUpper(nonTrivia[idx].Text(src))
		switch {
		case upper == "OR" && idx+1 < len(nonTrivia) && nonTrivia[idx+1].Kind == syntax.TokenIdent:
			nextUpper := strings.ToUpper(nonTrivia[idx+1].Text(src))
			if nextUpper == "IGNORE" || nextUpper == "REPLACE" || nextUpper == "UPDATE" {
				idx += 2
			}
		case upper == "IGNORE", upper == "REPLACE", upper == "UPDATE":
			idx++
		default:
		}
	}

	if idx < len(nonTrivia) && nonTrivia[idx].Kind == syntax.TokenIdent && strings.EqualFold(nonTrivia[idx].Text(src), "INTO") {
		idx++
	}

	return idx
}

func skipHintIfPresent(src string, nonTrivia []syntax.Token, idx int) int {
	if isPunctAt(src, nonTrivia, idx, "@") && isPunctAt(src, nonTrivia, idx+1, "{") {
		return skipBalancedDelims(src, nonTrivia, idx+1, "{", "}")
	}

	return idx
}

func collectParenColumnListIndices(src string, nonTrivia []syntax.Token, openIdx int) []int {
	j := openIdx + 1
	if j >= len(nonTrivia) {
		return nil
	}

	var cols []int
	for j < len(nonTrivia) {
		tok := nonTrivia[j]
		if len(cols) > 0 && tok.Kind == syntax.TokenPunct && tok.Text(src) == ")" {
			return cols
		}
		if !isIdentOrBacktick(tok) {
			return nil
		}
		if len(cols) == 0 && isQueryStartKeyword(src, tok) {
			return nil
		}

		cols = append(cols, j)
		j++

		if j >= len(nonTrivia) {
			return cols
		}
		if nonTrivia[j].Kind != syntax.TokenPunct {
			return nil
		}

		switch nonTrivia[j].Text(src) {
		case ")":
			return cols
		case ",":
			j++
		default:
			return nil
		}
	}

	return cols
}

func isQueryStartKeyword(src string, tok syntax.Token) bool {
	if tok.Kind != syntax.TokenIdent {
		return false
	}

	switch strings.ToUpper(tok.Text(src)) {
	case "SELECT", "WITH", "FROM", "GRAPH":
		return true
	default:
		return false
	}
}

func (c *tokenAliasCollector) handleUpdateKeyword(idx int) {
	if isKeywordAt(c.src, c.nonTrivia, idx, "SET") {
		c.markUpdateSetLHS(idx+1, c.mergeTargetDecls)
		return
	}

	decls, nextIdx := c.parseTableFactorAlias(idx)
	c.decls = append(c.decls, decls...)

	nextIdx = skipHintIfPresent(c.src, c.nonTrivia, nextIdx)
	offDecls, afterOff := readUnnestWithOffsetAlias(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, nextIdx)
	c.withOffsetDecls = append(c.withOffsetDecls, offDecls...)

	if isKeywordAt(c.src, c.nonTrivia, afterOff, "SET") {
		c.markUpdateSetLHS(afterOff+1, decls)
	}
}

func (c *tokenAliasCollector) markUpdateSetLHS(startIdx int, targetDecls []tokenDecl) {
	st := updateSetState{expectLHS: true}
	for j := startIdx; j < len(c.nonTrivia); {
		nextIdx, markCol, done := advanceUpdateSetState(c.src, c.nonTrivia, j, &st)
		if markCol {
			c.colTargetPos[j] = true
			if len(targetDecls) > 0 {
				c.colTargetDecls[j] = targetDecls
			}
		}
		if done {
			return
		}
		j = nextIdx
	}
}

func advanceUpdateSetState(
	src string,
	nonTrivia []syntax.Token,
	j int,
	st *updateSetState,
) (nextIdx int, markCol, done bool) {
	tok := nonTrivia[j]
	txt := tok.Text(src)

	if st.parenDepth == 0 && st.bracketDepth == 0 {
		return stepUpdateSetTopLevel(src, nonTrivia, j, tok, txt, st)
	}

	updateSetNesting(tok, txt, st)
	if st.parenDepth < 0 || st.bracketDepth < 0 {
		return j, false, true
	}

	return j + 1, false, false
}

func stepUpdateSetTopLevel(
	src string,
	nonTrivia []syntax.Token,
	j int,
	tok syntax.Token,
	txt string,
	st *updateSetState,
) (nextIdx int, markCol, done bool) {
	if st.expectLHS {
		if isIdentOrBacktick(tok) {
			st.expectLHS = false
			return j + 1, true, false
		}
		if tok.Kind == syntax.TokenPunct && txt == "(" {
			st.expectLHS = false
			return skipBalancedParens(src, nonTrivia, j), false, false
		}
	}

	if tok.Kind == syntax.TokenOperator && txt == "=" {
		st.expectLHS = false
		return j + 1, false, false
	}
	if tok.Kind == syntax.TokenPunct && txt == "," {
		st.expectLHS = true
		return j + 1, false, false
	}
	if isUpdateSetClauseTerminator(tok, txt, st.caseDepth) {
		return j, false, true
	}

	updateSetNesting(tok, txt, st)

	return j + 1, false, st.parenDepth < 0 || st.bracketDepth < 0
}

func updateSetNesting(tok syntax.Token, txt string, st *updateSetState) {
	if tok.Kind == syntax.TokenPunct {
		switch txt {
		case "(":
			st.parenDepth++
		case ")":
			st.parenDepth--
		case "[":
			st.bracketDepth++
		case "]":
			st.bracketDepth--
		default:
		}

		return
	}

	if tok.Kind == syntax.TokenIdent {
		switch strings.ToUpper(txt) {
		case "CASE":
			st.caseDepth++
		case "END":
			if st.caseDepth > 0 {
				st.caseDepth--
			}
		default:
		}
	}
}

func isUpdateSetClauseTerminator(tok syntax.Token, txt string, caseDepth int) bool {
	if tok.Kind == syntax.TokenPunct && txt == ";" {
		return true
	}
	if tok.Kind == syntax.TokenOperator && txt == "|>" {
		return true
	}
	if tok.Kind != syntax.TokenIdent {
		return false
	}

	switch strings.ToUpper(txt) {
	case "FROM", "WHERE", "RETURNING", "ASSERT_ROWS_MODIFIED", "ORDER", "LIMIT":
		return true
	case "WHEN":
		return caseDepth == 0
	default:
		return false
	}
}

func (c *tokenAliasCollector) parseMergeAliases(idx int) []tokenDecl {
	if isKeywordAt(c.src, c.nonTrivia, idx, "INTO") {
		idx++
	}

	targetDecls, nextIdx := c.parseTableFactorAlias(idx)
	c.mergeTargetDecls = targetDecls

	decls := append([]tokenDecl(nil), targetDecls...)
	if isKeywordAt(c.src, c.nonTrivia, nextIdx, "USING") {
		srcDecls, _ := c.parseTableFactorAlias(nextIdx + 1)
		decls = append(decls, srcDecls...)
	}

	return decls
}

type tableFactorMeta struct {
	decls            []tokenDecl
	subqOpen         int
	subqAlias        string
	tableAlias       string
	tableCols        []tokenDecl
	knownSchema      bool
	isParenJoin      bool
	parenLeafFactors []tableFactorMeta
	parenSubqOpens   []int
	parenJoinOnSpans []syntax.ByteSpan
}

type fromChainResult struct {
	decls            []tokenDecl
	allCols          []tokenDecl
	leafFactors      []tableFactorMeta
	subqOpens        []int
	joinOnSpans      []syntax.ByteSpan
	parenJoinOnSpans []syntax.ByteSpan
}

func (c *tokenAliasCollector) handleFromKeyword(startIdx, scopeID int) {
	res := c.parseFromChain(startIdx, len(c.nonTrivia), scopeID, true, false)
	c.emitFromChainJoinOnSpans(res, scopeID)
}

func (c *tokenAliasCollector) emitFromChainJoinOnSpans(res fromChainResult, scopeID int) {
	if !c.seenSelectInChain[scopeID] {
		if len(res.joinOnSpans) == 0 {
			return
		}
		if len(res.allCols) > 0 {
			for _, sp := range res.joinOnSpans {
				c.emitSelectAliasDeclsFromDecls(res.allCols, sp.Start, sp.End, scopeID)
			}
		}
		for _, sqOpen := range res.subqOpens {
			if meta, ok := c.tableSubqueryOpens[sqOpen]; ok {
				meta.extraJoinOnSpans = append(meta.extraJoinOnSpans, res.joinOnSpans...)
				c.tableSubqueryOpens[sqOpen] = meta
			}
		}

		return
	}

	if len(res.parenJoinOnSpans) == 0 {
		return
	}
	if len(res.allCols) > 0 {
		for _, sp := range res.parenJoinOnSpans {
			c.emitSelectAliasDeclsFromDecls(res.allCols, sp.Start, sp.End, scopeID)
		}
	}
	if pw, ok := c.pendingSelectPostWhere[scopeID]; ok {
		pw.joinOnSpans = append(pw.joinOnSpans, res.parenJoinOnSpans...)
		c.pendingSelectPostWhere[scopeID] = pw
	}
}

func (c *tokenAliasCollector) parseFromChain(startIdx, limitIdx, scopeID int, mutate, inParenJoin bool) fromChainResult {
	var res fromChainResult
	j := startIdx
	afterJoin := false

	for j < limitIdx {
		fm, nextIdx := c.parseTableFactorWithMeta(j, scopeID, mutate)
		c.applyFromChainFactor(&res, fm, scopeID, mutate, inParenJoin)
		j = nextIdx
		if mutate && afterJoin && isKeywordAt(c.src, c.nonTrivia, j, "USING") && isPunctAt(c.src, c.nonTrivia, j+1, "(") {
			c.markJoinUsingColsNonRef(j + 1)
		}

		var cont bool
		var onSpan syntax.ByteSpan
		var hasOn bool
		j, cont, afterJoin, onSpan, hasOn = advanceToNextFromItemWithOnSpan(c.src, c.nonTrivia, j, limitIdx, afterJoin)
		if hasOn {
			res.joinOnSpans = append(res.joinOnSpans, onSpan)
		}
		if !cont {
			break
		}
	}

	return res
}

func (c *tokenAliasCollector) applyFromChainFactor(
	res *fromChainResult,
	fm tableFactorMeta,
	scopeID int,
	mutate, inParenJoin bool,
) {
	if len(fm.tableCols) > 0 {
		res.allCols = appendUniqueDecls(res.allCols, fm.tableCols)
	}

	if fm.isParenJoin {
		res.decls = append(res.decls, fm.decls...)
		res.leafFactors = append(res.leafFactors, fm.parenLeafFactors...)
		res.subqOpens = append(res.subqOpens, fm.parenSubqOpens...)
		res.joinOnSpans = append(res.joinOnSpans, fm.parenJoinOnSpans...)
		res.parenJoinOnSpans = append(res.parenJoinOnSpans, fm.parenJoinOnSpans...)

		return
	}

	decls := fm.decls
	if inParenJoin {
		decls = rewriteDeclsScope(decls, c.scopes[scopeID], scopeID)
	}
	res.decls = append(res.decls, decls...)
	res.leafFactors = append(res.leafFactors, fm)
	if fm.subqOpen >= 0 {
		res.subqOpens = append(res.subqOpens, fm.subqOpen)
	}
	if mutate {
		c.decls = append(c.decls, decls...)
		c.recordFromItemMeta(fm, scopeID, c.seenSelectInChain[scopeID])
	}
}

func rewriteDeclsScope(decls []tokenDecl, scope tokenScope, scopeID int) []tokenDecl {
	if len(decls) == 0 {
		return nil
	}

	out := make([]tokenDecl, len(decls))
	for i, d := range decls {
		d.scope = scope
		d.scopeID = scopeID
		out[i] = d
	}

	return out
}

func (c *tokenAliasCollector) markJoinUsingColsNonRef(openIdx int) {
	j := openIdx + 1
	first := true
	for j < len(c.nonTrivia) {
		tok := c.nonTrivia[j]
		if !first && tok.Kind == syntax.TokenPunct && tok.Text(c.src) == ")" {
			return
		}
		if !isIdentOrBacktick(tok) {
			return
		}
		if first && isQueryStartKeyword(c.src, tok) {
			return
		}
		c.nonRefAliasPos[j] = true
		first = false
		j++
		if j >= len(c.nonTrivia) || c.nonTrivia[j].Kind != syntax.TokenPunct {
			return
		}
		switch c.nonTrivia[j].Text(c.src) {
		case ")":
			return
		case ",":
			j++
		default:
			return
		}
	}
}

func (c *tokenAliasCollector) recordFromItemMeta(fm tableFactorMeta, scopeID int, isSelectFrom bool) {
	if fm.subqOpen >= 0 {
		c.tableSubqueryOpens[fm.subqOpen] = tableSubqueryMeta{
			alias:        fm.subqAlias,
			ownerScopeID: scopeID,
			isSelectFrom: isSelectFrom,
		}
	}
	if isSelectFrom {
		return
	}
	if len(fm.tableCols) > 0 {
		c.setPipeRangeVarCols(scopeID, fm.tableAlias, fm.tableCols)
		c.pipeSelectAliases[scopeID] = appendUniqueDecls(c.pipeSelectAliases[scopeID], fm.tableCols)
	} else if !fm.knownSchema && fm.subqOpen < 0 && fm.tableAlias != "" {
		c.setPipeUnknownRangeVar(scopeID, fm.tableAlias)
	}
}

func (c *tokenAliasCollector) parseTableFactorAlias(idx int) ([]tokenDecl, int) {
	fm, nextIdx := c.parseTableFactorWithMeta(idx, c.tokScopeIDs[min(idx, max(0, len(c.nonTrivia)-1))], true)

	return fm.decls, nextIdx
}

func (c *tokenAliasCollector) parseTableFactorWithMeta(idx, scopeID int, mutate bool) (tableFactorMeta, int) {
	fm := tableFactorMeta{subqOpen: -1}
	if idx >= len(c.nonTrivia) {
		return fm, idx
	}

	if isKeywordAt(c.src, c.nonTrivia, idx, "LATERAL") {
		idx++
		if idx >= len(c.nonTrivia) {
			return fm, idx
		}
	}

	offStart := len(c.withOffsetDecls)

	switch {
	case isPunctAt(c.src, c.nonTrivia, idx, "("):
		fm, idx = c.parseParenTableFactorOrJoin(idx, scopeID, mutate)
	case isKeywordAt(c.src, c.nonTrivia, idx, "UNNEST") && isPunctAt(c.src, c.nonTrivia, idx+1, "("):
		fm.decls, idx = c.parseUnnestTableFactorWithMutate(idx, mutate)
		if len(fm.decls) > 0 {
			fm.tableAlias = fm.decls[0].name
		}
	default:
		fm, idx = c.parsePathOrTVFFactorWithMeta(idx, scopeID, mutate)
	}

	var hadPostfix bool
	fm.decls, idx, hadPostfix = c.parsePostfixTableAliases(fm.decls, idx)
	if hadPostfix {
		fm.tableCols = nil
		fm.knownSchema = false
		fm.subqOpen = -1
		fm.subqAlias = ""
		fm.tableAlias = ""
		fm.isParenJoin = false
		fm.parenLeafFactors = nil
		fm.parenSubqOpens = nil
		fm.parenJoinOnSpans = nil
		if len(fm.decls) > 0 {
			fm.tableAlias = fm.decls[0].name
		}
		if mutate && len(c.withOffsetDecls) > offStart {
			c.withOffsetDecls = c.withOffsetDecls[:offStart]
		}
	}
	idx = skipTableFactorModifiers(c.src, c.nonTrivia, idx)

	return fm, idx
}

func (c *tokenAliasCollector) parseParenTableFactorOrJoin(openIdx, scopeID int, mutate bool) (tableFactorMeta, int) {
	if !c.isSubqueryParen(openIdx) {
		return c.parseParenJoinFactorWithMeta(openIdx, scopeID, mutate)
	}

	fm := tableFactorMeta{subqOpen: -1}
	var idx int
	fm.decls, idx = c.parseParenTableFactor(openIdx)
	if openIdx+1 < len(c.nonTrivia) && c.scopes[c.tokScopeIDs[openIdx+1]].depth > c.scopes[c.tokScopeIDs[openIdx]].depth {
		fm.subqOpen = openIdx
	}
	if len(fm.decls) > 0 {
		fm.subqAlias = fm.decls[0].name
	}

	return fm, idx
}

func (c *tokenAliasCollector) parseParenJoinFactorWithMeta(openIdx, scopeID int, mutate bool) (tableFactorMeta, int) {
	afterParen := skipBalancedParens(c.src, c.nonTrivia, openIdx)
	interiorEnd := afterParen
	if interiorEnd > openIdx+1 && isPunctAt(c.src, c.nonTrivia, interiorEnd-1, ")") {
		interiorEnd--
	}

	res := c.parseFromChain(openIdx+1, interiorEnd, scopeID, mutate, true)
	afterMods := skipTableFactorModifiers(c.src, c.nonTrivia, afterParen)
	extraDecls, nextIdx := readExplicitAliasAfterFactor(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, afterMods)
	nextIdx = skipTableFactorModifiers(c.src, c.nonTrivia, nextIdx)
	if mutate && len(extraDecls) > 0 {
		c.decls = append(c.decls, extraDecls...)
	}

	fm := tableFactorMeta{
		decls:            append(res.decls, extraDecls...),
		subqOpen:         -1,
		tableCols:        res.allCols,
		knownSchema:      len(res.allCols) > 0,
		isParenJoin:      true,
		parenLeafFactors: res.leafFactors,
		parenSubqOpens:   res.subqOpens,
		parenJoinOnSpans: res.joinOnSpans,
	}

	return fm, nextIdx
}

func (c *tokenAliasCollector) parseParenTableFactor(idx int) ([]tokenDecl, int) {
	afterParen := skipBalancedParens(c.src, c.nonTrivia, idx)
	afterParen = skipTableFactorModifiers(c.src, c.nonTrivia, afterParen)
	decls, nextIdx := readExplicitAliasAfterFactor(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, afterParen)
	nextIdx = skipTableFactorModifiers(c.src, c.nonTrivia, nextIdx)

	return decls, nextIdx
}

func (c *tokenAliasCollector) parseUnnestTableFactorWithMutate(idx int, mutate bool) ([]tokenDecl, int) {
	afterParen := skipBalancedParens(c.src, c.nonTrivia, idx+1)
	decls, nextIdx := readExplicitAliasAfterFactor(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, afterParen)
	offDecls, afterOff := readUnnestWithOffsetAlias(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, nextIdx)
	if mutate {
		c.withOffsetDecls = append(c.withOffsetDecls, offDecls...)
	}
	afterOff = skipTableFactorModifiers(c.src, c.nonTrivia, afterOff)

	return decls, afterOff
}

func (c *tokenAliasCollector) parsePathOrTVFFactorWithMeta(idx, parentScopeID int, mutate bool) (tableFactorMeta, int) {
	fm := tableFactorMeta{subqOpen: -1}
	lastName, lastStart, lastEnd, tokScopeID, nextIdx, trailingDot := readDottedIdentSpan(c.src, c.nonTrivia, c.tokScopeIDs, idx)
	if lastName == "" {
		return fm, nextIdx
	}

	if mutate {
		c.tablePos[idx] = true
	}

	if !trailingDot && isPunctAt(c.src, c.nonTrivia, nextIdx, "(") {
		afterParen := skipBalancedParens(c.src, c.nonTrivia, nextIdx)
		afterParen = skipTableFactorModifiers(c.src, c.nonTrivia, afterParen)
		fm.decls, nextIdx = readExplicitAliasAfterFactor(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, afterParen)
		nextIdx = skipTableFactorModifiers(c.src, c.nonTrivia, nextIdx)
		if len(fm.decls) > 0 {
			fm.tableAlias = fm.decls[0].name
		}

		return fm, nextIdx
	}

	parts := readDottedPathParts(c.src, c.nonTrivia, idx)
	afterMods := skipTableFactorModifiers(c.src, c.nonTrivia, nextIdx)
	decls, afterAl := readExplicitAliasAfterFactor(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, afterMods)
	aliasName := lastName
	if len(decls) > 0 {
		aliasName = decls[0].name
	} else if !trailingDot {
		decls = []tokenDecl{{
			name:      lastName,
			nameStart: lastStart,
			nameEnd:   lastEnd,
			scope:     c.scopes[tokScopeID],
			scopeID:   tokScopeID,
			explicit:  false,
		}}
	}

	offDecls, afterOff := readUnnestWithOffsetAlias(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, afterAl)
	if mutate {
		c.withOffsetDecls = append(c.withOffsetDecls, offDecls...)
	}
	afterOff = skipTableFactorModifiers(c.src, c.nonTrivia, afterOff)

	fm.decls = decls
	if !trailingDot && len(parts) > 0 {
		fm.tableAlias = aliasName
		fm.tableCols, fm.knownSchema = c.resolveTableFactorCols(parts, c.nonTrivia[idx], parentScopeID)
	}

	return fm, afterOff
}

func (c *tokenAliasCollector) resolveTableFactorCols(
	parts []string,
	anchorTok syntax.Token,
	scopeID int,
) ([]tokenDecl, bool) {
	if len(parts) == 1 {
		if cteCols, ok := c.lookupCTECols(parts[0], anchorTok); ok {
			return cloneDeclsForScope(cteCols, scopeID), true
		}
	}
	if c.snap != nil {
		if meta := lookupTableMetaByParts(c.snap, parts); meta != nil {
			return tableMetaColDecls(meta, anchorTok, scopeID), true
		}
	}

	return nil, false
}

func (c *tokenAliasCollector) lookupCTECols(name string, pos syntax.Token) ([]tokenDecl, bool) {
	entries := c.cteSelectAliases[strings.ToLower(name)]
	for _, entry := range slices.Backward(entries) {
		sc := entry.scope
		if sc.start <= pos.Start && pos.End <= sc.end {
			return entry.cols, true
		}
	}

	return nil, false
}

func readDottedPathParts(src string, nonTrivia []syntax.Token, start int) []string {
	if start >= len(nonTrivia) || !isTablePathIdent(src, nonTrivia[start]) {
		return nil
	}

	parts := appendPartsFromToken(nil, src, nonTrivia[start])
	idx := start + 1
	for idx+1 < len(nonTrivia) && nonTrivia[idx].Kind == syntax.TokenPunct && nonTrivia[idx].Text(src) == "." {
		seg := nonTrivia[idx+1]
		if !isIdentOrBacktick(seg) {
			break
		}
		if seg.Start > nonTrivia[idx].End && isTablePathReservedKeyword(src, seg) {
			break
		}
		parts = appendPartsFromToken(parts, src, seg)
		idx += 2
	}

	return parts
}

func appendPartsFromToken(parts []string, src string, tok syntax.Token) []string {
	if tok.Kind == syntax.TokenBacktickIdent {
		raw := strings.Trim(tok.Text(src), "`")
		if raw == "" {
			return parts
		}
		for seg := range strings.SplitSeq(raw, ".") {
			if seg != "" {
				parts = append(parts, seg)
			}
		}
		return parts
	}

	return append(parts, tok.Text(src))
}

func tableMetaColDecls(meta *catalog.TableMeta, anchorTok syntax.Token, scopeID int) []tokenDecl {
	if meta == nil {
		return nil
	}

	decls := make([]tokenDecl, 0, len(meta.Schema)+3)
	for i := range meta.Schema {
		decls = append(decls, tokenDecl{
			name:         meta.Schema[i].Name,
			nameStart:    anchorTok.Start,
			nameEnd:      anchorTok.End,
			scopeID:      scopeID,
			explicit:     true,
			structFields: meta.Schema[i].Fields,
		})
	}
	if meta.IsIngestionTimePartitioned() {
		decls = append(decls,
			tokenDecl{name: "_PARTITIONTIME", nameStart: anchorTok.Start, nameEnd: anchorTok.End, scopeID: scopeID, explicit: true},
			tokenDecl{name: "_PARTITIONDATE", nameStart: anchorTok.Start, nameEnd: anchorTok.End, scopeID: scopeID, explicit: true},
		)
	}
	if strings.HasSuffix(meta.Ref.Table, "*") {
		decls = append(decls, tokenDecl{
			name:      "_TABLE_SUFFIX",
			nameStart: anchorTok.Start,
			nameEnd:   anchorTok.End,
			scopeID:   scopeID,
			explicit:  true,
		})
	}

	return decls
}

func (c *tokenAliasCollector) scanSelectProjection(selectIdx, clauseIdx, scopeID int) *pendingSelectProjection {
	proj := &pendingSelectProjection{}
	startIdx := selectIdx + 1
	if isKeywordAt(c.src, c.nonTrivia, startIdx, "DISTINCT") || isKeywordAt(c.src, c.nonTrivia, startIdx, "ALL") {
		startIdx++
	}
	if isKeywordAt(c.src, c.nonTrivia, startIdx, "AS") &&
		(isKeywordAt(c.src, c.nonTrivia, startIdx+1, "STRUCT") || isKeywordAt(c.src, c.nonTrivia, startIdx+1, "VALUE")) {
		startIdx += 2
	}

	for _, span := range splitTopLevelCommaSpans(c.src, c.nonTrivia, startIdx, clauseIdx) {
		c.collectSelectProjSpan(span[0], span[1], scopeID, proj)
	}

	return proj
}

func (c *tokenAliasCollector) collectSelectProjSpan(itemStart, itemEnd, scopeID int, proj *pendingSelectProjection) {
	if asAliases := c.markTopLevelAsAliasesRange(itemStart, itemEnd); len(asAliases) > 0 {
		c.recordAliasedSelectProjSpan(itemStart, itemEnd, asAliases, proj)
		return
	}

	starIdx := findTopLevelStarIdx(c.src, c.nonTrivia, itemStart, itemEnd)
	if starIdx >= 0 && (starIdx == itemStart || isPunctAt(c.src, c.nonTrivia, starIdx-1, ".")) {
		exceptNames, replaceDecls, replaceAliased, valid := c.parseStarModifiers(starIdx+1, itemEnd, scopeID)
		if !valid {
			return
		}
		proj.exceptCols = append(proj.exceptCols, exceptNames...)
		for _, rd := range replaceDecls {
			proj.replaceCols = append(proj.replaceCols, rd.name)
		}
		proj.aliasedCols = append(proj.aliasedCols, replaceAliased...)
		if starIdx == itemStart {
			proj.hasUnqualStar = true
		} else if qualParts := readDotStarQualifierParts(c.src, c.nonTrivia, itemStart, starIdx); len(qualParts) > 0 {
			proj.dotStarQuals = append(proj.dotStarQuals, qualParts)
		}
		return
	}

	leafIdx, isDotted, isBare := classifyPassThroughSelectItem(c.src, c.nonTrivia, itemStart, itemEnd)
	if isBare {
		proj.bareCols = append(proj.bareCols, strings.Trim(c.nonTrivia[leafIdx].Text(c.src), "`"))
	} else if isDotted {
		if parts := readPassThroughDottedParts(c.src, c.nonTrivia, itemStart, itemEnd); len(parts) >= 2 {
			proj.dottedCols = append(proj.dottedCols, parts)
		}
	}
}

func (c *tokenAliasCollector) recordAliasedSelectProjSpan(itemStart, itemEnd int, asAliases []int, proj *pendingSelectProjection) {
	if ac, ok := extractSingleAsAliasParts(c.src, c.nonTrivia, itemStart, itemEnd, asAliases); ok {
		proj.aliasedCols = append(proj.aliasedCols, ac)
	}
}

func extractSingleAsAliasParts(
	src string,
	nonTrivia []syntax.Token,
	itemStart, itemEnd int,
	asAliases []int,
) (aliasedProjCol, bool) {
	if len(asAliases) != 1 || asAliases[0] != itemEnd-1 || !isKeywordAt(src, nonTrivia, itemEnd-2, "AS") {
		return aliasedProjCol{}, false
	}
	parts := readPassThroughDottedParts(src, nonTrivia, itemStart, itemEnd-2)
	if len(parts) == 0 {
		return aliasedProjCol{}, false
	}

	return aliasedProjCol{
		alias: strings.Trim(nonTrivia[asAliases[0]].Text(src), "`"),
		parts: parts,
	}, true
}

func readDotStarQualifierParts(src string, nonTrivia []syntax.Token, itemStart, starIdx int) []string {
	if starIdx < itemStart+2 || !isPunctAt(src, nonTrivia, starIdx-1, ".") {
		return nil
	}

	return readPassThroughDottedParts(src, nonTrivia, itemStart, starIdx-1)
}

func readPassThroughDottedParts(src string, nonTrivia []syntax.Token, startIdx, endIdx int) []string {
	if startIdx >= endIdx || !isIdentOrBacktick(nonTrivia[startIdx]) {
		return nil
	}

	parts := []string{strings.Trim(nonTrivia[startIdx].Text(src), "`")}
	idx := startIdx
	for idx+2 < endIdx {
		if !isPunctAt(src, nonTrivia, idx+1, ".") || !isIdentOrBacktick(nonTrivia[idx+2]) {
			return nil
		}
		parts = append(parts, strings.Trim(nonTrivia[idx+2].Text(src), "`"))
		idx += 2
	}
	if idx+1 != endIdx {
		return nil
	}

	return parts
}

func (c *tokenAliasCollector) markTopLevelAsAliasesRange(startIdx, endIdx int) []int {
	var out []int
	st := updateSetState{}
	for j := startIdx; j < endIdx; j++ {
		tok := c.nonTrivia[j]
		txt := tok.Text(c.src)
		if st.caseDepth == 0 && st.parenDepth == 0 && st.bracketDepth == 0 &&
			isKeywordAt(c.src, c.nonTrivia, j, "AS") && j+1 < endIdx && isIdentOrBacktick(c.nonTrivia[j+1]) {
			out = append(out, j+1)
			j++
			continue
		}
		updateSetNesting(tok, txt, &st)
	}

	return out
}

func (c *tokenAliasCollector) resolveSelectFromTableDecls(
	clauseIdx, scopeID int,
	proj *pendingSelectProjection,
) (projDecls, allDecls []tokenDecl, aliasedStructs map[string][]catalog.ColumnMeta) {
	if !isKeywordAt(c.src, c.nonTrivia, clauseIdx, "FROM") {
		return nil, nil, nil
	}

	anchorTok := c.nonTrivia[clauseIdx]
	j := clauseIdx + 1
	for j < len(c.nonTrivia) {
		fm, nextIdx := c.parseTableFactorWithMeta(j, scopeID, false)
		if len(fm.tableCols) > 0 {
			allDecls = appendUniqueDecls(allDecls, fm.tableCols)
		}
		projDecls, aliasedStructs = resolveTableFactorProj(fm, proj, anchorTok, scopeID, projDecls, aliasedStructs)
		j = nextIdx

		nextJ, cont, _ := advanceToNextFromItem(c.src, c.nonTrivia, j)
		if !cont {
			break
		}
		j = nextJ
	}

	return projDecls, allDecls, aliasedStructs
}

func resolveTableFactorProj(
	fm tableFactorMeta,
	proj *pendingSelectProjection,
	anchorTok syntax.Token,
	scopeID int,
	projDecls []tokenDecl,
	aliasedStructs map[string][]catalog.ColumnMeta,
) ([]tokenDecl, map[string][]catalog.ColumnMeta) {
	if fm.isParenJoin {
		for _, leaf := range fm.parenLeafFactors {
			projDecls, aliasedStructs = resolveLeafFactorProj(leaf, proj, anchorTok, scopeID, projDecls, aliasedStructs)
		}

		return projDecls, aliasedStructs
	}

	return resolveLeafFactorProj(fm, proj, anchorTok, scopeID, projDecls, aliasedStructs)
}

func resolveLeafFactorProj(
	fm tableFactorMeta,
	proj *pendingSelectProjection,
	anchorTok syntax.Token,
	scopeID int,
	projDecls []tokenDecl,
	aliasedStructs map[string][]catalog.ColumnMeta,
) ([]tokenDecl, map[string][]catalog.ColumnMeta) {
	projDecls = appendUniqueDecls(projDecls, filterTableFactorDeclsByProj(fm, proj, anchorTok, scopeID))
	if fm.knownSchema && proj != nil && len(proj.aliasedCols) > 0 {
		aliasedStructs = mergeAliasedStructMap(aliasedStructs, resolveAliasedStructsMap(fm.tableCols, fm.tableAlias, proj))
	}

	return projDecls, aliasedStructs
}

func filterTableFactorDeclsByProj(
	fm tableFactorMeta,
	proj *pendingSelectProjection,
	anchorTok syntax.Token,
	scopeID int,
) []tokenDecl {
	if proj == nil {
		return nil
	}

	out := filterDeclsBySelectProj(fm.tableCols, fm.tableAlias, proj)
	if fm.knownSchema {
		out = appendUniqueDecls(out, filterColsStructProjDecls(fm.tableCols, fm.tableAlias, proj, anchorTok, scopeID))
		return out
	}

	if !fm.knownSchema && fm.subqOpen < 0 && fm.tableAlias != "" {
		for _, parts := range proj.dottedCols {
			if len(parts) >= 2 && strings.EqualFold(parts[0], fm.tableAlias) {
				leaf := parts[len(parts)-1]
				if !containsFoldString(proj.exceptCols, leaf) && !containsFoldString(proj.replaceCols, leaf) {
					out = appendUniqueDecls(out, []tokenDecl{{
						name:      leaf,
						nameStart: anchorTok.Start,
						nameEnd:   anchorTok.End,
						scopeID:   scopeID,
						explicit:  true,
					}})
				}
			}
		}
	}

	return out
}

func filterTableSubqueryDeclsByProj(
	cols []tokenDecl,
	alias string,
	proj *pendingSelectProjection,
	anchorTok syntax.Token,
	scopeID int,
) []tokenDecl {
	if proj == nil {
		return nil
	}

	out := filterDeclsBySelectProj(cols, alias, proj)

	return appendUniqueDecls(out, filterColsStructProjDecls(cols, alias, proj, anchorTok, scopeID))
}

func filterColsStructProjDecls(
	cols []tokenDecl,
	alias string,
	proj *pendingSelectProjection,
	anchorTok syntax.Token,
	scopeID int,
) []tokenDecl {
	var out []tokenDecl
	for _, qualParts := range proj.dotStarQuals {
		structPath := structPathForMetaQualifier(qualParts, alias)
		if len(structPath) == 0 {
			continue
		}
		for _, d := range structFieldDeclsFromCols(cols, structPath, anchorTok, scopeID) {
			if !containsFoldString(proj.exceptCols, d.name) && !containsFoldString(proj.replaceCols, d.name) {
				out = append(out, d)
			}
		}
	}

	for _, parts := range proj.dottedCols {
		if len(parts) < 2 {
			continue
		}
		structPath := structPathForMetaQualifier(parts[:len(parts)-1], alias)
		leaf := parts[len(parts)-1]
		if len(structPath) > 0 {
			if col, ok := findStructFieldInCols(cols, structPath, leaf); ok {
				out = append(out, tokenDecl{
					name:         leaf,
					nameStart:    anchorTok.Start,
					nameEnd:      anchorTok.End,
					scopeID:      scopeID,
					explicit:     true,
					structFields: col.Fields,
				})
			}
		}
	}

	return out
}

func resolveAliasedStructsMap(
	cols []tokenDecl,
	alias string,
	proj *pendingSelectProjection,
) map[string][]catalog.ColumnMeta {
	if proj == nil || len(proj.aliasedCols) == 0 {
		return nil
	}

	var out map[string][]catalog.ColumnMeta
	for _, ac := range proj.aliasedCols {
		sf := resolveStructFieldsForProjParts(cols, alias, ac.parts)
		if len(sf) == 0 {
			continue
		}
		if out == nil {
			out = make(map[string][]catalog.ColumnMeta)
		}
		out[strings.ToLower(ac.alias)] = sf
	}

	return out
}

func resolveAliasedProjDecls(
	cols []tokenDecl,
	alias string,
	proj *pendingSelectProjection,
	anchorTok syntax.Token,
	scopeID int,
) []tokenDecl {
	if proj == nil || len(proj.aliasedCols) == 0 {
		return nil
	}

	var out []tokenDecl
	for _, ac := range proj.aliasedCols {
		sf := resolveStructFieldsForProjParts(cols, alias, ac.parts)
		if len(sf) == 0 {
			continue
		}
		out = append(out, tokenDecl{
			name:         ac.alias,
			nameStart:    anchorTok.Start,
			nameEnd:      anchorTok.End,
			scopeID:      scopeID,
			explicit:     true,
			structFields: sf,
		})
	}

	return out
}

func resolveStructFieldsForProjParts(cols []tokenDecl, alias string, parts []string) []catalog.ColumnMeta {
	if len(parts) == 0 {
		return nil
	}
	if len(parts) == 1 {
		if d, ok := findDeclByName(cols, parts[0]); ok {
			return d.structFields
		}
		return nil
	}

	path := structPathForMetaQualifier(parts, alias)
	if len(path) == 0 {
		return nil
	}

	return resolveStructFieldsInCols(cols, path)
}

func mergeAliasedStructMap(dst, src map[string][]catalog.ColumnMeta) map[string][]catalog.ColumnMeta {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string][]catalog.ColumnMeta, len(src))
	}
	maps.Copy(dst, src)

	return dst
}

func attachStructFieldsToDecls(decls []tokenDecl, structs map[string][]catalog.ColumnMeta) {
	if len(structs) == 0 {
		return
	}
	for i := range decls {
		if sf, ok := structs[strings.ToLower(decls[i].name)]; ok && len(sf) > 0 {
			decls[i].structFields = sf
		}
	}
}

func structPathForMetaQualifier(qualParts []string, alias string) []string {
	if len(qualParts) == 0 {
		return nil
	}
	if len(qualParts) == 1 {
		if alias != "" && strings.EqualFold(qualParts[0], alias) {
			return nil
		}
		return qualParts
	}
	if alias != "" && strings.EqualFold(qualParts[0], alias) {
		return qualParts[1:]
	}

	return qualParts
}

func filterDeclsBySelectProj(cols []tokenDecl, alias string, proj *pendingSelectProjection) []tokenDecl {
	if proj == nil {
		return nil
	}

	var out []tokenDecl
	for _, d := range cols {
		if containsFoldString(proj.exceptCols, d.name) || containsFoldString(proj.replaceCols, d.name) {
			continue
		}
		if proj.hasUnqualStar ||
			(alias != "" && containsDotStarTableAlias(proj.dotStarQuals, alias)) ||
			containsFoldString(proj.bareCols, d.name) ||
			(alias != "" && containsDottedColForAlias(proj.dottedCols, alias, d.name)) {
			out = append(out, d)
		}
	}

	return out
}

func containsDotStarTableAlias(dotStarQuals [][]string, alias string) bool {
	for _, q := range dotStarQuals {
		if len(q) == 1 && strings.EqualFold(q[0], alias) {
			return true
		}
	}

	return false
}

func containsDottedColForAlias(dottedCols [][]string, alias, colName string) bool {
	for _, parts := range dottedCols {
		if len(parts) == 2 && strings.EqualFold(parts[0], alias) && strings.EqualFold(parts[1], colName) {
			return true
		}
	}

	return false
}

func (c *tokenAliasCollector) parsePostfixTableAliases(decls []tokenDecl, idx int) ([]tokenDecl, int, bool) {
	hadPostfix := false
	for idx < len(c.nonTrivia) {
		if !isPostfixReplacingKeyword(c.src, c.nonTrivia, idx) {
			break
		}
		openIdx := idx + 1
		if isKeywordAt(c.src, c.nonTrivia, idx, "UNPIVOT") {
			openIdx = skipUnpivotNullModifier(c.src, c.nonTrivia, idx+1, len(c.nonTrivia))
		}
		if !isPunctAt(c.src, c.nonTrivia, openIdx, "(") {
			break
		}

		hadPostfix = true
		afterParen := skipBalancedParens(c.src, c.nonTrivia, openIdx)
		afterParen = skipHintIfPresent(c.src, c.nonTrivia, afterParen)
		decls, idx = readExplicitAliasAfterFactor(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, afterParen)
	}

	return decls, idx, hadPostfix
}

func (c *tokenAliasCollector) handlePipeOperator(pipeIdx int) {
	j := pipeIdx + 1
	if j >= len(c.nonTrivia) {
		return
	}

	scopeID := c.tokScopeIDs[pipeIdx]
	delete(c.seenSelectInChain, scopeID)
	delete(c.pendingSelect, scopeID)
	delete(c.pendingSelectPostWhere, scopeID)
	pipeEnd := nextPipeStartInScope(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, pipeIdx, scopeID, c.scopes[scopeID].end)
	endIdx := nextPipeTokenIdxInScope(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, pipeIdx, scopeID)

	if c.handleSimplePipeOperator(pipeIdx, j, scopeID, endIdx, pipeEnd) {
		return
	}

	c.handleComplexPipeOperator(pipeIdx, j, scopeID, endIdx, pipeEnd)
}

func (c *tokenAliasCollector) handleSimplePipeOperator(pipeIdx, j, scopeID, endIdx, pipeEnd int) bool {
	switch {
	case isKeywordAt(c.src, c.nonTrivia, j, "AS"):
		c.handlePipeAs(j, scopeID)
	case isKeywordAt(c.src, c.nonTrivia, j, "SET"):
		c.emitPipeSelectAliasesRange(scopeID, c.nonTrivia[j].End, pipeEnd)
		c.markUpdateSetLHS(j+1, nil)
	case isKeywordAt(c.src, c.nonTrivia, j, "EXTEND"), isKeywordAt(c.src, c.nonTrivia, j, "WINDOW"):
		c.emitPipeSelectAliasesRange(scopeID, c.nonTrivia[j].End, pipeEnd)
		c.pipeSelectAliases[scopeID] = appendOrReplacePipeDecls(
			c.pipeSelectAliases[scopeID],
			c.collectPipeExtendAsDecls(j+1, endIdx, scopeID),
		)
	case isKeywordAt(c.src, c.nonTrivia, j, "DROP"):
		c.removePipeSelectAliases(scopeID, c.markPipeDropCols(j+1, scopeID))
	case isKeywordAt(c.src, c.nonTrivia, j, "RENAME"):
		c.handlePipeRenameAliases(j+1, scopeID)
	case isKeywordAt(c.src, c.nonTrivia, j, "ORDER"):
		c.handlePipeOrderByAliases(pipeIdx, j, scopeID)
	case isPipeExprClauseKeyword(c.src, c.nonTrivia, j):
		c.emitPipeSelectAliasesRange(scopeID, c.nonTrivia[j].Start, pipeEnd)
	default:
		return false
	}

	return true
}

func (c *tokenAliasCollector) handleComplexPipeOperator(pipeIdx, j, scopeID, endIdx, pipeEnd int) {
	switch {
	case isKeywordAt(c.src, c.nonTrivia, j, "SELECT"):
		c.emitPipeSelectAliasesRange(scopeID, c.nonTrivia[j].End, pipeEnd)
		c.clampDeclsAtBarrier(scopeID, pipeEnd)
		nextAliases := c.computePipeSelectOutputAliases(j+1, endIdx, scopeID, c.pipeSelectAliases[scopeID])
		c.pipeSelectAliases[scopeID] = nextAliases
		c.clearPipeRangeVars(scopeID)
	case isKeywordAt(c.src, c.nonTrivia, j, "AGGREGATE"):
		c.emitPipeSelectAliasesRange(scopeID, c.nonTrivia[j].End, pipeEnd)
		c.clampDeclsAtBarrier(scopeID, pipeEnd)
		nextAliases := c.computePipeAggregateOutputAliases(j+1, endIdx, scopeID, c.pipeSelectAliases[scopeID])
		c.pipeSelectAliases[scopeID] = nextAliases
		c.clearPipeRangeVars(scopeID)
	case isKeywordAt(c.src, c.nonTrivia, j, "TEE"), isKeywordAt(c.src, c.nonTrivia, j, "FORK"):
		c.propagatePipeAliasesToBranches(j+1, endIdx, scopeID)
	case isKeywordAt(c.src, c.nonTrivia, j, "IF"):
		c.handlePipeIfOperator(j, endIdx, pipeEnd, scopeID)
	case isKeywordAt(c.src, c.nonTrivia, j, "PIVOT") && isPunctAt(c.src, c.nonTrivia, j+1, "("):
		c.emitPipeParenExprRange(scopeID, j+1)
		c.clearPipeRangeVars(scopeID)
		c.updatePipePivotAliases(j+1, scopeID)
		c.handlePipePostfixOpAt(j, j+1, scopeID)
	case isKeywordAt(c.src, c.nonTrivia, j, "UNPIVOT"):
		unpivotOpen := skipUnpivotNullModifier(c.src, c.nonTrivia, j+1, endIdx)
		if isPunctAt(c.src, c.nonTrivia, unpivotOpen, "(") {
			c.emitPipeParenExprRange(scopeID, unpivotOpen)
			c.clearPipeRangeVars(scopeID)
			c.updatePipeUnpivotAliases(unpivotOpen, scopeID)
			c.handlePipePostfixOpAt(j, unpivotOpen, scopeID)
		}
	case isKeywordAt(c.src, c.nonTrivia, j, "MATCH_RECOGNIZE") && isPunctAt(c.src, c.nonTrivia, j+1, "("):
		delete(c.pipeSelectAliases, scopeID)
		c.clearPipeRangeVars(scopeID)
		c.handlePipePostfixOpAt(j, j+1, scopeID)
	case isKeywordAt(c.src, c.nonTrivia, j, "CALL"):
		delete(c.pipeSelectAliases, scopeID)
		c.clearPipeRangeVars(scopeID)
		c.handlePipeCallOp(j, scopeID)
	case isKeywordAt(c.src, c.nonTrivia, j, "RECURSIVE"):
		c.propagatePipeAliasesToRecursiveBranch(j+1, endIdx, scopeID)
		c.handlePipeRecursiveUnion(j, pipeIdx, scopeID)
	case isPipeSetOpKeyword(c.src, c.nonTrivia, j):
		c.clampDeclsAtBarrier(scopeID, pipeEnd)
		c.clearPipeRangeVars(scopeID)
	default:
		if joinIdx, isJoin := findPipeJoinKeyword(c.src, c.nonTrivia, j); isJoin {
			c.handlePipeJoinOperator(joinIdx, scopeID, endIdx, pipeEnd)
		}
	}
}

func (c *tokenAliasCollector) emitPipeParenExprRange(scopeID, openIdx int) {
	if openIdx < 0 || openIdx >= len(c.nonTrivia) {
		return
	}
	afterParen := skipBalancedParens(c.src, c.nonTrivia, openIdx)
	endTok := min(afterParen-1, len(c.nonTrivia)-1)
	if endTok >= openIdx {
		c.emitPipeSelectAliasesRange(scopeID, c.nonTrivia[openIdx].Start, c.nonTrivia[endTok].End)
	}
}

func (c *tokenAliasCollector) handlePipeJoinOperator(joinIdx, scopeID, endIdx, pipeEnd int) {
	fm, afterJoin := c.parseTableFactorWithMeta(joinIdx+1, scopeID, true)
	c.decls = append(c.decls, fm.decls...)
	c.recordFromItemMeta(fm, scopeID, false)
	c.emitParenJoinRHSOnSpans(fm, scopeID)
	if isKeywordAt(c.src, c.nonTrivia, afterJoin, "USING") && isPunctAt(c.src, c.nonTrivia, afterJoin+1, "(") {
		c.markJoinUsingColsNonRef(afterJoin + 1)
	}
	if afterJoin < endIdx {
		onSpan := syntax.ByteSpan{Start: c.nonTrivia[afterJoin].Start, End: pipeEnd}
		c.attachPipeJoinOuterOnSpan(fm, onSpan)
		c.emitPipeSelectAliasesRange(scopeID, onSpan.Start, onSpan.End)
	}
}

func (c *tokenAliasCollector) emitParenJoinRHSOnSpans(fm tableFactorMeta, scopeID int) {
	if len(fm.parenJoinOnSpans) == 0 {
		return
	}
	if len(fm.tableCols) > 0 {
		for _, sp := range fm.parenJoinOnSpans {
			c.emitSelectAliasDeclsFromDecls(fm.tableCols, sp.Start, sp.End, scopeID)
		}
	}
	for _, sqOpen := range fm.parenSubqOpens {
		if meta, ok := c.tableSubqueryOpens[sqOpen]; ok {
			meta.extraJoinOnSpans = append(meta.extraJoinOnSpans, fm.parenJoinOnSpans...)
			c.tableSubqueryOpens[sqOpen] = meta
		}
	}
}

func (c *tokenAliasCollector) attachPipeJoinOuterOnSpan(fm tableFactorMeta, onSpan syntax.ByteSpan) {
	if onSpan.Start >= onSpan.End {
		return
	}
	if fm.subqOpen >= 0 {
		meta := c.tableSubqueryOpens[fm.subqOpen]
		meta.pipeJoinOnSpan = onSpan
		c.tableSubqueryOpens[fm.subqOpen] = meta
	}
	for _, sqOpen := range fm.parenSubqOpens {
		if meta, ok := c.tableSubqueryOpens[sqOpen]; ok {
			meta.extraJoinOnSpans = append(meta.extraJoinOnSpans, onSpan)
			c.tableSubqueryOpens[sqOpen] = meta
		}
	}
}

func (c *tokenAliasCollector) propagatePipeAliasesToBranches(startIdx, endIdx, scopeID int) {
	aliases := c.pipeSelectAliases[scopeID]
	if len(aliases) == 0 {
		return
	}

	limit := min(endIdx, len(c.nonTrivia))
	for k := startIdx; k < limit; {
		if c.tokScopeIDs[k] != scopeID {
			k++
			continue
		}
		if isPunctAt(c.src, c.nonTrivia, k, "(") {
			c.copyPipeAliasesToParenScope(k, aliases)
			k = skipBalancedParens(c.src, c.nonTrivia, k)
			continue
		}
		k++
	}
}

func (c *tokenAliasCollector) propagatePipeAliasesToRecursiveBranch(startIdx, endIdx, scopeID int) {
	aliases := c.pipeSelectAliases[scopeID]
	if len(aliases) == 0 {
		return
	}

	limit := min(endIdx, len(c.nonTrivia))
	for k := startIdx; k < limit; k++ {
		if c.tokScopeIDs[k] != scopeID || !isPunctAt(c.src, c.nonTrivia, k, "(") {
			continue
		}
		if isPipeOpAt(c.src, c.nonTrivia, k+1) {
			c.copyPipeAliasesToParenScope(k, aliases)
		}
		return
	}
}

func (c *tokenAliasCollector) handlePipeIfOperator(ifIdx, endIdx, pipeEnd, scopeID int) {
	aliases := c.pipeSelectAliases[scopeID]
	limit := min(endIdx, len(c.nonTrivia))
	var branchOpens []int
	hasElse := false
	for k := ifIdx; k < limit; {
		if c.tokScopeIDs[k] != scopeID {
			k++
			continue
		}
		if isKeywordAt(c.src, c.nonTrivia, k, "IF") || isKeywordAt(c.src, c.nonTrivia, k, "ELSEIF") {
			condEnd := findPipeIfConditionEnd(c.src, c.nonTrivia, c.tokScopeIDs, k+1, limit, scopeID, pipeEnd)
			c.emitPipeSelectAliasesRange(scopeID, c.nonTrivia[k].End, condEnd)
			k++
			continue
		}
		if isPunctAt(c.src, c.nonTrivia, k, "(") {
			branchOpens, hasElse, k = c.handlePipeIfBranchParen(k, scopeID, aliases, branchOpens, hasElse)
			continue
		}
		k++
	}

	if len(branchOpens) > 0 {
		state := &pipeIfState{
			parentScopeID: scopeID,
			hasElse:       hasElse,
			branchTotal:   len(branchOpens),
		}
		for _, openIdx := range branchOpens {
			c.pipeIfByOpen[openIdx] = state
		}
	}
}

func (c *tokenAliasCollector) handlePipeIfBranchParen(
	openIdx, scopeID int,
	aliases []tokenDecl,
	branchOpens []int,
	hasElse bool,
) ([]int, bool, int) {
	isThen := isKeywordAt(c.src, c.nonTrivia, openIdx-1, "THEN")
	isElse := isKeywordAt(c.src, c.nonTrivia, openIdx-1, "ELSE")
	if isElse {
		hasElse = true
	}
	if isThen || isElse {
		c.copyPipeAliasesToParenScope(openIdx, aliases)
		if openIdx+1 < len(c.nonTrivia) && c.scopes[c.tokScopeIDs[openIdx+1]].depth > c.scopes[scopeID].depth {
			branchOpens = append(branchOpens, openIdx)
		}
	}

	return branchOpens, hasElse, skipBalancedParens(c.src, c.nonTrivia, openIdx)
}

func findPipeIfConditionEnd(
	src string,
	nonTrivia []syntax.Token,
	tokScopeIDs []int,
	startIdx, limit, scopeID, fallbackEnd int,
) int {
	for k := startIdx; k < limit; k++ {
		if tokScopeIDs[k] == scopeID && isKeywordAt(src, nonTrivia, k, "THEN") {
			return nonTrivia[k].Start
		}
	}

	return fallbackEnd
}

func (c *tokenAliasCollector) copyPipeAliasesToParenScope(openIdx int, aliases []tokenDecl) {
	if openIdx+1 >= len(c.nonTrivia) {
		return
	}

	parentScopeID := c.tokScopeIDs[openIdx]
	innerScopeID := c.tokScopeIDs[openIdx+1]
	if c.scopes[innerScopeID].depth <= c.scopes[parentScopeID].depth {
		return
	}

	if len(aliases) > 0 {
		c.pipeSelectAliases[innerScopeID] = append(c.pipeSelectAliases[innerScopeID], cloneDeclsForScope(aliases, innerScopeID)...)
	}
	if rvs := cloneRangeVarColsForScope(c.pipeRangeVarCols[parentScopeID], innerScopeID); len(rvs) > 0 {
		if c.pipeRangeVarCols[innerScopeID] == nil {
			c.pipeRangeVarCols[innerScopeID] = rvs
		} else {
			mergeRangeVarColsMap(c.pipeRangeVarCols[innerScopeID], rvs)
		}
	}
	if unks := c.pipeUnknownRangeVars[parentScopeID]; len(unks) > 0 {
		for k := range unks {
			c.setPipeUnknownRangeVar(innerScopeID, k)
		}
	}
}

func (c *tokenAliasCollector) updatePipePivotAliases(openIdx, scopeID int) {
	afterParen := skipBalancedParens(c.src, c.nonTrivia, openIdx)
	limit := min(afterParen-1, len(c.nonTrivia))
	startIdx := openIdx + 1
	if startIdx >= limit {
		return
	}

	forIdx := findTopLevelKeyword(c.src, c.nonTrivia, startIdx, limit, "FOR")
	inIdx := findTopLevelKeyword(c.src, c.nonTrivia, startIdx, limit, "IN")

	consumed := c.collectPivotConsumedCols(startIdx, forIdx, inIdx, limit)
	c.removePipeSelectAliases(scopeID, consumed)

	aggEnd := limit
	if forIdx >= 0 {
		aggEnd = forIdx
	}
	aggToks, hasUnaliasedAgg := c.collectPivotAggAliases(startIdx, aggEnd)

	if inIdx >= 0 && isPunctAt(c.src, c.nonTrivia, inIdx+1, "(") {
		inClose := skipBalancedParens(c.src, c.nonTrivia, inIdx+1)
		valToks := c.markTopLevelAsAliases(inIdx+2, min(inClose-1, limit))
		c.appendPivotOutputDecls(scopeID, aggToks, hasUnaliasedAgg, valToks)
	}
}

func (c *tokenAliasCollector) collectPivotAggAliases(startIdx, aggEnd int) (aggToks []int, hasUnaliased bool) {
	for _, span := range splitTopLevelCommaSpans(c.src, c.nonTrivia, startIdx, aggEnd) {
		al := c.markTopLevelAsAliases(span[0], span[1])
		if len(al) > 0 {
			aggToks = append(aggToks, al[len(al)-1])
		} else {
			hasUnaliased = true
		}
	}

	return aggToks, hasUnaliased
}

func (c *tokenAliasCollector) appendPivotOutputDecls(scopeID int, aggToks []int, hasUnaliased bool, valToks []int) {
	for _, vt := range valToks {
		valTok := c.nonTrivia[vt]
		valName := strings.Trim(valTok.Text(c.src), "`")
		if hasUnaliased {
			c.pipeSelectAliases[scopeID] = append(c.pipeSelectAliases[scopeID], tokenDecl{
				name:      valName,
				nameStart: valTok.Start,
				nameEnd:   valTok.End,
				scopeID:   scopeID,
				explicit:  true,
			})
		}
		for _, at := range aggToks {
			aggTok := c.nonTrivia[at]
			aggName := strings.Trim(aggTok.Text(c.src), "`")
			c.pipeSelectAliases[scopeID] = append(c.pipeSelectAliases[scopeID], tokenDecl{
				name:      aggName + "_" + valName,
				nameStart: aggTok.Start,
				nameEnd:   aggTok.End,
				scopeID:   scopeID,
				explicit:  true,
			})
		}
	}
}

func (c *tokenAliasCollector) collectPivotConsumedCols(startIdx, forIdx, inIdx, limit int) []string {
	var consumed []string
	aggEnd := limit
	if forIdx >= 0 {
		aggEnd = forIdx
	}
	for m := startIdx; m < aggEnd; m++ {
		if isPivotBareInputIdent(c.src, c.nonTrivia, m) {
			consumed = append(consumed, strings.Trim(c.nonTrivia[m].Text(c.src), "`"))
		}
	}

	if forIdx >= 0 {
		forEnd := limit
		if inIdx > forIdx {
			forEnd = inIdx
		}
		if forIdx+2 == forEnd && isIdentOrBacktick(c.nonTrivia[forIdx+1]) {
			consumed = append(consumed, strings.Trim(c.nonTrivia[forIdx+1].Text(c.src), "`"))
		}
	}

	return consumed
}

func isPivotBareInputIdent(src string, nonTrivia []syntax.Token, idx int) bool {
	if !isBareAliasIdent(src, nonTrivia[idx]) {
		return false
	}
	if idx > 0 && (isPunctAt(src, nonTrivia, idx-1, ".") || isKeywordAt(src, nonTrivia, idx-1, "AS")) {
		return false
	}
	if idx+1 < len(nonTrivia) && (isPunctAt(src, nonTrivia, idx+1, ".") || isPunctAt(src, nonTrivia, idx+1, "(")) {
		return false
	}

	return true
}

func (c *tokenAliasCollector) updatePipeUnpivotAliases(openIdx, scopeID int) {
	afterParen := skipBalancedParens(c.src, c.nonTrivia, openIdx)
	limit := min(afterParen-1, len(c.nonTrivia))
	startIdx := openIdx + 1
	if startIdx >= limit {
		return
	}

	forIdx := findTopLevelKeyword(c.src, c.nonTrivia, startIdx, limit, "FOR")
	inIdx := findTopLevelKeyword(c.src, c.nonTrivia, startIdx, limit, "IN")

	outToks := c.collectUnpivotOutputToks(startIdx, forIdx, inIdx, limit)
	consumed := c.collectUnpivotConsumedCols(inIdx, limit)

	c.removePipeSelectAliases(scopeID, consumed)
	c.pipeSelectAliases[scopeID] = append(c.pipeSelectAliases[scopeID], c.tokenDeclsFromIndices(scopeID, outToks)...)
}

func (c *tokenAliasCollector) collectUnpivotOutputToks(startIdx, forIdx, inIdx, limit int) []int {
	var outToks []int
	valStart := skipUnpivotNullModifier(c.src, c.nonTrivia, startIdx, limit)
	valEnd := limit
	if forIdx >= 0 {
		valEnd = forIdx
	}
	for m := valStart; m < valEnd; m++ {
		if isBareAliasIdent(c.src, c.nonTrivia[m]) {
			c.nonRefAliasPos[m] = true
			outToks = append(outToks, m)
		}
	}

	if forIdx >= 0 {
		nameEnd := limit
		if inIdx > forIdx {
			nameEnd = inIdx
		}
		if forIdx+1 < nameEnd && isBareAliasIdent(c.src, c.nonTrivia[forIdx+1]) {
			c.nonRefAliasPos[forIdx+1] = true
			outToks = append(outToks, forIdx+1)
		}
	}

	return outToks
}

func skipUnpivotNullModifier(src string, nonTrivia []syntax.Token, idx, limit int) int {
	if idx+1 < limit &&
		(isKeywordAt(src, nonTrivia, idx, "INCLUDE") || isKeywordAt(src, nonTrivia, idx, "EXCLUDE")) &&
		isKeywordAt(src, nonTrivia, idx+1, "NULLS") {
		return idx + 2
	}

	return idx
}

func (c *tokenAliasCollector) collectUnpivotConsumedCols(inIdx, limit int) []string {
	if inIdx < 0 || !isPunctAt(c.src, c.nonTrivia, inIdx+1, "(") {
		return nil
	}

	inClose := skipBalancedParens(c.src, c.nonTrivia, inIdx+1)
	inLimit := min(inClose-1, limit)
	var consumed []string
	for m := inIdx + 2; m < inLimit; m++ {
		tok := c.nonTrivia[m]
		if !isIdentOrBacktick(tok) || isKeywordAt(c.src, c.nonTrivia, m, "AS") || isKeywordAt(c.src, c.nonTrivia, m-1, "AS") {
			continue
		}
		consumed = append(consumed, strings.Trim(tok.Text(c.src), "`"))
	}

	return consumed
}

func isPipeExprClauseKeyword(src string, nonTrivia []syntax.Token, j int) bool {
	if nonTrivia[j].Kind != syntax.TokenIdent {
		return false
	}

	switch strings.ToUpper(nonTrivia[j].Text(src)) {
	case "WHERE", "HAVING", "QUALIFY", "ASSERT":
		return true
	default:
		return false
	}
}

func isPipeSetOpKeyword(src string, nonTrivia []syntax.Token, j int) bool {
	if nonTrivia[j].Kind != syntax.TokenIdent {
		return false
	}

	switch strings.ToUpper(nonTrivia[j].Text(src)) {
	case "UNION", "INTERSECT", "EXCEPT":
		return true
	case "LEFT", "INNER", "FULL", "OUTER", "CROSS", "CORRESPONDING":
		return isPipeSetOpPrefix(src, nonTrivia, j)
	default:
		return false
	}
}

func (c *tokenAliasCollector) emitPipeSelectAliasesRange(scopeID, startOff, endOff int) {
	c.emitSelectAliasDeclsFromDecls(c.pipeSelectAliases[scopeID], startOff, endOff, scopeID)
}

func (c *tokenAliasCollector) collectPipeExtendAsDecls(startIdx, endIdx, scopeID int) []tokenDecl {
	var out []tokenDecl
	for _, span := range splitTopLevelCommaSpans(c.src, c.nonTrivia, startIdx, endIdx) {
		if asDecls, hasAs := c.pipeSelectAsDecls(span[0], span[1], scopeID, c.pipeSelectAliases[scopeID]); hasAs {
			out = append(out, asDecls...)
		}
	}

	return out
}

func (c *tokenAliasCollector) computePipeSelectOutputAliases(
	startIdx, endIdx, scopeID int,
	prevAliases []tokenDecl,
) []tokenDecl {
	var out []tokenDecl
	for _, span := range splitTopLevelCommaSpans(c.src, c.nonTrivia, startIdx, endIdx) {
		itemStart, itemEnd := span[0], span[1]
		if asDecls, hasAs := c.pipeSelectAsDecls(itemStart, itemEnd, scopeID, prevAliases); hasAs {
			out = append(out, asDecls...)
			continue
		}
		if starOut, isStar := c.computePipeSelectStarItem(itemStart, itemEnd, scopeID, prevAliases); isStar {
			out = append(out, starOut...)
			continue
		}
		out = append(out, c.passThroughItemDecl(itemStart, itemEnd, scopeID, prevAliases)...)
	}

	return out
}

func (c *tokenAliasCollector) pipeSelectAsDecls(
	itemStart, itemEnd, scopeID int,
	prevAliases []tokenDecl,
) ([]tokenDecl, bool) {
	asAliases := c.markTopLevelAsAliases(itemStart, itemEnd)
	if len(asAliases) == 0 {
		return nil, false
	}

	decls := c.tokenDeclsFromIndices(scopeID, asAliases)
	if ac, ok := extractSingleAsAliasParts(c.src, c.nonTrivia, itemStart, itemEnd, asAliases); ok {
		decls[0].structFields = c.resolvePipePathStructFields(ac.parts, scopeID, prevAliases)
	}

	return decls, true
}

func (c *tokenAliasCollector) resolvePipePathStructFields(
	parts []string,
	scopeID int,
	prevAliases []tokenDecl,
) []catalog.ColumnMeta {
	if len(parts) == 0 {
		return nil
	}
	if len(parts) == 1 {
		if col, ok := findDeclByName(prevAliases, parts[0]); ok {
			return col.structFields
		}
		return nil
	}
	if rvCols, ok := c.pipeRangeVarCols[scopeID][strings.ToLower(parts[0])]; ok {
		return resolveStructFieldsInCols(rvCols, parts[1:])
	}

	return resolveStructFieldsInCols(prevAliases, parts)
}

func (c *tokenAliasCollector) computePipeSelectStarItem(
	itemStart, itemEnd, scopeID int,
	prevAliases []tokenDecl,
) ([]tokenDecl, bool) {
	starIdx := findTopLevelStarIdx(c.src, c.nonTrivia, itemStart, itemEnd)
	if starIdx < 0 {
		return nil, false
	}
	if starIdx != itemStart && !isPunctAt(c.src, c.nonTrivia, starIdx-1, ".") {
		return nil, false
	}

	exceptNames, replaceDecls, _, valid := c.parseStarModifiers(starIdx+1, itemEnd, scopeID)
	if !valid {
		return nil, false
	}

	sourceCols := prevAliases
	if starIdx > itemStart {
		qualCols, knownQual := c.resolvePipeDotStarCols(itemStart, starIdx, scopeID)
		if !knownQual {
			return replaceDecls, true
		}
		sourceCols = qualCols
	}

	var out []tokenDecl
	for _, d := range sourceCols {
		if containsFoldString(exceptNames, d.name) || containsDeclName(replaceDecls, d.name) {
			continue
		}
		d.scopeID = scopeID
		out = append(out, d)
	}
	out = append(out, replaceDecls...)

	return out, true
}

func (c *tokenAliasCollector) resolvePipeDotStarCols(itemStart, starIdx, scopeID int) ([]tokenDecl, bool) {
	parts := readDotStarQualifierParts(c.src, c.nonTrivia, itemStart, starIdx)
	if len(parts) == 0 {
		return nil, false
	}

	anchorTok := c.nonTrivia[itemStart]
	if len(parts) == 1 {
		if cols, ok := c.pipeRangeVarCols[scopeID][strings.ToLower(parts[0])]; ok {
			return cols, true
		}
		if sfCols := structFieldDeclsFromCols(c.pipeSelectAliases[scopeID], parts, anchorTok, scopeID); len(sfCols) > 0 {
			return sfCols, true
		}
		return nil, false
	}

	if rvCols, ok := c.pipeRangeVarCols[scopeID][strings.ToLower(parts[0])]; ok {
		if sfCols := structFieldDeclsFromCols(rvCols, parts[1:], anchorTok, scopeID); len(sfCols) > 0 {
			return sfCols, true
		}
		return nil, false
	}

	if sfCols := structFieldDeclsFromCols(c.pipeSelectAliases[scopeID], parts, anchorTok, scopeID); len(sfCols) > 0 {
		return sfCols, true
	}

	return nil, false
}

func structFieldDeclsFromCols(
	cols []tokenDecl,
	structPath []string,
	anchorTok syntax.Token,
	scopeID int,
) []tokenDecl {
	fields := resolveStructFieldsInCols(cols, structPath)
	if len(fields) == 0 {
		return nil
	}

	decls := make([]tokenDecl, 0, len(fields))
	for i := range fields {
		decls = append(decls, tokenDecl{
			name:         fields[i].Name,
			nameStart:    anchorTok.Start,
			nameEnd:      anchorTok.End,
			scopeID:      scopeID,
			explicit:     true,
			structFields: fields[i].Fields,
		})
	}

	return decls
}

func resolveStructFieldsInCols(cols []tokenDecl, structPath []string) []catalog.ColumnMeta {
	if len(cols) == 0 || len(structPath) == 0 {
		return nil
	}

	col, ok := findDeclByName(cols, structPath[0])
	if !ok || len(col.structFields) == 0 {
		return nil
	}

	fields := col.structFields
	for _, seg := range structPath[1:] {
		next, found := findNestedColMetaField(fields, seg)
		if !found {
			return nil
		}
		fields = next.Fields
	}

	return fields
}

func findStructFieldInCols(cols []tokenDecl, structPath []string, fieldName string) (catalog.ColumnMeta, bool) {
	fields := resolveStructFieldsInCols(cols, structPath)
	if len(fields) == 0 {
		return catalog.ColumnMeta{}, false
	}

	return findNestedColMetaField(fields, fieldName)
}

func structFieldExistsInCols(cols []tokenDecl, structPath []string, fieldName string) bool {
	_, ok := findStructFieldInCols(cols, structPath, fieldName)

	return ok
}

func findNestedColMetaField(fields []catalog.ColumnMeta, name string) (catalog.ColumnMeta, bool) {
	for i := range fields {
		if strings.EqualFold(fields[i].Name, name) {
			return fields[i], true
		}
	}

	return catalog.ColumnMeta{}, false
}

func findTopLevelStarIdx(src string, nonTrivia []syntax.Token, startIdx, endIdx int) int {
	st := updateSetState{}
	for j := startIdx; j < endIdx; j++ {
		tok := nonTrivia[j]
		txt := tok.Text(src)
		if st.parenDepth == 0 && st.bracketDepth == 0 && st.caseDepth == 0 && isStarOperatorAt(src, nonTrivia, j) {
			return j
		}
		updateSetNesting(tok, txt, &st)
	}

	return -1
}

func (c *tokenAliasCollector) parseStarModifiers(
	startIdx, itemEnd, scopeID int,
) (exceptNames []string, replaceDecls []tokenDecl, replaceAliased []aliasedProjCol, valid bool) {
	k := startIdx
	for k < itemEnd {
		switch {
		case isKeywordAt(c.src, c.nonTrivia, k, "EXCEPT") && isPunctAt(c.src, c.nonTrivia, k+1, "("):
			for _, colIdx := range collectParenColumnListIndices(c.src, c.nonTrivia, k+1) {
				exceptNames = append(exceptNames, strings.Trim(c.nonTrivia[colIdx].Text(c.src), "`"))
			}
			k = skipBalancedParens(c.src, c.nonTrivia, k+1)
		case isKeywordAt(c.src, c.nonTrivia, k, "REPLACE") && isPunctAt(c.src, c.nonTrivia, k+1, "("):
			afterRep := skipBalancedParens(c.src, c.nonTrivia, k+1)
			repEnd := min(afterRep-1, itemEnd)
			decls, aliased := c.parseStarReplaceItems(k+2, repEnd, scopeID)
			replaceDecls = append(replaceDecls, decls...)
			replaceAliased = append(replaceAliased, aliased...)
			k = afterRep
		default:
			return nil, nil, nil, false
		}
	}

	return exceptNames, replaceDecls, replaceAliased, true
}

func (c *tokenAliasCollector) parseStarReplaceItems(
	startIdx, endIdx, scopeID int,
) (replaceDecls []tokenDecl, replaceAliased []aliasedProjCol) {
	for _, sp := range splitTopLevelCommaSpans(c.src, c.nonTrivia, startIdx, endIdx) {
		asAliases := c.markTopLevelAsAliases(sp[0], sp[1])
		if len(asAliases) == 0 {
			continue
		}
		decls := c.tokenDeclsFromIndices(scopeID, asAliases)
		if ac, ok := extractSingleAsAliasParts(c.src, c.nonTrivia, sp[0], sp[1], asAliases); ok {
			decls[0].structFields = c.resolvePipePathStructFields(ac.parts, scopeID, c.pipeSelectAliases[scopeID])
			replaceAliased = append(replaceAliased, ac)
		}
		replaceDecls = append(replaceDecls, decls...)
	}

	return replaceDecls, replaceAliased
}

func (c *tokenAliasCollector) computePipeAggregateOutputAliases(
	startIdx, endIdx, scopeID int,
	prevAliases []tokenDecl,
) []tokenDecl {
	groupIdx := findTopLevelKeyword(c.src, c.nonTrivia, startIdx, endIdx, "GROUP")
	aggEnd := endIdx
	if groupIdx >= 0 {
		aggEnd = groupIdx
	}

	out := c.tokenDeclsFromIndices(scopeID, c.markTopLevelAsAliases(startIdx, aggEnd))
	if groupIdx < 0 {
		return out
	}

	gbStart := skipGroupByHeader(c.src, c.nonTrivia, groupIdx, endIdx)
	for _, span := range splitTopLevelCommaSpans(c.src, c.nonTrivia, gbStart, endIdx) {
		itemStart, itemEnd := span[0], span[1]
		if asDecls, hasAs := c.pipeSelectAsDecls(itemStart, itemEnd, scopeID, prevAliases); hasAs {
			out = append(out, asDecls...)
			continue
		}
		out = append(out, c.passThroughItemDecl(itemStart, itemEnd, scopeID, prevAliases)...)
	}

	return out
}

func (c *tokenAliasCollector) tokenDeclsFromIndices(scopeID int, tokIndices []int) []tokenDecl {
	out := make([]tokenDecl, 0, len(tokIndices))
	for _, tokIdx := range tokIndices {
		tok := c.nonTrivia[tokIdx]
		out = append(out, tokenDecl{
			name:      strings.Trim(tok.Text(c.src), "`"),
			nameStart: tok.Start,
			nameEnd:   tok.End,
			scopeID:   scopeID,
			explicit:  true,
		})
	}

	return out
}

func (c *tokenAliasCollector) passThroughItemDecl(itemStart, itemEnd, scopeID int, prevAliases []tokenDecl) []tokenDecl {
	leafIdx, isDotted, isBare := classifyPassThroughSelectItem(c.src, c.nonTrivia, itemStart, itemEnd)
	if !isDotted && !isBare {
		return nil
	}

	tok := c.nonTrivia[leafIdx]
	name := strings.Trim(tok.Text(c.src), "`")
	if isBare {
		if col, ok := findDeclByName(prevAliases, name); ok {
			d := newExplicitTokenDecl(name, tok, scopeID)
			d.structFields = col.structFields
			return []tokenDecl{d}
		}
		return nil
	}

	parts := readPassThroughDottedParts(c.src, c.nonTrivia, itemStart, itemEnd)
	if len(parts) < 2 || !c.pipeDottedItemExists(parts, scopeID, prevAliases) {
		return nil
	}

	d := newExplicitTokenDecl(name, tok, scopeID)
	d.structFields = c.resolvePipePathStructFields(parts, scopeID, prevAliases)

	return []tokenDecl{d}
}

func newExplicitTokenDecl(name string, tok syntax.Token, scopeID int) tokenDecl {
	return tokenDecl{
		name:      name,
		nameStart: tok.Start,
		nameEnd:   tok.End,
		scopeID:   scopeID,
		explicit:  true,
	}
}

func (c *tokenAliasCollector) pipeDottedItemExists(parts []string, scopeID int, prevAliases []tokenDecl) bool {
	headKey := strings.ToLower(parts[0])
	if rvCols, ok := c.pipeRangeVarCols[scopeID][headKey]; ok {
		if len(parts) == 2 {
			return containsDeclName(rvCols, parts[1])
		}
		return structFieldExistsInCols(rvCols, parts[1:len(parts)-1], parts[len(parts)-1])
	}

	if c.pipeUnknownRangeVars[scopeID][headKey] {
		return true
	}

	if col, ok := findDeclByName(prevAliases, parts[0]); ok && len(col.structFields) > 0 {
		return structFieldExistsInCols([]tokenDecl{col}, parts[:len(parts)-1], parts[len(parts)-1])
	}

	return false
}

func findDeclIndexByName(decls []tokenDecl, target string) int {
	for i := range decls {
		if strings.EqualFold(decls[i].name, target) {
			return i
		}
	}

	return -1
}

func findDeclByName(decls []tokenDecl, target string) (tokenDecl, bool) {
	if idx := findDeclIndexByName(decls, target); idx >= 0 {
		return decls[idx], true
	}

	return tokenDecl{}, false
}

func containsDeclName(decls []tokenDecl, target string) bool {
	return findDeclIndexByName(decls, target) >= 0
}

func splitTopLevelCommaSpans(src string, nonTrivia []syntax.Token, startIdx, endIdx int) [][2]int {
	limit := min(endIdx, len(nonTrivia))
	if startIdx >= limit {
		return nil
	}

	var spans [][2]int
	itemStart := startIdx
	st := updateSetState{}
	for j := startIdx; j < limit; j++ {
		tok := nonTrivia[j]
		txt := tok.Text(src)
		if st.parenDepth == 0 && st.bracketDepth == 0 && st.caseDepth == 0 &&
			tok.Kind == syntax.TokenPunct && txt == "," {
			if itemStart < j {
				spans = append(spans, [2]int{itemStart, j})
			}
			itemStart = j + 1
			continue
		}
		updateSetNesting(tok, txt, &st)
	}
	if itemStart < limit {
		spans = append(spans, [2]int{itemStart, limit})
	}

	return spans
}

func classifyPassThroughSelectItem(src string, nonTrivia []syntax.Token, startIdx, endIdx int) (leafIdx int, isDotted, isBare bool) {
	if startIdx >= endIdx || !isIdentOrBacktick(nonTrivia[startIdx]) {
		return -1, false, false
	}

	if endIdx == startIdx+1 {
		return startIdx, false, true
	}

	idx := startIdx
	for idx+2 < endIdx {
		if nonTrivia[idx+1].Kind != syntax.TokenPunct || nonTrivia[idx+1].Text(src) != "." {
			return -1, false, false
		}
		if !isIdentOrBacktick(nonTrivia[idx+2]) {
			return -1, false, false
		}
		idx += 2
	}
	if idx+1 != endIdx {
		return -1, false, false
	}

	return idx, true, false
}

func findTopLevelKeyword(src string, nonTrivia []syntax.Token, startIdx, endIdx int, kw string) int {
	limit := min(endIdx, len(nonTrivia))
	st := updateSetState{}
	for j := startIdx; j < limit; j++ {
		tok := nonTrivia[j]
		txt := tok.Text(src)
		if st.parenDepth == 0 && st.bracketDepth == 0 && st.caseDepth == 0 && isKeywordAt(src, nonTrivia, j, kw) {
			return j
		}
		updateSetNesting(tok, txt, &st)
	}

	return -1
}

func skipGroupByHeader(src string, nonTrivia []syntax.Token, groupIdx, endIdx int) int {
	k := groupIdx + 1
	if k < endIdx && isKeywordAt(src, nonTrivia, k, "AND") && isKeywordAt(src, nonTrivia, k+1, "ORDER") {
		k += 2
	}
	if k < endIdx && isKeywordAt(src, nonTrivia, k, "BY") {
		k++
	}

	return k
}

func isPipeOpAt(src string, nonTrivia []syntax.Token, idx int) bool {
	return idx >= 0 && idx < len(nonTrivia) &&
		nonTrivia[idx].Kind == syntax.TokenOperator &&
		nonTrivia[idx].Text(src) == "|>"
}

func (c *tokenAliasCollector) removePipeSelectAliases(scopeID int, dropped []string) {
	if len(dropped) == 0 || len(c.pipeSelectAliases[scopeID]) == 0 {
		return
	}

	active := c.pipeSelectAliases[scopeID]
	kept := active[:0]
	for _, d := range active {
		if containsFoldString(dropped, d.name) {
			continue
		}
		kept = append(kept, d)
	}
	c.pipeSelectAliases[scopeID] = kept
}

func containsFoldString(items []string, target string) bool {
	for _, item := range items {
		if strings.EqualFold(item, target) {
			return true
		}
	}

	return false
}

func (c *tokenAliasCollector) handlePipeOrderByAliases(pipeIdx, orderIdx, scopeID int) {
	aliases := c.pipeSelectAliases[scopeID]
	if len(aliases) == 0 {
		return
	}

	orderEnd := nextPipeOrderByEndInScope(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, pipeIdx, scopeID)
	orderStart := c.nonTrivia[orderIdx].Start
	if orderStart >= orderEnd {
		return
	}

	sc := tokenScope{depth: c.scopes[scopeID].depth, start: orderStart, end: orderEnd}
	for _, d := range aliases {
		d.scope = sc
		c.selectAliasDecls = append(c.selectAliasDecls, d)
	}
}

func nextPipeOrderByEndInScope(
	src string,
	nonTrivia []syntax.Token,
	scopes []tokenScope,
	tokScopeIDs []int,
	pipeIdx, scopeID int,
) int {
	scopeDepth := scopes[scopeID].depth
	for k := pipeIdx + 1; k < len(nonTrivia); k++ {
		if scopes[tokScopeIDs[k]].depth < scopeDepth {
			return nonTrivia[k].Start
		}
		if tokScopeIDs[k] != scopeID {
			continue
		}
		if isSelectQueryEndToken(src, nonTrivia, k, false) {
			return nonTrivia[k].Start
		}
	}

	return scopes[scopeID].end
}

func (c *tokenAliasCollector) markPipeDropCols(startIdx, scopeID int) []string {
	endIdx := nextPipeTokenIdxInScope(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, startIdx-1, scopeID)
	scopeDepth := c.scopes[scopeID].depth
	var dropped []string
	for k := startIdx; k < endIdx; k++ {
		if c.scopes[c.tokScopeIDs[k]].depth < scopeDepth {
			return dropped
		}
		if c.tokScopeIDs[k] == scopeID && isIdentOrBacktick(c.nonTrivia[k]) {
			c.nonRefAliasPos[k] = true
			dropped = append(dropped, strings.Trim(c.nonTrivia[k].Text(c.src), "`"))
		}
	}

	return dropped
}

func (c *tokenAliasCollector) handlePipeRenameAliases(startIdx, scopeID int) {
	endIdx := nextPipeTokenIdxInScope(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, startIdx-1, scopeID)
	scopeDepth := c.scopes[scopeID].depth
	prevAliases := slices.Clone(c.pipeSelectAliases[scopeID])
	var oldNames []string
	var newDecls []tokenDecl
	var itemIdents []int

	flushItem := func() {
		if len(itemIdents) == 0 {
			return
		}
		oldName := strings.Trim(c.nonTrivia[itemIdents[0]].Text(c.src), "`")
		oldNames = append(oldNames, oldName)
		if len(itemIdents) >= 2 {
			tok := c.nonTrivia[itemIdents[1]]
			d := newExplicitTokenDecl(strings.Trim(tok.Text(c.src), "`"), tok, scopeID)
			if oldDecl, ok := findDeclByName(prevAliases, oldName); ok {
				d.structFields = oldDecl.structFields
			}
			newDecls = append(newDecls, d)
		}
		itemIdents = itemIdents[:0]
	}

	for k := startIdx; k < endIdx; k++ {
		if c.scopes[c.tokScopeIDs[k]].depth < scopeDepth {
			break
		}
		if c.tokScopeIDs[k] != scopeID {
			continue
		}
		if isPunctAt(c.src, c.nonTrivia, k, ",") {
			flushItem()
			continue
		}
		if isIdentOrBacktick(c.nonTrivia[k]) && !isKeywordAt(c.src, c.nonTrivia, k, "AS") {
			c.nonRefAliasPos[k] = true
			itemIdents = append(itemIdents, k)
		}
	}
	flushItem()

	c.removePipeSelectAliases(scopeID, oldNames)
	c.pipeSelectAliases[scopeID] = appendOrReplacePipeDecls(c.pipeSelectAliases[scopeID], newDecls)
}

func appendOrReplacePipeDecls(dst, src []tokenDecl) []tokenDecl {
	for _, d := range src {
		if idx := findDeclIndexByName(dst, d.name); idx >= 0 {
			dst[idx] = d
		} else {
			dst = append(dst, d)
		}
	}

	return dst
}

func (c *tokenAliasCollector) markTopLevelAsAliases(startIdx, endIdx int) []int {
	var topAliases []int
	st := updateSetState{}
	limit := min(endIdx, len(c.nonTrivia))
	for j := startIdx; j < limit; j++ {
		tok := c.nonTrivia[j]
		txt := tok.Text(c.src)
		if isKeywordAt(c.src, c.nonTrivia, j, "MATCH_RECOGNIZE") && isPunctAt(c.src, c.nonTrivia, j+1, "(") {
			j = skipBalancedParens(c.src, c.nonTrivia, j+1) - 1
			continue
		}
		if st.caseDepth == 0 && isKeywordAt(c.src, c.nonTrivia, j, "AS") &&
			j+1 < limit && isIdentOrBacktick(c.nonTrivia[j+1]) {
			c.nonRefAliasPos[j+1] = true
			if st.parenDepth == 0 && st.bracketDepth == 0 {
				topAliases = append(topAliases, j+1)
			}
			j++
			continue
		}

		updateSetNesting(tok, txt, &st)
		if st.parenDepth < 0 || st.bracketDepth < 0 {
			return topAliases
		}
	}

	return topAliases
}

func (c *tokenAliasCollector) applyPipeBarrierAlias(scopeID, pipeIdx, clampAt, aliasStart int, newDecls []tokenDecl) {
	stmtEnd := nextStmtEndInScope(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, pipeIdx, scopeID, c.scopes[scopeID].end)
	c.clampDeclsAtBarrier(scopeID, clampAt)

	sc := c.scopes[scopeID]
	for i := range newDecls {
		newDecls[i].scope = tokenScope{depth: sc.depth, start: aliasStart, end: stmtEnd}
		newDecls[i].scopeID = scopeID
	}
	c.decls = append(c.decls, newDecls...)
}

func (c *tokenAliasCollector) handlePipeAs(asIdx, scopeID int) {
	if asIdx+1 >= len(c.nonTrivia) || !isIdentOrBacktick(c.nonTrivia[asIdx+1]) {
		return
	}

	al := c.nonTrivia[asIdx+1]
	aliasName := strings.Trim(al.Text(c.src), "`")
	c.setPipeRangeVarCols(scopeID, aliasName, c.pipeSelectAliases[scopeID])
	c.applyPipeBarrierAlias(scopeID, asIdx-1, al.End, al.End, []tokenDecl{{
		name:      aliasName,
		nameStart: al.Start,
		nameEnd:   al.End,
		explicit:  true,
	}})
}

func (c *tokenAliasCollector) handlePipePostfixOpAt(opIdx, openIdx, scopeID int) {
	afterParen := skipBalancedParens(c.src, c.nonTrivia, openIdx)
	afterParen = skipHintIfPresent(c.src, c.nonTrivia, afterParen)
	newDecls, afterAl := readExplicitAliasAfterFactor(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, afterParen)
	if len(newDecls) > 0 {
		c.setPipeRangeVarCols(scopeID, newDecls[0].name, c.pipeSelectAliases[scopeID])
	}

	barrierEnd := c.nonTrivia[afterParen-1].End
	if afterAl > afterParen && afterAl-1 < len(c.nonTrivia) {
		barrierEnd = c.nonTrivia[afterAl-1].End
	}
	c.applyPipeBarrierAlias(scopeID, opIdx-1, barrierEnd, barrierEnd, newDecls)
}

func (c *tokenAliasCollector) handlePipeCallOp(callIdx, scopeID int) {
	lastName, _, _, _, nextIdx, trailingDot := readDottedIdentSpan(c.src, c.nonTrivia, c.tokScopeIDs, callIdx+1)
	if lastName == "" || trailingDot || !isPunctAt(c.src, c.nonTrivia, nextIdx, "(") {
		barrierEnd := nextPipeStartInScope(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, callIdx-1, scopeID, c.scopes[scopeID].end)
		c.clampDeclsAtBarrier(scopeID, barrierEnd)
		return
	}

	afterParen := skipBalancedParens(c.src, c.nonTrivia, nextIdx)
	afterParen = skipHintIfPresent(c.src, c.nonTrivia, afterParen)
	newDecls, afterAl := readExplicitAliasAfterFactor(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, afterParen)

	barrierEnd := c.nonTrivia[afterParen-1].End
	if afterAl > afterParen && afterAl-1 < len(c.nonTrivia) {
		barrierEnd = c.nonTrivia[afterAl-1].End
	}
	c.applyPipeBarrierAlias(scopeID, callIdx-1, barrierEnd, barrierEnd, newDecls)
}

func (c *tokenAliasCollector) handlePipeRecursiveUnion(recIdx, pipeIdx, scopeID int) {
	lastParenEnd := c.findLastParenEndBeforePipe(recIdx+1, scopeID)
	if lastParenEnd <= 0 || lastParenEnd > len(c.nonTrivia) {
		barrierEnd := nextPipeStartInScope(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, pipeIdx, scopeID, c.scopes[scopeID].end)
		c.clampDeclsAtBarrier(scopeID, barrierEnd)
		return
	}

	newDecls, afterAl := readExplicitAliasAfterFactor(c.src, c.nonTrivia, c.scopes, c.tokScopeIDs, lastParenEnd)
	aliasStart := c.nonTrivia[lastParenEnd-1].End
	if afterAl > lastParenEnd && afterAl-1 < len(c.nonTrivia) {
		aliasStart = c.nonTrivia[afterAl-1].End
	}
	c.applyPipeBarrierAlias(scopeID, pipeIdx, c.nonTrivia[pipeIdx].Start, aliasStart, newDecls)
}

func (c *tokenAliasCollector) findLastParenEndBeforePipe(startIdx, scopeID int) int {
	lastParenEnd := -1
	for k := startIdx; k < len(c.nonTrivia); {
		tok := c.nonTrivia[k]
		if tok.Kind == syntax.TokenOperator && tok.Text(c.src) == "|>" {
			break
		}
		if tok.Kind == syntax.TokenPunct && tok.Text(c.src) == ";" {
			break
		}
		if c.tokScopeIDs[k] != scopeID && tok.Kind == syntax.TokenPunct && tok.Text(c.src) == ")" {
			break
		}
		if isPunctAt(c.src, c.nonTrivia, k, "(") {
			k = skipBalancedParens(c.src, c.nonTrivia, k)
			lastParenEnd = k
			continue
		}
		k++
	}

	return lastParenEnd
}

func (c *tokenAliasCollector) clampDeclsAtBarrier(scopeID, barrierEnd int) {
	clampTokenDeclsScopeEnd(c.decls, scopeID, barrierEnd)
	clampTokenDeclsScopeEnd(c.withOffsetDecls, scopeID, barrierEnd)
}

func clampTokenDeclsScopeEnd(decls []tokenDecl, scopeID, barrierEnd int) {
	for i := range decls {
		if decls[i].scopeID == scopeID && decls[i].scope.end > barrierEnd {
			decls[i].scope.end = barrierEnd
		}
	}
}

func nextPipeTokenIdxInScope(
	src string,
	nonTrivia []syntax.Token,
	scopes []tokenScope,
	tokScopeIDs []int,
	pipeIdx int,
	scopeID int,
) int {
	scopeDepth := scopes[scopeID].depth
	for k := pipeIdx + 1; k < len(nonTrivia); k++ {
		if scopes[tokScopeIDs[k]].depth < scopeDepth {
			return k
		}
		if tokScopeIDs[k] != scopeID {
			continue
		}
		tok := nonTrivia[k]
		if (tok.Kind == syntax.TokenOperator && tok.Text(src) == "|>") ||
			(tok.Kind == syntax.TokenPunct && tok.Text(src) == ";") {
			return k
		}
	}

	return len(nonTrivia)
}

func nextStmtEndInScope(
	src string,
	nonTrivia []syntax.Token,
	scopes []tokenScope,
	tokScopeIDs []int,
	pipeIdx int,
	scopeID int,
	scopeEnd int,
) int {
	scopeDepth := scopes[scopeID].depth
	for k := pipeIdx + 1; k < len(nonTrivia); k++ {
		if scopes[tokScopeIDs[k]].depth < scopeDepth {
			return nonTrivia[k].Start
		}
		if tokScopeIDs[k] != scopeID {
			continue
		}
		if nonTrivia[k].Kind == syntax.TokenPunct && nonTrivia[k].Text(src) == ";" {
			return nonTrivia[k].Start
		}
	}

	return scopeEnd
}

func nextPipeStartInScope(
	src string,
	nonTrivia []syntax.Token,
	scopes []tokenScope,
	tokScopeIDs []int,
	pipeIdx int,
	scopeID int,
	scopeEnd int,
) int {
	scopeDepth := scopes[scopeID].depth
	for k := pipeIdx + 1; k < len(nonTrivia); k++ {
		if scopes[tokScopeIDs[k]].depth < scopeDepth {
			return nonTrivia[k].Start
		}
		if tokScopeIDs[k] != scopeID {
			continue
		}
		tok := nonTrivia[k]
		if tok.Kind == syntax.TokenOperator && tok.Text(src) == "|>" {
			return tok.Start
		}
		if tok.Kind == syntax.TokenPunct && tok.Text(src) == ";" {
			return tok.Start
		}
	}

	return scopeEnd
}

func isPipeSetOpPrefix(src string, nonTrivia []syntax.Token, j int) bool {
	limit := min(j+8, len(nonTrivia))
	for k := j; k < limit; k++ {
		tok := nonTrivia[k]
		if tok.Kind == syntax.TokenOperator && tok.Text(src) == "|>" {
			return false
		}
		if tok.Kind == syntax.TokenPunct && (tok.Text(src) == ";" || tok.Text(src) == "(") {
			return false
		}
		if tok.Kind != syntax.TokenIdent {
			continue
		}

		switch strings.ToUpper(tok.Text(src)) {
		case "UNION", "INTERSECT", "EXCEPT":
			return true
		case "JOIN":
			return false
		default:
		}
	}

	return false
}

func findPipeJoinKeyword(src string, nonTrivia []syntax.Token, j int) (int, bool) {
	limit := min(j+5, len(nonTrivia))
	for k := j; k < limit; k++ {
		tok := nonTrivia[k]
		if tok.Kind != syntax.TokenIdent {
			return 0, false
		}

		switch strings.ToUpper(tok.Text(src)) {
		case "JOIN":
			return k, true
		case "LEFT", "RIGHT", "FULL", "INNER", "OUTER", "CROSS", "NATURAL", "HASH", "LOOKUP":
			continue
		default:
			return 0, false
		}
	}

	return 0, false
}

func isPostfixReplacingKeyword(src string, nonTrivia []syntax.Token, idx int) bool {
	if idx >= len(nonTrivia) || nonTrivia[idx].Kind != syntax.TokenIdent {
		return false
	}

	switch strings.ToUpper(nonTrivia[idx].Text(src)) {
	case "PIVOT", "UNPIVOT", "MATCH_RECOGNIZE":
		return true
	default:
		return false
	}
}

func isKeywordAt(src string, nonTrivia []syntax.Token, idx int, kw string) bool {
	return idx >= 0 && idx < len(nonTrivia) &&
		nonTrivia[idx].Kind == syntax.TokenIdent &&
		strings.EqualFold(nonTrivia[idx].Text(src), kw)
}

func isPunctAt(src string, nonTrivia []syntax.Token, idx int, p string) bool {
	return idx >= 0 && idx < len(nonTrivia) &&
		nonTrivia[idx].Kind == syntax.TokenPunct &&
		nonTrivia[idx].Text(src) == p
}

func readUnnestWithOffsetAlias(
	src string,
	nonTrivia []syntax.Token,
	scopes []tokenScope,
	tokScopeIDs []int,
	idx int,
) ([]tokenDecl, int) {
	if idx+1 >= len(nonTrivia) {
		return nil, idx
	}

	if nonTrivia[idx].Kind != syntax.TokenIdent || !strings.EqualFold(nonTrivia[idx].Text(src), "WITH") {
		return nil, idx
	}

	if nonTrivia[idx+1].Kind != syntax.TokenIdent || !strings.EqualFold(nonTrivia[idx+1].Text(src), "OFFSET") {
		return nil, idx
	}

	return readExplicitAliasAfterFactor(src, nonTrivia, scopes, tokScopeIDs, idx+2)
}

func readExplicitAliasAfterFactor(
	src string,
	nonTrivia []syntax.Token,
	scopes []tokenScope,
	tokScopeIDs []int,
	idx int,
) ([]tokenDecl, int) {
	if idx >= len(nonTrivia) {
		return nil, idx
	}

	if nonTrivia[idx].Kind == syntax.TokenIdent && strings.EqualFold(nonTrivia[idx].Text(src), "AS") {
		if idx+1 < len(nonTrivia) && isIdentOrBacktick(nonTrivia[idx+1]) {
			al := nonTrivia[idx+1]

			return []tokenDecl{{
				name:      strings.Trim(al.Text(src), "`"),
				nameStart: al.Start,
				nameEnd:   al.End,
				scope:     scopes[tokScopeIDs[idx+1]],
				scopeID:   tokScopeIDs[idx+1],
				explicit:  true,
			}}, idx + 2
		}

		return nil, idx + 1
	}

	if isBareAliasIdent(src, nonTrivia[idx]) {
		al := nonTrivia[idx]

		return []tokenDecl{{
			name:      strings.Trim(al.Text(src), "`"),
			nameStart: al.Start,
			nameEnd:   al.End,
			scope:     scopes[tokScopeIDs[idx]],
			scopeID:   tokScopeIDs[idx],
			explicit:  true,
		}}, idx + 1
	}

	return nil, idx
}

func extractIdentSpan(src string, tok syntax.Token) (name string, start, end int) {
	if tok.Kind == syntax.TokenBacktickIdent {
		parts := strings.Split(strings.Trim(tok.Text(src), "`"), ".")
		return parts[len(parts)-1], tok.Start, tok.End
	}

	return tok.Text(src), tok.Start, tok.End
}

func readDottedIdentSpan(
	src string,
	nonTrivia []syntax.Token,
	tokScopeIDs []int,
	start int,
) (lastName string, lastStart, lastEnd, scopeID, nextIdx int, trailingDot bool) {
	if start >= len(nonTrivia) {
		return "", 0, 0, 0, start, false
	}

	tok := nonTrivia[start]
	if !isTablePathIdent(src, tok) {
		return "", 0, 0, 0, start, false
	}

	lastName, lastStart, lastEnd = extractIdentSpan(src, tok)
	scopeID = tokScopeIDs[start]
	idx := start + 1

	for idx < len(nonTrivia) && nonTrivia[idx].Kind == syntax.TokenPunct && nonTrivia[idx].Text(src) == "." {
		if idx+1 >= len(nonTrivia) || !isIdentOrBacktick(nonTrivia[idx+1]) {
			trailingDot = true
			break
		}

		seg := nonTrivia[idx+1]
		if seg.Start > nonTrivia[idx].End && isTablePathReservedKeyword(src, seg) {
			trailingDot = true
			break
		}

		lastName, lastStart, lastEnd = extractIdentSpan(src, seg)
		scopeID = tokScopeIDs[idx+1]
		idx += 2
	}

	return lastName, lastStart, lastEnd, scopeID, idx, trailingDot
}

var fromTerminatorKeywords = buildFromTerminatorKeywords()

func buildFromTerminatorKeywords() map[string]struct{} {
	words := []string{
		"WHERE", "GROUP", "HAVING", "QUALIFY", "WINDOW", "ORDER", "LIMIT",
		"FETCH", "RETURNING", "UNION", "EXCEPT", "INTERSECT", "SET", "VALUES", "INTO",
	}
	out := make(map[string]struct{}, len(words))
	for _, w := range words {
		out[w] = struct{}{}
	}

	return out
}

func advanceToNextFromItem(src string, nonTrivia []syntax.Token, idx int) (nextIdx int, cont, viaJoin bool) {
	nextIdx, cont, viaJoin, _, _ = advanceToNextFromItemWithOnSpan(src, nonTrivia, idx, len(nonTrivia), false)

	return nextIdx, cont, viaJoin
}

func advanceToNextFromItemWithOnSpan(
	src string,
	nonTrivia []syntax.Token,
	idx, limit int,
	afterJoin bool,
) (nextIdx int, cont, viaJoin bool, onSpan syntax.ByteSpan, hasOn bool) {
	onStart := -1
	for idx < limit {
		tok := nonTrivia[idx]
		if next, handled, shouldCont, done := stepFromItemAdvancePunctOrOp(src, nonTrivia, idx, tok); handled {
			if done {
				onSpan, hasOn = finalizeOnSpan(onStart, tok.Start)
				return next, shouldCont, false, onSpan, hasOn
			}
			idx = next
			continue
		}

		if tok.Kind == syntax.TokenIdent {
			upper := strings.ToUpper(tok.Text(src))
			if upper == "ON" && afterJoin && onStart < 0 {
				onStart = tok.End
			} else if upper == "JOIN" {
				onSpan, hasOn = finalizeOnSpan(onStart, tok.Start)
				return idx + 1, true, true, onSpan, hasOn
			} else if _, stop := fromTerminatorKeywords[upper]; stop {
				onSpan, hasOn = finalizeOnSpan(onStart, tok.Start)
				return idx, false, false, onSpan, hasOn
			}
		}

		idx++
	}

	endByte := len(src)
	if limit < len(nonTrivia) {
		endByte = nonTrivia[limit].Start
	}
	onSpan, hasOn = finalizeOnSpan(onStart, endByte)

	return idx, false, false, onSpan, hasOn
}

func stepFromItemAdvancePunctOrOp(
	src string,
	nonTrivia []syntax.Token,
	idx int,
	tok syntax.Token,
) (nextIdx int, handled, cont, done bool) {
	txt := tok.Text(src)
	if tok.Kind == syntax.TokenOperator && txt == "|>" {
		return idx, true, false, true
	}
	if tok.Kind != syntax.TokenPunct {
		return idx, false, false, false
	}

	switch txt {
	case "(":
		return skipBalancedDelims(src, nonTrivia, idx, "(", ")"), true, false, false
	case "[":
		return skipBalancedDelims(src, nonTrivia, idx, "[", "]"), true, false, false
	case ",":
		return idx + 1, true, true, true
	case ")", "]", ";":
		return idx, true, false, true
	default:
		return idx, false, false, false
	}
}

func finalizeOnSpan(onStart, endByte int) (syntax.ByteSpan, bool) {
	if onStart >= 0 && onStart < endByte {
		return syntax.ByteSpan{Start: onStart, End: endByte}, true
	}

	return syntax.ByteSpan{}, false
}

func skipTableFactorModifiers(src string, nonTrivia []syntax.Token, idx int) int {
	for idx < len(nonTrivia) {
		if next := skipHintIfPresent(src, nonTrivia, idx); next != idx {
			idx = next
			continue
		}
		if next := skipForSystemTimeAsOf(src, nonTrivia, idx); next != idx {
			idx = next
			continue
		}
		if next := skipTableSampleClause(src, nonTrivia, idx); next != idx {
			idx = next
			continue
		}

		break
	}

	return idx
}

func skipTableSampleClause(src string, nonTrivia []syntax.Token, idx int) int {
	if !isKeywordAt(src, nonTrivia, idx, "TABLESAMPLE") {
		return idx
	}

	idx++
	if idx < len(nonTrivia) && isIdentOrBacktick(nonTrivia[idx]) {
		idx++
	}
	if isPunctAt(src, nonTrivia, idx, "(") {
		idx = skipBalancedParens(src, nonTrivia, idx)
	}
	if isKeywordAt(src, nonTrivia, idx, "REPEATABLE") && isPunctAt(src, nonTrivia, idx+1, "(") {
		idx = skipBalancedParens(src, nonTrivia, idx+1)
	}

	return idx
}

func skipForSystemTimeAsOf(src string, nonTrivia []syntax.Token, idx int) int {
	if !isKeywordAt(src, nonTrivia, idx, "FOR") ||
		!isKeywordAt(src, nonTrivia, idx+1, "SYSTEM_TIME") ||
		!isKeywordAt(src, nonTrivia, idx+2, "AS") ||
		!isKeywordAt(src, nonTrivia, idx+3, "OF") {
		return idx
	}

	return skipAsOfExpr(src, nonTrivia, idx+4)
}

func skipAsOfExpr(src string, nonTrivia []syntax.Token, idx int) int {
	next := skipAsOfExprPrimary(src, nonTrivia, idx)
	if next == idx {
		return idx
	}

	idx = next
	for idx < len(nonTrivia) {
		if isPunctAt(src, nonTrivia, idx, "[") {
			idx = skipBalancedDelims(src, nonTrivia, idx, "[", "]")
			continue
		}
		if isPunctAt(src, nonTrivia, idx, ".") && idx+1 < len(nonTrivia) && isIdentOrBacktick(nonTrivia[idx+1]) {
			idx += 2
			if isPunctAt(src, nonTrivia, idx, "(") {
				idx = skipBalancedParens(src, nonTrivia, idx)
			}
			continue
		}
		if isAsOfBinaryOp(src, nonTrivia, idx) {
			nextPrim := skipAsOfExprPrimary(src, nonTrivia, idx+1)
			if nextPrim == idx+1 {
				break
			}
			idx = nextPrim
			continue
		}

		break
	}

	return idx
}

func skipAsOfExprPrimary(src string, nonTrivia []syntax.Token, idx int) int {
	if idx >= len(nonTrivia) {
		return idx
	}

	start := idx
	if nonTrivia[idx].Kind == syntax.TokenOperator && (nonTrivia[idx].Text(src) == "+" || nonTrivia[idx].Text(src) == "-") {
		idx++
	}
	if idx >= len(nonTrivia) {
		return start
	}

	tok := nonTrivia[idx]
	if tok.Kind == syntax.TokenPunct && tok.Text(src) == "(" {
		return skipBalancedParens(src, nonTrivia, idx)
	}

	switch tok.Kind {
	case syntax.TokenString, syntax.TokenBytes, syntax.TokenNumber, syntax.TokenParam:
		return idx + 1
	case syntax.TokenSysVar:
		return skipSysVarChain(src, nonTrivia, idx)
	case syntax.TokenIdent, syntax.TokenBacktickIdent:
		if isAsOfClauseStopKeyword(src, tok) {
			return start
		}
		if isKeywordAt(src, nonTrivia, idx, "INTERVAL") {
			return skipAsOfIntervalLiteral(src, nonTrivia, idx)
		}
		if tok.Kind == syntax.TokenIdent && idx+1 < len(nonTrivia) && nonTrivia[idx+1].Kind == syntax.TokenString {
			return idx + 2
		}

		return skipAsOfPathOrCall(src, nonTrivia, idx)
	default:
		return start
	}
}

func skipSysVarChain(src string, nonTrivia []syntax.Token, idx int) int {
	idx++
	for idx+1 < len(nonTrivia) && isPunctAt(src, nonTrivia, idx, ".") && isIdentOrBacktick(nonTrivia[idx+1]) {
		idx += 2
	}

	return idx
}

func skipAsOfIntervalLiteral(src string, nonTrivia []syntax.Token, idx int) int {
	idx++
	if idx >= len(nonTrivia) {
		return idx
	}
	if isPunctAt(src, nonTrivia, idx, "(") {
		idx = skipBalancedParens(src, nonTrivia, idx)
	} else {
		idx++
	}
	if idx < len(nonTrivia) && isIdentOrBacktick(nonTrivia[idx]) {
		idx++
	}
	if isKeywordAt(src, nonTrivia, idx, "TO") && idx+1 < len(nonTrivia) && isIdentOrBacktick(nonTrivia[idx+1]) {
		idx += 2
	}

	return idx
}

func skipAsOfPathOrCall(src string, nonTrivia []syntax.Token, idx int) int {
	idx++
	for idx+1 < len(nonTrivia) && isPunctAt(src, nonTrivia, idx, ".") && isIdentOrBacktick(nonTrivia[idx+1]) {
		idx += 2
	}
	if isPunctAt(src, nonTrivia, idx, "(") {
		idx = skipBalancedParens(src, nonTrivia, idx)
	}

	return idx
}

func isAsOfBinaryOp(src string, nonTrivia []syntax.Token, idx int) bool {
	if idx >= len(nonTrivia) || nonTrivia[idx].Kind != syntax.TokenOperator {
		return false
	}

	switch nonTrivia[idx].Text(src) {
	case "+", "-", "*", "/", "||":
		return true
	default:
		return false
	}
}

func isAsOfClauseStopKeyword(src string, tok syntax.Token) bool {
	if tok.Kind != syntax.TokenIdent {
		return false
	}

	upper := strings.ToUpper(tok.Text(src))
	if _, stop := fromTerminatorKeywords[upper]; stop {
		return true
	}

	switch upper {
	case "USING", "ON", "JOIN", "AS", "TABLESAMPLE", "PIVOT", "UNPIVOT", "MATCH_RECOGNIZE":
		return true
	default:
		return false
	}
}

func skipBalancedParens(src string, nonTrivia []syntax.Token, openIdx int) int {
	return skipBalancedDelims(src, nonTrivia, openIdx, "(", ")")
}

func skipBalancedDelims(src string, nonTrivia []syntax.Token, openIdx int, openDelim, closeDelim string) int {
	if openIdx >= len(nonTrivia) || nonTrivia[openIdx].Kind != syntax.TokenPunct || nonTrivia[openIdx].Text(src) != openDelim {
		return openIdx
	}

	depth := 0
	for i := openIdx; i < len(nonTrivia); i++ {
		if nonTrivia[i].Kind != syntax.TokenPunct {
			continue
		}
		switch nonTrivia[i].Text(src) {
		case openDelim:
			depth++
		case closeDelim:
			depth--
			if depth == 0 {
				return i + 1
			}
		default:
		}
	}

	return len(nonTrivia)
}
