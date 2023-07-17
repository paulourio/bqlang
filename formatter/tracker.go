// This file contains code to keep track of node positions.
package formatter

import (
	"sort"

	"github.com/goccy/go-zetasql/ast"
)

// LocationTracker track an ordered sequence of nodes.
type LocationTracker struct {
	// Pos is an ordered slice of unique positions of start positions.
	Pos []int
	// Lines tracks the byte offsets of each line begin.
	Lines *LineTracker
}

// NewStartLocationTracker returns a location tracker for an input and
// the respective parsed AST nodes.
func NewStartLocationTracker(s string, root ast.Node) *LocationTracker {
	t := &LocationTracker{}
	t.initNodePos(root)
	t.initLines(s)

	return t
}

func (t *LocationTracker) initNodePos(root ast.Node) {
	n := int(float64(countNodes(root)) * .6)
	set := make(map[int]bool, n)

	ast.Walk(root, func(n ast.Node) error {
		if !nodeDefined(n) {
			return nil
		}

		r := n.ParseLocationRange()
		set[r.Start().ByteOffset()] = true

		return nil
	})

	t.Pos = make([]int, 0, len(set))

	for p := range set {
		t.Pos = append(t.Pos, p)
	}

	sort.Ints(t.Pos)
}

func (t *LocationTracker) initLines(s string) {
	t.Lines = NewLineTracker(s)
}

// NextPos returns the next position in the slice.  If not available,
// returns itself.
func (t *LocationTracker) NextPos(pos int) int {
	j := t.MaybeNextPos(pos)
	if j < 0 {
		return pos
	}

	return j
}

// MaybeNextPos returns the start position of the next node.  If not
// available, returns -1.
func (t *LocationTracker) MaybeNextPos(pos int) int {
	j := sort.Search(len(t.Pos), func(i int) bool { return t.Pos[i] > pos })
	if j == len(t.Pos) {
		return -1
	}

	return t.Pos[j]
}

func countNodes(root ast.Node) (count int) {
	ast.Walk(root, func(n ast.Node) error {
		count++

		return nil
	})

	return
}
