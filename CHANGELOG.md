# Changelog

All notable changes to `bqlang` are documented in this file.

## [1.1.0] - 2026-11-21

### Added

- Migrated the parser backend from CGO `go-zetasql` to pure-Go `github.com/goccy/go-googlesql`, removing CGO, Bazel, and C++ build dependencies.
- Added formatting for modern BigQuery and GoogleSQL syntax:
  - Pipe operators (`|>`, including `WITH`, `ASSERT`, `DESCRIBE`, `CALL`, `JOIN`, `PIVOT`, `UNPIVOT`, `MATCH_RECOGNIZE`, and DML/DDL sinks).
  - Property Graphs and GQL (`GRAPH_TABLE`, `MATCH`, `OPTIONAL MATCH`, `LET`, `FILTER`, `NEXT`, path search prefixes, path modes, and edge orientations).
  - Row pattern matching (`MATCH_RECOGNIZE`), multi-level aggregate modifiers (`WHERE`, `GROUP BY`, `HAVING`, `WITH REPORT`), and quantified `LIKE`.
  - BigQuery schema, index (`SEARCH INDEX`, `VECTOR INDEX`), model, connection, and reservation DDL, plus 35 additional GoogleSQL AST visitors covering module/catalog DDL, batch/utility statements, and `NEW` constructors.
- Expanded Jinja2 and dbt template support:
  - Mid-clause `{% if %}` and `{% for %}` blocks for leading/trailing comma column lists, `AND`/`OR` predicate continuations, and conditional SQL clauses (`WHERE`, `HAVING`, `QUALIFY`, `GROUP BY`, `ORDER BY`, `LIMIT`, `JOIN`, `UNION`, `CASE WHEN`).
  - Block-style `{% set %} ... {% endset %}`, `{% macro %} ... {% endmacro %}`, `{% call %} ... {% endcall %}`, `{% do ... %}`, and `{% raw %} ... {% endraw %}` tags.
- Improved `bqfmt` CLI ergonomics:
  - Upward `.bqfmt.toml` discovery with multi-style `regex` and `include` path matching.
  - Added `-d` (unified diff), `-check` (CI exit codes `0`/`1`/`2`), `-stdin-filename`, and `-version` flags.
  - Skipped hidden subdirectories and `node_modules` via `filepath.SkipDir` during directory walks.
- Added formatter benchmarks (`formatter/benchmark_test.go`), fuzz targets (`FuzzExtractAll`, `FuzzPrintOptions`, `FuzzFormatInput`), and expanded golden and regression test suites.
- Added `LICENSE`.

### Fixed

- Preserved `ColumnListTrailingComma=AUTO` trailing commas, DDL/DML/transaction clauses, query/pipe/expression precedence, string/bytes literals, and comment alignment using collision-free sentinels.
- Fixed `bqfmt` symlink truncation, backup restoration, directory-walk error handling, config glob/section validation, and `bqdump` exit status reporting.
- Fixed `extensions` comment offset clamping, AST dump state, and stderr suppression in `FormatError`.

### Changed

- Upgraded Go dependencies and migrated `.golangci.yml` to `golangci-lint` v2.
- Removed obsolete prototype commands and updated `README.md` for the pure-Go tooling surface.

## [1.0.0] - 2025-07-05

### Added

- Initial release of `bqlang`, `bqfmt`, `bqdump`, and the `extensions` comment and Jinja2 scanner built on `go-zetasql`.
