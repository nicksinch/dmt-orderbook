"""
Time `query book` end to end and report the bytes each rebuild reads.

T points are spread evenly from the first to the last localview timestamp.
For each T: take the first name from `query top T`, run `query book <name> T`
--repeat times and keep the median wall time of the whole process. Then ask
`storestat --work` for the checkpoint and scanned bytes of all Ts at once.
Test tooling, not part of the solution. Run with: python3 -I tools/measure_book.py --store DIR
"""
import argparse
import json
import os
import statistics
import subprocess
import sys
import time
from datetime import datetime, timedelta, timezone

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EPOCH = datetime(1970, 1, 1, tzinfo=timezone.utc)


def to_us(s):
    d = datetime.fromisoformat(s)
    return (d - EPOCH) // timedelta(microseconds=1)


def fmt_z(us):
    return (EPOCH + timedelta(microseconds=us)).strftime("%Y-%m-%dT%H:%M:%S.%fZ")


def first_last_ts(path):
    """timestamp_utc of the first and last line, without reading the file."""
    with open(path, "rb") as f:
        first = f.readline()
        f.seek(0, os.SEEK_END)
        size = f.tell()
        chunk = min(size, 1 << 16)
        f.seek(size - chunk)
        last = f.read(chunk).rstrip(b"\n").rsplit(b"\n", 1)[-1]
    return to_us(json.loads(first)["timestamp_utc"]), to_us(json.loads(last)["timestamp_utc"])


def run(*args):
    return subprocess.run(args, capture_output=True, text=True, check=True).stdout


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--store", required=True)
    ap.add_argument("--localview", default=os.path.join(ROOT, "task/localview.jsonl"))
    ap.add_argument("--points", type=int, default=20)
    ap.add_argument("--repeat", type=int, default=5)
    a = ap.parse_args()
    query = os.path.join(ROOT, "tmp/query")
    storestat = os.path.join(ROOT, "tmp/storestat")

    lo, hi = first_last_ts(a.localview)
    rows = []
    for i in range(a.points):
        t = fmt_z(lo + (hi - lo) * i // (a.points - 1))
        top = json.loads(run(query, "top", t, "--store", a.store))
        if not top:
            print(f"skip {t}: query top is empty", file=sys.stderr)
            continue
        name = next(iter(top))
        times = []
        for _ in range(a.repeat):
            s = time.perf_counter()
            run(query, "book", name, t, "--store", a.store)
            times.append((time.perf_counter() - s) * 1000)
        rows.append((t, name, statistics.median(times)))

    work_file = os.path.join(a.store, "..", f"work_{os.getpid()}.txt")
    with open(work_file, "w") as f:
        f.write("".join(t + "\n" for t, _, _ in rows))
    try:
        work = run(storestat, "--store", a.store, "--work", work_file).splitlines()
    finally:
        os.remove(work_file)

    print("T name median_ms checkpoint_bytes scanned_bytes")
    for (t, name, ms), w in zip(rows, work):
        _, ck, sc = w.split()
        print(f"{t} {name} {ms:.1f} {ck} {sc}")
    meds = sorted(ms for _, _, ms in rows)
    print(f"p50 {meds[len(meds) // 2]:.1f} ms  max {meds[-1]:.1f} ms")


main()
