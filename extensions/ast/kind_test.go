package ast_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/paulourio/bqlang/extensions/ast"
)

func TestKindConstantsAreTyped(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Unknown", ast.UnknownKind.String())
	assert.Equal(t, "Comment", ast.Comment.String())
	assert.Equal(t, "Script", ast.Script.String())
	assert.Equal(t, "TemplateComment", ast.TemplateComment.String())
	assert.Equal(t, "TemplateForBlock", ast.TemplateForBlock.String())
	assert.Equal(t, "TemplateIfBlock", ast.TemplateIfBlock.String())
	assert.Equal(t, "TemplateSetBlock", ast.TemplateSetBlock.String())
	assert.Equal(t, "TemplateVariable", ast.TemplateVariable.String())
}
