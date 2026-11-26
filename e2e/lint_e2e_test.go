package e2e_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_LintMissingPartitionAndClusteringLSPAndCLI(t *testing.T) {
	t.Parallel()

	bqpls := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	// Create an optional-partitioned + multi-column clustered table and a join dimension table.
	ordersSpec := schemaTableSpec{
		Kind: "bigquery#table",
		ID:   "my-proj:analytics.orders",
		TableReference: schemaTableRef{
			ProjectID: "my-proj",
			DatasetID: "analytics",
			TableID:   "orders",
		},
		Type: "TABLE",
		TimePartitioning: &schemaTimePartitioning{
			Type:  "DAY",
			Field: "order_dt",
		},
		Clustering: &schemaClustering{
			Fields: []string{"tenant_id", "status"},
		},
		Schema: schemaTableSchema{
			Fields: []schemaFieldSpec{
				{Name: "order_dt", Type: "DATE", Mode: "REQUIRED"},
				{Name: "tenant_id", Type: "STRING", Mode: "REQUIRED"},
				{Name: "status", Type: "STRING", Mode: "NULLABLE"},
				{Name: "amount", Type: "NUMERIC", Mode: "NULLABLE"},
			},
		},
	}
	tenantsSpec := schemaTableSpec{
		Kind: "bigquery#table",
		ID:   "my-proj:analytics.tenants",
		TableReference: schemaTableRef{
			ProjectID: "my-proj",
			DatasetID: "analytics",
			TableID:   "tenants",
		},
		Type: "TABLE",
		Schema: schemaTableSchema{
			Fields: []schemaFieldSpec{
				{Name: "tenant_id", Type: "STRING", Mode: "REQUIRED"},
				{Name: "tier", Type: "STRING", Mode: "NULLABLE"},
			},
		},
	}

	writeSchemaTableJSON(t, filepath.Join(ws, "schemas", "my-proj.analytics.orders.json"), ordersSpec)
	writeSchemaTableJSON(t, filepath.Join(ws, "schemas", "my-proj.analytics.tenants.json"), tenantsSpec)
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), strings.Join([]string{
		"[lsp]",
		"default_project = \"my-proj\"",
		"default_dataset = \"analytics\"",
		"schema_dirs = [\"./schemas\"]",
		"bigquery_api_enabled = false",
		"",
	}, "\n"))

	badFile := filepath.Join(ws, "queries", "unfiltered.sql")
	joinOKFile := filepath.Join(ws, "queries", "join_ok.sql")
	writeFile(t, badFile, "SELECT amount FROM orders\n")
	writeFile(
		t,
		joinOKFile,
		"SELECT o.amount FROM tenants t JOIN orders o ON t.tenant_id = o.tenant_id WHERE o.order_dt = DATE '2026-03-01'\n",
	)

	// 1. CLI check on clean file returns exit 0.
	resOK := runCLI(t, bqpls, ws, env.env, "", "check", joinOKFile)
	assert.Equal(t, 0, resOK.exitCode, resOK.stderr)
	assert.Empty(t, resOK.stdout)

	// 2. CLI check on directory finds unfiltered.sql and returns exit 1 with both lint codes.
	resText := runCLI(t, bqpls, ws, env.env, "", "check", filepath.Join(ws, "queries"))
	assert.Equal(t, 1, resText.exitCode, resText.stderr)
	assert.Contains(t, resText.stdout, "warning[missing_partition_filter]")
	assert.Contains(t, resText.stdout, "warning[clustering_filter]")

	// 3. CLI lint -format json returns structured JSON diagnostics.
	resJSON := runCLI(t, bqpls, ws, env.env, "", "lint", "-format", "json", badFile)
	assert.Equal(t, 1, resJSON.exitCode, resJSON.stderr)
	var parsed []map[string]any
	require.NoError(t, json.Unmarshal([]byte(resJSON.stdout), &parsed))
	require.Len(t, parsed, 2)

	// 4. LSP server integration emits both warnings on didOpen and clears them when fixed.
	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, badFile)
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", "SELECT amount FROM orders", 1)

	cur, ev1 := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	require.Len(t, ev1.Diagnostics, 2)

	// Fix partition filter and use leading cluster key in JOIN -> zero diagnostics.
	client.didChange(t, uri, 2, lspContentChange{
		Text: "SELECT o.amount FROM tenants t JOIN orders o ON t.tenant_id = o.tenant_id WHERE o.order_dt = DATE '2026-03-01'",
	})
	_, ev2 := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev2.Diagnostics)
}
