package formatter

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/goccy/go-googlesql"

	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/extensions"
)

// SQLFormatter formats GoogleSQL and BigQuery scripts using configurable parser and print options.
type SQLFormatter struct {
	Logger        *log.Logger
	PrintOptions  *PrintOptions
	ParserOptions *googlesql.ParserOptions
}

// Option configures a SQLFormatter instance.
type Option func(*SQLFormatter)

// NewBigQueryFormatter creates a SQLFormatter configured with the given functional options.
func NewBigQueryFormatter(options ...Option) *SQLFormatter {
	f := &SQLFormatter{}

	for _, apply := range options {
		apply(f)
	}

	return f
}

// WithParserOptions overrides the GoogleSQL ParserOptions used by SQLFormatter.
func WithParserOptions(p *googlesql.ParserOptions) Option {
	return func(f *SQLFormatter) {
		f.ParserOptions = p
	}
}

// WithPrintOptions overrides the PrintOptions used by SQLFormatter.
func WithPrintOptions(p *PrintOptions) Option {
	return func(f *SQLFormatter) {
		f.PrintOptions = p
	}
}

// WithLogger attaches a debug trace logger to SQLFormatter.
func WithLogger(logger *log.Logger) Option {
	return func(f *SQLFormatter) {
		f.Logger = logger
	}
}

// Format parses and formats input according to f's configured options.
func (f *SQLFormatter) Format(input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", nil
	}

	if err := bqlang.Init(); err != nil {
		return input, fmt.Errorf("Format: %w", err)
	}

	opts := f.PrintOptions
	if opts == nil {
		opts = DefaultPrintOptions()
	}

	if f.Logger != nil {
		f.log(strings.Repeat("\n", 4))
		f.log("# BigQuery Format\n\n")
		f.log("## Options")

		if dopts, merr := json.MarshalIndent(opts, "", "  "); merr == nil {
			f.log(string(dopts) + "\n\n")
		}
	}

	comms, elems, extErr := extensions.ExtractAll(input)
	if extErr != nil {
		f.warn("Failed to parse extensions: ", extErr)
	}

	placeholders := newTemplatePlaceholders()
	inputOriginal := input
	input = placeholders.ApplyAll(input, elems, comms)

	parserOpts := f.ParserOptions
	if parserOpts == nil {
		parserOpts = bqlang.DefaultParserOptions()
	}

	parsed, err := googlesql.ParseScript(
		input,
		parserOpts,
		bqlang.DefaultErrorMessageOptions(),
	)
	if err != nil {
		f.errorf("Failed to ParseScript: %v", err)

		return "", fmt.Errorf("Format: ParseScript: %w", err)
	}

	root, err := parsed.Script()
	if err != nil {
		return "", fmt.Errorf("Format: Script: %w", err)
	}

	erasedInput := extensions.EraseComments(input, comms)
	f.logDebugInputs(root, inputOriginal, input, erasedInput, elems)

	tracker := newStartLocationTracker(input, root)
	errs := &errorQueue{}

	var bqCatalog stringMapSet
	if opts.FunctionCatalog == BigQueryCatalog {
		bqCatalog = bigQueryFunctionCatalog
	}

	p := &printer{
		fmt: &formatterState{
			opts:      opts,
			comments:  &commentsQueue{comms},
			bqCatalog: bqCatalog,
			maxLength: opts.SoftMaxColumns,
		},
		input:       input,
		erasedInput: erasedInput,
		tracker:     tracker,
		logger:      f.Logger,
		errs:        errs,
	}

	d := make(printerData, 10)
	p.accept(root, d)
	p.fmt.FlushLine()

	if len(p.fmt.comments.comments) > 0 {
		p.println("")
		p.fmt.flushCommentsUpTo(len(input))
	}

	result := p.unnest()
	if p.fmt.opts.AlignTrailingComments {
		result = alignTrailingComments(result)
	}

	result = strings.ReplaceAll(result, "\v", "") + "\n"
	result = strings.ReplaceAll(result, lineBreakPlaceholder, "\n")
	result = rowsTrimRight(result)
	// Restore multi-line literal newlines AFTER rowsTrimRight so literal bytes
	// and internal trailing spaces are preserved verbatim.
	result = strings.ReplaceAll(result, literalLineBreakPlaceholder, "\n")

	f.log("## Formatted print\n\n")
	f.logf("```\n%s\n```\n\n", result)

	reverted := result
	for _, ph := range placeholders.Placeholders {
		reverted = ph.Revert(reverted)
	}

	if reverted != result {
		f.log("## Result with template elements re-inserted\n\n")
		f.logf("```\n%s\n```\n\n", reverted)
	}

	return reverted, errs.err
}

func (f *SQLFormatter) logDebugInputs(
	root *googlesql.ASTScript,
	inputOriginal string,
	input string,
	erasedInput string,
	elems []*extensions.TemplateElement,
) {
	if f.Logger == nil {
		return
	}

	if ds, derr := root.DebugString(100); derr == nil {
		f.log("## Input AST\n\n")
		f.logf("```\n%s\n```\n", ds)
	}

	f.log("## Input\n\n")
	f.logf("```\n%s\n```\n", inputOriginal)

	f.log("## Preprocessed\n\n")
	f.logf("```\n%s\n```\n\n", input)

	f.log("## Template Elements")

	for i, e := range elems {
		f.logf("Element %d: %#v", i+1, e)
	}

	f.log("## Pre-processed input without comments\n\n")
	f.logf("```\n%s\n```\n\n", erasedInput)
}

func (f *SQLFormatter) log(msg string, keyvals ...any) {
	if f.Logger == nil {
		return
	}

	args := append([]any{msg}, keyvals...)
	f.Logger.Print(args...)
}

func (f *SQLFormatter) logf(format string, args ...any) {
	if f.Logger == nil {
		return
	}

	f.Logger.Printf(format, args...)
}

func (f *SQLFormatter) errorf(format string, args ...any) {
	f.logf("[ERROR] "+format, args...)
}

func (f *SQLFormatter) warn(msg string, keyvals ...any) {
	f.log("[WARN] "+msg, keyvals...)
}

// Format formats script using opts (or DefaultPrintOptions when opts is nil).
func Format(script string, opts *PrintOptions) (string, error) {
	bqf := NewBigQueryFormatter(WithPrintOptions(opts))

	return bqf.Format(script)
}

func rowsTrimRight(s string) string {
	rows := strings.Split(s, "\n")
	r := make([]string, 0, len(rows))

	for _, row := range rows {
		r = append(r, strings.TrimRight(row, " "))
	}

	return strings.Join(r, "\n")
}
