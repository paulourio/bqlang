package formatter

import (
	"strings"

	"github.com/goccy/go-googlesql"
)

func (p *printer) visitArrayColumnSchema(n *googlesql.ASTArrayColumnSchema, d printerData) {
	p.visitElementTypeColumnSchema("ARRAY", n, d)
}

func (p *printer) visitRangeColumnSchema(n *googlesql.ASTRangeColumnSchema, d printerData) {
	p.visitElementTypeColumnSchema("RANGE", n, d)
}

func (p *printer) visitElementTypeColumnSchema(
	kw string,
	n googlesql.ASTElementTypeColumnSchemaNode,
	d printerData,
) {
	p.moveBefore(n)

	pp := p.nest()
	simple := isSimpleColumnSchema(n)

	elem, _ := n.ElementSchema()
	p2 := pp.nest()
	p2.accept(elem, d)
	typ := p2.unnestLeft()

	if simple {
		pp.print(p.keyword(kw) + "<" + typ + ">")
		pp.printColumnSchemaTail(n, false, d)
	} else {
		p1 := pp.nest()
		p1.println(p1.keyword(kw) + "<")
		p1.incDepth()
		p1.println(typ)
		p1.decDepth()
		p1.print(">")
		p1.printColumnSchemaTail(n, false, d)
		pp.print(p1.unnestLeft())
	}

	p.print(pp.unnestLeft())
	p.movePast(n)
}

func (p *printer) visitArrayType(n *googlesql.ASTArrayType, d printerData) {
	et, _ := n.ElementType()
	collate, _ := n.Collate()
	p.visitParameterizedContainerType("ARRAY", n, et, collate, d)
}

func (p *printer) visitRangeType(n *googlesql.ASTRangeType, d printerData) {
	et, _ := n.ElementType()
	collate, _ := n.Collate()
	p.visitParameterizedContainerType("RANGE", n, et, collate, d)
}

func (p *printer) visitParameterizedContainerType(
	kw string,
	n googlesql.ASTNode,
	et googlesql.ASTTypeNode,
	collate *googlesql.ASTCollate,
	d printerData,
) {
	pp := p.nest()
	pp.moveBefore(n)

	simple := isSimpleType(et)
	pp2 := pp.nest()
	pp2.accept(et, d)

	if simple {
		elemType := strings.Trim(pp2.String(), "\n")
		pp.print(pp.keyword(kw) + "<" + elemType + ">")
	} else {
		pp.println(pp.keyword(kw) + "<")
		pp.incDepth()
		pp.print(pp2.unnest())
		pp.println("")
		pp.decDepth()
		pp.print(">")
	}

	pp.accept(collate, d)
	p.print(pp.String())
}

func (p *printer) visitMapType(n *googlesql.ASTMapType, d printerData) {
	pp := p.nest()
	pp.moveBefore(n)

	kt, _ := n.KeyType()
	vt, _ := n.ValueType()
	collate, _ := n.Collate()

	keyStr := strings.Trim(pp.toString(kt, d), "\n")
	valStr := strings.Trim(pp.toString(vt, d), "\n")
	pp.print(pp.keyword("MAP") + "<" + keyStr + ", " + valStr + ">")
	pp.accept(collate, d)
	p.print(pp.String())
}

func (p *printer) visitColumnDefinition(n *googlesql.ASTColumnDefinition, d printerData) {
	p.moveBefore(n)

	name, _ := n.Name()
	schema, _ := n.Schema()

	p.accept(name, d)
	p.print("\v")
	p.acceptNestedString(schema, d)
}

func (p *printer) visitColumnSchema(n *googlesql.ASTColumnSchema, _ printerData) {
	p.addError(&PrinterError{
		Msg:   "unexpected abstract column schema node",
		Node:  n,
		Input: &p.input,
	})
}

func (p *printer) printColumnSchemaTail(
	n googlesql.ASTColumnSchemaNode,
	includeTypeParams bool,
	d printerData,
) {
	if includeTypeParams {
		tp, _ := n.TypeParameters()
		p.accept(tp, d)
	}

	collate, _ := n.Collate()
	gen, _ := n.GeneratedColumnInfo()
	def, _ := n.DefaultExpression()
	attrs, _ := n.Attributes()
	opts, _ := n.OptionsList()

	p.accept(collate, d)
	p.accept(gen, d)

	if nodeDefined(def) {
		p.print(p.keyword("DEFAULT"))
		p.acceptNested(def, d)
	}

	p.accept(attrs, d)
	p.accept(opts, d)
}

func (p *printer) visitMapColumnSchema(n *googlesql.ASTMapColumnSchema, d printerData) {
	p.moveBefore(n)

	ks, _ := n.KeySchema()
	vs, _ := n.ValueSchema()

	pp := p.nest()
	keyStr := strings.Trim(pp.toString(ks, d), "\n")
	valStr := strings.Trim(pp.toString(vs, d), "\n")

	pp.print(pp.keyword("MAP") + "<" + keyStr + ", " + valStr + ">")
	pp.printColumnSchemaTail(n, false, d)

	p.print(pp.unnestLeft())
	p.movePast(n)
}

func (p *printer) visitInferredTypeColumnSchema(n *googlesql.ASTInferredTypeColumnSchema, d printerData) {
	p.moveBefore(n)

	pp := p.nest()
	pp.printColumnSchemaTail(n, false, d)
	p.print(strings.TrimPrefix(pp.unnest(), "\v"))
	p.movePast(n)
}

func (p *printer) visitSimpleColumnSchema(n *googlesql.ASTSimpleColumnSchema, d printerData) {
	p.moveBefore(n)

	typeName, _ := n.TypeName()

	pp := p.nest()
	pp.print(p.typename(pp.toString(typeName, d)))
	pp.printColumnSchemaTail(n, true, d)

	p.print(pp.unnest())
	p.movePast(n)
}

func (p *printer) visitSimpleType(n *googlesql.ASTSimpleType, d printerData) {
	p.moveBefore(n)

	typeName, _ := n.TypeName()
	tp, _ := n.TypeParameters()
	collate, _ := n.Collate()

	p.print(p.typename(p.toString(typeName, d)))
	p.accept(tp, d)
	p.accept(collate, d)
}

func (p *printer) visitStructColumnSchema(n *googlesql.ASTStructColumnSchema, d printerData) {
	p.moveBefore(n)

	pp := p.nest()
	simple := isSimpleColumnSchema(n)
	p1 := pp.nest()

	fields := selectChildrenOfType[*googlesql.ASTStructColumnField](n)
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

	pp.printColumnSchemaTail(n, false, d)

	p.print(pp.unnestLeft())
	p.movePast(n)
}

func (p *printer) visitStructField(n *googlesql.ASTStructField, d printerData) {
	p.moveBefore(n)
	name, _ := n.Name()
	typ, _ := n.Type()

	p.accept(name, d)
	p.acceptNested(typ, d)
}

func (p *printer) visitStructType(n *googlesql.ASTStructType, d printerData) {
	pp := p.nest()
	pp.moveBefore(n)

	fields := selectChildrenOfType[*googlesql.ASTStructField](n)
	simple := areSimpleStructFields(fields)
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

	tp, _ := n.TypeParameters()
	collate, _ := n.Collate()

	pp.accept(tp, d)
	pp.accept(collate, d)
	p.print(pp.unnest())
}

func (p *printer) visitTemplatedParameterType(n *googlesql.ASTTemplatedParameterType, _ printerData) {
	p.moveBefore(n)
	kind, _ := n.Kind()

	switch kind {
	case googlesql.ASTTemplatedParameterTypeEnums_TemplatedTypeKindUninitialized,
		googlesql.ASTTemplatedParameterTypeEnums_TemplatedTypeKindAnyType:
		p.print(p.typename("ANY TYPE"))
	case googlesql.ASTTemplatedParameterTypeEnums_TemplatedTypeKindAnyProto:
		p.print(p.typename("ANY PROTO"))
	case googlesql.ASTTemplatedParameterTypeEnums_TemplatedTypeKindAnyEnum:
		p.print(p.typename("ANY ENUM"))
	case googlesql.ASTTemplatedParameterTypeEnums_TemplatedTypeKindAnyStruct:
		p.print(p.typename("ANY STRUCT"))
	case googlesql.ASTTemplatedParameterTypeEnums_TemplatedTypeKindAnyArray:
		p.print(p.typename("ANY ARRAY"))
	case googlesql.ASTTemplatedParameterTypeEnums_TemplatedTypeKindAnyTable:
		p.print(p.typename("ANY TABLE"))
	}

	p.movePast(n)
}

func (p *printer) visitTVFSchema(n *googlesql.ASTTVFSchema, d printerData) {
	cols := selectChildrenOfType[*googlesql.ASTTVFSchemaColumn](n)
	simple := len(cols) <= 4 && areSimpleTVFSchemaColumns(cols)

	pp := p.nest()
	pp.moveBefore(n)

	p1 := pp.nest()

	for i, e := range cols {
		if i > 0 {
			p1.print(",")

			if !simple {
				p1.println("")
			}
		}

		p1.accept(e, d)
	}

	parent := nodeParent(n)
	isReturnSchema := nodeDefined(parent) && nodeKind(parent) == googlesql.ASTNodeKindAstCreateTableFunctionStatement

	if isReturnSchema {
		pp.print(pp.keyword("RETURNS"))
	}

	if simple {
		pp.print(pp.keyword("TABLE") + "<" + p1.unnestLeft() + ">")
	} else {
		if isReturnSchema {
			pp.println("")
			pp.incDepth()
		}

		pp.println(pp.keyword("TABLE") + "<")
		pp.incDepth()
		pp.println(p1.unnestLeft())
		pp.decDepth()
		pp.print(">")

		if isReturnSchema {
			pp.decDepth()
		}
	}

	p.print(pp.unnestLeft())
}

func (p *printer) visitTVFSchemaColumn(n *googlesql.ASTTVFSchemaColumn, d printerData) {
	pp := p.nest()
	pp.moveBefore(n)
	name, _ := n.Name()
	typ, _ := n.Type()

	pp.accept(name, d)
	pp.acceptNested(typ, d)
	p.print(pp.String())
}

func (p *printer) visitTypeParameterList(n *googlesql.ASTTypeParameterList, d printerData) {
	p.moveBefore(n)
	p.print("(")

	params := selectChildrenOfType[googlesql.ASTLeafNode](n)
	printNestedWithSep(p, params, d, ",")
	p.print(")")
}
