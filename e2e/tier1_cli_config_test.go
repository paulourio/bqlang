package e2e_test

import (
	"encoding/json"
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

// --- F1: bqfmt CLI & .bqfmt.toml Suffix/Style Discovery ---

func TestT1_F1_AllSixSuffixesDirectoryWalk(t *testing.T) {
	t.Parallel()

	bin := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	supported := []string{
		"a.bql",
		"b.sql",
		"c.bql.j2",
		"d.sql.j2",
		"e.bql.tpl",
		"f.sql.tpl",
	}
	rawSQL := "select   1   as   x\n"

	for _, name := range supported {
		writeFile(t, filepath.Join(ws, name), rawSQL)
	}

	skipPath := filepath.Join(ws, "skip.txt")
	writeFile(t, skipPath, rawSQL)

	res := runCLI(t, bin, ws, env.env, "", "-l", "-w", ws)
	require.Equal(t, 0, res.exitCode, res.stderr)

	for _, name := range supported {
		full := filepath.Join(ws, name)
		assert.Contains(t, res.stdout, name, "expected %s to be listed", name)

		got, err := os.ReadFile(full)
		require.NoError(t, err)
		assert.Equal(t, "SELECT 1 AS x\n", string(got))
	}

	assert.NotContains(t, res.stdout, "skip.txt")

	skipGot, err := os.ReadFile(skipPath)
	require.NoError(t, err)
	assert.Equal(t, rawSQL, string(skipGot))
}

func TestT1_F1_UpwardDiscovery(t *testing.T) {
	t.Parallel()

	bin := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), "keyword_style = \"LOWER_CASE\"\n")

	queryPath := filepath.Join(ws, "sub", "deep", "q.sql")
	writeFile(t, queryPath, "SELECT 1 AS a FROM t\n")

	res := runCLI(t, bin, filepath.Join(ws, "sub", "deep"), env.env, "", queryPath)
	require.Equal(t, 0, res.exitCode, res.stderr)
	assert.Contains(t, res.stdout, "select 1 as a")
	assert.Contains(t, res.stdout, "from t")
}

func TestT1_F1_MultiStyleRegexAndInclude(t *testing.T) {
	t.Parallel()

	bin := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	cfg := `
default_style = "default"

[[styles]]
name = "models"
regex = "^models/.*"
[styles.options]
indentation = 4
keyword_style = "UPPER_CASE"

[[styles]]
name = "templates"
include = "*.sql.tpl"
[styles.options]
keyword_style = "LOWER_CASE"

[[styles]]
name = "default"
[styles.options]
keyword_style = "UPPER_CASE"
`
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), cfg)

	modelSQL := filepath.Join(ws, "models", "mart.sql")
	tplSQL := filepath.Join(ws, "templates", "mart.sql.tpl")
	writeFile(t, modelSQL, "begin select 1; end;\n")
	writeFile(t, tplSQL, "SELECT a, b FROM t WHERE a = 1\n")

	resModel := runCLI(t, bin, ws, env.env, "", modelSQL)
	require.Equal(t, 0, resModel.exitCode, resModel.stderr)
	assert.Contains(t, resModel.stdout, "\n    SELECT 1;")

	resTpl := runCLI(t, bin, ws, env.env, "", tplSQL)
	require.Equal(t, 0, resTpl.exitCode, resTpl.stderr)
	assert.Contains(t, resTpl.stdout, "select a, b")
	assert.Contains(t, resTpl.stdout, "from t")
}

func TestT1_F1_StdinWithStdinFilename(t *testing.T) {
	t.Parallel()

	bin := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	cfg := `
default_style = "default"

[[styles]]
name = "models"
regex = "^models/.*"
[styles.options]
keyword_style = "LOWER_CASE"

[[styles]]
name = "default"
[styles.options]
keyword_style = "UPPER_CASE"
`
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), cfg)

	res := runCLI(t, bin, ws, env.env, "SELECT 1 AS a\n", "-stdin-filename", filepath.Join(ws, "models", "q.sql"))
	require.Equal(t, 0, res.exitCode, res.stderr)
	assert.Equal(t, "select 1 as a\n", res.stdout)
}

func TestT1_F1_CheckDiffAndVersionFlags(t *testing.T) {
	t.Parallel()

	bin := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	verRes := runCLI(t, bin, ws, env.env, "", "-version")
	require.Equal(t, 0, verRes.exitCode, verRes.stderr)
	assert.Contains(t, verRes.stdout, "bqfmt")

	sqlPath := filepath.Join(ws, "q.sql")
	writeFile(t, sqlPath, "select   1   as   a\n")

	checkFail := runCLI(t, bin, ws, env.env, "", "-check", sqlPath)
	assert.Equal(t, 1, checkFail.exitCode)

	diffRes := runCLI(t, bin, ws, env.env, "", "-d", sqlPath)
	require.Equal(t, 0, diffRes.exitCode, diffRes.stderr)
	assert.Contains(t, diffRes.stdout, "--- ")
	assert.Contains(t, diffRes.stdout, "+++ ")
	assert.Contains(t, diffRes.stdout, "+SELECT 1 AS a")

	writeFile(t, sqlPath, "SELECT 1 AS a\n")
	checkPass := runCLI(t, bin, ws, env.env, "", "-check", sqlPath)
	assert.Equal(t, 0, checkPass.exitCode)
}

// --- F2: .bqfmt.toml [lsp] Options & Validation ---

func TestT1_F2_LSPCoexistsWithAllFourLayouts(t *testing.T) {
	t.Parallel()

	bin := requireBqfmt(t)
	env := newIsolatedEnv(t)

	layouts := []struct {
		name string
		toml string
	}{
		{
			name: "flat",
			toml: "keyword_style = \"LOWER_CASE\"\n\n[lsp]\ndefault_project = \"p\"\n",
		},
		{
			name: "options",
			toml: "[options]\nkeyword_style = \"LOWER_CASE\"\n\n[lsp]\ndefault_project = \"p\"\n",
		},
		{
			name: "print_options",
			toml: "[print_options]\nkeyword_style = \"LOWER_CASE\"\n\n[lsp]\ndefault_project = \"p\"\n",
		},
		{
			name: "styles",
			toml: "[[styles]]\nname = \"s\"\n[styles.options]\nkeyword_style = \"LOWER_CASE\"\n\n[lsp]\ndefault_project = \"p\"\n",
		},
	}

	for _, tc := range layouts {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ws := t.TempDir()
			cfgPath := filepath.Join(ws, ".bqfmt.toml")
			writeFile(t, cfgPath, tc.toml)

			res := runCLI(t, bin, ws, env.env, "SELECT 1 AS a\n", "-config", cfgPath)
			require.Equal(t, 0, res.exitCode, res.stderr)
			assert.Equal(t, "select 1 as a\n", res.stdout)
		})
	}
}

func TestT1_F2_LSPDefaultValuesWhenOmitted(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), "keyword_style = \"UPPER_CASE\"\n")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, filepath.Join(ws, "q.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", "SELECT FROM WHERE", 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.NotEmpty(t, ev.Diagnostics, "diagnostics_enabled should default to true when [lsp] is omitted")
}

func TestT1_F2_LSPDefaultProjectAndDataset(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	for i, query := range []string{
		"SELECT tenant_id FROM events WHERE event_date = '2026-01-01' AND tenant_id = 't1'",
		"SELECT tenant_id FROM analytics.events WHERE event_date = '2026-01-01' AND tenant_id = 't1'",
	} {
		uri := pathToURI(t, filepath.Join(ws, fmt.Sprintf("q%d.sql", i)))
		cur := client.diagCursor()
		client.didOpen(t, uri, "bqlang", query, 1)

		_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
		assert.Empty(t, ev.Diagnostics, "expected clean resolution for %q", query)
		client.didClose(t, uri)
	}
}

func TestT1_F2_LSPSchemaDirsRelativeResolution(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")
	otherCWD := t.TempDir()

	client := startLSP(t, otherCWD, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, filepath.Join(ws, "q.sql"))
	sql := "SELECT tenant_id FROM events WHERE event_date = '2026-01-01'"
	client.didOpen(t, uri, "bqlang", sql, 1)

	h, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "events"))
	require.Nil(t, rpcErr)
	require.NotNil(t, h)
	assert.Contains(t, h.Contents.Value, "Raw clickstream events table")
}

func TestT1_F2_LSPConfigDiscoveryInBqpls(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	parentWS := t.TempDir()
	subWS := filepath.Join(parentWS, "project_sub")
	schemaDir := filepath.Join(subWS, "schemas")
	subEvents := standardEventsSchema()
	subEvents.ID = "sub-proj:sub_ds.events"
	subEvents.TableReference.ProjectID = "sub-proj"
	subEvents.TableReference.DatasetID = "sub_ds"
	writeSchemaTableJSON(t, filepath.Join(schemaDir, "sub-proj.sub_ds.events.json"), subEvents)

	cfg := `
[lsp]
default_project = "sub-proj"
default_dataset = "sub_ds"
schema_dirs = ["./schemas"]
bigquery_api_enabled = false
`
	writeFile(t, filepath.Join(subWS, ".bqfmt.toml"), cfg)

	client := startLSP(t, parentWS, env.env)
	client.initialize(t, parentWS)

	uri := pathToURI(t, filepath.Join(subWS, "models", "q.sql"))
	sql := "SELECT tenant_id FROM events WHERE event_date = '2026-01-01' AND tenant_id = 't1'"
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", sql, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev.Diagnostics)
}

// --- F3: bqpls CLI Subcommands & Errors ---

func TestT1_F3_VersionSubcommand(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	res := runCLI(t, bin, ws, env.env, "", "version")
	require.Equal(t, 0, res.exitCode, res.stderr)
	assert.NotEmpty(t, strings.TrimSpace(res.stdout))
	assert.Contains(t, strings.ToLower(res.stdout), "bqpls")
}

func TestT1_F3_ExportVsix(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()
	out := filepath.Join(ws, "bqlang.vsix")

	res := runCLI(t, bin, ws, env.env, "", "export", "-target", "vsix", "-o", out)
	require.Equal(t, 0, res.exitCode, res.stderr)

	info, err := os.Stat(out)
	require.NoError(t, err)
	assert.Positive(t, info.Size())
}

func TestT1_F3_ExportVSCodeDirAndNvim(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	vscDir := filepath.Join(ws, "vsc")
	resVsc := runCLI(t, bin, ws, env.env, "", "export", "-target", "vscode-dir", "-o", vscDir)
	require.Equal(t, 0, resVsc.exitCode, resVsc.stderr)

	for _, rel := range []string{
		"package.json",
		"language-configuration.json",
		filepath.Join("syntaxes", "bqlang.tmLanguage.json"),
		"extension.js",
	} {
		_, err := os.Stat(filepath.Join(vscDir, rel))
		require.NoError(t, err, "missing vscode-dir asset %s", rel)
	}

	nvimDir := filepath.Join(ws, "nvim")
	resNvim := runCLI(t, bin, ws, env.env, "", "export", "-target", "nvim", "-o", nvimDir)
	require.Equal(t, 0, resNvim.exitCode, resNvim.stderr)

	for _, rel := range []string{
		filepath.Join("ftdetect", "bqlang.lua"),
		filepath.Join("syntax", "bqlang.vim"),
		filepath.Join("ftplugin", "bqlang.lua"),
		filepath.Join("plugin", "bqpls.lua"),
		filepath.Join("lua", "bqpls", "init.lua"),
	} {
		_, err := os.Stat(filepath.Join(nvimDir, rel))
		require.NoError(t, err, "missing nvim asset %s", rel)
	}
}

func TestT1_F3_InstallNeovim(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	res := runCLI(t, bin, ws, env.env, "", "install", "nvim", "-bin-path", bin)
	require.Equal(t, 0, res.exitCode, res.stderr)

	packRoot := filepath.Join(env.xdgData, "nvim", "site", "pack", "bqlang", "start", "bqpls.nvim")
	for _, rel := range []string{
		filepath.Join("ftdetect", "bqlang.lua"),
		filepath.Join("syntax", "bqlang.vim"),
		filepath.Join("ftplugin", "bqlang.lua"),
		filepath.Join("plugin", "bqpls.lua"),
		filepath.Join("lua", "bqpls", "init.lua"),
	} {
		_, err := os.Stat(filepath.Join(packRoot, rel))
		require.NoError(t, err, "missing installed nvim pack file %s", rel)
	}
}

func TestT1_F3_InvalidSubcommandsAndFlags(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	cases := [][]string{
		{"unknown-subcommand"},
		{"export", "-target", "bogus", "-o", filepath.Join(ws, "x")},
		{"export", "-target", "vsix"},
		{"install", "bogus"},
		{"serve", "-unknown-flag"},
	}

	for _, args := range cases {
		res := runCLI(t, bin, ws, env.env, "", args...)
		assert.NotEqual(t, 0, res.exitCode, "expected non-zero exit for args %v", args)
		assert.NotEmpty(t, strings.TrimSpace(res.stderr), "expected stderr message for args %v", args)
	}
}

// --- F12: Embedded Editor Bundles (.vsix & Neovim) ---

func TestT1_F12_VsixArchiveStructureAndManifests(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()
	vsixPath := filepath.Join(ws, "bqlang.vsix")

	res := runCLI(t, bin, ws, env.env, "", "export", "-target", "vsix", "-o", vsixPath)
	require.Equal(t, 0, res.exitCode, res.stderr)

	rep := inspectVSIX(t, vsixPath)
	assert.Equal(t, "bqlang", rep.packageJSON["name"])
	assert.Equal(t, "paulourio", rep.packageJSON["publisher"])

	pkgRaw, err := json.Marshal(rep.packageJSON)
	require.NoError(t, err)
	pkgStr := string(pkgRaw)

	for _, ext := range []string{".bql", ".sql", ".bql.j2", ".sql.j2", ".bql.tpl", ".sql.tpl"} {
		assert.Contains(t, pkgStr, ext)
	}

	assert.Contains(t, pkgStr, "semanticTokenModifiers")
	assert.Contains(t, pkgStr, "partitioned")
	assert.Contains(t, pkgStr, "semanticTokenScopes")
}

func TestT1_F12_VsixDeterministicBytes(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	p1 := filepath.Join(ws, "one.vsix")
	p2 := filepath.Join(ws, "two.vsix")

	res1 := runCLI(t, bin, ws, env.env, "", "export", "-target", "vsix", "-o", p1)
	require.Equal(t, 0, res1.exitCode, res1.stderr)

	res2 := runCLI(t, bin, ws, env.env, "", "export", "-target", "vsix", "-o", p2)
	require.Equal(t, 0, res2.exitCode, res2.stderr)

	assert.Equal(t, sha256HexFile(t, p1), sha256HexFile(t, p2))
}

func TestT1_F12_TextMateGrammarRE2AndSampleMatching(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()
	vsixPath := filepath.Join(ws, "bqlang.vsix")

	res := runCLI(t, bin, ws, env.env, "", "export", "-target", "vsix", "-o", vsixPath)
	require.Equal(t, 0, res.exitCode, res.stderr)

	rep := inspectVSIX(t, vsixPath)

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

	compiled := make([]*regexp.Regexp, 0, len(rep.patterns))
	for _, pat := range rep.patterns {
		re, err := regexp.Compile(pat)
		require.NoError(t, err)
		compiled = append(compiled, re)
	}

	for _, sample := range samples {
		matched := false
		for _, re := range compiled {
			loc := re.FindStringIndex(sample.text)
			if loc != nil && loc[0] == 0 && loc[1] >= sample.minPrefix {
				matched = true

				break
			}
		}

		assert.True(t, matched, "expected at least one TextMate RE2 pattern to match prefix of %q", sample.text)
	}
}

func TestT1_F12_NeovimBundleStructure(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	res := runCLI(t, bin, ws, env.env, "", "install", "nvim", "-bin-path", bin)
	require.Equal(t, 0, res.exitCode, res.stderr)

	packRoot := filepath.Join(env.xdgData, "nvim", "site", "pack", "bqlang", "start", "bqpls.nvim")
	initLua, err := os.ReadFile(filepath.Join(packRoot, "lua", "bqpls", "init.lua"))
	require.NoError(t, err)
	assert.Contains(t, string(initLua), "vim.lsp.start")
	assert.Contains(t, string(initLua), "@lsp.mod.partitioned.bqlang")
	assert.Contains(t, string(initLua), "@lsp.mod.clustered.bqlang")
}

func TestT1_F12_CodeCLIInstallExtension(t *testing.T) {
	t.Parallel()

	codeBin, err := exec.LookPath("code")
	if err != nil {
		t.Skip("code CLI not installed in PATH")
	}

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()
	vsixPath := filepath.Join(ws, "bqlang.vsix")

	expRes := runCLI(t, bin, ws, env.env, "", "export", "-target", "vsix", "-o", vsixPath)
	require.Equal(t, 0, expRes.exitCode, expRes.stderr)

	extDir := filepath.Join(ws, "code-ext")
	userDir := filepath.Join(ws, "code-user")
	require.NoError(t, os.MkdirAll(extDir, 0o755))
	require.NoError(t, os.MkdirAll(userDir, 0o755))

	installRes := runCLI(t, codeBin, ws, env.env, "", "--install-extension", vsixPath, "--extensions-dir", extDir, "--user-data-dir", userDir, "--force")
	require.Equal(t, 0, installRes.exitCode, installRes.stderr)

	listRes := runCLI(t, codeBin, ws, env.env, "", "--list-extensions", "--show-versions", "--extensions-dir", extDir, "--user-data-dir", userDir)
	require.Equal(t, 0, listRes.exitCode, listRes.stderr)
	assert.Contains(t, listRes.stdout, "paulourio.bqlang")
}
