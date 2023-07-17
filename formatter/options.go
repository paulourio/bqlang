// This file contains information for ASTOptionsLists.
package formatter

import "github.com/goccy/go-zetasql/ast"

func knownOptionKeys(n *ast.OptionsListNode) StringMapSet {
	parent := n.Parent()
	if parent == nil {
		return nil
	}

	// TODO: set options for all in
	// https://cloud.google.com/bigquery/docs/reference/standard-sql/data-definition-language#table_option_list

	switch parent.Kind() {
	case ast.CreateExternalTableStatement:
		return createExternalTableOptions
	case ast.CreateFunctionStatement:
		return createFunctionOptions
	case ast.CreateMaterializedViewStatement:
		return createMaterializedViewOptions
	case ast.CreateProcedureStatement:
		return createProcedureOptions
	case ast.CreateTableFunctionStatement:
		return createTableFunctionOptions
	case ast.CreateTableStatement:
		return createTableOptions
	case ast.CreateSchemaStatement:
		return createSchemaOptions
	case ast.CreateSnapshotTableStatement:
		return createSnapshotTableOptions
	case ast.CreateViewStatement:
		return createViewOptions
	case ast.SimpleColumnSchema:
		return simpleColumnOptions
	}

	return nil
}

var createExternalTableOptions = NewStringMapSet(
	"allow_jagged_rows",                 // BOOL
	"allow_quoted_newlines",             // BOOL
	"bigtable_options",                  // STRING
	"compression",                       // STRING
	"decimal_target_types",              // ARRAY<STRING>
	"description",                       // STRING
	"enable_list_inference",             // BOOL
	"enable_logical_types",              // BOOL
	"encoding",                          // STRING
	"enum_as_string",                    // BOOL
	"expiration_timestamp",              // TIMESTAMP
	"field_delimiter",                   // STRING
	"format",                            // STRING
	"hive_partition_uri_prefix",         // STRING
	"file_set_spec_type",                // STRING
	"ignore_unknown_values",             // BOOL
	"json_extension",                    // STRING
	"max_bad_records",                   // INT64
	"max_staleness",                     // INTERVAL
	"metadata_cache_mode",               // STRING
	"null_marker",                       // STRING
	"object_metadata",                   // STRING
	"preserve_ascii_control_characters", // BOOL
	"projection_fields",                 // STRING
	"quote",                             // STRING
	"reference_file_schema_uri",         // STRING
	"require_hive_partition_filter",     // BOOL
	"sheet_range",                       // STRING
	"skip_leading_rows",                 //INT64
	"uris",                              // ARRAY<STRING>
)

var createFunctionOptions = NewStringMapSet(
	"description",          // STRING
	"library",              // ARRAY<STRING>
	"endpoint",             // STRING
	"user_defined_context", // ARRAY<STRUCT<STRING, STRING>>
	"max_batching_rows",    // INT64
)

var createMaterializedViewOptions = NewStringMapSet(
	"enable_refresh",                   // BOOL
	"refresh_interval_minutes",         // FLOAT64
	"expiration_timestamp",             // TIMESTAMP
	"max_staleness",                    // INTERVAL
	"allow_non_incremental_definition", // BOOLEAN
	"kms_key_name",                     // STRING
	"friendly_name",                    // STRING
	"description",                      // STRING
	"labels",                           // ARRAY<STRUCT<STRING, STRING>>
)

var createProcedureOptions = NewStringMapSet(
	"strict_mode",     // bOOL
	"description",     // STRING
	"engine",          // STRING
	"runtime_version", // STRING
	"container_image", // STRING
	"properties",      // ARRAY<STRUCT<STRING, STRING>>
	"main_file_uri",   // STRING
	"main_class",      // STRING
	"py_file_uris",    // ARRAY<STRING>
	"jar_uris",        // ARRAY<STRING>
	"file_uris",       // ARRAY<STRING>
	"archive_uris",    // ARRAY<STRING>
)

var createReservationOptions = NewStringMapSet(
	"strict_mode",     // bOOL
	"description",     // STRING
	"engine",          // STRING
	"runtime_version", // STRING
	"container_image", // STRING
	"properties",      // ARRAY<STRUCT<STRING, STRING>>
	"main_file_uri",   // STRING
	"main_class",      // STRING
	"py_file_uris",    // ARRAY<STRING>
	"jar_uris",        // ARRAY<STRING>
	"file_uris",       // ARRAY<STRING>
	"archive_uris",    // ARRAY<STRING>
)

var createTableFunctionOptions = NewStringMapSet("description")

var createTableOptions = NewStringMapSet(
	"expiration_timestamp",
	"partition_expiration_days",
	"require_partition_filter",
	"kms_key_name",
	"friendly_name",
	"description",
	"labels",
	"default_rounding_mode",
)

var createSchemaOptions = NewStringMapSet(
	"default_kms_key_name",              // STRING
	"default_partition_expiration_days", // FLOAT64
	"default_rounding_mode",             // STRING
	"default_table_expiration_days",     // ROUND_HALF_AWAY_FROM_ZERO or ROUND_HALF_EVEN
	"default_table_expiration_days",     // FLOAT64
	"description",                       // STRING
	"friendly_name",                     // STRING
	"is_case_insensitive",               // BOOL
	"labels",                            // ARRAY<STRUCT<STRING, STRING>>
	"location",                          // STRING
	"max_time_travel_hours",             // SMALLINT
	"storage_billing_model",             // STRING
)

var createSnapshotTableOptions = NewStringMapSet(
	"expiration_timestamp", // TIMESTAMP
	"friendly_name",        // STRING
	"description",          // description
	"labels",               // ARRAY<STRUCT<STRING, STRING>>
)

var createViewOptions = NewStringMapSet("description")

var simpleColumnOptions = NewStringMapSet(
	"description",
	"rounding_mode",
)
