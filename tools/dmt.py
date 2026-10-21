"""
Helpers for exploring the Dark Matter Trading order-book data.

Two jobs:
  1. Read the two .jsonl files line by line and merge them by timestamp_utc.
  2. RefBook: a deliberately simple, slow, "obviously correct" order book.
     Later it becomes the test oracle you compare your Go store against.

Nothing here loads a whole file into memory, so it works on the full 1 GB file
(it is just slow in Python: expect a few minutes for the whole thing).
"""
from __future__ import annotations

import heapq
import json
import re
from collections import defaultdict
from datetime import datetime, timedelta, timezone

try:  # orjson is ~3-5x faster than the standard json module
    import orjson
    _loads = orjson.loads
except ImportError:  # pragma: no cover
    _loads = json.loads

BG_AREA = 46
_EPOCH = datetime(1970, 1, 1, tzinfo=timezone.utc)
_TS_RE = re.compile(
    r"^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})?$"
)


# --------------------------------------------------------------------------
# Time
# --------------------------------------------------------------------------
def parse_ts(s: str) -> int:
    """ISO-8601 string -> integer microseconds since epoch (exact, no floats).

    Accepts '...Z', '...+00:00', no fraction, or 1-9 fraction digits.
    Do NOT compare the raw strings: '...:13+00:00' (no fraction) sorts
    after '...:13.5+00:00' as text, but is earlier in time.
    """
    m = _TS_RE.match(s)
    if not m:
        raise ValueError(f"bad timestamp: {s!r}")
    base, frac, tz = m.groups()
    dt = datetime.fromisoformat(base + (tz if tz and tz != "Z" else "+00:00"))
    d = dt - _EPOCH
    us = (d.days * 86_400 + d.seconds) * 1_000_000
    if frac:
        us += int((frac + "000000")[:6])  # truncate to microseconds
    return us


def fmt_ts(us: int) -> str:
    """Microseconds -> '2026-09-24T10:04:12.345678Z' (for display only)."""
    return (_EPOCH + timedelta(microseconds=int(us))).strftime("%Y-%m-%dT%H:%M:%S.%fZ")


# --------------------------------------------------------------------------
# Reading
# --------------------------------------------------------------------------
def iter_jsonl(path: str, limit: int | None = None):
    """Yield (line_number, size_in_bytes, record). Line numbers start at 1."""
    with open(path, "rb") as f:
        for n, raw in enumerate(f, start=1):
            if limit is not None and n > limit:
                return
            yield n, len(raw), _loads(raw)


def merged(localview_path: str, contracts_path: str, lv_limit: int | None = None):
    """Both files as one stream, ordered by timestamp_utc.

    Yields (ts_us, source, line_number, record), source is 'contracts' or 'localview'.
    On equal timestamps contracts come first (a decision: the product should be
    known before its orders are applied). Stops after the last localview line
    when lv_limit is set.

    Assumes each file is already in timestamp order -- check that first
    (notebook section 1), because heapq.merge silently misbehaves otherwise.
    """
    def tagged(path, source, prio, limit):
        for n, _size, rec in iter_jsonl(path, limit):
            yield parse_ts(rec["timestamp_utc"]), prio, n, source, rec

    lv = tagged(localview_path, "localview", 1, lv_limit)
    ct = tagged(contracts_path, "contracts", 0, None)
    lv_done_at = None
    for ts, _prio, n, source, rec in heapq.merge(ct, lv):
        if source == "localview" and lv_limit is not None and n == lv_limit:
            lv_done_at = ts
        if lv_done_at is not None and ts > lv_done_at:
            return
        yield ts, source, n, rec


# --------------------------------------------------------------------------
# Reference order book
# --------------------------------------------------------------------------
def bg_state(contract_body: dict) -> str | None:
    """State of this contract in Bulgaria (area 46), e.g. 'ACTI', 'IACT', or None."""
    for a in contract_body.get("dlvryAreaState") or []:
        if a.get("dlvryAreaId") == BG_AREA:
            return a.get("state")
    return None


class RefBook:
    """The simplest possible correct book. Slow on purpose: easy to trust.

    State:
      orders[contractId][orderId] = (is_buy, price, qty)
      contracts[contractId]       = {'name', 'state', 'deleted', ...}

    Orders are tracked for every contract, active or not; the ACTI/area-46
    filter is applied only when printing. (So a product that flips to IACT
    and back keeps its orders. Worth confirming with DMT.)
    """

    def __init__(self):
        self.orders: dict[str, dict[str, tuple[bool, int, int]]] = defaultdict(dict)
        self.contracts: dict[str, dict] = {}
        self.name_to_id: dict[str, str] = {}
        self.last_localview_line = 0
        self.last_ts = None

    # -- applying input ---------------------------------------------------
    def apply(self, ts: int, source: str, line: int, rec: dict):
        self.last_ts = ts
        if source == "contracts":
            self.apply_contract(rec["body"])
        else:
            self.apply_localview(rec)
            self.last_localview_line = line

    def apply_contract(self, body: dict):
        cid = body["contractId"]
        self.contracts[cid] = {
            "name": body.get("contractName"),
            "state": bg_state(body),
            "deleted": bool(body.get("deleted")),
            "dlvryStart": body.get("dlvryStart"),
            "dlvryEnd": body.get("dlvryEnd"),
            "productType": body.get("productType"),
        }
        self.name_to_id[body.get("contractName")] = cid

    def apply_localview(self, rec: dict):
        if rec["stomp_headers"].get("x-nps-snapshot") == "true":
            self.orders = defaultdict(dict)  # a snapshot REPLACES everything
        for c in rec["body"]:
            book = self.orders[c["contractId"]]
            for is_buy, key in ((True, "buyOrders"), (False, "sellOrders")):
                for o in c.get(key) or []:
                    if o.get("deleted"):
                        book.pop(o["orderId"], None)
                    else:
                        book[o["orderId"]] = (is_buy, o["price"], o["qty"])

    # -- reading ------------------------------------------------------------
    def is_active(self, cid: str) -> bool:
        c = self.contracts.get(cid)
        return bool(c) and c["state"] == "ACTI" and not c["deleted"]

    def active_ids(self):
        return [cid for cid in self.contracts if self.is_active(cid)]

    def levels(self, cid: str):
        """Aggregated book: (bids desc, asks asc) as lists of [price, qty]."""
        bid, ask = defaultdict(int), defaultdict(int)
        for is_buy, price, qty in self.orders.get(cid, {}).values():
            (bid if is_buy else ask)[price] += qty
        bids = [[p, q] for p, q in sorted(bid.items(), reverse=True) if q]
        asks = [[p, q] for p, q in sorted(ask.items()) if q]
        return bids, asks

    def book(self, name: str):
        cid = self.name_to_id.get(name)
        if cid is None or not self.is_active(cid):
            return {name: None}
        bids, asks = self.levels(cid)
        return {name: {"bid": bids, "ask": asks}}

    def top(self):
        out = {}
        for cid in sorted(self.active_ids(), key=lambda c: self.contracts[c]["name"]):
            bids, asks = self.levels(cid)
            out[self.contracts[cid]["name"]] = {
                "bid": bids[0] if bids else None,
                "ask": asks[0] if asks else None,
            }
        return out


def book_at(localview_path: str, contracts_path: str, T: str, lv_limit: int | None = None) -> RefBook:
    """Replay everything with timestamp_utc <= T and return the RefBook."""
    t = parse_ts(T)
    rb = RefBook()
    for ts, source, n, rec in merged(localview_path, contracts_path, lv_limit):
        if ts > t:
            break
        rb.apply(ts, source, n, rec)
    return rb


# --------------------------------------------------------------------------
# Flat summaries (small enough for pandas)
# --------------------------------------------------------------------------
def localview_rows(path: str, limit: int | None = None):
    """One small dict per localview line -- the nested orders are counted, not kept."""
    for n, size, rec in iter_jsonl(path, limit):
        h = rec.get("stomp_headers") or {}
        body = rec.get("body") or []
        n_orders = n_deleted = 0
        for c in body:
            for key in ("buyOrders", "sellOrders"):
                for o in c.get(key) or []:
                    n_orders += 1
                    n_deleted += bool(o.get("deleted"))
        yield {
            "line": n,
            "ts": parse_ts(rec["timestamp_utc"]),
            "ts_raw": rec["timestamp_utc"],
            "bytes": size,
            "snapshot": h.get("x-nps-snapshot") == "true",
            "seq": int(h["x-nps-sequenceNo"]) if "x-nps-sequenceNo" in h else None,
            "subscription": rec.get("subscription_id") or h.get("subscription"),
            "sent_at_ms": int(h["x-nps-sent-at"]) if "x-nps-sent-at" in h else None,
            "n_contracts": len(body),
            "n_orders": n_orders,
            "n_deleted": n_deleted,
        }


def contract_rows(path: str, limit: int | None = None):
    for n, size, rec in iter_jsonl(path, limit):
        b = rec["body"]
        yield {
            "line": n,
            "ts": parse_ts(rec["timestamp_utc"]),
            "ts_raw": rec["timestamp_utc"],
            "contractId": b["contractId"],
            "name": b.get("contractName"),
            "state_bg": bg_state(b),
            "deleted": bool(b.get("deleted")),
            "productType": b.get("productType"),
            "dlvryStart": b.get("dlvryStart"),
            "dlvryEnd": b.get("dlvryEnd"),
            "n_areas": len(b.get("dlvryAreaState") or []),
        }
