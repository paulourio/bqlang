package catalog_test

import (
	"fmt"
	"runtime"
	"testing"
	"time"
	"weak"

	"github.com/goccy/go-googlesql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/bqconfig"
	"github.com/paulourio/bqlang/catalog"
)

func TestCatalogForAnalysisNoReferenceCycle(t *testing.T) {
	require.NoError(t, bqlang.Init())

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

	po, err := googlesql.NewParserOptions()
	require.NoError(t, err)
	require.NoError(t, po.SetLanguageOptions(catalog.DefaultLSPLanguageOptions()))

	ao, err := googlesql.NewAnalyzerOptions(catalog.DefaultLSPLanguageOptions())
	require.NoError(t, err)

	const iterations = 40
	weakCats := make([]weak.Pointer[catalog.Catalog], 0, iterations)
	weakOverlays := make([]weak.Pointer[catalog.ScriptOverlay], 0, iterations)
	weakTFs := make([]weak.Pointer[googlesql.TypeFactory], 0, iterations)

	for i := range iterations {
		tf, tfErr := googlesql.NewTypeFactory()
		require.NoError(t, tfErr)

		ov, ovErr := catalog.NewScriptOverlay(tf)
		require.NoError(t, ovErr)

		varType, typeErr := catalog.AnalyzeType("INT64", ao, tf)
		require.NoError(t, typeErr)
		require.NoError(t, ov.AddVariable("x", varType))

		cat, catErr := catalog.NewCatalogForAnalysis(store, nil, ov)
		require.NoError(t, catErr)

		sql := fmt.Sprintf("SELECT id, amount FROM p.d.orders WHERE id > x + %d", i)
		resume, rErr := googlesql.NewParseResumeLocationFromString(sql)
		require.NoError(t, rErr)

		pout, pErr := googlesql.ParseNextScriptStatement(resume, po)
		require.NoError(t, pErr)

		stmt, sErr := pout.Statement()
		require.NoError(t, sErr)

		aout, aErr := googlesql.AnalyzeStatementFromParserAST(stmt, ao, sql, cat.GoogleSQLCatalog(), tf)
		require.NoError(t, aErr)
		require.NotNil(t, aout)

		// Also exercise AddTempTable with a TypeNode derived from AnalyzeStatementFromParserAST
		// to ensure no AnalyzerOutput -> Catalog -> ScriptOverlay cycle is retained.
		intType, _ := tf.GetInt64()
		require.NoError(t, ov.AddTempTable(catalog.TableRef{Table: "tmp_t"}, []catalog.OverlayColumn{
			{Name: "v", Type: intType},
		}))

		weakCats = append(weakCats, weak.Make(cat))
		weakOverlays = append(weakOverlays, weak.Make(ov))
		weakTFs = append(weakTFs, weak.Make(tf))
	}

	require.Eventually(t, func() bool {
		runtime.GC()

		for i := range weakCats {
			if weakCats[i].Value() != nil || weakOverlays[i].Value() != nil || weakTFs[i].Value() != nil {
				return false
			}
		}

		return true
	}, 5*time.Second, 20*time.Millisecond, "Catalog, ScriptOverlay, and TypeFactory handles must be garbage-collected without reference cycles")

	for i := range weakCats {
		assert.Nil(t, weakCats[i].Value())
		assert.Nil(t, weakOverlays[i].Value())
		assert.Nil(t, weakTFs[i].Value())
	}
}
