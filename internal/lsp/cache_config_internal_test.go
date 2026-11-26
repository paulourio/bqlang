package lsp

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/internal/bqconfig"
)

func TestClientAndTOMLProjectCacheTTLAdditiveResolution(t *testing.T) {
	t.Parallel()

	t.Run("parseClientLSPOptions validates and normalizes camelCase and snake_case maps", func(t *testing.T) {
		t.Parallel()

		raw := json.RawMessage(`{
			"cacheTtlSeconds": 43200,
			"projectCacheTtlSeconds": {
				"BigQuery-Public-Data": 2592000,
				"shared-proj": 172800
			},
			"project_cache_ttl_seconds": {
				"nvim-proj": 604800,
				"shared-proj": 86400
			}
		}`)

		opts, def, err := parseClientLSPOptions(raw, "")
		require.NoError(t, err)
		assert.True(t, def.CacheTTLSeconds)
		assert.True(t, def.ProjectCacheTTLSeconds)
		assert.Equal(t, 43200, opts.CacheTTLSeconds)
		assert.Equal(t, map[string]int{
			"bigquery-public-data": 2592000,
			"nvim-proj":            604800,
			"shared-proj":          172800,
		}, opts.ProjectCacheTTLSeconds)

		_, _, errNeg := parseClientLSPOptions(json.RawMessage(`{"projectCacheTtlSeconds":{"bad":-5}}`), "")
		require.ErrorIs(t, errNeg, bqconfig.ErrInvalidLSPOption)

		_, _, errEmpty := parseClientLSPOptions(json.RawMessage(`{"projectCacheTtlSeconds":{"  ":100}}`), "")
		require.ErrorIs(t, errEmpty, bqconfig.ErrInvalidLSPOption)
	})

	t.Run("Server resolveDocConfig extends VS Code project TTLs with .bqfmt.toml project TTLs", func(t *testing.T) {
		t.Parallel()

		wsDir := t.TempDir()
		cfgPath := filepath.Join(wsDir, bqconfig.ConfigFileName)
		require.NoError(t, os.WriteFile(cfgPath, []byte(`
[lsp]
default_project = "repo-proj"

[lsp.project_cache_ttl_seconds]
"repo-proj" = 604800
"shared-proj" = 86400
`), 0o600))

		srv, err := NewServer(ServerOptions{})
		require.NoError(t, err)
		defer func() { _ = srv.Close() }()

		initParams, marshalErr := json.Marshal(map[string]any{
			"rootUri": (&url.URL{Scheme: "file", Path: filepath.ToSlash(wsDir)}).String(),
			"initializationOptions": map[string]any{
				"cacheTtlSeconds": 86400,
				"projectCacheTtlSeconds": map[string]int{
					"bigquery-public-data": 2592000,
					"shared-proj":          259200,
				},
			},
		})
		require.NoError(t, marshalErr)

		_, initErr := srv.handleInitialize(t.Context(), initParams)
		require.Nil(t, initErr)

		docURI := (&url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(wsDir, "query.sql"))}).String()
		loaded, docCfg := srv.resolveDocConfig(t.Context(), docURI)
		require.NotNil(t, loaded)
		require.NotNil(t, docCfg)
		require.NotNil(t, docCfg.Store)

		assert.Equal(t, 30*24*time.Hour, docCfg.Store.EffectiveCacheTTL("bigquery-public-data"), "VS Code project TTL preserved")
		assert.Equal(t, 7*24*time.Hour, docCfg.Store.EffectiveCacheTTL("repo-proj"), ".bqfmt.toml project TTL applied")
		assert.Equal(t, 3*24*time.Hour, docCfg.Store.EffectiveCacheTTL("shared-proj"), "max TTL wins across VS Code and .bqfmt.toml")
		assert.Equal(t, 24*time.Hour, docCfg.Store.EffectiveCacheTTL("unlisted-proj"), "default 24h TTL preserved for unlisted projects")

		// Deterministic storeCacheKey differentiates distinct project TTL maps while matching identical maps regardless of input order.
		keyA := storeCacheKey(&bqconfig.LSPOptions{
			CacheTTLSeconds:        86400,
			ProjectCacheTTLSeconds: map[string]int{"b": 200, "a": 100},
		})
		keyB := storeCacheKey(&bqconfig.LSPOptions{
			CacheTTLSeconds:        86400,
			ProjectCacheTTLSeconds: map[string]int{"A": 100, "B": 200},
		})
		keyC := storeCacheKey(&bqconfig.LSPOptions{
			CacheTTLSeconds:        86400,
			ProjectCacheTTLSeconds: map[string]int{"a": 100, "b": 300},
		})
		assert.Equal(t, keyA, keyB)
		assert.NotEqual(t, keyA, keyC)
	})
}
