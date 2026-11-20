package formatter

import (
	"strings"

	"github.com/paulourio/bqlang/extensions"
)

// commentsQueue is a shared queue of comments consumed across all nested
// printers during formatting.
type commentsQueue struct {
	comments []*extensions.Comment
}

// alignTrailingComments performs a post-pass on the formatted output to align
// trailing line comments marked during comment flushing.
func alignTrailingComments(input string) string {
	if !strings.Contains(input, trailingCommentPlaceholder) {
		return input
	}

	lines := strings.Split(input, "\n")
	match := false

	for i, line := range lines {
		before, after, ok := strings.Cut(line, trailingCommentPlaceholder)
		if !ok {
			continue
		}

		if strings.TrimSpace(before) != "" {
			match = true
			lines[i] = before + "\v" + after
		} else {
			lines[i] = before + after
		}
	}

	joined := strings.Join(lines, "\n")
	if !match {
		return joined
	}

	return leftAlignNested(joined)
}
