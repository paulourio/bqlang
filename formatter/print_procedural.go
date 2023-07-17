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
