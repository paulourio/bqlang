package ast

import "fmt"

// Kind identifies the syntactic category of an extension AST node.
type Kind int

// Extension AST node kinds.
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
