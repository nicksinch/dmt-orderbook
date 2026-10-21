package feed

import (
	"fmt"
	"io"
)

type Source int

const (
	SourceContracts Source = iota
	SourceLocalview
)

// Record is one decoded input line from either file.
type Record struct {
	Source    Source
	Line      int        // line number within its own file
	Localview *Localview // set iff Source == SourceLocalview
	Contract  *Contract  // set iff Source == SourceContracts
}

func (r Record) Time() int64 {
	if r.Source == SourceLocalview {
		return r.Localview.Time
	}
	return r.Contract.Time
}

// input is one file with at most one decoded record read ahead.
type input struct {
	name     string
	src      Source
	lines    *LineReader
	peeked   *Record
	done     bool
	lastTime int64
}

// fill reads the next line into peeked, unless one is already there or the file is finished.
func (in *input) fill() error {
	if in.peeked != nil || in.done {
		return nil
	}
	line, n, err := in.lines.Next()
	if err == io.EOF {
		in.done = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s:%d: %w", in.name, in.lines.n+1, err)
	}
	rec := Record{Source: in.src, Line: n}
	if in.src == SourceLocalview {
		var lv Localview
		if lv, err = DecodeLocalview(line); err == nil {
			rec.Localview = &lv
		}
	} else {
		var c Contract
		if c, err = DecodeContract(line); err == nil {
			rec.Contract = &c
		}
	}
	if err != nil {
		return fmt.Errorf("%s:%d: %w", in.name, n, err)
	}
	if t := rec.Time(); t < in.lastTime {
		return fmt.Errorf("%s:%d: time goes backwards (%d < %d)", in.name, n, t, in.lastTime)
	}
	in.lastTime = rec.Time()
	in.peeked = &rec
	return nil
}

// Merger merges the two files into one stream ordered by time.
type Merger struct {
	localview, contracts *input
	err                  error // sticky: once set, every Next returns it
}

func NewMerger(localview, contracts io.Reader, localviewName, contractsName string) *Merger {
	return &Merger{
		localview: &input{name: localviewName, src: SourceLocalview, lines: NewLineReader(localview)},
		contracts: &input{name: contractsName, src: SourceContracts, lines: NewLineReader(contracts)},
	}
}

// Next returns records ordered by time; contracts first on equal times.
// It returns io.EOF when both files are done. Errors read "<name>:<line>: <cause>".
func (m *Merger) Next() (Record, error) {
	if m.err != nil {
		return Record{}, m.err
	}
	if err := m.contracts.fill(); err != nil {
		m.err = err
		return Record{}, err
	}
	if err := m.localview.fill(); err != nil {
		m.err = err
		return Record{}, err
	}
	c, l := m.contracts, m.localview
	var from *input
	switch {
	case c.peeked == nil && l.peeked == nil:
		return Record{}, io.EOF
	case l.peeked == nil, c.peeked != nil && c.peeked.Time() <= l.peeked.Time():
		from = c
	default:
		from = l
	}
	rec := *from.peeked
	from.peeked = nil
	return rec, nil
}
