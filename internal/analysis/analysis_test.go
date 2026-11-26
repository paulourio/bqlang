package analysis_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/internal/analysis"
	"github.com/paulourio/bqlang/internal/bqconfig"
	"github.com/paulourio/bqlang/internal/catalog"
	"github.com/paulourio/bqlang/internal/syntax"
)

type decodedTok struct {
	line      uint32
	startChar uint32
	length    uint32
	tokType   analysis.SemanticTokenType
	mods      analysis.SemanticTokenModifier
}

func decodeTokens(t *testing.T, data []uint32) []decodedTok {
	t.Helper()
	require.Equal(t, 0, len(data)%5)

	out := make([]decodedTok, 0, len(data)/5)
	var line, ch uint32
	for i := 0; i < len(data); i += 5 {
		dl, dc, ln, tp, md := data[i], data[i+1], data[i+2], data[i+3], data[i+4]
		require.Positive(t, ln)
		require.Less(t, tp, uint32(14))
		require.Less(t, md, uint32(128))
		if dl > 0 {
			line += dl
			ch = dc
		} else {
			ch += dc
		}
		out = append(out, decodedTok{
			line:      line,
			startChar: ch,
			length:    ln,
			tokType:   analysis.SemanticTokenType(tp),
			mods:      analysis.SemanticTokenModifier(md),
		})
	}

	return out
}

func applyEdits(prev []uint32, edits []analysis.SemanticTokensEdit) []uint32 {
	cur := append([]uint32(nil), prev...)
	for _, ed := range edits {
		start := int(ed.Start)
		end := start + int(ed.DeleteCount)
		next := make([]uint32, 0, len(cur)-int(ed.DeleteCount)+len(ed.Data))
		next = append(next, cur[:start]...)
		next = append(next, ed.Data...)
		next = append(next, cur[end:]...)
		cur = next
	}

	return cur
}

func utf16Pos(t *testing.T, src, needle string) syntax.Position {
	t.Helper()

	return utf16PosNth(t, src, needle, 0)
}

func utf16PosNth(t *testing.T, src, needle string, nth int) syntax.Position {
	t.Helper()
	li := syntax.NewLineIndex(src)
	searchFrom := 0
	for i := 0; i <= nth; i++ {
		idx := strings.Index(src[searchFrom:], needle)
		require.GreaterOrEqual(t, idx, 0, "needle %q (#%d) not found", needle, nth)
		abs := searchFrom + idx
		if i == nth {
			return li.PositionAt(abs)
		}
		searchFrom = abs + len(needle)
	}

	return syntax.Position{}
}

func utf16PosAfter(t *testing.T, src, needle string) syntax.Position {
	t.Helper()
	idx := strings.Index(src, needle)
	require.GreaterOrEqual(t, idx, 0, "needle %q not found", needle)

	return syntax.NewLineIndex(src).PositionAt(idx + len(needle))
}

func findTokAt(toks []decodedTok, pos syntax.Position) (decodedTok, bool) {
	for _, tk := range toks {
		if int(tk.line) == pos.Line && pos.Character >= int(tk.startChar) && pos.Character < int(tk.startChar+tk.length) {
			return tk, true
		}
	}

	return decodedTok{}, false
}

func newTestStoreAndEngine(t *testing.T) (*catalog.Store, *analysis.Engine) {
	t.Helper()

	opts := bqconfig.DefaultLSPOptions()
	opts.DefaultProject = "my-proj"
	opts.DefaultDataset = "analytics"
	opts.BigQueryAPIEnabled = false
	opts.CacheDir = t.TempDir()

	store, err := catalog.NewStore(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	seedTestTables(store)

	eng, err := analysis.NewEngine(store, opts)
	require.NoError(t, err)

	return store, eng
}

func seedTestTables(store *catalog.Store) {
	store.PutTable(&catalog.TableMeta{
		Ref:         catalog.TableRef{Project: "my-proj", Dataset: "analytics", Table: "events"},
		Type:        catalog.TableTypeTable,
		Description: "Raw clickstream events table",
		TimePartitioning: &catalog.TimePartitioning{
			Type:                   "DAY",
			Field:                  "event_date",
			RequirePartitionFilter: true,
		},
		Clustering:             &catalog.ClusteringInfo{Fields: []string{"tenant_id", "user_ctx.user_id"}},
		RequirePartitionFilter: true,
		Schema: []catalog.ColumnMeta{
			{Name: "event_date", Type: "DATE", Mode: catalog.ModeRequired, Description: "Event partition date"},
			{Name: "tenant_id", Type: "STRING", Mode: catalog.ModeRequired, Description: "Tenant identifier"},
			{
				Name: "user_ctx",
				Type: "RECORD",
				Mode: catalog.ModeNullable,
				Fields: []catalog.ColumnMeta{
					{Name: "user_id", Type: "INT64", Mode: catalog.ModeRequired, Description: "Numeric user ID"},
					{
						Name: "tags",
						Type: "RECORD",
						Mode: catalog.ModeRepeated,
						Fields: []catalog.ColumnMeta{
							{Name: "key", Type: "STRING", Mode: catalog.ModeNullable},
							{Name: "val", Type: "STRING", Mode: catalog.ModeNullable},
						},
					},
				},
			},
			{Name: "date", Type: "STRING", Mode: catalog.ModeNullable},
			{Name: "status", Type: "STRING", Mode: catalog.ModeNullable},
		},
	})

	store.PutTable(&catalog.TableMeta{
		Ref:         catalog.TableRef{Project: "my-proj", Dataset: "analytics", Table: "events_ing"},
		Type:        catalog.TableTypeTable,
		Description: "Ingestion-time partitioned events",
		TimePartitioning: &catalog.TimePartitioning{
			Type:                   "DAY",
			RequirePartitionFilter: true,
		},
		RequirePartitionFilter: true,
		Schema: []catalog.ColumnMeta{
			{Name: "payload", Type: "STRING", Mode: catalog.ModeNullable},
		},
	})

	store.PutTable(&catalog.TableMeta{
		Ref:  catalog.TableRef{Project: "my-proj", Dataset: "analytics", Table: "events_20260101"},
		Type: catalog.TableTypeTable,
		Schema: []catalog.ColumnMeta{
			{Name: "event_date", Type: "DATE", Mode: catalog.ModeRequired},
			{Name: "tenant_id", Type: "STRING", Mode: catalog.ModeRequired},
		},
	})

	store.PutTable(&catalog.TableMeta{
		Ref:  catalog.TableRef{Project: "my-proj", Dataset: "analytics", Table: "range_tbl"},
		Type: catalog.TableTypeTable,
		RangePartitioning: &catalog.RangePartitioning{
			Field: "bucket_id",
			Range: &catalog.IntRange{Start: 0, End: 100, Interval: 10},
		},
		RequirePartitionFilter: true,
		Schema: []catalog.ColumnMeta{
			{Name: "bucket_id", Type: "INT64", Mode: catalog.ModeRequired},
			{Name: "val", Type: "STRING", Mode: catalog.ModeNullable},
		},
	})

	store.PutTable(&catalog.TableMeta{
		Ref:  catalog.TableRef{Project: "my-proj", Dataset: "analytics", Table: "users"},
		Type: catalog.TableTypeTable,
		Schema: []catalog.ColumnMeta{
			{Name: "user_id", Type: "INT64", Mode: catalog.ModeRequired},
			{Name: "country", Type: "STRING", Mode: catalog.ModeNullable},
		},
	})

	store.PutTable(&catalog.TableMeta{
		Ref:  catalog.TableRef{Project: "my-proj", Dataset: "analytics", Table: "min_tbl"},
		Type: catalog.TableTypeTable,
		Schema: []catalog.ColumnMeta{
			{Name: "id", Type: "INT64", Mode: catalog.ModeRequired},
		},
	})
}

func TestDefaultSemanticTokensLegend(t *testing.T) {
	t.Parallel()

	leg := analysis.DefaultSemanticTokensLegend()
	assert.Equal(t, []string{
		"namespace", "class", "function", "variable", "property",
		"parameter", "keyword", "type", "string", "number",
		"comment", "operator", "macro", "enumMember",
	}, leg.TokenTypes)
	assert.Equal(t, []string{
		"declaration", "readonly", "defaultLibrary",
		"partitioned", "clustered", "pseudo", "modification",
	}, leg.TokenModifiers)
}

func TestSemanticTokensCommentsBuiltinsUnitsAndModifiers(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	ctx := context.Background()

	sql := strings.Join([]string{
		"-- dash comment",
		"# hash comment",
		"// slash comment",
		"/* block\ncomment */",
		"{# jinja comment #}",
		"SELECT",
		"  u.event_date,",
		"  u.tenant_id,",
		"  u.user_ctx.user_id,",
		"  u.date,",
		"  u.status,",
		"  i._PARTITIONTIME,",
		"  w._TABLE_SUFFIX,",
		"  DATE_TRUNC(u.event_date, MONTH),",
		"  COUNT(*),",
		"  @@project_id,",
		"  @my_param",
		"FROM {{ ref('events') }} u",
		"JOIN events_ing i ON u.event_date = DATE(i._PARTITIONTIME)",
		"JOIN `my-proj.analytics.events_*` w ON u.tenant_id = w.tenant_id",
		"WHERE u.event_date = DATE '2026-01-01'",
	}, "\n")

	uri := "file:///tokens.sql.j2"
	eng.OpenDocument(uri, sql, 1, nil)

	res, err := eng.SemanticTokensFull(ctx, uri)
	require.NoError(t, err)
	require.NotEmpty(t, res.ResultID)

	toks := decodeTokens(t, res.Data)
	for _, cmt := range []string{"-- dash", "# hash", "// slash", "block", "comment */", "{# jinja"} {
		tk, ok := findTokAt(toks, utf16Pos(t, sql, cmt))
		require.True(t, ok, "missing comment %q", cmt)
		assert.Equal(t, analysis.TokenComment, tk.tokType)
	}

	refTok, ok := findTokAt(toks, utf16Pos(t, sql, "ref"))
	require.True(t, ok)
	assert.Equal(t, analysis.TokenMacro, refTok.tokType)

	countTok, ok := findTokAt(toks, utf16Pos(t, sql, "COUNT"))
	require.True(t, ok)
	assert.Equal(t, analysis.TokenFunction, countTok.tokType)
	assert.NotZero(t, countTok.mods&analysis.ModDefaultLibrary)

	monthTok, ok := findTokAt(toks, utf16Pos(t, sql, "MONTH"))
	require.True(t, ok)
	assert.Equal(t, analysis.TokenEnumMember, monthTok.tokType)

	dateFieldTok, ok := findTokAt(toks, utf16Pos(t, sql, "date,"))
	require.True(t, ok)
	assert.Equal(t, analysis.TokenProperty, dateFieldTok.tokType)

	sysTok, ok := findTokAt(toks, utf16Pos(t, sql, "@@project_id"))
	require.True(t, ok)
	assert.Equal(t, analysis.TokenVariable, sysTok.tokType)
	assert.NotZero(t, sysTok.mods&analysis.ModReadonly)

	paramTok, ok := findTokAt(toks, utf16Pos(t, sql, "@my_param"))
	require.True(t, ok)
	assert.Equal(t, analysis.TokenParameter, paramTok.tokType)

	// Resolved modifiers on a pure SQL query.
	pureSQL := strings.Join([]string{
		"SELECT u.event_date, u.tenant_id, u.user_ctx.user_id, i._PARTITIONTIME, w._TABLE_SUFFIX",
		"FROM events u",
		"JOIN events_ing i ON u.event_date = DATE(i._PARTITIONTIME)",
		"JOIN `my-proj.analytics.events_*` w ON u.tenant_id = w.tenant_id",
		"WHERE u.event_date = '2026-01-01'",
	}, "\n")
	pureURI := "file:///pure.sql"
	eng.OpenDocument(pureURI, pureSQL, 1, nil)
	pureRes, err := eng.SemanticTokensFull(ctx, pureURI)
	require.NoError(t, err)
	pureToks := decodeTokens(t, pureRes.Data)

	evTok, ok := findTokAt(pureToks, utf16Pos(t, pureSQL, "event_date"))
	require.True(t, ok)
	assert.NotZero(t, evTok.mods&analysis.ModPartitioned)

	tenTok, ok := findTokAt(pureToks, utf16Pos(t, pureSQL, "tenant_id"))
	require.True(t, ok)
	assert.NotZero(t, tenTok.mods&analysis.ModClustered)

	parentStructTok, ok := findTokAt(pureToks, utf16Pos(t, pureSQL, "user_ctx"))
	require.True(t, ok)
	assert.Zero(t, parentStructTok.mods&analysis.ModClustered)

	leafStructTok, ok := findTokAt(pureToks, utf16Pos(t, pureSQL, "user_id"))
	require.True(t, ok)
	assert.Equal(t, analysis.TokenProperty, leafStructTok.tokType)
	assert.NotZero(t, leafStructTok.mods&analysis.ModClustered)

	ptTok, ok := findTokAt(pureToks, utf16Pos(t, pureSQL, "_PARTITIONTIME"))
	require.True(t, ok)
	assert.NotZero(t, ptTok.mods&analysis.ModPseudo)
	assert.NotZero(t, ptTok.mods&analysis.ModPartitioned)

	sufTok, ok := findTokAt(pureToks, utf16Pos(t, pureSQL, "_TABLE_SUFFIX"))
	require.True(t, ok)
	assert.NotZero(t, sufTok.mods&analysis.ModPseudo)
}

func TestSemanticTokensMultilineUTF16DeltaRangeAndLKG(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	ctx := context.Background()

	sql := "/* a\nb\nc */\nSELECT '😀', event_date, tenant_id\nFROM events\nWHERE event_date = '2026-01-01'"
	uri := "file:///delta_lkg.sql"
	eng.OpenDocument(uri, sql, 1, nil)

	full1, err := eng.SemanticTokensFull(ctx, uri)
	require.NoError(t, err)

	lines := strings.Split(sql, "\n")
	for _, tk := range decodeTokens(t, full1.Data) {
		lineUnits := uint32(len(utf16.Encode([]rune(lines[tk.line]))))
		assert.LessOrEqual(t, tk.startChar+tk.length, lineUnits)
	}

	// No-op delta must return non-nil empty Edits.
	noop, err := eng.SemanticTokensFullDelta(ctx, uri, full1.ResultID)
	require.NoError(t, err)
	assert.True(t, noop.IsDelta)
	require.NotNil(t, noop.Edits)
	assert.Empty(t, noop.Edits)

	// Stale ID falls back to full Data.
	stale, err := eng.SemanticTokensFullDelta(ctx, uri, "stale-id")
	require.NoError(t, err)
	assert.False(t, stale.IsDelta)
	assert.Equal(t, full1.Data, stale.Data)

	// Range query returns tokens overlapping line 3 only.
	rngRes, err := eng.SemanticTokensRange(ctx, uri, syntax.Range{
		Start: syntax.Position{Line: 3, Character: 0},
		End:   syntax.Position{Line: 3, Character: 100},
	})
	require.NoError(t, err)
	for _, tk := range decodeTokens(t, rngRes.Data) {
		assert.Equal(t, uint32(3), tk.line)
	}

	// Edit: insert newline at top and break WHERE at bottom -> LKG retains ModPartitioned shifted +1 line.
	require.NoError(t, eng.ChangeDocument(uri, 2, []syntax.TextChange{
		{
			Range: &syntax.Range{Start: syntax.Position{Line: 0, Character: 0}, End: syntax.Position{Line: 0, Character: 0}},
			Text:  "\n",
		},
		{
			Range: &syntax.Range{
				Start: syntax.Position{Line: 5, Character: 0},
				End:   syntax.Position{Line: 5, Character: len("WHERE event_date = '2026-01-01'")},
			},
			Text: "WHERE (((",
		},
	}))

	delta, err := eng.SemanticTokensFullDelta(ctx, uri, stale.ResultID)
	require.NoError(t, err)
	assert.True(t, delta.IsDelta)

	full2, err := eng.SemanticTokensFull(ctx, uri)
	require.NoError(t, err)
	assert.Equal(t, full2.Data, applyEdits(stale.Data, delta.Edits))

	shiftedPos := utf16Pos(t, sql, "event_date")
	shiftedPos.Line++
	evTok, ok := findTokAt(decodeTokens(t, full2.Data), shiftedPos)
	require.True(t, ok, "expected LKG token retained on shifted line")
	assert.NotZero(t, evTok.mods&analysis.ModPartitioned)
}

func TestDiagnosticsSyntaxSemanticPartitionAndSuppression(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("require_partition_filter_modes", func(t *testing.T) {
		t.Parallel()
		_, eng := newTestStoreAndEngine(t)

		// Missing partition and cluster filters -> both warnings.
		uriMissing := "file:///missing_pf.sql"
		eng.OpenDocument(uriMissing, "SELECT tenant_id FROM events", 1, nil)
		diags, err := eng.Diagnostics(ctx, uriMissing)
		require.NoError(t, err)
		require.Len(t, diags, 2)
		assert.Equal(t, "clustering_filter", diags[0].Code)
		assert.Equal(t, analysis.SeverityWarning, diags[1].Severity)
		assert.Equal(t, "require_partition_filter", diags[1].Code)

		// Satisfied in WHERE.
		eng.OpenDocument("file:///ok_where.sql", "SELECT tenant_id FROM events WHERE event_date = '2026-01-01' AND tenant_id = 't1'", 1, nil)
		diagsOK, err := eng.Diagnostics(ctx, "file:///ok_where.sql")
		require.NoError(t, err)
		assert.Empty(t, diagsOK)

		// Satisfied in JOIN ON.
		eng.OpenDocument("file:///ok_join.sql", "SELECT u.user_id FROM users u JOIN events e ON e.event_date = '2026-01-01' AND e.tenant_id = u.country", 1, nil)
		diagsJoin, err := eng.Diagnostics(ctx, "file:///ok_join.sql")
		require.NoError(t, err)
		assert.Empty(t, diagsJoin)

		// Satisfied via CTE + outer WHERE.
		eng.OpenDocument("file:///ok_cte.sql", "WITH c AS (SELECT event_date, tenant_id FROM events) SELECT tenant_id FROM c WHERE event_date = '2026-01-01' AND tenant_id = 't1'", 1, nil)
		diagsCTE, err := eng.Diagnostics(ctx, "file:///ok_cte.sql")
		require.NoError(t, err)
		assert.Empty(t, diagsCTE)

		// Satisfied via ingestion-time _PARTITIONDATE.
		eng.OpenDocument("file:///ok_ing.sql", "SELECT payload FROM events_ing WHERE _PARTITIONDATE = '2026-01-01'", 1, nil)
		diagsIng, err := eng.Diagnostics(ctx, "file:///ok_ing.sql")
		require.NoError(t, err)
		assert.Empty(t, diagsIng)

		// Integer RANGE partition filter required.
		eng.OpenDocument("file:///miss_range.sql", "SELECT val FROM range_tbl", 1, nil)
		diagsRange, err := eng.Diagnostics(ctx, "file:///miss_range.sql")
		require.NoError(t, err)
		require.Len(t, diagsRange, 1)
		assert.Equal(t, "require_partition_filter", diagsRange[0].Code)

		// Severity off + DiagnosticsEnabled false.
		offOpts := bqconfig.DefaultLSPOptions()
		offOpts.DefaultProject = "my-proj"
		offOpts.DefaultDataset = "analytics"
		offOpts.RequirePartitionFilterSeverity = bqconfig.SeverityOff
		offOpts.ClusteringFilterSeverity = bqconfig.SeverityOff
		require.NoError(t, eng.UpdateDocumentConfig(uriMissing, &analysis.DocumentConfig{Options: offOpts}))
		diagsOff, err := eng.Diagnostics(ctx, uriMissing)
		require.NoError(t, err)
		assert.Empty(t, diagsOff)

		disOpts := bqconfig.DefaultLSPOptions()
		disOpts.DiagnosticsEnabled = false
		eng.OpenDocument("file:///disabled.sql", "SELECT ((( FROM WHERE", 1, &analysis.DocumentConfig{Options: disOpts})
		diagsDis, err := eng.Diagnostics(ctx, "file:///disabled.sql")
		require.NoError(t, err)
		require.NotNil(t, diagsDis)
		assert.Empty(t, diagsDis)
	})

	t.Run("jinja_elif_else_and_script_state", func(t *testing.T) {
		t.Parallel()
		_, eng := newTestStoreAndEngine(t)

		sql := strings.Join([]string{
			"DECLARE dt DATE DEFAULT DATE '2026-01-01';",
			"BEGIN",
			"  CREATE TEMP TABLE tmp_ev AS SELECT event_date, tenant_id FROM events WHERE event_date = dt AND tenant_id = 'a';",
			"  SELECT tenant_id FROM tmp_ev",
			"  {% if true %} WHERE tenant_id = 'a' {% elif false %} WHERE tenant_id = 'b' {% else %} WHERE tenant_id = 'c' {% endif %};",
			"END;",
		}, "\n")
		eng.OpenDocument("file:///script.sql.j2", sql, 1, nil)
		diags, err := eng.Diagnostics(ctx, "file:///script.sql.j2")
		require.NoError(t, err)
		assert.Empty(t, diags)

		// Bad column surfaces semantic error.
		eng.OpenDocument("file:///bad_col.sql", "SELECT nonexistent_col FROM users", 1, nil)
		badDiags, err := eng.Diagnostics(ctx, "file:///bad_col.sql")
		require.NoError(t, err)
		require.NotEmpty(t, badDiags)
		assert.Equal(t, "semantic", badDiags[0].Code)
	})

	t.Run("uncached_table_suppression_when_api_enabled", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		opts := bqconfig.DefaultLSPOptions()
		opts.DefaultProject = "my-proj"
		opts.DefaultDataset = "analytics"
		opts.BigQueryAPIEnabled = true
		opts.CacheDir = t.TempDir()

		rest := catalog.NewRestClient(
			catalog.WithBaseURL(srv.URL),
			catalog.WithTokenProvider(func(context.Context) (string, error) { return "tok", nil }),
			catalog.WithTimeout(time.Second),
		)

		var eng *analysis.Engine
		store, err := catalog.NewStore(opts, catalog.WithRestClient(rest), catalog.WithOnUpdate(func(ref catalog.TableRef) {
			if eng != nil {
				eng.InvalidateCatalog(ref)
			}
		}))
		require.NoError(t, err)
		defer func() { _ = store.Close() }()

		eng, err = analysis.NewEngine(store, opts)
		require.NoError(t, err)

		eng.OpenDocument("file:///uncached.sql", "SELECT x FROM remote_proj.remote_ds.remote_tbl", 1, nil)
		diags, err := eng.Diagnostics(ctx, "file:///uncached.sql")
		require.NoError(t, err)
		assert.Empty(t, diags)

		// Syntax errors still appear even with API enabled.
		eng.OpenDocument("file:///uncached_syn.sql", "SELECT FROM WHERE", 1, nil)
		synDiags, err := eng.Diagnostics(ctx, "file:///uncached_syn.sql")
		require.NoError(t, err)
		require.NotEmpty(t, synDiags)
		assert.Equal(t, "syntax", synDiags[0].Code)
	})

	t.Run("uncached_external_table_suppressed_when_api_disabled", func(t *testing.T) {
		t.Parallel()

		opts := bqconfig.DefaultLSPOptions()
		opts.BigQueryAPIEnabled = false
		opts.CacheDir = t.TempDir()

		store, err := catalog.NewStore(opts)
		require.NoError(t, err)
		defer func() { _ = store.Close() }()

		eng, err := analysis.NewEngine(store, opts)
		require.NoError(t, err)

		eng.OpenDocument(
			"file:///public_data.sql",
			"SELECT word, word_count FROM `bigquery-public-data.samples.shakespeare` LIMIT 10",
			1,
			nil,
		)
		diags, err := eng.Diagnostics(ctx, "file:///public_data.sql")
		require.NoError(t, err)
		assert.Empty(t, diags, "uncached external tables must not emit false-positive table-not-found errors")

		// Syntax errors still surface without metadata.
		eng.OpenDocument("file:///public_data_syn.sql", "SELECT FROM `bigquery-public-data.samples.shakespeare`", 1, nil)
		synDiags, err := eng.Diagnostics(ctx, "file:///public_data_syn.sql")
		require.NoError(t, err)
		require.NotEmpty(t, synDiags)
		assert.Equal(t, "syntax", synDiags[0].Code)
	})
}

func TestHoverCards(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, eng := newTestStoreAndEngine(t)

	sql := strings.Join([]string{
		"{# jinja comment #}",
		"-- sql comment",
		"DECLARE v_dt DATE DEFAULT DATE '2026-01-01';",
		"CREATE TEMP TABLE tmp_u AS SELECT user_id FROM users;",
		"WITH cte AS (SELECT event_date, tenant_id FROM events WHERE event_date = v_dt)",
		"SELECT u.event_date, u.tenant_id, u.user_ctx.user_id, u.user_ctx.tags, DATE_TRUNC(u.event_date, DAY), i._PARTITIONTIME, i._PARTITIONDATE, w._TABLE_SUFFIX, c.tenant_id, t.user_id",
		"FROM events u",
		"JOIN events_ing i ON u.event_date = DATE(i._PARTITIONTIME)",
		"JOIN `my-proj.analytics.events_*` w ON u.tenant_id = w.tenant_id",
		"JOIN cte c ON u.tenant_id = c.tenant_id",
		"JOIN tmp_u t ON u.user_ctx.user_id = t.user_id",
		"JOIN min_tbl m ON u.user_ctx.user_id = m.id",
		"WHERE u.event_date = '2026-01-01'",
	}, "\n")

	uri := "file:///hover.sql.j2"
	eng.OpenDocument(uri, sql, 1, nil)

	hNilCmt, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "sql comment"))
	require.NoError(t, err)
	assert.Nil(t, hNilCmt)

	hNilJinjaCmt, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "jinja comment"))
	require.NoError(t, err)
	assert.Nil(t, hNilJinjaCmt)

	hTbl, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "events u"))
	require.NoError(t, err)
	require.NotNil(t, hTbl)
	assert.Equal(t, "markdown", hTbl.Contents.Kind)
	assert.Contains(t, hTbl.Contents.Value, "my-proj.analytics.events")
	assert.Contains(t, hTbl.Contents.Value, "Raw clickstream events table")

	hMin, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "min_tbl"))
	require.NoError(t, err)
	require.NotNil(t, hMin)
	assert.NotContains(t, hMin.Contents.Value, "<nil>")

	hPart, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "u.event_date") /* on u -> table */)
	require.NoError(t, err)
	require.NotNil(t, hPart)
	assert.Contains(t, hPart.Contents.Value, "my-proj.analytics.events")

	hPartCol, err := eng.Hover(ctx, uri, utf16PosNth(t, sql, "event_date", 1))
	require.NoError(t, err)
	require.NotNil(t, hPartCol)
	assert.Contains(t, hPartCol.Contents.Value, "DATE")
	assert.Contains(t, hPartCol.Contents.Value, "PARTITION")

	hClustCol, err := eng.Hover(ctx, uri, utf16PosNth(t, sql, "tenant_id", 1))
	require.NoError(t, err)
	require.NotNil(t, hClustCol)
	assert.Contains(t, hClustCol.Contents.Value, "CLUSTER")

	hStructField, err := eng.Hover(ctx, uri, utf16PosNth(t, sql, "user_id", 1))
	require.NoError(t, err)
	require.NotNil(t, hStructField)
	assert.Contains(t, hStructField.Contents.Value, "Numeric user ID")

	hTmp, err := eng.Hover(ctx, uri, utf16PosNth(t, sql, "tmp_u", 1))
	require.NoError(t, err)
	require.NotNil(t, hTmp)
	assert.Contains(t, hTmp.Contents.Value, "tmp_u")

	hCTE, err := eng.Hover(ctx, uri, utf16PosNth(t, sql, "cte", 1))
	require.NoError(t, err)
	require.NotNil(t, hCTE)
	assert.Contains(t, hCTE.Contents.Value, "cte")

	hTags, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "tags"))
	require.NoError(t, err)
	require.NotNil(t, hTags)
	assert.Contains(t, strings.ToUpper(hTags.Contents.Value), "ARRAY")
	assert.Contains(t, strings.ToUpper(hTags.Contents.Value), "STRUCT")

	hFn, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "DATE_TRUNC"))
	require.NoError(t, err)
	require.NotNil(t, hFn)
	assert.Contains(t, strings.ToUpper(hFn.Contents.Value), "DATE_TRUNC")

	hPT, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "_PARTITIONTIME"))
	require.NoError(t, err)
	require.NotNil(t, hPT)
	assert.Contains(t, hPT.Contents.Value, "TIMESTAMP")

	hPD, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "_PARTITIONDATE"))
	require.NoError(t, err)
	require.NotNil(t, hPD)
	assert.Contains(t, hPD.Contents.Value, "DATE")

	hSuf, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "_TABLE_SUFFIX"))
	require.NoError(t, err)
	require.NotNil(t, hSuf)
	assert.Contains(t, hSuf.Contents.Value, "STRING")

	hVar, err := eng.Hover(ctx, uri, utf16PosNth(t, sql, "v_dt", 1))
	require.NoError(t, err)
	require.NotNil(t, hVar)
	assert.Contains(t, hVar.Contents.Value, "DATE")
}

func completionLabels(list *analysis.CompletionList) []string {
	out := make([]string, 0, len(list.Items))
	for _, it := range list.Items {
		out = append(out, it.Label)
	}

	return out
}

func TestCompletionContexts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, eng := newTestStoreAndEngine(t)

	// 1. Qualified alias + emoji UTF-16 + nested struct.
	sql1 := "SELECT '😀', u. FROM events u"
	eng.OpenDocument("file:///c1.sql", sql1, 1, nil)
	c1, err := eng.Complete(ctx, "file:///c1.sql", utf16PosAfter(t, sql1, "u."))
	require.NoError(t, err)
	labels1 := completionLabels(c1)
	assert.Contains(t, labels1, "event_date")
	assert.Contains(t, labels1, "tenant_id")
	assert.Contains(t, labels1, "user_ctx")
	assert.NotContains(t, labels1, "country")

	sql2 := "SELECT u.user_ctx. FROM events u"
	eng.OpenDocument("file:///c2.sql", sql2, 1, nil)
	c2, err := eng.Complete(ctx, "file:///c2.sql", utf16PosAfter(t, sql2, "u.user_ctx."))
	require.NoError(t, err)
	assert.Contains(t, completionLabels(c2), "user_id")
	assert.Contains(t, completionLabels(c2), "tags")

	// 2. Unclosed subquery parens.
	sql3 := "SELECT u. FROM (SELECT event_date, tenant_id FROM events u"
	eng.OpenDocument("file:///c3.sql", sql3, 1, nil)
	c3, err := eng.Complete(ctx, "file:///c3.sql", utf16PosAfter(t, sql3, "u."))
	require.NoError(t, err)
	assert.Contains(t, completionLabels(c3), "event_date")

	// 3. Broken SELECT + FROM CTEs.
	sql4 := "SELECT , tenant_id FROM events"
	eng.OpenDocument("file:///c4.sql", sql4, 1, nil)
	c4, err := eng.Complete(ctx, "file:///c4.sql", utf16PosAfter(t, sql4, "SELECT "))
	require.NoError(t, err)
	assert.Contains(t, completionLabels(c4), "event_date")

	sql5 := "WITH cte_orders AS (SELECT 1 AS order_id) SELECT order_id FROM "
	eng.OpenDocument("file:///c5.sql", sql5, 1, nil)
	c5, err := eng.Complete(ctx, "file:///c5.sql", utf16PosAfter(t, sql5, "FROM "))
	require.NoError(t, err)
	assert.Contains(t, completionLabels(c5), "cte_orders")

	// 4. Function positional units & Pipe |>.
	sql6 := "SELECT DATE_TRUNC(event_date, "
	eng.OpenDocument("file:///c6.sql", sql6, 1, nil)
	c6, err := eng.Complete(ctx, "file:///c6.sql", utf16PosAfter(t, sql6, "DATE_TRUNC(event_date, "))
	require.NoError(t, err)
	assert.Contains(t, completionLabels(c6), "DAY")
	assert.Contains(t, completionLabels(c6), "MONTH")

	sql7 := "FROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS extra_col |> "
	eng.OpenDocument("file:///c7.sql", sql7, 1, nil)
	c7, err := eng.Complete(ctx, "file:///c7.sql", utf16PosAfter(t, sql7, "|> EXTEND 1 AS extra_col |> "))
	require.NoError(t, err)
	l7 := completionLabels(c7)
	assert.Contains(t, l7, "extra_col")
	assert.Contains(t, l7, "event_date")
	assert.Contains(t, l7, "AGGREGATE")

	// 5. Jinja expr, loop., and {% raw %} suppression.
	sql8 := "{% macro my_mac() %}{% endmacro %}\n{% set my_var = 1 %}\nSELECT 1 FROM {{  }}"
	eng.OpenDocument("file:///c8.sql.j2", sql8, 1, nil)
	c8, err := eng.Complete(ctx, "file:///c8.sql.j2", utf16PosAfter(t, sql8, "{{ "))
	require.NoError(t, err)
	l8 := completionLabels(c8)
	for _, want := range []string{"ref", "source", "config", "var", "is_incremental", "this", "my_var", "my_mac"} {
		assert.Contains(t, l8, want)
	}

	sql9 := "{% for x in items %}{{ loop. }}{% endfor %}"
	eng.OpenDocument("file:///c9.sql.j2", sql9, 1, nil)
	c9, err := eng.Complete(ctx, "file:///c9.sql.j2", utf16PosAfter(t, sql9, "loop."))
	require.NoError(t, err)
	assert.Contains(t, completionLabels(c9), "index")

	sql10 := "{% raw %} SELECT {{ not_jinja }} {% endraw %}"
	eng.OpenDocument("file:///c10.sql.j2", sql10, 1, nil)
	c10, err := eng.Complete(ctx, "file:///c10.sql.j2", utf16PosAfter(t, sql10, "not_jinja"))
	require.NoError(t, err)
	assert.Empty(t, c10.Items)

	// No snippet syntax in InsertText.
	for _, item := range c7.Items {
		assert.NotContains(t, item.InsertText, "$1")
	}
}

func TestDefinitionAndDocumentSymbols(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, eng := newTestStoreAndEngine(t)

	// Shadowed alias + UTF-16 prefix + CTE + DECLARE + CREATE TEMP TABLE.
	sql := strings.Join([]string{
		"DECLARE v_dt DATE DEFAULT DATE '2026-01-01';",
		"CREATE TEMP TABLE tmp_tbl AS SELECT event_date FROM events WHERE event_date = v_dt;",
		"SELECT '😀'; WITH my_cte AS (SELECT u.country FROM users u WHERE EXISTS (SELECT 1 FROM events u WHERE u.event_date = '2026-01-01')) SELECT country FROM my_cte;",
		"SELECT event_date FROM tmp_tbl;",
	}, "\n")
	uri := "file:///nav.sql"
	eng.OpenDocument(uri, sql, 1, nil)

	// Shadowed inner alias `u` in `u.event_date`.
	locsAlias, err := eng.Definition(ctx, uri, utf16Pos(t, sql, "u.event_date"))
	require.NoError(t, err)
	require.NotEmpty(t, locsAlias)
	assert.Equal(t, utf16Pos(t, sql, "u WHERE u.event_date").Character, locsAlias[0].Range.Start.Character)

	// CTE definition with emoji UTF-16 prefix.
	locsCTE, err := eng.Definition(ctx, uri, utf16PosNth(t, sql, "my_cte", 1))
	require.NoError(t, err)
	require.NotEmpty(t, locsCTE)
	assert.Equal(t, utf16PosNth(t, sql, "my_cte", 0).Character, locsCTE[0].Range.Start.Character)

	// DECLARE variable.
	locsVar, err := eng.Definition(ctx, uri, utf16PosNth(t, sql, "v_dt", 1))
	require.NoError(t, err)
	require.NotEmpty(t, locsVar)
	assert.Equal(t, 0, locsVar[0].Range.Start.Line)

	// CREATE TEMP TABLE.
	locsTmp, err := eng.Definition(ctx, uri, utf16PosNth(t, sql, "tmp_tbl", 1))
	require.NoError(t, err)
	require.NotEmpty(t, locsTmp)
	assert.Equal(t, 1, locsTmp[0].Range.Start.Line)

	// External table returns non-nil empty slice.
	locsExt, err := eng.Definition(ctx, uri, utf16Pos(t, sql, "events WHERE"))
	require.NoError(t, err)
	require.NotNil(t, locsExt)
	assert.Empty(t, locsExt)

	// Jinja macro & set definitions.
	jSQL := "{% macro mac_a() %}1{% endmacro %}\n{% set var_a = 2 %}\nSELECT {{ mac_a() }}, {{ var_a }}"
	jURI := "file:///nav.sql.j2"
	eng.OpenDocument(jURI, jSQL, 1, nil)

	locsMac, err := eng.Definition(ctx, jURI, utf16PosNth(t, jSQL, "mac_a", 1))
	require.NoError(t, err)
	require.NotEmpty(t, locsMac)
	assert.Equal(t, 0, locsMac[0].Range.Start.Line)

	locsSet, err := eng.Definition(ctx, jURI, utf16PosNth(t, jSQL, "var_a", 1))
	require.NoError(t, err)
	require.NotEmpty(t, locsSet)
	assert.Equal(t, 1, locsSet[0].Range.Start.Line)

	// DocumentSymbols on valid + broken SQL + empty file.
	brokenSQL := "{% macro helper_macro() %}1{% endmacro %}\nCREATE TEMP TABLE good_tmp AS SELECT 1 AS id;\nSELECT ((( FROM WHERE;"
	bURI := "file:///syms_broken.sql.j2"
	eng.OpenDocument(bURI, brokenSQL, 1, nil)
	syms, err := eng.DocumentSymbols(ctx, bURI)
	require.NoError(t, err)
	raw, err := json.Marshal(syms)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "helper_macro")
	assert.Contains(t, string(raw), "good_tmp")

	emptyURI := "file:///empty.sql"
	eng.OpenDocument(emptyURI, "-- just a comment\n", 1, nil)
	emptySyms, err := eng.DocumentSymbols(ctx, emptyURI)
	require.NoError(t, err)
	require.NotNil(t, emptySyms)
	assert.Empty(t, emptySyms)
}

func TestCatalogInvalidationRefreshesDiagnosticsAndTokens(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, eng := newTestStoreAndEngine(t)

	// Start with an older schema lacking new_col so diagnostics report the missing column.
	store.PutTable(&catalog.TableMeta{
		Ref:  catalog.TableRef{Project: "my-proj", Dataset: "analytics", Table: "dynamic_tbl"},
		Type: catalog.TableTypeTable,
		Schema: []catalog.ColumnMeta{
			{Name: "old_col", Type: "STRING", Mode: catalog.ModeNullable},
		},
	})

	sql := "SELECT new_col FROM dynamic_tbl"
	uri := "file:///dynamic.sql"
	eng.OpenDocument(uri, sql, 1, nil)

	diags1, err := eng.Diagnostics(ctx, uri)
	require.NoError(t, err)
	require.NotEmpty(t, diags1)
	assert.Equal(t, "semantic", diags1[0].Code)

	store.PutTable(&catalog.TableMeta{
		Ref:  catalog.TableRef{Project: "my-proj", Dataset: "analytics", Table: "dynamic_tbl"},
		Type: catalog.TableTypeTable,
		TimePartitioning: &catalog.TimePartitioning{
			Type:  "DAY",
			Field: "new_col",
		},
		Schema: []catalog.ColumnMeta{
			{Name: "new_col", Type: "DATE", Mode: catalog.ModeRequired},
		},
	})
	eng.InvalidateCatalog(catalog.TableRef{Project: "my-proj", Dataset: "analytics", Table: "dynamic_tbl"})

	diags2, err := eng.Diagnostics(ctx, uri)
	require.NoError(t, err)
	require.Len(t, diags2, 1)
	assert.Equal(t, "missing_partition_filter", diags2[0].Code)

	res, err := eng.SemanticTokensFull(ctx, uri)
	require.NoError(t, err)
	tok, ok := findTokAt(decodeTokens(t, res.Data), utf16Pos(t, sql, "new_col"))
	require.True(t, ok)
	assert.NotZero(t, tok.mods&analysis.ModPartitioned)
}

func TestEngineConcurrentStress(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, eng := newTestStoreAndEngine(t)

	const workers = 6
	var wg sync.WaitGroup
	wg.Add(workers)

	for w := range workers {
		go func(id int) {
			defer wg.Done()

			uri := "file:///stress_" + string(rune('a'+id)) + ".sql"
			sql := "SELECT u.event_date, u.tenant_id FROM events u WHERE u.event_date = '2026-01-01'"
			eng.OpenDocument(uri, sql, 1, nil)

			for iter := range 10 {
				if iter%3 == 0 {
					eng.InvalidateCatalog(catalog.TableRef{})
				}

				_, err := eng.SemanticTokensFull(ctx, uri)
				assert.NoError(t, err)

				_, err = eng.Diagnostics(ctx, uri)
				assert.NoError(t, err)

				_, err = eng.Hover(ctx, uri, syntax.Position{Line: 0, Character: 9})
				assert.NoError(t, err)

				_, err = eng.Complete(ctx, uri, syntax.Position{Line: 0, Character: 9})
				assert.NoError(t, err)

				_, err = eng.Definition(ctx, uri, syntax.Position{Line: 0, Character: 7})
				assert.NoError(t, err)

				_, err = eng.DocumentSymbols(ctx, uri)
				assert.NoError(t, err)
			}

			eng.CloseDocument(uri)
		}(w)
	}

	wg.Wait()
}

func TestRegressionOperatorAndNamespaceTokensAndLeafIdent(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	ctx := context.Background()

	sql := "SELECT 1 + 2, SAFE.SUBSTR(u.tenant_id, 1, 2), NET.HOST('https://example.com'), AI.GENERATE('hi'), u.event_date, u.user_ctx.user_id FROM events u WHERE u.event_date = '2026-01-01'"
	uri := "file:///reg_tokens.sql"
	eng.OpenDocument(uri, sql, 1, nil)

	res, err := eng.SemanticTokensFull(ctx, uri)
	require.NoError(t, err)
	toks := decodeTokens(t, res.Data)

	assertTok := func(needle string, nth int, wantType analysis.SemanticTokenType, wantMods analysis.SemanticTokenModifier) {
		t.Helper()
		tk, ok := findTokAt(toks, utf16PosNth(t, sql, needle, nth))
		require.True(t, ok, "missing token %q #%d", needle, nth)
		assert.Equal(t, wantType, tk.tokType, "token %q #%d type", needle, nth)
		assert.Equal(t, wantMods, tk.mods, "token %q #%d mods", needle, nth)
	}

	assertTok("1 +", 0, analysis.TokenNumber, 0)
	assertTok("+", 0, analysis.TokenOperator, 0)
	assertTok("2,", 0, analysis.TokenNumber, 0)
	assertTok("SAFE", 0, analysis.TokenKeyword, 0)
	assertTok("SUBSTR", 0, analysis.TokenFunction, analysis.ModDefaultLibrary)
	assertTok("NET", 0, analysis.TokenNamespace, analysis.ModDefaultLibrary)
	assertTok("HOST", 0, analysis.TokenFunction, analysis.ModDefaultLibrary)
	assertTok("AI", 0, analysis.TokenNamespace, analysis.ModDefaultLibrary)
	assertTok("GENERATE", 0, analysis.TokenFunction, analysis.ModDefaultLibrary)
	assertTok("=", 0, analysis.TokenOperator, 0)
	assertTok("'2026-01-01'", 0, analysis.TokenString, 0)

	// Qualifier alias `u` must stay TokenVariable without ModPartitioned/ModClustered.
	assertTok("u.event_date", 0, analysis.TokenVariable, 0)
	assertTok("event_date", 0, analysis.TokenProperty, analysis.ModPartitioned)
	assertTok("u.event_date", 1, analysis.TokenVariable, 0)
	assertTok("event_date", 1, analysis.TokenProperty, analysis.ModPartitioned)
	assertTok("u.user_ctx.user_id", 0, analysis.TokenVariable, 0)
	assertTok("user_id", 0, analysis.TokenProperty, analysis.ModClustered)
}

func TestRegressionFiveLevelCTEPropagation(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	ctx := context.Background()

	sql := strings.Join([]string{
		"WITH c1 AS (SELECT event_date, tenant_id, user_ctx FROM events),",
		"c2 AS (SELECT event_date, tenant_id, user_ctx FROM c1),",
		"c3 AS (SELECT event_date, tenant_id, user_ctx FROM c2),",
		"c4 AS (SELECT event_date, tenant_id, user_ctx FROM c3),",
		"c5 AS (SELECT event_date, tenant_id, user_ctx FROM c4)",
		"SELECT tenant_id, user_ctx.user_id FROM c5 WHERE event_date = '2026-01-01' AND tenant_id = 't1'",
	}, "\n")
	uri := "file:///reg_cte5.sql"
	eng.OpenDocument(uri, sql, 1, nil)

	diags, err := eng.Diagnostics(ctx, uri)
	require.NoError(t, err)
	assert.Empty(t, diags)

	res, err := eng.SemanticTokensFull(ctx, uri)
	require.NoError(t, err)
	toks := decodeTokens(t, res.Data)

	tenTok, ok := findTokAt(toks, utf16PosNth(t, sql, "tenant_id", 5))
	require.True(t, ok)
	assert.NotZero(t, tenTok.mods&analysis.ModClustered)

	uidTok, ok := findTokAt(toks, utf16Pos(t, sql, "user_id"))
	require.True(t, ok)
	assert.NotZero(t, uidTok.mods&analysis.ModClustered)

	evTok, ok := findTokAt(toks, utf16PosNth(t, sql, "event_date", 5))
	require.True(t, ok)
	assert.NotZero(t, evTok.mods&analysis.ModPartitioned)
}

func TestRegressionCrossStatementSuppressionIsolation(t *testing.T) {
	t.Parallel()

	releaseCh := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-releaseCh
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	defer close(releaseCh)

	opts := bqconfig.DefaultLSPOptions()
	opts.DefaultProject = "my-proj"
	opts.DefaultDataset = "analytics"
	opts.BigQueryAPIEnabled = true
	opts.CacheDir = t.TempDir()

	rest := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithTokenProvider(func(context.Context) (string, error) { return "tok", nil }),
		catalog.WithTimeout(2*time.Second),
	)

	store, err := catalog.NewStore(opts, catalog.WithRestClient(rest))
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	seedTestTables(store)

	eng, err := analysis.NewEngine(store, opts)
	require.NoError(t, err)

	ctx := context.Background()
	sql := strings.Join([]string{
		"SELECT x FROM remote_proj.remote_ds.remote_tbl;",
		"SELECT y FROM remote_proj.remote_ds.remote_tbl;",
		"SELECT nonexistent_col FROM users;",
	}, "\n")
	uri := "file:///reg_multi_supp.sql"
	eng.OpenDocument(uri, sql, 1, nil)

	diags, err := eng.Diagnostics(ctx, uri)
	require.NoError(t, err)
	require.Len(t, diags, 1)
	assert.Equal(t, "semantic", diags[0].Code)
	assert.Equal(t, 2, diags[0].Range.Start.Line)
	assert.Contains(t, diags[0].Message, "nonexistent_col")
}

func TestRegressionTempTableStructArrayRangeAcrossEdits(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	ctx := context.Background()

	sql := strings.Join([]string{
		"CREATE TEMP TABLE tmp_complex AS SELECT STRUCT(1 AS a, [2, 3] AS arr) AS s, RANGE(DATE '2026-01-01', DATE '2026-01-02') AS rng;",
		"SELECT t.s.a, t.rng FROM tmp_complex t;",
	}, "\n")
	uri := "file:///reg_temp_struct.sql"
	eng.OpenDocument(uri, sql, 1, nil)

	diags1, err := eng.Diagnostics(ctx, uri)
	require.NoError(t, err)
	assert.Empty(t, diags1)

	// Trigger ChangeDocument hitting resolvedCache for statement 1 (replaying AddTableMeta).
	require.NoError(t, eng.ChangeDocument(uri, 2, []syntax.TextChange{{
		Range: &syntax.Range{Start: syntax.Position{Line: 1, Character: len("SELECT t.s.a, t.rng FROM tmp_complex t;")}, End: syntax.Position{Line: 1, Character: len("SELECT t.s.a, t.rng FROM tmp_complex t;")}},
		Text:  " -- ok",
	}}))

	diags2, err := eng.Diagnostics(ctx, uri)
	require.NoError(t, err)
	assert.Empty(t, diags2)

	hov, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "a, t.rng"))
	require.NoError(t, err)
	require.NotNil(t, hov)
	assert.Contains(t, hov.Contents.Value, "INT64")
}

func TestRegressionDefinitionExternalAndQualifiedColumn(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	ctx := context.Background()

	sql := strings.Join([]string{
		"DECLARE events INT64 DEFAULT 1;",
		"DECLARE user_id INT64 DEFAULT 2;",
		"CREATE TEMP TABLE tbl_x AS SELECT 1 AS id;",
		"SELECT u.user_id FROM my_proj.analytics.events u;",
		"SELECT t.id FROM other_proj.other_ds.tbl_x t;",
	}, "\n")
	uri := "file:///reg_def.sql"
	eng.OpenDocument(uri, sql, 1, nil)

	locsExt, err := eng.Definition(ctx, uri, utf16Pos(t, sql, "events u"))
	require.NoError(t, err)
	require.NotNil(t, locsExt)
	assert.Empty(t, locsExt)

	locsTempCollision, err := eng.Definition(ctx, uri, utf16Pos(t, sql, "tbl_x t"))
	require.NoError(t, err)
	require.NotNil(t, locsTempCollision)
	assert.Empty(t, locsTempCollision)

	locsCol, err := eng.Definition(ctx, uri, utf16Pos(t, sql, "user_id FROM"))
	require.NoError(t, err)
	require.NotNil(t, locsCol)
	assert.Empty(t, locsCol)

	locsAlias, err := eng.Definition(ctx, uri, utf16Pos(t, sql, "u.user_id"))
	require.NoError(t, err)
	require.Len(t, locsAlias, 1)
	assert.Equal(t, 3, locsAlias[0].Range.Start.Line)
}

func TestRegressionBeginEndMultiStatementHoverAndDiagnostics(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	ctx := context.Background()

	sqlHover := strings.Join([]string{
		"BEGIN",
		"  SELECT u.user_ctx.user_id, u.event_date FROM events u WHERE u.event_date = '2026-01-01';",
		"  SELECT 1 AS ok;",
		"END;",
	}, "\n")
	eng.OpenDocument("file:///reg_begin_hover.sql", sqlHover, 1, nil)

	hov, err := eng.Hover(ctx, "file:///reg_begin_hover.sql", utf16Pos(t, sqlHover, "user_id"))
	require.NoError(t, err)
	require.NotNil(t, hov)
	assert.Contains(t, hov.Contents.Value, "Numeric user ID")

	sqlDiags := strings.Join([]string{
		"BEGIN",
		"  SELECT bad_col FROM users;",
		"  SELECT tenant_id FROM events;",
		"END;",
	}, "\n")
	eng.OpenDocument("file:///reg_begin_diags.sql", sqlDiags, 1, nil)

	diags, err := eng.Diagnostics(ctx, "file:///reg_begin_diags.sql")
	require.NoError(t, err)
	require.Len(t, diags, 3)
	assert.Equal(t, "semantic", diags[0].Code)
	assert.Equal(t, "clustering_filter", diags[1].Code)
	assert.Equal(t, "require_partition_filter", diags[2].Code)
}

func TestRegressionJinjaRawEndBoundaryAndSQLBeforeTag(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	ctx := context.Background()

	rawSQL := "{% raw %}{{ is_{% endraw %}"
	eng.OpenDocument("file:///reg_raw_end.sql.j2", rawSQL, 1, nil)
	clRaw, err := eng.Complete(ctx, "file:///reg_raw_end.sql.j2", utf16PosAfter(t, rawSQL, "is_"))
	require.NoError(t, err)
	assert.Empty(t, clRaw.Items)

	tagSQL := "SELECT 1{% if true %}2{% endif %}"
	eng.OpenDocument("file:///reg_before_tag.sql.j2", tagSQL, 1, nil)
	clTag, err := eng.Complete(ctx, "file:///reg_before_tag.sql.j2", utf16PosAfter(t, tagSQL, "SELECT 1"))
	require.NoError(t, err)
	labels := completionLabels(clTag)
	assert.NotContains(t, labels, "is_incremental")
	assert.NotContains(t, labels, "ref")
}

func TestRegressionPipeDeclareAndAggregateIsolation(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	ctx := context.Background()

	sqlDecl := "DECLARE v_inc INT64 DEFAULT 1;\nFROM events |> EXTEND v_inc AS extra_col |> "
	eng.OpenDocument("file:///reg_pipe_decl.sql", sqlDecl, 1, nil)
	clDecl, err := eng.Complete(ctx, "file:///reg_pipe_decl.sql", utf16PosAfter(t, sqlDecl, "extra_col |> "))
	require.NoError(t, err)
	labelsDecl := completionLabels(clDecl)
	assert.Contains(t, labelsDecl, "extra_col")
	assert.Contains(t, labelsDecl, "event_date")

	sqlAgg := "FROM events |> AGGREGATE COUNT(*) AS cnt |> WHERE "
	eng.OpenDocument("file:///reg_pipe_agg.sql", sqlAgg, 1, nil)
	clAgg, err := eng.Complete(ctx, "file:///reg_pipe_agg.sql", utf16PosAfter(t, sqlAgg, "|> WHERE "))
	require.NoError(t, err)
	labelsAgg := completionLabels(clAgg)
	assert.Contains(t, labelsAgg, "cnt")
	assert.NotContains(t, labelsAgg, "event_date")
	assert.NotContains(t, labelsAgg, "tenant_id")
}

func TestRegressionHoverShadowedAliasUnnestAndQualFallback(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	ctx := context.Background()

	sqlShadow := "SELECT u.country FROM users u WHERE EXISTS (SELECT 1 FROM events u WHERE u.event_date = '2026-01-01')"
	eng.OpenDocument("file:///reg_shadow.sql", sqlShadow, 1, nil)

	hOuter, err := eng.Hover(ctx, "file:///reg_shadow.sql", utf16Pos(t, sqlShadow, "u.country"))
	require.NoError(t, err)
	require.NotNil(t, hOuter)
	assert.Contains(t, hOuter.Contents.Value, "my-proj.analytics.users")

	hInner, err := eng.Hover(ctx, "file:///reg_shadow.sql", utf16Pos(t, sqlShadow, "u.event_date"))
	require.NoError(t, err)
	require.NotNil(t, hInner)
	assert.Contains(t, hInner.Contents.Value, "my-proj.analytics.events")

	sqlUnnest := "SELECT item.key FROM events u, UNNEST(u.user_ctx.tags) AS item WHERE u.event_date = '2026-01-01'"
	eng.OpenDocument("file:///reg_unnest.sql", sqlUnnest, 1, nil)
	hUnnest, err := eng.Hover(ctx, "file:///reg_unnest.sql", utf16Pos(t, sqlUnnest, "item.key"))
	require.NoError(t, err)
	require.NotNil(t, hUnnest)
	assert.Contains(t, hUnnest.Contents.Value, "user_ctx.tags")

	sqlQual := "SELECT '😀', u.user_ctx. FROM events u"
	eng.OpenDocument("file:///reg_qual.sql", sqlQual, 1, nil)
	clQual, err := eng.Complete(ctx, "file:///reg_qual.sql", utf16PosAfter(t, sqlQual, "u."))
	require.NoError(t, err)
	labelsQual := completionLabels(clQual)
	assert.Contains(t, labelsQual, "event_date")
	assert.Contains(t, labelsQual, "tenant_id")
	assert.Contains(t, labelsQual, "user_ctx")
}

func TestDefinitionIncompleteAndEdgeCases(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, eng := newTestStoreAndEngine(t)

	// 1. Trailing dot + CTE in incomplete query (Stage 1 repaired AST) + unclosed paren variant.
	sql1 := "WITH daily AS (\n  SELECT event_date, COUNT(*) AS n FROM events WHERE event_date = '2026-01-01' GROUP BY 1\n)\nSELECT d. FROM daily d"
	eng.OpenDocument("file:///def_inc1.sql", sql1, 1, nil)

	locsD, err := eng.Definition(ctx, "file:///def_inc1.sql", utf16Pos(t, sql1, "d."))
	require.NoError(t, err)
	require.Len(t, locsD, 1)
	assert.Equal(t, utf16PosAfter(t, sql1, "FROM daily "), locsD[0].Range.Start)

	locsDaily, err := eng.Definition(ctx, "file:///def_inc1.sql", utf16PosNth(t, sql1, "daily", 1))
	require.NoError(t, err)
	require.Len(t, locsDaily, 1)
	assert.Equal(t, utf16PosNth(t, sql1, "daily", 0), locsDaily[0].Range.Start)

	sql1Paren := "WITH daily AS (SELECT event_date FROM events) SELECT (d. FROM daily d"
	eng.OpenDocument("file:///def_inc1_paren.sql", sql1Paren, 1, nil)
	locsParen, err := eng.Definition(ctx, "file:///def_inc1_paren.sql", utf16Pos(t, sql1Paren, "d."))
	require.NoError(t, err)
	require.Len(t, locsParen, 1)
	assert.Equal(t, utf16PosAfter(t, sql1Paren, "FROM daily "), locsParen[0].Range.Start)

	// 2. Chained and shadowed CTEs with broken WHERE (Stage 2 token fallback).
	sqlChained := "WITH c1 AS (SELECT event_date FROM events), c2 AS (SELECT c1. FROM c1 WHERE ) SELECT c2.event_date FROM c2"
	eng.OpenDocument("file:///def_chained.sql", sqlChained, 1, nil)

	locsC1Ref, err := eng.Definition(ctx, "file:///def_chained.sql", utf16Pos(t, sqlChained, "c1."))
	require.NoError(t, err)
	require.Len(t, locsC1Ref, 1)
	assert.Equal(t, utf16PosAfter(t, sqlChained, "c1. FROM "), locsC1Ref[0].Range.Start)

	locsC1CTE, err := eng.Definition(ctx, "file:///def_chained.sql", utf16PosNth(t, sqlChained, "c1", 2))
	require.NoError(t, err)
	require.Len(t, locsC1CTE, 1)
	assert.Equal(t, utf16PosNth(t, sqlChained, "c1", 0), locsC1CTE[0].Range.Start)

	locsC2CTE, err := eng.Definition(ctx, "file:///def_chained.sql", utf16PosNth(t, sqlChained, "c2", 2))
	require.NoError(t, err)
	require.Len(t, locsC2CTE, 1)
	assert.Equal(t, utf16PosNth(t, sqlChained, "c2", 0), locsC2CTE[0].Range.Start)

	sqlShadowCTE := "WITH c1 AS (SELECT 1 AS a) SELECT sub.b FROM (WITH c1 AS (SELECT 2 AS b) SELECT c1. FROM c1 WHERE ) sub"
	eng.OpenDocument("file:///def_shadow_cte.sql", sqlShadowCTE, 1, nil)

	locsInnerCTE, err := eng.Definition(ctx, "file:///def_shadow_cte.sql", utf16PosNth(t, sqlShadowCTE, "c1", 3))
	require.NoError(t, err)
	require.Len(t, locsInnerCTE, 1)
	assert.Equal(t, utf16PosNth(t, sqlShadowCTE, "c1", 1), locsInnerCTE[0].Range.Start)

	// Clicking on inner CTE declaration itself must not jump to outer CTE.
	locsInnerDecl, err := eng.Definition(ctx, "file:///def_shadow_cte.sql", utf16PosNth(t, sqlShadowCTE, "c1", 1))
	require.NoError(t, err)
	require.NotNil(t, locsInnerDecl)
	assert.Empty(t, locsInnerDecl)

	// Sibling subquery scope isolation: outer alias must not leak from inner subquery.
	sqlIso := "SELECT u. FROM (SELECT 1 AS x FROM events u) sub WHERE "
	eng.OpenDocument("file:///def_iso.sql", sqlIso, 1, nil)
	locsIso, err := eng.Definition(ctx, "file:///def_iso.sql", utf16Pos(t, sqlIso, "u."))
	require.NoError(t, err)
	require.NotNil(t, locsIso)
	assert.Empty(t, locsIso)

	// 3. Incomplete JOIN, subquery, comma-join, UNNEST, and EXTRACT aliases.
	sqlJoin := "SELECT e.event_date, u. FROM events e JOIN users u"
	eng.OpenDocument("file:///def_join.sql", sqlJoin, 1, nil)

	locsJoinU, err := eng.Definition(ctx, "file:///def_join.sql", utf16Pos(t, sqlJoin, "u."))
	require.NoError(t, err)
	require.Len(t, locsJoinU, 1)
	assert.Equal(t, utf16PosAfter(t, sqlJoin, "JOIN users "), locsJoinU[0].Range.Start)

	locsJoinE, err := eng.Definition(ctx, "file:///def_join.sql", utf16Pos(t, sqlJoin, "e.event_date"))
	require.NoError(t, err)
	require.Len(t, locsJoinE, 1)
	assert.Equal(t, utf16PosAfter(t, sqlJoin, "FROM events "), locsJoinE[0].Range.Start)

	sqlSub := "SELECT d. FROM (SELECT 1 AS a) d LEFT JOIN users u ON "
	eng.OpenDocument("file:///def_sub.sql", sqlSub, 1, nil)
	locsSubD, err := eng.Definition(ctx, "file:///def_sub.sql", utf16Pos(t, sqlSub, "d."))
	require.NoError(t, err)
	require.Len(t, locsSubD, 1)
	assert.Equal(t, utf16PosAfter(t, sqlSub, ") "), locsSubD[0].Range.Start)

	sqlComma := "SELECT u. FROM events e, users u WHERE "
	eng.OpenDocument("file:///def_comma.sql", sqlComma, 1, nil)
	locsCommaU, err := eng.Definition(ctx, "file:///def_comma.sql", utf16Pos(t, sqlComma, "u."))
	require.NoError(t, err)
	require.Len(t, locsCommaU, 1)
	assert.Equal(t, utf16PosAfter(t, sqlComma, ", users "), locsCommaU[0].Range.Start)

	sqlUnnest := "SELECT u. FROM UNNEST([1, 2]) AS u WHERE "
	eng.OpenDocument("file:///def_unnest.sql", sqlUnnest, 1, nil)
	locsUnnest, err := eng.Definition(ctx, "file:///def_unnest.sql", utf16Pos(t, sqlUnnest, "u."))
	require.NoError(t, err)
	require.Len(t, locsUnnest, 1)
	assert.Equal(t, utf16PosAfter(t, sqlUnnest, "AS "), locsUnnest[0].Range.Start)

	sqlExtract := "SELECT EXTRACT(YEAR FROM d.) FROM events d WHERE "
	eng.OpenDocument("file:///def_extract.sql", sqlExtract, 1, nil)
	locsExtract, err := eng.Definition(ctx, "file:///def_extract.sql", utf16Pos(t, sqlExtract, "d.)"))
	require.NoError(t, err)
	require.Len(t, locsExtract, 1)
	assert.Equal(t, utf16PosAfter(t, sqlExtract, "FROM events "), locsExtract[0].Range.Start)

	// 4. Standalone alias (`SELECT d FROM daily d`) + negative controls.
	sqlStandPos := "WITH daily AS (SELECT event_date FROM events) SELECT d FROM daily d"
	eng.OpenDocument("file:///def_stand_pos.sql", sqlStandPos, 1, nil)
	locsStandPos, err := eng.Definition(ctx, "file:///def_stand_pos.sql", utf16Pos(t, sqlStandPos, "d FROM"))
	require.NoError(t, err)
	require.Len(t, locsStandPos, 1)
	assert.Equal(t, utf16PosAfter(t, sqlStandPos, "FROM daily "), locsStandPos[0].Range.Start)

	sqlStandCol := "WITH daily AS (SELECT 1 AS x) SELECT x FROM daily d"
	eng.OpenDocument("file:///def_stand_col.sql", sqlStandCol, 1, nil)
	locsStandCol, err := eng.Definition(ctx, "file:///def_stand_col.sql", utf16Pos(t, sqlStandCol, "x FROM"))
	require.NoError(t, err)
	require.NotNil(t, locsStandCol)
	assert.Empty(t, locsStandCol)

	sqlStandUnaliasedCol := "WITH daily AS (SELECT 1 AS d) SELECT d FROM daily"
	eng.OpenDocument("file:///def_stand_unaliased_col.sql", sqlStandUnaliasedCol, 1, nil)
	locsStandUnaliasedCol, err := eng.Definition(ctx, "file:///def_stand_unaliased_col.sql", utf16Pos(t, sqlStandUnaliasedCol, "d FROM"))
	require.NoError(t, err)
	require.NotNil(t, locsStandUnaliasedCol)
	assert.Empty(t, locsStandUnaliasedCol)

	sqlUnaliased := "SELECT events FROM events"
	eng.OpenDocument("file:///def_unaliased.sql", sqlUnaliased, 1, nil)
	locsUnaliased, err := eng.Definition(ctx, "file:///def_unaliased.sql", utf16Pos(t, sqlUnaliased, "events FROM"))
	require.NoError(t, err)
	require.NotNil(t, locsUnaliased)
	assert.Empty(t, locsUnaliased)

	sqlStandBroken := "WITH daily AS (SELECT event_date FROM events) SELECT d FROM daily d WHERE "
	eng.OpenDocument("file:///def_stand_broken.sql", sqlStandBroken, 1, nil)
	locsStandBroken, err := eng.Definition(ctx, "file:///def_stand_broken.sql", utf16Pos(t, sqlStandBroken, "d FROM"))
	require.NoError(t, err)
	require.Len(t, locsStandBroken, 1)
	assert.Equal(t, utf16PosAfter(t, sqlStandBroken, "FROM daily "), locsStandBroken[0].Range.Start)

	sqlUnaliasedBroken := "SELECT events FROM events WHERE "
	eng.OpenDocument("file:///def_unaliased_broken.sql", sqlUnaliasedBroken, 1, nil)
	locsUnaliasedBroken, err := eng.Definition(ctx, "file:///def_unaliased_broken.sql", utf16Pos(t, sqlUnaliasedBroken, "events FROM"))
	require.NoError(t, err)
	require.NotNil(t, locsUnaliasedBroken)
	assert.Empty(t, locsUnaliasedBroken)

	// 5. Jinja {{ expr }} + UTF-16 surrogate pairs (`OffsetMap` accuracy).
	sqlJinjaUTF16 := "{% set x = 1 %}\nWITH daily AS (SELECT '😀' AS emo, {{ some_macro() }} AS m, event_date FROM events)\nSELECT '😀', d. FROM daily d WHERE "
	eng.OpenDocument("file:///def_jinja_utf16.sql.j2", sqlJinjaUTF16, 1, nil)

	locsJinjaD, err := eng.Definition(ctx, "file:///def_jinja_utf16.sql.j2", utf16Pos(t, sqlJinjaUTF16, "d."))
	require.NoError(t, err)
	require.Len(t, locsJinjaD, 1)
	assert.Equal(t, utf16PosAfter(t, sqlJinjaUTF16, "FROM daily "), locsJinjaD[0].Range.Start)

	locsJinjaDaily, err := eng.Definition(ctx, "file:///def_jinja_utf16.sql.j2", utf16PosNth(t, sqlJinjaUTF16, "daily", 1))
	require.NoError(t, err)
	require.Len(t, locsJinjaDaily, 1)
	assert.Equal(t, utf16PosNth(t, sqlJinjaUTF16, "daily", 0), locsJinjaDaily[0].Range.Start)

	// 6. External table & qualified column negative controls on incomplete SQL.
	sqlNeg := strings.Join([]string{
		"DECLARE events INT64 DEFAULT 1;",
		"DECLARE user_id INT64 DEFAULT 2;",
		"CREATE TEMP TABLE tbl_x AS SELECT 1 AS id;",
		"SELECT u.user_id FROM my_proj.analytics.events u WHERE ",
	}, "\n")
	eng.OpenDocument("file:///def_neg_incomplete.sql", sqlNeg, 1, nil)

	locsNegCol, err := eng.Definition(ctx, "file:///def_neg_incomplete.sql", utf16Pos(t, sqlNeg, "user_id FROM"))
	require.NoError(t, err)
	require.NotNil(t, locsNegCol)
	assert.Empty(t, locsNegCol)

	locsNegExtTbl, err := eng.Definition(ctx, "file:///def_neg_incomplete.sql", utf16Pos(t, sqlNeg, "events u"))
	require.NoError(t, err)
	require.NotNil(t, locsNegExtTbl)
	assert.Empty(t, locsNegExtTbl)

	locsNegProj, err := eng.Definition(ctx, "file:///def_neg_incomplete.sql", utf16Pos(t, sqlNeg, "my_proj"))
	require.NoError(t, err)
	require.NotNil(t, locsNegProj)
	assert.Empty(t, locsNegProj)

	locsNegAlias, err := eng.Definition(ctx, "file:///def_neg_incomplete.sql", utf16Pos(t, sqlNeg, "u.user_id"))
	require.NoError(t, err)
	require.Len(t, locsNegAlias, 1)
	assert.Equal(t, utf16PosAfter(t, sqlNeg, "my_proj.analytics.events "), locsNegAlias[0].Range.Start)

	sqlNegTemp := "CREATE TEMP TABLE tbl_x AS SELECT 1 AS id;\nSELECT t.id FROM other_proj.other_ds.tbl_x t WHERE "
	eng.OpenDocument("file:///def_neg_temp.sql", sqlNegTemp, 1, nil)
	locsNegTemp, err := eng.Definition(ctx, "file:///def_neg_temp.sql", utf16Pos(t, sqlNegTemp, "tbl_x t"))
	require.NoError(t, err)
	require.NotNil(t, locsNegTemp)
	assert.Empty(t, locsNegTemp)

	sqlNegUnqual := "DECLARE events INT64 DEFAULT 1;\nSELECT event_date FROM events WHERE "
	eng.OpenDocument("file:///def_neg_unqual.sql", sqlNegUnqual, 1, nil)
	locsNegUnqual, err := eng.Definition(ctx, "file:///def_neg_unqual.sql", utf16Pos(t, sqlNegUnqual, "events WHERE"))
	require.NoError(t, err)
	require.NotNil(t, locsNegUnqual)
	assert.Empty(t, locsNegUnqual)
}

func TestRegressionDefinitionAllVerifierCases(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, eng := newTestStoreAndEngine(t)

	assertDefAt := func(uri, sql, needle string, nth int, wantNeedle string, wantNth int) {
		t.Helper()
		eng.OpenDocument(uri, sql, 1, nil)
		locs, err := eng.Definition(ctx, uri, utf16PosNth(t, sql, needle, nth))
		require.NoError(t, err)
		require.Len(t, locs, 1, "uri=%s needle=%q#%d", uri, needle, nth)
		assert.Equal(t, utf16PosNth(t, sql, wantNeedle, wantNth), locs[0].Range.Start, "uri=%s needle=%q#%d", uri, needle, nth)
	}

	assertEmptyDefAt := func(uri, sql, needle string, nth int) {
		t.Helper()
		eng.OpenDocument(uri, sql, 1, nil)
		locs, err := eng.Definition(ctx, uri, utf16PosNth(t, sql, needle, nth))
		require.NoError(t, err)
		require.NotNil(t, locs, "uri=%s needle=%q#%d", uri, needle, nth)
		assert.Empty(t, locs, "uri=%s needle=%q#%d", uri, needle, nth)
	}

	// 1. ASTFunctionCall direct arguments (Stage 0 and Stage 1 repaired).
	assertDefAt("file:///fn_coal.sql", "WITH daily AS (SELECT 1 AS a) SELECT COALESCE(d., 1) FROM daily d", "d.,", 0, "d", 3)
	assertDefAt("file:///fn_cnt_dot.sql", "SELECT COUNT(d.) FROM events d WHERE d.event_date = '2026-01-01'", "d.)", 0, "d WHERE", 0)
	assertDefAt("file:///fn_cnt_col.sql", "SELECT COUNT(d.event_date) FROM events d WHERE d.event_date = '2026-01-01'", "d.event_date", 0, "d WHERE", 0)
	assertDefAt("file:///fn_date.sql", "SELECT DATE(d.event_date) FROM events d WHERE d.event_date = '2026-01-01'", "d.event_date", 0, "d WHERE", 0)
	assertDefAt("file:///fn_lower.sql", "SELECT LOWER(d.status) FROM events d WHERE d.event_date = '2026-01-01'", "d.status", 0, "d WHERE", 0)
	assertDefAt("file:///fn_tojson.sql", "SELECT TO_JSON(d) FROM events d WHERE d.event_date = '2026-01-01'", "d)", 0, "d WHERE", 0)
	assertDefAt("file:///fn_decl.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT COALESCE(x, 0) FROM events WHERE event_date = '2026-01-01';", "x, 0", 0, "x INT64", 0)
	assertEmptyDefAt("file:///fn_name_neg.sql", "DECLARE COUNT INT64 DEFAULT 1;\nSELECT COUNT(d.event_date) FROM events d WHERE d.event_date = '2026-01-01'", "COUNT(", 0)

	star := "*"

	// 2. SELECT d.* and d.* EXCEPT (...) when ResolvedStmt != nil.
	assertDefAt("file:///star_cte.sql", "WITH daily AS (SELECT 1 AS a) SELECT d."+star+" FROM daily d", "d.*", 0, "d", 3)
	assertDefAt("file:///star_tbl.sql", "SELECT d."+star+" FROM events d WHERE d.event_date = '2026-01-01'", "d.*", 0, "d WHERE", 0)
	assertDefAt("file:///star_except.sql", "SELECT d."+star+" EXCEPT (status) FROM events d WHERE d.event_date = '2026-01-01'", "d.*", 0, "d WHERE", 0)
	assertDefAt("file:///star_decl.sql", "DECLARE d STRUCT<a INT64> DEFAULT STRUCT(1 AS a);\nSELECT d."+star+" FROM events WHERE event_date = '2026-01-01'", "d.*", 0, "d STRUCT", 0)
	assertEmptyDefAt("file:///star_unaliased_neg.sql", "SELECT events."+star+" FROM events WHERE event_date = '2026-01-01'", "events.*", 0)

	// 3. BEGIN...END non-final statements using ResolvedStmts.
	assertDefAt("file:///begin_rv.sql", "BEGIN\n  SELECT d FROM events d WHERE d.event_date = '2026-01-01';\n  SELECT 1;\nEND;", "d FROM", 0, "d WHERE", 0)
	assertEmptyDefAt("file:///begin_col_neg.sql", "BEGIN\n  WITH daily AS (SELECT 1 AS x)\n  SELECT x FROM daily d;\n  SELECT 1;\nEND;", "x FROM", 0)
	assertEmptyDefAt("file:///begin_unaliased_col_neg.sql", "BEGIN\n  WITH daily AS (SELECT 1 AS d)\n  SELECT d FROM daily;\n  SELECT 1;\nEND;", "d FROM", 0)

	// 4. Range variable precedence over shadowing CTE / DECLARE.
	assertDefAt("file:///shadow_cte_rv.sql", "WITH d AS (SELECT 1 AS a) SELECT d FROM events d WHERE d.event_date = '2026-01-01'", "d FROM", 0, "d WHERE", 0)
	assertDefAt("file:///shadow_cte_star.sql", "WITH d AS (SELECT 1 AS a) SELECT d."+star+" FROM events d WHERE d.event_date = '2026-01-01'", "d.*", 0, "d WHERE", 0)
	assertDefAt("file:///shadow_decl_rv.sql", "DECLARE d INT64 DEFAULT 1;\nSELECT d FROM events d WHERE d.event_date = '2026-01-01'", "d FROM", 0, "d WHERE", 0)
	assertDefAt("file:///shadow_cte_stage2.sql", "WITH d AS (SELECT 1 AS a) SELECT d FROM events d WHERE ", "d FROM", 0, "d WHERE", 0)

	// 5. Pipe |> alias scoping.
	assertDefAt("file:///pipe_dot.sql", "FROM events d |> WHERE d.", "d.", 0, "d |>", 0)
	assertDefAt("file:///pipe_valid.sql", "FROM events d |> WHERE d.event_date = '2026-01-01'", "d.event_date", 0, "d |>", 0)
	assertDefAt("file:///pipe_extend.sql", "FROM events d |> EXTEND 1 AS extra |> WHERE d.event_date = '2026-01-01'", "d.event_date", 0, "d |>", 0)
	assertDefAt("file:///pipe_join.sql", "FROM events d |> JOIN users u ON d.user_id = u.user_id |> WHERE u.country = 'US'", "u.country", 0, "u ON", 0)
	assertDefAt("file:///pipe_join_lhs.sql", "FROM events d |> JOIN users u ON d.user_id = u.user_id |> WHERE u.country = 'US'", "d.user_id", 0, "d |>", 0)
	assertDefAt("file:///pipe_as.sql", "FROM events d |> AS ev |> WHERE ev.event_date = '2026-01-01'", "ev.event_date", 0, "ev |>", 0)
	assertDefAt("file:///pipe_select_prefix.sql", "SELECT "+star+" FROM events d |> WHERE d.event_date = '2026-01-01'", "d.event_date", 0, "d |>", 0)
	assertEmptyDefAt("file:///pipe_select_neg.sql", "FROM events d |> SELECT d.event_date |> WHERE d.", "d.", 1)

	// 6. Jinja {% elif %} / {% else %} secondary branches.
	sqlJinjaBranches := "WITH daily AS (SELECT 1 AS a)\n{% if x %}\nSELECT d. FROM daily d\n{% elif y %}\nSELECT m. FROM daily m\n{% else %}\nSELECT o. FROM daily o\n{% endif %}"
	assertDefAt("file:///jinja_branches.sql.j2", sqlJinjaBranches, "d.", 0, "d\n{% elif", 0)
	assertDefAt("file:///jinja_branches.sql.j2", sqlJinjaBranches, "m.", 0, "m\n{% else", 0)
	assertDefAt("file:///jinja_branches.sql.j2", sqlJinjaBranches, "o.", 0, "o\n{% endif", 0)

	sqlJinjaElseWhere := "WITH daily AS (SELECT 1 AS a) SELECT d.a FROM daily d {% if x %} WHERE d.a = 1 {% else %} WHERE d.a = 2 {% endif %}"
	assertDefAt("file:///jinja_else_where.sql.j2", sqlJinjaElseWhere, "d.a = 2", 0, "d {% if", 0)

	sqlJinjaElseDecl := "DECLARE v_x INT64 DEFAULT 1;\n{% if true %}SELECT v_x{% else %}SELECT v_x FROM events{% endif %}"
	assertDefAt("file:///jinja_else_decl.sql.j2", sqlJinjaElseDecl, "v_x FROM", 0, "v_x INT64", 0)
	assertEmptyDefAt("file:///jinja_else_neg.sql.j2", "{% if true %}SELECT 1{% else %}SELECT events FROM events{% endif %}", "events FROM", 0)

	// 7. Stage 2 mixed-backtick table paths.
	assertDefAt("file:///bt_mixed1.sql", "SELECT e. FROM `my-proj`.analytics.events e WHERE ", "e.", 0, "e WHERE", 0)
	assertDefAt("file:///bt_mixed2.sql", "SELECT e. FROM `my-proj`.analytics.`events` e WHERE ", "e.", 0, "e WHERE", 0)
	assertDefAt("file:///bt_mixed3.sql", "SELECT e. FROM `my-proj.analytics`.events e WHERE ", "e.", 0, "e WHERE", 0)
	assertEmptyDefAt("file:///bt_dangling_neg.sql", "SELECT e. FROM `my-proj`. WHERE ", "e.", 0)

	// 8. Stage 2 comma-joined FROM table positions vs DECLARE / TEMP TABLE / UNNEST.
	assertEmptyDefAt("file:///comma_decl_proj_neg.sql", "DECLARE other_proj INT64 DEFAULT 1;\nSELECT 1 FROM events e, other_proj.other_ds.tbl_x t WHERE ", "other_proj.", 0)
	assertEmptyDefAt("file:///comma_decl_tbl_neg.sql", "DECLARE events INT64 DEFAULT 1;\nSELECT 1 FROM users u, events WHERE ", "events WHERE", 0)
	assertDefAt("file:///comma_temp_pos.sql", "CREATE TEMP TABLE tbl_x AS SELECT 1 AS id;\nSELECT 1 FROM users u, tbl_x WHERE ", "tbl_x WHERE", 0, "tbl_x AS", 0)
	assertDefAt("file:///comma_unnest_arg.sql", "DECLARE e INT64 DEFAULT 1;\nSELECT 1 FROM events e, UNNEST(e.user_ctx.tags) AS tag WHERE ", "e.user_ctx", 0, "e, UNNEST", 0)

	// 9. Stage 2 array literal bracket balancing in JOIN ... ON.
	assertEmptyDefAt("file:///arr_lit_on_neg.sql", "SELECT c2. FROM t1 a JOIN t2 b ON b.arr = [c1, c2] WHERE ", "c2.", 0)
	assertDefAt("file:///arr_lit_on_comma.sql", "SELECT u. FROM t1 a JOIN t2 b ON b.arr = [c1, c2], users u WHERE ", "u.", 0, "u WHERE", 0)

	// 10. Unreserved keywords in CTE/table names and UNNEST WITH OFFSET AS off.
	assertDefAt("file:///kw_data_cte_alias.sql", "WITH data AS (SELECT 1 AS x) SELECT d. FROM data d WHERE ", "d.", 0, "d WHERE", 0)
	assertDefAt("file:///kw_data_cte_ref.sql", "WITH data AS (SELECT 1 AS x) SELECT d. FROM data d WHERE ", "data d", 0, "data AS", 0)
	assertDefAt("file:///kw_merge_target_src.sql", "MERGE target t USING source s ON t.id = s.id AND s.", "s.", 0, "s ON", 0)
	assertDefAt("file:///unnest_with_offset_u.sql", "SELECT u., off FROM UNNEST([1, 2]) AS u WITH OFFSET AS off WHERE ", "u.", 0, "u WITH", 0)
	assertDefAt("file:///unnest_with_offset_off.sql", "SELECT u., off FROM UNNEST([1, 2]) AS u WITH OFFSET AS off WHERE ", "off FROM", 0, "off WHERE", 0)
	assertDefAt("file:///unnest_comma_offset.sql", "SELECT tag, off FROM events u, UNNEST(u.user_ctx.tags) AS tag WITH OFFSET AS off WHERE ", "off FROM", 0, "off WHERE", 0)
}

func TestRegressionDefinitionChallenger2AndPipeBarriers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, eng := newTestStoreAndEngine(t)

	assertDefAt := func(uri, sql, needle string, nth int, wantNeedle string, wantNth int) {
		t.Helper()
		eng.OpenDocument(uri, sql, 1, nil)
		locs, err := eng.Definition(ctx, uri, utf16PosNth(t, sql, needle, nth))
		require.NoError(t, err)
		require.Len(t, locs, 1, "uri=%s needle=%q#%d", uri, needle, nth)
		assert.Equal(t, utf16PosNth(t, sql, wantNeedle, wantNth), locs[0].Range.Start, "uri=%s needle=%q#%d", uri, needle, nth)
	}

	assertEmptyDefAt := func(uri, sql, needle string, nth int) {
		t.Helper()
		eng.OpenDocument(uri, sql, 1, nil)
		locs, err := eng.Definition(ctx, uri, utf16PosNth(t, sql, needle, nth))
		require.NoError(t, err)
		require.NotNil(t, locs, "uri=%s needle=%q#%d", uri, needle, nth)
		assert.Empty(t, locs, "uri=%s needle=%q#%d", uri, needle, nth)
	}

	// 1. Stage 2 bare unreserved keyword aliases without AS + negative controls for OFFSET/PIVOT/RETURNING.
	assertDefAt("file:///bare_data.sql", "SELECT data. FROM my_tbl data WHERE ", "data.", 0, "data WHERE", 0)
	assertDefAt("file:///bare_source.sql", "SELECT source. FROM my_tbl source WHERE ", "source.", 0, "source WHERE", 0)
	assertDefAt("file:///bare_target.sql", "SELECT target. FROM my_tbl target WHERE ", "target.", 0, "target WHERE", 0)
	assertDefAt("file:///bare_key.sql", "SELECT key. FROM my_tbl key WHERE ", "key.", 0, "key WHERE", 0)
	assertDefAt("file:///bare_value.sql", "SELECT value. FROM my_tbl value WHERE ", "value.", 0, "value WHERE", 0)
	assertEmptyDefAt("file:///bare_offset_neg.sql", "SELECT offset. FROM UNNEST([1]) WITH OFFSET WHERE ", "offset.", 0)
	assertEmptyDefAt("file:///bare_limit_offset_neg.sql", "SELECT offset. FROM my_tbl LIMIT 10 OFFSET 5 WHERE ", "offset.", 0)
	assertEmptyDefAt("file:///bare_pivot_neg.sql", "SELECT pivot. FROM my_tbl PIVOT(SUM(x) FOR y IN ('a')) WHERE ", "pivot.", 0)
	assertEmptyDefAt("file:///bare_returning_neg.sql", "SELECT returning. FROM my_tbl RETURNING WHERE ", "returning.", 0)
	assertEmptyDefAt("file:///bare_delete_returning_neg.sql", "DELETE FROM my_tbl RETURNING returning. WHERE ", "returning.", 0)
	assertDefAt("file:///as_offset_pos.sql", "SELECT u., offset FROM UNNEST([1]) AS u WITH OFFSET AS offset WHERE ", "offset FROM", 0, "offset WHERE", 0)

	// 2. Stage 0 UNNEST(...) WITH OFFSET AS off + DECLARE shadow prevention on ResolvedColumnRef.
	assertDefAt("file:///unnest_off_stage0.sql", "SELECT off FROM UNNEST([1, 2]) AS u WITH OFFSET AS off", "off FROM", 0, "off", 1)
	assertDefAt("file:///unnest_off_stage0_no_as.sql", "SELECT off FROM UNNEST([1, 2]) WITH OFFSET off", "off FROM", 0, "off", 1)
	assertDefAt("file:///unnest_off_stage0_decl_shadow.sql", "DECLARE off INT64 DEFAULT 1;\nSELECT off FROM UNNEST([1, 2]) AS u WITH OFFSET AS off", "off FROM", 0, "off", 2)
	assertDefAt("file:///unnest_off_pipe_stage0.sql", "FROM UNNEST([1, 2]) AS u WITH OFFSET AS off |> WHERE off > 0", "off >", 0, "off |>", 0)
	assertEmptyDefAt("file:///unnest_off_qual_neg.sql", "SELECT off. FROM UNNEST([1, 2]) AS u WITH OFFSET AS off", "off.", 0)
	assertEmptyDefAt("file:///stage0_decl_shadow_subq_col.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT x FROM (SELECT 1 AS x)", "x FROM", 0)
	assertEmptyDefAt("file:///stage0_decl_shadow_tbl_col.sql", "DECLARE event_date DATE DEFAULT '2026-01-01';\nSELECT event_date FROM events WHERE event_date = '2026-01-01'", "event_date FROM", 0)
	assertDefAt("file:///stage0_decl_var_pos.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT COALESCE(x, 0) FROM events WHERE event_date = '2026-01-01'", "x, 0", 0, "x INT64", 0)

	// 3. Stage 0 & Stage 2 DML targets with shadowing DECLARE.
	assertEmptyDefAt("file:///dml_s0_insert_into.sql", "DECLARE events INT64 DEFAULT 1;\nINSERT INTO events VALUES ('2026-01-01', 't', 'ok', STRUCT(1, ['a']))", "events VALUES", 0)
	assertEmptyDefAt("file:///dml_s0_insert_no_into.sql", "DECLARE events INT64 DEFAULT 1;\nINSERT events (tenant_id) VALUES ('t')", "events (", 0)
	assertEmptyDefAt("file:///dml_s0_insert_col_list.sql", "DECLARE tenant_id INT64 DEFAULT 1;\nINSERT INTO events (tenant_id) VALUES ('t')", "tenant_id)", 0)
	assertEmptyDefAt("file:///dml_s0_update.sql", "DECLARE events INT64 DEFAULT 1;\nUPDATE events SET tenant_id = 'x' WHERE event_date = '2026-01-01'", "events SET", 0)
	assertEmptyDefAt("file:///dml_s0_delete_from.sql", "DECLARE events INT64 DEFAULT 1;\nDELETE FROM events WHERE event_date = '2026-01-01'", "events WHERE", 0)
	assertEmptyDefAt("file:///dml_s0_delete_no_from.sql", "DECLARE events INT64 DEFAULT 1;\nDELETE events WHERE event_date = '2026-01-01'", "events WHERE", 0)
	assertEmptyDefAt("file:///dml_s0_merge.sql", "DECLARE events INT64 DEFAULT 1;\nMERGE INTO events t USING users s ON t.tenant_id = s.user_id WHEN MATCHED THEN DELETE", "events t", 0)
	assertEmptyDefAt("file:///dml_s0_truncate.sql", "DECLARE events INT64 DEFAULT 1;\nTRUNCATE TABLE events", "events", 1)

	assertEmptyDefAt("file:///dml_s2_insert_into.sql", "DECLARE events INT64 DEFAULT 1;\nINSERT INTO events (a) VALUES (", "events (", 0)
	assertEmptyDefAt("file:///dml_s2_insert_no_into.sql", "DECLARE events INT64 DEFAULT 1;\nINSERT events (a) VALUES (", "events (", 0)
	assertEmptyDefAt("file:///dml_s2_insert_ignore.sql", "DECLARE events INT64 DEFAULT 1;\nINSERT IGNORE events (a) VALUES (", "events (", 0)
	assertEmptyDefAt("file:///dml_s2_update.sql", "DECLARE events INT64 DEFAULT 1;\nUPDATE events SET a = (", "events SET", 0)
	assertEmptyDefAt("file:///dml_s2_delete.sql", "DECLARE events INT64 DEFAULT 1;\nDELETE events WHERE (", "events WHERE", 0)
	assertEmptyDefAt("file:///dml_s2_merge.sql", "DECLARE events INT64 DEFAULT 1;\nMERGE events USING users u ON (", "events USING", 0)
	assertDefAt("file:///dml_s2_insert_val_decl.sql", "DECLARE events INT64 DEFAULT 1;\nINSERT INTO other_tbl (a) VALUES (events", "events", 1, "events INT64", 0)
	assertDefAt("file:///dml_s2_insert_temp.sql", "CREATE TEMP TABLE tmp_ins AS SELECT 1 AS a;\nINSERT tmp_ins (a) VALUES (", "tmp_ins (", 0, "tmp_ins AS", 0)

	// 4. Pipe |> barriers (ASTPipeAs, ASTPipeSetOperation, ASTPipePivot, ASTPipeSet non-barrier).
	star := "*"
	assertEmptyDefAt("file:///pipe_as_barrier_neg.sql", "FROM events d |> AS ev |> WHERE d.event_date = '2026-01-01'", "d.event_date", 0)
	assertEmptyDefAt("file:///pipe_stmt_as_barrier_neg.sql", "SELECT "+star+" FROM events d |> AS ev |> WHERE d.event_date = '2026-01-01'", "d.event_date", 0)
	assertEmptyDefAt("file:///pipe_union_barrier_neg.sql", "FROM events d |> UNION ALL (FROM users u) |> WHERE d.event_date = '2026-11-26'", "d.event_date", 0)
	assertEmptyDefAt("file:///pipe_pivot_barrier_neg.sql", "FROM events d |> PIVOT(COUNT(*) FOR status IN ('ok')) |> WHERE d.event_date = '2026-01-01'", "d.event_date", 0)
	assertDefAt("file:///pipe_set_preserves_alias.sql", "FROM events d |> SET status = 'ok' |> WHERE d.event_date = '2026-01-01'", "d.event_date", 0, "d |>", 0)
}

func assertDefLocation(t *testing.T, eng *analysis.Engine, uri, sql, needle string, nth int, wantNeedle string, wantNth int) {
	t.Helper()
	eng.OpenDocument(uri, sql, 1, nil)
	locs, err := eng.Definition(context.Background(), uri, utf16PosNth(t, sql, needle, nth))
	require.NoError(t, err)
	require.Len(t, locs, 1, "uri=%s needle=%q#%d", uri, needle, nth)
	assert.Equal(t, utf16PosNth(t, sql, wantNeedle, wantNth), locs[0].Range.Start, "uri=%s needle=%q#%d", uri, needle, nth)
}

func assertEmptyDefLocation(t *testing.T, eng *analysis.Engine, uri, sql, needle string, nth int) {
	t.Helper()
	eng.OpenDocument(uri, sql, 1, nil)
	locs, err := eng.Definition(context.Background(), uri, utf16PosNth(t, sql, needle, nth))
	require.NoError(t, err)
	require.NotNil(t, locs, "uri=%s needle=%q#%d", uri, needle, nth)
	assert.Empty(t, locs, "uri=%s needle=%q#%d", uri, needle, nth)
}

func TestRegressionDefinitionIter4Stage0(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	star := "*"

	// 1. Parent-aware ASTIdentifierList & direct column-name lists.
	assertDefLocation(t, eng, "file:///i4_set_struct_x.sql", "DECLARE x INT64 DEFAULT 1; DECLARE y INT64 DEFAULT 2; SET (x, y) = (1, 2);", "x, y)", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i4_set_struct_y.sql", "DECLARE x INT64 DEFAULT 1; DECLARE y INT64 DEFAULT 2; SET (x, y) = (1, 2);", "y) =", 0, "y INT64", 0)
	assertDefLocation(t, eng, "file:///i4_exec_into.sql", "DECLARE x INT64; EXECUTE IMMEDIATE 'SELECT 1' INTO x;", "x;", 0, "x INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_pipe_drop_neg.sql", "DECLARE x INT64 DEFAULT 1;\nFROM (SELECT 1 AS x) |> DROP x", "x", 2)
	assertEmptyDefLocation(t, eng, "file:///i4_decl_site_neg.sql", "DECLARE x INT64 DEFAULT 1;", "x ", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_except_neg.sql", "DECLARE status STRING DEFAULT 'ok';\nSELECT "+star+" EXCEPT (status) FROM events WHERE event_date = '2026-01-01'", "status)", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_pipe_rename_neg.sql", "DECLARE a INT64 DEFAULT 1; DECLARE b INT64 DEFAULT 2;\nFROM (SELECT 1 AS a) |> RENAME a AS b", "a AS b", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_pipe_rename_new_neg.sql", "DECLARE a INT64 DEFAULT 1; DECLARE b INT64 DEFAULT 2;\nFROM (SELECT 1 AS a) |> RENAME a AS b", "b", 1)

	// 2. ResolvedColumnRef / ArrayOffsetColumn before WITH OFFSET & CTE lookup.
	// In GoogleSQL (name_scope.cc:246-261), implicit range variable `x` overrides column `x` in `FROM x`,
	// producing ResolvedMakeStruct (range var -> WITH x), whereas `FROM x y` or `(SELECT 1 AS x)` leaves `x` as ResolvedColumnRef ([]).
	assertDefLocation(t, eng, "file:///i4_cte_col_same_rv.sql", "WITH x AS (SELECT 1 AS x) SELECT x FROM x", "x FROM", 0, "x AS", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_cte_col_aliased.sql", "WITH x AS (SELECT 1 AS x) SELECT x FROM x y", "x FROM", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_cte_col_subq.sql", "WITH x AS (SELECT 1 AS a) SELECT x FROM (SELECT 1 AS x)", "x FROM", 0)
	assertDefLocation(t, eng, "file:///i4_cte_star_pos.sql", "WITH x AS (SELECT 1 AS a) SELECT x."+star+" FROM x", "x.*", 0, "x AS", 0)
	assertDefLocation(t, eng, "file:///i4_cte_tojson_pos.sql", "WITH x AS (SELECT 1 AS a) SELECT TO_JSON(x) FROM x", "x) FROM", 0, "x AS", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_off_inner_shadow.sql", "SELECT (SELECT off FROM (SELECT 1 AS off)) FROM UNNEST([1, 2]) AS u WITH OFFSET AS off", "off FROM", 0)
	assertDefLocation(t, eng, "file:///i4_off_correlated.sql", "SELECT (SELECT off) FROM UNNEST([1, 2]) AS u WITH OFFSET AS off", "off)", 0, "off", 1)

	// 3. Subquery & ON boundaries in FROM / WITH OFFSET walks.
	assertEmptyDefLocation(t, eng, "file:///i4_on_exists_off.sql", "SELECT off FROM events e JOIN users u ON EXISTS (SELECT 1 FROM UNNEST([1]) WITH OFFSET AS off) WHERE e.event_date = '2026-01-01'", "off FROM", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_unnest_arr_off.sql", "SELECT off FROM UNNEST(ARRAY(SELECT off FROM UNNEST([1]) WITH OFFSET AS off)) AS item", "off FROM", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_on_exists_alias.sql", "SELECT x.id FROM events e JOIN users u ON EXISTS (SELECT 1 FROM users x WHERE x.user_id = u.user_id) WHERE e.event_date = '2026-01-01'", "x.id", 0)

	// 4. ASTTVF, ASTPivotClause, ASTUnpivotClause, ASTMatchRecognizeClause, ASTSampleClause, ASTGraphTableQuery, Pipe MATCH_RECOGNIZE.
	assertDefLocation(t, eng, "file:///i4_tvf_as.sql", "SELECT x.a FROM my_tvf(1, 2) AS x", "x.a", 0, "x", 1)
	assertDefLocation(t, eng, "file:///i4_tvf_bare.sql", "SELECT x.a FROM my_tvf(1, 2) x", "x.a", 0, "x", 1)
	assertDefLocation(t, eng, "file:///i4_tvf_star.sql", "SELECT x."+star+" FROM my_tvf(1, 2) AS x", "x.*", 0, "x", 1)
	assertEmptyDefLocation(t, eng, "file:///i4_tvf_unaliased_neg.sql", "SELECT my_tvf.a FROM my_tvf(1, 2)", "my_tvf.a", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_tvf_arg_leak_neg.sql", "SELECT u.id FROM outer_tvf((SELECT "+star+" FROM users u)) AS x", "u.id", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_tvf_name_decl_neg.sql", "DECLARE my_tvf INT64 DEFAULT 1;\nSELECT x.a FROM my_tvf(1, 2) AS x", "my_tvf(1", 0)

	assertDefLocation(t, eng, "file:///i4_pivot_bare.sql", "SELECT p.a FROM events PIVOT(COUNT("+star+") FOR status IN ('ok')) p", "p.a", 0, "p", 1)
	assertEmptyDefLocation(t, eng, "file:///i4_pivot_shadows_base.sql", "SELECT e.a FROM events e PIVOT(COUNT("+star+") FOR status IN ('ok')) p", "e.a", 0)
	assertDefLocation(t, eng, "file:///i4_pivot_inside_base.sql", "SELECT p.a FROM events e PIVOT(SUM(e.x) FOR e.status IN ('ok')) p", "e.x", 0, "e PIVOT", 0)
	assertDefLocation(t, eng, "file:///i4_pivot_subq.sql", "SELECT p.a FROM (SELECT "+star+" FROM events) AS sub PIVOT(COUNT("+star+") FOR status IN ('ok')) p", "p.a", 0, "p", 1)
	assertEmptyDefLocation(t, eng, "file:///i4_pivot_subq_shadow.sql", "SELECT sub.a FROM (SELECT "+star+" FROM events) AS sub PIVOT(COUNT("+star+") FOR status IN ('ok')) p", "sub.a", 0)
	assertDefLocation(t, eng, "file:///i4_pivot_tvf.sql", "SELECT p.a FROM my_tvf(1) AS t PIVOT(COUNT("+star+") FOR status IN ('ok')) p", "p.a", 0, "p", 1)
	assertEmptyDefLocation(t, eng, "file:///i4_pivot_off_shadow.sql", "SELECT off FROM UNNEST([1]) AS u WITH OFFSET AS off PIVOT(COUNT("+star+") FOR u IN (1)) p", "off FROM", 0)

	assertDefLocation(t, eng, "file:///i4_unpivot_bare.sql", "SELECT u.val FROM events UNPIVOT(val FOR c IN (status)) u", "u.val", 0, "u", 2)
	assertEmptyDefLocation(t, eng, "file:///i4_unpivot_shadow.sql", "SELECT e.val FROM events e UNPIVOT(val FOR c IN (status)) u", "e.val", 0)
	assertDefLocation(t, eng, "file:///i4_mr_from.sql", "SELECT m.x FROM events e MATCH_RECOGNIZE(ORDER BY event_date MEASURES A.status AS x PATTERN (A) DEFINE A AS TRUE) m", "m.x", 0, "m", 1)
	assertEmptyDefLocation(t, eng, "file:///i4_mr_from_shadow.sql", "SELECT e.x FROM events e MATCH_RECOGNIZE(ORDER BY event_date MEASURES A.status AS x PATTERN (A) DEFINE A AS TRUE) m", "e.x", 0)
	assertDefLocation(t, eng, "file:///i4_pipe_mr_alias.sql", "FROM events d |> MATCH_RECOGNIZE(ORDER BY event_date MEASURES A.status AS x PATTERN (A) DEFINE A AS TRUE) AS mr |> WHERE mr.x = 'ok'", "mr.x", 0, "mr |>", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_pipe_mr_shadow.sql", "FROM events d |> MATCH_RECOGNIZE(ORDER BY event_date MEASURES A.status AS x PATTERN (A) DEFINE A AS TRUE) AS mr |> WHERE d.event_date = '2026-01-01'", "d.event_date", 0)
	assertDefLocation(t, eng, "file:///i4_pipe_pivot_in_op.sql", "FROM events d |> PIVOT(SUM(d.x) FOR status IN ('ok')) AS d |> WHERE d.x = 1", "d.x)", 0, "d |>", 0)

	assertDefLocation(t, eng, "file:///i4_sample_preserves.sql", "SELECT e.event_date FROM events e TABLESAMPLE SYSTEM (10 PERCENT) WHERE e.event_date = '2026-01-01'", "e.event_date FROM", 0, "e TABLESAMPLE", 0)
	assertDefLocation(t, eng, "file:///i4_pivot_sample.sql", "SELECT p.a FROM events e PIVOT(COUNT("+star+") FOR status IN ('ok')) p TABLESAMPLE SYSTEM (10 PERCENT)", "p.a", 0, "p TABLESAMPLE", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_pivot_sample_shadow.sql", "SELECT e.a FROM events e PIVOT(COUNT("+star+") FOR status IN ('ok')) p TABLESAMPLE SYSTEM (10 PERCENT)", "e.a", 0)
	assertDefLocation(t, eng, "file:///i4_graph_table.sql", "SELECT g.a FROM GRAPH_TABLE(my_graph MATCH (n) COLUMNS (n.id AS a)) g", "g.a", 0, "g", 2)

	// 5. Stage 0 ASTUpdateSetValue.Path() targets vs RHS & array index.
	assertEmptyDefLocation(t, eng, "file:///i4_s0_upd_struct.sql", "DECLARE user_ctx INT64 DEFAULT 1;\nUPDATE events SET user_ctx.id = 'a' WHERE TRUE", "user_ctx.id", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s0_upd_unknown.sql", "DECLARE tenant_id STRING DEFAULT 'z';\nUPDATE unknown_tbl SET tenant_id = 'x' WHERE TRUE", "tenant_id =", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s0_merge_unknown.sql", "DECLARE tenant_id STRING DEFAULT 'z';\nMERGE INTO unknown_tbl t USING users s ON t.id = s.user_id WHEN MATCHED THEN UPDATE SET tenant_id = s.user_id", "tenant_id =", 0)
	assertDefLocation(t, eng, "file:///i4_s0_upd_alias_lhs.sql", "UPDATE events e SET e.tenant_id = 'a' WHERE TRUE", "e.tenant_id", 0, "e SET", 0)
	assertDefLocation(t, eng, "file:///i4_s0_upd_unaliased_lhs.sql", "DECLARE events INT64 DEFAULT 1;\nUPDATE events SET events.tenant_id = 'a' WHERE TRUE", "events.tenant_id", 0, "events SET", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s0_upd_arr_lhs.sql", "DECLARE arr INT64 DEFAULT 1; DECLARE idx INT64 DEFAULT 0; DECLARE val INT64 DEFAULT 2;\nUPDATE unknown_tbl SET arr[idx].field = val WHERE TRUE", "arr[", 0)
	assertDefLocation(t, eng, "file:///i4_s0_upd_arr_idx.sql", "DECLARE arr INT64 DEFAULT 1; DECLARE idx INT64 DEFAULT 0; DECLARE val INT64 DEFAULT 2;\nUPDATE unknown_tbl SET arr[idx].field = val WHERE TRUE", "idx]", 0, "idx INT64", 0)
	assertDefLocation(t, eng, "file:///i4_s0_upd_rhs.sql", "DECLARE arr INT64 DEFAULT 1; DECLARE idx INT64 DEFAULT 0; DECLARE val INT64 DEFAULT 2;\nUPDATE unknown_tbl SET arr[idx].field = val WHERE TRUE", "val WHERE", 0, "val INT64", 0)
}

func TestRegressionDefinitionIter4Stage2(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	star := "*"

	// 1. Stage 2 INSERT modes & column lists (positive + negative controls).
	assertEmptyDefLocation(t, eng, "file:///i4_s2_ins_replace.sql", "DECLARE events INT64 DEFAULT 1;\nINSERT REPLACE events (a) VALUES (", "events (", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s2_ins_update.sql", "DECLARE events INT64 DEFAULT 1;\nINSERT UPDATE events (a) VALUES (", "events (", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s2_ins_or_upd.sql", "DECLARE events INT64 DEFAULT 1;\nINSERT OR UPDATE events (a) VALUES (", "events (", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s2_ins_col.sql", "DECLARE tenant_id STRING DEFAULT 'z';\nINSERT INTO events (tenant_id) VALUES (", "tenant_id)", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s2_ins_unclosed.sql", "DECLARE tenant_id STRING DEFAULT 'z';\nINSERT events (tenant_id, ", "tenant_id,", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s2_merge_ins_col.sql", "DECLARE tenant_id STRING DEFAULT 'z';\nMERGE INTO events t USING users s ON t.id = s.user_id WHEN NOT MATCHED THEN INSERT (tenant_id) VALUES (", "tenant_id)", 0)
	assertDefLocation(t, eng, "file:///i4_s2_ins_val_rhs.sql", "DECLARE x INT64 DEFAULT 1;\nINSERT INTO events (col1) VALUES (x", "x", 1, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i4_s2_ins_subq_src.sql", "DECLARE x INT64 DEFAULT 1;\nINSERT INTO events (SELECT x FROM (", "x FROM", 0, "x INT64", 0)

	// 2. Stage 2 UPDATE / MERGE SET LHS targets vs RHS & CASE/array indexing.
	assertEmptyDefLocation(t, eng, "file:///i4_s2_upd_lhs.sql", "DECLARE tenant_id STRING DEFAULT 'z';\nUPDATE events SET tenant_id = (", "tenant_id =", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s2_upd_lhs2.sql", "DECLARE tenant_id STRING DEFAULT 'z';\nUPDATE events SET a = 1, tenant_id = (", "tenant_id =", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s2_upd_struct.sql", "DECLARE user_ctx INT64 DEFAULT 1;\nUPDATE events SET user_ctx.id = (", "user_ctx.", 0)
	assertDefLocation(t, eng, "file:///i4_s2_upd_alias.sql", "UPDATE events e SET e.tenant_id = (", "e.tenant_id", 0, "e SET", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s2_merge_upd_lhs.sql", "DECLARE tenant_id STRING DEFAULT 'z';\nMERGE INTO events t USING users s ON t.id = s.user_id WHEN MATCHED THEN UPDATE SET tenant_id = (", "tenant_id =", 0)
	assertDefLocation(t, eng, "file:///i4_s2_upd_case_rhs.sql", "DECLARE x INT64 DEFAULT 1;\nUPDATE events SET a = CASE WHEN x = 1 THEN 2 ELSE 3 END, b = x + (", "x + (", 0, "x INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s2_upd_case_lhs2.sql", "DECLARE b INT64 DEFAULT 1;\nUPDATE events SET a = CASE WHEN x = 1 THEN 2 ELSE 3 END, b = x (", "b =", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s2_upd_arr_lhs.sql", "DECLARE arr INT64 DEFAULT 1; DECLARE idx INT64 DEFAULT 0;\nUPDATE events SET arr[idx].field = (", "arr[", 0)
	assertDefLocation(t, eng, "file:///i4_s2_upd_arr_idx.sql", "DECLARE arr INT64 DEFAULT 1; DECLARE idx INT64 DEFAULT 0;\nUPDATE events SET arr[idx].field = (", "idx]", 0, "idx INT64", 0)

	// 3. Stage 2 TVF, PIVOT, UNPIVOT, MATCH_RECOGNIZE, TABLESAMPLE, GRAPH_TABLE, implicit UNNEST + WITH OFFSET.
	assertDefLocation(t, eng, "file:///i4_s2_tvf_as.sql", "SELECT x. FROM my_tvf(1, 2) AS x WHERE ", "x.", 0, "x WHERE", 0)
	assertDefLocation(t, eng, "file:///i4_s2_tvf_bare.sql", "SELECT x. FROM my_tvf(1, 2) x WHERE ", "x.", 0, "x WHERE", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s2_tvf_unaliased_neg.sql", "SELECT my_tvf. FROM my_tvf(1, 2) WHERE ", "my_tvf.", 0)
	assertDefLocation(t, eng, "file:///i4_s2_pivot_pos.sql", "SELECT p. FROM events PIVOT(COUNT("+star+") FOR col1 IN (1)) p WHERE ", "p.", 0, "p WHERE", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s2_pivot_shadow.sql", "SELECT e. FROM events e PIVOT(COUNT("+star+") FOR col1 IN (1)) p WHERE ", "e.", 0)
	assertDefLocation(t, eng, "file:///i4_s2_unpivot_pos.sql", "SELECT u. FROM events UNPIVOT(val FOR c IN (col1)) u WHERE ", "u.", 0, "u WHERE", 0)
	assertDefLocation(t, eng, "file:///i4_s2_mr_pos.sql", "SELECT m. FROM events MATCH_RECOGNIZE(ORDER BY ts MEASURES A.x AS x PATTERN (A) DEFINE A AS TRUE) m WHERE ", "m.", 0, "m WHERE", 0)
	assertDefLocation(t, eng, "file:///i4_s2_sample.sql", "SELECT e. FROM events e TABLESAMPLE SYSTEM (10 PERCENT) WHERE ", "e.", 0, "e TABLESAMPLE", 0)
	assertDefLocation(t, eng, "file:///i4_s2_graph.sql", "SELECT g. FROM GRAPH_TABLE(my_graph MATCH (n) COLUMNS (n.id AS a)) g WHERE ", "g.", 0, "g WHERE", 0)
	assertDefLocation(t, eng, "file:///i4_s2_impl_unnest_off.sql", "SELECT tag, off FROM events e, e.user_ctx.tags AS tag WITH OFFSET AS off WHERE ", "off FROM", 0, "off WHERE", 0)
	assertDefLocation(t, eng, "file:///i4_s2_impl_unnest_tag.sql", "SELECT tag, off FROM events e, e.user_ctx.tags AS tag WITH OFFSET AS off WHERE ", "tag,", 0, "tag WITH", 0)
	assertDefLocation(t, eng, "file:///i4_s2_impl_unnest_noas.sql", "SELECT tags, off FROM events e, e.user_ctx.tags WITH OFFSET AS off WHERE ", "off FROM", 0, "off WHERE", 0)

	// 4. Stage 2 Pipe |> aliases & barriers.
	assertDefLocation(t, eng, "file:///i4_s2_pipe_as.sql", "FROM events d |> AS ev |> WHERE ev. (", "ev.", 0, "ev |>", 0)
	assertDefLocation(t, eng, "file:///i4_s2_pipe_as_tok.sql", "FROM events d |> AS ev |> WHERE ev. = (", "ev.", 0, "ev |>", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s2_pipe_as_shadow.sql", "FROM events d |> AS ev |> WHERE d. (", "d.", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s2_pipe_as_shadow_tok.sql", "FROM events d |> AS ev |> WHERE d. = (", "d.", 0)
	assertDefLocation(t, eng, "file:///i4_s2_pipe_sel_in.sql", "FROM events d |> SELECT d.x + (", "d.", 0, "d |>", 0)
	assertDefLocation(t, eng, "file:///i4_s0_pipe_sel_in.sql", "FROM events d |> SELECT d.x", "d.", 0, "d |>", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s2_pipe_sel_after.sql", "FROM events d |> SELECT d.x |> WHERE d. (", "d.", 1)
	assertDefLocation(t, eng, "file:///i4_s2_pipe_piv_pos.sql", "FROM events d |> PIVOT(COUNT("+star+") FOR status IN ('ok')) AS p |> WHERE p. (", "p.", 0, "p |>", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s2_pipe_piv_shadow.sql", "FROM events d |> PIVOT(COUNT("+star+") FOR status IN ('ok')) AS p |> WHERE d. (", "d.", 0)
	assertDefLocation(t, eng, "file:///i4_s2_pipe_piv_in.sql", "FROM events d |> PIVOT(SUM(d. (", "d.", 0, "d |>", 0)
	assertDefLocation(t, eng, "file:///i4_s2_pipe_call.sql", "FROM events d |> CALL my_tvf(1) AS x |> WHERE x. (", "x.", 0, "x |>", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s2_pipe_call_shadow.sql", "FROM events d |> CALL my_tvf(1) AS x |> WHERE d. (", "d.", 0)
	assertDefLocation(t, eng, "file:///i4_s2_pipe_mr.sql", "FROM events d |> MATCH_RECOGNIZE(ORDER BY ts MEASURES A.x AS x PATTERN (A) DEFINE A AS TRUE) AS mr |> WHERE mr. (", "mr.", 0, "mr |>", 0)
	assertEmptyDefLocation(t, eng, "file:///i4_s2_pipe_mr_shadow.sql", "FROM events d |> MATCH_RECOGNIZE(ORDER BY ts MEASURES A.x AS x PATTERN (A) DEFINE A AS TRUE) AS mr |> WHERE d. (", "d.", 0)
}

func TestRegressionDefinitionIter5Stage0(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	star := "*"

	// 1. Direct *ASTIdentifier non-script targets in definitionFromAST.
	assertEmptyDefLocation(t, eng, "file:///i5_col_def.sql", "DECLARE x INT64 DEFAULT 1;\nCREATE TEMP TABLE t (x INT64);", "x INT64)", 0)
	assertDefLocation(t, eng, "file:///i5_col_def_default_pos.sql", "DECLARE x_var INT64 DEFAULT 1;\nCREATE TEMP TABLE t (x INT64 DEFAULT x_var);", "x_var);", 0, "x_var INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_struct_field.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT CAST(NULL AS STRUCT<x INT64>);", "x INT64>", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_struct_col_field.sql", "DECLARE x INT64 DEFAULT 1;\nCREATE TEMP TABLE t (s STRUCT<x INT64>);", "x INT64>", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_window_def.sql", "DECLARE w INT64 DEFAULT 1;\nSELECT ROW_NUMBER() OVER w FROM events WINDOW w AS (ORDER BY event_date);", "w AS (", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_window_ref.sql", "DECLARE w INT64 DEFAULT 1;\nSELECT ROW_NUMBER() OVER w FROM events WINDOW w AS (ORDER BY event_date);", "w FROM", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_replace_alias.sql", "DECLARE status STRING DEFAULT 'ok';\nSELECT "+star+" REPLACE ('x' AS status) FROM events WHERE event_date = '2026-01-01';", "status)", 0)
	assertDefLocation(t, eng, "file:///i5_replace_expr_pos.sql", "DECLARE status_var STRING DEFAULT 'ok'; DECLARE status STRING DEFAULT 'no';\nSELECT "+star+" REPLACE (status_var AS status) FROM events WHERE event_date = '2026-01-01';", "status_var AS", 0, "status_var STRING", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_pipe_set_lhs.sql", "DECLARE status STRING DEFAULT 'ok';\nFROM events d |> SET status = 'ok'", "status =", 0)
	assertDefLocation(t, eng, "file:///i5_pipe_set_rhs_pos.sql", "DECLARE status STRING DEFAULT 'ok'; DECLARE status_var STRING DEFAULT 'v';\nFROM events d |> SET status = status_var", "status_var", 1, "status_var STRING", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_named_arg_lhs.sql", "DECLARE arg INT64 DEFAULT 1;\nSELECT "+star+" FROM my_tvf(arg => 1);", "arg =>", 0)
	assertDefLocation(t, eng, "file:///i5_named_arg_rhs_pos.sql", "DECLARE arg_var INT64 DEFAULT 1;\nSELECT "+star+" FROM my_tvf(arg => arg_var);", "arg_var);", 0, "arg_var INT64", 0)

	// 2. UDF / TVF parameter & body scoping (DECLARE is not in scope inside SQL UDF/TVF bodies).
	assertEmptyDefLocation(t, eng, "file:///i5_udf_name.sql", "DECLARE f INT64 DEFAULT 1;\nCREATE TEMP FUNCTION f(x INT64) RETURNS INT64 AS (x + 1);", "f(x", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_udf_param.sql", "DECLARE x INT64 DEFAULT 1;\nCREATE TEMP FUNCTION f(x INT64) RETURNS INT64 AS (x + 1);", "x INT64)", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_udf_body.sql", "DECLARE x INT64 DEFAULT 1;\nCREATE TEMP FUNCTION f(x INT64) RETURNS INT64 AS (x + 1);", "x + 1", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_tvf_body_decl.sql", "DECLARE x INT64 DEFAULT 1;\nCREATE TEMP TABLE FUNCTION tf(x INT64) AS (SELECT x, d.event_date FROM events d);", "x, d", 0)
	assertDefLocation(t, eng, "file:///i5_tvf_body_alias_pos.sql", "CREATE TEMP TABLE FUNCTION tf(x INT64) AS (SELECT x, d.event_date FROM events d);", "d.event_date", 0, "d);", 0)

	// 3. UNPIVOT, MATCH_RECOGNIZE, and GRAPH_TABLE DECLARE-leak suppression.
	assertEmptyDefLocation(t, eng, "file:///i5_unpivot_val.sql", "DECLARE val INT64 DEFAULT 1; DECLARE c INT64 DEFAULT 2; DECLARE status STRING DEFAULT 'ok';\nSELECT u.val FROM events UNPIVOT(val FOR c IN (status)) u", "val FOR", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_unpivot_col.sql", "DECLARE val INT64 DEFAULT 1; DECLARE c INT64 DEFAULT 2;\nSELECT u.val FROM events UNPIVOT(val FOR c IN (status)) u", "c IN", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_unpivot_in_unk.sql", "DECLARE status STRING DEFAULT 'ok';\nSELECT u.val FROM unknown_tbl UNPIVOT(val FOR c IN (status)) u", "status))", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_pipe_unpivot_val.sql", "DECLARE val INT64 DEFAULT 1;\nFROM events |> UNPIVOT(val FOR c IN (status)) AS u", "val FOR", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_mr_measures_a.sql", "DECLARE A INT64 DEFAULT 1;\nSELECT m.x FROM events e MATCH_RECOGNIZE(PARTITION BY e.tenant_id ORDER BY event_date MEASURES LAST(A.status) AS x PATTERN (A) DEFINE A AS A.status = 'ok') m", "A.status)", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_mr_pattern_a.sql", "DECLARE A INT64 DEFAULT 1;\nSELECT m.x FROM events e MATCH_RECOGNIZE(PARTITION BY e.tenant_id ORDER BY event_date MEASURES LAST(A.status) AS x PATTERN (A) DEFINE A AS A.status = 'ok') m", "A) DEFINE", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_mr_define_a_qual.sql", "DECLARE A INT64 DEFAULT 1;\nSELECT m.x FROM events e MATCH_RECOGNIZE(PARTITION BY e.tenant_id ORDER BY event_date MEASURES LAST(A.status) AS x PATTERN (A) DEFINE A AS A.status = 'ok') m", "A.status =", 0)
	assertDefLocation(t, eng, "file:///i5_mr_partition_e_pos.sql", "DECLARE A INT64 DEFAULT 1;\nSELECT m.x FROM events e MATCH_RECOGNIZE(PARTITION BY e.tenant_id ORDER BY event_date MEASURES LAST(A.status) AS x PATTERN (A) DEFINE A AS A.status = 'ok') m", "e.tenant_id", 0, "e MATCH_RECOGNIZE", 0)
	assertDefLocation(t, eng, "file:///i5_mr_var_pos.sql", "DECLARE var_x STRING DEFAULT 'ok';\nSELECT m.x FROM events e MATCH_RECOGNIZE(ORDER BY event_date MEASURES LAST(A.status) AS x PATTERN (A) DEFINE A AS A.status = var_x) m", "var_x)", 0, "var_x STRING", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_graph_ref.sql", "DECLARE my_graph INT64 DEFAULT 1; DECLARE n INT64 DEFAULT 1;\nSELECT g.a FROM GRAPH_TABLE(my_graph MATCH (n) COLUMNS (n.id AS a)) g", "my_graph MATCH", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_graph_elem.sql", "DECLARE my_graph INT64 DEFAULT 1; DECLARE n INT64 DEFAULT 1;\nSELECT g.a FROM GRAPH_TABLE(my_graph MATCH (n) COLUMNS (n.id AS a)) g", "n) COLUMNS", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_graph_qual.sql", "DECLARE my_graph INT64 DEFAULT 1; DECLARE n INT64 DEFAULT 1;\nSELECT g.a FROM GRAPH_TABLE(my_graph MATCH (n) COLUMNS (n.id AS a)) g", "n.id", 0)
	assertDefLocation(t, eng, "file:///i5_graph_outer_pos.sql", "DECLARE g INT64 DEFAULT 1;\nSELECT g.a FROM GRAPH_TABLE(my_graph MATCH (n) COLUMNS (n.id AS a)) g", "g.a", 0, "g", 3)
}

func TestRegressionDefinitionIter5Stage2(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	star := "*"

	// 1. Stage 2 |> SET LHS vs RHS & |> EXTEND / SELECT / AGGREGATE AS alias.
	assertEmptyDefLocation(t, eng, "file:///i5_s2_pipe_set_lhs.sql", "DECLARE status STRING DEFAULT 'ok';\nFROM events d |> SET status = (", "status =", 0)
	assertDefLocation(t, eng, "file:///i5_s2_pipe_set_rhs.sql", "DECLARE status_var STRING DEFAULT 'ok';\nFROM events d |> SET status = status_var + (", "status_var +", 0, "status_var STRING", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_s2_pipe_set_dot_neg.sql", "FROM events d |> SET d.status = (", "d.", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_s2_pipe_extend_as.sql", "DECLARE status STRING DEFAULT 'ok';\nFROM events d |> EXTEND 'ok' AS status |> WHERE (", "status |>", 0)
	assertDefLocation(t, eng, "file:///i5_s2_pipe_extend_rhs.sql", "DECLARE status_var STRING DEFAULT 'ok'; DECLARE status STRING DEFAULT 'no';\nFROM events d |> EXTEND CAST(status_var AS STRING) AS status |> WHERE (", "status_var AS", 0, "status_var STRING", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_s2_pipe_agg_as.sql", "DECLARE cnt INT64 DEFAULT 1;\nFROM events d |> AGGREGATE COUNT("+star+") AS cnt |> WHERE (", "cnt |>", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_s2_pipe_sel_as.sql", "DECLARE status STRING DEFAULT 'ok';\nFROM events d |> SELECT 'ok' AS status |> WHERE (", "status |>", 0)

	// 2. Stage 2 DML target restriction (no USING / FROM / prior-statement alias leakage on LHS).
	assertEmptyDefLocation(t, eng, "file:///i5_s2_merge_src_lhs_neg.sql", "MERGE INTO events t USING users s ON t.tenant_id = s.user_id WHEN MATCHED THEN UPDATE SET s.col1 = (", "s.col1", 0)
	assertDefLocation(t, eng, "file:///i5_s2_merge_tgt_lhs_pos.sql", "MERGE INTO events t USING users s ON t.tenant_id = s.user_id WHEN MATCHED THEN UPDATE SET t.col1 = (", "t.col1", 0, "t USING", 0)
	assertDefLocation(t, eng, "file:///i5_s2_merge_src_rhs_pos.sql", "MERGE INTO events t USING users s ON t.tenant_id = s.user_id WHEN MATCHED THEN UPDATE SET t.col1 = s.col1 + (", "s.col1 +", 0, "s ON", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_s2_upd_from_lhs_neg.sql", "UPDATE events e SET u.col1 = 1 FROM users u WHERE (", "u.col1", 0)
	assertDefLocation(t, eng, "file:///i5_s2_upd_tgt_lhs_pos.sql", "UPDATE events e SET e.col1 = u.col1 + 1 FROM users u WHERE (", "e.col1", 0, "e SET", 0)
	assertDefLocation(t, eng, "file:///i5_s2_upd_from_rhs_pos.sql", "UPDATE events e SET e.col1 = u.col1 + 1 FROM users u WHERE (", "u.col1", 0, "u WHERE", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_s2_begin_upd_isol.sql", "BEGIN\n  UPDATE events e SET e.col1 = 1;\n  UPDATE users u SET e.col1 = (;\nEND;", "e.col1 = (", 0)

	// 3. Stage 2 WITH OFFSET separated from dotted aliases.
	assertEmptyDefLocation(t, eng, "file:///i5_s2_with_off_dot_neg.sql", "SELECT off.a FROM UNNEST([1]) AS u WITH OFFSET AS off WHERE ", "off.", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_s2_with_off_dot_decl_neg.sql", "DECLARE off STRUCT<a INT64>;\nSELECT off.a FROM UNNEST([1]) AS u WITH OFFSET AS off WHERE ", "off.", 0)
	assertDefLocation(t, eng, "file:///i5_s2_with_off_standalone_pos.sql", "DECLARE off INT64 DEFAULT 1;\nSELECT off FROM UNNEST([1]) AS u WITH OFFSET AS off WHERE ", "off FROM", 0, "off WHERE", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_s2_with_off_pivot_shadow.sql", "SELECT off FROM UNNEST([1]) AS u WITH OFFSET AS off PIVOT(COUNT("+star+") FOR u IN (1)) p WHERE ", "off FROM", 0)

	// 4. Stage 2 |> RECURSIVE UNION ... AS r, INSERT (col1, ), * EXCEPT / * REPLACE, BEGIN...END ; clamping.
	assertDefLocation(t, eng, "file:///i5_s2_rec_union_r_pos.sql", "FROM events d |> RECURSIVE UNION ALL (FROM r) AS r |> WHERE r. +", "r.", 0, "r |>", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_s2_rec_union_d_neg.sql", "FROM events d |> RECURSIVE UNION ALL (FROM r) AS r |> WHERE d. +", "d.", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_s2_ins_trailing_comma.sql", "DECLARE col1 INT64 DEFAULT 1;\nINSERT INTO events (col1, ) VALUES (", "col1,", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_s2_except.sql", "DECLARE status STRING DEFAULT 'ok';\nSELECT "+star+" EXCEPT (status) FROM events WHERE (", "status)", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_s2_replace.sql", "DECLARE status STRING DEFAULT 'ok';\nSELECT "+star+" REPLACE ('x' AS status) FROM events WHERE (", "status)", 0)
	assertEmptyDefLocation(t, eng, "file:///i5_s2_begin_pipe_as_isol.sql", "BEGIN\n  FROM events d |> AS ev |> WHERE ev. (;\n  FROM users u |> WHERE ev. (;\nEND;", "ev.", 1)
}

func TestRegressionDefinitionIter6(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	star := "*"

	// 1. Challenge 1: TABLESAMPLE method and CREATE VIEW column list (Stage 0 + Stage 2).
	assertEmptyDefLocation(t, eng, "file:///i6_tablesample_system.sql", "DECLARE SYSTEM INT64 DEFAULT 1;\nSELECT e.status FROM events e TABLESAMPLE SYSTEM (10 PERCENT) WHERE e.event_date = '2026-01-01';", "SYSTEM (", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_tablesample_bernoulli.sql", "DECLARE BERNOULLI INT64 DEFAULT 1;\nSELECT e.status FROM events e TABLESAMPLE BERNOULLI (10 PERCENT) WHERE e.event_date = '2026-01-01';", "BERNOULLI (", 0)
	assertDefLocation(t, eng, "file:///i6_tablesample_alias_pos.sql", "SELECT e.status FROM events e TABLESAMPLE SYSTEM (10 PERCENT) WHERE e.event_date = '2026-01-01';", "e.status", 0, "e TABLESAMPLE", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_s2_tablesample_system.sql", "DECLARE SYSTEM INT64 DEFAULT 1;\nSELECT e.status FROM events e TABLESAMPLE SYSTEM (", "SYSTEM (", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_view_col.sql", "DECLARE col1 INT64 DEFAULT 1;\nCREATE TEMP VIEW v (col1) AS SELECT 1 AS col1;", "col1) AS", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_view_col_opts.sql", "DECLARE col1 INT64 DEFAULT 1;\nCREATE TEMP VIEW v (col1 OPTIONS(description='hi')) AS SELECT 1 AS col1;", "col1 OPTIONS", 0)
	assertDefLocation(t, eng, "file:///i6_view_col_opts_rhs_pos.sql", "DECLARE desc_var STRING DEFAULT 'hi';\nCREATE TEMP VIEW v (col1 OPTIONS(description=desc_var)) AS SELECT 1 AS col1;", "desc_var))", 0, "desc_var STRING", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_s2_view_col.sql", "DECLARE col1 INT64 DEFAULT 1;\nCREATE TEMP VIEW v (col1) AS SELECT (", "col1) AS", 0)

	// 2. Challenge 2: CREATE PROCEDURE body/language and CALL procedure names (Stage 0 + Stage 2).
	assertEmptyDefLocation(t, eng, "file:///i6_proc_body_x.sql", "DECLARE x INT64 DEFAULT 1;\nCREATE PROCEDURE my_proc(IN x INT64) BEGIN SELECT x; END;", "x;", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_proc_body_outer_y.sql", "DECLARE y INT64 DEFAULT 1;\nCREATE PROCEDURE my_proc(IN x INT64) BEGIN SELECT y; END;", "y;", 0)
	assertDefLocation(t, eng, "file:///i6_proc_body_alias_pos.sql", "DECLARE d INT64 DEFAULT 1;\nCREATE PROCEDURE my_proc(IN x INT64) BEGIN SELECT d.event_date FROM events d; END;", "d.event_date", 0, "d;", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_proc_lang_js.sql", "DECLARE js INT64 DEFAULT 1;\nCREATE PROCEDURE my_proc(IN x INT64) OPTIONS(strict_mode=true) LANGUAGE js AS 'return x;';", "js AS", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_call_proc.sql", "DECLARE my_proc INT64 DEFAULT 1;\nCALL my_proc(1);", "my_proc(1)", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_call_qual_proc_ds.sql", "DECLARE ds INT64 DEFAULT 1; DECLARE my_proc INT64 DEFAULT 1;\nCALL ds.my_proc(1);", "ds.my_proc", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_call_qual_proc_name.sql", "DECLARE ds INT64 DEFAULT 1; DECLARE my_proc INT64 DEFAULT 1;\nCALL ds.my_proc(1);", "my_proc(1)", 0)
	assertDefLocation(t, eng, "file:///i6_call_arg_pos.sql", "DECLARE arg_var INT64 DEFAULT 1;\nCALL my_proc(arg_var);", "arg_var);", 0, "arg_var INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_call_named_arg_neg.sql", "DECLARE arg INT64 DEFAULT 1; DECLARE arg_var INT64 DEFAULT 2;\nCALL my_proc(arg => arg_var);", "arg =>", 0)
	assertDefLocation(t, eng, "file:///i6_call_named_arg_rhs_pos.sql", "DECLARE arg INT64 DEFAULT 1; DECLARE arg_var INT64 DEFAULT 2;\nCALL my_proc(arg => arg_var);", "arg_var);", 0, "arg_var INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_s2_proc_body_outer_y.sql", "DECLARE y INT64 DEFAULT 1;\nCREATE PROCEDURE my_proc(IN x INT64) BEGIN SELECT y FROM events WHERE (; END;", "y FROM", 0)
	assertDefLocation(t, eng, "file:///i6_s2_proc_body_alias_pos.sql", "DECLARE d INT64 DEFAULT 1;\nCREATE PROCEDURE my_proc(IN x INT64) BEGIN SELECT d.event_date FROM events d WHERE (; END;", "d.event_date", 0, "d WHERE", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_s2_proc_lang_js.sql", "DECLARE js INT64 DEFAULT 1;\nCREATE PROCEDURE my_proc(IN x INT64) LANGUAGE js AS (", "js AS", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_s2_call_proc.sql", "DECLARE my_proc INT64 DEFAULT 1;\nCALL my_proc(", "my_proc(", 0)

	// 3. Challenge 3: GRAPH_TABLE outer DECLARE struct & FOR ... IN loop variable.
	assertDefLocation(t, eng, "file:///i6_graph_where_decl_struct.sql", "DECLARE cfg STRUCT<limit_val INT64>;\nSELECT g.a FROM GRAPH_TABLE(my_graph MATCH (n) WHERE n.val = cfg.limit_val COLUMNS (n.id AS a)) g", "cfg.limit_val", 0, "cfg STRUCT", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_graph_where_elem_shadow.sql", "DECLARE cfg STRUCT<limit_val INT64>; DECLARE n STRUCT<val INT64>;\nSELECT g.a FROM GRAPH_TABLE(my_graph MATCH (n) WHERE n.val = cfg.limit_val COLUMNS (n.id AS a)) g", "n.val", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_for_in_decl_site.sql", "DECLARE r INT64 DEFAULT 1;\nFOR r IN (SELECT 1 AS a) DO SELECT r.a; END FOR;", "r IN (", 0)
	assertDefLocation(t, eng, "file:///i6_for_in_qual_ref.sql", "DECLARE r INT64 DEFAULT 1;\nFOR r IN (SELECT 1 AS a) DO SELECT r.a; END FOR;", "r.a", 0, "r IN (", 0)
	assertDefLocation(t, eng, "file:///i6_for_in_single_ref.sql", "DECLARE r INT64 DEFAULT 1;\nFOR r IN (SELECT 1 AS a) DO SELECT r; END FOR;", "r;", 0, "r IN (", 0)
	assertDefLocation(t, eng, "file:///i6_for_in_query_outer_pos.sql", "DECLARE r INT64 DEFAULT 1;\nFOR r IN (SELECT r FROM events WHERE event_date = '2026-01-01') DO SELECT r.a; END FOR;", "r FROM", 0, "r INT64", 0)
	assertDefLocation(t, eng, "file:///i6_s2_for_in_qual_ref.sql", "DECLARE r INT64 DEFAULT 1;\nFOR r IN (SELECT 1 AS a) DO SELECT r.a FROM events WHERE (; END FOR;", "r.a", 0, "r IN (", 0)

	// 4. Low Stage 2 clean hardening: QUALIFY select-alias, SELECT ... AS, STRUCT(... AS ...), |> DROP, |> RENAME.
	assertEmptyDefLocation(t, eng, "file:///i6_s2_qualify_rn.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT event_date, ROW_NUMBER() OVER (ORDER BY event_date) AS rn FROM events QUALIFY rn = (", "rn = (", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_s2_qualify_as_rn.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT event_date, ROW_NUMBER() OVER (ORDER BY event_date) AS rn FROM events QUALIFY rn = (", "rn FROM", 0)
	assertDefLocation(t, eng, "file:///i6_s2_qualify_var_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT event_date, ROW_NUMBER() OVER (ORDER BY event_date) AS other FROM events QUALIFY rn = (", "rn = (", 0, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_s2_reg_select_as.sql", "DECLARE status STRING DEFAULT 'ok';\nSELECT 'ok' AS status FROM events WHERE (", "status FROM", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_s2_pipe_extend_struct_as.sql", "DECLARE status STRING DEFAULT 'ok';\nFROM events d |> EXTEND STRUCT(1 AS status) AS out |> WHERE (", "status)", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_s2_replace_struct_as.sql", "DECLARE status STRING DEFAULT 'ok';\nSELECT "+star+" REPLACE (STRUCT(1 AS status) AS out) FROM events WHERE (", "status)", 0)
	assertDefLocation(t, eng, "file:///i6_s2_mr_define_var_pos.sql", "DECLARE var_x STRING DEFAULT 'ok';\nSELECT "+star+" REPLACE (1 AS out) FROM events MATCH_RECOGNIZE(ORDER BY event_date MEASURES A.status AS x PATTERN (A) DEFINE A AS var_x = (", "var_x = (", 0, "var_x STRING", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_s2_pipe_drop.sql", "DECLARE status STRING DEFAULT 'ok';\nFROM events d |> DROP status |> WHERE (", "status |>", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_s2_pipe_rename_old.sql", "DECLARE status STRING DEFAULT 'ok'; DECLARE new_s STRING DEFAULT 'x';\nFROM events d |> RENAME status AS new_s |> WHERE (", "status AS", 0)
	assertEmptyDefLocation(t, eng, "file:///i6_s2_pipe_rename_new.sql", "DECLARE status STRING DEFAULT 'ok'; DECLARE new_s STRING DEFAULT 'x';\nFROM events d |> RENAME status AS new_s |> WHERE (", "new_s |>", 0)
}

func TestRegressionDefinitionIter7(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	star := "*"

	// 1. Generalized DDL & Label DECLARE isolation (Stage 0).
	assertEmptyDefLocation(t, eng, "file:///i7_alter_table_target.sql", "DECLARE events INT64 DEFAULT 1;\nALTER TABLE events ADD COLUMN new_col STRING;", "events ADD", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_alter_col_type.sql", "DECLARE status INT64 DEFAULT 1;\nALTER TABLE events ALTER COLUMN status SET DATA TYPE STRING;", "status SET", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_alter_col_opts.sql", "DECLARE status INT64 DEFAULT 1;\nALTER TABLE events ALTER COLUMN status SET OPTIONS(description='d');", "status SET", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_alter_col_set_default.sql", "DECLARE status INT64 DEFAULT 1;\nALTER TABLE events ALTER COLUMN status SET DEFAULT 'x';", "status SET", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_alter_col_drop_default.sql", "DECLARE status INT64 DEFAULT 1;\nALTER TABLE events ALTER COLUMN status DROP DEFAULT;", "status DROP", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_alter_col_drop_not_null.sql", "DECLARE status INT64 DEFAULT 1;\nALTER TABLE events ALTER COLUMN status DROP NOT NULL;", "status DROP", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_alter_col_drop.sql", "DECLARE status INT64 DEFAULT 1;\nALTER TABLE events DROP COLUMN status;", "status;", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_alter_col_rename_old.sql", "DECLARE status INT64 DEFAULT 1;\nALTER TABLE events RENAME COLUMN status TO new_status;", "status TO", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_alter_col_rename_new.sql", "DECLARE new_status INT64 DEFAULT 1;\nALTER TABLE events RENAME COLUMN status TO new_status;", "new_status;", 0)

	assertEmptyDefLocation(t, eng, "file:///i7_label_begin.sql", "DECLARE my_lbl INT64 DEFAULT 1;\nmy_lbl: BEGIN SELECT 1; END;", "my_lbl: BEGIN", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_label_leave.sql", "DECLARE my_lbl INT64 DEFAULT 1;\nmy_lbl: LOOP LEAVE my_lbl; END LOOP;", "my_lbl; END", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_label_iterate.sql", "DECLARE my_lbl INT64 DEFAULT 1;\nmy_lbl: LOOP ITERATE my_lbl; END LOOP;", "my_lbl; END", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_label_break.sql", "DECLARE my_lbl INT64 DEFAULT 1;\nmy_lbl: WHILE TRUE DO BREAK my_lbl; END WHILE;", "my_lbl; END", 0)

	assertEmptyDefLocation(t, eng, "file:///i7_create_model_name.sql", "DECLARE m1 INT64 DEFAULT 1;\nCREATE OR REPLACE MODEL m1 OPTIONS(model_type='linear_reg') AS SELECT 1 AS label;", "m1 OPTIONS", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_create_model_conn.sql", "DECLARE conn1 INT64 DEFAULT 1;\nCREATE MODEL m1 REMOTE WITH CONNECTION conn1 OPTIONS(endpoint='e');", "conn1 OPTIONS", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_create_model_aliased_q.sql", "DECLARE q1 INT64 DEFAULT 1;\nCREATE MODEL m1 AS (q1 AS (SELECT 1 AS a));", "q1 AS", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_create_matview.sql", "DECLARE mv1 INT64 DEFAULT 1;\nCREATE MATERIALIZED VIEW mv1 AS SELECT status FROM events;", "mv1 AS", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_create_ext_table.sql", "DECLARE ext1 INT64 DEFAULT 1;\nCREATE EXTERNAL TABLE ext1 OPTIONS(format='CSV', uris=['gs://b/*']);", "ext1 OPTIONS", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_create_snapshot.sql", "DECLARE snap1 INT64 DEFAULT 1;\nCREATE SNAPSHOT TABLE snap1 CLONE events;", "snap1 CLONE", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_create_snapshot_src.sql", "DECLARE events INT64 DEFAULT 1;\nCREATE SNAPSHOT TABLE snap1 CLONE events;", "events;", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_create_schema.sql", "DECLARE ds1 INT64 DEFAULT 1;\nCREATE SCHEMA ds1;", "ds1;", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_create_prop_graph.sql", "DECLARE g1 INT64 DEFAULT 1;\nCREATE PROPERTY GRAPH g1 NODE TABLES (events KEY (tenant_id));", "g1 NODE", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_create_table_like.sql", "DECLARE events INT64 DEFAULT 1;\nCREATE TEMP TABLE t2 LIKE events;", "events;", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_create_table_copy.sql", "DECLARE events INT64 DEFAULT 1;\nCREATE TEMP TABLE t2 COPY events;", "events;", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_create_rap_name.sql", "DECLARE rap1 INT64 DEFAULT 1; DECLARE events INT64 DEFAULT 2;\nCREATE ROW ACCESS POLICY rap1 ON events GRANT TO ('allUsers') FILTER USING (TRUE);", "rap1 ON", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_create_rap_tbl.sql", "DECLARE rap1 INT64 DEFAULT 1; DECLARE events INT64 DEFAULT 2;\nCREATE ROW ACCESS POLICY rap1 ON events GRANT TO ('allUsers') FILTER USING (TRUE);", "events GRANT", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_drop_rap_name.sql", "DECLARE rap1 INT64 DEFAULT 1; DECLARE events INT64 DEFAULT 2;\nDROP ROW ACCESS POLICY rap1 ON events;", "rap1 ON", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_drop_rap_tbl.sql", "DECLARE rap1 INT64 DEFAULT 1; DECLARE events INT64 DEFAULT 2;\nDROP ROW ACCESS POLICY rap1 ON events;", "events;", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_drop_all_rap_tbl.sql", "DECLARE events INT64 DEFAULT 1;\nDROP ALL ROW ACCESS POLICIES ON events;", "events;", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_drop_table.sql", "DECLARE events INT64 DEFAULT 1;\nDROP TABLE events;", "events;", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_drop_view.sql", "DECLARE v1 INT64 DEFAULT 1;\nDROP VIEW v1;", "v1;", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_export_model.sql", "DECLARE m1 INT64 DEFAULT 1;\nEXPORT MODEL m1 OPTIONS(uri='gs://b/*');", "m1 OPTIONS", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_export_data_conn.sql", "DECLARE conn1 INT64 DEFAULT 1;\nEXPORT DATA WITH CONNECTION conn1 OPTIONS(uri='gs://b/*', format='CSV') AS SELECT 1;", "conn1 OPTIONS", 0)
	assertDefLocation(t, eng, "file:///i7_export_data_query_pos.sql", "DECLARE var_x INT64 DEFAULT 1;\nEXPORT DATA OPTIONS(uri='gs://b/*', format='CSV') AS SELECT var_x FROM events;", "var_x FROM", 0, "var_x INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_load_data_tbl.sql", "DECLARE my_tbl INT64 DEFAULT 1;\nLOAD DATA INTO my_tbl FROM FILES(format='CSV', uris=['gs://b/*']);", "my_tbl FROM", 0)

	// 2. Bare GROUP BY / ORDER BY select-alias shadowing in Stage 0 and Stage 1 (+ positive controls).
	assertEmptyDefLocation(t, eng, "file:///i7_s0_order_by_alias.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT status, user_id AS rn FROM events ORDER BY rn;", "rn;", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_s0_group_by_alias.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT status, user_id AS rn FROM events GROUP BY status, rn;", "rn;", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_s1_group_by_alias_paren.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT status, 1 AS rn FROM events GROUP BY rn, (", "rn, (", 0)
	assertDefLocation(t, eng, "file:///i7_s0_over_order_by_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT ROW_NUMBER() OVER (ORDER BY rn) AS rn FROM events;", "rn) AS", 0, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_s0_order_by_subq_alias.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events ORDER BY (SELECT rn FROM users LIMIT 1);", "rn FROM users", 0)
	assertDefLocation(t, eng, "file:///i7_s0_order_by_subq_var_pos.sql", "DECLARE rn_var INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events ORDER BY (SELECT rn_var FROM users LIMIT 1);", "rn_var FROM users", 0, "rn_var INT64", 0)
	assertDefLocation(t, eng, "file:///i7_s0_group_by_subq_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY (SELECT rn FROM users LIMIT 1);", "rn FROM users", 0, "rn INT64", 0)

	// 3. Stage 2 hardening: CASE...END in CREATE PROCEDURE, LIMIT/FETCH/INTO bound, * EXCEPT (...), nested FOR, incomplete GRAPH_TABLE.
	assertEmptyDefLocation(t, eng, "file:///i7_s2_proc_case_end.sql", "DECLARE y INT64 DEFAULT 1;\nCREATE PROCEDURE my_proc() BEGIN\n  SELECT CASE WHEN 1=1 THEN 1 ELSE 2 END;\n  SELECT y FROM events WHERE (;\nEND;", "y FROM", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_s2_proc_case_if_end.sql", "DECLARE y INT64 DEFAULT 1;\nCREATE PROCEDURE p() BEGIN\n  IF TRUE THEN SELECT CASE WHEN 1=1 THEN 1 ELSE 2 END; END IF;\n  SELECT y FROM events WHERE (;\nEND;", "y FROM", 0)

	assertDefLocation(t, eng, "file:///i7_s2_limit_after_order_pos.sql", "DECLARE lim INT64 DEFAULT 10;\nSELECT 1 AS lim FROM events ORDER BY event_date LIMIT lim + (", "lim +", 0, "lim INT64", 0)
	assertDefLocation(t, eng, "file:///i7_s2_fetch_after_order_pos.sql", "DECLARE lim INT64 DEFAULT 10;\nSELECT 1 AS lim FROM events ORDER BY event_date FETCH FIRST lim + (", "lim +", 0, "lim INT64", 0)

	assertEmptyDefLocation(t, eng, "file:///i7_s2_except_before_as_qualify.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT "+star+" EXCEPT (tenant_id), ROW_NUMBER() OVER (ORDER BY event_date) AS rn FROM events QUALIFY rn = (", "rn = (", 0)
	assertEmptyDefLocation(t, eng, "file:///i7_s2_except_after_as_qualify.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT ROW_NUMBER() OVER (ORDER BY event_date) AS rn, "+star+" EXCEPT (tenant_id) FROM events QUALIFY rn = (", "rn = (", 0)

	sqlNestedForS2 := "FOR r IN (SELECT 1 AS a) DO\n  FOR r IN (SELECT 2 AS b) DO\n    SELECT r.b FROM events WHERE (;\n  END FOR;\nEND FOR;"
	assertDefLocation(t, eng, "file:///i7_s2_nested_for_inner.sql", sqlNestedForS2, "r.b", 0, "r IN (SELECT 2", 0)
	sqlNestedForAfterS2 := "FOR r IN (SELECT 1 AS a) DO\n  FOR r IN (SELECT 2 AS b) DO\n    SELECT 1;\n  END FOR;\n  SELECT r.a FROM events WHERE (;\nEND FOR;"
	assertDefLocation(t, eng, "file:///i7_s2_nested_for_outer.sql", sqlNestedForAfterS2, "r.a", 0, "r IN (SELECT 1", 0)

	assertEmptyDefLocation(t, eng, "file:///i7_s2_graph_elem_var.sql", "DECLARE n INT64 DEFAULT 1;\nSELECT g.a FROM GRAPH_TABLE(my_graph MATCH (n) WHERE n.val = (", "n.val", 0)
	assertDefLocation(t, eng, "file:///i7_s2_graph_outer_cfg_pos.sql", "DECLARE cfg STRUCT<limit_val INT64>;\nSELECT g.a FROM GRAPH_TABLE(my_graph MATCH (n) WHERE n.val = cfg.limit_val + (", "cfg.limit_val", 0, "cfg STRUCT", 0)
	assertDefLocation(t, eng, "file:///i7_s2_graph_outer_alias_pos.sql", "DECLARE g INT64 DEFAULT 1;\nSELECT g.a FROM GRAPH_TABLE(my_graph MATCH (n) WHERE n.val = 1 COLUMNS (n.id AS a)) g WHERE g.a = (", "g.a = (", 0, "g WHERE", 0)
}

func TestRegressionDefinitionIter8(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	star := "*"

	// 1. Guard definitionInSQLWASM against raw-token DECLARE fallthrough on valid ASTs (END <label>, keywords).
	assertEmptyDefLocation(t, eng, "file:///i8_end_loop_label.sql", "DECLARE my_lbl INT64 DEFAULT 1;\nmy_lbl: LOOP LEAVE my_lbl; END LOOP my_lbl;", "my_lbl;", 1)
	assertEmptyDefLocation(t, eng, "file:///i8_end_while_label.sql", "DECLARE my_lbl INT64 DEFAULT 1;\nmy_lbl: WHILE TRUE DO LEAVE my_lbl; END WHILE my_lbl;", "my_lbl;", 1)
	assertEmptyDefLocation(t, eng, "file:///i8_end_for_label.sql", "DECLARE my_lbl INT64 DEFAULT 1;\nmy_lbl: FOR r IN (SELECT 1 AS a) DO LEAVE my_lbl; END FOR my_lbl;", "my_lbl;", 1)
	assertEmptyDefLocation(t, eng, "file:///i8_kw_backtick_decl_guard.sql", "DECLARE `select` INT64 DEFAULT 1;\nSELECT 1;", "SELECT 1", 0)

	// 2. Generalized ORDER BY select-alias shadowing (UNION ALL, parenthesized query, pipe |> ORDER BY, Stage 1 HAVING/QUALIFY).
	assertEmptyDefLocation(t, eng, "file:///i8_s0_union_order_by.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events UNION ALL SELECT 2 AS rn FROM users ORDER BY rn;", "rn;", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_s0_union3_order_by.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events UNION ALL SELECT 2 AS rn FROM users UNION ALL SELECT 3 AS rn FROM events ORDER BY rn;", "rn;", 0)
	assertDefLocation(t, eng, "file:///i8_s0_union_order_by_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS other FROM events UNION ALL SELECT 2 AS other FROM users ORDER BY rn;", "rn;", 0, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_s0_paren_order_by.sql", "DECLARE rn INT64 DEFAULT 1;\n(SELECT 1 AS rn FROM events) ORDER BY rn;", "rn;", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_s0_paren_pipe_order_by.sql", "DECLARE rn INT64 DEFAULT 1;\n(FROM events |> EXTEND 1 AS rn) ORDER BY rn;", "rn;", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_s0_select_pipe_order_by.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events |> ORDER BY rn;", "rn;", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_s0_pipe_select_order_by.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> SELECT 1 AS rn |> ORDER BY rn;", "rn;", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_s0_pipe_extend_order_by.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> ORDER BY rn;", "rn;", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_s0_pipe_agg_order_by.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> AGGREGATE COUNT("+star+") AS rn |> ORDER BY rn;", "rn;", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_s0_pipe_agg_grp_order_by.sql", "DECLARE grp INT64 DEFAULT 1;\nFROM events |> AGGREGATE COUNT("+star+") AS cnt GROUP BY status AS grp |> ORDER BY grp;", "grp;", 0)
	assertDefLocation(t, eng, "file:///i8_s0_pipe_drop_order_by_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> SELECT 1 AS rn |> DROP rn |> ORDER BY rn;", "rn;", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i8_s0_pipe_rename_order_by_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> RENAME rn AS rn2 |> ORDER BY rn;", "rn;", 0, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_s0_pipe_rename_new_order_by.sql", "DECLARE rn2 INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> RENAME rn AS rn2 |> ORDER BY rn2;", "rn2;", 0)
	assertDefLocation(t, eng, "file:///i8_s0_pipe_select_barrier_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> SELECT 2 AS other |> ORDER BY rn;", "rn;", 0, "rn INT64", 0)

	// Stage 1 QUALIFY (rn / HAVING (rn parity + positive controls.
	assertEmptyDefLocation(t, eng, "file:///i8_s1_qualify_paren.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events QUALIFY (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i8_s1_having_paren.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT status, 1 AS rn FROM events GROUP BY status HAVING (rn", "rn", 2)
	assertDefLocation(t, eng, "file:///i8_s1_having_paren_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT status, 1 AS other FROM events GROUP BY status HAVING (rn", "rn", 1, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_s1_qualify_over_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events QUALIFY ROW_NUMBER() OVER (ORDER BY rn", "rn", 2)

	// Stage 2 pipe |> ORDER BY selectAliasDecls.
	assertEmptyDefLocation(t, eng, "file:///i8_s2_pipe_extend_order_by.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_s2_select_pipe_order_by.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events |> ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_s2_pipe_select_order_by.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> SELECT 1 AS rn |> ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_s2_pipe_agg_order_by.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> AGGREGATE COUNT("+star+") AS rn |> ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_s2_pipe_set_preserves_order_by.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> SET status = 'ok' |> ORDER BY rn + (", "rn +", 0)
	assertDefLocation(t, eng, "file:///i8_s2_pipe_drop_order_by_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> SELECT 1 AS rn |> DROP rn |> ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i8_s2_pipe_rename_order_by_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> RENAME rn AS rn2 |> ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_s2_pipe_rename_new_order_by.sql", "DECLARE rn2 INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> RENAME rn AS rn2 |> ORDER BY rn2 + (", "rn2 +", 0)
	assertDefLocation(t, eng, "file:///i8_s2_pipe_select_barrier_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> SELECT 2 AS other |> ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_s2_pipe_where_not_shadowed_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> WHERE rn = (", "rn = (", 0)

	// 3. Missing DDL/Graph AST nodes (ASTPrimaryKeyElement, ASTGraphElementLabelAndProperties, ASTGraphPathPattern).
	assertEmptyDefLocation(t, eng, "file:///i8_pk_col.sql", "DECLARE a INT64 DEFAULT 2;\nCREATE TABLE t (a INT64, PRIMARY KEY (a) NOT ENFORCED);", "a) NOT", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_prop_graph_label.sql", "DECLARE lbl1 INT64 DEFAULT 1;\nCREATE PROPERTY GRAPH g1 NODE TABLES (events KEY (tenant_id) LABEL lbl1 PROPERTIES (status));", "lbl1 PROPERTIES", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_graph_path_var.sql", "DECLARE p INT64 DEFAULT 1;\nSELECT g.a FROM GRAPH_TABLE(my_graph MATCH p = (n)->(m) COLUMNS (1 AS a)) g;", "p =", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_graph_path_var_ref.sql", "DECLARE p INT64 DEFAULT 1;\nSELECT g.a FROM GRAPH_TABLE(my_graph MATCH p = (n)->(m) WHERE p IS NOT NULL COLUMNS (1 AS a)) g;", "p IS", 0)

	// 4. Stage 2 GRAPH_TABLE unclosed '(' ';' clamp & CREATE PROCEDURE dotted .end field access.
	assertDefLocation(t, eng, "file:///i8_s2_graph_unclosed_next_stmt.sql", "DECLARE n INT64 DEFAULT 1;\nSELECT g.a FROM GRAPH_TABLE(my_graph MATCH (n) WHERE n.val = (;\nSELECT n FROM events;", "n FROM events", 0, "n INT64", 0)
	assertDefLocation(t, eng, "file:///i8_s2_graph_unclosed_begin_next.sql", "DECLARE n INT64 DEFAULT 1;\nBEGIN\n  SELECT g.a FROM GRAPH_TABLE(my_graph MATCH (n) WHERE n.val = (;\n  SELECT n FROM events WHERE (;\nEND;", "n FROM events", 0, "n INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i8_s2_proc_dot_end.sql", "DECLARE y INT64 DEFAULT 1;\nCREATE PROCEDURE p() BEGIN SELECT s.end FROM events s; SELECT y FROM events WHERE (; END;", "y FROM", 0)
	assertDefLocation(t, eng, "file:///i8_s2_proc_dot_end_after_pos.sql", "DECLARE y INT64 DEFAULT 1;\nCREATE PROCEDURE p() BEGIN SELECT s.end FROM events s; END;\nSELECT y FROM events WHERE (;", "y FROM", 0, "y INT64", 0)
}

func TestRegressionDefinitionIter9(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	star := "*"

	// 1. ResolvedOrderByItem & bare GROUP BY column references (+ DECLARE x positive controls).
	assertEmptyDefLocation(t, eng, "file:///i9_s0_bare_col_order_by.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT status FROM events WHERE event_date = '2026-01-01' ORDER BY status;", "status;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_bare_col_order_by_desc.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT status FROM events WHERE event_date = '2026-01-01' ORDER BY status DESC;", "status DESC", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_star_order_by.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT "+star+" FROM events WHERE event_date = '2026-01-01' ORDER BY status;", "status;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_alias_order_bare_col.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT a.status FROM events a WHERE a.event_date = '2026-01-01' ORDER BY status;", "status;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_bare_col_group_by.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT status FROM events WHERE event_date = '2026-01-01' GROUP BY status;", "status;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_bare_col_group_by_cnt.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT status, COUNT("+star+") FROM events WHERE event_date = '2026-01-01' GROUP BY status;", "status;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_rollup_group_by.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT status FROM events WHERE event_date = '2026-01-01' GROUP BY ROLLUP(status);", "status);", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_grouping_sets.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT status FROM events WHERE event_date = '2026-01-01' GROUP BY GROUPING SETS((status), ());", "status),", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_bare_col_order.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> ORDER BY status;", "status;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_sel_col_order.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> SELECT status |> ORDER BY status;", "status;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_set_order.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> SET status = 'ok' |> ORDER BY status;", "status;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_drop_other_order.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> DROP tenant_id |> ORDER BY status;", "status;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_join_order.sql", "DECLARE country INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> JOIN users ON events.user_ctx.user_id = users.user_id |> ORDER BY country;", "country;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_pivot_order.sql", "DECLARE a INT64 DEFAULT 1;\nFROM users |> PIVOT (COUNT("+star+") FOR country IN ('US' AS a)) |> ORDER BY a;", "a;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_pivot_passthrough_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM users |> EXTEND 1 AS rn |> PIVOT (COUNT("+star+") FOR country IN ('US' AS a)) |> ORDER BY rn;", "rn;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_pivot_struct_fallback_order.sql", "DECLARE a INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> PIVOT (COUNT("+star+") FOR status IN ('ok' AS a)) |> ORDER BY a;", "a;", 0)
	assertDefLocation(t, eng, "file:///i9_s0_pipe_pivot_for_barrier_pos.sql", "DECLARE country INT64 DEFAULT 1;\nFROM users |> PIVOT (COUNT("+star+") FOR country IN ('US' AS a)) |> ORDER BY country;", "country;", 0, "country INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_unpivot_order.sql", "DECLARE v INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> UNPIVOT (v FOR k IN (status, tenant_id)) |> ORDER BY v;", "v;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_unk_dotted_order.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT a.status FROM unknown_tbl a ORDER BY status;", "status;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_unk_dotted_group.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT a.status FROM unknown_tbl a GROUP BY status;", "status;", 0)

	// Positive controls for DECLARE x in GROUP BY / ORDER BY / |> ORDER BY / scalar subquery / OVER.
	assertDefLocation(t, eng, "file:///i9_s0_decl_select_group_pos.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT x FROM events WHERE event_date = '2026-01-01' GROUP BY x;", "x;", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i9_s0_decl_select_order_pos.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT x FROM events WHERE event_date = '2026-01-01' ORDER BY x;", "x;", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i9_s0_decl_order_pos.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT status FROM events WHERE event_date = '2026-01-01' ORDER BY x;", "x;", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i9_s0_decl_pipe_order_pos.sql", "DECLARE x INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> ORDER BY x;", "x;", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i9_s0_decl_group_invalid_pos.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT status FROM events GROUP BY x;", "x;", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i9_s0_decl_unk_group_pos.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT x FROM unknown_tbl GROUP BY x;", "x;", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i9_s0_decl_unk_order_pos.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT x FROM unknown_tbl ORDER BY x;", "x;", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i9_s0_decl_select_over_pos.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT ROW_NUMBER() OVER (ORDER BY x) AS x FROM events WHERE event_date = '2026-01-01';", "x) AS", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i9_s0_decl_qualify_over_other_pos.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 AS other FROM events WHERE event_date = '2026-01-01' QUALIFY ROW_NUMBER() OVER (ORDER BY x) = 1;", "x) =", 0, "x INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_qualify_over_select_alias.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 AS x FROM events WHERE event_date = '2026-01-01' QUALIFY ROW_NUMBER() OVER (ORDER BY x) = 1;", "x) =", 0)

	// 2. Stage 0/1 Pipe Operator Tracking (ASTPipeWindow, ASTPipeAs, ASTPipeSetOperation, bare pass-through, TEE/FORK/IF).
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_window_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> WINDOW COUNT("+star+") OVER () AS rn |> ORDER BY rn;", "rn;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_window_passthrough.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS rn |> WINDOW COUNT("+star+") OVER () AS w |> ORDER BY rn;", "rn;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s1_pipe_window_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WINDOW COUNT("+star+") OVER () AS rn |> ORDER BY (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_as_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS rn |> AS t |> ORDER BY rn;", "rn;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s1_pipe_as_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> AS t |> ORDER BY (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_union_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> SELECT 1 AS rn |> UNION ALL (FROM users |> SELECT 2 AS other) |> ORDER BY rn;", "rn;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s1_pipe_union_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> SELECT 1 AS rn |> UNION ALL (FROM users |> SELECT 2 AS other) |> ORDER BY (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_select_pass_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS rn |> SELECT rn |> ORDER BY rn;", "rn;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s1_pipe_select_pass_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> SELECT rn |> ORDER BY (rn", "rn", 3)
	assertDefLocation(t, eng, "file:///i9_s0_pipe_select_decl_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> SELECT rn |> ORDER BY rn;", "rn;", 0, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_agg_bare_tbl_order.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> AGGREGATE COUNT("+star+") AS cnt GROUP BY status |> ORDER BY status;", "status;", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_agg_bare_ext_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS rn |> AGGREGATE COUNT("+star+") AS cnt GROUP BY rn |> ORDER BY rn;", "rn;", 0)
	assertDefLocation(t, eng, "file:///i9_s0_pipe_agg_decl_pos.sql", "DECLARE x INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> AGGREGATE COUNT("+star+") AS cnt GROUP BY x |> ORDER BY x;", "x;", 0, "x INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_tee_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS rn |> TEE (|> ORDER BY rn);", "rn);", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_fork_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS rn |> FORK (|> ORDER BY rn);", "rn);", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_if_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS rn |> IF TRUE THEN (|> ORDER BY rn);", "rn);", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s0_pipe_if_else_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS rn |> IF FALSE THEN (|> SELECT 2 AS other) ELSE (|> ORDER BY rn);", "rn);", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s1_pipe_where_paren.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> WHERE (rn", "rn", 2)

	// 3. Stage 2 Pipe Stream Visibility, UNION ALL Branch-Inversion, Parenthesized Queries, GRAPH_TABLE MATCH p=, OVER, RENAME swap.
	assertEmptyDefLocation(t, eng, "file:///i9_s2_pipe_window_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WINDOW COUNT("+star+") OVER () AS rn |> ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s2_pipe_as_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> AS t |> ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s2_pipe_union_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> SELECT 1 AS rn |> UNION ALL (FROM users |> SELECT 2 AS other) |> ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s2_pipe_select_pass_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> SELECT rn |> ORDER BY rn + (", "rn +", 0)
	assertDefLocation(t, eng, "file:///i9_s2_pipe_select_decl_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> SELECT rn |> ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s2_pipe_agg_pass_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> AGGREGATE COUNT("+star+") AS cnt GROUP BY rn |> ORDER BY rn + (", "rn +", 0)
	assertDefLocation(t, eng, "file:///i9_s2_pipe_agg_decl_pos.sql", "DECLARE x INT64 DEFAULT 1;\nFROM events |> AGGREGATE COUNT("+star+") AS cnt GROUP BY x |> ORDER BY x + (", "x +", 0, "x INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s2_pipe_select_where.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> SELECT 1 AS rn |> WHERE rn = (", "rn = (", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s2_pipe_agg_where.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> AGGREGATE COUNT("+star+") AS rn |> WHERE rn = (", "rn = (", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s2_pipe_extend_extend.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> EXTEND rn + (", "rn + (", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s2_pipe_extend_set_rhs.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> SET status = rn + (", "rn + (", 0)
	assertDefLocation(t, eng, "file:///i9_s2_pipe_drop_where_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> DROP rn |> WHERE rn = (", "rn = (", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i9_s2_select_where_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE rn = (", "rn = (", 0, "rn INT64", 0)

	// Stage 2 UNION ALL branch-inversion.
	assertEmptyDefLocation(t, eng, "file:///i9_s2_union_first_as_order.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT status AS rn FROM events UNION ALL SELECT 'ok' AS other FROM events ORDER BY rn + (", "rn +", 0)
	assertDefLocation(t, eng, "file:///i9_s2_union_second_as_order_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT status FROM events UNION ALL SELECT 'ok' AS rn FROM events ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i9_s2_union_second_as_pipe_order_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT status FROM events UNION ALL SELECT 'ok' AS rn FROM events |> ORDER BY rn + (", "rn +", 0, "rn INT64", 0)

	// Stage 2 parenthesized queries.
	assertEmptyDefLocation(t, eng, "file:///i9_s2_paren_select_order.sql", "DECLARE rn INT64 DEFAULT 1;\n(SELECT 1 AS rn FROM events) ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s2_paren_select_pipe_order.sql", "DECLARE rn INT64 DEFAULT 1;\n(SELECT 1 AS rn FROM events) |> ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s2_paren_pipe_pipe_order.sql", "DECLARE rn INT64 DEFAULT 1;\n(FROM events |> EXTEND 1 AS rn) |> ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s2_nested_paren_order.sql", "DECLARE rn INT64 DEFAULT 1;\n((SELECT 1 AS rn FROM events)) ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s2_paren_union_order.sql", "DECLARE rn INT64 DEFAULT 1;\n(SELECT 1 AS rn FROM events) UNION ALL (SELECT 2 AS other FROM users) ORDER BY rn + (", "rn +", 0)
	assertDefLocation(t, eng, "file:///i9_s2_cte_not_paren_query_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nWITH c AS (SELECT 1 AS rn FROM events) SELECT 2 AS other FROM c |> ORDER BY rn + (", "rn +", 0, "rn INT64", 0)

	// Stage 2 GRAPH_TABLE MATCH p =.
	assertEmptyDefLocation(t, eng, "file:///i9_s2_graph_path_decl.sql", "DECLARE p INT64 DEFAULT 1;\nSELECT g.a FROM GRAPH_TABLE(my_graph MATCH p = (n)->(m) WHERE (", "p =", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s2_graph_path_qual.sql", "DECLARE p INT64 DEFAULT 1;\nSELECT g.a FROM GRAPH_TABLE(my_graph MATCH p = (n)->(m) WHERE p.x = (", "p.x", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s2_graph_path_bare.sql", "DECLARE p INT64 DEFAULT 1;\nSELECT g.a FROM GRAPH_TABLE(my_graph MATCH p = (n)->(m) WHERE p IS NOT NULL AND (", "p IS", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s2_graph_path_cols.sql", "DECLARE p INT64 DEFAULT 1;\nSELECT g.a FROM GRAPH_TABLE(my_graph MATCH p = (n)->(m) COLUMNS (p.x AS a)) g WHERE (", "p.x", 0)
	assertDefLocation(t, eng, "file:///i9_s2_graph_path_unclosed_next_pos.sql", "DECLARE p INT64 DEFAULT 1;\nSELECT g.a FROM GRAPH_TABLE(my_graph MATCH p = (n)->(m) WHERE n.val = (;\nSELECT p FROM events;", "p FROM", 0, "p INT64", 0)

	// Stage 2 OVER (ORDER BY rn) + positive controls (SELECT list & WINDOW clause).
	assertEmptyDefLocation(t, eng, "file:///i9_s2_qualify_over_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events QUALIFY ROW_NUMBER() OVER (ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s2_order_over_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events ORDER BY COUNT("+star+") OVER (ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s2_pipe_over_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> ORDER BY ROW_NUMBER() OVER (ORDER BY rn) + (", "rn) +", 0)
	assertDefLocation(t, eng, "file:///i10_s2_select_over_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT ROW_NUMBER() OVER (ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i10_s2_window_clause_over_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WINDOW w AS (ORDER BY rn + (", "rn +", 0, "rn INT64", 0)

	// Stage 2 |> RENAME swap.
	assertEmptyDefLocation(t, eng, "file:///i9_s2_rename_swap_b.sql", "DECLARE a INT64 DEFAULT 1; DECLARE b INT64 DEFAULT 2;\nFROM events |> EXTEND 1 AS a, 2 AS b |> RENAME a AS b, b AS a |> ORDER BY b + (", "b +", 0)
	assertEmptyDefLocation(t, eng, "file:///i9_s2_rename_swap_rn.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn, 2 AS other |> RENAME other AS rn, rn AS other |> ORDER BY rn + (", "rn +", 0)
}

func TestRegressionDefinitionIter10(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	star := "*"

	// 1. Scoped GROUP BY script-symbol check (CTEs, FROM subqueries, UNION ALL, nested scalar subqueries).
	assertEmptyDefLocation(t, eng, "file:///i10_gb_cte_decl_outer_col.sql", "DECLARE status INT64 DEFAULT 1;\nWITH c AS (SELECT status AS k FROM users GROUP BY status)\nSELECT status FROM events WHERE event_date = '2026-01-01' GROUP BY status;", "status;", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_gb_cte_bare_select.sql", "DECLARE status INT64 DEFAULT 1;\nWITH sub AS (SELECT status FROM users GROUP BY status)\nSELECT status FROM events WHERE event_date = '2026-01-01' GROUP BY status;", "status;", 0)
	assertDefLocation(t, eng, "file:///i10_gb_cte_own_decl_pos.sql", "DECLARE status INT64 DEFAULT 1;\nWITH sub AS (SELECT status FROM users GROUP BY status)\nSELECT status FROM events WHERE event_date = '2026-01-01' GROUP BY status;", "status)", 0, "status INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_gb_from_subq_outer_col.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT status FROM (SELECT COUNT("+star+") AS k FROM users GROUP BY status) u CROSS JOIN events WHERE events.event_date = '2026-01-01' GROUP BY status;", "status;", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_gb_union_all_valid_decl_then_col.sql", "DECLARE status STRING DEFAULT 'ok';\nSELECT status FROM users GROUP BY status UNION ALL SELECT status FROM events WHERE event_date = '2026-01-01' GROUP BY status;", "status;", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_gb_nested_scalar_subq_outer_col.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT (SELECT COUNT("+star+") FROM users GROUP BY status LIMIT 1) AS k, status FROM events WHERE event_date = '2026-01-01' GROUP BY status;", "status;", 0)

	// 2. ResolvedComputedColumn script-symbol lineage (HAVING, QUALIFY, pipe SELECT->AGGREGATE->ORDER BY, explicit AS controls).
	assertDefLocation(t, eng, "file:///i10_decl_sel_x_having_x_pos.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT x FROM events WHERE event_date = '2026-01-01' GROUP BY x HAVING x > 0;", "x >", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i10_decl_sel_x_qualify_x_pos.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT x FROM events WHERE event_date = '2026-01-01' QUALIFY x > 0 AND ROW_NUMBER() OVER () = 1;", "x >", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i10_decl_pipe_sel_x_agg_order_pos.sql", "DECLARE x INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> SELECT "+star+", x |> AGGREGATE COUNT("+star+") AS c GROUP BY x |> ORDER BY x;", "x;", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i10_decl_pipe_sel_x_agg_group_pos.sql", "DECLARE x INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> SELECT "+star+", x |> AGGREGATE COUNT("+star+") AS c GROUP BY x |> ORDER BY x;", "x |>", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i10_decl_sel_x_as_y_order_x_pos.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT x AS y FROM events WHERE event_date = '2026-01-01' ORDER BY x;", "x;", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i10_decl_sel_x_as_y_group_x_pos.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT x AS y FROM events WHERE event_date = '2026-01-01' GROUP BY x;", "x;", 0, "x INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_sel_1_as_x_having.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 AS x FROM events WHERE event_date = '2026-01-01' GROUP BY x HAVING x > 0;", "x >", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_sel_x_as_status_order.sql", "DECLARE x INT64 DEFAULT 1; DECLARE status INT64 DEFAULT 2;\nSELECT x AS status FROM events WHERE event_date = '2026-01-01' ORDER BY status;", "status;", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_sel_from_subq_x_where.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT u.x FROM (SELECT x FROM users) u WHERE u.x > 0;", "x >", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_sel_from_subq_bare_x_where.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT x FROM (SELECT x FROM users) u WHERE x > 0;", "x >", 0)

	// 3. OVER (ORDER BY rn) Stage 0/1/2 parity & WINDOW clause exclusion.
	assertEmptyDefLocation(t, eng, "file:///i10_s0_order_over.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE event_date = '2026-01-01' ORDER BY COUNT("+star+") OVER (ORDER BY rn);", "rn);", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_s1_order_over.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events ORDER BY COUNT("+star+") OVER (ORDER BY rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i10_s0_pipe_order_over.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS rn |> ORDER BY ROW_NUMBER() OVER (ORDER BY rn);", "rn);", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_s1_pipe_order_over.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> ORDER BY ROW_NUMBER() OVER (ORDER BY (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i10_s1_pipe_window_over.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> WINDOW ROW_NUMBER() OVER (ORDER BY rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i10_s2_pipe_window_over.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> SELECT 1 AS rn |> WINDOW ROW_NUMBER() OVER (ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_s2_pipe_extend_over.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> EXTEND ROW_NUMBER() OVER (ORDER BY rn + (", "rn +", 0)
	assertDefLocation(t, eng, "file:///i10_s0_window_clause_over_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE event_date = '2026-01-01' WINDOW w AS (ORDER BY rn);", "rn);", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i10_s1_window_clause_over_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WINDOW w AS (ORDER BY rn", "rn", 2, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i10_s2_group_then_window_over_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY status WINDOW w AS (ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_s2_window_then_order.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WINDOW w AS (ORDER BY event_date) ORDER BY rn + (", "rn +", 0)

	// 4. Stage 1 & Stage 2 Pipe, Subpipeline, CTE-Paren, * EXCEPT / e.*, PIVOT / UNPIVOT / RECURSIVE UNION, and subquery parity.
	assertEmptyDefLocation(t, eng, "file:///i10_s1_pipe_agg_group_by_paren.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> AGGREGATE COUNT("+star+") AS cnt GROUP BY (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i10_s0_unk_pipe_agg_group_by.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM unknown_tbl |> EXTEND 1 AS rn |> AGGREGATE COUNT("+star+") AS cnt GROUP BY rn;", "rn;", 0)
	assertDefLocation(t, eng, "file:///i10_s0_pipe_agg_group_by_x_pos.sql", "DECLARE x INT64 DEFAULT 1;\nFROM events |> AGGREGATE COUNT("+star+") AS cnt GROUP BY x;", "x;", 0, "x INT64", 0)

	assertEmptyDefLocation(t, eng, "file:///i10_s2_pipe_tee_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> TEE (|> ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_s2_pipe_fork_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> FORK (|> ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_s2_pipe_if_cond.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> IF rn = (", "rn = (", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_s2_pipe_if_then_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> IF TRUE THEN (|> ORDER BY rn + (", "rn +", 0)
	assertDefLocation(t, eng, "file:///i10_s2_pipe_if_select_drop_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> IF TRUE THEN (|> SELECT 2 AS other |> ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_s2_pipe_if_else_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> IF FALSE THEN (|> SELECT 2 AS other) ELSE (|> ORDER BY rn + (", "rn +", 0)

	assertEmptyDefLocation(t, eng, "file:///i10_s2_with_paren_order.sql", "DECLARE rn INT64 DEFAULT 1;\nWITH c AS (SELECT 1 AS other FROM events) (SELECT 1 AS rn FROM c) ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_s2_with_paren_pipe_order.sql", "DECLARE rn INT64 DEFAULT 1;\nWITH c AS (SELECT 1 AS other FROM events) (SELECT 1 AS rn FROM c) |> ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_s2_from_subq_alias_pipe_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM (SELECT 1 AS rn FROM events) AS sub |> ORDER BY rn + (", "rn +", 0)

	assertEmptyDefLocation(t, eng, "file:///i10_s1_pipe_star_except_order.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS status |> SELECT "+star+" EXCEPT(val) |> ORDER BY (status", "status", 2)
	assertEmptyDefLocation(t, eng, "file:///i10_s2_pipe_star_except_order.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS status |> SELECT "+star+" EXCEPT(val) |> ORDER BY status + (", "status +", 0)
	assertDefLocation(t, eng, "file:///i10_s2_pipe_star_except_dropped_pos.sql", "DECLARE val INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS val |> SELECT "+star+" EXCEPT(val) |> ORDER BY val + (", "val +", 0, "val INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_s1_pipe_dot_star_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM (SELECT 1 AS rn FROM events) e |> SELECT e."+star+" |> ORDER BY (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i10_s2_pipe_dot_star_order.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM (SELECT 1 AS rn FROM events) e |> SELECT e."+star+" |> ORDER BY rn + (", "rn +", 0)

	assertEmptyDefLocation(t, eng, "file:///i10_s0_unk_pipe_rec_union.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM unknown_tbl |> EXTEND 1 AS rn |> RECURSIVE UNION ALL (|> WHERE rn < 0) |> ORDER BY rn;", "rn;", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_s2_pipe_rec_union.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> RECURSIVE UNION ALL (|> WHERE rn < 0) |> ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_s1_pipe_pivot_passthrough.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM users |> EXTEND 1 AS rn |> PIVOT (COUNT("+star+") FOR country IN ('US' AS a)) |> ORDER BY (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i10_s2_pipe_pivot_passthrough.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM users |> EXTEND 1 AS rn |> PIVOT (COUNT("+star+") FOR country IN ('US' AS a)) |> ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_s2_pipe_pivot_alias.sql", "DECLARE a INT64 DEFAULT 1;\nFROM users |> PIVOT (COUNT("+star+") FOR country IN ('US' AS a)) |> ORDER BY a + (", "a +", 0)
	assertDefLocation(t, eng, "file:///i10_s2_pipe_pivot_for_barrier_pos.sql", "DECLARE country INT64 DEFAULT 1;\nFROM users |> PIVOT (COUNT("+star+") FOR country IN ('US' AS a)) |> ORDER BY country + (", "country +", 0, "country INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_s0_unk_pipe_unpivot.sql", "DECLARE v INT64 DEFAULT 1;\nFROM unknown_tbl |> UNPIVOT (v FOR k IN (status, tenant_id)) |> ORDER BY v;", "v;", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_s2_pipe_unpivot.sql", "DECLARE v INT64 DEFAULT 1;\nFROM events |> UNPIVOT (v FOR k IN (status, tenant_id)) |> ORDER BY v + (", "v +", 0)
	assertEmptyDefLocation(t, eng, "file:///i10_s2_pipe_unpivot_passthrough.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> UNPIVOT (v FOR k IN (status, tenant_id)) |> ORDER BY rn + (", "rn +", 0)

	assertEmptyDefLocation(t, eng, "file:///i10_s1_order_scalar_subq.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events ORDER BY (SELECT rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i10_s1_pipe_where_exists_subq.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 'US' AS rn |> WHERE (EXISTS (SELECT 1 FROM users WHERE country = rn", "rn", 2)
	assertDefLocation(t, eng, "file:///i10_s1_group_scalar_subq_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY (SELECT rn", "rn", 2, "rn INT64", 0)
}

func TestRegressionDefinitionIter11(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	star := "*"

	// 1. ResolvedSetOperationScan lineage in traceResolvedColScriptSymbol (UNION ALL & |> UNION ALL).
	assertDefLocation(t, eng, "file:///i11_union_all_decl_x_order_pos.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT x FROM events WHERE event_date = '2026-01-01' UNION ALL SELECT x FROM users ORDER BY x;", "x;", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i11_union_all_paren_decl_x_order_pos.sql", "DECLARE x INT64 DEFAULT 1;\n(SELECT x FROM events WHERE event_date = '2026-01-01') UNION ALL (SELECT x FROM users) ORDER BY x;", "x;", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i11_union_all_3way_decl_x_order_pos.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT x FROM events WHERE event_date = '2026-01-01' UNION ALL SELECT x FROM users UNION ALL SELECT x FROM users ORDER BY x;", "x;", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i11_union_all_decl_then_1as_x_pos.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT x FROM events WHERE event_date = '2026-01-01' UNION ALL SELECT 1 AS x FROM users ORDER BY x;", "x;", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i11_pipe_union_all_decl_x_order_pos.sql", "DECLARE x INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> SELECT x |> UNION ALL (FROM users |> SELECT x) |> ORDER BY x;", "x;", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i11_pipe_union_all_decl_x_where_pos.sql", "DECLARE x INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> SELECT x |> UNION ALL (FROM users |> SELECT x) |> WHERE x > 0;", "x >", 0, "x INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_union_all_explicit_alias_neg.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 AS x FROM events WHERE event_date = '2026-01-01' UNION ALL SELECT x FROM users ORDER BY x;", "x;", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_pipe_union_all_explicit_alias_neg.sql", "DECLARE x INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> SELECT 1 AS x |> UNION ALL (FROM users |> SELECT x) |> ORDER BY x;", "x;", 0)

	// 2. Stage 0/1/2 |> SELECT <alias>.* (ASTDotStar) & |> IF schema barriers.
	assertDefLocation(t, eng, "file:///i11_s0_pipe_join_b_dot_star_decl_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS rn |> JOIN users b ON TRUE |> SELECT b."+star+" |> ORDER BY rn;", "rn;", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i11_s1_pipe_join_b_dot_star_decl_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> JOIN users b ON TRUE |> SELECT b."+star+" |> ORDER BY (rn", "rn", 2, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i11_s2_pipe_join_b_dot_star_decl_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> JOIN users b ON TRUE |> SELECT b."+star+" |> ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i11_s0_pipe_struct_dot_star_decl_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS rn |> SELECT user_ctx."+star+" |> ORDER BY rn;", "rn;", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i11_s1_pipe_struct_dot_star_decl_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> SELECT user_ctx."+star+" |> ORDER BY (rn", "rn", 2, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i11_s2_pipe_struct_dot_star_decl_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> SELECT user_ctx."+star+" |> ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s0_pipe_struct_dot_star_field.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> SELECT user_ctx."+star+" |> ORDER BY user_id;", "user_id;", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s2_pipe_struct_dot_star_field.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> SELECT user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s0_pipe_as_t_dot_star.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS rn |> AS t |> SELECT t."+star+" |> ORDER BY rn;", "rn;", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s1_pipe_as_t_dot_star.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> AS t |> SELECT t."+star+" |> ORDER BY (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i11_s2_pipe_as_t_dot_star.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> AS t |> SELECT t."+star+" |> ORDER BY rn + (", "rn +", 0)
	assertDefLocation(t, eng, "file:///i11_s0_pipe_if_else_drop_decl_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS rn |> IF TRUE THEN (|> SELECT 2 AS other) ELSE (|> SELECT 3 AS other) |> ORDER BY rn;", "rn;", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i11_s1_pipe_if_else_drop_decl_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> IF TRUE THEN (|> SELECT 2 AS other) ELSE (|> SELECT 3 AS other) |> ORDER BY (rn", "rn", 2, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i11_s2_pipe_if_else_drop_decl_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS rn |> IF TRUE THEN (|> SELECT 2 AS other) ELSE (|> SELECT 3 AS other) |> ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s0_pipe_if_else_other.sql", "DECLARE other INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> IF TRUE THEN (|> SELECT 2 AS other) ELSE (|> SELECT 3 AS other) |> ORDER BY other;", "other;", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s1_pipe_if_else_other.sql", "DECLARE other INT64 DEFAULT 1;\nFROM events |> IF TRUE THEN (|> SELECT 2 AS other) ELSE (|> SELECT 3 AS other) |> ORDER BY (other", "other", 3)
	assertEmptyDefLocation(t, eng, "file:///i11_s2_pipe_if_else_other.sql", "DECLARE other INT64 DEFAULT 1;\nFROM events |> IF TRUE THEN (|> SELECT 2 AS other) ELSE (|> SELECT 3 AS other) |> ORDER BY other + (", "other +", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s0_pipe_if_no_else_false_pass.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS rn |> IF FALSE THEN (|> SELECT 2 AS other) |> ORDER BY rn;", "rn;", 0)
	assertDefLocation(t, eng, "file:///i11_s0_pipe_if_no_else_true_drop_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS rn |> IF TRUE THEN (|> SELECT 2 AS other) |> ORDER BY rn;", "rn;", 0, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s2_pipe_if_no_else_other.sql", "DECLARE other INT64 DEFAULT 1;\nFROM events |> IF TRUE THEN (|> SELECT 2 AS other) |> ORDER BY other + (", "other +", 0)

	// 3. Stage 1/2 FROM <table> catalog column pass-through & i9_adv_18_pipe_pivot_implicit_col_s1.
	assertEmptyDefLocation(t, eng, "file:///i11_adv_18_pipe_pivot_implicit_col_s1.sql", "DECLARE tenant_id INT64 DEFAULT 1;\nFROM events |> SELECT tenant_id, status |> PIVOT (COUNT("+star+") FOR status IN ('ok' AS a)) |> ORDER BY (tenant_id", "tenant_id", 2)
	assertEmptyDefLocation(t, eng, "file:///i11_pipe_pivot_implicit_col_s2.sql", "DECLARE tenant_id INT64 DEFAULT 1;\nFROM events |> SELECT tenant_id, status |> PIVOT (COUNT("+star+") FOR status IN ('ok' AS a)) |> ORDER BY tenant_id + (", "tenant_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_pipe_agg_table_col_s1.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> AGGREGATE COUNT("+star+") AS cnt GROUP BY status |> ORDER BY (status", "status", 2)
	assertEmptyDefLocation(t, eng, "file:///i11_pipe_agg_table_col_s2.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> AGGREGATE COUNT("+star+") AS cnt GROUP BY status |> ORDER BY status + (", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_pipe_select_table_col_s1.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> SELECT tenant_id, status |> ORDER BY (status", "status", 2)
	assertEmptyDefLocation(t, eng, "file:///i11_pipe_select_table_col_s2.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> SELECT tenant_id, status |> ORDER BY status + (", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_select_table_col_pipe_order_s1.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT status FROM events |> ORDER BY (status", "status", 2)
	assertEmptyDefLocation(t, eng, "file:///i11_select_table_col_pipe_order_s2.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT status FROM events |> ORDER BY status + (", "status +", 0)
	assertDefLocation(t, eng, "file:///i11_pipe_select_unknown_col_s1_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> SELECT rn |> ORDER BY (rn", "rn", 2, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i11_pipe_drop_table_col_s1_pos.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> DROP status |> ORDER BY (status", "status", 2, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i11_pipe_drop_table_col_s2_pos.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> DROP status |> ORDER BY status + (", "status +", 0, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i11_pipe_pivot_consumed_col_s1_pos.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> PIVOT (COUNT("+star+") FOR status IN ('ok' AS a)) |> ORDER BY (status", "status", 2, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i11_select_dropped_table_col_pipe_s1_pos.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT tenant_id FROM events |> ORDER BY (status", "status", 1, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i11_select_dropped_table_col_pipe_s2_pos.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT tenant_id FROM events |> ORDER BY status + (", "status +", 0, "status INT64", 0)

	// 4. Stage 2 |> UNPIVOT INCLUDE NULLS / EXCLUDE NULLS.
	assertEmptyDefLocation(t, eng, "file:///i11_s2_unpivot_inc_nulls_v.sql", "DECLARE v INT64 DEFAULT 1;\nFROM events |> UNPIVOT INCLUDE NULLS (v FOR k IN (status, tenant_id)) |> ORDER BY v + (", "v +", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s2_unpivot_inc_nulls_k.sql", "DECLARE k INT64 DEFAULT 1;\nFROM events |> UNPIVOT INCLUDE NULLS (v FOR k IN (status, tenant_id)) |> ORDER BY k + (", "k +", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s2_unpivot_exc_nulls_v.sql", "DECLARE v INT64 DEFAULT 1;\nFROM events |> UNPIVOT EXCLUDE NULLS (v FOR k IN (status, tenant_id)) |> ORDER BY v + (", "v +", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s2_unpivot_inc_nulls_multi_val.sql", "DECLARE v1 INT64 DEFAULT 1;\nFROM events |> UNPIVOT INCLUDE NULLS ((v1, v2) FOR k IN ((status, tenant_id) AS 'a')) |> ORDER BY v1 + (", "v1 +", 0)
	assertDefLocation(t, eng, "file:///i11_s2_unpivot_inc_nulls_consumed_pos.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> UNPIVOT INCLUDE NULLS (v FOR k IN (status, tenant_id)) |> ORDER BY status + (", "status +", 0, "status INT64", 0)

	// 5. Stage 2 FROM (...) JOIN ... |>, FROM (...), ... |>, |> JOIN (...).
	assertEmptyDefLocation(t, eng, "file:///i11_s2_from_subq_join_pipe.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM (SELECT 1 AS rn FROM events) AS sub JOIN users ON TRUE |> ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s2_from_subq_comma_pipe.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM (SELECT 1 AS rn FROM events) AS sub, users |> ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s2_from_join_subq_pipe.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events JOIN (SELECT 1 AS rn FROM users) AS u ON TRUE |> ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s2_pipe_join_subq.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events |> JOIN (SELECT 1 AS rn FROM users) AS u ON TRUE |> ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s2_from_two_subq_join_pipe.sql", "DECLARE b INT64 DEFAULT 1;\nFROM (SELECT 1 AS a FROM events) s1 JOIN (SELECT 2 AS b FROM users) s2 ON TRUE |> ORDER BY b + (", "b +", 0)

	// 6. Stage 2 SELECT * REPLACE(... AS alias).
	assertEmptyDefLocation(t, eng, "file:///i11_s2_sel_star_replace_order.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT "+star+" REPLACE('x' AS status) FROM events ORDER BY status + (", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s2_sel_star_replace_pipe_order.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT "+star+" REPLACE('x' AS status) FROM events |> ORDER BY status + (", "status +", 0)
	assertDefLocation(t, eng, "file:///i11_s2_sel_star_replace_expr_pos.sql", "DECLARE status_var INT64 DEFAULT 1;\nSELECT "+star+" REPLACE(status_var AS status) FROM events ORDER BY status_var + (", "status_var +", 0, "status_var INT64", 0)

	// 7. Stage 1/2 |> PIVOT / |> UNPIVOT input refs & aliased-agg s_a outputs.
	assertEmptyDefLocation(t, eng, "file:///i11_s0_pivot_input_ref.sql", "DECLARE x INT64 DEFAULT 1;\nFROM users |> EXTEND 1 AS x |> PIVOT (SUM(x) FOR country IN ('US' AS a));", "x)", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s1_pivot_input_ref.sql", "DECLARE x INT64 DEFAULT 1;\nFROM users |> EXTEND 1 AS x |> PIVOT (SUM(x) FOR country IN ('US' AS a)) |> WHERE (1=1", "x)", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s2_pivot_input_ref.sql", "DECLARE x INT64 DEFAULT 1;\nFROM users |> EXTEND 1 AS x |> PIVOT (SUM(x) FOR country IN ('US' AS a)) |> WHERE (", "x)", 0)
	assertDefLocation(t, eng, "file:///i11_s1_pivot_unknown_input_pos.sql", "DECLARE y INT64 DEFAULT 1;\nFROM users |> EXTEND 1 AS x |> PIVOT (SUM(y) FOR country IN ('US' AS a)) |> WHERE (1=1", "y)", 0, "y INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s2_unpivot_input_ref.sql", "DECLARE x INT64 DEFAULT 1;\nFROM events |> EXTEND 'a' AS x |> UNPIVOT (v FOR k IN (x, status)) |> WHERE (", "x,", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s0_pivot_agg_alias_s_a.sql", "DECLARE s_a INT64 DEFAULT 1;\nFROM users |> EXTEND 1 AS x |> PIVOT (SUM(x) AS s FOR country IN ('US' AS a)) |> ORDER BY s_a;", "s_a;", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s1_pivot_agg_alias_s_a.sql", "DECLARE s_a INT64 DEFAULT 1;\nFROM users |> EXTEND 1 AS x |> PIVOT (SUM(x) AS s FOR country IN ('US' AS a)) |> ORDER BY (s_a", "s_a", 1)
	assertEmptyDefLocation(t, eng, "file:///i11_s2_pivot_agg_alias_s_a.sql", "DECLARE s_a INT64 DEFAULT 1;\nFROM users |> EXTEND 1 AS x |> PIVOT (SUM(x) AS s FOR country IN ('US' AS a)) |> ORDER BY s_a + (", "s_a +", 0)
	assertDefLocation(t, eng, "file:///i11_s0_pivot_agg_alias_bare_a_pos.sql", "DECLARE a INT64 DEFAULT 1;\nFROM users |> EXTEND 1 AS x |> PIVOT (SUM(x) AS s FOR country IN ('US' AS a)) |> ORDER BY a;", "a;", 0, "a INT64", 0)
	assertDefLocation(t, eng, "file:///i11_s1_pivot_agg_alias_bare_a_pos.sql", "DECLARE a INT64 DEFAULT 1;\nFROM users |> EXTEND 1 AS x |> PIVOT (SUM(x) AS s FOR country IN ('US' AS a)) |> ORDER BY (a", "a", 2, "a INT64", 0)
	assertDefLocation(t, eng, "file:///i11_s2_pivot_agg_alias_bare_a_pos.sql", "DECLARE a INT64 DEFAULT 1;\nFROM users |> EXTEND 1 AS x |> PIVOT (SUM(x) AS s FOR country IN ('US' AS a)) |> ORDER BY a + (", "a +", 0, "a INT64", 0)

	// 8. Stage 1/2 QUALIFY ... WINDOW w AS (ORDER BY rn) & Stage 1 ORDER BY (SELECT ... ORDER BY rn ...).
	assertEmptyDefLocation(t, eng, "file:///i11_s0_qualify_window_w.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE event_date = '2026-01-01' QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (ORDER BY rn + 1);", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s1_qualify_window_w.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (ORDER BY rn + 1", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s2_qualify_window_w.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (ORDER BY rn + (", "rn +", 0)
	assertDefLocation(t, eng, "file:///i11_s1_select_and_qualify_window_w_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn, ROW_NUMBER() OVER w AS k FROM events QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (ORDER BY rn + 1", "rn +", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i11_s2_select_and_qualify_window_w_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn, ROW_NUMBER() OVER w AS k FROM events QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s0_order_subq_inner_order.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE event_date = '2026-01-01' ORDER BY (SELECT 1 FROM users ORDER BY rn + 1 LIMIT 1);", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s1_order_subq_inner_order.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events ORDER BY (SELECT 1 FROM users ORDER BY rn + 1 LIMIT 1", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i11_s2_order_subq_inner_order.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events ORDER BY (SELECT 1 FROM users ORDER BY rn + (", "rn +", 0)
}

func TestRegressionDefinitionIter12(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	star := "*"

	// 1. CTE shadowing catalog/temp tables checked before catalog/temp lookup.
	assertDefLocation(t, eng, "file:///i12_s0_cte_shadow_select.sql", "DECLARE status INT64 DEFAULT 1;\nWITH events AS (SELECT 1 AS a)\nSELECT status FROM events ORDER BY status;", "status;", 0, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s0_cte_shadow_pipe_where.sql", "DECLARE status INT64 DEFAULT 1;\nWITH events AS (SELECT 1 AS a)\nFROM events |> SELECT status |> WHERE status > 0;", "status >", 0, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s0_cte_shadow_pipe_if.sql", "DECLARE status INT64 DEFAULT 1;\nWITH events AS (SELECT 1 AS a)\nFROM events |> IF TRUE THEN (|> SELECT status) ELSE (|> SELECT status) |> WHERE status > 0;", "status >", 0, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s1_cte_shadow_pipe_star.sql", "DECLARE status INT64 DEFAULT 1;\nWITH events AS (SELECT 1 AS a)\nFROM events |> SELECT "+star+" |> ORDER BY (status", "status", 1, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s2_cte_shadow_pipe_star.sql", "DECLARE status INT64 DEFAULT 1;\nWITH events AS (SELECT 1 AS a)\nFROM events |> SELECT "+star+" |> ORDER BY status + (", "status +", 0, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s2_cte_shadow_select.sql", "DECLARE status INT64 DEFAULT 1;\nWITH events AS (SELECT 1 AS a)\nSELECT status FROM events |> ORDER BY status + (", "status +", 0, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s1_cte_shadow_dot_star.sql", "DECLARE status INT64 DEFAULT 1;\nWITH events AS (SELECT 1 AS a)\nFROM events |> SELECT events."+star+" |> ORDER BY (status", "status", 1, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s2_cte_shadow_dot_star.sql", "DECLARE status INT64 DEFAULT 1;\nWITH events AS (SELECT 1 AS a)\nFROM events |> SELECT events."+star+" |> ORDER BY status + (", "status +", 0, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s0_cte_shadow_temp_tbl.sql", "DECLARE status INT64 DEFAULT 1;\nCREATE TEMP TABLE tmp_ev AS SELECT 'ok' AS status;\nWITH tmp_ev AS (SELECT 1 AS a) SELECT status FROM tmp_ev ORDER BY status;", "status;", 1, "status INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s0_cte_shadow_own_col.sql", "DECLARE a INT64 DEFAULT 1;\nWITH events AS (SELECT 1 AS a)\nSELECT a FROM events ORDER BY a;", "a;", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s1_cte_shadow_own_col.sql", "DECLARE a INT64 DEFAULT 1;\nWITH events AS (SELECT 1 AS a)\nFROM events |> SELECT "+star+" |> ORDER BY (a", "a", 2)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_cte_shadow_own_col.sql", "DECLARE a INT64 DEFAULT 1;\nWITH events AS (SELECT 1 AS a)\nFROM events |> SELECT "+star+" |> ORDER BY a + (", "a +", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s0_cte_same_name_non_rec.sql", "DECLARE status INT64 DEFAULT 1;\nWITH events AS (SELECT status FROM events WHERE event_date = '2026-01-01')\nSELECT status FROM events ORDER BY status;", "status;", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_subq_cte_scoped.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT status FROM (WITH events AS (SELECT 1 AS a) SELECT a FROM events) sub JOIN events ON TRUE ORDER BY status + (", "status +", 0)

	// 2. Stage 1 pipeOpDotStarQualState respects stopped on ASTPipePivot & ASTPipeUnpivot.
	assertDefLocation(t, eng, "file:///i12_s0_pivot_as_p_dot_star_consumed.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> SELECT tenant_id, status |> PIVOT (COUNT("+star+") FOR status IN ('ok' AS ok_cnt)) AS p |> SELECT p."+star+" |> ORDER BY status;", "status;", 0, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s1_pivot_as_p_dot_star_consumed.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> SELECT tenant_id, status |> PIVOT (COUNT("+star+") FOR status IN ('ok' AS ok_cnt)) AS p |> SELECT p."+star+" |> ORDER BY (status", "status", 3, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s2_pivot_as_p_dot_star_consumed.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> SELECT tenant_id, status |> PIVOT (COUNT("+star+") FOR status IN ('ok' AS ok_cnt)) AS p |> SELECT p."+star+" |> ORDER BY status + (", "status +", 0, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s1_unpivot_as_u_dot_star_consumed.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> UNPIVOT (v FOR k IN (status, tenant_id)) AS u |> SELECT u."+star+" |> ORDER BY (status", "status", 2, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s2_unpivot_as_u_dot_star_consumed.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> UNPIVOT (v FOR k IN (status, tenant_id)) AS u |> SELECT u."+star+" |> ORDER BY status + (", "status +", 0, "status INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s1_pivot_as_p_dot_star_ok_cnt.sql", "DECLARE ok_cnt INT64 DEFAULT 1;\nFROM events |> SELECT tenant_id, status |> PIVOT (COUNT("+star+") FOR status IN ('ok' AS ok_cnt)) AS p |> SELECT p."+star+" |> ORDER BY (ok_cnt", "ok_cnt", 2)
	assertEmptyDefLocation(t, eng, "file:///i12_s1_pivot_as_p_dot_star_passthrough.sql", "DECLARE tenant_id INT64 DEFAULT 1;\nFROM events |> SELECT tenant_id, status |> PIVOT (COUNT("+star+") FOR status IN ('ok' AS ok_cnt)) AS p |> SELECT p."+star+" |> ORDER BY (tenant_id", "tenant_id", 2)

	// 3. Stage 2 schema-replacing pipe barriers clear range vars & Stage 1 pipeOpDotStarQualState handles ASTPipeIf.
	assertDefLocation(t, eng, "file:///i12_s1_select_bar_e_dot_star.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> AS e |> SELECT 1 AS a |> SELECT e."+star+" |> ORDER BY (status", "status", 1, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s2_select_bar_e_dot_star.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> AS e |> SELECT 1 AS a |> SELECT e."+star+" |> ORDER BY status + (", "status +", 0, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s1_agg_bar_e_dot_star.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> AS e |> AGGREGATE COUNT("+star+") AS c GROUP BY tenant_id |> SELECT e."+star+" |> ORDER BY (status", "status", 1, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s2_agg_bar_e_dot_star.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> AS e |> AGGREGATE COUNT("+star+") AS c GROUP BY tenant_id |> SELECT e."+star+" |> ORDER BY status + (", "status +", 0, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s1_select_bar_user_ctx_dot_star.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> SELECT 1 AS a |> SELECT user_ctx."+star+" |> ORDER BY (user_id", "user_id", 1, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s2_select_bar_user_ctx_dot_star.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> SELECT 1 AS a |> SELECT user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s1_if_else_e_dot_star.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> AS e |> IF TRUE THEN (|> SELECT 2 AS other) ELSE (|> SELECT 3 AS other) |> SELECT e."+star+" |> ORDER BY (status", "status", 1, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s2_if_else_e_dot_star.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> AS e |> IF TRUE THEN (|> SELECT 2 AS other) ELSE (|> SELECT 3 AS other) |> SELECT e."+star+" |> ORDER BY status + (", "status +", 0, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s1_if_else_user_ctx_dot_star.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> IF TRUE THEN (|> SELECT 2 AS other) ELSE (|> SELECT 3 AS other) |> SELECT user_ctx."+star+" |> ORDER BY (user_id", "user_id", 1, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s2_if_else_user_ctx_dot_star.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> IF TRUE THEN (|> SELECT 2 AS other) ELSE (|> SELECT 3 AS other) |> SELECT user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s1_if_else_events_dot_star.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> IF TRUE THEN (|> SELECT 2 AS other) ELSE (|> SELECT 3 AS other) |> SELECT events."+star+" |> ORDER BY (status", "status", 1, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s2_if_else_events_dot_star.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> IF TRUE THEN (|> SELECT 2 AS other) ELSE (|> SELECT 3 AS other) |> SELECT events."+star+" |> ORDER BY status + (", "status +", 0, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s1_pivot_bar_e_dot_star.sql", "DECLARE tenant_id INT64 DEFAULT 1;\nFROM events |> AS e |> PIVOT (COUNT("+star+") FOR status IN ('ok' AS a)) AS p |> SELECT e."+star+" |> ORDER BY (tenant_id", "tenant_id", 1, "tenant_id INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s2_pivot_bar_e_dot_star.sql", "DECLARE tenant_id INT64 DEFAULT 1;\nFROM events |> AS e |> PIVOT (COUNT("+star+") FOR status IN ('ok' AS a)) AS p |> SELECT e."+star+" |> ORDER BY tenant_id + (", "tenant_id +", 0, "tenant_id INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s1_if_partial_keep_e_dot_star.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> AS e |> IF TRUE THEN (|> SELECT 2 AS other) ELSE (|> WHERE TRUE) |> SELECT e."+star+" |> ORDER BY (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_if_partial_keep_e_dot_star.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> AS e |> IF TRUE THEN (|> SELECT 2 AS other) ELSE (|> WHERE TRUE) |> SELECT e."+star+" |> ORDER BY status + (", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s1_if_branch_as_e_dot_star.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> IF TRUE THEN (|> AS e) ELSE (|> SELECT 3 AS other) |> SELECT e."+star+" |> ORDER BY (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_if_branch_as_e_dot_star.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> IF TRUE THEN (|> AS e) ELSE (|> SELECT 3 AS other) |> SELECT e."+star+" |> ORDER BY status + (", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s1_star_then_user_ctx_dot_star.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> SELECT "+star+" |> SELECT user_ctx."+star+" |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_star_then_user_ctx_dot_star.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> SELECT "+star+" |> SELECT user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0)

	// 4. WINDOW clause FROM table-column visibility & Stage 2 handleSelectKeyword / finishTableSubqueryClose post-WHERE clauses.
	assertEmptyDefLocation(t, eng, "file:///i12_s0_window_no_qualify_table_col.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT tenant_id, SUM(1) OVER w FROM events WHERE event_date = '2026-01-01' WINDOW w AS (ORDER BY status);", "status);", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s1_window_no_qualify_table_col.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT tenant_id, SUM(1) OVER w FROM events WINDOW w AS (ORDER BY (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_window_no_qualify_table_col.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT tenant_id, SUM(1) OVER w FROM events WINDOW w AS (ORDER BY status + (", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_window_with_qualify_table_col.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT tenant_id FROM events QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (ORDER BY status + (", "status +", 0)
	assertDefLocation(t, eng, "file:///i12_s1_window_no_qualify_alias_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn, SUM(1) OVER w FROM events WINDOW w AS (ORDER BY (rn", "rn", 2, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s2_window_no_qualify_alias_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn, SUM(1) OVER w FROM events WINDOW w AS (ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_select_table_order_unprojected.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT tenant_id FROM events ORDER BY status + (", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_select_table_having_unprojected.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT tenant_id FROM events GROUP BY tenant_id, status HAVING status + (", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_select_table_qualify_unprojected.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT tenant_id FROM events QUALIFY ROW_NUMBER() OVER (ORDER BY status + (", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_select_star_subq_order.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT "+star+" FROM (SELECT 1 AS rn FROM events) AS sub ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_select_a_subq_order_rn.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT a FROM (SELECT 1 AS rn, 2 AS a FROM events) AS sub ORDER BY rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_select_a_cte_order_rn.sql", "DECLARE rn INT64 DEFAULT 1;\nWITH c AS (SELECT 1 AS rn, 2 AS a FROM events) SELECT a FROM c ORDER BY rn + (", "rn +", 0)
	assertDefLocation(t, eng, "file:///i12_s2_select_a_subq_pipe_order_rn_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT a FROM (SELECT 1 AS rn, 2 AS a FROM events) AS sub |> ORDER BY rn + (", "rn +", 0, "rn INT64", 0)

	// 5. 2+-part e.user_ctx.* in Stage 1 & 2, and Stage 2 SELECT struct_col.* FROM events |>.
	assertEmptyDefLocation(t, eng, "file:///i12_s0_e_user_ctx_dot_star.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events e |> WHERE event_date = '2026-01-01' |> SELECT e.user_ctx."+star+" |> ORDER BY user_id;", "user_id;", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s1_e_user_ctx_dot_star.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events e |> SELECT e.user_ctx."+star+" |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_e_user_ctx_dot_star.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events e |> SELECT e.user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s1_pipe_as_e_user_ctx_dot_star.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> AS e |> SELECT e.user_ctx."+star+" |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_pipe_as_e_user_ctx_dot_star.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> AS e |> SELECT e.user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0)
	assertDefLocation(t, eng, "file:///i12_s1_wrong_alias_user_ctx_dot_star_pos.sql", "DECLARE tags INT64 DEFAULT 1;\nFROM events e JOIN users b ON TRUE |> SELECT b.user_ctx."+star+" |> ORDER BY (tags", "tags", 1, "tags INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s2_wrong_alias_user_ctx_dot_star_pos.sql", "DECLARE tags INT64 DEFAULT 1;\nFROM events e JOIN users b ON TRUE |> SELECT b.user_ctx."+star+" |> ORDER BY tags + (", "tags +", 0, "tags INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s1_e_user_ctx_dot_star_missing_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events e |> SELECT e.user_ctx."+star+" |> ORDER BY (rn", "rn", 1, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s2_e_user_ctx_dot_star_missing_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM events e |> SELECT e.user_ctx."+star+" |> ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_select_struct_dot_star_pipe.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT user_ctx."+star+" FROM events |> ORDER BY user_id + (", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_select_e_struct_dot_star_pipe.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT e.user_ctx."+star+" FROM events e |> ORDER BY user_id + (", "user_id +", 0)
	assertDefLocation(t, eng, "file:///i12_s2_select_u_struct_dot_star_pipe_pos.sql", "DECLARE tags INT64 DEFAULT 1;\nSELECT u.user_ctx."+star+" FROM users u |> ORDER BY tags + (", "tags +", 0, "tags INT64", 0)

	// 6. Dotted-column qualifier checks in Stage 1 & Stage 2.
	assertDefLocation(t, eng, "file:///i12_s1_select_unknown_dotted_col.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT u.status FROM users u |> ORDER BY (status", "status", 2, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s2_select_unknown_dotted_col.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT u.status FROM users u |> ORDER BY status + (", "status +", 0, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s1_select_join_wrong_qual_dotted_col.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT u.status FROM events e JOIN users u ON e.tenant_id = u.country |> ORDER BY (status", "status", 2, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s2_select_join_wrong_qual_dotted_col.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT u.status FROM events e JOIN users u ON e.tenant_id = u.country |> ORDER BY status + (", "status +", 0, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s1_pipe_join_wrong_qual_dotted_col.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events e |> JOIN users u ON TRUE |> SELECT u.status |> ORDER BY (status", "status", 2, "status INT64", 0)
	assertDefLocation(t, eng, "file:///i12_s2_pipe_join_wrong_qual_dotted_col.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events e |> JOIN users u ON TRUE |> SELECT u.status |> ORDER BY status + (", "status +", 0, "status INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s1_select_join_right_qual_dotted_col.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT e.status FROM events e JOIN users u ON e.tenant_id = u.country |> ORDER BY (status", "status", 2)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_select_join_right_qual_dotted_col.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT e.status FROM events e JOIN users u ON e.tenant_id = u.country |> ORDER BY status + (", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s1_unk_dotted_col_pipe.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT a.status FROM unknown_tbl a |> ORDER BY (status", "status", 2)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_unk_dotted_col_pipe.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT a.status FROM unknown_tbl a |> ORDER BY status + (", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s1_unk_pipe_dotted_col.sql", "DECLARE status INT64 DEFAULT 1;\nFROM unknown_tbl a |> SELECT a.status |> ORDER BY (status", "status", 2)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_unk_pipe_dotted_col.sql", "DECLARE status INT64 DEFAULT 1;\nFROM unknown_tbl a |> SELECT a.status |> ORDER BY status + (", "status +", 0)

	// 7. Stage 2 GROUP BY scalar-subquery isolation.
	assertDefLocation(t, eng, "file:///i12_s2_group_by_scalar_subq_order_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY (SELECT 1 FROM users ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_group_by_after_subq_rn.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY (SELECT 1 FROM users ORDER BY 1 LIMIT 1), rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_group_by_paren_rn.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY (rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i12_s2_order_by_scalar_subq_rn.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events ORDER BY (SELECT 1 FROM users ORDER BY rn + (", "rn +", 0)
}

func TestRegressionDefinitionIter13(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	star := "*"

	// 1. Stage 2 UNNEST & TVF tableAlias for dotted projections.
	assertEmptyDefLocation(t, eng, "file:///i13_s0_unnest_sel.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT t.x FROM UNNEST([STRUCT(1 AS x)]) t |> ORDER BY x;", "x;", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_unnest_sel.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT t.x FROM UNNEST([STRUCT(1 AS x)]) t |> ORDER BY x + (", "x +", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_unnest_pipe.sql", "DECLARE x INT64 DEFAULT 1;\nFROM UNNEST([STRUCT(1 AS x)]) t |> SELECT t.x |> ORDER BY x + (", "x +", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_tvf_sel.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT t.x FROM my_tvf() t |> ORDER BY x + (", "x +", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_tvf_pipe.sql", "DECLARE x INT64 DEFAULT 1;\nFROM my_tvf() t |> SELECT t.x |> ORDER BY x + (", "x +", 0)
	assertDefLocation(t, eng, "file:///i13_s2_unnest_wrong_alias_pos.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT a.x FROM UNNEST([STRUCT(1 AS x)]) t |> ORDER BY x + (", "x +", 0, "x INT64", 0)

	// 2. Unified STRUCT provenance across CTEs, subqueries, and pipe barriers.
	assertEmptyDefLocation(t, eng, "file:///i13_s0_cte_bare_struct.sql", "DECLARE user_id INT64 DEFAULT 1;\nWITH c AS (SELECT user_ctx FROM events WHERE event_date = '2026-01-01')\nSELECT user_ctx.user_id FROM c ORDER BY user_id;", "user_id;", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s1_cte_bare_struct.sql", "DECLARE user_id INT64 DEFAULT 1;\nWITH c AS (SELECT user_ctx FROM events)\nSELECT user_ctx.user_id FROM c ORDER BY (user_id", "user_id", 2)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_cte_bare_struct.sql", "DECLARE user_id INT64 DEFAULT 1;\nWITH c AS (SELECT user_ctx FROM events)\nSELECT user_ctx.user_id FROM c |> ORDER BY user_id + (", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s1_cte_qual_dotstar.sql", "DECLARE user_id INT64 DEFAULT 1;\nWITH c AS (SELECT user_ctx FROM events)\nFROM c |> SELECT c.user_ctx."+star+" |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_cte_qual_dotstar.sql", "DECLARE user_id INT64 DEFAULT 1;\nWITH c AS (SELECT user_ctx FROM events)\nFROM c |> SELECT c.user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s1_cte_sel_dotstar.sql", "DECLARE user_id INT64 DEFAULT 1;\nWITH c AS (SELECT user_ctx FROM events)\nSELECT c.user_ctx."+star+" FROM c |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_cte_sel_dotstar.sql", "DECLARE user_id INT64 DEFAULT 1;\nWITH c AS (SELECT user_ctx FROM events)\nSELECT c.user_ctx."+star+" FROM c |> ORDER BY user_id + (", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s1_cte_as_ctx_dotstar.sql", "DECLARE user_id INT64 DEFAULT 1;\nWITH c AS (SELECT user_ctx AS ctx FROM events)\nFROM c |> SELECT c.ctx."+star+" |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_cte_as_ctx_dotstar.sql", "DECLARE user_id INT64 DEFAULT 1;\nWITH c AS (SELECT user_ctx AS ctx FROM events)\nFROM c |> SELECT c.ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0)
	assertDefLocation(t, eng, "file:///i13_s1_cte_1_as_user_ctx_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nWITH c AS (SELECT 1 AS user_ctx FROM events)\nSELECT c.user_ctx."+star+" FROM c |> ORDER BY (user_id", "user_id", 1, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s2_cte_1_as_user_ctx_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nWITH c AS (SELECT 1 AS user_ctx FROM events)\nSELECT c.user_ctx."+star+" FROM c |> ORDER BY user_id + (", "user_id +", 0, "user_id INT64", 0)

	assertEmptyDefLocation(t, eng, "file:///i13_s1_subq_qual_dotstar.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM (SELECT user_ctx FROM events) AS sub |> SELECT sub.user_ctx."+star+" |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_subq_qual_dotstar.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM (SELECT user_ctx FROM events) AS sub |> SELECT sub.user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s1_subq_sel_dotstar.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT sub.user_ctx."+star+" FROM (SELECT user_ctx FROM events) sub |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_subq_sel_dotstar.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT sub.user_ctx."+star+" FROM (SELECT user_ctx FROM events) sub |> ORDER BY user_id + (", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s1_subq_qual_col.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT sub.user_ctx.user_id FROM (SELECT user_ctx FROM events) AS sub |> ORDER BY (user_id", "user_id", 2)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_subq_qual_col.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT sub.user_ctx.user_id FROM (SELECT user_ctx FROM events) AS sub |> ORDER BY user_id + (", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s1_subq_bare_col.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT user_ctx.user_id FROM (SELECT user_ctx FROM events) sub |> ORDER BY (user_id", "user_id", 2)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_subq_bare_col.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT user_ctx.user_id FROM (SELECT user_ctx FROM events) sub |> ORDER BY user_id + (", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s1_subq_as_ctx.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT sub.user_ctx AS ctx FROM (SELECT user_ctx FROM events) sub |> SELECT ctx."+star+" |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_subq_as_ctx.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT sub.user_ctx AS ctx FROM (SELECT user_ctx FROM events) sub |> SELECT ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0)

	assertEmptyDefLocation(t, eng, "file:///i13_s1_pipe_e_user_ctx.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events e |> SELECT e.user_ctx |> SELECT user_ctx."+star+" |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_pipe_e_user_ctx.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events e |> SELECT e.user_ctx |> SELECT user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s1_pipe_alias_ctx.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> SELECT user_ctx AS ctx |> SELECT ctx."+star+" |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_pipe_alias_ctx.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> SELECT user_ctx AS ctx |> SELECT ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s1_pipe_e_star_user_ctx.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events e |> SELECT e."+star+" |> SELECT user_ctx."+star+" |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_pipe_e_star_user_ctx.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events e |> SELECT e."+star+" |> SELECT user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s1_pipe_extend_ctx.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> EXTEND user_ctx AS ctx |> SELECT ctx."+star+" |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_pipe_extend_ctx.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> EXTEND user_ctx AS ctx |> SELECT ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s1_pipe_if_then_user_ctx.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> IF TRUE THEN (|> SELECT user_ctx."+star+") ELSE (|> SELECT 3 AS other) |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_pipe_if_then_user_ctx.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> IF TRUE THEN (|> SELECT user_ctx."+star+") ELSE (|> SELECT 3 AS other) |> ORDER BY user_id + (", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s1_pivot_p_user_ctx.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> PIVOT (COUNT("+star+") FOR status IN ('ok' AS a)) AS p |> SELECT p.user_ctx."+star+" |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_pivot_p_user_ctx.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> PIVOT (COUNT("+star+") FOR status IN ('ok' AS a)) AS p |> SELECT p.user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s1_rename_user_ctx_ctx.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> RENAME user_ctx AS ctx |> SELECT ctx."+star+" |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_rename_user_ctx_ctx.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> RENAME user_ctx AS ctx |> SELECT ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0)

	assertDefLocation(t, eng, "file:///i13_s2_sel_1_as_user_ctx_dotstar_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> SELECT 1 AS user_ctx |> SELECT user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s2_sel_1_as_user_ctx_dotted_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> SELECT 1 AS user_ctx |> SELECT user_ctx.user_id |> ORDER BY user_id + (", "user_id +", 0, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s2_agg_1_as_user_ctx_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> AGGREGATE COUNT("+star+") AS user_ctx |> SELECT user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s1_replace_1_as_user_ctx_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> SELECT "+star+" REPLACE(1 AS user_ctx) |> SELECT user_ctx."+star+" |> ORDER BY (user_id", "user_id", 1, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s2_replace_1_as_user_ctx_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> SELECT "+star+" REPLACE(1 AS user_ctx) |> SELECT user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s1_rename_x_as_user_ctx_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> SELECT 1 AS x |> RENAME x AS user_ctx |> SELECT user_ctx."+star+" |> ORDER BY (user_id", "user_id", 1, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s2_rename_x_as_user_ctx_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> SELECT 1 AS x |> RENAME x AS user_ctx |> SELECT user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s1_rename_away_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> RENAME user_ctx AS ctx |> SELECT user_ctx."+star+" |> ORDER BY (user_id", "user_id", 1, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s2_rename_away_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> RENAME user_ctx AS ctx |> SELECT user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s1_drop_user_ctx_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> DROP user_ctx |> SELECT user_ctx."+star+" |> ORDER BY (user_id", "user_id", 1, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s2_drop_user_ctx_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> DROP user_ctx |> SELECT user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s2_if_else_1_as_user_ctx_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> IF TRUE THEN (|> SELECT 1 AS user_ctx) ELSE (|> SELECT 2 AS user_ctx) |> SELECT user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0, "user_id INT64", 0)

	// 3. Stage 2 UNION ALL + FROM (subquery) cross-branch isolation.
	assertDefLocation(t, eng, "file:///i13_s0_union_branch2_order_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT "+star+" FROM (SELECT 1 AS a FROM events WHERE event_date = '2026-01-01') s1 UNION ALL SELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 ORDER BY rn;", "rn;", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s1_union_branch2_order_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT "+star+" FROM (SELECT 1 AS a FROM events) s1 UNION ALL SELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 ORDER BY (rn", "rn", 2, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s2_union_branch2_order_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT "+star+" FROM (SELECT 1 AS a FROM events) s1 UNION ALL SELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s2_union_branch2_pipe_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT "+star+" FROM (SELECT 1 AS a FROM events) s1 UNION ALL SELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 |> ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s0_union_branch2_groupby.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS a FROM events WHERE event_date = '2026-01-01' UNION ALL SELECT MAX(b) FROM (SELECT 1 AS rn, 2 AS b FROM users) s2 GROUP BY rn;", "rn;", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s1_union_branch2_groupby.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS a FROM events UNION ALL SELECT MAX(b) FROM (SELECT 1 AS rn, 2 AS b FROM users) s2 GROUP BY (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_union_branch2_groupby.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS a FROM events UNION ALL SELECT MAX(b) FROM (SELECT 1 AS rn, 2 AS b FROM users) s2 GROUP BY rn + (", "rn +", 0)

	// 4. Stage 2 unclosed WINDOW w AS ( clamp at queryEndIdx.
	assertDefLocation(t, eng, "file:///i13_s2_unclosed_window_next_stmt_pos.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT tenant_id, SUM(1) OVER w FROM events WINDOW w AS (ORDER BY (;\nSELECT status;", "status;", 0, "status INT64", 0)

	// 5. GROUP BY scalar subqueries (FROM cols visible, SELECT aliases excluded, nested parens/calls).
	assertEmptyDefLocation(t, eng, "file:///i13_s0_gb_subq_from_col.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE event_date = '2026-01-01' GROUP BY (SELECT 1 FROM users ORDER BY status LIMIT 1);", "status LIMIT", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s1_gb_subq_from_col.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT 1 FROM events GROUP BY (SELECT 1 FROM users ORDER BY (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_gb_subq_from_col.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT 1 FROM events GROUP BY (SELECT 1 FROM users ORDER BY status + (", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s0_gb_subq_pipe_agg.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> AGGREGATE COUNT("+star+") GROUP BY (SELECT 1 FROM users ORDER BY status LIMIT 1);", "status LIMIT", 0)
	assertEmptyDefLocation(t, eng, "file:///i13_s1_gb_subq_pipe_agg.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> AGGREGATE COUNT("+star+") GROUP BY (SELECT 1 FROM users ORDER BY (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i13_s2_gb_subq_pipe_agg.sql", "DECLARE status INT64 DEFAULT 1;\nFROM events |> AGGREGATE COUNT("+star+") GROUP BY (SELECT 1 FROM users ORDER BY status + (", "status +", 0)
	assertDefLocation(t, eng, "file:///i13_s1_gb_subq_sel_alias_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY (SELECT 1 FROM users ORDER BY (rn", "rn", 2, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s2_gb_nested_paren_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY (1 + (SELECT 1 FROM users ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s2_gb_nested_closed_after_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY (1 + (SELECT 1 FROM users ORDER BY rn + 1 LIMIT 1)) + (", "rn +", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s2_gb_coalesce_pos.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY COALESCE(1, (SELECT 1 FROM users ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i13_s2_gb_nested_unclosed_next_stmt_pos.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT 1 FROM events GROUP BY (1 + (SELECT 1 FROM users ORDER BY (;\nSELECT status;", "status;", 0, "status INT64", 0)

	// 6. matchCTEInWithClause entry-order & queryHasNonSelfBranch.
	assertEmptyDefLocation(t, eng, "file:///i13_s0_nonrec_self_cte_from.sql", "WITH events AS (SELECT status FROM events WHERE event_date = '2026-01-01') SELECT status FROM events;", "events WHERE", 0)
	assertDefLocation(t, eng, "file:///i13_s0_rec_self_cte_from.sql", "WITH RECURSIVE events AS (SELECT 1 AS status UNION ALL SELECT status FROM events) SELECT status FROM events;", "events) SELECT", 0, "events AS", 0)
}

func TestRegressionDefinitionIter14(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	star := "*"

	// 1. * REPLACE (<struct_expr> AS <col>) STRUCT provenance in Stage 0, Stage 1, and Stage 2.
	assertEmptyDefLocation(t, eng, "file:///i14_s0_pipe_rep_struct.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM (SELECT 1 AS ctx, user_ctx AS u FROM events WHERE event_date = '2026-01-01') |> SELECT "+star+" REPLACE (u AS ctx) |> SELECT ctx."+star+" |> ORDER BY user_id;", "user_id;", 0)
	assertEmptyDefLocation(t, eng, "file:///i14_s1_pipe_rep_struct.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM (SELECT 1 AS ctx, user_ctx AS u FROM events) |> SELECT "+star+" REPLACE (u AS ctx) |> SELECT ctx."+star+" |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i14_s2_pipe_rep_struct.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM (SELECT 1 AS ctx, user_ctx AS u FROM events) |> SELECT "+star+" REPLACE (u AS ctx) |> SELECT ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0)

	assertEmptyDefLocation(t, eng, "file:///i14_s0_cte_rep_struct.sql", "DECLARE user_id INT64 DEFAULT 1;\nWITH c AS (SELECT "+star+" REPLACE (user_ctx AS status) FROM events WHERE event_date = '2026-01-01') SELECT c.status."+star+" FROM c |> ORDER BY user_id;", "user_id;", 0)
	assertEmptyDefLocation(t, eng, "file:///i14_s1_cte_rep_struct.sql", "DECLARE user_id INT64 DEFAULT 1;\nWITH c AS (SELECT "+star+" REPLACE (user_ctx AS status) FROM events) SELECT c.status."+star+" FROM c |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i14_s2_cte_rep_struct.sql", "DECLARE user_id INT64 DEFAULT 1;\nWITH c AS (SELECT "+star+" REPLACE (user_ctx AS status) FROM events) SELECT c.status."+star+" FROM c |> ORDER BY user_id + (", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i14_s1_cte_rep_struct_col.sql", "DECLARE user_id INT64 DEFAULT 1;\nWITH c AS (SELECT "+star+" REPLACE (user_ctx AS status) FROM events) SELECT c.status.user_id FROM c |> ORDER BY (user_id", "user_id", 2)

	assertEmptyDefLocation(t, eng, "file:///i14_s0_sel_rep_struct.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT "+star+" REPLACE (user_ctx AS status) FROM events WHERE event_date = '2026-01-01' |> SELECT status."+star+" |> ORDER BY user_id;", "user_id;", 0)
	assertEmptyDefLocation(t, eng, "file:///i14_s1_sel_rep_struct.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT "+star+" REPLACE (user_ctx AS status) FROM events |> SELECT status."+star+" |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i14_s2_sel_rep_struct.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT "+star+" REPLACE (user_ctx AS status) FROM events |> SELECT status."+star+" |> ORDER BY user_id + (", "user_id +", 0)

	assertEmptyDefLocation(t, eng, "file:///i14_s0_dotstar_rep_struct.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT e."+star+" REPLACE (e.user_ctx AS status) FROM events e WHERE e.event_date = '2026-01-01' |> SELECT status."+star+" |> ORDER BY user_id;", "user_id;", 0)
	assertEmptyDefLocation(t, eng, "file:///i14_s1_dotstar_rep_struct.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT e."+star+" REPLACE (e.user_ctx AS status) FROM events e |> SELECT status."+star+" |> ORDER BY (user_id", "user_id", 1)
	assertEmptyDefLocation(t, eng, "file:///i14_s2_dotstar_rep_struct.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT e."+star+" REPLACE (e.user_ctx AS status) FROM events e |> SELECT status."+star+" |> ORDER BY user_id + (", "user_id +", 0)

	assertEmptyDefLocation(t, eng, "file:///i14_s2_subq_rep_struct.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT "+star+" REPLACE (u AS ctx) FROM (SELECT 1 AS ctx, user_ctx AS u FROM events) sub |> SELECT ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0)

	// Negative controls: non-struct REPLACE (1 AS ...) must NOT propagate structFields.
	assertDefLocation(t, eng, "file:///i14_s0_sel_rep_1_neg.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT "+star+" REPLACE (1 AS user_ctx) FROM events WHERE event_date = '2026-01-01' |> SELECT user_ctx."+star+" |> ORDER BY user_id;", "user_id;", 0, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i14_s1_sel_rep_1_neg.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT "+star+" REPLACE (1 AS user_ctx) FROM events |> SELECT user_ctx."+star+" |> ORDER BY (user_id", "user_id", 1, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i14_s2_sel_rep_1_neg.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT "+star+" REPLACE (1 AS user_ctx) FROM events |> SELECT user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i14_s1_cte_rep_1_neg.sql", "DECLARE user_id INT64 DEFAULT 1;\nWITH c AS (SELECT "+star+" REPLACE (1 AS user_ctx) FROM events) SELECT c.user_ctx."+star+" FROM c |> ORDER BY (user_id", "user_id", 1, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i14_s2_cte_rep_1_neg.sql", "DECLARE user_id INT64 DEFAULT 1;\nWITH c AS (SELECT "+star+" REPLACE (1 AS user_ctx) FROM events) SELECT c.user_ctx."+star+" FROM c |> ORDER BY user_id + (", "user_id +", 0, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i14_s1_pipe_rep_1_neg.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM (SELECT 1 AS ctx, user_ctx AS u FROM events) |> SELECT "+star+" REPLACE (1 AS ctx) |> SELECT ctx."+star+" |> ORDER BY (user_id", "user_id", 1, "user_id INT64", 0)

	// 2. Stage 2 parseWithClauseCTEs non-recursive self-named CTE & WITH RECURSIVE control.
	assertEmptyDefLocation(t, eng, "file:///i14_s1_nonrec_self_cte_inner.sql", "WITH events AS (SELECT status FROM events) SELECT status FROM (events", "events", 1)
	assertEmptyDefLocation(t, eng, "file:///i14_s2_nonrec_self_cte_inner.sql", "WITH events AS (SELECT status FROM events) SELECT status FROM events + (", "events)", 0)
	assertDefLocation(t, eng, "file:///i14_s2_nonrec_self_cte_outer.sql", "WITH events AS (SELECT status FROM events) SELECT status FROM events + (", "events +", 0, "events AS", 0)
	assertEmptyDefLocation(t, eng, "file:///i14_s2_nonrec_unclosed_paren_end.sql", "WITH events AS (SELECT status FROM events WHERE status IN ('a')", "events WHERE", 0)
	assertDefLocation(t, eng, "file:///i14_s2_rec_self_cte_inner.sql", "WITH RECURSIVE events AS (SELECT 1 AS status UNION ALL SELECT status FROM events) SELECT status FROM events + (", "events)", 0, "events AS", 0)
	assertDefLocation(t, eng, "file:///i14_s2_rec_self_cte_outer.sql", "WITH RECURSIVE events AS (SELECT 1 AS status UNION ALL SELECT status FROM events) SELECT status FROM events + (", "events +", 0, "events AS", 0)

	// 3. Stage 2 findSelectPostWhereRanges WHERE for FROM (subquery) & negative controls.
	assertEmptyDefLocation(t, eng, "file:///i14_s0_subq_where_rn.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE rn > 0;", "rn >", 0)
	assertEmptyDefLocation(t, eng, "file:///i14_s2_subq_where_rn.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i14_s2_subq_where_corr.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE (SELECT 1 FROM users WHERE rn + (", "rn +", 0)
	assertDefLocation(t, eng, "file:///i14_s0_where_sel_alias_neg.sql", "DECLARE alias_only INT64 DEFAULT 1;\nSELECT 1 AS alias_only FROM (SELECT 2 AS rn FROM users) s2 WHERE alias_only > 0;", "alias_only >", 0, "alias_only INT64", 0)
	assertDefLocation(t, eng, "file:///i14_s2_where_sel_alias_neg.sql", "DECLARE alias_only INT64 DEFAULT 1;\nSELECT 1 AS alias_only FROM (SELECT 2 AS rn FROM users) s2 WHERE alias_only + (", "alias_only +", 0, "alias_only INT64", 0)
	assertDefLocation(t, eng, "file:///i14_s2_where_next_stmt_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE rn = 1;\nSELECT rn;", "rn;", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i14_s2_where_union_b2_order_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS a FROM events UNION ALL SELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE rn = 1 ORDER BY rn + (", "rn +", 0, "rn INT64", 0)

	// 4. Nested parens around subquery + arithmetic in GROUP BY & colliding EXTEND/WINDOW/RENAME.
	assertEmptyDefLocation(t, eng, "file:///i14_s1_gb_nested_arith_outer.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY ((SELECT 1 FROM users ORDER BY 1 LIMIT 1) + (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i14_s2_gb_nested_arith_outer.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY ((SELECT 1 FROM users ORDER BY 1 LIMIT 1) + rn + (", "rn +", 0)
	assertDefLocation(t, eng, "file:///i14_s2_gb_nested_arith_inner_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY ((SELECT 1 FROM users ORDER BY rn + (", "rn +", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i14_s2_gb_nested_setop_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY ((SELECT 1 FROM users ORDER BY rn LIMIT 1) UNION ALL (SELECT 2 FROM users ORDER BY rn LIMIT 1)) + (", "rn LIMIT", 0, "rn INT64", 0)

	assertDefLocation(t, eng, "file:///i14_s0_extend_collide_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS user_ctx |> SELECT user_ctx."+star+" |> ORDER BY user_id;", "user_id;", 0, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i14_s1_extend_collide_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS user_ctx |> SELECT user_ctx."+star+" |> ORDER BY (user_id", "user_id", 1, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i14_s2_extend_collide_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> EXTEND 1 AS user_ctx |> SELECT user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i14_s2_window_collide_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> WINDOW 1 AS user_ctx OVER () |> SELECT user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i14_s0_rename_collide_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> WHERE event_date = '2026-01-01' |> RENAME status AS user_ctx |> SELECT user_ctx."+star+" |> ORDER BY user_id;", "user_id;", 0, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i14_s1_rename_collide_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> RENAME status AS user_ctx |> SELECT user_ctx."+star+" |> ORDER BY (user_id", "user_id", 1, "user_id INT64", 0)
	assertDefLocation(t, eng, "file:///i14_s2_rename_collide_pos.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> RENAME status AS user_ctx |> SELECT user_ctx."+star+" |> ORDER BY user_id + (", "user_id +", 0, "user_id INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i14_s2_rename_swap_struct.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM events |> RENAME status AS user_ctx, user_ctx AS status |> SELECT status."+star+" |> ORDER BY user_id + (", "user_id +", 0)
}

func TestRegressionDefinitionIter15(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	star := "*"

	// 1. Stage 1 ASTWhereClause parity with Stage 0 and Stage 2.
	assertEmptyDefLocation(t, eng, "file:///i15_s0_where_subq_rn.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE rn > 0;", "rn >", 0)
	assertEmptyDefLocation(t, eng, "file:///i15_s1_where_subq_rn.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i15_s2_where_subq_rn.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE rn + (", "rn +", 0)

	assertEmptyDefLocation(t, eng, "file:///i15_s0_where_tbl_status.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND status = 'ok';", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i15_s1_where_tbl_status.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i15_s2_where_tbl_status.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE status + (", "status +", 0)

	assertEmptyDefLocation(t, eng, "file:///i15_s0_where_corr_rn.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE (SELECT 1 FROM events WHERE event_date = '2026-01-01' AND rn > 0) > 0;", "rn >", 0)
	assertEmptyDefLocation(t, eng, "file:///i15_s1_where_corr_rn.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE (SELECT 1 FROM events WHERE (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i15_s2_where_corr_rn.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE (SELECT 1 FROM events WHERE rn + (", "rn +", 0)

	// 2. GROUP BY scalar subquery WHERE: outer SELECT alias invisible, outer FROM col visible.
	assertDefLocation(t, eng, "file:///i15_s0_gb_subq_where_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY (1 + (SELECT 1 FROM users WHERE rn = 1));", "rn =", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i15_s1_gb_subq_where_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY (1 + (SELECT 1 FROM users WHERE (rn", "rn", 2, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i15_s1_gb_subq_where_from_pos.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 AS b FROM events GROUP BY (1 + (SELECT 1 FROM users WHERE (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i15_s2_gb_subq_where_from_pos.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 AS b FROM events GROUP BY (1 + (SELECT 1 FROM users WHERE status + (", "status +", 0)

	// 3. Negative control: SELECT output alias must remain invisible in WHERE.
	assertDefLocation(t, eng, "file:///i15_s0_where_alias_only_neg.sql", "DECLARE alias_only INT64 DEFAULT 1;\nSELECT 1 AS alias_only FROM (SELECT 2 AS rn FROM users) s2 WHERE alias_only > 0;", "alias_only >", 0, "alias_only INT64", 0)
	assertDefLocation(t, eng, "file:///i15_s1_where_alias_only_neg.sql", "DECLARE alias_only INT64 DEFAULT 1;\nSELECT 1 AS alias_only FROM (SELECT 2 AS rn FROM users) s2 WHERE (alias_only", "alias_only", 2, "alias_only INT64", 0)
	assertDefLocation(t, eng, "file:///i15_s2_where_alias_only_neg.sql", "DECLARE alias_only INT64 DEFAULT 1;\nSELECT 1 AS alias_only FROM (SELECT 2 AS rn FROM users) s2 WHERE alias_only + (", "alias_only +", 0, "alias_only INT64", 0)

	// 4. * REPLACE (user_ctx AS status) ... WHERE (status.
	assertEmptyDefLocation(t, eng, "file:///i15_s0_rep_where_status.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT "+star+" REPLACE (user_ctx AS status) FROM events WHERE event_date = '2026-01-01' AND status IS NOT NULL;", "status IS", 0)
	assertEmptyDefLocation(t, eng, "file:///i15_s1_rep_where_status.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT "+star+" REPLACE (user_ctx AS status) FROM events WHERE (status", "status", 2)
	assertEmptyDefLocation(t, eng, "file:///i15_s2_rep_where_status.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT "+star+" REPLACE (user_ctx AS status) FROM events WHERE status + (", "status +", 0)

	// 5. Pipe WHERE, CTE WHERE, IN/EXISTS correlated WHERE, non-correlating FROM subquery WHERE, and DELETE WHERE nil-guard safety.
	assertEmptyDefLocation(t, eng, "file:///i15_s1_pipe_where_status.sql", "DECLARE status STRING DEFAULT 'a';\nFROM events |> WHERE (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i15_s1_pipe_where_corr.sql", "DECLARE status STRING DEFAULT 'a';\nFROM events |> WHERE (SELECT 1 FROM users WHERE (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i15_s1_where_in_corr.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE 1 IN (SELECT 1 FROM events WHERE (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i15_s1_where_exists_corr.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE EXISTS (SELECT 1 FROM events WHERE (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i15_s1_cte_where_rn.sql", "DECLARE rn INT64 DEFAULT 1;\nWITH c AS (SELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE (rn", "rn", 2)
	assertDefLocation(t, eng, "file:///i15_s1_from_subq_no_corr_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM (SELECT 2 AS rn FROM users) s2, (SELECT 1 FROM events WHERE (rn", "rn", 2, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i15_s1_delete_where_safe.sql", "DELETE FROM events WHERE (status", "status", 0)
}

func TestRegressionDefinitionIter16(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)

	// 1. Nested ASTQuery / ASTSetOperation unwrapping (LIKE ANY, = ANY, UNION ALL inside EXISTS/LIKE ANY).
	assertEmptyDefLocation(t, eng, "file:///i16_s0_like_any_where.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND 'x' LIKE ANY (SELECT 'x' FROM users WHERE status = 'ok');", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_like_any_where_unclosed.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE 'x' LIKE ANY (SELECT 'x' FROM users WHERE (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_like_any_where_trailing.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE 'x' LIKE ANY (SELECT 'x' FROM users WHERE status = 'ok') AND (1=1", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i16_s2_like_any_where.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE 'x' LIKE ANY (SELECT 'x' FROM users WHERE status + (", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_like_any_order.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE 'x' LIKE ANY (SELECT 'x' FROM users ORDER BY (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i16_s0_gb_like_any_from.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 AS b FROM events WHERE event_date = '2026-01-01' GROUP BY ('x' LIKE ANY (SELECT 'x' FROM users WHERE status = 'ok'));", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_gb_like_any_from.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 AS b FROM events GROUP BY ('x' LIKE ANY (SELECT 'x' FROM users WHERE (status", "status", 1)
	assertDefLocation(t, eng, "file:///i16_s1_gb_like_any_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY ('x' LIKE ANY (SELECT 'x' FROM users WHERE (rn", "rn", 2, "rn INT64", 0)

	assertEmptyDefLocation(t, eng, "file:///i16_s1_quant_any_closed.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE 1 = ANY (SELECT 1 FROM events WHERE (rn));", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_quant_any_unclosed.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE 1 = ANY (SELECT 1 FROM events WHERE (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i16_s2_quant_any.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE 1 = ANY (SELECT 1 FROM events WHERE rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_gb_quant_any_from.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 AS b FROM events GROUP BY (1 = ANY (SELECT 1 FROM users WHERE (status", "status", 1)
	assertDefLocation(t, eng, "file:///i16_s1_gb_quant_any_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY (1 = ANY (SELECT 1 FROM users WHERE (rn", "rn", 2, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_quant_any_order.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE 1 = ANY (SELECT 1 FROM users ORDER BY (status", "status", 1)

	assertEmptyDefLocation(t, eng, "file:///i16_s0_exists_union_b2.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE EXISTS (SELECT 1 FROM users UNION ALL SELECT 1 FROM events WHERE event_date = '2026-01-01' AND rn > 0);", "rn >", 0)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_exists_union_b2.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE EXISTS (SELECT 1 FROM users UNION ALL SELECT 1 FROM events WHERE (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i16_s2_exists_union_b2.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE EXISTS (SELECT 1 FROM users UNION ALL SELECT 1 FROM events WHERE rn + (", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_exists_union_b1.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM users WHERE (status) UNION ALL SELECT 2) AND (1=1", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_like_any_union_b2.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE 'x' LIKE ANY (SELECT 'x' FROM users UNION ALL SELECT 'x' FROM events WHERE (status", "status", 1)

	// 2. Nested ASTTableSubquery inside expression subqueries (positive correlation & sibling non-correlation).
	assertEmptyDefLocation(t, eng, "file:///i16_s0_exists_nested_from_subq.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (SELECT 1 FROM (SELECT 1 FROM users WHERE status = 'ok'));", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_exists_nested_from_subq.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM (SELECT 1 FROM users WHERE (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i16_s2_exists_nested_from_subq.sql", "DECLARE status INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM (SELECT 1 FROM users WHERE status + (", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_scalar_nested_from_subq.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS b FROM (SELECT 2 AS rn FROM users) s2 WHERE (SELECT 1 FROM (SELECT 1 FROM events WHERE (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_exists_nested_from_order.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM (SELECT 1 FROM users ORDER BY (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_exists_nested_2deep_subq.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM (SELECT 1 FROM (SELECT 1 FROM users WHERE (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_exists_nested_union_subq.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM (SELECT 1 FROM users UNION ALL SELECT 1 FROM users WHERE (status", "status", 1)

	// 3. selectAncestorAliasRef fromClauseHasColumn (SELECT-list subqueries, ARRAY_AGG WHERE, JOIN ON, bare SELECT col).
	assertEmptyDefLocation(t, eng, "file:///i16_s0_select_subq.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT (SELECT 1 FROM users WHERE status = 'ok') FROM events WHERE event_date = '2026-01-01';", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_select_subq.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT (SELECT 1 FROM users WHERE (status)) FROM events WHERE (1=1", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i16_s2_pipe_select_subq.sql", "DECLARE status STRING DEFAULT 'a';\nFROM events |> SELECT (SELECT 1 FROM users WHERE status + (", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_select_bare_col.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT (status) FROM events WHERE (1=1", "status", 1)
	assertDefLocation(t, eng, "file:///i16_s1_select_alias_only_neg.sql", "DECLARE alias_only INT64 DEFAULT 1;\nSELECT 1 AS alias_only, (alias_only) FROM events WHERE (1=1", "alias_only)", 0, "alias_only INT64", 0)

	assertEmptyDefLocation(t, eng, "file:///i16_s0_agg_where.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT ARRAY_AGG(1 WHERE status = 'ok') FROM events WHERE event_date = '2026-01-01';", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_agg_where_closed.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT ARRAY_AGG(1 WHERE (status)) FROM events WHERE (1=1", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_pipe_agg_where_unclosed.sql", "DECLARE status STRING DEFAULT 'a';\nFROM events |> SELECT ARRAY_AGG(1 WHERE (status", "status", 1)
	assertDefLocation(t, eng, "file:///i16_s0_agg_where_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn, ARRAY_AGG(1 WHERE rn = 1) FROM events WHERE event_date = '2026-01-01';", "rn =", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i16_s1_agg_where_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn, ARRAY_AGG(1 WHERE (rn)) FROM events WHERE (1=1", "rn))", 0, "rn INT64", 0)

	assertEmptyDefLocation(t, eng, "file:///i16_s0_join_on_subq.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events e JOIN users u ON (SELECT 1 FROM users u2 WHERE status = 'ok') = 1 WHERE e.event_date = '2026-01-01';", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_join_on_subq.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events e JOIN users u ON (SELECT 1 FROM users u2 WHERE (status)) = 1 WHERE (1=1", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i16_s1_join_on_exists.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events e JOIN users u ON EXISTS (SELECT 1 FROM users u2 WHERE (status)) WHERE (1=1", "status", 1)

	// 4. Preserved negative controls: sibling FROM items, sibling inside EXISTS, WITH CTE inside EXISTS, GROUP BY SELECT alias.
	assertDefLocation(t, eng, "file:///i16_s1_from_sibling_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM (SELECT 2 AS rn FROM users) s2, (SELECT 1 FROM events WHERE (rn)) sub WHERE (1=1", "rn))", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i16_s1_exists_sibling_from_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM (SELECT 2 AS rn FROM users) s2, (SELECT 1 FROM users WHERE (rn)) s3)", "rn))", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i16_s1_exists_sibling_unclosed_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM (SELECT 2 AS rn FROM users) s2, (SELECT 1 FROM users WHERE (rn", "rn", 2, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i16_s1_exists_sibling_join_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM (SELECT 2 AS rn FROM users) s2 JOIN (SELECT 1 FROM users WHERE (rn)) s3 ON TRUE) AND (1=1", "rn))", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i16_s1_exists_with_cte_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM (SELECT 2 AS rn FROM users) s2 WHERE EXISTS (WITH c AS (SELECT 1 FROM events WHERE (rn)) SELECT 1 FROM c)", "rn))", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i16_s1_gb_subq_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY (1 + (SELECT 1 FROM users WHERE (rn))", "rn))", 0, "rn INT64", 0)
}

func TestRegressionDefinitionIter17(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	star := "*"

	// 1. ASTGroupBy, ASTHaving, and ASTQualify inside expression subqueries (Stage 0/1/2 parity).
	assertEmptyDefLocation(t, eng, "file:///i17_s0_exists_having.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (SELECT 1 FROM users GROUP BY user_id HAVING status = 'ok');", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_having_closed.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM users GROUP BY user_id HAVING (status));", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_having_unclosed.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM users GROUP BY user_id HAVING (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i17_s2_exists_having.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM users GROUP BY user_id HAVING status + (", "status +", 0)

	assertEmptyDefLocation(t, eng, "file:///i17_s0_in_having.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND 1 IN (SELECT 1 FROM users GROUP BY user_id HAVING status = 'ok');", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_in_having.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE 1 IN (SELECT 1 FROM users GROUP BY user_id HAVING (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i17_s0_exists_nested_having.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (SELECT 1 FROM (SELECT 1 FROM users GROUP BY user_id HAVING status = 'ok'));", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_nested_having.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM (SELECT 1 FROM users GROUP BY user_id HAVING (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i17_s2_exists_nested_having.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM (SELECT 1 FROM users GROUP BY user_id HAVING status + (", "status +", 0)

	assertEmptyDefLocation(t, eng, "file:///i17_s0_exists_groupby_col.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (SELECT 1 FROM users GROUP BY status);", "status);", 0)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_groupby_col.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM users GROUP BY (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i17_s2_exists_groupby_col.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM users GROUP BY status + (", "status +", 0)

	assertEmptyDefLocation(t, eng, "file:///i17_s0_exists_groupby_subq.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (SELECT 1 FROM users GROUP BY (SELECT 1 FROM users u2 WHERE status = 'ok'));", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_groupby_subq.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM users GROUP BY (SELECT 1 FROM users u2 WHERE (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i17_s0_exists_nested_gb_subq.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (SELECT 1 FROM users GROUP BY (1 + (SELECT 1 FROM users u2 WHERE status = 'ok')));", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_nested_gb_subq.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM users GROUP BY (1 + (SELECT 1 FROM users u2 WHERE (status", "status", 1)

	assertEmptyDefLocation(t, eng, "file:///i17_s0_exists_qualify.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (SELECT 1 FROM users QUALIFY ROW_NUMBER() OVER () = 1 AND status = 'ok');", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_qualify_closed.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM users QUALIFY ROW_NUMBER() OVER () = 1 AND (status));", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_qualify_unclosed.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM users QUALIFY ROW_NUMBER() OVER () = 1 AND (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i17_s0_scalar_qualify.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND (SELECT 1 FROM users QUALIFY ROW_NUMBER() OVER () = 1 AND status = 'ok') = 1;", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_scalar_qualify.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE (SELECT 1 FROM users QUALIFY ROW_NUMBER() OVER () = 1 AND (status)) = 1 AND (1=1", "status", 1)

	// 2. Pipe operators (WHERE, SELECT, EXTEND, ORDER BY, AGGREGATE GROUP BY / SUM) inside expression subqueries.
	assertEmptyDefLocation(t, eng, "file:///i17_s0_exists_pipe_where.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (FROM users |> WHERE status = 'ok');", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_pipe_where.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM users |> WHERE (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i17_s2_exists_pipe_where.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM users |> WHERE status + (", "status +", 0)

	assertEmptyDefLocation(t, eng, "file:///i17_s0_exists_pipe_select.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (FROM users |> SELECT status);", "status);", 0)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_pipe_select.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM users |> SELECT (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i17_s2_exists_pipe_select.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM users |> SELECT status + (", "status +", 0)

	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_pipe_extend.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM users |> EXTEND (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i17_s2_exists_pipe_extend.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM users |> EXTEND status + (", "status +", 0)

	assertEmptyDefLocation(t, eng, "file:///i17_s0_exists_pipe_order.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (FROM users |> ORDER BY status);", "status);", 0)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_pipe_order.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM users |> ORDER BY (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i17_s2_exists_pipe_order.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM users |> ORDER BY status + (", "status +", 0)

	assertEmptyDefLocation(t, eng, "file:///i17_s0_exists_pipe_agg_gb.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (FROM users |> AGGREGATE COUNT("+star+") GROUP BY status);", "status);", 0)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_pipe_agg_gb.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM users |> AGGREGATE COUNT("+star+") GROUP BY (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_pipe_agg_expr.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM users |> AGGREGATE MAX(status", "status", 1)

	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_nested_pipe_where.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM (FROM users |> WHERE (status)) s3)", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_nested_pipe_order.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM (FROM users |> ORDER BY (status)) s3)", "status", 1)

	// 3. Positive controls: outer HAVING alias exposure & inner alias visibility.
	assertEmptyDefLocation(t, eng, "file:///i17_s0_outer_having_exists_having_alias.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE event_date = '2026-01-01' GROUP BY event_date HAVING EXISTS (SELECT 1 FROM users GROUP BY user_id HAVING rn = 1);", "rn =", 0)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_outer_having_exists_having_alias.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY event_date HAVING EXISTS (SELECT 1 FROM users GROUP BY user_id HAVING (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_inner_having_alias.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 AS rn FROM users GROUP BY user_id HAVING (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_inner_gb_alias.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 AS rn FROM users GROUP BY (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_inner_qualify_alias.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 AS rn FROM users QUALIFY ROW_NUMBER() OVER () = 1 AND (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i17_s1_exists_inner_pipe_alias.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE EXISTS (FROM users |> SELECT 1 AS rn |> WHERE (rn", "rn", 2)

	// 4. Negative controls: outer SELECT aliases remain hidden inside subqueries; sibling FROM / GROUP BY isolation preserved.
	assertDefLocation(t, eng, "file:///i17_s0_exists_having_outer_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE event_date = '2026-01-01' AND EXISTS (SELECT 1 FROM users GROUP BY user_id HAVING rn = 1);", "rn =", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i17_s1_exists_having_outer_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE EXISTS (SELECT 1 FROM users GROUP BY user_id HAVING (rn", "rn", 2, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i17_s0_exists_gb_outer_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE event_date = '2026-01-01' AND EXISTS (SELECT 1 FROM users GROUP BY rn);", "rn);", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i17_s1_exists_gb_outer_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE EXISTS (SELECT 1 FROM users GROUP BY (rn", "rn", 2, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i17_s1_exists_qualify_outer_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE EXISTS (SELECT 1 FROM users QUALIFY ROW_NUMBER() OVER () = 1 AND (rn", "rn", 2, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i17_s0_exists_pipe_where_outer_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE event_date = '2026-01-01' AND EXISTS (FROM users |> WHERE rn = 1);", "rn =", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i17_s1_exists_pipe_where_outer_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE EXISTS (FROM users |> WHERE (rn", "rn", 2, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i17_s1_exists_pipe_select_outer_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE EXISTS (FROM users |> SELECT (rn", "rn", 2, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i17_s1_exists_pipe_order_outer_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE EXISTS (FROM users |> ORDER BY (rn", "rn", 2, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i17_s1_exists_pipe_agg_outer_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE EXISTS (FROM users |> AGGREGATE COUNT("+star+") GROUP BY (rn", "rn", 2, "rn INT64", 0)

	assertDefLocation(t, eng, "file:///i17_s1_gb_exists_gb_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events GROUP BY (EXISTS (SELECT 1 FROM users GROUP BY (rn)))", "rn)))", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i17_s1_from_sibling_gb_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM (SELECT 2 AS rn FROM users) s2, (SELECT 1 FROM users GROUP BY (rn)) s3", "rn))", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i17_s1_from_sibling_having_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM (SELECT 2 AS rn FROM users) s2, (SELECT 1 FROM users GROUP BY user_id HAVING (rn)) s3", "rn))", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i17_s1_from_sibling_pipe_where_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM (SELECT 2 AS rn FROM users) s2, (FROM users |> WHERE (rn)) s3", "rn))", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i17_s1_from_sibling_pipe_order_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM (SELECT 2 AS rn FROM users) s2, (FROM users |> ORDER BY (rn)) s3", "rn))", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i17_s1_exists_sibling_having_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM (SELECT 2 AS rn FROM users) s2, (SELECT 1 FROM users GROUP BY user_id HAVING (rn)) s3)", "rn))", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i17_s1_exists_sibling_pipe_where_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM (SELECT 2 AS rn FROM users) s2, (FROM users |> WHERE (rn)) s3)", "rn))", 0, "rn INT64", 0)
}

func TestRegressionDefinitionIter18(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)

	// 1. ASTPipeSetOperation inside expression subqueries (Stage 0/1/2 parity + negative controls).
	assertEmptyDefLocation(t, eng, "file:///i18_s0_exists_pipe_union_sql.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (FROM users |> UNION ALL (SELECT 1, 'x' FROM users WHERE status = 'ok'));", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s1_exists_pipe_union_sql.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM users |> UNION ALL (SELECT 1, 'x' FROM users WHERE (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i18_s2_exists_pipe_union_sql.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM users |> UNION ALL (SELECT 1, 'x' FROM users WHERE status + (", "status +", 0)

	assertEmptyDefLocation(t, eng, "file:///i18_s0_exists_pipe_union_pipe.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (FROM users |> UNION ALL (FROM users |> WHERE status = 'ok'));", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s1_exists_pipe_union_pipe.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM users |> UNION ALL (FROM users |> WHERE (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i18_s2_exists_pipe_union_pipe.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM users |> UNION ALL (FROM users |> WHERE status + (", "status +", 0)

	assertEmptyDefLocation(t, eng, "file:///i18_s0_exists_pipe_union_2pipe.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (FROM users |> UNION ALL (FROM users |> SELECT user_id, country |> WHERE status = 'ok'));", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s1_exists_pipe_union_2pipe.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM users |> UNION ALL (FROM users |> SELECT user_id, country |> WHERE (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i18_s1_exists_pipe_union_order.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM users |> UNION ALL (SELECT 1, 'x' FROM users ORDER BY (status", "status", 1)

	// Negative controls for ASTPipeSetOperation: predecessor pipe aliases do not leak, outer SELECT aliases stay hidden, top-level does not correlate.
	assertDefLocation(t, eng, "file:///i18_s0_exists_pipe_union_pred_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (FROM users |> SELECT 1 AS rn |> UNION ALL (SELECT 1 FROM users WHERE rn = 1));", "rn =", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i18_s1_exists_pipe_union_pred_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE EXISTS (FROM users |> SELECT 1 AS rn |> UNION ALL (SELECT 1 FROM users WHERE (rn", "rn", 2, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i18_s0_exists_pipe_union_outer_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE event_date = '2026-01-01' AND EXISTS (FROM users |> UNION ALL (FROM users |> WHERE rn = 1));", "rn =", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i18_s1_exists_pipe_union_outer_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE EXISTS (FROM users |> UNION ALL (FROM users |> WHERE (rn", "rn", 2, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i18_s0_top_pipe_union_neg.sql", "DECLARE status STRING DEFAULT 'a';\nFROM users |> UNION ALL (FROM users |> WHERE status = 'ok');", "status =", 0, "status STRING", 0)
	assertDefLocation(t, eng, "file:///i18_s1_top_pipe_union_neg.sql", "DECLARE status STRING DEFAULT 'a';\nFROM users |> UNION ALL (FROM users |> WHERE (status", "status", 1, "status STRING", 0)

	// 2. ASTPipeJoin ON clause RHS table/subquery columns (Stage 0/1/2 parity + controls).
	assertEmptyDefLocation(t, eng, "file:///i18_s0_pipe_join_rhs.sql", "DECLARE status STRING DEFAULT 'a';\nFROM users u |> JOIN events e ON e.event_date = '2026-01-01' AND status = 'ok';", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s1_pipe_join_rhs.sql", "DECLARE status STRING DEFAULT 'a';\nFROM users u |> JOIN events e ON (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i18_s2_pipe_join_rhs.sql", "DECLARE status STRING DEFAULT 'a';\nFROM users u |> JOIN events e ON status + (", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s1_pipe_join_rhs_closed_later_err.sql", "DECLARE status STRING DEFAULT 'a';\nFROM users u |> JOIN events e ON status = 'ok' |> WHERE (1=1", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s1_pipe_join_rhs_extra_paren.sql", "DECLARE status STRING DEFAULT 'a';\nFROM users u |> JOIN events e ON (status))", "status", 1)

	assertEmptyDefLocation(t, eng, "file:///i18_s0_pipe_join_rhs_subq.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM users u |> JOIN (SELECT 2 AS rn FROM events) s2 ON rn = 2;", "rn =", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s1_pipe_join_rhs_subq.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM users u |> JOIN (SELECT 2 AS rn FROM events) s2 ON (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i18_s2_pipe_join_rhs_subq.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM users u |> JOIN (SELECT 2 AS rn FROM events) s2 ON rn + (", "rn +", 0)

	assertEmptyDefLocation(t, eng, "file:///i18_s0_exists_pipe_join_rhs.sql", "DECLARE country STRING DEFAULT 'US';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (FROM events e |> JOIN users u ON e.user_ctx.user_id = u.user_id AND country = 'US');", "country =", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s1_exists_pipe_join_rhs.sql", "DECLARE country STRING DEFAULT 'US';\nSELECT 1 FROM events WHERE EXISTS (FROM events e |> JOIN users u ON e.user_ctx.user_id = u.user_id AND (country", "country", 1)
	assertEmptyDefLocation(t, eng, "file:///i18_s2_exists_pipe_join_rhs.sql", "DECLARE country STRING DEFAULT 'US';\nSELECT 1 FROM events WHERE EXISTS (FROM events e |> JOIN users u ON e.user_ctx.user_id = u.user_id AND country + (", "country +", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s1_exists_pipe_join_rhs_subq.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE EXISTS (FROM users u |> JOIN (SELECT 1 AS rn) s2 ON (rn", "rn", 2)

	// Preserved ASTPipeJoin positive and negative controls.
	assertEmptyDefLocation(t, eng, "file:///i18_s1_pipe_join_pred_col.sql", "DECLARE status STRING DEFAULT 'a';\nFROM events e |> JOIN users u ON (status", "status", 1)
	assertDefLocation(t, eng, "file:///i18_s0_pipe_join_dropped_pred_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM (SELECT 1 AS rn FROM users) s1 |> SELECT 1 AS x |> JOIN (SELECT 1 FROM users) s2 ON rn = 1;", "rn =", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i18_s1_pipe_join_dropped_pred_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM (SELECT 1 AS rn FROM users) s1 |> SELECT 1 AS x |> JOIN (SELECT 1 FROM users) s2 ON (rn", "rn", 2, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i18_s2_pipe_join_dropped_pred_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nFROM (SELECT 1 AS rn FROM users) s1 |> SELECT 1 AS x |> JOIN (SELECT 1 FROM users) s2 ON rn + (", "rn +", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i18_s0_pipe_join_rhs_subq_pred_neg.sql", "DECLARE status STRING DEFAULT 'a';\nFROM events e |> JOIN (SELECT 1 FROM users WHERE status = 'ok') s ON TRUE;", "status =", 0, "status STRING", 0)
	assertDefLocation(t, eng, "file:///i18_s1_pipe_join_rhs_subq_pred_neg.sql", "DECLARE status STRING DEFAULT 'a';\nFROM events e |> JOIN (SELECT 1 FROM users WHERE (status)) s ON (1=1", "status", 1, "status STRING", 0)
	assertDefLocation(t, eng, "file:///i18_s2_pipe_join_rhs_subq_pred_neg.sql", "DECLARE status STRING DEFAULT 'a';\nFROM events e |> JOIN (SELECT 1 FROM users WHERE status + ()) s ON 1=1", "status +", 0, "status STRING", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s0_exists_pipe_join_rhs_subq_outer.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (FROM users u |> JOIN (SELECT 1 FROM users u2 WHERE status = 'ok') s ON TRUE);", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s1_exists_pipe_join_rhs_subq_outer.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM users u |> JOIN (SELECT 1 FROM users u2 WHERE (status)) s ON (1=1", "status", 1)
	assertDefLocation(t, eng, "file:///i18_s0_exists_pipe_join_rhs_subq_pred_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (FROM users u |> SELECT 1 AS rn |> JOIN (SELECT 1 FROM users u2 WHERE rn = 1) s ON TRUE);", "rn =", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i18_s1_exists_pipe_join_rhs_subq_pred_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE EXISTS (FROM users u |> SELECT 1 AS rn |> JOIN (SELECT 1 FROM users u2 WHERE (rn)) s ON (1=1", "rn))", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i18_s2_exists_pipe_join_rhs_subq_pred_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE EXISTS (FROM users u |> SELECT 1 AS rn |> JOIN (SELECT 1 FROM users u2 WHERE rn + ()) s ON 1=1", "rn +", 0, "rn INT64", 0)

	// 3. ASTWindowDefinition inside expression subqueries (Stage 0/1/2 parity + negative controls).
	assertEmptyDefLocation(t, eng, "file:///i18_s0_exists_windef_qualify_part.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (SELECT 1 FROM users QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (PARTITION BY status));", "status));", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s1_exists_windef_qualify_part.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM users QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (PARTITION BY (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i18_s2_exists_windef_qualify_part.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM users QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (PARTITION BY status + (", "status +", 0)

	assertEmptyDefLocation(t, eng, "file:///i18_s0_exists_windef_substr.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (SELECT 1 FROM users QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (PARTITION BY SUBSTR(status, 1)));", "status,", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s1_exists_windef_substr.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM users QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (PARTITION BY SUBSTR(status, 1", "status,", 0)

	assertEmptyDefLocation(t, eng, "file:///i18_s0_exists_windef_order.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (SELECT 1 FROM users QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (ORDER BY status));", "status));", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s1_exists_windef_order.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM users QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (ORDER BY (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i18_s2_exists_windef_order.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM users QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (ORDER BY status + (", "status +", 0)

	// Negative controls for ASTWindowDefinition: top-level isolation and outer SELECT alias invisibility.
	assertDefLocation(t, eng, "file:///i18_s0_top_windef_neg.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (PARTITION BY status);", "status);", 0, "status STRING", 0)
	assertDefLocation(t, eng, "file:///i18_s1_top_windef_neg.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (PARTITION BY (status", "status", 1, "status STRING", 0)
	assertDefLocation(t, eng, "file:///i18_s0_exists_windef_outer_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE event_date = '2026-01-01' AND EXISTS (SELECT 1 FROM users QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (PARTITION BY rn));", "rn));", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i18_s1_exists_windef_outer_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE EXISTS (SELECT 1 FROM users QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (PARTITION BY (rn", "rn", 2, "rn INT64", 0)

	// Check Stage 2 WINDOW ... QUALIFY (WQ_s2_window_qualify) and additional ASTWindowDefinition / ASTPipeJoin / ASTPipeSetOperation controls.
	assertEmptyDefLocation(t, eng, "file:///i18_s2_window_qualify.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (PARTITION BY rn + ();", "rn +", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s0_exists_windef_select_outer_col.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (SELECT 1, ROW_NUMBER() OVER w FROM users WINDOW w AS (PARTITION BY status));", "status));", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s1_exists_windef_select_outer_col.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1, ROW_NUMBER() OVER w FROM users WINDOW w AS (PARTITION BY (status", "status", 1)
	assertDefLocation(t, eng, "file:///i18_s0_exists_windef_select_inner_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE event_date = '2026-01-01' AND EXISTS (SELECT 1 AS rn, ROW_NUMBER() OVER w FROM users WINDOW w AS (PARTITION BY rn));", "rn));", 0, "rn INT64", 0)
	assertDefLocation(t, eng, "file:///i18_s1_exists_windef_select_inner_alias_neg.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 AS rn FROM events WHERE EXISTS (SELECT 1 AS rn, ROW_NUMBER() OVER w FROM users WINDOW w AS (PARTITION BY (rn", "rn", 3, "rn INT64", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s0_exists_windef_qualify_inner_alias.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (SELECT 1 AS rn FROM users QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (PARTITION BY rn));", "rn));", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s1_exists_windef_qualify_inner_alias.sql", "DECLARE rn INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 AS rn FROM users QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (PARTITION BY (rn", "rn", 2)
	assertEmptyDefLocation(t, eng, "file:///i18_s1_exists_nested_windef.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (SELECT 1 FROM (SELECT 1 FROM users QUALIFY ROW_NUMBER() OVER w = 1 WINDOW w AS (PARTITION BY status)) s)", "status))", 0)
	assertEmptyDefLocation(t, eng, "file:///i18_s1_exists_pipe_union_having.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM users |> UNION ALL (SELECT 1, 'x' FROM users GROUP BY user_id HAVING (status", "status", 1)
	assertDefLocation(t, eng, "file:///i18_s1_pipe_join_unnest_rhs_neg.sql", "DECLARE status STRING DEFAULT 'a';\nFROM users u |> JOIN UNNEST(ARRAY(SELECT 1 FROM users u2 WHERE (status))) s ON (1=1", "status", 1, "status STRING", 0)
}

func TestRegressionDefinitionIter19(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)

	// 1. JOIN ... USING (...) and |> JOIN ... USING (...) across Stage 0, Stage 1, and Stage 2.
	assertEmptyDefLocation(t, eng, "file:///i19_s0_sql_join_using.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT 1 FROM users u JOIN (SELECT 1 AS user_id) e USING (user_id);", "user_id);", 0)
	assertEmptyDefLocation(t, eng, "file:///i19_s1_sql_join_using.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT 1 FROM users u JOIN (SELECT 1 AS user_id) e USING (user_id", "user_id", 2)
	assertEmptyDefLocation(t, eng, "file:///i19_s0_pipe_join_using.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM users u |> JOIN (SELECT 1 AS user_id) e USING (user_id);", "user_id);", 0)
	assertEmptyDefLocation(t, eng, "file:///i19_s1_pipe_join_using.sql", "DECLARE user_id INT64 DEFAULT 1;\nFROM users u |> JOIN (SELECT 1 AS user_id) e USING (user_id", "user_id", 2)

	assertEmptyDefLocation(t, eng, "file:///i19_s0_pipe_join_using_where_key.sql", "DECLARE status STRING DEFAULT 'a';\nFROM (SELECT 'ok' AS status) u |> JOIN events e USING (status) |> WHERE status = 'ok';", "status)", 0)
	assertEmptyDefLocation(t, eng, "file:///i19_s0_pipe_join_using_where_expr.sql", "DECLARE status STRING DEFAULT 'a';\nFROM (SELECT 'ok' AS status) u |> JOIN events e USING (status) |> WHERE status = 'ok';", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i19_s1_pipe_join_using_rhs.sql", "DECLARE status STRING DEFAULT 'a';\nFROM (SELECT 'ok' AS status) u |> JOIN events e USING (status", "status", 2)
	assertEmptyDefLocation(t, eng, "file:///i19_s2_pipe_join_using_rhs.sql", "DECLARE status STRING DEFAULT 'a';\nFROM (SELECT 'ok' AS status) u |> JOIN events e USING (status + ()", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i19_s2_pipe_join_using_where_expr.sql", "DECLARE status STRING DEFAULT 'a';\nFROM (SELECT 'ok' AS status) u |> JOIN events e USING (status) |> WHERE status + ()", "status +", 0)

	// 2. EXISTS (... |> JOIN ... USING (...)) across Stage 0, Stage 1, and Stage 2.
	assertEmptyDefLocation(t, eng, "file:///i19_s0_exists_pipe_join_using.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (FROM (SELECT 'ok' AS status) u |> JOIN events e USING (status));", "status));", 0)
	assertEmptyDefLocation(t, eng, "file:///i19_s1_exists_pipe_join_using.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM (SELECT 'ok' AS status) u |> JOIN events e USING (status", "status", 2)
	assertEmptyDefLocation(t, eng, "file:///i19_s2_exists_pipe_join_using.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events WHERE EXISTS (FROM (SELECT 'ok' AS status) u |> JOIN events e USING (status + ()", "status +", 0)

	// 3. Multi-key USING (a, b) across Stage 0, Stage 1, and Stage 2.
	assertEmptyDefLocation(t, eng, "file:///i19_s0_sql_join_using_multi_k1.sql", "DECLARE user_id INT64 DEFAULT 1;\nDECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN (SELECT 1 AS user_id, 'ok' AS status) e USING (user_id, status);", "user_id, status)", 0)
	assertEmptyDefLocation(t, eng, "file:///i19_s0_sql_join_using_multi_k2.sql", "DECLARE user_id INT64 DEFAULT 1;\nDECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN (SELECT 1 AS user_id, 'ok' AS status) e USING (user_id, status);", "status);", 0)
	assertEmptyDefLocation(t, eng, "file:///i19_s0_pipe_join_using_multi_k1.sql", "DECLARE user_id INT64 DEFAULT 1;\nDECLARE status STRING DEFAULT 'a';\nFROM (SELECT 1 AS user_id, 'ok' AS status) u |> JOIN events e USING (user_id, status);", "user_id, status)", 0)
	assertEmptyDefLocation(t, eng, "file:///i19_s0_pipe_join_using_multi_k2.sql", "DECLARE user_id INT64 DEFAULT 1;\nDECLARE status STRING DEFAULT 'a';\nFROM (SELECT 1 AS user_id, 'ok' AS status) u |> JOIN events e USING (user_id, status);", "status);", 0)
	assertEmptyDefLocation(t, eng, "file:///i19_s1_pipe_join_using_multi_k1.sql", "DECLARE user_id INT64 DEFAULT 1;\nDECLARE status STRING DEFAULT 'a';\nFROM (SELECT 1 AS user_id, 'ok' AS status) u |> JOIN events e USING (user_id, status", "user_id, status", 0)
	assertEmptyDefLocation(t, eng, "file:///i19_s1_pipe_join_using_multi_k2.sql", "DECLARE user_id INT64 DEFAULT 1;\nDECLARE status STRING DEFAULT 'a';\nFROM (SELECT 1 AS user_id, 'ok' AS status) u |> JOIN events e USING (user_id, status", "status", 2)
	assertEmptyDefLocation(t, eng, "file:///i19_s2_pipe_join_using_multi_k1.sql", "DECLARE user_id INT64 DEFAULT 1;\nDECLARE status STRING DEFAULT 'a';\nFROM (SELECT 1 AS user_id, 'ok' AS status) u |> JOIN events e USING (user_id, status + ()", "user_id, status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i19_s2_pipe_join_using_multi_k2.sql", "DECLARE user_id INT64 DEFAULT 1;\nDECLARE status STRING DEFAULT 'a';\nFROM (SELECT 1 AS user_id, 'ok' AS status) u |> JOIN events e USING (user_id, status + ()", "status +", 0)

	// 4. Negative controls: DECLARE referenced outside USING (...) when not in table scope resolves to DECLARE at (0, 8).
	assertDefLocation(t, eng, "file:///i19_s0_pipe_using_where_neg.sql", "DECLARE x INT64 DEFAULT 1;\nFROM (SELECT 1 AS user_id) u |> JOIN events e USING (user_id) |> WHERE x = 1;", "x =", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i19_s1_pipe_using_where_neg.sql", "DECLARE x INT64 DEFAULT 1;\nFROM (SELECT 1 AS user_id) u |> JOIN events e USING (user_id) |> WHERE (x", "x", 1, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i19_s2_pipe_using_where_neg.sql", "DECLARE x INT64 DEFAULT 1;\nFROM (SELECT 1 AS user_id) u |> JOIN events e USING (user_id) |> WHERE x + ()", "x +", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i19_s0_sql_using_where_neg.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 FROM users u JOIN events e USING (user_id) WHERE x = 1;", "x =", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i19_s1_sql_using_where_neg.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 FROM users u JOIN events e USING (user_id) WHERE (x", "x", 1, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i19_s2_sql_using_where_neg.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 FROM users u JOIN events e USING (user_id) WHERE x + ()", "x +", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i19_s0_pipe_using_rhs_subq_neg.sql", "DECLARE x INT64 DEFAULT 1;\nFROM users u |> JOIN (SELECT 1 AS user_id FROM events WHERE event_date = '2026-01-01' AND x = 1) e USING (user_id);", "x =", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i19_s0_exists_pipe_using_where_neg.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 FROM events WHERE event_date = '2026-01-01' AND EXISTS (FROM (SELECT 1 AS user_id) u |> JOIN events e USING (user_id) |> WHERE x = 1);", "x =", 0, "x INT64", 0)
}

func TestRegressionDefinitionIter20(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)

	// 1. SQL JOIN ... USING (...) across Stage 0, Stage 1, and Stage 2.
	assertEmptyDefLocation(t, eng, "file:///i20_s0_sql_using_known.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN events e USING (status);", "status);", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s1_sql_using_known.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN events e USING (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_using_known_err.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN events e USING (status + ()", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_using_known_where_err.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u LEFT JOIN events e USING (status) WHERE 1 =", "status)", 0)

	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_using_subq_err.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT 1 FROM users u JOIN (SELECT 1 AS user_id) e USING (user_id + ()", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_using_subq_where_err.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT 1 FROM users u JOIN (SELECT 1 AS user_id) e USING (user_id) WHERE 1 =", "user_id)", 0)

	assertEmptyDefLocation(t, eng, "file:///i20_s0_sql_using_unknown_tbl.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 FROM unknown_a u JOIN unknown_b e USING (x);", "x);", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s1_sql_using_unknown_tbl.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 FROM unknown_a u JOIN unknown_b e USING (x", "x", 1)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_using_unknown_tbl_err.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 FROM unknown_a u JOIN unknown_b e USING (x + ()", "x +", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_using_unknown_tbl_where_err.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 FROM unknown_a u JOIN unknown_b e USING (x) WHERE 1 =", "x)", 0)

	assertEmptyDefLocation(t, eng, "file:///i20_s0_sql_using_unknown_col.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 FROM users u JOIN events e USING (x);", "x);", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s1_sql_using_unknown_col.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 FROM users u JOIN events e USING (x", "x", 1)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_using_unknown_col_err.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 FROM users u JOIN events e USING (x + ()", "x +", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_using_unknown_col_where_err.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 FROM users u JOIN events e USING (x) WHERE 1 =", "x)", 0)

	// Multi-key, backticked, and chained SQL JOIN ... USING (...) across Stage 0/1/2.
	assertEmptyDefLocation(t, eng, "file:///i20_s1_sql_using_multi_k2.sql", "DECLARE user_id INT64 DEFAULT 1;\nDECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN (SELECT 1 AS user_id, 'ok' AS status) e USING (user_id, status", "status", 2)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_using_multi_k1_err.sql", "DECLARE user_id INT64 DEFAULT 1;\nDECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN (SELECT 1 AS user_id, 'ok' AS status) e USING (user_id, status + ()", "user_id, status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_using_multi_k2_err.sql", "DECLARE user_id INT64 DEFAULT 1;\nDECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN (SELECT 1 AS user_id, 'ok' AS status) e USING (user_id, status + ()", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_using_multi_k1_where_err.sql", "DECLARE user_id INT64 DEFAULT 1;\nDECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u FULL JOIN (SELECT 1 AS user_id, 'ok' AS status) e USING (user_id, status) WHERE 1 =", "user_id, status)", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_using_multi_k2_where_err.sql", "DECLARE user_id INT64 DEFAULT 1;\nDECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u FULL JOIN (SELECT 1 AS user_id, 'ok' AS status) e USING (user_id, status) WHERE 1 =", "status)", 0)

	assertEmptyDefLocation(t, eng, "file:///i20_s0_sql_using_backtick.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT 1 FROM users u LEFT JOIN events e USING (`user_id`);", "user_id`);", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s1_sql_using_backtick.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT 1 FROM users u LEFT JOIN events e USING (`user_id`", "user_id`", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_using_backtick_err.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT 1 FROM users u LEFT JOIN events e USING (`user_id` + ()", "user_id` +", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_using_backtick_where_err.sql", "DECLARE user_id INT64 DEFAULT 1;\nSELECT 1 FROM users u LEFT JOIN events e USING (`user_id`) WHERE 1 =", "user_id`)", 0)

	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_using_chained_k2.sql", "DECLARE user_id INT64 DEFAULT 1;\nDECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u INNER JOIN events e USING (user_id) RIGHT JOIN events e2 USING (status) WHERE 1 =", "status)", 0)

	// 2. Pipe |> JOIN ... USING (...) on unknown tables, unknown columns, and LEFT JOIN across Stage 0/1/2.
	assertEmptyDefLocation(t, eng, "file:///i20_s0_pipe_using_unknown_tbl.sql", "DECLARE x INT64 DEFAULT 1;\nFROM unknown_a u |> JOIN unknown_b e USING (x);", "x);", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s1_pipe_using_unknown_tbl.sql", "DECLARE x INT64 DEFAULT 1;\nFROM unknown_a u |> JOIN unknown_b e USING (x", "x", 1)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_pipe_using_unknown_tbl_err.sql", "DECLARE x INT64 DEFAULT 1;\nFROM unknown_a u |> JOIN unknown_b e USING (x + ()", "x +", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_pipe_using_unknown_tbl_where_err.sql", "DECLARE x INT64 DEFAULT 1;\nFROM unknown_a u |> JOIN unknown_b e USING (x) |> WHERE 1 =", "x)", 0)

	assertEmptyDefLocation(t, eng, "file:///i20_s0_pipe_using_unknown_col.sql", "DECLARE x INT64 DEFAULT 1;\nFROM users u |> JOIN events e USING (x);", "x);", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s1_pipe_using_unknown_col.sql", "DECLARE x INT64 DEFAULT 1;\nFROM users u |> JOIN events e USING (x", "x", 1)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_pipe_using_unknown_col_err.sql", "DECLARE x INT64 DEFAULT 1;\nFROM users u |> JOIN events e USING (x + ()", "x +", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_pipe_using_left_unknown_where_err.sql", "DECLARE x INT64 DEFAULT 1;\nFROM users u |> LEFT JOIN events e USING (x) |> WHERE 1 =", "x)", 0)

	// 3. SQL JOIN ... ON ... column resolution across Stage 0, Stage 1, and Stage 2 (including joined subqueries & nested subqueries).
	assertEmptyDefLocation(t, eng, "file:///i20_s0_sql_on_col.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN events e ON status = 'ok';", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s1_sql_on_col.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN events e ON (status", "status", 1)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_on_col_err.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN events e ON status + ()", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_on_col_where_err.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN events e ON status = 'ok' WHERE 1 =", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_on_subq_col_err.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN (SELECT 'ok' AS status) e ON status + ()", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_on_subq_col_where_err.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN (SELECT 'ok' AS status) e ON status = 'ok' WHERE 1 =", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_on_nested_exists_err.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users WHERE EXISTS (SELECT 1 FROM users u JOIN events e ON status + ())", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///i20_s2_sql_on_chained_second_err.sql", "DECLARE country STRING DEFAULT 'US';\nSELECT 1 FROM users u JOIN events e ON status = 'ok' LEFT JOIN users u2 ON country + ()", "country +", 0)

	// 4. Negative controls: non-table identifiers in ON, MERGE ... USING, EXECUTE IMMEDIATE ... USING, RHS subquery isolation, and outer leakage prevention.
	assertDefLocation(t, eng, "file:///i20_s0_sql_on_decl_neg.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 FROM users u JOIN events e ON u.user_id = x;", "x;", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i20_s1_sql_on_decl_neg.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 FROM users u JOIN events e ON (u.user_id = x", "x", 1, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i20_s2_sql_on_decl_err_neg.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 FROM users u JOIN events e ON u.user_id = x + ()", "x +", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i20_s2_sql_on_decl_where_err_neg.sql", "DECLARE x INT64 DEFAULT 1;\nSELECT 1 FROM users u JOIN events e ON u.user_id = x WHERE 1 =", "x WHERE", 0, "x INT64", 0)

	assertDefLocation(t, eng, "file:///i20_s2_sql_on_rhs_subq_isolation_neg.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM events e JOIN users u ON u.user_id = e.tenant_id JOIN (SELECT 1 FROM users u2 WHERE status + ()) s ON 1=1", "status +", 0, "status STRING", 0)
	assertDefLocation(t, eng, "file:///i20_s2_sql_on_pipe_leak_neg.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN events e ON status = 'ok' |> WHERE status + ()", "status +", 0, "status STRING", 0)
	assertDefLocation(t, eng, "file:///i20_s2_sql_on_union_leak_neg.sql", "DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN events e ON status = 'ok' UNION ALL SELECT 1 FROM users u2 WHERE status + ()", "status +", 0, "status STRING", 0)

	assertDefLocation(t, eng, "file:///i20_s0_merge_using_subq_neg.sql", "DECLARE x INT64 DEFAULT 1;\nMERGE users t USING (SELECT x AS user_id) s ON t.user_id = s.user_id WHEN NOT MATCHED THEN INSERT (user_id) VALUES (s.user_id);", "x AS", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i20_s2_merge_using_subq_neg.sql", "DECLARE x INT64 DEFAULT 1;\nMERGE users t USING (SELECT x + () AS user_id) s ON t.user_id = s.user_id WHEN NOT MATCHED THEN INSERT (user_id) VALUES (s.user_id);", "x +", 0, "x INT64", 0)

	assertDefLocation(t, eng, "file:///i20_s0_exec_imm_using_neg.sql", "DECLARE x INT64 DEFAULT 1;\nEXECUTE IMMEDIATE 'SELECT ?' USING (x);", "x);", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i20_s1_exec_imm_using_neg.sql", "DECLARE x INT64 DEFAULT 1;\nEXECUTE IMMEDIATE 'SELECT ?' USING (x", "x", 1, "x INT64", 0)
	assertDefLocation(t, eng, "file:///i20_s2_exec_imm_using_neg.sql", "DECLARE x INT64 DEFAULT 1;\nEXECUTE IMMEDIATE 'SELECT ?' USING (x + ()", "x +", 0, "x INT64", 0)
}

func TestJinjaSecondaryBranchCompletionAndHover(t *testing.T) {
	t.Parallel()
	_, eng := newTestStoreAndEngine(t)
	ctx := context.Background()

	// 1. Completion inside {% else %} and {% elif %} branches.
	elseCompSQL := "{% if var('x') %}\nSELECT 1;\n{% else %}\nSELECT u. FROM users u\n{% endif %}"
	eng.OpenDocument("file:///branch_comp_else.sql.j2", elseCompSQL, 1, nil)
	list, err := eng.Complete(ctx, "file:///branch_comp_else.sql.j2", utf16PosAfter(t, elseCompSQL, "u."))
	require.NoError(t, err)
	assert.Contains(t, completionLabels(list), "user_id")
	assert.Contains(t, completionLabels(list), "country")

	elifCompSQL := "{% if var('x') %}\nSELECT 1;\n{% elif var('y') %}\nSELECT '😀', u. FROM users u\n{% else %}\nSELECT 2;\n{% endif %}"
	eng.OpenDocument("file:///branch_comp_elif.sql.j2", elifCompSQL, 1, nil)
	listElif, err := eng.Complete(ctx, "file:///branch_comp_elif.sql.j2", utf16PosAfter(t, elifCompSQL, "u."))
	require.NoError(t, err)
	assert.Contains(t, completionLabels(listElif), "user_id")
	assert.Contains(t, completionLabels(listElif), "country")

	// 2. Hover on table, column, and alias inside {% else %} and {% elif %} (with UTF-16 emoji alignment).
	elseHovSQL := "{% if var('x') %}\nSELECT 1;\n{% else %}\nSELECT '😀', u.user_id FROM users u\n{% endif %}"
	eng.OpenDocument("file:///branch_hov_else.sql.j2", elseHovSQL, 1, nil)

	hovTbl, err := eng.Hover(ctx, "file:///branch_hov_else.sql.j2", utf16Pos(t, elseHovSQL, "users u"))
	require.NoError(t, err)
	require.NotNil(t, hovTbl)
	assert.Contains(t, hovTbl.Contents.Value, "my-proj.analytics.users")
	require.NotNil(t, hovTbl.Range)
	assert.Equal(t, utf16Pos(t, elseHovSQL, "users u"), hovTbl.Range.Start)

	hovCol, err := eng.Hover(ctx, "file:///branch_hov_else.sql.j2", utf16Pos(t, elseHovSQL, "user_id"))
	require.NoError(t, err)
	require.NotNil(t, hovCol)
	assert.Contains(t, hovCol.Contents.Value, "INT64")
	require.NotNil(t, hovCol.Range)
	assert.Equal(t, utf16Pos(t, elseHovSQL, "u.user_id"), hovCol.Range.Start)

	hovAlias, err := eng.Hover(ctx, "file:///branch_hov_else.sql.j2", utf16Pos(t, elseHovSQL, "u.user_id"))
	require.NoError(t, err)
	require.NotNil(t, hovAlias)
	assert.Contains(t, hovAlias.Contents.Value, "my-proj.analytics.users")

	elifHovSQL := "{% if var('x') %}\nSELECT 1;\n{% elif var('y') %}\nSELECT u.country FROM users u\n{% else %}\nSELECT 2;\n{% endif %}"
	eng.OpenDocument("file:///branch_hov_elif.sql.j2", elifHovSQL, 1, nil)
	hovElif, err := eng.Hover(ctx, "file:///branch_hov_elif.sql.j2", utf16Pos(t, elifHovSQL, "country"))
	require.NoError(t, err)
	require.NotNil(t, hovElif)
	assert.Contains(t, hovElif.Contents.Value, "STRING")
}

func TestRegressionPipeHashLookupJoinStage2(t *testing.T) {
	t.Parallel()
	_, eng := newTestStoreAndEngine(t)

	assertEmptyDefLocation(t, eng, "file:///t5_pipe_hash_using_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM unknown_a u |> INNER HASH JOIN unknown_b e USING (status + ())", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_pipe_lookup_on_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM users u |> LEFT LOOKUP JOIN events e ON status + ()", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_pipe_outer_hash_on_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM users u |> LEFT OUTER HASH JOIN events e ON status + ()", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_pipe_full_outer_lookup_on_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM users u |> FULL OUTER LOOKUP JOIN events e ON status + ()", "status +", 0)
	assertDefLocation(t, eng, "file:///t5_pipe_hash_on_decl_neg.sql",
		"DECLARE x INT64 DEFAULT 1;\nFROM users u |> INNER HASH JOIN events e ON u.user_id = x + ()", "x +", 0, "x INT64", 0)
}

func TestRegressionJoinTableSampleForSystemTimeUsingStage2(t *testing.T) {
	t.Parallel()
	_, eng := newTestStoreAndEngine(t)

	assertEmptyDefLocation(t, eng, "file:///t5_tablesample_using_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN events e TABLESAMPLE SYSTEM (10 PERCENT) USING (status) WHERE 1 =", "status)", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_pipe_tablesample_using_s2.sql",
		"DECLARE x INT64 DEFAULT 1;\nFROM unknown_a u |> JOIN unknown_b e TABLESAMPLE SYSTEM (10 PERCENT) USING (x + ())", "x +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_forsystime_using_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN events e FOR SYSTEM_TIME AS OF CURRENT_TIMESTAMP() USING (status) WHERE 1 =", "status)", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_forsystime_before_alias_tablesample_using_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM users u JOIN events FOR SYSTEM_TIME AS OF TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 1 HOUR) e TABLESAMPLE SYSTEM (10 PERCENT) USING (status) WHERE 1 =", "status)", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_pipe_forsystime_using_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM users u |> JOIN events e FOR SYSTEM_TIME AS OF CURRENT_TIMESTAMP() USING (status + ())", "status +", 0)
}

func TestRegressionParenthesizedJoinStage2(t *testing.T) {
	t.Parallel()
	_, eng := newTestStoreAndEngine(t)

	assertEmptyDefLocation(t, eng, "file:///t5_paren_join_using_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM (users u JOIN events e USING (status)) WHERE 1 =", "status))", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_paren_join_on_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM (users u JOIN events e ON status + ());", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_nested_paren_join_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM ((users u JOIN events e USING (status)) JOIN users u2 ON status + ());", "status +", 0)
	assertDefLocation(t, eng, "file:///t5_paren_join_outer_alias_s2.sql",
		"SELECT u.user_id FROM (users u JOIN events e ON u.user_id = e.user_id) WHERE 1 =", "u.user_id", 0, "u JOIN", 0)
	assertDefLocation(t, eng, "file:///t5_paren_join_on_decl_neg.sql",
		"DECLARE x INT64 DEFAULT 1;\nSELECT 1 FROM (users u JOIN events e ON u.user_id = x + ());", "x +", 0, "x INT64", 0)
}

func TestRegressionTopLevelFromJoinOnStage1And2(t *testing.T) {
	t.Parallel()
	_, eng := newTestStoreAndEngine(t)

	assertEmptyDefLocation(t, eng, "file:///t5_from_join_on_s1_unknown.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM unknown_a u JOIN (SELECT 'ok' AS status) e ON status = 'ok';", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_from_join_on_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM users u JOIN events e ON status + ();", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_from_join_on_subq_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM unknown_a u JOIN (SELECT 'ok' AS status) e ON status + ();", "status +", 0)
	assertDefLocation(t, eng, "file:///t5_from_join_on_decl_neg.sql",
		"DECLARE x INT64 DEFAULT 1;\nFROM users u JOIN events e ON u.user_id = x + ();", "x +", 0, "x INT64", 0)
}

func TestCoverageGapCoreAnalysisPaths(t *testing.T) {
	t.Parallel()
	_, eng := newTestStoreAndEngine(t)
	ctx := context.Background()

	// 1. CTE dot, subquery dot, unaliased struct dot, Jinja tag keyword completion.
	cteSQL := "WITH c AS (SELECT user_id, country FROM users)\nSELECT c. FROM c"
	eng.OpenDocument("file:///t5_comp_cte.sql", cteSQL, 1, nil)
	res, err := eng.Complete(ctx, "file:///t5_comp_cte.sql", utf16PosAfter(t, cteSQL, "c."))
	require.NoError(t, err)
	assert.Contains(t, completionLabels(res), "user_id")
	assert.Contains(t, completionLabels(res), "country")

	subqSQL := "SELECT sub. FROM (SELECT 1 AS alpha, 2 AS beta) sub"
	eng.OpenDocument("file:///t5_comp_subq.sql", subqSQL, 1, nil)
	res, err = eng.Complete(ctx, "file:///t5_comp_subq.sql", utf16PosAfter(t, subqSQL, "sub."))
	require.NoError(t, err)
	assert.Contains(t, completionLabels(res), "alpha")
	assert.Contains(t, completionLabels(res), "beta")

	structSQL := "SELECT user_ctx. FROM events"
	eng.OpenDocument("file:///t5_comp_struct.sql", structSQL, 1, nil)
	res, err = eng.Complete(ctx, "file:///t5_comp_struct.sql", utf16PosAfter(t, structSQL, "user_ctx."))
	require.NoError(t, err)
	assert.Contains(t, completionLabels(res), "user_id")
	assert.Contains(t, completionLabels(res), "tags")

	jkwSQL := "{% "
	eng.OpenDocument("file:///t5_comp_jkw.sql.j2", jkwSQL, 1, nil)
	res, err = eng.Complete(ctx, "file:///t5_comp_jkw.sql.j2", syntax.Position{Line: 0, Character: 3})
	require.NoError(t, err)
	assert.Contains(t, completionLabels(res), "endif")

	// 2. Hover on @@sysvar, @param, Jinja set & dbt ref.
	svSQL := "SELECT @@time_zone, @user_id"
	eng.OpenDocument("file:///t5_hov_sv.sql", svSQL, 1, nil)
	hov, err := eng.Hover(ctx, "file:///t5_hov_sv.sql", utf16Pos(t, svSQL, "@@time_zone"))
	require.NoError(t, err)
	require.NotNil(t, hov)
	assert.Contains(t, hov.Contents.Value, "@@time_zone")

	hov, err = eng.Hover(ctx, "file:///t5_hov_sv.sql", utf16Pos(t, svSQL, "@user_id"))
	require.NoError(t, err)
	require.NotNil(t, hov)
	assert.Contains(t, hov.Contents.Value, "@user_id")

	jHovSQL := "{% set my_tbl = 'users' %}\nSELECT {{ my_tbl }}, {{ ref('users') }}"
	eng.OpenDocument("file:///t5_hov_jinja.sql.j2", jHovSQL, 1, nil)
	hov, err = eng.Hover(ctx, "file:///t5_hov_jinja.sql.j2", utf16PosNth(t, jHovSQL, "my_tbl", 1))
	require.NoError(t, err)
	require.NotNil(t, hov)
	assert.Contains(t, hov.Contents.Value, "set my_tbl")

	// 3. Semantic tokens for EXTRACT & INTERVAL + full-doc ChangeDocument (diffReplacementBounds) + LineIndex.
	semSQL := "SELECT EXTRACT(DAY FROM CURRENT_DATE()), INTERVAL 1 DAY"
	eng.OpenDocument("file:///t5_sem_ext.sql", semSQL, 1, nil)
	semRes, err := eng.SemanticTokensFull(ctx, "file:///t5_sem_ext.sql")
	require.NoError(t, err)
	toks := decodeTokens(t, semRes.Data)
	dayExtTok, ok := findTokAt(toks, utf16PosNth(t, semSQL, "DAY", 0))
	require.True(t, ok)
	assert.Equal(t, analysis.TokenEnumMember, dayExtTok.tokType)
	dayIntTok, ok := findTokAt(toks, utf16PosNth(t, semSQL, "DAY", 1))
	require.True(t, ok)
	assert.Equal(t, analysis.TokenEnumMember, dayIntTok.tokType)

	require.NoError(t, eng.ChangeDocument("file:///t5_sem_ext.sql", 2, []syntax.TextChange{{Text: semSQL + " + ("}}))
	li, ok := eng.LineIndex("file:///t5_sem_ext.sql")
	require.True(t, ok)
	require.NotNil(t, li)
	_, missingLI := eng.LineIndex("file:///t5_missing.sql")
	assert.False(t, missingLI)

	// 4. Procedural CASE statement & MERGE / |> RENAME / |> CALL definition.
	caseSQL := "BEGIN\n  DECLARE v INT64 DEFAULT 1;\n  CASE v WHEN 1 THEN SELECT v; ELSE SELECT 2; END CASE;\nEND;"
	eng.OpenDocument("file:///t5_case.sql", caseSQL, 1, nil)
	diags, err := eng.Diagnostics(ctx, "file:///t5_case.sql")
	require.NoError(t, err)
	assert.Empty(t, diags)

	assertDefLocation(t, eng, "file:///t5_merge_alias.sql",
		"MERGE users t USING events s ON t.user_id = s.user_id WHEN MATCHED THEN UPDATE SET t.country = s.status;", "s.user_id", 0, "s ON", 0)
	assertDefLocation(t, eng, "file:///t5_merge_target_alias.sql",
		"MERGE users t USING events s ON t.user_id = s.user_id WHEN MATCHED THEN UPDATE SET t.country = s.status;", "t.country", 0, "t USING", 0)

	assertEmptyDefLocation(t, eng, "file:///t5_pipe_rename_s0_unknown.sql",
		"DECLARE x INT64 DEFAULT 1;\nFROM unknown_tbl |> SELECT 1 AS a |> RENAME a AS x |> WHERE x = 1;", "x = 1", 0)
	assertDefLocation(t, eng, "file:///t5_pipe_rename_old_neg.sql",
		"DECLARE a INT64 DEFAULT 1;\nFROM (SELECT 1 AS a) |> RENAME a AS x |> WHERE a = 1;", "a = 1", 0, "a INT64", 0)

	assertDefLocation(t, eng, "file:///t5_pipe_call_s2.sql",
		"DECLARE z INT64 DEFAULT 1;\nFROM users |> CALL my_tvf(1) AS z |> WHERE z + ()", "z +", 0, "z |>", 0)
	assertDefLocation(t, eng, "file:///t5_pipe_call_barrier_s2.sql",
		"DECLARE z INT64 DEFAULT 1;\nFROM users |> CALL my_tvf(1) |> WHERE z + ()", "z +", 0, "z INT64", 0)
}

func TestRegressionParenJoinWithSubqueryStage2(t *testing.T) {
	t.Parallel()
	_, eng := newTestStoreAndEngine(t)

	assertEmptyDefLocation(t, eng, "file:///t5_i2_paren_subq_on_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM (users u JOIN (SELECT 'ok' AS status) e ON status + ()) WHERE 1 =", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i2_paren_subq_where_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT status FROM (users u JOIN (SELECT 'ok' AS status) e ON u.user_id = 1) WHERE status + ()", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i2_nested_paren_subq_both_on1_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM ((users u JOIN (SELECT 'ok' AS status) e ON status + ()) JOIN users u2 ON status + ()) WHERE 1 =", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i2_nested_paren_subq_both_on2_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM ((users u JOIN (SELECT 'ok' AS status) e ON status + ()) JOIN users u2 ON status + ()) WHERE 1 =", "status +", 1)
	assertEmptyDefLocation(t, eng, "file:///t5_i2_nested_paren_subq_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM ((users u JOIN (SELECT 'ok' AS status) e ON u.user_id = 1) JOIN users u2 ON status + ()) WHERE 1 =", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i2_paren_subq_from_pipe_where_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM (users u JOIN (SELECT 'ok' AS status) e ON status + ()) |> WHERE status + ()", "status +", 1)
	assertDefLocation(t, eng, "file:///t5_i2_paren_subq_decl_neg_s2.sql",
		"DECLARE x INT64 DEFAULT 1;\nSELECT 1 FROM (users u JOIN (SELECT 'ok' AS status) e ON u.user_id = x + ()) WHERE 1 =", "x +", 0, "x INT64", 0)
}

func TestRegressionPipeJoinParenJoinRHSStage1And2(t *testing.T) {
	t.Parallel()
	_, eng := newTestStoreAndEngine(t)

	assertEmptyDefLocation(t, eng, "file:///t5_i2_pipe_paren_s1.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM unknown_a u |> JOIN (users u2 JOIN events e ON status = 'ok') ON 1 = 1", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i2_pipe_paren_nested_s1.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM unknown_a u |> JOIN ((users u2 JOIN events e ON status = 'ok') JOIN users u3 ON 1 = 1) ON 1 = 1", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i2_pipe_paren_outer_subq_s1.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM unknown_a u |> JOIN (users u2 JOIN (SELECT 'ok' AS status) e ON u2.user_id = 1) ON status = 'ok'", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i2_pipe_paren_exists_on_s1.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM unknown_a u |> JOIN (users u2 JOIN events e ON EXISTS (SELECT 1 FROM unknown_b JOIN unknown_c ON status = 'ok')) ON 1 = 1", "status =", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i2_pipe_paren_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM users u |> JOIN (users u2 JOIN events e ON status + ()) ON 1 = 1", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i2_pipe_paren_unknown_lhs_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM unknown_a u |> JOIN (users u2 JOIN events e ON status + ()) ON 1 = 1", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i2_pipe_paren_cross_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM users u |> CROSS JOIN (users u2 JOIN (SELECT 'ok' AS status) e ON status + ())", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i2_pipe_paren_subq_inner_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM users u |> JOIN (users u2 JOIN (SELECT 'ok' AS status) e ON status + ()) ON 1 = 1", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i2_pipe_paren_subq_outer_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM users u |> JOIN (users u2 JOIN (SELECT 'ok' AS status) e ON u2.user_id = 1) ON status + ()", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i2_pipe_paren_nested_subq_s2.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM users u |> JOIN ((users u2 JOIN (SELECT 'ok' AS status) e ON status + ()) JOIN users u3 ON status + ()) ON status + ()", "status +", 2)
	assertDefLocation(t, eng, "file:///t5_i2_pipe_paren_decl_neg_s1.sql",
		"DECLARE x INT64 DEFAULT 1;\nFROM unknown_a u |> JOIN (users u2 JOIN events e ON u2.user_id = x) ON 1 = 1", "x)", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///t5_i2_pipe_paren_decl_neg.sql",
		"DECLARE x INT64 DEFAULT 1;\nFROM users u |> JOIN (users u2 JOIN events e ON u2.user_id = x + ()) ON 1 = 1", "x +", 0, "x INT64", 0)
	assertDefLocation(t, eng, "file:///t5_i2_pipe_paren_subq_where_neg.sql",
		"DECLARE status STRING DEFAULT 'a';\nFROM users u |> JOIN (users u2 JOIN (SELECT 1 FROM users u3 WHERE status + ()) e ON 1 = 1) ON 1 = 1", "status +", 0, "status STRING", 0)
}

func TestRegressionSelectProjFromParenJoinStage2(t *testing.T) {
	t.Parallel()
	store, eng := newTestStoreAndEngine(t)
	store.PutTable(&catalog.TableMeta{
		Ref:  catalog.TableRef{Project: "my-proj", Dataset: "analytics", Table: "users"},
		Type: catalog.TableTypeTable,
		Schema: []catalog.ColumnMeta{
			{Name: "user_id", Type: "INT64", Mode: catalog.ModeRequired},
			{Name: "country", Type: "STRING", Mode: catalog.ModeNullable},
			{
				Name: "profile",
				Type: "RECORD",
				Mode: catalog.ModeNullable,
				Fields: []catalog.ColumnMeta{
					{Name: "email", Type: "STRING", Mode: catalog.ModeNullable},
				},
			},
		},
	})

	star := "*"

	assertEmptyDefLocation(t, eng, "file:///t5_i3_paren_proj_qual_pipe.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT e.status FROM (users u JOIN events e ON u.user_id = e.user_id) |> WHERE status + ()", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i3_paren_proj_dotstar_pipe.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT e."+star+" FROM (users u JOIN events e ON u.user_id = e.user_id) |> WHERE status + ()", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i3_paren_proj_struct_pipe.sql",
		"DECLARE email STRING DEFAULT 'a';\nSELECT u.profile.email FROM (users u JOIN events e ON 1 = 1) |> WHERE email + ()", "email +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i3_paren_proj_struct_star_pipe.sql",
		"DECLARE email STRING DEFAULT 'a';\nSELECT u.profile."+star+" FROM (users u JOIN events e ON 1 = 1) |> WHERE email + ()", "email +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i3_paren_proj_aliased_struct_pipe.sql",
		"DECLARE email STRING DEFAULT 'a';\nSELECT u.profile AS p FROM (users u JOIN events e ON 1 = 1) |> SELECT p."+star+" |> WHERE email + ()", "email +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i3_paren_proj_events_struct_pipe.sql",
		"DECLARE user_id INT64 DEFAULT 1;\nSELECT e.user_ctx.user_id FROM (users u JOIN events e ON 1 = 1) |> WHERE user_id + ()", "user_id +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i3_paren_proj_cte.sql",
		"DECLARE status STRING DEFAULT 'a';\nWITH cte AS (SELECT e.status FROM (users u JOIN events e ON 1 = 1))\nSELECT 1 FROM cte WHERE status + ()", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i3_paren_proj_nested_cte.sql",
		"DECLARE status STRING DEFAULT 'a';\nWITH cte AS (SELECT e.status FROM ((users u JOIN events e ON 1 = 1) JOIN users u2 ON 1 = 1))\nSELECT 1 FROM cte WHERE status + ()", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i3_paren_proj_subq.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM (SELECT e.status FROM (users u JOIN events e ON 1 = 1)) sub WHERE status + ()", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i3_paren_proj_subq_dotstar.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT 1 FROM (SELECT e."+star+" FROM (users u JOIN events e ON 1 = 1)) sub WHERE status + ()", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i3_paren_proj_unknown_qual.sql",
		"DECLARE col STRING DEFAULT 'a';\nSELECT x.col FROM (unknown_a x JOIN events e ON 1 = 1) |> WHERE col + ()", "col +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i3_paren_proj_cte_in_paren.sql",
		"DECLARE status STRING DEFAULT 'a';\nWITH c AS (SELECT 'ok' AS status)\nSELECT c.status FROM (users u JOIN c ON 1 = 1) |> WHERE status + ()", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i3_paren_proj_setop_order.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT e.status FROM (users u JOIN events e ON 1 = 1) UNION ALL SELECT 'x' ORDER BY status + ()", "status +", 0)
	assertEmptyDefLocation(t, eng, "file:///t5_i3_paren_proj_subq_in_paren.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT e.status FROM (users u JOIN (SELECT 'ok' AS status) e ON 1 = 1) |> WHERE status + ()", "status +", 0)
	assertDefLocation(t, eng, "file:///t5_i3_paren_proj_unproj_neg.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT u.user_id FROM (users u JOIN events e ON 1 = 1) |> WHERE status + ()", "status +", 0, "status STRING", 0)
	assertDefLocation(t, eng, "file:///t5_i3_paren_proj_subq_unproj_neg.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT u.user_id FROM (users u JOIN (SELECT 'ok' AS status) e ON 1 = 1) |> WHERE status + ()", "status +", 0, "status STRING", 0)
	assertDefLocation(t, eng, "file:///t5_i3_paren_proj_setop_unproj_neg.sql",
		"DECLARE status STRING DEFAULT 'a';\nSELECT u.user_id FROM (users u JOIN events e ON 1 = 1) UNION ALL SELECT 'x' ORDER BY status + ()", "status +", 0, "status STRING", 0)
}
