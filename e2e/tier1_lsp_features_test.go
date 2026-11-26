package e2e_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- F8: Rich Markdown Hover ---

func TestT1_F8_TableHoverCard(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT tenant_id FROM events WHERE event_date = '2026-01-01'"
	uri := pathToURI(t, filepath.Join(ws, "hover_tbl.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	h, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "events"))
	require.Nil(t, rpcErr)
	require.NotNil(t, h)
	assert.Equal(t, "markdown", h.Contents.Kind)
	assert.Contains(t, h.Contents.Value, "my-proj.analytics.events")
	assert.Contains(t, h.Contents.Value, "Raw clickstream events table")
	assert.Contains(t, h.Contents.Value, "event_date")
	assert.Contains(t, h.Contents.Value, "tenant_id")
}

func TestT1_F8_PartitionAndClusterColumnHover(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT u.event_date, u.tenant_id FROM events u WHERE u.event_date = '2026-01-01'"
	uri := pathToURI(t, filepath.Join(ws, "hover_col.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	hPart, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "event_date"))
	require.Nil(t, rpcErr)
	require.NotNil(t, hPart)
	assert.Contains(t, hPart.Contents.Value, "DATE")
	assert.Contains(t, strings.ToUpper(hPart.Contents.Value), "PARTITION")

	hClust, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "tenant_id"))
	require.Nil(t, rpcErr)
	require.NotNil(t, hClust)
	assert.Contains(t, hClust.Contents.Value, "STRING")
	assert.Contains(t, strings.ToUpper(hClust.Contents.Value), "CLUSTER")
}

func TestT1_F8_NestedStructFieldHover(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT u.user_ctx.user_id FROM events u WHERE u.event_date = '2026-01-01'"
	uri := pathToURI(t, filepath.Join(ws, "hover_struct.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	h, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "user_id"))
	require.Nil(t, rpcErr)
	require.NotNil(t, h)
	assert.Contains(t, h.Contents.Value, "INT64")
	assert.Contains(t, h.Contents.Value, "Numeric user ID")
}

func TestT1_F8_CTEAndScriptVariableHover(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := strings.Join([]string{
		"DECLARE x INT64 DEFAULT 1;",
		"WITH cte AS (SELECT event_date, tenant_id FROM events WHERE event_date = '2026-01-01')",
		"SELECT c.tenant_id, x FROM cte c;",
	}, "\n")
	uri := pathToURI(t, filepath.Join(ws, "hover_cte.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	hVar, rpcErr := client.hover(t, uri, utf16PosAtNth(t, sql, "x", 1))
	require.Nil(t, rpcErr)
	require.NotNil(t, hVar)
	assert.Contains(t, hVar.Contents.Value, "INT64")

	hCTE, rpcErr := client.hover(t, uri, utf16PosAtNth(t, sql, "cte", 1))
	require.Nil(t, rpcErr)
	require.NotNil(t, hCTE)
	assert.Contains(t, hCTE.Contents.Value, "cte")
}

func TestT1_F8_BuiltinFunctionAndEmptyWhitespaceHover(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT DATE_TRUNC(event_date, DAY)    FROM events WHERE event_date = '2026-01-01'"
	uri := pathToURI(t, filepath.Join(ws, "hover_fn.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	hFn, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "DATE_TRUNC"))
	require.Nil(t, rpcErr)
	require.NotNil(t, hFn)
	assert.Contains(t, strings.ToUpper(hFn.Contents.Value), "DATE_TRUNC")

	hBlank, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "    "))
	require.Nil(t, rpcErr)
	assert.Nil(t, hBlank)
}

// --- F9: Context-Aware Completion ---

func TestT1_F9_QualifiedAliasAndNestedStructCompletion(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql1 := "SELECT u. FROM events u"
	uri1 := pathToURI(t, filepath.Join(ws, "comp_alias.sql"))
	client.didOpen(t, uri1, "bqlang", sql1, 1)

	labels1 := completionLabels(client.completion(t, uri1, utf16PosAfter(t, sql1, "u.")))
	assert.Contains(t, labels1, "event_date")
	assert.Contains(t, labels1, "tenant_id")
	assert.Contains(t, labels1, "user_ctx")
	assert.NotContains(t, labels1, "country")

	sql2 := "SELECT u.user_ctx. FROM events u"
	uri2 := pathToURI(t, filepath.Join(ws, "comp_struct.sql"))
	client.didOpen(t, uri2, "bqlang", sql2, 1)

	labels2 := completionLabels(client.completion(t, uri2, utf16PosAfter(t, sql2, "u.user_ctx.")))
	assert.Contains(t, labels2, "user_id")
	assert.Contains(t, labels2, "tags")
}

func TestT1_F9_BrokenSQLPhantomCursorCompletion(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT , tenant_id FROM events"
	uri := pathToURI(t, filepath.Join(ws, "comp_broken.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	labels := completionLabels(client.completion(t, uri, utf16PosAfter(t, sql, "SELECT ")))
	assert.Contains(t, labels, "event_date")
	assert.Contains(t, labels, "tenant_id")
}

func TestT1_F9_TablePathAndCTECompletion(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "WITH cte_orders AS (SELECT 1 AS order_id) SELECT order_id FROM "
	uri := pathToURI(t, filepath.Join(ws, "comp_from.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	labels := completionLabels(client.completion(t, uri, utf16PosAfter(t, sql, "FROM ")))
	assert.Contains(t, labels, "cte_orders")
	assert.Contains(t, strings.Join(labels, ","), "events")
}

func TestT1_F9_PipeOperatorAndIncomingColumns(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	// GoogleSQL pipe syntax requires `|>` before `WHERE` after a standalone FROM query.
	sql := "FROM events |> WHERE event_date = '2026-01-01' |> "
	uri := pathToURI(t, filepath.Join(ws, "comp_pipe.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	labels := completionLabels(client.completion(t, uri, utf16PosAfter(t, sql, "'2026-01-01' |> ")))
	joined := strings.ToUpper(strings.Join(labels, ","))
	assert.Contains(t, joined, "EXTEND")
	assert.Contains(t, joined, "AGGREGATE")
	assert.Contains(t, joined, "WHERE")
	assert.Contains(t, labels, "event_date")
	assert.Contains(t, labels, "tenant_id")
}

func TestT1_F9_JinjaAndDbtCompletion(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sqlExpr := "{% macro my_mac() %}{% endmacro %}\n{% set my_var = 1 %}\nSELECT 1 FROM {{  }}"
	uri1 := pathToURI(t, filepath.Join(ws, "comp_jinja.sql.j2"))
	client.didOpen(t, uri1, "bqlang", sqlExpr, 1)

	labels1 := completionLabels(client.completion(t, uri1, utf16PosAfter(t, sqlExpr, "{{ ")))
	for _, want := range []string{"ref", "source", "config", "var", "is_incremental", "this", "my_var", "my_mac"} {
		assert.Contains(t, labels1, want)
	}

	sqlLoop := "{% for x in items %}{{ loop. }}{% endfor %}"
	uri2 := pathToURI(t, filepath.Join(ws, "comp_loop.sql.j2"))
	client.didOpen(t, uri2, "bqlang", sqlLoop, 1)

	labels2 := completionLabels(client.completion(t, uri2, utf16PosAfter(t, sqlLoop, "loop.")))
	assert.Contains(t, strings.Join(labels2, ","), "index")
	assert.Contains(t, strings.Join(labels2, ","), "first")
}

// --- F10: Go-to-Definition & Document Symbols ---

func TestT1_F10_CTEDefinition(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "WITH my_cte AS (\n  SELECT 1 AS id\n)\nSELECT id FROM my_cte"
	uri := pathToURI(t, filepath.Join(ws, "def_cte.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	locs := client.definition(t, uri, utf16PosAtNth(t, sql, "my_cte", 1))
	require.NotEmpty(t, locs)
	assert.Equal(t, uri, locs[0].URI)
	assert.Equal(t, 0, locs[0].Range.Start.Line)
}

func TestT1_F10_TableAliasDefinition(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT u.event_date\nFROM events u\nWHERE u.event_date = '2026-01-01'"
	uri := pathToURI(t, filepath.Join(ws, "def_alias.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	locs := client.definition(t, uri, utf16PosAt(t, sql, "u.event_date"))
	require.NotEmpty(t, locs)
	assert.Equal(t, uri, locs[0].URI)
	assert.Equal(t, 1, locs[0].Range.Start.Line)
}

func TestT1_F10_DeclareAndTempTableDefinition(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := strings.Join([]string{
		"DECLARE v_dt DATE DEFAULT DATE '2026-01-01';",
		"CREATE TEMP TABLE tmp_tbl AS SELECT event_date FROM events WHERE event_date = v_dt;",
		"SELECT event_date FROM tmp_tbl;",
	}, "\n")
	uri := pathToURI(t, filepath.Join(ws, "def_script.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	locsVar := client.definition(t, uri, utf16PosAtNth(t, sql, "v_dt", 1))
	require.NotEmpty(t, locsVar)
	assert.Equal(t, 0, locsVar[0].Range.Start.Line)

	locsTbl := client.definition(t, uri, utf16PosAtNth(t, sql, "tmp_tbl", 1))
	require.NotEmpty(t, locsTbl)
	assert.Equal(t, 1, locsTbl[0].Range.Start.Line)
}

func TestT1_F10_JinjaMacroAndSetDefinition(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := strings.Join([]string{
		"{% macro render_filters() %}event_date >= '2026-01-01'{% endmacro %}",
		"{% set cutoff = '2026-01-01' %}",
		"SELECT tenant_id FROM events WHERE {{ render_filters() }} AND event_date >= {{ cutoff }}",
	}, "\n")
	uri := pathToURI(t, filepath.Join(ws, "def_jinja.sql.j2"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	locsMac := client.definition(t, uri, utf16PosAtNth(t, sql, "render_filters", 1))
	require.NotEmpty(t, locsMac)
	assert.Equal(t, 0, locsMac[0].Range.Start.Line)

	locsSet := client.definition(t, uri, utf16PosAtNth(t, sql, "cutoff", 1))
	require.NotEmpty(t, locsSet)
	assert.Equal(t, 1, locsSet[0].Range.Start.Line)
}

func TestT1_F10_DocumentSymbolsOutline(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := strings.Join([]string{
		"{% macro helper_macro() %}1{% endmacro %}",
		"CREATE TEMP TABLE tmp_out AS SELECT 1 AS id;",
		"BEGIN",
		"  WITH step_one AS (SELECT id FROM tmp_out)",
		"  SELECT id FROM step_one;",
		"END;",
	}, "\n")
	uri := pathToURI(t, filepath.Join(ws, "symbols.sql.j2"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	syms, _ := client.documentSymbols(t, uri)
	require.NotEmpty(t, syms)

	names := strings.Join(flattenSymbolNames(syms), ",")
	assert.Contains(t, names, "helper_macro")
	assert.Contains(t, names, "tmp_out")
	assert.Contains(t, names, "step_one")
}

// --- F11: LSP Formatting ---

func TestT1_F11_BasicSQLFormattingMatchesBqfmt(t *testing.T) {
	t.Parallel()

	bqfmt := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws, cfgPath := setupStandardWorkspace(t, "")

	rawSQL := "select a,b from t where a=1 and b=2\n"
	sqlPath := filepath.Join(ws, "fmt.sql")
	writeFile(t, sqlPath, rawSQL)

	cliRes := runCLI(t, bqfmt, ws, env.env, "", "-config", cfgPath, sqlPath)
	require.Equal(t, 0, cliRes.exitCode, cliRes.stderr)

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, sqlPath)
	client.didOpen(t, uri, "bqlang", rawSQL, 1)

	edits, rpcErr := client.formatting(t, uri, 2, true)
	require.Nil(t, rpcErr)
	assert.Equal(t, cliRes.stdout, applyTextEdits(rawSQL, edits))
}

func TestT1_F11_FormattingHonorsBqfmtTomlStyles(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), "keyword_style = \"LOWER_CASE\"\nindentation = 4\n")

	rawSQL := "WITH c AS (SELECT 1 AS x) SELECT x FROM c\n"
	sqlPath := filepath.Join(ws, "styled.sql")
	writeFile(t, sqlPath, rawSQL)

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, sqlPath)
	client.didOpen(t, uri, "bqlang", rawSQL, 1)

	edits, rpcErr := client.formatting(t, uri, 2, true)
	require.Nil(t, rpcErr)

	formatted := applyTextEdits(rawSQL, edits)
	assert.Contains(t, formatted, "with\n    c as (")
	assert.Contains(t, formatted, "from c")
}

func TestT1_F11_FormattingJinjaTemplate(t *testing.T) {
	t.Parallel()

	bqfmt := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws, cfgPath := setupStandardWorkspace(t, "")

	rawSQL := "{# comment #}\nselect a from {{ ref('a') }} {% if flag %} where a = 1 {% endif %}\n"
	sqlPath := filepath.Join(ws, "fmt.sql.j2")
	writeFile(t, sqlPath, rawSQL)

	cliRes := runCLI(t, bqfmt, ws, env.env, "", "-config", cfgPath, sqlPath)
	require.Equal(t, 0, cliRes.exitCode, cliRes.stderr)

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, sqlPath)
	client.didOpen(t, uri, "bqlang", rawSQL, 1)

	edits, rpcErr := client.formatting(t, uri, 2, true)
	require.Nil(t, rpcErr)

	formatted := applyTextEdits(rawSQL, edits)
	assert.Equal(t, cliRes.stdout, formatted)
	assert.Contains(t, formatted, "{{ ref('a') }}")
	assert.Contains(t, formatted, "{# comment #}")
}

func TestT1_F11_AlreadyFormattedReturnsEmptyEdits(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	formattedSQL := "SELECT 1 AS a\n"
	uri := pathToURI(t, filepath.Join(ws, "clean.sql"))
	client.didOpen(t, uri, "bqlang", formattedSQL, 1)

	edits, rpcErr := client.formatting(t, uri, 2, true)
	require.Nil(t, rpcErr)
	assert.Empty(t, edits)
}

func TestT1_F11_UTF16EditRanges(t *testing.T) {
	t.Parallel()

	bqfmt := requireBqfmt(t)
	env := newIsolatedEnv(t)
	ws, cfgPath := setupStandardWorkspace(t, "")

	rawSQL := "select   '😀'   as   emoji,   tenant_id   from   events\n"
	sqlPath := filepath.Join(ws, "emoji_fmt.sql")
	writeFile(t, sqlPath, rawSQL)

	cliRes := runCLI(t, bqfmt, ws, env.env, "", "-config", cfgPath, sqlPath)
	require.Equal(t, 0, cliRes.exitCode, cliRes.stderr)

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, sqlPath)
	client.didOpen(t, uri, "bqlang", rawSQL, 1)

	edits, rpcErr := client.formatting(t, uri, 2, true)
	require.Nil(t, rpcErr)
	assert.Equal(t, cliRes.stdout, applyTextEdits(rawSQL, edits))

	// Also verify formatting a single-line input without trailing newline uses UTF-16 length on End.Character.
	rawNoNL := "select '😀', a from t"
	noNLPath := filepath.Join(ws, "emoji_no_nl.sql")
	cliNoNL := runCLI(t, bqfmt, ws, env.env, rawNoNL, "-config", cfgPath, "-stdin-filename", noNLPath)
	require.Equal(t, 0, cliNoNL.exitCode, cliNoNL.stderr)

	uriNoNL := pathToURI(t, noNLPath)
	client.didOpen(t, uriNoNL, "bqlang", rawNoNL, 1)

	editsNoNL, rpcErr := client.formatting(t, uriNoNL, 2, true)
	require.Nil(t, rpcErr)
	require.NotEmpty(t, editsNoNL)

	lastEdit := editsNoNL[len(editsNoNL)-1]
	if lastEdit.Range.End.Line == 0 {
		assert.Equal(t, utf16Len(rawNoNL), lastEdit.Range.End.Character)
	}
	assert.Equal(t, cliNoNL.stdout, applyTextEdits(rawNoNL, editsNoNL))
}

// --- F12: BigQuery Function Signature Help & Usage Docs ---

func TestT1_F12_SignatureHelpAndRichFunctionDocs(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	initRes := client.initialize(t, ws)
	caps, _ := initRes["capabilities"].(map[string]any)
	require.NotNil(t, caps["signatureHelpProvider"], "bqpls must advertise signatureHelpProvider")

	sql := "SELECT DATE_TRUNC(event_date, DAY), SAFE.PARSE_DATE('%Y-%m-%d', tenant_id) FROM events WHERE event_date = '2026-01-01'"
	uri := pathToURI(t, filepath.Join(ws, "sighelp.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	sh, rpcErr := client.signatureHelp(t, uri, utf16PosAfter(t, sql, "event_date, "))
	require.Nil(t, rpcErr)
	require.NotNil(t, sh)
	require.NotEmpty(t, sh.Signatures)
	require.NotNil(t, sh.ActiveSignature)
	require.NotNil(t, sh.ActiveParameter)
	assert.Equal(t, 1, *sh.ActiveParameter)
	active := sh.Signatures[*sh.ActiveSignature]
	assert.Contains(t, active.Label, "DATE_TRUNC(")
	require.NotNil(t, active.Documentation)
	assert.Contains(t, active.Documentation.Value, "Truncates")

	// Hover on SAFE.PARSE_DATE shows rich docs and SAFE. note.
	hSafe, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "PARSE_DATE"))
	require.Nil(t, rpcErr)
	require.NotNil(t, hSafe)
	assert.Contains(t, hSafe.Contents.Value, "SAFE.PARSE_DATE")
	assert.Contains(t, hSafe.Contents.Value, "BigQuery Documentation")

	// Outside a call -> null.
	shNone, rpcErr := client.signatureHelp(t, uri, lspPosition{Line: 0, Character: 0})
	require.Nil(t, rpcErr)
	assert.Nil(t, shNone)
}
