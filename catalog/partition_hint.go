package catalog

import (
	"regexp"
	"slices"
	"strings"
	"time"
)

const maxPendingPartitionHints = 64

// RequiredPartitionFilterError holds the table reference and partition column
// names extracted from a BigQuery "require partition filter" dry-run error.
type RequiredPartitionFilterError struct {
	Table   TableRef
	Columns []string
}

type pendingPartitionHint struct {
	info           RequiredPartitionFilterError
	referencedRefs []TableRef
	recordedAt     time.Time
}

type enrichedTableUpdate struct {
	meta      *TableMeta
	fetchedAt time.Time
	static    bool
}

var requiredPartitionFilterRe = regexp.MustCompile(
	`(?i)cannot query over table\s+['"` + "`" + `]([^'"` + "`" + `]+)['"` + "`" + `]\s+without a filter over column(?:\(s\))?\s+(.+?)\s+that can be used for partition elimination`,
)

// ParseRequiredPartitionFilterError parses a BigQuery dry-run rejection message
// caused by require_partition_filter = true and returns the referenced table
// and required partition column name(s).
func ParseRequiredPartitionFilterError(msg string) (RequiredPartitionFilterError, bool) {
	matches := requiredPartitionFilterRe.FindStringSubmatch(msg)
	if len(matches) < 3 {
		return RequiredPartitionFilterError{}, false
	}

	tblRef := parseErrorTableRef(matches[1])
	if tblRef.Table == "" {
		return RequiredPartitionFilterError{}, false
	}

	cols := parseErrorColumnList(matches[2])
	if len(cols) == 0 {
		return RequiredPartitionFilterError{}, false
	}

	return RequiredPartitionFilterError{
		Table:   tblRef,
		Columns: cols,
	}, true
}

func parseErrorTableRef(raw string) TableRef {
	cleaned := strings.Trim(strings.TrimSpace(raw), "`'\"")
	if cleaned == "" {
		return TableRef{}
	}

	if proj, rest, hasColon := strings.Cut(cleaned, ":"); hasColon {
		ds, tbl, hasDot := strings.Cut(rest, ".")
		if hasDot {
			return TableRef{
				Project: strings.TrimSpace(proj),
				Dataset: strings.TrimSpace(ds),
				Table:   strings.TrimSpace(tbl),
			}
		}

		return TableRef{
			Project: strings.TrimSpace(proj),
			Table:   strings.TrimSpace(rest),
		}
	}

	parts := strings.Split(cleaned, ".")
	switch len(parts) {
	case 3:
		return TableRef{
			Project: strings.TrimSpace(parts[0]),
			Dataset: strings.TrimSpace(parts[1]),
			Table:   strings.TrimSpace(parts[2]),
		}
	case 2:
		return TableRef{
			Dataset: strings.TrimSpace(parts[0]),
			Table:   strings.TrimSpace(parts[1]),
		}
	case 1:
		return TableRef{
			Table: strings.TrimSpace(parts[0]),
		}
	default:
		return TableRef{}
	}
}

func parseErrorColumnList(raw string) []string {
	parts := strings.Split(raw, ",")
	cols := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))

	for _, part := range parts {
		col := strings.Trim(strings.TrimSpace(part), "'\"`")
		if col == "" {
			continue
		}

		lower := strings.ToLower(col)
		if _, dup := seen[lower]; dup {
			continue
		}

		seen[lower] = struct{}{}
		cols = append(cols, col)
	}

	return cols
}

func isIngestionPartitionPseudoColumn(col string) bool {
	return strings.EqualFold(col, "_PARTITIONTIME") ||
		strings.EqualFold(col, "_PARTITIONDATE") ||
		strings.EqualFold(col, "_PARTITION_LOAD_TIME")
}

func splitPartitionErrorColumns(columns []string) (concrete []string, hasPseudo bool) {
	for _, col := range columns {
		if isIngestionPartitionPseudoColumn(col) {
			hasPseudo = true
		} else {
			concrete = append(concrete, col)
		}
	}

	return concrete, hasPseudo
}

func findMatchingSchemaColumn(meta *TableMeta, columns []string) (*ColumnMeta, bool) {
	for _, col := range columns {
		if found, ok := meta.FindColumn(col); ok {
			return found, true
		}
	}

	return nil, false
}

// applyInferredPartition updates meta in place with partition metadata inferred
// from a BigQuery require_partition_filter dry-run error. Callers must pass a
// cloned TableMeta. It returns true when meta was modified.
func applyInferredPartition(meta *TableMeta, columns []string, allowIngestionPseudo bool) bool {
	if meta == nil || len(columns) == 0 {
		return false
	}

	concreteCols, hasPseudo := splitPartitionErrorColumns(columns)
	if len(concreteCols) > 0 {
		return applyConcreteInferredPartition(meta, concreteCols)
	}

	if hasPseudo && allowIngestionPseudo && meta.Type != TableTypeView {
		return applyIngestionInferredPartition(meta)
	}

	return false
}

func applyConcreteInferredPartition(meta *TableMeta, concreteCols []string) bool {
	col, ok := findMatchingSchemaColumn(meta, concreteCols)
	if !ok {
		return false
	}

	if meta.EffectivePartitioning() != nil {
		if !meta.IsPartitionColumn(col.Name) || meta.EffectiveRequirePartitionFilter() {
			return false
		}

		meta.RequirePartitionFilter = true
		if meta.TimePartitioning != nil {
			meta.TimePartitioning.RequirePartitionFilter = true
		}
		if meta.Type == TableTypeView {
			meta.PartitionProbed = true
		}
		meta.Normalize(meta.Ref)

		return true
	}

	if strings.EqualFold(col.Type, "INT64") || strings.EqualFold(col.Type, "INTEGER") {
		meta.RangePartitioning = &RangePartitioning{
			Field: col.Name,
		}
	} else {
		meta.TimePartitioning = &TimePartitioning{
			Field:                  col.Name,
			RequirePartitionFilter: true,
		}
	}

	meta.RequirePartitionFilter = true
	if meta.Type == TableTypeView {
		meta.PartitionProbed = true
	}
	meta.Normalize(meta.Ref)

	return true
}

// applyBaseTablePartitionToView copies a single underlying base table's named
// time or integer-range partitioning onto a cloned view TableMeta with
// RequirePartitionFilter = false (since Stage 1 unfiltered SELECT * succeeded).
// Clustering metadata is intentionally NOT copied. Returns true if modified.
func applyBaseTablePartitionToView(viewMeta, baseMeta *TableMeta) bool {
	if viewMeta == nil || baseMeta == nil || viewMeta.EffectivePartitioning() != nil {
		return false
	}

	basePart := baseMeta.EffectivePartitioning()
	if basePart == nil || basePart.Field == "" {
		return false
	}

	viewCol, ok := viewMeta.FindColumn(basePart.Field)
	if !ok {
		return false
	}

	switch {
	case baseMeta.TimePartitioning != nil && baseMeta.TimePartitioning.Field != "":
		tp := *baseMeta.TimePartitioning
		tp.Field = viewCol.Name
		tp.RequirePartitionFilter = false
		viewMeta.TimePartitioning = &tp
		viewMeta.RangePartitioning = nil
		viewMeta.Partitioning = nil
	case baseMeta.RangePartitioning != nil && baseMeta.RangePartitioning.Field != "":
		rp := cloneRangePartitioning(baseMeta.RangePartitioning)
		rp.Field = viewCol.Name
		viewMeta.RangePartitioning = rp
		viewMeta.TimePartitioning = nil
		viewMeta.Partitioning = nil
	case basePart.Kind == PartitionKindRange:
		viewMeta.RangePartitioning = &RangePartitioning{
			Field: viewCol.Name,
			Range: cloneIntRange(basePart.Range),
		}
		viewMeta.TimePartitioning = nil
		viewMeta.Partitioning = nil
	default:
		viewMeta.TimePartitioning = &TimePartitioning{
			Type:                   basePart.Type,
			Field:                  viewCol.Name,
			ExpirationMs:           basePart.ExpirationMs,
			RequirePartitionFilter: false,
		}
		viewMeta.RangePartitioning = nil
		viewMeta.Partitioning = nil
	}

	viewMeta.RequirePartitionFilter = false
	viewMeta.PartitionProbed = true
	viewMeta.Normalize(viewMeta.Ref)

	return true
}

// applyOptionalTimePartition records an inferred optional time-partition column
// (RequirePartitionFilter = false) on a cloned TableMeta. Returns true if modified.
func applyOptionalTimePartition(meta *TableMeta, colName string) bool {
	if meta == nil || colName == "" || meta.EffectivePartitioning() != nil {
		return false
	}

	col, ok := meta.FindColumn(colName)
	if !ok {
		return false
	}

	meta.TimePartitioning = &TimePartitioning{
		Field:                  col.Name,
		RequirePartitionFilter: false,
	}
	meta.RangePartitioning = nil
	meta.Partitioning = nil
	meta.RequirePartitionFilter = false
	meta.PartitionProbed = true
	meta.Normalize(meta.Ref)

	return true
}

func cloneIntRange(r *IntRange) *IntRange {
	if r == nil {
		return nil
	}

	cp := *r

	return &cp
}

func applyIngestionInferredPartition(meta *TableMeta) bool {
	if meta.EffectivePartitioning() != nil {
		if !meta.IsIngestionTimePartitioned() || meta.EffectiveRequirePartitionFilter() {
			return false
		}

		meta.RequirePartitionFilter = true
		if meta.TimePartitioning != nil {
			meta.TimePartitioning.RequirePartitionFilter = true
		}
		meta.Normalize(meta.Ref)

		return true
	}

	meta.TimePartitioning = &TimePartitioning{
		Field:                  "",
		RequirePartitionFilter: true,
	}
	meta.RequirePartitionFilter = true
	meta.Normalize(meta.Ref)

	return true
}

func (s *Store) applyRequiredPartitionFilterHintLocked(
	key string,
	info RequiredPartitionFilterError,
	referencedRefs []TableRef,
	now time.Time,
) []enrichedTableUpdate {
	var updates []enrichedTableUpdate

	errorRef := info.Table.WithDefaults(s.opts.DefaultProject, s.opts.DefaultDataset)
	if upd, ok := s.enrichTableInMemoryLocked(errorRef, info.Columns, true, now); ok {
		updates = append(updates, upd)
	}

	resolvedRefs := s.resolveUniqueTableRefs(referencedRefs)
	if len(resolvedRefs) == 0 || containsTableRef(resolvedRefs, errorRef) {
		delete(s.pendingPartitionHints, key)

		return updates
	}

	concreteCols, _ := splitPartitionErrorColumns(info.Columns)
	if len(concreteCols) == 0 {
		delete(s.pendingPartitionHints, key)

		return updates
	}

	if s.anyRefPendingLocked(resolvedRefs, now) {
		s.storePendingPartitionHintLocked(key, pendingPartitionHint{
			info:           info,
			referencedRefs: resolvedRefs,
			recordedAt:     now,
		})

		return updates
	}

	delete(s.pendingPartitionHints, key)

	if candidate, ok := s.selectViewPartitionCandidateLocked(resolvedRefs, errorRef, concreteCols, now); ok {
		if upd, changed := s.enrichTableInMemoryLocked(candidate, concreteCols, false, now); changed {
			updates = append(updates, upd)
		}
	}

	return updates
}

func (s *Store) applyPendingPartitionHintsLocked(now time.Time) []enrichedTableUpdate {
	if len(s.pendingPartitionHints) == 0 {
		return nil
	}

	keys := make([]string, 0, len(s.pendingPartitionHints))
	for k, hint := range s.pendingPartitionHints {
		if now.Sub(hint.recordedAt) > dryRunCacheTTL {
			delete(s.pendingPartitionHints, k)

			continue
		}

		keys = append(keys, k)
	}

	var updates []enrichedTableUpdate
	for _, k := range keys {
		hint, ok := s.pendingPartitionHints[k]
		if !ok {
			continue
		}

		updates = append(updates, s.applyRequiredPartitionFilterHintLocked(k, hint.info, hint.referencedRefs, now)...)
	}

	return updates
}

func (s *Store) storePendingPartitionHintLocked(key string, hint pendingPartitionHint) {
	if _, exists := s.pendingPartitionHints[key]; !exists && len(s.pendingPartitionHints) >= maxPendingPartitionHints {
		var oldestKey string
		var oldestAt time.Time
		for k, v := range s.pendingPartitionHints {
			if oldestKey == "" || v.recordedAt.Before(oldestAt) {
				oldestKey = k
				oldestAt = v.recordedAt
			}
		}

		if oldestKey != "" {
			delete(s.pendingPartitionHints, oldestKey)
		}
	}

	s.pendingPartitionHints[key] = hint
}

func (s *Store) resolveUniqueTableRefs(refs []TableRef) []TableRef {
	if len(refs) == 0 {
		return nil
	}

	out := make([]TableRef, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		resolved := r.WithDefaults(s.opts.DefaultProject, s.opts.DefaultDataset)
		if resolved.Table == "" {
			continue
		}

		norm := resolved.NormalizedKey()
		if _, dup := seen[norm]; dup {
			continue
		}

		seen[norm] = struct{}{}
		out = append(out, resolved)
	}

	return out
}

func containsTableRef(refs []TableRef, target TableRef) bool {
	targetKey := target.NormalizedKey()
	if targetKey == "" {
		return false
	}

	return slices.ContainsFunc(refs, func(r TableRef) bool {
		return r.NormalizedKey() == targetKey
	})
}

func (s *Store) anyRefPendingLocked(refs []TableRef, now time.Time) bool {
	for _, r := range refs {
		if _, loaded := s.getFromMemoryLocked(r, now); loaded {
			continue
		}

		if _, pending := s.pending[r.FullName()]; pending {
			return true
		}
	}

	return false
}

func (s *Store) selectViewPartitionCandidateLocked(
	refs []TableRef,
	errorRef TableRef,
	concreteCols []string,
	now time.Time,
) (TableRef, bool) {
	var candidates []*TableMeta

	for _, r := range refs {
		meta, ok := s.getFromMemoryLocked(r, now)
		if ok && isEligibleViewPartitionCandidate(meta, concreteCols) {
			candidates = append(candidates, meta)
		}
	}

	if len(candidates) == 1 {
		return candidates[0].Ref, true
	}

	return disambiguateViewCandidates(candidates, errorRef)
}

func isEligibleViewPartitionCandidate(meta *TableMeta, concreteCols []string) bool {
	if meta == nil || meta.Type != TableTypeView {
		return false
	}

	col, hasCol := findMatchingSchemaColumn(meta, concreteCols)
	if !hasCol {
		return false
	}

	if meta.EffectivePartitioning() != nil {
		return meta.IsPartitionColumn(col.Name) && !meta.EffectiveRequirePartitionFilter()
	}

	return true
}

func disambiguateViewCandidates(candidates []*TableMeta, errorRef TableRef) (TableRef, bool) {
	if len(candidates) <= 1 {
		return TableRef{}, false
	}

	var matched []*TableMeta
	for _, meta := range candidates {
		if meta.View != nil && viewQueryReferencesTable(meta.View.Query, errorRef) {
			matched = append(matched, meta)
		}
	}

	if len(matched) == 1 {
		return matched[0].Ref, true
	}

	return TableRef{}, false
}

func viewQueryReferencesTable(query string, target TableRef) bool {
	if strings.TrimSpace(query) == "" || target.Table == "" {
		return false
	}

	lowerQuery := strings.ToLower(query)
	if full := strings.ToLower(target.FullName()); full != "" && strings.Contains(lowerQuery, full) {
		return true
	}

	if target.Dataset != "" {
		dsTable := strings.ToLower(target.Dataset + "." + target.Table)
		if strings.Contains(lowerQuery, dsTable) {
			return true
		}
	}

	return containsIdentifierToken(lowerQuery, strings.ToLower(target.Table))
}

func containsIdentifierToken(haystack, ident string) bool {
	if ident == "" {
		return false
	}

	offset := 0
	for {
		idx := strings.Index(haystack[offset:], ident)
		if idx < 0 {
			return false
		}

		start := offset + idx
		end := start + len(ident)
		leftOK := start == 0 || !isSQLIdentChar(haystack[start-1])
		rightOK := end == len(haystack) || !isSQLIdentChar(haystack[end])
		if leftOK && rightOK {
			return true
		}

		offset = start + 1
	}
}

func isSQLIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

func (s *Store) enrichTableInMemoryLocked(
	ref TableRef,
	columns []string,
	allowIngestionPseudo bool,
	now time.Time,
) (enrichedTableUpdate, bool) {
	entry, exactKey, ok := s.getEntryFromMemoryLocked(ref, now)
	if !ok || entry.meta == nil {
		return enrichedTableUpdate{}, false
	}

	cloned := entry.meta.Clone()
	if !applyInferredPartition(cloned, columns, allowIngestionPseudo) {
		return enrichedTableUpdate{}, false
	}

	entry.meta = cloned
	s.mem[exactKey] = entry
	s.memLower[strings.ToLower(exactKey)] = exactKey

	return enrichedTableUpdate{
		meta:      cloned,
		fetchedAt: entry.fetchedAt,
		static:    entry.static,
	}, true
}

func (s *Store) getEntryFromMemoryLocked(ref TableRef, now time.Time) (cacheEntry, string, bool) {
	key := ref.FullName()
	if key == "" {
		return cacheEntry{}, "", false
	}

	if entry, ok := s.mem[key]; ok && !s.isMemEntryExpiredLocked(entry, now) {
		return entry, key, true
	}

	if exact, ok := s.memLower[strings.ToLower(key)]; ok {
		if entry, exists := s.mem[exact]; exists && !s.isMemEntryExpiredLocked(entry, now) {
			return entry, exact, true
		}
	}

	return cacheEntry{}, "", false
}
