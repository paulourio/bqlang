package ast

import (
	"fmt"
	"strings"
)

type dumper struct {
	sep      string
	maxDepth int
	out      strings.Builder
}

func newDumper(sep string, maxDepth int) *dumper {
	return &dumper{sep: sep, maxDepth: maxDepth}
}

func (d *dumper) dump(n NodeHandler, depth int) {
	d.indent(depth)
	fmt.Fprintf(&d.out, "%s [%d-%d]%s", n.SingleNodeDebugString(), n.StartLoc(), n.EndLoc(), d.sep)

	if depth >= d.maxDepth {
		d.indent(depth)
		fmt.Fprintf(&d.out, "  Subtree skipped (reached max depth %d)", d.maxDepth)

		return
	}

	for _, c := range n.Children() {
		d.dump(c, depth+1)
	}
}

func (d *dumper) String() string {
	return d.out.String()
}

func (d *dumper) indent(depth int) {
	d.out.WriteString(strings.Repeat(" ", 2*depth))
}
