package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5"
)

// sqlQuery implements Query for SQL databases using native $N params.
type sqlQuery struct {
	db    *SQLDB
	tx    pgx.Tx
	txCtx context.Context
	kind  string

	filters     []QueryFilter
	orders      []QueryOrder
	projections []string
	ancestor    Key
	limit       int
	offset      int
	distinct    bool
	startCursor *SimpleCursor
	endCursor   *SimpleCursor
}

func (q *sqlQuery) Filter(filterStr string, value interface{}) Query {
	field, op := ParseFilterString(filterStr)
	return q.FilterField(field, op, value)
}

func (q *sqlQuery) FilterField(fieldPath string, op string, value interface{}) Query {
	newQ := q.clone()
	newQ.filters = append(newQ.filters, QueryFilter{
		Field: fieldPath, Op: NormalizeOp(op), Value: value,
	})
	return newQ
}

func (q *sqlQuery) Order(fieldPath string) Query {
	newQ := q.clone()
	if strings.HasPrefix(fieldPath, "-") {
		newQ.orders = append(newQ.orders, QueryOrder{
			Field: strings.TrimPrefix(fieldPath, "-"), Desc: true,
		})
	} else {
		newQ.orders = append(newQ.orders, QueryOrder{Field: fieldPath})
	}
	return newQ
}

func (q *sqlQuery) OrderDesc(fieldPath string) Query {
	newQ := q.clone()
	newQ.orders = append(newQ.orders, QueryOrder{Field: fieldPath, Desc: true})
	return newQ
}

func (q *sqlQuery) Limit(limit int) Query {
	newQ := q.clone()
	newQ.limit = limit
	return newQ
}

func (q *sqlQuery) Offset(offset int) Query {
	newQ := q.clone()
	newQ.offset = offset
	return newQ
}

func (q *sqlQuery) Project(fieldNames ...string) Query {
	newQ := q.clone()
	newQ.projections = append(newQ.projections, fieldNames...)
	return newQ
}

func (q *sqlQuery) Distinct() Query {
	newQ := q.clone()
	newQ.distinct = true
	return newQ
}

func (q *sqlQuery) Ancestor(ancestor Key) Query {
	newQ := q.clone()
	newQ.ancestor = ancestor
	return newQ
}

func (q *sqlQuery) Start(cursor Cursor) Query {
	newQ := q.clone()
	if c, ok := cursor.(*SimpleCursor); ok {
		newQ.startCursor = c
	}
	return newQ
}

func (q *sqlQuery) End(cursor Cursor) Query {
	newQ := q.clone()
	if c, ok := cursor.(*SimpleCursor); ok {
		newQ.endCursor = c
	}
	return newQ
}

func (q *sqlQuery) GetAll(ctx context.Context, dst interface{}) ([]Key, error) {
	query, args := q.buildSQL()

	rows, err := q.queryRows(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	dstVal := reflect.ValueOf(dst)
	if dstVal.Kind() != reflect.Ptr || dstVal.Elem().Kind() != reflect.Slice {
		return nil, errors.New("db: dst must be a pointer to a slice")
	}

	sliceVal := dstVal.Elem()
	elemType := sliceVal.Type().Elem()
	isPointer := elemType.Kind() == reflect.Ptr
	if isPointer {
		elemType = elemType.Elem()
	}

	var keys []Key
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}

		elem := reflect.New(elemType)
		if err := json.Unmarshal(data, elem.Interface()); err != nil {
			return nil, err
		}

		if isPointer {
			sliceVal = reflect.Append(sliceVal, elem)
		} else {
			sliceVal = reflect.Append(sliceVal, elem.Elem())
		}

		keys = append(keys, &sqlKey{
			kind: q.kind, stringID: id, namespace: q.db.config.TenantID,
		})
	}

	dstVal.Elem().Set(sliceVal)
	return keys, rows.Err()
}

func (q *sqlQuery) First(ctx context.Context, dst interface{}) (Key, error) {
	limitedQ := q.Limit(1).(*sqlQuery)
	query, args := limitedQ.buildSQL()

	row := q.queryRow(ctx, query, args...)

	var id string
	var data []byte
	if err := row.Scan(&id, &data); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoSuchEntity
		}
		return nil, err
	}

	if err := json.Unmarshal(data, dst); err != nil {
		return nil, err
	}

	return &sqlKey{
		kind: q.kind, stringID: id, namespace: q.db.config.TenantID,
	}, nil
}

func (q *sqlQuery) Count(ctx context.Context) (int, error) {
	where, args := q.buildWhere(2) // start after $1 (kind)
	query := fmt.Sprintf(`SELECT COUNT(*) FROM _entities WHERE kind = $1 AND deleted = FALSE%s`, where)
	allArgs := make([]interface{}, 0, len(args)+1)
	allArgs = append(allArgs, q.kind)
	allArgs = append(allArgs, args...)

	var count int
	row := q.queryRow(ctx, query, allArgs...)
	err := row.Scan(&count)
	return count, err
}

func (q *sqlQuery) Keys(ctx context.Context) ([]Key, error) {
	where, args := q.buildWhere(2) // after $1 (kind)
	query := fmt.Sprintf(`SELECT id FROM _entities WHERE kind = $1 AND deleted = FALSE%s`, where)
	allArgs := make([]interface{}, 0, len(args)+1)
	allArgs = append(allArgs, q.kind)
	allArgs = append(allArgs, args...)
	query += q.buildOrderBy()
	query += q.buildLimitOffset(len(allArgs))

	rows, err := q.queryRows(ctx, query, allArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []Key
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		keys = append(keys, &sqlKey{
			kind: q.kind, stringID: id, namespace: q.db.config.TenantID,
		})
	}
	return keys, rows.Err()
}

func (q *sqlQuery) Run(ctx context.Context) Iterator {
	query, args := q.buildSQL()

	rows, err := q.queryRows(ctx, query, args...)

	return &sqlIterator{
		rows: rows, err: err, kind: q.kind, namespace: q.db.config.TenantID,
	}
}

func (q *sqlQuery) buildSQL() (string, []interface{}) {
	where, args := q.buildWhere(2) // start after $1 (kind)

	selectClause := "id, data"
	if q.distinct {
		selectClause = "DISTINCT " + selectClause
	}

	query := fmt.Sprintf(`SELECT %s FROM _entities WHERE kind = $1 AND deleted = FALSE%s`,
		selectClause, where)
	allArgs := make([]interface{}, 0, len(args)+1)
	allArgs = append(allArgs, q.kind)
	allArgs = append(allArgs, args...)
	query += q.buildOrderBy()
	query += q.buildLimitOffset(len(allArgs))

	return query, allArgs
}

func (q *sqlQuery) buildWhere(startParam int) (string, []interface{}) {
	var conditions []string
	var args []interface{}
	pn := startParam

	if q.ancestor != nil {
		conditions = append(conditions, fmt.Sprintf("parent_id = $%d", pn))
		args = append(args, q.ancestor.Encode())
		pn++
	}

	for _, f := range q.filters {
		fieldName := ToJSONFieldName(f.Field)
		jsonPath := fmt.Sprintf("data->>'%s'", fieldName)

		conditions = append(conditions, fmt.Sprintf("%s %s $%d", jsonPath, f.Op, pn))
		args = append(args, f.Value)
		pn++
	}

	if q.startCursor != nil {
		conditions = append(conditions, fmt.Sprintf("id >= $%d", pn))
		args = append(args, q.startCursor.ID)
		pn++
	}
	if q.endCursor != nil {
		conditions = append(conditions, fmt.Sprintf("id < $%d", pn))
		args = append(args, q.endCursor.ID)
		pn++
	}

	if len(conditions) == 0 {
		return "", args
	}
	return " AND " + strings.Join(conditions, " AND "), args
}

func (q *sqlQuery) buildOrderBy() string {
	if len(q.orders) == 0 {
		return ""
	}

	var parts []string
	for _, o := range q.orders {
		jsonPath := fmt.Sprintf("data->>'%s'", ToJSONFieldName(o.Field))
		if o.Desc {
			parts = append(parts, jsonPath+" DESC")
		} else {
			parts = append(parts, jsonPath+" ASC")
		}
	}
	return " ORDER BY " + strings.Join(parts, ", ")
}

func (q *sqlQuery) buildLimitOffset(argCount int) string {
	var result string
	if q.limit > 0 {
		result += fmt.Sprintf(" LIMIT %d", q.limit)
	}
	if q.offset > 0 {
		result += fmt.Sprintf(" OFFSET %d", q.offset)
	}
	return result
}

func (q *sqlQuery) clone() *sqlQuery {
	newQ := *q
	newQ.filters = append([]QueryFilter{}, q.filters...)
	newQ.orders = append([]QueryOrder{}, q.orders...)
	newQ.projections = append([]string{}, q.projections...)
	return &newQ
}

// queryRows delegates to tx or pool.
func (q *sqlQuery) queryRows(ctx context.Context, query string, args ...interface{}) (pgx.Rows, error) {
	if q.tx != nil {
		return q.tx.Query(q.txCtx, query, args...)
	}
	return q.db.pool.Query(ctx, query, args...)
}

// queryRow delegates to tx or pool.
func (q *sqlQuery) queryRow(ctx context.Context, query string, args ...interface{}) pgx.Row {
	if q.tx != nil {
		return q.tx.QueryRow(q.txCtx, query, args...)
	}
	return q.db.pool.QueryRow(ctx, query, args...)
}

// sqlIterator implements Iterator for SQL databases.
type sqlIterator struct {
	rows      pgx.Rows
	err       error
	kind      string
	namespace string
	offset    int
}

func (it *sqlIterator) Next(dst interface{}) (Key, error) {
	if it.err != nil {
		return nil, it.err
	}
	if it.rows == nil || !it.rows.Next() {
		if it.rows != nil {
			if err := it.rows.Err(); err != nil {
				return nil, err
			}
		}
		return nil, errors.New("db: no more results")
	}

	var id string
	var data []byte
	if err := it.rows.Scan(&id, &data); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return nil, err
	}

	it.offset++

	return &sqlKey{
		kind: it.kind, stringID: id, namespace: it.namespace,
	}, nil
}

func (it *sqlIterator) Cursor() (Cursor, error) {
	return &SimpleCursor{
		ID: fmt.Sprintf("%d", it.offset), Offset: it.offset,
	}, nil
}

// sqlRowsAdapter wraps pgx.Rows to support the common iteration pattern.
type sqlRowsAdapter struct {
	rows pgx.Rows
}

func (a *sqlRowsAdapter) Next() bool                    { return a.rows.Next() }
func (a *sqlRowsAdapter) Scan(dest ...interface{}) error { return a.rows.Scan(dest...) }
func (a *sqlRowsAdapter) Close()                        { a.rows.Close() }
func (a *sqlRowsAdapter) Err() error                    { return a.rows.Err() }

// PoolFromSQLDB is an alias for PoolFromDB.
var _ = PoolFromDB
