package parser

import (
	"fmt"
	"reflect"

	"github.com/paulourio/bqlang/extensions/ast"
	"github.com/paulourio/bqlang/extensions/errors"
	"github.com/paulourio/bqlang/extensions/token"
)

// NewComment constructs an ast.CommentNode from a scanned token attribute.
func NewComment(a Attrib) (Attrib, error) {
	return InitLiteral(ast.NewComment(), a)
}

// NewScript constructs the root ast.ScriptNode from a parsed attribute.
func NewScript(a Attrib) (Attrib, error) {
	if a == nil {
		return nil, fmt.Errorf("%w: nil script attribute", errors.ErrMalformedParser)
	}

	n := ast.NewScript()

	if _, ok := a.(*token.Token); !ok {
		return WithExtraChild(n, a)
	}

	return n, nil
}

// NewTemplateForBlock constructs an ast.TemplateBlockNode of kind TemplateForBlock.
func NewTemplateForBlock(a Attrib) (Attrib, error) {
	return newTemplateBlock(a, ast.TemplateForBlock, "template for block")
}

// NewTemplateIfBlock constructs an ast.TemplateBlockNode of kind TemplateIfBlock.
func NewTemplateIfBlock(a Attrib) (Attrib, error) {
	return newTemplateBlock(a, ast.TemplateIfBlock, "template if block")
}

// NewTemplateSetBlock constructs an ast.TemplateBlockNode of kind TemplateSetBlock.
func NewTemplateSetBlock(a Attrib) (Attrib, error) {
	return newTemplateBlock(a, ast.TemplateSetBlock, "template set block")
}

// NewTemplateMacroBlock constructs an ast.TemplateBlockNode of kind TemplateMacroBlock.
func NewTemplateMacroBlock(a Attrib) (Attrib, error) {
	return newTemplateBlock(a, ast.TemplateMacroBlock, "template macro block")
}

// NewTemplateCallBlock constructs an ast.TemplateBlockNode of kind TemplateCallBlock.
func NewTemplateCallBlock(a Attrib) (Attrib, error) {
	return newTemplateBlock(a, ast.TemplateCallBlock, "template call block")
}

// NewTemplateDoBlock constructs an ast.TemplateBlockNode of kind TemplateDoBlock.
func NewTemplateDoBlock(a Attrib) (Attrib, error) {
	return newTemplateBlock(a, ast.TemplateDoBlock, "template do block")
}

// NewTemplateRawBlock constructs an ast.TemplateBlockNode of kind TemplateRawBlock.
func NewTemplateRawBlock(a Attrib) (Attrib, error) {
	return newTemplateBlock(a, ast.TemplateRawBlock, "template raw block")
}

func newTemplateBlock(a Attrib, kind ast.Kind, label string) (Attrib, error) {
	if a == nil {
		return nil, fmt.Errorf("%w: nil %s attribute", errors.ErrMalformedParser, label)
	}

	return UpdateLoc(ast.NewTemplateBlock(kind), a)
}

// NewTemplateComment constructs an ast.TemplateCommentNode from a scanned token.
func NewTemplateComment(a Attrib) (Attrib, error) {
	if a == nil {
		return nil, fmt.Errorf("%w: nil template comment attribute", errors.ErrMalformedParser)
	}

	return InitLiteral(ast.NewTemplateComment(), a)
}

// NewTemplateVariable constructs an ast.TemplateVariableNode spanning delimiters a and b.
func NewTemplateVariable(a, b Attrib) (Attrib, error) {
	if a == nil {
		return nil, fmt.Errorf("%w: nil template variable attribute", errors.ErrMalformedParser)
	}

	return UpdateLoc(ast.NewTemplateVariable(), a, b)
}

// InitLiteral populates a leaf node's text and byte offsets from token t.
func InitLiteral(lit ast.LeafHandler, t Attrib) (Attrib, error) {
	tok, ok := t.(*token.Token)
	if !ok {
		return nil, fmt.Errorf("%w: expected *token.Token, got %v",
			errors.ErrMalformedParser, reflect.TypeOf(t))
	}

	lit.SetImage(string(tok.Lit))
	lit.SetStartLoc(tok.Offset)
	lit.SetEndLoc(tok.Offset + len(tok.Lit))

	return lit, nil
}

// UpdateLoc expands the localization of a node with a list of tokens
// or locations, from which a location range [min, max) is inferred.
func UpdateLoc(node Attrib, tokens ...Attrib) (ast.NodeHandler, error) {
	n, ok := node.(ast.NodeHandler)
	if !ok {
		return nil, fmt.Errorf("%w: expected ast.NodeHandler, got %v",
			errors.ErrMalformedParser, reflect.TypeOf(node))
	}

	for _, t := range tokens {
		if t == nil {
			continue
		}

		switch v := t.(type) {
		case *token.Token:
			n.ExpandLoc(v.Offset, v.Offset+len(v.Lit))
		case ast.Loc:
			n.ExpandLoc(v.Start, v.End)
		case ast.NodeHandler:
			n.ExpandLoc(v.StartLoc(), v.EndLoc())
		default:
			return nil, fmt.Errorf(
				"%w: cannot UpdateLoc with type %v",
				errors.ErrMalformedParser,
				reflect.TypeOf(t),
			)
		}
	}

	return n, nil
}

// WithExtraChild adds a child node to the node.
func WithExtraChild(a, c Attrib) (Attrib, error) {
	// If the child candidate c is a token, we should only discard it.
	if _, ok := c.(*token.Token); ok {
		return a, nil
	}

	node, err := getNodeHandler(a)
	if err != nil {
		return nil, err
	}

	child, err := getNodeHandler(c)
	if err != nil {
		return nil, err
	}

	node.AddChild(child)

	return node, nil
}

func getNodeHandler(v any) (ast.NodeHandler, error) {
	if t, ok := v.(ast.NodeHandler); ok {
		return t, nil
	}

	return nil, fmt.Errorf("%w: could not get NodeHandler from %v",
		errors.ErrMalformedParser, reflect.TypeOf(v))
}
