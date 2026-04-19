// ZAP protocol driver for the ORM.
//
// ZAP (Zero-Copy App Proto) uses binary encoding over RPC, communicating
// directly with ZAP-native backends (hanzo/sql, hanzo/kv, hanzo/datastore,
// hanzo/documentdb). Each backend speaks ZAP natively — no sidecar needed.
// Structs are encoded directly into the ZAP binary format — no JSON
// serialization step for storage fields. Complex types (slices, maps,
// nested structs) are transmitted natively.
package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/luxfi/zap"
)

// ZapBackend selects which ZAP-native backend to connect to.
type ZapBackend int

const (
	// ZapSQL connects to hanzo/sql (PostgreSQL fork) on port 9651.
	ZapSQL ZapBackend = iota
	// ZapDocumentDB connects to hanzo/documentdb (FerretDB fork) on port 9654.
	// Provides MongoDB-style document semantics over PostgreSQL storage.
	// Clients who "think mongo" use this; data lives in hanzo/sql.
	ZapDocumentDB
	// ZapKV connects to hanzo/kv (Valkey fork) on port 9653.
	ZapKV
	// ZapDatastore connects to hanzo/datastore (ClickHouse fork) on port 9655.
	ZapDatastore
)

// DefaultPorts for each ZAP-native backend.
var DefaultPorts = map[ZapBackend]int{
	ZapSQL:        9651,
	ZapKV:         9653,
	ZapDocumentDB: 9654,
	ZapDatastore:  9655,
}

// ZAP message type IDs (matching native backend handlers).
const (
	zapMsgTypeSQL        uint16 = 300
	zapMsgTypeKV         uint16 = 301
	zapMsgTypeDatastore  uint16 = 302
	zapMsgTypeDocumentDB uint16 = 303
)

// ZAP message field offsets (matching zap-sidecar protocol).
const (
	zapFieldPath    = 4  // Text: request path (e.g., "/query", "/get")
	zapFieldBody    = 12 // Bytes: JSON body
	zapRespStatus   = 0  // Uint32: HTTP-style status
	zapRespBody     = 4  // Bytes: response JSON
	zapRespHeaders  = 8  // Bytes: response headers JSON
)

// ZapConfig configures a ZAP database connection.
type ZapConfig struct {
	// Addr is the backend address (e.g., "localhost:9651").
	// If empty, uses DefaultPorts[Backend] on localhost.
	Addr string

	// Backend selects which ZAP-native backend to connect to.
	Backend ZapBackend

	// Database is the target database name (for SQL/DocumentDB backends).
	Database string

	// Collection is the default collection/table for entity storage.
	// Defaults to "_entities" for SQL, "entities" for DocumentDB.
	Collection string

	// NodeID is this client's node ID for ZAP peer identification.
	NodeID string

	// QueryTimeout is the per-query timeout (default 30s).
	QueryTimeout time.Duration

	// Logger for ZAP node (optional).
	Logger *slog.Logger
}

// ZapDB implements db.DB over ZAP binary protocol.
type ZapDB struct {
	node     *zap.Node
	cfg      ZapConfig
	peerID   string // sidecar peer ID (discovered or set)
	mu       sync.RWMutex
	closed   bool
	tenantID string
}

// NewZapDB connects to a ZAP-native backend and returns a DB implementation.
func NewZapDB(cfg *ZapConfig) (*ZapDB, error) {
	if cfg.Addr == "" {
		if port, ok := DefaultPorts[cfg.Backend]; ok {
			cfg.Addr = fmt.Sprintf("localhost:%d", port)
		} else {
			return nil, errors.New("db: zap addr required")
		}
	}
	if cfg.Collection == "" {
		switch cfg.Backend {
		case ZapSQL, ZapDatastore:
			cfg.Collection = "_entities"
		case ZapDocumentDB:
			cfg.Collection = "entities"
		case ZapKV:
			cfg.Collection = "orm"
		}
	}
	if cfg.NodeID == "" {
		cfg.NodeID = fmt.Sprintf("orm-client-%d", timeNow().UnixNano())
	}
	if cfg.QueryTimeout == 0 {
		cfg.QueryTimeout = 30 * time.Second
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	node := zap.NewNode(zap.NodeConfig{
		NodeID:      cfg.NodeID,
		Port:        0, // ephemeral port for client
		NoDiscovery: true,
		Logger:      logger,
	})

	if err := node.Start(); err != nil {
		return nil, fmt.Errorf("db: zap node start: %w", err)
	}

	// Connect directly to the backend
	if err := node.ConnectDirect(cfg.Addr); err != nil {
		node.Stop()
		return nil, fmt.Errorf("db: zap connect %s: %w", cfg.Addr, err)
	}

	// The backend's node ID is discovered via handshake
	peers := node.Peers()
	if len(peers) == 0 {
		node.Stop()
		return nil, fmt.Errorf("db: zap no peers after connect to %s", cfg.Addr)
	}

	return &ZapDB{
		node:   node,
		cfg:    *cfg,
		peerID: peers[0],
	}, nil
}

// call sends a ZAP request to the sidecar and returns the response.
func (z *ZapDB) call(ctx context.Context, path string, body []byte) (uint32, []byte, error) {
	msgType := z.msgType()

	b := zap.NewBuilder(len(body) + 128)
	obj := b.StartObject(20)
	obj.SetText(zapFieldPath, path)
	obj.SetBytes(zapFieldBody, body)
	obj.FinishAsRoot()
	data := b.FinishWithFlags(uint16(msgType) << 8)

	msg, err := zap.Parse(data)
	if err != nil {
		return 0, nil, fmt.Errorf("db: zap build msg: %w", err)
	}

	resp, err := z.node.Call(ctx, z.peerID, msg)
	if err != nil {
		return 0, nil, fmt.Errorf("db: zap call: %w", err)
	}

	root := resp.Root()
	status := root.Uint32(zapRespStatus)
	respBody := root.Bytes(zapRespBody)
	return status, respBody, nil
}

func (z *ZapDB) msgType() uint16 {
	switch z.cfg.Backend {
	case ZapSQL:
		return zapMsgTypeSQL
	case ZapKV:
		return zapMsgTypeKV
	case ZapDatastore:
		return zapMsgTypeDatastore
	case ZapDocumentDB:
		return zapMsgTypeDocumentDB
	default:
		return zapMsgTypeSQL
	}
}

func (z *ZapDB) Get(ctx context.Context, key Key, dst interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, z.cfg.QueryTimeout)
	defer cancel()

	switch z.cfg.Backend {
	case ZapDocumentDB:
		return z.docGet(ctx, key, dst)
	case ZapKV:
		return z.kvGet(ctx, key, dst)
	default:
		return z.sqlGet(ctx, key, dst)
	}
}

func (z *ZapDB) Put(ctx context.Context, key Key, src interface{}) (Key, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, z.cfg.QueryTimeout)
	defer cancel()

	switch z.cfg.Backend {
	case ZapDocumentDB:
		return z.docPut(ctx, key, src)
	case ZapKV:
		return z.kvPut(ctx, key, src)
	default:
		return z.sqlPut(ctx, key, src)
	}
}

func (z *ZapDB) Delete(ctx context.Context, key Key) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, z.cfg.QueryTimeout)
	defer cancel()

	switch z.cfg.Backend {
	case ZapDocumentDB:
		return z.docDelete(ctx, key)
	case ZapKV:
		return z.kvDelete(ctx, key)
	default:
		return z.sqlDelete(ctx, key)
	}
}

func (z *ZapDB) GetMulti(ctx context.Context, keys []Key, dst interface{}) error {
	for _, k := range keys {
		if err := z.Get(ctx, k, dst); err != nil {
			return err
		}
	}
	return nil
}

func (z *ZapDB) PutMulti(ctx context.Context, keys []Key, src interface{}) ([]Key, error) {
	return nil, errors.New("db: zap PutMulti not yet implemented")
}

func (z *ZapDB) DeleteMulti(ctx context.Context, keys []Key) error {
	for _, k := range keys {
		if err := z.Delete(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

func (z *ZapDB) Query(kind string) Query {
	return &zapQuery{db: z, kind: kind}
}

func (z *ZapDB) VectorSearch(ctx context.Context, opts *VectorSearchOptions) ([]VectorResult, error) {
	return nil, errors.New("db: zap vector search not yet implemented")
}

func (z *ZapDB) PutVector(ctx context.Context, kind string, id string, vector []float32, metadata map[string]interface{}) error {
	return errors.New("db: zap PutVector not yet implemented")
}

func (z *ZapDB) NewKey(kind string, stringID string, intID int64, parent Key) Key {
	id := stringID
	if id == "" && intID != 0 {
		id = fmt.Sprintf("%d", intID)
	}
	if id == "" {
		id = GenerateID()
	}
	return &zapKey{kind: kind, stringID: id, intID: intID, parent: parent}
}

func (z *ZapDB) NewIncompleteKey(kind string, parent Key) Key {
	return z.NewKey(kind, GenerateID(), 0, parent)
}

func (z *ZapDB) AllocateIDs(kind string, parent Key, n int) ([]Key, error) {
	keys := make([]Key, n)
	for i := 0; i < n; i++ {
		keys[i] = z.NewKey(kind, GenerateID(), 0, parent)
	}
	return keys, nil
}

func (z *ZapDB) RunInTransaction(ctx context.Context, fn func(tx Transaction) error, opts *TransactionOptions) error {
	// ZAP sidecar delegates transactions to the backend.
	return fn(&zapTransaction{db: z})
}

func (z *ZapDB) Close() error {
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.closed {
		return nil
	}
	z.closed = true
	z.node.Stop()
	return nil
}

func (z *ZapDB) TenantID() string   { return z.tenantID }
func (z *ZapDB) TenantType() string { return "zap" }

// --- SQL backend ---

func (z *ZapDB) sqlGet(ctx context.Context, key Key, dst interface{}) error {
	body, _ := json.Marshal(map[string]interface{}{
		"sql":  fmt.Sprintf("SELECT data FROM %s WHERE id = $1 AND kind = $2 AND deleted = false", z.cfg.Collection),
		"args": []interface{}{key.StringID(), key.Kind()},
	})
	status, resp, err := z.call(ctx, "/query", body)
	if err != nil {
		return err
	}
	if status != 200 {
		return ErrNoSuchEntity
	}
	return z.unmarshalSQLRows(resp, dst)
}

func (z *ZapDB) sqlPut(ctx context.Context, key Key, src interface{}) (Key, error) {
	data, err := json.Marshal(src)
	if err != nil {
		return nil, err
	}
	now := timeNow().Format(time.RFC3339)
	body, _ := json.Marshal(map[string]interface{}{
		"sql": fmt.Sprintf(`INSERT INTO %s (id, kind, data, created_at, updated_at, deleted)
			VALUES ($1, $2, $3, $4, $5, false)
			ON CONFLICT (id) DO UPDATE SET data = $3, updated_at = $5`, z.cfg.Collection),
		"args": []interface{}{key.StringID(), key.Kind(), string(data), now, now},
	})
	status, _, err := z.call(ctx, "/exec", body)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("db: zap sql put: status %d", status)
	}
	return key, nil
}

func (z *ZapDB) sqlDelete(ctx context.Context, key Key) error {
	body, _ := json.Marshal(map[string]interface{}{
		"sql":  fmt.Sprintf("UPDATE %s SET deleted = true, updated_at = $1 WHERE id = $2 AND kind = $3", z.cfg.Collection),
		"args": []interface{}{timeNow().Format(time.RFC3339), key.StringID(), key.Kind()},
	})
	_, _, err := z.call(ctx, "/exec", body)
	return err
}

// --- DocumentDB backend ---

func (z *ZapDB) docGet(ctx context.Context, key Key, dst interface{}) error {
	body, _ := json.Marshal(map[string]interface{}{
		"collection": z.cfg.Collection,
		"filter":     map[string]interface{}{"_id": key.StringID(), "kind": key.Kind(), "deleted": false},
		"limit":      1,
	})
	status, resp, err := z.call(ctx, "/find", body)
	if err != nil {
		return err
	}
	if status != 200 {
		return ErrNoSuchEntity
	}
	return z.unmarshalDocResult(resp, dst)
}

func (z *ZapDB) docPut(ctx context.Context, key Key, src interface{}) (Key, error) {
	data, _ := json.Marshal(src)
	var doc map[string]interface{}
	json.Unmarshal(data, &doc)
	doc["_id"] = key.StringID()
	doc["kind"] = key.Kind()
	doc["deleted"] = false
	doc["updatedAt"] = timeNow().Format(time.RFC3339)

	// Upsert via update, fall back to insert
	body, _ := json.Marshal(map[string]interface{}{
		"collection": z.cfg.Collection,
		"filter":     map[string]interface{}{"_id": key.StringID()},
		"update":     map[string]interface{}{"$set": doc},
	})
	status, _, err := z.call(ctx, "/update", body)
	if err != nil || status != 200 {
		body, _ = json.Marshal(map[string]interface{}{
			"collection": z.cfg.Collection,
			"documents":  []interface{}{doc},
		})
		_, _, err = z.call(ctx, "/insert", body)
		if err != nil {
			return nil, err
		}
	}
	return key, nil
}

func (z *ZapDB) docDelete(ctx context.Context, key Key) error {
	body, _ := json.Marshal(map[string]interface{}{
		"collection": z.cfg.Collection,
		"filter":     map[string]interface{}{"_id": key.StringID()},
		"update":     map[string]interface{}{"$set": map[string]interface{}{"deleted": true}},
	})
	_, _, err := z.call(ctx, "/update", body)
	return err
}

// --- KV backend ---

func (z *ZapDB) kvGet(ctx context.Context, key Key, dst interface{}) error {
	kvKey := fmt.Sprintf("%s:%s:%s", z.cfg.Collection, key.Kind(), key.StringID())
	body, _ := json.Marshal(map[string]interface{}{"key": kvKey})
	status, resp, err := z.call(ctx, "/get", body)
	if err != nil {
		return err
	}
	if status != 200 || len(resp) == 0 {
		return ErrNoSuchEntity
	}
	return json.Unmarshal(resp, dst)
}

func (z *ZapDB) kvPut(ctx context.Context, key Key, src interface{}) (Key, error) {
	data, _ := json.Marshal(src)
	kvKey := fmt.Sprintf("%s:%s:%s", z.cfg.Collection, key.Kind(), key.StringID())
	body, _ := json.Marshal(map[string]interface{}{
		"key":   kvKey,
		"value": string(data),
	})
	_, _, err := z.call(ctx, "/set", body)
	if err != nil {
		return nil, err
	}
	return key, nil
}

func (z *ZapDB) kvDelete(ctx context.Context, key Key) error {
	kvKey := fmt.Sprintf("%s:%s:%s", z.cfg.Collection, key.Kind(), key.StringID())
	body, _ := json.Marshal(map[string]interface{}{
		"cmd":  "DEL",
		"args": []string{kvKey},
	})
	_, _, err := z.call(ctx, "/cmd", body)
	return err
}

// --- Helpers ---

func (z *ZapDB) unmarshalSQLRows(resp []byte, dst interface{}) error {
	var rows []map[string]interface{}
	if err := json.Unmarshal(resp, &rows); err != nil {
		return json.Unmarshal(resp, dst)
	}
	if len(rows) == 0 {
		return ErrNoSuchEntity
	}
	if data, ok := rows[0]["data"]; ok {
		b, _ := json.Marshal(data)
		return json.Unmarshal(b, dst)
	}
	b, _ := json.Marshal(rows[0])
	return json.Unmarshal(b, dst)
}

func (z *ZapDB) unmarshalDocResult(resp []byte, dst interface{}) error {
	var result struct {
		Documents []json.RawMessage `json:"documents"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return json.Unmarshal(resp, dst)
	}
	if len(result.Documents) == 0 {
		return ErrNoSuchEntity
	}
	return json.Unmarshal(result.Documents[0], dst)
}

// --- zapKey ---

type zapKey struct {
	kind     string
	stringID string
	intID    int64
	parent   Key
}

func (k *zapKey) Kind() string      { return k.kind }
func (k *zapKey) StringID() string  { return k.stringID }
func (k *zapKey) IntID() int64      { return k.intID }
func (k *zapKey) Namespace() string { return "" }
func (k *zapKey) Incomplete() bool  { return k.stringID == "" && k.intID == 0 }

func (k *zapKey) Encode() string {
	if k.stringID != "" {
		return k.stringID
	}
	return fmt.Sprintf("%d", k.intID)
}

func (k *zapKey) Equal(other Key) bool {
	if other == nil {
		return false
	}
	return k.kind == other.Kind() && k.Encode() == other.Encode()
}

func (k *zapKey) Parent() Key { return k.parent }

// --- zapQuery ---

type zapQuery struct {
	db      *ZapDB
	kind    string
	filters []zapFilter
	order   string
	limit   int
	offset  int
}

type zapFilter struct {
	field string
	op    string
	value interface{}
}

func (q *zapQuery) Filter(filterStr string, value interface{}) Query {
	field, op := ParseFilterString(filterStr)
	nq := *q
	nq.filters = append(append([]zapFilter{}, q.filters...), zapFilter{field, op, value})
	return &nq
}

func (q *zapQuery) FilterField(fieldPath string, op string, value interface{}) Query {
	nq := *q
	nq.filters = append(append([]zapFilter{}, q.filters...), zapFilter{fieldPath, op, value})
	return &nq
}

func (q *zapQuery) Order(fieldPath string) Query     { nq := *q; nq.order = fieldPath; return &nq }
func (q *zapQuery) OrderDesc(fieldPath string) Query  { nq := *q; nq.order = "-" + fieldPath; return &nq }
func (q *zapQuery) Limit(limit int) Query             { nq := *q; nq.limit = limit; return &nq }
func (q *zapQuery) Offset(offset int) Query           { nq := *q; nq.offset = offset; return &nq }
func (q *zapQuery) Project(fieldNames ...string) Query { return q }
func (q *zapQuery) Distinct() Query                    { return q }
func (q *zapQuery) Ancestor(ancestor Key) Query        { return q }
func (q *zapQuery) Start(cursor Cursor) Query          { return q }
func (q *zapQuery) End(cursor Cursor) Query            { return q }

func (q *zapQuery) GetAll(ctx context.Context, dst interface{}) ([]Key, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, q.db.cfg.QueryTimeout)
	defer cancel()

	switch q.db.cfg.Backend {
	case ZapDocumentDB:
		return q.docGetAll(ctx, dst)
	default:
		return q.sqlGetAll(ctx, dst)
	}
}

func (q *zapQuery) First(ctx context.Context, dst interface{}) (Key, error) {
	limited := q.Limit(1)
	keys, err := limited.GetAll(ctx, dst)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, ErrNoSuchEntity
	}
	return keys[0], nil
}

func (q *zapQuery) Count(ctx context.Context) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, q.db.cfg.QueryTimeout)
	defer cancel()

	sql, args := q.buildSQL(true)
	body, _ := json.Marshal(map[string]interface{}{"sql": sql, "args": args})
	_, resp, err := q.db.call(ctx, "/query", body)
	if err != nil {
		return 0, err
	}
	var rows []map[string]interface{}
	json.Unmarshal(resp, &rows)
	if len(rows) == 0 {
		return 0, nil
	}
	if cnt, ok := rows[0]["count"]; ok {
		if v, ok := cnt.(float64); ok {
			return int(v), nil
		}
	}
	return 0, nil
}

func (q *zapQuery) Keys(ctx context.Context) ([]Key, error) { return q.GetAll(ctx, nil) }
func (q *zapQuery) Run(ctx context.Context) Iterator        { return nil }

func (q *zapQuery) buildSQL(countOnly bool) (string, []interface{}) {
	table := q.db.cfg.Collection
	args := []interface{}{q.kind}
	idx := 2

	var sql string
	if countOnly {
		sql = fmt.Sprintf("SELECT COUNT(*) as count FROM %s WHERE kind = $1 AND deleted = false", table)
	} else {
		sql = fmt.Sprintf("SELECT id, data FROM %s WHERE kind = $1 AND deleted = false", table)
	}

	for _, f := range q.filters {
		jsonField := ToJSONFieldName(f.field)
		op := NormalizeOp(f.op)
		sql += fmt.Sprintf(" AND json_extract(data, '$.%s') %s $%d", jsonField, op, idx)
		args = append(args, f.value)
		idx++
	}

	if !countOnly {
		if q.order != "" {
			desc := q.order[0] == '-'
			field := q.order
			if desc {
				field = field[1:]
			}
			jsonField := ToJSONFieldName(field)
			dir := "ASC"
			if desc {
				dir = "DESC"
			}
			sql += fmt.Sprintf(" ORDER BY json_extract(data, '$.%s') %s", jsonField, dir)
		}
		if q.limit > 0 {
			sql += fmt.Sprintf(" LIMIT %d", q.limit)
		}
		if q.offset > 0 {
			sql += fmt.Sprintf(" OFFSET %d", q.offset)
		}
	}
	return sql, args
}

func (q *zapQuery) sqlGetAll(ctx context.Context, dst interface{}) ([]Key, error) {
	sql, args := q.buildSQL(false)
	body, _ := json.Marshal(map[string]interface{}{"sql": sql, "args": args})
	status, resp, err := q.db.call(ctx, "/query", body)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("db: zap query: status %d", status)
	}

	var rows []map[string]interface{}
	json.Unmarshal(resp, &rows)

	keys := make([]Key, 0, len(rows))
	for _, row := range rows {
		id, _ := row["id"].(string)
		keys = append(keys, &zapKey{kind: q.kind, stringID: id})
	}

	if dst != nil && len(rows) > 0 {
		var dataList []json.RawMessage
		for _, row := range rows {
			if data, ok := row["data"]; ok {
				b, _ := json.Marshal(data)
				dataList = append(dataList, b)
			}
		}
		combined, _ := json.Marshal(dataList)
		json.Unmarshal(combined, dst)
	}
	return keys, nil
}

func (q *zapQuery) docGetAll(ctx context.Context, dst interface{}) ([]Key, error) {
	filter := map[string]interface{}{"kind": q.kind, "deleted": false}
	for _, f := range q.filters {
		jsonField := ToJSONFieldName(f.field)
		switch f.op {
		case "=", "==":
			filter[jsonField] = f.value
		case ">":
			filter[jsonField] = map[string]interface{}{"$gt": f.value}
		case ">=":
			filter[jsonField] = map[string]interface{}{"$gte": f.value}
		case "<":
			filter[jsonField] = map[string]interface{}{"$lt": f.value}
		case "<=":
			filter[jsonField] = map[string]interface{}{"$lte": f.value}
		case "!=":
			filter[jsonField] = map[string]interface{}{"$ne": f.value}
		}
	}
	args := map[string]interface{}{"collection": q.db.cfg.Collection, "filter": filter}
	if q.limit > 0 {
		args["limit"] = q.limit
	}
	body, _ := json.Marshal(args)
	_, resp, err := q.db.call(ctx, "/find", body)
	if err != nil {
		return nil, err
	}
	var result struct {
		Documents []json.RawMessage `json:"documents"`
	}
	json.Unmarshal(resp, &result)

	keys := make([]Key, 0, len(result.Documents))
	for _, doc := range result.Documents {
		var m map[string]interface{}
		json.Unmarshal(doc, &m)
		id, _ := m["_id"].(string)
		keys = append(keys, &zapKey{kind: q.kind, stringID: id})
	}
	if dst != nil && len(result.Documents) > 0 {
		combined, _ := json.Marshal(result.Documents)
		json.Unmarshal(combined, dst)
	}
	return keys, nil
}

// --- zapTransaction ---

type zapTransaction struct{ db *ZapDB }

func (t *zapTransaction) Get(key Key, dst interface{}) error {
	return t.db.Get(context.Background(), key, dst)
}

// GetForUpdate is Get over ZAP — ZAP's transaction model is application-level
// and the underlying backend handles locking. Treat as regular Get.
func (t *zapTransaction) GetForUpdate(key Key, dst interface{}) error {
	return t.db.Get(context.Background(), key, dst)
}

func (t *zapTransaction) Put(key Key, src interface{}) (Key, error) {
	return t.db.Put(context.Background(), key, src)
}

func (t *zapTransaction) Delete(key Key) error {
	return t.db.Delete(context.Background(), key)
}

func (t *zapTransaction) Query(kind string) Query {
	return t.db.Query(kind)
}
