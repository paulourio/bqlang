package analysis_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/internal/syntax"
)

func TestSignatureHelpBasicAndArgumentAdvancement(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	uri := "file:///sig_basic.sql"
	sql := "SELECT DATE_TRUNC(event_date, DAY) FROM events WHERE event_date = '2026-01-01'"
	eng.OpenDocument(uri, sql, 1, nil)

	// Arg 0: right after "("
	sh0, err := eng.SignatureHelp(t.Context(), uri, utf16PosAfter(t, sql, "DATE_TRUNC("), false)
	require.NoError(t, err)
	require.NotNil(t, sh0)
	require.GreaterOrEqual(t, len(sh0.Signatures), 3)
	require.NotNil(t, sh0.ActiveSignature)
	require.NotNil(t, sh0.ActiveParameter)
	assert.Equal(t, 0, *sh0.ActiveParameter)
	assert.Contains(t, sh0.Signatures[*sh0.ActiveSignature].Label, "DATE_TRUNC(")
	require.NotNil(t, sh0.Signatures[*sh0.ActiveSignature].Documentation)
	assert.Contains(t, sh0.Signatures[*sh0.ActiveSignature].Documentation.Value, "Truncates")

	// Arg 1: right after ", "
	sh1, err := eng.SignatureHelp(t.Context(), uri, utf16PosAfter(t, sql, "event_date, "), false)
	require.NoError(t, err)
	require.NotNil(t, sh1)
	require.NotNil(t, sh1.ActiveParameter)
	assert.Equal(t, 1, *sh1.ActiveParameter)
	activeSig := sh1.Signatures[*sh1.ActiveSignature]
	require.GreaterOrEqual(t, len(activeSig.Parameters), 2)
	require.NotNil(t, activeSig.Parameters[1].Documentation)
	assert.Contains(t, activeSig.Parameters[1].Documentation.Value, "granularity")
}

func TestSignatureHelpIncompleteCallAndNestedCalls(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	uri := "file:///sig_incomplete.sql"

	// Incomplete SQL while user is actively typing inside nested calls + string/array commas.
	sql := "SELECT COALESCE(SUBSTR('a,b,c', 1, "
	eng.OpenDocument(uri, sql, 1, nil)

	sh, err := eng.SignatureHelp(t.Context(), uri, utf16PosAfter(t, sql, "1, "), false)
	require.NoError(t, err)
	require.NotNil(t, sh)
	require.NotEmpty(t, sh.Signatures)
	assert.Contains(t, sh.Signatures[0].Label, "SUBSTR(")
	require.NotNil(t, sh.ActiveParameter)
	assert.Equal(t, 2, *sh.ActiveParameter, "comma inside string literal must not increment arg index")

	// Array literal containing commas inside an outer call.
	sqlArr := "SELECT COALESCE(ARRAY_LENGTH([1, 2, 3]), "
	eng.OpenDocument(uri, sqlArr, 2, nil)

	shArr, err := eng.SignatureHelp(t.Context(), uri, utf16PosAfter(t, sqlArr, "]), "), false)
	require.NoError(t, err)
	require.NotNil(t, shArr)
	assert.Contains(t, shArr.Signatures[0].Label, "COALESCE(")

	// Parenthesized grouping inside a function call falls back to the enclosing function.
	sqlGroup := "SELECT ROUND((1 + 2), "
	eng.OpenDocument(uri, sqlGroup, 3, nil)
	shGroup, err := eng.SignatureHelp(t.Context(), uri, utf16PosAfter(t, sqlGroup, "(1 + 2), "), false)
	require.NoError(t, err)
	require.NotNil(t, shGroup)
	assert.Contains(t, shGroup.Signatures[0].Label, "ROUND(")
	assert.Equal(t, 1, *shGroup.ActiveParameter)

	// Inside unclosed grouping parens within a function argument: still shows enclosing function at arg 0.
	sqlInnerGroup := "SELECT ROUND((1 + "
	eng.OpenDocument(uri, sqlInnerGroup, 4, nil)
	shInner, err := eng.SignatureHelp(t.Context(), uri, utf16PosAfter(t, sqlInnerGroup, "(1 + "), false)
	require.NoError(t, err)
	require.NotNil(t, shInner)
	assert.Contains(t, shInner.Signatures[0].Label, "ROUND(")
	assert.Equal(t, 0, *shInner.ActiveParameter)
}

func TestSignatureHelpOverloadSelectionAndVariadic(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	uri := "file:///sig_overloads.sql"

	// 3-arg DATE_TRUNC selects the 3-parameter TIMESTAMP overload even when cursor is on arg 0.
	sql3 := "SELECT DATE_TRUNC(ts, DAY, 'UTC')"
	eng.OpenDocument(uri, sql3, 1, nil)

	sh3, err := eng.SignatureHelp(t.Context(), uri, utf16PosAfter(t, sql3, "DATE_TRUNC("), false)
	require.NoError(t, err)
	require.NotNil(t, sh3)
	require.NotNil(t, sh3.ActiveSignature)
	active := sh3.Signatures[*sh3.ActiveSignature]
	assert.Len(t, active.Parameters, 3)
	assert.Contains(t, active.Label, "time_zone")

	// Variadic CONCAT with 5 arguments clamps ActiveParameter to valid parameter bounds.
	sqlVar := "SELECT CONCAT('a', 'b', 'c', 'd', 'e')"
	eng.OpenDocument(uri, sqlVar, 2, nil)
	shVar, err := eng.SignatureHelp(t.Context(), uri, utf16PosAfter(t, sqlVar, "'d', "), false)
	require.NoError(t, err)
	require.NotNil(t, shVar)
	require.NotNil(t, shVar.ActiveParameter)
	assert.Less(t, *shVar.ActiveParameter, len(shVar.Signatures[*shVar.ActiveSignature].Parameters))
}

func TestSignatureHelpSpecialSyntaxAndSafePrefix(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	uri := "file:///sig_special.sql"

	// EXTRACT(part FROM expr)
	sqlExt := "SELECT EXTRACT(DAY FROM event_date)"
	eng.OpenDocument(uri, sqlExt, 1, nil)

	shExt0, err := eng.SignatureHelp(t.Context(), uri, utf16PosAfter(t, sqlExt, "EXTRACT("), false)
	require.NoError(t, err)
	require.NotNil(t, shExt0)
	assert.Equal(t, 0, *shExt0.ActiveParameter)

	shExt1, err := eng.SignatureHelp(t.Context(), uri, utf16PosAfter(t, sqlExt, "FROM "), false)
	require.NoError(t, err)
	require.NotNil(t, shExt1)
	assert.Equal(t, 1, *shExt1.ActiveParameter)

	// CAST(expr AS type FORMAT fmt)
	sqlCast := "SELECT CAST('2026-01-01' AS DATE FORMAT 'YYYY-MM-DD')"
	eng.OpenDocument(uri, sqlCast, 2, nil)

	shCast0, err := eng.SignatureHelp(t.Context(), uri, utf16PosAfter(t, sqlCast, "CAST("), false)
	require.NoError(t, err)
	require.NotNil(t, shCast0)
	assert.Equal(t, 0, *shCast0.ActiveParameter)

	shCast1, err := eng.SignatureHelp(t.Context(), uri, utf16PosAfter(t, sqlCast, "AS "), false)
	require.NoError(t, err)
	require.NotNil(t, shCast1)
	assert.Equal(t, 1, *shCast1.ActiveParameter)

	shCast2, err := eng.SignatureHelp(t.Context(), uri, utf16PosAfter(t, sqlCast, "FORMAT "), false)
	require.NoError(t, err)
	require.NotNil(t, shCast2)
	assert.Equal(t, 2, *shCast2.ActiveParameter)

	// SAFE.PARSE_DATE(...)
	sqlSafe := "SELECT SAFE.PARSE_DATE('%Y-%m-%d', '2026-01-01')"
	eng.OpenDocument(uri, sqlSafe, 3, nil)

	shSafe, err := eng.SignatureHelp(t.Context(), uri, utf16PosAfter(t, sqlSafe, "SAFE.PARSE_DATE("), false)
	require.NoError(t, err)
	require.NotNil(t, shSafe)
	assert.True(t, strings.HasPrefix(shSafe.Signatures[0].Label, "SAFE.PARSE_DATE("))
	require.NotNil(t, shSafe.Signatures[0].Documentation)
	assert.Contains(t, shSafe.Signatures[0].Documentation.Value, "SAFE.")
}

func TestSignatureHelpDottedNamespacesAndNamedArguments(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	uri := "file:///sig_ai.sql"

	sql := "SELECT AI.GENERATE(prompt => 'Summarize', endpoint => 'gemini-2.5-flash')"
	eng.OpenDocument(uri, sql, 1, nil)

	sh, err := eng.SignatureHelp(t.Context(), uri, utf16PosAfter(t, sql, "endpoint => "), false)
	require.NoError(t, err)
	require.NotNil(t, sh)
	require.NotNil(t, sh.ActiveSignature)
	require.NotNil(t, sh.ActiveParameter)
	sig := sh.Signatures[*sh.ActiveSignature]
	assert.Contains(t, sig.Label, "AI.GENERATE(")
	paramLabel, ok := sig.Parameters[*sh.ActiveParameter].Label.(string)
	require.True(t, ok)
	assert.Contains(t, strings.ToLower(paramLabel), "endpoint")

	// Dotted AEAD function with per-parameter docs.
	sqlAEAD := "SELECT AEAD.ENCRYPT(ks, pt, ad)"
	eng.OpenDocument(uri, sqlAEAD, 2, nil)
	shAEAD, err := eng.SignatureHelp(t.Context(), uri, utf16PosAfter(t, sqlAEAD, "ks, "), false)
	require.NoError(t, err)
	require.NotNil(t, shAEAD)
	assert.Equal(t, 1, *shAEAD.ActiveParameter)
	require.NotNil(t, shAEAD.Signatures[0].Parameters[1].Documentation)
	assert.Contains(t, shAEAD.Signatures[0].Parameters[1].Documentation.Value, "plaintext")
}

func TestSignatureHelpUTF16LabelOffsets(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	uri := "file:///sig_offsets.sql"
	sql := "SELECT SUBSTR('hello', 1, 3)"
	eng.OpenDocument(uri, sql, 1, nil)

	sh, err := eng.SignatureHelp(t.Context(), uri, utf16PosAfter(t, sql, "SUBSTR("), true)
	require.NoError(t, err)
	require.NotNil(t, sh)
	sig := sh.Signatures[*sh.ActiveSignature]
	require.Len(t, sig.Parameters, 3)

	// Verify each [start, end] offset slices back to the expected parameter name.
	expected := []string{"value", "position", "length"}
	for i, want := range expected {
		pair, ok := sig.Parameters[i].Label.([2]uint32)
		require.True(t, ok, "expected [2]uint32 offset pair for param %d", i)
		require.Less(t, pair[0], pair[1])
		assert.Equal(t, want, sig.Label[pair[0]:pair[1]])
	}
}

func TestSignatureHelpLocalUDFAndJinjaAndNegativeControls(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)

	// Local CREATE TEMP FUNCTION.
	uriUDF := "file:///sig_udf.sql"
	sqlUDF := "CREATE TEMP FUNCTION add_tax(amount NUMERIC, rate FLOAT64) RETURNS NUMERIC AS (amount * (1 + rate));\nSELECT add_tax(100, 0.1);"
	eng.OpenDocument(uriUDF, sqlUDF, 1, nil)

	shUDF, err := eng.SignatureHelp(t.Context(), uriUDF, utf16PosAfter(t, sqlUDF, "add_tax(100, "), false)
	require.NoError(t, err)
	require.NotNil(t, shUDF)
	assert.Contains(t, shUDF.Signatures[0].Label, "add_tax(amount NUMERIC, rate FLOAT64)")
	assert.Equal(t, 1, *shUDF.ActiveParameter)

	// Jinja2 / dbt macros.
	uriJinja := "file:///sig_jinja.sql.j2"
	sqlJinja := "{% macro fmt_id(prefix, col) %}{{ prefix }}_{{ col }}{% endmacro %}\nSELECT id FROM {{ source('raw', 'orders') }} WHERE id = {{ fmt_id('usr', 'id') }}"
	eng.OpenDocument(uriJinja, sqlJinja, 1, nil)

	shSrc, err := eng.SignatureHelp(t.Context(), uriJinja, utf16PosAfter(t, sqlJinja, "source('raw', "), false)
	require.NoError(t, err)
	require.NotNil(t, shSrc)
	assert.Contains(t, shSrc.Signatures[0].Label, "source(source_name, table_name)")
	assert.Equal(t, 1, *shSrc.ActiveParameter)

	shMac, err := eng.SignatureHelp(t.Context(), uriJinja, utf16PosAfter(t, sqlJinja, "fmt_id('usr', "), false)
	require.NoError(t, err)
	require.NotNil(t, shMac)
	assert.Contains(t, shMac.Signatures[0].Label, "fmt_id(prefix, col)")
	assert.Equal(t, 1, *shMac.ActiveParameter)

	// Negative controls: non-function parens, comments, outside calls return nil.
	uriNeg := "file:///sig_neg.sql"
	sqlNeg := "SELECT a FROM (SELECT 1 AS a) WHERE (a = 1) AND b IN (1, 2) -- DATE_TRUNC(x, "
	eng.OpenDocument(uriNeg, sqlNeg, 1, nil)

	for _, needle := range []string{"FROM (", "WHERE (", "IN (1, ", "-- DATE_TRUNC(x, "} {
		shNeg, nErr := eng.SignatureHelp(t.Context(), uriNeg, utf16PosAfter(t, sqlNeg, needle), false)
		require.NoError(t, nErr)
		assert.Nil(t, shNeg, "expected nil signatureHelp at %q", needle)
	}

	// Cursor at position (0,0) before anything returns nil.
	shZero, err := eng.SignatureHelp(t.Context(), uriNeg, syntax.Position{Line: 0, Character: 0}, false)
	require.NoError(t, err)
	assert.Nil(t, shZero)
}

func TestEnrichedFunctionHoverAndCompletionDocs(t *testing.T) {
	t.Parallel()

	_, eng := newTestStoreAndEngine(t)
	uri := "file:///enriched_docs.sql"

	// Hover on resolved function + SAFE. prefix + unresolved BigQuery AI function.
	sql := "SELECT DATE_TRUNC(event_date, DAY), SAFE.PARSE_DATE('%Y-%m-%d', tenant_id), AI.GENERATE(prompt => 'hi') FROM events WHERE event_date = '2026-01-01'"
	eng.OpenDocument(uri, sql, 1, nil)

	hDT, err := eng.Hover(t.Context(), uri, utf16Pos(t, sql, "DATE_TRUNC"))
	require.NoError(t, err)
	require.NotNil(t, hDT)
	assert.Contains(t, hDT.Contents.Value, "Truncates")
	assert.Contains(t, hDT.Contents.Value, "**Returns:**")
	assert.Contains(t, hDT.Contents.Value, "BigQuery Documentation")

	hSafe, err := eng.Hover(t.Context(), uri, utf16Pos(t, sql, "PARSE_DATE"))
	require.NoError(t, err)
	require.NotNil(t, hSafe)
	assert.Contains(t, hSafe.Contents.Value, "SAFE.PARSE_DATE")
	assert.Contains(t, hSafe.Contents.Value, "NULL")

	hAI, err := eng.Hover(t.Context(), uri, utf16Pos(t, sql, "GENERATE"))
	require.NoError(t, err)
	require.NotNil(t, hAI)
	assert.Contains(t, hAI.Contents.Value, "AI.GENERATE")
	assert.Contains(t, hAI.Contents.Value, "prompt")

	// Hover on a table column named "date" in a broken query must NOT falsely show DATE() function docs.
	uriCol := "file:///col_named_date.sql"
	sqlCol := "SELECT date FROM nonexistent_table_xyz"
	eng.OpenDocument(uriCol, sqlCol, 1, nil)
	hCol, err := eng.Hover(t.Context(), uriCol, utf16Pos(t, sqlCol, "date"))
	require.NoError(t, err)
	if hCol != nil {
		assert.NotContains(t, hCol.Contents.Value, "### Function `DATE`")
	}

	// Completion items for BigQuery functions carry signature detail & markdown docs.
	uriComp := "file:///comp_fn_docs.sql"
	sqlComp := "SELECT "
	eng.OpenDocument(uriComp, sqlComp, 1, nil)

	list, err := eng.Complete(t.Context(), uriComp, utf16PosAfter(t, sqlComp, "SELECT "))
	require.NoError(t, err)
	require.NotNil(t, list)

	foundDT := false
	for _, it := range list.Items {
		if it.Label == "DATE_TRUNC" {
			foundDT = true
			assert.Contains(t, it.Detail, "DATE_TRUNC(")
			require.NotNil(t, it.Documentation)
			assert.Equal(t, "markdown", it.Documentation.Kind)
			assert.Contains(t, it.Documentation.Value, "Truncates")
		}
	}
	assert.True(t, foundDT, "DATE_TRUNC must appear in completions with docs")

	// Namespace completion for AI. includes AI.GENERATE with full docs.
	uriAI := "file:///comp_ai_ns.sql"
	sqlAI := "SELECT AI."
	eng.OpenDocument(uriAI, sqlAI, 1, nil)

	aiList, err := eng.Complete(t.Context(), uriAI, utf16PosAfter(t, sqlAI, "AI."))
	require.NoError(t, err)
	require.NotNil(t, aiList)

	foundGen := false
	for _, it := range aiList.Items {
		if it.Label == "GENERATE" {
			foundGen = true
			assert.Contains(t, it.Detail, "AI.GENERATE")
			require.NotNil(t, it.Documentation)
			assert.NotEmpty(t, it.Documentation.Value)
		}
	}
	assert.True(t, foundGen, "AI. namespace completion must include GENERATE with docs")
}
