package bqlang

import (
	"fmt"

	"github.com/goccy/go-googlesql"
)

// Init initializes the underlying pure-Go GoogleSQL engine.
// Calling Init multiple times is safe and idempotent.
func Init() error {
	if err := googlesql.Init(); err != nil {
		return fmt.Errorf("bqlang.Init: %w", err)
	}

	return nil
}

// DefaultParserOptions returns ParserOptions configured with BigQuery and
// modern GoogleSQL language features.
func DefaultParserOptions() *googlesql.ParserOptions {
	if err := Init(); err != nil {
		panic(err)
	}

	po, err := googlesql.NewParserOptions()
	if err != nil {
		panic(fmt.Errorf("DefaultParserOptions: %w", err))
	}

	if err := po.SetLanguageOptions(DefaultLanguageOptions()); err != nil {
		panic(fmt.Errorf("DefaultParserOptions: set language options: %w", err))
	}

	return po
}

// DefaultLanguageOptions returns LanguageOptions enabled for BigQuery SQL,
// procedural scripting, DDL/DML, Pipe syntax, chained calls, inline WITH()
// expressions, GROUP BY ALL, and property graphs.
func DefaultLanguageOptions() *googlesql.LanguageOptions {
	if err := Init(); err != nil {
		panic(err)
	}

	lang, err := googlesql.NewLanguageOptions()
	if err != nil {
		panic(fmt.Errorf("DefaultLanguageOptions: %w", err))
	}

	if err := lang.SetSupportsAllStatementKinds(); err != nil {
		panic(fmt.Errorf("DefaultLanguageOptions: %w", err))
	}

	for _, feat := range defaultEnabledFeatures() {
		if err := lang.EnableLanguageFeature(feat); err != nil {
			panic(fmt.Errorf("DefaultLanguageOptions: enable feature %d: %w", feat, err))
		}
	}

	if err := lang.EnableReservableKeyword("QUALIFY", true); err != nil {
		panic(fmt.Errorf("DefaultLanguageOptions: enable QUALIFY: %w", err))
	}

	if err := lang.EnableReservableKeyword("GRAPH_TABLE", true); err != nil {
		panic(fmt.Errorf("DefaultLanguageOptions: enable GRAPH_TABLE: %w", err))
	}

	return lang
}

func defaultEnabledFeatures() []googlesql.LanguageFeature {
	return []googlesql.LanguageFeature{
		googlesql.LanguageFeatureFeatureAnalyticFunctions,
		googlesql.LanguageFeatureFeatureTablesample,
		googlesql.LanguageFeatureFeatureTableValuedFunctions,
		googlesql.LanguageFeatureFeatureCreateAggregateFunction,
		googlesql.LanguageFeatureFeatureCreateTableFunction,
		googlesql.LanguageFeatureFeatureGroupByRollup,
		googlesql.LanguageFeatureFeatureTemplateFunctions,
		googlesql.LanguageFeatureFeatureCreateTablePartitionBy,
		googlesql.LanguageFeatureFeatureCreateTableClusterBy,
		googlesql.LanguageFeatureFeatureNumericType,
		googlesql.LanguageFeatureFeatureBignumericType,
		googlesql.LanguageFeatureFeatureJsonType,
		googlesql.LanguageFeatureFeatureIntervalType,
		googlesql.LanguageFeatureFeatureGeography,
		googlesql.LanguageFeatureFeatureParameterizedTypes,
		googlesql.LanguageFeatureFeatureNamedArguments,
		googlesql.LanguageFeatureFeatureCreateTableNotNull,
		googlesql.LanguageFeatureFeatureCreateTableLike,
		googlesql.LanguageFeatureFeatureCreateTableClone,
		googlesql.LanguageFeatureFeatureCreateTableCopy,
		googlesql.LanguageFeatureFeatureCreateSnapshotTable,
		googlesql.LanguageFeatureFeatureCreateExternalTableWithTableElementList,
		googlesql.LanguageFeatureFeatureCreateExternalTableWithPartitionColumns,
		googlesql.LanguageFeatureFeatureCreateExternalTableWithConnection,
		googlesql.LanguageFeatureFeatureCreateMaterializedViewPartitionBy,
		googlesql.LanguageFeatureFeatureCreateMaterializedViewClusterBy,
		googlesql.LanguageFeatureFeatureCreateViewWithColumnList,
		googlesql.LanguageFeatureFeatureCreateViewsWithColumnOptions,
		googlesql.LanguageFeatureFeatureUnenforcedPrimaryKeys,
		googlesql.LanguageFeatureFeatureForeignKeys,
		googlesql.LanguageFeatureFeatureCheckConstraint,
		googlesql.LanguageFeatureFeatureAlterColumnSetDataType,
		googlesql.LanguageFeatureFeatureAlterTableRenameColumn,
		googlesql.LanguageFeatureFeatureAllowLegacyRowAccessPolicySyntax,
		googlesql.LanguageFeatureFeatureV11SelectStarExceptReplace,
		googlesql.LanguageFeatureFeatureV11OrderByInAggregate,
		googlesql.LanguageFeatureFeatureV11LimitInAggregate,
		googlesql.LanguageFeatureFeatureV11HavingInAggregate,
		googlesql.LanguageFeatureFeatureV11NullHandlingModifierInAnalytic,
		googlesql.LanguageFeatureFeatureV11NullHandlingModifierInAggregate,
		googlesql.LanguageFeatureFeatureV11ForSystemTimeAsOf,
		googlesql.LanguageFeatureFeatureV12CivilTime,
		googlesql.LanguageFeatureFeatureV12SafeFunctionCall,
		googlesql.LanguageFeatureFeatureV12WeekWithWeekday,
		googlesql.LanguageFeatureFeatureV12GeneratedColumns,
		googlesql.LanguageFeatureFeatureV13AllowConsecutiveOn,
		googlesql.LanguageFeatureFeatureV13AllowDashesInTableName,
		googlesql.LanguageFeatureFeatureV13IsDistinct,
		googlesql.LanguageFeatureFeatureV13Qualify,
		googlesql.LanguageFeatureFeatureQualify,
		googlesql.LanguageFeatureFeatureV13RemoteFunction,
		googlesql.LanguageFeatureFeatureV13Pivot,
		googlesql.LanguageFeatureFeatureV13Unpivot,
		googlesql.LanguageFeatureFeatureV13WithRecursive,
		googlesql.LanguageFeatureFeatureV13FormatInCast,
		googlesql.LanguageFeatureFeatureV13CollationSupport,
		googlesql.LanguageFeatureFeatureV13ForIn,
		googlesql.LanguageFeatureFeatureV13Repeat,
		googlesql.LanguageFeatureFeatureV13CaseStmt,
		googlesql.LanguageFeatureFeatureV13ScriptLabel,
		googlesql.LanguageFeatureFeatureV13ColumnDefaultValue,
		googlesql.LanguageFeatureFeatureV13OmitInsertColumnList,
		googlesql.LanguageFeatureFeaturePipes,
		googlesql.LanguageFeatureFeaturePipeIf,
		googlesql.LanguageFeatureFeaturePipeFork,
		googlesql.LanguageFeatureFeaturePipeTee,
		googlesql.LanguageFeatureFeaturePipeWith,
		googlesql.LanguageFeatureFeaturePipeExportData,
		googlesql.LanguageFeatureFeaturePipeCreateTable,
		googlesql.LanguageFeatureFeaturePipeInsert,
		googlesql.LanguageFeatureFeaturePipeAssert,
		googlesql.LanguageFeatureFeaturePipeLog,
		googlesql.LanguageFeatureFeaturePipeDescribe,
		googlesql.LanguageFeatureFeaturePipeStaticDescribe,
		googlesql.LanguageFeatureFeatureStatementWithPipeOperators,
		googlesql.LanguageFeatureFeatureV14PipeRecursiveUnion,
		googlesql.LanguageFeatureFeatureV14PipeNamedWindows,
		googlesql.LanguageFeatureFeatureV14WithExpression,
		googlesql.LanguageFeatureFeatureV14ChainedFunctionCalls,
		googlesql.LanguageFeatureFeatureV13LikeAnySomeAll,
		googlesql.LanguageFeatureFeatureV14LikeAnySomeAllArray,
		googlesql.LanguageFeatureFeatureV14LikeAnySomeAllSubquery,
		googlesql.LanguageFeatureFeatureGeneralQuantifiedComparisons,
		googlesql.LanguageFeatureFeatureV14GroupByAll,
		googlesql.LanguageFeatureFeatureV14GroupingBuiltin,
		googlesql.LanguageFeatureFeatureV14GroupingSets,
		googlesql.LanguageFeatureFeatureMultiGroupingSets,
		googlesql.LanguageFeatureFeatureV14MultilevelAggregation,
		googlesql.LanguageFeatureFeatureMultilevelAggregationOnUdas,
		googlesql.LanguageFeatureFeatureV14AggregateFiltering,
		googlesql.LanguageFeatureFeatureDifferentialPrivacy,
		googlesql.LanguageFeatureFeatureDifferentialPrivacyReportFunctions,
		googlesql.LanguageFeatureFeatureDifferentialPrivacyNested,
		googlesql.LanguageFeatureFeatureV14LoadDataPartitions,
		googlesql.LanguageFeatureFeatureV14LoadDataTempTable,
		googlesql.LanguageFeatureFeatureV14RemoteModel,
		googlesql.LanguageFeatureFeatureV14SqlGraph,
		googlesql.LanguageFeatureFeatureV14SqlGraphAdvancedQuery,
		googlesql.LanguageFeatureFeatureV14SqlGraphBoundedPathQuantification,
		googlesql.LanguageFeatureFeatureV14SqlGraphPathMode,
		googlesql.LanguageFeatureFeatureV14SqlGraphPathSearchPrefixPathCount,
	}
}

// DefaultErrorMessageOptions returns standard multi-line caret error options
// with a valid stability mode for go-googlesql.
func DefaultErrorMessageOptions() *googlesql.ErrorMessageOptions {
	return &googlesql.ErrorMessageOptions{
		Mode:                     googlesql.ErrorMessageModeErrorMessageMultiLineWithCaret,
		Stability:                googlesql.ErrorMessageStabilityProduction,
		InputOriginalStartLine:   1,
		InputOriginalStartColumn: 1,
	}
}
