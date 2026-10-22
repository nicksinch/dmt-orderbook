package engine

import (
	"cmp"
	"fmt"
	"slices"

	"dmt-orderbook/internal/feed"
)

// Engine holds the books and the contract history. ApplyLocalview feeds the
// books; BookByName and Top answer queries from them.
type Engine struct {
	books     map[string]*Book           // by contractId
	contracts map[string][]contractState // by contractId, in time order
	names     map[string][]string        // name -> every contractId ever seen with it
	now       int64                      // time of the latest record applied

	// Scratch space for ApplyLocalview, reused (cleared) for every line.
	before map[levelKey]int64 // level total before the line first touched it
	order  []string           // contract IDs of the line, by first appearance
}

// levelKey names one price level of one contract.
type levelKey struct {
	id    string
	buy   bool
	price int64
}

// New returns an empty engine.
func New() *Engine {
	return &Engine{
		books:     make(map[string]*Book),
		contracts: make(map[string][]contractState),
		names:     make(map[string][]string),
		before:    make(map[levelKey]int64),
	}
}

// ProductChange lists the levels of one contract that a line changed.
type ProductChange struct {
	ContractID string
	Bids, Asks []Level // changed levels with new totals; Qty 0 = gone; best first; never both empty
}

// ApplyLocalview applies one localview line and returns what it changed.
// A snapshot replaces every book; a delta changes only the listed orders.
func (e *Engine) ApplyLocalview(lv feed.Localview) []ProductChange {
	e.now = lv.Time
	if lv.Snapshot {
		return e.applySnapshot(lv)
	}
	return e.applyDelta(lv)
}

// applyDelta applies the orders to the existing books. For each level it
// remembers the total before the line first touched it, then compares with the
// total after, so a level that ends where it started is not reported.
func (e *Engine) applyDelta(lv feed.Localview) []ProductChange {
	clear(e.before) // e.before is cleared and reused rather than replaced with a new map so that a map isn't allocated for each of the ~1M lines. The behaviour is the same either way.
	e.order = e.order[:0]
	for _, co := range lv.Body {
		b := e.books[co.ContractID]
		if b == nil {
			b = newBook()
			e.books[co.ContractID] = b
		}
		if !slices.Contains(e.order, co.ContractID) {
			e.order = append(e.order, co.ContractID)
		}
		touch := func(buy bool, price, before int64) {
			k := levelKey{co.ContractID, buy, price}
			if _, seen := e.before[k]; !seen {
				e.before[k] = before
			}
		}
		for _, o := range co.BuyOrders {
			b.applyOrder(o, true, touch)
		}
		for _, o := range co.SellOrders {
			b.applyOrder(o, false, touch)
		}
	}

	byID := make(map[string]*ProductChange, len(e.order))
	for k, before := range e.before {
		now := e.books[k.id].levelQty(k.buy, k.price)
		if now == before {
			continue
		}
		pc := byID[k.id] // start reporting the levels that differ after this line's changes
		if pc == nil {
			pc = &ProductChange{ContractID: k.id}
			byID[k.id] = pc
		}
		if k.buy {
			pc.Bids = append(pc.Bids, Level{k.price, now})
		} else {
			pc.Asks = append(pc.Asks, Level{k.price, now})
		}
	}
	var out []ProductChange
	for _, id := range e.order {
		pc := byID[id]
		if pc == nil {
			continue
		}
		slices.SortFunc(pc.Bids, func(a, b Level) int { return cmp.Compare(b.Price, a.Price) })
		slices.SortFunc(pc.Asks, func(a, b Level) int { return cmp.Compare(a.Price, b.Price) })
		out = append(out, *pc)
	}
	return out
}

// applySnapshot builds fresh books from the line and swaps them in. A contract
// the snapshot does not list ends up empty.
func (e *Engine) applySnapshot(lv feed.Localview) []ProductChange {
	newBooks := make(map[string]*Book)
	var listed []string // by first appearance
	for _, co := range lv.Body {
		b := newBooks[co.ContractID]
		if b == nil {
			b = newBook()
			newBooks[co.ContractID] = b
			listed = append(listed, co.ContractID)
		}
		for _, o := range co.BuyOrders {
			b.applyOrder(o, true, nil)
		}
		for _, o := range co.SellOrders {
			b.applyOrder(o, false, nil)
		}
	}

	var out []ProductChange
	empty := newBook()
	report := func(id string, old, new *Book) {
		bids := diffLevels(old.bids, new.bids, true)
		asks := diffLevels(old.asks, new.asks, false)
		if len(bids) > 0 || len(asks) > 0 {
			out = append(out, ProductChange{ContractID: id, Bids: bids, Asks: asks})
		}
	}
	for _, id := range listed {
		old := e.books[id]
		if old == nil {
			old = empty
		}
		report(id, old, newBooks[id])
	}
	var dropped []string
	for id := range e.books {
		if _, ok := newBooks[id]; !ok {
			dropped = append(dropped, id)
		}
	}
	slices.Sort(dropped)
	for _, id := range dropped {
		report(id, e.books[id], empty)
	}
	e.books = newBooks
	return out
}

// ProductBook is the full aggregated book of one product.
type ProductBook struct {
	Name       string
	Bids, Asks []Level // non-nil (empty slice when a side is empty)
}

// BookByName returns the book of the product with this name as of time t.
// ok is false if no ID with that name is in scope at t; err is set if more than one is.
// t must be at or after the last applied record (the books are only the current
// ones): apply every record with time <= t first, then ask. Otherwise err is set.
func (e *Engine) BookByName(name string, t int64) (pb ProductBook, ok bool, err error) {
	if t < e.now {
		return ProductBook{}, false, fmt.Errorf("time %d is before the last applied record (%d)", t, e.now)
	}
	ids := e.IDsForName(name, t)
	switch len(ids) {
	case 0:
		return ProductBook{}, false, nil
	case 1:
	default:
		return ProductBook{}, false, fmt.Errorf("name %q is ambiguous at that time: %v", name, ids)
	}
	pb = ProductBook{Name: name, Bids: []Level{}, Asks: []Level{}}
	if b := e.books[ids[0]]; b != nil {
		pb.Bids = append(pb.Bids, b.bids...)
		pb.Asks = append(pb.Asks, b.asks...)
	}
	return pb, true, nil
}

// TopEntry is the best bid and ask of one product.
type TopEntry struct {
	Name     string
	Bid, Ask *Level // nil = empty side
}

// Top returns the best bid and ask of every in-scope contract at t, sorted by name.
// t must be at or after the last applied record, as for BookByName. err is set
// if two in-scope contracts share a name.
func (e *Engine) Top(t int64) ([]TopEntry, error) {
	if t < e.now {
		return nil, fmt.Errorf("time %d is before the last applied record (%d)", t, e.now)
	}
	var out []TopEntry
	for id := range e.contracts {
		if !e.inScope(id, t) {
			continue
		}
		s, _ := e.at(id, t)
		entry := TopEntry{Name: s.name}
		if b := e.books[id]; b != nil {
			if len(b.bids) > 0 {
				best := b.bids[0]
				entry.Bid = &best
			}
			if len(b.asks) > 0 {
				best := b.asks[0]
				entry.Ask = &best
			}
		}
		out = append(out, entry)
	}
	slices.SortFunc(out, func(a, b TopEntry) int { return cmp.Compare(a.Name, b.Name) })
	for i := 1; i < len(out); i++ {
		if out[i].Name == out[i-1].Name {
			return nil, fmt.Errorf("name %q is ambiguous at that time", out[i].Name)
		}
	}
	return out, nil
}
