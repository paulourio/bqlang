package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	dryRunQueueSize       = 64
	dryRunWorkers         = 2
	dryRunCacheTTL        = 5 * time.Minute
	maxDryRunCacheEntries = 256
)

// DryRunResult holds byte-processing and query metadata returned by BigQuery jobs.insert (dryRun=true).
type DryRunResult struct {
	TotalBytesProcessed int64      `json:"totalBytesProcessed"`
	TotalBytesBilled    int64      `json:"totalBytesBilled"`
	Accuracy            string     `json:"accuracy,omitempty"`
	StatementType       string     `json:"statementType,omitempty"`
	CacheHit            bool       `json:"cacheHit,omitempty"`
	ReferencedTables    []TableRef `json:"referencedTables,omitempty"`
	FetchedAt           time.Time  `json:"fetchedAt"`
}

// Clone returns a deep copy of r.
func (r *DryRunResult) Clone() *DryRunResult {
	if r == nil {
		return nil
	}

	cp := *r
	cp.ReferencedTables = slices.Clone(r.ReferencedTables)

	return &cp
}

// DryRunError captures a structured failure from a BigQuery dry-run attempt.
type DryRunError struct {
	Message   string    `json:"message"`
	Rejected  bool      `json:"rejected,omitempty"`
	AuthError bool      `json:"authError,omitempty"`
	FetchedAt time.Time `json:"fetchedAt"`
}

type dryRunNegEntry struct {
	failures int
	retryAt  time.Time
	errInfo  DryRunError
}

type dryRunJobRequest struct {
	Configuration dryRunJobConfig `json:"configuration"`
}

type dryRunJobConfig struct {
	DryRun bool                 `json:"dryRun"`
	Query  dryRunQueryJobConfig `json:"query"`
}

type dryRunQueryJobConfig struct {
	Query          string      `json:"query"`
	UseLegacySQL   bool        `json:"useLegacySql"`
	DefaultDataset *DatasetRef `json:"defaultDataset,omitempty"`
}

type dryRunJobResponse struct {
	Statistics *dryRunJobStatistics `json:"statistics,omitempty"`
}

type dryRunJobStatistics struct {
	TotalBytesProcessed *FlexInt64             `json:"totalBytesProcessed,omitempty"`
	Query               *dryRunQueryStatistics `json:"query,omitempty"`
}

type dryRunQueryStatistics struct {
	TotalBytesProcessed         *FlexInt64 `json:"totalBytesProcessed,omitempty"`
	TotalBytesBilled            *FlexInt64 `json:"totalBytesBilled,omitempty"`
	TotalBytesProcessedAccuracy string     `json:"totalBytesProcessedAccuracy,omitempty"`
	StatementType               string     `json:"statementType,omitempty"`
	CacheHit                    bool       `json:"cacheHit,omitempty"`
	ReferencedTables            []TableRef `json:"referencedTables,omitempty"`
}

type bigQueryAPIErrorEnvelope struct {
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type dryRunRequestItem struct {
	key            string
	project        string
	dataset        string
	sql            string
	referencedRefs []TableRef
}

type dryRunCacheEntry struct {
	result    *DryRunResult
	fetchedAt time.Time
}

// DryRunQuery executes a zero-cost BigQuery dry-run job via POST /projects/{projectId}/jobs
// with configuration.dryRun=true and returns the estimated bytes processed and query metadata.
func (c *RestClient) DryRunQuery(ctx context.Context, projectID, defaultDataset, querySQL string) (*DryRunResult, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("%w: empty project id", ErrInvalidTableRef)
	}

	if strings.TrimSpace(querySQL) == "" {
		return nil, fmt.Errorf("%w: empty query", ErrDryRunRejected)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("DryRunQuery context: %w", err)
	}

	key := "dryrun:" + dryRunCacheKey(projectID, defaultDataset, querySQL)

	val, err := c.doFlight(ctx, key, func(flightCtx context.Context) (any, error) {
		return c.executeDryRun(flightCtx, projectID, defaultDataset, querySQL)
	})
	if err != nil {
		return nil, err
	}

	res, ok := val.(*DryRunResult)
	if !ok {
		return nil, fmt.Errorf("%w: unexpected dry-run result type %T", ErrAPIError, val)
	}

	return res.Clone(), nil
}

func (c *RestClient) executeDryRun(
	ctx context.Context,
	projectID, defaultDataset, querySQL string,
) (*DryRunResult, error) {
	reqBody := dryRunJobRequest{
		Configuration: dryRunJobConfig{
			DryRun: true,
			Query: dryRunQueryJobConfig{
				Query:        querySQL,
				UseLegacySQL: false,
			},
		},
	}

	if ds := strings.TrimSpace(defaultDataset); ds != "" {
		reqBody.Configuration.Query.DefaultDataset = &DatasetRef{
			Project: projectID,
			Dataset: ds,
		}
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal dry-run request: %w", ErrAPIError, err)
	}

	endpoint := fmt.Sprintf("%sprojects/%s/jobs", c.baseURL, url.PathEscape(projectID))

	tokProvider := c.dryRunTokenProvider
	if tokProvider == nil {
		tokProvider = c.tokenProvider
	}

	body, err := c.doPOST(ctx, endpoint, payload, tokProvider)
	if err != nil {
		return nil, err
	}

	return decodeDryRunResponse(body)
}

func decodeDryRunResponse(body []byte) (*DryRunResult, error) {
	var resp dryRunJobResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("%w: decode dry-run response: %w", ErrAPIError, err)
	}

	res := &DryRunResult{
		FetchedAt: time.Now(),
	}

	if resp.Statistics == nil {
		return res, nil
	}

	if resp.Statistics.TotalBytesProcessed != nil {
		res.TotalBytesProcessed = resp.Statistics.TotalBytesProcessed.Int64()
	}

	if q := resp.Statistics.Query; q != nil {
		if q.TotalBytesProcessed != nil {
			res.TotalBytesProcessed = q.TotalBytesProcessed.Int64()
		}

		if q.TotalBytesBilled != nil {
			res.TotalBytesBilled = q.TotalBytesBilled.Int64()
		}

		res.Accuracy = strings.ToUpper(strings.TrimSpace(q.TotalBytesProcessedAccuracy))
		res.StatementType = strings.ToUpper(strings.TrimSpace(q.StatementType))
		res.CacheHit = q.CacheHit
		res.ReferencedTables = slices.Clone(q.ReferencedTables)
	}

	if res.TotalBytesProcessed < 0 {
		res.TotalBytesProcessed = 0
	}

	if res.TotalBytesBilled <= 0 {
		res.TotalBytesBilled = res.TotalBytesProcessed
	}

	return res, nil
}

func (c *RestClient) doPOST(
	ctx context.Context,
	endpoint string,
	payload []byte,
	tokProvider TokenProvider,
) ([]byte, error) {
	body, err := c.doPOSTOnce(ctx, endpoint, payload, tokProvider)
	if err != nil && errors.Is(err, ErrUnauthorized) && !c.customAuth && c.defaultAuth != nil {
		if retryTok, ok := c.defaultAuth.fallbackAfterUnauthorized(ctx, true); ok {
			return c.doPOSTOnce(ctx, endpoint, payload, func(context.Context) (string, error) {
				return retryTok, nil
			})
		}
	}

	return body, err
}

func (c *RestClient) doPOSTOnce(
	ctx context.Context,
	endpoint string,
	payload []byte,
	tokProvider TokenProvider,
) ([]byte, error) {
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

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %w", ErrAPIError, err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
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

	if err := mapDryRunHTTPStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	return body, nil
}

func mapDryRunHTTPStatus(statusCode int, body []byte) error {
	if statusCode == http.StatusBadRequest {
		msg := extractBigQueryErrorMessage(body)
		if msg == "" {
			msg = fmt.Sprintf("http %d", statusCode)
		}

		return fmt.Errorf("%w: %s", ErrDryRunRejected, msg)
	}

	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		if msg := extractBigQueryErrorMessage(body); msg != "" {
			return fmt.Errorf("%w: http %d: %s", ErrUnauthorized, statusCode, msg)
		}

		return fmt.Errorf("%w: http %d", ErrUnauthorized, statusCode)
	}

	return mapHTTPStatus(statusCode, body)
}

func extractBigQueryErrorMessage(body []byte) string {
	var env bigQueryAPIErrorEnvelope
	if json.Unmarshal(body, &env) == nil && env.Error != nil {
		if msg := strings.TrimSpace(env.Error.Message); msg != "" {
			return msg
		}
	}

	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 256 {
		snippet = snippet[:256]
	}

	return snippet
}

func dryRunCacheKey(project, dataset, querySQL string) string {
	normalizedSQL := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(querySQL), ";"))
	h := sha256.New()
	h.Write([]byte(strings.TrimSpace(project)))
	h.Write([]byte{0})
	h.Write([]byte(strings.TrimSpace(dataset)))
	h.Write([]byte{0})
	h.Write([]byte(normalizedSQL))

	return hex.EncodeToString(h.Sum(nil))
}

// WithOnDryRunUpdate registers a callback invoked after an async dry-run job completes.
//
// For multiple independent listeners sharing one Store, prefer Store.SubscribeDryRun.
func WithOnDryRunUpdate(fn func()) StoreOption {
	return func(s *Store) {
		s.OnDryRunUpdate(fn)
	}
}

// SubscribeDryRun registers fn to run after each async dry-run job completes
// (success or failure). It returns an idempotent function that removes the
// subscription. fn runs on a background goroutine with the store mutex released;
// it must not call Close synchronously.
func (s *Store) SubscribeDryRun(fn func()) (unsubscribe func()) {
	if s == nil || fn == nil {
		return func() {}
	}

	s.mu.Lock()
	id := s.addDryRunSubscriberLocked(fn)
	s.mu.Unlock()

	var once sync.Once

	return func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.dryRunSubs, id)
			s.mu.Unlock()
		})
	}
}

// OnDryRunUpdate sets or replaces the legacy single asynchronous dry-run completion callback.
func (s *Store) OnDryRunUpdate(fn func()) {
	if s == nil {
		return
	}

	s.mu.Lock()
	if s.legacyDryRunSubID != 0 {
		delete(s.dryRunSubs, s.legacyDryRunSubID)
		s.legacyDryRunSubID = 0
	}

	if fn != nil {
		s.legacyDryRunSubID = s.addDryRunSubscriberLocked(fn)
	}
	s.mu.Unlock()
}

func (s *Store) addDryRunSubscriberLocked(fn func()) uint64 {
	if s.dryRunSubs == nil {
		s.dryRunSubs = make(map[uint64]func())
	}

	s.nextSubID++
	id := s.nextSubID
	s.dryRunSubs[id] = fn

	return id
}

func (s *Store) snapshotDryRunSubscribersLocked() []func() {
	if len(s.dryRunSubs) == 0 {
		return nil
	}

	ids := make([]uint64, 0, len(s.dryRunSubs))
	for id := range s.dryRunSubs {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	subs := make([]func(), 0, len(ids))
	for _, id := range ids {
		subs = append(subs, s.dryRunSubs[id])
	}

	return subs
}

// EffectiveBillingProject returns the GCP project ID used for BigQuery dry-run
// jobs. Precedence:
//  1. [lsp].default_project / bqpls.defaultProject
//  2. GOOGLE_CLOUD_PROJECT / GCLOUD_PROJECT / CLOUDSDK_CORE_PROJECT
//  3. Active gcloud CLI configuration ([core] project)
//  4. ADC credentials file (quota_project_id / project_id)
func (s *Store) EffectiveBillingProject() string {
	if s == nil {
		return ""
	}

	if p := strings.TrimSpace(s.opts.DefaultProject); p != "" {
		return p
	}

	for _, envName := range []string{"GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT", "CLOUDSDK_CORE_PROJECT"} {
		if p := strings.TrimSpace(os.Getenv(envName)); p != "" {
			return p
		}
	}

	return discoverLocalGCPProject()
}

func discoverLocalGCPProject() string {
	if p := readGcloudConfigProject(); p != "" {
		return p
	}

	return readADCProject()
}

func gcloudConfigDir() string {
	if dir := strings.TrimSpace(os.Getenv("CLOUDSDK_CONFIG")); dir != "" {
		return filepath.Clean(dir)
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}

	return filepath.Join(home, ".config", "gcloud")
}

func readGcloudConfigProject() string {
	cfgDir := gcloudConfigDir()
	if cfgDir == "" {
		return ""
	}

	activeName := strings.TrimSpace(os.Getenv("CLOUDSDK_ACTIVE_CONFIG_NAME"))
	if activeName == "" {
		raw, err := os.ReadFile(filepath.Join(cfgDir, "active_config"))
		if err == nil {
			activeName = strings.TrimSpace(string(raw))
		}
	}
	if activeName == "" || activeName == "." || activeName == ".." || strings.ContainsAny(activeName, `/\`) {
		activeName = "default"
	}

	cfgPath := filepath.Join(cfgDir, "configurations", "config_"+activeName)
	data, err := os.ReadFile(filepath.Clean(cfgPath)) //nolint:gosec // reads standard local gcloud CLI configuration under CLOUDSDK_CONFIG
	if err != nil {
		return ""
	}

	return parseGcloudCoreProject(string(data))
}

func parseGcloudCoreProject(ini string) string {
	inCore := false
	for rawLine := range strings.SplitSeq(ini, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := strings.TrimSpace(line[1 : len(line)-1])
			inCore = strings.EqualFold(section, "core")

			continue
		}

		if !inCore {
			continue
		}

		k, v, hasEq := strings.Cut(line, "=")
		if hasEq && strings.EqualFold(strings.TrimSpace(k), "project") {
			return strings.TrimSpace(v)
		}
	}

	return ""
}

func readADCProject() string {
	credPath := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"))
	if credPath == "" {
		cfgDir := gcloudConfigDir()
		if cfgDir == "" {
			return ""
		}
		credPath = filepath.Join(cfgDir, "application_default_credentials.json")
	}

	data, err := os.ReadFile(filepath.Clean(credPath)) //nolint:gosec // reads standard GCP ADC file path from GOOGLE_APPLICATION_CREDENTIALS or gcloud dir
	if err != nil {
		return ""
	}

	var creds struct {
		QuotaProjectID string `json:"quota_project_id"`
		ProjectID      string `json:"project_id"`
	}
	if json.Unmarshal(data, &creds) != nil {
		return ""
	}

	if p := strings.TrimSpace(creds.QuotaProjectID); p != "" {
		return p
	}

	return strings.TrimSpace(creds.ProjectID)
}

// GetDryRun returns a fresh cached DryRunResult for (project, dataset, querySQL)
// without blocking on network I/O.
func (s *Store) GetDryRun(project, dataset, querySQL string) (*DryRunResult, bool) {
	if s == nil || strings.TrimSpace(project) == "" || strings.TrimSpace(querySQL) == "" {
		return nil, false
	}

	key := dryRunCacheKey(project, dataset, querySQL)
	now := s.now()

	s.mu.RLock()
	entry, ok := s.dryRuns[key]
	s.mu.RUnlock()

	if !ok || entry.result == nil {
		return nil, false
	}

	if now.Sub(entry.fetchedAt) > dryRunCacheTTL {
		return nil, false
	}

	return entry.result.Clone(), true
}

// GetDryRunError returns a recent cached DryRunError for (project, dataset, querySQL)
// without blocking on network I/O.
func (s *Store) GetDryRunError(project, dataset, querySQL string) (*DryRunError, bool) {
	if s == nil || strings.TrimSpace(project) == "" || strings.TrimSpace(querySQL) == "" {
		return nil, false
	}

	key := dryRunCacheKey(project, dataset, querySQL)
	now := s.now()

	s.mu.RLock()
	entry, ok := s.dryRunNeg[key]
	s.mu.RUnlock()

	if !ok || entry.errInfo.Message == "" || !now.Before(entry.retryAt) {
		return nil, false
	}

	cp := entry.errInfo

	return &cp, true
}

// PutDryRun inserts a DryRunResult directly into the in-memory dry-run cache.
func (s *Store) PutDryRun(project, dataset, querySQL string, res *DryRunResult) {
	if s == nil || res == nil || strings.TrimSpace(project) == "" || strings.TrimSpace(querySQL) == "" {
		return
	}

	key := dryRunCacheKey(project, dataset, querySQL)
	now := s.now()
	cp := res.Clone()
	if cp.FetchedAt.IsZero() {
		cp.FetchedAt = now
	}

	s.mu.Lock()
	delete(s.dryRunNeg, key)
	s.putDryRunLocked(key, cp, now)
	s.mu.Unlock()
}

// IsDryRunPending reports whether a dry-run request for (project, dataset, querySQL) is in flight.
func (s *Store) IsDryRunPending(project, dataset, querySQL string) bool {
	if s == nil || strings.TrimSpace(project) == "" || strings.TrimSpace(querySQL) == "" {
		return false
	}

	key := dryRunCacheKey(project, dataset, querySQL)

	s.mu.RLock()
	_, ok := s.dryRunPending[key]
	s.mu.RUnlock()

	return ok
}

// RequestDryRun enqueues an asynchronous BigQuery dry-run job for querySQL.
// Optional referencedTables identify the catalog tables/views scanned by querySQL
// so partition-filter rejections can be attributed back to views.
// When force is false, fresh cached results and negative-backoff entries are respected.
// Returns true if a new background job was enqueued.
func (s *Store) RequestDryRun(project, dataset, querySQL string, force bool, referencedTables ...TableRef) bool {
	if s == nil || !s.opts.BigQueryAPIEnabled {
		return false
	}

	project = strings.TrimSpace(project)
	querySQL = strings.TrimSpace(querySQL)
	if project == "" || querySQL == "" {
		return false
	}

	key := dryRunCacheKey(project, dataset, querySQL)
	now := s.now()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()

		return false
	}

	if _, pending := s.dryRunPending[key]; pending {
		s.mu.Unlock()

		return false
	}

	if !force {
		if entry, ok := s.dryRuns[key]; ok && entry.result != nil && now.Sub(entry.fetchedAt) <= dryRunCacheTTL {
			s.mu.Unlock()

			return false
		}

		if neg, hasNeg := s.dryRunNeg[key]; hasNeg && now.Before(neg.retryAt) {
			s.mu.Unlock()

			return false
		}
	}

	s.dryRunPending[key] = struct{}{}
	s.mu.Unlock()

	item := dryRunRequestItem{
		key:            key,
		project:        project,
		dataset:        strings.TrimSpace(dataset),
		sql:            querySQL,
		referencedRefs: slices.Clone(referencedTables),
	}

	select {
	case s.dryRunCh <- item:
		return true
	default:
		s.mu.Lock()
		delete(s.dryRunPending, key)
		s.mu.Unlock()

		return false
	}
}

func (s *Store) runDryRunWorker() {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			return
		case item := <-s.dryRunCh:
			if s.ctx.Err() != nil {
				return
			}

			s.processDryRun(item)
		}
	}
}

func (s *Store) processDryRun(item dryRunRequestItem) {
	res, err := s.rest.DryRunQuery(s.ctx, item.project, item.dataset, item.sql)
	now := s.now()

	var enriched []enrichedTableUpdate

	s.mu.Lock()
	delete(s.dryRunPending, item.key)

	updated := false
	switch {
	case err == nil && res != nil:
		res.FetchedAt = now
		delete(s.dryRunNeg, item.key)
		s.putDryRunLocked(item.key, res, now)
		updated = true
	case !errors.Is(err, context.Canceled):
		if errors.Is(err, ErrDryRunRejected) {
			s.logger.Debug("bigquery dry-run rejected query", slog.Any("error", err))
			if partErr, ok := ParseRequiredPartitionFilterError(cleanDryRunErrorText(err)); ok {
				enriched = s.applyRequiredPartitionFilterHintLocked(item.key, partErr, item.referencedRefs, now)
			}
		} else {
			s.logger.Warn("bigquery dry-run request failed", slog.Any("error", err))
		}
		delete(s.dryRuns, item.key)
		s.recordDryRunNegativeLocked(item.key, now, err)
		updated = true
	}

	dryRunSubs := s.snapshotDryRunSubscribersLocked()
	updateSubs := s.snapshotUpdateSubscribersLocked()
	closed := s.closed
	s.mu.Unlock()

	for _, upd := range enriched {
		if !upd.static {
			s.writeDiskCache(upd.meta, upd.fetchedAt)
		}
	}

	if updated && !closed && s.ctx.Err() == nil {
		if len(updateSubs) > 0 {
			for _, upd := range enriched {
				notifyUpdateSubscribers(updateSubs, upd.meta.Ref)
			}
		}

		for _, cb := range dryRunSubs {
			cb()
		}
	}
}

func (s *Store) putDryRunLocked(key string, res *DryRunResult, now time.Time) {
	if _, exists := s.dryRuns[key]; !exists {
		if len(s.dryRunOrder) >= maxDryRunCacheEntries {
			evict := s.dryRunOrder[0]
			s.dryRunOrder = s.dryRunOrder[1:]
			delete(s.dryRuns, evict)
		}

		s.dryRunOrder = append(s.dryRunOrder, key)
	}

	s.dryRuns[key] = dryRunCacheEntry{
		result:    res,
		fetchedAt: now,
	}
}

func (s *Store) recordDryRunNegativeLocked(key string, now time.Time, err error) {
	entry := s.dryRunNeg[key]
	shift := min(entry.failures, 4)
	backoff := min(baseNegBackoff<<shift, maxNegBackoff)
	entry.failures++
	entry.retryAt = now.Add(backoff)
	entry.errInfo = DryRunError{
		Message:   cleanDryRunErrorText(err),
		Rejected:  errors.Is(err, ErrDryRunRejected),
		AuthError: errors.Is(err, ErrUnauthorized),
		FetchedAt: now,
	}
	s.dryRunNeg[key] = entry
}

func cleanDryRunErrorText(err error) string {
	if err == nil {
		return ""
	}

	msg := strings.TrimSpace(err.Error())
	for _, prefix := range []string{
		ErrDryRunRejected.Error() + ": ",
		ErrUnauthorized.Error() + ": ",
		ErrAPIError.Error() + ": ",
	} {
		msg = strings.TrimPrefix(msg, prefix)
	}

	return msg
}
