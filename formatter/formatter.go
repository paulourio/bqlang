package formatter

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/paulourio/bqlang/extensions"
)

type formatterState struct {
	opts                   *PrintOptions
	comments               *commentsQueue
	bqCatalog              stringMapSet
	buffer                 strings.Builder
	formatted              strings.Builder
	maxLength              int
	depth                  int
	last                   rune
	lastWasSingleCharUnary bool
	// noFlushInNextFormat disables line flushing in the next call to Format().
	noFlushInNextFormat bool
}

// Format formats token s into the line buffer, inserting spaces between tokens
// and flushing lines when soft column limits are reached at separator points.
func (p *formatterState) Format(s string) {
	if len(s) == 0 {
		return
	}

	defer func() {
		noFlush := p.noFlushInNextFormat
		p.lastWasSingleCharUnary = false
		p.noFlushInNextFormat = false

		if p.currentLineLen() >= p.maxLength && p.lastIsSeparator() && !noFlush {
			p.FlushLine()
		}
	}()

	if p.buffer.Len() > 0 && p.last != '\n' && p.currentLineHasLineComment() {
		p.FlushLine()
	}

	if p.buffer.Len() == 0 {
		p.writeIndent()
		p.writeToken(s)

		return
	}

	switch p.last {
	case '\n':
		p.buffer.WriteByte('\n')
		p.writeToken(s)
	case '(', '[', '@', '.', '~', ' ', '\v', '\b':
		p.writeToken(s)
	default:
		p.formatAfterToken(s)
	}
}

func (p *formatterState) formatAfterToken(s string) {
	if p.lastWasSingleCharUnary {
		p.writeToken(s)

		return
	}

	curr, _ := utf8.DecodeRuneInString(s)
	if curr == '(' {
		if p.lastIsSeparator() {
			p.buffer.WriteByte(' ')
		}

		p.writeToken(s)

		return
	}

	if curr == ')' ||
		curr == '[' ||
		curr == ']' ||
		(curr == '.' && p.last != ',') ||
		curr == ';' ||
		curr == ',' {
		p.writeToken(s)

		return
	}

	p.buffer.WriteByte(' ')
	p.writeToken(s)
}

// FormatLine formats s and unconditionally flushes the line.
func (p *formatterState) FormatLine(s string) {
	p.Format(s)
	p.FlushLine()
}

// FlushLine flushes buffer to formatted with a trailing newline, skipping
// duplicate empty lines when buffer is empty.
func (p *formatterState) FlushLine() {
	if p.buffer.Len() == 0 {
		return
	}

	p.formatted.WriteString(p.buffer.String())
	p.formatted.WriteByte('\n')
	p.buffer.Reset()
	p.last = '\n'
}

func (p *formatterState) flushCommentsUpTo(pos int) {
	lhs, rhs := extensions.SplitComments(p.comments.comments, pos)
	p.comments.comments = rhs

	for i, c := range lhs {
		if i > 0 || c.IsOneline() {
			p.FlushLine()
		}

		p.emitComment(c)
	}
}

func (p *formatterState) emitComment(c *extensions.Comment) {
	image := c.Image
	if c.IsMultiline() {
		image = strings.ReplaceAll(c.Image, "\n", lineBreakPlaceholder)
	}

	trimmed := strings.TrimRight(image, "\n")
	if isLineCommentKind(c.Kind) {
		trimmed = trailingCommentPlaceholder + trimmed
	}

	switch {
	case c.AtLineBegin() && p.buffer.Len() > 0:
		p.FlushLine()
		p.Format(trimmed)
	case !c.AtLineBegin() && (p.formatted.Len()+p.buffer.Len()) > 0:
		if p.buffer.Len() == 0 || p.bufferEndsWithWhitespace() {
			p.Format(trimmed)
		} else {
			p.Format(" " + trimmed)
		}
	default:
		p.Format(trimmed)
	}

	if c.MustEndLine() || c.AtLineEnd() {
		p.FlushLine()
	}
}

func isLineCommentKind(k extensions.CommentKind) bool {
	return k == extensions.DoubleDash || k == extensions.DoubleSlash || k == extensions.Pound
}

func (p *formatterState) bufferEndsWithWhitespace() bool {
	return p.buffer.Len() > 0 && unicode.IsSpace(p.last)
}

func (p *formatterState) currentLineHasLineComment() bool {
	buf := p.buffer.String()
	start := strings.LastIndexByte(buf, '\n') + 1

	return strings.Contains(buf[start:], trailingCommentPlaceholder)
}

func (p *formatterState) lastIsSeparator() bool {
	if p.buffer.Len() == 0 {
		return false
	}

	if p.last < 0 || p.last > 127 || !isAlphanum(byte(p.last)) {
		return isNonWordSeparator(p.last)
	}

	buf := p.buffer.String()
	i := len(buf) - 1

	for i >= 0 && isAlphanum(buf[i]) {
		i--
	}

	return isWordSeparator(buf[i+1:])
}

func (p *formatterState) addUnary(s string) {
	if p.lastWasSingleCharUnary && p.last == '-' && s == "-" {
		p.lastWasSingleCharUnary = false
	}

	p.Format(s)
	p.lastWasSingleCharUnary = len(s) == 1
}

func (p *formatterState) indentWidth() int {
	if p.opts != nil && p.opts.Indentation > 0 {
		return p.opts.Indentation
	}

	return 2
}

func (p *formatterState) writeIndent() {
	p.buffer.WriteString(strings.Repeat(" ", p.depth*p.indentWidth()))

	if p.depth > 0 {
		p.last = ' '
	}
}

func (p *formatterState) currentLineLen() int {
	buf := p.buffer.String()
	if idx := strings.LastIndexByte(buf, '\n'); idx >= 0 {
		return len(buf) - idx - 1
	}

	return len(buf)
}

func (p *formatterState) writeSpace() {
	p.buffer.WriteByte(' ')
	p.last = ' '
}

func (p *formatterState) writeToken(s string) {
	p.buffer.WriteString(s)
	p.last, _ = utf8.DecodeLastRuneInString(s)
}

func isAlphanum(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') || c == '_'
}

func isWordSeparator(tok string) bool {
	switch strings.ToUpper(tok) {
	case "AND", "OR", "ON", "IN":
		return true
	default:
		return false
	}
}

func isNonWordSeparator(r rune) bool {
	switch r {
	case ',', '<', '>', '-', '+', '=', '*', '/', '%':
		return true
	default:
		return false
	}
}
