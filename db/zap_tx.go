package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	zaphttp "github.com/zap-proto/http"
)

// zap_tx.go is what makes hanzo/sql a store several processes can share: a
// transaction that is one, and an index on every field a model tags.
//
// A transaction holds ONE connection for its whole life. hanzo/sql opens it on
// /begin and runs every statement that arrives on that connection inside it
// until /commit or /rollback, so a read-check-write — a unique email, a code used
// once — is decided by the server, not by which process wrote last. It runs at
// SERIALIZABLE: two transactions that could not have run one after the other are
// refused at the second (409, ErrSerializationFailure), and that one runs again.

// ErrSerializationFailure is a transaction the server refused because another
// could not be serialized with it (SQLSTATE 40001, or a deadlock). Running it
// again is the answer; orm's RunInTransactionWith does.
var ErrSerializationFailure = errors.New("db: serialization failure")

// txAttempts is how many times a plain RunInTransaction runs fn on a SQL backend
// before a serialization failure is returned: on a server several processes share,
// a refused transaction is ordinary, not exceptional.
const txAttempts = 5

// sqlRunInTransaction runs fn in a SERIALIZABLE transaction on its own connection.
// With opts from RunInTransactionWith the caller owns retry; a plain call (nil
// opts) retries a serialization failure here.
func (z *ZapDB) sqlRunInTransaction(ctx context.Context, fn func(tx Transaction) error, opts *TransactionOptions) error {
	if z.inTx {
		return errors.New("db: zap sql: a transaction is already open on this handle")
	}
	attempts := 1
	if opts == nil {
		attempts = txAttempts
	}
	var err error
	for i := 0; i < attempts; i++ {
		err = z.sqlTransaction(ctx, fn)
		if !errors.Is(err, ErrSerializationFailure) {
			return err
		}
	}
	return err
}

// sqlTransaction is one attempt.
func (z *ZapDB) sqlTransaction(ctx context.Context, fn func(tx Transaction) error) error {
	t := zaphttp.Dial("tcp", z.cfg.Addr)
	t.SetReadTimeout(z.cfg.QueryTimeout)
	t.SetMaxIdleConns(1)
	defer t.CloseIdleConnections()
	tx := &ZapDB{transport: t, cfg: z.cfg, tenantID: z.tenantID, inTx: true, indexed: z.indexed}

	if err := tx.control(ctx, "begin", map[string]any{"isolation": "serializable"}); err != nil {
		return err
	}
	if err := fn(&zapTransaction{db: tx, ctx: ctx}); err != nil {
		// The rollback is best-effort: a connection that is gone has taken its
		// transaction with it.
		_ = tx.control(context.Background(), "rollback", nil)
		return err
	}
	return tx.control(ctx, "commit", nil)
}

// control sends /begin, /commit or /rollback and maps the answer to an error.
func (z *ZapDB) control(ctx context.Context, op string, body map[string]any) error {
	b := []byte("{}")
	if body != nil {
		b, _ = json.Marshal(body)
	}
	status, resp, err := z.call(ctx, "/"+op, b)
	if err != nil {
		return err
	}
	if status != 200 {
		return sqlError(op, status, resp)
	}
	return nil
}

// sqlError is the error a non-200 reply from hanzo/sql stands for. A 409 is a
// serialization failure; anything else names the operation, the status and the
// server's message, never "not found".
func sqlError(op string, status uint32, resp []byte) error {
	var reply struct {
		Error    string `json:"error"`
		SQLState string `json:"sqlstate"`
	}
	_ = json.Unmarshal(resp, &reply)
	msg := reply.Error
	if reply.SQLState != "" {
		msg += " (" + reply.SQLState + ")"
	}
	if status == 409 {
		return fmt.Errorf("%w: zap sql %s: %s", ErrSerializationFailure, op, msg)
	}
	return fmt.Errorf("db: zap sql %s: status %d: %s", op, status, msg)
}

// forUpdateKey marks a Get that locks its row for the enclosing transaction.
type forUpdateKey struct{}

func withForUpdate(ctx context.Context) context.Context {
	return context.WithValue(ctx, forUpdateKey{}, true)
}

// forUpdate is the clause a Get carries when its context asks for a row lock.
func forUpdate(ctx context.Context) string {
	if v, _ := ctx.Value(forUpdateKey{}).(bool); v {
		return " FOR UPDATE"
	}
	return ""
}

// pgKindClause is the leading condition of every statement over one kind, and the
// predicate of every index on it: the kind as a literal, and live rows only.
func pgKindClause(kind string) string {
	return "kind = " + kindLiteral(kind) + " AND deleted = false"
}

// Index gives each field path of kind its own partial expression index on the SQL
// backend, the same index a model's orm:"index" tag gives it on SQLite:
//
//	CREATE INDEX IF NOT EXISTS "idx_users_email" ON _entities ((data->>'email'))
//	WHERE kind = 'users' AND deleted = false
//
// Partial, because the table holds every kind and an insert should maintain only
// its own kind's indexes. Idempotent: IF NOT EXISTS, and a path this process has
// indexed is skipped. Other backends index nothing here.
func (z *ZapDB) Index(kind string, paths []string) error {
	if z.cfg.Backend != ZapSQL {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), z.cfg.QueryTimeout)
	defer cancel()
	for _, p := range paths {
		p = ToJSONFieldName(p)
		if p == "" {
			continue
		}
		key := kind + "\x00" + p
		if _, done := z.indexed.Load(key); done {
			continue
		}
		body, _ := json.Marshal(map[string]any{"sql": pgIndexDDL(z.cfg.Collection, kind, p)})
		status, resp, err := z.call(ctx, "/exec", body)
		if err != nil {
			return fmt.Errorf("db: index %s.%s: %w", kind, p, err)
		}
		if status != 200 {
			return fmt.Errorf("db: index %s.%s: %w", kind, p, sqlError("index", status, resp))
		}
		z.indexed.Store(key, true)
	}
	return nil
}

// indexedPath reports whether this process has indexed path of kind (Index).
func (z *ZapDB) indexedPath(kind, path string) bool {
	if z.indexed == nil {
		return false
	}
	_, ok := z.indexed.Load(kind + "\x00" + ToJSONFieldName(path))
	return ok
}

// pgIndexDDL is the statement that indexes one path of one kind. Its expression is
// the one the query builder filters on (jsonField) and its predicate the one every
// statement over the kind states (pgKindClause).
func pgIndexDDL(table, kind, path string) string {
	return fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s ((%s)) WHERE %s`,
		quoteIdent(pgIndexName(table, kind, path)), table, jsonField(path), pgKindClause(kind))
}

// pgIndexName names an index within PostgreSQL's 63-byte identifier limit. An index
// name is unique across a schema, not per table, so a collection other than the
// default carries its table in the name: two collections in one database must not
// each read the other's index as already built. A name past the limit would be
// truncated, and two truncated names can collide the same way; so a long name
// keeps its prefix and ends in a hash of the whole.
func pgIndexName(table, kind, path string) string {
	name := indexPrefix + kind + "_" + path
	if table != "_entities" {
		name = indexPrefix + table + "_" + kind + "_" + path
	}
	if len(name) <= 63 {
		return name
	}
	sum := sha256.Sum256([]byte(table + "\x00" + kind + "\x00" + path))
	return name[:50] + "_" + hex.EncodeToString(sum[:6])
}
