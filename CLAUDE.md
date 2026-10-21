# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Context

This is a take-home task for a Software Engineer interview at Dark Matter Trading (DMT). **`task/software_engineering_task.md` is the source of truth. Read it before any design or code work.** From DMT the repo contains only that brief and the two `.jsonl` data files.

In short, the task has two parts:
- **Part 1, code.** Write `ingest`, which turns the feed into an order-book history store on disk of your own design. Write `query`, which answers from that store only.
- **Part 2, writing.** A design of at most two pages for the multi-market, multi-year system. It also needs a note of at most one page on the Part 1 layout, trade-offs and measurements.

The language that has been chosen is `Golang`.

Keep the code simple and explainable, but it should comply 100% to the functional and non-functional requirements of the task.

## Hard requirements from the brief (easy to miss)

- **No database and no existing file format** (SQLite, DuckDB, Parquet, …). General-purpose compression such as zstd or lz4 is allowed.
- **`kill -9` safety:** after `ingest` is killed at a random moment, the store must still open and must hold the history exactly up to some line. Nothing half-written, nothing missing before that line.
- **Read while writing:** `query` must work while `ingest` is still running and see data from a moment ago. `replay … follow` keeps streaming new changes as they are stored.
- **Commands:**
  - `query status`: the last localview line number that is fully in the store.
  - `query book <product-name> <T>`: the full aggregated book for one product as it was at time T.
  - `query top <T>`: best bid and best ask for every product, with `null` for an empty side.
  - `query replay <from> <to|follow>`: first a full book per product with `t`=`from`, then one line per product per input message that changed it. Each of those lines lists only the price levels that changed, with their new totals; 0 means the level disappeared. `t` is the `timestamp_utc` of the line that caused it, printed in its raw form as in the brief's example (`...+00:00`).
- "At T" means after applying every line with `timestamp_utc` ≤ T. Both files are consumed together in `timestamp_utc` order.
- Scope: only contracts whose state is `ACTI` in delivery area 46 (Bulgaria). Products are queried by **name** and identified in localview by `contractId`.
- Order semantics: an order is keyed by `orderId`, and a new version replaces the old one. `"deleted": true` removes the order. A message with `x-nps-snapshot: "true"` **replaces** the whole book: it lists every open contract, and an empty list means no orders.
- Prices are in EUR cents and quantities in kW, all integers. Output is JSON exactly as shown in the brief.
- Correctness comes first. After that, these get measured and defended:
  - store size compared with the JSONL
  - cost of `query book` and how it grows with the length of the history
  - throughput of `replay` over the whole input, extrapolated to a week and a month
  - ingest-to-`follow` latency
  - what survives `kill -9`, and how you know
- The note must record every decision made instead of asking DMT. Unclear points are better taken to DMT as questions.

## Observed in the full data (not guaranteed)

These come from one 3-hour file, so they are observations, not guarantees. The code must not depend on them. They show what the code has to cope with, what to measure, and which edge cases have no data to test against (those are questions for DMT). They were checked during data exploration with stdlib scripts, `trace_contract.py` and `explore.ipynb` run with `LIMIT = None` (in another repo -> `/Users/nickkirov/Github-Repos/dark-matter-task`):
- Both files are sorted by `timestamp_utc`, and no localview line shares a timestamp with a contracts line.
- Each name maps to exactly one `contractId`.
- Every snapshot is a single line; there are 8 (line 1, then 7 reconnects). `x-nps-sequenceNo` resets to 1 only at snapshots and has no other gaps. `revisionNo` goes up by exactly 1 per contract per delta and continues across reconnects.
- The book rebuilt from deltas matches each reconnect snapshot except for churn during the outage: about 2% of orders differ after gaps of about 1.5–2 s, and 25–35% after gaps of 100–290 s.
- `task/contracts.jsonl` is the full catalogue (about 640 contracts, from 2 days back to 3 days ahead), re-sent in a burst at the start and at each reconnect. Only about 58 contracts are `ACTI` in area 46 at the start. Gate closures (`ACTI`→`IACT`, every 15 min) and the next day's opening (120 products at 12:00 UTC) always arrive as new lines.
- Deleted orders arrive with `qty: 0`. A re-sent `orderId` usually changes qty; price changes are usually a new id plus a delete.
- Product names count periods in Central European time; `dlvryStart`/`dlvryEnd` are UTC.
- Reconnect snapshots come at irregular times, 4 to 59 minutes apart. Each is 1.0–1.5 MB and lists about 485 contracts, most of them out of scope. They can't serve as regular checkpoints, so the store needs checkpoints of its own.
- Rate: normally 80–140 lines/s at about 1 KB per line. Five of the seven reconnects fall in an outage from 10:30 to 10:56, when the rate drops to near 0 for minutes at a time.
- Backlog bursts: after some reconnects the feed replays a backlog. Up to about 15k lines arrive within seconds, up to 145 s after the exchange sent them (`x-nps-sent-at`). Ingest throughput and `follow` latency must be measured in these bursts, not just in steady state. "At T" uses our receive time (`timestamp_utc`), not the exchange's send time.
- Every delta line touches exactly one contract (design for several anyway). The median delta has 2 orders, and about 48% of order entries are deletes.
- No contract goes back to `ACTI` after `IACT`. Every `contractId` in localview is described in `task/contracts.jsonl` before its first localview line.
- Never seen: a crossed book (best bid ≥ best ask), an `orderId` moving to another contract or side, or `qty: 0` without `deleted`.
- Open oddity: order `X30609986168` (`NX_610556`) was created at 09:17 but is missing from the 09:25:13 snapshot, then arrives as new 50 ms later.

## Decisions made (the brief doesn't settle them; record each in the note)

These were checked against the Nord Pool docs linked in the brief.
- **Store everything and filter at query time.** Ingest stores every contract and order, whatever its state or whether its contract is known yet. The docs say topics have no ordering between them: just after login, a localview snapshot can arrive before the contracts message that describes its contracts. On an exact timestamp tie between the two files, apply contracts first. That is a fixed choice, not something the docs require.
- **Contract state is a history over time.** Store each contract's state changes with their times, as a plain value, not just the latest state. "In scope at T" means the state at T is `ACTI` in area 46. Any other state (`IACT`, `HIBE`, `FRZN` or something new) is simply out of scope, with no special code. Never clear a contract's orders because it left `ACTI`; if orders really disappear, the feed sends deletes. Per the docs, `IACT` is either new (will open) or expired (never reopens), while `HIBE` is closed but can reopen. Only `ACTI` and `IACT` occur in this data.
- **Key by `contractId`, keep a plain name-to-ID map.** Names are not unique across markets (the docs' own example has the same name for an `NI_` and an `NX_` contract), but every contract here is `NX_`. If a name ever maps to two IDs, use the one that is `ACTI` at T. Mention this in Part 2 for multi-market.
- **Every snapshot line is a full replacement.** The docs don't say a snapshot can span several messages. Snapshots can also arrive without a reconnect. On a `x-nps-sequenceNo` gap, apply what arrives and let the next snapshot correct the book (the docs tell a live client to resubscribe, which a recording can't do). Optionally log the gap during ingest.
- **Parser defaults from the docs:** a deleted order may have no `qty` field, and a line without the `x-nps-snapshot` header is a delta. Neither happens in this data.

## Data files (never load whole into memory)

- `task/localview.jsonl`: about 1 GB and 963,161 lines, a STOMP feed. Each line has `timestamp_utc`, `stomp_headers` (`x-nps-snapshot`, `x-nps-sequenceNo`, `x-nps-sent-at`), and a `body` list of per-contract updates (`contractId`, `revisionNo`, `buyOrders`/`sellOrders` with `orderId`/`price`/`qty`/`deleted`).
- `task/contracts.jsonl`: about 30 MB and 5.5k lines, one contract per line. `body.dlvryAreaState[]` holds the state for each area.
- In the data files every `timestamp_utc` has the form `2026-09-24T09:25:13.543924+00:00`. That is real microsecond precision: the last digit is spread evenly across 0–9, so these are not milliseconds padded with zeros. `x-nps-sent-at` is coarser, a millisecond epoch. Store times as int64 microseconds since the Unix epoch. The query arguments in the brief use other forms (`...12.345Z`, `...10:00:00Z` with no fraction), so pad them to microseconds when parsing. Parse every timestamp to an integer before comparing, never compare them as strings, and print `t` in its raw input form.
- Develop against the first 100k lines. A real day is about 10 times this file, and a week about 70 times.
