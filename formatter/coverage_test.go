package formatter_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/formatter"
)

func TestPrinterErrorFormattingAndUnwrap(t *testing.T) {
	t.Parallel()

	// Exercise PrinterError produced by an unimplemented AST node.
	_, err := formatter.Format(
		"CREATE PRIVILEGE RESTRICTION ON SELECT ON TABLE t RESTRICT TO ('user:a@b.com')",
		nil,
	)
	require.Error(t, err)

	var perr *formatter.PrinterError
	require.ErrorAs(t, err, &perr)
	assert.Contains(t, perr.Error(), "PrinterError")
	assert.Contains(t, perr.Error(), "CreatePrivilegeRestrictionStatement")
	assert.Contains(t, perr.Error(), "not implemented")
	require.NoError(t, perr.Unwrap())

	// Exercise PrinterError wrapping an inner sentinel error.
	wrapped := &formatter.PrinterError{
		Msg: "literal failure",
		Err: formatter.ErrInvalidStringLiteral,
	}
	require.ErrorIs(t, wrapped, formatter.ErrInvalidStringLiteral)
	assert.Contains(t, wrapped.Error(), "literal failure")
	assert.Contains(t, wrapped.Error(), formatter.ErrInvalidStringLiteral.Error())
}

func TestLowCoverageBranchesIdempotent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		sql  string
	}{
		{
			name: "model_input_output",
			sql:  "CREATE MODEL m INPUT (x INT64) OUTPUT (y FLOAT64) REMOTE WITH CONNECTION `p.us.c` OPTIONS (endpoint = 'gemini')",
		},
		{
			name: "graph_path_search_prefixes_modes_and_edges",
			sql: `SELECT * FROM GRAPH_TABLE(
  g
  MATCH ANY SHORTEST WALK (a)-[e1]->(b),
        SHORTEST 2 TRAIL (b)<-[e2]-(c),
        ANY SIMPLE (c)-[e3]-(d),
        ANY 3 ACYCLIC (d)-(e),
        ALL (e)->(f),
        ALL SHORTEST (f)<-(h)
  RETURN a.id
)`,
		},
		{
			name: "complex_parenthesized_or_in_and",
			sql: `SELECT a
FROM t
WHERE x = 1
  AND (
    col_alpha = 'one'
    OR col_beta = 'two'
    OR col_gamma = 'three'
    OR col_delta = 'four'
  )`,
		},
		{
			name: "complex_function_modifiers",
			sql: `SELECT
  ARRAY_AGG(
    x
    WHERE a = 1 AND b = 2 AND c = 3 AND d = 4
    GROUP BY g1, g2, g3, g4, g5
    HAVING COUNT(*) > 1 AND SUM(y) > 0 AND AVG(z) < 10
    WITH REPORT (format = 'JSON', include_nulls = TRUE)
  )
FROM t`,
		},
		{
			name: "drop_schema_object_kinds",
			sql: `DROP EXTERNAL TABLE IF EXISTS ds.et;
DROP MATERIALIZED VIEW IF EXISTS ds.mv;
DROP MODEL IF EXISTS ds.m;
DROP PROCEDURE IF EXISTS ds.p;
DROP SCHEMA IF EXISTS ds;
DROP EXTERNAL SCHEMA IF EXISTS eds;
DROP SNAPSHOT TABLE IF EXISTS ds.st;
DROP PROPERTY GRAPH IF EXISTS ds.pg;
DROP TABLE FUNCTION IF EXISTS ds.tf;
DROP VIEW IF EXISTS ds.v;
DROP FUNCTION IF EXISTS ds.f;`,
		},
	}

	opts := formatter.DefaultPrintOptions()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out1, err := formatter.Format(tc.sql, opts)
			require.NoError(t, err)
			assert.NotEmpty(t, out1)

			out2, err := formatter.Format(out1, opts)
			require.NoError(t, err)
			assert.Equal(t, out1, out2)
		})
	}
}
