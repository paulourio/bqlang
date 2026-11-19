package extensions

import (
	"fmt"
	"strings"

	"github.com/paulourio/bqlang/extensions/ast"
	"github.com/paulourio/bqlang/extensions/errors"
	"github.com/paulourio/bqlang/extensions/lexer"
	"github.com/paulourio/bqlang/extensions/parser"
)

// Comment represents an extracted SQL or Jinja2 comment and its line-context flags.
type Comment struct {
	// Start is the bytes offset location the comment starts in the input.
	Start int
	// End is the bytes offset location the comment ends in the input.
	End int
	// Image contains the literal parsed comment token, with symbols included.
	Image string
	// Kind specifies the comment type.
	Kind CommentKind

	multiline   bool
	atLineBegin bool
	atLineEnd   bool
	mustEndLine bool
}

// CommentKind classifies comment delimiter syntax.
type CommentKind int

// Supported SQL and Jinja2 comment delimiter kinds.
const (
	SlashStar     CommentKind = iota + 1 // "/* ... */"
	DoubleSlash                          // "// ...\n"
	DoubleDash                           // "-- ...\n"
	Pound                                // "# ...\n"
	Jinja2Comment                        // "{# ... #}"
)

func (k CommentKind) String() string {
	switch k {
	case SlashStar:
		return "SLASH_STAR"
	case DoubleSlash:
		return "DOUBLE_SLASH"
	case DoubleDash:
		return "DOUBLE_DASH"
	case Pound:
		return "POUND"
	case Jinja2Comment:
		return "JINJA2_COMMENT"
	}

	panic(fmt.Sprintf("unexpected comment kind: %d", int(k)))
}

// ExtractAll parses input once and extracts both top-level comments and
// top-level Jinja2 template elements.
func ExtractAll(input string) ([]*Comment, []*TemplateElement, error) {
	n, ok, err := parseExtensionAST(input)
	if err != nil {
		return nil, nil, fmt.Errorf("ExtractAll: %w", err)
	}

	if !ok {
		return nil, nil, nil
	}

	comms, err := extractCommentsFromNode(n, input)
	if err != nil {
		return comms, nil, fmt.Errorf("ExtractAll: %w", err)
	}

	elems, err := extractTemplatesFromNode(n, input)
	if err != nil {
		return comms, elems, fmt.Errorf("ExtractAll: %w", err)
	}

	return comms, elems, nil
}

// ExtractComments parses input and extracts all top-level SQL and Jinja2 comments.
func ExtractComments(input string) ([]*Comment, error) {
	n, ok, err := parseExtensionAST(input)
	if err != nil {
		return nil, fmt.Errorf("ExtractComments: %w", err)
	}

	if !ok {
		return nil, nil
	}

	comms, err := extractCommentsFromNode(n, input)
	if err != nil {
		return comms, fmt.Errorf("ExtractComments: %w", err)
	}

	return comms, nil
}

func parseExtensionAST(input string) (ast.NodeHandler, bool, error) {
	if strings.TrimSpace(input) == "" {
		return nil, false, nil
	}

	l := lexer.NewLexer([]byte(input))
	p := parser.NewParser()

	s, err := p.Parse(l)
	if err != nil {
		msg := errors.FormatError(err, input)

		return nil, false, fmt.Errorf("%w: %s", ErrParseExtension, msg)
	}

	n, ok := s.(ast.NodeHandler)
	if !ok {
		return nil, false, nil
	}

	return n, true, nil
}

func extractCommentsFromNode(n ast.NodeHandler, input string) ([]*Comment, error) {
	children := n.Children()
	r := make([]*Comment, 0, len(children))

	for _, child := range children {
		switch c := child.(type) {
		case *ast.CommentNode:
			nc, err := NewComment(c.StartLoc(), c.EndLoc(), c.Image(), input)
			if err != nil {
				return r, err
			}

			r = append(r, nc)
		case *ast.TemplateCommentNode:
			a, b := c.StartLoc(), c.EndLoc()

			nc, err := NewComment(a, b, input[a:b], input)
			if err != nil {
				return r, err
			}

			r = append(r, nc)
		}
	}

	return r, nil
}

// EraseComments returns script without comments without changing any of token
// positions. Comments are replaced by whitespace.
func EraseComments(script string, comments []*Comment) string {
	buf := []byte(script)

	for _, c := range comments {
		start := c.Start
		end := c.End

		if start < 0 {
			start = 0
		}

		if end > len(buf) {
			end = len(buf)
		}

		for i := start; i < end; i++ {
			if buf[i] != '\n' {
				buf[i] = ' '
			}
		}
	}

	return string(buf)
}

// SplitComments separates the comments into two parts, where the left-hand side
// slice contains all elements with start pos less or equal than the
// requested pos, and the right-hand side slice contains the remaining
// elements.
func SplitComments(comments []*Comment, pos int) ([]*Comment, []*Comment) {
	i := 0

	for _, c := range comments {
		if c.Start > pos {
			break
		}

		i++
	}

	if i == 0 {
		return nil, comments
	}

	if i == len(comments) {
		return comments, nil
	}

	return comments[:i], comments[i:]
}

// NewComment returns an initialized comment. The input is necessary
// to extract information about its surroundings.
func NewComment(start, end int, img, input string) (*Comment, error) {
	c := &Comment{
		Start: start,
		End:   end,
		Image: img,
	}

	if err := c.update(input); err != nil {
		return nil, fmt.Errorf("NewComment: %w", err)
	}

	return c, nil
}

// AtLineBegin reports whether only whitespace precedes the comment on its line.
func (c *Comment) AtLineBegin() bool {
	return c.atLineBegin
}

// AtLineEnd reports whether only whitespace follows the comment on its line.
func (c *Comment) AtLineEnd() bool {
	return c.atLineEnd
}

// MustEndLine reports whether the comment syntax requires a trailing line break.
func (c *Comment) MustEndLine() bool {
	return c.mustEndLine
}

// EndsWithNewline reports whether c.Image terminates with a newline byte.
func (c *Comment) EndsWithNewline() bool {
	return len(c.Image) > 0 && c.Image[len(c.Image)-1] == '\n'
}

// IsOneline returns whether the comment occupies its own single line.
func (c *Comment) IsOneline() bool {
	return c.AtLineBegin() && c.AtLineEnd() && !c.IsMultiline()
}

// IsMultiline reports whether the comment spans multiple lines.
func (c *Comment) IsMultiline() bool {
	return c.multiline
}

func (c *Comment) String() string {
	return fmt.Sprintf("Comment[Start=%d]", c.Start)
}

func (c *Comment) update(input string) error {
	if err := c.updateKind(); err != nil {
		return err
	}

	return c.updateType(input)
}

func (c *Comment) updateKind() error {
	if len(c.Image) == 0 {
		return ErrInvalidComment
	}

	if c.Image[0] == '#' {
		c.Kind = Pound
		c.mustEndLine = true

		return nil
	}

	if len(c.Image) < 2 {
		return ErrInvalidComment
	}

	switch c.Image[:2] {
	case "//":
		c.Kind = DoubleSlash
		c.mustEndLine = true
	case "--":
		c.Kind = DoubleDash
		c.mustEndLine = true
	case "/*":
		c.Kind = SlashStar
		c.mustEndLine = false
	case "{#":
		c.Kind = Jinja2Comment
		c.mustEndLine = false
	default:
		return ErrInvalidComment
	}

	return nil
}

func (c *Comment) updateType(input string) error {
	c.multiline = c.containsLineBreaks()
	c.atLineBegin = c.beginsLine(input)
	c.atLineEnd = c.endsLine(input)

	return nil
}

func (c *Comment) containsLineBreaks() bool {
	if c.Kind == SlashStar || c.Kind == Jinja2Comment {
		return strings.Contains(c.Image, "\n")
	}

	return false
}

// beginsLine scans the bytes before the comment until a line break or
// the start of the input to check whether the comment starts a line.
func (c *Comment) beginsLine(input string) bool {
	i := min(c.Start, len(input))

	for i > 0 {
		if input[i-1] == '\n' {
			break
		}

		i--
	}

	return len(strings.TrimSpace(input[i:c.Start])) == 0
}

// endsLine scans the bytes after the comment until a line break or
// the end of the input to check whether the comment ends a line.
func (c *Comment) endsLine(input string) bool {
	switch c.Kind {
	case DoubleSlash, DoubleDash, Pound:
		return true
	case SlashStar, Jinja2Comment:
		i := c.End
		n := len(input)

		if i > n {
			i = n
		}

		for i < n {
			if input[i] == '\n' {
				break
			}

			i++
		}

		return len(strings.TrimSpace(input[c.End:i])) == 0
	}

	panic(fmt.Sprintf("unknown comment kind: %d", int(c.Kind)))
}
