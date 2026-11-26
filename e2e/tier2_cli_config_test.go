package e2e_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- F1 Boundaries ---

func TestT2_F1_HiddenDotfileAndSkippedDirs(t *testing.T) {
	t.Parallel()

	bin := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	rawSQL := "select   1   as   a\n"
	hidden := filepath.Join(ws, ".hidden.sql")
	gitSQL := filepath.Join(ws, ".git", "x.sql")
	nmSQL := filepath.Join(ws, "node_modules", "y.sql")
	valid := filepath.Join(ws, "valid.sql.tpl")

	writeFile(t, hidden, rawSQL)
	writeFile(t, gitSQL, rawSQL)
	writeFile(t, nmSQL, rawSQL)
	writeFile(t, valid, rawSQL)

	res := runCLI(t, bin, ws, env.env, "", "-l", "-w", ws)
	require.Equal(t, 0, res.exitCode, res.stderr)
	assert.Contains(t, res.stdout, "valid.sql.tpl")
	assert.NotContains(t, res.stdout, ".hidden.sql")
	assert.NotContains(t, res.stdout, "x.sql")
	assert.NotContains(t, res.stdout, "y.sql")

	for _, untouched := range []string{hidden, gitSQL, nmSQL} {
		got, err := os.ReadFile(untouched)
		require.NoError(t, err)
		assert.Equal(t, rawSQL, string(got))
	}

	gotValid, err := os.ReadFile(valid)
	require.NoError(t, err)
	assert.Equal(t, "SELECT 1 AS a\n", string(gotValid))
}

func TestT2_F1_EmptyFileAndWhitespaceOnly(t *testing.T) {
	t.Parallel()

	bin := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	emptyPath := filepath.Join(ws, "empty.sql")
	wsPath := filepath.Join(ws, "ws.sql")
	writeFile(t, emptyPath, "")
	writeFile(t, wsPath, "   \n\t\n  ")

	resEmpty := runCLI(t, bin, ws, env.env, "", emptyPath)
	assert.Equal(t, 0, resEmpty.exitCode, resEmpty.stderr)

	resWS := runCLI(t, bin, ws, env.env, "", wsPath)
	assert.Equal(t, 0, resWS.exitCode, resWS.stderr)
}

func TestT2_F1_SymlinkTargetSafety(t *testing.T) {
	t.Parallel()

	bin := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	target := filepath.Join(ws, "real.data")
	writeFile(t, target, "select          1          as          x          from          t\n")
	require.NoError(t, os.Chmod(target, 0o640))

	link := filepath.Join(ws, "link.sql")
	require.NoError(t, os.Symlink(target, link))

	res := runCLI(t, bin, ws, env.env, "", "-w", link)
	require.Equal(t, 0, res.exitCode, res.stderr)

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "SELECT 1 AS x\n  FROM t\n", string(got))

	info, err := os.Stat(target)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
}

func TestT2_F1_MultipleFilesIsolateErrors(t *testing.T) {
	t.Parallel()

	bin := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	good1 := filepath.Join(ws, "good1.sql")
	broken := filepath.Join(ws, "broken.sql")
	good2 := filepath.Join(ws, "good2.sql")

	writeFile(t, good1, "select 1 as a\n")
	writeFile(t, broken, "SELECT ((( FROM\n")
	writeFile(t, good2, "select 2 as b\n")

	res := runCLI(t, bin, ws, env.env, "", good1, broken, good2)
	assert.Equal(t, 2, res.exitCode)
	assert.Contains(t, res.stdout, "SELECT 1 AS a")
	assert.Contains(t, res.stdout, "SELECT 2 AS b")
	assert.Contains(t, res.stderr, "broken.sql")
}

func TestT2_F1_ExplicitNonBQLFileFormatted(t *testing.T) {
	t.Parallel()

	bin := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	txt := filepath.Join(ws, "query.txt")
	writeFile(t, txt, "select 1 as a\n")

	res := runCLI(t, bin, ws, env.env, "", txt)
	require.Equal(t, 0, res.exitCode, res.stderr)
	assert.Equal(t, "SELECT 1 AS a\n", res.stdout)
}

// --- F2 Boundaries ---

func TestT2_F2_UnknownLSPKeyRejected(t *testing.T) {
	t.Parallel()

	bin := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	cfg := filepath.Join(ws, ".bqfmt.toml")
	writeFile(t, cfg, "[lsp]\nbad_key = true\n")

	res := runCLI(t, bin, ws, env.env, "SELECT 1\n", "-config", cfg)
	assert.Equal(t, 2, res.exitCode)
	assert.Contains(t, res.stderr, `unknown option "lsp.bad_key"`)
}

func TestT2_F2_NegativeCacheTTLRejected(t *testing.T) {
	t.Parallel()

	bin := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	cfg := filepath.Join(ws, ".bqfmt.toml")
	writeFile(t, cfg, "[lsp]\ncache_ttl_seconds = -5\n")

	res := runCLI(t, bin, ws, env.env, "SELECT 1\n", "-config", cfg)
	assert.Equal(t, 2, res.exitCode)
	assert.NotEmpty(t, strings.TrimSpace(res.stderr))
}

func TestT2_F2_ZeroCacheTTLDisablesDiskReuse(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws := t.TempDir()
	cacheDir := filepath.Join(ws, "cache")

	cached := usersSchema()
	cached.ID = "my-proj:analytics.zero_ttl_tbl"
	cached.TableReference.TableID = "zero_ttl_tbl"
	writeDiskCacheTable(t, cacheDir, cached, time.Now())

	cfg := fmt.Sprintf("[lsp]\ndefault_project = %q\ndefault_dataset = %q\nbigquery_api_enabled = false\ncache_dir = %q\ncache_ttl_seconds = 0\n",
		"my-proj", "analytics", filepath.ToSlash(cacheDir))
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), cfg)

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT country FROM zero_ttl_tbl"
	uri := pathToURI(t, filepath.Join(ws, "zero_ttl.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", sql, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev.Diagnostics, "uncached table should not emit false-positive table-not-found diagnostics")

	h, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "country"))
	require.Nil(t, rpcErr)
	assert.Nil(t, h, "expected nil hover when cache_ttl_seconds = 0 disables disk reuse")
}

func TestT2_F2_InvalidPartitionSeverityRejected(t *testing.T) {
	t.Parallel()

	bin := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	cfg := filepath.Join(ws, ".bqfmt.toml")
	writeFile(t, cfg, "[lsp]\nrequire_partition_filter_severity = \"critical\"\n")

	res := runCLI(t, bin, ws, env.env, "SELECT 1\n", "-config", cfg)
	assert.Equal(t, 2, res.exitCode)
	assert.NotEmpty(t, strings.TrimSpace(res.stderr))
}

func TestT2_F2_MalformedLSPConfigFallbackInBqpls(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), "[lsp]\nbad_key = [unclosed\n")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, filepath.Join(ws, "fallback.sql"))
	client.didOpen(t, uri, "bqlang", "SELECT 1 AS ok", 1)

	tok := client.semanticTokensFull(t, uri)
	_, err := decodeSemanticTokens(tok.Data)
	require.NoError(t, err)
}

// --- F3 Boundaries ---

func TestT2_F3_ExportVsixMissingOutputFlag(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	res := runCLI(t, bin, ws, env.env, "", "export", "-target", "vsix")
	assert.NotEqual(t, 0, res.exitCode)
	assert.NotEmpty(t, strings.TrimSpace(res.stderr))
}

func TestT2_F3_ExportToReadOnlyOrInvalidPath(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	roFile := filepath.Join(ws, "not_a_dir")
	writeFile(t, roFile, "file")

	res := runCLI(t, bin, ws, env.env, "", "export", "-target", "vsix", "-o", filepath.Join(roFile, "sub", "out.vsix"))
	assert.NotEqual(t, 0, res.exitCode)
	assert.NotEmpty(t, strings.TrimSpace(res.stderr))
}

func TestT2_F3_InstallKiroHeadlessFallback(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t, "PATH=/usr/bin:/bin")
	ws := t.TempDir()

	res := runCLI(t, bin, ws, env.env, "", "install", "kiro")
	require.Equal(t, 0, res.exitCode, "expected headless Kiro install fallback to succeed; stderr:\n%s", res.stderr)

	matches, err := filepath.Glob(filepath.Join(env.home, ".kiro", "extensions", "paulourio.bqlang-*"))
	require.NoError(t, err)
	assert.NotEmpty(t, matches, "expected headless Kiro extension directory under ~/.kiro/extensions")
}

func TestT2_F3_InstallAllWithTempHomeAndXDG(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	res := runCLI(t, bin, ws, env.env, "", "install", "all", "-bin-path", bin)
	assert.NotContains(t, res.stderr, "panic:")

	nvimLua := filepath.Join(env.xdgData, "nvim", "site", "pack", "bqlang", "start", "bqpls.nvim", "lua", "bqpls", "init.lua")
	_, err := os.Stat(nvimLua)
	require.NoError(t, err, "expected Neovim plugin to be installed unconditionally by install all")

	if res.exitCode != 0 {
		// If VS Code / Kiro CLI failed in an environment without IDE dirs, stderr must be explicit.
		assert.NotEmpty(t, strings.TrimSpace(res.stderr))
	}
}

func TestT2_F3_ServeWithExplicitConfigAndLogFile(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws := t.TempDir()
	schemaDir := filepath.Join(ws, "custom_schemas")
	customEvents := standardEventsSchema()
	customEvents.ID = "custom-proj:custom_ds.events"
	customEvents.TableReference.ProjectID = "custom-proj"
	customEvents.TableReference.DatasetID = "custom_ds"
	writeSchemaTableJSON(t, filepath.Join(schemaDir, "custom-proj.custom_ds.events.json"), customEvents)

	customCfg := filepath.Join(t.TempDir(), "custom.bqfmt.toml")
	writeFile(t, customCfg, fmt.Sprintf("[lsp]\ndefault_project = %q\ndefault_dataset = %q\nschema_dirs = [%q]\nbigquery_api_enabled = false\n",
		"custom-proj", "custom_ds", schemaDir))

	logPath := filepath.Join(t.TempDir(), "bqpls.log")
	client := startLSP(t, ws, env.env, "-config", customCfg, "-log-file", logPath, "-debug")
	client.initialize(t, ws)

	uri := pathToURI(t, filepath.Join(ws, "custom.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", "SELECT tenant_id FROM events WHERE event_date = '2026-01-01' AND tenant_id = 't1'", 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev.Diagnostics)
	assert.Equal(t, 0, client.shutdownAndExit(t))

	logData, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.NotEmpty(t, strings.TrimSpace(string(logData)))
}

// --- F12 Boundaries ---

func TestT2_F12_TextMateNoForbiddenConstructs(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()
	vsixPath := filepath.Join(ws, "bqlang.vsix")

	res := runCLI(t, bin, ws, env.env, "", "export", "-target", "vsix", "-o", vsixPath)
	require.Equal(t, 0, res.exitCode, res.stderr)

	rep := inspectVSIX(t, vsixPath)
	forbidden := []string{"(?=", "(?!", "(?<=", "(?<!", "*+", "++", "?+", "(?>", `\1`}

	for _, pat := range rep.patterns {
		for _, bad := range forbidden {
			assert.NotContains(t, pat, bad, "pattern %q contains forbidden non-RE2 construct %q", pat, bad)
		}
	}
}

func TestT2_F12_ExtensionJSContainsNoExternalRequires(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()
	vsixPath := filepath.Join(ws, "bqlang.vsix")

	res := runCLI(t, bin, ws, env.env, "", "export", "-target", "vsix", "-o", vsixPath)
	require.Equal(t, 0, res.exitCode, res.stderr)

	rep := inspectVSIX(t, vsixPath)
	allowedBuiltins := map[string]bool{
		"vscode":        true,
		"child_process": true,
		"path":          true,
		"fs":            true,
		"os":            true,
		"util":          true,
		"process":       true,
		"events":        true,
		"stream":        true,
	}

	reqRe := regexp.MustCompile(`require\(\s*['"]([^'"]+)['"]\s*\)`)
	matches := reqRe.FindAllStringSubmatch(rep.extensionJS, -1)
	require.NotEmpty(t, matches, "expected extension.js to require vscode/node modules")

	for _, m := range matches {
		mod := strings.TrimPrefix(m[1], "node:")
		assert.True(t, allowedBuiltins[mod], "extension.js must not require external dependency %q", m[1])
	}
}

func TestT2_F12_NeovimFtdetectAllSixSuffixes(t *testing.T) {
	t.Parallel()

	nvimBin, err := exec.LookPath("nvim")
	if err != nil {
		t.Skip("nvim not installed in PATH")
	}

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	inst := runCLI(t, bin, ws, env.env, "", "install", "nvim", "-bin-path", bin)
	require.Equal(t, 0, inst.exitCode, inst.stderr)

	files := []string{"a.bql", "b.sql", "c.bql.j2", "d.sql.j2", "e.bql.tpl", "f.sql.tpl"}
	for _, name := range files {
		writeFile(t, filepath.Join(ws, name), "SELECT 1\n")
	}

	luaScript := filepath.Join(ws, "check_ft.lua")
	luaBody := fmt.Sprintf(`
local ok, err = xpcall(function()
  local files = { %q, %q, %q, %q, %q, %q }
  for _, f in ipairs(files) do
    vim.cmd.edit(vim.fn.fnameescape(f))
    assert(vim.bo.filetype == "bqlang", "expected filetype=bqlang for " .. f .. ", got " .. tostring(vim.bo.filetype))
  end
end, debug.traceback)
if not ok then
  io.stderr:write(tostring(err) .. "\n")
  vim.cmd("cquit 1")
end
vim.cmd("qa!")
`,
		filepath.Join(ws, files[0]),
		filepath.Join(ws, files[1]),
		filepath.Join(ws, files[2]),
		filepath.Join(ws, files[3]),
		filepath.Join(ws, files[4]),
		filepath.Join(ws, files[5]),
	)
	writeFile(t, luaScript, luaBody)

	res := runCLI(t, nvimBin, ws, env.env, "", "--headless", "-u", "NORC", "-i", "NONE", "-S", luaScript)
	require.Equal(t, 0, res.exitCode, "stdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
}

func TestT2_F12_NeovimSyntaxAndHighlightGroups(t *testing.T) {
	t.Parallel()

	nvimBin, err := exec.LookPath("nvim")
	if err != nil {
		t.Skip("nvim not installed in PATH")
	}

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	inst := runCLI(t, bin, ws, env.env, "", "install", "nvim", "-bin-path", bin)
	require.Equal(t, 0, inst.exitCode, inst.stderr)

	sqlFile := filepath.Join(ws, "syntax_check.sql")
	writeFile(t, sqlFile, "SELECT 1\n-- comment\n")

	luaScript := filepath.Join(ws, "check_syn.lua")
	luaBody := fmt.Sprintf(`
local ok, err = xpcall(function()
  vim.cmd("syntax on")
  vim.cmd.edit(vim.fn.fnameescape(%q))
  assert(vim.bo.filetype == "bqlang", "expected filetype=bqlang")
  assert(vim.b.current_syntax == "bqlang", "expected b:current_syntax=bqlang, got " .. tostring(vim.b.current_syntax))
  assert(vim.bo.commentstring == "-- %%s", "expected commentstring='-- %%s', got " .. tostring(vim.bo.commentstring))
  local hp = vim.api.nvim_get_hl(0, { name = "@lsp.mod.partitioned.bqlang" })
  assert(next(hp) ~= nil, "expected @lsp.mod.partitioned.bqlang highlight group")
  local hc = vim.api.nvim_get_hl(0, { name = "@lsp.mod.clustered.bqlang" })
  assert(next(hc) ~= nil, "expected @lsp.mod.clustered.bqlang highlight group")
end, debug.traceback)
if not ok then
  io.stderr:write(tostring(err) .. "\n")
  vim.cmd("cquit 1")
end
vim.cmd("qa!")
`, sqlFile)
	writeFile(t, luaScript, luaBody)

	res := runCLI(t, nvimBin, ws, env.env, "", "--headless", "-u", "NORC", "-i", "NONE", "-S", luaScript)
	require.Equal(t, 0, res.exitCode, "stdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
}

func TestT2_F12_NeovimMissingXDGDataHomeFallback(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t, "XDG_DATA_HOME=")
	ws := t.TempDir()

	res := runCLI(t, bin, ws, env.env, "", "install", "nvim", "-bin-path", bin)
	require.Equal(t, 0, res.exitCode, res.stderr)

	fallbackInit := filepath.Join(env.home, ".local", "share", "nvim", "site", "pack", "bqlang", "start", "bqpls.nvim", "lua", "bqpls", "init.lua")
	_, err := os.Stat(fallbackInit)
	assert.NoError(t, err)
}
