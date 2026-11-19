package formatter

import (
	"regexp"
	"strings"

	"github.com/paulourio/bqlang/extensions"
)

// commentsQueue is a shared queue of comments consumed across all nested
// printers during formatting.
type commentsQueue struct {
	comments []*extensions.Comment
}

// alignTrailingComments performs a post-pass on the formatted output to align
// trailing line comments.
func alignTrailingComments(input string) string {
	if !strings.Contains(input, "--") &&
		!strings.Contains(input, "//") &&
		!strings.Contains(input, "#") {
		return input
	}

	trailingComment := regexp.MustCompile(`^(.*)((--|//|#)[^\n]+)$`)
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

	return leftAlignNested(strings.Join(lines, "\n"))
}
