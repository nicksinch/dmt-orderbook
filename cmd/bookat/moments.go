package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"dmt-orderbook/internal/engine"
	"dmt-orderbook/internal/feed"
	"dmt-orderbook/internal/output"
	"dmt-orderbook/internal/store"
)

// moment is one question: the book of one product, or the top of all, at time T.
type moment struct {
	Kind string // "book" or "top"
	Name string // book only
	T    int64
}

// parseMoments reads "book <name> <T>" and "top <T>" lines, skipping blank ones.
// T must not go backwards from one moment to the next.
func parseMoments(r io.Reader) ([]moment, error) {
	var ms []moment
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		var m moment
		var ts string
		switch {
		case f[0] == "book" && len(f) == 3:
			m.Kind, m.Name, ts = "book", f[1], f[2]
		case f[0] == "top" && len(f) == 2:
			m.Kind, ts = "top", f[1]
		default:
			return nil, fmt.Errorf("moments line %d: want \"book <name> <T>\" or \"top <T>\", got %q", n, sc.Text())
		}
		t, err := feed.ParseTime(ts)
		if err != nil {
			return nil, fmt.Errorf("moments line %d: %w", n, err)
		}
		m.T = t
		if len(ms) > 0 && t < ms[len(ms)-1].T {
			return nil, fmt.Errorf("moments line %d: time goes backwards", n)
		}
		ms = append(ms, m)
	}
	return ms, sc.Err()
}

// stats counts the records applied.
type stats struct{ Localview, Contracts int }

// runMoments drives the merge and writes one JSON line per moment, in input order.
// It reads one record ahead and applies it only once its time is <= the moment's T.
// It stops after the last moment; at end of input the rest are answered from the final state.
func runMoments(m *feed.Merger, ms []moment, w io.Writer) (stats, error) {
	var st stats
	e := engine.New()
	enc := output.NewEncoder(w)

	var pending *feed.Record
	eof := false
	for _, mo := range ms {
		for {
			if pending == nil && !eof {
				rec, err := m.Next()
				if err == io.EOF {
					eof = true
				} else if err != nil {
					return st, err
				} else {
					pending = &rec
				}
			}
			if pending == nil || pending.Time() > mo.T {
				break
			}
			if pending.Source == feed.SourceLocalview {
				e.ApplyLocalview(*pending.Localview)
				st.Localview++
			} else {
				e.ApplyContract(*pending.Contract)
				st.Contracts++
			}
			pending = nil
		}
		if err := answer(enc, e, mo); err != nil {
			return st, err
		}
	}
	return st, nil
}

// answer encodes the book or top answer for one moment from the engine state at mo.T.
func answer(enc *json.Encoder, e *engine.Engine, mo moment) error {
	var out any
	var err error
	if mo.Kind == "book" {
		out, err = output.Book(e, mo.Name, mo.T)
	} else {
		out, err = output.Top(e, mo.T)
	}
	if err != nil {
		return err
	}
	return enc.Encode(out)
}

// runMomentsStore answers each moment from the store with a fresh StateAt,
// in the same JSON format as runMoments.
func runMomentsStore(r *store.Reader, ms []moment, w io.Writer) error {
	enc := output.NewEncoder(w)
	for _, mo := range ms {
		e, err := r.StateAt(mo.T)
		if err != nil {
			return err
		}
		if err := answer(enc, e, mo); err != nil {
			return err
		}
	}
	return nil
}
