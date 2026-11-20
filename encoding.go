package bqlang

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/goccy/go-googlesql"
)

// ErrNilASTNode is returned when NewASTNode is called with a nil AST node.
var ErrNilASTNode = errors.New("nil AST node")

// ASTNode exports a GoogleSQL AST subtree to a language-independent JSON structure.
type ASTNode struct {
	Kind                  string                 `json:"kind"`
	NumChildren           int                    `json:"num_children"`
	SingleNodeDebugString string                 `json:"single_node_debug_string"`
	DebugString           string                 `json:"debug_string"`
	IsTableExpression     bool                   `json:"is_table_expression,omitempty"`
	IsQueryExpression     bool                   `json:"is_query_expression,omitempty"`
	IsExpression          bool                   `json:"is_expression,omitempty"`
	IsType                bool                   `json:"is_type,omitempty"`
	IsLeaf                bool                   `json:"is_leaf,omitempty"`
	IsStatement           bool                   `json:"is_statement,omitempty"`
	IsScriptStatement     bool                   `json:"is_script_statement,omitempty"`
	IsLoopStatement       bool                   `json:"is_loop_statement,omitempty"`
	IsSQLStatement        bool                   `json:"is_sql_statement,omitempty"`
	IsDDLStatement        bool                   `json:"is_ddl_statement,omitempty"`
	IsCreateStatement     bool                   `json:"is_create_statement,omitempty"`
	IsAlterStatement      bool                   `json:"is_alter_statement,omitempty"`
	ParseLocationRange    *ASTParseLocationRange `json:"parse_location"`
	Location              string                 `json:"location"`
	Children              []*ASTNode             `json:"children"`
}

// ASTParseLocationRange represents a half-open byte span [Start, End).
type ASTParseLocationRange struct {
	Start *ASTParseLocationPoint `json:"start"`
	End   *ASTParseLocationPoint `json:"end"`
}

// ASTParseLocationPoint represents a 0-indexed byte offset in the input script.
type ASTParseLocationPoint struct {
	ByteOffset int `json:"byte_offset"`
}

// MarshalJSON serializes an ASTNode hierarchy into indented JSON.
func MarshalJSON(n googlesql.ASTNode) ([]byte, error) {
	if n == nil {
		return nil, nil
	}

	an, err := NewASTNode(n)
	if err != nil {
		return nil, fmt.Errorf("MarshalJSON: %w", err)
	}

	data, err := json.MarshalIndent(an, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("MarshalJSON: %w", err)
	}

	return data, nil
}

// NewASTNode converts a GoogleSQL ASTNode into an exportable ASTNode record.
func NewASTNode(n googlesql.ASTNode) (*ASTNode, error) {
	if n == nil {
		return nil, ErrNilASTNode
	}

	numChildren32, err := n.NumChildren()
	if err != nil {
		return nil, fmt.Errorf("NewASTNode: NumChildren: %w", err)
	}

	numChildren := int(numChildren32)
	children := make([]*ASTNode, 0, numChildren)

	for i := range numChildren32 {
		child, cerr := n.Child(i)
		if cerr != nil {
			return nil, fmt.Errorf("NewASTNode: Child(%d): %w", i, cerr)
		}

		childNode, cerr := NewASTNode(child)
		if cerr != nil {
			return nil, cerr
		}

		children = append(children, childNode)
	}

	an, err := populateNodeMetadata(n, numChildren, children)
	if err != nil {
		return nil, err
	}

	return an, nil
}

type debugStringer interface {
	DebugString(maxDepth int32) (string, error)
}

func populateNodeMetadata(n googlesql.ASTNode, numChildren int, children []*ASTNode) (*ASTNode, error) {
	kindStr, err := n.GetNodeKindString()
	if err != nil {
		return nil, fmt.Errorf("NewASTNode: GetNodeKindString: %w", err)
	}

	singleDebug, err := n.SingleNodeDebugString()
	if err != nil {
		return nil, fmt.Errorf("NewASTNode: SingleNodeDebugString: %w", err)
	}

	fullDebug := singleDebug

	if ds, ok := n.(debugStringer); ok {
		if d, derr := ds.DebugString(10000); derr == nil {
			fullDebug = d
		}
	}

	locRange, locStr, err := extractNodeLocation(n)
	if err != nil {
		return nil, err
	}

	isTableExpr, _ := n.IsTableExpression()
	isQueryExpr, _ := n.IsQueryExpression()
	isExpr, _ := n.IsExpression()
	isType, _ := n.IsType()
	isLeaf, _ := n.IsLeaf()
	isStmt, _ := n.IsStatement()
	isScriptStmt, _ := n.IsScriptStatement()
	isLoopStmt, _ := n.IsLoopStatement()
	isSQLStmt, _ := n.IsSqlStatement()
	isDDLStmt, _ := n.IsDdlStatement()
	isCreateStmt, _ := n.IsCreateStatement()
	isAlterStmt, _ := n.IsAlterStatement()

	return &ASTNode{
		Kind:                  kindStr,
		NumChildren:           numChildren,
		SingleNodeDebugString: singleDebug,
		DebugString:           fullDebug,
		IsTableExpression:     isTableExpr,
		IsQueryExpression:     isQueryExpr,
		IsExpression:          isExpr,
		IsType:                isType,
		IsLeaf:                isLeaf,
		IsStatement:           isStmt,
		IsScriptStatement:     isScriptStmt,
		IsLoopStatement:       isLoopStmt,
		IsSQLStatement:        isSQLStmt,
		IsDDLStatement:        isDDLStmt,
		IsCreateStatement:     isCreateStmt,
		IsAlterStatement:      isAlterStmt,
		ParseLocationRange:    locRange,
		Location:              locStr,
		Children:              children,
	}, nil
}

func extractNodeLocation(n googlesql.ASTNode) (*ASTParseLocationRange, string, error) {
	loc, err := n.Location()
	if err != nil {
		return nil, "", fmt.Errorf("NewASTNode: Location: %w", err)
	}

	startPt, err := loc.Start()
	if err != nil {
		return nil, "", fmt.Errorf("NewASTNode: Start: %w", err)
	}

	endPt, err := loc.End()
	if err != nil {
		return nil, "", fmt.Errorf("NewASTNode: End: %w", err)
	}

	startOff, err := startPt.GetByteOffset()
	if err != nil {
		return nil, "", fmt.Errorf("NewASTNode: Start.GetByteOffset: %w", err)
	}

	endOff, err := endPt.GetByteOffset()
	if err != nil {
		return nil, "", fmt.Errorf("NewASTNode: End.GetByteOffset: %w", err)
	}

	locStr, err := n.GetLocationString()
	if err != nil {
		return nil, "", fmt.Errorf("NewASTNode: GetLocationString: %w", err)
	}

	return &ASTParseLocationRange{
		Start: &ASTParseLocationPoint{ByteOffset: int(startOff)},
		End:   &ASTParseLocationPoint{ByteOffset: int(endOff)},
	}, locStr, nil
}
