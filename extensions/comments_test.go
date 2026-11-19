package extensions_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/extensions"
)

func TestComments(t *testing.T) {
	t.Parallel()

	tests := MustReadTest(t, "testdata/extract.toml")

	for i, tc := range tests.Cases {
		name := fmt.Sprintf("case_%d", i+1)

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cmts, err := extensions.ExtractComments(tc.Input)
			require.NoError(t, err)

			r := newCaseComments(cmts)

			if !assert.Equal(t, tc.Comments, r) {
				if assert.Len(t, r, len(tc.Comments)) {
					for j := range r {
						assert.Equal(t, tc.Comments[j], r[j])
					}
				}
			}

			erased := extensions.EraseComments(tc.Input, cmts)
			assert.Equal(
				t,
				strings.ReplaceAll(tc.Erased, " ", "~"),
				strings.ReplaceAll(erased, " ", "~"),
			)
		})
	}
}

func TestCommentEdgeCasesAndExtractAll(t *testing.T) {
	t.Parallel()

	_, err := extensions.NewComment(0, 1, "/", "/")
	require.ErrorIs(t, err, extensions.ErrInvalidComment)

	_, err = extensions.NewComment(0, 0, "", "")
	require.ErrorIs(t, err, extensions.ErrInvalidComment)

	input := strings.Join([]string{
		"{# header #}",
		"{% if a %}",
		"SELECT 1 % 2, a != 1, x:y, $foo, a -> b",
		"{% elif b %}",
		"SELECT 2 |> WHERE true",
		"{% else %}",
		"SELECT 3 FROM t",
		"{% endif %}",
	}, "\n")

	comms, elems, err := extensions.ExtractAll(input)
	require.NoError(t, err)
	require.Len(t, comms, 1)
	assert.Equal(t, extensions.Jinja2Comment, comms[0].Kind)
	assert.Equal(t, "JINJA2_COMMENT", comms[0].Kind.String())
	require.Len(t, elems, 2)
}

type TestDataFile struct {
	Cases []*Case `toml:"cases"`
}

type Case struct {
	Input    string         `toml:"input"`
	Erased   string         `toml:"erased"`
	Comments []*CaseComment `toml:"comments"`
}

type CaseComment struct {
	Image       string `toml:"image"`
	Kind        string `toml:"kind"`
	Multiline   bool   `toml:"multiline"`
	Oneline     bool   `toml:"oneline"`
	AtLineBegin bool   `toml:"at_line_begin"`
	AtLineEnd   bool   `toml:"at_line_end"`
}

func newCaseComments(cs []*extensions.Comment) []*CaseComment {
	r := make([]*CaseComment, 0, len(cs))

	for _, c := range cs {
		r = append(r, newCaseComment(c))
	}

	return r
}

func newCaseComment(c *extensions.Comment) *CaseComment {
	return &CaseComment{
		Image:       c.Image,
		Kind:        c.Kind.String(),
		Multiline:   c.IsMultiline(),
		Oneline:     c.IsOneline(),
		AtLineBegin: c.AtLineBegin(),
		AtLineEnd:   c.AtLineEnd(),
	}
}

func MustReadTest(tb testing.TB, p string) *TestDataFile {
	tb.Helper()

	f, err := os.Open(p)
	require.NoError(tb, err)

	defer func() {
		_ = f.Close()
	}()

	var td TestDataFile

	_, err = toml.NewDecoder(f).Decode(&td)
	require.NoError(tb, err)

	return &td
}
