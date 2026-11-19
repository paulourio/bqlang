// Package formatter formats GoogleSQL and BigQuery scripts, including comments
// and Jinja2 templates.
package formatter

import (
	"math"
	"reflect"

	"github.com/goccy/go-googlesql"
)

// nodeDefined returns whether an ASTNode interface is non-nil and wraps a
// non-nil concrete pointer.
func nodeDefined(n googlesql.ASTNode) bool {
	if n == nil {
		return false
	}

	v := reflect.ValueOf(n)

	return v.Kind() != reflect.Pointer || !v.IsNil()
}

func nodeKind(n googlesql.ASTNode) googlesql.ASTNodeKind {
	if !nodeDefined(n) {
		return googlesql.ASTNodeKindKUnknownAstNodeKind
	}

	k, err := n.NodeKind()
	if err != nil {
		return googlesql.ASTNodeKindKUnknownAstNodeKind
	}

	return k
}

func nodeParent(n googlesql.ASTNode) googlesql.ASTNode {
	if !nodeDefined(n) {
		return nil
	}

	parent, err := n.Parent()
	if err != nil || !nodeDefined(parent) {
		return nil
	}

	return parent
}

func nodeByteRange(n googlesql.ASTNode) (start, end int) {
	if !nodeDefined(n) {
		return 0, 0
	}

	loc, err := n.Location()
	if err != nil || loc == nil {
		return 0, 0
	}

	sp, err := loc.Start()
	if err == nil && sp != nil {
		b, berr := sp.GetByteOffset()
		if berr == nil {
			start = int(b)
		}
	}

	ep, err := loc.End()
	if err == nil && ep != nil {
		b, berr := ep.GetByteOffset()
		if berr == nil {
			end = int(b)
		}
	}

	return start, end
}

func nodeNumChildren(n googlesql.ASTNode) int {
	if !nodeDefined(n) {
		return 0
	}

	num, err := n.NumChildren()
	if err != nil || num < 0 {
		return 0
	}

	return int(num)
}

func nodeChild(n googlesql.ASTNode, idx int) googlesql.ASTNode {
	if !nodeDefined(n) || idx < 0 || idx > math.MaxInt32 {
		return nil
	}

	c, err := n.Child(int32(idx))
	if err != nil || !nodeDefined(c) {
		return nil
	}

	return c
}

// selectChildrenOfType extracts all direct children of n matching type T.
func selectChildrenOfType[T googlesql.ASTNode](n googlesql.ASTNode) []T {
	num := nodeNumChildren(n)
	if num == 0 {
		return nil
	}

	r := make([]T, 0, num)

	for i := range num {
		c := nodeChild(n, i)
		if !nodeDefined(c) {
			continue
		}

		if ct, ok := c.(T); ok {
			r = append(r, ct)
		}
	}

	return r
}

// functionCallArgs extracts expression arguments of an ASTFunctionCall,
// skipping child 0 (which is the function name ASTPathExpression).
func functionCallArgs(n *googlesql.ASTFunctionCall) []googlesql.ASTExpressionNode {
	num := nodeNumChildren(n)
	if num <= 1 {
		return nil
	}

	args := make([]googlesql.ASTExpressionNode, 0, num-1)

	for i := 1; i < num; i++ {
		c := nodeChild(n, i)
		if !nodeDefined(c) {
			continue
		}

		isExpr, err := c.IsExpression()
		if err != nil || !isExpr {
			continue
		}

		if expr, ok := c.(googlesql.ASTExpressionNode); ok {
			args = append(args, expr)
		}
	}

	return args
}

// locationRange returns the minimum and maximum parse location range
// that covers all defined nodes in the arguments. If no defined nodes are
// passed, [0, 0) is returned.
func locationRange(nodes ...googlesql.ASTNode) (start, end int) {
	initialized := false

	for _, n := range nodes {
		if !nodeDefined(n) {
			continue
		}

		s, e := nodeByteRange(n)

		if !initialized || s < start {
			start = s
		}

		if !initialized || e > end {
			end = e
		}

		initialized = true
	}

	return start, end
}

func walkAST(n googlesql.ASTNode, cb func(googlesql.ASTNode) error) error {
	if !nodeDefined(n) {
		return nil
	}

	if err := cb(n); err != nil {
		return err
	}

	num := nodeNumChildren(n)
	for i := range num {
		if err := walkAST(nodeChild(n, i), cb); err != nil {
			return err
		}
	}

	return nil
}
