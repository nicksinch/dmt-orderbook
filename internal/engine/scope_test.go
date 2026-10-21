package engine

import (
	"slices"
	"testing"

	"dmt-orderbook/internal/feed"
)

func TestScopeHelpers(t *testing.T) {
	e := New()
	e.ApplyContract(feed.Contract{Time: 1, ContractID: "NX_2", Name: "QH-B", State: "ACTI"})
	e.ApplyContract(feed.Contract{Time: 2, ContractID: "NX_1", Name: "QH-A", State: "ACTI"})
	e.ApplyContract(feed.Contract{Time: 3, ContractID: "NX_3", Name: "QH-C", State: "IACT"})
	e.ApplyLocalview(delta(4, co("NX_2", []feed.Order{ord("1", 100, 5)}, nil)))
	e.ApplyContract(feed.Contract{Time: 10, ContractID: "NX_1", Name: "QH-A", State: "IACT"})

	if got := e.IDsInScope(5); !slices.Equal(got, []string{"NX_1", "NX_2"}) {
		t.Errorf("IDsInScope(5) = %v", got)
	}
	if got := e.IDsInScope(10); !slices.Equal(got, []string{"NX_2"}) {
		t.Errorf("IDsInScope(10) = %v", got)
	}
	if got := e.IDsInScope(0); len(got) != 0 {
		t.Errorf("IDsInScope(0) = %v", got)
	}

	for _, c := range []struct {
		id   string
		t    int64
		name string
		ok   bool
	}{
		{"NX_1", 5, "QH-A", true},
		{"NX_1", 10, "", false},
		{"NX_3", 5, "", false},
		{"NX_9", 5, "", false},
	} {
		if name, ok := e.NameInScope(c.id, c.t); name != c.name || ok != c.ok {
			t.Errorf("NameInScope(%s, %d) = %q, %v", c.id, c.t, name, ok)
		}
	}

	bids, asks := e.Levels("NX_2")
	if !slices.Equal(bids, []Level{{100, 5}}) || asks == nil || len(asks) != 0 {
		t.Errorf("Levels(NX_2) = %v, %v", bids, asks)
	}
	bids, asks = e.Levels("NX_9")
	if bids == nil || asks == nil || len(bids) != 0 || len(asks) != 0 {
		t.Errorf("Levels(NX_9) = %v, %v", bids, asks)
	}
}
