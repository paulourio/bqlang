package formatter

import (
	"errors"
	"fmt"
	"strings"

	"github.com/goccy/go-zetasql/ast"
)

type PrinterError struct {
	Msg   string
	Err   error
	Node  ast.Node
	Input *string
}

var (
	ErrInvalidBytesLiteral  = errors.New("invalid bytes literal")
	ErrInvalidStringLiteral = errors.New("invalid string literal")
	ErrInvalidStringStyle   = errors.New("invalid string style")
)

func (e *PrinterError) Error() string {
	parts := []string{"PrinterError"}

	if e.Node != nil {
		parts = append(parts, fmt.Sprintf("at %s (%s)",
			e.Node.LocationString(), e.Node.Kind().String()))
	}

	if e.Msg != "" {
		parts = append(parts, e.Msg)
	}

	if e.Err != nil {
		parts = append(parts, e.Err.Error())
	}

	if e.Node != nil && e.Input != nil {
		r := e.Node.ParseLocationRange()
		b := r.Start().ByteOffset()
		t := r.End().ByteOffset()
		s := (*e.Input)[b:t]

		parts = append(parts, s)
	}

	return strings.Join(parts, ": ")
}
