package syntax

import (
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ScannerMode identifies the active lexical state when a token was scanned.
type ScannerMode uint8

const (
	// ModeSQL indicates standard GoogleSQL / bqlang lexing mode.
	ModeSQL ScannerMode = iota
	// ModeJinjaExpr indicates lexing inside {{ ... }}.
	ModeJinjaExpr
	// ModeJinjaTag indicates lexing inside {% ... %}.
	ModeJinjaTag
	// ModeJinjaRaw indicates verbatim lexing inside {% raw %} ... {% endraw %}.
	ModeJinjaRaw
	// ModeJinjaComment indicates lexing a {# ... #} comment.
	ModeJinjaComment
)

var scannerModeNames = [...]string{
	ModeSQL:          "sql",
	ModeJinjaExpr:    "jinja_expr",
	ModeJinjaTag:     "jinja_tag",
	ModeJinjaRaw:     "jinja_raw",
	ModeJinjaComment: "jinja_comment",
}

// String returns the human-readable name of m.
func (m ScannerMode) String() string {
	if int(m) < len(scannerModeNames) {
		return scannerModeNames[m]
	}

	return "unknown"
}

// TokenKind classifies a lexical token emitted by Scan.
type TokenKind uint8

const (
	// TokenInvalid represents an unrecognized byte or invalid UTF-8 sequence.
	TokenInvalid TokenKind = iota
	// TokenWhitespace represents horizontal whitespace (' ', '\t', '\v', '\f').
	TokenWhitespace
	// TokenNewline represents a line terminator ("\r\n", "\n", or "\r").
	TokenNewline
	// TokenComment represents a SQL line or block comment (--, #, //, /* ... */).
	TokenComment
	// TokenIdent represents an unquoted SQL identifier or keyword.
	TokenIdent
	// TokenBacktickIdent represents a backtick-quoted SQL identifier (`...`).
	TokenBacktickIdent
	// TokenString represents a SQL single-, double-, or triple-quoted string literal.
	TokenString
	// TokenBytes represents a SQL bytes literal (b'...', rb'...', br'...').
	TokenBytes
	// TokenNumber represents a SQL numeric or hex literal.
	TokenNumber
	// TokenParam represents a named query parameter (@name).
	TokenParam
	// TokenSysVar represents a BigQuery system variable sigil (@@name or bare @@).
	TokenSysVar
	// TokenOperator represents a SQL operator (|>, ->, =>, ||, +, -, etc.).
	TokenOperator
	// TokenPunct represents SQL punctuation ((, ), [, ], {, }, ,, ., ;, :, ?, $, @).
	TokenPunct

	// TokenJinjaComment represents a Jinja2 comment ({# ... #}).
	TokenJinjaComment
	// TokenJinjaExprOpen represents {{, {{-, or {{+.
	TokenJinjaExprOpen
	// TokenJinjaExprClose represents }} or -}}.
	TokenJinjaExprClose
	// TokenJinjaBlockOpen represents {%, {%-, or {%+.
	TokenJinjaBlockOpen
	// TokenJinjaBlockClose represents %}, -%}, or +%}.
	TokenJinjaBlockClose
	// TokenJinjaTagKeyword represents the leading tag keyword inside {% ... %}.
	TokenJinjaTagKeyword
	// TokenJinjaKeyword represents a Jinja2 expression keyword (in, not, and, or, etc.).
	TokenJinjaKeyword
	// TokenJinjaIdent represents an identifier inside {{ ... }} or {% ... %}.
	TokenJinjaIdent
	// TokenJinjaString represents a string literal inside {{ ... }} or {% ... %}.
	TokenJinjaString
	// TokenJinjaNumber represents a numeric literal inside {{ ... }} or {% ... %}.
	TokenJinjaNumber
	// TokenJinjaOperator represents an operator inside {{ ... }} or {% ... %}.
	TokenJinjaOperator
	// TokenJinjaPunct represents punctuation inside {{ ... }} or {% ... %}.
	TokenJinjaPunct
	// TokenJinjaRawBody represents verbatim content inside {% raw %} ... {% endraw %}.
	TokenJinjaRawBody
)

// String returns the human-readable name of k.
func (k TokenKind) String() string {
	switch k {
	case TokenInvalid:
		return "invalid"
	case TokenWhitespace:
		return "whitespace"
	case TokenNewline:
		return "newline"
	case TokenComment:
		return "comment"
	case TokenIdent:
		return "ident"
	case TokenBacktickIdent:
		return "backtick_ident"
	case TokenString:
		return "string"
	case TokenBytes:
		return "bytes"
	case TokenNumber:
		return "number"
	case TokenParam:
		return "param"
	case TokenSysVar:
		return "sysvar"
	case TokenOperator:
		return "operator"
	case TokenPunct:
		return "punct"
	case TokenJinjaComment:
		return "jinja_comment"
	case TokenJinjaExprOpen:
		return "jinja_expr_open"
	case TokenJinjaExprClose:
		return "jinja_expr_close"
	case TokenJinjaBlockOpen:
		return "jinja_block_open"
	case TokenJinjaBlockClose:
		return "jinja_block_close"
	case TokenJinjaTagKeyword:
		return "jinja_tag_keyword"
	case TokenJinjaKeyword:
		return "jinja_keyword"
	case TokenJinjaIdent:
		return "jinja_ident"
	case TokenJinjaString:
		return "jinja_string"
	case TokenJinjaNumber:
		return "jinja_number"
	case TokenJinjaOperator:
		return "jinja_operator"
	case TokenJinjaPunct:
		return "jinja_punct"
	case TokenJinjaRawBody:
		return "jinja_raw_body"
	default:
		return "unknown"
	}
}

// Token represents a non-empty half-open byte slice [Start, End) of source input.
type Token struct {
	Kind     TokenKind
	Mode     ScannerMode
	Start    int
	End      int
	Unclosed bool
}

// Len returns the byte length of the token.
func (t Token) Len() int {
	return t.End - t.Start
}

// Text safely slices the token's text from src.
func (t Token) Text(src string) string {
	start := max(0, min(t.Start, len(src)))
	end := max(start, min(t.End, len(src)))

	return src[start:end]
}

// IsTrivia reports whether t is whitespace, a newline, or any comment.
func (t Token) IsTrivia() bool {
	return t.Kind == TokenWhitespace ||
		t.Kind == TokenNewline ||
		t.Kind == TokenComment ||
		t.Kind == TokenJinjaComment
}

// IsComment reports whether t is a SQL or Jinja2 comment.
func (t Token) IsComment() bool {
	return t.Kind == TokenComment || t.Kind == TokenJinjaComment
}

// IsJinja reports whether t was scanned outside ModeSQL.
func (t Token) IsJinja() bool {
	return t.Mode != ModeSQL
}

var (
	sqlTwoCharOps = map[string]struct{}{
		"|>": {}, "->": {}, "=>": {}, "||": {},
		"<<": {}, ">>": {}, "<>": {}, "!=": {},
		"<=": {}, ">=": {}, "+=": {}, "-=": {},
	}
	sqlOneCharOps = map[byte]struct{}{
		'+': {}, '-': {}, '*': {}, '/': {}, '%': {},
		'=': {}, '<': {}, '>': {}, '&': {}, '|': {},
		'^': {}, '~': {}, '!': {},
	}
	sqlPunctChars = map[byte]struct{}{
		'(': {}, ')': {}, '[': {}, ']': {}, '{': {}, '}': {},
		',': {}, '.': {}, ';': {}, ':': {}, '?': {}, '$': {}, '@': {},
	}
	jinjaTwoCharOps = map[string]struct{}{
		"==": {}, "!=": {}, "<=": {}, ">=": {}, "**": {}, "//": {},
	}
	jinjaOneCharOps = map[byte]struct{}{
		'|': {}, '~': {}, '+': {}, '-': {}, '*': {},
		'/': {}, '%': {}, '<': {}, '>': {}, '=': {},
	}
	jinjaPunctChars = map[byte]struct{}{
		'.': {}, ',': {}, '(': {}, ')': {}, '[': {},
		']': {}, '{': {}, '}': {}, ':': {}, ';': {},
	}
	jinjaTagKeywords = map[string]struct{}{
		"if": {}, "elif": {}, "else": {}, "endif": {},
		"for": {}, "endfor": {}, "set": {}, "endset": {},
		"macro": {}, "endmacro": {}, "call": {}, "endcall": {},
		"do": {}, "raw": {}, "endraw": {}, "block": {}, "endblock": {},
		"extends": {}, "include": {}, "import": {}, "from": {},
		"filter": {}, "endfilter": {}, "with": {}, "endwith": {},
		"break": {}, "continue": {}, "print": {}, "autoescape": {},
		"endautoescape": {}, "trans": {}, "endtrans": {}, "debug": {},
	}
	jinjaExprKeywords = map[string]struct{}{
		"in": {}, "not": {}, "and": {}, "or": {}, "is": {},
		"true": {}, "false": {}, "none": {},
		"True": {}, "False": {}, "None": {},
		"if": {}, "else": {}, "as": {}, "with": {}, "without": {}, "recursive": {},
	}
)

// Scan tokenizes src in a single resilient pass, returning a gapless partition
// of [0, len(src)] without ever aborting on malformed or unclosed constructs.
func Scan(src string) []Token {
	if src == "" {
		return nil
	}

	sc := scanner{
		src:    src,
		mode:   ModeSQL,
		tokens: make([]Token, 0, len(src)/4+1),
	}
	sc.run()

	return sc.tokens
}

// TokenAt returns the token containing byte offset (Start <= offset < End) and its index.
func TokenAt(tokens []Token, offset int) (Token, int, bool) {
	if len(tokens) == 0 || offset < 0 {
		return Token{}, -1, false
	}

	idx := sort.Search(len(tokens), func(i int) bool {
		return tokens[i].End > offset
	})
	if idx < len(tokens) && tokens[idx].Start <= offset && offset < tokens[idx].End {
		return tokens[idx], idx, true
	}

	return Token{}, -1, false
}

// TokenBefore returns the token containing offset, or the token ending at offset
// when offset equals the end of the token stream.
func TokenBefore(tokens []Token, offset int) (Token, int, bool) {
	if len(tokens) == 0 || offset < 0 {
		return Token{}, -1, false
	}
	if tok, idx, ok := TokenAt(tokens, offset); ok {
		return tok, idx, true
	}
	if offset > 0 {
		if tok, idx, ok := TokenAt(tokens, offset-1); ok {
			return tok, idx, true
		}
	}

	last := tokens[len(tokens)-1]
	if offset >= last.End {
		return last, len(tokens) - 1, true
	}

	return Token{}, -1, false
}

type scanner struct {
	src                string
	pos                int
	mode               ScannerMode
	tokens             []Token
	jinjaBrackets      []byte
	jinjaBracketCounts [3]int
	sawTagKeyword      bool
	pendingRaw         bool
}

func (s *scanner) emit(kind TokenKind, mode ScannerMode, start, end int, unclosed bool) {
	s.tokens = append(s.tokens, Token{
		Kind:     kind,
		Mode:     mode,
		Start:    start,
		End:      end,
		Unclosed: unclosed,
	})
	s.pos = end
}

func (s *scanner) run() {
	for s.pos < len(s.src) {
		switch s.mode {
		case ModeSQL:
			s.scanSQL()
		case ModeJinjaExpr:
			s.scanJinjaInner(true)
		case ModeJinjaTag:
			s.scanJinjaInner(false)
		case ModeJinjaRaw:
			s.scanJinjaRaw()
		case ModeJinjaComment:
			s.mode = ModeSQL
		}
	}
}

func (s *scanner) scanSQL() {
	if s.tryScanTrivia(ModeSQL) {
		return
	}
	if s.tryScanSQLComment() {
		return
	}
	if s.tryScanSQLJinja() {
		return
	}
	if s.tryScanSQLLiteral() {
		return
	}
	s.scanSQLSymbol()
}

func (s *scanner) tryScanTrivia(mode ScannerMode) bool {
	b := s.src[s.pos]
	if isHorizSpace(b) {
		end := s.pos + 1
		for end < len(s.src) && isHorizSpace(s.src[end]) {
			end++
		}
		s.emit(TokenWhitespace, mode, s.pos, end, false)

		return true
	}
	if b == '\r' || b == '\n' {
		end := s.pos + 1
		if b == '\r' && end < len(s.src) && s.src[end] == '\n' {
			end++
		}
		s.emit(TokenNewline, mode, s.pos, end, false)

		return true
	}

	return false
}

func (s *scanner) tryScanSQLComment() bool {
	b := s.src[s.pos]
	if b == '#' || hasPrefixAt(s.src, s.pos, "--") || hasPrefixAt(s.src, s.pos, "//") {
		end := s.pos + 1
		if b != '#' {
			end = s.pos + 2
		}
		for end < len(s.src) && s.src[end] != '\r' && s.src[end] != '\n' {
			end++
		}
		s.emit(TokenComment, ModeSQL, s.pos, end, false)

		return true
	}
	if hasPrefixAt(s.src, s.pos, "/*") {
		closeIdx := strings.Index(s.src[s.pos+2:], "*/")
		if closeIdx < 0 {
			s.emit(TokenComment, ModeSQL, s.pos, len(s.src), true)
		} else {
			s.emit(TokenComment, ModeSQL, s.pos, s.pos+2+closeIdx+2, false)
		}

		return true
	}

	return false
}

func (s *scanner) tryScanSQLJinja() bool {
	if hasPrefixAt(s.src, s.pos, "{#") {
		s.scanJinjaComment()

		return true
	}
	if hasPrefixAt(s.src, s.pos, "{{") {
		end := s.pos + 2
		if end < len(s.src) && (s.src[end] == '-' || s.src[end] == '+') {
			end++
		}
		s.jinjaBrackets = s.jinjaBrackets[:0]
		s.jinjaBracketCounts = [3]int{}
		s.mode = ModeJinjaExpr
		s.emit(TokenJinjaExprOpen, ModeJinjaExpr, s.pos, end, false)

		return true
	}
	if hasPrefixAt(s.src, s.pos, "{%") {
		end := s.pos + 2
		if end < len(s.src) && (s.src[end] == '-' || s.src[end] == '+') {
			end++
		}
		s.jinjaBrackets = s.jinjaBrackets[:0]
		s.jinjaBracketCounts = [3]int{}
		s.sawTagKeyword = false
		s.pendingRaw = false
		s.mode = ModeJinjaTag
		s.emit(TokenJinjaBlockOpen, ModeJinjaTag, s.pos, end, false)

		return true
	}

	return false
}

func (s *scanner) scanJinjaComment() {
	start := s.pos
	scanFrom := start + 2
	if scanFrom < len(s.src) && (s.src[scanFrom] == '-' || s.src[scanFrom] == '+') {
		scanFrom++
	}
	closeIdx := strings.Index(s.src[scanFrom:], "#}")
	if closeIdx < 0 {
		s.emit(TokenJinjaComment, ModeJinjaComment, start, len(s.src), true)

		return
	}
	s.emit(TokenJinjaComment, ModeJinjaComment, start, scanFrom+closeIdx+2, false)
}

func (s *scanner) tryScanSQLLiteral() bool {
	if prefixLen, isBytes, ok := matchSQLStringPrefix(s.src, s.pos); ok {
		s.scanSQLQuoted(prefixLen, isBytes)

		return true
	}
	b := s.src[s.pos]
	if b == '`' {
		s.scanSQLBacktick()

		return true
	}
	if b == '@' {
		s.scanSQLSigil()

		return true
	}
	if isDigit(b) {
		if s.followsPathDot() {
			end := s.pos + 1
			for end < len(s.src) && isASCIIIdentCont(s.src[end]) {
				end++
			}
			s.emit(TokenIdent, ModeSQL, s.pos, end, false)

			return true
		}
		s.scanSQLNumber()

		return true
	}
	if b == '.' && s.pos+1 < len(s.src) && isDigit(s.src[s.pos+1]) && !s.dotFollowsPathAnchor() {
		s.scanSQLNumber()

		return true
	}
	if isASCIIIdentStart(b) {
		end := s.pos + 1
		for end < len(s.src) && isASCIIIdentCont(s.src[end]) {
			end++
		}
		s.emit(TokenIdent, ModeSQL, s.pos, end, false)

		return true
	}

	return false
}

func (s *scanner) followsPathDot() bool {
	if len(s.tokens) < 2 {
		return false
	}
	dot := s.tokens[len(s.tokens)-1]
	if dot.Kind != TokenPunct || dot.End != s.pos || s.src[dot.Start] != '.' {
		return false
	}
	anchor := s.tokens[len(s.tokens)-2]

	return isPathAnchorToken(s.src, anchor, dot.Start)
}

func (s *scanner) dotFollowsPathAnchor() bool {
	if len(s.tokens) == 0 {
		return false
	}

	return isPathAnchorToken(s.src, s.tokens[len(s.tokens)-1], s.pos)
}

func isPathAnchorToken(src string, tok Token, expectedEnd int) bool {
	if tok.End != expectedEnd {
		return false
	}
	switch tok.Kind {
	case TokenIdent, TokenBacktickIdent, TokenSysVar, TokenParam, TokenJinjaExprClose:
		return true
	case TokenPunct:
		ch := src[tok.Start]
		return ch == ']' || ch == ')'
	default:
		return false
	}
}

func (s *scanner) scanSQLQuoted(prefixLen int, isBytes bool) {
	kind := TokenString
	if isBytes {
		kind = TokenBytes
	}

	quotePos := s.pos + prefixLen
	quote := s.src[quotePos]
	if quotePos+2 < len(s.src) && s.src[quotePos+1] == quote && s.src[quotePos+2] == quote {
		end, unclosed := scanTripleQuoted(s.src, quotePos+3, quote)
		s.emit(kind, ModeSQL, s.pos, end, unclosed)

		return
	}

	end, unclosed := scanSingleLineQuoted(s.src, quotePos+1, quote)
	s.emit(kind, ModeSQL, s.pos, end, unclosed)
}

func (s *scanner) scanSQLBacktick() {
	end, unclosed := scanSingleLineQuoted(s.src, s.pos+1, '`')
	s.emit(TokenBacktickIdent, ModeSQL, s.pos, end, unclosed)
}

func (s *scanner) scanSQLSigil() {
	if s.pos+1 < len(s.src) && s.src[s.pos+1] == '@' {
		if s.pos+2 < len(s.src) && isASCIIIdentStart(s.src[s.pos+2]) {
			end := s.pos + 3
			for end < len(s.src) && isASCIIIdentCont(s.src[end]) {
				end++
			}
			s.emit(TokenSysVar, ModeSQL, s.pos, end, false)

			return
		}
		s.emit(TokenSysVar, ModeSQL, s.pos, s.pos+2, true)

		return
	}

	if s.pos+1 < len(s.src) && isASCIIIdentStart(s.src[s.pos+1]) {
		end := s.pos + 2
		for end < len(s.src) && isASCIIIdentCont(s.src[end]) {
			end++
		}
		s.emit(TokenParam, ModeSQL, s.pos, end, false)

		return
	}

	s.emit(TokenPunct, ModeSQL, s.pos, s.pos+1, false)
}

func (s *scanner) scanSQLNumber() {
	start := s.pos
	if s.src[start] == '0' && start+2 < len(s.src) && (s.src[start+1] == 'x' || s.src[start+1] == 'X') && isHexDigit(s.src[start+2]) {
		end := start + 3
		for end < len(s.src) && isHexDigit(s.src[end]) {
			end++
		}
		s.emit(TokenNumber, ModeSQL, start, end, false)

		return
	}

	end := start
	for end < len(s.src) && isDigit(s.src[end]) {
		end++
	}
	if end < len(s.src) && s.src[end] == '.' && (end+1 >= len(s.src) || s.src[end+1] != '.') {
		end++
		for end < len(s.src) && isDigit(s.src[end]) {
			end++
		}
	}
	end = consumeExponent(s.src, end, false)
	s.emit(TokenNumber, ModeSQL, start, end, false)
}

func (s *scanner) scanSQLSymbol() {
	if s.pos+2 <= len(s.src) {
		if _, ok := sqlTwoCharOps[s.src[s.pos:s.pos+2]]; ok {
			s.emit(TokenOperator, ModeSQL, s.pos, s.pos+2, false)

			return
		}
	}

	b := s.src[s.pos]
	if _, ok := sqlOneCharOps[b]; ok {
		s.emit(TokenOperator, ModeSQL, s.pos, s.pos+1, false)

		return
	}
	if _, ok := sqlPunctChars[b]; ok {
		s.emit(TokenPunct, ModeSQL, s.pos, s.pos+1, false)

		return
	}

	_, size := utf8.DecodeRuneInString(s.src[s.pos:])
	if size < 1 {
		size = 1
	}
	s.emit(TokenInvalid, ModeSQL, s.pos, s.pos+size, false)
}

func (s *scanner) scanJinjaInner(isExpr bool) {
	canClose := !isExpr || len(s.jinjaBrackets) == 0 || s.jinjaBrackets[len(s.jinjaBrackets)-1] != '}'
	if canClose && s.tryScanJinjaClose(isExpr) {
		return
	}
	if s.tryScanTrivia(s.mode) {
		return
	}
	b := s.src[s.pos]
	if b == '\'' || b == '"' {
		end, unclosed := scanMultilineJinjaString(s.src, s.pos+1, b)
		s.emit(TokenJinjaString, s.mode, s.pos, end, unclosed)

		return
	}
	if isDigit(b) || (b == '.' && s.pos+1 < len(s.src) && isDigit(s.src[s.pos+1])) {
		s.scanJinjaNumber()

		return
	}
	if s.tryScanJinjaIdent() {
		return
	}
	s.scanJinjaSymbol()
}

func (s *scanner) tryScanJinjaClose(isExpr bool) bool {
	if isExpr {
		var closeLen int
		switch {
		case hasPrefixAt(s.src, s.pos, "-}}"):
			closeLen = 3
		case hasPrefixAt(s.src, s.pos, "}}"):
			closeLen = 2
		default:
			return false
		}
		s.emit(TokenJinjaExprClose, ModeJinjaExpr, s.pos, s.pos+closeLen, false)
		s.mode = ModeSQL

		return true
	}

	var closeLen int
	switch {
	case hasPrefixAt(s.src, s.pos, "-%}"), hasPrefixAt(s.src, s.pos, "+%}"):
		closeLen = 3
	case hasPrefixAt(s.src, s.pos, "%}"):
		closeLen = 2
	default:
		return false
	}
	s.emit(TokenJinjaBlockClose, ModeJinjaTag, s.pos, s.pos+closeLen, false)
	if s.pendingRaw {
		s.pendingRaw = false
		s.mode = ModeJinjaRaw
	} else {
		s.mode = ModeSQL
	}

	return true
}

func (s *scanner) scanJinjaNumber() {
	start := s.pos
	if s.src[start] == '0' && start+2 < len(s.src) {
		if end, ok := scanJinjaPrefixedInt(s.src, start); ok {
			s.emit(TokenJinjaNumber, s.mode, start, end, false)

			return
		}
	}

	end := start
	for end < len(s.src) && (isDigit(s.src[end]) || s.src[end] == '_') {
		end++
	}
	if end < len(s.src) && s.src[end] == '.' && end+1 < len(s.src) && isDigit(s.src[end+1]) {
		end++
		for end < len(s.src) && (isDigit(s.src[end]) || s.src[end] == '_') {
			end++
		}
	}
	end = consumeExponent(s.src, end, true)
	s.emit(TokenJinjaNumber, s.mode, start, end, false)
}

func (s *scanner) tryScanJinjaIdent() bool {
	r, size := utf8.DecodeRuneInString(s.src[s.pos:])
	if r != '_' && !unicode.IsLetter(r) {
		return false
	}

	end := s.pos + size
	for end < len(s.src) {
		nr, nsize := utf8.DecodeRuneInString(s.src[end:])
		if nr != '_' && !unicode.IsLetter(nr) && !unicode.IsDigit(nr) {
			break
		}
		end += nsize
	}

	word := s.src[s.pos:end]
	kind := TokenJinjaIdent
	if s.mode == ModeJinjaTag && !s.sawTagKeyword {
		s.sawTagKeyword = true
		if _, ok := jinjaTagKeywords[word]; ok {
			kind = TokenJinjaTagKeyword
			if word == "raw" {
				s.pendingRaw = true
			}
		} else if _, ok := jinjaExprKeywords[word]; ok {
			kind = TokenJinjaKeyword
		}
	} else if _, ok := jinjaExprKeywords[word]; ok {
		kind = TokenJinjaKeyword
	}

	s.emit(kind, s.mode, s.pos, end, false)

	return true
}

func (s *scanner) scanJinjaSymbol() {
	if s.pos+2 <= len(s.src) {
		if _, ok := jinjaTwoCharOps[s.src[s.pos:s.pos+2]]; ok {
			s.emit(TokenJinjaOperator, s.mode, s.pos, s.pos+2, false)

			return
		}
	}

	b := s.src[s.pos]
	if _, ok := jinjaOneCharOps[b]; ok {
		s.emit(TokenJinjaOperator, s.mode, s.pos, s.pos+1, false)

		return
	}
	if _, ok := jinjaPunctChars[b]; ok {
		s.updateJinjaBrackets(b)
		s.emit(TokenJinjaPunct, s.mode, s.pos, s.pos+1, false)

		return
	}

	_, size := utf8.DecodeRuneInString(s.src[s.pos:])
	if size < 1 {
		size = 1
	}
	s.emit(TokenInvalid, s.mode, s.pos, s.pos+size, false)
}

func bracketSlot(b byte) int {
	switch b {
	case ')':
		return 0
	case ']':
		return 1
	case '}':
		return 2
	default:
		return -1
	}
}

func (s *scanner) popJinjaBracketsTo(idx int) {
	for _, popped := range s.jinjaBrackets[idx:] {
		if slot := bracketSlot(popped); slot >= 0 {
			s.jinjaBracketCounts[slot]--
		}
	}
	s.jinjaBrackets = s.jinjaBrackets[:idx]
}

func (s *scanner) updateJinjaBrackets(b byte) {
	switch b {
	case '(':
		s.jinjaBrackets = append(s.jinjaBrackets, ')')
		s.jinjaBracketCounts[0]++
	case '[':
		s.jinjaBrackets = append(s.jinjaBrackets, ']')
		s.jinjaBracketCounts[1]++
	case '{':
		s.jinjaBrackets = append(s.jinjaBrackets, '}')
		s.jinjaBracketCounts[2]++
	case ')', ']', '}':
		slot := bracketSlot(b)
		if s.jinjaBracketCounts[slot] == 0 {
			if n := len(s.jinjaBrackets); n > 0 && b != '}' {
				s.popJinjaBracketsTo(n - 1)
			}

			return
		}
		for idx, ch := range slices.Backward(s.jinjaBrackets) {
			if ch == b {
				s.popJinjaBracketsTo(idx)

				return
			}
		}
	default:
	}
}

func (s *scanner) scanJinjaRaw() {
	endRawPos := findEndRawTag(s.src, s.pos)
	if endRawPos < 0 {
		s.emit(TokenJinjaRawBody, ModeJinjaRaw, s.pos, len(s.src), true)

		return
	}
	if endRawPos > s.pos {
		s.emit(TokenJinjaRawBody, ModeJinjaRaw, s.pos, endRawPos, false)
	}
	s.mode = ModeSQL
}

func findEndRawTag(src string, from int) int {
	for idx := from; idx < len(src); {
		rel := strings.Index(src[idx:], "{%")
		if rel < 0 {
			return -1
		}
		tagPos := idx + rel
		cur := tagPos + 2
		if cur < len(src) && (src[cur] == '-' || src[cur] == '+') {
			cur++
		}
		for cur < len(src) && isAnySpace(src[cur]) {
			cur++
		}
		if hasPrefixAt(src, cur, "endraw") {
			afterKW := cur + len("endraw")
			for afterKW < len(src) && isAnySpace(src[afterKW]) {
				afterKW++
			}
			if hasPrefixAt(src, afterKW, "%}") || hasPrefixAt(src, afterKW, "-%}") || hasPrefixAt(src, afterKW, "+%}") {
				return tagPos
			}
		}
		idx = tagPos + 2
	}

	return -1
}

func matchSQLStringPrefix(src string, pos int) (prefixLen int, isBytes, ok bool) {
	b := src[pos]
	if b == '\'' || b == '"' {
		return 0, false, true
	}
	if pos+2 < len(src) && (src[pos+2] == '\'' || src[pos+2] == '"') {
		b0 := toLowerASCII(b)
		b1 := toLowerASCII(src[pos+1])
		if (b0 == 'r' && b1 == 'b') || (b0 == 'b' && b1 == 'r') {
			return 2, true, true
		}
	}
	if pos+1 < len(src) && (src[pos+1] == '\'' || src[pos+1] == '"') {
		switch toLowerASCII(b) {
		case 'r':
			return 1, false, true
		case 'b':
			return 1, true, true
		default:
		}
	}

	return 0, false, false
}

func scanSingleLineQuoted(src string, start int, quote byte) (end int, unclosed bool) {
	for i := start; i < len(src); {
		ch := src[i]
		if ch == '\r' || ch == '\n' {
			return i, true
		}
		if ch == quote {
			return i + 1, false
		}
		if ch == '\\' {
			if i+1 < len(src) && src[i+1] != '\r' && src[i+1] != '\n' {
				i += 2
				continue
			}
			i++
			continue
		}
		i++
	}

	return len(src), true
}

func scanTripleQuoted(src string, start int, quote byte) (end int, unclosed bool) {
	for i := start; i < len(src); {
		ch := src[i]
		if ch == quote && i+2 < len(src) && src[i+1] == quote && src[i+2] == quote {
			return i + 3, false
		}
		if ch == '\\' && i+1 < len(src) {
			i += 2
			continue
		}
		i++
	}

	return len(src), true
}

func scanMultilineJinjaString(src string, start int, quote byte) (end int, unclosed bool) {
	for i := start; i < len(src); {
		ch := src[i]
		if ch == quote {
			return i + 1, false
		}
		if ch == '\\' && i+1 < len(src) {
			i += 2
			continue
		}
		i++
	}

	return len(src), true
}

func scanJinjaPrefixedInt(src string, start int) (int, bool) {
	var valid func(byte) bool
	switch src[start+1] {
	case 'b', 'B':
		valid = func(b byte) bool { return b == '0' || b == '1' }
	case 'o', 'O':
		valid = func(b byte) bool { return b >= '0' && b <= '7' }
	case 'x', 'X':
		valid = isHexDigit
	default:
		return 0, false
	}
	if !valid(src[start+2]) {
		return 0, false
	}
	end := start + 3
	for end < len(src) && (valid(src[end]) || src[end] == '_') {
		end++
	}

	return end, true
}

func consumeExponent(src string, pos int, allowUnderscore bool) int {
	if pos >= len(src) || (src[pos] != 'e' && src[pos] != 'E') {
		return pos
	}
	cur := pos + 1
	if cur < len(src) && (src[cur] == '+' || src[cur] == '-') {
		cur++
	}
	if cur >= len(src) || !isDigit(src[cur]) {
		return pos
	}
	cur++
	for cur < len(src) && (isDigit(src[cur]) || (allowUnderscore && src[cur] == '_')) {
		cur++
	}

	return cur
}

func hasPrefixAt(s string, pos int, prefix string) bool {
	return pos+len(prefix) <= len(s) && s[pos:pos+len(prefix)] == prefix
}

func isHorizSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\v' || b == '\f'
}

func isAnySpace(b byte) bool {
	return isHorizSpace(b) || b == '\r' || b == '\n'
}

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func isHexDigit(b byte) bool {
	return isDigit(b) || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func isASCIIIdentStart(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_'
}

func isASCIIIdentCont(b byte) bool {
	return isASCIIIdentStart(b) || isDigit(b)
}

func toLowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}

	return b
}
