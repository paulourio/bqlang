'use strict';

const vscode = require('vscode');
const cp = require('child_process');
const fs = require('fs');
const path = require('path');
const process = require('process');

const DEFAULT_BQPLS_PATH = "__BQPLS_BIN_PATH__";
const LANGUAGE_ID = 'bqlang';

const TOKEN_TYPES = [
  'namespace',
  'class',
  'function',
  'variable',
  'property',
  'parameter',
  'keyword',
  'type',
  'string',
  'number',
  'comment',
  'operator',
  'macro',
  'enumMember'
];

const TOKEN_MODIFIERS = [
  'declaration',
  'readonly',
  'defaultLibrary',
  'partitioned',
  'clustered',
  'pseudo',
  'modification'
];

const SEMANTIC_LEGEND = new vscode.SemanticTokensLegend(TOKEN_TYPES, TOKEN_MODIFIERS);

function toLspPosition(pos) {
  return { line: pos.line, character: pos.character };
}

function toLspRange(range) {
  return {
    start: toLspPosition(range.start),
    end: toLspPosition(range.end)
  };
}

function fromLspPosition(pos) {
  return new vscode.Position(pos.line || 0, pos.character || 0);
}

function fromLspRange(range) {
  if (!range || !range.start || !range.end) {
    return new vscode.Range(0, 0, 0, 0);
  }
  return new vscode.Range(fromLspPosition(range.start), fromLspPosition(range.end));
}

function mapDiagnosticSeverity(severity) {
  switch (severity) {
    case 1:
      return vscode.DiagnosticSeverity.Error;
    case 2:
      return vscode.DiagnosticSeverity.Warning;
    case 3:
      return vscode.DiagnosticSeverity.Information;
    case 4:
      return vscode.DiagnosticSeverity.Hint;
    default:
      return vscode.DiagnosticSeverity.Warning;
  }
}

function mapCompletionKind(kind) {
  if (typeof kind !== 'number' || kind < 1) {
    return vscode.CompletionItemKind.Text;
  }
  return kind - 1;
}

function mapSymbolKind(kind) {
  if (typeof kind !== 'number' || kind < 1) {
    return vscode.SymbolKind.Variable;
  }
  return kind - 1;
}

function mapMarkupContent(contents) {
  if (!contents) {
    return '';
  }
  if (typeof contents === 'string') {
    return contents;
  }
  if (typeof contents.value === 'string') {
    if (contents.kind === 'markdown') {
      const md = new vscode.MarkdownString(contents.value);
      md.isTrusted = false;
      return md;
    }
    return contents.value;
  }
  return String(contents);
}

function mapDocumentSymbol(sym) {
  const ds = new vscode.DocumentSymbol(
    sym.name || '',
    sym.detail || '',
    mapSymbolKind(sym.kind),
    fromLspRange(sym.range),
    fromLspRange(sym.selectionRange || sym.range)
  );
  if (Array.isArray(sym.children) && sym.children.length > 0) {
    ds.children = sym.children.map(mapDocumentSymbol);
  }
  return ds;
}

function resolveBqplsBinary() {
  const cfg = vscode.workspace.getConfiguration('bqpls');
  const configured = (cfg.get('path') || '').trim();
  if (configured.length > 0) {
    return configured;
  }
  if (DEFAULT_BQPLS_PATH && DEFAULT_BQPLS_PATH !== '__BQPLS_' + 'BIN_PATH__' && fs.existsSync(DEFAULT_BQPLS_PATH)) {
    return DEFAULT_BQPLS_PATH;
  }
  return 'bqpls';
}

function getTraceLevel() {
  const cfg = vscode.workspace.getConfiguration('bqpls');
  return cfg.get('trace.server') || 'off';
}

const LSP_SETTING_KEYS = [
  'defaultProject',
  'defaultDataset',
  'schemaDirs',
  'bigqueryApiEnabled',
  'gcloudPath',
  'cacheDir',
  'cacheTtlSeconds',
  'projectCacheTtlSeconds',
  'diagnosticsEnabled',
  'requirePartitionFilterSeverity',
  'missingPartitionFilterSeverity',
  'clusteringFilterSeverity',
  'costEstimationEnabled',
  'costCodeLensEnabled',
  'costStatusBarEnabled',
  'costHoverEnabled',
  'costDiagnosticSeverity',
  'costWarningThresholdBytes',
  'costWarningThresholdUsd',
  'costPricePerTibUsd',
  'costDryRunDebounceMs'
];

function isExplicitlyConfigured(inspected) {
  if (!inspected) {
    return false;
  }
  return (
    inspected.workspaceFolderLanguageValue !== undefined ||
    inspected.workspaceLanguageValue !== undefined ||
    inspected.globalLanguageValue !== undefined ||
    inspected.workspaceFolderValue !== undefined ||
    inspected.workspaceValue !== undefined ||
    inspected.globalValue !== undefined
  );
}

function mergeProjectTtlScope(dst, rawObj) {
  if (!rawObj || typeof rawObj !== 'object' || Array.isArray(rawObj)) {
    return false;
  }
  let added = false;
  for (const [rawKey, val] of Object.entries(rawObj)) {
    const key = String(rawKey || '').trim().toLowerCase();
    if (!key || typeof val !== 'number' || !Number.isFinite(val) || val < 0) {
      continue;
    }
    const ttl = Math.trunc(val);
    if (!Object.prototype.hasOwnProperty.call(dst, key) || ttl > dst[key]) {
      dst[key] = ttl;
    }
    added = true;
  }
  return added;
}

function readMergedProjectCacheTtls(inspected) {
  if (!inspected) {
    return null;
  }
  const merged = {};
  let hasAny = false;
  const scopes = [
    inspected.globalValue,
    inspected.globalLanguageValue,
    inspected.workspaceValue,
    inspected.workspaceLanguageValue,
    inspected.workspaceFolderValue,
    inspected.workspaceFolderLanguageValue
  ];
  for (const scopeVal of scopes) {
    if (mergeProjectTtlScope(merged, scopeVal)) {
      hasAny = true;
    }
  }
  return hasAny ? merged : null;
}

function readLspSettings() {
  const cfg = vscode.workspace.getConfiguration('bqpls');
  const settings = {};
  for (const key of LSP_SETTING_KEYS) {
    const inspected = cfg.inspect(key);
    if (!isExplicitlyConfigured(inspected)) {
      continue;
    }
    if (key === 'projectCacheTtlSeconds') {
      const mergedTtls = readMergedProjectCacheTtls(inspected);
      if (mergedTtls) {
        settings[key] = mergedTtls;
      }
      continue;
    }
    const val = cfg.get(key);
    if (val === undefined || val === null) {
      continue;
    }
    if (typeof val === 'string' && val.trim() === '') {
      continue;
    }
    if (Array.isArray(val) && val.length === 0) {
      continue;
    }
    settings[key] = val;
  }
  return settings;
}

class BqplsClient {
  constructor(output, diagnostics, semanticRefreshEmitter, codeLensRefreshEmitter, onQueryCost) {
    this.output = output;
    this.diagnostics = diagnostics;
    this.semanticRefreshEmitter = semanticRefreshEmitter;
    this.codeLensRefreshEmitter = codeLensRefreshEmitter;
    this.onQueryCost = onQueryCost;
    this.proc = null;
    this.buffer = Buffer.alloc(0);
    this.nextId = 1;
    this.pending = new Map();
    this.resultIds = new Map();
    this.openDocs = new Set();
    this.initialized = false;
    this.startPromise = null;
    this.disposed = false;
  }

  trace(level, message) {
    const current = getTraceLevel();
    if (current === 'off') {
      return;
    }
    if (level === 'verbose' && current !== 'verbose') {
      return;
    }
    this.output.appendLine(`[trace] ${message}`);
  }

  async ensureStarted() {
    if (this.disposed) {
      throw new Error('bqpls client disposed');
    }
    if (this.initialized && this.proc) {
      return;
    }
    if (this.startPromise) {
      return this.startPromise;
    }
    this.startPromise = this._start().finally(() => {
      this.startPromise = null;
    });
    return this.startPromise;
  }

  async _start() {
    const bin = resolveBqplsBinary();
    const folders = vscode.workspace.workspaceFolders || [];
    const cwd = folders.length > 0 ? folders[0].uri.fsPath : undefined;

    this.output.appendLine(`Starting ${bin} serve`);
    const proc = cp.spawn(bin, ['serve'], {
      cwd,
      stdio: ['pipe', 'pipe', 'pipe']
    });
    this.proc = proc;
    this.buffer = Buffer.alloc(0);
    this.initialized = false;

    proc.stdout.on('data', (chunk) => this._onStdoutData(chunk));
    proc.stderr.on('data', (chunk) => {
      const text = chunk.toString('utf8').trimEnd();
      if (text) {
        this.output.appendLine(`[stderr] ${text}`);
      }
    });
    proc.on('error', (err) => {
      this.output.appendLine(`bqpls process error: ${err.message}`);
      this._rejectAllPending(err);
      this.proc = null;
      this.initialized = false;
    });
    proc.on('exit', (code, signal) => {
      this.output.appendLine(`bqpls exited (code=${code}, signal=${signal})`);
      this._rejectAllPending(new Error(`bqpls exited (${code})`));
      this.proc = null;
      this.initialized = false;
    });

    const workspaceFolders = folders.map((f) => ({
      uri: f.uri.toString(),
      name: f.name
    }));

    const initParams = {
      processId: process.pid,
      clientInfo: {
        name: 'vscode-bqlang',
        version: '0.1.0'
      },
      rootUri: folders.length > 0 ? folders[0].uri.toString() : null,
      workspaceFolders,
      initializationOptions: readLspSettings(),
      capabilities: {
        workspace: {
          semanticTokens: {
            refreshSupport: true
          },
          codeLens: {
            refreshSupport: true
          }
        },
        textDocument: {
          synchronization: {
            dynamicRegistration: false,
            didSave: true
          },
          hover: {
            contentFormat: ['markdown', 'plaintext']
          },
          signatureHelp: {
            signatureInformation: {
              documentationFormat: ['markdown', 'plaintext'],
              parameterInformation: {
                labelOffsetSupport: true
              },
              activeParameterSupport: true
            },
            contextSupport: true
          },
          completion: {
            completionItem: {
              documentationFormat: ['markdown', 'plaintext'],
              labelDetailsSupport: true,
              snippetSupport: false
            }
          },
          definition: {},
          documentSymbol: {
            hierarchicalDocumentSymbolSupport: true
          },
          formatting: {},
          semanticTokens: {
            requests: {
              range: true,
              full: {
                delta: true
              }
            },
            tokenTypes: TOKEN_TYPES,
            tokenModifiers: TOKEN_MODIFIERS,
            formats: ['relative']
          },
          publishDiagnostics: {}
        }
      }
    };

    await this._sendRequestRaw('initialize', initParams);
    this.initialized = true;
    this.sendNotification('initialized', {});

    for (const doc of vscode.workspace.textDocuments) {
      if (doc.languageId === LANGUAGE_ID) {
        this.didOpen(doc);
      }
    }
  }

  _rejectAllPending(err) {
    for (const [, entry] of this.pending) {
      entry.reject(err);
    }
    this.pending.clear();
  }

  _writeFrame(payload) {
    if (!this.proc || !this.proc.stdin || this.proc.stdin.destroyed) {
      return false;
    }
    const body = JSON.stringify(payload);
    const bodyBuf = Buffer.from(body, 'utf8');
    const headerBuf = Buffer.from(`Content-Length: ${bodyBuf.length}\r\n\r\n`, 'ascii');
    this.trace('verbose', `-> ${body}`);
    this.proc.stdin.write(Buffer.concat([headerBuf, bodyBuf]));
    return true;
  }

  _onStdoutData(chunk) {
    this.buffer = Buffer.concat([this.buffer, chunk]);
    while (true) {
      const headerEnd = this.buffer.indexOf('\r\n\r\n');
      if (headerEnd < 0) {
        return;
      }
      const headerStr = this.buffer.slice(0, headerEnd).toString('ascii');
      const match = /^content-length:\s*(\d+)\s*$/im.exec(headerStr);
      if (!match) {
        this.output.appendLine(`Invalid LSP header: ${headerStr}`);
        this.buffer = this.buffer.slice(headerEnd + 4);
        continue;
      }
      const contentLength = parseInt(match[1], 10);
      const frameStart = headerEnd + 4;
      if (this.buffer.length < frameStart + contentLength) {
        return;
      }
      const bodyStr = this.buffer.slice(frameStart, frameStart + contentLength).toString('utf8');
      this.buffer = this.buffer.slice(frameStart + contentLength);
      this.trace('verbose', `<- ${bodyStr}`);
      try {
        const msg = JSON.parse(bodyStr);
        this._handleMessage(msg);
      } catch (err) {
        this.output.appendLine(`Failed to parse LSP message: ${err.message}`);
      }
    }
  }

  _handleMessage(msg) {
    if (!msg || typeof msg !== 'object') {
      return;
    }
    const hasId = Object.prototype.hasOwnProperty.call(msg, 'id') && msg.id !== null && msg.id !== undefined;
    if (hasId && !msg.method) {
      const entry = this.pending.get(msg.id);
      if (!entry) {
        return;
      }
      this.pending.delete(msg.id);
      if (msg.error) {
        const e = new Error(msg.error.message || 'LSP error');
        e.code = msg.error.code;
        entry.reject(e);
      } else {
        entry.resolve(msg.result);
      }
      return;
    }
    if (hasId && msg.method) {
      this._handleServerRequest(msg);
      return;
    }
    if (msg.method) {
      this._handleNotification(msg);
    }
  }

  _handleServerRequest(msg) {
    this.trace('messages', `<- request ${msg.method} (id=${msg.id})`);
    if (msg.method === 'workspace/semanticTokens/refresh') {
      this.semanticRefreshEmitter.fire();
      this._writeFrame({ jsonrpc: '2.0', id: msg.id, result: null });
      return;
    }
    if (msg.method === 'workspace/codeLens/refresh') {
      if (this.codeLensRefreshEmitter) {
        this.codeLensRefreshEmitter.fire();
      }
      this._writeFrame({ jsonrpc: '2.0', id: msg.id, result: null });
      return;
    }
    this._writeFrame({
      jsonrpc: '2.0',
      id: msg.id,
      error: { code: -32601, message: `Method not found: ${msg.method}` }
    });
  }

  _handleNotification(msg) {
    this.trace('messages', `<- notification ${msg.method}`);
    if (msg.method === 'bqpls/queryCost' && msg.params) {
      if (typeof this.onQueryCost === 'function') {
        this.onQueryCost(msg.params);
      }
      return;
    }
    if (msg.method === 'textDocument/publishDiagnostics' && msg.params) {
      const uriStr = msg.params.uri;
      if (!uriStr) {
        return;
      }
      const uri = vscode.Uri.parse(uriStr);
      const list = Array.isArray(msg.params.diagnostics) ? msg.params.diagnostics : [];
      const converted = list.map((d) => {
        const diag = new vscode.Diagnostic(
          fromLspRange(d.range),
          d.message || '',
          mapDiagnosticSeverity(d.severity)
        );
        if (d.source) {
          diag.source = d.source;
        }
        if (d.code !== undefined && d.code !== null && d.code !== '') {
          diag.code = d.code;
        }
        return diag;
      });
      this.diagnostics.set(uri, converted);
    }
  }

  _sendRequestRaw(method, params, token) {
    const id = this.nextId++;
    this.trace('messages', `-> request ${method} (id=${id})`);
    return new Promise((resolve, reject) => {
      let cancelSub = null;
      const cleanup = () => {
        if (cancelSub) {
          cancelSub.dispose();
          cancelSub = null;
        }
      };
      this.pending.set(id, {
        resolve: (val) => {
          cleanup();
          resolve(val);
        },
        reject: (err) => {
          cleanup();
          reject(err);
        }
      });
      if (token) {
        if (token.isCancellationRequested) {
          this.pending.delete(id);
          cleanup();
          reject(new vscode.CancellationError());
          return;
        }
        cancelSub = token.onCancellationRequested(() => {
          this.sendNotification('$/cancelRequest', { id });
        });
      }
      if (!this._writeFrame({ jsonrpc: '2.0', id, method, params })) {
        this.pending.delete(id);
        cleanup();
        reject(new Error('Failed to write LSP request to bqpls'));
      }
    });
  }

  async sendRequest(method, params, token) {
    await this.ensureStarted();
    return this._sendRequestRaw(method, params, token);
  }

  sendNotification(method, params) {
    if (!this.proc) {
      return;
    }
    this.trace('messages', `-> notification ${method}`);
    this._writeFrame({ jsonrpc: '2.0', method, params });
  }

  async ensureDocumentOpen(doc) {
    await this.ensureStarted();
    const uri = doc.uri.toString();
    if (!this.openDocs.has(uri)) {
      this.didOpen(doc);
    }
  }

  didOpen(doc) {
    const uri = doc.uri.toString();
    this.openDocs.add(uri);
    this.sendNotification('textDocument/didOpen', {
      textDocument: {
        uri,
        languageId: doc.languageId,
        version: doc.version,
        text: doc.getText()
      }
    });
  }

  didChange(event) {
    if (!event.contentChanges || event.contentChanges.length === 0) {
      return;
    }
    const uri = event.document.uri.toString();
    if (!this.openDocs.has(uri)) {
      this.didOpen(event.document);
      return;
    }
    this.sendNotification('textDocument/didChange', {
      textDocument: {
        uri,
        version: event.document.version
      },
      contentChanges: event.contentChanges.map((ch) => ({
        range: toLspRange(ch.range),
        rangeLength: ch.rangeLength,
        text: ch.text
      }))
    });
  }

  didSave(doc) {
    this.sendNotification('textDocument/didSave', {
      textDocument: {
        uri: doc.uri.toString()
      }
    });
  }

  didClose(doc) {
    const uri = doc.uri.toString();
    this.openDocs.delete(uri);
    this.resultIds.delete(uri);
    this.diagnostics.delete(doc.uri);
    this.sendNotification('textDocument/didClose', {
      textDocument: {
        uri
      }
    });
  }

  async stop() {
    if (!this.proc) {
      return;
    }
    const proc = this.proc;
    try {
      if (this.initialized) {
        await Promise.race([
          this._sendRequestRaw('shutdown', null),
          new Promise((resolve) => setTimeout(resolve, 1500))
        ]);
        this.sendNotification('exit', null);
      }
    } catch (_err) {
      // Ignore shutdown errors on teardown.
    }
    this.initialized = false;
    this.openDocs.clear();
    this.resultIds.clear();
    if (proc && !proc.killed) {
      setTimeout(() => {
        try {
          if (!proc.killed) {
            proc.kill();
          }
        } catch (_err) {
          // Ignore kill errors.
        }
      }, 300);
    }
    this.proc = null;
  }

  async restart() {
    await this.stop();
    await this.ensureStarted();
  }

  async dispose() {
    this.disposed = true;
    await this.stop();
  }
}

function formatPreciseUsd(usd) {
  if (typeof usd !== 'number' || !Number.isFinite(usd) || usd <= 0) {
    return '$0.00';
  }
  if (usd < 0.01) {
    return `$${usd.toFixed(4)}`;
  }
  return `$${usd.toFixed(2)}`;
}

function findStatementAtLine(statements, line) {
  if (!Array.isArray(statements) || statements.length === 0) {
    return null;
  }
  for (const st of statements) {
    if (st && st.range && st.range.start && st.range.end) {
      if (line >= st.range.start.line && line <= st.range.end.line) {
        return st;
      }
    }
  }
  return null;
}

function buildCostErrorTooltip(cost) {
  const md = new vscode.MarkdownString();
  md.isTrusted = false;
  const isDryRunOnly =
    Array.isArray(cost.errors) &&
    cost.errors.length > 0 &&
    cost.errors.every((e) => e && e.source === 'dry_run');
  md.appendMarkdown(isDryRunOnly ? '**BigQuery Dry Run Error**\n\n' : '**Invalid SQL**\n\n');
  const list = Array.isArray(cost.errors) ? cost.errors : [];
  if (list.length > 0) {
    const shown = list.slice(0, 5);
    for (const item of shown) {
      if (!item || !item.message) {
        continue;
      }
      const loc = item.line > 0 ? `Line ${item.line}: ` : '';
      md.appendMarkdown(`- ${loc}${item.message}\n`);
    }
    if (list.length > shown.length) {
      md.appendMarkdown(`\n…and ${list.length - shown.length} more error(s).\n`);
    }
  } else if (cost.errorMessage) {
    md.appendMarkdown(`${cost.errorMessage}\n`);
  }
  md.appendMarkdown(
    isDryRunOnly ? '\nClick to retry BigQuery dry run.' : '\nClick to open the Problems panel.'
  );
  return md;
}

function hasDisplayableCost(cost) {
  if (!cost || !cost.enabled) {
    return false;
  }
  if (typeof cost.status === 'string' && cost.status.length > 0) {
    return true;
  }
  return Array.isArray(cost.statements) && cost.statements.length > 0;
}

function buildCostTooltip(cost, activeStmt) {
  if (cost.status === 'error') {
    return buildCostErrorTooltip(cost);
  }
  if (cost.status === 'pending') {
    const md = new vscode.MarkdownString('**BigQuery Dry Run**\n\nEstimating query bytes processed…');
    md.isTrusted = false;
    return md;
  }
  if (cost.status === 'unavailable') {
    const md = new vscode.MarkdownString();
    md.isTrusted = false;
    md.appendMarkdown('**Estimated Query Cost Unavailable**\n\n');
    if (cost.errorMessage) {
      md.appendMarkdown(`${cost.errorMessage}\n\n`);
    }
    md.appendMarkdown('Click to retry BigQuery dry run.');
    return md;
  }
  const md = new vscode.MarkdownString();
  md.isTrusted = false;
  const sourceLabel = cost.source === 'dry_run' ? 'BigQuery Dry Run' : 'Offline table-size upper bound';
  md.appendMarkdown(`**Estimated Query Cost** (${sourceLabel})\n\n`);
  if (activeStmt && cost.statements.length > 1) {
    md.appendMarkdown(
      `Active statement: **${activeStmt.formattedBytes}** (${formatPreciseUsd(activeStmt.estimatedCostUsd)})\n\n`
    );
    md.appendMarkdown(
      `Document total (${cost.statements.length} statements): **${cost.formattedBytes}** (${formatPreciseUsd(cost.totalEstimatedCostUsd)})\n\n`
    );
  } else {
    md.appendMarkdown(
      `Scan: **${cost.formattedBytes}** (${formatPreciseUsd(cost.totalEstimatedCostUsd)})\n\n`
    );
  }
  if (cost.exceedsThreshold) {
    md.appendMarkdown(`Warning: estimated scan exceeds configured threshold.\n\n`);
  }
  md.appendMarkdown(`Click to refresh estimated cost.`);
  return md;
}

let activeClient = null;

function activate(context) {
  const output = vscode.window.createOutputChannel('bqpls');
  const diagnostics = vscode.languages.createDiagnosticCollection(LANGUAGE_ID);
  const semanticRefreshEmitter = new vscode.EventEmitter();
  const codeLensRefreshEmitter = new vscode.EventEmitter();
  const costStatusBar = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Right, 100);
  costStatusBar.command = 'bqpls.refreshCost';
  const costByUri = new Map();

  const updateCostStatusBar = (editor) => {
    const active = editor || vscode.window.activeTextEditor;
    if (!active || !active.document || active.document.languageId !== LANGUAGE_ID) {
      costStatusBar.hide();
      return;
    }
    const cost = costByUri.get(active.document.uri.toString());
    if (!hasDisplayableCost(cost)) {
      costStatusBar.hide();
      return;
    }
    if (cost.status === 'error') {
      const hasLocalErr =
        Array.isArray(cost.errors) &&
        cost.errors.some((e) => e && (e.source === 'syntax' || e.source === 'semantic'));
      costStatusBar.text = `$(error) ${cost.summary || 'Invalid SQL'}`;
      costStatusBar.backgroundColor = new vscode.ThemeColor('statusBarItem.errorBackground');
      costStatusBar.command = hasLocalErr ? 'workbench.actions.view.problems' : 'bqpls.refreshCost';
      costStatusBar.tooltip = buildCostTooltip(cost, null);
      costStatusBar.show();
      return;
    }
    if (cost.status === 'pending') {
      costStatusBar.text = `$(sync~spin) ${cost.summary || 'Estimating...'}`;
      costStatusBar.backgroundColor = undefined;
      costStatusBar.command = 'bqpls.refreshCost';
      costStatusBar.tooltip = buildCostTooltip(cost, null);
      costStatusBar.show();
      return;
    }
    if (cost.status === 'unavailable') {
      costStatusBar.text = `$(info) ${cost.summary || 'Cost unavailable'}`;
      costStatusBar.backgroundColor = new vscode.ThemeColor('statusBarItem.warningBackground');
      costStatusBar.command = 'bqpls.refreshCost';
      costStatusBar.tooltip = buildCostTooltip(cost, null);
      costStatusBar.show();
      return;
    }
    const stmts = Array.isArray(cost.statements) ? cost.statements : [];
    const cursorLine = active.selection ? active.selection.active.line : 0;
    const activeStmt = stmts.length > 1 ? findStatementAtLine(stmts, cursorLine) : null;
    const displayLabel = activeStmt ? activeStmt.label : cost.summary;
    const warn = activeStmt ? Boolean(activeStmt.exceedsThreshold) : Boolean(cost.exceedsThreshold);
    const cleanLabel = String(displayLabel || '').replace(/^⚠\s*/, '');
    costStatusBar.text = `${warn ? '$(warning)' : '$(database)'} ${cleanLabel}`;
    costStatusBar.backgroundColor = warn
      ? new vscode.ThemeColor('statusBarItem.warningBackground')
      : undefined;
    costStatusBar.command = 'bqpls.refreshCost';
    costStatusBar.tooltip = buildCostTooltip(cost, activeStmt);
    costStatusBar.show();
  };

  const client = new BqplsClient(
    output,
    diagnostics,
    semanticRefreshEmitter,
    codeLensRefreshEmitter,
    (params) => {
      if (!params || !params.uri) {
        return;
      }
      if (!hasDisplayableCost(params)) {
        costByUri.delete(params.uri);
      } else {
        costByUri.set(params.uri, params);
      }
      updateCostStatusBar();
    }
  );
  activeClient = client;

  context.subscriptions.push(output, diagnostics, semanticRefreshEmitter, codeLensRefreshEmitter, costStatusBar);

  const selector = { language: LANGUAGE_ID };

  context.subscriptions.push(
    vscode.languages.registerDocumentSemanticTokensProvider(
      selector,
      {
        onDidChangeSemanticTokens: semanticRefreshEmitter.event,
        async provideDocumentSemanticTokens(document, token) {
          await client.ensureDocumentOpen(document);
          const uri = document.uri.toString();
          const res = await client.sendRequest(
            'textDocument/semanticTokens/full',
            { textDocument: { uri } },
            token
          );
          if (!res) {
            return new vscode.SemanticTokens(new Uint32Array(0));
          }
          if (res.resultId) {
            client.resultIds.set(uri, res.resultId);
          }
          return new vscode.SemanticTokens(new Uint32Array(res.data || []), res.resultId);
        },
        async provideDocumentSemanticTokensEdits(document, previousResultId, token) {
          await client.ensureDocumentOpen(document);
          const uri = document.uri.toString();
          const prevId = previousResultId || client.resultIds.get(uri) || '';
          if (!prevId) {
            const full = await client.sendRequest(
              'textDocument/semanticTokens/full',
              { textDocument: { uri } },
              token
            );
            if (!full) {
              return new vscode.SemanticTokens(new Uint32Array(0));
            }
            if (full.resultId) {
              client.resultIds.set(uri, full.resultId);
            }
            return new vscode.SemanticTokens(new Uint32Array(full.data || []), full.resultId);
          }
          const res = await client.sendRequest(
            'textDocument/semanticTokens/full/delta',
            { textDocument: { uri }, previousResultId: prevId },
            token
          );
          if (!res) {
            return new vscode.SemanticTokens(new Uint32Array(0));
          }
          if (res.resultId) {
            client.resultIds.set(uri, res.resultId);
          }
          if (Array.isArray(res.edits)) {
            const edits = res.edits.map(
              (e) =>
                new vscode.SemanticTokensEdit(
                  e.start || 0,
                  e.deleteCount || 0,
                  e.data ? new Uint32Array(e.data) : undefined
                )
            );
            return new vscode.SemanticTokensEdits(edits, res.resultId);
          }
          return new vscode.SemanticTokens(new Uint32Array(res.data || []), res.resultId);
        }
      },
      SEMANTIC_LEGEND
    )
  );

  context.subscriptions.push(
    vscode.languages.registerDocumentRangeSemanticTokensProvider(
      selector,
      {
        async provideDocumentRangeSemanticTokens(document, range, token) {
          await client.ensureDocumentOpen(document);
          const res = await client.sendRequest(
            'textDocument/semanticTokens/range',
            {
              textDocument: { uri: document.uri.toString() },
              range: toLspRange(range)
            },
            token
          );
          if (!res) {
            return new vscode.SemanticTokens(new Uint32Array(0));
          }
          return new vscode.SemanticTokens(new Uint32Array(res.data || []), res.resultId);
        }
      },
      SEMANTIC_LEGEND
    )
  );

  context.subscriptions.push(
    vscode.languages.registerHoverProvider(selector, {
      async provideHover(document, position, token) {
        await client.ensureDocumentOpen(document);
        const res = await client.sendRequest(
          'textDocument/hover',
          {
            textDocument: { uri: document.uri.toString() },
            position: toLspPosition(position)
          },
          token
        );
        if (!res || !res.contents) {
          return null;
        }
        const contents = mapMarkupContent(res.contents);
        return res.range ? new vscode.Hover(contents, fromLspRange(res.range)) : new vscode.Hover(contents);
      }
    })
  );

  context.subscriptions.push(
    vscode.languages.registerSignatureHelpProvider(
      selector,
      {
        async provideSignatureHelp(document, position, token, context) {
          await client.ensureDocumentOpen(document);
          const params = {
            textDocument: { uri: document.uri.toString() },
            position: toLspPosition(position)
          };
          if (context) {
            params.context = {
              triggerKind: Number.isInteger(context.triggerKind) ? context.triggerKind : 1,
              triggerCharacter: context.triggerCharacter,
              isRetrigger: Boolean(context.isRetrigger)
            };
          }
          const res = await client.sendRequest('textDocument/signatureHelp', params, token);
          if (!res || !Array.isArray(res.signatures) || res.signatures.length === 0) {
            return null;
          }
          const help = new vscode.SignatureHelp();
          help.activeSignature = Number.isInteger(res.activeSignature) ? res.activeSignature : 0;
          help.activeParameter = Number.isInteger(res.activeParameter) ? res.activeParameter : 0;
          help.signatures = res.signatures.map((sig) => {
            const info = new vscode.SignatureInformation(sig.label || '');
            if (sig.documentation) {
              info.documentation = mapMarkupContent(sig.documentation);
            }
            if (Array.isArray(sig.parameters)) {
              info.parameters = sig.parameters.map((p) => {
                const label = Array.isArray(p.label) && p.label.length === 2 ? [p.label[0], p.label[1]] : String(p.label || '');
                const param = new vscode.ParameterInformation(label);
                if (p.documentation) {
                  param.documentation = mapMarkupContent(p.documentation);
                }
                return param;
              });
            }
            if (Number.isInteger(sig.activeParameter)) {
              info.activeParameter = sig.activeParameter;
            }
            return info;
          });
          return help;
        }
      },
      {
        triggerCharacters: ['(', ','],
        retriggerCharacters: [',', ' ']
      }
    )
  );

  context.subscriptions.push(
    vscode.languages.registerCompletionItemProvider(
      selector,
      {
        async provideCompletionItems(document, position, token, triggerContext) {
          await client.ensureDocumentOpen(document);
          const params = {
            textDocument: { uri: document.uri.toString() },
            position: toLspPosition(position)
          };
          if (triggerContext) {
            params.context = {
              triggerKind: Number.isInteger(triggerContext.triggerKind) ? triggerContext.triggerKind + 1 : 1,
              triggerCharacter: triggerContext.triggerCharacter
            };
          }
          const res = await client.sendRequest('textDocument/completion', params, token);
          if (!res) {
            return new vscode.CompletionList([], false);
          }
          const rawItems = Array.isArray(res) ? res : res.items || [];
          const items = rawItems.map((it) => {
            const label =
              it.labelDetails && (it.labelDetails.detail || it.labelDetails.description)
                ? {
                    label: it.label,
                    detail: it.labelDetails.detail || undefined,
                    description: it.labelDetails.description || undefined
                  }
                : it.label;
            const item = new vscode.CompletionItem(label, mapCompletionKind(it.kind));
            if (it.detail) {
              item.detail = it.detail;
            }
            if (it.documentation) {
              item.documentation = mapMarkupContent(it.documentation);
            }
            if (it.sortText) {
              item.sortText = it.sortText;
            }
            if (it.filterText) {
              item.filterText = it.filterText;
            } else if (typeof label === 'object') {
              item.filterText = it.label;
            }
            if (it.insertText) {
              item.insertText = it.insertText;
            }
            return item;
          });
          return new vscode.CompletionList(items, Boolean(res.isIncomplete));
        }
      },
      '.',
      '|',
      '{',
      '%',
      '@',
      '`'
    )
  );

  context.subscriptions.push(
    vscode.languages.registerDefinitionProvider(selector, {
      async provideDefinition(document, position, token) {
        await client.ensureDocumentOpen(document);
        const res = await client.sendRequest(
          'textDocument/definition',
          {
            textDocument: { uri: document.uri.toString() },
            position: toLspPosition(position)
          },
          token
        );
        if (!res) {
          return [];
        }
        const locs = Array.isArray(res) ? res : [res];
        return locs.map((loc) => new vscode.Location(vscode.Uri.parse(loc.uri), fromLspRange(loc.range)));
      }
    })
  );

  context.subscriptions.push(
    vscode.languages.registerDocumentSymbolProvider(selector, {
      async provideDocumentSymbols(document, token) {
        await client.ensureDocumentOpen(document);
        const res = await client.sendRequest(
          'textDocument/documentSymbol',
          { textDocument: { uri: document.uri.toString() } },
          token
        );
        if (!Array.isArray(res)) {
          return [];
        }
        return res.map(mapDocumentSymbol);
      }
    })
  );

  context.subscriptions.push(
    vscode.languages.registerCodeLensProvider(selector, {
      onDidChangeCodeLenses: codeLensRefreshEmitter.event,
      async provideCodeLenses(document, token) {
        await client.ensureDocumentOpen(document);
        const res = await client.sendRequest(
          'textDocument/codeLens',
          { textDocument: { uri: document.uri.toString() } },
          token
        );
        if (!Array.isArray(res)) {
          return [];
        }
        return res.map((lens) => {
          const range = fromLspRange(lens.range);
          if (lens.command) {
            return new vscode.CodeLens(range, {
              title: lens.command.title || '',
              command: lens.command.command || '',
              arguments: Array.isArray(lens.command.arguments) ? lens.command.arguments : []
            });
          }
          return new vscode.CodeLens(range);
        });
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('bqpls.refreshCost', async (targetUri) => {
      let uriStr = typeof targetUri === 'string' && targetUri ? targetUri : '';
      if (!uriStr && vscode.window.activeTextEditor && vscode.window.activeTextEditor.document.languageId === LANGUAGE_ID) {
        await client.ensureDocumentOpen(vscode.window.activeTextEditor.document);
        uriStr = vscode.window.activeTextEditor.document.uri.toString();
      }
      if (!uriStr) {
        return;
      }
      try {
        const res = await client.sendRequest('bqpls/refreshCost', {
          textDocument: { uri: uriStr }
        });
        if (res && res.uri) {
          if (hasDisplayableCost(res)) {
            costByUri.set(res.uri, res);
          } else {
            costByUri.delete(res.uri);
          }
          updateCostStatusBar();
        }
        codeLensRefreshEmitter.fire();
      } catch (err) {
        output.appendLine(`Failed to refresh query cost: ${err.message}`);
      }
    })
  );

  context.subscriptions.push(
    vscode.window.onDidChangeActiveTextEditor((editor) => {
      updateCostStatusBar(editor);
    })
  );

  context.subscriptions.push(
    vscode.window.onDidChangeTextEditorSelection((event) => {
      if (event && event.textEditor === vscode.window.activeTextEditor) {
        updateCostStatusBar(event.textEditor);
      }
    })
  );

  context.subscriptions.push(
    vscode.languages.registerDocumentFormattingEditProvider(selector, {
      async provideDocumentFormattingEdits(document, options, token) {
        await client.ensureDocumentOpen(document);
        const res = await client.sendRequest(
          'textDocument/formatting',
          {
            textDocument: { uri: document.uri.toString() },
            options: {
              tabSize: options.tabSize,
              insertSpaces: options.insertSpaces
            }
          },
          token
        );
        if (!Array.isArray(res)) {
          return [];
        }
        return res.map((ed) => vscode.TextEdit.replace(fromLspRange(ed.range), ed.newText || ''));
      }
    })
  );

  context.subscriptions.push(
    vscode.workspace.onDidOpenTextDocument((doc) => {
      if (doc.languageId === LANGUAGE_ID) {
        client.ensureStarted().then(() => client.didOpen(doc)).catch((err) => {
          output.appendLine(`Failed to open document in bqpls: ${err.message}`);
        });
      }
    })
  );

  context.subscriptions.push(
    vscode.workspace.onDidChangeTextDocument((event) => {
      if (event.document.languageId === LANGUAGE_ID) {
        client.ensureStarted().then(() => client.didChange(event)).catch((err) => {
          output.appendLine(`Failed to sync change to bqpls: ${err.message}`);
        });
      }
    })
  );

  context.subscriptions.push(
    vscode.workspace.onDidSaveTextDocument((doc) => {
      if (doc.languageId === LANGUAGE_ID || path.basename(doc.fileName || '') === '.bqfmt.toml') {
        client.ensureStarted().then(() => client.didSave(doc)).catch((err) => {
          output.appendLine(`Failed to notify save to bqpls: ${err.message}`);
        });
      }
    })
  );

  context.subscriptions.push(
    vscode.workspace.onDidCloseTextDocument((doc) => {
      if (doc.languageId === LANGUAGE_ID) {
        costByUri.delete(doc.uri.toString());
        updateCostStatusBar();
        client.didClose(doc);
      }
    })
  );

  context.subscriptions.push(
    vscode.workspace.onDidChangeConfiguration((event) => {
      if (event.affectsConfiguration('bqpls.path')) {
        client.restart().catch((err) => {
          output.appendLine(`Failed to restart bqpls: ${err.message}`);
        });
        return;
      }
      if (LSP_SETTING_KEYS.some((key) => event.affectsConfiguration(`bqpls.${key}`))) {
        client.ensureStarted().then(() => {
          client.sendNotification('workspace/didChangeConfiguration', {
            settings: readLspSettings()
          });
        }).catch((err) => {
          output.appendLine(`Failed to sync bqpls configuration: ${err.message}`);
        });
      }
    })
  );

  client.ensureStarted().catch((err) => {
    output.appendLine(`Initial bqpls startup failed: ${err.message}`);
  });
}

async function deactivate() {
  if (activeClient) {
    const client = activeClient;
    activeClient = null;
    await client.dispose();
  }
}

module.exports = {
  activate,
  deactivate
};
