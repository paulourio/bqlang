package analysis

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/goccy/go-googlesql"

	"github.com/paulourio/bqlang/internal/catalog"
	"github.com/paulourio/bqlang/internal/funcdocs"
	"github.com/paulourio/bqlang/internal/syntax"
)

// SignatureHelp returns LSP 3.17 signature help for the innermost callable
// BigQuery function, script-local UDF, or dbt/Jinja2 macro enclosing pos in uri,
// or (nil, nil) when pos is outside a function call.
func (e *Engine) SignatureHelp(
	ctx context.Context,
	uri string,
	pos syntax.Position,
	labelOffsetSupport bool,
) (*SignatureHelp, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	entry, snap, err := e.ensureSnapshot(uri)
	if err != nil {
		return nil, err
	}

	origOff := snap.LineIndex.ByteOffset(pos)
	if isRawOrCommentCursor(snap.Tokens, origOff) {
		return nil, nil //nolint:nilnil // LSP signatureHelp returns null inside comments/raw blocks
	}

	if inJinja, _ := cursorJinjaMode(snap.Tokens, origOff); inJinja {
		return signatureHelpInJinja(snap.Text, snap.Tokens, origOff, labelOffsetSupport), nil
	}

	branchSnap, stmtSQL, localOff := completionBranchAndStatementSlice(snap, origOff)
	if localOff <= 0 || localOff > len(stmtSQL) {
		return nil, nil //nolint:nilnil // cursor outside SQL statement
	}

	frames := scanCallStack(stmtSQL, localOff)
	if len(frames) == 0 {
		return nil, nil //nolint:nilnil // cursor not inside parentheses
	}

	localUDFs := extractLocalUDFs(branchSnap)

	for _, frame := range slices.Backward(frames) {
		if frame.funcName == "" {
			continue
		}

		totalArgs := countTotalCallArgs(stmtSQL, localOff, frame.argIndex)
		argIdx := adjustSpecialArgIndex(frame, stmtSQL, localOff)

		if doc, isSafe, ok := funcdocs.LookupWithSafe(frame.funcName); ok {
			return buildFuncDocSignatureHelp(doc, isSafe, argIdx, totalArgs, frame.namedArg, labelOffsetSupport), nil
		}

		if udf, ok := localUDFs[strings.ToUpper(frame.funcName)]; ok {
			return buildFuncDocSignatureHelp(udf, false, argIdx, totalArgs, frame.namedArg, labelOffsetSupport), nil
		}

		if sh := e.signatureHelpFromResolvedWASM(entry, branchSnap, origOff, frame.funcName, argIdx, labelOffsetSupport); sh != nil {
			return sh, nil
		}
	}

	return nil, nil //nolint:nilnil // no callable function enclosing cursor
}

type callFrame struct {
	funcName       string
	openTokIdx     int
	argStartTokIdx int
	argIndex       int
	bracketDepth   int
	sawComma       bool
	namedArg       string
}

func scanCallStack(stmtSQL string, localOff int) []callFrame {
	prefix := stmtSQL[:localOff]
	toks := syntax.Scan(prefix)
	var stack []callFrame

	for i, tok := range toks {
		if tok.IsTrivia() || tok.Kind != syntax.TokenPunct {
			continue
		}

		stack = applyCallStackPunct(stack, prefix, toks, i, tok.Text(prefix))
	}

	for i := range stack {
		stack[i].namedArg = detectNamedArgAt(prefix, toks, stack[i].argStartTokIdx)
	}

	return stack
}

func applyCallStackPunct(stack []callFrame, prefix string, toks []syntax.Token, tokIdx int, ch string) []callFrame {
	switch ch {
	case "(":
		return append(stack, callFrame{
			funcName:       extractCalleeBeforeOpenParen(prefix, toks, tokIdx),
			openTokIdx:     tokIdx,
			argStartTokIdx: tokIdx + 1,
		})
	case ")":
		if len(stack) > 0 {
			return stack[:len(stack)-1]
		}
	case "[":
		if len(stack) > 0 {
			stack[len(stack)-1].bracketDepth++
		}
	case "]":
		if len(stack) > 0 && stack[len(stack)-1].bracketDepth > 0 {
			stack[len(stack)-1].bracketDepth--
		}
	case ",":
		if len(stack) > 0 && stack[len(stack)-1].bracketDepth == 0 {
			top := &stack[len(stack)-1]
			top.argIndex++
			top.argStartTokIdx = tokIdx + 1
			top.sawComma = true
		}
	default:
	}

	return stack
}

func extractCalleeBeforeOpenParen(src string, toks []syntax.Token, openIdx int) string {
	idx := prevNonTrivia(toks, openIdx-1)
	if idx < 0 || !isIdentLikeToken(toks[idx]) {
		return ""
	}

	var parts []string
	parts = append(parts, strings.Trim(toks[idx].Text(src), "`"))

	for {
		dotIdx := prevNonTrivia(toks, idx-1)
		if dotIdx < 0 || toks[dotIdx].Kind != syntax.TokenPunct || toks[dotIdx].Text(src) != "." {
			break
		}

		segIdx := prevNonTrivia(toks, dotIdx-1)
		if segIdx < 0 || !isIdentLikeToken(toks[segIdx]) {
			break
		}

		parts = append(parts, strings.Trim(toks[segIdx].Text(src), "`"))
		idx = segIdx
	}

	reverseStrings(parts)

	return strings.Join(parts, ".")
}

func isIdentLikeToken(tok syntax.Token) bool {
	return tok.Kind == syntax.TokenIdent || tok.Kind == syntax.TokenBacktickIdent
}

func prevNonTrivia(toks []syntax.Token, idx int) int {
	for idx >= 0 && toks[idx].IsTrivia() {
		idx--
	}

	return idx
}

func detectNamedArgAt(src string, toks []syntax.Token, startIdx int) string {
	i := startIdx
	for i < len(toks) && toks[i].IsTrivia() {
		i++
	}

	if i >= len(toks) || !isIdentLikeToken(toks[i]) {
		return ""
	}

	name := strings.Trim(toks[i].Text(src), "`")
	j := i + 1
	for j < len(toks) && toks[j].IsTrivia() {
		j++
	}

	if j < len(toks) && toks[j].Kind == syntax.TokenOperator && toks[j].Text(src) == "=>" {
		return name
	}

	return ""
}

func countTotalCallArgs(stmtSQL string, localOff, curArgIdx int) int {
	if localOff >= len(stmtSQL) {
		return curArgIdx + 1
	}

	suffix := stmtSQL[localOff:]
	toks := syntax.Scan(suffix)
	parenDepth := 0
	bracketDepth := 0
	extraCommas := 0

	for _, tok := range toks {
		if tok.Kind != syntax.TokenPunct {
			continue
		}

		switch tok.Text(suffix) {
		case "(":
			parenDepth++
		case ")":
			if parenDepth == 0 {
				return curArgIdx + 1 + extraCommas
			}
			parenDepth--
		case "[":
			bracketDepth++
		case "]":
			if bracketDepth > 0 {
				bracketDepth--
			}
		case ",":
			if parenDepth == 0 && bracketDepth == 0 {
				extraCommas++
			}
		default:
		}
	}

	return curArgIdx + 1 + extraCommas
}

func adjustSpecialArgIndex(frame callFrame, stmtSQL string, localOff int) int {
	upper := strings.ToUpper(frame.funcName)
	upper = strings.TrimPrefix(upper, "SAFE.")

	switch upper {
	case "EXTRACT":
		return scanExtractArgIndex(stmtSQL[:localOff], frame.openTokIdx)
	case "CAST", "SAFE_CAST":
		return scanCastArgIndex(stmtSQL[:localOff], frame.openTokIdx)
	case "SUBSTRING", "SUBSTR":
		if !frame.sawComma {
			return scanSubstringFromArgIndex(stmtSQL[:localOff], frame.openTokIdx)
		}
		return frame.argIndex
	default:
		return frame.argIndex
	}
}

func scanExtractArgIndex(prefix string, openTokIdx int) int {
	toks := syntax.Scan(prefix)
	kws := collectTopLevelKeywords(prefix, toks, openTokIdx+1)
	if hasKeywordSequence(kws, "AT", "TIME", "ZONE") {
		return 2
	}

	if hasKeyword(kws, "FROM") {
		return 1
	}

	return 0
}

func scanCastArgIndex(prefix string, openTokIdx int) int {
	toks := syntax.Scan(prefix)
	kws := collectTopLevelKeywords(prefix, toks, openTokIdx+1)
	if hasKeyword(kws, "FORMAT") {
		return 2
	}

	if hasKeyword(kws, "AS") {
		return 1
	}

	return 0
}

func scanSubstringFromArgIndex(prefix string, openTokIdx int) int {
	toks := syntax.Scan(prefix)
	kws := collectTopLevelKeywords(prefix, toks, openTokIdx+1)
	if hasKeyword(kws, "FOR") {
		return 2
	}

	if hasKeyword(kws, "FROM") {
		return 1
	}

	return 0
}

func collectTopLevelKeywords(src string, toks []syntax.Token, startIdx int) []string {
	var kws []string
	depth := 0
	for i := startIdx; i < len(toks); i++ {
		tok := toks[i]
		if tok.Kind == syntax.TokenPunct {
			switch tok.Text(src) {
			case "(", "[":
				depth++
			case ")", "]":
				if depth > 0 {
					depth--
				}
			default:
			}
			continue
		}

		if depth == 0 && tok.Kind == syntax.TokenIdent {
			kws = append(kws, strings.ToUpper(tok.Text(src)))
		}
	}

	return kws
}

func hasKeyword(kws []string, target string) bool {
	return slices.Contains(kws, target)
}

func hasKeywordSequence(kws []string, seq ...string) bool {
	if len(seq) == 0 || len(kws) < len(seq) {
		return false
	}

	for i := 0; i <= len(kws)-len(seq); i++ {
		matched := true
		for j, s := range seq {
			if kws[i+j] != s {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}

	return false
}

func buildFuncDocSignatureHelp(
	doc *funcdocs.FuncDoc,
	isSafe bool,
	argIdx int,
	totalArgs int,
	namedArg string,
	labelOffsetSupport bool,
) *SignatureHelp {
	if doc == nil || len(doc.Signatures) == 0 {
		return nil
	}

	sigs := make([]SignatureInformation, 0, len(doc.Signatures))
	for i := range doc.Signatures {
		sd := &doc.Signatures[i]
		sigLabel := sd.Label
		if isSafe && !strings.HasPrefix(strings.ToUpper(sigLabel), "SAFE.") {
			sigLabel = "SAFE." + sigLabel
		}

		params := buildParameterInfos(sigLabel, sd.Parameters, labelOffsetSupport)
		sigActiveParam := resolveActiveParamForSignature(sd, argIdx, namedArg)

		sigDoc := funcdocs.FormatSignatureDoc(doc, sd, isSafe)
		var docContent *MarkupContent
		if sigDoc != "" {
			docContent = &MarkupContent{Kind: "markdown", Value: sigDoc}
		}

		info := SignatureInformation{
			Label:         sigLabel,
			Documentation: docContent,
			Parameters:    params,
		}
		if len(params) > 0 {
			ap := sigActiveParam
			info.ActiveParameter = &ap
		}

		sigs = append(sigs, info)
	}

	activeSig := selectActiveSignature(doc.Signatures, argIdx, totalArgs, namedArg)
	activeParam := resolveActiveParamForSignature(&doc.Signatures[activeSig], argIdx, namedArg)

	return &SignatureHelp{
		Signatures:      sigs,
		ActiveSignature: &activeSig,
		ActiveParameter: &activeParam,
	}
}

func buildParameterInfos(
	sigLabel string,
	params []funcdocs.ParameterDoc,
	labelOffsetSupport bool,
) []ParameterInformation {
	if len(params) == 0 {
		return nil
	}

	out := make([]ParameterInformation, 0, len(params))
	searchFrom := 0
	if openIdx := strings.IndexByte(sigLabel, '('); openIdx >= 0 {
		searchFrom = openIdx + 1
	}

	for _, p := range params {
		var label any = p.Label
		if labelOffsetSupport {
			if startByte, endByte, ok := findParamByteSpan(sigLabel, p.Label, searchFrom); ok {
				label = [2]uint32{
					utf16Len(sigLabel[:startByte]),
					utf16Len(sigLabel[:endByte]),
				}
				searchFrom = endByte
			}
		}

		var doc *MarkupContent
		if p.Documentation != "" {
			doc = &MarkupContent{Kind: "markdown", Value: p.Documentation}
		}

		out = append(out, ParameterInformation{
			Label:         label,
			Documentation: doc,
		})
	}

	return out
}

func findParamByteSpan(sigLabel, paramLabel string, searchFrom int) (start, end int, ok bool) {
	if paramLabel == "" || searchFrom < 0 || searchFrom > len(sigLabel) {
		return 0, 0, false
	}

	if rel := strings.Index(sigLabel[searchFrom:], paramLabel); rel >= 0 {
		s := searchFrom + rel
		return s, s + len(paramLabel), true
	}

	if idx := strings.Index(sigLabel, paramLabel); idx >= 0 {
		return idx, idx + len(paramLabel), true
	}

	return 0, 0, false
}

func utf16Len(s string) uint32 {
	var n uint32
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}

	return n
}

func selectActiveSignature(sigs []funcdocs.SignatureDoc, argIdx, totalArgs int, namedArg string) int {
	if len(sigs) <= 1 {
		return 0
	}

	if idx := findSignatureWithNamedParam(sigs, namedArg); idx >= 0 {
		return idx
	}

	needed := max(argIdx+1, totalArgs)
	if idx := findClosestAritySignature(sigs, needed); idx >= 0 {
		return idx
	}

	return findLargestAritySignature(sigs)
}

func findSignatureWithNamedParam(sigs []funcdocs.SignatureDoc, namedArg string) int {
	if namedArg == "" {
		return -1
	}

	for i := range sigs {
		if matchNamedParameter(&sigs[i], namedArg) >= 0 {
			return i
		}
	}

	return -1
}

func findClosestAritySignature(sigs []funcdocs.SignatureDoc, needed int) int {
	bestIdx := -1
	bestExcess := int(^uint(0) >> 1)

	for i := range sigs {
		nParams := len(sigs[i].Parameters)
		if sigs[i].Variadic {
			if nParams > 0 && bestIdx < 0 {
				bestIdx = i
			}
			continue
		}

		if nParams >= needed && nParams-needed < bestExcess {
			bestExcess = nParams - needed
			bestIdx = i
		}
	}

	return bestIdx
}

func findLargestAritySignature(sigs []funcdocs.SignatureDoc) int {
	maxIdx := 0
	for i := 1; i < len(sigs); i++ {
		if len(sigs[i].Parameters) > len(sigs[maxIdx].Parameters) {
			maxIdx = i
		}
	}

	return maxIdx
}

func resolveActiveParamForSignature(sig *funcdocs.SignatureDoc, argIdx int, namedArg string) int {
	if sig == nil || len(sig.Parameters) == 0 {
		return 0
	}

	if namedArg != "" {
		if idx := matchNamedParameter(sig, namedArg); idx >= 0 {
			return idx
		}
	}

	if argIdx < 0 {
		return 0
	}

	if argIdx >= len(sig.Parameters) {
		if sig.Variadic {
			return len(sig.Parameters) - 1
		}

		return len(sig.Parameters) - 1
	}

	return argIdx
}

func matchNamedParameter(sig *funcdocs.SignatureDoc, namedArg string) int {
	target := strings.ToLower(strings.TrimSpace(namedArg))
	if target == "" {
		return -1
	}

	for i, p := range sig.Parameters {
		lbl := strings.ToLower(p.Label)
		if lbl == target {
			return i
		}

		if lhs, _, hasArrow := strings.Cut(lbl, "=>"); hasArrow {
			lhs = strings.TrimSpace(strings.Trim(lhs, "[]"))
			if lhs == target {
				return i
			}
		}
	}

	return -1
}

// extractLocalUDFs scans CREATE [OR REPLACE] [TEMP|TEMPORARY] FUNCTION declarations
// in the document so users receive signature help for script-local UDFs.
func extractLocalUDFs(snap *DocumentSnapshot) map[string]*funcdocs.FuncDoc {
	var udfs map[string]*funcdocs.FuncDoc
	for _, st := range snap.Statements {
		if st == nil || st.SQL == "" {
			continue
		}

		doc := parseCreateFunctionHeader(st.SQL)
		if doc == nil {
			continue
		}

		if udfs == nil {
			udfs = make(map[string]*funcdocs.FuncDoc)
		}

		udfs[strings.ToUpper(doc.Name)] = doc
	}

	return udfs
}

func parseCreateFunctionHeader(sql string) *funcdocs.FuncDoc {
	toks := syntax.Scan(sql)
	nonTrivia := collectNonTriviaTokens(toks)
	if len(nonTrivia) < 4 {
		return nil
	}

	idx := 0
	if !strings.EqualFold(nonTrivia[idx].Text(sql), "CREATE") {
		return nil
	}
	idx++

	idx = skipOptionalCreateFunctionModifiers(sql, nonTrivia, idx)
	if idx >= len(nonTrivia) || !strings.EqualFold(nonTrivia[idx].Text(sql), "FUNCTION") {
		return nil
	}
	idx++

	idx = skipOptionalIfNotExists(sql, nonTrivia, idx)
	name, nextIdx := readDottedIdentFromTokens(sql, nonTrivia, idx)
	if len(name) == 0 || nextIdx >= len(nonTrivia) {
		return nil
	}

	if nonTrivia[nextIdx].Kind != syntax.TokenPunct || nonTrivia[nextIdx].Text(sql) != "(" {
		return nil
	}

	closeIdx := findMatchingCloseParen(sql, nonTrivia, nextIdx)
	if closeIdx < 0 {
		return nil
	}

	fnName := strings.Join(name, ".")
	paramStr := strings.TrimSpace(sql[nonTrivia[nextIdx].End:nonTrivia[closeIdx].Start])
	retType := readOptionalReturnsType(sql, nonTrivia, closeIdx+1)
	params := parseUDFParams(paramStr)

	label := fmt.Sprintf("%s(%s)", fnName, collapseSpaces(paramStr))
	if retType != "" {
		label += " RETURNS " + retType
	}

	return &funcdocs.FuncDoc{
		Name:        fnName,
		Category:    "Local function",
		Summary:     "User-defined function declared in this document.",
		Description: "User-defined function declared in this document.",
		ReturnType:  retType,
		Signatures: []funcdocs.SignatureDoc{{
			Label:      label,
			Parameters: params,
		}},
	}
}

func skipOptionalCreateFunctionModifiers(sql string, toks []syntax.Token, idx int) int {
	if idx+1 < len(toks) &&
		strings.EqualFold(toks[idx].Text(sql), "OR") &&
		strings.EqualFold(toks[idx+1].Text(sql), "REPLACE") {
		idx += 2
	}

	if idx < len(toks) {
		w := strings.ToUpper(toks[idx].Text(sql))
		if w == "TEMP" || w == "TEMPORARY" || w == "PUBLIC" || w == "PRIVATE" {
			idx++
		}
	}

	if idx < len(toks) && strings.EqualFold(toks[idx].Text(sql), "TABLE") {
		idx++
	}

	return idx
}

func skipOptionalIfNotExists(sql string, toks []syntax.Token, idx int) int {
	if idx+2 < len(toks) &&
		strings.EqualFold(toks[idx].Text(sql), "IF") &&
		strings.EqualFold(toks[idx+1].Text(sql), "NOT") &&
		strings.EqualFold(toks[idx+2].Text(sql), "EXISTS") {
		return idx + 3
	}

	return idx
}

func findMatchingCloseParen(sql string, toks []syntax.Token, openIdx int) int {
	depth := 0
	for i := openIdx; i < len(toks); i++ {
		if toks[i].Kind != syntax.TokenPunct {
			continue
		}

		switch toks[i].Text(sql) {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return i
			}
		default:
		}
	}

	return -1
}

func readOptionalReturnsType(sql string, toks []syntax.Token, startIdx int) string {
	if startIdx >= len(toks) || !strings.EqualFold(toks[startIdx].Text(sql), "RETURNS") {
		return ""
	}

	endIdx := startIdx + 1
	angleDepth := 0
	parenDepth := 0
	for endIdx < len(toks) {
		txt := toks[endIdx].Text(sql)
		if angleDepth == 0 && parenDepth == 0 {
			up := strings.ToUpper(txt)
			if up == "AS" || up == "LANGUAGE" || up == "OPTIONS" || txt == ";" {
				break
			}
		}

		switch txt {
		case "<":
			angleDepth++
		case ">":
			if angleDepth > 0 {
				angleDepth--
			}
		case "(":
			parenDepth++
		case ")":
			if parenDepth > 0 {
				parenDepth--
			}
		default:
		}

		endIdx++
	}

	if endIdx <= startIdx+1 {
		return ""
	}

	return collapseSpaces(sql[toks[startIdx+1].Start:toks[endIdx-1].End])
}

func parseUDFParams(paramStr string) []funcdocs.ParameterDoc {
	if strings.TrimSpace(paramStr) == "" {
		return nil
	}

	parts := splitCSVBalanced(paramStr)
	out := make([]funcdocs.ParameterDoc, 0, len(parts))
	for _, p := range parts {
		clean := collapseSpaces(p)
		if clean != "" {
			out = append(out, funcdocs.ParameterDoc{Label: clean})
		}
	}

	return out
}

func splitCSVBalanced(s string) []string {
	var parts []string
	start := 0
	paren := 0
	angle := 0
	for i := range len(s) {
		switch s[i] {
		case '(':
			paren++
		case ')':
			if paren > 0 {
				paren--
			}
		case '<':
			angle++
		case '>':
			if angle > 0 {
				angle--
			}
		case ',':
			if paren == 0 && angle == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		default:
		}
	}

	parts = append(parts, s[start:])

	return parts
}

func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func (e *Engine) signatureHelpFromResolvedWASM(
	entry *docEntry,
	snap *DocumentSnapshot,
	origOff int,
	funcName string,
	argIdx int,
	labelOffsetSupport bool,
) *SignatureHelp {
	st, ok := snap.StatementAtByte(origOff)
	if !ok || st == nil {
		return nil
	}

	rsList := resolvedStatementsForStmt(st)
	if len(rsList) == 0 {
		return nil
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()

	wasmMu.Lock()
	defer wasmMu.Unlock()

	matchedFn := findResolvedFunctionByName(rsList, funcName)
	if isNilNode(matchedFn) {
		return nil
	}

	sigText, err := matchedFn.GetSupportedSignaturesUserFacingText(
		catalog.DefaultLSPLanguageOptions(),
		googlesql.NamePrintingStyleKIfNamedOnly,
		false,
	)
	if err != nil || strings.TrimSpace(sigText) == "" {
		return nil
	}

	doc := buildFallbackGoogleSQLFuncDoc(funcName, sigText)

	return buildFuncDocSignatureHelp(doc, false, argIdx, argIdx+1, "", labelOffsetSupport)
}

func resolvedStatementsForStmt(st *AnalyzedStatement) []googlesql.ResolvedStatementNode {
	if len(st.ResolvedStmts) > 0 {
		return st.ResolvedStmts
	}

	if !isNilNode(st.ResolvedStmt) {
		return []googlesql.ResolvedStatementNode{st.ResolvedStmt}
	}

	return nil
}

func findResolvedFunctionByName(rsList []googlesql.ResolvedStatementNode, funcName string) *googlesql.Function {
	var matched *googlesql.Function
	for _, rs := range rsList {
		walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
			if !isNilNode(matched) {
				return false
			}

			fn := resolvedNodeFunction(n)
			if isNilNode(fn) {
				return true
			}

			sqlName, _ := fn.SQLName()
			if strings.EqualFold(sqlName, funcName) {
				matched = fn
				return false
			}

			return true
		})
	}

	return matched
}

func resolvedNodeFunction(n googlesql.ResolvedNode) *googlesql.Function {
	fnNode, isFn := n.(interface {
		Function() (*googlesql.Function, error)
	})
	if !isFn {
		return nil
	}

	fn, err := fnNode.Function()
	if err != nil || isNilNode(fn) {
		return nil
	}

	return fn
}

func buildFallbackGoogleSQLFuncDoc(funcName, sigText string) *funcdocs.FuncDoc {
	doc := &funcdocs.FuncDoc{
		Name:        strings.ToUpper(funcName),
		Summary:     "GoogleSQL built-in function.",
		Description: "GoogleSQL built-in function.",
	}
	for ln := range strings.SplitSeq(strings.TrimSpace(sigText), "\n") {
		clean := strings.TrimSpace(ln)
		if clean == "" {
			continue
		}

		doc.Signatures = append(doc.Signatures, funcdocs.SignatureDoc{
			Label:      clean,
			Parameters: parseUDFParams(extractInsideParens(clean)),
		})
	}

	return doc
}

func extractInsideParens(sig string) string {
	o := strings.IndexByte(sig, '(')
	c := strings.LastIndexByte(sig, ')')
	if o < 0 || c <= o {
		return ""
	}

	return sig[o+1 : c]
}

// signatureHelpInJinja provides signature help for built-in dbt functions and
// local {% macro name(args) %} definitions inside Jinja2 expressions/tags.
func signatureHelpInJinja(src string, tokens []syntax.Token, origOff int, labelOffsetSupport bool) *SignatureHelp {
	prefix := src[:origOff]
	prefixToks := syntax.Scan(prefix)
	var stack []callFrame

	for i, tok := range prefixToks {
		if tok.IsTrivia() || (tok.Kind != syntax.TokenJinjaPunct && tok.Kind != syntax.TokenPunct) {
			continue
		}

		switch tok.Text(prefix) {
		case "(":
			fn := extractJinjaCalleeBeforeOpenParen(prefix, prefixToks, i)
			stack = append(stack, callFrame{funcName: fn, openTokIdx: i, argStartTokIdx: i + 1})
		case ")":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case ",":
			if len(stack) > 0 {
				stack[len(stack)-1].argIndex++
			}
		default:
		}
	}

	for _, frame := range slices.Backward(stack) {
		fn := frame.funcName
		if fn == "" {
			continue
		}

		if doc, ok := dbtSignatureDocs[fn]; ok {
			return buildFuncDocSignatureHelp(doc, false, frame.argIndex, frame.argIndex+1, "", labelOffsetSupport)
		}

		if doc := findLocalJinjaMacroDoc(src, tokens, fn); doc != nil {
			return buildFuncDocSignatureHelp(doc, false, frame.argIndex, frame.argIndex+1, "", labelOffsetSupport)
		}
	}

	return nil
}

func extractJinjaCalleeBeforeOpenParen(src string, toks []syntax.Token, openIdx int) string {
	idx := prevNonTrivia(toks, openIdx-1)
	if idx < 0 || toks[idx].Kind != syntax.TokenJinjaIdent {
		return ""
	}

	return toks[idx].Text(src)
}

func findLocalJinjaMacroDoc(src string, tokens []syntax.Token, name string) *funcdocs.FuncDoc {
	for i := range tokens {
		if tokens[i].Kind != syntax.TokenJinjaTagKeyword || tokens[i].Text(src) != "macro" {
			continue
		}

		j := i + 1
		for j < len(tokens) && tokens[j].IsTrivia() {
			j++
		}

		if j >= len(tokens) || tokens[j].Kind != syntax.TokenJinjaIdent || tokens[j].Text(src) != name {
			continue
		}

		k := j + 1
		for k < len(tokens) && tokens[k].IsTrivia() {
			k++
		}

		if k >= len(tokens) || tokens[k].Text(src) != "(" {
			continue
		}

		closeIdx := findMatchingJinjaCloseParen(src, tokens, k)
		if closeIdx < 0 {
			continue
		}

		paramStr := strings.TrimSpace(src[tokens[k].End:tokens[closeIdx].Start])

		return &funcdocs.FuncDoc{
			Name:        name,
			Category:    "Jinja2 macro",
			Summary:     "Local Jinja2 macro defined in this template.",
			Description: "Local Jinja2 macro defined in this template.",
			Signatures: []funcdocs.SignatureDoc{{
				Label:      fmt.Sprintf("%s(%s)", name, collapseSpaces(paramStr)),
				Parameters: parseUDFParams(paramStr),
			}},
		}
	}

	return nil
}

func findMatchingJinjaCloseParen(src string, tokens []syntax.Token, openIdx int) int {
	depth := 0
	for i := openIdx; i < len(tokens); i++ {
		if tokens[i].Kind != syntax.TokenJinjaPunct && tokens[i].Kind != syntax.TokenPunct {
			continue
		}

		switch tokens[i].Text(src) {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return i
			}
		default:
		}
	}

	return -1
}

var dbtSignatureDocs = map[string]*funcdocs.FuncDoc{
	"ref": {
		Name:        "ref",
		Category:    "dbt macro",
		Summary:     "References another dbt model and establishes a DAG dependency.",
		Description: "Returns the `Relation` for the referenced dbt model and records a lineage dependency.",
		Signatures: []funcdocs.SignatureDoc{
			{
				Label: "ref(model_name)",
				Parameters: []funcdocs.ParameterDoc{
					{Label: "model_name", Documentation: "Name of the dbt model to reference (string)."},
				},
			},
			{
				Label: "ref(package_name, model_name)",
				Parameters: []funcdocs.ParameterDoc{
					{Label: "package_name", Documentation: "Name of the dbt package containing the model."},
					{Label: "model_name", Documentation: "Name of the dbt model to reference."},
				},
			},
		},
	},
	"source": {
		Name:        "source",
		Category:    "dbt macro",
		Summary:     "References a declared dbt source table.",
		Description: "Returns the `Relation` for a source table declared in a dbt YAML sources file.",
		Signatures: []funcdocs.SignatureDoc{{
			Label: "source(source_name, table_name)",
			Parameters: []funcdocs.ParameterDoc{
				{Label: "source_name", Documentation: "Logical dbt source name defined in `sources:` YAML."},
				{Label: "table_name", Documentation: "Table name within the dbt source."},
			},
		}},
	},
	"var": {
		Name:        "var",
		Category:    "dbt macro",
		Summary:     "Retrieves a dbt project or CLI variable.",
		Description: "Looks up a variable defined in `dbt_project.yml` or passed via `--vars`, with an optional default value.",
		Signatures: []funcdocs.SignatureDoc{{
			Label: "var(name, default=None)",
			Parameters: []funcdocs.ParameterDoc{
				{Label: "name", Documentation: "Variable name to look up."},
				{Label: "default=None", Documentation: "Optional fallback value if the variable is undefined.", Optional: true},
			},
		}},
	},
	"env_var": {
		Name:        "env_var",
		Category:    "dbt macro",
		Summary:     "Reads an environment variable during dbt compilation.",
		Description: "Returns the string value of environment variable `name`, or `default` if unset.",
		Signatures: []funcdocs.SignatureDoc{{
			Label: "env_var(name, default=None)",
			Parameters: []funcdocs.ParameterDoc{
				{Label: "name", Documentation: "Environment variable name."},
				{Label: "default=None", Documentation: "Optional fallback value if unset.", Optional: true},
			},
		}},
	},
	"config": {
		Name:        "config",
		Category:    "dbt macro",
		Summary:     "Configures model-level dbt materialization and BigQuery options.",
		Description: "Sets dbt model configuration such as `materialized`, `partition_by`, `cluster_by`, `labels`, or `require_partition_filter`.",
		Signatures: []funcdocs.SignatureDoc{{
			Label:    "config(materialized=..., partition_by=..., cluster_by=...)",
			Variadic: true,
			Parameters: []funcdocs.ParameterDoc{
				{Label: "materialized=...", Documentation: "Materialization strategy (`table`, `view`, `incremental`, `ephemeral`).", Optional: true},
				{Label: "partition_by=...", Documentation: "BigQuery table partitioning specification dictionary.", Optional: true},
				{Label: "cluster_by=...", Documentation: "List of BigQuery clustering column names.", Optional: true},
			},
		}},
	},
}
