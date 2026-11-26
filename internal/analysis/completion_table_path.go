package analysis

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/paulourio/bqlang/internal/catalog"
	"github.com/paulourio/bqlang/internal/syntax"
)

const tableDiscoveryTimeout = 3 * time.Second

type tablePathContext struct {
	segs           []string
	partial        string
	insideBacktick bool
}

// detectTablePathContext reports whether localOff in stmtSQL is positioned
// inside a BigQuery table reference (project, project.dataset, or
// project.dataset.table) following FROM, JOIN, INTO, UPDATE, or TABLE,
// handling both unquoted and backtick-quoted (unclosed or auto-closed) paths.
func detectTablePathContext(stmtSQL string, localOff int) (tablePathContext, bool) {
	if localOff <= 0 || localOff > len(stmtSQL) {
		return tablePathContext{}, false
	}

	prefix := stmtSQL[:localOff]
	toks := syntax.Scan(prefix)
	if len(toks) == 0 {
		return tablePathContext{}, false
	}

	// Case 1: cursor is inside an unclosed backtick segment (e.g. FROM `proj.ds.| or FROM `proj.ds.|`).
	if ctx, ok := detectUnclosedBacktickTablePath(prefix, toks); ok {
		return ctx, true
	}

	// Case 2: cursor is after whitespace (e.g. "FROM |").
	last := toks[len(toks)-1]
	if last.IsTrivia() {
		kwIdx := prevNonTrivia(toks, len(toks)-1)
		if kwIdx >= 0 && isTableIntroKeywordAt(prefix, toks, kwIdx) {
			return tablePathContext{}, true
		}

		return tablePathContext{}, false
	}

	// Case 3: unquoted or mixed dotted path (e.g. "FROM proj.ds.|", "FROM `proj`.ds.|", "FROM ev|").
	return detectDottedTablePath(prefix, toks)
}

func detectUnclosedBacktickTablePath(prefix string, toks []syntax.Token) (tablePathContext, bool) {
	lastIdx := len(toks) - 1
	raw := toks[lastIdx].Text(prefix)
	if !strings.HasPrefix(raw, "`") {
		return tablePathContext{}, false
	}

	if len(raw) >= 2 && strings.HasSuffix(raw, "`") {
		return tablePathContext{}, false
	}

	inner := raw[1:]
	innerSegs, partial := splitBacktickCompletedSegments(inner)

	prevIdx := prevNonTrivia(toks, lastIdx-1)
	if prevIdx < 0 {
		return tablePathContext{}, false
	}

	if isTableIntroKeywordAt(prefix, toks, prevIdx) {
		return tablePathContext{segs: innerSegs, partial: partial, insideBacktick: true}, true
	}

	// Support mixed syntax like FROM `proj`.`ds.|`
	if toks[prevIdx].Kind == syntax.TokenPunct && toks[prevIdx].Text(prefix) == "." {
		leadSegs, kwIdx, ok := walkPrecedingDottedPath(prefix, toks, prevIdx)
		if ok && kwIdx >= 0 && isTableIntroKeywordAt(prefix, toks, kwIdx) {
			return tablePathContext{
				segs:           append(leadSegs, innerSegs...),
				partial:        partial,
				insideBacktick: true,
			}, true
		}
	}

	return tablePathContext{}, false
}

// splitBacktickCompletedSegments splits the text after an opening backtick up
// to the cursor into already-completed path segments (before the final '.')
// and any trailing partial identifier after the final '.'.
// It preserves dots inside domain-scoped GCP project prefixes like "example.com:my-proj".
func splitBacktickCompletedSegments(inner string) (segs []string, partial string) {
	if inner == "" {
		return nil, ""
	}

	// Everything up to and including ':' belongs to the project ID, even if it contains dots.
	colonIdx := strings.IndexByte(inner, ':')
	searchStart := 0
	if colonIdx >= 0 {
		searchStart = colonIdx + 1
	}

	lastDotRel := strings.LastIndexByte(inner[searchStart:], '.')
	if lastDotRel < 0 {
		return nil, inner
	}

	splitIdx := searchStart + lastDotRel
	completed := inner[:splitIdx]
	partial = inner[splitIdx+1:]
	if completed == "" {
		return nil, partial
	}

	if colonIdx >= 0 {
		firstDotRel := strings.IndexByte(completed[searchStart:], '.')
		if firstDotRel < 0 {
			return []string{completed}, partial
		}

		proj := completed[:searchStart+firstDotRel]
		rest := completed[searchStart+firstDotRel+1:]
		segs = []string{proj}
		for p := range strings.SplitSeq(rest, ".") {
			if p != "" {
				segs = append(segs, p)
			}
		}

		return segs, partial
	}

	for p := range strings.SplitSeq(completed, ".") {
		if p != "" {
			segs = append(segs, p)
		}
	}

	return segs, partial
}

func detectDottedTablePath(prefix string, toks []syntax.Token) (tablePathContext, bool) {
	idx := len(toks) - 1
	partial := ""

	// If cursor is on a trailing partial path segment (e.g. "FROM proj.ds.sh|", "FROM my-proj|", or "FROM sh|"), step past it.
	if seg, nextIdx, ok := walkBackwardTablePathSeg(prefix, toks, idx); ok && toks[idx].Kind != syntax.TokenBacktickIdent {
		partial = seg
		idx = nextIdx
		if idx < 0 {
			return tablePathContext{}, false
		}

		if isTableIntroKeywordAt(prefix, toks, idx) {
			return tablePathContext{partial: partial}, true
		}
	}

	// Must now be sitting on a '.' that separates path segments.
	if toks[idx].Kind != syntax.TokenPunct || toks[idx].Text(prefix) != "." {
		return tablePathContext{}, false
	}

	segs, kwIdx, ok := walkPrecedingDottedPath(prefix, toks, idx)
	if !ok || kwIdx < 0 || !isTableIntroKeywordAt(prefix, toks, kwIdx) {
		return tablePathContext{}, false
	}

	return tablePathContext{segs: segs, partial: partial}, true
}

func walkPrecedingDottedPath(prefix string, toks []syntax.Token, dotIdx int) (segs []string, kwIdx int, ok bool) {
	idx := dotIdx
	var rev []string

	for idx >= 0 && toks[idx].Kind == syntax.TokenPunct && toks[idx].Text(prefix) == "." {
		segIdx := prevNonTrivia(toks, idx-1)
		rawSeg, prevIdx, segOK := walkBackwardTablePathSeg(prefix, toks, segIdx)
		if !segOK || rawSeg == "" {
			return nil, -1, false
		}

		rev = append(rev, rawSeg)
		idx = prevIdx
	}

	if len(rev) == 0 {
		return nil, -1, false
	}

	reverseStrings(rev)

	return mergeColonProjectSegments(normalizeASTTableParts(rev)), idx, true
}

func isUnquotedPathSegPart(prefix string, tok syntax.Token) bool {
	switch tok.Kind {
	case syntax.TokenIdent, syntax.TokenNumber:
		return true
	case syntax.TokenOperator:
		return tok.Text(prefix) == "-"
	case syntax.TokenPunct:
		return tok.Text(prefix) == ":"
	default:
		return false
	}
}

func walkBackwardTablePathSeg(prefix string, toks []syntax.Token, endIdx int) (seg string, prevIdx int, ok bool) {
	if endIdx < 0 || endIdx >= len(toks) {
		return "", -1, false
	}

	if toks[endIdx].Kind == syntax.TokenBacktickIdent {
		trimmed := strings.Trim(toks[endIdx].Text(prefix), "`")
		if trimmed == "" {
			return "", -1, false
		}

		return trimmed, prevNonTrivia(toks, endIdx-1), true
	}

	if !isUnquotedPathSegPart(prefix, toks[endIdx]) {
		return "", -1, false
	}

	startIdx := endIdx
	for startIdx > 0 {
		prev := toks[startIdx-1]
		if prev.End != toks[startIdx].Start || !isUnquotedPathSegPart(prefix, prev) {
			break
		}

		startIdx--
	}

	if toks[startIdx].Kind != syntax.TokenIdent {
		return "", -1, false
	}

	raw := prefix[toks[startIdx].Start:toks[endIdx].End]

	return raw, prevNonTrivia(toks, startIdx-1), true
}

func mergeColonProjectSegments(segs []string) []string {
	for i, s := range segs {
		if i > 0 && strings.Contains(s, ":") {
			merged := make([]string, 0, len(segs)-i)
			merged = append(merged, strings.Join(segs[:i+1], "."))
			merged = append(merged, segs[i+1:]...)

			return merged
		}
	}

	return segs
}

func isTableIntroKeywordAt(prefix string, toks []syntax.Token, kwIdx int) bool {
	if kwIdx < 0 || kwIdx >= len(toks) || toks[kwIdx].Kind != syntax.TokenIdent {
		return false
	}

	kw := strings.ToUpper(toks[kwIdx].Text(prefix))
	switch kw {
	case "JOIN", "INTO", "UPDATE", "TABLE":
		return true
	case "FROM":
		return !isExpressionFromKeyword(prefix, toks, kwIdx)
	default:
		return false
	}
}

// isExpressionFromKeyword distinguishes EXTRACT(part FROM x), SUBSTRING(s FROM n),
// and TRIM(c FROM s) from a table-introducing FROM clause.
func isExpressionFromKeyword(prefix string, toks []syntax.Token, fromIdx int) bool {
	var openCallers []string
	for i := range fromIdx {
		if toks[i].Kind != syntax.TokenPunct {
			continue
		}

		switch toks[i].Text(prefix) {
		case "(":
			openCallers = append(openCallers, strings.ToUpper(extractCalleeBeforeOpenParen(prefix, toks, i)))
		case ")":
			if len(openCallers) > 0 {
				openCallers = openCallers[:len(openCallers)-1]
			}
		default:
		}
	}

	if len(openCallers) == 0 {
		return false
	}

	top := openCallers[len(openCallers)-1]

	return top == "EXTRACT" || top == "SUBSTRING" || top == "SUBSTR" || top == "TRIM"
}

func completeTablePathContext(
	ctx context.Context,
	snap *DocumentSnapshot,
	stmtSQL string,
	tpCtx tablePathContext,
) *CompletionList {
	var (
		items        []CompletionItem
		isIncomplete bool
	)

	defProj, defDS := storeDefaultProjectDataset(snap)

	switch len(tpCtx.segs) {
	case 0:
		items, isIncomplete = completeRootTablePath(ctx, snap, stmtSQL, defProj, defDS, tpCtx.partial)
	case 1:
		items, isIncomplete = completeAfterOneTableSeg(ctx, snap, defProj, tpCtx.segs[0], tpCtx.partial)
	case 2:
		items, isIncomplete = completeTablesInDataset(ctx, snap, tpCtx.segs[0], tpCtx.segs[1], tpCtx.partial)
	default:
		return &CompletionList{IsIncomplete: false, Items: []CompletionItem{}}
	}

	if snap.Options != nil && snap.Options.BigQueryAPIEnabled {
		// Mark table-path lists incomplete when live discovery is enabled so
		// editors re-query completion if the user types an uncached dataset/table prefix.
		isIncomplete = true
	}

	return finalizeCompletions(items, isIncomplete)
}

func completeRootTablePath(
	ctx context.Context,
	snap *DocumentSnapshot,
	stmtSQL, defProj, defDS, partial string,
) ([]CompletionItem, bool) {
	var items []CompletionItem

	for _, cte := range collectCTENamesFromTokens(stmtSQL) {
		items = append(items, CompletionItem{
			Label:        cte,
			LabelDetails: &CompletionItemLabelDetails{Description: "CTE"},
			Kind:         CompletionKindStruct,
			Detail:       "CTE",
			SortText:     "0_" + cte,
		})
	}

	for _, tt := range snap.TempTables {
		items = append(items, CompletionItem{
			Label:        tt.Ref.Table,
			LabelDetails: &CompletionItemLabelDetails{Description: "TEMP TABLE"},
			Kind:         CompletionKindClass,
			Detail:       "TEMP TABLE",
			SortText:     "0_" + tt.Ref.Table,
		})
	}

	if snap.Store == nil {
		return items, false
	}

	for _, proj := range snap.Store.ListProjects() {
		items = append(items, CompletionItem{
			Label:        proj,
			LabelDetails: &CompletionItemLabelDetails{Description: "GCP Project"},
			Kind:         CompletionKindModule,
			Detail:       "GCP Project",
			SortText:     "1_" + proj,
		})
	}

	items, isIncomplete := appendRootDefaultScopeItems(ctx, snap, defProj, defDS, partial, items)

	for _, tbl := range snap.Store.ListCachedTables() {
		items = append(items, tableMetaCompletionItem(tbl, tbl.Ref.Table, "3_"))
		if tbl.Ref.Dataset != "" {
			items = append(items, tableMetaCompletionItem(tbl, tbl.Ref.Dataset+"."+tbl.Ref.Table, "4_"))
		}
	}

	return items, isIncomplete
}

func appendRootDefaultScopeItems(
	ctx context.Context,
	snap *DocumentSnapshot,
	defProj, defDS, partial string,
	items []CompletionItem,
) ([]CompletionItem, bool) {
	if defProj == "" {
		return items, false
	}

	discoverPrefix := partial
	if discoverPrefix != "" && hasCompletionItemPrefix(items, discoverPrefix) {
		discoverPrefix = ""
	}

	datasets, isIncomplete := snap.Store.DiscoverDatasetsMatching(ctx, defProj, discoverPrefix, tableDiscoveryTimeout)
	for _, ds := range datasets {
		items = append(items, CompletionItem{
			Label:        ds,
			LabelDetails: &CompletionItemLabelDetails{Description: "Dataset"},
			Kind:         CompletionKindModule,
			Detail:       "Dataset (" + defProj + ")",
			SortText:     "2_" + ds,
		})
	}

	if defDS == "" {
		return items, isIncomplete
	}

	tblPrefix := partial
	if tblPrefix != "" && hasCompletionItemPrefix(items, tblPrefix) {
		tblPrefix = ""
	}

	tbls, incT := snap.Store.DiscoverTablesMatching(ctx, defProj, defDS, tblPrefix, tableDiscoveryTimeout)
	for _, ts := range tbls {
		items = append(items, tableSummaryCompletionItem(snap.Store, ts, "2_"))
	}

	return items, isIncomplete || incT
}

func hasCompletionItemPrefix(items []CompletionItem, prefix string) bool {
	lower := strings.ToLower(prefix)
	for _, it := range items {
		if strings.HasPrefix(strings.ToLower(it.Label), lower) {
			return true
		}
	}

	return false
}

func completeAfterOneTableSeg(
	ctx context.Context,
	snap *DocumentSnapshot,
	defProj, seg0, partial string,
) ([]CompletionItem, bool) {
	if snap.Store == nil {
		return nil, false
	}

	var (
		items        []CompletionItem
		isIncomplete bool
	)

	// 1. Treat seg0 as a GCP project ID -> list datasets in seg0.
	datasets, incDS := snap.Store.DiscoverDatasetsMatching(ctx, seg0, partial, tableDiscoveryTimeout)
	isIncomplete = isIncomplete || incDS
	for _, ds := range datasets {
		items = append(items, CompletionItem{
			Label:        ds,
			LabelDetails: &CompletionItemLabelDetails{Description: "Dataset"},
			Kind:         CompletionKindModule,
			Detail:       "Dataset (" + seg0 + ")",
			SortText:     "0_" + ds,
		})
	}

	// 2. Also treat seg0 as a dataset in default_project -> list cached tables
	// (and live-discover tables if seg0 did not resolve as a project with matching datasets).
	if defProj != "" {
		if len(datasets) == 0 || (partial != "" && !hasCompletionItemPrefix(items, partial)) {
			tbls, incTbl := snap.Store.DiscoverTablesMatching(ctx, defProj, seg0, partial, tableDiscoveryTimeout)
			isIncomplete = isIncomplete || incTbl
			for _, ts := range tbls {
				items = append(items, tableSummaryCompletionItem(snap.Store, ts, "1_"))
			}
		} else {
			for _, tbl := range snap.Store.ListTables(defProj, seg0) {
				items = append(items, tableMetaCompletionItem(tbl, tbl.Ref.Table, "1_"))
			}
		}
	}

	return items, isIncomplete
}

func completeTablesInDataset(
	ctx context.Context,
	snap *DocumentSnapshot,
	project, dataset, partial string,
) ([]CompletionItem, bool) {
	if snap.Store == nil {
		return nil, false
	}

	tables, isIncomplete := snap.Store.DiscoverTablesMatching(ctx, project, dataset, partial, tableDiscoveryTimeout)
	items := make([]CompletionItem, 0, len(tables))
	for _, ts := range tables {
		items = append(items, tableSummaryCompletionItem(snap.Store, ts, "0_"))
	}

	return items, isIncomplete
}

func tableSummaryCompletionItem(store *catalog.Store, ts catalog.TableSummary, sortPrefix string) CompletionItem {
	if store != nil {
		// Enrich with full metadata if already in memory (without triggering a REST fetch).
		for _, meta := range store.ListTables(ts.Ref.Project, ts.Ref.Dataset) {
			if strings.EqualFold(meta.Ref.Table, ts.Ref.Table) {
				return tableMetaCompletionItem(meta, meta.Ref.Table, sortPrefix)
			}
		}
	}

	detail := ts.Ref.FullName()
	if ts.Type != "" {
		detail = fmt.Sprintf("%s (%s)", ts.Ref.FullName(), ts.Type)
	}

	var doc *MarkupContent
	if ts.FriendlyName != "" {
		doc = &MarkupContent{Kind: "markdown", Value: ts.FriendlyName}
	}

	kindLabel := string(ts.Type)
	if kindLabel == "" {
		kindLabel = "TABLE"
	}

	return CompletionItem{
		Label:         ts.Ref.Table,
		LabelDetails:  &CompletionItemLabelDetails{Description: kindLabel},
		Kind:          CompletionKindClass,
		Detail:        detail,
		Documentation: doc,
		SortText:      sortPrefix + ts.Ref.Table,
	}
}

// collectCTENamesFromTokens extracts CTE names from WITH clauses even when the
// statement is incomplete and cannot be parsed into a full AST yet.
func collectCTENamesFromTokens(sql string) []string {
	toks := collectNonTriviaTokens(syntax.Scan(sql))
	var names []string
	seen := make(map[string]struct{})
	depth := 0

	for i := range toks {
		if toks[i].Kind == syntax.TokenPunct {
			depth = adjustParenDepth(depth, toks[i].Text(sql))
			continue
		}

		if depth != 0 {
			continue
		}

		if name, ok := cteNameAtToken(sql, toks, i); ok {
			key := strings.ToLower(name)
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				names = append(names, name)
			}
		}
	}

	return names
}

func adjustParenDepth(depth int, punct string) int {
	switch punct {
	case "(":
		return depth + 1
	case ")":
		return max(0, depth-1)
	default:
		return depth
	}
}

func cteNameAtToken(sql string, toks []syntax.Token, i int) (string, bool) {
	if !isIdentLikeToken(toks[i]) || i+2 >= len(toks) {
		return "", false
	}

	if !strings.EqualFold(toks[i+1].Text(sql), "AS") ||
		toks[i+2].Kind != syntax.TokenPunct ||
		toks[i+2].Text(sql) != "(" {
		return "", false
	}

	name := strings.Trim(toks[i].Text(sql), "`")
	key := strings.ToLower(name)
	if name == "" || key == "with" || key == "recursive" {
		return "", false
	}

	return name, true
}
