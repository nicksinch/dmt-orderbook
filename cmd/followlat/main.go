// followlat is a dev command (test tooling, not part of the solution). It
// feeds ingest through named pipes, follows the store with `query replay
// follow`, and reports the delay between the moment a localview line was
// written into the pipe and the moment the first follow line for it arrived.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"dmt-orderbook/internal/feed"
)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "followlat:", err)
	os.Exit(1)
}

// forEachLine calls fn with each line of the file (without the newline). The
// slice is only valid during the call.
func forEachLine(path string, fn func(n int, line []byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for n := 1; ; n++ {
		line, err := r.ReadSlice('\n')
		if err == bufio.ErrBufferFull { // a line longer than the buffer: copy, then append the rest
			line = append([]byte(nil), line...)
			for err == bufio.ErrBufferFull {
				var more []byte
				more, err = r.ReadSlice('\n')
				line = append(line, more...)
			}
		}
		if len(line) > 0 {
			if ferr := fn(n, bytes.TrimRight(line, "\r\n")); ferr != nil {
				return ferr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// ms formats a duration in milliseconds with 3 decimals.
func ms(d time.Duration) string {
	return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 3, 64)
}

// waitStatus polls `query status` until it prints line, for at most 30 s (an
// ingest that flushes late may never get there before more input arrives).
func waitStatus(bin, storeDir string, line int) {
	want := strconv.Itoa(line)
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		out, err := exec.Command(filepath.Join(bin, "query"), "status", "--store", storeDir).Output()
		if err == nil && strings.TrimSpace(string(out)) == want {
			return
		}
	}
	fmt.Fprintf(os.Stderr, "followlat: store status never reached line %s; going on\n", want)
}

func main() {
	bin := flag.String("bin", "tmp", "directory holding the ingest and query binaries")
	lvPath := flag.String("localview", "", "localview.jsonl path")
	ctPath := flag.String("contracts", "", "contracts.jsonl path")
	work := flag.String("work", "", "work directory (must not exist): holds the pipes and the store")
	paced := flag.Bool("paced", false, "write the window at the speed of the timestamps")
	startArg := flag.String("start", "", "window start (with --paced)")
	endArg := flag.String("end", "", "window end (with --paced)")
	outPath := flag.String("out", "", "write one CSV line per measured input line (optional)")
	flag.Parse()
	if *lvPath == "" || *ctPath == "" || *work == "" || (*paced && (*startArg == "" || *endArg == "")) {
		fmt.Fprintln(os.Stderr, "usage: followlat [--bin DIR] --localview F --contracts F --work DIR [--paced --start T --end T] [--out F]")
		os.Exit(2)
	}
	var start, end int64
	if *paced {
		var err error
		if start, err = feed.ParseTime(*startArg); err != nil {
			fatal(err)
		}
		if end, err = feed.ParseTime(*endArg); err != nil {
			fatal(err)
		}
	}

	// 1. Pre-pass: raw and microsecond time of every localview line.
	index := map[string]int{} // raw time -> line number (first line with it)
	var times []int64         // times[i] belongs to line i+1
	var raws []string         // so does raws[i]
	dups := 0
	err := forEachLine(*lvPath, func(n int, line []byte) error {
		raw, ok := rawTimestamp(line)
		if !ok {
			return fmt.Errorf("%s:%d: no timestamp_utc", *lvPath, n)
		}
		us, err := feed.ParseTime(raw)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", *lvPath, n, err)
		}
		if _, seen := index[raw]; seen {
			dups++
		} else {
			index[raw] = n
		}
		times = append(times, us)
		raws = append(raws, raw)
		return nil
	})
	if err != nil {
		fatal(err)
	}
	burst := burstFlags(times, 1000)
	first, last := 1, len(times) // measured line range, 1-based
	if *paced {
		first, last = 0, 0
		for i, us := range times {
			if us >= start && us <= end {
				if first == 0 {
					first = i + 1
				}
				last = i + 1
			}
		}
		if first == 0 {
			fatal(fmt.Errorf("no localview line in [%s, %s]", *startArg, *endArg))
		}
	}

	// 2. Pipes and ingest.
	if err := os.Mkdir(*work, 0o755); err != nil {
		fatal(err)
	}
	lvFifo, ctFifo := filepath.Join(*work, "localview.fifo"), filepath.Join(*work, "contracts.fifo")
	storeDir := filepath.Join(*work, "store")
	for _, p := range []string{lvFifo, ctFifo} {
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			fatal(err)
		}
	}
	ingest := exec.Command(filepath.Join(*bin, "ingest"), "--localview", lvFifo, "--contracts", ctFifo, "--store", storeDir)
	var ingestErr bytes.Buffer
	ingest.Stderr = &ingestErr
	if err := ingest.Start(); err != nil {
		fatal(err)
	}
	var query *exec.Cmd // set by the follower goroutine before it closes started
	started := make(chan struct{})
	ingestDone := make(chan struct{})
	go func() { // a failed ingest ends the run, wherever the other goroutines are
		if err := ingest.Wait(); err != nil {
			fmt.Fprintf(os.Stderr, "followlat: ingest failed: %v\n%s", err, ingestErr.String())
			select {
			case <-started: // query was started; do not leave the follower running
				query.Process.Kill()
			default:
			}
			os.Exit(1)
		}
		close(ingestDone)
	}()

	// 3. Contracts writer: unpaced, in paced mode only up to the window end.
	go func() {
		f, err := os.OpenFile(ctFifo, os.O_WRONLY, 0)
		if err != nil {
			fatal(err)
		}
		defer f.Close()
		w := bufio.NewWriterSize(f, 1<<20)
		err = forEachLine(*ctPath, func(_ int, line []byte) error {
			if *paced {
				raw, ok := rawTimestamp(line)
				if !ok {
					return fmt.Errorf("%s: line without timestamp_utc", *ctPath)
				}
				us, err := feed.ParseTime(raw)
				if err != nil {
					return err
				}
				if us > end {
					return io.EOF // stop before the first later line
				}
			}
			w.Write(line)
			return w.WriteByte('\n')
		})
		if err != nil && err != io.EOF {
			fatal(err)
		}
		if err := w.Flush(); err != nil {
			fatal(err)
		}
	}()

	// 4. Follower, started once the store has a log. Its reader goroutine
	// stamps every output line and records the first sight of each input line.
	from := "2026-09-24T00:00:00Z"
	if *paced {
		from = *startArg
	}
	var lastArrival atomic.Int64     // unix nanoseconds of the latest follow line
	firstSeen := map[int]time.Time{} // owned by the reader goroutine until readerDone
	readerDone := make(chan struct{})
	go func() {
		for {
			if _, err := os.Stat(filepath.Join(storeDir, "log")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		query = exec.Command(filepath.Join(*bin, "query"), "replay", from, "follow", "--store", storeDir)
		out, err := query.StdoutPipe()
		if err != nil {
			fatal(err)
		}
		if err := query.Start(); err != nil {
			fatal(err)
		}
		close(started)
		defer close(readerDone)
		r := bufio.NewReaderSize(out, 1<<20)
		for {
			line, err := r.ReadBytes('\n')
			now := time.Now()
			if len(line) > 0 {
				lastArrival.Store(now.UnixNano())
				if t, ok := followT(line); ok {
					match(index, firstSeen, t, now)
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// 5. Localview writer.
	lf, err := os.OpenFile(lvFifo, os.O_WRONLY, 0)
	if err != nil {
		fatal(err)
	}
	written := make([]time.Time, len(times)+1)
	var wall0 time.Time
	buf := bufio.NewWriterSize(lf, 1<<20) // for the lines before the window
	err = forEachLine(*lvPath, func(n int, line []byte) error {
		if n > last {
			return io.EOF
		}
		if n < first { // before the window: unmeasured, unpaced
			buf.Write(line)
			return buf.WriteByte('\n')
		}
		if n == first {
			if err := buf.Flush(); err != nil {
				return err
			}
			<-started
			if *paced && first > 1 {
				waitStatus(*bin, storeDir, first-1)
			}
			time.Sleep(time.Second)
			wall0 = time.Now()
		}
		if *paced {
			time.Sleep(time.Until(wall0.Add(time.Duration(times[n-1]-times[first-1]) * time.Microsecond)))
		}
		if _, err := lf.Write(append(line, '\n')); err != nil {
			return err
		}
		written[n] = time.Now()
		return nil
	})
	if err != nil && err != io.EOF {
		fatal(err)
	}
	if err := lf.Close(); err != nil {
		fatal(err)
	}

	// 6. Wait for ingest. 7. Wait until follow is quiet for 2 s, then stop it.
	<-ingestDone
	lastArrival.CompareAndSwap(0, time.Now().UnixNano())
	for time.Since(time.Unix(0, lastArrival.Load())) < 2*time.Second {
		time.Sleep(100 * time.Millisecond)
	}
	query.Process.Kill()
	query.Wait()
	<-readerDone

	// 8. Report.
	var all, steady, bursty []time.Duration
	var csv strings.Builder
	type worst struct {
		line int
		lat  time.Duration
	}
	var worsts []worst
	for n := first; n <= last; n++ {
		seen, ok := firstSeen[n]
		if !ok || written[n].IsZero() {
			continue
		}
		lat := seen.Sub(written[n])
		all = append(all, lat)
		if burst[n-1] {
			bursty = append(bursty, lat)
		} else {
			steady = append(steady, lat)
		}
		worsts = append(worsts, worst{n, lat})
		fmt.Fprintf(&csv, "%d,%s,%t,%d\n", n, raws[n-1], burst[n-1], lat.Microseconds())
	}
	mode := "unpaced"
	if *paced {
		mode = "paced"
	}
	fmt.Printf("mode: %s\nwindow: lines %d-%d", mode, first, last)
	if *paced {
		fmt.Printf(" (%s to %s)", *startArg, *endArg)
	}
	fmt.Printf("\nlines written in window: %d\nlines measured: %d\n", last-first+1, len(all))
	if dups > 0 {
		fmt.Printf("note: %d localview lines share a raw time with an earlier line (first one is measured)\n", dups)
	}
	fmt.Println("latency in ms (time to first follow line for the input line):")
	fmt.Println("class    count      p50      p90      p99    p99.9      max")
	for _, c := range []struct {
		name string
		d    []time.Duration
	}{{"all", all}, {"steady", steady}, {"burst", bursty}} {
		s := summarize(c.d)
		fmt.Printf("%-7s %6d %8s %8s %8s %8s %8s\n", c.name, s.Count, ms(s.P50), ms(s.P90), ms(s.P99), ms(s.P999), ms(s.Max))
	}
	fmt.Println("worst lines: line, raw time, latency ms, gap to next input line ms")
	for k := 0; k < 5 && len(worsts) > 0; k++ {
		wi := 0
		for i, w := range worsts {
			if w.lat > worsts[wi].lat {
				wi = i
			}
		}
		w := worsts[wi]
		worsts = append(worsts[:wi], worsts[wi+1:]...)
		gap := "n/a"
		if w.line < len(times) {
			gap = ms(time.Duration(times[w.line]-times[w.line-1]) * time.Microsecond)
		}
		fmt.Printf("%d %s %s %s\n", w.line, raws[w.line-1], ms(w.lat), gap)
	}
	if *outPath != "" {
		if err := os.WriteFile(*outPath, []byte(csv.String()), 0o644); err != nil {
			fatal(err)
		}
	}
}
