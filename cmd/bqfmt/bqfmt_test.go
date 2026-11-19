package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBqfmtCLI(t *testing.T) {
	t.Parallel()

	t.Run("stdin formats unformatted SQL to stdout", func(t *testing.T) {
		t.Parallel()

		var stdout, stderr bytes.Buffer

		code := run(nil, strings.NewReader("select a, b from my_table where a = 1;"), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Empty(t, stderr.String())
		assert.Equal(t, "SELECT a, b\n  FROM my_table\n WHERE a = 1\n", stdout.String())
	})

	t.Run("stdin preserves already formatted SQL on stdout", func(t *testing.T) {
		t.Parallel()

		alreadyFormatted := "SELECT a, b\n  FROM my_table\n WHERE a = 1\n"

		var stdout, stderr bytes.Buffer

		code := run(nil, strings.NewReader(alreadyFormatted), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Empty(t, stderr.String())
		assert.Equal(t, alreadyFormatted, stdout.String())
	})

	t.Run("rejects -w with standard input", func(t *testing.T) {
		t.Parallel()

		var stdout, stderr bytes.Buffer

		code := run([]string{"-w"}, strings.NewReader("SELECT 1\n"), &stdout, &stderr)
		assert.Equal(t, 2, code)
		assert.Contains(t, stderr.String(), "cannot use -w with standard input")
	})

	t.Run("returns exit code 2 with filename on invalid SQL", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		badFile := filepath.Join(dir, "broken.sql")
		require.NoError(t, os.WriteFile(badFile, []byte("SELECT FROM WHERE"), 0o600))

		var stdout, stderr bytes.Buffer

		code := run([]string{badFile}, strings.NewReader(""), &stdout, &stderr)
		assert.Equal(t, 2, code)
		assert.Contains(t, stderr.String(), badFile)
		assert.Contains(t, stderr.String(), "cannot format input")
	})

	t.Run("list -l and write -w on directory tree", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		sqlPath := filepath.Join(dir, "query.sql")
		j2Path := filepath.Join(dir, "templated.sql.j2")
		ignoredPath := filepath.Join(dir, "notes.txt")

		require.NoError(t, os.WriteFile(sqlPath, []byte("select 1 as x"), 0o600))
		require.NoError(t, os.WriteFile(j2Path, []byte("select {{ col }} from {{ tbl }}"), 0o600))
		require.NoError(t, os.WriteFile(ignoredPath, []byte("not sql {{{{"), 0o600))

		var listOut, listErr bytes.Buffer

		code := run([]string{"-l", dir}, strings.NewReader(""), &listOut, &listErr)
		require.Equal(t, 0, code, listErr.String())
		assert.Contains(t, listOut.String(), sqlPath)
		assert.Contains(t, listOut.String(), j2Path)
		assert.NotContains(t, listOut.String(), ignoredPath)

		var writeOut, writeErr bytes.Buffer

		code = run([]string{"-w", dir}, strings.NewReader(""), &writeOut, &writeErr)
		require.Equal(t, 0, code, writeErr.String())
		assert.Empty(t, writeOut.String())

		formattedSQL, err := os.ReadFile(sqlPath)
		require.NoError(t, err)
		assert.Equal(t, "SELECT 1 AS x\n", string(formattedSQL))

		var listAfterOut, listAfterErr bytes.Buffer

		code = run([]string{"-l", dir}, strings.NewReader(""), &listAfterOut, &listAfterErr)
		require.Equal(t, 0, code, listAfterErr.String())
		assert.Empty(t, listAfterOut.String())
	})
}
