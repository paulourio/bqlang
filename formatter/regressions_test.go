package formatter_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/formatter"
)

func TestRegressions(t *testing.T) {
	t.Parallel()

	t.Run("EOF identifier without trailing newline is preserved", func(t *testing.T) {
		t.Parallel()

		got, err := formatter.Format("SELECT foo", formatter.DefaultPrintOptions())
		require.NoError(t, err)
		assert.Equal(t, "SELECT foo\n", got)
	})

	t.Run("Zero-valued PrintOptions does not panic or erase strings", func(t *testing.T) {
		t.Parallel()

		got, err := formatter.Format("SELECT 'hello', b'world'", &formatter.PrintOptions{SoftMaxColumns: 80})
		require.NoError(t, err)
		assert.Equal(t, "SELECT 'hello',\n       b'world'\n", got)
	})

	t.Run("PreferDoubleQuote converts single-quoted strings and bytes cleanly", func(t *testing.T) {
		t.Parallel()

		opts := formatter.DefaultPrintOptions()
		opts.StringStyle = formatter.PreferDoubleQuote
		opts.BytesStyle = formatter.PreferDoubleQuote

		got, err := formatter.Format(`SELECT 'abc', R'raw', b'xyz', RB'rbytes', 'has "quote"'`, opts)
		require.NoError(t, err)
		assert.Equal(
			t,
			"SELECT \"abc\",\n       r\"raw\",\n       b\"xyz\",\n       rb\"rbytes\",\n       'has \"quote\"'\n",
			got,
		)
	})

	t.Run("Multi-line string literal internal trailing whitespace is preserved", func(t *testing.T) {
		t.Parallel()

		input := "SELECT '''line1   \nline2  ''' AS s\n"
		got, err := formatter.Format(input, formatter.DefaultPrintOptions())
		require.NoError(t, err)
		assert.Equal(t, "SELECT '''line1   \nline2  ''' AS s\n", got)
	})

	t.Run("PrintOptions Indentation, IndentCaseWhen, ColumnListTrailingComma, NumericStyle", func(t *testing.T) {
		t.Parallel()

		opts := formatter.DefaultPrintOptions()
		opts.Indentation = 4
		opts.IndentCaseWhen = false
		opts.ColumnListTrailingComma = formatter.Always
		opts.NumericStyle = formatter.UpperCase
		opts.MaxColumnsForSingleLineSelect = 1

		sql := "SELECT 1.5e10 AS a, CASE WHEN x = 1 THEN 'a' ELSE 'b' END AS b FROM (SELECT 1 AS x)"
		got, err := formatter.Format(sql, opts)
		require.NoError(t, err)
		assert.Contains(t, got, "1.5E10")
		assert.Contains(t, got, "END AS b,")
		assert.Contains(t, got, "  FROM (\n           SELECT 1 AS x\n       )")
		assert.Contains(t, got, "       CASE\n       WHEN x = 1 THEN 'a'")
	})

	t.Run("Template placeholders survive IdentifierStyle UpperCase and KeywordStyle LowerCase", func(t *testing.T) {
		t.Parallel()

		opts := formatter.DefaultPrintOptions()
		opts.IdentifierStyle = formatter.UpperCase
		opts.KeywordStyle = formatter.LowerCase

		sql := "{{ stmt }}\nselect {{ myCol }}, baz from {{ myTable }}"
		got, err := formatter.Format(sql, opts)
		require.NoError(t, err)
		assert.Contains(t, got, "{{ stmt }}")
		assert.Contains(t, got, "{{ myCol }}")
		assert.Contains(t, got, "BAZ")
		assert.Contains(t, got, "from {{ myTable }}")
	})

	t.Run("BigQuery function catalog fixes typos", func(t *testing.T) {
		t.Parallel()

		opts := formatter.DefaultPrintOptions()
		opts.FunctionCatalog = formatter.BigQueryCatalog
		opts.FunctionNameStyle = formatter.AsIs

		sql := "SELECT hll_count.init(x), to_json(x), net.ipv4_to_int64(b), net.safe_ip_from_string(s), myCustomUdf(x) FROM t"
		got, err := formatter.Format(sql, opts)
		require.NoError(t, err)
		assert.Contains(t, got, "HLL_COUNT.INIT(x)")
		assert.Contains(t, got, "TO_JSON(x)")
		assert.Contains(t, got, "NET.IPV4_TO_INT64(b)")
		assert.Contains(t, got, "NET.SAFE_IP_FROM_STRING(s)")
		assert.Contains(t, got, "myCustomUdf(x)")
	})

	t.Run("Unsupported AST node inside nested statement list propagates PrinterError", func(t *testing.T) {
		t.Parallel()

		_, err := formatter.Format("BEGIN SHOW TABLES; END;", formatter.DefaultPrintOptions())
		require.Error(t, err)

		var perr *formatter.PrinterError
		require.ErrorAs(t, err, &perr)
	})

	t.Run("Extended style options PseudoColumn, TableName, QueryParameter, SystemVariable", func(t *testing.T) {
		t.Parallel()

		opts := formatter.DefaultPrintOptions()
		opts.IdentifierStyle = formatter.LowerCase
		opts.PseudoColumnStyle = formatter.UpperCase
		opts.TableNameStyle = formatter.AsIs
		opts.QueryParameterStyle = formatter.AsIs
		opts.SystemVariableStyle = formatter.AsIs

		sql := "SELECT MyCol, _partitiondate FROM MyDataset.MyTable WHERE MyCol = @MyParam AND @@project_id = 'p'"
		got, err := formatter.Format(sql, opts)
		require.NoError(t, err)
		assert.Contains(t, got, "mycol")
		assert.Contains(t, got, "_PARTITIONDATE")
		assert.Contains(t, got, "MyDataset.MyTable")
		assert.Contains(t, got, "@MyParam")
		assert.Contains(t, got, "@@project_id")
	})
}
