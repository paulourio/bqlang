package e2e_test

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

const (
	tokNamespace uint32 = iota
	tokClass
	tokFunction
	tokVariable
	tokProperty
	tokParameter
	tokKeyword
	tokType
	tokString
	tokNumber
	tokComment
	tokOperator
	tokMacro
	tokEnumMember
)

const (
	modDeclaration uint32 = 1 << iota
	modReadonly
	modDefaultLibrary
	modPartitioned
	modClustered
	modPseudo
	modModification
)

var (
	canonicalTokenTypes = []string{
		"namespace",
		"class",
		"function",
		"variable",
		"property",
		"parameter",
		"keyword",
		"type",
		"string",
		"number",
		"comment",
		"operator",
		"macro",
		"enumMember",
	}
	canonicalTokenModifiers = []string{
		"declaration",
		"readonly",
		"defaultLibrary",
		"partitioned",
		"clustered",
		"pseudo",
		"modification",
	}
)

var sharedBinDir string

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	dir, err := os.MkdirTemp("", "bqlang-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e TestMain MkdirTemp: %v\n", err)

		return 1
	}
	defer os.RemoveAll(dir)

	sharedBinDir = dir

	return m.Run()
}

var (
	repoRoot = sync.OnceValues(findRepoRoot)
	bqfmtBin = sync.OnceValues(func() (string, error) {
		return buildBinary("bqfmt")
	})
	bqplsBin = sync.OnceValues(func() (string, error) {
		return buildBinary("bqpls")
	})
)

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}

	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not locate go.mod from working directory")
		}

		dir = parent
	}
}

func buildBinary(name string) (string, error) {
	root, err := repoRoot()
	if err != nil {
		return "", err
	}

	outPath := filepath.Join(sharedBinDir, name)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build", "-o", outPath, "./cmd/"+name)
	cmd.Dir = root

	out, buildErr := cmd.CombinedOutput()
	if buildErr != nil {
		return "", fmt.Errorf("go build ./cmd/%s: %w\n%s", name, buildErr, string(out))
	}

	return outPath, nil
}

func requireBqfmt(t *testing.T) string {
	t.Helper()

	bin, err := bqfmtBin()
	require.NoError(t, err, "build ./cmd/bqfmt")

	return bin
}

func requireBqpls(t *testing.T) string {
	t.Helper()

	bin, err := bqplsBin()
	require.NoError(t, err, "build ./cmd/bqpls")

	return bin
}

type envPaths struct {
	home     string
	xdgData  string
	xdgCfg   string
	xdgState string
	xdgCache string
	env      []string
}

func newIsolatedEnv(t *testing.T, extra ...string) envPaths {
	t.Helper()

	base := t.TempDir()
	home := filepath.Join(base, "home")
	xdgData := filepath.Join(base, "xdg-data")
	xdgCfg := filepath.Join(base, "xdg-config")
	xdgState := filepath.Join(base, "xdg-state")
	xdgCache := filepath.Join(base, "xdg-cache")

	for _, d := range []string{home, xdgData, xdgCfg, xdgState, xdgCache} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}

	envMap := make(map[string]string)

	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			envMap[k] = v
		}
	}

	envMap["HOME"] = home
	envMap["XDG_DATA_HOME"] = xdgData
	envMap["XDG_CONFIG_HOME"] = xdgCfg
	envMap["XDG_STATE_HOME"] = xdgState
	envMap["XDG_CACHE_HOME"] = xdgCache
	envMap["GOOGLE_APPLICATION_CREDENTIALS"] = filepath.Join(base, "nonexistent-adc.json")
	envMap["CLOUDSDK_CONFIG"] = filepath.Join(base, "nonexistent-gcloud")

	for _, kv := range extra {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			envMap[k] = v
		}
	}

	keys := slices.Sorted(maps.Keys(envMap))
	env := make([]string, 0, len(keys))

	for _, k := range keys {
		env = append(env, k+"="+envMap[k])
	}

	return envPaths{
		home:     home,
		xdgData:  xdgData,
		xdgCfg:   xdgCfg,
		xdgState: xdgState,
		xdgCache: xdgCache,
		env:      env,
	}
}

type cliResult struct {
	exitCode int
	stdout   string
	stderr   string
}

func runCLI(t *testing.T, bin, dir string, env []string, stdin string, args ...string) cliResult {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir

	if len(env) > 0 {
		cmd.Env = env
	}

	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0

	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("exec %s %v failed: %v\nstderr:\n%s", bin, args, err, stderr.String())
		}
	}

	return cliResult{
		exitCode: exitCode,
		stdout:   stdout.String(),
		stderr:   stderr.String(),
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func pathToURI(t *testing.T, path string) string {
	t.Helper()

	abs, err := filepath.Abs(path)
	require.NoError(t, err)

	u := url.URL{
		Scheme: "file",
		Path:   filepath.ToSlash(abs),
	}

	return u.String()
}

// --- LSP 3.17 Types & JSON-RPC 2.0 Client ---

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

type lspLocation struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}

type lspLocationLink struct {
	TargetURI            string   `json:"targetUri"`
	TargetRange          lspRange `json:"targetRange"`
	TargetSelectionRange lspRange `json:"targetSelectionRange"`
}

type lspTextEdit struct {
	Range   lspRange `json:"range"`
	NewText string   `json:"newText"`
}

type lspContentChange struct {
	Range *lspRange `json:"range,omitempty"`
	Text  string    `json:"text"`
}

type lspDiagnostic struct {
	Range    lspRange `json:"range"`
	Severity int      `json:"severity"`
	Code     any      `json:"code,omitempty"`
	Source   string   `json:"source,omitempty"`
	Message  string   `json:"message"`
}

type lspMarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type lspHover struct {
	Contents lspMarkupContent `json:"contents"`
	Range    *lspRange        `json:"range,omitempty"`
}

type lspParameterInfo struct {
	Label         any               `json:"label"`
	Documentation *lspMarkupContent `json:"documentation,omitempty"`
}

type lspSignatureInfo struct {
	Label           string             `json:"label"`
	Documentation   *lspMarkupContent  `json:"documentation,omitempty"`
	Parameters      []lspParameterInfo `json:"parameters,omitempty"`
	ActiveParameter *int               `json:"activeParameter,omitempty"`
}

type lspSignatureHelp struct {
	Signatures      []lspSignatureInfo `json:"signatures"`
	ActiveSignature *int               `json:"activeSignature,omitempty"`
	ActiveParameter *int               `json:"activeParameter,omitempty"`
}

type lspCompletionItem struct {
	Label         string `json:"label"`
	Kind          int    `json:"kind,omitempty"`
	Detail        string `json:"detail,omitempty"`
	Documentation any    `json:"documentation,omitempty"`
}

type lspCompletionList struct {
	IsIncomplete bool                `json:"isIncomplete"`
	Items        []lspCompletionItem `json:"items"`
}

type lspDocumentSymbol struct {
	Name           string              `json:"name"`
	Kind           int                 `json:"kind"`
	Range          lspRange            `json:"range"`
	SelectionRange lspRange            `json:"selectionRange"`
	Children       []lspDocumentSymbol `json:"children,omitempty"`
}

type lspSymbolInformation struct {
	Name     string      `json:"name"`
	Kind     int         `json:"kind"`
	Location lspLocation `json:"location"`
}

type lspSemanticTokens struct {
	ResultID string   `json:"resultId,omitempty"`
	Data     []uint32 `json:"data"`
}

type lspSemanticTokensEdit struct {
	Start       uint32   `json:"start"`
	DeleteCount uint32   `json:"deleteCount"`
	Data        []uint32 `json:"data,omitempty"`
}

type lspSemanticTokensDelta struct {
	ResultID string                  `json:"resultId,omitempty"`
	Edits    []lspSemanticTokensEdit `json:"edits"`
	Data     []uint32                `json:"data,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type publishDiagnosticsEvent struct {
	URI            string          `json:"uri"`
	RawDiagnostics json.RawMessage `json:"diagnostics"`
	Diagnostics    []lspDiagnostic `json:"-"`
}

type lspClient struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stdin  io.WriteCloser

	writeMu sync.Mutex
	stderr  bytes.Buffer
	stderrM sync.Mutex

	nextID atomic.Int64

	pendingMu sync.Mutex
	pending   map[string]chan rpcEnvelope

	stateMu        sync.Mutex
	diags          []publishDiagnosticsEvent
	nullIDErrors   []rpcEnvelope
	serverRequests []rpcEnvelope
	readErr        string
	closed         bool

	notifyCh   chan struct{}
	doneCh     chan struct{}
	stderrDone chan struct{}
}

func startLSP(t *testing.T, dir string, env []string, extraArgs ...string) *lspClient {
	t.Helper()

	bin := requireBqpls(t)

	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	args := append([]string{"serve"}, extraArgs...)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir

	if len(env) > 0 {
		cmd.Env = env
	}

	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)

	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)

	client := &lspClient{
		cmd:        cmd,
		cancel:     cancel,
		stdin:      stdin,
		pending:    make(map[string]chan rpcEnvelope),
		notifyCh:   make(chan struct{}, 1),
		doneCh:     make(chan struct{}),
		stderrDone: make(chan struct{}),
	}

	stderrPipe, err := cmd.StderrPipe()
	require.NoError(t, err)

	require.NoError(t, cmd.Start())

	go func() {
		defer close(client.stderrDone)
		_, _ = io.Copy(&lockedWriter{mu: &client.stderrM, buf: &client.stderr}, stderrPipe)
	}()
	go client.readLoop(stdout)

	t.Cleanup(func() {
		client.cleanup()
	})

	return client
}

type lockedWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.buf.Write(p)
}

func (c *lspClient) stderrString() string {
	c.stderrM.Lock()
	defer c.stderrM.Unlock()

	return c.stderr.String()
}

func (c *lspClient) setReadError(msg string) {
	c.stateMu.Lock()
	if c.readErr == "" {
		c.readErr = msg
	}
	c.stateMu.Unlock()
}

func (c *lspClient) readError() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()

	return c.readErr
}

func (c *lspClient) failPendingRequests(msg string) {
	if msg == "" {
		msg = "LSP transport closed"
	}

	c.pendingMu.Lock()
	pending := c.pending
	c.pending = make(map[string]chan rpcEnvelope)
	c.pendingMu.Unlock()

	for _, ch := range pending {
		ch <- rpcEnvelope{
			JSONRPC: "2.0",
			Error: &rpcError{
				Code:    -32603,
				Message: msg,
			},
		}
	}
}

func (c *lspClient) readLoop(r io.Reader) {
	defer func() {
		c.failPendingRequests(c.readError())
		close(c.doneCh)
	}()

	br := bufio.NewReader(r)
	tp := textproto.NewReader(br)

	for {
		headers, err := tp.ReadMIMEHeader()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.setReadError(fmt.Sprintf("read MIME header: %v", err))
			}

			return
		}

		clStr := headers.Get("Content-Length")
		if clStr == "" {
			c.setReadError("LSP frame missing Content-Length header")

			return
		}

		length, convErr := strconv.Atoi(strings.TrimSpace(clStr))
		if convErr != nil || length < 0 {
			c.setReadError(fmt.Sprintf("invalid Content-Length %q: %v", clStr, convErr))

			return
		}

		payload := make([]byte, length)
		if _, readErr := io.ReadFull(br, payload); readErr != nil {
			c.setReadError(fmt.Sprintf("read LSP payload: %v", readErr))

			return
		}

		var env rpcEnvelope
		if jsonErr := json.Unmarshal(payload, &env); jsonErr != nil {
			c.setReadError(fmt.Sprintf("unmarshal LSP payload: %v", jsonErr))

			continue
		}

		c.dispatchIncoming(env)
	}
}

func (c *lspClient) dispatchIncoming(env rpcEnvelope) {
	hasID := len(env.ID) > 0 && string(env.ID) != "null"

	if env.Method != "" && hasID {
		c.stateMu.Lock()
		c.serverRequests = append(c.serverRequests, env)
		c.stateMu.Unlock()

		ack := fmt.Appendf(nil, `{"jsonrpc":"2.0","id":%s,"result":null}`, string(env.ID))
		_ = c.writeFrame(ack)

		select {
		case c.notifyCh <- struct{}{}:
		default:
		}

		return
	}

	if env.Method != "" {
		if env.Method == "textDocument/publishDiagnostics" {
			var ev publishDiagnosticsEvent
			if err := json.Unmarshal(env.Params, &ev); err == nil {
				if len(ev.RawDiagnostics) > 0 && string(ev.RawDiagnostics) != "null" {
					_ = json.Unmarshal(ev.RawDiagnostics, &ev.Diagnostics)
				}

				c.stateMu.Lock()
				c.diags = append(c.diags, ev)
				c.stateMu.Unlock()

				select {
				case c.notifyCh <- struct{}{}:
				default:
				}
			}
		}

		return
	}

	if hasID {
		key := canonicalIDKey(env.ID)

		c.pendingMu.Lock()
		ch, ok := c.pending[key]
		if ok {
			delete(c.pending, key)
		}
		c.pendingMu.Unlock()

		if ok {
			ch <- env
		}

		return
	}

	if env.Error != nil {
		c.stateMu.Lock()
		c.nullIDErrors = append(c.nullIDErrors, env)
		c.stateMu.Unlock()

		select {
		case c.notifyCh <- struct{}{}:
		default:
		}
	}
}

func canonicalIDKey(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, `"`) {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return "s:" + s
		}
	}

	return "n:" + trimmed
}

func (c *lspClient) writeFrame(body []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	header := fmt.Appendf(nil, "Content-Length: %d\r\n\r\n", len(body))
	if _, err := c.stdin.Write(header); err != nil {
		return err
	}

	_, err := c.stdin.Write(body)

	return err
}

func (c *lspClient) sendRawBytes(t *testing.T, raw []byte) {
	t.Helper()

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	_, err := c.stdin.Write(raw)
	require.NoError(t, err)
}

func (c *lspClient) writeFramedJSON(t *testing.T, payload any) {
	t.Helper()

	data, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, c.writeFrame(data))
}

func (c *lspClient) startRequest(t *testing.T, explicitID any, method string, params any) <-chan rpcEnvelope {
	t.Helper()

	var idVal any
	if explicitID != nil {
		idVal = explicitID
	} else {
		idVal = c.nextID.Add(1)
	}

	rawID, err := json.Marshal(idVal)
	require.NoError(t, err)

	key := canonicalIDKey(rawID)
	ch := make(chan rpcEnvelope, 1)

	c.pendingMu.Lock()
	c.pending[key] = ch
	c.pendingMu.Unlock()

	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      idVal,
		"method":  method,
	}
	if params != nil {
		msg["params"] = params
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	if writeErr := c.writeFrame(data); writeErr != nil {
		c.pendingMu.Lock()
		delete(c.pending, key)
		c.pendingMu.Unlock()
		require.NoError(t, writeErr)
	}

	return ch
}

func (c *lspClient) callRaw(t *testing.T, method string, params any) (json.RawMessage, *rpcError) {
	t.Helper()

	ch := c.startRequest(t, nil, method, params)

	select {
	case resp := <-ch:
		return resp.Result, resp.Error
	case <-c.doneCh:
		t.Fatalf("LSP server exited unexpectedly during %s (readErr=%q); stderr:\n%s", method, c.readError(), c.stderrString())

		return nil, nil
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for %s response; stderr:\n%s", method, c.stderrString())

		return nil, nil
	}
}

func (c *lspClient) call(t *testing.T, method string, params any) json.RawMessage {
	t.Helper()

	res, rpcErr := c.callRaw(t, method, params)
	require.Nil(t, rpcErr, "unexpected JSON-RPC error on %s: %+v\nstderr:\n%s", method, rpcErr, c.stderrString())

	return res
}

func (c *lspClient) notify(t *testing.T, method string, params any) {
	t.Helper()

	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		msg["params"] = params
	}

	c.writeFramedJSON(t, msg)
}

func (c *lspClient) cancelRequest(t *testing.T, id any) {
	t.Helper()
	c.notify(t, "$/cancelRequest", map[string]any{"id": id})
}

func (c *lspClient) initialize(t *testing.T, workspaceDir string) map[string]any {
	t.Helper()

	rootURI := ""
	if workspaceDir != "" {
		rootURI = pathToURI(t, workspaceDir)
	}

	params := map[string]any{
		"processId": nil,
		"rootUri":   rootURI,
		"capabilities": map[string]any{
			"workspace": map[string]any{
				"semanticTokens": map[string]any{"refreshSupport": true},
			},
			"textDocument": map[string]any{
				"synchronization": map[string]any{
					"didSave":             true,
					"dynamicRegistration": false,
				},
				"hover": map[string]any{
					"contentFormat": []string{"markdown", "plaintext"},
				},
				"completion": map[string]any{
					"completionItem": map[string]any{
						"snippetSupport":      false,
						"documentationFormat": []string{"markdown", "plaintext"},
					},
				},
				"definition":     map[string]any{},
				"documentSymbol": map[string]any{"hierarchicalDocumentSymbolSupport": true},
				"formatting":     map[string]any{},
				"semanticTokens": map[string]any{
					"requests": map[string]any{
						"full":  map[string]any{"delta": true},
						"range": true,
					},
					"tokenTypes":     canonicalTokenTypes,
					"tokenModifiers": canonicalTokenModifiers,
					"formats":        []string{"relative"},
				},
			},
		},
	}

	raw := c.call(t, "initialize", params)

	var result map[string]any
	require.NoError(t, json.Unmarshal(raw, &result))

	c.notify(t, "initialized", map[string]any{})

	return result
}

//nolint:unparam // explicit LSP languageId parameter keeps test calls self-documenting
func (c *lspClient) didOpen(t *testing.T, uri, langID, text string, version int) {
	t.Helper()

	if langID == "" {
		langID = "bqlang"
	}

	c.notify(t, "textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": langID,
			"version":    version,
			"text":       text,
		},
	})
}

func (c *lspClient) didChange(t *testing.T, uri string, version int, changes ...lspContentChange) {
	t.Helper()

	c.notify(t, "textDocument/didChange", map[string]any{
		"textDocument": map[string]any{
			"uri":     uri,
			"version": version,
		},
		"contentChanges": changes,
	})
}

func (c *lspClient) didSave(t *testing.T, uri, text string) {
	t.Helper()

	params := map[string]any{
		"textDocument": map[string]any{"uri": uri},
	}
	if text != "" {
		params["text"] = text
	}

	c.notify(t, "textDocument/didSave", params)
}

func (c *lspClient) didClose(t *testing.T, uri string) {
	t.Helper()

	c.notify(t, "textDocument/didClose", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
}

func (c *lspClient) diagCursor() int {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()

	return len(c.diags)
}

//nolint:unparam // timeout parameter allows per-test deadline tuning
func (c *lspClient) waitDiagnostics(t *testing.T, uri string, afterIdx int, timeout time.Duration) (int, publishDiagnosticsEvent) {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for {
		c.stateMu.Lock()
		for i := afterIdx; i < len(c.diags); i++ {
			if c.diags[i].URI == uri {
				ev := c.diags[i]
				c.stateMu.Unlock()

				return i + 1, ev
			}
		}
		c.stateMu.Unlock()

		rem := time.Until(deadline)
		if rem <= 0 {
			t.Fatalf("timed out waiting for publishDiagnostics on %s (afterIdx=%d); stderr:\n%s", uri, afterIdx, c.stderrString())
		}

		select {
		case <-c.notifyCh:
		case <-c.doneCh:
			t.Fatalf("LSP server exited while waiting for publishDiagnostics (readErr=%q); stderr:\n%s", c.readError(), c.stderrString())
		case <-time.After(rem):
		}
	}
}

func (c *lspClient) pollDiagnostics(t *testing.T, uri string, afterIdx int, window time.Duration) (publishDiagnosticsEvent, bool) {
	t.Helper()

	deadline := time.Now().Add(window)

	for {
		c.stateMu.Lock()
		for i := afterIdx; i < len(c.diags); i++ {
			if c.diags[i].URI == uri {
				ev := c.diags[i]
				c.stateMu.Unlock()

				return ev, true
			}
		}
		c.stateMu.Unlock()

		rem := time.Until(deadline)
		if rem <= 0 {
			return publishDiagnosticsEvent{}, false
		}

		select {
		case <-c.notifyCh:
		case <-c.doneCh:
			t.Fatalf("LSP server exited while polling publishDiagnostics (readErr=%q); stderr:\n%s", c.readError(), c.stderrString())
		case <-time.After(rem):
		}
	}
}

func (c *lspClient) nullErrCursor() int {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()

	return len(c.nullIDErrors)
}

func (c *lspClient) waitNullIDErr(t *testing.T, afterIdx int, timeout time.Duration) (int, rpcEnvelope) {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for {
		c.stateMu.Lock()
		if afterIdx < len(c.nullIDErrors) {
			ev := c.nullIDErrors[afterIdx]
			c.stateMu.Unlock()

			return afterIdx + 1, ev
		}
		c.stateMu.Unlock()

		rem := time.Until(deadline)
		if rem <= 0 {
			t.Fatalf("timed out waiting for null-id JSON-RPC error (afterIdx=%d); stderr:\n%s", afterIdx, c.stderrString())
		}

		select {
		case <-c.notifyCh:
		case <-c.doneCh:
			t.Fatalf("LSP server exited while waiting for null-id error (readErr=%q); stderr:\n%s", c.readError(), c.stderrString())
		case <-time.After(rem):
		}
	}
}

func (c *lspClient) semanticTokensFull(t *testing.T, uri string) lspSemanticTokens {
	t.Helper()

	raw := c.call(t, "textDocument/semanticTokens/full", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})

	var tok lspSemanticTokens
	if len(raw) > 0 && string(raw) != "null" {
		require.NoError(t, json.Unmarshal(raw, &tok))
	}

	return tok
}

func (c *lspClient) signatureHelp(t *testing.T, uri string, pos lspPosition) (*lspSignatureHelp, *rpcError) {
	t.Helper()

	raw, rpcErr := c.callRaw(t, "textDocument/signatureHelp", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     pos,
	})
	if rpcErr != nil {
		return nil, rpcErr
	}

	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}

	var sh lspSignatureHelp
	require.NoError(t, json.Unmarshal(raw, &sh))

	return &sh, nil
}

func (c *lspClient) semanticTokensFullRaw(t *testing.T, uri string) json.RawMessage {
	t.Helper()

	return c.call(t, "textDocument/semanticTokens/full", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
}

func (c *lspClient) semanticTokensFullDelta(t *testing.T, uri, prevResultID string) (lspSemanticTokensDelta, json.RawMessage) {
	t.Helper()

	raw := c.call(t, "textDocument/semanticTokens/full/delta", map[string]any{
		"textDocument":     map[string]any{"uri": uri},
		"previousResultId": prevResultID,
	})

	var delta lspSemanticTokensDelta
	if len(raw) > 0 && string(raw) != "null" {
		require.NoError(t, json.Unmarshal(raw, &delta))
	}

	return delta, raw
}

func (c *lspClient) semanticTokensRange(t *testing.T, uri string, rng lspRange) lspSemanticTokens {
	t.Helper()

	raw := c.call(t, "textDocument/semanticTokens/range", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"range":        rng,
	})

	var tok lspSemanticTokens
	if len(raw) > 0 && string(raw) != "null" {
		require.NoError(t, json.Unmarshal(raw, &tok))
	}

	return tok
}

func (c *lspClient) hover(t *testing.T, uri string, pos lspPosition) (*lspHover, *rpcError) {
	t.Helper()

	raw, rpcErr := c.callRaw(t, "textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     pos,
	})
	if rpcErr != nil {
		return nil, rpcErr
	}

	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var h lspHover
	require.NoError(t, json.Unmarshal(raw, &h))

	return &h, nil
}

func (c *lspClient) completion(t *testing.T, uri string, pos lspPosition) []lspCompletionItem {
	t.Helper()

	raw := c.call(t, "textDocument/completion", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     pos,
	})

	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}

	if strings.HasPrefix(trimmed, "[") {
		var items []lspCompletionItem
		require.NoError(t, json.Unmarshal(raw, &items))

		return items
	}

	var list lspCompletionList
	require.NoError(t, json.Unmarshal(raw, &list))

	return list.Items
}

func (c *lspClient) definition(t *testing.T, uri string, pos lspPosition) []lspLocation {
	t.Helper()

	raw := c.call(t, "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     pos,
	})

	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}

	if strings.HasPrefix(trimmed, "{") {
		var loc lspLocation
		require.NoError(t, json.Unmarshal(raw, &loc))

		return []lspLocation{loc}
	}

	var rawItems []json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &rawItems))

	locs := make([]lspLocation, 0, len(rawItems))

	for _, item := range rawItems {
		var link lspLocationLink
		if err := json.Unmarshal(item, &link); err == nil && link.TargetURI != "" {
			locs = append(locs, lspLocation{
				URI:   link.TargetURI,
				Range: link.TargetSelectionRange,
			})

			continue
		}

		var loc lspLocation
		require.NoError(t, json.Unmarshal(item, &loc))
		locs = append(locs, loc)
	}

	return locs
}

func (c *lspClient) documentSymbols(t *testing.T, uri string) ([]lspDocumentSymbol, json.RawMessage) {
	t.Helper()

	raw := c.call(t, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})

	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, raw
	}

	var docSyms []lspDocumentSymbol
	if err := json.Unmarshal(raw, &docSyms); err == nil {
		hasDocRange := len(docSyms) == 0 || docSyms[0].SelectionRange != (lspRange{}) || docSyms[0].Range != (lspRange{})
		if hasDocRange {
			return docSyms, raw
		}
	}

	var symInfos []lspSymbolInformation
	require.NoError(t, json.Unmarshal(raw, &symInfos))

	converted := make([]lspDocumentSymbol, 0, len(symInfos))
	for _, s := range symInfos {
		converted = append(converted, lspDocumentSymbol{
			Name:           s.Name,
			Kind:           s.Kind,
			Range:          s.Location.Range,
			SelectionRange: s.Location.Range,
		})
	}

	return converted, raw
}

//nolint:unparam // tabSize parameter allows per-test formatting option testing
func (c *lspClient) formatting(t *testing.T, uri string, tabSize int, insertSpaces bool) ([]lspTextEdit, *rpcError) {
	t.Helper()

	raw, rpcErr := c.callRaw(t, "textDocument/formatting", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"options": map[string]any{
			"tabSize":      tabSize,
			"insertSpaces": insertSpaces,
		},
	})
	if rpcErr != nil {
		return nil, rpcErr
	}

	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}

	var edits []lspTextEdit
	require.NoError(t, json.Unmarshal(raw, &edits))

	return edits, nil
}

func (c *lspClient) drainPipes(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	doneCh := c.doneCh
	stderrDone := c.stderrDone

	for doneCh != nil || stderrDone != nil {
		select {
		case <-doneCh:
			doneCh = nil
		case <-stderrDone:
			stderrDone = nil
		case <-timer.C:
			return false
		}
	}

	return true
}

func (c *lspClient) shutdownAndExit(t *testing.T) int {
	t.Helper()

	c.stateMu.Lock()
	alreadyClosed := c.closed
	c.closed = true
	c.stateMu.Unlock()

	if alreadyClosed {
		return 0
	}

	raw := c.call(t, "shutdown", nil)
	if len(raw) > 0 {
		assert.Equal(t, "null", strings.TrimSpace(string(raw)))
	}

	c.notify(t, "exit", nil)
	_ = c.stdin.Close()

	if !c.drainPipes(5 * time.Second) {
		c.cancel()
		<-c.doneCh
		<-c.stderrDone
		_ = c.cmd.Wait()
		t.Fatalf("bqpls did not exit cleanly after exit notification; stderr:\n%s", c.stderrString())

		return -1
	}

	err := c.cmd.Wait()
	c.cancel()

	require.Empty(t, c.readError(), "LSP client read error; stderr:\n%s", c.stderrString())

	if err == nil {
		return 0
	}

	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode()
	}

	t.Fatalf("wait bqpls failed: %v", err)

	return -1
}

func (c *lspClient) cleanup() {
	c.stateMu.Lock()
	alreadyClosed := c.closed
	c.closed = true
	c.stateMu.Unlock()

	if alreadyClosed {
		c.cancel()

		return
	}

	_ = c.stdin.Close()

	if !c.drainPipes(2 * time.Second) {
		c.cancel()
		<-c.doneCh
		<-c.stderrDone
	}

	_ = c.cmd.Wait()
	c.cancel()
}

// --- UTF-16 & Semantic Token Helpers ---

func utf16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
}

func utf16PosAt(t *testing.T, src, needle string) lspPosition {
	t.Helper()

	return utf16PosAtNth(t, src, needle, 0)
}

func utf16PosAtNth(t *testing.T, src, needle string, nth int) lspPosition {
	t.Helper()

	searchFrom := 0
	found := -1

	for i := 0; i <= nth; i++ {
		idx := strings.Index(src[searchFrom:], needle)
		require.NotEqual(t, -1, idx, "substring %q occurrence %d not found in:\n%s", needle, nth, src)
		found = searchFrom + idx
		searchFrom = found + len(needle)
	}

	return utf16PosForByteOffset(src, found)
}

func utf16PosAfter(t *testing.T, src, needle string) lspPosition {
	t.Helper()

	idx := strings.Index(src, needle)
	require.NotEqual(t, -1, idx, "substring %q not found in:\n%s", needle, src)

	return utf16PosForByteOffset(src, idx+len(needle))
}

func utf16PosForByteOffset(src string, byteOff int) lspPosition {
	if byteOff <= 0 {
		return lspPosition{}
	}

	if byteOff > len(src) {
		byteOff = len(src)
	}

	line := 0
	lineStart := 0

	for i := 0; i < byteOff; {
		if src[i] == '\r' || src[i] == '\n' {
			if src[i] == '\r' && i+1 < len(src) && src[i+1] == '\n' {
				if i+1 >= byteOff {
					return lspPosition{
						Line:      line,
						Character: utf16Len(src[lineStart:i]),
					}
				}

				i += 2
			} else {
				i++
			}

			lineStart = i
			line++

			continue
		}

		i++
	}

	return lspPosition{
		Line:      line,
		Character: utf16Len(src[lineStart:byteOff]),
	}
}

func utf16PosToByteOffset(src string, pos lspPosition) int {
	if pos.Line < 0 {
		return 0
	}

	line := 0
	lineStart := 0

	for line < pos.Line && lineStart < len(src) {
		termIdx := strings.IndexAny(src[lineStart:], "\r\n")
		if termIdx < 0 {
			lineStart = len(src)

			break
		}

		termPos := lineStart + termIdx
		if src[termPos] == '\r' && termPos+1 < len(src) && src[termPos+1] == '\n' {
			lineStart = termPos + 2
		} else {
			lineStart = termPos + 1
		}

		line++
	}

	if pos.Character <= 0 {
		return lineStart
	}

	units := 0
	off := lineStart

	for off < len(src) {
		if src[off] == '\n' || src[off] == '\r' {
			break
		}

		r, size := decodeRuneAt(src[off:])
		if size <= 0 || off+size > len(src) {
			break
		}

		runeUnits := 1
		if r > 0xFFFF {
			runeUnits = 2
		}

		if units+runeUnits > pos.Character {
			break
		}

		units += runeUnits
		off += size
	}

	return off
}

func decodeRuneAt(s string) (rune, int) {
	if s == "" {
		return 0, 0
	}

	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && size == 1 {
		if len(s) < 1 {
			return utf8.RuneError, 0
		}

		return utf8.RuneError, 1
	}

	return r, size
}

type resolvedTextEdit struct {
	startOff int
	endOff   int
	newText  string
}

func applyTextEdits(src string, edits []lspTextEdit) string {
	sorted := slices.Clone(edits)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Range.Start.Line != sorted[j].Range.Start.Line {
			return sorted[i].Range.Start.Line < sorted[j].Range.Start.Line
		}

		if sorted[i].Range.Start.Character != sorted[j].Range.Start.Character {
			return sorted[i].Range.Start.Character < sorted[j].Range.Start.Character
		}

		if sorted[i].Range.End.Line != sorted[j].Range.End.Line {
			return sorted[i].Range.End.Line < sorted[j].Range.End.Line
		}

		return sorted[i].Range.End.Character < sorted[j].Range.End.Character
	})

	resolved := make([]resolvedTextEdit, 0, len(sorted))
	for _, ed := range sorted {
		startOff := utf16PosToByteOffset(src, ed.Range.Start)
		endOff := utf16PosToByteOffset(src, ed.Range.End)
		resolved = append(resolved, resolvedTextEdit{
			startOff: startOff,
			endOff:   endOff,
			newText:  ed.NewText,
		})
	}

	var b strings.Builder

	cursor := 0
	for _, ed := range resolved {
		if ed.startOff < cursor || ed.endOff < ed.startOff || ed.endOff > len(src) {
			panic(fmt.Sprintf("invalid/overlapping LSP text edit [%d,%d) with cursor=%d", ed.startOff, ed.endOff, cursor))
		}

		b.WriteString(src[cursor:ed.startOff])
		b.WriteString(ed.newText)
		cursor = ed.endOff
	}

	b.WriteString(src[cursor:])

	return b.String()
}

type decodedToken struct {
	line           uint32
	startChar      uint32
	length         uint32
	tokenType      uint32
	tokenModifiers uint32
}

func decodeSemanticTokens(data []uint32) ([]decodedToken, error) {
	if len(data)%5 != 0 {
		return nil, fmt.Errorf("semantic tokens data length %d is not a multiple of 5", len(data))
	}

	tokens := make([]decodedToken, 0, len(data)/5)

	var (
		line    uint32
		col     uint32
		hasPrev bool
		prevEnd uint32
		prevLn  uint32
	)

	for i := 0; i < len(data); i += 5 {
		dl := data[i]
		ds := data[i+1]
		length := data[i+2]
		typ := data[i+3]
		mod := data[i+4]

		line += dl
		if dl == 0 {
			col += ds
		} else {
			col = ds
		}

		if length == 0 {
			return nil, fmt.Errorf("token %d at (%d,%d) has zero length", i/5, line, col)
		}

		if int(typ) >= len(canonicalTokenTypes) {
			return nil, fmt.Errorf("token %d has out-of-range type %d", i/5, typ)
		}

		if mod >= (1 << len(canonicalTokenModifiers)) {
			return nil, fmt.Errorf("token %d has out-of-range modifiers %#x", i/5, mod)
		}

		if hasPrev {
			if line < prevLn || (line == prevLn && col < prevEnd) {
				return nil, fmt.Errorf("token %d at (%d,%d) overlaps or goes backward after (%d, end=%d)", i/5, line, col, prevLn, prevEnd)
			}
		}

		tokens = append(tokens, decodedToken{
			line:           line,
			startChar:      col,
			length:         length,
			tokenType:      typ,
			tokenModifiers: mod,
		})

		hasPrev = true
		prevLn = line
		prevEnd = col + length
	}

	return tokens, nil
}

func findTokenAt(tokens []decodedToken, pos lspPosition) (decodedToken, bool) {
	line := uint32(pos.Line)
	ch := uint32(pos.Character)

	for _, tok := range tokens {
		if tok.line == line && tok.startChar <= ch && ch < tok.startChar+tok.length {
			return tok, true
		}
	}

	return decodedToken{}, false
}

func applySemanticTokenEdits(t *testing.T, prev []uint32, edits []lspSemanticTokensEdit) []uint32 {
	t.Helper()

	sorted := slices.Clone(edits)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Start < sorted[j].Start
	})

	out := make([]uint32, 0, len(prev))
	cursor := 0

	for _, ed := range sorted {
		start := int(ed.Start)
		delCount := int(ed.DeleteCount)
		require.GreaterOrEqual(t, start, cursor, "semantic token edit overlaps previous edit (start=%d < cursor=%d)", start, cursor)
		require.LessOrEqual(t, start+delCount, len(prev), "semantic token edit exceeds prev length (start=%d, deleteCount=%d, len=%d)", start, delCount, len(prev))

		out = append(out, prev[cursor:start]...)
		out = append(out, ed.Data...)
		cursor = start + delCount
	}

	out = append(out, prev[cursor:]...)

	return out
}

func flattenSymbolNames(syms []lspDocumentSymbol) []string {
	var names []string
	for _, s := range syms {
		names = append(names, s.Name)
		names = append(names, flattenSymbolNames(s.Children)...)
	}

	return names
}

func completionLabels(items []lspCompletionItem) []string {
	labels := make([]string, 0, len(items))
	for _, it := range items {
		labels = append(labels, it.Label)
	}

	return labels
}

// --- Fixture & VSIX Helpers ---

type schemaFieldSpec struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Mode        string            `json:"mode,omitempty"`
	Description string            `json:"description,omitempty"`
	Fields      []schemaFieldSpec `json:"fields,omitempty"`
}

type schemaTableRef struct {
	ProjectID string `json:"projectId"`
	DatasetID string `json:"datasetId"`
	TableID   string `json:"tableId"`
}

type schemaTimePartitioning struct {
	Type                   string `json:"type"`
	Field                  string `json:"field,omitempty"`
	ExpirationMs           string `json:"expirationMs,omitempty"`
	RequirePartitionFilter bool   `json:"requirePartitionFilter,omitempty"`
}

type schemaRangeBounds struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Interval string `json:"interval"`
}

type schemaRangePartitioning struct {
	Field string            `json:"field"`
	Range schemaRangeBounds `json:"range"`
}

type schemaClustering struct {
	Fields []string `json:"fields"`
}

type schemaTableSchema struct {
	Fields []schemaFieldSpec `json:"fields"`
}

type schemaTableSpec struct {
	Kind                   string                   `json:"kind"`
	ID                     string                   `json:"id"`
	TableReference         schemaTableRef           `json:"tableReference"`
	Type                   string                   `json:"type"`
	Description            string                   `json:"description,omitempty"`
	NumRows                string                   `json:"numRows,omitempty"`
	NumBytes               string                   `json:"numBytes,omitempty"`
	RequirePartitionFilter bool                     `json:"requirePartitionFilter,omitempty"`
	TimePartitioning       *schemaTimePartitioning  `json:"timePartitioning,omitempty"`
	RangePartitioning      *schemaRangePartitioning `json:"rangePartitioning,omitempty"`
	Clustering             *schemaClustering        `json:"clustering,omitempty"`
	Schema                 schemaTableSchema        `json:"schema"`
}

func standardEventsSchema() schemaTableSpec {
	return schemaTableSpec{
		Kind: "bigquery#table",
		ID:   "my-proj:analytics.events",
		TableReference: schemaTableRef{
			ProjectID: "my-proj",
			DatasetID: "analytics",
			TableID:   "events",
		},
		Type:                   "TABLE",
		Description:            "Raw clickstream events table",
		NumRows:                "1250000",
		NumBytes:               "536870912",
		RequirePartitionFilter: true,
		TimePartitioning: &schemaTimePartitioning{
			Type:                   "DAY",
			Field:                  "event_date",
			ExpirationMs:           "7776000000",
			RequirePartitionFilter: true,
		},
		Clustering: &schemaClustering{
			Fields: []string{"tenant_id", "event_type"},
		},
		Schema: schemaTableSchema{
			Fields: []schemaFieldSpec{
				{Name: "event_date", Type: "DATE", Mode: "REQUIRED", Description: "Event UTC partition date"},
				{Name: "tenant_id", Type: "STRING", Mode: "REQUIRED", Description: "Customer tenant identifier"},
				{Name: "event_type", Type: "STRING", Mode: "NULLABLE", Description: "Normalized event category"},
				{
					Name:        "user_ctx",
					Type:        "RECORD",
					Mode:        "NULLABLE",
					Description: "Nested user context",
					Fields: []schemaFieldSpec{
						{Name: "user_id", Type: "INTEGER", Mode: "REQUIRED", Description: "Numeric user ID"},
						{
							Name:        "tags",
							Type:        "RECORD",
							Mode:        "REPEATED",
							Description: "Attached key-value tags",
							Fields: []schemaFieldSpec{
								{Name: "key", Type: "STRING", Mode: "REQUIRED"},
								{Name: "val", Type: "STRING", Mode: "NULLABLE"},
							},
						},
					},
				},
			},
		},
	}
}

func usersSchema() schemaTableSpec {
	return schemaTableSpec{
		Kind: "bigquery#table",
		ID:   "my-proj:analytics.users",
		TableReference: schemaTableRef{
			ProjectID: "my-proj",
			DatasetID: "analytics",
			TableID:   "users",
		},
		Type:        "TABLE",
		Description: "Registered user dimension table",
		NumRows:     "42000",
		Schema: schemaTableSchema{
			Fields: []schemaFieldSpec{
				{Name: "user_id", Type: "INT64", Mode: "REQUIRED", Description: "Unique user key"},
				{Name: "country", Type: "STRING", Mode: "NULLABLE", Description: "ISO country code"},
				{Name: "signup_at", Type: "TIMESTAMP", Mode: "NULLABLE"},
			},
		},
	}
}

func writeSchemaTableJSON(t *testing.T, path string, spec schemaTableSpec) {
	t.Helper()

	data, err := json.MarshalIndent(spec, "", "  ")
	require.NoError(t, err)
	writeFile(t, path, string(data))
}

func writeBqShowSchemaArrayJSON(t *testing.T, path string, fields []schemaFieldSpec) {
	t.Helper()

	data, err := json.MarshalIndent(fields, "", "  ")
	require.NoError(t, err)
	writeFile(t, path, string(data))
}

func writeDiskCacheTable(t *testing.T, cacheDir string, spec schemaTableSpec, fetchedAt time.Time) {
	t.Helper()

	require.NoError(t, os.MkdirAll(cacheDir, 0o750))
	data, err := json.Marshal(spec)
	require.NoError(t, err)

	dbPath := filepath.Join(cacheDir, "cache.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	_, err = db.ExecContext(t.Context(), `
		CREATE TABLE IF NOT EXISTS table_cache (
			project_lower TEXT NOT NULL,
			dataset_lower TEXT NOT NULL,
			table_lower   TEXT NOT NULL,
			project       TEXT NOT NULL,
			dataset       TEXT NOT NULL,
			table_name    TEXT NOT NULL,
			table_type    TEXT NOT NULL DEFAULT '',
			friendly_name TEXT NOT NULL DEFAULT '',
			payload       BLOB NOT NULL,
			fetched_at    INTEGER NOT NULL,
			PRIMARY KEY (project_lower, dataset_lower, table_lower)
		)
	`)
	require.NoError(t, err)

	ref := spec.TableReference
	_, err = db.ExecContext(
		t.Context(),
		`INSERT OR REPLACE INTO table_cache (
			project_lower, dataset_lower, table_lower,
			project, dataset, table_name, table_type, friendly_name,
			payload, fetched_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.ToLower(ref.ProjectID),
		strings.ToLower(ref.DatasetID),
		strings.ToLower(ref.TableID),
		ref.ProjectID,
		ref.DatasetID,
		ref.TableID,
		spec.Type,
		"",
		data,
		fetchedAt.UnixNano(),
	)
	require.NoError(t, err)
}

func setupStandardWorkspace(t *testing.T, extraLSP string) (string, string) {
	t.Helper()

	ws := t.TempDir()
	schemaDir := filepath.Join(ws, "schemas")
	writeSchemaTableJSON(t, filepath.Join(schemaDir, "my-proj.analytics.events.json"), standardEventsSchema())
	writeSchemaTableJSON(t, filepath.Join(schemaDir, "my-proj.analytics.users.json"), usersSchema())

	cfgContent := "keyword_style = \"UPPER_CASE\"\nindentation = 2\n\n[lsp]\ndefault_project = \"my-proj\"\ndefault_dataset = \"analytics\"\nschema_dirs = [\"./schemas\"]\nbigquery_api_enabled = false\n" + extraLSP
	cfgPath := filepath.Join(ws, ".bqfmt.toml")
	writeFile(t, cfgPath, cfgContent)

	return ws, cfgPath
}

type vsixReport struct {
	entries     map[string][]byte
	packageJSON map[string]any
	tmGrammar   map[string]any
	patterns    []string
	extensionJS string
}

func inspectVSIX(t *testing.T, vsixPath string) vsixReport {
	t.Helper()

	zr, err := zip.OpenReader(vsixPath)
	require.NoError(t, err)
	defer zr.Close()

	entries := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		rc, openErr := f.Open()
		require.NoError(t, openErr)
		body, readErr := io.ReadAll(rc)
		_ = rc.Close()
		require.NoError(t, readErr)
		entries[f.Name] = body
	}

	required := []string{
		"[Content_Types].xml",
		"extension.vsixmanifest",
		"extension/package.json",
		"extension/language-configuration.json",
		"extension/syntaxes/bqlang.tmLanguage.json",
		"extension/extension.js",
	}
	for _, req := range required {
		_, ok := entries[req]
		require.True(t, ok, "missing required VSIX entry %q", req)
	}

	var xmlDoc any
	require.NoError(t, xml.Unmarshal(entries["[Content_Types].xml"], &xmlDoc), "invalid [Content_Types].xml")
	require.NoError(t, xml.Unmarshal(entries["extension.vsixmanifest"], &xmlDoc), "invalid extension.vsixmanifest")

	manifestStr := string(entries["extension.vsixmanifest"])
	assert.Contains(t, manifestStr, "paulourio")
	assert.Contains(t, manifestStr, "bqlang")
	assert.Contains(t, manifestStr, "Microsoft.VisualStudio.Code")

	var pkg map[string]any
	require.NoError(t, json.Unmarshal(entries["extension/package.json"], &pkg))

	var langCfg map[string]any
	require.NoError(t, json.Unmarshal(entries["extension/language-configuration.json"], &langCfg))

	var tm map[string]any
	require.NoError(t, json.Unmarshal(entries["extension/syntaxes/bqlang.tmLanguage.json"], &tm))
	assert.Equal(t, "source.bqlang", tm["scopeName"])

	var patterns []string
	collectTMPatterns(tm, &patterns)
	assert.NotEmpty(t, patterns, "TextMate grammar must declare regex patterns")

	for _, pat := range patterns {
		_, reErr := regexp.Compile(pat)
		require.NoError(t, reErr, "TextMate regex failed Go RE2 regexp.Compile: %q", pat)
	}

	return vsixReport{
		entries:     entries,
		packageJSON: pkg,
		tmGrammar:   tm,
		patterns:    patterns,
		extensionJS: string(entries["extension/extension.js"]),
	}
}

func collectTMPatterns(node any, out *[]string) {
	switch v := node.(type) {
	case map[string]any:
		for _, key := range []string{"match", "begin", "end", "while"} {
			if pat, ok := v[key].(string); ok {
				*out = append(*out, pat)
			}
		}

		for _, child := range v {
			collectTMPatterns(child, out)
		}
	case []any:
		for _, child := range v {
			collectTMPatterns(child, out)
		}
	}
}

func sha256HexFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func TestHarnessHelpers(t *testing.T) {
	t.Parallel()

	t.Run("applyTextEdits_same_start_multi_insert", func(t *testing.T) {
		t.Parallel()

		src := "SELECT '😀' x"
		pos := utf16PosAt(t, src, "x")
		got := applyTextEdits(src, []lspTextEdit{
			{Range: lspRange{Start: pos, End: pos}, NewText: "first_"},
			{Range: lspRange{Start: pos, End: pos}, NewText: "second_"},
		})
		assert.Equal(t, "SELECT '😀' first_second_x", got)
	})

	t.Run("applyTextEdits_same_start_insert_and_replace", func(t *testing.T) {
		t.Parallel()

		src := "SELECT '😀' x"
		pos := utf16PosAt(t, src, "x")
		endPos := utf16PosAfter(t, src, "x")
		got := applyTextEdits(src, []lspTextEdit{
			{Range: lspRange{Start: pos, End: pos}, NewText: "prefix_"},
			{Range: lspRange{Start: pos, End: endPos}, NewText: "y"},
		})
		assert.Equal(t, "SELECT '😀' prefix_y", got)
	})

	t.Run("applyTextEdits_surrogate_pairs_multi_edit", func(t *testing.T) {
		t.Parallel()

		src := "SELECT '😀', foo, '🚀', bar FROM t"
		got := applyTextEdits(src, []lspTextEdit{
			{
				Range: lspRange{
					Start: utf16PosAt(t, src, "foo"),
					End:   utf16PosAfter(t, src, "foo"),
				},
				NewText: "col_a",
			},
			{
				Range: lspRange{
					Start: utf16PosAt(t, src, "bar"),
					End:   utf16PosAfter(t, src, "bar"),
				},
				NewText: "col_b",
			},
		})
		assert.Equal(t, "SELECT '😀', col_a, '🚀', col_b FROM t", got)
	})

	t.Run("applyTextEdits_crlf", func(t *testing.T) {
		t.Parallel()

		src := "select   1   as   a\r\n"
		gotEOL := applyTextEdits(src, []lspTextEdit{
			{
				Range: lspRange{
					Start: lspPosition{Line: 0, Character: 0},
					End:   lspPosition{Line: 0, Character: 99},
				},
				NewText: "SELECT 1 AS a",
			},
		})
		assert.Equal(t, "SELECT 1 AS a\r\n", gotEOL)

		gotWhole := applyTextEdits(src, []lspTextEdit{
			{
				Range: lspRange{
					Start: lspPosition{Line: 0, Character: 0},
					End:   lspPosition{Line: 1, Character: 0},
				},
				NewText: "SELECT 1 AS a\n",
			},
		})
		assert.Equal(t, "SELECT 1 AS a\n", gotWhole)
	})

	t.Run("applySemanticTokenEdits_same_start_multi_insert", func(t *testing.T) {
		t.Parallel()

		prev := []uint32{0, 0, 6, 6, 0, 0, 20, 4, 6, 0}
		got := applySemanticTokenEdits(t, prev, []lspSemanticTokensEdit{
			{Start: 5, DeleteCount: 0, Data: []uint32{0, 7, 3, 4, 0}},
			{Start: 5, DeleteCount: 0, Data: []uint32{0, 12, 3, 4, 0}},
		})
		assert.Equal(t, []uint32{0, 0, 6, 6, 0, 0, 7, 3, 4, 0, 0, 12, 3, 4, 0, 0, 20, 4, 6, 0}, got)
	})

	t.Run("applySemanticTokenEdits_delete_and_insert", func(t *testing.T) {
		t.Parallel()

		prev := []uint32{
			0, 0, 6, 6, 0,
			0, 7, 3, 4, 0,
			0, 5, 4, 6, 0,
			0, 5, 1, 1, 0,
		}
		got := applySemanticTokenEdits(t, prev, []lspSemanticTokensEdit{
			// Replace second token (indices 5..9) with two tokens.
			{Start: 5, DeleteCount: 5, Data: []uint32{0, 7, 4, 4, modPartitioned, 0, 5, 2, 4, 0}},
			// Delete third token (indices 10..14).
			{Start: 10, DeleteCount: 5},
		})
		assert.Equal(t, []uint32{
			0, 0, 6, 6, 0,
			0, 7, 4, 4, modPartitioned,
			0, 5, 2, 4, 0,
			0, 5, 1, 1, 0,
		}, got)
	})

	t.Run("decodeSemanticTokens", func(t *testing.T) {
		t.Parallel()

		tokens, err := decodeSemanticTokens([]uint32{
			0, 0, 6, tokKeyword, 0,
			0, 7, 10, tokProperty, modPartitioned | modClustered,
			1, 5, 6, tokClass, 0,
		})
		require.NoError(t, err)
		require.Len(t, tokens, 3)
		assert.Equal(t, decodedToken{line: 0, startChar: 0, length: 6, tokenType: tokKeyword, tokenModifiers: 0}, tokens[0])
		assert.Equal(t, decodedToken{line: 0, startChar: 7, length: 10, tokenType: tokProperty, tokenModifiers: modPartitioned | modClustered}, tokens[1])
		assert.Equal(t, decodedToken{line: 1, startChar: 5, length: 6, tokenType: tokClass, tokenModifiers: 0}, tokens[2])

		_, err = decodeSemanticTokens([]uint32{0, 0, 6, tokKeyword})
		require.ErrorContains(t, err, "multiple of 5")

		_, err = decodeSemanticTokens([]uint32{0, 0, 0, tokKeyword, 0})
		require.ErrorContains(t, err, "zero length")

		_, err = decodeSemanticTokens([]uint32{0, 0, 3, uint32(len(canonicalTokenTypes)), 0})
		require.ErrorContains(t, err, "out-of-range type")

		_, err = decodeSemanticTokens([]uint32{0, 0, 3, tokKeyword, 1 << len(canonicalTokenModifiers)})
		require.ErrorContains(t, err, "out-of-range modifiers")

		_, err = decodeSemanticTokens([]uint32{
			0, 5, 4, tokKeyword, 0,
			0, 2, 3, tokKeyword, 0,
		})
		require.ErrorContains(t, err, "overlaps")
	})

	t.Run("utf16PosToByteOffset_and_decodeRuneAt_invalid_utf8_and_crlf", func(t *testing.T) {
		t.Parallel()

		bad := "a\xff"
		r, sz := decodeRuneAt(bad[1:])
		assert.Equal(t, utf8.RuneError, r)
		assert.Equal(t, 1, sz)

		assert.Equal(t, 2, utf16PosToByteOffset(bad, lspPosition{Line: 0, Character: 10}))
		assert.Equal(t, 1, utf16PosToByteOffset("x\r\ny", lspPosition{Line: 0, Character: 5}))
		assert.Equal(t, 4, utf16PosToByteOffset("x\r\ny", lspPosition{Line: 1, Character: 5}))
	})

	t.Run("readLoop_missing_content_length_breaks_with_error", func(t *testing.T) {
		t.Parallel()

		pr, pw := io.Pipe()
		pendingCh := make(chan rpcEnvelope, 1)
		client := &lspClient{
			pending:  map[string]chan rpcEnvelope{"n:1": pendingCh},
			notifyCh: make(chan struct{}, 1),
			doneCh:   make(chan struct{}),
		}
		go client.readLoop(pr)

		_, err := io.WriteString(pw, "Content-Type: application/vscode-jsonrpc\r\n\r\n{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":null}")
		require.NoError(t, err)
		_ = pw.Close()

		select {
		case <-client.doneCh:
		case <-time.After(2 * time.Second):
			t.Fatal("readLoop did not exit on frame missing Content-Length")
		}

		assert.Contains(t, client.readError(), "Content-Length")

		select {
		case resp := <-pendingCh:
			require.NotNil(t, resp.Error)
			assert.Contains(t, resp.Error.Message, "Content-Length")
		case <-time.After(2 * time.Second):
			t.Fatal("pending request was not failed when readLoop broke on missing Content-Length")
		}
	})
}
