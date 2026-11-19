# bqlang: Pure-Go GoogleSQL and BigQuery Tooling

The `bqlang` module provides a deterministic SQL formatter (`bqfmt`), an AST JSON exporter (`bqdump`), and Go library packages for GoogleSQL and BigQuery scripts. The parser runs on [`github.com/goccy/go-googlesql`](https://github.com/goccy/go-googlesql), which compiles the upstream C++ GoogleSQL parser to pure Go ahead of time via `wasm2go`. Building and running `bqlang` requires only a standard Go toolchain without `cgo`, Bazel, or C++ system libraries.

Because GoogleSQL discards comments during AST construction and rejects unrendered template tags, `bqlang` pairs the GoogleSQL parser with a dedicated Gocc scanner and parser in `extensions/`. Before parsing SQL, the formatter extracts line comments, block comments, and Jinja2 template constructs in a single pass, substitutes recoverable SQL placeholders for template blocks, aligns tokens through nested `text/tabwriter` cells, and re-injects comments and original template expressions at their tracked source coordinates.

## Installation

Install both command-line utilities with Go:

```bash
go install github.com/paulourio/bqlang/cmd/bqfmt@latest
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

When given a directory path, `bqfmt` walks the directory tree and processes files ending in `.sql`, `.bql`, `.sql.j2`, and `.bql.j2`.

| Flag | Description |
| :--- | :--- |
| `-l` | Print paths of files whose formatting differs from `bqfmt` output. |
| `-w` | Write formatted output back to source files instead of `stdout`. |
| `-config <path>` | Read print options from the specified TOML file. |
| `-debug` | Write formatter trace diagnostics to `stderr`. |

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

### Query Syntax and Expressions

- Standard `SELECT` projections, `WITH` / `WITH RECURSIVE` CTEs, set operations (`UNION`, `INTERSECT`, `EXCEPT` with `CORRESPONDING` / `BY NAME`), `GROUP BY` (`ROLLUP`, `CUBE`, `GROUPING SETS`, `GROUP BY ALL`), `HAVING`, `QUALIFY`, `WINDOW`, `PIVOT`, `UNPIVOT`, and `TABLESAMPLE`.
- GoogleSQL Pipe syntax (`FROM ... |> WHERE ... |> EXTEND ... |> AGGREGATE ... |> SET ... |> RENAME ... |> DROP ... |> JOIN ... |> CALL ...`).
- Dot-chained function calls (`(col).TRIM().LOWER()`) and inline `WITH(var AS expr, result)` scoped expressions.
- Scalar, aggregate, analytic/window, lambda (`x -> expr`), `SAFE.`-prefixed, and templated (`ANY TYPE`, `ANY TABLE`) function calls.
- All BigQuery data types and literals: scalar types, parameterized types, `ARRAY`, `STRUCT`, `JSON`, `GEOGRAPHY`, `INTERVAL`, and `RANGE`.

### DDL, DML, DCL, and Data Movement

- **DDL**: `CREATE`, `ALTER`, and `DROP` statements for schemas, databases, tables (`LIKE`, `CLONE`, `COPY`, `SNAPSHOT`, `EXTERNAL`), views (including column lists with `OPTIONS`), materialized views, functions, table functions, procedures, BigQuery ML models (`CREATE MODEL` with `TRANSFORM`), row access policies, search indexes, and `UNDROP`, plus `PRIMARY KEY`, `FOREIGN KEY`, and `CHECK` constraints (`ENFORCED` / `NOT ENFORCED`).
- **DML**: `INSERT`, `DELETE`, `UPDATE`, `MERGE`, and `TRUNCATE TABLE`, including `THEN RETURN` (`WITH ACTION`) clauses.
- **DCL**: `GRANT` and `REVOKE` privilege statements.
- **Data Movement**: `EXPORT DATA` and `LOAD DATA INTO` / `LOAD DATA OVERWRITE` (`FROM FILES`).

### Procedural Scripting

- Variable and system declarations (`DECLARE`, `SET`), block scopes (`BEGIN ... EXCEPTION WHEN ERROR THEN ... END`), conditionals (`IF ... ELSEIF ... ELSE ... END IF`, `CASE ... WHEN ... END CASE`), loops and labels (`LOOP`, `WHILE`, `REPEAT ... UNTIL`, `FOR ... IN`, `BREAK`, `LEAVE`, `CONTINUE`, `ITERATE`), control transfers (`CALL`, `RETURN`, `RAISE`, `EXECUTE IMMEDIATE`, `ASSERT`), and multi-statement transactions (`BEGIN`, `COMMIT`, `ROLLBACK`).

### Comments and Jinja2 Templates

- Preserves SQL line comments (`--`, `#`), block comments (`/* ... */`), and Jinja2 comments (`{# ... #}`).
- Preserves Jinja2 variable expressions (`{{ expr }}`) and control blocks (`{% if %} ... {% elif %} ... {% else %} ... {% endif %}`, `{% for %} ... {% endfor %}`) when each template expression occupies a valid identifier or statement position.
