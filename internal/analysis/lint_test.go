package analysis_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/bqconfig"
	"github.com/paulourio/bqlang/catalog"
	"github.com/paulourio/bqlang/internal/analysis"
)

func newLintTestEngine(t *testing.T, customOpts func(*bqconfig.LSPOptions)) *analysis.Engine {
	t.Helper()

	opts := bqconfig.DefaultLSPOptions()
	opts.DefaultProject = "proj"
	opts.DefaultDataset = "ds"
	opts.BigQueryAPIEnabled = false
	if customOpts != nil {
		customOpts(opts)
	}

	store, err := catalog.NewStore(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// 1. Optional-partitioned table (time partitioning, requirePartitionFilter = false).
	store.PutTable(&catalog.TableMeta{
		Ref:  catalog.TableRef{Project: "proj", Dataset: "ds", Table: "orders"},
		Type: catalog.TableTypeTable,
		TimePartitioning: &catalog.TimePartitioning{
			Type:  "DAY",
			Field: "order_ts",
		},
		Schema: []catalog.ColumnMeta{
			{Name: "order_id", Type: "INT64", Mode: catalog.ModeRequired},
			{Name: "order_ts", Type: "TIMESTAMP", Mode: catalog.ModeRequired},
			{Name: "ship_ts", Type: "TIMESTAMP", Mode: catalog.ModeNullable},
			{Name: "status", Type: "STRING", Mode: catalog.ModeNullable},
		},
	})

	// 2. Ingestion-time partitioned table without requirePartitionFilter.
	store.PutTable(&catalog.TableMeta{
		Ref:              catalog.TableRef{Project: "proj", Dataset: "ds", Table: "raw_logs"},
		Type:             catalog.TableTypeTable,
		TimePartitioning: &catalog.TimePartitioning{Type: "DAY"},
		Schema: []catalog.ColumnMeta{
			{Name: "msg", Type: "STRING", Mode: catalog.ModeNullable},
		},
	})

	// 3. Clustered table (multi-column: tenant_id, region) without partitioning.
	store.PutTable(&catalog.TableMeta{
		Ref:  catalog.TableRef{Project: "proj", Dataset: "ds", Table: "accounts"},
		Type: catalog.TableTypeTable,
		Clustering: &catalog.ClusteringInfo{
			Fields: []string{"tenant_id", "region"},
		},
		Schema: []catalog.ColumnMeta{
			{Name: "tenant_id", Type: "STRING", Mode: catalog.ModeRequired},
			{Name: "region", Type: "STRING", Mode: catalog.ModeNullable},
			{Name: "plan", Type: "STRING", Mode: catalog.ModeNullable},
		},
	})

	// 4. Nested-struct clustered + partitioned table.
	store.PutTable(&catalog.TableMeta{
		Ref:  catalog.TableRef{Project: "proj", Dataset: "ds", Table: "telemetry"},
		Type: catalog.TableTypeTable,
		TimePartitioning: &catalog.TimePartitioning{
			Type:  "DAY",
			Field: "event_dt",
		},
		Clustering: &catalog.ClusteringInfo{
			Fields: []string{"ctx.org_id", "kind"},
		},
		Schema: []catalog.ColumnMeta{
			{Name: "event_dt", Type: "DATE", Mode: catalog.ModeRequired},
			{Name: "kind", Type: "STRING", Mode: catalog.ModeNullable},
			{
				Name: "ctx",
				Type: "STRUCT",
				Mode: catalog.ModeNullable,
				Fields: []catalog.ColumnMeta{
					{Name: "org_id", Type: "INT64", Mode: catalog.ModeRequired},
				},
			},
		},
	})

	// 5. Flat dimension table for joins.
	store.PutTable(&catalog.TableMeta{
		Ref:  catalog.TableRef{Project: "proj", Dataset: "ds", Table: "orgs"},
		Type: catalog.TableTypeTable,
		Schema: []catalog.ColumnMeta{
			{Name: "org_id", Type: "INT64", Mode: catalog.ModeRequired},
			{Name: "tenant_id", Type: "STRING", Mode: catalog.ModeRequired},
			{Name: "created_ts", Type: "TIMESTAMP", Mode: catalog.ModeNullable},
		},
	})

	eng, err := analysis.NewEngine(store, opts)
	require.NoError(t, err)

	return eng
}

func diagCodes(diags []analysis.Diagnostic) []string {
	codes := make([]string, 0, len(diags))
	for _, d := range diags {
		codes = append(codes, d.Code)
	}

	return codes
}

func TestMissingPartitionFilterLintRules(t *testing.T) {
	t.Parallel()

	eng := newLintTestEngine(t, nil)
	ctx := t.Context()

	t.Run("missing filter on optional partitioned table", func(t *testing.T) {
		t.Parallel()
		eng.OpenDocument("file:///p_missing.sql", "SELECT order_id FROM orders WHERE status = 'PAID'", 1, nil)
		diags, err := eng.Diagnostics(ctx, "file:///p_missing.sql")
		require.NoError(t, err)
		require.Len(t, diags, 1)
		assert.Equal(t, analysis.DiagCodeMissingPartitionFilter, diags[0].Code)
		assert.Equal(t, analysis.SeverityWarning, diags[0].Severity)
		assert.Contains(t, diags[0].Message, "order_ts")
	})

	t.Run("missing filter on ingestion-time partitioned table", func(t *testing.T) {
		t.Parallel()
		eng.OpenDocument("file:///p_ing_miss.sql", "SELECT msg FROM raw_logs", 1, nil)
		diags, err := eng.Diagnostics(ctx, "file:///p_ing_miss.sql")
		require.NoError(t, err)
		require.Len(t, diags, 1)
		assert.Equal(t, analysis.DiagCodeMissingPartitionFilter, diags[0].Code)
		assert.Contains(t, diags[0].Message, "_PARTITIONTIME/_PARTITIONDATE")

		eng.OpenDocument("file:///p_ing_ok.sql", "SELECT msg FROM raw_logs WHERE _PARTITIONDATE = DATE '2026-03-01'", 1, nil)
		diagsOK, err := eng.Diagnostics(ctx, "file:///p_ing_ok.sql")
		require.NoError(t, err)
		assert.Empty(t, diagsOK)
	})

	t.Run("satisfied via WHERE, JOIN ON, CTE, BETWEEN, OR, and Pipe syntax", func(t *testing.T) {
		t.Parallel()
		validQueries := []string{
			"SELECT order_id FROM orders WHERE order_ts >= TIMESTAMP '2026-03-01 00:00:00 UTC'",
			"SELECT order_id FROM orders WHERE order_ts BETWEEN TIMESTAMP '2026-03-01' AND TIMESTAMP '2026-03-02'",
			"SELECT order_id FROM orders WHERE order_ts = TIMESTAMP '2026-03-01' OR order_ts = TIMESTAMP '2026-03-02'",
			"SELECT o.order_id FROM orgs g JOIN orders o ON o.order_ts >= g.created_ts",
			"WITH c AS (SELECT order_id, order_ts FROM orders) SELECT order_id FROM c WHERE order_ts >= TIMESTAMP '2026-03-01'",
			"FROM orders |> WHERE order_ts >= TIMESTAMP '2026-03-01' |> SELECT order_id",
			"DELETE FROM orders WHERE order_ts < TIMESTAMP '2025-01-01'",
		}

		for i, q := range validQueries {
			uri := "file:///p_valid_" + string(rune('a'+i)) + ".sql"
			eng.OpenDocument(uri, q, 1, nil)
			diags, err := eng.Diagnostics(ctx, uri)
			require.NoError(t, err, q)
			assert.Empty(t, diags, "expected no lint warnings for %q", q)
		}
	})

	t.Run("non-sargable function wrapper, cast, unpruned OR, and intra-table comparison", func(t *testing.T) {
		t.Parallel()
		nonSargable := []string{
			"SELECT order_id FROM orders WHERE DATE(order_ts) = DATE '2026-03-01'",
			"SELECT order_id FROM orders WHERE EXTRACT(YEAR FROM order_ts) = 2026",
			"SELECT order_id FROM orders WHERE CAST(order_ts AS STRING) LIKE '2026%'",
			"SELECT order_id FROM orders WHERE order_ts >= TIMESTAMP '2026-03-01' OR status = 'OPEN'",
			"SELECT order_id FROM orders WHERE order_ts >= ship_ts",
		}

		for i, q := range nonSargable {
			uri := "file:///p_nonsarg_" + string(rune('a'+i)) + ".sql"
			eng.OpenDocument(uri, q, 1, nil)
			diags, err := eng.Diagnostics(ctx, uri)
			require.NoError(t, err, q)
			require.Len(t, diags, 1, "expected non-sargable warning for %q", q)
			assert.Equal(t, analysis.DiagCodeMissingPartitionFilter, diags[0].Code)
			assert.Contains(t, strings.ToLower(diags[0].Message), "not sargable", q)
		}
	})
}

func TestClusteringFilterLintRules(t *testing.T) {
	t.Parallel()

	eng := newLintTestEngine(t, nil)
	ctx := t.Context()

	t.Run("unused clustering columns emit clustering_filter", func(t *testing.T) {
		t.Parallel()
		eng.OpenDocument("file:///c_missing.sql", "SELECT plan FROM accounts WHERE plan = 'PRO'", 1, nil)
		diags, err := eng.Diagnostics(ctx, "file:///c_missing.sql")
		require.NoError(t, err)
		require.Len(t, diags, 1)
		assert.Equal(t, analysis.DiagCodeClusteringFilter, diags[0].Code)
		assert.Equal(t, analysis.SeverityWarning, diags[0].Severity)
		assert.Contains(t, diags[0].Message, "(tenant_id, region)")
		assert.Contains(t, diags[0].Message, "tenant_id")
	})

	t.Run("secondary cluster column without leading column emits prefix warning", func(t *testing.T) {
		t.Parallel()
		eng.OpenDocument("file:///c_prefix.sql", "SELECT plan FROM accounts WHERE region = 'US'", 1, nil)
		diags, err := eng.Diagnostics(ctx, "file:///c_prefix.sql")
		require.NoError(t, err)
		require.Len(t, diags, 1)
		assert.Equal(t, analysis.DiagCodeClusteringFilter, diags[0].Code)
		assert.Contains(t, diags[0].Message, "without leading cluster column tenant_id")
	})

	t.Run("non-sargable wrapper on leading cluster column", func(t *testing.T) {
		t.Parallel()
		eng.OpenDocument("file:///c_nonsarg.sql", "SELECT plan FROM accounts WHERE LOWER(tenant_id) = 'acme'", 1, nil)
		diags, err := eng.Diagnostics(ctx, "file:///c_nonsarg.sql")
		require.NoError(t, err)
		require.Len(t, diags, 1)
		assert.Equal(t, analysis.DiagCodeClusteringFilter, diags[0].Code)
		assert.Contains(t, strings.ToLower(diags[0].Message), "not sargable")
	})

	t.Run("satisfied via WHERE, JOIN ON, JOIN USING, IN, LIKE, STARTS_WITH, nested STRUCT, DML, and Pipe", func(t *testing.T) {
		t.Parallel()
		validQueries := []string{
			"SELECT plan FROM accounts WHERE tenant_id = 'acme'",
			"SELECT plan FROM accounts WHERE tenant_id IN ('acme', 'globex')",
			"SELECT plan FROM accounts WHERE tenant_id IN UNNEST(@tenants)",
			"SELECT plan FROM accounts WHERE tenant_id IN (SELECT tenant_id FROM orgs)",
			"SELECT plan FROM accounts WHERE tenant_id LIKE 'acme%'",
			"SELECT plan FROM accounts WHERE STARTS_WITH(tenant_id, 'acme')",
			"SELECT a.plan FROM orgs g JOIN accounts a ON g.tenant_id = a.tenant_id",
			"SELECT a.plan FROM orgs g JOIN accounts a USING (tenant_id)",
			"SELECT kind FROM telemetry WHERE event_dt = DATE '2026-03-01' AND ctx.org_id = 42",
			"SELECT t.kind FROM orgs g JOIN telemetry t ON g.org_id = t.ctx.org_id WHERE t.event_dt = DATE '2026-03-01'",
			"WITH c AS (SELECT event_dt, ctx.org_id AS oid, kind FROM telemetry) SELECT kind FROM c WHERE event_dt = DATE '2026-03-01' AND oid = 7",
			"FROM accounts |> WHERE tenant_id = 'acme' |> SELECT plan",
			"UPDATE accounts SET plan = 'ENT' WHERE tenant_id = 'acme'",
			"MERGE INTO accounts t USING orgs s ON t.tenant_id = s.tenant_id WHEN MATCHED THEN UPDATE SET plan = 'ENT'",
		}

		for i, q := range validQueries {
			uri := "file:///c_valid_" + string(rune('a'+i)) + ".sql"
			eng.OpenDocument(uri, q, 1, nil)
			diags, err := eng.Diagnostics(ctx, uri)
			require.NoError(t, err, q)
			assert.Empty(t, diags, "expected no lint warnings for %q", q)
		}
	})

	t.Run("partitioned and clustered table emits both warnings when unfiltered and respects severities", func(t *testing.T) {
		t.Parallel()
		uri := "file:///both.sql"
		eng.OpenDocument(uri, "SELECT kind FROM telemetry", 1, nil)

		diags, err := eng.Diagnostics(ctx, uri)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{
			analysis.DiagCodeMissingPartitionFilter,
			analysis.DiagCodeClusteringFilter,
		}, diagCodes(diags))

		// Reconfigure severities without re-parsing: missing_partition = error, clustering = off.
		custom := bqconfig.DefaultLSPOptions()
		custom.DefaultProject = "proj"
		custom.DefaultDataset = "ds"
		custom.MissingPartitionFilterSeverity = bqconfig.SeverityError
		custom.ClusteringFilterSeverity = bqconfig.SeverityOff
		require.NoError(t, eng.UpdateDocumentConfig(uri, &analysis.DocumentConfig{Options: custom}))

		diags2, err := eng.Diagnostics(ctx, uri)
		require.NoError(t, err)
		require.Len(t, diags2, 1)
		assert.Equal(t, analysis.DiagCodeMissingPartitionFilter, diags2[0].Code)
		assert.Equal(t, analysis.SeverityError, diags2[0].Severity)
	})
}
