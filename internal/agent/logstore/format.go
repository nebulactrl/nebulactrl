// Package logstore is the agent's log history: an append-only, gzip-compressed,
// time-indexed store of the runtime output of every managed container, kept per
// service under <state-dir>/logs/<serviceId>/ and bounded by age and size.
//
// Layout of one service directory:
//
//	<start>.seg  concatenated gzip members; one member ("block") holds up to
//	             BlockMaxLines lines. A crash tears at most the last member.
//	<start>.idx  one fixed-size entry per block (offset, sizes, first sequence,
//	             time range), appended after the block's data is written. It is
//	             rebuilt from the data file when missing or behind.
//
// <start> is the segment's first timestamp in nanoseconds, zero-padded, so the
// files sort by age. A segment is sealed (never touched again) when it reaches
// SegmentMaxBytes or SegmentMaxAge, or when the agent restarts. Lines carry a
// per-service sequence number that increases with every append: blocks are
// physically ordered by it, it is the pagination cursor of queries and the
// position of drains.
package logstore

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Record is one stored line.
type Record struct {
	Seq          uint64
	TS           int64 // unix nanoseconds
	Replica      int
	DeploymentID string
	Text         string
}

// MaxLineBytes truncates pathological lines when they are stored.
const MaxLineBytes = 16 << 10

// encodeRecord appends the block form of r: seq TAB ts TAB replica TAB
// deployment TAB text LF. The text never contains LF (lines are split before
// they reach the store); a stray CR/LF is replaced by a space.
func encodeRecord(dst []byte, r Record) []byte {
	dst = strconv.AppendUint(dst, r.Seq, 10)
	dst = append(dst, '\t')
	dst = strconv.AppendInt(dst, r.TS, 10)
	dst = append(dst, '\t')
	dst = strconv.AppendInt(dst, int64(r.Replica), 10)
	dst = append(dst, '\t')
	dst = append(dst, r.DeploymentID...)
	dst = append(dst, '\t')
	text := r.Text
	if len(text) > MaxLineBytes {
		text = truncateUTF8(text, MaxLineBytes)
	}
	start := len(dst)
	dst = append(dst, text...)
	for i := start; i < len(dst); i++ {
		if dst[i] == '\n' || dst[i] == '\r' {
			dst[i] = ' '
		}
	}
	return append(dst, '\n')
}

// truncateUTF8 cuts s to at most n bytes on a rune boundary.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

var errBadRecord = errors.New("logstore: malformed record")

// decodeRecord parses one block line (without the trailing LF).
func decodeRecord(line []byte) (Record, error) {
	var f [4][]byte
	rest := line
	for i := range f {
		j := bytes.IndexByte(rest, '\t')
		if j < 0 {
			return Record{}, errBadRecord
		}
		f[i], rest = rest[:j], rest[j+1:]
	}
	seq, err := strconv.ParseUint(string(f[0]), 10, 64)
	if err != nil {
		return Record{}, errBadRecord
	}
	ts, err := strconv.ParseInt(string(f[1]), 10, 64)
	if err != nil {
		return Record{}, errBadRecord
	}
	rep, err := strconv.Atoi(string(f[2]))
	if err != nil {
		return Record{}, errBadRecord
	}
	return Record{Seq: seq, TS: ts, Replica: rep, DeploymentID: string(f[3]), Text: string(rest)}, nil
}

// decodeBlock parses a decompressed block.
func decodeBlock(b []byte) ([]Record, error) {
	var out []Record
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			return out, errBadRecord
		}
		r, err := decodeRecord(b[:i])
		if err != nil {
			return out, err
		}
		out = append(out, r)
		b = b[i+1:]
	}
	return out, nil
}

// ---- index ----------------------------------------------------------------

const (
	idxMagic     = "NLIDX001"
	idxEntrySize = 48
)

// block locates one gzip member and summarizes its lines.
type block struct {
	Offset   int64  // of the member in the .seg file
	CLen     uint32 // compressed length
	ULen     uint32 // uncompressed length
	Lines    uint32
	FirstSeq uint64
	MinTS    int64
	MaxTS    int64
}

func (b block) lastSeq() uint64 { return b.FirstSeq + uint64(b.Lines) - 1 }
func (b block) end() int64      { return b.Offset + int64(b.CLen) }

func (b block) marshal(dst []byte) []byte {
	var e [idxEntrySize]byte
	binary.LittleEndian.PutUint64(e[0:], uint64(b.Offset))
	binary.LittleEndian.PutUint32(e[8:], b.CLen)
	binary.LittleEndian.PutUint32(e[12:], b.ULen)
	binary.LittleEndian.PutUint32(e[16:], b.Lines)
	binary.LittleEndian.PutUint64(e[24:], b.FirstSeq)
	binary.LittleEndian.PutUint64(e[32:], uint64(b.MinTS))
	binary.LittleEndian.PutUint64(e[40:], uint64(b.MaxTS))
	return append(dst, e[:]...)
}

func unmarshalBlock(e []byte) block {
	return block{
		Offset:   int64(binary.LittleEndian.Uint64(e[0:])),
		CLen:     binary.LittleEndian.Uint32(e[8:]),
		ULen:     binary.LittleEndian.Uint32(e[12:]),
		Lines:    binary.LittleEndian.Uint32(e[16:]),
		FirstSeq: binary.LittleEndian.Uint64(e[24:]),
		MinTS:    int64(binary.LittleEndian.Uint64(e[32:])),
		MaxTS:    int64(binary.LittleEndian.Uint64(e[40:])),
	}
}

// parseIndex returns the entries of an index file, stopping at the first
// malformed one (a torn tail).
func parseIndex(data []byte) ([]block, error) {
	if len(data) < len(idxMagic) || string(data[:len(idxMagic)]) != idxMagic {
		return nil, fmt.Errorf("logstore: bad index header")
	}
	data = data[len(idxMagic):]
	out := make([]block, 0, len(data)/idxEntrySize)
	for len(data) >= idxEntrySize {
		b := unmarshalBlock(data[:idxEntrySize])
		if b.Lines == 0 || b.CLen == 0 {
			break
		}
		if n := len(out); n > 0 && (b.Offset != out[n-1].end() || b.FirstSeq <= out[n-1].lastSeq()) {
			break
		}
		out = append(out, b)
		data = data[idxEntrySize:]
	}
	return out, nil
}

// segment file names: <start ns, 20 digits>.seg / .idx.
func segName(startNS int64) string { return fmt.Sprintf("%020d", startNS) }

func parseSegName(file string) (startNS int64, ok bool) {
	base, ok := strings.CutSuffix(file, ".seg")
	if !ok || len(base) != 20 {
		return 0, false
	}
	n, err := strconv.ParseInt(base, 10, 64)
	return n, err == nil
}
