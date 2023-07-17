package extensions_test

import (
	"fmt"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/paulourio/bqlang/extensions"
	"github.com/stretchr/testify/assert"
)

func TestComments(t *testing.T) {
	tests := MustReadTest("testdata/extract.toml")
	for i, c := range tests.Cases {
		name := fmt.Sprintf("case_%d", i+1)

		t.Run(name, func(t *testing.T) {
			cmts, err := extensions.ExtractComments(c.Input)

			if assert.NoError(t, err) {
				r := newCaseComments(cmts)

				if !assert.Equal(t, c.Comments, r) {
					// Not equal, but both have the same length, compare
					// them one by one so that the output is legible.
					if assert.Equal(t, len(c.Comments), len(r)) {
						for i := 0; i < len(r); i++ {
							assert.Equal(t, c.Comments[i], r[i])
						}
					}
				}

				erased := extensions.EraseComments(c.Input, cmts)
				assert.Equal(
					t,
					strings.ReplaceAll(c.Erased, " ", "~"),
					strings.ReplaceAll(erased, " ", "~"))
			}
		})
	}
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

// MustReadTest reads the contents of file in path p.
func MustReadTest(p string) *TestDataFile {
	f, err := os.Open(p)
	if err != nil {
		log.Fatal(err)
	}

	var t TestDataFile

	_, err = toml.NewDecoder(f).Decode(&t)
	if err != nil {
		log.Fatal(err)
	}

	return &t
}
