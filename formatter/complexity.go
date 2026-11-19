package formatter

import (
	"strings"

	"github.com/goccy/go-googlesql"

	"github.com/paulourio/bqlang/extensions"
)

func isSimpleType(n googlesql.ASTTypeNode) bool {
	if !nodeDefined(n) {
		return true
	}

	switch nodeKind(n) {
	case googlesql.ASTNodeKindAstArrayType,
		googlesql.ASTNodeKindAstStructType,
		googlesql.ASTNodeKindAstRangeType,
		googlesql.ASTNodeKindAstMapType,
		googlesql.ASTNodeKindAstFunctionType:
		return false
	case googlesql.ASTNodeKindAstSimpleType:
		if b, ok := n.(*googlesql.ASTSimpleType); ok {
			tp, _ := b.TypeParameters()

			return !nodeDefined(tp)
		}

		return true
	default:
		return true
	}
}

// isSimpleExpr determines whether an expression fits cleanly on a single line.
func (p *printer) isSimpleExpr(n googlesql.ASTExpressionNode) bool {
	if !nodeDefined(n) || isSimpleAtomKind(nodeKind(n)) {
		return true
	}

	switch b := n.(type) {
	case *googlesql.ASTAndExpr:
		return p.isSimpleAndExpr(b)
	case *googlesql.ASTOrExpr:
		return p.isSimpleOrExpr(b)
	case *googlesql.ASTBytesLiteral, *googlesql.ASTStringLiteral:
		return !strings.Contains(p.nodeInput(n), "\n")
	case *googlesql.ASTExtractExpression:
		lhs, _ := b.LhsExpr()
		rhs, _ := b.RhsExpr()

		return p.isSimpleExpr(lhs) && p.isSimpleExpr(rhs)
	case *googlesql.ASTBinaryExpression:
		lhs, _ := b.Lhs()
		rhs, _ := b.Rhs()

		return p.isSimpleExpr2(lhs) && p.isSimpleExpr(rhs)
	case *googlesql.ASTConcatExpr:
		ops := selectChildrenOfType[googlesql.ASTExpressionNode](n)

		return allTrue(p.mapIsSimpleExprs2(ops))
	case *googlesql.ASTUnaryExpression:
		op, _ := b.Operand()

		return p.isSimpleExpr2(op)
	case *googlesql.ASTIntervalExpr:
		val, _ := b.IntervalValue()

		return p.isSimpleExpr2(val)
	case *googlesql.ASTFunctionCall:
		args := functionCallArgs(b)
		elems := countFunctionCallElements(b)

		return len(args) <= 4 && elems <= 1 &&
			allTrue(p.mapIsSimpleExprs2(args)) &&
			!p.hasComplexFunctionModifier(b)
	case *googlesql.ASTNamedArgument:
		expr, _ := b.Expr()

		return p.isSimpleExpr2(expr)
	default:
		return false
	}
}

func isSimpleAtomKind(k googlesql.ASTNodeKind) bool {
	switch k {
	case googlesql.ASTNodeKindAstPathExpression,
		googlesql.ASTNodeKindAstStar,
		googlesql.ASTNodeKindAstBignumericLiteral,
		googlesql.ASTNodeKindAstBooleanLiteral,
		googlesql.ASTNodeKindAstDateOrTimeLiteral,
		googlesql.ASTNodeKindAstFloatLiteral,
		googlesql.ASTNodeKindAstIntLiteral,
		googlesql.ASTNodeKindAstJsonLiteral,
		googlesql.ASTNodeKindAstNullLiteral,
		googlesql.ASTNodeKindAstNumericLiteral,
		googlesql.ASTNodeKindAstParameterExpr,
		googlesql.ASTNodeKindAstSystemVariableExpr,
		googlesql.ASTNodeKindAstIdentifier:
		return true
	default:
		return false
	}
}

// isSimpleExpr2 applies a stricter single-line complexity check for nested operands.
func (p *printer) isSimpleExpr2(n googlesql.ASTExpressionNode) bool {
	if !nodeDefined(n) {
		return true
	}

	switch nodeKind(n) {
	case googlesql.ASTNodeKindAstPathExpression,
		googlesql.ASTNodeKindAstStar,
		googlesql.ASTNodeKindAstBignumericLiteral,
		googlesql.ASTNodeKindAstBooleanLiteral,
		googlesql.ASTNodeKindAstBytesLiteral,
		googlesql.ASTNodeKindAstDateOrTimeLiteral,
		googlesql.ASTNodeKindAstFloatLiteral,
		googlesql.ASTNodeKindAstIntLiteral,
		googlesql.ASTNodeKindAstJsonLiteral,
		googlesql.ASTNodeKindAstNullLiteral,
		googlesql.ASTNodeKindAstNumericLiteral,
		googlesql.ASTNodeKindAstParameterExpr,
		googlesql.ASTNodeKindAstStringLiteral,
		googlesql.ASTNodeKindAstSystemVariableExpr,
		googlesql.ASTNodeKindAstIdentifier:
		return true
	case googlesql.ASTNodeKindAstBinaryExpression, googlesql.ASTNodeKindAstConcatExpr:
		parent := nodeParent(n)

		return !nodeDefined(parent) || !isBinaryOrConcatKind(nodeKind(parent))
	case googlesql.ASTNodeKindAstUnaryExpression:
		if e, ok := n.(*googlesql.ASTUnaryExpression); ok {
			op, _ := e.Operand()

			return p.isSimpleExpr2(op)
		}
	case googlesql.ASTNodeKindAstFunctionCall:
		if f, ok := n.(*googlesql.ASTFunctionCall); ok {
			args := functionCallArgs(f)
			elems := countFunctionCallElements(f)

			return len(args) <= 2 && elems <= 1 &&
				allTrue(p.mapIsSimpleExprs2(args)) &&
				!p.hasComplexFunctionModifier(f)
		}
	default:
		return false
	}

	return false
}

func isBinaryOrConcatKind(k googlesql.ASTNodeKind) bool {
	return k == googlesql.ASTNodeKindAstBinaryExpression || k == googlesql.ASTNodeKindAstConcatExpr
}

func (p *printer) isSimpleAndExpr(n *googlesql.ASTAndExpr) bool {
	conjuncts := selectChildrenOfType[googlesql.ASTExpressionNode](n)

	parent := nodeParent(n)
	if nodeDefined(parent) {
		switch nodeKind(parent) {
		case googlesql.ASTNodeKindAstMergeStatement,
			googlesql.ASTNodeKindAstMergeWhenClause,
			googlesql.ASTNodeKindAstDeleteStatement,
			googlesql.ASTNodeKindAstUpdateStatement:
			return false
		case googlesql.ASTNodeKindAstWhereClause:
			if nodeKind(nodeParent(parent)) != googlesql.ASTNodeKindAstFunctionCall {
				return false
			}
		default:
			// Continue complexity check.
		}
	}

	return len(conjuncts) <= 4 && allTrue(p.mapIsSimpleExprs2(conjuncts))
}

func (p *printer) isSimpleOrExpr(n *googlesql.ASTOrExpr) bool {
	disjuncts := selectChildrenOfType[googlesql.ASTExpressionNode](n)

	parent := nodeParent(n)
	if nodeDefined(parent) {
		switch nodeKind(parent) {
		case googlesql.ASTNodeKindAstMergeStatement, googlesql.ASTNodeKindAstMergeWhenClause:
			return false
		default:
			// Continue complexity check.
		}
	}

	return len(disjuncts) <= 4 && allTrue(p.mapIsSimpleExprs2(disjuncts))
}

func isSimpleColumnSchema(n googlesql.ASTColumnSchemaNode) bool {
	if !nodeDefined(n) {
		return true
	}

	num := nodeNumChildren(n)

	switch nodeKind(n) {
	case googlesql.ASTNodeKindAstArrayColumnSchema, googlesql.ASTNodeKindAstRangeColumnSchema:
		if num > 1 {
			return false
		}

		return isSimpleColumnSchema2(nodeChild(n, 0))
	case googlesql.ASTNodeKindAstInferredTypeColumnSchema, googlesql.ASTNodeKindAstMapColumnSchema:
		return false
	case googlesql.ASTNodeKindAstSimpleColumnSchema:
		return true
	case googlesql.ASTNodeKindAstStructColumnSchema:
		if num > 1 {
			return false
		}

		return isSimpleColumnSchema2(nodeChild(n, 0))
	default:
		return false
	}
}

func isSimpleColumnSchema2(n googlesql.ASTNode) bool {
	if !nodeDefined(n) {
		return false
	}

	num := nodeNumChildren(n)

	switch cs := n.(type) {
	case *googlesql.ASTArrayColumnSchema,
		*googlesql.ASTRangeColumnSchema,
		*googlesql.ASTMapColumnSchema,
		*googlesql.ASTInferredTypeColumnSchema:
		return false
	case *googlesql.ASTSimpleColumnSchema:
		return true
	case *googlesql.ASTStructColumnField:
		schema, _ := cs.Schema()

		return isSimpleColumnSchema(schema)
	case *googlesql.ASTStructColumnSchema:
		if num > 1 {
			return false
		}

		return isSimpleColumnSchema2(nodeChild(cs, 0))
	default:
		return false
	}
}

// maybeSingleLineColumns inspects a SELECT to determine whether its columns may
// be rendered on a single line.
func (p *printer) maybeSingleLineColumns(n *googlesql.ASTSelect) bool {
	sl, err := n.SelectList()
	if err != nil || !nodeDefined(sl) {
		return false
	}

	cols := selectChildrenOfType[*googlesql.ASTSelectColumn](sl)
	if len(cols) > p.fmt.opts.MaxColumnsForSingleLineSelect {
		return false
	}

	_, e := nodeByteRange(n)
	if lhs, _ := extensions.SplitComments(p.fmt.comments.comments, e); len(lhs) > 0 {
		return false
	}

	r := make([]bool, 0, len(cols))
	functions := 0
	aliases := 0

	for _, c := range cols {
		expr, _ := c.Expression()
		if !nodeDefined(expr) {
			continue
		}

		if nodeKind(expr) == googlesql.ASTNodeKindAstFunctionCall {
			functions++
		}

		if alias, _ := c.Alias(); nodeDefined(alias) {
			aliases++
		}

		r = append(r, p.isSimpleExpr2(expr))
	}

	return functions <= 1 && aliases <= 1 && allTrue(r)
}

func (p *printer) mapIsAlignable(exprs []googlesql.ASTExpressionNode) []bool {
	r := make([]bool, 0, len(exprs))

	for _, e := range exprs {
		var simple bool

		switch nodeKind(e) {
		case googlesql.ASTNodeKindAstBinaryExpression, googlesql.ASTNodeKindAstConcatExpr:
			simple = p.isSimpleExpr(e)
		case googlesql.ASTNodeKindAstUnaryExpression:
			simple = true
		default:
			simple = false
		}

		r = append(r, simple)
	}

	return r
}

func mapIsSimpleFunctionParameters(params []*googlesql.ASTFunctionParameter) []bool {
	r := make([]bool, 0, len(params))

	for _, param := range params {
		simple := false

		if typ, _ := param.Type(); nodeDefined(typ) {
			simple = isSimpleType(typ)
		} else if tpt, _ := param.TemplatedParameterType(); nodeDefined(tpt) {
			simple = true
		}

		r = append(r, simple)
	}

	return r
}

func mapIsSimpleTVFSchema(cols []*googlesql.ASTTVFSchemaColumn) []bool {
	r := make([]bool, 0, len(cols))

	for _, c := range cols {
		typ, _ := c.Type()
		r = append(r, isSimpleType(typ))
	}

	return r
}

func (p *printer) mapIsSimpleOptionsList(n *googlesql.ASTOptionsList) []bool {
	entries := selectChildrenOfType[*googlesql.ASTOptionsEntry](n)
	r := make([]bool, 0, len(entries))

	for _, e := range entries {
		val, _ := e.Value()
		r = append(r, p.isSimpleExpr(val))
	}

	return r
}

func (p *printer) mapIsSimplePathExpressionList(n *googlesql.ASTPathExpressionList) []bool {
	paths := selectChildrenOfType[*googlesql.ASTPathExpression](n)
	r := make([]bool, 0, len(paths))

	for _, path := range paths {
		r = append(r, p.isSimpleExpr(path))
	}

	return r
}

func (p *printer) mapIsSimplePivotExpressionList(n *googlesql.ASTPivotExpressionList) []bool {
	exprs := selectChildrenOfType[*googlesql.ASTPivotExpression](n)
	r := make([]bool, 0, len(exprs))

	for _, a := range exprs {
		expr, _ := a.Expression()
		alias, _ := a.Alias()
		r = append(r, p.isSimpleExpr(expr) && !nodeDefined(alias))
	}

	return r
}

func (p *printer) mapIsSimplePivotForExpression(n *googlesql.ASTPivotClause) []bool {
	lhs, _ := n.ForExpression()
	vl, _ := n.PivotValues()

	lhsSimple := p.isSimpleExpr(lhs)
	vlSimple := p.mapIsSimplePivotValueList(vl)

	return append([]bool{lhsSimple}, vlSimple...)
}

func (p *printer) mapIsSimplePivotValueList(n *googlesql.ASTPivotValueList) []bool {
	if !nodeDefined(n) {
		return nil
	}

	exprs := selectChildrenOfType[*googlesql.ASTPivotValue](n)
	r := make([]bool, 0, len(exprs))

	for _, a := range exprs {
		val, _ := a.Value()
		alias, _ := a.Alias()
		r = append(r, p.isSimpleExpr(val) && !nodeDefined(alias))
	}

	return r
}

func (p *printer) mapIsSimpleStructConstructorArg(args []*googlesql.ASTStructConstructorArg) []bool {
	r := make([]bool, 0, len(args))

	for _, a := range args {
		expr, _ := a.Expression()
		r = append(r, p.isSimpleExpr(expr))
	}

	return r
}

func mapIsSimpleStructFields(fields []*googlesql.ASTStructField) []bool {
	r := make([]bool, 0, len(fields))

	for _, f := range fields {
		typ, _ := f.Type()
		r = append(r, isSimpleType(typ))
	}

	return r
}

func (p *printer) mapIsSimpleTVFArguments(args []*googlesql.ASTTVFArgument) []bool {
	r := make([]bool, 0, len(args))

	for _, a := range args {
		r = append(r, p.isSimpleTVFArgument(a))
	}

	return r
}

func (p *printer) mapIsSimpleUnpivotInItemList(n *googlesql.ASTUnpivotInItemList) []bool {
	items := selectChildrenOfType[*googlesql.ASTUnpivotInItem](n)
	r := make([]bool, 0, len(items))

	for _, item := range items {
		cols, _ := item.UnpivotColumns()
		alias, _ := item.Alias()
		simple := allTrue(p.mapIsSimplePathExpressionList(cols))
		r = append(r, simple && !nodeDefined(alias))
	}

	return r
}

func (p *printer) isSimpleTVFArgument(n *googlesql.ASTTVFArgument) bool {
	if expr, _ := n.Expr(); nodeDefined(expr) && !p.isSimpleExpr(expr) {
		return false
	}

	if tc, _ := n.TableClause(); nodeDefined(tc) {
		return false
	}

	if mc, _ := n.ModelClause(); nodeDefined(mc) {
		return false
	}

	if cc, _ := n.ConnectionClause(); nodeDefined(cc) {
		return false
	}

	if desc, _ := n.Descriptor(); nodeDefined(desc) {
		return false
	}

	return true
}

func (p *printer) mapIsSimpleExprs(n []googlesql.ASTExpressionNode) []bool {
	r := make([]bool, 0, len(n))

	for _, e := range n {
		r = append(r, p.isSimpleExpr(e))
	}

	return r
}

func (p *printer) mapIsSimpleExprs2(n []googlesql.ASTExpressionNode) []bool {
	r := make([]bool, 0, len(n))

	for _, e := range n {
		r = append(r, p.isSimpleExpr2(e))
	}

	return r
}

func countFunctionCallElements(n *googlesql.ASTFunctionCall) int {
	elems := 0

	if nh, _ := n.NullHandlingModifier(); nh != googlesql.ASTFunctionCallEnums_NullHandlingModifierDefaultNullHandling {
		elems++
	}

	if hm, _ := n.HavingModifier(); nodeDefined(hm) {
		elems++
	}

	if cb, _ := n.ClampedBetweenModifier(); nodeDefined(cb) {
		elems++
	}

	if ob, _ := n.OrderBy(); nodeDefined(ob) {
		elems++
	}

	if lo, _ := n.LimitOffset(); nodeDefined(lo) {
		elems++
	}

	if gb, _ := n.GroupBy(); nodeDefined(gb) {
		elems++
	}

	if we, _ := n.WhereExpr(); nodeDefined(we) {
		elems++
	}

	if he, _ := n.HavingExpr(); nodeDefined(he) {
		elems++
	}

	if wr, _ := n.WithReportModifier(); nodeDefined(wr) {
		elems++
	}

	return elems
}

func (p *printer) hasComplexFunctionModifier(n *googlesql.ASTFunctionCall) bool {
	if we, _ := n.WhereExpr(); nodeDefined(we) {
		if expr, _ := we.Expression(); !p.isSimpleExpr(expr) {
			return true
		}
	}

	if he, _ := n.HavingExpr(); nodeDefined(he) {
		if expr, _ := he.Expression(); !p.isSimpleExpr(expr) {
			return true
		}
	}

	if gb, _ := n.GroupBy(); nodeDefined(gb) && p.isComplexGroupByModifier(gb) {
		return true
	}

	if wr, _ := n.WithReportModifier(); nodeDefined(wr) {
		if ol, _ := wr.OptionsList(); nodeDefined(ol) {
			entries := selectChildrenOfType[*googlesql.ASTOptionsEntry](ol)
			if len(entries) > 1 || !allTrue(p.mapIsSimpleOptionsList(ol)) {
				return true
			}
		}
	}

	return false
}

func (p *printer) isComplexGroupByModifier(gb *googlesql.ASTGroupBy) bool {
	items := selectChildrenOfType[*googlesql.ASTGroupingItem](gb)
	if len(items) > 4 {
		return true
	}

	for _, item := range items {
		if gsl, _ := item.GroupingSetList(); nodeDefined(gsl) {
			return true
		}

		if expr, _ := item.Expression(); nodeDefined(expr) && !p.isSimpleExpr(expr) {
			return true
		}
	}

	return false
}

func countWindowSpecElems(n *googlesql.ASTWindowSpecification) int {
	if !nodeDefined(n) {
		return 0
	}

	elems := 0

	if wn, _ := n.BaseWindowName(); nodeDefined(wn) {
		elems++
	}

	if pb, _ := n.PartitionBy(); nodeDefined(pb) {
		elems++
	}

	if ob, _ := n.OrderBy(); nodeDefined(ob) {
		elems++
	}

	if wf, _ := n.WindowFrame(); nodeDefined(wf) {
		elems++
	}

	return elems
}
