package analysis_test

import (
	"context"
	"encoding/binary"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/bqconfig"
	"github.com/paulourio/bqlang/catalog"
	"github.com/paulourio/bqlang/internal/analysis"
	"github.com/paulourio/bqlang/internal/syntax"
)

var adversarialAnalysisSeeds = []string{
	"",
	"   \t\r\n  ",
	"-- comment only\n# hash\n// slash\n/* multi\nline */",
	"SELECT 1",
	"SELECT event_date, tenant_id, user_ctx.user_id FROM events WHERE event_date = '2026-01-01'",
	"SELECT u.event_date, i._PARTITIONTIME, w._TABLE_SUFFIX FROM events u JOIN events_ing i ON u.event_date = DATE(i._PARTITIONTIME) JOIN `my-proj.analytics.events_*` w ON u.tenant_id = w.tenant_id",
	"WITH c AS (SELECT event_date, tenant_id FROM events) SELECT tenant_id FROM c WHERE event_date = '2026-01-01'",
	"DECLARE dt DATE DEFAULT DATE '2026-01-01'; CREATE TEMP TABLE t AS SELECT dt AS d; SELECT d FROM t;",
	"BEGIN DECLARE x INT64; IF TRUE THEN SELECT 1; ELSE SELECT 2; END IF; END;",
	"FROM events |> WHERE event_date = '2026-01-01' |> EXTEND 1 AS extra_col |> AGGREGATE COUNT(*) AS n",
	"SELECT DATE_TRUNC(event_date, MONTH), SAFE.SUBSTR(tenant_id, 1, 2), AI.GENERATE('hi') FROM events",
	"{# jinja comment #}\n{% macro m(a) %}{{ a }}{% endmacro %}\n{% set v = 1 %}\nSELECT {{ ref('events') }} FROM tbl {% if x %} WHERE 1=1 {% elif y %} WHERE 2=2 {% else %} WHERE 3=3 {% endif %}",
	"{% raw %}SELECT {{ not_jinja }} {% endraw %}",
	"{% for item in items %}{{ loop.index }}{% endfor %}",
	"SELECT '😀', u. FROM events u",
	"SELECT u. FROM (SELECT event_date, tenant_id FROM events u",
	"SELECT , tenant_id FROM events",
	"SELECT 'unclosed string\nSELECT '''triple\n",
	"SELECT `unclosed backtick",
	"SELECT ((( FROM WHERE;",
	"\x00\xff\xfe\xfdSELECT \x00 1",
	"CREATE TABLE t (a STRUCT<b ARRAY<INT64>, c RANGE<DATE>>);",
}

func newFuzzEngine(t testing.TB) (*catalog.Store, *analysis.Engine) {
	t.Helper()
	require.NoError(t, bqlang.Init())

	opts := bqconfig.DefaultLSPOptions()
	opts.DefaultProject = "my-proj"
	opts.DefaultDataset = "analytics"
	opts.BigQueryAPIEnabled = false

	store, err := catalog.NewStore(opts)
	require.NoError(t, err)
	seedTestTables(store)

	eng, err := analysis.NewEngine(store, opts)
	require.NoError(t, err)

	return store, eng
}

func assertValidEncodedTokens(t testing.TB, src string, data []uint32) {
	t.Helper()
	require.Equal(t, 0, len(data)%5, "encoded token length must be multiple of 5")

	lines := splitLinesAnyTerm(src)
	var line, ch, prevEnd uint32
	hasPrev := false

	for i := 0; i < len(data); i += 5 {
		dl, dc, ln, tp, md := data[i], data[i+1], data[i+2], data[i+3], data[i+4]
		require.Positive(t, ln, "token length must be > 0")
		require.Less(t, tp, uint32(14), "token type out of legend bounds")
		require.Less(t, md, uint32(128), "token modifier out of legend bounds")

		if dl > 0 {
			line += dl
			ch = dc
		} else {
			ch += dc
			if hasPrev {
				require.GreaterOrEqual(t, ch, prevEnd, "tokens on line %d overlap", line)
			}
		}

		require.Less(t, int(line), len(lines), "token line %d out of bounds (%d lines)", line, len(lines))
		lineUnits := uint32(len(utf16.Encode([]rune(lines[line]))))
		require.LessOrEqual(t, ch+ln, lineUnits, "token on line %d exceeds line UTF-16 length", line)

		prevEnd = ch + ln
		hasPrev = true
	}
}

func splitLinesAnyTerm(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); {
		if s[i] == '\r' || s[i] == '\n' {
			lines = append(lines, s[start:i])
			if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
				i += 2
			} else {
				i++
			}
			start = i
			continue
		}
		i++
	}
	lines = append(lines, s[start:])

	return lines
}

func applyFuzzEdits(prev []uint32, edits []analysis.SemanticTokensEdit) []uint32 {
	cur := make([]uint32, len(prev))
	copy(cur, prev)
	for _, ed := range edits {
		start := int(ed.Start)
		end := start + int(ed.DeleteCount)
		next := make([]uint32, 0, len(cur)-int(ed.DeleteCount)+len(ed.Data))
		next = append(next, cur[:start]...)
		next = append(next, ed.Data...)
		next = append(next, cur[end:]...)
		cur = next
	}

	return cur
}

func FuzzSemanticTokensAndDelta(f *testing.F) {
	require.NoError(f, bqlang.Init())
	for _, s := range adversarialAnalysisSeeds {
		f.Add(s, " event_date ", uint16(0), uint16(0))
	}

	store, eng := newFuzzEngine(f)
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	f.Fuzz(func(t *testing.T, src, insert string, lineRaw, charRaw uint16) {
		if len(src) > 8192 || len(insert) > 1024 {
			return
		}

		uri := "file:///fuzz_tokens.sql.j2"
		eng.OpenDocument(uri, src, 1, nil)
		defer eng.CloseDocument(uri)

		full1, err := eng.SemanticTokensFull(ctx, uri)
		require.NoError(t, err)
		require.NotNil(t, full1.Data)
		assertValidEncodedTokens(t, src, full1.Data)

		rngRes, err := eng.SemanticTokensRange(ctx, uri, syntax.Range{
			Start: syntax.Position{Line: 0, Character: 0},
			End:   syntax.Position{Line: int(lineRaw % 64), Character: int(charRaw % 256)},
		})
		require.NoError(t, err)
		require.NotNil(t, rngRes.Data)
		assertValidEncodedTokens(t, src, rngRes.Data)

		pos := syntax.Position{Line: int(lineRaw % 32), Character: int(charRaw % 128)}
		err = eng.ChangeDocument(uri, 2, []syntax.TextChange{{
			Range: &syntax.Range{Start: pos, End: pos},
			Text:  insert,
		}})
		require.NoError(t, err)

		updatedText, ok := eng.DocumentText(uri)
		require.True(t, ok)

		delta, err := eng.SemanticTokensFullDelta(ctx, uri, full1.ResultID)
		require.NoError(t, err)
		require.NotNil(t, delta.Edits)

		full2, err := eng.SemanticTokensFull(ctx, uri)
		require.NoError(t, err)
		assertValidEncodedTokens(t, updatedText, full2.Data)

		if delta.IsDelta {
			require.Equal(t, full2.Data, applyFuzzEdits(full1.Data, delta.Edits))
		}
	})
}

func FuzzDiffSemanticTokens(f *testing.F) {
	f.Add([]byte{0, 0, 6, 6, 0, 0, 7, 10, 4, 8}, []byte{0, 0, 6, 6, 0, 0, 7, 10, 4, 8})
	f.Add([]byte{0, 0, 6, 6, 0}, []byte{0, 0, 6, 6, 0, 1, 0, 4, 6, 0})
	f.Add([]byte{}, []byte{0, 0, 3, 1, 0})
	f.Add([]byte{1, 2, 3, 4}, []byte{})

	f.Fuzz(func(t *testing.T, prevBytes, nextBytes []byte) {
		if len(prevBytes) > 4096 || len(nextBytes) > 4096 {
			return
		}

		prev := bytesToUint32s(prevBytes)
		next := bytesToUint32s(nextBytes)

		edits := analysis.DiffSemanticTokens(prev, next)
		require.NotNil(t, edits)
		require.Equal(t, next, applyFuzzEdits(prev, edits))
	})
}

func bytesToUint32s(b []byte) []uint32 {
	n := len(b) / 4
	out := make([]uint32, n)
	for i := range n {
		out[i] = binary.LittleEndian.Uint32(b[i*4 : (i+1)*4])
	}

	return out
}

func FuzzDiagnosticsAndHover(f *testing.F) {
	require.NoError(f, bqlang.Init())
	for _, s := range adversarialAnalysisSeeds {
		f.Add(s, uint16(0), uint16(0))
	}

	store, eng := newFuzzEngine(f)
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	f.Fuzz(func(t *testing.T, src string, lineRaw, charRaw uint16) {
		if len(src) > 8192 {
			return
		}

		uri := "file:///fuzz_diag_hover.sql.j2"
		eng.OpenDocument(uri, src, 1, nil)
		defer eng.CloseDocument(uri)

		diags, err := eng.Diagnostics(ctx, uri)
		require.NoError(t, err)
		require.NotNil(t, diags)
		for _, d := range diags {
			require.True(t, posLE(d.Range.Start, d.Range.End), "diagnostic range inverted")
		}

		pos := syntax.Position{Line: int(lineRaw % 64), Character: int(charRaw % 256)}
		hov, err := eng.Hover(ctx, uri, pos)
		require.NoError(t, err)
		if hov != nil {
			require.Equal(t, "markdown", hov.Contents.Kind)
			if hov.Range != nil {
				require.True(t, posLE(hov.Range.Start, hov.Range.End), "hover range inverted")
			}
		}

		defs, err := eng.Definition(ctx, uri, pos)
		require.NoError(t, err)
		require.NotNil(t, defs)
		for _, loc := range defs {
			require.True(t, posLE(loc.Range.Start, loc.Range.End), "definition range inverted")
		}

		syms, err := eng.DocumentSymbols(ctx, uri)
		require.NoError(t, err)
		require.NotNil(t, syms)
	})
}

func FuzzComplete(f *testing.F) {
	require.NoError(f, bqlang.Init())
	for _, s := range adversarialAnalysisSeeds {
		f.Add(s, uint16(0), uint16(10))
	}

	store, eng := newFuzzEngine(f)
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	f.Fuzz(func(t *testing.T, src string, lineRaw, charRaw uint16) {
		if len(src) > 8192 {
			return
		}

		uri := "file:///fuzz_complete.sql.j2"
		eng.OpenDocument(uri, src, 1, nil)
		defer eng.CloseDocument(uri)

		pos := syntax.Position{Line: int(lineRaw % 64), Character: int(charRaw % 256)}
		cl, err := eng.Complete(ctx, uri, pos)
		require.NoError(t, err)
		require.NotNil(t, cl)
		require.NotNil(t, cl.Items)

		for _, item := range cl.Items {
			require.NotEmpty(t, item.Label)
			require.NotContains(t, item.InsertText, "$1", "unexpected snippet syntax in InsertText")
		}
	})
}

func posLE(a, b syntax.Position) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}

	return a.Character <= b.Character
}
