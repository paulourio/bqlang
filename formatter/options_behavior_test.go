package formatter_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/formatter"
)

func formatWithOpts(t *testing.T, sql string, opts *formatter.PrintOptions) string {
	t.Helper()

	require.NoError(t, bqlang.Init())

	out, err := formatter.Format(sql, opts)
	require.NoError(t, err, "first format pass failed for SQL:\n%s", sql)

	again, err := formatter.Format(out, opts)
	require.NoError(t, err, "second format pass failed for formatted SQL:\n%s", out)
	assert.Equal(t, out, again, "formatting must be idempotent")

	return out
}

func cloneOpts(mutate func(*formatter.PrintOptions)) *formatter.PrintOptions {
	opts := formatter.DefaultPrintOptions()
	if mutate != nil {
		mutate(opts)
	}

	return opts
}

func TestPrintOptionsFieldCoverage(t *testing.T) {
	t.Parallel()

	covered := map[string]bool{
		"SoftMaxColumns":                 true,
		"NewlineBeforeClause":            true,
		"AlignLogicalWithClauses":        true,
		"AlignTrailingComments":          true,
		"ColumnListTrailingComma":        true,
		"Indentation":                    true,
		"IndentCaseWhen":                 true,
		"IndentWithClause":               true,
		"IndentWithEntries":              true,
		"MinJoinsToSeparateInBlocks":     true,
		"MaxColumnsForSingleLineSelect":  true,
		"MaxParamsForSingleLineFunction": true,
		"FunctionCatalog":                true,
		"BuiltinFunctionNameStyle":       true,
		"FunctionNameStyle":              true,
		"IdentifierStyle":                true,
		"KeywordStyle":                   true,
		"TypeStyle":                      true,
		"BoolStyle":                      true,
		"HexStyle":                       true,
		"NumericStyle":                   true,
		"NullStyle":                      true,
		"PseudoColumnStyle":              true,
		"TableNameStyle":                 true,
		"QueryParameterStyle":            true,
		"SystemVariableStyle":            true,
		"BytesStyle":                     true,
		"StringStyle":                    true,
	}

	typ := reflect.TypeFor[formatter.PrintOptions]()
	require.Equal(t, 28, typ.NumField(), "update options_behavior_test.go when PrintOptions fields change")

	for field := range typ.Fields() {
		assert.True(t, covered[field.Name], "PrintOptions field %q must be covered by options_behavior_test.go", field.Name)
	}
}

func TestPrintOptionsLayout(t *testing.T) {
	t.Parallel()

	t.Run("SoftMaxColumns", func(t *testing.T) {
		t.Parallel()

		sql := "SELECT alpha_col + beta_col + gamma_col + delta_col + epsilon_col FROM my_table"

		wide := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.SoftMaxColumns = 120
		}))
		assert.Equal(t,
			"SELECT alpha_col + beta_col + gamma_col + delta_col + epsilon_col\n  FROM my_table\n",
			wide,
		)

		narrow := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.SoftMaxColumns = 30
		}))
		assert.Equal(t,
			"SELECT alpha_col + beta_col + gamma_col + delta_col +\n       epsilon_col\n  FROM my_table\n",
			narrow,
		)
		assert.NotEqual(t, wide, narrow, "tightening SoftMaxColumns must break long expressions")

		zero := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.SoftMaxColumns = 0
		}))
		assert.Equal(t, narrow, zero, "SoftMaxColumns=0 enforces the minimum wrapping budget cleanly")
	})

	t.Run("NewlineBeforeClause", func(t *testing.T) {
		t.Parallel()

		sql := "SELECT a, b FROM t WHERE x = 1"

		withNewline := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.NewlineBeforeClause = true
		}))
		assert.Equal(t, "SELECT a, b\n  FROM t\n WHERE x = 1\n", withNewline)

		inline := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.NewlineBeforeClause = false
		}))
		assert.Equal(t, "SELECT a, b FROM t\n WHERE x = 1\n", inline)
		assert.NotEqual(t, withNewline, inline)
	})

	t.Run("AlignLogicalWithClauses", func(t *testing.T) {
		t.Parallel()

		sqlAnd := "SELECT a FROM t WHERE x = 1 AND y = 2 AND z = 3"

		alignedAnd := formatWithOpts(t, sqlAnd, cloneOpts(func(o *formatter.PrintOptions) {
			o.AlignLogicalWithClauses = true
		}))
		assert.Equal(t,
			"SELECT a\n  FROM t\n WHERE x = 1\n   AND y = 2\n   AND z = 3\n",
			alignedAnd,
		)

		unalignedAnd := formatWithOpts(t, sqlAnd, cloneOpts(func(o *formatter.PrintOptions) {
			o.AlignLogicalWithClauses = false
		}))
		assert.Equal(t,
			"SELECT a\n  FROM t\n WHERE     x = 1\n       AND y = 2\n       AND z = 3\n",
			unalignedAnd,
		)
		assert.NotEqual(t, alignedAnd, unalignedAnd)

		sqlMixed := "SELECT a FROM t WHERE x = 1 AND y = 2 OR z = 3"

		alignedMixed := formatWithOpts(t, sqlMixed, cloneOpts(func(o *formatter.PrintOptions) {
			o.AlignLogicalWithClauses = true
		}))
		assert.Equal(t,
			"SELECT a\n  FROM t\n WHERE     x = 1\n       AND y = 2\n    OR z = 3\n",
			alignedMixed,
		)

		unalignedMixed := formatWithOpts(t, sqlMixed, cloneOpts(func(o *formatter.PrintOptions) {
			o.AlignLogicalWithClauses = false
		}))
		assert.Equal(t,
			"SELECT a\n  FROM t\n WHERE    x = 1 AND y = 2\n       OR z = 3\n",
			unalignedMixed,
		)
		assert.NotEqual(t, alignedMixed, unalignedMixed)
	})

	t.Run("AlignTrailingComments", func(t *testing.T) {
		t.Parallel()

		sql := "SELECT a, -- first\n       long_column_name -- second\n  FROM t"

		aligned := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.AlignTrailingComments = true
		}))
		assert.Equal(t,
			"SELECT a,                -- first\n       long_column_name  -- second\n  FROM t\n",
			aligned,
		)

		unaligned := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.AlignTrailingComments = false
		}))
		assert.Equal(t,
			"SELECT a,  -- first\n       long_column_name  -- second\n  FROM t\n",
			unaligned,
		)
		assert.NotEqual(t, aligned, unaligned)
	})

	t.Run("ColumnListTrailingComma", func(t *testing.T) {
		t.Parallel()

		multiNoTrailing := "SELECT a, b, c, d, e FROM t"
		multiWithTrailing := "SELECT a, b, c, d, e, FROM t"
		singleLine := "SELECT a, b FROM t"

		wantWithComma := "SELECT a,\n       b,\n       c,\n       d,\n       e,\n  FROM t\n"
		wantNoComma := "SELECT a,\n       b,\n       c,\n       d,\n       e\n  FROM t\n"

		// Always adds trailing comma on multiline SQL SELECT lists.
		alwaysOpts := cloneOpts(func(o *formatter.PrintOptions) {
			o.ColumnListTrailingComma = formatter.Always
		})
		assert.Equal(t, wantWithComma, formatWithOpts(t, multiNoTrailing, alwaysOpts))
		assert.Equal(t, wantWithComma, formatWithOpts(t, multiWithTrailing, alwaysOpts))

		// Single-line SELECT never receives a trailing comma even under Always.
		assert.Equal(t, "SELECT a, b\n  FROM t\n", formatWithOpts(t, singleLine, alwaysOpts))

		// Never and UnspecifiedWhen always omit trailing commas.
		for _, mode := range []formatter.When{
			formatter.Never,
			formatter.UnspecifiedWhen,
		} {
			opts := cloneOpts(func(o *formatter.PrintOptions) {
				o.ColumnListTrailingComma = mode
			})
			assert.Equal(t, wantNoComma, formatWithOpts(t, multiNoTrailing, opts), "mode=%q no input comma", mode)
			assert.Equal(t, wantNoComma, formatWithOpts(t, multiWithTrailing, opts), "mode=%q with input comma", mode)
		}

		// Auto preserves a trailing comma when present and does not add one when absent.
		autoOpts := cloneOpts(func(o *formatter.PrintOptions) {
			o.ColumnListTrailingComma = formatter.Auto
		})
		assert.Equal(t, wantNoComma, formatWithOpts(t, multiNoTrailing, autoOpts))
		assert.Equal(t, wantWithComma, formatWithOpts(t, multiWithTrailing, autoOpts))
		assert.Equal(t, "SELECT a, b\n  FROM t\n", formatWithOpts(t, "SELECT a, b, FROM t", autoOpts))

		// Auto handles comments around the trailing comma and ignores commas inside comments.
		withCommentBeforeComma := "SELECT a, b, c, d, e /* note */, FROM t"
		assert.Equal(
			t,
			"SELECT a,\n       b,\n       c,\n       d,\n       e,  /* note */\n  FROM t\n",
			formatWithOpts(t, withCommentBeforeComma, autoOpts),
		)
		withCommaInsideComment := "SELECT a, b, c, d, e /* , */ FROM t"
		assert.Equal(
			t,
			"SELECT a,\n       b,\n       c,\n       d,\n       e  /* , */\n  FROM t\n",
			formatWithOpts(t, withCommaInsideComment, autoOpts),
		)

		// DML THEN RETURN and pipe SELECT honor Always, Auto, and Never.
		dmlNoComma := "DELETE FROM t WHERE id = 1 THEN RETURN a, b, c, d, e"
		dmlWithComma := "DELETE FROM t WHERE id = 1 THEN RETURN a, b, c, d, e,"
		dmlAlways := formatWithOpts(t, dmlNoComma, alwaysOpts)
		dmlNever := formatWithOpts(t, dmlWithComma, cloneOpts(func(o *formatter.PrintOptions) {
			o.ColumnListTrailingComma = formatter.Never
		}))
		assert.Contains(t, dmlAlways, "e,\n")
		assert.NotContains(t, dmlNever, "e,\n")
		assert.NotContains(t, formatWithOpts(t, dmlNoComma, autoOpts), "e,\n")
		assert.Contains(t, formatWithOpts(t, dmlWithComma, autoOpts), "e,\n")

		pipeNoComma := "FROM t |> SELECT a, b, c, d, e"
		pipeWithComma := "FROM t |> SELECT a, b, c, d, e,"
		assert.NotContains(t, formatWithOpts(t, pipeNoComma, autoOpts), "e,\n")
		assert.Contains(t, formatWithOpts(t, pipeWithComma, autoOpts), "e,\n")

		// Non-leakage: GQL RETURN must not emit an illegal trailing comma even under Always.
		gql := "GRAPH g MATCH (n) RETURN n.a, n.b, n.c, n.d, n.e"
		gqlAlways := formatWithOpts(t, gql, alwaysOpts)
		assert.NotContains(t, gqlAlways, "n.e,")
	})

	t.Run("Indentation", func(t *testing.T) {
		t.Parallel()

		sql := "CREATE TABLE t (id INT64, name STRING)"

		indent2 := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.Indentation = 2
		}))
		assert.Equal(t, "CREATE TABLE t\n(\n  id   INT64,\n  name STRING\n)\n", indent2)

		indent4 := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.Indentation = 4
		}))
		assert.Equal(t, "CREATE TABLE t\n(\n    id   INT64,\n    name STRING\n)\n", indent4)

		// Indentation=0 falls back to the 2-space default width.
		indent0 := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.Indentation = 0
		}))
		assert.Equal(t, indent2, indent0)
		assert.NotEqual(t, indent2, indent4)
	})

	t.Run("IndentCaseWhen", func(t *testing.T) {
		t.Parallel()

		sqlExpr := "SELECT CASE WHEN x = 1 THEN 'a' ELSE 'b' END FROM t"

		indented := formatWithOpts(t, sqlExpr, cloneOpts(func(o *formatter.PrintOptions) {
			o.IndentCaseWhen = true
		}))
		assert.Equal(t,
			"SELECT CASE\n         WHEN x = 1 THEN 'a'\n                    ELSE 'b'\n       END\n  FROM t\n",
			indented,
		)

		flush := formatWithOpts(t, sqlExpr, cloneOpts(func(o *formatter.PrintOptions) {
			o.IndentCaseWhen = false
		}))
		assert.Equal(t,
			"SELECT CASE\n       WHEN x = 1 THEN 'a'\n                  ELSE 'b'\n       END\n  FROM t\n",
			flush,
		)
		assert.NotEqual(t, indented, flush)

		// Procedural CASE statement also respects IndentCaseWhen.
		procSQL := "CASE x WHEN 1 THEN SELECT 1; ELSE SELECT 2; END CASE;"
		procIndented := formatWithOpts(t, procSQL, cloneOpts(func(o *formatter.PrintOptions) {
			o.IndentCaseWhen = true
		}))
		procFlush := formatWithOpts(t, procSQL, cloneOpts(func(o *formatter.PrintOptions) {
			o.IndentCaseWhen = false
		}))
		assert.Equal(t,
			"CASE x\n  WHEN 1 THEN\n    SELECT 1;\n  ELSE\n    SELECT 2;\nEND CASE\n",
			procIndented,
		)
		assert.Equal(t,
			"CASE x\nWHEN 1 THEN\n  SELECT 1;\nELSE\n  SELECT 2;\nEND CASE\n",
			procFlush,
		)
	})

	t.Run("IndentWithClause", func(t *testing.T) {
		t.Parallel()

		sql := "WITH cte AS (SELECT 1 AS a) SELECT a FROM cte"

		indented := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IndentWithClause = true
			o.IndentWithEntries = false
		}))
		assert.Equal(t,
			"WITH\n  cte AS (\n  SELECT 1 AS a\n  )\nSELECT a\n  FROM cte\n",
			indented,
		)

		unindented := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IndentWithClause = false
			o.IndentWithEntries = false
		}))
		assert.Equal(t,
			"WITH\ncte AS (\nSELECT 1 AS a\n)\nSELECT a\n  FROM cte\n",
			unindented,
		)
		assert.NotEqual(t, indented, unindented)
	})

	t.Run("IndentWithEntries", func(t *testing.T) {
		t.Parallel()

		sql := "WITH cte AS (SELECT 1 AS a) SELECT a FROM cte"

		entriesIndented := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IndentWithClause = false
			o.IndentWithEntries = true
		}))
		assert.Equal(t,
			"WITH\ncte AS (\n  SELECT 1 AS a\n)\nSELECT a\n  FROM cte\n",
			entriesIndented,
		)

		entriesUnindented := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IndentWithClause = false
			o.IndentWithEntries = false
		}))
		assert.Equal(t,
			"WITH\ncte AS (\nSELECT 1 AS a\n)\nSELECT a\n  FROM cte\n",
			entriesUnindented,
		)

		bothIndented := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IndentWithClause = true
			o.IndentWithEntries = true
		}))
		assert.Equal(t,
			"WITH\n  cte AS (\n    SELECT 1 AS a\n  )\nSELECT a\n  FROM cte\n",
			bothIndented,
		)
		assert.NotEqual(t, entriesIndented, entriesUnindented)
		assert.NotEqual(t, bothIndented, entriesIndented)
	})

	t.Run("MinJoinsToSeparateInBlocks", func(t *testing.T) {
		t.Parallel()

		sql := "SELECT a.id FROM a JOIN b ON a.id = b.id JOIN c ON b.id = c.id"

		separated := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.MinJoinsToSeparateInBlocks = 2
		}))
		assert.Equal(t,
			"SELECT a.id\n  FROM a\n\n       JOIN\n       b\n       ON a.id = b.id\n\n       JOIN\n       c\n       ON b.id = c.id\n",
			separated,
		)

		compact := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.MinJoinsToSeparateInBlocks = 3
		}))
		assert.Equal(t,
			"SELECT a.id\n  FROM a\n       JOIN\n       b\n       ON a.id = b.id\n       JOIN\n       c\n       ON b.id = c.id\n",
			compact,
		)
		assert.NotEqual(t, separated, compact)
	})

	t.Run("MaxColumnsForSingleLineSelect", func(t *testing.T) {
		t.Parallel()

		sql := "SELECT a, b, c FROM t"

		single := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.MaxColumnsForSingleLineSelect = 3
		}))
		assert.Equal(t, "SELECT a, b, c\n  FROM t\n", single)

		multi := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.MaxColumnsForSingleLineSelect = 2
		}))
		assert.Equal(t, "SELECT a,\n       b,\n       c\n  FROM t\n", multi)
		assert.NotEqual(t, single, multi)
	})

	t.Run("MaxParamsForSingleLineFunction", func(t *testing.T) {
		t.Parallel()

		sql := "CREATE FUNCTION my_fn(a INT64, b INT64, c INT64) RETURNS INT64 AS (a + b + c)"

		single := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.MaxParamsForSingleLineFunction = 3
		}))
		assert.Equal(t,
			"CREATE FUNCTION my_fn(a INT64, b INT64, c INT64)\nRETURNS INT64\nAS (\n  a + b + c\n)\n",
			single,
		)

		multi := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.MaxParamsForSingleLineFunction = 2
		}))
		assert.Equal(t,
			"CREATE FUNCTION my_fn\n(\n  a INT64,\n  b INT64,\n  c INT64\n)\nRETURNS INT64\nAS (\n  a + b + c\n)\n",
			multi,
		)
		assert.NotEqual(t, single, multi)
	})
}

func TestPrintOptionsFunctionCatalogAndCasing(t *testing.T) {
	t.Parallel()

	t.Run("FunctionCatalog", func(t *testing.T) {
		t.Parallel()

		sql := "SELECT count(x), safe.parse_date('%Y', s), My_Udf(x) FROM t"

		bq := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.FunctionCatalog = formatter.BigQueryCatalog
			o.BuiltinFunctionNameStyle = formatter.UpperCase
			o.FunctionNameStyle = formatter.LowerCase
		}))
		assert.Equal(t,
			"SELECT COUNT(x),\n       SAFE.PARSE_DATE('%Y', s),\n       my_udf(x)\n  FROM t\n",
			bq,
		)

		empty := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.FunctionCatalog = formatter.EmptyCatalog
			o.BuiltinFunctionNameStyle = formatter.UpperCase
			o.FunctionNameStyle = formatter.LowerCase
		}))
		assert.Equal(t,
			"SELECT count(x),\n       safe.parse_date('%Y', s),\n       my_udf(x)\n  FROM t\n",
			empty,
		)
		assert.NotEqual(t, bq, empty, "EmptyCatalog must bypass BigQuery builtin catalog lookup")
	})

	t.Run("BuiltinFunctionNameStyle", func(t *testing.T) {
		t.Parallel()

		sql := "SELECT CoUnT(x), Safe.Parse_Date('%Y', s), My_Udf(x), `CoUnT`(x) FROM t"

		upper := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.FunctionCatalog = formatter.BigQueryCatalog
			o.BuiltinFunctionNameStyle = formatter.UpperCase
			o.FunctionNameStyle = formatter.AsIs
		}))
		assert.Equal(t,
			"SELECT COUNT(x),\n       SAFE.PARSE_DATE('%Y', s),\n       My_Udf(x),\n       `CoUnT`(x)\n  FROM t\n",
			upper,
		)

		lower := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.FunctionCatalog = formatter.BigQueryCatalog
			o.BuiltinFunctionNameStyle = formatter.LowerCase
			o.FunctionNameStyle = formatter.AsIs
		}))
		assert.Equal(t,
			"SELECT count(x),\n       safe.parse_date('%Y', s),\n       My_Udf(x),\n       `CoUnT`(x)\n  FROM t\n",
			lower,
		)

		asIs := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.FunctionCatalog = formatter.BigQueryCatalog
			o.BuiltinFunctionNameStyle = formatter.AsIs
			o.FunctionNameStyle = formatter.AsIs
		}))
		assert.Equal(t,
			"SELECT CoUnT(x),\n       Safe.Parse_Date('%Y', s),\n       My_Udf(x),\n       `CoUnT`(x)\n  FROM t\n",
			asIs,
		)

		unspec := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.FunctionCatalog = formatter.BigQueryCatalog
			o.BuiltinFunctionNameStyle = formatter.Unspecified
			o.FunctionNameStyle = formatter.AsIs
		}))
		assert.Equal(t, upper, unspec, "Unspecified BuiltinFunctionNameStyle uses canonical catalog case")

		// Non-leakage: non-builtin My_Udf and backticked `CoUnT` stay unchanged in all modes.
		for _, out := range []string{upper, lower, asIs, unspec} {
			assert.Contains(t, out, "My_Udf(x)")
			assert.Contains(t, out, "`CoUnT`(x)")
		}
	})

	t.Run("FunctionNameStyle", func(t *testing.T) {
		t.Parallel()

		sql := "SELECT count(x), My_Custom_Udf(x), my_ds.My_Udf(x), `KeepFn`(x) FROM t"

		upper := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.FunctionCatalog = formatter.BigQueryCatalog
			o.BuiltinFunctionNameStyle = formatter.UpperCase
			o.FunctionNameStyle = formatter.UpperCase
		}))
		assert.Equal(t,
			"SELECT COUNT(x),\n       MY_CUSTOM_UDF(x),\n       MY_DS.MY_UDF(x),\n       `KeepFn`(x)\n  FROM t\n",
			upper,
		)

		lower := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.FunctionCatalog = formatter.BigQueryCatalog
			o.BuiltinFunctionNameStyle = formatter.UpperCase
			o.FunctionNameStyle = formatter.LowerCase
		}))
		assert.Equal(t,
			"SELECT COUNT(x),\n       my_custom_udf(x),\n       my_ds.my_udf(x),\n       `KeepFn`(x)\n  FROM t\n",
			lower,
		)

		asIs := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.FunctionCatalog = formatter.BigQueryCatalog
			o.BuiltinFunctionNameStyle = formatter.UpperCase
			o.FunctionNameStyle = formatter.AsIs
		}))
		assert.Equal(t,
			"SELECT COUNT(x),\n       My_Custom_Udf(x),\n       my_ds.My_Udf(x),\n       `KeepFn`(x)\n  FROM t\n",
			asIs,
		)

		unspec := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.FunctionCatalog = formatter.BigQueryCatalog
			o.BuiltinFunctionNameStyle = formatter.UpperCase
			o.FunctionNameStyle = formatter.Unspecified
		}))
		assert.Equal(t, asIs, unspec)

		// Non-leakage: builtin COUNT and backticked `KeepFn` stay unchanged across FunctionNameStyle values.
		for _, out := range []string{upper, lower, asIs, unspec} {
			assert.Contains(t, out, "COUNT(x)")
			assert.Contains(t, out, "`KeepFn`(x)")
		}
	})
}

func TestPrintOptionsCasing(t *testing.T) {
	t.Parallel()

	t.Run("IdentifierStyle", func(t *testing.T) {
		t.Parallel()

		sql := "SELECT MyCol AS MyAlias, `KeepCase` FROM MyTbl WHERE OtherCol = 1"

		upper := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IdentifierStyle = formatter.UpperCase
		}))
		assert.Equal(t,
			"SELECT MYCOL AS MYALIAS, `KeepCase`\n  FROM MYTBL\n WHERE OTHERCOL = 1\n",
			upper,
		)

		lower := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IdentifierStyle = formatter.LowerCase
		}))
		assert.Equal(t,
			"SELECT mycol AS myalias, `KeepCase`\n  FROM mytbl\n WHERE othercol = 1\n",
			lower,
		)

		asIs := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IdentifierStyle = formatter.AsIs
		}))
		assert.Equal(t,
			"SELECT MyCol AS MyAlias, `KeepCase`\n  FROM MyTbl\n WHERE OtherCol = 1\n",
			asIs,
		)

		unspec := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IdentifierStyle = formatter.Unspecified
		}))
		assert.Equal(t, asIs, unspec)
	})

	t.Run("KeywordStyle", func(t *testing.T) {
		t.Parallel()

		sql := "select MyCol as MyAlias from MyTbl where Flag = TRUE order by MyCol desc"

		upper := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.KeywordStyle = formatter.UpperCase
		}))
		assert.Equal(t,
			"SELECT MyCol AS MyAlias\n  FROM MyTbl\n WHERE Flag = TRUE\n ORDER BY MyCol DESC\n",
			upper,
		)

		lower := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.KeywordStyle = formatter.LowerCase
		}))
		assert.Equal(t,
			"select MyCol as MyAlias\n  from MyTbl\n where Flag = TRUE\n order by MyCol desc\n",
			lower,
		)

		// Non-leakage: identifiers and BoolStyle literal TRUE are untouched by KeywordStyle.
		assert.Contains(t, lower, "MyCol")
		assert.Contains(t, lower, "MyAlias")
		assert.Contains(t, lower, "MyTbl")
		assert.Contains(t, lower, "TRUE")

		asIs := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.KeywordStyle = formatter.AsIs
		}))
		unspec := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.KeywordStyle = formatter.Unspecified
		}))
		assert.Equal(t, upper, asIs)
		assert.Equal(t, upper, unspec)
	})

	t.Run("TypeStyle", func(t *testing.T) {
		t.Parallel()

		sql := "SELECT CAST(MyCol AS String), CAST(OtherCol AS Int64) FROM t"

		upper := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.TypeStyle = formatter.UpperCase
		}))
		assert.Equal(t,
			"SELECT CAST(MyCol AS STRING),\n       CAST(OtherCol AS INT64)\n  FROM t\n",
			upper,
		)

		lower := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.TypeStyle = formatter.LowerCase
		}))
		assert.Equal(t,
			"SELECT CAST(MyCol AS string),\n       CAST(OtherCol AS int64)\n  FROM t\n",
			lower,
		)

		asIs := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.TypeStyle = formatter.AsIs
		}))
		assert.Equal(t,
			"SELECT CAST(MyCol AS String),\n       CAST(OtherCol AS Int64)\n  FROM t\n",
			asIs,
		)

		unspec := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.TypeStyle = formatter.Unspecified
		}))
		assert.Equal(t, asIs, unspec)

		// Non-leakage: column names MyCol/OtherCol and keywords CAST/AS stay untouched.
		assert.Contains(t, lower, "CAST(MyCol AS string)")
		assert.Contains(t, lower, "CAST(OtherCol AS int64)")
	})

	t.Run("BoolStyle", func(t *testing.T) {
		t.Parallel()

		sql := "SELECT TrUe, FaLsE, NULL FROM t WHERE Flag = tRuE"

		upper := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.BoolStyle = formatter.UpperCase
		}))
		assert.Equal(t,
			"SELECT TRUE, FALSE, NULL\n  FROM t\n WHERE Flag = TRUE\n",
			upper,
		)

		lower := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.BoolStyle = formatter.LowerCase
		}))
		assert.Equal(t,
			"SELECT true, false, NULL\n  FROM t\n WHERE Flag = true\n",
			lower,
		)

		asIs := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.BoolStyle = formatter.AsIs
		}))
		assert.Equal(t,
			"SELECT TrUe, FaLsE, NULL\n  FROM t\n WHERE Flag = tRuE\n",
			asIs,
		)

		unspec := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.BoolStyle = formatter.Unspecified
		}))
		assert.Equal(t, asIs, unspec)
	})

	t.Run("HexStyle", func(t *testing.T) {
		t.Parallel()

		sql := "SELECT 0xDeAdBeEf, 0X1a2B, 42 FROM t"

		upper := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.HexStyle = formatter.UpperCase
		}))
		assert.Equal(t, "SELECT 0xDEADBEEF, 0x1A2B, 42\n  FROM t\n", upper)

		lower := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.HexStyle = formatter.LowerCase
		}))
		assert.Equal(t, "SELECT 0xdeadbeef, 0x1a2b, 42\n  FROM t\n", lower)

		asIs := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.HexStyle = formatter.AsIs
		}))
		assert.Equal(t, "SELECT 0xDeAdBeEf, 0x1a2B, 42\n  FROM t\n", asIs)

		unspec := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.HexStyle = formatter.Unspecified
		}))
		assert.Equal(t, asIs, unspec)
	})

	t.Run("NumericStyle", func(t *testing.T) {
		t.Parallel()

		sql := "SELECT 1.5E10, 2.5e-3, NUMERIC '1.2E+4', BIGNUMERIC '3.4E-5', '1.2E+4' FROM t"

		upper := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.NumericStyle = formatter.UpperCase
		}))
		assert.Equal(t,
			"SELECT 1.5E10,\n       2.5E-3,\n       NUMERIC '1.2E+4',\n       BIGNUMERIC '3.4E-5',\n       '1.2E+4'\n  FROM t\n",
			upper,
		)

		lower := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.NumericStyle = formatter.LowerCase
		}))
		assert.Equal(t,
			"SELECT 1.5e10,\n       2.5e-3,\n       NUMERIC '1.2e+4',\n       BIGNUMERIC '3.4e-5',\n       '1.2E+4'\n  FROM t\n",
			lower,
		)

		asIs := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.NumericStyle = formatter.AsIs
		}))
		assert.Equal(t,
			"SELECT 1.5E10,\n       2.5e-3,\n       NUMERIC '1.2E+4',\n       BIGNUMERIC '3.4E-5',\n       '1.2E+4'\n  FROM t\n",
			asIs,
		)

		unspec := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.NumericStyle = formatter.Unspecified
		}))
		assert.Equal(t, lower, unspec, "Unspecified NumericStyle defaults to lowercase exponent")

		// Non-leakage: plain string literal '1.2E+4' is untouched by NumericStyle.
		assert.Contains(t, lower, "'1.2E+4'")
	})

	t.Run("NullStyle", func(t *testing.T) {
		t.Parallel()

		sql := "SELECT NuLl, COALESCE(x, nUlL), TRUE FROM t"

		upper := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.NullStyle = formatter.UpperCase
		}))
		assert.Equal(t, "SELECT NULL, COALESCE(x, NULL), TRUE\n  FROM t\n", upper)

		lower := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.NullStyle = formatter.LowerCase
		}))
		assert.Equal(t, "SELECT null, COALESCE(x, null), TRUE\n  FROM t\n", lower)

		asIs := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.NullStyle = formatter.AsIs
		}))
		assert.Equal(t, "SELECT NuLl, COALESCE(x, nUlL), TRUE\n  FROM t\n", asIs)

		unspec := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.NullStyle = formatter.Unspecified
		}))
		assert.Equal(t, asIs, unspec)
	})

	t.Run("PseudoColumnStyle", func(t *testing.T) {
		t.Parallel()

		sql := "SELECT _PartitionDate, _PartitionTime, _Table_Suffix, _Dbt_Max_Partition, _File_Name, _Row_Timestamp, RegularCol, `_PartitionDate` FROM t"

		// PseudoColumnStyle overrides IdentifierStyle for pseudo-columns only; backticked preserved.
		upperOverride := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IdentifierStyle = formatter.LowerCase
			o.PseudoColumnStyle = formatter.UpperCase
		}))
		assert.Equal(t,
			"SELECT _PARTITIONDATE,\n       _PARTITIONTIME,\n       _TABLE_SUFFIX,\n       _DBT_MAX_PARTITION,\n       _FILE_NAME,\n       _ROW_TIMESTAMP,\n       regularcol,\n       `_PartitionDate`\n  FROM t\n",
			upperOverride,
		)

		lowerOverride := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IdentifierStyle = formatter.UpperCase
			o.PseudoColumnStyle = formatter.LowerCase
		}))
		assert.Equal(t,
			"SELECT _partitiondate,\n       _partitiontime,\n       _table_suffix,\n       _dbt_max_partition,\n       _file_name,\n       _row_timestamp,\n       REGULARCOL,\n       `_PartitionDate`\n  FROM T\n",
			lowerOverride,
		)

		asIsOverride := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IdentifierStyle = formatter.LowerCase
			o.PseudoColumnStyle = formatter.AsIs
		}))
		assert.Contains(t, asIsOverride, "_PartitionDate,")
		assert.Contains(t, asIsOverride, "regularcol,")

		// Unspecified falls back to IdentifierStyle.
		fallback := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IdentifierStyle = formatter.LowerCase
			o.PseudoColumnStyle = formatter.Unspecified
		}))
		assert.Contains(t, fallback, "_partitiondate,")
		assert.Contains(t, fallback, "regularcol,")
		assert.Contains(t, fallback, "`_PartitionDate`")
	})

	t.Run("TableNameStyle", func(t *testing.T) {
		t.Parallel()

		t.Run("query_precedence_and_backticks", func(t *testing.T) {
			t.Parallel()

			sql := "SELECT MyCol FROM MyDataset.MyTable AS MyAlias JOIN `KeepProj.KeepDs.KeepTbl` AS k ON MyAlias.id = k.id"

			// TableNameStyle overrides IdentifierStyle on table paths; columns, aliases, and backticks are unaffected.
			upperTbl := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
				o.IdentifierStyle = formatter.LowerCase
				o.TableNameStyle = formatter.UpperCase
			}))
			assert.Equal(t,
				"SELECT mycol\n  FROM MYDATASET.MYTABLE AS myalias\n       JOIN\n       `KeepProj.KeepDs.KeepTbl` AS k\n       ON myalias.id = k.id\n",
				upperTbl,
			)

			lowerTbl := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
				o.IdentifierStyle = formatter.UpperCase
				o.TableNameStyle = formatter.LowerCase
			}))
			assert.Equal(t,
				"SELECT MYCOL\n  FROM mydataset.mytable AS MYALIAS\n       JOIN\n       `KeepProj.KeepDs.KeepTbl` AS K\n       ON MYALIAS.ID = K.ID\n",
				lowerTbl,
			)

			asIsTbl := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
				o.IdentifierStyle = formatter.LowerCase
				o.TableNameStyle = formatter.AsIs
			}))
			assert.Contains(t, asIsTbl, "FROM MyDataset.MyTable AS myalias")
			assert.Contains(t, asIsTbl, "SELECT mycol")

			// Unspecified falls back to IdentifierStyle.
			fallback := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
				o.IdentifierStyle = formatter.LowerCase
				o.TableNameStyle = formatter.Unspecified
			}))
			assert.Contains(t, fallback, "FROM mydataset.mytable AS myalias")
		})

		t.Run("extended_table_reference_scopes", func(t *testing.T) {
			t.Parallel()

			upperOpts := cloneOpts(func(o *formatter.PrintOptions) {
				o.IdentifierStyle = formatter.LowerCase
				o.TableNameStyle = formatter.UpperCase
			})
			lowerOpts := cloneOpts(func(o *formatter.PrintOptions) {
				o.IdentifierStyle = formatter.UpperCase
				o.TableNameStyle = formatter.LowerCase
			})

			cases := []struct {
				name      string
				sql       string
				wantUpper string
				wantLower string
			}{
				{
					name:      "foreign_key_references",
					sql:       "CREATE TABLE MyDs.ChildTbl (Id INT64, ParentId INT64, FOREIGN KEY (ParentId) REFERENCES MyDs.ParentTbl (Id) NOT ENFORCED)",
					wantUpper: "CREATE TABLE MYDS.CHILDTBL\n(\n  id       INT64,\n  parentid INT64,\n  FOREIGN KEY (parentid)\n  REFERENCES MYDS.PARENTTBL(id) NOT ENFORCED\n)\n",
					wantLower: "CREATE TABLE myds.childtbl\n(\n  ID       INT64,\n  PARENTID INT64,\n  FOREIGN KEY (PARENTID)\n  REFERENCES myds.parenttbl(ID) NOT ENFORCED\n)\n",
				},
				{
					name:      "table_clause_tvf_argument",
					sql:       "SELECT MyCol FROM my_tvf(TABLE MyDs.InputTbl)",
					wantUpper: "SELECT mycol\n  FROM my_tvf(\n         TABLE MYDS.INPUTTBL\n       )\n",
					wantLower: "SELECT MYCOL\n  FROM MY_TVF(\n         TABLE myds.inputtbl\n       )\n",
				},
				{
					name:      "property_graph_element_table",
					sql:       "CREATE PROPERTY GRAPH MyGraph NODE TABLES (MyDs.PersonTbl KEY (Id))",
					wantUpper: "CREATE PROPERTY GRAPH mygraph\n  NODE TABLES (\n    MYDS.PERSONTBL\n      KEY (id)\n  )\n",
					wantLower: "CREATE PROPERTY GRAPH MYGRAPH\n  NODE TABLES (\n    myds.persontbl\n      KEY (ID)\n  )\n",
				},
				{
					name:      "alter_table_rename_to",
					sql:       "ALTER TABLE MyDs.OldTbl RENAME TO MyDs.NewTbl",
					wantUpper: "ALTER TABLE MYDS.OLDTBL\n  RENAME TO MYDS.NEWTBL\n",
					wantLower: "ALTER TABLE myds.oldtbl\n  RENAME TO myds.newtbl\n",
				},
				{
					name:      "grant_on_table_vs_schema",
					sql:       "GRANT `roles/bigquery.dataViewer` ON TABLE MyDs.SecTbl TO 'user:a@b.com'; GRANT `roles/bigquery.dataViewer` ON SCHEMA MyDs TO 'user:a@b.com';",
					wantUpper: "GRANT `roles/bigquery.dataViewer` ON TABLE MYDS.SECTBL TO 'user:a@b.com';\n\nGRANT `roles/bigquery.dataViewer` ON SCHEMA myds TO 'user:a@b.com';\n",
					wantLower: "GRANT `roles/bigquery.dataViewer` ON TABLE myds.sectbl TO 'user:a@b.com';\n\nGRANT `roles/bigquery.dataViewer` ON SCHEMA MYDS TO 'user:a@b.com';\n",
				},
				{
					name:      "revoke_on_view",
					sql:       "REVOKE `roles/bigquery.dataViewer` ON VIEW MyDs.SecView FROM 'user:a@b.com'",
					wantUpper: "REVOKE `roles/bigquery.dataViewer` ON VIEW MYDS.SECVIEW FROM 'user:a@b.com'\n",
					wantLower: "REVOKE `roles/bigquery.dataViewer` ON VIEW myds.secview FROM 'user:a@b.com'\n",
				},
				{
					name:      "create_search_index_does_not_style_index_name",
					sql:       "CREATE SEARCH INDEX MyIdx ON MyDs.IndexedTbl (ALL COLUMNS)",
					wantUpper: "CREATE SEARCH INDEX myidx\nON MYDS.INDEXEDTBL(ALL COLUMNS)\n",
					wantLower: "CREATE SEARCH INDEX MYIDX\nON myds.indexedtbl(ALL COLUMNS)\n",
				},
				{
					name:      "drop_table_vs_drop_schema",
					sql:       "DROP TABLE MyDs.DropTbl; DROP SCHEMA MyDs;",
					wantUpper: "DROP TABLE MYDS.DROPTBL;\n\nDROP SCHEMA myds;\n",
					wantLower: "DROP TABLE myds.droptbl;\n\nDROP SCHEMA MYDS;\n",
				},
			}

			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()

					assert.Equal(t, tc.wantUpper, formatWithOpts(t, tc.sql, upperOpts))
					assert.Equal(t, tc.wantLower, formatWithOpts(t, tc.sql, lowerOpts))
				})
			}
		})
	})

	t.Run("QueryParameterStyle", func(t *testing.T) {
		t.Parallel()

		sql := "SELECT @MyParam, @`KeepParam`, ?, MyCol FROM t WHERE x = @OtherParam"

		// QueryParameterStyle overrides IdentifierStyle; positional ? and backticked param unaffected.
		upper := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IdentifierStyle = formatter.LowerCase
			o.QueryParameterStyle = formatter.UpperCase
		}))
		assert.Equal(t,
			"SELECT @MYPARAM, @`KeepParam`, ?, mycol\n  FROM t\n WHERE x = @OTHERPARAM\n",
			upper,
		)

		lower := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IdentifierStyle = formatter.UpperCase
			o.QueryParameterStyle = formatter.LowerCase
		}))
		assert.Equal(t,
			"SELECT @myparam, @`KeepParam`, ?, MYCOL\n  FROM T\n WHERE X = @otherparam\n",
			lower,
		)

		asIs := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IdentifierStyle = formatter.LowerCase
			o.QueryParameterStyle = formatter.AsIs
		}))
		assert.Equal(t,
			"SELECT @MyParam, @`KeepParam`, ?, mycol\n  FROM t\n WHERE x = @OtherParam\n",
			asIs,
		)

		// Unspecified falls back to IdentifierStyle.
		fallback := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IdentifierStyle = formatter.LowerCase
			o.QueryParameterStyle = formatter.Unspecified
		}))
		assert.Equal(t,
			"SELECT @myparam, @`KeepParam`, ?, mycol\n  FROM t\n WHERE x = @otherparam\n",
			fallback,
		)
	})

	t.Run("SystemVariableStyle", func(t *testing.T) {
		t.Parallel()

		sql := "SET @@Time_Zone = 'UTC'; SELECT @@Project_Id, @@Dataset_Id, @@`Keep_Var`, MyCol FROM t;"

		// SystemVariableStyle overrides IdentifierStyle on both SET and SELECT; backticked preserved.
		upper := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IdentifierStyle = formatter.LowerCase
			o.SystemVariableStyle = formatter.UpperCase
		}))
		assert.Equal(t,
			"SET @@TIME_ZONE = 'UTC';\n\nSELECT @@PROJECT_ID, @@DATASET_ID, @@`Keep_Var`, mycol\n  FROM t;\n",
			upper,
		)

		lower := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IdentifierStyle = formatter.UpperCase
			o.SystemVariableStyle = formatter.LowerCase
		}))
		assert.Equal(t,
			"SET @@time_zone = 'UTC';\n\nSELECT @@project_id, @@dataset_id, @@`Keep_Var`, MYCOL\n  FROM T;\n",
			lower,
		)

		asIs := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IdentifierStyle = formatter.LowerCase
			o.SystemVariableStyle = formatter.AsIs
		}))
		assert.Equal(t,
			"SET @@Time_Zone = 'UTC';\n\nSELECT @@Project_Id, @@Dataset_Id, @@`Keep_Var`, mycol\n  FROM t;\n",
			asIs,
		)

		// Unspecified falls back to IdentifierStyle.
		fallback := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.IdentifierStyle = formatter.LowerCase
			o.SystemVariableStyle = formatter.Unspecified
		}))
		assert.Equal(t,
			"SET @@time_zone = 'UTC';\n\nSELECT @@project_id, @@dataset_id, @@`Keep_Var`, mycol\n  FROM t;\n",
			fallback,
		)
	})
}

func TestPrintOptionsQuoteStyles(t *testing.T) {
	t.Parallel()

	t.Run("StringStyle", func(t *testing.T) {
		t.Parallel()

		sql := `SELECT "hello", 'world', "it's", 'say "hi"', R"raw", """triple""", B"bytes" FROM t`

		preferSingle := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.StringStyle = formatter.PreferSingleQuote
			o.BytesStyle = formatter.AsIsStringStyle
		}))
		assert.Equal(t,
			"SELECT 'hello',\n       'world',\n       \"it's\",\n       'say \"hi\"',\n       r'raw',\n       '''triple''',\n       B\"bytes\"\n  FROM t\n",
			preferSingle,
		)

		preferDouble := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.StringStyle = formatter.PreferDoubleQuote
			o.BytesStyle = formatter.AsIsStringStyle
		}))
		assert.Equal(t,
			"SELECT \"hello\",\n       \"world\",\n       \"it's\",\n       'say \"hi\"',\n       r\"raw\",\n       \"\"\"triple\"\"\",\n       B\"bytes\"\n  FROM t\n",
			preferDouble,
		)

		asIs := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.StringStyle = formatter.AsIsStringStyle
			o.BytesStyle = formatter.AsIsStringStyle
		}))
		assert.Equal(t,
			"SELECT \"hello\",\n       'world',\n       \"it's\",\n       'say \"hi\"',\n       R\"raw\",\n       \"\"\"triple\"\"\",\n       B\"bytes\"\n  FROM t\n",
			asIs,
		)

		unspec := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.StringStyle = formatter.UnspecifiedStringStyle
			o.BytesStyle = formatter.AsIsStringStyle
		}))
		assert.Equal(t, asIs, unspec)

		// Non-leakage: bytes literal B"bytes" is untouched by StringStyle.
		for _, out := range []string{preferSingle, preferDouble, asIs, unspec} {
			assert.Contains(t, out, `B"bytes"`)
		}
	})

	t.Run("BytesStyle", func(t *testing.T) {
		t.Parallel()

		sql := `SELECT B"hello", b'world', b"it's", b'say "hi"', RB"raw", BR'raw2', b"""triple""", "text" FROM t`

		preferSingle := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.BytesStyle = formatter.PreferSingleQuote
			o.StringStyle = formatter.AsIsStringStyle
		}))
		assert.Equal(t,
			"SELECT b'hello',\n       b'world',\n       b\"it's\",\n       b'say \"hi\"',\n       rb'raw',\n       rb'raw2',\n       b'''triple''',\n       \"text\"\n  FROM t\n",
			preferSingle,
		)

		preferDouble := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.BytesStyle = formatter.PreferDoubleQuote
			o.StringStyle = formatter.AsIsStringStyle
		}))
		assert.Equal(t,
			"SELECT b\"hello\",\n       b\"world\",\n       b\"it's\",\n       b'say \"hi\"',\n       rb\"raw\",\n       rb\"raw2\",\n       b\"\"\"triple\"\"\",\n       \"text\"\n  FROM t\n",
			preferDouble,
		)

		asIs := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.BytesStyle = formatter.AsIsStringStyle
			o.StringStyle = formatter.AsIsStringStyle
		}))
		assert.Equal(t,
			"SELECT B\"hello\",\n       b'world',\n       b\"it's\",\n       b'say \"hi\"',\n       RB\"raw\",\n       BR'raw2',\n       b\"\"\"triple\"\"\",\n       \"text\"\n  FROM t\n",
			asIs,
		)

		unspec := formatWithOpts(t, sql, cloneOpts(func(o *formatter.PrintOptions) {
			o.BytesStyle = formatter.UnspecifiedStringStyle
			o.StringStyle = formatter.AsIsStringStyle
		}))
		assert.Equal(t, asIs, unspec)

		// Non-leakage: string literal "text" is untouched by BytesStyle.
		for _, out := range []string{preferSingle, preferDouble, asIs, unspec} {
			assert.Contains(t, out, `"text"`)
		}
	})
}

func TestPrintOptionsValidate(t *testing.T) {
	t.Parallel()

	t.Run("valid_configurations", func(t *testing.T) {
		t.Parallel()

		var nilOpts *formatter.PrintOptions
		require.NoError(t, nilOpts.Validate())
		require.NoError(t, (&formatter.PrintOptions{}).Validate())
		require.NoError(t, formatter.DefaultPrintOptions().Validate())

		for _, c := range []formatter.PrintCase{
			formatter.Unspecified,
			formatter.AsIs,
			formatter.LowerCase,
			formatter.UpperCase,
		} {
			opts := &formatter.PrintOptions{
				BuiltinFunctionNameStyle: c,
				FunctionNameStyle:        c,
				IdentifierStyle:          c,
				KeywordStyle:             c,
				TypeStyle:                c,
				BoolStyle:                c,
				HexStyle:                 c,
				NumericStyle:             c,
				NullStyle:                c,
				PseudoColumnStyle:        c,
				TableNameStyle:           c,
				QueryParameterStyle:      c,
				SystemVariableStyle:      c,
			}
			require.NoError(t, opts.Validate(), "PrintCase=%q should be valid", c)
		}

		for _, s := range []formatter.StringStyle{
			formatter.UnspecifiedStringStyle,
			formatter.AsIsStringStyle,
			formatter.PreferSingleQuote,
			formatter.PreferDoubleQuote,
		} {
			opts := &formatter.PrintOptions{
				BytesStyle:  s,
				StringStyle: s,
			}
			require.NoError(t, opts.Validate(), "StringStyle=%q should be valid", s)
		}

		for _, w := range []formatter.When{
			formatter.UnspecifiedWhen,
			formatter.Never,
			formatter.Always,
			formatter.Auto,
		} {
			opts := &formatter.PrintOptions{ColumnListTrailingComma: w}
			require.NoError(t, opts.Validate(), "When=%q should be valid", w)
		}

		for _, cat := range []formatter.FunctionCatalog{
			formatter.EmptyCatalog,
			formatter.BigQueryCatalog,
		} {
			opts := &formatter.PrintOptions{FunctionCatalog: cat}
			require.NoError(t, opts.Validate(), "FunctionCatalog=%q should be valid", cat)
		}
	})

	t.Run("invalid_enum_fields", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			field  string
			mutate func(*formatter.PrintOptions)
		}{
			{"builtin_function_name_style", func(o *formatter.PrintOptions) { o.BuiltinFunctionNameStyle = "TITLE_CASE" }},
			{"function_name_style", func(o *formatter.PrintOptions) { o.FunctionNameStyle = "TITLE_CASE" }},
			{"identifier_style", func(o *formatter.PrintOptions) { o.IdentifierStyle = "SNAKE_CASE" }},
			{"keyword_style", func(o *formatter.PrintOptions) { o.KeywordStyle = "MIXED" }},
			{"type_style", func(o *formatter.PrintOptions) { o.TypeStyle = "CAMEL" }},
			{"bool_style", func(o *formatter.PrintOptions) { o.BoolStyle = "INVALID" }},
			{"hex_style", func(o *formatter.PrintOptions) { o.HexStyle = "INVALID" }},
			{"numeric_style", func(o *formatter.PrintOptions) { o.NumericStyle = "INVALID" }},
			{"null_style", func(o *formatter.PrintOptions) { o.NullStyle = "INVALID" }},
			{"pseudo_column_style", func(o *formatter.PrintOptions) { o.PseudoColumnStyle = "INVALID" }},
			{"table_name_style", func(o *formatter.PrintOptions) { o.TableNameStyle = "INVALID" }},
			{"query_parameter_style", func(o *formatter.PrintOptions) { o.QueryParameterStyle = "INVALID" }},
			{"system_variable_style", func(o *formatter.PrintOptions) { o.SystemVariableStyle = "INVALID" }},
			{"bytes_style", func(o *formatter.PrintOptions) { o.BytesStyle = "BACKTICK" }},
			{"string_style", func(o *formatter.PrintOptions) { o.StringStyle = "BACKTICK" }},
			{"column_list_trailing_comma", func(o *formatter.PrintOptions) { o.ColumnListTrailingComma = "SOMETIMES" }},
			{"function_catalog", func(o *formatter.PrintOptions) { o.FunctionCatalog = "POSTGRES" }},
		}

		for _, tc := range cases {
			t.Run(tc.field, func(t *testing.T) {
				t.Parallel()

				opts := cloneOpts(tc.mutate)
				err := opts.Validate()
				require.ErrorIs(t, err, formatter.ErrInvalidPrintOption)
				assert.Contains(t, err.Error(), tc.field)
			})
		}
	})

	t.Run("negative_count_fields", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			field  string
			mutate func(*formatter.PrintOptions)
		}{
			{"soft_max_cols", func(o *formatter.PrintOptions) { o.SoftMaxColumns = -1 }},
			{"indentation", func(o *formatter.PrintOptions) { o.Indentation = -1 }},
			{"min_joins_to_separate_in_blocks", func(o *formatter.PrintOptions) { o.MinJoinsToSeparateInBlocks = -1 }},
			{"max_cols_for_single_line_select", func(o *formatter.PrintOptions) { o.MaxColumnsForSingleLineSelect = -1 }},
			{"max_params_for_single_line_function", func(o *formatter.PrintOptions) { o.MaxParamsForSingleLineFunction = -1 }},
		}

		for _, tc := range cases {
			t.Run(tc.field, func(t *testing.T) {
				t.Parallel()

				opts := cloneOpts(tc.mutate)
				err := opts.Validate()
				require.ErrorIs(t, err, formatter.ErrInvalidPrintOption)
				assert.Contains(t, err.Error(), tc.field)
			})
		}
	})
}
