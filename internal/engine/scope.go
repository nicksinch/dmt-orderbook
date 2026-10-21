package engine

import (
	"cmp"
	"slices"
)

// NameInScope returns the contract's name at t. ok is true only if the contract
// is in scope at t (the same rule as inScope).
func (e *Engine) NameInScope(id string, t int64) (name string, ok bool) {
	if !e.inScope(id, t) {
		return "", false
	}
	s, _ := e.at(id, t)
	return s.name, true
}

// IDsInScope returns every contract in scope at t, sorted by name, then by ID.
func (e *Engine) IDsInScope(t int64) []string {
	var ids []string
	for id := range e.contracts {
		if e.inScope(id, t) {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, func(a, b string) int {
		na, _ := e.NameInScope(a, t)
		nb, _ := e.NameInScope(b, t)
		if c := cmp.Compare(na, nb); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	return ids
}

// Levels returns copies of the current book of one contract, best first.
// It takes no time argument: callers must have applied the records up to the
// time they mean.
// Both are non-nil: an empty side or an unknown ID gives an empty slice.
func (e *Engine) Levels(id string) (bids, asks []Level) {
	bids, asks = []Level{}, []Level{}
	if b := e.books[id]; b != nil {
		bids = append(bids, b.bids...)
		asks = append(asks, b.asks...)
	}
	return bids, asks
}
