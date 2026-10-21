package store

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dmt-orderbook/internal/engine"
	"dmt-orderbook/internal/feed"
)

func at(us int) string {
	return fmt.Sprintf("2026-09-24T10:00:%02d.%06d+00:00", us/1000000, us%1000000)
}

func contractLine(us int, id, name string) string {
	return fmt.Sprintf(`{"timestamp_utc":%q,"body":{"contractId":%q,"contractName":%q,"dlvryAreaState":[{"dlvryAreaId":46,"state":"ACTI"}]}}`, at(us), id, name)
}

// deltaLine puts one bid order (id "B"+price) on contract id.
func deltaLine(us int, id string, price, qty int) string {
	return fmt.Sprintf(`{"timestamp_utc":%q,"stomp_headers":{"x-nps-snapshot":"false"},"body":[{"contractId":%q,"buyOrders":[{"orderId":"B%d","price":%d,"qty":%d,"deleted":false}],"sellOrders":[]}]}`, at(us), id, price, price, qty)
}

func merger(lv, ct []string) *feed.Merger {
	join := func(l []string) *strings.Reader {
		if len(l) == 0 {
			return strings.NewReader("")
		}
		return strings.NewReader(strings.Join(l, "\n") + "\n")
	}
	return feed.NewMerger(join(lv), join(ct), "localview.jsonl", "contracts.jsonl")
}

// clock is a fake wall clock.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func testOptions(c *clock) Options {
	o := DefaultOptions()
	o.Now = c.now
	return o
}

type blk struct {
	h       Header
	payload []byte
	off     int64
}

func readLog(t *testing.T, dir string) []blk {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []blk
	for off := int64(0); ; {
		h, p, next, ok, err := ReadBlock(f, off)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		out = append(out, blk{h, p, off})
		off = next
	}
	st, _ := f.Stat()
	if n := len(out); n > 0 {
		if end := out[n-1].off + HeaderSize + int64(len(out[n-1].payload)); end != st.Size() {
			t.Fatalf("log has %d bytes but valid blocks end at %d", st.Size(), end)
		}
	}
	return out
}

func readIndex(t *testing.T, dir string) [][2]int64 {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b)%16 != 0 {
		t.Fatalf("index size %d", len(b))
	}
	var out [][2]int64
	for i := 0; i < len(b); i += 16 {
		out = append(out, [2]int64{int64(leU64(b[i:])), int64(leU64(b[i+8:]))})
	}
	return out
}

func leU64(b []byte) uint64 {
	var v uint64
	for i := 7; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}

func mustContract(t *testing.T, line string) feed.Contract {
	t.Helper()
	c, err := feed.DecodeContract([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mustLocalview(t *testing.T, line string) feed.Localview {
	t.Helper()
	lv, err := feed.DecodeLocalview([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	return lv
}

// feedLV applies a localview line to e and hands it to w as line number n.
func feedLV(t *testing.T, w *Writer, e *engine.Engine, n int, line string) {
	t.Helper()
	lv := mustLocalview(t, line)
	if err := w.Localview(n, lv, e.ApplyLocalview(lv)); err != nil {
		t.Fatal(err)
	}
}

func feedContract(t *testing.T, w *Writer, e *engine.Engine, line string) {
	t.Helper()
	c := mustContract(t, line)
	if err := w.Contract(c, e.ApplyContract(c)); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRefusesExistingDir(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "keep")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(dir, engine.New(), DefaultOptions()); err == nil {
		t.Fatal("expected error")
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != "keep" {
		t.Fatalf("dir changed: %v", ents)
	}
	if b, _ := os.ReadFile(keep); string(b) != "x" {
		t.Fatal("file changed")
	}
}

func TestCreateWritesStartCheckpoint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	w, err := Create(dir, engine.New(), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	bs := readLog(t, dir)
	if len(bs) != 1 || bs[0].h.Kind != KindCheckpoint || bs[0].h.FirstTS != math.MinInt64 || bs[0].h.LastLVLine != 0 {
		t.Fatalf("blocks %+v", bs)
	}
	cp, err := DecodeCheckpoint(bs[0].payload)
	if err != nil || len(cp.IDs)+len(cp.Contracts)+len(cp.Books) != 0 {
		t.Fatalf("checkpoint %+v %v", cp, err)
	}
	if ix := readIndex(t, dir); !reflect.DeepEqual(ix, [][2]int64{{math.MinInt64, 0}}) {
		t.Fatalf("index %v", ix)
	}
}

func TestWriterFlushByAge(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	clk := &clock{time.Unix(1000, 0)}
	e := engine.New()
	w, err := Create(dir, e, testOptions(clk))
	if err != nil {
		t.Fatal(err)
	}
	feedContract(t, w, e, contractLine(1, "NX_1", "QH-1"))
	for i := 1; i <= 2; i++ {
		feedLV(t, w, e, i, deltaLine(10+i, "NX_1", 100+i, 5))
	}
	if n := len(readLog(t, dir)); n != 1 {
		t.Fatalf("%d blocks before age, want 1", n)
	}
	clk.t = clk.t.Add(2 * time.Millisecond)
	feedLV(t, w, e, 3, deltaLine(13, "NX_1", 103, 5))
	bs := readLog(t, dir)
	if len(bs) != 2 || bs[1].h.Kind != KindChanges {
		t.Fatalf("blocks %+v", bs)
	}
	h := bs[1].h
	if h.FirstTS != micros(at(1)) || h.LastLVLine != 3 {
		t.Fatalf("header %+v", h)
	}
	if h.ReadAt != time.Unix(1000, 0).UnixMicro() || h.FlushedAt != clk.t.UnixMicro() {
		t.Fatalf("stamps %+v", h)
	}
	recs, err := DecodeRecords(bs[1].payload)
	if err != nil {
		t.Fatal(err)
	}
	var defs, cons, lines int
	for _, r := range recs {
		switch {
		case r.Define != nil:
			defs++
		case r.Contract != nil:
			cons++
		case r.Line != nil:
			lines++
		}
	}
	if defs != 1 || cons != 1 || lines != 3 {
		t.Fatalf("records %d %d %d", defs, cons, lines)
	}
	w.Close()
}

// micros is the Unix microsecond time of an RFC 3339 timestamp.
func micros(s string) int64 {
	tm, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return tm.UnixMicro()
}

func TestWriterEmptyBlockKeepsLineNumber(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	clk := &clock{time.Unix(1000, 0)}
	e := engine.New()
	w, _ := Create(dir, e, testOptions(clk))
	// An empty delta changes nothing.
	feedLV(t, w, e, 7, fmt.Sprintf(`{"timestamp_utc":%q,"stomp_headers":{"x-nps-snapshot":"false"},"body":[]}`, at(1)))
	clk.t = clk.t.Add(time.Second)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	bs := readLog(t, dir)
	if len(bs) != 2 || bs[1].h.Kind != KindChanges || len(bs[1].payload) != 0 || bs[1].h.LastLVLine != 7 {
		t.Fatalf("blocks %+v", bs)
	}
}

func TestWriterCheckpointBySize(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	clk := &clock{time.Unix(1000, 0)}
	o := testOptions(clk)
	o.CheckpointBytes = 1
	e := engine.New()
	w, _ := Create(dir, e, o)
	feedContract(t, w, e, contractLine(1, "NX_1", "QH-1"))
	feedLV(t, w, e, 1, deltaLine(2, "NX_1", 100, 5))
	feedLV(t, w, e, 2, deltaLine(3, "NX_2", 200, 6))
	w.Close()
	bs := readLog(t, dir)
	if len(bs) != 7 {
		t.Fatalf("%d blocks, want 1 start + 3x(changes, checkpoint)", len(bs))
	}
	ix := readIndex(t, dir)
	if len(ix) != 4 {
		t.Fatalf("index %v", ix)
	}
	for i := 1; i <= 3; i++ {
		ch, cp := bs[2*i-1], bs[2*i]
		if ch.h.Kind != KindChanges || cp.h.Kind != KindCheckpoint {
			t.Fatalf("block %d kinds %d %d", i, ch.h.Kind, cp.h.Kind)
		}
		if ix[i][1] != cp.off || ix[i][0] != cp.h.FirstTS {
			t.Fatalf("index %d = %v, block at %d ts %d", i, ix[i], cp.off, cp.h.FirstTS)
		}
	}
	last := bs[6]
	if last.h.LastLVLine != 2 || last.h.FirstTS != micros(at(3)) {
		t.Fatalf("header %+v", last.h)
	}
	cp, err := DecodeCheckpoint(last.payload)
	if err != nil {
		t.Fatal(err)
	}
	want := Checkpoint{
		IDs:       []string{"NX_1", "NX_2"},
		Contracts: []ContractRec{{T: micros(at(1)), PID: 0, Name: "QH-1", State: "ACTI"}},
		Books: []ProductRec{
			{PID: 0, Bids: []engine.Level{{Price: 100, Qty: 5}}},
			{PID: 1, Bids: []engine.Level{{Price: 200, Qty: 6}}},
		},
	}
	if !reflect.DeepEqual(cp, want) {
		t.Fatalf("got %+v\nwant %+v", cp, want)
	}
}

func TestWriterOversizedLine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	clk := &clock{time.Unix(1000, 0)}
	o := testOptions(clk)
	o.BlockBytes = 64
	e := engine.New()
	w, _ := Create(dir, e, o)
	lv := mustLocalview(t, fmt.Sprintf(`{"timestamp_utc":%q,"stomp_headers":{"x-nps-snapshot":"false"},"body":[{"contractId":"NX_1","buyOrders":[`+
		`{"orderId":"a","price":1,"qty":1,"deleted":false},{"orderId":"b","price":2,"qty":1,"deleted":false},`+
		`{"orderId":"c","price":3,"qty":1,"deleted":false},{"orderId":"d","price":4,"qty":1,"deleted":false}],"sellOrders":[]}]}`, at(1)))
	changes := e.ApplyLocalview(lv)
	if err := w.Localview(1, lv, changes); err != nil {
		t.Fatal(err)
	}
	bs := readLog(t, dir)
	if len(bs) != 2 || len(bs[1].payload) <= 64 {
		t.Fatalf("blocks %d", len(bs))
	}
	recs, err := DecodeRecords(bs[1].payload)
	if err != nil || len(recs) != 2 || recs[1].Line == nil || len(recs[1].Line.Products[0].Bids) != 4 {
		t.Fatalf("recs %+v %v", recs, err)
	}
	w.Close()
}

func TestIngestBadLineFlushes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	lv := []string{deltaLine(2, "NX_1", 100, 5), deltaLine(3, "NX_1", 101, 5), `{not json`}
	st, err := Ingest(merger(lv, []string{contractLine(1, "NX_1", "QH-1")}), dir, DefaultOptions(), 0)
	if err == nil || !strings.Contains(err.Error(), ":3") {
		t.Fatalf("err %v", err)
	}
	if st.Localview != 2 {
		t.Fatalf("stats %+v", st)
	}
	bs := readLog(t, dir)
	last := bs[len(bs)-1]
	if last.h.Kind != KindChanges || last.h.LastLVLine != 2 {
		t.Fatalf("last %+v", last.h)
	}
}

func TestIngestStopAfter(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	lv := []string{deltaLine(2, "NX_1", 100, 5), deltaLine(3, "NX_1", 101, 5), deltaLine(4, "NX_1", 102, 5)}
	st, err := Ingest(merger(lv, []string{contractLine(1, "NX_1", "QH-1")}), dir, DefaultOptions(), 2)
	if err != nil || st.Localview != 2 || st.Contracts != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	bs := readLog(t, dir)
	if got := bs[len(bs)-1].h.LastLVLine; got != 2 {
		t.Fatalf("LastLVLine %d", got)
	}
	if lb, ib := st.LogBytes, st.IndexBytes; lb == 0 || ib != 16 {
		t.Fatalf("sizes %d %d", lb, ib)
	}
}

func TestWriterRejectsOversizedBlock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	w, err := Create(dir, engine.New(), testOptions(&clock{time.Unix(1000, 0)}))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.appendLog(Header{Kind: KindChanges}, make([]byte, maxPayloadLen+1)); err == nil {
		t.Fatal("oversized payload was written")
	}
}
