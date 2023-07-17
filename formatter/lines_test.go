package formatter_test

import (
	"fmt"
	"testing"

	"github.com/paulourio/bqlang/formatter"
	"github.com/stretchr/testify/assert"
)

func TestLineLoc(t *testing.T) {
	for i, c := range lineLocCases {
		t.Run(fmt.Sprintf("Case %d", i+1), func(t *testing.T) {
			lt := formatter.NewLineTracker(c.Input)

			cstr := fmt.Sprintf("Input: %#v\nStartPos: %#v\n", c.Input, c.StartPos)
			if assert.Equal(t, c.StartPos, lt.StartPos, cstr) {
				for j, q := range c.Queries {
					msg := fmt.Sprintf("%sTracker: %#v\nQuery %d: %#v",
						cstr, lt, j+1, q)
					assert.Equal(t, q.line, lt.LineOf(q.byteOffset), msg)
					assert.Equal(t, q.nextLineBreak, lt.NextLineBreak(q.byteOffset), msg)
				}
			}
		})
	}
}

var lineLocCases = []*lineLocCase{
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
			{-1, 0, 3},  // out of bounds
			{0, 0, 3},   // "|abc^^def^bar^"
			{1, 0, 3},   // "a|bc^^def^bar^"
			{3, 0, 3},   // "abc|^^def^bar^"
			{4, 1, 4},   // "abc^|^def^bar^"
			{5, 2, 8},   // "abc^^|def^bar^"
			{7, 2, 8},   // "abc^^de|f^bar^"
			{9, 3, 12},  // "abc^^def^|bar^"
			{12, 3, 12}, // "abc^^def^bar|^"
			{13, 4, -1}, // "abc^^def^bar^|"
			{19, 4, -1}, // out of bounds
		},
	},
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
