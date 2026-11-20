package formatter_test

import (
	"bytes"
	"log"
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

	t.Run("WithLogger emits debug trace sections cleanly", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		bqf := formatter.NewBigQueryFormatter(formatter.WithLogger(log.New(&buf, "", 0)))

		got, err := bqf.Format("SELECT {{ col }} FROM t -- note")
		require.NoError(t, err)
		assert.Equal(t, "SELECT {{ col }}\n  FROM t  -- note\n", got)
		assert.Contains(t, buf.String(), "# BigQuery Format")
		assert.Contains(t, buf.String(), "## Input AST")
	})

	t.Run("Trailing comment alignment ignores URLs and comment tokens inside literals and backticks", func(t *testing.T) {
		t.Parallel()

		opts := formatter.DefaultPrintOptions()
		opts.MaxColumnsForSingleLineSelect = 1

		sql := "SELECT 'https://example.com' AS a, -- first\n" +
			"'http://short.io' AS b,\n" +
			"'foo--bar' AS c,\n" +
			"'tag#1' AS d,\n" +
			"`x--2` AS e -- fifth\nFROM t"
		got, err := formatter.Format(sql, opts)
		require.NoError(t, err)
		assert.Contains(t, got, "'http://short.io'")
		assert.Contains(t, got, "'foo--bar'")
		assert.Contains(t, got, "'tag#1'")
		assert.Contains(t, got, "`x--2`")
		assert.Contains(t, got, "-- first")
		assert.Contains(t, got, "-- fifth")
	})

	t.Run("DROP statements preserve backticked case and honor TableNameStyle", func(t *testing.T) {
		t.Parallel()

		opts := formatter.DefaultPrintOptions()
		opts.IdentifierStyle = formatter.LowerCase
		opts.TableNameStyle = formatter.AsIs

		gotBacktick, err := formatter.Format("DROP TABLE my_dataset.`MyCaseSensitiveTable`", opts)
		require.NoError(t, err)
		assert.Equal(t, "DROP TABLE my_dataset.`MyCaseSensitiveTable`\n", gotBacktick)

		gotTableStyle, err := formatter.Format("DROP TABLE MyDataset.MyTable", opts)
		require.NoError(t, err)
		assert.Equal(t, "DROP TABLE MyDataset.MyTable\n", gotTableStyle)

		gotPolicies, err := formatter.Format("DROP ALL ROW ACCESS POLICIES ON MyDataset.`MyTable`", opts)
		require.NoError(t, err)
		assert.Equal(t, "DROP ALL ROW ACCESS POLICIES ON MyDataset.`MyTable`\n", gotPolicies)
	})

	t.Run("PRIMARY KEY and MERGE keyword scans do not match identifier substrings", func(t *testing.T) {
		t.Parallel()

		pkSQL := "CREATE TABLE t (enforced_id INT64, PRIMARY KEY (enforced_id) OPTIONS (description = 'not enforced'))"
		gotPK, err := formatter.Format(pkSQL, formatter.DefaultPrintOptions())
		require.NoError(t, err)
		assert.NotContains(t, gotPK, ") ENFORCED")

		mergeSQL := "MERGE dst t USING src s ON t.id = s.id WHEN NOT MATCHED AND target_flag = 1 THEN INSERT ROW"
		gotMerge, err := formatter.Format(mergeSQL, formatter.DefaultPrintOptions())
		require.NoError(t, err)
		assert.NotContains(t, gotMerge, "BY TARGET")
		assert.Contains(t, gotMerge, "WHEN NOT MATCHED AND target_flag = 1 THEN")
	})

	t.Run("IS DISTINCT FROM aligns with single space before RHS", func(t *testing.T) {
		t.Parallel()

		sql := "SELECT id FROM t WHERE a IS DISTINCT FROM b AND long_col = 1"
		got, err := formatter.Format(sql, formatter.DefaultPrintOptions())
		require.NoError(t, err)
		assert.Equal(
			t,
			"SELECT id\n  FROM t\n WHERE a        IS DISTINCT FROM b\n   AND long_col =                1\n",
			got,
		)
	})

	t.Run("Entity DDL honors KeywordStyle AsIs and Validate rejects negative counts", func(t *testing.T) {
		t.Parallel()

		opts := formatter.DefaultPrintOptions()
		opts.KeywordStyle = formatter.AsIs

		got, err := formatter.Format("CREATE reservation my_res OPTIONS (slot_capacity = 100)", opts)
		require.NoError(t, err)
		assert.Contains(t, got, "CREATE reservation my_res")

		badOpts := formatter.DefaultPrintOptions()
		badOpts.Indentation = -1
		require.ErrorIs(t, badOpts.Validate(), formatter.ErrInvalidPrintOption)
	})

	t.Run("HAVING and QUALIFY with AlignLogicalWithClauses false stay valid and idempotent", func(t *testing.T) {
		t.Parallel()

		opts := formatter.DefaultPrintOptions()
		opts.AlignLogicalWithClauses = false

		for _, tc := range []struct {
			in   string
			want string
		}{
			{
				in:   "SELECT a FROM t GROUP BY a HAVING a > 1 AND COUNT(*) > 2",
				want: "SELECT a\n  FROM t\n GROUP BY a\nHAVING     a > 1\n       AND COUNT(*) > 2\n",
			},
			{
				in:   "SELECT a FROM t QUALIFY a > 1 OR b > 2",
				want: " SELECT a\n   FROM t\nQUALIFY    a > 1\n        OR b > 2\n",
			},
		} {
			got, err := formatter.Format(tc.in, opts)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)

			got2, err := formatter.Format(got, opts)
			require.NoError(t, err)
			assert.Equal(t, got, got2)
		}
	})

	t.Run("LOOP statements and trailing labels handle empty condition and keyword suffixes", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			in   string
			want string
		}{
			{
				in:   "LOOP SELECT 1; BREAK; END LOOP;",
				want: "LOOP\n  SELECT 1;\n\n  BREAK;\nEND LOOP\n",
			},
			{
				in:   "op: LOOP BREAK op; END LOOP;",
				want: "op: LOOP\n  BREAK op;\nEND LOOP\n",
			},
			{
				in:   "op: LOOP BREAK op; END LOOP op;",
				want: "op: LOOP\n  BREAK op;\nEND LOOP op\n",
			},
			{
				in:   "d: BEGIN SELECT 1; END;",
				want: "d: BEGIN\n  SELECT 1;\nEND\n",
			},
			{
				in:   "le: WHILE true DO BREAK le; END WHILE;",
				want: "le: WHILE TRUE DO\n  BREAK le;\nEND WHILE\n",
			},
		} {
			got, err := formatter.Format(tc.in, formatter.DefaultPrintOptions())
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)

			got2, err := formatter.Format(got, formatter.DefaultPrintOptions())
			require.NoError(t, err)
			assert.Equal(t, got, got2)
		}
	})

	t.Run("Bitwise shift and unary NOT preserve semantically required parentheses", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			in   string
			want string
		}{
			{in: "SELECT (a << 1) + 2", want: "SELECT (a << 1) + 2\n"},
			{in: "SELECT 1 + (a << 2)", want: "SELECT 1 + (a << 2)\n"},
			{in: "SELECT (a << 1) * 2", want: "SELECT (a << 1) * 2\n"},
			{in: "SELECT (NOT a) = TRUE", want: "SELECT (NOT a) = TRUE\n"},
			{in: "SELECT (NOT a) IS TRUE", want: "SELECT (NOT a) IS TRUE\n"},
			{in: "SELECT NOT (NOT a NOT LIKE b)", want: "SELECT NOT NOT a NOT LIKE b\n"},
		} {
			got, err := formatter.Format(tc.in, formatter.DefaultPrintOptions())
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)

			got2, err := formatter.Format(got, formatter.DefaultPrintOptions())
			require.NoError(t, err)
			assert.Equal(t, got, got2)
		}
	})

	t.Run("Template blocks do not falsely match SELECT identifier substrings", func(t *testing.T) {
		t.Parallel()

		got, err := formatter.Format(
			"SELECT a FROM t WHERE {% if is_selected %}x = 1{% endif %}",
			formatter.DefaultPrintOptions(),
		)
		require.NoError(t, err)
		assert.Equal(t, "SELECT a\n  FROM t\n WHERE {% if is_selected %}x = 1{% endif %}\n", got)
	})

	t.Run("KeywordStyle AsIs is not overridden by IdentifierStyle on keyword identifiers", func(t *testing.T) {
		t.Parallel()

		opts := formatter.DefaultPrintOptions()
		opts.IdentifierStyle = formatter.LowerCase
		opts.KeywordStyle = formatter.AsIs

		gotCreate, err := formatter.Format("CREATE RESERVATION my_res OPTIONS (slot_capacity = 100)", opts)
		require.NoError(t, err)
		assert.Contains(t, gotCreate, "CREATE RESERVATION my_res")

		gotAlter, err := formatter.Format("ALTER RESERVATION my_res SET OPTIONS (slot_capacity = 200)", opts)
		require.NoError(t, err)
		assert.Contains(t, gotAlter, "ALTER RESERVATION my_res")

		gotDrop, err := formatter.Format("DROP RESERVATION my_res", opts)
		require.NoError(t, err)
		assert.Equal(t, "DROP RESERVATION my_res\n", gotDrop)

		gotSample, err := formatter.Format("SELECT id FROM t TABLESAMPLE SYSTEM (10 PERCENT)", opts)
		require.NoError(t, err)
		assert.Contains(t, gotSample, "TABLESAMPLE SYSTEM")

		gotInterval, err := formatter.Format("SELECT INTERVAL 1 DAY TO SECOND", opts)
		require.NoError(t, err)
		assert.Equal(t, "SELECT INTERVAL 1 DAY TO SECOND\n", gotInterval)
	})

	t.Run("TableNameStyle applies to row access policy and index target tables only", func(t *testing.T) {
		t.Parallel()

		opts := formatter.DefaultPrintOptions()
		opts.IdentifierStyle = formatter.LowerCase
		opts.TableNameStyle = formatter.AsIs

		gotPolicy, err := formatter.Format(
			"CREATE ROW ACCESS POLICY MyPol ON MyDataset.MyTable GRANT TO ('user:a@b.com') FILTER USING (a = 1)",
			opts,
		)
		require.NoError(t, err)
		assert.Contains(t, gotPolicy, "POLICY mypol")
		assert.Contains(t, gotPolicy, "ON MyDataset.MyTable")

		gotIdx, err := formatter.Format("CREATE SEARCH INDEX MyIdx ON MyDataset.MyTable (ALL COLUMNS)", opts)
		require.NoError(t, err)
		assert.Contains(t, gotIdx, "INDEX myidx")
		assert.Contains(t, gotIdx, "ON MyDataset.MyTable")

		gotDropIdx, err := formatter.Format("DROP SEARCH INDEX MyIdx ON MyDataset.MyTable", opts)
		require.NoError(t, err)
		assert.Equal(t, "DROP SEARCH INDEX myidx ON MyDataset.MyTable\n", gotDropIdx)

		gotAlterIdx, err := formatter.Format("ALTER SEARCH INDEX MyIdx ON MyDataset.MyTable REBUILD", opts)
		require.NoError(t, err)
		assert.Contains(t, gotAlterIdx, "INDEX myidx ON MyDataset.MyTable")
	})
}
