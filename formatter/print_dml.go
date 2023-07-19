// This file contains printing functions specific for
// Data Definition Language (DDL).
package formatter

import (
	"strings"

	"github.com/goccy/go-zetasql/ast"
)

func (p *Printer) VisitColumnList(n *ast.ColumnListNode, d Data) {
	p.moveBefore(n)

	cols := n.Identifiers()
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

func (p *Printer) VisitInsertStatement(n *ast.InsertStatementNode, d Data) {
	cl := n.ColumnList()

	p.moveBefore(n)
	p.print(p.keyword("INSERT"))
	p.accept(n.TargetPath(), d)

	if cl != nil {
		p.println("")
		p.incDepth()
		p.accept(n.ColumnList(), d)
		p.println("")
		p.decDepth()
	}

	if q := n.Query(); q != nil {
		p.println("")
		p.acceptNested(q, d)
	} else {
		p.println("")
		p.println(p.keyword("VALUES"))
		p.incDepth()
		p.accept(n.Rows(), d)
		p.println("")
		p.decDepth()
	}

	p.movePast(n)
}

func (p *Printer) VisitInsertValuesRowList(n *ast.InsertValuesRowListNode, d Data) {
	p.moveBefore(n)

	for i, r := range n.Rows() {
		if i > 0 {
			p.println(",")
		}

		p.accept(r, d)
	}

	p.movePast(n)
}

func (p *Printer) VisitInsertValuesRow(n *ast.InsertValuesRowNode, d Data) {
	p.moveBefore(n)

	values := n.Values()
	simple := len(values) <= 4 && allTrue(mapIsSimpleExprs(values))

	p.print("(")

	if !simple {
		p.println("")
		p.incDepth()
	}

	for i, r := range values {
		if i > 0 {
			p.print(",")

			if !simple {
				p.println("")
			}
		}

		p.accept(r, d)
	}

	if !simple {
		p.println("")
		p.decDepth()
	}

	p.print(")")
	p.movePast(n)
}

func (p *Printer) VisitMergeStatement(n *ast.MergeStatementNode, d Data) {
	pp := p.nest()

	pp.moveBefore(n)
	pp.print(pp.keyword("MERGE") + " ")

	p1 := pp.nest()
	// p1.print(p.keyword("INTO"))
	p1.acceptNested(n.TargetPath(), d)

	pp.print(p1.unnest())
	pp.accept(n.Alias(), d)
	pp.println("")
	pp.print(pp.keyword("USING") + " ")
	pp.acceptNested(n.TableExpression(), d)
	pp.println("")
	pp.print(pp.keyword("ON") + " ")
	pp.acceptNested(n.MergeCondition(), d)
	p.println("")
	pp.accept(n.WhenClauses(), d)
	pp.movePast(n)

	p.print(pp.unnest())
}

func (p *Printer) VisitMergeAction(n *ast.MergeActionNode, d Data) {
	p.moveBefore(n)

	switch n.ActionType() {
	case ast.MergeActionNotSet:
		// Nothing.
	case ast.MergeActionInsert:
		p.visitMergeActionInsert(n, d)
	case ast.MergeActionUpdate:
		p.visitMergeActionUpdate(n, d)
	case ast.MergeActionDelete:
		p.visitMergeActionDelete(n, d)
	}

	p.movePast(n)
}

func (p *Printer) visitMergeActionDelete(n *ast.MergeActionNode, d Data) {
	p.println(p.keyword("DELETE"))
}

func (p *Printer) visitMergeActionInsert(n *ast.MergeActionNode, d Data) {
	cl := n.InsertColumnList()
	ir := n.InsertRow()

	if cl == nil && ir != nil && ir.NumChildren() == 0 {
		p.println(p.keyword("INSERT ROW"))

		return
	}

	p.println(p.keyword("INSERT"))

	if cl != nil {
		p.incDepth()
		p.accept(n.InsertColumnList(), d)
		p.println("")
		p.decDepth()
	}

	if ir != nil {
		p.println("")
		p.println(p.keyword("VALUES"))
		p.incDepth()
		p.accept(n.InsertRow(), d)
		p.println("")
		p.decDepth()
	}
}

func (p *Printer) visitMergeActionUpdate(n *ast.MergeActionNode, d Data) {
	p.println(p.keyword("UPDATE SET"))
	p.incDepth()
	p.accept(n.UpdateItemList(), d)
	p.println("")
	p.decDepth()
}

func (p *Printer) VisitMergeWhenClauseList(n *ast.MergeWhenClauseListNode, d Data) {
	p.moveBefore(n)

	for _, c := range n.ClauseList() {
		p.println("")
		p.print(p.keyword("WHEN") + " ")
		p.acceptNested(c, d)
	}

	p.println("")
	p.movePast(n)
}

func (p *Printer) VisitMergeWhenClause(n *ast.MergeWhenClauseNode, d Data) {
	p.moveBefore(n)

	switch n.MatchType() {
	case ast.MergeMatchNotSet:
		// Nothing.
	case ast.MergeMatched:
		p.print(p.keyword("MATCHED"))
	case ast.MergeNotMatchedBySource:
		p.print(p.keyword("NOT MATCHED BY SOURCE"))
	case ast.MergeNotMatchedByTarget:
		start := n.ParseLocationRange().Start().ByteOffset()
		next := n.Action().ParseLocationRange().Start().ByteOffset()
		input := strings.ToUpper(p.viewErasedInput(start, next))

		if strings.Contains(input, "TARGET") {
			p.print(p.keyword("NOT MATCHED BY TARGET"))
		} else {
			p.print(p.keyword("NOT MATCHED"))
		}
	}

	if cond := n.SearchCondition(); cond != nil {

		if isSimpleExpr(cond) {
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
	p.accept(n.Action(), d)

	p.movePast(n)
}

func (p *Printer) VisitTruncateStatement(n *ast.TrucateStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("TRUNCATE TABLE"))
	p.accept(n.TargetPath(), d)

	if w := n.Where(); w != nil {
		p.println("")
		p.print(p.keyword("WHERE"))
		p.accept(n.Where(), d)
	}

	p.movePast(n)
}

func (p *Printer) VisitUpdateItemList(n *ast.UpdateItemListNode, d Data) {
	p.moveBefore(n)

	pp := p.nest()
	items := n.UpdateItems()

	for i, item := range items {
		if i > 0 {
			pp.println(",")
		}

		pp.accept(item, d)
	}

	p.print(pp.unnestLeft())
	p.movePast(n)
}

func (p *Printer) VisitUpdateItem(n *ast.UpdateItemNode, d Data) {
	p.moveBefore(n)
	p.accept(n.SetValue(), d)
	p.accept(n.InsertStatement(), d)
	p.accept(n.DeleteStatement(), d)
	p.accept(n.UpdateStatement(), d)
	p.movePast(n)
}

func (p *Printer) VisitUpdateSetValue(n *ast.UpdateSetValueNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Path(), d)

	pp := p.nest()
	pp.print("=")

	pp.acceptNested(n.Value(), d)
	p.print(pp.unnest())
	p.movePast(n)
}
