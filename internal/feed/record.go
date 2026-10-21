package feed

import (
	"encoding/json"
	"errors"
	"fmt"
)

// bulgariaArea is the delivery area whose contract state we track.
const bulgariaArea = 46

// Order is one order version. Price and Qty are nil when the field is absent,
// which is allowed only for deleted orders.
type Order struct {
	OrderID string
	Price   *int64
	Qty     *int64
	Deleted bool
}

// ContractOrders holds the orders of one contract in one localview message.
type ContractOrders struct {
	ContractID string
	BuyOrders  []Order
	SellOrders []Order
}

// Localview is one decoded localview.jsonl line.
type Localview struct {
	TimeRaw  string // timestamp_utc as written; printed by replay later
	Time     int64
	Snapshot bool // stomp_headers["x-nps-snapshot"] == "true"
	Body     []ContractOrders
}

// Contract is one decoded contracts.jsonl line.
type Contract struct {
	TimeRaw    string
	Time       int64
	ContractID string
	Name       string
	State      string // state for dlvryAreaId 46; "" if no such entry
	Deleted    bool
}

type rawOrder struct {
	OrderID string `json:"orderId"`
	Price   *int64 `json:"price"`
	Qty     *int64 `json:"qty"`
	Deleted bool   `json:"deleted"`
}

type rawContractOrders struct {
	ContractID string     `json:"contractId"`
	BuyOrders  []rawOrder `json:"buyOrders"`
	SellOrders []rawOrder `json:"sellOrders"`
}

type rawLocalview struct {
	TimestampUTC string `json:"timestamp_utc"`
	StompHeaders struct {
		Snapshot string `json:"x-nps-snapshot"`
	} `json:"stomp_headers"`
	Body []rawContractOrders `json:"body"` // nil if missing, non-nil if "[]"
}

type rawContract struct {
	TimestampUTC string `json:"timestamp_utc"`
	Body         struct {
		ContractID     string `json:"contractId"`
		ContractName   string `json:"contractName"`
		DlvryAreaState []struct {
			DlvryAreaID int    `json:"dlvryAreaId"`
			State       string `json:"state"`
		} `json:"dlvryAreaState"`
		Deleted bool `json:"deleted"`
	} `json:"body"`
}

// DecodeLocalview decodes and validates one localview.jsonl line.
func DecodeLocalview(line []byte) (Localview, error) {
	var raw rawLocalview
	if err := json.Unmarshal(line, &raw); err != nil {
		return Localview{}, fmt.Errorf("decode localview: %w", err)
	}
	if raw.TimestampUTC == "" {
		return Localview{}, errors.New("localview: missing timestamp_utc")
	}
	t, err := ParseTime(raw.TimestampUTC)
	if err != nil {
		return Localview{}, err
	}
	if raw.Body == nil {
		return Localview{}, errors.New("localview: missing body")
	}
	lv := Localview{
		TimeRaw:  raw.TimestampUTC,
		Time:     t,
		Snapshot: raw.StompHeaders.Snapshot == "true",
		Body:     make([]ContractOrders, 0, len(raw.Body)),
	}
	for _, rc := range raw.Body {
		if rc.ContractID == "" {
			return Localview{}, errors.New("localview: empty contractId")
		}
		buys, err := convertOrders(rc.BuyOrders)
		if err != nil {
			return Localview{}, fmt.Errorf("contract %s buyOrders: %w", rc.ContractID, err)
		}
		sells, err := convertOrders(rc.SellOrders)
		if err != nil {
			return Localview{}, fmt.Errorf("contract %s sellOrders: %w", rc.ContractID, err)
		}
		lv.Body = append(lv.Body, ContractOrders{ContractID: rc.ContractID, BuyOrders: buys, SellOrders: sells})
	}
	return lv, nil
}

func convertOrders(raw []rawOrder) ([]Order, error) {
	out := make([]Order, 0, len(raw))
	for _, r := range raw {
		if r.OrderID == "" {
			return nil, errors.New("empty orderId")
		}
		if !r.Deleted {
			if r.Price == nil {
				return nil, fmt.Errorf("order %s: missing price", r.OrderID)
			}
			if r.Qty == nil {
				return nil, fmt.Errorf("order %s: missing qty", r.OrderID)
			}
			if *r.Qty < 0 {
				return nil, fmt.Errorf("order %s: negative qty %d", r.OrderID, *r.Qty)
			}
		}
		out = append(out, Order{OrderID: r.OrderID, Price: r.Price, Qty: r.Qty, Deleted: r.Deleted})
	}
	return out, nil
}

// DecodeContract decodes and validates one contracts.jsonl line.
func DecodeContract(line []byte) (Contract, error) {
	var raw rawContract
	if err := json.Unmarshal(line, &raw); err != nil {
		return Contract{}, fmt.Errorf("decode contract: %w", err)
	}
	if raw.TimestampUTC == "" {
		return Contract{}, errors.New("contract: missing timestamp_utc")
	}
	t, err := ParseTime(raw.TimestampUTC)
	if err != nil {
		return Contract{}, err
	}
	if raw.Body.ContractID == "" {
		return Contract{}, errors.New("contract: empty contractId")
	}
	if raw.Body.ContractName == "" {
		return Contract{}, fmt.Errorf("contract %s: empty contractName", raw.Body.ContractID)
	}
	c := Contract{
		TimeRaw:    raw.TimestampUTC,
		Time:       t,
		ContractID: raw.Body.ContractID,
		Name:       raw.Body.ContractName,
		Deleted:    raw.Body.Deleted,
	}
	for _, a := range raw.Body.DlvryAreaState {
		if a.DlvryAreaID == bulgariaArea {
			c.State = a.State
		}
	}
	return c, nil
}
