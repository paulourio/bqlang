package e2e_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/bqconfig"
	"github.com/paulourio/bqlang/catalog"
	"github.com/paulourio/bqlang/lsp"
)

func TestInProcessEmbeddedServerOverIOPipe(t *testing.T) {
	t.Parallel()

	// Build a shared catalog.Store as an external embedder (e.g. bqui) would.
	storeOpts := bqconfig.DefaultLSPOptions()
	storeOpts.DefaultProject = "embed-proj"
	storeOpts.DefaultDataset = "embed-ds"
	storeOpts.CacheDir = t.TempDir()
	require.NoError(t, storeOpts.Validate())

	shared, err := catalog.NewStore(storeOpts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = shared.Close() })

	shared.PutTable(&catalog.TableMeta{
		Ref: catalog.TableRef{Project: "embed-proj", Dataset: "embed-ds", Table: "orders"},
		Schema: []catalog.ColumnMeta{
			{Name: "order_id", Type: "INT64"},
			{Name: "event_date", Type: "DATE"},
		},
		Partitioning: &catalog.PartitionInfo{
			Kind:  catalog.PartitionKindTime,
			Field: "event_date",
		},
	})

	cr, sw := io.Pipe() // client reads what the server writes
	sr, cw := io.Pipe() // server reads what the client writes

	srv, err := lsp.NewServer(lsp.ServerOptions{
		Store:             shared,
		DefaultLSPOptions: storeOpts,
		Logger:            slog.New(slog.DiscardHandler),
		Version:           "embed-test",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	serveDone := make(chan error, 1)
	go func() {
		defer func() { _ = sw.Close() }()
		serveDone <- srv.Serve(ctx, sr, sw)
	}()

	clientTr := lsp.NewTransport(cr, cw)

	// Helper to send a request and read frames until the matching response arrives.
	callRPC := func(id int, method string, params any) lsp.RPCMessage {
		t.Helper()

		rawID, mErr := json.Marshal(id)
		require.NoError(t, mErr)

		reqBody, mErr := json.Marshal(lsp.RequestEnvelope{
			JSONRPC: "2.0",
			ID:      rawID,
			Method:  method,
			Params:  params,
		})
		require.NoError(t, mErr)
		require.NoError(t, clientTr.WriteFrame(reqBody))

		wantKey, _ := lsp.ClassifyAndNormalizeID(rawID)
		for {
			frame, rErr := clientTr.ReadFrame()
			require.NoError(t, rErr)

			msg, kind, rpcErr := lsp.DecodeRPCMessage(frame)
			require.Nil(t, rpcErr)

			if kind == lsp.MessageKindResponse {
				gotKey, _ := lsp.ClassifyAndNormalizeID(msg.ID)
				if gotKey == wantKey {
					return msg
				}
			}
		}
	}

	notifyRPC := func(method string, params any) {
		t.Helper()

		require.NoError(t, clientTr.SendNotification(method, params))
	}

	// 1. initialize
	initResp := callRPC(1, "initialize", lsp.InitializeParams{
		ClientInfo: &lsp.ClientInfo{Name: "bqui-test", Version: "0.1"},
	})
	require.Nil(t, initResp.Error)

	var initRes lsp.InitializeResult
	require.NoError(t, json.Unmarshal(initResp.Result, &initRes))
	require.NotNil(t, initRes.Capabilities.Experimental)
	assert.True(t, initRes.Capabilities.Experimental.BQPLSStatements)
	require.NotNil(t, initRes.Capabilities.SemanticTokensProvider)
	assert.Contains(t, initRes.Capabilities.SemanticTokensProvider.Legend.TokenModifiers, "partitioned")

	notifyRPC("initialized", map[string]any{})

	// 2. didOpen
	uri := "untitled:embedded.sql"
	sqlDoc := "-- query\nSELECT order_id, event_date FROM orders WHERE event_date = DATE '2026-01-01';\nCREATE TEMP TABLE tmp AS SELECT 1;"
	notifyRPC("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{
			URI:        uri,
			LanguageID: "bqlang",
			Version:    1,
			Text:       sqlDoc,
		},
	})

	// 3. semanticTokens/full
	tokResp := callRPC(2, "textDocument/semanticTokens/full", lsp.SemanticTokensParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
	})
	require.Nil(t, tokResp.Error)

	var tokRes lsp.SemanticTokens
	require.NoError(t, json.Unmarshal(tokResp.Result, &tokRes))
	assert.NotEmpty(t, tokRes.Data)
	assert.Zero(t, len(tokRes.Data)%5)

	// 4. bqpls/statements
	stmtResp := callRPC(3, "bqpls/statements", lsp.StatementsParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
	})
	require.Nil(t, stmtResp.Error)

	var stmtRes lsp.StatementsResult
	require.NoError(t, json.Unmarshal(stmtResp.Result, &stmtRes))
	require.Len(t, stmtRes.Statements, 2)
	assert.Equal(t, lsp.StatementKindQuery, stmtRes.Statements[0].Kind)
	assert.Equal(t, "SELECT order_id, event_date FROM orders WHERE event_date = DATE '2026-01-01'", stmtRes.Statements[0].Text)
	assert.Equal(t, lsp.Range{
		Start: lsp.Position{Line: 1, Character: 0},
		End:   lsp.Position{Line: 1, Character: 76},
	}, stmtRes.Statements[0].Range)

	assert.Equal(t, lsp.StatementKindDDL, stmtRes.Statements[1].Kind)
	assert.Equal(t, "CREATE TEMP TABLE tmp AS SELECT 1", stmtRes.Statements[1].Text)

	// 5. shutdown -> exit
	shutResp := callRPC(4, "shutdown", nil)
	require.Nil(t, shutResp.Error)

	notifyRPC("exit", nil)
	require.NoError(t, cw.Close())
	require.NoError(t, cr.Close())

	// 6. Serve returns nil
	select {
	case serveErr := <-serveDone:
		require.NoError(t, serveErr)
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after shutdown + exit")
	}

	// 7. Close is idempotent and leaves the injected Store usable.
	require.NoError(t, srv.Close())
	require.NoError(t, srv.Close())

	meta, ok := shared.GetTable(catalog.TableRef{Project: "embed-proj", Dataset: "embed-ds", Table: "orders"})
	require.True(t, ok, "injected store must remain usable after Server.Close")
	assert.Equal(t, "orders", meta.Ref.Table)
	assert.True(t, meta.IsPartitionColumn("event_date"))

	shared.PutTable(&catalog.TableMeta{
		Ref:    catalog.TableRef{Project: "embed-proj", Dataset: "embed-ds", Table: "post_close"},
		Schema: []catalog.ColumnMeta{{Name: "id", Type: "INT64"}},
	})
	postMeta, postOK := shared.GetTable(catalog.TableRef{Project: "embed-proj", Dataset: "embed-ds", Table: "post_close"})
	require.True(t, postOK)
	assert.Equal(t, "post_close", postMeta.Ref.Table)
}
