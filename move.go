package orm

import (
	"context"
	"encoding/json"
	"fmt"
)

// Move copies entities from one store to another.
//
// This is what makes the backends a choice rather than a commitment: a service
// starts on SQLite because it needs no server, moves to Hanzo SQL when it
// outgrows one file, and takes a copy into Hanzo Datastore when the questions
// turn analytical. DocDB is document semantics over the same SQL rows, so a move
// there is the same operation.
//
// It is written against the DB interface and nothing else, so it works for any
// pair — including a pair that did not exist when this was written. There is no
// per-backend branch here, and there must not be one: the moment a move knows
// which store it is talking to, the interface has stopped being the contract.
//
// Entities cross as their stored bytes. Decoding into a concrete type would
// force this to know every caller's types, and would silently drop any field the
// destination's Go struct has since removed — a migration that quietly discards
// data is worse than one that refuses.
//
// It is IDEMPOTENT: Put is an unconditional upsert, so a move that fails partway
// is re-run rather than unpicked, and a move onto a populated store overwrites by
// key rather than duplicating. It is deliberately NOT a delete: the source is
// left intact, so a bad move is recovered by pointing back at it.
//
// With no kinds named it moves everything registered. Naming them moves only
// those, which is how a large store is moved in pieces.
func Move(ctx context.Context, src, dst DB, kinds ...string) (Moved, error) {
	if src == nil || dst == nil {
		return Moved{}, fmt.Errorf("orm: Move needs both a source and a destination")
	}
	if len(kinds) == 0 {
		kinds = Kinds()
	}
	moved := Moved{ByKind: map[string]int{}}
	for _, kind := range kinds {
		var raw []json.RawMessage
		keys, err := src.Query(kind).GetAll(ctx, &raw)
		if err != nil {
			return moved, fmt.Errorf("orm: Move: reading %q: %w", kind, err)
		}
		if len(keys) != len(raw) {
			return moved, fmt.Errorf("orm: Move: %q returned %d keys for %d entities", kind, len(keys), len(raw))
		}
		for i, k := range keys {
			// The key is rebuilt on the DESTINATION, because a Key carries the
			// store that made it; handing a source key to another store is how a
			// move silently writes nowhere.
			if _, err := dst.Put(ctx, dst.NewKey(kind, k.StringID(), k.IntID(), nil), &raw[i]); err != nil {
				return moved, fmt.Errorf("orm: Move: writing %s/%s: %w", kind, k.StringID(), err)
			}
			moved.ByKind[kind]++
			moved.Total++
		}
	}
	return moved, nil
}

// Moved reports what a Move carried, per kind, so a caller can compare it with
// what it expected rather than trust that the call returned nil.
type Moved struct {
	ByKind map[string]int
	Total  int
}
