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

func TestConfigSchemas(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		toml string
		sql  string
		want string
	}{
		{
			name: "empty config uses defaults",
			toml: "",
			sql:  "select 1 as a",
			want: "SELECT 1 AS a\n",
		},
		{
			name: "comment-only config uses defaults",
			toml: "# formatting defaults\n",
			sql:  "select 1 as a",
			want: "SELECT 1 AS a\n",
		},
		{
			name: "flat options",
			toml: "keyword_style = \"LOWER_CASE\"\n",
			sql:  "SELECT 1 AS a",
			want: "select 1 as a\n",
		},
		{
			name: "options table section",
			toml: "[options]\nkeyword_style = \"LOWER_CASE\"\n",
			sql:  "SELECT 1 AS a",
			want: "select 1 as a\n",
		},
		{
			name: "print_options table section",
			toml: "[print_options]\nkeyword_style = \"LOWER_CASE\"\nindentation = 4\n",
			sql:  "SELECT a, b FROM t WHERE a = 1 AND b = 2",
			want: "select a, b\n  from t\n where a = 1\n   and b = 2\n",
		},
		{
			name: "multi-style without default_style falls back to first style",
			toml: "[[styles]]\nname = \"first\"\n[styles.options]\nkeyword_style = \"LOWER_CASE\"\n\n[[styles]]\nname = \"second\"\n[styles.options]\nkeyword_style = \"UPPER_CASE\"\n",
			sql:  "SELECT 1 AS a",
			want: "select 1 as a\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			cfgPath := filepath.Join(dir, ".bqfmt.toml")
			require.NoError(t, os.WriteFile(cfgPath, []byte(tc.toml), 0o600))

			var stdout, stderr bytes.Buffer

			code := run([]string{"-config", cfgPath}, strings.NewReader(tc.sql), &stdout, &stderr)
			require.Equal(t, 0, code, stderr.String())
			assert.Empty(t, stderr.String())
			assert.Equal(t, tc.want, stdout.String())
		})
	}
}

func TestConfigValidationErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		toml    string
		wantErr string
	}{
		{
			name:    "malformed toml syntax",
			toml:    "keyword_style = [unclosed\n",
			wantErr: "parse config",
		},
		{
			name:    "unknown key in flat config",
			toml:    "unknown_option = true\n",
			wantErr: `unknown option "unknown_option"`,
		},
		{
			name:    "unknown key in options section",
			toml:    "[options]\nunknown_option = true\n",
			wantErr: `unknown option "options.unknown_option"`,
		},
		{
			name:    "unknown key in print_options section",
			toml:    "[print_options]\nunknown_option = true\n",
			wantErr: `unknown option "print_options.unknown_option"`,
		},
		{
			name:    "unknown top-level key alongside options section",
			toml:    "stray_key = 1\n[options]\nindentation = 2\n",
			wantErr: `unknown option "stray_key"`,
		},
		{
			name:    "unknown field inside styles entry",
			toml:    "[[styles]]\nname = \"s\"\nunknown_field = \"x\"\n",
			wantErr: `unknown option "styles.unknown_field"`,
		},
		{
			name:    "unknown key inside second styles.options entry",
			toml:    "[[styles]]\nname = \"a\"\n[styles.options]\nindentation = 2\n[[styles]]\nname = \"b\"\n[styles.options]\nbad_opt = 1\n",
			wantErr: `unknown option "styles.options.bad_opt"`,
		},
		{
			name:    "invalid regex in style",
			toml:    "[[styles]]\nname = \"bad\"\nregex = \"[unclosed\"\n",
			wantErr: `style "bad" regex`,
		},
		{
			name:    "invalid include glob in style",
			toml:    "[[styles]]\nname = \"s\"\ninclude = \"[unclosed\"\n",
			wantErr: "syntax error in pattern",
		},
		{
			name:    "conflicting options and print_options",
			toml:    "[options]\nindentation = 2\n[print_options]\nindentation = 4\n",
			wantErr: "conflicting configuration sections",
		},
		{
			name:    "conflicting styles and options",
			toml:    "[[styles]]\nname = \"a\"\n[options]\nindentation = 2\n",
			wantErr: "conflicting configuration sections",
		},
		{
			name:    "conflicting empty styles and options",
			toml:    "styles = []\n[options]\nindentation = 2\n",
			wantErr: "conflicting configuration sections",
		},
		{
			name:    "empty styles array",
			toml:    "styles = []\n",
			wantErr: "styles section must define at least one style",
		},
		{
			name:    "duplicate style names",
			toml:    "default_style = \"dup\"\n[[styles]]\nname = \"dup\"\n[[styles]]\nname = \"dup\"\n",
			wantErr: `duplicate style name "dup"`,
		},
		{
			name:    "missing default_style target",
			toml:    "default_style = \"missing\"\n[[styles]]\nname = \"present\"\n",
			wantErr: `default_style "missing" not found`,
		},
		{
			name:    "orphan default_style without styles",
			toml:    "default_style = \"custom\"\n[options]\nindentation = 2\n",
			wantErr: "default_style requires a styles section",
		},
		{
			name:    "negative integer option",
			toml:    "indentation = -1\n",
			wantErr: "invalid print option",
		},
		{
			name:    "invalid enum option in style",
			toml:    "[[styles]]\nname = \"s\"\n[styles.options]\nkeyword_style = \"BAD\"\n",
			wantErr: "invalid print option",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "cfg.toml")
			require.NoError(t, os.WriteFile(cfgPath, []byte(tc.toml), 0o600))

			var stdout, stderr bytes.Buffer

			code := run([]string{"-config", cfgPath}, strings.NewReader("SELECT 1"), &stdout, &stderr)
			assert.Equal(t, 2, code)
			assert.Empty(t, stdout.String())
			assert.Contains(t, stderr.String(), tc.wantErr)
		})
	}

	t.Run("missing and directory -config paths fail cleanly", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()

		var stdout, stderr bytes.Buffer

		code := run([]string{"-config", filepath.Join(dir, "does_not_exist.toml")}, strings.NewReader("SELECT 1"), &stdout, &stderr)
		assert.Equal(t, 2, code)
		assert.Contains(t, stderr.String(), "read config")

		stdout.Reset()
		stderr.Reset()

		code = run([]string{"-config", dir}, strings.NewReader("SELECT 1"), &stdout, &stderr)
		assert.Equal(t, 2, code)
		assert.Contains(t, stderr.String(), "read config")
	})
}

func TestConfigStyleMatchingAcrossWorkingDirs(t *testing.T) {
	// Uses t.Chdir, so this test intentionally runs sequentially.
	repo := t.TempDir()
	martDir := filepath.Join(repo, "mart")
	stagingDir := filepath.Join(repo, "staging")
	deepDir := filepath.Join(repo, "analytics", "deep")
	outsideDir := t.TempDir()

	require.NoError(t, os.MkdirAll(martDir, 0o750))
	require.NoError(t, os.MkdirAll(stagingDir, 0o750))
	require.NoError(t, os.MkdirAll(deepDir, 0o750))

	cfgContent := `
default_style = "default"

[[styles]]
name = "mart_regex"
regex = "^mart/"
[styles.options]
keyword_style = "LOWER_CASE"

[[styles]]
name = "staging_path_glob"
include = "staging/*.sql"
[styles.options]
keyword_style = "LOWER_CASE"
bool_style = "LOWER_CASE"

[[styles]]
name = "jinja_basename_glob"
include = "*.sql.j2"
[styles.options]
keyword_style = "LOWER_CASE"
null_style = "LOWER_CASE"

[[styles]]
name = "default"
[styles.options]
keyword_style = "UPPER_CASE"
bool_style = "UPPER_CASE"
null_style = "UPPER_CASE"
`
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".bqfmt.toml"), []byte(cfgContent), 0o600))

	martFile := filepath.Join(martDir, "orders.sql")
	stagingFile := filepath.Join(stagingDir, "raw.sql")
	jinjaFile := filepath.Join(deepDir, "model.sql.j2")
	plainFile := filepath.Join(deepDir, "plain.sql")

	require.NoError(t, os.WriteFile(martFile, []byte("SELECT TRUE, NULL"), 0o600))
	require.NoError(t, os.WriteFile(stagingFile, []byte("SELECT TRUE, NULL"), 0o600))
	require.NoError(t, os.WriteFile(jinjaFile, []byte("SELECT TRUE, NULL"), 0o600))
	require.NoError(t, os.WriteFile(plainFile, []byte("SELECT TRUE, NULL"), 0o600))

	// From repo root with relative paths.
	t.Chdir(repo)

	for _, tc := range []struct {
		arg  string
		want string
	}{
		{"mart/orders.sql", "select TRUE, NULL\n"},
		{"staging/raw.sql", "select true, NULL\n"},
		{"analytics/deep/model.sql.j2", "select TRUE, null\n"},
		{"analytics/deep/plain.sql", "SELECT TRUE, NULL\n"},
	} {
		var stdout, stderr bytes.Buffer

		code := run([]string{tc.arg}, strings.NewReader(""), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Equal(t, tc.want, stdout.String(), "from repo root: %s", tc.arg)
	}

	// From mart/ subdir: regex "^mart/" must still match orders.sql.
	t.Chdir(martDir)

	var stdout, stderr bytes.Buffer

	code := run([]string{"orders.sql"}, strings.NewReader(""), &stdout, &stderr)
	require.Equal(t, 0, code, stderr.String())
	assert.Equal(t, "select TRUE, NULL\n", stdout.String())

	// From staging/ subdir: path-qualified include "staging/*.sql" must still match raw.sql.
	t.Chdir(stagingDir)
	stdout.Reset()
	stderr.Reset()

	code = run([]string{"raw.sql"}, strings.NewReader(""), &stdout, &stderr)
	require.Equal(t, 0, code, stderr.String())
	assert.Equal(t, "select true, NULL\n", stdout.String())

	// Stdin from mart/ discovers repo/.bqfmt.toml and uses default_style (not file filters).
	t.Chdir(martDir)
	stdout.Reset()
	stderr.Reset()

	code = run(nil, strings.NewReader("select true, null"), &stdout, &stderr)
	require.Equal(t, 0, code, stderr.String())
	assert.Equal(t, "SELECT TRUE, NULL\n", stdout.String())

	// From an unrelated cwd with absolute paths: both regex and path-qualified include must match.
	t.Chdir(outsideDir)

	for _, tc := range []struct {
		arg  string
		want string
	}{
		{martFile, "select TRUE, NULL\n"},
		{stagingFile, "select true, NULL\n"},
		{jinjaFile, "select TRUE, null\n"},
		{plainFile, "SELECT TRUE, NULL\n"},
	} {
		stdout.Reset()
		stderr.Reset()

		code = run([]string{tc.arg}, strings.NewReader(""), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Equal(t, tc.want, stdout.String(), "absolute path: %s", tc.arg)
	}
}

func TestConfigNestedDiscoveryAndExplicitOverride(t *testing.T) {
	t.Parallel()

	t.Run("nested .bqfmt.toml overrides parent without leaking to sibling", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		childDir := filepath.Join(root, "child", "deep")
		siblingDir := filepath.Join(root, "sibling")
		require.NoError(t, os.MkdirAll(childDir, 0o750))
		require.NoError(t, os.MkdirAll(siblingDir, 0o750))

		require.NoError(t, os.WriteFile(filepath.Join(root, ".bqfmt.toml"), []byte("keyword_style = \"LOWER_CASE\"\n"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(root, "child", ".bqfmt.toml"), []byte("keyword_style = \"UPPER_CASE\"\n"), 0o600))

		childFile := filepath.Join(childDir, "a.sql")
		siblingFile := filepath.Join(siblingDir, "b.sql")
		require.NoError(t, os.WriteFile(childFile, []byte("select 1"), 0o600))
		require.NoError(t, os.WriteFile(siblingFile, []byte("SELECT 2"), 0o600))

		var stdout, stderr bytes.Buffer

		code := run([]string{childFile, siblingFile}, strings.NewReader(""), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Equal(t, "SELECT 1\nselect 2\n", stdout.String())
	})

	t.Run("explicit -config overrides broken local .bqfmt.toml and matches external paths", func(t *testing.T) {
		t.Parallel()

		cfgDir := t.TempDir()
		workDir := t.TempDir()
		subDir := filepath.Join(workDir, "special")
		require.NoError(t, os.MkdirAll(subDir, 0o750))

		// Broken local config that must be ignored when -config is set.
		require.NoError(t, os.WriteFile(filepath.Join(subDir, ".bqfmt.toml"), []byte("broken = [[["), 0o600))

		externalCfg := filepath.Join(cfgDir, "shared.toml")
		require.NoError(t, os.WriteFile(externalCfg, []byte(`
default_style = "default"

[[styles]]
name = "special"
regex = "special/[^/]+\\.sql$"
[styles.options]
keyword_style = "LOWER_CASE"

[[styles]]
name = "default"
[styles.options]
keyword_style = "UPPER_CASE"
`), 0o600))

		targetFile := filepath.Join(subDir, "q.sql")
		require.NoError(t, os.WriteFile(targetFile, []byte("SELECT 1"), 0o600))

		var stdout, stderr bytes.Buffer

		code := run([]string{"-config", externalCfg, targetFile}, strings.NewReader(""), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Equal(t, "select 1\n", stdout.String())
	})

	t.Run("broken discovered .bqfmt.toml fails all files in directory cleanly", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".bqfmt.toml"), []byte("unknown_key = 42\n"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "a.sql"), []byte("SELECT 1"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "b.sql"), []byte("SELECT 2"), 0o600))

		var stdout, stderr bytes.Buffer

		code := run([]string{dir}, strings.NewReader(""), &stdout, &stderr)
		assert.Equal(t, 2, code)
		assert.Empty(t, stdout.String())
		assert.Contains(t, stderr.String(), `unknown option "unknown_key"`)
	})

	t.Run("first matching style wins across overlapping regex and include filters", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".bqfmt.toml"), []byte(`
default_style = "fallback"

[[styles]]
name = "specific"
regex = "^priority_"
include = "*.bql"
[styles.options]
keyword_style = "LOWER_CASE"

[[styles]]
name = "catchall"
include = "*.sql"
[styles.options]
keyword_style = "UPPER_CASE"
bool_style = "LOWER_CASE"

[[styles]]
name = "fallback"
[styles.options]
keyword_style = "UPPER_CASE"
bool_style = "UPPER_CASE"
`), 0o600))

		prioritySQL := filepath.Join(dir, "priority_one.sql")
		bqlFile := filepath.Join(dir, "other.bql")
		normalSQL := filepath.Join(dir, "normal.sql")
		otherTxt := filepath.Join(dir, "manual.txt")

		for _, p := range []string{prioritySQL, bqlFile, normalSQL, otherTxt} {
			require.NoError(t, os.WriteFile(p, []byte("SELECT TRUE"), 0o600))
		}

		var stdout, stderr bytes.Buffer

		code := run([]string{prioritySQL, bqlFile, normalSQL, otherTxt}, strings.NewReader(""), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Equal(t, "select TRUE\nselect TRUE\nSELECT true\nSELECT TRUE\n", stdout.String())
	})
}
