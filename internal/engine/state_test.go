package engine

import (
	"reflect"
	"testing"

	"dmt-orderbook/internal/feed"
)

func TestApplyContractReportsChange(t *testing.T) {
	e := New()
	if !e.ApplyContract(contract(10, "c1", "A", "ACTI", false)) {
		t.Error("first line: got false, want true")
	}
	if e.ApplyContract(contract(11, "c1", "A", "ACTI", false)) {
		t.Error("same name/state/deleted: got true, want false")
	}
	if !e.ApplyContract(contract(12, "c1", "A", "IACT", false)) {
		t.Error("ACTI to IACT: got false, want true")
	}
}

func TestApplyChangesSetsLevels(t *testing.T) {
	e := New()
	e.ApplyChanges(10, []ProductChange{{ContractID: "A", Bids: lv(Level{100, 5}, Level{102, 1}), Asks: lv(Level{110, 3})}})
	e.ApplyChanges(20, []ProductChange{{ContractID: "A", Bids: lv(Level{102, 0}, Level{101, 2}), Asks: lv(Level{110, 4})}})
	s := e.State()
	want := []BookLevels{{ID: "A", Bids: lv(Level{101, 2}, Level{100, 5}), Asks: lv(Level{110, 4})}}
	if !reflect.DeepEqual(s.Books, want) {
		t.Errorf("books = %+v, want %+v", s.Books, want)
	}
	if s.Now != 20 {
		t.Errorf("now = %d, want 20", s.Now)
	}
}

// stateLines is a mixed sequence: two contracts opening, deltas with a move, a
// delete and a level touched twice, then a snapshot listing only B.
func stateLines() []any {
	return []any{
		contract(1, "A", "OLD-A", "ACTI", false),
		contract(2, "A", "NAME-A", "ACTI", false), // renamed: only NAME-A resolves
		contract(2, "B", "NAME-B", "ACTI", false),
		delta(10,
			co("A", []feed.Order{ord("1", 100, 5), ord("2", 101, 2)}, []feed.Order{ord("3", 110, 3)}),
			co("B", []feed.Order{ord("4", 50, 7)}, nil)),
		delta(11,
			co("A", []feed.Order{ord("1", 99, 5), del("2"), ord("5", 100, 1), ord("6", 100, 2)}, nil)), // move, delete, 100 touched twice
		delta(12, co("B", nil, []feed.Order{ord("7", 60, 1)})),
		snapshot(13, co("B", []feed.Order{ord("8", 55, 4)}, nil)),
	}
}

func applyLine(e *Engine, line any) []ProductChange {
	switch l := line.(type) {
	case feed.Contract:
		e.ApplyContract(l)
	case feed.Localview:
		return e.ApplyLocalview(l)
	}
	return nil
}

func TestApplyChangesMatchesApplyLocalview(t *testing.T) {
	x, y := New(), New()
	for i, line := range stateLines() {
		changes := applyLine(x, line)
		switch l := line.(type) {
		case feed.Contract:
			y.ApplyContract(l)
		case feed.Localview:
			y.ApplyChanges(l.Time, changes)
		}
		if !reflect.DeepEqual(x.State(), y.State()) {
			t.Fatalf("line %d: X state %+v, Y state %+v", i, x.State(), y.State())
		}
	}
}

func TestRestoreRoundTrip(t *testing.T) {
	x := New()
	for _, line := range stateLines() {
		applyLine(x, line)
	}
	y := Restore(x.State())
	if !reflect.DeepEqual(x.State(), y.State()) {
		t.Fatalf("X state %+v, Y state %+v", x.State(), y.State())
	}
	for _, at := range []int64{x.State().Now, x.State().Now + 1_000_000} {
		for _, name := range []string{"NAME-A", "NAME-B", "OLD-A"} {
			xb, xok, xerr := x.BookByName(name, at)
			yb, yok, yerr := y.BookByName(name, at)
			if !reflect.DeepEqual(xb, yb) || xok != yok || (xerr == nil) != (yerr == nil) {
				t.Errorf("BookByName(%q, %d): X %+v %v %v, Y %+v %v %v", name, at, xb, xok, xerr, yb, yok, yerr)
			}
			if name == "OLD-A" && yok {
				t.Error("renamed name still resolves after restore")
			}
		}
		xt, xerr := x.Top(at)
		yt, yerr := y.Top(at)
		if !reflect.DeepEqual(xt, yt) || (xerr == nil) != (yerr == nil) {
			t.Errorf("Top(%d): X %+v %v, Y %+v %v", at, xt, xerr, yt, yerr)
		}
	}
}

func TestStateDoesNotAliasEngine(t *testing.T) {
	e := New()
	e.ApplyChanges(1, []ProductChange{{ContractID: "A", Bids: lv(Level{100, 5})}})
	s := e.State()
	s.Books[0].Bids[0].Qty = 99
	if got := e.State().Books[0].Bids[0].Qty; got != 5 {
		t.Errorf("engine book changed through State: qty %d", got)
	}
}
