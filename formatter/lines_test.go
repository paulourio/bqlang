package formatter_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/paulourio/bqlang/formatter"
)

func TestLineLoc(t *testing.T) {
	t.Parallel()

	lineLocCases := []*lineLocCase{
		{
			Input:    "",
			StartPos: []int{},
			Queries: []*lineSpanQuery{
				{0, 0, -1},
				{10, 0, -1},
			},
		},
		{
			Input:    "\n",
			StartPos: []int{0},
			Queries: []*lineSpanQuery{
				{0, 0, 0},
				{1, 1, -1},
				{2, 1, -1},
			},
		},
		{
			Input:    "abc\n\ndef\nbar\n",
			StartPos: []int{3, 4, 8, 12},
			Queries: []*lineSpanQuery{
				{-1, 0, 3},
				{0, 0, 3},
				{1, 0, 3},
				{3, 0, 3},
				{4, 1, 4},
				{5, 2, 8},
				{7, 2, 8},
				{9, 3, 12},
				{12, 3, 12},
				{13, 4, -1},
				{19, 4, -1},
			},
		},
	}

	for i, c := range lineLocCases {
		t.Run(fmt.Sprintf("Case %d", i+1), func(t *testing.T) {
			t.Parallel()

			lt := formatter.NewLineTracker(c.Input)
			cstr := fmt.Sprintf("Input: %#v\nStartPos: %#v\n", c.Input, c.StartPos)

			if assert.Equal(t, c.StartPos, lt.StartPos, cstr) {
				for j, q := range c.Queries {
					msg := fmt.Sprintf("%sTracker: %#v\nQuery %d: %#v", cstr, lt, j+1, q)
					assert.Equal(t, q.line, lt.LineOf(q.byteOffset), msg)
					assert.Equal(t, q.nextLineBreak, lt.NextLineBreak(q.byteOffset), msg)
				}
			}
		})
	}
}

type lineLocCase struct {
	Input    string
	StartPos []int
	Queries  []*lineSpanQuery
}

type lineSpanQuery struct {
	byteOffset    int
	line          int
	nextLineBreak int
}
