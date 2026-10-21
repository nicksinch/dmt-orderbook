package main

import (
	"bufio"
	"io"
	"sort"
	"time"

	"dmt-orderbook/internal/engine"
	"dmt-orderbook/internal/output"
	"dmt-orderbook/internal/store"
)

const pollInterval = time.Millisecond

// runReplay prints the full book of every product in scope at from (t = fromRaw
// as typed), then one line per changed product for each later stored record.
// With follow it keeps going and only returns on an error. Output is flushed
// before every follow wait and on return.
func runReplay(r *store.Reader, fromRaw string, from, to int64, follow bool, w io.Writer) error {
	bw := bufio.NewWriter(w)
	enc := output.NewEncoder(bw)

	start := func(e *engine.Engine) error {
		for _, id := range e.IDsInScope(from) {
			name, _ := e.NameInScope(id, from)
			bids, asks := e.Levels(id)
			if err := enc.Encode(output.NewReplayLine(fromRaw, name, bids, asks)); err != nil {
				return err
			}
		}
		return nil
	}

	step := func(e *engine.Engine, ev store.Event) error {
		if ev.Entered != "" {
			name, _ := e.NameInScope(ev.Entered, ev.T)
			bids, asks := e.Levels(ev.Entered)
			return enc.Encode(output.NewReplayLine(ev.TimeRaw, name, bids, asks))
		}
		var out []output.ReplayLine
		for _, c := range ev.Changes {
			if name, ok := e.NameInScope(c.ContractID, ev.T); ok {
				out = append(out, output.NewReplayLine(ev.TimeRaw, name, c.Bids, c.Asks))
			}
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Product < out[j].Product })
		for _, l := range out {
			if err := enc.Encode(l); err != nil {
				return err
			}
		}
		return nil
	}

	err := r.Replay(store.ReplayOptions{
		From: from, To: to, Follow: follow, Poll: pollInterval,
		Idle: bw.Flush,
	}, start, step)
	if ferr := bw.Flush(); err == nil {
		err = ferr
	}
	return err
}
