package extensions

import (
	"fmt"

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

	return extractTemplatesFromNode(n, input), nil
}

func extractTemplatesFromNode(n ast.NodeHandler, input string) []*TemplateElement {
	children := n.Children()
	r := make([]*TemplateElement, 0, len(children))

	for _, child := range children {
		switch child.(type) {
		case *ast.TemplateBlockNode, *ast.TemplateVariableNode, *ast.TemplateCommentNode:
			a, b := child.StartLoc(), child.EndLoc()
			r = append(r, NewTemplateElement(child.Kind(), a, b, input[a:b], input))
		}
	}

	return r
}

// NewTemplateElement returns an initialized template element.
// The input is necessary to extract information about its surroundings.
func NewTemplateElement(
	kind ast.Kind, start, end int,
	img, input string,
) *TemplateElement {
	c := &TemplateElement{
		Start:       start,
		End:         end,
		Image:       img,
		Kind:        kind,
		atLineBegin: computeBeginsLine(input, start),
		atLineEnd:   endsLineAt(input, end),
	}

	return c
}

// AtLineBegin reports whether the element starts at column 0 of its line.
func (t *TemplateElement) AtLineBegin() bool {
	return t.atLineBegin
}

// AtLineEnd reports whether only horizontal whitespace follows the element on its line.
func (t *TemplateElement) AtLineEnd() bool {
	return t.atLineEnd
}

func computeBeginsLine(input string, start int) bool {
	return start <= 0 || (start <= len(input) && input[start-1] == '\n')
}
