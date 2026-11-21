package formatter

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/paulourio/bqlang/extensions"
	"github.com/paulourio/bqlang/extensions/ast"
)

type templatePlaceholders struct {
	Placeholders []*placeholderInfo
	counter      int
}

type placeholderInfo struct {
	Element        *extensions.TemplateElement
	Placeholder    string
	Original       string
	Recoverable    bool
	preserveLeadWS bool
	revertRe       *regexp.Regexp
}

func newTemplatePlaceholders() *templatePlaceholders {
	return &templatePlaceholders{
		Placeholders: make([]*placeholderInfo, 0, 5),
	}
}

// New creates a placeholderInfo for a single template element.
func (t *templatePlaceholders) New(e *extensions.TemplateElement) *placeholderInfo {
	var r *placeholderInfo

	switch e.Kind {
	case ast.TemplateComment:
		r = &placeholderInfo{
			Element:     e,
			Placeholder: eraseNonNewlines(e.Image),
			Recoverable: false,
		}
	case ast.TemplateForBlock, ast.TemplateIfBlock:
		t.counter++
		tok := t.makeName()
		r = newControlBlockPlaceholder(e, tok)
	case ast.TemplateVariable:
		t.counter++
		tok := t.makeName()

		if e.AtLineBegin() && e.AtLineEnd() {
			r = &placeholderInfo{
				Element:     e,
				Placeholder: fmt.Sprintf("SELECT %s;", tok),
				Recoverable: true,
				revertRe:    regexp.MustCompile(`(?i)SELECT\s+` + regexp.QuoteMeta(tok) + `\s*;?`),
			}
		} else {
			r = newInlinePlaceholder(e, tok)
		}
	case ast.TemplateSetBlock:
		t.counter++
		tok := t.makeName()

		if e.AtLineBegin() && e.AtLineEnd() {
			r = &placeholderInfo{
				Element:     e,
				Placeholder: fmt.Sprintf("SET %s = 1;", tok),
				Recoverable: true,
				revertRe:    regexp.MustCompile(`(?i)SET\s+` + regexp.QuoteMeta(tok) + `\s*=\s*1\s*;?`),
			}
		} else {
			r = newInlinePlaceholder(e, tok)
		}
	default:
		panic(fmt.Sprintf("templatePlaceholders.New: unexpected kind %v (%v)",
			e.Kind.String(), reflect.TypeOf(e)))
	}

	t.Placeholders = append(t.Placeholders, r)

	return r
}

func newInlinePlaceholder(e *extensions.TemplateElement, tok string) *placeholderInfo {
	return &placeholderInfo{
		Element:     e,
		Placeholder: tok,
		Recoverable: true,
		revertRe:    regexp.MustCompile(`(?i)` + regexp.QuoteMeta(tok)),
	}
}

func eraseNonNewlines(s string) string {
	buf := []byte(s)
	for i, b := range buf {
		if b != '\n' {
			buf[i] = ' '
		}
	}

	return string(buf)
}

// ApplyAll replaces all template elements in input with valid SQL placeholders,
// shifting byte offsets of subsequent template elements and comments whenever a
// short template element expands to a longer placeholder.
func (t *templatePlaceholders) ApplyAll(
	input string,
	elems []*extensions.TemplateElement,
	comms []*extensions.Comment,
) string {
	for i, e := range elems {
		ph := t.New(e)
		oldEnd := e.End

		var delta int

		input, delta = ph.applyWithDelta(input)

		if delta != 0 {
			for j := i + 1; j < len(elems); j++ {
				elems[j].Start += delta
				elems[j].End += delta
			}

			for _, c := range comms {
				if c.Start >= oldEnd {
					c.Start += delta
					c.End += delta
				}
			}
		}
	}

	return input
}

func (t *templatePlaceholders) makeName() string {
	return fmt.Sprintf("__bqfmt_tmpl_%d__", t.counter)
}

func (p *placeholderInfo) applyWithDelta(input string) (string, int) {
	start, end := p.Element.Start, p.Element.End
	size := end - start
	p.Original = input[start:end]

	rep := p.Placeholder
	if p.Recoverable && len(rep) > 0 {
		if start > 0 && isIdentByte(input[start-1]) && isIdentByte(rep[0]) {
			rep = " " + rep
		}

		if len(rep) >= size && end < len(input) && isIdentByte(input[end]) && isIdentByte(rep[len(rep)-1]) {
			rep += " "
		}
	}

	if len(rep) <= size {
		pad := strings.Repeat(" ", size-len(rep))

		return input[:start] + rep + pad + input[end:], 0
	}

	delta := len(rep) - size

	return input[:start] + rep + input[end:], delta
}

func (p *placeholderInfo) Revert(input string) string {
	if !p.Recoverable || p.revertRe == nil {
		return input
	}

	if p.preserveLeadWS {
		if loc := p.revertRe.FindStringSubmatchIndex(input); len(loc) >= 4 {
			ws := input[loc[2]:loc[3]]
			if !strings.Contains(ws, "\n") {
				ws = " "
			}

			return input[:loc[0]] + ws + p.Original + input[loc[1]:]
		}
	}

	loc := p.revertRe.FindStringIndex(input)
	if loc == nil {
		return strings.Replace(input, p.Placeholder, p.Original, 1)
	}

	return input[:loc[0]] + p.Original + input[loc[1]:]
}

func newControlBlockPlaceholder(e *extensions.TemplateElement, tok string) *placeholderInfo {
	body := extractTemplateBlockBody(e.Image)
	if body == "" {
		return newInlinePlaceholder(e, tok)
	}

	qtok := regexp.QuoteMeta(tok)

	if strings.HasPrefix(body, ",") {
		return &placeholderInfo{
			Element:        e,
			Placeholder:    ", " + tok,
			Recoverable:    true,
			preserveLeadWS: true,
			revertRe:       regexp.MustCompile(`(?i),([ \t]*\n[ \t]*|[ \t]*)` + qtok),
		}
	}

	if strings.HasSuffix(body, ",") {
		return &placeholderInfo{
			Element:     e,
			Placeholder: tok + ",",
			Recoverable: true,
			revertRe:    regexp.MustCompile(`(?i)` + qtok + `\s*,`),
		}
	}

	if ph := newKeywordBlockPlaceholder(e, tok, qtok, body); ph != nil {
		return ph
	}

	return newInlinePlaceholder(e, tok)
}

func newKeywordBlockPlaceholder(
	e *extensions.TemplateElement,
	tok, qtok, body string,
) *placeholderInfo {
	w1, w2 := leadingSQLKeywords(body)
	if w1 == "" {
		return nil
	}

	placeholder, pattern := keywordBlockRule(w1, w2, tok, qtok)
	if placeholder == "" {
		return nil
	}

	return &placeholderInfo{
		Element:     e,
		Placeholder: placeholder,
		Recoverable: true,
		revertRe:    regexp.MustCompile(pattern),
	}
}

func keywordBlockRule(w1, w2, tok, qtok string) (string, string) {
	switch w1 {
	case "SELECT", "WITH":
		return "SELECT " + tok, `(?i)\bSELECT\s+` + qtok
	case "AND":
		return "AND " + tok, `(?i)\bAND\s+` + qtok
	case "OR":
		return "OR " + tok, `(?i)\bOR\s+` + qtok
	case "WHERE":
		return "WHERE " + tok, `(?i)\bWHERE\s+` + qtok
	case "HAVING":
		return "HAVING " + tok, `(?i)\bHAVING\s+` + qtok
	case "QUALIFY":
		return "QUALIFY " + tok, `(?i)\bQUALIFY\s+` + qtok
	case "GROUP":
		if w2 == "BY" {
			return "GROUP BY " + tok, `(?i)\bGROUP\s+BY\s+` + qtok
		}
	case "ORDER":
		if w2 == "BY" {
			return "ORDER BY " + tok, `(?i)\bORDER\s+BY\s+` + qtok
		}
	case "LIMIT":
		return "LIMIT @" + tok, `(?i)\bLIMIT\s+@` + qtok
	case "UNION", "INTERSECT", "EXCEPT":
		return "UNION ALL SELECT " + tok, `(?i)\bUNION\s+ALL\s+SELECT\s+` + qtok
	case "WHEN":
		return "WHEN " + tok + " THEN " + tok, `(?i)\bWHEN\s+` + qtok + `\s+THEN\s+` + qtok
	default:
		if isJoinLeadingKeywords(w1, w2) {
			return "CROSS JOIN " + tok, `(?i)\bCROSS\s+JOIN\s+` + qtok
		}
	}

	return "", ""
}

func isJoinLeadingKeywords(w1, w2 string) bool {
	if w1 == "JOIN" {
		return true
	}

	switch w1 {
	case "INNER", "LEFT", "RIGHT", "FULL", "CROSS":
		return w2 == "JOIN" || w2 == "OUTER"
	}

	return false
}

// extractTemplateBlockBody returns the trimmed SQL body of the first non-empty
// top-level branch inside a Jinja2 {% if %} or {% for %} block.
func extractTemplateBlockBody(img string) string {
	depth := 0
	bodyStart := -1

	for i := 0; i < len(img); {
		tagStart, tagEnd, ok := nextJinjaTagSpan(img, i)
		if !ok {
			break
		}

		kw := jinjaTagKeyword(img[tagStart:tagEnd])
		if body, done := advanceBlockBranch(img, kw, tagStart, tagEnd, &depth, &bodyStart); done {
			return body
		}

		i = tagEnd
	}

	return ""
}

func nextJinjaTagSpan(img string, from int) (int, int, bool) {
	relStart := strings.Index(img[from:], "{%")
	if relStart < 0 {
		return 0, 0, false
	}

	tagStart := from + relStart

	relEnd := strings.Index(img[tagStart+2:], "%}")
	if relEnd < 0 {
		return 0, 0, false
	}

	return tagStart, tagStart + 4 + relEnd, true
}

func advanceBlockBranch(
	img, kw string,
	tagStart, tagEnd int,
	depth, bodyStart *int,
) (string, bool) {
	switch kw {
	case "if", "for":
		*depth++
		if *depth == 1 {
			*bodyStart = tagEnd
		}
	case "elif", "else":
		if *depth == 1 && *bodyStart >= 0 {
			if body := trimTemplateBodySQL(img[*bodyStart:tagStart]); body != "" {
				return body, true
			}

			*bodyStart = tagEnd
		}
	case "endif", "endfor":
		if *depth == 1 && *bodyStart >= 0 {
			return trimTemplateBodySQL(img[*bodyStart:tagStart]), true
		}

		if *depth > 0 {
			*depth--
		}
	}

	return "", false
}

func jinjaTagKeyword(tag string) string {
	inner := strings.TrimPrefix(tag, "{%")
	inner = strings.TrimSuffix(inner, "%}")
	inner = strings.TrimSpace(strings.Trim(inner, "-"))
	fields := strings.Fields(inner)
	if len(fields) == 0 {
		return ""
	}

	return strings.ToLower(fields[0])
}

func trimTemplateBodySQL(s string) string {
	for {
		s = strings.TrimSpace(s)
		next, stripped := stripLeadingTrivia(s)
		if !stripped {
			break
		}

		s = next
	}

	return strings.TrimSpace(s)
}

func stripLeadingTrivia(s string) (string, bool) {
	switch {
	case strings.HasPrefix(s, "{#"):
		if idx := strings.Index(s[2:], "#}"); idx >= 0 {
			return s[idx+4:], true
		}
	case strings.HasPrefix(s, "{%") && jinjaTagKeyword(firstJinjaTag(s)) == "set":
		if idx := strings.Index(s[2:], "%}"); idx >= 0 {
			return s[idx+4:], true
		}
	case strings.HasPrefix(s, "/*"):
		if idx := strings.Index(s[2:], "*/"); idx >= 0 {
			return s[idx+4:], true
		}
	case strings.HasPrefix(s, "--"), strings.HasPrefix(s, "#"):
		if _, after, ok := strings.Cut(s, "\n"); ok {
			return after, true
		}

		return "", true
	}

	return s, false
}

func firstJinjaTag(s string) string {
	if idx := strings.Index(s[2:], "%}"); idx >= 0 {
		return s[:idx+4]
	}

	return s
}

func leadingSQLKeywords(s string) (string, string) {
	w1, rest := nextSQLWord(s)
	if w1 == "" {
		return "", ""
	}

	w2, _ := nextSQLWord(rest)

	return strings.ToUpper(w1), strings.ToUpper(w2)
}

func nextSQLWord(s string) (string, string) {
	s = strings.TrimSpace(s)
	if len(s) == 0 || !isAlphaByte(s[0]) {
		return "", s
	}

	end := 1
	for end < len(s) && isIdentByte(s[end]) {
		end++
	}

	return s[:end], s[end:]
}

func isAlphaByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isIdentByte(b byte) bool {
	return isAlphaByte(b) || (b >= '0' && b <= '9') || b == '_'
}
