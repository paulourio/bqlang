package formatter_test

import (
	"fmt"
	"os"
	"path"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/goccy/go-googlesql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/formatter"
)

var defaultTestParserOptions = sync.OnceValue(bqlang.DefaultParserOptions)

func TestPrinter(t *testing.T) {
	t.Parallel()

	require.NoError(t, bqlang.Init())

	files, err := os.ReadDir("testdata")
	require.NoError(t, err)

	var nerr atomic.Int32

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		s := MustReadTest(t, path.Join("testdata", file.Name()))

		for i, c := range s.Cases {
			name := fmt.Sprintf("%s:case %d", file.Name(), i)

			t.Run(name, func(t *testing.T) {
				t.Parallel()

				if nerr.Load() > 10 {
					t.Skipf("skipping after %d failures", nerr.Load())
				}

				if !testCase(t, s, c) {
					nerr.Add(1)
				}
			})
		}
	}
}

func testCase(t *testing.T, f *TestDataFile, c *Case) bool {
	t.Helper()

	parserOpts := getParserOpts(f)
	input := ExtractScriptInfo(c.Input, parserOpts)

	bqfmt := formatter.NewBigQueryFormatter(
		formatter.WithParserOptions(parserOpts),
		formatter.WithPrintOptions(f.Setup.PrintOptions),
	)

	fmtScript, ferr := bqfmt.Format(c.Input)
	formatted := ExtractScriptInfo(fmtScript, parserOpts)

	fmtScriptAgain, ferr2 := bqfmt.Format(fmtScript)
	formattedAgain := ExtractScriptInfo(fmtScriptAgain, parserOpts)

	cr := &CaseResult{
		Case:           c,
		Input:          input,
		Formatted:      formatted,
		FormattedAgain: formattedAgain,
	}

	msg := cr.String()

	if assert.NoError(t, ferr, msg) && assert.NoError(t, ferr2, "[SECOND PASS] "+msg) {
		if assert.Equal(t, c.Formatted, formatted.Script, msg) {
			if assert.Equal(
				t,
				cr.Formatted.debugStringClean,
				cr.FormattedAgain.debugStringClean,
				"Debug string must match before and after formatting.\n\n"+msg,
			) {
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

func getParserOpts(f *TestDataFile) *googlesql.ParserOptions {
	if f.Setup == nil || f.Setup.LanguageOptions == nil {
		return defaultTestParserOptions()
	}

	parser := bqlang.DefaultParserOptions()

	lang, err := parser.LanguageOptions()
	if err != nil {
		return parser
	}

	if f.Setup.LanguageOptions.DisableQualifyAsKeyword {
		_ = lang.EnableReservableKeyword("QUALIFY", false)
	}

	_ = parser.SetLanguageOptions(lang)

	return parser
}
