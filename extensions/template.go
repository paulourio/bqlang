package extensions

import (
	"fmt"
	"strings"

	"github.com/paulourio/bqlang/extensions/ast"
	"github.com/paulourio/bqlang/extensions/errors"
	"github.com/paulourio/bqlang/extensions/lexer"
	"github.com/paulourio/bqlang/extensions/parser"
)

type TemplateElement struct {
	// Start is the bytes offset location the template starts in the
	// input.
	Start int
	// End is the bytes offset location the comment ends in the input.
	End int
	// Image contains the literal parsed comment token, with symbols
	// included.
	Image string
	// Kind specifies the comment type.
	Kind ast.Kind

	Children []*TemplateElement
}

func ExtractTemplateElements(input string) ([]*TemplateElement, error) {
	if strings.TrimSpace(input) == "" {
		return nil, nil
	}

	l := lexer.NewLexer([]byte(input))
	p := parser.NewParser()

	s, err := p.Parse(l)
	if err != nil {
		msg := errors.FormatError(err, input)

		return nil, fmt.Errorf("ExtractTemplateElements: %s", msg)
	}

	if n, ok := s.(ast.NodeHandler); ok {
		r := make([]*TemplateElement, 0, 5)

		for _, child := range n.Children() {
			switch c := child.(type) {
			case *ast.TemplateBlockNode:
				a, b := c.StartLoc(), c.EndLoc()

				nc, err := NewTemplateElement(
					c.Kind(), c.StartLoc(), c.EndLoc(), input[a:b], input)
				if err != nil {
					return r, fmt.Errorf("ExtractTemplateElements: %w", err)
				}

				r = append(r, nc)
			case *ast.TemplateVariableNode:
				nc, err := NewTemplateElement(
					c.Kind(), c.StartLoc(), c.EndLoc(), c.Image(), input)
				if err != nil {
					return r, fmt.Errorf("ExtractTemplateElements: %w", err)
				}

				r = append(r, nc)
			case *ast.TemplateCommentNode:
				a, b := c.StartLoc(), c.EndLoc()
				nc, err := NewTemplateElement(c.Kind(), a, b, input[a:b], input)
				if err != nil {
					return r, fmt.Errorf("ExtractTemplateElements: %w", err)
				}

				r = append(r, nc)
			}
		}

		return r, nil
	}

	return nil, nil
}

// NewTemplateElement returns an initialized template element.
// The input is necessary to extract information about its surroundings.
func NewTemplateElement(
	kind ast.Kind, start int, end int,
	img string, input string,
) (*TemplateElement, error) {
	c := &TemplateElement{
		Start: start,
		End:   end,
		Image: img,
		Kind:  kind,
	}

	if err := c.update(input); err != nil {
		return nil, fmt.Errorf("NewTemplateElement: %w", err)
	}

	return c, nil
}

func (t *TemplateElement) update(input string) error {

	return nil
}

// BeginsLine scans the bytes before the comment until a line break or
// the start of the input to check whether the comment is starting
// a new line.
func (t *TemplateElement) BeginsLine(input string) bool {
	i := t.Start - 1

	if i < 0 {
		return true
	}

	return input[i] == '\n'
}

// EndsLine scans the bytes after the comment until a line break or
// the end of the input to check whether the comment is ending a line.
func (t *TemplateElement) EndsLine(input string) bool {
	i := t.End

	if i == len(input) {
		return true
	}

	return input[i] == '\n'
}
