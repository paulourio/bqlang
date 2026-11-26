package bqconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulourio/bqlang/formatter"
)

func TestIsBQLFilename(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		want bool
	}{
		// Supported suffixes
		{name: "query.bql", want: true},
		{name: "query.sql", want: true},
		{name: "query.bql.j2", want: true},
		{name: "query.sql.j2", want: true},
		{name: "query.bql.tpl", want: true},
		{name: "query.sql.tpl", want: true},
		{name: "models/mart/orders.sql", want: true},
		{name: "/abs/path/model.sql.tpl", want: true},
		{name: "dir.txt/query.bql.tpl", want: true},
		{name: ".hidden.sql", want: true},
		{name: ".hidden.sql.tpl", want: true},

		// Unsupported / edge cases
		{name: "", want: false},
		{name: "/", want: false},
		{name: string(filepath.Separator), want: false},
		{name: ".", want: false},
		{name: "..", want: false},
		{name: "dir.sql/", want: false},
		{name: "dir.sql" + string(filepath.Separator), want: false},
		{name: "dir.sql/notes.txt", want: false},
		{name: "QUERY.SQL", want: false},
		{name: "query.SQL", want: false},
		{name: "query.sql.J2", want: false},
		{name: "query.sql.TPL", want: false},
		{name: "query.sql.bak", want: false},
		{name: "model.tpl", want: false},
		{name: "model.j2", want: false},
		{name: "model.bql.jinja2", want: false},
		{name: "sql", want: false},
		{name: "bql", want: false},
		{name: "notes.txt", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, IsBQLFilename(tc.name))
		})
	}
}

func TestDefaultLSPOptions(t *testing.T) {
	// Subtest uses t.Setenv, so parent runs sequentially.
	t.Run("defaults match specification", func(t *testing.T) {
		opts := DefaultLSPOptions()
		require.NotNil(t, opts)
		assert.Empty(t, opts.DefaultProject)
		assert.Empty(t, opts.DefaultDataset)
		assert.Empty(t, opts.SchemaDirs)
		assert.False(t, opts.BigQueryAPIEnabled)
		assert.True(t, opts.DiagnosticsEnabled)
		assert.Equal(t, 86400, opts.CacheTTLSeconds)
		assert.Equal(t, SeverityWarning, opts.RequirePartitionFilterSeverity)
		assert.Equal(t, "bqpls", filepath.Base(opts.CacheDir))
		assert.True(t, filepath.IsAbs(opts.CacheDir))
		require.NoError(t, opts.Validate())

		data, err := json.Marshal(opts)
		require.NoError(t, err)

		var decoded LSPOptions
		require.NoError(t, json.Unmarshal(data, &decoded))
		assert.Equal(t, *opts, decoded)
	})

	t.Run("falls back to temp dir when UserCacheDir fails", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", "")
		t.Setenv("HOME", "")

		opts := DefaultLSPOptions()
		assert.Equal(t, filepath.Join(os.TempDir(), "bqpls"), opts.CacheDir)
	})
}

func TestLSPOptionsValidation(t *testing.T) {
	t.Parallel()

	t.Run("severity IsValid exhaustive", func(t *testing.T) {
		t.Parallel()

		for _, s := range []Severity{SeverityError, SeverityWarning, SeverityInfo, SeverityHint, SeverityOff} {
			assert.True(t, s.IsValid(), "expected %q to be valid", s)
		}

		for _, s := range []Severity{"", "warn", "ERROR", "fatal"} {
			assert.False(t, s.IsValid(), "expected %q to be invalid", s)
		}
	})

	t.Run("nil LSPOptions validates", func(t *testing.T) {
		t.Parallel()

		var nilOpts *LSPOptions
		assert.NoError(t, nilOpts.Validate())
	})

	t.Run("rejects invalid values wrapping ErrInvalidLSPOption", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name   string
			mutate func(*LSPOptions)
		}{
			{
				name: "negative cache_ttl_seconds",
				mutate: func(o *LSPOptions) {
					o.CacheTTLSeconds = -1
				},
			},
			{
				name: "invalid require_partition_filter_severity",
				mutate: func(o *LSPOptions) {
					o.RequirePartitionFilterSeverity = Severity("invalid")
				},
			},
			{
				name: "empty require_partition_filter_severity",
				mutate: func(o *LSPOptions) {
					o.RequirePartitionFilterSeverity = ""
				},
			},
			{
				name: "empty schema_dirs entry",
				mutate: func(o *LSPOptions) {
					o.SchemaDirs = []string{"./schemas", ""}
				},
			},
			{
				name: "whitespace schema_dirs entry",
				mutate: func(o *LSPOptions) {
					o.SchemaDirs = []string{" \t\n "}
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				opts := DefaultLSPOptions()
				tc.mutate(opts)

				err := opts.Validate()
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrInvalidLSPOption)
			})
		}
	})

	t.Run("accepts zero cache_ttl_seconds and valid schema_dirs", func(t *testing.T) {
		t.Parallel()

		opts := DefaultLSPOptions()
		opts.CacheTTLSeconds = 0
		opts.SchemaDirs = []string{"schemas", "/abs/schemas"}
		opts.RequirePartitionFilterSeverity = SeverityOff
		assert.NoError(t, opts.Validate())
	})
}

func TestLSPCoexistenceAcrossAllLayouts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		toml        string
		wantKeyword formatter.PrintCase
		wantProject string
		wantSev     Severity
	}{
		{
			name:        "lsp-only config",
			toml:        "[lsp]\ndefault_project = \"only-proj\"\nrequire_partition_filter_severity = \"error\"\n",
			wantKeyword: formatter.UpperCase,
			wantProject: "only-proj",
			wantSev:     SeverityError,
		},
		{
			name:        "empty lsp table",
			toml:        "[lsp]\n",
			wantKeyword: formatter.UpperCase,
			wantProject: "",
			wantSev:     SeverityWarning,
		},
		{
			name:        "lsp alongside flat options",
			toml:        "keyword_style = \"LOWER_CASE\"\n\n[lsp]\ndefault_project = \"flat-proj\"\nrequire_partition_filter_severity = \"info\"\n",
			wantKeyword: formatter.LowerCase,
			wantProject: "flat-proj",
			wantSev:     SeverityInfo,
		},
		{
			name:        "lsp alongside options section",
			toml:        "[options]\nkeyword_style = \"LOWER_CASE\"\n\n[lsp]\ndefault_project = \"opt-proj\"\nrequire_partition_filter_severity = \"hint\"\n",
			wantKeyword: formatter.LowerCase,
			wantProject: "opt-proj",
			wantSev:     SeverityHint,
		},
		{
			name:        "lsp alongside print_options section",
			toml:        "[print_options]\nkeyword_style = \"LOWER_CASE\"\n\n[lsp]\ndefault_project = \"print-proj\"\nrequire_partition_filter_severity = \"off\"\n",
			wantKeyword: formatter.LowerCase,
			wantProject: "print-proj",
			wantSev:     SeverityOff,
		},
		{
			name: "lsp alongside styles section",
			toml: `
default_style = "default"

[[styles]]
name = "mart"
include = "mart/*.sql"
[styles.options]
keyword_style = "LOWER_CASE"

[[styles]]
name = "default"
[styles.options]
keyword_style = "UPPER_CASE"

[lsp]
default_project = "styles-proj"
require_partition_filter_severity = "error"
`,
			wantKeyword: formatter.LowerCase,
			wantProject: "styles-proj",
			wantSev:     SeverityError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			cfgPath := filepath.Join(dir, ConfigFileName)
			require.NoError(t, os.WriteFile(cfgPath, []byte(tc.toml), 0o600))

			target := filepath.Join(dir, "mart", "orders.sql")

			// Test upward discovery.
			loader, err := NewLoader("")
			require.NoError(t, err)

			cfg, err := loader.ConfigFor(target, false)
			require.NoError(t, err)
			assert.Equal(t, tc.wantKeyword, cfg.PrintOptions.KeywordStyle)
			assert.Equal(t, tc.wantProject, cfg.LSP.DefaultProject)
			assert.Equal(t, tc.wantSev, cfg.LSP.RequirePartitionFilterSeverity)
			assert.Equal(t, dir, cfg.BaseDir)
			assert.Equal(t, cfgPath, cfg.ConfigPath)

			// Test explicit config path.
			explicitLoader, err := NewLoader(cfgPath)
			require.NoError(t, err)

			explicitCfg, err := explicitLoader.ConfigFor(target, false)
			require.NoError(t, err)
			assert.Equal(t, tc.wantKeyword, explicitCfg.PrintOptions.KeywordStyle)
			assert.Equal(t, tc.wantProject, explicitCfg.LSP.DefaultProject)
			assert.Equal(t, tc.wantSev, explicitCfg.LSP.RequirePartitionFilterSeverity)
			assert.Equal(t, dir, explicitCfg.BaseDir)
			assert.Equal(t, cfgPath, explicitCfg.ConfigPath)
		})
	}
}

func TestLSPFieldOverridesAndPathResolution(t *testing.T) {
	t.Parallel()

	t.Run("overrides all 8 fields and resolves relative paths against baseDir", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		workspace := filepath.Join(root, "repo")
		require.NoError(t, os.MkdirAll(workspace, 0o750))

		absSchemas := filepath.Join(root, "shared_schemas")
		cfgPath := filepath.Join(workspace, ConfigFileName)
		tomlContent := `
[lsp]
default_project = "analytics-prod"
default_dataset = "core_mart"
schema_dirs = ["./schemas", "../shared", "` + filepath.ToSlash(absSchemas) + `"]
bigquery_api_enabled = true
cache_dir = ".bqpls-cache"
cache_ttl_seconds = 3600
diagnostics_enabled = false
require_partition_filter_severity = "error"
`
		require.NoError(t, os.WriteFile(cfgPath, []byte(tomlContent), 0o600))

		loader, err := NewLoader("")
		require.NoError(t, err)

		queryFile := filepath.Join(workspace, "models", "q.sql")
		cfg, err := loader.ConfigFor(queryFile, false)
		require.NoError(t, err)

		assert.Equal(t, workspace, cfg.BaseDir)
		assert.Equal(t, cfgPath, cfg.ConfigPath)
		assert.Equal(t, "analytics-prod", cfg.LSP.DefaultProject)
		assert.Equal(t, "core_mart", cfg.LSP.DefaultDataset)
		assert.Equal(t, []string{
			filepath.Join(workspace, "schemas"),
			filepath.Join(root, "shared"),
			absSchemas,
		}, cfg.LSP.SchemaDirs)
		assert.True(t, cfg.LSP.BigQueryAPIEnabled)
		assert.Equal(t, filepath.Join(workspace, ".bqpls-cache"), cfg.LSP.CacheDir)
		assert.Equal(t, 3600, cfg.LSP.CacheTTLSeconds)
		assert.False(t, cfg.LSP.DiagnosticsEnabled)
		assert.Equal(t, SeverityError, cfg.LSP.RequirePartitionFilterSeverity)

		// Verify deep cloning: mutating returned LoadedConfig must not leak to subsequent calls.
		cfg.LSP.SchemaDirs[0] = "/mutated"
		cfg.LSP.DefaultProject = "mutated"
		cfg.PrintOptions.Indentation = 99

		cfg2, err := loader.ConfigFor(queryFile, false)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(workspace, "schemas"), cfg2.LSP.SchemaDirs[0])
		assert.Equal(t, "analytics-prod", cfg2.LSP.DefaultProject)
		assert.Equal(t, 2, cfg2.PrintOptions.Indentation)
	})

	t.Run("explicit empty cache_dir stays empty while omitted uses default", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		cfgPath := filepath.Join(dir, ConfigFileName)
		require.NoError(t, os.WriteFile(cfgPath, []byte("[lsp]\ncache_dir = \"\"\n"), 0o600))

		loader, err := NewLoader(cfgPath)
		require.NoError(t, err)

		cfg, err := loader.ConfigFor(filepath.Join(dir, "q.sql"), false)
		require.NoError(t, err)
		assert.Empty(t, cfg.LSP.CacheDir)
	})
}

func TestLSPValidationErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		toml       string
		wantSubstr string
		wantIs     error
	}{
		{
			name:       "unknown key in standalone lsp",
			toml:       "[lsp]\nunknown_lsp_key = true\n",
			wantSubstr: `unknown option "lsp.unknown_lsp_key"`,
		},
		{
			name:       "unknown key in lsp alongside flat options",
			toml:       "keyword_style = \"LOWER_CASE\"\n[lsp]\nunknown_lsp_key = true\n",
			wantSubstr: `unknown option "lsp.unknown_lsp_key"`,
		},
		{
			name:       "unknown key in lsp alongside options section",
			toml:       "[options]\nindentation = 2\n[lsp]\nbad_key = 1\n",
			wantSubstr: `unknown option "lsp.bad_key"`,
		},
		{
			name:       "unknown key in lsp alongside print_options section",
			toml:       "[print_options]\nindentation = 2\n[lsp]\nbad_key = 1\n",
			wantSubstr: `unknown option "lsp.bad_key"`,
		},
		{
			name:       "unknown key in lsp alongside styles section",
			toml:       "[[styles]]\nname = \"s\"\n[lsp]\nbad_key = 1\n",
			wantSubstr: `unknown option "lsp.bad_key"`,
		},
		{
			name:       "nested table inside lsp",
			toml:       "[lsp]\n[lsp.nested]\nx = 1\n",
			wantSubstr: `unknown option "lsp.nested`,
		},
		{
			name:       "unknown top-level key alongside valid lsp",
			toml:       "stray_key = 1\n[lsp]\ndefault_project = \"p\"\n",
			wantSubstr: `unknown option "stray_key"`,
		},
		{
			name:       "non-table lsp value",
			toml:       "lsp = \"not-a-table\"\n",
			wantSubstr: "parse config",
		},
		{
			name:       "wrong type for lsp field",
			toml:       "[lsp]\ncache_ttl_seconds = \"not-an-int\"\n",
			wantSubstr: "parse config",
		},
		{
			name:       "negative cache_ttl_seconds",
			toml:       "[lsp]\ncache_ttl_seconds = -10\n",
			wantSubstr: "invalid lsp option",
			wantIs:     ErrInvalidLSPOption,
		},
		{
			name:       "invalid require_partition_filter_severity",
			toml:       "[lsp]\nrequire_partition_filter_severity = \"critical\"\n",
			wantSubstr: "invalid lsp option",
			wantIs:     ErrInvalidLSPOption,
		},
		{
			name:       "whitespace element in schema_dirs",
			toml:       "[lsp]\nschema_dirs = [\"schemas\", \"   \"]\n",
			wantSubstr: "invalid lsp option",
			wantIs:     ErrInvalidLSPOption,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			cfgPath := filepath.Join(dir, ConfigFileName)
			require.NoError(t, os.WriteFile(cfgPath, []byte(tc.toml), 0o600))

			_, err := NewLoader(cfgPath)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSubstr)

			if tc.wantIs != nil {
				assert.ErrorIs(t, err, tc.wantIs)
			}
		})
	}
}

func TestLoaderDiscoveryCachingAndRace(t *testing.T) {
	t.Parallel()

	t.Run("missing config returns defaults and empty metadata", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		loader, err := NewLoader("")
		require.NoError(t, err)

		cfg, err := loader.ConfigFor(filepath.Join(dir, "sub", "query.sql"), false)
		require.NoError(t, err)
		assert.Empty(t, cfg.BaseDir)
		assert.Empty(t, cfg.ConfigPath)
		assert.Equal(t, formatter.DefaultPrintOptions(), cfg.PrintOptions)
		assert.Equal(t, DefaultLSPOptions(), cfg.LSP)

		opts, err := loader.OptionsFor(filepath.Join(dir, "sub", "query.sql"), false)
		require.NoError(t, err)
		assert.Equal(t, formatter.DefaultPrintOptions(), opts)
	})

	t.Run("Invalidate reloads discovered and explicit configs", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		sub := filepath.Join(dir, "nested")
		require.NoError(t, os.MkdirAll(sub, 0o750))

		cfgPath := filepath.Join(dir, ConfigFileName)
		queryPath := filepath.Join(sub, "q.sql")

		require.NoError(t, os.WriteFile(cfgPath, []byte("keyword_style = \"LOWER_CASE\"\n[lsp]\ndefault_project = \"p1\"\n"), 0o600))

		loader, err := NewLoader("")
		require.NoError(t, err)

		cfg1, err := loader.ConfigFor(queryPath, false)
		require.NoError(t, err)
		assert.Equal(t, formatter.LowerCase, cfg1.PrintOptions.KeywordStyle)
		assert.Equal(t, "p1", cfg1.LSP.DefaultProject)

		// Update file on disk; cached loader still returns old values until Invalidate.
		require.NoError(t, os.WriteFile(cfgPath, []byte("keyword_style = \"UPPER_CASE\"\n[lsp]\ndefault_project = \"p2\"\n"), 0o600))

		cached, err := loader.ConfigFor(queryPath, false)
		require.NoError(t, err)
		assert.Equal(t, "p1", cached.LSP.DefaultProject)

		require.NoError(t, loader.Invalidate())

		reloaded, err := loader.ConfigFor(queryPath, false)
		require.NoError(t, err)
		assert.Equal(t, formatter.UpperCase, reloaded.PrintOptions.KeywordStyle)
		assert.Equal(t, "p2", reloaded.LSP.DefaultProject)

		// Explicit loader Invalidate reloads and surfaces parse errors if file becomes invalid.
		explicitLoader, err := NewLoader(cfgPath)
		require.NoError(t, err)

		require.NoError(t, os.WriteFile(cfgPath, []byte("[lsp]\ndefault_project = \"p3\"\n"), 0o600))
		require.NoError(t, explicitLoader.Invalidate())

		ecfg, err := explicitLoader.ConfigFor(queryPath, false)
		require.NoError(t, err)
		assert.Equal(t, "p3", ecfg.LSP.DefaultProject)

		require.NoError(t, os.WriteFile(cfgPath, []byte("bad_key = 1\n"), 0o600))
		err = explicitLoader.Invalidate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown option "bad_key"`)
	})

	t.Run("concurrent ConfigFor, OptionsFor, and Invalidate pass race detector", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		sub1 := filepath.Join(dir, "a", "b")
		sub2 := filepath.Join(dir, "c", "d")
		require.NoError(t, os.MkdirAll(sub1, 0o750))
		require.NoError(t, os.MkdirAll(sub2, 0o750))

		cfgPath := filepath.Join(dir, ConfigFileName)
		require.NoError(t, os.WriteFile(cfgPath, []byte(`
default_style = "default"
[[styles]]
name = "a"
regex = "^a/"
[styles.options]
keyword_style = "LOWER_CASE"
[[styles]]
name = "default"
[styles.options]
keyword_style = "UPPER_CASE"
[lsp]
default_project = "race-proj"
schema_dirs = ["schemas"]
`), 0o600))

		loader, err := NewLoader("")
		require.NoError(t, err)

		explicitLoader, err := NewLoader(cfgPath)
		require.NoError(t, err)

		var wg sync.WaitGroup

		for i := range 16 {
			wg.Add(1)

			go func(idx int) {
				defer wg.Done()

				target := filepath.Join(sub1, "q.sql")
				if idx%2 == 1 {
					target = filepath.Join(sub2, "q.sql")
				}

				for j := range 20 {
					cfg, cerr := loader.ConfigFor(target, false)
					if cerr == nil && !strings.HasPrefix(cfg.LSP.DefaultProject, "race") {
						t.Errorf("unexpected default_project: %q", cfg.LSP.DefaultProject)
					}

					_, _ = explicitLoader.OptionsFor(target, false)

					if j%5 == 0 {
						_ = loader.Invalidate()
						_ = explicitLoader.Invalidate()
					}
				}
			}(i)
		}

		wg.Wait()
	})
}

func TestLoaderStdinDiscovery(t *testing.T) {
	// Uses t.Chdir, so runs sequentially.
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o750))

	cfgPath := filepath.Join(dir, ConfigFileName)
	require.NoError(t, os.WriteFile(cfgPath, []byte(`
default_style = "default"

[[styles]]
name = "sub"
include = "sub/*.sql"
[styles.options]
keyword_style = "LOWER_CASE"

[[styles]]
name = "default"
[styles.options]
keyword_style = "UPPER_CASE"

[lsp]
default_project = "stdin-proj"
`), 0o600))

	t.Chdir(sub)

	loader, err := NewLoader("")
	require.NoError(t, err)

	cfg, err := loader.ConfigFor("<standard input>", true)
	require.NoError(t, err)
	assert.Equal(t, formatter.UpperCase, cfg.PrintOptions.KeywordStyle)
	assert.Equal(t, "stdin-proj", cfg.LSP.DefaultProject)
	assert.Equal(t, cfgPath, cfg.ConfigPath)
}

func TestLSPDefinedFieldsAndMergePrecedence(t *testing.T) {
	t.Parallel()

	clientOpts := &LSPOptions{
		DefaultProject:                 "ext-proj",
		DefaultDataset:                 "ext-ds",
		SchemaDirs:                     []string{"/ext/schemas"},
		BigQueryAPIEnabled:             true,
		CacheDir:                       "/ext/cache",
		CacheTTLSeconds:                3600,
		DiagnosticsEnabled:             false,
		RequirePartitionFilterSeverity: SeverityError,
	}
	clientDef := LSPDefinedFields{
		DefaultProject:                 true,
		DefaultDataset:                 true,
		SchemaDirs:                     true,
		BigQueryAPIEnabled:             true,
		CacheDir:                       true,
		CacheTTLSeconds:                true,
		DiagnosticsEnabled:             true,
		RequirePartitionFilterSeverity: true,
	}

	t.Run("no config file uses extension settings", func(t *testing.T) {
		t.Parallel()

		merged := MergeLSPOptions(DefaultLSPOptions(), LSPDefinedFields{}, clientOpts, clientDef)
		assert.Equal(t, "ext-proj", merged.DefaultProject)
		assert.Equal(t, "ext-ds", merged.DefaultDataset)
		assert.Equal(t, []string{"/ext/schemas"}, merged.SchemaDirs)
		assert.True(t, merged.BigQueryAPIEnabled)
		assert.Equal(t, "/ext/cache", merged.CacheDir)
		assert.Equal(t, 3600, merged.CacheTTLSeconds)
		assert.False(t, merged.DiagnosticsEnabled)
		assert.Equal(t, SeverityError, merged.RequirePartitionFilterSeverity)
	})

	t.Run("formatting-only config file inherits all extension settings", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		cfgPath := filepath.Join(dir, ConfigFileName)
		require.NoError(t, os.WriteFile(cfgPath, []byte("indentation = 4\n"), 0o600))

		loader, err := NewLoader(cfgPath)
		require.NoError(t, err)

		loaded, err := loader.ConfigFor(filepath.Join(dir, "q.sql"), false)
		require.NoError(t, err)
		assert.False(t, loaded.LSPDefined.Any())

		merged := MergeLSPOptions(loaded.LSP, loaded.LSPDefined, clientOpts, clientDef)
		assert.Equal(t, "ext-proj", merged.DefaultProject)
		assert.True(t, merged.BigQueryAPIEnabled)
		assert.False(t, merged.DiagnosticsEnabled)
	})

	t.Run("config file fields override extension settings including explicit false and zero", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		cfgPath := filepath.Join(dir, ConfigFileName)
		require.NoError(t, os.WriteFile(cfgPath, []byte(`
[lsp]
default_project = "file-proj"
bigquery_api_enabled = false
cache_ttl_seconds = 0
diagnostics_enabled = true
`), 0o600))

		loader, err := NewLoader(cfgPath)
		require.NoError(t, err)

		loaded, err := loader.ConfigFor(filepath.Join(dir, "q.sql"), false)
		require.NoError(t, err)
		assert.True(t, loaded.LSPDefined.DefaultProject)
		assert.False(t, loaded.LSPDefined.DefaultDataset)
		assert.True(t, loaded.LSPDefined.BigQueryAPIEnabled)
		assert.True(t, loaded.LSPDefined.CacheTTLSeconds)
		assert.True(t, loaded.LSPDefined.DiagnosticsEnabled)
		assert.False(t, loaded.LSPDefined.RequirePartitionFilterSeverity)

		merged := MergeLSPOptions(loaded.LSP, loaded.LSPDefined, clientOpts, clientDef)
		assert.Equal(t, "file-proj", merged.DefaultProject)
		assert.Equal(t, "ext-ds", merged.DefaultDataset, "omitted file field inherits extension value")
		assert.Equal(t, []string{"/ext/schemas"}, merged.SchemaDirs, "omitted file field inherits extension value")
		assert.False(t, merged.BigQueryAPIEnabled, "explicit false in .bqfmt.toml must override extension true")
		assert.Equal(t, "/ext/cache", merged.CacheDir)
		assert.Equal(t, 0, merged.CacheTTLSeconds, "explicit 0 in .bqfmt.toml must override extension TTL")
		assert.True(t, merged.DiagnosticsEnabled, "explicit true in .bqfmt.toml must override extension false")
		assert.Equal(t, SeverityError, merged.RequirePartitionFilterSeverity)
	})
}
