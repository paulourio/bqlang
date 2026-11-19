package formatter

import (
	"sort"
	"strings"

	"github.com/goccy/go-googlesql"
)

// LineTracker indexes newline byte offsets to map byte positions to 0-based line numbers.
type LineTracker struct {
	StartPos []int
}

// NewLineTracker builds a LineTracker for input.
func NewLineTracker(input string) *LineTracker {
	t := &LineTracker{}
	t.initialize(input)

	return t
}

// Span returns the range of lines a node spans.
func (t *LineTracker) Span(n googlesql.ASTNode) (start, end int) {
	b, e := nodeByteRange(n)

	return t.LineOf(b), t.LineOf(e)
}

// LineOf returns the 0-based line index a specific byte offset is located in.
func (t *LineTracker) LineOf(b int) int {
	return sort.Search(len(t.StartPos), func(i int) bool {
		return t.StartPos[i] >= b
	})
}

// NextLineBreak returns the byte offset of the next line break or -1 if none.
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

	for i := range n {
		pos := strings.IndexByte(s, '\n')
		t.StartPos[i] = offset + pos
		s = s[pos+1:]
		offset += pos + 1
	}
}
