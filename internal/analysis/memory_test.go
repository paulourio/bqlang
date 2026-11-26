package analysis

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/bqconfig"
	"github.com/paulourio/bqlang/catalog"
	"github.com/paulourio/bqlang/internal/syntax"
)

func TestEnginePrunesStaleStatementCaches(t *testing.T) {
	t.Parallel()

	opts := bqconfig.DefaultLSPOptions()
	opts.CacheDir = ""
	store, err := catalog.NewStore(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	store.PutTable(&catalog.TableMeta{
		Ref: catalog.TableRef{Project: "p", Dataset: "d", Table: "orders"},
		Schema: []catalog.ColumnMeta{
			{Name: "id", Type: "INT64"},
			{Name: "amount", Type: "FLOAT64"},
		},
	})

	eng, err := NewEngine(store, opts)
	require.NoError(t, err)

	uri := "untitled:cache_prune.sql"
	eng.OpenDocument(uri, "DECLARE x INT64 DEFAULT 0;\nSELECT id FROM p.d.orders WHERE id > x;", 1, nil)

	for i := 1; i <= 50; i++ {
		sql := fmt.Sprintf(
			"DECLARE x INT64 DEFAULT %d;\nCREATE TEMP TABLE tmp AS SELECT %d AS v;\nSELECT id, v FROM p.d.orders, tmp WHERE id > x;",
			i, i,
		)
		require.NoError(t, eng.ChangeDocument(uri, i+1, []syntax.TextChange{{Text: sql}}))
	}

	entry, ok := eng.lookupEntry(uri)
	require.True(t, ok)

	entry.mu.Lock()
	astLen := len(entry.astCache)
	resolvedLen := len(entry.resolvedCache)
	astOrderLen := len(entry.astOrder)
	resolvedOrderLen := len(entry.resolvedOrder)
	entry.mu.Unlock()

	// 3 active statements + at most maxIdleStmtCacheEntries (8) retained for undo.
	const maxExpected = 3 + maxIdleStmtCacheEntries
	assert.LessOrEqual(t, astLen, maxExpected)
	assert.LessOrEqual(t, resolvedLen, maxExpected)
	assert.Equal(t, astLen, astOrderLen)
	assert.Equal(t, resolvedLen, resolvedOrderLen)
}
