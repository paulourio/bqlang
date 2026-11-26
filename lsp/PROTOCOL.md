# bqpls Custom LSP Protocol Reference

This specification defines the custom JSON-RPC 2.0 methods, client configuration payloads, and semantic token legend exposed by `github.com/paulourio/bqlang/lsp` on top of LSP 3.17. All positions and ranges use 0-based UTF-16 line and character offsets as defined by LSP 3.17.

## 1. Capability Advertisement

In the `initialize` response, `bqpls` advertises custom request support under `capabilities.experimental` and its semantic token legend under `capabilities.semanticTokensProvider.legend`:

```json
{
  "capabilities": {
    "semanticTokensProvider": {
      "legend": {
        "tokenTypes": [
          "namespace", "class", "function", "variable", "property",
          "parameter", "keyword", "type", "string", "number",
          "comment", "operator", "macro", "enumMember"
        ],
        "tokenModifiers": [
          "declaration", "readonly", "defaultLibrary",
          "partitioned", "clustered", "pseudo", "modification"
        ]
      },
      "range": true,
      "full": { "delta": true }
    },
    "experimental": {
      "bqplsStatements": true
    }
  }
}
```

## 2. Client Settings (`initializationOptions` and `workspace/didChangeConfiguration`)

Both `initialize.initializationOptions` and `workspace/didChangeConfiguration.settings` accept a JSON object containing the keys of the `[lsp]` table in `.bqfmt.toml`. Keys are accepted in either `snake_case` or `camelCase`, and may optionally be wrapped inside a top-level `"bqpls"` or `"lsp"` object. Unrecognized keys are ignored. Explicit `.bqfmt.toml` values take precedence over client settings, which in turn take precedence over `ServerOptions.DefaultLSPOptions` and built-in defaults.

```json
{
  "bqpls": {
    "default_project": "my-gcp-project",
    "default_dataset": "analytics",
    "schema_dirs": ["./schemas"],
    "bigquery_api_enabled": true,
    "gcloud_path": "",
    "cache_dir": "/path/to/cache",
    "cache_ttl_seconds": 86400,
    "project_cache_ttl_seconds": {
      "bigquery-public-data": 2592000
    },
    "diagnostics_enabled": true,
    "require_partition_filter_severity": "warning",
    "missing_partition_filter_severity": "warning",
    "clustering_filter_severity": "warning",
    "cost_estimation_enabled": true,
    "cost_codelens_enabled": true,
    "cost_status_bar_enabled": true,
    "cost_hover_enabled": true,
    "cost_diagnostic_severity": "warning",
    "cost_warning_threshold_bytes": 0,
    "cost_warning_threshold_usd": 0.0,
    "cost_price_per_tib_usd": 6.25,
    "cost_dry_run_debounce_ms": 800
  }
}
```

Equivalent `camelCase` keys are `defaultProject`, `defaultDataset`, `schemaDirs`, `bigqueryApiEnabled`, `gcloudPath`, `cacheDir`, `cacheTtlSeconds`, `projectCacheTtlSeconds`, `diagnosticsEnabled`, `requirePartitionFilterSeverity`, `missingPartitionFilterSeverity`, `clusteringFilterSeverity`, `costEstimationEnabled`, `costCodeLensEnabled`, `costStatusBarEnabled`, `costHoverEnabled`, `costDiagnosticSeverity`, `costWarningThresholdBytes`, `costWarningThresholdUsd`, `costPricePerTibUsd`, and `costDryRunDebounceMs`.

## 3. Custom Requests and Notifications

### 3.1 `bqpls/statements` (Client -> Server Request)

Returns top-level SQL statement boundaries, coarse statement classes, and exact source slices for an open document. Used by editors and GUI clients to implement "Run statement at cursor" and script splitting.

- **Capability**: `capabilities.experimental.bqplsStatements == true`
- **Failure mode**: Returns JSON-RPC `-32602` (`InvalidParams`) when `textDocument.uri` is not open. On open documents, parsing failures never return an RPC error; the server falls back to resilient top-level `;` splitting and leading-keyword classification.
- **Empty document**: Returns `{"statements": []}` (never `null`).

**Request parameters:**

```json
{
  "textDocument": {
    "uri": "file:///workspace/query.sql"
  }
}
```

**Response payload:**

```json
{
  "statements": [
    {
      "range": {
        "start": { "line": 1, "character": 0 },
        "end": { "line": 1, "character": 52 }
      },
      "kind": "QUERY",
      "text": "SELECT id FROM {{ ref('orders') }} WHERE active = 1"
    },
    {
      "range": {
        "start": { "line": 2, "character": 0 },
        "end": { "line": 2, "character": 33 }
      },
      "kind": "DDL",
      "text": "CREATE TEMP TABLE tmp AS SELECT 1"
    }
  ]
}
```

| Field | Type | Contract |
| :--- | :--- | :--- |
| `range` | `Range` | Half-open LSP UTF-16 range covering the statement, excluding the trailing `;` and surrounding whitespace or comments. |
| `kind` | `string` | One of `QUERY`, `DML`, `DDL`, `SCRIPT`, `CALL`, `EXPORT`, `LOAD`, or `OTHER`. |
| `text` | `string` | Exact slice of the document source covered by `range`; Jinja2/dbt templates stay verbatim. |

### 3.2 `bqpls/queryCost` (Server -> Client Notification)

Published asynchronously when a document opens, changes, saves, closes, or finishes a background BigQuery dry-run. Controlled by `cost_estimation_enabled` and `cost_status_bar_enabled`.

**Notification parameters (`DocumentCostSummary`):**

```json
{
  "uri": "file:///workspace/query.sql",
  "version": 3,
  "enabled": true,
  "status": "ok",
  "totalBytesProcessed": 1073741824,
  "totalBytesBilled": 1073741824,
  "totalEstimatedCostUsd": 0.006103515625,
  "formattedBytes": "1.00 GiB",
  "formattedCost": "$0.01",
  "summary": "1.00 GiB ($0.01)",
  "source": "dry_run",
  "exceedsThreshold": false,
  "statements": [
    {
      "index": 0,
      "range": {
        "start": { "line": 0, "character": 0 },
        "end": { "line": 0, "character": 26 }
      },
      "bytesProcessed": 1073741824,
      "bytesBilled": 1073741824,
      "estimatedCostUsd": 0.006103515625,
      "formattedBytes": "1.00 GiB",
      "formattedCost": "$0.01",
      "label": "1.00 GiB ($0.01)",
      "source": "dry_run",
      "accuracy": "exact",
      "statementType": "SELECT",
      "cacheHit": false,
      "partitionFiltered": true,
      "exceedsThreshold": false,
      "referencedTables": [
        {
          "table": "proj.ds.orders",
          "numBytes": 1073741824,
          "hasNumBytes": true,
          "partitioned": true,
          "partitionFiltered": true
        }
      ]
    }
  ]
}
```

| Field | Values / Notes |
| :--- | :--- |
| `status` | `""` (empty), `"ok"`, `"error"`, `"pending"`, or `"unavailable"`. |
| `source` | `"dry_run"` (BigQuery `jobs.insert` dry-run) or `"offline"` (table-size upper bound from cached `numBytes`). |
| `errors` | Optional array of `{ "line"?: int, "column"?: int, "source": string, "message": string }` when `status == "error"`. |
| `errorCount`, `errorMessage` | Summary count and primary message when `status == "error"`. |
| `statements[].thresholdReason` | Optional explanation present when `exceedsThreshold == true`. |

When a document closes or status-bar reporting is disabled, `bqpls` emits a clearing notification with `"enabled": false` and `"statements": []`.

### 3.3 `bqpls/refreshCost` (Client -> Server Request)

Cancels any pending debounce timer for `textDocument.uri`, forces an immediate background BigQuery dry-run refresh when `bigquery_api_enabled` is enabled, and returns the current `DocumentCostSummary` object (same schema as `bqpls/queryCost`). When the asynchronous dry-run finishes, `bqpls` publishes an updated `bqpls/queryCost` notification and requests `workspace/codeLens/refresh` if the client advertises support.

**Request parameters:**

```json
{
  "textDocument": {
    "uri": "file:///workspace/query.sql"
  }
}
```

## 4. Semantic Token Legend

The server encodes `textDocument/semanticTokens/full`, `textDocument/semanticTokens/full/delta`, and `textDocument/semanticTokens/range` using a fixed 14-type, 7-modifier legend.

### Token Types (Index 0..13)

| Index | Name | SQL / Template Usage |
| :--- | :--- | :--- |
| 0 | `namespace` | Project and dataset qualifiers in table paths |
| 1 | `class` | Tables, views, CTEs, and table aliases |
| 2 | `function` | Built-in, scalar, aggregate, window, and user-defined functions |
| 3 | `variable` | Script `DECLARE` variables, system variables (`@@...`), and Jinja variables |
| 4 | `property` | Columns and nested `STRUCT` fields |
| 5 | `parameter` | Named query parameters (`@param`) and function/procedure parameters |
| 6 | `keyword` | SQL keywords and Jinja tag keywords |
| 7 | `type` | SQL data types (`INT64`, `STRING`, `STRUCT`, `ARRAY`, `JSON`, etc.) |
| 8 | `string` | String and bytes literals |
| 9 | `number` | Numeric and hexadecimal literals |
| 10 | `comment` | SQL (`--`, `#`, `//`, `/* ... */`) and Jinja (`{# ... #}`) comments |
| 11 | `operator` | SQL (`|>`, `->`, `=>`, `||`, arithmetic/comparison) and Jinja operators |
| 12 | `macro` | Jinja delimiters (`{{`, `}}`, `{%`, `%}`) and dbt macros |
| 13 | `enumMember` | Date/time part keywords (`DAY`, `MONTH`, `YEAR`, etc.) |

### Token Modifiers (Bitmask `1 << bit`)

| Bit | Mask | Name | Meaning |
| :--- | :--- | :--- | :--- |
| 0 | `1` (`0x01`) | `declaration` | Symbol definition site (CTE name, column alias, `DECLARE` variable, Jinja `macro`/`set`) |
| 1 | `2` (`0x02`) | `readonly` | Read-only system variables or constants |
| 2 | `4` (`0x04`) | `defaultLibrary` | Built-in BigQuery functions and system symbols |
| 3 | `8` (`0x08`) | `partitioned` | Table partition column (resolved from catalog metadata or view partition probing) |
| 4 | `16` (`0x10`) | `clustered` | Table clustering column |
| 5 | `32` (`0x20`) | `pseudo` | BigQuery pseudo-columns (`_PARTITIONTIME`, `_PARTITIONDATE`, `_TABLE_SUFFIX`, `_FILE_NAME`) |
| 6 | `64` (`0x40`) | `modification` | Target table or column modified by DML (`INSERT`, `UPDATE`, `DELETE`, `MERGE`) |
