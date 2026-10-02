package db

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
)

// trackedListener remembers the connections it accepts, so a test can end them
// the way a restarting server does.
type trackedListener struct {
	net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func (l *trackedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.mu.Lock()
		l.conns = append(l.conns, c)
		l.mu.Unlock()
	}
	return c, err
}

func (l *trackedListener) dropAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.conns {
		_ = c.Close()
	}
	l.conns = nil
}

// A server that ends every connection between two requests — a restart, a
// failover — costs the next statement nothing: it is sent again on a fresh one.
func TestZapStatementSurvivesAServerThatDroppedItsConnections(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tl := &trackedListener{Listener: ln}
	srv := &zaphttp.Server{Handler: func(c *fasthttp.RequestCtx) {
		c.SetStatusCode(200)
		c.SetBodyString(`[{"id":"k1","data":{"name":"one"}}]`)
	}}
	go func() { _ = srv.Serve(tl) }()
	t.Cleanup(func() { _ = ln.Close() })

	z, err := NewZapDB(&ZapConfig{Addr: ln.Addr().String(), Backend: ZapSQL, QueryTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := z.Get(context.Background(), z.NewKey("k", "k1", 0, nil), &got); err != nil {
		t.Fatalf("first get: %v", err)
	}
	tl.dropAll()
	time.Sleep(50 * time.Millisecond)
	if err := z.Get(context.Background(), z.NewKey("k", "k1", 0, nil), &got); err != nil {
		t.Fatalf("get after the server dropped its connections: %v", err)
	}
	if got["name"] != "one" {
		t.Fatalf("got %v", got)
	}
}
