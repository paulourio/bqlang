package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang"
)

const modeFlag = "-mode"

func TestBqdumpCLI(t *testing.T) {
	t.Parallel()

	t.Run("script mode outputs valid ASTScript JSON and exits 0", func(t *testing.T) {
		t.Parallel()

		var stdout, stderr bytes.Buffer

		code := run([]string{modeFlag, "script"}, strings.NewReader("SELECT 1 AS a;"), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Empty(t, stderr.String())

		var node bqlang.ASTNode
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &node))
		assert.Equal(t, "Script", node.Kind)
	})

	t.Run("statement mode outputs each statement JSON and exits 0", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		sqlFile := filepath.Join(dir, "multi.sql")
		require.NoError(t, os.WriteFile(sqlFile, []byte("DECLARE x INT64 DEFAULT 1;\nSELECT x;\n"), 0o600))

		var stdout, stderr bytes.Buffer

		code := run([]string{"-file", sqlFile, modeFlag, "statement"}, strings.NewReader(""), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Empty(t, stderr.String())

		chunks := strings.Split(strings.TrimSpace(stdout.String()), "\n\n")
		require.Len(t, chunks, 2)

		var first, second bqlang.ASTNode
		require.NoError(t, json.Unmarshal([]byte(chunks[0]), &first))
		require.NoError(t, json.Unmarshal([]byte(chunks[1]), &second))
		assert.Equal(t, "VariableDeclaration", first.Kind)
		assert.Equal(t, "QueryStatement", second.Kind)
	})

	t.Run("invalid SQL returns exit code 2 without crashing", func(t *testing.T) {
		t.Parallel()

		var stdout, stderr bytes.Buffer

		code := run([]string{modeFlag, "statement"}, strings.NewReader("SELECT FROM WHERE;"), &stdout, &stderr)
		assert.Equal(t, 2, code)
		assert.Contains(t, stderr.String(), "Syntax error")
	})

	t.Run("invalid mode returns exit code 2", func(t *testing.T) {
		t.Parallel()

		var stdout, stderr bytes.Buffer

		code := run([]string{modeFlag, "unknown"}, strings.NewReader("SELECT 1;"), &stdout, &stderr)
		assert.Equal(t, 2, code)
		assert.Contains(t, stderr.String(), "mode must be script or statement")
	})
}
