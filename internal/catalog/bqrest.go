package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"golang.org/x/sync/singleflight"
)

const (
	defaultBigQueryBaseURL = "https://bigquery.googleapis.com/bigquery/v2/"
	bigqueryReadonlyScope  = "https://www.googleapis.com/auth/bigquery.readonly"
	bigqueryScope          = "https://www.googleapis.com/auth/bigquery"
	defaultHTTPTimeout     = 5 * time.Second
	gcloudCommandTimeout   = 15 * time.Second
	defaultMaxResponseSize = 16 << 20 // 16 MiB
	maxListPages           = 50
	gcloudTokenTTL         = 5 * time.Minute
)

// Sentinel errors returned by RestClient.
var (
	ErrTableNotFound   = errors.New("bigquery table not found")
	ErrUnauthorized    = errors.New("bigquery request unauthorized")
	ErrInvalidTableRef = errors.New("invalid bigquery table reference")
	ErrDryRunRejected  = errors.New("bigquery dry run rejected")
	ErrAPIError        = errors.New("bigquery api error")
)

// TokenProvider supplies OAuth2 bearer tokens for BigQuery REST requests.
type TokenProvider func(ctx context.Context) (string, error)

// DatasetRef identifies a BigQuery dataset.
type DatasetRef struct {
	Project string `json:"projectId,omitempty"`
	Dataset string `json:"datasetId,omitempty"`
}

// TableSummary holds lightweight metadata returned by tables.list.
type TableSummary struct {
	Ref          TableRef  `json:"tableReference"`
	Type         TableType `json:"type,omitempty"`
	FriendlyName string    `json:"friendlyName,omitempty"`
}

type datasetListResponse struct {
	Datasets      []datasetListEntry `json:"datasets,omitempty"`
	NextPageToken string             `json:"nextPageToken,omitempty"`
}

type datasetListEntry struct {
	Ref DatasetRef `json:"datasetReference"`
}

type tableListResponse struct {
	Tables        []TableSummary `json:"tables,omitempty"`
	NextPageToken string         `json:"nextPageToken,omitempty"`
}

// RestClientOption configures a RestClient.
type RestClientOption func(*RestClient)

// WithBaseURL overrides the BigQuery REST v2 base URL (useful for httptest).
func WithBaseURL(baseURL string) RestClientOption {
	return func(c *RestClient) {
		if baseURL != "" {
			c.baseURL = strings.TrimRight(baseURL, "/") + "/"
		}
	}
}

// WithHTTPClient sets a custom *http.Client on the RestClient.
func WithHTTPClient(client *http.Client) RestClientOption {
	return func(c *RestClient) {
		if client != nil {
			c.httpClient = client
		}
	}
}

// WithTimeout overrides the per-request HTTP timeout (default 5s).
func WithTimeout(d time.Duration) RestClientOption {
	return func(c *RestClient) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithTokenProvider overrides OAuth2 token acquisition.
func WithTokenProvider(provider TokenProvider) RestClientOption {
	return func(c *RestClient) {
		c.tokenProvider = provider
		c.dryRunTokenProvider = provider
		c.customAuth = true
	}
}

// WithGcloudPath overrides the path to the gcloud CLI executable used for
// authentication fallback when ADC is unavailable or unauthorized.
func WithGcloudPath(path string) RestClientOption {
	return func(c *RestClient) {
		c.defaultAuth.gcloudPath = strings.TrimSpace(path)
	}
}

type restFlight struct {
	cancel  context.CancelFunc
	doneCh  chan struct{}
	waiters int
	done    bool
	val     any
	err     error
}

// RestClient fetches BigQuery metadata via the BigQuery REST v2 HTTP API.
type RestClient struct {
	baseURL             string
	httpClient          *http.Client
	timeout             time.Duration
	tokenProvider       TokenProvider
	dryRunTokenProvider TokenProvider
	customAuth          bool
	sf                  singleflight.Group
	flightsMu           sync.Mutex
	flights             map[string]*restFlight
	defaultAuth         *defaultTokenCache
}

// NewRestClient constructs a BigQuery REST v2 client with a 5s timeout,
// ADC + gcloud auth fallback, and singleflight request deduplication.
func NewRestClient(opts ...RestClientOption) *RestClient {
	c := &RestClient{
		baseURL:     defaultBigQueryBaseURL,
		httpClient:  &http.Client{},
		timeout:     defaultHTTPTimeout,
		flights:     make(map[string]*restFlight),
		defaultAuth: &defaultTokenCache{},
	}
	c.tokenProvider = c.defaultAuth.Token
	c.dryRunTokenProvider = c.defaultAuth.DryRunToken

	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}

	c.defaultAuth.httpTimeout = c.timeout

	return c
}

// FetchTable retrieves canonical TableMeta for ref via tables.get.
func (c *RestClient) FetchTable(ctx context.Context, ref TableRef) (*TableMeta, error) {
	if !ref.IsComplete() {
		return nil, fmt.Errorf("%w: %q", ErrInvalidTableRef, ref.FullName())
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("FetchTable context: %w", err)
	}

	key := "table:" + ref.FullName()

	val, err := c.doFlight(ctx, key, func(flightCtx context.Context) (any, error) {
		endpoint := fmt.Sprintf(
			"%sprojects/%s/datasets/%s/tables/%s",
			c.baseURL,
			url.PathEscape(ref.Project),
			url.PathEscape(ref.Dataset),
			url.PathEscape(ref.Table),
		)

		body, err := c.doGET(flightCtx, endpoint)
		if err != nil {
			return nil, err
		}

		return DecodeTableJSON(body, ref)
	})
	if err != nil {
		return nil, err
	}

	meta, ok := val.(*TableMeta)
	if !ok {
		return nil, fmt.Errorf("%w: unexpected result type %T", ErrAPIError, val)
	}

	return meta.Clone(), nil
}

// ListDatasets retrieves all datasets in projectID via datasets.list.
func (c *RestClient) ListDatasets(ctx context.Context, projectID string) ([]DatasetRef, error) {
	if projectID == "" {
		return nil, fmt.Errorf("%w: empty project id", ErrInvalidTableRef)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("ListDatasets context: %w", err)
	}

	key := "datasets:" + projectID

	val, err := c.doFlight(ctx, key, func(flightCtx context.Context) (any, error) {
		return c.fetchAllDatasets(flightCtx, projectID)
	})
	if err != nil {
		return nil, err
	}

	datasets, ok := val.([]DatasetRef)
	if !ok {
		return nil, fmt.Errorf("%w: unexpected dataset list type %T", ErrAPIError, val)
	}

	return slices.Clone(datasets), nil
}

func (c *RestClient) fetchAllDatasets(ctx context.Context, projectID string) ([]DatasetRef, error) {
	var all []DatasetRef
	pageToken := ""

	for range maxListPages {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("list datasets context: %w", err)
		}

		reqCtx, cancel := context.WithTimeout(ctx, c.timeout)

		endpoint := fmt.Sprintf("%sprojects/%s/datasets?maxResults=1000", c.baseURL, url.PathEscape(projectID))
		if pageToken != "" {
			endpoint += "&pageToken=" + url.QueryEscape(pageToken)
		}

		body, err := c.doGET(reqCtx, endpoint)
		cancel()

		if err != nil {
			return nil, err
		}

		var page datasetListResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("%w: decode dataset list: %w", ErrAPIError, err)
		}

		for _, entry := range page.Datasets {
			ref := entry.Ref
			if ref.Project == "" {
				ref.Project = projectID
			}

			all = append(all, ref)
		}

		if page.NextPageToken == "" {
			break
		}

		pageToken = page.NextPageToken
	}

	return all, nil
}

// ListTables retrieves all tables in (projectID, datasetID) via tables.list.
func (c *RestClient) ListTables(ctx context.Context, projectID, datasetID string) ([]TableSummary, error) {
	if projectID == "" || datasetID == "" {
		return nil, fmt.Errorf("%w: empty project or dataset id", ErrInvalidTableRef)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("ListTables context: %w", err)
	}

	key := "tables:" + projectID + "." + datasetID

	val, err := c.doFlight(ctx, key, func(flightCtx context.Context) (any, error) {
		return c.fetchAllTables(flightCtx, projectID, datasetID)
	})
	if err != nil {
		return nil, err
	}

	tables, ok := val.([]TableSummary)
	if !ok {
		return nil, fmt.Errorf("%w: unexpected table list type %T", ErrAPIError, val)
	}

	return slices.Clone(tables), nil
}

func (c *RestClient) fetchAllTables(ctx context.Context, projectID, datasetID string) ([]TableSummary, error) {
	var all []TableSummary
	pageToken := ""

	for range maxListPages {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("list tables context: %w", err)
		}

		reqCtx, cancel := context.WithTimeout(ctx, c.timeout)

		endpoint := fmt.Sprintf(
			"%sprojects/%s/datasets/%s/tables?maxResults=1000",
			c.baseURL,
			url.PathEscape(projectID),
			url.PathEscape(datasetID),
		)
		if pageToken != "" {
			endpoint += "&pageToken=" + url.QueryEscape(pageToken)
		}

		body, err := c.doGET(reqCtx, endpoint)
		cancel()

		if err != nil {
			return nil, err
		}

		var page tableListResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("%w: decode table list: %w", ErrAPIError, err)
		}

		for _, tbl := range page.Tables {
			tbl.Ref = tbl.Ref.WithDefaults(projectID, datasetID)
			all = append(all, tbl)
		}

		if page.NextPageToken == "" {
			break
		}

		pageToken = page.NextPageToken
	}

	return all, nil
}

func (c *RestClient) doFlight(
	ctx context.Context,
	key string,
	fn func(context.Context) (any, error),
) (any, error) {
	fl := c.acquireFlight(key, fn)

	select {
	case <-ctx.Done():
		c.abandonFlight(key, fl)

		return nil, fmt.Errorf("request canceled: %w", ctx.Err())
	case <-fl.doneCh:
		return fl.val, fl.err
	}
}

func (c *RestClient) acquireFlight(
	key string,
	fn func(context.Context) (any, error),
) *restFlight {
	c.flightsMu.Lock()
	defer c.flightsMu.Unlock()

	if c.flights == nil {
		c.flights = make(map[string]*restFlight)
	}

	if fl, ok := c.flights[key]; ok {
		fl.waiters++

		return fl
	}

	flightCtx, cancel := context.WithCancel(context.Background())
	fl := &restFlight{
		cancel:  cancel,
		doneCh:  make(chan struct{}),
		waiters: 1,
	}
	c.flights[key] = fl

	_ = c.sf.DoChan(key, func() (any, error) {
		var (
			val any
			err error
		)

		defer func() {
			c.finishFlight(key, fl, val, err)
		}()

		val, err = fn(flightCtx)

		return val, err
	})

	return fl
}

func (c *RestClient) finishFlight(key string, fl *restFlight, val any, err error) {
	c.flightsMu.Lock()
	fl.val = val
	fl.err = err
	fl.done = true
	if c.flights[key] == fl {
		delete(c.flights, key)
		c.sf.Forget(key)
	}
	fl.cancel()
	close(fl.doneCh)
	c.flightsMu.Unlock()
}

func (c *RestClient) abandonFlight(key string, fl *restFlight) {
	c.flightsMu.Lock()
	fl.waiters--
	if fl.waiters <= 0 && !fl.done {
		if c.flights[key] == fl {
			delete(c.flights, key)
			c.sf.Forget(key)
		}
		fl.cancel()
	}
	c.flightsMu.Unlock()
}

func (c *RestClient) doGET(ctx context.Context, endpoint string) ([]byte, error) {
	body, err := c.doGETOnce(ctx, endpoint, c.tokenProvider)
	if err != nil && errors.Is(err, ErrUnauthorized) && !c.customAuth && c.defaultAuth != nil {
		if retryTok, ok := c.defaultAuth.fallbackAfterUnauthorized(ctx, false); ok {
			return c.doGETOnce(ctx, endpoint, func(context.Context) (string, error) {
				return retryTok, nil
			})
		}
	}

	return body, err
}

func (c *RestClient) tokenTimeout() time.Duration {
	if c.timeout > 0 && c.timeout < defaultHTTPTimeout {
		return c.timeout
	}

	return max(c.timeout, gcloudCommandTimeout)
}

func (c *RestClient) doGETOnce(ctx context.Context, endpoint string, tokProvider TokenProvider) ([]byte, error) {
	var authHeader string
	if tokProvider != nil {
		tokCtx, tokCancel := context.WithTimeout(ctx, c.tokenTimeout())
		tok, tokErr := tokProvider(tokCtx)
		tokCancel()
		if tokErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnauthorized, tokErr)
		}

		if tok != "" {
			authHeader = "Bearer " + tok
		}
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %w", ErrAPIError, err)
	}

	req.Header.Set("Accept", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: request failed: %w", ErrAPIError, err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, defaultMaxResponseSize))
	if readErr != nil {
		return nil, fmt.Errorf("%w: read response: %w", ErrAPIError, readErr)
	}

	if err := mapHTTPStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	return body, nil
}

func mapHTTPStatus(statusCode int, body []byte) error {
	switch statusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("%w: http %d", ErrTableNotFound, statusCode)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: http %d", ErrUnauthorized, statusCode)
	default:
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 256 {
			snippet = snippet[:256]
		}

		return fmt.Errorf("%w: http %d: %s", ErrAPIError, statusCode, snippet)
	}
}

type defaultTokenCache struct {
	httpTimeout      time.Duration
	gcloudPath       string
	mu               sync.Mutex
	adcSource        oauth2.TokenSource
	adcRetryAt       time.Time
	adcFullSource    oauth2.TokenSource
	adcFullRetryAt   time.Time
	gcloudToken      string
	gcloudExp        time.Time
	preferUserGcloud bool
}

func (d *defaultTokenCache) timeout() time.Duration {
	if d != nil && d.httpTimeout > 0 {
		return d.httpTimeout
	}

	return defaultHTTPTimeout
}

func (d *defaultTokenCache) gcloudTimeout() time.Duration {
	t := d.timeout()
	if t < defaultHTTPTimeout {
		return t
	}

	return max(t, gcloudCommandTimeout)
}

// WarmAuth proactively resolves and caches an OAuth2 access token so the first
// interactive BigQuery metadata lookup does not pay cold credential/CLI startup latency.
func (c *RestClient) WarmAuth(ctx context.Context) {
	if c == nil || c.tokenProvider == nil {
		return
	}

	warmCtx, cancel := context.WithTimeout(ctx, c.tokenTimeout())
	defer cancel()

	_, _ = c.tokenProvider(warmCtx)
}

func (d *defaultTokenCache) Token(ctx context.Context) (string, error) {
	return d.tokenForScope(ctx, false)
}

func (d *defaultTokenCache) DryRunToken(ctx context.Context) (string, error) {
	return d.tokenForScope(ctx, true)
}

func (d *defaultTokenCache) tokenForScope(ctx context.Context, fullScope bool) (string, error) {
	now := time.Now()

	if tok, skipADC, ok := d.cachedOrSkipADC(now, fullScope); ok {
		return tok, nil
	} else if !skipADC {
		tok, adcErr := d.tryADCToken(fullScope)
		if adcErr == nil {
			return tok, nil
		}

		gcloudTok, gcloudErr := d.gcloudFallback(ctx)
		if gcloudErr != nil {
			return "", fmt.Errorf("adc (%w) and %w", adcErr, gcloudErr)
		}

		return gcloudTok, nil
	}

	return d.gcloudFallback(ctx)
}

func (d *defaultTokenCache) fallbackAfterUnauthorized(ctx context.Context, fullScope bool) (string, bool) {
	now := time.Now()

	d.mu.Lock()
	alreadyUsingGcloud := d.gcloudToken != "" && now.Before(d.gcloudExp) && d.preferUserGcloud
	nextRetry := now.Add(gcloudTokenTTL)
	if fullScope {
		d.adcFullRetryAt = nextRetry
	} else {
		d.adcRetryAt = nextRetry
	}
	d.mu.Unlock()

	if alreadyUsingGcloud {
		return "", false
	}

	cmdCtx, cancel := context.WithTimeout(ctx, d.gcloudTimeout())
	defer cancel()

	tok, err := d.runGcloudTokenCommand(cmdCtx, "auth", "print-access-token")
	if err != nil || tok == "" {
		return "", false
	}

	d.cacheGcloudToken(tok, true)

	return tok, true
}

func (d *defaultTokenCache) cachedOrSkipADC(now time.Time, fullScope bool) (tok string, skipADC, hit bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.gcloudToken != "" && now.Before(d.gcloudExp) {
		return d.gcloudToken, false, true
	}

	retryAt := d.adcRetryAt
	if fullScope {
		retryAt = d.adcFullRetryAt
	}

	return "", now.Before(retryAt), false
}

func (d *defaultTokenCache) tryADCToken(fullScope bool) (string, error) {
	src, err := d.getOrInitADC(fullScope)
	if err == nil && src != nil {
		tok, tokErr := src.Token()
		if tokErr == nil && tok.AccessToken != "" {
			return tok.AccessToken, nil
		}

		if tokErr != nil {
			err = tokErr
		} else {
			err = errors.New("adc returned empty access token")
		}
	}

	d.mu.Lock()
	nextRetry := time.Now().Add(gcloudTokenTTL)
	if fullScope {
		d.adcFullRetryAt = nextRetry
	} else {
		d.adcRetryAt = nextRetry
	}
	d.mu.Unlock()

	return "", err
}

func (d *defaultTokenCache) getOrInitADC(fullScope bool) (oauth2.TokenSource, error) {
	d.mu.Lock()
	src := d.adcSource
	if fullScope {
		src = d.adcFullSource
	}
	d.mu.Unlock()

	if src != nil {
		return src, nil
	}

	scope := bigqueryReadonlyScope
	if fullScope {
		scope = bigqueryScope
	}

	// Bind a long-lived background context with a bounded HTTP client timeout so
	// future OAuth2 token refreshes never inherit a canceled per-request context.
	adcClient := &http.Client{Timeout: d.timeout()}
	adcCtx := context.WithValue(context.Background(), oauth2.HTTPClient, adcClient)

	creds, err := google.FindDefaultCredentials(adcCtx, scope)
	if err != nil {
		return nil, fmt.Errorf("find default credentials: %w", err)
	}

	if creds == nil || creds.TokenSource == nil {
		return nil, errors.New("find default credentials returned nil token source")
	}

	d.mu.Lock()
	if fullScope {
		if d.adcFullSource == nil {
			d.adcFullSource = creds.TokenSource
		}
		src = d.adcFullSource
	} else {
		if d.adcSource == nil {
			d.adcSource = creds.TokenSource
		}
		src = d.adcSource
	}
	d.mu.Unlock()

	return src, nil
}

func (d *defaultTokenCache) gcloudFallback(ctx context.Context) (string, error) {
	now := time.Now()

	d.mu.Lock()
	if d.gcloudToken != "" && now.Before(d.gcloudExp) {
		tok := d.gcloudToken
		d.mu.Unlock()

		return tok, nil
	}
	preferUser := d.preferUserGcloud
	d.mu.Unlock()

	if preferUser {
		if tok, err := d.runGcloudWithTimeout(ctx, "auth", "print-access-token"); err == nil {
			d.cacheGcloudToken(tok, true)
			return tok, nil
		}
	}

	tok, err := d.runGcloudWithTimeout(ctx, "auth", "application-default", "print-access-token")
	if err == nil {
		d.cacheGcloudToken(tok, false)
		return tok, nil
	}

	if !preferUser {
		userTok, userErr := d.runGcloudWithTimeout(ctx, "auth", "print-access-token")
		if userErr == nil {
			d.cacheGcloudToken(userTok, true)
			return userTok, nil
		}

		return "", fmt.Errorf("gcloud print-access-token failed: %w (user fallback: %w)", err, userErr)
	}

	return "", err
}

func (d *defaultTokenCache) runGcloudWithTimeout(ctx context.Context, args ...string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, d.gcloudTimeout())
	defer cancel()

	return d.runGcloudTokenCommand(cmdCtx, args...)
}

func (d *defaultTokenCache) cacheGcloudToken(tok string, preferUser bool) {
	d.mu.Lock()
	d.gcloudToken = tok
	d.gcloudExp = time.Now().Add(gcloudTokenTTL)
	d.preferUserGcloud = preferUser
	d.mu.Unlock()
}

func (d *defaultTokenCache) runGcloudTokenCommand(ctx context.Context, args ...string) (string, error) {
	bin := ResolveGcloudBinary(d.gcloudPath)
	cmd := exec.CommandContext(ctx, bin, args...)

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gcloud (%s) %s: %w", bin, strings.Join(args, " "), err)
	}

	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", errors.New("gcloud returned empty access token")
	}

	return tok, nil
}

// ResolveGcloudBinary returns the gcloud CLI executable path.
// If configuredPath is non-empty, it expands a leading "~/" and returns it.
// Otherwise it checks PATH and falls back to common Google Cloud SDK install locations
// so GUI editors launched with a minimal desktop PATH still locate gcloud.
func ResolveGcloudBinary(configuredPath string) string {
	if trimmed := strings.TrimSpace(configuredPath); trimmed != "" {
		return expandHomePath(trimmed)
	}

	if found, err := exec.LookPath("gcloud"); err == nil && found != "" {
		return found
	}

	for _, candidate := range commonGcloudCandidates() {
		if isExecutableFile(candidate) {
			return candidate
		}
	}

	return "gcloud"
}

func expandHomePath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(p, `~\`)) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if p == "~" {
				return home
			}

			return filepath.Join(home, p[2:])
		}
	}

	return p
}

func commonGcloudCandidates() []string {
	binNames := []string{"gcloud"}
	if runtime.GOOS == "windows" {
		binNames = []string{"gcloud.cmd", "gcloud.exe"}
	}

	var dirs []string
	if sdkRoot := strings.TrimSpace(os.Getenv("CLOUDSDK_ROOT_DIR")); sdkRoot != "" {
		dirs = append(dirs, filepath.Join(sdkRoot, "bin"))
	}

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs,
			filepath.Join(home, "google-cloud-sdk", "bin"),
			filepath.Join(home, ".gcloudsdk", "bin"),
			filepath.Join(home, ".local", "share", "google-cloud-sdk", "bin"),
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, "bin"),
		)
	}

	if runtime.GOOS == "windows" {
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			dirs = append(dirs, filepath.Join(localAppData, "Google", "Cloud SDK", "google-cloud-sdk", "bin"))
		}
		if progFiles := strings.TrimSpace(os.Getenv("ProgramFiles")); progFiles != "" {
			dirs = append(dirs, filepath.Join(progFiles, "Google", "Cloud SDK", "google-cloud-sdk", "bin"))
		}
	} else {
		dirs = append(dirs,
			"/usr/local/google-cloud-sdk/bin",
			"/opt/google-cloud-sdk/bin",
			"/opt/homebrew/share/google-cloud-sdk/bin",
			"/usr/local/share/google-cloud-sdk/bin",
			"/snap/bin",
			"/usr/lib/google-cloud-sdk/bin",
		)
	}

	out := make([]string, 0, len(dirs)*len(binNames))
	for _, dir := range dirs {
		for _, name := range binNames {
			out = append(out, filepath.Join(dir, name))
		}
	}

	return out
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}

	if runtime.GOOS == "windows" {
		return true
	}

	return info.Mode().Perm()&0o111 != 0
}
