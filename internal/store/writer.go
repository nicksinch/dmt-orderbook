package store

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"

	"dmt-orderbook/internal/engine"
	"dmt-orderbook/internal/feed"
)

// Options tune the writer. Zero fields are not defaulted; start from DefaultOptions.
type Options struct {
	CheckpointBytes int64            // change bytes between checkpoints
	BlockBytes      int              // flush a block at this much pending payload
	BlockAge        time.Duration    // flush a block this long after its first line; must be > 0
	Now             func() time.Time // wall clock, replaceable in tests
}

func DefaultOptions() Options {
	return Options{CheckpointBytes: 1 << 20, BlockBytes: 64 << 10, BlockAge: 2 * time.Millisecond, Now: time.Now}
}

// Writer appends blocks to dir/log and checkpoint entries to dir/index.
// It is not safe for concurrent use.
type Writer struct {
	log, idx *os.File
	e        *engine.Engine
	o        Options
	logOff   int64 // bytes written to log so far
	idxOff   int64

	ids  []string // pid = index
	pids map[string]uint32

	pending  []byte // records of the block being built
	lines    int    // input lines consumed since the last block
	firstTS  int64  // time of the block's first line
	firstNow time.Time
	lastLine int64 // last localview line consumed
	sinceCP  int64 // change payload bytes written since the last checkpoint
	buf      []byte
	err      error // sticky write error
}

// Create makes dir (failing if it exists), opens log and index, and writes the
// empty start checkpoint. e is the engine whose state checkpoints are taken from.
func Create(dir string, e *engine.Engine, o Options) (*Writer, error) {
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL | os.O_APPEND
	log, err := os.OpenFile(filepath.Join(dir, "log"), flags, 0o644)
	if err != nil {
		return nil, err
	}
	idx, err := os.OpenFile(filepath.Join(dir, "index"), flags, 0o644)
	if err != nil {
		log.Close()
		return nil, err
	}
	w := &Writer{log: log, idx: idx, e: e, o: o, pids: map[string]uint32{}}
	if err := w.writeCheckpoint(Checkpoint{}, math.MinInt64); err != nil {
		w.closeFiles()
		return nil, err
	}
	return w, nil
}

// Contract records one consumed contracts line; changed is ApplyContract's result.
func (w *Writer) Contract(c feed.Contract, changed bool) error {
	if w.err != nil {
		return w.err
	}
	w.begin(c.Time)
	if changed {
		pid := w.pid(c.ContractID)
		w.pending = AppendRecord(w.pending, Record{Contract: &ContractRec{
			T: c.Time, PID: pid, Deleted: c.Deleted, Name: c.Name, State: c.State,
		}})
	}
	return w.endLine()
}

// Localview records one consumed localview line (its 1-based line number) and its changes.
func (w *Writer) Localview(line int, lv feed.Localview, changes []engine.ProductChange) error {
	if w.err != nil {
		return w.err
	}
	w.begin(lv.Time)
	w.lastLine = int64(line)
	if len(changes) > 0 {
		rec := &LineRec{T: lv.Time, Snapshot: lv.Snapshot}
		if lv.TimeRaw != CanonicalTime(lv.Time) {
			rec.Raw = lv.TimeRaw
		}
		// pid first: it may append DEFINE records, which must precede the LINE.
		for _, pc := range changes {
			rec.Products = append(rec.Products, ProductRec{PID: w.pid(pc.ContractID), Bids: pc.Bids, Asks: pc.Asks})
		}
		w.pending = AppendRecord(w.pending, Record{Line: rec})
	}
	return w.endLine()
}

// Close flushes the pending block, if any line was consumed since the last
// block, and closes both files.
func (w *Writer) Close() error {
	err := w.err
	if err == nil && w.lines > 0 {
		err = w.flush()
	}
	if cerr := w.closeFiles(); err == nil {
		err = cerr
	}
	return err
}

func (w *Writer) closeFiles() error {
	err := w.log.Close()
	if e := w.idx.Close(); err == nil {
		err = e
	}
	return err
}

func (w *Writer) Sizes() (logBytes, indexBytes int64) { return w.logOff, w.idxOff }

// begin marks the start of one consumed input line.
func (w *Writer) begin(t int64) {
	if w.lines == 0 {
		w.firstTS = t
		w.firstNow = w.o.Now()
	}
	w.lines++
}

// pid returns the contract's pid, assigning one and appending its DEFINE if new.
func (w *Writer) pid(id string) uint32 {
	if p, ok := w.pids[id]; ok {
		return p
	}
	p := uint32(len(w.ids))
	w.ids = append(w.ids, id)
	w.pids[id] = p
	w.pending = AppendRecord(w.pending, Record{Define: &Define{PID: p, ContractID: id}})
	return p
}

// endLine applies the flush and checkpoint rules after each consumed line.
func (w *Writer) endLine() error {
	if len(w.pending) >= w.o.BlockBytes || w.o.Now().Sub(w.firstNow) >= w.o.BlockAge {
		if err := w.flush(); err != nil {
			return err
		}
	}
	if w.sinceCP+int64(len(w.pending)) >= w.o.CheckpointBytes {
		if err := w.flush(); err != nil {
			return err
		}
		return w.checkpoint()
	}
	return nil
}

// FlushIfDue flushes the pending block if its first line is at least BlockAge
// old. Ingest calls it on a timer, so a block is written even when no further
// line arrives. It never checkpoints.
func (w *Writer) FlushIfDue() error {
	if w.err != nil {
		return w.err
	}
	if w.lines > 0 && w.o.Now().Sub(w.firstNow) >= w.o.BlockAge {
		return w.flush()
	}
	return nil
}

// flush writes the pending lines as one changes block. It does nothing when no
// line is pending.
func (w *Writer) flush() error {
	if w.lines == 0 {
		return nil
	}
	h := Header{
		Kind:       KindChanges,
		FirstTS:    w.firstTS,
		LastLVLine: w.lastLine,
		ReadAt:     w.firstNow.UnixMicro(),
		FlushedAt:  w.o.Now().UnixMicro(),
	}
	if err := w.appendLog(h, w.pending); err != nil {
		return err
	}
	w.sinceCP += int64(len(w.pending))
	w.pending = w.pending[:0]
	w.lines = 0
	return nil
}

// checkpoint writes the engine's current state; call it right after flush.
func (w *Writer) checkpoint() error {
	s := w.e.State()
	cp := Checkpoint{IDs: w.ids}
	for _, c := range s.Contracts {
		p, ok := w.pids[c.ID]
		if !ok {
			return w.fail(fmt.Errorf("store: checkpoint contract %q has no pid", c.ID))
		}
		cp.Contracts = append(cp.Contracts, ContractRec{T: c.T, PID: p, Deleted: c.Deleted, Name: c.Name, State: c.State})
	}
	for _, b := range s.Books {
		p, ok := w.pids[b.ID]
		if !ok {
			return w.fail(fmt.Errorf("store: checkpoint book %q has no pid", b.ID))
		}
		cp.Books = append(cp.Books, ProductRec{PID: p, Bids: b.Bids, Asks: b.Asks})
	}
	slices.SortFunc(cp.Contracts, func(a, b ContractRec) int { return int(a.PID) - int(b.PID) })
	slices.SortFunc(cp.Books, func(a, b ProductRec) int { return int(a.PID) - int(b.PID) })
	return w.writeCheckpoint(cp, s.Now)
}

// writeCheckpoint appends the checkpoint block to log, then its entry to index.
func (w *Writer) writeCheckpoint(cp Checkpoint, ts int64) error {
	at := w.logOff
	h := Header{Kind: KindCheckpoint, FirstTS: ts, LastLVLine: w.lastLine, FlushedAt: w.o.Now().UnixMicro()}
	h.ReadAt = h.FlushedAt
	if err := w.appendLog(h, AppendCheckpoint(nil, cp)); err != nil {
		return err
	}
	var ent [16]byte
	binary.LittleEndian.PutUint64(ent[0:], uint64(ts))
	binary.LittleEndian.PutUint64(ent[8:], uint64(at))
	if _, err := w.idx.Write(ent[:]); err != nil {
		return w.fail(err)
	}
	w.idxOff += int64(len(ent))
	w.sinceCP = 0
	return nil
}

// appendLog writes one block with a single Write call.
func (w *Writer) appendLog(h Header, payload []byte) error {
	if len(payload) > maxPayloadLen {
		return w.fail(fmt.Errorf("store: block payload %d bytes exceeds the limit of %d", len(payload), maxPayloadLen))
	}
	w.buf = AppendBlock(w.buf[:0], h, payload)
	n, err := w.log.Write(w.buf)
	w.logOff += int64(n)
	if err != nil {
		return w.fail(err)
	}
	return nil
}

func (w *Writer) fail(err error) error {
	w.err = err
	return err
}
