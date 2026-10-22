# dmt-orderbook

## Build and run

Requires Go 1.27 or newer. Only the standard library is used, so nothing else needs installing.

Put the two input files in `task/`:

```
task/localview.jsonl
task/contracts.jsonl
```

Build both commands into `bin/`:

```
go build -o bin/ ./cmd/ingest ./cmd/query
```

Ingest the feed into a store. The store directory must not exist yet:

```
bin/ingest --localview task/localview.jsonl --contracts task/contracts.jsonl --store store
```

Query the store. `--store` defaults to `store`, and `query` can run while `ingest` is still writing:

```
bin/query status
bin/query book PH-20260924-15 2026-09-24T10:00:00Z
bin/query top 2026-09-24T10:00:00.000Z
bin/query replay 2026-09-24T10:00:00Z 2026-09-24T10:05:00Z
bin/query replay 2026-09-24T10:00:00Z follow
```

Times are UTC in ISO 8601, with or without a fraction of a second.

Run the tests:

```
go test ./...
```
