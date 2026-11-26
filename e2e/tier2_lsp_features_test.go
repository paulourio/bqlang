package e2e_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- F8 Boundaries ---

func TestT2_F8_HoverDeeplyNestedRepeatedStruct(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT u.user_ctx.tags FROM events u WHERE u.event_date = '2026-01-01'"
	uri := pathToURI(t, filepath.Join(ws, "hover_rep.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	h, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "tags"))
	require.Nil(t, rpcErr)
	require.NotNil(t, h)
	assert.Contains(t, strings.ToUpper(h.Contents.Value), "ARRAY")
	assert.Contains(t, strings.ToUpper(h.Contents.Value), "STRUCT")
}

func TestT2_F8_HoverTableWithNoDescriptionOrPartitioning(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws := t.TempDir()
	writeBqShowSchemaArrayJSON(t, filepath.Join(ws, "schemas", "my-proj.analytics.events_min.json"), []schemaFieldSpec{
		{Name: "id", Type: "INT64", Mode: "REQUIRED"},
	})
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), "[lsp]\ndefault_project = \"my-proj\"\ndefault_dataset = \"analytics\"\nschema_dirs = [\"./schemas\"]\nbigquery_api_enabled = false\n")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT id FROM events_min"
	uri := pathToURI(t, filepath.Join(ws, "hover_min.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	h, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "events_min"))
	require.Nil(t, rpcErr)
	require.NotNil(t, h)
	assert.Contains(t, h.Contents.Value, "events_min")
	assert.NotContains(t, h.Contents.Value, "<nil>")
}

func TestT2_F8_HoverIngestionTimePseudoColumns(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws := t.TempDir()
	ing := standardEventsSchema()
	ing.ID = "my-proj:analytics.ing_hover"
	ing.TableReference.TableID = "ing_hover"
	ing.TimePartitioning = &schemaTimePartitioning{Type: "DAY"}
	writeSchemaTableJSON(t, filepath.Join(ws, "schemas", "my-proj.analytics.ing_hover.json"), ing)
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), "[lsp]\ndefault_project = \"my-proj\"\ndefault_dataset = \"analytics\"\nschema_dirs = [\"./schemas\"]\nbigquery_api_enabled = false\n")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT _PARTITIONTIME, _PARTITIONDATE FROM ing_hover"
	uri := pathToURI(t, filepath.Join(ws, "hover_pseudo.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	hPT, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "_PARTITIONTIME"))
	require.Nil(t, rpcErr)
	require.NotNil(t, hPT)
	assert.Contains(t, strings.ToUpper(hPT.Contents.Value), "TIMESTAMP")

	hPD, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "_PARTITIONDATE"))
	require.Nil(t, rpcErr)
	require.NotNil(t, hPD)
	assert.Contains(t, strings.ToUpper(hPD.Contents.Value), "DATE")
}

func TestT2_F8_HoverOnJinjaTagAndCommentReturnsNull(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "{# j comment #}\n-- sql comment\nSELECT 1 FROM {{ ref('x') }}"
	uri := pathToURI(t, filepath.Join(ws, "hover_cmt.sql.j2"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	for _, needle := range []string{"j comment", "sql comment"} {
		h, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, needle))
		require.Nil(t, rpcErr)
		assert.Nil(t, h)
	}

	_, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "ref"))
	require.Nil(t, rpcErr)
}

func TestT2_F8_HoverOnWildcardTableAndSuffix(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws := t.TempDir()
	shard := standardEventsSchema()
	shard.RequirePartitionFilter = false
	shard.TimePartitioning.RequirePartitionFilter = false
	shard.ID = "my-proj:analytics.events_20260101"
	shard.TableReference.TableID = "events_20260101"
	writeSchemaTableJSON(t, filepath.Join(ws, "schemas", "my-proj.analytics.events_20260101.json"), shard)
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), "[lsp]\ndefault_project = \"my-proj\"\ndefault_dataset = \"analytics\"\nschema_dirs = [\"./schemas\"]\nbigquery_api_enabled = false\n")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT _TABLE_SUFFIX FROM `my-proj.analytics.events_*`"
	uri := pathToURI(t, filepath.Join(ws, "hover_wild.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	hSuf, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "_TABLE_SUFFIX"))
	require.Nil(t, rpcErr)
	require.NotNil(t, hSuf)
	assert.Contains(t, strings.ToUpper(hSuf.Contents.Value), "STRING")

	hTbl, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "events_*"))
	require.Nil(t, rpcErr)
	require.NotNil(t, hTbl)
	assert.Contains(t, hTbl.Contents.Value, "events_")
}

// --- F9 Boundaries ---

func TestT2_F9_CompletionUnclosedSubqueryParens(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT u. FROM (SELECT event_date, tenant_id FROM events u"
	uri := pathToURI(t, filepath.Join(ws, "comp_unclosed_paren.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	labels := completionLabels(client.completion(t, uri, utf16PosAfter(t, sql, "u.")))
	assert.Contains(t, labels, "event_date")
	assert.Contains(t, labels, "tenant_id")
}

func TestT2_F9_CompletionUnclosedStringEarlierInFile(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT 'unclosed;\nSELECT u. FROM events u"
	uri := pathToURI(t, filepath.Join(ws, "comp_unclosed_str.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	_, rpcErr := client.callRaw(t, "textDocument/completion", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     utf16PosAfter(t, sql, "u."),
	})
	require.Nil(t, rpcErr)

	cleanSQL := "SELECT 'unclosed';\nSELECT u. FROM events u"
	client.didChange(t, uri, 2, lspContentChange{Text: cleanSQL})

	labels := completionLabels(client.completion(t, uri, utf16PosAfter(t, cleanSQL, "u.")))
	assert.Contains(t, labels, "event_date")
	assert.Contains(t, labels, "tenant_id")
}

func TestT2_F9_CompletionFunctionPositionalUnits(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT DATE_TRUNC(event_date, "
	uri := pathToURI(t, filepath.Join(ws, "comp_units.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	labels := completionLabels(client.completion(t, uri, utf16PosAfter(t, sql, "DATE_TRUNC(event_date, ")))
	joined := strings.ToUpper(strings.Join(labels, ","))
	assert.Contains(t, joined, "DAY")
	assert.Contains(t, joined, "MONTH")
}

func TestT2_F9_CompletionPipeAfterExtendAndRename(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	// GoogleSQL pipe syntax requires `|>` before `WHERE` after a standalone FROM query.
	sql := "FROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS extra_col |> "
	uri := pathToURI(t, filepath.Join(ws, "comp_pipe_ext.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	labels := completionLabels(client.completion(t, uri, utf16PosAfter(t, sql, "extra_col |> ")))
	assert.Contains(t, labels, "extra_col")
	assert.Contains(t, labels, "event_date")
}

func TestT2_F9_CompletionInsideJinjaRawBlock(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "{% raw %} SELECT {{ not_jinja }} {% endraw %}"
	uri := pathToURI(t, filepath.Join(ws, "comp_raw.sql.j2"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	items := client.completion(t, uri, utf16PosAfter(t, sql, "not_jinja"))
	labels := completionLabels(items)
	assert.NotContains(t, labels, "is_incremental")
}

// --- F10 Boundaries ---

func TestT2_F10_DefinitionOnExternalTableReturnsEmpty(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT tenant_id FROM events WHERE event_date = '2026-01-01'"
	uri := pathToURI(t, filepath.Join(ws, "def_ext.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	locs := client.definition(t, uri, utf16PosAt(t, sql, "events"))
	assert.Empty(t, locs)
}

func TestT2_F10_DefinitionShadowedAliasInSubquery(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := strings.Join([]string{
		"SELECT u.country",
		"FROM users u",
		"WHERE EXISTS (",
		"  SELECT 1 FROM events u",
		"  WHERE u.event_date = '2026-01-01'",
		")",
	}, "\n")
	uri := pathToURI(t, filepath.Join(ws, "def_shadow.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	locs := client.definition(t, uri, utf16PosAt(t, sql, "u.event_date"))
	require.NotEmpty(t, locs)
	assert.Equal(t, 3, locs[0].Range.Start.Line)
}

func TestT2_F10_DocumentSymbolsOnBrokenSQL(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := strings.Join([]string{
		"CREATE TEMP TABLE good_tmp AS SELECT 1 AS id;",
		"SELECT ((( FROM WHERE;",
	}, "\n")
	uri := pathToURI(t, filepath.Join(ws, "sym_broken.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	syms, _ := client.documentSymbols(t, uri)
	assert.Contains(t, strings.Join(flattenSymbolNames(syms), ","), "good_tmp")
}

func TestT2_F10_DocumentSymbolsEmptyAndCommentOnly(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	for i, src := range []string{"", "-- comment only\n"} {
		uri := pathToURI(t, filepath.Join(ws, "sym_empty_"+string(rune('a'+i))+".sql"))
		client.didOpen(t, uri, "bqlang", src, 1)

		syms, raw := client.documentSymbols(t, uri)
		assert.Empty(t, syms)
		assert.NotEqual(t, "null", strings.TrimSpace(string(raw)))
	}
}

func TestT2_F10_DefinitionWithUTF16SurrogatePrefix(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT '😀'; WITH cte AS (SELECT 1 AS id) SELECT id FROM cte"
	uri := pathToURI(t, filepath.Join(ws, "def_utf16.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	locs := client.definition(t, uri, utf16PosAtNth(t, sql, "cte", 1))
	require.NotEmpty(t, locs)
	assert.Equal(t, utf16PosAtNth(t, sql, "cte", 0).Character, locs[0].Range.Start.Character)
}

// --- F11 Boundaries ---

func TestT2_F11_FormattingSyntaxErrorGraceful(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, filepath.Join(ws, "fmt_err.sql"))
	client.didOpen(t, uri, "bqlang", "SELECT FROM WHERE", 1)

	edits, _ := client.formatting(t, uri, 2, true)
	assert.Empty(t, edits)

	// Server stays responsive after failed format.
	uri2 := pathToURI(t, filepath.Join(ws, "fmt_ok.sql"))
	client.didOpen(t, uri2, "bqlang", "select 1", 1)
	okEdits, rpcErr := client.formatting(t, uri2, 2, true)
	require.Nil(t, rpcErr)
	assert.Equal(t, "SELECT 1\n", applyTextEdits("select 1", okEdits))
}

func TestT2_F11_FormattingEmptyDocument(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, filepath.Join(ws, "fmt_empty.sql"))
	client.didOpen(t, uri, "bqlang", "", 1)

	edits, rpcErr := client.formatting(t, uri, 2, true)
	require.Nil(t, rpcErr)
	assert.Empty(t, edits)
}

func TestT2_F11_FormattingCRLFLineEndings(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	rawSQL := "select   1   as   a\r\n"
	uri := pathToURI(t, filepath.Join(ws, "fmt_crlf.sql"))
	client.didOpen(t, uri, "bqlang", rawSQL, 1)

	edits, rpcErr := client.formatting(t, uri, 2, true)
	require.Nil(t, rpcErr)
	assert.Equal(t, "SELECT 1 AS a\n", applyTextEdits(rawSQL, edits))
}

func TestT2_F11_FormattingRespectsTabSizeOrBqfmtConfig(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), "indentation = 4\n")

	rawSQL := "begin select 1; end;\n"
	uri := pathToURI(t, filepath.Join(ws, "fmt_tab.sql"))

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)
	client.didOpen(t, uri, "bqlang", rawSQL, 1)

	edits, rpcErr := client.formatting(t, uri, 2, true)
	require.Nil(t, rpcErr)
	assert.Contains(t, applyTextEdits(rawSQL, edits), "\n    SELECT 1;\n")
}

func TestT2_F11_FormattingPipeAndProceduralScript(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	rawSQL := "begin declare x int64 default 1; from events |> where x = 1; end;\n"
	uri := pathToURI(t, filepath.Join(ws, "fmt_script.sql"))
	client.didOpen(t, uri, "bqlang", rawSQL, 1)

	edits1, rpcErr := client.formatting(t, uri, 2, true)
	require.Nil(t, rpcErr)
	once := applyTextEdits(rawSQL, edits1)
	assert.Contains(t, once, "DECLARE x INT64 DEFAULT 1;")
	assert.Contains(t, once, "|>")

	client.didChange(t, uri, 2, lspContentChange{Text: once})
	edits2, rpcErr := client.formatting(t, uri, 2, true)
	require.Nil(t, rpcErr)
	assert.Empty(t, edits2, "formatting must be idempotent")
}
