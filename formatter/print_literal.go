// This file contains routines for formatting string.s
package formatter

import (
	"fmt"
	"strings"

	"github.com/goccy/go-zetasql/ast"
)

func (p *Printer) VisitBigNumericLiteral(n *ast.BigNumericLiteralNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("BIGNUMERIC"))
	p.print(strings.ToLower(n.Image()))
}

func (p *Printer) VisitBoolLiteral(n *ast.BooleanLiteralNode, d Data) {
	p.moveBefore(n)
	p.print(formatPrintStyle(n.Image(), p.fmt.opts.BoolStyle))
}

func (p *Printer) VisitBytesLiteral(n *ast.BytesLiteralNode, d Data) {
	p.moveBefore(n)

	s, err := FormatBytes(n.Image(), p.fmt.opts.BytesStyle)
	if err != nil {
		panic(err)
	}

	p.print(strings.ReplaceAll(s, "\n", lineBreakPlaceholder))
}

func (p *Printer) VisitDateOrTimeLiteral(n *ast.DateOrTimeLiteralNode, d Data) {
	p.moveBefore(n)

	// There's a bug in the mapping of TypeKind to actual type.
	// For instance, TIMESTAMP (11) is being mapped as NUMERIC (19).
	// For now, the safest approach seems to re-tokenize the node input.
	input := p.nodeInput(n)

	pos := strings.Index(input, " ")
	if pos < 0 {
		panic("invalid date time literal")
	}

	token := strings.ToUpper(viewString(input, 0, pos))

	switch token {
	case "DATE":
		p.print(p.keyword("DATE"))
	case "DATETIME":
		p.print(p.keyword("DATETIME"))
	case "TIME":
		p.print(p.keyword("TIME"))
	case "TIMESTAMP":
		p.print(p.keyword("TIMESTAMP"))
	default:
		p.addError(&PrinterError{
			Msg: fmt.Sprintf("failed to parse date time kind: %s", token),
		})
	}

	p.accept(n.StringLiteral(), d)
}

func (p *Printer) VisitFloatLiteral(n *ast.FloatLiteralNode, d Data) {
	p.moveBefore(n)
	p.print(strings.ToLower(n.Image()))
}

func (p *Printer) VisitIntLiteral(n *ast.IntLiteralNode, d Data) {
	p.moveBefore(n)
	v := n.Image()

	if !maybeHexValue(v) {
		p.print(v)
	} else {
		p.print("0x" + formatPrintStyle(v[2:], p.fmt.opts.HexStyle))
	}

	p.movePast(n)
}

func (p *Printer) VisitNullLiteral(n *ast.NullLiteralNode, d Data) {
	p.moveBefore(n)
	p.print(formatPrintStyle(n.Image(), p.fmt.opts.NullStyle))
}

func formatPrintStyle(s string, style PrintCase) string {
	switch style {
	case Unspecified, AsIs:
		return s
	case LowerCase:
		return strings.ToLower(s)
	case UpperCase:
		return strings.ToUpper(s)
	}

	panic(fmt.Sprintf("invalid print style %#v", style))
}

func (p *Printer) VisitNumericLiteral(n *ast.NumericLiteralNode, d Data) {
	p.moveBefore(n)
	p.print(p.keyword("NUMERIC"))
	p.print(strings.ToLower(n.Image()))
}

func (p *Printer) VisitStringLiteral(n *ast.StringLiteralNode, d Data) {
	p.moveBefore(n)

	s, err := FormatString(n.Image(), p.fmt.opts.StringStyle)
	if err != nil {
		panic(err)
	}

	p.print(strings.ReplaceAll(s, "\n", lineBreakPlaceholder))
}

func FormatBytes(s string, style StringStyle) (string, error) {
	isBytes := maybeBytesLiteral(s)
	isRaw := maybeRawBytesLiteral(s)

	if !isBytes && !isRaw {
		return "", ErrInvalidStringLiteral
	}

	if style == AsIsStringStyle {
		return s, nil
	}

	offset := 0 // Offset to control the error position.
	prefix := ""
	noPrefix := s

	// Strip off the prefix from the raw string content before
	// parsing.
	if isRaw {
		prefix = "rb"
		noPrefix = noPrefix[2:]
		offset = 2
	} else {
		prefix = "b"
		noPrefix = noPrefix[1:]
		offset = 1
	}

	quotesLen := 1
	isTripleQuoted := maybeTripleQuotedStringLiteral(noPrefix)
	isSingleQuote := isSingleQuote(noPrefix)

	if isTripleQuoted {
		quotesLen = 3
	}

	offset += quotesLen
	content := s[offset : len(s)-quotesLen]

	// if s == "RB'''abc'''" {
	// 	panic(
	// 		fmt.Sprintf(
	// 			"Bytes: %#v\nStyle: %v\noffset: %d\nnoPrefix: %#v\nquotesLen: %d\ncontent: %v\nIsSingleQuote: %v\nIsTripleQuoted: %v\nPrefix: %v\nContains ': %v\n",
	// 			s, style, offset, noPrefix, quotesLen, content,
	// 			isSingleQuote, isTripleQuoted, prefix,
	// 			strings.Contains(content, "'"),
	// 		))
	// }

	if style == PreferSingleQuote {
		if isSingleQuote || strings.Contains(content, "'") {
			return prefix + s[len(prefix):], nil
		}

		if isTripleQuoted {
			return fmt.Sprintf("%s'''%s'''", prefix, content), nil
		}

		return fmt.Sprintf("%s'%s'", prefix, content), nil
	}

	if style == PreferDoubleQuote {
		if isSingleQuote || strings.Contains(content, `"`) {
			return s, nil
		}

		if isTripleQuoted {
			return fmt.Sprintf(`%s"""%s"""`, prefix, content), nil
		}

		return fmt.Sprintf(`%s"%s"`, prefix, content), nil
	}

	return "", ErrInvalidStringStyle
}

func FormatString(s string, style StringStyle) (string, error) {
	isStr := maybeStringLiteral(s)
	isRaw := maybeRawStringLiteral(s)

	if !isStr && !isRaw {
		return "", ErrInvalidStringLiteral
	}

	if style == AsIsStringStyle {
		return s, nil
	}

	offset := 0 // Offset to control the error position.
	prefix := ""
	noPrefix := s

	if isRaw {
		// Strip off the prefix 'r' from the raw string content before
		// parsing.
		prefix = "r"
		noPrefix = noPrefix[1:]
		offset = 1
	}

	quotesLen := 1
	isTripleQuoted := maybeTripleQuotedStringLiteral(noPrefix)
	isSingleQuote := isSingleQuote(noPrefix)

	if isTripleQuoted {
		quotesLen = 3
	}

	offset += quotesLen
	content := s[offset : len(s)-quotesLen]

	if style == PreferSingleQuote {
		if isSingleQuote || strings.Contains(content, "'") {
			return prefix + s[len(prefix):], nil
		}

		if isTripleQuoted {
			return fmt.Sprintf("%s'''%s'''", prefix, content), nil
		}

		return fmt.Sprintf("%s'%s'", prefix, content), nil
	}

	if style == PreferDoubleQuote {
		if isSingleQuote || strings.Contains(content, `"`) {
			return s, nil
		}

		if isTripleQuoted {
			return fmt.Sprintf(`%s"""%s"""`, prefix, content), nil
		}

		return fmt.Sprintf(`%s"%s"`, prefix, content), nil
	}

	return "", ErrInvalidStringStyle
}

func isSingleQuote(s string) bool {
	return s[0] == '\''
}

func maybeTripleQuotedStringLiteral(s string) bool {
	if len(s) >= 6 &&
		(strings.HasPrefix(s, "'''") && strings.HasSuffix(s, "'''") ||
			strings.HasPrefix(s, `"""`) && strings.HasSuffix(s, `"""`)) {
		return true
	}

	return false
}

func maybeStringLiteral(s string) bool {
	if (len(s) >= 2) &&
		(s[0] == s[len(s)-1]) &&
		(s[0] == '\'' || s[0] == '"') {
		return true
	}

	return false
}

func maybeRawStringLiteral(s string) bool {
	if (len(s) >= 3) &&
		(s[0] == 'r' || s[0] == 'R') &&
		(s[1] == s[len(s)-1]) &&
		(s[1] == '\'' || s[1] == '"') {
		return true
	}

	return false
}

func maybeBytesLiteral(s string) bool {
	if (len(s) >= 3) &&
		(s[0] == 'b' || s[0] == 'B') &&
		(s[1] == s[len(s)-1]) &&
		(s[1] == '\'' || s[1] == '"') {
		return true
	}

	return false
}

func maybeRawBytesLiteral(s string) bool {
	if len(s) >= 4 {
		low := strings.ToLower(s[:2])

		if (low == "rb" || low == "br") &&
			(s[2] == s[len(s)-1]) &&
			(s[2] == '\'' || s[2] == '"') {
			return true
		}
	}

	return false
}

func maybeHexValue(s string) bool {
	// Note that hex values are always unsigned, and -0xA will be
	// parsed with a unary operator applied to the int literal.
	return len(s) > 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X')
}
