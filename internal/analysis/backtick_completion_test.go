package analysis_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/internal/analysis"
	"github.com/paulourio/bqlang/internal/bqconfig"
	"github.com/paulourio/bqlang/internal/catalog"
)

func TestBacktickQualifiedTableAliasCompletion(t *testing.T) {
	t.Parallel()

	store, eng := newTestStoreAndEngine(t)
	store.PutTable(&catalog.TableMeta{
		Ref: catalog.TableRef{
			Project: "bigquery-public-data",
			Dataset: "samples",
			Table:   "shakespeare",
		},
		Type:        catalog.TableTypeTable,
		Description: "Shakespeare word index",
		Schema: []catalog.ColumnMeta{
			{Name: "word", Type: "STRING", Mode: catalog.ModeRequired, Description: "A single unique word"},
			{Name: "word_count", Type: "INT64", Mode: catalog.ModeRequired, Description: "Number of occurrences"},
			{Name: "corpus", Type: "STRING", Mode: catalog.ModeRequired},
			{Name: "corpus_date", Type: "INT64", Mode: catalog.ModeRequired},
		},
	})

	// Exact multiline query with backtick-quoted 3-part table name and explicit alias.
	sql := "SELECT a.\n  FROM `bigquery-public-data.samples.shakespeare` AS a"
	uri := "file:///backtick_alias.sql"
	eng.OpenDocument(uri, sql, 1, nil)

	list, err := eng.Complete(t.Context(), uri, utf16PosAfter(t, sql, "a."))
	require.NoError(t, err)
	require.NotNil(t, list)

	labels := make([]string, 0, len(list.Items))
	for _, it := range list.Items {
		labels = append(labels, it.Label)
	}
	assert.Contains(t, labels, "word")
	assert.Contains(t, labels, "word_count")
	assert.Contains(t, labels, "corpus")
	assert.Contains(t, labels, "corpus_date")

	// Unaliased backtick-quoted table reference using table name as qualifier.
	sqlUnaliased := "SELECT shakespeare.\n  FROM `bigquery-public-data.samples.shakespeare`"
	uriUnaliased := "file:///backtick_unaliased.sql"
	eng.OpenDocument(uriUnaliased, sqlUnaliased, 1, nil)

	listU, err := eng.Complete(t.Context(), uriUnaliased, utf16PosAfter(t, sqlUnaliased, "shakespeare."))
	require.NoError(t, err)
	require.NotNil(t, listU)

	labelsU := make([]string, 0, len(listU.Items))
	for _, it := range listU.Items {
		labelsU = append(labelsU, it.Label)
	}
	assert.Contains(t, labelsU, "word")
	assert.Contains(t, labelsU, "word_count")

	// Hover on backtick-quoted table inside incomplete query also works.
	h, err := eng.Hover(t.Context(), uri, utf16Pos(t, sql, "shakespeare"))
	require.NoError(t, err)
	require.NotNil(t, h)
	assert.Contains(t, h.Contents.Value, "bigquery-public-data.samples.shakespeare")
	assert.Contains(t, h.Contents.Value, "Shakespeare word index")
}

func TestBacktickQualifiedTableColdAsyncFetchCompletion(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/projects/bigquery-public-data/datasets/samples/tables/shakespeare" {
			time.Sleep(30 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"tableReference": {"projectId": "bigquery-public-data", "datasetId": "samples", "tableId": "shakespeare"},
				"type": "TABLE",
				"schema": {
					"fields": [
						{"name": "word", "type": "STRING", "mode": "REQUIRED"},
						{"name": "word_count", "type": "INT64", "mode": "REQUIRED"}
					]
				}
			}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	opts := bqconfig.DefaultLSPOptions()
	opts.BigQueryAPIEnabled = true
	opts.CacheDir = t.TempDir()

	restClient := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithTokenProvider(func(_ context.Context) (string, error) { return "test-tok", nil }),
	)

	store, err := catalog.NewStore(opts, catalog.WithRestClient(restClient))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	eng, err := analysis.NewEngine(store, opts)
	require.NoError(t, err)

	// Open broken SQL on a completely cold cache and immediately ask for completion at a.|.
	sql := "SELECT a.\n  FROM `bigquery-public-data.samples.shakespeare` AS a"
	uri := "file:///cold_shakespeare.sql"
	eng.OpenDocument(uri, sql, 1, nil)

	list, err := eng.Complete(t.Context(), uri, utf16PosAfter(t, sql, "a."))
	require.NoError(t, err)
	require.NotNil(t, list)

	labels := make([]string, 0, len(list.Items))
	for _, it := range list.Items {
		labels = append(labels, it.Label)
	}
	assert.Contains(t, labels, "word")
	assert.Contains(t, labels, "word_count")
}

func TestTablePathCompletionOnlySuggestsProjectsDatasetsAndTables(t *testing.T) {
	t.Parallel()

	var dsCalls, tblCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/projects/bigquery-public-data/datasets":
			dsCalls++
			_, _ = w.Write([]byte(`{
				"datasets": [
					{"datasetReference": {"projectId": "bigquery-public-data", "datasetId": "samples"}},
					{"datasetReference": {"projectId": "bigquery-public-data", "datasetId": "github_repos"}}
				]
			}`))
		case "/projects/bigquery-public-data/datasets/samples/tables":
			tblCalls++
			_, _ = w.Write([]byte(`{
				"tables": [
					{"tableReference": {"projectId": "bigquery-public-data", "datasetId": "samples", "tableId": "shakespeare"}, "type": "TABLE"},
					{"tableReference": {"projectId": "bigquery-public-data", "datasetId": "samples", "tableId": "gsod"}, "type": "TABLE"},
					{"tableReference": {"projectId": "bigquery-public-data", "datasetId": "samples", "tableId": "natality"}, "type": "VIEW"}
				]
			}`))
		case "/projects/example.com:my-proj/datasets/my_ds/tables":
			_, _ = w.Write([]byte(`{
				"tables": [
					{"tableReference": {"projectId": "example.com:my-proj", "datasetId": "my_ds", "tableId": "orders"}, "type": "TABLE"}
				]
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	opts := bqconfig.DefaultLSPOptions()
	opts.DefaultProject = "my-proj"
	opts.DefaultDataset = "analytics"
	opts.BigQueryAPIEnabled = true
	opts.CacheDir = t.TempDir()

	restClient := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithTokenProvider(func(_ context.Context) (string, error) { return "tok", nil }),
	)

	store, err := catalog.NewStore(opts, catalog.WithRestClient(restClient))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	seedTestTables(store)

	eng, err := analysis.NewEngine(store, opts)
	require.NoError(t, err)

	labelsOf := func(list *analysis.CompletionList) []string {
		out := make([]string, 0, len(list.Items))
		for _, it := range list.Items {
			out = append(out, it.Label)
		}

		return out
	}

	// 1. FROM `bigquery-public-data.samples.| (unclosed backtick) -> ONLY table names.
	sql1 := "SELECT 1\n  FROM `bigquery-public-data.samples."
	uri1 := "file:///tp_tables_unclosed.sql"
	eng.OpenDocument(uri1, sql1, 1, nil)
	list1, err := eng.Complete(t.Context(), uri1, utf16PosAfter(t, sql1, "samples."))
	require.NoError(t, err)
	labels1 := labelsOf(list1)
	assert.ElementsMatch(t, []string{"gsod", "natality", "shakespeare"}, labels1)
	assert.NotContains(t, labels1, "SELECT")
	assert.NotContains(t, labels1, "DATE_TRUNC")
	assert.NotContains(t, labels1, "samples")

	// 2. FROM `bigquery-public-data.samples.|` (auto-closed backtick) -> hits cache, ONLY table names.
	sql2 := "SELECT 1\n  FROM `bigquery-public-data.samples.`"
	uri2 := "file:///tp_tables_closed.sql"
	eng.OpenDocument(uri2, sql2, 1, nil)
	list2, err := eng.Complete(t.Context(), uri2, utf16PosAfter(t, sql2, "samples."))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"gsod", "natality", "shakespeare"}, labelsOf(list2))
	assert.Equal(t, 1, tblCalls, "second table-list lookup must hit cache without a second REST call")

	// 3. FROM `bigquery-public-data.| -> ONLY dataset names in bigquery-public-data.
	sql3 := "SELECT 1\n  FROM `bigquery-public-data."
	uri3 := "file:///tp_datasets.sql"
	eng.OpenDocument(uri3, sql3, 1, nil)
	list3, err := eng.Complete(t.Context(), uri3, utf16PosAfter(t, sql3, "bigquery-public-data."))
	require.NoError(t, err)
	labels3 := labelsOf(list3)
	assert.ElementsMatch(t, []string{"github_repos", "samples"}, labels3)
	assert.NotContains(t, labels3, "ABS")
	assert.NotContains(t, labels3, "WHERE")
	assert.Equal(t, 1, dsCalls)

	// 4. FROM `| -> projects, datasets, tables only (no SQL functions/keywords).
	sql4 := "SELECT 1\n  FROM `"
	uri4 := "file:///tp_root_bt.sql"
	eng.OpenDocument(uri4, sql4, 1, nil)
	list4, err := eng.Complete(t.Context(), uri4, utf16PosAfter(t, sql4, "FROM `"))
	require.NoError(t, err)
	labels4 := labelsOf(list4)
	assert.Contains(t, labels4, "my-proj")
	assert.Contains(t, labels4, "analytics")
	assert.Contains(t, labels4, "events")
	assert.NotContains(t, labels4, "DATE_TRUNC")
	assert.NotContains(t, labels4, "SELECT")

	// 5. Domain-scoped GCP project with colon: FROM `example.com:my-proj.my_ds.| -> ONLY tables.
	sql5 := "SELECT 1 FROM `example.com:my-proj.my_ds."
	uri5 := "file:///tp_domain.sql"
	eng.OpenDocument(uri5, sql5, 1, nil)
	list5, err := eng.Complete(t.Context(), uri5, utf16PosAfter(t, sql5, "my_ds."))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"orders"}, labelsOf(list5))

	// 6. Unknown project/dataset -> empty list (NEVER leaks SQL functions/keywords).
	sql6 := "SELECT 1 FROM `unknown-proj.unknown-ds."
	uri6 := "file:///tp_unknown.sql"
	eng.OpenDocument(uri6, sql6, 1, nil)
	list6, err := eng.Complete(t.Context(), uri6, utf16PosAfter(t, sql6, "unknown-ds."))
	require.NoError(t, err)
	assert.Empty(t, list6.Items)

	// 7. EXTRACT(DAY FROM |) is an expression FROM, NOT a table-path FROM.
	sql7 := "SELECT EXTRACT(DAY FROM "
	uri7 := "file:///tp_extract.sql"
	eng.OpenDocument(uri7, sql7, 1, nil)
	list7, err := eng.Complete(t.Context(), uri7, utf16PosAfter(t, sql7, "FROM "))
	require.NoError(t, err)
	assert.Contains(t, labelsOf(list7), "CURRENT_DATE", "expression FROM inside EXTRACT must still offer SQL functions")
}

func TestCompletionInlineFieldTypesLabelDetails(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)

	// Table columns + partition/cluster badges + nested STRUCT field types.
	sql := "SELECT u. FROM events u"
	uri := "file:///field_types.sql"
	eng.OpenDocument(uri, sql, 1, nil)

	list, err := eng.Complete(t.Context(), uri, utf16PosAfter(t, sql, "u."))
	require.NoError(t, err)

	byLabel := make(map[string]analysis.CompletionItem, len(list.Items))
	for _, it := range list.Items {
		byLabel[it.Label] = it
	}

	evDate, ok := byLabel["event_date"]
	require.True(t, ok)
	require.NotNil(t, evDate.LabelDetails)
	assert.Contains(t, evDate.LabelDetails.Description, "DATE")
	assert.Contains(t, evDate.LabelDetails.Description, "[PARTITION]")

	tenantID, ok := byLabel["tenant_id"]
	require.True(t, ok)
	require.NotNil(t, tenantID.LabelDetails)
	assert.Contains(t, tenantID.LabelDetails.Description, "STRING")
	assert.Contains(t, tenantID.LabelDetails.Description, "[CLUSTER #1]")

	userCtx, ok := byLabel["user_ctx"]
	require.True(t, ok)
	require.NotNil(t, userCtx.LabelDetails)
	assert.Contains(t, userCtx.LabelDetails.Description, "STRUCT<")

	// CTE columns inherit types from their source table.
	sqlCTE := "WITH summary AS (SELECT event_date, tenant_id AS tid FROM events) SELECT s. FROM summary s"
	uriCTE := "file:///cte_field_types.sql"
	eng.OpenDocument(uriCTE, sqlCTE, 1, nil)

	cteList, err := eng.Complete(t.Context(), uriCTE, utf16PosAfter(t, sqlCTE, "s."))
	require.NoError(t, err)

	cteByLabel := make(map[string]analysis.CompletionItem, len(cteList.Items))
	for _, it := range cteList.Items {
		cteByLabel[it.Label] = it
	}

	require.NotNil(t, cteByLabel["event_date"].LabelDetails)
	assert.Equal(t, "DATE", cteByLabel["event_date"].LabelDetails.Description)
	require.NotNil(t, cteByLabel["tid"].LabelDetails)
	assert.Equal(t, "STRING", cteByLabel["tid"].LabelDetails.Description)
}
