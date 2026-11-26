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
		unsupported := filepath.Join(dir, "4_unsupported.sql")
		last := filepath.Join(dir, "5_last.txt") // explicit non-.sql file must be formatted

		require.NoError(t, os.WriteFile(first, []byte("select 1"), 0o600))
		require.NoError(t, os.WriteFile(broken, []byte("SELECT FROM WHERE"), 0o600))
		require.NoError(
			t,
			os.WriteFile(
				unsupported,
				[]byte("CREATE PRIVILEGE RESTRICTION ON SELECT ON TABLE t RESTRICT TO ('user:a@b.com')"),
				0o600,
			),
		)
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

	t.Run("-l -w walks .sql.tpl and .bql.tpl while skipping dotfiles and unsupported extensions", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		bql := filepath.Join(dir, "a.bql")
		sql := filepath.Join(dir, "b.sql")
		bqlJ2 := filepath.Join(dir, "c.bql.j2")
		sqlJ2 := filepath.Join(dir, "d.sql.j2")
		bqlTpl := filepath.Join(dir, "e.bql.tpl")
		sqlTpl := filepath.Join(dir, "f.sql.tpl")
		hiddenTpl := filepath.Join(dir, ".hidden.sql.tpl")
		ignoredTpl := filepath.Join(dir, "ignored.tpl")
		backupBak := filepath.Join(dir, "backup.sql.bak")
		notesTxt := filepath.Join(dir, "notes.txt")

		require.NoError(t, os.WriteFile(bql, []byte("select 1"), 0o600))
		require.NoError(t, os.WriteFile(sql, []byte("SELECT 2\n"), 0o600))
		require.NoError(t, os.WriteFile(bqlJ2, []byte("select {{ a }}"), 0o600))
		require.NoError(t, os.WriteFile(sqlJ2, []byte("select {{ b }}"), 0o600))
		require.NoError(t, os.WriteFile(bqlTpl, []byte("select {{ c }}"), 0o600))
		require.NoError(t, os.WriteFile(sqlTpl, []byte("select {{ d }} from {{ tbl }}"), 0o600))
		require.NoError(t, os.WriteFile(hiddenTpl, []byte("select 7"), 0o600))
		require.NoError(t, os.WriteFile(ignoredTpl, []byte("not sql {{{{"), 0o600))
		require.NoError(t, os.WriteFile(backupBak, []byte("not sql {{{{"), 0o600))
		require.NoError(t, os.WriteFile(notesTxt, []byte("not sql {{{{"), 0o600))

		var stdout, stderr bytes.Buffer

		code := run([]string{"-l", "-w", dir}, strings.NewReader(""), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Empty(t, stderr.String())
		assert.Equal(t, bql+"\n"+bqlJ2+"\n"+sqlJ2+"\n"+bqlTpl+"\n"+sqlTpl+"\n", stdout.String())

		tplContent, err := os.ReadFile(sqlTpl)
		require.NoError(t, err)
		assert.Equal(t, "SELECT {{ d }}\n  FROM {{ tbl }}\n", string(tplContent))

		hiddenAfter, err := os.ReadFile(hiddenTpl)
		require.NoError(t, err)
		assert.Equal(t, "select 7", string(hiddenAfter))

		ignoredAfter, err := os.ReadFile(ignoredTpl)
		require.NoError(t, err)
		assert.Equal(t, "not sql {{{{", string(ignoredAfter))

		bakAfter, err := os.ReadFile(backupBak)
		require.NoError(t, err)
		assert.Equal(t, "not sql {{{{", string(bakAfter))
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

	t.Run("directory walk skips hidden directories and node_modules", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		good := filepath.Join(dir, "models", "good.sql")
		gitSQL := filepath.Join(dir, ".git", "hooks", "broken.sql")
		venvSQL := filepath.Join(dir, ".venv", "pkg", "broken.sql")
		nestedHidden := filepath.Join(dir, "models", ".cache", "broken.sql")
		nodeModSQL := filepath.Join(dir, "node_modules", "pkg", "broken.sql")

		for _, p := range []string{good, gitSQL, venvSQL, nestedHidden, nodeModSQL} {
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
		}

		require.NoError(t, os.WriteFile(good, []byte("select 1"), 0o600))
		// Invalid SQL inside skipped directories would fail the run if visited.
		for _, p := range []string{gitSQL, venvSQL, nestedHidden, nodeModSQL} {
			require.NoError(t, os.WriteFile(p, []byte("SELECT FROM WHERE"), 0o600))
		}

		var stdout, stderr bytes.Buffer

		code := run([]string{"-l", dir}, strings.NewReader(""), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Equal(t, good+"\n", stdout.String())
		assert.Empty(t, stderr.String())
	})

	t.Run("explicitly targeted hidden directory is walked while its hidden children are skipped", func(t *testing.T) {
		t.Parallel()

		parent := t.TempDir()
		hiddenRoot := filepath.Join(parent, ".queries")
		target := filepath.Join(hiddenRoot, "report.sql")
		nestedGit := filepath.Join(hiddenRoot, ".git", "ignored.sql")

		require.NoError(t, os.MkdirAll(filepath.Dir(nestedGit), 0o750))
		require.NoError(t, os.WriteFile(target, []byte("select 2"), 0o600))
		require.NoError(t, os.WriteFile(nestedGit, []byte("SELECT FROM WHERE"), 0o600))

		var stdout, stderr bytes.Buffer

		code := run([]string{"-l", hiddenRoot}, strings.NewReader(""), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Equal(t, target+"\n", stdout.String())
		assert.Empty(t, stderr.String())
	})
}

func TestBqfmtDiffCheckStdinFilenameAndVersion(t *testing.T) {
	t.Parallel()

	t.Run("-version prints version and exits 0", func(t *testing.T) {
		t.Parallel()

		var stdout, stderr bytes.Buffer

		code := run([]string{"-version"}, strings.NewReader(""), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.True(t, strings.HasPrefix(stdout.String(), "bqfmt "), "got %q", stdout.String())
		assert.Empty(t, stderr.String())
	})

	t.Run("-d emits unified diff on changes and empty output when already formatted", func(t *testing.T) {
		t.Parallel()

		var stdout, stderr bytes.Buffer

		code := run([]string{"-d"}, strings.NewReader("select a, b from t"), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Empty(t, stderr.String())
		assert.Contains(t, stdout.String(), "--- <standard input>.orig\n+++ <standard input>\n")
		assert.Contains(t, stdout.String(), "-select a, b from t\n\\ No newline at end of file\n")
		assert.Contains(t, stdout.String(), "+SELECT a, b\n+  FROM t\n")

		stdout.Reset()
		stderr.Reset()

		code = run([]string{"-d"}, strings.NewReader("SELECT a, b\n  FROM t\n"), &stdout, &stderr)
		require.Equal(t, 0, code, stderr.String())
		assert.Empty(t, stdout.String())
		assert.Empty(t, stderr.String())
	})

	t.Run("-check exits 0 when clean, 1 on diff, 2 on error, and rejects -w", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		clean := filepath.Join(dir, "clean.sql")
		dirty := filepath.Join(dir, "dirty.sql")
		broken := filepath.Join(dir, "broken.sql")

		require.NoError(t, os.WriteFile(clean, []byte("SELECT 1\n"), 0o600))
		require.NoError(t, os.WriteFile(dirty, []byte("select 1"), 0o600))
		require.NoError(t, os.WriteFile(broken, []byte("SELECT FROM WHERE"), 0o600))

		var stdout, stderr bytes.Buffer

		// Clean file -> exit 0
		code := run([]string{"-check", clean}, strings.NewReader(""), &stdout, &stderr)
		assert.Equal(t, 0, code)
		assert.Empty(t, stdout.String())
		assert.Empty(t, stderr.String())

		// Dirty file with -check alone -> exit 1, quiet stdout
		code = run([]string{"-check", dirty}, strings.NewReader(""), &stdout, &stderr)
		assert.Equal(t, 1, code)
		assert.Empty(t, stdout.String())
		assert.Empty(t, stderr.String())

		// Dirty file with -check -l -> exit 1 + listed filename
		code = run([]string{"-check", "-l", dirty}, strings.NewReader(""), &stdout, &stderr)
		assert.Equal(t, 1, code)
		assert.Equal(t, dirty+"\n", stdout.String())
		assert.Empty(t, stderr.String())

		// Dirty + broken -> error exit 2 takes precedence over diff exit 1
		stdout.Reset()
		stderr.Reset()
		code = run([]string{"-check", "-d", dirty, broken}, strings.NewReader(""), &stdout, &stderr)
		assert.Equal(t, 2, code)
		assert.Contains(t, stdout.String(), "--- "+dirty+".orig")
		assert.Contains(t, stderr.String(), broken)

		// -check with -w -> rejected with exit 2
		stdout.Reset()
		stderr.Reset()
		code = run([]string{"-check", "-w", clean}, strings.NewReader(""), &stdout, &stderr)
		assert.Equal(t, 2, code)
		assert.Contains(t, stderr.String(), "cannot use -check with -w")
	})

	t.Run("-stdin-filename discovers config, matches styles, and labels output", func(t *testing.T) {
		t.Parallel()

		rootDir := t.TempDir()
		martDir := filepath.Join(rootDir, "models", "mart")
		require.NoError(t, os.MkdirAll(martDir, 0o750))

		cfgContent := `
default_style = "upper"

[[styles]]
name = "lower_mart"
include = "models/mart/*.sql"

[styles.options]
keyword_style = "LOWER_CASE"

[[styles]]
name = "upper"

[styles.options]
keyword_style = "UPPER_CASE"
`
		require.NoError(t, os.WriteFile(filepath.Join(rootDir, ".bqfmt.toml"), []byte(cfgContent), 0o600))

		virtualPath := filepath.Join(martDir, "orders.sql")

		var stdout, stderr bytes.Buffer

		code := run(
			[]string{"-stdin-filename", virtualPath},
			strings.NewReader("SELECT 1 AS x"),
			&stdout,
			&stderr,
		)
		require.Equal(t, 0, code, stderr.String())
		assert.Equal(t, "select 1 as x\n", stdout.String())

		// Also verify -l and -d use virtualPath as the label.
		stdout.Reset()
		stderr.Reset()
		code = run(
			[]string{"-check", "-l", "-d", "-stdin-filename", virtualPath},
			strings.NewReader("SELECT 1 AS x"),
			&stdout,
			&stderr,
		)
		assert.Equal(t, 1, code)
		assert.Contains(t, stdout.String(), virtualPath+"\n")
		assert.Contains(t, stdout.String(), "--- "+virtualPath+".orig\n+++ "+virtualPath+"\n")

		// Reject -stdin-filename with positional file arguments.
		stdout.Reset()
		stderr.Reset()
		code = run(
			[]string{"-stdin-filename", virtualPath, virtualPath},
			strings.NewReader(""),
			&stdout,
			&stderr,
		)
		assert.Equal(t, 2, code)
		assert.Contains(t, stderr.String(), "cannot use -stdin-filename with file arguments")
	})

	t.Run("unifiedDiff emits multiple hunks when changes are separated", func(t *testing.T) {
		t.Parallel()

		oldText := "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n"
		newText := "L1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nL10\n"

		diff := unifiedDiff("q.sql", oldText, newText)
		assert.Equal(t, 2, strings.Count(diff, "@@ "))
		assert.Contains(t, diff, "@@ -1,4 +1,4 @@\n-l1\n+L1\n")
		assert.Contains(t, diff, "@@ -7,4 +7,4 @@\n l7\n l8\n l9\n-l10\n+L10\n")
		assert.Empty(t, unifiedDiff("q.sql", oldText, oldText))
	})

	t.Run("-stdin-filename matches .sql.tpl style rules", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		cfgContent := `
default_style = "upper"

[[styles]]
name = "tpl_lower"
include = "*.sql.tpl"
[styles.options]
keyword_style = "LOWER_CASE"

[[styles]]
name = "upper"
[styles.options]
keyword_style = "UPPER_CASE"
`
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".bqfmt.toml"), []byte(cfgContent), 0o600))

		var stdout, stderr bytes.Buffer

		code := run(
			[]string{"-stdin-filename", filepath.Join(dir, "model.sql.tpl")},
			strings.NewReader("SELECT 1 AS x"),
			&stdout,
			&stderr,
		)
		require.Equal(t, 0, code, stderr.String())
		assert.Empty(t, stderr.String())
		assert.Equal(t, "select 1 as x\n", stdout.String())
	})

	t.Run("[lsp] section coexists across all layouts and rejects invalid [lsp] options with exit 2", func(t *testing.T) {
		t.Parallel()

		validCases := []struct {
			name string
			toml string
			want string
		}{
			{
				name: "flat + lsp",
				toml: "keyword_style = \"LOWER_CASE\"\n[lsp]\ndefault_project = \"proj\"\n",
				want: "select 1 as x\n",
			},
			{
				name: "options + lsp",
				toml: "[options]\nkeyword_style = \"LOWER_CASE\"\n[lsp]\ndefault_dataset = \"ds\"\n",
				want: "select 1 as x\n",
			},
			{
				name: "print_options + lsp",
				toml: "[print_options]\nkeyword_style = \"LOWER_CASE\"\n[lsp]\nbigquery_api_enabled = true\n",
				want: "select 1 as x\n",
			},
			{
				name: "styles + lsp",
				toml: "[[styles]]\nname = \"s\"\n[styles.options]\nkeyword_style = \"LOWER_CASE\"\n[lsp]\ncache_ttl_seconds = 60\n",
				want: "select 1 as x\n",
			},
			{
				name: "lsp only",
				toml: "[lsp]\nrequire_partition_filter_severity = \"error\"\n",
				want: "SELECT 1 AS x\n",
			},
		}

		for _, tc := range validCases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				dir := t.TempDir()
				cfgPath := filepath.Join(dir, ".bqfmt.toml")
				require.NoError(t, os.WriteFile(cfgPath, []byte(tc.toml), 0o600))

				var stdout, stderr bytes.Buffer

				code := run([]string{"-config", cfgPath}, strings.NewReader("SELECT 1 AS x"), &stdout, &stderr)
				require.Equal(t, 0, code, stderr.String())
				assert.Empty(t, stderr.String())
				assert.Equal(t, tc.want, stdout.String())
			})
		}

		invalidCases := []struct {
			name    string
			toml    string
			wantErr string
		}{
			{
				name:    "unknown lsp key",
				toml:    "[lsp]\nbad_key = true\n",
				wantErr: `unknown option "lsp.bad_key"`,
			},
			{
				name:    "negative cache_ttl_seconds",
				toml:    "[lsp]\ncache_ttl_seconds = -1\n",
				wantErr: "invalid lsp option",
			},
			{
				name:    "invalid severity",
				toml:    "[lsp]\nrequire_partition_filter_severity = \"fatal\"\n",
				wantErr: "invalid lsp option",
			},
		}

		for _, tc := range invalidCases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				dir := t.TempDir()
				cfgPath := filepath.Join(dir, ".bqfmt.toml")
				require.NoError(t, os.WriteFile(cfgPath, []byte(tc.toml), 0o600))

				var stdout, stderr bytes.Buffer

				code := run([]string{"-config", cfgPath}, strings.NewReader("SELECT 1"), &stdout, &stderr)
				assert.Equal(t, 2, code)
				assert.Empty(t, stdout.String())
				assert.Contains(t, stderr.String(), tc.wantErr)
			})
		}
	})
}
