package formatter

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"text/tabwriter"

	"github.com/goccy/go-zetasql/ast"
	"github.com/hashicorp/go-multierror"
)

type Printer struct {
	fmt         *Formatter
	input       string
	erasedInput string
	tracker     *LocationTracker
	err         error
}

type Data map[string]int

func (d Data) Enable(key string) {
	d[key] = 1
}

func (d Data) SetBool(key string, value bool) {
	if value {
		d[key] = 1
	} else {
		d[key] = 0
	}
}

func (d Data) IsEnabled(key string) bool {
	if v, ok := d[key]; ok && v > 0 {
		return true
	}

	return false
}

func (p *Printer) acceptClause(n ast.Node, d Data) {
	p.visit(n, p.fmt.opts.NewlineBeforeClause, d)
}

// accept visits a node on current line.
func (p *Printer) accept(n ast.Node, d Data) {
	p.visit(n, false, d)
}

// accept visits a node in a new line. If the node is not defined, no
// line is created.
func (p *Printer) lnaccept(n ast.Node, d Data) {
	p.visit(n, true, d)
}

func (p *Printer) visit(n ast.Node, newline bool, d Data) {
	if !nodeDefined(n) {
		return
	}

	if newline {
		p.println("")
	}

	switch m := n.(type) {
	case *ast.AddColumnActionNode:
		p.VisitAddColumnAction(m, d)
	case *ast.AddConstraintActionNode:
		p.VisitAddConstraintAction(m, d)
	case *ast.AliasNode:
		p.VisitAlias(m, d)
	case *ast.AlterActionListNode:
		p.VisitAlterActionList(m, d)
	case *ast.AlterAllRowAccessPoliciesStatementNode:
		p.VisitAlterAllRowAccessPoliciesStatement(m, d)
	case *ast.AlterColumnDropDefaultActionNode:
		p.VisitAlterColumnDropDefaultAction(m, d)
	case *ast.AlterColumnDropNotNullActionNode:
		p.VisitAlterColumnDropNotNullAction(m, d)
	case *ast.AlterColumnOptionsActionNode:
		p.VisitAlterColumnOptionsAction(m, d)
	case *ast.AlterColumnSetDefaultActionNode:
		p.VisitAlterColumnSetDefaultAction(m, d)
	case *ast.AlterColumnTypeActionNode:
		p.VisitAlterColumnTypeAction(m, d)
	case *ast.AlterConstraintEnforcementActionNode:
		p.VisitAlterConstraintEnforcementAction(m, d)
	case *ast.AlterConstraintSetOptionsActionNode:
		p.VisitAlterConstraintSetOptionsAction(m, d)
	case *ast.AlterDatabaseStatementNode:
		p.VisitAlterDatabaseStatement(m, d)
	case *ast.AlterEntityStatementNode:
		p.VisitAlterEntityStatement(m, d)
	case *ast.AlterMaterializedViewStatementNode:
		p.VisitAlterMaterializedViewStatement(m, d)
	case *ast.AlterPrivilegeRestrictionStatementNode:
		p.VisitAlterPrivilegeRestrictionStatement(m, d)
	case *ast.AlterRowAccessPolicyStatementNode:
		p.VisitAlterRowAccessPolicyStatement(m, d)
	case *ast.AlterSchemaStatementNode:
		p.VisitAlterSchemaStatement(m, d)
	case *ast.AlterTableStatementNode:
		p.VisitAlterTableStatement(m, d)
	case *ast.AlterViewStatementNode:
		p.VisitAlterViewStatement(m, d)
	case *ast.AnalyticFunctionCallNode:
		p.VisitAnalyticFunctionCall(m, d)
	case *ast.AndExprNode:
		p.VisitAndExpr(m, d)
	case *ast.ArrayConstructorNode:
		p.VisitArrayConstructor(m, d)
	case *ast.ArrayElementNode:
		p.VisitArrayElement(m, d)
	case *ast.ArrayTypeNode:
		p.VisitArrayType(m, d)
	case *ast.AssignmentFromStructNode:
		p.VisitAssignmentFromStruct(m, d)
	case *ast.BeginEndBlockNode:
		p.VisitBeginEndBlock(m, d)
	case *ast.BeginStatementNode:
		p.VisitBeginStatementNode(m, d)
	case *ast.BetweenExpressionNode:
		p.VisitBetweenExpression(m, d)
	case *ast.BigNumericLiteralNode:
		p.VisitBigNumericLiteral(m, d)
	case *ast.BinaryExpressionNode:
		p.VisitBinaryExpression(m, d)
	case *ast.BitwiseShiftExpressionNode:
		p.VisitBitwiseShiftExpression(m, d)
	case *ast.BooleanLiteralNode:
		p.VisitBoolLiteral(m, d)
	case *ast.BytesLiteralNode:
		p.VisitBytesLiteral(m, d)
	case *ast.CallStatementNode:
		p.VisitCallStatement(m, d)
	case *ast.CaseNoValueExpressionNode:
		p.VisitCaseNoValueExpression(m, d)
	case *ast.CaseValueExpressionNode:
		p.VisitCaseValueExpression(m, d)
	case *ast.CastExpressionNode:
		p.VisitCastExpression(m, d)
	case *ast.ClampedBetweenModifierNode:
		p.VisitClampedBetweenModifier(m, d)
	case *ast.CloneDataSourceNode:
		p.VisitCloneDataSource(m, d)
	case *ast.ClusterByNode:
		p.VisitClusterBy(m, d)
	case *ast.CollateNode:
		p.VisitCollate(m, d)
	case *ast.ColumnAttributeListNode:
		p.VisitColumnAttributeList(m, d)
	case *ast.ColumnDefinitionNode:
		p.VisitColumnDefinition(m, d)
	case *ast.ColumnListNode:
		p.VisitColumnList(m, d)
	case *ast.CommitStatementNode:
		p.VisitCommitStatement(m, d)
	case *ast.ConnectionClauseNode:
		p.VisitConnectionClause(m, d)
	case *ast.ColumnSchemaNode:
		p.VisitColumnSchema(m, d)
	case *ast.CopyDataSourceNode:
		p.VisitCopyDataSource(m, d)
	case *ast.CreateExternalTableStatementNode:
		p.VisitCreateExternalTableStatement(m, d)
	case *ast.CreateFunctionStatementNode:
		p.VisitCreateFunctionStatement(m, d)
	case *ast.CreateMaterializedViewStatementNode:
		p.VisitCreateMaterializedViewStatement(m, d)
	case *ast.CreateProcedureStatementNode:
		p.VisitCreateProcedureStatement(m, d)
	case *ast.CreateRowAccessPolicyStatementNode:
		p.VisitCreateRowAccessPolicyStatement(m, d)
	case *ast.CreateSchemaStatementNode:
		p.VisitCreateSchemaStatement(m, d)
	case *ast.CreateSnapshotTableStatementNode:
		p.VisitCreateSnapshotTableStatement(m, d)
	case *ast.CreateTableStatementNode:
		p.VisitCreateTableStatement(m, d)
	case *ast.CreateTableFunctionStatementNode:
		p.VisitCreateTableFunctionStatement(m, d)
	case *ast.CreateViewStatementNode:
		p.VisitCreateViewStatement(m, d)
	case *ast.DateOrTimeLiteralNode:
		p.VisitDateOrTimeLiteral(m, d)
	case *ast.DescriptorNode:
		p.VisitDescriptor(m, d)
	case *ast.DescriptorColumnNode:
		p.VisitDescriptorColumn(m, d)
	case *ast.DescriptorColumnListNode:
		p.VisitDescriptorColumnList(m, d)
	case *ast.DotIdentifierNode:
		p.VisitDotIdentifier(m, d)
	case *ast.DotGeneralizedFieldNode:
		p.VisitDotGeneralizedField(m, d)
	case *ast.DotStarNode:
		p.VisitDotStar(m, d)
	case *ast.DotStarWithModifiersNode:
		p.VisitDotStarWithModifiers(m, d)
	case *ast.DropAllRowAccessPoliciesStatementNode:
		p.VisitDropAllRowAccessPoliciesStatement(m, d)
	case *ast.DropColumnActionNode:
		p.VisitDropColumnAction(m, d)
	case *ast.DropConstraintActionNode:
		p.VisitDropConstraintAction(m, d)
	case *ast.DropEntityStatementNode:
		p.VisitDropEntityStatement(m, d)
	case *ast.DropFunctionStatementNode:
		p.VisitDropFunctionStatement(m, d)
	case *ast.DropMaterializedViewStatementNode:
		p.VisitDropMaterializedViewStatement(m, d)
	case *ast.DropPrimaryKeyActionNode:
		p.VisitDropPrimaryKeyAction(m, d)
	case *ast.DropPrivilegeRestrictionStatementNode:
		p.VisitDropPrivilegeRestrictionStatement(m, d)
	case *ast.DropRowAccessPolicyStatementNode:
		p.VisitDropRowAccessPolicyStatement(m, d)
	case *ast.DropSearchIndexStatementNode:
		p.VisitDropSearchIndexStatement(m, d)
	case *ast.DropSnapshotTableStatementNode:
		p.VisitDropSnapshotTableStatement(m, d)
	case *ast.DropTableFunctionStatementNode:
		p.VisitDropTableFunctionStatement(m, d)
	case *ast.DropStatementNode:
		p.VisitDropStatement(m, d)
	case *ast.ExceptionHandlerListNode:
		p.VisitExceptionHandlerListNode(m, d)
	case *ast.ExceptionHandlerNode:
		p.VisitExceptionHandlerNode(m, d)
	case *ast.ExecuteIntoClauseNode:
		p.VisitExecuteIntoClause(m, d)
	case *ast.ExecuteImmediateStatementNode:
		p.VisitExecuteImmediateStatement(m, d)
	case *ast.ExecuteUsingArgumentNode:
		p.VisitExecuteUsingArgument(m, d)
	case *ast.ExecuteUsingClauseNode:
		p.VisitExecuteUsingClause(m, d)
	case *ast.ExpressionSubqueryNode:
		p.VisitExpressionSubquery(m, d)
	case *ast.ExtractExpressionNode:
		p.VisitExtractExpression(m, d)
	case *ast.FloatLiteralNode:
		p.VisitFloatLiteral(m, d)
	case *ast.FilterUsingClauseNode:
		p.VisitFilterUsingClause(m, d)
	case *ast.ForeignKeyNode:
		p.VisitForeignKey(m, d)
	case *ast.ForeignKeyReferenceNode:
		p.VisitForeignKeyReference(m, d)
	case *ast.FormatClauseNode:
		p.VisitFormatClause(m, d)
	case *ast.ForSystemTimeNode:
		p.VisitForSystemTime(m, d)
	case *ast.FromClauseNode:
		p.VisitFromClause(m, d)
	case *ast.FunctionCallNode:
		p.VisitFunctionCall(m, d)
	case *ast.FunctionDeclarationNode:
		p.VisitFunctionDeclaration(m, d)
	case *ast.FunctionParameterNode:
		p.VisitFunctionParameter(m, d)
	case *ast.FunctionParametersNode:
		p.VisitFunctionParameters(m, d)
	case *ast.GranteeListNode:
		p.VisitGranteeList(m, d)
	case *ast.GrantToClauseNode:
		p.VisitGrantToClause(m, d)
	case *ast.GroupByNode:
		p.VisitGroupBy(m, d)
	case *ast.GroupingItemNode:
		p.VisitGroupingItem(m, d)
	case *ast.HavingModifierNode:
		p.VisitHavingModifier(m, d)
	case *ast.HavingNode:
		p.VisitHaving(m, d)
	case *ast.HintNode:
		p.VisitHint(m, d)
	case *ast.HintedStatementNode:
		p.VisitHintedStatement(m, d)
	case *ast.IdentifierNode:
		p.VisitIdentifier(m, d)
	case *ast.IdentifierListNode:
		p.VisitIdentifierList(m, d)
	case *ast.IfStatementNode:
		p.VisitIfStatement(m, d)
	case *ast.InExpressionNode:
		p.VisitInExpression(m, d)
	case *ast.InListNode:
		p.VisitInList(m, d)
	case *ast.IntervalExprNode:
		p.VisitIntervalExpr(m, d)
	case *ast.IntLiteralNode:
		p.VisitIntLiteral(m, d)
	case *ast.InsertStatementNode:
		p.VisitInsertStatement(m, d)
	case *ast.InsertValuesRowListNode:
		p.VisitInsertValuesRowList(m, d)
	case *ast.InsertValuesRowNode:
		p.VisitInsertValuesRow(m, d)
	case *ast.JoinNode:
		p.VisitJoin(m, d)
	case *ast.LimitOffsetNode:
		p.VisitLimitOffset(m, d)
	case *ast.MergeActionNode:
		p.VisitMergeAction(m, d)
	case *ast.MergeStatementNode:
		p.VisitMergeStatement(m, d)
	case *ast.MergeWhenClauseNode:
		p.VisitMergeWhenClause(m, d)
	case *ast.MergeWhenClauseListNode:
		p.VisitMergeWhenClauseList(m, d)
	case *ast.ModelClauseNode:
		p.VisitModelClause(m, d)
	case *ast.NamedArgumentNode:
		p.VisitNamedArgument(m, d)
	case *ast.NotNullColumnAttributeNode:
		p.VisitNotNullColumnAttribute(m, d)
	case *ast.NullLiteralNode:
		p.VisitNullLiteral(m, d)
	case *ast.NullOrderNode:
		p.VisitNullOrder(m, d)
	case *ast.NumericLiteralNode:
		p.VisitNumericLiteral(m, d)
	case *ast.OnClauseNode:
		p.VisitOnClause(m, d)
	case *ast.OptionsListNode:
		p.VisitOptionsList(m, d)
	case *ast.OptionsEntryNode:
		p.VisitOptionsEntry(m, d)
	case *ast.OrExprNode:
		p.VisitOrExpr(m, d)
	case *ast.OrderByNode:
		p.VisitOrderBy(m, d)
	case *ast.OrderingExpressionNode:
		p.VisitOrderingExpression(m, d)
	case *ast.ParameterAssignmentNode:
		p.VisitParameterAssignment(m, d)
	case *ast.ParameterExprNode:
		p.VisitParameterExpr(m, d)
	case *ast.ParenthesizedJoinNode:
		p.VisitParenthesizedJoin(m, d)
	case *ast.PartitionByNode:
		p.VisitPartitionBy(m, d)
	case *ast.PathExpressionListNode:
		p.VisitPathExpressionList(m, d)
	case *ast.PathExpressionNode:
		p.VisitPathExpression(m, d)
	case *ast.PivotClauseNode:
		p.VisitPivotClause(m, d)
	case *ast.PivotExpressionNode:
		p.VisitPivotExpression(m, d)
	case *ast.PivotExpressionListNode:
		p.VisitPivotExpressionList(m, d)
	case *ast.PivotValueNode:
		p.VisitPivotValue(m, d)
	case *ast.PivotValueListNode:
		p.VisitPivotValueList(m, d)
	case *ast.PrimaryKeyNode:
		p.VisitPrimaryKey(m, d)
	case *ast.PrimaryKeyColumnAttributeNode:
		p.VisitPrimaryKeyColumnAttribute(m, d)
	case *ast.QualifyNode:
		p.VisitQualify(m, d)
	case *ast.QueryNode:
		p.VisitQuery(m, d)
	case *ast.QueryStatementNode:
		p.VisitQueryStatement(m, d)
	case *ast.RenameColumnActionNode:
		p.VisitRenameColumnAction(m, d)
	case *ast.RenameToClauseNode:
		p.VisitRenameToClause(m, d)
	case *ast.RepeatableClauseNode:
		p.VisitRepeatableClause(m, d)
	case *ast.ReturnStatementNode:
		p.VisitReturnStatement(m, d)
	case *ast.RollbackStatementNode:
		p.VisitRollbackStatementNode(m, d)
	case *ast.RollupNode:
		p.VisitRollup(m, d)
	case *ast.SampleClauseNode:
		p.VisitSampleClause(m, d)
	case *ast.SampleSizeNode:
		p.VisitSampleSize(m, d)
	case *ast.SampleSuffixNode:
		p.VisitSampleSuffix(m, d)
	case *ast.SetCollateClauseNode:
		p.VisitSetCollateClause(m, d)
	case *ast.ScriptBaseNode:
		p.VisitScript(m, d)
	case *ast.SelectNode:
		p.VisitSelect(m, d)
	case *ast.SelectAsNode:
		p.VisitSelectAs(m, d)
	case *ast.SelectColumnNode:
		p.VisitSelectColumn(m, d)
	case *ast.SelectListNode:
		p.VisitSelectList(m, d)
	case *ast.SetOptionsActionNode:
		p.VisitSetOptionsAction(m, d)
	case *ast.SetOperationNode:
		p.VisitSetOperation(m, d)
	case *ast.SimpleColumnSchemaNode:
		p.VisitSimpleColumnSchema(m, d)
	case *ast.SimpleTypeNode:
		p.VisitSimpleType(m, d)
	case *ast.SqlFunctionBodyNode:
		p.VisitSQLFunctionBody(m, d)
	case *ast.StarNode:
		p.VisitStar(m, d)
	case *ast.StarModifiersNode:
		p.VisitStarModifiers(m, d)
	case *ast.StarReplaceItemNode:
		p.VisitStarReplaceItem(m, d)
	case *ast.StarWithModifiersNode:
		p.VisitStarWithModifiers(m, d)
	case *ast.StatementListNode:
		p.VisitStatementList(m, d)
	case *ast.StringLiteralNode:
		p.VisitStringLiteral(m, d)
	case *ast.StructColumnFieldNode:
		p.VisitStructColumnField(m, d)
	case *ast.StructColumnSchemaNode:
		p.VisitStructColumnSchema(m, d)
	case *ast.StructConstructorArgNode:
		p.VisitStructConstructorArg(m, d)
	case *ast.StructConstructorWithKeywordNode:
		p.VisitStructConstructorWithKeyword(m, d)
	case *ast.StructConstructorWithParensNode:
		p.VisitStructConstructorWithParens(m, d)
	case *ast.StructFieldNode:
		p.VisitStructField(m, d)
	case *ast.StructTypeNode:
		p.VisitStructType(m, d)
	case *ast.SystemVariableAssignmentNode:
		p.VisitSystemVariableAssignment(m, d)
	case *ast.SystemVariableExprNode:
		p.VisitSystemVariableExpr(m, d)
	case *ast.TableClauseNode:
		p.VisitTableClause(m, d)
	case *ast.TableConstraintBaseNode:
		p.VisitTableConstraint(m, d)
	case *ast.TableElementListNode:
		p.VisitTableElementList(m, d)
	case *ast.TablePathExpressionNode:
		p.VisitTablePathExpression(m, d)
	case *ast.TableSubqueryNode:
		p.VisitTableSubquery(m, d)
	case *ast.TemplatedParameterTypeNode:
		p.VisitTemplatedParameterType(m, d)
	case *ast.TrucateStatementNode:
		p.VisitTruncateStatement(m, d)
	case *ast.TVFArgumentNode:
		p.VisitTVFArgument(m, d)
	case *ast.TVFNode:
		p.VisitTVF(m, d)
	case *ast.TVFSchemaNode:
		p.VisitTVFSchema(m, d)
	case *ast.TVFSchemaColumnNode:
		p.VisitTVFSchemaColumn(m, d)
	case *ast.TypeParameterListNode:
		p.VisitTypeParameterList(m, d)
	case *ast.UnpivotClauseNode:
		p.VisitUnpivotClause(m, d)
	case *ast.UnaryExpressionNode:
		p.VisitUnaryExpression(m, d)
	case *ast.UnpivotInItemLabelNode:
		p.VisitUnpivotInItemLabel(m, d)
	case *ast.UnpivotInItemListNode:
		p.VisitUnpivotInItemList(m, d)
	case *ast.UnpivotInItemNode:
		p.VisitUnpivotInItem(m, d)
	case *ast.UnnestExpressionNode:
		p.VisitUnnestExpression(m, d)
	case *ast.UpdateItemNode:
		p.VisitUpdateItem(m, d)
	case *ast.UpdateItemListNode:
		p.VisitUpdateItemList(m, d)
	case *ast.UpdateSetValueNode:
		p.VisitUpdateSetValue(m, d)
	case *ast.UsingClauseNode:
		p.VisitUsingClause(m, d)
	case *ast.VariableDeclarationNode:
		p.VisitVariableDeclaration(m, d)
	case *ast.SingleAssignmentNode:
		p.VisitSingleAssignment(m, d)
	case *ast.WhereClauseNode:
		p.VisitWhereClause(m, d)
	case *ast.WindowClauseNode:
		p.VisitWindowClause(m, d)
	case *ast.WindowFrameNode:
		p.VisitWindowFrame(m, d)
	case *ast.WindowFrameExprNode:
		p.VisitWindowFrameExpr(m, d)
	case *ast.WindowSpecificationNode:
		p.VisitWindowSpecification(m, d)
	case *ast.WithClauseNode:
		p.VisitWithClause(m, d)
	case *ast.WithClauseEntryNode:
		p.VisitWithClauseEntry(m, d)
	case *ast.WithConnectionClauseNode:
		p.VisitWithConnectionClause(m, d)
	case *ast.WithOffsetNode:
		p.VisitWithOffset(m, d)
	case *ast.WithPartitionColumnsClauseNode:
		p.VisitWithPartitionColumnsClause(m, d)
	case *ast.WithWeightNode:
		p.VisitWithWeight(m, d)

	default:
		p.addError(&PrinterError{
			Err:  nil,
			Msg:  fmt.Sprintf("not implemented for %#v", n),
			Node: n,
		})
	}
}

func (p *Printer) addError(err error) {
	p.err = multierror.Append(p.err, err)
	log.Println("[ERROR]", err)
}

func (p *Printer) String() string {
	p.fmt.FlushLine()
	return strings.Trim(p.fmt.formatted.String(), "\n")
}

func (p *Printer) moveBefore(n ast.Node) {
	loc := n.ParseLocationRange()
	p.fmt.flushCommentsUpTo(loc.Start().ByteOffset())
}

func (p *Printer) movePast(n ast.Node) {
	loc := n.ParseLocationRange()
	p.fmt.flushCommentsUpTo(loc.End().ByteOffset())
}

func (p *Printer) moveAt(pos int) {
	p.fmt.flushCommentsUpTo(pos)
}

// movePastLine scans from the end of a node to the end of the line or
// until the next node.
// We do this limited to the end of the parent's end location.
func (p *Printer) movePastLine(n ast.Node) {
	loc := n.ParseLocationRange()
	e := loc.End().ByteOffset()
	// _, le := p.tracker.Lines.Span(n)

	newlinePos := p.tracker.Lines.NextLineBreak(e)
	b := p.tracker.MaybeNextPos(e)

	if b == -1 || newlinePos == -1 {
		// Only flush comments if at the top level.
		parent := n.Parent()

		if parent == nil || parent.Kind() == ast.Script {
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

	// lb, _ := p.tracker.Lines.Span(n)

	// if le == lb {
	// 	// No tokens are available in the same line, find the next
	// 	// line break and flush comments until the end of the line.

	// 	p.fmt.flushCommentsUpTo(lb)
	// }
}

// moveBeforeSuccessorOf move cursor to before the start of the
// succeding start position.
func (p *Printer) moveBeforeSuccessorOf(n ast.Node) {
	loc := n.ParseLocationRange()
	e := loc.End().ByteOffset()

	// Limit this kind of comment flush up to the statement level.
	// A statement list is flat aligned and we will not have problems
	// with comment indentation at that level or higher.
	max := e
	parent := n.Parent()

	for nodeDefined(parent) && parent.Kind() != ast.StatementList {
		max = parent.ParseLocationRange().End().ByteOffset()
		parent = parent.Parent()
	}

	next := p.tracker.MaybeNextPos(e)

	if next > max {
		next = max
	}

	if next > 0 {
		p.fmt.flushCommentsUpTo(next)
	}
}

func (p *Printer) print(s string) {
	p.fmt.Format(s)
}

func (p *Printer) println(s string) {
	p.fmt.FormatLine(s)
}

func (p *Printer) incDepth() {
	p.fmt.depth++
}

func (p *Printer) decDepth() {
	p.fmt.depth--
}

// nest returns a new printer with the same options to perform printing
// on a nested section of the tree.
func (p *Printer) nest() *Printer {
	buf := p.fmt.buffer.String()
	currSize := strings.LastIndex(buf, "\n")
	if currSize < 0 {
		currSize = len(p.fmt.buffer.String())
	}

	capacity := p.fmt.opts.SoftMaxColumns - currSize

	// Some scripts with lots of nested printers could lead to very
	// small or even negative maximum length.  We allow at least some
	// characters per-line at any given nested level.
	if capacity < 40 {
		capacity = 40
	}

	n := &Printer{
		fmt: &Formatter{
			opts:      p.fmt.opts,
			comments:  p.fmt.comments,
			maxLength: capacity,
		},
		input:       p.input,
		erasedInput: p.erasedInput,
		tracker:     p.tracker,
		err:         nil,
	}

	return n
}

// unnest flushes the buffer and returns the strings with alignment
// symbols at the beginning of each line.
func (p *Printer) unnest() string {
	trimmed := p.String()
	aligned := alignNested(trimmed)
	aligned = "\v" + aligned
	aligned = strings.ReplaceAll(aligned, "\n", "\n\v")

	return aligned
}

// unnest flushes the buffer and returns the strings with alignment
// symbols at the beginning of each line.
func (p *Printer) unnestWithDepth(d int) string {
	trimmed := p.String()
	aligned := alignNested(trimmed)
	aligned = "\v" + aligned
	alignment := strings.Repeat("\v", d)
	aligned = strings.ReplaceAll(aligned, "\n", "\n"+alignment)

	return aligned
}

// printNestedWithSep receives a slice of ast.Node items and print each
// in a nested printer.
// Since we cannot have generic methods, this is a function that receives
// a printer as the first argument.  Otherwise, it would be a method.
func printNestedWithSep[T ast.Node](p *Printer, items []T, d Data, sep string) {
	pp := p.nest()

	for i, item := range items {
		if i > 0 {
			pp.print(sep)
		}

		pp.acceptNested(item, d)
	}

	p.print(pp.unnest())
}

// acceptNested visits a node with a nested printer.
func (p *Printer) acceptNested(n ast.Node, d Data) {
	pp := p.nest()
	pp.accept(n, d)
	p.print(pp.unnest())
}

// acceptNestedLeft visits a node with a nested printer, and unnests
// result with left alignment.
func (p *Printer) acceptNestedLeft(n ast.Node, d Data) {
	pp := p.nest()
	pp.accept(n, d)
	p.print(pp.unnest())
}

// acceptNestedString visits a node with a nested printer, and prints
// result to current printer as a string.
func (p *Printer) acceptNestedString(n ast.Node, d Data) {
	pp := p.nest()
	pp.accept(n, d)
	p.print(pp.String())
}

// toString visits a node with a nested printer and returns its
// string contents instead of writing to current printer.
func (p *Printer) toString(n ast.Node, d Data) string {
	pp := p.nest()
	pp.accept(n, d)

	return pp.String()
}

// toUnnestedString visits a node with a nested printer and returns its
// unnested string contents .
func (p *Printer) toUnnestedString(n ast.Node, d Data) string {
	pp := p.nest()
	pp.accept(n, d)

	return pp.unnest()
}

func debugContent(s string) string {
	d := strings.ReplaceAll(s, "\v", "|")
	d = strings.ReplaceAll(d, "\b", "%")

	return d
}

// unnest flushes the buffer and returns the strings with alignment
// symbols at the beginning of each line.
func (p *Printer) unnestLeft() string {
	aligned := leftAlignNested(p.String())
	return "\v" + strings.ReplaceAll(aligned, "\n", "\n\v")
}

func (p *Printer) printOpenParenIfNeeded(n ast.QueryExpressionNode) {
	if p.isParenNeeded(n) {
		p.print("(")

		if n.IsQueryExpression() {
			p.println("")
			p.incDepth()
		}
	}
}

func (p *Printer) printCloseParenIfNeeded(n ast.QueryExpressionNode) {
	if p.isParenNeeded(n) {
		if n.IsQueryExpression() {
			p.println("")
			p.decDepth()
		}

		p.print(")")
	}
}

func (p *Printer) printOpenParenIfNeededWithDepth(n ast.QueryExpressionNode) {
	if p.isParenNeeded(n) {
		p.print("(")
		p.println("")
		p.incDepth()
	}
}

func (p *Printer) printCloseParenIfNeededWithDepth(n ast.QueryExpressionNode) {
	if p.isParenNeeded(n) {
		p.println("")
		p.decDepth()
		p.print(")")
	}
}

func (p *Printer) isParenNeeded(n ast.QueryExpressionNode) bool {
	if n.Parenthesized() {
		return true
	}

	if eval, ok := hasLowerPrecedence(n.Parent(), n); ok && eval {
		return true
	}

	return false
}

// hasParenAround checks if there is parenthesis just before the start
// location of a node.
func (p *Printer) hasParenAround(n ast.Node) bool {
	s := n.ParseLocationRange().Start().ByteOffset()
	if s == 0 {
		return false
	}

	return p.input[s-1] == '('
}

func (p *Printer) printClause(s string) {
	if p.fmt.opts.NewlineBeforeClause {
		p.println("")
	}

	p.print(s)
}

func (p *Printer) identifier(s string) string {
	if len(s) > 0 && s[0] == '`' {
		return s
	}

	switch p.fmt.opts.IdentifierStyle {
	case AsIs:
		return s
	case UpperCase:
		return strings.ToUpper(s)
	case LowerCase:
		return strings.ToLower(s)
	}

	return ""
}

func (p *Printer) function(s string) string {
	if s[0] == '`' {
		return s
	}

	name := p.fallbackFunction(s)

	if p.fmt.opts.FunctionCatalog == BigQueryCatalog {
		return bigqueryFunctions.GetWithFallback(s, name)
	}

	return name
}

func (p *Printer) fallbackFunction(s string) string {
	if s[0] == '`' {
		return s
	}

	switch p.fmt.opts.FunctionNameStyle {
	case AsIs, Unspecified:
		return s
	case UpperCase:
		return strings.ToUpper(s)
	case LowerCase:
		return strings.ToLower(s)
	}

	return ""
}

func (p *Printer) keyword(s string) string {
	switch p.fmt.opts.KeywordStyle {
	case AsIs:
		return s
	case UpperCase:
		return strings.ToUpper(s)
	case LowerCase:
		return strings.ToLower(s)
	}

	return ""
}

func (p *Printer) typename(s string) string {
	if len(s) == 0 || s[0] == '`' {
		return s
	}

	switch p.fmt.opts.TypeStyle {
	case AsIs:
		return s
	case UpperCase:
		return strings.ToUpper(s)
	case LowerCase:
		return strings.ToLower(s)
	}

	return ""
}

// identifierWithCase prints according to specified case, and falls back
// to default identifier definition.  This is used to render specific
// function arguments.
func (p *Printer) identifierWithCase(s string, c PrintCase) string {
	if s[0] == '`' {
		return s
	}

	switch c {
	case AsIs:
		return s
	case UpperCase:
		return strings.ToUpper(s)
	case LowerCase:
		return strings.ToLower(s)
	case Unspecified:
		return p.identifier(s)
	}

	return ""
}

func (p *Printer) nodeInput(n ast.Node) string {
	r := n.ParseLocationRange()
	b := r.Start().ByteOffset()
	e := r.End().ByteOffset()

	return p.viewInput(b, e)
}

func (p *Printer) nodeErasedInput(n ast.Node) string {
	r := n.ParseLocationRange()
	b := r.Start().ByteOffset()
	e := r.End().ByteOffset()

	return p.viewErasedInput(b, e)
}

// viewErasedInput safely returns the input within the interval
// [begin, end). Note that input is not necessarily available, and this
// method may return empty without an error.
func (p *Printer) viewErasedInput(begin, end int) string {
	if end >= len(p.erasedInput) {
		log.Println("[ERROR] Out of bounds on erased input.")

		return ""
	}

	return p.erasedInput[begin:end]
}

// viewInput safely returns the input within the interval [begin, end).
// Note that input is not necessarily available, and this method may
// return empty without an error.
func (p *Printer) viewInput(begin, end int) string {
	if end >= len(p.input) {
		return ""
	}

	return p.input[begin:end]
}

func alignNested(s string) string {
	var buf bytes.Buffer

	w := tabwriter.NewWriter(&buf, 0, 0, 0, ' ', tabwriter.AlignRight)

	fmt.Fprint(w, s)
	w.Flush()

	return strings.Trim(buf.String(), "\n")
}

func leftAlignNested(s string) string {
	var buf bytes.Buffer

	w := tabwriter.NewWriter(&buf, 0, 0, 0, ' ', 0)

	fmt.Fprint(w, s)
	w.Flush()

	return strings.Trim(buf.String(), "\n")
}

// isInsideOfMergeStatement returns true when the current node is is inside
// of a MERGE statement directly.
func isInsideOfMergeStatement(n ast.Node) bool {
	p := n.Parent()
	if !nodeDefined(p) {
		return false
	}

	return p.Kind() == ast.MergeStatement
}

// isInsideOfOnClause returns true when the current node is is inside
// of an ON clause directly. The node can be inside of other AndExpr
// and OrExpr.
func isInsideOfOnClause(n ast.Node) bool {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Kind() == ast.OnClause {
			return true
		}

		if p.Kind() != ast.AndExpr && p.Kind() != ast.OrExpr {
			return false
		}
	}

	return false
}

// isInsideOfWhereClause returns true when the current node is is inside
// of a WHERE clause directly. The node can be inside of other AndExpr
// and OrExpr.
func isInsideOfWhereClause(n ast.Node) bool {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Kind() == ast.WhereClause {
			return true
		}

		if p.Kind() != ast.AndExpr && p.Kind() != ast.OrExpr {
			return false
		}
	}

	return false
}

// hasLowerPrecedence returns whether child has lower precedence than
// parent.  This is mainly used to help on determine the necessity
// for parenthesis when rendering expressions.
func hasLowerPrecedence(parent, child ast.Node) (eval bool, ok bool) {
	p := precedenceNum(parent)
	c := lowestPrecedenceBelow(child)

	eval = p > 0 && c > 0 && p > c
	ok = p < 1000 && c < 1000

	return
}

func lowestPrecedenceBelow(n ast.Node) int {
	switch t := n.(type) {
	case *ast.BinaryExpressionNode:
		var (
			min int = precedenceNum(n)
			lhs int = precedenceNum(t.Lhs())
			rhs int = precedenceNum(t.Rhs())
		)

		if lhs < min {
			min = lhs
		}

		if rhs < min {
			min = rhs
		}

		return min
	default:
		return 1000
	}
}

func precedenceNum(n ast.Node) int {
	switch n.Kind() {
	case ast.DotStar:
		return 1
	case ast.OrExpr:
		return 2
	case ast.AndExpr:
		return 3
	case ast.UnaryExpression:
		return precedenceUnaryExpr(n.(*ast.UnaryExpressionNode))
	case ast.BinaryExpression:
		return precedenceBinExpr(n.(*ast.BinaryExpressionNode))
	case ast.BetweenExpression:
		return 5
	}

	return 1000
}

func precedenceBinExpr(n *ast.BinaryExpressionNode) int {
	switch n.Op() {
	case ast.NotSetOp:
		return -1
	case ast.EqOp, ast.NeOp, ast.Ne2Op,
		ast.GtOp, ast.GeOp,
		ast.LtOp, ast.LeOp,
		ast.LikeOp,
		ast.DistinctOp,
		ast.IsOp:
		return 5
	case ast.BitwiseOrOp:
		return 6
	case ast.BitwiseXorOp:
		return 7
	case ast.BitwiseAndOp:
		return 8
	case ast.PlusOp, ast.MinusOp:
		return 9
	case ast.ConcatOP:
		return 10
	case ast.MultiplyOp, ast.DivideOp:
		return 11
	}

	return 1000
}

func precedenceUnaryExpr(n *ast.UnaryExpressionNode) int {
	switch n.Op() {
	case ast.NotSetUnaryOp:
		return 0
	case ast.NotUnaryOp:
		return 4
	case ast.BitwiseNotUnaryOp, ast.MinusUnaryOp,
		ast.PlusUnaryOp,
		ast.IsUnknownUnaryOp,
		ast.IsNotUnknownUnaryOp:
		return 12
	}

	return -1
}

var lineBreakPlaceholder = string([]byte{33, 26, '1', '0'})
