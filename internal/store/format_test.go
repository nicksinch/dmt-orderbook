package store

import (
	"bytes"
	"reflect"
	"testing"

	"dmt-orderbook/internal/engine"
)

func sampleRecords() []Record {
	return []Record{
		{Define: &Define{PID: 7, ContractID: "NX_123"}},
		{Contract: &ContractRec{T: 1790241913543924, PID: 7, Deleted: true, Name: "NX_PH-01", State: "ACTI"}},
		{Line: &LineRec{
			T: 1790241913543924, Snapshot: true, Raw: "2026-09-24T10:00:00.5Z",
			Products: []ProductRec{
				{PID: 7, Bids: []engine.Level{{Price: -500, Qty: 10}, {Price: -600, Qty: 0}}, Asks: []engine.Level{{Price: 100, Qty: 5}}},
				{PID: 8, Asks: []engine.Level{{Price: 200, Qty: 3}}},
			},
		}},
		{Line: &LineRec{T: 5, Products: []ProductRec{{PID: 1, Bids: []engine.Level{{Price: 1, Qty: 2}}}}}},
	}
}

func TestRecordRoundTrip(t *testing.T) {
	want := sampleRecords()
	var b []byte
	for _, r := range want {
		b = AppendRecord(b, r)
	}
	got, err := DecodeRecords(b)
	if err != nil {
		t.Fatal(err)
	}
	// Absent sides decode as nil, same as the literals above.
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestCanonicalTime(t *testing.T) {
	if got := CanonicalTime(1790241913543924); got != "2026-09-24T09:25:13.543924+00:00" {
		t.Fatal(got)
	}
	if (LineRec{T: 1790241913543924}).TimeRaw() != "2026-09-24T09:25:13.543924+00:00" {
		t.Fatal("canonical")
	}
	if (LineRec{Raw: "x"}).TimeRaw() != "x" {
		t.Fatal("raw")
	}
}

func TestCheckpointRoundTrip(t *testing.T) {
	want := Checkpoint{
		IDs: []string{"A", "B", "C"},
		Contracts: []ContractRec{
			{T: 10, PID: 0, Name: "n0", State: "ACTI"},
			{T: 20, PID: 2, Deleted: true, Name: "n2", State: "IACT"},
		},
		Books: []ProductRec{
			{PID: 0, Bids: []engine.Level{{Price: 5, Qty: 1}}, Asks: []engine.Level{{Price: 6, Qty: 2}}},
			{PID: 2, Bids: []engine.Level{{Price: -5, Qty: 1}}},
		},
	}
	got, err := DecodeCheckpoint(AppendCheckpoint(nil, want))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	e, err := DecodeCheckpoint(AppendCheckpoint(nil, Checkpoint{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(e.IDs)+len(e.Contracts)+len(e.Books) != 0 {
		t.Fatalf("empty: %+v", e)
	}
}

func TestDecodeRecordsTruncated(t *testing.T) {
	recs := sampleRecords()
	first := AppendRecord(nil, recs[2])
	b := AppendRecord(first, recs[1])
	for k := 0; k < len(b); k++ {
		if k == len(first) {
			continue
		}
		if _, err := DecodeRecords(b[:k]); k > 0 && err == nil {
			t.Fatalf("prefix %d decoded without error", k)
		}
	}
	if _, err := DecodeRecords([]byte{99}); err == nil {
		t.Fatal("unknown type accepted")
	}
	// A huge count must fail, not allocate.
	if _, err := DecodeCheckpoint([]byte{0xff, 0xff, 0xff, 0xff}); err == nil {
		t.Fatal("huge count accepted")
	}
	if _, err := DecodeRecords([]byte{3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff}); err == nil {
		t.Fatal("huge product count accepted")
	}
}

func TestBlockRoundTrip(t *testing.T) {
	h1 := Header{Kind: KindChanges, FirstTS: -5, LastLVLine: 42, ReadAt: 1, FlushedAt: 2}
	h2 := Header{Kind: KindCheckpoint, FirstTS: 9, LastLVLine: 43}
	p1 := []byte("hello")
	b := AppendBlock(nil, h1, p1)
	b = AppendBlock(b, h2, nil)
	r := bytes.NewReader(b)
	h, p, next, ok, err := ReadBlock(r, 0)
	if err != nil || !ok || h != h1 || !bytes.Equal(p, p1) || next != int64(HeaderSize+len(p1)) {
		t.Fatalf("block1: %+v %q %d %v %v", h, p, next, ok, err)
	}
	h, p, next2, ok, err := ReadBlock(r, next)
	if err != nil || !ok || h != h2 || len(p) != 0 || next2 != int64(len(b)) {
		t.Fatalf("block2: %+v %q %d %v %v", h, p, next2, ok, err)
	}
	if _, _, _, ok, err := ReadBlock(r, next2); ok || err != nil {
		t.Fatal("past end should be invalid, no error")
	}
}

func TestBlockInvalid(t *testing.T) {
	b := AppendBlock(nil, Header{Kind: KindChanges, FirstTS: 0x0102030405060708, LastLVLine: 3}, []byte("payload"))
	check := func(name string, x []byte) {
		t.Helper()
		_, _, _, ok, err := ReadBlock(bytes.NewReader(x), 0)
		if ok || err != nil {
			t.Fatalf("%s: ok=%v err=%v", name, ok, err)
		}
	}
	for _, k := range []int{0, 10, HeaderSize, len(b) - 1} {
		check("prefix", b[:k])
	}
	c := append([]byte(nil), b...)
	c[HeaderSize+2] ^= 1
	check("payload flip", c)
	c = append([]byte(nil), b...)
	c[13+1] ^= 1 // inside FirstTS
	check("firstts flip", c)
	c = append([]byte(nil), b...)
	c[0] ^= 1
	check("magic", c)
	// payload_len beyond the limit
	c = append([]byte(nil), b...)
	c[9], c[10], c[11], c[12] = 0, 0, 0, 0x10
	check("too long", c)
}
