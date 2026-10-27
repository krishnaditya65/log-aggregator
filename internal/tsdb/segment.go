package tsdb

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"sync"
)

// This file implements the immutable, columnar on-disk segment format.
//
// Layout:
//
//	[Header]  magic(4) version(1) seriesCount(uint32) minTime(int64) maxTime(int64)
//	[Data]    concatenated per-series blocks:
//	              uvarint(numPoints)
//	              uvarint(tsLen)  tsBytes   (delta-of-delta encoded timestamps)
//	              uvarint(valLen) valBytes  (XOR encoded values)
//	[Index]   per-series doc-value entries (see blockIndexEntry), in series order
//	[Footer]  indexOffset(uint64) indexLen(uint32) magic2(uint32)  -- fixed 16 bytes
//
// The footer is fixed-size and located at the end of the file, so a reader
// can seek to (fileSize-16), read the footer, then jump straight to the
// index section without scanning the (potentially large) data section.
// The index carries min/max time and min/max value per series block, which
// Query() uses to skip decoding blocks that cannot match a request
// ("doc-value skipping", analogous to Elasticsearch doc values / Prometheus
// TSDB's block index).

const (
	segmentMagic       uint32 = 0x54534431 // "TSD1"
	segmentFooterMagic uint32 = 0x54534446 // "TSDF"
	segmentVersion     uint8  = 1
	footerSize                = 8 + 4 + 4 // indexOffset + indexLen + magic2
)

// blockIndexEntry is the persisted doc-value index entry for one series'
// block within a segment.
type blockIndexEntry struct {
	SeriesID SeriesID
	MinTime  int64
	MaxTime  int64
	MinValue float64
	MaxValue float64
	Offset   uint64
	Length   uint32
	NumPts   uint32
}

// segmentHeader describes the whole-segment summary stored at the front of
// the file.
type segmentHeader struct {
	Version     uint8
	SeriesCount uint32
	MinTime     int64
	MaxTime     int64
}

// writeSegment serializes the given series to a new segment file at path.
// Series must have their Points sorted ascending by Time (callers in this
// package guarantee this).
func writeSegment(path string, series map[SeriesID][]Point) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		cerr := f.Close()
		if err == nil {
			err = cerr
		}
	}()

	w := bufio.NewWriter(f)

	minTime, maxTime := int64(math.MaxInt64), int64(math.MinInt64)
	for _, pts := range series {
		for _, p := range pts {
			if p.Time < minTime {
				minTime = p.Time
			}
			if p.Time > maxTime {
				maxTime = p.Time
			}
		}
	}
	if len(series) == 0 {
		minTime, maxTime = 0, 0
	}

	// Header
	var hdr [4 + 1 + 4 + 8 + 8]byte
	binary.BigEndian.PutUint32(hdr[0:4], segmentMagic)
	hdr[4] = segmentVersion
	binary.BigEndian.PutUint32(hdr[5:9], uint32(len(series)))
	binary.BigEndian.PutUint64(hdr[9:17], uint64(minTime))
	binary.BigEndian.PutUint64(hdr[17:25], uint64(maxTime))
	if _, err = w.Write(hdr[:]); err != nil {
		return err
	}

	// Stable iteration order for reproducibility.
	ids := make([]SeriesID, 0, len(series))
	for id := range series {
		ids = append(ids, id)
	}
	sortSeriesIDs(ids)

	entries := make([]blockIndexEntry, 0, len(ids))
	var offset uint64 = uint64(len(hdr))
	var tmp [binary.MaxVarintLen64]byte

	for _, id := range ids {
		pts := series[id]
		ts := make([]int64, len(pts))
		vals := make([]float64, len(pts))
		minV, maxV := math.MaxFloat64, -math.MaxFloat64
		for i, p := range pts {
			ts[i] = p.Time
			vals[i] = p.Value
			if p.Value < minV {
				minV = p.Value
			}
			if p.Value > maxV {
				maxV = p.Value
			}
		}
		if len(pts) == 0 {
			minV, maxV = 0, 0
		}
		tsBytes := encodeTimestamps(ts)
		valBytes := encodeValues(vals)

		block := &bytes.Buffer{}
		n := binary.PutUvarint(tmp[:], uint64(len(pts)))
		block.Write(tmp[:n])
		n = binary.PutUvarint(tmp[:], uint64(len(tsBytes)))
		block.Write(tmp[:n])
		block.Write(tsBytes)
		n = binary.PutUvarint(tmp[:], uint64(len(valBytes)))
		block.Write(tmp[:n])
		block.Write(valBytes)

		if _, err = w.Write(block.Bytes()); err != nil {
			return err
		}

		entry := blockIndexEntry{
			SeriesID: id,
			MinTime:  0,
			MaxTime:  0,
			MinValue: minV,
			MaxValue: maxV,
			Offset:   offset,
			Length:   uint32(block.Len()),
			NumPts:   uint32(len(pts)),
		}
		if len(pts) > 0 {
			entry.MinTime = ts[0]
			entry.MaxTime = ts[len(ts)-1]
		}
		entries = append(entries, entry)
		offset += uint64(block.Len())
	}

	indexOffset := offset
	idxBuf := &bytes.Buffer{}
	for _, e := range entries {
		n := binary.PutUvarint(tmp[:], uint64(len(e.SeriesID)))
		idxBuf.Write(tmp[:n])
		idxBuf.WriteString(string(e.SeriesID))

		var fixed [8 + 8 + 8 + 8 + 8 + 4 + 4]byte
		binary.BigEndian.PutUint64(fixed[0:8], uint64(e.MinTime))
		binary.BigEndian.PutUint64(fixed[8:16], uint64(e.MaxTime))
		binary.BigEndian.PutUint64(fixed[16:24], math.Float64bits(e.MinValue))
		binary.BigEndian.PutUint64(fixed[24:32], math.Float64bits(e.MaxValue))
		binary.BigEndian.PutUint64(fixed[32:40], e.Offset)
		binary.BigEndian.PutUint32(fixed[40:44], e.Length)
		binary.BigEndian.PutUint32(fixed[44:48], e.NumPts)
		idxBuf.Write(fixed[:])
	}
	if _, err = w.Write(idxBuf.Bytes()); err != nil {
		return err
	}

	var footer [footerSize]byte
	binary.BigEndian.PutUint64(footer[0:8], indexOffset)
	binary.BigEndian.PutUint32(footer[8:12], uint32(idxBuf.Len()))
	binary.BigEndian.PutUint32(footer[12:16], segmentFooterMagic)
	if _, err = w.Write(footer[:]); err != nil {
		return err
	}

	return w.Flush()
}

// segmentReader gives read-only, index-first access to a segment file on
// disk. It is safe for concurrent use by multiple goroutines (each Read
// call opens its own file handle / uses positioned reads).
type segmentReader struct {
	path   string
	header segmentHeader
	index  []blockIndexEntry
	// decodedBlocks counts how many data blocks have actually been
	// decoded via readBlock, so tests (and operators) can verify that
	// doc-value skipping is effective.
	decodedBlocks *counter
}

// openSegmentReader reads the header and index of a segment file. It does
// not decode any data blocks.
func openSegmentReader(path string, decodedBlocks *counter) (*segmentReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < int64(25+footerSize) {
		return nil, fmt.Errorf("tsdb: segment %s too small", path)
	}

	var hdr [25]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return nil, err
	}
	magic := binary.BigEndian.Uint32(hdr[0:4])
	if magic != segmentMagic {
		return nil, fmt.Errorf("tsdb: segment %s has bad magic", path)
	}
	sh := segmentHeader{
		Version:     hdr[4],
		SeriesCount: binary.BigEndian.Uint32(hdr[5:9]),
		MinTime:     int64(binary.BigEndian.Uint64(hdr[9:17])),
		MaxTime:     int64(binary.BigEndian.Uint64(hdr[17:25])),
	}

	var footer [footerSize]byte
	if _, err := f.ReadAt(footer[:], info.Size()-int64(footerSize)); err != nil {
		return nil, err
	}
	indexOffset := binary.BigEndian.Uint64(footer[0:8])
	indexLen := binary.BigEndian.Uint32(footer[8:12])
	magic2 := binary.BigEndian.Uint32(footer[12:16])
	if magic2 != segmentFooterMagic {
		return nil, fmt.Errorf("tsdb: segment %s has bad footer magic", path)
	}

	idxBytes := make([]byte, indexLen)
	if _, err := f.ReadAt(idxBytes, int64(indexOffset)); err != nil {
		return nil, err
	}

	entries := make([]blockIndexEntry, 0, sh.SeriesCount)
	buf := idxBytes
	for len(buf) > 0 {
		idLen, n := binary.Uvarint(buf)
		if n <= 0 {
			return nil, fmt.Errorf("tsdb: segment %s has corrupt index", path)
		}
		buf = buf[n:]
		if uint64(len(buf)) < idLen+48 {
			return nil, fmt.Errorf("tsdb: segment %s has truncated index", path)
		}
		id := SeriesID(buf[:idLen])
		buf = buf[idLen:]

		var e blockIndexEntry
		e.SeriesID = id
		e.MinTime = int64(binary.BigEndian.Uint64(buf[0:8]))
		e.MaxTime = int64(binary.BigEndian.Uint64(buf[8:16]))
		e.MinValue = math.Float64frombits(binary.BigEndian.Uint64(buf[16:24]))
		e.MaxValue = math.Float64frombits(binary.BigEndian.Uint64(buf[24:32]))
		e.Offset = binary.BigEndian.Uint64(buf[32:40])
		e.Length = binary.BigEndian.Uint32(buf[40:44])
		e.NumPts = binary.BigEndian.Uint32(buf[44:48])
		buf = buf[48:]
		entries = append(entries, e)
	}

	return &segmentReader{path: path, header: sh, index: entries, decodedBlocks: decodedBlocks}, nil
}

// readBlock decodes the point data for a single series block described by
// entry. This is the expensive operation that doc-value skipping avoids
// for blocks that cannot match a query.
func (r *segmentReader) readBlock(entry blockIndexEntry) ([]Point, error) {
	if r.decodedBlocks != nil {
		r.decodedBlocks.inc()
	}
	f, err := os.Open(r.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	raw := make([]byte, entry.Length)
	if _, err := f.ReadAt(raw, int64(entry.Offset)); err != nil {
		return nil, err
	}

	numPts, n := binary.Uvarint(raw)
	if n <= 0 {
		return nil, fmt.Errorf("tsdb: corrupt block header in %s", r.path)
	}
	raw = raw[n:]
	tsLen, n := binary.Uvarint(raw)
	if n <= 0 {
		return nil, fmt.Errorf("tsdb: corrupt block ts-len in %s", r.path)
	}
	raw = raw[n:]
	tsBytes := raw[:tsLen]
	raw = raw[tsLen:]
	valLen, n := binary.Uvarint(raw)
	if n <= 0 {
		return nil, fmt.Errorf("tsdb: corrupt block val-len in %s", r.path)
	}
	raw = raw[n:]
	valBytes := raw[:valLen]

	ts, err := decodeTimestamps(tsBytes, int(numPts))
	if err != nil {
		return nil, err
	}
	vals, err := decodeValues(valBytes, int(numPts))
	if err != nil {
		return nil, err
	}
	pts := make([]Point, numPts)
	for i := range pts {
		pts[i] = Point{Time: ts[i], Value: vals[i]}
	}
	return pts, nil
}

// counter is a tiny concurrency-safe counter used to observe how many
// blocks were decoded, primarily so tests can verify doc-value skipping.
type counter struct {
	mu sync.Mutex
	n  int64
}

func (c *counter) inc()         { c.mu.Lock(); c.n++; c.mu.Unlock() }
func (c *counter) Value() int64 { c.mu.Lock(); defer c.mu.Unlock(); return c.n }
func (c *counter) Reset()       { c.mu.Lock(); c.n = 0; c.mu.Unlock() }

func sortSeriesIDs(ids []SeriesID) {
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
}
