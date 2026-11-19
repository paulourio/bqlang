package errors

import (
	"errors"

	"github.com/paulourio/bqlang/extensions/ast"
)

// ErrMalformedParser indicates a bug in the parser logic.
var ErrMalformedParser = errors.New("malformed parser")

// SyntaxError represents a syntax error encountered while scanning or parsing extensions.
type SyntaxError struct {
	Loc ast.Loc
	Msg string
}

// NewSyntaxError constructs a SyntaxError at loc with message msg.
func NewSyntaxError(loc ast.Loc, msg string) *SyntaxError {
	return &SyntaxError{loc, msg}
}

// Error formats the syntax error message.
func (e *SyntaxError) Error() string {
	return "Syntax error: " + e.Msg
}
