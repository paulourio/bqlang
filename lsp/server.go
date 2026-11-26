package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/paulourio/bqlang/bqconfig"
	"github.com/paulourio/bqlang/catalog"
	"github.com/paulourio/bqlang/formatter"
	"github.com/paulourio/bqlang/internal/analysis"
	"github.com/paulourio/bqlang/internal/syntax"
)

const (
	serverName            = "bqpls"
	defaultServerVersion  = "dev"
	semanticRefreshPeriod = 5 * time.Second
)

var errCancelledByClient = errors.New("request cancelled by client")

type serverLifecycleState uint8

const (
	stateUninitialized serverLifecycleState = iota
	stateInitialized
	stateShutdown
)

// ServerOptions configures a bqpls LSP Server instance.
type ServerOptions struct {
	// ConfigPath points to an explicit .bqfmt.toml file. When empty, the server
	// discovers .bqfmt.toml upward from each document's directory.
	ConfigPath string

	// Logger receives structured server logs. When nil, logs are discarded.
	Logger *slog.Logger

	// Version is reported in InitializeResult.ServerInfo.Version (default "dev").
	Version string

	// StoreOptions are appended when the server creates catalog stores.
	StoreOptions []catalog.StoreOption

	// Store, when non-nil, is used as the default catalog store instead of
	// creating one. The server does not Close an injected Store; documents whose
	// directory resolves to a different .bqfmt.toml may still get their own
	// server-owned stores.
	Store *catalog.Store

	// TokenProvider, when non-nil, replaces the default ADC→gcloud chain for
	// every RestClient the server creates (metadata fetches and dry runs).
	TokenProvider catalog.TokenProvider

	// DefaultLSPOptions, when non-nil, replaces bqconfig.DefaultLSPOptions()
	// as the base configuration when no .bqfmt.toml is found.
	DefaultLSPOptions *bqconfig.LSPOptions
}

type openDocState struct {
	uri                  string
	version              int
	diagnosticsEnabled   bool
	costStatusBarEnabled bool
	hasPublishedCost     bool
	costDryRunDebounceMs int
	storeKey             string
	costKey              string
	store                *catalog.Store
}

type inflightEntry struct {
	cancel context.CancelCauseFunc
}

type queueItemKind uint8

const (
	queueItemInbound queueItemKind = iota
	queueItemRefresh
	queueItemCostRefresh
)

type queuedMessage struct {
	itemKind  queueItemKind
	msg       RPCMessage
	kind      MessageKind
	idKey     string
	reqCtx    context.Context //nolint:containedctx // per-request cancellation context created on readLoop
	reqCancel context.CancelCauseFunc
	done      chan struct{}
}

type commandQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []queuedMessage
	closed bool
}

func newCommandQueue() *commandQueue {
	q := &commandQueue{}
	q.cond = sync.NewCond(&q.mu)

	return q
}

func (q *commandQueue) Push(item queuedMessage) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return false
	}

	q.items = append(q.items, item)
	q.cond.Signal()

	return true
}

func (q *commandQueue) Pop() (queuedMessage, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for len(q.items) == 0 && !q.closed {
		q.cond.Wait()
	}

	if len(q.items) == 0 {
		return queuedMessage{}, false
	}

	item := q.items[0]
	q.items[0] = queuedMessage{}
	q.items = q.items[1:]
	if len(q.items) == 0 {
		q.items = nil
	}

	return item, true
}

func (q *commandQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

func (q *commandQueue) DrainAndClose() []queuedMessage {
	q.mu.Lock()
	q.closed = true
	rem := q.items
	q.items = nil
	q.cond.Broadcast()
	q.mu.Unlock()

	return rem
}

// Server implements a concurrent JSON-RPC 2.0 / LSP 3.17 language server for BigQuery SQL and Jinja2/dbt.
type Server struct {
	opts              ServerOptions
	defaultLSPOptions *bqconfig.LSPOptions
	logger            *slog.Logger
	loader            *bqconfig.Loader
	engine            *analysis.Engine

	storesMu     sync.Mutex
	stores       map[string]*catalog.Store
	ownedStores  []*catalog.Store
	storeUnsubs  []func()
	defaultStore *catalog.Store
	storesClosed bool

	diagMu           sync.Mutex
	costMu           sync.Mutex
	docsMu           sync.RWMutex
	docs             map[string]*openDocState
	rootDir          string
	clientLSP        *bqconfig.LSPOptions
	clientLSPDefined bqconfig.LSPDefinedFields

	stateMu                sync.Mutex
	lifecycle              serverLifecycleState
	shutdownReceived       atomic.Bool
	exitReceived           atomic.Bool
	refreshSupport         atomic.Bool
	codeLensRefreshSupport atomic.Bool
	sigLabelOffsetSupport  atomic.Bool

	inflightMu sync.Mutex
	inflight   map[string]inflightEntry

	refreshCh     chan struct{}
	costRefreshCh chan struct{}

	dryRunTimersMu     sync.Mutex
	dryRunTimers       map[string]*time.Timer
	dryRunTimersClosed bool

	serveMu     sync.Mutex
	serveCancel context.CancelFunc

	closeMu sync.Mutex
	closed  bool
}

// NewServer creates a Server with a shared bqconfig.Loader, default catalog.Store,
// and incremental analysis.Engine.
func NewServer(opts ServerOptions) (*Server, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	if opts.Version == "" {
		opts.Version = defaultServerVersion
	}

	opts.Logger = logger

	var defaultLSPOpts *bqconfig.LSPOptions
	if opts.DefaultLSPOptions != nil {
		if err := opts.DefaultLSPOptions.Validate(); err != nil {
			return nil, fmt.Errorf("NewServer default LSP options: %w", err)
		}

		defaultLSPOpts = cloneLSPOptions(opts.DefaultLSPOptions)
	}

	loader, err := bqconfig.NewLoader(opts.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("NewServer config loader: %w", err)
	}

	s := &Server{
		opts:              opts,
		defaultLSPOptions: defaultLSPOpts,
		logger:            logger,
		loader:            loader,
		stores:            make(map[string]*catalog.Store),
		docs:              make(map[string]*openDocState),
		inflight:          make(map[string]inflightEntry),
		refreshCh:         make(chan struct{}, 1),
		costRefreshCh:     make(chan struct{}, 1),
		dryRunTimers:      make(map[string]*time.Timer),
	}

	defaultCfg := s.loadInitialDefaultConfig()

	defaultStore, err := s.initDefaultStore(opts.ConfigPath != "", defaultCfg.LSP)
	if err != nil {
		return nil, err
	}

	eng, err := analysis.NewEngine(defaultStore, defaultCfg.LSP)
	if err != nil {
		_ = s.closeOwnedStores()

		return nil, fmt.Errorf("NewServer analysis engine: %w", err)
	}

	s.defaultStore = defaultStore
	s.engine = eng

	return s, nil
}

func cloneLSPOptions(opts *bqconfig.LSPOptions) *bqconfig.LSPOptions {
	if opts == nil {
		return bqconfig.DefaultLSPOptions()
	}

	cp := *opts
	cp.SchemaDirs = slices.Clone(opts.SchemaDirs)
	cp.ProjectCacheTTLSeconds = bqconfig.NormalizeProjectCacheTTLs(opts.ProjectCacheTTLSeconds)

	return &cp
}

func (s *Server) baseLSPOptions() *bqconfig.LSPOptions {
	if s.defaultLSPOptions != nil {
		return cloneLSPOptions(s.defaultLSPOptions)
	}

	return bqconfig.DefaultLSPOptions()
}

func (s *Server) loadInitialDefaultConfig() *bqconfig.LoadedConfig {
	return s.normalizeLoadedConfig(loadInitialDefaultConfig(s.loader, s.logger))
}

func loadInitialDefaultConfig(loader *bqconfig.Loader, logger *slog.Logger) *bqconfig.LoadedConfig {
	cfg, err := loader.ConfigFor("", true)
	if err == nil && cfg != nil {
		return cfg
	}

	if err != nil {
		logger.Warn("failed to load initial config; using defaults", slog.Any("error", err))
	}

	return &bqconfig.LoadedConfig{
		PrintOptions: formatter.DefaultPrintOptions(),
		LSP:          bqconfig.DefaultLSPOptions(),
	}
}

func (s *Server) buildStoreOptions() []catalog.StoreOption {
	extra := 1
	if s.opts.TokenProvider != nil {
		extra++
	}

	storeOpts := make([]catalog.StoreOption, 0, len(s.opts.StoreOptions)+extra)
	storeOpts = append(storeOpts, catalog.WithLogger(s.logger))
	if s.opts.TokenProvider != nil {
		storeOpts = append(storeOpts, catalog.WithRestOptions(catalog.WithTokenProvider(s.opts.TokenProvider)))
	}
	storeOpts = append(storeOpts, s.opts.StoreOptions...)

	return storeOpts
}

func (s *Server) subscribeStoreLocked(st *catalog.Store) {
	if st == nil {
		return
	}

	s.storeUnsubs = append(
		s.storeUnsubs,
		st.Subscribe(s.onCatalogUpdate),
		st.SubscribeDryRun(s.onDryRunUpdate),
	)
}

func (s *Server) initDefaultStore(explicit bool, lspOpts *bqconfig.LSPOptions) (*catalog.Store, error) {
	if s.opts.Store != nil {
		s.stores[storeCacheKey(lspOpts)] = s.opts.Store
		s.subscribeStoreLocked(s.opts.Store)

		return s.opts.Store, nil
	}

	storeOpts := s.buildStoreOptions()
	store, err := catalog.NewStore(lspOpts, storeOpts...)
	if err != nil && !explicit {
		s.logger.Warn("failed to create initial catalog store; falling back to defaults", slog.Any("error", err))
		lspOpts = s.baseLSPOptions()
		store, err = catalog.NewStore(lspOpts, storeOpts...)
	}

	if err != nil {
		return nil, fmt.Errorf("NewServer catalog store: %w", err)
	}

	s.stores[storeCacheKey(lspOpts)] = store
	s.ownedStores = append(s.ownedStores, store)
	s.subscribeStoreLocked(store)

	return store, nil
}

// Serve runs the JSON-RPC 2.0 / LSP 3.17 event loop over r and w until ctx is
// canceled, EOF is reached, or exit is processed.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	serveCtx, serveCancel := context.WithCancel(ctx)
	refreshCtx, refreshCancel := context.WithCancel(serveCtx)

	s.serveMu.Lock()
	s.serveCancel = serveCancel
	s.serveMu.Unlock()

	tr := NewTransport(r, w)
	cmdQueue := newCommandQueue()

	var (
		dispatchWG sync.WaitGroup
		workerWG   sync.WaitGroup
		refreshWG  sync.WaitGroup
	)

	refreshWG.Add(1)
	go s.runRefreshWorker(refreshCtx, tr, cmdQueue, &refreshWG)

	dispatchWG.Add(1)
	go s.runDispatcher(serveCtx, tr, cmdQueue, &dispatchWG, &workerWG, refreshCancel)

	readErr := s.runReadLoop(serveCtx, tr, cmdQueue, serveCancel, refreshCancel)

	if s.exitReceived.Load() {
		s.cancelAllDryRunTimers()
		refreshCancel()
		serveCancel()
		tr.FailPending(ErrTransportClosed)
		s.cancelAllInflight(errCancelledByClient)
		rem := cmdQueue.DrainAndClose()
		releaseQueuedRequests(rem)
		dispatchWG.Wait()
		workerWG.Wait()
		refreshWG.Wait()
		_ = s.closeOwnedStores()

		if !s.shutdownReceived.Load() {
			return ErrExitedWithoutShutdown
		}

		return nil
	}

	// Clean EOF or context cancellation: drain queued commands before stopping refresh.
	s.cancelAllDryRunTimers()
	cmdQueue.Close()
	dispatchWG.Wait()
	workerWG.Wait()

	refreshCancel()
	serveCancel()
	tr.FailPending(ErrTransportClosed)
	refreshWG.Wait()
	_ = s.closeOwnedStores()

	if readErr != nil && !errors.Is(readErr, io.EOF) {
		if errors.Is(readErr, context.Canceled) && ctx.Err() == nil && s.isClosed() {
			return nil
		}

		return readErr
	}

	return ctx.Err()
}

func (s *Server) isClosed() bool {
	s.closeMu.Lock()
	closed := s.closed
	s.closeMu.Unlock()

	return closed
}

// Close cancels active serving, unsubscribes from all catalog stores, and closes
// all server-owned catalog stores (leaving any injected ServerOptions.Store open).
// It is safe to call multiple times and must not be called from Store callbacks.
func (s *Server) Close() error {
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()

		return nil
	}
	s.closed = true
	s.closeMu.Unlock()

	s.cancelAllDryRunTimers()

	s.serveMu.Lock()
	cancel := s.serveCancel
	s.serveMu.Unlock()
	if cancel != nil {
		cancel()
	}

	return s.closeOwnedStores()
}

func (s *Server) closeOwnedStores() error {
	s.storesMu.Lock()
	if s.storesClosed {
		s.storesMu.Unlock()

		return nil
	}

	s.storesClosed = true
	unsubs := slices.Clone(s.storeUnsubs)
	s.storeUnsubs = nil
	stores := slices.Clone(s.ownedStores)
	s.storesMu.Unlock()

	for _, unsub := range unsubs {
		if unsub != nil {
			unsub()
		}
	}

	var firstErr error
	for _, st := range stores {
		if st == nil {
			continue
		}

		if err := st.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

func (s *Server) onCatalogUpdate(ref catalog.TableRef) {
	if s.engine != nil {
		s.engine.InvalidateCatalog(ref)
	}

	select {
	case s.refreshCh <- struct{}{}:
	default:
	}
}

func (s *Server) runRefreshWorker(
	ctx context.Context,
	tr *Transport,
	cmdQueue *commandQueue,
	wg *sync.WaitGroup,
) {
	defer wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.refreshCh:
			if ctx.Err() != nil || s.currentState() != stateInitialized {
				continue
			}

			s.refreshOpenDocuments(ctx, tr, cmdQueue)
		case <-s.costRefreshCh:
			if ctx.Err() != nil || s.currentState() != stateInitialized {
				continue
			}

			s.refreshOpenDocCosts(ctx, tr, cmdQueue)
		}
	}
}

func (s *Server) refreshOpenDocuments(ctx context.Context, tr *Transport, cmdQueue *commandQueue) {
	done := make(chan struct{})
	if !cmdQueue.Push(queuedMessage{
		itemKind: queueItemRefresh,
		done:     done,
	}) {
		return
	}

	select {
	case <-done:
	case <-ctx.Done():
		return
	}

	if ctx.Err() != nil || s.currentState() != stateInitialized {
		return
	}

	if s.refreshSupport.Load() {
		refreshCtx, cancel := context.WithTimeout(ctx, semanticRefreshPeriod)
		if _, err := tr.SendRequest(refreshCtx, "workspace/semanticTokens/refresh", nil); err != nil && ctx.Err() == nil {
			s.logger.DebugContext(ctx, "semanticTokens/refresh request finished", slog.Any("error", err))
		}
		cancel()
	}

	s.sendCodeLensRefresh(ctx, tr)
}

func (s *Server) refreshOpenDocDiagnostics(ctx context.Context, tr *Transport) {
	uris := s.snapshotOpenURIs()
	for _, uri := range uris {
		if ctx.Err() != nil || s.currentState() != stateInitialized {
			return
		}

		s.publishDocDiagnostics(ctx, tr, uri)
		s.publishDocCost(ctx, tr, uri)
	}
}

type frameReadResult struct {
	frame []byte
	err   error
}

func (s *Server) runReadLoop(
	ctx context.Context,
	tr *Transport,
	cmdQueue *commandQueue,
	serveCancel context.CancelFunc,
	activeCancel context.CancelFunc,
) error {
	nextCh := make(chan struct{})
	resCh := make(chan frameReadResult, 1)

	go func() {
		for range nextCh {
			frame, err := tr.ReadFrame()
			resCh <- frameReadResult{frame: frame, err: err}
			if err != nil {
				return
			}
		}
	}()
	defer close(nextCh)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		nextCh <- struct{}{}

		var res frameReadResult
		select {
		case <-ctx.Done():
			return ctx.Err()
		case res = <-resCh:
		}

		if res.err != nil {
			return res.err
		}

		msg, kind, rpcErr := DecodeRPCMessage(res.frame)
		if rpcErr != nil {
			replyID := errorReplyID(msg.ID)
			if sendErr := tr.SendError(replyID, rpcErr); sendErr != nil {
				return sendErr
			}

			continue
		}

		if shouldStop := s.routeInboundMessage(ctx, tr, cmdQueue, msg, kind, serveCancel, activeCancel); shouldStop {
			return nil
		}
	}
}

func (s *Server) routeInboundMessage(
	ctx context.Context,
	tr *Transport,
	cmdQueue *commandQueue,
	msg RPCMessage,
	kind MessageKind,
	serveCancel context.CancelFunc,
	activeCancel context.CancelFunc,
) bool {
	switch kind {
	case MessageKindResponse:
		tr.DeliverResponse(msg)
		return false

	case MessageKindNotification:
		if msg.Method == "$/cancelRequest" {
			s.handleCancelNotification(ctx, msg.Params)
			return false
		}

		if msg.Method == "exit" {
			s.exitReceived.Store(true)
			activeCancel()
			serveCancel()
			return true
		}

		cmdQueue.Push(queuedMessage{msg: msg, kind: kind})
		return false

	case MessageKindRequest:
		idKey, _ := ClassifyAndNormalizeID(msg.ID)
		reqCtx, reqCancel := context.WithCancelCause(ctx)
		s.registerInflight(idKey, reqCancel)

		if !cmdQueue.Push(queuedMessage{
			msg:       msg,
			kind:      kind,
			idKey:     idKey,
			reqCtx:    reqCtx,
			reqCancel: reqCancel,
		}) {
			s.unregisterInflight(idKey)
			reqCancel(nil)
		}
		return false

	case MessageKindInvalid:
		return false
	}

	return false
}

func errorReplyID(raw json.RawMessage) json.RawMessage {
	_, kind := ClassifyAndNormalizeID(raw)
	if kind == IDString || kind == IDNumber {
		return raw
	}

	return json.RawMessage("null")
}

func (s *Server) registerInflight(idKey string, cancel context.CancelCauseFunc) {
	if idKey == "" {
		return
	}

	s.inflightMu.Lock()
	s.inflight[idKey] = inflightEntry{cancel: cancel}
	s.inflightMu.Unlock()
}

func (s *Server) unregisterInflight(idKey string) {
	if idKey == "" {
		return
	}

	s.inflightMu.Lock()
	delete(s.inflight, idKey)
	s.inflightMu.Unlock()
}

func (s *Server) cancelInflight(idKey string, cause error) {
	if idKey == "" {
		return
	}

	s.inflightMu.Lock()
	entry, ok := s.inflight[idKey]
	s.inflightMu.Unlock()

	if ok && entry.cancel != nil {
		entry.cancel(cause)
	}
}

func (s *Server) cancelAllInflight(cause error) {
	s.inflightMu.Lock()
	entries := make([]inflightEntry, 0, len(s.inflight))
	for _, entry := range s.inflight {
		entries = append(entries, entry)
	}
	s.inflightMu.Unlock()

	for _, entry := range entries {
		if entry.cancel != nil {
			entry.cancel(cause)
		}
	}
}

func releaseQueuedRequests(items []queuedMessage) {
	for _, item := range items {
		if item.reqCancel != nil {
			item.reqCancel(nil)
		}
		if item.done != nil {
			close(item.done)
		}
	}
}

func (s *Server) handleCancelNotification(ctx context.Context, raw json.RawMessage) {
	var params CancelParams
	if err := json.Unmarshal(raw, &params); err != nil {
		s.logger.DebugContext(ctx, "invalid $/cancelRequest params", slog.Any("error", err))
		return
	}

	idKey, kind := ClassifyAndNormalizeID(params.ID)
	if kind != IDString && kind != IDNumber {
		return
	}

	s.cancelInflight(idKey, errCancelledByClient)
}

func (s *Server) runDispatcher(
	ctx context.Context,
	tr *Transport,
	cmdQueue *commandQueue,
	dispatchWG *sync.WaitGroup,
	workerWG *sync.WaitGroup,
	activeCancel context.CancelFunc,
) {
	defer dispatchWG.Done()

	for {
		item, ok := cmdQueue.Pop()
		if !ok {
			return
		}

		if s.handleRefreshQueueItem(ctx, tr, item) {
			continue
		}

		switch item.kind {
		case MessageKindNotification:
			s.dispatchNotification(ctx, tr, item.msg)
		case MessageKindRequest:
			s.dispatchRequestItem(ctx, tr, item, workerWG, activeCancel)
		case MessageKindInvalid, MessageKindResponse:
		}
	}
}

func (s *Server) handleRefreshQueueItem(ctx context.Context, tr *Transport, item queuedMessage) bool {
	if item.itemKind != queueItemRefresh && item.itemKind != queueItemCostRefresh {
		return false
	}

	if s.currentState() == stateInitialized {
		if item.itemKind == queueItemRefresh {
			s.refreshOpenDocDiagnostics(ctx, tr)
		} else {
			s.refreshAllOpenDocCostState(ctx, tr)
		}
	}

	if item.done != nil {
		close(item.done)
	}

	return true
}

func (s *Server) dispatchRequestItem(
	ctx context.Context,
	tr *Transport,
	item queuedMessage,
	workerWG *sync.WaitGroup,
	activeCancel context.CancelFunc,
) {
	state := s.currentState()

	if state == stateShutdown {
		s.finishSyncRequest(tr, item, nil, &ResponseError{
			Code:    ErrCodeInvalidRequest,
			Message: "server is shutting down",
		})
		return
	}

	if state == stateUninitialized && item.msg.Method != "initialize" {
		s.finishSyncRequest(tr, item, nil, &ResponseError{
			Code:    ErrCodeServerNotInitialized,
			Message: "server not initialized",
		})
		return
	}

	switch item.msg.Method {
	case "initialize":
		res, rpcErr := s.handleInitialize(item.reqCtx, item.msg.Params)
		s.finishSyncRequest(tr, item, res, rpcErr)

	case "shutdown":
		res, rpcErr := s.handleShutdown()
		if rpcErr == nil {
			activeCancel()
			workerWG.Wait()
		}
		s.finishSyncRequest(tr, item, res, rpcErr)

	default:
		workerWG.Go(func() {
			defer s.unregisterInflight(item.idKey)
			defer item.reqCancel(nil)

			if errors.Is(context.Cause(item.reqCtx), errCancelledByClient) {
				s.sendCancelledError(ctx, tr, item.msg.ID)
				return
			}

			res, rpcErr := s.handleFeatureRequest(item.reqCtx, item.msg)
			if errors.Is(context.Cause(item.reqCtx), errCancelledByClient) {
				s.sendCancelledError(ctx, tr, item.msg.ID)
				return
			}

			if ctx.Err() != nil {
				return
			}

			s.sendRequestReply(ctx, tr, item.msg.ID, res, rpcErr)
		})
	}
}

func (s *Server) finishSyncRequest(tr *Transport, item queuedMessage, res any, rpcErr *ResponseError) {
	defer s.unregisterInflight(item.idKey)
	defer item.reqCancel(nil)

	if errors.Is(context.Cause(item.reqCtx), errCancelledByClient) {
		s.sendCancelledError(item.reqCtx, tr, item.msg.ID)
		return
	}

	s.sendRequestReply(item.reqCtx, tr, item.msg.ID, res, rpcErr)
}

func (s *Server) sendCancelledError(ctx context.Context, tr *Transport, id json.RawMessage) {
	if err := tr.SendError(id, &ResponseError{
		Code:    ErrCodeRequestCancelled,
		Message: "request cancelled",
	}); err != nil {
		s.logger.DebugContext(ctx, "failed to send cancel response", slog.Any("error", err))
	}
}

func (s *Server) sendRequestReply(
	ctx context.Context,
	tr *Transport,
	id json.RawMessage,
	res any,
	rpcErr *ResponseError,
) {
	var err error
	if rpcErr != nil {
		err = tr.SendError(id, rpcErr)
	} else {
		err = tr.SendSuccess(id, res)
	}

	if err != nil {
		s.logger.DebugContext(ctx, "failed to send jsonrpc response", slog.Any("error", err))
	}
}

func (s *Server) currentState() serverLifecycleState {
	s.stateMu.Lock()
	st := s.lifecycle
	s.stateMu.Unlock()

	return st
}

func (s *Server) handleInitialize(ctx context.Context, raw json.RawMessage) (any, *ResponseError) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	if s.lifecycle != stateUninitialized {
		return nil, &ResponseError{
			Code:    ErrCodeInvalidRequest,
			Message: "server already initialized",
		}
	}

	var params InitializeParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, &ResponseError{
				Code:    ErrCodeInvalidParams,
				Message: fmt.Sprintf("invalid initialize params: %v", err),
			}
		}
	}

	rootDir := extractWorkspaceRoot(params)
	clientLSP, clientLSPDef, clientCfgErr := parseClientLSPOptions(params.InitializationOptions, rootDir)
	if clientCfgErr != nil {
		s.logger.WarnContext(ctx, "ignoring invalid initializationOptions", slog.Any("error", clientCfgErr))
	}

	s.docsMu.Lock()
	s.rootDir = rootDir
	s.clientLSP = clientLSP
	s.clientLSPDefined = clientLSPDef
	s.docsMu.Unlock()

	refreshSupport := params.Capabilities.Workspace != nil &&
		params.Capabilities.Workspace.SemanticTokens != nil &&
		params.Capabilities.Workspace.SemanticTokens.RefreshSupport
	s.refreshSupport.Store(refreshSupport)

	codeLensRefreshSupport := params.Capabilities.Workspace != nil &&
		params.Capabilities.Workspace.CodeLens != nil &&
		params.Capabilities.Workspace.CodeLens.RefreshSupport
	s.codeLensRefreshSupport.Store(codeLensRefreshSupport)

	labelOffsetSupport := params.Capabilities.TextDocument != nil &&
		params.Capabilities.TextDocument.SignatureHelp != nil &&
		params.Capabilities.TextDocument.SignatureHelp.SignatureInformation != nil &&
		params.Capabilities.TextDocument.SignatureHelp.SignatureInformation.ParameterInformation != nil &&
		params.Capabilities.TextDocument.SignatureHelp.SignatureInformation.ParameterInformation.LabelOffsetSupport
	s.sigLabelOffsetSupport.Store(labelOffsetSupport)

	s.lifecycle = stateInitialized
	s.logger.InfoContext(ctx, "LSP server initialized", slog.String("rootDir", rootDir))

	return InitializeResult{
		Capabilities: ServerCapabilities{
			TextDocumentSync: &TextDocumentSyncOptions{
				OpenClose: true,
				Change:    TextDocumentSyncIncremental,
				Save:      &SaveOptions{IncludeText: false},
			},
			HoverProvider: true,
			CompletionProvider: &CompletionOptions{
				TriggerCharacters: []string{".", "|", "{", "%", "@", "`"},
			},
			SignatureHelpProvider: &SignatureHelpOptions{
				TriggerCharacters:   []string{"(", ","},
				RetriggerCharacters: []string{",", " "},
			},
			DefinitionProvider:         true,
			DocumentSymbolProvider:     true,
			CodeLensProvider:           &CodeLensOptions{ResolveProvider: false},
			DocumentFormattingProvider: true,
			SemanticTokensProvider: &SemanticTokensOptions{
				Legend: analysis.DefaultSemanticTokensLegend(),
				Range:  true,
				Full:   &SemanticTokensFullOptions{Delta: true},
			},
			Experimental: &ExperimentalCapabilities{
				BQPLSStatements: true,
			},
		},
		ServerInfo: &ServerInfo{
			Name:    serverName,
			Version: s.opts.Version,
		},
	}, nil
}

func extractWorkspaceRoot(params InitializeParams) string {
	for _, folder := range params.WorkspaceFolders {
		if p, ok := uriToPath(folder.URI); ok {
			return p
		}
	}

	if p, ok := uriToPath(params.RootURI); ok {
		return p
	}

	if params.RootPath != "" {
		return filepath.Clean(params.RootPath)
	}

	return ""
}

func (s *Server) handleShutdown() (any, *ResponseError) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	if s.lifecycle == stateShutdown {
		return nil, &ResponseError{
			Code:    ErrCodeInvalidRequest,
			Message: "server already shut down",
		}
	}

	s.lifecycle = stateShutdown
	s.shutdownReceived.Store(true)

	return nil, nil
}

func (s *Server) dispatchNotification(ctx context.Context, tr *Transport, msg RPCMessage) {
	state := s.currentState()
	if state != stateInitialized {
		return
	}

	switch msg.Method {
	case "initialized", "$/setTrace":
		return
	case "textDocument/didOpen":
		s.handleDidOpen(ctx, tr, msg.Params)
	case "textDocument/didChange":
		s.handleDidChange(ctx, tr, msg.Params)
	case "textDocument/didSave":
		s.handleDidSave(ctx, tr, msg.Params)
	case "textDocument/didClose":
		s.handleDidClose(ctx, tr, msg.Params)
	case "workspace/didChangeConfiguration":
		s.handleDidChangeConfiguration(ctx, tr, msg.Params)
	default:
		if !strings.HasPrefix(msg.Method, "$/") {
			s.logger.DebugContext(ctx, "ignoring unknown notification", slog.String("method", msg.Method))
		}
	}
}

func (s *Server) handleFeatureRequest(ctx context.Context, msg RPCMessage) (any, *ResponseError) {
	switch msg.Method {
	case "textDocument/semanticTokens/full":
		return s.handleSemanticTokensFull(ctx, msg.Params)
	case "textDocument/semanticTokens/full/delta":
		return s.handleSemanticTokensDelta(ctx, msg.Params)
	case "textDocument/semanticTokens/range":
		return s.handleSemanticTokensRange(ctx, msg.Params)
	case "textDocument/hover":
		return s.handleHover(ctx, msg.Params)
	case "textDocument/signatureHelp":
		return s.handleSignatureHelp(ctx, msg.Params)
	case "textDocument/completion":
		return s.handleCompletion(ctx, msg.Params)
	case "textDocument/definition":
		return s.handleDefinition(ctx, msg.Params)
	case "textDocument/documentSymbol":
		return s.handleDocumentSymbol(ctx, msg.Params)
	case "textDocument/codeLens":
		return s.handleCodeLens(ctx, msg.Params)
	case "bqpls/refreshCost":
		return s.handleRefreshCost(ctx, msg.Params)
	case "bqpls/statements":
		return s.handleStatements(ctx, msg.Params)
	case "textDocument/formatting":
		return s.handleFormatting(ctx, msg.Params)
	default:
		return nil, &ResponseError{
			Code:    ErrCodeMethodNotFound,
			Message: "method not found: " + msg.Method,
		}
	}
}

func (s *Server) handleDidOpen(ctx context.Context, tr *Transport, raw json.RawMessage) {
	var params DidOpenTextDocumentParams
	if err := json.Unmarshal(raw, &params); err != nil {
		s.logger.WarnContext(ctx, "invalid didOpen params", slog.Any("error", err))
		return
	}

	uri := params.TextDocument.URI
	loaded, docCfg := s.resolveDocConfig(ctx, uri)
	storeKey := storeCacheKey(loaded.LSP)
	cKey := costConfigKey(loaded.LSP)

	s.docsMu.Lock()
	s.docs[uri] = &openDocState{
		uri:                  uri,
		version:              params.TextDocument.Version,
		diagnosticsEnabled:   loaded.LSP.DiagnosticsEnabled,
		costStatusBarEnabled: docCostStatusEnabled(loaded.LSP),
		costDryRunDebounceMs: loaded.LSP.CostDryRunDebounceMs,
		storeKey:             storeKey,
		costKey:              cKey,
		store:                docCfg.Store,
	}
	s.docsMu.Unlock()

	s.engine.OpenDocument(uri, params.TextDocument.Text, params.TextDocument.Version, docCfg)
	s.publishDocDiagnostics(ctx, tr, uri)
	s.scheduleDryRun(uri, true)
	s.publishDocCost(ctx, tr, uri)
}

func (s *Server) handleDidChange(ctx context.Context, tr *Transport, raw json.RawMessage) {
	var params DidChangeTextDocumentParams
	if err := json.Unmarshal(raw, &params); err != nil {
		s.logger.WarnContext(ctx, "invalid didChange params", slog.Any("error", err))
		return
	}

	uri := params.TextDocument.URI
	changes := make([]syntax.TextChange, 0, len(params.ContentChanges))
	for _, ch := range params.ContentChanges {
		changes = append(changes, syntax.TextChange{
			Range: ch.Range,
			Text:  ch.Text,
		})
	}

	if err := s.engine.ChangeDocument(uri, params.TextDocument.Version, changes); err != nil {
		s.logger.WarnContext(ctx, "didChange failed", slog.String("uri", uri), slog.Any("error", err))
		return
	}

	s.docsMu.Lock()
	if doc, ok := s.docs[uri]; ok {
		doc.version = params.TextDocument.Version
	}
	s.docsMu.Unlock()

	s.publishDocDiagnostics(ctx, tr, uri)
	s.publishDocCost(ctx, tr, uri)
	s.scheduleDryRun(uri, false)
}

func (s *Server) handleDidSave(ctx context.Context, tr *Transport, raw json.RawMessage) {
	var params DidSaveTextDocumentParams
	if err := json.Unmarshal(raw, &params); err != nil {
		s.logger.WarnContext(ctx, "invalid didSave params", slog.Any("error", err))
		return
	}

	if err := s.loader.Invalidate(); err != nil {
		s.logger.WarnContext(ctx, "config reload failed on didSave", slog.Any("error", err))
	}

	uri := params.TextDocument.URI
	if s.isConfigDocument(uri) {
		s.reloadAllOpenDocs(ctx, tr)
		return
	}

	s.applySavedTextIfChanged(ctx, uri, params.Text)
	configChanged := s.refreshSingleOpenDocConfig(ctx, uri)
	if store := s.storeForURI(uri); store != nil {
		store.ClearNegativeCache()
	}
	s.publishDocDiagnostics(ctx, tr, uri)
	s.scheduleDryRun(uri, true)
	s.publishDocCost(ctx, tr, uri)

	if configChanged {
		s.triggerSemanticRefresh()
	}
}

func (s *Server) applySavedTextIfChanged(ctx context.Context, uri string, savedText *string) {
	if savedText == nil {
		return
	}

	curText, ok := s.engine.DocumentText(uri)
	if !ok || curText == *savedText {
		return
	}

	nextVersion := 0
	s.docsMu.Lock()
	if doc, exists := s.docs[uri]; exists {
		doc.version++
		nextVersion = doc.version
	}
	s.docsMu.Unlock()

	if err := s.engine.ChangeDocument(uri, nextVersion, []syntax.TextChange{{Text: *savedText}}); err != nil {
		s.logger.WarnContext(ctx, "didSave text sync failed", slog.String("uri", uri), slog.Any("error", err))
	}
}

func (s *Server) refreshSingleOpenDocConfig(ctx context.Context, uri string) bool {
	s.docsMu.RLock()
	doc, ok := s.docs[uri]
	prevStoreKey := ""
	prevCostKey := ""
	prevDiagEnabled := false
	if ok {
		prevStoreKey = doc.storeKey
		prevCostKey = doc.costKey
		prevDiagEnabled = doc.diagnosticsEnabled
	}
	s.docsMu.RUnlock()

	if !ok {
		return false
	}

	loaded, docCfg := s.resolveDocConfig(ctx, uri)
	nextStoreKey := storeCacheKey(loaded.LSP)
	nextCostKey := costConfigKey(loaded.LSP)
	changed := nextStoreKey != prevStoreKey ||
		nextCostKey != prevCostKey ||
		loaded.LSP.DiagnosticsEnabled != prevDiagEnabled

	s.docsMu.Lock()
	if cur, exists := s.docs[uri]; exists {
		cur.storeKey = nextStoreKey
		cur.costKey = nextCostKey
		cur.store = docCfg.Store
		cur.diagnosticsEnabled = loaded.LSP.DiagnosticsEnabled
		cur.costStatusBarEnabled = docCostStatusEnabled(loaded.LSP)
		cur.costDryRunDebounceMs = loaded.LSP.CostDryRunDebounceMs
	}
	s.docsMu.Unlock()

	if err := s.engine.UpdateDocumentConfig(uri, docCfg); err != nil && !errors.Is(err, analysis.ErrDocumentNotOpen) {
		s.logger.WarnContext(ctx, "UpdateDocumentConfig failed", slog.String("uri", uri), slog.Any("error", err))
	}

	return changed
}

func (s *Server) reloadAllOpenDocs(ctx context.Context, tr *Transport) {
	uris := s.snapshotOpenURIs()
	for _, uri := range uris {
		s.refreshSingleOpenDocConfig(ctx, uri)
		s.publishDocDiagnostics(ctx, tr, uri)
		s.scheduleDryRun(uri, true)
		s.publishDocCost(ctx, tr, uri)
	}

	s.triggerSemanticRefresh()
}

func (s *Server) triggerSemanticRefresh() {
	select {
	case s.refreshCh <- struct{}{}:
	default:
	}
}

func (s *Server) isConfigDocument(uri string) bool {
	fsPath, ok := uriToPath(uri)
	if !ok {
		return false
	}

	if s.opts.ConfigPath != "" {
		absCfg, err := filepath.Abs(s.opts.ConfigPath)
		absPath, pathErr := filepath.Abs(fsPath)
		if err == nil && pathErr == nil && absCfg == absPath {
			return true
		}
	}

	return filepath.Base(fsPath) == bqconfig.ConfigFileName
}

func (s *Server) handleDidChangeConfiguration(ctx context.Context, tr *Transport, raw json.RawMessage) {
	var params DidChangeConfigurationParams
	if err := json.Unmarshal(raw, &params); err != nil {
		s.logger.WarnContext(ctx, "invalid workspace/didChangeConfiguration params", slog.Any("error", err))
		return
	}

	s.docsMu.RLock()
	rootDir := s.rootDir
	s.docsMu.RUnlock()

	clientLSP, clientLSPDef, err := parseClientLSPOptions(params.Settings, rootDir)
	if err != nil {
		s.logger.WarnContext(ctx, "ignoring invalid workspace/didChangeConfiguration settings", slog.Any("error", err))
		return
	}

	s.docsMu.Lock()
	s.clientLSP = clientLSP
	s.clientLSPDefined = clientLSPDef
	s.docsMu.Unlock()

	s.reloadAllOpenDocs(ctx, tr)
}

func (s *Server) handleDidClose(ctx context.Context, tr *Transport, raw json.RawMessage) {
	var params DidCloseTextDocumentParams
	if err := json.Unmarshal(raw, &params); err != nil {
		s.logger.WarnContext(ctx, "invalid didClose params", slog.Any("error", err))
		return
	}

	uri := params.TextDocument.URI
	s.cancelDryRunTimer(uri)

	s.diagMu.Lock()
	defer s.diagMu.Unlock()

	s.docsMu.Lock()
	wasPublished := false
	if doc, exists := s.docs[uri]; exists {
		wasPublished = doc.hasPublishedCost
	}
	delete(s.docs, uri)
	s.docsMu.Unlock()

	s.engine.CloseDocument(uri)

	if err := tr.SendNotification("textDocument/publishDiagnostics", PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: []analysis.Diagnostic{},
	}); err != nil {
		s.logger.DebugContext(ctx, "failed to publish empty diagnostics on close", slog.Any("error", err))
	}

	if wasPublished {
		s.publishEmptyDocCost(ctx, tr, uri)
	}
}

func (s *Server) publishDocDiagnostics(ctx context.Context, tr *Transport, uri string) {
	s.diagMu.Lock()
	defer s.diagMu.Unlock()

	version, diagEnabled, ok := s.readDocDiagState(uri)
	if !ok {
		return
	}

	diags := []analysis.Diagnostic{}
	if diagEnabled {
		var err error
		diags, err = s.engine.Diagnostics(ctx, uri)
		if err != nil {
			if !errors.Is(err, analysis.ErrDocumentNotOpen) && !errors.Is(err, context.Canceled) {
				s.logger.WarnContext(ctx, "diagnostics computation failed", slog.String("uri", uri), slog.Any("error", err))
			}

			return
		}

		if diags == nil {
			diags = []analysis.Diagnostic{}
		}
	}

	if !s.isDocDiagStateCurrent(uri, version, diagEnabled) {
		return
	}

	if err := tr.SendNotification("textDocument/publishDiagnostics", PublishDiagnosticsParams{
		URI:         uri,
		Version:     &version,
		Diagnostics: diags,
	}); err != nil {
		s.logger.DebugContext(ctx, "failed to publish diagnostics", slog.String("uri", uri), slog.Any("error", err))
	}
}

func (s *Server) readDocDiagState(uri string) (version int, diagEnabled, ok bool) {
	s.docsMu.RLock()
	doc, exists := s.docs[uri]
	if exists {
		version = doc.version
		diagEnabled = doc.diagnosticsEnabled
	}
	s.docsMu.RUnlock()

	return version, diagEnabled, exists
}

func (s *Server) isDocDiagStateCurrent(uri string, version int, diagEnabled bool) bool {
	s.docsMu.RLock()
	cur, ok := s.docs[uri]
	current := ok && cur.version == version && cur.diagnosticsEnabled == diagEnabled
	s.docsMu.RUnlock()

	return current
}

func (s *Server) handleSemanticTokensFull(ctx context.Context, raw json.RawMessage) (any, *ResponseError) {
	params, rpcErr := decodeParams[SemanticTokensParams](raw)
	if rpcErr != nil {
		return nil, rpcErr
	}

	res, err := s.engine.SemanticTokensFull(ctx, params.TextDocument.URI)
	if err != nil {
		return nil, mapAnalysisError(err)
	}

	data := res.Data
	if data == nil {
		data = []uint32{}
	}

	return SemanticTokens{
		ResultID: res.ResultID,
		Data:     data,
	}, nil
}

func (s *Server) handleSemanticTokensDelta(ctx context.Context, raw json.RawMessage) (any, *ResponseError) {
	params, rpcErr := decodeParams[SemanticTokensDeltaParams](raw)
	if rpcErr != nil {
		return nil, rpcErr
	}

	res, err := s.engine.SemanticTokensFullDelta(ctx, params.TextDocument.URI, params.PreviousResultID)
	if err != nil {
		return nil, mapAnalysisError(err)
	}

	if res.IsDelta {
		edits := res.Edits
		if edits == nil {
			edits = []analysis.SemanticTokensEdit{}
		}

		return SemanticTokensDelta{
			ResultID: res.ResultID,
			Edits:    edits,
		}, nil
	}

	data := res.Data
	if data == nil {
		data = []uint32{}
	}

	return SemanticTokens{
		ResultID: res.ResultID,
		Data:     data,
	}, nil
}

func (s *Server) handleSemanticTokensRange(ctx context.Context, raw json.RawMessage) (any, *ResponseError) {
	params, rpcErr := decodeParams[SemanticTokensRangeParams](raw)
	if rpcErr != nil {
		return nil, rpcErr
	}

	res, err := s.engine.SemanticTokensRange(ctx, params.TextDocument.URI, params.Range)
	if err != nil {
		return nil, mapAnalysisError(err)
	}

	data := res.Data
	if data == nil {
		data = []uint32{}
	}

	return SemanticTokens{
		ResultID: res.ResultID,
		Data:     data,
	}, nil
}

func (s *Server) handleHover(ctx context.Context, raw json.RawMessage) (any, *ResponseError) {
	params, rpcErr := decodeParams[HoverParams](raw)
	if rpcErr != nil {
		return nil, rpcErr
	}

	h, err := s.engine.Hover(ctx, params.TextDocument.URI, params.Position)
	if err != nil {
		return nil, mapAnalysisError(err)
	}

	if h == nil {
		return nil, nil
	}

	return h, nil
}

func (s *Server) handleSignatureHelp(ctx context.Context, raw json.RawMessage) (any, *ResponseError) {
	params, rpcErr := decodeParams[SignatureHelpParams](raw)
	if rpcErr != nil {
		return nil, rpcErr
	}

	sh, err := s.engine.SignatureHelp(ctx, params.TextDocument.URI, params.Position, s.sigLabelOffsetSupport.Load())
	if err != nil {
		return nil, mapAnalysisError(err)
	}

	if sh == nil {
		return nil, nil
	}

	return sh, nil
}

func (s *Server) handleCompletion(ctx context.Context, raw json.RawMessage) (any, *ResponseError) {
	params, rpcErr := decodeParams[CompletionParams](raw)
	if rpcErr != nil {
		return nil, rpcErr
	}

	list, err := s.engine.Complete(ctx, params.TextDocument.URI, params.Position)
	if err != nil {
		return nil, mapAnalysisError(err)
	}

	if list == nil {
		return &analysis.CompletionList{Items: []analysis.CompletionItem{}}, nil
	}

	if list.Items == nil {
		list.Items = []analysis.CompletionItem{}
	}

	return list, nil
}

func (s *Server) handleDefinition(ctx context.Context, raw json.RawMessage) (any, *ResponseError) {
	params, rpcErr := decodeParams[DefinitionParams](raw)
	if rpcErr != nil {
		return nil, rpcErr
	}

	locs, err := s.engine.Definition(ctx, params.TextDocument.URI, params.Position)
	if err != nil {
		return nil, mapAnalysisError(err)
	}

	if locs == nil {
		return []analysis.Location{}, nil
	}

	return locs, nil
}

func (s *Server) handleDocumentSymbol(ctx context.Context, raw json.RawMessage) (any, *ResponseError) {
	params, rpcErr := decodeParams[DocumentSymbolParams](raw)
	if rpcErr != nil {
		return nil, rpcErr
	}

	syms, err := s.engine.DocumentSymbols(ctx, params.TextDocument.URI)
	if err != nil {
		return nil, mapAnalysisError(err)
	}

	if syms == nil {
		return []analysis.DocumentSymbol{}, nil
	}

	return syms, nil
}

func (s *Server) handleStatements(ctx context.Context, raw json.RawMessage) (any, *ResponseError) {
	params, rpcErr := decodeParams[StatementsParams](raw)
	if rpcErr != nil {
		return nil, rpcErr
	}

	stmts, err := s.engine.Statements(ctx, params.TextDocument.URI)
	if err != nil {
		return nil, mapAnalysisError(err)
	}

	out := make([]StatementInfo, 0, len(stmts))
	for _, st := range stmts {
		out = append(out, StatementInfo{
			Range: Range{
				Start: Position{Line: st.Range.Start.Line, Character: st.Range.Start.Character},
				End:   Position{Line: st.Range.End.Line, Character: st.Range.End.Character},
			},
			Kind: StatementKind(st.Kind),
			Text: st.Text,
		})
	}

	return StatementsResult{Statements: out}, nil
}

func (s *Server) handleFormatting(ctx context.Context, raw json.RawMessage) (any, *ResponseError) {
	params, rpcErr := decodeParams[DocumentFormattingParams](raw)
	if rpcErr != nil {
		return nil, rpcErr
	}

	if err := ctx.Err(); err != nil {
		return nil, mapAnalysisError(err)
	}

	uri := params.TextDocument.URI
	text, ok := s.engine.DocumentText(uri)
	if !ok {
		return nil, mapAnalysisError(analysis.ErrDocumentNotOpen)
	}

	li, hasIdx := s.engine.LineIndex(uri)
	if !hasIdx || li == nil {
		li = syntax.NewLineIndex(text)
	}

	loaded, _ := s.resolveDocConfig(ctx, uri)
	formatted, err := formatter.NewBigQueryFormatter(
		formatter.WithPrintOptions(loaded.PrintOptions),
	).Format(text)
	if err != nil {
		s.logger.DebugContext(ctx, "formatting skipped due to syntax or print error", slog.String("uri", uri), slog.Any("error", err))
		return []TextEdit{}, nil
	}

	if formatted == text {
		return []TextEdit{}, nil
	}

	return []TextEdit{{
		Range:   li.RangeAt(0, len(text)),
		NewText: formatted,
	}}, nil
}

func decodeParams[T any](raw json.RawMessage) (T, *ResponseError) {
	var zero T
	if len(raw) == 0 {
		return zero, &ResponseError{
			Code:    ErrCodeInvalidParams,
			Message: "missing params",
		}
	}

	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, &ResponseError{
			Code:    ErrCodeInvalidParams,
			Message: fmt.Sprintf("invalid params: %v", err),
		}
	}

	return out, nil
}

func mapAnalysisError(err error) *ResponseError {
	if errors.Is(err, context.Canceled) {
		return &ResponseError{
			Code:    ErrCodeRequestCancelled,
			Message: "request cancelled",
		}
	}

	if errors.Is(err, analysis.ErrDocumentNotOpen) {
		return &ResponseError{
			Code:    ErrCodeInvalidParams,
			Message: err.Error(),
		}
	}

	return &ResponseError{
		Code:    ErrCodeInternalError,
		Message: err.Error(),
	}
}

func (s *Server) resolveDocConfig(ctx context.Context, rawURI string) (*bqconfig.LoadedConfig, *analysis.DocumentConfig) {
	loaded := s.loadConfigForURI(ctx, rawURI)
	store := s.getOrCreateStore(ctx, loaded.LSP)

	return loaded, &analysis.DocumentConfig{
		Store:   store,
		Options: loaded.LSP,
	}
}

func (s *Server) loadConfigForURI(ctx context.Context, rawURI string) *bqconfig.LoadedConfig {
	loaded := s.loadRawConfigForURI(ctx, rawURI)
	s.applyClientLSPFallback(loaded)

	return loaded
}

func (s *Server) loadRawConfigForURI(ctx context.Context, rawURI string) *bqconfig.LoadedConfig {
	fsPath, isFile := uriToPath(rawURI)
	if isFile {
		loaded, err := s.loader.ConfigFor(fsPath, false)
		if err == nil && loaded != nil {
			return s.normalizeLoadedConfig(loaded)
		}

		s.logger.WarnContext(ctx, "failed to load config for URI; using defaults", slog.String("uri", rawURI), slog.Any("error", err))
		return s.defaultLoadedConfig()
	}

	s.docsMu.RLock()
	rootDir := s.rootDir
	s.docsMu.RUnlock()

	if rootDir != "" {
		loaded, err := s.loader.ConfigFor(filepath.Join(rootDir, "untitled.sql"), false)
		if err == nil && loaded != nil {
			return s.normalizeLoadedConfig(loaded)
		}
	}

	loaded, err := s.loader.ConfigFor("", true)
	if err == nil && loaded != nil {
		return s.normalizeLoadedConfig(loaded)
	}

	if err != nil {
		s.logger.WarnContext(ctx, "failed to load fallback config; using defaults", slog.String("uri", rawURI), slog.Any("error", err))
	}

	return s.defaultLoadedConfig()
}

func (s *Server) applyClientLSPFallback(loaded *bqconfig.LoadedConfig) {
	if loaded == nil {
		return
	}

	s.docsMu.RLock()
	clientLSP := s.clientLSP
	clientDef := s.clientLSPDefined
	s.docsMu.RUnlock()

	if clientLSP == nil || !clientDef.Any() {
		return
	}

	loaded.LSP = bqconfig.MergeLSPOptionsOver(s.baseLSPOptions(), loaded.LSP, loaded.LSPDefined, clientLSP, clientDef)
}

func (s *Server) normalizeLoadedConfig(cfg *bqconfig.LoadedConfig) *bqconfig.LoadedConfig {
	if cfg.PrintOptions == nil {
		cfg.PrintOptions = formatter.DefaultPrintOptions()
	}

	if cfg.LSP == nil || (cfg.ConfigPath == "" && !cfg.LSPDefined.Any()) {
		cfg.LSP = s.baseLSPOptions()
	} else if s.defaultLSPOptions != nil {
		cfg.LSP = bqconfig.MergeLSPOptionsOver(s.baseLSPOptions(), cfg.LSP, cfg.LSPDefined, nil, bqconfig.LSPDefinedFields{})
	}

	return cfg
}

func (s *Server) defaultLoadedConfig() *bqconfig.LoadedConfig {
	return &bqconfig.LoadedConfig{
		PrintOptions: formatter.DefaultPrintOptions(),
		LSP:          s.baseLSPOptions(),
	}
}

func (s *Server) getOrCreateStore(ctx context.Context, lspOpts *bqconfig.LSPOptions) *catalog.Store {
	if lspOpts == nil {
		return s.defaultStore
	}

	key := storeCacheKey(lspOpts)

	s.storesMu.Lock()
	defer s.storesMu.Unlock()

	if st, ok := s.stores[key]; ok {
		return st
	}

	if s.storesClosed {
		return s.defaultStore
	}

	storeOpts := s.buildStoreOptions()
	st, err := catalog.NewStore(lspOpts, storeOpts...)
	if err != nil {
		s.logger.WarnContext(ctx, "failed to create catalog store for document; using default store", slog.Any("error", err))
		return s.defaultStore
	}

	s.stores[key] = st
	s.ownedStores = append(s.ownedStores, st)
	s.subscribeStoreLocked(st)

	return st
}

func (s *Server) snapshotOpenURIs() []string {
	s.docsMu.RLock()
	uris := make([]string, 0, len(s.docs))
	for uri := range s.docs {
		uris = append(uris, uri)
	}
	s.docsMu.RUnlock()

	return uris
}

func storeCacheKey(opts *bqconfig.LSPOptions) string {
	if opts == nil {
		return ""
	}

	return fmt.Sprintf(
		"%s\x00%s\x00%s\x00%t\x00%s\x00%s\x00%d\x00%s",
		opts.DefaultProject,
		opts.DefaultDataset,
		strings.Join(opts.SchemaDirs, "\x01"),
		opts.BigQueryAPIEnabled,
		opts.GcloudPath,
		opts.CacheDir,
		opts.CacheTTLSeconds,
		formatProjectTTLCacheKey(opts.ProjectCacheTTLSeconds),
	)
}

func formatProjectTTLCacheKey(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}

	norm := bqconfig.NormalizeProjectCacheTTLs(m)
	keys := make([]string, 0, len(norm))
	for k := range norm {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('\x01')
		}

		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(strconv.Itoa(norm[k]))
	}

	return b.String()
}

func uriToPath(rawURI string) (string, bool) {
	if rawURI == "" {
		return "", false
	}

	u, err := url.Parse(rawURI)
	if err != nil || !strings.EqualFold(u.Scheme, "file") {
		return "", false
	}

	p := u.Path
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		p = "//" + u.Host + p
	}

	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}

	fsPath := filepath.FromSlash(p)
	if fsPath == "" {
		return "", false
	}

	return fsPath, true
}
