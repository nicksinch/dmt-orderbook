package store

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"

	"dmt-orderbook/internal/engine"
	"dmt-orderbook/internal/feed"
)

const indexEntrySize = 16

// errStop ends a walk early without being an error.
var errStop = errors.New("stop")

// Reader answers from the store as it is now. It keeps only the directory:
// every call opens the files, so it also works while ingest is writing.
type Reader struct{ dir string }

// Open fails only if dir does not exist. A missing or empty log or index is an
// empty store.
func Open(dir string) (*Reader, error) {
	st, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("store: %s is not a directory", dir)
	}
	return &Reader{dir: dir}, nil
}

// openFile opens dir/name; a missing file gives nil, nil.
func (r *Reader) openFile(name string) (*os.File, error) {
	f, err := os.Open(filepath.Join(r.dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return f, err
}

// openBoth opens log and index; either may come back nil. The caller closes the non-nil ones.
func (r *Reader) openBoth() (log, idx *os.File, err error) {
	if log, err = r.openFile("log"); err != nil {
		return nil, nil, err
	}
	if idx, err = r.openFile("index"); err != nil {
		if log != nil {
			log.Close()
		}
		return nil, nil, err
	}
	return log, idx, nil
}

func closeAll(fs ...*os.File) {
	for _, f := range fs {
		if f != nil {
			f.Close()
		}
	}
}

// walk calls fn for every valid block from off, in order, and stops at the
// first invalid one (the end of the store). An error from fn is returned as is.
func walk(log io.ReaderAt, off int64, fn func(off int64, h Header, payload []byte) error) error {
	for {
		h, payload, next, ok, err := ReadBlock(log, off)
		if err != nil || !ok {
			return err
		}
		if err := fn(off, h, payload); err != nil {
			return err
		}
		off = next
	}
}

// ForEachBlock calls fn for every valid block from the start of the log, in
// order, then stops at the first invalid one.
func (r *Reader) ForEachBlock(fn func(off int64, h Header, payload []byte) error) error {
	log, err := r.openFile("log")
	if err != nil || log == nil {
		return err
	}
	defer log.Close()
	return walk(log, 0, fn)
}

// checkpointRef is a valid checkpoint block found through the index.
type checkpointRef struct {
	off, next int64
	h         Header
	payload   []byte
}

// findCheckpoint returns the last indexed checkpoint with ts <= t whose block
// is a valid checkpoint. Entries that fail are skipped for the one before; ok
// is false when none works. Only whole index entries are used.
func findCheckpoint(idx, log *os.File, t int64) (ref checkpointRef, ok bool, err error) {
	if idx == nil || log == nil {
		return ref, false, nil
	}
	st, err := idx.Stat()
	if err != nil {
		return ref, false, err
	}
	var ent [indexEntrySize]byte
	read := func(i int) (ts, off int64, err error) {
		if _, err := idx.ReadAt(ent[:], int64(i)*indexEntrySize); err != nil {
			return 0, 0, err
		}
		return int64(binary.LittleEndian.Uint64(ent[0:])), int64(binary.LittleEndian.Uint64(ent[8:])), nil
	}
	n := int(st.Size() / indexEntrySize)
	var searchErr error
	i := sort.Search(n, func(i int) bool { // first entry with ts > t
		ts, _, err := read(i)
		if err != nil {
			searchErr = err
		}
		return ts > t
	})
	if searchErr != nil {
		return ref, false, searchErr
	}
	for j := i - 1; j >= 0; j-- {
		_, off, err := read(j)
		if err != nil {
			return ref, false, err
		}
		h, payload, next, valid, err := ReadBlock(log, off)
		if err != nil {
			return ref, false, err
		}
		if valid && h.Kind == KindCheckpoint {
			return checkpointRef{off, next, h, payload}, true, nil
		}
	}
	return ref, false, nil
}

// Status is the last localview line number in the valid blocks; 0 for an empty store.
func (r *Reader) Status() (int64, error) {
	log, idx, err := r.openBoth()
	if err != nil || log == nil {
		return 0, err
	}
	defer closeAll(log, idx)
	ref, ok, err := findCheckpoint(idx, log, math.MaxInt64)
	if err != nil {
		return 0, err
	}
	var off, last int64
	if ok {
		off = ref.off
	}
	err = walk(log, off, func(_ int64, h Header, _ []byte) error {
		last = h.LastLVLine
		return nil
	})
	return last, err
}

// StateAt returns a fresh engine holding the state after every stored record
// with time <= t.
func (r *Reader) StateAt(t int64) (*engine.Engine, error) {
	log, idx, err := r.openBoth()
	if err != nil {
		return nil, err
	}
	defer closeAll(log, idx)
	if log == nil {
		return engine.New(), nil
	}
	ref, ok, err := findCheckpoint(idx, log, t)
	if err != nil {
		return nil, err
	}
	e := engine.New()
	var ids []string
	var off int64
	if ok {
		if e, ids, err = restoreCheckpoint(ref); err != nil {
			return nil, err
		}
		off = ref.next
	}
	// Without a usable checkpoint this walks from offset 0 and replays every
	// change; the start checkpoint (and any other) is skipped.
	err = walk(log, off, func(off int64, h Header, payload []byte) error {
		if h.Kind != KindChanges {
			return nil
		}
		recs, err := DecodeRecords(payload)
		if err != nil {
			return fmt.Errorf("store: block at %d: %w", off, err)
		}
		for _, rec := range recs {
			if rec.Define == nil && recTime(rec) > t {
				return errStop
			}
			if ids, err = applyRecord(e, ids, rec); err != nil {
				return fmt.Errorf("store: block at %d: %w", off, err)
			}
		}
		return nil
	})
	if err != nil && err != errStop {
		return nil, err
	}
	return e, nil
}

func recTime(rec Record) int64 {
	if rec.Contract != nil {
		return rec.Contract.T
	}
	return rec.Line.T
}

func restoreCheckpoint(ref checkpointRef) (*engine.Engine, []string, error) {
	cp, err := DecodeCheckpoint(ref.payload)
	if err != nil {
		return nil, nil, fmt.Errorf("store: checkpoint at %d: %w", ref.off, err)
	}
	id := func(pid uint32) (string, error) {
		if int(pid) >= len(cp.IDs) {
			return "", fmt.Errorf("store: checkpoint at %d: pid %d out of range", ref.off, pid)
		}
		return cp.IDs[pid], nil
	}
	s := engine.State{Now: ref.h.FirstTS}
	for _, c := range cp.Contracts {
		cid, err := id(c.PID)
		if err != nil {
			return nil, nil, err
		}
		s.Contracts = append(s.Contracts, engine.ContractEntry{ID: cid, T: c.T, Name: c.Name, State: c.State, Deleted: c.Deleted})
	}
	for _, b := range cp.Books {
		cid, err := id(b.PID)
		if err != nil {
			return nil, nil, err
		}
		s.Books = append(s.Books, engine.BookLevels{ID: cid, Bids: b.Bids, Asks: b.Asks})
	}
	return engine.Restore(s), cp.IDs, nil
}

// applyRecord applies one record to e. A DEFINE grows ids.
func applyRecord(e *engine.Engine, ids []string, rec Record) ([]string, error) {
	switch {
	case rec.Define != nil:
		if int(rec.Define.PID) != len(ids) {
			return ids, fmt.Errorf("define pid %d, want %d", rec.Define.PID, len(ids))
		}
		return append(ids, rec.Define.ContractID), nil
	case rec.Contract != nil:
		c := rec.Contract
		if int(c.PID) >= len(ids) {
			return ids, fmt.Errorf("pid %d out of range", c.PID)
		}
		e.ApplyContract(feed.Contract{Time: c.T, ContractID: ids[c.PID], Name: c.Name, State: c.State, Deleted: c.Deleted})
	default:
		l := rec.Line
		changes := make([]engine.ProductChange, len(l.Products))
		for i, p := range l.Products {
			if int(p.PID) >= len(ids) {
				return ids, fmt.Errorf("pid %d out of range", p.PID)
			}
			changes[i] = engine.ProductChange{ContractID: ids[p.PID], Bids: p.Bids, Asks: p.Asks}
		}
		e.ApplyChanges(l.T, changes)
	}
	return ids, nil
}
