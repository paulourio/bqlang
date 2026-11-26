package lsp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/paulourio/bqlang/internal/bqconfig"
)

type rawClientLSPSettings struct {
	// Nested wrapper objects (optional; e.g. {"bqpls": {...}} or {"lsp": {...}}).
	BQPLS json.RawMessage `json:"bqpls,omitempty"`
	LSP   json.RawMessage `json:"lsp,omitempty"`

	// camelCase (VS Code extension settings)
	DefaultProjectCamel                 *string         `json:"defaultProject,omitempty"`
	DefaultDatasetCamel                 *string         `json:"defaultDataset,omitempty"`
	SchemaDirsCamel                     *[]string       `json:"schemaDirs,omitempty"`
	BigQueryAPIEnabledCamel             *bool           `json:"bigqueryApiEnabled,omitempty"`
	GcloudPathCamel                     *string         `json:"gcloudPath,omitempty"`
	CacheDirCamel                       *string         `json:"cacheDir,omitempty"`
	CacheTTLSecondsCamel                *int            `json:"cacheTtlSeconds,omitempty"`
	ProjectCacheTTLSecondsCamel         *map[string]int `json:"projectCacheTtlSeconds,omitempty"`
	DiagnosticsEnabledCamel             *bool           `json:"diagnosticsEnabled,omitempty"`
	RequirePartitionFilterSeverityCamel *string         `json:"requirePartitionFilterSeverity,omitempty"`
	MissingPartitionFilterSeverityCamel *string         `json:"missingPartitionFilterSeverity,omitempty"`
	ClusteringFilterSeverityCamel       *string         `json:"clusteringFilterSeverity,omitempty"`
	CostEstimationEnabledCamel          *bool           `json:"costEstimationEnabled,omitempty"`
	CostCodeLensEnabledCamel            *bool           `json:"costCodeLensEnabled,omitempty"`
	CostStatusBarEnabledCamel           *bool           `json:"costStatusBarEnabled,omitempty"`
	CostHoverEnabledCamel               *bool           `json:"costHoverEnabled,omitempty"`
	CostDiagnosticSeverityCamel         *string         `json:"costDiagnosticSeverity,omitempty"`
	CostWarningThresholdBytesCamel      *int64          `json:"costWarningThresholdBytes,omitempty"`
	CostWarningThresholdUSDCamel        *float64        `json:"costWarningThresholdUsd,omitempty"`
	CostPricePerTiBUSDCamel             *float64        `json:"costPricePerTibUsd,omitempty"`
	CostDryRunDebounceMsCamel           *int            `json:"costDryRunDebounceMs,omitempty"`

	// snake_case (.bqfmt.toml / Neovim convention)
	DefaultProjectSnake                 *string         `json:"default_project,omitempty"`
	DefaultDatasetSnake                 *string         `json:"default_dataset,omitempty"`
	SchemaDirsSnake                     *[]string       `json:"schema_dirs,omitempty"`
	BigQueryAPIEnabledSnake             *bool           `json:"bigquery_api_enabled,omitempty"`
	GcloudPathSnake                     *string         `json:"gcloud_path,omitempty"`
	CacheDirSnake                       *string         `json:"cache_dir,omitempty"`
	CacheTTLSecondsSnake                *int            `json:"cache_ttl_seconds,omitempty"`
	ProjectCacheTTLSecondsSnake         *map[string]int `json:"project_cache_ttl_seconds,omitempty"`
	DiagnosticsEnabledSnake             *bool           `json:"diagnostics_enabled,omitempty"`
	RequirePartitionFilterSeveritySnake *string         `json:"require_partition_filter_severity,omitempty"`
	MissingPartitionFilterSeveritySnake *string         `json:"missing_partition_filter_severity,omitempty"`
	ClusteringFilterSeveritySnake       *string         `json:"clustering_filter_severity,omitempty"`
	CostEstimationEnabledSnake          *bool           `json:"cost_estimation_enabled,omitempty"`
	CostCodeLensEnabledSnake            *bool           `json:"cost_codelens_enabled,omitempty"`
	CostStatusBarEnabledSnake           *bool           `json:"cost_status_bar_enabled,omitempty"`
	CostHoverEnabledSnake               *bool           `json:"cost_hover_enabled,omitempty"`
	CostDiagnosticSeveritySnake         *string         `json:"cost_diagnostic_severity,omitempty"`
	CostWarningThresholdBytesSnake      *int64          `json:"cost_warning_threshold_bytes,omitempty"`
	CostWarningThresholdUSDSnake        *float64        `json:"cost_warning_threshold_usd,omitempty"`
	CostPricePerTiBUSDSnake             *float64        `json:"cost_price_per_tib_usd,omitempty"`
	CostDryRunDebounceMsSnake           *int            `json:"cost_dry_run_debounce_ms,omitempty"`
}

// parseClientLSPOptions decodes editor/extension LSP settings from initialize
// initializationOptions or workspace/didChangeConfiguration.settings.
// Both camelCase and snake_case keys are accepted, optionally wrapped in a
// top-level "bqpls" or "lsp" object. Unrelated keys are ignored.
func parseClientLSPOptions(raw json.RawMessage, rootDir string) (*bqconfig.LSPOptions, bqconfig.LSPDefinedFields, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, bqconfig.LSPDefinedFields{}, nil
	}

	if trimmed[0] != '{' {
		return nil, bqconfig.LSPDefinedFields{}, errors.New("client settings must be a JSON object")
	}

	var payload rawClientLSPSettings
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return nil, bqconfig.LSPDefinedFields{}, fmt.Errorf("decode client settings: %w", err)
	}

	if len(bytes.TrimSpace(payload.BQPLS)) > 0 && !bytes.Equal(bytes.TrimSpace(payload.BQPLS), []byte("null")) {
		return parseClientLSPOptions(payload.BQPLS, rootDir)
	}

	if len(bytes.TrimSpace(payload.LSP)) > 0 && !bytes.Equal(bytes.TrimSpace(payload.LSP), []byte("null")) {
		return parseClientLSPOptions(payload.LSP, rootDir)
	}

	opts := bqconfig.DefaultLSPOptions()
	var def bqconfig.LSPDefinedFields

	applyClientStringFields(&payload, opts, &def)
	applyClientScalarFields(&payload, opts, &def)

	if err := opts.Validate(); err != nil {
		return nil, bqconfig.LSPDefinedFields{}, err
	}

	opts.ProjectCacheTTLSeconds = bqconfig.NormalizeProjectCacheTTLs(opts.ProjectCacheTTLSeconds)

	if def.SchemaDirs || def.CacheDir {
		bqconfig.ResolveLSPPaths(opts, rootDir)
	}

	return opts, def, nil
}

func applyClientStringFields(p *rawClientLSPSettings, opts *bqconfig.LSPOptions, def *bqconfig.LSPDefinedFields) {
	if s, ok := pickNonEmptyString(p.DefaultProjectCamel, p.DefaultProjectSnake); ok {
		opts.DefaultProject = s
		def.DefaultProject = true
	}

	if s, ok := pickNonEmptyString(p.DefaultDatasetCamel, p.DefaultDatasetSnake); ok {
		opts.DefaultDataset = s
		def.DefaultDataset = true
	}

	if s, ok := pickNonEmptyString(p.GcloudPathCamel, p.GcloudPathSnake); ok {
		opts.GcloudPath = s
		def.GcloudPath = true
	}

	if s, ok := pickNonEmptyString(p.CacheDirCamel, p.CacheDirSnake); ok {
		opts.CacheDir = s
		def.CacheDir = true
	}

	if dirs := pickStringSlice(p.SchemaDirsCamel, p.SchemaDirsSnake); len(dirs) > 0 {
		clean := make([]string, 0, len(dirs))
		for _, d := range dirs {
			if t := strings.TrimSpace(d); t != "" {
				clean = append(clean, t)
			}
		}
		if len(clean) > 0 {
			opts.SchemaDirs = clean
			def.SchemaDirs = true
		}
	}
}

func applyClientScalarFields(p *rawClientLSPSettings, opts *bqconfig.LSPOptions, def *bqconfig.LSPDefinedFields) {
	if v, ok := pickBool(p.BigQueryAPIEnabledCamel, p.BigQueryAPIEnabledSnake); ok {
		opts.BigQueryAPIEnabled = v
		def.BigQueryAPIEnabled = true
	}

	if v, ok := pickInt(p.CacheTTLSecondsCamel, p.CacheTTLSecondsSnake); ok {
		opts.CacheTTLSeconds = v
		def.CacheTTLSeconds = true
	}

	if m, ok := pickMergedIntMap(p.ProjectCacheTTLSecondsCamel, p.ProjectCacheTTLSecondsSnake); ok {
		opts.ProjectCacheTTLSeconds = m
		def.ProjectCacheTTLSeconds = true
	}

	if v, ok := pickBool(p.DiagnosticsEnabledCamel, p.DiagnosticsEnabledSnake); ok {
		opts.DiagnosticsEnabled = v
		def.DiagnosticsEnabled = true
	}

	applyClientLintFields(p, opts, def)
	applyClientCostFields(p, opts, def)
}

func applyClientLintFields(p *rawClientLSPSettings, opts *bqconfig.LSPOptions, def *bqconfig.LSPDefinedFields) {
	if s, ok := pickNonEmptyString(p.RequirePartitionFilterSeverityCamel, p.RequirePartitionFilterSeveritySnake); ok {
		opts.RequirePartitionFilterSeverity = bqconfig.Severity(strings.ToLower(s))
		def.RequirePartitionFilterSeverity = true
	}

	if s, ok := pickNonEmptyString(p.MissingPartitionFilterSeverityCamel, p.MissingPartitionFilterSeveritySnake); ok {
		opts.MissingPartitionFilterSeverity = bqconfig.Severity(strings.ToLower(s))
		def.MissingPartitionFilterSeverity = true
	}

	if s, ok := pickNonEmptyString(p.ClusteringFilterSeverityCamel, p.ClusteringFilterSeveritySnake); ok {
		opts.ClusteringFilterSeverity = bqconfig.Severity(strings.ToLower(s))
		def.ClusteringFilterSeverity = true
	}
}

func applyClientCostFields(p *rawClientLSPSettings, opts *bqconfig.LSPOptions, def *bqconfig.LSPDefinedFields) {
	if v, ok := pickBool(p.CostEstimationEnabledCamel, p.CostEstimationEnabledSnake); ok {
		opts.CostEstimationEnabled = v
		def.CostEstimationEnabled = true
	}

	if v, ok := pickBool(p.CostCodeLensEnabledCamel, p.CostCodeLensEnabledSnake); ok {
		opts.CostCodeLensEnabled = v
		def.CostCodeLensEnabled = true
	}

	if v, ok := pickBool(p.CostStatusBarEnabledCamel, p.CostStatusBarEnabledSnake); ok {
		opts.CostStatusBarEnabled = v
		def.CostStatusBarEnabled = true
	}

	if v, ok := pickBool(p.CostHoverEnabledCamel, p.CostHoverEnabledSnake); ok {
		opts.CostHoverEnabled = v
		def.CostHoverEnabled = true
	}

	if s, ok := pickNonEmptyString(p.CostDiagnosticSeverityCamel, p.CostDiagnosticSeveritySnake); ok {
		opts.CostDiagnosticSeverity = bqconfig.Severity(strings.ToLower(s))
		def.CostDiagnosticSeverity = true
	}

	if v, ok := pickInt64(p.CostWarningThresholdBytesCamel, p.CostWarningThresholdBytesSnake); ok {
		opts.CostWarningThresholdBytes = v
		def.CostWarningThresholdBytes = true
	}

	if v, ok := pickFloat64(p.CostWarningThresholdUSDCamel, p.CostWarningThresholdUSDSnake); ok {
		opts.CostWarningThresholdUSD = v
		def.CostWarningThresholdUSD = true
	}

	if v, ok := pickFloat64(p.CostPricePerTiBUSDCamel, p.CostPricePerTiBUSDSnake); ok {
		opts.CostPricePerTiBUSD = v
		def.CostPricePerTiBUSD = true
	}

	if v, ok := pickInt(p.CostDryRunDebounceMsCamel, p.CostDryRunDebounceMsSnake); ok {
		opts.CostDryRunDebounceMs = v
		def.CostDryRunDebounceMs = true
	}
}

func pickNonEmptyString(primary, fallback *string) (string, bool) {
	if primary != nil {
		if s := strings.TrimSpace(*primary); s != "" {
			return s, true
		}
	}

	if fallback != nil {
		if s := strings.TrimSpace(*fallback); s != "" {
			return s, true
		}
	}

	return "", false
}

func pickStringSlice(primary, fallback *[]string) []string {
	if primary != nil {
		return *primary
	}

	if fallback != nil {
		return *fallback
	}

	return nil
}

func pickBool(primary, fallback *bool) (bool, bool) {
	if primary != nil {
		return *primary, true
	}

	if fallback != nil {
		return *fallback, true
	}

	return false, false
}

func pickInt(primary, fallback *int) (int, bool) {
	if primary != nil {
		return *primary, true
	}

	if fallback != nil {
		return *fallback, true
	}

	return 0, false
}

func pickMergedIntMap(primary, fallback *map[string]int) (map[string]int, bool) {
	if primary == nil && fallback == nil {
		return nil, false
	}

	merged := make(map[string]int)
	mergeRawProjectTTLMap(merged, fallback)
	mergeRawProjectTTLMap(merged, primary)

	return merged, true
}

func mergeRawProjectTTLMap(dst map[string]int, src *map[string]int) {
	if src == nil {
		return
	}

	for rawKey, ttl := range *src {
		trimmed := strings.TrimSpace(rawKey)
		if trimmed == "" || ttl < 0 {
			// Preserve invalid entries verbatim so opts.Validate() surfaces a structured error.
			dst[rawKey] = ttl
			continue
		}

		key := strings.ToLower(trimmed)
		if cur, exists := dst[key]; !exists || ttl > cur {
			dst[key] = ttl
		}
	}
}

func pickInt64(primary, fallback *int64) (int64, bool) {
	if primary != nil {
		return *primary, true
	}

	if fallback != nil {
		return *fallback, true
	}

	return 0, false
}

func pickFloat64(primary, fallback *float64) (float64, bool) {
	if primary != nil {
		return *primary, true
	}

	if fallback != nil {
		return *fallback, true
	}

	return 0, false
}
