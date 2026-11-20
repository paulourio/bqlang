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

// Leaf provides the base tree links and raw source text embedded by terminal AST nodes.
type Leaf struct {
	Node
	image string
}

// Image returns the raw source text of the leaf node.
func (l *Leaf) Image() string {
	return l.image
}

// SetImage sets the raw source text of the leaf node.
func (l *Leaf) SetImage(i string) {
	l.image = i
}

// CommentNode represents a SQL or Jinja2 comment token in the extension AST.
type CommentNode struct {
	Leaf
}

// ScriptNode represents the root sequence of comments and template elements.
type ScriptNode struct {
	Node
}

// TemplateBlockNode represents a Jinja2 block tag sequence ({% ... %}).
type TemplateBlockNode struct {
	Leaf
}

// TemplateCommentNode represents a Jinja2 comment ({# ... #}).
type TemplateCommentNode struct {
	Leaf
}

// TemplateVariableNode represents a Jinja2 expression tag ({{ ... }}).
type TemplateVariableNode struct {
	Leaf
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
	if n.Start == 0 && n.End == 0 {
		n.Start = start
		n.End = end

		return
	}

	n.Start = min(n.Start, start)
	n.End = max(n.End, end)
}

// SingleNodeDebugString returns the node kind name.
func (n *Node) SingleNodeDebugString() string {
	return n.kind.String()
}

// DebugString formats the subtree rooted at n as an indented multi-line string.
func (n *Node) DebugString(_ string) string {
	d := newDumper("\n", 256)
	d.dump(n, 0)

	return d.String()
}

// String returns SingleNodeDebugString.
func (n *Node) String() string {
	return n.SingleNodeDebugString()
}

// NewComment allocates an initialized CommentNode.
func NewComment() *CommentNode {
	c := &CommentNode{}
	c.SetKind(Comment)

	return c
}

// NewScript allocates an initialized ScriptNode.
func NewScript() *ScriptNode {
	n := &ScriptNode{}
	n.SetKind(Script)

	return n
}

// NewTemplateBlock allocates an initialized TemplateBlockNode of kind k.
func NewTemplateBlock(k Kind) *TemplateBlockNode {
	n := &TemplateBlockNode{}
	n.SetKind(k)

	return n
}

// NewTemplateComment allocates an initialized TemplateCommentNode.
func NewTemplateComment() *TemplateCommentNode {
	n := &TemplateCommentNode{}
	n.SetKind(TemplateComment)

	return n
}

// NewTemplateVariable allocates an initialized TemplateVariableNode.
func NewTemplateVariable() *TemplateVariableNode {
	n := &TemplateVariableNode{}
	n.SetKind(TemplateVariable)

	return n
}
