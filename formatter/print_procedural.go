package formatter

import (
	"strings"

	"github.com/goccy/go-googlesql"
)

func (p *printer) visitAssertStatement(n *googlesql.ASTAssertStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("ASSERT"))

	expr, _ := n.Expr()
	p.acceptNested(expr, d)

	if desc, _ := n.Description(); nodeDefined(desc) {
		p.print(p.keyword("AS"))
		p.accept(desc, d)
	}

	p.movePast(n)
}

func (p *printer) visitAssignmentFromStruct(n *googlesql.ASTAssignmentFromStruct, d printerData) {
	p.moveBefore(n)

	vars, _ := n.Variables()
	expr, _ := n.StructExpression()

	p.print(p.keyword("SET") + " (")
	p.print(p.toString(vars, d) + ")")
	p.print("=")
	p.accept(expr, d)
	p.movePast(n)
}

func (p *printer) visitLabel(n *googlesql.ASTLabel, d printerData) {
	p.moveBefore(n)
	name, _ := n.Name()
	p.print(p.toString(name, d) + ":")
	p.movePast(n)
}

func (p *printer) printLeadingLabel(label *googlesql.ASTLabel, d printerData) {
	if nodeDefined(label) {
		p.visitLabel(label, d)
	}
}

func (p *printer) printTrailingLabelIfPresent(n googlesql.ASTNode, label *googlesql.ASTLabel, d printerData) {
	if !nodeDefined(label) {
		return
	}

	name, _ := label.Name()
	if !nodeDefined(name) {
		return
	}

	labelText := p.toString(name, d)
	raw := strings.TrimSpace(p.nodeErasedInput(n))

	if strings.HasSuffix(strings.ToUpper(raw), strings.ToUpper(labelText)) {
		p.print(labelText)
	}
}

func (p *printer) visitBeginEndBlock(n *googlesql.ASTBeginEndBlock, d printerData) {
	p.moveBefore(n)

	label, _ := n.Label()
	p.printLeadingLabel(label, d)

	p.println(p.keyword("BEGIN"))
	p.incDepth()

	stmtList, _ := n.StatementListNode()
	p.accept(stmtList, d)

	if hasExc, _ := n.HasExceptionHandler(); hasExc {
		hl, _ := n.HandlerList()

		p.decDepth()
		p.println(p.keyword("EXCEPTION WHEN ERROR THEN"))
		p.incDepth()
		p.accept(hl, d)
	}

	p.movePast(n)
	p.println("")
	p.decDepth()
	p.print(p.keyword("END"))
	p.printTrailingLabelIfPresent(n, label, d)
	p.println("")
}

func (p *printer) visitBeginStatementNode(n *googlesql.ASTBeginStatement, _ printerData) {
	p.moveBefore(n)
	p.println(p.keyword("BEGIN TRANSACTION"))
	p.movePast(n)
}

func (p *printer) visitRollbackStatementNode(n *googlesql.ASTRollbackStatement, _ printerData) {
	p.moveBefore(n)
	p.println(p.keyword("ROLLBACK TRANSACTION"))
	p.movePast(n)
}

func (p *printer) visitExceptionHandlerListNode(n *googlesql.ASTExceptionHandlerList, d printerData) {
	for _, node := range selectChildrenOfType[*googlesql.ASTExceptionHandler](n) {
		p.accept(node, d)
	}
}

func (p *printer) visitExceptionHandlerNode(n *googlesql.ASTExceptionHandler, d printerData) {
	sl, _ := n.StatementList()
	p.accept(sl, d)
}

func (p *printer) visitCallStatement(n *googlesql.ASTCallStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("CALL"))

	procName, _ := n.ProcedureName()
	p.accept(procName, d)

	args := selectChildrenOfType[*googlesql.ASTTVFArgument](n)
	simple := len(args) < 4 && allTrue(p.mapIsSimpleTVFArguments(args))

	pp := p.nest()

	for i, a := range args {
		if i > 0 {
			pp.print(",")

			if !simple {
				pp.println("")
			}
		}

		pp.accept(a, d)
	}

	if simple {
		p.print("(" + pp.unnestLeft() + ")")
	} else {
		p.println("(")
		p.incDepth()
		p.print(pp.unnestLeft())
		p.println("")
		p.decDepth()
		p.print(")")
	}

	p.movePast(n)
}

func (p *printer) visitCommitStatement(n *googlesql.ASTCommitStatement, _ printerData) {
	p.moveBefore(n)
	p.print(p.keyword("COMMIT TRANSACTION"))
	p.movePast(n)
}

func (p *printer) visitExecuteIntoClause(n *googlesql.ASTExecuteIntoClause, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("INTO"))

	ids, _ := n.Identifiers()
	p.accept(ids, d)
}

func (p *printer) visitExecuteImmediateStatement(n *googlesql.ASTExecuteImmediateStatement, d printerData) {
	p.moveBefore(n)
	p.println(p.keyword("EXECUTE IMMEDIATE"))
	p.incDepth()

	sqlExpr, _ := n.Sql()
	p.accept(sqlExpr, d)
	p.println("")
	p.decDepth()

	intoClause, _ := n.IntoClause()
	usingClause, _ := n.UsingClause()

	p.lnaccept(intoClause, d)
	p.lnaccept(usingClause, d)
}

func (p *printer) visitExecuteUsingArgument(n *googlesql.ASTExecuteUsingArgument, d printerData) {
	p.moveBefore(n)
	expr, _ := n.Expression()
	alias, _ := n.Alias()

	p.accept(expr, d)
	p.accept(alias, d)
}

func (p *printer) visitExecuteUsingClause(n *googlesql.ASTExecuteUsingClause, d printerData) {
	p.moveBefore(n)
	p.println(p.keyword("USING"))
	p.incDepth()

	args := selectChildrenOfType[*googlesql.ASTExecuteUsingArgument](n)
	for i, a := range args {
		if i > 0 {
			p.println(",")
		}

		p.accept(a, d)
	}

	p.println("")
	p.decDepth()
}

func (p *printer) visitIfStatement(n *googlesql.ASTIfStatement, d printerData) {
	p.moveBefore(n)

	cond, _ := n.Condition()
	thenList, _ := n.ThenList()

	if p.isSimpleExpr(cond) {
		p.print(p.keyword("IF"))
		p.print(strings.TrimLeft(p.toString(cond, d), "\v"))
		p.println(p.keyword("THEN"))
	} else {
		p.println(p.keyword("IF"))
		p.incDepth()
		p.accept(cond, d)
		p.println("")
		p.decDepth()
		p.println(p.keyword("THEN"))
	}

	p.incDepth()
	p.accept(thenList, d)
	p.println("")
	p.decDepth()

	if elseifs, _ := n.ElseifClauses(); nodeDefined(elseifs) {
		p.moveBefore(elseifs)

		for _, e := range selectChildrenOfType[*googlesql.ASTElseifClause](elseifs) {
			p.moveBefore(e)
			p.print(p.keyword("ELSEIF"))

			ec, _ := e.Condition()
			p.accept(ec, d)
			p.println(p.keyword("THEN"))

			if body, _ := e.Body(); nodeDefined(body) && nodeNumChildren(body) > 0 {
				p.incDepth()
				p.acceptNested(body, d)
				p.println("")
				p.decDepth()
			}

			p.movePast(e)
		}

		p.movePast(elseifs)
	}

	if e, _ := n.ElseList(); nodeDefined(e) {
		p.println(p.keyword("ELSE"))
		p.moveBefore(e)

		if nodeNumChildren(e) > 0 {
			p.incDepth()
			p.acceptNested(e, d)
			p.println("")
			p.decDepth()
		}

		p.movePast(e)
	}

	p.print(p.keyword("END IF"))
}

func (p *printer) visitCaseStatement(n *googlesql.ASTCaseStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("CASE"))

	if expr, _ := n.Expression(); nodeDefined(expr) {
		p.accept(expr, d)
	}

	p.println("")

	if p.fmt.opts.IndentCaseWhen {
		p.incDepth()
	}

	if whens, _ := n.WhenThenClauses(); nodeDefined(whens) {
		for _, wt := range selectChildrenOfType[*googlesql.ASTWhenThenClause](whens) {
			p.moveBefore(wt)
			p.print(p.keyword("WHEN"))

			cond, _ := wt.Condition()
			p.accept(cond, d)
			p.println(p.keyword("THEN"))

			if body, _ := wt.Body(); nodeDefined(body) && nodeNumChildren(body) > 0 {
				p.incDepth()
				p.acceptNested(body, d)
				p.println("")
				p.decDepth()
			}

			p.movePast(wt)
		}
	}

	if el, _ := n.ElseList(); nodeDefined(el) {
		p.println(p.keyword("ELSE"))

		if nodeNumChildren(el) > 0 {
			p.incDepth()
			p.acceptNested(el, d)
			p.println("")
			p.decDepth()
		}
	}

	if p.fmt.opts.IndentCaseWhen {
		p.decDepth()
	}

	p.print(p.keyword("END CASE"))
	p.movePast(n)
}

func (p *printer) visitWhileStatement(n *googlesql.ASTWhileStatement, d printerData) {
	p.moveBefore(n)

	label, _ := n.Label()
	p.printLeadingLabel(label, d)

	cond, _ := n.Condition()
	body, _ := n.Body()

	p.print(p.keyword("WHILE"))
	p.accept(cond, d)
	p.println(p.keyword("DO"))

	if nodeDefined(body) && nodeNumChildren(body) > 0 {
		p.incDepth()
		p.accept(body, d)
		p.println("")
		p.decDepth()
	}

	p.print(p.keyword("END WHILE"))
	p.printTrailingLabelIfPresent(n, label, d)
	p.movePast(n)
}

func (p *printer) visitLoopStatement(n *googlesql.ASTLoopStatement, d printerData) {
	p.moveBefore(n)

	label, _ := n.Label()
	p.printLeadingLabel(label, d)
	p.println(p.keyword("LOOP"))

	if body, _ := n.Body(); nodeDefined(body) && nodeNumChildren(body) > 0 {
		p.incDepth()
		p.accept(body, d)
		p.println("")
		p.decDepth()
	}

	p.print(p.keyword("END LOOP"))
	p.printTrailingLabelIfPresent(n, label, d)
	p.movePast(n)
}

func (p *printer) visitRepeatStatement(n *googlesql.ASTRepeatStatement, d printerData) {
	p.moveBefore(n)

	label, _ := n.Label()
	p.printLeadingLabel(label, d)
	p.println(p.keyword("REPEAT"))

	if body, _ := n.Body(); nodeDefined(body) && nodeNumChildren(body) > 0 {
		p.incDepth()
		p.accept(body, d)
		p.println("")
		p.decDepth()
	}

	if until, _ := n.UntilClause(); nodeDefined(until) {
		cond, _ := until.Condition()

		p.print(p.keyword("UNTIL"))
		p.accept(cond, d)
		p.println("")
	}

	p.print(p.keyword("END REPEAT"))
	p.printTrailingLabelIfPresent(n, label, d)
	p.movePast(n)
}

func (p *printer) visitForInStatement(n *googlesql.ASTForInStatement, d printerData) {
	p.moveBefore(n)

	label, _ := n.Label()
	p.printLeadingLabel(label, d)

	v, _ := n.Variable()
	q, _ := n.Query()
	body, _ := n.Body()

	p.print(p.keyword("FOR"))
	p.accept(v, d)
	p.println(p.keyword("IN") + " (")
	p.incDepth()
	p.acceptNested(q, d)
	p.println("")
	p.decDepth()
	p.println(") " + p.keyword("DO"))

	if nodeDefined(body) && nodeNumChildren(body) > 0 {
		p.incDepth()
		p.accept(body, d)
		p.println("")
		p.decDepth()
	}

	p.print(p.keyword("END FOR"))
	p.printTrailingLabelIfPresent(n, label, d)
	p.movePast(n)
}

func (p *printer) visitBreakStatement(n *googlesql.ASTBreakStatement, d printerData) {
	p.visitBreakContinue(n, d)
}

func (p *printer) visitContinueStatement(n *googlesql.ASTContinueStatement, d printerData) {
	p.visitBreakContinue(n, d)
}

func (p *printer) visitBreakContinue(n googlesql.ASTBreakContinueStatementNode, d printerData) {
	p.moveBefore(n)
	kw, _ := n.GetKeywordText()

	if kw == "" {
		kw = "BREAK"
	}

	p.print(p.keyword(kw))

	if label, _ := n.Label(); nodeDefined(label) {
		name, _ := label.Name()
		p.accept(name, d)
	}

	p.movePast(n)
}

func (p *printer) visitRaiseStatement(n *googlesql.ASTRaiseStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("RAISE"))

	if msg, _ := n.Message(); nodeDefined(msg) {
		p.print(p.keyword("USING MESSAGE") + " =")
		p.accept(msg, d)
	}

	p.movePast(n)
}

func (p *printer) visitParameterAssignment(n *googlesql.ASTParameterAssignment, d printerData) {
	p.moveBefore(n)
	param, _ := n.Parameter()
	expr, _ := n.Expression()

	p.print(p.keyword("SET"))
	p.accept(param, d)
	p.print("=")
	p.accept(expr, d)
	p.movePast(n)
}

func (p *printer) visitReturnStatement(n *googlesql.ASTReturnStatement, _ printerData) {
	p.moveBefore(n)
	p.print(p.keyword("RETURN"))
	p.movePast(n)
}

func (p *printer) visitSystemVariableAssignment(n *googlesql.ASTSystemVariableAssignment, d printerData) {
	p.moveBefore(n)
	sv, _ := n.SystemVariable()
	expr, _ := n.Expression()

	p.print(p.keyword("SET"))
	p.accept(sv, d)
	p.print("=")
	p.accept(expr, d)
	p.movePast(n)
}

func (p *printer) visitSingleAssignment(n *googlesql.ASTSingleAssignment, d printerData) {
	p.moveBefore(n)
	v, _ := n.Variable()
	expr, _ := n.Expression()

	p.print(p.keyword("SET"))
	p.accept(v, d)
	p.print("=")
	p.accept(expr, d)
	p.movePast(n)
}

func (p *printer) visitVariableDeclaration(n *googlesql.ASTVariableDeclaration, d printerData) {
	p.moveBefore(n)
	vl, _ := n.VariableList()
	typ, _ := n.Type()

	p.print(p.keyword("DECLARE"))
	p.accept(vl, d)
	p.acceptNested(typ, d)

	if dv, _ := n.DefaultValue(); nodeDefined(dv) {
		p.print(p.keyword("DEFAULT"))
		p.moveBefore(dv)
		p.acceptNested(dv, d)
	}

	p.movePast(n)
}
