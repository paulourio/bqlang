package lsp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/internal/analysis"
	"github.com/paulourio/bqlang/lsp"
)

func TestLSPLintDiagnosticsAndSettingsIntegration(t *testing.T) {
	t.Parallel()

	wsDir := t.TempDir()
	schemaDir := filepath.Join(wsDir, "schemas")
	require.NoError(t, os.MkdirAll(schemaDir, 0o750))

	tableJSON := `{
		"tableReference": {"projectId": "my-proj", "datasetId": "analytics", "tableId": "orders"},
		"type": "TABLE",
		"timePartitioning": {"type": "DAY", "field": "order_dt"},
		"clustering": {"fields": ["customer_id", "channel"]},
		"schema": {
			"fields": [
				{"name": "order_dt", "type": "DATE", "mode": "REQUIRED"},
				{"name": "customer_id", "type": "STRING", "mode": "REQUIRED"},
				{"name": "channel", "type": "STRING", "mode": "NULLABLE"},
				{"name": "amount", "type": "NUMERIC", "mode": "NULLABLE"}
			]
		}
	}`
	require.NoError(t, os.WriteFile(filepath.Join(schemaDir, "my-proj.analytics.orders.json"), []byte(tableJSON), 0o600))

	c := startTestServer(t, lsp.ServerOptions{})
	initOpts, err := json.Marshal(map[string]any{
		"defaultProject":                 "my-proj",
		"defaultDataset":                 "analytics",
		"schemaDirs":                     []string{schemaDir},
		"bigqueryApiEnabled":             false,
		"missingPartitionFilterSeverity": "error",
		"clusteringFilterSeverity":       "info",
	})
	require.NoError(t, err)

	initResp := c.call("initialize", lsp.InitializeParams{
		RootURI:               costFileURI(wsDir),
		InitializationOptions: initOpts,
	})
	require.Nil(t, initResp.Error)
	c.notify("initialized", map[string]any{})

	uri := costFileURI(filepath.Join(wsDir, "query.sql"))
	cur := c.diagCount(uri)
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{
			URI:        uri,
			LanguageID: "bqlang",
			Version:    1,
			Text:       "SELECT amount FROM orders",
		},
	})

	d1 := c.waitDiagnostics(uri, cur)
	require.Len(t, d1.Diagnostics, 2)
	byCode := make(map[string]lsp.Diagnostic)
	for _, d := range d1.Diagnostics {
		byCode[d.Code] = d
	}
	assert.Equal(t, lsp.SeverityError, byCode[analysis.DiagCodeMissingPartitionFilter].Severity)
	assert.Equal(t, lsp.SeverityInformation, byCode[analysis.DiagCodeClusteringFilter].Severity)

	// Filtering on order_dt and joining/filtering on customer_id clears both warnings.
	cur = c.diagCount(uri)
	c.notify("textDocument/didChange", lsp.DidChangeTextDocumentParams{
		TextDocument: lsp.VersionedTextDocumentIdentifier{
			URI:     uri,
			Version: 2,
		},
		ContentChanges: []lsp.TextDocumentContentChangeEvent{{
			Text: "SELECT amount FROM orders WHERE order_dt = DATE '2026-03-01' AND customer_id = 'c1'",
		}},
	})
	d2 := c.waitDiagnostics(uri, cur)
	assert.Empty(t, d2.Diagnostics)

	// Filtering on channel (C2) without customer_id (C1) emits clustering_filter at info severity.
	cur = c.diagCount(uri)
	c.notify("textDocument/didChange", lsp.DidChangeTextDocumentParams{
		TextDocument: lsp.VersionedTextDocumentIdentifier{
			URI:     uri,
			Version: 3,
		},
		ContentChanges: []lsp.TextDocumentContentChangeEvent{{
			Text: "SELECT amount FROM orders WHERE order_dt = DATE '2026-03-01' AND channel = 'web'",
		}},
	})
	d3 := c.waitDiagnostics(uri, cur)
	require.Len(t, d3.Diagnostics, 1)
	assert.Equal(t, analysis.DiagCodeClusteringFilter, d3.Diagnostics[0].Code)
	assert.Equal(t, lsp.SeverityInformation, d3.Diagnostics[0].Severity)
	assert.Contains(t, d3.Diagnostics[0].Message, "without leading cluster column customer_id")

	// Live didChangeConfiguration turning clusteringFilterSeverity = "off" clears it.
	offSettings, err := json.Marshal(map[string]any{
		"bqpls": map[string]any{
			"defaultProject":                 "my-proj",
			"defaultDataset":                 "analytics",
			"schemaDirs":                     []string{schemaDir},
			"bigqueryApiEnabled":             false,
			"missingPartitionFilterSeverity": "error",
			"clusteringFilterSeverity":       "off",
		},
	})
	require.NoError(t, err)

	cur = c.diagCount(uri)
	c.notify("workspace/didChangeConfiguration", lsp.DidChangeConfigurationParams{
		Settings: offSettings,
	})
	d4 := c.waitDiagnostics(uri, cur)
	assert.Empty(t, d4.Diagnostics)
}
