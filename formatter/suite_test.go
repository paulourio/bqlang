package formatter_test

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/goccy/go-zetasql"
	"github.com/goccy/go-zetasql/ast"
	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/formatter"
)

type WriteStringer interface {
	WriteString(str string) (int, error)
}

// TestDataFile contains a single unit test data.
type TestDataFile struct {
	Setup *Setup  `toml:"setup"`
	Cases []*Case `toml:"cases"`
}

// Setup defines the default configuration for a test case.
type Setup struct {
	PrintOptions *formatter.PrintOptions `toml:"print_options"`
}

// Case is a single test case specification.
type Case struct {
	Description string `toml:"description,omitempty"`
	Input       string `toml:"input"`
	Formatted   string `toml:"formatted"`
}

// CaseResult has the initial input, the formatted script, and the
// second formatted pass.  The FormattedAgain is used to guarantee
// the formatting algorithm is idempotent.
type CaseResult struct {
	Case           *Case
	Input          *ScriptInfo
	Formatted      *ScriptInfo
	FormattedAgain *ScriptInfo
}

// ScriptInfo has information for a single script.
type ScriptInfo struct {
	Script           string
	AST              ast.ScriptNode
	Err              error
	debugString      string
	debugStringClean string
}

func ExtractScriptInfo(script string) *ScriptInfo {
	var ds string

	s, err := parseScript(script)
	if err == nil {
		ds = s.DebugString(100)
	}

	return &ScriptInfo{
		Script:           script,
		AST:              s,
		Err:              err,
		debugString:      ds,
		debugStringClean: cleanupDebugString(ds),
	}
}

func (c *Case) String() string {
	var b strings.Builder

	return b.String()
}

func (c *CaseResult) String() string {
	b := &strings.Builder{}

	b.Grow(len(c.Input.Script) * 50)

	if c.Case.Description != "" {
		b.WriteString(fmt.Sprintf("Test Case: %s\n\n", c.Case.Description))
	}

	writeBlock(b, "Input AST", c.Input.debugString)
	writeBlock(b, "Formatted AST", c.Formatted.debugString)
	writeBlock(b, "Input", c.Case.Input)
	writeBlock(b, "Expected Formatted", c.Case.Formatted)
	writeBlock(b, "Result Formatted", c.Formatted.Script)

	return b.String()
}

func writeBlock(w WriteStringer, title string, content string) {
	w.WriteString(fmt.Sprintf("%s (%d bytes):\n%s\n\n", title, len(content), content))
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

	if t.Setup != nil && t.Setup.PrintOptions != nil {
		verr := t.Setup.PrintOptions.Validate()
		if verr != nil {
			log.Fatal(verr)
		}
	}

	return &t
}

func MaybeFormattedAST(script string) string {
	z, err := zetasql.ParseScript(
		script, bqlang.DefaultParserOptions(), zetasql.ErrorMessageOneLine)
	if err != nil {
		return ""
	}

	return z.DebugString(1000)
}

func parseScript(script string) (ast.ScriptNode, error) {
	return zetasql.ParseScript(
		script,
		bqlang.DefaultParserOptions(),
		zetasql.ErrorMessageMultiLineWithCaret,
	)
}

func cleanupDebugString(ds string) string {
	return removeValues(removeParseLocation(ds))
}

func removeParseLocation(ds string) string {
	return parseLocPat.ReplaceAllLiteralString(ds, "")
}

func removeValues(ds string) string {
	return valuesPat.ReplaceAllLiteralString(ds, "")
}

var parseLocPat = regexp.MustCompile(`\[\d+\-\d+\]`)
var valuesPat = regexp.MustCompile(`\([^\)]+\)`)
