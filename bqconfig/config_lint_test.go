package bqconfig_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/bqconfig"
)

func TestLintSeverityDefaultsValidationAndMerge(t *testing.T) {
	t.Parallel()

	def := bqconfig.DefaultLSPOptions()
	assert.Equal(t, bqconfig.SeverityWarning, def.RequirePartitionFilterSeverity)
	assert.Equal(t, bqconfig.SeverityWarning, def.MissingPartitionFilterSeverity)
	assert.Equal(t, bqconfig.SeverityWarning, def.ClusteringFilterSeverity)
	require.NoError(t, def.Validate())

	badMissing := bqconfig.DefaultLSPOptions()
	badMissing.MissingPartitionFilterSeverity = "fatal"
	require.ErrorIs(t, badMissing.Validate(), bqconfig.ErrInvalidLSPOption)

	badCluster := bqconfig.DefaultLSPOptions()
	badCluster.ClusteringFilterSeverity = "loud"
	require.ErrorIs(t, badCluster.Validate(), bqconfig.ErrInvalidLSPOption)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".bqfmt.toml")
	tomlText := `
[lsp]
missing_partition_filter_severity = "error"
clustering_filter_severity = "info"
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(tomlText), 0o600))

	loader, err := bqconfig.NewLoader("")
	require.NoError(t, err)

	loaded, err := loader.ConfigFor(filepath.Join(dir, "query.sql"), false)
	require.NoError(t, err)
	assert.Equal(t, bqconfig.SeverityError, loaded.LSP.MissingPartitionFilterSeverity)
	assert.Equal(t, bqconfig.SeverityInfo, loaded.LSP.ClusteringFilterSeverity)
	assert.True(t, loaded.LSPDefined.MissingPartitionFilterSeverity)
	assert.True(t, loaded.LSPDefined.ClusteringFilterSeverity)

	// Client sets clustering_filter_severity = "off" and require_partition_filter_severity = "hint";
	// file explicitly sets missing_partition_filter_severity = "error" and clustering_filter_severity = "info".
	clientOpts := bqconfig.DefaultLSPOptions()
	clientOpts.RequirePartitionFilterSeverity = bqconfig.SeverityHint
	clientOpts.MissingPartitionFilterSeverity = bqconfig.SeverityOff
	clientOpts.ClusteringFilterSeverity = bqconfig.SeverityOff
	clientDef := bqconfig.LSPDefinedFields{
		RequirePartitionFilterSeverity: true,
		MissingPartitionFilterSeverity: true,
		ClusteringFilterSeverity:       true,
	}

	merged := bqconfig.MergeLSPOptions(loaded.LSP, loaded.LSPDefined, clientOpts, clientDef)
	assert.Equal(t, bqconfig.SeverityHint, merged.RequirePartitionFilterSeverity)
	assert.Equal(t, bqconfig.SeverityError, merged.MissingPartitionFilterSeverity)
	assert.Equal(t, bqconfig.SeverityInfo, merged.ClusteringFilterSeverity)
}
