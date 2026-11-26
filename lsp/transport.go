package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	defaultMaxMessageBytes = 16 << 20 // 16 MiB
	maxHeaderBytes         = 16 << 10 // 16 KiB
	maxHeaderLines         = 64
	chunkedReadThreshold   = 64 << 10 // 64 KiB
)

// Framing and transport sentinel errors.
var (
	ErrInvalidHeader         = errors.New("invalid lsp header")
	ErrMissingContentLength  = errors.New("missing Content-Length header")
	ErrInvalidContentLength  = errors.New("invalid Content-Length header")
	ErrMessageTooLarge       = errors.New("lsp message exceeds maximum size")
	ErrExitedWithoutShutdown = errors.New("server exited before shutdown request")
	ErrTransportClosed       = errors.New("lsp transport closed")
)

type pendingResult struct {
	msg RPCMessage
	err error
}

// Transport reads and writes Content-Length framed JSON-RPC 2.0 messages over stdio.
type Transport struct {
	br             *bufio.Reader
	bw             *bufio.Writer
	maxMessageSize int

	writeMu sync.Mutex

	nextServerID atomic.Int64
	pendingMu    sync.Mutex
	pending      map[string]chan pendingResult
	closed       bool
	closeErr     error
}

// NewTransport constructs a Transport over r and w with the default 16 MiB frame limit.
func NewTransport(r io.Reader, w io.Writer) *Transport {
	return NewTransportWithLimit(r, w, defaultMaxMessageBytes)
}

// NewTransportWithLimit constructs a Transport over r and w with a custom maxMessageSize.
func NewTransportWithLimit(r io.Reader, w io.Writer, maxMessageSize int) *Transport {
	if maxMessageSize <= 0 {
		maxMessageSize = defaultMaxMessageBytes
	}

	return &Transport{
		br:             bufio.NewReaderSize(r, chunkedReadThreshold),
		bw:             bufio.NewWriter(w),
		maxMessageSize: maxMessageSize,
		pending:        make(map[string]chan pendingResult),
	}
}

// ReadFrame reads a single Content-Length framed payload from the inbound stream.
// It returns io.EOF only on a clean boundary between frames.
func (t *Transport) ReadFrame() ([]byte, error) {
	length, err := t.readHeaders()
	if err != nil {
		return nil, err
	}

	return t.readPayload(length)
}

func (t *Transport) readHeaders() (int, error) {
	var (
		totalBytes       int
		sawAnyByte       bool
		hasContentLength bool
		contentLength    int
	)

	for range maxHeaderLines {
		line, err := readHeaderLine(t.br, maxHeaderBytes-totalBytes)
		if len(line) > 0 {
			sawAnyByte = true
			totalBytes += len(line)
		}

		if err != nil {
			return 0, mapHeaderReadError(err, sawAnyByte)
		}

		trimmed, ok := strings.CutSuffix(line, "\r\n")
		if !ok {
			return 0, ErrInvalidHeader
		}

		if trimmed == "" {
			if !hasContentLength {
				return 0, ErrMissingContentLength
			}

			return contentLength, nil
		}

		cl, isCL, parseErr := parseHeaderLine(trimmed, t.maxMessageSize)
		if parseErr != nil {
			return 0, parseErr
		}

		if isCL {
			if hasContentLength {
				return 0, ErrInvalidHeader
			}

			hasContentLength = true
			contentLength = cl
		}
	}

	return 0, ErrInvalidHeader
}

func mapHeaderReadError(err error, sawAnyByte bool) error {
	if errors.Is(err, io.EOF) {
		if !sawAnyByte {
			return io.EOF
		}

		return io.ErrUnexpectedEOF
	}

	return err
}

func readHeaderLine(br *bufio.Reader, maxLen int) (string, error) {
	if maxLen <= 0 {
		return "", ErrInvalidHeader
	}

	buf := make([]byte, 0, min(256, maxLen))
	for len(buf) < maxLen {
		b, err := br.ReadByte()
		if err != nil {
			return string(buf), err
		}

		buf = append(buf, b)
		if b == '\n' {
			return string(buf), nil
		}
	}

	return string(buf), ErrInvalidHeader
}

func parseHeaderLine(line string, maxMessageSize int) (contentLength int, isContentLength bool, err error) {
	key, val, ok := strings.Cut(line, ":")
	if !ok || key == "" || strings.ContainsAny(key, " \t") {
		return 0, false, ErrInvalidHeader
	}

	if !strings.EqualFold(key, "Content-Length") {
		return 0, false, nil
	}

	trimmedVal := strings.TrimSpace(val)
	if !isASCIIUint(trimmedVal) {
		return 0, false, ErrInvalidContentLength
	}

	n, parseErr := strconv.ParseInt(trimmedVal, 10, 64)
	if parseErr != nil || n < 0 {
		return 0, false, ErrInvalidContentLength
	}

	if n > int64(maxMessageSize) {
		return 0, false, ErrMessageTooLarge
	}

	return int(n), true, nil
}

func isASCIIUint(s string) bool {
	if s == "" {
		return false
	}

	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return true
}

func (t *Transport) readPayload(length int) ([]byte, error) {
	if length == 0 {
		return []byte{}, nil
	}

	if length <= chunkedReadThreshold {
		buf := make([]byte, length)
		if _, err := io.ReadFull(t.br, buf); err != nil {
			if errors.Is(err, io.EOF) {
				return nil, io.ErrUnexpectedEOF
			}

			return nil, err
		}

		return buf, nil
	}

	return readChunkedPayload(t.br, length)
}

func readChunkedPayload(br *bufio.Reader, length int) ([]byte, error) {
	buf := make([]byte, 0, chunkedReadThreshold)
	chunk := make([]byte, chunkedReadThreshold)
	remaining := length

	for remaining > 0 {
		toRead := min(remaining, len(chunk))

		n, err := io.ReadFull(br, chunk[:toRead])
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			remaining -= n
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, io.ErrUnexpectedEOF
			}

			return nil, err
		}
	}

	return buf, nil
}

// WriteFrame writes a single Content-Length framed payload under writeMu and flushes.
func (t *Transport) WriteFrame(body []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := t.bw.WriteString(header); err != nil {
		return err
	}

	if _, err := t.bw.Write(body); err != nil {
		return err
	}

	return t.bw.Flush()
}

// SendSuccess marshals and writes a JSON-RPC 2.0 success response.
func (t *Transport) SendSuccess(id json.RawMessage, result any) error {
	env := SuccessResponse{
		JSONRPC: "2.0",
		ID:      normalizeOutboundID(id),
		Result:  result,
	}

	return t.marshalAndWrite(env)
}

// SendError marshals and writes a JSON-RPC 2.0 error response.
func (t *Transport) SendError(id json.RawMessage, rpcErr *ResponseError) error {
	env := ErrorResponse{
		JSONRPC: "2.0",
		ID:      normalizeOutboundID(id),
		Error:   rpcErr,
	}

	return t.marshalAndWrite(env)
}

// SendNotification marshals and writes a JSON-RPC 2.0 notification.
func (t *Transport) SendNotification(method string, params any) error {
	env := NotificationEnvelope{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}

	return t.marshalAndWrite(env)
}

// SendRequest sends a server-to-client JSON-RPC 2.0 request and waits for the
// correlated client response, ctx cancellation, or transport shutdown.
func (t *Transport) SendRequest(ctx context.Context, method string, params any) (RPCMessage, error) {
	idNum := t.nextServerID.Add(1)
	rawID := json.RawMessage(strconv.AppendInt(nil, idNum, 10))
	key := "n:" + string(rawID)
	ch := make(chan pendingResult, 1)

	t.pendingMu.Lock()
	if t.closed {
		err := t.closeErr
		if err == nil {
			err = ErrTransportClosed
		}
		t.pendingMu.Unlock()

		return RPCMessage{}, err
	}

	t.pending[key] = ch
	t.pendingMu.Unlock()

	env := RequestEnvelope{
		JSONRPC: "2.0",
		ID:      rawID,
		Method:  method,
		Params:  params,
	}

	if err := t.marshalAndWrite(env); err != nil {
		t.removePending(key)

		return RPCMessage{}, err
	}

	select {
	case res := <-ch:
		return res.msg, res.err
	case <-ctx.Done():
		t.removePending(key)

		return RPCMessage{}, ctx.Err()
	}
}

// DeliverResponse routes an incoming client response to its waiting SendRequest caller.
func (t *Transport) DeliverResponse(msg RPCMessage) bool {
	key, kind := ClassifyAndNormalizeID(msg.ID)
	if kind != IDString && kind != IDNumber {
		return false
	}

	t.pendingMu.Lock()
	ch, ok := t.pending[key]
	if ok {
		delete(t.pending, key)
	}
	t.pendingMu.Unlock()

	if !ok {
		return false
	}

	select {
	case ch <- pendingResult{msg: msg}:
	default:
	}

	return true
}

// FailPending unblocks all in-flight SendRequest waiters with err and rejects future requests.
func (t *Transport) FailPending(err error) {
	if err == nil {
		err = ErrTransportClosed
	}

	t.pendingMu.Lock()
	t.closed = true
	t.closeErr = err
	for key, ch := range t.pending {
		delete(t.pending, key)
		select {
		case ch <- pendingResult{err: err}:
		default:
		}
	}
	t.pendingMu.Unlock()
}

func (t *Transport) removePending(key string) {
	t.pendingMu.Lock()
	delete(t.pending, key)
	t.pendingMu.Unlock()
}

func (t *Transport) marshalAndWrite(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal jsonrpc message: %w", err)
	}

	return t.WriteFrame(data)
}

func normalizeOutboundID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}

	return id
}
