package store

import (
	"fmt"
	"math"
	"time"

	"dmt-orderbook/internal/engine"
)

// ReplayOptions: To is ignored when Follow is set.
type ReplayOptions struct {
	From, To int64
	Follow   bool
	Poll     time.Duration // follow: wait at the end of the log before retrying
	Idle     func() error  // follow: called each time the end is reached, before waiting; a non-nil error ends Replay with that error
}

// Event is one stored record after From, already applied to the engine.
type Event struct {
	T       int64
	TimeRaw string                 // a LINE: LineRec.TimeRaw(); a CONTRACT: CanonicalTime(T)
	Changes []engine.ProductChange // a LINE: its changed levels (contract IDs, new totals, 0 = gone)
	Entered string                 // a CONTRACT: the contract ID that came into scope, or was renamed while in scope
}

// Replay builds the state at From, calls start once, then calls step for each
// later record with time <= To, in store order. CONTRACT records that bring
// nothing into scope, DEFINE records and checkpoint blocks produce no event.
// step may be nil.
//
// With Follow set, To is ignored and Replay never ends by itself: at the end of
// the valid blocks (or while there is no log yet) it calls Idle, sleeps Poll
// and reads again at the same offset. It returns only an I/O or decode error,
// or the error from Idle.
func (r *Reader) Replay(o ReplayOptions, start func(e *engine.Engine) error, step func(e *engine.Engine, ev Event) error) error {
	if o.Follow {
		o.To = math.MaxInt64
	}
	log, idx, err := r.openBoth()
	if err != nil {
		return err
	}
	defer func() { closeAll(log, idx) }()
	for log == nil {
		if !o.Follow {
			return start(engine.New())
		}
		if err := o.wait(); err != nil {
			return err
		}
		closeAll(idx)
		if log, idx, err = r.openBoth(); err != nil {
			return err
		}
	}
	e := engine.New()
	ref, ok, err := findCheckpoint(idx, log, o.From)
	if err != nil {
		return err
	}
	var ids []string
	var off int64
	if ok {
		if e, ids, err = restoreCheckpoint(ref); err != nil {
			return err
		}
		off = ref.next
	}

	started := false
	// An explicit offset loop, so that follow can retry at the same offset.
	for {
		h, payload, next, valid, err := ReadBlock(log, off)
		if err != nil {
			return err
		}
		if !valid { // the end of the valid blocks
			if !o.Follow {
				break
			}
			if err := o.wait(); err != nil {
				return err
			}
			continue
		}
		if h.Kind == KindChanges {
			// Every line in this block is after From, so the store holds every line <= From.
			if !started && h.FirstTS > o.From {
				if err := start(e); err != nil {
					return err
				}
				started = true
			}
			recs, err := DecodeRecords(payload)
			if err != nil {
				return fmt.Errorf("store: block at %d: %w", off, err)
			}
			for _, rec := range recs {
				if rec.Define != nil {
					if ids, err = applyRecord(e, ids, rec); err != nil {
						return fmt.Errorf("store: block at %d: %w", off, err)
					}
					continue
				}
				t := recTime(rec)
				if !started && t > o.From {
					if err := start(e); err != nil {
						return err
					}
					started = true
				}
				if started && t > o.To {
					return nil
				}
				ev, emit, err := applyForEvent(e, ids, rec, started)
				if err != nil {
					return fmt.Errorf("store: block at %d: %w", off, err)
				}
				if emit && step != nil {
					if err := step(e, ev); err != nil {
						return err
					}
				}
			}
		}
		off = next
	}
	if !started {
		return start(e)
	}
	return nil
}

// wait is one follow pause at the end of the store: Idle, then sleep Poll.
func (o ReplayOptions) wait() error {
	if o.Idle != nil {
		if err := o.Idle(); err != nil {
			return err
		}
	}
	time.Sleep(o.Poll)
	return nil
}

// applyForEvent applies a CONTRACT or LINE record to e. When report is set it
// also says whether the record is an event, and which.
func applyForEvent(e *engine.Engine, ids []string, rec Record, report bool) (ev Event, emit bool, err error) {
	if rec.Contract != nil {
		c := rec.Contract
		if int(c.PID) >= len(ids) {
			return ev, false, fmt.Errorf("pid %d out of range", c.PID)
		}
		id := ids[c.PID]
		before, okB := e.NameInScope(id, c.T)
		if _, err := applyRecord(e, ids, rec); err != nil {
			return ev, false, err
		}
		after, okA := e.NameInScope(id, c.T)
		if report && okA && (!okB || before != after) {
			return Event{T: c.T, TimeRaw: CanonicalTime(c.T), Entered: id}, true, nil
		}
		return ev, false, nil
	}
	changes, err := lineChanges(ids, rec.Line)
	if err != nil {
		return ev, false, err
	}
	e.ApplyChanges(rec.Line.T, changes)
	if !report {
		return ev, false, nil
	}
	return Event{T: rec.Line.T, TimeRaw: rec.Line.TimeRaw(), Changes: changes}, true, nil
}
