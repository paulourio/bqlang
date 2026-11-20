package formatter

import (
	"errors"
	"fmt"
)

// ErrInvalidPrintOption is returned when a PrintOptions field contains an unrecognized enum value.
var ErrInvalidPrintOption = errors.New("invalid print option")

// PrintOptions configures SQL formatting layout, alignment, and casing rules.
type PrintOptions struct {
	// SoftMaxColumns is a soft limit on the maximum number of characters per line.
	SoftMaxColumns int `toml:"soft_max_cols" json:"soft_max_cols"`
	// NewlineBeforeClause sets whether newlines are emitted before clauses like FROM, WHERE, etc.
	NewlineBeforeClause bool `toml:"newline_before_clause" json:"newline_before_clause"`
	// AlignLogicalWithClauses specifies whether AND and OR expressions align with WHERE/ON/HAVING/QUALIFY.
	AlignLogicalWithClauses bool `toml:"align_logical_with_clauses" json:"align_logical_with_clauses"`
	AlignTrailingComments   bool `toml:"align_trailing_comments" json:"align_trailing_comments"`
	// ColumnListTrailingComma controls trailing commas in multiline SQL projection lists:
	// ALWAYS emits one, NEVER omits it, and AUTO preserves a trailing comma only when present in input.
	ColumnListTrailingComma        When            `toml:"column_list_trailing_comma" json:"column_list_trailing_comma"`
	Indentation                    int             `toml:"indentation" json:"indentation"`
	IndentCaseWhen                 bool            `toml:"indent_case_when" json:"indent_case_when"`
	IndentWithClause               bool            `toml:"indent_with_clause" json:"indent_with_clause"`
	IndentWithEntries              bool            `toml:"indent_with_entries" json:"indent_with_entries"`
	MinJoinsToSeparateInBlocks     int             `toml:"min_joins_to_separate_in_blocks" json:"min_joins_to_separate_in_blocks"`
	MaxColumnsForSingleLineSelect  int             `toml:"max_cols_for_single_line_select" json:"max_cols_for_single_line_select"`
	MaxParamsForSingleLineFunction int             `toml:"max_params_for_single_line_function" json:"max_params_for_single_line_function"`
	FunctionCatalog                FunctionCatalog `toml:"function_catalog" json:"function_catalog"`
	BuiltinFunctionNameStyle       PrintCase       `toml:"builtin_function_name_style" json:"builtin_function_name_style"`
	FunctionNameStyle              PrintCase       `toml:"function_name_style" json:"function_name_style"`
	IdentifierStyle                PrintCase       `toml:"identifier_style" json:"identifier_style"`
	KeywordStyle                   PrintCase       `toml:"keyword_style" json:"keyword_style"`
	TypeStyle                      PrintCase       `toml:"type_style" json:"type_style"`
	BoolStyle                      PrintCase       `toml:"bool_style" json:"bool_style"`
	HexStyle                       PrintCase       `toml:"hex_style" json:"hex_style"`
	NumericStyle                   PrintCase       `toml:"numeric_style" json:"numeric_style"`
	NullStyle                      PrintCase       `toml:"null_style" json:"null_style"`
	PseudoColumnStyle              PrintCase       `toml:"pseudo_column_style" json:"pseudo_column_style"`
	TableNameStyle                 PrintCase       `toml:"table_name_style" json:"table_name_style"`
	QueryParameterStyle            PrintCase       `toml:"query_parameter_style" json:"query_parameter_style"`
	SystemVariableStyle            PrintCase       `toml:"system_variable_style" json:"system_variable_style"`
	BytesStyle                     StringStyle     `toml:"bytes_style" json:"bytes_style"`
	StringStyle                    StringStyle     `toml:"string_style" json:"string_style"`
}

// DefaultPrintOptions returns the canonical BigQuery formatting configuration.
func DefaultPrintOptions() *PrintOptions {
	return &PrintOptions{
		SoftMaxColumns:                 80,
		NewlineBeforeClause:            true,
		AlignLogicalWithClauses:        true,
		AlignTrailingComments:          true,
		ColumnListTrailingComma:        Auto,
		Indentation:                    2,
		IndentCaseWhen:                 true,
		IndentWithClause:               true,
		IndentWithEntries:              false,
		MinJoinsToSeparateInBlocks:     2,
		MaxColumnsForSingleLineSelect:  4,
		MaxParamsForSingleLineFunction: 2,
		FunctionCatalog:                BigQueryCatalog,
		BuiltinFunctionNameStyle:       UpperCase,
		FunctionNameStyle:              AsIs,
		IdentifierStyle:                AsIs,
		KeywordStyle:                   UpperCase,
		TypeStyle:                      UpperCase,
		BoolStyle:                      UpperCase,
		HexStyle:                       UpperCase,
		NumericStyle:                   LowerCase,
		NullStyle:                      UpperCase,
		BytesStyle:                     PreferSingleQuote,
		StringStyle:                    PreferSingleQuote,
	}
}

// FunctionCatalog selects a dialect catalog of built-in functions and argument casing rules.
type FunctionCatalog string

// Supported built-in function catalogs.
const (
	EmptyCatalog    FunctionCatalog = ""
	BigQueryCatalog FunctionCatalog = "BIGQUERY"
)

// When controls conditional formatting behavior such as trailing commas.
type When string

// Supported When policies.
const (
	UnspecifiedWhen When = ""
	Never           When = "NEVER"
	Always          When = "ALWAYS"
	Auto            When = "AUTO"
)

// PrintCase specifies identifier, keyword, literal, or function name casing.
type PrintCase string

// Supported PrintCase modes.
const (
	Unspecified PrintCase = ""
	AsIs        PrintCase = "AS_IS"
	LowerCase   PrintCase = "LOWER_CASE"
	UpperCase   PrintCase = "UPPER_CASE"
)

// StringStyle specifies quote normalization for string and bytes literals.
type StringStyle string

// Supported StringStyle modes.
const (
	UnspecifiedStringStyle StringStyle = ""
	// AsIsStringStyle prints the string as is from the input source.
	AsIsStringStyle StringStyle = "AS_IS"
	// PreferSingleQuote prefers single quotes unless the string contains a single quote.
	PreferSingleQuote StringStyle = "PREFER_SINGLE_QUOTE"
	// PreferDoubleQuote prefers double quotes unless the string contains a double quote.
	PreferDoubleQuote StringStyle = "PREFER_DOUBLE_QUOTE"
)

// Validate checks that all enum-typed PrintOptions fields hold recognized
// values and that numeric size/count fields are non-negative.
func (p *PrintOptions) Validate() error {
	if p == nil {
		return nil
	}

	printCases := []struct {
		name string
		val  PrintCase
	}{
		{"builtin_function_name_style", p.BuiltinFunctionNameStyle},
		{"function_name_style", p.FunctionNameStyle},
		{"identifier_style", p.IdentifierStyle},
		{"keyword_style", p.KeywordStyle},
		{"type_style", p.TypeStyle},
		{"bool_style", p.BoolStyle},
		{"hex_style", p.HexStyle},
		{"numeric_style", p.NumericStyle},
		{"null_style", p.NullStyle},
		{"pseudo_column_style", p.PseudoColumnStyle},
		{"table_name_style", p.TableNameStyle},
		{"query_parameter_style", p.QueryParameterStyle},
		{"system_variable_style", p.SystemVariableStyle},
	}

	for _, f := range printCases {
		if !isValidPrintCase(f.val) {
			return fmt.Errorf("Validate %s=%q: %w", f.name, f.val, ErrInvalidPrintOption)
		}
	}

	if !isValidStringStyle(p.BytesStyle) {
		return fmt.Errorf("Validate bytes_style=%q: %w", p.BytesStyle, ErrInvalidPrintOption)
	}

	if !isValidStringStyle(p.StringStyle) {
		return fmt.Errorf("Validate string_style=%q: %w", p.StringStyle, ErrInvalidPrintOption)
	}

	if !isValidWhen(p.ColumnListTrailingComma) {
		return fmt.Errorf("Validate column_list_trailing_comma=%q: %w",
			p.ColumnListTrailingComma, ErrInvalidPrintOption)
	}

	if !isValidCatalog(p.FunctionCatalog) {
		return fmt.Errorf("Validate function_catalog=%q: %w",
			p.FunctionCatalog, ErrInvalidPrintOption)
	}

	return p.validateCounts()
}

func (p *PrintOptions) validateCounts() error {
	intFields := []struct {
		name string
		val  int
	}{
		{"soft_max_cols", p.SoftMaxColumns},
		{"indentation", p.Indentation},
		{"min_joins_to_separate_in_blocks", p.MinJoinsToSeparateInBlocks},
		{"max_cols_for_single_line_select", p.MaxColumnsForSingleLineSelect},
		{"max_params_for_single_line_function", p.MaxParamsForSingleLineFunction},
	}

	for _, f := range intFields {
		if f.val < 0 {
			return fmt.Errorf("Validate %s=%d: %w", f.name, f.val, ErrInvalidPrintOption)
		}
	}

	return nil
}

func isValidPrintCase(c PrintCase) bool {
	switch c {
	case Unspecified, AsIs, LowerCase, UpperCase:
		return true
	default:
		return false
	}
}

func isValidStringStyle(s StringStyle) bool {
	switch s {
	case UnspecifiedStringStyle, AsIsStringStyle, PreferDoubleQuote, PreferSingleQuote:
		return true
	default:
		return false
	}
}

func isValidWhen(w When) bool {
	switch w {
	case UnspecifiedWhen, Never, Always, Auto:
		return true
	default:
		return false
	}
}

func isValidCatalog(c FunctionCatalog) bool {
	switch c {
	case EmptyCatalog, BigQueryCatalog:
		return true
	default:
		return false
	}
}
