package ast

// NodeHandler defines the tree and byte-location interface implemented by all extension AST nodes.
type NodeHandler interface {
	Kind() Kind
	SetKind(Kind)

	GetLoc() Loc
	StartLoc() int
	EndLoc() int
	SetStartLoc(int)
	SetEndLoc(int)
	// ExpandLoc updates the parsing location expanding when the given
	// range is outside the current range.
	ExpandLoc(start, end int)

	// Tree handlers.
	Parent() NodeHandler
	SetParent(NodeHandler)
	Children() []NodeHandler
	// AddChild adds child to the list of children. The child element
	// must not be nil.
	AddChild(NodeHandler)
	// AddChildren adds all non-nil nodes to the list of children.
	AddChildren([]NodeHandler)

	// Stringers.
	String() string
	// DebugString returns a multi-line tree dump with byte offset ranges.
	DebugString(sql string) string
	// SingleNodeDebugString returns a one-line description of the node without children.
	SingleNodeDebugString() string
}

// LeafHandler represents a terminal AST node carrying raw source text.
type LeafHandler interface {
	NodeHandler

	Image() string
	SetImage(string)
}

// Node provides the base tree links and source location embedded by concrete AST nodes.
type Node struct {
	// Loc holds the parser location.
	Loc

	kind     Kind
	children []NodeHandler
	parent   NodeHandler
}

// Loc represents a half-open byte range [Start, End) in the input script.
type Loc struct {
	Start int
	End   int
}

// CommentNode represents a SQL or Jinja2 comment token in the extension AST.
type CommentNode struct {
	Node
	image string
}

// NewLineNode represents a line break token in the extension AST.
type NewLineNode struct {
	Node
	image string
}

// ScriptNode represents the root sequence of comments and template elements.
type ScriptNode struct {
	Node
}

// TemplateBlockNode represents a Jinja2 block tag sequence ({% ... %}).
type TemplateBlockNode struct {
	Node
	image string
}

// TemplateCommentNode represents a Jinja2 comment ({# ... #}).
type TemplateCommentNode struct {
	Node
	image string
}

// TemplateVariableNode represents a Jinja2 expression tag ({{ ... }}).
type TemplateVariableNode struct {
	Node
	image string
}

// TemplateStatementNode represents a single Jinja2 statement clause inside a block.
type TemplateStatementNode struct {
	Node
	image string
}

// Image returns the raw source text of the comment node.
func (n *CommentNode) Image() string {
	return n.image
}

// SetImage sets the raw source text of the comment node.
func (n *CommentNode) SetImage(i string) {
	n.image = i
}

// Image returns the raw source text of the newline node.
func (n *NewLineNode) Image() string {
	return n.image
}

// SetImage sets the raw source text of the newline node.
func (n *NewLineNode) SetImage(i string) {
	n.image = i
}

// Image returns the raw source text of the template block node.
func (n *TemplateBlockNode) Image() string {
	return n.image
}

// SetImage sets the raw source text of the template block node.
func (n *TemplateBlockNode) SetImage(i string) {
	n.image = i
}

// Image returns the raw source text of the template comment node.
func (n *TemplateCommentNode) Image() string {
	return n.image
}

// SetImage sets the raw source text of the template comment node.
func (n *TemplateCommentNode) SetImage(i string) {
	n.image = i
}

// Image returns the raw source text of the template variable node.
func (n *TemplateVariableNode) Image() string {
	return n.image
}

// SetImage sets the raw source text of the template variable node.
func (n *TemplateVariableNode) SetImage(i string) {
	n.image = i
}

// Image returns the raw source text of the template statement node.
func (n *TemplateStatementNode) Image() string {
	return n.image
}

// SetImage sets the raw source text of the template statement node.
func (n *TemplateStatementNode) SetImage(i string) {
	n.image = i
}

// AddChild appends c to n's children and expands n's byte span.
func (n *Node) AddChild(c NodeHandler) {
	n.children = append(n.children, c)
	c.SetParent(n)
	n.ExpandLoc(c.StartLoc(), c.EndLoc())
}

// AddChildren appends all non-nil elements of children to n.
func (n *Node) AddChildren(children []NodeHandler) {
	for _, c := range children {
		if c != nil {
			n.AddChild(c)
		}
	}
}

// StartLoc returns the 0-based inclusive start byte offset.
func (l *Loc) StartLoc() int { return l.Start }

// SetStartLoc sets the 0-based inclusive start byte offset.
func (l *Loc) SetStartLoc(pos int) { l.Start = pos }

// EndLoc returns the 0-based exclusive end byte offset.
func (l *Loc) EndLoc() int { return l.End }

// SetEndLoc sets the 0-based exclusive end byte offset.
func (l *Loc) SetEndLoc(pos int) { l.End = pos }

// GetLoc returns the node's byte location range.
func (n *Node) GetLoc() Loc { return n.Loc }

// Kind returns the node's syntactic kind.
func (n *Node) Kind() Kind { return n.kind }

// SetKind sets the node's syntactic kind.
func (n *Node) SetKind(k Kind) { n.kind = k }

// Parent returns the node's parent in the AST, or nil for the root.
func (n *Node) Parent() NodeHandler { return n.parent }

// SetParent sets the node's parent reference.
func (n *Node) SetParent(p NodeHandler) { n.parent = p }

// Children returns the node's child slice.
func (n *Node) Children() []NodeHandler { return n.children }

// ExpandLoc widens the node's [Start, End) span to cover [start, end).
func (n *Node) ExpandLoc(start, end int) {
	s := n.StartLoc()
	e := n.EndLoc()

	if s == 0 && e == 0 {
		n.SetStartLoc(start)
		n.SetEndLoc(end)
	} else {
		if s > start {
			n.SetStartLoc(start)
		}

		if e < end {
			n.SetEndLoc(end)
		}
	}
}

// SingleNodeDebugString returns the node kind name.
func (n *Node) SingleNodeDebugString() string {
	return n.kind.String()
}

// DebugString formats the subtree rooted at n as an indented multi-line string.
func (n *Node) DebugString(sql string) string {
	d := newDumper(n, "\n", 256, sql)
	d.Dump()

	return d.String()
}

// String returns SingleNodeDebugString.
func (n *Node) String() string {
	return n.SingleNodeDebugString()
}

// NewComment allocates an initialized CommentNode.
func NewComment() (*CommentNode, error) {
	c := &CommentNode{}
	c.SetKind(Comment)

	return c, nil
}

// NewNewLine allocates an initialized NewLineNode.
func NewNewLine() (*NewLineNode, error) {
	c := &NewLineNode{}
	c.SetKind(NewLine)

	return c, nil
}

// NewScript allocates an initialized ScriptNode.
func NewScript() (*ScriptNode, error) {
	n := &ScriptNode{}
	n.SetKind(Script)

	return n, nil
}

// NewTemplateBlock allocates an initialized TemplateBlockNode.
func NewTemplateBlock() (*TemplateBlockNode, error) {
	n := &TemplateBlockNode{}
	n.SetKind(TemplateBlock)

	return n, nil
}

// NewTemplateComment allocates an initialized TemplateCommentNode.
func NewTemplateComment() (*TemplateCommentNode, error) {
	n := &TemplateCommentNode{}
	n.SetKind(TemplateComment)

	return n, nil
}

// NewTemplateVariable allocates an initialized TemplateVariableNode.
func NewTemplateVariable() (*TemplateVariableNode, error) {
	n := &TemplateVariableNode{}
	n.SetKind(TemplateVariable)

	return n, nil
}

// NewTemplateStatement allocates an initialized TemplateStatementNode.
func NewTemplateStatement() (*TemplateStatementNode, error) {
	n := &TemplateStatementNode{}
	n.SetKind(TemplateStatement)

	return n, nil
}
