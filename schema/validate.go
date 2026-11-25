// Copyright © 2026 Hanzo AI. MIT License.

package schema

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// ValidIdent reports whether s is a safe bare SQL / ClickHouse / ZAP identifier.
func ValidIdent(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// ValidDottedIdent validates a dotted identifier like "hanzo.schema" or "analytics.dim_orgs".
func ValidDottedIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, seg := range strings.Split(s, ".") {
		if !ValidIdent(seg) {
			return false
		}
	}
	return true
}

// Validate enforces structural, tenant-isolation, financial-exactness, and
// ClickHouse-correctness invariants on an Entity.
func (e *Entity) Validate() error {
	if !ValidIdent(e.Name) {
		return fmt.Errorf("schema: invalid entity name %q", e.Name)
	}
	if !e.Kind.Valid() {
		return fmt.Errorf("schema: entity %s has invalid kind %q", e.Name, e.Kind)
	}
	if e.Planes == 0 {
		return fmt.Errorf("schema: entity %s enables no compilation planes", e.Name)
	}
	if e.SQLSchema != "" && !ValidIdent(e.SQLSchema) {
		return fmt.Errorf("schema: entity %s has invalid sql_schema %q", e.Name, e.SQLSchema)
	}
	if e.SQLTable != "" && !ValidIdent(e.SQLTable) {
		return fmt.Errorf("schema: entity %s has invalid sql_table %q", e.Name, e.SQLTable)
	}
	if e.DatastoreDB != "" && !ValidIdent(e.DatastoreDB) {
		return fmt.Errorf("schema: entity %s has invalid datastore_db %q", e.Name, e.DatastoreDB)
	}
	if e.DatastoreTable != "" && !ValidIdent(e.DatastoreTable) {
		return fmt.Errorf("schema: entity %s has invalid datastore_table %q", e.Name, e.DatastoreTable)
	}
	if e.ZAPPackage != "" && !ValidDottedIdent(e.ZAPPackage) {
		return fmt.Errorf("schema: entity %s has invalid zap_package %q", e.Name, e.ZAPPackage)
	}
	if len(e.Fields) == 0 {
		return fmt.Errorf("schema: entity %s has no fields", e.Name)
	}

	seen := make(map[string]Field, len(e.Fields))
	seenOrd := make(map[int]string)
	for _, f := range e.Fields {
		if !ValidIdent(f.Name) {
			return fmt.Errorf("schema: entity %s has invalid field name %q", e.Name, f.Name)
		}
		if !f.Type.Valid() {
			return fmt.Errorf("schema: entity %s.%s has invalid type %q", e.Name, f.Name, f.Type)
		}
		if _, dup := seen[f.Name]; dup {
			return fmt.Errorf("schema: entity %s declares duplicate field %q", e.Name, f.Name)
		}
		if f.PrimaryKey && f.Nullable {
			return fmt.Errorf("schema: entity %s.%s cannot be both primary key and nullable", e.Name, f.Name)
		}
		if e.Catalog.Financial && f.Type.IsFloat() {
			return fmt.Errorf("schema: financial entity %s.%s MUST NOT use floating-point type %s (use money_minor or money_nano)", e.Name, f.Name, f.Type)
		}
		if f.ZAPOrdinal < 0 {
			return fmt.Errorf("schema: entity %s.%s has negative zap_ordinal %d", e.Name, f.Name, f.ZAPOrdinal)
		}
		if f.ZAPOrdinal > 0 {
			if prev, dup := seenOrd[f.ZAPOrdinal]; dup {
				return fmt.Errorf("schema: entity %s fields %q and %q share zap_ordinal %d", e.Name, prev, f.Name, f.ZAPOrdinal)
			}
			seenOrd[f.ZAPOrdinal] = f.Name
		}
		seen[f.Name] = f
	}

	// Tenant isolation invariant.
	if e.Tenant.Isolated {
		tc := e.tenantColumn()
		if !ValidIdent(tc) {
			return fmt.Errorf("schema: entity %s has invalid tenant column %q", e.Name, tc)
		}
		tf, ok := seen[tc]
		if !ok {
			return fmt.Errorf("schema: tenant-isolated entity %s is missing tenant column %q", e.Name, tc)
		}
		if tf.Nullable {
			return fmt.Errorf("schema: tenant column %s.%s MUST NOT be nullable", e.Name, tc)
		}
	}

	// SQL invariants.
	if e.Planes.Has(PlaneSQL) {
		if len(e.PrimaryKeys()) == 0 {
			return fmt.Errorf("schema: SQL entity %s must declare at least one primary key column", e.Name)
		}
		for _, idx := range e.Indexes {
			if len(idx.Columns) == 0 {
				return fmt.Errorf("schema: entity %s has an index with no columns", e.Name)
			}
			for _, col := range idx.Columns {
				if _, ok := seen[col]; !ok {
					return fmt.Errorf("schema: entity %s index references unknown column %q", e.Name, col)
				}
			}
		}
	}

	// Datastore (ClickHouse) invariants.
	if e.Planes.Has(PlaneDatastore) {
		orderBy := e.ResolvedOrderBy()
		if len(orderBy) == 0 {
			return fmt.Errorf("schema: datastore entity %s must have a non-empty ORDER BY key", e.Name)
		}
		for _, col := range orderBy {
			if _, ok := seen[col]; !ok {
				return fmt.Errorf("schema: entity %s ORDER BY references unknown column %q", e.Name, col)
			}
		}
		switch e.Kind {
		case KindDim, KindState:
			ver := e.ResolvedVersionCol()
			if ver == "" {
				return fmt.Errorf("schema: %s entity %s requires a version or updated_at column for ReplacingMergeTree", e.Kind, e.Name)
			}
			if _, ok := seen[ver]; !ok {
				return fmt.Errorf("schema: entity %s version column %q does not exist", e.Name, ver)
			}
		case KindFact:
			// Fact tables should be immutable append-only; check partition col if explicit.
		}
	}

	// HIP-1405 §4.1 invariant: mutable ReplacingMergeTree (KindState/KindDim) MUST NEVER
	// attach incremental SummingMergeTree / AggregatingMergeTree rollups.
	if len(e.Rollups) > 0 && e.Kind != KindFact {
		return fmt.Errorf("schema: HIP-1405 invariant violation: %s entity %s (ReplacingMergeTree) MUST NOT feed incremental rollup Materialized Views; emit an immutable KindFact entity instead", e.Kind, e.Name)
	}
	for _, r := range e.Rollups {
		if err := r.Validate(e); err != nil {
			return err
		}
	}

	return nil
}

// Validate checks a RollupSpec against its source Entity and ClickHouse 26.2 rules.
func (r *RollupSpec) Validate(source *Entity) error {
	if !ValidIdent(r.Name) {
		return fmt.Errorf("schema: invalid rollup name %q", r.Name)
	}
	db := r.Database
	if db == "" {
		db = "analytics"
	}
	if !ValidIdent(db) {
		return fmt.Errorf("schema: rollup %s has invalid database %q", r.Name, db)
	}
	if r.Engine != EngineAggregatingMergeTree && r.Engine != EngineSummingMergeTree {
		return fmt.Errorf("schema: rollup %s has unsupported engine %q", r.Name, r.Engine)
	}
	if source != nil && source.Kind != KindFact {
		return fmt.Errorf("schema: HIP-1405 invariant violation: rollup %s cannot read from %s entity %s (ReplacingMergeTree); source MUST be immutable KindFact", r.Name, source.Kind, source.Name)
	}
	if !ValidIdent(r.TimeSource) || !ValidIdent(r.TimeTarget) {
		return fmt.Errorf("schema: rollup %s requires valid time_source (%q) and time_target (%q)", r.Name, r.TimeSource, r.TimeTarget)
	}
	if r.TimeSource == r.TimeTarget {
		return fmt.Errorf("schema: ClickHouse 26.2 alias-shadowing hazard in rollup %s: time_target %q MUST differ from time_source %q (e.g. timestamp -> hour/day)", r.Name, r.TimeTarget, r.TimeSource)
	}
	if source != nil && source.FieldByName(r.TimeSource) == nil {
		return fmt.Errorf("schema: rollup %s time_source %q does not exist on entity %s", r.Name, r.TimeSource, source.Name)
	}
	switch r.Bucket {
	case BucketMinute, BucketHour, BucketDay:
	default:
		return fmt.Errorf("schema: rollup %s has invalid bucket %q", r.Name, r.Bucket)
	}
	if len(r.Measures) == 0 {
		return fmt.Errorf("schema: rollup %s must declare at least one measure", r.Name)
	}

	seenCols := map[string]bool{r.TimeTarget: true}
	for _, d := range r.Dimensions {
		if !ValidIdent(d.TargetCol) {
			return fmt.Errorf("schema: rollup %s has invalid dimension target %q", r.Name, d.TargetCol)
		}
		if seenCols[d.TargetCol] {
			return fmt.Errorf("schema: rollup %s declares duplicate column %q", r.Name, d.TargetCol)
		}
		seenCols[d.TargetCol] = true

		srcCol := d.SourceCol
		if srcCol == "" {
			srcCol = d.TargetCol
		}
		if source != nil && source.FieldByName(srcCol) == nil {
			return fmt.Errorf("schema: rollup %s dimension %q references unknown source column %q on %s", r.Name, d.TargetCol, srcCol, source.Name)
		}
		// ClickHouse 26.2 safety: never apply a transform expression to a column
		// and alias it back to the exact same identifier when it appears in GROUP BY.
		if d.SourceExpr != "" && d.SourceExpr != d.TargetCol && containsIdent(d.SourceExpr, d.TargetCol) {
			return fmt.Errorf("schema: ClickHouse 26.2 alias-shadowing hazard in rollup %s: dimension expr %q aliases back to input column %q", r.Name, d.SourceExpr, d.TargetCol)
		}
		if !d.Type.Valid() {
			return fmt.Errorf("schema: rollup %s dimension %q has invalid type %q", r.Name, d.TargetCol, d.Type)
		}
	}

	for _, m := range r.Measures {
		if !ValidIdent(m.TargetCol) {
			return fmt.Errorf("schema: rollup %s has invalid measure target %q", r.Name, m.TargetCol)
		}
		if seenCols[m.TargetCol] {
			return fmt.Errorf("schema: rollup %s declares duplicate column %q", r.Name, m.TargetCol)
		}
		seenCols[m.TargetCol] = true
		if strings.TrimSpace(m.StateExpr) == "" {
			return fmt.Errorf("schema: rollup %s measure %q missing state_expr", r.Name, m.TargetCol)
		}
		if source != nil && source.Catalog.Financial && (strings.Contains(m.ValueType, "Float") || strings.Contains(m.StateExpr, "toFloat")) {
			return fmt.Errorf("schema: financial rollup %s measure %q MUST NOT use Float types", r.Name, m.TargetCol)
		}
		if r.Engine == EngineAggregatingMergeTree && m.AggFunc == "" {
			return fmt.Errorf("schema: AggregatingMergeTree rollup %s measure %q requires agg_func", r.Name, m.TargetCol)
		}
		if r.Engine == EngineSummingMergeTree && m.ValueType == "" {
			return fmt.Errorf("schema: SummingMergeTree rollup %s measure %q requires value_type", r.Name, m.TargetCol)
		}
		// ClickHouse 26.2 safety: a SummingMergeTree or AggregatingMergeTree MV
		// measure should not shadow a raw input column name when that input column
		// is transformed inside the aggregate expression unless the target table
		// owns a distinct non-GROUP-BY column — check for cross-expression shadowing too.
	}

	return nil
}

// Validate checks an L3 SnapshotSpec for structural & ClickHouse 26.2 safety.
func (s *SnapshotSpec) Validate() error {
	if !ValidIdent(s.Name) {
		return fmt.Errorf("schema: invalid snapshot name %q", s.Name)
	}
	db := s.Database
	if db == "" {
		db = "analytics"
	}
	if !ValidIdent(db) {
		return fmt.Errorf("schema: snapshot %s has invalid database %q", s.Name, db)
	}
	if len(s.Fields) == 0 {
		return fmt.Errorf("schema: snapshot %s has no fields", s.Name)
	}
	seen := make(map[string]bool, len(s.Fields)+1)
	for _, f := range s.Fields {
		if !ValidIdent(f.Name) {
			return fmt.Errorf("schema: snapshot %s has invalid field name %q", s.Name, f.Name)
		}
		if !f.Type.Valid() {
			return fmt.Errorf("schema: snapshot %s.%s has invalid type %q", s.Name, f.Name, f.Type)
		}
		if seen[f.Name] {
			return fmt.Errorf("schema: snapshot %s declares duplicate field %q", s.Name, f.Name)
		}
		seen[f.Name] = true
	}
	seen["refreshed_at"] = true
	if len(s.OrderBy) == 0 {
		return fmt.Errorf("schema: snapshot %s requires a non-empty ORDER BY key", s.Name)
	}
	for _, col := range s.OrderBy {
		if !seen[col] {
			return fmt.Errorf("schema: snapshot %s ORDER BY references unknown column %q", s.Name, col)
		}
	}
	if strings.TrimSpace(s.RefreshSelectSQL) != "" {
		// L3 serving snapshots MUST NEVER scan raw event.* tables directly.
		lower := strings.ToLower(s.RefreshSelectSQL)
		for _, forbidden := range []string{"from event.fact", "from event.span", "from hanzo.cloud_usage"} {
			if strings.Contains(lower, forbidden) {
				return fmt.Errorf("schema: L3 snapshot %s MUST NOT scan raw table (%s); read from L1 rollups or state/dim tables", s.Name, forbidden)
			}
		}
		if err := CheckClickHouse262Safety(s.RefreshSelectSQL); err != nil {
			return fmt.Errorf("schema: snapshot %s refresh SQL: %w", s.Name, err)
		}
	}
	return nil
}

// Validate runs full cross-entity invariant validation across the entire Registry.
func (r *Registry) Validate() error {
	for _, e := range r.entities {
		if err := e.Validate(); err != nil {
			return err
		}
		for _, rp := range e.Rollups {
			src := e
			if rp.SourceEntity != "" && rp.SourceEntity != e.Name {
				var ok bool
				src, ok = r.byName[rp.SourceEntity]
				if !ok {
					return fmt.Errorf("schema: rollup %s references unknown source_entity %q", rp.Name, rp.SourceEntity)
				}
			}
			if err := rp.Validate(src); err != nil {
				return err
			}
		}
	}
	for _, s := range r.snapshots {
		if err := s.Validate(); err != nil {
			return err
		}
	}
	return nil
}

var (
	reIdentToken = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\b`)
	reSelectAs   = regexp.MustCompile(`(?is)^(.+?)\s+AS\s+([A-Za-z_][A-Za-z0-9_]*)\s*$`)
)

// CheckClickHouse262Safety statically inspects a ClickHouse SELECT (including
// nested subqueries and Materialized View queries) for ClickHouse 26.2
// analyzer alias-shadowing hazards:
//
//  1. Self-shadowing transform alias in GROUP BY: e.g.
//     `SELECT toLowCardinality(org) AS org ... GROUP BY org`
//     (expression transforms `col` and aliases back to `col`, while `GROUP BY`
//     references `col`).
//  2. Sibling alias shadowing in SELECT: e.g.
//     `SELECT sumMerge(billed_nano) AS billed_nano, if(billed_nano > 0, ...)`
//     (an aggregate expression aliases to the exact same identifier as its own
//     input column, and a sibling SELECT expression references that identifier).
func CheckClickHouse262Safety(sql string) error {
	blocks := extractSelectBlocks(sql)
	for _, blk := range blocks {
		items := splitTopLevelCSV(blk.selectList)
		groupByIdents := parseGroupByIdents(blk.groupByList)

		type aliasInfo struct {
			expr        string
			alias       string
			selfShadows bool
			isAggregate bool
		}
		var infos []aliasInfo
		shadowedAliases := map[string]string{}

		for _, raw := range items {
			item := strings.TrimSpace(raw)
			if item == "" {
				continue
			}
			m := reSelectAs.FindStringSubmatch(item)
			if len(m) != 3 {
				continue
			}
			expr := strings.TrimSpace(m[1])
			alias := m[2]
			// A bare pass-through like `t.col AS col` or `col AS col` is NOT a
			// transforming/aggregating shadow.
			isPassThrough := expr == alias || strings.HasSuffix(expr, "."+alias) && !strings.ContainsAny(expr, "()+-*/ ")
			if isPassThrough {
				continue
			}
			selfShadows := containsIdent(expr, alias)
			isAgg := looksLikeAggregate(expr)

			if selfShadows && slices.Contains(groupByIdents, alias) {
				return fmt.Errorf("ClickHouse 26.2 alias shadowing: %q shadows input column %q referenced in GROUP BY", item, alias)
			}
			if selfShadows {
				shadowedAliases[alias] = item
			}
			infos = append(infos, aliasInfo{
				expr:        expr,
				alias:       alias,
				selfShadows: selfShadows,
				isAggregate: isAgg,
			})
		}

		// Check if any sibling expression references an alias that self-shadowed
		// an input column (classic ClickHouse 26.2 ILLEGAL_AGGREGATION hazard).
		for _, info := range infos {
			for alias, definingItem := range shadowedAliases {
				if info.alias == alias {
					continue
				}
				if containsIdent(info.expr, alias) {
					return fmt.Errorf("ClickHouse 26.2 alias shadowing: expression %q references self-shadowed alias %q defined by %q", info.expr, alias, definingItem)
				}
			}
		}
	}
	return nil
}

type selectBlock struct {
	selectList  string
	groupByList string
}

// extractSelectBlocks finds every SELECT ... FROM ... [GROUP BY ...] block,
// including inside subqueries, tracking parenthesis depth and string literals.
func extractSelectBlocks(sql string) []selectBlock {
	var blocks []selectBlock
	n := len(sql)
	inSingle := false
	for i := 0; i < n; i++ {
		if sql[i] == '\'' {
			inSingle = !inSingle
			continue
		}
		if inSingle {
			continue
		}
		if hasKeywordAt(sql, i, "SELECT") {
			selStart := i + len("SELECT")
			fromPos := findKeywordAtDepth(sql, selStart, "FROM")
			if fromPos < 0 {
				continue
			}
			blk := selectBlock{
				selectList: sql[selStart:fromPos],
			}
			// Find GROUP BY at same paren depth before end of this SELECT scope.
			scopeEnd := findSelectScopeEnd(sql, fromPos+len("FROM"))
			if gbPos := findKeywordAtDepthInRange(sql, fromPos+len("FROM"), scopeEnd, "GROUP"); gbPos >= 0 {
				rest := strings.TrimSpace(sql[gbPos+len("GROUP"):scopeEnd])
				if len(rest) >= 2 && strings.EqualFold(rest[:2], "BY") && (len(rest) == 2 || !isIdentChar(rest[2])) {
					gbStart := gbPos + len("GROUP")
					// advance past whitespace + BY
					for gbStart < scopeEnd && (sql[gbStart] == ' ' || sql[gbStart] == '\t' || sql[gbStart] == '\n' || sql[gbStart] == '\r') {
						gbStart++
					}
					gbStart += 2
					gbEnd := findGroupByEnd(sql, gbStart, scopeEnd)
					blk.groupByList = sql[gbStart:gbEnd]
				}
			}
			blocks = append(blocks, blk)
		}
	}
	return blocks
}

func hasKeywordAt(s string, pos int, kw string) bool {
	if pos+len(kw) > len(s) {
		return false
	}
	if !strings.EqualFold(s[pos:pos+len(kw)], kw) {
		return false
	}
	if pos > 0 && isIdentChar(s[pos-1]) {
		return false
	}
	if pos+len(kw) < len(s) && isIdentChar(s[pos+len(kw)]) {
		return false
	}
	return true
}

func isIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

func findKeywordAtDepth(s string, start int, kw string) int {
	return findKeywordAtDepthInRange(s, start, len(s), kw)
}

func findKeywordAtDepthInRange(s string, start, end int, kw string) int {
	depth := 0
	inSingle := false
	for i := start; i < end; i++ {
		if s[i] == '\'' {
			inSingle = !inSingle
			continue
		}
		if inSingle {
			continue
		}
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return -1
			}
			depth--
		default:
			if depth == 0 && hasKeywordAt(s, i, kw) {
				return i
			}
		}
	}
	return -1
}

func findSelectScopeEnd(s string, start int) int {
	depth := 0
	inSingle := false
	for i := start; i < len(s); i++ {
		if s[i] == '\'' {
			inSingle = !inSingle
			continue
		}
		if inSingle {
			continue
		}
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return i
			}
			depth--
		case ';':
			if depth == 0 {
				return i
			}
		default:
			if depth == 0 && hasKeywordAt(s, i, "UNION") {
				return i
			}
		}
	}
	return len(s)
}

func findGroupByEnd(s string, start, end int) int {
	depth := 0
	inSingle := false
	terminators := []string{"HAVING", "ORDER", "LIMIT", "SETTINGS", "FORMAT", "UNION"}
	for i := start; i < end; i++ {
		if s[i] == '\'' {
			inSingle = !inSingle
			continue
		}
		if inSingle {
			continue
		}
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return i
			}
			depth--
		default:
			if depth == 0 {
				for _, t := range terminators {
					if hasKeywordAt(s, i, t) {
						return i
					}
				}
			}
		}
	}
	return end
}

func splitTopLevelCSV(s string) []string {
	var parts []string
	depth := 0
	inSingle := false
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			inSingle = !inSingle
			continue
		}
		if inSingle {
			continue
		}
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	if start < len(s) {
		parts = append(parts, s[start:])
	}
	return parts
}

func parseGroupByIdents(groupBy string) []string {
	if strings.TrimSpace(groupBy) == "" {
		return nil
	}
	var out []string
	for _, raw := range splitTopLevelCSV(groupBy) {
		tok := strings.TrimSpace(raw)
		if ValidIdent(tok) {
			out = append(out, tok)
		}
	}
	return out
}

func containsIdent(expr, ident string) bool {
	// Strip single-quoted string literals before matching identifiers.
	cleaned := stripSingleQuoted(expr)
	for _, m := range reIdentToken.FindAllString(cleaned, -1) {
		if m == ident {
			return true
		}
	}
	return false
}

func stripSingleQuoted(s string) string {
	var b strings.Builder
	inSingle := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			inSingle = !inSingle
			b.WriteByte(' ')
			continue
		}
		if !inSingle {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func looksLikeAggregate(expr string) bool {
	lower := strings.ToLower(expr)
	for _, fn := range []string{
		"sum(", "sumif(", "sumstate(", "summerge(",
		"count(", "countif(", "countstate(", "countmerge(",
		"uniq(", "uniqexact(", "uniqstate(", "uniqmerge(",
		"argmax(", "argmin(", "avg(", "min(", "max(",
		"quantiletdigest",
	} {
		if strings.Contains(lower, fn) {
			return true
		}
	}
	return false
}
