// Functions to format common query syntax elements.
package formatter

import (
	"fmt"
	"log"
	"reflect"
	"strings"
	"unicode"

	"github.com/goccy/go-zetasql/ast"
	"golang.org/x/exp/maps"
)

func (p *Printer) VisitAlias(n *ast.AliasNode, d Data) {
	if n.Parent().Kind() != ast.WithOffset {
		p.print(p.keyword("AS"))
	}

	p.accept(n.Identifier(), d)
}

func (p *Printer) VisitAnalyticFunctionCall(n *ast.AnalyticFunctionCallNode, d Data) {
	pp := p.nest()

	pp.printOpenParenIfNeeded(n)

	pp.accept(n.Function(), d)
	pp.print(p.keyword("OVER") + " ")

	ws := n.WindowSpec()
	elems := countWindowSpecElems(ws)

	// We have the option of keeping parenthesis even when not necessary
	// if we set requireParenthesis to p.nodeInput(ws)[0] == '('
	requireParenthesis := true
	if elems == 1 && ws.BaseWindowName() != nil {
		requireParenthesis = false
	}

	if requireParenthesis {
		pp.print("(")
	}

	// When more than one element, the window specification spans more
	// than one line.
	if elems > 1 {
		pp.println("")

		pp2 := pp.nest()
		pp2.incDepth()
		pp2.accept(ws, d)
		pp2.decDepth()
		pp.print(pp2.unnest())

		pp.println("")
	} else {
		pp.accept(ws, d)
	}

	if requireParenthesis {
		pp.print(")")
	}

	pp.printCloseParenIfNeeded(n)
	p.print(pp.unnest())
}

func (p *Printer) VisitAndExpr(n *ast.AndExprNode, d Data) {
	conjuncts := n.Conjuncts()
	inClause := isInsideOfWhereClause(n) || isInsideOfOnClause(n)
	alignWithClause := p.fmt.opts.AlignLogicalWithClauses && inClause
	inMerge := isInsideOfMergeStatement(n)
	simple := isSimpleAndExpr(n)
	ctx := maps.Clone(d)

	budget, _ := d["align_binary_op_budget"]
	alignAnd := budget > 0

	if simple && alignAnd {
		d["align_binary_op_budget"]--
	}

	// If no budget is active, setup a new budget
	if alignAnd || inMerge || !simple && allTrue(mapIsAlignable(conjuncts)) {
		budget = 1
	}

	pp := p.nest()

	pp.moveBefore(n)
	if pp.isParenNeeded(n) {
		if !simple {
			pp.println("(")
			pp.incDepth()
		} else {
			pp.print("(")
		}
	}

	p1 := pp.nest()
	andLines := make([]int, 0, len(conjuncts)-1)

	for i, conjunct := range conjuncts {
		if i > 0 {
			if !simple {
				p1.println("")
			}

			if alignWithClause {
				nlines := strings.Count(p1.String(), "\n") + 1
				andLines = append(andLines, nlines)
			} else {
				if !simple {
					p1.print(pp.keyword("AND") + " \v")
				} else {
					p1.print(pp.keyword("AND"))
				}
			}
		} else {
			if !simple && !alignWithClause {
				p1.print("\v")
			}
		}

		ctx["align_binary_op_budget"] = budget

		if !simple {
			p1.accept(conjunct, ctx)
		} else {
			p1.acceptNested(conjunct, ctx)
		}

		p1.movePastLine(conjunct)
	}

	s := p1.unnestLeft()

	if alignWithClause {
		lines := strings.Split(s, "\n")

		for _, i := range andLines {
			lines[i] = "AND " + lines[i]
		}

		pp.print(strings.Join(lines, "\n"))
	} else {
		pp.print(s)
	}

	if pp.isParenNeeded(n) {
		if !simple {
			pp.println("")
			pp.decDepth()
		}

		pp.print(")")
	}

	if alignWithClause {
		p.print(pp.String())
	} else {
		p.print(pp.unnest())
	}
}

func (p *Printer) VisitArrayConstructor(n *ast.ArrayConstructorNode, d Data) {
	p.moveBefore(n)
	pp := p.nest()

	if t := n.Type(); t != nil {
		typ := strings.Trim(p.toString(n.Type(), d), "\n")
		pp.print(typ)
	} else {
		s := pp.nodeInput(n)
		if strings.HasPrefix(strings.ToUpper(s), "ARRAY") {
			pp.print(pp.keyword("ARRAY"))
		}
	}

	simple := allTrue(mapIsSimpleExprs(n.Elements()))

	if simple {
		pp.print("[")
		printNestedWithSep(pp, n.Elements(), d, ",")
		pp.print("]")
	} else {
		pp1 := pp.nest()
		pp1.println("[")
		pp1.incDepth()

		pp12 := pp1.nest()

		for i, elem := range n.Elements() {
			if i > 0 {
				pp12.println(",")
			}

			pp12.acceptNested(elem, d)
		}

		pp1.print(pp12.unnestLeft())

		pp1.println("")
		pp1.decDepth()
		pp1.print("]")
		pp.print(strings.TrimLeft(pp1.unnest(), "\v"))
	}

	p.print(pp.unnest())
}

func (p *Printer) VisitArrayElement(n *ast.ArrayElementNode, d Data) {
	p.moveBefore(n)
	p.printOpenParenIfNeeded(n)
	p.accept(n.Array(), d)
	p.print("[")
	p.accept(n.Position(), d)
	p.print("]")
	p.printCloseParenIfNeeded(n)
}

func (p *Printer) VisitBetweenExpression(n *ast.BetweenExpressionNode, d Data) {
	p.printOpenParenIfNeeded(n)

	p.accept(n.Lhs(), d)
	p.moveBefore(n)

	if n.IsNot() {
		p.print(p.keyword("NOT BETWEEN") + " ")
	} else {
		p.print(p.keyword("BETWEEN") + " ")
	}

	p.accept(n.Low(), d)
	p.print(p.keyword("AND"))
	p.accept(n.High(), d)

	p.printCloseParenIfNeeded(n)
}

func (p *Printer) VisitBinaryExpression(n *ast.BinaryExpressionNode, d Data) {
	p.printOpenParenIfNeeded(n)

	var (
		lhsAlign string
		rhsAlign string
	)

	if capacity, _ := d["align_binary_op_budget"]; capacity > 0 {
		d["align_binary_op_budget"]--
		lhsAlign = "\v"
		rhsAlign = " \v"
	}

	p.accept(n.Lhs(), d)
	p.movePast(n.Lhs())

	// We may have comments between the end of LHS and the beginning
	// of RHS.  Here we scan the comment-erased input to find the
	// position of binary op so we can flush comments on the right
	// side of the operator.
	b := n.Lhs().ParseLocationRange().End().ByteOffset()
	e := n.Rhs().ParseLocationRange().Start().ByteOffset()
	view := p.viewErasedInput(b, e)
	binPos := indexFunc(view, unicode.IsSpace, false)
	p.fmt.flushCommentsUpTo(b + binPos)

	switch n.Op() {
	case ast.NotSetOp:
		p.print("<UNKNOWN OPERATOR>")
	case ast.LikeOp:
		if n.IsNot() {
			p.print(lhsAlign + p.keyword("NOT LIKE") + rhsAlign)
		} else {
			p.print(lhsAlign + p.keyword("LIKE") + rhsAlign)
		}
	case ast.IsOp:
		if n.IsNot() {
			p.print(lhsAlign + p.keyword("IS NOT") + rhsAlign)
		} else {
			p.print(lhsAlign + p.keyword("IS") + rhsAlign)
		}
	case ast.EqOp:
		p.print(lhsAlign + "=" + rhsAlign)
	case ast.NeOp:
		p.print(lhsAlign + "!=" + rhsAlign)
	case ast.Ne2Op:
		p.print(lhsAlign + "<>" + rhsAlign)
	case ast.GtOp:
		p.print(lhsAlign + ">" + rhsAlign)
	case ast.LtOp:
		p.print(lhsAlign + "<" + rhsAlign)
	case ast.GeOp:
		p.print(lhsAlign + ">=" + rhsAlign)
	case ast.LeOp:
		p.print(lhsAlign + "<=" + rhsAlign)
	case ast.BitwiseOrOp:
		p.print(lhsAlign + "|" + rhsAlign)
	case ast.BitwiseXorOp:
		p.print(lhsAlign + "^" + rhsAlign)
	case ast.BitwiseAndOp:
		p.print(lhsAlign + "&" + rhsAlign)
	case ast.PlusOp:
		p.print(lhsAlign + "+" + rhsAlign)
	case ast.MinusOp:
		p.print(lhsAlign + "-" + rhsAlign)
	case ast.MultiplyOp:
		p.print(lhsAlign + "*" + rhsAlign)
	case ast.DivideOp:
		p.print(lhsAlign + "/" + rhsAlign)
	case ast.ConcatOP:
		p.print(lhsAlign + "||" + rhsAlign)
	case ast.DistinctOp:
		if n.IsNot() {
			p.print(lhsAlign + p.keyword("IS NOT DISTINCT FROM") + " " + rhsAlign)
		} else {
			p.print(lhsAlign + p.keyword("IS DISTINCT FROM") + " " + rhsAlign)
		}
	}

	p.moveBefore(n.Rhs())
	p.accept(n.Rhs(), d)
	p.movePast(n.Rhs())
	p.movePast(n)
	p.printCloseParenIfNeeded(n)
}

func (p *Printer) VisitBitwiseShiftExpression(n *ast.BitwiseShiftExpressionNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Lhs(), d)

	if n.IsLeftShift() {
		p.print("<<")
	} else {
		p.print(">>")
	}

	p.accept(n.Rhs(), d)
}

func (p *Printer) VisitCaseNoValueExpression(n *ast.CaseNoValueExpressionNode, d Data) {
	p.moveBefore(n)
	p.printOpenParenIfNeededWithDepth(n)
	p.println(p.keyword("CASE"))
	p.incDepth()

	args := n.Arguments()
	argsSimple := caseArgsGetIsSimple(args)
	simple := allTrue(argsSimple)

	d.SetBool("simple_case", simple)

	pp := p.nest()
	visitCaseArgs(pp, args, d)
	p.print(pp.unnest())

	p.println("")
	p.decDepth()
	p.print(p.keyword("END"))
	p.printCloseParenIfNeededWithDepth(n)
}

func (p *Printer) VisitCaseValueExpression(n *ast.CaseValueExpressionNode, d Data) {
	p.moveBefore(n)
	p.printOpenParenIfNeededWithDepth(n)
	p.print(p.keyword("CASE"))

	args := n.Arguments()
	argsSimple := caseArgsGetIsSimple(args)
	simple := allTrue(argsSimple)
	value := args[0]
	valueSimple := argsSimple[0]

	d.SetBool("simple_case", simple)

	if !simple || !valueSimple {
		p.println("")
		p.incDepth()
	}

	pv := p.nest()
	pv.accept(value, d)

	if valueSimple {
		p.print(strings.TrimLeft(pv.unnest(), "\v"))
	} else {
		p.print(pv.unnest())
	}

	p.println("")

	if simple {
		p.println("")
		p.incDepth()
	} else {
		p.println(" ")
	}

	pp := p.nest()
	visitCaseArgs(pp, args[1:], d)
	p.print(pp.unnest())

	p.println("")
	p.decDepth()

	p.print(p.keyword("END"))
	p.printCloseParenIfNeededWithDepth(n)
}

// visitCaseValues prints the "WHEN ... THEN .. ELSE" part.
// When coming from a CaseValueExpression, the value element removed
// before passing to this function.
func visitCaseArgs[T ast.ExpressionNode](p *Printer, args []T, d Data) {
	var (
		lhs T
		rhs T
	)

	pp := p.nest()
	simple := d.IsEnabled("simple_case")
	initAlignBinOpBudget := 0

	// If in simple mode, we need to scan whether LHS arguments are
	// of a single binary expression, so we can enable binary operator
	// alignment.
	if simple {
		if onlyBinaryExprOnCaseLHS(args) {
			initAlignBinOpBudget = 1
		}
	}

	for len(args) >= 2 {
		lhsData := maps.Clone(d)

		// When rendering a simple case, we allow a m number of
		// alignments inside the case, so that we can align binary
		// expressions.  This budget is independent for each argument
		// inside a WHEN.
		if simple {
			lhsData["align_binary_op_budget"] = initAlignBinOpBudget
		}

		lhs, rhs, args = args[0], args[1], args[2:]
		lhsSimple := isSimpleExpr(lhs)

		pp.print(pp.keyword("WHEN"))

		if simple {
			pp.print("\v")
		}

		if lhsSimple {
			lhsFmt := strings.TrimLeft(p.toString(lhs, lhsData), "\v")
			pp.print(lhsFmt)

			// Each alined binary operation will yield three vertical
			// alignment characters "|lhs |op |rhs".
			// When the LHS has not used all of its budget, we prepend
			// those vertical alignment characters so that THEN and ELSE
			// remain aligned over all rows within the same CASE.
			count := strings.Count(lhsFmt, "\v")
			remaining := initAlignBinOpBudget*3 - count

			if remaining > 1 {
				pp.print(strings.Repeat("\v", remaining-1))
			}
		} else {
			pp.println("")
			pp.incDepth()
			pp.accept(lhs, lhsData)
			pp.println("")
			pp.decDepth()
		}

		pp.print(pp.keyword("THEN"))

		if simple {
			pp.print("\v")
		} else {
			pp.println("")
			pp.incDepth()
		}

		pp.acceptNested(rhs, d)

		if len(args) >= 2 {
			pp.println("")
		}

		if !simple {
			pp.decDepth()
			pp.println(" ")
		}
	}

	if !simple {
		pp.println(" ")
	}

	if len(args) == 1 {
		pp.println("")

		if simple {
			pp.print(" ")
			pp.print(strings.Repeat("\v", 1+initAlignBinOpBudget*2))
		}

		pp.print(pp.keyword("ELSE"))

		if simple {
			pp.print("\v")
		} else {
			pp.println("")
			pp.incDepth()
		}

		p2 := pp.nest()
		p2.accept(args[0], d)
		pp.print(p2.unnestWithDepth(4))

		if !simple {
			pp.println("")
			pp.decDepth()
			pp.println(" ")
		}
	}

	p.print(pp.String())
}

func onlyBinaryExprOnCaseLHS[T ast.ExpressionNode](args []T) bool {
	for i := 0; i < len(args)-1; i += 2 {
		if !isSimpleExpr(args[i]) || args[i].Kind() != ast.BinaryExpression {
			return false
		}
	}

	return true
}

func (p *Printer) VisitCastExpression(n *ast.CastExpressionNode, d Data) {
	pp := p.nest()
	pp.moveBefore(n)

	if n.IsSafeCast() {
		pp.print(p.keyword("SAFE_CAST") + "(")
	} else {
		pp.print(p.keyword("CAST") + "(")
	}

	pp.accept(n.Expr(), d)
	pp.print(p.keyword("AS"))
	pp.accept(n.Type(), d)
	pp.accept(n.Format(), d)
	pp.print(")")
	p.print(pp.unnest())
}

func (p *Printer) VisitClampedBetweenModifier(n *ast.ClampedBetweenModifierNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("CLAMPED BETWEEN"))
	p.accept(n.Low(), d)
	p.print(p.keyword("AND"))
	p.accept(n.High(), d)
}

func (p *Printer) VisitClusterBy(n *ast.ClusterByNode, d Data) {
	p.moveBefore(n)

	p.print(p.keyword("CLUSTER"))

	p1 := p.nest()
	p1.print(p1.keyword("BY"))
	printNestedWithSep(p1, n.ClusteringExpressions(), d, ",")
	p.print(p1.unnest())
}

func (p *Printer) VisitCollate(n *ast.CollateNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("COLLATE"))
	p.accept(n.Name(), d)
}

func (p *Printer) VisitColumnAttributeList(n *ast.ColumnAttributeListNode, d Data) {
	p.moveBefore(n)

	for _, val := range n.Values() {
		p.accept(val, d)
	}

	p.movePast(n)
}

func (p *Printer) VisitConnectionClause(n *ast.ConnectionClauseNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("CONNECTION"))
	p.accept(n.ConnectionPath(), d)
}

func (p *Printer) VisitDescriptor(n *ast.DescriptorNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("DESCRIPTOR") + "(")
	p.accept(n.Columns(), d)
	p.print(")")
	p.movePast(n)
}

func (p *Printer) VisitDescriptorColumn(n *ast.DescriptorColumnNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Name(), d)
}

func (p *Printer) VisitDescriptorColumnList(n *ast.DescriptorColumnListNode, d Data) {
	p.moveBefore(n)

	for i, c := range n.DescriptorColumnList() {
		if i > 0 {
			p.print(",")
		}

		p.accept(c, d)
	}

	p.movePast(n)
}

func (p *Printer) VisitDotIdentifier(n *ast.DotIdentifierNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Expr(), d)
	p.print(".")
	p.accept(n.Name(), d)
}

func (p *Printer) VisitDotGeneralizedField(n *ast.DotGeneralizedFieldNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Expr(), d)
	p.print(".(")
	p.accept(n.Path(), d)
	p.print(")")
}

func (p *Printer) VisitDotStar(n *ast.DotStarNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Expr(), d)
	p.print(".*")
}

func (p *Printer) VisitDotStarWithModifiers(n *ast.DotStarWithModifiersNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Expr(), d)
	p.print(".*")
	p.accept(n.Modifiers(), d)
}

func (p *Printer) VisitExpressionSubquery(n *ast.ExpressionSubqueryNode, d Data) {
	p.moveBefore(n)

	switch n.Modifier() {
	case ast.ExpressionSubqueryNone:
		// Nothing.
	case ast.ExpressionSubqueryArray:
		p.print(p.keyword("ARRAY"))
	case ast.ExpressionSubqueryExists:
		p.print(p.keyword("EXISTS"))
	}

	p.println("(")
	p.incDepth()
	p.accept(n.Hint(), d)
	p.accept(n.Query(), d)
	p.decDepth()
	p.println("")
	p.print(")")
}

func (p *Printer) VisitExtractExpression(n *ast.ExtractExpressionNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("EXTRACT") + "(")

	simple := isSimpleExpr(n)
	if !simple {
		p.println("")
		p.incDepth()
	}

	// We handle the LHS date part as a type name.
	p.print(p.typename(p.toString(n.LhsExpr(), d)))

	if !simple {
		p.println("")
		p.decDepth()
	}

	p.print(p.keyword("FROM"))

	if !simple {
		p.println("")
		p.incDepth()
	}

	p.accept(n.RhsExpr(), d)

	if tz := n.TimeZoneExpr(); tz != nil {
		p.print(p.keyword("AT TIME ZONE"))
		p.accept(tz, d)
	}

	if !simple {
		p.println("")
		p.decDepth()
	}

	p.print(")")
}

func (p *Printer) VisitFormatClause(n *ast.FormatClauseNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("FORMAT"))
	p.accept(n.Format(), d)

	if tz := n.TimeZoneExpr(); tz != nil {
		p.print(p.keyword("AT TIME ZONE"))
		p.accept(n.TimeZoneExpr(), d)
	}
}

func (p *Printer) VisitForSystemTime(n *ast.ForSystemTimeNode, d Data) {
	p.moveBefore(n)
	p.println("")
	p.print(p.keyword("FOR SYSTEM_TIME AS OF"))
	p.accept(n.Expression(), d)
}

func (p *Printer) VisitFromClause(n *ast.FromClauseNode, d Data) {
	var count int

	p.moveBefore(n)

	expr := n.TableExpression()

	if expr.Kind() == ast.Join {
		if d == nil || reflect.ValueOf(d).IsNil() {
			d = make(map[string]int, 3)
		}

		count = countJoins(expr)
		d["join_count"] = count
	}

	p.accept(expr, d)

	s := n.Parent().(*ast.SelectNode)
	a, _ := locationRange(
		s.WhereClause(),
		s.GroupBy(),
		s.Having(),
		s.Qualify(),
		s.WindowClause())

	if count >= p.fmt.opts.MinJoinsToSeparateInBlocks {
		p.println("")

		// Only add an empty line if we are sure the query continues.
		if a > 0 {
			p.println(" ")
		}
	}

	if a > 0 {
		p.moveAt(a)
	}
}

func countJoins(n ast.TableExpressionNode) int {
	if n.Kind() == ast.Join {
		return 1 + countJoins(n.Child(0))
	}

	return 0
}

func (p *Printer) VisitFunctionCall(n *ast.FunctionCallNode, d Data) {
	p.moveBefore(n)

	pp := p.nest()
	pp.printOpenParenIfNeeded(n)
	pp.acceptNestedString(n.Function(), d)

	// Get function signature, if available, to assist on rendering.
	signature := p.getFunctionSignature(n)

	// Strip off the alignment symbol at the beginning.
	expr := pp.unnest()[1:]

	pp = p.nest()

	// If the function call has too many elements, we split in one line
	// per element.
	args := n.Arguments()
	elems := countFunctionCallElements(n)
	// multiline := p.maybeMultilineFunctionCall(n)
	simple := len(args) <= 4 && elems <= 1 && onlySimpleFunctionCallArgs(n)

	pp.print(pp.function(expr))
	pp.print("(")

	if !simple {
		pp.println("")
		pp.incDepth()
	}

	if n.Distinct() {
		pp.print(pp.keyword("DISTINCT"))

		if !simple {
			pp.println("")
		}
	}

	pp2 := pp.nest()

	for i, arg := range args {
		if i > 0 {
			pp2.print(",")

			if !simple {
				pp2.println("")
			}
		}

		printedArg := strings.Trim(pp2.toString(arg, d), "\n")
		if strings.Contains(printedArg, "\n") {
			pp2.println("")
		}

		switch arg.(type) {
		case *ast.PathExpressionNode:
			sigStyle := signature.PrintCaseAt(i)
			pp2.print(pp2.identifierWithCase(printedArg, sigStyle))
		default:
			pp2.print(printedArg)
		}
	}

	switch n.NullHandlingModifier() {
	case ast.DefaultNullHandling:
		// Nothing.
	case ast.IgnoreNulls:
		if !simple {
			pp2.println("")
		}

		pp2.print(pp2.keyword("IGNORE NULLS"))
	case ast.RespectNulls:
		if !simple {
			pp2.println("")
		}

		pp2.print(pp2.keyword("RESPECT NULLS"))
	}

	if !simple {
		pp2.println("")
	}

	pp2.accept(n.HavingModifier(), d)

	if !simple {
		pp2.println("")
	}

	pp2.accept(n.ClampedBetweenModifier(), d)

	if !simple {
		pp2.println("")
	}

	pp2.accept(n.OrderBy(), d)

	if !simple {
		pp2.println("")
	}

	pp2.accept(n.LimitOffset(), d)

	pp.print(pp2.unnest())

	if !simple {
		pp.println("")
		pp.decDepth()
	}

	pp.print(")")
	pp.printCloseParenIfNeeded(n)
	p.print(pp.unnest())
}

func (p *Printer) VisitGroupBy(n *ast.GroupByNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Hint(), d)
	p.print(p.keyword("BY"))

	pp := p.nest()
	printNestedWithSep(pp, n.GroupingItems(), d, ",")
	p.print(pp.unnest())

	s := n.Parent().(*ast.SelectNode)
	a, _ := locationRange(
		s.Having(),
		s.Qualify(),
		s.WindowClause())

	if a > 0 {
		p.moveAt(a)
	}
}

func (p *Printer) VisitGroupingItem(n *ast.GroupingItemNode, d Data) {
	p.moveBefore(n)

	p.accept(n.Expression(), d)
	p.accept(n.Rollup(), d)
}

func (p *Printer) VisitIdentifier(n *ast.IdentifierNode, d Data) {
	p.moveBefore(n)

	r := n.ParseLocationRange()
	start := r.Start().ByteOffset()
	end := r.End().ByteOffset()

	if start > 0 && viewStringAt(p.input, start-1) == '`' {
		start--
		end++
	}

	p.print(p.identifier(p.viewInput(start, end)))
}

func (p *Printer) VisitIdentifierList(n *ast.IdentifierListNode, d Data) {
	p.moveBefore(n)
	printNestedWithSep(p, n.IdentifierList(), d, ",")
}

func (p *Printer) VisitInExpression(n *ast.InExpressionNode, d Data) {
	p.moveBefore(n)
	p.printOpenParenIfNeeded(n)
	p.accept(n.Lhs(), d)

	if n.IsNot() {
		p.print(p.keyword("NOT IN"))
	} else {
		p.print(p.keyword("IN"))
	}

	p.accept(n.Hint(), d)

	// Exactly one of InList, UnnestExpr, or Query is present.
	p.accept(n.InList(), d)
	p.accept(n.UnnestExpr(), d)

	// A query may be IN (SELECT 1) or IN ((SELECT 1)), where the first
	// is parsed as not parenthesized but the seconde one is.
	if q := n.Query(); q != nil {
		p.println("(")
		p.incDepth()
		p.accept(q, d)
		p.decDepth()
		p.println("")
		p.print(")")
	}

	p.printCloseParenIfNeeded(n)
	p.movePast(n)
}

func (p *Printer) VisitInList(n *ast.InListNode, d Data) {
	p.moveBefore(n)

	p.print("(")

	elems := n.List()
	simple := allTrue(mapIsSimpleExprs(elems))

	if !simple {
		p.println("")
		p.incDepth()
	}

	for i, elem := range elems {
		if i > 0 {
			p.print(",")

			if !simple {
				p.println("")
			}
		}

		p.accept(elem, d)
	}

	if !simple {
		p.println("")
		p.decDepth()
	}

	p.print(")")
}

func (p *Printer) VisitIntervalExpr(n *ast.IntervalExprNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("INTERVAL"))
	p.accept(n.InternalValue(), d)

	pp := p.nest()
	pp.accept(n.DatePartName(), d)

	if to := n.DatePartNameTo(); to != nil {
		pp.print(pp.keyword("TO"))
		pp.accept(to, d)
	}

	p.print(p.keyword(pp.unnest()))
	p.movePast(n)
}

func (p *Printer) VisitHint(n *ast.HintNode, d Data) {
	// We use a strings builder here because we don't want automatic
	// spaces between token separators.
	var b strings.Builder

	p.moveBefore(n)

	if shards := n.NumShardsHint(); shards != nil {
		p.print("@")
		p.accept(shards, d)
	}

	entries := n.HintEntries()
	if len(entries) > 0 {
		b.WriteString("@{")

		for i, h := range n.HintEntries() {
			if i > 0 {
				p.print(",")
			}

			b.WriteString(p.toString(h.Name(), d))
			b.WriteString("=")
			b.WriteString(p.toString(h.Value(), d))
		}

		b.WriteString("}")
	}

	p.print(b.String())
}

func (p *Printer) VisitHintedStatement(n *ast.HintedStatementNode, d Data) {
	p.accept(n.Hint(), d)
	p.println("")
	p.accept(n.Statement(), d)
}

func (p *Printer) VisitHavingModifier(n *ast.HavingModifierNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("HAVING"))

	switch n.ModifierKind() {
	case ast.HavingModifierNotSet:
		// Nothing.
	case ast.HavingModifierMin:
		p.print(p.keyword("MIN"))
	case ast.HavingModifierMax:
		p.print(p.keyword("MAX"))
	}

	p.accept(n.Expr(), d)
}

func (p *Printer) VisitHaving(n *ast.HavingNode, d Data) {
	p.moveBefore(n)
	p.visitMaybeClauseAligned(n.Expression(), d)
}

func (p *Printer) VisitJoin(n *ast.JoinNode, d Data) {
	// We should keep in mind that, in the AST, joins are structured
	// from the last on the top to the first on the bottom.
	var count int

	count, _ = d["join_count"]

	pp := p.nest()
	pp.accept(n.Lhs(), d)
	pp.movePast(n.Lhs())

	switch n.JoinType() {
	case ast.CommaJoinType:
		pp.print(",")
	case ast.DefaultJoinType, ast.CrossJoinType, ast.FullJoinType,
		ast.InnerJoinType, ast.LeftJoinType, ast.RightJoinType:
		if count >= p.fmt.opts.MinJoinsToSeparateInBlocks {
			pp.println("")
		}

		pp.moveBefore(n)
		pp.println("\v")
		pp.print(p.keyword(p.joinKeyword(n)))
	}

	pp.accept(n.Hint(), d)

	pp.println("")

	pp2 := p.nest()
	pp2.accept(n.Rhs(), d)
	pp2.movePast(n.Rhs())
	pp.print(pp2.unnest())

	if oc := n.OnClause(); oc != nil {
		pp.println("")
		pp.acceptNested(oc, d)
	}

	if uc := n.UsingClause(); uc != nil {
		pp.println("")
		pp.acceptNested(uc, d)
	}

	// pp.movePast(n)
	p.print(pp.unnestLeft())
}

// joinKeyword returns the SQL representation for the join.  We try to
// keep with the same specification from the input.  For example,
// FULL JOIN and FULL OUTER JOIN are equivalent, but we want to keep
// the one the input contains.
func (p *Printer) joinKeyword(n *ast.JoinNode) string {
	var kw strings.Builder

	// Capacity for the largest keyword possible: NATURAL RIGHT OUTER JOIN.
	kw.Grow(24)

	if n.Natural() {
		kw.WriteString("NATURAL ")
	}

	switch n.JoinType() {
	case ast.CrossJoinType:
		kw.WriteString("CROSS ")
	case ast.FullJoinType:
		kw.WriteString("FULL ")
	case ast.InnerJoinType:
		kw.WriteString("INNER ")
	case ast.LeftJoinType:
		kw.WriteString("LEFT ")
	case ast.RightJoinType:
		kw.WriteString("RIGHT ")
	case ast.DefaultJoinType, ast.CommaJoinType:
		// Nothing.
	}

	switch n.JoinHint() {
	case ast.NoJoinHint:
		// Nothing.
	case ast.HashJoinHint:
		kw.WriteString("HASH ")
	case ast.LookupJoinHint:
		kw.WriteString("LOOKUP ")
	}

	begin := n.ParseLocationRange().Start().ByteOffset()
	end := n.Rhs().ParseLocationRange().Start().ByteOffset()
	str := strings.ToUpper(p.viewInput(begin, end))

	if strings.Contains(str, "OUTER") {
		kw.WriteString("OUTER ")
	}

	kw.WriteString("JOIN")

	return kw.String()
}

func (p *Printer) VisitLimitOffset(n *ast.LimitOffsetNode, d Data) {
	p.print(p.keyword("LIMIT"))

	p2 := p.nest()
	p2.moveBefore(n)
	p2.accept(n.Limit(), d)

	if os := n.Offset(); os != nil {
		p2.print(p2.keyword("OFFSET"))
		p2.accept(os, d)
	}

	p2.moveBeforeSuccessorOf(n)
	p.print(p2.unnest())
}

func (p *Printer) visitMaybeClauseAligned(n ast.ExpressionNode, d Data) {
	pp := p.nest()

	// If the WHERE clause contains AND or OR, we will format them
	// as if they were clauses, right-aligned with the WHERE clause.
	switch n.Kind() {
	case ast.AndExpr:
		bin := n.(*ast.AndExprNode)

		for i, conjunct := range bin.Conjuncts() {
			if i > 0 {
				if p.fmt.opts.AlignLogicalWithClauses {
					// Clear buffer and write AND as a clause.
					p.print(pp.unnest())
					p.printClause(p.keyword("AND"))
					// Create new nested builder.
					pp = p.nest()
				} else {
					p.printClause(p.keyword("AND"))
				}
			}

			pp.acceptNested(conjunct, d)
		}
	case ast.OrExpr:
		bin := n.(*ast.OrExprNode)

		for i, disjunct := range bin.Disjuncts() {
			if i > 0 {
				if p.fmt.opts.AlignLogicalWithClauses {
					p.print(pp.unnest())
					p.printClause(p.keyword("OR"))
					p = p.nest()
				} else {
					p.printClause(p.keyword("OR"))
				}
			}

			pp.acceptNested(disjunct, d)
		}
	default:
		pp.accept(n, d)
	}

	pp.moveBeforeSuccessorOf(n)
	p.print(pp.unnest())
}

func (p *Printer) VisitModelClause(n *ast.ModelClauseNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("MODEL"))
	p.accept(n.ModelPath(), d)
}

func (p *Printer) VisitNamedArgument(n *ast.NamedArgumentNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Name(), d)
	p.print("=>")

	expr := n.Expr()
	simple := isSimpleExpr(expr)

	if !simple {
		p.println("")
		p.incDepth()
	}

	p.accept(n.Expr(), d)

	if !simple {
		p.println("")
		p.decDepth()
	}
}

func (p *Printer) VisitNullOrder(n *ast.NullOrderNode, d Data) {
	if n.NullsFirst() {
		p.print(p.keyword("NULLS FIRST"))
	} else {
		p.print(p.keyword("NULLS LAST"))
	}
}

func (p *Printer) VisitOnClause(n *ast.OnClauseNode, d Data) {
	p1 := p.nest()
	p1.printClause(p1.keyword("ON"))
	p1.moveBefore(n)
	p1.accept(n.Expression(), d)
	p.print(p1.unnestLeft())
}

func (p *Printer) VisitOptionsList(n *ast.OptionsListNode, d Data) {
	entries := n.OptionsEntries()
	simple := len(entries) <= 1 && allTrue(mapIsSimpleOptionsList(n))
	d.SetBool("options_simple", simple)

	pp := p.nest()

	pp.print(pp.keyword("OPTIONS") + " (")

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

func (p *Printer) VisitOptionsEntry(n *ast.OptionsEntryNode, d Data) {
	keys := knownOptionKeys(n.Parent().(*ast.OptionsListNode))
	simple := d.IsEnabled("options_simple")

	pp := p.nest()
	key := keys.Get(pp.toString(n.Name(), d))
	value := pp.toUnnestedString(n.Value(), d)

	if simple {
		pp.print(key + "=" + value)
	} else {
		// We need to add an additional vertical aligned to compensate
		// for the one were adding to the equal symbol.
		pp.print(key + " \v= " + strings.ReplaceAll(value, "\n", "\n\v"))
	}

	p.print(pp.String())
}

func (p *Printer) VisitOrExpr(n *ast.OrExprNode, d Data) {
	disjuncts := n.Disjuncts()
	p1 := p.nest()
	inClause := isInsideOfWhereClause(n) || isInsideOfOnClause(n)
	simple := allTrue(mapIsSimpleExprs(disjuncts)) && (len(disjuncts) < 4)

	p.moveBefore(n)
	if p.isParenNeeded(n) {
		if !simple {
			p.println("(")
			p.incDepth()
		} else {
			p.print("(")
		}
	}

	for i, disjunct := range disjuncts {
		if i > 0 {
			if simple && !inClause {
				p1.print(p1.keyword("OR"))
			} else {
				if p1.fmt.opts.AlignLogicalWithClauses && inClause {
					// Clear buffer and write AND as a clause.
					p.print(p1.unnest())
					p.printClause(p.keyword("OR"))
					// Create new nested builder.
					p1 = p.nest()
				} else {
					p1.printClause(p1.keyword("OR"))
				}
			}
		}

		if disjunct.Kind() == ast.AndExpr {
			p1.accept(disjunct, d)
		} else {
			p1.acceptNested(disjunct, d)
		}

		p1.movePastLine(disjunct)
	}

	p.print(p1.unnestLeft())
	if p.isParenNeeded(n) {
		if !simple {
			p.println("")
			p.decDepth()
		}

		p.print(")")
	}
}

func (p *Printer) VisitOrderBy(n *ast.OrderByNode, d Data) {
	p.moveBefore(n)

	if n.Parent().Kind() == ast.Query {
		p.printClause(p.keyword("ORDER"))
	} else {
		p.print(p.keyword("ORDER"))
	}

	p1 := p.nest()
	p1.accept(n.Hint(), d)
	p1.print(p1.keyword("BY"))
	p1.moveBefore(n)
	printNestedWithSep(p1, n.OrderingExpressions(), d, ",")
	p1.moveBeforeSuccessorOf(n)
	p.print(p1.unnest())
}

func (p *Printer) VisitOrderingExpression(n *ast.OrderingExpressionNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Expression(), d)
	p.accept(n.Collate(), d)

	switch n.OrderingSpec() {
	case ast.NotSetSpec:
		// No op.
	case ast.AscSpec:
		p.print(p.keyword("ASC"))
	case ast.DescSpec:
		p.print(p.keyword("DESC"))
	case ast.UnspecifiedSpec:
		// No op.
	}

	p.accept(n.NullOrder(), d)
	p.movePast(n)
}

func (p *Printer) VisitParameterExpr(n *ast.ParameterExprNode, d Data) {
	p.moveBefore(n)

	if n.Position() == 0 {
		p.print("@")
		p.accept(n.Name(), d)
	} else {
		p.print("?")
	}
}

func (p *Printer) VisitParenthesizedJoin(n *ast.ParenthesizedJoinNode, d Data) {
	p.moveBefore(n)
	p.println("(")
	p.incDepth()
	p.accept(n.Join(), d)
	p.decDepth()
	p.println("")
	p.print(")")
	p.accept(n.SampleClause(), d)
}

func (p *Printer) VisitPartitionBy(n *ast.PartitionByNode, d Data) {
	p.moveBefore(n)

	p.print(p.keyword("PARTITION"))

	p1 := p.nest()
	p1.accept(n.Hint(), d)
	p1.print(p1.keyword("BY"))
	printNestedWithSep(p1, n.PartitioningExpressions(), d, ",")
	p.print(p1.unnest())
}

func (p *Printer) VisitPathExpression(n *ast.PathExpressionNode, d Data) {
	p.moveBefore(n)
	p.printOpenParenIfNeeded(n)

	for i, name := range n.Names() {
		if i > 0 {
			p.print(".")
		}

		p.accept(name, d)
	}

	p.printCloseParenIfNeeded(n)
}

func (p *Printer) VisitPathExpressionList(n *ast.PathExpressionListNode, d Data) {
	p.moveBefore(n)

	exprs := n.PathExpressionList()

	parens := len(exprs) > 1
	if parens {
		p.print("(")
	}

	simple := allTrue(mapIsSimplePathExpressionList(n))

	for i, name := range n.PathExpressionList() {
		if i > 0 {
			p.print(",")

			if !simple {
				p.println("")
			}
		}

		p.accept(name, d)
	}

	if parens {
		p.print(")")
	}
}

func (p *Printer) VisitPivotClause(n *ast.PivotClauseNode, d Data) {
	p.moveBefore(n)
	p.println("")
	p.print(p.keyword("PIVOT") + " (")
	p.println("")
	p.incDepth()
	p.VisitPivotExpressionList(mustGetPivotExpressionList(n), d)
	p.println("")
	p.visitPivotForExpression(n, d)
	p.println("")
	p.decDepth()
	p.print(")")
	p.accept(n.OutputAlias(), d)
	p.movePast(n)
}

func (p *Printer) VisitPivotExpression(n *ast.PivotExpressionNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Expression(), d)
	p.accept(n.Alias(), d)
	p.movePast(n)
}

func (p *Printer) visitPivotForExpression(n *ast.PivotClauseNode, d Data) {
	// For the structure "FOR <lhs> IN (<rhs>)":
	//   simple(<lhs>)                  => format <lhs> in single line
	//   simple(<lhs>) & simple(<rhs>)  => format <rhs> in single line
	//   <lhs> and <rhs> are formatted as multiline otherwise.
	exprsSimple := mapIsSimplePivotForExpression(n)
	simpleLHS := exprsSimple[0]
	simpleRHS := allTrue(exprsSimple[1:])
	simpleValues := simpleLHS && simpleRHS
	ctx := maps.Clone(d)

	ctx.SetBool("pivot_for_expr_lhs_simple", simpleLHS)
	ctx.SetBool("pivot_for_expr_rhs_simple", simpleRHS)
	ctx.SetBool("pivot_for_expr_values_simple", simpleValues)

	p.print(p.keyword("FOR"))

	if !simpleLHS {
		p.println("")
		p.incDepth()
	}

	p.accept(n.ForExpression(), ctx)

	if !simpleLHS {
		p.println("")
		p.decDepth()
	}

	p.print(p.keyword("IN") + " (")

	if !simpleValues {
		p.println("")
		p.incDepth()
	}

	p.accept(mustGetPivotValueList(n), ctx)

	if !simpleValues {
		p.println("")
		p.decDepth()
	}

	p.print(")")
}

func (p *Printer) VisitPivotExpressionList(n *ast.PivotExpressionListNode, d Data) {
	p.moveBefore(n)

	exprs := n.Expressions()
	simple := allTrue(mapIsSimplePivotExpressionList(n))

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

func (p *Printer) VisitPivotValue(n *ast.PivotValueNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Value(), d)
	p.accept(n.Alias(), d)
	p.movePast(n)
}

func (p *Printer) VisitPivotValueList(n *ast.PivotValueListNode, d Data) {
	p.moveBefore(n)

	simple := d.IsEnabled("pivot_for_expr_values_simple")

	for i, v := range n.Values() {
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

func (p *Printer) VisitQualify(n *ast.QualifyNode, d Data) {
	pp := p.nest()
	pp.moveBefore(n)
	p.visitMaybeClauseAligned(n.Expression(), d)
	pp.moveBeforeSuccessorOf(n)
	p.print(pp.unnest())
}

func (p *Printer) VisitQuery(n *ast.QueryNode, d Data) {
	pp := p

	// Normally a with entry has no indentation, but when rendering
	// a WITH inside a WITH, we need to render the inner WITH at a
	// deeper indentation.
	nestedWith := withInsideWith(n)
	if nestedWith {
		pp.incDepth()
	}

	pp.moveBefore(n)
	pp.printOpenParenIfNeeded(n)
	pp.accept(n.WithClause(), d)
	pp.accept(n.QueryExpr(), d)

	if ob := n.OrderBy(); ob != nil {
		pp.println("")
		pp.accept(n.OrderBy(), d)
	}

	if lo := n.LimitOffset(); lo != nil {
		pp.println("")
		pp.accept(n.LimitOffset(), d)
	}

	if parent := n.Parent(); parent != nil && parent.Kind() != ast.QueryStatement {
		pp.movePast(n)
	}

	if nestedWith {
		pp.decDepth()
	}

	pp.printCloseParenIfNeeded(n)
}

func (p *Printer) VisitQueryStatement(n *ast.QueryStatementNode, d Data) {
	p.moveBefore(n)

	p1 := p.nest()
	p1.accept(n.Query(), d)
	p.print(p1.unnest())
}

func (p *Printer) VisitRepeatableClause(n *ast.RepeatableClauseNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("REPEATABLE"))
	p.print("(")
	p.accept(n.Argument(), d)
	p.print(")")
}

func (p *Printer) VisitRollup(n *ast.RollupNode, d Data) {
	p.print(p.keyword("ROLLUP"))
	p.print("(")

	for i, expr := range n.Expressions() {
		if i > 0 {
			p.print(",")
		}

		p.accept(expr, d)
	}

	p.print(")")
}

func (p *Printer) VisitSampleClause(n *ast.SampleClauseNode, d Data) {
	p.moveBefore(n)
	p.println("")
	p.print(p.keyword("TABLESAMPLE"))

	// Sample method is an identifier, but here I decided to treat it
	// like a keyword.
	p.print(p.keyword(p.toString(n.SampleMethod(), d)) + " ")

	p.print("(")
	p.accept(n.SampleSize(), d)
	p.print(")")
	p.accept(n.SampleSuffix(), d)
}

func (p *Printer) VisitSampleSize(n *ast.SampleSizeNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Size(), d)

	switch n.Unit() {
	case ast.SampleSizeNotSet:
		// Nothing.
	case ast.SampleSizeRows:
		p.print(p.keyword("ROWS"))
	case ast.SampleSizePercent:
		p.print(p.keyword("PERCENT"))
	}

	p.accept(n.PartitionBy(), d)
}

func (p *Printer) VisitSampleSuffix(n *ast.SampleSuffixNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Weight(), d)
	p.accept(n.Repeat(), d)
}

func (p *Printer) VisitScript(n *ast.ScriptBaseNode, d Data) {
	for i := 0; i < n.NumChildren(); i++ {
		p.moveBefore(n.Child(i))
		p.accept(n.Child(i), d)
	}

	// p.movePast(n)
}

func (p *Printer) VisitSelect(n *ast.SelectNode, d Data) {
	p.moveBefore(n)

	pp := p.nest()
	pp.printOpenParenIfNeeded(n)

	pp2 := pp.nest()
	pp2.printClause(pp2.keyword("SELECT"))

	pp3 := pp2.nest()
	pp3.accept(n.Hint(), d)
	pp3.accept(n.AnonymizationOptions(), d)

	singleLine := p.maybeSingleLineColumns(n)
	d.SetBool("single_line_cols", singleLine)

	if n.Distinct() {
		pp3.print(pp3.keyword("DISTINCT"))

		if n.SelectAs() == nil && !singleLine {
			pp3.println("")
		}
	}

	pp3.accept(n.SelectAs(), d)
	pp3.accept(n.SelectList(), d)

	fc := n.FromClause()
	w := n.WhereClause()
	gb := n.GroupBy()
	h := n.Having()
	q := n.Qualify()
	win := n.WindowClause()

	if fc != nil {
		pp3.moveBefore(fc)
	}

	pp2.print(pp3.unnest())

	if fc != nil {
		pp2.printClause(pp2.keyword("FROM"))
		pp2.acceptNested(fc, d)
	}

	if w != nil {
		pp2.printClause(pp2.keyword("WHERE"))
		pp2.accept(w, d)
	}

	if gb != nil {
		pp2.moveBefore(gb)
		pp2.printClause(pp2.keyword("GROUP") + " ")
		pp2.acceptNestedLeft(gb, d)
	}

	if h != nil {
		pp2.moveBefore(h)
		pp2.printClause(pp2.keyword("HAVING"))
		pp2.accept(h, d)
	}

	if q != nil {
		pp2.moveBefore(q)
		pp2.printClause(pp2.keyword("QUALIFY"))
		pp2.accept(q, d)
	}

	if win != nil {
		pp2.moveBefore(win)
		pp2.printClause(pp2.keyword("WINDOW"))
		pp2.acceptNested(win, d)
	}

	// We may have a comment on the last line of the select.

	// If this select is inside a Query node, we want to possibly align
	// SELECT, FROM, WHERE and other clauses with Query's ORDER BY and LIMIT.
	// Thus, we will not unnest is this case.
	k := n.Parent().Kind()
	if k == ast.Query || k == ast.SetOperation {
		pp.print(pp2.String())
		p.print(pp.String())
	} else {
		pp.print(pp2.unnest())
		pp.printCloseParenIfNeeded(n)
		pp.println("")
		p.print(pp.unnest())
	}
}

func (p *Printer) VisitSelectAs(n *ast.SelectAsNode, d Data) {
	switch n.AsMode() {
	case ast.NotSetMode:
		// Nothing.
	case ast.StructMode:
		p.println(p.keyword("AS STRUCT"))
	case ast.ValueMode:
		p.print(p.keyword("AS VALUE"))
	case ast.TypeNameMode:
		p.print(p.keyword("AS"))
		p.accept(n.TypeName(), d)
	}

	p.println("")
}

func (p *Printer) VisitSelectColumn(n *ast.SelectColumnNode, d Data) {
	// SelectColumnNode.Expression is theoretically required, but for
	// JSON literals we have a bug on go-zetasql that fails to read
	// the expression and returns nil instead.  To avoid a fatal failure,
	// the current workaround is to pipe through the input.
	if !nodeDefined(n.Child(0)) {
		// Apply patch specific for JSON literals, as this is the only
		// one observed with problems.
		s := p.nodeInput(n)
		if strings.HasPrefix(strings.ToUpper(s), "JSON ") {
			json := s[len("JSON "):]
			json = strings.ReplaceAll(json, "\n", lineBreakPlaceholder)

			s = p.keyword("JSON") + " " + json
		} else {
			// There should be no node should reach here.
			log.Printf(
				"[WARN] fallback to pass input through due to "+
					" null expression on select column at [%s]:\n%s\n",
				n.LocationString(),
				n.DebugString(10),
			)
		}

		p.print(s)

		return
	}

	pp := p.nest()
	pp.accept(n.Expression(), d)
	p.print(pp.unnest())

	if n.Alias() != nil {
		pp = p.nest()
		pp.accept(n.Alias(), d)
		p.print(pp.unnest())
	}
}

func (p *Printer) VisitSelectList(n *ast.SelectListNode, d Data) {
	pp := p.nest()
	singleLine := d.IsEnabled("single_line_cols")

	var prev ast.Node

	for i, c := range n.Columns() {
		if i > 0 {
			pp.print(",")

			pp.movePastLine(prev)

			if !singleLine {
				pp.println("")
			}
		}

		pp.moveBefore(c)

		// We don't use pp.acceptNested() here because we will only
		// unnest after generating all columns.
		pp.acceptNestedString(c, d)
		prev = c
	}
	pp.movePastLine(prev)

	p.print(pp.unnestLeft())
}

func (p *Printer) VisitSetOperation(n *ast.SetOperationNode, d Data) {
	p.printOpenParenIfNeeded(n)

	for i, query := range n.Inputs() {
		if i > 0 {
			switch n.OpType() {
			case ast.NotSetOperation:
				p.print(p.keyword("<UNKNOWN SET OPERATOR>"))
			case ast.UnionSetOperation:
				p.print(p.keyword("UNION"))
			case ast.ExceptSetOperation:
				p.print(p.keyword("EXCEPT"))
			case ast.IntersectSetOperation:
				p.print(p.keyword("INTERSECT"))
			default:
				if int(n.OpType()) == 4 {
					p.print(p.keyword("INTERSECT"))
				} else {
					p.addError(&PrinterError{
						Msg:   fmt.Sprintf("Unknown set operation with code %d", int(n.OpType())),
						Node:  n,
						Input: &p.input,
					})
				}
			}

			p.print("\v")
			p.accept(n.Hint(), d)

			if n.Distinct() {
				p.print(p.keyword("DISTINCT"))
			} else {
				p.print(p.keyword("ALL"))
			}

			p.println("")
		}

		p.accept(query, d)
		p.println("")
	}

	p.printCloseParenIfNeeded(n)
}

func (p *Printer) VisitStar(n *ast.StarNode, d Data) {
	p.moveBefore(n)

	p.print(n.Image())
}

func (p *Printer) VisitStarModifiers(n *ast.StarModifiersNode, d Data) {
	if el := n.ExceptList(); el != nil {
		p.print(p.keyword("EXCEPT"))
		p.print("(")

		for i, e := range el.Identifiers() {
			if i > 0 {
				p.print(",")
			}

			p.accept(e, d)
		}

		p.print(")")
	}

	if items := n.ReplaceItems(); len(items) > 0 {
		p.println("")
		p.print(p.keyword("REPLACE"))
		p.println("(")
		p.incDepth()

		for i, e := range items {
			if i > 0 {
				p.print(",")
				p.println("")
			}

			p.accept(e, d)
		}

		p.decDepth()
		p.println("")
		p.print(")")
	}
}

func (p *Printer) VisitStarReplaceItem(n *ast.StarReplaceItemNode, d Data) {
	p.accept(n.Expression(), d)

	if a := n.Alias(); a != nil {
		p.print(p.keyword("AS"))
		p.accept(n.Alias(), d)
	}
}

func (p *Printer) VisitStarWithModifiers(n *ast.StarWithModifiersNode, d Data) {
	p.moveBefore(n)
	p.print("*")
	p.accept(n.Modifiers(), d)
}

func (p *Printer) VisitStatementList(n *ast.StatementListNode, d Data) {
	var prev ast.Node

	num := n.NumChildren()

	p.moveBefore(n)

	for i := 0; i < num; i++ {
		c := n.Child(i)
		curr := c.Kind()

		if i > 0 {
			p.print(";")
			p.movePastLine(prev)
			p.println("")

			if !canGroupStatements(prev.Kind(), curr) {
				p.println(" ")
			}
		}

		p.acceptNested(c, d)
		p.movePast(c)
		prev = c
	}

	parent := n.Parent()

	topLevel := !nodeDefined(parent) || parent.Kind() == ast.Script

	// p.movePast(n)

	if num > 1 || (num > 0 && !topLevel) {
		p.println(";")

		if prev != nil {
			p.movePastLine(prev)
		}
	}

	p.movePastLine(n)
}

func canGroupStatements(last, curr ast.Kind) bool {
	if curr != last {
		return false
	}

	if curr == ast.VariableDeclaration {
		return true
	}

	return false
}

func (p *Printer) VisitStructColumnField(n *ast.StructColumnFieldNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Name(), d)
	p.accept(n.Schema(), d)
	p.movePast(n)
}

func (p *Printer) VisitTableElementList(n *ast.TableElementListNode, d Data) {
	elems := n.Elements()

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

	for i, e := range n.Elements() {
		if i > 0 {
			pp.println(",")
		}

		pp.accept(e, d)
	}

	p.print(pp.unnestLeft())

	p.println("")
	p.decDepth()
	p.print(")")
	p.movePast(n)
}

func (p *Printer) VisitTablePathExpression(n *ast.TablePathExpressionNode, d Data) {
	p.accept(n.PathExpr(), d)
	p.accept(n.UnnestExpr(), d)
	p.accept(n.Hint(), d)
	p.accept(n.Alias(), d)
	p.accept(n.WithOffset(), d)
	p.accept(n.PivotClause(), d)
	p.accept(n.UnpivotClause(), d)
	p.accept(n.ForSystemTime(), d)
	p.accept(n.SampleClause(), d)
}

func (p *Printer) VisitTableSubquery(n *ast.TableSubqueryNode, d Data) {
	p.moveBefore(n)
	p.println("(")
	p.incDepth()
	p.acceptNested(n.Subquery(), d)
	p.println("")
	p.decDepth()
	p.print(")")
	p.accept(n.PivotClause(), d)
	p.accept(n.UnpivotClause(), d)
	p.accept(n.SampleClause(), d)
	p.accept(n.Alias(), d)
}

func (p *Printer) VisitStructConstructorArg(n *ast.StructConstructorArgNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Expression(), d)
	p.accept(n.Alias(), d)
}

func (p *Printer) VisitStructConstructorWithKeyword(n *ast.StructConstructorWithKeywordNode, d Data) {
	p.moveBefore(n)
	p.printOpenParenIfNeededWithDepth(n)

	if n.StructType() != nil {
		pp := p.nest()
		pp.accept(n.StructType(), d)

		typ := pp.unnest()
		p.print(typ + "(")
	} else {
		p.print(p.keyword("STRUCT") + "(")
	}

	fields := n.Fields()
	simple := allTrue(mapIsSimpleStructConstructorArg(fields))

	if !simple {
		p.println("")
		p.incDepth()
	}

	for i, e := range fields {
		if i > 0 {
			p.print(",")

			if !simple {
				p.println("")
			}
		}

		p.accept(e, d)
	}

	if !simple {
		p.println("")
		p.decDepth()
	}

	p.print(")")
	p.printCloseParenIfNeededWithDepth(n)
}

func (p *Printer) VisitStructConstructorWithParens(n *ast.StructConstructorWithParensNode, d Data) {
	p.moveBefore(n)
	p.printOpenParenIfNeededWithDepth(n)
	p.print("(")

	simple := allTrue(mapIsSimpleExprs(n.FieldExpressions()))

	if !simple {
		p.println("")
		p.incDepth()
	}

	for i, e := range n.FieldExpressions() {
		if i > 0 {
			p.print(",")

			if !simple {
				p.println("")
			}
		}

		p.accept(e, d)
	}

	if !simple {
		p.println("")
		p.decDepth()
	}

	p.print(")")
	p.printCloseParenIfNeededWithDepth(n)
}

func (p *Printer) VisitSystemVariableExpr(n *ast.SystemVariableExprNode, d Data) {
	p.moveBefore(n)
	p.printOpenParenIfNeeded(n)
	p.print("@@")
	p.accept(n.Path(), d)
	p.printCloseParenIfNeeded(n)
}

func (p *Printer) VisitTableClause(n *ast.TableClauseNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("TABLE"))
	p.accept(n.TablePath(), d)
	p.accept(n.TVF(), d)
}

func (p *Printer) VisitTVFArgument(n *ast.TVFArgumentNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Expr(), d)
	p.accept(n.TableClause(), d)
	p.accept(n.ModelClause(), d)
	p.accept(n.ConnectionClause(), d)
	p.accept(n.Descriptor(), d)
	p.movePast(n)
}

func (p *Printer) VisitTVF(n *ast.TVFNode, d Data) {
	p.moveBefore(n)

	args := n.ArgumentEntries()
	simple := allTrue(mapIsSimpleTVFArguments(args))

	p.print(p.function(p.toString(n.Name(), d)) + "(")

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

	if b, e := locationRange(
		n.Hint(),
		n.Alias(),
		n.PivotClause(),
		n.UnpivotClause(),
		n.SampleClause(),
	); e > 0 {
		// There will be more elements to render, flush comments up to
		// the beginning of next element.
		p.fmt.flushCommentsUpTo(b)
	} else {
		// No other elements will be rendered, flush up to the closing
		// parenthesis.
		p.movePast(n)
	}

	if !simple {
		p.println("")
		p.decDepth()
	}

	p.print(")")
	p.accept(n.Hint(), d)
	p.accept(n.Alias(), d)
	p.accept(n.PivotClause(), d)
	p.accept(n.UnpivotClause(), d)
	p.accept(n.SampleClause(), d)
	p.movePast(n)
}

func (p *Printer) VisitTVFSchema(n *ast.TVFSchemaNode, d Data) {
	p.moveBefore(n)
}

func (p *Printer) VisitTVFSchemaColumn(n *ast.TVFSchemaColumnNode, d Data) {
	p.moveBefore(n)
}

func (p *Printer) VisitTypeParameterList(n *ast.TypeParameterListNode, d Data) {
	p.moveBefore(n)
	p.print("(")
	printNestedWithSep(p, n.Parameters(), d, ",")
	p.print(")")
}

func (p *Printer) VisitUnaryExpression(n *ast.UnaryExpressionNode, d Data) {
	p.moveBefore(n)

	switch n.Op() {
	case ast.NotSetUnaryOp:
		p.fmt.addUnary(p.keyword("<UNKNOWN OPERATOR>"))
	case ast.NotUnaryOp:
		p.fmt.addUnary(p.keyword("NOT"))
	case ast.BitwiseNotUnaryOp:
		p.fmt.addUnary("~")
	case ast.MinusUnaryOp:
		p.fmt.addUnary("-")
	case ast.PlusUnaryOp:
		p.fmt.addUnary("+")
	}

	p.accept(n.Operand(), d)

	switch n.Op() {
	case ast.IsUnknownUnaryOp:
		p.fmt.addUnary(p.keyword("IS UNKNOWN"))
	case ast.IsNotUnknownUnaryOp:
		p.fmt.addUnary(p.keyword("IS NOT UNKNOWN"))
	}

	p.movePast(n)
}

func (p *Printer) VisitUnnestExpression(n *ast.UnnestExpressionNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("UNNEST") + "(")
	p.accept(n.Expression(), d)
	p.print(")")
}

func (p *Printer) VisitUnpivotClause(n *ast.UnpivotClauseNode, d Data) {
	p.moveBefore(n)
	p.println("")

	switch int(n.NullFilter()) {
	case 1: // ast.UnpivotUnspecified:
		p.println(p.keyword("UNPIVOT") + " (")
	case 2: // ast.UnpivotInclude:
		p.println(p.keyword("UNPIVOT INCLUDE NULLS") + " (")
	case 3: // ast.UnpivotExclude:
		p.println(p.keyword("UNPIVOT EXCLUDE NULLS") + " (")
	}

	inItems := n.UnpivotInItems()
	simple := allTrue(mapIsSimpleUnpivotInItemList(inItems))
	d.SetBool("unpivot_in_time_simple", simple)

	p.incDepth()
	p.accept(n.UnpivotOutputValueColumns(), d)
	p.println("")
	p.print(p.keyword("FOR"))
	p.accept(n.UnpivotOutputNameColumn(), d)
	p.print(p.keyword("IN") + " (")

	if !simple {
		p.println("")
		p.incDepth()
	}

	p.accept(n.UnpivotInItems(), d)

	if !simple {
		p.println("")
		p.decDepth()
	}

	p.println(")")
	p.decDepth()
	p.print(")")
	p.accept(n.OutputAlias(), d)
	p.movePast(n)
}

func (p *Printer) VisitUnpivotInItem(n *ast.UnpivotInItemNode, d Data) {
	p.moveBefore(n)
	p.accept(n.UnpivotColumns(), d)
	p.accept(n.Alias(), d)
	p.movePast(n)
}

func (p *Printer) VisitUnpivotInItemLabel(n *ast.UnpivotInItemLabelNode, d Data) {
	p.moveBefore(n)

	if label := n.Label(); label != nil {
		p.print(p.keyword("AS"))
		p.accept(n.Label(), d)
	}

	p.movePast(n)
}

func (p *Printer) VisitUnpivotInItemList(n *ast.UnpivotInItemListNode, d Data) {
	p.moveBefore(n)

	simple := d.IsEnabled("unpivot_in_time_simple")

	for i, item := range n.InItems() {
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

func (p *Printer) VisitUsingClause(n *ast.UsingClauseNode, d Data) {
	p.moveBefore(n)
	p.printClause(p.keyword("USING") + " (")
	printNestedWithSep(p, n.Keys(), d, ",")
	p.print(")")
}

func (p *Printer) VisitWhereClause(n *ast.WhereClauseNode, d Data) {
	p.moveBefore(n)

	e := n.Expression()

	switch e.Kind() {
	case ast.AndExpr, ast.OrExpr:
		d["align_binary_op_budget"] = 1
		p.accept(e, d)
	default:
		p.acceptNested(e, d)
	}
}

func (p *Printer) VisitWindowClause(n *ast.WindowClauseNode, d Data) {
	p.moveBefore(n)

	for i, w := range n.Windows() {
		if i > 0 {
			p.println(",")
		}

		p.accept(w.Name(), d)
		p.print(p.keyword("AS") + " ")

		ws := w.WindowSpec()
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

func (p *Printer) VisitWindowFrame(n *ast.WindowFrameNode, d Data) {
	p.moveBefore(n)

	// Unfortunately FrameUnit enum is incorrect the wrapper.
	switch int(n.FrameUnit()) {
	case 1:
		p.print(p.keyword("ROWS"))
	case 2:
		p.print(p.keyword("RANGE"))
	default:
		panic(fmt.Sprintf("Unknown frame unit id %d", n.FrameUnit()))
	}

	pp := p.nest()

	if n.EndExpr() != nil {
		pp.print(p.keyword("BETWEEN"))
		pp.accept(n.StartExpr(), d)
		pp.print(p.keyword("AND"))
		pp.accept(n.EndExpr(), d)
	} else {
		pp.accept(n.StartExpr(), d)
	}

	p.print(pp.unnest())
}

func (p *Printer) VisitWindowFrameExpr(n *ast.WindowFrameExprNode, d Data) {
	p.moveBefore(n)

	// Unfortunately FrameUnit enum is incorrect the wrapper.
	switch int(n.BoundaryType()) {
	case 1:
		p.print(p.keyword("UNBOUNDED PRECEDING"))
	case 2:
		p.accept(n.Expression(), d)
		p.print(p.keyword("PRECEDING"))
	case 3:
		p.print(p.keyword("CURRENT ROW"))
	case 4:
		p.accept(n.Expression(), d)
		p.print(p.keyword("FOLLOWING"))
	case 5:
		p.print(p.keyword("UNBOUNDED FOLLOWING"))
	}
}

func (p *Printer) VisitWindowSpecification(n *ast.WindowSpecificationNode, d Data) {
	forceAcrossLines := true

	p.moveBefore(n)
	pp := p.nest()

	wn := n.BaseWindowName()
	if wn != nil {
		pp.accept(wn, d)

		if forceAcrossLines {
			pp.println("")
		}
	}

	pp2 := pp.nest()

	pb := n.PartitionBy()
	if pb != nil {
		pp2.accept(pb, d)
	}

	ob := n.OrderBy()
	if ob != nil {
		if forceAcrossLines && pb != nil {
			pp2.println("")
		}

		pp2.accept(ob, d)
	}

	if wf := n.WindowFrame(); wf != nil {
		if forceAcrossLines && (pb != nil || ob != nil) {
			pp2.println("")
		}

		pp2.accept(wf, d)
	}

	pp.print(pp2.unnest())
	pp.movePast(n)
	p.print(pp.unnest())
}

func (p *Printer) VisitWithClause(n *ast.WithClauseNode, d Data) {
	p.moveBefore(n)
	p.println("")

	if n.Recursive() {
		p.println(p.keyword("WITH RECURSIVE"))
	} else {
		p.println(p.keyword("WITH"))
	}

	if p.fmt.opts.IndentWithClause {
		p.incDepth()
	}

	for i, e := range n.With() {
		if i > 0 {
			p.println(",")
		}

		p.accept(e, d)
	}

	// WITH clause will be followed by a new query, so we need to
	// create a new line.
	p.println("")

	if p.fmt.opts.IndentWithClause {
		p.decDepth()
	}

	p.movePast(n)
}

func withInsideWith(n *ast.QueryNode) bool {
	if !nodeDefined(n.WithClause()) {
		return false
	}

	parent := n.Parent()
	if parent == nil {
		return false
	}

	_, ok := parent.(*ast.WithClauseEntryNode)
	return ok
}

func (p *Printer) VisitWithClauseEntry(n *ast.WithClauseEntryNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Alias(), d)
	p.println(p.keyword("AS") + " (")

	if p.fmt.opts.IndentWithEntries {
		p.incDepth()
	}

	p.accept(n.Query(), d)
	p.println("")

	if p.fmt.opts.IndentWithEntries {
		p.decDepth()
	}

	p.print(")")
	p.movePast(n)
}

func (p *Printer) VisitWithOffset(n *ast.WithOffsetNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("WITH OFFSET"))
	p.accept(n.Alias(), d)
}

func (p *Printer) VisitWithWeight(n *ast.WithWeightNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("WITH WEIGHT"))
	p.accept(n.Alias(), d)
}
