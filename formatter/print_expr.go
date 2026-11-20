package formatter

import (
	"slices"
	"strings"
	"unicode"

	"github.com/goccy/go-googlesql"

	"github.com/paulourio/bqlang/extensions"
)

func (p *printer) visitAnalyticFunctionCall(n *googlesql.ASTAnalyticFunctionCall, d printerData) {
	pp := p.nest()
	pp.printOpenParenIfNeeded(n)

	fn, _ := n.Function()
	ws, _ := n.WindowSpec()

	pp.accept(fn, d)
	pp.print(p.keyword("OVER") + " ")

	elems := countWindowSpecElems(ws)
	requireParenthesis := true

	if elems == 1 {
		if wn, _ := ws.BaseWindowName(); nodeDefined(wn) {
			requireParenthesis = false
		}
	}

	if requireParenthesis {
		pp.print("(")
	}

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

func (p *printer) visitAndExpr(n *googlesql.ASTAndExpr, d printerData) {
	conjuncts := selectChildrenOfType[googlesql.ASTExpressionNode](n)
	inClause := isInsideOfWhereClause(n) || isInsideOfOnClause(n)
	alignWithClause := p.fmt.opts.AlignLogicalWithClauses && inClause
	inMerge := isInsideOfMergeStatement(n)
	simple := p.isSimpleAndExpr(n)

	budget := d.alignBinaryOpBudget
	alignAnd := budget > 0

	if alignAnd || inMerge || (!simple && p.areAlignable(conjuncts)) {
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
	andLines := pp.formatAndConjuncts(p1, conjuncts, simple, alignWithClause, budget, d)
	s := p1.unnestLeft()

	if alignWithClause {
		pp.print(prefixAlignedAndLines(s, andLines, pp.keyword("AND")+" "))
	} else {
		pp.print(s)
	}

	pp.closeAndParen(n, simple)

	if alignWithClause {
		p.print(pp.String())
	} else {
		p.print(pp.unnest())
	}
}

func (p *printer) formatAndConjuncts(
	p1 *printer,
	conjuncts []googlesql.ASTExpressionNode,
	simple, alignWithClause bool,
	budget int,
	ctx printerData,
) []int {
	andLines := make([]int, 0, len(conjuncts))

	for i, conjunct := range conjuncts {
		if i > 0 {
			if !simple {
				p1.println("")
			}

			switch {
			case alignWithClause:
				nlines := strings.Count(p1.String(), "\n") + 1
				andLines = append(andLines, nlines)
			case !simple:
				p1.print(p.keyword("AND") + " \v")
			default:
				p1.print(p.keyword("AND"))
			}
		} else if !simple && !alignWithClause {
			p1.print("\v")
		}

		ctx.alignBinaryOpBudget = budget

		if !simple {
			p1.accept(conjunct, ctx)
		} else {
			p1.acceptNested(conjunct, ctx)
		}

		p1.movePastLine(conjunct)
	}

	return andLines
}

func (p *printer) closeAndParen(n *googlesql.ASTAndExpr, simple bool) {
	if !p.isParenNeeded(n) {
		return
	}

	if !simple {
		p.println("")
		p.decDepth()
	}

	p.print(")")
}

func prefixAlignedAndLines(s string, andLines []int, andTok string) string {
	lines := strings.Split(s, "\n")

	for _, idx := range andLines {
		if idx < len(lines) {
			lines[idx] = andTok + lines[idx]
		}
	}

	return strings.Join(lines, "\n")
}

func (p *printer) visitOrExpr(n *googlesql.ASTOrExpr, d printerData) {
	disjuncts := selectChildrenOfType[googlesql.ASTExpressionNode](n)
	paren := p.isParenNeeded(n)
	inClause := !paren && (isInsideOfWhereClause(n) || isInsideOfOnClause(n))
	simple := len(disjuncts) < 4 && p.areSimpleExprs(disjuncts)

	p.moveBefore(n)

	if paren {
		p.openLogicalParen(simple)
	}

	p1 := p.printOrDisjuncts(disjuncts, simple, inClause, d)

	if paren && simple {
		p.print(leftAlignNested(p1.String()))
	} else {
		p.print(p1.unnestLeft())
	}

	if paren {
		p.closeLogicalParen(simple)
	}
}

func (p *printer) openLogicalParen(simple bool) {
	if !simple {
		p.println("(")
		p.incDepth()

		return
	}

	p.print("(")
}

func (p *printer) closeLogicalParen(simple bool) {
	if !simple {
		p.println("")
		p.decDepth()
	}

	p.print(")")
}

func (p *printer) printOrDisjuncts(
	disjuncts []googlesql.ASTExpressionNode,
	simple, inClause bool,
	d printerData,
) *printer {
	p1 := p.nest()

	for i, disjunct := range disjuncts {
		if i > 0 {
			switch {
			case simple && !inClause:
				p1.print(p1.keyword("OR"))
			case p1.fmt.opts.AlignLogicalWithClauses && inClause:
				p.print(p1.unnest())
				p.printClause(p.keyword("OR"))
				p1 = p.nest()
			default:
				p1.printClause(p1.keyword("OR"))
			}
		}

		if nodeKind(disjunct) == googlesql.ASTNodeKindAstAndExpr {
			p1.accept(disjunct, d)
		} else {
			p1.acceptNested(disjunct, d)
		}

		p1.movePastLine(disjunct)
	}

	return p1
}

func (p *printer) visitArrayConstructor(n *googlesql.ASTArrayConstructor, d printerData) {
	p.moveBefore(n)
	pp := p.nest()

	if t, _ := n.Type(); nodeDefined(t) {
		pp.print(p.toString(t, d))
	} else {
		s := pp.nodeInput(n)
		if strings.HasPrefix(strings.ToUpper(s), "ARRAY") {
			pp.print(pp.keyword("ARRAY"))
		}
	}

	elems := selectChildrenOfType[googlesql.ASTExpressionNode](n)
	simple := p.areSimpleExprs(elems)

	if simple {
		pp.print("[")
		printNestedWithSep(pp, elems, d, ",")
		pp.print("]")
	} else {
		pp1 := pp.nest()
		pp1.println("[")
		pp1.incDepth()

		pp12 := pp1.nest()

		for i, elem := range elems {
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

func (p *printer) visitArrayElement(n *googlesql.ASTArrayElement, d printerData) {
	p.moveBefore(n)
	p.printOpenParenIfNeeded(n)
	arr, _ := n.Array()
	pos, _ := n.Position()

	p.accept(arr, d)
	p.print("[")
	p.accept(pos, d)
	p.print("]")
	p.printCloseParenIfNeeded(n)
}

func (p *printer) visitBetweenExpression(n *googlesql.ASTBetweenExpression, d printerData) {
	p.printOpenParenIfNeeded(n)

	lhs, _ := n.Lhs()
	low, _ := n.Low()
	high, _ := n.High()
	isNot, _ := n.IsNot()

	p.accept(lhs, d)
	p.moveBefore(n)

	if isNot {
		p.print(p.keyword("NOT BETWEEN") + " ")
	} else {
		p.print(p.keyword("BETWEEN") + " ")
	}

	p.accept(low, d)
	p.print(p.keyword("AND"))
	p.accept(high, d)
	p.printCloseParenIfNeeded(n)
}

func (p *printer) visitBinaryExpression(n *googlesql.ASTBinaryExpression, d printerData) {
	p.printOpenParenIfNeeded(n)

	var lhsAlign, rhsAlign string

	if d.alignBinaryOpBudget > 0 {
		d.alignBinaryOpBudget--
		lhsAlign = "\v"
		rhsAlign = " \v"
	}

	lhs, _ := n.Lhs()
	rhs, _ := n.Rhs()

	p.accept(lhs, d)
	p.movePast(lhs)
	p.flushCommentsBeforeOperator(lhs, rhs)
	p.printBinaryOp(n, lhsAlign, rhsAlign)

	p.moveBefore(rhs)
	p.accept(rhs, d)
	p.movePast(rhs)
	p.movePast(n)
	p.printCloseParenIfNeeded(n)
}

func (p *printer) printBinaryOp(n *googlesql.ASTBinaryExpression, lhsAlign, rhsAlign string) {
	op, _ := n.Op()
	isNot, _ := n.IsNot()

	if sym := binaryOpSymbol(op); sym != "" {
		p.print(lhsAlign + sym + rhsAlign)

		return
	}

	switch op {
	case googlesql.ASTBinaryExpressionEnums_OpNotSet:
		p.print("<UNKNOWN OPERATOR>")
	case googlesql.ASTBinaryExpressionEnums_OpLike:
		if isNot {
			p.print(lhsAlign + p.keyword("NOT LIKE") + rhsAlign)
		} else {
			p.print(lhsAlign + p.keyword("LIKE") + rhsAlign)
		}
	case googlesql.ASTBinaryExpressionEnums_OpIs:
		if isNot {
			p.print(lhsAlign + p.keyword("IS NOT") + rhsAlign)
		} else {
			p.print(lhsAlign + p.keyword("IS") + rhsAlign)
		}
	case googlesql.ASTBinaryExpressionEnums_OpDistinct:
		p.printNotKeywordOp(isNot, "IS DISTINCT FROM", "IS NOT DISTINCT FROM", lhsAlign, rhsAlign)
	case googlesql.ASTBinaryExpressionEnums_OpIsSourceNode:
		p.printNotKeywordOp(isNot, "IS SOURCE OF", "IS NOT SOURCE OF", lhsAlign, rhsAlign)
	case googlesql.ASTBinaryExpressionEnums_OpIsDestNode:
		p.printNotKeywordOp(isNot, "IS DESTINATION OF", "IS NOT DESTINATION OF", lhsAlign, rhsAlign)
	default:
		sqlOp, _ := n.GetSQLForOperator()
		p.print(lhsAlign + p.keyword(sqlOp) + rhsAlign)
	}
}

func binaryOpSymbol(op googlesql.ASTBinaryExpressionEnums_Op) string {
	switch op {
	case googlesql.ASTBinaryExpressionEnums_OpEq:
		return "="
	case googlesql.ASTBinaryExpressionEnums_OpNe:
		return "!="
	case googlesql.ASTBinaryExpressionEnums_OpNe2:
		return "<>"
	case googlesql.ASTBinaryExpressionEnums_OpGt:
		return ">"
	case googlesql.ASTBinaryExpressionEnums_OpLt:
		return "<"
	case googlesql.ASTBinaryExpressionEnums_OpGe:
		return ">="
	case googlesql.ASTBinaryExpressionEnums_OpLe:
		return "<="
	case googlesql.ASTBinaryExpressionEnums_OpBitwiseOr:
		return "|"
	case googlesql.ASTBinaryExpressionEnums_OpBitwiseXor:
		return "^"
	case googlesql.ASTBinaryExpressionEnums_OpBitwiseAnd:
		return "&"
	case googlesql.ASTBinaryExpressionEnums_OpPlus:
		return "+"
	case googlesql.ASTBinaryExpressionEnums_OpMinus:
		return "-"
	case googlesql.ASTBinaryExpressionEnums_OpMultiply:
		return "*"
	case googlesql.ASTBinaryExpressionEnums_OpDivide:
		return "/"
	case googlesql.ASTBinaryExpressionEnums_OpConcatOp:
		return "||"
	default:
		return ""
	}
}

func (p *printer) printNotKeywordOp(isNot bool, posKw, negKw, lhsAlign, rhsAlign string) {
	kw := posKw
	if isNot {
		kw = negKw
	}

	if rhsAlign == "" {
		p.print(lhsAlign + p.keyword(kw) + " ")
	} else {
		p.print(lhsAlign + p.keyword(kw) + rhsAlign)
	}
}

func (p *printer) flushCommentsBeforeOperator(prev, next googlesql.ASTNode) {
	_, b := nodeByteRange(prev)
	e, _ := nodeByteRange(next)

	if opPos := strings.IndexFunc(p.viewErasedInput(b, e), isNonSpace); opPos >= 0 {
		p.fmt.flushCommentsUpTo(b + opPos)
	}
}

func isNonSpace(r rune) bool {
	return !unicode.IsSpace(r)
}

func (p *printer) visitConcatExpr(n *googlesql.ASTConcatExpr, d printerData) {
	p.printOpenParenIfNeeded(n)

	var lhsAlign, rhsAlign string

	if d.alignBinaryOpBudget > 0 {
		d.alignBinaryOpBudget--
		lhsAlign = "\v"
		rhsAlign = " \v"
	}

	operands := selectChildrenOfType[googlesql.ASTExpressionNode](n)

	for i, operand := range operands {
		if i > 0 {
			p.flushCommentsBeforeOperator(operands[i-1], operand)

			if i == 1 {
				p.print(lhsAlign + "||" + rhsAlign)
			} else {
				p.print("||")
			}
		}

		p.moveBefore(operand)
		p.accept(operand, d)
		p.movePast(operand)
	}

	p.movePast(n)
	p.printCloseParenIfNeeded(n)
}

func (p *printer) visitBitwiseShiftExpression(n *googlesql.ASTBitwiseShiftExpression, d printerData) {
	p.moveBefore(n)
	p.printOpenParenIfNeeded(n)
	lhs, _ := n.Lhs()
	rhs, _ := n.Rhs()
	isLeft, _ := n.IsLeftShift()

	p.accept(lhs, d)

	if isLeft {
		p.print("<<")
	} else {
		p.print(">>")
	}

	p.accept(rhs, d)
	p.printCloseParenIfNeeded(n)
}

func (p *printer) visitCaseNoValueExpression(n *googlesql.ASTCaseNoValueExpression, d printerData) {
	p.moveBefore(n)
	p.printOpenParenIfNeededWithDepth(n)
	p.println(p.keyword("CASE"))

	if p.fmt.opts.IndentCaseWhen {
		p.incDepth()
	}

	args := selectChildrenOfType[googlesql.ASTExpressionNode](n)
	simple := p.areSimpleExprs(args)
	d.simpleCase = simple

	pp := p.nest()
	p.visitCaseArgs(pp, args, d)
	p.print(pp.unnest())

	p.println("")

	if p.fmt.opts.IndentCaseWhen {
		p.decDepth()
	}

	p.print(p.keyword("END"))
	p.printCloseParenIfNeededWithDepth(n)
}

func (p *printer) visitCaseValueExpression(n *googlesql.ASTCaseValueExpression, d printerData) {
	p.moveBefore(n)
	p.printOpenParenIfNeededWithDepth(n)
	p.print(p.keyword("CASE"))

	args := selectChildrenOfType[googlesql.ASTExpressionNode](n)
	value := args[0]
	valueSimple := p.isSimpleExpr(value)
	simple := valueSimple && p.areSimpleExprs(args[1:])
	indentWhen := p.fmt.opts.IndentCaseWhen

	d.simpleCase = simple

	if !simple {
		p.println("")

		if indentWhen {
			p.incDepth()
		}
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
		if indentWhen {
			p.incDepth()
		}
	} else {
		p.printBlankLine()
	}

	pp := p.nest()
	p.visitCaseArgs(pp, args[1:], d)
	p.print(pp.unnest())

	p.println("")

	if indentWhen {
		p.decDepth()
	}

	p.print(p.keyword("END"))
	p.printCloseParenIfNeededWithDepth(n)
}

func (p *printer) visitCaseArgs(target *printer, args []googlesql.ASTExpressionNode, d printerData) {
	pp := target.nest()
	simple := d.simpleCase
	initAlignBinOpBudget := 0

	if simple && p.onlyBinaryExprOnCaseLHS(args) {
		initAlignBinOpBudget = 1
	}

	for len(args) >= 2 {
		lhs, rhs := args[0], args[1]
		args = args[2:]
		p.visitCaseWhenPair(pp, target, lhs, rhs, simple, initAlignBinOpBudget, len(args) >= 2, d)
	}

	if !simple {
		pp.printBlankLine()
	}

	if len(args) == 1 {
		visitCaseElseArg(pp, args[0], simple, initAlignBinOpBudget, d)
	}

	target.print(pp.String())
}

func (p *printer) visitCaseWhenPair(
	pp *printer,
	target *printer,
	lhs googlesql.ASTExpressionNode,
	rhs googlesql.ASTExpressionNode,
	simple bool,
	alignBudget int,
	hasMorePairs bool,
	d printerData,
) {
	lhsData := d
	if simple {
		lhsData.alignBinaryOpBudget = alignBudget
	}

	pp.print(pp.keyword("WHEN"))

	if simple {
		pp.print("\v")
	}

	if p.isSimpleExpr(lhs) {
		lhsFmt := strings.TrimLeft(target.toString(lhs, lhsData), "\v")
		pp.print(lhsFmt)

		remaining := alignBudget*3 - strings.Count(lhsFmt, "\v")
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

	if hasMorePairs {
		pp.println("")
	}

	if !simple {
		pp.decDepth()
		pp.printBlankLine()
	}
}

func visitCaseElseArg(
	pp *printer,
	elseExpr googlesql.ASTExpressionNode,
	simple bool,
	alignBudget int,
	d printerData,
) {
	pp.println("")

	if simple {
		pp.print(" ")
		pp.print(strings.Repeat("\v", 1+alignBudget*2))
	}

	pp.print(pp.keyword("ELSE"))

	if simple {
		pp.print("\v")
	} else {
		pp.println("")
		pp.incDepth()
	}

	p2 := pp.nest()
	p2.accept(elseExpr, d)
	pp.print(p2.unnestWithDepth(4))

	if !simple {
		pp.println("")
		pp.decDepth()
		pp.printBlankLine()
	}
}

func (p *printer) onlyBinaryExprOnCaseLHS(args []googlesql.ASTExpressionNode) bool {
	for i := 0; i < len(args)-1; i += 2 {
		if !p.isSimpleExpr(args[i]) || nodeKind(args[i]) != googlesql.ASTNodeKindAstBinaryExpression {
			return false
		}
	}

	return true
}

func (p *printer) visitCastExpression(n *googlesql.ASTCastExpression, d printerData) {
	pp := p.nest()
	pp.moveBefore(n)

	isSafe, _ := n.IsSafeCast()
	if isSafe {
		pp.print(p.keyword("SAFE_CAST") + "(")
	} else {
		pp.print(p.keyword("CAST") + "(")
	}

	expr, _ := n.Expr()
	typ, _ := n.Type()
	fc, _ := n.Format()

	pp.accept(expr, d)
	pp.print(p.keyword("AS"))
	pp.accept(typ, d)
	pp.accept(fc, d)
	pp.print(")")
	p.print(pp.unnest())
}

func (p *printer) visitClampedBetweenModifier(n *googlesql.ASTClampedBetweenModifier, d printerData) {
	p.moveBefore(n)
	low, _ := n.Low()
	high, _ := n.High()

	p.print(p.keyword("CLAMPED BETWEEN"))
	p.accept(low, d)
	p.print(p.keyword("AND"))
	p.accept(high, d)
}

func (p *printer) visitDotIdentifier(n *googlesql.ASTDotIdentifier, d printerData) {
	p.moveBefore(n)
	p.printOpenParenIfNeeded(n)
	expr, _ := n.Expr()
	name, _ := n.Name()

	p.printDotBase(expr, d)
	p.print(".")
	p.accept(name, d)
	p.printCloseParenIfNeeded(n)
}

func (p *printer) visitDotGeneralizedField(n *googlesql.ASTDotGeneralizedField, d printerData) {
	p.moveBefore(n)
	p.printOpenParenIfNeeded(n)
	expr, _ := n.Expr()
	path, _ := n.Path()

	p.printDotBase(expr, d)
	p.print(".(")
	p.accept(path, d)
	p.print(")")
	p.printCloseParenIfNeeded(n)
}

func (p *printer) visitDotStar(n *googlesql.ASTDotStar, d printerData) {
	p.moveBefore(n)
	expr, _ := n.Expr()
	p.printDotBase(expr, d)
	p.print(".*")
}

func (p *printer) visitDotStarWithModifiers(n *googlesql.ASTDotStarWithModifiers, d printerData) {
	p.moveBefore(n)
	expr, _ := n.Expr()
	mods, _ := n.Modifiers()

	p.printDotBase(expr, d)
	p.print(".*")
	p.accept(mods, d)
}

func (p *printer) printDotBase(expr googlesql.ASTExpressionNode, d printerData) {
	needParen := needsInnerParenBeforeDot(expr)
	if needParen {
		p.print("(")
	}

	p.accept(expr, d)

	if needParen {
		p.print(")")
	}
}

func needsInnerParenBeforeDot(n googlesql.ASTExpressionNode) bool {
	if !nodeDefined(n) {
		return false
	}

	switch nodeKind(n) {
	case googlesql.ASTNodeKindAstIntLiteral,
		googlesql.ASTNodeKindAstFloatLiteral:
		return true
	case googlesql.ASTNodeKindAstSystemVariableExpr:
		paren, _ := n.Parenthesized()

		return !paren
	default:
		return false
	}
}

func (p *printer) visitExpressionSubquery(n *googlesql.ASTExpressionSubquery, d printerData) {
	p.moveBefore(n)

	mod, _ := n.Modifier()
	switch mod {
	case googlesql.ASTExpressionSubqueryEnums_ModifierNone:
		// Nothing.
	case googlesql.ASTExpressionSubqueryEnums_ModifierArray:
		p.print(p.keyword("ARRAY"))
	case googlesql.ASTExpressionSubqueryEnums_ModifierExists:
		p.print(p.keyword("EXISTS"))
	case googlesql.ASTExpressionSubqueryEnums_ModifierValue:
		p.print(p.keyword("VALUE"))
	}

	hint, _ := n.Hint()
	q, _ := n.Query()
	p.printDelimitedQuery(q, hint, d)
}

func (p *printer) printDelimitedQuery(q *googlesql.ASTQuery, hint *googlesql.ASTHint, d printerData) {
	openTok, closeTok := subqueryDelimiters(q)
	p.println(openTok)
	p.incDepth()
	p.accept(hint, d)
	p.accept(q, d)
	p.decDepth()
	p.println("")
	p.print(closeTok)
}

func subqueryDelimiters(q *googlesql.ASTQuery) (string, string) {
	if nodeDefined(q) {
		qe, _ := q.QueryExpr()
		switch qe.(type) {
		case *googlesql.ASTGqlQuery,
			*googlesql.ASTGqlGraphPatternQuery,
			*googlesql.ASTGqlLinearOpsQuery:
			return "{", "}"
		}
	}

	return "(", ")"
}

func (p *printer) visitExtractExpression(n *googlesql.ASTExtractExpression, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("EXTRACT") + "(")

	simple := p.isSimpleExpr(n)
	if !simple {
		p.println("")
		p.incDepth()
	}

	lhs, _ := n.LhsExpr()
	rhs, _ := n.RhsExpr()
	tz, _ := n.TimeZoneExpr()

	p.print(p.typename(p.toString(lhs, d)))

	if !simple {
		p.println("")
		p.decDepth()
	}

	p.print(p.keyword("FROM"))

	if !simple {
		p.println("")
		p.incDepth()
	}

	p.accept(rhs, d)

	if nodeDefined(tz) {
		p.print(p.keyword("AT TIME ZONE"))
		p.accept(tz, d)
	}

	if !simple {
		p.println("")
		p.decDepth()
	}

	p.print(")")
}

func (p *printer) visitFunctionCall(n *googlesql.ASTFunctionCall, d printerData) {
	p.moveBefore(n)

	fn, _ := n.Function()
	pp := p.nest()
	pp.acceptNestedString(fn, d)

	expr := strings.TrimPrefix(pp.unnest(), "\v")
	signature := lookupFunctionSignature(expr)

	pp = p.nest()
	pp.printOpenParenIfNeeded(n)

	allArgs := functionCallArgs(n)
	chained, _ := n.IsChainedCall()
	sigOffset := 0
	callArgs := allArgs

	if chained && len(allArgs) > 0 {
		pp.printChainedBase(allArgs[0], d)
		pp.print(".")

		callArgs = allArgs[1:]
		sigOffset = 1
	}

	elems := countFunctionCallElements(n)
	simple := len(callArgs) <= 4 &&
		elems <= 1 &&
		p.areSimpleExprs(callArgs) &&
		!p.hasComplexFunctionModifier(n)

	pp.printFunctionName(fn, expr, chained)
	pp.print("(")

	if !simple {
		pp.println("")
		pp.incDepth()
	}

	if distinct, _ := n.Distinct(); distinct {
		pp.print(pp.keyword("DISTINCT"))

		if !simple {
			pp.println("")
		}
	}

	pp2 := pp.nest()
	pp2.printFunctionCallArgs(callArgs, signature, sigOffset, simple, d)
	p.printFunctionCallModifiers(pp2, n, simple, len(callArgs) > 0, d)
	pp.print(pp2.unnest())

	if !simple {
		pp.println("")
		pp.decDepth()
	}

	pp.print(")")

	if hint, _ := n.Hint(); nodeDefined(hint) {
		pp.accept(hint, d)
	}

	pp.printCloseParenIfNeeded(n)
	p.print(pp.unnest())
}

func (p *printer) printFunctionName(fn *googlesql.ASTPathExpression, expr string, chained bool) {
	fnFormatted := p.function(expr)
	if chained && len(selectChildrenOfType[*googlesql.ASTIdentifier](fn)) > 1 {
		p.print("(" + fnFormatted + ")")

		return
	}

	p.print(fnFormatted)
}

func (p *printer) printFunctionCallArgs(
	callArgs []googlesql.ASTExpressionNode,
	signature *functionSignature,
	sigOffset int,
	simple bool,
	d printerData,
) {
	for i, arg := range callArgs {
		if i > 0 {
			p.print(",")

			if !simple {
				p.println("")
			}
		}

		printedArg := p.toString(arg, d)
		if strings.Contains(printedArg, "\n") {
			p.println("")
		}

		if _, ok := arg.(*googlesql.ASTPathExpression); ok {
			sigStyle := signature.PrintCaseAt(i + sigOffset)
			p.print(p.identifierWithCase(printedArg, sigStyle))
		} else {
			p.print(printedArg)
		}
	}
}

func (p *printer) printFunctionCallModifiers(
	pp2 *printer,
	n *googlesql.ASTFunctionCall,
	simple, hasArgs bool,
	d printerData,
) {
	needNewline := hasArgs

	if kw := nullHandlingKeyword(n); kw != "" {
		emitFunctionModifier(pp2, nil, simple, &needNewline, func(m *printer) {
			m.print(m.keyword(kw))
		})
	}

	we, _ := n.WhereExpr()
	hm, _ := n.HavingModifier()
	gb, _ := n.GroupBy()
	he, _ := n.HavingExpr()
	cb, _ := n.ClampedBetweenModifier()
	wr, _ := n.WithReportModifier()
	ob, _ := n.OrderBy()
	lo, _ := n.LimitOffset()

	p.emitOptionalNodeModifier(pp2, we, simple, &needNewline, d)
	p.emitOptionalNodeModifier(pp2, hm, simple, &needNewline, d)

	if nodeDefined(gb) {
		emitFunctionModifier(pp2, gb, simple, &needNewline, func(m *printer) {
			m.print(m.keyword("GROUP"))
			m.accept(gb, d)
		})
	}

	if nodeDefined(he) {
		emitFunctionModifier(pp2, he, simple, &needNewline, func(m *printer) {
			m.print(m.keyword("HAVING"))
			expr, _ := he.Expression()
			m.acceptNested(expr, d)
		})
	}

	p.emitOptionalNodeModifier(pp2, cb, simple, &needNewline, d)
	p.emitOptionalNodeModifier(pp2, wr, simple, &needNewline, d)
	p.emitOptionalNodeModifier(pp2, ob, simple, &needNewline, d)
	p.emitOptionalNodeModifier(pp2, lo, simple, &needNewline, d)
}

func nullHandlingKeyword(n *googlesql.ASTFunctionCall) string {
	nh, _ := n.NullHandlingModifier()
	switch nh {
	case googlesql.ASTFunctionCallEnums_NullHandlingModifierDefaultNullHandling:
		return ""
	case googlesql.ASTFunctionCallEnums_NullHandlingModifierIgnoreNulls:
		return "IGNORE NULLS"
	case googlesql.ASTFunctionCallEnums_NullHandlingModifierRespectNulls:
		return "RESPECT NULLS"
	}

	return ""
}

func (p *printer) emitOptionalNodeModifier(
	pp2 *printer,
	mod googlesql.ASTNode,
	simple bool,
	needNewline *bool,
	d printerData,
) {
	if !nodeDefined(mod) {
		return
	}

	emitFunctionModifier(pp2, mod, simple, needNewline, func(m *printer) {
		m.accept(mod, d)
	})
}

func emitFunctionModifier(
	pp2 *printer,
	mod googlesql.ASTNode,
	simple bool,
	needNewline *bool,
	fn func(*printer),
) {
	pp2.moveBefore(mod)

	if !simple {
		if *needNewline {
			pp2.println("")
		}

		*needNewline = true
		m := pp2.nest()
		fn(m)
		pp2.print(alignNested(m.String()))

		return
	}

	fn(pp2)
}

func (p *printer) visitIdentifier(n *googlesql.ASTIdentifier, _ printerData) {
	p.moveBefore(n)

	raw := p.rawIdentifier(n)
	parent := nodeParent(n)

	if p.fmt.opts.TableNameStyle != Unspecified {
		if path, ok := parent.(*googlesql.ASTPathExpression); ok && isTablePath(path) {
			p.print(p.identifierWithCase(raw, p.fmt.opts.TableNameStyle))

			return
		}
	}

	p.print(p.identifier(raw))
}

func (p *printer) rawIdentifier(n *googlesql.ASTIdentifier) string {
	start, end := nodeByteRange(n)

	if 0 < start && start < end && end < len(p.input) &&
		p.input[start] != '`' && p.input[start-1] == '`' && p.input[end] == '`' {
		start--
		end++
	}

	return strings.ReplaceAll(p.viewInput(start, end), "\v", p.fmt.sentinels.verticalTab)
}

type tableNameHolder interface {
	TableName() (*googlesql.ASTPathExpression, error)
}

func isTablePath(path *googlesql.ASTPathExpression) bool {
	parent := nodeParent(path)

	switch stmt := parent.(type) {
	case *googlesql.ASTDropStatement:
		sok, _ := stmt.SchemaObjectKind()

		return isTableSchemaObjectKind(sok)
	case *googlesql.ASTUndropStatement:
		sok, _ := stmt.SchemaObjectKind()

		return isTableSchemaObjectKind(sok)
	case *googlesql.ASTCreateRowAccessPolicyStatement:
		tbl, _ := stmt.TargetPath()

		return sameNodeSpan(path, tbl)
	case *googlesql.ASTAlterRowAccessPolicyStatement:
		tbl, _ := stmt.Path()

		return sameNodeSpan(path, tbl)
	case *googlesql.ASTTableClause:
		tbl, _ := stmt.TablePath()

		return sameNodeSpan(path, tbl)
	case *googlesql.ASTGraphElementTable:
		tbl, _ := stmt.Name()

		return sameNodeSpan(path, tbl)
	case *googlesql.ASTRenameToClause:
		nn, _ := stmt.NewName()

		return sameNodeSpan(path, nn) && isTableRenameClause(stmt)
	case *googlesql.ASTAlterPrivilegeRestrictionStatement:
		tbl, _ := stmt.Path()
		ot, _ := stmt.ObjectType()

		return sameNodeSpan(path, tbl) && isTableObjectType(ot)
	case *googlesql.ASTDropPrivilegeRestrictionStatement:
		tbl, _ := stmt.NamePath()
		ot, _ := stmt.ObjectType()

		return sameNodeSpan(path, tbl) && isTableObjectType(ot)
	case *googlesql.ASTGrantStatement:
		tbl, _ := stmt.TargetPath()

		return sameNodeSpan(path, tbl) && isTablePrivilegeTarget(stmt)
	case *googlesql.ASTRevokeStatement:
		tbl, _ := stmt.TargetPath()

		return sameNodeSpan(path, tbl) && isTablePrivilegeTarget(stmt)
	case tableNameHolder:
		tbl, _ := stmt.TableName()

		return sameNodeSpan(path, tbl)
	}

	switch nodeKind(parent) {
	case googlesql.ASTNodeKindAstTablePathExpression,
		googlesql.ASTNodeKindAstCreateTableStatement,
		googlesql.ASTNodeKindAstCreateViewStatement,
		googlesql.ASTNodeKindAstCreateMaterializedViewStatement,
		googlesql.ASTNodeKindAstCreateExternalTableStatement,
		googlesql.ASTNodeKindAstCreateSnapshotTableStatement,
		googlesql.ASTNodeKindAstAlterTableStatement,
		googlesql.ASTNodeKindAstAlterViewStatement,
		googlesql.ASTNodeKindAstAlterMaterializedViewStatement,
		googlesql.ASTNodeKindAstAlterAllRowAccessPoliciesStatement,
		googlesql.ASTNodeKindAstDropMaterializedViewStatement,
		googlesql.ASTNodeKindAstDropSnapshotTableStatement,
		googlesql.ASTNodeKindAstAuxLoadDataStatement,
		googlesql.ASTNodeKindAstCloneDataSource,
		googlesql.ASTNodeKindAstCloneDataStatement,
		googlesql.ASTNodeKindAstCopyDataSource,
		googlesql.ASTNodeKindAstDefineTableStatement,
		googlesql.ASTNodeKindAstInsertStatement,
		googlesql.ASTNodeKindAstDeleteStatement,
		googlesql.ASTNodeKindAstUpdateStatement,
		googlesql.ASTNodeKindAstMergeStatement,
		googlesql.ASTNodeKindAstTruncateStatement:
		return true
	default:
		return false
	}
}

func isTableRenameClause(n *googlesql.ASTRenameToClause) bool {
	parent := nodeParent(n)
	if nodeKind(parent) == googlesql.ASTNodeKindAstAlterActionList {
		parent = nodeParent(parent)
	}

	switch nodeKind(parent) {
	case googlesql.ASTNodeKindAstAlterTableStatement,
		googlesql.ASTNodeKindAstAlterViewStatement,
		googlesql.ASTNodeKindAstAlterMaterializedViewStatement:
		return true
	default:
		return false
	}
}

func isTableObjectType(ot *googlesql.ASTIdentifier) bool {
	if !nodeDefined(ot) {
		return false
	}

	s, _ := ot.GetAsString()
	switch strings.ToUpper(s) {
	case "TABLE", "VIEW":
		return true
	default:
		return false
	}
}

func isTablePrivilegeTarget(stmt googlesql.ASTNode) bool {
	parts := selectChildrenOfType[*googlesql.ASTIdentifier](stmt)
	if len(parts) == 0 {
		return false
	}

	words := make([]string, 0, len(parts))
	for _, part := range parts {
		s, _ := part.GetAsString()
		words = append(words, strings.ToUpper(s))
	}

	switch strings.Join(words, " ") {
	case "TABLE", "EXTERNAL TABLE", "SNAPSHOT TABLE", "VIEW", "MATERIALIZED VIEW":
		return true
	default:
		return false
	}
}

func sameNodeSpan(a, b googlesql.ASTNode) bool {
	if !nodeDefined(a) || !nodeDefined(b) {
		return false
	}

	sa, ea := nodeByteRange(a)
	sb, eb := nodeByteRange(b)

	return sa == sb && ea == eb
}

func isTableSchemaObjectKind(sok googlesql.SchemaObjectKind) bool {
	switch sok {
	case googlesql.SchemaObjectKindKTable,
		googlesql.SchemaObjectKindKExternalTable,
		googlesql.SchemaObjectKindKSnapshotTable,
		googlesql.SchemaObjectKindKView,
		googlesql.SchemaObjectKindKMaterializedView:
		return true
	default:
		return false
	}
}

func (p *printer) visitIdentifierList(n *googlesql.ASTIdentifierList, d printerData) {
	p.moveBefore(n)
	ids := selectChildrenOfType[*googlesql.ASTIdentifier](n)
	printNestedWithSep(p, ids, d, ",")
}

func (p *printer) visitInExpression(n *googlesql.ASTInExpression, d printerData) {
	p.moveBefore(n)
	p.printOpenParenIfNeeded(n)

	lhs, _ := n.Lhs()
	isNot, _ := n.IsNot()
	hint, _ := n.Hint()
	inList, _ := n.InList()
	unnest, _ := n.UnnestExpr()
	q, _ := n.Query()

	p.accept(lhs, d)

	if isNot {
		p.print(p.keyword("NOT IN"))
	} else {
		p.print(p.keyword("IN"))
	}

	p.accept(hint, d)
	p.accept(inList, d)
	p.accept(unnest, d)

	if nodeDefined(q) {
		p.printDelimitedQuery(q, nil, d)
	}

	p.printCloseParenIfNeeded(n)
	p.movePast(n)
}

func (p *printer) visitInList(n *googlesql.ASTInList, d printerData) {
	p.moveBefore(n)
	elems := selectChildrenOfType[googlesql.ASTExpressionNode](n)
	printParenList(p, elems, p.areSimpleExprs(elems), d)
}

func (p *printer) visitIntervalExpr(n *googlesql.ASTIntervalExpr, d printerData) {
	p.moveBefore(n)
	val, _ := n.IntervalValue()
	dp, _ := n.DatePartName()
	dpTo, _ := n.DatePartNameTo()

	p.print(p.keyword("INTERVAL"))
	p.accept(val, d)
	p.print(p.rawKeywordIdent(dp))

	if nodeDefined(dpTo) {
		p.print(p.keyword("TO"))
		p.print(p.rawKeywordIdent(dpTo))
	}

	p.movePast(n)
}

func (p *printer) visitLambda(n *googlesql.ASTLambda, d printerData) {
	p.moveBefore(n)
	args, _ := n.ArgumentList()
	body, _ := n.Body()

	p.accept(args, d)
	p.print("->")
	p.accept(body, d)
	p.movePast(n)
}

func (p *printer) visitNamedArgument(n *googlesql.ASTNamedArgument, d printerData) {
	p.moveBefore(n)
	name, _ := n.Name()
	expr, _ := n.Expr()

	p.accept(name, d)
	p.print("=>")

	simple := p.isSimpleExpr(expr)
	if !simple {
		p.println("")
		p.incDepth()
	}

	p.accept(expr, d)

	if !simple {
		p.println("")
		p.decDepth()
	}
}

func (p *printer) visitParameterExpr(n *googlesql.ASTParameterExpr, d printerData) {
	p.moveBefore(n)
	pos, _ := n.Position()

	if pos == 0 {
		name, _ := n.Name()

		p.print("@")

		if p.fmt.opts.QueryParameterStyle != Unspecified && nodeDefined(name) {
			p.moveBefore(name)
			p.print(p.identifierWithCase(p.rawIdentifier(name), p.fmt.opts.QueryParameterStyle))
		} else {
			p.accept(name, d)
		}
	} else {
		p.print("?")
	}
}

func (p *printer) visitPathExpression(n *googlesql.ASTPathExpression, d printerData) {
	p.moveBefore(n)
	p.printOpenParenIfNeeded(n)

	names := selectChildrenOfType[*googlesql.ASTIdentifier](n)
	sharedSegs := p.sharedSpanPathSegments(names)
	tablePath := p.fmt.opts.TableNameStyle != Unspecified && isTablePath(n)

	for i, name := range names {
		if i > 0 {
			p.print(".")
		}

		if i < len(sharedSegs) {
			p.moveBefore(name)
			p.printPathIdentifier(sharedSegs[i], tablePath)
		} else {
			p.accept(name, d)
		}
	}

	p.printCloseParenIfNeeded(n)
}

func (p *printer) printPathIdentifier(raw string, tablePath bool) {
	if tablePath {
		p.print(p.identifierWithCase(raw, p.fmt.opts.TableNameStyle))

		return
	}

	p.print(p.identifier(raw))
}

// sharedSpanPathSegments handles dashed/slashed path prefixes where GoogleSQL's
// SeparatedIdentifierTmpNode::BuildPathParts assigns the combined prefix byte
// range to every leading ASTIdentifier child. When two or more leading children
// share an identical byte span, it extracts each segment's raw text (preserving
// backticks and original casing) instead of letting rawIdentifier repeat the
// combined span for every child.
func (p *printer) sharedSpanPathSegments(names []*googlesql.ASTIdentifier) []string {
	if len(names) < 2 || !sameNodeSpan(names[0], names[1]) {
		return nil
	}

	sharedCount := 2
	for sharedCount < len(names) && sameNodeSpan(names[0], names[sharedCount]) {
		sharedCount++
	}

	start, end := nodeByteRange(names[0])
	if segs := splitPathSegments(p.viewErasedInput(start, end)); len(segs) == sharedCount && !slices.Contains(segs, "") {
		return segs
	}

	fallback := make([]string, sharedCount)
	for i := range sharedCount {
		s, _ := names[i].GetAsString()
		fallback[i] = s
	}

	return fallback
}

func splitPathSegments(s string) []string {
	var (
		segs       []string
		segStart   int
		inBacktick bool
	)

	for i := 0; i < len(s); i++ {
		if inBacktick {
			if s[i] == '\\' && i+1 < len(s) {
				i++

				continue
			}

			if s[i] == '`' {
				inBacktick = false
			}

			continue
		}

		switch s[i] {
		case '`':
			inBacktick = true
		case '.':
			segs = append(segs, strings.TrimSpace(s[segStart:i]))
			segStart = i + 1
		}
	}

	segs = append(segs, strings.TrimSpace(s[segStart:]))

	return segs
}

func (p *printer) visitPathExpressionList(n *googlesql.ASTPathExpressionList, d printerData) {
	p.moveBefore(n)

	exprs := selectChildrenOfType[*googlesql.ASTPathExpression](n)
	parens := len(exprs) > 1

	if parens {
		p.print("(")
	}

	for i, name := range exprs {
		if i > 0 {
			p.print(",")
		}

		p.accept(name, d)
	}

	if parens {
		p.print(")")
	}
}

func (p *printer) visitStar(n *googlesql.ASTStar, _ printerData) {
	p.moveBefore(n)
	img, _ := n.Image()
	p.print(img)
}

func (p *printer) visitStarModifiers(n *googlesql.ASTStarModifiers, d printerData) {
	if el, _ := n.ExceptList(); nodeDefined(el) {
		p.print(p.keyword("EXCEPT"))
		p.print("(")

		for i, e := range selectChildrenOfType[*googlesql.ASTIdentifier](el) {
			if i > 0 {
				p.print(",")
			}

			p.accept(e, d)
		}

		p.print(")")
	}

	items := selectChildrenOfType[*googlesql.ASTStarReplaceItem](n)
	if len(items) > 0 {
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

func (p *printer) visitStarReplaceItem(n *googlesql.ASTStarReplaceItem, d printerData) {
	expr, _ := n.Expression()
	alias, _ := n.Alias()

	p.accept(expr, d)

	if nodeDefined(alias) {
		p.print(p.keyword("AS"))
		p.accept(alias, d)
	}
}

func (p *printer) visitStarWithModifiers(n *googlesql.ASTStarWithModifiers, d printerData) {
	p.moveBefore(n)
	mods, _ := n.Modifiers()

	p.print("*")
	p.accept(mods, d)
}

func (p *printer) visitStructColumnField(n *googlesql.ASTStructColumnField, d printerData) {
	p.moveBefore(n)
	name, _ := n.Name()
	sc, _ := n.Schema()

	p.accept(name, d)
	p.accept(sc, d)
	p.movePast(n)
}

func (p *printer) visitStructConstructorArg(n *googlesql.ASTStructConstructorArg, d printerData) {
	p.moveBefore(n)
	expr, _ := n.Expression()
	alias, _ := n.Alias()

	p.accept(expr, d)
	p.accept(alias, d)
}

func (p *printer) visitStructConstructorWithKeyword(n *googlesql.ASTStructConstructorWithKeyword, d printerData) {
	p.moveBefore(n)
	p.printOpenParenIfNeededWithDepth(n)

	if st, _ := n.StructType(); nodeDefined(st) {
		pp := p.nest()
		pp.accept(st, d)
		p.print(pp.unnest() + "(")
	} else {
		p.print(p.keyword("STRUCT") + "(")
	}

	fields := selectChildrenOfType[*googlesql.ASTStructConstructorArg](n)
	printParenBody(p, fields, p.areSimpleStructConstructorArgs(fields), d)
	p.print(")")
	p.printCloseParenIfNeededWithDepth(n)
}

func (p *printer) visitStructConstructorWithParens(n *googlesql.ASTStructConstructorWithParens, d printerData) {
	p.moveBefore(n)
	p.printOpenParenIfNeededWithDepth(n)

	fields := selectChildrenOfType[googlesql.ASTExpressionNode](n)
	printParenList(p, fields, p.areSimpleExprs(fields), d)
	p.printCloseParenIfNeededWithDepth(n)
}

func (p *printer) visitSystemVariableExpr(n *googlesql.ASTSystemVariableExpr, d printerData) {
	p.moveBefore(n)
	p.printOpenParenIfNeeded(n)
	path, _ := n.Path()

	p.print("@@")

	if p.fmt.opts.SystemVariableStyle != Unspecified && nodeDefined(path) {
		p.moveBefore(path)

		for i, seg := range selectChildrenOfType[*googlesql.ASTIdentifier](path) {
			if i > 0 {
				p.print(".")
			}

			p.moveBefore(seg)
			p.print(p.identifierWithCase(p.rawIdentifier(seg), p.fmt.opts.SystemVariableStyle))
		}
	} else {
		p.accept(path, d)
	}

	p.printCloseParenIfNeeded(n)
}

func (p *printer) visitUnaryExpression(n *googlesql.ASTUnaryExpression, d printerData) {
	p.moveBefore(n)

	needParen := isUnaryParenNeeded(n)
	if needParen {
		p.print("(")
	}

	op, _ := n.Op()
	switch op {
	case googlesql.ASTUnaryExpressionEnums_OpNotSet:
		p.fmt.addUnary(p.keyword("<UNKNOWN OPERATOR>"))
	case googlesql.ASTUnaryExpressionEnums_OpNot:
		p.fmt.addUnary(p.keyword("NOT"))
	case googlesql.ASTUnaryExpressionEnums_OpBitwiseNot:
		p.fmt.addUnary("~")
	case googlesql.ASTUnaryExpressionEnums_OpMinus:
		p.fmt.addUnary("-")
	case googlesql.ASTUnaryExpressionEnums_OpPlus:
		p.fmt.addUnary("+")
	case googlesql.ASTUnaryExpressionEnums_OpIsUnknown,
		googlesql.ASTUnaryExpressionEnums_OpIsNotUnknown:
		// Postfix unary handled after operand.
	}

	operand, _ := n.Operand()
	p.accept(operand, d)

	switch op {
	case googlesql.ASTUnaryExpressionEnums_OpIsUnknown:
		p.print(p.keyword("IS UNKNOWN"))
	case googlesql.ASTUnaryExpressionEnums_OpIsNotUnknown:
		p.print(p.keyword("IS NOT UNKNOWN"))
	default:
		// Prefix unary handled before operand.
	}

	if needParen {
		p.print(")")
	}

	p.movePast(n)
}

func isUnaryParenNeeded(n *googlesql.ASTUnaryExpression) bool {
	parent := nodeParent(n)
	if !nodeDefined(parent) {
		return false
	}

	pp := precedenceNum(parent)
	cp := precedenceUnaryExpr(n)

	if pp < 1000 && cp > 0 && pp > cp {
		return true
	}

	if paren, _ := n.Parenthesized(); paren {
		switch nodeKind(parent) {
		case googlesql.ASTNodeKindAstDotIdentifier,
			googlesql.ASTNodeKindAstDotGeneralizedField,
			googlesql.ASTNodeKindAstDotStar,
			googlesql.ASTNodeKindAstDotStarWithModifiers,
			googlesql.ASTNodeKindAstArrayElement:
			return true
		default:
			// Parentheses not required by postfix access.
		}
	}

	return false
}

func (p *printer) visitUnnestExpression(n *googlesql.ASTUnnestExpression, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("UNNEST") + "(")

	exprs := selectChildrenOfType[*googlesql.ASTExpressionWithOptAlias](n)
	for i, e := range exprs {
		if i > 0 {
			p.print(",")
		}

		p.accept(e, d)
	}

	if zm, _ := n.ArrayZipMode(); nodeDefined(zm) {
		if len(exprs) > 0 {
			p.print(",")
		}

		p.accept(zm, d)
	}

	p.print(")")
}

func (p *printer) visitWithExpression(n *googlesql.ASTWithExpression, d printerData) {
	p.moveBefore(n)
	p.printOpenParenIfNeeded(n)

	vars, _ := n.Variables()
	expr, _ := n.Expression()
	cols := selectChildrenOfType[*googlesql.ASTSelectColumn](vars)

	p.print(p.keyword("WITH") + "(")

	simple := len(cols) <= 2 && p.isSimpleExpr(expr)
	for _, c := range cols {
		ce, _ := c.Expression()
		if !p.isSimpleExpr(ce) {
			simple = false

			break
		}
	}

	if !simple {
		p.println("")
		p.incDepth()
	}

	for _, c := range cols {
		al, _ := c.Alias()
		ce, _ := c.Expression()

		if nodeDefined(al) {
			id, _ := al.Identifier()
			p.accept(id, d)
			p.print(p.keyword("AS"))
		}

		p.acceptNested(ce, d)
		p.print(",")

		if !simple {
			p.println("")
		}
	}

	p.acceptNested(expr, d)

	if !simple {
		p.println("")
		p.decDepth()
	}

	p.print(")")
	p.printCloseParenIfNeeded(n)
	p.movePast(n)
}

func (p *printer) printChainedBase(baseExpr googlesql.ASTExpressionNode, d printerData) {
	forceParen := needsChainedBaseParens(baseExpr)
	if forceParen {
		p.print("(")
	}

	p.accept(baseExpr, d)

	if forceParen {
		p.print(")")
	}
}

func needsChainedBaseParens(n googlesql.ASTExpressionNode) bool {
	if !nodeDefined(n) {
		return false
	}

	switch nodeKind(n) {
	case googlesql.ASTNodeKindAstIntLiteral,
		googlesql.ASTNodeKindAstFloatLiteral,
		googlesql.ASTNodeKindAstUnaryExpression,
		googlesql.ASTNodeKindAstDateOrTimeLiteral,
		googlesql.ASTNodeKindAstNumericLiteral,
		googlesql.ASTNodeKindAstBignumericLiteral,
		googlesql.ASTNodeKindAstJsonLiteral,
		googlesql.ASTNodeKindAstIntervalExpr:
		return true
	case googlesql.ASTNodeKindAstStringLiteral,
		googlesql.ASTNodeKindAstBytesLiteral,
		googlesql.ASTNodeKindAstBooleanLiteral,
		googlesql.ASTNodeKindAstNullLiteral,
		googlesql.ASTNodeKindAstParameterExpr:
		paren, _ := n.Parenthesized()

		return paren
	default:
		return false
	}
}

func (p *printer) visitChainedBaseExpr(n *googlesql.ASTChainedBaseExpr, d printerData) {
	p.moveBefore(n)
	expr, _ := n.Expr()
	p.printChainedBase(expr, d)
	p.movePast(n)
}

func (p *printer) visitExtendedPathExpression(n *googlesql.ASTExtendedPathExpression, d printerData) {
	p.moveBefore(n)
	pp, _ := n.ParenthesizedPath()
	gp, _ := n.GeneralizedPathExpression()

	p.accept(pp, d)
	p.print(".")
	p.accept(gp, d)
	p.movePast(n)
}

func (p *printer) visitLikeExpression(n *googlesql.ASTLikeExpression, d printerData) {
	p.moveBefore(n)
	p.printOpenParenIfNeeded(n)

	lhs, _ := n.Lhs()
	isNot, _ := n.IsNot()
	op, _ := n.Op()
	hint, _ := n.Hint()
	inList, _ := n.InList()
	unnest, _ := n.UnnestExpr()
	q, _ := n.Query()

	p.accept(lhs, d)

	if isNot {
		p.print(p.keyword("NOT LIKE"))
	} else {
		p.print(p.keyword("LIKE"))
	}

	p.printQuantifiedRHS(op, hint, inList, unnest, q, d)
	p.printCloseParenIfNeeded(n)
	p.movePast(n)
}

func (p *printer) visitQuantifiedComparisonExpression(
	n *googlesql.ASTQuantifiedComparisonExpression,
	d printerData,
) {
	p.moveBefore(n)
	p.printOpenParenIfNeeded(n)

	lhs, _ := n.Lhs()
	sqlOp, _ := n.GetSQLForOperator()
	quant, _ := n.Quantifier()
	hint, _ := n.Hint()
	inList, _ := n.InList()
	unnest, _ := n.UnnestExpr()
	q, _ := n.Query()

	p.accept(lhs, d)
	p.print(sqlOp)
	p.printQuantifiedRHS(quant, hint, inList, unnest, q, d)
	p.printCloseParenIfNeeded(n)
	p.movePast(n)
}

func (p *printer) printQuantifiedRHS(
	quant *googlesql.ASTAnySomeAllOp,
	hint *googlesql.ASTHint,
	inList *googlesql.ASTInList,
	unnest *googlesql.ASTUnnestExpression,
	q *googlesql.ASTQuery,
	d printerData,
) {
	needParenSpace := nodeDefined(inList) || nodeDefined(q)

	if kw := anySomeAllKeyword(quant); kw != "" {
		if needParenSpace && !nodeDefined(hint) {
			p.print(p.keyword(kw) + " ")
		} else {
			p.print(p.keyword(kw))
		}
	}

	if nodeDefined(hint) {
		p.accept(hint, d)

		if needParenSpace {
			p.fmt.writeSpace()
		}
	}

	p.accept(inList, d)
	p.accept(unnest, d)

	if nodeDefined(q) {
		p.printDelimitedQuery(q, nil, d)
	}
}

func anySomeAllKeyword(n *googlesql.ASTAnySomeAllOp) string {
	if !nodeDefined(n) {
		return ""
	}

	op, _ := n.Op()
	switch op {
	case googlesql.ASTAnySomeAllOpEnums_OpKAny:
		return "ANY"
	case googlesql.ASTAnySomeAllOpEnums_OpKSome:
		return "SOME"
	case googlesql.ASTAnySomeAllOpEnums_OpKAll:
		return "ALL"
	case googlesql.ASTAnySomeAllOpEnums_OpKUninitialized:
		return ""
	}

	return ""
}

func (p *printer) visitDefaultLiteral(n *googlesql.ASTDefaultLiteral, _ printerData) {
	p.moveBefore(n)
	p.print(p.keyword("DEFAULT"))
	p.movePast(n)
}

func (p *printer) visitMaxLiteral(n *googlesql.ASTMaxLiteral, _ printerData) {
	p.moveBefore(n)
	p.print(p.keyword("MAX"))
	p.movePast(n)
}

func (p *printer) visitInputTableArgument(n *googlesql.ASTInputTableArgument, _ printerData) {
	p.moveBefore(n)
	p.print(p.keyword("INPUT TABLE"))
	p.movePast(n)
}

func (p *printer) visitExpressionWithAlias(n *googlesql.ASTExpressionWithAlias, d printerData) {
	p.moveBefore(n)

	expr, _ := n.Expression()
	alias, _ := n.Alias()

	p.accept(expr, d)
	p.moveBefore(alias)
	p.accept(alias, d)
	p.movePast(n)
}

func (p *printer) visitReplaceFieldsExpression(n *googlesql.ASTReplaceFieldsExpression, d printerData) {
	p.moveBefore(n)

	expr, _ := n.Expr()
	args := selectChildrenOfType[*googlesql.ASTReplaceFieldsArg](n)
	simple := len(args) <= 3 &&
		!p.hasCommentsInside(n) &&
		p.isSimpleExpr(expr) &&
		p.areSimpleReplaceFieldsArgs(args)

	pp := p.nest()
	pp.printOpenParenIfNeeded(n)
	pp.print(pp.keyword("REPLACE_FIELDS") + "(")

	if !simple {
		pp.println("")
		pp.incDepth()
		pp.acceptNested(expr, d)
	} else {
		pp.accept(expr, d)
	}

	for i, arg := range args {
		pp.print(",")

		if !simple {
			if i == 0 {
				pp.movePastLine(expr)
			} else {
				pp.movePastLine(args[i-1])
			}

			pp.println("")
		}

		pp.printReplaceFieldsArg(arg, simple, d)
	}

	paren, _ := n.Parenthesized()
	if !paren {
		pp.movePast(n)
	}

	if !simple {
		pp.println("")
		pp.decDepth()
	}

	pp.print(")")

	if paren {
		pp.movePast(n)
	}

	pp.printCloseParenIfNeeded(n)
	p.print(pp.unnest())
}

func (p *printer) hasCommentsInside(n googlesql.ASTNode) bool {
	_, end := nodeByteRange(n)
	if end <= 0 {
		return false
	}

	lhs, _ := extensions.SplitComments(p.fmt.comments.comments, end-1)

	return len(lhs) > 0
}

func (p *printer) areSimpleReplaceFieldsArgs(args []*googlesql.ASTReplaceFieldsArg) bool {
	return allMatch(args, func(arg *googlesql.ASTReplaceFieldsArg) bool {
		expr, _ := arg.Expression()

		return p.isSimpleExpr(expr)
	})
}

func (p *printer) printReplaceFieldsArg(arg *googlesql.ASTReplaceFieldsArg, simple bool, d printerData) {
	expr, _ := arg.Expression()
	path, _ := arg.PathExpression()

	if simple {
		p.moveBefore(arg)
		p.accept(expr, d)
		p.flushCommentsBeforeOperator(expr, path)
		p.print(p.keyword("AS") + " ")
		p.accept(path, d)
		p.movePast(arg)

		return
	}

	pp := p.nest()
	pp.moveBefore(arg)
	pp.acceptNested(expr, d)
	pp.flushCommentsBeforeOperator(expr, path)
	pp.print(pp.keyword("AS") + " ")
	pp.accept(path, d)
	pp.movePast(arg)
	p.print(pp.unnestLeft())
}

func (p *printer) visitSequenceArg(n *googlesql.ASTSequenceArg, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("SEQUENCE"))

	seqPath, _ := n.SequencePath()
	p.accept(seqPath, d)
	p.movePast(n)
}
