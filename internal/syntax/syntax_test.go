package syntax_test

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/internal/syntax"
)

func TestLineIndex_UTF16AndLineEndings(t *testing.T) {
	t.Parallel()

	t.Run("ascii_and_crlf_cr_lf", func(t *testing.T) {
		t.Parallel()

		src := "ab\r\nc\rd\n"
		idx := syntax.NewLineIndex(src)

		require.Equal(t, 4, idx.LineCount())
		assert.Equal(t, len(src), idx.ByteLen())
		assert.Equal(t, 0, idx.LineStart(0))
		assert.Equal(t, 2, idx.LineEnd(0))
		assert.Equal(t, 4, idx.LineStart(1))
		assert.Equal(t, 5, idx.LineEnd(1))
		assert.Equal(t, 6, idx.LineStart(2))
		assert.Equal(t, 7, idx.LineEnd(2))
		assert.Equal(t, 8, idx.LineStart(3))
		assert.Equal(t, 8, idx.LineEnd(3))

		// Clamping before CRLF (never splits \r and \n).
		assert.Equal(t, syntax.Position{Line: 0, Character: 2}, idx.PositionAt(2))
		assert.Equal(t, syntax.Position{Line: 0, Character: 2}, idx.PositionAt(3))
		assert.Equal(t, syntax.Position{Line: 1, Character: 0}, idx.PositionAt(4))
		assert.Equal(t, 2, idx.ByteOffset(syntax.Position{Line: 0, Character: 99}))
		assert.Equal(t, len(src), idx.ByteOffset(syntax.Position{Line: 99, Character: 0}))
		assert.Equal(t, 0, idx.ByteOffset(syntax.Position{Line: -1, Character: 5}))
	})

	t.Run("utf16_surrogate_pairs_and_invalid_utf8", func(t *testing.T) {
		t.Parallel()

		// 'a' (1B, 1 u16), '😀' U+1F600 (4B, 2 u16), 'é' U+00E9 (2B, 1 u16), 'b' (1B, 1 u16).
		src := "a\U0001F600\u00e9b"
		idx := syntax.NewLineIndex(src)

		assert.Equal(t, syntax.Position{Line: 0, Character: 0}, idx.PositionAt(0))
		assert.Equal(t, syntax.Position{Line: 0, Character: 1}, idx.PositionAt(1))
		// Mid-rune byte offsets inside '😀' snap down to byte 1 (Character 1).
		assert.Equal(t, syntax.Position{Line: 0, Character: 1}, idx.PositionAt(2))
		assert.Equal(t, syntax.Position{Line: 0, Character: 1}, idx.PositionAt(4))
		assert.Equal(t, syntax.Position{Line: 0, Character: 3}, idx.PositionAt(5))
		assert.Equal(t, syntax.Position{Line: 0, Character: 4}, idx.PositionAt(7))
		assert.Equal(t, syntax.Position{Line: 0, Character: 5}, idx.PositionAt(8))

		assert.Equal(t, 1, idx.ByteOffset(syntax.Position{Line: 0, Character: 1}))
		// Mid-surrogate column 2 snaps down to rune start byte 1.
		assert.Equal(t, 1, idx.ByteOffset(syntax.Position{Line: 0, Character: 2}))
		assert.Equal(t, 5, idx.ByteOffset(syntax.Position{Line: 0, Character: 3}))
		assert.Equal(t, 7, idx.ByteOffset(syntax.Position{Line: 0, Character: 4}))
		assert.Equal(t, 8, idx.ByteOffset(syntax.Position{Line: 0, Character: 5}))

		// Invalid UTF-8 byte counts as 1 UTF-16 unit (U+FFFD).
		bad := "a\xffb"
		badIdx := syntax.NewLineIndex(bad)
		assert.Equal(t, syntax.Position{Line: 0, Character: 2}, badIdx.PositionAt(2))
		assert.Equal(t, 2, badIdx.ByteOffset(syntax.Position{Line: 0, Character: 2}))
	})

	t.Run("google_sql_tab_and_rune_columns", func(t *testing.T) {
		t.Parallel()

		src := "\ta\U0001F600b"
		idx := syntax.NewLineIndex(src)

		// Tab at col 1 expands to col 9 (next token starts at col 9).
		assert.Equal(t, 0, idx.ByteOffsetFromGoogleSQL(1, 1))
		assert.Equal(t, 1, idx.ByteOffsetFromGoogleSQL(1, 9))
		// 'a' is col 9, '😀' is col 10 (1 rune in GoogleSQL, 4 bytes), 'b' is col 11.
		assert.Equal(t, 2, idx.ByteOffsetFromGoogleSQL(1, 10))
		assert.Equal(t, 6, idx.ByteOffsetFromGoogleSQL(1, 11))
		assert.Equal(t, len(src), idx.ByteOffsetFromGoogleSQL(1, 99))
		assert.Equal(t, 0, idx.ByteOffsetFromGoogleSQL(0, 1))
		assert.Equal(t, len(src), idx.ByteOffsetFromGoogleSQL(5, 1))
	})

	t.Run("sequential_apply_changes", func(t *testing.T) {
		t.Parallel()

		src := "SELECT '😀' FROM tbl"
		idx := syntax.NewLineIndex(src)

		// Replace '😀' (chars 8..10) with 'x', then append ';' at the post-edit end.
		updated, newIdx := idx.ApplyChanges(src, []syntax.TextChange{
			{
				Range: &syntax.Range{
					Start: syntax.Position{Line: 0, Character: 8},
					End:   syntax.Position{Line: 0, Character: 10},
				},
				Text: "x",
			},
			{
				Range: &syntax.Range{
					Start: syntax.Position{Line: 0, Character: 19},
					End:   syntax.Position{Line: 0, Character: 19},
				},
				Text: ";",
			},
		})

		require.Equal(t, "SELECT 'x' FROM tbl;", updated)
		assert.Equal(t, len(updated), newIdx.ByteLen())
		assert.True(t, utf8.ValidString(updated))

		// Full-document replacement when Range is nil.
		replaced, repIdx := newIdx.ApplyChanges(updated, []syntax.TextChange{
			{Text: "SELECT 1\nSELECT 2"},
		})
		require.Equal(t, "SELECT 1\nSELECT 2", replaced)
		assert.Equal(t, 2, repIdx.LineCount())

		// Package-level ApplyChanges and nil LineIndex safety.
		pkgUpdated, pkgIdx := syntax.ApplyChanges("SELECT 1", nil)
		assert.Equal(t, "SELECT 1", pkgUpdated)
		assert.Equal(t, 1, pkgIdx.LineCount())

		var nilIdx *syntax.LineIndex
		assert.Equal(t, 1, nilIdx.LineCount())
		assert.Equal(t, 0, nilIdx.ByteLen())
		assert.Equal(t, syntax.Position{}, nilIdx.PositionAt(5))
		assert.Equal(t, 0, nilIdx.ByteOffset(syntax.Position{Line: 1, Character: 1}))
		assert.Equal(t, 0, nilIdx.ByteOffsetFromGoogleSQL(1, 5))
		assert.Equal(t, syntax.Range{Start: syntax.Position{Line: 1}, End: syntax.Position{Line: 1, Character: 8}}, repIdx.RangeAt(9, 17))
	})

	t.Run("byte_offset_max_int_no_overflow", func(t *testing.T) {
		t.Parallel()

		src := "SELECT 1\nFROM tbl\nWHERE a = '😀'"
		idx := syntax.NewLineIndex(src)

		assert.Equal(t, idx.LineEnd(1), idx.ByteOffset(syntax.Position{Line: 1, Character: math.MaxInt}))
		assert.Equal(t, idx.LineEnd(1), idx.ByteOffset(syntax.Position{Line: 1, Character: math.MaxInt32}))
		assert.Equal(t, idx.LineEnd(2), idx.ByteOffset(syntax.Position{Line: 2, Character: math.MaxInt}))
		assert.Equal(t, idx.LineEnd(2), idx.ByteOffset(syntax.Position{Line: 2, Character: math.MaxInt32}))

		updated, newIdx := idx.ApplyChanges(src, []syntax.TextChange{
			{
				Range: &syntax.Range{
					Start: syntax.Position{Line: 1, Character: math.MaxInt32},
					End:   syntax.Position{Line: 1, Character: math.MaxInt},
				},
				Text: " AS t",
			},
		})
		assert.Equal(t, "SELECT 1\nFROM tbl AS t\nWHERE a = '😀'", updated)
		assert.Equal(t, len(updated), newIdx.ByteLen())
	})
}

func TestScan_SQLAndJinjaModes(t *testing.T) {
	t.Parallel()

	assertPartition := func(t *testing.T, src string, toks []syntax.Token) {
		t.Helper()

		if src == "" {
			require.Empty(t, toks)

			return
		}

		require.NotEmpty(t, toks)
		assert.Equal(t, 0, toks[0].Start)
		assert.Equal(t, len(src), toks[len(toks)-1].End)

		for i, tok := range toks {
			assert.Less(t, tok.Start, tok.End, "token %d empty", i)
			assert.NotEmpty(t, tok.Kind.String())
			assert.NotEmpty(t, tok.Mode.String())

			if i > 0 {
				assert.Equal(t, toks[i-1].End, tok.Start, "gap at token %d", i)
			}
		}
	}

	t.Run("comments_opaque_to_jinja", func(t *testing.T) {
		t.Parallel()

		src := "SELECT 1 -- {{ a }}\n# {{ b }}\n// {{ c }}\n/* {# nested */ '{{ x }}' FROM `tbl--1`"
		toks := syntax.Scan(src)
		assertPartition(t, src, toks)

		var kinds []syntax.TokenKind
		for _, tok := range toks {
			if tok.Kind != syntax.TokenWhitespace && tok.Kind != syntax.TokenNewline {
				kinds = append(kinds, tok.Kind)
			}
		}

		assert.Equal(t, []syntax.TokenKind{
			syntax.TokenIdent,
			syntax.TokenNumber,
			syntax.TokenComment,
			syntax.TokenComment,
			syntax.TokenComment,
			syntax.TokenComment,
			syntax.TokenString,
			syntax.TokenIdent,
			syntax.TokenBacktickIdent,
		}, kinds)
	})

	t.Run("raw_bytes_triple_and_backtick_escapes", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name     string
			src      string
			kind     syntax.TokenKind
			unclosed bool
			count    int
		}{
			{"raw_odd_backslash_unclosed", `r'abc\'`, syntax.TokenString, true, 1},
			{"raw_even_backslash_closed", `r'abc\\'`, syntax.TokenString, false, 1},
			{"raw_bytes_triple", `RB"""line1\n'''"""`, syntax.TokenBytes, false, 1},
			{"bytes_single", `b"ab"`, syntax.TokenBytes, false, 1},
			{"triple_six_quotes", `''''''`, syntax.TokenString, false, 1},
			{"backtick_escaped", "`a\\`b`", syntax.TokenBacktickIdent, false, 1},
			{"backtick_double_not_escape", "`a``b`", syntax.TokenBacktickIdent, false, 2},
			{"single_line_stops_before_newline", "'abc\nSELECT", syntax.TokenString, true, 3},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				toks := syntax.Scan(tc.src)
				assertPartition(t, tc.src, toks)
				require.Len(t, toks, tc.count)
				assert.Equal(t, tc.kind, toks[0].Kind)
				assert.Equal(t, tc.unclosed, toks[0].Unclosed)
			})
		}
	})

	t.Run("identifiers_dots_numbers_sigils_operators", func(t *testing.T) {
		t.Parallel()

		src := "t.20160101, t.123abc, 123.abc, .5e-2, 0xDEAD, @p, @@script.tz, @@, a |> b -> c => d"
		toks := syntax.Scan(src)
		assertPartition(t, src, toks)

		type pair struct {
			kind syntax.TokenKind
			text string
		}

		var got []pair
		for _, tok := range toks {
			if tok.Kind == syntax.TokenWhitespace {
				continue
			}

			got = append(got, pair{tok.Kind, tok.Text(src)})
		}

		assert.Equal(t, []pair{
			{syntax.TokenIdent, "t"},
			{syntax.TokenPunct, "."},
			{syntax.TokenIdent, "20160101"},
			{syntax.TokenPunct, ","},
			{syntax.TokenIdent, "t"},
			{syntax.TokenPunct, "."},
			{syntax.TokenIdent, "123abc"},
			{syntax.TokenPunct, ","},
			{syntax.TokenNumber, "123."},
			{syntax.TokenIdent, "abc"},
			{syntax.TokenPunct, ","},
			{syntax.TokenNumber, ".5e-2"},
			{syntax.TokenPunct, ","},
			{syntax.TokenNumber, "0xDEAD"},
			{syntax.TokenPunct, ","},
			{syntax.TokenParam, "@p"},
			{syntax.TokenPunct, ","},
			{syntax.TokenSysVar, "@@script"},
			{syntax.TokenPunct, "."},
			{syntax.TokenIdent, "tz"},
			{syntax.TokenPunct, ","},
			{syntax.TokenSysVar, "@@"},
			{syntax.TokenPunct, ","},
			{syntax.TokenIdent, "a"},
			{syntax.TokenOperator, "|>"},
			{syntax.TokenIdent, "b"},
			{syntax.TokenOperator, "->"},
			{syntax.TokenIdent, "c"},
			{syntax.TokenOperator, "=>"},
			{syntax.TokenIdent, "d"},
		}, got)

		// Bare @@ is marked Unclosed for completion support.
		tok, _, ok := syntax.TokenAt(toks, strings.Index(src, "@@,"))
		require.True(t, ok)
		assert.Equal(t, syntax.TokenSysVar, tok.Kind)
		assert.True(t, tok.Unclosed)
	})

	t.Run("jinja_nested_braces_strings_and_raw", func(t *testing.T) {
		t.Parallel()

		src := `{{ {"a": {"b": "}}"}} }} {%- raw -%} {{ not_jinja }} {% endraw +%} {#- c -#}`
		toks := syntax.Scan(src)
		assertPartition(t, src, toks)

		var sawRawBody, sawExprClose, sawComment bool
		for _, tok := range toks {
			switch tok.Kind {
			case syntax.TokenJinjaRawBody:
				sawRawBody = true
				assert.Equal(t, syntax.ModeJinjaRaw, tok.Mode)
				assert.False(t, tok.Unclosed)
				assert.Equal(t, " {{ not_jinja }} ", tok.Text(src))
			case syntax.TokenJinjaExprClose:
				sawExprClose = true
				assert.Equal(t, "}}", tok.Text(src))
			case syntax.TokenJinjaComment:
				sawComment = true
				assert.Equal(t, syntax.ModeJinjaComment, tok.Mode)
				assert.False(t, tok.Unclosed)
			default:
			}
		}

		assert.True(t, sawRawBody)
		assert.True(t, sawExprClose)
		assert.True(t, sawComment)
	})

	t.Run("unclosed_jinja_and_token_lookup", func(t *testing.T) {
		t.Parallel()

		for _, src := range []string{"{#}", "{#-}", "/*/", "{% raw %}abc", "{{ unclosed"} {
			toks := syntax.Scan(src)
			assertPartition(t, src, toks)
			assert.True(t, toks[len(toks)-1].Unclosed || toks[0].Mode != syntax.ModeSQL)
		}

		toks := syntax.Scan("SELECT 1")
		tok, idx, ok := syntax.TokenBefore(toks, 8)
		require.True(t, ok)
		assert.Equal(t, len(toks)-1, idx)
		assert.Equal(t, "1", tok.Text("SELECT 1"))
		assert.False(t, tok.IsTrivia())
		assert.False(t, tok.IsComment())
		assert.False(t, tok.IsJinja())
	})

	t.Run("invalid_prefixes_numbers_and_jinja_keywords", func(t *testing.T) {
		t.Parallel()

		// Invalid string prefix rr'a' lexes as ident + string; 1e+ lexes as number + ident + operator.
		toks := syntax.Scan("rr'a' 1e+ `tbl`.2024")
		assertPartition(t, "rr'a' 1e+ `tbl`.2024", toks)
		require.Len(t, toks, 10)
		assert.Equal(t, syntax.TokenIdent, toks[0].Kind)
		assert.Equal(t, syntax.TokenString, toks[1].Kind)
		assert.Equal(t, syntax.TokenNumber, toks[3].Kind)
		assert.Equal(t, syntax.TokenIdent, toks[4].Kind)
		assert.Equal(t, syntax.TokenOperator, toks[5].Kind)
		assert.Equal(t, syntax.TokenBacktickIdent, toks[7].Kind)
		assert.Equal(t, syntax.TokenPunct, toks[8].Kind)
		assert.Equal(t, syntax.TokenIdent, toks[9].Kind)

		// Jinja tag keywords, expression keywords, and prefixed numbers.
		jToks := syntax.Scan("{%+ for a in items if a > 0b10_1 +%}")
		assertPartition(t, "{%+ for a in items if a > 0b10_1 +%}", jToks)
		var tagKWs, exprKWs, jNums int
		for _, tok := range jToks {
			assert.True(t, tok.IsJinja())
			switch tok.Kind {
			case syntax.TokenJinjaTagKeyword:
				tagKWs++
			case syntax.TokenJinjaKeyword:
				exprKWs++
			case syntax.TokenJinjaNumber:
				jNums++
			default:
			}
		}
		assert.Equal(t, 1, tagKWs)
		assert.Equal(t, 2, exprKWs)
		assert.Equal(t, 1, jNums)
	})

	t.Run("unbalanced_and_mismatched_jinja_brackets_recover", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name    string
			src     string
			sqlWord string
		}{
			{"unclosed_paren_in_expr", "SELECT {{ func(a }} FROM my_table", "FROM"},
			{"unclosed_bracket_in_expr", "SELECT {{ arr[0 }} FROM my_table", "FROM"},
			{"mismatched_bracket_in_expr", `SELECT {{ {"a": 1] }} FROM my_table`, "FROM"},
			{"unclosed_paren_in_tag", "{% if (x %} SELECT 1 {% endif %}", "SELECT"},
			{"unclosed_bracket_in_tag", "{% for x in items[0 %} SELECT 1 {% endfor %}", "SELECT"},
			{"valid_nested_dict_3_braces", `{{ {"a": 1}}} SELECT 1`, "SELECT"},
			{"valid_nested_dict_string_braces", `{{ {"a": {"b": "}}"}} }} SELECT 1`, "SELECT"},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				toks := syntax.Scan(tc.src)
				assertPartition(t, tc.src, toks)

				var found bool
				for _, tok := range toks {
					if tok.Text(tc.src) == tc.sqlWord {
						found = true
						assert.Equal(t, syntax.ModeSQL, tok.Mode)
					}
				}
				assert.True(t, found, "missing SQL token %q in %q", tc.sqlWord, tc.src)
			})
		}

		proj := syntax.ProjectSQL("SELECT {{ func(a }} FROM my_table;\nSELECT 2 FROM other_table;", nil)
		assert.Contains(t, proj.SQL, "FROM my_table;")
		assert.Contains(t, proj.SQL, "other_table")
	})

	t.Run("pathological_mismatched_jinja_brackets_linear_recovery", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name string
			src  string
		}{
			{"many_open_parens_then_close_braces", "{{ " + strings.Repeat("(", 5000) + strings.Repeat("}", 5000) + " }} SELECT 1"},
			{"many_open_braces_then_close_brackets", "{{ " + strings.Repeat("{", 5000) + strings.Repeat("]", 5000) + " }} SELECT 1"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				toks := syntax.Scan(tc.src)
				assertPartition(t, tc.src, toks)

				var sawSQLSelect bool
				for _, tok := range toks {
					if tok.Mode == syntax.ModeSQL && tok.Text(tc.src) == "SELECT" {
						sawSQLSelect = true
					}
				}
				assert.True(t, sawSQLSelect)
			})
		}
	})
}

func TestProjectSQL_And_Branches(t *testing.T) {
	t.Parallel()

	t.Run("comments_and_primary_if_keep_1_to_1_offsets", func(t *testing.T) {
		t.Parallel()

		src := "SELECT 1 // line note\n{% if flag %}\n  , col_a\n{% elif other %}\n  , col_b\n{% else %}\n  , col_c\n{% endif %}\nFROM tbl"
		proj := syntax.ProjectSQL(src, nil)

		require.Len(t, proj.SQL, len(src))
		assert.Equal(t, strings.Count(src, "\n"), strings.Count(proj.SQL, "\n"))
		assert.NotContains(t, proj.SQL, "//")
		assert.Contains(t, proj.SQL, "col_a")
		assert.NotContains(t, proj.SQL, "col_b")
		assert.NotContains(t, proj.SQL, "col_c")

		// Check 1:1 offset mapping on col_a and FROM tbl.
		colOff := strings.Index(src, "col_a")
		shOff := proj.OffsetMap.OrigByteToShadow(colOff)
		assert.Equal(t, colOff, shOff)
		origOff, ok := proj.OffsetMap.ShadowByteToOrig(shOff)
		assert.True(t, ok)
		assert.Equal(t, colOff, origOff)

		require.Len(t, proj.Branches, 2)
		assert.Equal(t, syntax.BranchElif, proj.Branches[0].Kind)
		assert.True(t, proj.Branches[0].Closed)
		assert.Equal(t, syntax.BranchElse, proj.Branches[1].Kind)

		// Projecting the elif branch activates col_b and blanks col_a / col_c.
		elifProj := syntax.ProjectBranch(src, nil, proj.Branches[0])
		assert.NotContains(t, elifProj.SQL, "col_a")
		assert.Contains(t, elifProj.SQL, "col_b")
		assert.NotContains(t, elifProj.SQL, "col_c")
	})

	t.Run("standalone_vs_inline_expressions_and_offset_map", func(t *testing.T) {
		t.Parallel()

		src := "{{ config(materialized='table') }}\nSELECT\n  {{ col }}\n  , tbl_{{ ds }}\n  , {{ x }}FROM t"
		proj := syntax.ProjectSQL(src, nil)

		require.Len(t, proj.Exprs, 4)
		assert.True(t, proj.Exprs[0].Standalone)
		assert.False(t, proj.Exprs[1].Standalone)
		assert.False(t, proj.Exprs[2].Standalone)
		assert.False(t, proj.Exprs[3].Standalone)

		assert.Contains(t, proj.SQL, "SELECT __bqpls_expr_0__;")
		assert.Contains(t, proj.SQL, "\n  __bqpls_expr_1__\n")
		assert.Contains(t, proj.SQL, "tbl___bqpls_expr_2__")
		assert.Contains(t, proj.SQL, "__bqpls_expr_3__ FROM t")

		// Synthetic interior returns ok=false with anchor; boundary returns ok=true.
		ph0 := proj.Exprs[0]
		orig, ok := proj.OffsetMap.ShadowByteToOrig(ph0.ShadowStart)
		assert.True(t, ok)
		assert.Equal(t, ph0.OrigStart, orig)

		origMid, okMid := proj.OffsetMap.ShadowByteToOrig(ph0.ShadowStart + 2)
		assert.False(t, okMid)
		assert.Equal(t, ph0.OrigStart, origMid)

		fromOrig := strings.Index(src, "FROM t")
		fromShadow := proj.OffsetMap.OrigByteToShadow(fromOrig)
		assert.Equal(t, "FROM t", proj.SQL[fromShadow:fromShadow+6])
		back, okBack := proj.OffsetMap.ShadowByteToOrig(fromShadow)
		assert.True(t, okBack)
		assert.Equal(t, fromOrig, back)
	})

	t.Run("macro_and_raw_blocks_blanked_when_closed", func(t *testing.T) {
		t.Parallel()

		src := "{% macro helper(x) %}\nSELECT {{ x }}\n{% endmacro %}\n{% raw %}{{ a }}{% endraw %}\nSELECT 1"
		proj := syntax.ProjectSQL(src, nil)

		assert.Empty(t, proj.Exprs)
		assert.Contains(t, proj.SQL, "SELECT 1")
		assert.NotContains(t, proj.SQL, "__bqpls_expr_")
		assert.Equal(t, strings.Count(src, "\n"), strings.Count(proj.SQL, "\n"))

		// Unclosed macro does not swallow following SQL; multiline {{ expr }} preserves newlines.
		unclosed := "{% macro m() %}\nSELECT\n  {{\n    col\n  }}\nFROM t"
		uProj := syntax.ProjectSQL(unclosed, nil)
		assert.Equal(t, strings.Count(unclosed, "\n"), strings.Count(uProj.SQL, "\n"))
		assert.Contains(t, uProj.SQL, "SELECT\n  __bqpls_expr_0__")
		assert.Contains(t, uProj.SQL, "FROM t")
		assert.Equal(t, "elif", syntax.BranchElif.String())
		assert.Equal(t, "else", syntax.BranchElse.String())
	})

	t.Run("unclosed_branches_and_nested_project_branch", func(t *testing.T) {
		t.Parallel()

		unclosed := "{% if a %}\n  SELECT 1\n{% elif b %}\n  SELECT 2\n{% else %}\n  SELECT 3\n"
		proj := syntax.ProjectSQL(unclosed, nil)
		require.Len(t, proj.Branches, 2)
		assert.Equal(t, syntax.BranchElif, proj.Branches[0].Kind)
		assert.False(t, proj.Branches[0].Closed)
		assert.Equal(t, len(unclosed), proj.Branches[0].BlockEnd)
		assert.Equal(t, syntax.BranchElse, proj.Branches[1].Kind)
		assert.False(t, proj.Branches[1].Closed)
		assert.Equal(t, len(unclosed), proj.Branches[1].BodyEnd)
		assert.Equal(t, len(unclosed), proj.Branches[1].BlockEnd)

		elifProj := syntax.ProjectBranch(unclosed, nil, proj.Branches[0])
		assert.NotContains(t, elifProj.SQL, "SELECT 1")
		assert.Contains(t, elifProj.SQL, "SELECT 2")
		assert.NotContains(t, elifProj.SQL, "SELECT 3")

		elseProj := syntax.ProjectBranch(unclosed, nil, proj.Branches[1])
		assert.NotContains(t, elseProj.SQL, "SELECT 1")
		assert.NotContains(t, elseProj.SQL, "SELECT 2")
		assert.Contains(t, elseProj.SQL, "SELECT 3")

		// Nested ProjectBranch inside closed and unclosed blocks.
		nested := "{% if a %}SELECT 1{% else %}{% if b %}SELECT 2{% else %}SELECT 3{% endif %}{% endif %}"
		nProj := syntax.ProjectSQL(nested, nil)
		require.Len(t, nProj.Branches, 2)
		innerElse := nProj.Branches[0]
		assert.Equal(t, 2, innerElse.Depth)
		nBranchProj := syntax.ProjectBranch(nested, nil, innerElse)
		assert.NotContains(t, nBranchProj.SQL, "SELECT 1")
		assert.NotContains(t, nBranchProj.SQL, "SELECT 2")
		assert.Contains(t, nBranchProj.SQL, "SELECT 3")

		// Branches inside blanked non-query blocks must not be recorded.
		inMacro := "{% macro m(x) %}{% if x %}SELECT 1{% else %}SELECT 2{% endif %}{% endmacro %}\n" +
			"{% call c() %}{% if a %}SELECT 3{% else %}SELECT 4{% endif %}{% endcall %}\n" +
			"{% set blk | trim %}{% if a %}SELECT 5{% else %}SELECT 6{% endif %}{% endset %}\n" +
			"{% filter upper %}{% if a %}SELECT 7{% else %}SELECT 8{% endif %}{% endfilter %}\n" +
			"SELECT 9"
		mProj := syntax.ProjectSQL(inMacro, nil)
		assert.Empty(t, mProj.Branches)
		assert.Contains(t, mProj.SQL, "SELECT 9")
	})

	t.Run("interpolated_ident_suffix_shadow_end_and_same_line_trivia", func(t *testing.T) {
		t.Parallel()

		// Ident prefix+suffix concatenation should not insert a space.
		interp := "SELECT id FROM proj_{{ env }}_db.tbl"
		iProj := syntax.ProjectSQL(interp, nil)
		assert.Contains(t, iProj.SQL, "proj___bqpls_expr_0___db.tbl")
		assert.NotContains(t, iProj.SQL, "__bqpls_expr_0__ _db")

		// ShadowEnd maps cleanly back to OrigEnd even when trailing space/newlines exist.
		spaceSrc := "SELECT {{ x }}FROM t"
		sProj := syntax.ProjectSQL(spaceSrc, nil)
		require.Len(t, sProj.Exprs, 1)
		assert.Contains(t, sProj.SQL, "__bqpls_expr_0__ FROM t")
		origEnd, okEnd := sProj.OffsetMap.ShadowByteToOrig(sProj.Exprs[0].ShadowEnd)
		assert.True(t, okEnd)
		assert.Equal(t, sProj.Exprs[0].OrigEnd, origEnd)

		nlSrc := "SELECT\n  {{\n    col\n  }}\nFROM t"
		nlProj := syntax.ProjectSQL(nlSrc, nil)
		require.Len(t, nlProj.Exprs, 1)
		nlEnd, okNLEnd := nlProj.OffsetMap.ShadowByteToOrig(nlProj.Exprs[0].ShadowEnd)
		assert.True(t, okNLEnd)
		assert.Equal(t, nlProj.Exprs[0].OrigEnd, nlEnd)

		// Standalone expressions with trailing same-line Jinja tags/comments and leading blanked tags.
		standaloneCases := []struct {
			name string
			src  string
		}{
			{"trailing_set_tag", "{{ config(materialized='table') }} {% set x = 1 %}\nSELECT 1;"},
			{"trailing_jinja_comment", "{{ config(materialized='table') }} {# note #}\nSELECT 1;"},
			{"trailing_multiline_jinja_comment", "{{ config(materialized='table') }} {# multi\nline note #}\nSELECT 1;"},
			{"inside_single_line_if", "{% if flag %}{{ config(materialized='table') }}{% endif %}\nSELECT 1;"},
		}
		for _, tc := range standaloneCases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				p := syntax.ProjectSQL(tc.src, nil)
				require.Len(t, p.Exprs, 1)
				assert.True(t, p.Exprs[0].Standalone)
				assert.Contains(t, p.SQL, "SELECT __bqpls_expr_0__;")
			})
		}

		// CR-only multiline expression preserves LineCount.
		crSrc := "SELECT\r  {{\r    col\r  }}\rFROM t"
		crProj := syntax.ProjectSQL(crSrc, nil)
		assert.Equal(t, syntax.NewLineIndex(crSrc).LineCount(), syntax.NewLineIndex(crProj.SQL).LineCount())
	})

	t.Run("same_line_blanked_branches_and_non_query_blocks", func(t *testing.T) {
		t.Parallel()

		standaloneSameLine := []struct {
			name string
			src  string
		}{
			{
				"if_else_sql_same_line",
				"{% if flag %}{{ config(materialized='table') }}{% else %}SELECT 1{% endif %}\nSELECT 2 AS ok;",
			},
			{
				"if_else_expr_same_line",
				"{% if flag %}{{ config(materialized='table') }}{% else %}{{ other() }}{% endif %}\nSELECT 2 AS ok;",
			},
			{
				"if_elif_expr_same_line",
				"{% if flag %}{{ config(materialized='table') }}{% elif other_flag %}{{ other() }}{% endif %}\nSELECT 2 AS ok;",
			},
			{
				"trailing_closed_macro",
				"{{ config(materialized='table') }} {% macro m() %}SELECT 1{% endmacro %}\nSELECT 2 AS ok;",
			},
			{
				"trailing_closed_block_set",
				"{{ config(materialized='table') }} {% set q %}SELECT 1{% endset %}\nSELECT 2 AS ok;",
			},
			{
				"trailing_closed_filter",
				"{{ config(materialized='table') }} {% filter upper %}abc{% endfilter %}\nSELECT 2 AS ok;",
			},
			{
				"trailing_closed_call",
				"{{ config(materialized='table') }} {% call c() %}SELECT 1{% endcall %}\nSELECT 2 AS ok;",
			},
			{
				"trailing_raw_block",
				"{{ config(materialized='table') }} {% raw %}SELECT 1{% endraw %}\nSELECT 2 AS ok;",
			},
		}

		for _, tc := range standaloneSameLine {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				p := syntax.ProjectSQL(tc.src, nil)
				require.Len(t, p.Exprs, 1)
				assert.True(t, p.Exprs[0].Standalone)
				assert.Contains(t, p.SQL, "SELECT __bqpls_expr_0__;")
			})
		}

		// Non-blanked SQL on the same line must keep Standalone == false.
		nonStandaloneSameLine := []struct {
			name string
			src  string
		}{
			{"active_branch_trailing_sql", "{% if flag %}{{ col }} FROM t{% endif %}"},
			{"after_endif_trailing_sql", "{% if flag %}{{ col }}{% endif %} FROM t"},
			{"after_closed_macro_trailing_sql", "{{ col }} {% macro m() %}SELECT 1{% endmacro %} FROM t"},
			{"unclosed_macro_does_not_blank", "{{ config() }} {% macro m() %}SELECT 1\n"},
		}

		for _, tc := range nonStandaloneSameLine {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				p := syntax.ProjectSQL(tc.src, nil)
				require.Len(t, p.Exprs, 1)
				assert.False(t, p.Exprs[0].Standalone)
				assert.NotContains(t, p.SQL, "SELECT __bqpls_expr_0__;")
			})
		}

		// ProjectBranch activating secondary branch on the same line.
		branchBothExprs := "{% if a %}{{ x() }}{% else %}{{ config() }}{% endif %}\nSELECT 2 AS ok;"
		prim := syntax.ProjectSQL(branchBothExprs, nil)
		require.Len(t, prim.Exprs, 1)
		assert.True(t, prim.Exprs[0].Standalone)
		assert.Contains(t, prim.SQL, "SELECT __bqpls_expr_0__;")
		require.Len(t, prim.Branches, 1)

		sec := syntax.ProjectBranch(branchBothExprs, nil, prim.Branches[0])
		require.Len(t, sec.Exprs, 1)
		assert.True(t, sec.Exprs[0].Standalone)
		assert.Contains(t, sec.SQL, "SELECT __bqpls_expr_0__;")

		branchWithTrailingSQL := "{% if a %}SELECT 1{% else %}{{ col }}{% endif %} FROM t"
		primTrail := syntax.ProjectSQL(branchWithTrailingSQL, nil)
		require.Len(t, primTrail.Branches, 1)
		secTrail := syntax.ProjectBranch(branchWithTrailingSQL, nil, primTrail.Branches[0])
		require.Len(t, secTrail.Exprs, 1)
		assert.False(t, secTrail.Exprs[0].Standalone)
		assert.Contains(t, secTrail.SQL, "__bqpls_expr_0__")
		assert.NotContains(t, secTrail.SQL, "SELECT __bqpls_expr_0__;")
	})
}

func TestSplitStatements(t *testing.T) {
	t.Parallel()

	t.Run("procedural_transactions_and_expressions", func(t *testing.T) {
		t.Parallel()

		src := strings.Join([]string{
			"BEGIN TRANSACTION;",
			"BEGIN DECLARE x INT64 DEFAULT 1; BEGIN TRANSACTION; SELECT x; COMMIT; EXCEPTION WHEN ERROR THEN SELECT 0; END;",
			"CREATE OR REPLACE PROCEDURE ds.p() BEGIN SELECT 1; END;",
			"SELECT IF(a > 0, 1, 2), REPEAT('x', 3) FROM t FOR SYSTEM_TIME AS OF CURRENT_TIMESTAMP();",
			"IF x > 0 THEN SELECT 1; ELSE SELECT 2; END IF;",
			"CASE x WHEN 1 THEN SELECT 1; ELSE SELECT 2; END CASE;",
			"SELECT CASE WHEN a THEN 1 ELSE 2 END FROM t;",
			"lbl: LOOP LEAVE lbl; END LOOP lbl;",
		}, "\n")

		spans := syntax.SplitStatements(src, nil)
		require.Len(t, spans, 8)

		for i, sp := range spans {
			assert.True(t, sp.HasSemicolon, "stmt %d", i)
			assert.Less(t, sp.Start, sp.End)
		}

		assert.True(t, strings.HasPrefix(src[spans[0].Start:spans[0].End], "BEGIN TRANSACTION;"))
		assert.True(t, strings.HasPrefix(src[spans[1].Start:spans[1].End], "BEGIN DECLARE"))
		assert.True(t, strings.HasPrefix(src[spans[2].Start:spans[2].End], "CREATE OR REPLACE PROCEDURE"))
		assert.True(t, strings.HasPrefix(src[spans[3].Start:spans[3].End], "SELECT IF("))
		assert.True(t, strings.HasPrefix(src[spans[4].Start:spans[4].End], "IF x > 0"))
		assert.True(t, strings.HasPrefix(src[spans[5].Start:spans[5].End], "CASE x"))
		assert.True(t, strings.HasPrefix(src[spans[6].Start:spans[6].End], "SELECT CASE"))
		assert.True(t, strings.HasPrefix(src[spans[7].Start:spans[7].End], "lbl: LOOP"))
	})

	t.Run("jinja_blocks_and_empty_semicolons", func(t *testing.T) {
		t.Parallel()

		src := ";; {% if flag %} SELECT 1; {% else %} SELECT 2; {% endif %} ; SELECT 3 -- tail"
		spans := syntax.SplitStatements(src, nil)
		require.Len(t, spans, 2)
		assert.True(t, spans[0].HasSemicolon)
		assert.False(t, spans[1].HasSemicolon)
		assert.Equal(t, "SELECT 3", src[spans[1].Start:spans[1].End])

		assert.Empty(t, syntax.SplitStatements("\u3000 \u00a0", nil))
	})

	t.Run("while_for_repeat_and_non_block_keywords", func(t *testing.T) {
		t.Parallel()

		src := strings.Join([]string{
			"WHILE x > 0 DO SET x = x - 1; END WHILE;",
			"FOR r IN (SELECT 1;) DO SELECT r; END FOR;",
			"REPEAT SET x = x + 1; UNTIL x > 5 END REPEAT;",
			"IF (a = 1) AND (b = 2) THEN SELECT 1; ELSEIF (c = 3) THEN SELECT 2; END IF;",
			"CREATE TABLE IF NOT EXISTS t (a INT64);",
			"SELECT a FROM t PIVOT(SUM(v) FOR k IN ('a')) FOR UPDATE;",
			"SELECT 1;",
		}, "\n")

		spans := syntax.SplitStatements(src, nil)
		require.Len(t, spans, 7)
		for i, sp := range spans {
			assert.True(t, sp.HasSemicolon, "stmt %d", i)
		}
		assert.True(t, strings.HasPrefix(src[spans[0].Start:spans[0].End], "WHILE x > 0"))
		assert.True(t, strings.HasPrefix(src[spans[1].Start:spans[1].End], "FOR r IN"))
		assert.True(t, strings.HasPrefix(src[spans[2].Start:spans[2].End], "REPEAT SET"))
		assert.True(t, strings.HasPrefix(src[spans[3].Start:spans[3].End], "IF (a = 1) AND"))
		assert.True(t, strings.HasPrefix(src[spans[4].Start:spans[4].End], "CREATE TABLE IF NOT EXISTS"))
		assert.True(t, strings.HasPrefix(src[spans[5].Start:spans[5].End], "SELECT a FROM t PIVOT"))
		assert.Equal(t, "SELECT 1;", src[spans[6].Start:spans[6].End])
	})

	t.Run("do_alias_begin_in_parens_dot_member_and_block_set_filter", func(t *testing.T) {
		t.Parallel()

		src := strings.Join([]string{
			"FOR r IN (SELECT 1;) DO SELECT a FROM tbl do FOR SYSTEM_TIME AS OF CURRENT_TIMESTAMP(); END FOR;",
			"WHILE x > 0 DO SELECT a FROM tbl do FOR SYSTEM_TIME AS OF CURRENT_TIMESTAMP(); END WHILE;",
			"CREATE PROCEDURE p(x INT64 DEFAULT (SELECT begin FROM t)) BEGIN SELECT 1; END;",
			"SELECT s.case FROM t;",
			"BEGIN SELECT s.end FROM t; END;",
			"{% set x | filter(arg=1) %} SELECT 1; {% endset %} SELECT 2;",
		}, "\n")

		spans := syntax.SplitStatements(src, nil)
		require.Len(t, spans, 6)
		for i, sp := range spans {
			assert.True(t, sp.HasSemicolon, "stmt %d", i)
		}
		assert.True(t, strings.HasPrefix(src[spans[0].Start:spans[0].End], "FOR r IN"))
		assert.True(t, strings.HasPrefix(src[spans[1].Start:spans[1].End], "WHILE x > 0"))
		assert.True(t, strings.HasPrefix(src[spans[2].Start:spans[2].End], "CREATE PROCEDURE p("))
		assert.Equal(t, "SELECT s.case FROM t;", src[spans[3].Start:spans[3].End])
		assert.Equal(t, "BEGIN SELECT s.end FROM t; END;", src[spans[4].Start:spans[4].End])
		assert.Equal(t, "SELECT 1; {% endset %} SELECT 2;", src[spans[5].Start:spans[5].End])
	})

	t.Run("case_end_unreserved_aliases_repeat_paren_create_view_and_for_do_var", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name      string
			src       string
			wantSpans []string
		}{
			{
				"case_end_repeat_alias",
				"SELECT CASE WHEN true THEN 1 ELSE 2 END repeat; SELECT 2;",
				[]string{"SELECT CASE WHEN true THEN 1 ELSE 2 END repeat;", "SELECT 2;"},
			},
			{
				"case_end_loop_alias",
				"SELECT CASE WHEN true THEN 1 ELSE 2 END loop; SELECT 2;",
				[]string{"SELECT CASE WHEN true THEN 1 ELSE 2 END loop;", "SELECT 2;"},
			},
			{
				"case_end_while_alias",
				"SELECT CASE WHEN true THEN 1 ELSE 2 END while; SELECT 2;",
				[]string{"SELECT CASE WHEN true THEN 1 ELSE 2 END while;", "SELECT 2;"},
			},
			{
				"case_end_repeat_alias_inside_begin",
				"BEGIN SELECT CASE WHEN true THEN 1 ELSE 2 END repeat; END; SELECT 2;",
				[]string{"BEGIN SELECT CASE WHEN true THEN 1 ELSE 2 END repeat; END;", "SELECT 2;"},
			},
			{
				"case_end_while_alias_inside_while",
				"WHILE x > 0 DO SELECT CASE WHEN true THEN 1 ELSE 2 END while; END WHILE; SELECT 2;",
				[]string{"WHILE x > 0 DO SELECT CASE WHEN true THEN 1 ELSE 2 END while; END WHILE;", "SELECT 2;"},
			},
			{
				"repeat_parenthesized_query",
				"REPEAT (SELECT 1); UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"scalar_repeat_negative_control",
				"SELECT REPEAT('a', 3); SELECT 2;",
				[]string{"SELECT REPEAT('a', 3);", "SELECT 2;"},
			},
			{
				"malformed_repeat_call_before_valid_repeat",
				"REPEAT('a', 3); SELECT 2; REPEAT SET x = 1; UNTIL x > 5 END REPEAT;",
				[]string{"REPEAT('a', 3);", "SELECT 2;", "REPEAT SET x = 1; UNTIL x > 5 END REPEAT;"},
			},
			{
				"repeat_parenthesized_query_with_dot_until",
				"REPEAT (SELECT 1); SELECT r.until FROM t; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); SELECT r.until FROM t; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"broken_scalar_repeat_with_dot_until",
				"REPEAT('a', 3); SELECT r.until FROM t; SELECT 2;",
				[]string{"REPEAT('a', 3);", "SELECT r.until FROM t;", "SELECT 2;"},
			},
			{
				"create_view_with_procedure_begin_idents",
				"CREATE VIEW v AS SELECT 1 AS procedure, begin FROM t; SELECT 2;",
				[]string{"CREATE VIEW v AS SELECT 1 AS procedure, begin FROM t;", "SELECT 2;"},
			},
			{
				"create_or_replace_table_with_procedure_begin_idents",
				"CREATE OR REPLACE TABLE t AS SELECT 1 AS procedure, begin FROM u; SELECT 2;",
				[]string{"CREATE OR REPLACE TABLE t AS SELECT 1 AS procedure, begin FROM u;", "SELECT 2;"},
			},
			{
				"create_temp_table_with_procedure_begin_idents",
				"CREATE TEMP TABLE t AS SELECT 1 AS procedure, begin FROM u; SELECT 2;",
				[]string{"CREATE TEMP TABLE t AS SELECT 1 AS procedure, begin FROM u;", "SELECT 2;"},
			},
			{
				"create_procedure_if_not_exists",
				"CREATE PROCEDURE IF NOT EXISTS ds.p() BEGIN SELECT 1; END; SELECT 2;",
				[]string{"CREATE PROCEDURE IF NOT EXISTS ds.p() BEGIN SELECT 1; END;", "SELECT 2;"},
			},
			{
				"for_loop_var_named_do_with_nested_if",
				"IF true THEN FOR do IN (SELECT 1 AS a) DO IF do.a = 1 THEN SELECT 1; END IF; END FOR; END IF; SELECT 2;",
				[]string{"IF true THEN FOR do IN (SELECT 1 AS a) DO IF do.a = 1 THEN SELECT 1; END IF; END FOR; END IF;", "SELECT 2;"},
			},
			{
				"while_condition_var_named_do_with_nested_if",
				"WHILE do > 0 DO IF do = 1 THEN SELECT 1; END IF; END WHILE; SELECT 2;",
				[]string{"WHILE do > 0 DO IF do = 1 THEN SELECT 1; END IF; END WHILE;", "SELECT 2;"},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				spans := syntax.SplitStatements(tc.src, nil)
				require.Len(t, spans, len(tc.wantSpans))
				for i, sp := range spans {
					assert.True(t, sp.HasSemicolon)
					assert.Equal(t, tc.wantSpans[i], tc.src[sp.Start:sp.End])
				}
			})
		}
	})

	t.Run("r4_begin_end_labels_while_do_conditions_and_repeat_lookahead", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name      string
			src       string
			wantSpans []string
		}{
			{
				"begin_end_repeat_label",
				"repeat: BEGIN SELECT 1; END repeat; SELECT 2;",
				[]string{"repeat: BEGIN SELECT 1; END repeat;", "SELECT 2;"},
			},
			{
				"begin_end_while_label",
				"while: BEGIN SELECT 1; END while; SELECT 2;",
				[]string{"while: BEGIN SELECT 1; END while;", "SELECT 2;"},
			},
			{
				"begin_end_loop_label",
				"loop: BEGIN SELECT 1; END loop; SELECT 2;",
				[]string{"loop: BEGIN SELECT 1; END loop;", "SELECT 2;"},
			},
			{
				"while_bare_do_condition_nested_while",
				"WHILE do DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE do DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_and_do_condition_nested_while",
				"WHILE a AND do DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE a AND do DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_not_do_condition_nested_while",
				"WHILE NOT do DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE NOT do DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_do_call_empty_args_nested_while",
				"WHILE do() DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE do() DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_do_call_with_arg_nested_while",
				"WHILE do(1) DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE do(1) DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_do_call_eq_nested_if",
				"WHILE do(1) = 1 DO IF true THEN SELECT 1; END IF; END WHILE; SELECT 2;",
				[]string{"WHILE do(1) = 1 DO IF true THEN SELECT 1; END IF; END WHILE;", "SELECT 2;"},
			},
			{
				"while_do_call_member_nested_loop",
				"WHILE do(1).flag DO LOOP SELECT 1; LEAVE; END LOOP; END WHILE; SELECT 2;",
				[]string{"WHILE do(1).flag DO LOOP SELECT 1; LEAVE; END LOOP; END WHILE;", "SELECT 2;"},
			},
			{
				"while_do_not_in_nested_while",
				"WHILE do NOT IN (1, 2) DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE do NOT IN (1, 2) DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_do_not_like_nested_while",
				"WHILE do NOT LIKE '%a%' DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE do NOT LIKE '%a%' DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_do_not_between_nested_while",
				"WHILE do NOT BETWEEN 1 AND 2 DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE do NOT BETWEEN 1 AND 2 DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_do_inside_if_and_loop",
				"IF true THEN LOOP WHILE do NOT IN (1, 2) DO IF true THEN SELECT 1; END IF; END WHILE; END LOOP; END IF; SELECT 2;",
				[]string{"IF true THEN LOOP WHILE do NOT IN (1, 2) DO IF true THEN SELECT 1; END IF; END WHILE; END LOOP; END IF;", "SELECT 2;"},
			},
			{
				"while_rhs_do_condition_nested_begin",
				"WHILE a = do DO BEGIN SELECT 1; END; END WHILE; SELECT 2;",
				[]string{"WHILE a = do DO BEGIN SELECT 1; END; END WHILE;", "SELECT 2;"},
			},
			{
				"while_do_at_nested_if",
				"WHILE do AT TIME ZONE 'UTC' DO IF true THEN SELECT 1; END IF; END WHILE; SELECT 2;",
				[]string{"WHILE do AT TIME ZONE 'UTC' DO IF true THEN SELECT 1; END IF; END WHILE;", "SELECT 2;"},
			},
			{
				"while_body_label_named_do_regression",
				"WHILE x > 0 DO do: LOOP SELECT 1; LEAVE do; END LOOP do; END WHILE; SELECT 2;",
				[]string{"WHILE x > 0 DO do: LOOP SELECT 1; LEAVE do; END LOOP do; END WHILE;", "SELECT 2;"},
			},
			{
				"while_do_condition_and_do_label",
				"WHILE do DO do: LOOP SELECT 1; LEAVE do; END LOOP do; END WHILE; SELECT 2;",
				[]string{"WHILE do DO do: LOOP SELECT 1; LEAVE do; END LOOP do; END WHILE;", "SELECT 2;"},
			},
			{
				"while_body_paren_query_statement",
				"WHILE x > 0 DO (SELECT 1); END WHILE; SELECT 2;",
				[]string{"WHILE x > 0 DO (SELECT 1); END WHILE;", "SELECT 2;"},
			},
			{
				"while_body_paren_query_then_do_alias",
				"WHILE x > 0 DO (SELECT 1); SELECT do AS while FROM t; END WHILE; SELECT 2;",
				[]string{"WHILE x > 0 DO (SELECT 1); SELECT do AS while FROM t; END WHILE;", "SELECT 2;"},
			},
			{
				"repeat_paren_case_then_repeat_expr",
				"REPEAT (SELECT 1); SELECT CASE WHEN true THEN REPEAT('a', 3) ELSE 'b' END; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); SELECT CASE WHEN true THEN REPEAT('a', 3) ELSE 'b' END; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"repeat_paren_case_else_repeat_expr",
				"REPEAT (SELECT 1); SELECT CASE WHEN true THEN 'a' ELSE REPEAT('b', 3) END; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); SELECT CASE WHEN true THEN 'a' ELSE REPEAT('b', 3) END; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"repeat_paren_case_then_and_else_repeat_expr",
				"REPEAT (SELECT 1); SELECT CASE WHEN true THEN REPEAT('a', 3) ELSE REPEAT('b', 3) END; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); SELECT CASE WHEN true THEN REPEAT('a', 3) ELSE REPEAT('b', 3) END; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"repeat_paren_case_end_repeat_alias",
				"REPEAT (SELECT 1); SELECT CASE WHEN true THEN 1 ELSE 2 END repeat; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); SELECT CASE WHEN true THEN 1 ELSE 2 END repeat; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"repeat_paren_nested_case_then_repeat_end_repeat_alias",
				"REPEAT (SELECT 1); SELECT CASE WHEN true THEN CASE WHEN false THEN REPEAT('a', 3) ELSE 'b' END ELSE 'c' END repeat; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); SELECT CASE WHEN true THEN CASE WHEN false THEN REPEAT('a', 3) ELSE 'b' END ELSE 'c' END repeat; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"repeat_paren_nested_begin_end_repeat_label",
				"REPEAT (SELECT 1); repeat: BEGIN SELECT 1; END repeat; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); repeat: BEGIN SELECT 1; END repeat; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"repeat_paren_nested_empty_repeat",
				"REPEAT (SELECT 1); REPEAT UNTIL true END REPEAT; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); REPEAT UNTIL true END REPEAT; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"repeat_paren_begin_transaction",
				"REPEAT (SELECT 1); BEGIN TRANSACTION; SELECT 1; COMMIT; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); BEGIN TRANSACTION; SELECT 1; COMMIT; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"repeat_paren_if_case_expr_condition",
				"REPEAT (SELECT 1); IF CASE WHEN true THEN true ELSE false END THEN SELECT 1; END IF; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); IF CASE WHEN true THEN true ELSE false END THEN SELECT 1; END IF; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"malformed_repeat_call_before_labeled_repeat",
				"REPEAT('a', 3); lbl: REPEAT SELECT 1; UNTIL true END REPEAT lbl; SELECT 2;",
				[]string{"REPEAT('a', 3);", "lbl: REPEAT SELECT 1; UNTIL true END REPEAT lbl;", "SELECT 2;"},
			},
			{
				"malformed_repeat_paren_before_labeled_repeat",
				"REPEAT (SELECT 1); lbl: REPEAT SELECT 1; UNTIL true END REPEAT lbl; SELECT 2;",
				[]string{"REPEAT (SELECT 1);", "lbl: REPEAT SELECT 1; UNTIL true END REPEAT lbl;", "SELECT 2;"},
			},
			{
				"valid_outer_repeat_with_inner_labeled_repeat",
				"REPEAT (SELECT 1); lbl: REPEAT SELECT 1; UNTIL true END REPEAT lbl; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); lbl: REPEAT SELECT 1; UNTIL true END REPEAT lbl; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"malformed_repeat_call_before_backtick_labeled_repeat",
				"REPEAT('a', 3); `lbl`: REPEAT SELECT 1; UNTIL true END REPEAT `lbl`; SELECT 2;",
				[]string{"REPEAT('a', 3);", "`lbl`: REPEAT SELECT 1; UNTIL true END REPEAT `lbl`;", "SELECT 2;"},
			},
			{
				"malformed_repeat_call_before_as_until",
				"REPEAT('a', 3); SELECT 1 AS until; SELECT 2;",
				[]string{"REPEAT('a', 3);", "SELECT 1 AS until;", "SELECT 2;"},
			},
			{
				"malformed_repeat_call_before_select_until",
				"REPEAT('a', 3); SELECT until FROM t; SELECT 2;",
				[]string{"REPEAT('a', 3);", "SELECT until FROM t;", "SELECT 2;"},
			},
			{
				"valid_repeat_paren_with_as_until",
				"REPEAT (SELECT 1); SELECT 1 AS until; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); SELECT 1 AS until; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"malformed_repeat_call_before_select_loop",
				"REPEAT('a', 3); SELECT loop FROM t; SELECT 2;",
				[]string{"REPEAT('a', 3);", "SELECT loop FROM t;", "SELECT 2;"},
			},
			{
				"loop_while_do_nested_loop",
				"LOOP WHILE do DO LOOP LEAVE; END LOOP; END WHILE; END LOOP; SELECT 2;",
				[]string{"LOOP WHILE do DO LOOP LEAVE; END LOOP; END WHILE; END LOOP;", "SELECT 2;"},
			},
			{
				"if_while_do_nested_if",
				"IF true THEN WHILE do DO IF x = 1 THEN SELECT 1; END IF; END WHILE; END IF; SELECT 2;",
				[]string{"IF true THEN WHILE do DO IF x = 1 THEN SELECT 1; END IF; END WHILE; END IF;", "SELECT 2;"},
			},
			{
				"if_while_do_not_in_nested_if",
				"IF true THEN WHILE do NOT IN (1, 2) DO IF x = 1 THEN SELECT 1; END IF; END WHILE; END IF; SELECT 2;",
				[]string{"IF true THEN WHILE do NOT IN (1, 2) DO IF x = 1 THEN SELECT 1; END IF; END WHILE; END IF;", "SELECT 2;"},
			},
			{
				"if_while_do_not_like_nested_if",
				"IF true THEN WHILE do NOT LIKE 'a%' DO IF x = 1 THEN SELECT 1; END IF; END WHILE; END IF; SELECT 2;",
				[]string{"IF true THEN WHILE do NOT LIKE 'a%' DO IF x = 1 THEN SELECT 1; END IF; END WHILE; END IF;", "SELECT 2;"},
			},
			{
				"while_do_nested_while_trailing_select",
				"WHILE do DO WHILE x > 0 DO SELECT 1; END WHILE; SELECT 2; END WHILE; SELECT 3;",
				[]string{"WHILE do DO WHILE x > 0 DO SELECT 1; END WHILE; SELECT 2; END WHILE;", "SELECT 3;"},
			},
			{
				"while_not_do_nested_while_trailing_select",
				"WHILE NOT do DO WHILE x > 0 DO SELECT 1; END WHILE; SELECT 2; END WHILE; SELECT 3;",
				[]string{"WHILE NOT do DO WHILE x > 0 DO SELECT 1; END WHILE; SELECT 2; END WHILE;", "SELECT 3;"},
			},
			{
				"while_do_not_between_nested_while_trailing_select",
				"WHILE do NOT BETWEEN 1 AND 5 DO WHILE x > 0 DO SELECT 1; END WHILE; SELECT 2; END WHILE; SELECT 3;",
				[]string{"WHILE do NOT BETWEEN 1 AND 5 DO WHILE x > 0 DO SELECT 1; END WHILE; SELECT 2; END WHILE;", "SELECT 3;"},
			},
			{
				"while_do_label_do_loop_no_select",
				"WHILE do DO do: LOOP LEAVE do; END LOOP do; END WHILE; SELECT 2;",
				[]string{"WHILE do DO do: LOOP LEAVE do; END LOOP do; END WHILE;", "SELECT 2;"},
			},
			{
				"while_do_at_time_zone_comparison",
				"WHILE do AT TIME ZONE 'UTC' < TIMESTAMP '2026-01-01' DO WHILE x > 0 DO SELECT 1; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE do AT TIME ZONE 'UTC' < TIMESTAMP '2026-01-01' DO WHILE x > 0 DO SELECT 1; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_do_collate_comparison",
				"WHILE do COLLATE 'und:ci' = 'a' DO WHILE x > 0 DO SELECT 1; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE do COLLATE 'und:ci' = 'a' DO WHILE x > 0 DO SELECT 1; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"malformed_repeat_call_labeled_repeat_set",
				"REPEAT('a', 3); lbl: REPEAT SET x = 1; UNTIL x > 5 END REPEAT; SELECT 2;",
				[]string{"REPEAT('a', 3);", "lbl: REPEAT SET x = 1; UNTIL x > 5 END REPEAT;", "SELECT 2;"},
			},
			{
				"repeat_paren_select_star_from_do_repeat",
				"REPEAT (SELECT 1); SELECT * FROM do repeat; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); SELECT * FROM do repeat; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"repeat_paren_select_do_repeat_from_t",
				"REPEAT (SELECT 1); SELECT do repeat FROM t; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); SELECT do repeat FROM t; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"malformed_repeat_call_select_do_until_from_t",
				"REPEAT('a', 3); SELECT do until FROM t; SELECT 2;",
				[]string{"REPEAT('a', 3);", "SELECT do until FROM t;", "SELECT 2;"},
			},
			{
				"repeat_paren_begin_while_end_while_end_repeat_label",
				"REPEAT (SELECT 1); repeat: BEGIN WHILE x > 0 DO SET x = 0; END WHILE; END repeat; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); repeat: BEGIN WHILE x > 0 DO SET x = 0; END WHILE; END repeat; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"repeat_paren_begin_loop_end_loop_end_repeat_label",
				"REPEAT (SELECT 1); repeat: BEGIN LOOP LEAVE; END LOOP; END repeat; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); repeat: BEGIN LOOP LEAVE; END LOOP; END repeat; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"while_struct_do_field_nested_while",
				"WHILE STRUCT<do BOOL>(true).do DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE STRUCT<do BOOL>(true).do DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_array_struct_do_field_nested_while",
				"WHILE ARRAY<STRUCT<do BOOL>>[STRUCT(true)][OFFSET(0)].do DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE ARRAY<STRUCT<do BOOL>>[STRUCT(true)][OFFSET(0)].do DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_eq_struct_do_field_nested_while",
				"WHILE x = STRUCT<do INT64>(1).do DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE x = STRUCT<do INT64>(1).do DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_nested_struct_do_field_nested_while",
				"WHILE STRUCT<s STRUCT<do BOOL>>(STRUCT(true)).s.do DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE STRUCT<s STRUCT<do BOOL>>(STRUCT(true)).s.do DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"repeat_paren_while_struct_do_nested_repeat",
				"REPEAT (SELECT 1); WHILE STRUCT<do BOOL>(true).do DO REPEAT SELECT 1; UNTIL true END REPEAT; END WHILE; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); WHILE STRUCT<do BOOL>(true).do DO REPEAT SELECT 1; UNTIL true END REPEAT; END WHILE; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"while_lt_do_and_do_gt_zero",
				"WHILE a < do AND do > 0 DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE a < do AND do > 0 DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_lt_do_without_gt",
				"WHILE a < do DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE a < do DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_do_gt_and_struct_lt",
				"WHILE do > 0 AND STRUCT<x INT64>(1).x < 2 DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE do > 0 AND STRUCT<x INT64>(1).x < 2 DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_range_type_ctor",
				"WHILE RANGE<DATE> '[2020-01-01, 2020-01-02)' IS NOT NULL DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE RANGE<DATE> '[2020-01-01, 2020-01-02)' IS NOT NULL DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_struct_array_and_unreserved_field_names",
				"WHILE STRUCT<begin INT64, repeat INT64, while INT64, loop INT64, do ARRAY<INT64>>(1, 2, 3, 4, [1]).do[OFFSET(0)] = 1 DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE STRUCT<begin INT64, repeat INT64, while INT64, loop INT64, do ARRAY<INT64>>(1, 2, 3, 4, [1]).do[OFFSET(0)] = 1 DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"if_call_plus_case_then_before_semi",
				"IF(a, 1, 2) + CASE WHEN x THEN 1 ELSE 2 END; SELECT 2;",
				[]string{"IF(a, 1, 2) + CASE WHEN x THEN 1 ELSE 2 END;", "SELECT 2;"},
			},
			{
				"if_paren_cond_with_case_then_stmt",
				"IF (a) AND CASE WHEN x THEN true ELSE false END THEN SELECT 1; END IF; SELECT 2;",
				[]string{"IF (a) AND CASE WHEN x THEN true ELSE false END THEN SELECT 1; END IF;", "SELECT 2;"},
			},
			{
				"repeat_paren_if_case_then_nested_repeat",
				"REPEAT (SELECT 1); IF CASE WHEN a THEN true ELSE false END THEN REPEAT SELECT 1; UNTIL true END REPEAT; END IF; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); IF CASE WHEN a THEN true ELSE false END THEN REPEAT SELECT 1; UNTIL true END REPEAT; END IF; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"repeat_paren_for_in_do_nested_repeat",
				"REPEAT (SELECT 1); FOR r IN (SELECT 1 AS a) DO REPEAT SELECT 1; UNTIL true END REPEAT; END FOR; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); FOR r IN (SELECT 1 AS a) DO REPEAT SELECT 1; UNTIL true END REPEAT; END FOR; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"repeat_paren_while_do_nested_repeat",
				"REPEAT (SELECT 1); WHILE do DO REPEAT SELECT 1; UNTIL true END REPEAT; END WHILE; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); WHILE do DO REPEAT SELECT 1; UNTIL true END REPEAT; END WHILE; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"repeat_paren_case_stmt_end_case_nested_repeat",
				"REPEAT (SELECT 1); CASE x WHEN 1 THEN REPEAT SELECT 1; UNTIL true END REPEAT; END CASE; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); CASE x WHEN 1 THEN REPEAT SELECT 1; UNTIL true END REPEAT; END CASE; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"repeat_paren_while_entered_body_do_repeat",
				"REPEAT (SELECT 1); WHILE a < b DO SELECT do repeat FROM t; END WHILE; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); WHILE a < b DO SELECT do repeat FROM t; END WHILE; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"while_pipe_query_from_do_while",
				"WHILE x > 0 DO (SELECT 1) |> WHERE true; SELECT * FROM do while; END WHILE; SELECT 2;",
				[]string{"WHILE x > 0 DO (SELECT 1) |> WHERE true; SELECT * FROM do while; END WHILE;", "SELECT 2;"},
			},
			{
				"while_pipe_query_select_do_while",
				"WHILE x > 0 DO (SELECT 1) |> WHERE true; SELECT do while FROM t; END WHILE; SELECT 2;",
				[]string{"WHILE x > 0 DO (SELECT 1) |> WHERE true; SELECT do while FROM t; END WHILE;", "SELECT 2;"},
			},
			{
				"while_pipe_double_paren_query_select_do_while",
				"WHILE x > 0 DO ((SELECT 1)) |> WHERE true; SELECT do while FROM t; END WHILE; SELECT 2;",
				[]string{"WHILE x > 0 DO ((SELECT 1)) |> WHERE true; SELECT do while FROM t; END WHILE;", "SELECT 2;"},
			},
			{
				"repeat_paren_while_pipe_query_do_repeat",
				"REPEAT (SELECT 1); WHILE x > 0 DO (SELECT 1) |> WHERE true; SELECT do repeat FROM t; END WHILE; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); WHILE x > 0 DO (SELECT 1) |> WHERE true; SELECT do repeat FROM t; END WHILE; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"while_interval_do_day_nested_while",
				"WHILE CURRENT_DATE() + INTERVAL do DAY > DATE '2020-01-01' DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE CURRENT_DATE() + INTERVAL do DAY > DATE '2020-01-01' DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_interval_do_hour_nested_while",
				"WHILE CURRENT_TIMESTAMP() + INTERVAL do HOUR > TIMESTAMP '2020-01-01 00:00:00' DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE CURRENT_TIMESTAMP() + INTERVAL do HOUR > TIMESTAMP '2020-01-01 00:00:00' DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_interval_do_month_nested_while",
				"WHILE MAKE_INTERVAL(1) + INTERVAL do MONTH > MAKE_INTERVAL(2) DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE MAKE_INTERVAL(1) + INTERVAL do MONTH > MAKE_INTERVAL(2) DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_interval_do_call_day_nested_while",
				"WHILE CURRENT_DATE() + INTERVAL do() DAY > DATE '2020-01-01' DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE CURRENT_DATE() + INTERVAL do() DAY > DATE '2020-01-01' DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"while_interval_do_nanosecond_nested_while",
				"WHILE CURRENT_TIMESTAMP() + INTERVAL do NANOSECOND > TIMESTAMP '2020-01-01 00:00:00' DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE; SELECT 2;",
				[]string{"WHILE CURRENT_TIMESTAMP() + INTERVAL do NANOSECOND > TIMESTAMP '2020-01-01 00:00:00' DO WHILE y > 0 DO SET y = 0; END WHILE; END WHILE;", "SELECT 2;"},
			},
			{
				"repeat_paren_while_interval_do_day_nested_repeat",
				"REPEAT (SELECT 1); WHILE CURRENT_DATE() + INTERVAL do DAY > DATE '2020-01-01' DO REPEAT SELECT 1; UNTIL true END REPEAT; END WHILE; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); WHILE CURRENT_DATE() + INTERVAL do DAY > DATE '2020-01-01' DO REPEAT SELECT 1; UNTIL true END REPEAT; END WHILE; UNTIL true END REPEAT;", "SELECT 2;"},
			},
			{
				"while_do_labeled_day_loop",
				"WHILE do DO day: LOOP LEAVE day; END LOOP day; END WHILE; SELECT 2;",
				[]string{"WHILE do DO day: LOOP LEAVE day; END LOOP day; END WHILE;", "SELECT 2;"},
			},
			{
				"malformed_if_call_struct_then_field",
				"IF(a, 1, 2) = STRUCT<then INT64>(1).then; SELECT 2;",
				[]string{"IF(a, 1, 2) = STRUCT<then INT64>(1).then;", "SELECT 2;"},
			},
			{
				"malformed_if_stmt_struct_case_field",
				"IF (a) AND STRUCT<case BOOL>(true).case THEN SELECT 1; END IF; SELECT 2;",
				[]string{"IF (a) AND STRUCT<case BOOL>(true).case THEN SELECT 1; END IF;", "SELECT 2;"},
			},
			{
				"malformed_repeat_unclosed_inner_repeat_inside_if",
				"REPEAT (SELECT 1); IF true THEN REPEAT SELECT 1; END IF; UNTIL true END REPEAT; SELECT 2;",
				[]string{"REPEAT (SELECT 1); IF true THEN REPEAT SELECT 1; END IF; UNTIL true END REPEAT;", "SELECT 2;"},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				spans := syntax.SplitStatements(tc.src, nil)
				require.Len(t, spans, len(tc.wantSpans))
				for i, sp := range spans {
					assert.True(t, sp.HasSemicolon)
					assert.Equal(t, tc.wantSpans[i], tc.src[sp.Start:sp.End])
				}
			})
		}
	})
}
