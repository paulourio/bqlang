package parser_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/paulourio/bqlang/extensions/ast"
	"github.com/paulourio/bqlang/extensions/errors"
	"github.com/paulourio/bqlang/extensions/lexer"
	"github.com/paulourio/bqlang/extensions/parser"
)

func TestParser(t *testing.T) {
	t.Parallel()

	tests := MustReadTest(t, "testdata/comments.toml")

	for i, tcase := range tests.Cases {
		var name string

		if tcase.Description == "" {
			name = fmt.Sprintf("case %d", i)
		} else {
			name = fmt.Sprintf("case %d: %s", i, tcase.Description)
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var b strings.Builder

			b.Grow(len(tcase.Input) * 2)
			fmt.Fprintf(&b, "Input (%d bytes):\n%s\n\n", len(tcase.Input), tcase.Input)
			fmt.Fprintf(&b, "Expected Dump (%d bytes):\n%s\n\n", len(tcase.Dump), tcase.Dump)

			l := lexer.NewLexer([]byte(tcase.Input))
			p := parser.NewParser()

			res, err := p.Parse(l)
			if err != nil {
				msg := errors.FormatError(err, tcase.Input)

				fmt.Fprintf(&b, "Parse error:\n%s\n\n", msg)
				assert.NoError(t, err, b.String())

				return
			}

			var dump string

			if nh, ok := res.(ast.NodeHandler); ok {
				dump = nh.DebugString("")
			} else {
				dump = fmt.Sprintf("%#v", res)
			}

			fmt.Fprintf(&b, "Result Dump (%d bytes):\n%s\n\n", len(dump), dump)

			assert.Equal(t, tcase.Dump, dump, b.String())
		})
	}
}
