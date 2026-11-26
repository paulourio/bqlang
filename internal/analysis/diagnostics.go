package analysis

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/goccy/go-googlesql"

	"github.com/paulourio/bqlang/internal/bqconfig"
	"github.com/paulourio/bqlang/internal/catalog"
	"github.com/paulourio/bqlang/internal/syntax"
)

// Diagnostics returns all syntax, semantic, cost, and storage-layout lint diagnostics for uri.
func (e *Engine) Diagnostics(ctx context.Context, uri string) ([]Diagnostic, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	_, snap, err := e.ensureSnapshot(uri)
	if err != nil {
		return nil, err
	}

	out := slices.Clone(snap.Diagnostics)
	if out == nil {
		return []Diagnostic{}, nil
	}

	return out, nil
}

func computeDiagnostics(snap *DocumentSnapshot) []Diagnostic {
	if snap == nil {
		return []Diagnostic{}
	}

	if snap.Options != nil && !snap.Options.DiagnosticsEnabled {
		return []Diagnostic{}
	}

	out := make([]Diagnostic, 0)
	for _, st := range snap.Statements {
		out = appendStatementDiagnostics(out, snap, st)
	}

	for _, br := range snap.Branches {
		if br.HasParseErr {
			r := tokenRangeAtOffset(snap.LineIndex, snap.Tokens, snap.Text, br.ParseErrOrig)
			out = append(out, Diagnostic{
				Range:    r,
				Severity: SeverityError,
				Source:   "bqpls",
				Code:     DiagCodeSyntax,
				Message:  br.ParseErrMsg,
			})
		}
	}

	out = appendCostDiagnostics(out, snap)
	sortDiagnostics(out)

	return out
}

func appendStatementDiagnostics(out []Diagnostic, snap *DocumentSnapshot, st *AnalyzedStatement) []Diagnostic {
	if st.ParseErr != nil {
		if d, ok := buildSyntaxOrSemanticDiag(snap, st, st.ParseErrLine, st.ParseErrCol, st.ParseErrMsg, DiagCodeSyntax); ok {
			out = append(out, d)
		}

		return out
	}

	out = appendAnalyzeDiagnostics(out, snap, st)

	for _, issue := range st.ScanLintIssues {
		sev, enabled := severityForLintCode(snap.Options, issue.Code)
		if !enabled {
			continue
		}

		os, oe, ok := shadowSpanToOrig(
			snap.Projected.OffsetMap,
			st.ShadowSpan.Start+issue.LocalSpan.Start,
			st.ShadowSpan.Start+issue.LocalSpan.End,
		)
		if !ok {
			os = st.OrigSpan.Start
			oe = min(os+1, len(snap.Text))
		}

		out = append(out, Diagnostic{
			Range:    snap.LineIndex.RangeAt(os, oe),
			Severity: sev,
			Source:   "bqpls",
			Code:     issue.Code,
			Message:  issue.Message,
		})
	}

	return out
}

func appendAnalyzeDiagnostics(out []Diagnostic, snap *DocumentSnapshot, st *AnalyzedStatement) []Diagnostic {
	if len(st.InnerResults) == 0 {
		if st.AnalyzeErr != nil && !shouldSuppressAnalyzeErr(st) {
			if d, ok := buildSyntaxOrSemanticDiag(snap, st, st.AnalyzeErrLine, st.AnalyzeErrCol, st.AnalyzeErrMsg, DiagCodeSemantic); ok {
				out = append(out, d)
			}
		}

		return out
	}

	for _, ir := range st.InnerResults {
		if ir.AnalyzeErr != nil && !shouldSuppressInnerErr(ir) {
			if d, ok := buildSyntaxOrSemanticDiag(snap, st, ir.ErrLine, ir.ErrCol, ir.ErrMsg, DiagCodeSemantic); ok {
				out = append(out, d)
			}
		}
	}

	return out
}

func shouldSuppressAnalyzeErr(st *AnalyzedStatement) bool {
	return shouldSuppressRefs(st.HasExprPlaceholder, st.AnalyzeErrMsg, st.PendingRefs, st.UnresolvedRefs)
}

func shouldSuppressInnerErr(ir InnerAnalyzeResult) bool {
	return shouldSuppressRefs(ir.HasExprPlaceholder, ir.ErrMsg, ir.PendingRefs, ir.UnresolvedRefs)
}

func shouldSuppressRefs(
	hasPlaceholder bool,
	errMsg string,
	pendingRefs []catalog.TableRef,
	unresolvedRefs []catalog.TableRef,
) bool {
	if hasPlaceholder || strings.Contains(errMsg, "__bqpls_expr_") {
		return true
	}

	// Suppress semantic errors caused by missing table metadata (whether pending
	// an async BigQuery fetch, offline, or unconfigured in schema_dirs) so valid
	// queries against uncached external tables are not flagged as errors.
	return len(pendingRefs) > 0 || len(unresolvedRefs) > 0
}

func buildSyntaxOrSemanticDiag(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	errLine, errCol int,
	errMsg, code string,
) (Diagnostic, bool) {
	if strings.Contains(errMsg, "__bqpls_expr_") {
		return Diagnostic{}, false
	}

	origOff := st.OrigSpan.Start
	if errLine > 0 {
		stmtIdx := syntax.NewLineIndex(st.SQL)
		localOff := stmtIdx.ByteOffsetFromGoogleSQL(errLine, errCol)
		mapped, ok := snap.Projected.OffsetMap.ShadowByteToOrig(st.ShadowSpan.Start + localOff)
		if !ok {
			return Diagnostic{}, false
		}
		origOff = mapped
	}

	r := tokenRangeAtOffset(snap.LineIndex, snap.Tokens, snap.Text, origOff)

	return Diagnostic{
		Range:    r,
		Severity: SeverityError,
		Source:   "bqpls",
		Code:     code,
		Message:  errMsg,
	}, true
}

func tokenRangeAtOffset(li *syntax.LineIndex, tokens []syntax.Token, text string, off int) syntax.Range {
	if off < 0 {
		off = 0
	}

	if off > len(text) {
		off = len(text)
	}

	if tok, _, ok := syntax.TokenAt(tokens, off); ok && !tok.IsTrivia() && tok.End > tok.Start {
		return li.RangeAt(tok.Start, tok.End)
	}

	if tok, _, ok := syntax.TokenBefore(tokens, off); ok && !tok.IsTrivia() && tok.End > tok.Start {
		return li.RangeAt(tok.Start, tok.End)
	}

	end := min(off+1, len(text))

	return li.RangeAt(off, end)
}

func severityForLintCode(opts *bqconfig.LSPOptions, code string) (DiagnosticSeverity, bool) {
	if opts == nil {
		return SeverityWarning, true
	}

	switch code {
	case DiagCodeRequirePartitionFilter:
		return mapConfigSeverity(opts.RequirePartitionFilterSeverity)
	case DiagCodeMissingPartitionFilter:
		return mapConfigSeverity(opts.MissingPartitionFilterSeverity)
	case DiagCodeClusteringFilter:
		return mapConfigSeverity(opts.ClusteringFilterSeverity)
	default:
		return SeverityWarning, true
	}
}

func mapConfigSeverity(sev bqconfig.Severity) (DiagnosticSeverity, bool) {
	switch sev {
	case bqconfig.SeverityError:
		return SeverityError, true
	case bqconfig.SeverityWarning, "":
		return SeverityWarning, true
	case bqconfig.SeverityInfo:
		return SeverityInformation, true
	case bqconfig.SeverityHint:
		return SeverityHint, true
	case bqconfig.SeverityOff:
		return 0, false
	default:
		return SeverityWarning, true
	}
}

func sortDiagnostics(diags []Diagnostic) {
	sort.Slice(diags, func(i, j int) bool {
		a, b := diags[i], diags[j]
		if a.Range.Start.Line != b.Range.Start.Line {
			return a.Range.Start.Line < b.Range.Start.Line
		}

		if a.Range.Start.Character != b.Range.Start.Character {
			return a.Range.Start.Character < b.Range.Start.Character
		}

		if a.Code != b.Code {
			return a.Code < b.Code
		}

		return a.Message < b.Message
	})
}

type analyzedScanInfo struct {
	scanID         int
	meta           *catalog.TableMeta
	localSpan      syntax.ByteSpan
	partitionField string
	reqPartition   bool
	optPartition   bool
	hasClustering  bool
}

type scanColOrigin struct {
	scanID  int
	meta    *catalog.TableMeta
	colPath string
}

type sargableTarget struct {
	scanID      int
	isPartition bool
	clusterRank int
}

type scanFilterState struct {
	partSargable    bool
	partReferenced  bool
	clusterSargable map[int]bool
	clusterRef      map[int]bool
}

func newScanFilterState() *scanFilterState {
	return &scanFilterState{
		clusterSargable: make(map[int]bool),
		clusterRef:      make(map[int]bool),
	}
}

func collectScanLintIssues(
	stmtSQL string,
	astStmt googlesql.ASTStatementNode,
	rs googlesql.ResolvedStatementNode,
	cat *catalog.Catalog,
) []ScanLintIssue {
	if isNilNode(rs) || cat == nil {
		return nil
	}

	astTablePaths := collectASTTablePathSpans(astStmt)
	scans, colOrigins := indexAnalyzedScans(stmtSQL, rs, cat, astTablePaths)
	if len(scans) == 0 {
		return nil
	}

	propagateScanColOrigins(rs, colOrigins)
	states := collectScanPredicateStates(rs, scans, colOrigins)

	var issues []ScanLintIssue
	for _, sc := range scans {
		st := states[sc.scanID]
		if issue, ok := evaluatePartitionLint(sc, st); ok {
			issues = append(issues, issue)
		}

		if issue, ok := evaluateClusteringLint(sc, st); ok {
			issues = append(issues, issue)
		}
	}

	return issues
}

func evaluatePartitionLint(sc analyzedScanInfo, st *scanFilterState) (ScanLintIssue, bool) {
	if (!sc.reqPartition && !sc.optPartition) || st.partSargable {
		return ScanLintIssue{}, false
	}

	tblName := sc.meta.Ref.FullName()
	field := sc.partitionField

	if sc.reqPartition {
		msg := fmt.Sprintf("table %s requires a filter on partition column %s", tblName, field)
		if st.partReferenced {
			msg = fmt.Sprintf(
				"table %s requires a sargable filter on partition column %s; avoid wrapping %s in functions, casts, or unpruned OR branches",
				tblName,
				field,
				field,
			)
		}

		return ScanLintIssue{
			Code:      DiagCodeRequirePartitionFilter,
			LocalSpan: sc.localSpan,
			Message:   msg,
		}, true
	}

	msg := fmt.Sprintf(
		"table %s is partitioned by %s; filter or join on %s to prune partitions",
		tblName,
		field,
		field,
	)
	if st.partReferenced {
		msg = fmt.Sprintf(
			"filter on partition column %s of table %s is not sargable; avoid wrapping %s in functions, casts, or unpruned OR branches",
			field,
			tblName,
			field,
		)
	}

	return ScanLintIssue{
		Code:      DiagCodeMissingPartitionFilter,
		LocalSpan: sc.localSpan,
		Message:   msg,
	}, true
}

func evaluateClusteringLint(sc analyzedScanInfo, st *scanFilterState) (ScanLintIssue, bool) {
	if !sc.hasClustering || sc.meta.Clustering == nil || len(sc.meta.Clustering.Fields) == 0 {
		return ScanLintIssue{}, false
	}

	if st.clusterSargable[1] {
		return ScanLintIssue{}, false
	}

	fields := sc.meta.Clustering.Fields
	tblName := sc.meta.Ref.FullName()
	lead := fields[0]
	spec := formatClusterFieldsSpec(fields)

	if st.clusterRef[1] {
		return ScanLintIssue{
			Code:      DiagCodeClusteringFilter,
			LocalSpan: sc.localSpan,
			Message: fmt.Sprintf(
				"filter on leading cluster column %s of table %s is not sargable; avoid wrapping %s in functions, casts, or unpruned OR branches",
				lead,
				tblName,
				lead,
			),
		}, true
	}

	if secRank := lowestReferencedSecondaryCluster(st, len(fields)); secRank > 1 {
		secCol := fields[secRank-1]

		return ScanLintIssue{
			Code:      DiagCodeClusteringFilter,
			LocalSpan: sc.localSpan,
			Message: fmt.Sprintf(
				"table %s is clustered by %s; filtering or joining on %s without leading cluster column %s reduces block pruning",
				tblName,
				spec,
				secCol,
				lead,
			),
		}, true
	}

	return ScanLintIssue{
		Code:      DiagCodeClusteringFilter,
		LocalSpan: sc.localSpan,
		Message: fmt.Sprintf(
			"table %s is clustered by %s; filter or join on %s to prune storage blocks",
			tblName,
			spec,
			lead,
		),
	}, true
}

func lowestReferencedSecondaryCluster(st *scanFilterState, count int) int {
	for rank := 2; rank <= count; rank++ {
		if st.clusterSargable[rank] || st.clusterRef[rank] {
			return rank
		}
	}

	return 0
}

func formatClusterFieldsSpec(fields []string) string {
	if len(fields) == 1 {
		return fields[0]
	}

	return "(" + strings.Join(fields, ", ") + ")"
}

func indexAnalyzedScans(
	stmtSQL string,
	rs googlesql.ResolvedNode,
	cat *catalog.Catalog,
	astPaths []astTableSpan,
) ([]analyzedScanInfo, map[int32][]scanColOrigin) {
	var scans []analyzedScanInfo
	colOrigins := make(map[int32][]scanColOrigin)
	scanCounter := 0
	astIdx := 0

	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		scan, ok := n.(*googlesql.ResolvedTableScan)
		if !ok || isNilNode(scan) {
			return true
		}

		scanCounter++
		scanID := scanCounter
		tbl, err := scan.Table()
		if err != nil || isNilNode(tbl) {
			return true
		}

		meta, found := cat.LookupTable(tbl)
		if !found || meta == nil {
			return true
		}

		isWildcard := strings.HasSuffix(meta.Ref.Table, "*")
		reqPart := meta.EffectiveRequirePartitionFilter()
		optPart := !reqPart && !isWildcard && meta.EffectivePartitioning() != nil
		hasCluster := !isWildcard && meta.Clustering != nil && len(meta.Clustering.Fields) > 0
		if !reqPart && !optPart && !hasCluster {
			return true
		}

		span := resolveScanLocalSpan(stmtSQL, scan, meta, astPaths, &astIdx)
		scans = append(scans, analyzedScanInfo{
			scanID:         scanID,
			meta:           meta,
			localSpan:      span,
			partitionField: effectivePartitionFieldName(meta),
			reqPartition:   reqPart,
			optPartition:   optPart,
			hasClustering:  hasCluster,
		})

		recordScanColOrigins(scan, tbl, meta, scanID, colOrigins)

		return true
	})

	return scans, colOrigins
}

func recordScanColOrigins(
	scan *googlesql.ResolvedTableScan,
	tbl googlesql.TableNode,
	meta *catalog.TableMeta,
	scanID int,
	colOrigins map[int32][]scanColOrigin,
) {
	colList, _ := scan.ColumnList()
	idxList, _ := scan.ColumnIndexList()
	tblColList, _ := scan.TableColumnList()

	for i, rc := range colList {
		if isNilNode(rc) {
			continue
		}

		colID, _ := rc.ColumnId()
		colName, _ := rc.Name()
		if tblCol := resolveScanTableColumn(tbl, i, idxList, tblColList); !isNilNode(tblCol) {
			if tn, nErr := tblCol.Name(); nErr == nil && tn != "" {
				colName = tn
			}
		}

		if colName == "" {
			continue
		}

		colOrigins[colID] = append(colOrigins[colID], scanColOrigin{
			scanID:  scanID,
			meta:    meta,
			colPath: colName,
		})
	}
}

func effectivePartitionFieldName(meta *catalog.TableMeta) string {
	if p := meta.EffectivePartitioning(); p != nil && p.Field != "" {
		return p.Field
	}

	if meta.TimePartitioning != nil && meta.TimePartitioning.Field != "" {
		return meta.TimePartitioning.Field
	}

	if meta.RangePartitioning != nil && meta.RangePartitioning.Field != "" {
		return meta.RangePartitioning.Field
	}

	if meta.IsIngestionTimePartitioned() {
		return "_PARTITIONTIME/_PARTITIONDATE"
	}

	return "partition column"
}

type astTableSpan struct {
	name string
	span syntax.ByteSpan
}

func collectASTTablePathSpans(root googlesql.ASTNode) []astTableSpan {
	if isNilNode(root) {
		return nil
	}

	var spans []astTableSpan
	walkASTNodes(root, func(n googlesql.ASTNode) bool {
		tp, ok := n.(*googlesql.ASTTablePathExpression)
		if !ok || isNilNode(tp) {
			return true
		}

		pe, err := tp.PathExpr()
		if err != nil || isNilNode(pe) {
			return true
		}

		s, e, ok := astNodeByteRange(pe)
		if ok && s < e {
			parts, _ := pe.ToIdentifierVector()
			spans = append(spans, astTableSpan{
				name: strings.ToLower(strings.Join(parts, ".")),
				span: syntax.ByteSpan{Start: s, End: e},
			})
		}

		return true
	})

	return spans
}

func resolveScanLocalSpan(
	stmtSQL string,
	scan *googlesql.ResolvedTableScan,
	meta *catalog.TableMeta,
	astPaths []astTableSpan,
	astIdx *int,
) syntax.ByteSpan {
	if s, e, ok := resolvedNodeByteRange(scan); ok && s < e && e <= len(stmtSQL) {
		return syntax.ByteSpan{Start: s, End: e}
	}

	tblName := strings.ToLower(meta.Ref.Table)
	for i := *astIdx; i < len(astPaths); i++ {
		if strings.HasSuffix(astPaths[i].name, tblName) {
			*astIdx = i + 1
			return astPaths[i].span
		}
	}

	if len(astPaths) > 0 {
		return astPaths[0].span
	}

	return syntax.ByteSpan{Start: 0, End: len(stmtSQL)}
}

func propagateScanColOrigins(rs googlesql.ResolvedNode, colOrigins map[int32][]scanColOrigin) {
	withEntries := make(map[string][]*googlesql.ResolvedColumn)
	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		if we, ok := n.(*googlesql.ResolvedWithEntry); ok && !isNilNode(we) {
			qname, _ := we.WithQueryName()
			sub, _ := we.WithSubquery()
			if !isNilNode(sub) && qname != "" {
				withEntries[strings.ToLower(qname)] = scanColumnList(sub)
			}
		}

		return true
	})

	changed := true
	for iter := 0; changed && iter < maxOriginPropIters; iter++ {
		changed = false
		walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
			if propagateScanOriginsForNode(n, colOrigins, withEntries) {
				changed = true
			}

			return true
		})
	}
}

func propagateScanOriginsForNode(
	n googlesql.ResolvedNode,
	colOrigins map[int32][]scanColOrigin,
	withEntries map[string][]*googlesql.ResolvedColumn,
) bool {
	changed := false

	switch node := n.(type) {
	case *googlesql.ResolvedProjectScan:
		exprs, _ := node.ExprList()
		changed = propagateScanComputedCols(exprs, colOrigins)
	case *googlesql.ResolvedAggregateScan:
		groups, _ := node.GroupByList()
		changed = propagateScanComputedCols(groups, colOrigins)
	case *googlesql.ResolvedWithRefScan:
		qname, _ := node.WithQueryName()
		subCols := withEntries[strings.ToLower(qname)]
		refCols, _ := node.ColumnList()
		changed = linkScanColSliceOrigins(refCols, subCols, colOrigins)
	case *googlesql.ResolvedSetOperationScan:
		outCols, _ := node.ColumnList()
		items, _ := node.InputItemList()
		for _, item := range items {
			if !isNilNode(item) {
				inCols, _ := item.OutputColumnList()
				if linkScanColSliceOrigins(outCols, inCols, colOrigins) {
					changed = true
				}
			}
		}
	default:
	}

	return changed
}

func propagateScanComputedCols(cols []*googlesql.ResolvedComputedColumn, colOrigins map[int32][]scanColOrigin) bool {
	changed := false
	for _, cc := range cols {
		if isNilNode(cc) {
			continue
		}

		dst, _ := cc.Column()
		expr, _ := cc.Expr()
		if isNilNode(dst) || isNilNode(expr) {
			continue
		}

		origins := extractDirectScanOrigins(expr, colOrigins)
		if len(origins) > 0 {
			dstID, _ := dst.ColumnId()
			if mergeScanColOrigins(colOrigins, dstID, origins) {
				changed = true
			}
		}
	}

	return changed
}

func linkScanColSliceOrigins(dstCols, srcCols []*googlesql.ResolvedColumn, colOrigins map[int32][]scanColOrigin) bool {
	changed := false
	for i := 0; i < len(dstCols) && i < len(srcCols); i++ {
		if isNilNode(dstCols[i]) || isNilNode(srcCols[i]) {
			continue
		}

		srcID, _ := srcCols[i].ColumnId()
		dstID, _ := dstCols[i].ColumnId()
		if mergeScanColOrigins(colOrigins, dstID, colOrigins[srcID]) {
			changed = true
		}
	}

	return changed
}

func mergeScanColOrigins(colOrigins map[int32][]scanColOrigin, dstID int32, add []scanColOrigin) bool {
	changed := false
	for _, item := range add {
		if !slices.Contains(colOrigins[dstID], item) {
			colOrigins[dstID] = append(colOrigins[dstID], item)
			changed = true
		}
	}

	return changed
}

func extractDirectScanOrigins(
	expr googlesql.ResolvedExprNode,
	colOrigins map[int32][]scanColOrigin,
) []scanColOrigin {
	cur := unwrapLosslessCast(expr)
	var fieldIdxs []int32

	for !isNilNode(cur) {
		gsf, ok := cur.(*googlesql.ResolvedGetStructField)
		if !ok || isNilNode(gsf) {
			break
		}

		idx, err := gsf.FieldIdx()
		if err != nil {
			return nil
		}

		fieldIdxs = append(fieldIdxs, idx)
		inner, _ := gsf.Expr()
		cur = unwrapLosslessCast(inner)
	}

	ref, ok := cur.(*googlesql.ResolvedColumnRef)
	if !ok || isNilNode(ref) {
		return nil
	}

	rc, err := ref.Column()
	if err != nil || isNilNode(rc) {
		return nil
	}

	colID, _ := rc.ColumnId()
	baseOrigins := colOrigins[colID]
	if len(baseOrigins) == 0 {
		return nil
	}

	if len(fieldIdxs) == 0 {
		return baseOrigins
	}

	slices.Reverse(fieldIdxs)
	var extended []scanColOrigin
	for _, base := range baseOrigins {
		if subPath := extendScanColPath(base, fieldIdxs); subPath != "" {
			extended = append(extended, scanColOrigin{
				scanID:  base.scanID,
				meta:    base.meta,
				colPath: subPath,
			})
		}
	}

	return extended
}

func extendScanColPath(base scanColOrigin, fieldIdxs []int32) string {
	if base.meta == nil || base.colPath == "" {
		return ""
	}

	parts := strings.Split(base.colPath, ".")
	cur, ok := base.meta.FindField(parts...)
	if !ok {
		return ""
	}

	for _, idx := range fieldIdxs {
		next, found := cur.FieldAt(int(idx))
		if !found {
			return ""
		}

		parts = append(parts, next.Name)
		cur = next
	}

	return strings.Join(parts, ".")
}

func unwrapLosslessCast(expr googlesql.ResolvedExprNode) googlesql.ResolvedExprNode {
	for !isNilNode(expr) {
		castNode, ok := expr.(*googlesql.ResolvedCast)
		if !ok || isNilNode(castNode) {
			return expr
		}

		if safe, err := castNode.ReturnNullOnError(); err != nil || safe {
			return expr
		}

		if fmtExpr, _ := castNode.Format(); !isNilNode(fmtExpr) {
			return expr
		}

		inner, err := castNode.Expr()
		if err != nil || isNilNode(inner) {
			return expr
		}

		srcTypeNode, _ := inner.Type()
		dstTypeNode, _ := castNode.Type()
		if !isLosslessUpcastPair(formatGoogleSQLType(srcTypeNode), formatGoogleSQLType(dstTypeNode)) {
			return expr
		}

		expr = inner
	}

	return expr
}

func isLosslessUpcastPair(src, dst string) bool {
	src = strings.ToUpper(strings.TrimSpace(src))
	dst = strings.ToUpper(strings.TrimSpace(dst))
	if src == "" || dst == "" {
		return false
	}

	if src == dst {
		return true
	}

	if src == "DATE" && dst == "DATETIME" {
		return true
	}

	if src == "INT32" || src == "INT64" {
		return dst == "INT64" || dst == "NUMERIC" || dst == "BIGNUMERIC" || dst == "FLOAT64"
	}

	return false
}

func collectSargablePartitionScans(rs googlesql.ResolvedNode, colOrigins map[int32][]scanColOrigin) map[int]bool {
	filtered := make(map[int]bool)

	forEachPredicateExpr(rs, func(expr googlesql.ResolvedExprNode) {
		for tgt := range evalSargableTargets(expr, false, colOrigins) {
			if tgt.isPartition {
				filtered[tgt.scanID] = true
			}
		}
	})

	return filtered
}

func collectScanPredicateStates(
	rs googlesql.ResolvedNode,
	scans []analyzedScanInfo,
	colOrigins map[int32][]scanColOrigin,
) map[int]*scanFilterState {
	states := make(map[int]*scanFilterState, len(scans))
	for _, sc := range scans {
		states[sc.scanID] = newScanFilterState()
	}

	forEachPredicateExpr(rs, func(expr googlesql.ResolvedExprNode) {
		markAnyReferencedScanCols(expr, colOrigins, states)
		for tgt := range evalSargableTargets(expr, false, colOrigins) {
			st, ok := states[tgt.scanID]
			if !ok {
				continue
			}

			if tgt.isPartition {
				st.partSargable = true
			}

			if tgt.clusterRank > 0 {
				st.clusterSargable[tgt.clusterRank] = true
			}
		}
	})

	return states
}

func forEachPredicateExpr(rs googlesql.ResolvedNode, fn func(googlesql.ResolvedExprNode)) {
	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		switch node := n.(type) {
		case *googlesql.ResolvedFilterScan:
			fe, _ := node.FilterExpr()
			if !isNilNode(fe) {
				fn(fe)
			}
		case *googlesql.ResolvedJoinScan:
			je, _ := node.JoinExpr()
			if !isNilNode(je) {
				fn(je)
			}
		case *googlesql.ResolvedDeleteStmt:
			we, _ := node.WhereExpr()
			if !isNilNode(we) {
				fn(we)
			}
		case *googlesql.ResolvedUpdateStmt:
			we, _ := node.WhereExpr()
			if !isNilNode(we) {
				fn(we)
			}
		case *googlesql.ResolvedMergeStmt:
			me, _ := node.MergeExpr()
			if !isNilNode(me) {
				fn(me)
			}
		default:
		}

		return true
	})
}

func markAnyReferencedScanCols(
	expr googlesql.ResolvedExprNode,
	colOrigins map[int32][]scanColOrigin,
	states map[int]*scanFilterState,
) {
	if isNilNode(expr) {
		return
	}

	walkResolvedNodes(expr, func(n googlesql.ResolvedNode) bool {
		switch node := n.(type) {
		case *googlesql.ResolvedGetStructField:
			recordReferencedOrigins(extractDirectScanOrigins(node, colOrigins), states)
			return false
		case *googlesql.ResolvedColumnRef:
			recordReferencedOrigins(extractDirectScanOrigins(node, colOrigins), states)
			return false
		default:
			return true
		}
	})
}

func recordReferencedOrigins(origins []scanColOrigin, states map[int]*scanFilterState) {
	for _, orig := range origins {
		st, ok := states[orig.scanID]
		if !ok || orig.meta == nil {
			continue
		}

		if orig.meta.IsPartitionColumn(orig.colPath) {
			st.partReferenced = true
		}

		if rank, isCluster := orig.meta.ClusterRank(orig.colPath); isCluster {
			st.clusterRef[rank] = true
		}
	}
}

func evalSargableTargets(
	expr googlesql.ResolvedExprNode,
	negated bool,
	colOrigins map[int32][]scanColOrigin,
) map[sargableTarget]struct{} {
	if isNilNode(expr) {
		return nil
	}

	switch node := expr.(type) {
	case *googlesql.ResolvedWithExpr:
		body, _ := node.Expr()
		return evalSargableTargets(body, negated, colOrigins)
	case *googlesql.ResolvedSubqueryExpr:
		inExpr, _ := node.InExpr()
		if !isNilNode(inExpr) {
			return targetsFromDirectExpr(inExpr, colOrigins)
		}

		return nil
	case *googlesql.ResolvedFunctionCall:
		return evalFunctionSargableTargets(node, negated, colOrigins)
	case *googlesql.ResolvedColumnRef, *googlesql.ResolvedGetStructField:
		return targetsFromDirectExpr(node, colOrigins)
	default:
		return nil
	}
}

func evalFunctionSargableTargets(
	fc *googlesql.ResolvedFunctionCall,
	negated bool,
	colOrigins map[int32][]scanColOrigin,
) map[sargableTarget]struct{} {
	fnKey := resolvedFunctionKey(fc)
	args, _ := fc.ArgumentList()

	switch fnKey {
	case "$not":
		if len(args) == 1 {
			return evalSargableTargets(args[0], !negated, colOrigins)
		}

		return nil
	case "$and":
		if negated {
			return intersectArgTargets(args, true, colOrigins)
		}

		return unionArgTargets(args, false, colOrigins)
	case "$or":
		if negated {
			return unionArgTargets(args, true, colOrigins)
		}

		return intersectArgTargets(args, false, colOrigins)
	case "$equal", "$not_equal", "$less", "$less_or_equal",
		"$greater", "$greater_or_equal", "$is_distinct_from", "$is_not_distinct_from":
		return evalBinaryComparisonTargets(args, colOrigins)
	case "$between":
		return evalBetweenTargets(args, colOrigins)
	case "$is_null", "$is_true", "$is_false", "range_contains", "range_overlaps":
		return collectArgsDirectTargets(args, colOrigins)
	case "$in", "$in_array", "$like", "$like_any", "$like_all",
		"$like_any_array", "$like_all_array", "starts_with":
		if len(args) > 0 {
			return targetsFromDirectExpr(args[0], colOrigins)
		}

		return nil
	default:
		return nil
	}
}

func resolvedFunctionKey(fc *googlesql.ResolvedFunctionCall) string {
	if isNilNode(fc) {
		return ""
	}

	fn, err := fc.Function()
	if err != nil || isNilNode(fn) {
		return ""
	}

	name, err := fn.Name()
	if err != nil {
		return ""
	}

	return strings.ToLower(name)
}

func evalBinaryComparisonTargets(
	args []googlesql.ResolvedExprNode,
	colOrigins map[int32][]scanColOrigin,
) map[sargableTarget]struct{} {
	if len(args) != 2 {
		return collectArgsDirectTargets(args, colOrigins)
	}

	lhsScans := referencedScanSet(args[0], colOrigins)
	rhsScans := referencedScanSet(args[1], colOrigins)

	out := make(map[sargableTarget]struct{})
	addFilteredSideTargets(out, args[0], rhsScans, colOrigins)
	addFilteredSideTargets(out, args[1], lhsScans, colOrigins)

	return out
}

func evalBetweenTargets(
	args []googlesql.ResolvedExprNode,
	colOrigins map[int32][]scanColOrigin,
) map[sargableTarget]struct{} {
	if len(args) != 3 {
		return collectArgsDirectTargets(args, colOrigins)
	}

	valScans := referencedScanSet(args[0], colOrigins)
	boundScans := referencedScanSet(args[1], colOrigins)
	for id := range referencedScanSet(args[2], colOrigins) {
		boundScans[id] = true
	}

	out := make(map[sargableTarget]struct{})
	addFilteredSideTargets(out, args[0], boundScans, colOrigins)
	addFilteredSideTargets(out, args[1], valScans, colOrigins)
	addFilteredSideTargets(out, args[2], valScans, colOrigins)

	return out
}

func addFilteredSideTargets(
	dst map[sargableTarget]struct{},
	side googlesql.ResolvedExprNode,
	otherSideScans map[int]bool,
	colOrigins map[int32][]scanColOrigin,
) {
	for tgt := range targetsFromDirectExpr(side, colOrigins) {
		// Reject intra-scan column comparisons (e.g. t.event_date = t.other_date),
		// while preserving cross-scan join predicates (t1.id = t2.id).
		if !otherSideScans[tgt.scanID] {
			dst[tgt] = struct{}{}
		}
	}
}

func referencedScanSet(expr googlesql.ResolvedExprNode, colOrigins map[int32][]scanColOrigin) map[int]bool {
	scans := make(map[int]bool)
	if isNilNode(expr) {
		return scans
	}

	walkResolvedNodes(expr, func(n googlesql.ResolvedNode) bool {
		ref, ok := n.(*googlesql.ResolvedColumnRef)
		if !ok || isNilNode(ref) {
			return true
		}

		rc, err := ref.Column()
		if err != nil || isNilNode(rc) {
			return true
		}

		colID, _ := rc.ColumnId()
		for _, orig := range colOrigins[colID] {
			scans[orig.scanID] = true
		}

		return true
	})

	return scans
}

func collectArgsDirectTargets(
	args []googlesql.ResolvedExprNode,
	colOrigins map[int32][]scanColOrigin,
) map[sargableTarget]struct{} {
	var out map[sargableTarget]struct{}
	for _, arg := range args {
		for tgt := range targetsFromDirectExpr(arg, colOrigins) {
			if out == nil {
				out = make(map[sargableTarget]struct{})
			}
			out[tgt] = struct{}{}
		}
	}

	return out
}

func targetsFromDirectExpr(
	expr googlesql.ResolvedExprNode,
	colOrigins map[int32][]scanColOrigin,
) map[sargableTarget]struct{} {
	origins := extractDirectScanOrigins(expr, colOrigins)
	if len(origins) == 0 {
		return nil
	}

	var out map[sargableTarget]struct{}
	for _, orig := range origins {
		if orig.meta == nil {
			continue
		}

		if orig.meta.IsPartitionColumn(orig.colPath) {
			if out == nil {
				out = make(map[sargableTarget]struct{})
			}
			out[sargableTarget{scanID: orig.scanID, isPartition: true}] = struct{}{}
		}

		if rank, ok := orig.meta.ClusterRank(orig.colPath); ok {
			if out == nil {
				out = make(map[sargableTarget]struct{})
			}
			out[sargableTarget{scanID: orig.scanID, clusterRank: rank}] = struct{}{}
		}
	}

	return out
}

func unionArgTargets(
	args []googlesql.ResolvedExprNode,
	negated bool,
	colOrigins map[int32][]scanColOrigin,
) map[sargableTarget]struct{} {
	var out map[sargableTarget]struct{}
	for _, arg := range args {
		for tgt := range evalSargableTargets(arg, negated, colOrigins) {
			if out == nil {
				out = make(map[sargableTarget]struct{})
			}
			out[tgt] = struct{}{}
		}
	}

	return out
}

func intersectArgTargets(
	args []googlesql.ResolvedExprNode,
	negated bool,
	colOrigins map[int32][]scanColOrigin,
) map[sargableTarget]struct{} {
	if len(args) == 0 {
		return nil
	}

	first := evalSargableTargets(args[0], negated, colOrigins)
	if len(first) == 0 {
		return nil
	}

	out := make(map[sargableTarget]struct{}, len(first))
	for tgt := range first {
		out[tgt] = struct{}{}
	}

	for _, arg := range args[1:] {
		cur := evalSargableTargets(arg, negated, colOrigins)
		for tgt := range out {
			if _, ok := cur[tgt]; !ok {
				delete(out, tgt)
			}
		}

		if len(out) == 0 {
			return nil
		}
	}

	return out
}

func walkASTNodes(root googlesql.ASTNode, fn func(googlesql.ASTNode) bool) {
	if isNilNode(root) {
		return
	}

	if !fn(root) {
		return
	}

	num := astNumChildren(root)
	for i := range num {
		walkASTNodes(astChild(root, i), fn)
	}
}
