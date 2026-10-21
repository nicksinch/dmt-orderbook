package engine

import (
	"reflect"
	"testing"

	"dmt-orderbook/internal/feed"
)

func p(v int64) *int64 { return &v }

func ord(id string, price, qty int64) feed.Order {
	return feed.Order{OrderID: id, Price: p(price), Qty: p(qty)}
}

func del(id string) feed.Order { return feed.Order{OrderID: id, Deleted: true} }

func checkLevels(t *testing.T, name string, got, want []Level) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func TestAddNewOrders(t *testing.T) {
	b := newBook()
	b.applyOrder(ord("a", 100, 1), true, nil)
	b.applyOrder(ord("b", 102, 2), true, nil)
	b.applyOrder(ord("c", 105, 3), false, nil)
	checkLevels(t, "bids", b.Bids(), []Level{{102, 2}, {100, 1}})
	checkLevels(t, "asks", b.Asks(), []Level{{105, 3}})
}

func TestSamePriceAggregates(t *testing.T) {
	b := newBook()
	b.applyOrder(ord("a", 100, 3), true, nil)
	b.applyOrder(ord("b", 100, 4), true, nil)
	checkLevels(t, "bids", b.Bids(), []Level{{100, 7}})
}

func TestReplaceQty(t *testing.T) {
	b := newBook()
	b.applyOrder(ord("a", 100, 3), true, nil)
	b.applyOrder(ord("a", 100, 5), true, nil)
	checkLevels(t, "bids", b.Bids(), []Level{{100, 5}})
}

func TestMovePrice(t *testing.T) {
	b := newBook()
	b.applyOrder(ord("a", 100, 3), true, nil)
	b.applyOrder(ord("a", 101, 3), true, nil)
	checkLevels(t, "bids", b.Bids(), []Level{{101, 3}})
	if q := b.levelQty(true, 100); q != 0 {
		t.Errorf("level 100 qty = %d, want 0", q)
	}
}

func TestMoveSide(t *testing.T) {
	b := newBook()
	b.applyOrder(ord("a", 100, 3), true, nil)
	b.applyOrder(ord("a", 100, 3), false, nil)
	checkLevels(t, "bids", b.Bids(), nil)
	checkLevels(t, "asks", b.Asks(), []Level{{100, 3}})
}

func TestDeleteWithoutQty(t *testing.T) {
	b := newBook()
	b.applyOrder(ord("a", 100, 3), true, nil)
	b.applyOrder(del("a"), true, nil)
	checkLevels(t, "bids", b.Bids(), nil)
	if len(b.orders) != 0 {
		t.Errorf("orders = %v, want empty", b.orders)
	}
}

func TestDeleteUnknownIsNoop(t *testing.T) {
	b := newBook()
	b.applyOrder(ord("a", 100, 3), true, nil)
	b.applyOrder(del("zzz"), true, func(bool, int64, int64) { t.Error("touch called") })
	checkLevels(t, "bids", b.Bids(), []Level{{100, 3}})
}

func TestQtyZeroOrderAddsNothing(t *testing.T) {
	b := newBook()
	touch := func(bool, int64, int64) { t.Error("touch called for qty 0") }
	b.applyOrder(ord("a", 100, 0), true, touch)
	checkLevels(t, "bids", b.Bids(), nil)
	b.applyOrder(ord("a", 100, 5), true, nil)
	checkLevels(t, "bids", b.Bids(), []Level{{100, 5}})
}

func TestTouchReportsBefore(t *testing.T) {
	b := newBook()
	b.applyOrder(ord("a", 100, 3), true, nil)
	type call struct {
		buy           bool
		price, before int64
	}
	var calls []call
	touch := func(buy bool, price, before int64) { calls = append(calls, call{buy, price, before}) }
	b.applyOrder(ord("a", 100, 5), true, touch)
	want := []call{{true, 100, 3}, {true, 100, 0}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
}

func TestDiffLevels(t *testing.T) {
	got := diffLevels([]Level{{105, 1}, {103, 2}}, []Level{{105, 1}, {104, 5}}, true)
	checkLevels(t, "desc", got, []Level{{104, 5}, {103, 0}})

	got = diffLevels([]Level{{103, 2}, {105, 1}}, []Level{{104, 5}, {105, 1}}, false)
	checkLevels(t, "asc", got, []Level{{103, 0}, {104, 5}})

	got = diffLevels([]Level{{100, 1}}, []Level{{100, 2}}, false)
	checkLevels(t, "changed", got, []Level{{100, 2}})

	if got := diffLevels(nil, nil, true); len(got) != 0 {
		t.Errorf("empty diff = %v", got)
	}
}
