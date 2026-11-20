package formatter

import (
	"fmt"
	"io"
	"log"
	"reflect"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/goccy/go-googlesql"
)

type printer struct {
	fmt         *formatterState
	input       string
	erasedInput string
	tracker     *locationTracker
	logger      *log.Logger
	errs        *errorQueue
}

type printerData struct {
	alignBinaryOpBudget  int
	joinCount            int
	functionParamsSimple bool
	optionsSimple        bool
	pivotForValuesSimple bool
	procedureParams      bool
	simpleCase           bool
	singleLineCols       bool
	unpivotInItemSimple  bool
}

type nodeVisitorFunc func(*printer, googlesql.ASTNode, printerData)

type parenthesizableNode interface {
	googlesql.ASTNode
	Parenthesized() (bool, error)
}

type formatSentinels struct {
	lineBreak        string
	literalLineBreak string
	trailingComment  string
	verticalTab      string
}

func newFormatSentinels(input string) formatSentinels {
	prefix := "\x1e\x1f"
	for strings.Contains(input, prefix) {
		prefix += "0"
	}

	return formatSentinels{
		lineBreak:        prefix + "1",
		literalLineBreak: prefix + "2",
		trailingComment:  prefix + "3",
		verticalTab:      prefix + "4",
	}
}

// Initialized via sync.Once to break the visitor -> visit -> newVisitorMap cycle.
var (
	nodeVisitorsOnce sync.Once
	nodeVisitors     map[reflect.Type]nodeVisitorFunc
)

func lookupVisitor(t reflect.Type) (nodeVisitorFunc, bool) {
	nodeVisitorsOnce.Do(func() {
		nodeVisitors = newVisitorMap()
	})

	fn, ok := nodeVisitors[t]

	return fn, ok
}

func (p *printer) acceptClause(n googlesql.ASTNode, d printerData) {
	p.visit(n, p.fmt.opts.NewlineBeforeClause, d)
}

func (p *printer) accept(n googlesql.ASTNode, d printerData) {
	p.visit(n, false, d)
}

func (p *printer) lnaccept(n googlesql.ASTNode, d printerData) {
	p.visit(n, true, d)
}

func makeVisitor[T googlesql.ASTNode](fn func(*printer, T, printerData)) nodeVisitorFunc {
	return func(p *printer, n googlesql.ASTNode, d printerData) {
		if typed, ok := n.(T); ok {
			fn(p, typed, d)
		}
	}
}

func (p *printer) visit(n googlesql.ASTNode, newline bool, d printerData) {
	if !nodeDefined(n) {
		return
	}

	if newline {
		p.println("")
	}

	if handler, ok := lookupVisitor(reflect.TypeOf(n)); ok {
		handler(p, n, d)

		return
	}

	p.addError(&PrinterError{
		Msg:   fmt.Sprintf("not implemented for %T", n),
		Node:  n,
		Input: &p.input,
	})
}

func newVisitorMap() map[reflect.Type]nodeVisitorFunc {
	rowPatternVisitor := makeVisitor((*printer).visitRowPattern)
	quantifierVisitor := makeVisitor((*printer).visitQuantifier)
	graphLabelVisitor := makeVisitor((*printer).visitGraphLabelExpr)

	return map[reflect.Type]nodeVisitorFunc{
		reflect.TypeFor[*googlesql.ASTAddColumnAction]():                       makeVisitor((*printer).visitAddColumnAction),
		reflect.TypeFor[*googlesql.ASTAddConstraintAction]():                   makeVisitor((*printer).visitAddConstraintAction),
		reflect.TypeFor[*googlesql.ASTAddSubEntityAction]():                    makeVisitor((*printer).visitAddSubEntityAction),
		reflect.TypeFor[*googlesql.ASTAddToRestricteeListClause]():             makeVisitor((*printer).visitAddToRestricteeListClause),
		reflect.TypeFor[*googlesql.ASTAfterMatchSkipClause]():                  makeVisitor((*printer).visitAfterMatchSkipClause),
		reflect.TypeFor[*googlesql.ASTAlias]():                                 makeVisitor((*printer).visitAlias),
		reflect.TypeFor[*googlesql.ASTAliasedQuery]():                          makeVisitor((*printer).visitAliasedQuery),
		reflect.TypeFor[*googlesql.ASTAliasedQueryExpression]():                makeVisitor((*printer).visitAliasedQueryExpression),
		reflect.TypeFor[*googlesql.ASTAlterActionList]():                       makeVisitor((*printer).visitAlterActionList),
		reflect.TypeFor[*googlesql.ASTAlterAllRowAccessPoliciesStatement]():    makeVisitor((*printer).visitAlterAllRowAccessPoliciesStatement),
		reflect.TypeFor[*googlesql.ASTAlterColumnDropDefaultAction]():          makeVisitor((*printer).visitAlterColumnDropDefaultAction),
		reflect.TypeFor[*googlesql.ASTAlterColumnDropGeneratedAction]():        makeVisitor((*printer).visitAlterColumnDropGeneratedAction),
		reflect.TypeFor[*googlesql.ASTAlterColumnDropNotNullAction]():          makeVisitor((*printer).visitAlterColumnDropNotNullAction),
		reflect.TypeFor[*googlesql.ASTAlterColumnOptionsAction]():              makeVisitor((*printer).visitAlterColumnOptionsAction),
		reflect.TypeFor[*googlesql.ASTAlterColumnSetDefaultAction]():           makeVisitor((*printer).visitAlterColumnSetDefaultAction),
		reflect.TypeFor[*googlesql.ASTAlterColumnSetGeneratedAction]():         makeVisitor((*printer).visitAlterColumnSetGeneratedAction),
		reflect.TypeFor[*googlesql.ASTAlterColumnTypeAction]():                 makeVisitor((*printer).visitAlterColumnTypeAction),
		reflect.TypeFor[*googlesql.ASTAlterConnectionStatement]():              makeVisitor((*printer).visitAlterConnectionStatement),
		reflect.TypeFor[*googlesql.ASTAlterConstraintEnforcementAction]():      makeVisitor((*printer).visitAlterConstraintEnforcementAction),
		reflect.TypeFor[*googlesql.ASTAlterConstraintSetOptionsAction]():       makeVisitor((*printer).visitAlterConstraintSetOptionsAction),
		reflect.TypeFor[*googlesql.ASTAlterDatabaseStatement]():                makeVisitor((*printer).visitAlterDatabaseStatement),
		reflect.TypeFor[*googlesql.ASTAlterEntityStatement]():                  makeVisitor((*printer).visitAlterEntityStatement),
		reflect.TypeFor[*googlesql.ASTAlterExternalSchemaStatement]():          makeVisitor((*printer).visitAlterExternalSchemaStatement),
		reflect.TypeFor[*googlesql.ASTAlterIndexStatement]():                   makeVisitor((*printer).visitAlterIndexStatement),
		reflect.TypeFor[*googlesql.ASTAlterMaterializedViewStatement]():        makeVisitor((*printer).visitAlterMaterializedViewStatement),
		reflect.TypeFor[*googlesql.ASTAlterModelStatement]():                   makeVisitor((*printer).visitAlterModelStatement),
		reflect.TypeFor[*googlesql.ASTAlterPrivilegeRestrictionStatement]():    makeVisitor((*printer).visitAlterPrivilegeRestrictionStatement),
		reflect.TypeFor[*googlesql.ASTAlterRowAccessPolicyStatement]():         makeVisitor((*printer).visitAlterRowAccessPolicyStatement),
		reflect.TypeFor[*googlesql.ASTAlterSchemaStatement]():                  makeVisitor((*printer).visitAlterSchemaStatement),
		reflect.TypeFor[*googlesql.ASTAlterSequenceStatement]():                makeVisitor((*printer).visitAlterSequenceStatement),
		reflect.TypeFor[*googlesql.ASTAlterSubEntityAction]():                  makeVisitor((*printer).visitAlterSubEntityAction),
		reflect.TypeFor[*googlesql.ASTAlterTableStatement]():                   makeVisitor((*printer).visitAlterTableStatement),
		reflect.TypeFor[*googlesql.ASTAlterViewStatement]():                    makeVisitor((*printer).visitAlterViewStatement),
		reflect.TypeFor[*googlesql.ASTAnalyticFunctionCall]():                  makeVisitor((*printer).visitAnalyticFunctionCall),
		reflect.TypeFor[*googlesql.ASTAndExpr]():                               makeVisitor((*printer).visitAndExpr),
		reflect.TypeFor[*googlesql.ASTArrayColumnSchema]():                     makeVisitor((*printer).visitArrayColumnSchema),
		reflect.TypeFor[*googlesql.ASTArrayConstructor]():                      makeVisitor((*printer).visitArrayConstructor),
		reflect.TypeFor[*googlesql.ASTArrayElement]():                          makeVisitor((*printer).visitArrayElement),
		reflect.TypeFor[*googlesql.ASTArrayType]():                             makeVisitor((*printer).visitArrayType),
		reflect.TypeFor[*googlesql.ASTAssertStatement]():                       makeVisitor((*printer).visitAssertStatement),
		reflect.TypeFor[*googlesql.ASTAssignmentFromStruct]():                  makeVisitor((*printer).visitAssignmentFromStruct),
		reflect.TypeFor[*googlesql.ASTAuxLoadDataPartitionsClause]():           makeVisitor((*printer).visitAuxLoadDataPartitionsClause),
		reflect.TypeFor[*googlesql.ASTAuxLoadDataStatement]():                  makeVisitor((*printer).visitAuxLoadDataStatement),
		reflect.TypeFor[*googlesql.ASTBeginEndBlock]():                         makeVisitor((*printer).visitBeginEndBlock),
		reflect.TypeFor[*googlesql.ASTBeginStatement]():                        makeVisitor((*printer).visitBeginStatement),
		reflect.TypeFor[*googlesql.ASTBetweenExpression]():                     makeVisitor((*printer).visitBetweenExpression),
		reflect.TypeFor[*googlesql.ASTBigNumericLiteral]():                     makeVisitor((*printer).visitBigNumericLiteral),
		reflect.TypeFor[*googlesql.ASTBinaryExpression]():                      makeVisitor((*printer).visitBinaryExpression),
		reflect.TypeFor[*googlesql.ASTBitwiseShiftExpression]():                makeVisitor((*printer).visitBitwiseShiftExpression),
		reflect.TypeFor[*googlesql.ASTBooleanLiteral]():                        makeVisitor((*printer).visitBoolLiteral),
		reflect.TypeFor[*googlesql.ASTBoundedQuantifier]():                     quantifierVisitor,
		reflect.TypeFor[*googlesql.ASTBreakStatement]():                        makeVisitor((*printer).visitBreakStatement),
		reflect.TypeFor[*googlesql.ASTBytesLiteral]():                          makeVisitor((*printer).visitBytesLiteral),
		reflect.TypeFor[*googlesql.ASTCallStatement]():                         makeVisitor((*printer).visitCallStatement),
		reflect.TypeFor[*googlesql.ASTCaseNoValueExpression]():                 makeVisitor((*printer).visitCaseNoValueExpression),
		reflect.TypeFor[*googlesql.ASTCaseStatement]():                         makeVisitor((*printer).visitCaseStatement),
		reflect.TypeFor[*googlesql.ASTCaseValueExpression]():                   makeVisitor((*printer).visitCaseValueExpression),
		reflect.TypeFor[*googlesql.ASTCastExpression]():                        makeVisitor((*printer).visitCastExpression),
		reflect.TypeFor[*googlesql.ASTChainedBaseExpr]():                       makeVisitor((*printer).visitChainedBaseExpr),
		reflect.TypeFor[*googlesql.ASTCheckConstraint]():                       makeVisitor((*printer).visitCheckConstraint),
		reflect.TypeFor[*googlesql.ASTClampedBetweenModifier]():                makeVisitor((*printer).visitClampedBetweenModifier),
		reflect.TypeFor[*googlesql.ASTCloneDataSource]():                       makeVisitor((*printer).visitCloneDataSource),
		reflect.TypeFor[*googlesql.ASTCloneDataStatement]():                    makeVisitor((*printer).visitCloneDataStatement),
		reflect.TypeFor[*googlesql.ASTClusterBy]():                             makeVisitor((*printer).visitClusterBy),
		reflect.TypeFor[*googlesql.ASTCollate]():                               makeVisitor((*printer).visitCollate),
		reflect.TypeFor[*googlesql.ASTColumnAttributeList]():                   makeVisitor((*printer).visitColumnAttributeList),
		reflect.TypeFor[*googlesql.ASTColumnDefinition]():                      makeVisitor((*printer).visitColumnDefinition),
		reflect.TypeFor[*googlesql.ASTColumnList]():                            makeVisitor((*printer).visitColumnList),
		reflect.TypeFor[*googlesql.ASTColumnSchema]():                          makeVisitor((*printer).visitColumnSchema),
		reflect.TypeFor[*googlesql.ASTColumnWithOptions]():                     makeVisitor((*printer).visitColumnWithOptions),
		reflect.TypeFor[*googlesql.ASTColumnWithOptionsList]():                 makeVisitor((*printer).visitColumnWithOptionsList),
		reflect.TypeFor[*googlesql.ASTCommitStatement]():                       makeVisitor((*printer).visitCommitStatement),
		reflect.TypeFor[*googlesql.ASTConcatExpr]():                            makeVisitor((*printer).visitConcatExpr),
		reflect.TypeFor[*googlesql.ASTConnectionClause]():                      makeVisitor((*printer).visitConnectionClause),
		reflect.TypeFor[*googlesql.ASTContinueStatement]():                     makeVisitor((*printer).visitContinueStatement),
		reflect.TypeFor[*googlesql.ASTCopyDataSource]():                        makeVisitor((*printer).visitCopyDataSource),
		reflect.TypeFor[*googlesql.ASTCreateConnectionStatement]():             makeVisitor((*printer).visitCreateConnectionStatement),
		reflect.TypeFor[*googlesql.ASTCreateEntityStatement]():                 makeVisitor((*printer).visitCreateEntityStatement),
		reflect.TypeFor[*googlesql.ASTCreateExternalSchemaStatement]():         makeVisitor((*printer).visitCreateExternalSchemaStatement),
		reflect.TypeFor[*googlesql.ASTCreateExternalTableStatement]():          makeVisitor((*printer).visitCreateExternalTableStatement),
		reflect.TypeFor[*googlesql.ASTCreateFunctionStatement]():               makeVisitor((*printer).visitCreateFunctionStatement),
		reflect.TypeFor[*googlesql.ASTCreateIndexStatement]():                  makeVisitor((*printer).visitCreateIndexStatement),
		reflect.TypeFor[*googlesql.ASTCreateMaterializedViewStatement]():       makeVisitor((*printer).visitCreateMaterializedViewStatement),
		reflect.TypeFor[*googlesql.ASTCreateModelStatement]():                  makeVisitor((*printer).visitCreateModelStatement),
		reflect.TypeFor[*googlesql.ASTCreateProcedureStatement]():              makeVisitor((*printer).visitCreateProcedureStatement),
		reflect.TypeFor[*googlesql.ASTCreatePropertyGraphStatement]():          makeVisitor((*printer).visitCreatePropertyGraphStatement),
		reflect.TypeFor[*googlesql.ASTCreateRowAccessPolicyStatement]():        makeVisitor((*printer).visitCreateRowAccessPolicyStatement),
		reflect.TypeFor[*googlesql.ASTCreateSchemaStatement]():                 makeVisitor((*printer).visitCreateSchemaStatement),
		reflect.TypeFor[*googlesql.ASTCreateSequenceStatement]():               makeVisitor((*printer).visitCreateSequenceStatement),
		reflect.TypeFor[*googlesql.ASTCreateSnapshotTableStatement]():          makeVisitor((*printer).visitCreateSnapshotTableStatement),
		reflect.TypeFor[*googlesql.ASTCreateTableFunctionStatement]():          makeVisitor((*printer).visitCreateTableFunctionStatement),
		reflect.TypeFor[*googlesql.ASTCreateTableStatement]():                  makeVisitor((*printer).visitCreateTableStatement),
		reflect.TypeFor[*googlesql.ASTCreateViewStatement]():                   makeVisitor((*printer).visitCreateViewStatement),
		reflect.TypeFor[*googlesql.ASTCube]():                                  makeVisitor((*printer).visitCube),
		reflect.TypeFor[*googlesql.ASTDateOrTimeLiteral]():                     makeVisitor((*printer).visitDateOrTimeLiteral),
		reflect.TypeFor[*googlesql.ASTDefaultLiteral]():                        makeVisitor((*printer).visitDefaultLiteral),
		reflect.TypeFor[*googlesql.ASTDeleteStatement]():                       makeVisitor((*printer).visitDeleteStatement),
		reflect.TypeFor[*googlesql.ASTDescriptor]():                            makeVisitor((*printer).visitDescriptor),
		reflect.TypeFor[*googlesql.ASTDescriptorColumn]():                      makeVisitor((*printer).visitDescriptorColumn),
		reflect.TypeFor[*googlesql.ASTDescriptorColumnList]():                  makeVisitor((*printer).visitDescriptorColumnList),
		reflect.TypeFor[*googlesql.ASTDotGeneralizedField]():                   makeVisitor((*printer).visitDotGeneralizedField),
		reflect.TypeFor[*googlesql.ASTDotIdentifier]():                         makeVisitor((*printer).visitDotIdentifier),
		reflect.TypeFor[*googlesql.ASTDotStar]():                               makeVisitor((*printer).visitDotStar),
		reflect.TypeFor[*googlesql.ASTDotStarWithModifiers]():                  makeVisitor((*printer).visitDotStarWithModifiers),
		reflect.TypeFor[*googlesql.ASTDropAllRowAccessPoliciesStatement]():     makeVisitor((*printer).visitDropAllRowAccessPoliciesStatement),
		reflect.TypeFor[*googlesql.ASTDropColumnAction]():                      makeVisitor((*printer).visitDropColumnAction),
		reflect.TypeFor[*googlesql.ASTDropConstraintAction]():                  makeVisitor((*printer).visitDropConstraintAction),
		reflect.TypeFor[*googlesql.ASTDropEntityStatement]():                   makeVisitor((*printer).visitDropEntityStatement),
		reflect.TypeFor[*googlesql.ASTDropFunctionStatement]():                 makeVisitor((*printer).visitDropFunctionStatement),
		reflect.TypeFor[*googlesql.ASTDropIndexStatement]():                    makeVisitor((*printer).visitDropIndexStatement),
		reflect.TypeFor[*googlesql.ASTDropMaterializedViewStatement]():         makeVisitor((*printer).visitDropMaterializedViewStatement),
		reflect.TypeFor[*googlesql.ASTDropPrimaryKeyAction]():                  makeVisitor((*printer).visitDropPrimaryKeyAction),
		reflect.TypeFor[*googlesql.ASTDropPrivilegeRestrictionStatement]():     makeVisitor((*printer).visitDropPrivilegeRestrictionStatement),
		reflect.TypeFor[*googlesql.ASTDropRowAccessPolicyStatement]():          makeVisitor((*printer).visitDropRowAccessPolicyStatement),
		reflect.TypeFor[*googlesql.ASTDropSearchIndexStatement]():              makeVisitor((*printer).visitDropSearchIndexStatement),
		reflect.TypeFor[*googlesql.ASTDropSnapshotTableStatement]():            makeVisitor((*printer).visitDropSnapshotTableStatement),
		reflect.TypeFor[*googlesql.ASTDropStatement]():                         makeVisitor((*printer).visitDropStatement),
		reflect.TypeFor[*googlesql.ASTDropSubEntityAction]():                   makeVisitor((*printer).visitDropSubEntityAction),
		reflect.TypeFor[*googlesql.ASTDropTableFunctionStatement]():            makeVisitor((*printer).visitDropTableFunctionStatement),
		reflect.TypeFor[*googlesql.ASTDropVectorIndexStatement]():              makeVisitor((*printer).visitDropVectorIndexStatement),
		reflect.TypeFor[*googlesql.ASTEmptyRowPattern]():                       rowPatternVisitor,
		reflect.TypeFor[*googlesql.ASTExceptionHandler]():                      makeVisitor((*printer).visitExceptionHandler),
		reflect.TypeFor[*googlesql.ASTExceptionHandlerList]():                  makeVisitor((*printer).visitExceptionHandlerList),
		reflect.TypeFor[*googlesql.ASTExecuteImmediateStatement]():             makeVisitor((*printer).visitExecuteImmediateStatement),
		reflect.TypeFor[*googlesql.ASTExecuteIntoClause]():                     makeVisitor((*printer).visitExecuteIntoClause),
		reflect.TypeFor[*googlesql.ASTExecuteUsingArgument]():                  makeVisitor((*printer).visitExecuteUsingArgument),
		reflect.TypeFor[*googlesql.ASTExecuteUsingClause]():                    makeVisitor((*printer).visitExecuteUsingClause),
		reflect.TypeFor[*googlesql.ASTExportDataStatement]():                   makeVisitor((*printer).visitExportDataStatement),
		reflect.TypeFor[*googlesql.ASTExportModelStatement]():                  makeVisitor((*printer).visitExportModelStatement),
		reflect.TypeFor[*googlesql.ASTExpressionSubquery]():                    makeVisitor((*printer).visitExpressionSubquery),
		reflect.TypeFor[*googlesql.ASTExpressionWithAlias]():                   makeVisitor((*printer).visitExpressionWithAlias),
		reflect.TypeFor[*googlesql.ASTExpressionWithOptAlias]():                makeVisitor((*printer).visitExpressionWithOptAlias),
		reflect.TypeFor[*googlesql.ASTExtendedPathExpression]():                makeVisitor((*printer).visitExtendedPathExpression),
		reflect.TypeFor[*googlesql.ASTExtractExpression]():                     makeVisitor((*printer).visitExtractExpression),
		reflect.TypeFor[*googlesql.ASTFilterUsingClause]():                     makeVisitor((*printer).visitFilterUsingClause),
		reflect.TypeFor[*googlesql.ASTFixedQuantifier]():                       quantifierVisitor,
		reflect.TypeFor[*googlesql.ASTFloatLiteral]():                          makeVisitor((*printer).visitFloatLiteral),
		reflect.TypeFor[*googlesql.ASTForInStatement]():                        makeVisitor((*printer).visitForInStatement),
		reflect.TypeFor[*googlesql.ASTForSystemTime]():                         makeVisitor((*printer).visitForSystemTime),
		reflect.TypeFor[*googlesql.ASTForeignKey]():                            makeVisitor((*printer).visitForeignKey),
		reflect.TypeFor[*googlesql.ASTForeignKeyColumnAttribute]():             makeVisitor((*printer).visitForeignKeyColumnAttribute),
		reflect.TypeFor[*googlesql.ASTForeignKeyReference]():                   makeVisitor((*printer).visitForeignKeyReference),
		reflect.TypeFor[*googlesql.ASTFormatClause]():                          makeVisitor((*printer).visitFormatClause),
		reflect.TypeFor[*googlesql.ASTFromClause]():                            makeVisitor((*printer).visitFromClause),
		reflect.TypeFor[*googlesql.ASTFromQuery]():                             makeVisitor((*printer).visitFromQuery),
		reflect.TypeFor[*googlesql.ASTFunctionCall]():                          makeVisitor((*printer).visitFunctionCall),
		reflect.TypeFor[*googlesql.ASTFunctionDeclaration]():                   makeVisitor((*printer).visitFunctionDeclaration),
		reflect.TypeFor[*googlesql.ASTFunctionParameter]():                     makeVisitor((*printer).visitFunctionParameter),
		reflect.TypeFor[*googlesql.ASTFunctionParameters]():                    makeVisitor((*printer).visitFunctionParameters),
		reflect.TypeFor[*googlesql.ASTFunctionType]():                          makeVisitor((*printer).visitFunctionType),
		reflect.TypeFor[*googlesql.ASTGeneratedColumnInfo]():                   makeVisitor((*printer).visitGeneratedColumnInfo),
		reflect.TypeFor[*googlesql.ASTGqlFilter]():                             makeVisitor((*printer).visitGqlFilter),
		reflect.TypeFor[*googlesql.ASTGqlFor]():                                makeVisitor((*printer).visitGqlFor),
		reflect.TypeFor[*googlesql.ASTGqlGraphPatternQuery]():                  makeVisitor((*printer).visitGqlGraphPatternQuery),
		reflect.TypeFor[*googlesql.ASTGqlInlineSubqueryCall]():                 makeVisitor((*printer).visitGqlInlineSubqueryCall),
		reflect.TypeFor[*googlesql.ASTGqlLet]():                                makeVisitor((*printer).visitGqlLet),
		reflect.TypeFor[*googlesql.ASTGqlLetVariableDefinition]():              makeVisitor((*printer).visitGqlLetVariableDefinition),
		reflect.TypeFor[*googlesql.ASTGqlLetVariableDefinitionList]():          makeVisitor((*printer).visitGqlLetVariableDefinitionList),
		reflect.TypeFor[*googlesql.ASTGqlLinearOpsQuery]():                     makeVisitor((*printer).visitGqlLinearOpsQuery),
		reflect.TypeFor[*googlesql.ASTGqlMatch]():                              makeVisitor((*printer).visitGqlMatch),
		reflect.TypeFor[*googlesql.ASTGqlNamedCall]():                          makeVisitor((*printer).visitGqlNamedCall),
		reflect.TypeFor[*googlesql.ASTGqlOperatorList]():                       makeVisitor((*printer).visitGqlOperatorList),
		reflect.TypeFor[*googlesql.ASTGqlOrderByAndPage]():                     makeVisitor((*printer).visitGqlOrderByAndPage),
		reflect.TypeFor[*googlesql.ASTGqlPage]():                               makeVisitor((*printer).visitGqlPage),
		reflect.TypeFor[*googlesql.ASTGqlPageLimit]():                          makeVisitor((*printer).visitGqlPageLimit),
		reflect.TypeFor[*googlesql.ASTGqlPageOffset]():                         makeVisitor((*printer).visitGqlPageOffset),
		reflect.TypeFor[*googlesql.ASTGqlQuery]():                              makeVisitor((*printer).visitGqlQuery),
		reflect.TypeFor[*googlesql.ASTGqlReturn]():                             makeVisitor((*printer).visitGqlReturn),
		reflect.TypeFor[*googlesql.ASTGqlSample]():                             makeVisitor((*printer).visitGqlSample),
		reflect.TypeFor[*googlesql.ASTGqlSetOperation]():                       makeVisitor((*printer).visitGqlSetOperation),
		reflect.TypeFor[*googlesql.ASTGqlWith]():                               makeVisitor((*printer).visitGqlWith),
		reflect.TypeFor[*googlesql.ASTGrantStatement]():                        makeVisitor((*printer).visitGrantStatement),
		reflect.TypeFor[*googlesql.ASTGrantToClause]():                         makeVisitor((*printer).visitGrantToClause),
		reflect.TypeFor[*googlesql.ASTGranteeList]():                           makeVisitor((*printer).visitGranteeList),
		reflect.TypeFor[*googlesql.ASTGraphDerivedProperty]():                  makeVisitor((*printer).visitGraphDerivedProperty),
		reflect.TypeFor[*googlesql.ASTGraphDerivedPropertyList]():              makeVisitor((*printer).visitGraphDerivedPropertyList),
		reflect.TypeFor[*googlesql.ASTGraphDynamicLabel]():                     makeVisitor((*printer).visitGraphDynamicLabel),
		reflect.TypeFor[*googlesql.ASTGraphDynamicProperties]():                makeVisitor((*printer).visitGraphDynamicProperties),
		reflect.TypeFor[*googlesql.ASTGraphEdgePattern]():                      makeVisitor((*printer).visitGraphEdgePattern),
		reflect.TypeFor[*googlesql.ASTGraphElementLabel]():                     graphLabelVisitor,
		reflect.TypeFor[*googlesql.ASTGraphElementLabelAndProperties]():        makeVisitor((*printer).visitGraphElementLabelAndProperties),
		reflect.TypeFor[*googlesql.ASTGraphElementLabelAndPropertiesList]():    makeVisitor((*printer).visitGraphElementLabelAndPropertiesList),
		reflect.TypeFor[*googlesql.ASTGraphElementPatternFiller]():             makeVisitor((*printer).visitGraphElementPatternFiller),
		reflect.TypeFor[*googlesql.ASTGraphElementTable]():                     makeVisitor((*printer).visitGraphElementTable),
		reflect.TypeFor[*googlesql.ASTGraphElementTableList]():                 makeVisitor((*printer).visitGraphElementTableList),
		reflect.TypeFor[*googlesql.ASTGraphIsLabeledPredicate]():               makeVisitor((*printer).visitGraphIsLabeledPredicate),
		reflect.TypeFor[*googlesql.ASTGraphLabelFilter]():                      makeVisitor((*printer).visitGraphLabelFilter),
		reflect.TypeFor[*googlesql.ASTGraphLabelOperation]():                   graphLabelVisitor,
		reflect.TypeFor[*googlesql.ASTGraphLhsHint]():                          makeVisitor((*printer).visitGraphLHSHint),
		reflect.TypeFor[*googlesql.ASTGraphNodePattern]():                      makeVisitor((*printer).visitGraphNodePattern),
		reflect.TypeFor[*googlesql.ASTGraphNodeTableReference]():               makeVisitor((*printer).visitGraphNodeTableReference),
		reflect.TypeFor[*googlesql.ASTGraphPathMode]():                         makeVisitor((*printer).visitGraphPathMode),
		reflect.TypeFor[*googlesql.ASTGraphPathPattern]():                      makeVisitor((*printer).visitGraphPathPattern),
		reflect.TypeFor[*googlesql.ASTGraphPathSearchPrefix]():                 makeVisitor((*printer).visitGraphPathSearchPrefix),
		reflect.TypeFor[*googlesql.ASTGraphPathSearchPrefixCount]():            makeVisitor((*printer).visitGraphPathSearchPrefixCount),
		reflect.TypeFor[*googlesql.ASTGraphPattern]():                          makeVisitor((*printer).visitGraphPattern),
		reflect.TypeFor[*googlesql.ASTGraphProperties]():                       makeVisitor((*printer).visitGraphProperties),
		reflect.TypeFor[*googlesql.ASTGraphPropertyNameAndValue]():             makeVisitor((*printer).visitGraphPropertyNameAndValue),
		reflect.TypeFor[*googlesql.ASTGraphPropertySpecification]():            makeVisitor((*printer).visitGraphPropertySpecification),
		reflect.TypeFor[*googlesql.ASTGraphRhsHint]():                          makeVisitor((*printer).visitGraphRHSHint),
		reflect.TypeFor[*googlesql.ASTGraphTableQuery]():                       makeVisitor((*printer).visitGraphTableQuery),
		reflect.TypeFor[*googlesql.ASTGraphWildcardLabel]():                    graphLabelVisitor,
		reflect.TypeFor[*googlesql.ASTGroupBy]():                               makeVisitor((*printer).visitGroupBy),
		reflect.TypeFor[*googlesql.ASTGroupingItem]():                          makeVisitor((*printer).visitGroupingItem),
		reflect.TypeFor[*googlesql.ASTGroupingItemOrder]():                     makeVisitor((*printer).visitGroupingItemOrder),
		reflect.TypeFor[*googlesql.ASTGroupingSet]():                           makeVisitor((*printer).visitGroupingSet),
		reflect.TypeFor[*googlesql.ASTGroupingSetList]():                       makeVisitor((*printer).visitGroupingSetList),
		reflect.TypeFor[*googlesql.ASTHaving]():                                makeVisitor((*printer).visitHaving),
		reflect.TypeFor[*googlesql.ASTHavingModifier]():                        makeVisitor((*printer).visitHavingModifier),
		reflect.TypeFor[*googlesql.ASTHiddenColumnAttribute]():                 makeVisitor((*printer).visitHiddenColumnAttribute),
		reflect.TypeFor[*googlesql.ASTHint]():                                  makeVisitor((*printer).visitHint),
		reflect.TypeFor[*googlesql.ASTHintedStatement]():                       makeVisitor((*printer).visitHintedStatement),
		reflect.TypeFor[*googlesql.ASTIdentifier]():                            makeVisitor((*printer).visitIdentifier),
		reflect.TypeFor[*googlesql.ASTIdentifierList]():                        makeVisitor((*printer).visitIdentifierList),
		reflect.TypeFor[*googlesql.ASTIdentityColumnIncrementBy]():             makeVisitor((*printer).visitIdentityColumnIncrementBy),
		reflect.TypeFor[*googlesql.ASTIdentityColumnInfo]():                    makeVisitor((*printer).visitIdentityColumnInfo),
		reflect.TypeFor[*googlesql.ASTIdentityColumnMaxValue]():                makeVisitor((*printer).visitIdentityColumnMaxValue),
		reflect.TypeFor[*googlesql.ASTIdentityColumnMinValue]():                makeVisitor((*printer).visitIdentityColumnMinValue),
		reflect.TypeFor[*googlesql.ASTIdentityColumnStartWith]():               makeVisitor((*printer).visitIdentityColumnStartWith),
		reflect.TypeFor[*googlesql.ASTIfStatement]():                           makeVisitor((*printer).visitIfStatement),
		reflect.TypeFor[*googlesql.ASTInExpression]():                          makeVisitor((*printer).visitInExpression),
		reflect.TypeFor[*googlesql.ASTInList]():                                makeVisitor((*printer).visitInList),
		reflect.TypeFor[*googlesql.ASTIndexAllColumns]():                       makeVisitor((*printer).visitIndexAllColumns),
		reflect.TypeFor[*googlesql.ASTIndexItemList]():                         makeVisitor((*printer).visitIndexItemList),
		reflect.TypeFor[*googlesql.ASTIndexStoringExpressionList]():            makeVisitor((*printer).visitIndexStoringExpressionList),
		reflect.TypeFor[*googlesql.ASTIndexUnnestExpressionList]():             makeVisitor((*printer).visitIndexUnnestExpressionList),
		reflect.TypeFor[*googlesql.ASTInferredTypeColumnSchema]():              makeVisitor((*printer).visitInferredTypeColumnSchema),
		reflect.TypeFor[*googlesql.ASTInputTableArgument]():                    makeVisitor((*printer).visitInputTableArgument),
		reflect.TypeFor[*googlesql.ASTInsertStatement]():                       makeVisitor((*printer).visitInsertStatement),
		reflect.TypeFor[*googlesql.ASTInsertValuesRow]():                       makeVisitor((*printer).visitInsertValuesRow),
		reflect.TypeFor[*googlesql.ASTInsertValuesRowList]():                   makeVisitor((*printer).visitInsertValuesRowList),
		reflect.TypeFor[*googlesql.ASTIntLiteral]():                            makeVisitor((*printer).visitIntLiteral),
		reflect.TypeFor[*googlesql.ASTIntervalExpr]():                          makeVisitor((*printer).visitIntervalExpr),
		reflect.TypeFor[*googlesql.ASTJSONLiteral]():                           makeVisitor((*printer).visitJSONLiteral),
		reflect.TypeFor[*googlesql.ASTJoin]():                                  makeVisitor((*printer).visitJoin),
		reflect.TypeFor[*googlesql.ASTLabel]():                                 makeVisitor((*printer).visitLabel),
		reflect.TypeFor[*googlesql.ASTLambda]():                                makeVisitor((*printer).visitLambda),
		reflect.TypeFor[*googlesql.ASTLikeExpression]():                        makeVisitor((*printer).visitLikeExpression),
		reflect.TypeFor[*googlesql.ASTLimitOffset]():                           makeVisitor((*printer).visitLimitOffset),
		reflect.TypeFor[*googlesql.ASTMapColumnSchema]():                       makeVisitor((*printer).visitMapColumnSchema),
		reflect.TypeFor[*googlesql.ASTMapType]():                               makeVisitor((*printer).visitMapType),
		reflect.TypeFor[*googlesql.ASTMatchRecognizeClause]():                  makeVisitor((*printer).visitMatchRecognizeClause),
		reflect.TypeFor[*googlesql.ASTMaxLiteral]():                            makeVisitor((*printer).visitMaxLiteral),
		reflect.TypeFor[*googlesql.ASTMergeAction]():                           makeVisitor((*printer).visitMergeAction),
		reflect.TypeFor[*googlesql.ASTMergeStatement]():                        makeVisitor((*printer).visitMergeStatement),
		reflect.TypeFor[*googlesql.ASTMergeWhenClause]():                       makeVisitor((*printer).visitMergeWhenClause),
		reflect.TypeFor[*googlesql.ASTMergeWhenClauseList]():                   makeVisitor((*printer).visitMergeWhenClauseList),
		reflect.TypeFor[*googlesql.ASTModelClause]():                           makeVisitor((*printer).visitModelClause),
		reflect.TypeFor[*googlesql.ASTNamedArgument]():                         makeVisitor((*printer).visitNamedArgument),
		reflect.TypeFor[*googlesql.ASTNotNullColumnAttribute]():                makeVisitor((*printer).visitNotNullColumnAttribute),
		reflect.TypeFor[*googlesql.ASTNullLiteral]():                           makeVisitor((*printer).visitNullLiteral),
		reflect.TypeFor[*googlesql.ASTNullOrder]():                             makeVisitor((*printer).visitNullOrder),
		reflect.TypeFor[*googlesql.ASTNumericLiteral]():                        makeVisitor((*printer).visitNumericLiteral),
		reflect.TypeFor[*googlesql.ASTOnClause]():                              makeVisitor((*printer).visitOnClause),
		reflect.TypeFor[*googlesql.ASTOptionsEntry]():                          makeVisitor((*printer).visitOptionsEntry),
		reflect.TypeFor[*googlesql.ASTOptionsList]():                           makeVisitor((*printer).visitOptionsList),
		reflect.TypeFor[*googlesql.ASTOrExpr]():                                makeVisitor((*printer).visitOrExpr),
		reflect.TypeFor[*googlesql.ASTOrderBy]():                               makeVisitor((*printer).visitOrderBy),
		reflect.TypeFor[*googlesql.ASTOrderingExpression]():                    makeVisitor((*printer).visitOrderingExpression),
		reflect.TypeFor[*googlesql.ASTParameterAssignment]():                   makeVisitor((*printer).visitParameterAssignment),
		reflect.TypeFor[*googlesql.ASTParameterExpr]():                         makeVisitor((*printer).visitParameterExpr),
		reflect.TypeFor[*googlesql.ASTParenthesizedJoin]():                     makeVisitor((*printer).visitParenthesizedJoin),
		reflect.TypeFor[*googlesql.ASTPartitionBy]():                           makeVisitor((*printer).visitPartitionBy),
		reflect.TypeFor[*googlesql.ASTPathExpression]():                        makeVisitor((*printer).visitPathExpression),
		reflect.TypeFor[*googlesql.ASTPathExpressionList]():                    makeVisitor((*printer).visitPathExpressionList),
		reflect.TypeFor[*googlesql.ASTPipeAggregate]():                         makeVisitor((*printer).visitPipeAggregate),
		reflect.TypeFor[*googlesql.ASTPipeAs]():                                makeVisitor((*printer).visitPipeAs),
		reflect.TypeFor[*googlesql.ASTPipeAssert]():                            makeVisitor((*printer).visitPipeAssert),
		reflect.TypeFor[*googlesql.ASTPipeCall]():                              makeVisitor((*printer).visitPipeCall),
		reflect.TypeFor[*googlesql.ASTPipeCreateTable]():                       makeVisitor((*printer).visitPipeCreateTable),
		reflect.TypeFor[*googlesql.ASTPipeDescribe]():                          makeVisitor((*printer).visitPipeDescribe),
		reflect.TypeFor[*googlesql.ASTPipeDistinct]():                          makeVisitor((*printer).visitPipeDistinct),
		reflect.TypeFor[*googlesql.ASTPipeDrop]():                              makeVisitor((*printer).visitPipeDrop),
		reflect.TypeFor[*googlesql.ASTPipeExportData]():                        makeVisitor((*printer).visitPipeExportData),
		reflect.TypeFor[*googlesql.ASTPipeExtend]():                            makeVisitor((*printer).visitPipeExtend),
		reflect.TypeFor[*googlesql.ASTPipeFork]():                              makeVisitor((*printer).visitPipeFork),
		reflect.TypeFor[*googlesql.ASTPipeIf]():                                makeVisitor((*printer).visitPipeIf),
		reflect.TypeFor[*googlesql.ASTPipeInsert]():                            makeVisitor((*printer).visitPipeInsert),
		reflect.TypeFor[*googlesql.ASTPipeJoin]():                              makeVisitor((*printer).visitPipeJoin),
		reflect.TypeFor[*googlesql.ASTPipeJoinLhsPlaceholder]():                makeVisitor((*printer).visitPipeJoinLHSPlaceholder),
		reflect.TypeFor[*googlesql.ASTPipeLimitOffset]():                       makeVisitor((*printer).visitPipeLimitOffset),
		reflect.TypeFor[*googlesql.ASTPipeLog]():                               makeVisitor((*printer).visitPipeLog),
		reflect.TypeFor[*googlesql.ASTPipeMatchRecognize]():                    makeVisitor((*printer).visitPipeMatchRecognize),
		reflect.TypeFor[*googlesql.ASTPipeOrderBy]():                           makeVisitor((*printer).visitPipeOrderBy),
		reflect.TypeFor[*googlesql.ASTPipePivot]():                             makeVisitor((*printer).visitPipePivot),
		reflect.TypeFor[*googlesql.ASTPipeRecursiveUnion]():                    makeVisitor((*printer).visitPipeRecursiveUnion),
		reflect.TypeFor[*googlesql.ASTPipeRename]():                            makeVisitor((*printer).visitPipeRename),
		reflect.TypeFor[*googlesql.ASTPipeRenameItem]():                        makeVisitor((*printer).visitPipeRenameItem),
		reflect.TypeFor[*googlesql.ASTPipeSelect]():                            makeVisitor((*printer).visitPipeSelect),
		reflect.TypeFor[*googlesql.ASTPipeSet]():                               makeVisitor((*printer).visitPipeSet),
		reflect.TypeFor[*googlesql.ASTPipeSetItem]():                           makeVisitor((*printer).visitPipeSetItem),
		reflect.TypeFor[*googlesql.ASTPipeSetOperation]():                      makeVisitor((*printer).visitPipeSetOperation),
		reflect.TypeFor[*googlesql.ASTPipeStaticDescribe]():                    makeVisitor((*printer).visitPipeStaticDescribe),
		reflect.TypeFor[*googlesql.ASTPipeTablesample]():                       makeVisitor((*printer).visitPipeTablesample),
		reflect.TypeFor[*googlesql.ASTPipeTee]():                               makeVisitor((*printer).visitPipeTee),
		reflect.TypeFor[*googlesql.ASTPipeUnpivot]():                           makeVisitor((*printer).visitPipeUnpivot),
		reflect.TypeFor[*googlesql.ASTPipeWhere]():                             makeVisitor((*printer).visitPipeWhere),
		reflect.TypeFor[*googlesql.ASTPipeWindow]():                            makeVisitor((*printer).visitPipeWindow),
		reflect.TypeFor[*googlesql.ASTPipeWith]():                              makeVisitor((*printer).visitPipeWith),
		reflect.TypeFor[*googlesql.ASTPivotClause]():                           makeVisitor((*printer).visitPivotClause),
		reflect.TypeFor[*googlesql.ASTPivotExpression]():                       makeVisitor((*printer).visitPivotExpression),
		reflect.TypeFor[*googlesql.ASTPivotExpressionList]():                   makeVisitor((*printer).visitPivotExpressionList),
		reflect.TypeFor[*googlesql.ASTPivotValue]():                            makeVisitor((*printer).visitPivotValue),
		reflect.TypeFor[*googlesql.ASTPivotValueList]():                        makeVisitor((*printer).visitPivotValueList),
		reflect.TypeFor[*googlesql.ASTPrimaryKey]():                            makeVisitor((*printer).visitPrimaryKey),
		reflect.TypeFor[*googlesql.ASTPrimaryKeyColumnAttribute]():             makeVisitor((*printer).visitPrimaryKeyColumnAttribute),
		reflect.TypeFor[*googlesql.ASTPrimaryKeyElement]():                     makeVisitor((*printer).visitPrimaryKeyElement),
		reflect.TypeFor[*googlesql.ASTPrimaryKeyElementList]():                 makeVisitor((*printer).visitPrimaryKeyElementList),
		reflect.TypeFor[*googlesql.ASTQualify]():                               makeVisitor((*printer).visitQualify),
		reflect.TypeFor[*googlesql.ASTQuantifiedComparisonExpression]():        makeVisitor((*printer).visitQuantifiedComparisonExpression),
		reflect.TypeFor[*googlesql.ASTQuantifierBound]():                       makeVisitor((*printer).visitQuantifierBound),
		reflect.TypeFor[*googlesql.ASTQuery]():                                 makeVisitor((*printer).visitQuery),
		reflect.TypeFor[*googlesql.ASTQueryStatement]():                        makeVisitor((*printer).visitQueryStatement),
		reflect.TypeFor[*googlesql.ASTRaiseStatement]():                        makeVisitor((*printer).visitRaiseStatement),
		reflect.TypeFor[*googlesql.ASTRangeColumnSchema]():                     makeVisitor((*printer).visitRangeColumnSchema),
		reflect.TypeFor[*googlesql.ASTRangeLiteral]():                          makeVisitor((*printer).visitRangeLiteral),
		reflect.TypeFor[*googlesql.ASTRangeType]():                             makeVisitor((*printer).visitRangeType),
		reflect.TypeFor[*googlesql.ASTRebuildAction]():                         makeVisitor((*printer).visitRebuildAction),
		reflect.TypeFor[*googlesql.ASTRemoveFromRestricteeListClause]():        makeVisitor((*printer).visitRemoveFromRestricteeListClause),
		reflect.TypeFor[*googlesql.ASTRenameColumnAction]():                    makeVisitor((*printer).visitRenameColumnAction),
		reflect.TypeFor[*googlesql.ASTRenameToClause]():                        makeVisitor((*printer).visitRenameToClause),
		reflect.TypeFor[*googlesql.ASTRepeatStatement]():                       makeVisitor((*printer).visitRepeatStatement),
		reflect.TypeFor[*googlesql.ASTRepeatableClause]():                      makeVisitor((*printer).visitRepeatableClause),
		reflect.TypeFor[*googlesql.ASTReplaceFieldsExpression]():               makeVisitor((*printer).visitReplaceFieldsExpression),
		reflect.TypeFor[*googlesql.ASTRestrictToClause]():                      makeVisitor((*printer).visitRestrictToClause),
		reflect.TypeFor[*googlesql.ASTReturnStatement]():                       makeVisitor((*printer).visitReturnStatement),
		reflect.TypeFor[*googlesql.ASTReturningClause]():                       makeVisitor((*printer).visitReturningClause),
		reflect.TypeFor[*googlesql.ASTRevokeFromClause]():                      makeVisitor((*printer).visitRevokeFromClause),
		reflect.TypeFor[*googlesql.ASTRevokeStatement]():                       makeVisitor((*printer).visitRevokeStatement),
		reflect.TypeFor[*googlesql.ASTRollbackStatement]():                     makeVisitor((*printer).visitRollbackStatement),
		reflect.TypeFor[*googlesql.ASTRollup]():                                makeVisitor((*printer).visitRollup),
		reflect.TypeFor[*googlesql.ASTRowPatternAnchor]():                      rowPatternVisitor,
		reflect.TypeFor[*googlesql.ASTRowPatternOperation]():                   rowPatternVisitor,
		reflect.TypeFor[*googlesql.ASTRowPatternQuantification]():              rowPatternVisitor,
		reflect.TypeFor[*googlesql.ASTRowPatternVariable]():                    rowPatternVisitor,
		reflect.TypeFor[*googlesql.ASTSampleClause]():                          makeVisitor((*printer).visitSampleClause),
		reflect.TypeFor[*googlesql.ASTSampleSize]():                            makeVisitor((*printer).visitSampleSize),
		reflect.TypeFor[*googlesql.ASTSampleSuffix]():                          makeVisitor((*printer).visitSampleSuffix),
		reflect.TypeFor[*googlesql.ASTScript]():                                makeVisitor((*printer).visitScript),
		reflect.TypeFor[*googlesql.ASTSelect]():                                makeVisitor((*printer).visitSelect),
		reflect.TypeFor[*googlesql.ASTSelectAs]():                              makeVisitor((*printer).visitSelectAs),
		reflect.TypeFor[*googlesql.ASTSelectColumn]():                          makeVisitor((*printer).visitSelectColumn),
		reflect.TypeFor[*googlesql.ASTSelectList]():                            makeVisitor((*printer).visitSelectList),
		reflect.TypeFor[*googlesql.ASTSequenceArg]():                           makeVisitor((*printer).visitSequenceArg),
		reflect.TypeFor[*googlesql.ASTSetAsAction]():                           makeVisitor((*printer).visitSetAsAction),
		reflect.TypeFor[*googlesql.ASTSetCollateClause]():                      makeVisitor((*printer).visitSetCollateClause),
		reflect.TypeFor[*googlesql.ASTSetOperation]():                          makeVisitor((*printer).visitSetOperation),
		reflect.TypeFor[*googlesql.ASTSetOptionsAction]():                      makeVisitor((*printer).visitSetOptionsAction),
		reflect.TypeFor[*googlesql.ASTSetTransactionStatement]():               makeVisitor((*printer).visitSetTransactionStatement),
		reflect.TypeFor[*googlesql.ASTSimpleColumnSchema]():                    makeVisitor((*printer).visitSimpleColumnSchema),
		reflect.TypeFor[*googlesql.ASTSimpleType]():                            makeVisitor((*printer).visitSimpleType),
		reflect.TypeFor[*googlesql.ASTSingleAssignment]():                      makeVisitor((*printer).visitSingleAssignment),
		reflect.TypeFor[*googlesql.ASTSqlFunctionBody]():                       makeVisitor((*printer).visitSQLFunctionBody),
		reflect.TypeFor[*googlesql.ASTStar]():                                  makeVisitor((*printer).visitStar),
		reflect.TypeFor[*googlesql.ASTStarModifiers]():                         makeVisitor((*printer).visitStarModifiers),
		reflect.TypeFor[*googlesql.ASTStarReplaceItem]():                       makeVisitor((*printer).visitStarReplaceItem),
		reflect.TypeFor[*googlesql.ASTStarWithModifiers]():                     makeVisitor((*printer).visitStarWithModifiers),
		reflect.TypeFor[*googlesql.ASTStatementList]():                         makeVisitor((*printer).visitStatementList),
		reflect.TypeFor[*googlesql.ASTStatementWithPipeOperators]():            makeVisitor((*printer).visitStatementWithPipeOperators),
		reflect.TypeFor[*googlesql.ASTStringLiteral]():                         makeVisitor((*printer).visitStringLiteral),
		reflect.TypeFor[*googlesql.ASTStructColumnField]():                     makeVisitor((*printer).visitStructColumnField),
		reflect.TypeFor[*googlesql.ASTStructColumnSchema]():                    makeVisitor((*printer).visitStructColumnSchema),
		reflect.TypeFor[*googlesql.ASTStructConstructorArg]():                  makeVisitor((*printer).visitStructConstructorArg),
		reflect.TypeFor[*googlesql.ASTStructConstructorWithKeyword]():          makeVisitor((*printer).visitStructConstructorWithKeyword),
		reflect.TypeFor[*googlesql.ASTStructConstructorWithParens]():           makeVisitor((*printer).visitStructConstructorWithParens),
		reflect.TypeFor[*googlesql.ASTStructField]():                           makeVisitor((*printer).visitStructField),
		reflect.TypeFor[*googlesql.ASTStructType]():                            makeVisitor((*printer).visitStructType),
		reflect.TypeFor[*googlesql.ASTSubpipeline]():                           makeVisitor((*printer).visitSubpipeline),
		reflect.TypeFor[*googlesql.ASTSubpipelineStatement]():                  makeVisitor((*printer).visitSubpipelineStatement),
		reflect.TypeFor[*googlesql.ASTSymbolQuantifier]():                      quantifierVisitor,
		reflect.TypeFor[*googlesql.ASTSystemVariableAssignment]():              makeVisitor((*printer).visitSystemVariableAssignment),
		reflect.TypeFor[*googlesql.ASTSystemVariableExpr]():                    makeVisitor((*printer).visitSystemVariableExpr),
		reflect.TypeFor[*googlesql.ASTTVF]():                                   makeVisitor((*printer).visitTVF),
		reflect.TypeFor[*googlesql.ASTTVFArgument]():                           makeVisitor((*printer).visitTVFArgument),
		reflect.TypeFor[*googlesql.ASTTVFSchema]():                             makeVisitor((*printer).visitTVFSchema),
		reflect.TypeFor[*googlesql.ASTTVFSchemaColumn]():                       makeVisitor((*printer).visitTVFSchemaColumn),
		reflect.TypeFor[*googlesql.ASTTableClause]():                           makeVisitor((*printer).visitTableClause),
		reflect.TypeFor[*googlesql.ASTTableConstraint]():                       makeVisitor((*printer).visitTableConstraint),
		reflect.TypeFor[*googlesql.ASTTableElementList]():                      makeVisitor((*printer).visitTableElementList),
		reflect.TypeFor[*googlesql.ASTTablePathExpression]():                   makeVisitor((*printer).visitTablePathExpression),
		reflect.TypeFor[*googlesql.ASTTableSubquery]():                         makeVisitor((*printer).visitTableSubquery),
		reflect.TypeFor[*googlesql.ASTTemplatedParameterType]():                makeVisitor((*printer).visitTemplatedParameterType),
		reflect.TypeFor[*googlesql.ASTTruncateStatement]():                     makeVisitor((*printer).visitTruncateStatement),
		reflect.TypeFor[*googlesql.ASTTypeParameterList]():                     makeVisitor((*printer).visitTypeParameterList),
		reflect.TypeFor[*googlesql.ASTUnaryExpression]():                       makeVisitor((*printer).visitUnaryExpression),
		reflect.TypeFor[*googlesql.ASTUndropStatement]():                       makeVisitor((*printer).visitUndropStatement),
		reflect.TypeFor[*googlesql.ASTUnnestExpression]():                      makeVisitor((*printer).visitUnnestExpression),
		reflect.TypeFor[*googlesql.ASTUnnestExpressionWithOptAliasAndOffset](): makeVisitor((*printer).visitUnnestExpressionWithOptAliasAndOffset),
		reflect.TypeFor[*googlesql.ASTUnpivotClause]():                         makeVisitor((*printer).visitUnpivotClause),
		reflect.TypeFor[*googlesql.ASTUnpivotInItem]():                         makeVisitor((*printer).visitUnpivotInItem),
		reflect.TypeFor[*googlesql.ASTUnpivotInItemLabel]():                    makeVisitor((*printer).visitUnpivotInItemLabel),
		reflect.TypeFor[*googlesql.ASTUnpivotInItemList]():                     makeVisitor((*printer).visitUnpivotInItemList),
		reflect.TypeFor[*googlesql.ASTUpdateItem]():                            makeVisitor((*printer).visitUpdateItem),
		reflect.TypeFor[*googlesql.ASTUpdateItemList]():                        makeVisitor((*printer).visitUpdateItemList),
		reflect.TypeFor[*googlesql.ASTUpdateSetValue]():                        makeVisitor((*printer).visitUpdateSetValue),
		reflect.TypeFor[*googlesql.ASTUpdateStatement]():                       makeVisitor((*printer).visitUpdateStatement),
		reflect.TypeFor[*googlesql.ASTUsingClause]():                           makeVisitor((*printer).visitUsingClause),
		reflect.TypeFor[*googlesql.ASTVariableDeclaration]():                   makeVisitor((*printer).visitVariableDeclaration),
		reflect.TypeFor[*googlesql.ASTWhereClause]():                           makeVisitor((*printer).visitWhereClause),
		reflect.TypeFor[*googlesql.ASTWhileStatement]():                        makeVisitor((*printer).visitWhileStatement),
		reflect.TypeFor[*googlesql.ASTWindowClause]():                          makeVisitor((*printer).visitWindowClause),
		reflect.TypeFor[*googlesql.ASTWindowFrame]():                           makeVisitor((*printer).visitWindowFrame),
		reflect.TypeFor[*googlesql.ASTWindowFrameExpr]():                       makeVisitor((*printer).visitWindowFrameExpr),
		reflect.TypeFor[*googlesql.ASTWindowSpecification]():                   makeVisitor((*printer).visitWindowSpecification),
		reflect.TypeFor[*googlesql.ASTWithClause]():                            makeVisitor((*printer).visitWithClause),
		reflect.TypeFor[*googlesql.ASTWithClauseEntry]():                       makeVisitor((*printer).visitWithClauseEntry),
		reflect.TypeFor[*googlesql.ASTWithConnectionClause]():                  makeVisitor((*printer).visitWithConnectionClause),
		reflect.TypeFor[*googlesql.ASTWithExpression]():                        makeVisitor((*printer).visitWithExpression),
		reflect.TypeFor[*googlesql.ASTWithModifier]():                          makeVisitor((*printer).visitWithModifier),
		reflect.TypeFor[*googlesql.ASTWithOffset]():                            makeVisitor((*printer).visitWithOffset),
		reflect.TypeFor[*googlesql.ASTWithPartitionColumnsClause]():            makeVisitor((*printer).visitWithPartitionColumnsClause),
		reflect.TypeFor[*googlesql.ASTWithReportModifier]():                    makeVisitor((*printer).visitWithReportModifier),
		reflect.TypeFor[*googlesql.ASTWithWeight]():                            makeVisitor((*printer).visitWithWeight),
		reflect.TypeFor[*googlesql.ASTYieldItemList]():                         makeVisitor((*printer).visitYieldItemList),
	}
}

func (p *printer) addError(err error) {
	p.errs.append(err)

	if p.logger != nil {
		p.logger.Println("[ERROR]", err)
	}
}

func (p *printer) String() string {
	p.fmt.FlushLine()

	return strings.Trim(p.fmt.formatted.String(), "\n")
}

func (p *printer) moveBefore(n googlesql.ASTNode) {
	if !nodeDefined(n) {
		return
	}

	start, _ := nodeByteRange(n)
	p.fmt.flushCommentsUpTo(start)
}

func (p *printer) movePast(n googlesql.ASTNode) {
	if !nodeDefined(n) {
		return
	}

	_, end := nodeByteRange(n)
	if end > 0 {
		p.fmt.flushCommentsUpTo(end - 1)
	}
}

func (p *printer) moveAt(pos int) {
	p.fmt.flushCommentsUpTo(pos)
}

func (p *printer) movePastLine(n googlesql.ASTNode) {
	if !nodeDefined(n) {
		return
	}

	_, e := nodeByteRange(n)
	newlinePos := p.tracker.Lines.NextLineBreak(e)
	b := p.tracker.MaybeNextPos(e)

	if b == -1 || newlinePos == -1 {
		parent := nodeParent(n)
		if !nodeDefined(parent) || nodeKind(parent) == googlesql.ASTNodeKindAstScript {
			if newlinePos > 0 {
				p.fmt.flushCommentsUpTo(newlinePos)
			} else {
				p.fmt.flushCommentsUpTo(len(p.input))
			}
		}

		return
	}

	if newlinePos < b {
		p.fmt.flushCommentsUpTo(newlinePos)
	}
}

func (p *printer) moveBeforeSuccessorOf(n googlesql.ASTNode) {
	if !nodeDefined(n) {
		return
	}

	_, e := nodeByteRange(n)
	maxPos := e
	parent := nodeParent(n)

	for nodeDefined(parent) && nodeKind(parent) != googlesql.ASTNodeKindAstStatementList {
		_, maxPos = nodeByteRange(parent)
		parent = nodeParent(parent)
	}

	maybeNext := p.tracker.MaybeNextPos(e)
	if maybeNext < 0 {
		return
	}

	if next := min(maybeNext, maxPos); next > 0 {
		p.fmt.flushCommentsUpTo(next - 1)
	}
}

func (p *printer) print(s string) {
	p.fmt.Format(s)
}

func (p *printer) println(s string) {
	p.fmt.FormatLine(s)
}

// printBlankLine emits a forced blank line. FlushLine skips empty buffers, so a
// single space is flushed and stripped later by rowsTrimRight.
func (p *printer) printBlankLine() {
	p.println(" ")
}

func (p *printer) incDepth() {
	p.fmt.depth++
}

func (p *printer) decDepth() {
	p.fmt.depth--
}

// nest returns a child printer sharing comment and error queues with p.
func (p *printer) nest() *printer {
	capacity := max(p.fmt.opts.SoftMaxColumns-p.fmt.currentLineLen(), 40)

	return &printer{
		fmt: &formatterState{
			opts:      p.fmt.opts,
			comments:  p.fmt.comments,
			bqCatalog: p.fmt.bqCatalog,
			sentinels: p.fmt.sentinels,
			maxLength: capacity,
		},
		input:       p.input,
		erasedInput: p.erasedInput,
		tracker:     p.tracker,
		logger:      p.logger,
		errs:        p.errs,
	}
}

func (p *printer) unnest() string {
	return p.unnestWithDepth(1)
}

func (p *printer) unnestWithDepth(d int) string {
	trimmed := p.String()
	aligned := alignNested(trimmed)
	aligned = "\v" + aligned
	alignment := strings.Repeat("\v", d)

	return strings.ReplaceAll(aligned, "\n", "\n"+alignment)
}

func printNestedWithSep[T googlesql.ASTNode](p *printer, items []T, d printerData, sep string) {
	pp := p.nest()

	for i, item := range items {
		if i > 0 {
			pp.print(sep)
		}

		pp.acceptNested(item, d)
	}

	p.print(pp.unnest())
}

func printParenList[T googlesql.ASTNode](p *printer, items []T, simple bool, d printerData) {
	p.print("(")
	printParenBody(p, items, simple, d)
	p.print(")")
}

func printParenBody[T googlesql.ASTNode](p *printer, items []T, simple bool, d printerData) {
	if !simple {
		p.println("")
		p.incDepth()
	}

	for i, item := range items {
		if i > 0 {
			p.print(",")

			if !simple {
				p.println("")
			}
		}

		p.accept(item, d)
	}

	if !simple {
		p.println("")
		p.decDepth()
	}
}

func (p *printer) acceptNested(n googlesql.ASTNode, d printerData) {
	p.print(p.toUnnestedString(n, d))
}

func (p *printer) acceptNestedString(n googlesql.ASTNode, d printerData) {
	p.print(p.toString(n, d))
}

func (p *printer) toString(n googlesql.ASTNode, d printerData) string {
	pp := p.nest()
	pp.accept(n, d)

	return pp.String()
}

func (p *printer) toTrimmedString(n googlesql.ASTNode, d printerData) string {
	return strings.TrimSpace(p.toString(n, d))
}

func (p *printer) toAlignedString(n googlesql.ASTNode, d printerData) string {
	return strings.TrimSpace(alignNested(p.toString(n, d)))
}

func (p *printer) toLeftAlignedString(n googlesql.ASTNode, d printerData) string {
	return strings.TrimSpace(leftAlignNested(p.toString(n, d)))
}

func (p *printer) toUnnestedString(n googlesql.ASTNode, d printerData) string {
	pp := p.nest()
	pp.accept(n, d)

	return pp.unnest()
}

func (p *printer) unnestLeft() string {
	aligned := leftAlignNested(p.String())

	return "\v" + strings.ReplaceAll(aligned, "\n", "\n\v")
}

func (p *printer) printOpenParenIfNeeded(n parenthesizableNode) {
	if p.isParenNeeded(n) {
		p.print("(")

		if isQE, _ := n.IsQueryExpression(); isQE {
			p.println("")
			p.incDepth()
		}
	}
}

func (p *printer) printCloseParenIfNeeded(n parenthesizableNode) {
	if p.isParenNeeded(n) {
		if isQE, _ := n.IsQueryExpression(); isQE {
			p.println("")
			p.decDepth()
		}

		p.print(")")
	}
}

func (p *printer) printOpenParenIfNeededWithDepth(n parenthesizableNode) {
	if p.isParenNeeded(n) {
		p.print("(")
		p.println("")
		p.incDepth()
	}
}

func (p *printer) printCloseParenIfNeededWithDepth(n parenthesizableNode) {
	if p.isParenNeeded(n) {
		p.println("")
		p.decDepth()
		p.print(")")
	}
}

func (p *printer) isParenNeeded(n parenthesizableNode) bool {
	if !nodeDefined(n) {
		return false
	}

	if paren, _ := n.Parenthesized(); paren {
		return true
	}

	if eval, ok := hasLowerPrecedence(nodeParent(n), n); ok && eval {
		return true
	}

	return false
}

func (p *printer) printClause(s string) {
	if p.fmt.opts.NewlineBeforeClause {
		p.println("")
	}

	p.print(s)
}

func (p *printer) identifier(s string) string {
	if len(s) == 0 || s[0] == '`' {
		return s
	}

	if p.fmt.opts.PseudoColumnStyle != Unspecified && isPseudoColumn(s) {
		return applyPrintCase(s, p.fmt.opts.PseudoColumnStyle)
	}

	return applyPrintCase(s, p.fmt.opts.IdentifierStyle)
}

func isPseudoColumn(s string) bool {
	switch strings.ToUpper(s) {
	case "_PARTITIONDATE", "_PARTITIONTIME", "_TABLE_SUFFIX", "_DBT_MAX_PARTITION", "_FILE_NAME", "_ROW_TIMESTAMP":
		return true
	default:
		return false
	}
}

func applyPrintCase(s string, c PrintCase) string {
	switch c {
	case AsIs, Unspecified:
		return s
	case UpperCase:
		return strings.ToUpper(s)
	case LowerCase:
		return strings.ToLower(s)
	}

	return s
}

func (p *printer) function(s string) string {
	if len(s) == 0 || s[0] == '`' {
		return s
	}

	if p.fmt.opts.FunctionCatalog == BigQueryCatalog && p.fmt.bqCatalog != nil {
		if canon := p.lookupBuiltinFunction(s); canon != "" {
			switch p.fmt.opts.BuiltinFunctionNameStyle {
			case AsIs:
				return s
			case LowerCase:
				return strings.ToLower(canon)
			case UpperCase, Unspecified:
				return canon
			}
		}
	}

	return p.fallbackFunction(s)
}

func (p *printer) lookupBuiltinFunction(s string) string {
	if canon := p.fmt.bqCatalog.GetWithFallback(s, ""); canon != "" {
		return canon
	}

	if len(s) > 5 && strings.EqualFold(s[:5], "SAFE.") {
		if canon := p.fmt.bqCatalog.GetWithFallback(s[5:], ""); canon != "" {
			return "SAFE." + canon
		}
	}

	return ""
}

func (p *printer) fallbackFunction(s string) string {
	if len(s) == 0 || s[0] == '`' {
		return s
	}

	return applyPrintCase(s, p.fmt.opts.FunctionNameStyle)
}

func (p *printer) keyword(s string) string {
	return applyPrintCase(s, p.fmt.opts.KeywordStyle)
}

func (p *printer) rawKeywordIdent(n *googlesql.ASTIdentifier) string {
	p.moveBefore(n)

	return p.keyword(p.rawIdentifier(n))
}

func (p *printer) typename(s string) string {
	if len(s) == 0 || s[0] == '`' {
		return s
	}

	return applyPrintCase(s, p.fmt.opts.TypeStyle)
}

func (p *printer) identifierWithCase(s string, c PrintCase) string {
	if len(s) == 0 || s[0] == '`' {
		return s
	}

	if c == Unspecified {
		return p.identifier(s)
	}

	return applyPrintCase(s, c)
}

func (p *printer) nodeInput(n googlesql.ASTNode) string {
	b, e := nodeByteRange(n)

	return p.viewInput(b, e)
}

func (p *printer) nodeErasedInput(n googlesql.ASTNode) string {
	b, e := nodeByteRange(n)

	return p.viewErasedInput(b, e)
}

// viewErasedInput safely slices p.erasedInput[begin:end].
func (p *printer) viewErasedInput(begin, end int) string {
	if begin < 0 || begin > end || end > len(p.erasedInput) {
		return ""
	}

	return p.erasedInput[begin:end]
}

// viewInput safely slices p.input[begin:end].
func (p *printer) viewInput(begin, end int) string {
	if begin < 0 || begin > end || end > len(p.input) {
		return ""
	}

	return p.input[begin:end]
}

func alignNested(s string) string {
	return alignTabwriter(s, tabwriter.AlignRight)
}

func leftAlignNested(s string) string {
	return alignTabwriter(s, 0)
}

func alignTabwriter(s string, flags uint) string {
	if !strings.ContainsAny(s, "\t\v") {
		return strings.Trim(s, "\n")
	}

	var buf strings.Builder

	w := tabwriter.NewWriter(&buf, 0, 0, 0, ' ', flags)
	_, _ = io.WriteString(w, s)
	_ = w.Flush()

	return strings.Trim(buf.String(), "\n")
}

func isInsideOfMergeStatement(n googlesql.ASTNode) bool {
	parent := nodeParent(n)

	return nodeDefined(parent) && nodeKind(parent) == googlesql.ASTNodeKindAstMergeStatement
}

func isInsideOfOnClause(n googlesql.ASTNode) bool {
	for parent := nodeParent(n); nodeDefined(parent); parent = nodeParent(parent) {
		k := nodeKind(parent)
		if k == googlesql.ASTNodeKindAstOnClause {
			return true
		}

		if k != googlesql.ASTNodeKindAstAndExpr && k != googlesql.ASTNodeKindAstOrExpr {
			return false
		}
	}

	return false
}

func isInsideOfWhereClause(n googlesql.ASTNode) bool {
	for parent := nodeParent(n); nodeDefined(parent); parent = nodeParent(parent) {
		k := nodeKind(parent)
		if k == googlesql.ASTNodeKindAstWhereClause {
			return nodeKind(nodeParent(parent)) != googlesql.ASTNodeKindAstFunctionCall
		}

		if k == googlesql.ASTNodeKindAstDeleteStatement ||
			k == googlesql.ASTNodeKindAstUpdateStatement {
			return true
		}

		if k != googlesql.ASTNodeKindAstAndExpr && k != googlesql.ASTNodeKindAstOrExpr {
			return false
		}
	}

	return false
}

func hasLowerPrecedence(parent, child googlesql.ASTNode) (eval, ok bool) {
	if !nodeDefined(parent) || !nodeDefined(child) {
		return false, false
	}

	p := precedenceNum(parent)
	c := lowestPrecedenceBelow(child)

	eval = p > 0 && c > 0 && p > c
	ok = p < 1000 && c < 1000

	return eval, ok
}

func lowestPrecedenceBelow(n googlesql.ASTNode) int {
	switch t := n.(type) {
	case *googlesql.ASTBinaryExpression:
		lhsNode, _ := t.Lhs()
		rhsNode, _ := t.Rhs()

		return min(precedenceNum(n), precedenceNum(lhsNode), precedenceNum(rhsNode))
	case *googlesql.ASTBitwiseShiftExpression:
		lhsNode, _ := t.Lhs()
		rhsNode, _ := t.Rhs()

		return min(precedenceNum(n), precedenceNum(lhsNode), precedenceNum(rhsNode))
	case *googlesql.ASTConcatExpr:
		minPrec := precedenceNum(n)

		for _, op := range selectChildrenOfType[googlesql.ASTExpressionNode](t) {
			minPrec = min(minPrec, precedenceNum(op))
		}

		return minPrec
	default:
		return 1000
	}
}

func precedenceNum(n googlesql.ASTNode) int {
	if !nodeDefined(n) {
		return 1000
	}

	switch nodeKind(n) {
	case googlesql.ASTNodeKindAstDotStar:
		return 1
	case googlesql.ASTNodeKindAstOrExpr:
		return 2
	case googlesql.ASTNodeKindAstAndExpr:
		return 3
	case googlesql.ASTNodeKindAstUnaryExpression:
		if u, ok := n.(*googlesql.ASTUnaryExpression); ok {
			return precedenceUnaryExpr(u)
		}
	case googlesql.ASTNodeKindAstBinaryExpression:
		if b, ok := n.(*googlesql.ASTBinaryExpression); ok {
			return precedenceBinExpr(b)
		}
	case googlesql.ASTNodeKindAstBitwiseShiftExpression:
		return 9
	case googlesql.ASTNodeKindAstConcatExpr:
		return 11
	case googlesql.ASTNodeKindAstBetweenExpression,
		googlesql.ASTNodeKindAstInExpression,
		googlesql.ASTNodeKindAstLikeExpression,
		googlesql.ASTNodeKindAstQuantifiedComparisonExpression:
		return 5
	default:
		return 1000
	}

	return 1000
}

func precedenceBinExpr(n *googlesql.ASTBinaryExpression) int {
	op, _ := n.Op()

	switch op {
	case googlesql.ASTBinaryExpressionEnums_OpNotSet:
		return -1
	case googlesql.ASTBinaryExpressionEnums_OpEq,
		googlesql.ASTBinaryExpressionEnums_OpNe,
		googlesql.ASTBinaryExpressionEnums_OpNe2,
		googlesql.ASTBinaryExpressionEnums_OpGt,
		googlesql.ASTBinaryExpressionEnums_OpGe,
		googlesql.ASTBinaryExpressionEnums_OpLt,
		googlesql.ASTBinaryExpressionEnums_OpLe,
		googlesql.ASTBinaryExpressionEnums_OpLike,
		googlesql.ASTBinaryExpressionEnums_OpDistinct,
		googlesql.ASTBinaryExpressionEnums_OpIs:
		return 5
	case googlesql.ASTBinaryExpressionEnums_OpBitwiseOr:
		return 6
	case googlesql.ASTBinaryExpressionEnums_OpBitwiseXor:
		return 7
	case googlesql.ASTBinaryExpressionEnums_OpBitwiseAnd:
		return 8
	case googlesql.ASTBinaryExpressionEnums_OpPlus, googlesql.ASTBinaryExpressionEnums_OpMinus:
		return 10
	case googlesql.ASTBinaryExpressionEnums_OpConcatOp:
		return 11
	case googlesql.ASTBinaryExpressionEnums_OpMultiply, googlesql.ASTBinaryExpressionEnums_OpDivide:
		return 12
	default:
		return 1000
	}
}

func precedenceUnaryExpr(n *googlesql.ASTUnaryExpression) int {
	op, _ := n.Op()

	switch op {
	case googlesql.ASTUnaryExpressionEnums_OpNotSet:
		return 0
	case googlesql.ASTUnaryExpressionEnums_OpNot:
		return 4
	case googlesql.ASTUnaryExpressionEnums_OpBitwiseNot,
		googlesql.ASTUnaryExpressionEnums_OpMinus,
		googlesql.ASTUnaryExpressionEnums_OpPlus,
		googlesql.ASTUnaryExpressionEnums_OpIsUnknown,
		googlesql.ASTUnaryExpressionEnums_OpIsNotUnknown:
		return 13
	}

	return -1
}
