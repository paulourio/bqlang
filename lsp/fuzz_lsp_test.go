package lsp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/lsp"
)

func framePayload(body string) []byte {
	return fmt.Appendf(nil, "Content-Length: %d\r\n\r\n%s", len(body), body)
}

var adversarialFramingSeeds = [][]byte{
	[]byte(""),
	framePayload(`{"jsonrpc":"2.0","id":1,"method":"shutdown"}`),
	append(
		framePayload(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`),
		framePayload(`{"jsonrpc":"2.0","method":"initialized","params":{}}`)...,
	),
	[]byte("content-length: 2\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\nX-Custom: ok\r\n\r\n{}"),
	[]byte("Content-Length: 0\r\n\r\n"),
	[]byte("Content-Type: application/json\r\n\r\n{}"),
	[]byte("Content-Length: 2\r\nContent-Length: 2\r\n\r\n{}"),
	[]byte("Content-Length: -1\r\n\r\n"),
	[]byte("Content-Length: +5\r\n\r\nhello"),
	[]byte("Content-Length: abc\r\n\r\n"),
	[]byte("Content-Length: 99999999999999999999\r\n\r\n"),
	[]byte("Content-Length: 16777217\r\n\r\n{}"),
	[]byte("Content-Length: 16000000\r\n\r\n{}"),
	[]byte("Content-Length: 2\n\n{}"),
	[]byte("BadHeaderWithoutColon\r\n\r\n"),
	[]byte("Content-Length: 5\r\n"),
	[]byte("Content-Length: 5\r\n\r\n{\"a"),
	[]byte("\x00\xff\xfe\r\n\r\n\x00"),
}

func FuzzTransportFraming(f *testing.F) {
	for _, seed := range adversarialFramingSeeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 16384 {
			return
		}

		tr := lsp.NewTransport(bytes.NewReader(raw), io.Discard)
		const maxFrames = 64

		for range maxFrames {
			frame, err := tr.ReadFrame()
			if err != nil {
				return
			}

			var out bytes.Buffer
			wtr := lsp.NewTransport(io.LimitReader(nil, 0), &out)
			require.NoError(t, wtr.WriteFrame(frame))

			rtr := lsp.NewTransport(&out, io.Discard)
			roundTrip, rtErr := rtr.ReadFrame()
			require.NoError(t, rtErr)
			require.Equal(t, frame, roundTrip)
		}
	})
}

var adversarialRPCSeeds = [][]byte{
	[]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{}}}`),
	[]byte(`{"jsonrpc":"2.0","method":"initialized","params":{}}`),
	[]byte(`{"jsonrpc":"2.0","id":"req-1","method":"shutdown"}`),
	[]byte(`{"jsonrpc":"2.0","method":"exit"}`),
	[]byte(`{"jsonrpc":"2.0","method":"$/cancelRequest","params":{"id":42}}`),
	[]byte(`{"jsonrpc":"2.0","method":"$/cancelRequest","params":{"id":"str-id"}}`),
	[]byte(`{"jsonrpc":"2.0","method":"$/cancelRequest","params":{"id":null}}`),
	[]byte(`{"jsonrpc":"2.0","method":"textDocument/didOpen","params":{"textDocument":{"uri":"file:///a.sql","languageId":"bqlang","version":1,"text":"SELECT 1"}}}`),
	[]byte(`{"jsonrpc":"2.0","method":"textDocument/didChange","params":{"textDocument":{"uri":"file:///a.sql","version":2},"contentChanges":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":6}},"text":"select"}]}}`),
	[]byte(`{"jsonrpc":"2.0","method":"textDocument/didSave","params":{"textDocument":{"uri":"file:///a.sql"}}}`),
	[]byte(`{"jsonrpc":"2.0","method":"textDocument/didClose","params":{"textDocument":{"uri":"file:///a.sql"}}}`),
	[]byte(`{"jsonrpc":"2.0","id":2,"method":"textDocument/hover","params":{"textDocument":{"uri":"file:///a.sql"},"position":{"line":0,"character":1}}}`),
	[]byte(`{"jsonrpc":"2.0","id":3,"method":"textDocument/completion","params":{"textDocument":{"uri":"file:///a.sql"},"position":{"line":0,"character":1}}}`),
	[]byte(`{"jsonrpc":"2.0","id":4,"method":"textDocument/definition","params":{"textDocument":{"uri":"file:///a.sql"},"position":{"line":0,"character":1}}}`),
	[]byte(`{"jsonrpc":"2.0","id":5,"method":"textDocument/documentSymbol","params":{"textDocument":{"uri":"file:///a.sql"}}}`),
	[]byte(`{"jsonrpc":"2.0","id":6,"method":"textDocument/formatting","params":{"textDocument":{"uri":"file:///a.sql"},"options":{"tabSize":2,"insertSpaces":true}}}`),
	[]byte(`{"jsonrpc":"2.0","id":7,"method":"textDocument/semanticTokens/full","params":{"textDocument":{"uri":"file:///a.sql"}}}`),
	[]byte(`{"jsonrpc":"2.0","id":8,"method":"textDocument/semanticTokens/full/delta","params":{"textDocument":{"uri":"file:///a.sql"},"previousResultId":"r1"}}`),
	[]byte(`{"jsonrpc":"2.0","id":9,"method":"textDocument/semanticTokens/range","params":{"textDocument":{"uri":"file:///a.sql"},"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":5}}}}`),
	[]byte(`{"jsonrpc":"2.0","id":10,"result":null}`),
	[]byte(`{invalid!`),
	[]byte(`[]`),
	[]byte(`123`),
	[]byte(`"str"`),
	[]byte(`null`),
	[]byte(`{"jsonrpc":"1.0","id":1,"method":"initialize"}`),
	[]byte(`{"jsonrpc":"2.0","id":true,"method":"initialize"}`),
	[]byte(`{"jsonrpc":"2.0","id":[],"method":"initialize"}`),
	[]byte(`{"jsonrpc":"2.0","id":{},"method":"initialize"}`),
	[]byte(`{"jsonrpc":"2.0","id":1e309,"method":"initialize"}`),
	[]byte(`{"jsonrpc":"2.0","id":1,"method":"textDocument/hover","params":"bad"}`),
}

func FuzzRPCDecodeAndID(f *testing.F) {
	for _, seed := range adversarialRPCSeeds {
		f.Add(seed)
	}

	k1, kind1 := lsp.ClassifyAndNormalizeID(json.RawMessage("1"))
	k2, kind2 := lsp.ClassifyAndNormalizeID(json.RawMessage(`"1"`))
	require.Equal(f, lsp.IDNumber, kind1)
	require.Equal(f, lsp.IDString, kind2)
	require.NotEqual(f, k1, k2)

	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > 8192 {
			return
		}

		msg, kind, rpcErr := lsp.DecodeRPCMessage(payload)
		_, _ = lsp.ClassifyAndNormalizeID(msg.ID)

		trimmed := bytes.TrimSpace(payload)
		if len(trimmed) == 0 || !json.Valid(trimmed) {
			require.NotNil(t, rpcErr)
			require.Equal(t, lsp.ErrCodeParseError, rpcErr.Code)
			require.Equal(t, lsp.MessageKindInvalid, kind)
			return
		}

		if trimmed[0] != '{' {
			require.NotNil(t, rpcErr)
			require.Equal(t, lsp.ErrCodeInvalidRequest, rpcErr.Code)
			require.Equal(t, lsp.MessageKindInvalid, kind)
			return
		}

		if rpcErr == nil && kind == lsp.MessageKindRequest {
			switch msg.Method {
			case "initialize":
				var p lsp.InitializeParams
				_ = json.Unmarshal(msg.Params, &p)
			case "textDocument/hover":
				var p lsp.HoverParams
				_ = json.Unmarshal(msg.Params, &p)
			case "textDocument/completion":
				var p lsp.CompletionParams
				_ = json.Unmarshal(msg.Params, &p)
			case "textDocument/definition":
				var p lsp.DefinitionParams
				_ = json.Unmarshal(msg.Params, &p)
			case "textDocument/documentSymbol":
				var p lsp.DocumentSymbolParams
				_ = json.Unmarshal(msg.Params, &p)
			case "textDocument/formatting":
				var p lsp.DocumentFormattingParams
				_ = json.Unmarshal(msg.Params, &p)
			case "textDocument/semanticTokens/full":
				var p lsp.SemanticTokensParams
				_ = json.Unmarshal(msg.Params, &p)
			case "textDocument/semanticTokens/full/delta":
				var p lsp.SemanticTokensDeltaParams
				_ = json.Unmarshal(msg.Params, &p)
			case "textDocument/semanticTokens/range":
				var p lsp.SemanticTokensRangeParams
				_ = json.Unmarshal(msg.Params, &p)
			}
		}
	})
}

func buildSessionSeeds() [][]byte {
	var fullSession bytes.Buffer
	for _, msg := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{}}}`,
		`{"jsonrpc":"2.0","method":"initialized","params":{}}`,
		`{"jsonrpc":"2.0","method":"textDocument/didOpen","params":{"textDocument":{"uri":"untitled:fuzz.sql","languageId":"bqlang","version":1,"text":"WITH c AS (SELECT 1 AS a) SELECT a FROM c"}}}`,
		`{"jsonrpc":"2.0","method":"textDocument/didChange","params":{"textDocument":{"uri":"untitled:fuzz.sql","version":2},"contentChanges":[{"text":"SELECT 1 + 2"}]}}`,
		`{"jsonrpc":"2.0","id":2,"method":"textDocument/hover","params":{"textDocument":{"uri":"untitled:fuzz.sql"},"position":{"line":0,"character":7}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"textDocument/completion","params":{"textDocument":{"uri":"untitled:fuzz.sql"},"position":{"line":0,"character":7}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"textDocument/semanticTokens/full","params":{"textDocument":{"uri":"untitled:fuzz.sql"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"textDocument/semanticTokens/full/delta","params":{"textDocument":{"uri":"untitled:fuzz.sql"},"previousResultId":"missing"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"textDocument/semanticTokens/range","params":{"textDocument":{"uri":"untitled:fuzz.sql"},"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":6}}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"textDocument/formatting","params":{"textDocument":{"uri":"untitled:fuzz.sql"},"options":{"tabSize":2,"insertSpaces":true}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"textDocument/definition","params":{"textDocument":{"uri":"untitled:fuzz.sql"},"position":{"line":0,"character":7}}}`,
		`{"jsonrpc":"2.0","id":9,"method":"textDocument/documentSymbol","params":{"textDocument":{"uri":"untitled:fuzz.sql"}}}`,
		`{"jsonrpc":"2.0","id":10,"method":"shutdown"}`,
		`{"jsonrpc":"2.0","method":"exit"}`,
	} {
		fullSession.Write(framePayload(msg))
	}

	var cancelSession bytes.Buffer
	for _, msg := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"textDocument/hover","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"capabilities":{}}}`,
		`{"jsonrpc":"2.0","method":"initialized","params":{}}`,
		`{"jsonrpc":"2.0","method":"$/cancelRequest","params":{"id":999}}`,
		`{"jsonrpc":"2.0","method":"textDocument/didClose","params":{"textDocument":{"uri":"untitled:missing.sql"}}}`,
		`{"jsonrpc":"2.0","method":"exit"}`,
	} {
		cancelSession.Write(framePayload(msg))
	}

	var corruptRecovery bytes.Buffer
	corruptRecovery.Write(framePayload(`{bad}`))
	corruptRecovery.Write(framePayload(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{}}}`))
	corruptRecovery.Write(framePayload(`{"jsonrpc":"2.0","id":2,"method":"shutdown"}`))
	corruptRecovery.Write(framePayload(`{"jsonrpc":"2.0","method":"exit"}`))

	return [][]byte{
		fullSession.Bytes(),
		cancelSession.Bytes(),
		corruptRecovery.Bytes(),
	}
}

func FuzzServerSession(f *testing.F) {
	require.NoError(f, bqlang.Init())

	for _, seed := range buildSessionSeeds() {
		f.Add(seed)
	}

	discardLogger := slog.New(slog.DiscardHandler)

	f.Fuzz(func(t *testing.T, stream []byte) {
		if len(stream) > 8192 {
			return
		}

		srv, err := lsp.NewServer(lsp.ServerOptions{
			Logger:  discardLogger,
			Version: "fuzz",
		})
		require.NoError(t, err)

		var outBuf bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = srv.Serve(ctx, bytes.NewReader(stream), &outBuf)
		cancel()
		_ = srv.Close()

		outTr := lsp.NewTransport(&outBuf, io.Discard)
		for {
			frame, ferr := outTr.ReadFrame()
			if errors.Is(ferr, io.EOF) {
				break
			}

			require.NoError(t, ferr, "server emitted malformed outbound framing")
			require.True(t, json.Valid(frame), "server emitted invalid JSON payload")
		}
	})
}
