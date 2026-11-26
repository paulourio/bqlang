package analysis_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/bqconfig"
	"github.com/paulourio/bqlang/catalog"
	"github.com/paulourio/bqlang/internal/analysis"
	"github.com/paulourio/bqlang/internal/syntax"
)

func TestCostFormattingHelpers(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "0 B", analysis.FormatBytes(0))
	assert.Equal(t, "512 B", analysis.FormatBytes(512))
	assert.Equal(t, "1.00 KiB", analysis.FormatBytes(1024))
	assert.Equal(t, "256.0 MiB", analysis.FormatBytes(256<<20))
	assert.Equal(t, "1.50 GiB", analysis.FormatBytes(3*(1<<29)))
	assert.Equal(t, "2.00 TiB", analysis.FormatBytes(2*analysis.BytesPerTiB))

	assert.Equal(t, "$0.00", analysis.FormatUSD(0))
	assert.Equal(t, "<$0.01", analysis.FormatUSD(0.0007))
	assert.Equal(t, "$0.01", analysis.FormatUSD(0.006))
	assert.Equal(t, "$6.25", analysis.FormatUSD(6.25))

	assert.InDelta(t, 6.25, analysis.EstimateCostUSD(analysis.BytesPerTiB, 6.25), 1e-9)
	assert.InDelta(t, 0.0, analysis.EstimateCostUSD(0, 6.25), 1e-9)

	assert.Equal(t, "42", analysis.FormatRowCount(42))
	assert.Equal(t, "1,000", analysis.FormatRowCount(1000))
	assert.Equal(t, "1,250,000", analysis.FormatRowCount(1250000))
}

func makeSizedTable(proj, ds, tbl string, numRows, numBytes int64, partitioned bool) *catalog.TableMeta {
	rows := catalog.FlexInt64(numRows)
	bytes := catalog.FlexInt64(numBytes)
	meta := &catalog.TableMeta{
		Ref:      catalog.TableRef{Project: proj, Dataset: ds, Table: tbl},
		Type:     catalog.TableTypeTable,
		NumRows:  &rows,
		NumBytes: &bytes,
		Schema: []catalog.ColumnMeta{
			{Name: "id", Type: "INT64", Mode: catalog.ModeRequired},
			{Name: "dt", Type: "DATE", Mode: catalog.ModeNullable},
		},
	}
	if partitioned {
		meta.TimePartitioning = &catalog.TimePartitioning{
			Type:  "DAY",
			Field: "dt",
		}
	}
	meta.Normalize(meta.Ref)

	return meta
}

func TestOfflineCostCodeLensDiagnosticsAndHover(t *testing.T) {
	t.Parallel()

	opts := bqconfig.DefaultLSPOptions()
	opts.DefaultProject = "proj"
	opts.DefaultDataset = "ds"
	opts.CostWarningThresholdUSD = 5.0
	opts.CostDiagnosticSeverity = bqconfig.SeverityWarning

	store, err := catalog.NewStore(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// 1 TiB table => $6.25 full scan (exceeds $5.00 threshold).
	store.PutTable(makeSizedTable("proj", "ds", "big_events", 5000000, analysis.BytesPerTiB, true))
	// 1 GiB table => <$0.01 full scan.
	store.PutTable(makeSizedTable("proj", "ds", "small_users", 1000, 1<<30, false))

	eng, err := analysis.NewEngine(store, opts)
	require.NoError(t, err)

	uri := "file:///queries/report.sql"
	sqlText := "SELECT a.id FROM proj.ds.big_events AS a JOIN proj.ds.big_events AS b ON a.id = b.id WHERE a.dt = DATE '2026-01-01';\nSELECT id FROM proj.ds.small_users;\n"
	eng.OpenDocument(uri, sqlText, 1, &analysis.DocumentConfig{Store: store, Options: opts})

	summary, err := eng.DocumentCost(t.Context(), uri)
	require.NoError(t, err)
	require.Len(t, summary.Statements, 2)

	// Self-join deduplicates big_events to 1 TiB (not 2 TiB) and detects partition filtering.
	st0 := summary.Statements[0]
	assert.Equal(t, analysis.CostSourceOffline, st0.Source)
	assert.Equal(t, analysis.BytesPerTiB, st0.BytesProcessed)
	assert.InDelta(t, 6.25, st0.EstimatedCostUSD, 1e-6)
	assert.True(t, st0.PartitionFiltered)
	assert.True(t, st0.ExceedsThreshold)
	assert.Contains(t, st0.Label, "⚠ Est. ≤ 1.00 TiB ($6.25, offline, partition-filtered)")

	st1 := summary.Statements[1]
	assert.Equal(t, int64(1<<30), st1.BytesProcessed)
	assert.False(t, st1.ExceedsThreshold)
	assert.Equal(t, "Est. ≤ 1.00 GiB ($0.01, offline)", st1.Label)

	// CodeLens returns one lens per statement.
	lenses, err := eng.CodeLenses(t.Context(), uri)
	require.NoError(t, err)
	require.Len(t, lenses, 2)
	assert.Equal(t, 0, lenses[0].Range.Start.Line)
	assert.Equal(t, 1, lenses[1].Range.Start.Line)
	assert.Equal(t, analysis.CommandRefreshCost, lenses[0].Command.Command)

	// Threshold diagnostic emitted only for statement 0.
	diags, err := eng.Diagnostics(t.Context(), uri)
	require.NoError(t, err)
	var costDiags []analysis.Diagnostic
	for _, d := range diags {
		if d.Code == "estimated_cost" {
			costDiags = append(costDiags, d)
		}
	}
	require.Len(t, costDiags, 1)
	assert.Equal(t, analysis.SeverityWarning, costDiags[0].Severity)
	assert.Equal(t, 0, costDiags[0].Range.Start.Line)
	assert.Contains(t, costDiags[0].Message, "1.00 TiB")
	assert.Contains(t, costDiags[0].Message, "$5.00")

	// Table hover shows humanized row count, humanized bytes, and full scan cost.
	hover, err := eng.Hover(t.Context(), uri, syntax.Position{Line: 0, Character: 26})
	require.NoError(t, err)
	require.NotNil(t, hover)
	assert.Contains(t, hover.Contents.Value, "5,000,000 rows · 1.00 TiB (full scan $6.25)")

	// Dry-run result overrides offline estimate and clears warning if pruned below threshold.
	store.PutDryRun("proj", "ds", "SELECT a.id FROM proj.ds.big_events AS a JOIN proj.ds.big_events AS b ON a.id = b.id WHERE a.dt = DATE '2026-01-01'", &catalog.DryRunResult{
		TotalBytesProcessed: 50 << 20,
		TotalBytesBilled:    50 << 20,
		Accuracy:            "PRECISE",
		StatementType:       "SELECT",
	})
	eng.InvalidateCost()

	summary2, err := eng.DocumentCost(t.Context(), uri)
	require.NoError(t, err)
	require.Len(t, summary2.Statements, 2)
	assert.Equal(t, analysis.CostSourceDryRun, summary2.Statements[0].Source)
	assert.Equal(t, int64(50<<20), summary2.Statements[0].BytesProcessed)
	assert.False(t, summary2.Statements[0].ExceedsThreshold)
	assert.Equal(t, "Est. 50.00 MiB (<$0.01)", summary2.Statements[0].Label)

	diags2, err := eng.Diagnostics(t.Context(), uri)
	require.NoError(t, err)
	for _, d := range diags2 {
		assert.NotEqual(t, "estimated_cost", d.Code, "pruned dry-run should clear cost warning")
	}
}

func TestCostSettingsDisableSurfaces(t *testing.T) {
	t.Parallel()

	opts := bqconfig.DefaultLSPOptions()
	opts.DefaultProject = "proj"
	opts.DefaultDataset = "ds"
	opts.CostCodeLensEnabled = false
	opts.CostHoverEnabled = false
	opts.CostDiagnosticSeverity = bqconfig.SeverityOff
	opts.CostWarningThresholdBytes = 1

	store, err := catalog.NewStore(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	store.PutTable(makeSizedTable("proj", "ds", "events", 1000, 1<<30, false))

	eng, err := analysis.NewEngine(store, opts)
	require.NoError(t, err)

	uri := "file:///queries/disabled.sql"
	eng.OpenDocument(uri, "SELECT id FROM proj.ds.events", 1, &analysis.DocumentConfig{Store: store, Options: opts})

	lenses, err := eng.CodeLenses(t.Context(), uri)
	require.NoError(t, err)
	assert.Empty(t, lenses)

	diags, err := eng.Diagnostics(t.Context(), uri)
	require.NoError(t, err)
	for _, d := range diags {
		assert.NotEqual(t, "estimated_cost", d.Code)
	}

	hover, err := eng.Hover(t.Context(), uri, syntax.Position{Line: 0, Character: 24})
	require.NoError(t, err)
	require.NotNil(t, hover)
	assert.NotContains(t, hover.Contents.Value, "full scan")
	assert.Contains(t, hover.Contents.Value, "1000 rows")
}

func TestDryRunTargetsSkipsJinjaPlaceholders(t *testing.T) {
	t.Parallel()

	opts := bqconfig.DefaultLSPOptions()
	opts.DefaultProject = "proj"
	opts.DefaultDataset = "ds"
	opts.BigQueryAPIEnabled = true
	opts.CacheDir = t.TempDir()

	store, err := catalog.NewStore(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	eng, err := analysis.NewEngine(store, opts)
	require.NoError(t, err)

	uri := "file:///models/jinja.sql"
	eng.OpenDocument(uri, "SELECT id FROM {{ ref('orders') }} WHERE x = 1", 1, &analysis.DocumentConfig{Store: store, Options: opts})

	_, _, queries, ok := eng.DryRunTargets(uri)
	assert.False(t, ok)
	assert.Empty(t, queries)
}

func TestDocumentCostReportsInvalidSQLError(t *testing.T) {
	t.Parallel()

	opts := bqconfig.DefaultLSPOptions()
	store, err := catalog.NewStore(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	eng, err := analysis.NewEngine(store, opts)
	require.NoError(t, err)

	uri := "file:///queries/invalid.sql"
	eng.OpenDocument(uri, "SELECT FROM broken_table", 1, &analysis.DocumentConfig{Store: store, Options: opts})

	summary, err := eng.DocumentCost(t.Context(), uri)
	require.NoError(t, err)
	assert.Equal(t, analysis.CostStatusError, summary.Status)
	assert.Equal(t, 1, summary.ErrorCount)
	assert.Equal(t, "Invalid SQL (Ln 1)", summary.Summary)
	assert.NotEmpty(t, summary.ErrorMessage)
	require.Len(t, summary.Errors, 1)
	assert.Equal(t, "syntax", summary.Errors[0].Source)
	assert.Equal(t, 1, summary.Errors[0].Line)
}
