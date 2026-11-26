# Changelog

All notable changes to `bqlang` are documented in this file.

## [1.2.0] - 2026-11-26

### Added

- Promoted `lsp`, `catalog`, and `bqconfig` to public Go library packages (`github.com/paulourio/bqlang/lsp`, `github.com/paulourio/bqlang/catalog`, `github.com/paulourio/bqlang/bqconfig`) for in-process embedding without `cgo`.
- Added `lsp.ServerOptions.Store`, `lsp.ServerOptions.TokenProvider`, and `lsp.ServerOptions.DefaultLSPOptions` so host applications can inject a shared `*catalog.Store`, custom OAuth token provider, and default preferences without writing `.bqfmt.toml`.
- Added custom LSP request `bqpls/statements` (advertised under `capabilities.experimental.bqplsStatements`), returning LSP UTF-16 statement ranges, coarse kinds (`QUERY`, `DML`, `DDL`, `SCRIPT`, `CALL`, `EXPORT`, `LOAD`, `OTHER`), and exact source slices with resilient fallback for unparseable documents and verbatim Jinja2/dbt templates.
- Documented custom LSP extensions (`bqpls/statements`, `bqpls/queryCost`, `bqpls/refreshCost`), client configuration payloads, and the semantic token legend in `lsp/PROTOCOL.md`, and added an embedding example to `README.md`.
- Added `(*catalog.Store).Subscribe` and `(*catalog.Store).SubscribeDryRun` for multi-subscriber async update notifications on shared stores, plus `catalog.WithRestOptions`.
- Added `catalog.NewDefaultTokenProvider(scopes ...string)` and `catalog.DescribeCredentials(ctx)` exposing the cached ADC-to-`gcloud` authentication chain for arbitrary OAuth scopes.
- Added `bqconfig.MergeLSPOptionsOver` for layering file and client settings over caller-supplied default options.
- Added the `bqpls` language server (`cmd/bqpls`), semantic analyzer, BigQuery metadata catalog with SQLite WAL disk cache, dry-run query cost estimation, view partition discovery, embedded VS Code/Kiro/Neovim extension bundles, and in-process `io.Pipe` e2e tests.

### Fixed

- Unblocked `(*lsp.Server).Serve` immediately on context cancellation and `Close()` even when reading from a blocking `io.Pipe`, while keeping `Close()` idempotent and leaving injected `*catalog.Store` instances open.
- Isolated nested `text/tabwriter` columns in `WHERE` `AND` conjuncts and binary/concat operands in `formatter`.

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
