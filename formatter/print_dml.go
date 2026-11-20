package formatter

import (
	"strings"

	"github.com/goccy/go-googlesql"
)

func (p *printer) visitColumnList(n *googlesql.ASTColumnList, d printerData) {
	p.moveBefore(n)

	cols := selectChildrenOfType[*googlesql.ASTIdentifier](n)
	simple := len(cols) <= 4

	p.print("(")

	if !simple {
		p.println("")
		p.incDepth()
	}

	for i, c := range cols {
		if i > 0 {
			p.print(",")

			if !simple {
				p.println("")
			}
		}

		p.accept(c, d)
	}

	if !simple {
		if len(cols) > 0 && p.fmt.opts.ColumnListTrailingComma == Always {
			p.print(",")
		}

		p.println("")
		p.decDepth()
	}

	p.print(")")
	p.movePast(n)
}

func (p *printer) visitDeleteStatement(n *googlesql.ASTDeleteStatement, d printerData) {
	p.moveBefore(n)

	pp := p.nest()
	target, _ := n.TargetPath()
	begin, _ := nodeByteRange(n)
	targetBegin, _ := nodeByteRange(target)
	prefix := strings.ToUpper(p.viewErasedInput(begin, targetBegin))

	pp.print(pp.keyword("DELETE"))

	pTarget := pp.nest()
	if strings.Contains(prefix, "FROM") {
		pTarget.print(pTarget.keyword("FROM"))
	}

	hint, _ := n.Hint()
	alias, _ := n.Alias()
	off, _ := n.Offset()
	pTarget.acceptDMLTarget(target, hint, alias, off, d)
	pp.print(pTarget.unnest())

	if where, _ := n.Where(); nodeDefined(where) {
		pp.printClause(pp.keyword("WHERE"))
		pp.visitWhereExprBody(where, d)
	}

	p.print(alignNested(pp.String()))

	if ret, _ := n.Returning(); nodeDefined(ret) {
		p.lnaccept(ret, d)
	}

	p.movePast(n)
}

func (p *printer) acceptDMLTarget(
	target googlesql.ASTGeneralizedPathExpressionNode,
	hint *googlesql.ASTHint,
	alias *googlesql.ASTAlias,
	off *googlesql.ASTWithOffset,
	d printerData,
) {
	p.accept(target, d)
	p.accept(hint, d)
	p.accept(alias, d)
	p.accept(off, d)
}

func (p *printer) visitUpdateStatement(n *googlesql.ASTUpdateStatement, d printerData) {
	p.moveBefore(n)

	pp := p.nest()
	pp.print(pp.keyword("UPDATE"))

	pTarget := pp.nest()
	target, _ := n.TargetPath()
	hint, _ := n.Hint()
	alias, _ := n.Alias()
	off, _ := n.Offset()
	pTarget.acceptDMLTarget(target, hint, alias, off, d)
	pp.print(pTarget.unnest())

	if items, _ := n.UpdateItemList(); nodeDefined(items) {
		pp.printClause(pp.keyword("SET"))
		pp.acceptNested(items, d)
	}

	if fc, _ := n.FromClause(); nodeDefined(fc) {
		pp.printClause(pp.keyword("FROM"))
		pp.acceptNested(fc, d)
	}

	if where, _ := n.Where(); nodeDefined(where) {
		pp.printClause(pp.keyword("WHERE"))
		pp.visitWhereExprBody(where, d)
	}

	p.print(alignNested(pp.String()))

	if ret, _ := n.Returning(); nodeDefined(ret) {
		p.lnaccept(ret, d)
	}

	p.movePast(n)
}

func (p *printer) visitReturningClause(n *googlesql.ASTReturningClause, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("THEN RETURN"))

	if alias, _ := n.ActionAlias(); nodeDefined(alias) {
		p.print(p.keyword("WITH ACTION"))
		p.accept(alias, d)
	}

	if sl, _ := n.SelectList(); nodeDefined(sl) {
		p.accept(sl, d)
	}

	p.movePast(n)
}

func (p *printer) visitInsertStatement(n *googlesql.ASTInsertStatement, d printerData) {
	cl, _ := n.ColumnList()
	target, _ := n.TargetPath()

	p.moveBefore(n)

	begin, _ := nodeByteRange(n)
	end, _ := nodeByteRange(target)
	input := strings.ToUpper(p.viewErasedInput(begin, end))

	if strings.Contains(input, "INTO") {
		p.print(p.keyword("INSERT INTO"))
	} else {
		p.print(p.keyword("INSERT"))
	}

	p.accept(target, d)

	if nodeDefined(cl) {
		p.println("")
		p.incDepth()
		p.accept(cl, d)
		p.println("")
		p.decDepth()
	}

	if q, _ := n.Query(); nodeDefined(q) {
		p.println("")
		p.acceptNested(q, d)
	} else if rows, _ := n.Rows(); nodeDefined(rows) {
		p.println("")
		p.println(p.keyword("VALUES"))
		p.incDepth()
		p.accept(rows, d)
		p.println("")
		p.decDepth()
	}

	if ret, _ := n.Returning(); nodeDefined(ret) {
		p.lnaccept(ret, d)
	}

	p.movePast(n)
}

func (p *printer) visitInsertValuesRowList(n *googlesql.ASTInsertValuesRowList, d printerData) {
	p.moveBefore(n)

	rows := selectChildrenOfType[*googlesql.ASTInsertValuesRow](n)
	for i, r := range rows {
		if i > 0 {
			p.println(",")
		}

		p.accept(r, d)
	}

	p.movePast(n)
}

func (p *printer) visitInsertValuesRow(n *googlesql.ASTInsertValuesRow, d printerData) {
	p.moveBefore(n)

	values := selectChildrenOfType[googlesql.ASTExpressionNode](n)
	simple := len(values) <= 4 && p.areSimpleExprs(values)

	printParenList(p, values, simple, d)
	p.movePast(n)
}

func (p *printer) visitMergeStatement(n *googlesql.ASTMergeStatement, d printerData) {
	pp := p.nest()

	pp.moveBefore(n)
	pp.print(pp.keyword("MERGE") + " ")

	target, _ := n.TargetPath()
	alias, _ := n.Alias()
	tableExpr, _ := n.TableExpression()
	cond, _ := n.MergeCondition()
	whens, _ := n.WhenClauses()

	p1 := pp.nest()
	p1.acceptNested(target, d)

	pp.print(p1.unnest())
	pp.accept(alias, d)
	pp.println("")
	pp.print(pp.keyword("USING") + " ")
	pp.acceptNested(tableExpr, d)
	pp.println("")
	pp.print(pp.keyword("ON") + " ")
	pp.acceptNested(cond, d)
	p.println("")
	pp.accept(whens, d)
	pp.movePast(n)

	p.print(pp.unnest())
}

func (p *printer) visitMergeAction(n *googlesql.ASTMergeAction, d printerData) {
	p.moveBefore(n)
	at, _ := n.ActionType()

	switch at {
	case googlesql.ASTMergeActionEnums_ActionTypeNotSet:
		// Nothing.
	case googlesql.ASTMergeActionEnums_ActionTypeInsert:
		p.visitMergeActionInsert(n, d)
	case googlesql.ASTMergeActionEnums_ActionTypeUpdate:
		p.visitMergeActionUpdate(n, d)
	case googlesql.ASTMergeActionEnums_ActionTypeDelete:
		p.println(p.keyword("DELETE"))
	}

	p.movePast(n)
}

func (p *printer) visitMergeActionInsert(n *googlesql.ASTMergeAction, d printerData) {
	cl, _ := n.InsertColumnList()
	ir, _ := n.InsertRow()

	if !nodeDefined(cl) && nodeDefined(ir) && nodeNumChildren(ir) == 0 {
		p.println(p.keyword("INSERT ROW"))

		return
	}

	p.println(p.keyword("INSERT"))

	if nodeDefined(cl) {
		p.incDepth()
		p.accept(cl, d)
		p.println("")
		p.decDepth()
	}

	if nodeDefined(ir) {
		p.println("")
		p.println(p.keyword("VALUES"))
		p.incDepth()
		p.accept(ir, d)
		p.println("")
		p.decDepth()
	}
}

func (p *printer) visitMergeActionUpdate(n *googlesql.ASTMergeAction, d printerData) {
	uil, _ := n.UpdateItemList()

	p.println(p.keyword("UPDATE SET"))
	p.incDepth()
	p.accept(uil, d)
	p.println("")
	p.decDepth()
}

func (p *printer) visitMergeWhenClauseList(n *googlesql.ASTMergeWhenClauseList, d printerData) {
	p.moveBefore(n)

	for _, c := range selectChildrenOfType[*googlesql.ASTMergeWhenClause](n) {
		p.println("")
		p.print(p.keyword("WHEN") + " ")
		p.acceptNested(c, d)
	}

	p.println("")
	p.movePast(n)
}

func (p *printer) visitMergeWhenClause(n *googlesql.ASTMergeWhenClause, d printerData) {
	p.moveBefore(n)

	mt, _ := n.MatchType()
	action, _ := n.Action()

	switch mt {
	case googlesql.ASTMergeWhenClauseEnums_MatchTypeNotSet:
		// Nothing.
	case googlesql.ASTMergeWhenClauseEnums_MatchTypeMatched:
		p.print(p.keyword("MATCHED"))
	case googlesql.ASTMergeWhenClauseEnums_MatchTypeNotMatchedBySource:
		p.print(p.keyword("NOT MATCHED BY SOURCE"))
	case googlesql.ASTMergeWhenClauseEnums_MatchTypeNotMatchedByTarget:
		start, _ := nodeByteRange(n)
		next, _ := nodeByteRange(action)
		input := strings.ToUpper(p.viewErasedInput(start, next))

		if strings.Contains(input, "TARGET") {
			p.print(p.keyword("NOT MATCHED BY TARGET"))
		} else {
			p.print(p.keyword("NOT MATCHED"))
		}
	}

	if cond, _ := n.SearchCondition(); nodeDefined(cond) {
		if p.isSimpleExpr(cond) {
			p.print(p.keyword("AND"))
			p.accept(cond, d)
		} else {
			p.print(p.keyword("AND"))
			p.println("")
			p.incDepth()
			p.accept(cond, d)
			p.println("")
			p.decDepth()
		}
	}

	p.println(p.keyword("THEN"))
	p.accept(action, d)

	p.movePast(n)
}

func (p *printer) visitTruncateStatement(n *googlesql.ASTTruncateStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("TRUNCATE TABLE"))

	target, _ := n.TargetPath()
	p.accept(target, d)

	if w, _ := n.Where(); nodeDefined(w) {
		p.println("")
		p.print(p.keyword("WHERE"))
		p.accept(w, d)
	}

	p.movePast(n)
}

func (p *printer) visitUpdateItemList(n *googlesql.ASTUpdateItemList, d printerData) {
	p.moveBefore(n)

	pp := p.nest()
	items := selectChildrenOfType[*googlesql.ASTUpdateItem](n)

	for i, item := range items {
		if i > 0 {
			pp.println(",")
		}

		pp.accept(item, d)
	}

	p.print(pp.unnestLeft())
	p.movePast(n)
}

func (p *printer) visitUpdateItem(n *googlesql.ASTUpdateItem, d printerData) {
	p.moveBefore(n)

	sv, _ := n.SetValue()
	ins, _ := n.InsertStatement()
	del, _ := n.DeleteStatement()
	upd, _ := n.UpdateStatement()

	p.accept(sv, d)
	p.accept(ins, d)
	p.accept(del, d)
	p.accept(upd, d)
	p.movePast(n)
}

func (p *printer) visitUpdateSetValue(n *googlesql.ASTUpdateSetValue, d printerData) {
	p.moveBefore(n)

	path, _ := n.Path()
	val, _ := n.Value()

	p.accept(path, d)

	pp := p.nest()
	pp.print("=")
	pp.acceptNested(val, d)
	p.print(pp.unnest())
	p.movePast(n)
}
