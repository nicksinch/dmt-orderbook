# tools: the Python reference

Test tooling, not part of the solution.

## What it is

`dmt.py` holds `RefBook`, a deliberately slow and simple order book of about 100 lines ("Slow on purpose: easy to trust"). It was written during data exploration, before any Go code, and it never touches the store. `ref_moments.py` drives it. It reads both JSONL files once, merges them by `timestamp_utc` (contracts first on a tie) and applies the lines one by one. Whenever it reaches a moment from `tmp/mall.txt`, it prints the `book`/`top` answer as compact JSON. Those answers are saved in `tmp/pyall.out`, which `crash_test.py` and `live_read_test.py` compare against.

## How it builds state

- `orders[contractId][orderId] = (is_buy, price, qty)`. A delete pops the order and anything else overwrites it. A snapshot line wipes all orders and then refills them.
- `contracts[contractId]` keeps only the latest record. Because it streams forward in time, "latest so far" is the same as "the state at T".
- Price levels are never stored. On every query it sums all the orders of a contract into levels and sorts them.

## How Go differs

The Go side is `internal/engine` plus `internal/store`.

| | Python `RefBook` | Go |
|---|---|---|
| Source of truth | the JSONL itself, every time | the store only (`log` + `index`) |
| Price levels | summed from individual orders at query time | level totals kept up to date per line; only the *changed levels* are written to the log |
| Getting to T | replay everything from line 1 | binary-search the index, restore a checkpoint, apply level changes up to T |
| Contract state | latest only (fine for a forward stream) | full history per contract (`contracts map[string][]contractState`), because queries jump to any T |
| Name → ID | last name wins | every ID seen with the name, then the one in scope at T |
| Durability | none | blocks, CRC, checkpoints |

## How it catches bugs

Both sides answer the same questions from the same input, but they get there by different routes. Most of what makes the Go side complex simply doesn't exist in Python: the change encoding, checkpoint write and restore, index search, block framing, and diffing levels per line. A bug in any of those, say a lost level change, a checkpoint that misses a contract, or an off-by-one at the T boundary, gives a different JSON line and the comparison fails. In `crash_test.py`, the killed-vs-clean comparison can't catch that kind of bug, because both stores go through the same Go code.

## What it can't catch

Anything both sides share. They share the decisions (contracts first on a tie, snapshot = full replacement, scope filtered at read time), which they implement independently but identically. They also share the moments file. If a decision itself is wrong, both agree and the test passes. That's why those choices are listed in the note as decisions rather than verified facts.

`RefBook` doesn't implement the "two in-scope contracts with the same name" rule. It just takes the last name seen. That case never happens in this data, so the comparison says nothing about it. The Go unit tests are the only thing covering it.

## Files and commands

`cmd/bookat` is the Go side of the same check (also test tooling, not part of the solution). Both print one JSON line per moment, so the outputs can be diffed byte for byte.

- `dmt.py`: readers, timestamp helpers and `RefBook`.
- `gen_moments.py`: writes the moments (`top T`, `book <name> T`) to check.
- `ref_moments.py`: answers the moments with `RefBook`.
- `ref_replay.py`: the reference for `query replay <from> <to>`, built on `RefBook` only.
- `crash_test.py`: kills `ingest` with `kill -9` at random moments and checks the store against the reference output and a clean ingest.
- `live_read_test.py`: queries the store while `ingest` is still running and checks the answers against the reference output.
- `follow_test.py`: runs `ingest` on the whole file while two `query replay … follow` processes stream, then checks each follow output equals the plain replay of the finished store. Exit 0 means pass.

Go engine against `RefBook` (no output from `diff` means the two agree):

```
python3 -I tools/gen_moments.py localview.jsonl contracts.jsonl tmp/m100k.txt --limit 100000
diff <(go run ./cmd/bookat localview.jsonl contracts.jsonl tmp/m100k.txt) <(python3 -I tools/ref_moments.py localview.jsonl contracts.jsonl tmp/m100k.txt)
```

`replay`, `follow` and `kill -9`:

```
python3 -I tools/ref_replay.py task/localview.jsonl task/contracts.jsonl 2026-09-24T10:00:00Z 2026-09-24T10:00:01Z > tmp/r_ref.out
tmp/query replay 2026-09-24T10:00:00Z 2026-09-24T10:00:01Z --store tmp/sall | cmp - tmp/r_ref.out
python3 -I tools/follow_test.py
python3 -I tools/crash_test.py --runs 20
```

`book` and `top` are checked by running each line of `tmp/mall.txt` as `tmp/query <line> --store tmp/sall` and comparing with the same line of `tmp/pyall.out`.
