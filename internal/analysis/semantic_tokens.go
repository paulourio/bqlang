package analysis

import (
	"context"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/paulourio/bqlang/internal/syntax"
)

// ByteSemanticToken represents a classified token in document byte coordinates.
type ByteSemanticToken struct {
	Start     int
	End       int
	Type      SemanticTokenType
	Modifiers SemanticTokenModifier
	Priority  uint8
}

// AbsSemanticToken represents a single-line semantic token in 0-based UTF-16 coordinates.
type AbsSemanticToken struct {
	Line      int
	StartChar int
	Length    int
	Type      SemanticTokenType
	Modifiers SemanticTokenModifier
}

func buildSemanticTokens(snap *DocumentSnapshot, lkgTokens []ByteSemanticToken) {
	candidates := extractScannerTokens(snap.Text, snap.Tokens)
	declSet := buildDeclaredVarSet(snap.DeclaredVars)

	for _, st := range snap.Statements {
		if st.ParseErr == nil {
			candidates = appendMappedTokens(candidates, snap, st, st.ParserTokens, declSet)
			candidates = appendMappedTokens(candidates, snap, st, st.ResolvedTokens, nil)

			continue
		}

		candidates = appendLKGTokensForSpan(candidates, snap.Text, snap.Tokens, lkgTokens, st.OrigSpan)
	}

	for _, br := range snap.Branches {
		candidates = append(candidates, br.OrigTokens...)
	}

	merged := resolveOverlaps(candidates)
	abs := splitByteTokensAcrossLines(snap.LineIndex, merged)

	snap.AllByteTokens = merged
	snap.AbsTokens = abs
	snap.TokensEncoded = encodeRelative(abs)
}

func buildDeclaredVarSet(vars []DeclaredVarInfo) map[string]struct{} {
	if len(vars) == 0 {
		return nil
	}

	out := make(map[string]struct{}, len(vars))
	for _, v := range vars {
		out[strings.ToLower(v.Name)] = struct{}{}
	}

	return out
}

func appendMappedTokens(
	dst []ByteSemanticToken,
	snap *DocumentSnapshot,
	st *AnalyzedStatement,
	srcTokens []ByteSemanticToken,
	declSet map[string]struct{},
) []ByteSemanticToken {
	for _, tok := range srcTokens {
		sStart := st.ShadowSpan.Start + tok.Start
		sEnd := st.ShadowSpan.Start + tok.End
		if overlapsExprPlaceholder(snap.Projected.Exprs, sStart, sEnd) {
			continue
		}

		os, oe, ok := shadowSpanToOrig(snap.Projected.OffsetMap, sStart, sEnd)
		if !ok || os < 0 || oe > len(snap.Text) || os >= oe {
			continue
		}

		mapped := tok
		mapped.Start = os
		mapped.End = oe

		if mapped.Type == TokenProperty && len(declSet) > 0 {
			if _, isVar := declSet[strings.ToLower(snap.Text[os:oe])]; isVar {
				mapped.Type = TokenVariable
			}
		}

		dst = append(dst, mapped)
	}

	return dst
}

func overlapsExprPlaceholder(exprs []syntax.ExprPlaceholder, sStart, sEnd int) bool {
	for _, ex := range exprs {
		if sStart < ex.ShadowEnd && sEnd > ex.ShadowStart {
			return true
		}
	}

	return false
}

func appendLKGTokensForSpan(
	dst []ByteSemanticToken,
	text string,
	scanTokens []syntax.Token,
	lkgTokens []ByteSemanticToken,
	span syntax.ByteSpan,
) []ByteSemanticToken {
	for _, lkg := range lkgTokens {
		if lkg.Priority < priorityAST {
			continue
		}

		if lkg.Start < span.Start || lkg.End > span.End || lkg.End > len(text) || lkg.Start >= lkg.End {
			continue
		}

		tok, _, ok := syntax.TokenAt(scanTokens, lkg.Start)
		if !ok || tok.Mode != syntax.ModeSQL || tok.Start != lkg.Start || tok.End != lkg.End {
			continue
		}

		if tok.Kind != syntax.TokenIdent && tok.Kind != syntax.TokenBacktickIdent {
			continue
		}

		dst = append(dst, lkg)
	}

	return dst
}

func shiftByteTokens(tokens []ByteSemanticToken, editStart, editEnd, insLen int) []ByteSemanticToken {
	if len(tokens) == 0 {
		return nil
	}

	delta := insLen - (editEnd - editStart)
	out := make([]ByteSemanticToken, 0, len(tokens))

	for _, tok := range tokens {
		switch {
		case tok.End <= editStart:
			out = append(out, tok)
		case tok.Start >= editEnd:
			shifted := tok
			shifted.Start += delta
			shifted.End += delta
			if shifted.Start >= 0 && shifted.End > shifted.Start {
				out = append(out, shifted)
			}
		default:
			// Overlaps edited range; drop token.
		}
	}

	return out
}

func resolveOverlaps(candidates []ByteSemanticToken) []ByteSemanticToken {
	filtered := make([]ByteSemanticToken, 0, len(candidates))
	for _, c := range candidates {
		if c.Start >= 0 && c.End > c.Start {
			filtered = append(filtered, c)
		}
	}

	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].Priority != filtered[j].Priority {
			return filtered[i].Priority > filtered[j].Priority
		}

		if filtered[i].Start != filtered[j].Start {
			return filtered[i].Start < filtered[j].Start
		}

		return filtered[i].End < filtered[j].End
	})

	var placed []ByteSemanticToken
	for i := 0; i < len(filtered); {
		prio := filtered[i].Priority
		j := i + 1
		for j < len(filtered) && filtered[j].Priority == prio {
			j++
		}

		level := mergeSamePrioritySorted(filtered[i:j])
		placed = overlayPriorityLevel(placed, level)
		i = j
	}

	return placed
}

func mergeSamePrioritySorted(tokens []ByteSemanticToken) []ByteSemanticToken {
	out := make([]ByteSemanticToken, 0, len(tokens))
	for _, tok := range tokens {
		if len(out) == 0 {
			out = append(out, tok)
			continue
		}

		last := &out[len(out)-1]
		if tok.Start >= last.End {
			out = append(out, tok)
			continue
		}

		if tok.Start == last.Start && tok.End == last.End {
			last.Modifiers |= tok.Modifiers
			continue
		}

		if tok.End > last.End {
			tok.Start = last.End
			if tok.End > tok.Start {
				out = append(out, tok)
			}
		}
	}

	return out
}

func overlayPriorityLevel(placed, level []ByteSemanticToken) []ByteSemanticToken {
	if len(placed) == 0 {
		return level
	}

	if len(level) == 0 {
		return placed
	}

	out := make([]ByteSemanticToken, 0, len(placed)+len(level))
	pIdx := 0

	for _, tok := range level {
		out, pIdx = overlaySingleLevelToken(out, placed, pIdx, tok)
	}

	out = append(out, placed[pIdx:]...)

	return out
}

func overlaySingleLevelToken(
	out []ByteSemanticToken,
	placed []ByteSemanticToken,
	pIdx int,
	cur ByteSemanticToken,
) ([]ByteSemanticToken, int) {
	if len(out) > 0 && cur.Start < out[len(out)-1].End {
		cur.Start = out[len(out)-1].End
	}

	if cur.Start >= cur.End {
		return out, pIdx
	}

	for pIdx < len(placed) && placed[pIdx].End <= cur.Start {
		out = append(out, placed[pIdx])
		pIdx++
	}

	for pIdx < len(placed) && placed[pIdx].Start < cur.End {
		p := placed[pIdx]
		if cur.Start < p.Start {
			head := cur
			head.End = p.Start
			out = append(out, head)
		}

		out = append(out, p)
		pIdx++
		cur.Start = p.End
		if cur.Start >= cur.End {
			return out, pIdx
		}
	}

	if cur.Start < cur.End {
		out = append(out, cur)
	}

	return out, pIdx
}

func splitByteTokensAcrossLines(li *syntax.LineIndex, tokens []ByteSemanticToken) []AbsSemanticToken {
	if li == nil || len(tokens) == 0 {
		return nil
	}

	out := make([]AbsSemanticToken, 0, len(tokens))
	for _, tok := range tokens {
		out = appendSplitTokenAcrossLines(out, li, tok)
	}

	return out
}

func appendSplitTokenAcrossLines(out []AbsSemanticToken, li *syntax.LineIndex, tok ByteSemanticToken) []AbsSemanticToken {
	startPos := li.PositionAt(tok.Start)
	endPos := li.PositionAt(tok.End)

	if startPos.Line == endPos.Line {
		if length := endPos.Character - startPos.Character; length > 0 {
			out = append(out, AbsSemanticToken{
				Line:      startPos.Line,
				StartChar: startPos.Character,
				Length:    length,
				Type:      tok.Type,
				Modifiers: tok.Modifiers,
			})
		}

		return out
	}

	for line := startPos.Line; line <= endPos.Line; line++ {
		if seg, ok := multilineLineSegment(li, tok, line, startPos.Line, endPos.Line); ok {
			out = append(out, seg)
		}
	}

	return out
}

func multilineLineSegment(
	li *syntax.LineIndex,
	tok ByteSemanticToken,
	line, startLine, endLine int,
) (AbsSemanticToken, bool) {
	segStart := li.LineStart(line)
	if line == startLine {
		segStart = tok.Start
	}

	segEnd := li.LineEnd(line)
	if line == endLine {
		segEnd = tok.End
	}

	if segEnd <= segStart {
		return AbsSemanticToken{}, false
	}

	sp := li.PositionAt(segStart)
	ep := li.PositionAt(segEnd)
	length := ep.Character - sp.Character
	if length <= 0 {
		return AbsSemanticToken{}, false
	}

	return AbsSemanticToken{
		Line:      line,
		StartChar: sp.Character,
		Length:    length,
		Type:      tok.Type,
		Modifiers: tok.Modifiers,
	}, true
}

func safeUint32(v int) uint32 {
	if v <= 0 {
		return 0
	}

	if v > math.MaxUint32 {
		return math.MaxUint32
	}

	return uint32(v)
}

func encodeRelative(abs []AbsSemanticToken) []uint32 {
	data := make([]uint32, 0, len(abs)*5)
	prevLine := 0
	prevChar := 0

	for _, tok := range abs {
		if tok.Length <= 0 || tok.Line < prevLine {
			continue
		}

		deltaLine := tok.Line - prevLine
		deltaChar := tok.StartChar
		if deltaLine == 0 {
			if tok.StartChar < prevChar {
				continue
			}

			deltaChar = tok.StartChar - prevChar
		}

		data = append(data,
			safeUint32(deltaLine),
			safeUint32(deltaChar),
			safeUint32(tok.Length),
			uint32(tok.Type),
			uint32(tok.Modifiers),
		)

		prevLine = tok.Line
		prevChar = tok.StartChar
	}

	return data
}

// DiffSemanticTokens computes minimal LSP semanticTokens/full/delta edits
// transforming prev into next. When prev and next are equal, it returns a
// non-nil empty slice.
func DiffSemanticTokens(prev, next []uint32) []SemanticTokensEdit {
	if slices.Equal(prev, next) {
		return []SemanticTokensEdit{}
	}

	minLen := min(len(prev), len(next))
	prefix := 0
	for prefix < minLen && prev[prefix] == next[prefix] {
		prefix++
	}

	maxSuffix := min(len(prev)-prefix, len(next)-prefix)
	suffix := 0
	for suffix < maxSuffix && prev[len(prev)-1-suffix] == next[len(next)-1-suffix] {
		suffix++
	}

	delCount := safeUint32(len(prev) - prefix - suffix)
	ins := slices.Clone(next[prefix : len(next)-suffix])

	return []SemanticTokensEdit{
		{
			Start:       safeUint32(prefix),
			DeleteCount: delCount,
			Data:        ins,
		},
	}
}

func (e *Engine) nextResultID() string {
	return strconv.FormatUint(e.resultSeq.Add(1), 10)
}

func recordDeltaSnapshotLocked(entry *docEntry, resultID string, data []uint32) {
	cp := slices.Clone(data)
	if cp == nil {
		cp = []uint32{}
	}

	entry.deltaHistory = append(entry.deltaHistory, tokenDeltaEntry{
		resultID: resultID,
		data:     cp,
	})

	if len(entry.deltaHistory) > maxDeltaHistory {
		entry.deltaHistory = entry.deltaHistory[len(entry.deltaHistory)-maxDeltaHistory:]
	}
}

func findDeltaSnapshotLocked(entry *docEntry, resultID string) ([]uint32, bool) {
	if resultID == "" {
		return nil, false
	}

	for _, item := range slices.Backward(entry.deltaHistory) {
		if item.resultID == resultID {
			return item.data, true
		}
	}

	return nil, false
}

// SemanticTokensFull returns full relative-encoded semantic tokens for uri.
func (e *Engine) SemanticTokensFull(ctx context.Context, uri string) (*SemanticTokensResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	entry, snap, err := e.ensureSnapshot(uri)
	if err != nil {
		return nil, err
	}

	resultID := e.nextResultID()
	data := slices.Clone(snap.TokensEncoded)
	if data == nil {
		data = []uint32{}
	}

	entry.mu.Lock()
	recordDeltaSnapshotLocked(entry, resultID, data)
	entry.mu.Unlock()

	return &SemanticTokensResult{
		ResultID: resultID,
		Data:     data,
	}, nil
}

// SemanticTokensFullDelta computes semantic token edits relative to previousResultID,
// or falls back to a full Data payload when previousResultID is unknown or stale.
func (e *Engine) SemanticTokensFullDelta(
	ctx context.Context,
	uri string,
	previousResultID string,
) (*SemanticTokensDeltaResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	entry, snap, err := e.ensureSnapshot(uri)
	if err != nil {
		return nil, err
	}

	resultID := e.nextResultID()
	nextData := slices.Clone(snap.TokensEncoded)
	if nextData == nil {
		nextData = []uint32{}
	}

	entry.mu.Lock()
	prevData, ok := findDeltaSnapshotLocked(entry, previousResultID)
	recordDeltaSnapshotLocked(entry, resultID, nextData)
	entry.mu.Unlock()

	if !ok {
		return &SemanticTokensDeltaResult{
			ResultID: resultID,
			Edits:    []SemanticTokensEdit{},
			Data:     nextData,
			IsDelta:  false,
		}, nil
	}

	return &SemanticTokensDeltaResult{
		ResultID: resultID,
		Edits:    DiffSemanticTokens(prevData, nextData),
		IsDelta:  true,
	}, nil
}

// SemanticTokensRange returns semantic tokens intersecting rng, encoded from (0, 0).
func (e *Engine) SemanticTokensRange(
	ctx context.Context,
	uri string,
	rng syntax.Range,
) (*SemanticTokensResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	_, snap, err := e.ensureSnapshot(uri)
	if err != nil {
		return nil, err
	}

	filtered := make([]AbsSemanticToken, 0, len(snap.AbsTokens))
	for _, tok := range snap.AbsTokens {
		if absTokenIntersectsRange(tok, rng) {
			filtered = append(filtered, tok)
		}
	}

	data := encodeRelative(filtered)
	if data == nil {
		data = []uint32{}
	}

	return &SemanticTokensResult{
		ResultID: e.nextResultID(),
		Data:     data,
	}, nil
}

func absTokenIntersectsRange(tok AbsSemanticToken, rng syntax.Range) bool {
	if posLessOrEqual(rng.End, rng.Start) {
		return false
	}

	tStart := syntax.Position{Line: tok.Line, Character: tok.StartChar}
	tEnd := syntax.Position{Line: tok.Line, Character: tok.StartChar + tok.Length}

	return posLess(tStart, rng.End) && posLess(rng.Start, tEnd)
}

func posLess(a, b syntax.Position) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}

	return a.Character < b.Character
}

func posLessOrEqual(a, b syntax.Position) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}

	return a.Character <= b.Character
}
