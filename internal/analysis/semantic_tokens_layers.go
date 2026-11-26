package analysis

import (
	"slices"
	"strings"

	"github.com/goccy/go-googlesql"

	"github.com/paulourio/bqlang/formatter"
	"github.com/paulourio/bqlang/internal/catalog"
	"github.com/paulourio/bqlang/internal/syntax"
)

const (
	priorityScanner  uint8 = 1
	priorityAST      uint8 = 2
	priorityResolved uint8 = 3
)

var (
	builtinFuncSet = buildBuiltinFuncSet()
	dbtMacroSet    = map[string]struct{}{
		"ref":            {},
		"source":         {},
		"config":         {},
		"var":            {},
		"is_incremental": {},
		"env_var":        {},
		"log":            {},
		"return":         {},
		"exceptions":     {},
		"adapter":        {},
		"this":           {},
		"target":         {},
		"execute":        {},
		"schema":         {},
		"database":       {},
	}
	builtinTypeSet = map[string]struct{}{
		"INT64": {}, "INT": {}, "SMALLINT": {}, "INTEGER": {}, "BIGINT": {},
		"TINYINT": {}, "BYTEINT": {}, "FLOAT64": {}, "FLOAT": {}, "DOUBLE": {},
		"NUMERIC": {}, "DECIMAL": {}, "BIGNUMERIC": {}, "BIGDECIMAL": {},
		"BOOL": {}, "BOOLEAN": {}, "STRING": {}, "BYTES": {}, "DATE": {},
		"DATETIME": {}, "TIME": {}, "TIMESTAMP": {}, "INTERVAL": {}, "GEOGRAPHY": {},
		"JSON": {}, "UUID": {}, "ANY": {}, "ARRAY": {}, "STRUCT": {}, "RANGE": {}, "MAP": {},
	}
	sqlKeywordSet = buildSQLKeywordSet()
)

func buildBuiltinFuncSet() map[string]struct{} {
	list := formatter.BigQueryBuiltinFunctions()
	out := make(map[string]struct{}, len(list))
	for _, fn := range list {
		out[strings.ToUpper(fn)] = struct{}{}
	}

	return out
}

func buildSQLKeywordSet() map[string]struct{} {
	words := []string{
		"ALL", "ALTER", "AND", "ANY", "ARRAY", "AS", "ASC", "ASSERT",
		"ASSERT_ROWS_MODIFIED", "AT", "BEGIN", "BETWEEN", "BREAK", "BY",
		"CALL", "CASE", "CAST", "CLONE", "CLUSTER", "COLLATE", "CONSTRAINT",
		"CONTINUE", "COPY", "CORRESPONDING", "CREATE", "CROSS", "CUBE",
		"CURRENT", "DATA", "DECLARE", "DEFAULT", "DEFINE", "DELETE", "DESC",
		"DESCRIBE", "DISTINCT", "DO", "DROP", "ELSE", "ELSEIF", "ELSIF", "END",
		"ENFORCED", "ENUM", "EXCEPT", "EXCEPTION", "EXISTS", "EXPORT", "EXTEND",
		"EXTERNAL", "EXTRACT", "FALSE", "FETCH", "FILTER", "FIRST", "FOLLOWING",
		"FOR", "FOREIGN", "FORK", "FROM", "FULL", "FUNCTION", "GENERATED",
		"GRAPH", "GROUP", "GROUPING", "GROUPS", "HAVING", "HIDDEN", "IDENTITY",
		"IF", "IGNORE", "IN", "INNER", "INSERT", "INTERSECT", "INTERVAL", "INTO",
		"IS", "ITERATE", "JOIN", "KEY", "LAST", "LATERAL", "LEAVE", "LEFT",
		"LIKE", "LIMIT", "LOAD", "LOG", "LOOP", "MAP", "MATCH", "MATCHED",
		"MATERIALIZED", "MEASURE", "MERGE", "NATURAL", "NEXT", "NOT", "NULL",
		"NULLS", "OF", "OFFSET", "ON", "ONLY", "OPTIONS", "OR", "ORDER", "OUTER",
		"OVER", "PARTITION", "PERCENT", "PIVOT", "PRECEDING", "PREWHERE",
		"PRIMARY", "PRIVATE", "PROCEDURE", "PROTO", "PUBLIC", "QUALIFY",
		"QUANTIFIER", "RAISE", "RANGE", "RECURSIVE", "REFERENCES", "RENAME",
		"REPEAT", "REPLACE", "RESPECT", "RETURN", "RETURNS", "RIGHT", "ROLLUP",
		"ROW", "ROWS", "SAFE_CAST", "SELECT", "SET", "SETS", "SNAPSHOT", "SOME",
		"SOURCE", "STATIC_DESCRIBE", "STORED", "STRICT", "STRUCT", "SYSTEM",
		"SYSTEM_TIME", "TABLE", "TABLESAMPLE", "TARGET", "TEE", "TEMP",
		"TEMPORARY", "THEN", "TIME", "TOP", "TRUE", "TRUNCATE", "TTL",
		"UNBOUNDED", "UNION", "UNNEST", "UNPIVOT", "UNTIL", "UPDATE", "USING",
		"VALUE", "VALUES", "VIEW", "WHEN", "WHERE", "WHILE", "WINDOW", "WITH",
		"WITHIN", "ZONE",
	}

	out := make(map[string]struct{}, len(words))
	for _, w := range words {
		out[w] = struct{}{}
	}

	return out
}

func extractScannerTokens(src string, tokens []syntax.Token) []ByteSemanticToken {
	userMacros := collectJinjaMacroNames(src, tokens)
	out := make([]ByteSemanticToken, 0, len(tokens))

	for i, tok := range tokens {
		if bt, ok := classifyScannerToken(src, tokens, i, tok, userMacros); ok {
			out = append(out, bt)
		}
	}

	return out
}

func collectJinjaMacroNames(src string, tokens []syntax.Token) map[string]struct{} {
	macros := make(map[string]struct{})
	for i, tok := range tokens {
		if tok.Kind == syntax.TokenJinjaTagKeyword && tok.Text(src) == "macro" {
			if next, ok := nextNonTriviaToken(tokens, i+1); ok && next.Kind == syntax.TokenJinjaIdent {
				macros[next.Text(src)] = struct{}{}
			}
		}
	}

	return macros
}

func classifyScannerToken(
	src string,
	tokens []syntax.Token,
	idx int,
	tok syntax.Token,
	userMacros map[string]struct{},
) (ByteSemanticToken, bool) {
	if tok.End <= tok.Start {
		return ByteSemanticToken{}, false
	}

	switch tok.Kind {
	case syntax.TokenComment, syntax.TokenJinjaComment:
		return ByteSemanticToken{Start: tok.Start, End: tok.End, Type: TokenComment, Priority: priorityScanner}, true
	case syntax.TokenString, syntax.TokenBytes, syntax.TokenJinjaString:
		return ByteSemanticToken{Start: tok.Start, End: tok.End, Type: TokenString, Priority: priorityScanner}, true
	case syntax.TokenNumber, syntax.TokenJinjaNumber:
		return ByteSemanticToken{Start: tok.Start, End: tok.End, Type: TokenNumber, Priority: priorityScanner}, true
	case syntax.TokenParam:
		return ByteSemanticToken{Start: tok.Start, End: tok.End, Type: TokenParameter, Priority: priorityScanner}, true
	case syntax.TokenSysVar:
		return ByteSemanticToken{
			Start: tok.Start, End: tok.End, Type: TokenVariable,
			Modifiers: ModReadonly | ModDefaultLibrary, Priority: priorityScanner,
		}, true
	case syntax.TokenOperator, syntax.TokenJinjaOperator:
		return ByteSemanticToken{Start: tok.Start, End: tok.End, Type: TokenOperator, Priority: priorityScanner}, true
	case syntax.TokenJinjaExprOpen, syntax.TokenJinjaExprClose,
		syntax.TokenJinjaBlockOpen, syntax.TokenJinjaBlockClose, syntax.TokenJinjaTagKeyword:
		return ByteSemanticToken{Start: tok.Start, End: tok.End, Type: TokenMacro, Priority: priorityScanner}, true
	case syntax.TokenJinjaKeyword:
		return ByteSemanticToken{Start: tok.Start, End: tok.End, Type: TokenKeyword, Priority: priorityScanner}, true
	case syntax.TokenJinjaIdent:
		return classifyJinjaIdent(src, tokens, idx, tok, userMacros), true
	case syntax.TokenIdent:
		if tok.Mode == syntax.ModeSQL {
			if _, ok := sqlKeywordSet[strings.ToUpper(tok.Text(src))]; ok {
				return ByteSemanticToken{Start: tok.Start, End: tok.End, Type: TokenKeyword, Priority: priorityScanner}, true
			}
		}

		return ByteSemanticToken{}, false
	default:
		return ByteSemanticToken{}, false
	}
}

func classifyJinjaIdent(
	src string,
	tokens []syntax.Token,
	idx int,
	tok syntax.Token,
	userMacros map[string]struct{},
) ByteSemanticToken {
	name := tok.Text(src)
	if _, isMacro := userMacros[name]; isMacro {
		return ByteSemanticToken{Start: tok.Start, End: tok.End, Type: TokenMacro, Priority: priorityScanner}
	}

	if _, isDbt := dbtMacroSet[name]; isDbt {
		return ByteSemanticToken{Start: tok.Start, End: tok.End, Type: TokenMacro, Priority: priorityScanner}
	}

	if next, ok := nextNonTriviaToken(tokens, idx+1); ok && next.Kind == syntax.TokenJinjaPunct && next.Text(src) == "(" {
		return ByteSemanticToken{Start: tok.Start, End: tok.End, Type: TokenMacro, Priority: priorityScanner}
	}

	if prev, ok := prevNonTriviaToken(tokens, idx-1); ok && prev.Kind == syntax.TokenJinjaOperator && prev.Text(src) == "|" {
		return ByteSemanticToken{Start: tok.Start, End: tok.End, Type: TokenFunction, Priority: priorityScanner}
	}

	return ByteSemanticToken{Start: tok.Start, End: tok.End, Type: TokenVariable, Priority: priorityScanner}
}

func nextNonTriviaToken(tokens []syntax.Token, from int) (syntax.Token, bool) {
	for i := from; i < len(tokens); i++ {
		if !tokens[i].IsTrivia() {
			return tokens[i], true
		}
	}

	return syntax.Token{}, false
}

func prevNonTriviaToken(tokens []syntax.Token, from int) (syntax.Token, bool) {
	for i := from; i >= 0; i-- {
		if !tokens[i].IsTrivia() {
			return tokens[i], true
		}
	}

	return syntax.Token{}, false
}

// extractASTTokens walks a parsed statement AST and returns statement-relative tokens.
func extractASTTokens(stmtSQL string, root googlesql.ASTStatementNode) []ByteSemanticToken {
	if isNilNode(root) {
		return nil
	}

	w := &astTokenWalker{stmtSQL: stmtSQL}
	w.walk(root)

	return w.out
}

type astTokenWalker struct {
	stmtSQL string
	out     []ByteSemanticToken
}

func (w *astTokenWalker) emitNode(n googlesql.ASTNode, typ SemanticTokenType, mods SemanticTokenModifier) {
	s, e, ok := astNodeByteRange(n)
	if !ok || s >= e || e > len(w.stmtSQL) {
		return
	}

	w.out = append(w.out, ByteSemanticToken{
		Start:     s,
		End:       e,
		Type:      typ,
		Modifiers: mods,
		Priority:  priorityAST,
	})
}

func (w *astTokenWalker) walk(n googlesql.ASTNode) {
	if isNilNode(n) {
		return
	}

	if w.handleSpecificNode(n) {
		return
	}

	num := astNumChildren(n)
	for i := range num {
		w.walk(astChild(n, i))
	}
}

func (w *astTokenWalker) handleSpecificNode(n googlesql.ASTNode) bool {
	switch node := n.(type) {
	case *googlesql.ASTSystemVariableExpr, *googlesql.ASTParameterExpr:
		return true
	case *googlesql.ASTAliasedQuery:
		alias, _ := node.Alias()
		w.emitNode(alias, TokenClass, ModDeclaration)
		q, _ := node.Query()
		w.walk(q)
		return true
	case *googlesql.ASTTablePathExpression:
		w.handleTablePathExpr(node)
		return true
	case *googlesql.ASTAlias:
		w.handleAlias(node)
		return true
	case *googlesql.ASTFunctionCall:
		w.handleFunctionCall(node)
		return true
	case *googlesql.ASTExtractExpression:
		w.handleExtractExpr(node)
		return true
	case *googlesql.ASTIntervalExpr:
		w.handleIntervalExpr(node)
		return true
	case *googlesql.ASTNamedArgument:
		nm, _ := node.Name()
		w.emitNode(nm, TokenParameter, 0)
		ex, _ := node.Expr()
		w.walk(ex)
		return true
	case *googlesql.ASTSimpleType:
		tn, _ := node.TypeName()
		w.emitPathExpr(tn, TokenType, ModDefaultLibrary)
		return true
	case *googlesql.ASTStructField:
		nm, _ := node.Name()
		w.emitNode(nm, TokenProperty, ModDeclaration)
		tp, _ := node.Type()
		w.walk(tp)
		return true
	case *googlesql.ASTVariableDeclaration:
		w.handleVarDeclAST(node)
		return true
	case *googlesql.ASTSingleAssignment:
		v, _ := node.Variable()
		w.emitNode(v, TokenVariable, ModModification)
		ex, _ := node.Expression()
		w.walk(ex)
		return true
	case *googlesql.ASTAssignmentFromStruct:
		w.handleAssignmentFromStruct(node)
		return true
	case *googlesql.ASTColumnDefinition:
		nm, _ := node.Name()
		w.emitNode(nm, TokenProperty, ModDeclaration)
		sc, _ := node.Schema()
		w.walk(sc)
		return true
	case *googlesql.ASTGraphElementLabel:
		nm, _ := node.Name()
		w.emitNode(nm, TokenClass, 0)
		return true
	case *googlesql.ASTPathExpression:
		w.handleGenericPathExpr(node)
		return true
	case *googlesql.ASTDotIdentifier:
		ex, _ := node.Expr()
		w.walk(ex)
		nm, _ := node.Name()
		w.emitNode(nm, TokenProperty, 0)
		return true
	default:
		return w.handleTypeOrDMLNode(n)
	}
}

func (w *astTokenWalker) handleTypeOrDMLNode(n googlesql.ASTNode) bool {
	switch node := n.(type) {
	case *googlesql.ASTArrayType:
		w.emitLeadingTypeKeyword(node)
		et, _ := node.ElementType()
		w.walk(et)
		return true
	case *googlesql.ASTStructType:
		w.emitLeadingTypeKeyword(node)
		return false
	case *googlesql.ASTRangeType:
		w.emitLeadingTypeKeyword(node)
		et, _ := node.ElementType()
		w.walk(et)
		return true
	case *googlesql.ASTMapType:
		w.emitLeadingTypeKeyword(node)
		kt, _ := node.KeyType()
		vt, _ := node.ValueType()
		w.walk(kt)
		w.walk(vt)
		return true
	case *googlesql.ASTCreateTableStatement:
		nm, _ := node.Name()
		w.emitPathExpr(nm, TokenClass, ModDeclaration)
		return false
	case *googlesql.ASTCreateViewStatement:
		nm, _ := node.Name()
		w.emitPathExpr(nm, TokenClass, ModDeclaration)
		return false
	case *googlesql.ASTInsertStatement:
		tp, _ := node.GetTargetPathForNonNested()
		w.emitPathExpr(tp, TokenClass, ModModification)
		return false
	case *googlesql.ASTUpdateStatement:
		tp, _ := node.GetTargetPathForNonNested()
		w.emitPathExpr(tp, TokenClass, ModModification)
		return false
	case *googlesql.ASTDeleteStatement:
		tp, _ := node.GetTargetPathForNonNested()
		w.emitPathExpr(tp, TokenClass, ModModification)
		return false
	case *googlesql.ASTMergeStatement:
		tp, _ := node.TargetPath()
		w.emitPathExpr(tp, TokenClass, ModModification)
		return false
	case *googlesql.ASTDropStatement:
		tp, _ := node.Name()
		w.emitPathExpr(tp, TokenClass, ModModification)
		return false
	default:
		return false
	}
}

func (w *astTokenWalker) emitLeadingTypeKeyword(n googlesql.ASTNode) {
	s, e, ok := astNodeByteRange(n)
	if !ok || e > len(w.stmtSQL) {
		return
	}

	end := s
	for end < e && isIdentChar(w.stmtSQL[end]) {
		end++
	}

	if end > s {
		if _, isType := builtinTypeSet[strings.ToUpper(w.stmtSQL[s:end])]; isType {
			w.out = append(w.out, ByteSemanticToken{
				Start:     s,
				End:       end,
				Type:      TokenType,
				Modifiers: ModDefaultLibrary,
				Priority:  priorityAST,
			})
		}
	}
}

func (w *astTokenWalker) handleTablePathExpr(n *googlesql.ASTTablePathExpression) {
	pe, _ := n.PathExpr()
	w.emitTablePath(pe)

	unnest, _ := n.UnnestExpr()
	w.walk(unnest)

	alias, _ := n.Alias()
	if !isNilNode(alias) {
		id, _ := alias.Identifier()
		w.emitNode(id, TokenVariable, ModDeclaration)
	}

	forSys, _ := n.ForSystemTime()
	w.walk(forSys)

	hint, _ := n.Hint()
	w.walk(hint)

	withOff, _ := n.WithOffset()
	w.walk(withOff)
}

func (w *astTokenWalker) emitTablePath(pe *googlesql.ASTPathExpression) {
	if isNilNode(pe) {
		return
	}

	num, err := pe.NumNames()
	if err != nil || num <= 0 {
		return
	}

	for i := range num {
		id, _ := pe.Name(i)
		if i < num-1 {
			w.emitNode(id, TokenNamespace, 0)
		} else {
			w.emitNode(id, TokenClass, 0)
		}
	}
}

func (w *astTokenWalker) emitPathExpr(pe *googlesql.ASTPathExpression, leafType SemanticTokenType, mods SemanticTokenModifier) {
	if isNilNode(pe) {
		return
	}

	num, err := pe.NumNames()
	if err != nil || num <= 0 {
		return
	}

	for i := range num {
		id, _ := pe.Name(i)
		if i < num-1 {
			w.emitNode(id, TokenNamespace, mods)
		} else {
			w.emitNode(id, leafType, mods)
		}
	}
}

func (w *astTokenWalker) handleAlias(n *googlesql.ASTAlias) {
	id, _ := n.Identifier()
	parent := astParent(n)
	if _, isSelectCol := parent.(*googlesql.ASTSelectColumn); isSelectCol {
		w.emitNode(id, TokenProperty, ModDeclaration)

		return
	}

	w.emitNode(id, TokenVariable, ModDeclaration)
}

func (w *astTokenWalker) handleFunctionCall(n *googlesql.ASTFunctionCall) {
	fnPath, _ := n.Function()
	fnName := w.emitFunctionPath(fnPath)

	unitArgIdx := positionalUnitArgIndex(fnName)
	num := astNumChildren(n)
	argIdx := 0
	for i := 1; i < num; i++ {
		c := astChild(n, i)
		if isNilNode(c) {
			continue
		}

		isExpr, err := c.IsExpression()
		if err == nil && isExpr {
			if argIdx == unitArgIdx && isUnitIdentifierNode(c) {
				w.emitNode(c, TokenEnumMember, 0)
			} else {
				w.walk(c)
			}
			argIdx++

			continue
		}

		w.walk(c)
	}
}

func (w *astTokenWalker) emitFunctionPath(pe *googlesql.ASTPathExpression) string {
	if isNilNode(pe) {
		return ""
	}

	parts, _ := pe.ToIdentifierVector()
	full := strings.Join(parts, ".")
	_, isBuiltin := builtinFuncSet[strings.ToUpper(full)]

	num, _ := pe.NumNames()
	if num >= 2 && strings.EqualFold(parts[0], "SAFE") {
		n0, _ := pe.Name(0)
		w.emitNode(n0, TokenKeyword, 0)

		rest := strings.Join(parts[1:], ".")
		var mods SemanticTokenModifier
		if _, ok := builtinFuncSet[strings.ToUpper(rest)]; ok {
			mods = ModDefaultLibrary
		}

		for i := int32(1); i < num; i++ {
			id, _ := pe.Name(i)
			if i < num-1 {
				w.emitNode(id, TokenNamespace, mods)
			} else {
				w.emitNode(id, TokenFunction, mods)
			}
		}

		return rest
	}

	var mods SemanticTokenModifier
	if isBuiltin {
		mods = ModDefaultLibrary
	}

	for i := range num {
		id, _ := pe.Name(i)
		if i < num-1 {
			w.emitNode(id, TokenNamespace, mods)
		} else {
			w.emitNode(id, TokenFunction, mods)
		}
	}

	return full
}

func positionalUnitArgIndex(fnName string) int {
	switch strings.ToUpper(fnName) {
	case "DATE_TRUNC", "DATETIME_TRUNC", "TIME_TRUNC", "TIMESTAMP_TRUNC",
		"LAST_DAY", "NORMALIZE", "NORMALIZE_AND_CASEFOLD":
		return 1
	case "DATE_DIFF", "DATETIME_DIFF", "TIME_DIFF", "TIMESTAMP_DIFF":
		return 2
	case "WEEK":
		return 0
	default:
		return -1
	}
}

func isUnitIdentifierNode(n googlesql.ASTNode) bool {
	switch v := n.(type) {
	case *googlesql.ASTIdentifier:
		return true
	case *googlesql.ASTPathExpression:
		num, err := v.NumNames()
		return err == nil && num == 1
	default:
		return false
	}
}

func (w *astTokenWalker) handleExtractExpr(n *googlesql.ASTExtractExpression) {
	lhs, _ := n.LhsExpr()
	if isUnitIdentifierNode(lhs) {
		w.emitNode(lhs, TokenEnumMember, 0)
	} else {
		w.walk(lhs)
	}

	rhs, _ := n.RhsExpr()
	w.walk(rhs)

	tz, _ := n.TimeZoneExpr()
	w.walk(tz)
}

func (w *astTokenWalker) handleIntervalExpr(n *googlesql.ASTIntervalExpr) {
	iv, _ := n.IntervalValue()
	w.walk(iv)

	dp1, _ := n.DatePartName()
	w.emitNode(dp1, TokenEnumMember, 0)

	dp2, _ := n.DatePartNameTo()
	w.emitNode(dp2, TokenEnumMember, 0)
}

func (w *astTokenWalker) handleVarDeclAST(n *googlesql.ASTVariableDeclaration) {
	vl, _ := n.VariableList()
	num := astNumChildren(vl)
	for i := range num {
		w.emitNode(astChild(vl, i), TokenVariable, ModDeclaration)
	}

	tp, _ := n.Type()
	w.walk(tp)

	dv, _ := n.DefaultValue()
	w.walk(dv)
}

func (w *astTokenWalker) handleAssignmentFromStruct(n *googlesql.ASTAssignmentFromStruct) {
	vl, _ := n.Variables()
	num := astNumChildren(vl)
	for i := range num {
		w.emitNode(astChild(vl, i), TokenVariable, ModModification)
	}

	ex, _ := n.StructExpression()
	w.walk(ex)
}

func (w *astTokenWalker) handleGenericPathExpr(pe *googlesql.ASTPathExpression) {
	num, err := pe.NumNames()
	if err != nil || num <= 0 {
		return
	}

	if num == 1 {
		id, _ := pe.Name(0)
		w.emitNode(id, TokenProperty, 0)

		return
	}

	for i := range num {
		id, _ := pe.Name(i)
		if i == 0 {
			w.emitNode(id, TokenVariable, 0)
		} else {
			w.emitNode(id, TokenProperty, 0)
		}
	}
}

type colOrigin struct {
	meta     *catalog.TableMeta
	colName  string
	isPseudo bool
	path     []string
}

// extractResolvedTokens walks ResolvedAST and returns statement-relative leaf tokens.
func extractResolvedTokens(
	stmtSQL string,
	rs googlesql.ResolvedStatementNode,
	cat *catalog.Catalog,
) []ByteSemanticToken {
	if isNilNode(rs) || cat == nil {
		return nil
	}

	origins := buildColumnOriginMap(rs, cat)
	stmtTokens := syntax.Scan(stmtSQL)
	visited := make(map[googlesql.ResolvedNode]bool)
	var out []ByteSemanticToken

	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		if visited[n] {
			return false
		}

		tok, descend, ok := extractResolvedSingleToken(stmtSQL, stmtTokens, n, origins, visited)
		if ok {
			out = append(out, tok)
		}

		return descend
	})

	return out
}

func extractResolvedSingleToken(
	stmtSQL string,
	stmtTokens []syntax.Token,
	n googlesql.ResolvedNode,
	origins map[int32]colOrigin,
	visited map[googlesql.ResolvedNode]bool,
) (tok ByteSemanticToken, descend, ok bool) {
	switch node := n.(type) {
	case *googlesql.ResolvedGetStructField:
		t, found := resolveStructFieldToken(stmtSQL, stmtTokens, node, origins, visited)
		return t, false, found
	case *googlesql.ResolvedColumnRef:
		visited[node] = true
		t, found := resolveColumnRefToken(stmtSQL, stmtTokens, node, origins)
		return t, false, found
	case *googlesql.ResolvedConstant:
		if s, e, has := resolvedLeafIdentSpan(stmtSQL, stmtTokens, node); has {
			return ByteSemanticToken{Start: s, End: e, Type: TokenVariable, Priority: priorityResolved}, false, true
		}

		return ByteSemanticToken{}, false, false
	default:
		return ByteSemanticToken{}, true, false
	}
}

func resolveStructFieldToken(
	stmtSQL string,
	stmtTokens []syntax.Token,
	outer *googlesql.ResolvedGetStructField,
	origins map[int32]colOrigin,
	visited map[googlesql.ResolvedNode]bool,
) (ByteSemanticToken, bool) {
	var fieldIdxs []int32
	var cur googlesql.ResolvedExprNode = outer

	for !isNilNode(cur) {
		visited[cur] = true
		gsf, ok := cur.(*googlesql.ResolvedGetStructField)
		if !ok {
			break
		}

		idx, err := gsf.FieldIdx()
		if err != nil {
			return ByteSemanticToken{}, false
		}

		fieldIdxs = append(fieldIdxs, idx)
		cur, _ = gsf.Expr()
	}

	ref, ok := cur.(*googlesql.ResolvedColumnRef)
	if !ok || isNilNode(ref) {
		return ByteSemanticToken{}, false
	}

	visited[ref] = true
	rc, err := ref.Column()
	if err != nil || isNilNode(rc) {
		return ByteSemanticToken{}, false
	}

	colID, _ := rc.ColumnId()
	origin, hasOrigin := origins[colID]
	if !hasOrigin || origin.meta == nil {
		return ByteSemanticToken{}, false
	}

	slices.Reverse(fieldIdxs)
	fullPath := resolveFieldPathFromIndices(origin, fieldIdxs)
	if fullPath == "" {
		return ByteSemanticToken{}, false
	}

	s, e, hasLoc := resolvedLeafIdentSpan(stmtSQL, stmtTokens, outer)
	if !hasLoc {
		return ByteSemanticToken{}, false
	}

	mods := modifiersForColumn(origin.meta, fullPath, origin.isPseudo)

	return ByteSemanticToken{
		Start:     s,
		End:       e,
		Type:      TokenProperty,
		Modifiers: mods,
		Priority:  priorityResolved,
	}, true
}

func resolveFieldPathFromIndices(origin colOrigin, fieldIdxs []int32) string {
	baseCol, ok := origin.meta.FindField(origin.path...)
	if !ok {
		return ""
	}

	parts := append([]string(nil), origin.path...)
	cur := baseCol
	for _, idx := range fieldIdxs {
		next, found := cur.FieldAt(int(idx))
		if !found {
			return ""
		}

		parts = append(parts, next.Name)
		cur = next
	}

	return strings.Join(parts, ".")
}

func resolveColumnRefToken(
	stmtSQL string,
	stmtTokens []syntax.Token,
	ref *googlesql.ResolvedColumnRef,
	origins map[int32]colOrigin,
) (ByteSemanticToken, bool) {
	rc, err := ref.Column()
	if err != nil || isNilNode(rc) {
		return ByteSemanticToken{}, false
	}

	colID, _ := rc.ColumnId()
	origin, ok := origins[colID]
	if !ok {
		return ByteSemanticToken{}, false
	}

	s, e, hasLoc := resolvedLeafIdentSpan(stmtSQL, stmtTokens, ref)
	if !hasLoc {
		return ByteSemanticToken{}, false
	}

	colPath := origin.colName
	if len(origin.path) > 0 {
		colPath = strings.Join(origin.path, ".")
	}

	mods := modifiersForColumn(origin.meta, colPath, origin.isPseudo)

	return ByteSemanticToken{
		Start:     s,
		End:       e,
		Type:      TokenProperty,
		Modifiers: mods,
		Priority:  priorityResolved,
	}, true
}

func modifiersForColumn(meta *catalog.TableMeta, colPath string, isPseudo bool) SemanticTokenModifier {
	var mods SemanticTokenModifier
	if isPseudo || strings.EqualFold(colPath, "_PARTITIONTIME") ||
		strings.EqualFold(colPath, "_PARTITIONDATE") ||
		strings.EqualFold(colPath, "_TABLE_SUFFIX") {
		mods |= ModPseudo
	}

	if meta != nil && meta.IsPartitionColumn(colPath) {
		mods |= ModPartitioned
	}

	if meta != nil {
		if _, ok := meta.ClusterRank(colPath); ok {
			mods |= ModClustered
		}
	}

	return mods
}

const maxOriginPropIters = 64

func resolvedLeafIdentSpan(stmtSQL string, stmtTokens []syntax.Token, n googlesql.ResolvedNode) (int, int, bool) {
	s, e, ok := resolvedNodeByteRange(n)
	if !ok || e > len(stmtSQL) || s >= e {
		return 0, 0, false
	}

	_, idx, found := syntax.TokenAt(stmtTokens, e-1)
	if !found {
		return 0, 0, false
	}

	for idx >= 0 && stmtTokens[idx].Start >= s && stmtTokens[idx].IsTrivia() {
		idx--
	}

	if idx < 0 {
		return 0, 0, false
	}

	tok := stmtTokens[idx]
	if tok.Start >= s && tok.End <= e &&
		(tok.Kind == syntax.TokenIdent || tok.Kind == syntax.TokenBacktickIdent) {
		return tok.Start, tok.End, true
	}

	return 0, 0, false
}

func buildColumnOriginMap(rs googlesql.ResolvedNode, cat *catalog.Catalog) map[int32]colOrigin {
	origins := make(map[int32]colOrigin)
	withEntries := make(map[string][]*googlesql.ResolvedColumn)

	// Pass 1: collect direct table scans and WITH entry output columns.
	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		switch node := n.(type) {
		case *googlesql.ResolvedTableScan:
			collectTableScanOrigins(node, cat, origins)
		case *googlesql.ResolvedWithEntry:
			qname, _ := node.WithQueryName()
			sub, _ := node.WithSubquery()
			if !isNilNode(sub) && qname != "" {
				withEntries[strings.ToLower(qname)] = scanColumnList(sub)
			}
		default:
		}

		return true
	})

	// Pass 2: propagate origins across projections, WITH refs, set ops, and array scans until fixed point.
	changed := true
	for iter := 0; changed && iter < maxOriginPropIters; iter++ {
		changed = false
		walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
			if propagateNodeOrigins(n, origins, withEntries) {
				changed = true
			}

			return true
		})
	}

	return origins
}

func collectTableScanOrigins(scan *googlesql.ResolvedTableScan, cat *catalog.Catalog, origins map[int32]colOrigin) {
	tbl, err := scan.Table()
	if err != nil || isNilNode(tbl) {
		return
	}

	meta, _ := cat.LookupTable(tbl)
	colList, _ := scan.ColumnList()
	idxList, _ := scan.ColumnIndexList()
	tblColList, _ := scan.TableColumnList()

	for i, rc := range colList {
		if isNilNode(rc) {
			continue
		}

		colID, _ := rc.ColumnId()
		colName, _ := rc.Name()
		isPseudo := false

		if tblCol := resolveScanTableColumn(tbl, i, idxList, tblColList); !isNilNode(tblCol) {
			if tn, nErr := tblCol.Name(); nErr == nil && tn != "" {
				colName = tn
			}

			if ps, pErr := tblCol.IsPseudoColumn(); pErr == nil {
				isPseudo = ps
			}
		}

		origins[colID] = colOrigin{
			meta:     meta,
			colName:  colName,
			isPseudo: isPseudo,
			path:     []string{colName},
		}
	}
}

func resolveScanTableColumn(
	tbl googlesql.TableNode,
	i int,
	idxList []int32,
	tblColList []googlesql.Googlesql_ColumnNode,
) googlesql.Googlesql_ColumnNode {
	if i < len(idxList) {
		if col, err := tbl.GetColumn(idxList[i]); err == nil && !isNilNode(col) {
			return col
		}
	}

	if i < len(tblColList) && !isNilNode(tblColList[i]) {
		return tblColList[i]
	}

	return nil
}

func propagateNodeOrigins(
	n googlesql.ResolvedNode,
	origins map[int32]colOrigin,
	withEntries map[string][]*googlesql.ResolvedColumn,
) bool {
	changed := false

	switch node := n.(type) {
	case *googlesql.ResolvedProjectScan:
		exprs, _ := node.ExprList()
		for _, cc := range exprs {
			if propagateComputedCol(cc, origins) {
				changed = true
			}
		}
	case *googlesql.ResolvedAggregateScan:
		groups, _ := node.GroupByList()
		for _, cc := range groups {
			if propagateComputedCol(cc, origins) {
				changed = true
			}
		}
	case *googlesql.ResolvedWithRefScan:
		qname, _ := node.WithQueryName()
		refCols, _ := node.ColumnList()
		changed = linkOriginCols(refCols, withEntries[strings.ToLower(qname)], origins)
	case *googlesql.ResolvedSetOperationScan:
		outCols, _ := node.ColumnList()
		items, _ := node.InputItemList()
		for _, item := range items {
			if !isNilNode(item) {
				inCols, _ := item.OutputColumnList()
				if linkOriginCols(outCols, inCols, origins) {
					changed = true
				}
			}
		}
	case *googlesql.ResolvedArrayScan:
		changed = propagateArrayScan(node, origins)
	default:
	}

	return changed
}

func setColOrigin(origins map[int32]colOrigin, dstID int32, orig colOrigin) bool {
	if _, exists := origins[dstID]; exists {
		return false
	}

	origins[dstID] = orig

	return true
}

func linkOriginCols(dstCols, srcCols []*googlesql.ResolvedColumn, origins map[int32]colOrigin) bool {
	changed := false
	for i := 0; i < len(dstCols) && i < len(srcCols); i++ {
		if isNilNode(dstCols[i]) || isNilNode(srcCols[i]) {
			continue
		}

		srcID, _ := srcCols[i].ColumnId()
		dstID, _ := dstCols[i].ColumnId()
		if orig, ok := origins[srcID]; ok {
			if setColOrigin(origins, dstID, orig) {
				changed = true
			}
		}
	}

	return changed
}

func propagateComputedCol(cc *googlesql.ResolvedComputedColumn, origins map[int32]colOrigin) bool {
	if isNilNode(cc) {
		return false
	}

	dstCol, _ := cc.Column()
	expr, _ := cc.Expr()
	if isNilNode(dstCol) || isNilNode(expr) {
		return false
	}

	if orig, ok := extractExprOrigin(expr, origins); ok {
		dstID, _ := dstCol.ColumnId()
		return setColOrigin(origins, dstID, orig)
	}

	return false
}

func propagateArrayScan(node *googlesql.ResolvedArrayScan, origins map[int32]colOrigin) bool {
	arrExpr, _ := node.ArrayExpr()
	elemCol, _ := node.ElementColumn()
	if isNilNode(arrExpr) || isNilNode(elemCol) {
		return false
	}

	if orig, ok := extractExprOrigin(arrExpr, origins); ok {
		elemID, _ := elemCol.ColumnId()
		return setColOrigin(origins, elemID, orig)
	}

	return false
}

func extractExprOrigin(expr googlesql.ResolvedExprNode, origins map[int32]colOrigin) (colOrigin, bool) {
	var fieldIdxs []int32
	cur := expr

	for !isNilNode(cur) {
		switch node := cur.(type) {
		case *googlesql.ResolvedGetStructField:
			idx, err := node.FieldIdx()
			if err != nil {
				return colOrigin{}, false
			}
			fieldIdxs = append(fieldIdxs, idx)
			cur, _ = node.Expr()
		case *googlesql.ResolvedColumnRef:
			rc, err := node.Column()
			if err != nil || isNilNode(rc) {
				return colOrigin{}, false
			}
			colID, _ := rc.ColumnId()
			orig, ok := origins[colID]
			if !ok {
				return colOrigin{}, false
			}
			if len(fieldIdxs) == 0 {
				return orig, true
			}
			slices.Reverse(fieldIdxs)
			return extendOriginWithFields(orig, fieldIdxs)
		default:
			return colOrigin{}, false
		}
	}

	return colOrigin{}, false
}

func extendOriginWithFields(orig colOrigin, fieldIdxs []int32) (colOrigin, bool) {
	if orig.meta == nil {
		return colOrigin{}, false
	}

	cur, ok := orig.meta.FindField(orig.path...)
	if !ok {
		return colOrigin{}, false
	}

	path := append([]string(nil), orig.path...)
	for _, idx := range fieldIdxs {
		next, found := cur.FieldAt(int(idx))
		if !found {
			return colOrigin{}, false
		}
		path = append(path, next.Name)
		cur = next
	}

	return colOrigin{
		meta:     orig.meta,
		colName:  cur.Name,
		isPseudo: orig.isPseudo,
		path:     path,
	}, true
}

func walkResolvedNodes(root googlesql.ResolvedNode, fn func(googlesql.ResolvedNode) bool) {
	if isNilNode(root) {
		return
	}

	if !fn(root) {
		return
	}

	children, err := root.GetChildNodes()
	if err != nil {
		return
	}

	for _, c := range children {
		walkResolvedNodes(c, fn)
	}
}

func isIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}
