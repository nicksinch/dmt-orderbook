package feed

import "testing"

const lvTime = "2026-09-24T09:25:13.543924+00:00"

func lvLine(snapshotHeader, body string) []byte {
	return []byte(`{"timestamp_utc":"` + lvTime + `","stomp_headers":{` + snapshotHeader + `"x-nps-sequenceNo":"1"},"body":` + body + `}`)
}

func TestDecodeLocalviewDelta(t *testing.T) {
	line := lvLine(`"x-nps-snapshot":"false",`,
		`[{"revisionNo":5,"contractId":"NX_1","buyOrders":[{"orderId":"A","price":19102,"qty":4800,"deleted":false}],"sellOrders":[{"orderId":"B","deleted":true}]}]`)
	lv, err := DecodeLocalview(line)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := ParseTime(lvTime)
	if lv.Snapshot || lv.Time != want || lv.TimeRaw != lvTime {
		t.Fatalf("header fields wrong: %+v", lv)
	}
	if len(lv.Body) != 1 || lv.Body[0].ContractID != "NX_1" {
		t.Fatalf("body wrong: %+v", lv.Body)
	}
	b := lv.Body[0].BuyOrders[0]
	if b.OrderID != "A" || b.Price == nil || *b.Price != 19102 || b.Qty == nil || *b.Qty != 4800 || b.Deleted {
		t.Fatalf("buy order wrong: %+v", b)
	}
	s := lv.Body[0].SellOrders[0]
	if !s.Deleted || s.Qty != nil || s.Price != nil {
		t.Fatalf("sell order wrong: %+v", s)
	}
}

func TestDecodeLocalviewSnapshotEmptyBody(t *testing.T) {
	lv, err := DecodeLocalview(lvLine(`"x-nps-snapshot":"true",`, `[]`))
	if err != nil {
		t.Fatal(err)
	}
	if !lv.Snapshot || lv.Body == nil || len(lv.Body) != 0 {
		t.Fatalf("got %+v", lv)
	}
}

func TestDecodeLocalviewMissingHeaderIsDelta(t *testing.T) {
	lv, err := DecodeLocalview(lvLine(``, `[]`))
	if err != nil {
		t.Fatal(err)
	}
	if lv.Snapshot {
		t.Fatal("missing header must mean delta")
	}
}

func TestDecodeLocalviewErrors(t *testing.T) {
	order := func(o string) []byte {
		return lvLine(`"x-nps-snapshot":"false",`, `[{"contractId":"NX_1","buyOrders":[`+o+`],"sellOrders":[]}]`)
	}
	cases := map[string][]byte{
		"invalid json":      []byte(`{not json`),
		"missing timestamp": []byte(`{"stomp_headers":{},"body":[]}`),
		"missing body":      []byte(`{"timestamp_utc":"` + lvTime + `","stomp_headers":{}}`),
		"empty contractId":  lvLine(``, `[{"contractId":"","buyOrders":[],"sellOrders":[]}]`),
		"empty orderId":     order(`{"orderId":"","price":1,"qty":1,"deleted":false}`),
		"no price":          order(`{"orderId":"A","qty":1,"deleted":false}`),
		"no qty":            order(`{"orderId":"A","price":1,"deleted":false}`),
		"negative qty":      order(`{"orderId":"A","price":1,"qty":-1,"deleted":false}`),
		"bad timestamp":     []byte(`{"timestamp_utc":"nope","body":[]}`),
	}
	for name, line := range cases {
		if _, err := DecodeLocalview(line); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func contractLine(ts, id, name string) []byte {
	return []byte(`{` + ts + `"topic":"contracts","body":{` + id + name +
		`"dlvryAreaState":[{"dlvryAreaId":20,"state":"IACT"},{"dlvryAreaId":46,"state":"ACTI"}],"deleted":true}}`)
}

func TestDecodeContract(t *testing.T) {
	c, err := DecodeContract(contractLine(`"timestamp_utc":"`+lvTime+`",`, `"contractId":"NX_1",`, `"contractName":"QH-1",`))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := ParseTime(lvTime)
	if c.ContractID != "NX_1" || c.Name != "QH-1" || c.State != "ACTI" || !c.Deleted ||
		c.Time != want || c.TimeRaw != lvTime {
		t.Fatalf("got %+v", c)
	}
}

func TestDecodeContractNoArea46(t *testing.T) {
	line := []byte(`{"timestamp_utc":"` + lvTime + `","body":{"contractId":"NX_1","contractName":"QH-1","dlvryAreaState":[{"dlvryAreaId":20,"state":"IACT"}]}}`)
	c, err := DecodeContract(line)
	if err != nil {
		t.Fatal(err)
	}
	if c.State != "" {
		t.Fatalf("State = %q", c.State)
	}
}

func TestDecodeContractErrors(t *testing.T) {
	ts := `"timestamp_utc":"` + lvTime + `",`
	cases := map[string][]byte{
		"empty contractId":   contractLine(ts, `"contractId":"",`, `"contractName":"QH-1",`),
		"empty contractName": contractLine(ts, `"contractId":"NX_1",`, `"contractName":"",`),
		"missing timestamp":  contractLine(``, `"contractId":"NX_1",`, `"contractName":"QH-1",`),
		"invalid json":       []byte(`{`),
	}
	for name, line := range cases {
		if _, err := DecodeContract(line); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
