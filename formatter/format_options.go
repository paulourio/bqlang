package formatter

import (
	"fmt"

	"github.com/go-playground/validator"
)

type PrintOptions struct {
	// MaxCol is a soft limit of the maximum number of characters to be
	// formatted into a single line. This limit may ignore white spaces
	// at the beginning of each line.
	SoftMaxColumns int `toml:"soft_max_cols"`
	// NewlineBeforeClause sets whether new lines should be generated
	// before clauses at the same level, like FROM, WHERE, etc.
	NewlineBeforeClause bool `toml:"newline_before_clause"`
	// AlignConsecutiveBinaryOperations bool `toml:"align_consecutive_operators"`
	// AlignLogicalWithClauses specifies if AND and OR expressions
	// should be aligned with clauses like WHERE.
	// true:
	//  WHERE a = 1
	//    AND b = 3
	//     OR c = 3
	//
	// false:
	//  WHERE     a = 1
	//        AND b = 2
	//         OR c = 3
	AlignLogicalWithClauses bool `toml:"align_logical_with_clauses"`
	// AllowInlineWithEntries bool `toml:"allow_inline_with_entries"`
	// AllowInlineSelects bool `toml:"allow_inline_selects"`
	// AlignAfterOpenBracket bool `toml:"align_after_open_bracket"`
	// AlignArrayOfValues bool `toml:"align_array_of_values"`
	// AlignConsecutiveVariableDeclarations `toml:"align_consecutive_variable_declarations"`
	// AlwaysBreakBeforeMultilineStrings bool `toml:"always_break_before_multiline_strings"`
	AlignTrailingComments   bool `toml:"align_trailing_comments"`
	ColumnListTrailingComma When `toml:"column_list_trailing_comma"`
	// Indentation sets the minimum amount of indentation when certain
	// expressions need to be split across lines.
	Indentation    int  `toml:"indentation"`
	IndentCaseWhen bool `toml:"indent_case_when"`
	// IndentWithClause enabled indents each with entry.
	// true:
	// WITH
	//   cte_name AS (...)
	// SELECT 1
	//
	// false:
	// WITH
	// cte_name AS (...)
	// SELECT 1
	IndentWithClause bool `toml:"indent_with_clause"`
	// IndentWithEntries enabled indents the query expressions inside
	// each CTE.
	// true:
	// cte_name AS (
	//   SELECT 1
	// )
	//
	// false:
	// cte_name AS (
	// SELECT 1
	// )
	IndentWithEntries bool `toml:"indent_with_entries"`
	// MinJoinsToSeparateInBlocks is the minimum number of consecutive
	// joins in a from clause to format each join as a separate block,
	// that is an empty line before and after each join.
	MinJoinsToSeparateInBlocks    int `toml:"min_joins_to_separate_in_blocks"`
	MaxColumnsForSingleLineSelect int `toml:"max_cols_for_single_line_select"`
	// SpaceInAngles                 bool `toml:"space_in_angles"`
	// SpaceInParentheses bool `toml:"space_in_parentheses"`
	// SpaceInBrackets bool `toml:"space_in_brackets"`
	// FunctionCatalog is an optional function catalog to use when
	// printing function names.  When formatting a function, we match
	// the function name (case-insensitive) to render the proper name,
	// and fallbacks to FunctionNameStyle otherwise.
	FunctionCatalog FunctionCatalog `toml:"function_catalog"`
	// FunctionName sets how to style the name of function calls with
	// unquoted names.
	FunctionNameStyle PrintCase `toml:"function_name_style" validate:"print-case"`
	// IdentifierStyle sets how identifiers, such as column names and
	// aliases should be printed.
	IdentifierStyle PrintCase `toml:"identifier_style" validate:"print-case"`
	// KeywordStyle sets how keywords should be printed.
	KeywordStyle PrintCase `toml:"keyword_style" validate:"print-case"`
	// TypeStyle sets how type names should be printed.
	TypeStyle PrintCase `toml:"type_style" validate:"print-case"`
	BoolStyle PrintCase `toml:"bool_style" validate:"print-case"`
	// HexStyle sets how hexadecimal values should be parsed.
	// When not AsIs, the formatted value will always have prefix "0x"
	// followed by the specified style.
	HexStyle     PrintCase   `toml:"hex_style" validate:"print-case"`
	NumericStyle PrintCase   `toml:"numeric_style" validate:"print-case"`
	NullStyle    PrintCase   `toml:"null_style" validate:"print-case"`
	BytesStyle   StringStyle `toml:"bytes_style" validate:"string-style"`
	// StringStyle sets how single-line strings should be printed.
	StringStyle StringStyle `toml:"string_style" validate:"string-style"`
}

type FunctionCatalog string

const (
	EmptyCatalog    FunctionCatalog = ""
	BigQueryCatalog FunctionCatalog = "BIGQUERY"
)

type When string

const (
	Never  When = "NEVER"
	Always When = "ALWAYS"
	Auto   When = "AUTO"
)

type PrintCase string

const (
	Unspecified PrintCase = ""
	AsIs        PrintCase = "AS_IS"
	LowerCase   PrintCase = "LOWER_CASE"
	UpperCase   PrintCase = "UPPER_CASE"
)

type StringStyle string

const (
	// AsIsStringStyle prints the string as is from the input source.
	AsIsStringStyle StringStyle = "AS_IS"
	// PreferSingleQuote prints prefers single quotes but allows double
	// quotes when the string contains as single quote.
	// It prefers 'ab"bc' but allows "ab'cd".
	PreferSingleQuote StringStyle = "PREFER_SINGLE_QUOTE"
	// PreferDoubleQuote prints prefers double quotes but allows double
	// quotes when the string contains as double quote.
	// It prefers "ab'bc" but allows 'ab"cd'.
	PreferDoubleQuote StringStyle = "PREFER_DOUBLE_QUOTE"
)

func (p *PrintOptions) Validate() error {
	var err error

	validate := validator.New()

	err = validate.RegisterValidation("print-case", ValidatePrintCase)
	if err != nil {
		return fmt.Errorf("Validate: %w", err)
	}

	err = validate.RegisterValidation("string-style", ValidateStringStyle)
	if err != nil {
		return fmt.Errorf("Validate: %w", err)
	}

	err = validate.Struct(p)
	if err != nil {
		return fmt.Errorf("Validate: %w", err)
	}

	return nil
}

// ValidatePrintCase implements validator.Func to validate allowed
// values for string style.
func ValidatePrintCase(fl validator.FieldLevel) bool {
	switch PrintCase(fl.Field().String()) {
	case Unspecified,
		AsIs,
		LowerCase,
		UpperCase:
		return true
	default:
		return false
	}
}

// ValidateStringStyle implements validator.Func to validate allowed
// values for string style.
func ValidateStringStyle(fl validator.FieldLevel) bool {
	switch StringStyle(fl.Field().String()) {
	case AsIsStringStyle,
		PreferDoubleQuote,
		PreferSingleQuote:
		return true
	default:
		return false
	}
}
