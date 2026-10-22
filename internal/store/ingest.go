package store

import (
	"errors"
	"io"
	"time"

	"dmt-orderbook/internal/engine"
	"dmt-orderbook/internal/feed"
)

// IngestStats is what Ingest consumed and the size of the store it wrote.
type IngestStats struct {
	Localview, Contracts int
	LogBytes, IndexBytes int64
}

// next is one result of Merger.Next, passed from the reader goroutine.
type next struct {
	rec feed.Record
	err error
}

// Ingest runs merge, engine and writer until EOF, or until localview line
// stopAfter (0 = no limit) has been written. On a merger error it closes the
// writer, which flushes the lines before the bad one, and returns the error.
//
// A reader goroutine calls m.Next, so the main loop can also wake on a ticker
// and flush a block whose line was followed by silence. Only the main loop
// touches the engine and the writer.
//
// Closing done on return only releases a reader blocked on the send. A reader
// inside m.Next keeps reading until that call returns (it may read ahead up to
// the buffer past stopAfter) and exits when the input ends or is closed. That is
// harmless in cmd/ingest, whose process exits; callers must not use m after
// Ingest returns.
func Ingest(m *feed.Merger, dir string, o Options, stopAfter int) (IngestStats, error) {
	var st IngestStats
	if o.BlockAge <= 0 { // time.NewTicker panics on it
		return st, errors.New("store: BlockAge must be > 0")
	}
	e := engine.New()
	w, err := Create(dir, e, o)
	if err != nil {
		return st, err
	}

	// Read-ahead lets JSON decoding overlap applying and writing; memory is
	// bounded by 1024 decoded lines.
	results := make(chan next, 1024)
	done := make(chan struct{})
	defer close(done) // releases a reader blocked on the send; see above
	go func() {
		for {
			rec, err := m.Next()
			select {
			case results <- next{rec, err}:
			case <-done:
				return
			}
			if err != nil { // io.EOF included: nothing follows
				return
			}
		}
	}()

	// A fixed ticker at BlockAge rather than a timer armed per block: one wake
	// every BlockAge (2 ms by default) costs nothing measurable and is simpler. A block is written
	// at most about 2 × BlockAge after its first line.
	tick := time.NewTicker(o.BlockAge)
	defer tick.Stop()

	var loopErr error
	for loopErr == nil {
		var r next
		select {
		case r = <-results:
		case <-tick.C:
			loopErr = w.FlushIfDue()
			continue
		}
		if r.err == io.EOF {
			break
		}
		if r.err != nil {
			loopErr = r.err
			break
		}
		rec := r.rec
		if rec.Source == feed.SourceLocalview {
			changes := e.ApplyLocalview(*rec.Localview)
			loopErr = w.Localview(rec.Line, *rec.Localview, changes)
			st.Localview++
			if loopErr == nil && stopAfter > 0 && rec.Line == stopAfter {
				break
			}
		} else {
			changed := e.ApplyContract(*rec.Contract)
			loopErr = w.Contract(*rec.Contract, changed)
			st.Contracts++
		}
	}
	cerr := w.Close()
	st.LogBytes, st.IndexBytes = w.Sizes()
	if loopErr != nil {
		return st, loopErr
	}
	return st, cerr
}
