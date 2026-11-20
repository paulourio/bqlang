package extensions_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/extensions"
)

var adversarialExtensionSeeds = []string{
	"",
	" ",
	"\n\r\n\t",
	"\x00",
	"\xff\xfe\xfd",
	"'",
	`"`,
	"`",
	"'''",
	`"""`,
	`'unclosed\'`,
	`"unclosed\"`,
	"r'\\'",
	"rb\"\\\"",
	"/*",
	"/*/",
	"/* unclosed\nline2",
	"--",
	"-- eof comment",
	"//",
	"// eof slash",
	"#",
	"# eof pound",
	"{{",
	"}}",
	"{{ unclosed",
	"{{ }}",
	"{{- x -}}",
	"{%",
	"%}",
	"{% if x %}",
	"{% if x %} SELECT 1 {% elif y %} SELECT 2 {% else %} SELECT 3 {% endif %}",
	"{% for a in b %} {{ a }} {% endfor %}",
	"{% set x = 1 -%}",
	"{% set x = ",
	"{#",
	"#}",
	"{# unclosed comment",
	"{# multi\nline\ncomment #}",
	"SELECT '{{' /* comment */ -- tail",
	"SELECT /* {# */ '{{ a }}' FROM `tbl--1`",
	"SELECT 1 % 2, a != b, x:y, $foo, a -> b |> WHERE TRUE",
}

// FuzzExtractAll fuzzes extensions.ExtractAll, ExtractComments,
// ExtractTemplateElements, and EraseComments with arbitrary and malformed
// SQL, comment, and Jinja2 template byte sequences.
func FuzzExtractAll(f *testing.F) {
	tests := MustReadTest(f, "testdata/extract.toml")
	for _, tc := range tests.Cases {
		f.Add(tc.Input)
	}

	for _, seed := range adversarialExtensionSeeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		comms, elems, err := extensions.ExtractAll(input)

		for _, c := range comms {
			require.NotNil(t, c)
			require.GreaterOrEqual(t, c.Start, 0)
			require.LessOrEqual(t, c.Start, c.End)
			require.LessOrEqual(t, c.End, len(input))
			_ = c.Kind.String()
		}

		for _, e := range elems {
			require.NotNil(t, e)
			require.GreaterOrEqual(t, e.Start, 0)
			require.LessOrEqual(t, e.Start, e.End)
			require.LessOrEqual(t, e.End, len(input))
			require.Equal(t, input[e.Start:e.End], e.Image)
		}

		erased := extensions.EraseComments(input, comms)
		require.Len(t, erased, len(input))
		require.Equal(t, strings.Count(input, "\n"), strings.Count(erased, "\n"))

		commsOnly, cerr := extensions.ExtractComments(input)
		elemsOnly, eerr := extensions.ExtractTemplateElements(input)

		if err != nil {
			require.Error(t, cerr)
			require.Error(t, eerr)

			return
		}

		require.NoError(t, cerr)
		require.NoError(t, eerr)
		require.Len(t, commsOnly, len(comms))
		require.Len(t, elemsOnly, len(elems))
	})
}
