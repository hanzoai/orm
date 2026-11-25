// Copyright © 2026 Hanzo AI. MIT License.

// Package schema provides the Unified Global Schema Bridge for the Hanzo
// ecosystem. Any Hanzo entity is declared ONCE — either as a typed Go struct
// with `schema` / `orm` / `json` tags or as a ZAP (`.zap`) schema descriptor —
// and deterministically compiles to:
//
//  1. hanzoai/sql (PostgreSQL & SQLite) relational DDL, indexes, and optional
//     Row-Level Security policies (plus direct conversion to orm/tenant.Table).
//  2. hanzoai/datastore (ClickHouse 26.2-safe) analytical DDL:
//     ReplacingMergeTree(version) for dimensions & latest-state snapshots,
//     MergeTree for immutable append-only facts, AggregatingMergeTree /
//     SummingMergeTree + Materialized Views for L1 incremental rollups, and
//     ReplacingMergeTree(refreshed_at) for L3 serving snapshots — statically
//     verified for zero ClickHouse 26.2 alias shadowing and strict HIP-1405
//     financial invariants.
//  3. ZAP (`.zap`) IDL schemas for Go (`zapgen`), Rust (`zapc-rs`), Python,
//     and TypeScript wire/RPC/MCP interoperability, with lossless round-trip
//     parsing back into this package's IR.
//  4. Governance & Analytics Catalog metadata (`analytics.catalog` entries,
//     metric definitions, units, accuracy contracts, freshness SLAs, tenant
//     isolation keys, retention TTLs, and JSON Schema / OpenAPI 3.1 / MCP
//     schema projections).
package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// FieldType is the canonical semantic & storage type of an entity field.
// Every backend compiler (SQL, ClickHouse, ZAP, JSON Schema / OpenAPI / MCP)
// derives its concrete representation deterministically from FieldType.
type FieldType string

const (
	TypeString        FieldType = "string"
	TypeLowCardString FieldType = "lowcard_string"
	TypeBool          FieldType = "bool"
	TypeInt8          FieldType = "int8"
	TypeInt16         FieldType = "int16"
	TypeInt32         FieldType = "int32"
	TypeInt64         FieldType = "int64"
	TypeUint8         FieldType = "uint8"
	TypeUint16        FieldType = "uint16"
	TypeUint32        FieldType = "uint32"
	TypeUint64        FieldType = "uint64"
	TypeFloat32       FieldType = "float32"
	TypeFloat64       FieldType = "float64"
	TypeMoneyMinor    FieldType = "money_minor" // signed int64 in currency minor units (e.g. cents)
	TypeMoneyNano     FieldType = "money_nano"  // signed int64 in 1e-9 USD (nano-USD)
	TypeTimestamp     FieldType = "timestamp"   // TIMESTAMPTZ / DateTime64(3) / i64 unix ms
	TypeDate          FieldType = "date"        // DATE / Date / i32 days since epoch
	TypeJSON          FieldType = "json"        // JSONB / String / text
	TypeBytes         FieldType = "bytes"       // BYTEA / String / bytes
	TypeStringArray   FieldType = "string_array"
)

// IsFloat reports whether t is a floating-point type (forbidden on financial entities).
func (t FieldType) IsFloat() bool {
	return t == TypeFloat32 || t == TypeFloat64
}

// IsMonetary reports whether t is a fixed-point integer monetary type.
func (t FieldType) IsMonetary() bool {
	return t == TypeMoneyMinor || t == TypeMoneyNano
}

// Valid reports whether t is a recognized canonical FieldType.
func (t FieldType) Valid() bool {
	switch t {
	case TypeString, TypeLowCardString, TypeBool,
		TypeInt8, TypeInt16, TypeInt32, TypeInt64,
		TypeUint8, TypeUint16, TypeUint32, TypeUint64,
		TypeFloat32, TypeFloat64,
		TypeMoneyMinor, TypeMoneyNano,
		TypeTimestamp, TypeDate,
		TypeJSON, TypeBytes, TypeStringArray:
		return true
	default:
		return false
	}
}

// EntityKind classifies an entity's lifecycle and storage semantics across
// transactional (SQL) and analytical (ClickHouse) planes.
type EntityKind string

const (
	// KindDim is a conformed shared dimension (analytics.dim_*).
	// ClickHouse engine: ReplacingMergeTree(<version_or_updated_at>).
	KindDim EntityKind = "dim"

	// KindState is a mutable latest-state snapshot (analytics.state_*).
	// ClickHouse engine: ReplacingMergeTree(version) with optional tombstone.
	// INVARIANT: KindState MUST NEVER feed a SummingMergeTree or
	// AggregatingMergeTree Materialized View (HIP-1405 §4.1).
	KindState EntityKind = "state"

	// KindFact is an immutable append-only flow or telemetry fact
	// (analytics.fact_*, hanzo.*, event.*).
	// ClickHouse engine: MergeTree() with PARTITION BY toYYYYMM(<time_col>).
	// Safe source for L1 incremental Materialized Views.
	KindFact EntityKind = "fact"
)

// Valid reports whether k is a known EntityKind.
func (k EntityKind) Valid() bool {
	switch k {
	case KindDim, KindState, KindFact:
		return true
	default:
		return false
	}
}

// Plane selects which compilation targets an entity participates in.
type Plane uint8

const (
	PlaneSQL       Plane = 1 << iota // hanzoai/sql (PostgreSQL / SQLite)
	PlaneDatastore                   // hanzoai/datastore (ClickHouse)
	PlaneZAP                         // ZAP binary wire protocol (.zap IDL)
	PlaneCatalog                     // Governance & Analytics Catalog / OpenAPI / MCP

	PlaneAll = PlaneSQL | PlaneDatastore | PlaneZAP | PlaneCatalog
)

// Has reports whether p includes flag.
func (p Plane) Has(flag Plane) bool {
	return p&flag == flag
}

// String renders a comma-separated list of enabled planes.
func (p Plane) String() string {
	var parts []string
	if p.Has(PlaneSQL) {
		parts = append(parts, "sql")
	}
	if p.Has(PlaneDatastore) {
		parts = append(parts, "datastore")
	}
	if p.Has(PlaneZAP) {
		parts = append(parts, "zap")
	}
	if p.Has(PlaneCatalog) {
		parts = append(parts, "catalog")
	}
	return strings.Join(parts, ",")
}

// ParsePlanes parses a comma-separated plane list (e.g. "sql,datastore,zap,catalog").
func ParsePlanes(s string) (Plane, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "all") {
		return PlaneAll, nil
	}
	var p Plane
	for _, tok := range strings.Split(s, ",") {
		switch strings.ToLower(strings.TrimSpace(tok)) {
		case "sql":
			p |= PlaneSQL
		case "datastore", "clickhouse", "ch":
			p |= PlaneDatastore
		case "zap":
			p |= PlaneZAP
		case "catalog", "mcp", "openapi":
			p |= PlaneCatalog
		case "":
		default:
			return 0, fmt.Errorf("schema: unknown plane %q", tok)
		}
	}
	if p == 0 {
		return 0, fmt.Errorf("schema: empty plane set %q", s)
	}
	return p, nil
}

// TenantSpec governs multi-tenant isolation across SQL and Datastore.
type TenantSpec struct {
	// Column is the canonical tenant partition column (default "org").
	Column string `json:"column"`
	// Isolated enforces that Column leads SQL primary keys and every SQL index.
	Isolated bool `json:"isolated"`
	// RLS enables emitting PostgreSQL Row-Level Security DDL matching orm/tenant.
	RLS bool `json:"rls,omitempty"`
}

// Field defines one column / wire slot of an Entity.
type Field struct {
	Name       string    `json:"name"`                  // canonical snake_case storage name
	GoName     string    `json:"go_name,omitempty"`     // PascalCase Go field name
	Type       FieldType `json:"type"`                  // canonical FieldType
	Nullable   bool      `json:"nullable,omitempty"`    // SQL NULL allowed (never on PK/tenant)
	PrimaryKey bool      `json:"primary_key,omitempty"` // part of SQL PRIMARY KEY
	Unique     bool      `json:"unique,omitempty"`      // single-column SQL UNIQUE index
	Indexed    bool      `json:"indexed,omitempty"`     // single-column SQL index
	Default    string    `json:"default,omitempty"`     // SQL literal default (e.g. "''", "0", "false")
	Version    bool      `json:"version,omitempty"`     // ReplacingMergeTree version column
	Timestamp  bool      `json:"timestamp,omitempty"`   // primary event/record timestamp
	Partition  bool      `json:"partition,omitempty"`   // ClickHouse PARTITION BY toYYYYMM(col)
	OrderRank  int       `json:"order_rank,omitempty"`  // 1-based position in ClickHouse ORDER BY
	Sign       bool      `json:"sign,omitempty"`        // signed ledger multiplier (+1 / -1 Int8)
	Tombstone  bool      `json:"tombstone,omitempty"`   // soft-delete flag (deleted / is_deleted)
	PII        bool      `json:"pii,omitempty"`         // redacted by default in bot/MCP queries
	Unit       string    `json:"unit,omitempty"`        // semantic unit (minor_usd, nano_usd, ns, bytes, ...)
	ZAPOrdinal int       `json:"zap_ordinal,omitempty"` // 1-based override (0 = declaration order)
	Comment    string    `json:"comment,omitempty"`
}

// IndexSpec declares a SQL index (composite or single-column).
// When Entity.Tenant.Isolated is true, the tenant column is automatically
// prepended if not already leading Columns.
type IndexSpec struct {
	Name    string   `json:"name,omitempty"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique,omitempty"`
}

// RollupEngine names a ClickHouse incremental rollup engine.
type RollupEngine string

const (
	EngineAggregatingMergeTree RollupEngine = "AggregatingMergeTree"
	EngineSummingMergeTree     RollupEngine = "SummingMergeTree"
)

// BucketKind is a ClickHouse time truncation bucket for L1 rollups.
type BucketKind string

const (
	BucketMinute BucketKind = "minute"
	BucketHour   BucketKind = "hour"
	BucketDay    BucketKind = "day"
)

// RollupDim defines one GROUP BY dimension in a ClickHouse L1 rollup.
type RollupDim struct {
	// TargetCol is the column name in the rollup target table.
	TargetCol string `json:"target_col"`
	// SourceCol is the raw column name in the source table.
	// Defaults to TargetCol when empty.
	SourceCol string `json:"source_col,omitempty"`
	// SourceExpr optionally transforms SourceCol (e.g. "toLowCardinality(organization)").
	// ClickHouse 26.2 safety rule: SourceExpr MUST NOT wrap SourceCol and alias it
	// back to the exact same identifier if that identifier is used in GROUP BY;
	// use a distinct TargetCol or align source/target column types.
	SourceExpr string    `json:"source_expr,omitempty"`
	Type       FieldType `json:"type"`
}

// RollupMeasure defines one aggregated measure column in a ClickHouse L1 rollup.
type RollupMeasure struct {
	// TargetCol is the column name in the rollup target table.
	TargetCol string `json:"target_col"`
	// AggFunc is the ClickHouse aggregate function for AggregatingMergeTree
	// (e.g. "count", "sum", "uniq", "quantileTDigest(0.50)").
	AggFunc string `json:"agg_func,omitempty"`
	// ValueType is the ClickHouse type argument for AggregateFunction(func, T)
	// or the column type for SummingMergeTree (e.g. "Int64", "UInt64", "String").
	// Omit for zero-argument functions like "count".
	ValueType string `json:"value_type,omitempty"`
	// StateExpr is the exact SELECT expression in the Materialized View
	// (e.g. "sumState(toInt64(total_tokens))" or "sum(toInt64(sign) * amount_minor)").
	StateExpr string `json:"state_expr"`
	// Unit documents the measure unit (e.g. "minor_usd", "nano_usd", "count", "ns").
	Unit string `json:"unit,omitempty"`
}

// RollupSpec declares an L1 incremental ClickHouse rollup table and its
// attached Materialized View, derived from an immutable KindFact entity.
type RollupSpec struct {
	Name         string          `json:"name"` // e.g. "rollup_ai_hourly"
	Database     string          `json:"database"`
	MVName       string          `json:"mv_name,omitempty"` // default Name + "_mv"
	Engine       RollupEngine    `json:"engine"`
	SourceEntity string          `json:"source_entity"` // canonical Entity.Name (must be KindFact)
	TimeSource   string          `json:"time_source"`   // source timestamp column
	TimeTarget   string          `json:"time_target"`   // target bucket column ("hour", "day")
	Bucket       BucketKind      `json:"bucket"`
	Dimensions   []RollupDim     `json:"dimensions"`
	Measures     []RollupMeasure `json:"measures"`
	TTL          string          `json:"ttl,omitempty"` // e.g. "hour + INTERVAL 90 DAY"
	Description  string          `json:"description,omitempty"`
}

// SnapshotSpec declares an L3 pre-materialized serving snapshot table in
// ClickHouse backed by ReplacingMergeTree(refreshed_at).
//
// L3 snapshots serve global/org KPIs in O(1) constant time with zero raw-table
// scans and zero ClickHouse 26.2 alias shadowing.
type SnapshotSpec struct {
	Name              string   `json:"name"`               // e.g. "snap_global_overview"
	Database          string   `json:"database"`           // default "analytics"
	Fields            []Field  `json:"fields"`             // schema columns (refreshed_at auto-added if absent)
	OrderBy           []string `json:"order_by"`           // ClickHouse ORDER BY key
	RefreshSelectSQL  string   `json:"refresh_select_sql"` // ClickHouse 26.2-safe SELECT feeding INSERT INTO ...
	FreshnessTargetMS uint32   `json:"freshness_target_ms"`
	Domain            string   `json:"domain"`
	Description       string   `json:"description,omitempty"`
}

// MetricSpec declares one governed semantic metric in `analytics.catalog`.
type MetricSpec struct {
	Metric            string   `json:"metric"` // e.g. "commerce.mrr_minor"
	Domain            string   `json:"domain"`
	Type              string   `json:"type"` // currency | gauge | counter | ratio | latency
	Unit              string   `json:"unit"` // minor_usd | nano_usd | count | ns | bytes | ratio
	Aggregation       string   `json:"aggregation"`
	Dimensions        []string `json:"dimensions"`
	ServingTable      string   `json:"serving_table"`
	FreshnessTargetMS uint32   `json:"freshness_target_ms"`
	Accuracy          string   `json:"accuracy"` // exact | approximate_hll | approximate_tdigest
	Description       string   `json:"description"`
}

// CatalogMeta carries governance, SLA, retention, and semantic catalog
// metadata for an Entity.
type CatalogMeta struct {
	Domain            string       `json:"domain"`
	OwnerTeam         string       `json:"owner_team,omitempty"`
	Retention         string       `json:"retention,omitempty"` // ClickHouse TTL interval e.g. "2 YEAR"
	FreshnessTargetMS uint32       `json:"freshness_target_ms,omitempty"`
	Accuracy          string       `json:"accuracy,omitempty"` // exact | approximate_hll | approximate_tdigest
	Financial         bool         `json:"financial,omitempty"`
	Description       string       `json:"description,omitempty"`
	Metrics           []MetricSpec `json:"metrics,omitempty"`
}

// Entity is the single canonical schema definition for a Hanzo domain object.
type Entity struct {
	Name           string       `json:"name"`                      // PascalCase canonical name (e.g. "OrgBalance")
	Kind           EntityKind   `json:"kind"`                      // dim | state | fact
	Planes         Plane        `json:"planes"`                    // SQL | Datastore | ZAP | Catalog
	SQLSchema      string       `json:"sql_schema,omitempty"`      // PostgreSQL schema (default "public")
	SQLTable       string       `json:"sql_table,omitempty"`       // PostgreSQL/SQLite table name
	DatastoreDB    string       `json:"datastore_db,omitempty"`    // ClickHouse database (default "analytics")
	DatastoreTable string       `json:"datastore_table,omitempty"` // ClickHouse table name
	ZAPPackage     string       `json:"zap_package,omitempty"`     // ZAP IDL package (default "hanzo.schema")
	Tenant         TenantSpec   `json:"tenant"`
	Fields         []Field      `json:"fields"`
	Indexes        []IndexSpec  `json:"indexes,omitempty"`
	OrderBy        []string     `json:"order_by,omitempty"`     // explicit ClickHouse ORDER BY override
	PartitionBy    string       `json:"partition_by,omitempty"` // explicit ClickHouse PARTITION BY override
	VersionCol     string       `json:"version_col,omitempty"`  // explicit ReplacingMergeTree version column override
	TTL            string       `json:"ttl,omitempty"`          // ClickHouse TTL expression (e.g. "timestamp + INTERVAL 2 YEAR")
	Catalog        CatalogMeta  `json:"catalog"`
	Rollups        []RollupSpec `json:"rollups,omitempty"`
}

// FullDatastoreName returns "<db>.<table>" for ClickHouse.
func (e *Entity) FullDatastoreName() string {
	db := e.DatastoreDB
	if db == "" {
		db = "analytics"
	}
	tbl := e.DatastoreTable
	if tbl == "" {
		tbl = e.defaultDatastoreTable()
	}
	return db + "." + tbl
}

// FullSQLName returns "<schema>.<table>" (or bare "<table>" when schema is empty/public).
func (e *Entity) FullSQLName() string {
	tbl := e.SQLTable
	if tbl == "" {
		tbl = toSnake(e.Name)
	}
	if e.SQLSchema == "" || e.SQLSchema == "public" {
		return tbl
	}
	return e.SQLSchema + "." + tbl
}

func (e *Entity) defaultDatastoreTable() string {
	base := toSnake(e.Name)
	switch e.Kind {
	case KindDim:
		return "dim_" + base
	case KindState:
		return "state_" + base
	case KindFact:
		return "fact_" + base
	default:
		return base
	}
}

// FieldByName returns the Field with canonical storage name, or nil.
func (e *Entity) FieldByName(name string) *Field {
	for i := range e.Fields {
		if e.Fields[i].Name == name {
			return &e.Fields[i]
		}
	}
	return nil
}

// PrimaryKeys returns the ordered SQL primary key columns (with tenant column
// leading when Tenant.Isolated is true).
func (e *Entity) PrimaryKeys() []string {
	var pks []string
	tenantCol := e.tenantColumn()
	if e.Tenant.Isolated && tenantCol != "" {
		pks = append(pks, tenantCol)
	}
	for _, f := range e.Fields {
		if f.PrimaryKey && !slices.Contains(pks, f.Name) {
			pks = append(pks, f.Name)
		}
	}
	return pks
}

func (e *Entity) tenantColumn() string {
	if e.Tenant.Column != "" {
		return e.Tenant.Column
	}
	if e.Tenant.Isolated {
		return "org"
	}
	return ""
}

// ResolvedVersionCol returns the ReplacingMergeTree version column name.
func (e *Entity) ResolvedVersionCol() string {
	if e.VersionCol != "" {
		return e.VersionCol
	}
	for _, f := range e.Fields {
		if f.Version {
			return f.Name
		}
	}
	if f := e.FieldByName("version"); f != nil {
		return f.Name
	}
	if f := e.FieldByName("updated_at"); f != nil {
		return f.Name
	}
	return ""
}

// ResolvedOrderBy returns the deterministic ClickHouse ORDER BY column list.
func (e *Entity) ResolvedOrderBy() []string {
	if len(e.OrderBy) > 0 {
		return slices.Clone(e.OrderBy)
	}
	type ranked struct {
		name string
		rank int
		idx  int
	}
	var explicit []ranked
	for i, f := range e.Fields {
		if f.OrderRank > 0 {
			explicit = append(explicit, ranked{name: f.Name, rank: f.OrderRank, idx: i})
		}
	}
	if len(explicit) > 0 {
		sort.Slice(explicit, func(i, j int) bool {
			if explicit[i].rank != explicit[j].rank {
				return explicit[i].rank < explicit[j].rank
			}
			return explicit[i].idx < explicit[j].idx
		})
		out := make([]string, len(explicit))
		for i, r := range explicit {
			out[i] = r.name
		}
		return out
	}
	// Fallback: for facts, lead with timestamp then tenant then PK; for dim/state, lead with PK.
	var cols []string
	if e.Kind == KindFact {
		for _, f := range e.Fields {
			if f.Timestamp {
				cols = append(cols, f.Name)
				break
			}
		}
	}
	for _, pk := range e.PrimaryKeys() {
		if !slices.Contains(cols, pk) {
			cols = append(cols, pk)
		}
	}
	return cols
}

// ResolvedPartitionBy returns the ClickHouse PARTITION BY expression, if any.
func (e *Entity) ResolvedPartitionBy() string {
	if e.PartitionBy != "" {
		return e.PartitionBy
	}
	for _, f := range e.Fields {
		if f.Partition {
			if f.Type == TypeTimestamp || f.Type == TypeDate {
				return fmt.Sprintf("toYYYYMM(%s)", f.Name)
			}
			return f.Name
		}
	}
	if e.Kind == KindFact {
		for _, f := range e.Fields {
			if f.Timestamp {
				return fmt.Sprintf("toYYYYMM(%s)", f.Name)
			}
		}
	}
	return ""
}

// Registry holds a validated, deterministic set of Entities, Rollups, and
// L3 Serving Snapshots that compile together as one global schema graph.
type Registry struct {
	Namespace string
	entities  []*Entity
	byName    map[string]*Entity
	snapshots []SnapshotSpec
	snapByKey map[string]int
}

// NewRegistry creates an empty schema Registry under namespace (default "hanzo.schema").
func NewRegistry(namespace string) *Registry {
	if namespace == "" {
		namespace = "hanzo.schema"
	}
	return &Registry{
		Namespace: namespace,
		byName:    make(map[string]*Entity),
		snapByKey: make(map[string]int),
	}
}

// Register adds an Entity to the registry after validating it.
func (r *Registry) Register(e *Entity) error {
	if e == nil {
		return fmt.Errorf("schema: cannot register nil entity")
	}
	if e.ZAPPackage == "" {
		e.ZAPPackage = r.Namespace
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if _, dup := r.byName[e.Name]; dup {
		return fmt.Errorf("schema: duplicate entity %q", e.Name)
	}
	r.entities = append(r.entities, e)
	r.byName[e.Name] = e
	return nil
}

// RegisterSnapshot adds an L3 serving snapshot (`ReplacingMergeTree(refreshed_at)`).
func (r *Registry) RegisterSnapshot(s SnapshotSpec) error {
	if s.Database == "" {
		s.Database = "analytics"
	}
	if err := s.Validate(); err != nil {
		return err
	}
	key := s.Database + "." + s.Name
	if _, dup := r.snapByKey[key]; dup {
		return fmt.Errorf("schema: duplicate snapshot %q", key)
	}
	r.snapByKey[key] = len(r.snapshots)
	r.snapshots = append(r.snapshots, s)
	return nil
}

// Entity returns the registered entity by canonical name.
func (r *Registry) Entity(name string) (*Entity, bool) {
	e, ok := r.byName[name]
	return e, ok
}

// Entities returns all registered entities in registration order.
func (r *Registry) Entities() []*Entity {
	return slices.Clone(r.entities)
}

// Snapshots returns all registered L3 serving snapshots in registration order.
func (r *Registry) Snapshots() []SnapshotSpec {
	return slices.Clone(r.snapshots)
}

// Fingerprint returns a deterministic SHA-256 hex digest over the entire
// Registry IR for CI drift gates and runtime schema-version negotiation.
func (r *Registry) Fingerprint() string {
	type payload struct {
		Namespace string         `json:"namespace"`
		Entities  []*Entity      `json:"entities"`
		Snapshots []SnapshotSpec `json:"snapshots"`
	}
	b, _ := json.Marshal(payload{
		Namespace: r.Namespace,
		Entities:  r.entities,
		Snapshots: r.snapshots,
	})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// toSnake converts PascalCase/camelCase identifiers to snake_case cleanly
// (handling common acronyms like ID, AI, MRR, O11y, FX, URL).
func toSnake(s string) string {
	if s == "" {
		return ""
	}
	var out strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		isUpper := r >= 'A' && r <= 'Z'
		if isUpper {
			if i > 0 {
				prev := runes[i-1]
				prevLower := prev >= 'a' && prev <= 'z'
				prevDigit := prev >= '0' && prev <= '9'
				nextLower := i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z'
				if prevLower || prevDigit || (prev >= 'A' && prev <= 'Z' && nextLower) {
					out.WriteByte('_')
				}
			}
			out.WriteRune(r + ('a' - 'A'))
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// toPascal converts snake_case to PascalCase.
func toPascal(s string) string {
	parts := strings.Split(s, "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		switch strings.ToLower(p) {
		case "id", "ai", "fx", "mrr", "ip", "url", "sql", "zap", "mcp", "rls", "ttl", "cpu", "gpu", "pii":
			b.WriteString(strings.ToUpper(p))
		default:
			b.WriteString(strings.ToUpper(p[:1]) + p[1:])
		}
	}
	return b.String()
}

// toCamel converts snake_case to lowerCamelCase for ZAP Cap'n style fields.
func toCamel(s string) string {
	p := toPascal(s)
	if p == "" {
		return ""
	}
	// Lowercase leading uppercase run (e.g. "ID" -> "id", "AIModel" -> "aiModel").
	runes := []rune(p)
	i := 0
	for i < len(runes) && runes[i] >= 'A' && runes[i] <= 'Z' {
		i++
	}
	if i == len(runes) {
		return strings.ToLower(p)
	}
	if i > 1 {
		i--
	}
	return strings.ToLower(string(runes[:i])) + string(runes[i:])
}
