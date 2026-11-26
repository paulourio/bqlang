package catalog_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/internal/bqconfig"
	"github.com/paulourio/bqlang/internal/catalog"
)

const giB int64 = 1 << 30

type dryRunPayload struct {
	Configuration struct {
		DryRun bool `json:"dryRun"`
		Query  struct {
			Query string `json:"query"`
		} `json:"query"`
	} `json:"configuration"`
}

func decodeDryRunSQL(r *http.Request) string {
	var p dryRunPayload
	_ = json.NewDecoder(r.Body).Decode(&p)

	return p.Configuration.Query.Query
}

func writeDryRunOK(w http.ResponseWriter, bytes int64, refTables ...catalog.TableRef) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"statistics": map[string]any{
			"totalBytesProcessed": bytes,
			"query": map[string]any{
				"totalBytesProcessed":         bytes,
				"totalBytesBilled":            bytes,
				"totalBytesProcessedAccuracy": "PRECISE",
				"statementType":               "SELECT",
				"referencedTables":            refTables,
			},
		},
	})
}

func writeTableMetaJSON(w http.ResponseWriter, meta *catalog.TableMeta) {
	w.Header().Set("Content-Type", "application/json")
	data, err := meta.MarshalJSON()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	_, _ = w.Write(data)
}

func newViewProbeTestStore(
	t *testing.T,
	serverURL, cacheDir string,
	extraOpts ...catalog.StoreOption,
) *catalog.Store {
	t.Helper()

	opts := bqconfig.DefaultLSPOptions()
	opts.DefaultProject = "proj"
	opts.DefaultDataset = "ds"
	opts.BigQueryAPIEnabled = true
	opts.CacheDir = cacheDir

	rest := catalog.NewRestClient(
		catalog.WithBaseURL(serverURL),
		catalog.WithTokenProvider(func(_ context.Context) (string, error) { return "tok", nil }),
	)

	storeOpts := append([]catalog.StoreOption{catalog.WithRestClient(rest)}, extraOpts...)
	store, err := catalog.NewStore(opts, storeOpts...)
	require.NoError(t, err)

	return store
}

func waitUntilProbed(t *testing.T, store *catalog.Store, ref catalog.TableRef) *catalog.TableMeta {
	t.Helper()

	var got *catalog.TableMeta
	require.Eventually(t, func() bool {
		meta, ok := store.GetTable(ref)
		if ok && meta != nil && meta.PartitionProbed {
			got = meta
			return true
		}

		return false
	}, 5*time.Second, 10*time.Millisecond, "timed out waiting for %s to complete view partition probe", ref.FullName())

	return got
}

func TestTableMetaPartitionProbedJSONAndClone(t *testing.T) {
	t.Parallel()

	orig := &catalog.TableMeta{
		Ref:             catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_events"},
		Type:            catalog.TableTypeView,
		PartitionProbed: true,
		Schema:          []catalog.ColumnMeta{{Name: "event_date", Type: "DATE"}},
	}

	cloned := orig.Clone()
	assert.True(t, cloned.PartitionProbed)

	raw, err := orig.MarshalJSON()
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"partitionProbed":true`)

	var decoded catalog.TableMeta
	require.NoError(t, decoded.UnmarshalJSON(raw))
	assert.True(t, decoded.PartitionProbed)

	// Zero value must be omitted from JSON.
	unprobed := &catalog.TableMeta{
		Ref:  catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_plain"},
		Type: catalog.TableTypeView,
	}
	rawUnprobed, err := unprobed.MarshalJSON()
	require.NoError(t, err)
	assert.NotContains(t, string(rawUnprobed), "partitionProbed")
}

func TestViewProbeStage1RequirePartitionFilterAndDiskCacheNoReprobe(t *testing.T) {
	t.Parallel()

	viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_req"}
	var (
		mu       sync.Mutex
		dryRuns  []string
		getCalls atomic.Int32
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			sqlText := decodeDryRunSQL(r)
			mu.Lock()
			dryRuns = append(dryRuns, sqlText)
			mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "Cannot query over table 'proj.raw.events_base' without a filter over column(s) 'event_date' that can be used for partition elimination"}}`))
			return
		}

		getCalls.Add(1)
		writeTableMetaJSON(w, &catalog.TableMeta{
			Ref:  viewRef,
			Type: catalog.TableTypeView,
			Schema: []catalog.ColumnMeta{
				{Name: "event_date", Type: "DATE"},
				{Name: "user_id", Type: "STRING"},
			},
		})
	}))
	t.Cleanup(srv.Close)

	cacheDir := t.TempDir()
	updates := make(chan catalog.TableRef, 8)
	store := newViewProbeTestStore(t, srv.URL, cacheDir, catalog.WithOnUpdate(func(ref catalog.TableRef) {
		updates <- ref
	}))

	_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
	require.True(t, ok)

	probed := waitUntilProbed(t, store, viewRef)
	require.NotNil(t, probed.EffectivePartitioning())
	assert.Equal(t, catalog.PartitionKindTime, probed.EffectivePartitioning().Kind)
	assert.Equal(t, "event_date", probed.EffectivePartitioning().Field)
	assert.True(t, probed.EffectiveRequirePartitionFilter())
	assert.True(t, probed.IsPartitionColumn("event_date"))
	assert.True(t, probed.PartitionProbed)

	// Expect two onUpdate calls: initial table fetch + Stage 1 partition enrichment.
	require.Eventually(t, func() bool {
		return len(updates) >= 2
	}, 3*time.Second, 10*time.Millisecond)

	mu.Lock()
	require.Len(t, dryRuns, 1)
	assert.Equal(t, "SELECT * FROM `proj`.`ds`.`v_req`", dryRuns[0]) //nolint:unqueryvet // verifying exact Stage 1 probe SQL
	mu.Unlock()

	require.NoError(t, store.Close())

	// Reopen Store against same SQLite cache.db with API enabled: must NOT re-probe.
	store2 := newViewProbeTestStore(t, srv.URL, cacheDir)
	t.Cleanup(func() { _ = store2.Close() })

	reloaded, ok := store2.GetTable(viewRef)
	require.True(t, ok)
	assert.True(t, reloaded.PartitionProbed)
	assert.True(t, reloaded.EffectiveRequirePartitionFilter())
	assert.True(t, reloaded.IsPartitionColumn("event_date"))

	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	assert.Len(t, dryRuns, 1, "reloaded probed view must not trigger another dry-run")
	mu.Unlock()
	assert.Equal(t, int32(1), getCalls.Load())
}

func TestViewProbeStage1Deterministic400AndTransientBackoff(t *testing.T) {
	t.Parallel()

	t.Run("deterministic 400 marks PartitionProbed", func(t *testing.T) {
		t.Parallel()

		viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_broken"}
		var dryRunCount atomic.Int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				dryRunCount.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "Syntax error in view definition"}}`))
				return
			}

			writeTableMetaJSON(w, &catalog.TableMeta{
				Ref:    viewRef,
				Type:   catalog.TableTypeView,
				Schema: []catalog.ColumnMeta{{Name: "event_date", Type: "DATE"}},
			})
		}))
		t.Cleanup(srv.Close)

		cacheDir := t.TempDir()
		store := newViewProbeTestStore(t, srv.URL, cacheDir)

		_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
		require.True(t, ok)

		probed := waitUntilProbed(t, store, viewRef)
		assert.Nil(t, probed.EffectivePartitioning())
		assert.True(t, probed.PartitionProbed)
		assert.Equal(t, int32(1), dryRunCount.Load())
		require.NoError(t, store.Close())

		// Reload from SQLite must not re-probe.
		store2 := newViewProbeTestStore(t, srv.URL, cacheDir)
		t.Cleanup(func() { _ = store2.Close() })

		reloaded, ok := store2.GetTable(viewRef)
		require.True(t, ok)
		assert.True(t, reloaded.PartitionProbed)
		time.Sleep(100 * time.Millisecond)
		assert.Equal(t, int32(1), dryRunCount.Load())
	})

	t.Run("transient 503 backs off without marking PartitionProbed and retries", func(t *testing.T) {
		t.Parallel()

		viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_flaky"}
		var dryRunCount atomic.Int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				n := dryRunCount.Add(1)
				if n == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"error": {"code": 503, "message": "backend unavailable"}}`))
					return
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "Cannot query over table 'proj.raw.base' without a filter over column(s) 'event_date' that can be used for partition elimination"}}`))
				return
			}

			writeTableMetaJSON(w, &catalog.TableMeta{
				Ref:    viewRef,
				Type:   catalog.TableTypeView,
				Schema: []catalog.ColumnMeta{{Name: "event_date", Type: "DATE"}},
			})
		}))
		t.Cleanup(srv.Close)

		now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
		var clock atomic.Pointer[time.Time]
		clock.Store(&now)

		store := newViewProbeTestStore(
			t,
			srv.URL,
			t.TempDir(),
			catalog.WithClock(func() time.Time { return *clock.Load() }),
		)
		t.Cleanup(func() { _ = store.Close() })

		_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
		require.True(t, ok)

		require.Eventually(t, func() bool {
			return dryRunCount.Load() == 1
		}, 3*time.Second, 10*time.Millisecond)

		// Give worker a moment to record negative backoff, then confirm PartitionProbed stays false.
		time.Sleep(50 * time.Millisecond)
		meta, ok := store.GetTable(viewRef)
		require.True(t, ok)
		assert.False(t, meta.PartitionProbed)
		assert.Equal(t, int32(1), dryRunCount.Load(), "immediate lookup during backoff must not re-probe")

		// Advance clock past 30s backoff and re-read table to trigger retry.
		later := now.Add(35 * time.Second)
		clock.Store(&later)
		_, ok = store.GetTable(viewRef)
		require.True(t, ok)

		probed := waitUntilProbed(t, store, viewRef)
		assert.True(t, probed.PartitionProbed)
		assert.True(t, probed.EffectiveRequirePartitionFilter())
		assert.True(t, probed.IsPartitionColumn("event_date"))
		assert.Equal(t, int32(2), dryRunCount.Load())
	})

	t.Run("ClearNegativeCache allows immediate retry without waiting for backoff", func(t *testing.T) {
		t.Parallel()

		viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_clear_neg"}
		var dryRunCount atomic.Int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				if dryRunCount.Add(1) == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}

				writeDryRunOK(w, 10*giB) // below threshold -> marks PartitionProbed
				return
			}

			writeTableMetaJSON(w, &catalog.TableMeta{
				Ref:    viewRef,
				Type:   catalog.TableTypeView,
				Schema: []catalog.ColumnMeta{{Name: "event_date", Type: "DATE"}},
			})
		}))
		t.Cleanup(srv.Close)

		now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
		store := newViewProbeTestStore(
			t,
			srv.URL,
			t.TempDir(),
			catalog.WithClock(func() time.Time { return now }),
		)
		t.Cleanup(func() { _ = store.Close() })

		_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
		require.True(t, ok)

		require.Eventually(t, func() bool {
			return dryRunCount.Load() == 1
		}, 3*time.Second, 10*time.Millisecond)
		time.Sleep(50 * time.Millisecond)

		store.ClearNegativeCache()
		_, ok = store.GetTable(viewRef)
		require.True(t, ok)

		probed := waitUntilProbed(t, store, viewRef)
		assert.True(t, probed.PartitionProbed)
		assert.Equal(t, int32(2), dryRunCount.Load())
	})
}

func TestViewProbeStage2BaseTablePartitioningAndNoClusteringCopy(t *testing.T) {
	t.Parallel()

	t.Run("time partitioned base table copies partitioning only", func(t *testing.T) {
		t.Parallel()

		viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_events"}
		baseRef := catalog.TableRef{Project: "proj", Dataset: "raw", Table: "events_base"}
		var dryRunCount, baseGetCount atomic.Int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				dryRunCount.Add(1)
				writeDryRunOK(w, 250*giB, baseRef)
				return
			}

			if strings.HasSuffix(r.URL.Path, "/tables/events_base") {
				baseGetCount.Add(1)
				writeTableMetaJSON(w, &catalog.TableMeta{
					Ref:  baseRef,
					Type: catalog.TableTypeTable,
					TimePartitioning: &catalog.TimePartitioning{
						Type:                   "DAY",
						Field:                  "event_date",
						RequirePartitionFilter: false,
					},
					Clustering: &catalog.ClusteringInfo{
						Fields: []string{"user_id", "country"},
					},
					Schema: []catalog.ColumnMeta{
						{Name: "event_date", Type: "DATE"},
						{Name: "user_id", Type: "STRING"},
						{Name: "country", Type: "STRING"},
					},
				})
				return
			}

			writeTableMetaJSON(w, &catalog.TableMeta{
				Ref:  viewRef,
				Type: catalog.TableTypeView,
				Schema: []catalog.ColumnMeta{
					{Name: "event_date", Type: "DATE"},
					{Name: "user_id", Type: "STRING"},
					{Name: "country", Type: "STRING"},
				},
			})
		}))
		t.Cleanup(srv.Close)

		cacheDir := t.TempDir()
		updates := make(chan catalog.TableRef, 8)
		store := newViewProbeTestStore(t, srv.URL, cacheDir, catalog.WithOnUpdate(func(ref catalog.TableRef) {
			updates <- ref
		}))

		_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
		require.True(t, ok)

		probed := waitUntilProbed(t, store, viewRef)
		require.NotNil(t, probed.EffectivePartitioning())
		assert.Equal(t, catalog.PartitionKindTime, probed.EffectivePartitioning().Kind)
		assert.Equal(t, "DAY", probed.EffectivePartitioning().Type)
		assert.Equal(t, "event_date", probed.EffectivePartitioning().Field)
		assert.False(t, probed.EffectiveRequirePartitionFilter())
		assert.Nil(t, probed.Clustering, "Stage 2 must NOT copy clustering metadata onto view")
		assert.Equal(t, int32(1), dryRunCount.Load(), "Stage 3 must be skipped after Stage 2 hit")
		assert.Equal(t, int32(1), baseGetCount.Load())

		require.Eventually(t, func() bool {
			return len(updates) >= 2
		}, 3*time.Second, 10*time.Millisecond)

		require.NoError(t, store.Close())

		// Reload from SQLite and confirm metadata persists.
		store2 := newViewProbeTestStore(t, srv.URL, cacheDir)
		t.Cleanup(func() { _ = store2.Close() })

		reloaded, ok := store2.GetTable(viewRef)
		require.True(t, ok)
		require.NotNil(t, reloaded.EffectivePartitioning())
		assert.Equal(t, "DAY", reloaded.EffectivePartitioning().Type)
		assert.Equal(t, "event_date", reloaded.EffectivePartitioning().Field)
		assert.False(t, reloaded.EffectiveRequirePartitionFilter())
		assert.Nil(t, reloaded.Clustering)
		assert.True(t, reloaded.PartitionProbed)
	})

	t.Run("range partitioned base table copies range partitioning", func(t *testing.T) {
		t.Parallel()

		viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_buckets"}
		baseRef := catalog.TableRef{Project: "proj", Dataset: "raw", Table: "buckets_base"}
		var dryRunCount atomic.Int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				dryRunCount.Add(1)
				writeDryRunOK(w, 250*giB, baseRef)
				return
			}

			if strings.HasSuffix(r.URL.Path, "/tables/buckets_base") {
				writeTableMetaJSON(w, &catalog.TableMeta{
					Ref:  baseRef,
					Type: catalog.TableTypeTable,
					RangePartitioning: &catalog.RangePartitioning{
						Field: "bucket_id",
						Range: &catalog.IntRange{Start: 0, End: 1000, Interval: 10},
					},
					Schema: []catalog.ColumnMeta{{Name: "bucket_id", Type: "INT64"}},
				})
				return
			}

			writeTableMetaJSON(w, &catalog.TableMeta{
				Ref:    viewRef,
				Type:   catalog.TableTypeView,
				Schema: []catalog.ColumnMeta{{Name: "bucket_id", Type: "INT64"}},
			})
		}))
		t.Cleanup(srv.Close)

		store := newViewProbeTestStore(t, srv.URL, t.TempDir())
		t.Cleanup(func() { _ = store.Close() })

		_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
		require.True(t, ok)

		probed := waitUntilProbed(t, store, viewRef)
		require.NotNil(t, probed.EffectivePartitioning())
		assert.Equal(t, catalog.PartitionKindRange, probed.EffectivePartitioning().Kind)
		assert.Equal(t, "bucket_id", probed.EffectivePartitioning().Field)
		require.NotNil(t, probed.EffectivePartitioning().Range)
		assert.Equal(t, catalog.FlexInt64(10), probed.EffectivePartitioning().Range.Interval)
		assert.False(t, probed.EffectiveRequirePartitionFilter())
		assert.Equal(t, int32(1), dryRunCount.Load())
	})
}

func TestViewProbeStage2UnpartitionedBaseTableEarlyStop(t *testing.T) {
	t.Parallel()

	viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_unpart"}
	baseRef := catalog.TableRef{Project: "proj", Dataset: "raw", Table: "unpart_base"}
	var dryRunCount, baseGetCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			dryRunCount.Add(1)
			writeDryRunOK(w, 500*giB, baseRef)
			return
		}

		if strings.HasSuffix(r.URL.Path, "/tables/unpart_base") {
			baseGetCount.Add(1)
			writeTableMetaJSON(w, &catalog.TableMeta{
				Ref:  baseRef,
				Type: catalog.TableTypeTable,
				Schema: []catalog.ColumnMeta{
					{Name: "event_date", Type: "DATE"},
					{Name: "payload", Type: "STRING"},
				},
			})
			return
		}

		writeTableMetaJSON(w, &catalog.TableMeta{
			Ref:  viewRef,
			Type: catalog.TableTypeView,
			Schema: []catalog.ColumnMeta{
				{Name: "event_date", Type: "DATE"},
				{Name: "payload", Type: "STRING"},
			},
		})
	}))
	t.Cleanup(srv.Close)

	cacheDir := t.TempDir()
	store := newViewProbeTestStore(t, srv.URL, cacheDir)

	_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
	require.True(t, ok)

	probed := waitUntilProbed(t, store, viewRef)
	assert.True(t, probed.PartitionProbed)
	assert.Nil(t, probed.EffectivePartitioning())
	assert.Equal(t, int32(1), dryRunCount.Load(), "Stage 3 must be skipped when single base table is confirmed unpartitioned even at 500 GiB")
	assert.Equal(t, int32(1), baseGetCount.Load())

	require.NoError(t, store.Close())

	// Reload from disk cache: still marked probed, 0 additional dry-runs.
	store2 := newViewProbeTestStore(t, srv.URL, cacheDir)
	t.Cleanup(func() { _ = store2.Close() })

	reloaded, ok := store2.GetTable(viewRef)
	require.True(t, ok)
	assert.True(t, reloaded.PartitionProbed)
	assert.Nil(t, reloaded.EffectivePartitioning())
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), dryRunCount.Load())
}

func TestViewProbeStage3AuthorizedViewBinarySearch7TemporalColumns(t *testing.T) {
	t.Parallel()

	viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_authorized"}
	baseRef := catalog.TableRef{Project: "proj", Dataset: "restricted", Table: "secret_events"}
	const baseline = 200 * giB
	const pruned = 10 * giB

	var (
		mu      sync.Mutex
		dryRuns []string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			sqlText := decodeDryRunSQL(r)
			mu.Lock()
			dryRuns = append(dryRuns, sqlText)
			mu.Unlock()

			if !strings.Contains(sqlText, " WHERE ") {
				writeDryRunOK(w, baseline, baseRef)
				return
			}

			if strings.Contains(sqlText, "`c5` = TIMESTAMP '2028-06-15 00:00:00 UTC'") {
				writeDryRunOK(w, pruned, baseRef)
				return
			}

			writeDryRunOK(w, baseline, baseRef)
			return
		}

		if strings.HasSuffix(r.URL.Path, "/tables/secret_events") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error": {"code": 403, "message": "Access Denied"}}`))
			return
		}

		writeTableMetaJSON(w, &catalog.TableMeta{
			Ref:  viewRef,
			Type: catalog.TableTypeView,
			Schema: []catalog.ColumnMeta{
				{Name: "id", Type: "INT64"},
				{Name: "c0", Type: "DATE"},
				{Name: "name", Type: "STRING"},
				{Name: "c1", Type: "DATETIME"},
				{Name: "dates_arr", Type: "DATE", Mode: catalog.ModeRepeated},
				{Name: "c2", Type: "TIMESTAMP"},
				{Name: "nested", Type: "STRUCT", Fields: []catalog.ColumnMeta{{Name: "d", Type: "DATE"}}},
				{Name: "c3", Type: "DATE"},
				{Name: "c4", Type: "DATETIME"},
				{Name: "c5", Type: "TIMESTAMP"},
				{Name: "c6", Type: "DATE"},
			},
		})
	}))
	t.Cleanup(srv.Close)

	fixedNow := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	cacheDir := t.TempDir()
	store := newViewProbeTestStore(
		t,
		srv.URL,
		cacheDir,
		catalog.WithClock(func() time.Time { return fixedNow }),
	)

	_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
	require.True(t, ok)

	probed := waitUntilProbed(t, store, viewRef)
	require.NotNil(t, probed.EffectivePartitioning())
	assert.Equal(t, catalog.PartitionKindTime, probed.EffectivePartitioning().Kind)
	assert.Equal(t, "c5", probed.EffectivePartitioning().Field)
	assert.False(t, probed.EffectiveRequirePartitionFilter())
	assert.True(t, probed.IsPartitionColumn("c5"))
	assert.True(t, probed.PartitionProbed)

	// Stage 1 (1) + Stage 3 initial group probe (1) + ceil(log2(7))=3 halving probes = 5 total.
	mu.Lock()
	require.Len(t, dryRuns, 5)
	assert.Equal(t, "SELECT * FROM `proj`.`ds`.`v_authorized`", dryRuns[0]) //nolint:unqueryvet // verifying exact Stage 1 probe SQL

	expectedInit := "SELECT * FROM `proj`.`ds`.`v_authorized` WHERE " + //nolint:unqueryvet // verifying exact Stage 3 group probe SQL
		"`c0` = DATE '2028-06-15' AND " +
		"`c1` = DATETIME '2028-06-15 00:00:00' AND " +
		"`c2` = TIMESTAMP '2028-06-15 00:00:00 UTC' AND " +
		"`c3` = DATE '2028-06-15' AND " +
		"`c4` = DATETIME '2028-06-15 00:00:00' AND " +
		"`c5` = TIMESTAMP '2028-06-15 00:00:00 UTC' AND " +
		"`c6` = DATE '2028-06-15'"
	assert.Equal(t, expectedInit, dryRuns[1])

	for _, q := range dryRuns[1:] {
		assert.NotContains(t, q, "`id`")
		assert.NotContains(t, q, "`name`")
		assert.NotContains(t, q, "`dates_arr`")
		assert.NotContains(t, q, "`nested`")
	}
	mu.Unlock()

	require.NoError(t, store.Close())

	// Reload from SQLite: persisted with c5 and PartitionProbed = true, no extra dry-runs.
	store2 := newViewProbeTestStore(t, srv.URL, cacheDir)
	t.Cleanup(func() { _ = store2.Close() })

	reloaded, ok := store2.GetTable(viewRef)
	require.True(t, ok)
	assert.True(t, reloaded.PartitionProbed)
	assert.True(t, reloaded.IsPartitionColumn("c5"))
	assert.False(t, reloaded.EffectiveRequirePartitionFilter())
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	assert.Len(t, dryRuns, 5)
	mu.Unlock()
}

func TestViewProbeStage2FallthroughAliasedIngestionAndMultiTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		refTables []catalog.TableRef
		baseMeta  *catalog.TableMeta
	}{
		{
			name:      "aliased base partition column falls through to stage 3",
			refTables: []catalog.TableRef{{Project: "proj", Dataset: "raw", Table: "aliased_base"}},
			baseMeta: &catalog.TableMeta{
				Ref:              catalog.TableRef{Project: "proj", Dataset: "raw", Table: "aliased_base"},
				Type:             catalog.TableTypeTable,
				TimePartitioning: &catalog.TimePartitioning{Type: "DAY", Field: "raw_ts"},
			},
		},
		{
			name:      "ingestion-time base table falls through to stage 3",
			refTables: []catalog.TableRef{{Project: "proj", Dataset: "raw", Table: "ingest_base"}},
			baseMeta: &catalog.TableMeta{
				Ref:              catalog.TableRef{Project: "proj", Dataset: "raw", Table: "ingest_base"},
				Type:             catalog.TableTypeTable,
				TimePartitioning: &catalog.TimePartitioning{Type: "DAY", Field: ""},
			},
		},
		{
			name: "multi-table view falls through to stage 3",
			refTables: []catalog.TableRef{
				{Project: "proj", Dataset: "raw", Table: "t1"},
				{Project: "proj", Dataset: "raw", Table: "t2"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_fallthrough"}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					sqlText := decodeDryRunSQL(r)
					if strings.Contains(sqlText, "`event_date` = DATE") {
						writeDryRunOK(w, 5*giB, tc.refTables...)
						return
					}

					writeDryRunOK(w, 150*giB, tc.refTables...)
					return
				}

				if tc.baseMeta != nil && strings.HasSuffix(r.URL.Path, "/tables/"+tc.baseMeta.Ref.Table) {
					writeTableMetaJSON(w, tc.baseMeta)
					return
				}

				writeTableMetaJSON(w, &catalog.TableMeta{
					Ref:  viewRef,
					Type: catalog.TableTypeView,
					Schema: []catalog.ColumnMeta{
						{Name: "event_date", Type: "DATE"},
						{Name: "updated_at", Type: "TIMESTAMP"},
					},
				})
			}))
			t.Cleanup(srv.Close)

			store := newViewProbeTestStore(t, srv.URL, t.TempDir())
			t.Cleanup(func() { _ = store.Close() })

			_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
			require.True(t, ok)

			probed := waitUntilProbed(t, store, viewRef)
			assert.True(t, probed.PartitionProbed)
			assert.True(t, probed.IsPartitionColumn("event_date"))
			assert.False(t, probed.EffectiveRequirePartitionFilter())
		})
	}
}

func TestViewProbeStage3SingleCandidateFastPath(t *testing.T) {
	t.Parallel()

	viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_single"}
	var dryRunCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			dryRunCount.Add(1)
			sqlText := decodeDryRunSQL(r)
			if strings.Contains(sqlText, " WHERE ") {
				writeDryRunOK(w, 2*giB)
				return
			}

			writeDryRunOK(w, 120*giB)
			return
		}

		writeTableMetaJSON(w, &catalog.TableMeta{
			Ref:  viewRef,
			Type: catalog.TableTypeView,
			Schema: []catalog.ColumnMeta{
				{Name: "event_date", Type: "DATE"},
				{Name: "user_id", Type: "STRING"},
			},
		})
	}))
	t.Cleanup(srv.Close)

	store := newViewProbeTestStore(t, srv.URL, t.TempDir())
	t.Cleanup(func() { _ = store.Close() })

	_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
	require.True(t, ok)

	probed := waitUntilProbed(t, store, viewRef)
	assert.True(t, probed.PartitionProbed)
	assert.True(t, probed.IsPartitionColumn("event_date"))
	assert.False(t, probed.EffectiveRequirePartitionFilter())
	assert.Equal(t, int32(2), dryRunCount.Load(), "single temporal column must resolve in 1 Stage 1 + 1 Stage 3 probe")
}

func TestViewProbeStage3BelowThresholdAndNoCandidatesSkip(t *testing.T) {
	t.Parallel()

	t.Run("below 100 GiB default threshold skips stage 3", func(t *testing.T) {
		t.Parallel()

		viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_small"}
		var dryRunCount atomic.Int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				dryRunCount.Add(1)
				writeDryRunOK(w, 50*giB)
				return
			}

			writeTableMetaJSON(w, &catalog.TableMeta{
				Ref:    viewRef,
				Type:   catalog.TableTypeView,
				Schema: []catalog.ColumnMeta{{Name: "event_date", Type: "DATE"}},
			})
		}))
		t.Cleanup(srv.Close)

		cacheDir := t.TempDir()
		store := newViewProbeTestStore(t, srv.URL, cacheDir)

		_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
		require.True(t, ok)

		probed := waitUntilProbed(t, store, viewRef)
		assert.True(t, probed.PartitionProbed)
		assert.Nil(t, probed.EffectivePartitioning())
		assert.Equal(t, int32(1), dryRunCount.Load())
		require.NoError(t, store.Close())

		// Verify SQLite persists PartitionProbed = true.
		store2 := newViewProbeTestStore(t, srv.URL, cacheDir)
		t.Cleanup(func() { _ = store2.Close() })
		reloaded, ok := store2.GetTable(viewRef)
		require.True(t, ok)
		assert.True(t, reloaded.PartitionProbed)
	})

	t.Run("custom WithViewProbeThresholdBytes allows smaller views", func(t *testing.T) {
		t.Parallel()

		viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_custom_thresh"}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				if strings.Contains(decodeDryRunSQL(r), " WHERE ") {
					writeDryRunOK(w, 1<<20)
					return
				}

				writeDryRunOK(w, 10<<20)
				return
			}

			writeTableMetaJSON(w, &catalog.TableMeta{
				Ref:    viewRef,
				Type:   catalog.TableTypeView,
				Schema: []catalog.ColumnMeta{{Name: "event_date", Type: "DATE"}},
			})
		}))
		t.Cleanup(srv.Close)

		store := newViewProbeTestStore(t, srv.URL, t.TempDir(), catalog.WithViewProbeThresholdBytes(5<<20))
		t.Cleanup(func() { _ = store.Close() })

		_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
		require.True(t, ok)

		probed := waitUntilProbed(t, store, viewRef)
		assert.True(t, probed.IsPartitionColumn("event_date"))
	})

	t.Run("zero temporal candidate columns skips stage 3", func(t *testing.T) {
		t.Parallel()

		viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_no_temporal"}
		var dryRunCount atomic.Int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				dryRunCount.Add(1)
				writeDryRunOK(w, 250*giB)
				return
			}

			writeTableMetaJSON(w, &catalog.TableMeta{
				Ref:  viewRef,
				Type: catalog.TableTypeView,
				Schema: []catalog.ColumnMeta{
					{Name: "id", Type: "INT64"},
					{Name: "tags", Type: "DATE", Mode: catalog.ModeRepeated},
				},
			})
		}))
		t.Cleanup(srv.Close)

		store := newViewProbeTestStore(t, srv.URL, t.TempDir())
		t.Cleanup(func() { _ = store.Close() })

		_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
		require.True(t, ok)

		probed := waitUntilProbed(t, store, viewRef)
		assert.True(t, probed.PartitionProbed)
		assert.Nil(t, probed.EffectivePartitioning())
		assert.Equal(t, int32(1), dryRunCount.Load())
	})
}

func TestViewProbeStage3FirstTryNoReductionCancelsImmediately(t *testing.T) {
	t.Parallel()

	viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_no_prune"}
	var dryRunCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			dryRunCount.Add(1)
			writeDryRunOK(w, 200*giB)
			return
		}

		schema := make([]catalog.ColumnMeta, 0, 8)
		for i := range 8 {
			schema = append(schema, catalog.ColumnMeta{
				Name: fmt.Sprintf("d%d", i),
				Type: "DATE",
			})
		}
		writeTableMetaJSON(w, &catalog.TableMeta{
			Ref:    viewRef,
			Type:   catalog.TableTypeView,
			Schema: schema,
		})
	}))
	t.Cleanup(srv.Close)

	store := newViewProbeTestStore(t, srv.URL, t.TempDir())
	t.Cleanup(func() { _ = store.Close() })

	_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
	require.True(t, ok)

	probed := waitUntilProbed(t, store, viewRef)
	assert.True(t, probed.PartitionProbed)
	assert.Nil(t, probed.EffectivePartitioning())
	assert.Equal(t, int32(2), dryRunCount.Load(), "must stop after Stage 1 + 1 initial group probe when probeBytes == baselineBytes")
}

func TestViewProbeStage3ConcurrentDataGrowthCancelsWithoutPoisoningCache(t *testing.T) {
	t.Parallel()

	t.Run("growth on initial group probe cancels and retries after backoff", func(t *testing.T) {
		t.Parallel()

		viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_grow_init"}
		var dryRunCount atomic.Int32
		var allowStable atomic.Bool

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				dryRunCount.Add(1)
				sqlText := decodeDryRunSQL(r)
				if !strings.Contains(sqlText, " WHERE ") {
					writeDryRunOK(w, 200*giB)
					return
				}

				if !allowStable.Load() {
					writeDryRunOK(w, 205*giB) // concurrent insert grew table
					return
				}

				if strings.Contains(sqlText, "`event_date`") {
					writeDryRunOK(w, 10*giB)
					return
				}

				writeDryRunOK(w, 200*giB)
				return
			}

			writeTableMetaJSON(w, &catalog.TableMeta{
				Ref:  viewRef,
				Type: catalog.TableTypeView,
				Schema: []catalog.ColumnMeta{
					{Name: "created_at", Type: "TIMESTAMP"},
					{Name: "event_date", Type: "DATE"},
				},
			})
		}))
		t.Cleanup(srv.Close)

		now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
		var clock atomic.Pointer[time.Time]
		clock.Store(&now)

		cacheDir := t.TempDir()
		store := newViewProbeTestStore(
			t,
			srv.URL,
			cacheDir,
			catalog.WithClock(func() time.Time { return *clock.Load() }),
		)
		t.Cleanup(func() { _ = store.Close() })

		_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
		require.True(t, ok)

		require.Eventually(t, func() bool {
			return dryRunCount.Load() == 2
		}, 3*time.Second, 10*time.Millisecond)

		time.Sleep(50 * time.Millisecond)
		meta, ok := store.GetTable(viewRef)
		require.True(t, ok)
		assert.False(t, meta.PartitionProbed, "concurrent growth must not poison PartitionProbed")
		assert.Nil(t, meta.EffectivePartitioning())
		assert.Equal(t, int32(2), dryRunCount.Load(), "must cancel immediately without further binary search steps")

		// After backoff expires and table stabilizes, next lookup retries and succeeds.
		allowStable.Store(true)
		later := now.Add(35 * time.Second)
		clock.Store(&later)
		_, ok = store.GetTable(viewRef)
		require.True(t, ok)

		probed := waitUntilProbed(t, store, viewRef)
		assert.True(t, probed.PartitionProbed)
		assert.True(t, probed.IsPartitionColumn("event_date"))
	})

	t.Run("growth mid binary search cancels without setting PartitionProbed", func(t *testing.T) {
		t.Parallel()

		viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_grow_mid"}
		var dryRunCount atomic.Int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				n := dryRunCount.Add(1)
				switch n {
				case 1:
					writeDryRunOK(w, 200*giB) // Stage 1 baseline
				case 2:
					writeDryRunOK(w, 10*giB) // Stage 3 initial group probe reduces
				default:
					writeDryRunOK(w, 210*giB) // First binary search halving sees concurrent growth
				}
				return
			}

			writeTableMetaJSON(w, &catalog.TableMeta{
				Ref:  viewRef,
				Type: catalog.TableTypeView,
				Schema: []catalog.ColumnMeta{
					{Name: "d0", Type: "DATE"},
					{Name: "d1", Type: "DATE"},
					{Name: "d2", Type: "DATE"},
					{Name: "d3", Type: "DATE"},
				},
			})
		}))
		t.Cleanup(srv.Close)

		store := newViewProbeTestStore(t, srv.URL, t.TempDir())
		t.Cleanup(func() { _ = store.Close() })

		_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
		require.True(t, ok)

		require.Eventually(t, func() bool {
			return dryRunCount.Load() == 3
		}, 3*time.Second, 10*time.Millisecond)

		time.Sleep(50 * time.Millisecond)
		meta, ok := store.GetTable(viewRef)
		require.True(t, ok)
		assert.False(t, meta.PartitionProbed)
		assert.Nil(t, meta.EffectivePartitioning())
		assert.Equal(t, int32(3), dryRunCount.Load())
	})
}

func TestViewProbeColumnCap32StaticIsolationAndDiskLegacyTrigger(t *testing.T) {
	t.Parallel()

	t.Run("caps stage 3 candidates at 32 columns", func(t *testing.T) {
		t.Parallel()

		viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_wide"}
		var (
			mu      sync.Mutex
			initSQL string
		)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				sqlText := decodeDryRunSQL(r)
				if strings.Contains(sqlText, " WHERE ") {
					mu.Lock()
					initSQL = sqlText
					mu.Unlock()
				}
				writeDryRunOK(w, 200*giB)
				return
			}

			schema := make([]catalog.ColumnMeta, 0, 40)
			for i := range 40 {
				schema = append(schema, catalog.ColumnMeta{
					Name: fmt.Sprintf("dt_%02d", i),
					Type: "DATE",
				})
			}
			writeTableMetaJSON(w, &catalog.TableMeta{
				Ref:    viewRef,
				Type:   catalog.TableTypeView,
				Schema: schema,
			})
		}))
		t.Cleanup(srv.Close)

		store := newViewProbeTestStore(t, srv.URL, t.TempDir())
		t.Cleanup(func() { _ = store.Close() })

		_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
		require.True(t, ok)

		waitUntilProbed(t, store, viewRef)

		mu.Lock()
		defer mu.Unlock()
		require.NotEmpty(t, initSQL)
		_, wherePart, hasWhere := strings.Cut(initSQL, " WHERE ")
		require.True(t, hasWhere)
		assert.Len(t, strings.Split(wherePart, " AND "), 32)
		assert.Contains(t, wherePart, "`dt_31`")
		assert.NotContains(t, wherePart, "`dt_32`")
	})

	t.Run("escapes backticks and backslashes in view and column identifiers", func(t *testing.T) {
		t.Parallel()

		viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v`weird"}
		var (
			mu      sync.Mutex
			dryRuns []string
		)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				sqlText := decodeDryRunSQL(r)
				mu.Lock()
				dryRuns = append(dryRuns, sqlText)
				mu.Unlock()
				if strings.Contains(sqlText, " WHERE ") {
					writeDryRunOK(w, 5*giB)
					return
				}

				writeDryRunOK(w, 200*giB)
				return
			}

			writeTableMetaJSON(w, &catalog.TableMeta{
				Ref:    viewRef,
				Type:   catalog.TableTypeView,
				Schema: []catalog.ColumnMeta{{Name: `ev` + "`" + `dt\1`, Type: "DATE"}},
			})
		}))
		t.Cleanup(srv.Close)

		fixedNow := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
		store := newViewProbeTestStore(
			t,
			srv.URL,
			t.TempDir(),
			catalog.WithClock(func() time.Time { return fixedNow }),
		)
		t.Cleanup(func() { _ = store.Close() })

		_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
		require.True(t, ok)

		probed := waitUntilProbed(t, store, viewRef)
		assert.True(t, probed.IsPartitionColumn(`ev`+"`"+`dt\1`))

		mu.Lock()
		defer mu.Unlock()
		require.Len(t, dryRuns, 2)
		assert.Equal(t, "SELECT * FROM `proj`.`ds`.`v\\`weird`", dryRuns[0]) //nolint:unqueryvet // verifying backtick escaping in Stage 1 SQL
		assert.Equal(
			t,
			"SELECT * FROM `proj`.`ds`.`v\\`weird` WHERE `ev\\`dt\\\\1` = DATE '2028-06-15'", //nolint:unqueryvet // verifying backtick escaping in Stage 3 SQL
			dryRuns[1],
		)
	})

	t.Run("static PutTable view never fires background probe", func(t *testing.T) {
		t.Parallel()

		var dryRunCount atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			dryRunCount.Add(1)
			writeDryRunOK(w, 200*giB)
		}))
		t.Cleanup(srv.Close)

		store := newViewProbeTestStore(t, srv.URL, t.TempDir())
		t.Cleanup(func() { _ = store.Close() })

		viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_static"}
		store.PutTable(&catalog.TableMeta{
			Ref:    viewRef,
			Type:   catalog.TableTypeView,
			Schema: []catalog.ColumnMeta{{Name: "event_date", Type: "DATE"}},
		})

		got, ok := store.GetTable(viewRef)
		require.True(t, ok)
		assert.False(t, got.PartitionProbed)

		time.Sleep(100 * time.Millisecond)
		assert.Equal(t, int32(0), dryRunCount.Load())
	})

	t.Run("disk-loaded unprobed view triggers probe on API-enabled store", func(t *testing.T) {
		t.Parallel()

		viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_legacy_disk"}
		var dryRunCount atomic.Int32

		// Phase 1: Seed SQLite cache.db via a store where Stage 1 fails transiently
		// (so the view is cached on disk with PartitionProbed == false).
		var phase atomic.Int32
		phase.Store(1)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				dryRunCount.Add(1)
				if phase.Load() == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}

				if strings.Contains(decodeDryRunSQL(r), "`event_date` = DATE") {
					writeDryRunOK(w, 5*giB)
					return
				}

				writeDryRunOK(w, 200*giB)
				return
			}

			writeTableMetaJSON(w, &catalog.TableMeta{
				Ref:    viewRef,
				Type:   catalog.TableTypeView,
				Schema: []catalog.ColumnMeta{{Name: "event_date", Type: "DATE"}},
			})
		}))
		t.Cleanup(srv.Close)

		cacheDir := t.TempDir()
		store1 := newViewProbeTestStore(t, srv.URL, cacheDir)
		_, ok := store1.WaitForTable(t.Context(), viewRef, 3*time.Second)
		require.True(t, ok)
		require.Eventually(t, func() bool {
			return dryRunCount.Load() >= 1
		}, 3*time.Second, 10*time.Millisecond)
		require.NoError(t, store1.Close())

		// Phase 2: Reopen Store on the same cacheDir; loading from SQLite triggers Stage 1->3.
		phase.Store(2)
		store2 := newViewProbeTestStore(t, srv.URL, cacheDir)
		t.Cleanup(func() { _ = store2.Close() })

		loaded, ok := store2.GetTable(viewRef)
		require.True(t, ok)
		assert.False(t, loaded.PartitionProbed)

		probed := waitUntilProbed(t, store2, viewRef)
		assert.True(t, probed.PartitionProbed)
		assert.True(t, probed.IsPartitionColumn("event_date"))
		assert.False(t, probed.EffectiveRequirePartitionFilter())
	})
}

func TestViewProbePassiveVsActiveInteraction(t *testing.T) {
	t.Parallel()

	t.Run("cold-cache passive hint sets PartitionProbed and skips redundant Stage 1 probe", func(t *testing.T) {
		t.Parallel()

		viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_passive_first"}
		allowFetch := make(chan struct{})
		dryRunDone := make(chan struct{}, 4)
		fetchUpdated := make(chan catalog.TableRef, 4)

		var (
			mu      sync.Mutex
			dryRuns []string
		)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				sqlText := decodeDryRunSQL(r)
				mu.Lock()
				dryRuns = append(dryRuns, sqlText)
				mu.Unlock()

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "Cannot query over table 'proj.raw.events_base' without a filter over column(s) 'event_date' that can be used for partition elimination"}}`))
				return
			}

			<-allowFetch
			writeTableMetaJSON(w, &catalog.TableMeta{
				Ref:  viewRef,
				Type: catalog.TableTypeView,
				Schema: []catalog.ColumnMeta{
					{Name: "event_date", Type: "DATE"},
					{Name: "user_id", Type: "STRING"},
				},
			})
		}))
		t.Cleanup(srv.Close)

		store := newViewProbeTestStore(
			t,
			srv.URL,
			t.TempDir(),
			catalog.WithOnUpdate(func(ref catalog.TableRef) { fetchUpdated <- ref }),
			catalog.WithOnDryRunUpdate(func() { dryRunDone <- struct{}{} }),
		)
		t.Cleanup(func() { _ = store.Close() })

		// Start async fetch (blocked on allowFetch) and user dry-run (finishes immediately).
		_, ok := store.GetTable(viewRef)
		assert.False(t, ok)

		userSQL := "SELECT event_date FROM proj.ds.v_passive_first"
		require.True(t, store.RequestDryRun("proj", "ds", userSQL, false, viewRef))

		select {
		case <-dryRunDone:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for passive dry-run completion")
		}

		// Unblock table fetch; pending hint resolves the view during processFetch.
		close(allowFetch)

		select {
		case gotRef := <-fetchUpdated:
			assert.Equal(t, viewRef.NormalizedKey(), gotRef.NormalizedKey())
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for view fetch update")
		}

		meta, ok := store.GetTable(viewRef)
		require.True(t, ok)
		assert.True(t, meta.PartitionProbed)
		assert.True(t, meta.EffectiveRequirePartitionFilter())
		assert.True(t, meta.IsPartitionColumn("event_date"))

		// Confirm no redundant active Stage 1 SELECT * probe fires.
		time.Sleep(100 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		require.Len(t, dryRuns, 1, "passive cold-cache hint must suppress redundant active Stage 1 probe")
		assert.Equal(t, userSQL, dryRuns[0])
	})

	t.Run("Stage 3 optional partition upgrades to required on subsequent passive dry-run error", func(t *testing.T) {
		t.Parallel()

		viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_upgrade"}
		dryRunDone := make(chan struct{}, 4)
		var requireFilter atomic.Bool

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				if requireFilter.Load() {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "Cannot query over table 'proj.raw.events_base' without a filter over column(s) 'event_date' that can be used for partition elimination"}}`))
					return
				}

				if strings.Contains(decodeDryRunSQL(r), "`event_date` = DATE") {
					writeDryRunOK(w, 5*giB)
					return
				}

				writeDryRunOK(w, 200*giB)
				return
			}

			writeTableMetaJSON(w, &catalog.TableMeta{
				Ref:  viewRef,
				Type: catalog.TableTypeView,
				Schema: []catalog.ColumnMeta{
					{Name: "event_date", Type: "DATE"},
					{Name: "user_id", Type: "STRING"},
				},
			})
		}))
		t.Cleanup(srv.Close)

		store := newViewProbeTestStore(
			t,
			srv.URL,
			t.TempDir(),
			catalog.WithOnDryRunUpdate(func() { dryRunDone <- struct{}{} }),
		)
		t.Cleanup(func() { _ = store.Close() })

		_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
		require.True(t, ok)

		// Stage 3 initially discovers event_date as optional (RequirePartitionFilter = false).
		probed := waitUntilProbed(t, store, viewRef)
		assert.True(t, probed.PartitionProbed)
		assert.True(t, probed.IsPartitionColumn("event_date"))
		assert.False(t, probed.EffectiveRequirePartitionFilter())

		// Later, a passive user dry-run fails with require_partition_filter on event_date.
		requireFilter.Store(true)
		require.True(t, store.RequestDryRun("proj", "ds", "SELECT user_id FROM proj.ds.v_upgrade", false, viewRef))

		select {
		case <-dryRunDone:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for passive dry-run error")
		}

		upgraded, ok := store.GetTable(viewRef)
		require.True(t, ok)
		assert.True(t, upgraded.PartitionProbed)
		assert.True(t, upgraded.IsPartitionColumn("event_date"))
		assert.True(t, upgraded.EffectiveRequirePartitionFilter())
	})
}

func TestViewProbeStage1UnexposedRequiredColumn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		errMsg string
	}{
		{
			name:   "aliased column not in view schema",
			errMsg: "Cannot query over table 'proj.raw.events_base' without a filter over column(s) 'event_date' that can be used for partition elimination",
		},
		{
			name:   "ingestion-time pseudo columns not exposed by view",
			errMsg: "Cannot query over table 'proj.raw.events_base' without a filter over column(s) '_PARTITIONDATE', '_PARTITIONTIME' that can be used for partition elimination",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_unexposed"}
			var (
				dryRunCount atomic.Int32
				updateCount atomic.Int32
			)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					dryRunCount.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error": map[string]any{
							"code":    400,
							"message": tc.errMsg,
						},
					})
					return
				}

				writeTableMetaJSON(w, &catalog.TableMeta{
					Ref:    viewRef,
					Type:   catalog.TableTypeView,
					Schema: []catalog.ColumnMeta{{Name: "dt", Type: "DATE"}},
				})
			}))
			t.Cleanup(srv.Close)

			store := newViewProbeTestStore(
				t,
				srv.URL,
				t.TempDir(),
				catalog.WithOnUpdate(func(_ catalog.TableRef) {
					updateCount.Add(1)
				}),
			)
			t.Cleanup(func() { _ = store.Close() })

			_, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
			require.True(t, ok)

			probed := waitUntilProbed(t, store, viewRef)
			assert.True(t, probed.PartitionProbed)
			assert.Nil(t, probed.EffectivePartitioning())
			assert.False(t, probed.EffectiveRequirePartitionFilter())
			assert.Equal(t, int32(1), dryRunCount.Load())

			// Only the initial table fetch triggers onUpdate; unexposed Stage 1 match does not fire an extra callback.
			time.Sleep(100 * time.Millisecond)
			assert.Equal(t, int32(1), updateCount.Load())
		})
	}
}
