package analysis_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/internal/analysis"
	"github.com/paulourio/bqlang/internal/bqconfig"
	"github.com/paulourio/bqlang/internal/catalog"
	"github.com/paulourio/bqlang/internal/syntax"
)

func TestTablePathCompletionPositiveCacheMatchAndExternalFallback(t *testing.T) {
	t.Parallel()

	var (
		mu       sync.RWMutex
		datasets = []string{"analytics", "billing"}
		tables   = []string{"events", "users"}
		dsHits   atomic.Int32
		tblHits  atomic.Int32
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/projects/curated-proj/datasets":
			dsHits.Add(1)
			mu.RLock()
			dsCopy := slices.Clone(datasets)
			mu.RUnlock()

			type item struct {
				DatasetReference catalog.DatasetRef `json:"datasetReference"`
			}
			var resp struct {
				Datasets []item `json:"datasets"`
			}
			for _, d := range dsCopy {
				resp.Datasets = append(resp.Datasets, item{
					DatasetReference: catalog.DatasetRef{Project: "curated-proj", Dataset: d},
				})
			}
			_ = json.NewEncoder(w).Encode(resp)

		case "/projects/curated-proj/datasets/analytics/tables":
			tblHits.Add(1)
			mu.RLock()
			tblCopy := slices.Clone(tables)
			mu.RUnlock()

			type item struct {
				TableReference catalog.TableRef `json:"tableReference"`
				Type           string           `json:"type"`
			}
			var resp struct {
				Tables []item `json:"tables"`
			}
			for _, tbl := range tblCopy {
				resp.Tables = append(resp.Tables, item{
					TableReference: catalog.TableRef{Project: "curated-proj", Dataset: "analytics", Table: tbl},
					Type:           "TABLE",
				})
			}
			_ = json.NewEncoder(w).Encode(resp)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	opts := bqconfig.DefaultLSPOptions()
	opts.DefaultProject = "curated-proj"
	opts.BigQueryAPIEnabled = true
	opts.CacheDir = t.TempDir()
	opts.ProjectCacheTTLSeconds = map[string]int{"curated-proj": 7 * 86400}

	store, err := catalog.NewStore(
		opts,
		catalog.WithRestClient(catalog.NewRestClient(
			catalog.WithBaseURL(srv.URL),
			catalog.WithHTTPClient(srv.Client()),
			catalog.WithTokenProvider(func(context.Context) (string, error) { return "", nil }),
		)),
	)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	engine, err := analysis.NewEngine(store, opts)
	require.NoError(t, err)

	docCfg := &analysis.DocumentConfig{Store: store, Options: opts}
	completeAtEnd := func(uri, sql string) *analysis.CompletionList {
		engine.OpenDocument(uri, sql, 1, docCfg)
		defer engine.CloseDocument(uri)

		lines := strings.Split(sql, "\n")
		lastLine := len(lines) - 1
		col := len(lines[lastLine])
		list, compErr := engine.Complete(t.Context(), uri, syntax.Position{Line: lastLine, Character: col})
		require.NoError(t, compErr)
		require.NotNil(t, list)
		return list
	}

	collectLabels := func(list *analysis.CompletionList) []string {
		out := make([]string, 0, len(list.Items))
		for _, it := range list.Items {
			out = append(out, it.Label)
		}
		return out
	}

	// 1. Initial discovery populates dataset & table caches; list is marked IsIncomplete so editors re-query on uncached prefixes.
	initDS := completeAtEnd("file:///init_ds.sql", "SELECT id FROM `curated-proj.")
	assert.True(t, initDS.IsIncomplete)
	assert.Contains(t, collectLabels(initDS), "analytics")

	initTbl := completeAtEnd("file:///init_tbl.sql", "SELECT id FROM `curated-proj.analytics.")
	assert.True(t, initTbl.IsIncomplete)
	assert.Contains(t, collectLabels(initTbl), "events")

	// 2. Add newly created dataset & table externally on BigQuery.
	mu.Lock()
	datasets = append(datasets, "marketing")
	tables = append(tables, "conversions")
	mu.Unlock()

	dsBefore := dsHits.Load()
	tblBefore := tblHits.Load()

	// Positive prefix matches use the cache without hitting BigQuery.
	posDS := completeAtEnd("file:///pos_ds.sql", "SELECT id FROM curated-proj.ana")
	assert.Contains(t, collectLabels(posDS), "analytics")
	assert.Equal(t, dsBefore, dsHits.Load())

	posTbl := completeAtEnd("file:///pos_tbl.sql", "SELECT id FROM `curated-proj.analytics.ev")
	assert.Contains(t, collectLabels(posTbl), "events")
	assert.Equal(t, tblBefore, tblHits.Load())

	// CTE positive match at root table path must NOT trigger unnecessary external datasets.list recheck.
	posCTE := completeAtEnd("file:///pos_cte.sql", "WITH recent_orders AS (SELECT 1 AS id) SELECT id FROM recent_o")
	assert.Contains(t, collectLabels(posCTE), "recent_orders")
	assert.Equal(t, dsBefore, dsHits.Load())

	// Unmatched prefixes ("no presence" in cache) check BigQuery externally and discover new objects immediately!
	missDS := completeAtEnd("file:///miss_ds.sql", "SELECT id FROM `curated-proj.mark")
	assert.Contains(t, collectLabels(missDS), "marketing")
	assert.Greater(t, dsHits.Load(), dsBefore)

	missTbl := completeAtEnd("file:///miss_tbl.sql", "SELECT id FROM `curated-proj.analytics.conv")
	assert.Contains(t, collectLabels(missTbl), "conversions")
	assert.Greater(t, tblHits.Load(), tblBefore)
}
