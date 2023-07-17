package extensions

import (
	"fmt"
	"strings"

	"github.com/paulourio/bqlang/extensions/ast"
	"github.com/paulourio/bqlang/extensions/errors"
	"github.com/paulourio/bqlang/extensions/lexer"
	"github.com/paulourio/bqlang/extensions/parser"
)

type Comment struct {
	// Start is the bytes offset location the comment starts in the
	// input.
	Start int
	// End is the bytes offset location the comment ends in the input.
	End int
	// Image contains the literal parsed comment token, with symbols
	// included.
	Image string
	// Kind specifies the comment type.
	Kind CommentKind

	multiline   bool
	atLineBegin bool
	atLineEnd   bool
	mustEndLine bool
}

type CommentKind int

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
	}

	panic("unexpected kind")
}

func ExtractComments(input string) ([]*Comment, error) {
	if strings.TrimSpace(input) == "" {
		return nil, nil
	}

	l := lexer.NewLexer([]byte(input))
	p := parser.NewParser()

	s, err := p.Parse(l)
	if err != nil {
		msg := errors.FormatError(err, input)

		return nil, fmt.Errorf("ExtractComments: %s", msg)
	}

	if n, ok := s.(ast.NodeHandler); ok {
		children := n.Children()
		r := make([]*Comment, 0, len(children))

		for _, child := range children {
			switch c := child.(type) {
			case *ast.CommentNode:
				nc, err := NewComment(c.StartLoc(), c.EndLoc(), c.Image(), input)
				if err != nil {
					return r, fmt.Errorf("ExtractComments: %w", err)
				}

				r = append(r, nc)
			case *ast.TemplateCommentNode:
				a, b := c.StartLoc(), c.EndLoc()

				nc, err := NewComment(a, b, input[a:b], input)
				if err != nil {
					return r, fmt.Errorf("ExtractComments: %w", err)
				}

				r = append(r, nc)
			default:
				// fmt.Printf("ExtractComments: Skipping %#v\n", child)
			}
		}

		return r, nil
	}

	return nil, nil
}

// EraseComments returns script without comments without changing any of token
// positions.  Comments are replaced by whitespace.
func EraseComments(script string, comments []*Comment) string {
	buf := []byte(script)

	for _, c := range comments {
		for i := c.Start; i < c.End; i++ {
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

// NewComment returns an initialized comment.  The input is necessary
// to extract information about its surroundings.
func NewComment(start int, end int, img string, input string) (*Comment, error) {
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

func (c *Comment) AtLineBegin() bool {
	return c.atLineBegin
}

func (c *Comment) AtLineEnd() bool {
	return c.atLineEnd
}

func (c *Comment) MustEndLine() bool {
	return c.mustEndLine
}

func (c *Comment) EndsWithNewline() bool {
	return c.Image[len(c.Image)-1] == '\n'
}

// IsOneline returns whether the comment appears on the same line as a
// statement.
func (c *Comment) IsOneline() bool {
	return c.AtLineBegin() && c.AtLineEnd() && !c.IsMultiline()
}

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
	if c.Kind == SlashStar {
		return strings.Contains(c.Image, "\n")
	}

	return false
}

// beginsLine scans the bytes before the comment until a line break or
// the start of the input to check whether the comment is starting
// a new line.
func (c *Comment) beginsLine(input string) bool {
	i := c.Start

	for i > 0 {
		if input[i] == '\n' {
			break
		}

		i--
	}

	return len(strings.TrimSpace(input[i:c.Start])) == 0
}

// endsLine scans the bytes after the comment until a line break or
// the end of the input to check whether the comment is ending a line.
func (c *Comment) endsLine(input string) bool {
	switch c.Kind {
	case DoubleSlash, DoubleDash, Pound:
		return true
	case SlashStar, Jinja2Comment:
		i := c.End
		n := len(input)

		for i < n {
			if input[i] == '\n' {
				break
			}

			i++
		}

		return len(strings.TrimSpace(input[c.End:i])) == 0
	}

	panic("unknown comment kind")
}
