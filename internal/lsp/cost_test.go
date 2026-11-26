package lsp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/internal/analysis"
	"github.com/paulourio/bqlang/internal/catalog"
	"github.com/paulourio/bqlang/internal/lsp"
	"github.com/paulourio/bqlang/internal/syntax"
)

func costFileURI(p string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(p)}).String()
}

func (c *testClient) waitDiagMatching(uri string, pred func(lsp.PublishDiagnosticsParams) bool) lsp.PublishDiagnosticsParams {
	c.t.Helper()

	deadline := time.After(10 * time.Second)
	for {
		c.mu.Lock()
		list := c.diagnostics[uri]
		for _, item := range slices.Backward(list) {
			if pred(item) {
				c.mu.Unlock()
				return item
			}
		}
		c.mu.Unlock()

		select {
		case <-c.diagSig:
		case <-deadline:
			c.t.Fatalf("timed out waiting for diagnostics matching predicate for %s", uri)
			return lsp.PublishDiagnosticsParams{}
		}
	}
}

func (c *testClient) waitQueryCost(uri string, pred func(analysis.DocumentCostSummary) bool) analysis.DocumentCostSummary {
	c.t.Helper()

	deadline := time.After(10 * time.Second)
	for {
		c.mu.Lock()
		list := c.queryCosts[uri]
		for _, item := range slices.Backward(list) {
			if pred(item) {
				c.mu.Unlock()
				return item
			}
		}
		c.mu.Unlock()

		select {
		case <-c.costSig:
		case <-deadline:
			c.t.Fatalf("timed out waiting for bqpls/queryCost matching predicate for %s", uri)
			return analysis.DocumentCostSummary{}
		}
	}
}

func (c *testClient) waitServerRequest(method string) {
	c.t.Helper()

	deadline := time.After(10 * time.Second)
	for {
		c.mu.Lock()
		for _, req := range c.serverRequests {
			if req.Method == method {
				c.mu.Unlock()
				return
			}
		}
		c.mu.Unlock()

		select {
		case <-c.reqSig:
		case <-deadline:
			c.t.Fatalf("timed out waiting for server request %s", method)
			return
		}
	}
}

func TestLSPCostDryRunCodeLensStatusBarAndDiagnostics(t *testing.T) {
	t.Parallel()

	var dryRunCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			n := dryRunCalls.Add(1)
			bytesVal := int64(2) * analysis.BytesPerTiB
			if n >= 2 {
				bytesVal = 512 << 20
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statistics": map[string]any{
					"totalBytesProcessed": bytesVal,
					"query": map[string]any{
						"totalBytesProcessed":         bytesVal,
						"totalBytesBilled":            bytesVal,
						"totalBytesProcessedAccuracy": "PRECISE",
						"statementType":               "SELECT",
					},
				},
			})

			return
		}

		// Table metadata GET for proj.ds.events (1 GiB).
		_, _ = w.Write([]byte(`{
			"tableReference": {"projectId": "proj", "datasetId": "ds", "tableId": "events"},
			"type": "TABLE",
			"numRows": "1000",
			"numBytes": "1073741824",
			"schema": {"fields": [{"name": "id", "type": "INT64", "mode": "REQUIRED"}]}
		}`))
	}))
	t.Cleanup(srv.Close)

	rest := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithTokenProvider(func(_ context.Context) (string, error) { return "tok", nil }),
	)

	c := startTestServer(t, lsp.ServerOptions{
		StoreOptions: []catalog.StoreOption{catalog.WithRestClient(rest)},
	})

	initResp := c.call("initialize", lsp.InitializeParams{
		RootURI: costFileURI(t.TempDir()),
		InitializationOptions: json.RawMessage(`{
			"defaultProject": "proj",
			"defaultDataset": "ds",
			"bigqueryApiEnabled": true,
			"cacheDir": "` + t.TempDir() + `",
			"costWarningThresholdUsd": 10.0,
			"costDiagnosticSeverity": "warning",
			"costDryRunDebounceMs": 50
		}`),
		Capabilities: lsp.ClientCapabilities{
			Workspace: &lsp.WorkspaceClientCapabilities{
				SemanticTokens: &lsp.SemanticTokensWorkspaceClientCapabilities{RefreshSupport: true},
				CodeLens:       &lsp.CodeLensWorkspaceClientCapabilities{RefreshSupport: true},
			},
		},
	})
	require.Nil(t, initResp.Error)

	var initRes lsp.InitializeResult
	require.NoError(t, json.Unmarshal(initResp.Result, &initRes))
	require.NotNil(t, initRes.Capabilities.CodeLensProvider)

	c.notify("initialized", map[string]any{})

	uri := costFileURI(t.TempDir() + "/query.sql")
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{
			URI:        uri,
			LanguageID: "bqlang",
			Version:    1,
			Text:       "SELECT id FROM proj.ds.events",
		},
	})

	// Wait for dry-run cost notification (2 TiB => $12.50 > $10.00 threshold).
	dryCost := c.waitQueryCost(uri, func(s analysis.DocumentCostSummary) bool {
		return s.Source == analysis.CostSourceDryRun && s.TotalBytesProcessed == 2*analysis.BytesPerTiB
	})
	assert.True(t, dryCost.Enabled)
	assert.True(t, dryCost.ExceedsThreshold)
	assert.Contains(t, dryCost.Summary, "2.00 TiB")
	assert.Contains(t, dryCost.Summary, "$12.50")

	// Server must notify client to refresh CodeLenses.
	c.waitServerRequest("workspace/codeLens/refresh")

	// Request textDocument/codeLens and verify dry-run label.
	lensResp := c.call("textDocument/codeLens", lsp.CodeLensParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
	})
	require.Nil(t, lensResp.Error)
	var lenses []analysis.CodeLens
	require.NoError(t, json.Unmarshal(lensResp.Result, &lenses))
	require.Len(t, lenses, 1)
	require.NotNil(t, lenses[0].Command)
	assert.Contains(t, lenses[0].Command.Title, "2.00 TiB")
	assert.Equal(t, analysis.CommandRefreshCost, lenses[0].Command.Command)

	// Wait for estimated_cost diagnostic to be published.
	c.waitDiagMatching(uri, func(p lsp.PublishDiagnosticsParams) bool {
		for _, d := range p.Diagnostics {
			if d.Code == "estimated_cost" {
				return true
			}
		}

		return false
	})

	// Force refresh via bqpls/refreshCost -> triggers 2nd dry-run (512 MiB, below threshold).
	refreshResp := c.call("bqpls/refreshCost", lsp.RefreshCostParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
	})
	require.Nil(t, refreshResp.Error)

	updatedCost := c.waitQueryCost(uri, func(s analysis.DocumentCostSummary) bool {
		return s.Source == analysis.CostSourceDryRun && s.TotalBytesProcessed == 512<<20
	})
	assert.False(t, updatedCost.ExceedsThreshold)
	assert.GreaterOrEqual(t, dryRunCalls.Load(), int32(2))

	// Disable CodeLens and status bar via workspace/didChangeConfiguration.
	c.notify("workspace/didChangeConfiguration", lsp.DidChangeConfigurationParams{
		Settings: json.RawMessage(`{
			"defaultProject": "proj",
			"defaultDataset": "ds",
			"bigqueryApiEnabled": true,
			"costCodeLensEnabled": false,
			"costStatusBarEnabled": false
		}`),
	})

	disabledCost := c.waitQueryCost(uri, func(s analysis.DocumentCostSummary) bool {
		return !s.Enabled
	})
	assert.False(t, disabledCost.Enabled)

	lensResp2 := c.call("textDocument/codeLens", lsp.CodeLensParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
	})
	require.Nil(t, lensResp2.Error)
	var lenses2 []analysis.CodeLens
	require.NoError(t, json.Unmarshal(lensResp2.Result, &lenses2))
	assert.Empty(t, lenses2)

	// Closing document clears cost state cleanly.
	c.notify("textDocument/didClose", lsp.DidCloseTextDocumentParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
	})
}

func TestLSPCostDebouncedDidChangeAndImmediateDidSave(t *testing.T) {
	t.Parallel()

	var dryRunCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			dryRunCalls.Add(1)
			_, _ = w.Write([]byte(`{
				"statistics": {
					"totalBytesProcessed": "10485760",
					"query": {
						"totalBytesProcessed": "10485760",
						"totalBytesBilled": "10485760",
						"totalBytesProcessedAccuracy": "PRECISE"
					}
				}
			}`))

			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	rest := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithTokenProvider(func(_ context.Context) (string, error) { return "tok", nil }),
	)

	c := startTestServer(t, lsp.ServerOptions{
		StoreOptions: []catalog.StoreOption{catalog.WithRestClient(rest)},
	})

	initResp := c.call("initialize", lsp.InitializeParams{
		RootURI: costFileURI(t.TempDir()),
		InitializationOptions: json.RawMessage(`{
			"default_project": "proj",
			"bigquery_api_enabled": true,
			"cache_dir": "` + t.TempDir() + `",
			"cost_dry_run_debounce_ms": 5000
		}`),
	})
	require.Nil(t, initResp.Error)
	c.notify("initialized", map[string]any{})

	uri := costFileURI(t.TempDir() + "/debounced.sql")
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{
			URI:        uri,
			LanguageID: "bqlang",
			Version:    1,
			Text:       "SELECT 1",
		},
	})

	// Wait for initial open dry-run.
	c.waitQueryCost(uri, func(s analysis.DocumentCostSummary) bool {
		return s.Source == analysis.CostSourceDryRun
	})
	assert.Equal(t, int32(1), dryRunCalls.Load())

	// Edit with 5s debounce should NOT immediately hit dry-run, then didSave fires immediately.
	c.notify("textDocument/didChange", lsp.DidChangeTextDocumentParams{
		TextDocument: lsp.VersionedTextDocumentIdentifier{URI: uri, Version: 2},
		ContentChanges: []lsp.TextDocumentContentChangeEvent{
			{Text: "SELECT 2"},
		},
	})
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), dryRunCalls.Load(), "debounced didChange should not have fired yet")

	c.notify("textDocument/didSave", lsp.DidSaveTextDocumentParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
	})

	require.Eventually(t, func() bool {
		return dryRunCalls.Load() >= 2
	}, 3*time.Second, 20*time.Millisecond, "didSave should trigger immediate dry-run")
}

func TestLSPCostStatusBarReportsInvalidSQLAndDryRunRejection(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "Not found: Table proj:ds.missing"}}`))

			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	rest := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithTokenProvider(func(_ context.Context) (string, error) { return "tok", nil }),
	)

	c := startTestServer(t, lsp.ServerOptions{
		StoreOptions: []catalog.StoreOption{catalog.WithRestClient(rest)},
	})

	initResp := c.call("initialize", lsp.InitializeParams{
		RootURI: costFileURI(t.TempDir()),
		InitializationOptions: json.RawMessage(`{
			"defaultProject": "proj",
			"bigqueryApiEnabled": true,
			"cacheDir": "` + t.TempDir() + `"
		}`),
	})
	require.Nil(t, initResp.Error)
	c.notify("initialized", map[string]any{})

	// 1. Invalid SQL file immediately publishes error status for status bar.
	badURI := costFileURI(t.TempDir() + "/invalid.sql")
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{
			URI:        badURI,
			LanguageID: "bqlang",
			Version:    1,
			Text:       "SELECT FROM oops",
		},
	})

	badCost := c.waitQueryCost(badURI, func(s analysis.DocumentCostSummary) bool {
		return s.Status == analysis.CostStatusError && s.ErrorCount == 1
	})
	assert.Equal(t, "Invalid SQL (Ln 1)", badCost.Summary)
	assert.NotEmpty(t, badCost.ErrorMessage)

	// 2. Syntactically valid SQL rejected by BigQuery dry-run publishes dry-run error.
	missingURI := costFileURI(t.TempDir() + "/missing.sql")
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{
			URI:        missingURI,
			LanguageID: "bqlang",
			Version:    1,
			Text:       "SELECT id FROM proj.ds.missing",
		},
	})

	dryErrCost := c.waitQueryCost(missingURI, func(s analysis.DocumentCostSummary) bool {
		return s.Status == analysis.CostStatusError && s.Summary == "Dry run error"
	})
	assert.Contains(t, dryErrCost.ErrorMessage, "Not found: Table proj:ds.missing")
}

func TestLSPDryRunInfersViewPartitionColumnUpdatesDiagnosticsHoverAndSemanticTokens(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			var req struct {
				Configuration struct {
					Query struct {
						Query string `json:"query"`
					} `json:"query"`
				} `json:"configuration"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if !strings.Contains(strings.ToUpper(req.Configuration.Query.Query), "WHERE") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "Cannot query over table 'proj.raw.events_base' without a filter over column(s) 'event_date' that can be used for partition elimination"}}`))
				return
			}

			_, _ = w.Write([]byte(`{
				"statistics": {
					"totalBytesProcessed": "1048576",
					"query": {
						"totalBytesProcessed": "1048576",
						"totalBytesBilled": "1048576",
						"totalBytesProcessedAccuracy": "PRECISE",
						"statementType": "SELECT"
					}
				}
			}`))
			return
		}

		// GET view metadata for proj.ds.v_events (BigQuery views omit partitioning).
		_, _ = w.Write([]byte(`{
			"tableReference": {"projectId": "proj", "datasetId": "ds", "tableId": "v_events"},
			"type": "VIEW",
			"schema": {
				"fields": [
					{"name": "event_date", "type": "DATE", "mode": "NULLABLE"},
					{"name": "user_id", "type": "STRING", "mode": "NULLABLE"}
				]
			}
		}`))
	}))
	t.Cleanup(srv.Close)

	rest := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithTokenProvider(func(_ context.Context) (string, error) { return "tok", nil }),
	)

	c := startTestServer(t, lsp.ServerOptions{
		StoreOptions: []catalog.StoreOption{catalog.WithRestClient(rest)},
	})

	initResp := c.call("initialize", lsp.InitializeParams{
		RootURI: costFileURI(t.TempDir()),
		InitializationOptions: json.RawMessage(`{
			"defaultProject": "proj",
			"defaultDataset": "ds",
			"bigqueryApiEnabled": true,
			"cacheDir": "` + t.TempDir() + `",
			"costDryRunDebounceMs": 20
		}`),
		Capabilities: lsp.ClientCapabilities{
			Workspace: &lsp.WorkspaceClientCapabilities{
				SemanticTokens: &lsp.SemanticTokensWorkspaceClientCapabilities{RefreshSupport: true},
				CodeLens:       &lsp.CodeLensWorkspaceClientCapabilities{RefreshSupport: true},
			},
		},
	})
	require.Nil(t, initResp.Error)
	c.notify("initialized", map[string]any{})

	uri := costFileURI(t.TempDir() + "/view_query.sql")
	sqlUnfiltered := "SELECT event_date, user_id FROM proj.ds.v_events"
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{
			URI:        uri,
			LanguageID: "bqlang",
			Version:    1,
			Text:       sqlUnfiltered,
		},
	})

	// 1. Dry-run failure must infer event_date as partition column on v_events,
	// triggering a require_partition_filter diagnostic and semantic token refresh.
	diag := c.waitDiagMatching(uri, func(p lsp.PublishDiagnosticsParams) bool {
		for _, d := range p.Diagnostics {
			if d.Code == analysis.DiagCodeRequirePartitionFilter && strings.Contains(d.Message, "event_date") {
				return true
			}
		}

		return false
	})
	assert.NotEmpty(t, diag.Diagnostics)
	c.waitServerRequest("workspace/semanticTokens/refresh")

	// Hover on event_date must display the [PARTITION] badge.
	hoverResp := c.call("textDocument/hover", lsp.HoverParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
		Position:     syntax.Position{Line: 0, Character: 9},
	})
	require.Nil(t, hoverResp.Error)
	var hover analysis.Hover
	require.NoError(t, json.Unmarshal(hoverResp.Result, &hover))
	assert.Contains(t, hover.Contents.Value, "[PARTITION]")

	// Semantic tokens must attach ModPartitioned to event_date.
	semResp := c.call("textDocument/semanticTokens/full", lsp.SemanticTokensParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
	})
	require.Nil(t, semResp.Error)
	var semRes analysis.SemanticTokensResult
	require.NoError(t, json.Unmarshal(semResp.Result, &semRes))
	assert.True(t, hasSemanticTokenWithModifier(semRes.Data, 0, 7, uint32(analysis.ModPartitioned)), "expected ModPartitioned on event_date")

	// 2. Once the user adds WHERE event_date = '2026-01-01', dry-run succeeds,
	// require_partition_filter clears, and event_date REMAINS flagged as partitioned.
	sqlFiltered := "SELECT event_date, user_id FROM proj.ds.v_events WHERE event_date = '2026-01-01'"
	c.notify("textDocument/didChange", lsp.DidChangeTextDocumentParams{
		TextDocument: lsp.VersionedTextDocumentIdentifier{URI: uri, Version: 2},
		ContentChanges: []lsp.TextDocumentContentChangeEvent{
			{Text: sqlFiltered},
		},
	})

	c.waitDiagMatching(uri, func(p lsp.PublishDiagnosticsParams) bool {
		for _, d := range p.Diagnostics {
			if d.Code == analysis.DiagCodeRequirePartitionFilter {
				return false
			}
		}

		return p.Version != nil && *p.Version == 2
	})

	semResp2 := c.call("textDocument/semanticTokens/full", lsp.SemanticTokensParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
	})
	require.Nil(t, semResp2.Error)
	var semRes2 analysis.SemanticTokensResult
	require.NoError(t, json.Unmarshal(semResp2.Result, &semRes2))
	assert.True(t, hasSemanticTokenWithModifier(semRes2.Data, 0, 7, uint32(analysis.ModPartitioned)), "expected ModPartitioned to persist after adding filter")
}

func TestLSPViewProbeStage3AuthorizedViewMissingPartitionFilterHoverAndSemanticTokens(t *testing.T) {
	t.Parallel()

	const (
		baselineBytes int64 = 200 << 30 // 200 GiB (>= 100 GiB Stage 3 threshold)
		prunedBytes   int64 = 10 << 30  // 10 GiB when event_date is filtered
	)

	var baseGetDenied atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			var req struct {
				Configuration struct {
					Query struct {
						Query string `json:"query"`
					} `json:"query"`
				} `json:"configuration"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)

			q := req.Configuration.Query.Query
			bytesVal := baselineBytes
			if strings.Contains(strings.ToUpper(q), "WHERE ") && strings.Contains(strings.ToLower(q), "event_date") {
				bytesVal = prunedBytes
			}

			_ = json.NewEncoder(w).Encode(map[string]any{
				"statistics": map[string]any{
					"totalBytesProcessed": bytesVal,
					"query": map[string]any{
						"totalBytesProcessed":         bytesVal,
						"totalBytesBilled":            bytesVal,
						"totalBytesProcessedAccuracy": "PRECISE",
						"statementType":               "SELECT",
						"referencedTables": []map[string]string{
							{"projectId": "proj", "datasetId": "restricted", "tableId": "events_base"},
						},
					},
				},
			})
			return
		}

		if strings.HasSuffix(r.URL.Path, "/tables/events_base") {
			baseGetDenied.Add(1)
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error": {"code": 403, "message": "Access Denied: Table proj:restricted.events_base"}}`))
			return
		}

		// GET view metadata for proj.ds.v_authorized_events (views omit partitioning).
		_, _ = w.Write([]byte(`{
			"tableReference": {"projectId": "proj", "datasetId": "ds", "tableId": "v_authorized_events"},
			"type": "VIEW",
			"schema": {
				"fields": [
					{"name": "user_id", "type": "STRING", "mode": "NULLABLE"},
					{"name": "created_at", "type": "TIMESTAMP", "mode": "NULLABLE"},
					{"name": "event_date", "type": "DATE", "mode": "NULLABLE"},
					{"name": "updated_at", "type": "TIMESTAMP", "mode": "NULLABLE"}
				]
			}
		}`))
	}))
	t.Cleanup(srv.Close)

	rest := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithTokenProvider(func(_ context.Context) (string, error) { return "tok", nil }),
	)

	c := startTestServer(t, lsp.ServerOptions{
		StoreOptions: []catalog.StoreOption{catalog.WithRestClient(rest)},
	})

	initResp := c.call("initialize", lsp.InitializeParams{
		RootURI: costFileURI(t.TempDir()),
		InitializationOptions: json.RawMessage(`{
			"defaultProject": "proj",
			"defaultDataset": "ds",
			"bigqueryApiEnabled": true,
			"cacheDir": "` + t.TempDir() + `",
			"costDryRunDebounceMs": 20
		}`),
		Capabilities: lsp.ClientCapabilities{
			Workspace: &lsp.WorkspaceClientCapabilities{
				SemanticTokens: &lsp.SemanticTokensWorkspaceClientCapabilities{RefreshSupport: true},
				CodeLens:       &lsp.CodeLensWorkspaceClientCapabilities{RefreshSupport: true},
			},
		},
	})
	require.Nil(t, initResp.Error)
	c.notify("initialized", map[string]any{})

	uri := costFileURI(t.TempDir() + "/authorized_view.sql")
	sqlUnfiltered := "SELECT user_id, created_at, event_date, updated_at FROM proj.ds.v_authorized_events"
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{
			URI:        uri,
			LanguageID: "bqlang",
			Version:    1,
			Text:       sqlUnfiltered,
		},
	})

	// Stage 1 (200 GiB) -> Stage 2 (403 on restricted.events_base) -> Stage 3 binary search
	// discovers optional partition column event_date and publishes missing_partition_filter.
	diag := c.waitDiagMatching(uri, func(p lsp.PublishDiagnosticsParams) bool {
		for _, d := range p.Diagnostics {
			if d.Code == analysis.DiagCodeMissingPartitionFilter && strings.Contains(d.Message, "event_date") {
				return true
			}
		}

		return false
	})
	assert.NotEmpty(t, diag.Diagnostics)
	for _, d := range diag.Diagnostics {
		assert.NotEqual(t, analysis.DiagCodeRequirePartitionFilter, d.Code, "Stage 3 optional partition must not emit require_partition_filter")
	}
	assert.GreaterOrEqual(t, baseGetDenied.Load(), int32(1), "expected Stage 2 tables.get attempt on underlying base table")

	c.waitServerRequest("workspace/semanticTokens/refresh")

	// Hover on event_date (col 28..37) must display the [PARTITION] badge.
	hoverResp := c.call("textDocument/hover", lsp.HoverParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
		Position:     syntax.Position{Line: 0, Character: 30},
	})
	require.Nil(t, hoverResp.Error)
	var hover analysis.Hover
	require.NoError(t, json.Unmarshal(hoverResp.Result, &hover))
	assert.Contains(t, hover.Contents.Value, "[PARTITION]")

	// Semantic tokens must attach ModPartitioned only to event_date (col 28), not created_at (16) or updated_at (40).
	semResp := c.call("textDocument/semanticTokens/full", lsp.SemanticTokensParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
	})
	require.Nil(t, semResp.Error)
	var semRes analysis.SemanticTokensResult
	require.NoError(t, json.Unmarshal(semResp.Result, &semRes))
	assert.True(t, hasSemanticTokenWithModifier(semRes.Data, 0, 28, uint32(analysis.ModPartitioned)), "expected ModPartitioned on event_date")
	assert.False(t, hasSemanticTokenWithModifier(semRes.Data, 0, 28, uint32(analysis.ModClustered)), "event_date must not be marked ModClustered")
	assert.False(t, hasSemanticTokenWithModifier(semRes.Data, 0, 16, uint32(analysis.ModPartitioned)), "created_at must not be marked ModPartitioned")
	assert.False(t, hasSemanticTokenWithModifier(semRes.Data, 0, 40, uint32(analysis.ModPartitioned)), "updated_at must not be marked ModPartitioned")

	// Adding WHERE event_date = '2026-01-01' clears missing_partition_filter while ModPartitioned persists.
	sqlFiltered := sqlUnfiltered + "\nWHERE event_date = '2026-01-01'"
	c.notify("textDocument/didChange", lsp.DidChangeTextDocumentParams{
		TextDocument: lsp.VersionedTextDocumentIdentifier{URI: uri, Version: 2},
		ContentChanges: []lsp.TextDocumentContentChangeEvent{
			{Text: sqlFiltered},
		},
	})

	c.waitDiagMatching(uri, func(p lsp.PublishDiagnosticsParams) bool {
		for _, d := range p.Diagnostics {
			if d.Code == analysis.DiagCodeMissingPartitionFilter {
				return false
			}
		}

		return p.Version != nil && *p.Version == 2
	})

	semResp2 := c.call("textDocument/semanticTokens/full", lsp.SemanticTokensParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
	})
	require.Nil(t, semResp2.Error)
	var semRes2 analysis.SemanticTokensResult
	require.NoError(t, json.Unmarshal(semResp2.Result, &semRes2))
	assert.True(t, hasSemanticTokenWithModifier(semRes2.Data, 0, 28, uint32(analysis.ModPartitioned)), "expected ModPartitioned to persist after filtering")
	assert.True(t, hasSemanticTokenWithModifier(semRes2.Data, 1, 6, uint32(analysis.ModPartitioned)), "expected ModPartitioned on WHERE event_date")
}

func hasSemanticTokenWithModifier(data []uint32, wantLine, wantChar, wantMod uint32) bool {
	var line, char uint32
	for i := 0; i+4 < len(data); i += 5 {
		dl, dc, _, _, mods := data[i], data[i+1], data[i+2], data[i+3], data[i+4]
		if dl > 0 {
			line += dl
			char = dc
		} else {
			char += dc
		}
		if line == wantLine && char == wantChar && (mods&wantMod) != 0 {
			return true
		}
	}

	return false
}
