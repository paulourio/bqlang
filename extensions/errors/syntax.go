package errors

import (
	"errors"
	"fmt"

	"github.com/paulourio/bqlang/extensions/ast"
)

var (
	// ErrMalformedParser indicates a bug in the parser logic.
	ErrMalformedParser = errors.New("malformed parser")
)

type SyntaxError struct {
	Loc ast.Loc
	Msg string
}

func NewSyntaxError(loc ast.Loc, msg string) *SyntaxError {
	return &SyntaxError{loc, msg}
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("Syntax error: %s", e.Msg)
}
