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

		return p.areSimpleExprs2(ops)
	case *googlesql.ASTUnaryExpression:
		op, _ := b.Operand()

		return p.isSimpleExpr2(op)
	case *googlesql.ASTIntervalExpr:
		val, _ := b.IntervalValue()

		return p.isSimpleExpr2(val)
	case *googlesql.ASTFunctionCall:
		return p.isSimpleFunctionCall(b, 4)
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
		googlesql.ASTNodeKindAstIdentifier,
		googlesql.ASTNodeKindAstDefaultLiteral,
		googlesql.ASTNodeKindAstMaxLiteral,
		googlesql.ASTNodeKindAstInputTableArgument:
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

	k := nodeKind(n)
	if isSimpleAtomKind(k) {
		return true
	}

	switch k {
	case googlesql.ASTNodeKindAstBytesLiteral, googlesql.ASTNodeKindAstStringLiteral:
		return !strings.Contains(p.nodeInput(n), "\n")
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
			return p.isSimpleFunctionCall(f, 2)
		}
	default:
		return false
	}

	return false
}

func (p *printer) isSimpleFunctionCall(n *googlesql.ASTFunctionCall, maxArgs int) bool {
	args := functionCallArgs(n)

	return len(args) <= maxArgs &&
		countFunctionCallElements(n) <= 1 &&
		p.areSimpleExprs2(args) &&
		!p.hasComplexFunctionModifier(n)
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

	return len(conjuncts) <= 4 && p.areSimpleExprs2(conjuncts)
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

	return len(disjuncts) <= 4 && p.areSimpleExprs2(disjuncts)
}

func isSimpleColumnSchema(n googlesql.ASTColumnSchemaNode) bool {
	if !nodeDefined(n) {
		return true
	}

	switch nodeKind(n) {
	case googlesql.ASTNodeKindAstArrayColumnSchema,
		googlesql.ASTNodeKindAstRangeColumnSchema,
		googlesql.ASTNodeKindAstStructColumnSchema:
		if nodeNumChildren(n) > 1 {
			return false
		}

		return isSimpleColumnSchema2(nodeChild(n, 0))
	case googlesql.ASTNodeKindAstInferredTypeColumnSchema, googlesql.ASTNodeKindAstMapColumnSchema:
		return false
	case googlesql.ASTNodeKindAstSimpleColumnSchema:
		return true
	default:
		return false
	}
}

func isSimpleColumnSchema2(n googlesql.ASTNode) bool {
	if !nodeDefined(n) {
		return false
	}

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
		if nodeNumChildren(n) > 1 {
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

		if functions > 1 || aliases > 1 || !p.isSimpleExpr2(expr) {
			return false
		}
	}

	return true
}

func (p *printer) areAlignable(exprs []googlesql.ASTExpressionNode) bool {
	return allMatch(exprs, func(e googlesql.ASTExpressionNode) bool {
		switch nodeKind(e) {
		case googlesql.ASTNodeKindAstBinaryExpression, googlesql.ASTNodeKindAstConcatExpr:
			return p.isSimpleExpr(e)
		case googlesql.ASTNodeKindAstUnaryExpression:
			return true
		default:
			return false
		}
	})
}

func isSimpleFunctionParameters(params *googlesql.ASTFunctionParameters, maxParams int) bool {
	if !nodeDefined(params) {
		return true
	}

	if maxParams <= 0 {
		maxParams = 2
	}

	entries := selectChildrenOfType[*googlesql.ASTFunctionParameter](params)

	return len(entries) <= maxParams && areSimpleFunctionParameters(entries)
}

func areSimpleFunctionParameters(params []*googlesql.ASTFunctionParameter) bool {
	return allMatch(params, func(param *googlesql.ASTFunctionParameter) bool {
		if typ, _ := param.Type(); nodeDefined(typ) {
			return isSimpleType(typ)
		}

		tpt, _ := param.TemplatedParameterType()

		return nodeDefined(tpt)
	})
}

func areSimpleTVFSchemaColumns(cols []*googlesql.ASTTVFSchemaColumn) bool {
	return allMatch(cols, func(c *googlesql.ASTTVFSchemaColumn) bool {
		typ, _ := c.Type()

		return isSimpleType(typ)
	})
}

func (p *printer) isSimpleOptionsList(n *googlesql.ASTOptionsList) bool {
	entries := selectChildrenOfType[*googlesql.ASTOptionsEntry](n)

	return allMatch(entries, func(e *googlesql.ASTOptionsEntry) bool {
		val, _ := e.Value()

		return p.isSimpleExpr(val)
	})
}

func (p *printer) isSimplePivotExpressionList(n *googlesql.ASTPivotExpressionList) bool {
	exprs := selectChildrenOfType[*googlesql.ASTPivotExpression](n)

	return allMatch(exprs, func(a *googlesql.ASTPivotExpression) bool {
		expr, _ := a.Expression()
		alias, _ := a.Alias()

		return p.isSimpleExpr(expr) && !nodeDefined(alias)
	})
}

func (p *printer) isSimplePivotValueList(n *googlesql.ASTPivotValueList) bool {
	if !nodeDefined(n) {
		return true
	}

	exprs := selectChildrenOfType[*googlesql.ASTPivotValue](n)

	return allMatch(exprs, func(a *googlesql.ASTPivotValue) bool {
		val, _ := a.Value()
		alias, _ := a.Alias()

		return p.isSimpleExpr(val) && !nodeDefined(alias)
	})
}

func (p *printer) areSimpleStructConstructorArgs(args []*googlesql.ASTStructConstructorArg) bool {
	return allMatch(args, func(a *googlesql.ASTStructConstructorArg) bool {
		expr, _ := a.Expression()

		return p.isSimpleExpr(expr)
	})
}

func areSimpleStructFields(fields []*googlesql.ASTStructField) bool {
	return allMatch(fields, func(f *googlesql.ASTStructField) bool {
		typ, _ := f.Type()

		return isSimpleType(typ)
	})
}

func (p *printer) areSimpleTVFArguments(args []*googlesql.ASTTVFArgument) bool {
	return allMatch(args, p.isSimpleTVFArgument)
}

func isSimpleUnpivotInItemList(n *googlesql.ASTUnpivotInItemList) bool {
	items := selectChildrenOfType[*googlesql.ASTUnpivotInItem](n)

	return allMatch(items, func(item *googlesql.ASTUnpivotInItem) bool {
		alias, _ := item.Alias()

		return !nodeDefined(alias)
	})
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

func (p *printer) areSimpleExprs(exprs []googlesql.ASTExpressionNode) bool {
	return allMatch(exprs, p.isSimpleExpr)
}

func (p *printer) areSimpleExprs2(exprs []googlesql.ASTExpressionNode) bool {
	return allMatch(exprs, p.isSimpleExpr2)
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
			if len(entries) > 1 || !p.isSimpleOptionsList(ol) {
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
