package bqconfig_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/internal/bqconfig"
)

func TestProjectCacheTTLParsingValidationAndAdditiveMerge(t *testing.T) {
	t.Parallel()

	t.Run("default TTL is 24h and per-project entries only extend base TTL", func(t *testing.T) {
		t.Parallel()

		opts := bqconfig.DefaultLSPOptions()
		assert.Equal(t, 86400, opts.CacheTTLSeconds)
		assert.Equal(t, 24*time.Hour, opts.EffectiveCacheTTL(""))
		assert.Equal(t, 24*time.Hour, opts.EffectiveCacheTTL("any-proj"))

		opts.ProjectCacheTTLSeconds = map[string]int{
			"BigQuery-Public-Data": 604800, // 7d (> 24h -> extends)
			"shorter-proj":         3600,   // 1h (< 24h -> floored at 24h)
		}
		require.NoError(t, opts.Validate())

		assert.Equal(t, 7*24*time.Hour, opts.EffectiveCacheTTL("bigquery-public-data"))
		assert.Equal(t, 7*24*time.Hour, opts.EffectiveCacheTTL("BIGQUERY-PUBLIC-DATA"))
		assert.Equal(t, 24*time.Hour, opts.EffectiveCacheTTL("shorter-proj"))
		assert.Equal(t, 24*time.Hour, opts.EffectiveCacheTTL("unlisted-proj"))
	})

	t.Run("validation rejects empty project keys and negative TTL values", func(t *testing.T) {
		t.Parallel()

		optsEmptyKey := bqconfig.DefaultLSPOptions()
		optsEmptyKey.ProjectCacheTTLSeconds = map[string]int{"   ": 3600}
		err := optsEmptyKey.Validate()
		require.ErrorIs(t, err, bqconfig.ErrInvalidLSPOption)

		optsNegVal := bqconfig.DefaultLSPOptions()
		optsNegVal.ProjectCacheTTLSeconds = map[string]int{"my-proj": -1}
		err = optsNegVal.Validate()
		require.ErrorIs(t, err, bqconfig.ErrInvalidLSPOption)
	})

	t.Run("parses both subtable and inline table TOML syntax", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		subtableCfg := filepath.Join(dir, "subtable.toml")
		require.NoError(t, os.WriteFile(subtableCfg, []byte(`
[lsp]
cache_ttl_seconds = 43200

[lsp.project_cache_ttl_seconds]
"bigquery-public-data" = 2592000
"Corp-Warehouse" = 604800
`), 0o600))

		loader, err := bqconfig.NewLoader(subtableCfg)
		require.NoError(t, err)

		loaded, err := loader.ConfigFor(filepath.Join(dir, "q.sql"), false)
		require.NoError(t, err)
		assert.True(t, loaded.LSPDefined.CacheTTLSeconds)
		assert.True(t, loaded.LSPDefined.ProjectCacheTTLSeconds)
		assert.Equal(t, 43200, loaded.LSP.CacheTTLSeconds)
		assert.Equal(t, map[string]int{
			"bigquery-public-data": 2592000,
			"corp-warehouse":       604800,
		}, loaded.LSP.ProjectCacheTTLSeconds)

		inlineCfg := filepath.Join(dir, "inline.toml")
		require.NoError(t, os.WriteFile(inlineCfg, []byte(`
[lsp.project_cache_ttl_seconds]
"example.com:analytics-prod" = 172800
`), 0o600))

		inlineLoader, err := bqconfig.NewLoader(inlineCfg)
		require.NoError(t, err)

		inlineLoaded, err := inlineLoader.ConfigFor(filepath.Join(dir, "q.sql"), false)
		require.NoError(t, err)
		assert.True(t, inlineLoaded.LSPDefined.ProjectCacheTTLSeconds)
		assert.False(t, inlineLoaded.LSPDefined.CacheTTLSeconds)
		assert.Equal(t, 86400, inlineLoaded.LSP.CacheTTLSeconds)
		assert.Equal(t, 48*time.Hour, inlineLoaded.LSP.EffectiveCacheTTL("example.com:analytics-prod"))
	})

	t.Run("MergeLSPOptions extends client project TTLs with file project TTLs instead of overriding", func(t *testing.T) {
		t.Parallel()

		clientOpts := bqconfig.DefaultLSPOptions()
		clientOpts.CacheTTLSeconds = 86400
		clientOpts.ProjectCacheTTLSeconds = map[string]int{
			"bigquery-public-data": 2592000, // 30d in VS Code user settings
			"shared-longer-in-ext": 604800,  // 7d in VS Code
			"shared-longer-in-cfg": 172800,  // 2d in VS Code
		}
		clientDef := bqconfig.LSPDefinedFields{
			CacheTTLSeconds:        true,
			ProjectCacheTTLSeconds: true,
		}

		dir := t.TempDir()
		cfgPath := filepath.Join(dir, bqconfig.ConfigFileName)
		require.NoError(t, os.WriteFile(cfgPath, []byte(`
[lsp.project_cache_ttl_seconds]
"repo-curated-mart" = 432000
"shared-longer-in-ext" = 172800
"shared-longer-in-cfg" = 1209600
`), 0o600))

		loader, err := bqconfig.NewLoader(cfgPath)
		require.NoError(t, err)

		loaded, err := loader.ConfigFor(filepath.Join(dir, "query.sql"), false)
		require.NoError(t, err)

		merged := bqconfig.MergeLSPOptions(loaded.LSP, loaded.LSPDefined, clientOpts, clientDef)
		assert.Equal(t, map[string]int{
			"bigquery-public-data": 2592000, // preserved from VS Code
			"repo-curated-mart":    432000,  // added from .bqfmt.toml
			"shared-longer-in-ext": 604800,  // max(604800, 172800)
			"shared-longer-in-cfg": 1209600, // max(172800, 1209600)
		}, merged.ProjectCacheTTLSeconds)
	})
}
