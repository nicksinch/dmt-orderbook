package store

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"dmt-orderbook/internal/engine"
)

type seenEvent struct {
	T       int64
	TimeRaw string
	Entered string
	Changes int
}

// replayAll runs Replay and returns the start state and the events.
func replayAll(t *testing.T, r *Reader, from, to int64) (engine.State, []seenEvent, *engine.Engine) {
	t.Helper()
	var start engine.State
	var evs []seenEvent
	var last *engine.Engine
	err := r.Replay(ReplayOptions{From: from, To: to},
		func(e *engine.Engine) error { start = e.State(); return nil },
		func(e *engine.Engine, ev Event) error {
			evs = append(evs, seenEvent{ev.T, ev.TimeRaw, ev.Entered, len(ev.Changes)})
			last = e
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return start, evs, last
}

func sameState(t *testing.T, got, want engine.State, what string) {
	t.Helper()
	if !reflect.DeepEqual(got.Contracts, want.Contracts) || !reflect.DeepEqual(got.Books, want.Books) {
		t.Fatalf("%s:\n got %+v\nwant %+v", what, got, want)
	}
}

func testReplayEvents(t *testing.T, mutate func(*Options)) {
	lv, ct := sampleFeed()
	dir := buildStore(t, lv, ct, mutate)
	r := openReader(t, dir)

	var final *engine.Engine
	var start engine.State
	var times []int64
	var firstRaw string
	err := r.Replay(ReplayOptions{From: atMicros(30), To: atMicros(80)},
		func(e *engine.Engine) error { start = e.State(); return nil },
		func(e *engine.Engine, ev Event) error {
			if ev.Entered != "" || len(ev.Changes) == 0 {
				t.Fatalf("event %+v: want a line event with changes", ev)
			}
			if len(times) == 0 {
				firstRaw = ev.TimeRaw
			}
			times = append(times, ev.T)
			final = e
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	sameState(t, start, oracle(t, lv, ct, atMicros(30)).State(), "start")
	want := []int64{atMicros(35), atMicros(40), atMicros(50), atMicros(70), atMicros(80)}
	if !reflect.DeepEqual(times, want) {
		t.Fatalf("event times %v, want %v", times, want)
	}
	if firstRaw != at(35) {
		t.Fatalf("first TimeRaw %q, want %q", firstRaw, at(35))
	}
	sameState(t, final.State(), oracle(t, lv, ct, atMicros(80)).State(), "end")
}

func TestReplayEvents(t *testing.T) { testReplayEvents(t, nil) }

func TestReplayEntered(t *testing.T) {
	lv, ct := sampleFeed()
	ct = append(ct,
		ctLine(65, "NX_3", "QH-3", "ACTI"),
		ctLine(66, "NX_2", "QH-2b", "ACTI"),
		ctLine(67, "NX_2", "QH-2b", "ACTI"))
	// the feed must be sorted by time: the new line goes between 55 and 70
	lv = slices.Insert(lv, 7, lvLine(62, false, "NX_3", []order{{"z", 300, 1}}, nil))
	r := openReader(t, buildStore(t, lv, ct, nil))
	_, evs, _ := replayAll(t, r, atMicros(60), atMicros(68))
	want := []seenEvent{
		{atMicros(62), at(62), "", 1},
		{atMicros(65), CanonicalTime(atMicros(65)), "NX_3", 0},
		{atMicros(66), CanonicalTime(atMicros(66)), "NX_2", 0},
	}
	if !reflect.DeepEqual(evs, want) {
		t.Fatalf("events %+v, want %+v", evs, want)
	}
}

func TestReplayAcrossCheckpoints(t *testing.T) {
	cp := func(o *Options) { o.CheckpointBytes = 1 }
	testReplayEvents(t, cp)
	lv, ct := sampleFeed()
	r := openReader(t, buildStore(t, lv, ct, cp))
	for _, ti := range inputTimes(t, lv, ct) {
		start, _, _ := replayAll(t, r, ti, ti)
		sameState(t, start, oracle(t, lv, ct, ti).State(), "start")
	}
}

func TestReplayFromAfterEnd(t *testing.T) {
	lv, ct := sampleFeed()
	r := openReader(t, buildStore(t, lv, ct, nil))
	start, evs, _ := replayAll(t, r, atMicros(1000), atMicros(2000))
	sameState(t, start, oracle(t, lv, ct, atMicros(1000)).State(), "start")
	if len(evs) != 0 {
		t.Fatalf("events %+v, want none", evs)
	}
}

var errDone = errors.New("done")

// splitStore copies the store dir and cuts the copy's log at cut bytes.
// Appending rest to the copy's log completes it.
func splitStore(t *testing.T, dir string, cut int64) (prefixDir string, rest []byte) {
	t.Helper()
	full, err := os.ReadFile(filepath.Join(dir, "log"))
	if err != nil {
		t.Fatal(err)
	}
	prefixDir = copyDir(t, dir)
	if err := os.Truncate(filepath.Join(prefixDir, "log"), cut); err != nil {
		t.Fatal(err)
	}
	return prefixDir, full[cut:]
}

func appendLog(t *testing.T, dir string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, "log"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// replayEvents runs Replay and returns every event; idle is only used with follow.
func replayEvents(t *testing.T, dir string, o ReplayOptions, start func(e *engine.Engine) error, evs *[]Event) error {
	t.Helper()
	if start == nil {
		start = func(*engine.Engine) error { return nil }
	}
	return openReader(t, dir).Replay(o, start, func(_ *engine.Engine, ev Event) error {
		*evs = append(*evs, ev)
		return nil
	})
}

// wantEvents is the non-follow replay of the whole store from from.
func wantEvents(t *testing.T, dir string, from int64) []Event {
	t.Helper()
	var evs []Event
	if err := replayEvents(t, dir, ReplayOptions{From: from, To: math.MaxInt64}, nil, &evs); err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 {
		t.Fatal("reference replay has no events")
	}
	return evs
}

func followStore(t *testing.T) string {
	lv, ct := sampleFeed()
	return buildStore(t, lv, ct, func(o *Options) { o.CheckpointBytes = 1 })
}

// appendThenDone is an Idle that appends rest on its first call and ends follow on its second.
func appendThenDone(t *testing.T, dir string, rest []byte, onFirst func()) func() error {
	calls := 0
	return func() error {
		calls++
		switch calls {
		case 1:
			if onFirst != nil {
				onFirst()
			}
			appendLog(t, dir, rest)
			return nil
		case 2:
			return errDone
		}
		t.Fatalf("Idle called %d times", calls)
		return nil
	}
}

func TestFollowMatchesReplay(t *testing.T) {
	dir := followStore(t)
	want := wantEvents(t, dir, atMicros(5))
	blocks := readLog(t, dir)
	startCP := -1
	for i, b := range blocks {
		if b.h.Kind == KindCheckpoint && b.h.FirstTS <= atMicros(5) {
			startCP = i
		}
	}
	if startCP < 0 {
		t.Fatal("no start checkpoint")
	}
	last := blocks[len(blocks)-1]
	cuts := []int64{last.off + HeaderSize + int64(len(last.payload))}
	for _, b := range blocks[startCP+1:] {
		cuts = append(cuts, b.off)
	}
	for _, cut := range cuts {
		prefix, rest := splitStore(t, dir, cut)
		var got []Event
		o := ReplayOptions{From: atMicros(5), Follow: true, Idle: appendThenDone(t, prefix, rest, nil)}
		if err := replayEvents(t, prefix, o, nil, &got); err != errDone {
			t.Fatalf("cut %d: err %v, want errDone", cut, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("cut %d: events\n got %+v\nwant %+v", cut, got, want)
		}
	}
}

func TestFollowTornTail(t *testing.T) {
	dir := followStore(t)
	want := wantEvents(t, dir, atMicros(5))
	blocks := readLog(t, dir)
	var lastCh blk
	for _, b := range blocks {
		if b.h.Kind == KindChanges {
			lastCh = b
		}
	}
	var before []Event
	for _, ev := range want {
		if ev.T < lastCh.h.FirstTS {
			before = append(before, ev)
		}
	}
	if len(before) == len(want) {
		t.Fatal("the last changes block has no events")
	}
	prefix, rest := splitStore(t, dir, lastCh.off+(HeaderSize+int64(len(lastCh.payload)))/2)
	var got []Event
	idle := appendThenDone(t, prefix, rest, func() {
		if !reflect.DeepEqual(got, before) {
			t.Fatalf("events before the torn block\n got %+v\nwant %+v", got, before)
		}
	})
	if err := replayEvents(t, prefix, ReplayOptions{From: atMicros(5), Follow: true, Idle: idle}, nil, &got); err != errDone {
		t.Fatalf("err %v, want errDone", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events\n got %+v\nwant %+v", got, want)
	}
}

func TestFollowWaitsForFrom(t *testing.T) {
	lv, ct := sampleFeed()
	dir := followStore(t)
	want := wantEvents(t, dir, atMicros(50))
	var cut int64 = -1
	for _, b := range readLog(t, dir) {
		if b.h.Kind == KindChanges && b.h.FirstTS == atMicros(30) {
			cut = b.off + HeaderSize + int64(len(b.payload))
		}
	}
	if cut < 0 {
		t.Fatal("no block for the line at 30")
	}
	prefix, rest := splitStore(t, dir, cut)
	starts := 0
	var startState engine.State
	start := func(e *engine.Engine) error { starts++; startState = e.State(); return nil }
	idle := appendThenDone(t, prefix, rest, func() {
		if starts != 0 {
			t.Fatal("start called before the store reached From")
		}
	})
	var got []Event
	if err := replayEvents(t, prefix, ReplayOptions{From: atMicros(50), Follow: true, Idle: idle}, start, &got); err != errDone {
		t.Fatalf("err %v, want errDone", err)
	}
	if starts != 1 {
		t.Fatalf("start called %d times, want 1", starts)
	}
	sameState(t, startState, oracle(t, lv, ct, atMicros(50)).State(), "start")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events\n got %+v\nwant %+v", got, want)
	}
}

func TestFollowNoLogYet(t *testing.T) {
	full := followStore(t)
	want := wantEvents(t, full, atMicros(5))
	dir := t.TempDir()
	calls := 0
	idle := func() error {
		calls++
		if calls == 2 {
			return errDone
		}
		for _, name := range []string{"log", "index"} {
			b, err := os.ReadFile(filepath.Join(full, name))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	var got []Event
	if err := replayEvents(t, dir, ReplayOptions{From: atMicros(5), Follow: true, Idle: idle}, nil, &got); err != errDone {
		t.Fatalf("err %v, want errDone", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events\n got %+v\nwant %+v", got, want)
	}
}
