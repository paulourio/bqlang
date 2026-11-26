// Package bqconfig discovers, parses, validates, and caches .bqfmt.toml
// configuration files for both bqfmt and bqpls.
package bqconfig

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/paulourio/bqlang/formatter"
)

// ConfigFileName is the filename searched upward when discovering configuration.
const ConfigFileName = ".bqfmt.toml"

const (
	defaultCacheTTLSeconds  = 86400
	defaultPricePerTiBUSD   = 6.25
	defaultDryRunDebounceMs = 800
)

// ErrInvalidLSPOption is returned when an [lsp] setting has an invalid value.
var ErrInvalidLSPOption = errors.New("invalid lsp option")

// Severity represents a diagnostic severity level for LSP rules.
type Severity string

// Supported Severity levels.
const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
	SeverityHint    Severity = "hint"
	SeverityOff     Severity = "off"
)

// IsValid reports whether s is a recognized diagnostic severity.
func (s Severity) IsValid() bool {
	switch s {
	case SeverityError, SeverityWarning, SeverityInfo, SeverityHint, SeverityOff:
		return true
	default:
		return false
	}
}

// LSPOptions configures the BigQuery language server ([lsp] table in .bqfmt.toml).
type LSPOptions struct {
	DefaultProject                 string         `toml:"default_project" json:"default_project"`
	DefaultDataset                 string         `toml:"default_dataset" json:"default_dataset"`
	SchemaDirs                     []string       `toml:"schema_dirs" json:"schema_dirs"`
	BigQueryAPIEnabled             bool           `toml:"bigquery_api_enabled" json:"bigquery_api_enabled"`
	GcloudPath                     string         `toml:"gcloud_path" json:"gcloud_path"`
	CacheDir                       string         `toml:"cache_dir" json:"cache_dir"`
	CacheTTLSeconds                int            `toml:"cache_ttl_seconds" json:"cache_ttl_seconds"`
	ProjectCacheTTLSeconds         map[string]int `toml:"project_cache_ttl_seconds" json:"project_cache_ttl_seconds"`
	DiagnosticsEnabled             bool           `toml:"diagnostics_enabled" json:"diagnostics_enabled"`
	RequirePartitionFilterSeverity Severity       `toml:"require_partition_filter_severity" json:"require_partition_filter_severity"`
	MissingPartitionFilterSeverity Severity       `toml:"missing_partition_filter_severity" json:"missing_partition_filter_severity"`
	ClusteringFilterSeverity       Severity       `toml:"clustering_filter_severity" json:"clustering_filter_severity"`
	CostEstimationEnabled          bool           `toml:"cost_estimation_enabled" json:"cost_estimation_enabled"`
	CostCodeLensEnabled            bool           `toml:"cost_codelens_enabled" json:"cost_codelens_enabled"`
	CostStatusBarEnabled           bool           `toml:"cost_status_bar_enabled" json:"cost_status_bar_enabled"`
	CostHoverEnabled               bool           `toml:"cost_hover_enabled" json:"cost_hover_enabled"`
	CostDiagnosticSeverity         Severity       `toml:"cost_diagnostic_severity" json:"cost_diagnostic_severity"`
	CostWarningThresholdBytes      int64          `toml:"cost_warning_threshold_bytes" json:"cost_warning_threshold_bytes"`
	CostWarningThresholdUSD        float64        `toml:"cost_warning_threshold_usd" json:"cost_warning_threshold_usd"`
	CostPricePerTiBUSD             float64        `toml:"cost_price_per_tib_usd" json:"cost_price_per_tib_usd"`
	CostDryRunDebounceMs           int            `toml:"cost_dry_run_debounce_ms" json:"cost_dry_run_debounce_ms"`
}

// DefaultLSPOptions returns the default language server configuration.
func DefaultLSPOptions() *LSPOptions {
	return &LSPOptions{
		CacheDir:                       defaultCacheDir(),
		CacheTTLSeconds:                defaultCacheTTLSeconds,
		DiagnosticsEnabled:             true,
		RequirePartitionFilterSeverity: SeverityWarning,
		MissingPartitionFilterSeverity: SeverityWarning,
		ClusteringFilterSeverity:       SeverityWarning,
		CostEstimationEnabled:          true,
		CostCodeLensEnabled:            true,
		CostStatusBarEnabled:           true,
		CostHoverEnabled:               true,
		CostDiagnosticSeverity:         SeverityWarning,
		CostPricePerTiBUSD:             defaultPricePerTiBUSD,
		CostDryRunDebounceMs:           defaultDryRunDebounceMs,
	}
}

func defaultCacheDir() string {
	userCacheDir, err := os.UserCacheDir()
	if err != nil || userCacheDir == "" {
		userCacheDir = os.TempDir()
	}

	return filepath.Join(userCacheDir, "bqpls")
}

// Validate checks that all LSPOptions fields hold valid values.
func (o *LSPOptions) Validate() error {
	if o == nil {
		return nil
	}

	if o.CacheTTLSeconds < 0 {
		return fmt.Errorf("Validate cache_ttl_seconds=%d: %w", o.CacheTTLSeconds, ErrInvalidLSPOption)
	}

	if err := o.validateProjectCacheTTLs(); err != nil {
		return err
	}

	if err := o.validateLintOptions(); err != nil {
		return err
	}

	if err := o.validateCostOptions(); err != nil {
		return err
	}

	for i, dir := range o.SchemaDirs {
		if strings.TrimSpace(dir) == "" {
			return fmt.Errorf("Validate schema_dirs[%d]=%q: %w", i, dir, ErrInvalidLSPOption)
		}
	}

	return nil
}

func (o *LSPOptions) validateLintOptions() error {
	if !o.RequirePartitionFilterSeverity.IsValid() {
		return fmt.Errorf(
			"Validate require_partition_filter_severity=%q: %w",
			o.RequirePartitionFilterSeverity,
			ErrInvalidLSPOption,
		)
	}

	if o.MissingPartitionFilterSeverity != "" && !o.MissingPartitionFilterSeverity.IsValid() {
		return fmt.Errorf(
			"Validate missing_partition_filter_severity=%q: %w",
			o.MissingPartitionFilterSeverity,
			ErrInvalidLSPOption,
		)
	}

	if o.ClusteringFilterSeverity != "" && !o.ClusteringFilterSeverity.IsValid() {
		return fmt.Errorf(
			"Validate clustering_filter_severity=%q: %w",
			o.ClusteringFilterSeverity,
			ErrInvalidLSPOption,
		)
	}

	return nil
}

func (o *LSPOptions) validateCostOptions() error {
	if o.CostDiagnosticSeverity != "" && !o.CostDiagnosticSeverity.IsValid() {
		return fmt.Errorf(
			"Validate cost_diagnostic_severity=%q: %w",
			o.CostDiagnosticSeverity,
			ErrInvalidLSPOption,
		)
	}

	if o.CostWarningThresholdBytes < 0 {
		return fmt.Errorf(
			"Validate cost_warning_threshold_bytes=%d: %w",
			o.CostWarningThresholdBytes,
			ErrInvalidLSPOption,
		)
	}

	if o.CostWarningThresholdUSD < 0 || math.IsNaN(o.CostWarningThresholdUSD) || math.IsInf(o.CostWarningThresholdUSD, 0) {
		return fmt.Errorf(
			"Validate cost_warning_threshold_usd=%v: %w",
			o.CostWarningThresholdUSD,
			ErrInvalidLSPOption,
		)
	}

	if o.CostPricePerTiBUSD < 0 || math.IsNaN(o.CostPricePerTiBUSD) || math.IsInf(o.CostPricePerTiBUSD, 0) {
		return fmt.Errorf(
			"Validate cost_price_per_tib_usd=%v: %w",
			o.CostPricePerTiBUSD,
			ErrInvalidLSPOption,
		)
	}

	if o.CostDryRunDebounceMs < 0 {
		return fmt.Errorf(
			"Validate cost_dry_run_debounce_ms=%d: %w",
			o.CostDryRunDebounceMs,
			ErrInvalidLSPOption,
		)
	}

	return nil
}

func (o *LSPOptions) validateProjectCacheTTLs() error {
	for proj, ttl := range o.ProjectCacheTTLSeconds {
		if strings.TrimSpace(proj) == "" {
			return fmt.Errorf("Validate project_cache_ttl_seconds[%q]: %w", proj, ErrInvalidLSPOption)
		}

		if ttl < 0 {
			return fmt.Errorf("Validate project_cache_ttl_seconds[%q]=%d: %w", proj, ttl, ErrInvalidLSPOption)
		}
	}

	return nil
}

// EffectiveCacheTTL returns the effective metadata cache TTL for project.
// Per-project entries extend (never shrink) the base CacheTTLSeconds value.
func (o *LSPOptions) EffectiveCacheTTL(project string) time.Duration {
	if o == nil {
		return time.Duration(defaultCacheTTLSeconds) * time.Second
	}

	seconds := max(0, o.CacheTTLSeconds)
	p := strings.ToLower(strings.TrimSpace(project))
	if p == "" || len(o.ProjectCacheTTLSeconds) == 0 {
		return time.Duration(seconds) * time.Second
	}

	if ext, ok := o.ProjectCacheTTLSeconds[p]; ok {
		return time.Duration(max(seconds, ext)) * time.Second
	}

	for k, v := range o.ProjectCacheTTLSeconds {
		if strings.EqualFold(strings.TrimSpace(k), p) && v > seconds {
			seconds = v
		}
	}

	return time.Duration(seconds) * time.Second
}

// NormalizeProjectCacheTTLs returns a normalized copy of src with lowercase
// trimmed project keys, retaining the maximum TTL on case-insensitive collisions.
func NormalizeProjectCacheTTLs(src map[string]int) map[string]int {
	if len(src) == 0 {
		return nil
	}

	var out map[string]int
	ExtendProjectCacheTTLs(&out, src)

	return out
}

// ExtendProjectCacheTTLs additively merges src into *dst using normalized
// lowercase project keys and retaining max(existing, incoming) on key collisions.
func ExtendProjectCacheTTLs(dst *map[string]int, src map[string]int) {
	if dst == nil || len(src) == 0 {
		return
	}

	if *dst == nil {
		*dst = make(map[string]int, len(src))
	}

	for rawProj, ttl := range src {
		proj := strings.ToLower(strings.TrimSpace(rawProj))
		if proj == "" || ttl < 0 {
			continue
		}

		if cur, exists := (*dst)[proj]; !exists || ttl > cur {
			(*dst)[proj] = ttl
		}
	}
}

// LSPDefinedFields tracks which [lsp] fields were explicitly specified in a
// config file or editor settings payload so per-field precedence can be applied.
type LSPDefinedFields struct {
	DefaultProject                 bool
	DefaultDataset                 bool
	SchemaDirs                     bool
	BigQueryAPIEnabled             bool
	GcloudPath                     bool
	CacheDir                       bool
	CacheTTLSeconds                bool
	ProjectCacheTTLSeconds         bool
	DiagnosticsEnabled             bool
	RequirePartitionFilterSeverity bool
	MissingPartitionFilterSeverity bool
	ClusteringFilterSeverity       bool
	CostEstimationEnabled          bool
	CostCodeLensEnabled            bool
	CostStatusBarEnabled           bool
	CostHoverEnabled               bool
	CostDiagnosticSeverity         bool
	CostWarningThresholdBytes      bool
	CostWarningThresholdUSD        bool
	CostPricePerTiBUSD             bool
	CostDryRunDebounceMs           bool
}

// Any reports whether at least one LSP field was explicitly defined.
func (d LSPDefinedFields) Any() bool {
	return d.DefaultProject ||
		d.DefaultDataset ||
		d.SchemaDirs ||
		d.BigQueryAPIEnabled ||
		d.GcloudPath ||
		d.CacheDir ||
		d.CacheTTLSeconds ||
		d.ProjectCacheTTLSeconds ||
		d.DiagnosticsEnabled ||
		d.RequirePartitionFilterSeverity ||
		d.MissingPartitionFilterSeverity ||
		d.ClusteringFilterSeverity ||
		d.anyCostField()
}

func (d LSPDefinedFields) anyCostField() bool {
	return d.CostEstimationEnabled ||
		d.CostCodeLensEnabled ||
		d.CostStatusBarEnabled ||
		d.CostHoverEnabled ||
		d.CostDiagnosticSeverity ||
		d.CostWarningThresholdBytes ||
		d.CostWarningThresholdUSD ||
		d.CostPricePerTiBUSD ||
		d.CostDryRunDebounceMs
}

// MergeLSPOptions layers fileOpts (highest priority, per fileDefined) over
// clientOpts (editor/extension settings, per clientDefined) over DefaultLSPOptions.
func MergeLSPOptions(
	fileOpts *LSPOptions,
	fileDefined LSPDefinedFields,
	clientOpts *LSPOptions,
	clientDefined LSPDefinedFields,
) *LSPOptions {
	if clientOpts == nil || !clientDefined.Any() {
		return cloneLSPOptions(fileOpts)
	}

	return MergeLSPOptionsOver(DefaultLSPOptions(), fileOpts, fileDefined, clientOpts, clientDefined)
}

// MergeLSPOptionsOver layers fileOpts (highest priority, per fileDefined) over
// clientOpts (editor/extension settings, per clientDefined) over base instead of
// DefaultLSPOptions.
func MergeLSPOptionsOver(
	base *LSPOptions,
	fileOpts *LSPOptions,
	fileDefined LSPDefinedFields,
	clientOpts *LSPOptions,
	clientDefined LSPDefinedFields,
) *LSPOptions {
	out := cloneLSPOptions(base)
	if clientOpts != nil && clientDefined.Any() {
		applyDefinedLSPOptions(out, clientOpts, clientDefined)
	}

	if fileOpts != nil && fileDefined.Any() {
		applyDefinedLSPOptions(out, fileOpts, fileDefined)
	}

	return out
}

func applyDefinedLSPOptions(dst, src *LSPOptions, def LSPDefinedFields) {
	if src == nil {
		return
	}

	if def.DefaultProject {
		dst.DefaultProject = src.DefaultProject
	}

	if def.DefaultDataset {
		dst.DefaultDataset = src.DefaultDataset
	}

	if def.SchemaDirs {
		dst.SchemaDirs = slices.Clone(src.SchemaDirs)
	}

	if def.BigQueryAPIEnabled {
		dst.BigQueryAPIEnabled = src.BigQueryAPIEnabled
	}

	if def.GcloudPath {
		dst.GcloudPath = src.GcloudPath
	}

	if def.CacheDir {
		dst.CacheDir = src.CacheDir
	}

	if def.CacheTTLSeconds {
		dst.CacheTTLSeconds = src.CacheTTLSeconds
	}

	if def.ProjectCacheTTLSeconds {
		ExtendProjectCacheTTLs(&dst.ProjectCacheTTLSeconds, src.ProjectCacheTTLSeconds)
	}

	if def.DiagnosticsEnabled {
		dst.DiagnosticsEnabled = src.DiagnosticsEnabled
	}

	applyDefinedLintLSPOptions(dst, src, def)
	applyDefinedCostLSPOptions(dst, src, def)
}

func applyDefinedLintLSPOptions(dst, src *LSPOptions, def LSPDefinedFields) {
	if def.RequirePartitionFilterSeverity {
		dst.RequirePartitionFilterSeverity = src.RequirePartitionFilterSeverity
	}

	if def.MissingPartitionFilterSeverity {
		dst.MissingPartitionFilterSeverity = src.MissingPartitionFilterSeverity
	}

	if def.ClusteringFilterSeverity {
		dst.ClusteringFilterSeverity = src.ClusteringFilterSeverity
	}
}

func applyDefinedCostLSPOptions(dst, src *LSPOptions, def LSPDefinedFields) {
	if def.CostEstimationEnabled {
		dst.CostEstimationEnabled = src.CostEstimationEnabled
	}

	if def.CostCodeLensEnabled {
		dst.CostCodeLensEnabled = src.CostCodeLensEnabled
	}

	if def.CostStatusBarEnabled {
		dst.CostStatusBarEnabled = src.CostStatusBarEnabled
	}

	if def.CostHoverEnabled {
		dst.CostHoverEnabled = src.CostHoverEnabled
	}

	if def.CostDiagnosticSeverity {
		dst.CostDiagnosticSeverity = src.CostDiagnosticSeverity
	}

	if def.CostWarningThresholdBytes {
		dst.CostWarningThresholdBytes = src.CostWarningThresholdBytes
	}

	if def.CostWarningThresholdUSD {
		dst.CostWarningThresholdUSD = src.CostWarningThresholdUSD
	}

	if def.CostPricePerTiBUSD {
		dst.CostPricePerTiBUSD = src.CostPricePerTiBUSD
	}

	if def.CostDryRunDebounceMs {
		dst.CostDryRunDebounceMs = src.CostDryRunDebounceMs
	}
}

// LoadedConfig contains the resolved formatting and LSP options for a target file.
type LoadedConfig struct {
	PrintOptions *formatter.PrintOptions
	LSP          *LSPOptions
	LSPDefined   LSPDefinedFields
	BaseDir      string
	ConfigPath   string
}

var bqlSuffixes = []string{
	".bql",
	".sql",
	".bql.j2",
	".sql.j2",
	".bql.tpl",
	".sql.tpl",
}

// IsBQLFilename reports whether name has a supported BigQuery SQL or template filename suffix.
func IsBQLFilename(name string) bool {
	if name == "" || strings.HasSuffix(name, "/") || strings.HasSuffix(name, string(filepath.Separator)) {
		return false
	}

	base := filepath.Base(name)
	if base == "." || base == ".." {
		return false
	}

	for _, suffix := range bqlSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}

	return false
}

// Loader discovers, parses, and caches .bqfmt.toml configurations across files.
type Loader struct {
	mu           sync.Mutex
	explicitPath string
	explicitCfg  *parsedConfig
	dirCache     map[string]*parsedConfig
	fileCache    map[string]cachedConfigFile
}

type cachedConfigFile struct {
	cfg *parsedConfig
	err error
}

type parsedConfig struct {
	configPath string
	baseDir    string
	styles     []compiledStyle
	fallback   *formatter.PrintOptions
	lsp        *LSPOptions
	lspDefined LSPDefinedFields
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
	LSP          *toml.Primitive `toml:"lsp"`
}

type rawStyleEntry struct {
	Name    string          `toml:"name"`
	Regex   string          `toml:"regex"`
	Include string          `toml:"include"`
	Options *toml.Primitive `toml:"options"`
}

// NewLoader creates a configuration loader, optionally pinned to explicitPath.
func NewLoader(explicitPath string) (*Loader, error) {
	l := &Loader{
		explicitPath: explicitPath,
		dirCache:     make(map[string]*parsedConfig),
		fileCache:    make(map[string]cachedConfigFile),
	}

	if explicitPath != "" {
		cfg, err := parseConfigFile(explicitPath)
		if err != nil {
			return nil, err
		}

		l.explicitCfg = cfg
	}

	return l, nil
}

// Invalidate clears cached directory and file discoveries and reloads explicitPath if configured.
func (l *Loader) Invalidate() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	clear(l.dirCache)
	clear(l.fileCache)

	if l.explicitPath != "" {
		cfg, err := parseConfigFile(l.explicitPath)
		if err != nil {
			return err
		}

		l.explicitCfg = cfg
	}

	return nil
}

// ConfigFor returns the resolved formatting and LSP configuration for filename.
func (l *Loader) ConfigFor(filename string, isStdin bool) (*LoadedConfig, error) {
	l.mu.Lock()
	explicit := l.explicitCfg
	l.mu.Unlock()

	if explicit != nil {
		return explicit.selectConfig(filename, isStdin), nil
	}

	startDir, err := resolveStartDir(filename, isStdin)
	if err != nil {
		return nil, err
	}

	cfg, found, err := l.discoverFromDir(startDir)
	if err != nil {
		return nil, err
	}

	if !found {
		return &LoadedConfig{
			PrintOptions: formatter.DefaultPrintOptions(),
			LSP:          DefaultLSPOptions(),
		}, nil
	}

	return cfg.selectConfig(filename, isStdin), nil
}

// OptionsFor returns the resolved formatter PrintOptions for filename.
func (l *Loader) OptionsFor(filename string, isStdin bool) (*formatter.PrintOptions, error) {
	cfg, err := l.ConfigFor(filename, isStdin)
	if err != nil {
		return nil, err
	}

	return cfg.PrintOptions, nil
}

func resolveStartDir(filename string, isStdin bool) (string, error) {
	if isStdin {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getwd: %w", err)
		}

		return wd, nil
	}

	return resolveFileDir(filename), nil
}

func resolveFileDir(filename string) string {
	absPath, err := filepath.Abs(filename)
	if err != nil {
		return filepath.Dir(filename)
	}

	return filepath.Dir(absPath)
}

func (l *Loader) discoverFromDir(startDir string) (*parsedConfig, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var visited []string

	dir := startDir
	for {
		if cfg, ok := l.dirCache[dir]; ok {
			l.cacheDirs(visited, cfg)

			return cfg, cfg != nil, nil
		}

		visited = append(visited, dir)

		candidate := filepath.Join(dir, ConfigFileName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			entry, ok := l.fileCache[candidate]
			if !ok {
				parsed, perr := parseConfigFile(candidate)
				entry = cachedConfigFile{cfg: parsed, err: perr}
				l.fileCache[candidate] = entry
			}

			if entry.err != nil {
				return nil, false, entry.err
			}

			cCfg := entry.cfg
			l.cacheDirs(visited, cCfg)

			return cCfg, true, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}

		dir = parent
	}

	l.cacheDirs(visited, nil)

	return nil, false, nil
}

func (l *Loader) cacheDirs(dirs []string, cfg *parsedConfig) {
	for _, d := range dirs {
		l.dirCache[d] = cfg
	}
}

func (p *parsedConfig) selectConfig(filename string, isStdin bool) *LoadedConfig {
	return &LoadedConfig{
		PrintOptions: p.selectOptions(filename, isStdin),
		LSP:          cloneLSPOptions(p.lsp),
		LSPDefined:   p.lspDefined,
		BaseDir:      p.baseDir,
		ConfigPath:   p.configPath,
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

func cloneLSPOptions(src *LSPOptions) *LSPOptions {
	if src == nil {
		return DefaultLSPOptions()
	}

	cp := *src
	cp.SchemaDirs = slices.Clone(src.SchemaDirs)
	cp.ProjectCacheTTLSeconds = NormalizeProjectCacheTTLs(src.ProjectCacheTTLSeconds)

	return &cp
}

func parseConfigFile(cfgPath string) (*parsedConfig, error) {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", cfgPath, err)
	}

	configPath := cfgPath
	baseDir := ""

	if absPath, absErr := filepath.Abs(cfgPath); absErr == nil {
		configPath = absPath
		baseDir = filepath.Dir(absPath)
	}

	content := string(data)

	var raw rawConfigFile

	md, err := toml.Decode(content, &raw)
	if err != nil {
		return nil, fmt.Errorf("parse config %s: %w", cfgPath, err)
	}

	hasStyles := md.IsDefined("styles")
	if cerr := checkConflictingSections(cfgPath, hasStyles, &raw); cerr != nil {
		return nil, cerr
	}

	if hasStyles {
		if len(raw.Styles) == 0 {
			return nil, fmt.Errorf("config %s: styles section must define at least one style", cfgPath)
		}

		lspOpts, lspDef, lerr := decodeLSPOptions(cfgPath, baseDir, md, raw.LSP)
		if lerr != nil {
			return nil, lerr
		}

		return buildMultiStyleConfig(cfgPath, configPath, baseDir, md, &raw, lspOpts, lspDef)
	}

	if raw.DefaultStyle != "" {
		return nil, fmt.Errorf("config %s: default_style requires a styles section", cfgPath)
	}

	lspOpts, lspDef, err := decodeLSPOptions(cfgPath, baseDir, md, raw.LSP)
	if err != nil {
		return nil, err
	}

	if raw.Options != nil {
		return buildSectionConfig(cfgPath, configPath, baseDir, md, *raw.Options, lspOpts, lspDef)
	}

	if raw.PrintOptions != nil {
		return buildSectionConfig(cfgPath, configPath, baseDir, md, *raw.PrintOptions, lspOpts, lspDef)
	}

	return buildFlatConfig(cfgPath, configPath, baseDir, content, md, lspOpts, lspDef)
}

func checkConflictingSections(cfgPath string, hasStyles bool, raw *rawConfigFile) error {
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
		return fmt.Errorf(
			"config %s: conflicting configuration sections; specify at most one of styles, options, or print_options",
			cfgPath,
		)
	}

	return nil
}

func decodeLSPOptions(
	cfgPath, baseDir string,
	md toml.MetaData,
	lspPrim *toml.Primitive,
) (*LSPOptions, LSPDefinedFields, error) {
	lspOpts := DefaultLSPOptions()
	if lspPrim == nil {
		return lspOpts, LSPDefinedFields{}, nil
	}

	if err := md.PrimitiveDecode(*lspPrim, lspOpts); err != nil {
		return nil, LSPDefinedFields{}, fmt.Errorf("parse config %s: %w", cfgPath, err)
	}

	if err := lspOpts.Validate(); err != nil {
		return nil, LSPDefinedFields{}, fmt.Errorf("config %s: %w", cfgPath, err)
	}

	lspOpts.ProjectCacheTTLSeconds = NormalizeProjectCacheTTLs(lspOpts.ProjectCacheTTLSeconds)

	def := LSPDefinedFields{
		DefaultProject:                 md.IsDefined("lsp", "default_project"),
		DefaultDataset:                 md.IsDefined("lsp", "default_dataset"),
		SchemaDirs:                     md.IsDefined("lsp", "schema_dirs"),
		BigQueryAPIEnabled:             md.IsDefined("lsp", "bigquery_api_enabled"),
		GcloudPath:                     md.IsDefined("lsp", "gcloud_path"),
		CacheDir:                       md.IsDefined("lsp", "cache_dir"),
		CacheTTLSeconds:                md.IsDefined("lsp", "cache_ttl_seconds"),
		ProjectCacheTTLSeconds:         md.IsDefined("lsp", "project_cache_ttl_seconds"),
		DiagnosticsEnabled:             md.IsDefined("lsp", "diagnostics_enabled"),
		RequirePartitionFilterSeverity: md.IsDefined("lsp", "require_partition_filter_severity"),
		MissingPartitionFilterSeverity: md.IsDefined("lsp", "missing_partition_filter_severity"),
		ClusteringFilterSeverity:       md.IsDefined("lsp", "clustering_filter_severity"),
		CostEstimationEnabled:          md.IsDefined("lsp", "cost_estimation_enabled"),
		CostCodeLensEnabled:            md.IsDefined("lsp", "cost_codelens_enabled"),
		CostStatusBarEnabled:           md.IsDefined("lsp", "cost_status_bar_enabled"),
		CostHoverEnabled:               md.IsDefined("lsp", "cost_hover_enabled"),
		CostDiagnosticSeverity:         md.IsDefined("lsp", "cost_diagnostic_severity"),
		CostWarningThresholdBytes:      md.IsDefined("lsp", "cost_warning_threshold_bytes"),
		CostWarningThresholdUSD:        md.IsDefined("lsp", "cost_warning_threshold_usd"),
		CostPricePerTiBUSD:             md.IsDefined("lsp", "cost_price_per_tib_usd"),
		CostDryRunDebounceMs:           md.IsDefined("lsp", "cost_dry_run_debounce_ms"),
	}

	ResolveLSPPaths(lspOpts, baseDir)

	return lspOpts, def, nil
}

// ResolveLSPPaths resolves relative SchemaDirs and CacheDir entries against baseDir.
func ResolveLSPPaths(opts *LSPOptions, baseDir string) {
	if baseDir == "" || opts == nil {
		return
	}

	for i, dir := range opts.SchemaDirs {
		if !filepath.IsAbs(dir) {
			opts.SchemaDirs[i] = filepath.Join(baseDir, dir)
		}
	}

	if opts.CacheDir != "" && !filepath.IsAbs(opts.CacheDir) {
		opts.CacheDir = filepath.Join(baseDir, opts.CacheDir)
	}
}

func buildSectionConfig(
	cfgPath string,
	configPath string,
	baseDir string,
	md toml.MetaData,
	prim toml.Primitive,
	lspOpts *LSPOptions,
	lspDef LSPDefinedFields,
) (*parsedConfig, error) {
	opts := formatter.DefaultPrintOptions()

	if err := md.PrimitiveDecode(prim, opts); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", cfgPath, err)
	}

	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("config %s: unknown option %q", cfgPath, undecoded[0].String())
	}

	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", cfgPath, err)
	}

	return &parsedConfig{
		configPath: configPath,
		baseDir:    baseDir,
		fallback:   opts,
		lsp:        lspOpts,
		lspDefined: lspDef,
	}, nil
}

func buildFlatConfig(
	cfgPath string,
	configPath string,
	baseDir string,
	content string,
	md toml.MetaData,
	lspOpts *LSPOptions,
	lspDef LSPDefinedFields,
) (*parsedConfig, error) {
	opts := formatter.DefaultPrintOptions()

	mdFlat, err := toml.Decode(content, opts)
	if err != nil {
		return nil, fmt.Errorf("parse config %s: %w", cfgPath, err)
	}

	for _, k := range mdFlat.Undecoded() {
		if len(k) == 0 || k[0] != "lsp" {
			return nil, fmt.Errorf("config %s: unknown option %q", cfgPath, k.String())
		}
	}

	for _, k := range md.Undecoded() {
		if len(k) > 0 && k[0] == "lsp" {
			return nil, fmt.Errorf("config %s: unknown option %q", cfgPath, k.String())
		}
	}

	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", cfgPath, err)
	}

	return &parsedConfig{
		configPath: configPath,
		baseDir:    baseDir,
		fallback:   opts,
		lsp:        lspOpts,
		lspDefined: lspDef,
	}, nil
}

func buildMultiStyleConfig(
	cfgPath string,
	configPath string,
	baseDir string,
	md toml.MetaData,
	raw *rawConfigFile,
	lspOpts *LSPOptions,
	lspDef LSPDefinedFields,
) (*parsedConfig, error) {
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
		configPath: configPath,
		baseDir:    baseDir,
		styles:     styles,
		fallback:   fallback,
		lsp:        lspOpts,
		lspDefined: lspDef,
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
