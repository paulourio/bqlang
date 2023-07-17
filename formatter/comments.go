package formatter

import (
	"regexp"
	"strings"

	"github.com/paulourio/bqlang/extensions"
)

// CommentsQueue is a unique queue of comments to be used during the
// format process.  We cannot handle slices directly because they
// would not be updated on all nesting levels at the same time.
type CommentsQueue struct {
	comments []*extensions.Comment
}

// alignTrailingComments performs an additional pass on the formatted
// output to align trailing comments.  Current implementation is naive
// using some simple regular expressions.
func alignTrailingComments(input string) string {
	inputLines := strings.Split(input, "\n")
	lines := make([]string, len(inputLines))
	match := false

	for i, line := range inputLines {
		m := trailingComment.FindStringSubmatchIndex(line)
		if m != nil && strings.TrimSpace(line[m[2]:m[3]]) != "" {
			match = true
			pos := m[4]

			lines[i] = line[:pos] + "\v" + line[pos:]
		} else {
			lines[i] = line
		}
	}

	if !match {
		return input
	}

	rebuilt := strings.Join(lines, "\n")

	return leftAlignNested(rebuilt)
}

// trailingComment is designed to apply to a single line without the
// final line break.  It matches the part before a comment and the part
// after a trailing comment.
var trailingComment = regexp.MustCompile(`^(.*)((--|//|#)[^\n]+)$`)
