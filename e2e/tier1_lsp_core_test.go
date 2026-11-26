package e2e_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- F4: LSP 3.17 Transport & Document Lifecycle ---

func TestT1_F4_InitializeCapabilitiesAndLegend(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	initRes := client.initialize(t, ws)

	caps, ok := initRes["capabilities"].(map[string]any)
	require.True(t, ok, "missing capabilities object")

	switch syncVal := caps["textDocumentSync"].(type) {
	case float64:
		assert.InDelta(t, float64(2), syncVal, 0.001)
	case map[string]any:
		assert.InDelta(t, float64(2), syncVal["change"], 0.001)
	default:
		t.Fatalf("unexpected textDocumentSync type %T", caps["textDocumentSync"])
	}

	semProv, ok := caps["semanticTokensProvider"].(map[string]any)
	require.True(t, ok, "missing semanticTokensProvider")

	legend, ok := semProv["legend"].(map[string]any)
	require.True(t, ok, "missing semanticTokensProvider.legend")

	rawLegend, err := json.Marshal(legend)
	require.NoError(t, err)

	var parsedLegend struct {
		TokenTypes     []string `json:"tokenTypes"`
		TokenModifiers []string `json:"tokenModifiers"`
	}
	require.NoError(t, json.Unmarshal(rawLegend, &parsedLegend))
	assert.Equal(t, canonicalTokenTypes, parsedLegend.TokenTypes)
	assert.Equal(t, canonicalTokenModifiers, parsedLegend.TokenModifiers)

	fullOpt, ok := semProv["full"].(map[string]any)
	require.True(t, ok, "expected semanticTokensProvider.full object with delta: true")
	assert.Equal(t, true, fullOpt["delta"])
	assert.NotNil(t, semProv["range"])

	assert.NotNil(t, caps["hoverProvider"])
	assert.NotNil(t, caps["definitionProvider"])
	assert.NotNil(t, caps["documentSymbolProvider"])
	assert.NotNil(t, caps["documentFormattingProvider"])

	compProv, ok := caps["completionProvider"].(map[string]any)
	require.True(t, ok, "missing completionProvider")
	assert.Contains(t, compProv["triggerCharacters"], ".")
}

func TestT1_F4_DidOpenChangeSaveCloseLifecycle(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, filepath.Join(ws, "lifecycle.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", "SELECT tenant_id FROM events", 1)
	cur, ev1 := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.NotEmpty(t, ev1.Diagnostics)

	client.didChange(t, uri, 2, lspContentChange{
		Range: &lspRange{
			Start: lspPosition{Line: 0, Character: len("SELECT tenant_id FROM events")},
			End:   lspPosition{Line: 0, Character: len("SELECT tenant_id FROM events")},
		},
		Text: " WHERE event_date = '2026-01-01' AND tenant_id = 't1'",
	})
	_, ev2 := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev2.Diagnostics)

	client.didSave(t, uri, "SELECT tenant_id FROM events WHERE event_date = '2026-01-01' AND tenant_id = 't1'")
	client.didClose(t, uri)

	h, rpcErr := client.hover(t, uri, lspPosition{Line: 0, Character: 7})
	if rpcErr == nil {
		assert.Nil(t, h)
	}

	// Server remains alive for new documents after close.
	uri2 := pathToURI(t, filepath.Join(ws, "after_close.sql"))
	client.didOpen(t, uri2, "bqlang", "SELECT 1", 1)
	tok := client.semanticTokensFull(t, uri2)
	_, decErr := decodeSemanticTokens(tok.Data)
	require.NoError(t, decErr)
}

func TestT1_F4_IncrementalEditsSingleAndMultiLine(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, filepath.Join(ws, "inc.sql"))
	client.didOpen(t, uri, "bqlang", "SELECT tenant_id\nFROM events WHERE event_date = '2026-01-01'", 1)

	client.didChange(t, uri, 2, lspContentChange{
		Range: &lspRange{
			Start: lspPosition{Line: 0, Character: len("SELECT ")},
			End:   lspPosition{Line: 0, Character: len("SELECT ")},
		},
		Text: "event_date,\n  ",
	})

	finalSQL := "SELECT event_date,\n  tenant_id\nFROM events WHERE event_date = '2026-01-01'"
	tok := client.semanticTokensFull(t, uri)
	decoded, err := decodeSemanticTokens(tok.Data)
	require.NoError(t, err)

	tenantTok, ok := findTokenAt(decoded, utf16PosAt(t, finalSQL, "tenant_id"))
	require.True(t, ok, "expected semantic token at shifted tenant_id position")
	assert.NotZero(t, tenantTok.tokenModifiers&modClustered)

	h, rpcErr := client.hover(t, uri, utf16PosAt(t, finalSQL, "tenant_id"))
	require.Nil(t, rpcErr)
	require.NotNil(t, h)
	assert.Contains(t, h.Contents.Value, "tenant_id")
}

func TestT1_F4_UTF16SurrogatePairPositions(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT '😀' AS emoji, event_date FROM events WHERE event_date = '2026-01-01'"
	uri := pathToURI(t, filepath.Join(ws, "utf16.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	wantPos := utf16PosAt(t, sql, "event_date")

	tok := client.semanticTokensFull(t, uri)
	decoded, err := decodeSemanticTokens(tok.Data)
	require.NoError(t, err)

	evTok, ok := findTokenAt(decoded, wantPos)
	require.True(t, ok, "expected semantic token at UTF-16 position %+v", wantPos)
	assert.Equal(t, uint32(wantPos.Character), evTok.startChar)
	assert.NotZero(t, evTok.tokenModifiers&modPartitioned)

	h, rpcErr := client.hover(t, uri, wantPos)
	require.Nil(t, rpcErr)
	require.NotNil(t, h)
	assert.Contains(t, h.Contents.Value, "event_date")
}

func TestT1_F4_ShutdownAndExitClean(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	code := client.shutdownAndExit(t)
	assert.Equal(t, 0, code)
}

// --- F5: Semantic Tokens (full, full/delta, range, Modifiers & LKG) ---

func TestT1_F5_SQLTokensAndFiveCommentStyles(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := strings.Join([]string{
		"-- dash comment",
		"# hash comment",
		"// slash comment",
		"/* block comment */",
		"{# jinja comment #}",
		"FROM events",
		"|> WHERE event_date = DATE '2026-01-01'",
		"|> AGGREGATE COUNT(*) AS n",
	}, "\n")

	uri := pathToURI(t, filepath.Join(ws, "comments.sql.j2"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	tok := client.semanticTokensFull(t, uri)
	decoded, err := decodeSemanticTokens(tok.Data)
	require.NoError(t, err)
	assert.NotEmpty(t, decoded)

	for _, cmt := range []string{"-- dash", "# hash", "// slash", "/* block", "{# jinja"} {
		ct, ok := findTokenAt(decoded, utf16PosAt(t, sql, cmt))
		require.True(t, ok, "missing comment token for %q", cmt)
		assert.Equal(t, tokComment, ct.tokenType, "expected comment token type for %q", cmt)
	}

	countTok, ok := findTokenAt(decoded, utf16PosAt(t, sql, "COUNT"))
	require.True(t, ok, "missing COUNT token")
	assert.Equal(t, tokFunction, countTok.tokenType)
	assert.NotZero(t, countTok.tokenModifiers&modDefaultLibrary)
}

func TestT1_F5_PartitionedClusteredPseudoModifiers(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws := t.TempDir()
	ingestionTable := standardEventsSchema()
	ingestionTable.TableReference.TableID = "events_ing"
	ingestionTable.ID = "my-proj:analytics.events_ing"
	ingestionTable.TimePartitioning = &schemaTimePartitioning{Type: "DAY"}
	writeSchemaTableJSON(t, filepath.Join(ws, "schemas", "my-proj.analytics.events.json"), standardEventsSchema())
	writeSchemaTableJSON(t, filepath.Join(ws, "schemas", "my-proj.analytics.events_ing.json"), ingestionTable)
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), "[lsp]\ndefault_project = \"my-proj\"\ndefault_dataset = \"analytics\"\nschema_dirs = [\"./schemas\"]\nbigquery_api_enabled = false\n")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT u.event_date, u.tenant_id, i._PARTITIONTIME\nFROM events u\nJOIN events_ing i ON u.event_date = DATE(i._PARTITIONTIME)\nWHERE u.event_date = '2026-01-01'"
	uri := pathToURI(t, filepath.Join(ws, "mods.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	tok := client.semanticTokensFull(t, uri)
	decoded, err := decodeSemanticTokens(tok.Data)
	require.NoError(t, err)

	evTok, ok := findTokenAt(decoded, utf16PosAtNth(t, sql, "event_date", 0))
	require.True(t, ok)
	assert.NotZero(t, evTok.tokenModifiers&modPartitioned)

	tenTok, ok := findTokenAt(decoded, utf16PosAt(t, sql, "tenant_id"))
	require.True(t, ok)
	assert.NotZero(t, tenTok.tokenModifiers&modClustered)

	ptTok, ok := findTokenAt(decoded, utf16PosAtNth(t, sql, "_PARTITIONTIME", 0))
	require.True(t, ok)
	assert.NotZero(t, ptTok.tokenModifiers&modPseudo)
	assert.NotZero(t, ptTok.tokenModifiers&modPartitioned)
}

func TestT1_F5_NestedStructFieldModifiers(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws := t.TempDir()
	nestedTable := schemaTableSpec{
		Kind:           "bigquery#table",
		ID:             "my-proj:analytics.nested_events",
		TableReference: schemaTableRef{ProjectID: "my-proj", DatasetID: "analytics", TableID: "nested_events"},
		Type:           "TABLE",
		Clustering:     &schemaClustering{Fields: []string{"user_ctx.user_id"}},
		Schema: schemaTableSchema{
			Fields: []schemaFieldSpec{
				{Name: "event_date", Type: "DATE", Mode: "REQUIRED"},
				{
					Name: "user_ctx",
					Type: "RECORD",
					Fields: []schemaFieldSpec{
						{Name: "user_id", Type: "INT64", Mode: "REQUIRED"},
					},
				},
			},
		},
	}
	writeSchemaTableJSON(t, filepath.Join(ws, "schemas", "my-proj.analytics.nested_events.json"), nestedTable)
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), "[lsp]\ndefault_project = \"my-proj\"\ndefault_dataset = \"analytics\"\nschema_dirs = [\"./schemas\"]\nbigquery_api_enabled = false\n")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT u.user_ctx.user_id FROM nested_events u"
	uri := pathToURI(t, filepath.Join(ws, "struct.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	tok := client.semanticTokensFull(t, uri)
	decoded, err := decodeSemanticTokens(tok.Data)
	require.NoError(t, err)

	parentTok, hasParent := findTokenAt(decoded, utf16PosAt(t, sql, "user_ctx"))
	require.True(t, hasParent, "expected semantic token on parent struct user_ctx")
	assert.Zero(t, parentTok.tokenModifiers&modClustered, "clustering modifier must apply only to leaf field, not parent struct")

	leafTok, ok := findTokenAt(decoded, utf16PosAt(t, sql, "user_id"))
	require.True(t, ok, "expected semantic token on leaf struct field user_id")
	assert.Equal(t, tokProperty, leafTok.tokenType)
	assert.NotZero(t, leafTok.tokenModifiers&modClustered)
}

func TestT1_F5_SemanticTokensFullDeltaSplicing(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql1 := "SELECT event_date, tenant_id FROM events WHERE event_date = '2026-01-01'"
	uri := pathToURI(t, filepath.Join(ws, "delta.sql"))
	client.didOpen(t, uri, "bqlang", sql1, 1)

	full1 := client.semanticTokensFull(t, uri)
	require.NotEmpty(t, full1.ResultID)

	client.didChange(t, uri, 2, lspContentChange{
		Range: &lspRange{
			Start: lspPosition{Line: 0, Character: len("SELECT event_date, ")},
			End:   lspPosition{Line: 0, Character: len("SELECT event_date, tenant_id")},
		},
		Text: "event_type",
	})

	delta, _ := client.semanticTokensFullDelta(t, uri, full1.ResultID)
	full2 := client.semanticTokensFull(t, uri)

	require.NotEmpty(t, delta.Edits, "expected minimal delta edits for valid previousResultId")
	assert.Empty(t, delta.Data, "expected delta response without full data fallback")
	reconstructed := applySemanticTokenEdits(t, full1.Data, delta.Edits)

	assert.Equal(t, full2.Data, reconstructed)
}

func TestT1_F5_SemanticTokensRangeAndJinja(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := strings.Join([]string{
		"{% if flag %}",
		"SELECT event_date",
		"FROM {{ ref('orders') }}",
		"{% else %}",
		"SELECT 1",
		"{% endif %}",
	}, "\n")
	uri := pathToURI(t, filepath.Join(ws, "jinja_range.sql.j2"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	rngTok := client.semanticTokensRange(t, uri, lspRange{
		Start: lspPosition{Line: 1, Character: 0},
		End:   lspPosition{Line: 2, Character: 24},
	})
	decoded, err := decodeSemanticTokens(rngTok.Data)
	require.NoError(t, err)
	assert.NotEmpty(t, decoded)

	for _, tok := range decoded {
		assert.GreaterOrEqual(t, tok.line, uint32(1))
		assert.LessOrEqual(t, tok.line, uint32(2))
	}

	full := client.semanticTokensFull(t, uri)
	fullDec, err := decodeSemanticTokens(full.Data)
	require.NoError(t, err)

	refTok, ok := findTokenAt(fullDec, utf16PosAt(t, sql, "ref"))
	require.True(t, ok, "expected macro token for Jinja ref")
	assert.Equal(t, tokMacro, refTok.tokenType)
}

// --- F6: BigQuery Metadata Catalog, Wildcards & Caching ---

func TestT1_F6_SchemaDirsFlatAndNestedLayout(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws := t.TempDir()
	schemaDir := filepath.Join(ws, "schemas")

	flat := usersSchema()
	flat.ID = "proj1:ds1.flat_tbl"
	flat.TableReference = schemaTableRef{ProjectID: "proj1", DatasetID: "ds1", TableID: "flat_tbl"}
	writeSchemaTableJSON(t, filepath.Join(schemaDir, "proj1.ds1.flat_tbl.json"), flat)

	nested := usersSchema()
	nested.ID = "proj2:ds2.nested_tbl"
	nested.TableReference = schemaTableRef{ProjectID: "proj2", DatasetID: "ds2", TableID: "nested_tbl"}
	writeSchemaTableJSON(t, filepath.Join(schemaDir, "proj2", "ds2", "nested_tbl.json"), nested)

	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), "[lsp]\nschema_dirs = [\"./schemas\"]\nbigquery_api_enabled = false\n")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, filepath.Join(ws, "layouts.sql"))
	sql := "SELECT a.country, b.user_id FROM `proj1.ds1.flat_tbl` a JOIN `proj2.ds2.nested_tbl` b ON a.user_id = b.user_id"
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", sql, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev.Diagnostics)
}

func TestT1_F6_RESTv2AndBqShowFormats(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws := t.TempDir()
	schemaDir := filepath.Join(ws, "schemas")

	restTbl := standardEventsSchema()
	restTbl.NumRows = "42"
	writeSchemaTableJSON(t, filepath.Join(schemaDir, "my-proj.analytics.events.json"), restTbl)

	writeBqShowSchemaArrayJSON(t, filepath.Join(schemaDir, "my-proj.analytics.bqshow_tbl.json"), []schemaFieldSpec{
		{Name: "id", Type: "INTEGER", Mode: "REQUIRED", Description: "Primary row key"},
		{Name: "amount", Type: "FLOAT", Mode: "NULLABLE"},
		{Name: "is_active", Type: "BOOLEAN", Mode: "REQUIRED"},
	})

	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), "[lsp]\ndefault_project = \"my-proj\"\ndefault_dataset = \"analytics\"\nschema_dirs = [\"./schemas\"]\nbigquery_api_enabled = false\n")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, filepath.Join(ws, "formats.sql"))
	sql := "SELECT b.id + 1, b.amount * 2.0, b.is_active, e.tenant_id FROM bqshow_tbl b JOIN events e ON e.event_date = '2026-01-01' AND e.tenant_id = 't1'"
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", sql, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev.Diagnostics)

	h, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "id"))
	require.Nil(t, rpcErr)
	require.NotNil(t, h)
	assert.Contains(t, h.Contents.Value, "INT64")
}

func TestT1_F6_WildcardTableSuffix(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws := t.TempDir()
	evShard := standardEventsSchema()
	evShard.RequirePartitionFilter = false
	evShard.TimePartitioning.RequirePartitionFilter = false
	evShard.TableReference.TableID = "events_20260101"
	evShard.ID = "my-proj:analytics.events_20260101"
	writeSchemaTableJSON(t, filepath.Join(ws, "schemas", "my-proj.analytics.events_20260101.json"), evShard)
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), "[lsp]\ndefault_project = \"my-proj\"\ndefault_dataset = \"analytics\"\nschema_dirs = [\"./schemas\"]\nbigquery_api_enabled = false\n")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT _TABLE_SUFFIX, event_date FROM `my-proj.analytics.events_*` WHERE _TABLE_SUFFIX >= '20260101'"
	uri := pathToURI(t, filepath.Join(ws, "wildcard.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", sql, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev.Diagnostics)

	tok := client.semanticTokensFull(t, uri)
	decoded, err := decodeSemanticTokens(tok.Data)
	require.NoError(t, err)

	sufTok, ok := findTokenAt(decoded, utf16PosAtNth(t, sql, "_TABLE_SUFFIX", 0))
	require.True(t, ok)
	assert.NotZero(t, sufTok.tokenModifiers&modPseudo)
}

func TestT1_F6_DiskCacheHitWithinTTL(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws := t.TempDir()
	cacheDir := filepath.Join(ws, "cache")

	cached := usersSchema()
	cached.ID = "my-proj:analytics.cached_tbl"
	cached.TableReference.TableID = "cached_tbl"
	writeDiskCacheTable(t, cacheDir, cached, time.Now())

	cfg := "[lsp]\ndefault_project = \"my-proj\"\ndefault_dataset = \"analytics\"\nbigquery_api_enabled = false\ncache_dir = \"" + filepath.ToSlash(cacheDir) + "\"\ncache_ttl_seconds = 3600\n"
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), cfg)

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT country FROM cached_tbl"
	uri := pathToURI(t, filepath.Join(ws, "cached.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", sql, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev.Diagnostics)

	h, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "country"))
	require.Nil(t, rpcErr)
	require.NotNil(t, h)
	assert.Contains(t, h.Contents.Value, "ISO country code")
}

func TestT1_F6_ScriptOverlaysTempTableAndDeclare(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := strings.Join([]string{
		"DECLARE cutoff DATE DEFAULT DATE '2026-01-01';",
		"CREATE TEMP TABLE tmp AS",
		"SELECT event_date, tenant_id FROM events WHERE event_date >= cutoff AND tenant_id = 't1';",
		"SELECT tenant_id FROM tmp;",
	}, "\n")

	uri := pathToURI(t, filepath.Join(ws, "script.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", sql, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev.Diagnostics)
}

// --- F7: Diagnostics & require_partition_filter ---

func TestT1_F7_SyntaxAndSemanticErrors(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri1 := pathToURI(t, filepath.Join(ws, "syntax_err.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri1, "bqlang", "SELECT FROM WHERE", 1)
	cur, ev1 := client.waitDiagnostics(t, uri1, cur, 10*time.Second)
	require.NotEmpty(t, ev1.Diagnostics)
	assert.Equal(t, 1, ev1.Diagnostics[0].Severity)

	uri2 := pathToURI(t, filepath.Join(ws, "semantic_err.sql"))
	client.didOpen(t, uri2, "bqlang", "SELECT nonexistent_col FROM events WHERE event_date = '2026-01-01'", 1)
	_, ev2 := client.waitDiagnostics(t, uri2, cur, 10*time.Second)
	require.NotEmpty(t, ev2.Diagnostics)
	assert.Contains(t, strings.ToLower(ev2.Diagnostics[0].Message), "nonexistent_col")
}

func TestT1_F7_RequirePartitionFilterMissingAndSatisfied(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, filepath.Join(ws, "part.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", "SELECT tenant_id FROM events", 1)

	cur, ev1 := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	require.Len(t, ev1.Diagnostics, 2)
	assert.Equal(t, 2, ev1.Diagnostics[1].Severity)
	assert.Contains(t, strings.ToLower(ev1.Diagnostics[1].Message), "partition")

	client.didChange(t, uri, 2, lspContentChange{
		Text: "SELECT tenant_id FROM events WHERE event_date = '2026-01-01' AND tenant_id = 't1'",
	})
	_, ev2 := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Equal(t, "[]", strings.TrimSpace(string(ev2.RawDiagnostics)))
	assert.Empty(t, ev2.Diagnostics)
}

func TestT1_F7_RequirePartitionFilterInJoinOn(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT u.country, e.tenant_id FROM users u JOIN events e ON DATE(u.signup_at) = e.event_date AND u.country = e.tenant_id"
	uri := pathToURI(t, filepath.Join(ws, "join_on.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", sql, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev.Diagnostics)
}

func TestT1_F7_RequirePartitionFilterSeverityLevels(t *testing.T) {
	t.Parallel()

	cases := []struct {
		level    string
		wantSev  int
		wantNone bool
	}{
		{level: "error", wantSev: 1},
		{level: "info", wantSev: 3},
		{level: "hint", wantSev: 4},
		{level: "off", wantNone: true},
	}

	for _, tc := range cases {
		t.Run(tc.level, func(t *testing.T) {
			t.Parallel()

			env := newIsolatedEnv(t)
			ws, _ := setupStandardWorkspace(t, "require_partition_filter_severity = \""+tc.level+"\"\nclustering_filter_severity = \"off\"\n")

			client := startLSP(t, ws, env.env)
			client.initialize(t, ws)

			uri := pathToURI(t, filepath.Join(ws, "sev.sql"))
			cur := client.diagCursor()
			client.didOpen(t, uri, "bqlang", "SELECT tenant_id FROM events", 1)

			_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
			if tc.wantNone {
				assert.Empty(t, ev.Diagnostics)
			} else {
				require.NotEmpty(t, ev.Diagnostics)
				assert.Equal(t, tc.wantSev, ev.Diagnostics[0].Severity)
			}
		})
	}
}

func TestT1_F7_JinjaTemplateOffsetMappedDiagnostics(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := strings.Join([]string{
		"SELECT tenant_id",
		"{% if include_bad %}",
		"  , bad_col",
		"{% endif %}",
		"FROM events WHERE event_date = '2026-01-01'",
	}, "\n")

	uri := pathToURI(t, filepath.Join(ws, "offset.sql.j2"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", sql, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	require.NotEmpty(t, ev.Diagnostics)
	assert.Equal(t, 2, ev.Diagnostics[0].Range.Start.Line)
}
