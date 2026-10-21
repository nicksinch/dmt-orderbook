"""
Answer the moments file with RefBook, one JSON line per moment.

usage: python3 -I tools/ref_moments.py <localview> <contracts> <moments.txt>

Same output as `go run ./cmd/bookat`, so the two can be diffed.
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from dmt import RefBook, merged, parse_ts  # noqa: E402


def read_moments(path):
    ms = []
    with open(path) as f:
        for line in f:
            w = line.split()
            if not w:
                continue
            if w[0] == "top":
                ms.append((parse_ts(w[1]), None))
            else:
                ms.append((parse_ts(w[2]), w[1]))
    return ms


def answer(rb, name):
    obj = rb.top() if name is None else rb.book(name)
    print(json.dumps(obj, separators=(",", ":")))


def main():
    localview, contracts, moments = sys.argv[1:4]
    ms = read_moments(moments)
    rb = RefBook()
    i = 0
    for ts, source, n, rec in merged(localview, contracts):
        while i < len(ms) and ms[i][0] < ts:
            answer(rb, ms[i][1])
            i += 1
        if i == len(ms):
            return
        rb.apply(ts, source, n, rec)
    while i < len(ms):
        answer(rb, ms[i][1])
        i += 1


main()
