package catalog_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/bqconfig"
	"github.com/paulourio/bqlang/catalog"
)

func TestRestClientDryRunQuery(t *testing.T) {
	t.Parallel()

	var (
		gotPath string
		gotAuth string
		gotBody map[string]any
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		assert.Equal(t, http.MethodPost, r.Method)
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"statistics": {
				"totalBytesProcessed": "1073741824",
				"query": {
					"totalBytesProcessed": "1073741824",
					"totalBytesBilled": "1073741824",
					"totalBytesProcessedAccuracy": "PRECISE",
					"statementType": "SELECT",
					"cacheHit": false,
					"referencedTables": [
						{"projectId": "p1", "datasetId": "d1", "tableId": "t1"}
					]
				}
			}
		}`))
	}))
	t.Cleanup(srv.Close)

	client := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithTokenProvider(func(_ context.Context) (string, error) {
			return "test-tok", nil
		}),
	)

	res, err := client.DryRunQuery(t.Context(), "my-proj", "my-ds", "SELECT id FROM t1")
	require.NoError(t, err)
	assert.Equal(t, "/projects/my-proj/jobs", gotPath)
	assert.Equal(t, "Bearer test-tok", gotAuth)

	cfgMap, ok := gotBody["configuration"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, cfgMap["dryRun"])
	qMap, ok := cfgMap["query"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "SELECT id FROM t1", qMap["query"])
	assert.Equal(t, false, qMap["useLegacySql"])
	dsMap, ok := qMap["defaultDataset"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "my-proj", dsMap["projectId"])
	assert.Equal(t, "my-ds", dsMap["datasetId"])

	assert.Equal(t, int64(1<<30), res.TotalBytesProcessed)
	assert.Equal(t, int64(1<<30), res.TotalBytesBilled)
	assert.Equal(t, "PRECISE", res.Accuracy)
	assert.Equal(t, "SELECT", res.StatementType)
	assert.False(t, res.CacheHit)
	require.Len(t, res.ReferencedTables, 1)
	assert.Equal(t, "p1.d1.t1", res.ReferencedTables[0].FullName())
}

func TestRestClientDryRunQueryErrors(t *testing.T) {
	t.Parallel()

	var statusCode atomic.Int32
	statusCode.Store(http.StatusBadRequest)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(statusCode.Load()))
		_, _ = w.Write([]byte(`{"error": {"message": "invalid query"}}`))
	}))
	t.Cleanup(srv.Close)

	client := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithTokenProvider(func(_ context.Context) (string, error) { return "tok", nil }),
	)

	_, err := client.DryRunQuery(t.Context(), "p1", "", "SELECT bad")
	require.ErrorIs(t, err, catalog.ErrDryRunRejected)

	statusCode.Store(http.StatusForbidden)
	_, err = client.DryRunQuery(t.Context(), "p1", "", "SELECT forbidden")
	require.ErrorIs(t, err, catalog.ErrUnauthorized)

	_, err = client.DryRunQuery(t.Context(), "", "", "SELECT 1")
	require.ErrorIs(t, err, catalog.ErrInvalidTableRef)

	_, err = client.DryRunQuery(t.Context(), "p1", "", "   ")
	require.ErrorIs(t, err, catalog.ErrDryRunRejected)
}

func TestStoreAsyncDryRunCacheForceAndTTL(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		bytesVal := int64(n) * (1 << 30)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"statistics": map[string]any{
				"totalBytesProcessed": bytesVal,
				"query": map[string]any{
					"totalBytesProcessed":         bytesVal,
					"totalBytesBilled":            bytesVal,
					"totalBytesProcessedAccuracy": "PRECISE",
				},
			},
		})
	}))
	t.Cleanup(srv.Close)

	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	var clockMu atomic.Pointer[time.Time]
	clockMu.Store(&now)

	updatedCh := make(chan struct{}, 8)
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
		catalog.WithClock(func() time.Time { return *clockMu.Load() }),
		catalog.WithOnDryRunUpdate(func() {
			select {
			case updatedCh <- struct{}{}:
			default:
			}
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	sqlText := "SELECT id FROM proj.ds.events"
	assert.True(t, store.RequestDryRun("proj", "ds", sqlText, false))

	select {
	case <-updatedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for async dry-run update")
	}

	res, ok := store.GetDryRun("proj", "ds", sqlText)
	require.True(t, ok)
	assert.Equal(t, int64(1<<30), res.TotalBytesProcessed)
	assert.Equal(t, int32(1), calls.Load())

	// Second non-forced request hits cache without calling REST API.
	assert.False(t, store.RequestDryRun("proj", "ds", sqlText, false))
	assert.Equal(t, int32(1), calls.Load())

	// Forced request bypasses cache and updates result.
	assert.True(t, store.RequestDryRun("proj", "ds", sqlText, true))
	select {
	case <-updatedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for forced dry-run update")
	}

	res2, ok := store.GetDryRun("proj", "ds", sqlText)
	require.True(t, ok)
	assert.Equal(t, int64(2<<30), res2.TotalBytesProcessed)
	assert.Equal(t, int32(2), calls.Load())

	// Advance clock past 5m TTL: GetDryRun returns false and RequestDryRun re-enqueues.
	later := now.Add(6 * time.Minute)
	clockMu.Store(&later)

	_, ok = store.GetDryRun("proj", "ds", sqlText)
	assert.False(t, ok)
	assert.True(t, store.RequestDryRun("proj", "ds", sqlText, false))

	select {
	case <-updatedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for TTL-expired dry-run update")
	}
	assert.Equal(t, int32(3), calls.Load())
}

func TestStoreDryRunErrorCachedAndNotifies(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "Not found: Table proj:ds.missing"}}`))
	}))
	t.Cleanup(srv.Close)

	updatedCh := make(chan struct{}, 4)
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
		catalog.WithOnDryRunUpdate(func() {
			select {
			case updatedCh <- struct{}{}:
			default:
			}
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	sqlText := "SELECT id FROM proj.ds.missing"
	assert.True(t, store.RequestDryRun("proj", "", sqlText, false))

	select {
	case <-updatedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for dry-run error callback")
	}

	dryErr, ok := store.GetDryRunError("proj", "", sqlText)
	require.True(t, ok)
	assert.True(t, dryErr.Rejected)
	assert.False(t, dryErr.AuthError)
	assert.Equal(t, "Not found: Table proj:ds.missing", dryErr.Message)
}

func TestEffectiveBillingProjectResolutionOrder(t *testing.T) {
	gcloudDir := t.TempDir()
	cfgDir := filepath.Join(gcloudDir, "configurations")
	require.NoError(t, os.MkdirAll(cfgDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(gcloudDir, "active_config"), []byte("work\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config_work"), []byte("[core]\naccount = user@example.com\nproject = gcloud-work-proj\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(gcloudDir, "application_default_credentials.json"), []byte(`{"quota_project_id": "adc-quota-proj"}`), 0o600))

	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("GCLOUD_PROJECT", "")
	t.Setenv("CLOUDSDK_CORE_PROJECT", "")
	t.Setenv("CLOUDSDK_ACTIVE_CONFIG_NAME", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", gcloudDir)

	// 1. Active gcloud config project is discovered when DefaultProject & env vars are empty.
	opts := bqconfig.DefaultLSPOptions()
	opts.CacheDir = t.TempDir()
	store, err := catalog.NewStore(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	assert.Equal(t, "gcloud-work-proj", store.EffectiveBillingProject())

	// 2. ADC fallback when gcloud config has no project.
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config_work"), []byte("[core]\naccount = user@example.com\n"), 0o600))
	assert.Equal(t, "adc-quota-proj", store.EffectiveBillingProject())

	// 3. Env var overrides gcloud/ADC.
	t.Setenv("GOOGLE_CLOUD_PROJECT", "env-proj")
	assert.Equal(t, "env-proj", store.EffectiveBillingProject())

	// 4. Explicit DefaultProject overrides env var.
	optsExplicit := bqconfig.DefaultLSPOptions()
	optsExplicit.DefaultProject = "explicit-proj"
	optsExplicit.CacheDir = t.TempDir()
	storeExplicit, err := catalog.NewStore(optsExplicit)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storeExplicit.Close() })
	assert.Equal(t, "explicit-proj", storeExplicit.EffectiveBillingProject())
}

func TestResolveGcloudBinaryOverrideAndAutoDiscovery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir()) // stripped desktop PATH without gcloud
	t.Setenv("CLOUDSDK_ROOT_DIR", "")

	// 1. Explicit override with ~/ expansion always wins.
	assert.Equal(t, filepath.Join(home, "custom", "bin", "gcloud"), catalog.ResolveGcloudBinary("~/custom/bin/gcloud"))

	// 2. Auto-discovers ~/.gcloudsdk/bin/gcloud when PATH lacks gcloud.
	gcloudBin := filepath.Join(home, ".gcloudsdk", "bin", "gcloud")
	require.NoError(t, os.MkdirAll(filepath.Dir(gcloudBin), 0o750))
	require.NoError(t, os.WriteFile(gcloudBin, []byte("#!/bin/sh\n"), 0o700))
	assert.Equal(t, gcloudBin, catalog.ResolveGcloudBinary(""))

	// 3. CLOUDSDK_ROOT_DIR takes precedence over home fallbacks.
	sdkRoot := t.TempDir()
	sdkBin := filepath.Join(sdkRoot, "bin", "gcloud")
	require.NoError(t, os.MkdirAll(filepath.Dir(sdkBin), 0o750))
	require.NoError(t, os.WriteFile(sdkBin, []byte("#!/bin/sh\n"), 0o700))
	t.Setenv("CLOUDSDK_ROOT_DIR", sdkRoot)
	assert.Equal(t, sdkBin, catalog.ResolveGcloudBinary(""))
}
