package catalog_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/bqconfig"
	"github.com/paulourio/bqlang/catalog"
)

func TestParseRequiredPartitionFilterError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		msg       string
		wantOK    bool
		wantTable catalog.TableRef
		wantCols  []string
	}{
		{
			name:      "standard dot table single column",
			msg:       "Cannot query over table 'my-proj.raw_ds.events' without a filter over column(s) 'event_date' that can be used for partition elimination",
			wantOK:    true,
			wantTable: catalog.TableRef{Project: "my-proj", Dataset: "raw_ds", Table: "events"},
			wantCols:  []string{"event_date"},
		},
		{
			name:      "legacy colon project separator with location suffix",
			msg:       "Cannot query over table 'my-proj:raw_ds.events' without a filter over column(s) 'created_at' that can be used for partition elimination at [1:15]",
			wantOK:    true,
			wantTable: catalog.TableRef{Project: "my-proj", Dataset: "raw_ds", Table: "events"},
			wantCols:  []string{"created_at"},
		},
		{
			name:      "ingestion-time multi-column pseudo fields",
			msg:       "Cannot query over table 'p1.d1.t1' without a filter over column(s) '_PARTITION_LOAD_TIME', '_PARTITIONDATE', '_PARTITIONTIME' that can be used for partition elimination",
			wantOK:    true,
			wantTable: catalog.TableRef{Project: "p1", Dataset: "d1", Table: "t1"},
			wantCols:  []string{"_PARTITION_LOAD_TIME", "_PARTITIONDATE", "_PARTITIONTIME"},
		},
		{
			name:      "backtick quoted table and column",
			msg:       "bigquery dry-run rejected query: Cannot query over table `p1.d1.t1` without a filter over column `event_ts` that can be used for partition elimination",
			wantOK:    true,
			wantTable: catalog.TableRef{Project: "p1", Dataset: "d1", Table: "t1"},
			wantCols:  []string{"event_ts"},
		},
		{
			name:   "unrelated dry-run error",
			msg:    "Not found: Table proj:ds.missing",
			wantOK: false,
		},
		{
			name:   "empty message",
			msg:    "",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := catalog.ParseRequiredPartitionFilterError(tc.msg)
			assert.Equal(t, tc.wantOK, ok)
			if !tc.wantOK {
				return
			}

			assert.Equal(t, tc.wantTable, got.Table)
			assert.Equal(t, tc.wantCols, got.Columns)
		})
	}
}

func TestStoreDryRunInfersViewPartitionColumnAndPersists(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "Cannot query over table 'proj.raw.events_base' without a filter over column(s) 'event_date' that can be used for partition elimination"}}`))
			return
		}

		// GET view metadata (no partitioning exposed by BigQuery on VIEWs).
		_, _ = w.Write([]byte(`{
			"tableReference": {"projectId": "proj", "datasetId": "analytics", "tableId": "v_events"},
			"type": "VIEW",
			"schema": {
				"fields": [
					{"name": "event_date", "type": "DATE", "mode": "NULLABLE"},
					{"name": "user_id", "type": "STRING", "mode": "NULLABLE"}
				]
			}
		}`))
	}))
	t.Cleanup(srv.Close)

	cacheDir := t.TempDir()
	opts := bqconfig.DefaultLSPOptions()
	opts.DefaultProject = "proj"
	opts.DefaultDataset = "analytics"
	opts.BigQueryAPIEnabled = true
	opts.CacheDir = cacheDir

	rest := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithTokenProvider(func(_ context.Context) (string, error) { return "tok", nil }),
	)

	updateCh := make(chan catalog.TableRef, 8)
	dryRunCh := make(chan struct{}, 8)

	store, err := catalog.NewStore(
		opts,
		catalog.WithRestClient(rest),
		catalog.WithOnUpdate(func(ref catalog.TableRef) {
			updateCh <- ref
		}),
		catalog.WithOnDryRunUpdate(func() {
			dryRunCh <- struct{}{}
		}),
	)
	require.NoError(t, err)

	viewRef := catalog.TableRef{Project: "proj", Dataset: "analytics", Table: "v_events"}
	meta, ok := store.WaitForTable(t.Context(), viewRef, 3*time.Second)
	require.True(t, ok)
	assert.Equal(t, catalog.TableTypeView, meta.Type)
	assert.Nil(t, meta.EffectivePartitioning())
	assert.False(t, meta.EffectiveRequirePartitionFilter())

	// Drain initial fetch update.
	select {
	case <-updateCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for initial view fetch update")
	}

	require.True(t, store.RequestDryRun("proj", "analytics", "SELECT event_date, user_id FROM proj.analytics.v_events", false, viewRef))

	select {
	case gotRef := <-updateCh:
		assert.Equal(t, viewRef.NormalizedKey(), gotRef.NormalizedKey())
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for partition inference catalog update")
	}

	select {
	case <-dryRunCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for dry-run completion callback")
	}

	enriched, ok := store.GetTable(viewRef)
	require.True(t, ok)
	require.NotNil(t, enriched.EffectivePartitioning())
	assert.Equal(t, catalog.PartitionKindTime, enriched.EffectivePartitioning().Kind)
	assert.Equal(t, "event_date", enriched.EffectivePartitioning().Field)
	assert.True(t, enriched.EffectiveRequirePartitionFilter())
	assert.True(t, enriched.IsPartitionColumn("event_date"))
	assert.True(t, enriched.IsPartitionColumn("EVENT_DATE"))
	assert.False(t, enriched.IsPartitionColumn("user_id"))

	require.NoError(t, store.Close())

	// Verify the inferred partition metadata persisted to the SQLite disk cache.
	offlineOpts := bqconfig.DefaultLSPOptions()
	offlineOpts.DefaultProject = "proj"
	offlineOpts.DefaultDataset = "analytics"
	offlineOpts.BigQueryAPIEnabled = false
	offlineOpts.CacheDir = cacheDir

	store2, err := catalog.NewStore(offlineOpts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store2.Close() })

	reloaded, ok := store2.GetTable(viewRef)
	require.True(t, ok)
	assert.True(t, reloaded.EffectiveRequirePartitionFilter())
	assert.True(t, reloaded.IsPartitionColumn("event_date"))
}

func TestStoreDryRunInfersRangePartitionForInt64ViewColumn(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "Cannot query over table 'proj.raw.buckets_base' without a filter over column(s) 'bucket_id' that can be used for partition elimination"}}`))
	}))
	t.Cleanup(srv.Close)

	dryRunCh := make(chan struct{}, 4)
	opts := bqconfig.DefaultLSPOptions()
	opts.DefaultProject = "proj"
	opts.BigQueryAPIEnabled = true
	opts.CacheDir = t.TempDir()

	rest := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithTokenProvider(func(_ context.Context) (string, error) { return "tok", nil }),
	)

	store, err := catalog.NewStore(
		opts,
		catalog.WithRestClient(rest),
		catalog.WithOnDryRunUpdate(func() { dryRunCh <- struct{}{} }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_buckets"}
	store.PutTable(&catalog.TableMeta{
		Ref:  viewRef,
		Type: catalog.TableTypeView,
		Schema: []catalog.ColumnMeta{
			{Name: "bucket_id", Type: "INT64"},
			{Name: "val", Type: "STRING"},
		},
	})

	require.True(t, store.RequestDryRun("proj", "ds", "SELECT bucket_id, val FROM proj.ds.v_buckets", false, viewRef))
	select {
	case <-dryRunCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for dry-run")
	}

	got, ok := store.GetTable(viewRef)
	require.True(t, ok)
	require.NotNil(t, got.EffectivePartitioning())
	assert.Equal(t, catalog.PartitionKindRange, got.EffectivePartitioning().Kind)
	assert.Equal(t, "bucket_id", got.EffectivePartitioning().Field)
	assert.True(t, got.EffectiveRequirePartitionFilter())
	assert.True(t, got.IsPartitionColumn("bucket_id"))
}

func TestStoreDryRunViewDisambiguation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "Cannot query over table 'proj.raw.events_base' without a filter over column(s) 'event_date' that can be used for partition elimination"}}`))
	}))
	t.Cleanup(srv.Close)

	dryRunCh := make(chan struct{}, 8)
	opts := bqconfig.DefaultLSPOptions()
	opts.DefaultProject = "proj"
	opts.BigQueryAPIEnabled = true
	opts.CacheDir = t.TempDir()

	rest := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithTokenProvider(func(_ context.Context) (string, error) { return "tok", nil }),
	)

	store, err := catalog.NewStore(
		opts,
		catalog.WithRestClient(rest),
		catalog.WithOnDryRunUpdate(func() { dryRunCh <- struct{}{} }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// 1. View joined with regular table that also has event_date -> only view is flagged.
	vEvents := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_events"}
	dimUsers := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "dim_users"}
	store.PutTable(&catalog.TableMeta{
		Ref:  vEvents,
		Type: catalog.TableTypeView,
		Schema: []catalog.ColumnMeta{
			{Name: "event_date", Type: "DATE"},
			{Name: "user_id", Type: "STRING"},
		},
	})
	store.PutTable(&catalog.TableMeta{
		Ref:  dimUsers,
		Type: catalog.TableTypeTable,
		Schema: []catalog.ColumnMeta{
			{Name: "event_date", Type: "DATE"},
			{Name: "user_id", Type: "STRING"},
		},
	})

	require.True(t, store.RequestDryRun("proj", "ds", "SELECT user_id FROM proj.ds.v_events JOIN proj.ds.dim_users USING (user_id)", false, vEvents, dimUsers))
	<-dryRunCh

	gotView, _ := store.GetTable(vEvents)
	gotDim, _ := store.GetTable(dimUsers)
	assert.True(t, gotView.IsPartitionColumn("event_date"))
	assert.True(t, gotView.EffectiveRequirePartitionFilter())
	assert.False(t, gotDim.IsPartitionColumn("event_date"))

	// 2. Two views both with event_date, one has View.Query referencing raw.events_base -> disambiguated.
	vA := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_a"}
	vB := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_b"}
	store.PutTable(&catalog.TableMeta{
		Ref:  vA,
		Type: catalog.TableTypeView,
		View: &catalog.ViewDefinition{Query: "SELECT event_date, id FROM proj.raw.events_base"},
		Schema: []catalog.ColumnMeta{
			{Name: "event_date", Type: "DATE"},
			{Name: "id", Type: "INT64"},
		},
	})
	store.PutTable(&catalog.TableMeta{
		Ref:  vB,
		Type: catalog.TableTypeView,
		View: &catalog.ViewDefinition{Query: "SELECT event_date, id FROM proj.raw.other_table"},
		Schema: []catalog.ColumnMeta{
			{Name: "event_date", Type: "DATE"},
			{Name: "id", Type: "INT64"},
		},
	})

	require.True(t, store.RequestDryRun("proj", "ds", "SELECT id FROM proj.ds.v_a JOIN proj.ds.v_b USING (id)", false, vA, vB))
	<-dryRunCh

	gotA, _ := store.GetTable(vA)
	gotB, _ := store.GetTable(vB)
	assert.True(t, gotA.IsPartitionColumn("event_date"))
	assert.False(t, gotB.IsPartitionColumn("event_date"))

	// 3. Two views both with event_date and no View.Query -> ambiguous, neither flagged.
	vX := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_x"}
	vY := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_y"}
	store.PutTable(&catalog.TableMeta{
		Ref:    vX,
		Type:   catalog.TableTypeView,
		Schema: []catalog.ColumnMeta{{Name: "event_date", Type: "DATE"}, {Name: "id", Type: "INT64"}},
	})
	store.PutTable(&catalog.TableMeta{
		Ref:    vY,
		Type:   catalog.TableTypeView,
		Schema: []catalog.ColumnMeta{{Name: "event_date", Type: "DATE"}, {Name: "id", Type: "INT64"}},
	})

	require.True(t, store.RequestDryRun("proj", "ds", "SELECT id FROM proj.ds.v_x JOIN proj.ds.v_y USING (id)", false, vX, vY))
	<-dryRunCh

	gotX, _ := store.GetTable(vX)
	gotY, _ := store.GetTable(vY)
	assert.False(t, gotX.IsPartitionColumn("event_date"))
	assert.False(t, gotY.IsPartitionColumn("event_date"))

	// 4. View that renamed event_date to dt -> does not expose event_date, not flagged.
	vRenamed := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_renamed"}
	store.PutTable(&catalog.TableMeta{
		Ref:    vRenamed,
		Type:   catalog.TableTypeView,
		Schema: []catalog.ColumnMeta{{Name: "dt", Type: "DATE"}},
	})

	require.True(t, store.RequestDryRun("proj", "ds", "SELECT dt FROM proj.ds.v_renamed", false, vRenamed))
	<-dryRunCh

	gotRenamed, _ := store.GetTable(vRenamed)
	assert.Nil(t, gotRenamed.EffectivePartitioning())
	assert.False(t, gotRenamed.EffectiveRequirePartitionFilter())
}

func TestStoreDryRunColdCacheRaceAppliesHintAfterFetch(t *testing.T) {
	t.Parallel()

	allowFetch := make(chan struct{})
	dryRunDone := make(chan struct{}, 2)
	fetchUpdated := make(chan catalog.TableRef, 4)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "Cannot query over table 'proj.raw.events_base' without a filter over column(s) 'event_date' that can be used for partition elimination"}}`))
			return
		}

		if strings.Contains(r.URL.Path, "/tables/v_race") {
			<-allowFetch
			_, _ = w.Write([]byte(`{
				"tableReference": {"projectId": "proj", "datasetId": "ds", "tableId": "v_race"},
				"type": "VIEW",
				"schema": {"fields": [{"name": "event_date", "type": "DATE"}]}
			}`))
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	opts := bqconfig.DefaultLSPOptions()
	opts.DefaultProject = "proj"
	opts.DefaultDataset = "ds"
	opts.BigQueryAPIEnabled = true
	opts.CacheDir = t.TempDir()

	rest := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithTokenProvider(func(_ context.Context) (string, error) { return "tok", nil }),
	)

	store, err := catalog.NewStore(
		opts,
		catalog.WithRestClient(rest),
		catalog.WithOnUpdate(func(ref catalog.TableRef) { fetchUpdated <- ref }),
		catalog.WithOnDryRunUpdate(func() { dryRunDone <- struct{}{} }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	viewRef := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "v_race"}

	// Trigger async fetch (blocked on allowFetch) AND dry-run (completes immediately).
	_, ok := store.GetTable(viewRef)
	assert.False(t, ok)
	require.True(t, store.RequestDryRun("proj", "ds", "SELECT event_date FROM proj.ds.v_race", false, viewRef))

	// Wait for dry-run to finish BEFORE table fetch completes.
	select {
	case <-dryRunDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for dry-run")
	}

	// Now unblock table fetch; pending partition hint must apply automatically.
	close(allowFetch)

	select {
	case gotRef := <-fetchUpdated:
		assert.Equal(t, viewRef.NormalizedKey(), gotRef.NormalizedKey())
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for table fetch update")
	}

	meta, ok := store.GetTable(viewRef)
	require.True(t, ok)
	assert.True(t, meta.EffectiveRequirePartitionFilter())
	assert.True(t, meta.IsPartitionColumn("event_date"))
}
