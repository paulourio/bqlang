package formatter

import "github.com/goccy/go-googlesql"

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

	pp.print(pp.keyword("DELETE"))

	pTarget := pp.nest()
	if containsKeywordToken(p.viewErasedInput(begin, targetBegin), "FROM") {
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

	if arm, _ := n.AssertRowsModified(); nodeDefined(arm) {
		p.printAssertRowsModified(arm, d)
	}

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

	if arm, _ := n.AssertRowsModified(); nodeDefined(arm) {
		p.printAssertRowsModified(arm, d)
	}

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
	hint, _ := n.Hint()
	oc, _ := n.OnConflict()

	p.moveBefore(n)

	begin, _ := nodeByteRange(n)
	end, _ := nodeByteRange(target)

	p.print(p.keyword("INSERT"))
	p.printInsertMode(n)

	if containsKeywordToken(p.viewErasedInput(begin, end), "INTO") {
		p.print(p.keyword("INTO"))
	}

	p.accept(target, d)
	p.accept(hint, d)

	if nodeDefined(cl) {
		p.println("")
		p.incDepth()
		p.accept(cl, d)
		p.println("")
		p.decDepth()
	}

	if q, _ := n.Query(); nodeDefined(q) {
		p.println("")

		if nodeDefined(oc) && p.queryParenCount(q) == 0 {
			p.println("(")
			p.incDepth()
			p.acceptNested(q, d)
			p.println("")
			p.decDepth()
			p.print(")")
		} else {
			p.acceptNested(q, d)
		}
	} else if rows, _ := n.Rows(); nodeDefined(rows) {
		p.println("")
		p.println(p.keyword("VALUES"))
		p.incDepth()
		p.accept(rows, d)
		p.println("")
		p.decDepth()
	}

	if nodeDefined(oc) {
		p.printOnConflictClause(oc, d)
	}

	if arm, _ := n.AssertRowsModified(); nodeDefined(arm) {
		p.printAssertRowsModified(arm, d)
	}

	if ret, _ := n.Returning(); nodeDefined(ret) {
		p.lnaccept(ret, d)
	}

	p.movePast(n)
}

func (p *printer) printInsertMode(n *googlesql.ASTInsertStatement) {
	mode, _ := n.InsertMode()

	switch mode {
	case googlesql.ASTInsertStatementEnums_InsertModeIgnore:
		p.print(p.keyword("OR IGNORE"))
	case googlesql.ASTInsertStatementEnums_InsertModeReplace:
		p.print(p.keyword("OR REPLACE"))
	case googlesql.ASTInsertStatementEnums_InsertModeUpdate:
		p.print(p.keyword("OR UPDATE"))
	case googlesql.ASTInsertStatementEnums_InsertModeDefaultMode:
		// Nothing.
	}
}

func (p *printer) printAssertRowsModified(n *googlesql.ASTAssertRowsModified, d printerData) {
	p.println("")
	p.moveBefore(n)
	p.print(p.keyword("ASSERT_ROWS_MODIFIED"))

	if numRows, _ := n.NumRows(); nodeDefined(numRows) {
		p.accept(numRows, d)
	}

	p.movePast(n)
}

func (p *printer) printOnConflictClause(n *googlesql.ASTOnConflictClause, d printerData) {
	p.println("")
	p.moveBefore(n)
	p.print(p.keyword("ON CONFLICT"))

	if ct, _ := n.ConflictTarget(); nodeDefined(ct) {
		p.accept(ct, d)
	} else if ucn, _ := n.UniqueConstraintName(); nodeDefined(ucn) {
		p.print(p.keyword("ON UNIQUE CONSTRAINT"))
		p.accept(ucn, d)
	}

	action, _ := n.ConflictAction()
	switch action {
	case googlesql.ASTOnConflictClauseEnums_ConflictActionNothing:
		p.print(p.keyword("DO NOTHING"))
	case googlesql.ASTOnConflictClauseEnums_ConflictActionUpdate:
		p.print(p.keyword("DO UPDATE"))

		pp := p.nest()
		if items, _ := n.UpdateItemList(); nodeDefined(items) {
			pp.print(pp.keyword("SET"))
			pp.acceptNested(items, d)
		}

		if where, _ := n.UpdateWhereClause(); nodeDefined(where) {
			pp.printClause(pp.keyword("WHERE"))
			pp.visitWhereExprBody(where, d)
		}

		p.println("")
		p.print(alignNested(pp.String()))
	case googlesql.ASTOnConflictClauseEnums_ConflictActionNotSet:
		// Nothing.
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
	pp.println("")
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
		if nodeNumChildren(ir) == 0 {
			p.moveBefore(ir)
			p.println(p.keyword("ROW"))
			p.movePast(ir)
		} else {
			p.println("")
			p.println(p.keyword("VALUES"))
			p.incDepth()
			p.accept(ir, d)
			p.println("")
			p.decDepth()
		}
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
	cond, _ := n.SearchCondition()
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
		scanEnd, _ := nodeByteRange(action)
		if nodeDefined(cond) {
			scanEnd, _ = nodeByteRange(cond)
		}

		if containsKeywordToken(p.viewErasedInput(start, scanEnd), "TARGET") {
			p.print(p.keyword("NOT MATCHED BY TARGET"))
		} else {
			p.print(p.keyword("NOT MATCHED"))
		}
	}

	if nodeDefined(cond) {
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
		p.visitWhereExprBody(w, d)
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

	if sv, _ := n.SetValue(); nodeDefined(sv) {
		p.accept(sv, d)
		p.movePast(n)

		return
	}

	stmt := nestedDMLStatement(n)
	if nodeDefined(stmt) {
		pp := p.nest()
		pp.println("(")
		pp.incDepth()
		pp.acceptNested(stmt, d)
		pp.println("")
		pp.decDepth()
		pp.print(")")
		p.print(alignNested(pp.String()))
	}

	p.movePast(n)
}

func nestedDMLStatement(n *googlesql.ASTUpdateItem) googlesql.ASTNode {
	if ins, _ := n.InsertStatement(); nodeDefined(ins) {
		return ins
	}

	if del, _ := n.DeleteStatement(); nodeDefined(del) {
		return del
	}

	if upd, _ := n.UpdateStatement(); nodeDefined(upd) {
		return upd
	}

	return nil
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
