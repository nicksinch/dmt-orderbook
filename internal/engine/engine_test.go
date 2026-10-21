package engine

import (
	"slices"
	"testing"

	"dmt-orderbook/internal/feed"
)

func co(id string, buys, sells []feed.Order) feed.ContractOrders {
	return feed.ContractOrders{ContractID: id, BuyOrders: buys, SellOrders: sells}
}

func delta(t int64, body ...feed.ContractOrders) feed.Localview {
	return feed.Localview{Time: t, Body: body}
}

func snapshot(t int64, body ...feed.ContractOrders) feed.Localview {
	return feed.Localview{Time: t, Snapshot: true, Body: body}
}

func lv(l ...Level) []Level { return l }

// sameChange compares changes treating nil and empty level slices as equal.
func sameChange(t *testing.T, got, want []ProductChange) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d changes %+v, want %d %+v", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i].ContractID != want[i].ContractID ||
			!slices.Equal(got[i].Bids, want[i].Bids) || !slices.Equal(got[i].Asks, want[i].Asks) {
			t.Errorf("change %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func actEngine(ids ...string) *Engine {
	e := New()
	for _, id := range ids {
		e.ApplyContract(feed.Contract{Time: 1, ContractID: id, Name: "N-" + id, State: "ACTI"})
	}
	return e
}

func TestDeltaReportsChangedLevels(t *testing.T) {
	e := New()
	got := e.ApplyLocalview(delta(10, co("A", []feed.Order{ord("1", 100, 3), ord("2", 101, 2)}, nil)))
	sameChange(t, got, []ProductChange{{ContractID: "A", Bids: lv(Level{101, 2}, Level{100, 3})}})
}

func TestTouchedTwiceNetChangeOnly(t *testing.T) {
	e := New()
	got := e.ApplyLocalview(delta(10, co("A", []feed.Order{ord("1", 100, 3), ord("2", 100, 4)}, nil)))
	sameChange(t, got, []ProductChange{{ContractID: "A", Bids: lv(Level{100, 7})}})
}

func TestChangeAndRevertReportsNothing(t *testing.T) {
	e := New()
	e.ApplyLocalview(delta(10, co("A", []feed.Order{ord("X", 100, 3)}, nil)))
	got := e.ApplyLocalview(delta(11, co("A", []feed.Order{del("X"), ord("Y", 100, 3)}, nil)))
	sameChange(t, got, nil)
}

func TestResendSameOrderReportsNothing(t *testing.T) {
	e := New()
	e.ApplyLocalview(delta(10, co("A", []feed.Order{ord("X", 100, 3)}, nil)))
	got := e.ApplyLocalview(delta(11, co("A", []feed.Order{ord("X", 100, 3)}, nil)))
	sameChange(t, got, nil)
}

func TestDeltaMultipleContractsInBodyOrder(t *testing.T) {
	e := New()
	got := e.ApplyLocalview(delta(10,
		co("B", nil, []feed.Order{ord("1", 200, 1)}),
		co("A", []feed.Order{ord("2", 100, 1)}, nil)))
	sameChange(t, got, []ProductChange{
		{ContractID: "B", Asks: lv(Level{200, 1})},
		{ContractID: "A", Bids: lv(Level{100, 1})},
	})
}

func TestSnapshotReplaces(t *testing.T) {
	e := New()
	e.ApplyLocalview(delta(10,
		co("A", []feed.Order{ord("1", 100, 3)}, nil),
		co("B", nil, []feed.Order{ord("2", 200, 1)})))
	got := e.ApplyLocalview(snapshot(11, co("A", []feed.Order{ord("3", 101, 5)}, nil)))
	sameChange(t, got, []ProductChange{
		{ContractID: "A", Bids: lv(Level{101, 5}, Level{100, 0})},
		{ContractID: "B", Asks: lv(Level{200, 0})},
	})
	if b := e.books["B"]; b != nil && len(b.Asks()) != 0 {
		t.Errorf("B should be empty, asks %v", b.Asks())
	}
}

func TestSnapshotEmptyBodyClearsAll(t *testing.T) {
	e := New()
	e.ApplyLocalview(delta(10,
		co("A", []feed.Order{ord("1", 100, 3)}, []feed.Order{ord("2", 105, 1)}),
		co("B", nil, []feed.Order{ord("3", 200, 1)})))
	got := e.ApplyLocalview(snapshot(11))
	sameChange(t, got, []ProductChange{
		{ContractID: "A", Bids: lv(Level{100, 0}), Asks: lv(Level{105, 0})},
		{ContractID: "B", Asks: lv(Level{200, 0})},
	})
	for id, b := range e.books {
		if len(b.Bids()) != 0 || len(b.Asks()) != 0 {
			t.Errorf("book %s not empty", id)
		}
	}
}

func TestSnapshotSameContractTwice(t *testing.T) {
	e := New()
	got := e.ApplyLocalview(snapshot(10,
		co("A", []feed.Order{ord("1", 100, 3)}, nil),
		co("A", nil, []feed.Order{ord("2", 105, 1)})))
	sameChange(t, got, []ProductChange{
		{ContractID: "A", Bids: lv(Level{100, 3}), Asks: lv(Level{105, 1})},
	})
	b := e.books["A"]
	if len(b.Bids()) != 1 || len(b.Asks()) != 1 {
		t.Errorf("want both sides in one book, got %v %v", b.Bids(), b.Asks())
	}
}

func TestSnapshotUnchangedReportsNothing(t *testing.T) {
	e := New()
	body := []feed.ContractOrders{
		co("A", []feed.Order{ord("1", 100, 3)}, []feed.Order{ord("2", 105, 1)}),
		co("B", nil, []feed.Order{ord("3", 200, 1)}),
	}
	e.ApplyLocalview(delta(10, body...))
	got := e.ApplyLocalview(snapshot(11, body...))
	sameChange(t, got, nil)
}

func TestOrdersKeptBeforeContractKnown(t *testing.T) {
	e := New()
	e.ApplyLocalview(delta(10, co("A", []feed.Order{ord("1", 100, 3)}, nil)))
	if got, err := e.Top(10); err != nil || len(got) != 0 {
		t.Fatalf("unknown contract listed: %+v err=%v", got, err)
	}
	e.ApplyContract(feed.Contract{Time: 20, ContractID: "A", Name: "QH-1", State: "ACTI"})
	got, err := e.Top(20)
	if err != nil || len(got) != 1 || got[0].Name != "QH-1" || got[0].Bid == nil || *got[0].Bid != (Level{100, 3}) || got[0].Ask != nil {
		t.Fatalf("got %+v", got)
	}
}

func TestBookByName(t *testing.T) {
	e := New()
	e.ApplyContract(feed.Contract{Time: 1, ContractID: "A", Name: "QH-1", State: "ACTI"})
	e.ApplyLocalview(delta(10, co("A", []feed.Order{ord("1", 100, 3)}, nil)))

	pb, ok, err := e.BookByName("QH-1", 10)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if pb.Name != "QH-1" || !slices.Equal(pb.Bids, lv(Level{100, 3})) {
		t.Errorf("got %+v", pb)
	}
	if pb.Asks == nil || len(pb.Asks) != 0 {
		t.Errorf("asks must be non-nil empty, got %#v", pb.Asks)
	}

	if _, ok, err := e.BookByName("nope", 10); ok || err != nil {
		t.Errorf("unknown name: ok=%v err=%v", ok, err)
	}

	// An in-scope contract with no book still gives non-nil empty sides.
	e.ApplyContract(feed.Contract{Time: 1, ContractID: "C", Name: "QH-3", State: "ACTI"})
	pb, ok, err = e.BookByName("QH-3", 10)
	if err != nil || !ok || pb.Bids == nil || pb.Asks == nil {
		t.Errorf("no-book contract: %+v ok=%v err=%v", pb, ok, err)
	}

	e.ApplyContract(feed.Contract{Time: 1, ContractID: "B", Name: "QH-1", State: "ACTI"})
	if _, _, err := e.BookByName("QH-1", 10); err == nil {
		t.Error("want error for two IDs with one name")
	}
}

func TestTopSortedAndNulls(t *testing.T) {
	e := actEngine("A", "B")
	e.ApplyLocalview(delta(10,
		co("B", []feed.Order{ord("1", 100, 3), ord("2", 99, 1)}, []feed.Order{ord("3", 110, 2), ord("4", 111, 2)}),
		co("A", []feed.Order{ord("5", 50, 1)}, nil)))
	got, err := e.Top(10)
	if err != nil || len(got) != 2 || got[0].Name != "N-A" || got[1].Name != "N-B" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Ask != nil || got[0].Bid == nil || *got[0].Bid != (Level{50, 1}) {
		t.Errorf("A: %+v", got[0])
	}
	if *got[1].Bid != (Level{100, 3}) || *got[1].Ask != (Level{110, 2}) {
		t.Errorf("B: bid %v ask %v", *got[1].Bid, *got[1].Ask)
	}
}

func TestTopExcludesOutOfScope(t *testing.T) {
	e := New()
	e.ApplyContract(feed.Contract{Time: 1, ContractID: "A", Name: "QH-1", State: "IACT"})
	e.ApplyLocalview(delta(10, co("A", []feed.Order{ord("1", 100, 3)}, nil)))
	if got, err := e.Top(10); err != nil || len(got) != 0 {
		t.Errorf("got %+v err=%v", got, err)
	}
}

func TestQueryBeforeLastRecordIsError(t *testing.T) {
	e := New()
	e.ApplyContract(feed.Contract{Time: 10, ContractID: "A", Name: "QH-1", State: "ACTI"})
	e.ApplyLocalview(delta(20, co("A", []feed.Order{ord("1", 100, 3)}, nil)))
	if _, _, err := e.BookByName("QH-1", 10); err == nil {
		t.Error("BookByName before the last record: want error")
	}
	if _, err := e.Top(10); err == nil {
		t.Error("Top before the last record: want error")
	}
	for _, at := range []int64{20, 25} {
		if _, ok, err := e.BookByName("QH-1", at); err != nil || !ok {
			t.Errorf("BookByName at %d: ok=%v err=%v", at, ok, err)
		}
		if got, err := e.Top(at); err != nil || len(got) != 1 {
			t.Errorf("Top at %d: %+v err=%v", at, got, err)
		}
	}
}

func TestTopAmbiguousNameIsError(t *testing.T) {
	e := New()
	e.ApplyContract(feed.Contract{Time: 1, ContractID: "A", Name: "QH-1", State: "ACTI"})
	e.ApplyContract(feed.Contract{Time: 1, ContractID: "B", Name: "QH-1", State: "ACTI"})
	if got, err := e.Top(10); err == nil {
		t.Errorf("want error, got %+v", got)
	}
}

func TestSnapshotChangeOrder(t *testing.T) {
	e := New()
	e.ApplyLocalview(delta(10,
		co("A", []feed.Order{ord("1", 100, 1)}, nil),
		co("B", []feed.Order{ord("2", 100, 1)}, nil),
		co("C", []feed.Order{ord("3", 100, 1)}, nil),
		co("D", []feed.Order{ord("4", 100, 1)}, nil)))
	got := e.ApplyLocalview(snapshot(11,
		co("C", []feed.Order{ord("5", 101, 1)}, nil),
		co("A", []feed.Order{ord("6", 102, 1)}, nil)))
	sameChange(t, got, []ProductChange{
		{ContractID: "C", Bids: lv(Level{101, 1}, Level{100, 0})},
		{ContractID: "A", Bids: lv(Level{102, 1}, Level{100, 0})},
		{ContractID: "B", Bids: lv(Level{100, 0})},
		{ContractID: "D", Bids: lv(Level{100, 0})},
	})
}
