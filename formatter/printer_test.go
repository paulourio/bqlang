package formatter_test

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path"
	"testing"

	zetasql "github.com/goccy/go-zetasql"
	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/formatter"
	"github.com/stretchr/testify/assert"
)

func TestPrinter(t *testing.T) {
	t.Parallel()

	files, err := os.ReadDir("testdata")
	assert.NoError(t, err)

	nerr := 0

	for _, file := range files {
		s := MustReadTest(path.Join("testdata", file.Name()))

		for i, c := range s.Cases {
			name := fmt.Sprintf("%s:case %d", file.Name(), i)
			t.Run(name, func(t *testing.T) {
				if !testCase(t, s, c) {
					nerr++
				}
			})

			if nerr > 2 {
				t.Fatal("stopping due too many errors")
			}
		}
	}
}

func testCase(t *testing.T, f *TestDataFile, c *Case) bool {
	input := ExtractScriptInfo(c.Input)

	var logBuf bytes.Buffer

	logBuf.Grow(len(c.Input) * 20)

	logger := log.New(&logBuf, "", 0)

	bqfmt := formatter.NewBigQueryFormatter(
		formatter.WithLogger(logger),
		formatter.WithParserOptions(getParserOpts(f)),
		formatter.WithPrintOptions(f.Setup.PrintOptions),
	)

	fmtScript, ferr := bqfmt.Format(c.Input)
	formatted := ExtractScriptInfo(fmtScript)

	fmtScriptAgain, ferr2 := bqfmt.Format(fmtScript)
	formattedAgain := ExtractScriptInfo(fmtScriptAgain)

	cr := &CaseResult{
		Case:           c,
		Input:          input,
		Formatted:      formatted,
		FormattedAgain: formattedAgain,
	}

	msg := cr.String()

	if assert.NoError(t, ferr, msg) && assert.NoError(t, ferr2, "[SECOND PASS] "+msg) {
		// No error, continue to check formatted result.
		if assert.Equal(t, c.Formatted, formatted.Script, msg) {
			// Formatted result is as expected, now check the AST
			// remains the same.
			if assert.Equal(
				t,
				cr.Formatted.debugStringClean,
				cr.FormattedAgain.debugStringClean,
				"Debug string must match before and after formatting.\n\n"+msg,
			) {
				// Formatting is validated, now we check reformatting
				// the formatted code will cause no changes.
				if assert.Equal(
					t,
					cr.Formatted.Script,
					cr.FormattedAgain.Script,
					"Format must be idempotent.\n\n"+msg,
				) {
					return true
				}
			}
		}
	}

	return false
}

func getParserOpts(f *TestDataFile) *zetasql.ParserOptions {
	parser := bqlang.DefaultParserOptions()

	if f.Setup.LanguageOptions == nil {
		return parser
	}

	lang := parser.LanguageOptions()
	opts := f.Setup.LanguageOptions

	if opts.DisableQualifyAsKeyword {
		lang.EnableReservableKeyword("QUALIFY", false)
	}

	parser.SetLanguageOptions(lang)

	return parser
}
