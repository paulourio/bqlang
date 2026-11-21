package main

import (
	"fmt"
	"strconv"
	"strings"
)

type diffOp byte

const (
	diffEqual  diffOp = ' '
	diffDelete diffOp = '-'
	diffInsert diffOp = '+'
)

type diffLine struct {
	op   diffOp
	text string
}

type diffHunk struct {
	oldStart int
	oldCount int
	newStart int
	newCount int
	lines    []diffLine
}

const (
	diffContextLines = 3
	maxLCSCells      = 4_000_000
)

// unifiedDiff computes a unified diff between oldText and newText for filename.
// It returns an empty string when oldText and newText are identical.
func unifiedDiff(filename, oldText, newText string) string {
	if oldText == newText {
		return ""
	}

	oldLines, oldNoNL := splitLines(oldText)
	newLines, newNoNL := splitLines(newText)

	edits := computeLineEdits(oldLines, newLines)
	hunks := buildHunks(edits, diffContextLines)

	var b strings.Builder
	fmt.Fprintf(&b, "--- %s.orig\n+++ %s\n", filename, filename)

	for _, h := range hunks {
		writeHunk(&b, h, len(oldLines), oldNoNL, len(newLines), newNoNL)
	}

	return b.String()
}

func writeHunk(
	b *strings.Builder,
	h diffHunk,
	oldLen int,
	oldNoNL bool,
	newLen int,
	newNoNL bool,
) {
	fmt.Fprintf(b, "@@ -%s +%s @@\n", formatRange(h.oldStart, h.oldCount), formatRange(h.newStart, h.newCount))

	oldIdx := h.oldStart - 1
	newIdx := h.newStart - 1

	for _, l := range h.lines {
		b.WriteByte(byte(l.op))
		b.WriteString(l.text)
		b.WriteByte('\n')

		switch l.op {
		case diffEqual:
			oldIdx++
			newIdx++

			if oldIdx == oldLen && oldNoNL && newIdx == newLen && newNoNL {
				b.WriteString("\\ No newline at end of file\n")
			}
		case diffDelete:
			oldIdx++
			if oldIdx == oldLen && oldNoNL {
				b.WriteString("\\ No newline at end of file\n")
			}
		case diffInsert:
			newIdx++
			if newIdx == newLen && newNoNL {
				b.WriteString("\\ No newline at end of file\n")
			}
		}
	}
}

func splitLines(s string) ([]string, bool) {
	if s == "" {
		return nil, false
	}

	noTrailingNL := !strings.HasSuffix(s, "\n")
	trimmed := strings.TrimSuffix(s, "\n")

	return strings.Split(trimmed, "\n"), noTrailingNL
}

func computeLineEdits(oldLines, newLines []string) []diffLine {
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}

	oldEnd, newEnd := len(oldLines), len(newLines)
	for oldEnd > prefix && newEnd > prefix && oldLines[oldEnd-1] == newLines[newEnd-1] {
		oldEnd--
		newEnd--
	}

	edits := make([]diffLine, 0, len(oldLines)+len(newLines))
	for i := range prefix {
		edits = append(edits, diffLine{op: diffEqual, text: oldLines[i]})
	}

	edits = append(edits, diffMiddle(oldLines[prefix:oldEnd], newLines[prefix:newEnd])...)

	for i := oldEnd; i < len(oldLines); i++ {
		edits = append(edits, diffLine{op: diffEqual, text: oldLines[i]})
	}

	return edits
}

func diffMiddle(a, b []string) []diffLine {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil
	}

	if n*m > maxLCSCells {
		return fallbackEdits(a, b)
	}

	// Build bottom-up LCS length table.
	dp := make([]int, (n+1)*(m+1))
	stride := m + 1

	for i := n - 1; i >= 0; i-- {
		row := i * stride
		nextRow := (i + 1) * stride

		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[row+j] = dp[nextRow+j+1] + 1
			} else {
				dp[row+j] = max(dp[nextRow+j], dp[row+j+1])
			}
		}
	}

	return backtrackLCS(a, b, dp, stride)
}

func fallbackEdits(a, b []string) []diffLine {
	edits := make([]diffLine, 0, len(a)+len(b))
	for _, line := range a {
		edits = append(edits, diffLine{op: diffDelete, text: line})
	}

	for _, line := range b {
		edits = append(edits, diffLine{op: diffInsert, text: line})
	}

	return edits
}

func backtrackLCS(a, b []string, dp []int, stride int) []diffLine {
	edits := make([]diffLine, 0, len(a)+len(b))
	i, j := 0, 0

	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			edits = append(edits, diffLine{op: diffEqual, text: a[i]})
			i++
			j++
		case dp[(i+1)*stride+j] >= dp[i*stride+j+1]:
			edits = append(edits, diffLine{op: diffDelete, text: a[i]})
			i++
		default:
			edits = append(edits, diffLine{op: diffInsert, text: b[j]})
			j++
		}
	}

	for ; i < len(a); i++ {
		edits = append(edits, diffLine{op: diffDelete, text: a[i]})
	}

	for ; j < len(b); j++ {
		edits = append(edits, diffLine{op: diffInsert, text: b[j]})
	}

	return edits
}

func buildHunks(edits []diffLine, ctx int) []diffHunk {
	type span struct{ start, end int }

	var spans []span

	for i, e := range edits {
		if e.op == diffEqual {
			continue
		}

		s := max(0, i-ctx)
		end := min(len(edits), i+ctx+1)

		if len(spans) > 0 && s <= spans[len(spans)-1].end {
			spans[len(spans)-1].end = end
		} else {
			spans = append(spans, span{start: s, end: end})
		}
	}

	// Precompute 1-based line numbers at each edit index.
	oldPos := make([]int, len(edits)+1)
	newPos := make([]int, len(edits)+1)
	o, n := 1, 1

	for i, e := range edits {
		oldPos[i] = o
		newPos[i] = n

		if e.op != diffInsert {
			o++
		}

		if e.op != diffDelete {
			n++
		}
	}

	oldPos[len(edits)] = o
	newPos[len(edits)] = n

	hunks := make([]diffHunk, 0, len(spans))
	for _, sp := range spans {
		oldCount := oldPos[sp.end] - oldPos[sp.start]
		newCount := newPos[sp.end] - newPos[sp.start]
		oldStart := oldPos[sp.start]
		newStart := newPos[sp.start]

		if oldCount == 0 {
			oldStart--
		}

		if newCount == 0 {
			newStart--
		}

		hunks = append(hunks, diffHunk{
			oldStart: oldStart,
			oldCount: oldCount,
			newStart: newStart,
			newCount: newCount,
			lines:    edits[sp.start:sp.end],
		})
	}

	return hunks
}

func formatRange(start, count int) string {
	if count == 1 {
		return strconv.Itoa(start)
	}

	return fmt.Sprintf("%d,%d", start, count)
}
