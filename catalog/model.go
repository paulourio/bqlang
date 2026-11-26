// Package catalog provides BigQuery table metadata models, REST v2 fetching,
// non-blocking memory/disk caching, and GoogleSQL CatalogCallback integration.
package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// ErrInvalidTableJSON is returned when table metadata JSON cannot be decoded.
var ErrInvalidTableJSON = errors.New("invalid table metadata json")

// TableType identifies the BigQuery table kind.
type TableType string

// Supported BigQuery TableType values.
const (
	TableTypeTable            TableType = "TABLE"
	TableTypeView             TableType = "VIEW"
	TableTypeExternal         TableType = "EXTERNAL"
	TableTypeMaterializedView TableType = "MATERIALIZED_VIEW"
	TableTypeSnapshot         TableType = "SNAPSHOT"
)

// PartitionKind identifies time-based vs integer-range partitioning.
type PartitionKind string

// Supported PartitionKind values.
const (
	PartitionKindTime  PartitionKind = "TIME"
	PartitionKindRange PartitionKind = "RANGE"
)

// FieldMode identifies BigQuery column nullability/repetition mode.
type FieldMode string

// Supported FieldMode values.
const (
	ModeNullable FieldMode = "NULLABLE"
	ModeRequired FieldMode = "REQUIRED"
	ModeRepeated FieldMode = "REPEATED"
)

// FlexInt64 decodes BigQuery 64-bit integer values represented as quoted JSON
// strings ("1000"), empty strings ("" -> 0), unquoted numbers (1000), or null.
type FlexInt64 int64

// UnmarshalJSON implements json.Unmarshaler.
func (f *FlexInt64) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*f = 0

		return nil
	}

	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return fmt.Errorf("FlexInt64: %w", err)
		}

		s = strings.TrimSpace(s)
		if s == "" {
			*f = 0

			return nil
		}

		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("FlexInt64 parse %q: %w", s, err)
		}

		*f = FlexInt64(n)

		return nil
	}

	n, err := strconv.ParseInt(string(trimmed), 10, 64)
	if err != nil {
		return fmt.Errorf("FlexInt64 parse %q: %w", string(trimmed), err)
	}

	*f = FlexInt64(n)

	return nil
}

// MarshalJSON implements json.Marshaler.
func (f FlexInt64) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatInt(int64(f), 10)), nil
}

// Int64 returns the underlying int64 value.
func (f FlexInt64) Int64() int64 {
	return int64(f)
}

// TableRef identifies a BigQuery table by project, dataset, and table ID.
type TableRef struct {
	Project string `json:"projectId,omitempty"`
	Dataset string `json:"datasetId,omitempty"`
	Table   string `json:"tableId,omitempty"`
}

// FullName returns the dot-qualified table name (project.dataset.table when
// complete, or dataset.table / table when leading segments are empty).
func (r TableRef) FullName() string {
	switch {
	case r.Project != "" && r.Dataset != "" && r.Table != "":
		return r.Project + "." + r.Dataset + "." + r.Table
	case r.Dataset != "" && r.Table != "":
		return r.Dataset + "." + r.Table
	case r.Project != "" && r.Dataset != "":
		return r.Project + "." + r.Dataset
	default:
		return r.Table
	}
}

// IsComplete reports whether Project, Dataset, and Table are all non-empty.
func (r TableRef) IsComplete() bool {
	return r.Project != "" && r.Dataset != "" && r.Table != ""
}

// NormalizedKey returns a canonical lowercased lookup key for the TableRef.
func (r TableRef) NormalizedKey() string {
	return strings.ToLower(r.FullName())
}

// WithDefaults fills missing Project and Dataset fields using defaults.
func (r TableRef) WithDefaults(defaultProject, defaultDataset string) TableRef {
	if r.Dataset == "" {
		r.Dataset = defaultDataset
	}

	if r.Project == "" {
		r.Project = defaultProject
	}

	return r
}

// TableSchema wraps the REST v2 schema.fields array.
type TableSchema struct {
	Fields []ColumnMeta `json:"fields,omitempty"`
}

// TimePartitioning describes BigQuery time-based partitioning settings.
type TimePartitioning struct {
	Type                   string    `json:"type,omitempty"`
	Field                  string    `json:"field,omitempty"`
	ExpirationMs           FlexInt64 `json:"expirationMs,omitempty"`
	RequirePartitionFilter bool      `json:"requirePartitionFilter,omitempty"`
}

// IntRange describes integer range partitioning bounds.
type IntRange struct {
	Start    FlexInt64 `json:"start,omitempty"`
	End      FlexInt64 `json:"end,omitempty"`
	Interval FlexInt64 `json:"interval,omitempty"`
}

// RangePartitioning describes BigQuery integer-range partitioning settings.
type RangePartitioning struct {
	Field string    `json:"field,omitempty"`
	Range *IntRange `json:"range,omitempty"`
}

// PartitionInfo provides a unified view of time or integer-range partitioning.
type PartitionInfo struct {
	Kind          PartitionKind `json:"kind,omitempty"`
	Type          string        `json:"type,omitempty"`
	Field         string        `json:"field,omitempty"`
	ExpirationMs  FlexInt64     `json:"expirationMs,omitempty"`
	RequireFilter bool          `json:"requireFilter,omitempty"`
	Range         *IntRange     `json:"range,omitempty"`
}

// ClusteringInfo describes BigQuery table clustering columns.
type ClusteringInfo struct {
	Fields []string `json:"fields,omitempty"`
}

// RangeElementType specifies the element type of a BigQuery RANGE column.
type RangeElementType struct {
	Type string `json:"type,omitempty"`
}

// PolicyTags holds BigQuery column-level data catalog policy tag names.
type PolicyTags struct {
	Names []string `json:"names,omitempty"`
}

// ViewDefinition describes a BigQuery SQL view definition.
type ViewDefinition struct {
	Query                  string `json:"query,omitempty"`
	UseLegacySQL           bool   `json:"useLegacySql,omitempty"`
	UseExplicitColumnNames bool   `json:"useExplicitColumnNames,omitempty"`
}

// MaterializedViewDefinition describes a BigQuery materialized view.
type MaterializedViewDefinition struct {
	Query               string    `json:"query,omitempty"`
	EnableRefresh       bool      `json:"enableRefresh,omitempty"`
	RefreshIntervalMs   FlexInt64 `json:"refreshIntervalMs,omitempty"`
	LastRefreshTime     FlexInt64 `json:"lastRefreshTime,omitempty"`
	AllowNonIncremental bool      `json:"allowNonIncrementalDefinition,omitempty"`
	MaxStaleness        string    `json:"maxStaleness,omitempty"`
}

// ExternalDataConfiguration describes an external BigQuery table configuration.
type ExternalDataConfiguration struct {
	SourceFormat string       `json:"sourceFormat,omitempty"`
	SourceUris   []string     `json:"sourceUris,omitempty"`
	Autodetect   bool         `json:"autodetect,omitempty"`
	ConnectionID string       `json:"connectionId,omitempty"`
	Schema       *TableSchema `json:"schema,omitempty"`
}

// ColumnMeta represents a single column or nested STRUCT field in BigQuery.
type ColumnMeta struct {
	Name                   string            `json:"name"`
	Type                   string            `json:"type"`
	Mode                   FieldMode         `json:"mode,omitempty"`
	Description            string            `json:"description,omitempty"`
	Fields                 []ColumnMeta      `json:"fields,omitempty"`
	MaxLength              FlexInt64         `json:"maxLength,omitempty"`
	Precision              FlexInt64         `json:"precision,omitempty"`
	Scale                  FlexInt64         `json:"scale,omitempty"`
	TimestampPrecision     FlexInt64         `json:"timestampPrecision,omitempty"`
	RangeElementType       *RangeElementType `json:"rangeElementType,omitempty"`
	PolicyTags             *PolicyTags       `json:"policyTags,omitempty"`
	Collation              string            `json:"collation,omitempty"`
	DefaultValueExpression string            `json:"defaultValueExpression,omitempty"`
	RoundingMode           string            `json:"roundingMode,omitempty"`
}

// FieldAt returns the 0-based nested field of a STRUCT column if in bounds.
func (c *ColumnMeta) FieldAt(idx int) (*ColumnMeta, bool) {
	if c == nil || idx < 0 || idx >= len(c.Fields) {
		return nil, false
	}

	return &c.Fields[idx], true
}

// SQLType formats the column type in GoogleSQL/BigQuery SQL notation
// (for example, ARRAY<STRUCT<id INT64, name STRING>> or NUMERIC(10, 2)).
func (c *ColumnMeta) SQLType() string {
	if c == nil {
		return ""
	}

	base := c.baseSQLType()
	if c.EffectiveMode() == ModeRepeated {
		return "ARRAY<" + base + ">"
	}

	return base
}

func (c *ColumnMeta) baseSQLType() string {
	typ := normalizeTypeName(c.Type)

	switch typ {
	case "STRUCT":
		if len(c.Fields) == 0 {
			return "STRUCT"
		}

		parts := make([]string, 0, len(c.Fields))
		for i := range c.Fields {
			f := &c.Fields[i]
			parts = append(parts, f.Name+" "+f.SQLType())
		}

		return "STRUCT<" + strings.Join(parts, ", ") + ">"
	case "RANGE":
		if c.RangeElementType != nil && c.RangeElementType.Type != "" {
			return "RANGE<" + normalizeTypeName(c.RangeElementType.Type) + ">"
		}

		return "RANGE"
	case "STRING", "BYTES":
		if c.MaxLength > 0 {
			return fmt.Sprintf("%s(%d)", typ, c.MaxLength)
		}

		return typ
	case "NUMERIC", "BIGNUMERIC":
		if c.Precision > 0 && c.Scale > 0 {
			return fmt.Sprintf("%s(%d, %d)", typ, c.Precision, c.Scale)
		}

		if c.Precision > 0 {
			return fmt.Sprintf("%s(%d)", typ, c.Precision)
		}

		return typ
	case "TIMESTAMP":
		if c.TimestampPrecision > 0 && c.TimestampPrecision != 6 {
			return fmt.Sprintf("TIMESTAMP(%d)", c.TimestampPrecision)
		}

		return typ
	default:
		return typ
	}
}

// EffectiveMode returns Mode, defaulting to ModeNullable when empty.
func (c *ColumnMeta) EffectiveMode() FieldMode {
	if c == nil || c.Mode == "" {
		return ModeNullable
	}

	return FieldMode(strings.ToUpper(string(c.Mode)))
}

// TableMeta holds canonical metadata for a BigQuery table, view, or external table.
type TableMeta struct {
	Kind                   string                      `json:"kind,omitempty"`
	Etag                   string                      `json:"etag,omitempty"`
	ID                     string                      `json:"id,omitempty"`
	SelfLink               string                      `json:"selfLink,omitempty"`
	Ref                    TableRef                    `json:"tableReference"`
	Type                   TableType                   `json:"type,omitempty"`
	FriendlyName           string                      `json:"friendlyName,omitempty"`
	Description            string                      `json:"description,omitempty"`
	Labels                 map[string]string           `json:"labels,omitempty"`
	Location               string                      `json:"location,omitempty"`
	NumRows                *FlexInt64                  `json:"numRows,omitempty"`
	NumBytes               *FlexInt64                  `json:"numBytes,omitempty"`
	NumLongTermBytes       *FlexInt64                  `json:"numLongTermBytes,omitempty"`
	NumPhysicalBytes       *FlexInt64                  `json:"numPhysicalBytes,omitempty"`
	NumPartitions          *FlexInt64                  `json:"numPartitions,omitempty"`
	CreationTime           FlexInt64                   `json:"creationTime,omitempty"`
	LastModifiedTime       FlexInt64                   `json:"lastModifiedTime,omitempty"`
	ExpirationTime         FlexInt64                   `json:"expirationTime,omitempty"`
	Schema                 []ColumnMeta                `json:"-"`
	TimePartitioning       *TimePartitioning           `json:"timePartitioning,omitempty"`
	RangePartitioning      *RangePartitioning          `json:"rangePartitioning,omitempty"`
	Partitioning           *PartitionInfo              `json:"-"`
	Clustering             *ClusteringInfo             `json:"clustering,omitempty"`
	RequirePartitionFilter bool                        `json:"requirePartitionFilter,omitempty"`
	PartitionProbed        bool                        `json:"partitionProbed,omitempty"`
	View                   *ViewDefinition             `json:"view,omitempty"`
	MaterializedView       *MaterializedViewDefinition `json:"materializedView,omitempty"`
	ExternalDataConfig     *ExternalDataConfiguration  `json:"externalDataConfiguration,omitempty"`
}

type tableWire struct {
	Kind                   string                      `json:"kind,omitempty"`
	Etag                   string                      `json:"etag,omitempty"`
	ID                     string                      `json:"id,omitempty"`
	SelfLink               string                      `json:"selfLink,omitempty"`
	Ref                    TableRef                    `json:"tableReference"`
	Type                   TableType                   `json:"type,omitempty"`
	FriendlyName           string                      `json:"friendlyName,omitempty"`
	Description            string                      `json:"description,omitempty"`
	Labels                 map[string]string           `json:"labels,omitempty"`
	Location               string                      `json:"location,omitempty"`
	NumRows                *FlexInt64                  `json:"numRows,omitempty"`
	NumBytes               *FlexInt64                  `json:"numBytes,omitempty"`
	NumLongTermBytes       *FlexInt64                  `json:"numLongTermBytes,omitempty"`
	NumPhysicalBytes       *FlexInt64                  `json:"numPhysicalBytes,omitempty"`
	NumPartitions          *FlexInt64                  `json:"numPartitions,omitempty"`
	CreationTime           FlexInt64                   `json:"creationTime,omitempty"`
	LastModifiedTime       FlexInt64                   `json:"lastModifiedTime,omitempty"`
	ExpirationTime         FlexInt64                   `json:"expirationTime,omitempty"`
	Schema                 *TableSchema                `json:"schema,omitempty"`
	TimePartitioning       *TimePartitioning           `json:"timePartitioning,omitempty"`
	RangePartitioning      *RangePartitioning          `json:"rangePartitioning,omitempty"`
	Clustering             *ClusteringInfo             `json:"clustering,omitempty"`
	RequirePartitionFilter bool                        `json:"requirePartitionFilter,omitempty"`
	PartitionProbed        bool                        `json:"partitionProbed,omitempty"`
	View                   *ViewDefinition             `json:"view,omitempty"`
	MaterializedView       *MaterializedViewDefinition `json:"materializedView,omitempty"`
	ExternalDataConfig     *ExternalDataConfiguration  `json:"externalDataConfiguration,omitempty"`
}

// MarshalJSON encodes TableMeta as canonical BigQuery REST v2 Table JSON.
func (m *TableMeta) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}

	wire := tableWire{
		Kind:                   m.Kind,
		Etag:                   m.Etag,
		ID:                     m.ID,
		SelfLink:               m.SelfLink,
		Ref:                    m.Ref,
		Type:                   m.Type,
		FriendlyName:           m.FriendlyName,
		Description:            m.Description,
		Labels:                 m.Labels,
		Location:               m.Location,
		NumRows:                m.NumRows,
		NumBytes:               m.NumBytes,
		NumLongTermBytes:       m.NumLongTermBytes,
		NumPhysicalBytes:       m.NumPhysicalBytes,
		NumPartitions:          m.NumPartitions,
		CreationTime:           m.CreationTime,
		LastModifiedTime:       m.LastModifiedTime,
		ExpirationTime:         m.ExpirationTime,
		TimePartitioning:       m.TimePartitioning,
		RangePartitioning:      m.RangePartitioning,
		Clustering:             m.Clustering,
		RequirePartitionFilter: m.RequirePartitionFilter,
		PartitionProbed:        m.PartitionProbed,
		View:                   m.View,
		MaterializedView:       m.MaterializedView,
		ExternalDataConfig:     m.ExternalDataConfig,
	}

	if len(m.Schema) > 0 {
		wire.Schema = &TableSchema{Fields: m.Schema}
	}

	data, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("marshal TableMeta: %w", err)
	}

	return data, nil
}

// UnmarshalJSON decodes BigQuery REST v2 Table JSON into TableMeta and normalizes it.
func (m *TableMeta) UnmarshalJSON(data []byte) error {
	var wire tableWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("unmarshal TableMeta: %w", err)
	}

	*m = TableMeta{
		Kind:                   wire.Kind,
		Etag:                   wire.Etag,
		ID:                     wire.ID,
		SelfLink:               wire.SelfLink,
		Ref:                    wire.Ref,
		Type:                   wire.Type,
		FriendlyName:           wire.FriendlyName,
		Description:            wire.Description,
		Labels:                 wire.Labels,
		Location:               wire.Location,
		NumRows:                wire.NumRows,
		NumBytes:               wire.NumBytes,
		NumLongTermBytes:       wire.NumLongTermBytes,
		NumPhysicalBytes:       wire.NumPhysicalBytes,
		NumPartitions:          wire.NumPartitions,
		CreationTime:           wire.CreationTime,
		LastModifiedTime:       wire.LastModifiedTime,
		ExpirationTime:         wire.ExpirationTime,
		TimePartitioning:       wire.TimePartitioning,
		RangePartitioning:      wire.RangePartitioning,
		Clustering:             wire.Clustering,
		RequirePartitionFilter: wire.RequirePartitionFilter,
		PartitionProbed:        wire.PartitionProbed,
		View:                   wire.View,
		MaterializedView:       wire.MaterializedView,
		ExternalDataConfig:     wire.ExternalDataConfig,
	}

	if wire.Schema != nil {
		m.Schema = wire.Schema.Fields
	}

	m.Normalize(TableRef{})

	return nil
}

// DecodeTableJSON decodes either a BigQuery REST v2 / `bq show --format=json`
// Table object or a bare `bq show --schema --format=json` field array, filling
// missing TableRef fields from fallbackRef.
func DecodeTableJSON(data []byte, fallbackRef TableRef) (*TableMeta, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("%w: empty payload", ErrInvalidTableJSON)
	}

	switch trimmed[0] {
	case '{':
		var meta TableMeta
		if err := json.Unmarshal(trimmed, &meta); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidTableJSON, err)
		}

		meta.Normalize(fallbackRef)

		return &meta, nil
	case '[':
		var fields []ColumnMeta
		if err := json.Unmarshal(trimmed, &fields); err != nil {
			return nil, fmt.Errorf("%w: schema array: %w", ErrInvalidTableJSON, err)
		}

		meta := &TableMeta{
			Ref:    fallbackRef,
			Type:   TableTypeTable,
			Schema: fields,
		}
		meta.Normalize(fallbackRef)

		return meta, nil
	default:
		return nil, fmt.Errorf("%w: unexpected leading token %q", ErrInvalidTableJSON, trimmed[0])
	}
}

// Clone returns a deep copy of TableMeta including all slices, maps, and
// nested pointers so concurrent callers cannot race on shared metadata.
func (m *TableMeta) Clone() *TableMeta {
	if m == nil {
		return nil
	}

	cp := *m
	cp.Labels = maps.Clone(m.Labels)
	cp.NumRows = cloneFlexInt64Ptr(m.NumRows)
	cp.NumBytes = cloneFlexInt64Ptr(m.NumBytes)
	cp.NumLongTermBytes = cloneFlexInt64Ptr(m.NumLongTermBytes)
	cp.NumPhysicalBytes = cloneFlexInt64Ptr(m.NumPhysicalBytes)
	cp.NumPartitions = cloneFlexInt64Ptr(m.NumPartitions)
	cp.Schema = cloneColumns(m.Schema)

	if m.TimePartitioning != nil {
		tp := *m.TimePartitioning
		cp.TimePartitioning = &tp
	}

	cp.RangePartitioning = cloneRangePartitioning(m.RangePartitioning)
	cp.Partitioning = clonePartitionInfo(m.Partitioning)

	if m.Clustering != nil {
		cp.Clustering = &ClusteringInfo{
			Fields: slices.Clone(m.Clustering.Fields),
		}
	}

	if m.View != nil {
		v := *m.View
		cp.View = &v
	}

	if m.MaterializedView != nil {
		mv := *m.MaterializedView
		cp.MaterializedView = &mv
	}

	cp.ExternalDataConfig = cloneExternalDataConfig(m.ExternalDataConfig)

	return &cp
}

func cloneFlexInt64Ptr(p *FlexInt64) *FlexInt64 {
	if p == nil {
		return nil
	}

	v := *p

	return &v
}

func cloneRangePartitioning(rp *RangePartitioning) *RangePartitioning {
	if rp == nil {
		return nil
	}

	cp := *rp
	if rp.Range != nil {
		r := *rp.Range
		cp.Range = &r
	}

	return &cp
}

func clonePartitionInfo(pi *PartitionInfo) *PartitionInfo {
	if pi == nil {
		return nil
	}

	cp := *pi
	if pi.Range != nil {
		r := *pi.Range
		cp.Range = &r
	}

	return &cp
}

func cloneExternalDataConfig(ext *ExternalDataConfiguration) *ExternalDataConfiguration {
	if ext == nil {
		return nil
	}

	cp := *ext
	cp.SourceUris = slices.Clone(ext.SourceUris)
	if ext.Schema != nil {
		cp.Schema = &TableSchema{
			Fields: cloneColumns(ext.Schema.Fields),
		}
	}

	return &cp
}

func cloneColumns(cols []ColumnMeta) []ColumnMeta {
	if cols == nil {
		return nil
	}

	out := make([]ColumnMeta, len(cols))
	for i := range cols {
		out[i] = cloneColumn(cols[i])
	}

	return out
}

func cloneColumn(c ColumnMeta) ColumnMeta {
	cp := c
	cp.Fields = cloneColumns(c.Fields)

	if c.RangeElementType != nil {
		ret := *c.RangeElementType
		cp.RangeElementType = &ret
	}

	if c.PolicyTags != nil {
		cp.PolicyTags = &PolicyTags{
			Names: slices.Clone(c.PolicyTags.Names),
		}
	}

	return cp
}

// Normalize canonicalizes table type, missing TableRef segments, schema columns,
// external schema fallback, and effective partitioning metadata.
func (m *TableMeta) Normalize(fallbackRef TableRef) {
	if m == nil {
		return
	}

	m.Ref = m.Ref.WithDefaults(fallbackRef.Project, fallbackRef.Dataset)
	if m.Ref.Table == "" {
		m.Ref.Table = fallbackRef.Table
	}

	if m.Type == "" {
		m.Type = TableTypeTable
	} else {
		m.Type = TableType(strings.ToUpper(string(m.Type)))
	}

	if len(m.Schema) == 0 && m.ExternalDataConfig != nil && m.ExternalDataConfig.Schema != nil {
		m.Schema = m.ExternalDataConfig.Schema.Fields
	}

	normalizeColumns(m.Schema)
	m.RequirePartitionFilter = m.EffectiveRequirePartitionFilter()
	m.Partitioning = m.buildPartitionInfo()
}

func normalizeColumns(cols []ColumnMeta) {
	for i := range cols {
		cols[i].Type = normalizeTypeName(cols[i].Type)
		cols[i].Mode = cols[i].EffectiveMode()
		if cols[i].RangeElementType != nil && cols[i].RangeElementType.Type != "" {
			cols[i].RangeElementType.Type = normalizeTypeName(cols[i].RangeElementType.Type)
		}

		normalizeColumns(cols[i].Fields)
	}
}

func normalizeTypeName(raw string) string {
	upper := strings.ToUpper(strings.TrimSpace(raw))
	switch upper {
	case "RECORD":
		return "STRUCT"
	case "INTEGER":
		return "INT64"
	case "FLOAT", "DOUBLE":
		return "FLOAT64"
	case "BOOLEAN":
		return "BOOL"
	case "DECIMAL":
		return "NUMERIC"
	case "BIGDECIMAL":
		return "BIGNUMERIC"
	default:
		return upper
	}
}

func (m *TableMeta) buildPartitionInfo() *PartitionInfo {
	if m.TimePartitioning != nil {
		return &PartitionInfo{
			Kind:          PartitionKindTime,
			Type:          strings.ToUpper(m.TimePartitioning.Type),
			Field:         m.TimePartitioning.Field,
			ExpirationMs:  m.TimePartitioning.ExpirationMs,
			RequireFilter: m.EffectiveRequirePartitionFilter(),
		}
	}

	if m.RangePartitioning != nil {
		return &PartitionInfo{
			Kind:          PartitionKindRange,
			Type:          "RANGE",
			Field:         m.RangePartitioning.Field,
			RequireFilter: m.EffectiveRequirePartitionFilter(),
			Range:         m.RangePartitioning.Range,
		}
	}

	if m.Partitioning != nil {
		cp := *m.Partitioning
		cp.RequireFilter = cp.RequireFilter || m.RequirePartitionFilter

		return &cp
	}

	return nil
}

// EffectivePartitioning returns the normalized PartitionInfo, or nil if unpartitioned.
func (m *TableMeta) EffectivePartitioning() *PartitionInfo {
	if m == nil {
		return nil
	}

	if m.Partitioning != nil {
		return m.Partitioning
	}

	return m.buildPartitionInfo()
}

// EffectiveRequirePartitionFilter reports whether partition filtering is required
// either at the table level or on timePartitioning.
func (m *TableMeta) EffectiveRequirePartitionFilter() bool {
	if m == nil {
		return false
	}

	if m.RequirePartitionFilter {
		return true
	}

	if m.TimePartitioning != nil && m.TimePartitioning.RequirePartitionFilter {
		return true
	}

	return m.Partitioning != nil && m.Partitioning.RequireFilter
}

// IsIngestionTimePartitioned reports whether the table uses BigQuery ingestion-time
// partitioning (_PARTITIONTIME / _PARTITIONDATE).
func (m *TableMeta) IsIngestionTimePartitioned() bool {
	if m == nil {
		return false
	}

	if m.TimePartitioning != nil {
		return m.TimePartitioning.Field == ""
	}

	return m.Partitioning != nil && m.Partitioning.Kind == PartitionKindTime && m.Partitioning.Field == ""
}

// IsPartitionColumn reports whether colName is the table's partitioning column
// (case-insensitive), including _PARTITIONTIME and _PARTITIONDATE for ingestion-time tables.
func (m *TableMeta) IsPartitionColumn(colName string) bool {
	if m == nil || colName == "" {
		return false
	}

	if m.IsIngestionTimePartitioned() {
		return strings.EqualFold(colName, "_PARTITIONTIME") || strings.EqualFold(colName, "_PARTITIONDATE")
	}

	if m.TimePartitioning != nil && m.TimePartitioning.Field != "" {
		return strings.EqualFold(colName, m.TimePartitioning.Field)
	}

	if m.RangePartitioning != nil && m.RangePartitioning.Field != "" {
		return strings.EqualFold(colName, m.RangePartitioning.Field)
	}

	if m.Partitioning != nil && m.Partitioning.Field != "" {
		return strings.EqualFold(colName, m.Partitioning.Field)
	}

	return false
}

// ClusterRank returns the 1-based clustering position for colName (case-insensitive).
func (m *TableMeta) ClusterRank(colName string) (int, bool) {
	if m == nil || m.Clustering == nil || colName == "" {
		return 0, false
	}

	for i, field := range m.Clustering.Fields {
		if strings.EqualFold(colName, field) {
			return i + 1, true
		}
	}

	return 0, false
}

// FindColumn finds a top-level column by name (case-insensitive).
func (m *TableMeta) FindColumn(name string) (*ColumnMeta, bool) {
	if m == nil || name == "" {
		return nil, false
	}

	for i := range m.Schema {
		if strings.EqualFold(m.Schema[i].Name, name) {
			return &m.Schema[i], true
		}
	}

	return nil, false
}

// FindField resolves a nested column or STRUCT field path (case-insensitive).
func (m *TableMeta) FindField(path ...string) (*ColumnMeta, bool) {
	if m == nil || len(path) == 0 {
		return nil, false
	}

	cur, ok := m.FindColumn(path[0])
	if !ok {
		return nil, false
	}

	for _, seg := range path[1:] {
		next, found := findNestedField(cur.Fields, seg)
		if !found {
			return nil, false
		}

		cur = next
	}

	return cur, true
}

func findNestedField(fields []ColumnMeta, name string) (*ColumnMeta, bool) {
	for i := range fields {
		if strings.EqualFold(fields[i].Name, name) {
			return &fields[i], true
		}
	}

	return nil, false
}
