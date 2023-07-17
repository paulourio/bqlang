// Functions to map on which lines a node spans.
package formatter

import (
	"sort"
	"strings"

	"github.com/goccy/go-zetasql/ast"
)

type LineTracker struct {
	StartPos []int
}

func NewLineTracker(input string) *LineTracker {
	t := &LineTracker{}
	t.initialize(input)

	return t
}

// Span returns the range of lines a node spans.
func (t *LineTracker) Span(n ast.Node) (start int, end int) {
	p := n.ParseLocationRange()
	start = t.LineOf(p.Start().ByteOffset())
	end = t.LineOf(p.End().ByteOffset())

	return
}

// SpanPos returns the line a specific byte offset is located in.
func (t *LineTracker) LineOf(b int) int {
	return sort.Search(len(t.StartPos), func(i int) bool {
		return t.StartPos[i] >= b
	})
}

// NextLineBreak returns the byte offset of the next line break or
// -1 if not found.
func (t *LineTracker) NextLineBreak(offset int) int {
	i := t.LineOf(offset)

	if i < len(t.StartPos) {
		return t.StartPos[i]
	}

	return -1
}

func (t *LineTracker) initialize(s string) {
	n := strings.Count(s, "\n")
	t.StartPos = make([]int, n)
	offset := 0

	for i := 0; i < n; i++ {
		pos := strings.Index(s, "\n")
		t.StartPos[i] = offset + pos
		s = s[pos+1:]
		offset += pos + 1
	}
}
