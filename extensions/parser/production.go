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
	c, err := ast.NewComment()
	if err != nil {
		return nil, fmt.Errorf("NewComment: %w", err)
	}

	return InitLiteral(c, a)
}

// NewNewLine constructs an ast.NewLineNode from a scanned token attribute.
func NewNewLine(a Attrib) (Attrib, error) {
	c, err := ast.NewNewLine()
	if err != nil {
		return nil, fmt.Errorf("NewNewLine: %w", err)
	}

	return InitLiteral(c, a)
}

// NewScript constructs the root ast.ScriptNode from a parsed attribute.
func NewScript(a Attrib) (Attrib, error) {
	if a == nil {
		return nil, fmt.Errorf("%w: nil script attribute", errors.ErrMalformedParser)
	}

	n, err := ast.NewScript()
	if err != nil {
		return nil, fmt.Errorf("NewScript: %w", err)
	}

	if _, ok := a.(*token.Token); !ok {
		return WithExtraChild(n, a)
	}

	return n, nil
}

// NewTemplateBlock constructs a generic ast.TemplateBlockNode.
func NewTemplateBlock(a Attrib) (Attrib, error) {
	if a == nil {
		return nil, fmt.Errorf("%w: nil template block attribute", errors.ErrMalformedParser)
	}

	i, err := ast.NewTemplateBlock()
	if err != nil {
		return nil, fmt.Errorf("NewTemplateBlock: %w", err)
	}

	return UpdateLoc(i, a)
}

// NewTemplateForBlock constructs an ast.TemplateBlockNode of kind TemplateForBlock.
func NewTemplateForBlock(a Attrib) (Attrib, error) {
	if a == nil {
		return nil, fmt.Errorf("%w: nil template for block attribute", errors.ErrMalformedParser)
	}

	i, err := ast.NewTemplateBlock()
	if err != nil {
		return nil, fmt.Errorf("NewTemplateForBlock: %w", err)
	}

	i.SetKind(ast.TemplateForBlock)

	return UpdateLoc(i, a)
}

// NewTemplateIfBlock constructs an ast.TemplateBlockNode of kind TemplateIfBlock.
func NewTemplateIfBlock(a Attrib) (Attrib, error) {
	if a == nil {
		return nil, fmt.Errorf("%w: nil template if block attribute", errors.ErrMalformedParser)
	}

	i, err := ast.NewTemplateBlock()
	if err != nil {
		return nil, fmt.Errorf("NewTemplateIfBlock: %w", err)
	}

	i.SetKind(ast.TemplateIfBlock)

	return UpdateLoc(i, a)
}

// NewTemplateSetBlock constructs an ast.TemplateBlockNode of kind TemplateSetBlock.
func NewTemplateSetBlock(a Attrib) (Attrib, error) {
	if a == nil {
		return nil, fmt.Errorf("%w: nil template set block attribute", errors.ErrMalformedParser)
	}

	i, err := ast.NewTemplateBlock()
	if err != nil {
		return nil, fmt.Errorf("NewTemplateSetBlock: %w", err)
	}

	i.SetKind(ast.TemplateSetBlock)

	return UpdateLoc(i, a)
}

// NewTemplateComment constructs an ast.TemplateCommentNode from a scanned token.
func NewTemplateComment(a Attrib) (Attrib, error) {
	if a == nil {
		return nil, fmt.Errorf("%w: nil template comment attribute", errors.ErrMalformedParser)
	}

	i, err := ast.NewTemplateComment()
	if err != nil {
		return nil, fmt.Errorf("NewTemplateComment: %w", err)
	}

	return InitLiteral(i, a)
}

// NewTemplateVariable constructs an ast.TemplateVariableNode spanning delimiters a and b.
func NewTemplateVariable(a, b Attrib) (Attrib, error) {
	if a == nil {
		return nil, fmt.Errorf("%w: nil template variable attribute", errors.ErrMalformedParser)
	}

	i, err := ast.NewTemplateVariable()
	if err != nil {
		return nil, fmt.Errorf("NewTemplateVariable: %w", err)
	}

	return UpdateLoc(i, a, b)
}

// NewTemplateStatement constructs an ast.TemplateStatementNode from a scanned token.
func NewTemplateStatement(a Attrib) (Attrib, error) {
	if a == nil {
		return nil, fmt.Errorf("%w: nil template statement attribute", errors.ErrMalformedParser)
	}

	i, err := ast.NewTemplateStatement()
	if err != nil {
		return nil, fmt.Errorf("NewTemplateStatement: %w", err)
	}

	return InitLiteral(i, a)
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
