package parser_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/paulourio/bqlang/extensions/ast"
	"github.com/paulourio/bqlang/extensions/errors"
	"github.com/paulourio/bqlang/extensions/lexer"
	"github.com/paulourio/bqlang/extensions/parser"
	"github.com/stretchr/testify/assert"
)

func TestParser(t *testing.T) {
	tests := MustReadTest("testdata/comments.toml")

	for i, tcase := range tests.Cases {
		var name string

		if tcase.Description == "" {
			name = fmt.Sprintf("case %d", i)
		} else {
			name = fmt.Sprintf("case %d: %s", i, tcase.Description)
		}

		t.Run(name, func(t *testing.T) {
			var b strings.Builder

			c := tcase //nolint:loopclosure

			b.Grow(len(c.Input) * 2)
			b.WriteString(
				fmt.Sprintf("Input (%d bytes):\n%s\n\n", len(c.Input), c.Input))
			b.WriteString(
				fmt.Sprintf("Expected Dump (%d bytes):\n%s\n\n", len(c.Dump), c.Dump))

			l := lexer.NewLexer([]byte(c.Input))
			p := parser.NewParser()

			res, err := p.Parse(l)
			if err != nil {
				msg := errors.FormatError(err, c.Input)

				b.WriteString(fmt.Sprintf("Parse error:\n%s\n\n", msg))
				assert.NoError(t, err, b.String())

				return
			}

			var dump string

			if nh, ok := res.(ast.NodeHandler); ok {
				dump = nh.DebugString("")
			} else {
				dump = fmt.Sprintf("%#v", res)
			}

			b.WriteString(
				fmt.Sprintf("Result Dump (%d bytes):\n%s\n\n", len(dump), dump))

			assert.Equal(t, c.Dump, dump, b.String())
		})
	}
}
