package catalog

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/paulourio/bqlang/internal/bqconfig"
)

const (
	fetchQueueSize = 128
	fetchWorkers   = 2
	baseNegBackoff = 30 * time.Second
	maxNegBackoff  = 5 * time.Minute
	dirPerm        = 0o750
	cacheFilePerm  = 0o600
	maxSuggestDist = 4
)

type cacheEntry struct {
	meta      *TableMeta
	fetchedAt time.Time
	static    bool
}

type negCacheEntry struct {
	failures int
	retryAt  time.Time
}

// StoreOption configures a Store.
type StoreOption func(*Store)

// WithOnUpdate registers a callback invoked after an async table fetch completes
// (both on success and on failure).
//
// The callback runs synchronously on a Store background worker goroutine with
// Store's internal mutex released, so calling GetTable or IsPending from within
// fn is safe, but callers must NOT invoke Store.Close synchronously inside fn.
func WithOnUpdate(fn func(TableRef)) StoreOption {
	return func(s *Store) {
		s.onUpdate = fn
	}
}

// WithRestClient injects a custom RestClient into Store.
func WithRestClient(client *RestClient) StoreOption {
	return func(s *Store) {
		if client != nil {
			s.rest = client
		}
	}
}

// WithClock overrides the clock function used for TTL and backoff checks.
func WithClock(now func() time.Time) StoreOption {
	return func(s *Store) {
		if now != nil {
			s.now = now
		}
	}
}

// WithLogger sets the structured logger used by Store for non-fatal warnings.
func WithLogger(logger *slog.Logger) StoreOption {
	return func(s *Store) {
		if logger != nil {
			s.logger = logger
		}
	}
}

// Store provides non-blocking access to BigQuery table metadata backed by
// workspace schema_dirs, an in-memory map, a persistent disk TTL cache, and
// asynchronous BigQuery REST v2 fetches.
type Store struct {
	opts                    bqconfig.LSPOptions
	policy                  cacheTTLPolicy
	rest                    *RestClient
	disk                    *diskCache
	now                     func() time.Time
	onUpdate                func(TableRef)
	onDryRunUpdate          func()
	logger                  *slog.Logger
	viewProbeThresholdBytes int64

	mu                    sync.RWMutex
	mem                   map[string]cacheEntry // exact FullName -> entry
	memLower              map[string]string     // lowercase FullName -> exact FullName
	pending               map[string]struct{}
	negCache              map[string]negCacheEntry
	datasetLists          map[string]datasetListCacheEntry
	tableLists            map[string]tableListCacheEntry
	liveRecheckAt         map[string]time.Time
	dryRuns               map[string]dryRunCacheEntry
	dryRunOrder           []string
	dryRunPending         map[string]struct{}
	dryRunNeg             map[string]dryRunNegEntry
	pendingPartitionHints map[string]pendingPartitionHint
	viewProbePending      map[string]struct{}
	viewProbeNeg          map[string]negCacheEntry
	fetchDoneCh           chan struct{}
	closed                bool

	ctx         context.Context //nolint:containedctx // lifecycle context canceled by Close()
	cancel      context.CancelFunc
	fetchCh     chan TableRef
	dryRunCh    chan dryRunRequestItem
	viewProbeCh chan TableRef
	wg          sync.WaitGroup
}

// NewStore creates a Store initialized with opts, loading static schemas from
// opts.SchemaDirs and starting background fetch workers when BigQueryAPIEnabled is true.
func NewStore(opts *bqconfig.LSPOptions, storeOpts ...StoreOption) (*Store, error) {
	cfg := cloneLSPOptions(opts)
	policy := buildCacheTTLPolicy(&cfg)

	ctx, cancel := context.WithCancel(context.Background())
	s := &Store{
		opts:                    cfg,
		policy:                  policy,
		now:                     time.Now,
		logger:                  slog.New(slog.DiscardHandler),
		viewProbeThresholdBytes: defaultViewPartitionProbeThresholdBytes,
		mem:                     make(map[string]cacheEntry),
		memLower:                make(map[string]string),
		pending:                 make(map[string]struct{}),
		negCache:                make(map[string]negCacheEntry),
		datasetLists:            make(map[string]datasetListCacheEntry),
		tableLists:              make(map[string]tableListCacheEntry),
		liveRecheckAt:           make(map[string]time.Time),
		dryRuns:                 make(map[string]dryRunCacheEntry),
		dryRunPending:           make(map[string]struct{}),
		dryRunNeg:               make(map[string]dryRunNegEntry),
		pendingPartitionHints:   make(map[string]pendingPartitionHint),
		viewProbePending:        make(map[string]struct{}),
		viewProbeNeg:            make(map[string]negCacheEntry),
		fetchDoneCh:             make(chan struct{}),
		ctx:                     ctx,
		cancel:                  cancel,
		fetchCh:                 make(chan TableRef, fetchQueueSize),
		dryRunCh:                make(chan dryRunRequestItem, dryRunQueueSize),
		viewProbeCh:             make(chan TableRef, viewProbeQueueSize),
	}

	for _, opt := range storeOpts {
		if opt != nil {
			opt(s)
		}
	}

	if s.rest == nil {
		s.rest = NewRestClient(WithGcloudPath(s.opts.GcloudPath))
	}

	if s.opts.CacheDir != "" {
		s.disk = newDiskCache(s.opts.CacheDir, s.policy, s.logger)
	}

	s.loadSchemaDirs()

	if s.opts.BigQueryAPIEnabled {
		s.wg.Go(func() {
			s.rest.WarmAuth(s.ctx)
		})

		for range fetchWorkers {
			s.wg.Add(1)
			go s.runWorker()
		}

		for range dryRunWorkers {
			s.wg.Add(1)
			go s.runDryRunWorker()
		}

		s.wg.Add(1)
		go s.runViewProbeWorker()
	}

	return s, nil
}

func cloneLSPOptions(opts *bqconfig.LSPOptions) bqconfig.LSPOptions {
	if opts == nil {
		return *bqconfig.DefaultLSPOptions()
	}

	cp := *opts
	cp.SchemaDirs = slices.Clone(opts.SchemaDirs)
	cp.ProjectCacheTTLSeconds = bqconfig.NormalizeProjectCacheTTLs(opts.ProjectCacheTTLSeconds)

	return cp
}

// OnUpdate sets or replaces the asynchronous fetch completion callback.
// Callers must NOT invoke Store.Close synchronously from within fn, as fn runs
// on a background worker goroutine tracked by Close.
func (s *Store) OnUpdate(fn func(TableRef)) {
	s.mu.Lock()
	s.onUpdate = fn
	s.mu.Unlock()
}

// Close cancels background workers and waits for in-flight fetches to exit.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()

		return nil
	}

	s.closed = true
	s.broadcastFetchDoneLocked()
	s.cancel()
	s.mu.Unlock()

	s.wg.Wait()

	if s.disk != nil {
		return s.disk.Close()
	}

	return nil
}

// DefaultProject returns the configured default GCP project ID.
func (s *Store) DefaultProject() string {
	return s.opts.DefaultProject
}

// DefaultDataset returns the configured default BigQuery dataset ID.
func (s *Store) DefaultDataset() string {
	return s.opts.DefaultDataset
}

// EffectiveCacheTTL returns the effective metadata cache TTL for project,
// applying any configured per-project TTL extension over CacheTTLSeconds.
func (s *Store) EffectiveCacheTTL(project string) time.Duration {
	return s.policy.ttlForProject(project)
}

func (s *Store) ttlForProject(project string) time.Duration {
	return s.policy.ttlForProject(project)
}

// ClearNegativeCache clears transient in-memory negative lookup backoffs so
// newly created external BigQuery datasets and tables can be rechecked immediately.
func (s *Store) ClearNegativeCache() {
	s.mu.Lock()
	clear(s.negCache)
	clear(s.liveRecheckAt)
	clear(s.dryRunNeg)
	clear(s.pendingPartitionHints)
	clear(s.viewProbeNeg)
	s.mu.Unlock()
}

// PruneExpired evicts expired metadata entries from both in-memory maps and the
// persistent SQLite disk cache, and removes any temporary (_-prefixed) datasets
// from disk storage.
func (s *Store) PruneExpired() {
	now := s.now()

	s.mu.Lock()
	s.pruneMemoryLocked(now)
	s.mu.Unlock()

	if s.disk != nil {
		s.disk.pruneNow(now)
	}
}

func (s *Store) pruneMemoryLocked(now time.Time) {
	for key, entry := range s.mem {
		if s.isMemEntryExpiredLocked(entry, now) {
			delete(s.mem, key)
			delete(s.memLower, strings.ToLower(key))
		}
	}

	for projLower, entry := range s.datasetLists {
		ttl := s.policy.ttlForProject(projLower)
		if ttl > 0 && now.Sub(entry.loadedAt) > ttl {
			delete(s.datasetLists, projLower)
		}
	}

	for key, entry := range s.tableLists {
		projLower, _, _ := strings.Cut(key, ".")
		ttl := s.policy.ttlForProject(projLower)
		if ttl > 0 && now.Sub(entry.loadedAt) > ttl {
			delete(s.tableLists, key)
		}
	}

	for key, neg := range s.negCache {
		if !now.Before(neg.retryAt) {
			delete(s.negCache, key)
		}
	}

	for key, checkedAt := range s.liveRecheckAt {
		if now.Sub(checkedAt) >= liveListRecheckCooldown {
			delete(s.liveRecheckAt, key)
		}
	}

	s.pruneDryRunMemoryLocked(now)
}

func (s *Store) pruneDryRunMemoryLocked(now time.Time) {
	for key, neg := range s.dryRunNeg {
		if !now.Before(neg.retryAt) {
			delete(s.dryRunNeg, key)
		}
	}

	for key, hint := range s.pendingPartitionHints {
		if now.Sub(hint.recordedAt) > dryRunCacheTTL {
			delete(s.pendingPartitionHints, key)
		}
	}

	for key, neg := range s.viewProbeNeg {
		if !now.Before(neg.retryAt) {
			delete(s.viewProbeNeg, key)
		}
	}
}

// PutTable stores meta directly into the in-memory cache.
func (s *Store) PutTable(meta *TableMeta) {
	if meta == nil {
		return
	}

	cp := meta.Clone()
	cp.Normalize(TableRef{
		Project: s.opts.DefaultProject,
		Dataset: s.opts.DefaultDataset,
	})

	s.mu.Lock()
	s.putLocked(cp, time.Time{}, true)
	s.mu.Unlock()
}

func (s *Store) putLocked(meta *TableMeta, fetchedAt time.Time, static bool) {
	key := meta.Ref.FullName()
	if key == "" {
		return
	}

	s.mem[key] = cacheEntry{meta: meta, fetchedAt: fetchedAt, static: static}
	s.memLower[strings.ToLower(key)] = key
}

func (s *Store) isMemEntryExpiredLocked(entry cacheEntry, now time.Time) bool {
	if entry.static || entry.fetchedAt.IsZero() || entry.meta == nil {
		return false
	}

	ttl := s.policy.ttlForProject(entry.meta.Ref.Project)
	if ttl <= 0 {
		return false
	}

	return now.Sub(entry.fetchedAt) > ttl
}

// GetTable resolves ref from memory or the disk TTL cache without blocking on
// network I/O. On cache miss, if BigQueryAPIEnabled is true, it enqueues an
// asynchronous fetch and returns (nil, false).
func (s *Store) GetTable(ref TableRef) (*TableMeta, bool) {
	resolved := ref.WithDefaults(s.opts.DefaultProject, s.opts.DefaultDataset)
	if resolved.Table == "" {
		return nil, false
	}

	if entry, ok := s.getEntryFromMemory(resolved); ok {
		if !entry.static {
			s.enqueueViewProbeIfNeeded(entry.meta)
		}

		return entry.meta, true
	}

	if meta, ok := s.tryLoadDiskCache(resolved); ok {
		return meta, true
	}

	s.enqueueFetchIfNeeded(resolved)

	return nil, false
}

func (s *Store) getEntryFromMemory(ref TableRef) (cacheEntry, bool) {
	now := s.now()

	s.mu.RLock()
	entry, _, ok := s.getEntryFromMemoryLocked(ref, now)
	s.mu.RUnlock()

	return entry, ok
}

func (s *Store) tryLoadDiskCache(ref TableRef) (*TableMeta, bool) {
	if s.disk == nil || !ref.IsComplete() || isTempDataset(ref.Dataset) {
		return nil, false
	}

	if s.ttlForProject(ref.Project) <= 0 {
		return nil, false
	}

	meta, fetchedAt, ok := s.disk.getTable(ref, s.now())
	if !ok {
		return nil, false
	}

	s.mu.Lock()
	s.putLocked(meta, fetchedAt, false)
	s.mu.Unlock()

	s.enqueueViewProbeIfNeeded(meta)

	return meta, true
}

func (s *Store) enqueueFetchIfNeeded(ref TableRef) {
	if !s.opts.BigQueryAPIEnabled || !ref.IsComplete() {
		return
	}

	key := ref.FullName()
	now := s.now()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()

		return
	}

	if _, pending := s.pending[key]; pending {
		s.mu.Unlock()

		return
	}

	if neg, hasNeg := s.negCache[key]; hasNeg && now.Before(neg.retryAt) {
		s.mu.Unlock()

		return
	}

	s.pending[key] = struct{}{}
	s.mu.Unlock()

	select {
	case s.fetchCh <- ref:
	default:
		s.mu.Lock()
		delete(s.pending, key)
		s.mu.Unlock()
	}
}

func (s *Store) runWorker() {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			return
		case ref := <-s.fetchCh:
			if s.ctx.Err() != nil {
				return
			}

			s.processFetch(ref)
		}
	}
}

func (s *Store) processFetch(ref TableRef) {
	if strings.HasSuffix(ref.Table, "*") {
		s.processWildcardFetch(ref)
		return
	}

	meta, err := s.rest.FetchTable(s.ctx, ref)
	key := ref.FullName()
	now := s.now()

	if err == nil && meta != nil {
		meta = meta.Clone()
		meta.Normalize(ref)
		s.writeDiskCache(meta, now)
	}

	s.mu.Lock()
	delete(s.pending, key)

	var (
		enriched  []enrichedTableUpdate
		probeMeta *TableMeta
	)
	switch {
	case err == nil && meta != nil:
		delete(s.negCache, key)
		s.putLocked(meta, now, false)
		enriched = s.applyPendingPartitionHintsLocked(now)
		if cur, ok := s.getFromMemoryLocked(ref, now); ok {
			probeMeta = cur
		}
	case !errors.Is(err, context.Canceled):
		s.logger.Warn("bigquery table metadata fetch failed", slog.String("table", key), slog.Any("error", err))
		s.recordNegativeLocked(key)
		enriched = s.applyPendingPartitionHintsLocked(now)
	}

	cb := s.onUpdate
	closed := s.closed
	s.broadcastFetchDoneLocked()
	s.mu.Unlock()

	for _, upd := range enriched {
		if !upd.static {
			s.writeDiskCache(upd.meta, upd.fetchedAt)
		}
	}

	if !closed && s.ctx.Err() == nil && cb != nil {
		cb(ref)
		for _, upd := range enriched {
			if upd.meta.Ref.NormalizedKey() != ref.NormalizedKey() {
				cb(upd.meta.Ref)
			}
		}
	}

	if !closed && s.ctx.Err() == nil && probeMeta != nil {
		s.enqueueViewProbeIfNeeded(probeMeta)
	}
}

func (s *Store) processWildcardFetch(wildcardRef TableRef) {
	key := wildcardRef.FullName()
	prefix := strings.TrimSuffix(wildcardRef.Table, "*")
	summaries, err := s.rest.ListTables(s.ctx, wildcardRef.Project, wildcardRef.Dataset)

	var (
		meta     *TableMeta
		fetchErr = err
	)

	if err == nil {
		now := s.now()
		if len(summaries) > 0 {
			dsKey := strings.ToLower(wildcardRef.Project + "." + wildcardRef.Dataset)
			s.mu.Lock()
			s.tableLists[dsKey] = tableListCacheEntry{tables: summaries, loadedAt: now}
			s.mu.Unlock()

			if s.disk != nil {
				s.disk.putTableList(wildcardRef.Project, wildcardRef.Dataset, summaries, now)
			}
		}

		if matchRef, matched := pickWildcardSummaryRef(summaries, prefix); matched {
			meta, fetchErr = s.rest.FetchTable(s.ctx, matchRef)
			if fetchErr == nil && meta != nil {
				meta = meta.Clone()
				meta.Normalize(matchRef)
				s.writeDiskCache(meta, s.now())
			}
		} else {
			fetchErr = ErrTableNotFound
		}
	}

	now := s.now()

	s.mu.Lock()
	delete(s.pending, key)

	switch {
	case fetchErr == nil && meta != nil:
		delete(s.negCache, key)
		s.putLocked(meta, now, false)
	case !errors.Is(fetchErr, context.Canceled):
		s.logger.Warn("bigquery wildcard table fetch failed", slog.String("table", key), slog.Any("error", fetchErr))
		s.recordNegativeLocked(key)
	}

	cb := s.onUpdate
	closed := s.closed
	s.broadcastFetchDoneLocked()
	s.mu.Unlock()

	if !closed && s.ctx.Err() == nil && cb != nil {
		cb(wildcardRef)
	}
}

func pickWildcardSummaryRef(summaries []TableSummary, prefix string) (TableRef, bool) {
	sorted := slices.Clone(summaries)
	slices.SortFunc(sorted, func(a, b TableSummary) int {
		return strings.Compare(a.Ref.FullName(), b.Ref.FullName())
	})

	for _, ts := range sorted {
		if strings.HasPrefix(ts.Ref.Table, prefix) {
			return ts.Ref, true
		}
	}

	lowerPrefix := strings.ToLower(prefix)
	for _, ts := range sorted {
		if strings.HasPrefix(strings.ToLower(ts.Ref.Table), lowerPrefix) {
			return ts.Ref, true
		}
	}

	return TableRef{}, false
}

func (s *Store) recordNegativeLocked(key string) {
	entry := s.negCache[key]
	shift := min(entry.failures, 4)
	backoff := min(baseNegBackoff<<shift, maxNegBackoff)
	entry.failures++
	entry.retryAt = s.now().Add(backoff)
	s.negCache[key] = entry
}

func (s *Store) broadcastFetchDoneLocked() {
	if s.fetchDoneCh != nil {
		close(s.fetchDoneCh)
		s.fetchDoneCh = make(chan struct{})
	}
}

// WaitForTable returns cached metadata for ref immediately if available, or
// waits up to maxWait (or until ctx is canceled) for an in-flight async
// BigQuery REST fetch to complete. It never blocks when BigQueryAPIEnabled is
// false or no fetch is pending.
func (s *Store) WaitForTable(ctx context.Context, ref TableRef, maxWait time.Duration) (*TableMeta, bool) {
	if meta, ok := s.GetTable(ref); ok {
		return meta, true
	}

	if maxWait <= 0 {
		return nil, false
	}

	resolved := ref.WithDefaults(s.opts.DefaultProject, s.opts.DefaultDataset)
	key := resolved.FullName()

	timer := time.NewTimer(maxWait)
	defer timer.Stop()

	for {
		s.mu.RLock()
		if meta, ok := s.getFromMemoryLocked(resolved, s.now()); ok {
			s.mu.RUnlock()
			return meta, true
		}
		_, pending := s.pending[key]
		ch := s.fetchDoneCh
		closed := s.closed
		s.mu.RUnlock()

		if !pending || closed || ch == nil {
			return nil, false
		}

		select {
		case <-ctx.Done():
			return nil, false
		case <-timer.C:
			return nil, false
		case <-ch:
		}
	}
}

func (s *Store) getFromMemoryLocked(ref TableRef, now time.Time) (*TableMeta, bool) {
	key := ref.FullName()
	if entry, ok := s.mem[key]; ok && !s.isMemEntryExpiredLocked(entry, now) {
		return entry.meta, true
	}

	if exact, ok := s.memLower[strings.ToLower(key)]; ok {
		if entry, exists := s.mem[exact]; exists && !s.isMemEntryExpiredLocked(entry, now) {
			return entry.meta, true
		}
	}

	return nil, false
}

// IsPending reports whether an asynchronous fetch for ref is currently queued or in flight.
func (s *Store) IsPending(ref TableRef) bool {
	resolved := ref.WithDefaults(s.opts.DefaultProject, s.opts.DefaultDataset)
	key := resolved.FullName()

	s.mu.RLock()
	_, ok := s.pending[key]
	s.mu.RUnlock()

	return ok
}

// ListCachedTables returns a deterministically sorted snapshot of all tables
// currently in the in-memory store.
func (s *Store) ListCachedTables() []*TableMeta {
	now := s.now()

	s.mu.RLock()
	out := make([]*TableMeta, 0, len(s.mem))
	for _, entry := range s.mem {
		if !s.isMemEntryExpiredLocked(entry, now) {
			out = append(out, entry.meta)
		}
	}
	s.mu.RUnlock()

	slices.SortFunc(out, func(a, b *TableMeta) int {
		return strings.Compare(a.Ref.FullName(), b.Ref.FullName())
	})

	return out
}

// ListDatasets returns distinct sorted dataset names present in the store,
// optionally filtered by project (case-insensitive when non-empty).
func (s *Store) ListDatasets(project string) []string {
	now := s.now()

	s.mu.RLock()
	seen := make(map[string]struct{})
	for _, entry := range s.mem {
		if s.isMemEntryExpiredLocked(entry, now) {
			continue
		}

		ref := entry.meta.Ref
		if ref.Dataset == "" {
			continue
		}

		if project != "" && !strings.EqualFold(ref.Project, project) {
			continue
		}

		seen[ref.Dataset] = struct{}{}
	}
	s.mu.RUnlock()

	out := make([]string, 0, len(seen))
	for ds := range seen {
		out = append(out, ds)
	}

	slices.Sort(out)

	return out
}

// ListTables returns sorted cached tables matching project and dataset
// (case-insensitive when non-empty).
func (s *Store) ListTables(project, dataset string) []*TableMeta {
	now := s.now()

	s.mu.RLock()
	var out []*TableMeta
	for _, entry := range s.mem {
		if s.isMemEntryExpiredLocked(entry, now) {
			continue
		}

		ref := entry.meta.Ref
		if project != "" && !strings.EqualFold(ref.Project, project) {
			continue
		}

		if dataset != "" && !strings.EqualFold(ref.Dataset, dataset) {
			continue
		}

		out = append(out, entry.meta)
	}
	s.mu.RUnlock()

	slices.SortFunc(out, func(a, b *TableMeta) int {
		return strings.Compare(a.Ref.FullName(), b.Ref.FullName())
	})

	return out
}

// MatchWildcardTable finds a deterministic cached table in (project, dataset)
// whose table name begins with tablePrefix, falling back to the persistent disk
// cache or enqueuing an asynchronous BigQuery discovery fetch on cache miss.
func (s *Store) MatchWildcardTable(project, dataset, tablePrefix string) (*TableMeta, bool) {
	resolved := TableRef{Project: project, Dataset: dataset}.WithDefaults(
		s.opts.DefaultProject,
		s.opts.DefaultDataset,
	)

	tables := s.ListTables(resolved.Project, resolved.Dataset)
	lowerPrefix := strings.ToLower(tablePrefix)

	for _, meta := range tables {
		if strings.HasPrefix(meta.Ref.Table, tablePrefix) {
			return meta, true
		}
	}

	for _, meta := range tables {
		if strings.HasPrefix(strings.ToLower(meta.Ref.Table), lowerPrefix) {
			return meta, true
		}
	}

	if s.disk != nil && resolved.Project != "" && resolved.Dataset != "" {
		if meta, fetchedAt, ok := s.disk.matchWildcardTable(resolved.Project, resolved.Dataset, tablePrefix, s.now()); ok {
			s.mu.Lock()
			s.putLocked(meta, fetchedAt, false)
			s.mu.Unlock()

			return meta, true
		}
	}

	if resolved.Project != "" && resolved.Dataset != "" {
		s.enqueueFetchIfNeeded(TableRef{
			Project: resolved.Project,
			Dataset: resolved.Dataset,
			Table:   tablePrefix + "*",
		})
	}

	return nil, false
}

// SuggestTableName returns the closest cached table name for mistyped path parts,
// or an empty string if no reasonable suggestion exists.
func (s *Store) SuggestTableName(parts []string) string {
	if len(parts) == 0 {
		return ""
	}

	target := strings.ToLower(strings.Join(parts, "."))
	tables := s.ListCachedTables()

	bestName := ""
	bestDist := maxSuggestDist + 1

	for _, meta := range tables {
		candidates := []string{meta.Ref.Table, meta.Ref.FullName()}
		if meta.Ref.Dataset != "" {
			candidates = append(candidates, meta.Ref.Dataset+"."+meta.Ref.Table)
		}

		for _, cand := range candidates {
			d := levenshtein(target, strings.ToLower(cand))
			if d < bestDist {
				bestDist = d
				bestName = meta.Ref.FullName()
			}
		}
	}

	if bestDist > maxSuggestDist {
		return ""
	}

	return bestName
}

func (s *Store) loadSchemaDirs() {
	for _, rawDir := range s.opts.SchemaDirs {
		dir := filepath.Clean(rawDir)
		info, err := os.Stat(dir)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				s.logger.Warn("skipping unreadable schema_dir", slog.String("dir", dir), slog.Any("error", err))
			}

			continue
		}

		if !info.IsDir() {
			continue
		}

		s.walkSchemaDir(dir)
	}
}

func (s *Store) walkSchemaDir(root string) {
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			s.logger.Warn("skipping unreadable schema path", slog.String("path", path), slog.Any("error", walkErr))
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}

		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}

		s.loadSchemaFile(root, path)

		return nil
	}); err != nil {
		s.logger.Warn("skipping schema_dir walk failure", slog.String("dir", root), slog.Any("error", err))
	}
}

func (s *Store) loadSchemaFile(root, path string) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		s.logger.Warn("skipping schema file with invalid relative path", slog.String("path", path), slog.Any("error", err))

		return
	}

	fallbackRef := inferRefFromSchemaRelPath(rel).WithDefaults(
		s.opts.DefaultProject,
		s.opts.DefaultDataset,
	)

	data, err := os.ReadFile(path)
	if err != nil {
		s.logger.Warn("skipping unreadable schema file", slog.String("path", path), slog.Any("error", err))

		return
	}

	meta, err := DecodeTableJSON(data, fallbackRef)
	if err != nil {
		s.logger.Warn("skipping corrupt schema file", slog.String("path", path), slog.Any("error", err))

		return
	}

	if meta.Ref.Table != "" {
		s.mu.Lock()
		s.putLocked(meta, time.Time{}, true)
		s.mu.Unlock()
	}
}

func inferRefFromSchemaRelPath(rel string) TableRef {
	slashRel := filepath.ToSlash(rel)
	parts := strings.Split(slashRel, "/")
	if len(parts) == 3 {
		return TableRef{
			Project: parts[0],
			Dataset: parts[1],
			Table:   trimJSONSuffix(parts[2]),
		}
	}

	base := trimJSONSuffix(filepath.Base(rel))
	dotParts := strings.Split(base, ".")
	if len(dotParts) >= 3 {
		return TableRef{
			Project: strings.Join(dotParts[:len(dotParts)-2], "."),
			Dataset: dotParts[len(dotParts)-2],
			Table:   dotParts[len(dotParts)-1],
		}
	}

	return TableRef{Table: base}
}

func trimJSONSuffix(name string) string {
	if len(name) >= 5 && strings.EqualFold(name[len(name)-5:], ".json") {
		return name[:len(name)-5]
	}

	return name
}

func (s *Store) writeDiskCache(meta *TableMeta, now time.Time) {
	if s.disk == nil || meta == nil || !meta.Ref.IsComplete() || isTempDataset(meta.Ref.Dataset) {
		return
	}

	s.disk.putTable(meta, now)
}

func levenshtein(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}

	if len(rb) == 0 {
		return len(ra)
	}

	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)

	for j := range prev {
		prev[j] = j
	}

	for i, ca := range ra {
		curr[0] = i + 1
		for j, cb := range rb {
			cost := 1
			if ca == cb {
				cost = 0
			}

			curr[j+1] = min(prev[j+1]+1, curr[j]+1, prev[j]+cost)
		}

		copy(prev, curr)
	}

	return prev[len(rb)]
}
