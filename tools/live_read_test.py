"""Read-while-writing test: query a store while ingest is still filling it.

    python3 -I tools/live_read_test.py
"""
import argparse
import os
import random
import re
import shutil
import subprocess
import sys
import time
from datetime import datetime, timezone

EPOCH = datetime(1970, 1, 1, tzinfo=timezone.utc)
TS_RE = re.compile(rb'"timestamp_utc"\s*:\s*"([^"]+)"')


def parse_us(s):
    """RFC 3339 time to integer microseconds since the epoch (never compare strings)."""
    m = re.fullmatch(r"(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d+))?(Z|[+-]\d\d:\d\d)", s)
    if not m:
        raise ValueError("bad time %r" % s)
    base, frac, zone = m.groups()
    frac = ((frac or "") + "000000")[:6]
    dt = datetime.fromisoformat(base + "+00:00" if zone == "Z" else base + zone)
    d = dt - EPOCH
    return (d.days * 86400 + d.seconds) * 1_000_000 + int(frac)


def line_times(path):
    out = []
    with open(path, "rb") as f:
        for line in f:
            m = TS_RE.search(line, 0, 200)
            out.append(parse_us(m.group(1).decode()))
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", default="tmp")
    ap.add_argument("--localview", default="task/localview.jsonl")
    ap.add_argument("--contracts", default="task/contracts.jsonl")
    ap.add_argument("--moments", default="tmp/mall.txt")
    ap.add_argument("--ref", default="tmp/pyall.out")
    ap.add_argument("--workdir", default="tmp")
    a = ap.parse_args()

    ts = line_times(a.localview)
    with open(a.moments) as f:
        lines = [l.rstrip("\n") for l in f if l.strip()]
    with open(a.ref) as f:
        outs = [l.rstrip("\n") for l in f]
    if len(lines) != len(outs):
        sys.exit("moments (%d) and reference output (%d) differ in count" % (len(lines), len(outs)))
    moments = [(l, parse_us(l.split()[-1]), o) for l, o in zip(lines, outs)]
    print("localview lines: %d, moments: %d" % (len(ts), len(moments)))

    store = os.path.join(a.workdir, "live")
    shutil.rmtree(store, ignore_errors=True)
    mfile = os.path.join(a.workdir, "live_moments.txt")
    rng = random.Random(2)
    p = subprocess.Popen([os.path.join(a.bin, "ingest"), "--localview", a.localview,
                          "--contracts", a.contracts, "--store", store],
                         stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    prev, loops, checks, live_obs = 0, 0, 0, 0
    seen = False  # a status call has succeeded
    while True:
        done = p.poll() is not None  # one more pass after exit
        if os.path.isdir(store):
            r = subprocess.run([os.path.join(a.bin, "query"), "status", "--store", store],
                               capture_output=True, text=True)
            if r.returncode != 0:
                if not seen and not done:
                    time.sleep(0.05)  # store not usable yet; allowed only before the first success
                    continue
                p.kill()
                sys.exit("query status failed after %s: %s" % ("a success" if seen else "ingest exit", r.stderr))
            seen = True
            n = int(r.stdout.strip())
            if n < prev:
                sys.exit("status went backwards: %d after %d" % (n, prev))
            prev = n
            tn = ts[n - 1] if n > 0 else None
            idx = [k for k, m in enumerate(moments) if tn is not None and m[1] < tn]
            if len(idx) > 20:
                idx = sorted(rng.sample(idx, 20))
            if idx:
                with open(mfile, "w") as f:
                    for k in idx:
                        f.write(moments[k][0] + "\n")
                r = subprocess.run([os.path.join(a.bin, "bookat"), "--store", store, mfile],
                                   capture_output=True, text=True)
                if r.returncode != 0:
                    p.kill()
                    sys.exit("bookat failed at status %d: %s" % (n, r.stderr))
                got = r.stdout.splitlines()
                if len(got) != len(idx):
                    sys.exit("bookat returned %d lines for %d moments" % (len(got), len(idx)))
                for g, k in zip(got, idx):
                    if g != moments[k][2]:
                        sys.exit("status %d: moment %r differs\n store: %s\n ref:   %s"
                                 % (n, moments[k][0], g, moments[k][2]))
                    checks += 1
            loops += 1
            if not done and idx:
                live_obs += 1
        if done:
            break
    if p.returncode != 0:
        sys.exit("ingest exited with %d" % p.returncode)
    print("loops: %d, checks: %d, checked while ingest ran: %d, final status: %d"
          % (loops, checks, live_obs, prev))
    if prev != len(ts):
        sys.exit("final status %d != localview lines %d" % (prev, len(ts)))
    if checks == 0:
        sys.exit("no moments were checked")
    if live_obs == 0:
        sys.exit("no successful check happened while ingest was still running")
    shutil.rmtree(store, ignore_errors=True)


main()
