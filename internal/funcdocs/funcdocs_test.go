package funcdocs_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/formatter"
	"github.com/paulourio/bqlang/internal/funcdocs"
)

func TestRegistryIntegrityAndFullCatalogCoverage(t *testing.T) {
	t.Parallel()

	all := funcdocs.All()
	require.GreaterOrEqual(t, len(all), 500, "expected full BigQuery function catalog")

	// Every function recognized by formatter.BigQueryBuiltinFunctions must be documented.
	for _, name := range formatter.BigQueryBuiltinFunctions() {
		doc, ok := funcdocs.Lookup(name)
		require.True(t, ok, "missing funcdocs entry for builtin %q", name)
		assert.Equal(t, strings.ToUpper(name), doc.Name)
		assert.NotEmpty(t, doc.Summary, "empty summary for %q", name)
		assert.NotEmpty(t, doc.Description, "empty description for %q", name)
		assert.NotEmpty(t, doc.Signatures, "empty signatures for %q", name)
	}

	// Verify invariants across all 500+ documented functions.
	for _, doc := range all {
		assert.NotEmpty(t, doc.Name)
		assert.NotEmpty(t, doc.Summary, "empty summary for %s", doc.Name)
		assert.NotEmpty(t, doc.Description, "empty description for %s", doc.Name)
		require.NotEmpty(t, doc.Signatures, "no signatures for %s", doc.Name)

		for _, sig := range doc.Signatures {
			assert.NotEmpty(t, sig.Label, "empty signature label for %s", doc.Name)
			for _, param := range sig.Parameters {
				assert.NotEmpty(t, param.Label, "empty param label in %s: %s", doc.Name, sig.Label)
				assert.Contains(t, sig.Label, param.Label,
					"param label %q must appear in signature %q (%s)", param.Label, sig.Label, doc.Name)
			}
		}
	}
}

func TestLookupCaseInsensitiveAndSafePrefix(t *testing.T) {
	t.Parallel()

	doc, ok := funcdocs.Lookup("date_trunc")
	require.True(t, ok)
	assert.Equal(t, "DATE_TRUNC", doc.Name)
	assert.GreaterOrEqual(t, len(doc.Signatures), 3)

	safeDoc, isSafe, ok := funcdocs.LookupWithSafe("safe.parse_date")
	require.True(t, ok)
	assert.True(t, isSafe)
	assert.Equal(t, "PARSE_DATE", safeDoc.Name)

	// SAFE_CAST is a direct built-in function (not SAFE.CAST).
	castDoc, isSafe, ok := funcdocs.LookupWithSafe("SAFE_CAST")
	require.True(t, ok)
	assert.False(t, isSafe)
	assert.Equal(t, "SAFE_CAST", castDoc.Name)

	_, _, ok = funcdocs.LookupWithSafe("NONEXISTENT_FN_XYZ")
	assert.False(t, ok)
}

func TestByNamespaceAndFormatting(t *testing.T) {
	t.Parallel()

	aiFuncs := funcdocs.ByNamespace("ai")
	assert.NotEmpty(t, aiFuncs)

	var aiNames []string
	for _, f := range aiFuncs {
		aiNames = append(aiNames, f.Name)
	}
	assert.Contains(t, aiNames, "AI.GENERATE")
	assert.Contains(t, aiNames, "AI.FORECAST")

	doc, ok := funcdocs.Lookup("DATE_TRUNC")
	require.True(t, ok)

	hoverMD := funcdocs.FormatHoverMarkdown(doc, false, "")
	assert.Contains(t, hoverMD, "### Function `DATE_TRUNC`")
	assert.Contains(t, hoverMD, "DATE_TRUNC(")
	assert.Contains(t, hoverMD, "**Returns:**")
	assert.Contains(t, hoverMD, "**Example:**")
	assert.Contains(t, hoverMD, "https://cloud.google.com/bigquery/docs/reference/standard-sql/")

	safeHover := funcdocs.FormatHoverMarkdown(doc, true, "")
	assert.Contains(t, safeHover, "SAFE.DATE_TRUNC")
	assert.Contains(t, safeHover, "returns `NULL` instead of raising an error")

	sigMD := funcdocs.FormatSignatureDoc(doc, &doc.Signatures[0], false)
	assert.NotEmpty(t, sigMD)
	assert.Contains(t, sigMD, "**Returns:**")

	compMD := funcdocs.FormatCompletionDoc(doc)
	assert.Contains(t, compMD, "DATE_TRUNC(")
	assert.NotEmpty(t, funcdocs.PrimaryDetail(doc))
}
