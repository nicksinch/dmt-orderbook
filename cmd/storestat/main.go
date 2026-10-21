// storestat is a dev command (test tooling, not part of the solution). It
// reports store size, block decode speed and StateAt latency for the note.
package main

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"time"

	"dmt-orderbook/internal/store"
)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "storestat:", err)
	os.Exit(1)
}

func size(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		fatal(err)
	}
	return fi.Size()
}

func main() {
	dir := flag.String("store", "", "store directory")
	jsonl := flag.String("jsonl", "", "input localview JSONL, for the size ratio (optional)")
	samples := flag.Int("samples", 200, "number of random StateAt calls")
	seed := flag.Uint64("seed", 1, "random seed")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: storestat --store DIR [--jsonl F] [--samples K] [--seed S]")
		os.Exit(2)
	}
	r, err := store.Open(*dir)
	if err != nil {
		fatal(err)
	}

	// Size.
	logB, idxB := size(filepath.Join(*dir, "log")), size(filepath.Join(*dir, "index"))
	var nChg, nCkp int
	var bChg, bCkp int64
	var minTS, maxTS int64
	if err := r.ForEachBlock(func(_ int64, h store.Header, p []byte) error {
		n := int64(store.HeaderSize + len(p))
		switch h.Kind {
		case store.KindChanges:
			nChg++
			bChg += n
			if nChg == 1 {
				minTS = h.FirstTS
			}
			maxTS = h.FirstTS
		case store.KindCheckpoint:
			nCkp++
			bCkp += n
		}
		return nil
	}); err != nil {
		fatal(err)
	}
	fmt.Println("== size ==")
	fmt.Printf("log bytes:        %d\n", logB)
	fmt.Printf("index bytes:      %d\n", idxB)
	fmt.Printf("total bytes:      %d\n", logB+idxB)
	fmt.Printf("changes blocks:   %d (%d bytes incl. headers)\n", nChg, bChg)
	fmt.Printf("checkpoint blocks:%d (%d bytes incl. headers)\n", nCkp, bCkp)
	if *jsonl != "" {
		js := size(*jsonl)
		fmt.Printf("jsonl bytes:      %d\n", js)
		fmt.Printf("store / jsonl:    %.2f%%\n", 100*float64(logB+idxB)/float64(js))
	}

	// Decode.
	var bytes, recs int64
	start := time.Now()
	if err := r.ForEachBlock(func(_ int64, h store.Header, p []byte) error {
		bytes += int64(store.HeaderSize + len(p))
		if h.Kind != store.KindChanges {
			return nil
		}
		rs, err := store.DecodeRecords(p)
		recs += int64(len(rs))
		return err
	}); err != nil {
		fatal(err)
	}
	el := time.Since(start).Seconds()
	fmt.Println("== decode (walk all blocks, DecodeRecords on changes payloads) ==")
	fmt.Printf("wall: %.3fs  %.1f MB/s  %.0f records/s (%d records, DEFINEs included)\n",
		el, float64(bytes)/1e6/el, float64(recs)/el, recs)

	// StateAt.
	fmt.Println("== StateAt ==")
	if nChg == 0 || *samples <= 0 {
		fmt.Println("no changes blocks or no samples")
		return
	}
	rng := rand.New(rand.NewPCG(*seed, 0))
	ms := make([]float64, 0, *samples)
	for range *samples {
		t := minTS + rng.Int64N(maxTS-minTS+1)
		s := time.Now()
		if _, err := r.StateAt(t); err != nil {
			fatal(err)
		}
		ms = append(ms, float64(time.Since(s).Microseconds())/1000)
	}
	slices.Sort(ms)
	fmt.Printf("%d samples, t uniform in [%d, %d] us\n", len(ms), minTS, maxTS)
	fmt.Printf("p50 %.2f ms  p99 %.2f ms  max %.2f ms\n", ms[len(ms)/2], ms[len(ms)*99/100], ms[len(ms)-1])
}
