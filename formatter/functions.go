package formatter

import (
	"strings"

	"github.com/goccy/go-googlesql"
)

type functionSignature struct {
	Params    []*functionParameter
	positions map[string]int
}

type functionParameter struct {
	name  string
	style PrintCase
}

func newFunctionSignature(params ...*functionParameter) *functionSignature {
	f := &functionSignature{
		Params:    params,
		positions: make(map[string]int, len(params)),
	}

	for i, p := range params {
		f.positions[p.name] = i
	}

	return f
}

func newFunctionParam(name string, style PrintCase) *functionParameter {
	return &functionParameter{name, style}
}

func (f *functionSignature) PrintCaseByName(name string) PrintCase {
	if f == nil {
		return Unspecified
	}

	if i, ok := f.positions[name]; ok {
		return f.Params[i].style
	}

	return Unspecified
}

func (f *functionSignature) PrintCaseAt(pos int) PrintCase {
	if f == nil || pos < 0 || pos >= len(f.Params) {
		return Unspecified
	}

	return f.Params[pos].style
}

func (p *printer) getFunctionSignature(n *googlesql.ASTFunctionCall) *functionSignature {
	fn, err := n.Function()
	if err != nil || !nodeDefined(fn) {
		return nil
	}

	d := make(printerData)
	pp := p.nest()
	pp.accept(fn, d)
	name := strings.ToUpper(pp.String())

	switch name {
	case "DATE_TRUNC", "DATETIME_TRUNC", "TIME_TRUNC", "TIMESTAMP_TRUNC", "LAST_DAY":
		return newFunctionSignature(
			newFunctionParam("date_expression", Unspecified),
			newFunctionParam("date_part", UpperCase),
		)
	case "NORMALIZE":
		return newFunctionSignature(
			newFunctionParam("value", Unspecified),
			newFunctionParam("normalize", UpperCase),
		)
	case "NORMALIZE_AND_CASEFOLD":
		return newFunctionSignature(
			newFunctionParam("value", Unspecified),
			newFunctionParam("normalization_mode", UpperCase),
		)
	case "WEEK":
		return newFunctionSignature(
			newFunctionParam("weekday", UpperCase),
		)
	default:
		return nil
	}
}

func newBigQueryFunctionCatalog() stringMapSet {
	return newStringMapSet(
		// Conditional expressions.
		"COALESCE", "IF", "IFNULL", "NULLIF", "ZEROIFNULL", "NULLIFZERO",
		// Aggregate functions.
		"ANY_VALUE", "ARRAY_AGG", "ARRAY_CONCAT_AGG", "AVG", "BIT_AND", "BIT_OR",
		"BIT_XOR", "COUNT", "COUNTIF", "GROUPING", "LOGICAL_AND", "LOGICAL_OR",
		"MAX", "MAX_BY", "MIN", "MIN_BY", "STRING_AGG", "SUM",
		// Statistical aggregate functions.
		"CORR", "COVAR_POP", "COVAR_SAMP", "STDDEV", "STDDEV_POP", "STDDEV_SAMP",
		"VAR_POP", "VAR_SAMP", "VARIANCE",
		// Approximate aggregate & HyperLogLog++ functions.
		"APPROX_COUNT_DISTINCT", "APPROX_QUANTILES", "APPROX_TOP_COUNT", "APPROX_TOP_SUM",
		"HLL_COUNT.EXTRACT", "HLL_COUNT.INIT", "HLL_COUNT.MERGE", "HLL_COUNT.MERGE_PARTIAL",
		// Numbering, navigation & MATCH_RECOGNIZE functions.
		"CUME_DIST", "DENSE_RANK", "NTILE", "PERCENT_RANK", "RANK", "ROW_NUMBER",
		"FIRST_VALUE", "LAG", "LAST_VALUE", "LEAD", "NTH_VALUE",
		"PERCENTILE_CONT", "PERCENTILE_DISC",
		"CLASSIFIER", "FIRST", "LAST", "MATCH_NUMBER", "MATCH_ROW_NUMBER",
		"NEXT", "PREV",
		// Bit & conversion functions.
		"BIT_COUNT", "CAST", "PARSE_BIGNUMERIC", "PARSE_NUMERIC", "SAFE_CAST",
		"ARRAY_TO_STRING", "BOOL", "DATE", "DATETIME", "FLOAT64", "FROM_BASE32",
		"FROM_BASE64", "FROM_HEX", "INT64", "PARSE_DATE", "PARSE_DATETIME",
		"PARSE_JSON", "PARSE_TIME", "PARSE_TIMESTAMP", "SAFE_CONVERT_BYTES_TO_STRING",
		"STRING", "TIME", "TIMESTAMP", "TO_BASE32", "TO_BASE64", "TO_HEX",
		"TO_JSON", "TO_JSON_STRING",
		// Mathematical & vector distance functions.
		"ABS", "ACOS", "ACOSH", "ASIN", "ASINH", "ATAN", "ATAN2", "ATANH", "CBRT",
		"CEIL", "CEILING", "COS", "COSH", "COSINE_DISTANCE", "COT", "COTH", "CSC",
		"CSCH", "DIV", "DOT_PRODUCT", "EUCLIDEAN_DISTANCE", "EXP", "FLOOR",
		"GREATEST", "IEEE_DIVIDE", "IS_INF", "IS_NAN", "LEAST", "LN", "LOG", "LOG10",
		"MANHATTAN_DISTANCE", "MOD", "PI", "POW", "POWER", "RAND", "RANGE_BUCKET",
		"ROUND", "SAFE_ADD", "SAFE_DIVIDE", "SAFE_MULTIPLY", "SAFE_NEGATE",
		"SAFE_SUBTRACT", "SEC", "SECH", "SIGN", "SIN", "SINH", "SQRT", "TAN", "TANH",
		"TRUNC",
		// Hash functions.
		"FARM_FINGERPRINT", "MD5", "SHA1", "SHA256", "SHA512",
		// String functions.
		"ASCII", "BYTE_LENGTH", "CHAR_LENGTH", "CHARACTER_LENGTH", "CHR",
		"CODE_POINTS_TO_BYTES", "CODE_POINTS_TO_STRING", "COLLATE", "CONCAT",
		"CONTAINS_SUBSTR", "EDIT_DISTANCE", "ENDS_WITH", "FORMAT", "INITCAP",
		"INSTR", "LEFT", "LENGTH", "LOWER", "LPAD", "LTRIM", "NORMALIZE",
		"NORMALIZE_AND_CASEFOLD", "OCTET_LENGTH", "REGEXP_CONTAINS",
		"REGEXP_EXTRACT", "REGEXP_EXTRACT_ALL", "REGEXP_INSTR", "REGEXP_REPLACE",
		"REGEXP_SUBSTR", "REPEAT", "REPLACE", "REVERSE", "RIGHT", "RPAD", "RTRIM",
		"SOUNDEX", "SPLIT", "STARTS_WITH", "STRPOS", "SUBSTR", "SUBSTRING",
		"TO_CODE_POINTS", "TRANSLATE", "TRIM", "UNICODE", "UPPER",
		// JSON functions.
		"JSON_ARRAY", "JSON_EXTRACT", "JSON_EXTRACT_ARRAY", "JSON_EXTRACT_SCALAR",
		"JSON_EXTRACT_STRING_ARRAY", "JSON_KEYS", "JSON_OBJECT", "JSON_QUERY",
		"JSON_QUERY_ARRAY", "JSON_REMOVE", "JSON_SET", "JSON_STRIP_NULLS",
		"JSON_TYPE", "JSON_VALUE", "JSON_VALUE_ARRAY", "LAX_BOOL", "LAX_FLOAT64",
		"LAX_INT64", "LAX_STRING",
		// Array functions.
		"ARRAY", "ARRAY_CONCAT", "ARRAY_FIRST", "ARRAY_LAST", "ARRAY_LENGTH",
		"ARRAY_REVERSE", "ARRAY_SLICE", "ARRAY_TRANSFORM", "GENERATE_ARRAY",
		"GENERATE_DATE_ARRAY", "GENERATE_TIMESTAMP_ARRAY", "OFFSET", "ORDINAL",
		"SAFE_OFFSET", "SAFE_ORDINAL",
		// Date, Datetime, Time, Timestamp & Interval functions.
		"CURRENT_DATE", "DATE_ADD", "DATE_DIFF", "DATE_FROM_UNIX_DATE", "DATE_SUB",
		"DATE_TRUNC", "EXTRACT", "FORMAT_DATE", "LAST_DAY", "UNIX_DATE",
		"CURRENT_DATETIME", "DATETIME_ADD", "DATETIME_DIFF", "DATETIME_SUB",
		"DATETIME_TRUNC", "FORMAT_DATETIME",
		"CURRENT_TIME", "FORMAT_TIME", "TIME_ADD", "TIME_DIFF", "TIME_SUB", "TIME_TRUNC",
		"CURRENT_TIMESTAMP", "FORMAT_TIMESTAMP", "TIMESTAMP_ADD", "TIMESTAMP_DIFF",
		"TIMESTAMP_MICROS", "TIMESTAMP_MILLIS", "TIMESTAMP_SECONDS", "TIMESTAMP_SUB",
		"TIMESTAMP_TRUNC", "UNIX_MICROS", "UNIX_MILLIS", "UNIX_SECONDS",
		"JUSTIFY_DAYS", "JUSTIFY_HOURS", "JUSTIFY_INTERVAL", "MAKE_INTERVAL",
		// Geography functions.
		"S2_CELLIDFROMPOINT", "S2_COVERINGCELLIDS", "ST_ANGLE", "ST_AREA",
		"ST_ASBINARY", "ST_ASGEOJSON", "ST_ASTEXT", "ST_AZIMUTH", "ST_BOUNDARY",
		"ST_BOUNDINGBOX", "ST_BUFFER", "ST_BUFFERWITHTOLERANCE", "ST_CENTROID",
		"ST_CENTROID_AGG", "ST_CLOSESTPOINT", "ST_CLUSTERDBSCAN", "ST_CONTAINS",
		"ST_CONVEXHULL", "ST_COVEREDBY", "ST_COVERS", "ST_DIFFERENCE", "ST_DIMENSION",
		"ST_DISJOINT", "ST_DISTANCE", "ST_DUMP", "ST_DWITHIN", "ST_ENDPOINT",
		"ST_EQUALS", "ST_EXTENT", "ST_EXTERIORRING", "ST_GEOGFROM",
		"ST_GEOGFROMGEOJSON", "ST_GEOGFROMTEXT", "ST_GEOGFROMWKB", "ST_GEOGPOINT",
		"ST_GEOGPOINTFROMGEOHASH", "ST_GEOHASH", "ST_GEOMETRYTYPE",
		"ST_INTERIORRINGS", "ST_INTERSECTION", "ST_INTERSECTS", "ST_INTERSECTSBOX",
		"ST_ISCLOSED", "ST_ISCOLLECTION", "ST_ISEMPTY", "ST_ISRING", "ST_LENGTH",
		"ST_LINELOCATEPOINT", "ST_MAKELINE", "ST_MAKEPOLYGON",
		"ST_MAKEPOLYGONORIENTED", "ST_MAXDISTANCE", "ST_NPOINTS", "ST_NUMGEOMETRIES",
		"ST_NUMPOINTS", "ST_PERIMETER", "ST_POINTN", "ST_SIMPLIFY", "ST_SNAPTOGRID",
		"ST_STARTPOINT", "ST_TOUCHES", "ST_UNION", "ST_UNION_AGG", "ST_WITHIN",
		"ST_X", "ST_Y",
		// Search, Security, Utility, Net & Debugging functions.
		"SEARCH", "VECTOR_SEARCH", "SESSION_USER", "GENERATE_UUID", "ERROR",
		"NET.HOST", "NET.IP_FROM_STRING", "NET.IP_NET_MASK", "NET.IP_TO_STRING",
		"NET.IP_TRUNC", "NET.IPV4_FROM_INT64", "NET.IPV4_TO_INT64",
		"NET.PUBLIC_SUFFIX", "NET.REG_DOMAIN", "NET.SAFE_IP_FROM_STRING",
		// AEAD encryption functions.
		"AEAD.DECRYPT_BYTES", "AEAD.DECRYPT_STRING", "AEAD.ENCRYPT",
		"DETERMINISTIC_DECRYPT_BYTES", "DETERMINISTIC_DECRYPT_STRING",
		"DETERMINISTIC_ENCRYPT", "KEYS.ADD_KEY_FROM_RAW_BYTES", "KEYS.KEYSET_CHAIN",
		"KEYS.KEYSET_FROM_JSON", "KEYS.KEYSET_LENGTH", "KEYS.KEYSET_TO_JSON",
		"KEYS.NEW_KEYSET", "KEYS.NEW_WRAPPED_KEYSET", "KEYS.REWRAP_KEYSET",
		"KEYS.ROTATE_KEYSET", "KEYS.ROTATE_WRAPPED_KEYSET",
		// Built-in AI, ML & TVF functions.
		"EXTERNAL_OBJECT_TRANSFORM", "AI.GENERATE", "AI.GENERATE_TEXT",
		"AI.GENERATE_TABLE", "AI.GENERATE_BOOL", "AI.GENERATE_INT",
		"AI.GENERATE_DOUBLE", "AI.FORECAST", "AI.SEARCH", "ML.PREDICT", "ML.EVALUATE",
		// Property graph (GQL), measure & utility functions.
		"AGG", "ALL_DIFFERENT", "DESTINATION_NODE_ID", "EDGES", "ELEMENT_ID",
		"GRAPH_EXPAND", "IS_ACYCLIC", "IS_SIMPLE", "IS_TRAIL", "LABELS",
		"MEASURE", "NODES", "PATH_FIRST", "PATH_LAST", "PATH_LENGTH",
		"SAME", "SOURCE_NODE_ID", "TYPEOF",
	)
}
