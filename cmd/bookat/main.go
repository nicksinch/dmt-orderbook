// bookat is a dev command: it replays both input files through the engine and
// prints the book or top answers at the moments listed in a file.
// It is test tooling, not part of the solution.
package main

import (
	"fmt"
	"os"
	"time"

	"dmt-orderbook/internal/feed"
	"dmt-orderbook/internal/store"
)

func main() {
	if len(os.Args) == 4 && os.Args[1] == "--store" {
		if err := bookatStore(os.Args[2], os.Args[3]); err != nil {
			fmt.Fprintln(os.Stderr, "bookat:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: bookat <localview.jsonl> <contracts.jsonl> <moments.txt>")
		fmt.Fprintln(os.Stderr, "       bookat --store <dir> <moments.txt>")
		os.Exit(2)
	}
	if err := bookat(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, "bookat:", err)
		os.Exit(1)
	}
}

// bookatStore answers the moments from the store instead of the input files.
func bookatStore(dir, momentsPath string) error {
	mf, err := os.Open(momentsPath)
	if err != nil {
		return err
	}
	defer mf.Close()
	ms, err := parseMoments(mf)
	if err != nil {
		return err
	}
	r, err := store.Open(dir)
	if err != nil {
		return err
	}
	start := time.Now()
	if err := runMomentsStore(r, ms, os.Stdout); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "moments: %d, elapsed: %s\n", len(ms), time.Since(start))
	return nil
}

func bookat(localviewPath, contractsPath, momentsPath string) error {
	mf, err := os.Open(momentsPath)
	if err != nil {
		return err
	}
	defer mf.Close()
	ms, err := parseMoments(mf)
	if err != nil {
		return err
	}
	lf, err := os.Open(localviewPath)
	if err != nil {
		return err
	}
	defer lf.Close()
	cf, err := os.Open(contractsPath)
	if err != nil {
		return err
	}
	defer cf.Close()

	m := feed.NewMerger(lf, cf, localviewPath, contractsPath)
	start := time.Now()
	st, err := runMoments(m, ms, os.Stdout)
	if err != nil {
		return err
	}
	elapsed := time.Since(start)
	fmt.Fprintf(os.Stderr, "localview lines: %d, contracts lines: %d, elapsed: %s, localview lines/s: %.0f\n",
		st.Localview, st.Contracts, elapsed, float64(st.Localview)/elapsed.Seconds())
	return nil
}
