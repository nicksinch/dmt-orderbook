# tools

Python reference (`RefBook`) and moment generator used only to check the Go engine. Test tooling, not part of the solution.

`cmd/bookat` is the Go side of the same check (also test tooling, not part of the solution). Both print one JSON line per moment, so the outputs can be diffed byte for byte.

- `dmt.py`: readers, timestamp helpers and `RefBook`, a simple slow order book.
- `gen_moments.py`: writes the moments (`top T`, `book <name> T`) to check.
- `ref_moments.py`: answers the moments with `RefBook`.

```
python3 -I tools/gen_moments.py localview.jsonl contracts.jsonl tmp/m100k.txt --limit 100000
diff <(go run ./cmd/bookat localview.jsonl contracts.jsonl tmp/m100k.txt) <(python3 -I tools/ref_moments.py localview.jsonl contracts.jsonl tmp/m100k.txt)
```

No output from `diff` means the two agree.
