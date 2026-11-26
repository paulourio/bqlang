package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/catalog"
	"github.com/paulourio/bqlang/internal/analysis"
)

func setupCheckWorkspace(t *testing.T) string {
	t.Helper()

	ws := t.TempDir()
	schemaDir := filepath.Join(ws, "schemas")
	require.NoError(t, os.MkdirAll(schemaDir, 0o750))

	ordersSchema := `{
		"tableReference": {"projectId": "corp", "datasetId": "sales", "tableId": "orders"},
		"type": "TABLE",
		"timePartitioning": {"type": "DAY", "field": "order_dt"},
		"clustering": {"fields": ["customer_id", "region"]},
		"schema": {
			"fields": [
				{"name": "order_dt", "type": "DATE", "mode": "REQUIRED"},
				{"name": "customer_id", "type": "STRING", "mode": "REQUIRED"},
				{"name": "region", "type": "STRING", "mode": "NULLABLE"},
				{"name": "total_amt", "type": "NUMERIC", "mode": "NULLABLE"}
			]
		}
	}`
	require.NoError(t, os.WriteFile(filepath.Join(schemaDir, "corp.sales.orders.json"), []byte(ordersSchema), 0o600))

	cfg := `[lsp]
default_project = "corp"
default_dataset = "sales"
schema_dirs = ["./schemas"]
bigquery_api_enabled = false
`
	require.NoError(t, os.WriteFile(filepath.Join(ws, ".bqfmt.toml"), []byte(cfg), 0o600))

	return ws
}

func TestRunCheckTextJSONDirectoryAndStdin(t *testing.T) {
	t.Parallel()

	ws := setupCheckWorkspace(t)

	badPath := filepath.Join(ws, "queries", "bad.sql")
	okPath := filepath.Join(ws, "queries", "ok.sql")
	hiddenPath := filepath.Join(ws, "queries", ".hidden", "broken.sql")
	nodeModPath := filepath.Join(ws, "queries", "node_modules", "broken.sql")

	for _, p := range []string{badPath, okPath, hiddenPath, nodeModPath} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
	}

	require.NoError(t, os.WriteFile(badPath, []byte("SELECT total_amt FROM orders\n"), 0o600))
	require.NoError(t, os.WriteFile(okPath, []byte("SELECT total_amt FROM orders WHERE order_dt = DATE '2026-03-01' AND customer_id = 'c1'\n"), 0o600))
	require.NoError(t, os.WriteFile(hiddenPath, []byte("SELECT ((( FROM\n"), 0o600))
	require.NoError(t, os.WriteFile(nodeModPath, []byte("SELECT ((( FROM\n"), 0o600))

	// 1. Clean file exits 0.
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{"check", okPath}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 0, code, stderr.String())
	assert.Empty(t, stdout.String())

	// 2. Directory walk finds bad.sql, skips .hidden and node_modules, exits 1 on warnings.
	stdout.Reset()
	stderr.Reset()
	code = run(t.Context(), []string{"lint", filepath.Join(ws, "queries")}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 1, code, stderr.String())
	assert.Contains(t, stdout.String(), "warning[missing_partition_filter]")
	assert.Contains(t, stdout.String(), "warning[clustering_filter]")
	assert.NotContains(t, stdout.String(), "broken.sql")

	// 3. -fail-on error prints warnings but exits 0 when no error-level diagnostics exist.
	stdout.Reset()
	stderr.Reset()
	code = run(t.Context(), []string{"check", "-fail-on", "error", badPath}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 0, code, stderr.String())
	assert.Contains(t, stdout.String(), "warning[missing_partition_filter]")

	// 4. -format json produces structured array.
	stdout.Reset()
	stderr.Reset()
	code = run(t.Context(), []string{"check", "-format", "json", badPath}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 1, code, stderr.String())
	var findings []checkFinding
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &findings))
	require.Len(t, findings, 2)
	assert.Equal(t, badPath, findings[0].File)
	assert.Equal(t, 1, findings[0].Line)

	// 5. Stdin with -stdin-filename discovers .bqfmt.toml and checks JOIN usage.
	stdout.Reset()
	stderr.Reset()
	stdinSQL := "SELECT total_amt FROM orders WHERE order_dt = DATE '2026-03-01' AND region = 'EU'\n"
	code = run(
		t.Context(),
		[]string{"check", "-stdin-filename", filepath.Join(ws, "stdin.sql")},
		strings.NewReader(stdinSQL),
		&stdout,
		&stderr,
	)
	assert.Equal(t, 1, code, stderr.String())
	assert.Contains(t, stdout.String(), "warning[clustering_filter]")
	assert.Contains(t, stdout.String(), "without leading cluster column customer_id")
}

func TestRunCheckCLIOverridesAndBigQueryAPIWait(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"tableReference": {"projectId": "live-proj", "datasetId": "warehouse", "tableId": "sessions"},
			"type": "TABLE",
			"timePartitioning": {"type": "DAY", "field": "session_dt"},
			"clustering": {"fields": ["user_id"]},
			"schema": {
				"fields": [
					{"name": "session_dt", "type": "DATE", "mode": "REQUIRED"},
					{"name": "user_id", "type": "INT64", "mode": "REQUIRED"}
				]
			}
		}`))
	}))
	t.Cleanup(srv.Close)

	rest := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithTokenProvider(func(_ context.Context) (string, error) { return "tok", nil }),
	)

	ws := t.TempDir()
	cfgPath := filepath.Join(ws, ".bqfmt.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("[lsp]\ncache_dir = \""+filepath.ToSlash(filepath.Join(ws, "cache"))+"\"\n"), 0o600))

	sqlPath := filepath.Join(ws, "q.sql")
	require.NoError(t, os.WriteFile(sqlPath, []byte("SELECT user_id FROM sessions\n"), 0o600))

	var stdout, stderr bytes.Buffer
	code := runCheck(
		t.Context(),
		[]string{"-config", cfgPath, "-project", "live-proj", "-dataset", "warehouse", "-bigquery-api", sqlPath},
		strings.NewReader(""),
		&stdout,
		&stderr,
		catalog.WithRestClient(rest),
	)
	assert.Equal(t, 1, code, stderr.String())
	assert.Contains(t, stdout.String(), analysis.DiagCodeMissingPartitionFilter)
	assert.Contains(t, stdout.String(), analysis.DiagCodeClusteringFilter)
}

func TestRunCheckUsageAndMissingFileReturn2(t *testing.T) {
	t.Parallel()

	ws := t.TempDir()
	sqlFile := filepath.Join(ws, "q.sql")
	require.NoError(t, os.WriteFile(sqlFile, []byte("SELECT 1\n"), 0o600))

	var helpOut, helpErr bytes.Buffer
	assert.Equal(t, 0, run(t.Context(), []string{"check", "-h"}, strings.NewReader(""), &helpOut, &helpErr))

	for _, args := range [][]string{
		{"check", "-format", "xml", sqlFile},
		{"check", "-fail-on", "fatal", sqlFile},
		{"check", "-stdin-filename", "a.sql", sqlFile},
		{"check", "-timeout", "0s", sqlFile},
		{"check", "-schema-dir", "   ", sqlFile},
		{"check", "-config", filepath.Join(ws, "missing.toml"), sqlFile},
		{"check", filepath.Join(ws, "missing.sql")},
	} {
		var stdout, stderr bytes.Buffer
		code := run(t.Context(), args, strings.NewReader(""), &stdout, &stderr)
		assert.Equal(t, 2, code, "args=%v stdout=%s", args, stdout.String())
		assert.NotEmpty(t, stderr.String(), "args=%v", args)
	}
}
