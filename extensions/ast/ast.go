package ast

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
	ExpandLoc(start int, end int)

	// Tree handlers.
	Parent() NodeHandler
	SetParent(NodeHandler)
	Children() []NodeHandler
	// AddChild adds child to the list of children.  The child element
	// must not be nil.
	AddChild(NodeHandler)
	// AddChildren all nodes to the list of children.  Elements that
	// are nil are ignored.
	AddChildren([]NodeHandler)

	// Stringers.
	String() string
	// DebugString returns a multi-line tree dump.  Parse locations
	// are represented as integer ranges.  When sql is passed, fragments
	// of the original sql are used instead of raw integer offsets.
	DebugString(sql string) string
	// SingleNodeDebugString returns an one-line description of the
	// node, including modifiers but without child nodes.
	SingleNodeDebugString() string
}

type LeafHandler interface {
	NodeHandler

	Image() string
	SetImage(string)
}

type Node struct {
	// Loc holds the parser location.
	Loc

	kind     Kind
	children []NodeHandler
	parent   NodeHandler
}

type Loc struct {
	Start int
	End   int
}

type CommentNode struct {
	Node
	image string
}

type NewLineNode struct {
	Node
	image string
}

type ScriptNode struct {
	Node
}

type TemplateBlockNode struct {
	Node
	image string
}

type TemplateCommentNode struct {
	Node
	image string
}

type TemplateVariableNode struct {
	Node
	image string
}

type TemplateStatementNode struct {
	Node
	image string
}

func (n *CommentNode) Image() string {
	return n.image
}

func (n *CommentNode) SetImage(i string) {
	n.image = i
}

func (n *NewLineNode) Image() string {
	return n.image
}

func (n *NewLineNode) SetImage(i string) {
	n.image = i
}

func (n *TemplateBlockNode) Image() string {
	return n.image
}

func (n *TemplateBlockNode) SetImage(i string) {
	n.image = i
}

func (n *TemplateCommentNode) Image() string {
	return n.image
}

func (n *TemplateCommentNode) SetImage(i string) {
	n.image = i
}

func (n *TemplateVariableNode) Image() string {
	return n.image
}

func (n *TemplateVariableNode) SetImage(i string) {
	n.image = i
}

func (n *TemplateStatementNode) Image() string {
	return n.image
}

func (n *TemplateStatementNode) SetImage(i string) {
	n.image = i
}

func (n *Node) AddChild(c NodeHandler) {
	n.children = append(n.children, c)
	c.SetParent(n)
	n.ExpandLoc(c.StartLoc(), c.EndLoc())
}

func (n *Node) AddChildren(children []NodeHandler) {
	for _, c := range children {
		if c != nil {
			n.AddChild(c)
		}
	}
}

func (l *Loc) StartLoc() int       { return l.Start }
func (l *Loc) SetStartLoc(pos int) { l.Start = pos }
func (l *Loc) EndLoc() int         { return l.End }
func (l *Loc) SetEndLoc(pos int)   { l.End = pos }

func (n *Node) GetLoc() Loc             { return n.Loc }
func (n *Node) Kind() Kind              { return n.kind }
func (n *Node) SetKind(k Kind)          { n.kind = k }
func (n *Node) Parent() NodeHandler     { return n.parent }
func (n *Node) SetParent(p NodeHandler) { n.parent = p }
func (n *Node) Children() []NodeHandler { return n.children }

func (n *Node) ExpandLoc(start int, end int) {
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

func (n *Node) SingleNodeDebugString() string {
	return n.kind.String()
}

func (n *Node) DebugString(sql string) string {
	d := newDumper(n, "\n", 256, sql)
	d.Dump()

	return d.String()
}

func (n *Node) String() string {
	return n.SingleNodeDebugString()
}

func NewComment() (*CommentNode, error) {
	c := &CommentNode{}
	c.SetKind(Comment)

	return c, nil
}

func NewNewLine() (*NewLineNode, error) {
	c := &NewLineNode{}
	c.SetKind(NewLine)

	return c, nil
}

func NewScript() (*ScriptNode, error) {
	n := &ScriptNode{}
	n.SetKind(Script)

	return n, nil
}

func NewTemplateBlock() (*TemplateBlockNode, error) {
	n := &TemplateBlockNode{}
	n.SetKind(TemplateBlock)

	return n, nil
}

func NewTemplateComment() (*TemplateCommentNode, error) {
	n := &TemplateCommentNode{}
	n.SetKind(TemplateComment)

	return n, nil
}

func NewTemplateVariable() (*TemplateVariableNode, error) {
	n := &TemplateVariableNode{}
	n.SetKind(TemplateVariable)

	return n, nil
}

func NewTemplateStatement() (*TemplateStatementNode, error) {
	n := &TemplateStatementNode{}
	n.SetKind(TemplateStatement)

	return n, nil
}
