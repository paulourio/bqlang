package formatter

import (
	"fmt"
	"strings"

	"github.com/goccy/go-googlesql"
)

func (p *printer) visitAlias(n *googlesql.ASTAlias, d printerData) {
	parent := nodeParent(n)
	if !nodeDefined(parent) || nodeKind(parent) != googlesql.ASTNodeKindAstWithOffset {
		p.print(p.keyword("AS"))
	}

	id, _ := n.Identifier()
	p.accept(id, d)
}

func (p *printer) visitClusterBy(n *googlesql.ASTClusterBy, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("CLUSTER"))

	p1 := p.nest()
	p1.print(p1.keyword("BY"))

	exprs := selectChildrenOfType[googlesql.ASTExpressionNode](n)
	printNestedWithSep(p1, exprs, d, ",")
	p.print(p1.unnest())
}

func (p *printer) visitCollate(n *googlesql.ASTCollate, d printerData) {
	p.moveBefore(n)
	cn, _ := n.CollationName()

	p.print(p.keyword("COLLATE"))
	p.accept(cn, d)
}

func (p *printer) visitColumnAttributeList(n *googlesql.ASTColumnAttributeList, d printerData) {
	p.moveBefore(n)

	for _, val := range selectChildrenOfType[googlesql.ASTColumnAttributeNode](n) {
		p.accept(val, d)
	}

	p.movePast(n)
}

func (p *printer) visitConnectionClause(n *googlesql.ASTConnectionClause, d printerData) {
	p.moveBefore(n)
	cp, _ := n.ConnectionPath()

	p.print(p.keyword("CONNECTION"))
	p.accept(cp, d)
}

func (p *printer) visitDescriptor(n *googlesql.ASTDescriptor, d printerData) {
	p.moveBefore(n)
	cols, _ := n.Columns()

	p.print(p.keyword("DESCRIPTOR") + "(")
	p.accept(cols, d)
	p.print(")")
	p.movePast(n)
}

func (p *printer) visitDescriptorColumn(n *googlesql.ASTDescriptorColumn, d printerData) {
	p.moveBefore(n)
	name, _ := n.Name()
	p.accept(name, d)
}

func (p *printer) visitDescriptorColumnList(n *googlesql.ASTDescriptorColumnList, d printerData) {
	p.moveBefore(n)

	for i, c := range selectChildrenOfType[*googlesql.ASTDescriptorColumn](n) {
		if i > 0 {
			p.print(",")
		}

		p.accept(c, d)
	}

	p.movePast(n)
}

func (p *printer) visitFormatClause(n *googlesql.ASTFormatClause, d printerData) {
	p.moveBefore(n)
	f, _ := n.Format()

	p.print(p.keyword("FORMAT"))
	p.accept(f, d)

	if tz, _ := n.TimeZoneExpr(); nodeDefined(tz) {
		p.print(p.keyword("AT TIME ZONE"))
		p.accept(tz, d)
	}
}

func (p *printer) visitForSystemTime(n *googlesql.ASTForSystemTime, d printerData) {
	p.moveBefore(n)
	expr, _ := n.Expression()

	p.println("")
	p.print(p.keyword("FOR SYSTEM_TIME AS OF"))
	p.accept(expr, d)
}

func (p *printer) visitFromClause(n *googlesql.ASTFromClause, d printerData) {
	var count int

	p.moveBefore(n)
	expr, _ := n.TableExpression()

	if nodeKind(expr) == googlesql.ASTNodeKindAstJoin {
		count = countJoins(expr)
		d.joinCount = count
	}

	p.accept(expr, d)

	sel, ok := nodeParent(n).(*googlesql.ASTSelect)
	if !ok {
		return
	}

	w, _ := sel.WhereClause()
	gb, _ := sel.GroupBy()
	h, _ := sel.Having()
	q, _ := sel.Qualify()
	win, _ := sel.WindowClause()

	a, _ := locationRange(w, gb, h, q, win)

	if count >= p.fmt.opts.MinJoinsToSeparateInBlocks {
		p.println("")

		if nodeDefined(w) {
			p.printBlankLine()
		}
	}

	if a > 0 {
		p.moveAt(a)
	}
}

func countJoins(n googlesql.ASTNode) int {
	if nodeKind(n) == googlesql.ASTNodeKindAstJoin {
		return 1 + countJoins(nodeChild(n, 0))
	}

	return 0
}

func (p *printer) visitGroupBy(n *googlesql.ASTGroupBy, d printerData) {
	p.moveBefore(n)
	p.visitGroupByItems(n, d)

	sel, ok := nodeParent(n).(*googlesql.ASTSelect)
	if !ok {
		return
	}

	h, _ := sel.Having()
	q, _ := sel.Qualify()
	win, _ := sel.WindowClause()

	if a, _ := locationRange(h, q, win); a > 0 {
		p.moveAt(a)
	}
}

func (p *printer) visitGroupByItems(n *googlesql.ASTGroupBy, d printerData) {
	hint, _ := n.Hint()
	p.accept(hint, d)

	if andOrder, _ := n.AndOrderBy(); andOrder {
		p.print(p.keyword("AND ORDER"))
	}

	if allNode, _ := n.All(); nodeDefined(allNode) {
		p.print(p.keyword("BY ALL"))

		return
	}

	p.print(p.keyword("BY"))

	pp := p.nest()
	items := selectChildrenOfType[*googlesql.ASTGroupingItem](n)
	printNestedWithSep(pp, items, d, ",")
	p.print(pp.unnest())
}

func (p *printer) visitGroupingItem(n *googlesql.ASTGroupingItem, d printerData) {
	p.moveBefore(n)

	if expr, _ := n.Expression(); nodeDefined(expr) {
		p.accept(expr, d)

		if al, _ := n.Alias(); nodeDefined(al) {
			p.accept(al, d)
		}

		if order, _ := n.GroupingItemOrder(); nodeDefined(order) {
			p.accept(order, d)
		}

		return
	}

	if rollup, _ := n.Rollup(); nodeDefined(rollup) {
		p.accept(rollup, d)

		return
	}

	if cube, _ := n.Cube(); nodeDefined(cube) {
		p.accept(cube, d)

		return
	}

	if gsl, _ := n.GroupingSetList(); nodeDefined(gsl) {
		p.accept(gsl, d)

		return
	}

	p.print("()")
}

func (p *printer) visitGroupingItemOrder(n *googlesql.ASTGroupingItemOrder, d printerData) {
	p.moveBefore(n)
	spec, _ := n.OrderingSpec()
	no, _ := n.NullOrder()
	p.printOrderingSpec(spec, no, d)
}

func (p *printer) printOrderingSpec(
	spec googlesql.ASTOrderingExpressionEnums_OrderingSpec,
	no *googlesql.ASTNullOrder,
	d printerData,
) {
	switch spec {
	case googlesql.ASTOrderingExpressionEnums_OrderingSpecNotSet:
		// Nothing.
	case googlesql.ASTOrderingExpressionEnums_OrderingSpecAsc:
		p.print(p.keyword("ASC"))
	case googlesql.ASTOrderingExpressionEnums_OrderingSpecDesc:
		p.print(p.keyword("DESC"))
	}

	p.accept(no, d)
}

func (p *printer) visitGroupingSetList(n *googlesql.ASTGroupingSetList, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("GROUPING SETS"))
	p.print("(")

	for i, gs := range selectChildrenOfType[*googlesql.ASTGroupingSet](n) {
		if i > 0 {
			p.print(",")
		}

		p.accept(gs, d)
	}

	p.print(")")
}

func (p *printer) visitGroupingSet(n *googlesql.ASTGroupingSet, d printerData) {
	p.moveBefore(n)

	if expr, _ := n.Expression(); nodeDefined(expr) {
		p.accept(expr, d)

		return
	}

	if rollup, _ := n.Rollup(); nodeDefined(rollup) {
		p.accept(rollup, d)

		return
	}

	if cube, _ := n.Cube(); nodeDefined(cube) {
		p.accept(cube, d)

		return
	}

	p.print("()")
}

func (p *printer) visitCube(n *googlesql.ASTCube, d printerData) {
	p.printGroupingFunc("CUBE", n, d)
}

func (p *printer) printGroupingFunc(kw string, n googlesql.ASTNode, d printerData) {
	p.print(p.keyword(kw))
	p.print("(")

	for i, expr := range selectChildrenOfType[googlesql.ASTExpressionNode](n) {
		if i > 0 {
			p.print(",")
		}

		p.accept(expr, d)
	}

	p.print(")")
}

func (p *printer) visitHint(n *googlesql.ASTHint, d printerData) {
	var b strings.Builder

	p.moveBefore(n)

	if shards, _ := n.NumShardsHint(); nodeDefined(shards) {
		p.print("@")
		p.accept(shards, d)
	}

	entries := selectChildrenOfType[*googlesql.ASTHintEntry](n)
	if len(entries) > 0 {
		b.WriteString("@{")

		for i, h := range entries {
			if i > 0 {
				b.WriteString(", ")
			}

			if qual, _ := h.Qualifier(); nodeDefined(qual) {
				b.WriteString(p.toString(qual, d))
				b.WriteString(".")
			}

			name, _ := h.Name()
			val, _ := h.Value()

			b.WriteString(p.toString(name, d))
			b.WriteString("=")
			b.WriteString(p.toString(val, d))
		}

		b.WriteString("}")
		p.print(b.String())
	}
}

func (p *printer) visitHintedStatement(n *googlesql.ASTHintedStatement, d printerData) {
	hint, _ := n.Hint()
	stmt, _ := n.Statement()

	p.accept(hint, d)
	p.println("")
	p.accept(stmt, d)
}

func (p *printer) visitHavingModifier(n *googlesql.ASTHavingModifier, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("HAVING"))

	mk, _ := n.ModifierKind()
	switch mk {
	case googlesql.ASTHavingModifierEnums_ModifierKindNotSet:
		// Nothing.
	case googlesql.ASTHavingModifierEnums_ModifierKindMin:
		p.print(p.keyword("MIN"))
	case googlesql.ASTHavingModifierEnums_ModifierKindMax:
		p.print(p.keyword("MAX"))
	}

	expr, _ := n.Expr()
	p.accept(expr, d)
}

func (p *printer) visitHaving(n *googlesql.ASTHaving, d printerData) {
	p.moveBefore(n)
	expr, _ := n.Expression()
	p.visitMaybeClauseAligned(expr, d)
}

func (p *printer) visitJoin(n *googlesql.ASTJoin, d printerData) {
	count := d.joinCount

	lhs, _ := n.Lhs()
	rhs, _ := n.Rhs()
	hint, _ := n.Hint()
	oc, _ := n.OnClause()
	uc, _ := n.UsingClause()
	jt, _ := n.JoinType()

	pp := p.nest()
	pp.accept(lhs, d)
	pp.movePast(lhs)

	switch jt {
	case googlesql.ASTJoinEnums_JoinTypeComma:
		pp.print(",")
	case googlesql.ASTJoinEnums_JoinTypeDefaultJoinType,
		googlesql.ASTJoinEnums_JoinTypeCross,
		googlesql.ASTJoinEnums_JoinTypeFull,
		googlesql.ASTJoinEnums_JoinTypeInner,
		googlesql.ASTJoinEnums_JoinTypeLeft,
		googlesql.ASTJoinEnums_JoinTypeRight:
		if count >= p.fmt.opts.MinJoinsToSeparateInBlocks {
			pp.println("")
		}

		if jl, _ := n.JoinLocation(); nodeDefined(jl) {
			pp.moveBefore(jl)
		} else {
			pp.moveBefore(n)
		}

		pp.println("\v")
		pp.print(p.keyword(p.joinKeyword(n)))
	}

	pp.accept(hint, d)
	pp.println("")

	pp2 := p.nest()
	pp2.accept(rhs, d)
	pp2.movePast(rhs)
	pp.print(pp2.unnest())

	if nodeDefined(oc) {
		pp.println("")
		pp.acceptNested(oc, d)
	}

	if nodeDefined(uc) {
		pp.println("")
		pp.acceptNested(uc, d)
	}

	p.print(pp.unnestLeft())
}

func (p *printer) joinKeyword(n *googlesql.ASTJoin) string {
	var kw strings.Builder

	kw.Grow(24)

	if nat, _ := n.Natural(); nat {
		kw.WriteString("NATURAL ")
	}

	jt, _ := n.JoinType()
	switch jt {
	case googlesql.ASTJoinEnums_JoinTypeCross:
		kw.WriteString("CROSS ")
	case googlesql.ASTJoinEnums_JoinTypeFull:
		kw.WriteString("FULL ")
	case googlesql.ASTJoinEnums_JoinTypeInner:
		kw.WriteString("INNER ")
	case googlesql.ASTJoinEnums_JoinTypeLeft:
		kw.WriteString("LEFT ")
	case googlesql.ASTJoinEnums_JoinTypeRight:
		kw.WriteString("RIGHT ")
	case googlesql.ASTJoinEnums_JoinTypeDefaultJoinType, googlesql.ASTJoinEnums_JoinTypeComma:
		// Nothing.
	}

	jh, _ := n.JoinHint()
	switch jh {
	case googlesql.ASTJoinEnums_JoinHintNoJoinHint:
		// Nothing.
	case googlesql.ASTJoinEnums_JoinHintHash:
		kw.WriteString("HASH ")
	case googlesql.ASTJoinEnums_JoinHintLookup:
		kw.WriteString("LOOKUP ")
	}

	lhs, _ := n.Lhs()
	rhs, _ := n.Rhs()
	searchStart, _ := nodeByteRange(n)

	if nodeDefined(lhs) && nodeKind(lhs) != googlesql.ASTNodeKindAstPipeJoinLhsPlaceholder {
		_, searchStart = nodeByteRange(lhs)
	}

	end, _ := nodeByteRange(rhs)

	if containsKeywordToken(p.viewErasedInput(searchStart, end), "OUTER") {
		kw.WriteString("OUTER ")
	}

	kw.WriteString("JOIN")

	return kw.String()
}

func (p *printer) visitLimitOffset(n *googlesql.ASTLimitOffset, d printerData) {
	p.print(p.keyword("LIMIT"))

	p2 := p.nest()
	p2.moveBefore(n)

	if hasAll, _ := n.HasLimitAll(); hasAll {
		p2.print(p2.keyword("ALL"))
	} else {
		lim, _ := n.LimitExpression()
		p2.accept(lim, d)
	}

	if os, _ := n.Offset(); nodeDefined(os) {
		p2.print(p2.keyword("OFFSET"))
		p2.accept(os, d)
	}

	p2.moveBeforeSuccessorOf(n)
	p.print(p2.unnest())
}

func (p *printer) visitMaybeClauseAligned(n googlesql.ASTExpressionNode, d printerData) {
	pp := p.nest()

	switch nodeKind(n) {
	case googlesql.ASTNodeKindAstAndExpr:
		pp = p.visitClauseAlignedItems(pp, "AND", selectChildrenOfType[googlesql.ASTExpressionNode](n), d)
	case googlesql.ASTNodeKindAstOrExpr:
		pp = p.visitClauseAlignedItems(pp, "OR", selectChildrenOfType[googlesql.ASTExpressionNode](n), d)
	default:
		pp.accept(n, d)
	}

	pp.moveBeforeSuccessorOf(n)
	p.print(pp.unnest())
}

func (p *printer) visitClauseAlignedItems(
	pp *printer,
	kw string,
	items []googlesql.ASTExpressionNode,
	d printerData,
) *printer {
	for i, item := range items {
		if i > 0 {
			if p.fmt.opts.AlignLogicalWithClauses {
				p.print(pp.unnest())
				p.printClause(p.keyword(kw))
				pp = p.nest()
			} else {
				pp.printClause(pp.keyword(kw))
			}
		}

		pp.acceptNested(item, d)
	}

	return pp
}

func (p *printer) visitModelClause(n *googlesql.ASTModelClause, d printerData) {
	p.moveBefore(n)
	mp, _ := n.ModelPath()

	p.print(p.keyword("MODEL"))
	p.accept(mp, d)
}

func (p *printer) visitNullOrder(n *googlesql.ASTNullOrder, _ printerData) {
	if nf, _ := n.NullsFirst(); nf {
		p.print(p.keyword("NULLS FIRST"))
	} else {
		p.print(p.keyword("NULLS LAST"))
	}
}

func (p *printer) visitOnClause(n *googlesql.ASTOnClause, d printerData) {
	expr, _ := n.Expression()
	p1 := p.nest()
	p1.printClause(p1.keyword("ON"))
	p1.moveBefore(n)
	p1.accept(expr, d)
	p.print(p1.unnestLeft())
}

func (p *printer) visitOptionsList(n *googlesql.ASTOptionsList, d printerData) {
	p.printOptionsListBody(n, "OPTIONS", d)
}

func (p *printer) printOptionsListBody(n *googlesql.ASTOptionsList, kwPrefix string, d printerData) {
	if !nodeDefined(n) {
		return
	}

	entries := selectChildrenOfType[*googlesql.ASTOptionsEntry](n)
	simple := len(entries) <= 1 && p.isSimpleOptionsList(n)
	d.optionsSimple = simple

	pp := p.nest()
	if kwPrefix != "" {
		pp.print(pp.keyword(kwPrefix) + " (")
	} else {
		pp.print("(")
	}

	if !simple {
		pp.println("")
		pp.incDepth()
	}

	pp.moveBefore(n)
	p1 := pp.nest()

	for i, e := range entries {
		if i > 0 {
			p1.print(",")

			if !simple {
				p1.println("")
			}
		}

		p1.accept(e, d)
	}

	pp.print(p1.unnestLeft())

	if !simple {
		pp.println("")
		pp.decDepth()
	}

	pp.print(")")
	p.print(pp.unnest())
}

func (p *printer) visitOptionsEntry(n *googlesql.ASTOptionsEntry, d printerData) {
	var keys stringMapSet

	if ol, ok := nodeParent(n).(*googlesql.ASTOptionsList); ok {
		keys = knownOptionKeys(ol)
	}

	simple := d.optionsSimple
	name, _ := n.Name()
	val, _ := n.Value()

	pp := p.nest()
	key := keys.Get(pp.toString(name, d))
	value := pp.toUnnestedString(val, d)

	if simple {
		pp.print(key + "=" + value)
	} else {
		pp.print(key + " \v= " + strings.ReplaceAll(value, "\n", "\n\v"))
	}

	p.print(pp.String())
}

func (p *printer) visitOrderBy(n *googlesql.ASTOrderBy, d printerData) {
	p.moveBefore(n)

	if nodeKind(nodeParent(n)) == googlesql.ASTNodeKindAstQuery {
		p.printClause(p.keyword("ORDER"))
	} else {
		p.print(p.keyword("ORDER"))
	}

	hint, _ := n.Hint()
	exprs := selectChildrenOfType[*googlesql.ASTOrderingExpression](n)

	p1 := p.nest()
	p1.accept(hint, d)
	p1.print(p1.keyword("BY"))
	p1.moveBefore(n)
	printNestedWithSep(p1, exprs, d, ",")
	p1.moveBeforeSuccessorOf(n)
	p.print(p1.unnest())
}

func (p *printer) visitOrderingExpression(n *googlesql.ASTOrderingExpression, d printerData) {
	p.moveBefore(n)

	expr, _ := n.Expression()
	co, _ := n.Collate()
	spec, _ := n.OrderingSpec()
	no, _ := n.NullOrder()

	p.accept(expr, d)
	p.accept(co, d)
	p.printOrderingSpec(spec, no, d)

	if opt, _ := n.OptionList(); nodeDefined(opt) {
		p.accept(opt, d)
	}

	p.movePast(n)
}

func (p *printer) visitParenthesizedJoin(n *googlesql.ASTParenthesizedJoin, d printerData) {
	p.moveBefore(n)
	j, _ := n.Join()
	postOps := selectChildrenOfType[googlesql.ASTPostfixTableOperatorNode](n)

	p.println("(")
	p.incDepth()
	p.accept(j, d)
	p.decDepth()
	p.println("")
	p.print(")")

	for _, op := range postOps {
		p.accept(op, d)
	}
}

func (p *printer) visitPartitionBy(n *googlesql.ASTPartitionBy, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("PARTITION"))

	hint, _ := n.Hint()
	exprs := selectChildrenOfType[googlesql.ASTExpressionNode](n)

	p1 := p.nest()
	p1.accept(hint, d)
	p1.print(p1.keyword("BY"))
	printNestedWithSep(p1, exprs, d, ",")
	p.print(p1.unnest())
}

func (p *printer) visitPivotClause(n *googlesql.ASTPivotClause, d printerData) {
	p.moveBefore(n)
	el, _ := n.PivotExpressions()
	oa, _ := n.OutputAlias()

	p.println("")
	p.print(p.keyword("PIVOT") + " (")
	p.println("")
	p.incDepth()
	p.accept(el, d)
	p.println("")
	p.visitPivotForExpression(n, d)
	p.println("")
	p.decDepth()
	p.print(")")
	p.accept(oa, d)
	p.movePast(n)
}

func (p *printer) visitPivotExpression(n *googlesql.ASTPivotExpression, d printerData) {
	p.moveBefore(n)
	expr, _ := n.Expression()
	alias, _ := n.Alias()

	p.accept(expr, d)
	p.accept(alias, d)
	p.movePast(n)
}

func (p *printer) visitPivotForExpression(n *googlesql.ASTPivotClause, d printerData) {
	forExpr, _ := n.ForExpression()
	pvl, _ := n.PivotValues()

	simpleLHS := p.isSimpleExpr(forExpr)
	simpleValues := simpleLHS && p.isSimplePivotValueList(pvl)
	ctx := d
	ctx.pivotForValuesSimple = simpleValues

	p.print(p.keyword("FOR"))

	if !simpleLHS {
		p.println("")
		p.incDepth()
	}

	p.accept(forExpr, ctx)

	if !simpleLHS {
		p.println("")
		p.decDepth()
	}

	p.print(p.keyword("IN") + " (")

	if !simpleValues {
		p.println("")
		p.incDepth()
	}

	p.accept(pvl, ctx)

	if !simpleValues {
		p.println("")
		p.decDepth()
	}

	p.print(")")
}

func (p *printer) visitPivotExpressionList(n *googlesql.ASTPivotExpressionList, d printerData) {
	p.moveBefore(n)
	exprs := selectChildrenOfType[*googlesql.ASTPivotExpression](n)
	simple := p.isSimplePivotExpressionList(n)

	for i, e := range exprs {
		if i > 0 {
			p.print(",")

			if !simple {
				p.println("")
			}
		}

		p.acceptNested(e, d)
	}

	p.movePast(n)
}

func (p *printer) visitPivotValue(n *googlesql.ASTPivotValue, d printerData) {
	p.moveBefore(n)
	val, _ := n.Value()
	alias, _ := n.Alias()

	p.accept(val, d)
	p.accept(alias, d)
	p.movePast(n)
}

func (p *printer) visitPivotValueList(n *googlesql.ASTPivotValueList, d printerData) {
	p.moveBefore(n)

	simple := d.pivotForValuesSimple

	for i, v := range selectChildrenOfType[*googlesql.ASTPivotValue](n) {
		if i > 0 {
			p.print(",")

			if !simple {
				p.println("")
			}
		}

		p.accept(v, d)
	}

	p.movePast(n)
}

func (p *printer) visitQualify(n *googlesql.ASTQualify, d printerData) {
	p.moveBefore(n)
	expr, _ := n.Expression()
	p.visitMaybeClauseAligned(expr, d)
}

func (p *printer) visitQuery(n *googlesql.ASTQuery, d printerData) {
	nestedWith := withInsideWith(n)
	if nestedWith {
		p.incDepth()
	}

	wc, _ := n.WithClause()
	qe, _ := n.QueryExpr()
	ob, _ := n.OrderBy()
	lo, _ := n.LimitOffset()
	pipes := selectChildrenOfType[googlesql.ASTPipeOperatorNode](n)
	parens := p.queryParenCount(n)

	p.moveBefore(n)

	for range parens {
		p.print("(")
		p.println("")
		p.incDepth()
	}

	p.accept(wc, d)
	p.accept(qe, d)

	if nodeDefined(ob) {
		p.println("")
		p.accept(ob, d)
	}

	if nodeDefined(lo) {
		p.println("")
		p.accept(lo, d)
	}

	if len(pipes) > 0 {
		p.printPipeOperatorList(pipes, d, true)
	}

	if parent := nodeParent(n); nodeDefined(parent) && nodeKind(parent) != googlesql.ASTNodeKindAstQueryStatement {
		p.movePast(n)
	}

	if nestedWith {
		p.decDepth()
	}

	for range parens {
		p.println("")
		p.decDepth()
		p.print(")")
	}
}

func (p *printer) queryNeedsParen(n *googlesql.ASTQuery, parent googlesql.ASTNode) bool {
	if p.isParenNeeded(n) {
		return true
	}

	nested, _ := n.IsNested()
	if !nested {
		return false
	}

	if !nodeDefined(parent) {
		return true
	}

	k := nodeKind(parent)

	return k != googlesql.ASTNodeKindAstTableSubquery &&
		k != googlesql.ASTNodeKindAstAliasedQueryExpression
}

func (p *printer) queryParenCount(n *googlesql.ASTQuery) int {
	parent := nodeParent(n)
	if !p.queryNeedsParen(n, parent) {
		return 0
	}

	start, end := nodeByteRange(n)
	minStart := 0
	maxEnd := len(p.erasedInput)
	parentOwnsOne := false

	if nodeDefined(parent) {
		minStart, maxEnd = nodeByteRange(parent)
		switch nodeKind(parent) {
		case googlesql.ASTNodeKindAstTableSubquery,
			googlesql.ASTNodeKindAstExpressionSubquery,
			googlesql.ASTNodeKindAstInExpression,
			googlesql.ASTNodeKindAstLikeExpression,
			googlesql.ASTNodeKindAstQuantifiedComparisonExpression,
			googlesql.ASTNodeKindAstForInStatement,
			googlesql.ASTNodeKindAstAliasedQuery,
			googlesql.ASTNodeKindAstAliasedQueryExpression:
			parentOwnsOne = true
		default:
			parentOwnsOne = false
		}
	}

	openCount := 0

	for i := start - 1; i >= minStart && i < len(p.erasedInput); i-- {
		ch := p.erasedInput[i]
		if ch == '(' {
			openCount++
		} else if ch != ' ' && ch != '\t' && ch != '\n' && ch != '\r' {
			break
		}
	}

	closeCount := 0

	for i := end; i >= 0 && i < maxEnd && i < len(p.erasedInput); i++ {
		ch := p.erasedInput[i]
		if ch == ')' {
			closeCount++
		} else if ch != ' ' && ch != '\t' && ch != '\n' && ch != '\r' {
			break
		}
	}

	count := min(openCount, closeCount)
	if parentOwnsOne && count > 0 {
		count--
	}

	if count < 1 {
		return 1
	}

	return count
}

func withInsideWith(n *googlesql.ASTQuery) bool {
	wc, _ := n.WithClause()
	if !nodeDefined(wc) {
		return false
	}

	parent := nodeParent(n)
	if !nodeDefined(parent) {
		return false
	}

	switch nodeKind(parent) {
	case googlesql.ASTNodeKindAstWithClauseEntry:
		return true
	case googlesql.ASTNodeKindAstAliasedQuery:
		return nodeKind(nodeParent(parent)) == googlesql.ASTNodeKindAstWithClauseEntry
	default:
		return false
	}
}

func (p *printer) visitQueryStatement(n *googlesql.ASTQueryStatement, d printerData) {
	p.moveBefore(n)
	q, _ := n.Query()
	p1 := p.nest()
	p1.accept(q, d)
	p.print(p1.unnest())
}

func (p *printer) visitRepeatableClause(n *googlesql.ASTRepeatableClause, d printerData) {
	p.moveBefore(n)
	arg, _ := n.Argument()

	p.print(p.keyword("REPEATABLE"))
	p.print("(")
	p.accept(arg, d)
	p.print(")")
}

func (p *printer) visitRollup(n *googlesql.ASTRollup, d printerData) {
	p.printGroupingFunc("ROLLUP", n, d)
}

func (p *printer) visitSampleClause(n *googlesql.ASTSampleClause, d printerData) {
	p.moveBefore(n)
	method, _ := n.SampleMethod()
	size, _ := n.SampleSize()
	suffix, _ := n.SampleSuffix()

	p.println("")
	p.print(p.keyword("TABLESAMPLE"))
	p.print(p.rawKeywordIdent(method) + " ")
	p.print("(")
	p.accept(size, d)
	p.print(")")
	p.accept(suffix, d)
}

func (p *printer) visitSampleSize(n *googlesql.ASTSampleSize, d printerData) {
	p.moveBefore(n)
	size, _ := n.Size()
	unit, _ := n.Unit()
	pb, _ := n.PartitionBy()

	p.accept(size, d)

	switch unit {
	case googlesql.ASTSampleSizeEnums_UnitNotSet:
		// Nothing.
	case googlesql.ASTSampleSizeEnums_UnitRows:
		p.print(p.keyword("ROWS"))
	case googlesql.ASTSampleSizeEnums_UnitPercent:
		p.print(p.keyword("PERCENT"))
	}

	p.accept(pb, d)
}

func (p *printer) visitSampleSuffix(n *googlesql.ASTSampleSuffix, d printerData) {
	p.moveBefore(n)
	w, _ := n.Weight()
	r, _ := n.Repeat()

	p.accept(w, d)
	p.accept(r, d)
}

func (p *printer) visitScript(n *googlesql.ASTScript, d printerData) {
	num := nodeNumChildren(n)
	for i := range num {
		c := nodeChild(n, i)
		p.moveBefore(c)
		p.accept(c, d)
	}
}

func (p *printer) visitSelect(n *googlesql.ASTSelect, d printerData) {
	p.moveBefore(n)

	pp := p.nest()
	pp.printOpenParenIfNeeded(n)

	pp2 := pp.nest()
	pp2.printClause(pp2.keyword("SELECT"))

	hint, _ := n.Hint()
	wm, _ := n.WithModifier()
	distinct, _ := n.Distinct()
	sa, _ := n.SelectAs()
	sl, _ := n.SelectList()
	fc, _ := n.FromClause()
	w, _ := n.WhereClause()
	gb, _ := n.GroupBy()
	h, _ := n.Having()
	q, _ := n.Qualify()
	win, _ := n.WindowClause()

	pp3 := pp2.nest()
	pp3.accept(hint, d)
	pp3.accept(wm, d)

	singleLine := p.maybeSingleLineColumns(n)
	d.singleLineCols = singleLine

	if distinct {
		pp3.print(pp3.keyword("DISTINCT"))

		if !nodeDefined(sa) && !singleLine {
			pp3.println("")
		}
	} else if p.needsSelectAllAfterWithModifier(wm, sa, sl) {
		pp3.print(pp3.keyword("ALL"))

		if !nodeDefined(sa) && !singleLine {
			pp3.println("")
		}
	}

	pp3.accept(sa, d)
	pp3.accept(sl, d)

	if nodeDefined(fc) {
		pp3.moveBefore(fc)
	}

	pp2.print(pp3.unnest())

	if nodeDefined(fc) {
		pp2.printClause(pp2.keyword("FROM"))
		pp2.acceptNested(fc, d)
	}

	if nodeDefined(w) {
		pp2.lnaccept(w, d)
	}

	if nodeDefined(gb) {
		pp2.moveBefore(gb)
		pp2.printClause(pp2.keyword("GROUP") + " ")
		pp2.acceptNested(gb, d)
	}

	if nodeDefined(h) {
		pp2.moveBefore(h)
		pp2.printClause(pp2.keyword("HAVING"))
		pp2.accept(h, d)
	}

	if nodeDefined(q) {
		pp2.moveBefore(q)
		pp2.printClause(pp2.keyword("QUALIFY"))
		pp2.accept(q, d)
	}

	if nodeDefined(win) {
		pp2.moveBefore(win)
		pp2.printClause(pp2.keyword("WINDOW"))
		pp2.acceptNested(win, d)
	}

	k := nodeKind(nodeParent(n))
	if k == googlesql.ASTNodeKindAstQuery || k == googlesql.ASTNodeKindAstSetOperation {
		pp.print(pp2.String())
		p.print(pp.String())
	} else {
		pp.print(pp2.unnest())
		pp.printCloseParenIfNeeded(n)
		pp.println("")
		p.print(pp.unnest())
	}
}

func (p *printer) visitWithModifier(n *googlesql.ASTWithModifier, d printerData) {
	p.moveBefore(n)
	id, _ := n.Identifier()
	opts, _ := n.Options()

	p.print(p.keyword("WITH"))
	p.print(p.rawKeywordIdent(id))
	p.accept(opts, d)
}

func (p *printer) needsSelectAllAfterWithModifier(
	wm *googlesql.ASTWithModifier,
	sa *googlesql.ASTSelectAs,
	sl *googlesql.ASTSelectList,
) bool {
	if !nodeDefined(wm) || nodeDefined(sa) || !nodeDefined(sl) {
		return false
	}

	if opts, _ := wm.Options(); nodeDefined(opts) {
		return false
	}

	s := strings.TrimLeft(p.nodeErasedInput(sl), " \t\n\r")
	end := 0

	for end < len(s) && isIdentChar(s[end]) {
		end++
	}

	return strings.EqualFold(s[:end], "OPTIONS")
}

func isIdentChar(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func (p *printer) visitSelectAs(n *googlesql.ASTSelectAs, d printerData) {
	mode, _ := n.AsMode()
	switch mode {
	case googlesql.ASTSelectAsEnums_AsModeNotSet:
		// Nothing.
	case googlesql.ASTSelectAsEnums_AsModeStruct:
		p.println(p.keyword("AS STRUCT"))
	case googlesql.ASTSelectAsEnums_AsModeValue:
		p.print(p.keyword("AS VALUE"))
	case googlesql.ASTSelectAsEnums_AsModeTypeName:
		tn, _ := n.TypeName()

		p.print(p.keyword("AS"))
		p.accept(tn, d)
	}

	p.println("")
}

func (p *printer) visitSelectColumn(n *googlesql.ASTSelectColumn, d printerData) {
	expr, _ := n.Expression()
	alias, _ := n.Alias()
	order, _ := n.GroupingItemOrder()

	pp := p.nest()
	pp.accept(expr, d)
	p.print(pp.unnest())

	if nodeDefined(alias) {
		pp = p.nest()
		pp.accept(alias, d)
		p.print(pp.unnest())
	}

	if nodeDefined(order) {
		p.accept(order, d)
	}
}

func (p *printer) visitSelectList(n *googlesql.ASTSelectList, d printerData) {
	pp := p.nest()
	singleLine := d.singleLineCols
	cols := selectChildrenOfType[*googlesql.ASTSelectColumn](n)

	var prev googlesql.ASTNode

	for i, c := range cols {
		if i > 0 {
			pp.print(",")
			pp.movePastLine(prev)

			if !singleLine {
				pp.println("")
			}
		}

		pp.moveBefore(c)
		pp.acceptNestedString(c, d)
		prev = c
	}

	if !singleLine && len(cols) > 0 &&
		p.fmt.opts.ColumnListTrailingComma == Always &&
		p.allowsSelectListTrailingComma(n, cols) {
		pp.print(",")
	}

	pp.movePastLine(prev)
	p.print(pp.unnestLeft())
}

func (p *printer) allowsSelectListTrailingComma(
	n *googlesql.ASTSelectList,
	cols []*googlesql.ASTSelectColumn,
) bool {
	parent := nodeParent(n)
	switch nodeKind(parent) {
	case googlesql.ASTNodeKindAstTransformClause,
		googlesql.ASTNodeKindAstReturningClause:
		return true
	case googlesql.ASTNodeKindAstSelect:
		_, isGQL := nodeParent(parent).(googlesql.ASTGqlOperatorNode)

		return !isGQL && !p.isStandaloneTemplateSelect(parent, cols)
	default:
		return false
	}
}

func (p *printer) isStandaloneTemplateSelect(
	parent googlesql.ASTNode,
	cols []*googlesql.ASTSelectColumn,
) bool {
	if len(cols) != 1 || !strings.HasPrefix(p.nodeInput(cols[0]), "__bqfmt_tmpl_") {
		return false
	}

	sel, ok := parent.(*googlesql.ASTSelect)
	if !ok {
		return false
	}

	fc, _ := sel.FromClause()

	return !nodeDefined(fc)
}

func (p *printer) printSetOperationHeader(meta *googlesql.ASTSetOperationMetadata, d printerData) {
	if !nodeDefined(meta) {
		return
	}

	var propMode googlesql.ASTSetOperationEnums_ColumnPropagationMode
	if cpm, _ := meta.ColumnPropagationMode(); nodeDefined(cpm) {
		propMode, _ = cpm.Value()
	}

	switch propMode {
	case googlesql.ASTSetOperationEnums_ColumnPropagationModeInner:
		p.print(p.keyword("INNER"))
	case googlesql.ASTSetOperationEnums_ColumnPropagationModeLeft:
		p.print(p.keyword("LEFT"))
	case googlesql.ASTSetOperationEnums_ColumnPropagationModeFull:
		p.print(p.keyword("FULL"))
	case googlesql.ASTSetOperationEnums_ColumnPropagationModeStrict:
		// STRICT is emitted after ALL/DISTINCT and before CORRESPONDING.
	}

	opNode, _ := meta.OpType()
	opVal := googlesql.ASTSetOperationEnums_OperationTypeNotSet

	if nodeDefined(opNode) {
		opVal, _ = opNode.Value()
	}

	switch opVal {
	case googlesql.ASTSetOperationEnums_OperationTypeUnion:
		p.print(p.keyword("UNION"))
	case googlesql.ASTSetOperationEnums_OperationTypeExcept:
		p.print(p.keyword("EXCEPT"))
	case googlesql.ASTSetOperationEnums_OperationTypeIntersect:
		p.print(p.keyword("INTERSECT"))
	case googlesql.ASTSetOperationEnums_OperationTypeNotSet:
		p.addError(&PrinterError{Msg: "unset set operation type", Node: meta, Input: &p.input})
	}

	p.print("\v")

	if hint, _ := meta.Hint(); nodeDefined(hint) {
		p.accept(hint, d)
	}

	if aod, _ := meta.AllOrDistinct(); nodeDefined(aod) {
		val, _ := aod.Value()
		switch val {
		case googlesql.ASTSetOperationEnums_AllOrDistinctDistinct:
			p.print(p.keyword("DISTINCT"))
		case googlesql.ASTSetOperationEnums_AllOrDistinctAll:
			p.print(p.keyword("ALL"))
		default:
			// Unset.
		}
	}

	if propMode == googlesql.ASTSetOperationEnums_ColumnPropagationModeStrict {
		p.print(p.keyword("STRICT"))
	}

	if cmm, _ := meta.ColumnMatchMode(); nodeDefined(cmm) {
		val, _ := cmm.Value()
		switch val {
		case googlesql.ASTSetOperationEnums_ColumnMatchModeCorresponding:
			p.print(p.keyword("CORRESPONDING"))
		case googlesql.ASTSetOperationEnums_ColumnMatchModeCorrespondingBy:
			p.print(p.keyword("CORRESPONDING BY") + " ")
		case googlesql.ASTSetOperationEnums_ColumnMatchModeByName:
			p.print(p.keyword("BY NAME"))
		case googlesql.ASTSetOperationEnums_ColumnMatchModeByNameOn:
			p.print(p.keyword("BY NAME ON"))
		case googlesql.ASTSetOperationEnums_ColumnMatchModeByPosition:
			// Default.
		}
	}

	if cbcl, _ := meta.CorrespondingByColumnList(); nodeDefined(cbcl) {
		p.accept(cbcl, d)
	}
}

func (p *printer) visitSetOperation(n *googlesql.ASTSetOperation, d printerData) {
	p.printOpenParenIfNeeded(n)

	metaListNode, _ := n.Metadata()

	var metas []*googlesql.ASTSetOperationMetadata

	if nodeDefined(metaListNode) {
		metas = selectChildrenOfType[*googlesql.ASTSetOperationMetadata](metaListNode)
	}

	inputs := selectChildrenOfType[googlesql.ASTQueryExpressionNode](n)

	for i, query := range inputs {
		if i > 0 {
			if i-1 < len(metas) {
				p.printSetOperationHeader(metas[i-1], d)
			}

			p.println("")
		}

		p.accept(query, d)
		p.println("")
	}

	p.printCloseParenIfNeeded(n)
}

func (p *printer) visitStatementList(n *googlesql.ASTStatementList, d printerData) {
	var prev googlesql.ASTNode

	num := nodeNumChildren(n)
	p.moveBefore(n)

	for i := range num {
		c := nodeChild(n, i)
		curr := nodeKind(c)

		if i > 0 {
			p.print(";")
			p.movePastLine(prev)
			p.println("")

			if !canGroupStatements(nodeKind(prev), curr) {
				p.printBlankLine()
			}
		}

		p.acceptNested(c, d)
		p.movePast(c)
		prev = c
	}

	parent := nodeParent(n)
	topLevel := !nodeDefined(parent) || nodeKind(parent) == googlesql.ASTNodeKindAstScript

	if num > 1 || (num > 0 && !topLevel) {
		p.println(";")

		if nodeDefined(prev) {
			p.movePastLine(prev)
		}
	}

	p.movePastLine(n)
}

func canGroupStatements(last, curr googlesql.ASTNodeKind) bool {
	if curr != last {
		return false
	}

	return curr == googlesql.ASTNodeKindAstVariableDeclaration ||
		curr == googlesql.ASTNodeKindAstSingleAssignment
}

func (p *printer) visitTableElementList(n *googlesql.ASTTableElementList, d printerData) {
	elems := selectChildrenOfType[googlesql.ASTTableElementNode](n)
	p.moveBefore(n)

	if len(elems) == 0 {
		p.movePast(n)

		return
	}

	p.println("")
	p.print("(")
	p.println("")
	p.incDepth()

	pp := p.nest()

	var prev googlesql.ASTNode

	for i, e := range elems {
		if i > 0 {
			pp.print(",")
			pp.movePastLine(prev)
			pp.println("")
		}

		pp.accept(e, d)
		prev = e
	}

	p.print(pp.unnestLeft())
	p.println("")
	p.decDepth()
	p.print(")")
	p.movePast(n)
}

func (p *printer) visitTablePathExpression(n *googlesql.ASTTablePathExpression, d printerData) {
	pe, _ := n.PathExpr()
	ue, _ := n.UnnestExpr()
	hint, _ := n.Hint()
	alias, _ := n.Alias()
	wo, _ := n.WithOffset()
	fst, _ := n.ForSystemTime()
	postOps := selectChildrenOfType[googlesql.ASTPostfixTableOperatorNode](n)

	p.accept(pe, d)
	p.accept(ue, d)
	p.accept(hint, d)
	p.accept(alias, d)
	p.accept(wo, d)
	p.accept(fst, d)

	for _, op := range postOps {
		p.accept(op, d)
	}
}

func (p *printer) visitTableSubquery(n *googlesql.ASTTableSubquery, d printerData) {
	p.moveBefore(n)

	if lat, _ := n.IsLateral(); lat {
		p.print(p.keyword("LATERAL"))
	}

	sub, _ := n.Subquery()
	alias, _ := n.Alias()
	postOps := selectChildrenOfType[googlesql.ASTPostfixTableOperatorNode](n)

	p.println("(")
	p.incDepth()
	p.acceptNested(sub, d)
	p.println("")
	p.decDepth()
	p.print(")")
	p.accept(alias, d)

	for _, op := range postOps {
		p.accept(op, d)
	}
}

func (p *printer) visitTableClause(n *googlesql.ASTTableClause, d printerData) {
	p.moveBefore(n)
	tp, _ := n.TablePath()
	tvf, _ := n.Tvf()

	p.print(p.keyword("TABLE"))
	p.accept(tp, d)
	p.accept(tvf, d)
}

func (p *printer) visitTVFArgument(n *googlesql.ASTTVFArgument, d printerData) {
	p.moveBefore(n)
	expr, _ := n.Expr()
	tc, _ := n.TableClause()
	mc, _ := n.ModelClause()
	cc, _ := n.ConnectionClause()
	desc, _ := n.Descriptor()

	p.accept(expr, d)
	p.accept(tc, d)
	p.accept(mc, d)
	p.accept(cc, d)
	p.accept(desc, d)
	p.movePast(n)
}

func (p *printer) visitTVF(n *googlesql.ASTTVF, d printerData) {
	p.moveBefore(n)

	if lat, _ := n.IsLateral(); lat {
		p.print(p.keyword("LATERAL"))
	}

	name, _ := n.Name()
	args := selectChildrenOfType[*googlesql.ASTTVFArgument](n)
	simple := p.areSimpleTVFArguments(args)

	p.print(p.function(p.toString(name, d)) + "(")

	if !simple {
		p.println("")
		p.incDepth()
	}

	for i, e := range args {
		if i > 0 {
			p.print(",")

			if !simple {
				p.println("")
			}
		}

		p.accept(e, d)
		p.movePast(e)
	}

	hint, _ := n.Hint()
	alias, _ := n.Alias()
	postOps := selectChildrenOfType[googlesql.ASTPostfixTableOperatorNode](n)

	tailNodes := make([]googlesql.ASTNode, 0, len(postOps)+2)
	tailNodes = append(tailNodes, hint, alias)

	for _, op := range postOps {
		tailNodes = append(tailNodes, op)
	}

	if b, e := locationRange(tailNodes...); e > 0 {
		p.fmt.flushCommentsUpTo(b)
	} else {
		p.movePast(n)
	}

	if !simple {
		p.println("")
		p.decDepth()
	}

	p.print(")")
	p.accept(hint, d)
	p.accept(alias, d)

	for _, op := range postOps {
		p.accept(op, d)
	}

	p.movePast(n)
}

func (p *printer) visitUnpivotClause(n *googlesql.ASTUnpivotClause, d printerData) {
	p.moveBefore(n)
	p.println("")

	nf, _ := n.NullFilter()
	switch nf {
	case googlesql.ASTUnpivotClauseEnums_NullFilterKUnspecified:
		p.println(p.keyword("UNPIVOT") + " (")
	case googlesql.ASTUnpivotClauseEnums_NullFilterKInclude:
		p.println(p.keyword("UNPIVOT INCLUDE NULLS") + " (")
	case googlesql.ASTUnpivotClauseEnums_NullFilterKExclude:
		p.println(p.keyword("UNPIVOT EXCLUDE NULLS") + " (")
	}

	inItems, _ := n.UnpivotInItems()
	valCols, _ := n.UnpivotOutputValueColumns()
	nameCol, _ := n.UnpivotOutputNameColumn()
	oa, _ := n.OutputAlias()

	simple := isSimpleUnpivotInItemList(inItems)
	d.unpivotInItemSimple = simple

	p.incDepth()
	p.accept(valCols, d)
	p.println("")
	p.print(p.keyword("FOR"))
	p.accept(nameCol, d)
	p.print(p.keyword("IN") + " (")

	if !simple {
		p.println("")
		p.incDepth()
	}

	p.accept(inItems, d)

	if !simple {
		p.println("")
		p.decDepth()
	}

	p.println(")")
	p.decDepth()
	p.print(")")
	p.accept(oa, d)
	p.movePast(n)
}

func (p *printer) visitUnpivotInItem(n *googlesql.ASTUnpivotInItem, d printerData) {
	p.moveBefore(n)
	cols, _ := n.UnpivotColumns()
	alias, _ := n.Alias()

	p.accept(cols, d)
	p.accept(alias, d)
	p.movePast(n)
}

func (p *printer) visitUnpivotInItemLabel(n *googlesql.ASTUnpivotInItemLabel, d printerData) {
	p.moveBefore(n)

	if label, _ := n.Label(); nodeDefined(label) {
		p.print(p.keyword("AS"))
		p.accept(label, d)
	}

	p.movePast(n)
}

func (p *printer) visitUnpivotInItemList(n *googlesql.ASTUnpivotInItemList, d printerData) {
	p.moveBefore(n)

	simple := d.unpivotInItemSimple

	for i, item := range selectChildrenOfType[*googlesql.ASTUnpivotInItem](n) {
		if i > 0 {
			p.print(",")

			if !simple {
				p.println("")
			}
		}

		p.accept(item, d)
	}

	p.movePast(n)
}

func (p *printer) visitUsingClause(n *googlesql.ASTUsingClause, d printerData) {
	p.moveBefore(n)
	p.printClause(p.keyword("USING") + " (")

	keys := selectChildrenOfType[*googlesql.ASTIdentifier](n)
	printNestedWithSep(p, keys, d, ",")
	p.print(")")
}

func (p *printer) visitWhereClause(n *googlesql.ASTWhereClause, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("WHERE"))

	e, _ := n.Expression()
	if nodeKind(nodeParent(n)) == googlesql.ASTNodeKindAstFunctionCall {
		p.acceptNested(e, d)

		return
	}

	p.visitWhereExprBody(e, d)
}

func (p *printer) visitWhereExprBody(e googlesql.ASTExpressionNode, d printerData) {
	switch nodeKind(e) {
	case googlesql.ASTNodeKindAstAndExpr, googlesql.ASTNodeKindAstOrExpr:
		d.alignBinaryOpBudget = 1
		p.accept(e, d)
	default:
		p.acceptNested(e, d)
	}
}

func (p *printer) visitWindowClause(n *googlesql.ASTWindowClause, d printerData) {
	p.moveBefore(n)

	for i, w := range selectChildrenOfType[*googlesql.ASTWindowDefinition](n) {
		if i > 0 {
			p.println(",")
		}

		name, _ := w.Name()
		ws, _ := w.WindowSpec()

		p.accept(name, d)
		p.print(p.keyword("AS") + " ")

		count := countWindowSpecElems(ws)

		p.print("(")

		if count > 0 {
			p.println("")
			p.incDepth()
			p.accept(ws, d)
			p.println("")
			p.decDepth()
		}

		p.print(")")
	}

	p.moveBeforeSuccessorOf(n)
}

func (p *printer) visitWindowFrame(n *googlesql.ASTWindowFrame, d printerData) {
	p.moveBefore(n)

	fu, _ := n.FrameUnit()
	switch fu {
	case googlesql.ASTWindowFrameEnums_FrameUnitRows:
		p.print(p.keyword("ROWS"))
	case googlesql.ASTWindowFrameEnums_FrameUnitRange:
		p.print(p.keyword("RANGE"))
	default:
		p.addError(&PrinterError{
			Msg:   fmt.Sprintf("unknown frame unit id %d", int(fu)),
			Node:  n,
			Input: &p.input,
		})
	}

	startExpr, _ := n.StartExpr()
	endExpr, _ := n.EndExpr()
	pp := p.nest()

	if nodeDefined(endExpr) {
		pp.print(p.keyword("BETWEEN"))
		pp.accept(startExpr, d)
		pp.print(p.keyword("AND"))
		pp.accept(endExpr, d)
	} else {
		pp.accept(startExpr, d)
	}

	p.print(pp.unnest())
}

func (p *printer) visitWindowFrameExpr(n *googlesql.ASTWindowFrameExpr, d printerData) {
	p.moveBefore(n)

	bt, _ := n.BoundaryType()
	expr, _ := n.Expression()

	switch bt {
	case googlesql.ASTWindowFrameExprEnums_BoundaryTypeUnboundedPreceding:
		p.print(p.keyword("UNBOUNDED PRECEDING"))
	case googlesql.ASTWindowFrameExprEnums_BoundaryTypeOffsetPreceding:
		p.accept(expr, d)
		p.print(p.keyword("PRECEDING"))
	case googlesql.ASTWindowFrameExprEnums_BoundaryTypeCurrentRow:
		p.print(p.keyword("CURRENT ROW"))
	case googlesql.ASTWindowFrameExprEnums_BoundaryTypeOffsetFollowing:
		p.accept(expr, d)
		p.print(p.keyword("FOLLOWING"))
	case googlesql.ASTWindowFrameExprEnums_BoundaryTypeUnboundedFollowing:
		p.print(p.keyword("UNBOUNDED FOLLOWING"))
	}
}

func (p *printer) visitWindowSpecification(n *googlesql.ASTWindowSpecification, d printerData) {
	p.moveBefore(n)
	pp := p.nest()

	wn, _ := n.BaseWindowName()
	pb, _ := n.PartitionBy()
	ob, _ := n.OrderBy()
	wf, _ := n.WindowFrame()

	if nodeDefined(wn) {
		pp.accept(wn, d)
		pp.println("")
	}

	pp2 := pp.nest()

	if nodeDefined(pb) {
		pp2.accept(pb, d)
	}

	if nodeDefined(ob) {
		if nodeDefined(pb) {
			pp2.println("")
		}

		pp2.accept(ob, d)
	}

	if nodeDefined(wf) {
		if nodeDefined(pb) || nodeDefined(ob) {
			pp2.println("")
		}

		pp2.accept(wf, d)
	}

	pp.print(pp2.unnest())
	pp.movePast(n)
	p.print(pp.unnest())
}

func (p *printer) visitWithClause(n *googlesql.ASTWithClause, d printerData) {
	p.moveBefore(n)
	p.println("")

	if rec, _ := n.Recursive(); rec {
		p.println(p.keyword("WITH RECURSIVE"))
	} else {
		p.println(p.keyword("WITH"))
	}

	if p.fmt.opts.IndentWithClause {
		p.incDepth()
	}

	for i, e := range selectChildrenOfType[*googlesql.ASTWithClauseEntry](n) {
		if i > 0 {
			p.println(",")
		}

		p.accept(e, d)
	}

	p.println("")

	if p.fmt.opts.IndentWithClause {
		p.decDepth()
	}

	p.movePast(n)
}

func (p *printer) visitWithClauseEntry(n *googlesql.ASTWithClauseEntry, d printerData) {
	p.moveBefore(n)

	if aq, _ := n.AliasedQuery(); nodeDefined(aq) {
		p.visitAliasedQuery(aq, d)
	}

	p.movePast(n)
}

func (p *printer) visitAliasedQuery(n *googlesql.ASTAliasedQuery, d printerData) {
	p.moveBefore(n)
	alias, _ := n.Alias()
	q, _ := n.Query()

	p.accept(alias, d)
	p.println(p.keyword("AS") + " (")

	if p.fmt.opts.IndentWithEntries {
		p.incDepth()
	}

	p.accept(q, d)
	p.println("")

	if p.fmt.opts.IndentWithEntries {
		p.decDepth()
	}

	p.print(")")

	if mods, _ := n.Modifiers(); nodeDefined(mods) {
		p.printAliasedQueryModifiers(mods, d)
	}

	p.movePast(n)
}

func (p *printer) printAliasedQueryModifiers(n *googlesql.ASTAliasedQueryModifiers, d printerData) {
	p.moveBefore(n)

	if rdm, _ := n.RecursionDepthModifier(); nodeDefined(rdm) {
		p.printRecursionDepthModifier(rdm, d)
	}

	p.movePast(n)
}

func (p *printer) printRecursionDepthModifier(n *googlesql.ASTRecursionDepthModifier, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("WITH DEPTH"))

	if alias, _ := n.Alias(); nodeDefined(alias) {
		p.accept(alias, d)
	}

	lb, _ := n.LowerBound()
	ub, _ := n.UpperBound()

	var lbExpr, ubExpr googlesql.ASTExpressionNode
	if nodeDefined(lb) {
		lbExpr, _ = lb.Bound()
	}

	if nodeDefined(ub) {
		ubExpr, _ = ub.Bound()
	}

	switch {
	case !nodeDefined(lbExpr) && !nodeDefined(ubExpr):
		// Unbounded default: plain `WITH DEPTH [AS alias]`.
	case !nodeDefined(lbExpr) && nodeDefined(ubExpr):
		p.print(p.keyword("MAX"))
		p.accept(ubExpr, d)
	default:
		p.print(p.keyword("BETWEEN"))
		p.printIntOrUnbounded(lb, d)
		p.print(p.keyword("AND"))
		p.printIntOrUnbounded(ub, d)
	}

	p.movePast(n)
}

func (p *printer) printIntOrUnbounded(n *googlesql.ASTIntOrUnbounded, d printerData) {
	if !nodeDefined(n) {
		p.print(p.keyword("UNBOUNDED"))

		return
	}

	p.moveBefore(n)

	if bound, _ := n.Bound(); nodeDefined(bound) {
		p.accept(bound, d)
	} else {
		p.print(p.keyword("UNBOUNDED"))
	}

	p.movePast(n)
}

func (p *printer) visitWithOffset(n *googlesql.ASTWithOffset, d printerData) {
	p.moveBefore(n)
	alias, _ := n.Alias()

	p.print(p.keyword("WITH OFFSET"))
	p.accept(alias, d)
}

func (p *printer) visitWithWeight(n *googlesql.ASTWithWeight, d printerData) {
	p.moveBefore(n)
	alias, _ := n.Alias()

	p.print(p.keyword("WITH WEIGHT"))
	p.accept(alias, d)
}

func (p *printer) visitWithReportModifier(n *googlesql.ASTWithReportModifier, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("WITH REPORT"))

	if ol, _ := n.OptionsList(); nodeDefined(ol) {
		p.printOptionsListBody(ol, "", d)
	}
}

func (p *printer) visitUnnestExpressionWithOptAliasAndOffset(
	n *googlesql.ASTUnnestExpressionWithOptAliasAndOffset,
	d printerData,
) {
	p.moveBefore(n)
	ue, _ := n.UnnestExpression()
	al, _ := n.OptionalAlias()
	wo, _ := n.OptionalWithOffset()

	p.accept(ue, d)
	p.accept(al, d)
	p.accept(wo, d)
	p.movePast(n)
}

func (p *printer) visitAliasedQueryExpression(n *googlesql.ASTAliasedQueryExpression, d printerData) {
	p.moveBefore(n)

	q, _ := n.Query()
	alias, _ := n.Alias()

	p.println("(")
	p.incDepth()
	p.acceptNested(q, d)
	p.println("")
	p.decDepth()
	p.print(")")
	p.accept(alias, d)
	p.movePast(n)
}
