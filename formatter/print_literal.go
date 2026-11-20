package formatter

import (
	"fmt"
	"strings"

	"github.com/goccy/go-googlesql"
)

func (p *printer) visitBigNumericLiteral(n *googlesql.ASTBigNumericLiteral, _ printerData) {
	sl, _ := n.StringLiteral()
	p.printScaledNumericLiteral("BIGNUMERIC", n, sl)
}

func (p *printer) visitBoolLiteral(n *googlesql.ASTBooleanLiteral, _ printerData) {
	p.moveBefore(n)
	img, _ := n.Image()
	p.print(applyPrintCase(img, p.fmt.opts.BoolStyle))
}

func (p *printer) visitBytesLiteral(n *googlesql.ASTBytesLiteral, _ printerData) {
	p.moveBefore(n)

	raw := p.nodeInput(n)

	s, err := FormatBytes(raw, p.fmt.opts.BytesStyle)
	if err != nil {
		p.addError(&PrinterError{Msg: "invalid bytes literal", Err: err, Node: n, Input: &p.input})

		s = raw
	}

	p.print(strings.ReplaceAll(s, "\n", literalLineBreakPlaceholder))
}

func (p *printer) visitDateOrTimeLiteral(n *googlesql.ASTDateOrTimeLiteral, d printerData) {
	p.moveBefore(n)

	tk, _ := n.TypeKind()

	switch tk {
	case googlesql.TypeKindTypeDate:
		p.print(p.keyword("DATE"))
	case googlesql.TypeKindTypeDatetime:
		p.print(p.keyword("DATETIME"))
	case googlesql.TypeKindTypeTime:
		p.print(p.keyword("TIME"))
	case googlesql.TypeKindTypeTimestamp:
		p.print(p.keyword("TIMESTAMP"))
	default:
		p.addError(&PrinterError{
			Msg:   fmt.Sprintf("unsupported date/time literal TypeKind: %d", int(tk)),
			Node:  n,
			Input: &p.input,
		})
	}

	if sl, _ := n.StringLiteral(); nodeDefined(sl) {
		p.accept(sl, d)
	}
}

func (p *printer) visitFloatLiteral(n *googlesql.ASTFloatLiteral, _ printerData) {
	p.moveBefore(n)
	img, _ := n.Image()
	p.print(applyNumericCase(img, p.fmt.opts.NumericStyle))
}

func (p *printer) visitIntLiteral(n *googlesql.ASTIntLiteral, _ printerData) {
	p.moveBefore(n)
	v, _ := n.Image()

	if !maybeHexValue(v) {
		p.print(v)
	} else {
		p.print("0x" + applyPrintCase(v[2:], p.fmt.opts.HexStyle))
	}

	p.movePast(n)
}

func (p *printer) visitJSONLiteral(n *googlesql.ASTJSONLiteral, d printerData) {
	p.moveBefore(n)
	p.print(p.keyword("JSON"))

	if sl, _ := n.StringLiteral(); nodeDefined(sl) {
		p.accept(sl, d)
	}
}

func (p *printer) visitNullLiteral(n *googlesql.ASTNullLiteral, _ printerData) {
	p.moveBefore(n)
	img, _ := n.Image()
	p.print(applyPrintCase(img, p.fmt.opts.NullStyle))
}

func (p *printer) visitNumericLiteral(n *googlesql.ASTNumericLiteral, _ printerData) {
	sl, _ := n.StringLiteral()
	p.printScaledNumericLiteral("NUMERIC", n, sl)
}

func (p *printer) printScaledNumericLiteral(
	kw string,
	n googlesql.ASTNode,
	sl *googlesql.ASTStringLiteral,
) {
	p.moveBefore(n)
	p.print(p.keyword(kw))

	if nodeDefined(sl) {
		p.printNumericStringLiteral(sl)
	}
}

func (p *printer) printNumericStringLiteral(sl *googlesql.ASTStringLiteral) {
	p.moveBefore(sl)

	raw := applyNumericCase(p.nodeInput(sl), p.fmt.opts.NumericStyle)

	s, err := FormatString(raw, p.fmt.opts.StringStyle)
	if err != nil {
		p.addError(&PrinterError{Msg: "invalid string literal", Err: err, Node: sl, Input: &p.input})

		s = raw
	}

	p.print(strings.ReplaceAll(s, "\n", literalLineBreakPlaceholder))
}

func (p *printer) visitRangeLiteral(n *googlesql.ASTRangeLiteral, d printerData) {
	p.moveBefore(n)

	if t, _ := n.Type(); nodeDefined(t) {
		p.accept(t, d)
	}

	if rv, _ := n.RangeValue(); nodeDefined(rv) {
		p.accept(rv, d)
	}
}

func (p *printer) visitStringLiteral(n *googlesql.ASTStringLiteral, _ printerData) {
	p.moveBefore(n)

	raw := p.nodeInput(n)

	s, err := FormatString(raw, p.fmt.opts.StringStyle)
	if err != nil {
		p.addError(&PrinterError{Msg: "invalid string literal", Err: err, Node: n, Input: &p.input})

		s = raw
	}

	p.print(strings.ReplaceAll(s, "\n", literalLineBreakPlaceholder))
}

func applyNumericCase(s string, style PrintCase) string {
	switch style {
	case UpperCase:
		return strings.ToUpper(s)
	case AsIs:
		return s
	case LowerCase, Unspecified:
		return strings.ToLower(s)
	}

	return s
}

// FormatBytes normalizes the quote delimiters and prefix casing of a GoogleSQL bytes literal.
func FormatBytes(s string, style StringStyle) (string, error) {
	isBytes := maybeBytesLiteral(s)
	isRaw := maybeRawBytesLiteral(s)

	if !isBytes && !isRaw {
		return "", ErrInvalidBytesLiteral
	}

	if style == UnspecifiedStringStyle || style == AsIsStringStyle {
		return s, nil
	}

	offset := 1
	prefix := "b"
	noPrefix := s[1:]

	if isRaw {
		prefix = "rb"
		noPrefix = s[2:]
		offset = 2
	}

	return formatQuotedBody(s, prefix, noPrefix, offset, style)
}

// FormatString normalizes the quote delimiters and prefix casing of a GoogleSQL string literal.
func FormatString(s string, style StringStyle) (string, error) {
	isStr := maybeStringLiteral(s)
	isRaw := maybeRawStringLiteral(s)

	if !isStr && !isRaw {
		return "", ErrInvalidStringLiteral
	}

	if style == UnspecifiedStringStyle || style == AsIsStringStyle {
		return s, nil
	}

	offset := 0
	prefix := ""
	noPrefix := s

	if isRaw {
		prefix = "r"
		noPrefix = s[1:]
		offset = 1
	}

	return formatQuotedBody(s, prefix, noPrefix, offset, style)
}

func formatQuotedBody(s, prefix, noPrefix string, offset int, style StringStyle) (string, error) {
	quotesLen := 1
	isTripleQuoted := maybeTripleQuotedStringLiteral(noPrefix)
	singleQuoted := isSingleQuote(noPrefix)

	if isTripleQuoted {
		quotesLen = 3
	}

	offset += quotesLen
	content := s[offset : len(s)-quotesLen]

	switch style {
	case PreferSingleQuote:
		if singleQuoted || strings.Contains(content, "'") {
			return prefix + s[len(prefix):], nil
		}

		if isTripleQuoted {
			return fmt.Sprintf("%s'''%s'''", prefix, content), nil
		}

		return fmt.Sprintf("%s'%s'", prefix, content), nil
	case PreferDoubleQuote:
		if !singleQuoted || strings.Contains(content, `"`) {
			return prefix + s[len(prefix):], nil
		}

		if isTripleQuoted {
			return fmt.Sprintf(`%s"""%s"""`, prefix, content), nil
		}

		return fmt.Sprintf(`%s"%s"`, prefix, content), nil
	case UnspecifiedStringStyle, AsIsStringStyle:
		return s, nil
	}

	return "", ErrInvalidStringStyle
}

func isSingleQuote(s string) bool {
	return len(s) > 0 && s[0] == '\''
}

func maybeTripleQuotedStringLiteral(s string) bool {
	return len(s) >= 6 &&
		((strings.HasPrefix(s, "'''") && strings.HasSuffix(s, "'''")) ||
			(strings.HasPrefix(s, `"""`) && strings.HasSuffix(s, `"""`)))
}

func maybeStringLiteral(s string) bool {
	return len(s) >= 2 && s[0] == s[len(s)-1] && (s[0] == '\'' || s[0] == '"')
}

func maybeRawStringLiteral(s string) bool {
	return len(s) >= 3 &&
		(s[0] == 'r' || s[0] == 'R') &&
		s[1] == s[len(s)-1] &&
		(s[1] == '\'' || s[1] == '"')
}

func maybeBytesLiteral(s string) bool {
	return len(s) >= 3 &&
		(s[0] == 'b' || s[0] == 'B') &&
		s[1] == s[len(s)-1] &&
		(s[1] == '\'' || s[1] == '"')
}

func maybeRawBytesLiteral(s string) bool {
	if len(s) < 4 {
		return false
	}

	low := strings.ToLower(s[:2])

	return (low == "rb" || low == "br") &&
		s[2] == s[len(s)-1] &&
		(s[2] == '\'' || s[2] == '"')
}

func maybeHexValue(s string) bool {
	return len(s) > 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X')
}
