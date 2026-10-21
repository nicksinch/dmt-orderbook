package output

import (
	"bytes"
	"testing"

	"dmt-orderbook/internal/engine"
	"dmt-orderbook/internal/feed"
)

func encode(t *testing.T, v any, err error) string {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := NewEncoder(&buf).Encode(v); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func testEngine() *engine.Engine {
	e := engine.New()
	e.ApplyContract(feed.Contract{Time: 1, ContractID: "NX_2", Name: "QH-B", State: "ACTI"})
	e.ApplyContract(feed.Contract{Time: 2, ContractID: "NX_1", Name: "QH-A", State: "ACTI"})
	e.ApplyContract(feed.Contract{Time: 3, ContractID: "NX_3", Name: "QH-C", State: "IACT"})
	p, q := int64(100), int64(5)
	e.ApplyLocalview(feed.Localview{Time: 4, Body: []feed.ContractOrders{
		{ContractID: "NX_2", BuyOrders: []feed.Order{{OrderID: "1", Price: &p, Qty: &q}}},
	}})
	return e
}

func TestReplayLineJSON(t *testing.T) {
	l := NewReplayLine("2026-09-24T10:00:00Z", "QH-1", nil, []engine.Level{{Price: 110}})
	got := encode(t, l, nil)
	want := `{"t":"2026-09-24T10:00:00Z","product":"QH-1","bid":[],"ask":[[110,0]]}` + "\n"
	if got != want {
		t.Errorf("got %s want %s", got, want)
	}
}

func TestBookAndTopJSON(t *testing.T) {
	e := testEngine()
	v, err := Book(e, "QH-B", 5)
	if got, want := encode(t, v, err), `{"QH-B":{"bid":[[100,5]],"ask":[]}}`+"\n"; got != want {
		t.Errorf("got %s want %s", got, want)
	}
	v, err = Book(e, "QH-X", 5)
	if got, want := encode(t, v, err), `{"QH-X":null}`+"\n"; got != want {
		t.Errorf("got %s want %s", got, want)
	}
	v, err = Top(e, 5)
	if got, want := encode(t, v, err), `{"QH-A":{"bid":null,"ask":null},"QH-B":{"bid":[100,5],"ask":null}}`+"\n"; got != want {
		t.Errorf("got %s want %s", got, want)
	}
	v, err = Top(engine.New(), 5)
	if got, want := encode(t, v, err), `{}`+"\n"; got != want {
		t.Errorf("got %s want %s", got, want)
	}
}
