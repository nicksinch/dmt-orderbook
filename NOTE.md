# Part 1 Note: The Order-Book Store

Go, standard library only. Measured on an Apple M3 with a warm page cache. `README.md` has the full tables and the commands behind every number.

## How History Is Laid Out On Disk & Why ?
Two append-only files. 
- `log` is a sequence of blocks, each holding whole input lines, with a CRC. 
  - A *changes* block holds, per input line, the new total of each price level it changed (0 = gone). 
  - A *checkpoint* block holds all contract states and all books. 
  - Ingest writes one after every 1 MB of changes. 
- `index` holds one (time, offset) entry per checkpoint. 
###  Why
- changes alone make `book` replay from the start, and a full book per message is too big. 
- Checkpoints bound the work, and changed levels are exactly what `replay` prints. 
- Every contract is stored. Scope (`ACTI` in area 46) is applied when reading, because it changes over time and localview can arrive before its contract.

## How Big Is It Compared To localview.jsonl ?
- **68,668,045 bytes** after the whole input: **6.66% of `localview.jsonl`**
- Of that, 62.4 MB is changes, 6.3 MB is checkpoints and 960 bytes is index. 
- Integers are fixed-width, with no compression. 
- `Varints` or `zstd` would make it smaller, but I kept the format simple.

### How much work a query book for an arbitrary moment does, and how that grows with the length of the history ?

- **`query book` at any T for product P:
  - ** Binary-search the index
  - restore the checkpoint (about 105 KB)
  - apply the changes up to T (at most one spacing; 730 KB at the median). 
- It takes 6.0 ms at the median and 6.9 ms at worst, as a whole process. 
- A 256 KB spacing gives 3.4 ms for a 28% bigger store. 
- This does not grow with the length of the history, except for the O(log n) search and the checkpoints
- Checkpoints keep every contract ever seen: about 0.7 MB after a month and 7 MB after a year (about 26 ms to restore). 
- Dropping expired contracts fixes that however I have not built it.

## How Quick Your Store Reads Back ?
- `query replay` over the whole input takes 0.81 s, 
- 1.19 M lines/s. 
- Scaled linearly: 8 s for a day, 
- 57 s for a week, 4.1 min for a 30-day month. 
- Ingest runs at 165,000 lines/s, most of it JSON parsing.

## How Soon Your Data Can Be Read ?
- A test tool feeds the files to `ingest` at real-time pace and times each line until `follow` prints it. 
- Steady 10 minutes: p50 4.1 ms, p99 9.5 ms, max 86 ms. 
- The 30-minute outage with backlog bursts: p50 3.5 ms, p99 44 ms, max 387 ms. 
- What decides it: a block is written once its first line is 2 ms old, checked by a 2 ms timer (so 2–4 ms), and `follow` checks the log every 1 ms. 
- Measuring found a bug. Blocks were only written when the next line arrived, so a line just before a quiet spell waited up to 290 s. The timer fixed it. The tail above about 20 ms (45 of 62,629 steady lines) is not explained. 
- Those blocks were written on time, so the delay is in `follow`, the pipe or OS scheduling. 
- Merging two files needs the next contracts line before it releases a localview line, so the test feeds contracts ahead of localview.

## What Exactly Survives A kill -9, And How You Know ? (DONE)
- The log is append-only and written in blocks of whole input lines, each with a length and a CRC-32C, each with one `write()`. 
- Bytes passed to `write()` sit in the page cache and survive the death of the process, 
- so **a kill loses only the block still in ingest's memory (usually under 64 KB and 2–4 ms of input; at most 96 KB and 7.7 ms measured)** and can leave at most one partial block at the end.
- Readers trust a block only if it is complete and its CRC matches, and stop at the first one that isn’t, so the store is always exactly lines 1..N, where N = `last_lv_line` of the last valid block
- The above design decisions were also tested using the tool `tools/crash_test.py`
  - it killed `ingest` at random 20 times (18 mid-ingest)
  - Every store opened, and its answers for 5,997 moments matched a clean store (stopped, not killed) cut at the same line, and it also matched a separate Python reference (see [Tools README](./tools/README.md)).
  - A read loop during ingest and two `follow` runs also matched.
- **Note:** There is no `fsync`, since `kill -9` keeps what `write()` accepted. Also, `ingest` does not resume; a new run needs a new directory.


**Decisions made instead of asking DMT.**
- *Feed:* "at T" uses our `timestamp_utc`. Contracts lines go first on a tie. Every snapshot line replaces the whole book. A `sequenceNo` gap is ignored, and the next snapshot corrects the book. A deleted order may lack `qty`. A line without the snapshot header is a delta. An `orderId` belongs to its contract. Time going backwards, a malformed line or a negative `qty` stops ingest. The input is two finished files, read once.
- *Scope:* each contract keeps its state history. A `deleted` contract is out of scope. A name means the contract in scope at T. If two in-scope contracts share a name, `book`/`top` fail and `replay` prints both.
- *Output:* compact JSON, with no zero levels. A T past the stored data gets the latest state. `replay` opens with `t` = `from` as typed, sorts one line's products by name, and prints a full book when a product enters scope. A product leaving scope prints nothing.

**AI tools.** The Claude chatbot drafted the first layout. I reviewed it section by section and changed it (no varints, no lock file, every contract stored). Claude Code wrote the specs, plans, code and tests stage by stage, from my decisions. Separate AI reviewers checked each stage, and then I did. The Go output matches a slow, simple Python reference (`tools/dmt.py`) byte for byte.
