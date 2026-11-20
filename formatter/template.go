package formatter

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/paulourio/bqlang/extensions"
	"github.com/paulourio/bqlang/extensions/ast"
)

type templatePlaceholders struct {
	Placeholders []*placeholderInfo
	counter      int
}

type placeholderInfo struct {
	Element     *extensions.TemplateElement
	Placeholder string
	Original    string
	Recoverable bool
	revertRe    *regexp.Regexp
}

func newTemplatePlaceholders() *templatePlaceholders {
	return &templatePlaceholders{
		Placeholders: make([]*placeholderInfo, 0, 5),
	}
}

// New creates a placeholderInfo for a single template element.
func (t *templatePlaceholders) New(e *extensions.TemplateElement) *placeholderInfo {
	var r *placeholderInfo

	switch e.Kind {
	case ast.TemplateComment:
		r = &placeholderInfo{
			Element:     e,
			Placeholder: eraseNonNewlines(e.Image),
			Recoverable: false,
		}
	case ast.TemplateForBlock, ast.TemplateIfBlock:
		t.counter++
		tok := t.makeName()

		if containsKeywordToken(e.Image, "SELECT") {
			r = &placeholderInfo{
				Element:     e,
				Placeholder: "SELECT " + tok,
				Recoverable: true,
				revertRe:    regexp.MustCompile(`(?i)SELECT\s+` + regexp.QuoteMeta(tok)),
			}
		} else {
			r = newInlinePlaceholder(e, tok)
		}
	case ast.TemplateVariable:
		t.counter++
		tok := t.makeName()

		if e.AtLineBegin() && e.AtLineEnd() {
			r = &placeholderInfo{
				Element:     e,
				Placeholder: fmt.Sprintf("SELECT %s;", tok),
				Recoverable: true,
				revertRe:    regexp.MustCompile(`(?i)SELECT\s+` + regexp.QuoteMeta(tok) + `\s*;?`),
			}
		} else {
			r = newInlinePlaceholder(e, tok)
		}
	case ast.TemplateSetBlock:
		t.counter++
		tok := t.makeName()

		if e.AtLineBegin() && e.AtLineEnd() {
			r = &placeholderInfo{
				Element:     e,
				Placeholder: fmt.Sprintf("SET %s = 1;", tok),
				Recoverable: true,
				revertRe:    regexp.MustCompile(`(?i)SET\s+` + regexp.QuoteMeta(tok) + `\s*=\s*1\s*;?`),
			}
		} else {
			r = newInlinePlaceholder(e, tok)
		}
	default:
		panic(fmt.Sprintf("templatePlaceholders.New: unexpected kind %v (%v)",
			e.Kind.String(), reflect.TypeOf(e)))
	}

	t.Placeholders = append(t.Placeholders, r)

	return r
}

func newInlinePlaceholder(e *extensions.TemplateElement, tok string) *placeholderInfo {
	return &placeholderInfo{
		Element:     e,
		Placeholder: tok,
		Recoverable: true,
		revertRe:    regexp.MustCompile(`(?i)` + regexp.QuoteMeta(tok)),
	}
}

func eraseNonNewlines(s string) string {
	buf := []byte(s)
	for i, b := range buf {
		if b != '\n' {
			buf[i] = ' '
		}
	}

	return string(buf)
}

// ApplyAll replaces all template elements in input with valid SQL placeholders,
// shifting byte offsets of subsequent template elements and comments whenever a
// short template element expands to a longer placeholder.
func (t *templatePlaceholders) ApplyAll(
	input string,
	elems []*extensions.TemplateElement,
	comms []*extensions.Comment,
) string {
	for i, e := range elems {
		ph := t.New(e)
		oldEnd := e.End

		var delta int

		input, delta = ph.applyWithDelta(input)

		if delta != 0 {
			for j := i + 1; j < len(elems); j++ {
				elems[j].Start += delta
				elems[j].End += delta
			}

			for _, c := range comms {
				if c.Start >= oldEnd {
					c.Start += delta
					c.End += delta
				}
			}
		}
	}

	return input
}

func (t *templatePlaceholders) makeName() string {
	return fmt.Sprintf("__bqfmt_tmpl_%d__", t.counter)
}

func (p *placeholderInfo) applyWithDelta(input string) (string, int) {
	start, end := p.Element.Start, p.Element.End
	size := end - start
	p.Original = input[start:end]

	if len(p.Placeholder) <= size {
		pad := strings.Repeat(" ", size-len(p.Placeholder))

		return input[:start] + p.Placeholder + pad + input[end:], 0
	}

	delta := len(p.Placeholder) - size

	return input[:start] + p.Placeholder + input[end:], delta
}

func (p *placeholderInfo) Revert(input string) string {
	if !p.Recoverable || p.revertRe == nil {
		return input
	}

	loc := p.revertRe.FindStringIndex(input)
	if loc == nil {
		return strings.Replace(input, p.Placeholder, p.Original, 1)
	}

	return input[:loc[0]] + p.Original + input[loc[1]:]
}
