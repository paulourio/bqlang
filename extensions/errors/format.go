package errors

import (
	"errors"
	"fmt"
	"strings"

	"github.com/paulourio/bqlang/extensions/token"
)

// FormatError formats a scanner or parser error with 1-based line/column coordinates and caret context.
func FormatError(err error, sql string) string {
	var data string

	lines := strings.Split(sql, "\n")
	desc, line, col := findDescription(err, lines)

	if line <= 0 || line > len(lines)+1 {
		line = 1
		col = 1
	} else if line <= len(lines) {
		data = lines[line-1]
	}

	if col < 1 {
		col = 1
	}

	ind := strings.Repeat(" ", col-1) + "^"

	return fmt.Sprintf(
		"ERROR: %s [at %d:%d]\n%s\n%s\n",
		desc, line, col, data, ind,
	)
}

func findDescription(err error, lines []string) (desc string, line, col int) {
	var (
		genErr *Error
		synErr *SyntaxError
	)

	if errors.As(err, &genErr) {
		if genErr.Err == nil {
			desc, line, col = tokenError(genErr)
		} else {
			desc, line, col = findDescription(genErr.Err, lines)
		}

		return desc, line, col
	}

	if errors.As(err, &synErr) {
		desc = synErr.Error()
		line, col = computeLineCol(lines, synErr.Loc.Start)
	} else {
		desc = err.Error()
		line = 1
		col = 1
	}

	return desc, line, col
}

func tokenError(err *Error) (desc string, line, col int) {
	tokDesc := describeUnexpectedToken(err.ErrorToken)

	if len(err.ExpectedTokens) > 0 && isExpected("JOIN", err.ExpectedTokens) {
		desc = "Expected keyword JOIN but got " + tokDesc
	} else {
		desc = "Unexpected " + tokDesc
	}

	line = err.ErrorToken.Line
	col = err.ErrorToken.Column

	switch err.ErrorToken.Type {
	case token.INVALID:
		desc = "Unexpected unknown/invalid token"
	case token.EOF, token.TokMap.Type("$"):
		line++
		col = 1
		desc = "Unexpected end of statement"
	case token.TokMap.Type("unterminated_escaped_identifier"):
		desc = "Unclosed identifier literal"
	default:
		if isExpected("$", err.ExpectedTokens) {
			desc = "Expected end of input but got " + describeUnexpectedToken(err.ErrorToken)
		}
	}

	desc = "Syntax error: " + desc

	return desc, line, col
}

func describeUnexpectedToken(tok *token.Token) string {
	name := strings.ReplaceAll(token.TokMap.Id(tok.Type), "_", " ")
	value := string(tok.Lit)
	nameEqualsValue := name == value

	if !strings.HasPrefix(value, `'`) &&
		!strings.HasPrefix(value, `"`) {
		value = fmt.Sprintf("%q", value)
	}

	if nameEqualsValue {
		return value
	}

	return fmt.Sprintf("%s %s", name, value)
}

func isExpected(elem string, expected []string) bool {
	elem = strings.ToUpper(elem)
	for _, sym := range expected {
		if strings.ToUpper(sym) == elem {
			return true
		}
	}

	return false
}

func computeLineCol(lines []string, offset int) (line, col int) {
	for line < len(lines) && offset > len(lines[line])-1 {
		offset -= len(lines[line]) + 1
		line++
	}

	col = offset + 1
	line++

	return line, col
}
