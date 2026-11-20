package formatter

import (
	"strings"

	"github.com/goccy/go-googlesql"
)

func (p *printer) visitMatchRecognizeClause(n *googlesql.ASTMatchRecognizeClause, d printerData) {
	p.printMatchRecognizeBody(n, true, d)
}

func (p *printer) visitPipeMatchRecognize(n *googlesql.ASTPipeMatchRecognize, d printerData) {
	mr, _ := n.MatchRecognize()
	if !nodeDefined(mr) {
		return
	}

	p.printMatchRecognizeBody(mr, false, d)
}

func (p *printer) printMatchRecognizeBody(
	n *googlesql.ASTMatchRecognizeClause,
	leadingNewline bool,
	d printerData,
) {
	p.moveBefore(n)

	if leadingNewline {
		p.println("")
	}

	p.println(p.keyword("MATCH_RECOGNIZE") + " (")
	p.incDepth()

	if pb, _ := n.PartitionBy(); nodeDefined(pb) {
		p.printlnBlock(alignNested(p.toString(pb, d)))
	}

	if ob, _ := n.OrderBy(); nodeDefined(ob) {
		p.printlnBlock(alignNested(p.toString(ob, d)))
	}

	if meas, _ := n.Measures(); nodeDefined(meas) {
		ctx := d
		ctx.singleLineCols = false

		p.println(p.keyword("MEASURES"))
		p.incDepth()
		p.printlnBlock(p.toString(meas, ctx))
		p.decDepth()
	}

	if ams, _ := n.AfterMatchSkipClause(); nodeDefined(ams) {
		p.accept(ams, d)
		p.println("")
	}

	if pat, _ := n.Pattern(); nodeDefined(pat) {
		p.println(p.keyword("PATTERN") + " (" + strings.TrimSpace(p.formatRowPattern(pat, d)) + ")")
	}

	if defs, _ := n.PatternVariableDefinitionList(); nodeDefined(defs) {
		p.printMatchRecognizeDefines(defs, d)
	}

	if opt, _ := n.OptionsList(); nodeDefined(opt) {
		p.printlnBlock(alignNested(p.toString(opt, d)))
	}

	p.decDepth()
	p.print(")")

	if oa, _ := n.OutputAlias(); nodeDefined(oa) {
		p.accept(oa, d)
	}

	p.movePast(n)
}

func (p *printer) printMatchRecognizeDefines(defs *googlesql.ASTSelectList, d printerData) {
	cols := selectChildrenOfType[*googlesql.ASTSelectColumn](defs)
	if len(cols) == 0 {
		return
	}

	p.println(p.keyword("DEFINE"))
	p.incDepth()

	pp := p.nest()

	for i, col := range cols {
		if i > 0 {
			pp.println(",")
		}

		al, _ := col.Alias()
		expr, _ := col.Expression()

		var varName string
		if nodeDefined(al) {
			id, _ := al.Identifier()
			varName = pp.toTrimmedString(id, d)
		}

		exprStr := pp.toAlignedString(expr, d)
		pp.print(varName + " \v" + pp.keyword("AS") + " " + strings.ReplaceAll(exprStr, "\n", "\n\v"))
	}

	p.printlnBlock(leftAlignNested(pp.String()))
	p.decDepth()
}

func (p *printer) printlnBlock(s string) {
	for line := range strings.SplitSeq(s, "\n") {
		p.println(line)
	}
}

func (p *printer) visitAfterMatchSkipClause(n *googlesql.ASTAfterMatchSkipClause, _ printerData) {
	p.moveBefore(n)

	tt, _ := n.TargetType()
	switch tt {
	case googlesql.ASTAfterMatchSkipClauseEnums_AfterMatchSkipTargetTypePastLastRow:
		p.print(p.keyword("AFTER MATCH SKIP PAST LAST ROW"))
	case googlesql.ASTAfterMatchSkipClauseEnums_AfterMatchSkipTargetTypeToNextRow:
		p.print(p.keyword("AFTER MATCH SKIP TO NEXT ROW"))
	case googlesql.ASTAfterMatchSkipClauseEnums_AfterMatchSkipTargetTypeAfterMatchSkipTargetUnspecified:
		p.addError(&PrinterError{Msg: "unset AFTER MATCH SKIP target", Node: n, Input: &p.input})
	}

	p.movePast(n)
}

func (p *printer) visitRowPattern(n googlesql.ASTRowPatternExpressionNode, d printerData) {
	p.print(p.formatRowPattern(n, d))
}

func (p *printer) visitQuantifier(n googlesql.ASTQuantifierNode, d printerData) {
	p.print(p.formatQuantifier(n, d))
}

func (p *printer) visitQuantifierBound(n *googlesql.ASTQuantifierBound, d printerData) {
	if b, _ := n.Bound(); nodeDefined(b) {
		p.accept(b, d)
	}
}

func (p *printer) formatRowPattern(n googlesql.ASTRowPatternExpressionNode, d printerData) string {
	if !nodeDefined(n) {
		return ""
	}

	var body string

	switch pat := n.(type) {
	case *googlesql.ASTEmptyRowPattern:
		body = ""
	case *googlesql.ASTRowPatternVariable:
		name, _ := pat.Name()
		body = p.toTrimmedString(name, d)
	case *googlesql.ASTRowPatternAnchor:
		anc, _ := pat.Anchor()
		switch anc {
		case googlesql.ASTRowPatternAnchorEnums_AnchorStart:
			body = "^"
		case googlesql.ASTRowPatternAnchorEnums_AnchorEnd:
			body = "$"
		case googlesql.ASTRowPatternAnchorEnums_AnchorAnchorUnspecified:
			body = ""
		}
	case *googlesql.ASTRowPatternOperation:
		body = p.formatRowPatternOp(pat, d)
	case *googlesql.ASTRowPatternQuantification:
		op, _ := pat.Operand()
		q, _ := pat.Quantifier()
		body = p.formatRowPattern(op, d) + p.formatQuantifier(q, d)
	}

	if paren, _ := n.Parenthesized(); paren {
		return "(" + body + ")"
	}

	return body
}

func (p *printer) formatRowPatternOp(n *googlesql.ASTRowPatternOperation, d printerData) string {
	inputs := selectChildrenOfType[googlesql.ASTRowPatternExpressionNode](n)
	parts := make([]string, 0, len(inputs))

	for _, inp := range inputs {
		parts = append(parts, p.formatRowPattern(inp, d))
	}

	op, _ := n.OpType()
	switch op {
	case googlesql.ASTRowPatternOperationEnums_OperationTypeConcat:
		return strings.Join(parts, " ")
	case googlesql.ASTRowPatternOperationEnums_OperationTypeAlternate:
		return strings.Join(parts, " | ")
	case googlesql.ASTRowPatternOperationEnums_OperationTypePermute:
		return p.keyword("PERMUTE") + "(" + strings.Join(parts, ", ") + ")"
	case googlesql.ASTRowPatternOperationEnums_OperationTypeExclude:
		if len(parts) > 0 {
			return "{- " + parts[0] + " -}"
		}
	case googlesql.ASTRowPatternOperationEnums_OperationTypeOperationTypeUnspecified:
		return ""
	}

	return ""
}

func (p *printer) formatQuantifier(n googlesql.ASTQuantifierNode, d printerData) string {
	if !nodeDefined(n) {
		return ""
	}

	var s string

	switch q := n.(type) {
	case *googlesql.ASTSymbolQuantifier:
		sym, _ := q.Symbol()
		switch sym {
		case googlesql.ASTSymbolQuantifierEnums_SymbolQuestionMark:
			s = "?"
		case googlesql.ASTSymbolQuantifierEnums_SymbolPlus:
			s = "+"
		case googlesql.ASTSymbolQuantifierEnums_SymbolStar:
			s = "*"
		case googlesql.ASTSymbolQuantifierEnums_SymbolSymbolUnspecified:
			s = ""
		}
	case *googlesql.ASTFixedQuantifier:
		b, _ := q.Bound()
		s = "{" + p.toTrimmedString(b, d) + "}"
	case *googlesql.ASTBoundedQuantifier:
		lb, _ := q.LowerBound()
		ub, _ := q.UpperBound()
		lbStr := p.formatQuantifierBound(lb, d)
		ubStr := p.formatQuantifierBound(ub, d)

		switch {
		case lbStr != "" && ubStr != "":
			s = "{" + lbStr + ", " + ubStr + "}"
		case lbStr != "":
			s = "{" + lbStr + ",}"
		default:
			s = "{," + ubStr + "}"
		}
	}

	if rel, _ := n.IsReluctant(); rel {
		s += "?"
	}

	return s
}

func (p *printer) formatQuantifierBound(n *googlesql.ASTQuantifierBound, d printerData) string {
	if !nodeDefined(n) {
		return ""
	}

	b, _ := n.Bound()
	if !nodeDefined(b) {
		return ""
	}

	return p.toTrimmedString(b, d)
}
