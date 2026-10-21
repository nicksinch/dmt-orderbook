"""Follow test: `query replay <from> follow` during a live ingest equals the plain replay afterwards.

    python3 -I tools/follow_test.py
"""
import os
import shutil
import subprocess
import sys
import time

BIN = "tmp"
STORE = "tmp/follow"
END = "2026-09-24T13:00:00Z"
FROMS = {"A": "2026-09-24T09:00:00Z", "B": "2026-09-24T12:00:00Z"}  # B starts beyond the stored data
LAST_LINE = "963161"


def query(*args):
    return subprocess.run([os.path.join(BIN, "query"), *args, "--store", STORE], capture_output=True, text=True)


def main():
    shutil.rmtree(STORE, ignore_errors=True)
    ingest = subprocess.Popen([os.path.join(BIN, "ingest"), "--localview", "task/localview.jsonl",
                               "--contracts", "task/contracts.jsonl", "--store", STORE],
                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    outs, procs = {}, {}
    try:
        return check(ingest, outs, procs)
    finally:  # no exit path leaves a follower running
        for p in procs.values():
            p.kill()
            p.wait()
        if ingest.poll() is None:
            ingest.kill()
            ingest.wait()


def check(ingest, outs, procs):
    while not os.path.isdir(STORE):
        if ingest.poll() is not None:
            sys.exit("ingest exited %d before creating the store" % ingest.returncode)
        time.sleep(0.01)

    for k, frm in FROMS.items():
        outs[k] = "tmp/follow_%s.out" % k
        procs[k] = subprocess.Popen([os.path.join(BIN, "query"), "replay", frm, "follow", "--store", STORE],
                                    stdout=open(outs[k], "wb"))

    code = ingest.wait()
    if code != 0:
        sys.exit("ingest exited %d" % code)
    status = query("status").stdout.strip()
    if status != LAST_LINE:
        sys.exit("query status printed %r, want %s" % (status, LAST_LINE))

    sizes = None
    while True:  # wait until both outputs stop growing for 2 s
        time.sleep(2)
        now = {k: os.path.getsize(p) for k, p in outs.items()}
        if now == sizes:
            break
        sizes = now
    for p in procs.values():
        p.kill()
        p.wait()

    ok = True
    for k, frm in FROMS.items():
        r = subprocess.run([os.path.join(BIN, "query"), "replay", frm, END, "--store", STORE], capture_output=True)
        got = open(outs[k], "rb").read()
        same = got == r.stdout
        ok &= same
        print("%s from %s: follow %d lines, replay %d lines, %s" % (
            k, frm, got.count(b"\n"), r.stdout.count(b"\n"), "equal" if same else "DIFFERENT"))
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
