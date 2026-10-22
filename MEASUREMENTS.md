# Measurements

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

**Reading the binary store.** Walking every block and decoding every record, with no engine and no printing, takes 0.08 s for the whole store: about 12.2 M records/s. Parsing the JSONL takes about 5.5 s, so this is about 70 times faster. A `replay` that also builds and prints output will be slower; that is measured in the measurements session.

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

## Stage 3: query

```
go build -o tmp/ ./cmd/...
tmp/query status                            --store tmp/store
tmp/query book <product-name> <T>           --store tmp/store
tmp/query top <T>                           --store tmp/store
tmp/query replay <from> <to|follow>         --store tmp/store
```

`--store DIR` can go anywhere on the line (default `store`). `replay` prints a full book per product at `from`, then one line per changed product per input line, with only the changed levels (0 = gone). With `follow` it keeps streaming as `ingest` stores new data, until killed.

**Correctness.** `replay` output compared with `tools/ref_replay.py` (built on `RefBook` only) using `cmp`, on the 1 MB-spacing store and the 256 KB-spacing store:

| Window | from | to | Result |
|---|---|---|---|
| First 100k lines | `2026-09-24T09:00:00Z` | `2026-09-24T09:41:33.187577Z` | byte-identical on both stores |
| Whole file | `2026-09-24T09:00:00Z` | `2026-09-24T13:00:00Z` | byte-identical on both stores |
| Reconnect snapshot (line 331293) | `2026-09-24T10:23:40Z` | `2026-09-24T10:23:45Z` | byte-identical on both stores |
| Inside the outage (ms-form `from`) | `2026-09-24T10:40:00.500Z` | `2026-09-24T10:58:00Z` | byte-identical on both stores |
| Gate closure | `2026-09-24T11:14:00Z` | `2026-09-24T11:16:00Z` | byte-identical on both stores |
| 12:00 next-day opening (scope entries print full books) | `2026-09-24T11:59:00Z` | `2026-09-24T12:01:00Z` | byte-identical on both stores |

- `query book` and `query top` for 305 moments sampled from the stage-1 moments (every 125th plus the last): all equal to the `RefBook` answers.
- Follow test (`python3 -I tools/follow_test.py`): two `follow` processes, one started at 09:00 and one at 12:00 (beyond the stored data at start), run during a whole-file `ingest`. Both outputs equal the plain `replay` of the finished store.

## Stage 4: measurements

All timings: Apple M3, 16 GiB, macOS, warm page cache, Go 1.27.2, measured 2026-10-22. The logs behind the numbers are in `tmp/stage4/` (gitignored, not committed, regenerated by the commands in "To reproduce" below).

### Idle-flush fix in `ingest`

**What was wrong.** `ingest` only wrote a block when a later input line arrived after the block was 2 ms old (`BlockAge`). If the feed went quiet, the last lines stayed in memory, so a reader (and `follow`) saw them late: up to 2.9 s in the 10-minute steady window, and 290 s in the 30-minute outage window.

**What changed.** A reader goroutine feeds lines to the writer loop, and a ticker every `BlockAge` calls `Writer.FlushIfDue`. It only flushes (no checkpoint). Still one goroutine writes the store. The store format is unchanged.

**Checks after the fix** (`verify.log`, all pass again):

| Check | Result |
|---|---|
| `query status` after a whole-file `ingest` | 963161 |
| `bookat --store` vs `RefBook`, 37,942 moments | byte-identical |
| `tools/crash_test.py --runs 20` | 20 runs, 18 killed mid-ingest, 5,997 moments compared, all OK |
| `tools/live_read_test.py` | 74 loops, 1,440 checks, 71 while ingest ran, all OK, final status 963161 |
| `tools/follow_test.py` | follow output equals replay: 961,962 lines (from 09:00) and 169,878 lines (from 12:00) |
| `replay` vs `ref_replay.py`, 6 windows, 1 MB and 256 KB stores | all `cmp` OK |

**Ingest throughput**, whole file, 1 MB spacing, three interleaved rounds (`ingest_throughput.log`):

| Round | Before fix lines/s | After fix lines/s |
|---|---|---|
| 1 | 134,201 | 158,187 |
| 2 | 156,440 | 182,186 |
| 3 | 157,222 | 150,784 |
| Median elapsed | 6.157 s | 6.089 s |

The spread between rounds is about 15%, so the 1.1% difference is within noise. The fix does not make `ingest` faster, and it does not make it measurably slower.

### `query book` cost

`tools/measure_book.py`: 20 values of T spread evenly over the file, the first product from `query top T`, wall time of the whole `query book` process (median of repeats per T). Checkpoint and scanned bytes come from `storestat --work`. Table values are over the 20 Ts (`book_*.log`).

| Spacing | p50 of per-T medians | max | Median checkpoint bytes | Median scanned bytes |
|---|---|---|---|---|
| 256 KB | 3.4 ms | 4.0 ms | 104,784 | 159,250 |
| 1 MB | 6.0 ms | 6.9 ms | 104,784 | 730,031 |
| 4 MB | 14.2 ms | 23.6 ms | 104,446 | 2,898,702 |

The spacing bounds the cost: the scan after the checkpoint is at most about one spacing of changes. The s4m max (23.6 ms) is a single T (10:41:00, 3.9 MB scanned); the next largest is 17.2 ms. What grows with history is the checkpoint size, because contracts are never dropped from checkpoints. These timings are whole-process times (start-up, read, CRC, decode, apply, print), so they are higher than the in-process `StateAt` numbers of stage 2.

### Growth with history

Checkpoint size over the 3 hours (1 MB spacing, `checkpoints_sall.log`, `growth.txt`):

| Checkpoint | Contracts | Books | Bytes |
|---|---|---|---|
| First (09:27:32) | 644 | 57 | 124,380 |
| Lowest (11:52:48) | 655 | 45 | 94,936 |
| Last (12:23:51) | 658 | 158 | 105,264 |

The size is not monotonic: it falls as products close, then recovers after the 12:00 opening refills the books. The contracts count only goes up (644 to 658).

Extrapolation (`growth.txt`): 160 bytes per contract (last checkpoint bytes / contracts; this overstates the cost of one more contract), 120 new contracts a day, contracts never dropped, book size held constant. Restore cost uses the measured slope of `query book` time against checkpoint plus scanned bytes, a least-squares fit over the 60 points of the three `book_*.log` files: 2.77 ms + 3.69 ms/MB (r = 0.975). It is a whole-process slope taken mostly from scanned bytes, so applying it to checkpoint bytes is an assumption. Block decoding alone runs at 846 MB/s (1.2 ms/MB), which is only a lower bound.

| History | Contracts | Checkpoint | Restore at 3.69 ms/MB | Decode only |
|---|---|---|---|---|
| Today (3 h) | 658 | 105 KB | 0.39 ms | 0.12 ms |
| Day | 778 | 124 KB | 0.46 ms | 0.15 ms |
| Week | 1,498 | 240 KB | 0.88 ms | 0.28 ms |
| Month | 4,258 | 681 KB | 2.51 ms | 0.80 ms |
| Year | 44,458 | 7.1 MB | 26 ms | 8.4 ms |

Up to a month this is small next to the scan. At a year the checkpoint (7.1 MB) is bigger than the 1 MB spacing, so one checkpoint per 1 MB of changes would be about 7 times write amplification. The spacing would have to scale with the checkpoint size, or expired contracts be dropped from checkpoints.

### `replay` throughput

`query replay 2026-09-24T00:00:00Z 2026-09-25T00:00:00Z` on the 1 MB store: the whole input, so the opening full books plus every change line (`replay_time.log`, `replay_wc.log`).

| Run | Elapsed |
|---|---|
| 1 | 0.87 s |
| 2 | 0.80 s |
| 3 | 0.81 s |
| Piped to `wc -l` | 0.92 s |

Median of the three `/dev/null` runs: 0.81 s. That is about 1.19 M input lines/s (963,161 lines) and 1.19 M output lines/s. The output is 961,962 lines and 109,151,794 bytes, about 135 MB/s. The piped run is 14% above the median (a pipe consumer costs something), so both are reported.

| Input | Factor | Replay time at 0.81 s per file |
|---|---|---|
| This file (3 h) | 1 | 0.81 s |
| Day | 10 | 8.1 s |
| Week | 70 | 57 s |
| Month | 300 | 4.1 min |

Linear extrapolation. It assumes the book stays the size it is here; more live contracts would make each line a little more expensive.

### Ingest-to-`follow` latency

`cmd/followlat` feeds both files to `ingest` through pipes and runs `query replay <from> follow`. "Read time" of a line is the moment the tool finished writing it into the pipe. Latency is the time until the first follow output line with that line's raw `t` arrives. Follow lines whose `t` is not a localview timestamp are ignored. Contracts are fed unpaced (up to the window end in paced mode), because `feed.Merger.Next` needs the next contracts line (or end of file) before it can emit any localview line, so with paced contracts localview would stall until the next contracts line arrives, up to 15 minutes away. This is a property of merging two files, not of the store: the latency numbers assume the contracts feed is ahead of localview, and a single capture stream (Part 2) removes the limit. Localview is paced by `timestamp_utc` at speed 1 (steady and outage windows) or unpaced (full run). In the unpaced run, lines queue in the pipe, so the tool's write time can run ahead of what `ingest` has read. Burst = a line in a whole second with more than 1,000 localview lines.

Windows (`windows.txt`): steady is 09:40:00–09:50:00 (10 min, 62,703 lines, no snapshot inside), outage is 10:28:00–10:58:00 (30 min, 79,339 lines), full is the whole file unpaced. "Before" is the binary from before the fix.

| Run | Fix | Class | Count | p50 ms | p90 ms | p99 ms | p99.9 ms | max ms |
|---|---|---|---|---|---|---|---|---|
| Steady | before | all | 62,629 | 36.6 | 84.8 | 174.8 | 281.6 | 2,910.6 |
| Steady | after | all | 62,629 | 4.06 | 5.14 | 9.48 | 16.1 | 86.4 |
| Outage | before | all | 79,201 | 5.42 | 69.0 | 232.8 | 98,813.8 | 290,164.6 |
| Outage | after | all | 79,201 | 3.48 | 5.15 | 44.1 | 150.0 | 387.0 |
| Outage | after | burst | 22,787 | 2.16 | 3.81 | 10.2 | 26.1 | 48.3 |
| Full | before | all | 961,444 | 2.02 | 3.03 | 7.46 | 52.4 | 265.5 |
| Full | after | all | 961,444 | 2.06 | 3.07 | 6.37 | 93.8 | 96.9 |
| Full | after | burst | 22,787 | 2.03 | 3.05 | 6.39 | 22.5 | 23.9 |

Lines written but not measured (74 steady, 138 outage, 1,717 full) are lines for which no follow line with their `t` appeared; the log does not say why (the likely reason is lines that change nothing in scope, not checked).

**What decides it** (`lat_notes.txt`):

- **The old tail was the idle-flush bug.** Before the fix, latency depended on the gap to the next input line (gap over 100 ms: p50 121 ms; gap under 1 ms: 38 ms). After the fix it does not (p50 3.8–4.1 ms for all gap classes). The line that was worst before (2,910 ms) now takes 2.9 ms, and the outage max from 290 s to 387 ms.
- **The paced p50 (about 4 ms) is block age plus ticker phase.** A block is written 2–4 ms after its first line (a 2 ms ticker with random phase); measured from the store headers: p50 3.06 ms steady. The follow poll (1 ms) adds about half a poll. This adds up to about 4 ms, but that is arithmetic from the block age and the known poll. The follow-side share was not timed separately.
- **Unpaced p50 (2.06 ms)** is the block age alone: lines arrive continuously, and the next line cuts a block as soon as it is 2 ms old. Bursts are not worse than steady lines; `ingest` applies about 165k lines/s, so a 15k-line backlog is well under a second of CPU.
- **The full-run p99.9 got worse after the fix (52.4 to 93.8 ms), while the max got better (265 to 96.9 ms).** The cause is one episode: about 1,430 lines (317380–318812) over about 18 s of input time (10:20:41–58) all have 93–97 ms latency. Because the run is unpaced, that 18 s of input is about 0.1 s of wall time, so this is one stall of about 94 ms, then everything already in the pipe is delivered at once. The store headers show one 93.6 ms gap between consecutive `FlushedAt` stamps, so `ingest` itself made no progress for about 94 ms. No checkpoint is near. The cause is not determined (a slow write, the process not being scheduled, or other load on the machine are all possible). It is one occurrence in 961k lines and a rerun may not show it. The pre-fix full run had a 265 ms episode of its own (10:49:03).
- **The remaining tail (tens to hundreds of ms) is not explained.** In the paced runs, the worst moments (steady 86 ms at 09:45:40; outage 387 ms at 10:56:16 and a longer episode at 10:36:43–46 that peaks at 155 ms) show groups of consecutive lines with the same latency, falling by the input gap, as if delivered at one moment. Their blocks were written on time (for example line 415686: block flushed 1.5 ms after the input time, 3.8 ms after read), and no checkpoint is near. So the delay arises after the block is in the log: in `query`'s follow loop, in the pipe, or in OS scheduling of three busy processes on a shared Mac. It was not pinned to one of these; that would need timestamps inside `query`.
- Steady-window tail: 45 of 62,629 lines above 20 ms, 21 above 50 ms.

### To reproduce

```
go build -o tmp/ ./cmd/...
# pre-fix binaries (commit 1eca717) into tmp/prefix/, for the "before" runs
git worktree add /tmp/prefix-src 1eca717 && (cd /tmp/prefix-src && go build -o "$OLDPWD/tmp/prefix/" ./cmd/ingest ./cmd/query)
# ingest throughput, interleaved pre-fix and fixed, three rounds (a fresh store each time)
for i in 1 2 3; do for b in prefix fixed; do d=tmp; [ $b = prefix ] && d=tmp/prefix; rm -rf tmp/thr; $d/ingest --localview task/localview.jsonl --contracts task/contracts.jsonl --store tmp/thr; done; done
# other checkpoint spacings: 256 KB and 4 MB
tmp/ingest --localview task/localview.jsonl --contracts task/contracts.jsonl --store tmp/s256 --checkpoint-bytes 262144
tmp/ingest --localview task/localview.jsonl --contracts task/contracts.jsonl --store tmp/s4m --checkpoint-bytes 4194304
python3 -I tools/crash_test.py --runs 20
python3 -I tools/live_read_test.py
python3 -I tools/follow_test.py
# book cost (one store per spacing: tmp/s256, tmp/sall, tmp/s4m)
python3 -I tools/measure_book.py --store tmp/sall
tmp/storestat --store tmp/sall --jsonl task/localview.jsonl
# replay throughput
for i in 1 2 3; do /usr/bin/time -p tmp/query replay 2026-09-24T00:00:00Z 2026-09-25T00:00:00Z --store tmp/sall > /dev/null; done
tmp/query replay 2026-09-24T00:00:00Z 2026-09-25T00:00:00Z --store tmp/sall | wc -lc
# latency: paced windows (steady shown; outage is 10:28:00Z to 10:58:00Z); omit --paced/--start/--end for the unpaced full run
tmp/followlat --localview task/localview.jsonl --contracts task/contracts.jsonl --work tmp/fl_steady --paced --start 2026-09-24T09:40:00Z --end 2026-09-24T09:50:00Z --out tmp/stage4/lat_steady.csv
# outage window and full run (unpaced); add --bin tmp/prefix for the "before" runs
tmp/followlat --localview task/localview.jsonl --contracts task/contracts.jsonl --work tmp/fl_outage --paced --start 2026-09-24T10:28:00Z --end 2026-09-24T10:58:00Z --out tmp/stage4/lat_outage.csv
tmp/followlat --localview task/localview.jsonl --contracts task/contracts.jsonl --work tmp/fl_full --out tmp/stage4/lat_full.csv
```
