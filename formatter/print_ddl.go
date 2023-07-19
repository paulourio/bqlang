// Functions specific for data definition language.
package formatter

import (
	"strings"

	"github.com/goccy/go-zetasql/ast"
)

func (p *Printer) VisitAddColumnAction(n *ast.AddColumnActionNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("ADD COLUMN"))

	if n.IsIfNotExists() {
		p.print(p.keyword("IF NOT EXISTS"))
	}

	p.accept(n.ColumnDefinition(), d)
}

func (p *Printer) VisitAddConstraintAction(n *ast.AddConstraintActionNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("ADD"))

	if n.IsIfNotExists() {
		p.print(p.keyword("IF NOT EXISTS"))
	}

	p.accept(n.Constraint(), d)
}

func (p *Printer) VisitAlterActionList(n *ast.AlterActionListNode, d Data) {
	p.moveBefore(n)
	p.println("")

	for i, a := range n.Actions() {
		if i > 0 {
			p.println(",")
		}

		p.acceptNested(a, d)
	}
}

func (p *Printer) VisitAlterAllRowAccessPoliciesStatement(n *ast.AlterAllRowAccessPoliciesStatementNode, d Data) {

}

func (p *Printer) VisitAlterColumnDropDefaultAction(n *ast.AlterColumnDropDefaultActionNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER COLUMN"))

	if n.IsIfExists() {
		p.print(p.keyword("IF EXISTS"))
	}

	p.accept(n.ColumnName(), d)
	p.println("")
	p.incDepth()
	p.println(p.keyword("DROP DEFAULT"))
	p.decDepth()
	p.movePast(n)
}

func (p *Printer) VisitAlterColumnDropNotNullAction(n *ast.AlterColumnDropNotNullActionNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER COLUMN"))

	if n.IsIfExists() {
		p.print(p.keyword("IF EXISTS"))
	}

	p.accept(n.ColumnName(), d)
	p.println("")
	p.incDepth()
	p.println(p.keyword("DROP NOT NULL"))
	p.decDepth()
	p.movePast(n)
}

func (p *Printer) VisitAlterColumnOptionsAction(n *ast.AlterColumnOptionsActionNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER COLUMN"))

	if n.IsIfExists() {
		p.print(p.keyword("IF EXISTS"))
	}

	p.accept(n.ColumnName(), d)
	p.println("")
	p.incDepth()
	p.print(p.keyword("SET"))
	p.accept(n.OptionsList(), d)
	p.println("")
	p.decDepth()
}

func (p *Printer) VisitAlterColumnSetDefaultAction(n *ast.AlterColumnSetDefaultActionNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER COLUMN"))

	if n.IsIfExists() {
		p.print(p.keyword("IF EXISTS"))
	}

	p.accept(n.ColumnName(), d)
	p.println("")
	p.incDepth()
	p.print(p.keyword("SET DEFAULT"))
	p.accept(n.DefaultExpression(), d)
	p.println("")
	p.decDepth()
}

func (p *Printer) VisitAlterColumnTypeAction(n *ast.AlterColumnTypeActionNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER COLUMN"))

	if n.IsIfExists() {
		p.print(p.keyword("IF EXISTS"))
	}

	p.accept(n.ColumnName(), d)
	p.println("")
	p.incDepth()
	p.print(p.keyword("SET DATA TYPE"))
	p.accept(n.Schema(), d)
	p.println("")
	p.decDepth()
}

func (p *Printer) VisitAlterConstraintEnforcementAction(n *ast.AlterConstraintEnforcementActionNode, d Data) {

}

func (p *Printer) VisitAlterConstraintSetOptionsAction(n *ast.AlterConstraintSetOptionsActionNode, d Data) {

}

func (p *Printer) VisitAlterDatabaseStatement(n *ast.AlterDatabaseStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER DATABASE"))
	p.accept(n.DdlTarget(), d)
	p.println("")
	p.incDepth()
	p.accept(n.ActionList(), d)
	p.println("")
	p.decDepth()
}

func (p *Printer) VisitAlterEntityStatement(n *ast.AlterEntityStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER ENTITY"))
	p.accept(n.DdlTarget(), d)
	p.println("")
	p.incDepth()
	p.accept(n.ActionList(), d)
	p.println("")
	p.decDepth()
}

func (p *Printer) VisitAlterMaterializedViewStatement(n *ast.AlterMaterializedViewStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER MATERIALIZED VIEW"))
	p.accept(n.DdlTarget(), d)
	p.println("")
	p.incDepth()
	p.accept(n.ActionList(), d)
	p.println("")
	p.decDepth()
}

func (p *Printer) VisitAlterPrivilegeRestrictionStatement(n *ast.AlterPrivilegeRestrictionStatementNode, d Data) {

}

func (p *Printer) VisitAlterRowAccessPolicyStatement(n *ast.AlterRowAccessPolicyStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER ROW ACCESS POLICY"))
	p.accept(n.DdlTarget(), d)
	p.println("")
	p.incDepth()
	p.accept(n.ActionList(), d)
	p.println("")
	p.decDepth()
}

func (p *Printer) VisitAlterSchemaStatement(n *ast.AlterSchemaStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER SCHEMA"))
	p.accept(n.DdlTarget(), d)
	p.println("")
	p.incDepth()
	p.accept(n.ActionList(), d)
	p.println("")
	p.decDepth()
}

func (p *Printer) VisitAlterTableStatement(n *ast.AlterTableStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER TABLE"))
	p.accept(n.DdlTarget(), d)
	p.println("")
	p.incDepth()
	p.accept(n.ActionList(), d)
	p.println("")
	p.decDepth()
	p.movePast(n)
}

func (p *Printer) VisitAlterViewStatement(n *ast.AlterViewStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER VIEW"))
	p.accept(n.DdlTarget(), d)
	p.println("")
	p.incDepth()
	p.accept(n.ActionList(), d)
	p.println("")
	p.decDepth()
	p.movePast(n)
}

func (p *Printer) VisitCloneDataSource(n *ast.CloneDataSourceNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("CLONE"))
	p.accept(n.PathExpr(), d)
	p.accept(n.ForSystemTime(), d)
	p.lnaccept(n.WhereClause(), d)
	p.movePast(n)
}

func (p *Printer) VisitCopyDataSource(n *ast.CopyDataSourceNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("COPY"))
	p.accept(n.PathExpr(), d)
	p.accept(n.ForSystemTime(), d)
	p.lnaccept(n.WhereClause(), d)
	p.movePast(n)
}

func (p *Printer) VisitCreateExternalTableStatement(n *ast.CreateExternalTableStatementNode, d Data) {
	p.moveBefore(n)

	cs := createStatementKeywords(n.CreateStatementNode, false, "EXTERNAL TABLE")
	p.print(p.keyword(cs))
	p.accept(n.DdlTarget(), d)
	p.lnaccept(n.TableElementList(), d)
	p.lnaccept(n.WithConnectionClause(), d)
	p.lnaccept(n.WithPartitionColumnsClause(), d)
	p.lnaccept(n.OptionsList(), d)
}

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
	p.lnaccept(n.WithConnectionClause(), d)
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

func (p *Printer) VisitCreateProcedureStatement(n *ast.CreateProcedureStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword(createStatementKeywords(n.CreateStatementNode, false, "PROCEDURE")))
	p.accept(n.DdlTarget(), d)

	params := n.Parameters()
	simple := allTrue(mapIsSimpleFunctionParameters(params.ParameterEntries()))

	if simple {
		p.print(strings.TrimLeft(p.toString(params, d), "\v"))
	} else {
		p.lnaccept(params, d)
	}

	p.lnaccept(n.OptionsList(), d)
	p.lnaccept(n.Body(), d)
	p.movePast(n)
}

func (p *Printer) VisitCreateRowAccessPolicyStatement(n *ast.CreateRowAccessPolicyStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword(createStatementKeywords(n.CreateStatementNode, false, "ROW ACCESS POLICY")))

	// For BigQuery, the syntax is
	//
	//   CREATE [OR REPLACE] ROW ACCESS POLICY [IF NOT EXISTS]
	//   row_access_policy_name ON table_name
	//
	// but on ZetaSQL grammar, `row_access_policy_name` is  an optional
	// identifier.  However, when Name() is nil, both Name() and
	// DdlTarget() are nil, so we need to access the DDL target manually
	// as the first child.
	if name := n.Name(); name != nil {
		p.accept(n.Name(), d)
		p.println("")
		p.print(p.keyword("ON"))
		p.accept(n.DdlTarget(), d)
	} else {
		p.println("")
		p.print(p.keyword("ON"))
		p.accept(n.Child(0), d)
	}

	p.lnaccept(n.GrantTo(), d)
	p.lnaccept(n.FilterUsing(), d)
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

func (p *Printer) VisitCreateTableFunctionStatement(n *ast.CreateTableFunctionStatementNode, d Data) {
	p.moveBefore(n)

	pp := p.nest()
	pp.print(pp.keyword(createStatementKeywords(n.CreateStatementNode, false, "TABLE FUNCTION")))

	p.print(pp.unnestLeft())
	p.accept(n.FunctionDeclaration(), d)
	p.lnaccept(n.ReturnTVFSchema(), d)
	p.lnaccept(n.OptionsList(), d)

	if q := n.Query(); q != nil {
		p.println("")
		p.println(p.keyword("AS"))
		p.lnaccept(n.Query(), d)
	}
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

func (p *Printer) VisitDropAllRowAccessPoliciesStatement(n *ast.DropAllRowAccessPoliciesStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("DROP ALL ROW ACCESS POLICIES ON "))
	p.print(p.identifier(p.toString(n.TableName(), d)))
	p.movePast(n)
}

func (p *Printer) VisitDropColumnAction(n *ast.DropColumnActionNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword(dropKeyword(n, "COLUMN")))
	p.print(p.identifier(p.toString(n.ColumnName(), d)))
	p.movePast(n)
}

func (p *Printer) VisitDropConstraintAction(n *ast.DropConstraintActionNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword(dropKeyword(n, "CONSTRAINT")))
	p.print(p.identifier(p.toString(n.ConstraintName(), d)))
	p.movePast(n)
}

func (p *Printer) VisitDropEntityStatement(n *ast.DropEntityStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword(dropKeyword(n, "ENTITY")))
	p.print(p.identifier(p.toString(n.DdlTarget(), d)))
	p.movePast(n)
}

func (p *Printer) VisitDropFunctionStatement(n *ast.DropFunctionStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword(dropKeyword(n, "FUNCTION")))
	p.print(p.identifier(p.toString(n.Name(), d)))
	p.movePast(n)
}

func (p *Printer) VisitDropMaterializedViewStatement(n *ast.DropMaterializedViewStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword(dropKeyword(n, "MATERIALIZED VIEW")))
	p.print(p.identifier(p.toString(n.Name(), d)))
	p.movePast(n)
}

func (p *Printer) VisitDropPrimaryKeyAction(n *ast.DropPrimaryKeyActionNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword(dropKeyword(n, "PRIMARY KEY")))
	p.movePast(n)
}

func (p *Printer) VisitDropPrivilegeRestrictionStatement(n *ast.DropPrivilegeRestrictionStatementNode, d Data) {

}

func (p *Printer) VisitDropRowAccessPolicyStatement(n *ast.DropRowAccessPolicyStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword(dropKeyword(n, "ROW ACCESS POLICY")))
	p.print(p.identifier(p.toString(n.Name(), d)))
	p.print(p.keyword("ON"))
	p.accept(n.TableName(), d)
	p.movePast(n)

}

func (p *Printer) VisitDropSearchIndexStatement(n *ast.DropSearchIndexStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword(dropKeyword(n, "SEARCH INDEX")))
	p.print(p.identifier(p.toString(n.Name(), d)))
	p.movePast(n)

}

func (p *Printer) VisitDropSnapshotTableStatement(n *ast.DropSnapshotTableStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword(dropKeyword(n, "SNAPSHOT TABLE")))
	p.print(p.identifier(p.toString(n.Name(), d)))
	p.movePast(n)

}

func (p *Printer) VisitDropTableFunctionStatement(n *ast.DropTableFunctionStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword(dropKeyword(n, "TABLE FUNCTION")))
	p.print(p.identifier(p.toString(n.Name(), d)))
	p.movePast(n)

}

func (p *Printer) VisitDropStatement(n *ast.DropStatementNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword(dropStatementKeyword(n)))
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

type DropStatementKeyworder interface {
	IsIfExists() bool
}

func dropKeyword(n DropStatementKeyworder, object string) string {
	var b strings.Builder

	b.Grow(20)

	b.WriteString("DROP ")
	b.WriteString(object)

	if n.IsIfExists() {
		b.WriteString(" IF EXISTS")
	}

	return b.String()
}

func dropStatementKeyword(n *ast.DropStatementNode) string {
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
		b.WriteString(" IF EXISTS")
	}

	return b.String()
}

func (p *Printer) VisitFilterUsingClause(n *ast.FilterUsingClauseNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("FILTER USING") + " (")

	expr := n.Predicate()
	simple := isSimpleExpr(expr)

	if simple {
		p.accept(expr, d)
		p.print(")")
	} else {
		p.println("")
		p.incDepth()
		p.accept(expr, d)
		p.println("")
		p.decDepth()
		p.print(")")
	}
}

func (p *Printer) VisitForeignKey(n *ast.ForeignKeyNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("CONSTRAINT"))
	p.accept(n.ConstraintName(), d)
	p.print(p.keyword("FOREIGN KEY") + " ")
	p.accept(n.ColumnList(), d)
	p.lnaccept(n.Reference(), d)
}

func (p *Printer) VisitForeignKeyReference(n *ast.ForeignKeyReferenceNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("REFERENCES"))
	p.accept(n.TableName(), d)
	p.accept(n.ColumnList(), d)

	if n.Enforced() {
		p.print(p.keyword("ENFORCED"))
	} else {
		p.print(p.keyword("NOT ENFORCED"))
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

	switch n.ProcedureParameterMode() {
	case ast.NotSetProcedureParameter:
		// Nothing
	case ast.InProcedureParameter:
		p.print(p.keyword("IN"))
	case ast.OutProcedureParameter:
		p.print(p.keyword("OUT"))
	case ast.InOutProcedureParameter:
		p.print(p.keyword("INOUT"))
	}

	if !d.IsEnabled("function_params_simple") && d.IsEnabled("procedure_params") {
		p.acceptNested(n.Name(), d)
	} else {
		p.accept(n.Name(), d)
	}

	if t := n.Type(); t != nil {
		p.acceptNested(n.Type(), d)
	} else {
		p.patchedVisitTemplatedParameterType(n)
	}

	if n.IsNotAggregate() {
		p.print(p.keyword("NOT AGGREGATE"))
	}

	p.movePast(n)
}

func (p *Printer) VisitFunctionParameters(n *ast.FunctionParametersNode, d Data) {
	entries := n.ParameterEntries()
	simple := len(entries) < 3 && allTrue(mapIsSimpleFunctionParameters(entries))
	d.SetBool("function_params_simple", simple)

	parent := n.Parent()
	if nodeDefined(parent) {
		// This will allow indenting "IN/OUT/INOUT" in procedure parameters.
		d.SetBool("procedure_params", parent.Kind() == ast.CreateProcedureStatement)
	}

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

func (p *Printer) VisitGranteeList(n *ast.GranteeListNode, d Data) {
	p.moveBefore(n)

	exprs := n.GranteeList()
	simple := len(exprs) == 1

	if simple {
		p.print(p.toString(exprs[0], d))
	} else {
		var prev ast.Node

		p.println("")
		p.incDepth()

		for i, e := range exprs {
			if i > 0 {
				p.print(",")
				p.movePastLine(prev)
				p.println("")
			}

			p.accept(e, d)
			prev = e
		}

		p.println("")
		p.decDepth()
	}
}

func (p *Printer) VisitGrantToClause(n *ast.GrantToClauseNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("GRANT TO") + " (")
	p.accept(n.GranteeList(), d)
	p.print(")")
}

func (p *Printer) VisitPrimaryKey(n *ast.PrimaryKeyNode, d Data) {
	p.moveBefore(n)
	p.accept(n.ConstraintName(), d)
	p.print(p.keyword("PRIMARY KEY") + " ")
	p.accept(n.ColumnList(), d)

	if n.Enforced() {
		p.print(p.keyword("ENFORCED"))
	} else {
		p.print(p.keyword("NOT ENFORCED"))
	}
}

func (p *Printer) VisitPrimaryKeyColumnAttribute(n *ast.PrimaryKeyColumnAttributeNode, d Data) {
	p.moveBefore(n)

	if n.Enforced() {
		p.print(p.keyword("ENFORCED"))
	} else {
		p.print(p.keyword("NOT ENFORCED"))
	}
}

func (p *Printer) VisitRenameColumnAction(n *ast.RenameColumnActionNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("RENAME COLUMN"))

	if n.IsIfExists() {
		p.print(p.keyword("IF EXISTS"))
	}

	p.accept(n.ColumnName(), d)
	p.print(p.keyword("TO"))
	p.accept(n.NewColumnName(), d)
}

func (p *Printer) VisitRenameToClause(n *ast.RenameToClauseNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("RENAME TO"))
	p.accept(n.NewName(), d)
}

func (p *Printer) VisitSetCollateClause(n *ast.SetCollateClauseNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("SET DEFAULT"))
	p.accept(n.Collate(), d)
}

func (p *Printer) VisitSetOptionsAction(n *ast.SetOptionsActionNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("SET"))
	p.accept(n.OptionsList(), d)
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

func (p *Printer) VisitTableConstraint(n *ast.TableConstraintBaseNode, d Data) {
	p.moveBefore(n)
	p.accept(n.ConstraintName(), d)
}

func (p *Printer) VisitWithConnectionClause(n *ast.WithConnectionClauseNode, d Data) {
	p.moveBefore(n)

	kw := "WITH"

	parent := n.Parent()
	if nodeDefined(parent) {
		if f, ok := parent.(*ast.CreateFunctionStatementNode); ok {
			if f.IsRemote() {
				kw = "REMOTE WITH"
			}
		}
	}

	p.print(p.keyword(kw))
	p.accept(n.ConnectionClause(), d)
}

func (p *Printer) VisitWithPartitionColumnsClause(n *ast.WithPartitionColumnsClauseNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("WITH PARTITION COLUMNS"))
	p.lnaccept(n.TableElementList(), d)
}
