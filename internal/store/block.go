package store

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
)

const (
	KindChanges    uint8 = 1
	KindCheckpoint uint8 = 2

	// HeaderSize: magic 4, crc 4, kind 1, payload_len 4, four int64 fields.
	HeaderSize = 45

	blockMagic    uint32 = 0x42544D44 // "DMTB" as little-endian bytes
	maxPayloadLen        = 64 << 20
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// Header is the fixed part of a block.
type Header struct {
	Kind       uint8
	FirstTS    int64 // changes: time of the first input line in the block; checkpoint: time of its state
	LastLVLine int64
	ReadAt     int64 // wall clock µs, latency measurement only
	FlushedAt  int64 // wall clock µs, latency measurement only
}

// AppendBlock appends the header and payload to dst. The CRC covers every
// header byte after the crc field, plus the payload.
func AppendBlock(dst []byte, h Header, payload []byte) []byte {
	start := len(dst)
	dst = binary.LittleEndian.AppendUint32(dst, blockMagic)
	dst = binary.LittleEndian.AppendUint32(dst, 0) // crc, filled in below
	dst = append(dst, h.Kind)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(payload)))
	dst = binary.LittleEndian.AppendUint64(dst, uint64(h.FirstTS))
	dst = binary.LittleEndian.AppendUint64(dst, uint64(h.LastLVLine))
	dst = binary.LittleEndian.AppendUint64(dst, uint64(h.ReadAt))
	dst = binary.LittleEndian.AppendUint64(dst, uint64(h.FlushedAt))
	dst = append(dst, payload...)
	crc := crc32.Update(0, crcTable, dst[start+8:])
	binary.LittleEndian.PutUint32(dst[start+4:], crc)
	return dst
}

// ReadBlock reads the block at off. ok is false when it is not a whole valid block
// (short, bad magic, payload_len > 64 MiB, CRC mismatch), which means the end of the store.
// err is only for real I/O errors.
func ReadBlock(f io.ReaderAt, off int64) (h Header, payload []byte, next int64, ok bool, err error) {
	var hb [HeaderSize]byte
	if short, err := readFull(f, hb[:], off); err != nil || short {
		return Header{}, nil, 0, false, err
	}
	if binary.LittleEndian.Uint32(hb[0:]) != blockMagic {
		return Header{}, nil, 0, false, nil
	}
	plen := binary.LittleEndian.Uint32(hb[9:])
	if plen > maxPayloadLen {
		return Header{}, nil, 0, false, nil
	}
	buf := make([]byte, HeaderSize+int(plen))
	copy(buf, hb[:])
	if short, err := readFull(f, buf[HeaderSize:], off+HeaderSize); err != nil || short {
		return Header{}, nil, 0, false, err
	}
	if crc32.Update(0, crcTable, buf[8:]) != binary.LittleEndian.Uint32(buf[4:]) {
		return Header{}, nil, 0, false, nil
	}
	h = Header{
		Kind:       hb[8],
		FirstTS:    int64(binary.LittleEndian.Uint64(hb[13:])),
		LastLVLine: int64(binary.LittleEndian.Uint64(hb[21:])),
		ReadAt:     int64(binary.LittleEndian.Uint64(hb[29:])),
		FlushedAt:  int64(binary.LittleEndian.Uint64(hb[37:])),
	}
	return h, buf[HeaderSize:], off + int64(len(buf)), true, nil
}

// readFull fills p from off. short is true when the file ends first, which is
// not an error for us: it means the block is not complete.
func readFull(f io.ReaderAt, p []byte, off int64) (short bool, err error) {
	if len(p) == 0 {
		return false, nil
	}
	n, err := f.ReadAt(p, off)
	if n == len(p) {
		return false, nil
	}
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true, nil
	}
	return false, err
}
