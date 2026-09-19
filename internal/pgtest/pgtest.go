// Package pgtest runs a throwaway PostgreSQL server for one test binary.
//
// A suite that claims to hold on PostgreSQL has to have run on PostgreSQL. So
// this finds the server binaries installed on the machine, makes a cluster in a
// fresh temporary directory, and starts it listening on a Unix socket in that
// directory and nowhere else. Nothing is mocked and nothing is shared with
// another run.
//
// When a machine has no server to find, Start says so instead of failing: a
// suite reports the backend it did not reach by name, which is a fact a reader
// can act on, and still runs everything else.
package pgtest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	// The one PostgreSQL driver, registered as "pgx".
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Super is the cluster's superuser. Tests use it only to make roles and
// databases; a statement under test never runs as it, because a superuser
// ignores row-level security and would prove nothing.
const Super = "postgres"

// port only names the socket file; the server listens on no TCP address.
const port = 5432

// Server is one running cluster.
type Server struct {
	// Major is the server's major version.
	Major int

	dir  string
	cmd  *exec.Cmd
	done chan error
}

// Start makes and starts a cluster from the newest PostgreSQL on this machine.
// It returns the reason instead when there is none, or when it will not start.
func Start() (*Server, string) {
	bin, major, why := find()
	if bin == "" {
		return nil, why
	}
	// The socket's full path has to fit in sun_path (104 bytes on macOS), so the
	// directory is short and at the top of the temporary tree.
	dir, err := os.MkdirTemp("", "pg")
	if err != nil {
		return nil, fmt.Sprintf("make a directory for the cluster: %v", err)
	}
	s := &Server{Major: major, dir: dir, done: make(chan error, 1)}
	if why := s.start(bin); why != "" {
		s.Stop()
		return nil, why
	}
	return s, ""
}

func (s *Server) data() string { return filepath.Join(s.dir, "data") }

func (s *Server) start(bin string) string {
	initdb := exec.Command(filepath.Join(bin, "initdb"),
		"-D", s.data(), "-U", Super, "-A", "trust", "-E", "UTF8",
		"--locale=C", "--no-sync", "--no-instructions")
	if out, err := initdb.CombinedOutput(); err != nil {
		return fmt.Sprintf("initdb: %v: %s", err, strings.TrimSpace(string(out)))
	}
	log, err := os.Create(filepath.Join(s.dir, "server.log"))
	if err != nil {
		return fmt.Sprintf("server log: %v", err)
	}
	s.cmd = exec.Command(filepath.Join(bin, "postgres"),
		"-D", s.data(),
		"-k", s.dir,
		"-p", strconv.Itoa(port),
		"-c", "listen_addresses=",
		"-c", "fsync=off",
		"-c", "full_page_writes=off",
		"-c", "synchronous_commit=off",
		"-c", "max_connections=200",
	)
	s.cmd.Stdout, s.cmd.Stderr = log, log
	if err := s.cmd.Start(); err != nil {
		_ = log.Close()
		return fmt.Sprintf("start postgres: %v", err)
	}
	go func() { s.done <- s.cmd.Wait(); _ = log.Close() }()

	deadline := time.Now().Add(30 * time.Second)
	for {
		db, err := sql.Open("pgx", s.DSN(Super, "postgres"))
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err = db.PingContext(ctx)
			cancel()
			_ = db.Close()
		}
		if err == nil {
			return ""
		}
		select {
		case werr := <-s.done:
			s.done <- werr
			raw, _ := os.ReadFile(filepath.Join(s.dir, "server.log"))
			return fmt.Sprintf("postgres exited: %v: %s", werr, strings.TrimSpace(string(raw)))
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Sprintf("postgres did not accept a connection within 30s: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// DSN opens a connection as user to database db over the cluster's socket.
func (s *Server) DSN(user, db string) string {
	return fmt.Sprintf("host=%s port=%d user=%s dbname=%s sslmode=disable", s.dir, port, user, db)
}

// Open returns a pool connected as user to database db.
func (s *Server) Open(user, db string) (*sql.DB, error) {
	conn, err := sql.Open("pgx", s.DSN(user, db))
	if err != nil {
		return nil, err
	}
	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// Exec runs each statement as the superuser in database db, in order.
func (s *Server) Exec(db string, stmts ...string) error {
	conn, err := s.Open(Super, db)
	if err != nil {
		return err
	}
	defer conn.Close()
	for _, stmt := range stmts {
		if _, err := conn.Exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

// Stop shuts the server down and removes the cluster.
func (s *Server) Stop() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(syscall.SIGINT) // fast shutdown
		select {
		case <-s.done:
		case <-time.After(10 * time.Second):
			_ = s.cmd.Process.Kill()
			<-s.done
		}
	}
	_ = os.RemoveAll(s.dir)
}

var versionRe = regexp.MustCompile(`\(PostgreSQL\) (\d+)`)

// find returns the directory holding the newest postgres + initdb pair on this
// machine and its major version, or why there is none.
func find() (dir string, major int, why string) {
	if os.Geteuid() == 0 {
		return "", 0, "running as root, and postgres refuses to"
	}
	seen := map[string]bool{}
	var dirs []string
	add := func(d string) {
		if d == "" || seen[d] {
			return
		}
		seen[d] = true
		dirs = append(dirs, d)
	}
	if p, err := exec.LookPath("postgres"); err == nil {
		add(filepath.Dir(p))
	}
	if out, err := exec.Command("pg_config", "--bindir").Output(); err == nil {
		add(strings.TrimSpace(string(out)))
	}
	for _, pattern := range []string{
		"/opt/homebrew/opt/postgresql*/bin",
		"/usr/local/opt/postgresql*/bin",
		"/usr/lib/postgresql/*/bin",
		"/usr/local/pgsql/bin",
	} {
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			add(m)
		}
	}

	type found struct {
		dir   string
		major int
	}
	var all []found
	for _, d := range dirs {
		if !executable(filepath.Join(d, "initdb")) || !executable(filepath.Join(d, "postgres")) {
			continue
		}
		out, err := exec.Command(filepath.Join(d, "postgres"), "--version").Output()
		if err != nil {
			continue
		}
		m := versionRe.FindSubmatch(out)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(string(m[1]))
		all = append(all, found{d, n})
	}
	if len(all) == 0 {
		return "", 0, "no postgres + initdb on PATH, from pg_config, or in a standard install directory"
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].major > all[j].major })
	return all[0].dir, all[0].major, ""
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}
