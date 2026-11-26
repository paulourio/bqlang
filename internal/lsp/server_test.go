package lsp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/internal/analysis"
	"github.com/paulourio/bqlang/internal/catalog"
	"github.com/paulourio/bqlang/internal/lsp"
	"github.com/paulourio/bqlang/internal/syntax"
)

type testClient struct {
	t      *testing.T
	tr     *lsp.Transport
	inW    *io.PipeWriter
	outR   *io.PipeReader
	srvErr chan error

	mu             sync.Mutex
	nextID         int
	pending        map[string]chan lsp.RPCMessage
	diagnostics    map[string][]lsp.PublishDiagnosticsParams
	queryCosts     map[string][]analysis.DocumentCostSummary
	serverRequests []lsp.RPCMessage
	diagSig        chan struct{}
	costSig        chan struct{}
	reqSig         chan struct{}
	done           chan struct{}
}

func startTestServer(t *testing.T, opts lsp.ServerOptions) *testClient {
	t.Helper()

	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}

	srv, err := lsp.NewServer(opts)
	require.NoError(t, err)

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	c := &testClient{
		t:           t,
		tr:          lsp.NewTransport(outR, inW),
		inW:         inW,
		outR:        outR,
		srvErr:      make(chan error, 1),
		nextID:      1,
		pending:     make(map[string]chan lsp.RPCMessage),
		diagnostics: make(map[string][]lsp.PublishDiagnosticsParams),
		queryCosts:  make(map[string][]analysis.DocumentCostSummary),
		diagSig:     make(chan struct{}, 32),
		costSig:     make(chan struct{}, 32),
		reqSig:      make(chan struct{}, 32),
		done:        make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		err := srv.Serve(ctx, inR, outW)
		_ = outW.Close()
		c.srvErr <- err
	}()

	go c.readLoop()

	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
		_ = outR.Close()
		_ = srv.Close()
		<-c.done
	})

	return c
}

func (c *testClient) readLoop() {
	defer close(c.done)

	for {
		frame, err := c.tr.ReadFrame()
		if err != nil {
			return
		}

		var msg lsp.RPCMessage
		if uerr := json.Unmarshal(frame, &msg); uerr != nil {
			continue
		}

		idKey, idKind := lsp.ClassifyAndNormalizeID(msg.ID)
		if msg.Method != "" {
			switch {
			case idKind == lsp.IDString || idKind == lsp.IDNumber:
				c.mu.Lock()
				c.serverRequests = append(c.serverRequests, msg)
				c.mu.Unlock()
				select {
				case c.reqSig <- struct{}{}:
				default:
				}
				_ = c.tr.SendSuccess(msg.ID, nil)
			case msg.Method == "textDocument/publishDiagnostics":
				var p lsp.PublishDiagnosticsParams
				if json.Unmarshal(msg.Params, &p) == nil {
					c.mu.Lock()
					c.diagnostics[p.URI] = append(c.diagnostics[p.URI], p)
					c.mu.Unlock()
					select {
					case c.diagSig <- struct{}{}:
					default:
					}
				}
			case msg.Method == "bqpls/queryCost":
				var p analysis.DocumentCostSummary
				if json.Unmarshal(msg.Params, &p) == nil {
					c.mu.Lock()
					c.queryCosts[p.URI] = append(c.queryCosts[p.URI], p)
					c.mu.Unlock()
					select {
					case c.costSig <- struct{}{}:
					default:
					}
				}
			}
			continue
		}

		c.mu.Lock()
		var ch chan lsp.RPCMessage
		if idKind == lsp.IDNull {
			ch = c.pending["null"]
			delete(c.pending, "null")
		} else {
			ch = c.pending[idKey]
			delete(c.pending, idKey)
		}
		c.mu.Unlock()

		if ch != nil {
			ch <- msg
		}
	}
}

func (c *testClient) startRequest(rawID json.RawMessage, method string, params any) <-chan lsp.RPCMessage {
	c.t.Helper()

	key, kind := lsp.ClassifyAndNormalizeID(rawID)
	require.True(c.t, kind == lsp.IDString || kind == lsp.IDNumber)

	ch := make(chan lsp.RPCMessage, 1)
	c.mu.Lock()
	c.pending[key] = ch
	c.mu.Unlock()

	env := lsp.RequestEnvelope{
		JSONRPC: "2.0",
		ID:      rawID,
		Method:  method,
		Params:  params,
	}
	data, err := json.Marshal(env)
	require.NoError(c.t, err)
	require.NoError(c.t, c.tr.WriteFrame(data))

	return ch
}

func (c *testClient) call(method string, params any) lsp.RPCMessage {
	c.t.Helper()

	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.mu.Unlock()

	rawID := json.RawMessage(strconv.Itoa(id))
	ch := c.startRequest(rawID, method, params)

	select {
	case resp := <-ch:
		return resp
	case <-time.After(10 * time.Second):
		c.t.Fatalf("timed out waiting for %s response", method)
		return lsp.RPCMessage{}
	}
}

func (c *testClient) notify(method string, params any) {
	c.t.Helper()
	require.NoError(c.t, c.tr.SendNotification(method, params))
}

func (c *testClient) expectParseError(rawFrame []byte) lsp.RPCMessage {
	c.t.Helper()

	ch := make(chan lsp.RPCMessage, 1)
	c.mu.Lock()
	c.pending["null"] = ch
	c.mu.Unlock()

	require.NoError(c.t, c.tr.WriteFrame(rawFrame))

	select {
	case resp := <-ch:
		return resp
	case <-time.After(10 * time.Second):
		c.t.Fatal("timed out waiting for parse error response")
		return lsp.RPCMessage{}
	}
}

func (c *testClient) diagCount(uri string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.diagnostics[uri])
}

func (c *testClient) waitDiagnostics(uri string, afterIdx int) lsp.PublishDiagnosticsParams {
	c.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.mu.Lock()
		list := c.diagnostics[uri]
		if len(list) > afterIdx {
			res := list[len(list)-1]
			c.mu.Unlock()
			return res
		}
		c.mu.Unlock()

		remaining := time.Until(deadline)
		if remaining <= 0 {
			c.t.Fatalf("timed out waiting for diagnostics on %s (afterIdx=%d)", uri, afterIdx)
		}

		select {
		case <-c.diagSig:
		case <-time.After(remaining):
		}
	}
}

func (c *testClient) waitSemanticTokensRefresh() lsp.RPCMessage {
	const method = "workspace/semanticTokens/refresh"
	c.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.mu.Lock()
		for _, req := range c.serverRequests {
			if req.Method == method {
				c.mu.Unlock()
				return req
			}
		}
		c.mu.Unlock()

		remaining := time.Until(deadline)
		if remaining <= 0 {
			c.t.Fatalf("timed out waiting for server request %s", method)
		}

		select {
		case <-c.reqSig:
		case <-time.After(remaining):
		}
	}
}

func pathToURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	return u.String()
}

func setupTestWorkspace(t *testing.T, extraLSPConfig string) (wsDir, cfgPath string) {
	t.Helper()
	wsDir = t.TempDir()
	schemaDir := filepath.Join(wsDir, "schemas")
	require.NoError(t, os.MkdirAll(schemaDir, 0o750))

	tableJSON := `{
		"tableReference": {"projectId": "my-proj", "datasetId": "analytics", "tableId": "events"},
		"type": "TABLE",
		"description": "User event log",
		"schema": {
			"fields": [
				{"name": "event_date", "type": "DATE", "mode": "REQUIRED"},
				{"name": "tenant_id", "type": "STRING", "mode": "REQUIRED"},
				{"name": "payload", "type": "STRING", "mode": "NULLABLE"}
			]
		},
		"timePartitioning": {"type": "DAY", "field": "event_date", "requirePartitionFilter": true},
		"clustering": {"fields": ["tenant_id"]}
	}`
	require.NoError(t, os.WriteFile(filepath.Join(schemaDir, "my-proj.analytics.events.json"), []byte(tableJSON), 0o600))

	cfg := fmt.Sprintf(`
keyword_style = "UPPER_CASE"
indentation = 2

[lsp]
default_project = "my-proj"
default_dataset = "analytics"
schema_dirs = [%q]
cache_dir = %q
bigquery_api_enabled = false
diagnostics_enabled = true
require_partition_filter_severity = "warning"
%s
`, filepath.ToSlash(schemaDir), filepath.ToSlash(filepath.Join(wsDir, ".cache")), extraLSPConfig)

	cfgPath = filepath.Join(wsDir, ".bqfmt.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfg), 0o600))
	return wsDir, cfgPath
}

func defaultInitParams(wsDir string) lsp.InitializeParams {
	return lsp.InitializeParams{
		RootURI: pathToURI(wsDir),
		Capabilities: lsp.ClientCapabilities{
			Workspace: &lsp.WorkspaceClientCapabilities{
				SemanticTokens: &lsp.SemanticTokensWorkspaceClientCapabilities{
					RefreshSupport: true,
				},
			},
			TextDocument: &lsp.TextDocumentClientCapabilities{
				DocumentSymbol: &lsp.DocumentSymbolClientCapabilities{
					HierarchicalDocumentSymbolSupport: true,
				},
			},
		},
	}
}

func TestServerLifecycleAndFeatures(t *testing.T) {
	t.Parallel()

	wsDir, cfgPath := setupTestWorkspace(t, "")
	c := startTestServer(t, lsp.ServerOptions{Version: "test-1.0"})

	// 1. Pre-initialize request -> -32002 (server stays alive).
	preInit := c.call("textDocument/hover", lsp.HoverParams{})
	require.NotNil(t, preInit.Error)
	assert.Equal(t, lsp.ErrCodeServerNotInitialized, preInit.Error.Code)

	// 2. Invalid JSON frame -> -32700 with explicit id:null (server stays alive).
	parseErrResp := c.expectParseError([]byte("{invalid!"))
	require.NotNil(t, parseErrResp.Error)
	assert.Equal(t, lsp.ErrCodeParseError, parseErrResp.Error.Code)
	assert.Equal(t, "null", strings.TrimSpace(string(parseErrResp.ID)))

	// 3. Initialize & capabilities check.
	initResp := c.call("initialize", defaultInitParams(wsDir))
	require.Nil(t, initResp.Error)
	var initRes lsp.InitializeResult
	require.NoError(t, json.Unmarshal(initResp.Result, &initRes))
	require.NotNil(t, initRes.Capabilities.TextDocumentSync)
	assert.True(t, initRes.Capabilities.TextDocumentSync.OpenClose)
	assert.Equal(t, lsp.TextDocumentSyncIncremental, initRes.Capabilities.TextDocumentSync.Change)
	require.NotNil(t, initRes.Capabilities.TextDocumentSync.Save)
	assert.False(t, initRes.Capabilities.TextDocumentSync.Save.IncludeText)
	require.NotNil(t, initRes.Capabilities.SemanticTokensProvider)
	assert.Equal(t, analysis.DefaultSemanticTokensLegend(), initRes.Capabilities.SemanticTokensProvider.Legend)
	assert.True(t, initRes.Capabilities.SemanticTokensProvider.Range)
	require.NotNil(t, initRes.Capabilities.SemanticTokensProvider.Full)
	assert.True(t, initRes.Capabilities.SemanticTokensProvider.Full.Delta)
	assert.True(t, initRes.Capabilities.HoverProvider)
	assert.True(t, initRes.Capabilities.DefinitionProvider)
	assert.True(t, initRes.Capabilities.DocumentSymbolProvider)
	assert.True(t, initRes.Capabilities.DocumentFormattingProvider)
	require.NotNil(t, initRes.Capabilities.CompletionProvider)
	assert.Equal(t, []string{".", "|", "{", "%", "@", "`"}, initRes.Capabilities.CompletionProvider.TriggerCharacters)

	// 4. Second initialize -> -32600.
	secondInit := c.call("initialize", defaultInitParams(wsDir))
	require.NotNil(t, secondInit.Error)
	assert.Equal(t, lsp.ErrCodeInvalidRequest, secondInit.Error.Code)

	// 5. Unknown method -> -32601; unknown $/ notification ignored.
	c.notify("$/unknownNotification", map[string]any{"x": 1})
	unknownResp := c.call("bqpls/nonexistentMethod", map[string]any{})
	require.NotNil(t, unknownResp.Error)
	assert.Equal(t, lsp.ErrCodeMethodNotFound, unknownResp.Error.Code)

	c.notify("initialized", struct{}{})

	// 6. didOpen -> missing partition filter warning; sequential incremental didChange -> clean [].
	sqlURI := pathToURI(filepath.Join(wsDir, "query.sql"))
	cur := c.diagCount(sqlURI)
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{
			URI:        sqlURI,
			LanguageID: "bqlang",
			Version:    1,
			Text:       "select '😀', tenant_id from events",
		},
	})
	d1 := c.waitDiagnostics(sqlURI, cur)
	assert.NotEmpty(t, d1.Diagnostics)

	// Sequential multi-change in one didChange (including UTF-16 surrogate pair 😀).
	cur = c.diagCount(sqlURI)
	prefix := "select '😀', tenant_id from events"
	c.notify("textDocument/didChange", lsp.DidChangeTextDocumentParams{
		TextDocument: lsp.VersionedTextDocumentIdentifier{URI: sqlURI, Version: 2},
		ContentChanges: []lsp.TextDocumentContentChangeEvent{
			{
				Range: &syntax.Range{
					Start: syntax.Position{Line: 0, Character: 0},
					End:   syntax.Position{Line: 0, Character: len("select")},
				},
				Text: "SELECT",
			},
			{
				Range: &syntax.Range{
					Start: syntax.Position{Line: 0, Character: len("SELECT '") + 2 + len("', tenant_id from events")},
					End:   syntax.Position{Line: 0, Character: len(prefix) + 1}, // emoji is 2 UTF-16 units vs 4 UTF-8 bytes; use LineIndex below for exactness
				},
				Text: " WHERE event_date = '2026-01-01' AND tenant_id = 't1'",
			},
		},
	})
	d2 := c.waitDiagnostics(sqlURI, cur)
	assert.Empty(t, d2.Diagnostics)

	// 7. Semantic tokens: full, delta (edits only), delta no-op (edits: []), delta stale fallback (data only), range.
	fullResp := c.call("textDocument/semanticTokens/full", lsp.SemanticTokensParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: sqlURI},
	})
	require.Nil(t, fullResp.Error)
	var fullTok lsp.SemanticTokens
	require.NoError(t, json.Unmarshal(fullResp.Result, &fullTok))
	require.NotEmpty(t, fullTok.ResultID)
	assert.NotEmpty(t, fullTok.Data)

	// No-op delta must emit "edits": [] and NO "data".
	noopDelta := c.call("textDocument/semanticTokens/full/delta", lsp.SemanticTokensDeltaParams{
		TextDocument:     lsp.TextDocumentIdentifier{URI: sqlURI},
		PreviousResultID: fullTok.ResultID,
	})
	require.Nil(t, noopDelta.Error)
	var noopMap map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(noopDelta.Result, &noopMap))
	assert.Contains(t, noopMap, "edits")
	assert.NotContains(t, noopMap, "data")
	assert.Equal(t, "[]", strings.TrimSpace(string(noopMap["edits"])))

	// Stale delta must emit "data" and NO "edits".
	staleDelta := c.call("textDocument/semanticTokens/full/delta", lsp.SemanticTokensDeltaParams{
		TextDocument:     lsp.TextDocumentIdentifier{URI: sqlURI},
		PreviousResultID: "stale-id-999",
	})
	require.Nil(t, staleDelta.Error)
	var staleMap map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(staleDelta.Result, &staleMap))
	assert.Contains(t, staleMap, "data")
	assert.NotContains(t, staleMap, "edits")

	// Real delta edit after an incremental change.
	li := syntax.NewLineIndex("SELECT '😀', tenant_id from events WHERE event_date = '2026-01-01'")
	fromPos := li.PositionAt(strings.Index("SELECT '😀', tenant_id from events WHERE event_date = '2026-01-01'", "from"))
	cur = c.diagCount(sqlURI)
	c.notify("textDocument/didChange", lsp.DidChangeTextDocumentParams{
		TextDocument: lsp.VersionedTextDocumentIdentifier{URI: sqlURI, Version: 3},
		ContentChanges: []lsp.TextDocumentContentChangeEvent{{
			Range: &syntax.Range{
				Start: fromPos,
				End:   syntax.Position{Line: fromPos.Line, Character: fromPos.Character + 4},
			},
			Text: "FROM",
		}},
	})
	_ = c.waitDiagnostics(sqlURI, cur)

	editDelta := c.call("textDocument/semanticTokens/full/delta", lsp.SemanticTokensDeltaParams{
		TextDocument:     lsp.TextDocumentIdentifier{URI: sqlURI},
		PreviousResultID: fullTok.ResultID,
	})
	require.Nil(t, editDelta.Error)
	var editMap map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(editDelta.Result, &editMap))
	assert.Contains(t, editMap, "edits")
	assert.NotContains(t, editMap, "data")

	// Range semantic tokens.
	rangeResp := c.call("textDocument/semanticTokens/range", lsp.SemanticTokensRangeParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: sqlURI},
		Range:        syntax.Range{Start: syntax.Position{Line: 0, Character: 0}, End: syntax.Position{Line: 0, Character: 10}},
	})
	require.Nil(t, rangeResp.Error)
	var rangeTok lsp.SemanticTokens
	require.NoError(t, json.Unmarshal(rangeResp.Result, &rangeTok))
	assert.NotNil(t, rangeTok.Data)

	// 8. Hover (card + null on whitespace).
	updatedSQL := "SELECT '😀', tenant_id FROM events WHERE event_date = '2026-01-01' AND tenant_id = 't1'"
	li2 := syntax.NewLineIndex(updatedSQL)
	colPos := li2.PositionAt(strings.Index(updatedSQL, "tenant_id"))
	hoverResp := c.call("textDocument/hover", lsp.HoverParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: sqlURI},
		Position:     colPos,
	})
	require.Nil(t, hoverResp.Error)
	var h analysis.Hover
	require.NoError(t, json.Unmarshal(hoverResp.Result, &h))
	assert.Contains(t, h.Contents.Value, "tenant_id")

	nullHover := c.call("textDocument/hover", lsp.HoverParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: sqlURI},
		Position:     syntax.Position{Line: 0, Character: 6},
	})
	require.Nil(t, nullHover.Error)
	assert.Equal(t, "null", strings.TrimSpace(string(nullHover.Result)))

	// 9. Completion, Definition, DocumentSymbol (including empty [] check).
	compResp := c.call("textDocument/completion", lsp.CompletionParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: sqlURI},
		Position:     colPos,
	})
	require.Nil(t, compResp.Error)
	var compList analysis.CompletionList
	require.NoError(t, json.Unmarshal(compResp.Result, &compList))
	assert.NotNil(t, compList.Items)

	defResp := c.call("textDocument/definition", lsp.DefinitionParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: sqlURI},
		Position:     colPos,
	})
	require.Nil(t, defResp.Error)
	assert.Equal(t, "[]", strings.TrimSpace(string(defResp.Result)))

	cteURI := pathToURI(filepath.Join(wsDir, "cte.sql"))
	cteSQL := "WITH my_cte AS (SELECT tenant_id FROM events WHERE event_date = '2026-01-01')\nSELECT tenant_id FROM my_cte"
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{URI: cteURI, LanguageID: "bqlang", Version: 1, Text: cteSQL},
	})
	cteDef := c.call("textDocument/definition", lsp.DefinitionParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: cteURI},
		Position:     syntax.Position{Line: 1, Character: len("SELECT tenant_id FROM ")},
	})
	require.Nil(t, cteDef.Error)
	var locs []analysis.Location
	require.NoError(t, json.Unmarshal(cteDef.Result, &locs))
	assert.NotEmpty(t, locs)

	symResp := c.call("textDocument/documentSymbol", lsp.DocumentSymbolParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: cteURI},
	})
	require.Nil(t, symResp.Error)
	var syms []analysis.DocumentSymbol
	require.NoError(t, json.Unmarshal(symResp.Result, &syms))
	assert.NotEmpty(t, syms)

	emptyURI := pathToURI(filepath.Join(wsDir, "empty.sql"))
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{URI: emptyURI, LanguageID: "bqlang", Version: 1, Text: ""},
	})
	emptySym := c.call("textDocument/documentSymbol", lsp.DocumentSymbolParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: emptyURI},
	})
	require.Nil(t, emptySym.Error)
	assert.Equal(t, "[]", strings.TrimSpace(string(emptySym.Result)))

	emptyFull := c.call("textDocument/semanticTokens/full", lsp.SemanticTokensParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: emptyURI},
	})
	require.Nil(t, emptyFull.Error)
	var emptyMap map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(emptyFull.Result, &emptyMap))
	assert.Equal(t, "[]", strings.TrimSpace(string(emptyMap["data"])))

	// 10. Formatting (UTF-16 range, unchanged -> [], syntax error -> []).
	fmtResp := c.call("textDocument/formatting", lsp.DocumentFormattingParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: sqlURI},
		Options:      lsp.FormattingOptions{TabSize: 4, InsertSpaces: true},
	})
	require.Nil(t, fmtResp.Error)
	var edits []lsp.TextEdit
	require.NoError(t, json.Unmarshal(fmtResp.Result, &edits))
	require.Len(t, edits, 1)
	assert.Equal(t, li2.RangeAt(0, len(updatedSQL)), edits[0].Range)

	// Syntax error formatting returns [] gracefully.
	brokenURI := pathToURI(filepath.Join(wsDir, "broken.sql"))
	curBrokenOpen := c.diagCount(brokenURI)
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{URI: brokenURI, LanguageID: "bqlang", Version: 1, Text: "SELECT ((( FROM WHERE"},
	})
	openBrokenDiag := c.waitDiagnostics(brokenURI, curBrokenOpen)
	assert.NotEmpty(t, openBrokenDiag.Diagnostics)
	brokenFmt := c.call("textDocument/formatting", lsp.DocumentFormattingParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: brokenURI},
		Options:      lsp.FormattingOptions{TabSize: 2, InsertSpaces: true},
	})
	require.Nil(t, brokenFmt.Error)
	assert.Equal(t, "[]", strings.TrimSpace(string(brokenFmt.Result)))

	// 11. didSave .bqfmt.toml reload: turn diagnostics_enabled = false, trigger refresh.
	updatedCfg := strings.ReplaceAll(mustReadFile(t, cfgPath), "diagnostics_enabled = true", "diagnostics_enabled = false")
	require.NoError(t, os.WriteFile(cfgPath, []byte(updatedCfg), 0o600))
	curBroken := c.diagCount(brokenURI)
	c.notify("textDocument/didSave", lsp.DidSaveTextDocumentParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: pathToURI(cfgPath)},
	})
	clearedDiag := c.waitDiagnostics(brokenURI, curBroken)
	assert.Empty(t, clearedDiag.Diagnostics)
	refreshReq := c.waitSemanticTokensRefresh()
	assert.Equal(t, "workspace/semanticTokens/refresh", refreshReq.Method)

	// 12. didClose clears diagnostics.
	curClose := c.diagCount(sqlURI)
	c.notify("textDocument/didClose", lsp.DidCloseTextDocumentParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: sqlURI},
	})
	closedDiag := c.waitDiagnostics(sqlURI, curClose)
	assert.Empty(t, closedDiag.Diagnostics)

	// 13. shutdown -> post-shutdown -32600 -> exit returns nil.
	shutResp := c.call("shutdown", nil)
	require.Nil(t, shutResp.Error)
	assert.Equal(t, "null", strings.TrimSpace(string(shutResp.Result)))

	postShut := c.call("textDocument/hover", lsp.HoverParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: cteURI},
		Position:     syntax.Position{},
	})
	require.NotNil(t, postShut.Error)
	assert.Equal(t, lsp.ErrCodeInvalidRequest, postShut.Error.Code)

	c.notify("exit", nil)
	select {
	case err := <-c.srvErr:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server exit")
	}
}

func TestServerCancelRequest(t *testing.T) {
	t.Parallel()

	wsDir, _ := setupTestWorkspace(t, "")
	c := startTestServer(t, lsp.ServerOptions{Version: "test-1.0"})

	initResp := c.call("initialize", defaultInitParams(wsDir))
	require.Nil(t, initResp.Error)
	c.notify("initialized", struct{}{})

	var lines []string
	for i := range 80 {
		lines = append(lines, fmt.Sprintf("SELECT tenant_id, %d AS seq FROM events WHERE event_date = '2026-01-01';", i))
	}

	uri := pathToURI(filepath.Join(wsDir, "cancel.sql"))
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{URI: uri, LanguageID: "bqlang", Version: 1, Text: strings.Join(lines, "\n")},
	})

	chNum := c.startRequest(json.RawMessage("42"), "textDocument/semanticTokens/full", lsp.SemanticTokensParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
	})
	c.notify("$/cancelRequest", lsp.CancelParams{ID: json.RawMessage("42")})

	select {
	case resp := <-chNum:
		if resp.Error != nil {
			assert.Equal(t, lsp.ErrCodeRequestCancelled, resp.Error.Code)
		} else {
			assert.NotEmpty(t, resp.Result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for numeric cancel response")
	}

	chStr := c.startRequest(json.RawMessage(`"req-cancel-str"`), "textDocument/completion", lsp.CompletionParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
		Position:     syntax.Position{Line: 0, Character: 7},
	})
	c.notify("$/cancelRequest", lsp.CancelParams{ID: json.RawMessage(`"req-cancel-str"`)})

	select {
	case resp := <-chStr:
		if resp.Error != nil {
			assert.Equal(t, lsp.ErrCodeRequestCancelled, resp.Error.Code)
		} else {
			assert.NotEmpty(t, resp.Result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for string cancel response")
	}

	// Unknown cancel ID must be a silent no-op.
	c.notify("$/cancelRequest", lsp.CancelParams{ID: json.RawMessage("99999")})
	hResp := c.call("textDocument/hover", lsp.HoverParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
		Position:     syntax.Position{Line: 0, Character: 7},
	})
	assert.Nil(t, hResp.Error)
}

func TestServerAsyncCatalogUpdateRefresh(t *testing.T) {
	t.Parallel()

	mockAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/projects/my-proj/datasets/analytics/tables/remote_events" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{
				"tableReference": {"projectId": "my-proj", "datasetId": "analytics", "tableId": "remote_events"},
				"type": "TABLE",
				"schema": {"fields": [{"name": "event_date", "type": "DATE"}, {"name": "user_id", "type": "STRING"}]},
				"timePartitioning": {"type": "DAY", "field": "event_date", "requirePartitionFilter": true}
			}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(mockAPI.Close)

	restClient := catalog.NewRestClient(
		catalog.WithBaseURL(mockAPI.URL),
		catalog.WithTokenProvider(func(context.Context) (string, error) { return "test-token", nil }),
	)

	wsDir := t.TempDir()
	cfgPath := filepath.Join(wsDir, ".bqfmt.toml")
	cfg := fmt.Sprintf(`
[lsp]
default_project = "my-proj"
default_dataset = "analytics"
cache_dir = %q
bigquery_api_enabled = true
diagnostics_enabled = true
require_partition_filter_severity = "warning"
`, filepath.ToSlash(filepath.Join(wsDir, ".cache")))
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfg), 0o600))

	c := startTestServer(t, lsp.ServerOptions{
		ConfigPath:   cfgPath,
		StoreOptions: []catalog.StoreOption{catalog.WithRestClient(restClient)},
	})

	initResp := c.call("initialize", defaultInitParams(wsDir))
	require.Nil(t, initResp.Error)
	c.notify("initialized", struct{}{})

	uri := pathToURI(filepath.Join(wsDir, "remote.sql"))
	cur := c.diagCount(uri)
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{
			URI:        uri,
			LanguageID: "bqlang",
			Version:    1,
			Text:       "SELECT user_id FROM remote_events",
		},
	})

	// Initial open suppresses unknown-table error while pending; async fetch completes and triggers refresh + warning.
	_ = c.waitDiagnostics(uri, cur)
	refreshReq := c.waitSemanticTokensRefresh()
	assert.Equal(t, "workspace/semanticTokens/refresh", refreshReq.Method)

	// Wait until refreshed diagnostics carry require_partition_filter warning for remote_events.
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.mu.Lock()
		list := append([]lsp.PublishDiagnosticsParams(nil), c.diagnostics[uri]...)
		c.mu.Unlock()

		foundWarning := false
		for _, ev := range list {
			for _, d := range ev.Diagnostics {
				if strings.Contains(d.Message, "event_date") || strings.Contains(d.Code, "partition") {
					foundWarning = true
				}
			}
		}
		if foundWarning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected async catalog refresh to emit partition filter diagnostic, got %+v", list)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestServerExitWithoutShutdownReturnsError(t *testing.T) {
	t.Parallel()

	c := startTestServer(t, lsp.ServerOptions{})
	c.notify("exit", nil)

	select {
	case err := <-c.srvErr:
		require.ErrorIs(t, err, lsp.ErrExitedWithoutShutdown)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for exit without shutdown")
	}
}

func TestServerMalformedConfigFallback(t *testing.T) {
	t.Parallel()

	wsDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(wsDir, ".bqfmt.toml"), []byte("[lsp\nbad_toml = ???"), 0o600))

	c := startTestServer(t, lsp.ServerOptions{})
	initResp := c.call("initialize", defaultInitParams(wsDir))
	require.Nil(t, initResp.Error)
	c.notify("initialized", struct{}{})

	uri := pathToURI(filepath.Join(wsDir, "ok.sql"))
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{URI: uri, LanguageID: "bqlang", Version: 1, Text: "select 1"},
	})

	fmtResp := c.call("textDocument/formatting", lsp.DocumentFormattingParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
		Options:      lsp.FormattingOptions{TabSize: 2, InsertSpaces: true},
	})
	require.Nil(t, fmtResp.Error)
	var edits []lsp.TextEdit
	require.NoError(t, json.Unmarshal(fmtResp.Result, &edits))
	assert.NotEmpty(t, edits)
}

func TestTransportFramingErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want error
	}{
		{name: "clean_eof", raw: "", want: io.EOF},
		{name: "unexpected_eof_mid_header", raw: "Content-Length: 5\r\n", want: io.ErrUnexpectedEOF},
		{name: "unexpected_eof_mid_payload", raw: "Content-Length: 5\r\n\r\n{\"a", want: io.ErrUnexpectedEOF},
		{name: "missing_content_length", raw: "Content-Type: application/json\r\n\r\n{}", want: lsp.ErrMissingContentLength},
		{name: "duplicate_content_length", raw: "Content-Length: 2\r\nContent-Length: 2\r\n\r\n{}", want: lsp.ErrInvalidHeader},
		{name: "invalid_content_length", raw: "Content-Length: -1\r\n\r\n", want: lsp.ErrInvalidContentLength},
		{name: "bare_lf", raw: "Content-Length: 2\n\n{}", want: lsp.ErrInvalidHeader},
		{name: "too_large", raw: "Content-Length: 16777217\r\n\r\n{}", want: lsp.ErrMessageTooLarge},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tr := lsp.NewTransport(bytes.NewBufferString(tc.raw), io.Discard)
			_, err := tr.ReadFrame()
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func TestServerRefreshDoesNotPublishAfterDidCloseOrDidChange(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	var startOnce sync.Once
	gate := make(chan struct{})

	mockAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/projects/my-proj/datasets/analytics/tables/remote_events" {
			startOnce.Do(func() { close(started) })
			<-gate
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"tableReference":{"projectId":"my-proj","datasetId":"analytics","tableId":"remote_events"},"type":"TABLE","schema":{"fields":[{"name":"event_date","type":"DATE"},{"name":"user_id","type":"STRING"}]},"timePartitioning":{"type":"DAY","field":"event_date","requirePartitionFilter":true}}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(mockAPI.Close)

	restClient := catalog.NewRestClient(
		catalog.WithBaseURL(mockAPI.URL),
		catalog.WithHTTPClient(mockAPI.Client()),
		catalog.WithTokenProvider(func(context.Context) (string, error) { return "tok", nil }),
	)
	wsDir := t.TempDir()
	cfgPath := filepath.Join(wsDir, ".bqfmt.toml")
	cfgText := fmt.Sprintf(
		"[lsp]\ndefault_project = \"my-proj\"\ndefault_dataset = \"analytics\"\ncache_dir = %q\nbigquery_api_enabled = true\n",
		filepath.ToSlash(filepath.Join(wsDir, ".cache")),
	)
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfgText), 0o600))

	c := startTestServer(t, lsp.ServerOptions{
		ConfigPath:   cfgPath,
		StoreOptions: []catalog.StoreOption{catalog.WithRestClient(restClient)},
	})
	require.Nil(t, c.call("initialize", defaultInitParams(wsDir)).Error)
	c.notify("initialized", struct{}{})

	closeURI := pathToURI(filepath.Join(wsDir, "close_race.sql"))
	changeURI := pathToURI(filepath.Join(wsDir, "change_race.sql"))

	curClose := c.diagCount(closeURI)
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{URI: closeURI, LanguageID: "bqlang", Version: 1, Text: "SELECT user_id FROM remote_events"},
	})
	_ = c.waitDiagnostics(closeURI, curClose)

	curChange := c.diagCount(changeURI)
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{URI: changeURI, LanguageID: "bqlang", Version: 1, Text: "SELECT user_id FROM remote_events"},
	})
	_ = c.waitDiagnostics(changeURI, curChange)

	// Wait until the background catalog fetch is actively in-flight, then race
	// catalog completion against didClose and didChange(v2).
	<-started

	close(gate)
	c.notify("textDocument/didClose", lsp.DidCloseTextDocumentParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: closeURI},
	})
	c.notify("textDocument/didChange", lsp.DidChangeTextDocumentParams{
		TextDocument: lsp.VersionedTextDocumentIdentifier{URI: changeURI, Version: 2},
		ContentChanges: []lsp.TextDocumentContentChangeEvent{
			{Text: "SELECT user_id FROM remote_events WHERE event_date = '2026-01-01'"},
		},
	})

	// Wait for workspace/semanticTokens/refresh, which is emitted strictly after
	// queued diagnostic refresh finishes on the dispatcher.
	_ = c.waitSemanticTokensRefresh()

	// Wait for both didClose (Version == nil) and didChange (Version == 2)
	// notifications to arrive, then flush with a synchronous hover round-trip.
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.mu.Lock()
		cl := c.diagnostics[closeURI]
		ch := c.diagnostics[changeURI]
		sawClose := len(cl) > 0 && cl[len(cl)-1].Version == nil
		sawV2 := len(ch) > 0 && ch[len(ch)-1].Version != nil && *ch[len(ch)-1].Version == 2
		c.mu.Unlock()
		if sawClose && sawV2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for didClose/didChange v2 diagnostics (close=%+v change=%+v)", cl, ch)
		}
		select {
		case <-c.diagSig:
		case <-time.After(20 * time.Millisecond):
		}
	}

	_ = c.call("textDocument/hover", lsp.HoverParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: changeURI},
		Position:     syntax.Position{Line: 0, Character: 7},
	})

	c.mu.Lock()
	closeList := append([]lsp.PublishDiagnosticsParams(nil), c.diagnostics[closeURI]...)
	changeList := append([]lsp.PublishDiagnosticsParams(nil), c.diagnostics[changeURI]...)
	c.mu.Unlock()

	require.NotEmpty(t, closeList)
	assert.Empty(t, closeList[len(closeList)-1].Diagnostics, "stale refresh must not resurrect diagnostics after didClose")

	require.NotEmpty(t, changeList)
	maxVer := 0
	for _, ev := range changeList {
		require.NotNil(t, ev.Version)
		assert.GreaterOrEqual(t, *ev.Version, maxVer, "diagnostics version regressed: %+v", changeList)
		maxVer = *ev.Version
	}
	assert.Equal(t, 2, maxVer)
	assert.Empty(t, changeList[len(changeList)-1].Diagnostics)
}

func TestServerErrorMappingDidSaveTextSyncAndMissingParams(t *testing.T) {
	t.Parallel()

	wsDir, _ := setupTestWorkspace(t, "")
	customCfgPath := filepath.Join(wsDir, "custom-bqpls.toml")
	require.NoError(t, os.WriteFile(customCfgPath, []byte("[lsp]\ndefault_project = \"my-proj\"\ndefault_dataset = \"analytics\"\nschema_dirs = [\""+filepath.ToSlash(filepath.Join(wsDir, "schemas"))+"\"]\ndiagnostics_enabled = true\n"), 0o600))

	c := startTestServer(t, lsp.ServerOptions{
		ConfigPath: customCfgPath,
		Version:    "test-1.0",
	})
	require.Nil(t, c.call("initialize", defaultInitParams(wsDir)).Error)
	c.notify("initialized", struct{}{})

	missingURI := pathToURI(filepath.Join(wsDir, "not_open.sql"))
	for _, tc := range []struct {
		method string
		params any
	}{
		{"textDocument/hover", lsp.HoverParams{TextDocument: lsp.TextDocumentIdentifier{URI: missingURI}}},
		{"textDocument/completion", lsp.CompletionParams{TextDocument: lsp.TextDocumentIdentifier{URI: missingURI}}},
		{"textDocument/definition", lsp.DefinitionParams{TextDocument: lsp.TextDocumentIdentifier{URI: missingURI}}},
		{"textDocument/documentSymbol", lsp.DocumentSymbolParams{TextDocument: lsp.TextDocumentIdentifier{URI: missingURI}}},
		{"textDocument/semanticTokens/full", lsp.SemanticTokensParams{TextDocument: lsp.TextDocumentIdentifier{URI: missingURI}}},
		{"textDocument/semanticTokens/full/delta", lsp.SemanticTokensDeltaParams{TextDocument: lsp.TextDocumentIdentifier{URI: missingURI}, PreviousResultID: "1"}},
		{"textDocument/semanticTokens/range", lsp.SemanticTokensRangeParams{TextDocument: lsp.TextDocumentIdentifier{URI: missingURI}}},
		{"textDocument/formatting", lsp.DocumentFormattingParams{TextDocument: lsp.TextDocumentIdentifier{URI: missingURI}}},
	} {
		resp := c.call(tc.method, tc.params)
		require.NotNil(t, resp.Error, "method=%s", tc.method)
		assert.Equal(t, lsp.ErrCodeInvalidParams, resp.Error.Code, "method=%s", tc.method)
	}

	// Malformed params -> -32602 across handlers.
	for _, method := range []string{
		"textDocument/hover",
		"textDocument/signatureHelp",
		"textDocument/completion",
		"textDocument/definition",
		"textDocument/documentSymbol",
		"textDocument/semanticTokens/full",
		"textDocument/semanticTokens/full/delta",
		"textDocument/semanticTokens/range",
		"textDocument/formatting",
	} {
		bad := c.call(method, "not-an-object")
		require.NotNil(t, bad.Error, "method=%s", method)
		assert.Equal(t, lsp.ErrCodeInvalidParams, bad.Error.Code, "method=%s", method)
	}

	// Missing params key altogether -> -32602 ("missing params").
	chMissing := make(chan lsp.RPCMessage, 1)
	c.mu.Lock()
	c.pending["n:888"] = chMissing
	c.mu.Unlock()
	require.NoError(t, c.tr.WriteFrame([]byte(`{"jsonrpc":"2.0","id":888,"method":"textDocument/hover"}`)))
	missingParamsResp := <-chMissing
	require.NotNil(t, missingParamsResp.Error)
	assert.Equal(t, lsp.ErrCodeInvalidParams, missingParamsResp.Error.Code)
	assert.Contains(t, missingParamsResp.Error.Message, "missing params")

	// didSave with Text populated updates buffer and clears partition filter warning.
	uri := pathToURI(filepath.Join(wsDir, "save_sync.sql"))
	cur := c.diagCount(uri)
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{URI: uri, LanguageID: "bqlang", Version: 1, Text: "SELECT tenant_id FROM events"},
	})
	d1 := c.waitDiagnostics(uri, cur)
	require.NotEmpty(t, d1.Diagnostics)

	// didSave with same text keeps diagnostics intact.
	sameText := "SELECT tenant_id FROM events"
	cur = c.diagCount(uri)
	c.notify("textDocument/didSave", lsp.DidSaveTextDocumentParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
		Text:         &sameText,
	})
	dSame := c.waitDiagnostics(uri, cur)
	require.NotEmpty(t, dSame.Diagnostics)

	// didSave with updated text syncs engine + bumps version + clears warning.
	cur = c.diagCount(uri)
	fixed := "select tenant_id from events where event_date = '2026-01-01' and tenant_id = 't1'"
	c.notify("textDocument/didSave", lsp.DidSaveTextDocumentParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
		Text:         &fixed,
	})
	d2 := c.waitDiagnostics(uri, cur)
	assert.Empty(t, d2.Diagnostics)

	// Formatting sees the synced text from didSave.
	fmtResp := c.call("textDocument/formatting", lsp.DocumentFormattingParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
		Options:      lsp.FormattingOptions{TabSize: 2, InsertSpaces: true},
	})
	require.Nil(t, fmtResp.Error)
	var edits []lsp.TextEdit
	require.NoError(t, json.Unmarshal(fmtResp.Result, &edits))
	require.Len(t, edits, 1)
	assert.Contains(t, edits[0].NewText, "SELECT")
	assert.Contains(t, edits[0].NewText, "event_date")

	// Saving the explicit custom config path reloads open docs and triggers semanticTokens/refresh.
	updatedCfg := strings.ReplaceAll(mustReadFile(t, customCfgPath), "diagnostics_enabled = true", "diagnostics_enabled = false")
	require.NoError(t, os.WriteFile(customCfgPath, []byte(updatedCfg), 0o600))
	cur = c.diagCount(uri)
	c.notify("textDocument/didSave", lsp.DidSaveTextDocumentParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: pathToURI(customCfgPath)},
	})
	_ = c.waitDiagnostics(uri, cur)
	refreshReq := c.waitSemanticTokensRefresh()
	assert.Equal(t, "workspace/semanticTokens/refresh", refreshReq.Method)
}

func TestServerSignatureHelpAndCapabilities(t *testing.T) {
	t.Parallel()

	c := startTestServer(t, lsp.ServerOptions{})

	initResp := c.call("initialize", lsp.InitializeParams{
		Capabilities: lsp.ClientCapabilities{
			TextDocument: &lsp.TextDocumentClientCapabilities{
				SignatureHelp: &lsp.SignatureHelpClientCapabilities{
					SignatureInformation: &lsp.SignatureInformationClientCapabilities{
						ParameterInformation: &lsp.ParameterInformationClientCapabilities{
							LabelOffsetSupport: true,
						},
					},
				},
			},
		},
	})
	require.Nil(t, initResp.Error)

	var initRes lsp.InitializeResult
	require.NoError(t, json.Unmarshal(initResp.Result, &initRes))
	require.NotNil(t, initRes.Capabilities.SignatureHelpProvider)
	assert.Contains(t, initRes.Capabilities.SignatureHelpProvider.TriggerCharacters, "(")
	assert.Contains(t, initRes.Capabilities.SignatureHelpProvider.TriggerCharacters, ",")

	c.notify("initialized", map[string]any{})

	uri := "file:///sighelp_server_test.sql"
	sql := "SELECT DATE_TRUNC(event_date, DAY), AI.GENERATE(prompt => 'hi') FROM tbl"
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{
			URI:        uri,
			LanguageID: "bqlang",
			Version:    1,
			Text:       sql,
		},
	})

	// Inside DATE_TRUNC(..., |DAY) -> activeParameter 1 with UTF-16 offset pairs.
	posIdx := strings.Index(sql, "DAY")
	require.Positive(t, posIdx)

	shResp := c.call("textDocument/signatureHelp", lsp.SignatureHelpParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
		Position:     syntax.NewLineIndex(sql).PositionAt(posIdx),
	})
	require.Nil(t, shResp.Error)

	var sh analysis.SignatureHelp
	require.NoError(t, json.Unmarshal(shResp.Result, &sh))
	require.NotEmpty(t, sh.Signatures)
	require.NotNil(t, sh.ActiveSignature)
	require.NotNil(t, sh.ActiveParameter)
	assert.Equal(t, 1, *sh.ActiveParameter)

	activeSig := sh.Signatures[*sh.ActiveSignature]
	assert.Contains(t, activeSig.Label, "DATE_TRUNC(")
	require.NotNil(t, activeSig.Documentation)
	assert.Contains(t, activeSig.Documentation.Value, "Truncates")
	require.GreaterOrEqual(t, len(activeSig.Parameters), 2)
	// Because client advertised labelOffsetSupport=true, parameter label marshals as [start, end].
	arr, isArr := activeSig.Parameters[1].Label.([]any)
	require.True(t, isArr, "expected JSON [start, end] offset array when labelOffsetSupport is true")
	assert.Len(t, arr, 2)

	// Outside any function call -> JSON null.
	outsideResp := c.call("textDocument/signatureHelp", lsp.SignatureHelpParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uri},
		Position:     syntax.Position{Line: 0, Character: 0},
	})
	require.Nil(t, outsideResp.Error)
	assert.Equal(t, "null", strings.TrimSpace(string(outsideResp.Result)))
}

func TestServerExtensionSettingsAndConfigFilePrecedence(t *testing.T) {
	t.Parallel()

	wsDir := t.TempDir()
	extSchemaDir := filepath.Join(wsDir, "ext_schemas")
	fileSchemaDir := filepath.Join(wsDir, "file_schemas")
	require.NoError(t, os.MkdirAll(extSchemaDir, 0o750))
	require.NoError(t, os.MkdirAll(fileSchemaDir, 0o750))

	extTableJSON := `{
		"tableReference": {"projectId": "ext-proj", "datasetId": "ext_ds", "tableId": "orders"},
		"type": "TABLE",
		"description": "Orders from extension settings",
		"timePartitioning": {"type": "DAY", "field": "order_date", "requirePartitionFilter": true},
		"schema": {"fields": [
			{"name": "order_date", "type": "DATE", "mode": "REQUIRED"},
			{"name": "total", "type": "NUMERIC", "mode": "NULLABLE"}
		]}
	}`
	require.NoError(t, os.WriteFile(filepath.Join(extSchemaDir, "ext-proj.ext_ds.orders.json"), []byte(extTableJSON), 0o600))

	fileTableJSON := `{
		"tableReference": {"projectId": "file-proj", "datasetId": "ext_ds", "tableId": "orders"},
		"type": "TABLE",
		"description": "Orders from bqfmt.toml override",
		"schema": {"fields": [
			{"name": "order_date", "type": "DATE", "mode": "REQUIRED"},
			{"name": "file_only_col", "type": "STRING", "mode": "NULLABLE"}
		]}
	}`
	require.NoError(t, os.WriteFile(filepath.Join(fileSchemaDir, "file-proj.ext_ds.orders.json"), []byte(fileTableJSON), 0o600))

	// Case 1: No .bqfmt.toml -> extension initializationOptions drive schema resolution and hover.
	c := startTestServer(t, lsp.ServerOptions{})
	initOpts, err := json.Marshal(map[string]any{
		"defaultProject":                 "ext-proj",
		"defaultDataset":                 "ext_ds",
		"schemaDirs":                     []string{"ext_schemas"},
		"bigqueryApiEnabled":             false,
		"diagnosticsEnabled":             true,
		"requirePartitionFilterSeverity": "error",
	})
	require.NoError(t, err)

	initResp := c.call("initialize", lsp.InitializeParams{
		RootURI:               pathToURI(wsDir),
		InitializationOptions: initOpts,
	})
	require.Nil(t, initResp.Error)
	c.notify("initialized", map[string]any{})

	noCfgDir := filepath.Join(wsDir, "no_cfg")
	require.NoError(t, os.MkdirAll(noCfgDir, 0o750))
	uriNoCfg := pathToURI(filepath.Join(noCfgDir, "q.sql"))
	sql := "SELECT total FROM orders"
	cur := c.diagCount(uriNoCfg)
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{URI: uriNoCfg, LanguageID: "bqlang", Version: 1, Text: sql},
	})
	diag1 := c.waitDiagnostics(uriNoCfg, cur)
	require.NotEmpty(t, diag1.Diagnostics, "missing partition filter should trigger diagnostic from extension settings")
	assert.Equal(t, analysis.SeverityError, diag1.Diagnostics[0].Severity)

	hoverResp := c.call("textDocument/hover", lsp.HoverParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uriNoCfg},
		Position:     syntax.Position{Line: 0, Character: strings.Index(sql, "orders")},
	})
	require.Nil(t, hoverResp.Error)
	var h analysis.Hover
	require.NoError(t, json.Unmarshal(hoverResp.Result, &h))
	assert.Contains(t, h.Contents.Value, "Orders from extension settings")

	// Case 2: Subdirectory with .bqfmt.toml overrides default_project & schema_dirs while inheriting default_dataset.
	subDir := filepath.Join(wsDir, "with_cfg")
	require.NoError(t, os.MkdirAll(subDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(subDir, ".bqfmt.toml"), []byte(fmt.Sprintf(`
[lsp]
default_project = "file-proj"
schema_dirs = [%q]
`, fileSchemaDir)), 0o600))

	uriWithCfg := pathToURI(filepath.Join(subDir, "q.sql"))
	sqlSub := "SELECT file_only_col FROM orders"
	curSub := c.diagCount(uriWithCfg)
	c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{URI: uriWithCfg, LanguageID: "bqlang", Version: 1, Text: sqlSub},
	})
	_ = c.waitDiagnostics(uriWithCfg, curSub)

	hoverSubResp := c.call("textDocument/hover", lsp.HoverParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: uriWithCfg},
		Position:     syntax.Position{Line: 0, Character: strings.Index(sqlSub, "orders")},
	})
	require.Nil(t, hoverSubResp.Error)
	var hSub analysis.Hover
	require.NoError(t, json.Unmarshal(hoverSubResp.Result, &hSub))
	assert.Contains(t, hSub.Contents.Value, "Orders from bqfmt.toml override")
	assert.Contains(t, hSub.Contents.Value, "file-proj.ext_ds.orders")

	// Case 3: Live workspace/didChangeConfiguration disables diagnostics for the doc without .bqfmt.toml.
	updatedSettings, err := json.Marshal(map[string]any{
		"bqpls": map[string]any{
			"defaultProject":     "ext-proj",
			"defaultDataset":     "ext_ds",
			"schemaDirs":         []string{"ext_schemas"},
			"diagnosticsEnabled": false,
		},
	})
	require.NoError(t, err)

	cur = c.diagCount(uriNoCfg)
	c.notify("workspace/didChangeConfiguration", lsp.DidChangeConfigurationParams{
		Settings: updatedSettings,
	})
	diagCleared := c.waitDiagnostics(uriNoCfg, cur)
	assert.Empty(t, diagCleared.Diagnostics, "live didChangeConfiguration should disable diagnostics")
}
