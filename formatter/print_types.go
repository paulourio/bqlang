// Functions to print types and schemas.
package formatter

import (
	"strings"

	"github.com/goccy/go-zetasql/ast"
)

func (p *Printer) VisitArrayColumnSchema(n *ast.ArrayColumnSchemaNode, d Data) {
	p.moveBefore(n)
	p.visitArrayColumnSchema(n.ColumnSchemaNode, d)
}

func (p *Printer) VisitArrayType(n *ast.ArrayTypeNode, d Data) {
	pp := p.nest()
	pp.moveBefore(n)

	simple := true

	if et := n.ElementType(); et != nil {
		simple = isSimpleType(et)
	}

	pp2 := pp.nest()
	pp2.accept(n.ElementType(), d)

	if simple {
		elemType := strings.Trim(pp2.String(), "\n")
		pp.print(pp.keyword("ARRAY") + "<" + elemType + ">")
	} else {
		pp.println(pp.keyword("ARRAY") + "<")
		pp.incDepth()
		pp.print(pp2.unnest())
		pp.println("")
		pp.decDepth()
		pp.print(">")
	}

	pp.accept(n.Collate(), d)
	p.print(pp.String())
}

func (p *Printer) visitArrayColumnSchema(n *ast.ColumnSchemaNode, d Data) {
	p.moveBefore(n)

	// Here we may have a child that is a StructColumnSchema.
	pp := p.nest()
	simple := isSimpleColumnSchema(n)

	p2 := pp.nest()
	p2.accept(n.Child(0), d)

	typ := p2.unnestLeft()

	if simple {
		pp.print(p.keyword("ARRAY") + "<" + typ + ">")
	} else {
		p1 := pp.nest()
		p1.println(p1.keyword("ARRAY") + "<")
		p1.incDepth()
		p1.println(typ)
		p1.decDepth()
		p1.print(">")
		p1.accept(n.Collate(), d)
		p1.accept(n.Attributes(), d)
		p1.accept(n.OptionsList(), d)
		pp.print(p1.unnestLeft())
	}

	p.print(pp.unnestLeft())
}

func (p *Printer) VisitColumnDefinition(n *ast.ColumnDefinitionNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Name(), d)
	p.print("\v")
	p.acceptNestedString(n.Schema(), d)
}

func (p *Printer) VisitColumnSchema(n *ast.ColumnSchemaNode, d Data) {
	p.moveBefore(n)

	// In ZetaSQL, ASTColumnSchema is an abstract class, and its
	// extensions are
	//
	//   - ASTArrayColumnSchema
	//   - ASTInferredTypeColumnSchema
	//   - ASTSimpleColumnSchema
	//   - ASTStructColumnSchema
	//
	// However, in Go bindings, we have a struct ast.ColumnSchemaNode
	// and ast.Nodes's of kind ast.ArrayColumnSchema,
	// ast.InferredTypeColumnSchema, ast.StructColumnSchema, and
	// ast.StructColumnSchema are mapped to *ast.ColumnSchemaNode.
	//
	// Effectively, we cannot reach any of ast.ArrayColumnSchemaNode,
	// ast.InferredTypeColumnSchemaNode, ast.StructColumnSchemaNode,
	// or ast.ArrayColumnSchemaNode by walking with Child() methods.
	//
	// Issue: https://github.com/goccy/go-zetasql/issues/30
	//
	// We circumvent this issue by checking the node's kind and handling
	// children accordingly.

	switch n.Kind() {
	case ast.ArrayColumnSchema:
		p.visitArrayColumnSchema(n, d)
	case ast.InferredTypeColumnSchema:
		p.visitInferredTypeColumnSchema(n, d)
	case ast.SimpleColumnSchema:
		p.visitSimpleColumnSchema(n, d)
	case ast.StructColumnSchema:
		p.visitStructColumnSchema(n, d)
	default:
		panic(&PrinterError{
			Msg:  "unexpected kind for column schema node",
			Node: n,
		})
	}

	p.movePast(n)
}

func (p *Printer) visitInferredTypeColumnSchema(n *ast.ColumnSchemaNode, d Data) {
	p.addError(&PrinterError{
		Msg:  "not implemented",
		Node: n,
	})
}

func (p *Printer) visitSimpleColumnSchema(n *ast.ColumnSchemaNode, d Data) {
	p.moveBefore(n)

	pp := p.nest()
	pp.print(p.typename(pp.toString(n.Child(0), d)))
	pp.accept(n.TypeParameters(), d)
	pp.accept(n.Collate(), d)
	pp.accept(n.Attributes(), d)
	pp.accept(n.OptionsList(), d)

	p.print(pp.unnest())
	p.movePast(n)
}

func (p *Printer) VisitSimpleColumnSchema(n *ast.SimpleColumnSchemaNode, d Data) {
	p.moveBefore(n)

	pp := p.nest()
	pp.print(p.typename(pp.toString(n.Child(0), d)))
	pp.accept(n.TypeParameters(), d)
	pp.accept(n.Collate(), d)
	pp.accept(n.Attributes(), d)
	pp.accept(n.OptionsList(), d)

	p.print(pp.unnest())
	p.movePast(n)
}

func (p *Printer) VisitSimpleType(n *ast.SimpleTypeNode, d Data) {
	p.moveBefore(n)
	// n.TypeName() does not return the actual type name.  Instead,
	// we render the name which is the node's first child.
	p.print(p.typename(p.toString(n.Child(0), d)))
	p.accept(n.TypeParameters(), d)
	p.accept(n.Collate(), d)
}

func (p *Printer) visitStructColumnSchema(n *ast.ColumnSchemaNode, d Data) {
	pp := p.nest()
	simple := isSimpleColumnSchema(n)

	p1 := pp.nest()

	num := n.NumChildren()
	fields := selectChildrenOfType[*ast.StructColumnFieldNode](n, num)

	for i, f := range fields {
		if i > 0 {
			p1.println(",")
		}

		p1.accept(f, d)
	}

	if !simple {
		pp.println(pp.keyword("STRUCT") + "<")
		pp.incDepth()
		pp.println(p1.unnestLeft())
		pp.decDepth()
		pp.print(">")
	} else {
		pp.print(pp.keyword("STRUCT") + "<" + p1.unnestLeft() + ">")
	}

	attrs := selectChildrenOfType[*ast.ColumnAttributeListNode](n, num)
	if len(attrs) > 0 {
		printNestedWithSep(pp, attrs, d, "")
	}

	p.print(pp.unnestLeft())
}

func (p *Printer) VisitStructColumnSchema(n *ast.StructColumnSchemaNode, d Data) {
	p.visitStructColumnSchema(n.ColumnSchemaNode, d)
}

func (p *Printer) VisitStructField(n *ast.StructFieldNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Name(), d)
	p.acceptNested(n.Type(), d)
}

func (p *Printer) VisitStructType(n *ast.StructTypeNode, d Data) {
	pp := p.nest()
	pp.moveBefore(n)

	fields := n.StructFields()
	simple := allTrue(mapIsSimpleStructFields(fields))
	pp2 := pp.nest()

	for i, f := range fields {
		if i > 0 {
			pp2.print(",")

			if !simple {
				pp2.println("")
			}
		}

		pp2.accept(f, d)
	}

	elemType := pp2.unnestLeft()

	if simple {
		pp.print(pp.keyword("STRUCT") + "<" + elemType + ">")
	} else {
		pp.print(pp.keyword("STRUCT") + "<")
		pp.println("")
		pp.incDepth()
		pp.print(elemType)
		pp.println("")
		pp.decDepth()
		pp.print(">")
	}

	pp.accept(n.TypeParameters(), d)
	pp.accept(n.Collate(), d)
	p.print(pp.unnest())
}
