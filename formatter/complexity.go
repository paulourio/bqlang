// This file contains a set of analyzes to help assessing the complexity
// of a AST sub-tree.
package formatter

import (
	"strings"

	"github.com/goccy/go-zetasql/ast"
	"github.com/paulourio/bqlang/extensions"
)

func isSimpleType(n ast.TypeNode) bool {
	switch n.Kind() {
	case ast.ArrayType:
		return false
	case ast.StructType:
		return false
	case ast.SimpleType:
		b := n.(*ast.SimpleTypeNode)
		if b.TypeParameters() != nil {
			return false
		}

		return true
	}

	return true
}

// isSimpleExpr tries to determine if the expression is ok to be
// rendered in a single line.
func isSimpleExpr(n ast.ExpressionNode) bool {
	switch n.Kind() {
	case ast.AndExpr:
		return isSimpleAndExpr(n.(*ast.AndExprNode))
	case ast.OrExpr:
		return isSimpleOrExpr(n.(*ast.OrExprNode))
	case ast.PathExpression,
		ast.Star,
		ast.BignumericLiteral,
		ast.BooleanLiteral,
		ast.DateOrTimeLiteral,
		ast.FloatLiteral,
		ast.IntLiteral,
		ast.NullLiteral,
		ast.NumericLiteral,
		ast.ParameterExpr,
		ast.SystemVariableExpr,
		ast.Identifier:
		return true
	case ast.BytesLiteral:
		b := n.(*ast.BytesLiteralNode)
		return !strings.Contains(b.Image(), "\n")
	case ast.ExtractExpression:
		b := n.(*ast.ExtractExpressionNode)
		return isSimpleExpr(b.LhsExpr()) && isSimpleExpr(b.RhsExpr())
	case ast.StringLiteral:
		b := n.(*ast.StringLiteralNode)
		return !strings.Contains(b.Image(), "\n")
	case ast.BinaryExpression:
		b := n.(*ast.BinaryExpressionNode)
		return isSimpleExpr2(b.Lhs()) && isSimpleExpr(b.Rhs())
	case ast.UnaryExpression:
		b := n.(*ast.UnaryExpressionNode)
		return isSimpleExpr2(b.Operand())
	case ast.IntervalExpr:
		b := n.(*ast.IntervalExprNode)
		return isSimpleExpr2(b.InternalValue())
	case ast.FunctionCall:
		f := n.(*ast.FunctionCallNode)
		args := f.Arguments()
		elems := countFunctionCallElements(f)

		return len(args) <= 4 && elems <= 1 && allTrue(mapIsSimpleExprs2(args))
	case ast.NamedArgument:
		b := n.(*ast.NamedArgumentNode)
		return isSimpleExpr2(b.Expr())
	default:
		return false
	}
}

// isSimpleExpr2 tries to determine if the expression is ok to be
// rendered in a single line.
func isSimpleExpr2(n ast.ExpressionNode) bool {
	switch n.Kind() {
	case ast.PathExpression,
		ast.Star,
		ast.BignumericLiteral,
		ast.BooleanLiteral,
		ast.BytesLiteral,
		ast.DateOrTimeLiteral,
		ast.FloatLiteral,
		ast.IntLiteral,
		ast.NullLiteral,
		ast.NumericLiteral,
		ast.ParameterExpr,
		ast.StringLiteral,
		ast.SystemVariableExpr,
		ast.Identifier:
		return true
	case ast.BinaryExpression:
		parent := n.Parent()
		if !nodeDefined(parent) || parent.Kind() != ast.BinaryExpression {
			return true
		}

		parent = n.Parent()
		if !nodeDefined(parent) || parent.Kind() != ast.BinaryExpression {
			return true
		}

		return false
	case ast.UnaryExpression:
		e := n.(*ast.UnaryExpressionNode)

		return isSimpleExpr2(e.Operand())
	case ast.FunctionCall:
		f := n.(*ast.FunctionCallNode)
		args := f.Arguments()
		elems := countFunctionCallElements(f)

		return len(args) <= 2 && elems <= 1 && allTrue(mapIsSimpleExprs2(args))
	default:
		return false
	}
}

func isSimpleAndExpr(n *ast.AndExprNode) bool {
	conjuncts := n.Conjuncts()

	parent := n.Parent()
	if nodeDefined(parent) {
		switch parent.Kind() {
		case ast.MergeStatement, ast.MergeWhenClause:
			// When inside a MERGE ... ON, force to be handled as a multi-line
			// AND with aligned equal signs.
			return false
		case ast.WhereClause:
			return false
		}
	}

	return len(conjuncts) <= 4 && allTrue(mapIsSimpleExprs2(conjuncts))
}

func isSimpleOrExpr(n *ast.OrExprNode) bool {
	disjunct := n.Disjuncts()

	parent := n.Parent()
	if nodeDefined(parent) {
		switch parent.Kind() {
		case ast.MergeStatement, ast.MergeWhenClause:
			// When inside a MERGE ... ON, force to be handled as a multi-line
			// OR with aligned equal signs.
			return false
		}
	}

	return len(disjunct) <= 4 && allTrue(mapIsSimpleExprs2(disjunct))
}

func isSimpleColumnSchema(n *ast.ColumnSchemaNode) bool {
	num := n.NumChildren()

	switch n.Kind() {
	case ast.ArrayColumnSchema:
		if num > 1 {
			return false
		}

		return isSimpleColumnSchema2((n.Child(0)))
	case ast.InferredTypeColumnSchema:
		return false
	case ast.SimpleColumnSchema:
		return true
	case ast.StructColumnSchema:
		if num > 1 {
			return false
		}

		return isSimpleColumnSchema2(n.Child(0))
	}

	return false
}

func isSimpleColumnSchema2(n ast.Node) bool {
	num := n.NumChildren()

	switch cs := n.(type) {
	case *ast.ColumnSchemaNode:
		return isSimpleColumnSchema2(cs.Child(0))
	case *ast.ArrayColumnSchemaNode:
		return false
	case *ast.InferredTypeColumnSchemaNode:
		return false
	case *ast.SimpleColumnSchemaNode:
		return isSimpleColumnSchema(cs.ColumnSchemaNode)
	case *ast.StructColumnFieldNode:
		return isSimpleColumnSchema(cs.Schema())
	case *ast.StructColumnSchemaNode:
		if num > 1 {
			return false
		}

		return isSimpleColumnSchema2(cs.Child(0))
	}

	return false
}

// maybeSingleLineColumns inspects a select to determine whether it
// may be rendered as a single line.
func (p *Printer) maybeSingleLineColumns(n *ast.SelectNode) bool {
	cols := n.SelectList().Columns()

	if len(cols) > p.fmt.opts.MaxColumnsForSingleLineSelect {
		return false
	}

	// We need to disable single-line columns if we have a comment
	// inside.
	e := n.ParseLocationRange().End().ByteOffset()
	if lhs, _ := extensions.SplitComments(p.fmt.comments.comments, e); len(lhs) > 0 {
		return false
	}

	r := make([]bool, 0, len(cols))
	functions := 0
	aliases := 0

	for _, c := range cols {
		if !nodeDefined(c.Child(0)) { // Fix to skip bug on JSON literal.
			continue
		}

		e := c.Expression()
		if e.Kind() == ast.FunctionCall {
			functions++
		}

		alias := c.Alias() != nil
		if alias {
			aliases++
		}

		r = append(r, isSimpleExpr2(e))
	}

	return functions <= 1 && aliases <= 1 && allTrue(r)
}

func mapIsAlignable(exprs []ast.ExpressionNode) []bool {
	r := make([]bool, 0, len(exprs))

	for _, e := range exprs {
		simple := false

		switch e.Kind() {
		case ast.BinaryExpression:
			simple = isSimpleExpr(e)
		case ast.UnaryExpression:
			simple = true
		}

		r = append(r, simple)
	}

	return r
}

func mapIsSimpleFunctionParameters(params []*ast.FunctionParameterNode) []bool {
	r := make([]bool, 0, len(params))

	for _, p := range params {
		simple := false

		if typ := p.Type(); typ != nil {
			simple = isSimpleType(typ)
		} else if p.TemplatedParameterType() != nil {
			simple = true
		}
		r = append(r, simple)
	}

	return r
}

func mapIsSimpleTVFSchema(cols []*ast.TVFSchemaColumnNode) []bool {
	r := make([]bool, 0, len(cols))

	for _, c := range cols {
		r = append(r, isSimpleType(c.Type()))
	}

	return r
}

func mapIsSimpleOptionsList(n *ast.OptionsListNode) []bool {
	entries := n.OptionsEntries()
	r := make([]bool, 0, len(entries))

	for _, e := range entries {
		r = append(r, isSimpleExpr(e.Value()))
	}

	return r
}

func mapIsSimplePathExpressionList(n *ast.PathExpressionListNode) []bool {
	num := n.NumChildren()
	r := make([]bool, 0, num)

	for i := 0; i < num; i++ {
		path := n.Child(i)

		if nodeDefined(path) {
			r = append(r, isSimpleExpr(path))
		}
	}

	return r
}

func mapIsSimplePivotExpressionList(n *ast.PivotExpressionListNode) []bool {
	exprs := n.Expressions()
	r := make([]bool, 0, len(exprs))

	for _, a := range exprs {
		r = append(r, isSimpleExpr(a.Expression()) && a.Alias() == nil)
	}

	return r
}

func mapIsSimplePivotForExpression(n *ast.PivotClauseNode) []bool {
	lhs := n.ForExpression()
	vl := mustGetPivotValueList(n)

	lhsSimple := isSimpleExpr(lhs)
	vlSimple := mapIsSimplePivotValueList(vl)

	return append([]bool{lhsSimple}, vlSimple...)
}

func mapIsSimplePivotValueList(n *ast.PivotValueListNode) []bool {
	exprs := n.Values()
	r := make([]bool, 0, len(exprs))

	for _, a := range exprs {
		r = append(r, isSimpleExpr(a.Value()) && a.Alias() == nil)
	}

	return r
}

func mapIsSimpleStructConstructorArg(args []*ast.StructConstructorArgNode) []bool {
	r := make([]bool, 0, len(args))

	// Each struct constructor argument has an expression and an optional
	// alias. We only need to check for the expression.
	for _, a := range args {
		r = append(r, isSimpleExpr(a.Expression()))
	}

	return r
}

func mapIsSimpleStructFields(fields []*ast.StructFieldNode) []bool {
	r := make([]bool, 0, len(fields))

	for _, f := range fields {
		r = append(r, isSimpleType(f.Type()))
	}

	return r
}

func mapIsSimpleTVFArguments(args []*ast.TVFArgumentNode) []bool {
	r := make([]bool, 0, len(args))

	for _, a := range args {
		r = append(r, isSimpleTVFArgument(a))
	}

	return r
}

func mapIsSimpleUnpivotInItemList(n *ast.UnpivotInItemListNode) []bool {
	items := n.InItems()
	num := len(items)
	r := make([]bool, 0, num)

	for _, item := range items {
		simple := allTrue(mapIsSimplePathExpressionList(item.UnpivotColumns()))

		r = append(r, simple && item.Alias() == nil)
	}

	return r
}

func isSimpleTVFArgument(n *ast.TVFArgumentNode) bool {
	if expr := n.Expr(); expr != nil && !isSimpleExpr(expr) {
		return false
	}

	if n.TableClause() != nil {
		return false
	}

	if n.ModelClause() != nil {
		return false
	}

	if n.ConnectionClause() != nil {
		return false
	}

	if n.Descriptor() != nil {
		return false
	}

	return true
}

func onlySimpleFunctionCallArgs(n *ast.FunctionCallNode) bool {
	return allTrue(mapIsSimpleExprs(n.Arguments()))
}

func mapIsSimpleExprs(n []ast.ExpressionNode) []bool {
	r := make([]bool, 0, len(n))

	for _, e := range n {
		r = append(r, isSimpleExpr(e))
	}

	return r
}

func mapIsSimpleExprs2(n []ast.ExpressionNode) []bool {
	r := make([]bool, 0, len(n))

	for _, e := range n {
		r = append(r, isSimpleExpr2(e))
	}

	return r
}

// caseArgsGetIsSimple extract whether each argument is considered simple.
func caseArgsGetIsSimple[T ast.ExpressionNode](args []T) []bool {
	r := make([]bool, 0, len(args))

	for _, a := range args {
		r = append(r, isSimpleExpr(a))
	}

	return r
}

func countFunctionCallElements(n *ast.FunctionCallNode) int {
	elems := 0

	// We don't count DISTINCT as an element because we want to allow
	// COUNT(DISTINCT x ORDER BY y) in a single line.

	if n.NullHandlingModifier() != ast.DefaultNullHandling {
		elems++
	}

	if n.HavingModifier() != nil {
		elems++
	}

	if n.ClampedBetweenModifier() != nil {
		elems++
	}

	if n.OrderBy() != nil {
		elems++
	}

	if n.LimitOffset() != nil {
		elems++
	}

	if n.WithGroupRows() != nil {
		elems++
	}

	return elems
}

func countWindowSpecElems(n *ast.WindowSpecificationNode) int {
	elems := 0

	if n.BaseWindowName() != nil {
		elems++
	}

	if n.PartitionBy() != nil {
		elems++
	}

	if n.OrderBy() != nil {
		elems++
	}

	if n.WindowFrame() != nil {
		elems++
	}

	return elems
}
