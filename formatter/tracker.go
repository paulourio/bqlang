package formatter

import (
	"sort"

	"github.com/goccy/go-googlesql"
)

// locationTracker tracks an ordered sequence of AST node start byte offsets.
type locationTracker struct {
	// Pos is an ordered slice of unique node start byte positions.
	Pos []int
	// Lines tracks the byte offsets of each line break.
	Lines *LineTracker
}

// newStartLocationTracker returns a location tracker for an input and its AST.
func newStartLocationTracker(s string, root googlesql.ASTNode) *locationTracker {
	t := &locationTracker{}
	t.initNodePos(root)
	t.initLines(s)

	return t
}

func (t *locationTracker) initNodePos(root googlesql.ASTNode) {
	n := int(float64(countNodes(root)) * 0.6)
	set := make(map[int]bool, n)

	_ = walkAST(root, func(node googlesql.ASTNode) error {
		if !nodeDefined(node) {
			return nil
		}

		// Skip synthetic ASTLocation helper nodes so comment flushing aligns
		// with semantic AST nodes identically to the classic grammar.
		if nodeKind(node) == googlesql.ASTNodeKindAstLocation {
			return nil
		}

		start, _ := nodeByteRange(node)
		set[start] = true

		return nil
	})

	t.Pos = make([]int, 0, len(set))

	for p := range set {
		t.Pos = append(t.Pos, p)
	}

	sort.Ints(t.Pos)
}

func (t *locationTracker) initLines(s string) {
	t.Lines = NewLineTracker(s)
}

// NextPos returns the next start position after pos, or pos itself if none.
func (t *locationTracker) NextPos(pos int) int {
	j := t.MaybeNextPos(pos)
	if j < 0 {
		return pos
	}

	return j
}

// MaybeNextPos returns the start position of the next node after pos, or -1.
func (t *locationTracker) MaybeNextPos(pos int) int {
	j := sort.Search(len(t.Pos), func(i int) bool { return t.Pos[i] > pos })
	if j == len(t.Pos) {
		return -1
	}

	return t.Pos[j]
}

func countNodes(root googlesql.ASTNode) int {
	count := 0

	_ = walkAST(root, func(_ googlesql.ASTNode) error {
		count++

		return nil
	})

	return count
}
