package catalog_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goccy/go-googlesql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/formatter"
	"github.com/paulourio/bqlang/internal/bqconfig"
	"github.com/paulourio/bqlang/internal/catalog"
)

func TestFlexInt64JSON(t *testing.T) {
	t.Parallel()

	type payload struct {
		Rows *catalog.FlexInt64 `json:"rows,omitempty"`
		Size catalog.FlexInt64  `json:"size"`
	}

	cases := []struct {
		name     string
		raw      string
		wantRows *int64
		wantSize int64
		wantErr  bool
	}{
		{
			name:     "quoted strings",
			raw:      `{"rows":"12345","size":"678"}`,
			wantRows: new(int64(12345)),
			wantSize: 678,
		},
		{
			name:     "unquoted integers",
			raw:      `{"rows":0,"size":42}`,
			wantRows: new(int64(0)),
			wantSize: 42,
		},
		{
			name:     "empty string and null",
			raw:      `{"rows":null,"size":""}`,
			wantRows: nil,
			wantSize: 0,
		},
		{
			name:     "missing optional pointer",
			raw:      `{"size":"-10"}`,
			wantRows: nil,
			wantSize: -10,
		},
		{
			name:    "invalid boolean",
			raw:     `{"size":true}`,
			wantErr: true,
		},
		{
			name:    "invalid string",
			raw:     `{"size":"not-a-number"}`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var p payload
			err := json.Unmarshal([]byte(tc.raw), &p)
			if tc.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.wantSize, p.Size.Int64())

			if tc.wantRows == nil {
				assert.Nil(t, p.Rows)
			} else {
				require.NotNil(t, p.Rows)
				assert.Equal(t, *tc.wantRows, p.Rows.Int64())
			}

			out, err := json.Marshal(p)
			require.NoError(t, err)

			var roundTrip payload
			require.NoError(t, json.Unmarshal(out, &roundTrip))
			assert.Equal(t, p.Size, roundTrip.Size)
		})
	}
}

func TestDecodeTableJSONVariants(t *testing.T) {
	t.Parallel()

	t.Run("rest v2 table json with legacy normalization", func(t *testing.T) {
		t.Parallel()

		raw := `{
			"kind": "bigquery#table",
			"tableReference": {"projectId": "p1", "datasetId": "d1", "tableId": "events"},
			"type": "TABLE",
			"description": "User events",
			"numRows": "500",
			"numBytes": 2048,
			"timePartitioning": {
				"type": "DAY",
				"field": "event_ts",
				"requirePartitionFilter": true
			},
			"clustering": {"fields": ["user_id", "country"]},
			"schema": {
				"fields": [
					{"name": "user_id", "type": "INTEGER", "mode": "REQUIRED"},
					{"name": "score", "type": "FLOAT"},
					{"name": "active", "type": "BOOLEAN"},
					{"name": "event_ts", "type": "TIMESTAMP"},
					{"name": "window", "type": "RANGE", "rangeElementType": {"type": "DATE"}},
					{"name": "trace_id", "type": "UUID"},
					{
						"name": "items",
						"type": "RECORD",
						"mode": "REPEATED",
						"fields": [
							{"name": "sku", "type": "STRING", "maxLength": "64"},
							{
								"name": "meta",
								"type": "STRUCT",
								"fields": [
									{"name": "price", "type": "NUMERIC", "precision": "10", "scale": "2"}
								]
							}
						]
					}
				]
			}
		}`

		meta, err := catalog.DecodeTableJSON([]byte(raw), catalog.TableRef{})
		require.NoError(t, err)
		assert.Equal(t, catalog.TableTypeTable, meta.Type)
		assert.Equal(t, "p1.d1.events", meta.Ref.FullName())
		require.NotNil(t, meta.NumRows)
		assert.Equal(t, int64(500), meta.NumRows.Int64())
		require.NotNil(t, meta.NumBytes)
		assert.Equal(t, int64(2048), meta.NumBytes.Int64())

		assert.True(t, meta.EffectiveRequirePartitionFilter())
		assert.False(t, meta.IsIngestionTimePartitioned())
		assert.True(t, meta.IsPartitionColumn("EVENT_TS"))
		assert.False(t, meta.IsPartitionColumn("_PARTITIONTIME"))

		rank, ok := meta.ClusterRank("USER_ID")
		assert.True(t, ok)
		assert.Equal(t, 1, rank)
		rank2, ok := meta.ClusterRank("country")
		assert.True(t, ok)
		assert.Equal(t, 2, rank2)
		_, ok = meta.ClusterRank("missing")
		assert.False(t, ok)

		col, ok := meta.FindColumn("user_id")
		require.True(t, ok)
		assert.Equal(t, "INT64", col.Type)
		assert.Equal(t, catalog.ModeRequired, col.Mode)

		scoreCol, ok := meta.FindColumn("score")
		require.True(t, ok)
		assert.Equal(t, "FLOAT64", scoreCol.Type)
		assert.Equal(t, catalog.ModeNullable, scoreCol.Mode)

		itemsCol, ok := meta.FindColumn("items")
		require.True(t, ok)
		assert.Equal(t, "ARRAY<STRUCT<sku STRING(64), meta STRUCT<price NUMERIC(10, 2)>>>", itemsCol.SQLType())

		priceCol, ok := meta.FindField("ITEMS", "META", "PRICE")
		require.True(t, ok)
		assert.Equal(t, "NUMERIC(10, 2)", priceCol.SQLType())

		f0, ok := itemsCol.FieldAt(0)
		require.True(t, ok)
		assert.Equal(t, "sku", f0.Name)
		_, ok = itemsCol.FieldAt(99)
		assert.False(t, ok)

		rangeCol, ok := meta.FindColumn("window")
		require.True(t, ok)
		assert.Equal(t, "RANGE<DATE>", rangeCol.SQLType())
	})

	t.Run("bq show schema array", func(t *testing.T) {
		t.Parallel()

		raw := `[
			{"name": "id", "type": "INTEGER", "mode": "REQUIRED"},
			{"name": "payload", "type": "JSON"}
		]`
		fallback := catalog.TableRef{Project: "proj", Dataset: "ds", Table: "raw_tbl"}

		meta, err := catalog.DecodeTableJSON([]byte(raw), fallback)
		require.NoError(t, err)
		assert.Equal(t, fallback, meta.Ref)
		assert.Equal(t, catalog.TableTypeTable, meta.Type)
		assert.Nil(t, meta.NumRows)
		require.Len(t, meta.Schema, 2)
		assert.Equal(t, "INT64", meta.Schema[0].Type)
		assert.Equal(t, "JSON", meta.Schema[1].Type)
	})

	t.Run("external table schema fallback and ingestion partitioning", func(t *testing.T) {
		t.Parallel()

		raw := `{
			"tableReference": {"tableId": "ext_tbl"},
			"type": "EXTERNAL",
			"timePartitioning": {"type": "DAY"},
			"externalDataConfiguration": {
				"sourceFormat": "PARQUET",
				"schema": {"fields": [{"name": "k", "type": "STRING"}]}
			}
		}`

		meta, err := catalog.DecodeTableJSON([]byte(raw), catalog.TableRef{Project: "p", Dataset: "d"})
		require.NoError(t, err)
		assert.Equal(t, "p.d.ext_tbl", meta.Ref.FullName())
		assert.Equal(t, catalog.TableTypeExternal, meta.Type)
		assert.True(t, meta.IsIngestionTimePartitioned())
		assert.True(t, meta.IsPartitionColumn("_partitiontime"))
		assert.True(t, meta.IsPartitionColumn("_PARTITIONDATE"))
		require.Len(t, meta.Schema, 1)
		assert.Equal(t, "k", meta.Schema[0].Name)
	})
}

func TestRestClientFetchListAndSingleflight(t *testing.T) {
	t.Parallel()

	var tableHits atomic.Int32
	gate := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		switch r.URL.Path {
		case "/projects/p1/datasets/d1/tables/t1":
			tableHits.Add(1)
			<-gate
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"tableReference": {"projectId": "p1", "datasetId": "d1", "tableId": "t1"},
				"schema": {"fields": [{"name": "id", "type": "INT64"}]}
			}`))
		case "/projects/p1/datasets/d1/tables/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/projects/p1/datasets/d1/tables/denied":
			w.WriteHeader(http.StatusForbidden)
		case "/projects/p1/datasets/d1/tables/slow":
			time.Sleep(150 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		case "/projects/p1/datasets":
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("pageToken") == "" {
				_, _ = w.Write([]byte(`{
					"datasets": [{"datasetReference": {"projectId": "p1", "datasetId": "ds_a"}}],
					"nextPageToken": "page2"
				}`))
				return
			}
			_, _ = w.Write([]byte(`{
				"datasets": [{"datasetReference": {"projectId": "p1", "datasetId": "ds_b"}}]
			}`))
		case "/projects/p1/datasets/d1/tables":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"tables": [{"tableReference": {"projectId": "p1", "datasetId": "d1", "tableId": "t1"}, "type": "TABLE"}]
			}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	client := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithHTTPClient(srv.Client()),
		catalog.WithTimeout(50*time.Millisecond),
		catalog.WithTokenProvider(func(context.Context) (string, error) {
			return "test-token", nil
		}),
	)

	// Verify singleflight deduplication on concurrent FetchTable calls.
	const callers = 6
	var wg sync.WaitGroup
	errs := make([]error, callers)
	metas := make([]*catalog.TableMeta, callers)

	for i := range callers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			metas[idx], errs[idx] = client.FetchTable(context.Background(), catalog.TableRef{
				Project: "p1",
				Dataset: "d1",
				Table:   "t1",
			})
		}(i)
	}

	time.Sleep(10 * time.Millisecond)
	close(gate)
	wg.Wait()

	assert.Equal(t, int32(1), tableHits.Load())
	for i := range callers {
		require.NoError(t, errs[i])
		require.NotNil(t, metas[i])
		assert.Equal(t, "p1.d1.t1", metas[i].Ref.FullName())
	}

	// Sentinel error checks.
	_, err := client.FetchTable(context.Background(), catalog.TableRef{Project: "p1", Dataset: "d1", Table: "missing"})
	require.ErrorIs(t, err, catalog.ErrTableNotFound)

	_, err = client.FetchTable(context.Background(), catalog.TableRef{Project: "p1", Dataset: "d1", Table: "denied"})
	require.ErrorIs(t, err, catalog.ErrUnauthorized)

	_, err = client.FetchTable(context.Background(), catalog.TableRef{Table: "incomplete"})
	require.ErrorIs(t, err, catalog.ErrInvalidTableRef)

	_, err = client.FetchTable(context.Background(), catalog.TableRef{Project: "p1", Dataset: "d1", Table: "slow"})
	require.ErrorIs(t, err, catalog.ErrAPIError)

	// ListDatasets pagination + ListTables.
	datasets, err := client.ListDatasets(context.Background(), "p1")
	require.NoError(t, err)
	assert.Equal(t, []catalog.DatasetRef{
		{Project: "p1", Dataset: "ds_a"},
		{Project: "p1", Dataset: "ds_b"},
	}, datasets)

	tables, err := client.ListTables(context.Background(), "p1", "d1")
	require.NoError(t, err)
	require.Len(t, tables, 1)
	assert.Equal(t, "p1.d1.t1", tables[0].Ref.FullName())
}

func TestStoreSchemaDirsDiskTTLCacheAndAsyncFetch(t *testing.T) {
	t.Parallel()

	t.Run("schema_dirs flat and nested plus missing dir", func(t *testing.T) {
		t.Parallel()

		schemaDir := t.TempDir()
		// Flat <project>.<dataset>.<table>.json
		require.NoError(t, os.WriteFile(
			filepath.Join(schemaDir, "proj1.ds1.users.json"),
			[]byte(`[{"name":"user_id","type":"INT64","mode":"REQUIRED"}]`),
			0o600,
		))
		// Nested <project>/<dataset>/<table>.json
		nestedDir := filepath.Join(schemaDir, "proj1", "ds2")
		require.NoError(t, os.MkdirAll(nestedDir, 0o750))
		require.NoError(t, os.WriteFile(
			filepath.Join(nestedDir, "orders.json"),
			[]byte(`{"type":"TABLE","schema":{"fields":[{"name":"order_id","type":"STRING"}]}}`),
			0o600,
		))

		opts := bqconfig.DefaultLSPOptions()
		opts.CacheDir = ""
		opts.SchemaDirs = []string{schemaDir, filepath.Join(schemaDir, "nonexistent")}

		store, err := catalog.NewStore(opts)
		require.NoError(t, err)
		defer func() { _ = store.Close() }()

		u, ok := store.GetTable(catalog.TableRef{Project: "proj1", Dataset: "ds1", Table: "users"})
		require.True(t, ok)
		assert.Equal(t, "proj1.ds1.users", u.Ref.FullName())

		o, ok := store.GetTable(catalog.TableRef{Project: "proj1", Dataset: "ds2", Table: "orders"})
		require.True(t, ok)
		assert.Equal(t, "proj1.ds2.orders", o.Ref.FullName())

		assert.Equal(t, []string{"ds1", "ds2"}, store.ListDatasets("proj1"))
		assert.Len(t, store.ListCachedTables(), 2)
	})

	t.Run("non-blocking async GetTable, onUpdate on success & 404, backoff, and disk TTL", func(t *testing.T) {
		t.Parallel()

		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			if r.URL.Path == "/projects/p/datasets/d/tables/live" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"tableReference": {"projectId":"p","datasetId":"d","tableId":"live"},
					"schema": {"fields": [{"name":"id","type":"INT64"}]}
				}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		cacheDir := t.TempDir()
		now := time.Now()
		var clockMu sync.Mutex
		currentTime := func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return now
		}

		updates := make(chan catalog.TableRef, 8)
		rest := catalog.NewRestClient(
			catalog.WithBaseURL(srv.URL),
			catalog.WithHTTPClient(srv.Client()),
			catalog.WithTokenProvider(func(context.Context) (string, error) { return "", nil }),
		)

		opts := bqconfig.DefaultLSPOptions()
		opts.BigQueryAPIEnabled = true
		opts.CacheDir = cacheDir
		opts.CacheTTLSeconds = 60

		var store *catalog.Store
		store, err := catalog.NewStore(
			opts,
			catalog.WithRestClient(rest),
			catalog.WithClock(currentTime),
			catalog.WithOnUpdate(func(ref catalog.TableRef) {
				// Must not deadlock when calling back into Store.GetTable/IsPending inside onUpdate.
				if store != nil {
					_ = store.IsPending(ref)
				}
				updates <- ref
			}),
		)
		require.NoError(t, err)
		defer func() { _ = store.Close() }()

		liveRef := catalog.TableRef{Project: "p", Dataset: "d", Table: "live"}
		missingRef := catalog.TableRef{Project: "p", Dataset: "d", Table: "missing"}

		// Non-blocking misses trigger async fetches.
		_, ok := store.GetTable(liveRef)
		assert.False(t, ok)

		ref1 := <-updates
		assert.Equal(t, liveRef, ref1)
		assert.False(t, store.IsPending(liveRef))

		meta, ok := store.GetTable(liveRef)
		require.True(t, ok)
		assert.Equal(t, "p.d.live", meta.Ref.FullName())

		// Verify SQLite disk cache file exists with 0600 permissions and WAL mode.
		cacheFile := filepath.Join(cacheDir, catalog.DiskCacheFilename)
		info, err := os.Stat(cacheFile)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

		checkDB, err := sql.Open("sqlite", cacheFile)
		require.NoError(t, err)
		var journalMode string
		require.NoError(t, checkDB.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&journalMode))
		assert.Equal(t, "wal", strings.ToLower(journalMode))
		require.NoError(t, checkDB.Close())

		// Verify 404 still fires onUpdate and clears IsPending, and negative backoff prevents refetch.
		_, ok = store.GetTable(missingRef)
		assert.False(t, ok)
		ref2 := <-updates
		assert.Equal(t, missingRef, ref2)
		assert.False(t, store.IsPending(missingRef))

		hitsBefore := hits.Load()
		_, ok = store.GetTable(missingRef)
		assert.False(t, ok)
		assert.False(t, store.IsPending(missingRef))
		assert.Equal(t, hitsBefore, hits.Load())

		// New Store reads valid disk cache without HTTP hit.
		optsOffline := *opts
		optsOffline.BigQueryAPIEnabled = false
		store2, err := catalog.NewStore(&optsOffline, catalog.WithClock(currentTime))
		require.NoError(t, err)
		defer func() { _ = store2.Close() }()

		cached, ok := store2.GetTable(liveRef)
		require.True(t, ok)
		assert.Equal(t, "p.d.live", cached.Ref.FullName())

		// Expired disk cache (fetched_at older than TTL) is ignored.
		clockMu.Lock()
		now = now.Add(120 * time.Second)
		clockMu.Unlock()
		store3, err := catalog.NewStore(&optsOffline, catalog.WithClock(currentTime))
		require.NoError(t, err)
		defer func() { _ = store3.Close() }()

		_, ok = store3.GetTable(liveRef)
		assert.False(t, ok)

		// CacheTTLSeconds == 0 disables disk reuse even when fresh.
		clockMu.Lock()
		now = now.Add(-120 * time.Second)
		clockMu.Unlock()
		optsZeroTTL := optsOffline
		optsZeroTTL.CacheTTLSeconds = 0
		store4, err := catalog.NewStore(&optsZeroTTL, catalog.WithClock(currentTime))
		require.NoError(t, err)
		defer func() { _ = store4.Close() }()

		_, ok = store4.GetTable(liveRef)
		assert.False(t, ok)
	})

	t.Run("empty CacheDir disables disk writes", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schema":{"fields":[{"name":"x","type":"STRING"}]}}`))
		}))
		defer srv.Close()

		updates := make(chan catalog.TableRef, 1)
		opts := bqconfig.DefaultLSPOptions()
		opts.CacheDir = ""
		opts.BigQueryAPIEnabled = true

		store, err := catalog.NewStore(
			opts,
			catalog.WithRestClient(catalog.NewRestClient(
				catalog.WithBaseURL(srv.URL),
				catalog.WithHTTPClient(srv.Client()),
				catalog.WithTokenProvider(func(context.Context) (string, error) { return "", nil }),
			)),
			catalog.WithOnUpdate(func(ref catalog.TableRef) { updates <- ref }),
		)
		require.NoError(t, err)
		defer func() { _ = store.Close() }()

		_, ok := store.GetTable(catalog.TableRef{Project: "p", Dataset: "d", Table: "t"})
		assert.False(t, ok)
		<-updates
		_, ok = store.GetTable(catalog.TableRef{Project: "p", Dataset: "d", Table: "t"})
		assert.True(t, ok)
	})

	t.Run("corrupt, empty, wrong-type, non-json, and unreadable schema files are skipped while valid schemas load", func(t *testing.T) {
		t.Parallel()

		baseDir := t.TempDir()
		badDir := filepath.Join(baseDir, "schemas_bad")
		goodDir := filepath.Join(baseDir, "schemas_good")
		require.NoError(t, os.MkdirAll(badDir, 0o750))
		require.NoError(t, os.MkdirAll(goodDir, 0o750))

		// Valid table inside badDir.
		require.NoError(t, os.WriteFile(
			filepath.Join(badDir, "my-proj.analytics.events.json"),
			[]byte(`{"type":"TABLE","schema":{"fields":[{"name":"tenant_id","type":"STRING"}]}}`),
			0o600,
		))
		// Corrupt JSON, empty file, wrong JSON types, and non-.json file.
		invalidFiles := map[string]string{
			"my-proj.analytics.corrupt.json":   `{not valid json`,
			"my-proj.analytics.empty.json":     ``,
			"my-proj.analytics.null_type.json": `null`,
			"my-proj.analytics.num_type.json":  `123`,
			"my-proj.analytics.str_type.json":  `"str"`,
			"my-proj.analytics.arr_type.json":  `[123]`,
			"README.txt":                       `not a schema file`,
		}
		for name, content := range invalidFiles {
			require.NoError(t, os.WriteFile(filepath.Join(badDir, name), []byte(content), 0o600))
		}

		// File used as parent path to force a non-ENOENT os.Stat error (ENOTDIR) in loadSchemaDirs.
		notADirFile := filepath.Join(baseDir, "not_a_dir")
		require.NoError(t, os.WriteFile(notADirFile, []byte("file"), 0o600))
		enotdirSchemaDir := filepath.Join(notADirFile, "child")

		// Unreadable file and unreadable directory (skipped when running as root).
		unreadableFile := filepath.Join(badDir, "my-proj.analytics.unreadable.json")
		unreadableDir := filepath.Join(baseDir, "unreadable_dir")
		isNonRoot := os.Geteuid() != 0
		if isNonRoot {
			require.NoError(t, os.WriteFile(unreadableFile, []byte(`[{"name":"x","type":"INT64"}]`), 0o600))
			require.NoError(t, os.Chmod(unreadableFile, 0o000))
			t.Cleanup(func() { _ = os.Chmod(unreadableFile, 0o600) })

			require.NoError(t, os.MkdirAll(unreadableDir, 0o750))
			require.NoError(t, os.Chmod(unreadableDir, 0o000))
			t.Cleanup(func() { _ = os.Chmod(unreadableDir, 0o750) })
		}

		// Subsequent valid schema_dir after directories with errors must still load.
		require.NoError(t, os.WriteFile(
			filepath.Join(goodDir, "my-proj.analytics.users.json"),
			[]byte(`[{"name":"user_id","type":"INT64","mode":"REQUIRED"}]`),
			0o600,
		))

		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

		opts := bqconfig.DefaultLSPOptions()
		opts.CacheDir = ""
		opts.SchemaDirs = []string{badDir, enotdirSchemaDir, unreadableDir, goodDir}

		store, err := catalog.NewStore(opts, catalog.WithLogger(logger))
		require.NoError(t, err)
		defer func() { _ = store.Close() }()

		events, ok := store.GetTable(catalog.TableRef{Project: "my-proj", Dataset: "analytics", Table: "events"})
		require.True(t, ok)
		assert.Equal(t, "my-proj.analytics.events", events.Ref.FullName())

		users, ok := store.GetTable(catalog.TableRef{Project: "my-proj", Dataset: "analytics", Table: "users"})
		require.True(t, ok)
		assert.Equal(t, "my-proj.analytics.users", users.Ref.FullName())

		for _, skipped := range []string{"corrupt", "empty", "null_type", "num_type", "str_type", "arr_type", "unreadable"} {
			_, found := store.GetTable(catalog.TableRef{Project: "my-proj", Dataset: "analytics", Table: skipped})
			assert.False(t, found, "skipped schema %q should not be loaded", skipped)
		}

		assert.Len(t, store.ListCachedTables(), 2)

		logs := logBuf.String()
		for _, expected := range []string{
			"corrupt.json",
			"empty.json",
			"null_type.json",
			"num_type.json",
			"str_type.json",
			"arr_type.json",
			"skipping unreadable schema_dir",
		} {
			assert.Contains(t, logs, expected)
		}
		if isNonRoot {
			assert.Contains(t, logs, "unreadable.json")
			assert.Contains(t, logs, "unreadable_dir")
		}
	})

	t.Run("corrupt disk cache row and corrupt sqlite file recover gracefully", func(t *testing.T) {
		t.Parallel()

		// Case 1: corrupt payload inside an otherwise valid SQLite cache.db.
		cacheDir := t.TempDir()
		dbPath := filepath.Join(cacheDir, catalog.DiskCacheFilename)
		seedDB, err := sql.Open("sqlite", dbPath)
		require.NoError(t, err)
		_, err = seedDB.ExecContext(t.Context(), `
			CREATE TABLE table_cache (
				project_lower TEXT NOT NULL,
				dataset_lower TEXT NOT NULL,
				table_lower   TEXT NOT NULL,
				project       TEXT NOT NULL,
				dataset       TEXT NOT NULL,
				table_name    TEXT NOT NULL,
				table_type    TEXT NOT NULL DEFAULT '',
				friendly_name TEXT NOT NULL DEFAULT '',
				payload       BLOB NOT NULL,
				fetched_at    INTEGER NOT NULL,
				PRIMARY KEY (project_lower, dataset_lower, table_lower)
			);
			INSERT INTO table_cache VALUES (
				'p', 'd', 'corrupt_cached',
				'p', 'd', 'corrupt_cached', 'TABLE', '',
				X'7B6E6F742076616C6964206A736F6E', ?
			);
		`, time.Now().UnixNano())
		require.NoError(t, err)
		require.NoError(t, seedDB.Close())

		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		opts := bqconfig.DefaultLSPOptions()
		opts.CacheDir = cacheDir
		opts.CacheTTLSeconds = 60
		opts.BigQueryAPIEnabled = false

		store, err := catalog.NewStore(opts, catalog.WithLogger(logger))
		require.NoError(t, err)
		defer func() { _ = store.Close() }()

		ref := catalog.TableRef{Project: "p", Dataset: "d", Table: "corrupt_cached"}
		meta, ok := store.GetTable(ref)
		assert.False(t, ok)
		assert.Nil(t, meta)
		assert.Contains(t, logBuf.String(), "ignoring corrupt disk cache entry")

		// With BigQueryAPIEnabled, corrupt disk cache does not block async refresh.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schema":{"fields":[{"name":"id","type":"INT64"}]}}`))
		}))
		defer srv.Close()

		updates := make(chan catalog.TableRef, 1)
		optsOnline := *opts
		optsOnline.BigQueryAPIEnabled = true

		storeOnline, err := catalog.NewStore(
			&optsOnline,
			catalog.WithRestClient(catalog.NewRestClient(
				catalog.WithBaseURL(srv.URL),
				catalog.WithHTTPClient(srv.Client()),
				catalog.WithTokenProvider(func(context.Context) (string, error) { return "", nil }),
			)),
			catalog.WithOnUpdate(func(r catalog.TableRef) { updates <- r }),
		)
		require.NoError(t, err)
		defer func() { _ = storeOnline.Close() }()

		_, ok = storeOnline.GetTable(ref)
		assert.False(t, ok)
		<-updates
		refreshed, ok := storeOnline.GetTable(ref)
		require.True(t, ok)
		assert.Equal(t, "p.d.corrupt_cached", refreshed.Ref.FullName())

		// Case 2: completely corrupted (non-SQLite) cache.db file is recreated cleanly.
		corruptDir := t.TempDir()
		require.NoError(t, os.WriteFile(
			filepath.Join(corruptDir, catalog.DiskCacheFilename),
			[]byte("not a valid sqlite database header at all"),
			0o600,
		))

		var corruptLog bytes.Buffer
		corruptOpts := *opts
		corruptOpts.CacheDir = corruptDir
		corruptOpts.BigQueryAPIEnabled = true
		corruptUpdates := make(chan catalog.TableRef, 1)

		corruptStore, err := catalog.NewStore(
			&corruptOpts,
			catalog.WithLogger(slog.New(slog.NewTextHandler(&corruptLog, &slog.HandlerOptions{Level: slog.LevelWarn}))),
			catalog.WithRestClient(catalog.NewRestClient(
				catalog.WithBaseURL(srv.URL),
				catalog.WithHTTPClient(srv.Client()),
				catalog.WithTokenProvider(func(context.Context) (string, error) { return "", nil }),
			)),
			catalog.WithOnUpdate(func(r catalog.TableRef) { corruptUpdates <- r }),
		)
		require.NoError(t, err)
		defer func() { _ = corruptStore.Close() }()

		_, ok = corruptStore.GetTable(ref)
		assert.False(t, ok)
		<-corruptUpdates
		assert.Contains(t, corruptLog.String(), "recreating corrupt disk cache database")

		// Verify newly recreated cache.db persists across a fresh offline Store.
		verifyOpts := corruptOpts
		verifyOpts.BigQueryAPIEnabled = false
		verifyStore, err := catalog.NewStore(&verifyOpts)
		require.NoError(t, err)
		defer func() { _ = verifyStore.Close() }()

		recovered, ok := verifyStore.GetTable(ref)
		require.True(t, ok)
		assert.Equal(t, "p.d.corrupt_cached", recovered.Ref.FullName())
	})
}

func TestGoogleSQLBridgeAnalyzeLookupWildcardAndOverlays(t *testing.T) {
	opts := catalog.DefaultLSPLanguageOptions()
	require.NotNil(t, opts)

	lspOpts := bqconfig.DefaultLSPOptions()
	lspOpts.CacheDir = ""
	lspOpts.DefaultProject = "myproj"
	lspOpts.DefaultDataset = "analytics"

	store, err := catalog.NewStore(lspOpts)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	// Seed rich table with STRUCT, ARRAY<STRUCT>, RANGE<DATE>, UUID, and ingestion-time partitioning.
	store.PutTable(&catalog.TableMeta{
		Ref:              catalog.TableRef{Project: "myproj", Dataset: "analytics", Table: "events_20260101"},
		Type:             catalog.TableTypeTable,
		TimePartitioning: &catalog.TimePartitioning{Type: "DAY", RequirePartitionFilter: true},
		Clustering:       &catalog.ClusteringInfo{Fields: []string{"user_id"}},
		Schema: []catalog.ColumnMeta{
			{Name: "user_id", Type: "INT64", Mode: catalog.ModeRequired},
			{Name: "trace_id", Type: "UUID"},
			{Name: "active_range", Type: "RANGE", RangeElementType: &catalog.RangeElementType{Type: "DATE"}},
			{
				Name: "items",
				Type: "STRUCT",
				Mode: catalog.ModeRepeated,
				Fields: []catalog.ColumnMeta{
					{Name: "sku", Type: "STRING"},
					{
						Name: "nested",
						Type: "STRUCT",
						Fields: []catalog.ColumnMeta{
							{Name: "qty", Type: "INT64"},
						},
					},
				},
			},
		},
	})

	store.PutTable(&catalog.TableMeta{
		Ref:  catalog.TableRef{Project: "myproj", Dataset: "analytics", Table: "model_input"},
		Type: catalog.TableTypeTable,
		Schema: []catalog.ColumnMeta{
			{Name: "prompt", Type: "STRING"},
			{Name: "embedding", Type: "FLOAT64", Mode: catalog.ModeRepeated},
		},
	})

	tf, err := googlesql.NewTypeFactory()
	require.NoError(t, err)

	overlay, err := catalog.NewScriptOverlay(tf)
	require.NoError(t, err)

	int64Type, err := tf.GetInt64()
	require.NoError(t, err)
	strType, err := tf.GetString()
	require.NoError(t, err)

	require.NoError(t, overlay.AddVariable("threshold", int64Type))
	require.NoError(t, overlay.AddTempTable(
		catalog.TableRef{Table: "tmp_scores"},
		[]catalog.OverlayColumn{
			{Name: "user_id", Type: int64Type},
			{Name: "label", Type: strType},
		},
	))

	cat, err := catalog.NewCatalogForAnalysis(store, opts, overlay)
	require.NoError(t, err)

	analyzerOpts, err := googlesql.NewAnalyzerOptions(opts)
	require.NoError(t, err)
	require.NoError(t, analyzerOpts.SetRecordParseLocations(true))
	require.NoError(t, catalog.RegisterBigQuerySystemVariables(analyzerOpts, cat.TypeFactory()))

	parserOpts, err := googlesql.NewParserOptions()
	require.NoError(t, err)
	require.NoError(t, parserOpts.SetLanguageOptions(catalog.DefaultLSPLanguageOptions()))

	analyzeSQL := func(sql string) (*googlesql.AnalyzerOutput, error) {
		po, parseErr := googlesql.ParseStatement(sql, parserOpts)
		require.NoError(t, parseErr, "parse failed for: %s", sql)

		stmt, stmtErr := po.Statement()
		require.NoError(t, stmtErr)

		return googlesql.AnalyzeStatementFromParserAST(
			stmt,
			analyzerOpts,
			sql,
			cat.GoogleSQLCatalog(),
			cat.TypeFactory(),
		)
	}

	t.Run("1-part 2-part 3-part backticked ingestion pseudo-cols and LookupTable", func(t *testing.T) {
		queries := []string{
			"SELECT user_id, trace_id, active_range, _PARTITIONTIME, _PARTITIONDATE FROM events_20260101 WHERE _PARTITIONDATE = DATE '2026-01-01'",
			"SELECT it.nested.qty FROM analytics.events_20260101, UNNEST(items) AS it",
			"SELECT user_id FROM myproj.analytics.events_20260101",
			"SELECT user_id FROM `myproj.analytics.events_20260101`",
		}

		for _, q := range queries {
			out, analyzeErr := analyzeSQL(q)
			require.NoError(t, analyzeErr, "query: %s", q)
			stmt, stmtErr := out.ResolvedStatement()
			require.NoError(t, stmtErr)
			require.NotNil(t, stmt)

			var foundMeta *catalog.TableMeta
			require.NoError(t, walkResolvedScans(stmt, func(scan *googlesql.ResolvedTableScan) {
				tbl, tblErr := scan.Table()
				require.NoError(t, tblErr)
				meta, ok := cat.LookupTable(tbl)
				if ok {
					foundMeta = meta
				}
			}))
			require.NotNil(t, foundMeta, "LookupTable failed for query: %s", q)
			assert.Equal(t, "myproj.analytics.events_20260101", foundMeta.Ref.FullName())
		}
	})

	t.Run("wildcard table with _TABLE_SUFFIX", func(t *testing.T) {
		out, analyzeErr := analyzeSQL(
			"SELECT user_id, _TABLE_SUFFIX FROM `myproj.analytics.events_*` WHERE _TABLE_SUFFIX >= '20260101'",
		)
		require.NoError(t, analyzeErr)
		stmt, stmtErr := out.ResolvedStatement()
		require.NoError(t, stmtErr)
		require.NotNil(t, stmt)
	})

	t.Run("script overlay variable, temp table, and system variables", func(t *testing.T) {
		out, analyzeErr := analyzeSQL(
			"SELECT t.user_id, t.label, @@project_id, @@script.bytes_billed, @@script.bytes_scanned FROM tmp_scores AS t WHERE t.user_id > threshold",
		)
		require.NoError(t, analyzeErr)
		stmt, stmtErr := out.ResolvedStatement()
		require.NoError(t, stmtErr)
		require.NotNil(t, stmt)
	})

	t.Run("AnalyzeStatement directly with DefaultLSPLanguageOptions", func(t *testing.T) {
		out, analyzeErr := googlesql.AnalyzeStatement(
			"SELECT 1",
			analyzerOpts,
			cat.GoogleSQLCatalog(),
			cat.TypeFactory(),
		)
		require.NoError(t, analyzeErr)
		stmt, stmtErr := out.ResolvedStatement()
		require.NoError(t, stmtErr)
		require.NotNil(t, stmt)
	})

	t.Run("BigQuery functions, aggregates in GROUP BY, and TVFs", func(t *testing.T) {
		store.PutTable(&catalog.TableMeta{
			Ref:  catalog.TableRef{Project: "myproj", Dataset: "analytics", Table: "emp"},
			Type: catalog.TableTypeTable,
			Schema: []catalog.ColumnMeta{
				{Name: "dept", Type: "STRING"},
				{Name: "name", Type: "STRING"},
				{Name: "salary", Type: "INT64"},
			},
		})

		queries := []string{
			"SELECT AI.GENERATE('summarize', STRUCT('gemini-2.5-flash' AS model)).result, AI.GENERATE_TEXT('hi'), AI.GENERATE_BOOL('ok'), AI.GENERATE_INT('1'), AI.GENERATE_DOUBLE('1.5')",
			"SELECT NET.HOST('https://example.com'), HLL_COUNT.INIT(user_id) FROM events_20260101",
			"SELECT KEYS.NEW_KEYSET('AEAD_AES_GCM_256'), AEAD.ENCRYPT(KEYS.NEW_KEYSET('AEAD_AES_GCM_256'), 'secret', '')",
			"SELECT * FROM ML.PREDICT(MODEL myproj.analytics.my_model, TABLE myproj.analytics.model_input)",
			"SELECT * FROM ML.EVALUATE(MODEL myproj.analytics.my_model, TABLE myproj.analytics.model_input)",
			"SELECT * FROM AI.GENERATE_TABLE(MODEL myproj.analytics.gen_model, TABLE myproj.analytics.model_input)",
			"SELECT * FROM AI.GENERATE_TEXT(MODEL myproj.analytics.gen_model, TABLE myproj.analytics.model_input)",
			"SELECT * FROM AI.GENERATE_TEXT(MODEL myproj.analytics.gen_model, TABLE myproj.analytics.model_input, STRUCT(0.2 AS temperature))",
			"SELECT * FROM VECTOR_SEARCH(TABLE myproj.analytics.model_input, 'embedding', (SELECT [1.0, 2.0] AS q))",
			"SELECT prompt FROM model_input WHERE SEARCH(prompt, 'hello')",
			"SELECT * FROM EXTERNAL_OBJECT_TRANSFORM(TABLE myproj.analytics.model_input, ['RESIZE'])",
			"SELECT dept, MAX_BY(name, salary), MIN_BY(name, salary), MAX_BY(salary, name) + 1 FROM emp GROUP BY dept",
			"SELECT dept, SAFE.MAX_BY(name, salary), SAFE.MIN_BY(name, salary) FROM emp GROUP BY dept",
			"SELECT dept, MAX_BY(name, salary) OVER (PARTITION BY dept) FROM emp",
		}

		for _, q := range queries {
			out, analyzeErr := analyzeSQL(q)
			require.NoError(t, analyzeErr, "query: %s", q)
			stmt, stmtErr := out.ResolvedStatement()
			require.NoError(t, stmtErr)
			require.NotNil(t, stmt)
		}
	})

	t.Run("Pipe syntax, CREATE TABLE, and INSERT INTO DML", func(t *testing.T) {
		queries := []string{
			"FROM events_20260101 |> WHERE user_id > 0 |> SELECT user_id",
			"FROM emp |> WHERE salary > 0 |> AGGREGATE MAX_BY(name, salary) AS top_earner GROUP BY dept",
			"CREATE TABLE myproj.analytics.new_events (event_id INT64, payload STRING)",
			"CREATE TEMP TABLE tmp_copy AS SELECT user_id FROM events_20260101",
			"INSERT INTO myproj.analytics.emp (dept, name, salary) VALUES ('eng', 'ada', 200)",
			"INSERT INTO emp (dept, name, salary) SELECT 'sales', 'bob', user_id FROM events_20260101",
			"SELECT @@script.bytes_scanned",
		}

		for _, q := range queries {
			out, analyzeErr := analyzeSQL(q)
			require.NoError(t, analyzeErr, "query: %s", q)
			stmt, stmtErr := out.ResolvedStatement()
			require.NoError(t, stmtErr)
			require.NotNil(t, stmt)
		}
	})

	t.Run("unresolved table tracking and SuggestTable", func(t *testing.T) {
		_, analyzeErr := analyzeSQL("SELECT user_id FROM myproj.analytics.events_20260102")
		require.Error(t, analyzeErr)
		assert.Contains(t, analyzeErr.Error(), "events_20260101")

		unresolved := cat.UnresolvedRefs()
		require.NotEmpty(t, unresolved)
		assert.False(t, cat.HasPendingTables())

		_, fnErr := analyzeSQL("SELECT totally_unknown_fn(1)")
		require.Error(t, fnErr)
		assert.Contains(t, fnErr.Error(), "function not found")
	})

	t.Run("AI.FORECAST, AI.SEARCH, and AddTableMeta overlay", func(t *testing.T) {
		require.NoError(t, overlay.AddTableMeta(&catalog.TableMeta{
			Ref: catalog.TableRef{Project: "myproj", Dataset: "analytics", Table: "series_tbl"},
			Schema: []catalog.ColumnMeta{
				{Name: "ds", Type: "DATE"},
				{Name: "val", Type: "FLOAT64"},
			},
		}))

		cat2, catErr := catalog.NewCatalogForAnalysis(store, opts, overlay)
		require.NoError(t, catErr)

		for _, q := range []string{
			"SELECT * FROM AI.FORECAST(TABLE myproj.analytics.series_tbl)",
			"SELECT * FROM AI.SEARCH(TABLE myproj.analytics.model_input, 'prompt', 'hello')",
		} {
			po, parseErr := googlesql.ParseStatement(q, parserOpts)
			require.NoError(t, parseErr)
			stmt, stmtErr := po.Statement()
			require.NoError(t, stmtErr)

			out, aErr := googlesql.AnalyzeStatementFromParserAST(
				stmt,
				analyzerOpts,
				q,
				cat2.GoogleSQLCatalog(),
				cat2.TypeFactory(),
			)
			require.NoError(t, aErr, "query: %s", q)
			res, resErr := out.ResolvedStatement()
			require.NoError(t, resErr)
			require.NotNil(t, res)
		}
	})

	t.Run("HasPendingTables and PendingRefs while async fetch is in flight", func(t *testing.T) {
		gate := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			<-gate
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schema":{"fields":[{"name":"id","type":"INT64"}]}}`))
		}))
		defer srv.Close()

		done := make(chan catalog.TableRef, 1)
		apiOpts := bqconfig.DefaultLSPOptions()
		apiOpts.CacheDir = ""
		apiOpts.BigQueryAPIEnabled = true
		apiOpts.DefaultProject = "myproj"
		apiOpts.DefaultDataset = "analytics"

		asyncStore, storeErr := catalog.NewStore(
			apiOpts,
			catalog.WithRestClient(catalog.NewRestClient(
				catalog.WithBaseURL(srv.URL),
				catalog.WithHTTPClient(srv.Client()),
				catalog.WithTokenProvider(func(context.Context) (string, error) { return "", nil }),
			)),
		)
		require.NoError(t, storeErr)
		asyncStore.OnUpdate(func(ref catalog.TableRef) { done <- ref })
		defer func() { _ = asyncStore.Close() }()

		asyncCat, catErr := catalog.NewCatalogForAnalysis(asyncStore, opts, nil)
		require.NoError(t, catErr)

		q := "SELECT id FROM async_tbl"
		po, parseErr := googlesql.ParseStatement(q, parserOpts)
		require.NoError(t, parseErr)
		stmt, stmtErr := po.Statement()
		require.NoError(t, stmtErr)

		_, aErr := googlesql.AnalyzeStatementFromParserAST(
			stmt,
			analyzerOpts,
			q,
			asyncCat.GoogleSQLCatalog(),
			asyncCat.TypeFactory(),
		)
		require.Error(t, aErr)
		assert.True(t, asyncCat.HasPendingTables())
		require.Len(t, asyncCat.PendingRefs(), 1)
		assert.Equal(t, "myproj.analytics.async_tbl", asyncCat.PendingRefs()[0].FullName())

		close(gate)
		<-done
		assert.False(t, asyncCat.HasPendingTables())
		assert.Empty(t, asyncCat.PendingRefs())
	})

	t.Run("sorted UnresolvedRefs and PendingRefs across multiple tables", func(t *testing.T) {
		gate := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			<-gate
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schema":{"fields":[{"name":"id","type":"INT64"}]}}`))
		}))
		defer srv.Close()

		var updateCount atomic.Int32
		allDone := make(chan struct{})

		apiOpts := bqconfig.DefaultLSPOptions()
		apiOpts.CacheDir = ""
		apiOpts.BigQueryAPIEnabled = true
		apiOpts.DefaultProject = "myproj"
		apiOpts.DefaultDataset = "analytics"

		asyncStore, storeErr := catalog.NewStore(
			apiOpts,
			catalog.WithRestClient(catalog.NewRestClient(
				catalog.WithBaseURL(srv.URL),
				catalog.WithHTTPClient(srv.Client()),
				catalog.WithTokenProvider(func(context.Context) (string, error) { return "", nil }),
			)),
			catalog.WithOnUpdate(func(catalog.TableRef) {
				if updateCount.Add(1) == 3 {
					close(allDone)
				}
			}),
		)
		require.NoError(t, storeErr)
		defer func() { _ = asyncStore.Close() }()

		asyncCat, catErr := catalog.NewCatalogForAnalysis(asyncStore, opts, nil)
		require.NoError(t, catErr)

		// Trigger misses in reverse/unsorted order.
		for _, q := range []string{
			"SELECT id FROM z_tbl",
			"SELECT id FROM a_tbl",
			"SELECT id FROM m_tbl",
		} {
			po, parseErr := googlesql.ParseStatement(q, parserOpts)
			require.NoError(t, parseErr)
			stmt, stmtErr := po.Statement()
			require.NoError(t, stmtErr)

			_, aErr := googlesql.AnalyzeStatementFromParserAST(
				stmt,
				analyzerOpts,
				q,
				asyncCat.GoogleSQLCatalog(),
				asyncCat.TypeFactory(),
			)
			require.Error(t, aErr)
		}

		wantRefs := []catalog.TableRef{
			{Project: "myproj", Dataset: "analytics", Table: "a_tbl"},
			{Project: "myproj", Dataset: "analytics", Table: "m_tbl"},
			{Project: "myproj", Dataset: "analytics", Table: "z_tbl"},
		}
		for range 15 {
			assert.Equal(t, wantRefs, asyncCat.UnresolvedRefs())
			assert.Equal(t, wantRefs, asyncCat.PendingRefs())
		}
		assert.True(t, asyncCat.HasPendingTables())

		close(gate)
		<-allDone
		assert.False(t, asyncCat.HasPendingTables())
		assert.Empty(t, asyncCat.PendingRefs())
	})

	t.Run("domain-scoped dotted project IDs, 5-arg VECTOR_SEARCH, nonexistent CAST type, and FLOAT64 overlay", func(t *testing.T) {
		store.PutTable(&catalog.TableMeta{
			Ref:  catalog.TableRef{Project: "example.com:my-proj", Dataset: "ds", Table: "tbl"},
			Type: catalog.TableTypeTable,
			Schema: []catalog.ColumnMeta{
				{Name: "id", Type: "INT64"},
			},
		})
		store.PutTable(&catalog.TableMeta{
			Ref:  catalog.TableRef{Project: "example.com:my-proj", Dataset: "ds", Table: "events_20260101"},
			Type: catalog.TableTypeTable,
			Schema: []catalog.ColumnMeta{
				{Name: "id", Type: "INT64"},
			},
		})

		domainQueries := []struct {
			sql      string
			wantName string
		}{
			{
				sql:      "SELECT id FROM `example.com:my-proj.ds.tbl`",
				wantName: "example.com:my-proj.ds.tbl",
			},
			{
				sql:      "SELECT id FROM `example.com:my-proj`.ds.tbl",
				wantName: "example.com:my-proj.ds.tbl",
			},
			{
				sql:      "SELECT id, _TABLE_SUFFIX FROM `example.com:my-proj.ds.events_*`",
				wantName: "example.com:my-proj.ds.events_*",
			},
		}

		for _, tc := range domainQueries {
			out, aErr := analyzeSQL(tc.sql)
			require.NoError(t, aErr, "query: %s", tc.sql)
			res, resErr := out.ResolvedStatement()
			require.NoError(t, resErr)

			var gotName string
			require.NoError(t, walkResolvedScans(res, func(scan *googlesql.ResolvedTableScan) {
				tbl, tblErr := scan.Table()
				require.NoError(t, tblErr)
				if meta, ok := cat.LookupTable(tbl); ok {
					gotName = meta.Ref.FullName()
				}
			}))
			assert.Equal(t, tc.wantName, gotName)
		}

		// Plain 4-part path without colon must still fail.
		_, fourPartErr := analyzeSQL("SELECT id FROM a.b.c.d")
		require.Error(t, fourPartErr)

		// 5+ arg VECTOR_SEARCH, ML.PREDICT, and AI.FORECAST TVFs.
		for _, q := range []string{
			"SELECT * FROM VECTOR_SEARCH(TABLE myproj.analytics.model_input, 'embedding', (SELECT [1.0, 2.0] AS q), 'embedding', 5)",
			"SELECT * FROM ML.PREDICT(MODEL myproj.analytics.my_model, TABLE myproj.analytics.model_input, STRUCT(true AS keep_original_columns), 'opt4', 5)",
			"SELECT * FROM AI.FORECAST(TABLE myproj.analytics.series_tbl, 'val', 'ds', 10, 0.95, 'ARIMA')",
		} {
			out, aErr := analyzeSQL(q)
			require.NoError(t, aErr, "query: %s", q)
			res, resErr := out.ResolvedStatement()
			require.NoError(t, resErr)
			require.NotNil(t, res)
		}

		// Nonexistent type in CAST and unknown procedure in CALL must return clean errors (no wasm trap).
		_, castErr := analyzeSQL("SELECT CAST(1 AS NONEXISTENT_TYPE)")
		require.Error(t, castErr)
		assert.NotContains(t, castErr.Error(), "wasm trap")

		_, procErr := analyzeSQL("CALL unknown_proc()")
		require.Error(t, procErr)
		assert.Contains(t, procErr.Error(), "procedure not found: unknown_proc")

		// tf.GetDouble() in ScriptOverlay -> AddTempTable -> AddTableMeta round-trip must format as FLOAT64 and support arithmetic.
		doubleType, dErr := tf.GetDouble()
		require.NoError(t, dErr)
		require.NoError(t, overlay.AddTempTable(
			catalog.TableRef{Table: "tmp_doubles"},
			[]catalog.OverlayColumn{{Name: "c_double", Type: doubleType}},
		))
		require.NoError(t, overlay.AddTableMeta(&catalog.TableMeta{
			Ref: catalog.TableRef{Table: "raw_doubles"},
			Schema: []catalog.ColumnMeta{
				{Name: "d", Type: "DOUBLE"},
			},
		}))

		catDouble, cdErr := catalog.NewCatalogForAnalysis(store, opts, overlay)
		require.NoError(t, cdErr)

		for _, qDouble := range []string{
			"SELECT c_double + 1.5 FROM tmp_doubles",
			"SELECT d + 1.5 FROM raw_doubles",
		} {
			po, pErr := googlesql.ParseStatement(qDouble, parserOpts)
			require.NoError(t, pErr)
			stmt, sErr := po.Statement()
			require.NoError(t, sErr)
			out, aErr := googlesql.AnalyzeStatementFromParserAST(
				stmt,
				analyzerOpts,
				qDouble,
				catDouble.GoogleSQLCatalog(),
				catDouble.TypeFactory(),
			)
			require.NoError(t, aErr, "query: %s", qDouble)
			res, rErr := out.ResolvedStatement()
			require.NoError(t, rErr)

			if qDouble == "SELECT c_double + 1.5 FROM tmp_doubles" {
				var doubleMeta *catalog.TableMeta
				require.NoError(t, walkResolvedScans(res, func(scan *googlesql.ResolvedTableScan) {
					tbl, tblErr := scan.Table()
					require.NoError(t, tblErr)
					if meta, ok := catDouble.LookupTable(tbl); ok {
						doubleMeta = meta
					}
				}))
				require.NotNil(t, doubleMeta)
				scoreCol, ok := doubleMeta.FindColumn("c_double")
				require.True(t, ok)
				assert.Equal(t, "FLOAT64", scoreCol.Type)
				assert.Equal(t, "FLOAT64", scoreCol.SQLType())
			}
		}
	})
}

func TestSharedRestClientConcurrentStoresRaceAndCancellation(t *testing.T) {
	t.Parallel()

	t.Run("two Stores sharing one RestClient concurrently fetch same TableRef cleanly", func(t *testing.T) {
		t.Parallel()

		var hits atomic.Int32
		gate := make(chan struct{})

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			<-gate
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"tableReference": {"projectId":"p1","datasetId":"d1","tableId":"shared_tbl"},
				"labels": {"env":"prod"},
				"numRows": "42",
				"timePartitioning": {"type":"DAY","field":"ts"},
				"clustering": {"fields":["id"]},
				"schema": {"fields":[{"name":"id","type":"INT64"}]}
			}`))
		}))
		defer srv.Close()

		rest := catalog.NewRestClient(
			catalog.WithBaseURL(srv.URL),
			catalog.WithHTTPClient(srv.Client()),
			catalog.WithTokenProvider(func(context.Context) (string, error) { return "tok", nil }),
		)

		upd1 := make(chan catalog.TableRef, 1)
		upd2 := make(chan catalog.TableRef, 1)

		opts1 := bqconfig.DefaultLSPOptions()
		opts1.CacheDir = ""
		opts1.BigQueryAPIEnabled = true
		opts2 := bqconfig.DefaultLSPOptions()
		opts2.CacheDir = ""
		opts2.BigQueryAPIEnabled = true

		store1, err := catalog.NewStore(opts1, catalog.WithRestClient(rest), catalog.WithOnUpdate(func(ref catalog.TableRef) {
			upd1 <- ref
		}))
		require.NoError(t, err)
		defer func() { _ = store1.Close() }()

		store2, err := catalog.NewStore(opts2, catalog.WithRestClient(rest), catalog.WithOnUpdate(func(ref catalog.TableRef) {
			upd2 <- ref
		}))
		require.NoError(t, err)
		defer func() { _ = store2.Close() }()

		ref := catalog.TableRef{Project: "p1", Dataset: "d1", Table: "shared_tbl"}
		_, ok1 := store1.GetTable(ref)
		_, ok2 := store2.GetTable(ref)
		assert.False(t, ok1)
		assert.False(t, ok2)

		time.Sleep(25 * time.Millisecond)
		close(gate)

		<-upd1
		<-upd2

		assert.Equal(t, int32(1), hits.Load())

		m1, ok1 := store1.GetTable(ref)
		m2, ok2 := store2.GetTable(ref)
		require.True(t, ok1)
		require.True(t, ok2)
		assert.NotSame(t, m1, m2)

		// Mutating clone or PutTable source must not affect stored metadata.
		m1Clone := m1.Clone()
		m1Clone.Labels["env"] = "mutated"
		m1Clone.Clustering.Fields[0] = "other"
		m1Clone.Schema[0].Name = "changed"
		assert.Equal(t, "prod", m2.Labels["env"])
		assert.Equal(t, "id", m2.Clustering.Fields[0])
		assert.Equal(t, "id", m2.Schema[0].Name)

		srcMeta := &catalog.TableMeta{
			Ref:               catalog.TableRef{Project: "p1", Dataset: "d1", Table: "isolated_put"},
			Labels:            map[string]string{"team": "data"},
			NumRows:           new(catalog.FlexInt64(10)),
			NumBytes:          new(catalog.FlexInt64(20)),
			RangePartitioning: &catalog.RangePartitioning{Field: "id", Range: &catalog.IntRange{Start: 0, End: 100, Interval: 10}},
			Clustering:        &catalog.ClusteringInfo{Fields: []string{"id"}},
			View:              &catalog.ViewDefinition{Query: "SELECT 1"},
			MaterializedView:  &catalog.MaterializedViewDefinition{Query: "SELECT 1"},
			ExternalDataConfig: &catalog.ExternalDataConfiguration{
				SourceUris: []string{"gs://bucket/a.parquet"},
				Schema:     &catalog.TableSchema{Fields: []catalog.ColumnMeta{{Name: "ext_col", Type: "STRING"}}},
			},
			Schema: []catalog.ColumnMeta{
				{
					Name:             "rng",
					Type:             "RANGE",
					RangeElementType: &catalog.RangeElementType{Type: "DATE"},
					PolicyTags:       &catalog.PolicyTags{Names: []string{"projects/p/locations/us/taxonomies/1/policyTags/2"}},
					Fields:           []catalog.ColumnMeta{{Name: "nested", Type: "INT64"}},
				},
			},
		}
		store1.PutTable(srcMeta)
		srcMeta.Labels["team"] = "mutated"
		*srcMeta.NumRows = 999
		srcMeta.RangePartitioning.Range.End = 999
		srcMeta.Clustering.Fields[0] = "mutated"
		srcMeta.View.Query = "mutated"
		srcMeta.MaterializedView.Query = "mutated"
		srcMeta.ExternalDataConfig.SourceUris[0] = "mutated"
		srcMeta.ExternalDataConfig.Schema.Fields[0].Name = "mutated"
		srcMeta.Schema[0].RangeElementType.Type = "TIMESTAMP"
		srcMeta.Schema[0].PolicyTags.Names[0] = "mutated"
		srcMeta.Schema[0].Fields[0].Name = "mutated"

		stored, ok := store1.GetTable(srcMeta.Ref)
		require.True(t, ok)
		assert.Equal(t, "data", stored.Labels["team"])
		assert.Equal(t, int64(10), stored.NumRows.Int64())
		assert.Equal(t, int64(100), stored.RangePartitioning.Range.End.Int64())
		assert.Equal(t, "id", stored.Clustering.Fields[0])
		assert.Equal(t, "SELECT 1", stored.View.Query)
		assert.Equal(t, "SELECT 1", stored.MaterializedView.Query)
		assert.Equal(t, "gs://bucket/a.parquet", stored.ExternalDataConfig.SourceUris[0])
		assert.Equal(t, "ext_col", stored.ExternalDataConfig.Schema.Fields[0].Name)
		assert.Equal(t, "DATE", stored.Schema[0].RangeElementType.Type)
		assert.Equal(t, "projects/p/locations/us/taxonomies/1/policyTags/2", stored.Schema[0].PolicyTags.Names[0])
		assert.Equal(t, "nested", stored.Schema[0].Fields[0].Name)
	})

	t.Run("FetchTable respects 40ms caller deadline and shared flight keeps waiting for active caller", func(t *testing.T) {
		t.Parallel()

		var hits atomic.Int32
		gate := make(chan struct{})

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			<-gate
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schema":{"fields":[{"name":"id","type":"INT64"}]}}`))
		}))
		defer srv.Close()

		rest := catalog.NewRestClient(
			catalog.WithBaseURL(srv.URL),
			catalog.WithHTTPClient(srv.Client()),
			catalog.WithTimeout(1*time.Second),
			catalog.WithTokenProvider(func(context.Context) (string, error) { return "", nil }),
		)

		ref := catalog.TableRef{Project: "p1", Dataset: "d1", Table: "slow_flight"}

		// Single caller with 40ms deadline returns promptly (< 250ms) instead of 1s.
		ctxSingle, cancelSingle := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancelSingle()

		start := time.Now()
		_, err := rest.FetchTable(ctxSingle, ref)
		elapsed := time.Since(start)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Less(t, elapsed, 250*time.Millisecond)

		// Two callers coalescing: short deadline exits early while background caller completes.
		var wg sync.WaitGroup
		var bgMeta *catalog.TableMeta
		var bgErr error

		wg.Go(func() {
			bgMeta, bgErr = rest.FetchTable(context.Background(), ref)
		})

		time.Sleep(15 * time.Millisecond)

		ctxShort, cancelShort := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancelShort()

		shortStart := time.Now()
		_, shortErr := rest.FetchTable(ctxShort, ref)
		assert.Less(t, time.Since(shortStart), 250*time.Millisecond)
		require.ErrorIs(t, shortErr, context.DeadlineExceeded)

		close(gate)
		wg.Wait()
		require.NoError(t, bgErr)
		require.NotNil(t, bgMeta)
		assert.Equal(t, int32(2), hits.Load())
	})

	t.Run("ListDatasets cancels mid-pagination without waiting for client timeout", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		var pages atomic.Int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pages.Add(1)
			if r.URL.Query().Get("pageToken") == "" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"datasets":[{"datasetReference":{"datasetId":"ds1"}}],"nextPageToken":"p2"}`))
				cancel()

				return
			}

			<-r.Context().Done()
		}))
		defer srv.Close()

		rest := catalog.NewRestClient(
			catalog.WithBaseURL(srv.URL),
			catalog.WithHTTPClient(srv.Client()),
			catalog.WithTimeout(1*time.Second),
			catalog.WithTokenProvider(func(context.Context) (string, error) { return "", nil }),
		)

		start := time.Now()
		_, err := rest.ListDatasets(ctx, "p1")
		elapsed := time.Since(start)
		require.ErrorIs(t, err, context.Canceled)
		assert.Less(t, elapsed, 250*time.Millisecond)
		assert.Equal(t, int32(1), pages.Load())
	})

	t.Run("Store.Close returns promptly while FetchTable HTTP call is stalled", func(t *testing.T) {
		t.Parallel()

		var enteredOnce sync.Once
		entered := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			enteredOnce.Do(func() { close(entered) })
			<-r.Context().Done()
		}))
		defer srv.Close()

		opts := bqconfig.DefaultLSPOptions()
		opts.CacheDir = ""
		opts.BigQueryAPIEnabled = true

		store, err := catalog.NewStore(
			opts,
			catalog.WithRestClient(catalog.NewRestClient(
				catalog.WithBaseURL(srv.URL),
				catalog.WithHTTPClient(srv.Client()),
				catalog.WithTimeout(2*time.Second),
				catalog.WithTokenProvider(func(context.Context) (string, error) { return "", nil }),
			)),
		)
		require.NoError(t, err)

		_, ok := store.GetTable(catalog.TableRef{Project: "p1", Dataset: "d1", Table: "stalled"})
		assert.False(t, ok)

		<-entered
		start := time.Now()
		require.NoError(t, store.Close())
		assert.Less(t, time.Since(start), 300*time.Millisecond)
	})
}

func TestADCContextLifetimeAndCombinedError(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)

	privPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: pkcs8,
	}))

	var tokenHits atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := tokenHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// expires_in=1 triggers immediate refresh on next Token() due to oauth2 expiryDelta.
			_, _ = w.Write([]byte(`{"access_token":"tok-1","token_type":"Bearer","expires_in":1}`))

			return
		}

		_, _ = w.Write([]byte(`{"access_token":"tok-2","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenSrv.Close()

	var seenAuth []string
	var authMu sync.Mutex
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authMu.Lock()
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		authMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema":{"fields":[{"name":"id","type":"INT64"}]}}`))
	}))
	defer apiSrv.Close()

	saJSON, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"project_id":   "test-proj",
		"private_key":  privPEM,
		"client_email": "test@test-proj.iam.gserviceaccount.com",
		"token_uri":    tokenSrv.URL + "/token",
	})
	require.NoError(t, err)

	credPath := filepath.Join(t.TempDir(), "sa.json")
	require.NoError(t, os.WriteFile(credPath, saJSON, 0o600))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credPath)

	client := catalog.NewRestClient(
		catalog.WithBaseURL(apiSrv.URL),
		catalog.WithHTTPClient(apiSrv.Client()),
		catalog.WithTimeout(2*time.Second),
	)

	// Request 1 initializes ADC using a short-lived context that is canceled immediately after.
	ctx1, cancel1 := context.WithTimeout(context.Background(), 2*time.Second)
	_, err = client.FetchTable(ctx1, catalog.TableRef{Project: "p1", Dataset: "d1", Table: "t1"})
	cancel1()
	require.NoError(t, err)

	// Request 2 triggers ADC token refresh; must succeed even though ctx1 is canceled.
	_, err = client.FetchTable(context.Background(), catalog.TableRef{Project: "p1", Dataset: "d1", Table: "t2"})
	require.NoError(t, err)
	assert.Equal(t, int32(2), tokenHits.Load())
	assert.Equal(t, []string{"Bearer tok-1", "Bearer tok-2"}, seenAuth)

	// Combined ADC + gcloud failure must preserve both error details.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("PATH", t.TempDir())

	failClient := catalog.NewRestClient(
		catalog.WithBaseURL(apiSrv.URL),
		catalog.WithHTTPClient(apiSrv.Client()),
	)
	_, err = failClient.FetchTable(context.Background(), catalog.TableRef{Project: "p1", Dataset: "d1", Table: "t3"})
	require.ErrorIs(t, err, catalog.ErrUnauthorized)
	assert.Contains(t, err.Error(), "adc")
	assert.Contains(t, err.Error(), "gcloud")
}

func TestFormatterBuiltinFunctionsHelperAndPrintOptionsUnchanged(t *testing.T) {
	t.Parallel()

	// Ensure PrintOptions keeps its exact 28 fields.
	assert.Equal(t, 28, reflect.TypeFor[formatter.PrintOptions]().NumField())

	fns := formatter.BigQueryBuiltinFunctions()
	require.NotEmpty(t, fns)
	assert.True(t, slices.IsSorted(fns))
	assert.Contains(t, fns, "AI.GENERATE")
	assert.Contains(t, fns, "ML.PREDICT")
	assert.Contains(t, fns, "HLL_COUNT.INIT")
	assert.Contains(t, fns, "NET.HOST")
	assert.Contains(t, fns, "KEYS.NEW_KEYSET")
	assert.Contains(t, fns, "AEAD.ENCRYPT")

	// Ensure returned slice is a defensive copy.
	fns[0] = "MUTATED"
	fns2 := formatter.BigQueryBuiltinFunctions()
	assert.NotEqual(t, "MUTATED", fns2[0])

	// Smoke check bqlang.DefaultParserOptions still works.
	po := bqlang.DefaultParserOptions()
	require.NotNil(t, po)
}

func walkResolvedScans(node googlesql.ResolvedNode, fn func(*googlesql.ResolvedTableScan)) error {
	if node == nil || reflect.ValueOf(node).IsNil() {
		return nil
	}

	if scan, ok := node.(*googlesql.ResolvedTableScan); ok {
		fn(scan)
	}

	children, err := node.GetChildNodes()
	if err != nil {
		return err
	}

	for _, child := range children {
		if err := walkResolvedScans(child, fn); err != nil {
			return err
		}
	}

	return nil
}

var (
	_ error = (*json.SyntaxError)(nil)
	_       = errors.Is
)

func TestScriptOverlayStructArrayRangeScalarTypesAndRangePartition(t *testing.T) {
	t.Parallel()

	tf, err := googlesql.NewTypeFactory()
	require.NoError(t, err)

	intType, err := tf.GetInt64()
	require.NoError(t, err)
	strType, err := tf.GetString()
	require.NoError(t, err)
	dateType, err := tf.GetDate()
	require.NoError(t, err)

	structType, err := tf.MakeStructType2([]*googlesql.StructField{
		{Name: "id", Type_: intType},
		{Name: "tag", Type_: strType},
	})
	require.NoError(t, err)
	arrStructType, err := tf.MakeArrayType2(structType)
	require.NoError(t, err)
	rangeDateType, err := tf.MakeRangeType3(dateType)
	require.NoError(t, err)

	overlay, err := catalog.NewScriptOverlay(tf)
	require.NoError(t, err)
	require.NoError(t, overlay.AddTempTable(catalog.TableRef{Table: "rich_tmp"}, []catalog.OverlayColumn{
		{Name: "s", Type: structType},
		{Name: "arr_s", Type: arrStructType},
		{Name: "dr", Type: rangeDateType},
	}))

	opts := bqconfig.DefaultLSPOptions()
	opts.CacheDir = ""
	opts.DefaultProject = "p"
	opts.DefaultDataset = "d"
	store, err := catalog.NewStore(opts)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	// Verify TableRef helpers and RangePartitioning metadata methods.
	dsOnly := catalog.TableRef{Project: "p", Dataset: "d"}
	assert.Equal(t, "p.d", dsOnly.FullName())
	assert.Equal(t, "p.d.all_types", catalog.TableRef{Project: "P", Dataset: "D", Table: "ALL_TYPES"}.NormalizedKey())

	var nilMeta *catalog.TableMeta
	assert.Nil(t, nilMeta.EffectivePartitioning())
	assert.False(t, nilMeta.IsPartitionColumn("bucket_id"))

	rawMeta := &catalog.TableMeta{
		Ref:               catalog.TableRef{Project: "p", Dataset: "d", Table: "all_types"},
		RangePartitioning: &catalog.RangePartitioning{Field: "bucket_id", Range: &catalog.IntRange{Start: 0, End: 100, Interval: 10}},
		Schema: []catalog.ColumnMeta{
			{Name: "bucket_id", Type: "INT64"},
			{Name: "b", Type: "BYTES"},
			{Name: "n", Type: "NUMERIC", Precision: 10},
			{Name: "bn", Type: "BIGNUMERIC"},
			{Name: "ok", Type: "BOOL"},
			{Name: "tm", Type: "TIME"},
			{Name: "dt", Type: "DATETIME"},
			{Name: "ts", Type: "TIMESTAMP", TimestampPrecision: 12},
			{Name: "geo", Type: "GEOGRAPHY"},
			{Name: "js", Type: "JSON"},
			{Name: "iv", Type: "INTERVAL"},
			{Name: "u", Type: "UUID"},
			{Name: "unk", Type: "CUSTOM_UNKNOWN"},
		},
	}
	eff := rawMeta.EffectivePartitioning()
	require.NotNil(t, eff)
	assert.Equal(t, catalog.PartitionKindRange, eff.Kind)
	assert.Equal(t, "bucket_id", eff.Field)
	assert.True(t, rawMeta.IsPartitionColumn("BUCKET_ID"))
	assert.False(t, rawMeta.IsPartitionColumn("b"))

	partOnlyMeta := &catalog.TableMeta{
		Ref:          catalog.TableRef{Project: "p", Dataset: "d", Table: "part_only"},
		Partitioning: &catalog.PartitionInfo{Kind: catalog.PartitionKindRange, Field: "part_num", Range: &catalog.IntRange{Start: 1, End: 10, Interval: 1}},
	}
	assert.Same(t, partOnlyMeta.Partitioning, partOnlyMeta.EffectivePartitioning())
	assert.True(t, partOnlyMeta.IsPartitionColumn("PART_NUM"))

	store.PutTable(rawMeta)
	storedMeta, ok := store.GetTable(catalog.TableRef{Project: "p", Dataset: "d", Table: "all_types"})
	require.True(t, ok)
	require.NotNil(t, storedMeta.EffectivePartitioning())
	assert.Equal(t, catalog.PartitionKindRange, storedMeta.EffectivePartitioning().Kind)
	assert.True(t, storedMeta.IsPartitionColumn("bucket_id"))

	cloned := storedMeta.Clone()
	require.NotNil(t, cloned.Partitioning)
	require.NotNil(t, cloned.RangePartitioning)
	require.NotNil(t, cloned.RangePartitioning.Range)
	assert.Equal(t, int64(100), cloned.RangePartitioning.Range.End.Int64())

	langOpts := catalog.DefaultLSPLanguageOptions()
	cat, err := catalog.NewCatalogForAnalysis(store, langOpts, overlay)
	require.NoError(t, err)

	parserOpts, err := googlesql.NewParserOptions()
	require.NoError(t, err)
	require.NoError(t, parserOpts.SetLanguageOptions(langOpts))
	analyzerOpts, err := googlesql.NewAnalyzerOptions(langOpts)
	require.NoError(t, err)

	for _, query := range []string{
		"SELECT s.id, s.tag, arr_s[OFFSET(0)].tag AS first_tag, dr FROM rich_tmp",
		"SELECT bucket_id, b, n, bn, ok, tm, dt, ts, geo, js, iv, u, unk || '' AS unk_str FROM p.d.all_types",
	} {
		po, pErr := googlesql.ParseStatement(query, parserOpts)
		require.NoError(t, pErr, "query=%s", query)
		stmt, sErr := po.Statement()
		require.NoError(t, sErr, "query=%s", query)
		out, aErr := googlesql.AnalyzeStatementFromParserAST(
			stmt,
			analyzerOpts,
			query,
			cat.GoogleSQLCatalog(),
			cat.TypeFactory(),
		)
		require.NoError(t, aErr, "query=%s", query)

		res, rErr := out.ResolvedStatement()
		require.NoError(t, rErr)

		if strings.Contains(query, "rich_tmp") {
			var richMeta *catalog.TableMeta
			require.NoError(t, walkResolvedScans(res, func(scan *googlesql.ResolvedTableScan) {
				tbl, tblErr := scan.Table()
				require.NoError(t, tblErr)
				if meta, found := cat.LookupTable(tbl); found {
					richMeta = meta
				}
			}))
			require.NotNil(t, richMeta)

			sCol, sFound := richMeta.FindColumn("s")
			require.True(t, sFound)
			assert.Equal(t, "STRUCT", sCol.Type)
			require.Len(t, sCol.Fields, 2)
			assert.Equal(t, "id", sCol.Fields[0].Name)
			assert.Equal(t, "INT64", sCol.Fields[0].Type)
			assert.Equal(t, "tag", sCol.Fields[1].Name)
			assert.Equal(t, "STRING", sCol.Fields[1].Type)

			arrCol, arrFound := richMeta.FindColumn("arr_s")
			require.True(t, arrFound)
			assert.Equal(t, "STRUCT", arrCol.Type)
			assert.Equal(t, catalog.ModeRepeated, arrCol.Mode)
			require.Len(t, arrCol.Fields, 2)

			drCol, drFound := richMeta.FindColumn("dr")
			require.True(t, drFound)
			assert.Equal(t, "RANGE", drCol.Type)
			require.NotNil(t, drCol.RangeElementType)
			assert.Equal(t, "DATE", drCol.RangeElementType.Type)
		}
	}
}

func TestRestClientGcloudFallbackPaginationAndHTTPErrors(t *testing.T) {
	binDir := t.TempDir()
	countFile := filepath.Join(binDir, "hits")
	gcloudScript := filepath.Join(binDir, "gcloud")
	scriptBody := "#!/bin/sh\necho x >> " + countFile + "\necho gcloud-tok-1\n"
	require.NoError(t, os.WriteFile(gcloudScript, []byte(scriptBody), 0o755))

	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("PATH", binDir)

	var seenAuth []string
	var authMu sync.Mutex
	longBody := strings.Repeat("Z", 320)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authMu.Lock()
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		authMu.Unlock()

		switch r.URL.Path {
		case "/projects/p1/datasets/d1/tables/t1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"tableReference":{"projectId":"p1","datasetId":"d1","tableId":"t1"},"schema":{"fields":[{"name":"id","type":"INT64"}]}}`)
		case "/projects/p1/datasets/d1/tables/t2":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"tableReference":{"projectId":"p1","datasetId":"d1","tableId":"t2"},"schema":{"fields":[{"name":"id","type":"INT64"}]}}`)
		case "/projects/p1/datasets/d1/tables/unauth":
			w.WriteHeader(http.StatusUnauthorized)
		case "/projects/p1/datasets/d1/tables/forbid":
			w.WriteHeader(http.StatusForbidden)
		case "/projects/p1/datasets/d1/tables/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/projects/p1/datasets/d1/tables/err500":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, longBody)
		case "/projects/p1/datasets/d1/tables":
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("pageToken") == "" {
				_, _ = io.WriteString(w, `{"tables":[{"tableReference":{"tableId":"alpha"},"type":"TABLE"},{"tableReference":{"tableId":"beta"},"type":"VIEW"}],"nextPageToken":"page-2"}`)
				return
			}
			assert.Equal(t, "page-2", r.URL.Query().Get("pageToken"))
			_, _ = io.WriteString(w, `{"tables":[{"tableReference":{"projectId":"p1","datasetId":"d1","tableId":"gamma"},"type":"TABLE"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithHTTPClient(srv.Client()),
	)

	// Two FetchTable calls should reuse cached gcloud token (hits == 1).
	m1, err := client.FetchTable(t.Context(), catalog.TableRef{Project: "p1", Dataset: "d1", Table: "t1"})
	require.NoError(t, err)
	assert.Equal(t, "t1", m1.Ref.Table)

	m2, err := client.FetchTable(t.Context(), catalog.TableRef{Project: "p1", Dataset: "d1", Table: "t2"})
	require.NoError(t, err)
	assert.Equal(t, "t2", m2.Ref.Table)

	hitsData, err := os.ReadFile(countFile)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(hitsData), "x"))
	assert.Equal(t, []string{"Bearer gcloud-tok-1", "Bearer gcloud-tok-1"}, seenAuth)

	// Multi-page ListTables pagination.
	tables, err := client.ListTables(t.Context(), "p1", "d1")
	require.NoError(t, err)
	require.Len(t, tables, 3)
	assert.Equal(t, "p1.d1.alpha", tables[0].Ref.FullName())
	assert.Equal(t, "p1.d1.beta", tables[1].Ref.FullName())
	assert.Equal(t, "p1.d1.gamma", tables[2].Ref.FullName())

	// Empty project/dataset validation in ListTables.
	_, err = client.ListTables(t.Context(), "", "d1")
	require.ErrorIs(t, err, catalog.ErrInvalidTableRef)

	// HTTP status mapping (401, 403, 404, 500 with >256-byte snippet truncation).
	_, err = client.FetchTable(t.Context(), catalog.TableRef{Project: "p1", Dataset: "d1", Table: "unauth"})
	require.ErrorIs(t, err, catalog.ErrUnauthorized)

	_, err = client.FetchTable(t.Context(), catalog.TableRef{Project: "p1", Dataset: "d1", Table: "forbid"})
	require.ErrorIs(t, err, catalog.ErrUnauthorized)

	_, err = client.FetchTable(t.Context(), catalog.TableRef{Project: "p1", Dataset: "d1", Table: "missing"})
	require.ErrorIs(t, err, catalog.ErrTableNotFound)

	_, err = client.FetchTable(t.Context(), catalog.TableRef{Project: "p1", Dataset: "d1", Table: "err500"})
	require.ErrorIs(t, err, catalog.ErrAPIError)
	assert.Contains(t, err.Error(), strings.Repeat("Z", 256))
	assert.NotContains(t, err.Error(), strings.Repeat("Z", 257))

	// Empty gcloud output must fail with ErrUnauthorized.
	emptyBin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(emptyBin, "gcloud"), []byte("#!/bin/sh\necho ''\n"), 0o755))
	t.Setenv("PATH", emptyBin)
	emptyClient := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithHTTPClient(srv.Client()),
	)
	_, err = emptyClient.FetchTable(t.Context(), catalog.TableRef{Project: "p1", Dataset: "d1", Table: "t1"})
	require.ErrorIs(t, err, catalog.ErrUnauthorized)
	assert.Contains(t, err.Error(), "empty access token")
}

func TestSQLiteDiskCacheConcurrentMultiStore(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/datasets"):
			_, _ = w.Write([]byte(`{"datasets":[{"datasetReference":{"projectId":"proj","datasetId":"ds0"}},{"datasetReference":{"projectId":"proj","datasetId":"ds1"}}]}`))
		case strings.HasSuffix(r.URL.Path, "/tables"):
			_, _ = w.Write([]byte(`{"tables":[{"tableReference":{"projectId":"proj","datasetId":"ds0","tableId":"tbl_0"},"type":"TABLE"}]}`))
		default:
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
			if len(parts) == 6 {
				_, _ = fmt.Fprintf(
					w,
					`{"tableReference":{"projectId":%q,"datasetId":%q,"tableId":%q},"type":"TABLE","schema":{"fields":[{"name":"id","type":"INT64"}]}}`,
					parts[1], parts[3], parts[5],
				)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// Use a directory containing spaces and '#' to verify SQLite URI escaping.
	cacheDir := filepath.Join(t.TempDir(), "bqpls cache #1")

	const numStores = 6
	const tablesPerStore = 12

	errCh := make(chan error, numStores*(tablesPerStore+2))
	var wg sync.WaitGroup
	for storeIdx := range numStores {
		wg.Go(func() {
			opts := bqconfig.DefaultLSPOptions()
			opts.CacheDir = cacheDir
			opts.CacheTTLSeconds = 3600
			opts.BigQueryAPIEnabled = true

			rest := catalog.NewRestClient(
				catalog.WithBaseURL(srv.URL),
				catalog.WithHTTPClient(srv.Client()),
				catalog.WithTokenProvider(func(context.Context) (string, error) { return "", nil }),
			)

			store, err := catalog.NewStore(opts, catalog.WithRestClient(rest))
			if err != nil {
				errCh <- fmt.Errorf("store %d NewStore: %w", storeIdx, err)
				return
			}
			defer func() {
				if closeErr := store.Close(); closeErr != nil {
					errCh <- fmt.Errorf("store %d Close: %w", storeIdx, closeErr)
				}
			}()

			for i := range tablesPerStore {
				ds := fmt.Sprintf("ds%d", i%2)
				tbl := fmt.Sprintf("tbl_%d_%d", storeIdx, i)
				ref := catalog.TableRef{Project: "proj", Dataset: ds, Table: tbl}

				meta, ok := store.WaitForTable(t.Context(), ref, 5*time.Second)
				if !ok || meta == nil || meta.Ref.FullName() != ref.FullName() {
					errCh <- fmt.Errorf("store %d failed to resolve %s (ok=%v)", storeIdx, ref.FullName(), ok)
					continue
				}

				if i%3 == 0 {
					_ = store.ListProjects()
					_, _ = store.DiscoverDatasets(t.Context(), "proj", 2*time.Second)
					_, _ = store.DiscoverTables(t.Context(), "proj", ds, 2*time.Second)
				}
			}
		})
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}

	// Verify an offline Store sees all projects, datasets, tables, and cached lists from cache.db.
	offlineOpts := bqconfig.DefaultLSPOptions()
	offlineOpts.CacheDir = cacheDir
	offlineOpts.CacheTTLSeconds = 3600
	offlineOpts.BigQueryAPIEnabled = false

	offline, err := catalog.NewStore(offlineOpts)
	require.NoError(t, err)
	defer func() { require.NoError(t, offline.Close()) }()

	assert.Contains(t, offline.ListProjects(), "proj")

	datasets, incomplete := offline.DiscoverDatasets(t.Context(), "proj", 0)
	assert.False(t, incomplete)
	assert.Equal(t, []string{"ds0", "ds1"}, datasets)

	for storeIdx := range numStores {
		for i := range tablesPerStore {
			ref := catalog.TableRef{
				Project: "proj",
				Dataset: fmt.Sprintf("ds%d", i%2),
				Table:   fmt.Sprintf("tbl_%d_%d", storeIdx, i),
			}
			meta, ok := offline.GetTable(ref)
			require.True(t, ok, "missing persisted table %s", ref.FullName())
			assert.Equal(t, ref.FullName(), meta.Ref.FullName())
		}
	}

	summaries, incomplete := offline.DiscoverTables(t.Context(), "proj", "ds0", 0)
	assert.False(t, incomplete)
	assert.GreaterOrEqual(t, len(summaries), numStores*(tablesPerStore/2))
}
