package formatter

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/paulourio/bqlang/extensions"
	"github.com/paulourio/bqlang/extensions/ast"
)

type TemplatePlaceholders struct {
	// Placeholders map the placeholder to the original contents.
	Placeholders []*PlaceholderInfo

	counter int
	input   string
}

type PlaceholderInfo struct {
	Element     *extensions.TemplateElement
	Placeholder string
	Original    string
	Recoverable bool
}

func NewTemplatePlaceholders(input string) *TemplatePlaceholders {
	return &TemplatePlaceholders{
		Placeholders: make([]*PlaceholderInfo, 0, 5),
		input:        input,
	}
}

// New returns the string t
func (t *TemplatePlaceholders) New(e *extensions.TemplateElement) *PlaceholderInfo {
	var r *PlaceholderInfo

	switch e.Kind {
	case ast.TemplateComment:
		t.counter++

		// Comments will be reinserted by the comments queue.
		// Its placeholder is a string of whitespaces.
		r = &PlaceholderInfo{
			Element:     e,
			Placeholder: strings.Repeat(" ", e.End-e.Start),
			Recoverable: false,
		}
	case ast.TemplateForBlock, ast.TemplateIfBlock:
		t.counter++

		// Blocks of for loop should be able to be replaced either by
		// a simple statement or an identifier.
		if strings.Contains(e.Image, "SELECT") {
			r = &PlaceholderInfo{
				Element:     e,
				Placeholder: fmt.Sprintf("SELECT %s", t.makeName()),
				Recoverable: true,
			}
		} else {
			r = &PlaceholderInfo{
				Element:     e,
				Placeholder: t.makeName(),
				Recoverable: true,
			}
		}
	case ast.TemplateVariable:
		t.counter++

		if e.BeginsLine(t.input) && e.EndsLine(t.input) {
			r = &PlaceholderInfo{
				Element:     e,
				Placeholder: fmt.Sprintf("SELECT %s;", t.makeName()),
				Recoverable: true,
			}
		} else {
			r = &PlaceholderInfo{
				Element:     e,
				Placeholder: t.makeName(),
				Recoverable: true,
			}
		}
	case ast.TemplateSetBlock:
		t.counter++

		if e.BeginsLine(t.input) && e.EndsLine(t.input) {
			r = &PlaceholderInfo{
				Element:     e,
				Placeholder: fmt.Sprintf("SET %s = 1;", t.makeName()),
				Recoverable: true,
			}
		} else {
			r = &PlaceholderInfo{
				Element:     e,
				Placeholder: t.makeName(),
				Recoverable: true,
			}
		}
	default:
		panic(
			fmt.Sprintf("TemplatePlaceholders.New: kind %v, type %s, %v",
				e.Kind.String(), reflect.TypeOf(e), e))
	}

	if r != nil {
		t.Placeholders = append(t.Placeholders, r)
	}

	return r
}

func (t *TemplatePlaceholders) makeName() string {
	return fmt.Sprintf("__bqfmt_%d", t.counter)
}

func (p *PlaceholderInfo) Apply(input string) string {
	start, end := p.Element.Start, p.Element.End
	size := end - start
	padding := size - len(p.Placeholder)

	if padding < 0 {
		panic(
			fmt.Sprintf(
				"cannot replace template elements by placeholder. "+
					"Element has %d bytes but placeholder requires %d bytes",
				size, len(p.Placeholder)))
	}

	pad := strings.Repeat(" ", padding)

	p.Original = input[start:end]

	return input[:start] + p.Placeholder + pad + input[end:]
}

func (p *PlaceholderInfo) Revert(input string) string {
	return strings.Replace(input, p.Placeholder, p.Original, 1)
}
