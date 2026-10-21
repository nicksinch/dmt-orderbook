package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dmt-orderbook/internal/feed"
	"dmt-orderbook/internal/store"
)

func at(us int) string {
	return fmt.Sprintf("2026-09-24T10:00:%02d.%06d+00:00", us/1000000, us%1000000)
}

func atT(t *testing.T, us int) int64 {
	t.Helper()
	v, err := feed.ParseTime(at(us))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func ct(us int, id, name, state string) string {
	return fmt.Sprintf(`{"timestamp_utc":%q,"body":{"contractId":%q,"contractName":%q,"dlvryAreaState":[{"dlvryAreaId":46,"state":%q}]}}`, at(us), id, name, state)
}

func order(side, id string, price, qty int, deleted bool) string {
	return fmt.Sprintf(`"%sOrders":[{"orderId":%q,"price":%d,"qty":%d,"deleted":%t}]`, side, id, price, qty, deleted)
}

// contract builds one body entry; buy and sell are order fragments or "".
func contract(id, buy, sell string) string {
	var parts []string
	for _, p := range []string{buy, sell} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return fmt.Sprintf(`{"contractId":%q,%s}`, id, strings.Join(parts, ","))
}

func lv(ts string, snapshot bool, body string) string {
	return fmt.Sprintf(`{"timestamp_utc":%q,"stomp_headers":{"x-nps-snapshot":"%t"},"body":[%s]}`, ts, snapshot, body)
}

func buildStore(t *testing.T) *store.Reader {
	t.Helper()
	cts := []string{
		ct(0, "NX_1", "QH-1", "ACTI"),
		ct(1, "NX_2", "QH-2", "ACTI"),
		ct(45, "NX_3", "QH-0", "ACTI"),
		ct(60, "NX_1", "QH-1", "IACT"),
	}
	lvs := []string{
		lv(at(10), false, contract("NX_1", order("buy", "a", 100, 5, false), order("sell", "s", 110, 2, false))),
		lv(at(15), false, contract("NX_3", order("buy", "z", 300, 1, false), "")),
		lv(at(20), false, contract("NX_2", order("buy", "b", 200, 3, false), "")),
		lv(at(40), true, contract("NX_2", `"buyOrders":[{"orderId":"b","price":200,"qty":3,"deleted":false},{"orderId":"f","price":201,"qty":1,"deleted":false}]`, "")+","+
			contract("NX_1", order("buy", "a", 100, 5, false), "")),
		lv(at(42), false, contract("NX_3", order("buy", "y", 301, 2, false), "")),
		lv(at(50), false, contract("NX_1", order("buy", "a", 100, 0, true), "")),
		lv(at(70), false, contract("NX_1", order("buy", "c", 99, 1, false), "")),
		lv(at(80), false, contract("NX_2", "", order("sell", "t", 205, 4, false))),
		lv("2026-09-24T10:00:00.00009Z", false, contract("NX_2", order("buy", "g", 202, 1, false), "")),
	}
	join := func(l []string) *strings.Reader { return strings.NewReader(strings.Join(l, "\n") + "\n") }
	m := feed.NewMerger(join(lvs), join(cts), "localview.jsonl", "contracts.jsonl")
	dir := filepath.Join(t.TempDir(), "s")
	if _, err := store.Ingest(m, dir, store.DefaultOptions(), 0); err != nil {
		t.Fatal(err)
	}
	r, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestParseArgs(t *testing.T) {
	cases := []struct {
		in    []string
		cmd   string
		pos   []string
		store string
	}{
		{[]string{"status", "--store", "d"}, "status", []string{}, "d"},
		{[]string{"book", "--store=d", "QH-1", "T"}, "book", []string{"QH-1", "T"}, "d"},
		{[]string{"top", "T"}, "top", []string{"T"}, "store"},
		{[]string{"book", "QH-1", "T", "--store", "d"}, "book", []string{"QH-1", "T"}, "d"},
	}
	for _, c := range cases {
		cmd, pos, dir, err := parseArgs(c.in)
		if err != nil || cmd != c.cmd || dir != c.store || !reflect.DeepEqual(append([]string{}, pos...), c.pos) {
			t.Errorf("%v: got %q %v %q %v", c.in, cmd, pos, dir, err)
		}
	}
	if _, _, _, err := parseArgs([]string{"replay", "--store"}); err == nil {
		t.Error("want error for --store without value")
	}
}

func replayOut(t *testing.T, r *store.Reader, fromRaw string, to int64) string {
	t.Helper()
	from, err := feed.ParseTime(fromRaw)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := runReplay(r, fromRaw, from, to, false, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func lines(s ...string) string { return strings.Join(s, "\n") + "\n" }

func TestReplayOutput(t *testing.T) {
	r := buildStore(t)
	got := replayOut(t, r, "2026-09-24T10:00:00Z", atT(t, 80))
	want := lines(
		`{"t":"2026-09-24T10:00:00Z","product":"QH-1","bid":[],"ask":[]}`,
		`{"t":"2026-09-24T10:00:00.000001+00:00","product":"QH-2","bid":[],"ask":[]}`,
		`{"t":"2026-09-24T10:00:00.000010+00:00","product":"QH-1","bid":[[100,5]],"ask":[[110,2]]}`,
		`{"t":"2026-09-24T10:00:00.000020+00:00","product":"QH-2","bid":[[200,3]],"ask":[]}`,
		`{"t":"2026-09-24T10:00:00.000040+00:00","product":"QH-1","bid":[],"ask":[[110,0]]}`,
		`{"t":"2026-09-24T10:00:00.000040+00:00","product":"QH-2","bid":[[201,1]],"ask":[]}`,
		`{"t":"2026-09-24T10:00:00.000045+00:00","product":"QH-0","bid":[[301,2]],"ask":[]}`,
		`{"t":"2026-09-24T10:00:00.000050+00:00","product":"QH-1","bid":[[100,0]],"ask":[]}`,
		`{"t":"2026-09-24T10:00:00.000080+00:00","product":"QH-2","bid":[],"ask":[[205,4]]}`,
	)
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestReplayBoundaries(t *testing.T) {
	r := buildStore(t)
	got := replayOut(t, r, "2026-09-24T10:00:00.00004Z", atT(t, 50))
	want := lines(
		`{"t":"2026-09-24T10:00:00.00004Z","product":"QH-1","bid":[[100,5]],"ask":[]}`,
		`{"t":"2026-09-24T10:00:00.00004Z","product":"QH-2","bid":[[201,1],[200,3]],"ask":[]}`,
		`{"t":"2026-09-24T10:00:00.000045+00:00","product":"QH-0","bid":[[301,2]],"ask":[]}`,
		`{"t":"2026-09-24T10:00:00.000050+00:00","product":"QH-1","bid":[[100,0]],"ask":[]}`,
	)
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	got = replayOut(t, r, "2026-09-24T10:00:00.00008Z", atT(t, 90))
	want = lines(
		`{"t":"2026-09-24T10:00:00.00008Z","product":"QH-0","bid":[[301,2]],"ask":[]}`,
		`{"t":"2026-09-24T10:00:00.00008Z","product":"QH-2","bid":[[201,1],[200,3]],"ask":[[205,4]]}`,
		`{"t":"2026-09-24T10:00:00.00009Z","product":"QH-2","bid":[[202,1]],"ask":[]}`,
	)
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunUsageErrors(t *testing.T) {
	cases := [][]string{
		{"book", "QH-1", "not-a-time"},
		{"top", "not-a-time"},
		{"replay", "not-a-time", "2026-09-24T10:00:00Z"},
		{"replay", "2026-09-24T10:00:00Z", "not-a-time"},
		{"replay", "2026-09-24T10:00:05Z", "2026-09-24T10:00:00Z"},
		{"book", "QH-1"},
		{"top"},
		{"status", "extra"},
		{"nonsense"},
		{},
	}
	for _, args := range cases {
		var ue usageError
		if err := run(args, io.Discard); !errors.As(err, &ue) {
			t.Errorf("%v: got %v, want a usage error", args, err)
		}
	}
}
