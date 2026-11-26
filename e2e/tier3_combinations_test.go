package e2e_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestT3_F2_F6_LSPConfigToSchemaDirsAndDefaultQual(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT u.country, e.tenant_id FROM users u JOIN events e ON u.user_id = e.user_ctx.user_id AND u.country = e.tenant_id WHERE e.event_date = '2026-01-01'"
	uri := pathToURI(t, filepath.Join(ws, "qual.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", sql, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev.Diagnostics)
}

func TestT3_F2_F7_RequirePartitionSeverityAndToggle(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		sev      string
		wantCode int
		wantNone bool
	}{
		{sev: "warning", wantCode: 2},
		{sev: "error", wantCode: 1},
		{sev: "off", wantNone: true},
	} {
		t.Run(tc.sev, func(t *testing.T) {
			t.Parallel()

			env := newIsolatedEnv(t)
			ws, _ := setupStandardWorkspace(t, "require_partition_filter_severity = \""+tc.sev+"\"\nclustering_filter_severity = \"off\"\n")

			client := startLSP(t, ws, env.env)
			client.initialize(t, ws)

			uri := pathToURI(t, filepath.Join(ws, "sev_pair.sql"))
			cur := client.diagCursor()
			client.didOpen(t, uri, "bqlang", "SELECT tenant_id FROM events", 1)

			_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
			if tc.wantNone {
				assert.Empty(t, ev.Diagnostics)
			} else {
				require.NotEmpty(t, ev.Diagnostics)
				assert.Equal(t, tc.wantCode, ev.Diagnostics[0].Severity)
			}
		})
	}
}

func TestT3_F4_F5_IncrementalDidChangePlusDeltaSplicing(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, filepath.Join(ws, "delta_chain.sql"))
	sql1 := "SELECT event_date, tenant_id FROM events"
	client.didOpen(t, uri, "bqlang", sql1, 1)

	full1 := client.semanticTokensFull(t, uri)
	require.NotEmpty(t, full1.ResultID)

	// Step 2: add WHERE clause.
	client.didChange(t, uri, 2, lspContentChange{
		Range: &lspRange{
			Start: lspPosition{Line: 0, Character: len(sql1)},
			End:   lspPosition{Line: 0, Character: len(sql1)},
		},
		Text: " WHERE event_date = '2026-01-01'",
	})

	delta2, _ := client.semanticTokensFullDelta(t, uri, full1.ResultID)
	full2 := client.semanticTokensFull(t, uri)
	require.NotEmpty(t, delta2.Edits, "expected delta edits for valid previousResultId in step 2")
	assert.Empty(t, delta2.Data)
	assert.Equal(t, full2.Data, applySemanticTokenEdits(t, full1.Data, delta2.Edits))

	// Step 3: insert UTF-16 surrogate emoji.
	client.didChange(t, uri, 3, lspContentChange{
		Range: &lspRange{
			Start: lspPosition{Line: 0, Character: len("SELECT ")},
			End:   lspPosition{Line: 0, Character: len("SELECT ")},
		},
		Text: "'😀' AS icon, ",
	})

	delta3, _ := client.semanticTokensFullDelta(t, uri, full2.ResultID)
	full3 := client.semanticTokensFull(t, uri)
	require.NotEmpty(t, delta3.Edits, "expected delta edits for valid previousResultId in step 3")
	assert.Empty(t, delta3.Data)
	assert.Equal(t, full3.Data, applySemanticTokenEdits(t, full2.Data, delta3.Edits))

	// Step 4: introduce transient syntax error and verify LKG retention.
	sql3 := "SELECT '😀' AS icon, event_date, tenant_id FROM events WHERE event_date = '2026-01-01'"
	client.didChange(t, uri, 4, lspContentChange{
		Range: &lspRange{
			Start: lspPosition{Line: 0, Character: utf16Len("SELECT '😀' AS icon, event_date, tenant_id FROM events")},
			End:   lspPosition{Line: 0, Character: utf16Len(sql3)},
		},
		Text: " WHERE (((",
	})

	full4 := client.semanticTokensFull(t, uri)
	decoded4, err := decodeSemanticTokens(full4.Data)
	require.NoError(t, err)
	evTok, ok := findTokenAt(decoded4, utf16PosAt(t, sql3, "event_date"))
	require.True(t, ok, "LKG should retain partitioned modifier on event_date")
	assert.NotZero(t, evTok.tokenModifiers&modPartitioned)
}

func TestT3_F4_F7_IncrementalFixClearsDiagnostics(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, filepath.Join(ws, "fix_diag.sql"))
	sql1 := "SELECT tenant_id FROM events"
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", sql1, 1)

	cur, ev1 := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	require.NotEmpty(t, ev1.Diagnostics)

	client.didChange(t, uri, 2, lspContentChange{
		Range: &lspRange{
			Start: lspPosition{Line: 0, Character: len(sql1)},
			End:   lspPosition{Line: 0, Character: len(sql1)},
		},
		Text: " WHERE event_date = '2026-01-01' AND tenant_id = 't1'",
	})
	_, ev2 := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev2.Diagnostics)
}

func TestT3_F5_F6_WildcardAndStructModifiers(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	shard := standardEventsSchema()
	shard.RequirePartitionFilter = false
	shard.TimePartitioning.RequirePartitionFilter = false
	shard.ID = "my-proj:analytics.events_20260101"
	shard.TableReference.TableID = "events_20260101"
	writeSchemaTableJSON(t, filepath.Join(ws, "schemas", "my-proj.analytics.events_20260101.json"), shard)

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT _TABLE_SUFFIX, u.event_date, u.tenant_id, u.user_ctx.user_id FROM `my-proj.analytics.events_*` u"
	uri := pathToURI(t, filepath.Join(ws, "wild_struct.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	tok := client.semanticTokensFull(t, uri)
	decoded, err := decodeSemanticTokens(tok.Data)
	require.NoError(t, err)

	sufTok, ok := findTokenAt(decoded, utf16PosAt(t, sql, "_TABLE_SUFFIX"))
	require.True(t, ok)
	assert.NotZero(t, sufTok.tokenModifiers&modPseudo)

	evTok, ok := findTokenAt(decoded, utf16PosAt(t, sql, "event_date"))
	require.True(t, ok)
	assert.NotZero(t, evTok.tokenModifiers&modPartitioned)

	tenTok, ok := findTokenAt(decoded, utf16PosAt(t, sql, "tenant_id"))
	require.True(t, ok)
	assert.NotZero(t, tenTok.tokenModifiers&modClustered)
}

func TestT3_F6_F8_SchemaDirsMetadataInHover(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT u.event_date, u.tenant_id, u.user_ctx.tags FROM events u WHERE u.event_date = '2026-01-01'"
	uri := pathToURI(t, filepath.Join(ws, "schema_hover.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	hTbl, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "events"))
	require.Nil(t, rpcErr)
	require.NotNil(t, hTbl)
	assert.Contains(t, hTbl.Contents.Value, "Raw clickstream events table")

	hTags, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "tags"))
	require.Nil(t, rpcErr)
	require.NotNil(t, hTags)
	assert.Contains(t, hTags.Contents.Value, "Attached key-value tags")
}

func TestT3_F6_F9_SchemaDirsMetadataInCompletion(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql1 := "SELECT u. FROM events u"
	uri1 := pathToURI(t, filepath.Join(ws, "schema_comp1.sql"))
	client.didOpen(t, uri1, "bqlang", sql1, 1)
	items1 := client.completion(t, uri1, utf16PosAfter(t, sql1, "u."))
	assert.Contains(t, completionLabels(items1), "user_ctx")

	sql2 := "SELECT u.user_ctx. FROM events u"
	uri2 := pathToURI(t, filepath.Join(ws, "schema_comp2.sql"))
	client.didOpen(t, uri2, "bqlang", sql2, 1)
	items2 := client.completion(t, uri2, utf16PosAfter(t, sql2, "u.user_ctx."))
	assert.Contains(t, completionLabels(items2), "user_id")
}

func TestT3_F7_F11_FormatFixesLayoutPreservingDiagnostics(t *testing.T) {
	t.Parallel()

	bqfmt := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws, cfgPath := setupStandardWorkspace(t, "")

	rawSQL := "select   tenant_id   from   events\n"
	sqlPath := filepath.Join(ws, "fmt_diag.sql")
	writeFile(t, sqlPath, rawSQL)

	cliRes := runCLI(t, bqfmt, ws, env.env, "", "-config", cfgPath, sqlPath)
	require.Equal(t, 0, cliRes.exitCode, cliRes.stderr)

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, sqlPath)
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", rawSQL, 1)
	cur, ev1 := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	require.NotEmpty(t, ev1.Diagnostics)

	edits, rpcErr := client.formatting(t, uri, 2, true)
	require.Nil(t, rpcErr)
	formatted := applyTextEdits(rawSQL, edits)
	assert.Equal(t, cliRes.stdout, formatted)

	client.didChange(t, uri, 2, lspContentChange{Text: formatted})
	_, ev2 := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	require.Len(t, ev2.Diagnostics, 2)
	assert.Contains(t, strings.ToLower(ev2.Diagnostics[1].Message), "partition")
}

func TestT3_F4_F9_UTF16SurrogateBeforeCompletionDot(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT '😀', u. FROM events u"
	uri := pathToURI(t, filepath.Join(ws, "utf16_comp.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	labels := completionLabels(client.completion(t, uri, utf16PosAfter(t, sql, "u.")))
	assert.Contains(t, labels, "event_date")
	assert.Contains(t, labels, "tenant_id")
}

func TestT3_F1_F11_BqfmtCLIAndLSPFormattingParity(t *testing.T) {
	t.Parallel()

	bqfmt := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	cfg := `
default_style = "default"

[[styles]]
name = "jinja"
include = "*.sql.j2"
[styles.options]
keyword_style = "LOWER_CASE"
indentation = 4

[[styles]]
name = "default"
[styles.options]
keyword_style = "UPPER_CASE"
`
	cfgPath := filepath.Join(ws, ".bqfmt.toml")
	writeFile(t, cfgPath, cfg)

	rawSQL := "SELECT a, b FROM {{ ref('orders') }} WHERE a = 1 AND b = 2\n"
	sqlPath := filepath.Join(ws, "parity.sql.j2")
	writeFile(t, sqlPath, rawSQL)

	cliRes := runCLI(t, bqfmt, ws, env.env, "", sqlPath)
	require.Equal(t, 0, cliRes.exitCode, cliRes.stderr)

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, sqlPath)
	client.didOpen(t, uri, "bqlang", rawSQL, 1)

	edits, rpcErr := client.formatting(t, uri, 2, true)
	require.Nil(t, rpcErr)
	assert.Equal(t, cliRes.stdout, applyTextEdits(rawSQL, edits))
}

func TestT3_F5_F10_JinjaMacroHighlightAndGoToDef(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := strings.Join([]string{
		"{% macro active_filter() %}event_date >= '2026-01-01'{% endmacro %}",
		"{% set lookback = 7 %}",
		"SELECT tenant_id FROM events WHERE {{ active_filter() }} AND {{ lookback }} > 0",
	}, "\n")
	uri := pathToURI(t, filepath.Join(ws, "jinja_combo.sql.j2"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	tok := client.semanticTokensFull(t, uri)
	decoded, err := decodeSemanticTokens(tok.Data)
	require.NoError(t, err)

	macTok, ok := findTokenAt(decoded, utf16PosAtNth(t, sql, "active_filter", 1))
	require.True(t, ok)
	assert.Equal(t, tokMacro, macTok.tokenType)

	locs := client.definition(t, uri, utf16PosAtNth(t, sql, "active_filter", 1))
	require.NotEmpty(t, locs)
	assert.Equal(t, 0, locs[0].Range.Start.Line)

	syms, _ := client.documentSymbols(t, uri)
	assert.Contains(t, strings.Join(flattenSymbolNames(syms), ","), "active_filter")
}

func TestT3_F9_F10_CTEDefinitionAndCompletionTogether(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "WITH daily AS (\n  SELECT event_date, COUNT(*) AS n FROM events WHERE event_date = '2026-01-01' GROUP BY 1\n)\nSELECT d. FROM daily d"
	uri := pathToURI(t, filepath.Join(ws, "cte_combo.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	labels := completionLabels(client.completion(t, uri, utf16PosAfter(t, sql, "d.")))
	assert.Contains(t, labels, "event_date")
	assert.Contains(t, labels, "n")

	aliasLocs := client.definition(t, uri, utf16PosAt(t, sql, "d."))
	require.NotEmpty(t, aliasLocs)
	assert.Equal(t, 3, aliasLocs[0].Range.Start.Line)

	cteLocs := client.definition(t, uri, utf16PosAtNth(t, sql, "daily", 1))
	require.NotEmpty(t, cteLocs)
	assert.Equal(t, 0, cteLocs[0].Range.Start.Line)
}

func TestT3_F3_F12_ExportVsixAndInspectAgainstBqplsLegend(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()
	vsixPath := filepath.Join(ws, "bqlang.vsix")

	expRes := runCLI(t, bin, ws, env.env, "", "export", "-target", "vsix", "-o", vsixPath)
	require.Equal(t, 0, expRes.exitCode, expRes.stderr)

	rep := inspectVSIX(t, vsixPath)
	pkgBytes, err := json.Marshal(rep.packageJSON)
	require.NoError(t, err)
	pkgStr := string(pkgBytes)

	client := startLSP(t, ws, env.env)
	initRes := client.initialize(t, ws)
	caps, ok := initRes["capabilities"].(map[string]any)
	require.True(t, ok, "missing capabilities object")
	semProv, ok := caps["semanticTokensProvider"].(map[string]any)
	require.True(t, ok, "missing semanticTokensProvider")
	legend, ok := semProv["legend"].(map[string]any)
	require.True(t, ok, "missing semanticTokensProvider.legend")

	rawLegend, err := json.Marshal(legend)
	require.NoError(t, err)

	var parsed struct {
		TokenModifiers []string `json:"tokenModifiers"`
	}
	require.NoError(t, json.Unmarshal(rawLegend, &parsed))

	for _, mod := range []string{"partitioned", "clustered", "pseudo"} {
		assert.Contains(t, parsed.TokenModifiers, mod)
		assert.Contains(t, pkgStr, mod)
	}
}

func TestT3_F3_F12_InstallNvimAndLaunchBqplsServe(t *testing.T) {
	t.Parallel()

	bin := requireBqpls(t)
	env := newIsolatedEnv(t)
	ws := t.TempDir()

	res := runCLI(t, bin, ws, env.env, "", "install", "nvim", "-bin-path", bin)
	require.Equal(t, 0, res.exitCode, res.stderr)

	initLuaPath := filepath.Join(env.xdgData, "nvim", "site", "pack", "bqlang", "start", "bqpls.nvim", "lua", "bqpls", "init.lua")
	initLua, err := os.ReadFile(initLuaPath)
	require.NoError(t, err)
	assert.Contains(t, string(initLua), bin)

	client := startLSP(t, ws, env.env)
	initRes := client.initialize(t, ws)
	assert.NotNil(t, initRes["capabilities"])
	assert.Equal(t, 0, client.shutdownAndExit(t))
}
