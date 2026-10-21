"""Reference for `query replay <from> <to>`, built on RefBook only (never the Go code).

    python3 -I tools/ref_replay.py localview.jsonl contracts.jsonl <from> <to>

Prints the same lines as the Go replay: full books at `from`, then one line per
changed product per input line, and a full book when a contract enters scope.
"""
import json
import sys

sys.path.insert(0, __file__.rsplit("/", 1)[0] if "/" in __file__ else ".")
from dmt import RefBook, merged, parse_ts  # noqa: E402


def emit(t, name, bid, ask):
    print(json.dumps({"t": t, "product": name, "bid": bid, "ask": ask}, separators=(",", ":")))


def changed(before, after, bids):
    """Levels whose quantity differs (missing = 0): new quantity, best price first."""
    old, new = dict(before), dict(after)
    prices = sorted(set(old) | set(new), reverse=bids)
    return [[p, new.get(p, 0)] for p in prices if old.get(p, 0) != new.get(p, 0)]


def main():
    lv_path, ct_path, frm, to = sys.argv[1:5]
    t_from, t_to = parse_ts(frm), parse_ts(to)
    rb = RefBook()
    started = False

    def start():
        for cid in sorted(rb.active_ids(), key=lambda c: rb.contracts[c]["name"]):
            bids, asks = rb.levels(cid)
            emit(frm, rb.contracts[cid]["name"], bids, asks)

    for ts, source, _n, rec in merged(lv_path, ct_path):
        if not started and ts > t_from:
            start()
            started = True
        if ts > t_to:
            break
        if not started:
            rb.apply(ts, source, _n, rec)
            continue
        raw = rec["timestamp_utc"]
        if source == "contracts":
            cid = rec["body"]["contractId"]
            was = rb.is_active(cid)
            old_name = rb.contracts[cid]["name"] if cid in rb.contracts else None
            rb.apply(ts, source, _n, rec)
            if rb.is_active(cid) and (not was or rb.contracts[cid]["name"] != old_name):
                bids, asks = rb.levels(cid)
                emit(raw, rb.contracts[cid]["name"], bids, asks)
            continue
        touched = {c["contractId"] for c in rec["body"]}
        if rec["stomp_headers"].get("x-nps-snapshot") == "true":
            touched |= {cid for cid, o in rb.orders.items() if o}
        before = {cid: rb.levels(cid) for cid in touched}
        rb.apply(ts, source, _n, rec)
        out = []
        for cid in touched:
            if not rb.is_active(cid):
                continue
            b0, a0 = before[cid]
            b1, a1 = rb.levels(cid)
            cb, ca = changed(b0, b1, True), changed(a0, a1, False)
            if cb or ca:
                out.append((rb.contracts[cid]["name"], cb, ca))
        for name, cb, ca in sorted(out):
            emit(raw, name, cb, ca)
    else:
        if not started:
            start()


if __name__ == "__main__":
    main()
