package ast

import "fmt"

type Kind int

const (
	UnknownKind = iota
	Comment
	NewLine
	Script
	TemplateBlock
	TemplateComment
	TemplateForBlock
	TemplateIfBlock
	TemplateSetBlock
	TemplateStatement
	TemplateVariable
)

func (k Kind) String() string {
	switch k {
	case UnknownKind:
		return "Unknown"
	case Comment:
		return "Comment"
	case NewLine:
		return "NewLine"
	case Script:
		return "Script"
	case TemplateBlock:
		return "TemplateBlock"
	case TemplateComment:
		return "TemplateComment"
	case TemplateForBlock:
		return "TemplateForBlock"
	case TemplateIfBlock:
		return "TemplateIfBlock"
	case TemplateSetBlock:
		return "TemplateSetBlock"
	case TemplateStatement:
		return "TemplateStatement"
	case TemplateVariable:
		return "TemplateVariable"
	}

	panic(fmt.Sprint("unexpected kind ", int(k)))
}
