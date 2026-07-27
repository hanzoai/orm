package db

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// noopDB makes opening free, so these benchmarks measure the registry's own
// bookkeeping — lock traffic, LRU maintenance, eviction scans — and not the
// cost of a stand-in. One shared zero-size value, so allocs/op is the
// registry's own and nothing else.
type noopDB struct{ DB }

func (noopDB) Close() error { return nil }

var (
	benchHandle DB = noopDB{}
	benchFn        = func(DB) error { return nil }
)

func benchRegistry(b *testing.B, cfg NamespacesConfig[DB]) *Namespaces[DB] {
	b.Helper()
	if cfg.Dir == "" {
		cfg.Dir = b.TempDir()
	}
	if cfg.Open == nil {
		cfg.Open = func(Namespace, string) (DB, error) { return benchHandle, nil }
	}
	r, err := NewNamespaces(cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = r.Close() })
	return r
}

func benchSpaces(n int) []Namespace {
	ts := make([]Namespace, n)
	for i := range ts {
		ts[i] = Namespace("org" + "/" + strconv.Itoa(i))
	}
	return ts
}

// warm opens every tenant once, so the benchmark that follows measures hits
// rather than opens.
func warm(b *testing.B, r *Namespaces[DB], ts []Namespace) {
	b.Helper()
	for _, t := range ts {
		if err := r.With(context.Background(), t, benchFn); err != nil {
			b.Fatalf("warm %s: %v", t, err)
		}
	}
}

func idleName(d time.Duration) string {
	if d == 0 {
		return "off"
	}
	return "on"
}

// BenchmarkRegistryWarmHit is the common path: a request arrives for a tenant
// whose database is already open, so all the registry does is bookkeeping.
//
// The resident dimension is here because eviction runs on every release. If
// that scan is O(resident), the common path gets slower the more spaces a node
// holds — exactly backwards for the type whose job is holding many spaces.
// IdleTTL is the other axis for the same reason: the idle sweep walks the LRU
// unconditionally, and production runs with a TTL set.
func BenchmarkRegistryWarmHit(b *testing.B) {
	for _, resident := range []int{1, 64, 1024} {
		for _, idle := range []time.Duration{0, time.Hour} {
			b.Run(fmt.Sprintf("resident=%d/idlettl=%s", resident, idleName(idle)), func(b *testing.B) {
				ts := benchSpaces(resident)
				r := benchRegistry(b, NamespacesConfig[DB]{MaxOpen: resident, IdleTTL: idle})
				warm(b, r, ts)
				hot := ts[0] // one hot tenant; the rest are resident weight
				ctx := context.Background()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := r.With(ctx, hot, benchFn); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkRegistryConcurrentHits models the actual server: many goroutines,
// each serving a DIFFERENT tenant, every database already open. None of that
// work needs to be serialised — the databases are disjoint — so whatever this
// costs above the single-threaded warm hit is contention on the registry lock.
func BenchmarkRegistryConcurrentHits(b *testing.B) {
	for _, spaces := range []int{1, 64, 1024} {
		b.Run(fmt.Sprintf("spaces=%d", spaces), func(b *testing.B) {
			ts := benchSpaces(spaces)
			r := benchRegistry(b, NamespacesConfig[DB]{MaxOpen: spaces})
			warm(b, r, ts)
			ctx := context.Background()
			var seq atomic.Int64
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				// Stride the start so goroutines are not all on the same tenant.
				i := int(seq.Add(1)) * 7919
				for pb.Next() {
					if err := r.With(ctx, ts[i%len(ts)], benchFn); err != nil {
						b.Error(err)
						return
					}
					i++
				}
			})
		})
	}
}

// BenchmarkRegistryColdMiss is the first request for a tenant this node is not
// holding: stat the local cache, restore from object storage on a miss, open.
// The materialize dimension is the production shape — disk is a cache, the
// source of truth is remote.
func BenchmarkRegistryColdMiss(b *testing.B) {
	for _, mat := range []bool{false, true} {
		b.Run(fmt.Sprintf("materialize=%t", mat), func(b *testing.B) {
			cfg := NamespacesConfig[DB]{MaxOpen: 1}
			if mat {
				// A restore that writes nothing is legal ("new tenant, start
				// empty") and keeps the number about the registry rather than
				// about how fast this disk can copy a file.
				cfg.Materialize = func(context.Context, Namespace, string) error { return nil }
			}
			r := benchRegistry(b, cfg)
			// MaxOpen 1 against a pool of 16: nothing is ever resident, so every
			// iteration takes the open path.
			ts := benchSpaces(16)
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := r.With(ctx, ts[i%len(ts)], benchFn); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkRegistryEvictionThrash is a working set far larger than the bound,
// touched round-robin: the worst case, where every request evicts. MaxOpen is
// the interesting axis — eviction has to pick a victim among the open handles,
// so a node configured to hold more handles pays more per eviction.
func BenchmarkRegistryEvictionThrash(b *testing.B) {
	for _, maxOpen := range []int{8, 128, 1024} {
		b.Run(fmt.Sprintf("maxopen=%d", maxOpen), func(b *testing.B) {
			// Four times the bound, so the round trip never finds a warm handle.
			ts := benchSpaces(maxOpen * 4)
			r := benchRegistry(b, NamespacesConfig[DB]{MaxOpen: maxOpen})
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := r.With(ctx, ts[i%len(ts)], benchFn); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkRegistryHitRate measures what every other benchmark here cannot:
// how often the registry throws away a handle it should have kept.
//
// The rest of this file reports ns/op, and that is precisely how sampled
// eviction hid. Sampling IS faster per eviction; what it costs is accuracy —
// it evicts a hot handle whenever the true coldest was not in the sample — and
// the price of that shows up as a later miss, in a different operation, which
// a latency average absorbs. So this reports miss% as a custom metric.
//
// The two regimes are the point of the comparison, and they must be read
// together:
//
//	materialize=off  a miss is a local open        -> sampling is worth it
//	materialize=on   a miss is a restore from S3   -> sampling inverts
//
// Zipf-ish 90/10: nine in ten requests land in a hot set of 102 tenants out of
// 1024, against MaxOpen 128. Deterministic xorshift so the two regimes see the
// identical request stream and the miss counts are directly comparable.
func BenchmarkRegistryHitRate(b *testing.B) {
	const (
		spaces  = 1024
		hot     = 102
		maxOpen = 128
		ops     = 50000
	)
	// A Materialize that does nothing. Its PRESENCE is the whole variable —
	// it is what tells the registry a miss is expensive. Doing real work here
	// would only add noise to a number we already know how to price (~30ms a
	// GET), and the count is what this measures.
	present := func(context.Context, Namespace, string) error { return nil }

	for _, regime := range []struct {
		name        string
		materialize func(context.Context, Namespace, string) error
	}{
		{"materialize=off", nil},
		{"materialize=on", present},
	} {
		b.Run(regime.name, func(b *testing.B) {
			var opens int64
			r, err := NewNamespaces(NamespacesConfig[*fakeDB]{
				Dir:         b.TempDir(),
				MaxOpen:     maxOpen,
				Materialize: regime.materialize,
				Open: func(t Namespace, path string) (*fakeDB, error) {
					atomic.AddInt64(&opens, 1)
					return &fakeDB{tenant: t}, nil
				},
			})
			if err != nil {
				b.Fatal(err)
			}
			defer r.Close()

			ctx := context.Background()
			seed := uint64(12345)
			next := func() uint64 { seed ^= seed << 13; seed ^= seed >> 7; seed ^= seed << 17; return seed }

			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				atomic.StoreInt64(&opens, 0)
				for i := 0; i < ops; i++ {
					var id uint64
					if next()%10 < 9 {
						id = next() % hot
					} else {
						id = next() % spaces
					}
					_ = r.With(ctx, Namespace("org/"+fmt.Sprint(id)), func(*fakeDB) error { return nil })
				}
				b.ReportMetric(float64(atomic.LoadInt64(&opens))*100/float64(ops), "miss%")
			}
		})
	}
}
