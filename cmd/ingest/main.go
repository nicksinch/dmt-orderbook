// ingest reads both input files in timestamp order and writes the order-book store.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"dmt-orderbook/internal/feed"
	"dmt-orderbook/internal/store"
)

func main() {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError) // exits 2 on bad flags
	lvPath := fs.String("localview", "", "localview.jsonl path")
	ctPath := fs.String("contracts", "", "contracts.jsonl path")
	dir := fs.String("store", "", "store directory")
	stopAfter := fs.Int("stop-after", 0, "stop after this many localview lines (0 = all)")
	cpBytes := fs.Int64("checkpoint-bytes", store.DefaultOptions().CheckpointBytes, "log bytes between checkpoints")
	fs.Parse(os.Args[1:])
	if *lvPath == "" || *ctPath == "" || *dir == "" || *cpBytes <= 0 {
		fmt.Fprintln(os.Stderr, "usage: ingest --localview F --contracts F --store DIR [--stop-after N] [--checkpoint-bytes B]")
		os.Exit(2)
	}
	if err := run(*lvPath, *ctPath, *dir, *stopAfter, *cpBytes); err != nil {
		fmt.Fprintln(os.Stderr, "ingest:", err)
		os.Exit(1)
	}
}

func run(lvPath, ctPath, dir string, stopAfter int, cpBytes int64) error {
	lf, err := os.Open(lvPath)
	if err != nil {
		return err
	}
	defer lf.Close()
	cf, err := os.Open(ctPath)
	if err != nil {
		return err
	}
	defer cf.Close()

	o := store.DefaultOptions()
	o.CheckpointBytes = cpBytes
	start := time.Now()
	st, err := store.Ingest(feed.NewMerger(lf, cf, lvPath, ctPath), dir, o, stopAfter)
	if err != nil {
		return err
	}
	elapsed := time.Since(start)
	fmt.Fprintf(os.Stderr, "localview lines: %d, contracts lines: %d, elapsed: %s, localview lines/s: %.0f, log bytes: %d, index bytes: %d\n",
		st.Localview, st.Contracts, elapsed, float64(st.Localview)/elapsed.Seconds(), st.LogBytes, st.IndexBytes)
	return nil
}
