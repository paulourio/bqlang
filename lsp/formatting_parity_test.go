package lsp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/bqconfig"
	"github.com/paulourio/bqlang/formatter"
	"github.com/paulourio/bqlang/internal/syntax"
	"github.com/paulourio/bqlang/lsp"
)

type paritySuiteFile struct {
	Setup *paritySuiteSetup  `toml:"setup"`
	Cases []*paritySuiteCase `toml:"cases"`
}

type paritySuiteSetup struct {
	PrintOptions map[string]any `toml:"print_options"`
}

type paritySuiteCase struct {
	Description string `toml:"description"`
	Input       string `toml:"input"`
}

// TestFormattingParityWithBqfmt guarantees that bqpls textDocument/formatting
// produces byte-for-byte identical output to bqfmt across every formatter
// testdata suite and its .bqfmt.toml print_options configuration.
func TestFormattingParityWithBqfmt(t *testing.T) {
	t.Parallel()

	testdataDir := filepath.Join("..", "formatter", "testdata")
	entries, err := os.ReadDir(testdataDir)
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}

		suiteName := entry.Name()
		suitePath := filepath.Join(testdataDir, suiteName)

		t.Run(suiteName, func(t *testing.T) {
			t.Parallel()

			var suite paritySuiteFile
			_, decErr := toml.DecodeFile(suitePath, &suite)
			require.NoError(t, decErr)
			require.NotEmpty(t, suite.Cases)

			ws := t.TempDir()
			cfgPath := filepath.Join(ws, ".bqfmt.toml")

			var buf bytes.Buffer
			if suite.Setup != nil && len(suite.Setup.PrintOptions) > 0 {
				enc := toml.NewEncoder(&buf)
				require.NoError(t, enc.Encode(suite.Setup.PrintOptions))
			}

			buf.WriteString("\n[lsp]\nbigquery_api_enabled = false\ndiagnostics_enabled = false\n")
			require.NoError(t, os.WriteFile(cfgPath, buf.Bytes(), 0o600))

			loader, loadErr := bqconfig.NewLoader("")
			require.NoError(t, loadErr)

			c := startTestServer(t, lsp.ServerOptions{})
			initResp := c.call("initialize", lsp.InitializeParams{
				RootURI: pathToURI(ws),
			})
			require.Nil(t, initResp.Error)
			c.notify("initialized", map[string]any{})

			for idx, tc := range suite.Cases {
				caseLabel := fmt.Sprintf("%s:case_%d", suiteName, idx)
				if tc.Description != "" {
					caseLabel = fmt.Sprintf("%s (%s)", caseLabel, tc.Description)
				}

				filePath := filepath.Join(ws, fmt.Sprintf("case_%d.sql", idx))
				uri := pathToURI(filePath)

				// 1. Format via bqfmt's exact config + formatter pipeline.
				printOpts, optErr := loader.OptionsFor(filePath, false)
				require.NoError(t, optErr, caseLabel)

				bqfmtOut, bqfmtErr := formatter.NewBigQueryFormatter(
					formatter.WithPrintOptions(printOpts),
				).Format(tc.Input)

				// 2. Format via bqpls textDocument/formatting.
				c.notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
					TextDocument: lsp.TextDocumentItem{
						URI:        uri,
						LanguageID: "bqlang",
						Version:    1,
						Text:       tc.Input,
					},
				})

				resp := c.call("textDocument/formatting", lsp.DocumentFormattingParams{
					TextDocument: lsp.TextDocumentIdentifier{URI: uri},
					Options:      lsp.FormattingOptions{TabSize: 2, InsertSpaces: true},
				})
				require.Nil(t, resp.Error, caseLabel)

				var edits []lsp.TextEdit
				require.NoError(t, json.Unmarshal(resp.Result, &edits), caseLabel)

				if bqfmtErr != nil {
					assert.Empty(t, edits, "%s: bqpls should skip formatting when bqfmt errors (%v)", caseLabel, bqfmtErr)
				} else {
					bqplsOut := applyLSPTextEdits(t, tc.Input, edits)
					assert.Equal(t, bqfmtOut, bqplsOut, "%s: bqpls output must match bqfmt 100%%", caseLabel)

					if tc.Input == bqfmtOut {
						assert.Empty(t, edits, "%s: unchanged document must return empty edits", caseLabel)
					}

					// 3. Verify LSP formatting idempotency on the formatted result.
					if bqplsOut != tc.Input {
						c.notify("textDocument/didChange", lsp.DidChangeTextDocumentParams{
							TextDocument: lsp.VersionedTextDocumentIdentifier{URI: uri, Version: 2},
							ContentChanges: []lsp.TextDocumentContentChangeEvent{
								{Text: bqplsOut},
							},
						})

						resp2 := c.call("textDocument/formatting", lsp.DocumentFormattingParams{
							TextDocument: lsp.TextDocumentIdentifier{URI: uri},
							Options:      lsp.FormattingOptions{TabSize: 2, InsertSpaces: true},
						})
						require.Nil(t, resp2.Error, caseLabel)

						var edits2 []lsp.TextEdit
						require.NoError(t, json.Unmarshal(resp2.Result, &edits2), caseLabel)
						assert.Empty(t, edits2, "%s: second LSP formatting pass must be idempotent", caseLabel)
					}
				}

				c.notify("textDocument/didClose", lsp.DidCloseTextDocumentParams{
					TextDocument: lsp.TextDocumentIdentifier{URI: uri},
				})
			}
		})
	}
}

func applyLSPTextEdits(t *testing.T, src string, edits []lsp.TextEdit) string {
	t.Helper()

	if len(edits) == 0 {
		return src
	}

	li := syntax.NewLineIndex(src)
	sorted := slices.Clone(edits)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Range.Start.Line != sorted[j].Range.Start.Line {
			return sorted[i].Range.Start.Line < sorted[j].Range.Start.Line
		}

		if sorted[i].Range.Start.Character != sorted[j].Range.Start.Character {
			return sorted[i].Range.Start.Character < sorted[j].Range.Start.Character
		}

		if sorted[i].Range.End.Line != sorted[j].Range.End.Line {
			return sorted[i].Range.End.Line < sorted[j].Range.End.Line
		}

		return sorted[i].Range.End.Character < sorted[j].Range.End.Character
	})

	var b strings.Builder

	cursor := 0
	for _, ed := range sorted {
		startOff := li.ByteOffset(syntax.Position{Line: ed.Range.Start.Line, Character: ed.Range.Start.Character})
		endOff := li.ByteOffset(syntax.Position{Line: ed.Range.End.Line, Character: ed.Range.End.Character})
		require.GreaterOrEqual(t, startOff, cursor, "overlapping LSP text edits")
		require.GreaterOrEqual(t, endOff, startOff, "invalid LSP text edit range")
		require.LessOrEqual(t, endOff, len(src), "LSP text edit out of bounds")

		b.WriteString(src[cursor:startOff])
		b.WriteString(ed.NewText)
		cursor = endOff
	}

	b.WriteString(src[cursor:])

	return b.String()
}
