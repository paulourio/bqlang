package e2e_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestT4_W1_BigQueryAnalyticsWorkload(t *testing.T) {
	t.Parallel()

	bqfmt := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws, cfgPath := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	initialSQL := strings.Join([]string{
		"WITH recent_events AS (",
		"  SELECT e.event_date, e.tenant_id, e.user_ctx.user_id, u.country",
		"  FROM events e",
		"  JOIN users u ON e.user_ctx.user_id = u.user_id",
		")",
		"SELECT r.event_date, r.tenant_id, r.country",
		"FROM recent_events r",
		"QUALIFY ROW_NUMBER() OVER (PARTITION BY r.tenant_id ORDER BY r.event_date DESC) = 1",
	}, "\n")

	sqlPath := filepath.Join(ws, "analytics.sql")
	writeFile(t, sqlPath, initialSQL)
	uri := pathToURI(t, sqlPath)

	// Step 1: missing partition and cluster filters produce warnings.
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", initialSQL, 1)
	cur, ev1 := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	require.Len(t, ev1.Diagnostics, 2)
	assert.Contains(t, strings.ToLower(ev1.Diagnostics[1].Message), "partition")

	// Step 2: add WHERE e.event_date >= '2026-01-01' AND e.tenant_id = 't1' via incremental didChange -> clears warnings.
	client.didChange(t, uri, 2, lspContentChange{
		Range: &lspRange{
			Start: lspPosition{Line: 3, Character: len("  JOIN users u ON e.user_ctx.user_id = u.user_id")},
			End:   lspPosition{Line: 3, Character: len("  JOIN users u ON e.user_ctx.user_id = u.user_id")},
		},
		Text: "\n  WHERE e.event_date >= '2026-01-01' AND e.tenant_id = 't1'",
	})
	_, ev2 := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev2.Diagnostics)

	fixedSQL := strings.Join([]string{
		"WITH recent_events AS (",
		"  SELECT e.event_date, e.tenant_id, e.user_ctx.user_id, u.country",
		"  FROM events e",
		"  JOIN users u ON e.user_ctx.user_id = u.user_id",
		"  WHERE e.event_date >= '2026-01-01' AND e.tenant_id = 't1'",
		")",
		"SELECT r.event_date, r.tenant_id, r.country",
		"FROM recent_events r",
		"QUALIFY ROW_NUMBER() OVER (PARTITION BY r.tenant_id ORDER BY r.event_date DESC) = 1",
	}, "\n")

	// Step 3: semantic tokens + modifiers.
	tok := client.semanticTokensFull(t, uri)
	decoded, err := decodeSemanticTokens(tok.Data)
	require.NoError(t, err)
	evTok, ok := findTokenAt(decoded, utf16PosAt(t, fixedSQL, "event_date"))
	require.True(t, ok)
	assert.NotZero(t, evTok.tokenModifiers&modPartitioned)
	tenTok, ok := findTokenAt(decoded, utf16PosAt(t, fixedSQL, "tenant_id"))
	require.True(t, ok)
	assert.NotZero(t, tenTok.tokenModifiers&modClustered)

	// Step 4: hover on table & nested struct field.
	hTbl, rpcErr := client.hover(t, uri, utf16PosAt(t, fixedSQL, "events e"))
	require.Nil(t, rpcErr)
	require.NotNil(t, hTbl)
	assert.Contains(t, hTbl.Contents.Value, "Raw clickstream events table")

	hField, rpcErr := client.hover(t, uri, utf16PosAt(t, fixedSQL, "user_id"))
	require.Nil(t, rpcErr)
	require.NotNil(t, hField)
	assert.Contains(t, hField.Contents.Value, "Numeric user ID")

	// Step 5: definition & document symbols & formatting parity.
	locs := client.definition(t, uri, utf16PosAtNth(t, fixedSQL, "recent_events", 1))
	require.NotEmpty(t, locs)
	assert.Equal(t, 0, locs[0].Range.Start.Line)

	syms, _ := client.documentSymbols(t, uri)
	assert.Contains(t, strings.Join(flattenSymbolNames(syms), ","), "recent_events")

	edits, rpcErr := client.formatting(t, uri, 2, true)
	require.Nil(t, rpcErr)
	formatted := applyTextEdits(fixedSQL, edits)

	cliRes := runCLI(t, bqfmt, ws, env.env, fixedSQL, "-config", cfgPath, "-stdin-filename", sqlPath)
	require.Equal(t, 0, cliRes.exitCode, cliRes.stderr)
	assert.Equal(t, cliRes.stdout, formatted)
}

func TestT4_W2_DbtIncrementalJinjaPipeline(t *testing.T) {
	t.Parallel()

	bqfmt := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws, cfgPath := setupStandardWorkspace(t, "")

	fctSQL := strings.Join([]string{
		"{{ config(materialized='incremental', unique_key='tenant_id') }}",
		"{# dbt incremental events model #}",
		"{% macro active_cols() %}event_date, tenant_id{% endmacro %}",
		"{% set lookback_days = 7 %}",
		"select {{ active_cols() }}",
		"{% for extra in ['event_type'] %}",
		"  , {{ extra }}",
		"{% endfor %}",
		"from events",
		"{% if is_incremental() %}",
		"where event_date >= date_sub(current_date(), interval {{ lookback_days }} day)",
		"{% else %}",
		"where event_date >= '2025-01-01'",
		"{% endif %}",
		"",
	}, "\n")

	stgSQL := "select event_date, tenant_id from events where event_date = '2026-01-01'\n"

	fctPath := filepath.Join(ws, "models", "fct_events.sql.j2")
	stgPath := filepath.Join(ws, "models", "stg_events.sql.tpl")
	writeFile(t, fctPath, fctSQL)
	writeFile(t, stgPath, stgSQL)

	fmtRes := runCLI(t, bqfmt, ws, env.env, "", "-config", cfgPath, "-l", "-w", filepath.Join(ws, "models"))
	require.Equal(t, 0, fmtRes.exitCode, fmtRes.stderr)

	formattedFct, err := os.ReadFile(fctPath)
	require.NoError(t, err)
	fctText := string(formattedFct)

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, fctPath)
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", fctText, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	for _, d := range ev.Diagnostics {
		assert.NotContains(t, strings.ToLower(d.Message), "where", "secondary else branch should not cause duplicate WHERE error")
	}

	tok := client.semanticTokensFull(t, uri)
	decoded, decErr := decodeSemanticTokens(tok.Data)
	require.NoError(t, decErr)
	cfgTok, ok := findTokenAt(decoded, utf16PosAt(t, fctText, "config"))
	require.True(t, ok)
	assert.Equal(t, tokMacro, cfgTok.tokenType)

	wantMacLine := utf16PosAtNth(t, fctText, "active_cols", 0).Line
	require.Equal(t, 3, wantMacLine)
	locsMac := client.definition(t, uri, utf16PosAtNth(t, fctText, "active_cols", 1))
	require.NotEmpty(t, locsMac)
	assert.Equal(t, wantMacLine, locsMac[0].Range.Start.Line)

	wantVarLine := utf16PosAtNth(t, fctText, "lookback_days", 0).Line
	require.Equal(t, 4, wantVarLine)
	locsVar := client.definition(t, uri, utf16PosAtNth(t, fctText, "lookback_days", 1))
	require.NotEmpty(t, locsVar)
	assert.Equal(t, wantVarLine, locsVar[0].Range.Start.Line)

	syms, _ := client.documentSymbols(t, uri)
	assert.Contains(t, strings.Join(flattenSymbolNames(syms), ","), "active_cols")

	edits, rpcErr := client.formatting(t, uri, 2, true)
	require.Nil(t, rpcErr)
	assert.Empty(t, edits)
}

func TestT4_W3_ProceduralScriptWorkload(t *testing.T) {
	t.Parallel()

	bqfmt := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws, cfgPath := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	script := strings.Join([]string{
		"DECLARE target_date DATE DEFAULT DATE '2026-01-01';",
		"CREATE TEMP TABLE stage_events AS",
		"SELECT event_date, tenant_id, user_ctx.user_id",
		"FROM events",
		"WHERE event_date = target_date AND tenant_id = 't1';",
		"BEGIN",
		"  SELECT tenant_id, COUNT(*) AS cnt",
		"  FROM stage_events",
		"  GROUP BY 1;",
		"END;",
	}, "\n")

	sqlPath := filepath.Join(ws, "procedural.sql")
	uri := pathToURI(t, sqlPath)
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", script, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev.Diagnostics)

	hVar, rpcErr := client.hover(t, uri, utf16PosAtNth(t, script, "target_date", 1))
	require.Nil(t, rpcErr)
	require.NotNil(t, hVar)
	assert.Contains(t, hVar.Contents.Value, "DATE")

	hTmp, rpcErr := client.hover(t, uri, utf16PosAtNth(t, script, "stage_events", 1))
	require.Nil(t, rpcErr)
	require.NotNil(t, hTmp)
	assert.Contains(t, hTmp.Contents.Value, "stage_events")

	locsVar := client.definition(t, uri, utf16PosAtNth(t, script, "target_date", 1))
	require.NotEmpty(t, locsVar)
	assert.Equal(t, 0, locsVar[0].Range.Start.Line)

	locsTmp := client.definition(t, uri, utf16PosAtNth(t, script, "stage_events", 1))
	require.NotEmpty(t, locsTmp)
	assert.Equal(t, 1, locsTmp[0].Range.Start.Line)

	syms, _ := client.documentSymbols(t, uri)
	names := strings.Join(flattenSymbolNames(syms), ",")
	assert.Contains(t, names, "stage_events")

	edits, rpcErr := client.formatting(t, uri, 2, true)
	require.Nil(t, rpcErr)
	formatted := applyTextEdits(script, edits)
	assert.Contains(t, formatted, "BEGIN")
	assert.Contains(t, formatted, "\nEND")

	cliRes := runCLI(t, bqfmt, ws, env.env, script, "-config", cfgPath, "-stdin-filename", sqlPath)
	require.Equal(t, 0, cliRes.exitCode, cliRes.stderr)
	assert.Equal(t, cliRes.stdout, formatted)
}

func TestT4_W4_PipeSyntaxWorkflow(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	pipeSQL := strings.Join([]string{
		"FROM events",
		"|> WHERE event_date = '2026-01-01' AND tenant_id LIKE 'acme%'",
		"|> EXTEND UPPER(event_type) AS event_type_upper",
		"|> AGGREGATE COUNT(*) AS cnt GROUP BY tenant_id, event_type_upper",
		"|> ORDER BY cnt DESC",
		"|> LIMIT 10",
	}, "\n")

	uri := pathToURI(t, filepath.Join(ws, "pipe.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", pipeSQL, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev.Diagnostics)

	tok := client.semanticTokensFull(t, uri)
	decoded, err := decodeSemanticTokens(tok.Data)
	require.NoError(t, err)

	evTok, ok := findTokenAt(decoded, utf16PosAt(t, pipeSQL, "event_date"))
	require.True(t, ok)
	assert.NotZero(t, evTok.tokenModifiers&modPartitioned)

	tenTok, ok := findTokenAt(decoded, utf16PosAt(t, pipeSQL, "tenant_id"))
	require.True(t, ok)
	assert.NotZero(t, tenTok.tokenModifiers&modClustered)

	// Open second buffer with cursor after EXTEND |> to verify OutputColumnList() completion.
	pipeCompSQL := "FROM events\n|> WHERE event_date = '2026-01-01'\n|> EXTEND UPPER(event_type) AS event_type_upper\n|> "
	uriComp := pathToURI(t, filepath.Join(ws, "pipe_comp.sql"))
	client.didOpen(t, uriComp, "bqlang", pipeCompSQL, 1)
	labels := completionLabels(client.completion(t, uriComp, utf16PosAfter(t, pipeCompSQL, "event_type_upper\n|> ")))
	assert.Contains(t, labels, "event_type_upper")
	assert.Contains(t, labels, "tenant_id")

	edits, rpcErr := client.formatting(t, uri, 2, true)
	require.Nil(t, rpcErr)
	assert.Contains(t, applyTextEdits(pipeSQL, edits), "|>")
}

func TestT4_W5_HeadlessNeovimRealSmokeTest(t *testing.T) {
	t.Parallel()

	nvimBin, err := exec.LookPath("nvim")
	if err != nil {
		t.Skip("nvim not installed in PATH")
	}

	bqpls := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	inst := runCLI(t, bqpls, ws, env.env, "", "install", "nvim", "-bin-path", bqpls)
	require.Equal(t, 0, inst.exitCode, inst.stderr)

	sqlPath := filepath.Join(ws, "query.sql")
	writeFile(t, sqlPath, "SELECT event_date, tenant_id\nFROM events\nWHERE event_date = '2026-01-01'\n")

	j2Path := filepath.Join(ws, "model.sql.j2")
	writeFile(t, j2Path, "-- dbt model\nSELECT tenant_id FROM {{ ref('events') }}\n")

	luaPath := filepath.Join(ws, "verify_e2e.lua")
	luaScript := fmt.Sprintf(`
local ok, err = xpcall(function()
  vim.cmd("syntax on")

  -- 1. Open pure .sql file and verify syntax + LSP attach + semantic tokens + hover
  vim.cmd.edit(vim.fn.fnameescape(%q))
  assert(vim.bo.filetype == "bqlang", "expected .sql filetype=bqlang, got " .. tostring(vim.bo.filetype))
  assert(vim.b.current_syntax == "bqlang", "expected b:current_syntax=bqlang")
  assert(vim.bo.commentstring == "-- %%s", "expected commentstring='-- %%s'")

  local hp = vim.api.nvim_get_hl(0, { name = "@lsp.mod.partitioned.bqlang" })
  assert(next(hp) ~= nil, "missing @lsp.mod.partitioned.bqlang highlight")
  local hc = vim.api.nvim_get_hl(0, { name = "@lsp.mod.clustered.bqlang" })
  assert(next(hc) ~= nil, "missing @lsp.mod.clustered.bqlang highlight")

  local attached = vim.wait(8000, function()
    return #vim.lsp.get_clients({ bufnr = 0 }) > 0
  end, 50)
  assert(attached, "timed out waiting for bqpls LSP attachment on query.sql")

  local client = vim.lsp.get_clients({ bufnr = 0 })[1]
  assert(client.server_capabilities.semanticTokensProvider ~= nil, "missing semanticTokensProvider")
  assert(client.server_capabilities.hoverProvider ~= nil, "missing hoverProvider")

  local sem_ok = vim.wait(8000, function()
    local toks = vim.lsp.semantic_tokens.get_at_pos(0, 0, 7)
    if not toks then return false end
    for _, tk in ipairs(toks) do
      if tk.modifiers and tk.modifiers.partitioned then
        return true
      end
    end
    return false
  end, 50)
  assert(sem_ok, "expected partitioned semantic token modifier at event_date in Neovim")

  vim.api.nvim_win_set_cursor(0, { 1, 7 })
  local hov = client:request_sync("textDocument/hover", vim.lsp.util.make_position_params(0, client.offset_encoding), 5000, 0)
  assert(hov and hov.result and hov.result.contents, "expected hover result from bqpls in Neovim")
  local hval = type(hov.result.contents) == "table" and hov.result.contents.value or tostring(hov.result.contents)
  assert(hval:find("event_date", 1, true) ~= nil, "expected event_date in Neovim hover: " .. hval)

  -- 2. Open .sql.j2 file and verify filetype + syntax + LSP attach
  vim.cmd.edit(vim.fn.fnameescape(%q))
  assert(vim.bo.filetype == "bqlang", "expected .sql.j2 filetype=bqlang, got " .. tostring(vim.bo.filetype))
  assert(vim.b.current_syntax == "bqlang", "expected b:current_syntax=bqlang on .sql.j2")

  local syn_id = vim.fn.synIDattr(vim.fn.synID(1, 1, 1), "name")
  assert(syn_id ~= "", "expected non-empty syntax group on .sql.j2 line 1")

  local j2_attached = vim.wait(8000, function()
    return #vim.lsp.get_clients({ bufnr = 0 }) > 0
  end, 50)
  assert(j2_attached, "timed out waiting for bqpls LSP attachment on model.sql.j2")
end, debug.traceback)
if not ok then
  io.stderr:write(tostring(err) .. "\n")
  vim.cmd("cquit 1")
end
vim.cmd("qa!")
`, sqlPath, j2Path)
	writeFile(t, luaPath, luaScript)

	res := runCLI(t, nvimBin, ws, env.env, "", "--headless", "-u", "NORC", "-i", "NONE", "-S", luaPath)
	require.Equal(t, 0, res.exitCode, "stdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
}

func TestT4_W6_VSCodeExtensionVsixRealInstall(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	vsixPath := filepath.Join(ws, "bqlang.vsix")
	vscDir := filepath.Join(ws, "vscode-dir")

	resVsix := runCLI(t, bin, ws, env.env, "", "export", "-target", "vsix", "-o", vsixPath)
	require.Equal(t, 0, resVsix.exitCode, resVsix.stderr)

	resDir := runCLI(t, bin, ws, env.env, "", "export", "-target", "vscode-dir", "-o", vscDir)
	require.Equal(t, 0, resDir.exitCode, resDir.stderr)

	rep := inspectVSIX(t, vsixPath)
	assert.Equal(t, "paulourio", rep.packageJSON["publisher"])
	assert.Equal(t, "bqlang", rep.packageJSON["name"])

	codeBin, err := exec.LookPath("code")
	if err != nil {
		t.Skip("code CLI not installed in PATH")
	}

	extDir := filepath.Join(ws, "code-ext")
	userDir := filepath.Join(ws, "code-user")
	require.NoError(t, os.MkdirAll(extDir, 0o755))
	require.NoError(t, os.MkdirAll(userDir, 0o755))

	inst := runCLI(t, codeBin, ws, env.env, "", "--install-extension", vsixPath, "--extensions-dir", extDir, "--user-data-dir", userDir, "--force")
	require.Equal(t, 0, inst.exitCode, inst.stderr)

	list := runCLI(t, codeBin, ws, env.env, "", "--list-extensions", "--show-versions", "--extensions-dir", extDir, "--user-data-dir", userDir)
	require.Equal(t, 0, list.exitCode, list.stderr)
	assert.Contains(t, list.stdout, "paulourio.bqlang")

	_, statErr := os.Stat(filepath.Join(extDir, "extensions.json"))
	require.NoError(t, statErr)
}
