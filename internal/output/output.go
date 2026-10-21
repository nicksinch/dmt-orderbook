// Package output holds the JSON shapes of the query answers, shared by the
// bookat tool and the query command.
package output

import (
	"encoding/json"
	"io"

	"dmt-orderbook/internal/engine"
)

// NewEncoder returns a compact JSON encoder that does not escape <, > and &.
func NewEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc
}

type bookJSON struct {
	Bid [][2]int64 `json:"bid"`
	Ask [][2]int64 `json:"ask"`
}

type topJSON struct {
	Bid *[2]int64 `json:"bid"`
	Ask *[2]int64 `json:"ask"`
}

// Book returns the book answer for one product at t: {"<name>": {bid, ask}},
// or {"<name>": null} if the product is not in scope at t. The error is the
// engine's, e.g. when the name is ambiguous.
func Book(e *engine.Engine, name string, t int64) (any, error) {
	pb, ok, err := e.BookByName(name, t)
	if err != nil {
		return nil, err
	}
	if !ok {
		return map[string]*bookJSON{name: nil}, nil
	}
	return map[string]*bookJSON{name: {Bid: pairs(pb.Bids), Ask: pairs(pb.Asks)}}, nil
}

// pairs converts levels to [price, qty] pairs; never nil, so it prints as [].
func pairs(ls []engine.Level) [][2]int64 {
	out := make([][2]int64, len(ls))
	for i, l := range ls {
		out[i] = [2]int64{l.Price, l.Qty}
	}
	return out
}

// Top returns the top answer at t: {"<name>": {bid, ask}} for every product in
// scope, with null for an empty side; {} when nothing is in scope.
func Top(e *engine.Engine, t int64) (any, error) {
	entries, err := e.Top(t)
	if err != nil {
		return nil, err
	}
	out := make(map[string]topJSON)
	for _, te := range entries {
		var tj topJSON
		if te.Bid != nil {
			tj.Bid = &[2]int64{te.Bid.Price, te.Bid.Qty}
		}
		if te.Ask != nil {
			tj.Ask = &[2]int64{te.Ask.Price, te.Ask.Qty}
		}
		out[te.Name] = tj
	}
	return out, nil
}

// ReplayLine is one replay output line: the levels of one product that changed
// (or its full book), t as printed.
type ReplayLine struct {
	T       string     `json:"t"`
	Product string     `json:"product"`
	Bid     [][2]int64 `json:"bid"`
	Ask     [][2]int64 `json:"ask"`
}

// NewReplayLine builds a replay line; an empty side prints as [].
func NewReplayLine(t, product string, bids, asks []engine.Level) ReplayLine {
	return ReplayLine{T: t, Product: product, Bid: pairs(bids), Ask: pairs(asks)}
}
