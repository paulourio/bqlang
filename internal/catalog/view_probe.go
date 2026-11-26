package catalog

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	// defaultViewPartitionProbeThresholdBytes is the minimum Stage 1 baseline
	// scan size (100 GiB) required before Stage 3 binary-search probing runs.
	defaultViewPartitionProbeThresholdBytes int64 = 100 << 30
	maxViewProbeColumns                           = 32
	viewProbeQueueSize                            = 64
)

type viewProbeOutcomeKind uint8

const (
	viewProbeOutcomeNoop viewProbeOutcomeKind = iota
	viewProbeOutcomeDone
	viewProbeOutcomeRetry
	viewProbeOutcomeApply
)

type viewProbeOutcome struct {
	kind   viewProbeOutcomeKind
	mutate func(*TableMeta) bool
}

// WithViewProbeThresholdBytes overrides the minimum Stage 1 baseline scan size
// (default 100 GiB) required before Stage 3 binary-search view probing runs.
func WithViewProbeThresholdBytes(bytes int64) StoreOption {
	return func(s *Store) {
		if bytes >= 0 {
			s.viewProbeThresholdBytes = bytes
		}
	}
}

func (s *Store) enqueueViewProbeIfNeeded(meta *TableMeta) {
	if s == nil || meta == nil || !s.opts.BigQueryAPIEnabled {
		return
	}

	if meta.Type != TableTypeView || meta.PartitionProbed || meta.EffectivePartitioning() != nil {
		return
	}

	ref := meta.Ref.WithDefaults(s.opts.DefaultProject, s.opts.DefaultDataset)
	if !ref.IsComplete() {
		return
	}

	key := ref.NormalizedKey()
	now := s.now()
	if s.isViewProbeBlocked(key, now) {
		return
	}

	if s.EffectiveBillingProject() == "" {
		return
	}

	s.mu.Lock()
	if s.closed || s.isViewProbeBlockedLocked(key, now) {
		s.mu.Unlock()

		return
	}

	s.viewProbePending[key] = struct{}{}
	s.mu.Unlock()

	select {
	case s.viewProbeCh <- ref:
	default:
		s.mu.Lock()
		delete(s.viewProbePending, key)
		s.mu.Unlock()
	}
}

func (s *Store) isViewProbeBlocked(key string, now time.Time) bool {
	s.mu.RLock()
	blocked := s.closed || s.isViewProbeBlockedLocked(key, now)
	s.mu.RUnlock()

	return blocked
}

func (s *Store) isViewProbeBlockedLocked(key string, now time.Time) bool {
	if _, pending := s.viewProbePending[key]; pending {
		return true
	}

	if neg, hasNeg := s.viewProbeNeg[key]; hasNeg && now.Before(neg.retryAt) {
		return true
	}

	return false
}

func (s *Store) runViewProbeWorker() {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			return
		case ref := <-s.viewProbeCh:
			if s.ctx.Err() != nil {
				return
			}

			s.processViewProbe(ref)
		}
	}
}

func (s *Store) processViewProbe(ref TableRef) {
	outcome := s.executeViewProbe(ref)
	s.finishViewProbe(ref, outcome)
}

func (s *Store) executeViewProbe(ref TableRef) viewProbeOutcome {
	billingProject := s.EffectiveBillingProject()
	if billingProject == "" || s.ctx.Err() != nil {
		return viewProbeOutcome{kind: viewProbeOutcomeNoop}
	}

	entry, ok := s.getEntryFromMemory(ref)
	if !ok || entry.meta == nil || entry.static {
		return viewProbeOutcome{kind: viewProbeOutcomeNoop}
	}

	viewMeta := entry.meta.Clone()
	if viewMeta.Type != TableTypeView || viewMeta.PartitionProbed || viewMeta.EffectivePartitioning() != nil {
		return viewProbeOutcome{kind: viewProbeOutcomeNoop}
	}

	viewRef := viewMeta.Ref.WithDefaults(s.opts.DefaultProject, s.opts.DefaultDataset)
	if !viewRef.IsComplete() {
		return viewProbeOutcome{kind: viewProbeOutcomeNoop}
	}

	// Stage 1: Unfiltered SELECT * baseline dry-run (detects require_partition_filter = true).
	stage1Res, stage1Outcome, advance := s.runViewProbeStage1(billingProject, viewRef)
	if !advance {
		return stage1Outcome
	}

	// Stage 2: Single underlying base table inspection via tables.get.
	if stage2Outcome, handled := s.runViewProbeStage2(viewMeta, viewRef, stage1Res.ReferencedTables); handled {
		return stage2Outcome
	}

	// Stage 3: Size-threshold gate and Capacitor-safe SELECT * binary search.
	return s.runViewProbeStage3(billingProject, viewMeta, viewRef, stage1Res.TotalBytesProcessed)
}

func (s *Store) runViewProbeStage1(billingProject string, viewRef TableRef) (*DryRunResult, viewProbeOutcome, bool) {
	baselineSQL := buildViewProbeBaselineSQL(viewRef)
	res, err := s.rest.DryRunQuery(s.ctx, billingProject, "", baselineSQL)
	if s.ctx.Err() != nil {
		return nil, viewProbeOutcome{kind: viewProbeOutcomeNoop}, false
	}

	if err != nil {
		if errors.Is(err, ErrDryRunRejected) {
			if partErr, matched := ParseRequiredPartitionFilterError(cleanDryRunErrorText(err)); matched {
				cols := partErr.Columns

				return nil, viewProbeOutcome{
					kind: viewProbeOutcomeApply,
					mutate: func(meta *TableMeta) bool {
						return applyInferredPartition(meta, cols, false)
					},
				}, false
			}

			s.logger.Debug("view partition probe stage 1 rejected", slog.String("view", viewRef.FullName()), slog.Any("error", err))

			return nil, viewProbeOutcome{kind: viewProbeOutcomeDone}, false
		}

		s.logger.Debug("view partition probe stage 1 transient failure", slog.String("view", viewRef.FullName()), slog.Any("error", err))

		return nil, viewProbeOutcome{kind: viewProbeOutcomeRetry}, false
	}

	if res == nil {
		return nil, viewProbeOutcome{kind: viewProbeOutcomeRetry}, false
	}

	return res, viewProbeOutcome{}, true
}

func (s *Store) runViewProbeStage2(
	viewMeta *TableMeta,
	viewRef TableRef,
	refTables []TableRef,
) (viewProbeOutcome, bool) {
	if len(refTables) != 1 {
		return viewProbeOutcome{}, false
	}

	baseRef := refTables[0].WithDefaults(s.opts.DefaultProject, s.opts.DefaultDataset)
	if !baseRef.IsComplete() || baseRef.NormalizedKey() == viewRef.NormalizedKey() {
		return viewProbeOutcome{}, false
	}

	baseMeta, ok := s.resolveStage2BaseTable(baseRef)
	if s.ctx.Err() != nil {
		return viewProbeOutcome{kind: viewProbeOutcomeNoop}, true
	}

	if !ok || baseMeta == nil {
		return viewProbeOutcome{}, false
	}

	basePart := baseMeta.EffectivePartitioning()
	if basePart == nil {
		if baseMeta.Type != TableTypeView || baseMeta.PartitionProbed {
			return viewProbeOutcome{kind: viewProbeOutcomeDone}, true
		}

		return viewProbeOutcome{}, false
	}

	if basePart.Field == "" {
		return viewProbeOutcome{}, false
	}

	if _, hasCol := viewMeta.FindColumn(basePart.Field); !hasCol {
		return viewProbeOutcome{}, false
	}

	return viewProbeOutcome{
		kind: viewProbeOutcomeApply,
		mutate: func(meta *TableMeta) bool {
			return applyBaseTablePartitionToView(meta, baseMeta)
		},
	}, true
}

func (s *Store) resolveStage2BaseTable(baseRef TableRef) (*TableMeta, bool) {
	if entry, ok := s.getEntryFromMemory(baseRef); ok && entry.meta != nil {
		return entry.meta, true
	}

	if s.disk != nil && !isTempDataset(baseRef.Dataset) && s.ttlForProject(baseRef.Project) > 0 {
		if meta, fetchedAt, ok := s.disk.getTable(baseRef, s.now()); ok {
			s.mu.Lock()
			s.putLocked(meta, fetchedAt, false)
			s.mu.Unlock()

			return meta, true
		}
	}

	meta, err := s.rest.FetchTable(s.ctx, baseRef)
	if err != nil || meta == nil {
		return nil, false
	}

	meta = meta.Clone()
	meta.Normalize(baseRef)
	now := s.now()
	s.writeDiskCache(meta, now)

	s.mu.Lock()
	delete(s.negCache, baseRef.FullName())
	s.putLocked(meta, now, false)
	s.mu.Unlock()

	return meta, true
}

func (s *Store) runViewProbeStage3(
	billingProject string,
	viewMeta *TableMeta,
	viewRef TableRef,
	baselineBytes int64,
) viewProbeOutcome {
	if s.ctx.Err() != nil {
		return viewProbeOutcome{kind: viewProbeOutcomeNoop}
	}

	if baselineBytes <= 0 || baselineBytes < s.viewProbeThresholdBytes {
		return viewProbeOutcome{kind: viewProbeOutcomeDone}
	}

	candidates := viewProbeCandidateColumns(viewMeta.Schema)
	if len(candidates) == 0 {
		return viewProbeOutcome{kind: viewProbeOutcomeDone}
	}

	probeYear := s.now().UTC().Year() + 2
	initSQL := buildViewProbeFilterSQL(viewRef, candidates, probeYear)
	initRes, err := s.rest.DryRunQuery(s.ctx, billingProject, "", initSQL)
	if s.ctx.Err() != nil {
		return viewProbeOutcome{kind: viewProbeOutcomeNoop}
	}

	if err != nil || initRes == nil || initRes.TotalBytesProcessed > baselineBytes {
		return viewProbeOutcome{kind: viewProbeOutcomeRetry}
	}

	if initRes.TotalBytesProcessed == baselineBytes {
		return viewProbeOutcome{kind: viewProbeOutcomeDone}
	}

	if len(candidates) == 1 {
		colName := candidates[0].Name

		return viewProbeOutcome{
			kind: viewProbeOutcomeApply,
			mutate: func(meta *TableMeta) bool {
				return applyOptionalTimePartition(meta, colName)
			},
		}
	}

	return s.binarySearchViewPartitionColumn(billingProject, viewRef, candidates, baselineBytes, probeYear)
}

func (s *Store) binarySearchViewPartitionColumn(
	billingProject string,
	viewRef TableRef,
	candidates []ColumnMeta,
	baselineBytes int64,
	probeYear int,
) viewProbeOutcome {
	for len(candidates) > 1 {
		if s.ctx.Err() != nil {
			return viewProbeOutcome{kind: viewProbeOutcomeNoop}
		}

		mid := len(candidates) / 2
		left := candidates[:mid]
		right := candidates[mid:]

		leftSQL := buildViewProbeFilterSQL(viewRef, left, probeYear)
		leftRes, err := s.rest.DryRunQuery(s.ctx, billingProject, "", leftSQL)
		if s.ctx.Err() != nil {
			return viewProbeOutcome{kind: viewProbeOutcomeNoop}
		}

		if err != nil || leftRes == nil || leftRes.TotalBytesProcessed > baselineBytes {
			return viewProbeOutcome{kind: viewProbeOutcomeRetry}
		}

		if leftRes.TotalBytesProcessed < baselineBytes {
			candidates = left
		} else {
			candidates = right
		}
	}

	colName := candidates[0].Name

	return viewProbeOutcome{
		kind: viewProbeOutcomeApply,
		mutate: func(meta *TableMeta) bool {
			return applyOptionalTimePartition(meta, colName)
		},
	}
}

func (s *Store) finishViewProbe(ref TableRef, outcome viewProbeOutcome) {
	key := ref.NormalizedKey()
	now := s.now()

	switch outcome.kind {
	case viewProbeOutcomeNoop:
		s.mu.Lock()
		delete(s.viewProbePending, key)
		s.mu.Unlock()
	case viewProbeOutcomeRetry:
		s.mu.Lock()
		delete(s.viewProbePending, key)
		s.recordViewProbeNegativeLocked(key, now)
		s.mu.Unlock()
	case viewProbeOutcomeDone:
		s.commitViewProbePartition(ref, key, now, nil)
	case viewProbeOutcomeApply:
		s.commitViewProbePartition(ref, key, now, outcome.mutate)
	}
}

func (s *Store) commitViewProbePartition(
	ref TableRef,
	key string,
	now time.Time,
	mutate func(*TableMeta) bool,
) {
	var (
		diskMeta      *TableMeta
		diskFetchedAt time.Time
		changed       bool
		cb            func(TableRef)
		closed        bool
	)

	s.mu.Lock()
	delete(s.viewProbePending, key)
	delete(s.viewProbeNeg, key)

	entry, exactKey, ok := s.getEntryFromMemoryLocked(ref, now)
	if ok && entry.meta != nil && !entry.static {
		cloned := entry.meta.Clone()
		if mutate != nil {
			changed = mutate(cloned)
		}

		if !cloned.PartitionProbed {
			cloned.PartitionProbed = true
		}

		if changed || !entry.meta.PartitionProbed {
			entry.meta = cloned
			s.mem[exactKey] = entry
			s.memLower[strings.ToLower(exactKey)] = exactKey
			diskMeta = cloned
			diskFetchedAt = entry.fetchedAt
		}
	}

	cb = s.onUpdate
	closed = s.closed
	s.mu.Unlock()

	if diskMeta != nil {
		s.writeDiskCache(diskMeta, diskFetchedAt)
	}

	if changed && diskMeta != nil && !closed && s.ctx.Err() == nil && cb != nil {
		cb(diskMeta.Ref)
	}
}

func (s *Store) recordViewProbeNegativeLocked(key string, now time.Time) {
	entry := s.viewProbeNeg[key]
	shift := min(entry.failures, 4)
	backoff := min(baseNegBackoff<<shift, maxNegBackoff)
	entry.failures++
	entry.retryAt = now.Add(backoff)
	s.viewProbeNeg[key] = entry
}

func viewProbeCandidateColumns(schema []ColumnMeta) []ColumnMeta {
	if len(schema) == 0 {
		return nil
	}

	candidates := make([]ColumnMeta, 0, min(len(schema), maxViewProbeColumns))
	for _, col := range schema {
		if col.Name == "" || col.EffectiveMode() == ModeRepeated {
			continue
		}

		switch normalizeTypeName(col.Type) {
		case "DATE", "DATETIME", "TIMESTAMP":
			candidates = append(candidates, col)
			if len(candidates) == maxViewProbeColumns {
				return candidates
			}
		}
	}

	return candidates
}

func buildViewProbeBaselineSQL(ref TableRef) string {
	//nolint:unqueryvet // BigQuery Capacitor column-invariance requires SELECT * for baseline and partition-pruning dry-run probes
	return "SELECT * FROM " + quoteBQTableRef(ref)
}

func buildViewProbeFilterSQL(ref TableRef, candidates []ColumnMeta, probeYear int) string {
	preds := make([]string, 0, len(candidates))
	for _, col := range candidates {
		if pred := buildViewProbePredicate(col, probeYear); pred != "" {
			preds = append(preds, pred)
		}
	}

	//nolint:unqueryvet // BigQuery Capacitor column-invariance requires SELECT * for baseline and partition-pruning dry-run probes
	return "SELECT * FROM " + quoteBQTableRef(ref) + " WHERE " + strings.Join(preds, " AND ")
}

func buildViewProbePredicate(col ColumnMeta, probeYear int) string {
	quoted := quoteBQIdent(col.Name)
	switch normalizeTypeName(col.Type) {
	case "DATE":
		return fmt.Sprintf("%s = DATE '%04d-06-15'", quoted, probeYear)
	case "DATETIME":
		return fmt.Sprintf("%s = DATETIME '%04d-06-15 00:00:00'", quoted, probeYear)
	case "TIMESTAMP":
		return fmt.Sprintf("%s = TIMESTAMP '%04d-06-15 00:00:00 UTC'", quoted, probeYear)
	default:
		return ""
	}
}

func quoteBQTableRef(ref TableRef) string {
	return quoteBQIdent(ref.Project) + "." + quoteBQIdent(ref.Dataset) + "." + quoteBQIdent(ref.Table)
}

func quoteBQIdent(ident string) string {
	escaped := strings.ReplaceAll(ident, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, "`", "\\`")

	return "`" + escaped + "`"
}
