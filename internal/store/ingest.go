package store

import (
	"io"

	"dmt-orderbook/internal/engine"
	"dmt-orderbook/internal/feed"
)

// IngestStats is what Ingest consumed and the size of the store it wrote.
type IngestStats struct {
	Localview, Contracts int
	LogBytes, IndexBytes int64
}

// Ingest runs merge, engine and writer until EOF, or until localview line
// stopAfter (0 = no limit) has been written. On a merger error it closes the
// writer, which flushes the lines before the bad one, and returns the error.
func Ingest(m *feed.Merger, dir string, o Options, stopAfter int) (IngestStats, error) {
	var st IngestStats
	e := engine.New()
	w, err := Create(dir, e, o)
	if err != nil {
		return st, err
	}
	var loopErr error
	for loopErr == nil {
		rec, err := m.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			loopErr = err
			break
		}
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
