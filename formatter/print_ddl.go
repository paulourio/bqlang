package formatter

import (
	"strings"

	"github.com/goccy/go-googlesql"
)

func (p *printer) visitAddColumnAction(n *googlesql.ASTAddColumnAction, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("ADD COLUMN"))

	if ine, _ := n.IsIfNotExists(); ine {
		p.print(p.keyword("IF NOT EXISTS"))
	}

	cd, _ := n.ColumnDefinition()
	p.accept(cd, d)

	if pos, _ := n.ColumnPosition(); nodeDefined(pos) {
		p.printColumnPosition(pos, d)
	}

	if fill, _ := n.FillExpression(); nodeDefined(fill) {
		p.print(p.keyword("FILL USING"))
		p.accept(fill, d)
	}

	p.movePast(n)
}

func (p *printer) printColumnPosition(n *googlesql.ASTColumnPosition, d printerData) {
	p.moveBefore(n)

	posType, _ := n.Type()
	if posType == googlesql.ASTColumnPositionEnums_RelativePositionTypePreceding {
		p.print(p.keyword("PRECEDING"))
	} else {
		p.print(p.keyword("FOLLOWING"))
	}

	id, _ := n.Identifier()
	p.accept(id, d)
	p.movePast(n)
}

func (p *printer) visitAddConstraintAction(n *googlesql.ASTAddConstraintAction, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("ADD"))

	c, _ := n.Constraint()
	p.accept(c, d)
	p.movePast(n)
}

func (p *printer) printConstraintName(cn *googlesql.ASTIdentifier, parent googlesql.ASTNode, d printerData) {
	if !nodeDefined(cn) {
		return
	}

	p.print(p.keyword("CONSTRAINT"))

	if aca, ok := parent.(*googlesql.ASTAddConstraintAction); ok {
		if ine, _ := aca.IsIfNotExists(); ine {
			p.print(p.keyword("IF NOT EXISTS"))
		}
	}

	p.accept(cn, d)
}

func (p *printer) visitAlterActionList(n *googlesql.ASTAlterActionList, d printerData) {
	p.moveBefore(n)
	p.println("")

	for i, a := range selectChildrenOfType[googlesql.ASTAlterActionNode](n) {
		if i > 0 {
			p.println(",")
		}

		p.acceptNested(a, d)
	}
}

func (p *printer) visitAlterAllRowAccessPoliciesStatement(
	n *googlesql.ASTAlterAllRowAccessPoliciesStatement,
	d printerData,
) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER ALL ROW ACCESS POLICIES ON"))

	tbl, _ := n.TableNamePath()
	act, _ := n.AlterAction()

	p.accept(tbl, d)
	p.println("")
	p.incDepth()
	p.acceptNested(act, d)
	p.println("")
	p.decDepth()
	p.movePast(n)
}

func (p *printer) visitAlterColumnDropDefaultAction(n *googlesql.ASTAlterColumnDropDefaultAction, d printerData) {
	ie, _ := n.IsIfExists()
	cn, _ := n.ColumnName()
	p.printAlterColumnAction(n, ie, cn, d, func() {
		p.print(p.keyword("DROP DEFAULT"))
	})
}

func (p *printer) visitAlterColumnDropNotNullAction(n *googlesql.ASTAlterColumnDropNotNullAction, d printerData) {
	ie, _ := n.IsIfExists()
	cn, _ := n.ColumnName()
	p.printAlterColumnAction(n, ie, cn, d, func() {
		p.print(p.keyword("DROP NOT NULL"))
	})
}

func (p *printer) visitAlterColumnOptionsAction(n *googlesql.ASTAlterColumnOptionsAction, d printerData) {
	ie, _ := n.IsIfExists()
	cn, _ := n.ColumnName()
	ol, _ := n.OptionsList()
	p.printAlterColumnAction(n, ie, cn, d, func() {
		p.print(p.keyword("SET"))
		p.accept(ol, d)
	})
}

func (p *printer) visitAlterColumnSetDefaultAction(n *googlesql.ASTAlterColumnSetDefaultAction, d printerData) {
	ie, _ := n.IsIfExists()
	cn, _ := n.ColumnName()
	de, _ := n.DefaultExpression()
	p.printAlterColumnAction(n, ie, cn, d, func() {
		p.print(p.keyword("SET DEFAULT"))
		p.accept(de, d)
	})
}

func (p *printer) visitAlterColumnTypeAction(n *googlesql.ASTAlterColumnTypeAction, d printerData) {
	ie, _ := n.IsIfExists()
	cn, _ := n.ColumnName()
	sc, _ := n.Schema()
	co, _ := n.Collate()
	p.printAlterColumnAction(n, ie, cn, d, func() {
		p.print(p.keyword("SET DATA TYPE"))
		p.accept(sc, d)
		p.accept(co, d)
	})
}

func (p *printer) printAlterColumnAction(
	n googlesql.ASTNode,
	ifExists bool,
	colName *googlesql.ASTIdentifier,
	d printerData,
	bodyFn func(),
) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER COLUMN"))

	if ifExists {
		p.print(p.keyword("IF EXISTS"))
	}

	p.accept(colName, d)
	p.println("")
	p.incDepth()
	bodyFn()
	p.println("")
	p.decDepth()
	p.movePast(n)
}

func (p *printer) visitAlterConstraintEnforcementAction(
	n *googlesql.ASTAlterConstraintEnforcementAction,
	d printerData,
) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER CONSTRAINT"))

	if ie, _ := n.IsIfExists(); ie {
		p.print(p.keyword("IF EXISTS"))
	}

	cn, _ := n.ConstraintName()
	p.accept(cn, d)

	if enf, _ := n.IsEnforced(); enf {
		p.print(p.keyword("ENFORCED"))
	} else {
		p.print(p.keyword("NOT ENFORCED"))
	}

	p.movePast(n)
}

func (p *printer) visitAlterConstraintSetOptionsAction(
	n *googlesql.ASTAlterConstraintSetOptionsAction,
	d printerData,
) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER CONSTRAINT"))

	if ie, _ := n.IsIfExists(); ie {
		p.print(p.keyword("IF EXISTS"))
	}

	cn, _ := n.ConstraintName()
	ol, _ := n.OptionsList()

	p.accept(cn, d)
	p.print(p.keyword("SET"))
	p.accept(ol, d)
	p.movePast(n)
}

func (p *printer) printAlterActionList(n googlesql.ASTNode, al *googlesql.ASTAlterActionList, d printerData) {
	p.println("")
	p.incDepth()
	p.accept(al, d)
	p.println("")
	p.decDepth()
	p.movePast(n)
}

func (p *printer) visitAlterStatementBase(kw string, n googlesql.ASTAlterStatementBaseNode, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword(kw))

	if ie, _ := n.IsIfExists(); ie {
		p.print(p.keyword("IF EXISTS"))
	}

	target, _ := n.Path()
	al, _ := n.ActionList()

	p.accept(target, d)
	p.printAlterActionList(n, al, d)
}

func (p *printer) visitAlterDatabaseStatement(n *googlesql.ASTAlterDatabaseStatement, d printerData) {
	p.visitAlterStatementBase("ALTER DATABASE", n, d)
}

func (p *printer) visitAlterEntityStatement(n *googlesql.ASTAlterEntityStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER"))

	if typ, _ := n.Type(); nodeDefined(typ) {
		p.print(p.rawKeywordIdent(typ))
	} else {
		p.print(p.keyword("ENTITY"))
	}

	if ie, _ := n.IsIfExists(); ie {
		p.print(p.keyword("IF EXISTS"))
	}

	target, _ := n.Path()
	al, _ := n.ActionList()

	p.accept(target, d)
	p.printAlterActionList(n, al, d)
}

func (p *printer) visitAlterMaterializedViewStatement(n *googlesql.ASTAlterMaterializedViewStatement, d printerData) {
	p.visitAlterStatementBase("ALTER MATERIALIZED VIEW", n, d)
}

func (p *printer) visitAlterModelStatement(n *googlesql.ASTAlterModelStatement, d printerData) {
	p.visitAlterStatementBase("ALTER MODEL", n, d)
}

func (p *printer) visitAlterPrivilegeRestrictionStatement(
	n *googlesql.ASTAlterPrivilegeRestrictionStatement,
	d printerData,
) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER PRIVILEGE RESTRICTION"))

	if ie, _ := n.IsIfExists(); ie {
		p.print(p.keyword("IF EXISTS"))
	}

	p.print(p.keyword("ON"))

	if privs, _ := n.Privileges(); nodeDefined(privs) {
		p.printPrivileges(privs, d)
	}

	p.print(p.keyword("ON"))

	if ot, _ := n.ObjectType(); nodeDefined(ot) {
		p.print(p.rawKeywordIdent(ot))
	}

	target, _ := n.Path()
	al, _ := n.ActionList()

	p.accept(target, d)
	p.printAlterActionList(n, al, d)
}

func (p *printer) visitRestrictToClause(n *googlesql.ASTRestrictToClause, d printerData) {
	rl, _ := n.RestricteeList()
	p.printRestricteeClause(n, "RESTRICT TO", rl, d)
}

func (p *printer) visitAddToRestricteeListClause(
	n *googlesql.ASTAddToRestricteeListClause,
	d printerData,
) {
	kw := "ADD"
	if ine, _ := n.IsIfNotExists(); ine {
		kw = "ADD IF NOT EXISTS"
	}

	rl, _ := n.RestricteeList()
	p.printRestricteeClause(n, kw, rl, d)
}

func (p *printer) visitRemoveFromRestricteeListClause(
	n *googlesql.ASTRemoveFromRestricteeListClause,
	d printerData,
) {
	kw := "REMOVE"
	if ie, _ := n.IsIfExists(); ie {
		kw = "REMOVE IF EXISTS"
	}

	rl, _ := n.RestricteeList()
	p.printRestricteeClause(n, kw, rl, d)
}

func (p *printer) printRestricteeClause(
	n googlesql.ASTNode,
	kw string,
	rl *googlesql.ASTGranteeList,
	d printerData,
) {
	p.moveBefore(n)
	p.print(p.keyword(kw) + " (")
	p.accept(rl, d)
	p.print(")")
	p.movePast(n)
}

func (p *printer) visitAlterRowAccessPolicyStatement(n *googlesql.ASTAlterRowAccessPolicyStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("ALTER ROW ACCESS POLICY"))

	if ie, _ := n.IsIfExists(); ie {
		p.print(p.keyword("IF EXISTS"))
	}

	if name, _ := n.Name(); nodeDefined(name) {
		p.accept(name, d)
	}

	p.print(p.keyword("ON"))

	target, _ := n.Path()
	al, _ := n.ActionList()

	p.accept(target, d)
	p.printAlterActionList(n, al, d)
}

func (p *printer) visitAlterSchemaStatement(n *googlesql.ASTAlterSchemaStatement, d printerData) {
	p.visitAlterStatementBase("ALTER SCHEMA", n, d)
}

func (p *printer) visitAlterTableStatement(n *googlesql.ASTAlterTableStatement, d printerData) {
	p.visitAlterStatementBase("ALTER TABLE", n, d)
}

func (p *printer) visitAlterApproxViewStatement(n *googlesql.ASTAlterApproxViewStatement, d printerData) {
	p.visitAlterStatementBase("ALTER APPROX VIEW", n, d)
}

func (p *printer) visitAlterViewStatement(n *googlesql.ASTAlterViewStatement, d printerData) {
	p.visitAlterStatementBase("ALTER VIEW", n, d)
}

func (p *printer) visitCloneDataSource(n *googlesql.ASTCloneDataSource, d printerData) {
	pe, _ := n.PathExpr()
	fst, _ := n.ForSystemTime()
	wc, _ := n.WhereClause()

	kw := "CLONE"
	if nodeKind(nodeParent(n)) == googlesql.ASTNodeKindAstCloneDataSourceList {
		kw = ""
	}

	p.printTableDataSource(kw, n, pe, fst, wc, d)
}

func (p *printer) visitCopyDataSource(n *googlesql.ASTCopyDataSource, d printerData) {
	pe, _ := n.PathExpr()
	fst, _ := n.ForSystemTime()
	wc, _ := n.WhereClause()
	p.printTableDataSource("COPY", n, pe, fst, wc, d)
}

func (p *printer) printTableDataSource(
	kw string,
	n googlesql.ASTNode,
	pe *googlesql.ASTPathExpression,
	fst *googlesql.ASTForSystemTime,
	wc *googlesql.ASTWhereClause,
	d printerData,
) {
	p.moveBefore(n)

	if kw != "" {
		p.print(p.keyword(kw))
	}

	p.accept(pe, d)
	p.lnacceptAligned(fst, d)
	p.lnacceptAligned(wc, d)
	p.movePast(n)
}

func (p *printer) visitCreateExternalTableStatement(n *googlesql.ASTCreateExternalTableStatement, d printerData) {
	p.moveBefore(n)

	cs := createStatementKeywords(n, false, "EXTERNAL TABLE")
	p.print(p.keyword(cs))

	name, _ := n.Name()
	tel, _ := n.TableElementList()
	co, _ := n.Collate()
	like, _ := n.LikeTableName()
	wpc, _ := n.WithPartitionColumnsClause()
	wcc, _ := n.WithConnectionClause()
	ol, _ := n.OptionsList()

	p.accept(name, d)
	p.lnaccept(tel, d)

	if nodeDefined(co) {
		p.println("")
		p.print(p.keyword("DEFAULT"))
		p.accept(co, d)
	}

	if nodeDefined(like) {
		p.println("")
		p.print(p.keyword("LIKE"))
		p.accept(like, d)
	}

	p.lnaccept(wpc, d)
	p.lnaccept(wcc, d)
	p.lnaccept(ol, d)
}

func (p *printer) printSQLSecurity(sec googlesql.ASTCreateStatementEnums_SqlSecurity) {
	switch sec {
	case googlesql.ASTCreateStatementEnums_SqlSecuritySqlSecurityUnspecified:
		// Nothing.
	case googlesql.ASTCreateStatementEnums_SqlSecuritySqlSecurityDefiner:
		p.println("")
		p.print(p.keyword("SQL SECURITY DEFINER"))
	case googlesql.ASTCreateStatementEnums_SqlSecuritySqlSecurityInvoker:
		p.println("")
		p.print(p.keyword("SQL SECURITY INVOKER"))
	}
}

func (p *printer) visitCreateFunctionStatement(n *googlesql.ASTCreateFunctionStatement, d printerData) {
	pp := p.nest()

	pp.moveBefore(n)
	isAgg, _ := n.IsAggregate()
	pp.print(pp.keyword(createStatementKeywords(n, isAgg, "FUNCTION")))

	fd, _ := n.FunctionDeclaration()
	pp.accept(fd, d)

	if typ, _ := n.ReturnType(); nodeDefined(typ) {
		pp.print(pp.keyword("RETURNS"))

		if isSimpleType(typ) {
			pp.accept(typ, d)
		} else {
			pp.println("")
			pp.incDepth()
			pp.accept(typ, d)
			pp.println("")
			pp.decDepth()
		}
	}

	p.print(pp.unnestLeft())

	sec, _ := n.SqlSecurity()
	p.printSQLSecurity(sec)

	wcc, _ := n.WithConnectionClause()
	body, _ := n.SqlFunctionBody()

	p.lnaccept(wcc, d)
	p.accept(body, d)

	det, _ := n.DeterminismLevel()
	switch det {
	case googlesql.ASTCreateFunctionStmtBaseEnums_DeterminismLevelDeterminismUnspecified:
		// Nothing.
	case googlesql.ASTCreateFunctionStmtBaseEnums_DeterminismLevelDeterministic:
		p.println("")
		p.println(p.keyword("DETERMINISTIC"))
	case googlesql.ASTCreateFunctionStmtBaseEnums_DeterminismLevelNotDeterministic:
		p.println("")
		p.println(p.keyword("NOT DETERMINISTIC"))
	case googlesql.ASTCreateFunctionStmtBaseEnums_DeterminismLevelImmutable:
		p.println("")
		p.println(p.keyword("IMMUTABLE"))
	case googlesql.ASTCreateFunctionStmtBaseEnums_DeterminismLevelStable:
		p.println("")
		p.println(p.keyword("STABLE"))
	case googlesql.ASTCreateFunctionStmtBaseEnums_DeterminismLevelVolatile:
		p.println("")
		p.println(p.keyword("VOLATILE"))
	}

	if lang, _ := n.Language(); nodeDefined(lang) {
		p.println("")
		p.print(p.keyword("LANGUAGE"))
		p.moveBefore(lang)
		p.accept(lang, d)
		p.println("")
	}

	if code, _ := n.Code(); nodeDefined(code) {
		p.print(p.keyword("AS"))
		p.accept(code, d)
	}

	if opt, _ := n.OptionsList(); nodeDefined(opt) {
		p.println("")
		p.accept(opt, d)
	}
}

func (p *printer) visitCreateMaterializedViewStatement(n *googlesql.ASTCreateMaterializedViewStatement, d printerData) {
	p.moveBefore(n)

	obj := "MATERIALIZED VIEW"
	if rec, _ := n.Recursive(); rec {
		obj = "MATERIALIZED RECURSIVE VIEW"
	}

	cs := createStatementKeywords(n, false, obj)
	p.print(p.keyword(cs))

	name, _ := n.Name()
	cwol, _ := n.ColumnWithOptionsList()
	sec, _ := n.SqlSecurity()
	pb, _ := n.PartitionBy()
	cb, _ := n.ClusterBy()
	opt, _ := n.OptionsList()
	q, _ := n.Query()
	rep, _ := n.ReplicaSource()

	p.accept(name, d)
	p.lnaccept(cwol, d)
	p.printSQLSecurity(sec)
	p.printPartitionAndClusterBy(pb, cb, d)

	if nodeDefined(opt) {
		p.println("")
		p.accept(opt, d)
	}

	if nodeDefined(rep) {
		p.println("")
		p.print(p.keyword("AS REPLICA OF"))
		p.accept(rep, d)
	}

	if nodeDefined(q) {
		p.println("")
		p.println(p.keyword("AS"))
		p.accept(q, d)
	}
}

func (p *printer) visitCreateModelStatement(n *googlesql.ASTCreateModelStatement, d printerData) {
	p.moveBefore(n)

	cs := createStatementKeywords(n, false, "MODEL")
	p.print(p.keyword(cs))

	name, _ := n.Name()
	p.accept(name, d)

	if ioc, _ := n.InputOutputClause(); nodeDefined(ioc) {
		p.printInputOutputClause(ioc, d)
	}

	if tc, _ := n.TransformClause(); nodeDefined(tc) {
		sl, _ := tc.SelectList()

		p.println("")
		p.println(p.keyword("TRANSFORM") + " (")
		p.incDepth()
		p.accept(sl, d)
		p.println("")
		p.decDepth()
		p.print(")")
	}

	if wcc, _ := n.WithConnectionClause(); nodeDefined(wcc) {
		p.lnaccept(wcc, d)
	}

	if opt, _ := n.OptionsList(); nodeDefined(opt) {
		p.println("")
		p.accept(opt, d)
	}

	if q, _ := n.Query(); nodeDefined(q) {
		p.println("")
		p.println(p.keyword("AS"))
		p.accept(q, d)
	} else if aql, _ := n.AliasedQueryList(); nodeDefined(aql) {
		p.println("")
		p.println(p.keyword("AS") + " (")
		p.incDepth()

		for i, aq := range selectChildrenOfType[*googlesql.ASTAliasedQuery](aql) {
			if i > 0 {
				p.println(",")
			}

			p.accept(aq, d)
		}

		p.println("")
		p.decDepth()
		p.print(")")
	}

	p.movePast(n)
}

func (p *printer) printInputOutputClause(n *googlesql.ASTInputOutputClause, d printerData) {
	p.moveBefore(n)

	if in, _ := n.Input(); nodeDefined(in) {
		p.println("")
		p.print(p.keyword("INPUT"))
		p.printModelElementList(in, d)
	}

	if out, _ := n.Output(); nodeDefined(out) {
		p.println("")
		p.print(p.keyword("OUTPUT"))
		p.printModelElementList(out, d)
	}

	p.movePast(n)
}

func (p *printer) printModelElementList(tel *googlesql.ASTTableElementList, d printerData) {
	if len(selectChildrenOfType[googlesql.ASTTableElementNode](tel)) == 0 {
		p.moveBefore(tel)
		p.print(" ()")
		p.movePast(tel)

		return
	}

	p.accept(tel, d)
}

func (p *printer) visitCreateProcedureStatement(n *googlesql.ASTCreateProcedureStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword(createStatementKeywords(n, false, "PROCEDURE")))

	name, _ := n.Name()
	params, _ := n.Parameters()
	extSec, _ := n.ExternalSecurity()
	wcc, _ := n.WithConnectionClause()
	opt, _ := n.OptionsList()
	body, _ := n.Body()

	p.accept(name, d)

	if nodeDefined(params) {
		entries := selectChildrenOfType[*googlesql.ASTFunctionParameter](params)
		simple := areSimpleFunctionParameters(entries)

		if simple {
			p.print(strings.TrimLeft(p.toString(params, d), "\v"))
		} else {
			p.lnaccept(params, d)
		}
	}

	switch extSec {
	case googlesql.ASTCreateStatementEnums_SqlSecuritySqlSecurityUnspecified:
		// Nothing.
	case googlesql.ASTCreateStatementEnums_SqlSecuritySqlSecurityDefiner:
		p.println("")
		p.print(p.keyword("EXTERNAL SECURITY DEFINER"))
	case googlesql.ASTCreateStatementEnums_SqlSecuritySqlSecurityInvoker:
		p.println("")
		p.print(p.keyword("EXTERNAL SECURITY INVOKER"))
	}

	p.lnaccept(wcc, d)
	p.lnaccept(opt, d)
	p.lnaccept(body, d)

	if lang, _ := n.Language(); nodeDefined(lang) {
		p.println("")
		p.moveBefore(lang)
		p.print(p.keyword("LANGUAGE"))

		langName, _ := lang.GetAsString()
		if quoted, _ := lang.IsQuoted(); quoted {
			langName = "`" + langName + "`"
		}

		p.print(p.identifier(langName))
		p.movePast(lang)
	}

	if code, _ := n.Code(); nodeDefined(code) {
		p.println("")
		p.print(p.keyword("AS"))
		p.accept(code, d)
	}

	p.movePast(n)
}

func (p *printer) visitCreateRowAccessPolicyStatement(n *googlesql.ASTCreateRowAccessPolicyStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword(createStatementKeywords(n, false, "ROW ACCESS POLICY")))

	name, _ := n.Name()
	target, _ := n.TargetPath()
	gt, _ := n.GrantTo()
	fu, _ := n.FilterUsing()

	if nodeDefined(name) {
		p.accept(name, d)
	}

	p.println("")
	p.print(p.keyword("ON"))
	p.accept(target, d)
	p.lnaccept(gt, d)
	p.lnaccept(fu, d)
}

func (p *printer) visitCreateSchemaStatement(n *googlesql.ASTCreateSchemaStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword(createStatementKeywords(n, false, "SCHEMA")))

	name, _ := n.Name()
	p.accept(name, d)

	if c, _ := n.Collate(); nodeDefined(c) {
		p.println("")
		p.print(p.keyword("DEFAULT"))
		p.accept(c, d)
	}

	if opt, _ := n.OptionsList(); nodeDefined(opt) {
		p.println("")
		p.accept(opt, d)
	}

	p.movePast(n)
}

func (p *printer) visitCreateSnapshotTableStatement(n *googlesql.ASTCreateSnapshotTableStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword(createStatementKeywords(n, false, "SNAPSHOT TABLE")))

	name, _ := n.Name()
	cds, _ := n.CloneDataSource()
	opt, _ := n.OptionsList()

	p.accept(name, d)
	p.acceptClause(cds, d)
	p.acceptClause(opt, d)
	p.movePast(n)
}

func (p *printer) printPartitionAndClusterBy(
	pb *googlesql.ASTPartitionBy,
	cb *googlesql.ASTClusterBy,
	d printerData,
) {
	if !nodeDefined(pb) && !nodeDefined(cb) {
		return
	}

	pp := p.nest()

	if nodeDefined(pb) {
		pp.accept(pb, d)
	}

	if nodeDefined(cb) {
		if nodeDefined(pb) {
			pp.println("")
		}

		pp.accept(cb, d)
	}

	p.println("")
	p.print(pp.unnest())
}

func (p *printer) visitCreateTableStatement(n *googlesql.ASTCreateTableStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword(createStatementKeywords(n, false, "TABLE")))

	name, _ := n.Name()
	tel, _ := n.TableElementList()
	copyDS, _ := n.CopyDataSource()
	cloneDS, _ := n.CloneDataSource()
	like, _ := n.LikeTableName()
	co, _ := n.Collate()
	pb, _ := n.PartitionBy()
	cb, _ := n.ClusterBy()
	wcc, _ := n.WithConnectionClause()
	opt, _ := n.OptionsList()
	q, _ := n.Query()

	p.accept(name, d)
	p.accept(tel, d)
	p.lnaccept(copyDS, d)
	p.lnaccept(cloneDS, d)

	if nodeDefined(like) {
		p.println("")
		p.print(p.keyword("LIKE"))
		p.accept(like, d)
	}

	if nodeDefined(co) {
		p.println("")
		p.print(p.keyword("DEFAULT"))
		p.accept(co, d)
	}

	p.printPartitionAndClusterBy(pb, cb, d)
	p.lnaccept(wcc, d)

	if nodeDefined(opt) {
		p.println("")
		p.accept(opt, d)
	}

	if nodeDefined(q) {
		p.println("")
		p.println(p.keyword("AS"))
		p.accept(q, d)
	}
}

func (p *printer) visitCreateTableFunctionStatement(n *googlesql.ASTCreateTableFunctionStatement, d printerData) {
	p.moveBefore(n)

	pp := p.nest()
	pp.print(pp.keyword(createStatementKeywords(n, false, "TABLE FUNCTION")))
	p.print(pp.unnestLeft())

	fd, _ := n.FunctionDeclaration()
	rtvf, _ := n.ReturnTvfSchema()
	sec, _ := n.SqlSecurity()
	opt, _ := n.OptionsList()
	q, _ := n.Query()

	p.accept(fd, d)
	p.lnaccept(rtvf, d)
	p.printSQLSecurity(sec)
	p.lnaccept(opt, d)

	if nodeDefined(q) {
		p.println("")
		p.println(p.keyword("AS"))
		p.lnaccept(q, d)
	}
}

func (p *printer) visitCreateApproxViewStatement(n *googlesql.ASTCreateApproxViewStatement, d printerData) {
	obj := "APPROX VIEW"
	if rec, _ := n.Recursive(); rec {
		obj = "APPROX RECURSIVE VIEW"
	}

	p.visitCreateViewBase(n, obj, d)
}

func (p *printer) visitCreateViewStatement(n *googlesql.ASTCreateViewStatement, d printerData) {
	obj := "VIEW"
	if rec, _ := n.Recursive(); rec {
		obj = "RECURSIVE VIEW"
	}

	p.visitCreateViewBase(n, obj, d)
}

func (p *printer) visitCreateViewBase(n googlesql.ASTCreateViewStatementBaseNode, obj string, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword(createStatementKeywords(n, false, obj)))

	name, _ := n.Name()
	cwol, _ := n.ColumnWithOptionsList()
	sec, _ := n.SqlSecurity()
	opt, _ := n.OptionsList()
	q, _ := n.Query()

	p.accept(name, d)
	p.lnaccept(cwol, d)
	p.printSQLSecurity(sec)
	p.lnaccept(opt, d)

	if nodeDefined(q) {
		p.println("")
		p.println(p.keyword("AS"))
		p.accept(q, d)
	}
}

func (p *printer) visitColumnWithOptionsList(n *googlesql.ASTColumnWithOptionsList, d printerData) {
	p.moveBefore(n)

	cols := selectChildrenOfType[*googlesql.ASTColumnWithOptions](n)
	simple := len(cols) <= 4 && allMatch(cols, func(c *googlesql.ASTColumnWithOptions) bool {
		ol, _ := c.OptionsList()

		return !nodeDefined(ol)
	})

	printParenList(p, cols, simple, d)
	p.movePast(n)
}

func (p *printer) visitColumnWithOptions(n *googlesql.ASTColumnWithOptions, d printerData) {
	p.moveBefore(n)
	name, _ := n.Name()
	ol, _ := n.OptionsList()

	p.accept(name, d)
	p.accept(ol, d)
	p.movePast(n)
}

func (p *printer) visitExportDataStatement(n *googlesql.ASTExportDataStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("EXPORT DATA"))

	wcc, _ := n.WithConnectionClause()
	opt, _ := n.OptionsList()
	q, _ := n.Query()

	p.lnaccept(wcc, d)
	p.lnaccept(opt, d)

	if nodeDefined(q) {
		p.println("")
		p.println(p.keyword("AS"))
		p.accept(q, d)
	}

	p.movePast(n)
}

func (p *printer) visitAuxLoadDataStatement(n *googlesql.ASTAuxLoadDataStatement, d printerData) {
	p.moveBefore(n)

	im, _ := n.InsertionMode()
	isTemp, _ := n.IsTempTable()

	var kw strings.Builder
	kw.WriteString("LOAD DATA")

	if im == googlesql.ASTAuxLoadDataStatementEnums_InsertionModeOverwrite {
		kw.WriteString(" OVERWRITE")
	} else {
		kw.WriteString(" INTO")
	}

	if isTemp {
		kw.WriteString(" TEMPORARY TABLE")
	}

	p.print(p.keyword(kw.String()))

	name, _ := n.Name()
	tel, _ := n.TableElementList()
	ldp, _ := n.LoadDataPartitionsClause()
	pb, _ := n.PartitionBy()
	cb, _ := n.ClusterBy()
	opt, _ := n.OptionsList()
	ff, _ := n.FromFiles()
	wpc, _ := n.WithPartitionColumnsClause()
	wcc, _ := n.WithConnectionClause()

	p.accept(name, d)
	p.accept(tel, d)
	p.lnaccept(ldp, d)
	p.printPartitionAndClusterBy(pb, cb, d)
	p.lnaccept(opt, d)

	if nodeDefined(ff) {
		ffOpt, _ := ff.OptionsList()

		p.println("")
		p.printOptionsListBody(ffOpt, "FROM FILES", d)
	}

	p.lnaccept(wpc, d)
	p.lnaccept(wcc, d)
	p.movePast(n)
}

func (p *printer) printPrivileges(privs *googlesql.ASTPrivileges, d printerData) {
	if isAll, _ := privs.IsAllPrivileges(); isAll {
		p.print(p.keyword("ALL PRIVILEGES"))

		return
	}

	items := selectChildrenOfType[*googlesql.ASTPrivilege](privs)
	for i, item := range items {
		if i > 0 {
			p.print(",")
		}

		act, _ := item.PrivilegeAction()
		paths, _ := item.Paths()

		p.accept(act, d)

		if nodeDefined(paths) {
			p.moveBefore(paths)
			p.print("(")

			for j, path := range selectChildrenOfType[*googlesql.ASTPathExpression](paths) {
				if j > 0 {
					p.print(",")
				}

				p.accept(path, d)
			}

			p.print(")")
			p.movePast(paths)
		}
	}
}

func (p *printer) visitGrantStatement(n *googlesql.ASTGrantStatement, d printerData) {
	privs, _ := n.Privileges()
	target, _ := n.TargetPath()
	grantees, _ := n.GranteeList()
	p.printPrivilegeDCL("GRANT", "TO", n, privs, target, grantees, d)
}

func (p *printer) visitRevokeStatement(n *googlesql.ASTRevokeStatement, d printerData) {
	privs, _ := n.Privileges()
	target, _ := n.TargetPath()
	grantees, _ := n.GranteeList()
	p.printPrivilegeDCL("REVOKE", "FROM", n, privs, target, grantees, d)
}

func (p *printer) printPrivilegeDCL(
	stmtKw string,
	recipientKw string,
	n googlesql.ASTNode,
	privs *googlesql.ASTPrivileges,
	target *googlesql.ASTPathExpression,
	grantees *googlesql.ASTGranteeList,
	d printerData,
) {
	p.moveBefore(n)
	p.print(p.keyword(stmtKw))

	if nodeDefined(privs) {
		p.printPrivileges(privs, d)
	}

	p.print(p.keyword("ON"))

	for _, part := range selectChildrenOfType[*googlesql.ASTIdentifier](n) {
		p.print(p.rawKeywordIdent(part))
	}

	p.accept(target, d)
	p.print(p.keyword(recipientKw))
	p.accept(grantees, d)
	p.movePast(n)
}

func (p *printer) visitRevokeFromClause(n *googlesql.ASTRevokeFromClause, d printerData) {
	p.moveBefore(n)

	if all, _ := n.IsRevokeFromAll(); all {
		p.print(p.keyword("REVOKE FROM ALL"))
	} else {
		gl, _ := n.RevokeFromList()

		p.print(p.keyword("REVOKE FROM") + " (")
		p.accept(gl, d)
		p.print(")")
	}

	p.movePast(n)
}

func (p *printer) visitDropAllRowAccessPoliciesStatement(
	n *googlesql.ASTDropAllRowAccessPoliciesStatement,
	d printerData,
) {
	p.moveBefore(n)
	tbl, _ := n.TableName()

	p.print(p.keyword("DROP ALL ROW ACCESS POLICIES ON"))
	p.accept(tbl, d)
	p.movePast(n)
}

func (p *printer) visitDropColumnAction(n *googlesql.ASTDropColumnAction, d printerData) {
	ie, _ := n.IsIfExists()
	cn, _ := n.ColumnName()
	p.printDropNamedObject(n, "COLUMN", ie, cn, nil, d)
}

func (p *printer) visitDropConstraintAction(n *googlesql.ASTDropConstraintAction, d printerData) {
	ie, _ := n.IsIfExists()
	cn, _ := n.ConstraintName()
	p.printDropNamedObject(n, "CONSTRAINT", ie, cn, nil, d)
}

func (p *printer) visitDropEntityStatement(n *googlesql.ASTDropEntityStatement, d printerData) {
	ie, _ := n.IsIfExists()
	target, _ := n.Name()
	et, _ := n.EntityType()
	obj := "ENTITY"

	if nodeDefined(et) {
		obj = p.rawIdentifier(et)
	}

	p.printDropNamedObject(n, obj, ie, target, nil, d)
}

func (p *printer) visitDropFunctionStatement(n *googlesql.ASTDropFunctionStatement, d printerData) {
	p.moveBefore(n)

	ie, _ := n.IsIfExists()
	name, _ := n.Name()
	params, _ := n.Parameters()

	p.print(p.keyword(dropKeyword(ie, "FUNCTION")))
	p.accept(name, d)

	if nodeDefined(params) {
		if isSimpleFunctionParameters(params, p.fmt.opts.MaxParamsForSingleLineFunction) {
			p.print(strings.TrimLeft(p.toString(params, d), "\v"))
		} else {
			p.lnaccept(params, d)
		}
	}

	p.movePast(n)
}

func (p *printer) visitDropMaterializedViewStatement(n *googlesql.ASTDropMaterializedViewStatement, d printerData) {
	ie, _ := n.IsIfExists()
	name, _ := n.Name()
	p.printDropNamedObject(n, "MATERIALIZED VIEW", ie, name, nil, d)
}

func (p *printer) visitDropPrimaryKeyAction(n *googlesql.ASTDropPrimaryKeyAction, _ printerData) {
	p.moveBefore(n)
	ie, _ := n.IsIfExists()
	p.print(p.keyword(dropKeyword(ie, "PRIMARY KEY")))
	p.movePast(n)
}

func (p *printer) visitDropPrivilegeRestrictionStatement(
	n *googlesql.ASTDropPrivilegeRestrictionStatement,
	d printerData,
) {
	p.moveBefore(n)
	ie, _ := n.IsIfExists()
	p.print(p.keyword(dropKeyword(ie, "PRIVILEGE RESTRICTION")))
	p.print(p.keyword("ON"))

	if privs, _ := n.Privileges(); nodeDefined(privs) {
		p.printPrivileges(privs, d)
	}

	p.print(p.keyword("ON"))

	if ot, _ := n.ObjectType(); nodeDefined(ot) {
		p.print(p.rawKeywordIdent(ot))
	}

	np, _ := n.NamePath()
	p.accept(np, d)
	p.movePast(n)
}

func (p *printer) visitDropRowAccessPolicyStatement(n *googlesql.ASTDropRowAccessPolicyStatement, d printerData) {
	ie, _ := n.IsIfExists()
	name, _ := n.Name()
	tbl, _ := n.TableName()
	p.printDropNamedObject(n, "ROW ACCESS POLICY", ie, name, tbl, d)
}

func (p *printer) visitDropSearchIndexStatement(n *googlesql.ASTDropSearchIndexStatement, d printerData) {
	ie, _ := n.IsIfExists()
	name, _ := n.Name()
	tbl, _ := n.TableName()
	p.printDropNamedObject(n, "SEARCH INDEX", ie, name, tbl, d)
}

func (p *printer) visitDropSnapshotTableStatement(n *googlesql.ASTDropSnapshotTableStatement, d printerData) {
	ie, _ := n.IsIfExists()
	name, _ := n.Name()
	p.printDropNamedObject(n, "SNAPSHOT TABLE", ie, name, nil, d)
}

func (p *printer) visitDropTableFunctionStatement(n *googlesql.ASTDropTableFunctionStatement, d printerData) {
	ie, _ := n.IsIfExists()
	name, _ := n.Name()
	p.printDropNamedObject(n, "TABLE FUNCTION", ie, name, nil, d)
}

func (p *printer) visitDropStatement(n *googlesql.ASTDropStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword(dropStatementKeyword(n)))

	target, _ := n.Name()
	p.accept(target, d)

	dm, _ := n.DropMode()
	switch dm {
	case googlesql.ASTDropStatementEnums_DropModeDropModeUnspecified:
		// Nothing.
	case googlesql.ASTDropStatementEnums_DropModeRestrict:
		p.print(p.keyword("RESTRICT"))
	case googlesql.ASTDropStatementEnums_DropModeCascade:
		p.print(p.keyword("CASCADE"))
	}

	p.movePast(n)
}

func (p *printer) visitUndropStatement(n *googlesql.ASTUndropStatement, d printerData) {
	p.moveBefore(n)
	sok, _ := n.SchemaObjectKind()
	ine, _ := n.IsIfNotExists()
	name, _ := n.Name()
	fst, _ := n.ForSystemTime()
	opt, _ := n.OptionsList()

	kw := "UNDROP" + schemaObjectKindString(sok)
	if ine {
		kw += " IF NOT EXISTS"
	}

	p.print(p.keyword(kw))
	p.accept(name, d)
	p.accept(fst, d)
	p.lnacceptAligned(opt, d)
	p.movePast(n)
}

func dropKeyword(ifExists bool, object string) string {
	if ifExists {
		return "DROP " + object + " IF EXISTS"
	}

	return "DROP " + object
}

func dropStatementKeyword(n *googlesql.ASTDropStatement) string {
	sok, _ := n.SchemaObjectKind()
	ie, _ := n.IsIfExists()
	kw := "DROP" + schemaObjectKindString(sok)

	if ie {
		kw += " IF EXISTS"
	}

	return kw
}

func schemaObjectKindString(sok googlesql.SchemaObjectKind) string {
	switch sok {
	case googlesql.SchemaObjectKindKAggregateFunction:
		return " AGGREGATE FUNCTION"
	case googlesql.SchemaObjectKindKApproxView:
		return " APPROX VIEW"
	case googlesql.SchemaObjectKindKConnection:
		return " CONNECTION"
	case googlesql.SchemaObjectKindKConstant:
		return " CONSTANT"
	case googlesql.SchemaObjectKindKDatabase:
		return " DATABASE"
	case googlesql.SchemaObjectKindKExternalTable:
		return " EXTERNAL TABLE"
	case googlesql.SchemaObjectKindKFunction:
		return " FUNCTION"
	case googlesql.SchemaObjectKindKIndex:
		return " INDEX"
	case googlesql.SchemaObjectKindKMaterializedView:
		return " MATERIALIZED VIEW"
	case googlesql.SchemaObjectKindKModel:
		return " MODEL"
	case googlesql.SchemaObjectKindKProcedure:
		return " PROCEDURE"
	case googlesql.SchemaObjectKindKSchema:
		return " SCHEMA"
	case googlesql.SchemaObjectKindKSequence:
		return " SEQUENCE"
	case googlesql.SchemaObjectKindKTable:
		return " TABLE"
	case googlesql.SchemaObjectKindKTableFunction:
		return " TABLE FUNCTION"
	case googlesql.SchemaObjectKindKView:
		return " VIEW"
	case googlesql.SchemaObjectKindKSnapshotTable:
		return " SNAPSHOT TABLE"
	case googlesql.SchemaObjectKindKPropertyGraph:
		return " PROPERTY GRAPH"
	case googlesql.SchemaObjectKindKExternalSchema:
		return " EXTERNAL SCHEMA"
	default:
		return " <UNKNOWN SCHEMA OBJECT>"
	}
}

func (p *printer) visitFilterUsingClause(n *googlesql.ASTFilterUsingClause, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("FILTER USING") + " (")

	expr, _ := n.Predicate()
	simple := p.isSimpleExpr(expr)

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

func (p *printer) visitCheckConstraint(n *googlesql.ASTCheckConstraint, d printerData) {
	p.moveBefore(n)

	cn, _ := n.ConstraintName()
	p.printConstraintName(cn, nodeParent(n), d)

	expr, _ := n.Expression()

	p.print(p.keyword("CHECK") + " (")
	p.accept(expr, d)
	p.print(")")

	if enf, _ := n.IsEnforced(); enf {
		p.print(p.keyword("ENFORCED"))
	} else {
		p.print(p.keyword("NOT ENFORCED"))
	}

	if ol, _ := n.OptionsList(); nodeDefined(ol) {
		p.accept(ol, d)
	}

	p.movePast(n)
}

func (p *printer) visitForeignKey(n *googlesql.ASTForeignKey, d printerData) {
	p.moveBefore(n)

	cn, _ := n.ConstraintName()
	p.printConstraintName(cn, nodeParent(n), d)

	cl, _ := n.ColumnList()
	ref, _ := n.Reference()

	p.print(p.keyword("FOREIGN KEY") + " ")
	p.accept(cl, d)
	p.lnaccept(ref, d)

	if ol, _ := n.OptionsList(); nodeDefined(ol) {
		p.accept(ol, d)
	}

	p.movePast(n)
}

func (p *printer) visitForeignKeyReference(n *googlesql.ASTForeignKeyReference, d printerData) {
	p.moveBefore(n)

	tbl, _ := n.TableName()
	cl, _ := n.ColumnList()
	match, _ := n.Match()
	actions, _ := n.Actions()
	enf, _ := n.Enforced()

	p.print(p.keyword("REFERENCES"))
	p.accept(tbl, d)
	p.accept(cl, d)

	switch match {
	case googlesql.ASTForeignKeyReferenceEnums_MatchSimple:
		// Default match mode.
	case googlesql.ASTForeignKeyReferenceEnums_MatchFull:
		p.print(p.keyword("MATCH FULL"))
	case googlesql.ASTForeignKeyReferenceEnums_MatchNotDistinct:
		p.print(p.keyword("MATCH NOT DISTINCT"))
	}

	if nodeDefined(actions) {
		if upd, _ := actions.UpdateAction(); upd != googlesql.ASTForeignKeyActionsEnums_ActionNoAction {
			p.print(p.keyword("ON UPDATE " + foreignKeyActionKeyword(upd)))
		}

		if del, _ := actions.DeleteAction(); del != googlesql.ASTForeignKeyActionsEnums_ActionNoAction {
			p.print(p.keyword("ON DELETE " + foreignKeyActionKeyword(del)))
		}
	}

	if enf {
		p.print(p.keyword("ENFORCED"))
	} else {
		p.print(p.keyword("NOT ENFORCED"))
	}
}

func foreignKeyActionKeyword(act googlesql.ASTForeignKeyActionsEnums_Action) string {
	switch act {
	case googlesql.ASTForeignKeyActionsEnums_ActionRestrict:
		return "RESTRICT"
	case googlesql.ASTForeignKeyActionsEnums_ActionCascade:
		return "CASCADE"
	case googlesql.ASTForeignKeyActionsEnums_ActionSetNull:
		return "SET NULL"
	default:
		return "NO ACTION"
	}
}

func (p *printer) visitFunctionDeclaration(n *googlesql.ASTFunctionDeclaration, d printerData) {
	p.moveBefore(n)

	fnName, _ := n.Name()
	fnParams, _ := n.Parameters()
	name := p.function(p.toString(fnName, d))
	params := p.toString(fnParams, d)

	if isSimpleFunctionParameters(fnParams, p.fmt.opts.MaxParamsForSingleLineFunction) {
		p.print(name + params)
	} else {
		p.println(name)
		p.print(params)
	}

	p.println("")
}

func (p *printer) visitFunctionParameter(n *googlesql.ASTFunctionParameter, d printerData) {
	p.moveBefore(n)

	mode, _ := n.ProcedureParameterMode()
	switch mode {
	case googlesql.ASTFunctionParameterEnums_ProcedureParameterModeNotSet:
		// Nothing.
	case googlesql.ASTFunctionParameterEnums_ProcedureParameterModeIn:
		p.print(p.keyword("IN"))
	case googlesql.ASTFunctionParameterEnums_ProcedureParameterModeOut:
		p.print(p.keyword("OUT"))
	case googlesql.ASTFunctionParameterEnums_ProcedureParameterModeInout:
		p.print(p.keyword("INOUT"))
	}

	name, _ := n.Name()
	if nodeDefined(name) {
		if !d.functionParamsSimple && d.procedureParams {
			p.acceptNested(name, d)
		} else {
			p.accept(name, d)
		}
	}

	if t, _ := n.Type(); nodeDefined(t) {
		p.acceptNested(t, d)
	} else if tpt, _ := n.TemplatedParameterType(); nodeDefined(tpt) {
		p.acceptNested(tpt, d)
	} else if tvf, _ := n.TvfSchema(); nodeDefined(tvf) {
		p.acceptNested(tvf, d)
	}

	if alias, _ := n.Alias(); nodeDefined(alias) {
		p.accept(alias, d)
	}

	if dv, _ := n.DefaultValue(); nodeDefined(dv) {
		p.print(p.keyword("DEFAULT"))
		p.acceptNested(dv, d)
	}

	if na, _ := n.IsNotAggregate(); na {
		p.print(p.keyword("NOT AGGREGATE"))
	}

	p.movePast(n)
}

func (p *printer) visitFunctionParameters(n *googlesql.ASTFunctionParameters, d printerData) {
	entries := selectChildrenOfType[*googlesql.ASTFunctionParameter](n)
	simple := isSimpleFunctionParameters(n, p.fmt.opts.MaxParamsForSingleLineFunction)
	d.functionParamsSimple = simple

	if parent := nodeParent(n); nodeDefined(parent) {
		d.procedureParams = nodeKind(parent) == googlesql.ASTNodeKindAstCreateProcedureStatement
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

func (p *printer) visitGranteeList(n *googlesql.ASTGranteeList, d printerData) {
	p.moveBefore(n)

	exprs := selectChildrenOfType[googlesql.ASTExpressionNode](n)
	if len(exprs) == 0 {
		p.movePast(n)

		return
	}

	simple := len(exprs) == 1

	if simple {
		p.print(p.toString(exprs[0], d))
	} else {
		var prev googlesql.ASTNode

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

func (p *printer) visitGrantToClause(n *googlesql.ASTGrantToClause, d printerData) {
	p.moveBefore(n)
	gl, _ := n.GranteeList()

	p.print(p.keyword("GRANT TO") + " (")
	p.accept(gl, d)
	p.print(")")
}

func (p *printer) visitPrimaryKey(n *googlesql.ASTPrimaryKey, d printerData) {
	p.moveBefore(n)

	cn, _ := n.ConstraintName()
	p.printConstraintName(cn, nodeParent(n), d)

	el, _ := n.ElementList()
	enf, _ := n.Enforced()
	ol, _ := n.OptionsList()

	p.print(p.keyword("PRIMARY KEY") + " ")
	p.accept(el, d)

	if enf {
		_, scanStart := nodeByteRange(el)
		_, scanEnd := nodeByteRange(n)
		if nodeDefined(ol) {
			scanEnd, _ = nodeByteRange(ol)
		}

		if containsKeywordToken(p.viewErasedInput(scanStart, scanEnd), "ENFORCED") {
			p.print(p.keyword("ENFORCED"))
		}
	} else {
		p.print(p.keyword("NOT ENFORCED"))
	}

	if nodeDefined(ol) {
		p.accept(ol, d)
	}
}

func (p *printer) visitPrimaryKeyElementList(n *googlesql.ASTPrimaryKeyElementList, d printerData) {
	p.moveBefore(n)
	elems := selectChildrenOfType[*googlesql.ASTPrimaryKeyElement](n)
	printParenList(p, elems, len(elems) <= 4, d)
	p.movePast(n)
}

func (p *printer) visitPrimaryKeyElement(n *googlesql.ASTPrimaryKeyElement, d printerData) {
	p.moveBefore(n)
	col, _ := n.Column()
	spec, _ := n.OrderingSpec()
	no, _ := n.NullOrder()

	p.accept(col, d)
	p.printOrderingSpec(spec, no, d)
	p.movePast(n)
}

func (p *printer) visitPrimaryKeyColumnAttribute(n *googlesql.ASTPrimaryKeyColumnAttribute, _ printerData) {
	p.moveBefore(n)
	p.print(p.keyword("PRIMARY KEY"))

	enf, _ := n.Enforced()
	if !enf {
		p.print(p.keyword("NOT ENFORCED"))
	} else if containsKeywordToken(p.nodeErasedInput(n), "ENFORCED") {
		p.print(p.keyword("ENFORCED"))
	}

	p.movePast(n)
}

func (p *printer) visitRenameColumnAction(n *googlesql.ASTRenameColumnAction, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("RENAME COLUMN"))

	if ie, _ := n.IsIfExists(); ie {
		p.print(p.keyword("IF EXISTS"))
	}

	cn, _ := n.ColumnName()
	ncn, _ := n.NewColumnName()

	p.accept(cn, d)
	p.print(p.keyword("TO"))
	p.accept(ncn, d)
}

func (p *printer) visitRenameToClause(n *googlesql.ASTRenameToClause, d printerData) {
	p.moveBefore(n)
	nn, _ := n.NewName()

	p.print(p.keyword("RENAME TO"))
	p.accept(nn, d)
}

func (p *printer) visitSetCollateClause(n *googlesql.ASTSetCollateClause, d printerData) {
	p.moveBefore(n)
	co, _ := n.Collate()

	p.print(p.keyword("SET DEFAULT"))
	p.accept(co, d)
}

func (p *printer) visitSetOptionsAction(n *googlesql.ASTSetOptionsAction, d printerData) {
	p.moveBefore(n)
	ol, _ := n.OptionsList()

	p.print(p.keyword("SET"))
	p.accept(ol, d)
}

func (p *printer) visitSQLFunctionBody(n *googlesql.ASTSqlFunctionBody, d printerData) {
	p.moveBefore(n)
	expr, _ := n.Expression()

	p.println("")
	p.println(p.keyword("AS") + " (")
	p.incDepth()
	p.accept(expr, d)
	p.println("")
	p.decDepth()
	p.movePast(n)
	p.println(")")
}

func createStatementKeywords(n googlesql.ASTCreateStatementNode, agg bool, object string) string {
	var b strings.Builder

	b.Grow(48)
	b.WriteString("CREATE ")

	if orRep, _ := n.IsOrReplace(); orRep {
		b.WriteString("OR REPLACE ")
	}

	scope, _ := n.Scope()
	switch scope {
	case googlesql.ASTCreateStatementEnums_ScopeDefaultScope:
		// Nothing.
	case googlesql.ASTCreateStatementEnums_ScopePrivate:
		b.WriteString("PRIVATE ")
	case googlesql.ASTCreateStatementEnums_ScopePublic:
		b.WriteString("PUBLIC ")
	case googlesql.ASTCreateStatementEnums_ScopeTemporary:
		b.WriteString("TEMPORARY ")
	}

	if agg {
		b.WriteString("AGGREGATE ")
	}

	b.WriteString(object)

	if ine, _ := n.IsIfNotExists(); ine {
		b.WriteString(" IF NOT EXISTS")
	}

	return b.String()
}

func (p *printer) visitNotNullColumnAttribute(n *googlesql.ASTNotNullColumnAttribute, _ printerData) {
	p.moveBefore(n)
	p.print(p.keyword("NOT NULL"))
	p.movePast(n)
}

func (p *printer) visitTableConstraint(n *googlesql.ASTTableConstraint, d printerData) {
	p.moveBefore(n)
	cn, _ := n.ConstraintName()
	p.accept(cn, d)
}

func (p *printer) visitWithConnectionClause(n *googlesql.ASTWithConnectionClause, d printerData) {
	p.moveBefore(n)

	kw := "WITH"
	parent := nodeParent(n)

	if nodeDefined(parent) {
		switch fn := parent.(type) {
		case *googlesql.ASTCreateFunctionStatement:
			if rem, _ := fn.IsRemote(); rem {
				kw = "REMOTE WITH"
			}
		case *googlesql.ASTCreateModelStatement:
			if rem, _ := fn.IsRemote(); rem {
				kw = "REMOTE WITH"
			}
		}
	}

	cc, _ := n.ConnectionClause()

	p.print(p.keyword(kw))
	p.accept(cc, d)
}

func (p *printer) visitWithPartitionColumnsClause(n *googlesql.ASTWithPartitionColumnsClause, d printerData) {
	p.moveBefore(n)
	tel, _ := n.TableElementList()

	p.print(p.keyword("WITH PARTITION COLUMNS"))
	p.accept(tel, d)
}

func (p *printer) visitGeneratedColumnInfo(n *googlesql.ASTGeneratedColumnInfo, d printerData) {
	p.moveBefore(n)

	gm, _ := n.GeneratedMode()
	modeKw := "GENERATED ALWAYS AS"

	if gm == googlesql.ASTGeneratedColumnInfoEnums_GeneratedModeByDefault {
		modeKw = "GENERATED BY DEFAULT AS"
	}

	if expr, _ := n.Expression(); nodeDefined(expr) {
		p.print(p.keyword(modeKw) + " (")
		p.accept(expr, d)
		p.print(")")
	} else if ic, _ := n.IdentityColumnInfo(); nodeDefined(ic) {
		p.print(p.keyword(modeKw))
		p.accept(ic, d)
	}

	sm, _ := n.StoredMode()
	switch sm {
	case googlesql.ASTGeneratedColumnInfoEnums_StoredModeNonStored:
		// Default non-stored.
	case googlesql.ASTGeneratedColumnInfoEnums_StoredModeStored:
		p.print(p.keyword("STORED"))
	case googlesql.ASTGeneratedColumnInfoEnums_StoredModeStoredVolatile:
		p.print(p.keyword("STORED VOLATILE"))
	}

	p.movePast(n)
}

func (p *printer) visitIdentityColumnInfo(n *googlesql.ASTIdentityColumnInfo, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("IDENTITY") + "(")

	if sw, _ := n.StartWithValue(); nodeDefined(sw) {
		p.print(p.keyword("START WITH"))
		p.accept(sw, d)
	}

	if inc, _ := n.IncrementByValue(); nodeDefined(inc) {
		p.print(p.keyword("INCREMENT BY"))
		p.accept(inc, d)
	}

	if mx, _ := n.MaxValue(); nodeDefined(mx) {
		p.print(p.keyword("MAXVALUE"))
		p.accept(mx, d)
	}

	if mn, _ := n.MinValue(); nodeDefined(mn) {
		p.print(p.keyword("MINVALUE"))
		p.accept(mn, d)
	}

	if cyc, _ := n.CyclingEnabled(); cyc {
		p.print(p.keyword("CYCLE"))
	}

	p.print(")")
	p.movePast(n)
}

func (p *printer) visitIdentityColumnStartWith(n *googlesql.ASTIdentityColumnStartWith, d printerData) {
	val, _ := n.Value()
	p.accept(val, d)
}

func (p *printer) visitIdentityColumnIncrementBy(n *googlesql.ASTIdentityColumnIncrementBy, d printerData) {
	val, _ := n.Value()
	p.accept(val, d)
}

func (p *printer) visitIdentityColumnMaxValue(n *googlesql.ASTIdentityColumnMaxValue, d printerData) {
	val, _ := n.Value()
	p.accept(val, d)
}

func (p *printer) visitIdentityColumnMinValue(n *googlesql.ASTIdentityColumnMinValue, d printerData) {
	val, _ := n.Value()
	p.accept(val, d)
}

func (p *printer) visitAlterColumnSetGeneratedAction(
	n *googlesql.ASTAlterColumnSetGeneratedAction,
	d printerData,
) {
	ie, _ := n.IsIfExists()
	cn, _ := n.ColumnName()
	gci, _ := n.GeneratedColumnInfo()
	p.printAlterColumnAction(n, ie, cn, d, func() {
		p.print(p.keyword("SET"))
		p.accept(gci, d)
	})
}

func (p *printer) visitAlterColumnDropGeneratedAction(
	n *googlesql.ASTAlterColumnDropGeneratedAction,
	d printerData,
) {
	ie, _ := n.IsIfExists()
	cn, _ := n.ColumnName()
	p.printAlterColumnAction(n, ie, cn, d, func() {
		p.print(p.keyword("DROP GENERATED"))
	})
}

func (p *printer) visitForeignKeyColumnAttribute(n *googlesql.ASTForeignKeyColumnAttribute, d printerData) {
	p.moveBefore(n)

	if cn, _ := n.ConstraintName(); nodeDefined(cn) {
		p.print(p.keyword("CONSTRAINT"))
		p.accept(cn, d)
	}

	ref, _ := n.Reference()
	p.accept(ref, d)
	p.movePast(n)
}

func (p *printer) visitHiddenColumnAttribute(n *googlesql.ASTHiddenColumnAttribute, _ printerData) {
	p.moveBefore(n)
	p.print(p.keyword("HIDDEN"))
	p.movePast(n)
}

func (p *printer) visitCreateIndexStatement(n *googlesql.ASTCreateIndexStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword(createIndexKeywords(n)))

	name, _ := n.Name()
	tbl, _ := n.TableName()
	alias, _ := n.OptionalTableAlias()
	unnest, _ := n.OptionalIndexUnnestExpressionList()
	items, _ := n.IndexItemList()
	storing, _ := n.OptionalIndexStoringExpressions()
	pb, _ := n.OptionalPartitionBy()
	opt, _ := n.OptionsList()

	p.accept(name, d)
	p.println("")

	onLine := p.nest()
	onLine.print(onLine.keyword("ON"))
	onLine.accept(tbl, d)
	onLine.accept(alias, d)

	if nodeDefined(unnest) {
		onLine.lnaccept(unnest, d)
		onLine.println("")
	}

	onLine.accept(items, d)
	p.print(alignNested(onLine.String()))

	p.lnacceptAligned(storing, d)
	p.lnacceptAligned(pb, d)
	p.lnacceptAligned(opt, d)
	p.movePast(n)
}

func (p *printer) lnacceptAligned(n googlesql.ASTNode, d printerData) {
	if !nodeDefined(n) {
		return
	}

	p.println("")
	p.print(alignNested(p.toString(n, d)))
}

func createIndexKeywords(n *googlesql.ASTCreateIndexStatement) string {
	var parts []string

	parts = append(parts, "CREATE")

	if orRep, _ := n.IsOrReplace(); orRep {
		parts = append(parts, "OR REPLACE")
	}

	if uniq, _ := n.IsUnique(); uniq {
		parts = append(parts, "UNIQUE")
	}

	if nf, _ := n.SpannerIsNullFiltered(); nf {
		parts = append(parts, "NULL_FILTERED")
	}

	if srch, _ := n.IsSearch(); srch {
		parts = append(parts, "SEARCH")
	}

	if vec, _ := n.IsVector(); vec {
		parts = append(parts, "VECTOR")
	}

	parts = append(parts, "INDEX")

	if ine, _ := n.IsIfNotExists(); ine {
		parts = append(parts, "IF NOT EXISTS")
	}

	return strings.Join(parts, " ")
}

func (p *printer) visitAlterIndexStatement(n *googlesql.ASTAlterIndexStatement, d printerData) {
	p.moveBefore(n)

	it, _ := n.IndexType()
	switch it {
	case googlesql.ASTAlterIndexStatementEnums_IndexTypeIndexSearch:
		p.print(p.keyword("ALTER SEARCH INDEX"))
	case googlesql.ASTAlterIndexStatementEnums_IndexTypeIndexVector:
		p.print(p.keyword("ALTER VECTOR INDEX"))
	case googlesql.ASTAlterIndexStatementEnums_IndexTypeIndexDefault:
		p.print(p.keyword("ALTER INDEX"))
	}

	if ie, _ := n.IsIfExists(); ie {
		p.print(p.keyword("IF EXISTS"))
	}

	path, _ := n.Path()
	tbl, _ := n.TableName()
	al, _ := n.ActionList()

	p.accept(path, d)

	if nodeDefined(tbl) {
		p.print(p.keyword("ON"))
		p.accept(tbl, d)
	}

	p.printAlterActionList(n, al, d)
}

func (p *printer) visitDropIndexStatement(n *googlesql.ASTDropIndexStatement, d printerData) {
	ie, _ := n.IsIfExists()
	name, _ := n.Name()
	tbl, _ := n.TableName()
	p.printDropNamedObject(n, "INDEX", ie, name, tbl, d)
}

func (p *printer) visitDropVectorIndexStatement(n *googlesql.ASTDropVectorIndexStatement, d printerData) {
	ie, _ := n.IsIfExists()
	name, _ := n.Name()
	tbl, _ := n.TableName()
	p.printDropNamedObject(n, "VECTOR INDEX", ie, name, tbl, d)
}

func (p *printer) printDropNamedObject(
	n googlesql.ASTNode,
	kind string,
	ifExists bool,
	name googlesql.ASTNode,
	tbl googlesql.ASTNode,
	d printerData,
) {
	p.moveBefore(n)
	p.print(p.keyword(dropKeyword(ifExists, kind)))
	p.accept(name, d)

	if nodeDefined(tbl) {
		p.print(p.keyword("ON"))
		p.accept(tbl, d)
	}

	p.movePast(n)
}

func (p *printer) visitRebuildAction(n *googlesql.ASTRebuildAction, _ printerData) {
	p.moveBefore(n)
	p.print(p.keyword("REBUILD"))
	p.movePast(n)
}

func (p *printer) visitIndexItemList(n *googlesql.ASTIndexItemList, d printerData) {
	p.moveBefore(n)
	p.print("(")

	items := selectChildrenOfType[*googlesql.ASTOrderingExpression](n)
	for i, item := range items {
		if i > 0 {
			p.print(",")
		}

		p.accept(item, d)
	}

	p.print(")")
	p.movePast(n)
}

func (p *printer) visitIndexAllColumns(n *googlesql.ASTIndexAllColumns, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("ALL COLUMNS"))

	if co, _ := n.ColumnOptions(); nodeDefined(co) {
		p.print(p.keyword("WITH COLUMN OPTIONS") + " ")
		p.accept(co, d)
	}

	p.movePast(n)
}

func (p *printer) visitIndexStoringExpressionList(n *googlesql.ASTIndexStoringExpressionList, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("STORING") + " (")

	exprs := selectChildrenOfType[googlesql.ASTExpressionNode](n)
	printNestedWithSep(p, exprs, d, ",")

	p.print(")")
	p.movePast(n)
}

func (p *printer) visitIndexUnnestExpressionList(n *googlesql.ASTIndexUnnestExpressionList, d printerData) {
	p.moveBefore(n)

	for i, ue := range selectChildrenOfType[*googlesql.ASTUnnestExpressionWithOptAliasAndOffset](n) {
		if i > 0 {
			p.println("")
		}

		p.accept(ue, d)
	}

	p.movePast(n)
}

func (p *printer) visitCreateExternalSchemaStatement(
	n *googlesql.ASTCreateExternalSchemaStatement,
	d printerData,
) {
	p.moveBefore(n)
	p.print(p.keyword(createStatementKeywords(n, false, "EXTERNAL SCHEMA")))

	name, _ := n.Name()
	wcc, _ := n.WithConnectionClause()
	opt, _ := n.OptionsList()

	p.accept(name, d)
	p.lnaccept(wcc, d)
	p.lnaccept(opt, d)
	p.movePast(n)
}

func (p *printer) visitAlterExternalSchemaStatement(
	n *googlesql.ASTAlterExternalSchemaStatement,
	d printerData,
) {
	p.visitAlterStatementBase("ALTER EXTERNAL SCHEMA", n, d)
}

func (p *printer) visitCreateConnectionStatement(n *googlesql.ASTCreateConnectionStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword(createStatementKeywords(n, false, "CONNECTION")))

	name, _ := n.Name()
	opt, _ := n.OptionsList()

	p.accept(name, d)
	p.lnaccept(opt, d)
	p.movePast(n)
}

func (p *printer) visitAlterConnectionStatement(n *googlesql.ASTAlterConnectionStatement, d printerData) {
	p.visitAlterStatementBase("ALTER CONNECTION", n, d)
}

func (p *printer) visitCreateEntityStatement(n *googlesql.ASTCreateEntityStatement, d printerData) {
	p.moveBefore(n)

	obj := "ENTITY"
	if typ, _ := n.Type(); nodeDefined(typ) {
		obj = p.rawIdentifier(typ)
	}

	p.print(p.keyword(createStatementKeywords(n, false, obj)))

	name, _ := n.Name()
	opt, _ := n.OptionsList()
	jb, _ := n.JsonBody()
	tb, _ := n.TextBody()

	p.accept(name, d)
	p.lnaccept(opt, d)

	if nodeDefined(jb) {
		p.println("")
		p.print(p.keyword("AS"))
		p.accept(jb, d)
	}

	if nodeDefined(tb) {
		p.println("")
		p.print(p.keyword("AS"))
		p.accept(tb, d)
	}

	p.movePast(n)
}

func (p *printer) printSubEntityHeader(
	n googlesql.ASTNode,
	kw string,
	typ *googlesql.ASTIdentifier,
	condKw string,
	cond bool,
	name *googlesql.ASTIdentifier,
	d printerData,
) {
	p.moveBefore(n)
	p.print(p.keyword(kw))

	if nodeDefined(typ) {
		p.print(p.rawKeywordIdent(typ))
	}

	if cond {
		p.print(p.keyword(condKw))
	}

	p.accept(name, d)
}

func (p *printer) visitAddSubEntityAction(n *googlesql.ASTAddSubEntityAction, d printerData) {
	typ, _ := n.Type()
	ine, _ := n.IsIfNotExists()
	name, _ := n.Name()
	opt, _ := n.OptionsList()

	p.printSubEntityHeader(n, "ADD", typ, "IF NOT EXISTS", ine, name, d)
	p.accept(opt, d)
	p.movePast(n)
}

func (p *printer) visitDropSubEntityAction(n *googlesql.ASTDropSubEntityAction, d printerData) {
	typ, _ := n.Type()
	ie, _ := n.IsIfExists()
	name, _ := n.Name()

	p.printSubEntityHeader(n, "DROP", typ, "IF EXISTS", ie, name, d)
	p.movePast(n)
}

func (p *printer) visitAlterSubEntityAction(n *googlesql.ASTAlterSubEntityAction, d printerData) {
	typ, _ := n.Type()
	ie, _ := n.IsIfExists()
	name, _ := n.Name()
	act, _ := n.Action()

	p.printSubEntityHeader(n, "ALTER", typ, "IF EXISTS", ie, name, d)
	p.accept(act, d)
	p.movePast(n)
}

func (p *printer) visitExportModelStatement(n *googlesql.ASTExportModelStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("EXPORT MODEL"))

	name, _ := n.ModelNamePath()
	wcc, _ := n.WithConnectionClause()
	opt, _ := n.OptionsList()

	p.accept(name, d)
	p.lnaccept(wcc, d)
	p.lnaccept(opt, d)
	p.movePast(n)
}

func (p *printer) visitAuxLoadDataPartitionsClause(
	n *googlesql.ASTAuxLoadDataPartitionsClause,
	d printerData,
) {
	p.moveBefore(n)

	if ov, _ := n.IsOverwrite(); ov {
		p.print(p.keyword("OVERWRITE"))
	}

	pf, _ := n.PartitionFilter()

	p.print(p.keyword("PARTITIONS") + " (")
	p.accept(pf, d)
	p.print(")")
	p.movePast(n)
}

func (p *printer) visitAlterSequenceStatement(n *googlesql.ASTAlterSequenceStatement, d printerData) {
	p.visitAlterStatementBase("ALTER SEQUENCE", n, d)
}

func (p *printer) visitCloneDataStatement(n *googlesql.ASTCloneDataStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("CLONE DATA INTO"))

	target, _ := n.TargetPath()
	p.accept(target, d)

	if dsl, _ := n.DataSourceList(); nodeDefined(dsl) {
		p.println("")
		p.print(p.keyword("FROM"))
		p.printCloneDataSourceList(dsl, d)
	}

	p.movePast(n)
}

func (p *printer) printCloneDataSourceList(n *googlesql.ASTCloneDataSourceList, d printerData) {
	p.moveBefore(n)

	for i, src := range selectChildrenOfType[*googlesql.ASTCloneDataSource](n) {
		if i > 0 {
			p.println("")
			p.print(p.keyword("UNION ALL"))
		}

		p.accept(src, d)
	}

	p.movePast(n)
}

func (p *printer) visitCreateSequenceStatement(n *googlesql.ASTCreateSequenceStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword(createStatementKeywords(n, false, "SEQUENCE")))

	name, _ := n.Name()
	opt, _ := n.OptionsList()

	p.accept(name, d)
	p.lnaccept(opt, d)
	p.movePast(n)
}

func (p *printer) visitSetAsAction(n *googlesql.ASTSetAsAction, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("SET AS"))

	if jb, _ := n.JsonBody(); nodeDefined(jb) {
		p.accept(jb, d)
	}

	if tb, _ := n.TextBody(); nodeDefined(tb) {
		p.accept(tb, d)
	}

	p.movePast(n)
}

func (p *printer) visitAnalyzeStatement(n *googlesql.ASTAnalyzeStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("ANALYZE"))

	opt, _ := n.OptionsList()
	hasOpt := nodeDefined(opt)
	if hasOpt {
		p.lnaccept(opt, d)
	}

	if list, _ := n.TableAndColumnInfoList(); nodeDefined(list) {
		if hasOpt {
			p.lnaccept(list, d)
		} else {
			p.accept(list, d)
		}
	}

	p.movePast(n)
}

func (p *printer) visitTableAndColumnInfoList(n *googlesql.ASTTableAndColumnInfoList, d printerData) {
	p.moveBefore(n)

	entries := selectChildrenOfType[*googlesql.ASTTableAndColumnInfo](n)
	for i, entry := range entries {
		if i > 0 {
			p.print(",")
		}

		p.accept(entry, d)
	}

	p.movePast(n)
}

func (p *printer) visitTableAndColumnInfo(n *googlesql.ASTTableAndColumnInfo, d printerData) {
	p.moveBefore(n)

	tbl, _ := n.TableName()
	cols, _ := n.ColumnList()

	p.accept(tbl, d)

	if nodeDefined(cols) {
		p.accept(cols, d)
	}

	p.movePast(n)
}

func (p *printer) visitCreateConstantStatement(n *googlesql.ASTCreateConstantStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword(createStatementKeywords(n, false, "CONSTANT")))

	name, _ := n.Name()
	expr, _ := n.Expr()

	p.accept(name, d)
	p.print("=")
	p.accept(expr, d)
	p.movePast(n)
}

func (p *printer) visitCreateDatabaseStatement(n *googlesql.ASTCreateDatabaseStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("CREATE DATABASE"))

	name, _ := n.Name()
	opt, _ := n.OptionsList()

	p.accept(name, d)
	p.lnaccept(opt, d)
	p.movePast(n)
}

func (p *printer) visitDefineTableStatement(n *googlesql.ASTDefineTableStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("DEFINE TABLE"))

	name, _ := n.Name()
	opt, _ := n.OptionsList()

	p.accept(name, d)

	if nodeDefined(opt) {
		entries := selectChildrenOfType[*googlesql.ASTOptionsEntry](opt)
		if len(entries) > 1 || !p.isSimpleOptionsList(opt) {
			p.println("")
		}

		p.printOptionsListBody(opt, "", d)
	}

	p.movePast(n)
}

func (p *printer) visitDescribeStatement(n *googlesql.ASTDescribeStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("DESCRIBE"))

	if id, _ := n.OptionalIdentifier(); nodeDefined(id) {
		p.print(p.rawKeywordIdent(id))
	}

	name, _ := n.Name()
	p.accept(name, d)

	if fromName, _ := n.OptionalFromName(); nodeDefined(fromName) {
		p.print(p.keyword("FROM"))
		p.accept(fromName, d)
	}

	p.movePast(n)
}

func (p *printer) visitExplainStatement(n *googlesql.ASTExplainStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("EXPLAIN"))

	stmt, _ := n.Statement()
	p.accept(stmt, d)
	p.movePast(n)
}

func (p *printer) visitImportStatement(n *googlesql.ASTImportStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("IMPORT"))

	kind, _ := n.ImportKind()
	switch kind {
	case googlesql.ASTImportStatementEnums_ImportKindModule:
		p.print(p.keyword("MODULE"))
	case googlesql.ASTImportStatementEnums_ImportKindProto:
		p.print(p.keyword("PROTO"))
	}

	if name, _ := n.Name(); nodeDefined(name) {
		p.accept(name, d)
	}

	if sv, _ := n.StringValue(); nodeDefined(sv) {
		p.accept(sv, d)
	}

	if alias, _ := n.Alias(); nodeDefined(alias) {
		p.accept(alias, d)
	}

	if intoAlias, _ := n.IntoAlias(); nodeDefined(intoAlias) {
		p.accept(intoAlias, d)
	}

	if opt, _ := n.OptionsList(); nodeDefined(opt) {
		p.lnaccept(opt, d)
	}

	p.movePast(n)
}

func (p *printer) visitIntoAlias(n *googlesql.ASTIntoAlias, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("INTO"))

	id, _ := n.Identifier()
	p.accept(id, d)
	p.movePast(n)
}

func (p *printer) visitModuleStatement(n *googlesql.ASTModuleStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("MODULE"))

	name, _ := n.Name()
	opt, _ := n.OptionsList()

	p.accept(name, d)
	p.lnaccept(opt, d)
	p.movePast(n)
}

func (p *printer) visitRenameStatement(n *googlesql.ASTRenameStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("RENAME"))

	if id, _ := n.Identifier(); nodeDefined(id) {
		p.print(p.rawKeywordIdent(id))
	}

	oldName, _ := n.OldName()
	newName, _ := n.NewName()

	p.accept(oldName, d)
	p.print(p.keyword("TO"))
	p.accept(newName, d)
	p.movePast(n)
}

func (p *printer) visitShowStatement(n *googlesql.ASTShowStatement, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("SHOW"))

	if id, _ := n.Identifier(); nodeDefined(id) {
		p.print(p.rawKeywordIdent(id))
	}

	if optName, _ := n.OptionalName(); nodeDefined(optName) {
		p.print(p.keyword("FROM"))
		p.accept(optName, d)
	}

	if likeStr, _ := n.OptionalLikeString(); nodeDefined(likeStr) {
		p.print(p.keyword("LIKE"))
		p.accept(likeStr, d)
	}

	p.movePast(n)
}
