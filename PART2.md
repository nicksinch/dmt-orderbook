# Part 2: The Bigger Picture

**One idea:** keep what we received, raw and forever. Derive everything else from it with one versioned program, so it can be thrown away and rebuilt. Order everything by the time *we* received it.

## Where the data lives

```
 TRADING MACHINE (per market, keeps 14 days)               CENTRAL STORAGE (object store, kept for years)
 ┌───────────────────────────────────────────┐            ┌──────────────────────────────────────────────┐
 │ capture ──► raw/<feed>/<mkt>/<day>.log    │  day file  │ raw/<feed>/<mkt>/<day>.log.zst   (forever)   │
 │                 │  (append, CRC blocks)   │──closed──► │ book/v<N>/<mkt>/<day>.store      (derived)   │
 │                 ▼                         │  + CRCs    │ grid/v<N>/<mkt>/<month>          (derived)   │
 │ builder ──► book/<mkt>/<day>.store        │            │ health/<mkt>/<day>.json          (derived)   │
 │                 │  (Part 1 format)        │            └───────────────┬──────────────────────────────┘
 │                 ▼                         │                            ▼
 │ live strategy: follow + re-read last days │            backtests, training, compliance
 └───────────────────────────────────────────┘
```

- **Capture** does one thing: it appends every message of every feed (localview, contracts, trades, forecasts) with its receive time, in blocks with a CRC, as in Part 1. It doesn't decode messages or build books, so a bug or an unexpected message in the book code can't stop recording. It calls `fsync` every 100 ms, because a power cut, unlike `kill -9`, loses the page cache.
- **The builder** is Part 1's `ingest`. It tails the raw files and writes the book store next to them. It isn't the source of truth. After a reboot it rebuilds the day from raw, which takes under a minute at 180k lines/s.
- **Files are per market per UTC day.** Each book file starts with a checkpoint, so any day can be read without the ones before it. At midnight the closed day is copied to central storage, which re-checks every block's CRC and the byte count. The trading machine deletes its copy only after that check passes and 14 days have gone by. A restart needs the last week locally (8 day files). Doubling it covers shipping that is down over a long weekend, and lets recent days be rebuilt on the spot (~150 GB per market uncompressed).

## What is raw and what is derived

| Data | Form | Reader |
|---|---|---|
| raw feeds | the messages as received, zstd | rebuilds only, never read directly |
| book store | Part 1 format (changed levels + checkpoints), zstd per block | live, backtest, compliance |
| grids | 1 row per product per second: best bid/ask, top 3 levels, last trade, forecast as known | training |
| health | gap intervals, sequence gaps, snapshot-vs-book diffs | everyone |

Forecasts are re-issued only a few times a day (as per the task text, assumed small next to localview), so each issue is stored whole as its own immutable record with the time we received it, and nothing is ever overwritten. A corrected forecast is just another issue.

## One book for everyone, and rebuilding after a bug

- Only one program, the builder, turns raw into books. Backtests, training and compliance read its output and never parse raw. The live strategy reads the same format from the same code, so the book it saw is the book the backtest sees.
- Every derived file sits under the builder's version (`v<N>`, from the git hash), and its header lists the raw files it was built from (name and size; raw files never change once the day is closed). A backtest logs the version it read, so it can be run again and give the same result.
- **After a bug:** fix it, raise the version and rebuild from raw into `v<N+1>` next to the old one. Delete `v<N>` once nobody reads it. Rebuilding is sequential within a market, because orders stay open across midnight. It is parse-bound at about 180k lines/s, so a year of one market (≈3.5 B lines) takes about 5.5 h on one core, with markets running in parallel. Only the days from the bug onwards need rebuilding.

## Nothing known too early

- Every record is keyed by our receive time, never the exchange's send time or the delivery time. A backlog that arrives 145 s late is placed when we got it, because that is when a strategy knew it.
- Readers have one call: "give me everything received ≤ T". A backtest merges all feeds and markets by receive time, with a fixed tie-break (feed, market, line number), so every run is deterministic. A forecast is joined as of T: the latest issue *received* ≤ T.
- Contract states are a history over time (as in Part 1), so a product is in scope only when it really was.
- All feeds on a site share one clock (PTP/NTP), because receive times from different machines get compared.

## Complete and correct

- **Detect:** the builder writes a health file per day. It lists `sequenceNo` gaps, per-contract `revisionNo` jumps, quiet periods longer than N s, and how much each reconnect snapshot differs from the book rebuilt from deltas (in this file: about 2% after 2 s outages, up to 35% after 5 min). Training skips flagged intervals. A backtest sees them as the strategy did.
- **Capture machine down for an hour:** a second, non-trading capture machine records the same feeds. The trading machine's record stays canonical, because it is what the strategy saw. The missing hour is filled from the backup and marked `source=backup` in the health file.
- **Shipping:** block CRCs and byte counts are re-checked on upload. A nightly job rebuilds yesterday from central raw and compares it with the shipped book file byte for byte.

## How big and how long

Measured on Part 1's 3-hour file (963k lines, 1.03 GB); a day is about 10 times that.

| | 3 h (measured) | per market-day | per market-year |
|---|---|---|---|
| raw JSONL | 1.03 GB | 10 GB | 3.8 TB |
| raw, zstd -3 | 85 MB | 0.85 GB | **310 GB** |
| book store | 69 MB | 0.69 GB | 250 GB |
| book store, zstd -3 | 16 MB | 0.16 GB | **58 GB** |

Five markets come to about 2 TB a year, which is cheap (estimated at 20 USD–25 USD per TB per month, so about $50 a month for all five markets).

| Reader | What it reads | Wait |
|---|---|---|
| live, `follow` | local book store, page cache | ~4 ms p50, 10–44 ms p99 (Part 1). The builder hop adds a few ms (estimate) |
| live restart, last week | 7 local day files, one core each | ~8 s per market on 7 cores; 57 s on one |
| compliance, one moment | one ranged read from central: checkpoint + ≤ 1 MB of changes | ~50 ms (estimate) |
| backtest, 12 months | 365 day files, ~58 GB per market | reading ≈ 50 min on one core (1.19 M lines/s) |
| training, a month | grid files (~0.8 GB/day before compression, estimate) | unmeasured; a month of grid is rebuilt from the store in ~4 min |

Where the numbers come from (Part 1, Apple M3, warm page cache; a day = 10 × the 3-hour file ≈ 9.6 M lines):

- **Live, `follow`:** measured in Part 1, from `ingest` reading a line to `follow` printing it.
  - Steady flow: 4.1 ms p50, 9.5 ms p99.
  - Outage with backlog bursts: 3.5 ms p50, 44 ms p99.
  - The capture → builder hop is new in Part 2 (a ~1 ms poll plus parsing), so "a few ms" is an estimate.
- **Live restart:** `replay` of the 3-hour file took 0.81 s, so a day takes ~8 s.
  - Each day file starts with a checkpoint, so 7 days read in parallel on 7 cores in ~8 s, or 57 s on one.
  - The 0.81 s includes printing JSON, which a strategy reading in-process skips.
  - If the strategy needs only the book now, restoring one checkpoint takes ~6 ms.
- **Compliance:** `query book` is measured at 6.0 ms median as a whole process.
  - The index gives byte offsets, so one ranged read fetches only the checkpoint and the changes up to T, not the whole day file (~160 MB).
  - The ~50 ms total is mostly the round trip to central storage, an estimate.
- **Backtest:** a year is 365 × 9.6 M ≈ 3.5 B lines; at the measured 1.19 M lines/s that is ~2,950 s ≈ 49 min.
  - Measured with the data in memory. A year is 58 GB compressed (250 GB uncompressed). An SSD reads that in 1–2 min, and zstd decompresses at ~1 GB/s per core, so the disk isn't the limit.
- **Training:** the weakest row.
  - The ~4 min is the month replay (4.1 min, scaled from the measurement).
  - Grid size is estimated at ~150 products × 86,400 s × ~60 bytes per row ≈ 0.8 GB/day before compression. Most rows repeat the one before, so it should compress well. Unmeasured.

**A 12-month backtest in a day:** reading isn't the bottleneck (≈ 50 min per market-year). Day files are decoded ahead on other cores, in parallel with the strategy, and fed to it in order on its own core. 3.5 B messages in 24 h is ~40k messages/s, so **the strategy may spend ~25 µs per message** (~5 µs if one backtest reads 5 markets). Parameter sweeps share one decoded stream. When a strategy can warm up on a week of data, the year splits into 12 monthly jobs on 12 machines.
