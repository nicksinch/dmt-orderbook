// Package engine holds the in-memory order book and the engine that feeds it.
package engine

import (
	"cmp"
	"slices"

	"dmt-orderbook/internal/feed"
)

// Level is one price level: the total quantity of all orders at a price.
type Level struct{ Price, Qty int64 }

// order is what the book remembers about one open order.
type order struct {
	buy        bool
	price, qty int64
}

// Book is the order book of one contract. Levels with total 0 are never stored.
type Book struct {
	orders map[string]order // by orderId
	bids   []Level          // descending price, best first
	asks   []Level          // ascending price, best first
}

func newBook() *Book {
	return &Book{orders: make(map[string]order)}
}

// side returns the level slice for the buy or sell side.
func (b *Book) side(buy bool) *[]Level {
	if buy {
		return &b.bids
	}
	return &b.asks
}

// find locates price in one side. Bids are sorted descending, asks ascending,
// so the comparison is flipped for bids. It returns the index and whether the
// level exists (otherwise the index is where it would be inserted).
func find(levels []Level, buy bool, price int64) (int, bool) {
	return slices.BinarySearchFunc(levels, price, func(l Level, price int64) int {
		if buy {
			return cmp.Compare(price, l.Price)
		}
		return cmp.Compare(l.Price, price)
	})
}

// addQty changes the total at a price by delta, creating or removing the level
// as needed. Before changing anything it reports the old total to touch.
func (b *Book) addQty(buy bool, price, delta int64, touch func(buy bool, price, before int64)) {
	if delta == 0 {
		return // a qty-0 order changes nothing
	}
	levels := b.side(buy)
	i, found := find(*levels, buy, price)
	var before int64
	if found {
		before = (*levels)[i].Qty
	}
	if touch != nil {
		touch(buy, price, before)
	}
	switch {
	case !found:
		*levels = slices.Insert(*levels, i, Level{price, delta})
	case before+delta == 0:
		*levels = slices.Delete(*levels, i, i+1)
	default:
		(*levels)[i].Qty += delta
	}
}

// applyOrder applies one order entry from the buy (buy=true) or sell side.
// Before changing any level it calls touch(buy, price, qtyBefore). touch may be nil.
func (b *Book) applyOrder(o feed.Order, buy bool, touch func(buy bool, price, before int64)) {
	if old, ok := b.orders[o.OrderID]; ok {
		b.addQty(old.buy, old.price, -old.qty, touch)
		delete(b.orders, o.OrderID)
	}
	if o.Deleted {
		return
	}
	b.orders[o.OrderID] = order{buy: buy, price: *o.Price, qty: *o.Qty}
	b.addQty(buy, *o.Price, *o.Qty, touch)
}

// levelQty returns the total at a price, 0 if there is no such level.
func (b *Book) levelQty(buy bool, price int64) int64 {
	levels := *b.side(buy)
	if i, found := find(levels, buy, price); found {
		return levels[i].Qty
	}
	return 0
}

// setLevel sets the total at a price to qty: it inserts the level, overwrites
// it, or removes it when qty is 0. Removing a missing level does nothing.
func (b *Book) setLevel(buy bool, price, qty int64) {
	levels := b.side(buy)
	i, found := find(*levels, buy, price)
	switch {
	case qty == 0 && found:
		*levels = slices.Delete(*levels, i, i+1)
	case qty == 0:
	case found:
		(*levels)[i].Qty = qty
	default:
		*levels = slices.Insert(*levels, i, Level{price, qty})
	}
}

// Bids returns a copy of the bid levels, best (highest) first.
func (b *Book) Bids() []Level { return slices.Clone(b.bids) }

// Asks returns a copy of the ask levels, best (lowest) first.
func (b *Book) Asks() []Level { return slices.Clone(b.asks) }

// diffLevels returns levels whose qty differs between old and new (both sorted
// the same way), with the new qty (0 if gone), in that same order. desc=true for bids.
// It is one merge pass over the two sorted slices.
func diffLevels(old, new []Level, desc bool) []Level {
	var out []Level
	i, j := 0, 0
	for i < len(old) || j < len(new) {
		switch {
		case j == len(new) || (i < len(old) && comesBefore(old[i].Price, new[j].Price, desc)):
			out = append(out, Level{old[i].Price, 0}) // only in old: gone
			i++
		case i == len(old) || comesBefore(new[j].Price, old[i].Price, desc):
			out = append(out, new[j]) // only in new: appeared
			j++
		default: // same price on both sides
			if old[i].Qty != new[j].Qty {
				out = append(out, new[j])
			}
			i++
			j++
		}
	}
	return out
}

// comesBefore reports whether price a sorts before price b.
func comesBefore(a, b int64, desc bool) bool {
	if desc {
		return a > b
	}
	return a < b
}
