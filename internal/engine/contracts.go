package engine

import (
	"slices"

	"dmt-orderbook/internal/feed"
)

// contractState is one entry in a contract's history: from time t on, the
// contract has this name and state (state "" means no delivery area 46).
type contractState struct {
	t           int64
	name, state string
	deleted     bool
}

// ApplyContract records a contract line. The history only grows when the name,
// state or deleted flag differs from the last entry. Lines arrive in time order.
// It reports whether the history grew.
func (e *Engine) ApplyContract(c feed.Contract) bool {
	e.now = c.Time
	h := e.contracts[c.ContractID]
	if n := len(h); n > 0 {
		last := h[n-1]
		if last.name == c.Name && last.state == c.State && last.deleted == c.Deleted {
			return false
		}
	}
	e.contracts[c.ContractID] = append(h, contractState{c.Time, c.Name, c.State, c.Deleted})
	if !slices.Contains(e.names[c.Name], c.ContractID) {
		e.names[c.Name] = append(e.names[c.Name], c.ContractID)
	}
	return true
}

// at returns the history entry in force at time t, if any.
func (e *Engine) at(id string, t int64) (contractState, bool) {
	h := e.contracts[id]
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].t <= t {
			return h[i], true
		}
	}
	return contractState{}, false
}

// inScope reports whether the contract is ACTI in area 46 (and not deleted) at t.
func (e *Engine) inScope(id string, t int64) bool {
	s, ok := e.at(id, t)
	return ok && s.state == "ACTI" && !s.deleted
}

// IDsForName returns the IDs carrying this name that are in scope at t, sorted.
// More than one means the name is ambiguous; the caller decides what to do.
func (e *Engine) IDsForName(name string, t int64) []string {
	var ids []string
	for _, id := range e.names[name] {
		if s, ok := e.at(id, t); ok && s.name == name && e.inScope(id, t) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}
