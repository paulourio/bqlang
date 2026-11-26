package analysis

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveOverlapsPreservesSplitTails(t *testing.T) {
	t.Parallel()

	in := []ByteSemanticToken{
		{Start: 0, End: 10, Type: TokenString, Priority: priorityScanner},
		{Start: 3, End: 7, Type: TokenVariable, Priority: priorityAST},
		{Start: 5, End: 8, Type: TokenProperty, Modifiers: ModPartitioned, Priority: priorityResolved},
	}

	got := resolveOverlaps(in)
	want := []ByteSemanticToken{
		{Start: 0, End: 3, Type: TokenString, Priority: priorityScanner},
		{Start: 3, End: 5, Type: TokenVariable, Priority: priorityAST},
		{Start: 5, End: 8, Type: TokenProperty, Modifiers: ModPartitioned, Priority: priorityResolved},
		{Start: 8, End: 10, Type: TokenString, Priority: priorityScanner},
	}

	assert.Equal(t, want, got)
}
