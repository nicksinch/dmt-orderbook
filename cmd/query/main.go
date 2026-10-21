// query answers from the store only: status, book, top and replay.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"dmt-orderbook/internal/feed"
	"dmt-orderbook/internal/output"
	"dmt-orderbook/internal/store"
)

const usage = `usage:
  query status [--store DIR]
  query book <product-name> <T> [--store DIR]
  query top <T> [--store DIR]
  query replay <from> <to|follow> [--store DIR]`

// usageError marks a mistake in the command line (exit 2).
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// parseArgs splits os.Args[1:] into the subcommand, its positional arguments
// and the store directory. --store DIR or --store=DIR may appear anywhere
// after the subcommand.
func parseArgs(args []string) (cmd string, pos []string, storeDir string, err error) {
	if len(args) == 0 {
		return "", nil, "", usageError{"missing subcommand"}
	}
	cmd, storeDir = args[0], "store"
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--store":
			if i+1 >= len(rest) {
				return "", nil, "", usageError{"--store needs a directory"}
			}
			i++
			storeDir = rest[i]
		case strings.HasPrefix(a, "--store="):
			storeDir = strings.TrimPrefix(a, "--store=")
		default:
			pos = append(pos, a)
		}
	}
	return cmd, pos, storeDir, nil
}

func parseT(s string) (int64, error) {
	t, err := feed.ParseTime(s)
	if err != nil {
		return 0, usageError{err.Error()}
	}
	return t, nil
}

func run(args []string, stdout io.Writer) error {
	cmd, pos, dir, err := parseArgs(args)
	if err != nil {
		return err
	}
	want := map[string]int{"status": 0, "book": 2, "top": 1, "replay": 2}
	n, ok := want[cmd]
	if !ok {
		return usageError{"unknown subcommand " + cmd}
	}
	if len(pos) != n {
		return usageError{fmt.Sprintf("%s takes %d argument(s)", cmd, n)}
	}
	// Validate times before touching the store.
	var t, to int64
	follow := false
	switch cmd {
	case "book":
		t, err = parseT(pos[1])
	case "top":
		t, err = parseT(pos[0])
	case "replay":
		if t, err = parseT(pos[0]); err != nil {
			break
		}
		if pos[1] == "follow" {
			follow = true
		} else if to, err = parseT(pos[1]); err == nil && to < t {
			err = usageError{"to is before from"}
		}
	}
	if err != nil {
		return err
	}

	r, err := store.Open(dir)
	if err != nil {
		return err
	}
	switch cmd {
	case "status":
		n, err := r.Status()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, n)
		return err
	case "book", "top":
		e, err := r.StateAt(t)
		if err != nil {
			return err
		}
		var v any
		if cmd == "book" {
			v, err = output.Book(e, pos[0], t)
		} else {
			v, err = output.Top(e, t)
		}
		if err != nil {
			return err
		}
		return output.NewEncoder(stdout).Encode(v)
	default:
		return runReplay(r, pos[0], t, to, follow, stdout)
	}
}

func main() {
	err := run(os.Args[1:], os.Stdout)
	if err == nil {
		return
	}
	var ue usageError
	if errors.As(err, &ue) {
		fmt.Fprintf(os.Stderr, "query: %v\n%s\n", err, usage)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, "query:", err)
	os.Exit(1)
}
