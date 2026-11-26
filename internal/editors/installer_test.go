package editors_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/internal/analysis"
	"github.com/paulourio/bqlang/internal/editors"
)

func TestNormalizeSemver(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{in: "", want: "0.1.0"},
		{in: "dev", want: "0.1.0"},
		{in: "(devel)", want: "0.1.0"},
		{in: "dev+abc123-dirty", want: "0.1.0"},
		{in: "v0.2.3", want: "0.2.3"},
		{in: "V1.4.0+meta", want: "1.4.0"},
		{in: "2.10.5-rc1", want: "2.10.5"},
		{in: "1.2", want: "0.1.0"},
		{in: "1.x.3", want: "0.1.0"},
	}

	for _, tc := range cases {
		assert.Equal(t, tc.want, editors.NormalizeSemver(tc.in), "input %q", tc.in)
	}
}

func TestParseExportTarget(t *testing.T) {
	t.Parallel()

	tgt, err := editors.ParseExportTarget("vsix")
	require.NoError(t, err)
	assert.Equal(t, editors.TargetVSIX, tgt)

	tgt, err = editors.ParseExportTarget("vscode-dir")
	require.NoError(t, err)
	assert.Equal(t, editors.TargetVSCodeDir, tgt)

	tgt, err = editors.ParseExportTarget("nvim")
	require.NoError(t, err)
	assert.Equal(t, editors.TargetNeovim, tgt)

	_, err = editors.ParseExportTarget("unknown")
	require.ErrorIs(t, err, editors.ErrInvalidExportTarget)
}

func TestBuildVSIXDeterministicAndValid(t *testing.T) {
	t.Parallel()

	opts := editors.Options{
		Version: "dev+abc123",
		BinPath: "/custom/bin/bqpls",
	}

	b1, err := editors.BuildVSIX(opts)
	require.NoError(t, err)
	b2, err := editors.BuildVSIX(opts)
	require.NoError(t, err)

	assert.Equal(t, sha256.Sum256(b1), sha256.Sum256(b2), "VSIX bytes must be deterministic")

	zr, err := zip.NewReader(bytes.NewReader(b1), int64(len(b1)))
	require.NoError(t, err)

	entries := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		rc, openErr := f.Open()
		require.NoError(t, openErr)
		var buf bytes.Buffer
		_, readErr := buf.ReadFrom(rc)
		_ = rc.Close()
		require.NoError(t, readErr)
		entries[f.Name] = buf.Bytes()
	}

	required := []string{
		"[Content_Types].xml",
		"extension.vsixmanifest",
		"extension/package.json",
		"extension/language-configuration.json",
		"extension/syntaxes/bqlang.tmLanguage.json",
		"extension/extension.js",
	}
	for _, name := range required {
		_, ok := entries[name]
		assert.True(t, ok, "missing VSIX entry %s", name)
	}

	var ctDoc any
	ctXML := string(entries["[Content_Types].xml"])
	require.NoError(t, xml.Unmarshal(entries["[Content_Types].xml"], &ctDoc))
	assert.Contains(t, ctXML, `Extension="json"`)
	assert.Contains(t, ctXML, `Extension="js"`)
	assert.Contains(t, ctXML, `Extension="vsixmanifest"`)
	assert.NotContains(t, ctXML, `Extension=".`)

	var manifestDoc any
	manifestXML := string(entries["extension.vsixmanifest"])
	require.NoError(t, xml.Unmarshal(entries["extension.vsixmanifest"], &manifestDoc))
	assert.Contains(t, manifestXML, "paulourio")
	assert.Contains(t, manifestXML, "bqlang")
	assert.Contains(t, manifestXML, "Microsoft.VisualStudio.Code")
	assert.Contains(t, manifestXML, `Version="0.1.0"`)

	var pkg map[string]any
	require.NoError(t, json.Unmarshal(entries["extension/package.json"], &pkg))
	assert.Equal(t, "bqlang", pkg["name"])
	assert.Equal(t, "paulourio", pkg["publisher"])
	assert.Equal(t, "0.1.0", pkg["version"])

	pkgStr := string(entries["extension/package.json"])
	for _, ext := range []string{".bql", ".sql", ".bql.j2", ".sql.j2", ".bql.tpl", ".sql.tpl"} {
		assert.Contains(t, pkgStr, ext)
	}
	assert.Contains(t, pkgStr, "semanticTokenModifiers")
	assert.Contains(t, pkgStr, "partitioned")
	assert.Contains(t, pkgStr, "clustered")
	assert.Contains(t, pkgStr, "pseudo")
	assert.Contains(t, pkgStr, "semanticTokenScopes")
	for _, setting := range []string{
		"bqpls.defaultProject",
		"bqpls.defaultDataset",
		"bqpls.schemaDirs",
		"bqpls.bigqueryApiEnabled",
		"bqpls.gcloudPath",
		"bqpls.cacheDir",
		"bqpls.cacheTtlSeconds",
		"bqpls.projectCacheTtlSeconds",
		"bqpls.diagnosticsEnabled",
		"bqpls.requirePartitionFilterSeverity",
		"bqpls.costEstimationEnabled",
		"bqpls.costCodeLensEnabled",
		"bqpls.costStatusBarEnabled",
		"bqpls.costHoverEnabled",
		"bqpls.costDiagnosticSeverity",
		"bqpls.costWarningThresholdBytes",
		"bqpls.costWarningThresholdUsd",
		"bqpls.costPricePerTibUsd",
		"bqpls.costDryRunDebounceMs",
		"bqpls.refreshCost",
	} {
		assert.Contains(t, pkgStr, setting)
	}

	var langCfg map[string]any
	require.NoError(t, json.Unmarshal(entries["extension/language-configuration.json"], &langCfg))

	extJS := string(entries["extension/extension.js"])
	assert.Contains(t, extJS, `"/custom/bin/bqpls"`)
	assert.NotContains(t, extJS, `"__BQPLS_BIN_PATH__"`)
	assert.Contains(t, extJS, "initializationOptions")
	assert.Contains(t, extJS, "workspace/didChangeConfiguration")
	assert.Contains(t, extJS, "registerSignatureHelpProvider")
	assert.Contains(t, extJS, "textDocument/signatureHelp")
	assert.Contains(t, extJS, "registerCodeLensProvider")
	assert.Contains(t, extJS, "textDocument/codeLens")
	assert.Contains(t, extJS, "workspace/codeLens/refresh")
	assert.Contains(t, extJS, "bqpls/queryCost")
	assert.Contains(t, extJS, "bqpls/refreshCost")
	assert.Contains(t, extJS, "createStatusBarItem")
}

func TestTextMateGrammarRE2AndSamples(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, editors.Export(editors.TargetVSCodeDir, dir, editors.Options{}))

	raw, err := os.ReadFile(filepath.Join(dir, "syntaxes", "bqlang.tmLanguage.json"))
	require.NoError(t, err)

	var grammar map[string]any
	require.NoError(t, json.Unmarshal(raw, &grammar))
	assert.Equal(t, "source.bqlang", grammar["scopeName"])

	var patterns []string
	collectTMPatterns(grammar, &patterns)
	require.NotEmpty(t, patterns)

	forbidden := []string{"(?=", "(?!", "(?<=", "(?<!", "*+", "++", "?+", "(?>", `\1`, `\G`, `\K`}
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pat := range patterns {
		for _, bad := range forbidden {
			assert.NotContains(t, pat, bad, "pattern %q contains forbidden construct %q", pat, bad)
		}

		re, compErr := regexp.Compile(pat)
		require.NoError(t, compErr, "RE2 compile failed for %q", pat)
		compiled = append(compiled, re)
	}

	samples := []struct {
		text      string
		minPrefix int
	}{
		{text: "-- line comment", minPrefix: 2},
		{text: "# hash comment", minPrefix: 1},
		{text: "// slash comment", minPrefix: 2},
		{text: "/* block comment */", minPrefix: 2},
		{text: "{# jinja comment #}", minPrefix: 2},
		{text: "{{ ref('orders') }}", minPrefix: 2},
		{text: "{% if is_incremental() %}", minPrefix: 2},
		{text: "{% raw %} literal {% endraw %}", minPrefix: 2},
		{text: "|>", minPrefix: 2},
		{text: "r'raw_str'", minPrefix: 2},
		{text: "'''triple'''", minPrefix: 3},
		{text: "@query_param", minPrefix: 2},
		{text: "@@system_var", minPrefix: 2},
	}

	for _, s := range samples {
		matched := false
		for _, re := range compiled {
			loc := re.FindStringIndex(s.text)
			if loc != nil && loc[0] == 0 && loc[1] >= s.minPrefix {
				matched = true

				break
			}
		}

		assert.True(t, matched, "no RE2 pattern matched prefix of %q", s.text)
	}

	// Verify dual-role function-vs-keyword disambiguation and ordering (#dual-role-functions -> #keywords -> #builtins).
	topPatterns, ok := grammar["patterns"].([]any)
	require.True(t, ok)
	idxDual, idxKw, idxBuiltin := -1, -1, -1
	for i, item := range topPatterns {
		m, isMap := item.(map[string]any)
		if !isMap {
			continue
		}
		switch m["include"] {
		case "#dual-role-functions":
			idxDual = i
		case "#keywords":
			idxKw = i
		case "#builtins":
			idxBuiltin = i
		}
	}
	require.GreaterOrEqual(t, idxDual, 0)
	require.Greater(t, idxKw, idxDual, "#dual-role-functions must precede #keywords")
	require.Greater(t, idxBuiltin, idxKw, "#keywords must precede #builtins")

	repo, ok := grammar["repository"].(map[string]any)
	require.True(t, ok)
	dualRepo := repo["dual-role-functions"].(map[string]any)["patterns"].([]any)[0].(map[string]any)
	dualRe := regexp.MustCompile(dualRepo["match"].(string))
	assert.Equal(t, "LEFT", dualRe.FindStringSubmatch("LEFT(s, 2)")[1])
	assert.Nil(t, dualRe.FindStringSubmatch("LEFT JOIN u"))
	assert.Equal(t, "LOG", dualRe.FindStringSubmatch("LOG(10)")[1])
	assert.Nil(t, dualRe.FindStringSubmatch("|> LOG"))
	assert.Equal(t, "REPLACE", dualRe.FindStringSubmatch("REPLACE(s, 'a', 'b')")[1])
	assert.Nil(t, dualRe.FindStringSubmatch("CREATE OR REPLACE TABLE t"))
}

func TestExtensionJSWhitelistLegendAndSyntax(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, editors.Export(editors.TargetVSCodeDir, dir, editors.Options{BinPath: ""}))

	jsPath := filepath.Join(dir, "extension.js")
	raw, err := os.ReadFile(jsPath)
	require.NoError(t, err)
	jsStr := string(raw)

	allowed := map[string]bool{
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
	matches := reqRe.FindAllStringSubmatch(jsStr, -1)
	require.NotEmpty(t, matches)

	for _, m := range matches {
		mod := strings.TrimPrefix(m[1], "node:")
		assert.True(t, allowed[mod], "unexpected require(%q)", m[1])
	}

	legend := analysis.DefaultSemanticTokensLegend()
	for _, tt := range legend.TokenTypes {
		assert.Contains(t, jsStr, "'"+tt+"'")
	}
	for _, mod := range legend.TokenModifiers {
		assert.Contains(t, jsStr, "'"+mod+"'")
	}

	assert.Contains(t, jsStr, "Number.isInteger(triggerContext.triggerKind)")
	assert.Contains(t, jsStr, "triggerContext.triggerKind + 1")
	assert.Contains(t, jsStr, "/^content-length:")

	if nodeBin, lookErr := exec.LookPath("node"); lookErr == nil {
		cmd := exec.CommandContext(t.Context(), nodeBin, "--check", jsPath)
		out, runErr := cmd.CombinedOutput()
		require.NoError(t, runErr, "node --check failed:\n%s", string(out))
	}
}

func TestExportAllTargetsAndValidation(t *testing.T) {
	t.Parallel()

	ws := t.TempDir()

	vsixPath := filepath.Join(ws, "out", "bqlang.vsix")
	require.NoError(t, editors.Export(editors.TargetVSIX, vsixPath, editors.Options{Version: "1.2.3"}))
	_, err := os.Stat(vsixPath)
	require.NoError(t, err)

	vscDir := filepath.Join(ws, "vscode")
	require.NoError(t, editors.Export(editors.TargetVSCodeDir, vscDir, editors.Options{BinPath: "/tmp/bqpls"}))
	for _, rel := range []string{"package.json", "language-configuration.json", filepath.Join("syntaxes", "bqlang.tmLanguage.json"), "extension.js"} {
		_, statErr := os.Stat(filepath.Join(vscDir, rel))
		require.NoError(t, statErr)
	}

	nvimDir := filepath.Join(ws, "nvim")
	require.NoError(t, editors.Export(editors.TargetNeovim, nvimDir, editors.Options{}))
	for _, rel := range []string{
		filepath.Join("ftdetect", "bqlang.lua"),
		filepath.Join("ftplugin", "bqlang.lua"),
		filepath.Join("syntax", "bqlang.vim"),
		filepath.Join("plugin", "bqpls.lua"),
		filepath.Join("lua", "bqpls", "init.lua"),
	} {
		_, statErr := os.Stat(filepath.Join(nvimDir, rel))
		require.NoError(t, statErr)
	}

	initLua, err := os.ReadFile(filepath.Join(nvimDir, "lua", "bqpls", "init.lua"))
	require.NoError(t, err)
	assert.Contains(t, string(initLua), `local stamped_bin_path = ""`)

	require.ErrorIs(t, editors.Export(editors.TargetVSIX, "", editors.Options{}), editors.ErrInvalidExportPath)
	require.ErrorIs(t, editors.Export(editors.ExportTarget("bad"), filepath.Join(ws, "x"), editors.Options{}), editors.ErrInvalidExportTarget)
}

func TestInstallKiroAndVSCodeHeadlessFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/nonexistent")

	require.NoError(t, editors.InstallKiro(t.Context(), editors.Options{
		Version: "0.1.0",
		BinPath: "/usr/local/bin/bqpls",
	}))

	kiroPkg := filepath.Join(home, ".kiro", "extensions", "paulourio.bqlang-0.1.0", "package.json")
	_, err := os.Stat(kiroPkg)
	require.NoError(t, err)

	require.NoError(t, editors.InstallVSCode(t.Context(), editors.Options{
		Version: "0.1.0",
		BinPath: "/usr/local/bin/bqpls",
	}))

	vscPkg := filepath.Join(home, ".vscode", "extensions", "paulourio.bqlang-0.1.0", "package.json")
	_, err = os.Stat(vscPkg)
	require.NoError(t, err)
}

func TestInstallNeovimXDGAndFallback(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", xdg)

	require.NoError(t, editors.InstallNeovim(editors.Options{BinPath: `/opt/"bqpls"\bin`}))

	packDir := filepath.Join(xdg, "nvim", "site", "pack", "bqlang", "start", "bqpls.nvim")
	initData, err := os.ReadFile(filepath.Join(packDir, "lua", "bqpls", "init.lua"))
	require.NoError(t, err)
	assert.Contains(t, string(initData), `"/opt/\"bqpls\"\\bin"`)
	assert.Contains(t, string(initData), "vim.lsp.start")
	assert.Contains(t, string(initData), "@lsp.mod.partitioned.bqlang")
	assert.Contains(t, string(initData), "@lsp.mod.clustered.bqlang")
	assert.Contains(t, string(initData), "bqpls/queryCost")
	assert.Contains(t, string(initData), "M.statusline")
	assert.Contains(t, string(initData), "M.query_cost")
	assert.Contains(t, string(initData), "M.refresh_cost")
	assert.Contains(t, string(initData), "BqplsRefreshCost")

	// Verify idempotent atomic overwrite on repeat install.
	require.NoError(t, editors.InstallNeovim(editors.Options{BinPath: "/usr/bin/bqpls"}))

	// Verify empty XDG_DATA_HOME falls back to ~/.local/share.
	t.Setenv("XDG_DATA_HOME", "")
	require.NoError(t, editors.InstallNeovim(editors.Options{BinPath: "/usr/bin/bqpls"}))

	fallbackInit := filepath.Join(home, ".local", "share", "nvim", "site", "pack", "bqlang", "start", "bqpls.nvim", "lua", "bqpls", "init.lua")
	_, err = os.Stat(fallbackInit)
	require.NoError(t, err)
}

func TestInstallNeovimSyntaxGroups(t *testing.T) {
	nvimBin, err := exec.LookPath("nvim")
	if err != nil {
		t.Skip("nvim not installed in PATH")
	}

	home := t.TempDir()
	xdgData := t.TempDir()
	xdgCfg := t.TempDir()
	xdgState := t.TempDir()
	xdgCache := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", xdgData)
	t.Setenv("XDG_CONFIG_HOME", xdgCfg)
	t.Setenv("XDG_STATE_HOME", xdgState)
	t.Setenv("XDG_CACHE_HOME", xdgCache)

	// Point stamped binary to a non-existent path so LSP attach skips cleanly while syntax loads.
	require.NoError(t, editors.InstallNeovim(editors.Options{BinPath: filepath.Join(home, "missing-bqpls")}))

	ws := t.TempDir()
	sqlPath := filepath.Join(ws, "syntax_probe.sql")
	sqlSrc := strings.Join([]string{
		"-- c",
		"# c",
		"// c",
		"/* c */",
		"'--'",
		"{% raw %}SELECT {{ untouched }} FROM t{% endraw %}",
		"{% if true %}",
		"{{ a + b }}",
		"|> WHERE x = 1",
		"SELECT CAST('1' AS TIME), LEFT(s, 2) FROM t LEFT JOIN u",
		"GRANT IMMEDIATE OPTIONAL SIMPLE",
		"'''multi",
		"line'''",
		"{%- if a - b == 1 -%}",
		"{{- x - y -}}",
		"{{ 10 % 3 }}",
		`{{ a \ b }}`,
		`SELECT \ FROM t`,
		"'unclosed",
		`"unclosed double`,
		"`unclosed",
		`r'raw\'`,
		`r'raw\\' AS s`,
		"|> LOG",
		"SELECT LOG(10), REPLACE(s, 'a', 'b')",
		"CREATE OR REPLACE TABLE t",
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(sqlPath, []byte(sqlSrc), 0o600))

	luaPath := filepath.Join(ws, "check_syntax.lua")
	luaScript := fmt.Sprintf(`
local ok, err = xpcall(function()
  vim.cmd("syntax on")
  vim.cmd.edit(vim.fn.fnameescape(%q))
  assert(vim.bo.filetype == "bqlang", "expected filetype=bqlang, got " .. tostring(vim.bo.filetype))
  assert(vim.b.current_syntax == "bqlang", "expected b:current_syntax=bqlang")

  local function syn(line, col)
    return vim.fn.synIDattr(vim.fn.synID(line, col, 1), "name")
  end

  local checks = {
    { 1, 1, "bqlangComment", "-- c col 1" },
    { 1, 4, "bqlangComment", "-- c col 4" },
    { 2, 1, "bqlangComment", "# c col 1" },
    { 2, 3, "bqlangComment", "# c col 3" },
    { 3, 1, "bqlangComment", "// c col 1" },
    { 3, 4, "bqlangComment", "// c col 4" },
    { 4, 1, "bqlangComment", "/* c */ col 1" },
    { 4, 5, "bqlangComment", "/* c */ col 5" },
    { 5, 1, "bqlangString", "'--' col 1" },
    { 5, 2, "bqlangString", "'--' col 2" },
    { 6, 1, "bqlangJinjaRaw", "raw tag start" },
    { 6, 10, "bqlangJinjaRaw", "raw inner SELECT" },
    { 6, 17, "bqlangJinjaRaw", "raw inner {{" },
    { 6, 40, "bqlangJinjaRaw", "raw end tag" },
    { 7, 2, "bqlangJinjaTag", "jinja tag %%" },
    { 7, 4, "bqlangJinjaTagKeyword", "jinja tag if" },
    { 7, 12, "bqlangJinjaTag", "jinja tag closing %%" },
    { 7, 13, "bqlangJinjaTag", "jinja tag closing }" },
    { 8, 2, "bqlangJinjaExpr", "jinja expr {" },
    { 8, 6, "bqlangJinjaOperator", "jinja expr +" },
    { 9, 1, "bqlangPipeOperator", "|>" },
    { 10, 8, "bqlangBuiltin", "CAST(" },
    { 10, 20, "bqlangType", "TIME" },
    { 10, 27, "bqlangBuiltin", "LEFT(" },
    { 10, 45, "bqlangKeyword", "LEFT JOIN" },
    { 11, 1, "bqlangKeyword", "GRANT" },
    { 11, 7, "bqlangKeyword", "IMMEDIATE" },
    { 11, 17, "bqlangKeyword", "OPTIONAL" },
    { 11, 26, "bqlangKeyword", "SIMPLE" },
    { 12, 1, "bqlangString", "triple quote start" },
    { 13, 1, "bqlangString", "triple quote line 2" },
    { 14, 10, "bqlangJinjaOperator", "jinja tag inner -" },
    { 14, 19, "bqlangJinjaTag", "jinja tag closing -" },
    { 14, 20, "bqlangJinjaTag", "jinja tag closing %%" },
    { 15, 7, "bqlangJinjaOperator", "jinja expr inner -" },
    { 15, 11, "bqlangJinjaExpr", "jinja expr closing -" },
    { 15, 12, "bqlangJinjaExpr", "jinja expr closing }" },
    { 16, 7, "bqlangJinjaOperator", "jinja expr %%" },
    { 17, 6, "bqlangJinjaExpr", "jinja standalone backslash (not bqlangJinjaOperator)" },
    { 18, 8, "", "sql standalone backslash" },
    { 19, 1, "bqlangString", "unclosed single quote start" },
    { 19, 5, "bqlangString", "unclosed single quote body" },
    { 20, 1, "bqlangString", "unclosed double quote start" },
    { 20, 12, "bqlangString", "unclosed double quote body" },
    { 21, 1, "bqlangBacktick", "unclosed backtick start" },
    { 21, 5, "bqlangBacktick", "unclosed backtick body" },
    { 22, 1, "bqlangString", "raw escaped quote at EOL start" },
    { 22, 5, "bqlangString", "raw escaped quote at EOL body" },
    { 22, 6, "bqlangEscape", "raw escaped quote at EOL escape" },
    { 23, 1, "bqlangString", "raw double backslash start" },
    { 23, 8, "bqlangString", "raw double backslash close quote" },
    { 23, 10, "bqlangKeyword", "raw double backslash trailing AS" },
    { 24, 1, "bqlangPipeOperator", "pipe |>" },
    { 24, 4, "bqlangKeyword", "pipe LOG keyword" },
    { 25, 8, "bqlangBuiltin", "LOG() builtin" },
    { 25, 17, "bqlangBuiltin", "REPLACE() builtin" },
    { 26, 11, "bqlangKeyword", "CREATE OR REPLACE keyword" },
  }

  for _, c in ipairs(checks) do
    local got = syn(c[1], c[2])
    assert(got == c[3], string.format("%%s (line %%d col %%d): expected %%q, got %%q", c[4], c[1], c[2], c[3], got))
  end
end, debug.traceback)
if not ok then
  io.stderr:write(tostring(err) .. "\n")
  vim.cmd("cquit 1")
end
vim.cmd("qa!")
`, sqlPath)
	require.NoError(t, os.WriteFile(luaPath, []byte(luaScript), 0o600))

	cmd := exec.CommandContext(t.Context(), nvimBin, "--headless", "-u", "NORC", "-i", "NONE", "-S", luaPath)
	out, runErr := cmd.CombinedOutput()
	require.NoError(t, runErr, "nvim --headless failed:\n%s", string(out))
}

func collectTMPatterns(v any, out *[]string) {
	switch node := v.(type) {
	case map[string]any:
		for _, key := range []string{"match", "begin", "end", "while"} {
			if s, ok := node[key].(string); ok {
				*out = append(*out, s)
			}
		}
		for _, child := range node {
			collectTMPatterns(child, out)
		}
	case []any:
		for _, child := range node {
			collectTMPatterns(child, out)
		}
	}
}

func TestInstallVSIXEditorCLIPathSuccessFailureAndNeovimPackDir(t *testing.T) {
	binDir := t.TempDir()
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)
	t.Setenv("PATH", binDir)

	argsLog := filepath.Join(binDir, "code_args.txt")
	codeBin := filepath.Join(binDir, "code")
	require.NoError(t, os.WriteFile(codeBin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+argsLog+"\ntest -f \"$2\"\n"), 0o755))

	kiroBin := filepath.Join(binDir, "kiro")
	require.NoError(t, os.WriteFile(kiroBin, []byte("#!/bin/sh\necho 'install denied' >&2\nexit 1\n"), 0o755))

	require.NoError(t, editors.InstallVSCode(t.Context(), editors.Options{Version: "1.2.3", BinPath: "/tmp/bqpls"}))
	logged, err := os.ReadFile(argsLog)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(logged)), "\n")
	require.Len(t, lines, 3)
	assert.Equal(t, "--install-extension", lines[0])
	assert.True(t, strings.HasSuffix(lines[1], ".vsix"), "expected .vsix temp path, got %q", lines[1])
	assert.Equal(t, "--force", lines[2])

	leftover, err := filepath.Glob(filepath.Join(tmpDir, "bqlang-*.vsix"))
	require.NoError(t, err)
	assert.Empty(t, leftover, "temp vsix file should be removed after InstallVSCode")

	err = editors.InstallKiro(t.Context(), editors.Options{Version: "1.2.3"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kiro --install-extension failed")
	assert.Contains(t, err.Error(), "install denied")

	leftoverAfterErr, err := filepath.Glob(filepath.Join(tmpDir, "bqlang-*.vsix"))
	require.NoError(t, err)
	assert.Empty(t, leftoverAfterErr, "temp vsix file should be removed after failed install")

	// CLI failure with empty stderr/stdout must still return descriptive error.
	require.NoError(t, os.WriteFile(kiroBin, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	err = editors.InstallKiro(t.Context(), editors.Options{Version: "1.2.3"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kiro --install-extension failed")

	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", xdg)
	packDir, err := editors.NeovimPackDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(xdg, "nvim", "site", "pack", "bqlang", "start", "bqpls.nvim"), packDir)

	t.Setenv("XDG_DATA_HOME", "")
	fallbackDir, err := editors.NeovimPackDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".local", "share", "nvim", "site", "pack", "bqlang", "start", "bqpls.nvim"), fallbackDir)
}

func TestVSCodeExtensionAdditiveProjectCacheTTLScopeMerge(t *testing.T) {
	t.Parallel()

	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node binary not available on PATH")
	}

	extDir := t.TempDir()
	require.NoError(t, editors.Export(editors.TargetVSCodeDir, extDir, editors.Options{BinPath: "/usr/local/bin/bqpls"}))

	script := `
const fs = require('fs');
const vm = require('vm');
const path = require('path');

const extSource = fs.readFileSync(process.argv[1], 'utf8');
const mockInspected = {
  cacheTtlSeconds: { globalValue: 86400 },
  projectCacheTtlSeconds: {
    globalValue: { 'BigQuery-Public-Data': 2592000, 'shared-proj': 172800 },
    workspaceValue: { 'corp-datalake': 604800, 'shared-proj': 86400 },
    workspaceFolderValue: { 'team-mart': 259200, 'shared-proj': 432000 }
  }
};

const vscodeMock = {
  SemanticTokensLegend: function() {},
  workspace: {
    getConfiguration: () => ({
      inspect: (k) => mockInspected[k],
      get: (k) => (k === 'cacheTtlSeconds' ? 86400 : undefined)
    })
  }
};

const sandbox = {
  require: (mod) => (mod === 'vscode' ? vscodeMock : require(mod)),
  module: { exports: {} },
  exports: {},
  Buffer,
  process,
  setTimeout,
  clearTimeout
};
vm.runInNewContext(extSource + '\nmodule.exports._readLspSettings = readLspSettings;', sandbox);
process.stdout.write(JSON.stringify(sandbox.module.exports._readLspSettings()));
`

	cmd := exec.CommandContext(t.Context(), nodeBin, "-e", script, filepath.Join(extDir, "extension.js"))
	out, runErr := cmd.CombinedOutput()
	require.NoError(t, runErr, "node execution failed: %s", string(out))

	var got struct {
		CacheTTLSeconds        int            `json:"cacheTtlSeconds"`
		ProjectCacheTTLSeconds map[string]int `json:"projectCacheTtlSeconds"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, 86400, got.CacheTTLSeconds)
	assert.Equal(t, map[string]int{
		"bigquery-public-data": 2592000,
		"corp-datalake":        604800,
		"team-mart":            259200,
		"shared-proj":          432000,
	}, got.ProjectCacheTTLSeconds)
}
