// Copyright © 2026 Hanzo AI. MIT License.

package schema

import (
	"context"
	"fmt"
	"strings"
)

// DatastoreDDL describes one ClickHouse DDL statement along with its optional
// upstream source dependency (used to guard Materialized Views when external
// raw tables like `event.span` or `event.fact` are provisioned separately).
type DatastoreDDL struct {
	Name        string
	Kind        string // "database" | "table" | "rollup_table" | "materialized_view" | "snapshot_table" | "snapshot_refresh"
	Engine      string
	SourceDB    string
	SourceTable string
	SQL         string
}

// DatastoreBundle holds every ClickHouse DDL statement compiled from a Registry
// in strict topological creation order:
//
//	Databases -> L0 Tables -> L1 Rollup Tables -> L1 Materialized Views -> L3 Snapshot Tables
type DatastoreBundle struct {
	Databases         []DatastoreDDL
	Tables            []DatastoreDDL
	RollupTables      []DatastoreDDL
	MaterializedViews []DatastoreDDL
	SnapshotTables    []DatastoreDDL
	SnapshotRefreshes []DatastoreDDL
}

// AllDDL returns every CREATE statement (excluding periodic SnapshotRefreshes)
// in topological execution order.
func (b DatastoreBundle) AllDDL() []DatastoreDDL {
	out := make([]DatastoreDDL, 0, len(b.Databases)+len(b.Tables)+len(b.RollupTables)+len(b.MaterializedViews)+len(b.SnapshotTables))
	out = append(out, b.Databases...)
	out = append(out, b.Tables...)
	out = append(out, b.RollupTables...)
	out = append(out, b.MaterializedViews...)
	out = append(out, b.SnapshotTables...)
	return out
}

// Render returns all CREATE DDL statements formatted as a ClickHouse SQL script.
func (b DatastoreBundle) Render() string {
	all := b.AllDDL()
	if len(all) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, d := range all {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(strings.TrimSpace(d.SQL))
		if !strings.HasSuffix(strings.TrimSpace(d.SQL), ";") {
			sb.WriteByte(';')
		}
	}
	sb.WriteByte('\n')
	return sb.String()
}

// ExecFunc matches (*orm/datastore.Conn).Exec so DatastoreBundle.Apply works
// directly with hanzoai/orm/datastore without pulling in driver dependencies.
type ExecFunc func(ctx context.Context, stmt string, args ...any) error

// TableExistsFunc optionally checks whether an external source table exists
// before creating an attached Materialized View.
type TableExistsFunc func(ctx context.Context, db, table string) (bool, error)

// Apply idempotently executes every database, table, rollup, materialized view,
// and L3 snapshot DDL statement in topological order.
func (b DatastoreBundle) Apply(ctx context.Context, exec ExecFunc, exists TableExistsFunc) error {
	if exec == nil {
		return fmt.Errorf("schema: Apply requires a non-nil exec function")
	}
	for _, d := range b.Databases {
		if err := exec(ctx, d.SQL); err != nil {
			return fmt.Errorf("schema: ensure database %s: %w", d.Name, err)
		}
	}
	for _, d := range b.Tables {
		if err := exec(ctx, d.SQL); err != nil {
			return fmt.Errorf("schema: ensure table %s: %w", d.Name, err)
		}
	}
	for _, d := range b.RollupTables {
		if err := exec(ctx, d.SQL); err != nil {
			return fmt.Errorf("schema: ensure rollup table %s: %w", d.Name, err)
		}
	}
	for _, mv := range b.MaterializedViews {
		if exists != nil && mv.SourceDB != "" && mv.SourceTable != "" {
			ok, err := exists(ctx, mv.SourceDB, mv.SourceTable)
			if err != nil {
				return fmt.Errorf("schema: check source %s.%s for %s: %w", mv.SourceDB, mv.SourceTable, mv.Name, err)
			}
			if !ok {
				continue
			}
		}
		if err := exec(ctx, mv.SQL); err != nil {
			return fmt.Errorf("schema: ensure mv %s: %w", mv.Name, err)
		}
	}
	for _, d := range b.SnapshotTables {
		if err := exec(ctx, d.SQL); err != nil {
			return fmt.Errorf("schema: ensure snapshot %s: %w", d.Name, err)
		}
	}
	return nil
}

// CompileDatastoreTable compiles one Entity into a ClickHouse L0 table DDL.
func CompileDatastoreTable(e *Entity) (DatastoreDDL, error) {
	if err := e.Validate(); err != nil {
		return DatastoreDDL{}, err
	}
	if !e.Planes.Has(PlaneDatastore) {
		return DatastoreDDL{}, fmt.Errorf("schema: entity %s does not target PlaneDatastore", e.Name)
	}

	fullName := e.FullDatastoreName()
	var cols []string
	for _, f := range e.Fields {
		cols = append(cols, fmt.Sprintf("  %s %s", f.Name, chColumnType(f.Type)))
	}

	var engine string
	switch e.Kind {
	case KindDim, KindState:
		engine = fmt.Sprintf("ReplacingMergeTree(%s)", e.ResolvedVersionCol())
	case KindFact:
		engine = "MergeTree()"
	default:
		return DatastoreDDL{}, fmt.Errorf("schema: unsupported kind %q", e.Kind)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "CREATE TABLE IF NOT EXISTS %s (\n%s\n) ENGINE = %s", fullName, strings.Join(cols, ",\n"), engine)
	if part := e.ResolvedPartitionBy(); part != "" {
		fmt.Fprintf(&sb, "\nPARTITION BY %s", part)
	}
	fmt.Fprintf(&sb, "\nORDER BY (%s)", strings.Join(e.ResolvedOrderBy(), ", "))
	if e.TTL != "" {
		fmt.Fprintf(&sb, "\nTTL %s", e.TTL)
	}

	return DatastoreDDL{
		Name:   fullName,
		Kind:   "table",
		Engine: engine,
		SQL:    sb.String(),
	}, nil
}

// CompileRollup compiles one L1 RollupSpec into its target ClickHouse rollup
// table DDL and its ClickHouse 26.2-safe Materialized View DDL.
func CompileRollup(r RollupSpec, source *Entity) (tableDDL DatastoreDDL, mvDDL DatastoreDDL, err error) {
	if source == nil {
		return DatastoreDDL{}, DatastoreDDL{}, fmt.Errorf("schema: rollup %s requires a source entity", r.Name)
	}
	if err := r.Validate(source); err != nil {
		return DatastoreDDL{}, DatastoreDDL{}, err
	}

	db := r.Database
	if db == "" {
		db = "analytics"
	}
	targetFull := db + "." + r.Name
	mvName := r.MVName
	if mvName == "" {
		mvName = r.Name + "_mv"
	}
	mvFull := db + "." + mvName

	timeColType, bucketExpr := rollupBucketSQL(r.Bucket, r.TimeSource)

	var tableCols []string
	tableCols = append(tableCols, fmt.Sprintf("  %s %s", r.TimeTarget, timeColType))

	orderBy := []string{r.TimeTarget}
	for _, d := range r.Dimensions {
		tableCols = append(tableCols, fmt.Sprintf("  %s %s", d.TargetCol, chColumnType(d.Type)))
		orderBy = append(orderBy, d.TargetCol)
	}
	for _, m := range r.Measures {
		var colType string
		if r.Engine == EngineAggregatingMergeTree {
			if m.ValueType == "" {
				colType = fmt.Sprintf("AggregateFunction(%s)", m.AggFunc)
			} else {
				colType = fmt.Sprintf("AggregateFunction(%s, %s)", m.AggFunc, m.ValueType)
			}
		} else {
			colType = m.ValueType
		}
		tableCols = append(tableCols, fmt.Sprintf("  %s %s", m.TargetCol, colType))
	}

	var tblSB strings.Builder
	fmt.Fprintf(&tblSB, "CREATE TABLE IF NOT EXISTS %s (\n%s\n) ENGINE = %s()\nPARTITION BY toYYYYMM(%s)\nORDER BY (%s)",
		targetFull, strings.Join(tableCols, ",\n"), r.Engine, r.TimeTarget, strings.Join(orderBy, ", "))
	if r.TTL != "" {
		fmt.Fprintf(&tblSB, "\nTTL %s", r.TTL)
	}

	tableDDL = DatastoreDDL{
		Name:   targetFull,
		Kind:   "rollup_table",
		Engine: string(r.Engine) + "()",
		SQL:    tblSB.String(),
	}

	// Build ClickHouse 26.2-safe Materialized View SELECT (zero alias shadowing).
	var selectExprs []string
	selectExprs = append(selectExprs, fmt.Sprintf("  %s AS %s", bucketExpr, r.TimeTarget))

	for _, d := range r.Dimensions {
		srcCol := d.SourceCol
		if srcCol == "" {
			srcCol = d.TargetCol
		}
		switch {
		case d.SourceExpr != "":
			selectExprs = append(selectExprs, fmt.Sprintf("  %s AS %s", d.SourceExpr, d.TargetCol))
		case srcCol != d.TargetCol:
			// Safe rename (source != target): apply toLowCardinality when target is LowCardinality(String)
			// and source is plain String.
			sf := source.FieldByName(srcCol)
			if d.Type == TypeLowCardString && sf != nil && sf.Type != TypeLowCardString {
				selectExprs = append(selectExprs, fmt.Sprintf("  toLowCardinality(%s) AS %s", srcCol, d.TargetCol))
			} else {
				selectExprs = append(selectExprs, fmt.Sprintf("  %s AS %s", srcCol, d.TargetCol))
			}
		default:
			// Identical name in source and target: select bare column without
			// casting/aliasing to itself, avoiding ClickHouse 26.2 GROUP BY shadowing.
			selectExprs = append(selectExprs, fmt.Sprintf("  %s", d.TargetCol))
		}
	}

	for _, m := range r.Measures {
		selectExprs = append(selectExprs, fmt.Sprintf("  %s AS %s", m.StateExpr, m.TargetCol))
	}

	srcDB := source.DatastoreDB
	if srcDB == "" {
		srcDB = "analytics"
	}
	srcTable := source.DatastoreTable
	if srcTable == "" {
		srcTable = source.defaultDatastoreTable()
	}

	mvSQL := fmt.Sprintf("CREATE MATERIALIZED VIEW IF NOT EXISTS %s\nTO %s AS\nSELECT\n%s\nFROM %s.%s\nGROUP BY %s",
		mvFull, targetFull, strings.Join(selectExprs, ",\n"), srcDB, srcTable, strings.Join(orderBy, ", "))

	if err := CheckClickHouse262Safety(mvSQL); err != nil {
		return DatastoreDDL{}, DatastoreDDL{}, fmt.Errorf("schema: compiled MV %s failed ClickHouse 26.2 safety check: %w", mvFull, err)
	}

	mvDDL = DatastoreDDL{
		Name:        mvFull,
		Kind:        "materialized_view",
		Engine:      "MaterializedView",
		SourceDB:    srcDB,
		SourceTable: srcTable,
		SQL:         mvSQL,
	}
	return tableDDL, mvDDL, nil
}

// CompileSnapshot compiles one L3 SnapshotSpec into a ClickHouse
// ReplacingMergeTree(refreshed_at) table DDL and its refresh INSERT statement.
func CompileSnapshot(s SnapshotSpec) (tableDDL DatastoreDDL, refreshDDL DatastoreDDL, err error) {
	if err := s.Validate(); err != nil {
		return DatastoreDDL{}, DatastoreDDL{}, err
	}
	db := s.Database
	if db == "" {
		db = "analytics"
	}
	fullName := db + "." + s.Name

	var cols []string
	var colNames []string
	hasRefreshedAt := false
	for _, f := range s.Fields {
		cols = append(cols, fmt.Sprintf("  %s %s", f.Name, chColumnType(f.Type)))
		colNames = append(colNames, f.Name)
		if f.Name == "refreshed_at" {
			hasRefreshedAt = true
		}
	}
	if !hasRefreshedAt {
		cols = append(cols, "  refreshed_at DateTime64(3)")
		colNames = append(colNames, "refreshed_at")
	}

	tblSQL := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n%s\n) ENGINE = ReplacingMergeTree(refreshed_at)\nORDER BY (%s)",
		fullName, strings.Join(cols, ",\n"), strings.Join(s.OrderBy, ", "))

	tableDDL = DatastoreDDL{
		Name:   fullName,
		Kind:   "snapshot_table",
		Engine: "ReplacingMergeTree(refreshed_at)",
		SQL:    tblSQL,
	}

	if strings.TrimSpace(s.RefreshSelectSQL) != "" {
		refreshSQL := fmt.Sprintf("INSERT INTO %s (%s)\n%s",
			fullName, strings.Join(colNames, ", "), strings.TrimSpace(s.RefreshSelectSQL))
		refreshDDL = DatastoreDDL{
			Name: fullName,
			Kind: "snapshot_refresh",
			SQL:  refreshSQL,
		}
	}
	return tableDDL, refreshDDL, nil
}

// CompileDatastore compiles every PlaneDatastore entity, attached L1 rollup,
// Materialized View, and L3 serving snapshot in the Registry.
func (r *Registry) CompileDatastore() (DatastoreBundle, error) {
	if err := r.Validate(); err != nil {
		return DatastoreBundle{}, err
	}

	var bundle DatastoreBundle
	seenDB := map[string]bool{}
	addDB := func(db string) {
		if db == "" {
			db = "analytics"
		}
		if seenDB[db] {
			return
		}
		seenDB[db] = true
		bundle.Databases = append(bundle.Databases, DatastoreDDL{
			Name: db,
			Kind: "database",
			SQL:  fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", db),
		})
	}

	for _, e := range r.entities {
		if !e.Planes.Has(PlaneDatastore) {
			continue
		}
		addDB(e.DatastoreDB)
		tbl, err := CompileDatastoreTable(e)
		if err != nil {
			return DatastoreBundle{}, err
		}
		bundle.Tables = append(bundle.Tables, tbl)

		for _, rp := range e.Rollups {
			src := e
			if rp.SourceEntity != "" && rp.SourceEntity != e.Name {
				src = r.byName[rp.SourceEntity]
			}
			addDB(rp.Database)
			rTbl, mv, err := CompileRollup(rp, src)
			if err != nil {
				return DatastoreBundle{}, err
			}
			bundle.RollupTables = append(bundle.RollupTables, rTbl)
			bundle.MaterializedViews = append(bundle.MaterializedViews, mv)
		}
	}

	for _, snap := range r.snapshots {
		addDB(snap.Database)
		sTbl, sRef, err := CompileSnapshot(snap)
		if err != nil {
			return DatastoreBundle{}, err
		}
		bundle.SnapshotTables = append(bundle.SnapshotTables, sTbl)
		if sRef.SQL != "" {
			bundle.SnapshotRefreshes = append(bundle.SnapshotRefreshes, sRef)
		}
	}

	return bundle, nil
}

func rollupBucketSQL(bucket BucketKind, srcCol string) (colType string, expr string) {
	switch bucket {
	case BucketMinute:
		return "DateTime", fmt.Sprintf("toDateTime(toStartOfMinute(%s))", srcCol)
	case BucketHour:
		return "DateTime", fmt.Sprintf("toDateTime(toStartOfHour(%s))", srcCol)
	case BucketDay:
		return "Date", fmt.Sprintf("toDate(%s)", srcCol)
	default:
		return "DateTime", fmt.Sprintf("toDateTime(toStartOfHour(%s))", srcCol)
	}
}

func chColumnType(ft FieldType) string {
	switch ft {
	case TypeString:
		return "String"
	case TypeLowCardString:
		return "LowCardinality(String)"
	case TypeBool:
		return "UInt8"
	case TypeInt8:
		return "Int8"
	case TypeInt16:
		return "Int16"
	case TypeInt32:
		return "Int32"
	case TypeInt64, TypeMoneyMinor, TypeMoneyNano:
		return "Int64"
	case TypeUint8:
		return "UInt8"
	case TypeUint16:
		return "UInt16"
	case TypeUint32:
		return "UInt32"
	case TypeUint64:
		return "UInt64"
	case TypeFloat32:
		return "Float32"
	case TypeFloat64:
		return "Float64"
	case TypeTimestamp:
		return "DateTime64(3)"
	case TypeDate:
		return "Date"
	case TypeJSON, TypeBytes:
		return "String"
	case TypeStringArray:
		return "Array(String)"
	default:
		return "String"
	}
}
