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
	lastWasNewLine      bool
}

// Format formats token s into the line buffer, inserting spaces between tokens
// and flushing lines when soft column limits are reached at separator points.
func (p *formatterState) Format(s string) {
	if len(s) == 0 {
		return
	}

	defer func() {
		noFlush := p.noFlushInNextFormat
		p.lastWasNewLine = false
		p.lastWasSingleCharUnary = false
		p.noFlushInNextFormat = false

		l := p.buffer.Len() - strings.LastIndex(p.buffer.String(), "\n")
		if l >= p.maxLength && p.lastIsSeparator() && !noFlush {
			p.FlushLine()
		}
	}()

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

	if curr == ';' && p.lastWasNewLine {
		p.buffer.WriteByte('\n')
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
	sfmt := p.formatted.String()
	sz := len(sfmt)

	if (sz == 0 || sfmt[sz-1] == '\n') && p.buffer.Len() == 0 {
		return
	}

	p.formatted.WriteString(p.buffer.String())
	p.formatted.WriteByte('\n')
	p.buffer.Reset()
	p.lastWasNewLine = true
	p.last = '\n'
}

func (p *formatterState) flushCommentsUpTo(pos int) {
	lhs, rhs := extensions.SplitComments(p.comments.comments, pos)
	p.comments.comments = rhs

	for i, c := range lhs {
		if i > 0 || c.IsOneline() {
			p.FlushLine()
		}

		image := c.Image
		if c.IsMultiline() {
			image = strings.ReplaceAll(c.Image, "\n", lineBreakPlaceholder)
		}

		trimmed := strings.TrimRight(image, "\n")

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
}

func (p *formatterState) bufferEndsWithWhitespace() bool {
	n := p.buffer.Len()
	if n == 0 {
		return false
	}

	s := p.buffer.String()
	last := s[n-1]

	return last == '\n' || unicode.IsSpace(rune(last))
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

func (p *formatterState) writeToken(s string) {
	p.buffer.WriteString(s)
	p.last, _ = utf8.DecodeLastRuneInString(s)
}

func isLetter(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func isAlphanum(c byte) bool {
	return isLetter(c) || ('0' <= c && c <= '9') || (c == '_')
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
