package ast

import (
	"fmt"
	"strings"
)

type dumper struct {
	node         NodeHandler
	sep          string
	maxDepth     int
	currentDepth int
	out          strings.Builder
}

func newDumper(n NodeHandler, sep string, maxDepth int) *dumper {
	return &dumper{node: n, sep: sep, maxDepth: maxDepth}
}

func (d *dumper) Dump() {
	if !d.dumpNode() {
		return
	}

	prev := d.node
	d.currentDepth++

	for _, c := range prev.Children() {
		d.node = c
		d.Dump()
	}

	d.node = prev
	d.currentDepth--
}

func (d *dumper) String() string {
	return d.out.String()
}

func (d *dumper) dumpNode() bool {
	d.indent()
	d.out.WriteString(d.node.SingleNodeDebugString())
	fmt.Fprintf(&d.out, " [%s]", locationRangeAsString(d.node))
	d.out.WriteString(d.sep)

	if d.currentDepth >= d.maxDepth {
		d.indent()
		fmt.Fprintf(&d.out, "  Subtree skipped (reached max depth %d)", d.maxDepth)

		return false
	}

	return true
}

func (d *dumper) indent() {
	d.out.WriteString(strings.Repeat(" ", 2*d.currentDepth))
}

func locationRangeAsString(n NodeHandler) string {
	return fmt.Sprintf("%d-%d", n.StartLoc(), n.EndLoc())
}
