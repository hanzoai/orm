package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/sqlite"
)

// Two processes, one file, no lost write.
//
// This is the property a rolling upgrade of a PVC-backed service depends on.
// `maxSurge: 1` puts the new pod on the node while the old one still serves, so
// for the length of the handover two PROCESSES hold the same SQLite file open.
// `writeMu` and `MaxOpenConns(1)` both end at the process boundary and say
// nothing about that window; only the lock SQLite itself takes does.
//
// The workload is the one that actually breaks: read a counter, add one, write
// it back, inside a transaction — hanzoai/iam's failed-login counter
// (internal/users/lockout.go), which decides account lockout, and its wallet
// challenge burn. If a single increment is lost, a lockout is undercounted.
//
// The test spawns real subprocesses rather than goroutines because goroutines
// share `writeMu`, which is precisely the guard that does not exist between
// pods. A goroutine version of this test passes with or without the fix and
// would therefore prove nothing.

const (
	envWriterDB = "ORM_WRITER_DB"
	envWriterN  = "ORM_WRITER_N"
	counterKey  = "counter"
)

func TestConcurrentProcessesLoseNoWrite(t *testing.T) {
	if path := os.Getenv(envWriterDB); path != "" {
		runIncrementer(t, path)
		return
	}

	const writers, each = 2, 200
	dbPath := filepath.Join(t.TempDir(), "writer.db")
	seedCounter(t, dbPath)

	start := time.Now().Add(startDelay)
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = spawnIncrementer(dbPath, each, start)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}

	got := readCounter(t, dbPath)
	if want := writers * each; got != want {
		t.Fatalf("counter = %d, want %d — %d increments were lost across %d processes",
			got, want, want-got, writers)
	}
}

// TestDeferredTxDropsWrites is the control: the SAME workload on a DEFERRED
// begin — what the write pool used before immediateTx, and what a BORROWED
// handle still gets unless its opener sets `_txlock` (AdaptSQLDB's obligation,
// which is the shape hanzoai/cloud's cek.Open + AdaptSQLDB takes).
//
// It asserts the bug is REAL. A hardening test that also passes against the
// unfixed code proves nothing, so this one FAILS if deferred transactions turn
// out to be fine — which would mean immediateTx is cargo cult.
//
// WHAT ACTUALLY HAPPENS, precisely — this is not a silent lost update. Under
// WAL, a deferred transaction that has read holds a snapshot; when it then
// writes, SQLite sees a newer commit and returns SQLITE_BUSY_SNAPSHOT rather
// than writing from the stale read. So SQLite refuses to corrupt. The write is
// DROPPED and an error is returned, and the transaction must be retried from
// the top — busy_timeout does not help, because the snapshot is already stale.
//
// That matters because an error is only as safe as its handler.
// hanzoai/iam's `internal/users/lockout.go` swallows it:
//
//	if txErr != nil {
//	    // Persist fault: keep the best-effort contract …
//	    return passwordOK, false
//	}
//
// so a dropped write becomes a failed-login counter that silently does not
// advance — i.e. during the two-process window of a rolling upgrade, wrong
// password attempts stop counting toward lockout. Immediate begin removes the
// error, and with it the swallow.
func TestDeferredTxDropsWrites(t *testing.T) {
	if path := os.Getenv(envWriterDB); path != "" {
		runIncrementer(t, path)
		return
	}

	const writers, each = 2, 200
	dbPath := filepath.Join(t.TempDir(), "deferred.db")
	seedCounter(t, dbPath)

	start := time.Now().Add(startDelay)
	var wg sync.WaitGroup
	failures := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			failures[i] = spawnIncrementerDeferred(dbPath, each, start)
		}()
	}
	wg.Wait()

	var reported int
	for _, err := range failures {
		if err != nil {
			reported++
			t.Logf("deferred writer failed as expected: %v", err)
		}
	}
	if reported == 0 {
		t.Fatal("no deferred writer failed — this control did not reproduce the contention it exists to demonstrate")
	}

	got := readCounter(t, dbPath)
	if want := writers * each; got == want {
		t.Fatalf("deferred begin reached %d/%d — it dropped no write, so "+
			"TestConcurrentProcessesLoseNoWrite is not measuring the immediate-begin fix", got, want)
	} else {
		t.Logf("deferred begin dropped %d of %d increments (counter=%d) — the failure immediateTx removes",
			want-got, want, got)
	}
}

func TestImmediateTxAppendsToEitherDSNForm(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"file:/x/y.db?_pragma=busy_timeout(10000)", "file:/x/y.db?_pragma=busy_timeout(10000)&_txlock=immediate"},
		{"file:/x/y.db", "file:/x/y.db?_txlock=immediate"},
	} {
		if got := immediateTx(tc.in); got != tc.want {
			t.Errorf("immediateTx(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestWritePoolBeginsImmediate proves the DSN parameter is honoured by the
// ACTIVE backend rather than accepted and ignored — the failure mode that made
// the pragma set wrong before it (a mattn-shaped DSN silently dropped by
// modernc). A second connection cannot begin while an immediate transaction is
// open; under a deferred begin it could.
func TestWritePoolBeginsImmediate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mode.db")
	db, err := NewSQLiteDB(&SQLiteDBConfig{Path: path, Config: SQLiteConfig{BusyTimeout: 200, JournalMode: "WAL"}})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	held, err := db.writeDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()

	// A separate pool on the same file. Its BEGIN must block on the RESERVED
	// lock the held transaction already owns, and time out at busy_timeout.
	other, err := sql.Open("sqlite", immediateTx(sqlite.PragmaDSN(path, configPragmas(SQLiteConfig{BusyTimeout: 200}))))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	tx, err := other.BeginTx(context.Background(), nil)
	if err == nil {
		tx.Rollback()
		t.Fatal("a second immediate BEGIN succeeded while one was open — _txlock=immediate was not applied by this backend")
	}
}

// runIncrementer is the child half: open the same file, read-modify-write the
// counter `n` times, each in its own transaction.
//
// Deferred writers keep going after a failure and report the total at the end,
// so the control measures how many writes were DROPPED rather than stopping at
// the first one. An immediate writer treats any error as fatal, because under
// immediate begin there is nothing left for it to legitimately fail on.
func runIncrementer(t *testing.T, path string) {
	t.Helper()
	n, err := strconv.Atoi(os.Getenv(envWriterN))
	if err != nil {
		t.Fatalf("bad %s: %v", envWriterN, err)
	}
	deferred := os.Getenv(envWriterDeferred) != ""
	db, err := openWriter(path, deferred)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	waitForStart(t)

	ctx := context.Background()
	var dropped int
	var lastErr error
	for range n {
		err := db.RunInTransaction(ctx, func(tx Transaction) error {
			v, err := readCounterTx(tx)
			if err != nil {
				return err
			}
			return writeCounterTx(tx, v+1)
		}, nil)
		if err == nil {
			continue
		}
		if !deferred {
			t.Fatalf("increment: %v", err)
		}
		dropped++
		lastErr = err
	}
	if dropped > 0 {
		t.Fatalf("dropped %d of %d increments; last error: %v", dropped, n, lastErr)
	}
}

// waitForStart holds the child at a wall-clock barrier the parent set, so both
// writers reach their first transaction together. Without it a short run can
// finish before the second process has opened the file, and the test would
// report a green that contention never happened.
func waitForStart(t *testing.T) {
	t.Helper()
	ns, err := strconv.ParseInt(os.Getenv(envWriterStart), 10, 64)
	if err != nil {
		return
	}
	if d := time.Until(time.Unix(0, ns)); d > 0 {
		time.Sleep(d)
	}
}

const (
	envWriterDeferred = "ORM_WRITER_DEFERRED"
	envWriterStart    = "ORM_WRITER_START"
	// startDelay is how long the parent gives every child to be spawned, linked
	// and open before the barrier releases. Process start dominates; the
	// transactions themselves are sub-millisecond.
	startDelay = 750 * time.Millisecond
)

// openWriter opens the store the way the process under test opens it. The
// deferred form goes through AdaptSQLDB over a caller-opened pool with no
// `_txlock` — exactly the borrowed-handle shape (cek.Open + AdaptSQLDB) that
// does not inherit NewSQLiteDB's write-pool DSN.
func openWriter(path string, deferred bool) (*SQLiteDB, error) {
	cfg := SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"}
	if !deferred {
		return NewSQLiteDB(&SQLiteDBConfig{Path: path, Config: cfg})
	}
	conn, err := sql.Open("sqlite", sqlite.PragmaDSN(path, configPragmas(cfg)))
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	return AdaptSQLDB(conn)
}

func seedCounter(t *testing.T, path string) {
	t.Helper()
	db, err := NewSQLiteDB(&SQLiteDBConfig{Path: path, Config: SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"}})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.writeDB.Exec(
		`INSERT INTO _metadata (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, counterKey, "0"); err != nil {
		t.Fatal(err)
	}
}

func readCounter(t *testing.T, path string) int {
	t.Helper()
	db, err := NewSQLiteDB(&SQLiteDBConfig{Path: path, Config: SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"}})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw string
	if err := db.readDB.QueryRow(`SELECT value FROM _metadata WHERE key = ?`, counterKey).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func readCounterTx(tx Transaction) (int, error) {
	st, ok := tx.(*sqliteTransaction)
	if !ok {
		return 0, fmt.Errorf("unexpected transaction type %T", tx)
	}
	var raw string
	if err := st.tx.QueryRow(`SELECT value FROM _metadata WHERE key = ?`, counterKey).Scan(&raw); err != nil {
		return 0, err
	}
	return strconv.Atoi(raw)
}

func writeCounterTx(tx Transaction, v int) error {
	st, ok := tx.(*sqliteTransaction)
	if !ok {
		return fmt.Errorf("unexpected transaction type %T", tx)
	}
	_, err := st.tx.Exec(`UPDATE _metadata SET value = ? WHERE key = ?`, strconv.Itoa(v), counterKey)
	return err
}

func spawnIncrementer(path string, n int, start time.Time) error {
	return spawn(path, n, "TestConcurrentProcessesLoseNoWrite", false, start)
}

func spawnIncrementerDeferred(path string, n int, start time.Time) error {
	return spawn(path, n, "TestDeferredTxDropsWrites", true, start)
}

// spawn re-executes THIS test binary as a child, selecting the incrementer
// branch with an environment variable. It is the only way to get a second
// process holding the same file, which is the whole point.
func spawn(path string, n int, name string, deferred bool, start time.Time) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "-test.run=^"+name+"$", "-test.timeout=120s")
	cmd.Env = append(os.Environ(),
		envWriterDB+"="+path,
		envWriterN+"="+strconv.Itoa(n),
		envWriterStart+"="+strconv.FormatInt(start.UnixNano(), 10),
	)
	if deferred {
		cmd.Env = append(cmd.Env, envWriterDeferred+"=1")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
