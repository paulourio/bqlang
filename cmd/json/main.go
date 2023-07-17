package main

import (
	"fmt"

	"github.com/goccy/go-zetasql"
	"github.com/goccy/go-zetasql/ast"
)

func main() {

	stmt, err := zetasql.ParseStatement("SELECT JSON '{}'", nil)
	if err != nil {
		panic(err)
	}

	// traverse all nodes of stmt.
	ast.Walk(stmt, func(n ast.Node) error {
		fmt.Printf("node: %T loc:%s\n", n, n.ParseLocationRange())
		return nil
	})
}
