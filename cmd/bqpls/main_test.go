package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunHelpAndVersion(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"-h"},
		{"--help"},
		{"serve", "-h"},
		{"install", "-h"},
		{"export", "-h"},
		{"version", "-h"},
	} {
		var stdout, stderr bytes.Buffer
		code := run(t.Context(), args, strings.NewReader(""), &stdout, &stderr)
		assert.Equal(t, 0, code, "args=%v stderr=%s", args, stderr.String())
	}

	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{"version"}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 0, code, stderr.String())
	assert.Contains(t, strings.ToLower(stdout.String()), "bqpls")
	assert.Empty(t, stderr.String())

	stdout.Reset()
	stderr.Reset()
	code = run(t.Context(), []string{"version", "extra"}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 2, code)
	assert.NotEmpty(t, stderr.String())
}

func TestRunUsageErrorsReturn2(t *testing.T) {
	t.Parallel()

	ws := t.TempDir()
	cases := [][]string{
		{"unknown-subcommand"},
		{"serve", "-unknown-flag"},
		{"serve", "extra-arg"},
		{"install"},
		{"install", "bogus"},
		{"install", "nvim", "extra"},
		{"install", "-bin-path", "/bin/bqpls"},
		{"install", "-unknown", "nvim"},
		{"export", "-target", "bogus", "-o", filepath.Join(ws, "out.vsix")},
		{"export", "-target", "vsix"},
		{"export", "-target", "vsix", "-o", filepath.Join(ws, "out.vsix"), "extra"},
		{"export", "-unknown"},
	}

	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		code := run(t.Context(), args, strings.NewReader(""), &stdout, &stderr)
		assert.Equal(t, 2, code, "args=%v stdout=%s", args, stdout.String())
		assert.NotEmpty(t, strings.TrimSpace(stderr.String()), "args=%v", args)
	}
}

func TestRunInstallFlagOrderingAndAll(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("PATH", "/nonexistent")

	packInit := filepath.Join(xdg, "nvim", "site", "pack", "bqlang", "start", "bqpls.nvim", "lua", "bqpls", "init.lua")

	// 1. Target before -bin-path.
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{"install", "nvim", "-bin-path", "/opt/first/bqpls"}, strings.NewReader(""), &stdout, &stderr)
	require.Equal(t, 0, code, stderr.String())
	raw, err := os.ReadFile(packInit)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"/opt/first/bqpls"`)

	// 2. -bin-path before target.
	stdout.Reset()
	stderr.Reset()
	code = run(t.Context(), []string{"install", "-bin-path", "/opt/second/bqpls", "nvim"}, strings.NewReader(""), &stdout, &stderr)
	require.Equal(t, 0, code, stderr.String())
	raw, err = os.ReadFile(packInit)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"/opt/second/bqpls"`)

	// 3. install all installs vscode, kiro, and nvim without short-circuiting.
	stdout.Reset()
	stderr.Reset()
	code = run(t.Context(), []string{"install", "all", "-bin-path", "/opt/all/bqpls"}, strings.NewReader(""), &stdout, &stderr)
	require.Equal(t, 0, code, stderr.String())
	raw, err = os.ReadFile(packInit)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"/opt/all/bqpls"`)
	kiroMatches, err := filepath.Glob(filepath.Join(home, ".kiro", "extensions", "paulourio.bqlang-*"))
	require.NoError(t, err)
	assert.NotEmpty(t, kiroMatches)
	vscMatches, err := filepath.Glob(filepath.Join(home, ".vscode", "extensions", "paulourio.bqlang-*"))
	require.NoError(t, err)
	assert.NotEmpty(t, vscMatches)
}

func TestRunInstallVSCodeKiroAndAllFailureExit1(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	binDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("PATH", binDir)

	codeBin := filepath.Join(binDir, "code")
	kiroBin := filepath.Join(binDir, "kiro")
	require.NoError(t, os.WriteFile(codeBin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(t, os.WriteFile(kiroBin, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	// 1. install vscode success.
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{"install", "vscode", "-bin-path", "/opt/bqpls"}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 0, code, stderr.String())
	assert.Contains(t, stdout.String(), "VS Code")

	// 2. install kiro success.
	stdout.Reset()
	stderr.Reset()
	code = run(t.Context(), []string{"install", "kiro", "-bin-path", "/opt/bqpls"}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 0, code, stderr.String())
	assert.Contains(t, stdout.String(), "Kiro")

	// 3. install vscode failure returns exit code 1.
	require.NoError(t, os.WriteFile(codeBin, []byte("#!/bin/sh\necho 'code failed' >&2\nexit 1\n"), 0o755))
	stdout.Reset()
	stderr.Reset()
	code = run(t.Context(), []string{"install", "vscode"}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "bqpls install vscode")
	assert.Contains(t, stderr.String(), "code failed")

	// 4. install kiro failure returns exit code 1.
	require.NoError(t, os.WriteFile(kiroBin, []byte("#!/bin/sh\necho 'kiro failed' >&2\nexit 1\n"), 0o755))
	stdout.Reset()
	stderr.Reset()
	code = run(t.Context(), []string{"install", "kiro"}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "bqpls install kiro")
	assert.Contains(t, stderr.String(), "kiro failed")

	// 5. install all returns exit code 1 when vscode+kiro fail, while still installing nvim.
	stdout.Reset()
	stderr.Reset()
	code = run(t.Context(), []string{"install", "all", "-bin-path", "/opt/bqpls"}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "bqpls install all (vscode)")
	assert.Contains(t, stderr.String(), "bqpls install all (kiro)")
	assert.Contains(t, stdout.String(), "Neovim")
}

func TestRunExportTargetsAndError(t *testing.T) {
	t.Parallel()

	ws := t.TempDir()

	for _, tc := range []struct {
		target string
		out    string
		check  string
	}{
		{target: "vsix", out: filepath.Join(ws, "bqlang.vsix"), check: filepath.Join(ws, "bqlang.vsix")},
		{target: "vscode-dir", out: filepath.Join(ws, "vsc"), check: filepath.Join(ws, "vsc", "extension.js")},
		{target: "nvim", out: filepath.Join(ws, "nvim"), check: filepath.Join(ws, "nvim", "lua", "bqpls", "init.lua")},
	} {
		var stdout, stderr bytes.Buffer
		code := run(t.Context(), []string{"export", "-target", tc.target, "-o", tc.out}, strings.NewReader(""), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		_, err := os.Stat(tc.check)
		require.NoError(t, err)
	}

	roFile := filepath.Join(ws, "regular-file")
	require.NoError(t, os.WriteFile(roFile, []byte("x"), 0o600))

	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{"export", "-target", "vsix", "-o", filepath.Join(roFile, "sub", "out.vsix")}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 1, code)
	assert.NotEmpty(t, stderr.String())
}

func TestRunServeLifecycleAndLogFile(t *testing.T) {
	t.Parallel()

	ws := t.TempDir()
	cfgPath := filepath.Join(ws, ".bqfmt.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("[lsp]\nbigquery_api_enabled = false\n"), 0o600))

	logPath := filepath.Join(ws, "logs", "bqpls.log")

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	var stderr bytes.Buffer
	exitCh := make(chan int, 1)
	go func() {
		exitCh <- run(t.Context(), []string{"serve", "-config", cfgPath, "-log-file", logPath, "-debug"}, inR, outW, &stderr)
		_ = outW.Close()
	}()

	reader := bufio.NewReader(outR)

	sendLSPFrame(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{"capabilities": map[string]any{}},
	})
	initResp := readLSPFrame(t, reader)
	assert.Equal(t, json.Number("1"), initResp["id"])
	assert.Nil(t, initResp["error"])

	sendLSPFrame(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"method":  "initialized",
		"params":  map[string]any{},
	})

	sendLSPFrame(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":        "file://" + filepath.Join(ws, "q.sql"),
				"languageId": "bqlang",
				"version":    1,
				"text":       "SELECT 1\n",
			},
		},
	})

	// Drain publishDiagnostics notification before shutdown.
	diagNotif := readLSPFrame(t, reader)
	assert.Equal(t, "textDocument/publishDiagnostics", diagNotif["method"])

	sendLSPFrame(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "shutdown",
	})
	shutResp := readLSPFrame(t, reader)
	assert.Equal(t, json.Number("2"), shutResp["id"])
	assert.Nil(t, shutResp["error"])

	sendLSPFrame(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"method":  "exit",
	})
	_ = inW.Close()

	select {
	case code := <-exitCh:
		assert.Equal(t, 0, code, stderr.String())
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for bqpls serve exit")
	}

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.NotEmpty(t, strings.TrimSpace(string(logBytes)))
}

func TestRunServeBareAndExitWithoutShutdown(t *testing.T) {
	t.Parallel()

	// Bare invocation (args == nil) defaults to serve.
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var stderr bytes.Buffer

	exitCh := make(chan int, 1)
	go func() {
		exitCh <- run(t.Context(), nil, inR, outW, &stderr)
		_ = outW.Close()
	}()

	reader := bufio.NewReader(outR)
	sendLSPFrame(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{"capabilities": map[string]any{}},
	})
	_ = readLSPFrame(t, reader)

	sendLSPFrame(t, inW, map[string]any{
		"jsonrpc": "2.0",
		"method":  "exit",
	})
	_ = inW.Close()

	select {
	case code := <-exitCh:
		assert.Equal(t, 1, code)
		assert.Contains(t, stderr.String(), "shutdown")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for exit without shutdown")
	}
}

func TestRunServeBadConfigAndCancel(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{"serve", "-config", filepath.Join(t.TempDir(), "missing.toml")}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 1, code)
	assert.NotEmpty(t, stderr.String())

	ctx, cancel := context.WithCancel(t.Context())
	inR, inW := io.Pipe()
	defer func() {
		_ = inW.Close()
	}()
	var cancelOut, cancelErr bytes.Buffer

	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"-debug"}, inR, &cancelOut, &cancelErr)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	_ = inW.Close()

	select {
	case c := <-done:
		assert.Contains(t, []int{0, 1}, c)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for canceled serve")
	}
}

func sendLSPFrame(t *testing.T, w io.Writer, payload any) {
	t.Helper()

	body, err := json.Marshal(payload)
	require.NoError(t, err)

	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	_, err = io.WriteString(w, frame)
	require.NoError(t, err)
}

func readLSPFrame(t *testing.T, r *bufio.Reader) map[string]any {
	t.Helper()

	contentLen := -1
	for {
		line, err := r.ReadString('\n')
		require.NoError(t, err)
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			break
		}
		if after, ok := strings.CutPrefix(strings.ToLower(trimmed), "content-length:"); ok {
			n, convErr := strconv.Atoi(strings.TrimSpace(after))
			require.NoError(t, convErr)
			contentLen = n
		}
	}

	require.Positive(t, contentLen)
	buf := make([]byte, contentLen)
	_, err := io.ReadFull(r, buf)
	require.NoError(t, err)

	var out map[string]any
	dec := json.NewDecoder(bytes.NewReader(buf))
	dec.UseNumber()
	require.NoError(t, dec.Decode(&out))

	return out
}
