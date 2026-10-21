"""
Write the moments at which the Go engine and RefBook are compared.

usage: python3 -I tools/gen_moments.py <localview> <contracts> <out.txt>
           [--limit N] [--random K] [--seed S]

Output lines are "top T" and "book <name> T", sorted by T, T as ...ffffffZ.
"""
import argparse
import os
import random
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from dmt import bg_state, fmt_ts, iter_jsonl, parse_ts  # noqa: E402

BACKLOG_US = 10_000_000


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("localview")
    ap.add_argument("contracts")
    ap.add_argument("out")
    ap.add_argument("--limit", type=int, default=None)
    ap.add_argument("--random", type=int, default=300)
    ap.add_argument("--seed", type=int, default=1)
    a = ap.parse_args()

    # localview: first/last time, snapshots, backlog lines
    first = last = None
    times = set()
    backlog = 0
    for _n, _size, rec in iter_jsonl(a.localview, a.limit):
        ts = parse_ts(rec["timestamp_utc"])
        if first is None:
            first = ts
        last = ts
        h = rec["stomp_headers"]
        if h.get("x-nps-snapshot") == "true":
            times.add(ts - 1)
            times.add(ts)
        sent = h.get("x-nps-sent-at")
        if sent is not None and ts - int(sent) * 1000 > BACKLOG_US:
            backlog += 1
            if backlog % 1000 == 0:
                times.add(ts)

    times.add(last)  # the final state after the last line is compared too

    rng = random.Random(a.seed)
    for _ in range(a.random):
        times.add(rng.randint(first, last))

    # contracts alone: state changes, and the log used to find names in scope
    events = []  # (ts, contractId, name, active)
    prev = {}
    for _n, _size, rec in iter_jsonl(a.contracts):
        b = rec["body"]
        ts = parse_ts(rec["timestamp_utc"])
        state = bg_state(b)
        active = state == "ACTI" and not b.get("deleted")
        events.append((ts, b["contractId"], b.get("contractName"), active))
        if state != prev.get(b["contractId"]) and first < ts <= last:
            times.add(ts)
        prev[b["contractId"]] = state

    # for each time, in order, the names that are in scope
    active = {}  # contractId -> (name, active)
    i = 0
    with open(a.out, "w") as f:
        for t in sorted(times):
            while i < len(events) and events[i][0] <= t:
                _ts, cid, name, act = events[i]
                active[cid] = (name, act)
                i += 1
            f.write(f"top {fmt_ts(t)}\n")
            for name in sorted(n for n, act in active.values() if act):
                f.write(f"book {name} {fmt_ts(t)}\n")
    print(f"{len(times)} distinct times", file=sys.stderr)


main()
