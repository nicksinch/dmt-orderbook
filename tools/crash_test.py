"""kill -9 test: kill ingest at a random moment, then check the store against the
Python reference output (tmp/pyall.out) and against a clean ingest stopped at the same line.

    python3 -I tools/crash_test.py --runs 20
"""
import argparse
import os
import random
import re
import shutil
import signal
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


def fmt_us(us):
    dt = datetime.fromtimestamp(us // 1_000_000, timezone.utc)
    return dt.strftime("%Y-%m-%dT%H:%M:%S") + ".%06dZ" % (us % 1_000_000)


def line_times(path):
    """Microsecond timestamp of every localview line, found by a regex on the line start."""
    out = []
    with open(path, "rb") as f:
        for line in f:
            m = TS_RE.search(line, 0, 200)
            out.append(parse_us(m.group(1).decode()))
    return out


def read_moments(mall, pyall):
    """(raw line, T in us, expected output) for every non-blank moment line."""
    with open(mall) as f:
        lines = [l.rstrip("\n") for l in f if l.strip()]
    with open(pyall) as f:
        outs = [l.rstrip("\n") for l in f]
    if len(lines) != len(outs):
        sys.exit("moments (%d) and reference output (%d) differ in count" % (len(lines), len(outs)))
    return [(l, parse_us(l.split()[-1]), o) for l, o in zip(lines, outs)]


def run_bookat(bindir, store, moments_file):
    r = subprocess.run([os.path.join(bindir, "bookat"), "--store", store, moments_file],
                       capture_output=True, text=True)
    if r.returncode != 0:
        sys.exit("bookat failed on %s: %s" % (store, r.stderr))
    return r.stdout.splitlines()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--runs", type=int, default=20)
    ap.add_argument("--bin", default="tmp")
    ap.add_argument("--localview", default="task/localview.jsonl")
    ap.add_argument("--contracts", default="task/contracts.jsonl")
    ap.add_argument("--moments", default="tmp/mall.txt")
    ap.add_argument("--ref", default="tmp/pyall.out")
    ap.add_argument("--max-seconds", type=float, default=6)
    ap.add_argument("--workdir", default="tmp")
    a = ap.parse_args()

    ts = line_times(a.localview)
    moments = read_moments(a.moments, a.ref)
    print("localview lines: %d, moments: %d" % (len(ts), len(moments)))
    crash, clean = os.path.join(a.workdir, "crash"), os.path.join(a.workdir, "clean")
    mfile = os.path.join(a.workdir, "crash_moments.txt")
    rng = random.Random(1)
    mid_kills, total_checked, statuses = 0, 0, []

    for i in range(1, a.runs + 1):
        shutil.rmtree(crash, ignore_errors=True)
        shutil.rmtree(clean, ignore_errors=True)
        delay = random.uniform(0, a.max_seconds)
        p = subprocess.Popen([os.path.join(a.bin, "ingest"), "--localview", a.localview,
                              "--contracts", a.contracts, "--store", crash],
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        time.sleep(delay)
        p.send_signal(signal.SIGKILL)
        p.wait()

        r = subprocess.run([os.path.join(a.bin, "query"), "status", "--store", crash],
                           capture_output=True, text=True)
        if r.returncode != 0:
            sys.exit("run %d: query status failed: %s" % (i, r.stderr))
        n = int(r.stdout.strip())
        if n < 0 or n > len(ts):
            sys.exit("run %d: status %d out of range" % (i, n))
        tn = ts[n - 1] if n > 0 else None
        if n == 0:  # nothing stored: nothing to compare (--stop-after 0 would mean no limit)
            statuses.append(n)
            print("run %d: killed at %.2fs, status 0, 0 moments OK (empty store) (0 moments compared)" % (i, delay))
            continue

        r = subprocess.run([os.path.join(a.bin, "ingest"), "--localview", a.localview,
                            "--contracts", a.contracts, "--store", clean, "--stop-after", str(n)],
                           capture_output=True, text=True)
        if r.returncode != 0:
            sys.exit("run %d: clean ingest failed: %s" % (i, r.stderr))

        idx = [k for k, m in enumerate(moments) if tn is not None and m[1] <= tn]
        if len(idx) > 300:
            idx = sorted(rng.sample(idx, 300))
        chosen = [moments[k] for k in idx]
        with open(mfile, "w") as f:
            for raw, _, _ in chosen:
                f.write(raw + "\n")
            if tn is not None:
                f.write("top %s\n" % fmt_us(tn))
        got_crash = run_bookat(a.bin, crash, mfile)
        got_clean = run_bookat(a.bin, clean, mfile)
        if got_crash != got_clean:
            for k, (x, y) in enumerate(zip(got_crash, got_clean)):
                if x != y:
                    sys.exit("run %d: killed store differs from clean store at moment %d (%s)\n killed: %s\n clean:  %s"
                             % (i, k + 1, (chosen + [("top " + fmt_us(tn), 0, "")])[k][0], x, y))
            sys.exit("run %d: output line counts differ" % i)
        checked = 0
        for k, (raw, t, want) in enumerate(chosen):
            if t < tn:
                if got_crash[k] != want:
                    sys.exit("run %d: moment %r differs from reference\n store: %s\n ref:   %s"
                             % (i, raw, got_crash[k], want))
                checked += 1
        statuses.append(n)
        total_checked += checked
        note = ""
        if 0 < n < len(ts):
            mid_kills += 1
        else:
            note = " (%s)" % ("empty store" if n == 0 else "ingest had finished")
        if checked == 0:
            note += " (0 moments compared)"
        print("run %d: killed at %.2fs, status %d, %d moments OK%s" % (i, delay, n, checked, note))
    shutil.rmtree(crash, ignore_errors=True)
    shutil.rmtree(clean, ignore_errors=True)
    print("summary: %d runs, %d killed mid-ingest, %d moments compared, statuses %s"
          % (a.runs, mid_kills, total_checked, statuses))
    if mid_kills == 0:
        sys.exit("no run was killed mid-ingest")
    if total_checked == 0:
        sys.exit("no moments were compared")


main()
