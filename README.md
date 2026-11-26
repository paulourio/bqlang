# bqlang: Pure-Go GoogleSQL and BigQuery Tooling

The `bqlang` module provides a deterministic SQL formatter (`bqfmt`), a Language Server and embedded editor installer (`bqpls`), an AST JSON exporter (`bqdump`), and Go library packages for GoogleSQL and BigQuery scripts. The parser runs on [`github.com/goccy/go-googlesql`](https://github.com/goccy/go-googlesql), which compiles the upstream C++ GoogleSQL parser to pure Go ahead of time via `wasm2go`. Building and running `bqlang` requires only a standard Go toolchain without `cgo`, Bazel, or C++ system libraries.

Because GoogleSQL discards comments during AST construction and rejects unrendered template tags, `bqlang` pairs the GoogleSQL parser with a dedicated Gocc scanner and parser in `extensions/`. Before parsing SQL, the formatter extracts line comments, block comments, and Jinja2 template constructs in a single pass, substitutes recoverable SQL placeholders for template blocks, aligns tokens through nested `text/tabwriter` cells, and re-injects comments and original template expressions at their tracked source coordinates.

## Installation

Install the command-line utilities with Go:

```bash
go install github.com/paulourio/bqlang/cmd/bqfmt@latest
go install github.com/paulourio/bqlang/cmd/bqpls@latest
go install github.com/paulourio/bqlang/cmd/bqdump@latest
```

To add the library packages to an existing Go module:

```bash
go get github.com/paulourio/bqlang@latest
```

## Command-Line Tools

### `bqfmt`

The `bqfmt` CLI formats GoogleSQL and BigQuery scripts. Every formatting run verifies two invariants before emitting output: re-parsing must succeed without syntax errors, and a second formatting pass must produce byte-identical SQL.

```bash
# Format standard input to standard output
bqfmt < query.sql

# List files whose formatting differs from bqfmt output
bqfmt -l queries/

# Format files in place across a directory tree
bqfmt -w queries/

# Format with an explicit configuration file
bqfmt -config .bqfmt.toml -w queries/
```

When given a directory path, `bqfmt` walks the directory tree (skipping hidden subdirectories and `node_modules`) and processes files ending in `.sql`, `.bql`, `.sql.j2`, `.bql.j2`, `.sql.tpl`, and `.bql.tpl`.

| Flag | Description |
| :--- | :--- |
| `-l` | Print paths of files whose formatting differs from `bqfmt` output. |
| `-w` | Write formatted output back to source files instead of `stdout`. |
| `-d` | Print unified diffs for files whose formatting differs from `bqfmt` output. |
| `-check` | Exit with code `1` if any file's formatting differs from `bqfmt` output (`0` when clean, `2` on error). |
| `-config <path>` | Read print options from the specified TOML file. |
| `-stdin-filename <path>` | Filename used for `.bqfmt.toml` discovery, style matching, and diagnostics when reading `stdin`. |
| `-debug` | Write formatter trace diagnostics to `stderr`. |
| `-version` | Print the `bqfmt` version and exit. |

#### Configuration (`.bqfmt.toml`)

When `-config` is omitted, `bqfmt` searches upward from each target file's directory (or the current working directory when reading from standard input) to the filesystem root for `.bqfmt.toml`.

Configuration files support named style profiles with optional `regex` or `include` filename filters, as well as flat top-level option tables:

```toml
default_style = "default"

[[styles]]
name = "default"

[styles.options]
soft_max_cols = 120
newline_before_clause = true
align_logical_with_clauses = true
align_trailing_comments = true
column_list_trailing_comma = "AUTO"
indentation = 2
indent_case_when = true
indent_with_clause = true
indent_with_entries = false
min_joins_to_separate_in_blocks = 2
max_cols_for_single_line_select = 4
max_params_for_single_line_function = 2
function_catalog = "BIGQUERY"
builtin_function_name_style = "UPPER_CASE"
function_name_style = "AS_IS"
identifier_style = "AS_IS"
keyword_style = "UPPER_CASE"
type_style = "UPPER_CASE"
bool_style = "UPPER_CASE"
null_style = "UPPER_CASE"
pseudo_column_style = "UPPER_CASE"
table_name_style = "AS_IS"
query_parameter_style = "AS_IS"
system_variable_style = "AS_IS"
hex_style = "UPPER_CASE"
numeric_style = "LOWER_CASE"
bytes_style = "PREFER_SINGLE_QUOTE"
string_style = "PREFER_SINGLE_QUOTE"
```

### `bqpls`

The `bqpls` binary serves LSP 3.17 diagnostics, semantic tokens (`partitioned`, `clustered`, and `pseudo` column modifiers), Markdown hover cards, estimated query cost CodeLenses and status notifications, pipe-aware (`|>`) and Jinja2/dbt autocomplete, go-to-definition, document symbols, and formatting over stdio JSON-RPC 2.0. It also embeds zero-dependency editor bundles for VS Code, Kiro, and Neovim without requiring Node.js, `npm`, `vsce`, or Tree-sitter compilation.

```bash
# Analyze and lint SQL files or directories for syntax, semantic, partition, and clustering issues
bqpls check queries/
bqpls check -format json -fail-on error queries/report.sql

# Install editor bundles (VS Code, Kiro, Neovim, or all three)
bqpls install vscode
bqpls install kiro
bqpls install nvim
bqpls install all

# Export an editor bundle without installing it
bqpls export -target vsix -o bqlang.vsix
bqpls export -target vscode-dir -o ./dist/vscode
bqpls export -target nvim -o ./dist/bqpls.nvim

# Start the stdio language server manually with explicit config and debug logging
bqpls serve -config .bqfmt.toml -log-file /tmp/bqpls.log -debug

# Print the bqpls version
bqpls version
```

| Subcommand | Flags / Arguments | Description |
| :--- | :--- | :--- |
| `serve` (default) | `-config <path>`, `-log-file <path>`, `-debug` | Run the stdio LSP 3.17 server. |
| `check` (`lint`) | `-config`, `-format <text\|json>`, `-fail-on`, `-project`, `-dataset`, `-schema-dir`, `-bigquery-api`, `[path ...]` | Analyze and lint SQL files, directories, or `stdin` (`0` clean, `1` findings, `2` error). |
| `install` | `<vscode\|kiro\|nvim\|all>`, `-bin-path <path>` | Install embedded editor extensions and stamp the `bqpls` binary path. |
| `export` | `-target <vsix\|vscode-dir\|nvim>`, `-o <path>`, `-bin-path <path>` | Write a deterministic `.vsix` archive or directory bundle. |
| `version` | None | Print the `bqpls` build version and exit. |

Running `bqpls install nvim` writes the Neovim runtime package to `$XDG_DATA_HOME/nvim/site/pack/bqlang/start/bqpls.nvim` (falling back to `~/.local/share/nvim/site/pack/bqlang/start/bqpls.nvim` when `XDG_DATA_HOME` is unset). Running `bqpls install vscode` or `bqpls install kiro` builds an in-memory `.vsix` and runs the corresponding `--install-extension` CLI, or extracts the extension directly into `~/.vscode/extensions/` or `~/.kiro/extensions/` when the CLI binary is absent from `PATH`. All editor bundles associate `.bql`, `.sql`, `.bql.j2`, `.sql.j2`, `.bql.tpl`, and `.sql.tpl` with language `bqlang`.

#### LSP Configuration (`[lsp]` in `.bqfmt.toml`)

Both `bqfmt` and `bqpls` read the same `.bqfmt.toml` configuration file. Language server settings live under an optional `[lsp]` table:

```toml
[lsp]
default_project = "my-gcp-project"
default_dataset = "analytics"
schema_dirs = ["./schemas"]
bigquery_api_enabled = false
cache_dir = ".bqpls-cache"
cache_ttl_seconds = 86400
diagnostics_enabled = true
require_partition_filter_severity = "warning"
missing_partition_filter_severity = "warning"
clustering_filter_severity = "warning"
cost_estimation_enabled = true
cost_codelens_enabled = true
cost_status_bar_enabled = true
cost_hover_enabled = true
cost_diagnostic_severity = "warning"
cost_warning_threshold_bytes = 0
cost_warning_threshold_usd = 0.0
cost_price_per_tib_usd = 6.25
cost_dry_run_debounce_ms = 800

[lsp.project_cache_ttl_seconds]
"bigquery-public-data" = 2592000
```

| Key | Default | Description |
| :--- | :--- | :--- |
| `default_project` | `""` | Default GCP project ID prepended to 1-part and 2-part table names. |
| `default_dataset` | `""` | Default BigQuery dataset ID prepended to unqualified table names. |
| `schema_dirs` | `[]` | Local directories of offline table schema JSON files (relative paths resolve from `.bqfmt.toml`). |
| `bigquery_api_enabled` | `false` | Enable non-blocking background metadata fetches and dry-run cost estimates from the BigQuery REST v2 API. |
| `gcloud_path` | `""` | Explicit path to the `gcloud` executable (supports leading `~/`); auto-detects from `PATH` and standard Google Cloud SDK directories when empty. |
| `cache_dir` | `<userCacheDir>/bqpls` | Persistent disk cache directory for fetched table metadata. |
| `cache_ttl_seconds` | `86400` | Default disk metadata cache TTL in seconds (`24h`; `0` disables default disk cache reads). |
| `project_cache_ttl_seconds` | `{}` | Map of GCP project IDs to extended cache TTLs in seconds; merges additively across VS Code scopes (`bqpls.projectCacheTtlSeconds`) and `.bqfmt.toml`, extending `cache_ttl_seconds`. |
| `diagnostics_enabled` | `true` | Enable parser, semantic, partition/clustering lint, and cost threshold diagnostics. |
| `require_partition_filter_severity` | `"warning"` | Diagnostic severity when querying a table with `requirePartitionFilter` without a sargable partition filter (`"error"`, `"warning"`, `"info"`, `"hint"`, or `"off"`). |
| `missing_partition_filter_severity` | `"warning"` | Diagnostic severity when querying any partitioned table without a sargable filter on its partition column in `WHERE` or `JOIN`. |
| `clustering_filter_severity` | `"warning"` | Diagnostic severity when querying a clustered table without filtering or joining on its leading clustering column. |
| `cost_estimation_enabled` | `true` | Master toggle for query cost estimation (BigQuery dry-run plus offline fallback). |
| `cost_codelens_enabled` | `true` | Display estimated bytes and USD cost as a CodeLens above each SQL statement. |
| `cost_status_bar_enabled` | `true` | Publish `bqpls/queryCost` updates for the VS Code/Kiro status bar and Neovim `require("bqpls").statusline()`. |
| `cost_hover_enabled` | `true` | Display humanized table size and full-table scan cost in table hover cards. |
| `cost_diagnostic_severity` | `"warning"` | Diagnostic severity when estimated scan exceeds a configured cost threshold (`"error"`, `"warning"`, `"info"`, `"hint"`, or `"off"`). |
| `cost_warning_threshold_bytes` | `0` | Emit a diagnostic when a statement's estimated bytes processed exceeds this limit (`0` disables byte threshold). |
| `cost_warning_threshold_usd` | `0` | Emit a diagnostic when a statement's estimated USD cost exceeds this limit (`0` disables USD threshold). |
| `cost_price_per_tib_usd` | `6.25` | BigQuery on-demand query price in USD per TiB (`2^40` bytes). |
| `cost_dry_run_debounce_ms` | `800` | Debounce delay in milliseconds after document edits before triggering an async dry-run (`0` = immediate). |

#### Estimated Query Cost

When `cost_estimation_enabled = true`, `bqpls` estimates query bytes processed and on-demand USD cost without blocking LSP responsiveness:

- **BigQuery Dry-Run (`bigquery_api_enabled = true`)**: Issues asynchronous zero-cost `POST /bigquery/v2/projects/{projectId}/jobs` (`configuration.dryRun = true`) requests on open, save, and debounced edits (cached in memory by SQL hash for 5 minutes). Dry-run resolves the billing project from `default_project`, `GOOGLE_CLOUD_PROJECT` / `GCLOUD_PROJECT` / `CLOUDSDK_CORE_PROJECT`, the active `gcloud` CLI configuration, or the ADC credentials file, and requires the `https://www.googleapis.com/auth/bigquery` OAuth scope and `bigquery.jobs.create` IAM permission.
- **Offline Fallback**: When the API is disabled, offline, or a dry-run is pending, `bqpls` computes an upper-bound estimate from deduplicated referenced tables' cached `numBytes` metadata and labels it `offline`.
- **Editor Surfaces**: Toggleable via settings across statement CodeLenses (`textDocument/codeLens`), the VS Code/Kiro status bar and Neovim `require("bqpls").statusline()` / `:BqplsRefreshCost` (`bqpls/queryCost`, `bqpls/refreshCost`), table hover cards, and threshold diagnostics (`estimated_cost`). Invalid SQL and BigQuery dry-run errors are surfaced directly in the status bar with error details in the tooltip.

#### BigQuery Metadata Caching and Offline Schemas

Offline schema files placed under `schema_dirs` support both flat `<project>.<dataset>.<table>.json` filenames and nested `<project>/<dataset>/<table>.json` directory trees. Each JSON file accepts BigQuery REST v2 table metadata, `bq show --format=json` output, or a raw schema column array (`bq show --schema --format=json`).

When `bigquery_api_enabled = true`, LSP requests never block on network I/O. Cache misses enqueue a deduplicated background fetch against `bigquery.googleapis.com/bigquery/v2` with a 5-second timeout, authenticating through GCP Application Default Credentials (`https://www.googleapis.com/auth/bigquery.readonly`) or falling back to `gcloud auth application-default print-access-token`. Retrieved dataset lists, table lists, and table schemas persist across sessions in a CGo-free SQLite WAL database at `<cache_dir>/cache.db` (safe for concurrent use across multiple IDEs) for `cache_ttl_seconds` (`24h` by default) or the extended project-specific TTL in `project_cache_ttl_seconds` / `bqpls.projectCacheTtlSeconds`. Temporary `_`-prefixed datasets are restricted to the active in-memory session and excluded from cross-session disk persistence. Cached listings serve positive matches immediately while uncached table references and unmatched completion prefixes re-query BigQuery externally, expired entries and `_`-prefixed datasets are pruned automatically on session access and periodic writes with incremental SQLite page reclamation, and completed background fetches trigger `workspace/semanticTokens/refresh` and updated diagnostics automatically.

#### View Partition Discovery Heuristics

Because BigQuery's `tables.get` REST v2 endpoint omits `timePartitioning`, `rangePartitioning`, and `requirePartitionFilter` on `VIEW` resources, `bqpls` pairs passive dry-run error inspection with an automatic three-stage background probe pipeline whenever `bigquery_api_enabled = true` and a billing project is configured:

1. **Stage 1 (`SELECT *` Baseline Dry-Run)**: Executes a zero-cost unfiltered dry-run against the view. If BigQuery rejects the query with a required-partition-filter error, `bqpls` flags that column with `require_partition_filter = true`; on HTTP `200`, it records baseline bytes and `referencedTables`.
2. **Stage 2 (Single Underlying Table Lookup)**: When `referencedTables` names one accessible physical table, `bqpls` inspects that base table via `tables.get`, stops early if it is unpartitioned, or copies its partitioning metadata (with `require_partition_filter = false`) when the partition column appears in the view schema.
3. **Stage 3 (`>= 100 GiB` Group-Testing Binary Search)**: When Stage 2 falls back (for example, `403`/`404` on authorized views, multi-table joins, or aliased partition columns) and baseline scan size reaches `100 GiB`, `bqpls` probes top-level `DATE`, `DATETIME`, and `TIMESTAMP` columns sequentially using Capacitor-safe `SELECT *` queries against a future date predicate (`<currentUTCYear + 2>-06-15`), canceling immediately on first-try non-reduction or concurrent data growth.

Discovered partition metadata and negative probe completion (`partitionProbed`) persist once per TTL in `<cache_dir>/cache.db` and update `require_partition_filter` / `missing_partition_filter` diagnostics, `@lsp.mod.partitioned` semantic tokens, `[PARTITION]` hover badges, and completion items automatically. See [docs/view-partition-discovery.md](docs/view-partition-discovery.md) for the complete protocol, Mermaid decision diagram, predicate invariants, and probe complexity bounds.

### `bqdump`

The `bqdump` CLI parses GoogleSQL input and serializes the concrete syntax tree as indented JSON, including node kinds, byte offsets, debug strings, and statement classification flags.

```bash
# Dump the full ASTScript tree from standard input
bqdump -mode script < query.sql

# Dump each top-level statement as a separate JSON object
bqdump -mode statement -file script.sql
```

## Go Library Usage

Format a SQL script directly in Go with `formatter.Format` or configure a reusable `formatter.SQLFormatter`:

```go
package main

import (
	"fmt"
	"log"

	"github.com/paulourio/bqlang/formatter"
)

func main() {
	opts := formatter.DefaultPrintOptions()
	opts.SoftMaxColumns = 100

	sql := "select user_id, count(*) as total from events group by all |> order by total desc"
	formatted, err := formatter.Format(sql, opts)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Print(formatted)
}
```

## Supported SQL and Template Surface

### Query Syntax, Pipe Operators, and Graph Queries

- **Relational Queries**: `SELECT` projections, `WITH` / `WITH RECURSIVE` CTEs, set operations (`UNION`, `INTERSECT`, `EXCEPT` with `CORRESPONDING` / `BY NAME`), `GROUP BY` (`ROLLUP`, `CUBE`, `GROUPING SETS`, `()`, `GROUP BY ALL`, `GROUP AND ORDER BY`), `HAVING`, `QUALIFY`, `WINDOW`, `PIVOT`, `UNPIVOT`, `MATCH_RECOGNIZE` row pattern matching, `FOR SYSTEM_TIME AS OF`, and `TABLESAMPLE`.
- **Pipe Syntax (`|>`)**: Linear pipelines starting from `SELECT` or `FROM`, including `WHERE`, `EXTEND`, `SET`, `RENAME`, `DROP`, `AS`, `DISTINCT`, `AGGREGATE`, `JOIN`, `CALL`, `WINDOW`, `ORDER BY`, `LIMIT`, `TABLESAMPLE`, `PIVOT`, `UNPIVOT`, `MATCH_RECOGNIZE`, `WITH`, `UNION` / `INTERSECT` / `EXCEPT`, `ASSERT`, `CREATE TABLE`, `INSERT`, and `EXPORT DATA`.
- **Property Graphs and GQL**: `GRAPH_TABLE(...)` and standalone or `NEXT`-chained `GRAPH` queries (`MATCH`, `OPTIONAL MATCH`, `LET`, `FILTER`, `FOR`, `CALL`, `WITH`, `RETURN`, set operations, GPML path/element patterns, search prefixes, path modes, label expressions, `IS [NOT] SOURCE/DESTINATION OF`, `IS [NOT] LABELED`, and braced `{ ... }` GQL subqueries).
- **Expressions and Functions**: Dot-chained calls (`(col).TRIM().LOWER()`, `(-1).(SAFE.SQRT)()`), inline `WITH(var AS expr, result)` expressions, quantified comparisons and `LIKE` (`= ANY`, `LIKE ALL`), scalar, multi-level aggregate (`MAX(x WHERE p GROUP BY y)` with `HAVING` and `WITH REPORT`), analytic/window, lambda (`x -> expr`), and templated (`ANY TYPE`, `ANY TABLE`) function calls.
- **Data Types**: All BigQuery scalar, parameterized (`STRING(MAX)`, `NUMERIC(p, s)`), `ARRAY`, `STRUCT`, `JSON`, `GEOGRAPHY`, `INTERVAL`, and `RANGE` types and literals.

### DDL, DML, DCL, and Data Movement

- **Schema and Table DDL**: `CREATE`, `ALTER`, and `DROP` statements for schemas (`EXTERNAL SCHEMA`, `ADD/DROP REPLICA`), tables (`LIKE`, `CLONE`, `COPY`, `SNAPSHOT`, `EXTERNAL`, `UNDROP`), views, materialized views (`AS REPLICA OF`), functions, table functions, procedures, property graphs (`CREATE/DROP PROPERTY GRAPH`), connections, row access policies, and BigQuery capacity/reservation entities.
- **Columns, Constraints, Indexes, and ML**: Column `DEFAULT`, `GENERATED ALWAYS` / `BY DEFAULT`, `IDENTITY`, `HIDDEN`, `OPTIONS`, and `COLLATE` attributes; `PRIMARY KEY`, `FOREIGN KEY` / inline `REFERENCES`, and `CHECK` constraints (`ENFORCED` / `NOT ENFORCED`); `SEARCH INDEX` and `VECTOR INDEX` (`CREATE`, `ALTER ... REBUILD`, `DROP`); and `CREATE MODEL` (`TRANSFORM`, `INPUT` / `OUTPUT`).
- **DML, DCL, and Data Movement**: `INSERT` (including `DEFAULT` values), `DELETE`, `UPDATE`, `MERGE`, `TRUNCATE TABLE`, `THEN RETURN` (`WITH ACTION`), `GRANT` / `REVOKE`, `EXPORT DATA`, `EXPORT MODEL`, and `LOAD DATA INTO` / `OVERWRITE` (`PARTITIONS`, `FROM FILES`).

### Procedural Scripting

- **Declarations and Scopes**: `DECLARE`, `SET`, and nested `BEGIN ... EXCEPTION WHEN ERROR THEN ... END` blocks.
- **Control Flow and Transactions**: `IF ... ELSEIF ... ELSE ... END IF`, `CASE ... WHEN ... END CASE`, labeled loops (`LOOP`, `WHILE`, `REPEAT ... UNTIL`, `FOR ... IN`, `BREAK`, `LEAVE`, `CONTINUE`, `ITERATE`), `CALL`, `RETURN`, `RAISE`, `EXECUTE IMMEDIATE` (`INTO`, `USING`), `ASSERT`, and multi-statement transactions (`BEGIN`, `COMMIT`, `ROLLBACK`).

### Comments and Jinja2 Templates

- Preserves SQL line comments (`--`, `#`), block comments (`/* ... */`), and Jinja2 comments (`{# ... #}`).
- Preserves Jinja2 variable expressions (`{{ expr }}`), control blocks (`{% if %} ... {% elif %} ... {% else %} ... {% endif %}`, `{% for %} ... {% endfor %}`), including mid-clause predicates, commas, and conditional clauses, and statement-level directives (`{% set %}`, `{% set %} ... {% endset %}`, `{% macro %} ... {% endmacro %}`, `{% call %} ... {% endcall %}`, `{% do %}`, and `{% raw %} ... {% endraw %}`).
