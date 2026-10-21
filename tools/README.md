# tools

Python reference (`RefBook`) and moment generator used only to check the Go engine. Test tooling, not part of the solution.

`cmd/bookat` is the Go side of the same check (also test tooling, not part of the solution). Both print one JSON line per moment, so the outputs can be diffed byte for byte.

- `dmt.py`: readers, timestamp helpers and `RefBook`, a simple slow order book.
- `gen_moments.py`: writes the moments (`top T`, `book <name> T`) to check.
- `ref_moments.py`: answers the moments with `RefBook`.
- `crash_test.py`: kills `ingest` with `kill -9` at random moments and checks the store against the reference output and a clean ingest.
- `live_read_test.py`: queries the store while `ingest` is still running and checks the answers against the reference output.

```
python3 -I tools/gen_moments.py localview.jsonl contracts.jsonl tmp/m100k.txt --limit 100000
diff <(go run ./cmd/bookat localview.jsonl contracts.jsonl tmp/m100k.txt) <(python3 -I tools/ref_moments.py localview.jsonl contracts.jsonl tmp/m100k.txt)
```

No output from `diff` means the two agree.

Stage 3 (`query book`, `top`, `replay`):

- `ref_replay.py`: the reference for `query replay <from> <to>`, built on `RefBook` only. Compare with `cmp` (example below).
- `follow_test.py`: runs `ingest` on the whole file while two `query replay … follow` processes stream, then checks each follow output equals the plain replay of the finished store. Exit 0 means pass.

```
python3 -I tools/ref_replay.py task/localview.jsonl task/contracts.jsonl 2026-09-24T10:00:00Z 2026-09-24T10:00:01Z > tmp/r_ref.out
tmp/query replay 2026-09-24T10:00:00Z 2026-09-24T10:00:01Z --store tmp/sall | cmp - tmp/r_ref.out
python3 -I tools/follow_test.py
```

`book` and `top` are checked by running each line of `tmp/mall.txt` as `tmp/query <line> --store tmp/sall` and comparing with the same line of `tmp/pyall.out`.
