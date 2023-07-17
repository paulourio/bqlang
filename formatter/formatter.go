package formatter

import (
	"strings"
	"unicode"

	"github.com/paulourio/bqlang/extensions"
)

type Formatter struct {
	opts                   *PrintOptions
	comments               *CommentsQueue
	buffer                 strings.Builder
	formatted              strings.Builder
	maxLength              int
	depth                  int
	last                   rune
	lastWasSingleCharUnary bool
	// noFlushInNextFormat disables line flushing in the next call to Format()
	noFlushInNextFormat bool
	lastWasNewLine      bool
}

// Format formats the string automatically according to the context.
//  1. Inserts necessary space between tokens.
//  2. Calls FlushLine() when a line reaches column limit and it is at
//     some point appropriate to break.
//
// Param s should not contain any leading or trailing whitespace, such
// as ' ' and '\n'.
func (p *Formatter) Format(s string) {
	if len(s) == 0 {
		return
	}

	// At the end we check whether the buffer should be flushed to
	// the formatted buffer.
	defer func() {
		p.lastWasNewLine = false
		p.lastWasSingleCharUnary = false
		p.noFlushInNextFormat = false

		l := p.buffer.Len() - strings.LastIndex(p.buffer.String(), "\n")
		if l >= p.maxLength &&
			p.lastIsSeparator() &&
			!p.noFlushInNextFormat {
			p.FlushLine()
		}
	}()

	data := []rune(s)

	if p.buffer.Len() == 0 {
		p.writeIndent()
		p.writeRunes(data)

		return
	}

	switch p.last {
	case '\n':
		p.writeRunes(append([]rune{'\n'}, data...))
	case '(', '[', '@', '.', '~', ' ', '\v', '\b':
		p.writeRunes(data)
	default:
		if p.lastWasSingleCharUnary {
			p.writeRunes(data)

			return
		}

		curr := data[0]
		if curr == '(' {
			// Inserts a space if last token is a separator, otherwise
			// regards it as a function call.
			if p.lastIsSeparator() {
				p.writeRunes(append([]rune{' '}, data...))
			} else {
				p.writeRunes(data)
			}

			return
		}

		if curr == ';' && p.lastWasNewLine {
			p.writeRunes(append([]rune{'\n'}, data...))

			return
		}

		if curr == ')' ||
			curr == '[' ||
			curr == ']' ||
			// To avoid case like "SELECT 1e10,.1e10"
			(curr == '.' && p.last != ',') ||
			(curr == ';' && p.last != '\n') ||
			curr == ',' {

			p.writeRunes(data)

			return
		}

		if p.last == ' ' && data[0] == ' ' {
			p.writeRunes(data)
		} else {
			p.writeRunes(append([]rune{' '}, data...))
		}
	}
}

// FormatLine is like Format, except always calls FlushLine.
// Use this if you explicitly wants to break the line after this string.
// For example:
//  1. To put a newline after SELECT:
//     FormatLine("SELECT")
//  2. To put close parenthesis on a separate line:
//     FormatLine("")
//     FormatLine(")")
func (p *Formatter) FormatLine(s string) {
	p.Format(s)
	p.FlushLine()
}

// FlushLine flushes buffer to formatted, with a line break at the end.
// It will do nothing if it is a new line and buffer is empty, to avoid
// empty lines.
// Remember to call FlushLine once after the whole process is over in
// case some content remains in buffer.
func (p *Formatter) FlushLine() {
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

// flushCommentsUpTo returns the number of comments flushed, and an
// indicator whether the last character is a newline.
func (p *Formatter) flushCommentsUpTo(pos int) {
	lhs, rhs := extensions.SplitComments(p.comments.comments, pos)
	p.comments.comments = rhs

	for i, c := range lhs {
		// Contiguous comments will always be rendered on separate
		// lines.
		if i > 0 || c.IsOneline() {
			p.FlushLine()
		}

		image := c.Image
		if c.IsMultiline() {
			image = strings.ReplaceAll(c.Image, "\n", lineBreakPlaceholder)
		}

		if c.AtLineBegin() && p.buffer.Len() > 0 {
			p.FlushLine()
			p.Format(strings.TrimRight(image, "\n"))
		} else if !c.AtLineBegin() && (p.formatted.Len()+p.buffer.Len()) > 0 {
			// Add one additional space between line contents and
			// the comment at the final of current line.
			sp := " "

			if p.buffer.Len() == 0 || p.bufferEndsWithWhitespace() {
				sp = ""
			}

			p.Format(sp + strings.TrimRight(image, "\n"))
		} else {
			p.Format(strings.TrimRight(image, "\n"))
		}

		if c.MustEndLine() || c.AtLineEnd() {
			p.FlushLine()
		}
	}
}

func (p *Formatter) bufferEndsWithWhitespace() bool {
	n := p.buffer.Len()
	if n == 0 {
		return false
	}

	s := p.buffer.String()
	last := s[n-1]

	return last == '\n' || unicode.IsSpace(rune(last))
}

func (p *Formatter) lastIsSeparator() bool {
	if p.buffer.Len() == 0 {
		return false
	}

	if !isAlphanum(byte(p.last)) {
		return nonWordSeparators[p.last]
	}

	buf := p.buffer.String()

	i := len(buf) - 1
	for i >= 0 && isAlphanum(buf[i]) {
		i--
	}

	lastTok := buf[i+1:]

	return wordSeparators[lastTok]
}

func (p *Formatter) addUnary(s string) {
	if p.lastWasSingleCharUnary && p.last == '-' && s == "-" {
		p.lastWasSingleCharUnary = false
	}

	p.Format(s)
	p.lastWasSingleCharUnary = len(s) == 1
}

func (p *Formatter) writeIndent() {
	p.buffer.WriteString(strings.Repeat(" ", p.depth*2))

	if p.depth > 0 {
		p.last = ' '
	}
}

func (p *Formatter) writeRunes(d []rune) {
	p.buffer.WriteString(string(d))
	p.last = d[len(d)-1]
}

func isLetter(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func isAlphanum(c byte) bool {
	return isLetter(c) || ('0' <= c && c <= '9') || (c == '_')
}

var wordSeparators = map[string]bool{
	"AND": true,
	"OR":  true,
	"ON":  true,
	"IN":  true,
}

var nonWordSeparators = map[rune]bool{
	',': true,
	'<': true,
	'>': true,
	'-': true,
	'+': true,
	'=': true,
	'*': true,
	'/': true,
	'%': true,
}
