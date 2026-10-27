package tsdb

import (
	"encoding/binary"
	"fmt"
	"math"
)

// This file implements the columnar block codec used inside segment files.
//
// Timestamps are encoded using delta-of-delta ("Gorilla"/Prometheus TSDB
// style): the first timestamp is stored raw, the second as a delta from the
// first, and every subsequent timestamp as the delta-of-delta from the
// previous two, zig-zag varint encoded. Regularly spaced samples (the
// common case for scraped/streamed metrics) collapse to a single zero byte
// per point after the first two.
//
// Values are encoded with a simplified XOR scheme: the first value is
// stored raw (8 bytes), and every subsequent value is XORed with the
// previous value's bit pattern and stored as a uvarint. Repeated or
// slowly-changing values XOR to (mostly) zero, which varint-encodes to a
// single byte, giving good compression on typical metric data without the
// complexity of full Gorilla bit-packing.

// encodeTimestamps encodes a slice of ascending unix-nanos timestamps.
func encodeTimestamps(ts []int64) []byte {
	buf := make([]byte, 0, len(ts)*2+16)
	var tmp [binary.MaxVarintLen64]byte

	if len(ts) == 0 {
		return buf
	}
	n := binary.PutVarint(tmp[:], ts[0])
	buf = append(buf, tmp[:n]...)
	if len(ts) == 1 {
		return buf
	}
	firstDelta := ts[1] - ts[0]
	n = binary.PutVarint(tmp[:], firstDelta)
	buf = append(buf, tmp[:n]...)

	prevDelta := firstDelta
	prev := ts[1]
	for i := 2; i < len(ts); i++ {
		delta := ts[i] - prev
		dod := delta - prevDelta
		n = binary.PutVarint(tmp[:], dod)
		buf = append(buf, tmp[:n]...)
		prevDelta = delta
		prev = ts[i]
	}
	return buf
}

// decodeTimestamps decodes n timestamps from a byte slice produced by
// encodeTimestamps.
func decodeTimestamps(buf []byte, n int) ([]int64, error) {
	out := make([]int64, 0, n)
	if n == 0 {
		return out, nil
	}
	first, sz := binary.Varint(buf)
	if sz <= 0 {
		return nil, fmt.Errorf("tsdb: corrupt timestamp block (first value)")
	}
	buf = buf[sz:]
	out = append(out, first)
	if n == 1 {
		return out, nil
	}
	firstDelta, sz := binary.Varint(buf)
	if sz <= 0 {
		return nil, fmt.Errorf("tsdb: corrupt timestamp block (first delta)")
	}
	buf = buf[sz:]
	prev := first + firstDelta
	out = append(out, prev)
	prevDelta := firstDelta

	for i := 2; i < n; i++ {
		dod, sz := binary.Varint(buf)
		if sz <= 0 {
			return nil, fmt.Errorf("tsdb: corrupt timestamp block (dod at %d)", i)
		}
		buf = buf[sz:]
		delta := prevDelta + dod
		cur := prev + delta
		out = append(out, cur)
		prevDelta = delta
		prev = cur
	}
	return out, nil
}

// encodeValues encodes a slice of float64 values using the XOR+uvarint
// scheme described above.
func encodeValues(vals []float64) []byte {
	buf := make([]byte, 0, len(vals)*2+8)
	var tmp [binary.MaxVarintLen64]byte

	if len(vals) == 0 {
		return buf
	}
	prevBits := math.Float64bits(vals[0])
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], prevBits)
	buf = append(buf, raw[:]...)

	for i := 1; i < len(vals); i++ {
		bits := math.Float64bits(vals[i])
		xor := bits ^ prevBits
		n := binary.PutUvarint(tmp[:], xor)
		buf = append(buf, tmp[:n]...)
		prevBits = bits
	}
	return buf
}

// decodeValues decodes n float64 values from a byte slice produced by
// encodeValues.
func decodeValues(buf []byte, n int) ([]float64, error) {
	out := make([]float64, 0, n)
	if n == 0 {
		return out, nil
	}
	if len(buf) < 8 {
		return nil, fmt.Errorf("tsdb: corrupt value block (first value)")
	}
	prevBits := binary.BigEndian.Uint64(buf[:8])
	buf = buf[8:]
	out = append(out, math.Float64frombits(prevBits))

	for i := 1; i < n; i++ {
		xor, sz := binary.Uvarint(buf)
		if sz <= 0 {
			return nil, fmt.Errorf("tsdb: corrupt value block (xor at %d)", i)
		}
		buf = buf[sz:]
		bits := xor ^ prevBits
		out = append(out, math.Float64frombits(bits))
		prevBits = bits
	}
	return out, nil
}
