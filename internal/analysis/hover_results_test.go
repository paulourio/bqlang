package analysis_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/internal/analysis"
	"github.com/paulourio/bqlang/internal/bqconfig"
	"github.com/paulourio/bqlang/internal/catalog"
	"github.com/paulourio/bqlang/internal/syntax"
)

func newShakespeareEngine(t *testing.T) *analysis.Engine {
	t.Helper()

	opts := bqconfig.DefaultLSPOptions()
	opts.DefaultProject = "my-proj"
	opts.DefaultDataset = "analytics"
	opts.BigQueryAPIEnabled = false
	opts.CacheDir = t.TempDir()

	store, err := catalog.NewStore(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	store.PutTable(&catalog.TableMeta{
		Ref:  catalog.TableRef{Project: "bigquery-public-data", Dataset: "samples", Table: "shakespeare"},
		Type: catalog.TableTypeTable,
		Schema: []catalog.ColumnMeta{
			{Name: "word", Type: "STRING", Mode: catalog.ModeRequired, Description: "A single unique word."},
			{Name: "word_count", Type: "INT64", Mode: catalog.ModeRequired, Description: "Number of occurrences."},
			{Name: "corpus", Type: "STRING", Mode: catalog.ModeRequired, Description: "Work of Shakespeare."},
			{Name: "corpus_date", Type: "INT64", Mode: catalog.ModeRequired},
		},
	})

	eng, err := analysis.NewEngine(store, opts)
	require.NoError(t, err)

	return eng
}

func TestHoverResultAliasShakespeare(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	eng := newShakespeareEngine(t)

	sql := "SELECT APPROX_COUNT_DISTINCT(a.corpus) AS word_qty\n" +
		"  FROM `bigquery-public-data.samples.shakespeare` AS a"
	uri := "file:///shakespeare.sql"
	eng.OpenDocument(uri, sql, 1, nil)

	// Hovering the result alias word_qty shows type, role, expression, and source dependency.
	hAlias, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "word_qty"))
	require.NoError(t, err)
	require.NotNil(t, hAlias)
	assert.Equal(t, "markdown", hAlias.Contents.Kind)
	assert.Contains(t, hAlias.Contents.Value, "**`word_qty`** : `INT64`")
	assert.Contains(t, hAlias.Contents.Value, "output column · aggregate")
	assert.Contains(t, hAlias.Contents.Value, "APPROX_COUNT_DISTINCT(a.corpus)")
	assert.Contains(t, hAlias.Contents.Value, "bigquery-public-data.samples.shakespeare.corpus")
	assert.Contains(t, hAlias.Contents.Value, "`STRING`")

	// Existing hovers on the expression, alias prefix, column, and keyword remain intact.
	hFn, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "APPROX_COUNT_DISTINCT"))
	require.NoError(t, err)
	require.NotNil(t, hFn)
	assert.Contains(t, hFn.Contents.Value, "APPROX_COUNT_DISTINCT")

	hTbl, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "a.corpus"))
	require.NoError(t, err)
	require.NotNil(t, hTbl)
	assert.Contains(t, hTbl.Contents.Value, "bigquery-public-data.samples.shakespeare")

	hCol, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "corpus"))
	require.NoError(t, err)
	require.NotNil(t, hCol)
	assert.Contains(t, hCol.Contents.Value, "Work of Shakespeare.")

	hAs, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "AS word_qty"))
	require.NoError(t, err)
	assert.Nil(t, hAs)
}

func TestHoverResultAliasDirectAndComputed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, eng := newTestStoreAndEngine(t)

	sql := strings.Join([]string{
		"DECLARE v_dt DATE DEFAULT DATE '2026-01-01';",
		"SELECT u.event_date AS dt,",
		"       u.user_ctx.user_id AS uid,",
		"       APPROX_COUNT_DISTINCT(u.tenant_id) + @offset AS adjusted_cnt,",
		"       COUNT(*) AS row_cnt,",
		"       1 AS flag",
		"  FROM events u",
		" WHERE u.event_date = v_dt",
		" GROUP BY u.event_date, u.user_ctx.user_id",
	}, "\n")
	uri := "file:///direct_and_computed.sql"
	eng.OpenDocument(uri, sql, 1, nil)

	// Direct column passthrough alias keeps catalog metadata, badges, and source path.
	hDt, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "dt,"))
	require.NoError(t, err)
	require.NotNil(t, hDt)
	assert.Contains(t, hDt.Contents.Value, "**`dt`** : `DATE`")
	assert.Contains(t, hDt.Contents.Value, "From: `my-proj.analytics.events.event_date`")
	assert.Contains(t, hDt.Contents.Value, "`[PARTITION]`")

	// Direct nested struct field alias keeps nested field path and description.
	hUID, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "uid,"))
	require.NoError(t, err)
	require.NotNil(t, hUID)
	assert.Contains(t, hUID.Contents.Value, "**`uid`** : `INT64`")
	assert.Contains(t, hUID.Contents.Value, "From: `my-proj.analytics.events.user_ctx.user_id`")
	assert.Contains(t, hUID.Contents.Value, "Numeric user ID")

	// Expression combining an aggregate and a parameter unwraps synthetic $agg columns.
	hAdj, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "adjusted_cnt"))
	require.NoError(t, err)
	require.NotNil(t, hAdj)
	assert.Contains(t, hAdj.Contents.Value, "**`adjusted_cnt`** : `INT64`")
	assert.Contains(t, hAdj.Contents.Value, "aggregate")
	assert.Contains(t, hAdj.Contents.Value, "my-proj.analytics.events.tenant_id")
	assert.Contains(t, hAdj.Contents.Value, "`[CLUSTER #1]`")
	assert.Contains(t, hAdj.Contents.Value, "`@offset`")
	assert.Contains(t, hAdj.Contents.Value, "*(parameter)*")
	assert.NotContains(t, hAdj.Contents.Value, "$agg")

	// Table-only aggregate (COUNT(*)) reports the scanned table.
	hRowCnt, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "row_cnt"))
	require.NoError(t, err)
	require.NotNil(t, hRowCnt)
	assert.Contains(t, hRowCnt.Contents.Value, "**`row_cnt`** : `INT64`")
	assert.Contains(t, hRowCnt.Contents.Value, "aggregate")
	assert.Contains(t, hRowCnt.Contents.Value, "`my-proj.analytics.events` *(table)*")

	// Pure literal alias has type and expression with no spurious dependency section.
	hFlag, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "flag"))
	require.NoError(t, err)
	require.NotNil(t, hFlag)
	assert.Contains(t, hFlag.Contents.Value, "**`flag`** : `INT64`")
	assert.NotContains(t, hFlag.Contents.Value, "Depends on:")
}

func TestHoverResultAliasWindowAndPassthrough(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, eng := newTestStoreAndEngine(t)

	sql := strings.Join([]string{
		"SELECT u.event_date AS dt,",
		"       SUM(u.user_ctx.user_id) OVER (PARTITION BY u.tenant_id ORDER BY u.event_date) AS running_uid",
		"  FROM events u",
	}, "\n")
	uri := "file:///window_alias.sql"
	eng.OpenDocument(uri, sql, 1, nil)

	// Passthrough alias alongside an analytic function still resolves via output columns.
	hDt, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "dt,"))
	require.NoError(t, err)
	require.NotNil(t, hDt)
	assert.Contains(t, hDt.Contents.Value, "**`dt`** : `DATE`")
	assert.Contains(t, hDt.Contents.Value, "From: `my-proj.analytics.events.event_date`")
	assert.Contains(t, hDt.Contents.Value, "`[PARTITION]`")

	// Window alias shows value dependency plus PARTITION BY / ORDER BY window dependencies.
	hWin, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "running_uid"))
	require.NoError(t, err)
	require.NotNil(t, hWin)
	assert.Contains(t, hWin.Contents.Value, "**`running_uid`** : `INT64`")
	assert.Contains(t, hWin.Contents.Value, "output column · window")
	assert.Contains(t, hWin.Contents.Value, "my-proj.analytics.events.user_ctx.user_id")
	assert.Contains(t, hWin.Contents.Value, "my-proj.analytics.events.tenant_id` : `STRING` `[CLUSTER #1]` *(window)*")
	assert.Contains(t, hWin.Contents.Value, "my-proj.analytics.events.event_date` : `DATE` `[PARTITION]` *(window)*")
}

func TestHoverResultAliasCTELineageAndReferences(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, eng := newTestStoreAndEngine(t)

	sql := strings.Join([]string{
		"WITH stats AS (",
		"  SELECT APPROX_COUNT_DISTINCT(u.tenant_id) AS tenant_qty",
		"    FROM events u",
		")",
		"SELECT s.tenant_qty,",
		"       s.tenant_qty * 2 AS doubled_qty",
		"  FROM stats s",
	}, "\n")
	uri := "file:///cte_lineage.sql"
	eng.OpenDocument(uri, sql, 1, nil)

	// Hovering CTE definition name in WITH stats AS (...).
	hCTEDef, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "stats AS"))
	require.NoError(t, err)
	require.NotNil(t, hCTEDef)
	assert.Contains(t, hCTEDef.Contents.Value, "### CTE `stats`")
	assert.Contains(t, hCTEDef.Contents.Value, "| `tenant_qty` | `INT64` |")

	// Hovering alias inside CTE definition.
	hInner, err := eng.Hover(ctx, uri, utf16PosNth(t, sql, "tenant_qty", 0))
	require.NoError(t, err)
	require.NotNil(t, hInner)
	assert.Contains(t, hInner.Contents.Value, "**`tenant_qty`** : `INT64` *(CTE `stats` · aggregate)*")
	assert.Contains(t, hInner.Contents.Value, "my-proj.analytics.events.tenant_id")

	// Hovering downstream reference s.tenant_qty shows CTE origin, expression, and source dependency.
	hRef, err := eng.Hover(ctx, uri, utf16PosNth(t, sql, "tenant_qty", 1))
	require.NoError(t, err)
	require.NotNil(t, hRef)
	assert.Contains(t, hRef.Contents.Value, "**`tenant_qty`** : `INT64` *(from CTE `stats` · aggregate)*")
	assert.Contains(t, hRef.Contents.Value, "APPROX_COUNT_DISTINCT(u.tenant_id)")
	assert.Contains(t, hRef.Contents.Value, "my-proj.analytics.events.tenant_id")

	// Hovering derived outer alias doubled_qty shows transitive lineage through stats.tenant_qty.
	hDoubled, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "doubled_qty"))
	require.NoError(t, err)
	require.NotNil(t, hDoubled)
	assert.Contains(t, hDoubled.Contents.Value, "**`doubled_qty`** : `INT64`")
	assert.Contains(t, hDoubled.Contents.Value, "`stats.tenant_qty` : `INT64` ← `my-proj.analytics.events.tenant_id`")
}

func TestHoverResultAliasOrderByAndHaving(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, eng := newTestStoreAndEngine(t)

	sql := strings.Join([]string{
		"SELECT u.tenant_id,",
		"       COUNT(u.event_date) AS event_cnt",
		"  FROM events u",
		" GROUP BY u.tenant_id",
		"HAVING event_cnt > 5",
		" ORDER BY event_cnt DESC",
	}, "\n")
	uri := "file:///having_order.sql"
	eng.OpenDocument(uri, sql, 1, nil)

	// HAVING alias reference.
	hHaving, err := eng.Hover(ctx, uri, utf16PosNth(t, sql, "event_cnt", 1))
	require.NoError(t, err)
	require.NotNil(t, hHaving)
	assert.Contains(t, hHaving.Contents.Value, "**`event_cnt`** : `INT64`")
	assert.Contains(t, hHaving.Contents.Value, "aggregate")
	assert.Contains(t, hHaving.Contents.Value, "my-proj.analytics.events.event_date")

	// ORDER BY alias reference.
	hOrder, err := eng.Hover(ctx, uri, utf16PosNth(t, sql, "event_cnt", 2))
	require.NoError(t, err)
	require.NotNil(t, hOrder)
	assert.Contains(t, hOrder.Contents.Value, "**`event_cnt`** : `INT64`")
	assert.Contains(t, hOrder.Contents.Value, "aggregate")
	assert.Contains(t, hOrder.Contents.Value, "my-proj.analytics.events.event_date")

	// Hovering DESC keyword must not trigger column hover.
	hDesc, err := eng.Hover(ctx, uri, utf16Pos(t, sql, "DESC"))
	require.NoError(t, err)
	assert.Nil(t, hDesc)
}

func TestHoverResultAliasPipeOperators(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, eng := newTestStoreAndEngine(t)

	sql1 := strings.Join([]string{
		"FROM events u",
		"|> EXTEND u.user_ctx.user_id * 2 AS doubled_uid",
		"|> AGGREGATE APPROX_COUNT_DISTINCT(u.tenant_id) AS tenant_qty, SUM(doubled_uid) AS sum_uid",
		"   GROUP BY u.event_date AS dt",
	}, "\n")
	uri1 := "file:///pipe_agg.sql"
	eng.OpenDocument(uri1, sql1, 1, nil)

	hExt, err := eng.Hover(ctx, uri1, utf16Pos(t, sql1, "doubled_uid"))
	require.NoError(t, err)
	require.NotNil(t, hExt)
	assert.Contains(t, hExt.Contents.Value, "**`doubled_uid`** : `INT64`")
	assert.Contains(t, hExt.Contents.Value, "my-proj.analytics.events.user_ctx.user_id")

	hAgg, err := eng.Hover(ctx, uri1, utf16Pos(t, sql1, "tenant_qty"))
	require.NoError(t, err)
	require.NotNil(t, hAgg)
	assert.Contains(t, hAgg.Contents.Value, "**`tenant_qty`** : `INT64`")
	assert.Contains(t, hAgg.Contents.Value, "aggregate")
	assert.Contains(t, hAgg.Contents.Value, "my-proj.analytics.events.tenant_id")

	hSum, err := eng.Hover(ctx, uri1, utf16Pos(t, sql1, "sum_uid"))
	require.NoError(t, err)
	require.NotNil(t, hSum)
	assert.Contains(t, hSum.Contents.Value, "**`sum_uid`** : `INT64`")
	assert.Contains(t, hSum.Contents.Value, "`doubled_uid` : `INT64` ← `my-proj.analytics.events.user_ctx.user_id`")

	hGrp, err := eng.Hover(ctx, uri1, utf16Pos(t, sql1, "dt"))
	require.NoError(t, err)
	require.NotNil(t, hGrp)
	assert.Contains(t, hGrp.Contents.Value, "**`dt`** : `DATE`")
	assert.Contains(t, hGrp.Contents.Value, "From: `my-proj.analytics.events.event_date`")

	sql2 := strings.Join([]string{
		"FROM events u",
		"|> RENAME tenant_id AS org_id",
		"|> SET org_id = UPPER(org_id)",
	}, "\n")
	uri2 := "file:///pipe_rename_set.sql"
	eng.OpenDocument(uri2, sql2, 1, nil)

	hRenNew, err := eng.Hover(ctx, uri2, utf16PosNth(t, sql2, "org_id", 0))
	require.NoError(t, err)
	require.NotNil(t, hRenNew)
	assert.Contains(t, hRenNew.Contents.Value, "**`org_id`** : `STRING`")
	assert.Contains(t, hRenNew.Contents.Value, "From: `my-proj.analytics.events.tenant_id`")

	hRenOld, err := eng.Hover(ctx, uri2, utf16Pos(t, sql2, "tenant_id"))
	require.NoError(t, err)
	require.NotNil(t, hRenOld)
	assert.Contains(t, hRenOld.Contents.Value, "**`tenant_id`** : `STRING`")
	assert.Contains(t, hRenOld.Contents.Value, "my-proj.analytics.events")

	hSetLHS, err := eng.Hover(ctx, uri2, utf16PosNth(t, sql2, "org_id", 1))
	require.NoError(t, err)
	require.NotNil(t, hSetLHS)
	assert.Contains(t, hSetLHS.Contents.Value, "**`org_id`** : `STRING`")
	assert.Contains(t, hSetLHS.Contents.Value, "UPPER(org_id)")
	assert.Contains(t, hSetLHS.Contents.Value, "my-proj.analytics.events.tenant_id")
}

func TestHoverResultAliasStructAndDuplicateNames(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, eng := newTestStoreAndEngine(t)

	sqlStruct := "SELECT STRUCT(u.tenant_id AS tid, UPPER(u.tenant_id) AS tid_upper) AS info FROM events u"
	uriStruct := "file:///struct_alias.sql"
	eng.OpenDocument(uriStruct, sqlStruct, 1, nil)

	hInfo, err := eng.Hover(ctx, uriStruct, utf16Pos(t, sqlStruct, "info"))
	require.NoError(t, err)
	require.NotNil(t, hInfo)
	assert.Contains(t, hInfo.Contents.Value, "**`info`** : `STRUCT<tid STRING, tid_upper STRING>`")
	assert.Equal(t, 1, strings.Count(hInfo.Contents.Value, "my-proj.analytics.events.tenant_id"))

	hTid, err := eng.Hover(ctx, uriStruct, utf16Pos(t, sqlStruct, "tid,"))
	require.NoError(t, err)
	require.NotNil(t, hTid)
	assert.Contains(t, hTid.Contents.Value, "**`tid`** : `STRING`")
	assert.Contains(t, hTid.Contents.Value, "*(struct field)*")
	assert.Contains(t, hTid.Contents.Value, "From: `my-proj.analytics.events.tenant_id`")

	hTidUpper, err := eng.Hover(ctx, uriStruct, utf16Pos(t, sqlStruct, "tid_upper"))
	require.NoError(t, err)
	require.NotNil(t, hTidUpper)
	assert.Contains(t, hTidUpper.Contents.Value, "**`tid_upper`** : `STRING` *(struct field)*")
	assert.Contains(t, hTidUpper.Contents.Value, "UPPER(u.tenant_id)")
	assert.Contains(t, hTidUpper.Contents.Value, "my-proj.analytics.events.tenant_id")

	// Same alias name in inner CTE and outer SELECT resolves each scope independently.
	sqlDup := strings.Join([]string{
		"WITH base AS (",
		"  SELECT COUNT(u.tenant_id) AS qty FROM events u",
		")",
		"SELECT SUM(b.qty) AS qty FROM base b",
	}, "\n")
	uriDup := "file:///dup_alias.sql"
	eng.OpenDocument(uriDup, sqlDup, 1, nil)

	hInner, err := eng.Hover(ctx, uriDup, utf16PosNth(t, sqlDup, "qty", 0))
	require.NoError(t, err)
	require.NotNil(t, hInner)
	assert.Contains(t, hInner.Contents.Value, "COUNT(u.tenant_id)")
	assert.Contains(t, hInner.Contents.Value, "my-proj.analytics.events.tenant_id")
	assert.NotContains(t, hInner.Contents.Value, "base.qty")

	hOuter, err := eng.Hover(ctx, uriDup, utf16PosNth(t, sqlDup, "qty", 2))
	require.NoError(t, err)
	require.NotNil(t, hOuter)
	assert.Contains(t, hOuter.Contents.Value, "SUM(b.qty)")
	assert.Contains(t, hOuter.Contents.Value, "`base.qty` : `INT64` ← `my-proj.analytics.events.tenant_id`")
}

func TestHoverResultAliasParserFallbackAndCTAS(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, eng := newTestStoreAndEngine(t)

	// Unknown table forces parser-only fallback (no ResolvedAST).
	sqlFallback := strings.Join([]string{
		"SELECT APPROX_COUNT_DISTINCT(a.corpus) AS word_qty,",
		"       COUNT(a.word) AS total_words,",
		"       CAST(a.word_count AS FLOAT64) AS wc_f",
		"  FROM `unknown-proj.unknown_ds.unknown_tbl` AS a",
	}, "\n")
	uriFB := "file:///fallback_alias.sql"
	eng.OpenDocument(uriFB, sqlFallback, 1, nil)

	hWordQty, err := eng.Hover(ctx, uriFB, utf16Pos(t, sqlFallback, "word_qty"))
	require.NoError(t, err)
	require.NotNil(t, hWordQty)
	assert.Contains(t, hWordQty.Contents.Value, "**`word_qty`** *(output column · aggregate)*")
	assert.Contains(t, hWordQty.Contents.Value, "APPROX_COUNT_DISTINCT(a.corpus)")
	assert.Contains(t, hWordQty.Contents.Value, "`unknown-proj.unknown_ds.unknown_tbl.corpus`")

	hTotal, err := eng.Hover(ctx, uriFB, utf16Pos(t, sqlFallback, "total_words"))
	require.NoError(t, err)
	require.NotNil(t, hTotal)
	assert.Contains(t, hTotal.Contents.Value, "**`total_words`** : `INT64` *(output column · aggregate)*")
	assert.Contains(t, hTotal.Contents.Value, "`unknown-proj.unknown_ds.unknown_tbl.word`")

	hCast, err := eng.Hover(ctx, uriFB, utf16Pos(t, sqlFallback, "wc_f"))
	require.NoError(t, err)
	require.NotNil(t, hCast)
	assert.Contains(t, hCast.Contents.Value, "**`wc_f`** : `FLOAT64`")
	assert.Contains(t, hCast.Contents.Value, "`unknown-proj.unknown_ds.unknown_tbl.word_count`")

	// CTAS with implicit alias (no AS keyword) and backtick-quoted alias.
	sqlCTAS := "CREATE TEMP TABLE summary AS SELECT APPROX_COUNT_DISTINCT(u.tenant_id) `tenant qty`, COUNT(*) cnt FROM events u"
	uriCTAS := "file:///ctas_alias.sql"
	eng.OpenDocument(uriCTAS, sqlCTAS, 1, nil)

	hBT, err := eng.Hover(ctx, uriCTAS, utf16Pos(t, sqlCTAS, "tenant qty"))
	require.NoError(t, err)
	require.NotNil(t, hBT)
	assert.Contains(t, hBT.Contents.Value, "**`tenant qty`** : `INT64`")
	assert.Contains(t, hBT.Contents.Value, "my-proj.analytics.events.tenant_id")

	hImp, err := eng.Hover(ctx, uriCTAS, utf16Pos(t, sqlCTAS, "cnt"))
	require.NoError(t, err)
	require.NotNil(t, hImp)
	assert.Contains(t, hImp.Contents.Value, "**`cnt`** : `INT64`")
	assert.Contains(t, hImp.Contents.Value, "`my-proj.analytics.events` *(table)*")
}

func TestHoverResultUnionPipeCTE(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sql := strings.Join([]string{
		"WITH",
		"  my_cte AS (",
		"    SELECT APPROX_COUNT_DISTINCT(a.corpus) AS word_qty",
		"      FROM `bigquery-public-data.samples.shakespeare` AS a",
		"  ),",
		"  bar_cte AS (",
		"    SELECT 1 AS word_qty",
		"  ),",
		"  joined_stuff AS (",
		"    SELECT *",
		"      FROM my_cte",
		"     UNION ALL",
		"      (FROM bar_cte |> SELECT *)",
		"  )",
		"SELECT word_qty",
		"  FROM joined_stuff",
	}, "\n")

	// 1. With table schema in catalog (ResolvedAST): both UNION ALL branches appear,
	//    and hovering at the trailing edge `word_qty|` before newline also resolves.
	engResolved := newShakespeareEngine(t)
	uriResolved := "file:///union_pipe_resolved.sql"
	engResolved.OpenDocument(uriResolved, sql, 1, nil)

	startPos := utf16PosNth(t, sql, "word_qty", 2)
	endPos := startPos
	endPos.Character += len("word_qty")

	for _, pos := range []struct {
		label string
		p     syntax.Position
	}{
		{"start", startPos},
		{"trailing_edge", endPos},
	} {
		h, err := engResolved.Hover(ctx, uriResolved, pos.p)
		require.NoError(t, err, pos.label)
		require.NotNil(t, h, pos.label)
		assert.Contains(t, h.Contents.Value, "**`word_qty`** : `INT64`", pos.label)
		assert.Contains(t, h.Contents.Value, "from CTE `joined_stuff` · UNION ALL", pos.label)
		assert.Contains(t, h.Contents.Value, "`my_cte.word_qty` : `INT64` ← `bigquery-public-data.samples.shakespeare.corpus`", pos.label)
		assert.Contains(t, h.Contents.Value, "`bar_cte.word_qty` : `INT64`", pos.label)
	}

	// 2. Without table schema in catalog (Parser-AST fallback): hovering the final
	//    unaliased `word_qty` resolves through `joined_stuff` -> `UNION ALL` -> `my_cte` + `bar_cte`.
	_, engOffline := newTestStoreAndEngine(t)
	uriOffline := "file:///union_pipe_offline.sql"
	engOffline.OpenDocument(uriOffline, sql, 1, nil)

	hOffline, err := engOffline.Hover(ctx, uriOffline, startPos)
	require.NoError(t, err)
	require.NotNil(t, hOffline)
	assert.Contains(t, hOffline.Contents.Value, "**`word_qty`** : `INT64`")
	assert.Contains(t, hOffline.Contents.Value, "from CTE `joined_stuff` · UNION ALL")
	assert.Contains(t, hOffline.Contents.Value, "`my_cte.word_qty` ← `bigquery-public-data.samples.shakespeare.corpus`")
	assert.Contains(t, hOffline.Contents.Value, "`bar_cte.word_qty` : `INT64`")

	hOfflineEnd, err := engOffline.Hover(ctx, uriOffline, endPos)
	require.NoError(t, err)
	require.NotNil(t, hOfflineEnd)
	assert.Equal(t, hOffline.Contents.Value, hOfflineEnd.Contents.Value)

	// 3. Whitespace separated from identifier before newline still returns nil.
	sqlSpaces := strings.Replace(sql, "SELECT word_qty\n", "SELECT word_qty  \n", 1)
	uriSpaces := "file:///union_pipe_spaces.sql"
	engResolved.OpenDocument(uriSpaces, sqlSpaces, 1, nil)
	spacePos := utf16PosNth(t, sqlSpaces, "word_qty", 2)
	spacePos.Character += len("word_qty") + 2 // on '\n' after two spaces
	hSpace, err := engResolved.Hover(ctx, uriSpaces, spacePos)
	require.NoError(t, err)
	assert.Nil(t, hSpace)
}
