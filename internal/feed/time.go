package feed

import (
	"fmt"
	"time"
)

// ParseTime parses an RFC 3339 timestamp and returns it as microseconds since
// the Unix epoch. Sub-microsecond digits are truncated.
func ParseTime(s string) (int64, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, fmt.Errorf("parse time %q: %w", s, err)
	}
	return t.UnixMicro(), nil
}
