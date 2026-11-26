package catalog_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/bqconfig"
	"github.com/paulourio/bqlang/catalog"
)

func TestCrossSessionCacheProjectTTLsTempDatasetsAndPositiveMatch(t *testing.T) {
	t.Parallel()

	var (
		stateMu         sync.RWMutex
		curatedDatasets = []string{"analytics", "billing", "_script_session", "_temp_scratch"}
		curatedTables   = []string{"events", "users"}
		datasetListHits atomic.Int32
		tableListHits   atomic.Int32
		tableGetHits    atomic.Int32
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/projects/default-proj/datasets":
			datasetListHits.Add(1)
			_, _ = w.Write([]byte(`{"datasets":[
				{"datasetReference":{"projectId":"default-proj","datasetId":"core"}},
				{"datasetReference":{"projectId":"default-proj","datasetId":"_anon_tmp"}}
			]}`))

		case "/projects/curated-proj/datasets":
			datasetListHits.Add(1)
			stateMu.RLock()
			dsCopy := slices.Clone(curatedDatasets)
			stateMu.RUnlock()

			type dsItem struct {
				DatasetReference catalog.DatasetRef `json:"datasetReference"`
			}
			resp := struct {
				Datasets []dsItem `json:"datasets"`
			}{}
			for _, ds := range dsCopy {
				resp.Datasets = append(resp.Datasets, dsItem{
					DatasetReference: catalog.DatasetRef{Project: "curated-proj", Dataset: ds},
				})
			}
			_ = json.NewEncoder(w).Encode(resp)

		case "/projects/empty-proj/datasets":
			datasetListHits.Add(1)
			_, _ = w.Write([]byte(`{"datasets":[]}`))

		case "/projects/curated-proj/datasets/analytics/tables":
			tableListHits.Add(1)
			stateMu.RLock()
			tblCopy := slices.Clone(curatedTables)
			stateMu.RUnlock()

			type tblItem struct {
				TableReference catalog.TableRef `json:"tableReference"`
				Type           string           `json:"type"`
			}
			resp := struct {
				Tables []tblItem `json:"tables"`
			}{}
			for _, tbl := range tblCopy {
				resp.Tables = append(resp.Tables, tblItem{
					TableReference: catalog.TableRef{Project: "curated-proj", Dataset: "analytics", Table: tbl},
					Type:           "TABLE",
				})
			}
			_ = json.NewEncoder(w).Encode(resp)

		case "/projects/curated-proj/datasets/_script_session/tables":
			tableListHits.Add(1)
			_, _ = w.Write([]byte(`{"tables":[
				{"tableReference":{"projectId":"curated-proj","datasetId":"_script_session","tableId":"tmp_result"},"type":"TABLE"}
			]}`))

		default:
			if strings.Contains(r.URL.Path, "/tables/") {
				tableGetHits.Add(1)
				parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
				// projects/{p}/datasets/{d}/tables/{t}
				if len(parts) == 6 {
					resp := map[string]any{
						"tableReference": map[string]string{
							"projectId": parts[1],
							"datasetId": parts[3],
							"tableId":   parts[5],
						},
						"type": "TABLE",
						"schema": map[string]any{
							"fields": []map[string]string{
								{"name": "id", "type": "INT64"},
							},
						},
					}
					_ = json.NewEncoder(w).Encode(resp)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	rest := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithHTTPClient(srv.Client()),
		catalog.WithTokenProvider(func(context.Context) (string, error) { return "", nil }),
	)

	cacheDir := t.TempDir()
	baseNow := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	curNow := baseNow
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return curNow
	}
	setClock := func(t time.Time) {
		clockMu.Lock()
		curNow = t
		clockMu.Unlock()
	}

	makeOpts := func(apiEnabled bool) *bqconfig.LSPOptions {
		opts := bqconfig.DefaultLSPOptions() // 24h (86400s) default
		opts.CacheDir = cacheDir
		opts.BigQueryAPIEnabled = apiEnabled
		opts.ProjectCacheTTLSeconds = map[string]int{
			"curated-proj": 7 * 86400, // 7 days extended TTL
		}
		return opts
	}

	// --- Session 1: Populate default-proj (24h), curated-proj (7d), and _script_session (temp). ---
	store1, err := catalog.NewStore(makeOpts(true), catalog.WithRestClient(rest), catalog.WithClock(clock))
	require.NoError(t, err)

	dsDefault, inc := store1.DiscoverDatasets(t.Context(), "default-proj", 2*time.Second)
	assert.False(t, inc)
	assert.Contains(t, dsDefault, "core")

	dsCurated, inc := store1.DiscoverDatasets(t.Context(), "curated-proj", 2*time.Second)
	assert.False(t, inc)
	assert.Contains(t, dsCurated, "analytics")
	assert.Contains(t, dsCurated, "_script_session", "temp _ dataset usable in active session")

	tblsCurated, inc := store1.DiscoverTables(t.Context(), "curated-proj", "analytics", 2*time.Second)
	assert.False(t, inc)
	assert.Len(t, tblsCurated, 2)

	tblsTemp, inc := store1.DiscoverTables(t.Context(), "curated-proj", "_script_session", 2*time.Second)
	assert.False(t, inc)
	assert.Len(t, tblsTemp, 1)

	_, ok := store1.WaitForTable(t.Context(), catalog.TableRef{Project: "default-proj", Dataset: "core", Table: "orders"}, 2*time.Second)
	require.True(t, ok)
	_, ok = store1.WaitForTable(t.Context(), catalog.TableRef{Project: "curated-proj", Dataset: "analytics", Table: "events"}, 2*time.Second)
	require.True(t, ok)
	_, ok = store1.WaitForTable(t.Context(), catalog.TableRef{Project: "curated-proj", Dataset: "_script_session", Table: "tmp_result"}, 2*time.Second)
	require.True(t, ok, "temp _ dataset table resolves in memory during active session")

	// Empty dataset list must NOT be negatively cached.
	empty1, _ := store1.DiscoverDatasets(t.Context(), "empty-proj", 2*time.Second)
	assert.Empty(t, empty1)
	hitsBeforeEmptyRecheck := datasetListHits.Load()
	_, _ = store1.DiscoverDatasets(t.Context(), "empty-proj", 2*time.Second)
	assert.Greater(t, datasetListHits.Load(), hitsBeforeEmptyRecheck, "empty dataset list must always recheck externally")

	require.NoError(t, store1.Close())

	// --- Session 2 (+2h, offline): Reuses disk cache for normal datasets/tables, excludes _ temp datasets! ---
	setClock(baseNow.Add(2 * time.Hour))
	store2, err := catalog.NewStore(makeOpts(false), catalog.WithClock(clock))
	require.NoError(t, err)

	dsCuratedS2, _ := store2.DiscoverDatasets(t.Context(), "curated-proj", 0)
	assert.Equal(t, []string{"analytics", "billing"}, dsCuratedS2, "_-prefixed temp datasets must not persist across sessions")

	dsDefaultS2, _ := store2.DiscoverDatasets(t.Context(), "default-proj", 0)
	assert.Equal(t, []string{"core"}, dsDefaultS2)

	tblsTempS2, _ := store2.DiscoverTables(t.Context(), "curated-proj", "_script_session", 0)
	assert.Empty(t, tblsTempS2, "_-prefixed temp dataset table list must not persist across sessions")

	_, ok = store2.GetTable(catalog.TableRef{Project: "curated-proj", Dataset: "_script_session", Table: "tmp_result"})
	assert.False(t, ok, "_-prefixed temp dataset table schema must not persist across sessions")

	_, ok = store2.GetTable(catalog.TableRef{Project: "default-proj", Dataset: "core", Table: "orders"})
	assert.True(t, ok, "default-proj table schema cached within 24h TTL")

	_, ok = store2.GetTable(catalog.TableRef{Project: "curated-proj", Dataset: "analytics", Table: "events"})
	assert.True(t, ok, "curated-proj table schema cached within 7d TTL")

	require.NoError(t, store2.Close())

	// --- Session 3 (+48h, offline): default-proj (24h) expired; curated-proj (7d) still hits positively! ---
	setClock(baseNow.Add(48 * time.Hour))
	store3, err := catalog.NewStore(makeOpts(false), catalog.WithClock(clock))
	require.NoError(t, err)

	assert.Equal(t, 24*time.Hour, store3.EffectiveCacheTTL("default-proj"))
	assert.Equal(t, 7*24*time.Hour, store3.EffectiveCacheTTL("curated-proj"))

	dsDefaultS3, _ := store3.DiscoverDatasets(t.Context(), "default-proj", 0)
	assert.Empty(t, dsDefaultS3, "default-proj dataset list must expire after 24h")
	_, ok = store3.GetTable(catalog.TableRef{Project: "default-proj", Dataset: "core", Table: "orders"})
	assert.False(t, ok, "default-proj table schema must expire after 24h")

	dsCuratedS3, _ := store3.DiscoverDatasets(t.Context(), "curated-proj", 0)
	assert.Equal(t, []string{"analytics", "billing"}, dsCuratedS3, "curated-proj dataset list must survive at 48h under 7d TTL")
	tblsCuratedS3, _ := store3.DiscoverTables(t.Context(), "curated-proj", "analytics", 0)
	assert.Len(t, tblsCuratedS3, 2, "curated-proj table list must survive at 48h under 7d TTL")
	_, ok = store3.GetTable(catalog.TableRef{Project: "curated-proj", Dataset: "analytics", Table: "events"})
	assert.True(t, ok, "curated-proj table schema must survive at 48h under 7d TTL")

	require.NoError(t, store3.Close())

	// --- Session 4 (+48h, online): Positive match uses cache; no presence checks BigQuery externally! ---
	stateMu.Lock()
	curatedDatasets = append(curatedDatasets, "marketing")
	curatedTables = append(curatedTables, "conversions")
	stateMu.Unlock()

	store4, err := catalog.NewStore(makeOpts(true), catalog.WithRestClient(rest), catalog.WithClock(clock))
	require.NoError(t, err)
	defer func() { _ = store4.Close() }()

	dsHitsBefore := datasetListHits.Load()
	tblHitsBefore := tableListHits.Load()
	getHitsBefore := tableGetHits.Load()

	// Positive matches hit cache with 0 external HTTP calls.
	posDS, _ := store4.DiscoverDatasetsMatching(t.Context(), "curated-proj", "ana", 2*time.Second)
	assert.Contains(t, posDS, "analytics")
	posTbls, _ := store4.DiscoverTablesMatching(t.Context(), "curated-proj", "analytics", "ev", 2*time.Second)
	assert.Len(t, posTbls, 2)
	_, ok = store4.GetTable(catalog.TableRef{Project: "curated-proj", Dataset: "analytics", Table: "events"})
	assert.True(t, ok)

	assert.Equal(t, dsHitsBefore, datasetListHits.Load(), "positive dataset prefix match must not call BigQuery")
	assert.Equal(t, tblHitsBefore, tableListHits.Load(), "positive table prefix match must not call BigQuery")
	assert.Equal(t, getHitsBefore, tableGetHits.Load(), "positive table schema hit must not call BigQuery")

	// No presence in cache MUST check externally even though curated-proj cache is still within 7d TTL!
	missDS, _ := store4.DiscoverDatasetsMatching(t.Context(), "curated-proj", "mark", 2*time.Second)
	assert.Contains(t, missDS, "marketing", "uncached dataset prefix must be checked externally")
	assert.Greater(t, datasetListHits.Load(), dsHitsBefore)

	missTbls, _ := store4.DiscoverTablesMatching(t.Context(), "curated-proj", "analytics", "conv", 2*time.Second)
	var tblNames []string
	for _, ts := range missTbls {
		tblNames = append(tblNames, ts.Ref.Table)
	}
	assert.Contains(t, tblNames, "conversions", "uncached table prefix must be checked externally")
	assert.Greater(t, tableListHits.Load(), tblHitsBefore)

	brandNewTable := catalog.TableRef{Project: "curated-proj", Dataset: "marketing", Table: "campaigns"}
	meta, ok := store4.WaitForTable(t.Context(), brandNewTable, 2*time.Second)
	require.True(t, ok, "uncached table schema must be fetched externally despite cached lists")
	assert.Equal(t, "curated-proj.marketing.campaigns", meta.Ref.FullName())
}

func TestDiskCacheExpirationAndTempDatasetCleanupPolicy(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	now := time.Date(2026, 11, 26, 15, 0, 0, 0, time.UTC)

	// Seed SQLite cache.db with fresh, expired (default 24h), extended-fresh (3d < 7d),
	// extended-expired (10d > 7d), and legacy _temp_ds rows.
	optsSeed := bqconfig.DefaultLSPOptions()
	optsSeed.CacheDir = cacheDir
	optsSeed.ProjectCacheTTLSeconds = map[string]int{"extended-proj": 7 * 86400}

	var clockMu sync.Mutex
	curTime := now
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return curTime
	}

	seedStore, err := catalog.NewStore(optsSeed, catalog.WithClock(clock))
	require.NoError(t, err)
	// Force creation of cache.db via one valid write.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema":{"fields":[{"name":"id","type":"INT64"}]}}`))
	}))
	defer srv.Close()
	require.NoError(t, seedStore.Close())

	optsOnline := *optsSeed
	optsOnline.BigQueryAPIEnabled = true
	writerStore, err := catalog.NewStore(
		&optsOnline,
		catalog.WithClock(clock),
		catalog.WithRestClient(catalog.NewRestClient(
			catalog.WithBaseURL(srv.URL),
			catalog.WithHTTPClient(srv.Client()),
			catalog.WithTokenProvider(func(context.Context) (string, error) { return "", nil }),
		)),
	)
	require.NoError(t, err)
	_, ok := writerStore.WaitForTable(t.Context(), catalog.TableRef{Project: "default-proj", Dataset: "ds", Table: "fresh_tbl"}, 2*time.Second)
	require.True(t, ok)
	require.NoError(t, writerStore.Close())

	dbPath := filepath.Join(cacheDir, catalog.DiskCacheFilename)
	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)

	insertRow := func(proj, ds, tbl string, age time.Duration) {
		ts := now.Add(-age).UnixNano()
		_, execErr := rawDB.ExecContext(
			t.Context(),
			`INSERT OR REPLACE INTO table_cache (
				project_lower, dataset_lower, table_lower,
				project, dataset, table_name, table_type, friendly_name,
				payload, fetched_at
			) VALUES (?, ?, ?, ?, ?, ?, 'TABLE', '', ?, ?)`,
			strings.ToLower(proj),
			strings.ToLower(ds),
			strings.ToLower(tbl),
			proj,
			ds,
			tbl,
			[]byte(`{"schema":{"fields":[{"name":"id","type":"INT64"}]}}`),
			ts,
		)
		require.NoError(t, execErr)

		_, execErr = rawDB.ExecContext(
			t.Context(),
			`INSERT OR REPLACE INTO dataset_list_cache (project_lower, project, payload, fetched_at)
			 VALUES (?, ?, ?, ?)`,
			strings.ToLower(proj),
			proj,
			[]byte(`["`+ds+`"]`),
			ts,
		)
		require.NoError(t, execErr)

		_, execErr = rawDB.ExecContext(
			t.Context(),
			`INSERT OR REPLACE INTO table_list_cache (project_lower, dataset_lower, project, dataset, payload, fetched_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			strings.ToLower(proj),
			strings.ToLower(ds),
			proj,
			ds,
			[]byte(`[{"tableReference":{"projectId":"`+proj+`","datasetId":"`+ds+`","tableId":"`+tbl+`"}}]`),
			ts,
		)
		require.NoError(t, execErr)
	}

	insertRow("old-default-proj", "ds", "expired_25h", 25*time.Hour)
	insertRow("extended-proj", "curated", "fresh_3d", 72*time.Hour)
	insertRow("expired-extended", "curated", "expired_10d", 240*time.Hour)
	// Also test expired row inside extended-proj itself (>7d) while keeping fresh_3d (<7d).
	_, err = rawDB.ExecContext(
		t.Context(),
		`INSERT INTO table_cache (
			project_lower, dataset_lower, table_lower,
			project, dataset, table_name, table_type, friendly_name,
			payload, fetched_at
		) VALUES ('extended-proj', 'curated', 'stale_10d', 'extended-proj', 'curated', 'stale_10d', 'TABLE', '', ?, ?)`,
		[]byte(`{"schema":{"fields":[{"name":"id","type":"INT64"}]}}`),
		now.Add(-10*24*time.Hour).UnixNano(),
	)
	require.NoError(t, err)
	// Legacy temp _ dataset row even with a fresh timestamp must be purged.
	insertRow("extended-proj", "_temp_legacy", "leak_tbl", 5*time.Minute)
	require.NoError(t, rawDB.Close())

	optsPrune := bqconfig.DefaultLSPOptions()
	optsPrune.CacheDir = cacheDir
	optsPrune.BigQueryAPIEnabled = true
	optsPrune.ProjectCacheTTLSeconds = map[string]int{
		"extended-proj":    7 * 86400,
		"expired-extended": 7 * 86400,
	}

	// Opening a new session and performing a single write automatically triggers
	// cross-session expiration and temp-dataset cleanup (even well below 64 writes).
	pruneStore, err := catalog.NewStore(
		optsPrune,
		catalog.WithClock(clock),
		catalog.WithRestClient(catalog.NewRestClient(
			catalog.WithBaseURL(srv.URL),
			catalog.WithHTTPClient(srv.Client()),
			catalog.WithTokenProvider(func(context.Context) (string, error) { return "", nil }),
		)),
	)
	require.NoError(t, err)
	_, ok = pruneStore.WaitForTable(t.Context(), catalog.TableRef{Project: "default-proj", Dataset: "ds", Table: "trigger_write"}, 2*time.Second)
	require.True(t, ok)
	require.NoError(t, pruneStore.Close())

	verifyDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer func() { _ = verifyDB.Close() }()

	countRows := func(query string, args ...any) int {
		var n int
		require.NoError(t, verifyDB.QueryRowContext(t.Context(), query, args...).Scan(&n))
		return n
	}

	assert.Equal(t, 1, countRows(`SELECT COUNT(*) FROM table_cache WHERE project_lower = 'default-proj' AND table_lower = 'fresh_tbl'`))
	assert.Equal(t, 0, countRows(`SELECT COUNT(*) FROM table_cache WHERE project_lower = 'old-default-proj'`), "25h default-TTL table must be pruned")
	assert.Equal(t, 0, countRows(`SELECT COUNT(*) FROM dataset_list_cache WHERE project_lower = 'old-default-proj'`), "25h default-TTL dataset list must be pruned")
	assert.Equal(t, 0, countRows(`SELECT COUNT(*) FROM table_list_cache WHERE project_lower = 'old-default-proj'`), "25h default-TTL table list must be pruned")

	assert.Equal(t, 1, countRows(`SELECT COUNT(*) FROM table_cache WHERE project_lower = 'extended-proj' AND table_lower = 'fresh_3d'`), "3d table in 7d extended project must be retained")
	assert.Equal(t, 0, countRows(`SELECT COUNT(*) FROM table_cache WHERE project_lower = 'extended-proj' AND table_lower = 'stale_10d'`), "10d table in 7d extended project must be pruned")
	assert.Equal(t, 0, countRows(`SELECT COUNT(*) FROM table_cache WHERE project_lower = 'expired-extended'`), "10d extended project rows must be pruned")
	assert.Equal(t, 0, countRows(`SELECT COUNT(*) FROM dataset_list_cache WHERE project_lower = 'expired-extended'`))
	assert.Equal(t, 0, countRows(`SELECT COUNT(*) FROM table_list_cache WHERE project_lower = 'expired-extended'`))

	assert.Equal(t, 0, countRows(`SELECT COUNT(*) FROM table_cache WHERE dataset_lower LIKE '\_%' ESCAPE '\'`), "_-prefixed temp datasets must be pruned from table_cache")
	assert.Equal(t, 0, countRows(`SELECT COUNT(*) FROM table_list_cache WHERE dataset_lower LIKE '\_%' ESCAPE '\'`), "_-prefixed temp datasets must be pruned from table_list_cache")
}
