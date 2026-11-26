package catalog

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // Pure-Go (CGo-free) SQLite driver for multi-process WAL disk cache.

	"github.com/paulourio/bqlang/bqconfig"
)

// DiskCacheFilename is the SQLite database file stored inside CacheDir.
const DiskCacheFilename = "cache.db"

const (
	sqliteDriverName          = "sqlite"
	diskCacheOpTimeout        = 5 * time.Second
	maxCachePayloadBytes      = 32 << 20 // 32 MiB safety cap against corrupt gzip payloads.
	defaultDisabledPruneFloor = 24 * time.Hour
	pruneTimeInterval         = 15 * time.Minute
	pruneWriteInterval        = 64
	gzipMagicByte0            = 0x1f
	gzipMagicByte1            = 0x8b
)

const diskCacheSchemaSQL = `
CREATE TABLE IF NOT EXISTS table_cache (
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
CREATE INDEX IF NOT EXISTS idx_table_cache_proj_ds
	ON table_cache (project_lower, dataset_lower);
CREATE INDEX IF NOT EXISTS idx_table_cache_fetched
	ON table_cache (fetched_at);

CREATE TABLE IF NOT EXISTS dataset_list_cache (
	project_lower TEXT PRIMARY KEY,
	project       TEXT NOT NULL,
	payload       BLOB NOT NULL,
	fetched_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS table_list_cache (
	project_lower TEXT NOT NULL,
	dataset_lower TEXT NOT NULL,
	project       TEXT NOT NULL,
	dataset       TEXT NOT NULL,
	payload       BLOB NOT NULL,
	fetched_at    INTEGER NOT NULL,
	PRIMARY KEY (project_lower, dataset_lower)
);
`

var errDiskCacheClosed = errors.New("disk cache closed")

type cacheTTLPolicy struct {
	baseTTL     time.Duration
	projectTTLs map[string]time.Duration
}

func buildCacheTTLPolicy(opts *bqconfig.LSPOptions) cacheTTLPolicy {
	if opts == nil {
		return cacheTTLPolicy{baseTTL: defaultDisabledPruneFloor}
	}

	base := max(time.Duration(0), time.Duration(opts.CacheTTLSeconds)*time.Second)
	var proj map[string]time.Duration

	for rawProj, sec := range opts.ProjectCacheTTLSeconds {
		key := strings.ToLower(strings.TrimSpace(rawProj))
		if key == "" || sec <= 0 {
			continue
		}

		dur := max(base, time.Duration(sec)*time.Second)
		if proj == nil {
			proj = make(map[string]time.Duration, len(opts.ProjectCacheTTLSeconds))
		}

		if cur, exists := proj[key]; !exists || dur > cur {
			proj[key] = dur
		}
	}

	return cacheTTLPolicy{
		baseTTL:     base,
		projectTTLs: proj,
	}
}

func (p cacheTTLPolicy) ttlForProject(project string) time.Duration {
	if proj := strings.ToLower(strings.TrimSpace(project)); proj != "" && len(p.projectTTLs) > 0 {
		if ttl, ok := p.projectTTLs[proj]; ok {
			return ttl
		}
	}

	return p.baseTTL
}

func (p cacheTTLPolicy) basePruneTTL() time.Duration {
	if p.baseTTL > 0 {
		return p.baseTTL
	}

	return defaultDisabledPruneFloor
}

func isTempDataset(dataset string) bool {
	return strings.HasPrefix(strings.TrimSpace(dataset), "_")
}

func filterPersistentDatasets(datasets []string) []string {
	if len(datasets) == 0 {
		return nil
	}

	out := make([]string, 0, len(datasets))
	for _, ds := range datasets {
		trimmed := strings.TrimSpace(ds)
		if trimmed == "" || isTempDataset(trimmed) {
			continue
		}

		out = append(out, trimmed)
	}

	return out
}

// diskCache provides multi-process-safe persistent caching backed by a single
// pure-Go SQLite database in WAL mode. Keeping one long-lived file handle per
// Store avoids per-file create/rename/readdir churn under corporate EDR/AV/DLP
// hooks while safely supporting concurrent IDE instances.
type diskCache struct {
	dir    string
	policy cacheTTLPolicy
	logger *slog.Logger

	mu                sync.RWMutex
	db                *sql.DB
	closed            bool
	writes            atomic.Uint64
	lastPruneUnixNano atomic.Int64
}

func newDiskCache(dir string, policy cacheTTLPolicy, logger *slog.Logger) *diskCache {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	return &diskCache{
		dir:    filepath.Clean(dir),
		policy: policy,
		logger: logger,
	}
}

func (c *diskCache) path() string {
	return filepath.Join(c.dir, DiskCacheFilename)
}

func (c *diskCache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	c.closed = true
	if c.db == nil {
		return nil
	}

	err := c.db.Close()
	c.db = nil

	return err
}

// acquireDB returns the open *sql.DB while holding c.mu.RLock so Close() waits
// for in-flight operations to finish. When create is false and the database
// file does not exist yet, it returns os.ErrNotExist without creating files.
// Callers must invoke release when done.
func (c *diskCache) acquireDB(create bool) (db *sql.DB, release func(), err error) {
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()

		return nil, nil, errDiskCacheClosed
	}

	if c.db != nil {
		return c.db, c.mu.RUnlock, nil
	}
	c.mu.RUnlock()

	if !create {
		if _, statErr := os.Stat(c.path()); statErr != nil {
			return nil, nil, statErr
		}
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()

		return nil, nil, errDiskCacheClosed
	}

	if c.db == nil {
		opened, openErr := c.openDBLocked()
		if openErr != nil {
			c.mu.Unlock()

			return nil, nil, openErr
		}

		c.db = opened
	}
	c.mu.Unlock()

	c.mu.RLock()
	if c.closed || c.db == nil {
		c.mu.RUnlock()

		return nil, nil, errDiskCacheClosed
	}

	return c.db, c.mu.RUnlock, nil
}

func (c *diskCache) openDBLocked() (*sql.DB, error) {
	if err := os.MkdirAll(c.dir, dirPerm); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}

	dbPath := c.path()
	db, err := c.openAndInitSQLite(dbPath)
	if err == nil {
		return db, nil
	}

	if !isCorruptSQLiteErr(err) {
		return nil, err
	}

	c.logger.Warn("recreating corrupt disk cache database", slog.String("path", dbPath), slog.Any("error", err))
	removeSQLiteFiles(dbPath)

	return c.openAndInitSQLite(dbPath)
}

func (c *diskCache) openAndInitSQLite(dbPath string) (*sql.DB, error) {
	createdEmpty := false
	if info, statErr := os.Stat(dbPath); statErr == nil && info.Size() == 0 {
		createdEmpty = true
	}

	// Pre-create with 0600 atomically using O_EXCL so we never open/close an
	// already-existing SQLite file (preserving POSIX fcntl locks on Linux).
	if f, err := os.OpenFile(filepath.Clean(dbPath), os.O_CREATE|os.O_EXCL|os.O_WRONLY, cacheFilePerm); err == nil {
		createdEmpty = true
		_ = f.Close()
	}

	db, err := sql.Open(sqliteDriverName, sqliteCacheDSN(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open sqlite cache: %w", err)
	}

	// One persistent connection per process avoids intra-process lock contention
	// and avoids churning file handles under endpoint security agents.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	ctx, cancel := context.WithTimeout(context.Background(), diskCacheOpTimeout)
	defer cancel()

	if _, err := db.ExecContext(ctx, diskCacheSchemaSQL); err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("init sqlite cache schema: %w", err)
	}

	if createdEmpty {
		c.lastPruneUnixNano.CompareAndSwap(0, time.Now().UnixNano())
	}

	return db, nil
}

func (c *diskCache) resetIfCorrupt(badDB *sql.DB, err error) {
	if !isCorruptSQLiteErr(err) {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.db != badDB {
		return
	}

	c.logger.Warn("resetting corrupt disk cache database", slog.String("path", c.path()), slog.Any("error", err))
	_ = c.db.Close()
	c.db = nil
	removeSQLiteFiles(c.path())
}

func (c *diskCache) getTable(ref TableRef, now time.Time) (*TableMeta, time.Time, bool) {
	ttl := c.policy.ttlForProject(ref.Project)
	if ttl <= 0 || !ref.IsComplete() || isTempDataset(ref.Dataset) {
		return nil, time.Time{}, false
	}

	db, release, err := c.acquireDB(false)
	if err != nil {
		return nil, time.Time{}, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), diskCacheOpTimeout)
	defer cancel()

	var (
		payload   []byte
		fetchedAt int64
	)

	queryErr := db.QueryRowContext(
		ctx,
		`SELECT payload, fetched_at
		   FROM table_cache
		  WHERE project_lower = ? AND dataset_lower = ? AND table_lower = ?`,
		strings.ToLower(ref.Project),
		strings.ToLower(ref.Dataset),
		strings.ToLower(ref.Table),
	).Scan(&payload, &fetchedAt)

	if queryErr != nil {
		release()
		if !errors.Is(queryErr, sql.ErrNoRows) {
			c.resetIfCorrupt(db, queryErr)
		}

		return nil, time.Time{}, false
	}

	if isCacheExpired(now, fetchedAt, ttl) {
		release()

		return nil, time.Time{}, false
	}

	meta, ok := c.decodeCachedTablePayload(ctx, db, ref, payload)
	release()
	if !ok {
		return nil, time.Time{}, false
	}

	return meta, time.Unix(0, fetchedAt), true
}

func (c *diskCache) decodeCachedTablePayload(
	ctx context.Context,
	db *sql.DB,
	ref TableRef,
	payload []byte,
) (*TableMeta, bool) {
	raw, decErr := decompressPayload(payload)
	if decErr != nil {
		c.deleteTableRow(ctx, db, ref)
		c.logger.DebugContext(ctx, "ignoring corrupt disk cache entry", slog.String("table", ref.FullName()), slog.Any("error", decErr))

		return nil, false
	}

	meta, metaErr := DecodeTableJSON(raw, ref)
	if metaErr != nil {
		c.deleteTableRow(ctx, db, ref)
		c.logger.DebugContext(ctx, "ignoring corrupt disk cache entry", slog.String("table", ref.FullName()), slog.Any("error", metaErr))

		return nil, false
	}

	return meta, true
}

func (c *diskCache) putTable(meta *TableMeta, now time.Time) {
	if meta == nil || !meta.Ref.IsComplete() || isTempDataset(meta.Ref.Dataset) {
		return
	}

	if c.policy.ttlForProject(meta.Ref.Project) <= 0 {
		return
	}

	raw, err := meta.MarshalJSON()
	if err != nil {
		return
	}

	payload, err := compressPayload(raw)
	if err != nil {
		return
	}

	db, release, err := c.acquireDB(true)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), diskCacheOpTimeout)
	defer cancel()

	_, execErr := db.ExecContext(
		ctx,
		`INSERT INTO table_cache (
			project_lower, dataset_lower, table_lower,
			project, dataset, table_name, table_type, friendly_name,
			payload, fetched_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_lower, dataset_lower, table_lower) DO UPDATE SET
			project       = excluded.project,
			dataset       = excluded.dataset,
			table_name    = excluded.table_name,
			table_type    = excluded.table_type,
			friendly_name = excluded.friendly_name,
			payload       = excluded.payload,
			fetched_at    = excluded.fetched_at`,
		strings.ToLower(meta.Ref.Project),
		strings.ToLower(meta.Ref.Dataset),
		strings.ToLower(meta.Ref.Table),
		meta.Ref.Project,
		meta.Ref.Dataset,
		meta.Ref.Table,
		string(meta.Type),
		meta.FriendlyName,
		payload,
		now.UnixNano(),
	)

	if execErr == nil {
		c.maybePruneOnWrite(ctx, db, now)
		release()

		return
	}

	release()
	c.resetIfCorrupt(db, execErr)
}

func (c *diskCache) listProjects(now time.Time) []string {
	db, release, err := c.acquireDB(false)
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), diskCacheOpTimeout)
	defer cancel()

	out, queryErr := queryDiskProjects(ctx, db, now, c.policy)
	release()
	if queryErr != nil {
		c.resetIfCorrupt(db, queryErr)

		return nil
	}

	return out
}

func queryDiskProjects(ctx context.Context, db *sql.DB, now time.Time, policy cacheTTLPolicy) ([]string, error) {
	rows, err := db.QueryContext(
		ctx,
		`SELECT project, MAX(fetched_at)
		   FROM (
		       SELECT project_lower, project, fetched_at
		         FROM table_cache
		        WHERE dataset_lower NOT LIKE '\_%' ESCAPE '\'
		       UNION ALL
		       SELECT project_lower, project, fetched_at
		         FROM dataset_list_cache
		   )
		  GROUP BY project_lower`,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var (
			project   string
			fetchedAt int64
		)
		if scanErr := rows.Scan(&project, &fetchedAt); scanErr != nil || project == "" {
			continue
		}

		ttl := policy.ttlForProject(project)
		if !isCacheExpired(now, fetchedAt, ttl) {
			out = append(out, project)
		}
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

func (c *diskCache) listDatasets(project string, now time.Time) []string {
	ttl := c.policy.ttlForProject(project)
	if project == "" || ttl <= 0 {
		return nil
	}

	db, release, err := c.acquireDB(false)
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), diskCacheOpTimeout)
	defer cancel()

	cutoff := now.Add(-ttl).UnixNano()
	out, queryErr := queryDiskDatasets(ctx, db, project, cutoff)
	release()
	if queryErr != nil {
		c.resetIfCorrupt(db, queryErr)

		return nil
	}

	return out
}

func queryDiskDatasets(ctx context.Context, db *sql.DB, project string, cutoff int64) ([]string, error) {
	rows, err := db.QueryContext(
		ctx,
		`SELECT DISTINCT dataset
		   FROM table_cache
		  WHERE project_lower = ?
		    AND fetched_at >= ?
		    AND dataset_lower NOT LIKE '\_%' ESCAPE '\'`,
		strings.ToLower(project),
		cutoff,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var ds string
		if scanErr := rows.Scan(&ds); scanErr == nil && ds != "" && !isTempDataset(ds) {
			out = append(out, ds)
		}
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

func (c *diskCache) listTableSummaries(project, dataset string, now time.Time) []TableSummary {
	ttl := c.policy.ttlForProject(project)
	if project == "" || dataset == "" || isTempDataset(dataset) || ttl <= 0 {
		return nil
	}

	db, release, err := c.acquireDB(false)
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), diskCacheOpTimeout)
	defer cancel()

	cutoff := now.Add(-ttl).UnixNano()
	out, queryErr := queryDiskTableSummaries(ctx, db, project, dataset, cutoff)
	release()
	if queryErr != nil {
		c.resetIfCorrupt(db, queryErr)

		return nil
	}

	return out
}

func queryDiskTableSummaries(ctx context.Context, db *sql.DB, project, dataset string, cutoff int64) ([]TableSummary, error) {
	rows, err := db.QueryContext(
		ctx,
		`SELECT project, dataset, table_name, table_type, friendly_name
		   FROM table_cache
		  WHERE project_lower = ?
		    AND dataset_lower = ?
		    AND fetched_at >= ?`,
		strings.ToLower(project),
		strings.ToLower(dataset),
		cutoff,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []TableSummary
	for rows.Next() {
		var proj, ds, tbl, typ, friendly string
		if scanErr := rows.Scan(&proj, &ds, &tbl, &typ, &friendly); scanErr != nil || tbl == "" {
			continue
		}

		tableType := TableType(typ)
		if tableType == "" {
			tableType = TableTypeTable
		}

		out = append(out, TableSummary{
			Ref:          TableRef{Project: proj, Dataset: ds, Table: tbl},
			Type:         tableType,
			FriendlyName: friendly,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

func (c *diskCache) matchWildcardTable(project, dataset, tablePrefix string, now time.Time) (*TableMeta, time.Time, bool) {
	summaries := c.listTableSummaries(project, dataset, now)
	if len(summaries) == 0 {
		return nil, time.Time{}, false
	}

	slices.SortFunc(summaries, func(a, b TableSummary) int {
		return strings.Compare(a.Ref.FullName(), b.Ref.FullName())
	})

	for _, ts := range summaries {
		if strings.HasPrefix(ts.Ref.Table, tablePrefix) {
			if meta, fetchedAt, ok := c.getTable(ts.Ref, now); ok {
				return meta, fetchedAt, true
			}
		}
	}

	lowerPrefix := strings.ToLower(tablePrefix)
	for _, ts := range summaries {
		if strings.HasPrefix(strings.ToLower(ts.Ref.Table), lowerPrefix) {
			if meta, fetchedAt, ok := c.getTable(ts.Ref, now); ok {
				return meta, fetchedAt, true
			}
		}
	}

	return nil, time.Time{}, false
}

func (c *diskCache) getDatasetList(project string, now time.Time) ([]string, time.Time, bool) {
	ttl := c.policy.ttlForProject(project)
	if ttl <= 0 || project == "" {
		return nil, time.Time{}, false
	}

	db, release, err := c.acquireDB(false)
	if err != nil {
		return nil, time.Time{}, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), diskCacheOpTimeout)
	defer cancel()

	var (
		payload   []byte
		fetchedAt int64
	)

	queryErr := db.QueryRowContext(
		ctx,
		`SELECT payload, fetched_at FROM dataset_list_cache WHERE project_lower = ?`,
		strings.ToLower(project),
	).Scan(&payload, &fetchedAt)

	if queryErr != nil {
		release()
		if !errors.Is(queryErr, sql.ErrNoRows) {
			c.resetIfCorrupt(db, queryErr)
		}

		return nil, time.Time{}, false
	}

	release()

	if isCacheExpired(now, fetchedAt, ttl) {
		return nil, time.Time{}, false
	}

	datasets, ok := decodeCachedDatasets(payload)
	if !ok || len(datasets) == 0 {
		return nil, time.Time{}, false
	}

	return datasets, time.Unix(0, fetchedAt), true
}

func decodeCachedDatasets(payload []byte) ([]string, bool) {
	raw, decErr := decompressPayload(payload)
	if decErr != nil {
		return nil, false
	}

	var datasets []string
	if err := json.Unmarshal(raw, &datasets); err != nil {
		return nil, false
	}

	return filterPersistentDatasets(datasets), true
}

func (c *diskCache) putDatasetList(project string, datasets []string, now time.Time) {
	clean := filterPersistentDatasets(datasets)
	if project == "" || len(clean) == 0 || c.policy.ttlForProject(project) <= 0 {
		return
	}

	raw, err := json.Marshal(clean)
	if err != nil {
		return
	}

	payload, err := compressPayload(raw)
	if err != nil {
		return
	}

	db, release, err := c.acquireDB(true)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), diskCacheOpTimeout)
	defer cancel()

	_, execErr := db.ExecContext(
		ctx,
		`INSERT INTO dataset_list_cache (project_lower, project, payload, fetched_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(project_lower) DO UPDATE SET
		     project    = excluded.project,
		     payload    = excluded.payload,
		     fetched_at = excluded.fetched_at`,
		strings.ToLower(project),
		project,
		payload,
		now.UnixNano(),
	)

	if execErr == nil {
		c.maybePruneOnWrite(ctx, db, now)
		release()

		return
	}

	release()
	c.resetIfCorrupt(db, execErr)
}

func (c *diskCache) getTableList(project, dataset string, now time.Time) ([]TableSummary, time.Time, bool) {
	ttl := c.policy.ttlForProject(project)
	if ttl <= 0 || project == "" || dataset == "" || isTempDataset(dataset) {
		return nil, time.Time{}, false
	}

	db, release, err := c.acquireDB(false)
	if err != nil {
		return nil, time.Time{}, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), diskCacheOpTimeout)
	defer cancel()

	var (
		payload   []byte
		fetchedAt int64
	)

	queryErr := db.QueryRowContext(
		ctx,
		`SELECT payload, fetched_at
		   FROM table_list_cache
		  WHERE project_lower = ? AND dataset_lower = ?`,
		strings.ToLower(project),
		strings.ToLower(dataset),
	).Scan(&payload, &fetchedAt)

	if queryErr != nil {
		release()
		if !errors.Is(queryErr, sql.ErrNoRows) {
			c.resetIfCorrupt(db, queryErr)
		}

		return nil, time.Time{}, false
	}

	release()

	if isCacheExpired(now, fetchedAt, ttl) {
		return nil, time.Time{}, false
	}

	tables, ok := decodeCachedTableSummaries(payload)
	if !ok || len(tables) == 0 {
		return nil, time.Time{}, false
	}

	return tables, time.Unix(0, fetchedAt), true
}

func decodeCachedTableSummaries(payload []byte) ([]TableSummary, bool) {
	raw, decErr := decompressPayload(payload)
	if decErr != nil {
		return nil, false
	}

	var tables []TableSummary
	if err := json.Unmarshal(raw, &tables); err != nil {
		return nil, false
	}

	return tables, true
}

func (c *diskCache) putTableList(project, dataset string, tables []TableSummary, now time.Time) {
	if project == "" || dataset == "" || isTempDataset(dataset) || len(tables) == 0 {
		return
	}

	if c.policy.ttlForProject(project) <= 0 {
		return
	}

	raw, err := json.Marshal(tables)
	if err != nil {
		return
	}

	payload, err := compressPayload(raw)
	if err != nil {
		return
	}

	db, release, err := c.acquireDB(true)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), diskCacheOpTimeout)
	defer cancel()

	_, execErr := db.ExecContext(
		ctx,
		`INSERT INTO table_list_cache (
			project_lower, dataset_lower, project, dataset, payload, fetched_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_lower, dataset_lower) DO UPDATE SET
			project    = excluded.project,
			dataset    = excluded.dataset,
			payload    = excluded.payload,
			fetched_at = excluded.fetched_at`,
		strings.ToLower(project),
		strings.ToLower(dataset),
		project,
		dataset,
		payload,
		now.UnixNano(),
	)

	if execErr == nil {
		c.maybePruneOnWrite(ctx, db, now)
		release()

		return
	}

	release()
	c.resetIfCorrupt(db, execErr)
}

func (c *diskCache) deleteTableRow(ctx context.Context, db *sql.DB, ref TableRef) {
	_, _ = db.ExecContext(
		ctx,
		`DELETE FROM table_cache WHERE project_lower = ? AND dataset_lower = ? AND table_lower = ?`,
		strings.ToLower(ref.Project),
		strings.ToLower(ref.Dataset),
		strings.ToLower(ref.Table),
	)
}

func (c *diskCache) shouldPruneByTime(now time.Time) bool {
	nowNano := now.UnixNano()
	prev := c.lastPruneUnixNano.Load()
	if prev != 0 && nowNano-prev < int64(pruneTimeInterval) {
		return false
	}

	return c.lastPruneUnixNano.CompareAndSwap(prev, nowNano)
}

func (c *diskCache) maybePruneOnWrite(ctx context.Context, db *sql.DB, now time.Time) {
	byWrites := c.writes.Add(1)%pruneWriteInterval == 0
	byTime := c.shouldPruneByTime(now)
	if !byWrites && !byTime {
		return
	}

	if byWrites && !byTime {
		c.lastPruneUnixNano.Store(now.UnixNano())
	}

	c.pruneExpired(ctx, db, now)
}

func (c *diskCache) pruneNow(now time.Time) {
	db, release, err := c.acquireDB(false)
	if err != nil {
		return
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), diskCacheOpTimeout)
	defer cancel()

	c.lastPruneUnixNano.Store(now.UnixNano())
	c.pruneExpired(ctx, db, now)
}

// pruneExpired removes temporary (_-prefixed) dataset entries and rows older
// than each project's effective TTL (or the base TTL) so cache.db stays bounded
// across sessions without evicting projects configured with extended TTLs.
func (c *diskCache) pruneExpired(ctx context.Context, db *sql.DB, now time.Time) {
	baseRetention := c.policy.basePruneTTL()
	baseCutoff := now.Add(-baseRetention).UnixNano()

	if len(c.policy.projectTTLs) == 0 {
		_, _ = db.ExecContext(
			ctx,
			`DELETE FROM table_cache WHERE dataset_lower LIKE '\_%' ESCAPE '\' OR fetched_at < ?`,
			baseCutoff,
		)
		_, _ = db.ExecContext(ctx, `DELETE FROM dataset_list_cache WHERE fetched_at < ?`, baseCutoff)
		_, _ = db.ExecContext(
			ctx,
			`DELETE FROM table_list_cache WHERE dataset_lower LIKE '\_%' ESCAPE '\' OR fetched_at < ?`,
			baseCutoff,
		)

		return
	}

	_, _ = db.ExecContext(
		ctx,
		`CREATE TEMP TABLE IF NOT EXISTS _prune_projects (
			project_lower TEXT PRIMARY KEY,
			cutoff_ns     INTEGER NOT NULL
		)`,
	)
	_, _ = db.ExecContext(ctx, `DELETE FROM _prune_projects`)

	projects := make([]string, 0, len(c.policy.projectTTLs))
	for proj := range c.policy.projectTTLs {
		projects = append(projects, proj)
	}
	slices.Sort(projects)

	for _, proj := range projects {
		retention := max(baseRetention, c.policy.projectTTLs[proj])
		projCutoff := now.Add(-retention).UnixNano()
		_, _ = db.ExecContext(
			ctx,
			`INSERT INTO _prune_projects (project_lower, cutoff_ns) VALUES (?, ?)`,
			proj,
			projCutoff,
		)
	}

	_, _ = db.ExecContext(
		ctx,
		`DELETE FROM table_cache
		  WHERE dataset_lower LIKE '\_%' ESCAPE '\'
		     OR fetched_at < COALESCE(
		          (SELECT cutoff_ns FROM _prune_projects p WHERE p.project_lower = table_cache.project_lower),
		          ?
		        )`,
		baseCutoff,
	)
	_, _ = db.ExecContext(
		ctx,
		`DELETE FROM dataset_list_cache
		  WHERE fetched_at < COALESCE(
		          (SELECT cutoff_ns FROM _prune_projects p WHERE p.project_lower = dataset_list_cache.project_lower),
		          ?
		        )`,
		baseCutoff,
	)
	_, _ = db.ExecContext(
		ctx,
		`DELETE FROM table_list_cache
		  WHERE dataset_lower LIKE '\_%' ESCAPE '\'
		     OR fetched_at < COALESCE(
		          (SELECT cutoff_ns FROM _prune_projects p WHERE p.project_lower = table_list_cache.project_lower),
		          ?
		        )`,
		baseCutoff,
	)
}

func sqliteCacheDSN(dbPath string) string {
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		absPath = filepath.Clean(dbPath)
	}

	slashPath := filepath.ToSlash(absPath)
	if !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}

	escaped := (&url.URL{Path: slashPath}).EscapedPath()

	return "file://" + escaped +
		"?_busy_timeout=5000" +
		"&_journal_mode=WAL" +
		"&_synchronous=NORMAL" +
		"&_foreign_keys=OFF" +
		"&_defensive=1" +
		"&_pragma=temp_store(MEMORY)"
}

func isCacheExpired(now time.Time, fetchedAt int64, ttl time.Duration) bool {
	if ttl <= 0 {
		return true
	}

	age := now.Sub(time.Unix(0, fetchedAt))

	return age > ttl
}

func compressPayload(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	gw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return nil, fmt.Errorf("create gzip writer: %w", err)
	}

	if _, err := gw.Write(raw); err != nil {
		_ = gw.Close()

		return nil, fmt.Errorf("write gzip payload: %w", err)
	}

	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("close gzip payload: %w", err)
	}

	return buf.Bytes(), nil
}

func decompressPayload(blob []byte) ([]byte, error) {
	if len(blob) >= 2 && blob[0] == gzipMagicByte0 && blob[1] == gzipMagicByte1 {
		gr, err := gzip.NewReader(bytes.NewReader(blob))
		if err != nil {
			return nil, fmt.Errorf("open gzip payload: %w", err)
		}
		defer func() { _ = gr.Close() }()

		out, err := io.ReadAll(io.LimitReader(gr, maxCachePayloadBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read gzip payload: %w", err)
		}

		if len(out) > maxCachePayloadBytes {
			return nil, fmt.Errorf("gzip payload exceeds %d bytes", maxCachePayloadBytes)
		}

		return out, nil
	}

	return blob, nil
}

func isCorruptSQLiteErr(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())

	return strings.Contains(msg, "not a database") ||
		strings.Contains(msg, "malformed") ||
		strings.Contains(msg, "corrupt")
}

func removeSQLiteFiles(dbPath string) {
	_ = os.Remove(dbPath)
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
}
