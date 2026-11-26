package catalog_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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

func TestStoreSubscribeAndUnsubscribe(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects/p/datasets/d/tables/t1":
			_, _ = w.Write([]byte(`{"tableReference":{"projectId":"p","datasetId":"d","tableId":"t1"},"schema":{"fields":[{"name":"id","type":"INT64"}]}}`))
		case "/projects/p/datasets/d/tables/t2":
			_, _ = w.Write([]byte(`{"tableReference":{"projectId":"p","datasetId":"d","tableId":"t2"},"schema":{"fields":[{"name":"id","type":"INT64"}]}}`))
		case "/projects/p/datasets/d/tables/t3":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	opts := bqconfig.DefaultLSPOptions()
	opts.BigQueryAPIEnabled = true
	opts.CacheDir = t.TempDir()

	rest := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithTokenProvider(func(context.Context) (string, error) { return "tok", nil }),
	)

	legacyCh := make(chan catalog.TableRef, 8)
	store, err := catalog.NewStore(
		opts,
		catalog.WithRestClient(rest),
		catalog.WithOnUpdate(func(ref catalog.TableRef) {
			legacyCh <- ref
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	sub1Ch := make(chan catalog.TableRef, 8)
	unsub1 := store.Subscribe(func(ref catalog.TableRef) {
		// Verify store mutex is released (calling GetTable/IsPending does not deadlock).
		_ = store.IsPending(ref)
		sub1Ch <- ref
	})

	sub2Ch := make(chan catalog.TableRef, 8)
	unsub2 := store.Subscribe(func(ref catalog.TableRef) {
		sub2Ch <- ref
	})
	defer unsub2()

	// Nil subscriber returns a safe no-op unsubscribe.
	noop := store.Subscribe(nil)
	noop()

	// First fetch notifies legacy OnUpdate, sub1, and sub2.
	ref1 := catalog.TableRef{Project: "p", Dataset: "d", Table: "t1"}
	_, ok := store.GetTable(ref1)
	assert.False(t, ok)

	assertRecvRef(t, legacyCh, ref1)
	assertRecvRef(t, sub1Ch, ref1)
	assertRecvRef(t, sub2Ch, ref1)

	// Unsubscribe sub1 (idempotent double call).
	unsub1()
	unsub1()

	ref2 := catalog.TableRef{Project: "p", Dataset: "d", Table: "t2"}
	_, ok = store.GetTable(ref2)
	assert.False(t, ok)

	assertRecvRef(t, legacyCh, ref2)
	assertRecvRef(t, sub2Ch, ref2)
	select {
	case unexpected := <-sub1Ch:
		t.Fatalf("unsubscribed listener received %v", unexpected)
	default:
	}

	// Clearing legacy OnUpdate(nil) must not affect Subscribe listeners; 404 still fires sub2.
	store.OnUpdate(nil)
	ref3 := catalog.TableRef{Project: "p", Dataset: "d", Table: "t3"}
	_, ok = store.GetTable(ref3)
	assert.False(t, ok)

	assertRecvRef(t, sub2Ch, ref3)
	select {
	case unexpected := <-legacyCh:
		t.Fatalf("cleared legacy OnUpdate received %v", unexpected)
	default:
	}
}

func TestStoreSubscribeDryRun(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/jobs") {
			_, _ = w.Write([]byte(`{"statistics":{"totalBytesProcessed":"1024","query":{"totalBytesBilled":"10485760","statementType":"SELECT"}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	opts := bqconfig.DefaultLSPOptions()
	opts.BigQueryAPIEnabled = true
	opts.DefaultProject = "p"
	opts.CacheDir = t.TempDir()

	rest := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithTokenProvider(func(context.Context) (string, error) { return "tok", nil }),
	)

	store, err := catalog.NewStore(opts, catalog.WithRestClient(rest))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	var c1, c2 atomic.Int32
	done := make(chan struct{}, 4)

	unsub1 := store.SubscribeDryRun(func() {
		c1.Add(1)
		done <- struct{}{}
	})
	unsub2 := store.SubscribeDryRun(func() {
		c2.Add(1)
		done <- struct{}{}
	})
	defer unsub2()

	require.True(t, store.RequestDryRun("p", "d", "SELECT 1", true))
	waitSignal(t, done)
	waitSignal(t, done)
	assert.Equal(t, int32(1), c1.Load())
	assert.Equal(t, int32(1), c2.Load())

	unsub1()
	unsub1()

	require.True(t, store.RequestDryRun("p", "d", "SELECT 2", true))
	waitSignal(t, done)
	assert.Equal(t, int32(1), c1.Load())
	assert.Equal(t, int32(2), c2.Load())
}

func TestStoreConcurrentSubscribeAndDiscovery(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects/p/datasets":
			_, _ = w.Write([]byte(`{"datasets":[{"datasetReference":{"projectId":"p","datasetId":"d"}}]}`))
		case "/projects/p/datasets/d/tables":
			_, _ = w.Write([]byte(`{"tables":[{"tableReference":{"projectId":"p","datasetId":"d","tableId":"orders"},"type":"TABLE"}]}`))
		case "/projects/p/datasets/d/tables/orders":
			_, _ = w.Write([]byte(`{"tableReference":{"projectId":"p","datasetId":"d","tableId":"orders"},"schema":{"fields":[{"name":"id","type":"INT64"}]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	opts := bqconfig.DefaultLSPOptions()
	opts.BigQueryAPIEnabled = true
	opts.DefaultProject = "p"
	opts.DefaultDataset = "d"
	opts.CacheDir = t.TempDir()

	rest := catalog.NewRestClient(
		catalog.WithBaseURL(srv.URL),
		catalog.WithTokenProvider(func(context.Context) (string, error) { return "tok", nil }),
	)

	store, err := catalog.NewStore(opts, catalog.WithRestClient(rest))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			unsub := store.Subscribe(func(catalog.TableRef) {})
			defer unsub()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			_, _ = store.DiscoverDatasets(ctx, "p", time.Second)
			_, _ = store.DiscoverTables(ctx, "p", "d", time.Second)
			_, _ = store.GetTable(catalog.TableRef{Project: "p", Dataset: "d", Table: "orders"})
			meta, ok := store.WaitForTable(ctx, catalog.TableRef{Project: "p", Dataset: "d", Table: "orders"}, time.Second)
			if ok {
				assert.Equal(t, "orders", meta.Ref.Table)
			}
		})
	}
	wg.Wait()
}

func TestNewDefaultTokenProviderWithFakeADCScopes(t *testing.T) {
	var (
		mu             sync.Mutex
		requestedScope []string
		hits           atomic.Int32
	)

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		assertion := r.FormValue("assertion")
		parts := strings.Split(assertion, ".")
		if len(parts) != 3 {
			http.Error(w, "invalid jwt assertion", http.StatusBadRequest)
			return
		}

		claimBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var claims struct {
			Scope string `json:"scope"`
		}
		if err := json.Unmarshal(claimBytes, &claims); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		mu.Lock()
		requestedScope = append(requestedScope, claims.Scope)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"adc-scoped-token","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(tokenSrv.Close)

	credPath := writeFakeServiceAccountADC(t, tokenSrv.URL, "sa@proj.iam.gserviceaccount.com")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credPath)

	// Custom scopes (BigQuery + Cloud Platform).
	tp := catalog.NewDefaultTokenProvider(
		"https://www.googleapis.com/auth/bigquery",
		"https://www.googleapis.com/auth/cloud-platform",
	)

	tok1, err := tp(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "adc-scoped-token", tok1)

	// Second call reuses cached token without hitting the token endpoint again.
	tok2, err := tp(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "adc-scoped-token", tok2)
	assert.Equal(t, int32(1), hits.Load())

	// Default scope when none provided.
	defaultTP := catalog.NewDefaultTokenProvider()
	tok3, err := defaultTP(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "adc-scoped-token", tok3)

	mu.Lock()
	gotScopes := append([]string(nil), requestedScope...)
	mu.Unlock()

	require.Len(t, gotScopes, 2)
	assert.Equal(t, "https://www.googleapis.com/auth/bigquery https://www.googleapis.com/auth/cloud-platform", gotScopes[0])
	assert.Equal(t, "https://www.googleapis.com/auth/bigquery.readonly", gotScopes[1])
}

func TestNewDefaultTokenProviderGcloudFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake gcloud test runs on unix")
	}

	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing-adc.json"))

	binDir := t.TempDir()
	counterFile := filepath.Join(binDir, "calls")
	fakeGcloud := filepath.Join(binDir, "gcloud")
	script := "#!/bin/sh\necho x >> " + counterFile + "\necho fake-gcloud-token\n"
	require.NoError(t, os.WriteFile(fakeGcloud, []byte(script), 0o755))
	t.Setenv("PATH", binDir)

	tp := catalog.NewDefaultTokenProvider("https://www.googleapis.com/auth/bigquery")
	tok1, err := tp(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "fake-gcloud-token", tok1)

	// Second call uses cached gcloud token.
	tok2, err := tp(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "fake-gcloud-token", tok2)

	callsRaw, err := os.ReadFile(counterFile)
	require.NoError(t, err)
	assert.Len(t, strings.Split(strings.TrimSpace(string(callsRaw)), "\n"), 1)
}

func TestDescribeCredentials(t *testing.T) {
	t.Run("service_account ADC", func(t *testing.T) {
		credPath := writeFakeServiceAccountADC(t, "https://oauth2.googleapis.com/token", "sa@proj.iam.gserviceaccount.com")
		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credPath)

		desc, err := catalog.DescribeCredentials(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "service_account sa@proj.iam.gserviceaccount.com (ADC)", desc)
	})

	t.Run("authorized_user ADC", func(t *testing.T) {
		credPath := filepath.Join(t.TempDir(), "adc.json")
		payload := `{"type":"authorized_user","client_id":"cid","client_secret":"csec","refresh_token":"rtok"}`
		require.NoError(t, os.WriteFile(credPath, []byte(payload), 0o600))
		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credPath)

		desc, err := catalog.DescribeCredentials(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "authorized_user (ADC)", desc)
	})

	t.Run("gcloud fallback", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("shell script fake gcloud test runs on unix")
		}

		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing.json"))
		binDir := t.TempDir()
		fakeGcloud := filepath.Join(binDir, "gcloud")
		script := "#!/bin/sh\necho dev@example.com\n"
		require.NoError(t, os.WriteFile(fakeGcloud, []byte(script), 0o755))
		t.Setenv("PATH", binDir)

		desc, err := catalog.DescribeCredentials(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "gcloud (dev@example.com)", desc)
	})

	t.Run("unknown when no ADC and no active gcloud account", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("shell script fake gcloud test runs on unix")
		}

		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing.json"))
		binDir := t.TempDir()
		fakeGcloud := filepath.Join(binDir, "gcloud")
		script := "#!/bin/sh\nexit 0\n"
		require.NoError(t, os.WriteFile(fakeGcloud, []byte(script), 0o755))
		t.Setenv("PATH", binDir)

		desc, err := catalog.DescribeCredentials(context.Background())
		require.NoError(t, err)
		assert.Empty(t, desc)
	})
}

func writeFakeServiceAccountADC(t *testing.T, tokenURI, clientEmail string) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)

	privPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	cred := map[string]string{
		"type":         "service_account",
		"project_id":   "proj",
		"private_key":  privPEM,
		"client_email": clientEmail,
		"token_uri":    tokenURI,
	}
	raw, err := json.Marshal(cred)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "sa.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	return path
}

func assertRecvRef(t *testing.T, ch <-chan catalog.TableRef, want catalog.TableRef) {
	t.Helper()

	select {
	case got := <-ch:
		assert.Equal(t, want, got)
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for update %v", want)
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for dry-run signal")
	}
}
