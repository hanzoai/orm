package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// Session provides the xorm-compatible fluent query/mutation API.
type Session struct {
	engine *Engine
	tx     *sql.Tx

	table    string
	alias    string
	wheres   []condition
	joins    []joinClause
	orderBy  []orderClause
	groupBy  []string
	having   string
	cols     []string
	omitCols []string
	allCols  bool
	selectSt string // custom SELECT clause
	limit    int
	offset   int
	pk       interface{} // primary key value (single or PK{})

	rawSQL  string
	rawArgs []interface{}

	ctx context.Context
}

// Context sets the context for this session.
func (s *Session) Context(ctx context.Context) *Session {
	s.ctx = ctx
	return s
}

func (s *Session) getCtx() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}

// Begin starts a transaction.
func (s *Session) Begin() error {
	tx, err := s.engine.db.BeginTx(s.getCtx(), nil)
	if err != nil {
		return err
	}
	s.tx = tx
	return nil
}

// Commit commits the transaction.
func (s *Session) Commit() error {
	if s.tx == nil {
		return errors.New("engine: no transaction to commit")
	}
	return s.tx.Commit()
}

// Rollback rolls back the transaction.
func (s *Session) Rollback() error {
	if s.tx == nil {
		return errors.New("engine: no transaction to rollback")
	}
	return s.tx.Rollback()
}

// Close is a no-op for session (for xorm compatibility).
func (s *Session) Close() {}

// Table sets the table name.
func (s *Session) Table(name interface{}) *Session {
	switch v := name.(type) {
	case string:
		s.table = v
	default:
		// Assume it's a struct — derive table name
		typ := reflect.TypeOf(name)
		if typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
		}
		s.table = s.engine.tableMapper.Obj2Table(typ.Name())
	}
	return s
}

// Alias sets a table alias.
func (s *Session) Alias(alias string) *Session {
	s.alias = alias
	return s
}

// Select sets a custom SELECT clause.
func (s *Session) Select(str string) *Session {
	s.selectSt = str
	return s
}

// Where adds a WHERE condition.
func (s *Session) Where(query string, args ...interface{}) *Session {
	s.wheres = append(s.wheres, condition{query: query, args: args})
	return s
}

// And adds an AND condition.
func (s *Session) And(query string, args ...interface{}) *Session {
	s.wheres = append(s.wheres, condition{query: query, args: args})
	return s
}

// Or adds an OR condition.
func (s *Session) Or(query string, args ...interface{}) *Session {
	s.wheres = append(s.wheres, condition{query: query, args: args, or: true})
	return s
}

// In adds an IN condition.
func (s *Session) In(col string, args ...interface{}) *Session {
	s.wheres = append(s.wheres, buildIn(col, args))
	return s
}

// NotIn adds a NOT IN condition.
func (s *Session) NotIn(col string, args ...interface{}) *Session {
	s.wheres = append(s.wheres, buildNotIn(col, args))
	return s
}

// ID sets the primary key for the query.
func (s *Session) ID(pk interface{}) *Session {
	s.pk = pk
	return s
}

// Desc sets descending ORDER BY.
func (s *Session) Desc(colNames ...string) *Session {
	for _, col := range colNames {
		s.orderBy = append(s.orderBy, orderClause{col: col, desc: true})
	}
	return s
}

// Asc sets ascending ORDER BY.
func (s *Session) Asc(colNames ...string) *Session {
	for _, col := range colNames {
		s.orderBy = append(s.orderBy, orderClause{col: col, desc: false})
	}
	return s
}

// OrderBy sets a raw ORDER BY clause.
func (s *Session) OrderBy(order string) *Session {
	s.orderBy = append(s.orderBy, orderClause{col: order})
	return s
}

// Limit sets the LIMIT and optional OFFSET.
func (s *Session) Limit(limit int, start ...int) *Session {
	s.limit = limit
	if len(start) > 0 {
		s.offset = start[0]
	}
	return s
}

// Cols specifies which columns to include.
func (s *Session) Cols(cols ...string) *Session {
	s.cols = append(s.cols, cols...)
	return s
}

// AllCols includes all columns.
func (s *Session) AllCols() *Session {
	s.allCols = true
	return s
}

// Omit excludes specific columns.
func (s *Session) Omit(cols ...string) *Session {
	s.omitCols = append(s.omitCols, cols...)
	return s
}

// GroupBy sets GROUP BY.
func (s *Session) GroupBy(keys string) *Session {
	s.groupBy = append(s.groupBy, keys)
	return s
}

// Having sets the HAVING clause.
func (s *Session) Having(cond string) *Session {
	s.having = cond
	return s
}

// Join adds a JOIN clause.
func (s *Session) Join(joinType, tableName string, cond string, args ...interface{}) *Session {
	s.joins = append(s.joins, joinClause{
		joinType: joinType,
		table:    tableName,
		cond:     cond,
		args:     args,
	})
	return s
}

// SQL sets a raw SQL query.
func (s *Session) SQL(query string, args ...interface{}) *Session {
	s.rawSQL = query
	s.rawArgs = args
	return s
}

// Prepare is a no-op for compatibility.
func (s *Session) Prepare() *Session {
	return s
}

// Get retrieves a single record into bean. Returns (true, nil) if found.
func (s *Session) Get(bean interface{}) (bool, error) {
	table := s.resolveTable(bean)
	if table == nil {
		return false, fmt.Errorf("engine: cannot resolve table for %T", bean)
	}

	b := NewBuilder(s.engine.driver)

	if s.rawSQL != "" {
		b.WriteString(s.rawSQL)
		b.args = append(b.args, s.rawArgs...)
	} else {
		b.WriteString("SELECT ")
		b.WriteString(s.buildSelectCols(table))
		b.WriteString(" FROM ")
		b.WriteString(quoteIdent(table.Name, s.engine.driver))

		if s.alias != "" {
			b.WriteString(" AS ")
			b.WriteString(s.alias)
		}

		s.buildJoins(b)

		where := s.buildWhere(table, bean)
		if len(where) > 0 {
			b.WriteString(" WHERE ")
			buildConditions(b, where)
		}

		s.buildOrderBy(b)

		// Always limit to 1 for Get
		b.WriteString(" LIMIT 1")
	}

	row := s.queryRow(b.String(), b.Args()...)

	return s.scanRow(row, bean, table)
}

// Find retrieves multiple records into a slice pointer.
func (s *Session) Find(beans interface{}, conds ...interface{}) error {
	sliceVal := reflect.ValueOf(beans)
	if sliceVal.Kind() != reflect.Ptr || sliceVal.Elem().Kind() != reflect.Slice {
		return errors.New("engine: beans must be a pointer to a slice")
	}

	elemType := sliceVal.Elem().Type().Elem()
	isPtr := elemType.Kind() == reflect.Ptr
	if isPtr {
		elemType = elemType.Elem()
	}

	// Create a zero bean for table resolution
	zeroBeanPtr := reflect.New(elemType)
	table := s.resolveTable(zeroBeanPtr.Interface())
	if table == nil {
		return fmt.Errorf("engine: cannot resolve table for %T", beans)
	}

	// Add conditions from conds parameter
	for _, c := range conds {
		s.addBeanConditions(c, table)
	}

	b := NewBuilder(s.engine.driver)

	if s.rawSQL != "" {
		b.WriteString(s.rawSQL)
		b.args = append(b.args, s.rawArgs...)
	} else {
		b.WriteString("SELECT ")
		b.WriteString(s.buildSelectCols(table))
		b.WriteString(" FROM ")
		b.WriteString(quoteIdent(table.Name, s.engine.driver))

		if s.alias != "" {
			b.WriteString(" AS ")
			b.WriteString(s.alias)
		}

		s.buildJoins(b)

		where := s.buildWhereOnly(table)
		if len(where) > 0 {
			b.WriteString(" WHERE ")
			buildConditions(b, where)
		}

		if len(s.groupBy) > 0 {
			b.WriteString(" GROUP BY ")
			b.WriteString(strings.Join(s.groupBy, ", "))
			if s.having != "" {
				b.WriteString(" HAVING ")
				b.WriteString(s.having)
			}
		}

		s.buildOrderBy(b)
		s.buildLimitOffset(b)
	}

	rows, err := s.query(b.String(), b.Args()...)
	if err != nil {
		return err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return err
	}

	slice := sliceVal.Elem()
	for rows.Next() {
		elem := reflect.New(elemType)
		if err := s.scanRows(rows, columns, elem.Interface(), table); err != nil {
			return err
		}

		if isPtr {
			slice = reflect.Append(slice, elem)
		} else {
			slice = reflect.Append(slice, elem.Elem())
		}
	}

	sliceVal.Elem().Set(slice)
	return rows.Err()
}

// Insert inserts one or more records.
func (s *Session) Insert(beans ...interface{}) (int64, error) {
	var totalAffected int64

	for _, bean := range beans {
		table := s.resolveTable(bean)
		if table == nil {
			return 0, fmt.Errorf("engine: cannot resolve table for %T", bean)
		}

		cols, vals := s.buildInsertValues(bean, table)
		if len(cols) == 0 {
			continue
		}

		b := NewBuilder(s.engine.driver)
		b.WriteString("INSERT INTO ")
		b.WriteString(quoteIdent(table.Name, s.engine.driver))
		b.WriteString(" (")

		for i, col := range cols {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(quoteIdent(col, s.engine.driver))
		}

		b.WriteString(") VALUES (")
		for i, val := range vals {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteArg(val)
		}
		b.WriteString(")")

		result, err := s.exec(b.String(), b.Args()...)
		if err != nil {
			return totalAffected, err
		}

		affected, _ := result.RowsAffected()
		totalAffected += affected

		// Set auto-generated ID back on the bean
		if id, err := result.LastInsertId(); err == nil && id > 0 {
			s.setAutoID(bean, table, id)
		}
	}

	return totalAffected, nil
}

// Update updates records matching conditions.
func (s *Session) Update(bean interface{}, conds ...interface{}) (int64, error) {
	table := s.resolveTable(bean)
	if table == nil {
		return 0, fmt.Errorf("engine: cannot resolve table for %T", bean)
	}

	// Add conditions from conds parameter
	for _, c := range conds {
		s.addBeanConditions(c, table)
	}

	cols, vals := s.buildUpdateValues(bean, table)
	if len(cols) == 0 {
		return 0, nil
	}

	b := NewBuilder(s.engine.driver)
	b.WriteString("UPDATE ")
	b.WriteString(quoteIdent(table.Name, s.engine.driver))
	b.WriteString(" SET ")

	for i, col := range cols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteIdent(col, s.engine.driver))
		b.WriteString(" = ")
		b.WriteArg(vals[i])
	}

	where := s.buildWhere(table, bean)
	if len(where) > 0 {
		b.WriteString(" WHERE ")
		buildConditions(b, where)
	}

	result, err := s.exec(b.String(), b.Args()...)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

// Delete deletes records matching conditions.
func (s *Session) Delete(bean interface{}) (int64, error) {
	table := s.resolveTable(bean)
	if table == nil {
		return 0, fmt.Errorf("engine: cannot resolve table for %T", bean)
	}

	b := NewBuilder(s.engine.driver)
	b.WriteString("DELETE FROM ")
	b.WriteString(quoteIdent(table.Name, s.engine.driver))

	where := s.buildWhere(table, bean)
	if len(where) > 0 {
		b.WriteString(" WHERE ")
		buildConditions(b, where)
	}

	result, err := s.exec(b.String(), b.Args()...)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

// Count counts matching records.
func (s *Session) Count(bean ...interface{}) (int64, error) {
	var table *TableMeta
	if len(bean) > 0 && bean[0] != nil {
		table = s.resolveTable(bean[0])
	}
	if table == nil && s.table != "" {
		table = s.engine.Table(s.table)
		if table == nil {
			table = &TableMeta{Name: s.table}
		}
	}
	if table == nil {
		return 0, errors.New("engine: no table specified for Count")
	}

	b := NewBuilder(s.engine.driver)
	b.WriteString("SELECT COUNT(*) FROM ")
	b.WriteString(quoteIdent(table.Name, s.engine.driver))

	var where []condition
	if len(bean) > 0 && bean[0] != nil {
		where = s.buildWhere(table, bean[0])
	} else {
		where = s.buildWhereOnly(table)
	}

	if len(where) > 0 {
		b.WriteString(" WHERE ")
		buildConditions(b, where)
	}

	var count int64
	row := s.queryRow(b.String(), b.Args()...)
	err := row.Scan(&count)
	return count, err
}

// Exec executes raw SQL.
func (s *Session) Exec(sqlOrArgs ...interface{}) (sql.Result, error) {
	if s.rawSQL != "" {
		return s.exec(s.rawSQL, s.rawArgs...)
	}
	if len(sqlOrArgs) == 0 {
		return nil, errors.New("engine: no SQL to execute")
	}

	sqlStr, ok := sqlOrArgs[0].(string)
	if !ok {
		return nil, errors.New("engine: first argument must be SQL string")
	}

	return s.exec(sqlStr, sqlOrArgs[1:]...)
}

// QueryBytes executes raw SQL and returns []map[string][]byte.
func (s *Session) QueryBytes(sqlOrArgs ...interface{}) ([]map[string][]byte, error) {
	var sqlStr string
	var args []interface{}

	if s.rawSQL != "" {
		sqlStr = s.rawSQL
		args = s.rawArgs
	} else if len(sqlOrArgs) > 0 {
		var ok bool
		sqlStr, ok = sqlOrArgs[0].(string)
		if !ok {
			return nil, errors.New("engine: first argument must be SQL string")
		}
		args = sqlOrArgs[1:]
	}

	rows, err := s.query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var results []map[string][]byte
	for rows.Next() {
		values := make([]interface{}, len(columns))
		scanDest := make([]interface{}, len(columns))
		for i := range values {
			scanDest[i] = &values[i]
		}

		if err := rows.Scan(scanDest...); err != nil {
			return nil, err
		}

		row := make(map[string][]byte)
		for i, col := range columns {
			switch v := values[i].(type) {
			case []byte:
				row[col] = v
			case string:
				row[col] = []byte(v)
			case nil:
				row[col] = nil
			default:
				row[col] = []byte(fmt.Sprintf("%v", v))
			}
		}
		results = append(results, row)
	}

	return results, rows.Err()
}

// Exist checks if a record exists.
func (s *Session) Exist(bean ...interface{}) (bool, error) {
	var table *TableMeta
	if len(bean) > 0 && bean[0] != nil {
		table = s.resolveTable(bean[0])
	}
	if table == nil && s.table != "" {
		table = &TableMeta{Name: s.table}
	}
	if table == nil {
		return false, errors.New("engine: no table specified for Exist")
	}

	b := NewBuilder(s.engine.driver)
	b.WriteString("SELECT 1 FROM ")
	b.WriteString(quoteIdent(table.Name, s.engine.driver))

	var where []condition
	if len(bean) > 0 && bean[0] != nil {
		where = s.buildWhere(table, bean[0])
	} else {
		where = s.buildWhereOnly(table)
	}

	if len(where) > 0 {
		b.WriteString(" WHERE ")
		buildConditions(b, where)
	}
	b.WriteString(" LIMIT 1")

	row := s.queryRow(b.String(), b.Args()...)
	var dummy int
	err := row.Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// --- internal helpers ---

func (s *Session) resolveTable(bean interface{}) *TableMeta {
	if s.table != "" {
		if t := s.engine.Table(s.table); t != nil {
			return t
		}
		// Return a minimal TableMeta with just the name
		return s.engine.tableMeta(bean)
	}
	return s.engine.tableMeta(bean)
}

func (s *Session) buildSelectCols(table *TableMeta) string {
	if s.selectSt != "" {
		return s.selectSt
	}

	if len(s.cols) > 0 {
		quoted := make([]string, len(s.cols))
		for i, c := range s.cols {
			quoted[i] = quoteIdent(c, s.engine.driver)
		}
		return strings.Join(quoted, ", ")
	}

	if len(s.omitCols) > 0 {
		omitSet := make(map[string]bool)
		for _, c := range s.omitCols {
			omitSet[c] = true
		}
		var cols []string
		for _, col := range table.Columns {
			if !omitSet[col.Name] {
				cols = append(cols, quoteIdent(col.Name, s.engine.driver))
			}
		}
		if len(cols) > 0 {
			return strings.Join(cols, ", ")
		}
	}

	return "*"
}

func (s *Session) buildWhere(table *TableMeta, bean interface{}) []condition {
	conds := make([]condition, 0, len(s.wheres)+2)

	// Add PK condition if set
	if s.pk != nil {
		pkConds := s.buildPKCondition(table)
		conds = append(conds, pkConds...)
	}

	// Add explicit WHERE conditions
	conds = append(conds, s.wheres...)

	// Add non-zero bean field conditions for Get (xorm behavior)
	if bean != nil && s.pk == nil && len(s.wheres) == 0 {
		beanConds := s.beanFieldConditions(bean, table)
		conds = append(conds, beanConds...)
	}

	return conds
}

func (s *Session) buildWhereOnly(table *TableMeta) []condition {
	conds := make([]condition, 0, len(s.wheres)+2)

	if s.pk != nil {
		pkConds := s.buildPKCondition(table)
		conds = append(conds, pkConds...)
	}

	conds = append(conds, s.wheres...)
	return conds
}

func (s *Session) buildPKCondition(table *TableMeta) []condition {
	if s.pk == nil {
		return nil
	}

	// Composite PK
	if pk, ok := s.pk.(PK); ok {
		var conds []condition
		for i, pkCol := range table.PrimaryKey {
			if i < len(pk) {
				conds = append(conds, condition{
					query: quoteIdent(pkCol, s.engine.driver) + " = ?",
					args:  []interface{}{pk[i]},
				})
			}
		}
		return conds
	}

	// Single PK
	if len(table.PrimaryKey) > 0 {
		return []condition{{
			query: quoteIdent(table.PrimaryKey[0], s.engine.driver) + " = ?",
			args:  []interface{}{s.pk},
		}}
	}

	// Fallback to "id"
	return []condition{{
		query: quoteIdent("id", s.engine.driver) + " = ?",
		args:  []interface{}{s.pk},
	}}
}

// beanFieldConditions generates WHERE conditions from non-zero struct fields.
// This matches xorm's behavior where Get(&User{Name: "foo"}) generates
// WHERE name = 'foo'.
func (s *Session) beanFieldConditions(bean interface{}, table *TableMeta) []condition {
	val := reflect.ValueOf(bean)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return nil
	}

	var conds []condition
	for _, col := range table.Columns {
		if col.IsPrimaryKey || col.IsCreated || col.IsUpdated {
			continue
		}

		fv := val.FieldByName(col.GoName)
		if !fv.IsValid() {
			continue
		}

		if fv.IsZero() {
			continue
		}

		var v interface{}
		if col.IsJSON {
			data, err := json.Marshal(fv.Interface())
			if err != nil {
				continue
			}
			v = string(data)
		} else {
			v = fv.Interface()
		}

		conds = append(conds, condition{
			query: quoteIdent(col.Name, s.engine.driver) + " = ?",
			args:  []interface{}{v},
		})
	}

	return conds
}

func (s *Session) addBeanConditions(bean interface{}, table *TableMeta) {
	if bean == nil {
		return
	}

	val := reflect.ValueOf(bean)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return
	}

	for _, col := range table.Columns {
		fv := val.FieldByName(col.GoName)
		if !fv.IsValid() || fv.IsZero() {
			continue
		}

		var v interface{}
		if col.IsJSON {
			data, err := json.Marshal(fv.Interface())
			if err != nil {
				continue
			}
			v = string(data)
		} else {
			v = fv.Interface()
		}

		s.wheres = append(s.wheres, condition{
			query: quoteIdent(col.Name, s.engine.driver) + " = ?",
			args:  []interface{}{v},
		})
	}
}

func (s *Session) buildJoins(b *Builder) {
	for _, j := range s.joins {
		b.WriteString(" ")
		b.WriteString(strings.ToUpper(j.joinType))
		b.WriteString(" JOIN ")
		b.WriteString(j.table)
		b.WriteString(" ON ")
		if b.driver == "postgres" {
			b.WriteString(replacePlaceholders(j.cond, &b.pcount))
		} else {
			b.WriteString(j.cond)
		}
		b.args = append(b.args, j.args...)
	}
}

func (s *Session) buildOrderBy(b *Builder) {
	if len(s.orderBy) == 0 {
		return
	}
	b.WriteString(" ORDER BY ")
	for i, o := range s.orderBy {
		if i > 0 {
			b.WriteString(", ")
		}
		// If it contains spaces, it's a raw expression
		if strings.Contains(o.col, " ") {
			b.WriteString(o.col)
		} else {
			b.WriteString(quoteIdent(o.col, s.engine.driver))
			if o.desc {
				b.WriteString(" DESC")
			} else {
				b.WriteString(" ASC")
			}
		}
	}
}

func (s *Session) buildLimitOffset(b *Builder) {
	if s.limit > 0 {
		b.WriteString(fmt.Sprintf(" LIMIT %d", s.limit))
	}
	if s.offset > 0 {
		b.WriteString(fmt.Sprintf(" OFFSET %d", s.offset))
	}
}

func (s *Session) buildInsertValues(bean interface{}, table *TableMeta) ([]string, []interface{}) {
	val := reflect.ValueOf(bean)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	var cols []string
	var vals []interface{}

	for _, col := range table.Columns {
		if col.IsAutoIncr {
			continue
		}

		fv := val.FieldByName(col.GoName)
		if !fv.IsValid() {
			continue
		}

		// Handle specific column logic
		if col.IsCreated || col.IsUpdated {
			cols = append(cols, col.Name)
			vals = append(vals, time.Now())
			continue
		}

		if col.IsVersion {
			cols = append(cols, col.Name)
			vals = append(vals, 1)
			continue
		}

		// Skip zero values unless allCols or explicitly included
		if !s.allCols && len(s.cols) == 0 && fv.IsZero() {
			continue
		}

		// Check if this column is in the cols list
		if len(s.cols) > 0 && !s.colIncluded(col.Name) {
			continue
		}

		// Check omitted columns
		if s.colOmitted(col.Name) {
			continue
		}

		cols = append(cols, col.Name)
		if col.IsJSON {
			data, err := json.Marshal(fv.Interface())
			if err != nil {
				continue
			}
			vals = append(vals, string(data))
		} else {
			vals = append(vals, fv.Interface())
		}
	}

	return cols, vals
}

func (s *Session) buildUpdateValues(bean interface{}, table *TableMeta) ([]string, []interface{}) {
	val := reflect.ValueOf(bean)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	var cols []string
	var vals []interface{}

	for _, col := range table.Columns {
		if col.IsPrimaryKey {
			continue
		}

		fv := val.FieldByName(col.GoName)
		if !fv.IsValid() {
			continue
		}

		if col.IsUpdated {
			cols = append(cols, col.Name)
			vals = append(vals, time.Now())
			continue
		}

		if col.IsCreated {
			continue
		}

		if col.IsVersion {
			cols = append(cols, col.Name)
			vals = append(vals, fv.Interface().(int)+1)
			continue
		}

		// Without allCols, skip zero values
		if !s.allCols && len(s.cols) == 0 && fv.IsZero() {
			continue
		}

		if len(s.cols) > 0 && !s.colIncluded(col.Name) {
			continue
		}

		if s.colOmitted(col.Name) {
			continue
		}

		cols = append(cols, col.Name)
		if col.IsJSON {
			data, err := json.Marshal(fv.Interface())
			if err != nil {
				continue
			}
			vals = append(vals, string(data))
		} else {
			vals = append(vals, fv.Interface())
		}
	}

	return cols, vals
}

func (s *Session) colIncluded(name string) bool {
	for _, c := range s.cols {
		if c == name {
			return true
		}
	}
	return false
}

func (s *Session) colOmitted(name string) bool {
	for _, c := range s.omitCols {
		if c == name {
			return true
		}
	}
	return false
}

func (s *Session) setAutoID(bean interface{}, table *TableMeta, id int64) {
	val := reflect.ValueOf(bean)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	for _, col := range table.Columns {
		if col.IsPrimaryKey || col.IsAutoIncr {
			fv := val.FieldByName(col.GoName)
			if fv.IsValid() && fv.CanSet() {
				switch fv.Kind() {
				case reflect.Int, reflect.Int64, reflect.Int32:
					fv.SetInt(id)
				}
			}
			break
		}
	}
}

func (s *Session) scanRow(row *sql.Row, bean interface{}, table *TableMeta) (bool, error) {
	if s.rawSQL != "" || s.selectSt != "" {
		return s.scanRowDynamic(row, bean)
	}

	vals, fields := s.buildScanDest(bean, table)
	err := row.Scan(vals...)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	s.assignScanned(bean, fields, vals, table)
	return true, nil
}

func (s *Session) scanRowDynamic(row *sql.Row, bean interface{}) (bool, error) {
	// For raw SQL, scan all columns dynamically
	val := reflect.ValueOf(bean)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	// We need to know columns; for single row we can try scanning into interface
	var rawBytes []byte
	err := row.Scan(&rawBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if err := json.Unmarshal(rawBytes, bean); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Session) scanRows(rows *sql.Rows, columns []string, bean interface{}, table *TableMeta) error {
	vals := make([]interface{}, len(columns))
	for i := range vals {
		vals[i] = new(interface{})
	}

	if err := rows.Scan(vals...); err != nil {
		return err
	}

	beanVal := reflect.ValueOf(bean)
	if beanVal.Kind() == reflect.Ptr {
		beanVal = beanVal.Elem()
	}

	for i, colName := range columns {
		raw := *(vals[i].(*interface{}))
		if raw == nil {
			continue
		}

		col := table.colByName[colName]
		if col == nil {
			continue
		}

		fv := beanVal.FieldByName(col.GoName)
		if !fv.IsValid() || !fv.CanSet() {
			continue
		}

		if err := scanValue(raw, fv); err != nil {
			return fmt.Errorf("scan column %s: %w", colName, err)
		}
	}

	return nil
}

func (s *Session) buildScanDest(bean interface{}, table *TableMeta) ([]interface{}, []*ColumnMeta) {
	// For SELECT *, scan all columns
	vals := make([]interface{}, len(table.Columns))
	fields := make([]*ColumnMeta, len(table.Columns))

	for i, col := range table.Columns {
		vals[i] = new(interface{})
		fields[i] = col
	}

	return vals, fields
}

func (s *Session) assignScanned(bean interface{}, fields []*ColumnMeta, vals []interface{}, table *TableMeta) {
	beanVal := reflect.ValueOf(bean)
	if beanVal.Kind() == reflect.Ptr {
		beanVal = beanVal.Elem()
	}

	for i, col := range fields {
		if col == nil {
			continue
		}
		raw := *(vals[i].(*interface{}))
		if raw == nil {
			continue
		}

		fv := beanVal.FieldByName(col.GoName)
		if !fv.IsValid() || !fv.CanSet() {
			continue
		}

		scanValue(raw, fv)
	}
}

// exec delegates to tx or db.
func (s *Session) exec(query string, args ...interface{}) (sql.Result, error) {
	if s.engine.driver == "postgres" {
		query = convertToPostgres(query)
	}
	if s.tx != nil {
		return s.tx.ExecContext(s.getCtx(), query, args...)
	}
	return s.engine.db.ExecContext(s.getCtx(), query, args...)
}

// query delegates to tx or db.
func (s *Session) query(query string, args ...interface{}) (*sql.Rows, error) {
	if s.engine.driver == "postgres" {
		query = convertToPostgres(query)
	}
	if s.tx != nil {
		return s.tx.QueryContext(s.getCtx(), query, args...)
	}
	return s.engine.db.QueryContext(s.getCtx(), query, args...)
}

// queryRow delegates to tx or db.
func (s *Session) queryRow(query string, args ...interface{}) *sql.Row {
	if s.engine.driver == "postgres" {
		query = convertToPostgres(query)
	}
	if s.tx != nil {
		return s.tx.QueryRowContext(s.getCtx(), query, args...)
	}
	return s.engine.db.QueryRowContext(s.getCtx(), query, args...)
}

// convertToPostgres converts ? placeholders to $N for PostgreSQL
// when they haven't already been converted by the Builder.
func convertToPostgres(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}

	var result strings.Builder
	result.Grow(len(query) + 16)
	n := 0

	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			fmt.Fprintf(&result, "$%d", n)
		} else {
			result.WriteByte(query[i])
		}
	}

	return result.String()
}
