package formatter

import (
	"errors"
	"fmt"
	"strings"

	"github.com/goccy/go-googlesql"
	"github.com/hashicorp/go-multierror"
)

// PrinterError describes an AST formatting failure along with its source node and location.
type PrinterError struct {
	Msg   string
	Err   error
	Node  googlesql.ASTNode
	Input *string
}

type errorQueue struct {
	err error
}

func (q *errorQueue) append(err error) {
	if q == nil || err == nil {
		return
	}

	q.err = multierror.Append(q.err, err)
}

// Sentinel errors returned when formatting malformed string or bytes literals.
var (
	ErrInvalidBytesLiteral  = errors.New("invalid bytes literal")
	ErrInvalidStringLiteral = errors.New("invalid string literal")
	ErrInvalidStringStyle   = errors.New("invalid string style")
)

func (e *PrinterError) Error() string {
	parts := []string{"PrinterError"}

	if nodeDefined(e.Node) {
		locStr, _ := e.Node.GetLocationString()
		kindStr, _ := e.Node.GetNodeKindString()
		parts = append(parts, fmt.Sprintf("at %s (%s)", locStr, kindStr))
	}

	if e.Msg != "" {
		parts = append(parts, e.Msg)
	}

	if e.Err != nil {
		parts = append(parts, e.Err.Error())
	}

	if nodeDefined(e.Node) && e.Input != nil {
		b, end := nodeByteRange(e.Node)
		if b >= 0 && b <= end && end <= len(*e.Input) {
			parts = append(parts, (*e.Input)[b:end])
		}
	}

	return strings.Join(parts, ": ")
}

func (e *PrinterError) Unwrap() error {
	return e.Err
}
