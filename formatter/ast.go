// This file has as set of functions to help navigating on ZetaSQL's AST.
package formatter

import (
	"reflect"

	"github.com/goccy/go-zetasql/ast"
)

// selectChildrenOfType filters children nodes from a node according to a function.
// The argument cap may be used to set an initial expectation for the
// size of the resulting slice.
func selectChildrenOfType[T ast.Node](n ast.Node, cap int) []T {
	r := make([]T, 0, cap)
	num := n.NumChildren()

	for i := 0; i < num; i++ {
		c := n.Child(i)

		if ct, ok := c.(T); ok {
			r = append(r, ct)
		}
	}

	return r
}

func mustGetPivotExpressionList(n *ast.PivotClauseNode) *ast.PivotExpressionListNode {
	el, ok := n.Child(0).(*ast.PivotExpressionListNode)
	if !ok {
		panic(&PrinterError{
			Msg:  "invalid pivot clause structure",
			Node: n,
		})
	}

	return el
}

func mustGetPivotValueList(n *ast.PivotClauseNode) *ast.PivotValueListNode {
	pv, ok := n.Child(2).(*ast.PivotValueListNode)
	if !ok {
		panic(&PrinterError{
			Msg:  "invalid pivot clause structure",
			Node: n,
		})
	}

	return pv
}

// locationRange returns the minimum and maximum parse location range
// that covers all nodes in the arguments.  If no nodes are passed,
// the range [0, 0) is returned.
func locationRange(nodes ...ast.Node) (start int, end int) {
	for i, n := range nodes {
		if !nodeDefined(n) {
			continue
		}

		s := n.ParseLocationRange().Start().ByteOffset()
		e := n.ParseLocationRange().End().ByteOffset()

		if i == 0 || s < start {
			start = s
		}

		if i == 0 || e > end {
			end = e
		}
	}

	return
}

// nodeDefined returns whether a node is not nil.
func nodeDefined(n ast.Node) bool {
	return n != nil && !reflect.ValueOf(n).IsNil()
}
