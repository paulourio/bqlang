package formatter

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	// "github.com/charmbracelet/log"

	"github.com/goccy/go-zetasql"
	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/extensions"
)

type SQLFormatter struct {
	Logger        *log.Logger
	PrintOptions  *PrintOptions
	ParserOptions *zetasql.ParserOptions
}

func NewBigQueryFormatter(options ...func(*SQLFormatter)) *SQLFormatter {
	f := &SQLFormatter{}

	for _, apply := range options {
		apply(f)
	}

	return f
}

func WithParserOptions(p *zetasql.ParserOptions) func(*SQLFormatter) {
	return func(f *SQLFormatter) {
		f.ParserOptions = p
	}
}

func WithPrintOptions(p *PrintOptions) func(*SQLFormatter) {
	return func(f *SQLFormatter) {
		f.PrintOptions = p
	}
}

func WithLogger(logger *log.Logger) func(*SQLFormatter) {
	return func(f *SQLFormatter) {
		f.Logger = logger
	}
}

func (f *SQLFormatter) Format(input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", nil
	}

	opts := f.PrintOptions
	if opts == nil {
		opts = &PrintOptions{
			SoftMaxColumns:          80,
			NewlineBeforeClause:     true,
			AlignLogicalWithClauses: true,
			Indentation:             2,
			FunctionNameStyle:       UpperCase,
			IdentifierStyle:         UpperCase,
			KeywordStyle:            UpperCase,
			TypeStyle:               UpperCase,
			StringStyle:             PreferSingleQuote,
		}
	}

	f.debug(strings.Repeat("\n", 4))
	f.debug("# BigQuery Format\n\n")

	f.debug("## Options")
	dopts, _ := json.MarshalIndent(opts, "", "  ")
	f.debug(string(dopts) + "\n\n")

	comms, cerr := extensions.ExtractComments(input)
	if cerr != nil {
		f.warn("Failed to parse comments: ", cerr)
	}

	// We need to handle template because they are not parsable.
	elems, terr := extensions.ExtractTemplateElements(input)
	if terr != nil {
		f.warn("Cannot parse templated query: ", terr)
	}

	placeholders := NewTemplatePlaceholders(input)

	inputOriginal := input

	for _, e := range elems {
		if ph := placeholders.New(e); ph != nil {
			input = ph.Apply(input)
		}
	}

	var parserOpts *zetasql.ParserOptions

	if f.ParserOptions == nil {
		parserOpts = bqlang.DefaultParserOptions()
	} else {
		parserOpts = f.ParserOptions
	}

	root, err := zetasql.ParseScript(
		input,
		parserOpts,
		zetasql.ErrorMessageMultiLineWithCaret)
	if err != nil {
		f.error("Failed to ParseScript: %v", err)
		return "", fmt.Errorf("Format: ParseScript: %w", err)
	}

	f.debug("## Input AST\n\n")
	f.debugf("```\n%s\n```\n", root.DebugString(100))

	f.debug("## Input\n\n")
	f.debugf("```\n%s\n```\n", inputOriginal)

	f.log("## Preprocessed\n\n")
	f.logf("```\n%s\n```\n\n", input)

	f.debug("## Template Elements")
	for i, e := range elems {
		f.debugf("Element %d: %#v", i+1, e)
	}

	erasedInput := extensions.EraseComments(input, comms)
	f.log("## Pre-processed input without comments\n\n")
	f.logf("```\n%s\n```\n\n", erasedInput)

	// The start location tracker is used to flush comments between
	// the end of a node and the start of the "in-order successor"
	// node of a start location tree.
	tracker := NewStartLocationTracker(input, root)

	p := &Printer{
		fmt: &Formatter{
			opts:      opts,
			comments:  &CommentsQueue{comms},
			maxLength: opts.SoftMaxColumns,
		},
		input:       input,
		erasedInput: erasedInput,
		tracker:     tracker,
		err:         nil,
	}

	d := make(Data, 10)

	p.accept(root, d)
	p.fmt.FlushLine()

	// Flush any remaining extensions.
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

	f.log("## Formatted print\n\n")
	f.logf("```\n%s\n```\n\n", result)

	reverted := result
	for _, p := range placeholders.Placeholders {
		reverted = p.Revert(reverted)
	}

	if reverted != result {
		f.log("## Result with template elements re-inserted\n\n")
		f.logf("```\n%s\n```\n\n", result)
	}

	return reverted, p.err
}

func (f *SQLFormatter) log(msg string, keyvals ...any) {
	args := append([]any{msg}, keyvals...)

	if f.Logger != nil {
		f.Logger.Print(args...)
	} else {
		log.Print(args...)
	}
}

func (f *SQLFormatter) logf(format string, args ...any) {
	if f.Logger != nil {
		f.Logger.Printf(format, args...)
	} else {
		log.Printf(format, args...)
	}
}

func (f *SQLFormatter) debug(msg string, keyvals ...any) {
	f.log(msg, keyvals...)
}

func (f *SQLFormatter) debugf(format string, args ...any) {
	f.logf(format, args...)
}

func (f *SQLFormatter) error(msg string, keyvals ...any) {
	f.log("[ERROR] "+msg, keyvals...)
}

func (f *SQLFormatter) errorf(format string, args ...any) {
	f.log("[ERROR] "+format, args...)
}

func (f *SQLFormatter) warn(msg string, keyvals ...any) {
	f.log("[WARN] "+msg, keyvals...)
}

func (f *SQLFormatter) warnf(format string, args ...any) {
	f.log("[WARN] "+format, args...)
}

func Format(script string, opts *PrintOptions) (string, error) {
	bqf := NewBigQueryFormatter(WithPrintOptions(opts))
	return bqf.Format(script)
}

// rowsTrimRight returns string where each row has been right-trimmed.
// Note that this procedure applies to all rows in the string,
// disregarding its contents, so in case of a SQL code we will change
// the contents of strings as well.
func rowsTrimRight(s string) string {
	rows := strings.Split(s, "\n")
	r := make([]string, 0, len(rows))

	for _, row := range rows {
		r = append(r, strings.TrimRight(row, " "))
	}

	return strings.Join(r, "\n")
}
