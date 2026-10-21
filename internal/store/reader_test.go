package store

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"dmt-orderbook/internal/engine"
	"dmt-orderbook/internal/feed"
)

// ctLine is a contracts line with a chosen state.
func ctLine(us int, id, name, state string) string {
	return fmt.Sprintf(`{"timestamp_utc":%q,"body":{"contractId":%q,"contractName":%q,"dlvryAreaState":[{"dlvryAreaId":46,"state":%q}]}}`, at(us), id, name, state)
}

// order is one order for lvLine; Qty 0 means deleted.
type order struct {
	ID         string
	Price, Qty int
}

func orders(side []order) string {
	var parts []string
	for _, o := range side {
		parts = append(parts, fmt.Sprintf(`{"orderId":%q,"price":%d,"qty":%d,"deleted":%t}`, o.ID, o.Price, o.Qty, o.Qty == 0))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// lvLine is a localview line with one contract.
func lvLine(us int, snapshot bool, id string, buys, sells []order) string {
	return fmt.Sprintf(`{"timestamp_utc":%q,"stomp_headers":{"x-nps-snapshot":"%t"},"body":[{"contractId":%q,"buyOrders":%s,"sellOrders":%s}]}`, at(us), snapshot, id, orders(buys), orders(sells))
}

// sampleFeed: two contracts, deltas, a snapshot, and a gate closure of NX_1.
func sampleFeed() (lv, ct []string) {
	ct = []string{
		ctLine(0, "NX_1", "QH-1", "ACTI"),
		ctLine(1, "NX_2", "QH-2", "ACTI"),
		ctLine(60, "NX_1", "QH-1", "IACT"),
	}
	lv = []string{
		lvLine(10, false, "NX_1", []order{{"a", 100, 5}}, []order{{"s", 110, 2}}),
		lvLine(20, false, "NX_2", []order{{"b", 200, 3}}, nil),
		lvLine(30, false, "NX_1", []order{{"c", 101, 4}, {"a", 100, 6}}, nil),
		lvLine(35, false, "NX_1", []order{{"d", 99, 1}}, []order{{"s2", 112, 7}}),
		lvLine(40, true, "NX_1", []order{{"a", 100, 6}, {"e", 98, 2}}, []order{{"s", 110, 2}}),
		lvLine(50, false, "NX_2", []order{{"b", 200, 0}, {"f", 201, 9}}, []order{{"t", 205, 1}}),
		lvLine(55, false, "NX_1", []order{{"c", 101, 0}}, nil),
		lvLine(70, false, "NX_2", []order{{"g", 202, 2}}, nil),
		lvLine(80, false, "NX_2", nil, []order{{"t", 205, 0}, {"u", 204, 3}}),
		lvLine(90, false, "NX_2", []order{{"g", 202, 0}}, nil),
	}
	return lv, ct
}

// buildStore ingests the lines into a fresh store dir.
func buildStore(t *testing.T, lv, ct []string, mutate func(*Options)) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "store")
	o := testOptions(&clock{})
	if mutate != nil {
		mutate(&o)
	}
	if _, err := Ingest(merger(lv, ct), dir, o, 0); err != nil {
		t.Fatal(err)
	}
	return dir
}

// oracle feeds the same lines to a plain engine up to and including time t.
func oracle(t *testing.T, lv, ct []string, upTo int64) *engine.Engine {
	t.Helper()
	m := merger(lv, ct)
	e := engine.New()
	for {
		rec, err := m.Next()
		if err == io.EOF {
			return e
		}
		if err != nil {
			t.Fatal(err)
		}
		if rec.Time() > upTo {
			return e
		}
		if rec.Source == feed.SourceLocalview {
			e.ApplyLocalview(*rec.Localview)
		} else {
			e.ApplyContract(*rec.Contract)
		}
	}
}

// inputTimes are all distinct line times of the feed.
func inputTimes(t *testing.T, lv, ct []string) []int64 {
	t.Helper()
	m := merger(lv, ct)
	var out []int64
	for {
		rec, err := m.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if n := len(out); n == 0 || out[n-1] != rec.Time() {
			out = append(out, rec.Time())
		}
	}
}

// checkAt compares the reader's state at tt with the oracle's. Now is not
// compared: the store skips lines that changed nothing.
func checkAt(t *testing.T, r *Reader, lv, ct []string, tt int64) {
	t.Helper()
	got, err := r.StateAt(tt)
	if err != nil {
		t.Fatalf("StateAt(%d): %v", tt, err)
	}
	want := oracle(t, lv, ct, tt)
	gs, ws := got.State(), want.State()
	if !reflect.DeepEqual(gs.Contracts, ws.Contracts) || !reflect.DeepEqual(gs.Books, ws.Books) {
		t.Fatalf("StateAt(%d):\n got %+v\nwant %+v", tt, gs, ws)
	}
	gt, gerr := got.Top(tt)
	wt, werr := want.Top(tt)
	if gerr != nil || werr != nil || !reflect.DeepEqual(gt, wt) {
		t.Fatalf("Top(%d): got %v %v, want %v %v", tt, gt, gerr, wt, werr)
	}
}

func checkAll(t *testing.T, r *Reader, lv, ct []string) {
	t.Helper()
	for _, ti := range inputTimes(t, lv, ct) {
		for _, d := range []int64{-1, 0, 1} {
			checkAt(t, r, lv, ct, ti+d)
		}
	}
}

func openReader(t *testing.T, dir string) *Reader {
	t.Helper()
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestStateAtMatchesEngine(t *testing.T) {
	lv, ct := sampleFeed()
	for _, cp := range []int64{1, 200, 1 << 20} {
		t.Run(fmt.Sprint("checkpointBytes=", cp), func(t *testing.T) {
			dir := buildStore(t, lv, ct, func(o *Options) { o.CheckpointBytes = cp })
			checkAll(t, openReader(t, dir), lv, ct)
		})
	}
}

func TestStateAtSameTimeAcrossCheckpoint(t *testing.T) {
	ct := []string{ctLine(0, "NX_1", "QH-1", "ACTI")}
	lv := []string{
		lvLine(10, false, "NX_1", []order{{"a", 100, 5}}, nil),
		lvLine(10, false, "NX_1", []order{{"b", 101, 4}}, nil),
		lvLine(20, false, "NX_1", []order{{"c", 102, 3}}, nil),
	}
	dir := buildStore(t, lv, ct, func(o *Options) { o.CheckpointBytes = 1 })
	r := openReader(t, dir)
	e, err := r.StateAt(atMicros(10))
	if err != nil {
		t.Fatal(err)
	}
	pb, ok, err := e.BookByName("QH-1", atMicros(10))
	if err != nil || !ok || len(pb.Bids) != 2 {
		t.Fatalf("book %+v ok=%v err=%v, want both levels", pb, ok, err)
	}
	checkAll(t, r, lv, ct)
}

// atMicros is the Unix microsecond time of at(us).
func atMicros(us int) int64 {
	return micros(at(us))
}

func TestStateAtContractArrivesAfterOrders(t *testing.T) {
	lv := []string{
		lvLine(10, false, "NX_9", []order{{"a", 100, 5}}, []order{{"s", 110, 2}}),
		lvLine(20, false, "NX_9", []order{{"b", 99, 1}}, nil),
	}
	ct := []string{ctLine(30, "NX_9", "QH-9", "ACTI")}
	dir := buildStore(t, lv, ct, func(o *Options) { o.CheckpointBytes = 1 })
	r := openReader(t, dir)

	before := atMicros(25)
	e, err := r.StateAt(before)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.BookByName("QH-9", before); ok {
		t.Fatal("QH-9 found before its contract line")
	}
	after := atMicros(30)
	e, err = r.StateAt(after)
	if err != nil {
		t.Fatal(err)
	}
	pb, ok, err := e.BookByName("QH-9", after)
	if err != nil || !ok || len(pb.Bids) != 2 || len(pb.Asks) != 1 {
		t.Fatalf("book %+v ok=%v err=%v, want 2 bids and 1 ask", pb, ok, err)
	}
	checkAll(t, r, lv, ct)
}

func TestStateAtBeforeFirstAndAfterLast(t *testing.T) {
	lv, ct := sampleFeed()
	dir := buildStore(t, lv, ct, func(o *Options) { o.CheckpointBytes = 200 })
	r := openReader(t, dir)

	e, err := r.StateAt(math.MinInt64 + 1)
	if err != nil {
		t.Fatal(err)
	}
	if top, err := e.Top(math.MinInt64 + 1); err != nil || len(top) != 0 {
		t.Fatalf("Top before first = %v, %v", top, err)
	}

	last := atMicros(90) + 3600*1_000_000
	checkAt(t, r, lv, ct, last)
}

func copyDir(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "copy")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"log", "index"} {
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func TestReaderTornLog(t *testing.T) {
	lv, ct := sampleFeed()
	// One block per line, so the last block is exactly the last input line.
	dir := buildStore(t, lv, ct, func(o *Options) { o.BlockBytes = 1; o.CheckpointBytes = 1 << 20 })
	blocks := readLog(t, dir)
	last := blocks[len(blocks)-1]
	prev := blocks[len(blocks)-2]
	if last.h.Kind != KindChanges || last.h.FirstTS != atMicros(90) {
		t.Fatalf("unexpected last block %+v", last.h)
	}
	full, err := os.ReadFile(filepath.Join(dir, "log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, cut := range []int64{0, 1, 4, 20, HeaderSize - 1, HeaderSize, HeaderSize + 1, int64(len(full)) - last.off - 1} {
		cd := copyDir(t, dir)
		if err := os.Truncate(filepath.Join(cd, "log"), last.off+cut); err != nil {
			t.Fatal(err)
		}
		r := openReader(t, cd)
		st, err := r.Status()
		if err != nil || st != prev.h.LastLVLine {
			t.Fatalf("cut %d: Status = %d, %v; want %d", cut, st, err, prev.h.LastLVLine)
		}
		for _, ti := range inputTimes(t, lv, ct) {
			if ti < last.h.FirstTS {
				checkAt(t, r, lv, ct, ti)
			}
		}
	}
}

func TestReaderTornIndex(t *testing.T) {
	lv, ct := sampleFeed()
	dir := buildStore(t, lv, ct, func(o *Options) { o.CheckpointBytes = 200 })
	if n := len(readIndex(t, dir)); n < 3 {
		t.Fatalf("want several checkpoints, got %d", n)
	}
	idx := filepath.Join(dir, "index")
	b, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	for _, torn := range [][]byte{
		b[:len(b)-16],
		append(slices.Clone(b[:len(b)-16]), 1, 2, 3, 4, 5, 6, 7),
	} {
		cd := copyDir(t, dir)
		if err := os.WriteFile(filepath.Join(cd, "index"), torn, 0o644); err != nil {
			t.Fatal(err)
		}
		r := openReader(t, cd)
		checkAll(t, r, lv, ct)
		want, _ := openReader(t, dir).Status()
		if st, err := r.Status(); err != nil || st != want {
			t.Fatalf("Status = %d, %v; want %d", st, err, want)
		}
	}
}

func TestReaderIndexPointsAtBadBlock(t *testing.T) {
	lv, ct := sampleFeed()
	dir := buildStore(t, lv, ct, func(o *Options) { o.CheckpointBytes = 200 })
	entries := readIndex(t, dir)
	st, _ := os.Stat(filepath.Join(dir, "log"))
	for _, bad := range []int64{st.Size() + 100, 7} { // past the end; inside a block
		cd := copyDir(t, dir)
		b, err := os.ReadFile(filepath.Join(cd, "index"))
		if err != nil {
			t.Fatal(err)
		}
		off := (len(entries)-1)*16 + 8
		for i := 0; i < 8; i++ {
			b[off+i] = byte(bad >> (8 * i))
		}
		if err := os.WriteFile(filepath.Join(cd, "index"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		checkAll(t, openReader(t, cd), lv, ct)
	}
}

func TestReaderNoIndexWalksFromStart(t *testing.T) {
	lv, ct := sampleFeed()
	dir := buildStore(t, lv, ct, func(o *Options) { o.CheckpointBytes = 200 })
	if err := os.Remove(filepath.Join(dir, "index")); err != nil {
		t.Fatal(err)
	}
	r := openReader(t, dir)
	checkAll(t, r, lv, ct)
	if st, err := r.Status(); err != nil || st != 10 {
		t.Fatalf("Status = %d, %v; want 10", st, err)
	}
}

func TestReaderStatus(t *testing.T) {
	lv, ct := sampleFeed()
	for _, cp := range []int64{1, 200, 1 << 20} {
		dir := buildStore(t, lv, ct, func(o *Options) { o.CheckpointBytes = cp })
		if st, err := openReader(t, dir).Status(); err != nil || st != int64(len(lv)) {
			t.Fatalf("checkpointBytes=%d: Status = %d, %v; want %d", cp, st, err, len(lv))
		}
	}
}

func TestReaderForEachBlock(t *testing.T) {
	lv, ct := sampleFeed()
	dir := buildStore(t, lv, ct, func(o *Options) { o.CheckpointBytes = 200 })
	want := readLog(t, dir)
	var got []int64
	err := openReader(t, dir).ForEachBlock(func(off int64, h Header, payload []byte) error {
		got = append(got, off)
		return nil
	})
	if err != nil || len(got) != len(want) {
		t.Fatalf("got %d blocks, %v; want %d", len(got), err, len(want))
	}
	for i := range got {
		if got[i] != want[i].off {
			t.Fatalf("block %d at %d, want %d", i, got[i], want[i].off)
		}
	}
	stop := fmt.Errorf("stop")
	if err := openReader(t, dir).ForEachBlock(func(int64, Header, []byte) error { return stop }); err != stop {
		t.Fatalf("callback error not returned: %v", err)
	}
}

func TestReaderEmptyStore(t *testing.T) {
	empty := t.TempDir()
	withFiles := t.TempDir()
	for _, name := range []string{"log", "index"} {
		if err := os.WriteFile(filepath.Join(withFiles, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{empty, withFiles} {
		r := openReader(t, dir)
		if st, err := r.Status(); err != nil || st != 0 {
			t.Fatalf("Status = %d, %v", st, err)
		}
		e, err := r.StateAt(atMicros(50))
		if err != nil {
			t.Fatal(err)
		}
		if top, err := e.Top(atMicros(50)); err != nil || len(top) != 0 {
			t.Fatalf("Top = %v, %v", top, err)
		}
		if err := r.ForEachBlock(func(int64, Header, []byte) error { t.Fatal("block in empty store"); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Open(filepath.Join(empty, "missing")); err == nil {
		t.Fatal("Open of a missing dir succeeded")
	}
}
