package syntax_test

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/internal/syntax"
)

var adversarialSyntaxSeeds = []string{
	"",
	" ",
	"\r\n\n\r\t",
	"\u3000",
	"\u00a0",
	"\x00",
	"\xff\xfe\xfd",
	"SELECT 1",
	"SELECT '😀' FROM `tbl--1`",
	"r'abc\\'",
	"r'abc\\\\'",
	"RB'''multi\nline\n'''",
	"`unclosed\\`",
	"'unclosed\nSELECT 1",
	"/*/",
	"/* unclosed\nline2",
	"-- comment\n# pound\n// slash\nSELECT 1",
	"{#}",
	"{#-}",
	"{#- comment -#}",
	"{{",
	"}}",
	"{{ unclosed",
	`{{ {"a": {"b": "}}"}} }}`,
	"{% if a %}\nSELECT 1\n{% elif b %}\nSELECT 2\n{% else %}\nSELECT 3\n{% endif %}",
	"{%- raw -%}{{ {{ {% endraw +%}",
	"{% raw %}unclosed raw",
	"{% macro m() %}\nSELECT {{ x }}\n{% endmacro %}\nSELECT 1",
	"SELECT {{ a }} + {{ b }}FROM t",
	"t.20160101, t.123abc, 123.abc, .5e-2, 0x1A, @p, @@, @@sys.var",
	"BEGIN TRANSACTION; BEGIN DECLARE x INT64; IF x > 0 THEN SELECT 1; END IF; END;",
	"SELECT IF(a, 1, 2), CASE WHEN b THEN 3 ELSE 4 END FROM t FOR SYSTEM_TIME AS OF NOW();",
	";;; SELECT 1;;",
	"SELECT {{ func(a }} FROM my_table",
	`SELECT {{ {"a": 1] }} FROM my_table`,
	"{% if (x %} SELECT 1 {% endif %}",
	"proj_{{ env }}_db.tbl",
	"{{ config(materialized='table') }} {% set x = 1 %}\nSELECT 1;",
	"{% if a %}\nSELECT 1\n{% else %}\nSELECT 2",
	"SELECT s.case FROM t; SELECT 2;",
	"FOR r IN (SELECT 1;) DO SELECT a FROM tbl do FOR SYSTEM_TIME AS OF NOW(); END FOR; SELECT 2;",
	"CREATE PROCEDURE p(x INT64 DEFAULT (SELECT begin FROM t)) BEGIN SELECT 1; END;",
	"{% set x | filter(arg=1) %} SELECT 1; {% endset %} SELECT 2;",
	"{% if flag %}{{ config(materialized='table') }}{% else %}SELECT 1{% endif %}\nSELECT 2 AS ok;",
	"{{ config(materialized='table') }} {% macro m() %}SELECT 1{% endmacro %}\nSELECT 2 AS ok;",
	"SELECT CASE WHEN true THEN 1 ELSE 2 END repeat; SELECT 2;",
	"REPEAT (SELECT 1); UNTIL true END REPEAT; SELECT 2;",
	"CREATE VIEW v AS SELECT 1 AS procedure, begin FROM t; SELECT 2;",
	"IF true THEN FOR do IN (SELECT 1 AS a) DO IF do.a = 1 THEN SELECT 1; END IF; END FOR; END IF; SELECT 2;",
	"repeat: BEGIN SELECT 1; END repeat; SELECT 2;",
	"WHILE do NOT IN (1, 2) DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
	"WHILE do(1) DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
	"REPEAT (SELECT 1); SELECT CASE WHEN true THEN REPEAT('a', 3) ELSE 'b' END repeat; UNTIL true END REPEAT; SELECT 2;",
	"REPEAT('a', 3); lbl: REPEAT SELECT 1; UNTIL true END REPEAT lbl; SELECT 2;",
	"REPEAT('a', 3); SELECT until FROM t; SELECT 2;",
	"WHILE x > 0 DO (SELECT 1); END WHILE; SELECT 2;",
	"LOOP WHILE do DO LOOP LEAVE; END LOOP; END WHILE; END LOOP; SELECT 2;",
	"WHILE NOT do DO WHILE x > 0 DO SELECT 1; END WHILE; SELECT 2; END WHILE; SELECT 3;",
	"WHILE do NOT BETWEEN 1 AND 5 DO WHILE x > 0 DO SELECT 1; END WHILE; SELECT 2; END WHILE; SELECT 3;",
	"WHILE do DO do: LOOP LEAVE do; END LOOP do; END WHILE; SELECT 2;",
	"WHILE do AT TIME ZONE 'UTC' < TIMESTAMP '2026-01-01' DO WHILE x > 0 DO SELECT 1; END WHILE; END WHILE; SELECT 2;",
	"REPEAT (SELECT 1); SELECT * FROM do repeat; UNTIL true END REPEAT; SELECT 2;",
	"REPEAT('a', 3); SELECT do until FROM t; SELECT 2;",
	"REPEAT (SELECT 1); repeat: BEGIN WHILE x > 0 DO SET x = 0; END WHILE; END repeat; UNTIL true END REPEAT; SELECT 2;",
	"WHILE ARRAY<STRUCT<do BOOL>>[STRUCT(true)][OFFSET(0)].do DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
	"REPEAT (SELECT 1); WHILE STRUCT<do BOOL>(true).do DO REPEAT SELECT 1; UNTIL true END REPEAT; END WHILE; UNTIL true END REPEAT; SELECT 2;",
	"IF(a, 1, 2) + CASE WHEN x THEN 1 ELSE 2 END; SELECT 2;",
	"SELECT STRUCT<a INT64; SELECT 2;",
	"WHILE x > 0 DO (SELECT 1) |> WHERE true; SELECT * FROM do while; END WHILE; SELECT 2;",
	"REPEAT (SELECT 1); WHILE x > 0 DO (SELECT 1) |> WHERE true; SELECT do repeat FROM t; END WHILE; UNTIL true END REPEAT; SELECT 2;",
	"WHILE CURRENT_DATE() + INTERVAL do DAY > DATE '2020-01-01' DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
	"WHILE do DO day: LOOP LEAVE day; END LOOP day; END WHILE; SELECT 2;",
	"IF(a, 1, 2) = STRUCT<then INT64>(1).then; SELECT 2;",
	"REPEAT (SELECT 1); IF true THEN REPEAT SELECT 1; END IF; UNTIL true END REPEAT; SELECT 2;",
}

// FuzzScan fuzzes syntax.Scan and verifies gapless positive-width token
// partitioning and lookup invariants on arbitrary inputs.
func FuzzScan(f *testing.F) {
	for _, seed := range adversarialSyntaxSeeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 8192 {
			return
		}

		toks := syntax.Scan(input)
		if input == "" {
			require.Empty(t, toks)

			return
		}

		require.NotEmpty(t, toks)
		require.Equal(t, 0, toks[0].Start)
		require.Equal(t, len(input), toks[len(toks)-1].End)

		for i, tok := range toks {
			require.Less(t, tok.Start, tok.End)
			require.Equal(t, tok.Len(), tok.End-tok.Start)
			require.Equal(t, input[tok.Start:tok.End], tok.Text(input))
			require.NotEmpty(t, tok.Kind.String())
			require.NotEmpty(t, tok.Mode.String())

			if i > 0 {
				require.Equal(t, toks[i-1].End, tok.Start)
			}

			got, idx, ok := syntax.TokenAt(toks, tok.Start)
			require.True(t, ok)
			require.Equal(t, i, idx)
			require.Equal(t, tok, got)
		}

		_, lastIdx, ok := syntax.TokenBefore(toks, len(input))
		require.True(t, ok)
		require.Equal(t, len(toks)-1, lastIdx)
	})
}

// FuzzProjectSQL fuzzes syntax.ProjectSQL and ProjectBranch, verifying newline
// preservation and monotonic OffsetMap bounds/round-trip invariants.
func FuzzProjectSQL(f *testing.F) {
	for _, seed := range adversarialSyntaxSeeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 8192 {
			return
		}

		proj := syntax.ProjectSQL(input, nil)
		require.NotNil(t, proj.OffsetMap)
		require.Equal(t, strings.Count(input, "\n"), strings.Count(proj.SQL, "\n"))

		if len(proj.Exprs) == 0 {
			require.Len(t, proj.SQL, len(input))
		}

		prevOrig := -1
		for s := 0; s <= len(proj.SQL); s++ {
			orig, ok := proj.OffsetMap.ShadowByteToOrig(s)
			require.GreaterOrEqual(t, orig, 0)
			require.LessOrEqual(t, orig, len(input))
			require.GreaterOrEqual(t, orig, prevOrig)
			prevOrig = orig

			if len(proj.Exprs) == 0 {
				require.True(t, ok)
				require.Equal(t, s, orig)
			}
		}

		endOrig, endOK := proj.OffsetMap.ShadowByteToOrig(len(proj.SQL))
		require.True(t, endOK)
		require.Equal(t, len(input), endOrig)

		prevShadow := -1
		for o := 0; o <= len(input); o++ {
			sh := proj.OffsetMap.OrigByteToShadow(o)
			require.GreaterOrEqual(t, sh, 0)
			require.LessOrEqual(t, sh, len(proj.SQL))
			require.GreaterOrEqual(t, sh, prevShadow)
			prevShadow = sh
		}

		require.Equal(t, len(proj.SQL), proj.OffsetMap.OrigByteToShadow(len(input)))

		for _, ph := range proj.Exprs {
			require.GreaterOrEqual(t, ph.OrigStart, 0)
			require.Less(t, ph.OrigStart, ph.OrigEnd)
			require.LessOrEqual(t, ph.OrigEnd, len(input))
			require.GreaterOrEqual(t, ph.ShadowStart, 0)
			require.Less(t, ph.ShadowStart, ph.ShadowEnd)
			require.LessOrEqual(t, ph.ShadowEnd, len(proj.SQL))

			mappedStart, okStart := proj.OffsetMap.ShadowByteToOrig(ph.ShadowStart)
			require.True(t, okStart)
			require.Equal(t, ph.OrigStart, mappedStart)

			mappedEnd, okEnd := proj.OffsetMap.ShadowByteToOrig(ph.ShadowEnd)
			require.True(t, okEnd)
			require.Equal(t, ph.OrigEnd, mappedEnd)
		}

		for _, b := range proj.Branches {
			require.GreaterOrEqual(t, b.BlockStart, 0)
			require.LessOrEqual(t, b.BlockStart, b.TagStart)
			require.Less(t, b.TagStart, b.TagEnd)
			require.Equal(t, b.TagEnd, b.BodyStart)
			require.LessOrEqual(t, b.BodyStart, b.BodyEnd)
			require.LessOrEqual(t, b.BodyEnd, b.BlockEnd)
			require.LessOrEqual(t, b.BlockEnd, len(input))
			require.GreaterOrEqual(t, b.Depth, 1)
		}

		if len(proj.Branches) > 0 {
			brProj := syntax.ProjectBranch(input, nil, proj.Branches[0])
			require.NotNil(t, brProj.OffsetMap)
			require.Equal(t, strings.Count(input, "\n"), strings.Count(brProj.SQL, "\n"))
		}
	})
}

// FuzzSplitStatements fuzzes syntax.SplitStatements and verifies monotonic,
// non-empty, non-overlapping statement spans.
func FuzzSplitStatements(f *testing.F) {
	for _, seed := range adversarialSyntaxSeeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 8192 {
			return
		}

		spans := syntax.SplitStatements(input, nil)
		prevEnd := 0

		for _, sp := range spans {
			require.GreaterOrEqual(t, sp.Start, prevEnd)
			require.Less(t, sp.Start, sp.End)
			require.LessOrEqual(t, sp.End, len(input))
			require.NotEmpty(t, strings.Trim(input[sp.Start:sp.End], " \t\v\f\r\n"))
			prevEnd = sp.End
		}
	})
}

// FuzzLineIndexApplyChanges fuzzes LineIndex UTF-16 positioning, GoogleSQL column mapping,
// and sequential ApplyChanges range edits.
func FuzzLineIndexApplyChanges(f *testing.F) {
	for _, seed := range adversarialSyntaxSeeds {
		f.Add(seed, 0, 1, 0, 3, "x")
		f.Add("a\U0001F600b\r\nc", 0, 2, 1, 1, "\U0001F680")
	}
	f.Add("SELECT 1\nFROM tbl", 1, math.MaxInt32, 1, math.MaxInt, " AS t")
	f.Add("line0\nline1\nline2", 1, math.MaxInt, 2, math.MaxInt32, "")

	f.Fuzz(func(t *testing.T, src string, line1, char1, line2, char2 int, insert string) {
		if len(src) > 8192 || len(insert) > 1024 {
			return
		}

		idx := syntax.NewLineIndex(src)
		require.GreaterOrEqual(t, idx.LineCount(), 1)
		require.Equal(t, len(src), idx.ByteLen())

		for line := range idx.LineCount() {
			ls := idx.LineStart(line)
			le := idx.LineEnd(line)
			for off := ls; off <= le; {
				pos := idx.PositionAt(off)
				require.Equal(t, line, pos.Line)
				require.Equal(t, off, idx.ByteOffset(pos))

				if off == le {
					break
				}

				_, size := utf8.DecodeRuneInString(src[off:le])
				off += size
			}
		}

		rng := syntax.Range{
			Start: syntax.Position{Line: line1, Character: char1},
			End:   syntax.Position{Line: line2, Character: char2},
		}
		s, e := idx.ByteRange(rng)
		require.GreaterOrEqual(t, s, 0)
		require.LessOrEqual(t, s, e)
		require.LessOrEqual(t, e, len(src))

		gsOff := idx.ByteOffsetFromGoogleSQL(line1, char1)
		require.GreaterOrEqual(t, gsOff, 0)
		require.LessOrEqual(t, gsOff, len(src))

		updated, newIdx := syntax.ApplyChanges(src, []syntax.TextChange{
			{Range: &rng, Text: insert},
			{
				Range: &syntax.Range{
					Start: syntax.Position{Line: 0, Character: 0},
					End:   syntax.Position{Line: 0, Character: 0},
				},
				Text: " ",
			},
		})
		require.Equal(t, len(updated), newIdx.ByteLen())

		if utf8.ValidString(src) && utf8.ValidString(insert) {
			require.True(t, utf8.ValidString(updated))
			require.True(t, utf8.ValidString(src[:gsOff]))
		}
	})
}
