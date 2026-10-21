package engine

import (
	"slices"
	"testing"

	"dmt-orderbook/internal/feed"
)

func contract(t int64, id, name, state string, deleted bool) feed.Contract {
	return feed.Contract{Time: t, ContractID: id, Name: name, State: state, Deleted: deleted}
}

func TestScopeFollowsHistory(t *testing.T) {
	e := New()
	e.ApplyContract(contract(10, "c1", "A", "ACTI", false))
	e.ApplyContract(contract(20, "c1", "A", "IACT", false))
	for _, tc := range []struct {
		at   int64
		want bool
	}{{9, false}, {10, true}, {19, true}, {20, false}} {
		if got := e.inScope("c1", tc.at); got != tc.want {
			t.Errorf("inScope at %d = %v, want %v", tc.at, got, tc.want)
		}
	}
}

func TestDeletedIsOutOfScope(t *testing.T) {
	e := New()
	e.ApplyContract(contract(10, "c1", "A", "ACTI", true))
	if e.inScope("c1", 10) {
		t.Error("deleted contract in scope")
	}
}

func TestNoArea46IsOutOfScope(t *testing.T) {
	e := New()
	e.ApplyContract(contract(10, "c1", "A", "", false))
	if e.inScope("c1", 10) {
		t.Error("contract without area 46 in scope")
	}
}

func TestUnknownContractOutOfScope(t *testing.T) {
	if New().inScope("nope", 10) {
		t.Error("unknown contract in scope")
	}
}

func TestHistoryOnlyGrowsOnChange(t *testing.T) {
	e := New()
	for i := int64(0); i < 3; i++ {
		e.ApplyContract(contract(10+i, "c1", "A", "ACTI", false))
	}
	if n := len(e.contracts["c1"]); n != 1 {
		t.Errorf("history length = %d, want 1", n)
	}
}

func TestIDsForName(t *testing.T) {
	e := New()
	if got := e.IDsForName("A", 10); len(got) != 0 {
		t.Errorf("no match: got %v", got)
	}
	e.ApplyContract(contract(1, "c1", "A", "ACTI", false))
	if got := e.IDsForName("A", 10); !slices.Equal(got, []string{"c1"}) {
		t.Errorf("one ACTI: got %v", got)
	}
	e.ApplyContract(contract(1, "c2", "A", "IACT", false))
	if got := e.IDsForName("A", 10); !slices.Equal(got, []string{"c1"}) {
		t.Errorf("ACTI and IACT: got %v", got)
	}
	e.ApplyContract(contract(2, "c2", "A", "ACTI", false))
	if got := e.IDsForName("A", 10); !slices.Equal(got, []string{"c1", "c2"}) {
		t.Errorf("both ACTI: got %v", got)
	}
	// a rename: c1 no longer carries "A" at t=30
	e.ApplyContract(contract(30, "c1", "B", "ACTI", false))
	if got := e.IDsForName("A", 30); !slices.Equal(got, []string{"c2"}) {
		t.Errorf("after rename: got %v", got)
	}
}
