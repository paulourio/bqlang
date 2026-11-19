package formatter

import (
	"strings"

	"github.com/goccy/go-googlesql"
)

func (p *printer) visitPipeJoinLHSPlaceholder(_ *googlesql.ASTPipeJoinLhsPlaceholder, _ printerData) {
	// Intentionally empty: Pipe JOIN omits the implicit piped LHS table.
}

func (p *printer) visitPipeDescribe(_ *googlesql.ASTPipeDescribe, _ printerData) {
	p.print(p.keyword("DESCRIBE"))
}

func (p *printer) visitPipeStaticDescribe(_ *googlesql.ASTPipeStaticDescribe, _ printerData) {
	p.print(p.keyword("STATIC_DESCRIBE"))
}

func (p *printer) visitPipeCreateTable(n *googlesql.ASTPipeCreateTable, d printerData) {
	st, _ := n.CreateTableStatement()
	p.accept(st, d)
}

func (p *printer) visitPipeInsert(n *googlesql.ASTPipeInsert, d printerData) {
	st, _ := n.InsertStatement()
	p.accept(st, d)
}

func (p *printer) visitPipeExportData(n *googlesql.ASTPipeExportData, d printerData) {
	st, _ := n.ExportDataStatement()
	p.accept(st, d)
}

func (p *printer) visitFromQuery(n *googlesql.ASTFromQuery, d printerData) {
	p.moveBefore(n)
	p.printOpenParenIfNeeded(n)

	fc, _ := n.FromClause()

	p.print(p.keyword("FROM"))
	p.acceptNested(fc, d)

	p.printCloseParenIfNeeded(n)
	p.movePast(n)
}

func (p *printer) visitStatementWithPipeOperators(n *googlesql.ASTStatementWithPipeOperators, d printerData) {
	p.moveBefore(n)
	stmt, _ := n.Statement()
	suffix, _ := n.PipeOperatorSuffix()

	p.accept(stmt, d)

	if nodeDefined(suffix) {
		p.println("")
		p.accept(suffix, d)
	}

	p.movePast(n)
}

func (p *printer) visitSubpipelineStatement(n *googlesql.ASTSubpipelineStatement, d printerData) {
	sub, _ := n.Subpipeline()
	p.accept(sub, d)
}

func (p *printer) visitSubpipeline(n *googlesql.ASTSubpipeline, d printerData) {
	p.moveBefore(n)
	paren, _ := n.Parenthesized()

	if paren {
		p.println("(")
		p.incDepth()
	}

	ops := selectChildrenOfType[googlesql.ASTPipeOperatorNode](n)
	p.printPipeOperatorList(ops, d, !paren)

	if paren {
		p.println("")
		p.decDepth()
		p.print(")")
	}

	p.movePast(n)
}

func (p *printer) printPipeOperatorList(
	ops []googlesql.ASTPipeOperatorNode,
	d printerData,
	leadingNewline bool,
) {
	for i, op := range ops {
		if i > 0 || leadingNewline {
			p.println("")
		}

		p.moveBefore(op)
		pp := p.nest()
		pp.print("|>")
		pp.accept(op, d)
		p.print(strings.TrimLeft(pp.unnestLeft(), "\v"))
		p.movePast(op)
	}
}

func (p *printer) visitPipeSelect(n *googlesql.ASTPipeSelect, d printerData) {
	sel, _ := n.Select()
	if !nodeDefined(sel) {
		return
	}

	p.printPipeSelectLike("SELECT", sel, d)
}

func (p *printer) visitPipeExtend(n *googlesql.ASTPipeExtend, d printerData) {
	sel, _ := n.Select()
	if !nodeDefined(sel) {
		return
	}

	p.printPipeSelectLike("EXTEND", sel, d)
}

func (p *printer) visitPipeWindow(n *googlesql.ASTPipeWindow, d printerData) {
	sel, _ := n.Select()
	if !nodeDefined(sel) {
		return
	}

	p.printPipeSelectLike("WINDOW", sel, d)
}

func (p *printer) printPipeSelectLike(kw string, sel *googlesql.ASTSelect, d printerData) {
	p.print(p.keyword(kw))

	if distinct, _ := sel.Distinct(); distinct {
		p.print(p.keyword("DISTINCT"))
	}

	if sa, _ := sel.SelectAs(); nodeDefined(sa) {
		p.accept(sa, d)
	}

	singleLine := p.maybeSingleLineColumns(sel)
	d.SetBool("single_line_cols", singleLine)

	if sl, _ := sel.SelectList(); nodeDefined(sl) {
		p.accept(sl, d)
	}
}

func (p *printer) visitPipeSet(n *googlesql.ASTPipeSet, d printerData) {
	p.print(p.keyword("SET"))

	items := selectChildrenOfType[*googlesql.ASTPipeSetItem](n)
	printNestedWithSep(p, items, d, ",")
}

func (p *printer) visitPipeSetItem(n *googlesql.ASTPipeSetItem, d printerData) {
	col, _ := n.Column()
	expr, _ := n.Expression()

	p.accept(col, d)
	p.print("=")
	p.accept(expr, d)
}

func (p *printer) visitPipeRename(n *googlesql.ASTPipeRename, d printerData) {
	p.print(p.keyword("RENAME"))

	items := selectChildrenOfType[*googlesql.ASTPipeRenameItem](n)
	printNestedWithSep(p, items, d, ",")
}

func (p *printer) visitPipeRenameItem(n *googlesql.ASTPipeRenameItem, d printerData) {
	oldName, _ := n.OldName()
	newName, _ := n.NewName()

	p.accept(oldName, d)
	p.print(p.keyword("AS"))
	p.accept(newName, d)
}

func (p *printer) visitPipeDrop(n *googlesql.ASTPipeDrop, d printerData) {
	p.print(p.keyword("DROP"))

	cols, _ := n.ColumnList()
	p.accept(cols, d)
}

func (p *printer) visitPipeWhere(n *googlesql.ASTPipeWhere, d printerData) {
	w, _ := n.Where()
	if !nodeDefined(w) {
		return
	}

	expr, _ := w.Expression()

	p.print(p.keyword("WHERE"))
	p.acceptNested(expr, d)
}

func (p *printer) visitPipeAggregate(n *googlesql.ASTPipeAggregate, d printerData) {
	sel, _ := n.Select()
	if !nodeDefined(sel) {
		return
	}

	p.print(p.keyword("AGGREGATE"))

	if wm, _ := n.WithModifier(); nodeDefined(wm) {
		p.accept(wm, d)
	}

	singleLine := p.maybeSingleLineColumns(sel)
	d.SetBool("single_line_cols", singleLine)

	if sl, _ := sel.SelectList(); nodeDefined(sl) && nodeNumChildren(sl) > 0 {
		p.accept(sl, d)
	}

	if gb, _ := sel.GroupBy(); nodeDefined(gb) {
		p.print(p.keyword("GROUP"))
		p.visitGroupByItems(gb, d)
	}
}

func (p *printer) visitPipeJoin(n *googlesql.ASTPipeJoin, d printerData) {
	j, _ := n.Join()
	if !nodeDefined(j) {
		return
	}

	p.print(p.keyword(p.joinKeyword(j)))

	if hint, _ := j.Hint(); nodeDefined(hint) {
		p.accept(hint, d)
	}

	rhs, _ := j.Rhs()
	p.acceptNested(rhs, d)

	if oc, _ := j.OnClause(); nodeDefined(oc) {
		expr, _ := oc.Expression()

		p.print(p.keyword("ON"))
		p.acceptNested(expr, d)
	}

	if uc, _ := j.UsingClause(); nodeDefined(uc) {
		p.print(p.keyword("USING") + " (")

		keys := selectChildrenOfType[*googlesql.ASTIdentifier](uc)
		printNestedWithSep(p, keys, d, ",")
		p.print(")")
	}
}

func (p *printer) visitPipeCall(n *googlesql.ASTPipeCall, d printerData) {
	p.print(p.keyword("CALL"))

	tvf, _ := n.Tvf()
	p.accept(tvf, d)
}

func (p *printer) visitPipeOrderBy(n *googlesql.ASTPipeOrderBy, d printerData) {
	ob, _ := n.OrderBy()
	if !nodeDefined(ob) {
		return
	}

	p.print(p.keyword("ORDER"))

	if hint, _ := ob.Hint(); nodeDefined(hint) {
		p.accept(hint, d)
	}

	p.print(p.keyword("BY"))

	exprs := selectChildrenOfType[*googlesql.ASTOrderingExpression](ob)
	printNestedWithSep(p, exprs, d, ",")
}

func (p *printer) visitPipeLimitOffset(n *googlesql.ASTPipeLimitOffset, d printerData) {
	lo, _ := n.LimitOffset()
	p.accept(lo, d)
}

func (p *printer) visitPipeDistinct(_ *googlesql.ASTPipeDistinct, _ printerData) {
	p.print(p.keyword("DISTINCT"))
}

func (p *printer) visitPipeTablesample(n *googlesql.ASTPipeTablesample, d printerData) {
	sc, _ := n.Sample()
	if !nodeDefined(sc) {
		return
	}

	method, _ := sc.SampleMethod()
	size, _ := sc.SampleSize()
	suffix, _ := sc.SampleSuffix()

	p.print(p.keyword("TABLESAMPLE"))
	p.print(p.keyword(p.toString(method, d)) + " (")
	p.accept(size, d)
	p.print(")")
	p.accept(suffix, d)
}

func (p *printer) visitPipeAs(n *googlesql.ASTPipeAs, d printerData) {
	p.print(p.keyword("AS"))

	alias, _ := n.Alias()

	if nodeDefined(alias) {
		id, _ := alias.Identifier()
		p.accept(id, d)
	}
}

func (p *printer) visitPipeSetOperation(n *googlesql.ASTPipeSetOperation, d printerData) {
	meta, _ := n.Metadata()
	hdr := p.nest()
	hdr.printSetOperationHeader(meta, d)
	p.print(strings.ReplaceAll(hdr.String(), "\v", ""))

	inputs := selectChildrenOfType[googlesql.ASTQueryExpressionNode](n)
	if len(inputs) == 0 {
		return
	}

	p.println("")
	p.incDepth()

	for i, inp := range inputs {
		if i > 0 {
			p.println(",")
		}

		p.accept(inp, d)
	}

	p.decDepth()
}

func (p *printer) visitPipePivot(n *googlesql.ASTPipePivot, d printerData) {
	pv, _ := n.PivotClause()
	p.accept(pv, d)
}

func (p *printer) visitPipeUnpivot(n *googlesql.ASTPipeUnpivot, d printerData) {
	up, _ := n.UnpivotClause()
	p.accept(up, d)
}

func (p *printer) visitPipeAssert(n *googlesql.ASTPipeAssert, d printerData) {
	p.print(p.keyword("ASSERT"))

	cond, _ := n.Condition()
	msgs := selectChildrenOfType[googlesql.ASTExpressionNode](n)

	p.accept(cond, d)

	for i := 1; i < len(msgs); i++ {
		p.print(",")
		p.accept(msgs[i], d)
	}
}

func (p *printer) visitPipeWith(n *googlesql.ASTPipeWith, d printerData) {
	wc, _ := n.WithClause()
	if !nodeDefined(wc) {
		return
	}

	p.moveBefore(wc)

	if rec, _ := wc.Recursive(); rec {
		p.println(p.keyword("WITH RECURSIVE"))
	} else {
		p.println(p.keyword("WITH"))
	}

	p.incDepth()

	for i, e := range selectChildrenOfType[*googlesql.ASTWithClauseEntry](wc) {
		if i > 0 {
			p.println(",")
		}

		p.accept(e, d)
	}

	p.decDepth()
	p.movePast(wc)
}
