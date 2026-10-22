package main

import (
	"bytes"
	"slices"
	"time"
)

// stringAfter returns the JSON string value that follows key in line (the
// key includes its quotes). Optional whitespace is allowed after the colon.
func stringAfter(line []byte, key string) (string, bool) {
	i := bytes.Index(line, []byte(key))
	if i < 0 {
		return "", false
	}
	rest := bytes.TrimLeft(line[i+len(key):], " \t")
	if len(rest) == 0 || rest[0] != ':' {
		return "", false
	}
	rest = bytes.TrimLeft(rest[1:], " \t")
	if len(rest) == 0 || rest[0] != '"' {
		return "", false
	}
	end := bytes.IndexByte(rest[1:], '"')
	if end < 0 {
		return "", false
	}
	return string(rest[1 : 1+end]), true
}

// rawTimestamp returns the raw timestamp_utc value of an input line.
func rawTimestamp(line []byte) (string, bool) {
	return stringAfter(line, `"timestamp_utc"`)
}

// followT returns the t value of a compact follow line ({"t":"...",...).
func followT(line []byte) (string, bool) {
	if !bytes.HasPrefix(line, []byte(`{"t":`)) {
		return "", false
	}
	return stringAfter(line, `"t"`)
}

// burstFlags reports, per line, whether more than threshold lines share its
// whole second. ts holds microseconds and is sorted.
func burstFlags(ts []int64, threshold int) []bool {
	flags := make([]bool, len(ts))
	for lo := 0; lo < len(ts); {
		hi := lo
		for hi < len(ts) && ts[hi]/1_000_000 == ts[lo]/1_000_000 {
			hi++
		}
		for i := lo; i < hi; i++ {
			flags[i] = hi-lo > threshold
		}
		lo = hi
	}
	return flags
}

// Summary holds latency percentiles of one class of lines.
type Summary struct {
	Count                    int
	P50, P90, P99, P999, Max time.Duration
}

// summarize computes nearest-rank percentiles: sorted d[ceil(p*n)-1].
func summarize(d []time.Duration) Summary {
	n := len(d)
	if n == 0 {
		return Summary{}
	}
	s := slices.Sorted(slices.Values(d))
	pct := func(permille int) time.Duration {
		rank := (n*permille + 999) / 1000 // integer ceil(p*n)
		return s[rank-1]
	}
	return Summary{Count: n, P50: pct(500), P90: pct(900), P99: pct(990), P999: pct(999), Max: s[n-1]}
}

// match records at as the first-seen time of the input line whose raw time is
// t. A t that is not an input timestamp is ignored.
func match(index map[string]int, firstSeen map[int]time.Time, t string, at time.Time) {
	line, ok := index[t]
	if !ok {
		return
	}
	if _, seen := firstSeen[line]; !seen {
		firstSeen[line] = at
	}
}
