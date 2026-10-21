package feed

import (
	"bufio"
	"bytes"
	"io"
)

// LineReader reads a file line by line with no limit on line length
// (real lines exceed 1.5 MB) and numbers the lines from 1.
type LineReader struct {
	br *bufio.Reader
	n  int
}

func NewLineReader(r io.Reader) *LineReader {
	return &LineReader{br: bufio.NewReaderSize(r, 1<<20)}
}

// Next returns the next line without its trailing "\n" or "\r\n", and its
// 1-based number. It returns io.EOF once no bytes are left; a final line
// without "\n" is returned first.
func (lr *LineReader) Next() (line []byte, n int, err error) {
	line, err = lr.br.ReadBytes('\n')
	if err != nil && (err != io.EOF || len(line) == 0) {
		return nil, 0, err
	}
	lr.n++
	line = bytes.TrimSuffix(line, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	return line, lr.n, nil
}
