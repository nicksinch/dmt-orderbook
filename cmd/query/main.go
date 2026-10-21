// query answers from the store only. Stage 2 implements "status".
package main

import (
	"flag"
	"fmt"
	"os"

	"dmt-orderbook/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: query status --store DIR")
		os.Exit(2)
	}
	if os.Args[1] != "status" {
		fmt.Fprintln(os.Stderr, "not implemented in stage 2")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("query status", flag.ExitOnError)
	dir := fs.String("store", "", "store directory")
	fs.Parse(os.Args[2:])
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: query status --store DIR")
		os.Exit(2)
	}
	r, err := store.Open(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query:", err)
		os.Exit(1)
	}
	n, err := r.Status()
	if err != nil {
		fmt.Fprintln(os.Stderr, "query:", err)
		os.Exit(1)
	}
	fmt.Println(n)
}
