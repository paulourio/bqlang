package lsp_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/bqconfig"
	"github.com/paulourio/bqlang/catalog"
	"github.com/paulourio/bqlang/lsp"
)

func TestStatementsAllKindsAndEdgeCases(t *testing.T) {
	t.Parallel()

	c := startTestServer(t, lsp.ServerOptions{Version: "test-1.0"})
	initResp := c.call("initialize", defaultInitParams(t.TempDir()))
	require.Nil(t, initResp.Error)

	var initRes lsp.InitializeResult
	require.NoError(t, json.Unmarshal(initResp.Result, &initRes))
	require.NotNil(t, initRes.Capabilities.Experimental)
	assert.True(t, initRes.Capabilities.Experimental.BQPLSStatements)

	c.notify("initialized", map[string]any{})

	t.Run("all statement kinds with comments and semicolons trimmed", func(t *testing.T) {
		t.Parallel()

		doc := strings.Join([]string{
			"-- leading comment",
			"SELECT 1 /* inline */ ; -- trailing",
			"",
			"WITH cte AS (SELECT 2) SELECT * FROM cte;",
			"INSERT INTO ds.t VALUES (1);",
			"UPDATE ds.t SET a = 1 WHERE TRUE;",
			"DELETE FROM ds.t WHERE a = 1;",
			"CREATE TABLE ds.t (id INT64);",
			"DECLARE x INT64 DEFAULT 1;",
			"SET x = 2;",
			"BEGIN",
			"  DECLARE y INT64;",
			"  SET y = x;",
			"END;",
			"CALL ds.my_proc(1);",
			"EXPORT DATA OPTIONS(uri='gs://b/*.parquet', format='PARQUET') AS SELECT 1;",
			"LOAD DATA INTO ds.t FROM FILES(format='CSV', uris=['gs://b/a.csv']);",
			"ASSERT 1 = 1;",
		}, "\n")

		uri := "untitled:all_kinds.sql"
		c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
			TextDocument: lsp.TextDocumentItem{URI: uri, LanguageID: "bqlang", Version: 1, Text: doc},
		})

		resp := c.call("bqpls/statements", lsp.StatementsParams{
			TextDocument: lsp.TextDocumentIdentifier{URI: uri},
		})
		require.Nil(t, resp.Error)

		var res lsp.StatementsResult
		require.NoError(t, json.Unmarshal(resp.Result, &res))

		wantKinds := []lsp.StatementKind{
			lsp.StatementKindQuery,
			lsp.StatementKindQuery,
			lsp.StatementKindDML,
			lsp.StatementKindDML,
			lsp.StatementKindDML,
			lsp.StatementKindDDL,
			lsp.StatementKindScript,
			lsp.StatementKindScript,
			lsp.StatementKindScript,
			lsp.StatementKindCall,
			lsp.StatementKindExport,
			lsp.StatementKindLoad,
			lsp.StatementKindOther,
		}
		require.Len(t, res.Statements, len(wantKinds))
		for i, wk := range wantKinds {
			assert.Equal(t, wk, res.Statements[i].Kind, "statement %d: %q", i, res.Statements[i].Text)
			assert.False(t, strings.HasSuffix(res.Statements[i].Text, ";"), "statement %d should exclude trailing ';'", i)
		}

		// Verify first statement trimmed leading comment, trailing comment, and ';'.
		assert.Equal(t, "SELECT 1", res.Statements[0].Text)
		assert.Equal(t, lsp.Range{
			Start: lsp.Position{Line: 1, Character: 0},
			End:   lsp.Position{Line: 1, Character: 8},
		}, res.Statements[0].Range)

		// Verify procedural block stayed intact as one statement.
		assert.Equal(t, "BEGIN\n  DECLARE y INT64;\n  SET y = x;\nEND", res.Statements[8].Text)
	})

	t.Run("UTF-16 positions and Jinja preserved verbatim", func(t *testing.T) {
		t.Parallel()

		doc := "SELECT '🚀' AS rocket;\nSELECT id FROM {{ ref('orders') }} {% if true %}LIMIT 10{% endif %}"
		uri := "untitled:utf16_jinja.sql"
		c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
			TextDocument: lsp.TextDocumentItem{URI: uri, LanguageID: "bqlang", Version: 1, Text: doc},
		})

		resp := c.call("bqpls/statements", lsp.StatementsParams{
			TextDocument: lsp.TextDocumentIdentifier{URI: uri},
		})
		require.Nil(t, resp.Error)

		var res lsp.StatementsResult
		require.NoError(t, json.Unmarshal(resp.Result, &res))
		require.Len(t, res.Statements, 2)

		// '🚀' is 2 UTF-16 code units: "SELECT '" (8) + "🚀" (2) + "' AS rocket" (11) = 21.
		assert.Equal(t, "SELECT '🚀' AS rocket", res.Statements[0].Text)
		assert.Equal(t, lsp.StatementKindQuery, res.Statements[0].Kind)
		assert.Equal(t, lsp.Range{
			Start: lsp.Position{Line: 0, Character: 0},
			End:   lsp.Position{Line: 0, Character: 21},
		}, res.Statements[0].Range)

		assert.Equal(t, "SELECT id FROM {{ ref('orders') }} {% if true %}LIMIT 10{% endif %}", res.Statements[1].Text)
		assert.Equal(t, lsp.StatementKindQuery, res.Statements[1].Kind)
	})

	t.Run("broken SQL never errors and falls back to scanner splitting", func(t *testing.T) {
		t.Parallel()

		doc := "SELECT FROM WHERE;\nCREATE TABLE broken (\n;\nINSERT INTO bad_dml VALUES ("
		uri := "untitled:broken.sql"
		c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
			TextDocument: lsp.TextDocumentItem{URI: uri, LanguageID: "bqlang", Version: 1, Text: doc},
		})

		resp := c.call("bqpls/statements", lsp.StatementsParams{
			TextDocument: lsp.TextDocumentIdentifier{URI: uri},
		})
		require.Nil(t, resp.Error)

		var res lsp.StatementsResult
		require.NoError(t, json.Unmarshal(resp.Result, &res))
		require.Len(t, res.Statements, 2)
		assert.Equal(t, lsp.StatementKindQuery, res.Statements[0].Kind)
		assert.Equal(t, "SELECT FROM WHERE", res.Statements[0].Text)
		assert.Equal(t, lsp.StatementKindDDL, res.Statements[1].Kind)
	})

	t.Run("empty and comment-only document returns empty JSON array", func(t *testing.T) {
		t.Parallel()

		uri := "untitled:empty.sql"
		c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
			TextDocument: lsp.TextDocumentItem{URI: uri, LanguageID: "bqlang", Version: 1, Text: "-- just a comment\n;  \n"},
		})

		resp := c.call("bqpls/statements", lsp.StatementsParams{
			TextDocument: lsp.TextDocumentIdentifier{URI: uri},
		})
		require.Nil(t, resp.Error)
		assert.JSONEq(t, `{"statements":[]}`, string(resp.Result))
	})

	t.Run("unopened document returns invalid params", func(t *testing.T) {
		t.Parallel()

		resp := c.call("bqpls/statements", lsp.StatementsParams{
			TextDocument: lsp.TextDocumentIdentifier{URI: "untitled:missing.sql"},
		})
		require.NotNil(t, resp.Error)
		assert.Equal(t, lsp.ErrCodeInvalidParams, resp.Error.Code)
	})
}

func TestServerEmbedOptionsTokenProviderAndDefaultLSPOptions(t *testing.T) {
	t.Parallel()

	// Invalid DefaultLSPOptions rejected by NewServer.
	badOpts := bqconfig.DefaultLSPOptions()
	badOpts.CacheTTLSeconds = -5
	_, err := lsp.NewServer(lsp.ServerOptions{DefaultLSPOptions: badOpts})
	require.Error(t, err)

	var (
		tableAuthHeader  atomic.Value
		dryRunAuthHeader atomic.Value
	)

	bqSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/projects/pref-proj/datasets/pref-ds/tables/events":
			tableAuthHeader.Store(r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"tableReference":{"projectId":"pref-proj","datasetId":"pref-ds","tableId":"events"},"schema":{"fields":[{"name":"event_id","type":"INT64"}]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/projects/pref-proj/jobs":
			dryRunAuthHeader.Store(r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"statistics":{"totalBytesProcessed":"2048","query":{"totalBytesBilled":"10485760","statementType":"SELECT"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(bqSrv.Close)

	prefOpts := bqconfig.DefaultLSPOptions()
	prefOpts.DefaultProject = "pref-proj"
	prefOpts.DefaultDataset = "pref-ds"
	prefOpts.BigQueryAPIEnabled = true
	prefOpts.CacheDir = t.TempDir()
	prefOpts.CostDryRunDebounceMs = 0

	c := startTestServer(t, lsp.ServerOptions{
		DefaultLSPOptions: prefOpts,
		TokenProvider: func(context.Context) (string, error) {
			return "custom-embedded-bearer", nil
		},
		StoreOptions: []catalog.StoreOption{
			catalog.WithRestOptions(catalog.WithBaseURL(bqSrv.URL)),
		},
	})

	initResp := c.call("initialize", defaultInitParams(t.TempDir()))
	require.Nil(t, initResp.Error)
	c.notify("initialized", map[string]any{})

	// Open a file in a directory with NO .bqfmt.toml — should inherit prefOpts and custom TokenProvider.
	wsDir := t.TempDir()
	sqlURI := pathToURI(filepath.Join(wsDir, "q.sql"))
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{
			URI:        sqlURI,
			LanguageID: "bqlang",
			Version:    1,
			Text:       "SELECT event_id FROM events",
		},
	})

	require.Eventually(t, func() bool {
		v1, ok1 := tableAuthHeader.Load().(string)
		v2, ok2 := dryRunAuthHeader.Load().(string)

		return ok1 && ok2 && v1 == "Bearer custom-embedded-bearer" && v2 == "Bearer custom-embedded-bearer"
	}, 5*time.Second, 25*time.Millisecond)

	// Verify .bqfmt.toml in a subdirectory still overrides DefaultLSPOptions.
	subDir := filepath.Join(wsDir, "override")
	require.NoError(t, os.MkdirAll(subDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(subDir, ".bqfmt.toml"), []byte("[lsp]\nbigquery_api_enabled = false\n"), 0o600))
	overrideURI := pathToURI(filepath.Join(subDir, "local.sql"))
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{
			URI:        overrideURI,
			LanguageID: "bqlang",
			Version:    1,
			Text:       "SELECT 1",
		},
	})

	stmtResp := c.call("bqpls/statements", lsp.StatementsParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: overrideURI},
	})
	require.Nil(t, stmtResp.Error)

	_ = c.call("shutdown", nil)
	c.notify("exit", nil)
	require.NoError(t, <-c.srvErr)
}

func TestServeReturnsOnContextCancelAndClose(t *testing.T) {
	t.Parallel()

	t.Run("context cancellation unblocks Serve over io.Pipe", func(t *testing.T) {
		t.Parallel()

		sr, cw := io.Pipe()
		cr, sw := io.Pipe()
		defer func() {
			_ = cw.Close()
			_ = cr.Close()
		}()

		srv, err := lsp.NewServer(lsp.ServerOptions{})
		require.NoError(t, err)
		defer func() { _ = srv.Close() }()

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- srv.Serve(ctx, sr, sw)
		}()

		cancel()
		select {
		case serveErr := <-done:
			require.ErrorIs(t, serveErr, context.Canceled)
		case <-time.After(3 * time.Second):
			t.Fatal("Serve did not return promptly after context cancellation")
		}
	})

	t.Run("Close is idempotent and unblocks Serve", func(t *testing.T) {
		t.Parallel()

		sr, cw := io.Pipe()
		cr, sw := io.Pipe()
		defer func() {
			_ = cw.Close()
			_ = cr.Close()
		}()

		srv, err := lsp.NewServer(lsp.ServerOptions{})
		require.NoError(t, err)

		done := make(chan error, 1)
		go func() {
			done <- srv.Serve(context.Background(), sr, sw)
		}()

		// Give Serve a moment to register serveCancel.
		time.Sleep(25 * time.Millisecond)
		require.NoError(t, srv.Close())
		require.NoError(t, srv.Close())

		select {
		case serveErr := <-done:
			require.NoError(t, serveErr)
		case <-time.After(3 * time.Second):
			t.Fatal("Serve did not return promptly after Close")
		}
	})
}

func TestSharedStoreConcurrentLSPAndBrowser(t *testing.T) {
	t.Parallel()

	bqSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects/shared-proj/datasets":
			_, _ = w.Write([]byte(`{"datasets":[{"datasetReference":{"projectId":"shared-proj","datasetId":"analytics"}}]}`))
		case "/projects/shared-proj/datasets/analytics/tables":
			_, _ = w.Write([]byte(`{"tables":[{"tableReference":{"projectId":"shared-proj","datasetId":"analytics","tableId":"orders"},"type":"TABLE"}]}`))
		case "/projects/shared-proj/datasets/analytics/tables/orders":
			_, _ = w.Write([]byte(`{"tableReference":{"projectId":"shared-proj","datasetId":"analytics","tableId":"orders"},"schema":{"fields":[{"name":"order_id","type":"INT64"},{"name":"amount","type":"FLOAT64"}]}}`))
		case "/projects/shared-proj/datasets/analytics/tables/customers":
			_, _ = w.Write([]byte(`{"tableReference":{"projectId":"shared-proj","datasetId":"analytics","tableId":"customers"},"schema":{"fields":[{"name":"customer_id","type":"INT64"}]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(bqSrv.Close)

	storeOpts := bqconfig.DefaultLSPOptions()
	storeOpts.DefaultProject = "shared-proj"
	storeOpts.DefaultDataset = "analytics"
	storeOpts.BigQueryAPIEnabled = true
	storeOpts.CacheDir = t.TempDir()

	rest := catalog.NewRestClient(
		catalog.WithBaseURL(bqSrv.URL),
		catalog.WithTokenProvider(func(context.Context) (string, error) { return "tok", nil }),
	)

	shared, err := catalog.NewStore(storeOpts, catalog.WithRestClient(rest))
	require.NoError(t, err)
	t.Cleanup(func() { _ = shared.Close() })

	browserUpdates := make(chan catalog.TableRef, 16)
	unsubBrowser := shared.Subscribe(func(ref catalog.TableRef) {
		browserUpdates <- ref
	})
	defer unsubBrowser()

	c := startTestServer(t, lsp.ServerOptions{
		Store:             shared,
		DefaultLSPOptions: storeOpts,
	})

	initResp := c.call("initialize", defaultInitParams(t.TempDir()))
	require.Nil(t, initResp.Error)
	c.notify("initialized", map[string]any{})

	// Run concurrent "dataset browser" goroutines while the LSP server opens & queries documents.
	var browserWG sync.WaitGroup
	for range 4 {
		browserWG.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			for range 5 {
				tables, _ := shared.DiscoverTables(ctx, "shared-proj", "analytics", time.Second)
				assert.NotEmpty(t, tables)
				_, _ = shared.GetTable(catalog.TableRef{Project: "shared-proj", Dataset: "analytics", Table: "orders"})
				meta, ok := shared.WaitForTable(ctx, catalog.TableRef{Project: "shared-proj", Dataset: "analytics", Table: "orders"}, time.Second)
				if ok {
					assert.Equal(t, "orders", meta.Ref.Table)
				}
			}
		})
	}

	sqlURI := "untitled:shared.sql"
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{
			URI:        sqlURI,
			LanguageID: "bqlang",
			Version:    1,
			Text:       "SELECT order_id, amount FROM orders",
		},
	})

	tokResp := c.call("textDocument/semanticTokens/full", lsp.SemanticTokensParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: sqlURI},
	})
	require.Nil(t, tokResp.Error)

	browserWG.Wait()

	// Shutdown and close the LSP server; the injected store must remain open and usable!
	_ = c.call("shutdown", nil)
	c.notify("exit", nil)
	require.NoError(t, <-c.srvErr)
	require.NoError(t, c.srv.Close())

	// Fetch a brand-new table through the shared store after Server.Close to prove workers are still alive.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	custRef := catalog.TableRef{Project: "shared-proj", Dataset: "analytics", Table: "customers"}
	meta, ok := shared.WaitForTable(ctx, custRef, 2*time.Second)
	require.True(t, ok, "shared store must stay alive after Server.Close")
	assert.Equal(t, "customers", meta.Ref.Table)

	// Browser subscriber still receives updates after Server.Close.
	sawCustomers := false
	deadline := time.After(2 * time.Second)
	for !sawCustomers {
		select {
		case ref := <-browserUpdates:
			if ref.Table == "customers" {
				sawCustomers = true
			}
		case <-deadline:
			t.Fatal("browser subscriber did not receive update after Server.Close")
		}
	}
}
