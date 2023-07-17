package bqlang

import (
	"encoding/json"

	"github.com/goccy/go-zetasql/ast"
)

// ASTNode is a temporary structure to allow exporting an AST to a
// language independent format.
type ASTNode struct {
	ID                    int                    `json:"id"`
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

type ASTParseLocationRange struct {
	Start *ASTParseLocationPoint `json:"start"`
	End   *ASTParseLocationPoint `json:"end"`
}

type ASTParseLocationPoint struct {
	ByteOffset int `json:"byte_offset"`
}

func MarshalJSON(n ast.Node) ([]byte, error) {
	if n == nil {
		return nil, nil
	}

	an := NewASTNode(n)

	return json.MarshalIndent(an, "", "  ")
}

func NewASTNode(n ast.Node) *ASTNode {
	children := make([]*ASTNode, 0, n.NumChildren())
	for i := 0; i < n.NumChildren(); i++ {
		children = append(children, NewASTNode(n.Child(i)))
	}

	loc := n.ParseLocationRange()
	an := ASTNode{
		ID:                    n.ID(),
		Kind:                  n.Kind().String(),
		NumChildren:           n.NumChildren(),
		SingleNodeDebugString: n.SingleNodeDebugString(),
		DebugString:           n.DebugString(10000),
		IsTableExpression:     n.IsTableExpression(),
		IsQueryExpression:     n.IsQueryExpression(),
		IsExpression:          n.IsExpression(),
		IsType:                n.IsType(),
		IsLeaf:                n.IsLeaf(),
		IsStatement:           n.IsStatement(),
		IsScriptStatement:     n.IsScriptStatement(),
		IsLoopStatement:       n.IsLoopStatement(),
		IsSQLStatement:        n.IsSqlStatement(),
		IsDDLStatement:        n.IsDdlStatement(),
		IsCreateStatement:     n.IsCreateStatement(),
		IsAlterStatement:      n.IsAlterStatement(),
		ParseLocationRange: &ASTParseLocationRange{
			Start: &ASTParseLocationPoint{
				ByteOffset: loc.Start().ByteOffset(),
			},
			End: &ASTParseLocationPoint{
				ByteOffset: loc.End().ByteOffset(),
			},
		},
		Location: n.LocationString(),
		Children: children,
	}

	return &an
}
