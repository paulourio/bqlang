package formatter

import (
	"strings"

	"github.com/goccy/go-googlesql"
)

func (p *printer) visitCreatePropertyGraphStatement(
	n *googlesql.ASTCreatePropertyGraphStatement,
	d printerData,
) {
	p.moveBefore(n)
	p.print(p.keyword(createStatementKeywords(n, false, "PROPERTY GRAPH")))

	name, _ := n.Name()
	nt, _ := n.NodeTableList()
	et, _ := n.EdgeTableList()
	opt, _ := n.OptionsList()

	p.accept(name, d)
	p.println("")
	p.incDepth()

	if nodeDefined(nt) {
		p.println(p.keyword("NODE TABLES") + " (")
		p.incDepth()
		p.accept(nt, d)
		p.println("")
		p.decDepth()

		if nodeDefined(et) {
			p.println(")")
		} else {
			p.print(")")
		}
	}

	if nodeDefined(et) {
		p.println(p.keyword("EDGE TABLES") + " (")
		p.incDepth()
		p.accept(et, d)
		p.println("")
		p.decDepth()
		p.print(")")
	}

	p.decDepth()

	if nodeDefined(opt) {
		p.println("")
		p.printlnBlock(alignNested(p.toString(opt, d)))
	}

	p.movePast(n)
}

func (p *printer) visitGraphElementTableList(n *googlesql.ASTGraphElementTableList, d printerData) {
	p.moveBefore(n)

	tables := selectChildrenOfType[*googlesql.ASTGraphElementTable](n)
	for i, tbl := range tables {
		if i > 0 {
			p.println(",")
		}

		p.accept(tbl, d)
	}

	p.movePast(n)
}

func (p *printer) visitGraphElementTable(n *googlesql.ASTGraphElementTable, d printerData) {
	p.moveBefore(n)

	name, _ := n.Name()
	al, _ := n.Alias()
	p.accept(name, d)
	p.accept(al, d)

	p.incDepth()
	p.printGraphElementTableBody(n, d)
	p.decDepth()

	p.movePast(n)
}

func (p *printer) printGraphElementTableBody(n *googlesql.ASTGraphElementTable, d printerData) {
	if kl, _ := n.KeyList(); nodeDefined(kl) {
		p.println("")
		p.print(p.keyword("KEY") + " " + p.toAlignedString(kl, d))
	}

	if src, _ := n.SourceNodeReference(); nodeDefined(src) {
		p.println("")
		p.accept(src, d)
	}

	if dst, _ := n.DestNodeReference(); nodeDefined(dst) {
		p.println("")
		p.accept(dst, d)
	}

	if defOpt, _ := n.DefaultLabelOptionsList(); nodeDefined(defOpt) {
		p.println("")
		p.print(p.toAlignedString(defOpt, d))
	}

	if lpl, _ := n.LabelPropertiesList(); hasExplicitSourceSpan(lpl) {
		p.println("")
		p.accept(lpl, d)
	}

	if dl, _ := n.DynamicLabel(); nodeDefined(dl) {
		p.println("")
		p.accept(dl, d)
	}

	if dp, _ := n.DynamicProperties(); nodeDefined(dp) {
		p.println("")
		p.accept(dp, d)
	}
}

func hasExplicitSourceSpan(n googlesql.ASTNode) bool {
	if !nodeDefined(n) {
		return false
	}

	b, e := nodeByteRange(n)

	return b < e
}

func (p *printer) visitGraphDynamicLabel(n *googlesql.ASTGraphDynamicLabel, d printerData) {
	lbl, _ := n.Label()
	p.printGraphDynamicClause(n, "DYNAMIC LABEL", lbl, d)
}

func (p *printer) visitGraphDynamicProperties(n *googlesql.ASTGraphDynamicProperties, d printerData) {
	props, _ := n.Properties()
	p.printGraphDynamicClause(n, "DYNAMIC PROPERTIES", props, d)
}

func (p *printer) printGraphDynamicClause(n googlesql.ASTNode, kw string, expr googlesql.ASTNode, d printerData) {
	if !nodeDefined(expr) {
		return
	}

	p.moveBefore(n)
	p.print(p.keyword(kw) + " (" + p.toAlignedString(expr, d) + ")")
	p.movePast(n)
}

func (p *printer) visitGraphNodeTableReference(n *googlesql.ASTGraphNodeTableReference, d printerData) {
	p.moveBefore(n)

	rt, _ := n.NodeReferenceType()
	switch rt {
	case googlesql.ASTGraphNodeTableReferenceEnums_NodeReferenceTypeSource:
		p.print(p.keyword("SOURCE KEY") + " ")
	case googlesql.ASTGraphNodeTableReferenceEnums_NodeReferenceTypeDestination:
		p.print(p.keyword("DESTINATION KEY") + " ")
	case googlesql.ASTGraphNodeTableReferenceEnums_NodeReferenceTypeNodeReferenceTypeUnspecified:
		p.addError(&PrinterError{Msg: "unset graph node reference type", Node: n, Input: &p.input})
	}

	etc, _ := n.EdgeTableColumns()
	nti, _ := n.NodeTableIdentifier()
	ntc, _ := n.NodeTableColumns()

	p.print(p.toAlignedString(etc, d))
	p.print(p.keyword("REFERENCES"))

	ntiStr := p.toTrimmedString(nti, d)
	if nodeDefined(ntc) {
		ntiStr += " " + p.toAlignedString(ntc, d)
	}

	p.print(ntiStr)

	p.movePast(n)
}

func (p *printer) visitGraphElementLabelAndPropertiesList(
	n *googlesql.ASTGraphElementLabelAndPropertiesList,
	d printerData,
) {
	items := selectChildrenOfType[*googlesql.ASTGraphElementLabelAndProperties](n)
	printed := 0

	for _, item := range items {
		if !hasExplicitSourceSpan(item) {
			continue
		}

		if printed > 0 {
			p.println("")
		}

		p.accept(item, d)

		printed++
	}
}

func (p *printer) visitGraphElementLabelAndProperties(
	n *googlesql.ASTGraphElementLabelAndProperties,
	d printerData,
) {
	ln, _ := n.LabelName()
	lo, _ := n.LabelOptionsList()
	props, _ := n.Properties()

	hasLabelHeader := false

	switch {
	case nodeDefined(ln):
		p.print(p.keyword("LABEL"))
		p.accept(ln, d)

		hasLabelHeader = true
	case nodeDefined(lo) || !hasExplicitSourceSpan(props) || startsBeforeNode(n, props):
		p.print(p.keyword("DEFAULT LABEL"))

		hasLabelHeader = true
	}

	if nodeDefined(lo) {
		p.print(p.toAlignedString(lo, d))
	}

	if hasExplicitSourceSpan(props) {
		if hasLabelHeader {
			p.println("")
		}

		p.accept(props, d)
	}
}

func startsBeforeNode(outer, inner googlesql.ASTNode) bool {
	if !nodeDefined(outer) || !nodeDefined(inner) {
		return false
	}

	bOuter, _ := nodeByteRange(outer)
	bInner, _ := nodeByteRange(inner)

	return bOuter < bInner
}

func (p *printer) visitGraphProperties(n *googlesql.ASTGraphProperties, d printerData) {
	noProps, _ := n.NoProperties()
	if noProps {
		p.print(p.keyword("NO PROPERTIES"))

		return
	}

	dpl, _ := n.DerivedPropertyList()
	if !nodeDefined(dpl) {
		p.print(p.keyword("PROPERTIES ALL COLUMNS"))

		if exc, _ := n.AllExceptColumns(); nodeDefined(exc) {
			p.print(p.keyword("EXCEPT") + " " + p.toAlignedString(exc, d))
		}

		return
	}

	p.accept(dpl, d)
}

func (p *printer) visitGraphDerivedPropertyList(n *googlesql.ASTGraphDerivedPropertyList, d printerData) {
	props := selectChildrenOfType[*googlesql.ASTGraphDerivedProperty](n)
	parts := make([]string, 0, len(props))
	singleLine := len(props) <= 6

	for _, prop := range props {
		rendered := p.toAlignedString(prop, d)
		if strings.Contains(rendered, "\n") {
			singleLine = false
		}

		parts = append(parts, rendered)
	}

	joined := strings.Join(parts, ", ")
	if singleLine && len(joined) <= 68 {
		p.print(p.keyword("PROPERTIES") + " (" + joined + ")")

		return
	}

	p.println(p.keyword("PROPERTIES") + " (")
	p.incDepth()

	for i, part := range parts {
		if i < len(parts)-1 {
			p.printlnBlock(part + ",")
		} else {
			p.printlnBlock(part)
		}
	}

	p.decDepth()
	p.print(")")
}

func (p *printer) visitGraphDerivedProperty(n *googlesql.ASTGraphDerivedProperty, d printerData) {
	p.moveBefore(n)

	if expr, _ := n.Expression(); nodeDefined(expr) {
		p.accept(expr, d)
	}

	if al, _ := n.Alias(); nodeDefined(al) {
		p.accept(al, d)
	}

	if opt, _ := n.OptionsList(); nodeDefined(opt) {
		p.print(p.toAlignedString(opt, d))
	}

	p.movePast(n)
}

func (p *printer) visitGraphTableQuery(n *googlesql.ASTGraphTableQuery, d printerData) {
	p.moveBefore(n)
	p.println(p.keyword("GRAPH_TABLE") + " (")
	p.incDepth()

	if ref, _ := n.GraphReference(); nodeDefined(ref) {
		p.println(p.toTrimmedString(ref, d))
	}

	if op, _ := n.GraphOp(); nodeDefined(op) {
		p.accept(op, d)
	}

	if shape, _ := n.GraphTableShape(); nodeDefined(shape) {
		ctx := d
		ctx.singleLineCols = false
		p.println(p.keyword("COLUMNS") + " (")
		p.incDepth()
		p.printlnBlock(strings.TrimSpace(leftAlignNested(p.toString(shape, ctx))))
		p.decDepth()
		p.println(")")
	}

	p.decDepth()
	p.print(")")

	if al, _ := n.Alias(); nodeDefined(al) {
		p.accept(al, d)
	}

	for _, postOp := range selectChildrenOfType[googlesql.ASTPostfixTableOperatorNode](n) {
		p.accept(postOp, d)
	}

	p.movePast(n)
}

func (p *printer) visitGqlQuery(n *googlesql.ASTGqlQuery, d printerData) {
	gt, _ := n.GraphTable()
	if !nodeDefined(gt) {
		return
	}

	ref, _ := gt.GraphReference()
	p.printGqlGraphHeader(ref, d)

	if op, _ := gt.GraphOp(); nodeDefined(op) {
		p.accept(op, d)
	}
}

func (p *printer) visitGqlGraphPatternQuery(n *googlesql.ASTGqlGraphPatternQuery, d printerData) {
	ref, _ := n.GraphReference()
	p.printGqlGraphHeader(ref, d)

	if gp, _ := n.GraphPattern(); nodeDefined(gp) {
		p.accept(gp, d)
	}
}

func (p *printer) visitGqlLinearOpsQuery(n *googlesql.ASTGqlLinearOpsQuery, d printerData) {
	ref, _ := n.GraphReference()
	p.printGqlGraphHeader(ref, d)

	if ops, _ := n.LinearOps(); nodeDefined(ops) {
		p.accept(ops, d)
	}
}

func (p *printer) printGqlGraphHeader(ref *googlesql.ASTPathExpression, d printerData) {
	if nodeDefined(ref) {
		p.println(p.keyword("GRAPH") + " " + p.toTrimmedString(ref, d))
	}
}

func (p *printer) visitGqlOperatorList(n *googlesql.ASTGqlOperatorList, d printerData) {
	ops := selectChildrenOfType[googlesql.ASTGqlOperatorNode](n)
	if len(ops) == 0 {
		return
	}

	if isCompositeOrLinearGqlOp(ops[0]) {
		for i, op := range ops {
			if i > 0 {
				p.println(p.keyword("NEXT"))
			}

			p.accept(op, d)
		}

		return
	}

	for _, op := range ops {
		p.accept(op, d)
	}
}

func isCompositeOrLinearGqlOp(op googlesql.ASTGqlOperatorNode) bool {
	switch op.(type) {
	case *googlesql.ASTGqlOperatorList, *googlesql.ASTGqlSetOperation:
		return true
	default:
		return false
	}
}

func (p *printer) visitGqlSetOperation(n *googlesql.ASTGqlSetOperation, d printerData) {
	inputs := selectChildrenOfType[googlesql.ASTGqlOperatorNode](n)
	metaList, _ := n.Metadata()

	var metas []*googlesql.ASTSetOperationMetadata
	if nodeDefined(metaList) {
		metas = selectChildrenOfType[*googlesql.ASTSetOperationMetadata](metaList)
	}

	for i, inp := range inputs {
		if i > 0 && i-1 < len(metas) {
			pp := p.nest()
			pp.printSetOperationHeader(metas[i-1], d)
			p.println(strings.TrimSpace(alignNested(pp.String())))
		}

		p.accept(inp, d)
	}
}

func (p *printer) visitGqlMatch(n *googlesql.ASTGqlMatch, d printerData) {
	opt, _ := n.Optional()
	hint, _ := n.Hint()
	gp, _ := n.GraphPattern()

	kw := "MATCH"
	if opt {
		kw = "OPTIONAL MATCH"
	}

	header := p.keyword(kw)
	if nodeDefined(hint) {
		header += " " + p.toTrimmedString(hint, d)
	}

	if !nodeDefined(gp) {
		p.println(header)

		return
	}

	paths := selectChildrenOfType[*googlesql.ASTGraphPathPattern](gp)
	if len(paths) == 1 {
		pathStr := p.formatGraphPathPattern(paths[0], d)
		p.printlnBlock(header + " " + pathStr)
	} else if len(paths) > 1 {
		p.println(header)
		p.incDepth()

		for i, path := range paths {
			suffix := ""
			if i < len(paths)-1 {
				suffix = ","
			}

			p.printlnBlock(p.formatGraphPathPattern(path, d) + suffix)
		}

		p.decDepth()
	}

	if wc, _ := gp.WhereClause(); nodeDefined(wc) {
		p.printlnBlock(alignNested(p.toString(wc, d)))
	}
}

func (p *printer) visitGraphPattern(n *googlesql.ASTGraphPattern, d printerData) {
	paths := selectChildrenOfType[*googlesql.ASTGraphPathPattern](n)
	for i, path := range paths {
		suffix := ""
		if i < len(paths)-1 {
			suffix = ","
		}

		p.printlnBlock(p.formatGraphPathPattern(path, d) + suffix)
	}

	if wc, _ := n.WhereClause(); nodeDefined(wc) {
		p.printlnBlock(alignNested(p.toString(wc, d)))
	}
}

func (p *printer) visitGqlReturn(n *googlesql.ASTGqlReturn, d printerData) {
	sel, _ := n.Select()
	if nodeDefined(sel) {
		p.printGqlProjection(p.keyword("RETURN"), sel, d)
	}

	if obp, _ := n.OrderByPage(); nodeDefined(obp) {
		p.accept(obp, d)
	}
}

func (p *printer) visitGqlWith(n *googlesql.ASTGqlWith, d printerData) {
	sel, _ := n.Select()
	if nodeDefined(sel) {
		p.printGqlProjection(p.keyword("WITH"), sel, d)
	}
}

func (p *printer) printGqlProjection(kw string, sel *googlesql.ASTSelect, d printerData) {
	header := kw

	if hint, _ := sel.Hint(); nodeDefined(hint) {
		header += " " + p.toTrimmedString(hint, d)
	}

	if distinct, _ := sel.Distinct(); distinct {
		header += " " + p.keyword("DISTINCT")
	}

	if sl, _ := sel.SelectList(); nodeDefined(sl) {
		cols := selectChildrenOfType[*googlesql.ASTSelectColumn](sl)
		ctx := d
		ctx.singleLineCols = len(cols) <= 1

		slStr := strings.TrimSpace(leftAlignNested(p.toString(sl, ctx)))
		p.printPrefixedBlock(header+" ", slStr)
	} else {
		p.println(header)
	}

	if gb, _ := sel.GroupBy(); nodeDefined(gb) {
		gbStr := strings.TrimSpace(leftAlignNested(p.toString(gb, d)))
		p.printlnBlock(p.keyword("GROUP") + " " + gbStr)
	}
}

func (p *printer) printPrefixedBlock(prefix, block string) {
	pad := strings.Repeat(" ", len(prefix))
	first := true

	for line := range strings.SplitSeq(block, "\n") {
		if first {
			p.println(prefix + line)
			first = false
		} else {
			p.println(pad + line)
		}
	}
}

func (p *printer) visitGqlLet(n *googlesql.ASTGqlLet, d printerData) {
	vdl, _ := n.VariableDefinitionList()
	if !nodeDefined(vdl) {
		return
	}

	p.accept(vdl, d)
}

func (p *printer) visitGqlLetVariableDefinitionList(
	n *googlesql.ASTGqlLetVariableDefinitionList,
	d printerData,
) {
	defs := selectChildrenOfType[*googlesql.ASTGqlLetVariableDefinition](n)
	if len(defs) == 0 {
		return
	}

	prefix := p.keyword("LET") + " "
	if len(defs) == 1 {
		p.printlnBlock(prefix + p.toAlignedString(defs[0], d))

		return
	}

	pp := p.nest()

	for i, def := range defs {
		if i > 0 {
			pp.println(",")
		}

		id, _ := def.Identifier()
		expr, _ := def.Expression()
		idStr := pp.toTrimmedString(id, d)
		exprStr := pp.toAlignedString(expr, d)
		pp.print(idStr + " \v= " + strings.ReplaceAll(exprStr, "\n", "\n\v"))
	}

	p.printPrefixedBlock(prefix, leftAlignNested(pp.String()))
}

func (p *printer) visitGqlLetVariableDefinition(n *googlesql.ASTGqlLetVariableDefinition, d printerData) {
	id, _ := n.Identifier()
	expr, _ := n.Expression()
	p.print(p.toTrimmedString(id, d) + " = " + p.toAlignedString(expr, d))
}

func (p *printer) visitGqlFilter(n *googlesql.ASTGqlFilter, d printerData) {
	cond, _ := n.Condition()
	if !nodeDefined(cond) {
		return
	}

	if startsBeforeNode(n, cond) {
		p.printPrefixedBlock(p.keyword("FILTER")+" ", p.toAlignedString(cond, d))

		return
	}

	expr, _ := cond.Expression()
	p.printPrefixedBlock(p.keyword("FILTER")+" ", p.toAlignedString(expr, d))
}

func (p *printer) visitGqlFor(n *googlesql.ASTGqlFor, d printerData) {
	id, _ := n.Identifier()
	expr, _ := n.Expression()
	wo, _ := n.WithOffset()

	line := p.keyword("FOR") + " " +
		p.toTrimmedString(id, d) + " " +
		p.keyword("IN") + " " +
		p.toAlignedString(expr, d)

	if nodeDefined(wo) {
		line += " " + p.keyword("WITH OFFSET")
		if al, _ := wo.Alias(); nodeDefined(al) {
			alID, _ := al.Identifier()
			line += " " + p.keyword("AS") + " " + p.toTrimmedString(alID, d)
		}
	}

	p.printlnBlock(line)
}

func (p *printer) visitGqlInlineSubqueryCall(n *googlesql.ASTGqlInlineSubqueryCall, d printerData) {
	header := p.formatGqlCallPrefix(n.ASTGqlCallBase, d)
	p.println(header + " {")
	p.incDepth()

	if sub, _ := n.Subquery(); nodeDefined(sub) {
		p.accept(sub, d)
	}

	p.decDepth()
	p.println("}")
}

func (p *printer) visitGqlNamedCall(n *googlesql.ASTGqlNamedCall, d printerData) {
	line := p.formatGqlCallPrefix(n.ASTGqlCallBase, d)

	if tvf, _ := n.TvfCall(); nodeDefined(tvf) {
		line += " " + p.toAlignedString(tvf, d)
	}

	if yc, _ := n.YieldClause(); nodeDefined(yc) {
		line += " " + p.toAlignedString(yc, d)
	}

	p.printlnBlock(line)
}

func (p *printer) formatGqlCallPrefix(base *googlesql.ASTGqlCallBase, d printerData) string {
	var parts []string

	if opt, _ := base.Optional(); opt {
		parts = append(parts, p.keyword("OPTIONAL"))
	}

	parts = append(parts, p.keyword("CALL"))

	if part, _ := base.IsPartitioning(); part {
		parts = append(parts, p.keyword("PER"))
	}

	if nc, _ := base.NameCaptureList(); nodeDefined(nc) {
		parts = append(parts, "("+p.toAlignedString(nc, d)+")")
	}

	return strings.Join(parts, " ")
}

func (p *printer) visitYieldItemList(n *googlesql.ASTYieldItemList, d printerData) {
	items := selectChildrenOfType[*googlesql.ASTExpressionWithOptAlias](n)
	parts := make([]string, 0, len(items))

	for _, item := range items {
		parts = append(parts, p.toAlignedString(item, d))
	}

	p.print(p.keyword("YIELD") + " " + strings.Join(parts, ", "))
}

func (p *printer) visitExpressionWithOptAlias(n *googlesql.ASTExpressionWithOptAlias, d printerData) {
	p.moveBefore(n)

	if expr, _ := n.Expression(); nodeDefined(expr) {
		p.accept(expr, d)
	}

	if al, _ := n.OptionalAlias(); nodeDefined(al) {
		p.accept(al, d)
	}

	p.movePast(n)
}

func (p *printer) visitGqlOrderByAndPage(n *googlesql.ASTGqlOrderByAndPage, d printerData) {
	if ob, _ := n.OrderBy(); nodeDefined(ob) {
		p.printlnBlock(p.toAlignedString(ob, d))
	}

	if pg, _ := n.Page(); nodeDefined(pg) {
		p.accept(pg, d)
	}
}

func (p *printer) visitGqlPage(n *googlesql.ASTGqlPage, d printerData) {
	if off, _ := n.Offset(); nodeDefined(off) {
		p.accept(off, d)
	}

	if lim, _ := n.Limit(); nodeDefined(lim) {
		p.accept(lim, d)
	}
}

func (p *printer) visitGqlPageLimit(n *googlesql.ASTGqlPageLimit, d printerData) {
	lim, _ := n.Limit()
	p.println(p.keyword("LIMIT") + " " + p.toAlignedString(lim, d))
}

func (p *printer) visitGqlPageOffset(n *googlesql.ASTGqlPageOffset, d printerData) {
	off, _ := n.Offset()
	p.println(p.keyword("OFFSET") + " " + p.toAlignedString(off, d))
}

func (p *printer) visitGqlSample(n *googlesql.ASTGqlSample, d printerData) {
	if sc, _ := n.Sample(); nodeDefined(sc) {
		p.printlnBlock(p.toAlignedString(sc, d))
	}
}

func (p *printer) visitGraphPathPattern(n *googlesql.ASTGraphPathPattern, d printerData) {
	p.print(p.formatGraphPathPattern(n, d))
}

func (p *printer) formatGraphPathPattern(n *googlesql.ASTGraphPathPattern, d printerData) string {
	var b strings.Builder

	if hint, _ := n.Hint(); nodeDefined(hint) {
		b.WriteString(p.toTrimmedString(hint, d))
		b.WriteByte(' ')
	}

	core := p.formatGraphPathCore(n, d)
	paren := isExplicitlyParenthesizedGraphPath(n)

	if paren {
		b.WriteByte('(')
		b.WriteString(core)

		if wc, _ := n.WhereClause(); nodeDefined(wc) {
			expr, _ := wc.Expression()
			b.WriteByte(' ')
			b.WriteString(p.keyword("WHERE"))
			b.WriteByte(' ')
			b.WriteString(p.toAlignedString(expr, d))
		}

		b.WriteByte(')')
	} else {
		b.WriteString(core)
	}

	if q, _ := n.Quantifier(); nodeDefined(q) {
		b.WriteString(p.formatQuantifier(q, d))
	}

	return b.String()
}

func isExplicitlyParenthesizedGraphPath(n *googlesql.ASTGraphPathPattern) bool {
	paren, _ := n.Parenthesized()
	if !paren {
		return false
	}

	if wc, _ := n.WhereClause(); nodeDefined(wc) {
		return true
	}

	if pm, _ := n.PathMode(); nodeDefined(pm) {
		return true
	}

	if sp, _ := n.SearchPrefix(); nodeDefined(sp) {
		return true
	}

	if pn, _ := n.PathName(); nodeDefined(pn) {
		return true
	}

	q, _ := n.Quantifier()
	elems := selectChildrenOfType[googlesql.ASTGraphPathBaseNode](n)

	if nodeDefined(q) && len(elems) == 1 {
		if _, isEdge := elems[0].(*googlesql.ASTGraphEdgePattern); isEdge {
			return false
		}
	}

	return true
}

func (p *printer) formatGraphPathCore(n *googlesql.ASTGraphPathPattern, d printerData) string {
	var b strings.Builder

	if pn, _ := n.PathName(); nodeDefined(pn) {
		b.WriteString(p.toTrimmedString(pn, d))
		b.WriteString(" = ")
	}

	if sp, _ := n.SearchPrefix(); nodeDefined(sp) {
		b.WriteString(p.formatGraphPathSearchPrefix(sp, d))
	}

	if pm, _ := n.PathMode(); nodeDefined(pm) {
		b.WriteString(p.formatGraphPathMode(pm))
	}

	for _, elem := range selectChildrenOfType[googlesql.ASTGraphPathBaseNode](n) {
		switch e := elem.(type) {
		case *googlesql.ASTGraphNodePattern:
			b.WriteString(p.formatGraphNodePattern(e, d))
		case *googlesql.ASTGraphEdgePattern:
			b.WriteString(p.formatGraphEdgePattern(e, d))
		case *googlesql.ASTGraphPathPattern:
			b.WriteString(p.formatGraphPathPattern(e, d))
		}
	}

	return b.String()
}

func (p *printer) visitGraphPathSearchPrefix(n *googlesql.ASTGraphPathSearchPrefix, d printerData) {
	p.print(p.formatGraphPathSearchPrefix(n, d))
}

func (p *printer) formatGraphPathSearchPrefix(n *googlesql.ASTGraphPathSearchPrefix, d printerData) string {
	t, _ := n.Type()
	cnt, _ := n.PathCount()
	cntStr := ""

	if nodeDefined(cnt) {
		expr, _ := cnt.PathCount()
		cntStr = p.toAlignedString(expr, d)
	}

	switch t {
	case googlesql.ASTGraphPathSearchPrefixEnums_PathSearchPrefixTypeShortest:
		if cntStr == "" {
			return p.keyword("ANY SHORTEST") + " "
		}

		return p.keyword("SHORTEST") + " " + cntStr + " "
	case googlesql.ASTGraphPathSearchPrefixEnums_PathSearchPrefixTypeAny:
		if cntStr == "" {
			return p.keyword("ANY") + " "
		}

		return p.keyword("ANY") + " " + cntStr + " "
	case googlesql.ASTGraphPathSearchPrefixEnums_PathSearchPrefixTypeCheapest:
		if cntStr == "" {
			return p.keyword("ANY CHEAPEST") + " "
		}

		return p.keyword("CHEAPEST") + " " + cntStr + " "
	case googlesql.ASTGraphPathSearchPrefixEnums_PathSearchPrefixTypeAll:
		return p.keyword("ALL") + " "
	case googlesql.ASTGraphPathSearchPrefixEnums_PathSearchPrefixTypeAllShortest:
		return p.keyword("ALL SHORTEST") + " "
	case googlesql.ASTGraphPathSearchPrefixEnums_PathSearchPrefixTypeAllCheapest:
		return p.keyword("ALL CHEAPEST") + " "
	case googlesql.ASTGraphPathSearchPrefixEnums_PathSearchPrefixTypePathSearchPrefixTypeUnspecified:
		return ""
	}

	return ""
}

func (p *printer) visitGraphPathSearchPrefixCount(
	n *googlesql.ASTGraphPathSearchPrefixCount,
	d printerData,
) {
	if cnt, _ := n.PathCount(); nodeDefined(cnt) {
		p.accept(cnt, d)
	}
}

func (p *printer) visitGraphPathMode(n *googlesql.ASTGraphPathMode, _ printerData) {
	p.print(p.formatGraphPathMode(n))
}

func (p *printer) formatGraphPathMode(n *googlesql.ASTGraphPathMode) string {
	pm, _ := n.PathMode()
	switch pm {
	case googlesql.ASTGraphPathModeEnums_PathModeWalk:
		return p.keyword("WALK") + " "
	case googlesql.ASTGraphPathModeEnums_PathModeTrail:
		return p.keyword("TRAIL") + " "
	case googlesql.ASTGraphPathModeEnums_PathModeSimple:
		return p.keyword("SIMPLE") + " "
	case googlesql.ASTGraphPathModeEnums_PathModeAcyclic:
		return p.keyword("ACYCLIC") + " "
	case googlesql.ASTGraphPathModeEnums_PathModePathModeUnspecified:
		return ""
	}

	return ""
}

func (p *printer) visitGraphNodePattern(n *googlesql.ASTGraphNodePattern, d printerData) {
	p.print(p.formatGraphNodePattern(n, d))
}

func (p *printer) formatGraphNodePattern(n *googlesql.ASTGraphNodePattern, d printerData) string {
	filler, _ := n.Filler()
	if !nodeDefined(filler) {
		return "()"
	}

	return "(" + p.formatGraphElementFiller(filler, d) + ")"
}

func (p *printer) visitGraphEdgePattern(n *googlesql.ASTGraphEdgePattern, d printerData) {
	p.print(p.formatGraphEdgePattern(n, d))
}

func (p *printer) formatGraphEdgePattern(n *googlesql.ASTGraphEdgePattern, d printerData) string {
	var b strings.Builder

	if lh, _ := n.LhsHint(); nodeDefined(lh) {
		if h, _ := lh.Hint(); nodeDefined(h) {
			b.WriteString(p.toTrimmedString(h, d))
		}
	}

	left, right, abbr := edgePatternDelimiters(n)
	if filler, _ := n.Filler(); nodeDefined(filler) {
		b.WriteString(left)
		b.WriteString(p.formatGraphElementFiller(filler, d))
		b.WriteString(right)
	} else {
		b.WriteString(abbr)
	}

	if rh, _ := n.RhsHint(); nodeDefined(rh) {
		if h, _ := rh.Hint(); nodeDefined(h) {
			b.WriteString(p.toTrimmedString(h, d))
		}
	}

	if q, _ := n.Quantifier(); nodeDefined(q) {
		b.WriteString(p.formatQuantifier(q, d))
	}

	return b.String()
}

func edgePatternDelimiters(n *googlesql.ASTGraphEdgePattern) (string, string, string) {
	orient, _ := n.Orientation()
	switch orient {
	case googlesql.ASTGraphEdgePatternEnums_EdgeOrientationAny:
		return "-[", "]-", "-"
	case googlesql.ASTGraphEdgePatternEnums_EdgeOrientationLeft:
		return "<-[", "]-", "<-"
	case googlesql.ASTGraphEdgePatternEnums_EdgeOrientationRight:
		return "-[", "]->", "->"
	case googlesql.ASTGraphEdgePatternEnums_EdgeOrientationEdgeOrientationNotSet:
		return "-[", "]-", "-"
	}

	return "-[", "]-", "-"
}

func (p *printer) visitGraphLHSHint(n *googlesql.ASTGraphLhsHint, d printerData) {
	if h, _ := n.Hint(); nodeDefined(h) {
		p.accept(h, d)
	}
}

func (p *printer) visitGraphRHSHint(n *googlesql.ASTGraphRhsHint, d printerData) {
	if h, _ := n.Hint(); nodeDefined(h) {
		p.accept(h, d)
	}
}

func (p *printer) visitGraphElementPatternFiller(
	n *googlesql.ASTGraphElementPatternFiller,
	d printerData,
) {
	p.print(p.formatGraphElementFiller(n, d))
}

func (p *printer) formatGraphElementFiller(
	n *googlesql.ASTGraphElementPatternFiller,
	d printerData,
) string {
	var parts []string

	if h, _ := n.Hint(); nodeDefined(h) {
		parts = append(parts, p.toTrimmedString(h, d))
	}

	var head string
	if vn, _ := n.VariableName(); nodeDefined(vn) {
		head = p.toTrimmedString(vn, d)
	}

	if lf, _ := n.LabelFilter(); nodeDefined(lf) {
		le, _ := lf.LabelExpression()
		head += ":" + p.formatGraphLabelExpr(le, d)
	}

	if head != "" {
		parts = append(parts, head)
	}

	if ps, _ := n.PropertySpecification(); nodeDefined(ps) {
		parts = append(parts, p.formatGraphPropertySpec(ps, d))
	}

	if wc, _ := n.WhereClause(); nodeDefined(wc) {
		expr, _ := wc.Expression()
		parts = append(parts, p.keyword("WHERE")+" "+p.toAlignedString(expr, d))
	}

	if cost, _ := n.EdgeCost(); nodeDefined(cost) {
		parts = append(parts, p.keyword("COST")+" "+p.toAlignedString(cost, d))
	}

	return strings.Join(parts, " ")
}

func (p *printer) visitGraphPropertySpecification(
	n *googlesql.ASTGraphPropertySpecification,
	d printerData,
) {
	p.print(p.formatGraphPropertySpec(n, d))
}

func (p *printer) formatGraphPropertySpec(
	n *googlesql.ASTGraphPropertySpecification,
	d printerData,
) string {
	kvs := selectChildrenOfType[*googlesql.ASTGraphPropertyNameAndValue](n)
	parts := make([]string, 0, len(kvs))

	for _, kv := range kvs {
		pn, _ := kv.PropertyName()
		val, _ := kv.Value()
		parts = append(parts, p.toTrimmedString(pn, d)+": "+p.toAlignedString(val, d))
	}

	return "{" + strings.Join(parts, ", ") + "}"
}

func (p *printer) visitGraphPropertyNameAndValue(
	n *googlesql.ASTGraphPropertyNameAndValue,
	d printerData,
) {
	pn, _ := n.PropertyName()
	val, _ := n.Value()
	p.print(p.toTrimmedString(pn, d) + ": " + p.toAlignedString(val, d))
}

func (p *printer) visitGraphLabelFilter(n *googlesql.ASTGraphLabelFilter, d printerData) {
	le, _ := n.LabelExpression()
	p.print(":" + p.formatGraphLabelExpr(le, d))
}

func (p *printer) visitGraphElementLabel(n *googlesql.ASTGraphElementLabel, d printerData) {
	p.print(p.formatGraphLabelExpr(n, d))
}

func (p *printer) visitGraphWildcardLabel(n *googlesql.ASTGraphWildcardLabel, d printerData) {
	p.print(p.formatGraphLabelExpr(n, d))
}

func (p *printer) visitGraphLabelOperation(n *googlesql.ASTGraphLabelOperation, d printerData) {
	p.print(p.formatGraphLabelExpr(n, d))
}

func (p *printer) formatGraphLabelExpr(n googlesql.ASTGraphLabelExpressionNode, d printerData) string {
	if !nodeDefined(n) {
		return ""
	}

	var body string

	switch le := n.(type) {
	case *googlesql.ASTGraphElementLabel:
		name, _ := le.Name()
		body = p.toTrimmedString(name, d)
	case *googlesql.ASTGraphWildcardLabel:
		body = "%"
	case *googlesql.ASTGraphLabelOperation:
		body = p.formatGraphLabelOp(le, d)
	}

	if paren, _ := n.Parenthesized(); paren {
		return "(" + body + ")"
	}

	return body
}

func (p *printer) formatGraphLabelOp(n *googlesql.ASTGraphLabelOperation, d printerData) string {
	inputs := selectChildrenOfType[googlesql.ASTGraphLabelExpressionNode](n)
	parts := make([]string, 0, len(inputs))

	for _, inp := range inputs {
		parts = append(parts, p.formatGraphLabelExpr(inp, d))
	}

	op, _ := n.OpType()
	switch op {
	case googlesql.ASTGraphLabelOperationEnums_OperationTypeNot:
		if len(parts) > 0 {
			return "!" + parts[0]
		}
	case googlesql.ASTGraphLabelOperationEnums_OperationTypeAnd:
		return strings.Join(parts, " & ")
	case googlesql.ASTGraphLabelOperationEnums_OperationTypeOr:
		return strings.Join(parts, " | ")
	case googlesql.ASTGraphLabelOperationEnums_OperationTypeOperationTypeUnspecified:
		return ""
	}

	return ""
}

func (p *printer) visitGraphIsLabeledPredicate(
	n *googlesql.ASTGraphIsLabeledPredicate,
	d printerData,
) {
	p.moveBefore(n)
	p.printOpenParenIfNeeded(n)

	op, _ := n.Operand()
	le, _ := n.LabelExpression()
	isNot, _ := n.IsNot()

	p.accept(op, d)

	kw := "IS LABELED"
	if isNot {
		kw = "IS NOT LABELED"
	}

	p.print(p.keyword(kw) + " " + p.formatGraphLabelExpr(le, d))
	p.printCloseParenIfNeeded(n)
	p.movePast(n)
}
