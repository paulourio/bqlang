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
	formatBytes := func(raw string) (string, error) {
		return FormatBytes(raw, p.fmt.opts.BytesStyle)
	}

	if comps := selectChildrenOfType[*googlesql.ASTBytesLiteralComponent](n); len(comps) > 1 {
		for _, comp := range comps {
			p.printFormattedLiteral(comp, p.nodeInput(comp), "invalid bytes literal", formatBytes)
		}

		return
	}

	p.printFormattedLiteral(n, p.nodeInput(n), "invalid bytes literal", formatBytes)
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
	p.printStringLiteralWith(sl, func(raw string) (string, error) {
		return FormatString(applyNumericCase(raw, p.fmt.opts.NumericStyle), p.fmt.opts.StringStyle)
	})
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
	p.printStringLiteralWith(n, func(raw string) (string, error) {
		return FormatString(raw, p.fmt.opts.StringStyle)
	})
}

func (p *printer) printStringLiteralWith(
	n *googlesql.ASTStringLiteral,
	formatFn func(string) (string, error),
) {
	if comps := selectChildrenOfType[*googlesql.ASTStringLiteralComponent](n); len(comps) > 1 {
		for _, comp := range comps {
			p.printFormattedLiteral(comp, p.nodeInput(comp), "invalid string literal", formatFn)
		}

		return
	}

	p.printFormattedLiteral(n, p.nodeInput(n), "invalid string literal", formatFn)
}

func (p *printer) printFormattedLiteral(
	n googlesql.ASTNode,
	raw, errMsg string,
	formatFn func(string) (string, error),
) {
	p.moveBefore(n)

	s, err := formatFn(raw)
	if err != nil {
		p.addError(&PrinterError{Msg: errMsg, Err: err, Node: n, Input: &p.input})

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

	prefix := "b"
	noPrefix := s[1:]

	if isRaw {
		prefix = "rb"
		noPrefix = s[2:]
	}

	return formatQuotedBody(prefix, noPrefix, style)
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

	prefix := ""
	noPrefix := s

	if isRaw {
		prefix = "r"
		noPrefix = s[1:]
	}

	return formatQuotedBody(prefix, noPrefix, style)
}

func formatQuotedBody(prefix, noPrefix string, style StringStyle) (string, error) {
	quotesLen := 1
	isTripleQuoted := maybeTripleQuotedStringLiteral(noPrefix)
	singleQuoted := isSingleQuote(noPrefix)

	if isTripleQuoted {
		quotesLen = 3
	}

	content := noPrefix[quotesLen : len(noPrefix)-quotesLen]

	switch style {
	case PreferSingleQuote:
		if singleQuoted || strings.Contains(content, "'") {
			return prefix + noPrefix, nil
		}

		if isTripleQuoted {
			return prefix + "'''" + content + "'''", nil
		}

		return prefix + "'" + content + "'", nil
	case PreferDoubleQuote:
		if !singleQuoted || strings.Contains(content, `"`) {
			return prefix + noPrefix, nil
		}

		if isTripleQuoted {
			return prefix + `"""` + content + `"""`, nil
		}

		return prefix + `"` + content + `"`, nil
	case UnspecifiedStringStyle, AsIsStringStyle:
		return prefix + noPrefix, nil
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
	return len(s) >= 4 &&
		(strings.EqualFold(s[:2], "rb") || strings.EqualFold(s[:2], "br")) &&
		s[2] == s[len(s)-1] &&
		(s[2] == '\'' || s[2] == '"')
}

func maybeHexValue(s string) bool {
	return len(s) > 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X')
}
