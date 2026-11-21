package ast

import "fmt"

// Kind identifies the syntactic category of an extension AST node.
type Kind int

// Extension AST node kinds.
const (
	UnknownKind Kind = iota
	Comment
	Script
	TemplateComment
	TemplateForBlock
	TemplateIfBlock
	TemplateSetBlock
	TemplateMacroBlock
	TemplateCallBlock
	TemplateDoBlock
	TemplateRawBlock
	TemplateVariable
)

func (k Kind) String() string {
	switch k {
	case UnknownKind:
		return "Unknown"
	case Comment:
		return "Comment"
	case Script:
		return "Script"
	case TemplateComment:
		return "TemplateComment"
	case TemplateForBlock:
		return "TemplateForBlock"
	case TemplateIfBlock:
		return "TemplateIfBlock"
	case TemplateSetBlock:
		return "TemplateSetBlock"
	case TemplateMacroBlock:
		return "TemplateMacroBlock"
	case TemplateCallBlock:
		return "TemplateCallBlock"
	case TemplateDoBlock:
		return "TemplateDoBlock"
	case TemplateRawBlock:
		return "TemplateRawBlock"
	case TemplateVariable:
		return "TemplateVariable"
	}

	panic(fmt.Sprint("unexpected kind ", int(k)))
}
