package formatter_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/formatter"
)

var adversarialFormatInputSeeds = []string{
	"",
	"   \n\t  ",
	"\x00",
	"\xff\xfe",
	"SELECT",
	"SELECT 1",
	"SELECT 'unclosed",
	`SELECT "unclosed`,
	"SELECT '''unclosed\nline2",
	"SELECT `unclosed",
	"SELECT /* unclosed",
	"SELECT 1 -- eof",
	"SELECT 1 // eof",
	"SELECT 1 # eof",
	"SELECT 1 /* inline */ FROM t",
	"SELECT 'a' 'b', 'c' \"d\", b'e' b\"f\"",
	"SELECT '\x21\x1a10', '\x21\x1a11', '\x21\x1a12', '\v'",
	"SELECT {{ col }} FROM {{ tbl }}",
	"SELECT {{ unclosed FROM t",
	"SELECT {% if x %} 1 {% endif %} FROM t",
	"{% for a in b %}\nSELECT {{ a }}\n{% endfor %}",
	"{% set x = 1 -%}\n{# comment #}\nSELECT 1",
	"{# unclosed jinja comment\nSELECT 1",
	"SELECT 1 + (2 * (3 - 4))",
	"SELECT (((",
	"SELECT )))",
	"SELECT 1 FROM t WHERE a = 1 AND (b = 2 OR c = 3)",
	"FROM t |> WHERE a = 1 |> EXTEND a + 1 AS b |> AGGREGATE COUNT(*) GROUP BY b",
	"GRAPH g MATCH (a:N)-[e:E]->(b:N) RETURN a.id AS id",
	"BEGIN DECLARE x INT64 DEFAULT 1; LOOP BREAK; END LOOP; END;",
	"MERGE dst USING src ON dst.id = src.id WHEN MATCHED THEN DELETE",
	"CREATE TABLE ds.t (a INT64, b STRING) PARTITION BY DATE(ts) CLUSTER BY a",
}

// FuzzFormatInput fuzzes formatter.SQLFormatter.Format with arbitrary,
// malformed, and mutated SQL/Jinja2 inputs paired with fuzzed PrintOptions.
// It verifies crash/panic safety on all inputs and checks that any input
// accepted without error produces non-empty output that also formats without
// panicking or failing on a second pass.
func FuzzFormatInput(f *testing.F) {
	require.NoError(f, bqlang.Init())

	optSeeds := buildFuzzSeedBytes()
	for i, sql := range adversarialFormatInputSeeds {
		f.Add(sql, optSeeds[i%len(optSeeds)])
	}

	parserOpts := defaultTestParserOptions()

	f.Fuzz(func(t *testing.T, input string, rawOpts []byte) {
		// Cap extreme fuzzer-generated buffers to keep per-execution time bounded.
		if len(input) > 8192 {
			return
		}

		opts, withLogger := decodePrintOptions(rawOpts)

		fmtOpts := []formatter.Option{
			formatter.WithParserOptions(parserOpts),
			formatter.WithPrintOptions(opts),
		}
		if withLogger {
			fmtOpts = append(fmtOpts, formatter.WithLogger(discardTestLogger))
		}

		bqf := formatter.NewBigQueryFormatter(fmtOpts...)

		formatted, err := bqf.Format(input)
		if err != nil {
			return
		}

		if strings.TrimSpace(input) == "" {
			require.Empty(t, formatted)

			return
		}

		require.NotEmpty(t, formatted)

		bqf.Logger = nil
		if _, err2 := bqf.Format(formatted); err2 != nil {
			t.Fatalf(
				"second-pass format error on fuzzed input: %v\nInput (%q):\n%s\n\nFirst-pass Formatted (%q):\n%s",
				err2,
				input,
				input,
				formatted,
				formatted,
			)
		}
	})
}
