package errors_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/paulourio/bqlang/extensions/ast"
	extErrors "github.com/paulourio/bqlang/extensions/errors"
)

func TestFormatErrorLineEndOffset(t *testing.T) {
	t.Parallel()

	sql := "SELECT 1\nSELECT 2"

	// Offset 8 is the '\n' at the end of line 1 ("SELECT 1" has len 8).
	atLineEnd := extErrors.FormatError(&extErrors.SyntaxError{
		Loc: ast.Loc{Start: 8, End: 9},
		Msg: "unexpected newline",
	}, sql)
	assert.Contains(t, atLineEnd, "[at 1:9]")
	assert.Contains(t, atLineEnd, "SELECT 1\n        ^")

	// Offset 9 is the first byte of line 2 ("SELECT 2").
	nextLineStart := extErrors.FormatError(&extErrors.SyntaxError{
		Loc: ast.Loc{Start: 9, End: 10},
		Msg: "unexpected token",
	}, sql)
	assert.Contains(t, nextLineStart, "[at 2:1]")
	assert.Contains(t, nextLineStart, "SELECT 2\n^")
}
