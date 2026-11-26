package catalog

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"
)

const liveListRecheckCooldown = 5 * time.Second

type datasetListCacheEntry struct {
	datasets []string
	loadedAt time.Time
}

type tableListCacheEntry struct {
	tables   []TableSummary
	loadedAt time.Time
}

// ListProjects returns distinct sorted GCP project IDs known to the store from
// default_project, configured project_cache_ttl_seconds, in-memory cached tables,
// and non-expired persistent disk cache entries.
func (s *Store) ListProjects() []string {
	now := s.now()
	byLower := make(map[string]string)

	addProject := func(proj string) {
		trimmed := strings.TrimSpace(proj)
		if trimmed == "" {
			return
		}

		lower := strings.ToLower(trimmed)
		if _, exists := byLower[lower]; !exists {
			byLower[lower] = trimmed
		}
	}

	addProject(s.opts.DefaultProject)

	s.mu.RLock()
	for _, entry := range s.mem {
		if !s.isMemEntryExpiredLocked(entry, now) && entry.meta != nil {
			addProject(entry.meta.Ref.Project)
		}
	}
	s.mu.RUnlock()

	if s.disk != nil {
		for _, proj := range s.disk.listProjects(now) {
			addProject(proj)
		}
	}

	for proj := range s.opts.ProjectCacheTTLSeconds {
		addProject(proj)
	}

	out := make([]string, 0, len(byLower))
	for _, p := range byLower {
		out = append(out, p)
	}

	slices.Sort(out)

	return out
}

// DiscoverDatasets returns sorted dataset IDs for project from in-memory tables,
// disk cache, cached dataset listings, and (when BigQueryAPIEnabled is true)
// a bounded live BigQuery REST v2 datasets.list call.
func (s *Store) DiscoverDatasets(ctx context.Context, project string, maxWait time.Duration) (datasets []string, isIncomplete bool) {
	return s.DiscoverDatasetsMatching(ctx, project, "", maxWait)
}

// DiscoverDatasetsMatching behaves like DiscoverDatasets, using cached listings
// for positive matches while re-querying BigQuery externally whenever prefix is
// non-empty and has no positive match in the cache.
func (s *Store) DiscoverDatasetsMatching(
	ctx context.Context,
	project, prefix string,
	maxWait time.Duration,
) (datasets []string, isIncomplete bool) {
	if project == "" {
		return nil, false
	}

	seen := make(map[string]struct{})
	for _, ds := range s.ListDatasets(project) {
		seen[ds] = struct{}{}
	}

	for _, ds := range s.diskCachedDatasets(project) {
		seen[ds] = struct{}{}
	}

	cached, hadCached := s.getCachedDatasetList(project)
	for _, ds := range cached {
		seen[ds] = struct{}{}
	}

	if hadCached && (prefix == "" || hasDatasetPrefixMatch(seen, prefix)) {
		return sortedKeys(seen), false
	}

	if s.opts.BigQueryAPIEnabled && maxWait > 0 && (!hadCached || s.allowLiveListRecheck("ds:"+strings.ToLower(project))) {
		fetched, incomplete := s.fetchDatasetList(ctx, project, maxWait)
		isIncomplete = incomplete
		for _, ds := range fetched {
			seen[ds] = struct{}{}
		}
	}

	return sortedKeys(seen), isIncomplete
}

// DiscoverTables returns sorted TableSummary items for (project, dataset) from
// in-memory tables, disk cache, cached table listings, and (when BigQueryAPIEnabled
// is true) a bounded live BigQuery REST v2 tables.list call.
func (s *Store) DiscoverTables(ctx context.Context, project, dataset string, maxWait time.Duration) (tables []TableSummary, isIncomplete bool) {
	return s.DiscoverTablesMatching(ctx, project, dataset, "", maxWait)
}

// DiscoverTablesMatching behaves like DiscoverTables, using cached listings for
// positive matches while re-querying BigQuery externally whenever prefix is
// non-empty and has no positive match in the cache.
func (s *Store) DiscoverTablesMatching(
	ctx context.Context,
	project, dataset, prefix string,
	maxWait time.Duration,
) (tables []TableSummary, isIncomplete bool) {
	if project == "" || dataset == "" {
		return nil, false
	}

	byName := make(map[string]TableSummary)
	for _, meta := range s.ListTables(project, dataset) {
		if meta == nil || meta.Ref.Table == "" {
			continue
		}

		byName[strings.ToLower(meta.Ref.Table)] = TableSummary{
			Ref:          meta.Ref,
			Type:         meta.Type,
			FriendlyName: meta.FriendlyName,
		}
	}

	if s.disk != nil {
		mergeTableSummaries(byName, s.disk.listTableSummaries(project, dataset, s.now()))
	}

	cached, hadCached := s.getCachedTableList(project, dataset)
	if hadCached {
		mergeTableSummaries(byName, cached)
		if prefix == "" || hasTablePrefixMatch(byName, prefix) {
			return sortedTableSummaries(byName), false
		}
	}

	recheckKey := "tbl:" + strings.ToLower(project+"."+dataset)
	if s.opts.BigQueryAPIEnabled && maxWait > 0 && (!hadCached || s.allowLiveListRecheck(recheckKey)) {
		fetched, incomplete := s.fetchTableList(ctx, project, dataset, maxWait)
		isIncomplete = incomplete
		mergeTableSummaries(byName, fetched)
	}

	return sortedTableSummaries(byName), isIncomplete
}

func hasDatasetPrefixMatch(seen map[string]struct{}, prefix string) bool {
	lowerPrefix := strings.ToLower(prefix)
	for ds := range seen {
		if strings.HasPrefix(strings.ToLower(ds), lowerPrefix) {
			return true
		}
	}

	return false
}

func hasTablePrefixMatch(byName map[string]TableSummary, prefix string) bool {
	lowerPrefix := strings.ToLower(prefix)
	for lowerTbl := range byName {
		if strings.HasPrefix(lowerTbl, lowerPrefix) {
			return true
		}
	}

	return false
}

func (s *Store) allowLiveListRecheck(key string) bool {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if last, ok := s.liveRecheckAt[key]; ok && now.Sub(last) < liveListRecheckCooldown {
		return false
	}

	s.liveRecheckAt[key] = now

	return true
}

func (s *Store) diskCachedDatasets(project string) []string {
	if s.disk == nil || project == "" {
		return nil
	}

	return s.disk.listDatasets(project, s.now())
}

func (s *Store) getCachedDatasetList(project string) ([]string, bool) {
	key := strings.ToLower(project)
	ttl := s.ttlForProject(project)
	now := s.now()

	s.mu.RLock()
	entry, ok := s.datasetLists[key]
	s.mu.RUnlock()

	if ok && len(entry.datasets) > 0 && (ttl <= 0 || now.Sub(entry.loadedAt) <= ttl) {
		return slices.Clone(entry.datasets), true
	}

	if s.disk != nil {
		if datasets, fetchedAt, loaded := s.disk.getDatasetList(project, now); loaded && len(datasets) > 0 {
			s.mu.Lock()
			s.datasetLists[key] = datasetListCacheEntry{datasets: datasets, loadedAt: fetchedAt}
			s.mu.Unlock()

			return slices.Clone(datasets), true
		}
	}

	return nil, false
}

func (s *Store) getCachedTableList(project, dataset string) ([]TableSummary, bool) {
	key := strings.ToLower(project + "." + dataset)
	ttl := s.ttlForProject(project)
	now := s.now()

	s.mu.RLock()
	entry, ok := s.tableLists[key]
	s.mu.RUnlock()

	if ok && len(entry.tables) > 0 && (ttl <= 0 || now.Sub(entry.loadedAt) <= ttl) {
		return slices.Clone(entry.tables), true
	}

	if s.disk != nil {
		if tables, fetchedAt, loaded := s.disk.getTableList(project, dataset, now); loaded && len(tables) > 0 {
			s.mu.Lock()
			s.tableLists[key] = tableListCacheEntry{tables: tables, loadedAt: fetchedAt}
			s.mu.Unlock()

			return slices.Clone(tables), true
		}
	}

	return nil, false
}

func (s *Store) fetchDatasetList(ctx context.Context, project string, maxWait time.Duration) (datasets []string, isIncomplete bool) {
	negKey := "list:datasets:" + strings.ToLower(project)
	if s.isInNegativeBackoff(negKey) {
		return nil, false
	}

	reqCtx, cancel := context.WithTimeout(ctx, maxWait)
	defer cancel()

	refs, err := s.rest.ListDatasets(reqCtx, project)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, true
		}

		if !errors.Is(err, context.Canceled) {
			s.logger.WarnContext(ctx, "bigquery datasets.list failed", slog.String("project", project), slog.Any("error", err))
			s.mu.Lock()
			s.recordNegativeLocked(negKey)
			s.mu.Unlock()
		}

		return nil, false
	}

	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if r.Dataset != "" {
			out = append(out, r.Dataset)
		}
	}
	slices.Sort(out)

	key := strings.ToLower(project)
	now := s.now()

	s.mu.Lock()
	delete(s.negCache, negKey)
	if len(out) > 0 {
		s.datasetLists[key] = datasetListCacheEntry{datasets: out, loadedAt: now}
	} else {
		delete(s.datasetLists, key)
	}
	s.mu.Unlock()

	if s.disk != nil && len(out) > 0 {
		s.disk.putDatasetList(project, out, now)
	}

	return slices.Clone(out), false
}

func (s *Store) fetchTableList(ctx context.Context, project, dataset string, maxWait time.Duration) (tables []TableSummary, isIncomplete bool) {
	negKey := "list:tables:" + strings.ToLower(project+"."+dataset)
	if s.isInNegativeBackoff(negKey) {
		return nil, false
	}

	reqCtx, cancel := context.WithTimeout(ctx, maxWait)
	defer cancel()

	fetched, err := s.rest.ListTables(reqCtx, project, dataset)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, true
		}

		if !errors.Is(err, context.Canceled) {
			s.logger.WarnContext(ctx, "bigquery tables.list failed", slog.String("project", project), slog.String("dataset", dataset), slog.Any("error", err))
			s.mu.Lock()
			s.recordNegativeLocked(negKey)
			s.mu.Unlock()
		}

		return nil, false
	}

	key := strings.ToLower(project + "." + dataset)
	now := s.now()

	s.mu.Lock()
	delete(s.negCache, negKey)
	if len(fetched) > 0 {
		s.tableLists[key] = tableListCacheEntry{tables: fetched, loadedAt: now}
	} else {
		delete(s.tableLists, key)
	}
	s.mu.Unlock()

	if s.disk != nil && len(fetched) > 0 {
		s.disk.putTableList(project, dataset, fetched, now)
	}

	return slices.Clone(fetched), false
}

func (s *Store) isInNegativeBackoff(key string) bool {
	now := s.now()

	s.mu.RLock()
	neg, ok := s.negCache[key]
	s.mu.RUnlock()

	return ok && now.Before(neg.retryAt)
}

func sortedKeys(seen map[string]struct{}) []string {
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}

func mergeTableSummaries(byName map[string]TableSummary, list []TableSummary) {
	for _, ts := range list {
		if ts.Ref.Table == "" {
			continue
		}

		k := strings.ToLower(ts.Ref.Table)
		if existing, exists := byName[k]; exists {
			if existing.Type == "" && ts.Type != "" {
				existing.Type = ts.Type
			}
			if existing.FriendlyName == "" && ts.FriendlyName != "" {
				existing.FriendlyName = ts.FriendlyName
			}
			byName[k] = existing
			continue
		}

		byName[k] = ts
	}
}

func sortedTableSummaries(byName map[string]TableSummary) []TableSummary {
	out := make([]TableSummary, 0, len(byName))
	for _, ts := range byName {
		out = append(out, ts)
	}

	slices.SortFunc(out, func(a, b TableSummary) int {
		return strings.Compare(a.Ref.Table, b.Ref.Table)
	})

	return out
}
