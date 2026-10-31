package logstore

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// segment is one .seg file with its in-memory index. Blocks are only ever
// appended, and only to the active (newest, unsealed) segment of a service.
type segment struct {
	startNS int64
	path    string
	idxPath string
	blocks  []block
	size    int64 // bytes of data (end of the last block)
	idxSize int64
	sealed  bool

	minTS, maxTS int64
	lines        int64
}

func (s *segment) bytes() int64 { return s.size + s.idxSize }

func (s *segment) firstSeq() uint64 {
	if len(s.blocks) == 0 {
		return 0
	}
	return s.blocks[0].FirstSeq
}

func (s *segment) lastSeq() uint64 {
	if len(s.blocks) == 0 {
		return 0
	}
	return s.blocks[len(s.blocks)-1].lastSeq()
}

func (s *segment) note(b block) {
	if len(s.blocks) == 0 || b.MinTS < s.minTS {
		s.minTS = b.MinTS
	}
	if len(s.blocks) == 0 || b.MaxTS > s.maxTS {
		s.maxTS = b.MaxTS
	}
	s.lines += int64(b.Lines)
	s.blocks = append(s.blocks, b)
}

// remove deletes the segment's files.
func (s *segment) remove() error {
	err1 := os.Remove(s.path)
	err2 := os.Remove(s.idxPath)
	if err1 != nil && !errors.Is(err1, fs.ErrNotExist) {
		return err1
	}
	if err2 != nil && !errors.Is(err2, fs.ErrNotExist) {
		return err2
	}
	return nil
}

// readBlock decompresses block b of the segment from f.
func readBlock(f io.ReaderAt, b block) ([]byte, error) {
	comp := make([]byte, b.CLen)
	if _, err := f.ReadAt(comp, b.Offset); err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(comp))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	zr.Multistream(false)
	// The index says how large the block is; a larger stream is corrupt.
	raw, err := io.ReadAll(io.LimitReader(zr, int64(b.ULen)+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != int64(b.ULen) {
		return nil, fmt.Errorf("logstore: block size mismatch (%d, want %d)", len(raw), b.ULen)
	}
	return raw, nil
}

var gzPool = sync.Pool{New: func() any { return gzip.NewWriter(nil) }}

// compress returns the gzip member of raw.
func compress(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzPool.Get().(*gzip.Writer)
	defer gzPool.Put(zw)
	zw.Reset(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// summarize returns the block entry of raw (already decoded to recs).
func summarize(offset int64, clen, ulen int, recs []Record) block {
	b := block{Offset: offset, CLen: uint32(clen), ULen: uint32(ulen), Lines: uint32(len(recs)), FirstSeq: recs[0].Seq}
	b.MinTS, b.MaxTS = recs[0].TS, recs[0].TS
	for _, r := range recs[1:] {
		b.MinTS = min(b.MinTS, r.TS)
		b.MaxTS = max(b.MaxTS, r.TS)
	}
	return b
}

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// scanMembers walks the gzip members of a data file from offset, calling fn
// with each member's index entry (sequence numbers must not go below
// nextSeq). It returns the offset after the last whole member; a torn or
// corrupt member ends the walk (not an error).
func scanMembers(path string, offset int64, nextSeq uint64, fn func(block)) (end int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	cr := &countReader{r: f}
	br := bufio.NewReaderSize(cr, 64<<10)
	zr := new(gzip.Reader)
	end = offset
	for {
		if _, perr := br.Peek(1); perr != nil {
			return end, nil // clean end of file
		}
		start := offset + cr.n - int64(br.Buffered())
		if err := zr.Reset(br); err != nil {
			return end, nil
		}
		zr.Multistream(false)
		raw, rerr := io.ReadAll(zr)
		if rerr != nil {
			return end, nil
		}
		recs, derr := decodeBlock(raw)
		if derr != nil || len(recs) == 0 {
			return end, nil
		}
		if recs[0].Seq < nextSeq || !contiguous(recs) {
			return end, nil
		}
		after := offset + cr.n - int64(br.Buffered())
		b := summarize(start, int(after-start), len(raw), recs)
		fn(b)
		nextSeq = b.lastSeq() + 1
		end = after
	}
}

// loadSegment reads the index of a data file and reconciles it with the data:
// whole members the index does not know are indexed, a torn tail is cut off,
// and a missing or corrupt index is rebuilt from the data. The result is
// sealed (a segment found at startup is never appended to).
func loadSegment(dir string, startNS int64, log *slog.Logger) (*segment, error) {
	name := segName(startNS)
	seg := &segment{startNS: startNS, path: filepath.Join(dir, name+".seg"), idxPath: filepath.Join(dir, name+".idx"), sealed: true}
	var blocks []block
	if raw, err := os.ReadFile(seg.idxPath); err == nil {
		blocks, _ = parseIndex(raw)
	}
	st, err := os.Stat(seg.path)
	if err != nil {
		return nil, err
	}
	// Entries that point past the data are gone with it.
	for len(blocks) > 0 && blocks[len(blocks)-1].end() > st.Size() {
		blocks = blocks[:len(blocks)-1]
	}
	if len(blocks) > 0 && blocks[0].Offset != 0 {
		blocks = nil
	}
	var end int64
	var nextSeq uint64
	if n := len(blocks); n > 0 {
		end, nextSeq = blocks[n-1].end(), blocks[n-1].lastSeq()+1
	}
	rebuilt := false
	end, err = scanMembers(seg.path, end, nextSeq, func(b block) {
		blocks = append(blocks, b)
		rebuilt = true
	})
	if err != nil {
		return nil, err
	}
	if end < st.Size() {
		log.Warn("log segment has a torn tail; truncating", "segment", seg.path, "bytes", st.Size()-end)
		if err := os.Truncate(seg.path, end); err != nil {
			return nil, err
		}
	}
	if len(blocks) == 0 {
		return nil, errEmptySegment
	}
	for _, b := range blocks {
		seg.note(b)
	}
	seg.size = end
	if rebuilt || !indexMatches(seg.idxPath, len(blocks)) {
		buf := []byte(idxMagic)
		for _, b := range blocks {
			buf = b.marshal(buf)
		}
		if err := os.WriteFile(seg.idxPath, buf, 0o600); err != nil {
			return nil, err
		}
	}
	seg.idxSize = int64(len(idxMagic) + len(blocks)*idxEntrySize)
	return seg, nil
}

func contiguous(recs []Record) bool {
	for i := 1; i < len(recs); i++ {
		if recs[i].Seq != recs[i-1].Seq+1 {
			return false
		}
	}
	return true
}

var errEmptySegment = errors.New("logstore: empty segment")

func indexMatches(path string, n int) bool {
	st, err := os.Stat(path)
	return err == nil && st.Size() == int64(len(idxMagic)+n*idxEntrySize)
}
