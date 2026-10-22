package main

import (
	"slices"
	"testing"
	"time"
)

func TestRawTimestamp(t *testing.T) {
	const ts = "2026-09-24T09:25:13.543924+00:00"
	for _, line := range []string{
		`{"timestamp_utc":"` + ts + `","topic":"x"}`,
		`{"timestamp_utc": "` + ts + `","topic":"x"}`,
	} {
		if got, ok := rawTimestamp([]byte(line)); !ok || got != ts {
			t.Errorf("rawTimestamp(%s) = %q, %v", line, got, ok)
		}
	}
	if got, ok := rawTimestamp([]byte(`{"topic":"x"}`)); ok || got != "" {
		t.Errorf("no key: got %q, %v", got, ok)
	}
}

func TestFollowT(t *testing.T) {
	const ts = "2026-09-24T10:00:00.510741+00:00"
	if got, ok := followT([]byte(`{"t":"` + ts + `","product":"PH-1","bid":[],"ask":[]}`)); !ok || got != ts {
		t.Errorf("followT = %q, %v", got, ok)
	}
	if _, ok := followT([]byte("not json")); ok {
		t.Error("followT accepted not json")
	}
}

func TestBurstFlags(t *testing.T) {
	got := burstFlags([]int64{1_000_000, 1_100_000, 1_200_000, 2_000_000, 2_500_000}, 2)
	want := []bool{true, true, true, false, false}
	if !slices.Equal(got, want) {
		t.Errorf("burstFlags = %v, want %v", got, want)
	}
}

func TestSummarize(t *testing.T) {
	var d []time.Duration
	for i := 100; i >= 1; i-- {
		d = append(d, time.Duration(i)*time.Millisecond)
	}
	ms := time.Millisecond
	want := Summary{Count: 100, P50: 50 * ms, P90: 90 * ms, P99: 99 * ms, P999: 100 * ms, Max: 100 * ms}
	if got := summarize(d); got != want {
		t.Errorf("summarize = %+v, want %+v", got, want)
	}
	if got := summarize(nil); got != (Summary{}) {
		t.Errorf("summarize(nil) = %+v", got)
	}
}

func TestMatchIgnoresUnknownT(t *testing.T) {
	index := map[string]int{"A": 1}
	firstSeen := map[int]time.Time{}
	t1 := time.Unix(1, 0)
	t2 := time.Unix(2, 0)
	match(index, firstSeen, "A", t1)
	match(index, firstSeen, "A", t2)
	if !firstSeen[1].Equal(t1) {
		t.Errorf("firstSeen[1] = %v, want %v", firstSeen[1], t1)
	}
	match(index, firstSeen, "2026-09-24T10:00:00Z", time.Unix(3, 0))
	if len(firstSeen) != 1 {
		t.Errorf("len(firstSeen) = %d, want 1", len(firstSeen))
	}
}
