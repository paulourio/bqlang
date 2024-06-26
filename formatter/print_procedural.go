// Methods for procedural language.
package formatter

import (
	"strings"

	"github.com/goccy/go-zetasql/ast"
)

func (p *Printer) VisitAssignmentFromStruct(n *ast.AssignmentFromStructNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("SET") + " (")
	p.print(p.toString(n.Variables(), d) + ")")
	p.print("=")
	p.accept(n.StructExpression(), d)
	p.movePast(n)
}

func (p *Printer) VisitBeginEndBlock(n *ast.BeginEndBlockNode, d Data) {
	p.moveBefore(n)
	p.println(p.keyword("BEGIN"))
	p.incDepth()
	p.accept(n.StatementListNode(), d)

	if n.HasExceptionHandler() {
		p.decDepth()
		p.println(p.keyword("EXCEPTION WHEN ERROR THEN"))
		p.incDepth()
		p.accept(n.HandlerList(), d)
	}

	p.movePast(n)
	p.println("")
	p.decDepth()
	p.println(p.keyword("END"))
}

func (p *Printer) VisitBeginStatementNode(n *ast.BeginStatementNode, d Data) {
	p.moveBefore(n)
	p.println(p.keyword("BEGIN TRANSACTION"))
	p.movePast(n)
}

func (p *Printer) VisitRollbackStatementNode(n *ast.RollbackStatementNode, d Data) {
	p.moveBefore(n)
	p.println(p.keyword("ROLLBACK TRANSACTION"))
	p.movePast(n)
}

func (p *Printer) VisitExceptionHandlerListNode(n *ast.ExceptionHandlerListNode, d Data) {
	for _, node := range n.ExceptionHandlerList() {
		p.accept(node, d)
	}
}

func (p *Printer) VisitExceptionHandlerNode(n *ast.ExceptionHandlerNode, d Data) {
	p.accept(n.StatementList(), d)
}

func (p *Printer) VisitCallStatement(n *ast.CallStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("CALL"))
	p.accept(n.ProcedureName(), d)

	args := n.Arguments()
	simple := len(args) < 4 && allTrue(mapIsSimpleTVFArguments(args))

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

func (p *Printer) VisitCommitStatement(n *ast.CommitStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("COMMIT TRANSACTION"))
	p.movePast(n)
}

func (p *Printer) VisitExecuteIntoClause(n *ast.ExecuteIntoClauseNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("INTO"))
	p.accept(n.Identifiers(), d)
}

func (p *Printer) VisitExecuteImmediateStatement(n *ast.ExecuteImmediateStatementNode, d Data) {
	p.moveBefore(n)
	p.println(p.keyword("EXECUTE IMMEDIATE"))
	p.incDepth()
	// In the future we may try to format the SQL contents when they're
	// a single string containing a valid SQL.
	p.accept(n.SQL(), d)
	p.println("")
	p.decDepth()
	p.lnaccept(n.IntoClause(), d)
	p.lnaccept(n.UsingClause(), d)
}

func (p *Printer) VisitExecuteUsingArgument(n *ast.ExecuteUsingArgumentNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Expression(), d)
	p.accept(n.Alias(), d)
}

func (p *Printer) VisitExecuteUsingClause(n *ast.ExecuteUsingClauseNode, d Data) {
	p.moveBefore(n)
	p.println(p.keyword("USING"))
	p.incDepth()

	args := n.Arguments()

	for i, a := range args {
		if i > 0 {
			p.println(",")
		}

		p.accept(a, d)
	}

	p.println("")
	p.decDepth()
}

func (p *Printer) VisitIfStatement(n *ast.IfStatementNode, d Data) {
	p.moveBefore(n)

	cond := n.Condition()

	if isSimpleExpr(cond) {
		p.print(p.keyword("IF"))
		p.print(strings.TrimLeft(p.toString(n.Condition(), d), "\v"))
		p.println(p.keyword("THEN"))
	} else {
		p.println(p.keyword("IF"))
		p.incDepth()
		p.accept(n.Condition(), d)
		p.println("")
		p.decDepth()
		p.println(p.keyword("THEN"))
	}

	p.incDepth()
	p.accept(n.ThenList(), d)
	p.println("")
	p.decDepth()

	if elseifs := n.ElseifClauses(); elseifs != nil {
		p.moveBefore(elseifs)

		for _, e := range elseifs.ElseifClauses() {
			p.moveBefore(e)
			p.print(p.keyword("ELSEIF"))
			p.accept(e.Condition(), d)
			p.println(p.keyword("THEN"))

			body := e.Body()
			if body != nil && len(body.StatementList()) > 0 {
				p.incDepth()
				p.acceptNested(body, d)
				p.println("")
				p.decDepth()
			}

			p.movePast(e)
		}

		p.movePast(elseifs)
	}

	if e := n.ElseList(); e != nil {
		p.println(p.keyword("ELSE"))
		p.moveBefore(e)

		if len(e.StatementList()) > 0 {
			p.incDepth()
			p.acceptNested(e, d)
			p.println("")
			p.decDepth()
		}

		p.movePast(e)
	}

	p.print(p.keyword("END IF"))
}

func (p *Printer) VisitParameterAssignment(n *ast.ParameterAssignmentNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("SET"))
	p.accept(n.Parameter(), d)
	p.print("=")
	p.accept(n.Expression(), d)
	p.moveBefore(n)
}

func (p *Printer) VisitReturnStatement(n *ast.ReturnStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("RETURN"))
}

func (p *Printer) VisitSystemVariableAssignment(n *ast.SystemVariableAssignmentNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("SET"))
	p.accept(n.SystemVariable(), d)
	p.print("=")
	p.accept(n.Expression(), d)
	p.movePast(n)
}

func (p *Printer) VisitSingleAssignment(n *ast.SingleAssignmentNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("SET"))
	p.accept(n.Variable(), d)
	p.print("=")
	p.accept(n.Expression(), d)
	p.movePast(n)
}

func (p *Printer) VisitVariableDeclaration(n *ast.VariableDeclarationNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("DECLARE"))
	p.accept(n.VariableList(), d)
	p.acceptNested(n.Type(), d)

	if dv := n.DefaultValue(); dv != nil {
		p.print(p.keyword("DEFAULT"))
		p.moveBefore(dv)
		p.acceptNested(n.DefaultValue(), d)
	}

	p.movePast(n)
}
