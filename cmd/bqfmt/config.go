package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/paulourio/bqlang/formatter"
)

type configLoader struct {
	mu          sync.Mutex
	explicitCfg *parsedConfig
	dirCache    map[string]*parsedConfig
	fileCache   map[string]*parsedConfig
}

type parsedConfig struct {
	styles   []compiledStyle
	fallback *formatter.PrintOptions
}

type compiledStyle struct {
	name    string
	re      *regexp.Regexp
	include string
	options *formatter.PrintOptions
}

type rawConfigFile struct {
	DefaultStyle string          `toml:"default_style"`
	Styles       []rawStyleEntry `toml:"styles"`
	Options      *toml.Primitive `toml:"options"`
	PrintOptions *toml.Primitive `toml:"print_options"`
}

type rawStyleEntry struct {
	Name    string          `toml:"name"`
	Regex   string          `toml:"regex"`
	Include string          `toml:"include"`
	Options *toml.Primitive `toml:"options"`
}

const configFileName = ".bqfmt.toml"

func newConfigLoader(explicitPath string) (*configLoader, error) {
	cl := &configLoader{
		dirCache:  make(map[string]*parsedConfig),
		fileCache: make(map[string]*parsedConfig),
	}

	if explicitPath != "" {
		cfg, err := parseConfigFile(explicitPath)
		if err != nil {
			return nil, err
		}

		cl.explicitCfg = cfg
	}

	return cl, nil
}

func (c *configLoader) optionsFor(filename string, isStdin bool) (*formatter.PrintOptions, error) {
	if c.explicitCfg != nil {
		return c.explicitCfg.selectOptions(filename, isStdin), nil
	}

	var startDir string

	if isStdin {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getwd: %w", err)
		}

		startDir = wd
	} else {
		absPath, err := filepath.Abs(filename)
		if err != nil {
			startDir = filepath.Dir(filename)
		} else {
			startDir = filepath.Dir(absPath)
		}
	}

	cfg, found, err := c.discoverFromDir(startDir)
	if err != nil {
		return nil, err
	}

	if !found {
		return formatter.DefaultPrintOptions(), nil
	}

	return cfg.selectOptions(filename, isStdin), nil
}

func (c *configLoader) discoverFromDir(startDir string) (*parsedConfig, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var visited []string

	dir := startDir
	for {
		if cfg, ok := c.dirCache[dir]; ok {
			c.cacheDirs(visited, cfg)

			return cfg, cfg != nil, nil
		}

		visited = append(visited, dir)

		candidate := filepath.Join(dir, configFileName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			parsed, ok := c.fileCache[candidate]
			if !ok {
				var perr error

				parsed, perr = parseConfigFile(candidate)
				if perr != nil {
					return nil, false, perr
				}

				c.fileCache[candidate] = parsed
			}

			c.cacheDirs(visited, parsed)

			return parsed, true, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}

		dir = parent
	}

	c.cacheDirs(visited, nil)

	return nil, false, nil
}

func (c *configLoader) cacheDirs(dirs []string, cfg *parsedConfig) {
	for _, d := range dirs {
		c.dirCache[d] = cfg
	}
}

func (p *parsedConfig) selectOptions(filename string, isStdin bool) *formatter.PrintOptions {
	if !isStdin && len(p.styles) > 0 {
		slashPath := filepath.ToSlash(filename)
		baseName := filepath.Base(filename)

		for _, s := range p.styles {
			if s.matches(slashPath, baseName) {
				return clonePrintOptions(s.options)
			}
		}
	}

	return clonePrintOptions(p.fallback)
}

func (s compiledStyle) matches(slashPath, baseName string) bool {
	if s.re != nil && s.re.MatchString(slashPath) {
		return true
	}

	if s.include != "" {
		if matched, err := filepath.Match(s.include, baseName); err == nil && matched {
			return true
		}
	}

	return false
}

func clonePrintOptions(src *formatter.PrintOptions) *formatter.PrintOptions {
	if src == nil {
		return formatter.DefaultPrintOptions()
	}

	cp := *src

	return &cp
}

func parseConfigFile(path string) (*parsedConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	content := string(data)

	var raw rawConfigFile

	md, err := toml.Decode(content, &raw)
	if err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	sections := 0
	if len(raw.Styles) > 0 {
		sections++
	}

	if raw.Options != nil {
		sections++
	}

	if raw.PrintOptions != nil {
		sections++
	}

	if sections > 1 {
		return nil, fmt.Errorf(
			"config %s: conflicting configuration sections; specify at most one of styles, options, or print_options",
			path,
		)
	}

	if len(raw.Styles) > 0 {
		return buildMultiStyleConfig(path, md, &raw)
	}

	if raw.DefaultStyle != "" {
		return nil, fmt.Errorf("config %s: default_style requires a styles section", path)
	}

	if raw.Options != nil {
		return buildSectionConfig(path, md, *raw.Options)
	}

	if raw.PrintOptions != nil {
		return buildSectionConfig(path, md, *raw.PrintOptions)
	}

	opts := formatter.DefaultPrintOptions()

	mdFlat, err := toml.Decode(content, opts)
	if err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	return finalizeSingleStyleConfig(path, mdFlat, opts)
}

func buildSectionConfig(path string, md toml.MetaData, prim toml.Primitive) (*parsedConfig, error) {
	opts := formatter.DefaultPrintOptions()

	if err := md.PrimitiveDecode(prim, opts); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	return finalizeSingleStyleConfig(path, md, opts)
}

func finalizeSingleStyleConfig(
	path string,
	md toml.MetaData,
	opts *formatter.PrintOptions,
) (*parsedConfig, error) {
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("config %s: unknown option %q", path, undecoded[0].String())
	}

	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}

	return &parsedConfig{fallback: opts}, nil
}

func buildMultiStyleConfig(path string, md toml.MetaData, raw *rawConfigFile) (*parsedConfig, error) {
	styles := make([]compiledStyle, 0, len(raw.Styles))

	for _, entry := range raw.Styles {
		compiled, err := compileStyleEntry(path, md, entry)
		if err != nil {
			return nil, err
		}

		styles = append(styles, compiled)
	}

	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("config %s: unknown option %q", path, undecoded[0].String())
	}

	fallback := styles[0].options
	if raw.DefaultStyle != "" {
		opt, ok := findNamedStyleOptions(styles, raw.DefaultStyle)
		if !ok {
			return nil, fmt.Errorf("config %s: default_style %q not found in styles", path, raw.DefaultStyle)
		}

		fallback = opt
	}

	return &parsedConfig{
		styles:   styles,
		fallback: fallback,
	}, nil
}

func compileStyleEntry(path string, md toml.MetaData, entry rawStyleEntry) (compiledStyle, error) {
	opts := formatter.DefaultPrintOptions()

	if entry.Options != nil {
		if err := md.PrimitiveDecode(*entry.Options, opts); err != nil {
			return compiledStyle{}, fmt.Errorf("config %s style %q: %w", path, entry.Name, err)
		}
	}

	if err := opts.Validate(); err != nil {
		return compiledStyle{}, fmt.Errorf("config %s style %q: %w", path, entry.Name, err)
	}

	var compiledRe *regexp.Regexp

	if entry.Regex != "" {
		re, err := regexp.Compile(entry.Regex)
		if err != nil {
			return compiledStyle{}, fmt.Errorf("config %s style %q regex: %w", path, entry.Name, err)
		}

		compiledRe = re
	}

	if entry.Include != "" {
		if _, err := filepath.Match(entry.Include, ""); err != nil {
			return compiledStyle{}, fmt.Errorf("config %s style %q include: %w", path, entry.Name, err)
		}
	}

	return compiledStyle{
		name:    entry.Name,
		re:      compiledRe,
		include: entry.Include,
		options: opts,
	}, nil
}

func findNamedStyleOptions(styles []compiledStyle, name string) (*formatter.PrintOptions, bool) {
	for _, s := range styles {
		if s.name == name {
			return s.options, true
		}
	}

	return nil, false
}
