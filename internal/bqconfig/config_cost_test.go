package bqconfig_test

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/internal/bqconfig"
)

func TestDefaultCostLSPOptions(t *testing.T) {
	t.Parallel()

	opts := bqconfig.DefaultLSPOptions()
	require.NoError(t, opts.Validate())
	assert.True(t, opts.CostEstimationEnabled)
	assert.True(t, opts.CostCodeLensEnabled)
	assert.True(t, opts.CostStatusBarEnabled)
	assert.True(t, opts.CostHoverEnabled)
	assert.Equal(t, bqconfig.SeverityWarning, opts.CostDiagnosticSeverity)
	assert.Equal(t, int64(0), opts.CostWarningThresholdBytes)
	assert.InDelta(t, 0.0, opts.CostWarningThresholdUSD, 1e-9)
	assert.InDelta(t, 6.25, opts.CostPricePerTiBUSD, 1e-9)
	assert.Equal(t, 800, opts.CostDryRunDebounceMs)
}

func TestCostLSPOptionsTOMLDecodeAndMerge(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, bqconfig.ConfigFileName)
	content := `
[lsp]
gcloud_path = "~/.gcloudsdk/bin/gcloud"
cost_estimation_enabled = true
cost_codelens_enabled = false
cost_status_bar_enabled = true
cost_hover_enabled = false
cost_diagnostic_severity = "error"
cost_warning_threshold_bytes = 10737418240
cost_warning_threshold_usd = 2.5
cost_price_per_tib_usd = 7.5
cost_dry_run_debounce_ms = 250
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0o600))

	loader, err := bqconfig.NewLoader(cfgPath)
	require.NoError(t, err)

	loaded, err := loader.ConfigFor(filepath.Join(dir, "query.sql"), false)
	require.NoError(t, err)

	assert.Equal(t, "~/.gcloudsdk/bin/gcloud", loaded.LSP.GcloudPath)
	assert.True(t, loaded.LSPDefined.GcloudPath)
	assert.True(t, loaded.LSP.CostEstimationEnabled)
	assert.False(t, loaded.LSP.CostCodeLensEnabled)
	assert.True(t, loaded.LSP.CostStatusBarEnabled)
	assert.False(t, loaded.LSP.CostHoverEnabled)
	assert.Equal(t, bqconfig.SeverityError, loaded.LSP.CostDiagnosticSeverity)
	assert.Equal(t, int64(10737418240), loaded.LSP.CostWarningThresholdBytes)
	assert.InDelta(t, 2.5, loaded.LSP.CostWarningThresholdUSD, 1e-9)
	assert.InDelta(t, 7.5, loaded.LSP.CostPricePerTiBUSD, 1e-9)
	assert.Equal(t, 250, loaded.LSP.CostDryRunDebounceMs)

	assert.True(t, loaded.LSPDefined.CostEstimationEnabled)
	assert.True(t, loaded.LSPDefined.CostCodeLensEnabled)
	assert.True(t, loaded.LSPDefined.CostStatusBarEnabled)
	assert.True(t, loaded.LSPDefined.CostHoverEnabled)
	assert.True(t, loaded.LSPDefined.CostDiagnosticSeverity)
	assert.True(t, loaded.LSPDefined.CostWarningThresholdBytes)
	assert.True(t, loaded.LSPDefined.CostWarningThresholdUSD)
	assert.True(t, loaded.LSPDefined.CostPricePerTiBUSD)
	assert.True(t, loaded.LSPDefined.CostDryRunDebounceMs)

	// Client sets cost_codelens_enabled = true and cost_price_per_tib_usd = 9.0,
	// while file only defines cost_codelens_enabled = false: file wins on codelens, client wins on price.
	clientOpts := bqconfig.DefaultLSPOptions()
	clientOpts.CostCodeLensEnabled = true
	clientOpts.CostPricePerTiBUSD = 9.0
	clientDef := bqconfig.LSPDefinedFields{
		CostCodeLensEnabled: true,
		CostPricePerTiBUSD:  true,
	}

	filePartial := bqconfig.DefaultLSPOptions()
	filePartial.CostCodeLensEnabled = false
	filePartialDef := bqconfig.LSPDefinedFields{
		CostCodeLensEnabled: true,
	}

	merged := bqconfig.MergeLSPOptions(filePartial, filePartialDef, clientOpts, clientDef)
	assert.False(t, merged.CostCodeLensEnabled)
	assert.InDelta(t, 9.0, merged.CostPricePerTiBUSD, 1e-9)
}

func TestCostLSPOptionsValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*bqconfig.LSPOptions)
	}{
		{
			name:   "invalid cost severity",
			mutate: func(o *bqconfig.LSPOptions) { o.CostDiagnosticSeverity = "fatal" },
		},
		{
			name:   "negative threshold bytes",
			mutate: func(o *bqconfig.LSPOptions) { o.CostWarningThresholdBytes = -1 },
		},
		{
			name:   "negative threshold usd",
			mutate: func(o *bqconfig.LSPOptions) { o.CostWarningThresholdUSD = -0.1 },
		},
		{
			name:   "nan threshold usd",
			mutate: func(o *bqconfig.LSPOptions) { o.CostWarningThresholdUSD = math.NaN() },
		},
		{
			name:   "inf threshold usd",
			mutate: func(o *bqconfig.LSPOptions) { o.CostWarningThresholdUSD = math.Inf(1) },
		},
		{
			name:   "negative price per tib",
			mutate: func(o *bqconfig.LSPOptions) { o.CostPricePerTiBUSD = -1 },
		},
		{
			name:   "nan price per tib",
			mutate: func(o *bqconfig.LSPOptions) { o.CostPricePerTiBUSD = math.NaN() },
		},
		{
			name:   "negative debounce ms",
			mutate: func(o *bqconfig.LSPOptions) { o.CostDryRunDebounceMs = -5 },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := bqconfig.DefaultLSPOptions()
			tc.mutate(opts)
			err := opts.Validate()
			require.ErrorIs(t, err, bqconfig.ErrInvalidLSPOption)
		})
	}
}
