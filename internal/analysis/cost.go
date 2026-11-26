package analysis

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/goccy/go-googlesql"

	"github.com/paulourio/bqlang/internal/bqconfig"
	"github.com/paulourio/bqlang/internal/catalog"
	"github.com/paulourio/bqlang/internal/syntax"
)

const (
	// BytesPerTiB is the number of bytes in one tebibyte (2^40), the unit used
	// for BigQuery on-demand query pricing.
	BytesPerTiB = int64(1 << 40)

	// DefaultPricePerTiBUSD is the default BigQuery on-demand query price in USD per TiB.
	DefaultPricePerTiBUSD = 6.25

	// CommandRefreshCost is the LSP command identifier attached to cost CodeLenses.
	CommandRefreshCost = "bqpls.refreshCost"
)

// CostSource identifies how a query cost estimate was produced.
type CostSource string

// Supported CostSource values.
const (
	CostSourceDryRun  CostSource = "dry_run"
	CostSourceOffline CostSource = "offline"
)

// CostStatus describes the overall validation and cost-estimation state of a document.
type CostStatus string

// Supported CostStatus values.
const (
	CostStatusEmpty       CostStatus = ""
	CostStatusOK          CostStatus = "ok"
	CostStatusError       CostStatus = "error"
	CostStatusPending     CostStatus = "pending"
	CostStatusUnavailable CostStatus = "unavailable"
)

// CostErrorDetail describes a syntax, semantic, or BigQuery dry-run error blocking cost estimation.
type CostErrorDetail struct {
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Source  string `json:"source"`
	Message string `json:"message"`
}

// TableCostBreakdown describes a single table's contribution to an offline estimate.
type TableCostBreakdown struct {
	Table             string `json:"table"`
	NumBytes          int64  `json:"numBytes"`
	HasNumBytes       bool   `json:"hasNumBytes"`
	Partitioned       bool   `json:"partitioned,omitempty"`
	PartitionFiltered bool   `json:"partitionFiltered,omitempty"`
}

// StatementCostEstimate holds the estimated bytes processed and USD cost for one statement.
type StatementCostEstimate struct {
	Index             int                  `json:"index"`
	Range             syntax.Range         `json:"range"`
	HeaderRange       syntax.Range         `json:"-"`
	BytesProcessed    int64                `json:"bytesProcessed"`
	BytesBilled       int64                `json:"bytesBilled"`
	EstimatedCostUSD  float64              `json:"estimatedCostUsd"`
	FormattedBytes    string               `json:"formattedBytes"`
	FormattedCost     string               `json:"formattedCost"`
	Label             string               `json:"label"`
	Source            CostSource           `json:"source"`
	Accuracy          string               `json:"accuracy,omitempty"`
	StatementType     string               `json:"statementType,omitempty"`
	CacheHit          bool                 `json:"cacheHit,omitempty"`
	PartitionFiltered bool                 `json:"partitionFiltered,omitempty"`
	ExceedsThreshold  bool                 `json:"exceedsThreshold"`
	ThresholdReason   string               `json:"thresholdReason,omitempty"`
	ReferencedTables  []TableCostBreakdown `json:"referencedTables,omitempty"`
}

// DocumentCostSummary aggregates cost estimates across all statements in an open document.
type DocumentCostSummary struct {
	URI                   string                  `json:"uri"`
	Version               int                     `json:"version"`
	Enabled               bool                    `json:"enabled"`
	Status                CostStatus              `json:"status,omitempty"`
	TotalBytesProcessed   int64                   `json:"totalBytesProcessed"`
	TotalBytesBilled      int64                   `json:"totalBytesBilled"`
	TotalEstimatedCostUSD float64                 `json:"totalEstimatedCostUsd"`
	FormattedBytes        string                  `json:"formattedBytes"`
	FormattedCost         string                  `json:"formattedCost"`
	Summary               string                  `json:"summary"`
	Source                CostSource              `json:"source,omitempty"`
	ExceedsThreshold      bool                    `json:"exceedsThreshold"`
	ErrorCount            int                     `json:"errorCount,omitempty"`
	ErrorMessage          string                  `json:"errorMessage,omitempty"`
	Errors                []CostErrorDetail       `json:"errors,omitempty"`
	Statements            []StatementCostEstimate `json:"statements"`
}

// EstimateCostUSD computes the on-demand USD cost for scanning bytes at pricePerTiBUSD per TiB.
func EstimateCostUSD(bytes int64, pricePerTiBUSD float64) float64 {
	if bytes <= 0 || pricePerTiBUSD <= 0 || math.IsNaN(pricePerTiBUSD) || math.IsInf(pricePerTiBUSD, 0) {
		return 0
	}

	return (float64(bytes) / float64(BytesPerTiB)) * pricePerTiBUSD
}

// FormatBytes formats a byte count using binary IEC units matching BigQuery TiB pricing.
func FormatBytes(bytes int64) string {
	if bytes <= 0 {
		return "0 B"
	}

	const unit = 1024.0
	if bytes < 1024 {
		return strconv.FormatInt(bytes, 10) + " B"
	}

	units := [...]string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	val := float64(bytes) / unit
	idx := 0
	for val >= unit && idx < len(units)-1 {
		val /= unit
		idx++
	}

	if val >= 100 {
		return fmt.Sprintf("%.1f %s", val, units[idx])
	}

	return fmt.Sprintf("%.2f %s", val, units[idx])
}

// FormatUSD formats a USD cost concisely for CodeLens, hover cards, and status bars.
func FormatUSD(usd float64) string {
	if usd <= 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return "$0.00"
	}

	if usd < 0.005 {
		return "<$0.01"
	}

	return fmt.Sprintf("$%.2f", usd)
}

// FormatRowCount formats a non-negative integer row count with comma thousands separators.
func FormatRowCount(rows int64) string {
	s := strconv.FormatInt(rows, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}

	if len(s) <= 3 {
		if neg {
			return "-" + s
		}

		return s
	}

	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}

	rem := len(s) % 3
	if rem > 0 {
		b.WriteString(s[:rem])
	}

	for i := rem; i < len(s); i += 3 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}

	return b.String()
}

// DocumentCost computes the current cost summary for uri using cached dry-run
// results when available and falling back to offline table-size upper bounds.
func (e *Engine) DocumentCost(ctx context.Context, uri string) (*DocumentCostSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	_, snap, err := e.ensureSnapshot(uri)
	if err != nil {
		return nil, err
	}

	return computeDocumentCost(snap), nil
}

// CodeLenses returns LSP 3.17 CodeLens items displaying estimated query costs
// above each eligible statement in uri.
func (e *Engine) CodeLenses(ctx context.Context, uri string) ([]CodeLens, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	_, snap, err := e.ensureSnapshot(uri)
	if err != nil {
		return nil, err
	}

	if !isCostCodeLensEnabled(snap.Options) {
		return []CodeLens{}, nil
	}

	summary := computeDocumentCost(snap)
	if len(summary.Statements) == 0 {
		return []CodeLens{}, nil
	}

	lenses := make([]CodeLens, 0, len(summary.Statements))
	for _, st := range summary.Statements {
		lineRange := syntax.Range{
			Start: st.Range.Start,
			End:   st.Range.Start,
		}
		lenses = append(lenses, CodeLens{
			Range: lineRange,
			Command: &Command{
				Title:     st.Label,
				Command:   CommandRefreshCost,
				Arguments: []any{uri},
			},
		})
	}

	return lenses, nil
}

// DryRunTarget describes a SQL statement eligible for BigQuery dry-run
// estimation along with the catalog tables and views it references.
type DryRunTarget struct {
	SQL              string
	ReferencedTables []catalog.TableRef
}

// DryRunTargets returns the billing project, default dataset, and SQL statements
// eligible for asynchronous BigQuery dry-run estimation in uri.
func (e *Engine) DryRunTargets(uri string) (project, dataset string, queries []string, ok bool) {
	project, dataset, targets, ok := e.DryRunTargetSpecs(uri)
	if !ok || len(targets) == 0 {
		return project, dataset, nil, false
	}

	queries = make([]string, len(targets))
	for i, t := range targets {
		queries[i] = t.SQL
	}

	return project, dataset, queries, true
}

// DryRunTargetSpecs returns the billing project, default dataset, and dry-run
// targets (SQL plus referenced tables/views) eligible for uri.
func (e *Engine) DryRunTargetSpecs(uri string) (project, dataset string, targets []DryRunTarget, ok bool) {
	_, snap, err := e.ensureSnapshot(uri)
	if err != nil || snap == nil || snap.Store == nil {
		return "", "", nil, false
	}

	if !isCostEstimationEnabled(snap.Options) || !snap.Options.BigQueryAPIEnabled {
		return "", "", nil, false
	}

	project = snap.Store.EffectiveBillingProject()
	if project == "" {
		return "", "", nil, false
	}

	dataset = snap.Store.DefaultDataset()
	targets = collectDryRunTargets(snap)
	if len(targets) == 0 {
		return project, dataset, nil, false
	}

	return project, dataset, targets, true
}

func collectDryRunTargets(snap *DocumentSnapshot) []DryRunTarget {
	if snap == nil || len(snap.Statements) == 0 {
		return nil
	}

	defProj, defDS := storeDefaultProjectDataset(snap)
	if isScriptDocument(snap) {
		if scriptSQL, ok := wholeScriptDryRunSQL(snap); ok {
			var refs []catalog.TableRef
			for _, st := range snap.Statements {
				refs = appendStatementTableRefs(refs, st, defProj, defDS)
			}

			return []DryRunTarget{{
				SQL:              scriptSQL,
				ReferencedTables: refs,
			}}
		}

		return nil
	}

	targets := make([]DryRunTarget, 0, len(snap.Statements))
	for _, st := range snap.Statements {
		if st.DryRunEligible && st.ParseErr == nil && !st.HasExprPlaceholder && !strings.Contains(st.SQL, "__bqpls_expr_") {
			if trimmed := strings.TrimSpace(st.SQL); trimmed != "" {
				targets = append(targets, DryRunTarget{
					SQL:              trimmed,
					ReferencedTables: appendStatementTableRefs(nil, st, defProj, defDS),
				})
			}
		}
	}

	return targets
}

func appendStatementTableRefs(dst []catalog.TableRef, st *AnalyzedStatement, defProj, defDS string) []catalog.TableRef {
	if st == nil || len(st.ASTTableRefs) == 0 {
		return dst
	}

	for _, parts := range st.ASTTableRefs {
		ref := tableRefFromPath(parts, defProj, defDS)
		if ref.Table == "" {
			continue
		}

		dst = appendUniqueTableRefs(dst, []catalog.TableRef{ref})
	}

	return dst
}

func collectDryRunQueries(snap *DocumentSnapshot) []string {
	targets := collectDryRunTargets(snap)
	if len(targets) == 0 {
		return nil
	}

	queries := make([]string, len(targets))
	for i, t := range targets {
		queries[i] = t.SQL
	}

	return queries
}

func isScriptDocument(snap *DocumentSnapshot) bool {
	return len(snap.DeclaredVars) > 0 || len(snap.TempTables) > 0
}

func wholeScriptDryRunSQL(snap *DocumentSnapshot) (string, bool) {
	if snap == nil || strings.Contains(snap.Projected.SQL, "__bqpls_expr_") {
		return "", false
	}

	for _, st := range snap.Statements {
		if st.ParseErr != nil || st.HasExprPlaceholder {
			return "", false
		}
	}

	trimmed := strings.TrimSpace(snap.Projected.SQL)
	if trimmed == "" {
		return "", false
	}

	return trimmed, true
}

func isCostEstimationEnabled(opts *bqconfig.LSPOptions) bool {
	return opts == nil || opts.CostEstimationEnabled
}

func isCostCodeLensEnabled(opts *bqconfig.LSPOptions) bool {
	return opts == nil || (opts.CostEstimationEnabled && opts.CostCodeLensEnabled)
}

func isCostHoverEnabled(opts *bqconfig.LSPOptions) bool {
	return opts == nil || (opts.CostEstimationEnabled && opts.CostHoverEnabled)
}

func writeTableHoverStats(b *strings.Builder, opts *bqconfig.LSPOptions, meta *catalog.TableMeta) {
	b.WriteString("\n**Stats:**")

	if !isCostHoverEnabled(opts) {
		if meta.NumRows != nil {
			fmt.Fprintf(b, " %d rows", *meta.NumRows)
		}
		if meta.NumBytes != nil {
			fmt.Fprintf(b, " (%d bytes)", *meta.NumBytes)
		}
		b.WriteByte('\n')

		return
	}

	var parts []string
	if meta.NumRows != nil {
		parts = append(parts, FormatRowCount(meta.NumRows.Int64())+" rows")
	}
	if meta.NumBytes != nil {
		numBytes := meta.NumBytes.Int64()
		costUSD := EstimateCostUSD(numBytes, effectivePricePerTiB(opts))
		parts = append(parts, fmt.Sprintf("%s (full scan %s)", FormatBytes(numBytes), FormatUSD(costUSD)))
	}

	fmt.Fprintf(b, " %s\n", strings.Join(parts, " · "))
}

func effectivePricePerTiB(opts *bqconfig.LSPOptions) float64 {
	if opts == nil {
		return DefaultPricePerTiBUSD
	}

	return opts.CostPricePerTiBUSD
}

func computeDocumentCost(snap *DocumentSnapshot) *DocumentCostSummary {
	summary := &DocumentCostSummary{
		URI:        snap.URI,
		Version:    snap.Version,
		Enabled:    isCostEstimationEnabled(snap.Options),
		Statements: []StatementCostEstimate{},
	}

	if !summary.Enabled || len(snap.Statements) == 0 {
		return summary
	}

	price := effectivePricePerTiB(snap.Options)
	localErrors := collectLocalSQLErrors(snap)

	if isScriptDocument(snap) {
		if scriptEst, ok := tryScriptDryRunEstimate(snap, price); ok {
			summary.Statements = []StatementCostEstimate{scriptEst}
			finalizeDocumentSummary(summary, price)
			applyDocumentCostStatus(snap, summary, localErrors)

			return summary
		}
	}

	for i, st := range snap.Statements {
		if est, ok := estimateStatementCost(snap, st, i, price); ok {
			summary.Statements = append(summary.Statements, est)
		}
	}

	finalizeDocumentSummary(summary, price)
	applyDocumentCostStatus(snap, summary, localErrors)

	return summary
}

func collectLocalSQLErrors(snap *DocumentSnapshot) []CostErrorDetail {
	var errs []CostErrorDetail
	for _, st := range snap.Statements {
		errs = appendStatementSQLErrors(errs, snap, st)
	}

	for _, br := range snap.Branches {
		if br.HasParseErr {
			pos := snap.LineIndex.PositionAt(br.ParseErrOrig)
			errs = append(errs, CostErrorDetail{
				Line:    pos.Line + 1,
				Column:  pos.Character + 1,
				Source:  "syntax",
				Message: br.ParseErrMsg,
			})
		}
	}

	return errs
}

func appendStatementSQLErrors(errs []CostErrorDetail, snap *DocumentSnapshot, st *AnalyzedStatement) []CostErrorDetail {
	if st.ParseErr != nil {
		if d, ok := buildSyntaxOrSemanticDiag(snap, st, st.ParseErrLine, st.ParseErrCol, st.ParseErrMsg, "syntax"); ok {
			errs = append(errs, CostErrorDetail{
				Line:    d.Range.Start.Line + 1,
				Column:  d.Range.Start.Character + 1,
				Source:  "syntax",
				Message: d.Message,
			})
		}

		return errs
	}

	if len(st.InnerResults) == 0 {
		if st.AnalyzeErr != nil && !shouldSuppressAnalyzeErr(st) {
			if d, ok := buildSyntaxOrSemanticDiag(snap, st, st.AnalyzeErrLine, st.AnalyzeErrCol, st.AnalyzeErrMsg, "semantic"); ok {
				errs = append(errs, CostErrorDetail{
					Line:    d.Range.Start.Line + 1,
					Column:  d.Range.Start.Character + 1,
					Source:  "semantic",
					Message: d.Message,
				})
			}
		}

		return errs
	}

	for _, ir := range st.InnerResults {
		if ir.AnalyzeErr != nil && !shouldSuppressInnerErr(ir) {
			if d, ok := buildSyntaxOrSemanticDiag(snap, st, ir.ErrLine, ir.ErrCol, ir.ErrMsg, "semantic"); ok {
				errs = append(errs, CostErrorDetail{
					Line:    d.Range.Start.Line + 1,
					Column:  d.Range.Start.Character + 1,
					Source:  "semantic",
					Message: d.Message,
				})
			}
		}
	}

	return errs
}

func applyDocumentCostStatus(snap *DocumentSnapshot, summary *DocumentCostSummary, localErrors []CostErrorDetail) {
	if len(localErrors) > 0 {
		summary.Status = CostStatusError
		summary.Errors = localErrors
		summary.ErrorCount = len(localErrors)
		summary.ErrorMessage = localErrors[0].Message
		if len(localErrors) == 1 {
			if localErrors[0].Line > 0 {
				summary.Summary = fmt.Sprintf("Invalid SQL (Ln %d)", localErrors[0].Line)
			} else {
				summary.Summary = "Invalid SQL"
			}
		} else {
			summary.Summary = fmt.Sprintf("Invalid SQL (%d errors)", len(localErrors))
		}

		return
	}

	dryErrs, unavailErr, anyPending := collectDryRunState(snap)
	if len(dryErrs) > 0 {
		summary.Status = CostStatusError
		summary.Errors = dryErrs
		summary.ErrorCount = len(dryErrs)
		summary.ErrorMessage = dryErrs[0].Message
		summary.Summary = "Dry run error"

		return
	}

	if len(summary.Statements) > 0 {
		summary.Status = CostStatusOK

		return
	}

	if anyPending {
		summary.Status = CostStatusPending
		summary.Summary = "Estimating..."

		return
	}

	if unavailErr != nil {
		summary.Status = CostStatusUnavailable
		summary.ErrorMessage = unavailErr.Message
		summary.Errors = []CostErrorDetail{*unavailErr}
		summary.Summary = "Cost unavailable"
	}
}

func collectDryRunState(snap *DocumentSnapshot) (rejected []CostErrorDetail, unavailable *CostErrorDetail, pending bool) {
	if snap == nil || snap.Store == nil || snap.Options == nil || !snap.Options.BigQueryAPIEnabled {
		return nil, nil, false
	}

	queries := collectDryRunQueries(snap)
	if len(queries) == 0 {
		return nil, nil, false
	}

	project := snap.Store.EffectiveBillingProject()
	if project == "" {
		return nil, &CostErrorDetail{
			Source:  "config",
			Message: "No GCP project found (set bqpls.defaultProject, GOOGLE_CLOUD_PROJECT, or run gcloud config set project).",
		}, false
	}

	dataset := snap.Store.DefaultDataset()
	for _, q := range queries {
		if snap.Store.IsDryRunPending(project, dataset, q) {
			pending = true
		}

		if dryErr, ok := snap.Store.GetDryRunError(project, dataset, q); ok && dryErr != nil {
			if dryErr.Rejected {
				rejected = append(rejected, CostErrorDetail{
					Source:  "dry_run",
					Message: dryErr.Message,
				})
			} else if unavailable == nil {
				unavailable = &CostErrorDetail{
					Source:  "dry_run",
					Message: dryErr.Message,
				}
			}
		}
	}

	return rejected, unavailable, pending
}

func tryScriptDryRunEstimate(snap *DocumentSnapshot, price float64) (StatementCostEstimate, bool) {
	if snap.Store == nil {
		return StatementCostEstimate{}, false
	}

	project := snap.Store.EffectiveBillingProject()
	if project == "" {
		return StatementCostEstimate{}, false
	}

	scriptSQL, ok := wholeScriptDryRunSQL(snap)
	if !ok {
		return StatementCostEstimate{}, false
	}

	res, hit := snap.Store.GetDryRun(project, snap.Store.DefaultDataset(), scriptSQL)
	if !hit || res == nil {
		return StatementCostEstimate{}, false
	}

	firstStmt := snap.Statements[0]
	est := buildDryRunStatementEstimate(snap, firstStmt, 0, res, price, true)

	return est, true
}

func estimateStatementCost(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	idx int,
	price float64,
) (StatementCostEstimate, bool) {
	if snap.Store != nil && st.DryRunEligible && st.ParseErr == nil && !st.HasExprPlaceholder {
		if project := snap.Store.EffectiveBillingProject(); project != "" {
			trimmed := strings.TrimSpace(st.SQL)
			if res, ok := snap.Store.GetDryRun(project, snap.Store.DefaultDataset(), trimmed); ok && res != nil {
				return buildDryRunStatementEstimate(snap, st, idx, res, price, false), true
			}

			if dryErr, ok := snap.Store.GetDryRunError(project, snap.Store.DefaultDataset(), trimmed); ok && dryErr != nil && dryErr.Rejected {
				return StatementCostEstimate{}, false
			}
		}
	}

	return buildOfflineStatementEstimate(snap, st, idx, price)
}

func buildDryRunStatementEstimate(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	idx int,
	res *catalog.DryRunResult,
	price float64,
	isScript bool,
) StatementCostEstimate {
	bytesProcessed := max(res.TotalBytesProcessed, 0)
	bytesBilled := max(res.TotalBytesBilled, bytesProcessed)
	usd := EstimateCostUSD(bytesBilled, price)
	if res.CacheHit {
		bytesBilled = 0
		usd = 0
	}

	exceeds, reason := checkCostThreshold(snap.Options, bytesProcessed, usd)
	est := StatementCostEstimate{
		Index:            idx,
		Range:            statementOrigRange(snap, st),
		HeaderRange:      statementHeaderRange(snap, st),
		BytesProcessed:   bytesProcessed,
		BytesBilled:      bytesBilled,
		EstimatedCostUSD: usd,
		FormattedBytes:   FormatBytes(bytesProcessed),
		FormattedCost:    FormatUSD(usd),
		Source:           CostSourceDryRun,
		Accuracy:         res.Accuracy,
		StatementType:    res.StatementType,
		CacheHit:         res.CacheHit,
		ExceedsThreshold: exceeds,
		ThresholdReason:  reason,
	}
	est.Label = formatEstimateLabel(&est, isScript)

	return est
}

func buildOfflineStatementEstimate(
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	idx int,
	price float64,
) (StatementCostEstimate, bool) {
	tables := collectOfflineTablesForStmt(snap, st)
	if len(tables) == 0 {
		return StatementCostEstimate{}, false
	}

	var (
		totalBytes        int64
		knownCount        int
		partitionFiltered bool
	)

	for _, tbl := range tables {
		if tbl.HasNumBytes {
			totalBytes += max(tbl.NumBytes, 0)
			knownCount++
		}

		if tbl.PartitionFiltered {
			partitionFiltered = true
		}
	}

	if knownCount == 0 {
		return StatementCostEstimate{}, false
	}

	usd := EstimateCostUSD(totalBytes, price)
	exceeds, reason := checkCostThreshold(snap.Options, totalBytes, usd)

	est := StatementCostEstimate{
		Index:             idx,
		Range:             statementOrigRange(snap, st),
		HeaderRange:       statementHeaderRange(snap, st),
		BytesProcessed:    totalBytes,
		BytesBilled:       totalBytes,
		EstimatedCostUSD:  usd,
		FormattedBytes:    FormatBytes(totalBytes),
		FormattedCost:     FormatUSD(usd),
		Source:            CostSourceOffline,
		Accuracy:          "UPPER_BOUND",
		PartitionFiltered: partitionFiltered,
		ExceedsThreshold:  exceeds,
		ThresholdReason:   reason,
		ReferencedTables:  tables,
	}
	est.Label = formatEstimateLabel(&est, false)

	return est, true
}

func collectOfflineTablesForStmt(snap *DocumentSnapshot, st *AnalyzedStatement) []TableCostBreakdown {
	if len(st.ScannedTables) > 0 {
		return slices.Clone(st.ScannedTables)
	}

	if snap.Store == nil || len(st.ASTTableRefs) == 0 {
		return nil
	}

	defProj, defDS := storeDefaultProjectDataset(snap)
	seen := make(map[string]struct{}, len(st.ASTTableRefs))
	out := make([]TableCostBreakdown, 0, len(st.ASTTableRefs))

	for _, parts := range st.ASTTableRefs {
		ref := tableRefFromPath(parts, defProj, defDS)
		if ref.Table == "" {
			continue
		}

		key := strings.ToLower(ref.FullName())
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		meta, ok := snap.Store.GetTable(ref)
		if !ok || meta == nil {
			continue
		}

		entry := TableCostBreakdown{
			Table:       meta.Ref.FullName(),
			Partitioned: meta.EffectivePartitioning() != nil,
		}
		if meta.NumBytes != nil {
			entry.NumBytes = meta.NumBytes.Int64()
			entry.HasNumBytes = true
		}

		out = append(out, entry)
	}

	return out
}

func finalizeDocumentSummary(summary *DocumentCostSummary, price float64) {
	if len(summary.Statements) == 0 {
		return
	}

	allDryRun := true
	for _, st := range summary.Statements {
		summary.TotalBytesProcessed += st.BytesProcessed
		summary.TotalBytesBilled += st.BytesBilled
		if st.ExceedsThreshold {
			summary.ExceedsThreshold = true
		}
		if st.Source != CostSourceDryRun {
			allDryRun = false
		}
	}

	summary.TotalEstimatedCostUSD = EstimateCostUSD(summary.TotalBytesBilled, price)
	summary.FormattedBytes = FormatBytes(summary.TotalBytesProcessed)
	summary.FormattedCost = FormatUSD(summary.TotalEstimatedCostUSD)

	if allDryRun {
		summary.Source = CostSourceDryRun
	} else {
		summary.Source = CostSourceOffline
	}

	if len(summary.Statements) == 1 {
		summary.Summary = summary.Statements[0].Label

		return
	}

	prefix := "Est."
	if summary.ExceedsThreshold {
		prefix = "⚠ Est."
	}

	if allDryRun {
		summary.Summary = fmt.Sprintf("%s %s (%s)", prefix, summary.FormattedBytes, summary.FormattedCost)
	} else {
		summary.Summary = fmt.Sprintf("%s ≤ %s (%s, offline)", prefix, summary.FormattedBytes, summary.FormattedCost)
	}
}

func formatEstimateLabel(est *StatementCostEstimate, isScript bool) string {
	prefix := "Est."
	if isScript {
		prefix = "Script est."
	}
	if est.ExceedsThreshold {
		prefix = "⚠ " + prefix
	}

	if est.Source == CostSourceDryRun {
		if est.CacheHit {
			return fmt.Sprintf("%s %s (cached)", prefix, est.FormattedBytes)
		}

		bound := ""
		switch est.Accuracy {
		case "LOWER_BOUND":
			bound = "≥ "
		case "UPPER_BOUND":
			bound = "≤ "
		}

		return fmt.Sprintf("%s %s%s (%s)", prefix, bound, est.FormattedBytes, est.FormattedCost)
	}

	if est.PartitionFiltered {
		return fmt.Sprintf("%s ≤ %s (%s, offline, partition-filtered)", prefix, est.FormattedBytes, est.FormattedCost)
	}

	return fmt.Sprintf("%s ≤ %s (%s, offline)", prefix, est.FormattedBytes, est.FormattedCost)
}

func checkCostThreshold(opts *bqconfig.LSPOptions, bytes int64, usd float64) (exceeds bool, reason string) {
	if opts == nil {
		return false, ""
	}

	if opts.CostWarningThresholdBytes > 0 && bytes > opts.CostWarningThresholdBytes {
		return true, FormatBytes(opts.CostWarningThresholdBytes)
	}

	if opts.CostWarningThresholdUSD > 0 && usd > opts.CostWarningThresholdUSD {
		return true, FormatUSD(opts.CostWarningThresholdUSD)
	}

	return false, ""
}

func statementOrigRange(snap *DocumentSnapshot, st *AnalyzedStatement) syntax.Range {
	start := max(st.OrigSpan.Start, 0)
	end := max(st.OrigSpan.End, start)

	for _, tok := range snap.Tokens {
		if tok.End <= start {
			continue
		}
		if tok.Start >= end {
			break
		}
		if !tok.IsTrivia() {
			start = tok.Start

			break
		}
	}

	return snap.LineIndex.RangeAt(start, end)
}

func statementHeaderRange(snap *DocumentSnapshot, st *AnalyzedStatement) syntax.Range {
	start := max(st.OrigSpan.Start, 0)
	end := max(st.OrigSpan.End, start)

	for _, tok := range snap.Tokens {
		if tok.End <= start {
			continue
		}
		if tok.Start >= end {
			break
		}
		if !tok.IsTrivia() && tok.End > tok.Start {
			return snap.LineIndex.RangeAt(tok.Start, tok.End)
		}
	}

	return tokenRangeAtOffset(snap.LineIndex, snap.Tokens, snap.Text, start)
}

func appendCostDiagnostics(out []Diagnostic, snap *DocumentSnapshot) []Diagnostic {
	if snap == nil || !isCostEstimationEnabled(snap.Options) {
		return out
	}

	sev, enabled := mapCostDiagnosticSeverity(snap.Options)
	if !enabled {
		return out
	}

	if snap.Options == nil || (snap.Options.CostWarningThresholdBytes <= 0 && snap.Options.CostWarningThresholdUSD <= 0) {
		return out
	}

	summary := computeDocumentCost(snap)
	for _, st := range summary.Statements {
		if !st.ExceedsThreshold {
			continue
		}

		msg := fmt.Sprintf(
			"estimated query scan %s (%s) exceeds threshold (%s)",
			st.FormattedBytes,
			st.FormattedCost,
			st.ThresholdReason,
		)
		if st.Source == CostSourceOffline {
			msg = fmt.Sprintf(
				"estimated query scan ≤ %s (%s, offline upper bound) exceeds threshold (%s)",
				st.FormattedBytes,
				st.FormattedCost,
				st.ThresholdReason,
			)
		}

		out = append(out, Diagnostic{
			Range:    st.HeaderRange,
			Severity: sev,
			Source:   "bqpls",
			Code:     DiagCodeEstimatedCost,
			Message:  msg,
		})
	}

	return out
}

func mapCostDiagnosticSeverity(opts *bqconfig.LSPOptions) (DiagnosticSeverity, bool) {
	if opts == nil {
		return SeverityWarning, true
	}

	return mapConfigSeverity(opts.CostDiagnosticSeverity)
}

func isDryRunEligibleAST(astStmt googlesql.ASTStatementNode) bool {
	if isNilNode(astStmt) {
		return false
	}

	switch s := astStmt.(type) {
	case *googlesql.ASTQueryStatement,
		*googlesql.ASTStatementWithPipeOperators,
		*googlesql.ASTInsertStatement,
		*googlesql.ASTUpdateStatement,
		*googlesql.ASTDeleteStatement,
		*googlesql.ASTMergeStatement,
		*googlesql.ASTCreateViewStatement,
		*googlesql.ASTCreateMaterializedViewStatement,
		*googlesql.ASTExportDataStatement:
		return true
	case *googlesql.ASTCreateTableStatement:
		q, err := s.Query()

		return err == nil && !isNilNode(q)
	default:
		return false
	}
}

func collectASTTablePartsList(root googlesql.ASTNode) [][]string {
	if isNilNode(root) {
		return nil
	}

	var out [][]string
	walkASTNodes(root, func(n googlesql.ASTNode) bool {
		tp, ok := n.(*googlesql.ASTTablePathExpression)
		if !ok || isNilNode(tp) {
			return true
		}

		pe, err := tp.PathExpr()
		if err != nil || isNilNode(pe) {
			return true
		}

		parts, _ := pe.ToIdentifierVector()
		norm := normalizeASTTableParts(parts)
		if len(norm) > 0 {
			out = append(out, norm)
		}

		return true
	})

	return out
}

func collectResolvedScannedTables(rs googlesql.ResolvedNode, cat *catalog.Catalog) []TableCostBreakdown {
	if isNilNode(rs) || cat == nil {
		return nil
	}

	scanByID, colOrigins, scanCount := collectResolvedScanMetadata(rs, cat)
	if scanCount == 0 {
		return nil
	}

	propagateScanColOrigins(rs, colOrigins)
	filteredScans := collectSargablePartitionScans(rs, colOrigins)

	return buildScannedTableBreakdowns(scanByID, filteredScans, scanCount)
}

func collectResolvedScanMetadata(
	rs googlesql.ResolvedNode,
	cat *catalog.Catalog,
) (map[int]*catalog.TableMeta, map[int32][]scanColOrigin, int) {
	colOrigins := make(map[int32][]scanColOrigin)
	scanByID := make(map[int]*catalog.TableMeta)
	scanCounter := 0

	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		scan, ok := n.(*googlesql.ResolvedTableScan)
		if !ok || isNilNode(scan) {
			return true
		}

		tbl, err := scan.Table()
		if err != nil || isNilNode(tbl) {
			return true
		}

		meta, found := cat.LookupTable(tbl)
		if !found || meta == nil {
			return true
		}

		scanCounter++
		scanByID[scanCounter] = meta
		if meta.EffectivePartitioning() != nil {
			recordScanColOrigins(scan, tbl, meta, scanCounter, colOrigins)
		}

		return true
	})

	return scanByID, colOrigins, scanCounter
}

func buildScannedTableBreakdowns(
	scanByID map[int]*catalog.TableMeta,
	filteredScans map[int]bool,
	scanCount int,
) []TableCostBreakdown {
	byKey := make(map[string]int, scanCount)
	out := make([]TableCostBreakdown, 0, scanCount)

	for id := 1; id <= scanCount; id++ {
		meta := scanByID[id]
		if meta == nil {
			continue
		}

		key := strings.ToLower(meta.Ref.FullName())
		if idx, exists := byKey[key]; exists {
			if filteredScans[id] {
				out[idx].PartitionFiltered = true
			}

			continue
		}

		entry := TableCostBreakdown{
			Table:             meta.Ref.FullName(),
			Partitioned:       meta.EffectivePartitioning() != nil,
			PartitionFiltered: filteredScans[id],
		}
		if meta.NumBytes != nil {
			entry.NumBytes = meta.NumBytes.Int64()
			entry.HasNumBytes = true
		}

		byKey[key] = len(out)
		out = append(out, entry)
	}

	return out
}
