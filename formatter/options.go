package formatter

import "github.com/goccy/go-googlesql"

func knownOptionKeys(n *googlesql.ASTOptionsList) stringMapSet {
	parent := nodeParent(n)
	if !nodeDefined(parent) {
		return nil
	}

	switch nodeKind(parent) {
	case googlesql.ASTNodeKindAstCreateExternalTableStatement:
		return newStringMapSet(
			"allow_jagged_rows",
			"allow_quoted_newlines",
			"bigtable_options",
			"compression",
			"decimal_target_types",
			"description",
			"enable_list_inference",
			"enable_logical_types",
			"encoding",
			"enum_as_string",
			"expiration_timestamp",
			"field_delimiter",
			"format",
			"hive_partition_uri_prefix",
			"file_set_spec_type",
			"ignore_unknown_values",
			"json_extension",
			"max_bad_records",
			"max_staleness",
			"metadata_cache_mode",
			"null_marker",
			"object_metadata",
			"preserve_ascii_control_characters",
			"projection_fields",
			"quote",
			"reference_file_schema_uri",
			"require_hive_partition_filter",
			"sheet_range",
			"skip_leading_rows",
			"uris",
		)
	case googlesql.ASTNodeKindAstCreateFunctionStatement:
		return newStringMapSet(
			"description",
			"library",
			"endpoint",
			"user_defined_context",
			"max_batching_rows",
		)
	case googlesql.ASTNodeKindAstCreateMaterializedViewStatement:
		return newStringMapSet(
			"enable_refresh",
			"refresh_interval_minutes",
			"expiration_timestamp",
			"max_staleness",
			"allow_non_incremental_definition",
			"kms_key_name",
			"friendly_name",
			"description",
			"labels",
		)
	case googlesql.ASTNodeKindAstCreateProcedureStatement:
		return newStringMapSet(
			"strict_mode",
			"description",
			"engine",
			"runtime_version",
			"container_image",
			"properties",
			"main_file_uri",
			"main_class",
			"py_file_uris",
			"jar_uris",
			"file_uris",
			"archive_uris",
		)
	case googlesql.ASTNodeKindAstCreateTableFunctionStatement:
		return newStringMapSet("description")
	case googlesql.ASTNodeKindAstCreateTableStatement:
		return newStringMapSet(
			"expiration_timestamp",
			"partition_expiration_days",
			"require_partition_filter",
			"kms_key_name",
			"friendly_name",
			"description",
			"labels",
			"default_rounding_mode",
		)
	case googlesql.ASTNodeKindAstCreateSchemaStatement:
		return newStringMapSet(
			"default_kms_key_name",
			"default_partition_expiration_days",
			"default_rounding_mode",
			"default_table_expiration_days",
			"description",
			"friendly_name",
			"is_case_insensitive",
			"labels",
			"location",
			"max_time_travel_hours",
			"storage_billing_model",
		)
	case googlesql.ASTNodeKindAstCreateSnapshotTableStatement:
		return newStringMapSet(
			"expiration_timestamp",
			"friendly_name",
			"description",
			"labels",
		)
	case googlesql.ASTNodeKindAstCreateViewStatement:
		return newStringMapSet(
			"expiration_timestamp",
			"friendly_name",
			"description",
			"labels",
			"privacy_policy",
		)
	case googlesql.ASTNodeKindAstSimpleColumnSchema,
		googlesql.ASTNodeKindAstArrayColumnSchema,
		googlesql.ASTNodeKindAstStructColumnSchema,
		googlesql.ASTNodeKindAstColumnWithOptions:
		return newStringMapSet(
			"description",
			"rounding_mode",
			"data_policies",
		)
	default:
		return nil
	}
}
