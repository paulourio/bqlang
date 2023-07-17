package errors

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/paulourio/bqlang/extensions/token"
)

func FormatError(err error, sql string) string {
	var data string

	lines := strings.Split(sql, "\n")
	desc, line, col := findDescription(err, lines)

	if line <= 0 || line > len(lines)+1 {
		fmt.Fprintln(os.Stderr, "FormatError ERROR! line=", line, " col=", col)
		fmt.Fprintf(os.Stderr, "FormatError ERROR! %s\n", err.Error())
		fmt.Fprintln(os.Stderr, "Input:")
		fmt.Fprintln(os.Stderr, sql)
		fmt.Fprintln(os.Stderr, "~~~")

		line = 1
		col = 1
	} else {
		if line == len(lines)+1 {
			data = ""
		} else {
			data = lines[line-1]
		}
	}

	ind := fmt.Sprintf("%s^", strings.Repeat(" ", col-1))

	return fmt.Sprintf(
		"ERROR: %s [at %d:%d]\n%s\n%s\n",
		desc, line, col, data, ind)
}

func findDescription(err error, lines []string) (desc string, line, col int) {
	var (
		genErr *Error
		synErr *SyntaxError
	)

	if errors.As(err, &genErr) {
		if genErr.Err == nil {
			desc, line, col = tokenError(lines, genErr)
		} else {
			desc, line, col = findDescription(genErr.Err, lines)
		}

		return
	}

	if errors.As(err, &synErr) {
		desc = synErr.Error()
		line, col = computeLineCol(lines, synErr.Loc.Start)
	} else {
		desc = err.Error()
	}

	return
}

func tokenError(lines []string, err *Error) (desc string, line, col int) {
	tokDesc := describeUnexpectedToken(err.ErrorToken)

	if len(err.ExpectedTokens) > 0 {
		expected := ""
		if isExpected("JOIN", err.ExpectedTokens) {
			expected = "keyword JOIN"
		}

		if expected != "" {
			desc = fmt.Sprintf("Expected %s but got %s", expected,
				tokDesc)
		} else {
			desc = fmt.Sprintf("Unexpected %s",
				tokDesc)
		}
	} else {
		desc = fmt.Sprintf("Unexpected %s", tokDesc)
	}

	line = err.ErrorToken.Pos.Line
	col = err.ErrorToken.Pos.Column

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
			desc = fmt.Sprintf("Expected end of input but got %s",
				describeUnexpectedToken(err.ErrorToken))
		}
	}

	desc = "Syntax error: " + desc

	return
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

func computeMissingWhitespaceLocation(
	lines []string, tok *token.Token) (line, col int) {
	// Tokenizer consumes only the first wrong character from input,
	// so the offset should always be at the end.
	return computeLineCol(lines, tok.Offset+len(tok.Lit)-1)
}

func computeLineCol(lines []string, offset int) (line, col int) {
	for line < len(lines) && offset > len(lines[line])-1 {
		offset -= len(lines[line]) + 1
		line++
	}

	col = offset
	col++
	line++

	return
}
