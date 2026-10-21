// Package store holds the on-disk order-book history: the byte layout of
// records, checkpoints and blocks, and (in later files) the writer and reader.
package store

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"dmt-orderbook/internal/engine"
)

// Define gives a contractId its small number. Written before the first record
// that refers to it.
type Define struct {
	PID        uint32
	ContractID string
}

// ContractRec is one change of a contract's name, area-46 state or deleted flag.
type ContractRec struct {
	T           int64
	PID         uint32
	Deleted     bool
	Name, State string
}

// ProductRec is the changed levels (new totals, 0 = gone) of one contract in one
// line, or in a checkpoint the whole book. Levels are best first.
type ProductRec struct {
	PID        uint32
	Bids, Asks []engine.Level
}

// LineRec is one localview line that changed at least one level.
type LineRec struct {
	T        int64
	Snapshot bool
	Raw      string // "" when the input time equals CanonicalTime(T)
	Products []ProductRec
}

// Record is one entry of a changes block; exactly one field is set.
type Record struct {
	Define   *Define
	Contract *ContractRec
	Line     *LineRec
}

// Checkpoint is the full state at one moment, self-contained.
type Checkpoint struct {
	IDs       []string      // pid = index into IDs
	Contracts []ContractRec // T = start of the current history entry; sorted by PID
	Books     []ProductRec  // non-empty books, sorted by PID
}

const (
	recDefine   = 1
	recContract = 2
	recLine     = 3

	flagSnapshot = 1 << 0
	flagRaw      = 1 << 1
)

// Minimum encoded sizes, used to reject counts that cannot fit in the bytes left.
const (
	minString   = 4
	minContract = 8 + 4 + 1 + minString + minString
	minProduct  = 4 + 4 + 4
	levelSize   = 16
)

// CanonicalTime is the input time format for a microsecond timestamp.
func CanonicalTime(t int64) string {
	return time.UnixMicro(t).UTC().Format("2006-01-02T15:04:05.000000+00:00")
}

// TimeRaw is the time as it appeared in the input.
func (l LineRec) TimeRaw() string {
	if l.Raw != "" {
		return l.Raw
	}
	return CanonicalTime(l.T)
}

// ---- writing ----

func appendString(b []byte, s string) []byte {
	b = binary.LittleEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}

func appendContract(b []byte, c ContractRec) []byte {
	b = binary.LittleEndian.AppendUint64(b, uint64(c.T))
	b = binary.LittleEndian.AppendUint32(b, c.PID)
	if c.Deleted {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	b = appendString(b, c.Name)
	return appendString(b, c.State)
}

func appendProduct(b []byte, p ProductRec) []byte {
	b = binary.LittleEndian.AppendUint32(b, p.PID)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(p.Bids)))
	b = binary.LittleEndian.AppendUint32(b, uint32(len(p.Asks)))
	for _, side := range [2][]engine.Level{p.Bids, p.Asks} {
		for _, l := range side {
			b = binary.LittleEndian.AppendUint64(b, uint64(l.Price))
			b = binary.LittleEndian.AppendUint64(b, uint64(l.Qty))
		}
	}
	return b
}

// AppendRecord appends the encoding of r to b.
func AppendRecord(b []byte, r Record) []byte {
	switch {
	case r.Define != nil:
		b = append(b, recDefine)
		b = binary.LittleEndian.AppendUint32(b, r.Define.PID)
		b = appendString(b, r.Define.ContractID)
	case r.Contract != nil:
		b = append(b, recContract)
		b = appendContract(b, *r.Contract)
	case r.Line != nil:
		l := r.Line
		var flags uint8
		if l.Snapshot {
			flags |= flagSnapshot
		}
		if l.Raw != "" {
			flags |= flagRaw
		}
		b = append(b, recLine)
		b = binary.LittleEndian.AppendUint64(b, uint64(l.T))
		b = append(b, flags)
		if l.Raw != "" {
			b = appendString(b, l.Raw)
		}
		b = binary.LittleEndian.AppendUint32(b, uint32(len(l.Products)))
		for _, p := range l.Products {
			b = appendProduct(b, p)
		}
	}
	return b
}

// AppendCheckpoint appends the checkpoint payload to b.
func AppendCheckpoint(b []byte, c Checkpoint) []byte {
	b = binary.LittleEndian.AppendUint32(b, uint32(len(c.IDs)))
	for _, id := range c.IDs {
		b = appendString(b, id)
	}
	b = binary.LittleEndian.AppendUint32(b, uint32(len(c.Contracts)))
	for _, k := range c.Contracts {
		b = appendContract(b, k)
	}
	b = binary.LittleEndian.AppendUint32(b, uint32(len(c.Books)))
	for _, p := range c.Books {
		b = appendProduct(b, p)
	}
	return b
}

// ---- reading ----

var errShort = errors.New("store: truncated or malformed payload")

// cursor reads little-endian values and remembers the first error. After an
// error every read returns zero values, so callers check err once at the end.
type cursor struct {
	b   []byte
	err error
}

func (c *cursor) take(n int) []byte {
	if c.err != nil || n < 0 || n > len(c.b) {
		c.err = errShort
		return nil
	}
	out := c.b[:n]
	c.b = c.b[n:]
	return out
}

func (c *cursor) u8() uint8 {
	if b := c.take(1); b != nil {
		return b[0]
	}
	return 0
}

func (c *cursor) u32() uint32 {
	if b := c.take(4); b != nil {
		return binary.LittleEndian.Uint32(b)
	}
	return 0
}

func (c *cursor) i64() int64 {
	if b := c.take(8); b != nil {
		return int64(binary.LittleEndian.Uint64(b))
	}
	return 0
}

func (c *cursor) str() string {
	n := c.u32()
	if uint64(n) > uint64(len(c.b)) {
		c.err = errShort
		return ""
	}
	return string(c.take(int(n)))
}

// count reads a uint32 count of items that each take at least minSize bytes, and
// fails if that many cannot fit in the rest, so a corrupt count cannot make
// the caller allocate a huge slice.
func (c *cursor) count(minSize int) int {
	n := c.u32()
	if c.err == nil && uint64(n)*uint64(minSize) > uint64(len(c.b)) {
		c.err = errShort
	}
	if c.err != nil {
		return 0
	}
	return int(n)
}

func (c *cursor) levels(n int) []engine.Level {
	if n == 0 || c.err != nil {
		return nil
	}
	out := make([]engine.Level, n)
	for i := range out {
		out[i] = engine.Level{Price: c.i64(), Qty: c.i64()}
	}
	return out
}

func (c *cursor) contract() ContractRec {
	return ContractRec{T: c.i64(), PID: c.u32(), Deleted: c.u8() != 0, Name: c.str(), State: c.str()}
}

func (c *cursor) product() ProductRec {
	p := ProductRec{PID: c.u32()}
	nb, na := uint64(c.u32()), uint64(c.u32())
	if c.err == nil && (nb+na)*levelSize > uint64(len(c.b)) {
		c.err = errShort
		return p
	}
	p.Bids = c.levels(int(nb))
	p.Asks = c.levels(int(na))
	return p
}

// DecodeRecords decodes a changes-block payload. Malformed input is an error.
func DecodeRecords(payload []byte) ([]Record, error) {
	c := &cursor{b: payload}
	var out []Record
	for len(c.b) > 0 && c.err == nil {
		switch typ := c.u8(); typ {
		case recDefine:
			out = append(out, Record{Define: &Define{PID: c.u32(), ContractID: c.str()}})
		case recContract:
			k := c.contract()
			out = append(out, Record{Contract: &k})
		case recLine:
			l := &LineRec{T: c.i64()}
			flags := c.u8()
			l.Snapshot = flags&flagSnapshot != 0
			if flags&flagRaw != 0 {
				l.Raw = c.str()
			}
			n := c.count(minProduct)
			for i := 0; i < n && c.err == nil; i++ {
				l.Products = append(l.Products, c.product())
			}
			out = append(out, Record{Line: l})
		default:
			return nil, fmt.Errorf("store: unknown record type %d", typ)
		}
	}
	if c.err != nil {
		return nil, c.err
	}
	return out, nil
}

// DecodeCheckpoint decodes a checkpoint-block payload. Malformed input is an error.
func DecodeCheckpoint(payload []byte) (Checkpoint, error) {
	c := &cursor{b: payload}
	var cp Checkpoint
	n := c.count(minString)
	for i := 0; i < n && c.err == nil; i++ {
		cp.IDs = append(cp.IDs, c.str())
	}
	n = c.count(minContract)
	for i := 0; i < n && c.err == nil; i++ {
		cp.Contracts = append(cp.Contracts, c.contract())
	}
	n = c.count(minProduct)
	for i := 0; i < n && c.err == nil; i++ {
		cp.Books = append(cp.Books, c.product())
	}
	if c.err != nil {
		return Checkpoint{}, c.err
	}
	if len(c.b) != 0 {
		return Checkpoint{}, errors.New("store: trailing bytes after checkpoint")
	}
	return cp, nil
}
