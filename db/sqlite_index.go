package db

import (
	"fmt"
	"maps"
	"strings"
)

// sqlite_index.go makes a field a model tags orm:"index" a real SQLite index.
//
// Every entity lives in one table, `_entities`, as a JSON document, so a filter is
// `json_extract(data, '$.field') = ?`. Without an index on that expression the
// statement reads every live row of every kind: an IAM token lookup cost 143 ms at
// 150,000 users and an email lookup 419 ms, both linear in the table.
//
// Each tagged field gets a PARTIAL expression index scoped to its kind:
//
//	CREATE INDEX "idx_tokens_accessTokenHash" ON _entities(json_extract(data, '$.accessTokenHash'))
//	WHERE kind = 'tokens' AND deleted = 0
//
// Partial, because the table holds every kind: an index over all rows would carry an
// entry for every entity in every index, and each insert would pay for all of them.
// SQLite uses a partial index only when the statement's WHERE clause states the same
// terms, which is why every statement spells the kind as a literal (kindLiteral) and
// keeps `deleted = 0` as written. A bound `kind = ?` cannot be proven to equal the
// literal at prepare time, and the planner then ignores the index.
//
// The planner is not asked to choose between two of these indexes. The table has no
// statistics, and without them SQLite picks among equally costed indexes by creation
// order: an `owner` index, which matches every user of an org, beat the `email`
// index that matches one. drivingFilter picks the index from the query instead, and
// the other indexed terms are written with a unary + so they filter without driving.

// indexPrefix names every index this file creates.
const indexPrefix = "idx_"

// Index gives each JSON path of kind its own partial index. It is idempotent: a path
// already indexed in this process is skipped, and CREATE INDEX IF NOT EXISTS makes a
// second process, or a restart, a no-op on the file. Building an index over an
// existing table reads the table once, so the first call on a large store takes
// seconds; the write lock is taken per index so writes interleave with the build.
//
// Index must not be called by a goroutine that holds the write lock (inside a
// transaction): it waits for that lock.
func (db *SQLiteDB) Index(kind string, paths []string) error {
	db.indexMu.Lock()
	defer db.indexMu.Unlock()

	have := db.indexed.Load()
	var missing []string
	for _, p := range paths {
		p = ToJSONFieldName(p)
		if p != "" && !(*have)[kind][p] && !containsString(missing, p) {
			missing = append(missing, p)
		}
	}
	for _, p := range missing {
		db.writeMu.Lock()
		_, err := db.writeDB.Exec(indexDDL(kind, p))
		db.writeMu.Unlock()
		if err != nil {
			return fmt.Errorf("db: index %s.%s: %w", kind, p, err)
		}
		next := maps.Clone(*db.indexed.Load())
		next[kind] = maps.Clone(next[kind])
		if next[kind] == nil {
			next[kind] = map[string]bool{}
		}
		next[kind][p] = true
		db.indexed.Store(&next)
	}
	return nil
}

// indexedPaths is the set of JSON paths of kind that have an index. Statements read
// it without a lock: Index publishes a new map instead of changing the one a
// statement may be reading.
func (db *SQLiteDB) indexedPaths(kind string) map[string]bool {
	return (*db.indexed.Load())[kind]
}

// indexDDL is the statement that indexes one path of one kind. Its expression and
// its WHERE clause are the text the query builder writes, character for character:
// SQLite matches an index to a statement by comparing the two.
func indexDDL(kind, path string) string {
	return fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON _entities(%s) WHERE %s`,
		quoteIdent(indexPrefix+kind+"_"+path), jsonExpr(path), kindClause(kind))
}

// jsonExpr is the expression a JSON path is read and indexed through.
func jsonExpr(path string) string {
	return fmt.Sprintf("json_extract(data, '$.%s')", path)
}

// kindClause is the leading condition of every statement over one kind, and the
// WHERE clause of every index on it.
func kindClause(kind string) string {
	return "kind = " + kindLiteral(kind) + " AND deleted = 0"
}

// kindLiteral writes kind as a SQL string literal: quotes doubled, NUL dropped (a
// NUL would end the statement text early). Kinds are model names a program
// registers, but they are escaped rather than trusted.
func kindLiteral(kind string) string {
	kind = strings.ReplaceAll(kind, "\x00", "")
	return "'" + strings.ReplaceAll(kind, "'", "''") + "'"
}

// quoteIdent writes name as a SQL identifier.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// drivingFilter is the index of the filter that drives a query over an indexed
// kind, or -1 for none: the LAST equality filter on an indexed path, else the last
// range filter on one. Callers narrow last — Filter("Owner=", org).Filter("Email=",
// e) — so the last filter is the selective one. `!=` never drives; it cannot.
func drivingFilter(filters []QueryFilter, indexed map[string]bool) int {
	pick := -1
	for i, f := range filters {
		if !indexed[ToJSONFieldName(f.Field)] || zeroEquality(f) {
			continue
		}
		switch f.Op {
		case "=":
			pick = i
		case ">", ">=", "<", "<=":
			if pick < 0 || filters[pick].Op != "=" {
				pick = i
			}
		}
	}
	return pick
}

// zeroEquality reports a filter written as COALESCE(...) = 0 so that an absent field
// matches false or 0. That expression is not the indexed one and cannot drive.
func zeroEquality(f QueryFilter) bool {
	if f.Op != "=" {
		return false
	}
	switch v := f.Value.(type) {
	case bool:
		return !v
	case int:
		return v == 0
	}
	return false
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
