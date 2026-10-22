// storestat is a dev command (test tooling, not part of the solution). It
// reports store size, block decode speed and StateAt latency for the note.
package main

import (
	"bufio"
	"encoding/binary"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"time"

	"dmt-orderbook/internal/feed"
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
	checkpoints := flag.Bool("checkpoints", false, "print one row per checkpoint (time contracts books bytes) and exit")
	work := flag.String("work", "", "file with one time per line: print the bytes a book rebuild reads for each, and exit")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: storestat --store DIR [--jsonl F] [--samples K] [--seed S] [--checkpoints] [--work FILE]")
		os.Exit(2)
	}
	r, err := store.Open(*dir)
	if err != nil {
		fatal(err)
	}

	if *checkpoints {
		printCheckpoints(r)
		return
	}
	if *work != "" {
		printWork(r, *dir, *work)
		return
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

// printCheckpoints prints one row per checkpoint block: time, contracts, books, bytes.
func printCheckpoints(r *store.Reader) {
	if err := r.ForEachBlock(func(_ int64, h store.Header, p []byte) error {
		if h.Kind != store.KindCheckpoint {
			return nil
		}
		cp, err := store.DecodeCheckpoint(p)
		if err != nil {
			return err
		}
		fmt.Printf("%s %d %d %d\n", store.CanonicalTime(h.FirstTS), len(cp.Contracts), len(cp.Books), store.HeaderSize+len(p))
		return nil
	}); err != nil {
		fatal(err)
	}
}

// printWork prints, for each time T in file, the bytes a book rebuild reads
// (StateAt): the checkpoint block (last index entry with time <= T), then every
// block after it in order, checkpoint blocks included, up to and including the
// first changes block with a line or contract record after T, where replay
// stops (or to the end of the log if there is none). Whole blocks, headers
// included. No index entry <= T means offset 0, which is always the start
// checkpoint, because ingest writes it first.
func printWork(r *store.Reader, dir, file string) {
	type block struct {
		off, size, last int64 // last: time of the last non-DEFINE record (changes blocks)
		ckpt            bool
	}
	var blocks []block
	if err := r.ForEachBlock(func(off int64, h store.Header, p []byte) error {
		b := block{off: off, size: int64(store.HeaderSize + len(p)), ckpt: h.Kind == store.KindCheckpoint}
		if h.Kind == store.KindChanges {
			recs, err := store.DecodeRecords(p)
			if err != nil {
				return err
			}
			for _, rec := range recs {
				switch {
				case rec.Contract != nil:
					b.last = rec.Contract.T
				case rec.Line != nil:
					b.last = rec.Line.T
				}
			}
		}
		blocks = append(blocks, b)
		return nil
	}); err != nil {
		fatal(err)
	}
	idx, err := os.ReadFile(filepath.Join(dir, "index"))
	if err != nil {
		fatal(err)
	}
	f, err := os.Open(file)
	if err != nil {
		fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		t, err := feed.ParseTime(line)
		if err != nil {
			fatal(err)
		}
		var ckOff, ckBytes int64
		for i := 0; i+16 <= len(idx) && int64(binary.LittleEndian.Uint64(idx[i:])) <= t; i += 16 {
			ckOff = int64(binary.LittleEndian.Uint64(idx[i+8:]))
		}
		var scanned int64
		after := false // past the checkpoint used
		for _, b := range blocks {
			if b.off == ckOff {
				ckBytes, after = b.size, true
				continue
			}
			if !after {
				continue
			}
			scanned += b.size
			if !b.ckpt && b.last > t {
				break
			}
		}
		fmt.Printf("%s %d %d\n", line, ckBytes, scanned)
	}
	if err := sc.Err(); err != nil {
		fatal(err)
	}
}
