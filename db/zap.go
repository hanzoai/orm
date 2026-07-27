// ZAP protocol driver for the ORM.
//
// ZAP (Zero-Copy App Proto) uses binary encoding over RPC, communicating
// directly with ZAP-native backends (hanzo/sql, hanzo/kv, hanzo/datastore,
// hanzo/documentdb). Each backend speaks ZAP natively — no sidecar needed.
//
// Transport is github.com/zap-proto/http: a fasthttp-style request/response
// exchange carried over ZAP length-prefixed frames (encoded by the pure-Go
// zap-proto/go runtime). This is the same ZAP-HTTP transport the gateway,
// ingress, and luxd use — one and only one internal transport. The driver
// speaks it as a client: each backend op is a POST to a path (/query,
// /get, /set, /find, …) with a JSON body; the response carries a status and
// a JSON body. Routing is by address (each backend on its own port; see
// DefaultPorts) and by path — there is no peer-discovery layer, so the ORM
// takes no mDNS dependency.
package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
)

// ZapBackend selects which ZAP-native backend to connect to.
type ZapBackend int

const (
	// ZapSQL connects to hanzo/sql, the relational transactional backend, on
	// port 9651.
	ZapSQL ZapBackend = iota
	// ZapDocumentDB connects to hanzo/documentdb on port 9654. It serves
	// document semantics over relational storage, for clients that model data
	// as documents; the rows live in hanzo/sql.
	ZapDocumentDB
	// ZapKV connects to hanzo/kv, the key-value cache and session backend, on
	// port 9653.
	ZapKV
	// ZapDatastore connects to hanzo/datastore, the columnar analytics
	// backend, on port 9655.
	ZapDatastore
)

// DefaultPorts for each ZAP-native backend.
var DefaultPorts = map[ZapBackend]int{
	ZapSQL:        9651,
	ZapKV:         9653,
	ZapDocumentDB: 9654,
	ZapDatastore:  9655,
}

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

	// QueryTimeout is the per-query timeout (default 30s).
	QueryTimeout time.Duration
}

// ZapDB implements db.DB over the ZAP-HTTP binary protocol.
type ZapDB struct {
	transport *zaphttp.Transport
	cfg       ZapConfig
	mu        sync.RWMutex
	closed    bool
	tenantID  string
}

// NewZapDB dials a ZAP-native backend and returns a DB implementation. The
// transport connects lazily on the first operation, so this does not fail
// when the backend is momentarily unreachable — the first Get/Put surfaces a
// clear dial error instead.
func NewZapDB(cfg *ZapConfig) (*ZapDB, error) {
	if cfg.Addr == "" {
		port, ok := DefaultPorts[cfg.Backend]
		if !ok {
			return nil, errors.New("db: zap addr required")
		}
		cfg.Addr = fmt.Sprintf("localhost:%d", port)
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
	if cfg.QueryTimeout == 0 {
		cfg.QueryTimeout = 30 * time.Second
	}

	t := zaphttp.NewTransport(cfg.Addr)
	t.SetReadTimeout(cfg.QueryTimeout)

	return &ZapDB{
		transport: t,
		cfg:       *cfg,
	}, nil
}

// call executes one ZAP-HTTP request/response exchange: POST path with a JSON
// body, returning the response status and body. The transport owns framing
// and the ZAP wire codec.
func (z *ZapDB) call(ctx context.Context, path string, body []byte) (uint32, []byte, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(fasthttp.MethodPost)
	req.SetRequestURI(path)
	req.Header.SetHost(z.cfg.Addr)
	req.Header.SetContentType("application/json")
	req.SetBody(body)

	if err := z.transport.Do(req, resp); err != nil {
		return 0, nil, fmt.Errorf("db: zap call %s: %w", path, err)
	}

	// The response body is owned by resp, which is released on return; copy it
	// out so the caller keeps a stable slice.
	out := append([]byte(nil), resp.Body()...)
	return uint32(resp.StatusCode()), out, nil
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

// CreateIfAbsent conditionally inserts src under key, first-writer-wins. See
// db.DB.CreateIfAbsent for the contract. Dispatch mirrors Put: each ZAP-native
// backend uses its own conditional-insert primitive (SQL ON CONFLICT, Valkey
// SET NX, document unique _id). The reply decides created; an unrecognized or
// error reply returns an error rather than a guessed created value, so a caller
// never mistakes an upsert or a transport failure for a first-writer win.
//
// Status: the hanzo ZAP backends do not yet expose a zap-proto/http listener
// (see LLM.md), so these paths are wire-complete but exercised only by the
// env-gated live integration test, not unit CI. The SQLite backend is the
// fully-tested reference implementation of the identical contract.
func (z *ZapDB) CreateIfAbsent(ctx context.Context, key Key, src interface{}) (bool, error) {
	if key == nil || key.Incomplete() {
		return false, ErrInvalidKey
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, z.cfg.QueryTimeout)
	defer cancel()

	switch z.cfg.Backend {
	case ZapDocumentDB:
		return z.docCreateIfAbsent(ctx, key, src)
	case ZapKV:
		return z.kvCreateIfAbsent(ctx, key, src)
	default:
		return z.sqlCreateIfAbsent(ctx, key, src)
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
	// The backend owns transaction semantics; the driver runs fn against a
	// thin transaction view that forwards each op over ZAP.
	return fn(&zapTransaction{db: z})
}

func (z *ZapDB) Close() error {
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.closed {
		return nil
	}
	z.closed = true
	z.transport.CloseIdleConnections()
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

// sqlCreateIfAbsent is the SQL-backend conditional insert. It mirrors the SQLite
// createIfAbsentSQL: a fresh id inserts, a SAME-KIND soft-deleted id resurrects
// (kind is guarded in the WHERE and never set, so no type confusion), a live id
// leaves the DO UPDATE ... WHERE guard unsatisfied. RETURNING id makes the
// created signal a row-count, read over the same /query row-array envelope
// sqlGet consumes — so it depends on no new wire contract.
//
// Whether a different-kind id collision surfaces loudly here depends on the
// server table's key (an id primary key vs a composite (id, kind)); the caller
// precondition — keep each kind in its own stringID keyspace — governs it either
// way. This path is exercised by the env-gated live test, not unit CI.
func (z *ZapDB) sqlCreateIfAbsent(ctx context.Context, key Key, src interface{}) (bool, error) {
	data, err := json.Marshal(src)
	if err != nil {
		return false, err
	}
	now := timeNow().Format(time.RFC3339)
	body, _ := json.Marshal(map[string]interface{}{
		"sql": fmt.Sprintf(`INSERT INTO %s (id, kind, data, created_at, updated_at, deleted)
			VALUES ($1, $2, $3, $4, $5, false)
			ON CONFLICT (id) DO UPDATE SET
				data = $3, updated_at = $5, deleted = false
			WHERE %s.deleted = true AND %s.kind = $2
			RETURNING id`, z.cfg.Collection, z.cfg.Collection, z.cfg.Collection),
		"args": []interface{}{key.StringID(), key.Kind(), string(data), now, now},
	})
	status, resp, err := z.call(ctx, "/query", body)
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("db: zap sql create-if-absent: status %d", status)
	}
	return zapRowsReturned(resp)
}

// zapRowsReturned reports whether a /query reply carried at least one row — the
// created signal for INSERT ... ON CONFLICT ... RETURNING. It reuses the row-array
// envelope sqlGet decodes; an undecodable reply is an error, never a created win.
func zapRowsReturned(resp []byte) (bool, error) {
	var rows []map[string]interface{}
	if err := json.Unmarshal(resp, &rows); err != nil {
		return false, fmt.Errorf("db: zap create-if-absent: decode reply: %w", err)
	}
	return len(rows) > 0, nil
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

// docCreateIfAbsent is the document-backend conditional insert. The backend's
// unique _id index is the first-writer-wins gate: the winning /insert reports
// created=true; a duplicate _id (the key already holds a document) reports
// created=false. An unexpected status is an error, never a created win.
//
// src must marshal to a JSON object so the _id/kind envelope fields can be set;
// a non-object (array/scalar) is rejected rather than panicking on a nil map.
//
// Limitation (flagged, untested-here — the backend exposes no listener yet):
// existence keys on _id alone, so a soft-deleted _id reports created=false while
// docGet filters deleted, and a different-kind _id does not surface as
// ErrKindMismatch the way the SQLite path does. The caller preconditions —
// separate keyspace per kind, normalize before the key — cover both on this
// backend until it is live-verified.
func (z *ZapDB) docCreateIfAbsent(ctx context.Context, key Key, src interface{}) (bool, error) {
	data, err := json.Marshal(src)
	if err != nil {
		return false, err
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return false, fmt.Errorf("db: zap document create-if-absent: src must be a JSON object: %w", err)
	}
	if doc == nil {
		return false, fmt.Errorf("db: zap document create-if-absent: src must be a JSON object, got null or non-object")
	}
	doc["_id"] = key.StringID()
	doc["kind"] = key.Kind()
	doc["deleted"] = false
	doc["createdAt"] = timeNow().Format(time.RFC3339)
	doc["updatedAt"] = timeNow().Format(time.RFC3339)

	body, _ := json.Marshal(map[string]interface{}{
		"collection": z.cfg.Collection,
		"documents":  []interface{}{doc},
	})
	status, _, err := z.call(ctx, "/insert", body)
	if err != nil {
		return false, err
	}
	switch status {
	case fasthttp.StatusOK:
		return true, nil
	case fasthttp.StatusConflict:
		// Duplicate _id — a document already holds the key.
		return false, nil
	default:
		return false, fmt.Errorf("db: zap document create-if-absent: status %d", status)
	}
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

// kvCreateIfAbsent is the KV-backend conditional insert via Valkey SET ... NX,
// the KV-native first-writer-wins gate. NX writes only when the key is absent
// (the KV backend hard-deletes, so absent == not present, no soft-delete case).
// Valkey replies +OK when the write applied and a null bulk string when NX
// suppressed it; any other reply is an error, never a created win.
func (z *ZapDB) kvCreateIfAbsent(ctx context.Context, key Key, src interface{}) (bool, error) {
	data, _ := json.Marshal(src)
	kvKey := fmt.Sprintf("%s:%s:%s", z.cfg.Collection, key.Kind(), key.StringID())
	body, _ := json.Marshal(map[string]interface{}{
		"cmd":  "SET",
		"args": []string{kvKey, string(data), "NX"},
	})
	status, resp, err := z.call(ctx, "/cmd", body)
	if err != nil {
		return false, err
	}
	if status != fasthttp.StatusOK {
		return false, fmt.Errorf("db: zap kv create-if-absent: status %d", status)
	}
	switch strings.TrimSpace(string(resp)) {
	case "OK", `"OK"`, "+OK":
		return true, nil
	case "", "nil", "null", "$-1":
		return false, nil
	default:
		return false, fmt.Errorf("db: zap kv create-if-absent: unexpected reply %q", resp)
	}
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

func (q *zapQuery) Order(fieldPath string) Query { nq := *q; nq.order = fieldPath; return &nq }
func (q *zapQuery) OrderDesc(fieldPath string) Query {
	nq := *q
	nq.order = "-" + fieldPath
	return &nq
}
func (q *zapQuery) Limit(limit int) Query              { nq := *q; nq.limit = limit; return &nq }
func (q *zapQuery) Offset(offset int) Query            { nq := *q; nq.offset = offset; return &nq }
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

// CreateIfAbsent forwards to the DB; ZAP's transaction model is application-level
// and the underlying backend owns the conditional-insert atomicity.
func (t *zapTransaction) CreateIfAbsent(key Key, src interface{}) (bool, error) {
	return t.db.CreateIfAbsent(context.Background(), key, src)
}

func (t *zapTransaction) Delete(key Key) error {
	return t.db.Delete(context.Background(), key)
}

func (t *zapTransaction) Query(kind string) Query {
	return t.db.Query(kind)
}
