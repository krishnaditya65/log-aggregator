package logsdb

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// segmentFooter is everything needed to search a segment without
// re-scanning the whole file: block layout plus the inverted index.
//
// Term namespaces stored in Index:
//
//	msg:<token>        -> message word postings
//	tag:<key>=<value>  -> tag equality postings
//	field:<key>=<value> -> field equality postings (value via fmt.Sprint)
//
// Postings are lists of doc ordinals: a doc's position within the
// segment's globally (Host, Time)-sorted document sequence. Ordinal ->
// (block, offset-in-block) is a pure division, so no extra offset table is
// needed per doc.
type segmentFooter struct {
	DocCount        int
	BlockSize       int
	BlockOffsets    []int64 // file offset of each block's length-prefix
	Index           map[string][]int32
	MinTime         int64
	MaxTime         int64
	RawBytes        int64 // sum of uncompressed per-block JSON bytes
	CompressedBytes int64 // sum of on-disk compressed block bytes
}

// segment is an open, queryable handle onto a flushed segment file.
type segment struct {
	path   string
	footer segmentFooter
}

// SegmentStats is the public compression-ratio report for one segment.
type SegmentStats struct {
	Path            string
	DocCount        int
	RawBytes        int64
	CompressedBytes int64
	Ratio           float64 // RawBytes / CompressedBytes; higher is better
	MinTime         int64
	MaxTime         int64
}

func (s *segment) stats() SegmentStats {
	ratio := 0.0
	if s.footer.CompressedBytes > 0 {
		ratio = float64(s.footer.RawBytes) / float64(s.footer.CompressedBytes)
	}
	return SegmentStats{
		Path:            s.path,
		DocCount:        s.footer.DocCount,
		RawBytes:        s.footer.RawBytes,
		CompressedBytes: s.footer.CompressedBytes,
		Ratio:           ratio,
		MinTime:         s.footer.MinTime,
		MaxTime:         s.footer.MaxTime,
	}
}

// writeSegment sorts docs by (Host, Time) -- LogsDB-style co-location of a
// host's log lines -- chunks the sorted sequence into fixed-size blocks,
// gzip-compresses each block independently, and builds the inverted index
// over the final sorted ordinals. The sort is the whole point: grouping one
// host's (often near-identical, template-shaped) log lines together gives
// gzip/zstd far more redundancy to exploit per block than arrival order
// would, which is exactly what an "logsdb" style index mode buys you.
func writeSegment(dir string, name string, docs []LogDoc, blockSize int) (*segment, error) {
	if len(docs) == 0 {
		return nil, fmt.Errorf("logsdb: cannot write empty segment")
	}
	if blockSize <= 0 {
		blockSize = 128
	}

	sorted := make([]LogDoc, len(docs))
	copy(sorted, docs)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Host != sorted[j].Host {
			return sorted[i].Host < sorted[j].Host
		}
		return sorted[i].Time < sorted[j].Time
	})

	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	footer := segmentFooter{
		DocCount:  len(sorted),
		BlockSize: blockSize,
		Index:     make(map[string][]int32),
		MinTime:   sorted[0].Time,
		MaxTime:   sorted[0].Time,
	}

	var offset int64
	for start := 0; start < len(sorted); start += blockSize {
		end := start + blockSize
		if end > len(sorted) {
			end = len(sorted)
		}
		block := sorted[start:end]

		raw, err := json.Marshal(block)
		if err != nil {
			return nil, err
		}
		var cbuf bytes.Buffer
		gw := gzip.NewWriter(&cbuf)
		if _, err := gw.Write(raw); err != nil {
			return nil, err
		}
		if err := gw.Close(); err != nil {
			return nil, err
		}

		footer.BlockOffsets = append(footer.BlockOffsets, offset)
		footer.RawBytes += int64(len(raw))
		footer.CompressedBytes += int64(cbuf.Len())

		var lenPrefix [4]byte
		binary.BigEndian.PutUint32(lenPrefix[:], uint32(cbuf.Len()))
		n1, err := f.Write(lenPrefix[:])
		if err != nil {
			return nil, err
		}
		n2, err := f.Write(cbuf.Bytes())
		if err != nil {
			return nil, err
		}
		offset += int64(n1 + n2)

		for i, d := range block {
			ordinal := int32(start + i)
			if d.Time < footer.MinTime {
				footer.MinTime = d.Time
			}
			if d.Time > footer.MaxTime {
				footer.MaxTime = d.Time
			}
			for _, tok := range uniqueTokens(d.Message) {
				term := "msg:" + tok
				footer.Index[term] = append(footer.Index[term], ordinal)
			}
			for k, v := range d.Tags {
				term := "tag:" + k + "=" + v
				footer.Index[term] = append(footer.Index[term], ordinal)
			}
			for k, v := range d.Fields {
				term := "field:" + k + "=" + fieldToString(v)
				footer.Index[term] = append(footer.Index[term], ordinal)
			}
		}
	}

	footerOffset := offset
	var fbuf bytes.Buffer
	if err := gob.NewEncoder(&fbuf).Encode(footer); err != nil {
		return nil, err
	}
	nf, err := f.Write(fbuf.Bytes())
	if err != nil {
		return nil, err
	}

	var trailer [8]byte
	binary.BigEndian.PutUint64(trailer[:], uint64(footerOffset))
	if _, err := f.Write(trailer[:]); err != nil {
		return nil, err
	}
	_ = nf

	if err := f.Sync(); err != nil {
		return nil, err
	}

	return &segment{path: path, footer: footer}, nil
}

// openSegment reads just the footer (block layout + inverted index) of an
// existing segment file, without loading any documents.
func openSegment(path string) (*segment, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() < 8 {
		return nil, fmt.Errorf("logsdb: segment %s too small", path)
	}

	var trailer [8]byte
	if _, err := f.ReadAt(trailer[:], fi.Size()-8); err != nil {
		return nil, err
	}
	footerOffset := int64(binary.BigEndian.Uint64(trailer[:]))

	footerLen := fi.Size() - 8 - footerOffset
	if footerLen <= 0 {
		return nil, fmt.Errorf("logsdb: segment %s corrupt footer", path)
	}
	footerBytes := make([]byte, footerLen)
	if _, err := f.ReadAt(footerBytes, footerOffset); err != nil {
		return nil, err
	}

	var footer segmentFooter
	if err := gob.NewDecoder(bytes.NewReader(footerBytes)).Decode(&footer); err != nil {
		return nil, err
	}

	return &segment{path: path, footer: footer}, nil
}

// readBlock loads and decompresses a single block by index.
func (s *segment) readBlock(blockIdx int) ([]LogDoc, error) {
	if blockIdx < 0 || blockIdx >= len(s.footer.BlockOffsets) {
		return nil, fmt.Errorf("logsdb: block %d out of range", blockIdx)
	}
	f, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	off := s.footer.BlockOffsets[blockIdx]
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	var lenPrefix [4]byte
	if _, err := io.ReadFull(f, lenPrefix[:]); err != nil {
		return nil, err
	}
	clen := binary.BigEndian.Uint32(lenPrefix[:])
	cbuf := make([]byte, clen)
	if _, err := io.ReadFull(f, cbuf); err != nil {
		return nil, err
	}

	gr, err := gzip.NewReader(bytes.NewReader(cbuf))
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	raw, err := io.ReadAll(gr)
	if err != nil {
		return nil, err
	}

	var docs []LogDoc
	if err := json.Unmarshal(raw, &docs); err != nil {
		return nil, err
	}
	return docs, nil
}

// docsByOrdinals fetches a specific set of docs (given by their sorted
// position within the segment) grouping reads by block so each block is
// decompressed at most once.
func (s *segment) docsByOrdinals(ordinals []int32) ([]LogDoc, error) {
	if len(ordinals) == 0 {
		return nil, nil
	}
	sorted := make([]int32, len(ordinals))
	copy(sorted, ordinals)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	byBlock := make(map[int][]int32)
	for _, ord := range sorted {
		b := int(ord) / s.footer.BlockSize
		byBlock[b] = append(byBlock[b], ord)
	}

	var out []LogDoc
	for b, ords := range byBlock {
		block, err := s.readBlock(b)
		if err != nil {
			return nil, err
		}
		for _, ord := range ords {
			idx := int(ord) % s.footer.BlockSize
			if idx < 0 || idx >= len(block) {
				continue
			}
			out = append(out, block[idx])
		}
	}
	return out, nil
}

// allDocs loads the entire segment (used for full-scan fallback queries).
func (s *segment) allDocs() ([]LogDoc, error) {
	out := make([]LogDoc, 0, s.footer.DocCount)
	for b := range s.footer.BlockOffsets {
		block, err := s.readBlock(b)
		if err != nil {
			return nil, err
		}
		out = append(out, block...)
	}
	return out, nil
}
