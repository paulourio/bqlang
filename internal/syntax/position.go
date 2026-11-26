package syntax

import (
	"sort"
	"unicode/utf8"
)

// Position represents a 0-based line and UTF-16 code unit offset, matching LSP 3.17.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range represents a half-open UTF-16 span [Start, End) in a document.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// TextChange represents an LSP TextDocumentContentChangeEvent.
// If Range is nil, Text replaces the entire document.
type TextChange struct {
	Range *Range `json:"range,omitempty"`
	Text  string `json:"text"`
}

// ByteSpan represents a half-open byte range [Start, End) in a string.
type ByteSpan struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type lineInfo struct {
	start    int
	end      int
	nonASCII bool
}

// LineIndex maps between UTF-8 byte offsets and LSP 0-based UTF-16 Positions.
type LineIndex struct {
	src   string
	lines []lineInfo
}

// NewLineIndex builds a line and UTF-16 offset index for src, treating
// "\r\n", "\n", and "\r" as line terminators.
func NewLineIndex(src string) *LineIndex {
	lines := make([]lineInfo, 0, 16)
	lineStart := 0
	nonASCII := false

	for i := 0; i < len(src); {
		b := src[i]
		if b == '\r' || b == '\n' {
			lines = append(lines, lineInfo{
				start:    lineStart,
				end:      i,
				nonASCII: nonASCII,
			})
			if b == '\r' && i+1 < len(src) && src[i+1] == '\n' {
				i += 2
			} else {
				i++
			}
			lineStart = i
			nonASCII = false
			continue
		}
		if b >= 0x80 {
			nonASCII = true
		}
		i++
	}

	lines = append(lines, lineInfo{
		start:    lineStart,
		end:      len(src),
		nonASCII: nonASCII,
	})

	return &LineIndex{
		src:   src,
		lines: lines,
	}
}

// LineCount returns the number of lines in the indexed document (at least 1).
func (li *LineIndex) LineCount() int {
	if li == nil {
		return 1
	}

	return len(li.lines)
}

// ByteLen returns the total byte length of the indexed document.
func (li *LineIndex) ByteLen() int {
	if li == nil {
		return 0
	}

	return len(li.src)
}

// LineStart returns the starting byte offset of the 0-based line, clamped to [0, len(src)].
func (li *LineIndex) LineStart(line int) int {
	if li == nil || line < 0 {
		return 0
	}
	if line >= len(li.lines) {
		return len(li.src)
	}

	return li.lines[line].start
}

// LineEnd returns the exclusive content end byte offset (before any line terminator)
// of the 0-based line, clamped to [0, len(src)].
func (li *LineIndex) LineEnd(line int) int {
	if li == nil || line < 0 {
		return 0
	}
	if line >= len(li.lines) {
		return len(li.src)
	}

	return li.lines[line].end
}

// PositionAt converts a 0-based byte offset into an LSP UTF-16 Position.
// Offsets out of range are clamped; offsets inside a multi-byte UTF-8 rune
// or CRLF terminator snap cleanly to a valid boundary.
func (li *LineIndex) PositionAt(offset int) Position {
	if li == nil || offset <= 0 {
		return Position{}
	}
	if offset > len(li.src) {
		offset = len(li.src)
	}

	lineIdx := max(sort.Search(len(li.lines), func(i int) bool {
		return li.lines[i].start > offset
	})-1, 0)

	info := li.lines[lineIdx]
	limit := min(offset, info.end)
	if !info.nonASCII {
		return Position{
			Line:      lineIdx,
			Character: limit - info.start,
		}
	}

	char := utf16ColumnsUpTo(li.src[info.start:info.end], limit-info.start)

	return Position{
		Line:      lineIdx,
		Character: char,
	}
}

// ByteOffset converts an LSP UTF-16 Position to a clamped 0-based byte offset
// that never splits a valid multi-byte UTF-8 rune or CRLF sequence.
func (li *LineIndex) ByteOffset(pos Position) int {
	if li == nil || pos.Line < 0 {
		return 0
	}
	if pos.Line >= len(li.lines) {
		return len(li.src)
	}

	info := li.lines[pos.Line]
	if pos.Character <= 0 {
		return info.start
	}
	if !info.nonASCII {
		if pos.Character >= info.end-info.start {
			return info.end
		}

		return info.start + pos.Character
	}

	return info.start + byteAtUTF16Col(li.src[info.start:info.end], pos.Character)
}

// RangeAt converts a half-open byte span [start, end) into an LSP UTF-16 Range.
func (li *LineIndex) RangeAt(start, end int) Range {
	end = max(end, start)

	return Range{
		Start: li.PositionAt(start),
		End:   li.PositionAt(end),
	}
}

// ByteRange converts an LSP UTF-16 Range into a clamped half-open byte span [start, end).
func (li *LineIndex) ByteRange(r Range) (start, end int) {
	start = li.ByteOffset(r.Start)
	end = max(li.ByteOffset(r.End), start)

	return start, end
}

// ByteOffsetFromGoogleSQL converts a 1-based GoogleSQL (line, col) error location
// (using 8-column tab stops and 1 column per UTF-8 code point) into a 0-based byte offset.
func (li *LineIndex) ByteOffsetFromGoogleSQL(line, col int) int {
	if li == nil || line <= 0 {
		return 0
	}
	if line > len(li.lines) {
		return len(li.src)
	}

	info := li.lines[line-1]
	if col <= 1 {
		return info.start
	}

	return info.start + byteAtGoogleSQLCol(li.src[info.start:info.end], col)
}

// Apply applies incremental or full-document TextChanges sequentially to the indexed
// document, returning the updated source text and a refreshed LineIndex.
func (li *LineIndex) Apply(changes []TextChange) (string, *LineIndex) {
	if len(changes) == 0 {
		if li != nil {
			return li.src, li
		}

		return "", NewLineIndex("")
	}

	cur := ""
	curIdx := li
	if curIdx != nil {
		cur = curIdx.src
	}

	for _, ch := range changes {
		if ch.Range == nil {
			cur = ch.Text
			curIdx = nil
			continue
		}
		if curIdx == nil {
			curIdx = NewLineIndex(cur)
		}
		start, end := curIdx.ByteRange(*ch.Range)
		cur = cur[:start] + ch.Text + cur[end:]
		curIdx = nil
	}

	if curIdx == nil {
		curIdx = NewLineIndex(cur)
	}

	return cur, curIdx
}

// ApplyChanges applies incremental or full-document TextChanges sequentially to src
// using li when it matches src, returning the updated source text and a new LineIndex.
func (li *LineIndex) ApplyChanges(src string, changes []TextChange) (string, *LineIndex) {
	base := li
	if base == nil || base.src != src {
		base = NewLineIndex(src)
	}

	return base.Apply(changes)
}

// ApplyChanges applies incremental or full-document TextChanges sequentially to src
// and returns the updated source text along with its new LineIndex.
func ApplyChanges(src string, changes []TextChange) (string, *LineIndex) {
	return NewLineIndex(src).Apply(changes)
}

func utf16ColumnsUpTo(line string, byteLimit int) int {
	u16 := 0
	for i := 0; i < len(line); {
		r, size := utf8.DecodeRuneInString(line[i:])
		if i+size > byteLimit {
			break
		}
		if r > 0xFFFF {
			u16 += 2
		} else {
			u16++
		}
		i += size
	}

	return u16
}

func byteAtUTF16Col(line string, targetCol int) int {
	i := 0
	u16 := 0
	for i < len(line) && u16 < targetCol {
		r, size := utf8.DecodeRuneInString(line[i:])
		units := 1
		if r > 0xFFFF {
			units = 2
		}
		if u16+units > targetCol {
			break
		}
		u16 += units
		i += size
	}

	return i
}

func byteAtGoogleSQLCol(line string, targetCol int) int {
	i := 0
	currCol := 1
	for i < len(line) && currCol < targetCol {
		if line[i] == '\t' {
			nextCol := ((currCol-1)/8+1)*8 + 1
			if nextCol > targetCol {
				break
			}
			currCol = nextCol
			i++
			continue
		}
		_, size := utf8.DecodeRuneInString(line[i:])
		currCol++
		i += size
	}

	return i
}
