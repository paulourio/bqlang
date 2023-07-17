package parser

import (
	"fmt"
	"reflect"

	"github.com/paulourio/bqlang/extensions/ast"
	"github.com/paulourio/bqlang/extensions/errors"
	"github.com/paulourio/bqlang/extensions/token"
)

func NewComment(a Attrib) (Attrib, error) {
	c, err := ast.NewComment()
	if err != nil {
		return nil, err
	}

	return InitLiteral(c, a)
}

func NewNewLine(a Attrib) (Attrib, error) {
	c, err := ast.NewNewLine()
	if err != nil {
		return nil, err
	}

	return InitLiteral(c, a)
}

func NewScript(a Attrib) (Attrib, error) {
	if a == nil {
		panic(a)
	}

	n, err := ast.NewScript()
	if err != nil {
		return nil, err
	}

	// The captured token "a" may be only some text which can simply
	// discard.
	if a != nil {
		if _, ok := a.(*token.Token); ok {
			// Nothing.
		} else {
			return WithExtraChild(n, a)
		}
	}

	return n, nil
}

func NewTemplateBlock(a Attrib) (Attrib, error) {
	if a == nil {
		panic(a)
	}

	i, err := ast.NewTemplateBlock()
	if err != nil {
		return nil, err
	}

	return UpdateLoc(i, a)
}

func NewTemplateForBlock(a Attrib) (Attrib, error) {
	if a == nil {
		panic(a)
	}

	i, err := ast.NewTemplateBlock()
	if err != nil {
		return nil, err
	}

	i.SetKind(ast.TemplateForBlock)

	return UpdateLoc(i, a)
}

func NewTemplateIfBlock(a Attrib) (Attrib, error) {
	if a == nil {
		panic(a)
	}

	i, err := ast.NewTemplateBlock()
	if err != nil {
		return nil, err
	}

	i.SetKind(ast.TemplateIfBlock)

	return UpdateLoc(i, a)
}

func NewTemplateSetBlock(a Attrib) (Attrib, error) {
	if a == nil {
		panic(a)
	}

	i, err := ast.NewTemplateBlock()
	if err != nil {
		return nil, err
	}

	i.SetKind(ast.TemplateSetBlock)

	return UpdateLoc(i, a)
}

func NewTemplateComment(a Attrib) (Attrib, error) {
	if a == nil {
		panic(a)
	}

	i, err := ast.NewTemplateComment()
	if err != nil {
		return nil, err
	}

	return InitLiteral(i, a)
}

func NewTemplateVariable(a, b Attrib) (Attrib, error) {
	if a == nil {
		panic(a)
	}

	i, err := ast.NewTemplateVariable()
	if err != nil {
		return nil, err
	}

	return UpdateLoc(i, a, b)
}

func NewTemplateStatement(a Attrib) (Attrib, error) {
	if a == nil {
		panic(a)
	}

	i, err := ast.NewTemplateStatement()
	if err != nil {
		return nil, err
	}

	return InitLiteral(i, a)
}

func InitLiteral(lit ast.LeafHandler, t Attrib) (Attrib, error) {
	tok := t.(*token.Token)
	lit.SetImage(string(tok.Lit))
	lit.SetStartLoc(tok.Pos.Offset)
	lit.SetEndLoc(tok.Pos.Offset + len(tok.Lit))

	return lit, nil
}

// UpdateLoc expands the localization of a node with a list of tokens
// or locations, from which a location range [min, max) is inferred.
func UpdateLoc(node Attrib, tokens ...Attrib) (ast.NodeHandler, error) {
	n := node.(ast.NodeHandler)

	for _, t := range tokens {
		if t == nil {
			continue
		}

		switch v := t.(type) {
		case *token.Token:
			n.ExpandLoc(v.Pos.Offset, v.Pos.Offset+len(v.Lit))
		case ast.Loc:
			n.ExpandLoc(v.Start, v.End)
		case ast.NodeHandler:
			n.ExpandLoc(v.StartLoc(), v.EndLoc())
		default:
			return nil, fmt.Errorf(
				"%w: cannot UpdateLoc with type %v",
				errors.ErrMalformedParser,
				reflect.TypeOf(t))
		}
	}

	return n, nil
}

// WithExtraChild adds a child node to the node.
func WithExtraChild(a Attrib, c Attrib) (Attrib, error) {
	// If the child candidate c is a token, we should only discard it.
	if _, ok := c.(*token.Token); ok {
		return a, nil
	}

	node, _ := getNodeHandler(a)
	child, _ := getNodeHandler(c)

	node.AddChild(child)

	return node, nil // WrapWithLoc(node, locLeft, locRight)
}

func getNodeHandler(v interface{}) (ast.NodeHandler, ast.Loc) {
	switch t := v.(type) {
	case ast.NodeHandler:
		return t, ast.Loc{Start: t.StartLoc(), End: t.EndLoc()}
	}

	panic(fmt.Errorf("%w: could not get NodeHandler from %v",
		errors.ErrMalformedParser, reflect.TypeOf(v)))
}
