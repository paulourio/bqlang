package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/bqconfig"
	"github.com/paulourio/bqlang/internal/analysis"
)

func TestCancelQueuedRequestDeterministic(t *testing.T) {
	t.Parallel()

	srv, err := NewServer(ServerOptions{})
	require.NoError(t, err)
	defer func() { _ = srv.Close() }()

	// Initialize server state synchronously.
	initRes, initErr := srv.handleInitialize(t.Context(), nil)
	require.Nil(t, initErr)
	require.NotNil(t, initRes)

	inR, inW := io.Pipe()
	defer func() { _ = inW.Close() }()
	defer func() { _ = inR.Close() }()

	outR, outW := io.Pipe()
	defer func() { _ = outR.Close() }()
	defer func() { _ = outW.Close() }()

	srvTr := NewTransport(inR, outW)
	cliTr := NewTransport(outR, inW)
	cmdQueue := newCommandQueue()

	// Route a request and its $/cancelRequest before runDispatcher pops the item,
	// guaranteeing deterministic cancellation before worker execution.
	srv.routeInboundMessage(t.Context(), srvTr, cmdQueue, RPCMessage{
		JSONRPC: "2.0",
		ID:      json.RawMessage("702"),
		Method:  "textDocument/semanticTokens/full",
		Params:  json.RawMessage(`{"textDocument":{"uri":"file:///tmp/x.sql"}}`),
	}, MessageKindRequest, func() {}, func() {})

	srv.routeInboundMessage(t.Context(), srvTr, cmdQueue, RPCMessage{
		JSONRPC: "2.0",
		Method:  "$/cancelRequest",
		Params:  json.RawMessage(`{"id":702}`),
	}, MessageKindNotification, func() {}, func() {})

	// Also queue a cancelled sync initialize request to cover finishSyncRequest cancel branch.
	srv.routeInboundMessage(t.Context(), srvTr, cmdQueue, RPCMessage{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"sync-cancel"`),
		Method:  "initialize",
	}, MessageKindRequest, func() {}, func() {})
	srv.routeInboundMessage(t.Context(), srvTr, cmdQueue, RPCMessage{
		JSONRPC: "2.0",
		Method:  "$/cancelRequest",
		Params:  json.RawMessage(`{"id":"sync-cancel"}`),
	}, MessageKindNotification, func() {}, func() {})

	// Invalid $/cancelRequest payloads must be ignored cleanly.
	srv.handleCancelNotification(t.Context(), json.RawMessage(`{bad`))
	srv.handleCancelNotification(t.Context(), json.RawMessage(`{"id":null}`))

	cmdQueue.Close()

	var (
		dispatchWG sync.WaitGroup
		workerWG   sync.WaitGroup
	)
	dispatchWG.Add(1)
	go func() {
		srv.runDispatcher(t.Context(), srvTr, cmdQueue, &dispatchWG, &workerWG, func() {})
		workerWG.Wait()
	}()

	got := make(map[string]RPCMessage, 2)
	for range 2 {
		frame, readErr := cliTr.ReadFrame()
		require.NoError(t, readErr)

		var msg RPCMessage
		require.NoError(t, json.Unmarshal(frame, &msg))
		key, _ := ClassifyAndNormalizeID(msg.ID)
		got[key] = msg
	}
	dispatchWG.Wait()

	require.Contains(t, got, "n:702")
	require.NotNil(t, got["n:702"].Error)
	assert.Equal(t, ErrCodeRequestCancelled, got["n:702"].Error.Code)

	require.Contains(t, got, "s:sync-cancel")
	require.NotNil(t, got["s:sync-cancel"].Error)
	assert.Equal(t, ErrCodeRequestCancelled, got["s:sync-cancel"].Error.Code)
}

func TestMapAnalysisErrorResponseErrorAndFormattingCancel(t *testing.T) {
	t.Parallel()

	var nilErr *ResponseError
	assert.Empty(t, nilErr.Error())

	cancelled := mapAnalysisError(context.Canceled)
	require.NotNil(t, cancelled)
	assert.Equal(t, ErrCodeRequestCancelled, cancelled.Code)
	assert.Equal(t, "jsonrpc error -32800: request cancelled", cancelled.Error())

	notOpen := mapAnalysisError(analysis.ErrDocumentNotOpen)
	require.NotNil(t, notOpen)
	assert.Equal(t, ErrCodeInvalidParams, notOpen.Code)

	internal := mapAnalysisError(errors.New("unexpected failure"))
	require.NotNil(t, internal)
	assert.Equal(t, ErrCodeInternalError, internal.Code)
	assert.Contains(t, internal.Message, "unexpected failure")

	srv, err := NewServer(ServerOptions{})
	require.NoError(t, err)
	defer func() { _ = srv.Close() }()

	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	_, rpcErr := srv.handleFormatting(canceledCtx, json.RawMessage(`{"textDocument":{"uri":"file:///tmp/a.sql"},"options":{"tabSize":2,"insertSpaces":true}}`))
	require.NotNil(t, rpcErr)
	assert.Equal(t, ErrCodeRequestCancelled, rpcErr.Code)

	// Missing params on decodeParams -> -32602.
	_, decErr := decodeParams[HoverParams](nil)
	require.NotNil(t, decErr)
	assert.Equal(t, ErrCodeInvalidParams, decErr.Code)
}

func TestIsConfigDocumentAndLoadInitialConfigFallback(t *testing.T) {
	t.Parallel()

	wsDir := t.TempDir()
	customCfg := filepath.Join(wsDir, "custom.toml")
	require.NoError(t, os.WriteFile(customCfg, []byte("[lsp]\n"), 0o600))

	srv, err := NewServer(ServerOptions{ConfigPath: customCfg})
	require.NoError(t, err)
	defer func() { _ = srv.Close() }()

	assert.False(t, srv.isConfigDocument("untitled:foo.sql"))
	assert.True(t, srv.isConfigDocument("file://"+filepath.ToSlash(customCfg)))
	assert.True(t, srv.isConfigDocument("file://"+filepath.ToSlash(filepath.Join(wsDir, bqconfig.ConfigFileName))))
	assert.False(t, srv.isConfigDocument("file://"+filepath.ToSlash(filepath.Join(wsDir, "query.sql"))))

	// loadInitialDefaultConfig warning fallback when loader encounters malformed .bqfmt.toml.
	badDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(badDir, bqconfig.ConfigFileName), []byte("[lsp\nbad = ???"), 0o600))
	loader, lErr := bqconfig.NewLoader("")
	require.NoError(t, lErr)
	cfg := loadInitialDefaultConfig(loader, slog.New(slog.DiscardHandler))
	require.NotNil(t, cfg)
	require.NotNil(t, cfg.LSP)
}

func TestPublishDocDiagnosticsSkipsStaleVersionAndClosedDoc(t *testing.T) {
	t.Parallel()

	srv, err := NewServer(ServerOptions{})
	require.NoError(t, err)
	defer func() { _ = srv.Close() }()

	var out bytes.Buffer
	tr := NewTransport(bytes.NewReader(nil), &out)

	uri := "file:///tmp/diag_guard.sql"
	srv.docsMu.Lock()
	srv.docs[uri] = &openDocState{
		uri:                uri,
		version:            1,
		diagnosticsEnabled: true,
	}
	srv.docsMu.Unlock()

	assert.True(t, srv.isDocDiagStateCurrent(uri, 1, true))
	assert.False(t, srv.isDocDiagStateCurrent(uri, 2, true))
	assert.False(t, srv.isDocDiagStateCurrent(uri, 1, false))
	assert.False(t, srv.isDocDiagStateCurrent("file:///tmp/missing.sql", 1, true))

	// Calling publishDocDiagnostics when engine doesn't have the doc returns silently without emitting.
	srv.publishDocDiagnostics(t.Context(), tr, uri)
	assert.Empty(t, out.Bytes())

	// Calling publishDocDiagnostics on unopened URI returns silently.
	srv.publishDocDiagnostics(t.Context(), tr, "file:///tmp/missing.sql")
	assert.Empty(t, out.Bytes())
}

func TestTransportChunkedPayloadAndClosedSendRequest(t *testing.T) {
	t.Parallel()

	payload := bytes.Repeat([]byte("a"), chunkedReadThreshold+4096)
	var wire bytes.Buffer
	writerTr := NewTransportWithLimit(bytes.NewReader(nil), &wire, 0)
	require.NoError(t, writerTr.WriteFrame(payload))

	readerTr := NewTransport(&wire, io.Discard)
	got, err := readerTr.ReadFrame()
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	// SendRequest on already-closed transport returns ErrTransportClosed immediately.
	readerTr.FailPending(nil)
	_, reqErr := readerTr.SendRequest(t.Context(), "workspace/semanticTokens/refresh", nil)
	require.ErrorIs(t, reqErr, ErrTransportClosed)

	// DeliverResponse returns false for unknown / invalid IDs.
	assert.False(t, readerTr.DeliverResponse(RPCMessage{ID: json.RawMessage("null")}))
	assert.False(t, readerTr.DeliverResponse(RPCMessage{ID: json.RawMessage("9999")}))

	// Canceled SendRequest returns context error.
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	defer func() { _ = pr.Close() }()

	var sink bytes.Buffer
	tr := NewTransport(pr, &sink)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, timeoutErr := tr.SendRequest(ctx, "workspace/semanticTokens/refresh", nil)
	require.ErrorIs(t, timeoutErr, context.DeadlineExceeded)
}
