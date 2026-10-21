package feed

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestLineReaderLongLine(t *testing.T) {
	long := strings.Repeat("x", 200000)
	lr := NewLineReader(strings.NewReader(long + "\nb\n"))
	line, n, err := lr.Next()
	if err != nil || n != 1 || !bytes.Equal(line, []byte(long)) {
		t.Fatalf("long line: n=%d err=%v len=%d", n, err, len(line))
	}
	line, n, err = lr.Next()
	if err != nil || n != 2 || string(line) != "b" {
		t.Fatalf("second line: %q n=%d err=%v", line, n, err)
	}
	if _, _, err = lr.Next(); err != io.EOF {
		t.Fatalf("want io.EOF, got %v", err)
	}
}

func TestLineReaderLastLineWithoutNewline(t *testing.T) {
	lr := NewLineReader(strings.NewReader("a\nb"))
	for i, want := range []string{"a", "b"} {
		line, n, err := lr.Next()
		if err != nil || n != i+1 || string(line) != want {
			t.Fatalf("line %d: %q n=%d err=%v", i+1, line, n, err)
		}
	}
	if _, _, err := lr.Next(); err != io.EOF {
		t.Fatalf("want io.EOF, got %v", err)
	}
}

func TestLineReaderCRLFAndTrailingNewline(t *testing.T) {
	lr := NewLineReader(strings.NewReader("a\r\nb\r\n"))
	for i, want := range []string{"a", "b"} {
		line, n, err := lr.Next()
		if err != nil || n != i+1 || string(line) != want {
			t.Fatalf("line %d: %q n=%d err=%v", i+1, line, n, err)
		}
	}
	if _, _, err := lr.Next(); err != io.EOF {
		t.Fatalf("want io.EOF, got %v", err)
	}
}
