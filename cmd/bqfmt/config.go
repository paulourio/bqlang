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
	explicitPath string
	mu           sync.Mutex
	explicitCfg  *parsedConfig
	dirCache     map[string]*parsedConfig
	fileCache    map[string]*parsedConfig
}

type parsedConfig struct {
	defaultStyle string
	styles       []compiledStyle
	fallback     *formatter.PrintOptions
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
		explicitPath: explicitPath,
		dirCache:     make(map[string]*parsedConfig),
		fileCache:    make(map[string]*parsedConfig),
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

	if cfg, ok := c.dirCache[startDir]; ok {
		return cfg, cfg != nil, nil
	}

	dir := startDir
	for {
		candidate := filepath.Join(dir, configFileName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			if cached, ok := c.fileCache[candidate]; ok {
				c.dirCache[startDir] = cached

				return cached, true, nil
			}

			parsed, perr := parseConfigFile(candidate)
			if perr != nil {
				return nil, false, perr
			}

			c.fileCache[candidate] = parsed
			c.dirCache[startDir] = parsed

			return parsed, true, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}

		dir = parent
	}

	c.dirCache[startDir] = nil

	return nil, false, nil
}

func (p *parsedConfig) selectOptions(filename string, isStdin bool) *formatter.PrintOptions {
	if len(p.styles) == 0 {
		return clonePrintOptions(p.fallback)
	}

	if !isStdin {
		slashPath := filepath.ToSlash(filename)
		baseName := filepath.Base(filename)

		for _, s := range p.styles {
			if s.matches(slashPath, baseName) {
				return clonePrintOptions(s.options)
			}
		}
	}

	if p.defaultStyle != "" {
		for _, s := range p.styles {
			if s.name == p.defaultStyle {
				return clonePrintOptions(s.options)
			}
		}
	}

	return clonePrintOptions(p.styles[0].options)
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

	if len(raw.Styles) > 0 {
		return buildMultiStyleConfig(path, md, &raw)
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

	if undecoded := mdFlat.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("config %s: unknown option %q", path, undecoded[0].String())
	}

	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}

	return &parsedConfig{fallback: opts}, nil
}

func buildSectionConfig(path string, md toml.MetaData, prim toml.Primitive) (*parsedConfig, error) {
	opts := formatter.DefaultPrintOptions()

	if err := md.PrimitiveDecode(prim, opts); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

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
		opts := formatter.DefaultPrintOptions()

		if entry.Options != nil {
			if err := md.PrimitiveDecode(*entry.Options, opts); err != nil {
				return nil, fmt.Errorf("config %s style %q: %w", path, entry.Name, err)
			}
		}

		if err := opts.Validate(); err != nil {
			return nil, fmt.Errorf("config %s style %q: %w", path, entry.Name, err)
		}

		var compiledRe *regexp.Regexp

		if entry.Regex != "" {
			re, err := regexp.Compile(entry.Regex)
			if err != nil {
				return nil, fmt.Errorf("config %s style %q regex: %w", path, entry.Name, err)
			}

			compiledRe = re
		}

		styles = append(styles, compiledStyle{
			name:    entry.Name,
			re:      compiledRe,
			include: entry.Include,
			options: opts,
		})
	}

	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("config %s: unknown option %q", path, undecoded[0].String())
	}

	return &parsedConfig{
		defaultStyle: raw.DefaultStyle,
		styles:       styles,
	}, nil
}
