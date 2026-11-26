package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/paulourio/bqlang/internal/analysis"
	"github.com/paulourio/bqlang/internal/bqconfig"
	"github.com/paulourio/bqlang/internal/catalog"
)

const defaultCheckFetchTimeout = 15 * time.Second

type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSliceFlag) Set(val string) error {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" {
		return errors.New("directory path must not be empty")
	}

	*s = append(*s, trimmed)

	return nil
}

type checkOptions struct {
	configPath     string
	format         string
	failOn         analysis.DiagnosticSeverity
	stdinFilename  string
	project        string
	dataset        string
	schemaDirs     []string
	bigqueryAPI    bool
	bigqueryAPISet bool
	fetchTimeout   time.Duration
	paths          []string
	storeOptions   []catalog.StoreOption
}

type checkFinding struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	EndLine   int    `json:"endLine"`
	EndColumn int    `json:"endColumn"`
	Severity  string `json:"severity"`
	Code      string `json:"code"`
	Source    string `json:"source,omitempty"`
	Message   string `json:"message"`

	rawSeverity analysis.DiagnosticSeverity
}

func runCheck(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, storeOpts ...catalog.StoreOption) int {
	opts, exitCode := parseCheckArgs(args, stderr)
	if exitCode >= 0 {
		return exitCode
	}

	opts.storeOptions = storeOpts

	return executeCheck(ctx, opts, stdin, stdout, stderr)
}

func parseCheckArgs(args []string, stderr io.Writer) (checkOptions, int) {
	fs := flag.NewFlagSet("bqpls check", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var schemaDirs stringSliceFlag
	configFlag := fs.String("config", "", "path to .bqfmt.toml configuration file")
	formatFlag := fs.String("format", "text", "output format (text|json)")
	failOnFlag := fs.String("fail-on", "warning", "minimum severity that exits with code 1 (error|warning|info|hint)")
	stdinFileFlag := fs.String("stdin-filename", "", "virtual filename for config discovery and reporting when reading stdin")
	projectFlag := fs.String("project", "", "override default GCP project ID")
	datasetFlag := fs.String("dataset", "", "override default BigQuery dataset ID")
	bqAPIFlag := fs.Bool("bigquery-api", false, "enable BigQuery REST v2 metadata fetching for uncached tables")
	timeoutFlag := fs.Duration("timeout", defaultCheckFetchTimeout, "per-table metadata fetch wait timeout when BigQuery API is enabled")
	fs.Var(&schemaDirs, "schema-dir", "directory containing offline BigQuery table/schema JSON files (repeatable)")

	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: bqpls check [flags] [path ...]")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return checkOptions{}, 0
		}

		return checkOptions{}, 2
	}

	bqAPISet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "bigquery-api" {
			bqAPISet = true
		}
	})

	return validateParsedCheckArgs(
		fs,
		*configFlag,
		*formatFlag,
		*failOnFlag,
		*stdinFileFlag,
		*projectFlag,
		*datasetFlag,
		schemaDirs,
		*bqAPIFlag,
		bqAPISet,
		*timeoutFlag,
		stderr,
	)
}

func validateParsedCheckArgs(
	fs *flag.FlagSet,
	configPath, formatRaw, failOnRaw, stdinFilename, project, dataset string,
	schemaDirs []string,
	bqAPI, bqAPISet bool,
	timeout time.Duration,
	stderr io.Writer,
) (checkOptions, int) {
	format := strings.ToLower(strings.TrimSpace(formatRaw))
	if format != "text" && format != "json" {
		fmt.Fprintf(stderr, "bqpls check: invalid -format %q (expected text or json)\n", formatRaw)
		return checkOptions{}, 2
	}

	failOn, ok := parseFailOnSeverity(failOnRaw)
	if !ok {
		fmt.Fprintf(stderr, "bqpls check: invalid -fail-on %q (expected error, warning, info, or hint)\n", failOnRaw)
		return checkOptions{}, 2
	}

	if stdinFilename != "" && fs.NArg() > 0 {
		fmt.Fprintln(stderr, "bqpls check: cannot use -stdin-filename with file arguments")
		return checkOptions{}, 2
	}

	if timeout <= 0 {
		fmt.Fprintf(stderr, "bqpls check: -timeout must be positive (got %v)\n", timeout)
		return checkOptions{}, 2
	}

	return checkOptions{
		configPath:     configPath,
		format:         format,
		failOn:         failOn,
		stdinFilename:  stdinFilename,
		project:        strings.TrimSpace(project),
		dataset:        strings.TrimSpace(dataset),
		schemaDirs:     slices.Clone(schemaDirs),
		bigqueryAPI:    bqAPI,
		bigqueryAPISet: bqAPISet,
		fetchTimeout:   timeout,
		paths:          fs.Args(),
	}, -1
}

func parseFailOnSeverity(raw string) (analysis.DiagnosticSeverity, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "error":
		return analysis.SeverityError, true
	case "warning":
		return analysis.SeverityWarning, true
	case "info", "information":
		return analysis.SeverityInformation, true
	case "hint":
		return analysis.SeverityHint, true
	default:
		return 0, false
	}
}

type checkSession struct {
	opts   checkOptions
	loader *bqconfig.Loader
	engine *analysis.Engine
	stores map[string]*catalog.Store
	owned  []*catalog.Store
}

func executeCheck(ctx context.Context, opts checkOptions, stdin io.Reader, stdout, stderr io.Writer) int {
	loader, err := bqconfig.NewLoader(opts.configPath)
	if err != nil {
		fmt.Fprintf(stderr, "bqpls check: %v\n", err)
		return 2
	}

	defaultOpts := bqconfig.DefaultLSPOptions()
	applyCheckCLIOverrides(defaultOpts, opts)

	defaultStore, err := catalog.NewStore(defaultOpts, opts.storeOptions...)
	if err != nil {
		fmt.Fprintf(stderr, "bqpls check: %v\n", err)
		return 2
	}

	sess := &checkSession{
		opts:   opts,
		loader: loader,
		stores: map[string]*catalog.Store{checkStoreKey(defaultOpts): defaultStore},
		owned:  []*catalog.Store{defaultStore},
	}
	defer sess.closeStores()

	eng, err := analysis.NewEngine(defaultStore, defaultOpts)
	if err != nil {
		fmt.Fprintf(stderr, "bqpls check: %v\n", err)
		return 2
	}
	sess.engine = eng

	findings, hadIOErr := sess.collectAllFindings(ctx, stdin, stderr)
	if renderErr := writeCheckFindings(stdout, opts.format, findings); renderErr != nil {
		fmt.Fprintf(stderr, "bqpls check: %v\n", renderErr)
		return 2
	}

	if hadIOErr {
		return 2
	}

	for _, f := range findings {
		if f.rawSeverity > 0 && f.rawSeverity <= opts.failOn {
			return 1
		}
	}

	return 0
}

func (s *checkSession) closeStores() {
	for _, st := range s.owned {
		if st != nil {
			_ = st.Close()
		}
	}
}

func (s *checkSession) collectAllFindings(
	ctx context.Context,
	stdin io.Reader,
	stderr io.Writer,
) (findings []checkFinding, hadErr bool) {
	if len(s.opts.paths) == 0 {
		raw, err := io.ReadAll(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "bqpls check: read stdin: %v\n", err)
			return nil, true
		}

		display := "<stdin>"
		configTarget := ""
		isStdin := true
		if s.opts.stdinFilename != "" {
			display = s.opts.stdinFilename
			configTarget = s.opts.stdinFilename
			isStdin = false
		}

		fileFindings, checkErr := s.checkContent(ctx, display, configTarget, isStdin, string(raw))
		if checkErr != nil {
			fmt.Fprintf(stderr, "bqpls check: %v\n", checkErr)
			return nil, true
		}

		return fileFindings, false
	}

	files, hadWalkErr := collectCheckTargetFiles(s.opts.paths, stderr)
	hadErr = hadWalkErr

	for _, file := range files {
		if ctx.Err() != nil {
			fmt.Fprintf(stderr, "bqpls check: %v\n", ctx.Err())
			return findings, true
		}

		raw, readErr := os.ReadFile(file)
		if readErr != nil {
			fmt.Fprintf(stderr, "bqpls check: %v\n", readErr)
			hadErr = true

			continue
		}

		fileFindings, checkErr := s.checkContent(ctx, file, file, false, string(raw))
		if checkErr != nil {
			fmt.Fprintf(stderr, "bqpls check: %v\n", checkErr)
			hadErr = true

			continue
		}

		findings = append(findings, fileFindings...)
	}

	return findings, hadErr
}

func collectCheckTargetFiles(paths []string, stderr io.Writer) (files []string, hadErr bool) {
	for _, target := range paths {
		info, err := os.Stat(target)
		if err != nil {
			fmt.Fprintf(stderr, "bqpls check: %v\n", err)
			hadErr = true

			continue
		}

		if !info.IsDir() {
			files = append(files, target)
			continue
		}

		dirFiles, dirErr := walkCheckDirectory(target, stderr)
		files = append(files, dirFiles...)
		if dirErr {
			hadErr = true
		}
	}

	return files, hadErr
}

func walkCheckDirectory(root string, stderr io.Writer) (files []string, hadErr bool) {
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, werr error) error {
		if werr != nil {
			fmt.Fprintf(stderr, "bqpls check: %v\n", werr)
			hadErr = true

			return nil
		}

		if entry.IsDir() {
			if path != root && isSkippedCheckDir(entry.Name()) {
				return filepath.SkipDir
			}

			return nil
		}

		if !strings.HasPrefix(entry.Name(), ".") && bqconfig.IsBQLFilename(entry.Name()) {
			files = append(files, path)
		}

		return nil
	})
	if walkErr != nil {
		fmt.Fprintf(stderr, "bqpls check: %v\n", walkErr)
		hadErr = true
	}

	return files, hadErr
}

func isSkippedCheckDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules"
}

func (s *checkSession) checkContent(
	ctx context.Context,
	displayPath, configTarget string,
	isStdin bool,
	sqlText string,
) ([]checkFinding, error) {
	loaded, err := s.loader.ConfigFor(configTarget, isStdin)
	if err != nil {
		return nil, err
	}

	lspOpts := bqconfig.DefaultLSPOptions()
	if loaded != nil && loaded.LSP != nil {
		cp := *loaded.LSP
		cp.SchemaDirs = slices.Clone(loaded.LSP.SchemaDirs)
		cp.ProjectCacheTTLSeconds = maps.Clone(loaded.LSP.ProjectCacheTTLSeconds)
		lspOpts = &cp
	}

	applyCheckCLIOverrides(lspOpts, s.opts)
	lspOpts.DiagnosticsEnabled = true

	store, err := s.getOrCreateStore(lspOpts)
	if err != nil {
		return nil, err
	}

	uri := checkTargetURI(displayPath, isStdin)
	s.engine.OpenDocument(uri, sqlText, 1, &analysis.DocumentConfig{
		Store:   store,
		Options: lspOpts,
	})
	defer s.engine.CloseDocument(uri)

	if lspOpts.BigQueryAPIEnabled {
		s.awaitPendingTables(ctx, uri, store, lspOpts)
	}

	diags, err := s.engine.Diagnostics(ctx, uri)
	if err != nil {
		return nil, err
	}

	out := make([]checkFinding, 0, len(diags))
	for _, d := range diags {
		out = append(out, checkFinding{
			File:        displayPath,
			Line:        d.Range.Start.Line + 1,
			Column:      d.Range.Start.Character + 1,
			EndLine:     d.Range.End.Line + 1,
			EndColumn:   d.Range.End.Character + 1,
			Severity:    severityLabel(d.Severity),
			Code:        d.Code,
			Source:      d.Source,
			Message:     d.Message,
			rawSeverity: d.Severity,
		})
	}

	return out, nil
}

func (s *checkSession) awaitPendingTables(
	ctx context.Context,
	uri string,
	store *catalog.Store,
	lspOpts *bqconfig.LSPOptions,
) {
	// Loop up to 2 passes so multi-statement scripts that uncover additional tables
	// after resolving an earlier statement also complete cleanly.
	for range 2 {
		refs, err := s.engine.UnresolvedTableRefs(uri)
		if err != nil || len(refs) == 0 {
			return
		}

		anyResolved := false
		for _, ref := range refs {
			resolved := ref.WithDefaults(lspOpts.DefaultProject, lspOpts.DefaultDataset)
			if !resolved.IsComplete() {
				continue
			}

			if _, ok := store.WaitForTable(ctx, resolved, s.opts.fetchTimeout); ok {
				anyResolved = true
			}
		}

		if !anyResolved {
			return
		}

		s.engine.InvalidateCatalog(catalog.TableRef{})
	}
}

func (s *checkSession) getOrCreateStore(lspOpts *bqconfig.LSPOptions) (*catalog.Store, error) {
	key := checkStoreKey(lspOpts)
	if st, ok := s.stores[key]; ok {
		return st, nil
	}

	st, err := catalog.NewStore(lspOpts, s.opts.storeOptions...)
	if err != nil {
		return nil, err
	}

	s.stores[key] = st
	s.owned = append(s.owned, st)

	return st, nil
}

func applyCheckCLIOverrides(dst *bqconfig.LSPOptions, opts checkOptions) {
	if opts.project != "" {
		dst.DefaultProject = opts.project
	}

	if opts.dataset != "" {
		dst.DefaultDataset = opts.dataset
	}

	if len(opts.schemaDirs) > 0 {
		resolved := make([]string, 0, len(opts.schemaDirs))
		for _, d := range opts.schemaDirs {
			if abs, err := filepath.Abs(d); err == nil {
				resolved = append(resolved, abs)
			} else {
				resolved = append(resolved, d)
			}
		}
		dst.SchemaDirs = append(dst.SchemaDirs, resolved...)
	}

	if opts.bigqueryAPISet {
		dst.BigQueryAPIEnabled = opts.bigqueryAPI
	}
}

func checkStoreKey(opts *bqconfig.LSPOptions) string {
	if opts == nil {
		return ""
	}

	return fmt.Sprintf(
		"%s\x00%s\x00%s\x00%t\x00%s\x00%s\x00%d",
		opts.DefaultProject,
		opts.DefaultDataset,
		strings.Join(opts.SchemaDirs, "\x01"),
		opts.BigQueryAPIEnabled,
		opts.GcloudPath,
		opts.CacheDir,
		opts.CacheTTLSeconds,
	)
}

func checkTargetURI(displayPath string, isStdin bool) string {
	if isStdin && displayPath == "<stdin>" {
		return "untitled:stdin.sql"
	}

	abs, err := filepath.Abs(displayPath)
	if err != nil {
		abs = displayPath
	}

	u := url.URL{
		Scheme: "file",
		Path:   filepath.ToSlash(abs),
	}

	return u.String()
}

func severityLabel(sev analysis.DiagnosticSeverity) string {
	switch sev {
	case analysis.SeverityError:
		return "error"
	case analysis.SeverityWarning:
		return "warning"
	case analysis.SeverityInformation:
		return "info"
	case analysis.SeverityHint:
		return "hint"
	default:
		return "warning"
	}
}

func writeCheckFindings(w io.Writer, format string, findings []checkFinding) error {
	if format == "json" {
		if findings == nil {
			findings = []checkFinding{}
		}

		data, err := json.MarshalIndent(findings, "", "  ")
		if err != nil {
			return fmt.Errorf("encode json findings: %w", err)
		}

		_, err = fmt.Fprintf(w, "%s\n", data)

		return err
	}

	for _, f := range findings {
		codePart := ""
		if f.Code != "" {
			codePart = "[" + f.Code + "]"
		}

		if _, err := fmt.Fprintf(w, "%s:%d:%d: %s%s: %s\n", f.File, f.Line, f.Column, f.Severity, codePart, f.Message); err != nil {
			return err
		}
	}

	return nil
}
