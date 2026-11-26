if exists("b:current_syntax")
  finish
endif

let s:cpo_save = &cpo
set cpo&vim

syn case ignore
syn sync minlines=100

" Jinja2 inner tokens
syn region bqlangJinjaString contained start=+'+ skip=+\\\\\|\\'+ end=+'+
syn region bqlangJinjaString contained start=+"+ skip=+\\\\\|\\"+ end=+"+
syn match bqlangJinjaNumber contained /\<\(0[xX][0-9A-Fa-f_]\+\|0[oO][0-7_]\+\|0[bB][01_]\+\|[0-9][0-9_]*\(\.[0-9_]\+\)\=\([eE][+-]\=[0-9_]\+\)\=\)\>/
syn keyword bqlangJinjaKeyword contained in not and or is true false none if else as with without recursive
syn keyword bqlangJinjaTagKeyword contained if elif else endif for endfor set endset macro endmacro call endcall do raw endraw block endblock extends include import from filter endfilter with endwith break continue print autoescape endautoescape trans endtrans debug
syn keyword bqlangJinjaDbt contained ref source config var is_incremental env_var log return exceptions adapter this target execute schema database loop
syn match bqlangJinjaOperator contained @==\|!=\|<=\|>=\|\*\*\|//\|%\(}\)\@!\|-\(}\|%}\)\@!\|[|~+*/<>=]@
syn match bqlangJinjaIdent contained /\<[A-Za-z_][A-Za-z0-9_]*\>/

" Jinja2 comment, expression, block, and raw regions (raw after generic tag so {% raw %} wins)
syn region bqlangJinjaComment matchgroup=bqlangJinjaComment start="{#[-+]\=" end="[-+]\=#}" keepend containedin=bqlangString,bqlangBacktick
syn region bqlangJinjaExpr matchgroup=bqlangJinjaExpr start="{{\([-+]\)\=" end="\([-+]\)\=}}" keepend containedin=bqlangString,bqlangBacktick contains=bqlangJinjaString,bqlangJinjaNumber,bqlangJinjaKeyword,bqlangJinjaDbt,bqlangJinjaOperator,bqlangJinjaIdent
syn region bqlangJinjaTag matchgroup=bqlangJinjaTag start="{%\([-+]\)\=" end="\([-+]\)\=%}" keepend containedin=bqlangString,bqlangBacktick contains=bqlangJinjaTagKeyword,bqlangJinjaString,bqlangJinjaNumber,bqlangJinjaKeyword,bqlangJinjaDbt,bqlangJinjaOperator,bqlangJinjaIdent
syn region bqlangJinjaRaw matchgroup=bqlangJinjaRaw start="{%\([-+]\)\=\s*raw\s*\([-+]\)\=%}" end="{%\([-+]\)\=\s*endraw\s*\([-+]\)\=%}" keepend containedin=bqlangString,bqlangBacktick

" Escape sequences
syn match bqlangEscape contained /\\./

" Strings & Bytes (single-quoted before triple-quoted so later ''' / """ win at same column)
syn region bqlangString start=+\(\<\(rb\|br\|[rb]\)\)\='+ skip=+\\\\\|\\'+ end=+'\|$+ contains=bqlangEscape,bqlangJinjaExpr,bqlangJinjaTag,bqlangJinjaComment,bqlangJinjaRaw
syn region bqlangString start=+\(\<\(rb\|br\|[rb]\)\)\="+ skip=+\\\\\|\\"+ end=+"\|$+ contains=bqlangEscape,bqlangJinjaExpr,bqlangJinjaTag,bqlangJinjaComment,bqlangJinjaRaw
syn region bqlangString start=+\(\<\(rb\|br\|[rb]\)\)\='''+ skip=+\\\\\|\\'+ end=+'''+ keepend contains=bqlangEscape,bqlangJinjaExpr,bqlangJinjaTag,bqlangJinjaComment,bqlangJinjaRaw
syn region bqlangString start=+\(\<\(rb\|br\|[rb]\)\)\="""+ skip=+\\\\\|\\"+ end=+"""+ keepend contains=bqlangEscape,bqlangJinjaExpr,bqlangJinjaTag,bqlangJinjaComment,bqlangJinjaRaw

" Backtick-quoted identifiers
syn region bqlangBacktick start=+`+ skip=+\\\\\|\\`+ end=+`\|$+ contains=bqlangEscape,bqlangJinjaExpr,bqlangJinjaTag,bqlangJinjaComment,bqlangJinjaRaw

" System variables & query parameters
syn match bqlangParam /@[A-Za-z_][A-Za-z0-9_]*/
syn match bqlangSysVar /@@[A-Za-z_][A-Za-z0-9_.]*/

" Operators (generic operator before |> and comments so later rules win at same column)
syn match bqlangOperator @->\|=>\|||\|<<\|>>\|<>\|!=\|<=\|>=\|+=\|-=\|[-+*/%=<>&|^~!]@
syn match bqlangPipeOperator /|>/

" Comments (after operators so --, //, /* ... */ override -, /, *)
syn match bqlangComment /--.*$/
syn match bqlangComment /#.*$/
syn match bqlangComment /\/\/.*$/
syn region bqlangComment start="/\*" end="\*/" keepend

" Numbers
syn match bqlangNumber /\<0[xX][0-9A-Fa-f]\+\>/
syn match bqlangNumber /\<[0-9]\+\(\.[0-9]*\)\=\([eE][+-]\=[0-9]\+\)\=\>/
syn match bqlangNumber /\.[0-9]\+\([eE][+-]\=[0-9]\+\)\=\>/

" Constants & Pseudo-columns
syn keyword bqlangBoolean TRUE FALSE
syn keyword bqlangNull NULL
syn keyword bqlangPseudo _PARTITIONTIME _PARTITIONDATE _TABLE_SUFFIX

" BigQuery Types
syn keyword bqlangType INT64 INT SMALLINT INTEGER BIGINT TINYINT BYTEINT FLOAT64 FLOAT DOUBLE NUMERIC DECIMAL BIGNUMERIC BIGDECIMAL BOOL BOOLEAN STRING BYTES DATE DATETIME TIME TIMESTAMP INTERVAL GEOGRAPHY JSON UUID ANY ARRAY STRUCT RANGE MAP

" SQL Keywords (Query, DDL, DML, Procedural, Pipe, GQL)
syn keyword bqlangKeyword ACTION ACYCLIC ADD AGGREGATE ALL ALTER AND AS ASC ASSERT ASSERT_ROWS_MODIFIED AT BEGIN BETWEEN BREAK BY CALL CASE CLONE CLUSTER COLUMN CONNECTION CONSTRAINT CONTINUE COPY CORRESPONDING CREATE CROSS CUBE CURRENT DATA DECLARE DEFAULT DEFINE DELETE DESC DESCRIBE DESTINATION DISTINCT DO DROP ELSE ELSEIF ELSIF END ENFORCED ENUM EXCEPT EXCEPTION EXECUTE EXISTS EXPORT EXTEND EXTERNAL FETCH FILTER FIRST FOLLOWING FOR FOREIGN FORK FROM FULL FUNCTION GENERATED GRANT GRAPH GRAPH_TABLE GROUP GROUPS HAVING HIDDEN IDENTITY IGNORE IMMEDIATE IN INDEX INNER INPUT INSERT INTERSECT INTO IS ITERATE JOIN KEY LABELED LAST LATERAL LEAVE LET LIKE LIMIT LOAD LOOP MATCH MATCHED MATCH_RECOGNIZE MATERIALIZED MEASURE MERGE MODEL NATURAL NOT NULLS OF OFFSET ON ONLY OPTIONS OPTIONAL OR ORDER OUTER OVER OUTPUT OVERWRITE PARTITION PARTITIONS PATH PERCENT PIVOT POLICY PRECEDING PREWHERE PRIMARY PRIVATE PROCEDURE PROPERTY PROTO PUBLIC QUALIFY QUANTIFIER RAISE REBUILD RECURSIVE REFERENCES RENAME RESPECT RETURN RETURNS REVOKE ROLLUP ROW ROWS SCHEMA SELECT SET SETS SIMPLE SNAPSHOT SOME SOURCE STATIC_DESCRIBE STORED STRICT SYSTEM SYSTEM_TIME TABLE TABLESAMPLE TARGET TEE TEMP TEMPORARY THEN TOP TRAIL TRANSFORM TRUNCATE TTL UNBOUNDED UNDROP UNION UNNEST UNPIVOT UNTIL UPDATE USING VALUE VALUES VECTOR VIEW WHEN WHERE WHILE WINDOW WITH WITHIN ZONE

" BigQuery Built-in Functions
syn match bqlangBuiltin /\<\(AI\|ML\|NET\|KEYS\|AEAD\|HLL_COUNT\)\.[A-Za-z0-9_]\+\>/
syn match bqlangBuiltin /\<ST_[A-Za-z0-9_]\+\>/
syn keyword bqlangBuiltin ABS ACOS ACOSH AGG ALL_DIFFERENT ANY_VALUE APPROX_COUNT_DISTINCT APPROX_QUANTILES APPROX_TOP_COUNT APPROX_TOP_SUM ARRAY_AGG ARRAY_CONCAT ARRAY_CONCAT_AGG ARRAY_FIRST ARRAY_LAST ARRAY_LENGTH ARRAY_REVERSE ARRAY_SLICE ARRAY_TO_STRING ARRAY_TRANSFORM ASCII ASIN ASINH ATAN ATAN2 ATANH AVG BIT_AND BIT_COUNT BIT_OR BIT_XOR BYTE_LENGTH CBRT CEIL CEILING CHAR_LENGTH CHARACTER_LENGTH CHR CLASSIFIER COALESCE CODE_POINTS_TO_BYTES CODE_POINTS_TO_STRING CONCAT CONTAINS_SUBSTR CORR COS COSH COSINE_DISTANCE COT COTH COUNT COUNTIF COVAR_POP COVAR_SAMP CSC CSCH CUME_DIST CURRENT_DATE CURRENT_DATETIME CURRENT_TIME CURRENT_TIMESTAMP DATE_ADD DATE_DIFF DATE_FROM_UNIX_DATE DATE_SUB DATE_TRUNC DATETIME_ADD DATETIME_DIFF DATETIME_SUB DATETIME_TRUNC DENSE_RANK DESTINATION_NODE_ID DETERMINISTIC_DECRYPT_BYTES DETERMINISTIC_DECRYPT_STRING DETERMINISTIC_ENCRYPT DIV DOT_PRODUCT EDGES EDIT_DISTANCE ELEMENT_ID ENDS_WITH ERROR EUCLIDEAN_DISTANCE EXP EXTERNAL_OBJECT_TRANSFORM FARM_FINGERPRINT FIRST_VALUE FLOOR FORMAT FORMAT_DATE FORMAT_DATETIME FORMAT_TIME FORMAT_TIMESTAMP FROM_BASE32 FROM_BASE64 FROM_HEX GENERATE_ARRAY GENERATE_DATE_ARRAY GENERATE_TIMESTAMP_ARRAY GENERATE_UUID GRAPH_EXPAND GREATEST IEEE_DIVIDE IFNULL INITCAP INSTR IS_ACYCLIC IS_INF IS_NAN IS_SIMPLE IS_TRAIL JSON_ARRAY JSON_EXTRACT JSON_EXTRACT_ARRAY JSON_EXTRACT_SCALAR JSON_EXTRACT_STRING_ARRAY JSON_KEYS JSON_OBJECT JSON_QUERY JSON_QUERY_ARRAY JSON_REMOVE JSON_SET JSON_STRIP_NULLS JSON_TYPE JSON_VALUE JSON_VALUE_ARRAY JUSTIFY_DAYS JUSTIFY_HOURS JUSTIFY_INTERVAL LABELS LAG LAST_DAY LAST_VALUE LAX_BOOL LAX_FLOAT64 LAX_INT64 LAX_STRING LEAD LEAST LENGTH LN LOG10 LOGICAL_AND LOGICAL_OR LOWER LPAD LTRIM MAKE_INTERVAL MANHATTAN_DISTANCE MATCH_NUMBER MATCH_ROW_NUMBER MAX MAX_BY MD5 MIN MIN_BY MOD NODES NORMALIZE NORMALIZE_AND_CASEFOLD NTH_VALUE NTILE NULLIF NULLIFZERO OCTET_LENGTH ORDINAL PARSE_BIGNUMERIC PARSE_DATE PARSE_DATETIME PARSE_JSON PARSE_NUMERIC PARSE_TIME PARSE_TIMESTAMP PATH_FIRST PATH_LAST PATH_LENGTH PERCENT_RANK PERCENTILE_CONT PERCENTILE_DISC PI POW POWER PREV RAND RANGE_BUCKET RANK REGEXP_CONTAINS REGEXP_EXTRACT REGEXP_EXTRACT_ALL REGEXP_INSTR REGEXP_REPLACE REGEXP_SUBSTR REVERSE ROUND ROW_NUMBER RPAD RTRIM S2_CELLIDFROMPOINT S2_COVERINGCELLIDS SAFE_ADD SAFE_CAST SAFE_CONVERT_BYTES_TO_STRING SAFE_DIVIDE SAFE_MULTIPLY SAFE_NEGATE SAFE_OFFSET SAFE_ORDINAL SAFE_SUBTRACT SAME SESSION_USER SHA1 SHA256 SHA512 SIGN SIN SINH SOUNDEX SOURCE_NODE_ID SPLIT SQRT STARTS_WITH STDDEV STDDEV_POP STDDEV_SAMP STRING_AGG STRPOS SUBSTR SUBSTRING SUM TAN TANH TIME_ADD TIME_DIFF TIME_SUB TIME_TRUNC TIMESTAMP_ADD TIMESTAMP_DIFF TIMESTAMP_MICROS TIMESTAMP_MILLIS TIMESTAMP_SECONDS TIMESTAMP_SUB TIMESTAMP_TRUNC TO_BASE32 TO_BASE64 TO_CODE_POINTS TO_HEX TO_JSON TO_JSON_STRING TRANSLATE TRIM TRUNC TYPEOF UNICODE UNIX_DATE UNIX_MICROS UNIX_MILLIS UNIX_SECONDS UPPER VAR_POP VAR_SAMP VARIANCE VECTOR_SEARCH ZEROIFNULL

" Dual-role keyword vs function tokens (function call with '(' overrides keyword)
syn match bqlangKeyword /\<\(IF\|LEFT\|RIGHT\|CAST\|EXTRACT\|COLLATE\|NEXT\|SEARCH\|GROUPING\|REPEAT\|REPLACE\|LOG\)\>/
syn match bqlangBuiltin /\<\(IF\|LEFT\|RIGHT\|CAST\|EXTRACT\|COLLATE\|NEXT\|SEARCH\|GROUPING\|REPEAT\|REPLACE\|LOG\)\ze\s*(/

" Highlight Links
hi def link bqlangComment Comment
hi def link bqlangJinjaComment Comment
hi def link bqlangJinjaRaw String
hi def link bqlangJinjaExpr PreProc
hi def link bqlangJinjaTag PreProc
hi def link bqlangJinjaTagKeyword Keyword
hi def link bqlangJinjaKeyword Keyword
hi def link bqlangJinjaDbt Function
hi def link bqlangJinjaString String
hi def link bqlangJinjaNumber Number
hi def link bqlangJinjaOperator Operator
hi def link bqlangJinjaIdent Identifier
hi def link bqlangEscape SpecialChar
hi def link bqlangString String
hi def link bqlangBacktick Identifier
hi def link bqlangSysVar Constant
hi def link bqlangParam Identifier
hi def link bqlangPipeOperator Special
hi def link bqlangOperator Operator
hi def link bqlangNumber Number
hi def link bqlangBoolean Boolean
hi def link bqlangNull Constant
hi def link bqlangPseudo Constant
hi def link bqlangType Type
hi def link bqlangKeyword Keyword
hi def link bqlangBuiltin Function

let b:current_syntax = "bqlang"

let &cpo = s:cpo_save
unlet s:cpo_save
