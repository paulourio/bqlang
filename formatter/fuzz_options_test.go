package formatter_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/goccy/go-googlesql"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/extensions"
	"github.com/paulourio/bqlang/extensions/ast"
	"github.com/paulourio/bqlang/formatter"
)

const expectedPrintOptionsFieldCount = 28

type formatCorpusCase struct {
	name       string
	file       string
	input      string
	parserOpts *googlesql.ParserOptions

	baselineOnce sync.Once
	baseline     corpusCaseBaseline
}

type corpusCaseBaseline struct {
	comments     []*extensions.Comment
	templates    []*extensions.TemplateElement
	extErr       error
	astSignature string
	hasAST       bool
}

type optionProfile struct {
	name       string
	opts       *formatter.PrintOptions
	withLogger bool
}

var (
	fuzzPrintCases = []formatter.PrintCase{
		formatter.Unspecified,
		formatter.AsIs,
		formatter.LowerCase,
		formatter.UpperCase,
	}
	fuzzStringStyles = []formatter.StringStyle{
		formatter.UnspecifiedStringStyle,
		formatter.AsIsStringStyle,
		formatter.PreferSingleQuote,
		formatter.PreferDoubleQuote,
	}
	fuzzWhenPolicies = []formatter.When{
		formatter.UnspecifiedWhen,
		formatter.Never,
		formatter.Always,
		formatter.Auto,
	}
	fuzzFunctionCatalogs = []formatter.FunctionCatalog{
		formatter.EmptyCatalog,
		formatter.BigQueryCatalog,
	}
	fuzzSoftMaxColumns = []int{0, 1, 2, 10, 20, 40, 60, 80, 100, 120, 200, 1000}
	fuzzIndentations   = []int{0, 1, 2, 3, 4, 6, 8, 12}
	fuzzSmallCounts    = []int{0, 1, 2, 3, 4, 8, 16}

	discardTestLogger = log.New(io.Discard, "", 0)

	supplementalFormatInputs = []string{
		"SELECT foo",
		"SELECT 'hello', b'world'",
		`SELECT 'abc', R'raw', b'xyz', RB'rbytes', 'has "quote"'`,
		`SELECT 'abc' "def", 'one' 'two', b'x' b"y", NUMERIC '1e2' "3"`,
		"SELECT '''line1   \nline2  ''' AS s\n",
		"SELECT 1.5e10 AS a, 0xAbCdEf AS h, TRUE AS b, NULL AS n, CASE WHEN x = 1 THEN 'a' ELSE 'b' END AS c FROM (SELECT 1 AS x)",
		"{{ stmt }}\nselect {{ myCol }}, baz from {{ myTable }}",
		"SELECT {{ col }} FROM t -- note",
		"SELECT a FROM t WHERE {% if is_selected %}x = 1{% endif %}",
		"SELECT hll_count.init(x), to_json(x), net.ipv4_to_int64(b), safe.substr(s, 1, 2), myCustomUdf(x) FROM t",
		"SELECT MyCol, _partitiondate, _table_suffix FROM MyDataset.MyTable WHERE MyCol = @MyParam AND @@project_id = 'p'",
		"SELECT 'https://example.com' AS a, -- first\n'http://short.io' AS b,\n'foo--bar' AS c,\n'tag#1' AS d,\n`x--2` AS e -- fifth\nFROM t",
		"DROP TABLE my_dataset.`MyCaseSensitiveTable`",
		"DROP ALL ROW ACCESS POLICIES ON MyDataset.`MyTable`",
		"CREATE TABLE t (enforced_id INT64, PRIMARY KEY (enforced_id) OPTIONS (description = 'not enforced'))",
		"MERGE dst t USING src s ON t.id = s.id WHEN NOT MATCHED AND target_flag = 1 THEN INSERT ROW",
		"CREATE RESERVATION my_res OPTIONS (slot_capacity = 100)",
		"ALTER RESERVATION my_res SET OPTIONS (slot_capacity = 200)",
		"DROP RESERVATION my_res",
		"CREATE ROW ACCESS POLICY MyPol ON MyDataset.MyTable GRANT TO ('user:a@b.com') FILTER USING (a = 1)",
		"CREATE SEARCH INDEX MyIdx ON MyDataset.MyTable (ALL COLUMNS)",
		"ALTER SEARCH INDEX MyIdx ON MyDataset.MyTable REBUILD",
		"DROP SEARCH INDEX MyIdx ON MyDataset.MyTable",
		"CREATE MODEL m TRANSFORM (a, b, c) OPTIONS (model_type = 'linear_reg') AS SELECT 1 AS a, 2 AS b, 3 AS c",
		"INSERT INTO t (a, b, c, d, e) VALUES (1, 2, 3, 4, 5) THEN RETURN a, b, c",
		"LOOP SELECT 1; BREAK; END LOOP;",
		"op: LOOP BREAK op; END LOOP op;",
		"le: WHILE true DO BREAK le; END WHILE;",
		"SELECT (a << 1) + 2, 1 + (a << 2), (NOT a) = TRUE, NOT (NOT a NOT LIKE b)",
		`SELECT 'it\'s', "say \"hi\"", "has \' single", 'has \" double', r'raw\n', r"raw\"q", r'raw\'q', '''a''b''', """a""b""", 'foo(bar)baz', b'it\'s', RB"raw\"q"`,
		"SELECT a FROM A UNION ALL CORRESPONDING SELECT b FROM B",
		"SELECT 1 FULL UNION ALL SELECT 2",
		"SELECT 1 UNION ALL STRICT CORRESPONDING BY (a) SELECT 2",
		"SELECT 1 |> UNION ALL BY NAME (SELECT 2) |> INTERSECT DISTINCT BY NAME ON (x) (SELECT 3)",
		"(SELECT 1 UNION ALL SELECT 2 |> WHERE 2) |> WHERE 3",
		"SELECT WITH kind ALL OPTIONS (a = 1), another_column FROM t",
		"WITH RECURSIVE t AS (SELECT 1 AS n UNION ALL SELECT n + 1 FROM t WHERE n < 5) WITH DEPTH AS d BETWEEN 1 AND 3 SELECT * FROM t",
		"FROM t |> SELECT @{hint=1} SUM(x) OVER w WINDOW w AS (ORDER BY y)",
		"UPDATE T SET x = y, (DELETE a WITH OFFSET WHERE TRUE), (INSERT y.z VALUES (5) ASSERT_ROWS_MODIFIED 1), (UPDATE a SET b = c WHERE TRUE) WHERE TRUE ASSERT_ROWS_MODIFIED 2",
		"INSERT OR IGNORE replace SELECT 1",
		"INSERT OR REPLACE INTO T (a, b) VALUES (1, 2) ON CONFLICT (a) DO UPDATE SET b = 3 WHERE a > 0 ASSERT_ROWS_MODIFIED 1",
		"INSERT INTO T (a) (SELECT 1) ON CONFLICT DO NOTHING",
		"MERGE INTO T USING S ON t1 = s1 WHEN NOT MATCHED BY SOURCE THEN INSERT (t1) ROW",
		"UPDATE a-100.b.c SET x = y",
		"CREATE VIEW p-g-123.d456.v1 AS SELECT * FROM t2",
		"CREATE PROCEDURE p() EXTERNAL SECURITY INVOKER OPTIONS (a = b) LANGUAGE PYTHON",
		"ALTER TABLE t ADD CONSTRAINT IF NOT EXISTS c CHECK (a > 0) OPTIONS (description = 'ok')",
		"DROP SEQUENCE IF EXISTS s; DROP CONNECTION IF EXISTS c;",
		"CREATE EXTERNAL TABLE t (x INT64) WITH PARTITION COLUMNS WITH CONNECTION a.b.c OPTIONS (x = 'bar')",
		"REVOKE DELETE, UPDATE(col2) ON VIEW foo FROM 'mdbgroup/bar'",
		"DROP FUNCTION IF EXISTS foo(INT64, STRING)",
		"CREATE FUNCTION f(a ANY TYPE AS b, x INT64 AS y) SQL SECURITY INVOKER AS (a)",
		"CREATE RECURSIVE VIEW v SQL SECURITY DEFINER AS SELECT 1",
		"ALTER TABLE t ADD COLUMN c STRING PRECEDING b FILL USING a + 1",
		"ALTER TABLE t ADD FOREIGN KEY (a) REFERENCES u (b) MATCH FULL ON DELETE CASCADE ON UPDATE RESTRICT OPTIONS (k = 'v')",
		"CREATE MODEL m INPUT (a INT64) OUTPUT (b FLOAT64) OPTIONS (model_type = 'linear_reg')",
		"BEGIN TRANSACTION ISOLATION LEVEL SERIALIZABLE, READ ONLY",
		"SELECT (1).new, (1).(x), (1.).(x), (1).*",
		"SELECT * FROM UNNEST([1, 2] AS a1, [2, 3] AS a2)",
		"SELECT 1 FROM UNNEST([1], mode => e -> e)",
		"FROM t |> RECURSIVE UNION ALL WITH DEPTH AS d BETWEEN 1 AND 5 (|> SELECT x + 1 AS x) AS rec",
		"FROM t |> IF @{hint=1} x = 1 THEN (|> WHERE a > 0) ELSEIF x = 2 THEN (|> WHERE b > 0) ELSE (|> WHERE c > 0)",
		"FROM t |> FORK @{hint=1} (|> WHERE a > 0), (|> WHERE b > 0)",
		"FROM t |> TEE (|> WHERE a > 0) |> LOG (|> AGGREGATE COUNT(*))",
		"(FROM t1) AS q1 |> SELECT *",
		"(((SELECT 1) AS q1) AS q2) AS q3",
		"CREATE OR REPLACE SEQUENCE IF NOT EXISTS ds.seq OPTIONS (description = 's')",
		"ALTER SEQUENCE IF EXISTS ds.seq SET OPTIONS (description = 'u')",
		"CLONE DATA INTO ds.dst FROM ds.src1 FOR SYSTEM_TIME AS OF CURRENT_TIMESTAMP() UNION ALL ds.src2",
		`ALTER RESERVATION my_proj.my_res SET AS JSON r'{"k": "v"}'`,
		"SET TRANSACTION ISOLATION LEVEL SERIALIZABLE, READ ONLY",
		"SELECT REPLACE_FIELDS(proto_col, 1 AS f1, 'x' AS (nested.f2))",
		"SELECT SAFE.CAST(1 AS INT32), ARRAY_ZIP(a1 AS f1)",
		"SELECT SUM(SEQUENCE s1)",
		"CREATE FUNCTION f(x FUNCTION<(INT64, STRING) -> BOOL>, y FUNCTION<() -> INT64>(0)) AS (1)",
	}

	corpusOnce  sync.Once
	corpusCases []*formatCorpusCase
)

// TestPrintOptionsFuzz collects all formatting test inputs across testdata/*.toml
// and supplemental cases, formats them under systematic boundary profiles and
// deterministic fuzzed PrintOptions combinations, and verifies metamorphic
// formatting invariants (error-free formatting, idempotence, comment and Jinja2
// template preservation, canonical AST equivalence, and exact decoded literal
// preservation).
func TestPrintOptionsFuzz(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		expectedPrintOptionsFieldCount,
		reflect.TypeFor[formatter.PrintOptions]().NumField(),
		"PrintOptions field count changed; update decodePrintOptions and stress profiles",
	)

	cases := loadFormatCorpus(t)
	require.NotEmpty(t, cases)

	stressProfiles := buildStressOptionProfiles(t)
	structuralProfiles := buildStructuralStressProfiles()
	reps := selectRepresentativeCaseIndices(cases)

	var nerr atomic.Int32

	for i, tc := range cases {
		_, isRep := reps[i]

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if nerr.Load() > 10 {
				t.Skipf("skipping after %d failures", nerr.Load())
			}

			assigned := make([]optionProfile, 0, 12)
			assigned = append(assigned, stressProfiles[i%len(stressProfiles)])

			if !testing.Short() {
				rng := rand.New(rand.NewPCG(uint64(i)+1, 0xb9fa7097))
				for r := range 2 {
					raw := randomOptionBytes(rng)
					opts, withLogger := decodePrintOptions(raw)
					assigned = append(assigned, optionProfile{
						name:       fmt.Sprintf("rand_%d", r),
						opts:       opts,
						withLogger: withLogger,
					})
				}

				if isRep {
					assigned = append(assigned, structuralProfiles...)
				}
			}

			for _, prof := range assigned {
				if !checkFormatInvariants(t, tc, prof) {
					nerr.Add(1)

					return
				}
			}
		})
	}
}

// FuzzPrintOptions fuzzes PrintOptions combinations against the collected
// formatting test corpus, checking error-free formatting, idempotence,
// comment/template preservation, AST equivalence, and literal preservation.
func FuzzPrintOptions(f *testing.F) {
	cases := loadFormatCorpus(f)
	require.NotEmpty(f, cases)

	seeds := buildFuzzSeedBytes()
	step := max(len(cases)/len(seeds), 1)

	for i, raw := range seeds {
		caseIdx := uint16((i * step) % len(cases))
		f.Add(caseIdx, raw)
	}

	f.Fuzz(func(t *testing.T, caseIdx uint16, rawOpts []byte) {
		tc := cases[int(caseIdx)%len(cases)]
		opts, withLogger := decodePrintOptions(rawOpts)

		if opts != nil {
			require.NoError(t, opts.Validate())
		}

		prof := optionProfile{
			name:       "fuzz",
			opts:       opts,
			withLogger: withLogger,
		}

		if !checkFormatInvariants(t, tc, prof) {
			t.FailNow()
		}
	})
}

func loadFormatCorpus(tb testing.TB) []*formatCorpusCase {
	tb.Helper()

	require.NoError(tb, bqlang.Init())

	corpusOnce.Do(func() {
		entries, err := os.ReadDir("testdata")
		require.NoError(tb, err)

		collected := make([]*formatCorpusCase, 0, 1250)

		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".toml" {
				continue
			}

			td := MustReadTest(tb, path.Join("testdata", entry.Name()))
			parserOpts := getParserOpts(td)

			for i, c := range td.Cases {
				collected = append(collected, &formatCorpusCase{
					name:       fmt.Sprintf("%s:case_%d", entry.Name(), i),
					file:       entry.Name(),
					input:      c.Input,
					parserOpts: parserOpts,
				})
			}
		}

		defaultPO := bqlang.DefaultParserOptions()
		for i, sql := range supplementalFormatInputs {
			collected = append(collected, &formatCorpusCase{
				name:       fmt.Sprintf("supplemental:case_%d", i),
				file:       fmt.Sprintf("supplemental_%d", i),
				input:      sql,
				parserOpts: defaultPO,
			})
		}

		corpusCases = collected
	})

	return corpusCases
}

func (tc *formatCorpusCase) getBaseline() *corpusCaseBaseline {
	tc.baselineOnce.Do(func() {
		comms, elems, extErr := extensions.ExtractAll(tc.input)
		tc.baseline = corpusCaseBaseline{
			comments:  comms,
			templates: elems,
			extErr:    extErr,
		}

		if extErr == nil && strings.TrimSpace(tc.input) != "" && onlyTemplateComments(elems) {
			src := tc.input
			if len(elems) > 0 {
				src = extensions.EraseComments(src, comms)
			}

			if root, err := parseScript(src, tc.parserOpts); err == nil && root != nil {
				tc.baseline.astSignature = canonicalASTSignature(root)
				tc.baseline.hasAST = true
			}
		}
	})

	return &tc.baseline
}

func selectRepresentativeCaseIndices(cases []*formatCorpusCase) map[int]struct{} {
	seenFiles := make(map[string]struct{}, 96)
	reps := make(map[int]struct{}, 128)

	for i, tc := range cases {
		if _, ok := seenFiles[tc.file]; !ok {
			seenFiles[tc.file] = struct{}{}
			reps[i] = struct{}{}
		}
	}

	return reps
}

func checkFormatInvariants(t *testing.T, tc *formatCorpusCase, prof optionProfile) bool {
	t.Helper()

	fmtOpts := []formatter.Option{
		formatter.WithParserOptions(tc.parserOpts),
		formatter.WithPrintOptions(prof.opts),
	}
	if prof.withLogger {
		fmtOpts = append(fmtOpts, formatter.WithLogger(discardTestLogger))
	}

	bqf := formatter.NewBigQueryFormatter(fmtOpts...)

	formatted, err := bqf.Format(tc.input)
	if err != nil {
		t.Errorf(
			"first-pass format error on %s (profile %s): %v\n%s",
			tc.name,
			prof.name,
			err,
			formatFuzzDiagnostic(tc, prof, formatted, ""),
		)

		return false
	}

	if strings.TrimSpace(tc.input) != "" && formatted == "" {
		t.Errorf(
			"empty formatted output on non-empty input %s (profile %s)\n%s",
			tc.name,
			prof.name,
			formatFuzzDiagnostic(tc, prof, formatted, ""),
		)

		return false
	}

	bqf.Logger = nil

	formatted2, err2 := bqf.Format(formatted)
	if err2 != nil {
		t.Errorf(
			"second-pass format/parse error on %s (profile %s): %v\n%s",
			tc.name,
			prof.name,
			err2,
			formatFuzzDiagnostic(tc, prof, formatted, ""),
		)

		return false
	}

	if formatted != formatted2 {
		t.Errorf(
			"formatting is not idempotent on %s (profile %s)\n%s",
			tc.name,
			prof.name,
			formatFuzzDiagnostic(tc, prof, formatted, formatted2),
		)

		return false
	}

	base := tc.getBaseline()

	outComms, outElems, ok := checkExtensionsPreserved(t, tc, prof, base, formatted)
	if !ok {
		return false
	}

	return checkASTPreserved(t, tc, prof, base, formatted, outComms, outElems)
}

func checkExtensionsPreserved(
	t *testing.T,
	tc *formatCorpusCase,
	prof optionProfile,
	base *corpusCaseBaseline,
	formatted string,
) ([]*extensions.Comment, []*extensions.TemplateElement, bool) {
	t.Helper()

	if base.extErr != nil {
		return nil, nil, true
	}

	outComms, outElems, outExtErr := extensions.ExtractAll(formatted)
	if outExtErr != nil {
		t.Errorf(
			"extension extraction failed on formatted output for %s (profile %s): %v\n%s",
			tc.name,
			prof.name,
			outExtErr,
			formatFuzzDiagnostic(tc, prof, formatted, ""),
		)

		return nil, nil, false
	}

	if len(base.comments) != len(outComms) {
		t.Errorf(
			"comment count changed on %s (profile %s): input=%d formatted=%d\n%s",
			tc.name,
			prof.name,
			len(base.comments),
			len(outComms),
			formatFuzzDiagnostic(tc, prof, formatted, ""),
		)

		return nil, nil, false
	}

	for i, inC := range base.comments {
		outC := outComms[i]
		if inC.Kind != outC.Kind || normalizeExtensionImage(inC.Image) != normalizeExtensionImage(outC.Image) {
			t.Errorf(
				"comment[%d] mutated on %s (profile %s): input=(%s, %q) formatted=(%s, %q)\n%s",
				i,
				tc.name,
				prof.name,
				inC.Kind,
				inC.Image,
				outC.Kind,
				outC.Image,
				formatFuzzDiagnostic(tc, prof, formatted, ""),
			)

			return nil, nil, false
		}
	}

	if len(base.templates) != len(outElems) {
		t.Errorf(
			"template element count changed on %s (profile %s): input=%d formatted=%d\n%s",
			tc.name,
			prof.name,
			len(base.templates),
			len(outElems),
			formatFuzzDiagnostic(tc, prof, formatted, ""),
		)

		return nil, nil, false
	}

	for i, inE := range base.templates {
		outE := outElems[i]
		if inE.Kind != outE.Kind || normalizeExtensionImage(inE.Image) != normalizeExtensionImage(outE.Image) {
			t.Errorf(
				"template[%d] mutated on %s (profile %s): input=(%s, %q) formatted=(%s, %q)\n%s",
				i,
				tc.name,
				prof.name,
				inE.Kind,
				inE.Image,
				outE.Kind,
				outE.Image,
				formatFuzzDiagnostic(tc, prof, formatted, ""),
			)

			return nil, nil, false
		}
	}

	return outComms, outElems, true
}

func checkASTPreserved(
	t *testing.T,
	tc *formatCorpusCase,
	prof optionProfile,
	base *corpusCaseBaseline,
	formatted string,
	outComms []*extensions.Comment,
	outElems []*extensions.TemplateElement,
) bool {
	t.Helper()

	if !base.hasAST {
		return true
	}

	src := formatted
	if len(outElems) > 0 {
		src = extensions.EraseComments(src, outComms)
	}

	outAST, err := parseScript(src, tc.parserOpts)
	if err != nil || outAST == nil {
		t.Errorf(
			"AST parse error on formatted output for %s (profile %s): %v\n%s",
			tc.name,
			prof.name,
			err,
			formatFuzzDiagnostic(tc, prof, formatted, ""),
		)

		return false
	}

	outSig := canonicalASTSignature(outAST)
	if base.astSignature != outSig {
		t.Errorf(
			"canonical AST / literal value mismatch on %s (profile %s): %s\n%s",
			tc.name,
			prof.name,
			firstDiffLine(base.astSignature, outSig),
			formatFuzzDiagnostic(tc, prof, formatted, ""),
		)

		return false
	}

	return true
}

func onlyTemplateComments(elems []*extensions.TemplateElement) bool {
	for _, e := range elems {
		if e.Kind != ast.TemplateComment {
			return false
		}
	}

	return true
}

// normalizeExtensionImage strips trailing line endings (line comments at EOF
// gain a trailing newline when formatted) and trailing horizontal spaces per
// line (matching rowsTrimRight in formatter.Format).
func normalizeExtensionImage(s string) string {
	s = strings.TrimRight(s, "\r\n")
	lines := strings.Split(s, "\n")

	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}

	return strings.Join(lines, "\n")
}

// canonicalASTSignature builds a structural and semantic signature of root that
// is invariant across PrintOptions:
//   - preserves tree topology, NodeKind, operators, and node flags/modifiers;
//   - normalizes case-configurable tokens (identifiers, booleans, nulls, hex/float literals);
//   - ignores raw quote/prefix delimiters on string/bytes literal components while
//     preserving exact component counts and exact decoded StringValue()/BytesValue()
//     (folding exponent case only for strings under NUMERIC/BIGNUMERIC literals).
func canonicalASTSignature(root googlesql.ASTNode) string {
	var b strings.Builder

	var walk func(n googlesql.ASTNode, depth int, underNumeric bool)
	walk = func(n googlesql.ASTNode, depth int, underNumeric bool) {
		if !astNodeDefined(n) {
			return
		}

		k, _ := n.NodeKind()
		nextUnderNumeric := underNumeric ||
			k == googlesql.ASTNodeKindAstNumericLiteral ||
			k == googlesql.ASTNodeKindAstBignumericLiteral

		for range depth {
			b.WriteByte(' ')
		}

		switch v := n.(type) {
		case *googlesql.ASTStringLiteral:
			sv, _ := v.StringValue()
			if underNumeric {
				sv = strings.ToLower(sv)
			}

			fmt.Fprintf(&b, "StringLiteral(%q)\n", sv)
		case *googlesql.ASTStringLiteralComponent:
			b.WriteString("StringLiteralComponent\n")
		case *googlesql.ASTBytesLiteral:
			bv, _ := v.BytesValue()
			fmt.Fprintf(&b, "BytesLiteral(%q)\n", bv)
		case *googlesql.ASTBytesLiteralComponent:
			b.WriteString("BytesLiteralComponent\n")
		case *googlesql.ASTIdentifier,
			*googlesql.ASTBooleanLiteral,
			*googlesql.ASTNullLiteral,
			*googlesql.ASTIntLiteral,
			*googlesql.ASTFloatLiteral:
			s, _ := n.SingleNodeDebugString()
			b.WriteString(strings.ToLower(s))
			b.WriteByte('\n')
		default:
			s, _ := n.SingleNodeDebugString()
			b.WriteString(s)
			b.WriteByte('\n')
		}

		num, err := n.NumChildren()
		if err != nil || num <= 0 {
			return
		}

		for i := range num {
			c, cerr := n.Child(i)
			if cerr == nil && astNodeDefined(c) {
				walk(c, depth+1, nextUnderNumeric)
			}
		}
	}

	walk(root, 0, false)

	return b.String()
}

func astNodeDefined(n googlesql.ASTNode) bool {
	if n == nil {
		return false
	}

	v := reflect.ValueOf(n)

	return v.Kind() != reflect.Pointer || !v.IsNil()
}

func firstDiffLine(expected, actual string) string {
	expLines := strings.Split(expected, "\n")
	actLines := strings.Split(actual, "\n")
	limit := min(len(expLines), len(actLines))

	for i := range limit {
		if expLines[i] != actLines[i] {
			return fmt.Sprintf("line %d:\n  input:     %q\n  formatted: %q", i+1, expLines[i], actLines[i])
		}
	}

	return fmt.Sprintf("line count mismatch: input=%d formatted=%d", len(expLines), len(actLines))
}

func formatFuzzDiagnostic(tc *formatCorpusCase, prof optionProfile, formatted, formatted2 string) string {
	optsJSON := "<nil>"
	if prof.opts != nil {
		if raw, err := json.MarshalIndent(prof.opts, "", "  "); err == nil {
			optsJSON = string(raw)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "WithLogger: %v\nPrintOptions:\n%s\n\nInput:\n%s\n", prof.withLogger, optsJSON, tc.input)

	if formatted != "" {
		fmt.Fprintf(&b, "\nFirst-pass Formatted:\n%s\n", formatted)
	}

	if formatted2 != "" {
		fmt.Fprintf(&b, "\nSecond-pass Formatted:\n%s\n", formatted2)
	}

	return b.String()
}

const optionByteVectorLen = 24

func randomOptionBytes(rng *rand.Rand) []byte {
	buf := make([]byte, optionByteVectorLen)
	for i := range buf {
		buf[i] = byte(rng.UintN(256))
	}

	// Bias toward custom populated PrintOptions (mode >= 2) while occasionally sampling nil/zero.
	if buf[0]&0x0f < 2 {
		buf[0] |= 0x04
	}

	return buf
}

func decodePrintOptions(raw []byte) (*formatter.PrintOptions, bool) {
	var b [optionByteVectorLen]byte
	copy(b[:], raw)

	withLogger := b[0]&0x80 != 0

	switch b[0] & 0x0f {
	case 0:
		return nil, withLogger
	case 1:
		return &formatter.PrintOptions{}, withLogger
	}

	flags := b[2]
	pickCase := func(v byte) formatter.PrintCase {
		return fuzzPrintCases[int(v)%len(fuzzPrintCases)]
	}
	pickStr := func(v byte) formatter.StringStyle {
		return fuzzStringStyles[int(v)%len(fuzzStringStyles)]
	}
	pickCount := func(v byte) int {
		return fuzzSmallCounts[int(v)%len(fuzzSmallCounts)]
	}

	opts := &formatter.PrintOptions{
		SoftMaxColumns:                 fuzzSoftMaxColumns[int(b[1])%len(fuzzSoftMaxColumns)],
		NewlineBeforeClause:            flags&(1<<0) != 0,
		AlignLogicalWithClauses:        flags&(1<<1) != 0,
		AlignTrailingComments:          flags&(1<<2) != 0,
		IndentCaseWhen:                 flags&(1<<3) != 0,
		IndentWithClause:               flags&(1<<4) != 0,
		IndentWithEntries:              flags&(1<<5) != 0,
		ColumnListTrailingComma:        fuzzWhenPolicies[int(b[3])%len(fuzzWhenPolicies)],
		Indentation:                    fuzzIndentations[int(b[4])%len(fuzzIndentations)],
		MinJoinsToSeparateInBlocks:     pickCount(b[5]),
		MaxColumnsForSingleLineSelect:  pickCount(b[6]),
		MaxParamsForSingleLineFunction: pickCount(b[7]),
		FunctionCatalog:                fuzzFunctionCatalogs[int(b[8])%len(fuzzFunctionCatalogs)],
		BuiltinFunctionNameStyle:       pickCase(b[9]),
		FunctionNameStyle:              pickCase(b[10]),
		IdentifierStyle:                pickCase(b[11]),
		KeywordStyle:                   pickCase(b[12]),
		TypeStyle:                      pickCase(b[13]),
		BoolStyle:                      pickCase(b[14]),
		HexStyle:                       pickCase(b[15]),
		NumericStyle:                   pickCase(b[16]),
		NullStyle:                      pickCase(b[17]),
		PseudoColumnStyle:              pickCase(b[18]),
		TableNameStyle:                 pickCase(b[19]),
		QueryParameterStyle:            pickCase(b[20]),
		SystemVariableStyle:            pickCase(b[21]),
		BytesStyle:                     pickStr(b[22]),
		StringStyle:                    pickStr(b[23]),
	}

	return opts, withLogger
}

func buildStructuralStressProfiles() []optionProfile {
	noNewline := formatter.DefaultPrintOptions()
	noNewline.NewlineBeforeClause = false
	noNewline.AlignLogicalWithClauses = false
	noNewline.AlignTrailingComments = false

	trailingAlways := formatter.DefaultPrintOptions()
	trailingAlways.ColumnListTrailingComma = formatter.Always
	trailingAlways.MaxColumnsForSingleLineSelect = 0
	trailingAlways.MaxParamsForSingleLineFunction = 0

	doubleQuotesLower := formatter.DefaultPrintOptions()
	doubleQuotesLower.StringStyle = formatter.PreferDoubleQuote
	doubleQuotesLower.BytesStyle = formatter.PreferDoubleQuote
	doubleQuotesLower.KeywordStyle = formatter.LowerCase
	doubleQuotesLower.IdentifierStyle = formatter.UpperCase
	doubleQuotesLower.BuiltinFunctionNameStyle = formatter.LowerCase
	doubleQuotesLower.FunctionNameStyle = formatter.LowerCase
	doubleQuotesLower.TypeStyle = formatter.LowerCase
	doubleQuotesLower.BoolStyle = formatter.LowerCase
	doubleQuotesLower.NullStyle = formatter.LowerCase
	doubleQuotesLower.HexStyle = formatter.LowerCase
	doubleQuotesLower.NumericStyle = formatter.UpperCase
	doubleQuotesLower.SoftMaxColumns = 20

	return []optionProfile{
		{name: "zero", opts: &formatter.PrintOptions{}},
		{name: "no_newline_before_clause", opts: noNewline},
		{name: "trailing_comma_always", opts: trailingAlways},
		{name: "double_quotes_lower_narrow", opts: doubleQuotesLower},
	}
}

func buildStressOptionProfiles(tb testing.TB) []optionProfile {
	tb.Helper()

	profiles := []optionProfile{
		{name: "nil", opts: nil},
		{name: "zero", opts: &formatter.PrintOptions{}},
		{name: "default_with_logger", opts: formatter.DefaultPrintOptions(), withLogger: true},
	}

	addMutated := func(name string, fn func(*formatter.PrintOptions)) {
		p := formatter.DefaultPrintOptions()
		fn(p)
		require.NoError(tb, p.Validate())
		profiles = append(profiles, optionProfile{name: name, opts: p})
	}

	addMutated("soft_max_cols_0", func(p *formatter.PrintOptions) { p.SoftMaxColumns = 0 })
	addMutated("soft_max_cols_1", func(p *formatter.PrintOptions) { p.SoftMaxColumns = 1 })
	addMutated("soft_max_cols_40", func(p *formatter.PrintOptions) { p.SoftMaxColumns = 40 })
	addMutated("soft_max_cols_1000", func(p *formatter.PrintOptions) { p.SoftMaxColumns = 1000 })
	addMutated("newline_before_clause_false", func(p *formatter.PrintOptions) { p.NewlineBeforeClause = false })
	addMutated("align_logical_false", func(p *formatter.PrintOptions) { p.AlignLogicalWithClauses = false })
	addMutated("align_trailing_comments_false", func(p *formatter.PrintOptions) { p.AlignTrailingComments = false })
	addMutated("trailing_comma_unspecified", func(p *formatter.PrintOptions) { p.ColumnListTrailingComma = formatter.UnspecifiedWhen })
	addMutated("trailing_comma_never", func(p *formatter.PrintOptions) { p.ColumnListTrailingComma = formatter.Never })
	addMutated("trailing_comma_always", func(p *formatter.PrintOptions) {
		p.ColumnListTrailingComma = formatter.Always
		p.MaxColumnsForSingleLineSelect = 0
	})
	addMutated("indentation_0", func(p *formatter.PrintOptions) { p.Indentation = 0 })
	addMutated("indentation_1", func(p *formatter.PrintOptions) { p.Indentation = 1 })
	addMutated("indentation_8", func(p *formatter.PrintOptions) { p.Indentation = 8 })
	addMutated("indent_case_when_false", func(p *formatter.PrintOptions) { p.IndentCaseWhen = false })
	addMutated("indent_with_clause_false", func(p *formatter.PrintOptions) { p.IndentWithClause = false })
	addMutated("indent_with_entries_true", func(p *formatter.PrintOptions) { p.IndentWithEntries = true })
	addMutated("min_joins_0", func(p *formatter.PrintOptions) { p.MinJoinsToSeparateInBlocks = 0 })
	addMutated("min_joins_5", func(p *formatter.PrintOptions) { p.MinJoinsToSeparateInBlocks = 5 })
	addMutated("max_cols_single_line_0", func(p *formatter.PrintOptions) { p.MaxColumnsForSingleLineSelect = 0 })
	addMutated("max_cols_single_line_16", func(p *formatter.PrintOptions) { p.MaxColumnsForSingleLineSelect = 16 })
	addMutated("max_params_single_line_0", func(p *formatter.PrintOptions) { p.MaxParamsForSingleLineFunction = 0 })
	addMutated("max_params_single_line_16", func(p *formatter.PrintOptions) { p.MaxParamsForSingleLineFunction = 16 })
	addMutated("catalog_empty", func(p *formatter.PrintOptions) { p.FunctionCatalog = formatter.EmptyCatalog })

	for _, pc := range fuzzPrintCases {
		addMutated("all_cases_"+string(pc), func(p *formatter.PrintOptions) {
			p.BuiltinFunctionNameStyle = pc
			p.FunctionNameStyle = pc
			p.IdentifierStyle = pc
			p.KeywordStyle = pc
			p.TypeStyle = pc
			p.BoolStyle = pc
			p.HexStyle = pc
			p.NumericStyle = pc
			p.NullStyle = pc
			p.PseudoColumnStyle = pc
			p.TableNameStyle = pc
			p.QueryParameterStyle = pc
			p.SystemVariableStyle = pc
		})
	}

	addMutated("contrasting_identifier_styles", func(p *formatter.PrintOptions) {
		p.IdentifierStyle = formatter.LowerCase
		p.KeywordStyle = formatter.AsIs
		p.PseudoColumnStyle = formatter.UpperCase
		p.TableNameStyle = formatter.UpperCase
		p.QueryParameterStyle = formatter.UpperCase
		p.SystemVariableStyle = formatter.LowerCase
		p.BuiltinFunctionNameStyle = formatter.LowerCase
		p.FunctionNameStyle = formatter.UpperCase
	})

	for _, ss := range fuzzStringStyles {
		addMutated("string_bytes_"+string(ss), func(p *formatter.PrintOptions) {
			p.StringStyle = ss
			p.BytesStyle = ss
		})
	}

	return profiles
}

func buildFuzzSeedBytes() [][]byte {
	seeds := make([][]byte, 0, 24)

	// Explicit nil and zero PrintOptions seeds (with and without logger).
	seeds = append(seeds,
		[]byte{0x00},
		[]byte{0x01},
		[]byte{0x80},
		[]byte{0x81},
	)

	rng := rand.New(rand.NewPCG(20261120, 0x5eedcafe))
	for range 20 {
		seeds = append(seeds, randomOptionBytes(rng))
	}

	return seeds
}
