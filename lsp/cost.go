package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/paulourio/bqlang/bqconfig"
	"github.com/paulourio/bqlang/catalog"
	"github.com/paulourio/bqlang/internal/analysis"
)

const queryCostNotificationMethod = "bqpls/queryCost"

func (s *Server) onDryRunUpdate() {
	if s.engine != nil {
		s.engine.InvalidateCost()
	}

	s.triggerCostRefresh()
}

func (s *Server) triggerCostRefresh() {
	select {
	case s.costRefreshCh <- struct{}{}:
	default:
	}
}

func (s *Server) refreshOpenDocCosts(ctx context.Context, tr *Transport, cmdQueue *commandQueue) {
	done := make(chan struct{})
	if !cmdQueue.Push(queuedMessage{
		itemKind: queueItemCostRefresh,
		done:     done,
	}) {
		return
	}

	select {
	case <-done:
	case <-ctx.Done():
		return
	}

	s.sendCodeLensRefresh(ctx, tr)
}

func (s *Server) sendCodeLensRefresh(ctx context.Context, tr *Transport) {
	if ctx.Err() != nil || s.currentState() != stateInitialized || !s.codeLensRefreshSupport.Load() {
		return
	}

	refreshCtx, cancel := context.WithTimeout(ctx, semanticRefreshPeriod)
	defer cancel()

	if _, err := tr.SendRequest(refreshCtx, "workspace/codeLens/refresh", nil); err != nil && ctx.Err() == nil {
		s.logger.DebugContext(ctx, "codeLens/refresh request finished", slog.Any("error", err))
	}
}

func (s *Server) refreshAllOpenDocCostState(ctx context.Context, tr *Transport) {
	uris := s.snapshotOpenURIs()
	for _, uri := range uris {
		if ctx.Err() != nil || s.currentState() != stateInitialized {
			return
		}

		s.publishDocDiagnostics(ctx, tr, uri)
		s.publishDocCost(ctx, tr, uri)
	}
}

func (s *Server) publishDocCost(ctx context.Context, tr *Transport, uri string) {
	s.costMu.Lock()
	defer s.costMu.Unlock()

	version, statusEnabled, wasPublished, ok := s.readDocCostState(uri)
	if !ok {
		return
	}

	var payload *analysis.DocumentCostSummary
	if statusEnabled {
		var err error
		payload, err = s.engine.DocumentCost(ctx, uri)
		if err != nil {
			if !errors.Is(err, analysis.ErrDocumentNotOpen) && !errors.Is(err, context.Canceled) {
				s.logger.WarnContext(ctx, "cost computation failed", slog.String("uri", uri), slog.Any("error", err))
			}

			return
		}
	} else {
		payload = &analysis.DocumentCostSummary{
			URI:        uri,
			Version:    version,
			Enabled:    false,
			Statements: []analysis.StatementCostEstimate{},
		}
	}

	if !s.isDocCostStateCurrent(uri, version, statusEnabled) {
		return
	}

	hasData := payload.Enabled && (len(payload.Statements) > 0 || payload.Status != analysis.CostStatusEmpty)
	if !hasData && !wasPublished {
		return
	}

	if err := tr.SendNotification(queryCostNotificationMethod, payload); err != nil {
		s.logger.DebugContext(ctx, "failed to publish query cost", slog.String("uri", uri), slog.Any("error", err))

		return
	}

	s.setDocCostPublished(uri, hasData)
}

func (s *Server) publishEmptyDocCost(ctx context.Context, tr *Transport, uri string) {
	s.costMu.Lock()
	defer s.costMu.Unlock()

	if err := tr.SendNotification(queryCostNotificationMethod, &analysis.DocumentCostSummary{
		URI:        uri,
		Enabled:    false,
		Statements: []analysis.StatementCostEstimate{},
	}); err != nil {
		s.logger.DebugContext(ctx, "failed to clear query cost on close", slog.String("uri", uri), slog.Any("error", err))
	}
}

func (s *Server) readDocCostState(uri string) (version int, statusEnabled, wasPublished, ok bool) {
	s.docsMu.RLock()
	doc, exists := s.docs[uri]
	if exists {
		version = doc.version
		statusEnabled = doc.costStatusBarEnabled
		wasPublished = doc.hasPublishedCost
	}
	s.docsMu.RUnlock()

	return version, statusEnabled, wasPublished, exists
}

func (s *Server) setDocCostPublished(uri string, published bool) {
	s.docsMu.Lock()
	if cur, exists := s.docs[uri]; exists {
		cur.hasPublishedCost = published
	}
	s.docsMu.Unlock()
}

func (s *Server) isDocCostStateCurrent(uri string, version int, statusEnabled bool) bool {
	s.docsMu.RLock()
	cur, ok := s.docs[uri]
	current := ok && cur.version == version && cur.costStatusBarEnabled == statusEnabled
	s.docsMu.RUnlock()

	return current
}

func (s *Server) handleCodeLens(ctx context.Context, raw json.RawMessage) (any, *ResponseError) {
	params, rpcErr := decodeParams[CodeLensParams](raw)
	if rpcErr != nil {
		return nil, rpcErr
	}

	lenses, err := s.engine.CodeLenses(ctx, params.TextDocument.URI)
	if err != nil {
		return nil, mapAnalysisError(err)
	}

	if lenses == nil {
		return []analysis.CodeLens{}, nil
	}

	return lenses, nil
}

func (s *Server) handleRefreshCost(ctx context.Context, raw json.RawMessage) (any, *ResponseError) {
	params, rpcErr := decodeParams[RefreshCostParams](raw)
	if rpcErr != nil {
		return nil, rpcErr
	}

	uri := params.TextDocument.URI
	s.cancelDryRunTimer(uri)
	enqueued := s.triggerDryRunForURI(uri, true)

	summary, err := s.engine.DocumentCost(ctx, uri)
	if err != nil {
		return nil, mapAnalysisError(err)
	}

	if !enqueued {
		s.triggerCostRefresh()
	}

	return summary, nil
}

func (s *Server) scheduleDryRun(uri string, immediate bool) {
	s.docsMu.RLock()
	doc, ok := s.docs[uri]
	debounceMs := 0
	if ok {
		debounceMs = doc.costDryRunDebounceMs
	}
	s.docsMu.RUnlock()

	if !ok {
		return
	}

	if immediate || debounceMs <= 0 {
		s.cancelDryRunTimer(uri)
		s.triggerDryRunForURI(uri, false)

		return
	}

	delay := time.Duration(debounceMs) * time.Millisecond

	s.dryRunTimersMu.Lock()
	if s.dryRunTimersClosed {
		s.dryRunTimersMu.Unlock()

		return
	}

	if prev, exists := s.dryRunTimers[uri]; exists {
		prev.Stop()
	}

	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		s.dryRunTimersMu.Lock()
		if s.dryRunTimers[uri] == timer {
			delete(s.dryRunTimers, uri)
		}
		closed := s.dryRunTimersClosed
		s.dryRunTimersMu.Unlock()

		if !closed {
			s.triggerDryRunForURI(uri, false)
		}
	})
	s.dryRunTimers[uri] = timer
	s.dryRunTimersMu.Unlock()
}

func (s *Server) cancelDryRunTimer(uri string) {
	s.dryRunTimersMu.Lock()
	if timer, exists := s.dryRunTimers[uri]; exists {
		timer.Stop()
		delete(s.dryRunTimers, uri)
	}
	s.dryRunTimersMu.Unlock()
}

func (s *Server) cancelAllDryRunTimers() {
	s.dryRunTimersMu.Lock()
	s.dryRunTimersClosed = true
	for uri, timer := range s.dryRunTimers {
		timer.Stop()
		delete(s.dryRunTimers, uri)
	}
	s.dryRunTimersMu.Unlock()
}

func (s *Server) triggerDryRunForURI(uri string, force bool) bool {
	if s.currentState() != stateInitialized {
		return false
	}

	store := s.storeForURI(uri)
	if store == nil {
		return false
	}

	project, dataset, targets, ok := s.engine.DryRunTargetSpecs(uri)
	if !ok || len(targets) == 0 {
		return false
	}

	enqueued := false
	for _, target := range targets {
		if store.RequestDryRun(project, dataset, target.SQL, force, target.ReferencedTables...) {
			enqueued = true
		}
	}

	return enqueued
}

func (s *Server) storeForURI(uri string) *catalog.Store {
	s.docsMu.RLock()
	doc, ok := s.docs[uri]
	var store *catalog.Store
	if ok {
		store = doc.store
	}
	s.docsMu.RUnlock()

	if store != nil {
		return store
	}

	return s.defaultStore
}

func docCostStatusEnabled(opts *bqconfig.LSPOptions) bool {
	return opts == nil || (opts.CostEstimationEnabled && opts.CostStatusBarEnabled)
}

func costConfigKey(opts *bqconfig.LSPOptions) string {
	if opts == nil {
		return ""
	}

	return fmt.Sprintf(
		"%t\x00%t\x00%t\x00%t\x00%s\x00%d\x00%.6f\x00%.6f\x00%d",
		opts.CostEstimationEnabled,
		opts.CostCodeLensEnabled,
		opts.CostStatusBarEnabled,
		opts.CostHoverEnabled,
		opts.CostDiagnosticSeverity,
		opts.CostWarningThresholdBytes,
		opts.CostWarningThresholdUSD,
		opts.CostPricePerTiBUSD,
		opts.CostDryRunDebounceMs,
	)
}
