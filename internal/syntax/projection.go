package syntax

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// BranchKind identifies the Jinja2 tag that starts a secondary conditional branch.
type BranchKind uint8

const (
	// BranchElif represents an {% elif ... %} branch.
	BranchElif BranchKind = iota + 1
	// BranchElse represents an {% else %} branch.
	BranchElse
)

// String returns the human-readable name of k.
func (k BranchKind) String() string {
	switch k {
	case BranchElif:
		return "elif"
	case BranchElse:
		return "else"
	default:
		return "unknown"
	}
}

// BranchSpan records a secondary {% elif %} or {% else %} branch blanked in the primary projection.
type BranchSpan struct {
	Kind       BranchKind
	TagStart   int
	TagEnd     int
	BodyStart  int
	BodyEnd    int
	BlockStart int
	BlockEnd   int
	Depth      int
	Closed     bool
}

// ExprPlaceholder records a {{ expr }} substitution in ProjectedSQL.
type ExprPlaceholder struct {
	Index       int
	Placeholder string
	Standalone  bool
	OrigStart   int
	OrigEnd     int
	ShadowStart int
	ShadowEnd   int
}

type offsetSegment struct {
	origStart   int
	origEnd     int
	shadowStart int
	shadowEnd   int
	substituted bool
}

// OffsetMap translates byte offsets between original source and ProjectedSQL.SQL.
type OffsetMap struct {
	origLen   int
	shadowLen int
	segments  []offsetSegment
}

// ShadowByteToOrig maps a byte offset in ProjectedSQL.SQL back to the original source.
// For 1:1 segments and segment boundaries (including len(shadow)), ok is true.
// Strictly inside a synthetic {{ expr }} replacement, it returns (origStart, false).
func (m *OffsetMap) ShadowByteToOrig(shadowOff int) (int, bool) {
	if m == nil || shadowOff < 0 || shadowOff > m.shadowLen {
		return 0, false
	}
	if shadowOff == m.shadowLen {
		return m.origLen, true
	}

	idx := sort.Search(len(m.segments), func(i int) bool {
		return m.segments[i].shadowEnd > shadowOff
	})
	if idx >= len(m.segments) {
		return m.origLen, true
	}

	seg := m.segments[idx]
	if !seg.substituted {
		return seg.origStart + (shadowOff - seg.shadowStart), true
	}
	if shadowOff == seg.shadowStart {
		return seg.origStart, true
	}

	return seg.origStart, false
}

// OrigByteToShadow maps a byte offset in the original source to ProjectedSQL.SQL.
func (m *OffsetMap) OrigByteToShadow(origOff int) int {
	if m == nil || origOff <= 0 {
		return 0
	}
	if origOff >= m.origLen {
		return m.shadowLen
	}

	idx := sort.Search(len(m.segments), func(i int) bool {
		return m.segments[i].origEnd > origOff
	})
	if idx >= len(m.segments) {
		return m.shadowLen
	}

	seg := m.segments[idx]
	if !seg.substituted {
		return seg.shadowStart + (origOff - seg.origStart)
	}

	return seg.shadowStart
}

// ProjectedSQL holds the projected SQL string and metadata for analysis and diagnostics.
type ProjectedSQL struct {
	SQL       string
	OffsetMap *OffsetMap
	Branches  []BranchSpan
	Exprs     []ExprPlaceholder
}

// StatementSpan represents a single top-level SQL statement [Start, End) in src.
type StatementSpan struct {
	Start        int
	End          int
	HasSemicolon bool
}

// ProjectSQL projects mixed GoogleSQL and Jinja2/dbt source into parseable SQL.
// Comments, Jinja control tags, and secondary {% elif %}/{% else %} branches are blanked
// to equal-length horizontal spaces preserving '\r'/'\n', while {{ expr }} is replaced
// with a synthetic identifier or standalone SELECT statement tracked in OffsetMap.
func ProjectSQL(src string, tokens []Token) ProjectedSQL {
	return project(src, tokens, nil)
}

// ProjectBranch projects src with the specified secondary BranchSpan active instead
// of the enclosing block's primary branch.
func ProjectBranch(src string, tokens []Token, branch BranchSpan) ProjectedSQL {
	return project(src, tokens, &branch)
}

func project(src string, tokens []Token, target *BranchSpan) ProjectedSQL {
	if src == "" {
		return ProjectedSQL{
			OffsetMap: &OffsetMap{},
		}
	}
	if tokens == nil {
		tokens = Scan(src)
	}

	blocks := analyzeJinjaBlocks(src, tokens)
	p := projector{
		src:          src,
		tokens:       tokens,
		blocks:       blocks,
		targetBranch: target,
	}
	p.buf.Grow(len(src) + 64)
	p.run()

	return ProjectedSQL{
		SQL: p.buf.String(),
		OffsetMap: &OffsetMap{
			origLen:   len(src),
			shadowLen: p.buf.Len(),
			segments:  p.segments,
		},
		Branches: p.branches,
		Exprs:    p.exprs,
	}
}

type jinjaBlockMeta struct {
	openTokIdx int
	blockStart int
	blockEnd   int
	kw         string
	closed     bool
	branches   []BranchSpan
}

type pendingBranchMeta struct {
	kind     BranchKind
	tagStart int
	tagEnd   int
}

type blockMetaFrame struct {
	metaIdx int
	depth   int
	pending *pendingBranchMeta
}

func analyzeJinjaBlocks(src string, tokens []Token) map[int]*jinjaBlockMeta {
	metas := make([]*jinjaBlockMeta, 0)
	byOpenTok := make(map[int]*jinjaBlockMeta)
	var stack []blockMetaFrame

	for i := 0; i < len(tokens); i++ {
		if tokens[i].Kind != TokenJinjaBlockOpen {
			continue
		}
		endIdx, kw, hasAssign := readJinjaTagInfo(src, tokens, i)
		tagStart := tokens[i].Start
		tagEnd := tokens[endIdx].End

		switch kw {
		case "if", "for", "macro", "call", "filter", "with", "raw":
			m := &jinjaBlockMeta{
				openTokIdx: i,
				blockStart: tagStart,
				blockEnd:   len(src),
				kw:         kw,
			}
			metas = append(metas, m)
			byOpenTok[i] = m
			stack = append(stack, blockMetaFrame{metaIdx: len(metas) - 1, depth: len(stack) + 1})
		case "set":
			if !hasAssign {
				m := &jinjaBlockMeta{
					openTokIdx: i,
					blockStart: tagStart,
					blockEnd:   len(src),
					kw:         kw,
				}
				metas = append(metas, m)
				byOpenTok[i] = m
				stack = append(stack, blockMetaFrame{metaIdx: len(metas) - 1, depth: len(stack) + 1})
			}
		case "elif", "else":
			stack = handleMetaBranch(metas, stack, kw, tagStart, tagEnd)
		default:
			if opener, isEnd := jinjaEndTagPair[kw]; isEnd {
				stack = handleMetaClose(metas, stack, opener, tagStart, tagEnd)
			}
		}
		i = endIdx
	}

	finalizeUnclosedMetas(metas, stack, len(src))

	return byOpenTok
}

var jinjaEndTagPair = map[string]string{
	"endif":     "if",
	"endfor":    "for",
	"endmacro":  "macro",
	"endcall":   "call",
	"endfilter": "filter",
	"endset":    "set",
	"endwith":   "with",
	"endraw":    "raw",
}

func handleMetaBranch(metas []*jinjaBlockMeta, stack []blockMetaFrame, kw string, tagStart, tagEnd int) []blockMetaFrame {
	for idx := len(stack) - 1; idx >= 0; idx-- {
		m := metas[stack[idx].metaIdx]
		if m.kw != "if" && (m.kw != "for" || kw != "else") {
			continue
		}
		if stack[idx].pending != nil {
			pb := stack[idx].pending
			m.branches = append(m.branches, BranchSpan{
				Kind:       pb.kind,
				TagStart:   pb.tagStart,
				TagEnd:     pb.tagEnd,
				BodyStart:  pb.tagEnd,
				BodyEnd:    tagStart,
				BlockStart: m.blockStart,
				Depth:      stack[idx].depth,
			})
		}
		kind := BranchElse
		if kw == "elif" {
			kind = BranchElif
		}
		stack[idx].pending = &pendingBranchMeta{
			kind:     kind,
			tagStart: tagStart,
			tagEnd:   tagEnd,
		}

		break
	}

	return stack
}

func handleMetaClose(metas []*jinjaBlockMeta, stack []blockMetaFrame, opener string, tagStart, tagEnd int) []blockMetaFrame {
	for idx, frame := range slices.Backward(stack) {
		m := metas[frame.metaIdx]
		if m.kw != opener {
			continue
		}
		closeMetaFrame(metas, stack[idx:], tagStart, tagEnd, true)

		return stack[:idx]
	}

	return stack
}

func finalizeUnclosedMetas(metas []*jinjaBlockMeta, stack []blockMetaFrame, eof int) {
	closeMetaFrame(metas, stack, eof, eof, false)
}

func closeMetaFrame(metas []*jinjaBlockMeta, frames []blockMetaFrame, bodyEnd, blockEnd int, closed bool) {
	for _, f := range frames {
		m := metas[f.metaIdx]
		m.blockEnd = blockEnd
		m.closed = closed
		if f.pending != nil {
			m.branches = append(m.branches, BranchSpan{
				Kind:       f.pending.kind,
				TagStart:   f.pending.tagStart,
				TagEnd:     f.pending.tagEnd,
				BodyStart:  f.pending.tagEnd,
				BodyEnd:    bodyEnd,
				BlockStart: m.blockStart,
				BlockEnd:   blockEnd,
				Depth:      f.depth,
				Closed:     closed,
			})
			f.pending = nil
		}
		for bIdx := range m.branches {
			m.branches[bIdx].BlockEnd = blockEnd
			m.branches[bIdx].Closed = closed
		}
	}
}

type projFrame struct {
	meta        *jinjaBlockMeta
	depth       int
	inSecondary bool
	blanking    bool
	openBranch  *BranchSpan
}

type projector struct {
	src          string
	tokens       []Token
	blocks       map[int]*jinjaBlockMeta
	targetBranch *BranchSpan

	buf         strings.Builder
	segments    []offsetSegment
	branches    []BranchSpan
	exprs       []ExprPlaceholder
	stack       []projFrame
	inStatement bool
}

func isStackBlanking(stack []projFrame) bool {
	for _, frame := range slices.Backward(stack) {
		if frame.blanking {
			return true
		}
	}

	return false
}

func (p *projector) isBlanking() bool {
	return isStackBlanking(p.stack)
}

func (p *projector) run() {
	for i := 0; i < len(p.tokens); i++ {
		tok := p.tokens[i]
		switch tok.Kind {
		case TokenJinjaBlockOpen:
			i = p.handleBlockTag(i)
		case TokenJinjaExprOpen:
			i = p.handleExprTag(i)
		case TokenComment, TokenJinjaComment, TokenJinjaRawBody:
			p.appendBlanked(tok.Start, tok.End)
		default:
			if p.isBlanking() {
				p.appendBlanked(tok.Start, tok.End)
			} else {
				p.appendCopied(tok.Start, tok.End)
				p.trackStatementState(tok)
			}
		}
	}
	p.flushUnclosedBranches()
}

func isNonQueryBlockKeyword(kw string) bool {
	switch kw {
	case "macro", "call", "filter", "set", "raw":
		return true
	default:
		return false
	}
}

func inBlankedNonQueryBlockOnStack(stack []projFrame, upTo int) bool {
	for i := 0; i < upTo && i < len(stack); i++ {
		f := stack[i]
		if f.blanking && f.meta != nil && isNonQueryBlockKeyword(f.meta.kw) {
			return true
		}
	}

	return false
}

func (p *projector) inBlankedNonQueryBlock(upTo int) bool {
	return inBlankedNonQueryBlockOnStack(p.stack, upTo)
}

func (p *projector) flushUnclosedBranches() {
	eof := len(p.src)
	for i, f := range slices.Backward(p.stack) {
		if f.openBranch != nil && !p.inBlankedNonQueryBlock(i) {
			b := *f.openBranch
			b.BodyEnd = eof
			b.BlockEnd = eof
			b.Closed = false
			p.branches = append(p.branches, b)
		}
	}
}

func (p *projector) trackStatementState(tok Token) {
	if tok.IsTrivia() {
		return
	}
	if tok.Kind == TokenPunct && p.src[tok.Start] == ';' {
		p.inStatement = false

		return
	}
	p.inStatement = true
}

func (p *projector) handleBlockTag(openIdx int) int {
	endIdx, kw, _ := readJinjaTagInfo(p.src, p.tokens, openIdx)
	tagStart := p.tokens[openIdx].Start
	tagEnd := p.tokens[endIdx].End

	p.stack = p.applyBlockTagToStack(p.stack, openIdx, kw, tagStart, tagEnd, true)
	p.appendBlanked(tagStart, tagEnd)

	return endIdx
}

func (p *projector) applyBlockTagToStack(stack []projFrame, openIdx int, kw string, tagStart, tagEnd int, recordBranches bool) []projFrame {
	switch kw {
	case "if", "for", "with", "macro", "call", "filter", "set", "raw":
		if frame, ok := p.newOpenFrame(openIdx, kw, len(stack)+1); ok {
			stack = append(stack, frame)
		}
	case "elif", "else":
		p.switchBranchOnStack(stack, kw, tagStart, tagEnd, recordBranches)
	default:
		if opener, isEnd := jinjaEndTagPair[kw]; isEnd {
			stack = p.popCloseOnStack(stack, opener, tagStart, tagEnd, recordBranches)
		}
	}

	return stack
}

func (p *projector) newOpenFrame(openIdx int, kw string, depth int) (projFrame, bool) {
	meta, ok := p.blocks[openIdx]
	if !ok {
		return projFrame{}, false
	}

	frame := projFrame{
		meta:  meta,
		depth: depth,
	}

	switch kw {
	case "macro", "call", "filter", "set":
		frame.blanking = meta.closed
	case "raw":
		frame.blanking = true
	case "if", "for":
		frame.blanking = p.shouldBlankPrimaryBranch(meta)
	default:
	}

	return frame, true
}

func (p *projector) shouldBlankPrimaryBranch(meta *jinjaBlockMeta) bool {
	if p.targetBranch == nil {
		return false
	}
	if meta.blockStart == p.targetBranch.BlockStart {
		return true
	}
	for _, b := range meta.branches {
		if b.BodyStart <= p.targetBranch.TagStart && p.targetBranch.TagStart < b.BodyEnd {
			return true
		}
	}

	return false
}

func (p *projector) switchBranchOnStack(stack []projFrame, kw string, tagStart, tagEnd int, recordBranches bool) {
	for idx := len(stack) - 1; idx >= 0; idx-- {
		f := &stack[idx]
		if f.meta.kw != "if" && (f.meta.kw != "for" || kw != "else") {
			continue
		}
		f.inSecondary = true
		f.blanking = p.shouldBlankSecondaryBranch(f.meta, tagStart)
		if !recordBranches {
			break
		}
		suppressed := inBlankedNonQueryBlockOnStack(stack, idx)
		if f.openBranch != nil && !suppressed {
			b := *f.openBranch
			b.BodyEnd = tagStart
			p.branches = append(p.branches, b)
		}
		kind := BranchElse
		if kw == "elif" {
			kind = BranchElif
		}
		if suppressed {
			f.openBranch = nil
		} else {
			f.openBranch = &BranchSpan{
				Kind:       kind,
				TagStart:   tagStart,
				TagEnd:     tagEnd,
				BodyStart:  tagEnd,
				BlockStart: f.meta.blockStart,
				BlockEnd:   f.meta.blockEnd,
				Depth:      f.depth,
				Closed:     f.meta.closed,
			}
		}

		break
	}
}

func (p *projector) shouldBlankSecondaryBranch(meta *jinjaBlockMeta, tagStart int) bool {
	if p.targetBranch == nil {
		return true
	}
	if meta.blockStart == p.targetBranch.BlockStart {
		return tagStart != p.targetBranch.TagStart
	}
	for _, b := range meta.branches {
		if b.TagStart == tagStart && b.BodyStart <= p.targetBranch.TagStart && p.targetBranch.TagStart < b.BodyEnd {
			return false
		}
	}

	return true
}

func (p *projector) popCloseOnStack(stack []projFrame, opener string, tagStart, tagEnd int, recordBranches bool) []projFrame {
	for idx, frame := range slices.Backward(stack) {
		if frame.meta.kw != opener {
			continue
		}
		if recordBranches {
			for i := len(stack) - 1; i >= idx; i-- {
				f := stack[i]
				if f.openBranch != nil && !inBlankedNonQueryBlockOnStack(stack, i) {
					b := *f.openBranch
					b.BodyEnd = tagStart
					b.BlockEnd = tagEnd
					b.Closed = true
					p.branches = append(p.branches, b)
				}
			}
		}

		return stack[:idx]
	}

	return stack
}

func findExprCloseIdx(tokens []Token, openIdx int) int {
	endIdx := openIdx
	for endIdx+1 < len(tokens) {
		endIdx++
		if tokens[endIdx].Kind == TokenJinjaExprClose {
			break
		}
	}

	return endIdx
}

func (p *projector) handleExprTag(openIdx int) int {
	endIdx := findExprCloseIdx(p.tokens, openIdx)

	origStart := p.tokens[openIdx].Start
	origEnd := p.tokens[endIdx].End

	if p.isBlanking() {
		p.appendBlanked(origStart, origEnd)

		return endIdx
	}

	exprIdx := len(p.exprs)
	name := fmt.Sprintf("__bqpls_expr_%d__", exprIdx)
	standalone := !p.inStatement && atLineBegin(p.buf.String(), p.buf.Len()) && p.atLineEndOrComment(endIdx)

	rep := name
	if standalone {
		rep = "SELECT " + name + ";"
	}

	shadowStart := p.buf.Len()
	shadowEnd := shadowStart + len(rep)

	trailing := ""
	hasIdentPrefix := origStart > 0 && isASCIIIdentCont(p.src[origStart-1])
	if !standalone && !hasIdentPrefix && origEnd < len(p.src) && isASCIIIdentCont(p.src[origEnd]) {
		trailing = " "
	}
	trailing += extractLineBreaks(p.src[origStart:origEnd])

	p.appendReplaced(origStart, origEnd, rep)
	if trailing != "" {
		p.appendReplaced(origEnd, origEnd, trailing)
	}
	p.exprs = append(p.exprs, ExprPlaceholder{
		Index:       exprIdx,
		Placeholder: name,
		Standalone:  standalone,
		OrigStart:   origStart,
		OrigEnd:     origEnd,
		ShadowStart: shadowStart,
		ShadowEnd:   shadowEnd,
	})

	if standalone {
		p.inStatement = false
	} else {
		p.inStatement = true
	}

	return endIdx
}

func (p *projector) appendCopied(origStart, origEnd int) {
	if origStart >= origEnd {
		return
	}
	shadowStart := p.buf.Len()
	p.buf.WriteString(p.src[origStart:origEnd])
	p.recordOneToOne(origStart, origEnd, shadowStart, p.buf.Len())
}

func (p *projector) appendBlanked(origStart, origEnd int) {
	if origStart >= origEnd {
		return
	}
	shadowStart := p.buf.Len()
	for i := origStart; i < origEnd; i++ {
		b := p.src[i]
		if b == '\n' || b == '\r' {
			p.buf.WriteByte(b)
		} else {
			p.buf.WriteByte(' ')
		}
	}
	p.recordOneToOne(origStart, origEnd, shadowStart, p.buf.Len())
}

func (p *projector) appendReplaced(origStart, origEnd int, rep string) {
	shadowStart := p.buf.Len()
	p.buf.WriteString(rep)
	p.segments = append(p.segments, offsetSegment{
		origStart:   origStart,
		origEnd:     origEnd,
		shadowStart: shadowStart,
		shadowEnd:   p.buf.Len(),
		substituted: true,
	})
}

func (p *projector) recordOneToOne(origStart, origEnd, shadowStart, shadowEnd int) {
	if n := len(p.segments); n > 0 {
		last := &p.segments[n-1]
		if !last.substituted && last.origEnd == origStart && last.shadowEnd == shadowStart {
			last.origEnd = origEnd
			last.shadowEnd = shadowEnd

			return
		}
	}
	p.segments = append(p.segments, offsetSegment{
		origStart:   origStart,
		origEnd:     origEnd,
		shadowStart: shadowStart,
		shadowEnd:   shadowEnd,
		substituted: false,
	})
}

func extractLineBreaks(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return ""
	}
	var b strings.Builder
	for i := range len(s) {
		if s[i] == '\r' || s[i] == '\n' {
			b.WriteByte(s[i])
		}
	}

	return b.String()
}

func readJinjaTagInfo(src string, tokens []Token, openIdx int) (endIdx int, kw string, hasAssign bool) {
	endIdx = openIdx
	depth := 0
	for i := openIdx + 1; i < len(tokens); i++ {
		tok := tokens[i]
		endIdx = i
		switch tok.Kind {
		case TokenJinjaBlockClose:
			return endIdx, kw, hasAssign
		case TokenJinjaTagKeyword:
			if kw == "" {
				kw = tok.Text(src)
			}
		case TokenJinjaPunct:
			switch tok.Text(src) {
			case "(", "[", "{":
				depth++
			case ")", "]", "}":
				if depth > 0 {
					depth--
				}
			default:
			}
		case TokenJinjaOperator:
			if depth == 0 && tok.Text(src) == "=" {
				hasAssign = true
			}
		default:
			if tok.Mode != ModeJinjaTag {
				return i - 1, kw, hasAssign
			}
		}
	}

	return endIdx, kw, hasAssign
}

func atLineBegin(src string, offset int) bool {
	for i := offset - 1; i >= 0; i-- {
		b := src[i]
		if b == '\n' || b == '\r' {
			return true
		}
		if !isHorizSpace(b) {
			return false
		}
	}

	return true
}

func (p *projector) atLineEndOrComment(endIdx int) bool {
	simStack := slices.Clone(p.stack)
	for i := endIdx + 1; i < len(p.tokens); i++ {
		tok := p.tokens[i]
		if tok.Kind == TokenNewline {
			return true
		}
		if tok.Kind == TokenWhitespace {
			continue
		}
		if tok.Kind == TokenJinjaBlockOpen {
			closedIdx, kw, _ := readJinjaTagInfo(p.src, p.tokens, i)
			if strings.ContainsAny(p.src[tok.Start:p.tokens[closedIdx].End], "\r\n") {
				return true
			}
			simStack = p.applyBlockTagToStack(simStack, i, kw, tok.Start, p.tokens[closedIdx].End, false)
			i = closedIdx

			continue
		}
		if tok.Kind != TokenComment && tok.Kind != TokenJinjaComment && tok.Kind != TokenJinjaRawBody && !isStackBlanking(simStack) {
			return false
		}
		if tok.Kind == TokenJinjaExprOpen {
			i = findExprCloseIdx(p.tokens, i)
		}
		if strings.ContainsAny(p.src[tok.Start:p.tokens[i].End], "\r\n") {
			return true
		}
	}

	return true
}

type sqlBlockKind uint8

const (
	blockBegin sqlBlockKind = iota + 1
	blockCaseExpr
	blockCaseStmt
	blockIf
	blockWhile
	blockLoop
	blockRepeat
	blockFor
)

var endKeywordBlockMap = map[string]sqlBlockKind{
	"IF":     blockIf,
	"WHILE":  blockWhile,
	"LOOP":   blockLoop,
	"REPEAT": blockRepeat,
	"FOR":    blockFor,
	"CASE":   blockCaseStmt,
}

// SplitStatements splits src into top-level statement spans separated by ';',
// respecting parentheses, brackets, braces, Jinja block nesting, and GoogleSQL
// procedural blocks (BEGIN...END, CASE...END, IF...END IF, LOOP/WHILE/REPEAT/FOR).
func SplitStatements(src string, tokens []Token) []StatementSpan {
	if src == "" {
		return nil
	}
	if tokens == nil {
		tokens = Scan(src)
	}

	sp := stmtSplitter{
		src:         src,
		tokens:      tokens,
		stmtStart:   -1,
		atStmtStart: true,
	}
	sp.run()

	return sp.spans
}

type sqlBlockFrame struct {
	kind        sqlBlockKind
	sawIn       bool
	enteredBody bool
}

type stmtSplitter struct {
	src            string
	tokens         []Token
	spans          []StatementSpan
	stmtStart      int
	stmtEnd        int
	parenDepth     int
	bracketDepth   int
	braceDepth     int
	typeAngleDepth int
	jinjaStack     []string
	sqlStack       []sqlBlockFrame
	atStmtStart    bool
	sawCreate      bool
	inProcHeader   bool
	hasContent     bool
	afterDot       bool
	sawTypeCtor    bool
}

func (s *stmtSplitter) run() {
	for i := 0; i < len(s.tokens); i++ {
		i = s.step(i)
	}
	if s.stmtStart >= 0 && s.hasContent && s.stmtEnd > s.stmtStart {
		s.spans = append(s.spans, StatementSpan{
			Start:        s.stmtStart,
			End:          s.stmtEnd,
			HasSemicolon: false,
		})
	}
}

func (s *stmtSplitter) step(idx int) int {
	tok := s.tokens[idx]
	if tok.Kind == TokenJinjaBlockOpen {
		return s.handleJinjaBlock(idx)
	}
	if tok.Kind == TokenJinjaExprOpen {
		return s.handleJinjaExpr(idx)
	}
	if tok.Mode != ModeSQL || tok.IsTrivia() {
		return idx
	}
	if tok.Kind == TokenInvalid && strings.TrimSpace(tok.Text(s.src)) == "" {
		return idx
	}

	if tok.Kind == TokenPunct && s.src[tok.Start] == ';' {
		return s.handleSemicolon(idx, tok)
	}

	s.markSQLTokenStart(tok.Start)
	s.stmtEnd = tok.End
	s.hasContent = true

	switch tok.Kind {
	case TokenPunct:
		s.sawCreate = false

		return s.handleSQLPunct(idx, tok)
	case TokenOperator:
		s.sawCreate = false
		s.afterDot = false
		s.atStmtStart = false
		s.typeAngleDepth = stepTypeAngleOp(s.src, tok, s.typeAngleDepth, s.sawTypeCtor)
		s.sawTypeCtor = false

		return idx
	case TokenIdent:
		return s.handleSQLIdent(idx, tok)
	case TokenBacktickIdent:
		s.sawCreate = false
		s.afterDot = false
		s.sawTypeCtor = false
		if s.atStmtStart {
			if nextIdx, isLabel := s.tryConsumeLabel(idx); isLabel {
				return nextIdx
			}
		}
		s.atStmtStart = false

		return idx
	default:
		s.sawCreate = false
		s.afterDot = false
		s.sawTypeCtor = false
		s.atStmtStart = false

		return idx
	}
}

func (s *stmtSplitter) markSQLTokenStart(start int) {
	if s.stmtStart < 0 {
		s.stmtStart = start
	}
}

func (s *stmtSplitter) handleJinjaBlock(openIdx int) int {
	endIdx, kw, hasAssign := readJinjaTagInfo(s.src, s.tokens, openIdx)
	switch kw {
	case "if", "for", "macro", "call", "filter", "with", "raw":
		s.jinjaStack = append(s.jinjaStack, kw)
	case "set":
		if !hasAssign {
			s.jinjaStack = append(s.jinjaStack, kw)
		}
	default:
		if opener, isEnd := jinjaEndTagPair[kw]; isEnd {
			for idx, item := range slices.Backward(s.jinjaStack) {
				if item == opener {
					s.jinjaStack = s.jinjaStack[:idx]

					break
				}
			}
		}
	}

	return endIdx
}

func (s *stmtSplitter) handleJinjaExpr(openIdx int) int {
	s.markSQLTokenStart(s.tokens[openIdx].Start)
	endIdx := openIdx
	for endIdx+1 < len(s.tokens) {
		endIdx++
		if s.tokens[endIdx].Kind == TokenJinjaExprClose {
			break
		}
	}
	s.stmtEnd = s.tokens[endIdx].End
	s.hasContent = true
	s.atStmtStart = false
	s.sawCreate = false
	s.afterDot = false
	s.sawTypeCtor = false

	return endIdx
}

func (s *stmtSplitter) handleSQLPunct(idx int, tok Token) int {
	ch := s.src[tok.Start]
	switch ch {
	case '(':
		s.parenDepth++
	case ')':
		if s.parenDepth > 0 {
			s.parenDepth--
		}
	case '[':
		s.bracketDepth++
	case ']':
		if s.bracketDepth > 0 {
			s.bracketDepth--
		}
	case '{':
		s.braceDepth++
	case '}':
		if s.braceDepth > 0 {
			s.braceDepth--
		}
	default:
	}
	s.atStmtStart = false
	s.afterDot = ch == '.'
	s.sawTypeCtor = false

	return idx
}

func markLoopHeaderClosed(frame *sqlBlockFrame) {
	if !frame.enteredBody && (frame.kind == blockWhile || (frame.kind == blockFor && frame.sawIn)) {
		frame.enteredBody = true
	}
}

func (s *stmtSplitter) handleSemicolon(idx int, tok Token) int {
	atTopDepth := s.parenDepth == 0 && s.bracketDepth == 0 && s.braceDepth == 0 && s.typeAngleDepth == 0
	s.atStmtStart = true
	s.sawCreate = false
	s.inProcHeader = false
	s.afterDot = false
	s.sawTypeCtor = false
	s.typeAngleDepth = 0

	if s.parenDepth == 0 && s.bracketDepth == 0 && s.braceDepth == 0 &&
		len(s.jinjaStack) == 0 && len(s.sqlStack) == 0 {
		if s.stmtStart >= 0 && s.hasContent {
			s.spans = append(s.spans, StatementSpan{
				Start:        s.stmtStart,
				End:          tok.End,
				HasSemicolon: true,
			})
		}
		s.stmtStart = -1
		s.stmtEnd = 0
		s.hasContent = false

		return idx
	}

	if atTopDepth && len(s.sqlStack) > 0 {
		markLoopHeaderClosed(&s.sqlStack[len(s.sqlStack)-1])
	}

	if s.stmtStart >= 0 {
		s.stmtEnd = tok.End
	}

	return idx
}

func (s *stmtSplitter) handleSQLIdent(idx int, tok Token) int {
	wasAfterDot := s.afterDot
	s.afterDot = false
	wasSawCreate := s.sawCreate
	s.sawCreate = false
	s.sawTypeCtor = false
	if wasAfterDot {
		s.atStmtStart = false

		return idx
	}

	upper := strings.ToUpper(tok.Text(s.src))
	if s.typeAngleDepth > 0 {
		s.sawTypeCtor = isTypeAngleCtor(upper)
		s.atStmtStart = false

		return idx
	}

	if s.atStmtStart {
		if nextIdx, isLabel := s.tryConsumeLabel(idx); isLabel {
			return nextIdx
		}
	}

	switch upper {
	case "CREATE":
		s.sawCreate = s.atStmtStart && s.parenDepth == 0
		s.atStmtStart = false
	case "OR", "REPLACE", "TEMP", "TEMPORARY", "PUBLIC", "PRIVATE":
		s.sawCreate = wasSawCreate && s.parenDepth == 0
		s.atStmtStart = false
	case "PROCEDURE":
		s.inProcHeader = wasSawCreate && s.parenDepth == 0
		s.atStmtStart = false
	case "IN":
		s.handleIn()
	case "BEGIN":
		s.handleBegin(idx)
	case "CASE":
		s.pushCaseBlock()
	case "IF":
		s.handleIf(idx)
	case "WHILE", "LOOP", "FOR":
		s.pushSimpleBlock(upper)
	case "REPEAT":
		s.handleRepeat(idx)
	case "THEN", "ELSE", "DO":
		s.handleBodyTransition(upper, idx)
	case "END":
		return s.handleEnd(idx)
	default:
		s.sawTypeCtor = isTypeAngleCtor(upper)
		s.atStmtStart = false
	}

	return idx
}

func (s *stmtSplitter) handleIn() {
	if s.parenDepth == 0 && len(s.sqlStack) > 0 {
		if top := &s.sqlStack[len(s.sqlStack)-1]; top.kind == blockFor && !top.sawIn {
			top.sawIn = true
		}
	}
	s.atStmtStart = false
}

func (s *stmtSplitter) pushCaseBlock() {
	if s.atStmtStart {
		s.sqlStack = append(s.sqlStack, sqlBlockFrame{kind: blockCaseStmt})
	} else {
		s.sqlStack = append(s.sqlStack, sqlBlockFrame{kind: blockCaseExpr})
	}
	s.atStmtStart = false
}

func (s *stmtSplitter) pushSimpleBlock(upper string) {
	if !s.atStmtStart || s.parenDepth != 0 {
		s.atStmtStart = false

		return
	}
	switch upper {
	case "WHILE":
		s.sqlStack = append(s.sqlStack, sqlBlockFrame{kind: blockWhile})
		s.atStmtStart = false
	case "LOOP":
		s.sqlStack = append(s.sqlStack, sqlBlockFrame{kind: blockLoop})
		s.atStmtStart = true
	case "FOR":
		s.sqlStack = append(s.sqlStack, sqlBlockFrame{kind: blockFor})
		s.atStmtStart = false
	default:
		s.atStmtStart = false
	}
}

func (s *stmtSplitter) tryConsumeLabelIdx(idx int) (int, bool) {
	nextIdx, ok := s.nextSQLToken(idx + 1)
	if !ok || s.tokens[nextIdx].Kind != TokenPunct || s.src[s.tokens[nextIdx].Start] != ':' {
		return idx, false
	}

	return nextIdx, true
}

func (s *stmtSplitter) tryConsumeLabel(idx int) (int, bool) {
	nextIdx, isLabel := s.tryConsumeLabelIdx(idx)
	if !isLabel {
		return idx, false
	}
	s.stmtEnd = s.tokens[nextIdx].End
	s.hasContent = true
	s.atStmtStart = true

	return nextIdx, true
}

func (s *stmtSplitter) isBeginBlockAt(idx int) bool {
	nextIdx, ok := s.nextSQLToken(idx + 1)
	if !ok {
		return false
	}
	nextTok := s.tokens[nextIdx]
	if nextTok.Kind == TokenPunct && s.src[nextTok.Start] == ';' {
		return false
	}
	if nextTok.Kind == TokenIdent {
		switch strings.ToUpper(nextTok.Text(s.src)) {
		case "TRANSACTION", "WORK", "READ", "ISOLATION":
			return false
		default:
		}
	}

	return true
}

func (s *stmtSplitter) handleBegin(idx int) {
	if (!s.atStmtStart && !s.inProcHeader) || s.parenDepth != 0 {
		s.atStmtStart = false

		return
	}
	if !s.isBeginBlockAt(idx) {
		s.atStmtStart = false

		return
	}

	s.sqlStack = append(s.sqlStack, sqlBlockFrame{kind: blockBegin})
	s.inProcHeader = false
	s.sawCreate = false
	s.atStmtStart = true
}

func isTypeAngleCtor(upper string) bool {
	return upper == "STRUCT" || upper == "ARRAY" || upper == "RANGE"
}

func stepTypeAngleOp(src string, tok Token, depth int, sawTypeCtor bool) int {
	switch tok.Text(src) {
	case "<":
		if sawTypeCtor {
			return depth + 1
		}
	case ">":
		if depth > 0 {
			return depth - 1
		}
	case ">>":
		if depth >= 2 {
			return depth - 2
		}

		return 0
	default:
	}

	return depth
}

func (s *stmtSplitter) isIfStatementAt(idx int) bool {
	nextIdx, ok := s.nextSQLToken(idx + 1)
	if !ok {
		return false
	}
	if s.tokens[nextIdx].Kind == TokenPunct && s.src[s.tokens[nextIdx].Start] == '(' {
		afterParen, hasAfter := s.skipParenGroup(nextIdx)

		return hasAfter && s.hasThenBeforeSemi(afterParen)
	}

	return true
}

func (s *stmtSplitter) handleIf(idx int) {
	if s.atStmtStart && s.parenDepth == 0 && s.isIfStatementAt(idx) {
		s.sqlStack = append(s.sqlStack, sqlBlockFrame{kind: blockIf})
	}
	s.atStmtStart = false
}

func stepThenPunct(ch byte, depth int) (nextDepth int, atSemi bool) {
	switch ch {
	case '(':
		return depth + 1, false
	case ')':
		if depth > 0 {
			return depth - 1, false
		}

		return 0, false
	case ';':
		return depth, depth == 0
	default:
		return depth, false
	}
}

type thenLookahead struct {
	src            string
	depth          int
	typeAngleDepth int
	caseDepth      int
	afterDot       bool
	sawTypeCtor    bool
}

func (s *stmtSplitter) hasThenBeforeSemi(from int) bool {
	st := thenLookahead{src: s.src}
	for i := from; i < len(s.tokens); i++ {
		found, done := st.step(s.tokens[i])
		if done {
			return found
		}
	}

	return false
}

func (st *thenLookahead) step(tok Token) (found, done bool) {
	if tok.Mode != ModeSQL || tok.IsTrivia() {
		return false, false
	}
	if tok.Kind == TokenPunct {
		ch := st.src[tok.Start]
		st.afterDot = ch == '.'
		st.sawTypeCtor = false
		st.depth, done = stepThenPunct(ch, st.depth)

		return false, done
	}
	wasAfterDot := st.afterDot
	st.afterDot = false
	if tok.Kind == TokenOperator {
		st.typeAngleDepth = stepTypeAngleOp(st.src, tok, st.typeAngleDepth, st.sawTypeCtor)
		st.sawTypeCtor = false

		return false, false
	}
	st.sawTypeCtor = false
	if tok.Kind != TokenIdent || wasAfterDot {
		return false, false
	}

	upper := strings.ToUpper(tok.Text(st.src))
	st.sawTypeCtor = isTypeAngleCtor(upper)
	if st.depth != 0 || st.typeAngleDepth != 0 {
		return false, false
	}

	switch upper {
	case "CASE":
		st.caseDepth++
	case "END":
		if st.caseDepth > 0 {
			st.caseDepth--
		}
	case "THEN":
		if st.caseDepth == 0 {
			return true, true
		}
	default:
	}

	return false, false
}

func (s *stmtSplitter) handleRepeat(idx int) {
	if !s.atStmtStart || s.parenDepth != 0 {
		s.atStmtStart = false

		return
	}
	nextIdx, ok := s.nextSQLToken(idx + 1)
	if ok && s.tokens[nextIdx].Kind == TokenPunct && s.src[s.tokens[nextIdx].Start] == '(' {
		afterParen, hasAfter := s.skipParenGroup(nextIdx)
		if !hasAfter || !s.hasUntilAhead(afterParen) {
			s.atStmtStart = false

			return
		}
	}
	s.sqlStack = append(s.sqlStack, sqlBlockFrame{kind: blockRepeat})
	s.atStmtStart = true
}

type untilLookahead struct {
	parenDepth     int
	bracketDepth   int
	braceDepth     int
	typeAngleDepth int
	repeatDepth    int
	caseExprDepth  int
	blocks         []sqlBlockFrame
	atStart        bool
	afterDot       bool
	sawTypeCtor    bool
	found          bool
	done           bool
}

func (s *stmtSplitter) hasUntilAhead(from int) bool {
	st := untilLookahead{
		repeatDepth: 1,
		blocks:      []sqlBlockFrame{{kind: blockRepeat}},
		atStart:     true,
	}
	for i := from; i < len(s.tokens) && !st.done; i++ {
		i = s.stepUntilLookahead(&st, i)
	}

	return st.found
}

func (s *stmtSplitter) stepUntilLookahead(st *untilLookahead, idx int) int {
	tok := s.tokens[idx]
	if tok.Mode != ModeSQL || tok.IsTrivia() {
		return idx
	}
	if tok.Kind == TokenOperator {
		st.typeAngleDepth = stepTypeAngleOp(s.src, tok, st.typeAngleDepth, st.sawTypeCtor)
		st.sawTypeCtor = false
		st.afterDot = false
		st.atStart = false

		return idx
	}
	if tok.Kind == TokenPunct {
		s.stepLookaheadPunct(st, s.src[tok.Start])

		return idx
	}

	wasAfterDot := st.afterDot
	st.afterDot = false
	st.sawTypeCtor = false
	if st.parenDepth != 0 || st.bracketDepth != 0 || st.braceDepth != 0 || st.typeAngleDepth > 0 {
		if !wasAfterDot && tok.Kind == TokenIdent {
			st.sawTypeCtor = isTypeAngleCtor(strings.ToUpper(tok.Text(s.src)))
		}
		st.atStart = false

		return idx
	}
	if wasAfterDot {
		st.atStart = false

		return idx
	}
	if st.atStart && (tok.Kind == TokenIdent || tok.Kind == TokenBacktickIdent) {
		if nextIdx, isLabel := s.tryConsumeLabelIdx(idx); isLabel {
			return nextIdx
		}
	}
	if tok.Kind != TokenIdent {
		st.atStart = false

		return idx
	}

	return s.advanceUntilIdent(st, idx)
}

func (s *stmtSplitter) advanceUntilIdent(st *untilLookahead, idx int) int {
	upper := strings.ToUpper(s.tokens[idx].Text(s.src))
	switch upper {
	case "UNTIL":
		if st.atStart && st.repeatDepth == 1 {
			st.found = true
			st.done = true
		}
		st.atStart = false
	case "REPEAT":
		if st.atStart {
			st.repeatDepth++
			st.blocks = append(st.blocks, sqlBlockFrame{kind: blockRepeat})
			st.atStart = true

			return idx
		}
		st.atStart = false
	case "BEGIN":
		if st.atStart && s.isBeginBlockAt(idx) {
			st.blocks = append(st.blocks, sqlBlockFrame{kind: blockBegin})
			st.atStart = true

			return idx
		}
		st.atStart = false
	case "IF":
		if st.atStart && s.isIfStatementAt(idx) {
			st.blocks = append(st.blocks, sqlBlockFrame{kind: blockIf})
		}
		st.atStart = false
	case "CASE":
		s.pushLookaheadCase(st)
	case "WHILE", "LOOP", "FOR":
		s.pushLookaheadLoop(st, upper)
	case "IN":
		s.handleLookaheadIn(st)
	case "THEN", "ELSE", "DO":
		s.handleLookaheadBodyTransition(st, upper, idx)
	case "END":
		return s.handleLookaheadEnd(st, idx)
	default:
		st.sawTypeCtor = isTypeAngleCtor(upper)
		st.atStart = false
	}

	return idx
}

func (s *stmtSplitter) pushLookaheadCase(st *untilLookahead) {
	if st.atStart {
		st.blocks = append(st.blocks, sqlBlockFrame{kind: blockCaseStmt})
	} else {
		st.caseExprDepth++
		st.blocks = append(st.blocks, sqlBlockFrame{kind: blockCaseExpr})
	}
	st.atStart = false
}

func (s *stmtSplitter) pushLookaheadLoop(st *untilLookahead, upper string) {
	if !st.atStart {
		st.atStart = false

		return
	}
	switch upper {
	case "WHILE":
		st.blocks = append(st.blocks, sqlBlockFrame{kind: blockWhile})
		st.atStart = false
	case "LOOP":
		st.blocks = append(st.blocks, sqlBlockFrame{kind: blockLoop})
		st.atStart = true
	case "FOR":
		st.blocks = append(st.blocks, sqlBlockFrame{kind: blockFor})
		st.atStart = false
	default:
		st.atStart = false
	}
}

func (s *stmtSplitter) handleLookaheadIn(st *untilLookahead) {
	if len(st.blocks) > 0 {
		if top := &st.blocks[len(st.blocks)-1]; top.kind == blockFor && !top.sawIn {
			top.sawIn = true
		}
	}
	st.atStart = false
}

func (s *stmtSplitter) handleLookaheadBodyTransition(st *untilLookahead, kw string, idx int) {
	if st.caseExprDepth != 0 || st.typeAngleDepth != 0 || len(st.blocks) == 0 {
		st.atStart = false

		return
	}
	top := &st.blocks[len(st.blocks)-1]
	switch kw {
	case "THEN":
		st.atStart = top.kind == blockIf || top.kind == blockCaseStmt || top.kind == blockBegin
	case "ELSE":
		st.atStart = top.kind == blockIf || top.kind == blockCaseStmt
	case "DO":
		if !top.enteredBody && ((top.kind == blockWhile && s.isDoBodyStart(idx)) || (top.kind == blockFor && top.sawIn)) {
			top.enteredBody = true
			st.atStart = true
		} else {
			st.atStart = false
		}
	default:
		st.atStart = false
	}
}

func popTopLookaheadFrame(st *untilLookahead) {
	top := st.blocks[len(st.blocks)-1].kind
	st.blocks = st.blocks[:len(st.blocks)-1]
	if top == blockCaseExpr && st.caseExprDepth > 0 {
		st.caseExprDepth--
	}
}

func (s *stmtSplitter) handleLookaheadEnd(st *untilLookahead, idx int) int {
	st.atStart = false
	if len(st.blocks) == 0 {
		return idx
	}

	top := st.blocks[len(st.blocks)-1].kind
	nextIdx, ok := s.nextSQLToken(idx + 1)
	if ok && s.tokens[nextIdx].Kind == TokenIdent {
		nextUpper := strings.ToUpper(s.tokens[nextIdx].Text(s.src))
		if targetKind, isCompound := endKeywordBlockMap[nextUpper]; isCompound {
			if (top == blockCaseExpr || top == blockBegin) && isUnreservedEndAlias(nextUpper) {
				popTopLookaheadFrame(st)

				return idx
			}
			s.popLookaheadTarget(st, targetKind)

			return nextIdx
		}
	}

	if top == blockBegin || top == blockCaseExpr || top == blockCaseStmt {
		popTopLookaheadFrame(st)
	}

	return idx
}

func (s *stmtSplitter) popLookaheadTarget(st *untilLookahead, target sqlBlockKind) {
	if target == blockRepeat {
		s.popLookaheadRepeat(st)

		return
	}
	s.popLookaheadBlock(st, target)
}

func discardLookaheadFrames(st *untilLookahead, idx int) {
	for _, popped := range st.blocks[idx:] {
		switch popped.kind {
		case blockRepeat:
			st.repeatDepth--
		case blockCaseExpr:
			if st.caseExprDepth > 0 {
				st.caseExprDepth--
			}
		default:
		}
	}
	st.blocks = st.blocks[:idx]
	if st.repeatDepth <= 0 {
		st.done = true
	}
}

func (s *stmtSplitter) popLookaheadBlock(st *untilLookahead, target sqlBlockKind) {
	for idx, frame := range slices.Backward(st.blocks) {
		if frame.kind == target || (target == blockCaseStmt && frame.kind == blockCaseExpr) {
			discardLookaheadFrames(st, idx)

			return
		}
	}
}

func (s *stmtSplitter) popLookaheadRepeat(st *untilLookahead) {
	for idx, frame := range slices.Backward(st.blocks) {
		if frame.kind == blockRepeat {
			discardLookaheadFrames(st, idx)

			return
		}
	}
	st.repeatDepth--
	if st.repeatDepth <= 0 {
		st.done = true
	}
}

func (s *stmtSplitter) stepLookaheadPunct(st *untilLookahead, ch byte) {
	st.sawTypeCtor = false
	st.afterDot = ch == '.'
	switch ch {
	case '(':
		st.parenDepth++
	case ')':
		if st.parenDepth > 0 {
			st.parenDepth--
		}
	case '[':
		st.bracketDepth++
	case ']':
		if st.bracketDepth > 0 {
			st.bracketDepth--
		}
	case '{':
		st.braceDepth++
	case '}':
		if st.braceDepth > 0 {
			st.braceDepth--
		}
	case ';':
		atTopDepth := st.parenDepth == 0 && st.bracketDepth == 0 && st.braceDepth == 0 && st.typeAngleDepth == 0
		st.typeAngleDepth = 0
		if atTopDepth && len(st.blocks) > 0 {
			markLoopHeaderClosed(&st.blocks[len(st.blocks)-1])
		}
		st.atStart = st.parenDepth == 0 && st.bracketDepth == 0 && st.braceDepth == 0

		return
	default:
	}
	st.atStart = false
}

func (s *stmtSplitter) handleBodyTransition(kw string, idx int) {
	if len(s.sqlStack) == 0 {
		s.atStmtStart = false

		return
	}
	atTopDepth := s.parenDepth == 0 && s.bracketDepth == 0 && s.braceDepth == 0 && s.typeAngleDepth == 0
	top := &s.sqlStack[len(s.sqlStack)-1]
	switch kw {
	case "THEN":
		s.atStmtStart = atTopDepth && (top.kind == blockIf || top.kind == blockCaseStmt || top.kind == blockBegin)
	case "ELSE":
		s.atStmtStart = atTopDepth && (top.kind == blockIf || top.kind == blockCaseStmt)
	case "DO":
		if s.canEnterDoBody(top, idx) {
			top.enteredBody = true
			s.atStmtStart = true
		} else {
			s.atStmtStart = false
		}
	default:
		s.atStmtStart = false
	}
}

func (s *stmtSplitter) canEnterDoBody(top *sqlBlockFrame, doIdx int) bool {
	if s.parenDepth != 0 || s.bracketDepth != 0 || s.braceDepth != 0 || s.typeAngleDepth != 0 || top.enteredBody {
		return false
	}
	switch top.kind {
	case blockFor:
		return top.sawIn
	case blockWhile:
		return s.isDoBodyStart(doIdx)
	default:
		return false
	}
}

func (s *stmtSplitter) isDoBodyStart(doIdx int) bool {
	nextIdx, ok := s.nextSQLToken(doIdx + 1)
	if !ok {
		return true
	}
	tok := s.tokens[nextIdx]
	if tok.Kind == TokenOperator {
		return false
	}
	if tok.Kind == TokenPunct {
		return s.isDoBodyStartPunct(nextIdx, s.src[tok.Start])
	}
	if tok.Kind == TokenIdent {
		return s.isDoBodyStartIdent(nextIdx, strings.ToUpper(tok.Text(s.src)))
	}

	return true
}

func (s *stmtSplitter) isDoBodyStartPunct(nextIdx int, ch byte) bool {
	switch ch {
	case '.', ',', ')', ']', '[', ';', ':':
		return false
	case '(':
		afterParen, hasAfter := s.skipParenGroup(nextIdx)
		if !hasAfter {
			return false
		}

		return !s.isDoParenContinuation(afterParen)
	default:
		return true
	}
}

func isDoConditionKeyword(upper string) bool {
	switch upper {
	case "AND", "OR", "IS", "IN", "BETWEEN", "LIKE", "NOT", "AT", "COLLATE":
		return true
	default:
		return false
	}
}

func isIntervalUnitKeyword(upper string) bool {
	switch upper {
	case "YEAR", "QUARTER", "MONTH", "WEEK", "DAY",
		"HOUR", "MINUTE", "SECOND", "MILLISECOND", "MICROSECOND", "NANOSECOND":
		return true
	default:
		return false
	}
}

func (s *stmtSplitter) hasColonAfter(idx int) bool {
	afterIdx, hasAfter := s.nextSQLToken(idx + 1)

	return hasAfter && s.tokens[afterIdx].Kind == TokenPunct && s.src[s.tokens[afterIdx].Start] == ':'
}

func (s *stmtSplitter) isDoParenContinuation(afterIdx int) bool {
	tok := s.tokens[afterIdx]
	if tok.Kind == TokenOperator {
		return tok.Text(s.src) != "|>"
	}
	if tok.Kind == TokenPunct {
		switch s.src[tok.Start] {
		case '.', '[', ')', ',', ':':
			return true
		default:
			return false
		}
	}
	if tok.Kind == TokenIdent {
		upper := strings.ToUpper(tok.Text(s.src))
		if upper == "DO" || isDoConditionKeyword(upper) {
			return true
		}
		if isIntervalUnitKeyword(upper) {
			return !s.hasColonAfter(afterIdx)
		}
	}

	return false
}

func (s *stmtSplitter) isDoBodyStartIdent(nextIdx int, upper string) bool {
	if isDoConditionKeyword(upper) {
		return false
	}
	if upper == "DO" || isIntervalUnitKeyword(upper) {
		return s.hasColonAfter(nextIdx)
	}

	return true
}

func isUnreservedEndAlias(upper string) bool {
	return upper == "REPEAT" || upper == "LOOP" || upper == "WHILE"
}

func (s *stmtSplitter) handleEnd(idx int) int {
	s.atStmtStart = false
	if len(s.sqlStack) == 0 {
		return idx
	}

	top := s.sqlStack[len(s.sqlStack)-1].kind
	nextIdx, ok := s.nextSQLToken(idx + 1)
	if ok && s.tokens[nextIdx].Kind == TokenIdent {
		nextUpper := strings.ToUpper(s.tokens[nextIdx].Text(s.src))
		if targetKind, isCompound := endKeywordBlockMap[nextUpper]; isCompound {
			if (top == blockCaseExpr || top == blockBegin) && isUnreservedEndAlias(nextUpper) {
				s.sqlStack = s.sqlStack[:len(s.sqlStack)-1]

				return idx
			}
			s.popMatchingBlock(targetKind)
			s.stmtEnd = s.tokens[nextIdx].End

			return nextIdx
		}
	}

	if top == blockBegin || top == blockCaseExpr || top == blockCaseStmt {
		s.sqlStack = s.sqlStack[:len(s.sqlStack)-1]
	}

	return idx
}

func (s *stmtSplitter) popMatchingBlock(target sqlBlockKind) {
	for idx, frame := range slices.Backward(s.sqlStack) {
		if frame.kind == target || (target == blockCaseStmt && frame.kind == blockCaseExpr) {
			s.sqlStack = s.sqlStack[:idx]

			return
		}
	}
}

func (s *stmtSplitter) nextSQLToken(from int) (int, bool) {
	for i := from; i < len(s.tokens); i++ {
		tok := s.tokens[i]
		if tok.Mode == ModeSQL && !tok.IsTrivia() {
			return i, true
		}
	}

	return -1, false
}

func (s *stmtSplitter) skipParenGroup(openParenIdx int) (int, bool) {
	depth := 0
	for i := openParenIdx; i < len(s.tokens); i++ {
		tok := s.tokens[i]
		if tok.Mode != ModeSQL || tok.Kind != TokenPunct {
			continue
		}
		switch s.src[tok.Start] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s.nextSQLToken(i + 1)
			}
		default:
		}
	}

	return -1, false
}
