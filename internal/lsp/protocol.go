// Package lsp implements a pure-Go stdio JSON-RPC 2.0 / LSP 3.17 server for BigQuery SQL and Jinja2/dbt templates.
package lsp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/paulourio/bqlang/internal/analysis"
	"github.com/paulourio/bqlang/internal/syntax"
)

// Standard JSON-RPC 2.0 and LSP 3.17 error codes.
const (
	ErrCodeParseError           = -32700
	ErrCodeInvalidRequest       = -32600
	ErrCodeMethodNotFound       = -32601
	ErrCodeInvalidParams        = -32602
	ErrCodeInternalError        = -32603
	ErrCodeServerNotInitialized = -32002
	ErrCodeRequestCancelled     = -32800
	ErrCodeContentModified      = -32801
)

// TextDocumentSyncKind values defined by LSP 3.17.
const (
	TextDocumentSyncNone        = 0
	TextDocumentSyncFull        = 1
	TextDocumentSyncIncremental = 2
)

// IDKind classifies a JSON-RPC 2.0 id field.
type IDKind uint8

// JSON-RPC 2.0 ID classification values.
const (
	IDAbsent IDKind = iota
	IDNull
	IDString
	IDNumber
	IDInvalid
)

// MessageKind classifies a decoded JSON-RPC 2.0 message.
type MessageKind uint8

// JSON-RPC 2.0 message classification values.
const (
	MessageKindInvalid MessageKind = iota
	MessageKindRequest
	MessageKindNotification
	MessageKindResponse
)

// ResponseError represents a JSON-RPC 2.0 error object.
type ResponseError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error implements the error interface.
func (e *ResponseError) Error() string {
	if e == nil {
		return ""
	}

	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// RPCMessage represents an incoming or outgoing JSON-RPC 2.0 envelope.
type RPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ResponseError  `json:"error,omitempty"`
}

// SuccessResponse represents a JSON-RPC 2.0 success response without an error field.
type SuccessResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}

// ErrorResponse represents a JSON-RPC 2.0 error response without a result field.
type ErrorResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   *ResponseError  `json:"error"`
}

// RequestEnvelope represents an outgoing server-to-client JSON-RPC 2.0 request.
type RequestEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  any             `json:"params,omitempty"`
}

// NotificationEnvelope represents an outgoing JSON-RPC 2.0 notification.
type NotificationEnvelope struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// ClassifyAndNormalizeID validates a JSON-RPC 2.0 id value and returns a collision-free
// canonical map key ("s:..." for strings, "n:..." for numbers) along with its IDKind.
func ClassifyAndNormalizeID(raw json.RawMessage) (key string, kind IDKind) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", IDAbsent
	}

	if bytes.Equal(trimmed, []byte("null")) {
		return "", IDNull
	}

	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return "", IDInvalid
		}

		return "s:" + s, IDString
	}

	if trimmed[0] == '-' || (trimmed[0] >= '0' && trimmed[0] <= '9') {
		var n json.Number
		if err := json.Unmarshal(trimmed, &n); err != nil {
			return "", IDInvalid
		}

		numStr := string(trimmed)
		if strings.ContainsAny(numStr, " \t\r\n") {
			return "", IDInvalid
		}

		return "n:" + numStr, IDNumber
	}

	return "", IDInvalid
}

// DecodeRPCMessage parses and validates a JSON-RPC 2.0 payload into an RPCMessage
// and MessageKind, returning a *ResponseError (-32700 or -32600) on invalid input.
func DecodeRPCMessage(payload []byte) (RPCMessage, MessageKind, *ResponseError) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return RPCMessage{}, MessageKindInvalid, &ResponseError{
			Code:    ErrCodeParseError,
			Message: "invalid JSON",
		}
	}

	if trimmed[0] != '{' {
		return RPCMessage{}, MessageKindInvalid, &ResponseError{
			Code:    ErrCodeInvalidRequest,
			Message: "JSON-RPC message must be a JSON object",
		}
	}

	var msg RPCMessage
	if err := json.Unmarshal(trimmed, &msg); err != nil {
		return RPCMessage{}, MessageKindInvalid, &ResponseError{
			Code:    ErrCodeInvalidRequest,
			Message: "invalid JSON-RPC message structure",
		}
	}

	if msg.JSONRPC != "2.0" {
		return msg, MessageKindInvalid, &ResponseError{
			Code:    ErrCodeInvalidRequest,
			Message: `jsonrpc version must be "2.0"`,
		}
	}

	return classifyRPCEnvelope(msg)
}

func classifyRPCEnvelope(msg RPCMessage) (RPCMessage, MessageKind, *ResponseError) {
	_, idKind := ClassifyAndNormalizeID(msg.ID)
	if idKind == IDInvalid {
		return msg, MessageKindInvalid, &ResponseError{
			Code:    ErrCodeInvalidRequest,
			Message: "invalid JSON-RPC id",
		}
	}

	if msg.Method != "" {
		switch idKind {
		case IDAbsent:
			return msg, MessageKindNotification, nil
		case IDString, IDNumber:
			return msg, MessageKindRequest, nil
		case IDNull, IDInvalid:
			return msg, MessageKindInvalid, &ResponseError{
				Code:    ErrCodeInvalidRequest,
				Message: "request id must be string or number",
			}
		}
	}

	hasResult := len(bytes.TrimSpace(msg.Result)) > 0
	hasError := msg.Error != nil
	if (hasResult || hasError) && idKind != IDAbsent {
		return msg, MessageKindResponse, nil
	}

	return msg, MessageKindInvalid, &ResponseError{
		Code:    ErrCodeInvalidRequest,
		Message: "missing method",
	}
}

// ClientInfo describes the connecting LSP client.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// WorkspaceFolder describes a workspace folder sent in initialize.
type WorkspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

// SemanticTokensWorkspaceClientCapabilities declares client semantic token refresh support.
type SemanticTokensWorkspaceClientCapabilities struct {
	RefreshSupport bool `json:"refreshSupport,omitempty"`
}

// CodeLensWorkspaceClientCapabilities declares client code lens refresh support.
type CodeLensWorkspaceClientCapabilities struct {
	RefreshSupport bool `json:"refreshSupport,omitempty"`
}

// WorkspaceClientCapabilities declares workspace-level client capabilities.
type WorkspaceClientCapabilities struct {
	SemanticTokens *SemanticTokensWorkspaceClientCapabilities `json:"semanticTokens,omitempty"`
	CodeLens       *CodeLensWorkspaceClientCapabilities       `json:"codeLens,omitempty"`
}

// DocumentSymbolClientCapabilities declares client document symbol support.
type DocumentSymbolClientCapabilities struct {
	HierarchicalDocumentSymbolSupport bool `json:"hierarchicalDocumentSymbolSupport,omitempty"`
}

// ParameterInformationClientCapabilities declares client parameter label capabilities.
type ParameterInformationClientCapabilities struct {
	LabelOffsetSupport bool `json:"labelOffsetSupport,omitempty"`
}

// SignatureInformationClientCapabilities declares client signature information capabilities.
type SignatureInformationClientCapabilities struct {
	DocumentationFormat    []string                                `json:"documentationFormat,omitempty"`
	ParameterInformation   *ParameterInformationClientCapabilities `json:"parameterInformation,omitempty"`
	ActiveParameterSupport bool                                    `json:"activeParameterSupport,omitempty"`
}

// SignatureHelpClientCapabilities declares client signature help support.
type SignatureHelpClientCapabilities struct {
	SignatureInformation *SignatureInformationClientCapabilities `json:"signatureInformation,omitempty"`
	ContextSupport       bool                                    `json:"contextSupport,omitempty"`
}

// TextDocumentClientCapabilities declares text-document client capabilities.
type TextDocumentClientCapabilities struct {
	DocumentSymbol *DocumentSymbolClientCapabilities `json:"documentSymbol,omitempty"`
	SignatureHelp  *SignatureHelpClientCapabilities  `json:"signatureHelp,omitempty"`
}

// ClientCapabilities represents LSP 3.17 client capabilities.
type ClientCapabilities struct {
	Workspace    *WorkspaceClientCapabilities    `json:"workspace,omitempty"`
	TextDocument *TextDocumentClientCapabilities `json:"textDocument,omitempty"`
}

// InitializeParams carries parameters for the initialize request.
type InitializeParams struct {
	ProcessID             *int               `json:"processId,omitempty"`
	ClientInfo            *ClientInfo        `json:"clientInfo,omitempty"`
	RootPath              string             `json:"rootPath,omitempty"`
	RootURI               string             `json:"rootUri,omitempty"`
	InitializationOptions json.RawMessage    `json:"initializationOptions,omitempty"`
	Capabilities          ClientCapabilities `json:"capabilities"`
	WorkspaceFolders      []WorkspaceFolder  `json:"workspaceFolders,omitempty"`
}

// DidChangeConfigurationParams carries parameters for workspace/didChangeConfiguration.
type DidChangeConfigurationParams struct {
	Settings json.RawMessage `json:"settings"`
}

// ServerInfo describes this language server in InitializeResult.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// SaveOptions configures textDocument/didSave notifications.
type SaveOptions struct {
	IncludeText bool `json:"includeText"`
}

// TextDocumentSyncOptions configures document synchronization behavior.
type TextDocumentSyncOptions struct {
	OpenClose bool         `json:"openClose,omitempty"`
	Change    int          `json:"change,omitempty"`
	Save      *SaveOptions `json:"save,omitempty"`
}

// CompletionOptions configures server completion behavior.
type CompletionOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
}

// SignatureHelpOptions configures server signature help behavior.
type SignatureHelpOptions struct {
	TriggerCharacters   []string `json:"triggerCharacters,omitempty"`
	RetriggerCharacters []string `json:"retriggerCharacters,omitempty"`
}

// SemanticTokensFullOptions configures full semantic token and delta support.
type SemanticTokensFullOptions struct {
	Delta bool `json:"delta"`
}

// SemanticTokensOptions configures semantic token provider capabilities.
type SemanticTokensOptions struct {
	Legend analysis.SemanticTokensLegend `json:"legend"`
	Range  bool                          `json:"range,omitempty"`
	Full   *SemanticTokensFullOptions    `json:"full,omitempty"`
}

// CodeLensOptions configures server code lens capabilities.
type CodeLensOptions struct {
	ResolveProvider bool `json:"resolveProvider,omitempty"`
}

// ServerCapabilities declares supported LSP 3.17 capabilities.
type ServerCapabilities struct {
	TextDocumentSync           *TextDocumentSyncOptions `json:"textDocumentSync,omitempty"`
	CompletionProvider         *CompletionOptions       `json:"completionProvider,omitempty"`
	HoverProvider              bool                     `json:"hoverProvider,omitempty"`
	SignatureHelpProvider      *SignatureHelpOptions    `json:"signatureHelpProvider,omitempty"`
	DefinitionProvider         bool                     `json:"definitionProvider,omitempty"`
	DocumentSymbolProvider     bool                     `json:"documentSymbolProvider,omitempty"`
	CodeLensProvider           *CodeLensOptions         `json:"codeLensProvider,omitempty"`
	DocumentFormattingProvider bool                     `json:"documentFormattingProvider,omitempty"`
	SemanticTokensProvider     *SemanticTokensOptions   `json:"semanticTokensProvider,omitempty"`
}

// InitializeResult is returned by initialize.
type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   *ServerInfo        `json:"serverInfo,omitempty"`
}

// TextDocumentIdentifier identifies a text document by URI.
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// VersionedTextDocumentIdentifier identifies a specific version of a text document.
type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

// TextDocumentItem represents an opened text document.
type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

// DidOpenTextDocumentParams carries parameters for textDocument/didOpen.
type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

// TextDocumentContentChangeEvent describes an incremental or full document change.
type TextDocumentContentChangeEvent struct {
	Range       *syntax.Range `json:"range,omitempty"`
	RangeLength *int          `json:"rangeLength,omitempty"`
	Text        string        `json:"text"`
}

// DidChangeTextDocumentParams carries parameters for textDocument/didChange.
type DidChangeTextDocumentParams struct {
	TextDocument   VersionedTextDocumentIdentifier  `json:"textDocument"`
	ContentChanges []TextDocumentContentChangeEvent `json:"contentChanges"`
}

// DidSaveTextDocumentParams carries parameters for textDocument/didSave.
type DidSaveTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Text         *string                `json:"text,omitempty"`
}

// DidCloseTextDocumentParams carries parameters for textDocument/didClose.
type DidCloseTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// HoverParams carries parameters for textDocument/hover.
type HoverParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     syntax.Position        `json:"position"`
}

// SignatureHelpContext carries optional trigger context for textDocument/signatureHelp.
type SignatureHelpContext struct {
	TriggerKind      int    `json:"triggerKind"`
	TriggerCharacter string `json:"triggerCharacter,omitempty"`
	IsRetrigger      bool   `json:"isRetrigger,omitempty"`
}

// SignatureHelpParams carries parameters for textDocument/signatureHelp.
type SignatureHelpParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     syntax.Position        `json:"position"`
	Context      *SignatureHelpContext  `json:"context,omitempty"`
}

// CompletionContext carries optional completion trigger context.
type CompletionContext struct {
	TriggerKind      int    `json:"triggerKind"`
	TriggerCharacter string `json:"triggerCharacter,omitempty"`
}

// CompletionParams carries parameters for textDocument/completion.
type CompletionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     syntax.Position        `json:"position"`
	Context      *CompletionContext     `json:"context,omitempty"`
}

// DefinitionParams carries parameters for textDocument/definition.
type DefinitionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     syntax.Position        `json:"position"`
}

// DocumentSymbolParams carries parameters for textDocument/documentSymbol.
type DocumentSymbolParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// CodeLensParams carries parameters for textDocument/codeLens.
type CodeLensParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// RefreshCostParams carries parameters for the custom bqpls/refreshCost request.
type RefreshCostParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// FormattingOptions configures textDocument/formatting behavior.
type FormattingOptions struct {
	TabSize                int  `json:"tabSize"`
	InsertSpaces           bool `json:"insertSpaces"`
	TrimTrailingWhitespace bool `json:"trimTrailingWhitespace,omitempty"`
	InsertFinalNewline     bool `json:"insertFinalNewline,omitempty"`
	TrimFinalNewlines      bool `json:"trimFinalNewlines,omitempty"`
}

// DocumentFormattingParams carries parameters for textDocument/formatting.
type DocumentFormattingParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Options      FormattingOptions      `json:"options"`
}

// TextEdit represents a textual edit returned by formatting.
type TextEdit struct {
	Range   syntax.Range `json:"range"`
	NewText string       `json:"newText"`
}

// SemanticTokensParams carries parameters for textDocument/semanticTokens/full.
type SemanticTokensParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// SemanticTokensDeltaParams carries parameters for textDocument/semanticTokens/full/delta.
type SemanticTokensDeltaParams struct {
	TextDocument     TextDocumentIdentifier `json:"textDocument"`
	PreviousResultID string                 `json:"previousResultId"`
}

// SemanticTokensRangeParams carries parameters for textDocument/semanticTokens/range.
type SemanticTokensRangeParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        syntax.Range           `json:"range"`
}

// SemanticTokens represents the LSP SemanticTokens union member (never includes edits).
type SemanticTokens struct {
	ResultID string   `json:"resultId,omitempty"`
	Data     []uint32 `json:"data"`
}

// SemanticTokensDelta represents the LSP SemanticTokensDelta union member (never includes data).
type SemanticTokensDelta struct {
	ResultID string                        `json:"resultId,omitempty"`
	Edits    []analysis.SemanticTokensEdit `json:"edits"`
}

// PublishDiagnosticsParams carries parameters for textDocument/publishDiagnostics.
type PublishDiagnosticsParams struct {
	URI         string                `json:"uri"`
	Version     *int                  `json:"version,omitempty"`
	Diagnostics []analysis.Diagnostic `json:"diagnostics"`
}

// CancelParams carries parameters for $/cancelRequest.
type CancelParams struct {
	ID json.RawMessage `json:"id"`
}
