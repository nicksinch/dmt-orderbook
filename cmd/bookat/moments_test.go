package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"dmt-orderbook/internal/feed"
	"dmt-orderbook/internal/store"
)

// at renders a timestamp on 2026-09-24 at 10:00:00 plus us microseconds.
func at(us int) string {
	return fmt.Sprintf("2026-09-24T10:00:%02d.%06d+00:00", us/1000000, us%1000000)
}

func contractLine(us int) string {
	return fmt.Sprintf(`{"timestamp_utc":%q,"body":{"contractId":"NX_1","contractName":"QH-1","dlvryAreaState":[{"dlvryAreaId":46,"state":"ACTI"}]}}`, at(us))
}

// deltaLine is a delta for NX_1 with the given bid and ask orders ("" = none).
func deltaLine(us int, bid, ask string) string {
	order := func(id, price, qty string) string {
		return fmt.Sprintf(`[{"orderId":%q,"price":%s,"qty":%s,"deleted":false}]`, id, price, qty)
	}
	buys, sells := "[]", "[]"
	if bid != "" {
		buys = order("B1", bid, "3")
	}
	if ask != "" {
		sells = order("S1", ask, "2")
	}
	return fmt.Sprintf(`{"timestamp_utc":%q,"stomp_headers":{"x-nps-snapshot":"false"},"body":[{"contractId":"NX_1","buyOrders":%s,"sellOrders":%s}]}`, at(us), buys, sells)
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

// run answers the moments over the given lines and returns the output.
func run(t *testing.T, lv, ct []string, moments string) string {
	t.Helper()
	ms, err := parseMoments(strings.NewReader(moments))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := runMoments(merger(lv, ct), ms, &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestParseMoments(t *testing.T) {
	in := "top 2026-09-24T10:04:12Z\n\nbook PH-1 2026-09-24T10:04:12.345Z\n"
	ms, err := parseMoments(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[1].Kind != "book" || ms[1].Name != "PH-1" || ms[0].Kind != "top" {
		t.Fatalf("got %+v", ms)
	}
	if ms[1].T-ms[0].T != 345000 {
		t.Errorf("T difference %d, want 345000", ms[1].T-ms[0].T)
	}
	for _, bad := range []string{
		"top 2026-09-24T10:00:02Z\ntop 2026-09-24T10:00:01Z\n", // unsorted
		"book X\n",                        // no T
		"peek 2026-09-24T10:00:00Z\n",     // unknown kind
		"top\n",                           // no T
		"top 2026-09-24T10:00:00Z x\n",    // too many args
		"book X 2026-09-24T10:00:00Z y\n", // too many args
	} {
		if _, err := parseMoments(strings.NewReader(bad)); err == nil {
			t.Errorf("no error for %q", bad)
		}
	}
}

func TestRunMomentsBook(t *testing.T) {
	got := run(t, []string{deltaLine(2000000, "100", "105")}, []string{contractLine(1000000)},
		"book QH-1 2026-09-24T10:00:05Z\n")
	want := `{"QH-1":{"bid":[[100,3]],"ask":[[105,2]]}}` + "\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRunMomentsTopNull(t *testing.T) {
	got := run(t, []string{deltaLine(2000000, "100", "")}, []string{contractLine(1000000)},
		"top 2026-09-24T10:00:05Z\n")
	want := `{"QH-1":{"bid":[100,3],"ask":null}}` + "\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRunMomentsEmptySidesAreArrays(t *testing.T) {
	got := run(t, nil, []string{contractLine(1000000)}, "book QH-1 2026-09-24T10:00:05Z\n")
	want := `{"QH-1":{"bid":[],"ask":[]}}` + "\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRunMomentsUnknownProduct(t *testing.T) {
	got := run(t, nil, []string{contractLine(1000000)}, "book NOPE 2026-09-24T10:00:05Z\n")
	if want := `{"NOPE":null}` + "\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRunMomentsExactTimestampIncludesLine(t *testing.T) {
	lv := []string{deltaLine(2000000, "100", ""), deltaLine(2000001, "101", "")}
	got := run(t, lv, []string{contractLine(1000000)}, "top 2026-09-24T10:00:02.000000Z\n")
	want := `{"QH-1":{"bid":[100,3],"ask":null}}` + "\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRunMomentsBeforeFirstAndAfterLast(t *testing.T) {
	lv := []string{deltaLine(2000000, "100", "")}
	ct := []string{contractLine(1000000)}
	got := run(t, lv, ct, "top 2026-09-24T09:59:00Z\ntop 2026-09-24T10:00:02Z\ntop 2026-09-24T11:00:00Z\n")
	want := "{}\n" + `{"QH-1":{"bid":[100,3],"ask":null}}` + "\n" + `{"QH-1":{"bid":[100,3],"ask":null}}` + "\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRunMomentsStats(t *testing.T) {
	ms, _ := parseMoments(strings.NewReader("top 2026-09-24T10:00:02Z\n"))
	var out bytes.Buffer
	s, err := runMoments(merger([]string{deltaLine(2000000, "100", ""), deltaLine(9000000, "101", "")}, []string{contractLine(1000000)}), ms, &out)
	if err != nil {
		t.Fatal(err)
	}
	if s.Localview != 1 || s.Contracts != 1 {
		t.Errorf("got %+v, want 1 and 1 (stops after the last moment)", s)
	}
}

func TestRunMomentsMergerErrorIsFatal(t *testing.T) {
	ms, _ := parseMoments(strings.NewReader("top 2026-09-24T10:00:05Z\n"))
	var out bytes.Buffer
	if _, err := runMoments(merger([]string{"not json"}, nil), ms, &out); err == nil {
		t.Error("want error")
	}
}

// The store must answer every moment exactly as replaying the input does.
func TestRunMomentsStoreMatchesInput(t *testing.T) {
	ct := []string{contractLine(1000)}
	lv := []string{
		deltaLine(2000, "100", ""),
		deltaLine(3000, "101", "110"),
		deltaLine(5000, "", "109"),
	}
	var moments strings.Builder
	for _, us := range []int{0, 500, 1000, 1500, 2000, 2500, 3000, 4000, 5000, 6000} {
		fmt.Fprintf(&moments, "book QH-1 %s\ntop %s\nbook NOPE %s\n", at(us), at(us), at(us))
	}
	want := run(t, lv, ct, moments.String())
	ms, err := parseMoments(strings.NewReader(moments.String()))
	if err != nil {
		t.Fatal(err)
	}
	for _, cp := range []int64{1, 1 << 20} {
		dir := filepath.Join(t.TempDir(), "s")
		o := store.DefaultOptions()
		o.CheckpointBytes = cp
		if _, err := store.Ingest(merger(lv, ct), dir, o, 0); err != nil {
			t.Fatal(err)
		}
		r, err := store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := runMomentsStore(r, ms, &out); err != nil {
			t.Fatal(err)
		}
		if out.String() != want {
			t.Errorf("checkpoint bytes %d:\n got %s\nwant %s", cp, out.String(), want)
		}
	}
}
