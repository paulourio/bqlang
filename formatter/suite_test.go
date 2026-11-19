package formatter_test

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/goccy/go-googlesql"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/formatter"
)

type WriteStringer interface {
	WriteString(str string) (int, error)
}

type TestDataFile struct {
	Setup *Setup  `toml:"setup"`
	Cases []*Case `toml:"cases"`
}

type Setup struct {
	LanguageOptions *LanguageOptions        `toml:"language_options"`
	PrintOptions    *formatter.PrintOptions `toml:"print_options"`
}

type LanguageOptions struct {
	DisableQualifyAsKeyword bool `toml:"disable_qualify_as_keyword"`
}

type Case struct {
	Description string `toml:"description,omitempty"`
	Input       string `toml:"input"`
	Formatted   string `toml:"formatted"`
}

type CaseResult struct {
	Case           *Case
	Input          *ScriptInfo
	Formatted      *ScriptInfo
	FormattedAgain *ScriptInfo
}

type ScriptInfo struct {
	Script           string
	AST              *googlesql.ASTScript
	Err              error
	debugString      string
	debugStringClean string
}

func ExtractScriptInfo(script string, parserOpts *googlesql.ParserOptions) *ScriptInfo {
	var ds string

	s, err := parseScript(script, parserOpts)
	if err == nil && s != nil {
		ds, _ = s.DebugString(100)
	}

	return &ScriptInfo{
		Script:           script,
		AST:              s,
		Err:              err,
		debugString:      ds,
		debugStringClean: cleanupDebugString(ds),
	}
}

func (c *CaseResult) String() string {
	b := &strings.Builder{}
	b.Grow(len(c.Input.Script) * 20)

	if c.Case.Description != "" {
		fmt.Fprintf(b, "Test Case: %s\n\n", c.Case.Description)
	}

	writeBlock(b, "Input AST", c.Input.debugString)
	writeBlock(b, "Formatted AST", c.Formatted.debugString)
	writeBlock(b, "Input", c.Case.Input)
	writeBlock(b, "Expected Formatted", c.Case.Formatted)
	writeBlock(b, "Result Formatted", c.Formatted.Script)

	return b.String()
}

func writeBlock(w WriteStringer, title, content string) {
	_, _ = w.WriteString(fmt.Sprintf("%s (%d bytes):\n%s\n\n", title, len(content), content))
}

func MustReadTest(tb testing.TB, p string) *TestDataFile {
	tb.Helper()

	f, err := os.Open(p)
	require.NoError(tb, err)

	defer func() {
		_ = f.Close()
	}()

	var td TestDataFile

	meta, err := toml.NewDecoder(f).Decode(&td)
	require.NoError(tb, err)

	if td.Setup != nil && td.Setup.PrintOptions != nil {
		if !meta.IsDefined("setup", "print_options", "indent_case_when") {
			td.Setup.PrintOptions.IndentCaseWhen = true
		}

		require.NoError(tb, td.Setup.PrintOptions.Validate())
	}

	return &td
}

func parseScript(script string, parserOpts *googlesql.ParserOptions) (*googlesql.ASTScript, error) {
	if err := bqlang.Init(); err != nil {
		return nil, fmt.Errorf("parseScript: %w", err)
	}

	if parserOpts == nil {
		parserOpts = bqlang.DefaultParserOptions()
	}

	out, err := googlesql.ParseScript(
		script,
		parserOpts,
		bqlang.DefaultErrorMessageOptions(),
	)
	if err != nil {
		return nil, fmt.Errorf("parseScript: %w", err)
	}

	s, err := out.Script()
	if err != nil {
		return nil, fmt.Errorf("Script: %w", err)
	}

	return s, nil
}

func cleanupDebugString(ds string) string {
	parseLocPat := regexp.MustCompile(`\[\d+\-\d+\]`)
	valuesPat := regexp.MustCompile(`\([^\)]+\)`)

	return valuesPat.ReplaceAllLiteralString(
		parseLocPat.ReplaceAllLiteralString(ds, ""),
		"",
	)
}
