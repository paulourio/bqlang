package main

import (
	"bytes"
	"errors"
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

	t.Run("discovers upward .bqfmt.toml with multi-style schema", func(t *testing.T) {
		t.Parallel()

		rootDir := t.TempDir()
		subDir := filepath.Join(rootDir, "queries", "mart")
		require.NoError(t, os.MkdirAll(subDir, 0o750))

		cfgContent := `
default_style = "default"

[[styles]]
name = "default"

[styles.options]
soft_max_cols = 120
newline_before_clause = true
align_logical_with_clauses = true
align_trailing_comments = true
column_list_trailing_comma = "ALWAYS"
indentation = 2
indent_case_when = true
indent_with_clause = true
indent_with_entries = true
min_joins_to_separate_in_blocks = 2
max_cols_for_single_line_select = 1
max_params_for_single_line_function = 1
function_catalog = "BIGQUERY"
identifier_style = "LOWER_CASE"
keyword_style = "UPPER_CASE"
type_style = "UPPER_CASE"
builtin_function_name_style = "UPPER_CASE"
bool_style = "UPPER_CASE"
null_style = "UPPER_CASE"
pseudo_column_style = "UPPER_CASE"
table_name_style = "AS_IS"
string_style = "PREFER_SINGLE_QUOTE"
bytes_style = "PREFER_SINGLE_QUOTE"
hex_style = "LOWER_CASE"
numeric_style = "LOWER_CASE"
query_parameter_style = "AS_IS"
system_variable_style = "AS_IS"
function_name_style = "AS_IS"
`
		require.NoError(t, os.WriteFile(filepath.Join(rootDir, ".bqfmt.toml"), []byte(cfgContent), 0o600))

		targetSQL := filepath.Join(subDir, "orders.sql")
		require.NoError(
			t,
			os.WriteFile(
				targetSQL,
				[]byte("SELECT OrderId, _partitiondate FROM MyDataset.Orders WHERE OrderId = @TargetOrderId"),
				0o600,
			),
		)

		var stdout, stderr bytes.Buffer

		code := run([]string{targetSQL}, strings.NewReader(""), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Equal(
			t,
			"SELECT orderid,\n       _PARTITIONDATE,\n  FROM MyDataset.Orders\n WHERE orderid = @TargetOrderId\n",
			stdout.String(),
		)
	})

	t.Run("explicit -config flag with flat options and invalid option error", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		flatCfg := filepath.Join(dir, "custom.toml")
		require.NoError(t, os.WriteFile(flatCfg, []byte("keyword_style = \"LOWER_CASE\"\n"), 0o600))

		var stdout, stderr bytes.Buffer

		code := run([]string{"-config", flatCfg}, strings.NewReader("SELECT 1 AS a"), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Equal(t, "select 1 as a\n", stdout.String())

		badCfg := filepath.Join(dir, "bad.toml")
		require.NoError(t, os.WriteFile(badCfg, []byte("keyword_style = \"NOT_A_CASE\"\n"), 0o600))

		stdout.Reset()
		stderr.Reset()
		code = run([]string{"-config", badCfg}, strings.NewReader("SELECT 1"), &stdout, &stderr)
		assert.Equal(t, 2, code)
		assert.Contains(t, stderr.String(), "invalid print option")
	})

	t.Run("rejects invalid include glob, conflicting sections, and missing default_style", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()

		cases := []struct {
			name    string
			toml    string
			wantErr string
		}{
			{
				name:    "invalid_glob.toml",
				toml:    "[[styles]]\nname = \"s\"\ninclude = \"[unclosed\"\n",
				wantErr: "syntax error in pattern",
			},
			{
				name:    "conflicting.toml",
				toml:    "[options]\nindentation = 2\n[print_options]\nindentation = 4\n",
				wantErr: "conflicting configuration sections",
			},
			{
				name:    "missing_default.toml",
				toml:    "default_style = \"missing\"\n[[styles]]\nname = \"present\"\n",
				wantErr: "default_style \"missing\" not found",
			},
			{
				name:    "orphan_default_style.toml",
				toml:    "default_style = \"custom\"\n[options]\nindentation = 2\n",
				wantErr: "default_style requires a styles section",
			},
		}

		for _, tc := range cases {
			cfgPath := filepath.Join(dir, tc.name)
			require.NoError(t, os.WriteFile(cfgPath, []byte(tc.toml), 0o600))

			var stdout, stderr bytes.Buffer

			code := run([]string{"-config", cfgPath}, strings.NewReader("SELECT 1"), &stdout, &stderr)
			assert.Equal(t, 2, code)
			assert.Contains(t, stderr.String(), tc.wantErr)
		}
	})
}

type failingWriter struct {
	err error
}

func (f failingWriter) Write([]byte) (int, error) {
	return 0, f.err
}

func TestBqfmtFlagsAndStreams(t *testing.T) {
	t.Parallel()

	t.Run("-help exits 0 and writes usage to stderr", func(t *testing.T) {
		t.Parallel()

		var stdout, stderr bytes.Buffer

		code := run([]string{"-help"}, strings.NewReader(""), &stdout, &stderr)
		assert.Equal(t, 0, code)
		assert.Empty(t, stdout.String())
		assert.Contains(t, stderr.String(), "usage: bqfmt [flags] [path ...]")
	})

	t.Run("unknown flag exits 2 and writes error to stderr", func(t *testing.T) {
		t.Parallel()

		var stdout, stderr bytes.Buffer

		code := run([]string{"-no-such-flag"}, strings.NewReader(""), &stdout, &stderr)
		assert.Equal(t, 2, code)
		assert.Empty(t, stdout.String())
		assert.Contains(t, stderr.String(), "flag provided but not defined")
	})

	t.Run("-l on stdin reports diff only when formatting changes", func(t *testing.T) {
		t.Parallel()

		var stdout, stderr bytes.Buffer

		code := run([]string{"-l"}, strings.NewReader("select 1"), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Equal(t, "<standard input>\n", stdout.String())
		assert.Empty(t, stderr.String())

		stdout.Reset()
		stderr.Reset()

		code = run([]string{"-l"}, strings.NewReader("SELECT 1\n"), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Empty(t, stdout.String())
		assert.Empty(t, stderr.String())
	})

	t.Run("-debug emits trace to stderr without polluting stdout", func(t *testing.T) {
		t.Parallel()

		var stdout, stderr bytes.Buffer

		code := run([]string{"-debug"}, strings.NewReader("select 1"), &stdout, &stderr)
		require.Equal(t, 0, code)
		assert.Equal(t, "SELECT 1\n", stdout.String())
		assert.Contains(t, stderr.String(), "[bqfmt] ")
	})

	t.Run("empty, whitespace-only, comment-only, and CRLF inputs", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name  string
			input string
			want  string
		}{
			{"empty", "", ""},
			{"whitespace", "   \n\t\n", ""},
			{"comment_with_nl", "-- note\n", "-- note\n"},
			{"comment_no_nl", "-- note", "-- note\n"},
			{"crlf", "select a, b from t;\r\n", "SELECT a, b\n  FROM t\n"},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				var stdout, stderr bytes.Buffer

				code := run(nil, strings.NewReader(tc.input), &stdout, &stderr)
				require.Equal(t, 0, code, stderr.String())
				assert.Empty(t, stderr.String())
				assert.Equal(t, tc.want, stdout.String())
			})
		}
	})

	t.Run("stdout write failure returns exit code 2", func(t *testing.T) {
		t.Parallel()

		var stderr bytes.Buffer

		code := run(nil, strings.NewReader("select 1"), failingWriter{err: errors.New("broken pipe")}, &stderr)
		assert.Equal(t, 2, code)
		assert.Contains(t, stderr.String(), "write stdout")
		assert.Contains(t, stderr.String(), "broken pipe")
	})

	t.Run("multiple files preserve argument order and isolate errors", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		first := filepath.Join(dir, "1_first.sql")
		broken := filepath.Join(dir, "2_broken.sql")
		missing := filepath.Join(dir, "3_missing.sql")
		unsupported := filepath.Join(dir, "4_show.sql")
		last := filepath.Join(dir, "5_last.txt") // explicit non-.sql file must be formatted

		require.NoError(t, os.WriteFile(first, []byte("select 1"), 0o600))
		require.NoError(t, os.WriteFile(broken, []byte("SELECT FROM WHERE"), 0o600))
		require.NoError(t, os.WriteFile(unsupported, []byte("SHOW TABLES"), 0o600))
		require.NoError(t, os.WriteFile(last, []byte("select 2"), 0o600))

		var stdout, stderr bytes.Buffer

		code := run([]string{first, broken, missing, unsupported, last}, strings.NewReader(""), &stdout, &stderr)
		assert.Equal(t, 2, code)
		assert.Equal(t, "SELECT 1\nSELECT 2\n", stdout.String())
		assert.Contains(t, stderr.String(), broken)
		assert.Contains(t, stderr.String(), missing)
		assert.Contains(t, stderr.String(), unsupported)
	})
}

func TestBqfmtWalkSymlinksAndWriteSafety(t *testing.T) {
	t.Parallel()

	t.Run("-l -w combined lists and rewrites only changed files across all supported suffixes", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		bql := filepath.Join(dir, "a.bql")
		bqlJ2 := filepath.Join(dir, "b.bql.j2")
		clean := filepath.Join(dir, "c.sql")
		hidden := filepath.Join(dir, ".hidden.sql")

		require.NoError(t, os.WriteFile(bql, []byte("select 1"), 0o640))
		require.NoError(t, os.WriteFile(bqlJ2, []byte("select {{ x }}"), 0o600))
		require.NoError(t, os.WriteFile(clean, []byte("SELECT 3\n"), 0o600))
		require.NoError(t, os.WriteFile(hidden, []byte("select 4"), 0o600))

		var stdout, stderr bytes.Buffer

		code := run([]string{"-l", "-w", dir}, strings.NewReader(""), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Equal(t, bql+"\n"+bqlJ2+"\n", stdout.String())

		// Verify permissions preserved and no leftover backup files.
		info, err := os.Stat(bql)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())

		hiddenAfter, err := os.ReadFile(hidden)
		require.NoError(t, err)
		assert.Equal(t, "select 4", string(hiddenAfter))

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		assert.Len(t, entries, 4)
	})

	t.Run("symlinked .sql file under -w truncates target cleanly without stale bytes", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		realPath := filepath.Join(dir, "real.data")
		linkPath := filepath.Join(dir, "link.sql")

		orig := "SELECT    1,    2,    3,    4    FROM    my_very_long_table_name_here\n"
		require.NoError(t, os.WriteFile(realPath, []byte(orig), 0o640))
		require.NoError(t, os.Symlink("real.data", linkPath))

		var stdout, stderr bytes.Buffer

		code := run([]string{"-w", dir}, strings.NewReader(""), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Empty(t, stdout.String())

		got, err := os.ReadFile(realPath)
		require.NoError(t, err)
		assert.Equal(t, "SELECT 1, 2, 3, 4\n  FROM my_very_long_table_name_here\n", string(got))

		// Symlink itself must remain a symlink and no leftover backups should exist.
		linkInfo, err := os.Lstat(linkPath)
		require.NoError(t, err)
		assert.Equal(t, os.ModeSymlink, linkInfo.Mode().Type())

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		assert.Len(t, entries, 2)
	})

	t.Run("symlink to directory ending in .sql is skipped during directory walk", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		sub := filepath.Join(dir, "actual_dir")
		require.NoError(t, os.Mkdir(sub, 0o750))
		require.NoError(t, os.Symlink(sub, filepath.Join(dir, "dirlink.sql")))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "ok.sql"), []byte("select 1"), 0o600))

		var stdout, stderr bytes.Buffer

		code := run([]string{dir}, strings.NewReader(""), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Equal(t, "SELECT 1\n", stdout.String())
	})

	t.Run("broken symlink reports error without aborting sibling files", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		require.NoError(t, os.Symlink("nonexistent.sql", filepath.Join(dir, "a_broken.sql")))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "z_ok.sql"), []byte("select 1"), 0o600))

		var stdout, stderr bytes.Buffer

		code := run([]string{dir}, strings.NewReader(""), &stdout, &stderr)
		assert.Equal(t, 2, code)
		assert.Equal(t, "SELECT 1\n", stdout.String())
		assert.Contains(t, stderr.String(), "a_broken.sql")
	})

	t.Run("unreadable subdirectory reports error without aborting sibling directories", func(t *testing.T) {
		t.Parallel()

		if os.Geteuid() == 0 {
			t.Skip("permission checks do not apply when running as root")
		}

		dir := t.TempDir()
		blocked := filepath.Join(dir, "a_blocked")
		okDir := filepath.Join(dir, "z_ok")
		require.NoError(t, os.Mkdir(blocked, 0o750))
		require.NoError(t, os.Mkdir(okDir, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(okDir, "good.sql"), []byte("select 1"), 0o600))
		require.NoError(t, os.Chmod(blocked, 0o000))
		t.Cleanup(func() { _ = os.Chmod(blocked, 0o750) })

		var stdout, stderr bytes.Buffer

		code := run([]string{"-l", dir}, strings.NewReader(""), &stdout, &stderr)
		assert.Equal(t, 2, code)
		assert.Contains(t, stdout.String(), filepath.Join(okDir, "good.sql"))
		assert.Contains(t, stderr.String(), "a_blocked")
	})

	t.Run("read-only file under -w fails cleanly with no leftover backup and skips clean files", func(t *testing.T) {
		t.Parallel()

		if os.Geteuid() == 0 {
			t.Skip("permission checks do not apply when running as root")
		}

		dir := t.TempDir()
		dirtyRO := filepath.Join(dir, "dirty.sql")
		cleanRO := filepath.Join(dir, "clean.sql")

		require.NoError(t, os.WriteFile(dirtyRO, []byte("select 1"), 0o400))
		require.NoError(t, os.WriteFile(cleanRO, []byte("SELECT 1\n"), 0o400))
		t.Cleanup(func() {
			_ = os.Chmod(dirtyRO, 0o600)
			_ = os.Chmod(cleanRO, 0o600)
		})

		// Already-formatted read-only file succeeds without attempting a write.
		var stdout, stderr bytes.Buffer

		code := run([]string{"-w", cleanRO}, strings.NewReader(""), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())

		// Unformatted read-only file fails and cleans up its temporary backup.
		stdout.Reset()
		stderr.Reset()

		code = run([]string{"-w", dirtyRO}, strings.NewReader(""), &stdout, &stderr)
		assert.Equal(t, 2, code)
		assert.Contains(t, stderr.String(), dirtyRO)

		got, err := os.ReadFile(dirtyRO)
		require.NoError(t, err)
		assert.Equal(t, "select 1", string(got))

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		assert.Len(t, entries, 2)
	})
}

func TestBackupFileUsesSiblingDirForBareFilename(t *testing.T) {
	// Uses t.Chdir, so this test intentionally runs sequentially.
	dir := t.TempDir()
	t.Chdir(dir)

	runner := &cliRunner{fdSem: make(chan struct{}, 2)}

	bakname, err := runner.backupFile("query.sql", []byte("SELECT 1\n"), 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(bakname) })

	assert.Equal(t, ".", filepath.Dir(bakname))

	info, err := os.Stat(bakname)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	data, err := os.ReadFile(bakname)
	require.NoError(t, err)
	assert.Equal(t, "SELECT 1\n", string(data))
}

func TestRestoreOriginalAndReadSizeMismatch(t *testing.T) {
	t.Parallel()

	t.Run("restoreOriginal restores shorter original bytes and removes backup", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		target := filepath.Join(dir, "query.sql")
		bak := filepath.Join(dir, "query.sql.bak")
		orig := []byte("SELECT 1\n")
		longer := []byte("SELECT 1, 2, 3, 4, 5\n")

		require.NoError(t, os.WriteFile(target, longer, 0o600))
		require.NoError(t, os.WriteFile(bak, orig, 0o600))

		fout, err := os.OpenFile(target, os.O_WRONLY, 0o600)
		require.NoError(t, err)

		var restoreErr error

		restoreOriginal(fout, orig, len(longer), bak, func(rerr error) {
			restoreErr = rerr
		})

		require.NoError(t, restoreErr)

		got, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, string(orig), string(got))

		_, statErr := os.Stat(bak)
		assert.True(t, os.IsNotExist(statErr), "backup should be removed after successful restore")
	})

	t.Run("restoreOriginal preserves backup and invokes callback on write failure", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		target := filepath.Join(dir, "query.sql")
		bak := filepath.Join(dir, "query.sql.bak")
		orig := []byte("SELECT 1\n")

		require.NoError(t, os.WriteFile(target, orig, 0o600))
		require.NoError(t, os.WriteFile(bak, orig, 0o600))

		fout, err := os.OpenFile(target, os.O_WRONLY, 0o600)
		require.NoError(t, err)
		require.NoError(t, fout.Close()) // force WriteAt failure

		var restoreErr error

		restoreOriginal(fout, orig, len(orig), bak, func(rerr error) {
			restoreErr = rerr
		})

		require.Error(t, restoreErr)

		_, statErr := os.Stat(bak)
		require.NoError(t, statErr, "backup must be preserved when restore fails")
	})

	t.Run("readFile detects shrinking and growing files during read", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		f := filepath.Join(dir, "sample.sql")
		require.NoError(t, os.WriteFile(f, []byte("SELECT 123"), 0o600)) // 10 bytes

		info, err := os.Stat(f)
		require.NoError(t, err)

		runner := &cliRunner{fdSem: make(chan struct{}, 2)}

		_, err = runner.readFile(f, info, strings.NewReader("SELECT"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "changed during reading")

		_, err = runner.readFile(f, info, strings.NewReader("SELECT 1234567890"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "changed during reading")
	})
}
