package e2e_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- F4 Boundaries ---

func TestT2_F4_MalformedFramingAndInvalidJSON(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	curNull := client.nullErrCursor()
	badBody := `{invalid!`
	client.sendRawBytes(t, fmt.Appendf(nil, "Content-Length: %d\r\n\r\n%s", len(badBody), badBody))

	_, errEnv := client.waitNullIDErr(t, curNull, 10*time.Second)
	require.NotNil(t, errEnv.Error)
	assert.Equal(t, -32700, errEnv.Error.Code)

	_, rpcErr := client.callRaw(t, "bqpls/nonexistentMethod", map[string]any{})
	require.NotNil(t, rpcErr)
	assert.Equal(t, -32601, rpcErr.Code)

	// Verify server stays alive after protocol errors.
	uri := pathToURI(t, filepath.Join(ws, "alive.sql"))
	client.didOpen(t, uri, "bqlang", "SELECT 1 AS ok", 1)
	tok := client.semanticTokensFull(t, uri)
	_, decErr := decodeSemanticTokens(tok.Data)
	require.NoError(t, decErr)
}

func TestT2_F4_UninitializedRequestRejected(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)

	uri := pathToURI(t, filepath.Join(ws, "pre_init.sql"))
	_, rpcErr := client.hover(t, uri, lspPosition{Line: 0, Character: 0})
	require.NotNil(t, rpcErr)
	assert.Equal(t, -32002, rpcErr.Code)

	// Proceed to initialize cleanly after rejection.
	initRes := client.initialize(t, ws)
	assert.NotNil(t, initRes["capabilities"])
}

func TestT2_F4_SequentialMultiChangeInOneNotification(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, filepath.Join(ws, "multi_change.sql"))
	initial := "SELECT tenant_id FROM events WHERE event_date = '2026-01-01'"
	client.didOpen(t, uri, "bqlang", initial, 1)

	// Edit 1 inserts `'😀', ` (6 UTF-16 units) before tenant_id;
	// Edit 2 replaces `tenant_id` using post-Edit-1 UTF-16 offset!
	client.didChange(t, uri, 2,
		lspContentChange{
			Range: &lspRange{
				Start: lspPosition{Line: 0, Character: len("SELECT ")},
				End:   lspPosition{Line: 0, Character: len("SELECT ")},
			},
			Text: "'😀', ",
		},
		lspContentChange{
			Range: &lspRange{
				Start: lspPosition{Line: 0, Character: len("SELECT ") + utf16Len("'😀', ")},
				End:   lspPosition{Line: 0, Character: len("SELECT ") + utf16Len("'😀', ") + len("tenant_id")},
			},
			Text: "event_type",
		},
	)

	finalSQL := "SELECT '😀', event_type FROM events WHERE event_date = '2026-01-01'"
	tok := client.semanticTokensFull(t, uri)
	decoded, err := decodeSemanticTokens(tok.Data)
	require.NoError(t, err)

	etTok, ok := findTokenAt(decoded, utf16PosAt(t, finalSQL, "event_type"))
	require.True(t, ok, "expected token for event_type after sequential multi-edit")
	assert.NotZero(t, etTok.tokenModifiers&modClustered)
}

func TestT2_F4_CancelRequestAndUnknownCancelID(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	var lines []string
	for i := range 80 {
		lines = append(lines, fmt.Sprintf("SELECT tenant_id, %d AS seq FROM events WHERE event_date = '2026-01-01';", i))
	}

	uri := pathToURI(t, filepath.Join(ws, "cancel.sql"))
	client.didOpen(t, uri, "bqlang", strings.Join(lines, "\n"), 1)

	chNum := client.startRequest(t, 42, "textDocument/semanticTokens/full", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
	client.cancelRequest(t, 42)

	select {
	case resp := <-chNum:
		if resp.Error != nil {
			assert.Equal(t, -32800, resp.Error.Code)
		} else {
			assert.NotEmpty(t, resp.Result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for cancelled numeric request")
	}

	chStr := client.startRequest(t, "req-cancel-str", "textDocument/completion", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     lspPosition{Line: 0, Character: 7},
	})
	client.cancelRequest(t, "req-cancel-str")

	select {
	case resp := <-chStr:
		if resp.Error != nil {
			assert.Equal(t, -32800, resp.Error.Code)
		} else {
			assert.NotEmpty(t, resp.Result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for cancelled string request")
	}

	// Cancelling unknown ID must not crash or disturb subsequent requests.
	client.cancelRequest(t, 99999)

	h, rpcErr := client.hover(t, uri, lspPosition{Line: 0, Character: 7})
	require.Nil(t, rpcErr)
	require.NotNil(t, h)
}

func TestT2_F4_OutOfBoundsPositionClamping(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, filepath.Join(ws, "empty_oob.sql"))
	client.didOpen(t, uri, "bqlang", "", 1)

	for _, pos := range []lspPosition{
		{Line: 0, Character: 0},
		{Line: 9999, Character: 9999},
	} {
		h, rpcErr := client.hover(t, uri, pos)
		require.Nil(t, rpcErr)
		assert.Nil(t, h)

		_ = client.completion(t, uri, pos)
		locs := client.definition(t, uri, pos)
		assert.Empty(t, locs)
	}
}

// --- F5 Boundaries ---

func TestT2_F5_EmptyAndWhitespaceOnlyDocumentTokens(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	for i, src := range []string{"", "   \n\n  \t "} {
		uri := pathToURI(t, filepath.Join(ws, fmt.Sprintf("ws_%d.sql", i)))
		client.didOpen(t, uri, "bqlang", src, 1)

		raw := client.semanticTokensFullRaw(t, uri)
		assert.Contains(t, string(raw), `"data":[]`)

		tok := client.semanticTokensFull(t, uri)
		assert.Empty(t, tok.Data)
		assert.NotEmpty(t, tok.ResultID)
	}
}

func TestT2_F5_UnclosedStringsCommentsAndJinjaTags(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	cases := []string{
		"SELECT 'unclosed string",
		"SELECT 1 /* unclosed block comment\nstill comment",
		"SELECT 1 FROM {{ unclosed_expr",
	}

	for i, src := range cases {
		uri := pathToURI(t, filepath.Join(ws, fmt.Sprintf("unclosed_%d.sql.j2", i)))
		client.didOpen(t, uri, "bqlang", src, 1)

		tok := client.semanticTokensFull(t, uri)
		decoded, err := decodeSemanticTokens(tok.Data)
		require.NoError(t, err)
		assert.NotEmpty(t, decoded)
	}
}

func TestT2_F5_MultiLineCommentAndTripleQuotedSplit(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "/* line1\nline2\nline3 */\nSELECT '''a\nb\nc''' AS s"
	uri := pathToURI(t, filepath.Join(ws, "multiline.sql"))
	client.didOpen(t, uri, "bqlang", sql, 1)

	tok := client.semanticTokensFull(t, uri)
	decoded, err := decodeSemanticTokens(tok.Data)
	require.NoError(t, err)

	lines := strings.Split(sql, "\n")
	for _, tk := range decoded {
		lineUnits := uint32(utf16Len(lines[tk.line]))
		assert.LessOrEqual(t, tk.startChar+tk.length, lineUnits, "token on line %d exceeds line length", tk.line)
	}

	for lineIdx := range uint32(3) {
		ct, ok := findTokenAt(decoded, lspPosition{Line: int(lineIdx), Character: 0})
		require.True(t, ok, "expected comment token on line %d", lineIdx)
		assert.Equal(t, tokComment, ct.tokenType)
	}
}

func TestT2_F5_LKGRetentionAndShiftingOnSyntaxError(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	validSQL := "SELECT event_date, tenant_id\nFROM events\nWHERE event_date = '2026-01-01'"
	uri := pathToURI(t, filepath.Join(ws, "lkg.sql"))
	client.didOpen(t, uri, "bqlang", validSQL, 1)

	before := client.semanticTokensFull(t, uri)
	beforeDec, err := decodeSemanticTokens(before.Data)
	require.NoError(t, err)
	evBefore, ok := findTokenAt(beforeDec, utf16PosAt(t, validSQL, "event_date"))
	require.True(t, ok)
	require.NotZero(t, evBefore.tokenModifiers&modPartitioned)

	// Insert blank line at top and break syntax at bottom.
	client.didChange(t, uri, 2,
		lspContentChange{
			Range: &lspRange{
				Start: lspPosition{Line: 0, Character: 0},
				End:   lspPosition{Line: 0, Character: 0},
			},
			Text: "\n",
		},
		lspContentChange{
			Range: &lspRange{
				Start: lspPosition{Line: 3, Character: 0},
				End:   lspPosition{Line: 3, Character: len("WHERE event_date = '2026-01-01'")},
			},
			Text: "WHERE (((",
		},
	)

	after := client.semanticTokensFull(t, uri)
	afterDec, err := decodeSemanticTokens(after.Data)
	require.NoError(t, err)

	shiftedPos := utf16PosAt(t, validSQL, "event_date")
	shiftedPos.Line++
	evAfter, ok := findTokenAt(afterDec, shiftedPos)
	require.True(t, ok, "expected LKG token retained and shifted to line %d", shiftedPos.Line)
	assert.NotZero(t, evAfter.tokenModifiers&modPartitioned)
}

func TestT2_F5_StalePreviousResultIdAndNoOpDelta(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, filepath.Join(ws, "stale_delta.sql"))
	client.didOpen(t, uri, "bqlang", "SELECT event_date FROM events WHERE event_date = '2026-01-01'", 1)

	full := client.semanticTokensFull(t, uri)
	require.NotEmpty(t, full.ResultID)

	deltaNoOp, rawNoOp := client.semanticTokensFullDelta(t, uri, full.ResultID)
	assert.NotEmpty(t, deltaNoOp.ResultID)
	assert.Empty(t, deltaNoOp.Edits)
	assert.Empty(t, deltaNoOp.Data)

	var rawMap map[string]any
	require.NoError(t, json.Unmarshal(rawNoOp, &rawMap))
	editsVal, hasEditsKey := rawMap["edits"]
	require.True(t, hasEditsKey, "no-op delta JSON must include \"edits\" key: %s", string(rawNoOp))
	editsArr, isArr := editsVal.([]any)
	require.True(t, isArr, "expected \"edits\" to be JSON array: %s", string(rawNoOp))
	assert.Empty(t, editsArr)

	deltaStale, _ := client.semanticTokensFullDelta(t, uri, "stale-999")
	assert.NotEmpty(t, deltaStale.ResultID)
	assert.Equal(t, full.Data, deltaStale.Data)
}

// --- F6 Boundaries ---

func TestT2_F6_CaseInsensitiveAndBacktickedIdentifiers(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT EVENT_DATE, TENANT_ID FROM `MY-PROJ.Analytics.Events` WHERE EVENT_DATE = '2026-01-01' AND TENANT_ID = 't1'"
	uri := pathToURI(t, filepath.Join(ws, "case.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", sql, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev.Diagnostics)
}

func TestT2_F6_IngestionTimePartitioningPseudoCols(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws := t.TempDir()
	ing := standardEventsSchema()
	ing.ID = "my-proj:analytics.ing_only"
	ing.TableReference.TableID = "ing_only"
	ing.TimePartitioning = &schemaTimePartitioning{
		Type:                   "DAY",
		RequirePartitionFilter: true,
	}
	writeSchemaTableJSON(t, filepath.Join(ws, "schemas", "my-proj.analytics.ing_only.json"), ing)
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), "[lsp]\ndefault_project = \"my-proj\"\ndefault_dataset = \"analytics\"\nschema_dirs = [\"./schemas\"]\nbigquery_api_enabled = false\n")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT _PARTITIONTIME, _PARTITIONDATE, tenant_id FROM ing_only WHERE _PARTITIONDATE = '2026-01-01' AND tenant_id = 't1'"
	uri := pathToURI(t, filepath.Join(ws, "ing.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", sql, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev.Diagnostics)

	tok := client.semanticTokensFull(t, uri)
	decoded, err := decodeSemanticTokens(tok.Data)
	require.NoError(t, err)

	pt, ok := findTokenAt(decoded, utf16PosAt(t, sql, "_PARTITIONTIME"))
	require.True(t, ok)
	assert.NotZero(t, pt.tokenModifiers&modPseudo)
	assert.NotZero(t, pt.tokenModifiers&modPartitioned)
}

func TestT2_F6_ExpiredDiskCacheEvicted(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws := t.TempDir()
	cacheDir := filepath.Join(ws, "cache")

	expired := usersSchema()
	expired.ID = "my-proj:analytics.expired_tbl"
	expired.TableReference.TableID = "expired_tbl"
	writeDiskCacheTable(t, cacheDir, expired, time.Now().Add(-2*time.Hour))

	cfg := fmt.Sprintf("[lsp]\ndefault_project = %q\ndefault_dataset = %q\nbigquery_api_enabled = false\ncache_dir = %q\ncache_ttl_seconds = 60\n",
		"my-proj", "analytics", filepath.ToSlash(cacheDir))
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), cfg)

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT country FROM expired_tbl"
	uri := pathToURI(t, filepath.Join(ws, "expired.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", sql, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev.Diagnostics, "evicted cache miss should not emit false-positive table-not-found diagnostics")

	h, rpcErr := client.hover(t, uri, utf16PosAt(t, sql, "country"))
	require.Nil(t, rpcErr)
	assert.Nil(t, h, "expected nil hover when expired disk cache is evicted")
}

func TestT2_F6_OfflineBigQueryAPINonBlocking(t *testing.T) {
	t.Parallel()

	binDir := t.TempDir()
	stubGcloud := filepath.Join(binDir, "gcloud")
	writeFile(t, stubGcloud, "#!/bin/sh\nexit 1\n")
	require.NoError(t, os.Chmod(stubGcloud, 0o755))

	env := newIsolatedEnv(t, "PATH="+binDir+":/usr/bin:/bin")
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), "[lsp]\ndefault_project = \"p\"\ndefault_dataset = \"d\"\nbigquery_api_enabled = true\n")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	start := time.Now()
	uri := pathToURI(t, filepath.Join(ws, "offline_api.sql"))
	client.didOpen(t, uri, "bqlang", "SELECT col FROM remote_missing_tbl", 1)
	_ = client.semanticTokensFull(t, uri)
	_, rpcErr := client.hover(t, uri, lspPosition{Line: 0, Character: 16})
	require.Nil(t, rpcErr)

	assert.Less(t, time.Since(start), 2*time.Second, "offline BigQuery API must not block LSP requests")
}

func TestT2_F6_CorruptSchemaJSONResilience(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")
	writeFile(t, filepath.Join(ws, "schemas", "my-proj.analytics.corrupt.json"), `{not valid json`)

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT tenant_id FROM events WHERE event_date = '2026-01-01' AND tenant_id = 't1'"
	uri := pathToURI(t, filepath.Join(ws, "resilient.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", sql, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev.Diagnostics)
}

// --- F7 Boundaries ---

func TestT2_F7_DiagnosticsDisabledToggle(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "diagnostics_enabled = false\n")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, filepath.Join(ws, "no_diag.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", "SELECT FROM WHERE", 1)

	// Force a full analysis/server round-trip first, then briefly poll diagnostics.
	tok := client.semanticTokensFull(t, uri)
	_, decErr := decodeSemanticTokens(tok.Data)
	require.NoError(t, decErr)

	if ev, ok := client.pollDiagnostics(t, uri, cur, 300*time.Millisecond); ok {
		assert.Empty(t, ev.Diagnostics, "diagnostics_enabled = false must not emit non-empty diagnostics")
	}

	// Verify hover/semanticTokens remain healthy on a valid document when diagnostics are disabled.
	validSQL := "SELECT tenant_id FROM events"
	validURI := pathToURI(t, filepath.Join(ws, "no_diag_valid.sql"))
	cur2 := client.diagCursor()
	client.didOpen(t, validURI, "bqlang", validSQL, 1)

	h, rpcErr := client.hover(t, validURI, utf16PosAt(t, validSQL, "tenant_id"))
	require.Nil(t, rpcErr)
	require.NotNil(t, h)
	assert.Contains(t, h.Contents.Value, "tenant_id")

	if ev2, ok := client.pollDiagnostics(t, validURI, cur2, 300*time.Millisecond); ok {
		assert.Empty(t, ev2.Diagnostics, "diagnostics_enabled = false must suppress require_partition_filter too")
	}
}

func TestT2_F7_UncachedTableSuppressionWhenAPIEnabled(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t, "PATH=/usr/bin:/bin")
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), "[lsp]\nbigquery_api_enabled = true\n")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri1 := pathToURI(t, filepath.Join(ws, "uncached.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri1, "bqlang", "SELECT x FROM unknown_proj.ds.tbl", 1)
	cur, ev1 := client.waitDiagnostics(t, uri1, cur, 10*time.Second)
	assert.Empty(t, ev1.Diagnostics, "expected uncached table false positives to be suppressed")

	uri2 := pathToURI(t, filepath.Join(ws, "syntax.sql"))
	client.didOpen(t, uri2, "bqlang", "SELECT FROM WHERE", 1)
	_, ev2 := client.waitDiagnostics(t, uri2, cur, 10*time.Second)
	assert.NotEmpty(t, ev2.Diagnostics, "syntax errors must still report even when API enabled")
}

func TestT2_F7_SecondaryJinjaElifElseNoDuplicateErrors(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := strings.Join([]string{
		"SELECT tenant_id FROM events",
		"{% if a %}",
		"WHERE event_date = '2026-01-01' AND tenant_id = 't1'",
		"{% elif b %}",
		"WHERE event_date = '2026-01-02' AND tenant_id = 't2'",
		"{% else %}",
		"WHERE event_date = '2026-01-03' AND tenant_id = 't3'",
		"{% endif %}",
	}, "\n")

	uri := pathToURI(t, filepath.Join(ws, "branches.sql.j2"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", sql, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev.Diagnostics)
}

func TestT2_F7_RangePartitioningFilterCheck(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws := t.TempDir()
	ranged := schemaTableSpec{
		Kind:                   "bigquery#table",
		ID:                     "my-proj:analytics.bucketed",
		TableReference:         schemaTableRef{ProjectID: "my-proj", DatasetID: "analytics", TableID: "bucketed"},
		Type:                   "TABLE",
		RequirePartitionFilter: true,
		RangePartitioning: &schemaRangePartitioning{
			Field: "bucket_id",
			Range: schemaRangeBounds{Start: "0", End: "100", Interval: "10"},
		},
		Schema: schemaTableSchema{
			Fields: []schemaFieldSpec{
				{Name: "bucket_id", Type: "INT64", Mode: "REQUIRED"},
				{Name: "val", Type: "STRING", Mode: "NULLABLE"},
			},
		},
	}
	writeSchemaTableJSON(t, filepath.Join(ws, "schemas", "my-proj.analytics.bucketed.json"), ranged)
	writeFile(t, filepath.Join(ws, ".bqfmt.toml"), "[lsp]\ndefault_project = \"my-proj\"\ndefault_dataset = \"analytics\"\nschema_dirs = [\"./schemas\"]\nbigquery_api_enabled = false\n")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	uri := pathToURI(t, filepath.Join(ws, "range.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", "SELECT val FROM bucketed", 1)

	cur, ev1 := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	require.NotEmpty(t, ev1.Diagnostics)
	assert.Contains(t, strings.ToLower(ev1.Diagnostics[0].Message), "partition")

	client.didChange(t, uri, 2, lspContentChange{Text: "SELECT val FROM bucketed WHERE bucket_id = 10"})
	_, ev2 := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev2.Diagnostics)
}

func TestT2_F7_PartitionedInsideSubqueryOrAlias(t *testing.T) {
	t.Parallel()

	env := newIsolatedEnv(t)
	ws, _ := setupStandardWorkspace(t, "")

	client := startLSP(t, ws, env.env)
	client.initialize(t, ws)

	sql := "SELECT e.tenant_id FROM (SELECT event_date, tenant_id FROM events WHERE event_date = '2026-01-01' AND tenant_id = 't1') e"
	uri := pathToURI(t, filepath.Join(ws, "subq.sql"))
	cur := client.diagCursor()
	client.didOpen(t, uri, "bqlang", sql, 1)

	_, ev := client.waitDiagnostics(t, uri, cur, 10*time.Second)
	assert.Empty(t, ev.Diagnostics)
}
