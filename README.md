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
