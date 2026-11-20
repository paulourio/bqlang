package bqlang_test

import (
	"encoding/json"
	"testing"

	"github.com/goccy/go-googlesql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang"
)

func TestDefaultOptionsAndMarshalJSON(t *testing.T) {
	t.Parallel()

	require.NoError(t, bqlang.Init())

	opts := bqlang.DefaultParserOptions()
	require.NotNil(t, opts)

	sql := "SELECT a, SUM(b).ROUND(2) AS total FROM project-id.ds.tbl QUALIFY ROW_NUMBER() OVER (PARTITION BY a) = 1"
	out, err := googlesql.ParseStatement(sql, opts)
	require.NoError(t, err)

	stmt, err := out.Statement()
	require.NoError(t, err)

	raw, err := bqlang.MarshalJSON(stmt)
	require.NoError(t, err)
	assert.NotEmpty(t, raw)

	var decoded bqlang.ASTNode
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, "QueryStatement", decoded.Kind)
	assert.True(t, decoded.IsStatement)

	nilBytes, err := bqlang.MarshalJSON(nil)
	require.NoError(t, err)
	assert.Nil(t, nilBytes)

	nilNode, err := bqlang.NewASTNode(nil)
	require.ErrorIs(t, err, bqlang.ErrNilASTNode)
	assert.Nil(t, nilNode)
}
