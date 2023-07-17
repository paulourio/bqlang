// Functions specific for data definition language.
package formatter

import (
	"strings"

	"github.com/goccy/go-zetasql/ast"
)

func (p *Printer) VisitCreateFunctionStatement(n *ast.CreateFunctionStatementNode, d Data) {
	pp := p.nest()

	pp.moveBefore(n)
	pp.print(pp.keyword(createFunctionKeywords(n)))
	pp.accept(n.FunctionDeclaration(), d)

	if typ := n.ReturnType(); typ != nil {
		pp.print(pp.keyword("RETURNS"))

		if isSimpleType(typ) {
			pp.accept(n.ReturnType(), d)
		} else {
			pp.println("")
			pp.incDepth()
			pp.accept(typ, d)
			pp.println("")
			pp.decDepth()
		}
	}

	p.print(pp.unnestLeft())
	p.accept(n.SqlFunctionBody(), d)

	switch n.DeterminismLevel() {
	case ast.DeterminismUnspecified:
		// Nothing.
	case ast.DeterministicLevel:
		p.println("")
		p.println(p.keyword("DETERMINISTIC"))
	case ast.NotDeterministicLevel:
		p.println("")
		p.println(p.keyword("NOT DETERMINISTIC"))
	case ast.ImmutableLevel:
		p.println("")
		p.println(p.keyword("IMMUTABLE"))
	case ast.StableLevel:
		p.println("")
		p.println(p.keyword("STABLE"))
	case ast.VolatileLevel:
		p.println("")
		p.println(p.keyword("VOLATILE"))
	}

	if lang := n.Language(); lang != nil {
		p.println("")
		p.print(p.keyword("LANGUAGE"))
		p.moveBefore(lang)
		p.accept(lang, d)
		p.println("")
	}

	if code := n.Code(); code != nil {
		p.print("AS")
		p.accept(code, d)
	}

	if opt := n.OptionsList(); opt != nil {
		p.println("")
		p.accept(n.OptionsList(), d)
	}
}

func (p *Printer) VisitDropStatement(n *ast.DropStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword(dropKeyword(n)))
	p.print(p.identifier(p.toString(n.DdlTarget(), d)))

	switch n.DropMode() {
	case ast.DropModeUnspecified:
		// Nothing.
	case ast.DropModeRestrict:
		p.print(p.keyword("RESTRICT"))
	case ast.DropModeCascade:
		p.print(p.keyword("CASCADE"))
	}

	p.movePast(n)
}

func dropKeyword(n *ast.DropStatementNode) string {
	var b strings.Builder

	b.Grow(23)

	b.WriteString("DROP")

	switch n.SchemaObjectKind() {
	case ast.UnknownSchemaObject:
		b.WriteString(" <UNKNOWN SCHEMA OBJECT>")
	case ast.InvalidSchemaObjectKind:
		b.WriteString(" <INVALID SCHEMA OBJECT>")
	case ast.AggregateFunctionKind:
		b.WriteString(" AGGREGATE FUNCTION")
	case ast.ConstantKind:
		b.WriteString(" CONSTANT")
	case ast.DatabaseKind:
		b.WriteString(" DATABASE")
	case ast.ExternalTableKind:
		b.WriteString(" EXTERNAL TABLE")
	case ast.FunctionKind:
		b.WriteString(" FUNCTION")
	case ast.IndexKind:
		b.WriteString(" INDEX")
	case ast.MaterializedViewKind:
		b.WriteString(" MATERIALIZED VIEW")
	case ast.ModelKind:
		b.WriteString(" MODEL")
	case ast.ProcedureKind:
		b.WriteString(" PROCEDURE")
	case ast.SchemaKind:
		b.WriteString(" SCHEMA")
	case ast.TableKind:
		b.WriteString(" TABLE")
	case ast.TableFunctionKind:
		b.WriteString(" TABLE FUNCTION")
	case ast.ViewKind:
		b.WriteString(" VIEW")
	case ast.SnapshotTableKind:
		b.WriteString(" SNAPSHOT TABLE")
	}

	if n.IsIfExists() {
		b.WriteString("IF EXISTS")
	}

	return b.String()
}

func (p *Printer) VisitCloneDataSource(n *ast.CloneDataSourceNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("CLONE"))
	p.accept(n.PathExpr(), d)
	p.accept(n.ForSystemTime(), d)

	if w := n.WhereClause(); w != nil {
		p.println("")
		p.print(p.keyword("WHERE"))
		p.accept(n.WhereClause(), d)
	}

	p.movePast(n)
}

func (p *Printer) VisitCopyDataSource(n *ast.CopyDataSourceNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("COPY"))
	p.accept(n.PathExpr(), d)
	p.accept(n.ForSystemTime(), d)

	if w := n.WhereClause(); w != nil {
		p.println("")
		p.print(p.keyword("WHERE"))
		p.accept(n.WhereClause(), d)
	}

	p.movePast(n)
}

func (p *Printer) VisitCreateMaterializedViewStatement(n *ast.CreateMaterializedViewStatementNode, d Data) {
	p.moveBefore(n)

	// n.Recursive() is not available.
	cs := createStatementKeywords(n.CreateStatementNode, false, "MATERIALIZED VIEW")
	p.print(p.keyword(cs))
	p.accept(n.DdlTarget(), d)

	// PartitionBy and ClusterBy are the only ones aligned together.
	pb := n.PartitionBy()
	cb := n.ClusterBy()

	if pb != nil || cb != nil {
		pp := p.nest()

		if pb != nil {
			pp.accept(pb, d)
		}

		if cb != nil {
			pp.println("")
			pp.accept(cb, d)
		}

		p.println("")
		p.print(pp.unnest())
	}

	if opt := n.OptionsList(); opt != nil {
		p.println("")
		p.accept(opt, d)
	}

	if q := n.Query(); q != nil {
		p.println("")
		p.println(p.keyword("AS"))
		p.accept(n.Query(), d)
	}

	// n.ReplicaOf() not available.
}

func (p *Printer) VisitCreateTableStatement(n *ast.CreateTableStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword(createTableKeywords(n)))
	p.accept(n.Name(), d)
	p.accept(n.TableElementList(), d)
	p.lnaccept(n.CopyDataSource(), d)
	p.lnaccept(n.CloneDataSource(), d)

	if like := n.LikeTableName(); like != nil {
		p.println("")
		p.print(p.keyword("LIKE"))
		p.accept(like, d)
	}

	if co := n.Collate(); co != nil {
		p.println("")
		p.print(p.keyword("DEFAULT"))
		p.accept(co, d)
	}

	// PartitionBy and ClusterBy are the only ones aligned together.
	pb := n.PartitionBy()
	cb := n.ClusterBy()

	if pb != nil || cb != nil {
		pp := p.nest()

		if pb != nil {
			pp.accept(pb, d)
		}

		if cb != nil {
			pp.println("")
			pp.accept(cb, d)
		}

		p.println("")
		p.print(pp.unnest())
	}

	if opt := n.OptionsList(); opt != nil {
		p.println("")
		p.accept(opt, d)
	}

	if q := n.Query(); q != nil {
		p.println("")
		p.println(p.keyword("AS"))
		p.accept(n.Query(), d)
	}
}

func (p *Printer) VisitCreateSchemaStatement(n *ast.CreateSchemaStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword(createStatementKeywords(n.CreateStatementNode, false, "SCHEMA")))
	p.accept(n.Name(), d)

	if c := n.Collate(); c != nil {
		p.println("")
		p.print(p.keyword("DEFAULT"))
		p.accept(c, d)
	}

	if opt := n.OptionsList(); opt != nil {
		p.println("")
		p.accept(opt, d)
	}

	p.movePast(n)
}

func (p *Printer) VisitCreateSnapshotTableStatement(n *ast.CreateSnapshotTableStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword(createStatementKeywords(n.CreateStatementNode, false, "SNAPSHOT TABLE")))
	p.accept(n.DdlTarget(), d)
	p.acceptClause(n.CloneDataSource(), d)
	p.acceptClause(n.OptionsList(), d)
	p.movePast(n)
}

func (p *Printer) VisitCreateViewStatement(n *ast.CreateViewStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword(createViewKeywords(n)))
	p.accept(n.Name(), d)
	p.lnaccept(n.ColumnList(), d)
	p.lnaccept(n.OptionsList(), d)

	if q := n.Query(); q != nil {
		p.println("")
		p.println(p.keyword("AS"))
		p.accept(n.Query(), d)
	}
}

func (p *Printer) VisitFunctionDeclaration(n *ast.FunctionDeclarationNode, d Data) {
	p.moveBefore(n)

	name := p.function(p.toString(n.Name(), d))
	params := p.toString(n.Parameters(), d)

	// VisitFunctionParams sets "function_params_simple" in data d,
	// so we can use it to decide if we need a new line or not.
	if d.IsEnabled("function_params_simple") {
		p.print(name + params)
	} else {
		p.println(name)
		p.print(params)
	}

	p.println("")
}

func (p *Printer) VisitFunctionParameter(n *ast.FunctionParameterNode, d Data) {
	p.moveBefore(n)
	p.accept(n.Name(), d)
	p.acceptNested(n.Type(), d)

	if n.IsNotAggregate() {
		p.print(p.keyword("NOT AGGREGATE"))
	}

	p.movePast(n)
}

func (p *Printer) VisitFunctionParameters(n *ast.FunctionParametersNode, d Data) {
	entries := n.ParameterEntries()
	simple := len(entries) < 3 && allTrue(mapIsSimpleFunctionParameters(entries))
	d.SetBool("function_params_simple", simple)

	pp := p.nest()
	pp.moveBefore(n)

	if simple {
		pp.print("(")
	} else {
		pp.println("")
		pp.println("(")
		pp.incDepth()
	}

	for i, e := range entries {
		if i > 0 {
			pp.print(",")

			if !simple {
				pp.println("")
			}
		}

		pp.accept(e, d)
	}

	if simple {
		pp.print(")")
	} else {
		pp.println("")
		pp.decDepth()
		pp.print(")")
	}

	p.print(pp.unnestLeft())
}

func (p *Printer) VisitSQLFunctionBody(n *ast.SqlFunctionBodyNode, d Data) {
	p.moveBefore(n)
	p.println("")
	p.println(p.keyword("AS") + " (")
	p.incDepth()
	p.accept(n.Expression(), d)
	p.println("")
	p.decDepth()
	p.movePast(n)
	p.println(")")
}

func createFunctionKeywords(n *ast.CreateFunctionStatementNode) string {
	return createStatementKeywords(
		n.CreateStatementNode, n.IsAggregate(), "FUNCTION")
}

func createTableKeywords(n *ast.CreateTableStatementNode) string {
	return createStatementKeywords(n.CreateStatementNode, false, "TABLE")
}

func createViewKeywords(n *ast.CreateViewStatementNode) string {
	return createStatementKeywords(n.CreateStatementNode, false, "VIEW")
}

func createStatementKeywords(n *ast.CreateStatementNode, agg bool, object string) string {
	var b strings.Builder

	b.Grow(47)

	b.WriteString("CREATE ")

	if n.IsOrReplace() {
		b.WriteString("OR REPLACE ")
	}

	switch n.Scope() {
	case ast.CreateStatementDefaultScope:
		// Nothing.
	case ast.CreateStatementPrivate:
		b.WriteString("PRIVATE ")
	case ast.CreateStatementPublic:
		b.WriteString("PUBLIC ")
	case ast.CreateStatementTemporary:
		b.WriteString("TEMPORARY ")
	}

	if agg {
		b.WriteString("AGGREGATE ")
	}

	b.WriteString(object)

	if n.IsIfNotExists() {
		b.WriteString(" IF NOT EXISTS")
	}

	return b.String()
}

func (p *Printer) VisitNotNullColumnAttribute(n *ast.NotNullColumnAttributeNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("NOT NULL"))
	p.movePast(n)
}
