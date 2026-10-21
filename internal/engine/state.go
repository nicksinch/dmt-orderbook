package engine

import (
	"maps"
	"slices"
)

// ApplyChanges sets each listed level to its new total (0 removes it; a new
// price is inserted in order) and sets now = t. Used by store readers; such
// books have levels but no orders.
func (e *Engine) ApplyChanges(t int64, changes []ProductChange) {
	e.now = t
	for _, pc := range changes {
		b := e.books[pc.ContractID]
		if b == nil {
			b = newBook()
			e.books[pc.ContractID] = b
		}
		for _, l := range pc.Bids {
			b.setLevel(true, l.Price, l.Qty)
		}
		for _, l := range pc.Asks {
			b.setLevel(false, l.Price, l.Qty)
		}
	}
}

// ContractEntry is the current history entry of one contract.
type ContractEntry struct {
	ID          string
	T           int64 // when this history entry started
	Name, State string
	Deleted     bool
}

// BookLevels is the aggregated book of one contract.
type BookLevels struct {
	ID         string
	Bids, Asks []Level // best first
}

// State is what a checkpoint holds: enough to answer queries from now on.
type State struct {
	Now       int64
	Contracts []ContractEntry // the current (last) history entry of every contract, sorted by ID
	Books     []BookLevels    // every book with at least one level, sorted by ID
}

// State returns a copy of the engine's current state.
func (e *Engine) State() State {
	s := State{Now: e.now}
	for _, id := range slices.Sorted(maps.Keys(e.contracts)) {
		h := e.contracts[id]
		last := h[len(h)-1]
		s.Contracts = append(s.Contracts, ContractEntry{id, last.t, last.name, last.state, last.deleted})
	}
	for _, id := range slices.Sorted(maps.Keys(e.books)) {
		b := e.books[id]
		if len(b.bids) > 0 || len(b.asks) > 0 {
			s.Books = append(s.Books, BookLevels{id, copyLevels(b.bids), copyLevels(b.asks)})
		}
	}
	return s
}

// Restore returns an engine whose contracts each have the one given entry, whose
// names map is rebuilt from them, whose books hold the given levels (no orders),
// and whose now is s.Now.
func Restore(s State) *Engine {
	e := New()
	e.now = s.Now
	for _, c := range s.Contracts {
		e.contracts[c.ID] = []contractState{{c.T, c.Name, c.State, c.Deleted}}
		e.names[c.Name] = append(e.names[c.Name], c.ID)
	}
	for _, bl := range s.Books {
		b := newBook()
		b.bids = copyLevels(bl.Bids)
		b.asks = copyLevels(bl.Asks)
		e.books[bl.ID] = b
	}
	return e
}

// copyLevels copies a side; an empty side is always nil, so states compare equal
// however the side became empty.
func copyLevels(l []Level) []Level {
	if len(l) == 0 {
		return nil
	}
	return slices.Clone(l)
}
