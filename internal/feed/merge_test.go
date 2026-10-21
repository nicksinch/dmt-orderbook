package feed

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// ts renders microsecond t (seconds after a fixed base) in the feed's raw form.
func ts(t int) string {
	base := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	return base.Add(time.Duration(t)*time.Second).Format("2006-01-02T15:04:05.000000") + "+00:00"
}

func lvAt(t int) string {
	return fmt.Sprintf(`{"timestamp_utc":%q,"stomp_headers":{"x-nps-snapshot":"false"},"body":[]}`, ts(t))
}

func ctAt(t int) string {
	return fmt.Sprintf(`{"timestamp_utc":%q,"body":{"contractId":"NX_1","contractName":"QH-1","dlvryAreaState":[{"dlvryAreaId":46,"state":"ACTI"}]}}`, ts(t))
}

func newTestMerger(lv, ct []string) *Merger {
	join := func(l []string) *strings.Reader {
		if len(l) == 0 {
			return strings.NewReader("")
		}
		return strings.NewReader(strings.Join(l, "\n") + "\n")
	}
	return NewMerger(join(lv), join(ct), "localview.jsonl", "contracts.jsonl")
}

func TestMergeOrdersByTime(t *testing.T) {
	m := newTestMerger([]string{lvAt(1), lvAt(3)}, []string{ctAt(2), ctAt(4)})
	wantSrc := []Source{SourceLocalview, SourceContracts, SourceLocalview, SourceContracts}
	wantLine := []int{1, 1, 2, 2}
	for i := range wantSrc {
		r, err := m.Next()
		if err != nil {
			t.Fatal(err)
		}
		if r.Source != wantSrc[i] || r.Line != wantLine[i] {
			t.Fatalf("record %d: source %v line %d", i, r.Source, r.Line)
		}
		if (r.Source == SourceLocalview) != (r.Localview != nil) || (r.Source == SourceContracts) != (r.Contract != nil) {
			t.Fatalf("record %d: payload pointers wrong", i)
		}
	}
	if _, err := m.Next(); err != io.EOF {
		t.Fatalf("want io.EOF, got %v", err)
	}
}

func TestMergeTieContractsFirst(t *testing.T) {
	m := newTestMerger([]string{lvAt(5)}, []string{ctAt(5)})
	r, err := m.Next()
	if err != nil || r.Source != SourceContracts {
		t.Fatalf("first: %+v err=%v", r, err)
	}
	r, err = m.Next()
	if err != nil || r.Source != SourceLocalview {
		t.Fatalf("second: %+v err=%v", r, err)
	}
}

func TestMergeBackwardsTimeIsError(t *testing.T) {
	m := newTestMerger([]string{lvAt(2), lvAt(1)}, nil)
	var err error
	for err == nil {
		_, err = m.Next()
	}
	if err == io.EOF || !strings.Contains(err.Error(), "localview.jsonl:2") {
		t.Fatalf("got %v", err)
	}
}

func TestMergeDecodeErrorNamesLine(t *testing.T) {
	m := newTestMerger(nil, []string{ctAt(1), ctAt(2), `{}`})
	var err error
	for err == nil {
		_, err = m.Next()
	}
	if err == io.EOF || !strings.Contains(err.Error(), "contracts.jsonl:3") {
		t.Fatalf("got %v", err)
	}
}

func TestMergeOneFileEmpty(t *testing.T) {
	m := newTestMerger([]string{lvAt(1), lvAt(2)}, nil)
	for i := 1; i <= 2; i++ {
		r, err := m.Next()
		if err != nil || r.Source != SourceLocalview || r.Line != i {
			t.Fatalf("record %d: %+v err=%v", i, r, err)
		}
	}
	if _, err := m.Next(); err != io.EOF {
		t.Fatalf("want io.EOF, got %v", err)
	}
}
