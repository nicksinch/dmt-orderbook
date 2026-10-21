# dmt-orderbook

## Baseline: stage 1 (JSON parsing, in-memory engine)

Stage 1 has no store yet. `cmd/bookat` (test tooling) reads both JSONL files, decodes every line with `encoding/json`, merges them by time and applies them to the in-memory engine. These numbers are the cost of parsing and applying the raw feed, so later stages can compare reading from the binary store against them.

**Correctness.** `bookat` output compared with the Python reference `RefBook` (`tools/ref_moments.py`) using `cmp`:

| Run | Moments | Result |
|---|---|---|
| First 100k lines | 17,773 (305 `top`, 17,468 `book`) | byte-identical |
| Whole file | 37,942 (496 `top`, 37,446 `book`) | byte-identical |

The moments include the time of the last localview line, so the whole-file check reads all 963,161 lines and also compares the final state.

**Throughput.** Three runs, each over the whole file: 963,161 localview lines and 5,476 contracts lines, with one `top` moment at the end and output sent to `/dev/null`:

| Run | Elapsed | Localview lines/s |
|---|---|---|
| 1 | 5.54 s | 173,947 |
| 2 | 6.11 s | 157,541 |
| 3 | 5.56 s | 173,250 |

The median is 5.56 s, about 173,000 lines/s. Scaled linearly, that comes to about 55 s per day of feed (about 10 times this file), 6.5 min per week and 28 min per month. The runs were back to back, so the input was most likely in the page cache already (not verified).

Machine: Apple M3, 16 GiB, macOS (darwin/arm64), Go 1.27.2. Measured 2026-10-21.

To reproduce:

```
go build -o tmp/bookat ./cmd/bookat
python3 -I tools/gen_moments.py localview.jsonl contracts.jsonl tmp/mall.txt
tmp/bookat localview.jsonl contracts.jsonl tmp/mall.txt > tmp/goall.out
python3 -I tools/ref_moments.py localview.jsonl contracts.jsonl tmp/mall.txt > tmp/pyall.out
cmp tmp/goall.out tmp/pyall.out
printf 'top 2026-09-24T23:59:59Z\n' > tmp/end.txt
tmp/bookat localview.jsonl contracts.jsonl tmp/end.txt > /dev/null
```

For the 100k check, add `--limit 100000` to `gen_moments.py`.

## Stage 2: the store

`ingest` writes the append-only store (`log` and `index`) from both feed files. `query status` prints the last localview line fully in the store. `bookat --store` answers moments from the store (test tooling).

```
go build -o tmp/ ./cmd/...
tmp/ingest --localview task/localview.jsonl --contracts task/contracts.jsonl --store tmp/store
tmp/query status --store tmp/store
```

`ingest` refuses an existing store directory. Optional flags: `--stop-after N` (stop after localview line N) and `--checkpoint-bytes B` (checkpoint spacing, default 1 MB).

**Correctness.** `bookat --store` answers every moment from the store with its own rebuild (index binary search, checkpoint restore, partial scan), and its output is compared with `RefBook` using `cmp`. `query status` printed 963161 for both stores:

| Store | Moments | Result |
|---|---|---|
| Whole file, 1 MB spacing (60 checkpoints) | 37,942 | byte-identical |
| Whole file, 256 KB spacing (238 checkpoints) | 37,942 | byte-identical |

**`kill -9`.** `tools/crash_test.py --runs 20` killed `ingest` at a random moment (0–6 s) 20 times; all 20 kills landed mid-ingest. Each time the killed store opened, and 300 moments were compared against a clean store built with `--stop-after <status>` and against `RefBook`. All matched: the killed store held exactly the input up to the line `status` reports.

**Reading while ingest runs.** `tools/live_read_test.py` ran `query status` and `bookat --store` in a loop during a whole-file ingest: 108 loops and 2,100 checks, 104 of them while ingest was still running, all matching. Status never went down, and it ended at 963,161.

**Store size** (1 MB spacing; the JSONL is 1,031,377,275 bytes):

| Part | Bytes | % of JSONL |
|---|---|---|
| Change blocks (2,750) | 62,399,286 | 6.05% |
| Checkpoint blocks (60) | 6,277,969 | 0.61% |
| Index | 960 | 0.00% |
| Total | 68,678,215 | 6.66% |

That is about 65 bytes per input line, with fixed-width integers and no compression; varints or zstd per block are the options if size matters. Sizes vary by a few KB between runs, because the 2 ms flush timer places block boundaries differently each time.

**Ingest throughput** (whole file, 1 MB spacing, including writing the store). `ingest` reads the JSONL, so it is stage 1's parse and apply plus the store writes:

| Run | Elapsed | Localview lines/s |
|---|---|---|
| 1 | 5.88 s | 163,796 |
| 2 | 5.73 s | 168,125 |
| 3 | 5.84 s | 165,060 |

The median is 165,060 lines/s, about 5% below stage 1's 173,000. Scaled linearly, that comes to about 1 min per day of feed, 7 min per week and 30 min per month.

**Reading the binary store.** Walking every block and decoding every record, with no engine and no printing, takes 0.08 s for the whole store: about 12.2 M records/s. Parsing the JSONL takes about 5.5 s, so this is about 70 times faster. A `replay` that also builds and prints output will be slower; that is measured in stage 3.

**Book at time T** (`StateAt`: a full rebuild of every product at a random T, 200 samples per store):

| Spacing | Total / JSONL | StateAt p50 | p99 | max |
|---|---|---|---|---|
| 256 KB | 8.50% | 0.77 ms | 1.85 ms | 8.96 ms |
| 1 MB | 6.66% | 2.25 ms | 9.74 ms | 11.68 ms |
| 4 MB | 6.19% | 8.47 ms | 39.28 ms | 40.44 ms |

p99 and max are noisy. The cost is bounded by the checkpoint spacing, plus the number of contracts ever seen in the store: every checkpoint holds every contract, expired ones included. Over 3 hours (about 640 contracts) that part is invisible. Over a year it would be about 47k contracts and 2–3 MB per checkpoint, so dropping expired contracts from checkpoints, or keeping one store per delivery period, is an open point.

All timings are on a warm page cache, on the same machine as stage 1. Measured 2026-10-21.

To reproduce (`tmp/mall.txt` and `tmp/pyall.out` come from the stage-1 commands above):

```
go build -o tmp/ ./cmd/...
rm -rf tmp/sall && tmp/ingest --localview task/localview.jsonl --contracts task/contracts.jsonl --store tmp/sall
tmp/query status --store tmp/sall
tmp/bookat --store tmp/sall tmp/mall.txt > tmp/stall.out && cmp tmp/stall.out tmp/pyall.out
python3 -I tools/crash_test.py --runs 20
python3 -I tools/live_read_test.py
tmp/storestat --store tmp/sall --jsonl task/localview.jsonl
```

For another checkpoint spacing, add `--checkpoint-bytes 262144` (256 KB) or `--checkpoint-bytes 4194304` (4 MB) to `ingest`, using a fresh store directory.
