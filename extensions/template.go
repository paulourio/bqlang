package extensions

import (
	"fmt"
	"strings"

	"github.com/paulourio/bqlang/extensions/ast"
)

// TemplateElement represents a parsed Jinja2 expression, block, or comment with source offsets.
type TemplateElement struct {
	// Start is the bytes offset location the template starts in the input.
	Start int
	// End is the bytes offset location the template ends in the input.
	End int
	// Image contains the literal parsed template token, with delimiters included.
	Image string
	// Kind specifies the template element type.
	Kind ast.Kind

	Children []*TemplateElement

	atLineBegin bool
	atLineEnd   bool
}

// ExtractTemplateElements parses input and extracts all top-level Jinja2 template elements.
func ExtractTemplateElements(input string) ([]*TemplateElement, error) {
	n, ok, err := parseExtensionAST(input)
	if err != nil {
		return nil, fmt.Errorf("ExtractTemplateElements: %w", err)
	}

	if !ok {
		return nil, nil
	}

	elems, err := extractTemplatesFromNode(n, input)
	if err != nil {
		return elems, fmt.Errorf("ExtractTemplateElements: %w", err)
	}

	return elems, nil
}

func extractTemplatesFromNode(n ast.NodeHandler, input string) ([]*TemplateElement, error) {
	children := n.Children()
	r := make([]*TemplateElement, 0, len(children))

	for _, child := range children {
		switch child.(type) {
		case *ast.TemplateBlockNode, *ast.TemplateVariableNode, *ast.TemplateCommentNode:
			a, b := child.StartLoc(), child.EndLoc()

			nc, err := NewTemplateElement(child.Kind(), a, b, input[a:b], input)
			if err != nil {
				return r, err
			}

			r = append(r, nc)
		}
	}

	return r, nil
}

// NewTemplateElement returns an initialized template element.
// The input is necessary to extract information about its surroundings.
func NewTemplateElement(
	kind ast.Kind, start, end int,
	img, input string,
) (*TemplateElement, error) {
	c := &TemplateElement{
		Start: start,
		End:   end,
		Image: img,
		Kind:  kind,
	}

	c.update(input)

	return c, nil
}

// AtLineBegin reports whether the element starts at column 0 of its line.
func (t *TemplateElement) AtLineBegin() bool {
	return t.atLineBegin
}

// AtLineEnd reports whether only horizontal whitespace follows the element on its line.
func (t *TemplateElement) AtLineEnd() bool {
	return t.atLineEnd
}

// BeginsLine checks whether the element starts at column 0 of its line.
func (t *TemplateElement) BeginsLine(input string) bool {
	if input == "" {
		return t.atLineBegin
	}

	return computeBeginsLine(input, t.Start)
}

// EndsLine checks whether only horizontal whitespace follows the element on its line.
func (t *TemplateElement) EndsLine(input string) bool {
	if input == "" {
		return t.atLineEnd
	}

	return computeEndsLine(input, t.End)
}

func (t *TemplateElement) update(input string) {
	t.atLineBegin = computeBeginsLine(input, t.Start)
	t.atLineEnd = computeEndsLine(input, t.End)
}

func computeBeginsLine(input string, start int) bool {
	return start <= 0 || (start <= len(input) && input[start-1] == '\n')
}

func computeEndsLine(input string, end int) bool {
	if end >= len(input) {
		return true
	}

	if end < 0 {
		end = 0
	}

	i := end
	for i < len(input) && input[i] != '\n' {
		i++
	}

	return strings.TrimSpace(input[end:i]) == ""
}
