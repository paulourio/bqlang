package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/paulourio/bqlang/formatter"
)

type configLoader struct {
	mu          sync.Mutex
	explicitCfg *parsedConfig
	dirCache    map[string]*parsedConfig
	fileCache   map[string]cachedConfigFile
}

type cachedConfigFile struct {
	cfg *parsedConfig
	err error
}

type parsedConfig struct {
	baseDir  string
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
		fileCache: make(map[string]cachedConfigFile),
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
			entry, ok := c.fileCache[candidate]
			if !ok {
				parsed, perr := parseConfigFile(candidate)
				entry = cachedConfigFile{cfg: parsed, err: perr}
				c.fileCache[candidate] = entry
			}

			if entry.err != nil {
				return nil, false, entry.err
			}

			c.cacheDirs(visited, entry.cfg)

			return entry.cfg, true, nil
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
		matchPath := p.matchPathFor(filename)
		baseName := filepath.Base(filename)

		for _, s := range p.styles {
			if s.matches(matchPath, baseName) {
				return clonePrintOptions(s.options)
			}
		}
	}

	return clonePrintOptions(p.fallback)
}

func (p *parsedConfig) matchPathFor(filename string) string {
	if p.baseDir != "" {
		if absFile, err := filepath.Abs(filename); err == nil {
			rel, relErr := filepath.Rel(p.baseDir, absFile)
			if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return filepath.ToSlash(rel)
			}
		}
	}

	return filepath.ToSlash(filename)
}

func (s compiledStyle) matches(matchPath, baseName string) bool {
	if s.re != nil && s.re.MatchString(matchPath) {
		return true
	}

	if s.include != "" {
		target := baseName
		if strings.Contains(s.include, "/") {
			target = matchPath
		}

		if matched, err := path.Match(s.include, target); err == nil && matched {
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

func parseConfigFile(cfgPath string) (*parsedConfig, error) {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", cfgPath, err)
	}

	baseDir := ""
	if absPath, absErr := filepath.Abs(cfgPath); absErr == nil {
		baseDir = filepath.Dir(absPath)
	}

	content := string(data)

	var raw rawConfigFile

	md, err := toml.Decode(content, &raw)
	if err != nil {
		return nil, fmt.Errorf("parse config %s: %w", cfgPath, err)
	}

	hasStyles := md.IsDefined("styles")

	sections := 0
	if hasStyles {
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
			cfgPath,
		)
	}

	if hasStyles {
		if len(raw.Styles) == 0 {
			return nil, fmt.Errorf("config %s: styles section must define at least one style", cfgPath)
		}

		return buildMultiStyleConfig(cfgPath, baseDir, md, &raw)
	}

	if raw.DefaultStyle != "" {
		return nil, fmt.Errorf("config %s: default_style requires a styles section", cfgPath)
	}

	if raw.Options != nil {
		return buildSectionConfig(cfgPath, baseDir, md, *raw.Options)
	}

	if raw.PrintOptions != nil {
		return buildSectionConfig(cfgPath, baseDir, md, *raw.PrintOptions)
	}

	opts := formatter.DefaultPrintOptions()

	mdFlat, err := toml.Decode(content, opts)
	if err != nil {
		return nil, fmt.Errorf("parse config %s: %w", cfgPath, err)
	}

	return finalizeSingleStyleConfig(cfgPath, baseDir, mdFlat, opts)
}

func buildSectionConfig(cfgPath, baseDir string, md toml.MetaData, prim toml.Primitive) (*parsedConfig, error) {
	opts := formatter.DefaultPrintOptions()

	if err := md.PrimitiveDecode(prim, opts); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", cfgPath, err)
	}

	return finalizeSingleStyleConfig(cfgPath, baseDir, md, opts)
}

func finalizeSingleStyleConfig(
	cfgPath string,
	baseDir string,
	md toml.MetaData,
	opts *formatter.PrintOptions,
) (*parsedConfig, error) {
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("config %s: unknown option %q", cfgPath, undecoded[0].String())
	}

	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", cfgPath, err)
	}

	return &parsedConfig{baseDir: baseDir, fallback: opts}, nil
}

func buildMultiStyleConfig(cfgPath, baseDir string, md toml.MetaData, raw *rawConfigFile) (*parsedConfig, error) {
	styles := make([]compiledStyle, 0, len(raw.Styles))
	seenNames := make(map[string]struct{}, len(raw.Styles))

	for _, entry := range raw.Styles {
		if entry.Name != "" {
			if _, exists := seenNames[entry.Name]; exists {
				return nil, fmt.Errorf("config %s: duplicate style name %q", cfgPath, entry.Name)
			}

			seenNames[entry.Name] = struct{}{}
		}

		compiled, err := compileStyleEntry(cfgPath, md, entry)
		if err != nil {
			return nil, err
		}

		styles = append(styles, compiled)
	}

	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("config %s: unknown option %q", cfgPath, undecoded[0].String())
	}

	fallback := styles[0].options
	if raw.DefaultStyle != "" {
		opt, ok := findNamedStyleOptions(styles, raw.DefaultStyle)
		if !ok {
			return nil, fmt.Errorf("config %s: default_style %q not found in styles", cfgPath, raw.DefaultStyle)
		}

		fallback = opt
	}

	return &parsedConfig{
		baseDir:  baseDir,
		styles:   styles,
		fallback: fallback,
	}, nil
}

func compileStyleEntry(cfgPath string, md toml.MetaData, entry rawStyleEntry) (compiledStyle, error) {
	opts := formatter.DefaultPrintOptions()

	if entry.Options != nil {
		if err := md.PrimitiveDecode(*entry.Options, opts); err != nil {
			return compiledStyle{}, fmt.Errorf("config %s style %q: %w", cfgPath, entry.Name, err)
		}
	}

	if err := opts.Validate(); err != nil {
		return compiledStyle{}, fmt.Errorf("config %s style %q: %w", cfgPath, entry.Name, err)
	}

	var compiledRe *regexp.Regexp

	if entry.Regex != "" {
		re, err := regexp.Compile(entry.Regex)
		if err != nil {
			return compiledStyle{}, fmt.Errorf("config %s style %q regex: %w", cfgPath, entry.Name, err)
		}

		compiledRe = re
	}

	if entry.Include != "" {
		if _, err := path.Match(entry.Include, ""); err != nil {
			return compiledStyle{}, fmt.Errorf("config %s style %q include: %w", cfgPath, entry.Name, err)
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
