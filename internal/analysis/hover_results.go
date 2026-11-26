package analysis

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/goccy/go-googlesql"

	"github.com/paulourio/bqlang/catalog"
	"github.com/paulourio/bqlang/internal/funcdocs"
)

const (
	maxExprSnippetLines  = 6
	maxExprSnippetChars  = 320
	maxDisplayedDeps     = 12
	maxTransitiveSources = 4
	maxLineageDepth      = 32
)

var simpleBacktickTypeRe = regexp.MustCompile("^`([A-Z][A-Z0-9_]+)`$")

type computedKind int

const (
	computedKindExpr computedKind = iota
	computedKindAggregate
	computedKindWindow
)

type computedColInfo struct {
	cc       *googlesql.ResolvedComputedColumn
	kind     computedKind
	winGroup *googlesql.ResolvedAnalyticFunctionGroup
	aggScan  *googlesql.ResolvedAggregateScan
}

type setOpColInfo struct {
	opLabel   string
	branchIDs []int32
}

type resolvedLineage struct {
	origins     map[int32]colOrigin
	computed    map[int32]*computedColInfo
	setOpCols   map[int32]*setOpColInfo
	colLinks    map[int32][]int32
	cteOwner    map[int32]string
	withEntries map[string]googlesql.ResolvedScanNode
}

type aliasTargetKind int

const (
	aliasTargetSelectColumn aliasTargetKind = iota
	aliasTargetGroupingItem
	aliasTargetPipeRenameNew
	aliasTargetPipeRenameOld
	aliasTargetPipeSet
	aliasTargetStructField
	aliasTargetCTE
)

type aliasTarget struct {
	kind       aliasTargetKind
	name       string
	oldName    string
	aliasStart int
	aliasEnd   int
	exprNode   googlesql.ASTNode
	exprStart  int
	exprEnd    int
	roleLabel  string
	selectIdx  int
	selectNode *googlesql.ASTSelect
}

type depKind int

const (
	depColumn depKind = iota
	depDerived
	depWindowColumn
	depParam
	depVariable
	depSysVar
	depTable
)

type exprDependency struct {
	kind       depKind
	label      string
	sqlType    string
	badges     string
	viaSources []string
}

func buildResolvedLineage(rs googlesql.ResolvedNode, origins map[int32]colOrigin) *resolvedLineage {
	lin := &resolvedLineage{
		origins:     origins,
		computed:    make(map[int32]*computedColInfo),
		setOpCols:   make(map[int32]*setOpColInfo),
		colLinks:    make(map[int32][]int32),
		cteOwner:    make(map[int32]string),
		withEntries: make(map[string]googlesql.ResolvedScanNode),
	}

	var withRefs []*googlesql.ResolvedWithRefScan

	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		switch node := n.(type) {
		case *googlesql.ResolvedProjectScan:
			exprs, _ := node.ExprList()
			recordComputedCols(lin, exprs, computedKindExpr, nil, nil)
		case *googlesql.ResolvedAggregateScan:
			groups, _ := node.GroupByList()
			recordComputedCols(lin, groups, computedKindExpr, nil, nil)
			aggs, _ := node.AggregateList()
			recordComputedBaseCols(lin, aggs, computedKindAggregate, nil, node)
		case *googlesql.ResolvedAnalyticScan:
			groups, _ := node.FunctionGroupList()
			for _, grp := range groups {
				if isNilNode(grp) {
					continue
				}
				fns, _ := grp.AnalyticFunctionList()
				recordComputedBaseCols(lin, fns, computedKindWindow, grp, nil)
			}
		case *googlesql.ResolvedWithEntry:
			recordWithEntryLineage(lin, node)
		case *googlesql.ResolvedWithRefScan:
			withRefs = append(withRefs, node)
		case *googlesql.ResolvedSetOperationScan:
			recordSetOpLineage(lin, node)
		default:
		}

		return true
	})

	for _, ref := range withRefs {
		recordWithRefLineage(lin, ref)
	}

	return lin
}

func recordComputedCols(
	lin *resolvedLineage,
	cols []*googlesql.ResolvedComputedColumn,
	kind computedKind,
	winGroup *googlesql.ResolvedAnalyticFunctionGroup,
	aggScan *googlesql.ResolvedAggregateScan,
) {
	for _, cc := range cols {
		if isNilNode(cc) {
			continue
		}
		if id, ok := resolvedComputedColID(cc); ok {
			lin.computed[id] = &computedColInfo{
				cc:       cc,
				kind:     kind,
				winGroup: winGroup,
				aggScan:  aggScan,
			}
		}
	}
}

func recordComputedBaseCols(
	lin *resolvedLineage,
	cols []googlesql.ResolvedComputedColumnBaseNode,
	kind computedKind,
	winGroup *googlesql.ResolvedAnalyticFunctionGroup,
	aggScan *googlesql.ResolvedAggregateScan,
) {
	for _, base := range cols {
		cc, ok := base.(*googlesql.ResolvedComputedColumn)
		if !ok || isNilNode(cc) {
			continue
		}
		if id, okID := resolvedComputedColID(cc); okID {
			lin.computed[id] = &computedColInfo{
				cc:       cc,
				kind:     kind,
				winGroup: winGroup,
				aggScan:  aggScan,
			}
		}
	}
}

func recordWithEntryLineage(lin *resolvedLineage, entry *googlesql.ResolvedWithEntry) {
	qname, _ := entry.WithQueryName()
	sub, _ := entry.WithSubquery()
	if qname == "" || isNilNode(sub) {
		return
	}

	lin.withEntries[strings.ToLower(qname)] = sub
	for _, c := range scanColumnList(sub) {
		if isNilNode(c) {
			continue
		}
		if id, err := c.ColumnId(); err == nil {
			lin.cteOwner[id] = qname
		}
	}
}

func recordWithRefLineage(lin *resolvedLineage, ref *googlesql.ResolvedWithRefScan) {
	qname, _ := ref.WithQueryName()
	sub, ok := lin.withEntries[strings.ToLower(qname)]
	if !ok || isNilNode(sub) {
		return
	}

	refCols, _ := ref.ColumnList()
	srcCols := scanColumnList(sub)
	for i := 0; i < len(refCols) && i < len(srcCols); i++ {
		if isNilNode(refCols[i]) || isNilNode(srcCols[i]) {
			continue
		}
		refID, err1 := refCols[i].ColumnId()
		srcID, err2 := srcCols[i].ColumnId()
		if err1 == nil && err2 == nil {
			lin.cteOwner[refID] = qname
			lin.colLinks[refID] = append(lin.colLinks[refID], srcID)
		}
	}
}

func recordSetOpLineage(lin *resolvedLineage, scan *googlesql.ResolvedSetOperationScan) {
	outCols, _ := scan.ColumnList()
	items, _ := scan.InputItemList()
	opLabel := formatSetOpType(scan)

	for _, item := range items {
		if isNilNode(item) {
			continue
		}
		inCols, _ := item.OutputColumnList()
		for i := 0; i < len(outCols) && i < len(inCols); i++ {
			if isNilNode(outCols[i]) || isNilNode(inCols[i]) {
				continue
			}
			outID, err1 := outCols[i].ColumnId()
			inID, err2 := inCols[i].ColumnId()
			if err1 == nil && err2 == nil {
				lin.colLinks[outID] = append(lin.colLinks[outID], inID)
				so := lin.setOpCols[outID]
				if so == nil {
					so = &setOpColInfo{opLabel: opLabel}
					lin.setOpCols[outID] = so
				}
				so.branchIDs = append(so.branchIDs, inID)
			}
		}
	}
}

func formatSetOpType(scan *googlesql.ResolvedSetOperationScan) string {
	op, err := scan.OpType()
	if err != nil {
		return "UNION"
	}

	switch op {
	case googlesql.ResolvedSetOperationScanEnums_SetOperationTypeUnionAll:
		return "UNION ALL"
	case googlesql.ResolvedSetOperationScanEnums_SetOperationTypeUnionDistinct:
		return "UNION DISTINCT"
	case googlesql.ResolvedSetOperationScanEnums_SetOperationTypeIntersectAll:
		return "INTERSECT ALL"
	case googlesql.ResolvedSetOperationScanEnums_SetOperationTypeIntersectDistinct:
		return "INTERSECT DISTINCT"
	case googlesql.ResolvedSetOperationScanEnums_SetOperationTypeExceptAll:
		return "EXCEPT ALL"
	case googlesql.ResolvedSetOperationScanEnums_SetOperationTypeExceptDistinct:
		return "EXCEPT DISTINCT"
	default:
		return "UNION"
	}
}

func (lin *resolvedLineage) resolveSetOp(colID int32) (*setOpColInfo, bool) {
	return lin.resolveSetOpDepth(colID, 0, make(map[int32]bool))
}

func (lin *resolvedLineage) resolveSetOpDepth(colID int32, depth int, seen map[int32]bool) (*setOpColInfo, bool) {
	if depth > maxLineageDepth || seen[colID] {
		return nil, false
	}
	seen[colID] = true

	if so, ok := lin.setOpCols[colID]; ok {
		return so, true
	}

	for _, nextID := range lin.colLinks[colID] {
		if so, ok := lin.resolveSetOpDepth(nextID, depth+1, seen); ok {
			return so, true
		}
	}

	return nil, false
}

func (lin *resolvedLineage) resolveComputed(colID int32) (*computedColInfo, bool) {
	return lin.resolveComputedDepth(colID, 0, make(map[int32]bool))
}

func (lin *resolvedLineage) resolveComputedDepth(colID int32, depth int, seen map[int32]bool) (*computedColInfo, bool) {
	if depth > maxLineageDepth || seen[colID] {
		return nil, false
	}
	seen[colID] = true

	if info, ok := lin.computed[colID]; ok {
		return info, true
	}

	for _, nextID := range lin.colLinks[colID] {
		if info, ok := lin.resolveComputedDepth(nextID, depth+1, seen); ok {
			return info, true
		}
	}

	return nil, false
}

func findAliasTargetAtOffset(root googlesql.ASTNode, off int) (aliasTarget, bool) {
	if isNilNode(root) {
		return aliasTarget{}, false
	}

	var best aliasTarget
	bestLen := int(^uint(0) >> 1)
	found := false

	walkASTNodes(root, func(n googlesql.ASTNode) bool {
		cand, ok := aliasTargetFromASTNode(n, off)
		if !ok {
			return true
		}

		spanLen := cand.aliasEnd - cand.aliasStart
		if !found || spanLen < bestLen {
			best = cand
			bestLen = spanLen
			found = true
		}

		return true
	})

	return best, found
}

func aliasTargetFromASTNode(n googlesql.ASTNode, off int) (aliasTarget, bool) {
	switch node := n.(type) {
	case *googlesql.ASTSelectColumn:
		return aliasTargetFromSelectCol(node, off)
	case *googlesql.ASTGroupingItem:
		return aliasTargetFromGroupingItem(node, off)
	case *googlesql.ASTPipeRenameItem:
		return aliasTargetFromPipeRename(node, off)
	case *googlesql.ASTPipeSetItem:
		return aliasTargetFromPipeSet(node, off)
	case *googlesql.ASTStructConstructorArg:
		return aliasTargetFromStructArg(node, off)
	case *googlesql.ASTAliasedQuery:
		return aliasTargetFromCTE(node, off)
	default:
		return aliasTarget{}, false
	}
}

func aliasTargetFromSelectCol(sc *googlesql.ASTSelectColumn, off int) (aliasTarget, bool) {
	al, _ := sc.Alias()
	id, name, s, e, ok := aliasIdentAtOffset(al, off)
	if !ok || isNilNode(id) {
		return aliasTarget{}, false
	}

	expr, _ := sc.Expression()
	es, ee, _ := astNodeByteRange(expr)
	idx, sel := selectColumnPosition(sc)

	return aliasTarget{
		kind:       aliasTargetSelectColumn,
		name:       name,
		aliasStart: s,
		aliasEnd:   e,
		exprNode:   expr,
		exprStart:  es,
		exprEnd:    ee,
		roleLabel:  enclosingColumnRole(sc),
		selectIdx:  idx,
		selectNode: sel,
	}, true
}

func aliasTargetFromGroupingItem(gi *googlesql.ASTGroupingItem, off int) (aliasTarget, bool) {
	al, _ := gi.Alias()
	id, name, s, e, ok := aliasIdentAtOffset(al, off)
	if !ok || isNilNode(id) {
		return aliasTarget{}, false
	}

	expr, _ := gi.Expression()
	es, ee, _ := astNodeByteRange(expr)

	return aliasTarget{
		kind:       aliasTargetGroupingItem,
		name:       name,
		aliasStart: s,
		aliasEnd:   e,
		exprNode:   expr,
		exprStart:  es,
		exprEnd:    ee,
		roleLabel:  "group key",
		selectIdx:  -1,
	}, true
}

func aliasTargetFromPipeRename(ri *googlesql.ASTPipeRenameItem, off int) (aliasTarget, bool) {
	oldID, _ := ri.OldName()
	newID, _ := ri.NewName()
	oldName := identText(oldID)
	newName := identText(newID)

	if s, e, ok := astNodeByteRange(newID); ok && off >= s && off < e && newName != "" {
		return aliasTarget{
			kind:       aliasTargetPipeRenameNew,
			name:       newName,
			oldName:    oldName,
			aliasStart: s,
			aliasEnd:   e,
			roleLabel:  enclosingColumnRole(ri),
			selectIdx:  -1,
		}, true
	}

	if s, e, ok := astNodeByteRange(oldID); ok && off >= s && off < e && oldName != "" {
		return aliasTarget{
			kind:       aliasTargetPipeRenameOld,
			name:       oldName,
			oldName:    oldName,
			aliasStart: s,
			aliasEnd:   e,
			roleLabel:  enclosingColumnRole(ri),
			selectIdx:  -1,
		}, true
	}

	return aliasTarget{}, false
}

func aliasTargetFromPipeSet(si *googlesql.ASTPipeSetItem, off int) (aliasTarget, bool) {
	colID, _ := si.Column()
	s, e, ok := astNodeByteRange(colID)
	if !ok || off < s || off >= e {
		return aliasTarget{}, false
	}

	name := identText(colID)
	if name == "" {
		return aliasTarget{}, false
	}

	expr, _ := si.Expression()
	es, ee, _ := astNodeByteRange(expr)

	return aliasTarget{
		kind:       aliasTargetPipeSet,
		name:       name,
		aliasStart: s,
		aliasEnd:   e,
		exprNode:   expr,
		exprStart:  es,
		exprEnd:    ee,
		roleLabel:  enclosingColumnRole(si),
		selectIdx:  -1,
	}, true
}

func aliasTargetFromStructArg(arg *googlesql.ASTStructConstructorArg, off int) (aliasTarget, bool) {
	al, _ := arg.Alias()
	id, name, s, e, ok := aliasIdentAtOffset(al, off)
	if !ok || isNilNode(id) {
		return aliasTarget{}, false
	}

	expr, _ := arg.Expression()
	es, ee, _ := astNodeByteRange(expr)

	return aliasTarget{
		kind:       aliasTargetStructField,
		name:       name,
		aliasStart: s,
		aliasEnd:   e,
		exprNode:   expr,
		exprStart:  es,
		exprEnd:    ee,
		roleLabel:  "struct field",
		selectIdx:  -1,
	}, true
}

func aliasTargetFromCTE(aq *googlesql.ASTAliasedQuery, off int) (aliasTarget, bool) {
	al, _ := aq.Alias()
	if isNilNode(al) {
		return aliasTarget{}, false
	}

	s, e, ok := astNodeByteRange(al)
	if !ok || off < s || off >= e {
		return aliasTarget{}, false
	}

	name := identText(al)
	if name == "" {
		return aliasTarget{}, false
	}

	return aliasTarget{
		kind:       aliasTargetCTE,
		name:       name,
		aliasStart: s,
		aliasEnd:   e,
		selectIdx:  -1,
	}, true
}

func aliasIdentAtOffset(al *googlesql.ASTAlias, off int) (id *googlesql.ASTIdentifier, name string, start, end int, ok bool) {
	if isNilNode(al) {
		return nil, "", 0, 0, false
	}

	id, _ = al.Identifier()
	if isNilNode(id) {
		return nil, "", 0, 0, false
	}

	start, end, hasLoc := astNodeByteRange(id)
	if !hasLoc || off < start || off >= end {
		return nil, "", 0, 0, false
	}

	name = identText(id)
	if name == "" {
		return nil, "", 0, 0, false
	}

	return id, name, start, end, true
}

func identText(id *googlesql.ASTIdentifier) string {
	if isNilNode(id) {
		return ""
	}
	name, _ := id.GetAsString()

	return name
}

func selectColumnPosition(sc *googlesql.ASTSelectColumn) (int, *googlesql.ASTSelect) {
	parent := astParent(sc)
	sl, ok := parent.(*googlesql.ASTSelectList)
	if !ok || isNilNode(sl) {
		return -1, nil
	}

	sel, _ := astParent(sl).(*googlesql.ASTSelect)
	idx := -1
	for i := range astNumChildren(sl) {
		ch, isSC := astChild(sl, i).(*googlesql.ASTSelectColumn)
		if !isSC || isNilNode(ch) {
			continue
		}
		if isSameASTNode(ch, sc) {
			idx = i
			break
		}
		ex, _ := ch.Expression()
		if isStarExpr(ex) {
			return -1, sel
		}
	}

	return idx, sel
}

func isStarExpr(ex googlesql.ASTNode) bool {
	if isNilNode(ex) {
		return false
	}
	switch ex.(type) {
	case *googlesql.ASTStar, *googlesql.ASTDotStar, *googlesql.ASTStarWithModifiers:
		return true
	default:
		return false
	}
}

func enclosingColumnRole(n googlesql.ASTNode) string {
	for cur := astParent(n); !isNilNode(cur); cur = astParent(cur) {
		switch node := cur.(type) {
		case *googlesql.ASTAliasedQuery:
			al, _ := node.Alias()
			if name := identText(al); name != "" {
				return fmt.Sprintf("CTE `%s`", name)
			}
			return "CTE column"
		case *googlesql.ASTTableSubquery:
			if al, _ := node.Alias(); !isNilNode(al) {
				if id, _ := al.Identifier(); !isNilNode(id) {
					if name := identText(id); name != "" {
						return fmt.Sprintf("subquery `%s`", name)
					}
				}
			}
			return "subquery column"
		case *googlesql.ASTExpressionSubquery:
			return "subquery column"
		default:
		}
	}

	return "output column"
}

func hoverResultAliasFromResolvedAST(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	rs googlesql.ResolvedStatementNode,
	lineage *resolvedLineage,
	cat *catalog.Catalog,
	localOff int,
) *Hover {
	if isNilNode(st.ASTStmt) {
		return nil
	}

	target, ok := findAliasTargetAtOffset(st.ASTStmt, localOff)
	if !ok {
		return nil
	}

	switch target.kind {
	case aliasTargetCTE:
		return hoverCTEDefinition(snap, st, lineage, target)
	case aliasTargetStructField:
		return hoverStructFieldAlias(snap, st, rs, lineage, target)
	case aliasTargetPipeRenameNew, aliasTargetPipeRenameOld:
		return hoverPipeRenameAlias(snap, st, rs, lineage, target)
	case aliasTargetSelectColumn, aliasTargetGroupingItem, aliasTargetPipeSet:
		return hoverComputedOrSelectAlias(snap, st, rs, lineage, cat, target)
	default:
		return nil
	}
}

func hoverCTEDefinition(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	lineage *resolvedLineage,
	target aliasTarget,
) *Hover {
	sub, ok := lineage.withEntries[strings.ToLower(target.name)]
	if !ok || isNilNode(sub) {
		return nil
	}

	return renderCTEHoverCard(snap, st, target.name, scanColumnList(sub), target.aliasStart, target.aliasEnd)
}

func renderCTEHoverCard(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	qname string,
	cols []*googlesql.ResolvedColumn,
	s, e int,
) *Hover {
	var b strings.Builder
	fmt.Fprintf(&b, "### CTE `%s`\n", qname)
	if len(cols) > 0 {
		b.WriteString("\n| Column | Type |\n|---|---|\n")
		for _, c := range cols {
			if isNilNode(c) {
				continue
			}
			cn, _ := c.Name()
			ct, _ := c.Type()
			fmt.Fprintf(&b, "| `%s` | `%s` |\n", cn, formatGoogleSQLType(ct))
		}
	}

	return makeHoverAtLocalSpan(snap, st, b.String(), s, e)
}

func hoverComputedOrSelectAlias(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	rs googlesql.ResolvedStatementNode,
	lineage *resolvedLineage,
	cat *catalog.Catalog,
	target aliasTarget,
) *Hover {
	exprSQL := formatExprSnippet(st.SQL, target.exprStart, target.exprEnd)

	if info, matched := matchComputedColForTarget(lineage, target); matched {
		return renderComputedColHover(snap, st, lineage, cat, target.name, target.roleLabel, exprSQL, info, target.aliasStart, target.aliasEnd)
	}

	if exprNode, matched := findOutermostResolvedExprInSpan(rs, target.exprStart, target.exprEnd); matched {
		return renderResolvedExprAliasHover(snap, st, lineage, cat, target, exprSQL, exprNode)
	}

	if rc, matched := resolvePassthroughColumn(rs, target); matched {
		return renderResolvedColumnAsAlias(snap, st, lineage, cat, target.name, target.roleLabel, exprSQL, rc, target.aliasStart, target.aliasEnd)
	}

	return nil
}

func hoverStructFieldAlias(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	rs googlesql.ResolvedStatementNode,
	lineage *resolvedLineage,
	target aliasTarget,
) *Hover {
	exprNode, matched := findOutermostResolvedExprInSpan(rs, target.exprStart, target.exprEnd)
	if !matched {
		return nil
	}

	exprSQL := formatExprSnippet(st.SQL, target.exprStart, target.exprEnd)

	return renderResolvedExprAliasHover(snap, st, lineage, nil, target, exprSQL, exprNode)
}

func hoverPipeRenameAlias(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	rs googlesql.ResolvedStatementNode,
	lineage *resolvedLineage,
	target aliasTarget,
) *Hover {
	rc, matched := findOutputOrScanColumnByName(rs, target.oldName)
	if !matched && target.name != "" && !strings.EqualFold(target.name, target.oldName) {
		rc, matched = findOutputOrScanColumnByName(rs, target.name)
	}
	if !matched {
		return nil
	}

	if target.kind == aliasTargetPipeRenameOld {
		colID, _ := rc.ColumnId()
		if orig, ok := lineage.origins[colID]; ok && orig.meta != nil {
			if h := makeOriginColumnHover(snap, st, orig, target.aliasStart, target.aliasEnd); h != nil {
				return h
			}
		}
		typ, _ := rc.Type()

		return makeSimpleTypeHover(snap, st, target.name, formatGoogleSQLType(typ), target.aliasStart, target.aliasEnd)
	}

	return renderResolvedColumnAsAlias(snap, st, lineage, nil, target.name, target.roleLabel, target.oldName, rc, target.aliasStart, target.aliasEnd)
}

type computedMatchScore struct {
	info         *computedColInfo
	nameMatch    bool
	nonSynthetic bool
	spanLen      int
	startDist    int
}

func matchComputedColForTarget(lineage *resolvedLineage, target aliasTarget) (*computedColInfo, bool) {
	if target.exprEnd <= target.exprStart {
		return nil, false
	}

	var best computedMatchScore
	hasBest := false

	for _, info := range lineage.computed {
		if info == nil || isNilNode(info.cc) {
			continue
		}
		expr, _ := info.cc.Expr()
		cs, ce, ok := resolvedSubtreeByteRange(expr)
		if !ok || cs < target.exprStart || ce > target.exprEnd {
			continue
		}

		col, _ := info.cc.Column()
		colName := ""
		if !isNilNode(col) {
			colName, _ = col.Name()
		}

		cand := computedMatchScore{
			info:         info,
			nameMatch:    strings.EqualFold(colName, target.name),
			nonSynthetic: colName != "" && !strings.HasPrefix(colName, "$"),
			spanLen:      ce - cs,
			startDist:    cs - target.exprStart,
		}

		if !hasBest || isBetterComputedMatch(cand, best) {
			best = cand
			hasBest = true
		}
	}

	return best.info, hasBest
}

func isBetterComputedMatch(a, b computedMatchScore) bool {
	if a.nonSynthetic != b.nonSynthetic {
		return a.nonSynthetic
	}
	if a.nameMatch != b.nameMatch {
		return a.nameMatch
	}
	if a.spanLen != b.spanLen {
		return a.spanLen > b.spanLen
	}

	return a.startDist < b.startDist
}

func resolvedSubtreeByteRange(n googlesql.ResolvedNode) (start, end int, ok bool) {
	if s, e, has := resolvedNodeByteRange(n); has && e > s {
		return s, e, true
	}

	minS := int(^uint(0) >> 1)
	maxE := -1
	walkResolvedNodes(n, func(cur googlesql.ResolvedNode) bool {
		if s, e, has := resolvedNodeByteRange(cur); has && e > s {
			if s < minS {
				minS = s
			}
			if e > maxE {
				maxE = e
			}
		}

		return true
	})

	if maxE > minS {
		return minS, maxE, true
	}

	return 0, 0, false
}

func findOutermostResolvedExprInSpan(rs googlesql.ResolvedNode, exprStart, exprEnd int) (googlesql.ResolvedExprNode, bool) {
	if exprEnd <= exprStart {
		return nil, false
	}

	var best googlesql.ResolvedExprNode
	bestLen := -1

	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		expr, ok := n.(googlesql.ResolvedExprNode)
		if !ok || isNilNode(expr) {
			return true
		}
		s, e, has := resolvedSubtreeByteRange(expr)
		if !has || e <= s || s < exprStart || e > exprEnd {
			return true
		}
		if e-s > bestLen {
			best = expr
			bestLen = e - s
		}

		// Do not descend into children of a matching outermost expression.
		return false
	})

	return best, !isNilNode(best)
}

func resolvePassthroughColumn(rs googlesql.ResolvedStatementNode, target aliasTarget) (*googlesql.ResolvedColumn, bool) {
	if target.roleLabel == "output column" {
		if col, ok := matchStatementOutputColumn(statementOutputColumns(rs), target.selectIdx, target.name); ok {
			return col, true
		}
	}

	if !isNilNode(target.selectNode) {
		if selS, selE, hasSel := astNodeByteRange(target.selectNode); hasSel {
			return findScanColInASTSelect(rs, selS, selE, target.selectIdx, target.name)
		}
	}

	return nil, false
}

func matchStatementOutputColumn(outCols []*googlesql.ResolvedOutputColumn, selectIdx int, name string) (*googlesql.ResolvedColumn, bool) {
	if selectIdx >= 0 && selectIdx < len(outCols) {
		if col, ok := outputColumnIfMatches(outCols[selectIdx], name); ok {
			return col, true
		}
	}

	for _, oc := range outCols {
		if col, ok := outputColumnIfMatches(oc, name); ok {
			return col, true
		}
	}

	return nil, false
}

func outputColumnIfMatches(oc *googlesql.ResolvedOutputColumn, name string) (*googlesql.ResolvedColumn, bool) {
	if isNilNode(oc) {
		return nil, false
	}
	if nm, _ := oc.Name(); !strings.EqualFold(nm, name) {
		return nil, false
	}
	col, _ := oc.Column()

	return col, !isNilNode(col)
}

func statementOutputColumns(rs googlesql.ResolvedStatementNode) []*googlesql.ResolvedOutputColumn {
	if outNode, ok := rs.(interface {
		OutputColumnList() ([]*googlesql.ResolvedOutputColumn, error)
	}); ok {
		cols, _ := outNode.OutputColumnList()
		return cols
	}

	return nil
}

func findScanColInASTSelect(
	rs googlesql.ResolvedStatementNode,
	selS, selE, selectIdx int,
	name string,
) (*googlesql.ResolvedColumn, bool) {
	var bestScan googlesql.ResolvedScanNode
	bestLen := int(^uint(0) >> 1)

	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		scan, ok := n.(googlesql.ResolvedScanNode)
		if !ok || isNilNode(scan) {
			return true
		}
		s, e, has := resolvedNodeByteRange(scan)
		if !has || s > selS || e < selE {
			return true
		}
		if e-s < bestLen {
			bestScan = scan
			bestLen = e - s
		}

		return true
	})

	if isNilNode(bestScan) {
		return nil, false
	}

	cols := scanColumnList(bestScan)
	if selectIdx >= 0 && selectIdx < len(cols) && !isNilNode(cols[selectIdx]) {
		return cols[selectIdx], true
	}

	for _, c := range cols {
		if isNilNode(c) {
			continue
		}
		if nm, _ := c.Name(); strings.EqualFold(nm, name) {
			return c, true
		}
	}

	return nil, false
}

func findOutputOrScanColumnByName(rs googlesql.ResolvedStatementNode, name string) (*googlesql.ResolvedColumn, bool) {
	if col, ok := matchStatementOutputColumn(statementOutputColumns(rs), -1, name); ok {
		return col, true
	}

	var matched *googlesql.ResolvedColumn
	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		if !isNilNode(matched) {
			return false
		}
		scan, ok := n.(googlesql.ResolvedScanNode)
		if !ok || isNilNode(scan) {
			return true
		}
		if col, found := findColumnByNameInSlice(scanColumnList(scan), name); found {
			matched = col
			return false
		}

		return true
	})

	return matched, !isNilNode(matched)
}

func findColumnByNameInSlice(cols []*googlesql.ResolvedColumn, name string) (*googlesql.ResolvedColumn, bool) {
	for _, c := range cols {
		if isNilNode(c) {
			continue
		}
		if nm, _ := c.Name(); strings.EqualFold(nm, name) {
			return c, true
		}
	}

	return nil, false
}

func renderComputedColHover(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	lineage *resolvedLineage,
	cat *catalog.Catalog,
	name, roleLabel, exprSQL string,
	info *computedColInfo,
	s, e int,
) *Hover {
	col, _ := info.cc.Column()
	expr, _ := info.cc.Expr()
	if isNilNode(col) || isNilNode(expr) {
		return nil
	}

	typ, _ := col.Type()
	sqlType := formatGoogleSQLType(typ)

	if info.kind == computedKindExpr {
		if orig, ok := extractExprOrigin(expr, lineage.origins); ok && orig.meta != nil {
			return renderAliasedOriginHover(snap, st, name, roleLabel, orig, s, e)
		}
	}

	deps, effKind := collectComputedDeps(lineage, info, cat)
	badge := formatRoleTag(roleLabel, effKind)

	return makeResultCardHover(snap, st, name, sqlType, badge, exprSQL, deps, s, e)
}

func renderResolvedExprAliasHover(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	lineage *resolvedLineage,
	cat *catalog.Catalog,
	target aliasTarget,
	exprSQL string,
	expr googlesql.ResolvedExprNode,
) *Hover {
	if orig, ok := extractExprOrigin(expr, lineage.origins); ok && orig.meta != nil {
		return renderAliasedOriginHover(snap, st, target.name, target.roleLabel, orig, target.aliasStart, target.aliasEnd)
	}

	typ, _ := expr.Type()
	sqlType := formatGoogleSQLType(typ)
	deps, effKind := collectExprDeps(lineage, expr, computedKindExpr, nil, nil, cat)
	badge := formatRoleTag(target.roleLabel, effKind)

	return makeResultCardHover(snap, st, target.name, sqlType, badge, exprSQL, deps, target.aliasStart, target.aliasEnd)
}

func renderResolvedColumnAsAlias(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	lineage *resolvedLineage,
	cat *catalog.Catalog,
	name, roleLabel, exprSQL string,
	rc *googlesql.ResolvedColumn,
	s, e int,
) *Hover {
	colID, _ := rc.ColumnId()
	if orig, ok := lineage.origins[colID]; ok && orig.meta != nil {
		return renderAliasedOriginHover(snap, st, name, roleLabel, orig, s, e)
	}

	if info, ok := lineage.resolveComputed(colID); ok {
		return renderComputedColHover(snap, st, lineage, cat, name, roleLabel, exprSQL, info, s, e)
	}

	typ, _ := rc.Type()
	badge := formatRoleTag(roleLabel, computedKindExpr)

	return makeResultCardHover(snap, st, name, formatGoogleSQLType(typ), badge, exprSQL, nil, s, e)
}

func renderAliasedOriginHover(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	aliasName, roleLabel string,
	orig colOrigin,
	s, e int,
) *Hover {
	pathParts := orig.path
	if len(pathParts) == 0 {
		pathParts = []string{orig.colName}
	}
	fullPath := strings.Join(pathParts, ".")

	colMeta, found := orig.meta.FindField(pathParts...)
	if !found {
		if pseudoType := pseudoColumnSQLType(orig.colName); pseudoType != "" {
			colMeta = &catalog.ColumnMeta{Name: orig.colName, Type: pseudoType, Mode: catalog.ModeNullable}
			found = true
		}
	}
	if !found {
		return nil
	}

	var b strings.Builder
	roleSuffix := ""
	if roleLabel != "" {
		if roleLabel == "struct field" {
			roleSuffix = " *(struct field)*"
		} else {
			roleSuffix = fmt.Sprintf(" *(%s · alias)*", roleLabel)
		}
	}

	fmt.Fprintf(&b, "**`%s`** : `%s` (`%s`)%s\n", aliasName, colMeta.SQLType(), colMeta.EffectiveMode(), roleSuffix)

	src := fullPath
	if orig.meta.Ref.FullName() != "" {
		src = orig.meta.Ref.FullName() + "." + fullPath
	}
	fmt.Fprintf(&b, "\nFrom: `%s`\n", src)

	if badges := columnAttributeBadges(orig.meta, fullPath, orig.isPseudo); badges != "" {
		fmt.Fprintf(&b, "\nAttributes: %s\n", badges)
	}

	if colMeta.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", colMeta.Description)
	}

	return makeHoverAtLocalSpan(snap, st, b.String(), s, e)
}

func makeOriginColumnHover(snap *DocumentSnapshot, st *AnalyzedStatement, orig colOrigin, s, e int) *Hover {
	pathParts := orig.path
	if len(pathParts) == 0 {
		pathParts = []string{orig.colName}
	}
	fullPath := strings.Join(pathParts, ".")
	if colMeta, found := orig.meta.FindField(pathParts...); found {
		return makeColumnHover(snap, st, colMeta, fullPath, orig.meta, orig.isPseudo, s, e)
	}
	if pseudoType := pseudoColumnSQLType(orig.colName); pseudoType != "" {
		cm := &catalog.ColumnMeta{Name: orig.colName, Type: pseudoType, Mode: catalog.ModeNullable}
		return makeColumnHover(snap, st, cm, orig.colName, orig.meta, true, s, e)
	}

	return nil
}

func formatRoleTag(roleLabel string, kind computedKind) string {
	kindLabel := ""
	switch kind {
	case computedKindAggregate:
		kindLabel = "aggregate"
	case computedKindWindow:
		kindLabel = "window"
	case computedKindExpr:
	}

	switch {
	case roleLabel != "" && kindLabel != "":
		return roleLabel + " · " + kindLabel
	case roleLabel != "":
		return roleLabel
	default:
		return kindLabel
	}
}

func makeResultCardHover(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	name, sqlType, roleTag, exprSQL string,
	deps []exprDependency,
	s, e int,
) *Hover {
	var b strings.Builder

	fmt.Fprintf(&b, "**`%s`**", name)
	if sqlType != "" {
		fmt.Fprintf(&b, " : `%s`", sqlType)
	}
	if roleTag != "" {
		fmt.Fprintf(&b, " *(%s)*", roleTag)
	}
	b.WriteByte('\n')

	if exprSQL != "" {
		fmt.Fprintf(&b, "\n```sql\n%s\n```\n", exprSQL)
	}

	if len(deps) > 0 {
		b.WriteString("\n**Depends on:**\n")
		limit := min(len(deps), maxDisplayedDeps)
		for _, dep := range deps[:limit] {
			writeDependencyLine(&b, dep)
		}
		if len(deps) > maxDisplayedDeps {
			fmt.Fprintf(&b, "- *… and %d more*\n", len(deps)-maxDisplayedDeps)
		}
	}

	return makeHoverAtLocalSpan(snap, st, b.String(), s, e)
}

func writeDependencyLine(b *strings.Builder, dep exprDependency) {
	fmt.Fprintf(b, "- `%s`", dep.label)
	if dep.sqlType != "" {
		fmt.Fprintf(b, " : `%s`", dep.sqlType)
	}
	if dep.badges != "" {
		fmt.Fprintf(b, " %s", dep.badges)
	}
	switch dep.kind {
	case depWindowColumn:
		b.WriteString(" *(window)*")
	case depParam:
		b.WriteString(" *(parameter)*")
	case depVariable:
		b.WriteString(" *(variable)*")
	case depSysVar:
		b.WriteString(" *(system variable)*")
	case depTable:
		b.WriteString(" *(table)*")
	case depDerived:
		if len(dep.viaSources) > 0 {
			sources := dep.viaSources
			extra := 0
			if len(sources) > maxTransitiveSources {
				extra = len(sources) - maxTransitiveSources
				sources = sources[:maxTransitiveSources]
			}
			b.WriteString(" ← ")
			for i, src := range sources {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(b, "`%s`", src)
			}
			if extra > 0 {
				fmt.Fprintf(b, " *(+%d)*", extra)
			}
		}
	case depColumn:
	}
	b.WriteByte('\n')
}

type depCollector struct {
	lineage  *resolvedLineage
	cat      *catalog.Catalog
	deps     []exprDependency
	seenIdx  map[string]int
	seenCols map[int32]bool
	effKind  computedKind
}

func collectComputedDeps(
	lineage *resolvedLineage,
	info *computedColInfo,
	cat *catalog.Catalog,
) ([]exprDependency, computedKind) {
	expr, _ := info.cc.Expr()

	return collectExprDeps(lineage, expr, info.kind, info.winGroup, info.aggScan, cat)
}

func collectExprDeps(
	lineage *resolvedLineage,
	expr googlesql.ResolvedExprNode,
	baseKind computedKind,
	winGroup *googlesql.ResolvedAnalyticFunctionGroup,
	aggScan *googlesql.ResolvedAggregateScan,
	cat *catalog.Catalog,
) ([]exprDependency, computedKind) {
	dc := &depCollector{
		lineage:  lineage,
		cat:      cat,
		seenIdx:  make(map[string]int),
		seenCols: make(map[int32]bool),
		effKind:  baseKind,
	}

	dc.walkExpr(expr, false)
	dc.walkWindowGroup(winGroup)

	if len(dc.deps) == 0 && dc.effKind == computedKindAggregate && !isNilNode(aggScan) {
		dc.collectAggregateInputTables(aggScan)
	}

	return dc.deps, dc.effKind
}

func (dc *depCollector) addDep(dep exprDependency) {
	key := strings.ToLower(dep.label)
	if key == "" {
		return
	}

	if idx, exists := dc.seenIdx[key]; exists {
		// Upgrade a window-only entry if the same column is also used as a value input.
		if dc.deps[idx].kind == depWindowColumn && dep.kind != depWindowColumn {
			dc.deps[idx] = dep
		}
		return
	}

	dc.seenIdx[key] = len(dc.deps)
	dc.deps = append(dc.deps, dep)
}

func (dc *depCollector) promoteKind(k computedKind) {
	if k == computedKindWindow {
		dc.effKind = computedKindWindow
	} else if k == computedKindAggregate && dc.effKind == computedKindExpr {
		dc.effKind = computedKindAggregate
	}
}

func (dc *depCollector) walkExpr(expr googlesql.ResolvedNode, isWindow bool) {
	if isNilNode(expr) {
		return
	}

	walkResolvedNodes(expr, func(n googlesql.ResolvedNode) bool {
		switch node := n.(type) {
		case *googlesql.ResolvedGetStructField:
			if dc.tryAddStructFieldDep(node, isWindow) {
				return false
			}
			return true
		case *googlesql.ResolvedColumnRef:
			dc.handleColumnRef(node, isWindow)
			return false
		case *googlesql.ResolvedParameter:
			dc.handleParam(node)
			return false
		case *googlesql.ResolvedConstant:
			dc.handleConstant(node)
			return false
		case *googlesql.ResolvedSystemVariable:
			dc.handleSysVar(node)
			return false
		case *googlesql.ResolvedAggregateFunctionCall:
			dc.promoteKind(computedKindAggregate)
			return true
		case *googlesql.ResolvedAnalyticFunctionCall:
			dc.promoteKind(computedKindWindow)
			return true
		default:
			return true
		}
	})
}

func (dc *depCollector) tryAddStructFieldDep(outer *googlesql.ResolvedGetStructField, isWindow bool) bool {
	fieldIdxs, base, ok := unwrapStructFieldIndices(outer)
	if !ok {
		return false
	}
	ref, isRef := base.(*googlesql.ResolvedColumnRef)
	if !isRef || isNilNode(ref) {
		return false
	}

	rc, err := ref.Column()
	if err != nil || isNilNode(rc) {
		return false
	}

	colID, _ := rc.ColumnId()
	orig, hasOrig := dc.lineage.origins[colID]
	if !hasOrig || orig.meta == nil {
		return false
	}

	reversed := append([]int32(nil), fieldIdxs...)
	reverseInt32s(reversed)
	fullPath := resolveFieldPathFromIndices(orig, reversed)
	if fullPath == "" {
		return false
	}

	label := fullPath
	if orig.meta.Ref.FullName() != "" {
		label = orig.meta.Ref.FullName() + "." + fullPath
	}

	typ, _ := outer.Type()
	kind := depColumn
	if isWindow {
		kind = depWindowColumn
	}

	dc.addDep(exprDependency{
		kind:    kind,
		label:   label,
		sqlType: formatGoogleSQLType(typ),
		badges:  columnAttributeBadges(orig.meta, fullPath, orig.isPseudo),
	})

	return true
}

func (dc *depCollector) handleColumnRef(ref *googlesql.ResolvedColumnRef, isWindow bool) {
	rc, err := ref.Column()
	if err != nil || isNilNode(rc) {
		return
	}

	colID, _ := rc.ColumnId()
	colName, _ := rc.Name()
	typ, _ := rc.Type()
	sqlType := formatGoogleSQLType(typ)

	if strings.HasPrefix(colName, "$") {
		if ccInfo, ok := dc.lineage.computed[colID]; ok && !dc.seenCols[colID] {
			dc.seenCols[colID] = true
			dc.promoteKind(ccInfo.kind)
			innerExpr, _ := ccInfo.cc.Expr()
			dc.walkExpr(innerExpr, isWindow)
			dc.walkWindowGroup(ccInfo.winGroup)

			return
		}
	}

	if orig, ok := dc.lineage.origins[colID]; ok && orig.meta != nil {
		pathParts := orig.path
		if len(pathParts) == 0 {
			pathParts = []string{orig.colName}
		}
		fullPath := strings.Join(pathParts, ".")
		label := fullPath
		if orig.meta.Ref.FullName() != "" {
			label = orig.meta.Ref.FullName() + "." + fullPath
		}
		kind := depColumn
		if isWindow {
			kind = depWindowColumn
		}

		dc.addDep(exprDependency{
			kind:    kind,
			label:   label,
			sqlType: sqlType,
			badges:  columnAttributeBadges(orig.meta, fullPath, orig.isPseudo),
		})

		return
	}

	label := colName
	if cteName := dc.lineage.cteOwner[colID]; cteName != "" {
		label = cteName + "." + colName
	}

	via := dc.lineage.tracePhysicalSources(colID)
	kind := depDerived
	if isWindow {
		kind = depWindowColumn
	}

	dc.addDep(exprDependency{
		kind:       kind,
		label:      label,
		sqlType:    sqlType,
		viaSources: via,
	})
}

func (dc *depCollector) handleParam(p *googlesql.ResolvedParameter) {
	name, _ := p.Name()
	typ, _ := p.Type()
	if name == "" {
		return
	}
	dc.addDep(exprDependency{
		kind:    depParam,
		label:   "@" + name,
		sqlType: formatGoogleSQLType(typ),
	})
}

func (dc *depCollector) handleConstant(c *googlesql.ResolvedConstant) {
	cn, err := c.Constant()
	if err != nil || isNilNode(cn) {
		return
	}
	name, _ := cn.FullName()
	typ, _ := cn.Type()
	if name == "" {
		return
	}
	dc.addDep(exprDependency{
		kind:    depVariable,
		label:   name,
		sqlType: formatGoogleSQLType(typ),
	})
}

func (dc *depCollector) handleSysVar(sv *googlesql.ResolvedSystemVariable) {
	parts, _ := sv.NamePath()
	typ, _ := sv.Type()
	if len(parts) == 0 {
		return
	}
	dc.addDep(exprDependency{
		kind:    depSysVar,
		label:   "@@" + strings.Join(parts, "."),
		sqlType: formatGoogleSQLType(typ),
	})
}

func (dc *depCollector) walkWindowGroup(grp *googlesql.ResolvedAnalyticFunctionGroup) {
	if isNilNode(grp) {
		return
	}

	if pb, _ := grp.PartitionBy(); !isNilNode(pb) {
		refs, _ := pb.PartitionByList()
		for _, r := range refs {
			dc.handleColumnRef(r, true)
		}
	}

	if ob, _ := grp.OrderBy(); !isNilNode(ob) {
		items, _ := ob.OrderByItemList()
		for _, it := range items {
			if isNilNode(it) {
				continue
			}
			if r, _ := it.ColumnRef(); !isNilNode(r) {
				dc.handleColumnRef(r, true)
			}
		}
	}
}

func (dc *depCollector) collectAggregateInputTables(aggScan *googlesql.ResolvedAggregateScan) {
	inScan, _ := aggScan.InputScan()
	if isNilNode(inScan) {
		return
	}

	walkResolvedNodes(inScan, func(n googlesql.ResolvedNode) bool {
		switch node := n.(type) {
		case *googlesql.ResolvedTableScan:
			if name := dc.tableScanDisplayName(node); name != "" {
				dc.addDep(exprDependency{kind: depTable, label: name})
			}
			return false
		case *googlesql.ResolvedWithRefScan:
			if qname, _ := node.WithQueryName(); qname != "" {
				dc.addDep(exprDependency{kind: depTable, label: qname})
			}
			return false
		default:
			return true
		}
	})
}

func (dc *depCollector) tableScanDisplayName(scan *googlesql.ResolvedTableScan) string {
	tbl, _ := scan.Table()
	if isNilNode(tbl) {
		return ""
	}
	if dc.cat != nil {
		if meta, ok := dc.cat.LookupTable(tbl); ok && meta != nil && meta.Ref.FullName() != "" {
			return meta.Ref.FullName()
		}
	}
	name, _ := tbl.FullName()

	return name
}

func (lin *resolvedLineage) tracePhysicalSources(colID int32) []string {
	t := &physicalSourceTracer{
		lin:       lin,
		seenLabel: make(map[string]bool),
		seenCol:   make(map[int32]bool),
	}
	t.trace(colID, 0)

	return t.out
}

type physicalSourceTracer struct {
	lin       *resolvedLineage
	out       []string
	seenLabel map[string]bool
	seenCol   map[int32]bool
}

func (t *physicalSourceTracer) addOrigin(orig colOrigin) {
	pathParts := orig.path
	if len(pathParts) == 0 {
		pathParts = []string{orig.colName}
	}
	label := strings.Join(pathParts, ".")
	if orig.meta.Ref.FullName() != "" {
		label = orig.meta.Ref.FullName() + "." + label
	}
	low := strings.ToLower(label)
	if !t.seenLabel[low] {
		t.seenLabel[low] = true
		t.out = append(t.out, label)
	}
}

func (t *physicalSourceTracer) trace(id int32, depth int) {
	if depth > maxLineageDepth || t.seenCol[id] {
		return
	}
	t.seenCol[id] = true

	if orig, ok := t.lin.origins[id]; ok && orig.meta != nil {
		t.addOrigin(orig)
		return
	}

	if info, ok := t.lin.computed[id]; ok && info != nil && !isNilNode(info.cc) {
		t.traceComputedExpr(info.cc, depth)
		return
	}

	for _, nextID := range t.lin.colLinks[id] {
		t.trace(nextID, depth+1)
	}
}

func (t *physicalSourceTracer) traceComputedExpr(cc *googlesql.ResolvedComputedColumn, depth int) {
	expr, _ := cc.Expr()
	walkResolvedNodes(expr, func(n googlesql.ResolvedNode) bool {
		switch node := n.(type) {
		case *googlesql.ResolvedGetStructField:
			if orig, okOrig := extractExprOrigin(node, t.lin.origins); okOrig && orig.meta != nil {
				t.addOrigin(orig)
				return false
			}
			return true
		case *googlesql.ResolvedColumnRef:
			if rc, _ := node.Column(); !isNilNode(rc) {
				if nextID, err := rc.ColumnId(); err == nil {
					t.trace(nextID, depth+1)
				}
			}
			return false
		default:
			return true
		}
	})
}

func hoverComputedColumnRef(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	lineage *resolvedLineage,
	cat *catalog.Catalog,
	rc *googlesql.ResolvedColumn,
	s, e int,
) *Hover {
	colID, _ := rc.ColumnId()
	colName, _ := rc.Name()

	if setOp, ok := lineage.resolveSetOp(colID); ok {
		return renderSetOpColumnHover(snap, st, lineage, cat, rc, setOp, s, e)
	}

	info, ok := lineage.resolveComputed(colID)
	if !ok {
		return nil
	}

	roleLabel := "computed column"
	if cteName := lineage.cteOwner[colID]; cteName != "" {
		roleLabel = fmt.Sprintf("from CTE `%s`", cteName)
	}

	exprSQL := exprSQLForComputedCol(st, info)

	return renderComputedColHover(snap, st, lineage, cat, colName, roleLabel, exprSQL, info, s, e)
}

func renderSetOpColumnHover(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	lineage *resolvedLineage,
	cat *catalog.Catalog,
	rc *googlesql.ResolvedColumn,
	setOp *setOpColInfo,
	s, e int,
) *Hover {
	colID, _ := rc.ColumnId()
	colName, _ := rc.Name()
	typ, _ := rc.Type()
	sqlType := formatGoogleSQLType(typ)

	roleTag := setOp.opLabel
	if cteName := lineage.cteOwner[colID]; cteName != "" {
		roleTag = fmt.Sprintf("from CTE `%s` · %s", cteName, setOp.opLabel)
	}

	dc := &depCollector{
		lineage:  lineage,
		cat:      cat,
		seenIdx:  make(map[string]int),
		seenCols: make(map[int32]bool),
	}

	for _, branchID := range setOp.branchIDs {
		dc.collectBranchDep(branchID)
	}

	return makeResultCardHover(snap, st, colName, sqlType, roleTag, "", dc.deps, s, e)
}

func (dc *depCollector) collectBranchDep(branchID int32) {
	if info, ok := dc.lineage.computed[branchID]; ok && info != nil && !isNilNode(info.cc) {
		expr, _ := info.cc.Expr()
		dc.walkExpr(expr, false)
		dc.walkWindowGroup(info.winGroup)
		return
	}

	if orig, ok := dc.lineage.origins[branchID]; ok && orig.meta != nil {
		pathParts := orig.path
		if len(pathParts) == 0 {
			pathParts = []string{orig.colName}
		}
		fullPath := strings.Join(pathParts, ".")
		label := fullPath
		if orig.meta.Ref.FullName() != "" {
			label = orig.meta.Ref.FullName() + "." + fullPath
		}
		dc.addDep(exprDependency{
			kind:   depColumn,
			label:  label,
			badges: columnAttributeBadges(orig.meta, fullPath, orig.isPseudo),
		})
		return
	}

	for _, nextID := range dc.lineage.colLinks[branchID] {
		dc.collectBranchDep(nextID)
	}
}

func exprSQLForComputedCol(st *AnalyzedStatement, info *computedColInfo) string {
	if info == nil || isNilNode(info.cc) {
		return ""
	}
	expr, _ := info.cc.Expr()
	cs, ce, ok := resolvedSubtreeByteRange(expr)
	if !ok {
		return ""
	}

	// Prefer the full enclosing ASTSelectColumn / ASTPipeSetItem expression when available
	// so aggregate/window calls include their full argument lists and OVER clauses.
	if !isNilNode(st.ASTStmt) {
		if es, ee, found := findASTExprCoveringSpan(st.ASTStmt, cs, ce); found {
			return formatExprSnippet(st.SQL, es, ee)
		}
	}

	return formatExprSnippet(st.SQL, cs, ce)
}

func findASTExprCoveringSpan(root googlesql.ASTNode, cs, ce int) (start, end int, ok bool) {
	bestLen := int(^uint(0) >> 1)
	found := false

	walkASTNodes(root, func(n googlesql.ASTNode) bool {
		var ex googlesql.ASTNode
		switch node := n.(type) {
		case *googlesql.ASTSelectColumn:
			ex, _ = node.Expression()
		case *googlesql.ASTGroupingItem:
			ex, _ = node.Expression()
		case *googlesql.ASTPipeSetItem:
			ex, _ = node.Expression()
		default:
			return true
		}

		es, ee, has := astNodeByteRange(ex)
		if has && es <= cs && ce <= ee && ee-es < bestLen {
			start, end = es, ee
			bestLen = ee - es
			found = true
		}

		return true
	})

	return start, end, found
}

func hoverResolvedOrderByItem(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	item *googlesql.ResolvedOrderByItem,
	lineage *resolvedLineage,
	cat *catalog.Catalog,
	localOff int,
) *Hover {
	if isNilNode(item) {
		return nil
	}

	ref, err := item.ColumnRef()
	if err != nil || isNilNode(ref) {
		return nil
	}

	rc, err := ref.Column()
	if err != nil || isNilNode(rc) {
		return nil
	}

	s, e, ok := resolvedNodeByteRange(item)
	if !ok || e <= s || e > len(st.SQL) {
		return nil
	}

	// Narrow span to the leading identifier / dotted path so hovering ASC/DESC/NULLS does not trigger.
	identEnd := leadingPathEnd(st.SQL, s, e)
	if localOff < s || localOff >= identEnd {
		return nil
	}

	return hoverResolvedColumnRefAtSpan(snap, st, ref, lineage, cat, s, identEnd)
}

func leadingPathEnd(sql string, s, e int) int {
	name, ns, ne := dottedNameAroundOffset(sql, s)
	if name != "" && ns >= s && ne <= e {
		return ne
	}

	return e
}

func hoverResultAliasFromParserAST(snap *DocumentSnapshot, st *AnalyzedStatement, localOff int) *Hover {
	target, ok := findAliasTargetAtOffset(st.ASTStmt, localOff)
	if !ok {
		return nil
	}

	if target.kind == aliasTargetCTE {
		if cols := findCTEColumnsInAST(st.ASTStmt, target.name); len(cols) > 0 {
			return makeHoverAtLocalSpan(
				snap, st,
				fmt.Sprintf("### CTE `%s`\n\nColumns: %s", target.name, strings.Join(cols, ", ")),
				target.aliasStart, target.aliasEnd,
			)
		}

		return nil
	}

	if target.kind == aliasTargetPipeRenameNew || target.kind == aliasTargetPipeRenameOld {
		return nil
	}

	exprSQL := formatExprSnippet(st.SQL, target.exprStart, target.exprEnd)
	aliasMap := buildASTTableAliasMap(snap, target.selectNode)

	if pe, isPE := target.exprNode.(*googlesql.ASTPathExpression); isPE && !isNilNode(pe) {
		if h := hoverParserDirectColumnAlias(snap, st, target, pe, aliasMap); h != nil {
			return h
		}
	}

	sqlType, kind := inferParserExprTypeAndKind(st.SQL, target.exprNode)
	deps := collectParserExprDeps(target.exprNode, aliasMap)
	badge := formatRoleTag(target.roleLabel, kind)

	return makeResultCardHover(snap, st, target.name, sqlType, badge, exprSQL, deps, target.aliasStart, target.aliasEnd)
}

type astTableBinding struct {
	fullName string
	meta     *catalog.TableMeta
}

func buildASTTableAliasMap(snap *DocumentSnapshot, sel *googlesql.ASTSelect) map[string]astTableBinding {
	out := make(map[string]astTableBinding)
	if isNilNode(sel) {
		return out
	}

	fc, _ := sel.FromClause()
	if isNilNode(fc) {
		return out
	}

	defProj, defDS := storeDefaultProjectDataset(snap)

	walkASTNodes(fc, func(n googlesql.ASTNode) bool {
		if tp, ok := n.(*googlesql.ASTTablePathExpression); ok && !isNilNode(tp) {
			bindASTTablePath(snap, tp, defProj, defDS, out)
		}

		return true
	})

	return out
}

func bindASTTablePath(
	snap *DocumentSnapshot,
	tp *googlesql.ASTTablePathExpression,
	defProj, defDS string,
	out map[string]astTableBinding,
) {
	pe, _ := tp.PathExpr()
	if isNilNode(pe) {
		return
	}

	parts, _ := pe.ToIdentifierVector()
	if len(parts) == 0 {
		return
	}

	ref := tableRefFromPath(parts, defProj, defDS)
	fullName := ref.FullName()
	if fullName == "" {
		fullName = strings.Join(parts, ".")
	}

	var meta *catalog.TableMeta
	if snap.Store != nil {
		if m, found := snap.Store.GetTable(ref); found {
			meta = m
			if m.Ref.FullName() != "" {
				fullName = m.Ref.FullName()
			}
		}
	}

	binding := astTableBinding{fullName: fullName, meta: meta}
	if al, _ := tp.Alias(); !isNilNode(al) {
		if id, _ := al.Identifier(); !isNilNode(id) {
			if alName := identText(id); alName != "" {
				out[strings.ToLower(alName)] = binding
			}
		}
	}

	out[strings.ToLower(parts[len(parts)-1])] = binding
}

func hoverParserDirectColumnAlias(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	target aliasTarget,
	pe *googlesql.ASTPathExpression,
	aliasMap map[string]astTableBinding,
) *Hover {
	parts, _ := pe.ToIdentifierVector()
	if len(parts) == 0 {
		return nil
	}

	var binding astTableBinding
	var fieldParts []string

	if len(parts) >= 2 {
		if b, ok := aliasMap[strings.ToLower(parts[0])]; ok {
			binding = b
			fieldParts = parts[1:]
		}
	} else if len(aliasMap) == 1 {
		for _, b := range aliasMap {
			binding = b
		}
		fieldParts = parts
	}

	if binding.meta == nil || len(fieldParts) == 0 {
		return nil
	}

	orig := colOrigin{
		meta:    binding.meta,
		colName: fieldParts[len(fieldParts)-1],
		path:    fieldParts,
	}

	return renderAliasedOriginHover(snap, st, target.name, target.roleLabel, orig, target.aliasStart, target.aliasEnd)
}

func inferParserExprTypeAndKind(stmtSQL string, expr googlesql.ASTNode) (string, computedKind) {
	if isNilNode(expr) {
		return "", computedKindExpr
	}

	switch node := expr.(type) {
	case *googlesql.ASTIntLiteral:
		return "INT64", computedKindExpr
	case *googlesql.ASTFloatLiteral:
		return "FLOAT64", computedKindExpr
	case *googlesql.ASTStringLiteral:
		return "STRING", computedKindExpr
	case *googlesql.ASTBooleanLiteral:
		return "BOOL", computedKindExpr
	case *googlesql.ASTBytesLiteral:
		return "BYTES", computedKindExpr
	case *googlesql.ASTCastExpression:
		if typNode, _ := node.Type(); !isNilNode(typNode) {
			if s, e, ok := astNodeByteRange(typNode); ok && e <= len(stmtSQL) && e > s {
				return strings.TrimSpace(stmtSQL[s:e]), computedKindExpr
			}
		}
		return "", computedKindExpr
	case *googlesql.ASTAnalyticFunctionCall:
		fn, _ := node.Function()
		typ, _ := inferParserFuncCallTypeAndKind(fn)
		return typ, computedKindWindow
	case *googlesql.ASTFunctionCall:
		return inferParserFuncCallTypeAndKind(node)
	default:
		return "", computedKindExpr
	}
}

func inferParserFuncCallTypeAndKind(fc *googlesql.ASTFunctionCall) (string, computedKind) {
	if isNilNode(fc) {
		return "", computedKindExpr
	}

	pe, _ := fc.Function()
	if isNilNode(pe) {
		return "", computedKindExpr
	}

	parts, _ := pe.ToIdentifierVector()
	full := strings.ToUpper(strings.Join(parts, "."))
	doc, _, found := funcdocs.LookupWithSafe(full)
	if !found || doc == nil {
		return "", computedKindExpr
	}

	kind := computedKindExpr
	catLow := strings.ToLower(doc.Category)
	if strings.Contains(catLow, "aggregate") {
		kind = computedKindAggregate
	}

	typ := ""
	if m := simpleBacktickTypeRe.FindStringSubmatch(strings.TrimSpace(doc.ReturnType)); len(m) == 2 {
		typ = m[1]
	}

	return typ, kind
}

func collectParserExprDeps(expr googlesql.ASTNode, aliasMap map[string]astTableBinding) []exprDependency {
	if isNilNode(expr) {
		return nil
	}

	var deps []exprDependency
	seen := make(map[string]bool)

	walkASTNodes(expr, func(n googlesql.ASTNode) bool {
		pe, ok := n.(*googlesql.ASTPathExpression)
		if !ok || isNilNode(pe) {
			return true
		}
		if isFuncCallNamePath(pe) {
			return false
		}

		if dep, okDep := parserExprDepFromPath(pe, aliasMap); okDep {
			low := strings.ToLower(dep.label)
			if !seen[low] {
				seen[low] = true
				deps = append(deps, dep)
			}
		}

		return false
	})

	return deps
}

func isFuncCallNamePath(pe *googlesql.ASTPathExpression) bool {
	fc, isFC := astParent(pe).(*googlesql.ASTFunctionCall)
	if !isFC || isNilNode(fc) {
		return false
	}
	fnPE, _ := fc.Function()

	return isSameASTNode(fnPE, pe)
}

func parserExprDepFromPath(pe *googlesql.ASTPathExpression, aliasMap map[string]astTableBinding) (exprDependency, bool) {
	parts, _ := pe.ToIdentifierVector()
	if len(parts) == 0 {
		return exprDependency{}, false
	}

	label := strings.Join(parts, ".")
	sqlType := ""
	if len(parts) >= 2 {
		if b, found := aliasMap[strings.ToLower(parts[0])]; found {
			label = b.fullName + "." + strings.Join(parts[1:], ".")
			if b.meta != nil {
				if cm, hasField := b.meta.FindField(parts[1:]...); hasField {
					sqlType = cm.SQLType()
				}
			}
		}
	}

	return exprDependency{
		kind:    depColumn,
		label:   label,
		sqlType: sqlType,
	}, true
}

func formatExprSnippet(stmtSQL string, start, end int) string {
	if start < 0 || end <= start || end > len(stmtSQL) {
		return ""
	}

	raw := strings.TrimSpace(stmtSQL[start:end])
	if raw == "" {
		return ""
	}

	lines := strings.Split(raw, "\n")
	lines = dedentContinuationLines(lines)

	truncated := false
	if len(lines) > maxExprSnippetLines {
		lines = lines[:maxExprSnippetLines]
		truncated = true
	}

	out := strings.Join(lines, "\n")
	if len(out) > maxExprSnippetChars {
		out = strings.TrimRight(out[:maxExprSnippetChars], " \t\n")
		truncated = true
	}

	if truncated {
		out += " ..."
	}

	return out
}

func dedentContinuationLines(lines []string) []string {
	if len(lines) <= 1 {
		return lines
	}

	minIndent := -1
	for _, line := range lines[1:] {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		indent := len(line) - len(trimmed)
		if minIndent < 0 || indent < minIndent {
			minIndent = indent
		}
	}

	if minIndent <= 0 {
		return lines
	}

	out := slices.Clone(lines)
	for i := 1; i < len(out); i++ {
		if len(out[i]) >= minIndent {
			out[i] = out[i][minIndent:]
		} else {
			out[i] = strings.TrimLeft(out[i], " \t")
		}
	}

	return out
}

type parserResolvedCol struct {
	sqlType    string
	kind       computedKind
	exprSQL    string
	roleTag    string
	deps       []exprDependency
	directOrig *colOrigin
}

func hoverColumnRefFromParserAST(snap *DocumentSnapshot, st *AnalyzedStatement, localOff int) *Hover {
	pe := findPathExprAtOffset(st.ASTStmt, localOff)
	if isNilNode(pe) || isFuncCallNamePath(pe) || isTablePathExpr(pe) {
		return nil
	}

	parts, _ := pe.ToIdentifierVector()
	if len(parts) == 0 {
		return nil
	}

	s, e, ok := astNodeByteRange(pe)
	if !ok || localOff < s || localOff >= e {
		return nil
	}

	ctes := collectASTCTEs(st.ASTStmt)
	fromItems := collectEnclosingFromItems(snap, pe)
	if len(fromItems) == 0 {
		return nil
	}

	// If cursor is on the qualifier prefix (e.g. `j` in `j.word_qty`), hover the CTE or table.
	if len(parts) >= 2 {
		if firstID, err := pe.Name(0); err == nil && !isNilNode(firstID) {
			if fs, fe, hasF := astNodeByteRange(firstID); hasF && localOff >= fs && localOff < fe {
				return hoverParserQualifier(snap, st, fromItems, ctes, parts[0], fs, fe)
			}
		}
	}

	qual := ""
	fieldParts := parts
	if len(parts) >= 2 {
		qual = parts[0]
		fieldParts = parts[1:]
	}

	colName := fieldParts[len(fieldParts)-1]
	matched := resolveParserColFromItems(snap, st.SQL, fromItems, ctes, qual, fieldParts, make(map[string]bool))
	if matched == nil {
		return nil
	}

	if matched.directOrig != nil && matched.directOrig.meta != nil {
		if h := makeOriginColumnHover(snap, st, *matched.directOrig, s, e); h != nil {
			return h
		}
	}

	return makeResultCardHover(snap, st, colName, matched.sqlType, matched.roleTag, matched.exprSQL, matched.deps, s, e)
}

func isTablePathExpr(pe *googlesql.ASTPathExpression) bool {
	tp, ok := astParent(pe).(*googlesql.ASTTablePathExpression)
	if !ok || isNilNode(tp) {
		return false
	}
	tpPE, _ := tp.PathExpr()

	return isSameASTNode(tpPE, pe)
}

type parserFromItem struct {
	alias     string
	tableName string
	fullName  string
	meta      *catalog.TableMeta
}

func collectASTCTEs(root googlesql.ASTNode) map[string]*googlesql.ASTAliasedQuery {
	ctes := make(map[string]*googlesql.ASTAliasedQuery)
	walkASTNodes(root, func(n googlesql.ASTNode) bool {
		aq, ok := n.(*googlesql.ASTAliasedQuery)
		if !ok || isNilNode(aq) {
			return true
		}
		al, _ := aq.Alias()
		if name := identText(al); name != "" {
			ctes[strings.ToLower(name)] = aq
		}

		return true
	})

	return ctes
}

func collectEnclosingFromItems(snap *DocumentSnapshot, n googlesql.ASTNode) []parserFromItem {
	for cur := astParent(n); !isNilNode(cur); cur = astParent(cur) {
		fc := fromClauseForAncestor(cur)
		if isNilNode(fc) {
			continue
		}
		if items := extractFromClauseItems(snap, fc); len(items) > 0 {
			return items
		}
	}

	return nil
}

func fromClauseForAncestor(cur googlesql.ASTNode) *googlesql.ASTFromClause {
	switch node := cur.(type) {
	case *googlesql.ASTSelect:
		fc, _ := node.FromClause()
		return fc
	case *googlesql.ASTQuery:
		qe, _ := node.QueryExpr()
		if fq, isFQ := qe.(*googlesql.ASTFromQuery); isFQ && !isNilNode(fq) {
			fc, _ := fq.FromClause()
			return fc
		}
	default:
	}

	return nil
}

func extractFromClauseItems(snap *DocumentSnapshot, fc *googlesql.ASTFromClause) []parserFromItem {
	var items []parserFromItem
	defProj, defDS := storeDefaultProjectDataset(snap)

	walkASTNodes(fc, func(n googlesql.ASTNode) bool {
		tp, ok := n.(*googlesql.ASTTablePathExpression)
		if !ok || isNilNode(tp) {
			return true
		}

		if item, valid := parserFromItemFromTablePath(snap, tp, defProj, defDS); valid {
			items = append(items, item)
		}

		return false
	})

	return items
}

func parserFromItemFromTablePath(
	snap *DocumentSnapshot,
	tp *googlesql.ASTTablePathExpression,
	defProj, defDS string,
) (parserFromItem, bool) {
	pe, _ := tp.PathExpr()
	if isNilNode(pe) {
		return parserFromItem{}, false
	}

	parts, _ := pe.ToIdentifierVector()
	if len(parts) == 0 {
		return parserFromItem{}, false
	}

	ref := tableRefFromPath(parts, defProj, defDS)
	fullName := ref.FullName()
	if fullName == "" {
		fullName = strings.Join(parts, ".")
	}

	var meta *catalog.TableMeta
	if snap.Store != nil {
		if m, found := snap.Store.GetTable(ref); found {
			meta = m
			if m.Ref.FullName() != "" {
				fullName = m.Ref.FullName()
			}
		}
	}

	alias := ""
	if al, _ := tp.Alias(); !isNilNode(al) {
		if id, _ := al.Identifier(); !isNilNode(id) {
			alias = identText(id)
		}
	}

	return parserFromItem{
		alias:     alias,
		tableName: parts[len(parts)-1],
		fullName:  fullName,
		meta:      meta,
	}, true
}

func hoverParserQualifier(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	items []parserFromItem,
	ctes map[string]*googlesql.ASTAliasedQuery,
	qual string,
	s, e int,
) *Hover {
	for _, it := range items {
		if !matchesFromItemQual(it, qual) {
			continue
		}
		if it.meta != nil {
			return makeTableHover(snap, st, it.meta, s, e)
		}
		if aq, ok := ctes[strings.ToLower(it.tableName)]; ok {
			q, _ := aq.Query()
			if cols := extractSelectAliasesFromQuery(q); len(cols) > 0 {
				return makeHoverAtLocalSpan(
					snap, st,
					fmt.Sprintf("### CTE `%s`\n\nColumns: %s", it.tableName, strings.Join(cols, ", ")),
					s, e,
				)
			}
		}
	}

	return nil
}

func matchesFromItemQual(it parserFromItem, qual string) bool {
	if qual == "" {
		return true
	}
	if it.alias != "" {
		return strings.EqualFold(it.alias, qual)
	}

	return strings.EqualFold(it.tableName, qual)
}

func resolveParserColFromItems(
	snap *DocumentSnapshot,
	stmtSQL string,
	items []parserFromItem,
	ctes map[string]*googlesql.ASTAliasedQuery,
	qual string,
	fieldParts []string,
	visited map[string]bool,
) *parserResolvedCol {
	for _, it := range items {
		if !matchesFromItemQual(it, qual) {
			continue
		}

		if it.meta != nil {
			if _, ok := it.meta.FindField(fieldParts...); ok {
				return &parserResolvedCol{
					directOrig: &colOrigin{
						meta:    it.meta,
						colName: fieldParts[len(fieldParts)-1],
						path:    fieldParts,
					},
				}
			}
		}

		cteKey := strings.ToLower(it.tableName)
		if aq, ok := ctes[cteKey]; ok && !visited[cteKey] {
			visited[cteKey] = true
			res := resolveParserColInCTE(snap, stmtSQL, aq, ctes, fieldParts[0], visited)
			delete(visited, cteKey)
			if res != nil {
				return res
			}
		}
	}

	return nil
}

func resolveParserColInCTE(
	snap *DocumentSnapshot,
	stmtSQL string,
	aq *googlesql.ASTAliasedQuery,
	ctes map[string]*googlesql.ASTAliasedQuery,
	colName string,
	visited map[string]bool,
) *parserResolvedCol {
	al, _ := aq.Alias()
	cteName := identText(al)
	q, _ := aq.Query()
	if isNilNode(q) {
		return nil
	}

	res := resolveParserColInQuery(snap, stmtSQL, q, ctes, colName, visited)
	if res == nil {
		return nil
	}

	if res.roleTag == "" {
		res.roleTag = formatRoleTag(fmt.Sprintf("from CTE `%s`", cteName), res.kind)
	} else if !strings.HasPrefix(res.roleTag, "from CTE") {
		res.roleTag = fmt.Sprintf("from CTE `%s` · %s", cteName, res.roleTag)
	}

	return res
}

func resolveParserColInQuery(
	snap *DocumentSnapshot,
	stmtSQL string,
	q *googlesql.ASTQuery,
	ctes map[string]*googlesql.ASTAliasedQuery,
	colName string,
	visited map[string]bool,
) *parserResolvedCol {
	if isNilNode(q) {
		return nil
	}

	// Check pipe operators from last to first (EXTEND, AGGREGATE, SELECT, WINDOW, SET, RENAME).
	if res := resolveParserColInPipeOps(snap, stmtSQL, q, colName); res != nil {
		return res
	}

	qe, _ := q.QueryExpr()

	return resolveParserColInQueryExpr(snap, stmtSQL, qe, ctes, colName, visited)
}

func resolveParserColInPipeOps(
	snap *DocumentSnapshot,
	stmtSQL string,
	q *googlesql.ASTQuery,
	colName string,
) *parserResolvedCol {
	num := astNumChildren(q)
	for i := num - 1; i >= 0; i-- {
		ch := astChild(q, i)
		switch op := ch.(type) {
		case *googlesql.ASTPipeSelect:
			if sel := firstASTSelectChild(op); !isNilNode(sel) {
				if res, hasStar := resolveParserColInSelectList(snap, stmtSQL, sel, colName); res != nil {
					return res
				} else if !hasStar {
					return nil
				}
			}
		case *googlesql.ASTPipeExtend, *googlesql.ASTPipeAggregate, *googlesql.ASTPipeWindow:
			if sel := firstASTSelectChild(op); !isNilNode(sel) {
				if res, _ := resolveParserColInSelectList(snap, stmtSQL, sel, colName); res != nil {
					return res
				}
			}
		default:
		}
	}

	return nil
}

func firstASTSelectChild(n googlesql.ASTNode) *googlesql.ASTSelect {
	for i := range astNumChildren(n) {
		if sel, ok := astChild(n, i).(*googlesql.ASTSelect); ok && !isNilNode(sel) {
			return sel
		}
	}

	return nil
}

func resolveParserColInQueryExpr(
	snap *DocumentSnapshot,
	stmtSQL string,
	qe googlesql.ASTNode,
	ctes map[string]*googlesql.ASTAliasedQuery,
	colName string,
	visited map[string]bool,
) *parserResolvedCol {
	if isNilNode(qe) {
		return nil
	}

	switch node := qe.(type) {
	case *googlesql.ASTQuery:
		return resolveParserColInQuery(snap, stmtSQL, node, ctes, colName, visited)
	case *googlesql.ASTSelect:
		return resolveParserColInSelect(snap, stmtSQL, node, ctes, colName, visited)
	case *googlesql.ASTFromQuery:
		fc, _ := node.FromClause()
		if isNilNode(fc) {
			return nil
		}
		items := extractFromClauseItems(snap, fc)
		return resolveParserColFromItems(snap, stmtSQL, items, ctes, "", []string{colName}, visited)
	case *googlesql.ASTSetOperation:
		return resolveParserColInSetOp(snap, stmtSQL, node, ctes, colName, visited)
	default:
		return nil
	}
}

func resolveParserColInSelect(
	snap *DocumentSnapshot,
	stmtSQL string,
	sel *googlesql.ASTSelect,
	ctes map[string]*googlesql.ASTAliasedQuery,
	colName string,
	visited map[string]bool,
) *parserResolvedCol {
	res, hasStar := resolveParserColInSelectList(snap, stmtSQL, sel, colName)
	if res != nil {
		return res
	}
	if !hasStar {
		return nil
	}

	fc, _ := sel.FromClause()
	if isNilNode(fc) {
		return nil
	}

	items := extractFromClauseItems(snap, fc)

	return resolveParserColFromItems(snap, stmtSQL, items, ctes, "", []string{colName}, visited)
}

func resolveParserColInSelectList(
	snap *DocumentSnapshot,
	stmtSQL string,
	sel *googlesql.ASTSelect,
	colName string,
) (res *parserResolvedCol, hasStar bool) {
	sl, _ := sel.SelectList()
	if isNilNode(sl) {
		return nil, false
	}

	aliasMap := buildASTTableAliasMap(snap, sel)
	for i := range astNumChildren(sl) {
		sc, ok := astChild(sl, i).(*googlesql.ASTSelectColumn)
		if !ok || isNilNode(sc) {
			continue
		}

		expr, _ := sc.Expression()
		if isStarExpr(expr) {
			hasStar = true
			continue
		}

		if !strings.EqualFold(selectColumnName(sc), colName) {
			continue
		}

		es, ee, _ := astNodeByteRange(expr)
		sqlType, kind := inferParserExprTypeAndKind(stmtSQL, expr)
		deps := collectParserExprDeps(expr, aliasMap)

		return &parserResolvedCol{
			sqlType: sqlType,
			kind:    kind,
			exprSQL: formatExprSnippet(stmtSQL, es, ee),
			deps:    deps,
		}, hasStar
	}

	return nil, hasStar
}

func resolveParserColInSetOp(
	snap *DocumentSnapshot,
	stmtSQL string,
	so *googlesql.ASTSetOperation,
	ctes map[string]*googlesql.ASTAliasedQuery,
	colName string,
	visited map[string]bool,
) *parserResolvedCol {
	opLabel := parserSetOpLabel(stmtSQL, so)
	var sqlType string
	var deps []exprDependency
	seen := make(map[string]bool)
	matchedAny := false

	for i := range astNumChildren(so) {
		ch := astChild(so, i)
		if _, isMeta := ch.(*googlesql.ASTSetOperationMetadataList); isMeta {
			continue
		}

		branchRes := resolveParserColInQueryExpr(snap, stmtSQL, ch, ctes, colName, visited)
		if branchRes == nil {
			continue
		}
		matchedAny = true
		if sqlType == "" && branchRes.sqlType != "" {
			sqlType = branchRes.sqlType
		}

		dep := parserBranchToDep(ch, colName, branchRes)
		low := strings.ToLower(dep.label)
		if low != "" && !seen[low] {
			seen[low] = true
			deps = append(deps, dep)
		}
	}

	if !matchedAny {
		return nil
	}

	return &parserResolvedCol{
		sqlType: sqlType,
		roleTag: opLabel,
		deps:    deps,
	}
}

func parserBranchToDep(branch googlesql.ASTNode, colName string, res *parserResolvedCol) exprDependency {
	if srcCTE := parserBranchSourceName(branch); srcCTE != "" {
		var via []string
		for _, d := range res.deps {
			via = append(via, d.label)
		}
		return exprDependency{
			kind:       depDerived,
			label:      srcCTE + "." + colName,
			sqlType:    res.sqlType,
			viaSources: via,
		}
	}

	if len(res.deps) > 0 {
		return res.deps[0]
	}

	return exprDependency{
		kind:    depDerived,
		label:   colName,
		sqlType: res.sqlType,
	}
}

func parserBranchSourceName(branch googlesql.ASTNode) string {
	var name string
	walkASTNodes(branch, func(n googlesql.ASTNode) bool {
		if name != "" {
			return false
		}
		tp, ok := n.(*googlesql.ASTTablePathExpression)
		if !ok || isNilNode(tp) {
			return true
		}
		if pe, _ := tp.PathExpr(); !isNilNode(pe) {
			if parts, _ := pe.ToIdentifierVector(); len(parts) > 0 {
				name = parts[len(parts)-1]
				return false
			}
		}

		return true
	})

	return name
}

func parserSetOpLabel(stmtSQL string, so *googlesql.ASTSetOperation) string {
	s, e, ok := astNodeByteRange(so)
	if !ok || e <= s || e > len(stmtSQL) {
		return "UNION"
	}
	upper := strings.ToUpper(stmtSQL[s:e])
	switch {
	case strings.Contains(upper, "UNION ALL"):
		return "UNION ALL"
	case strings.Contains(upper, "UNION DISTINCT"):
		return "UNION DISTINCT"
	case strings.Contains(upper, "INTERSECT ALL"):
		return "INTERSECT ALL"
	case strings.Contains(upper, "INTERSECT DISTINCT"):
		return "INTERSECT DISTINCT"
	case strings.Contains(upper, "EXCEPT ALL"):
		return "EXCEPT ALL"
	case strings.Contains(upper, "EXCEPT DISTINCT"):
		return "EXCEPT DISTINCT"
	default:
		return "UNION"
	}
}
