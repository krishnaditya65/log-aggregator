// Package coldstore implements a simplified, genuinely columnar on-disk
// archive format (Parquet-ish) plus a cold storage backend abstraction and
// an archiver that ties the two together with query pushdown.
//
// File layout:
//
//	[RowGroup 0][RowGroup 1]...[RowGroup N-1][Footer JSON][footerLen uint32][magic 4 bytes]
//
// Each RowGroup is written as: [compressedLen uint32][rawLen uint32][gzip(raw)]
// where raw is a self-describing binary encoding of one batch of rows,
// column-by-column (see encodeRowGroup). Columns are dictionary-encoded
// when they look like low-cardinality strings. The Footer carries, for
// every row group and every column in it, the min/max of the column's
// values so that Reader.Filter can skip whole row groups without ever
// gunzip-ing or decoding them.
package coldstore

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
)

// Column type tags. We collapse all Go numeric kinds into a single
// "number" (float64) family to keep the format simple; strings and bools
// get their own families.
const (
	typeString byte = 0
	typeNumber byte = 1
	typeBool   byte = 2
)

const (
	magic = "CLA1"
	// dictEncoding marks a string column encoded via a value dictionary.
	plainEncoding byte = 0
	dictEncoding  byte = 1
)

// Predicate is a simple column comparison used both for row-group
// skipping (via footer min/max) and final row-level filtering.
type Predicate struct {
	Column string
	Op     string // "eq", "ne", "gt", "gte", "lt", "lte"
	Value  any
}

// ColStat holds the row-group-level statistics for one column, used to
// decide whether a row group can possibly contain a match for a predicate.
type ColStat struct {
	Type    byte
	Count   int  // number of non-null values in this row group
	HasStat bool // false if the column has zero non-null values
	MinNum  float64
	MaxNum  float64
	MinStr  string
	MaxStr  string
	MinBool bool
	MaxBool bool
}

// RowGroupMeta describes one encoded row group within the file.
type RowGroupMeta struct {
	Offset   int64
	CompLen  uint32
	RawLen   uint32
	RowCount int
	ColStats map[string]ColStat
}

// Footer is written at the end of the archive file and is the only thing
// a Reader needs to parse before deciding which row groups to decode.
type Footer struct {
	TotalRows int
	RowGroups []RowGroupMeta
}

// normVal normalizes an arbitrary Go value coming out of a
// map[string]any row into one of the three supported families.
// ok is false for nil/missing values.
func normVal(v any) (typ byte, out any, ok bool) {
	switch t := v.(type) {
	case nil:
		return 0, nil, false
	case string:
		return typeString, t, true
	case bool:
		return typeBool, t, true
	case float64:
		return typeNumber, t, true
	case float32:
		return typeNumber, float64(t), true
	case int:
		return typeNumber, float64(t), true
	case int8:
		return typeNumber, float64(t), true
	case int16:
		return typeNumber, float64(t), true
	case int32:
		return typeNumber, float64(t), true
	case int64:
		return typeNumber, float64(t), true
	case uint:
		return typeNumber, float64(t), true
	case uint8:
		return typeNumber, float64(t), true
	case uint16:
		return typeNumber, float64(t), true
	case uint32:
		return typeNumber, float64(t), true
	case uint64:
		return typeNumber, float64(t), true
	default:
		return typeString, fmt.Sprintf("%v", t), true
	}
}

// ---------------------------------------------------------------------
// Writer
// ---------------------------------------------------------------------

// Writer encodes batches of rows into row groups and appends them to an
// underlying file, tracking per-row-group column statistics for the
// footer.
type Writer struct {
	f         *os.File
	offset    int64
	rowGroups []RowGroupMeta
	totalRows int
	closed    bool
}

// NewWriter creates a Writer that writes to path (truncating any existing
// file).
func NewWriter(path string) (*Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &Writer{f: f}, nil
}

// WriteBatch encodes rows as a single new row group and appends it to the
// file. Column order within the row group is the sorted union of keys
// present across rows, which keeps encoding deterministic.
func (w *Writer) WriteBatch(rows []map[string]any) error {
	if w.closed {
		return fmt.Errorf("coldstore: write on closed writer")
	}
	if len(rows) == 0 {
		return nil
	}

	colNames := unionKeys(rows)
	raw, stats := encodeRowGroup(rows, colNames)

	var compBuf bytes.Buffer
	gz := gzip.NewWriter(&compBuf)
	if _, err := gz.Write(raw); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}

	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(compBuf.Len()))
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(len(raw)))
	if _, err := w.f.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := w.f.Write(compBuf.Bytes()); err != nil {
		return err
	}

	meta := RowGroupMeta{
		Offset:   w.offset,
		CompLen:  uint32(compBuf.Len()),
		RawLen:   uint32(len(raw)),
		RowCount: len(rows),
		ColStats: stats,
	}
	w.rowGroups = append(w.rowGroups, meta)
	w.offset += int64(8 + compBuf.Len())
	w.totalRows += len(rows)
	return nil
}

// Close writes the footer and closes the underlying file, returning the
// footer (row-group statistics) for inspection by the caller.
func (w *Writer) Close() (Footer, error) {
	if w.closed {
		return Footer{}, fmt.Errorf("coldstore: already closed")
	}
	w.closed = true
	footer := Footer{TotalRows: w.totalRows, RowGroups: w.rowGroups}

	fb, err := json.Marshal(footer)
	if err != nil {
		w.f.Close()
		return footer, err
	}
	if _, err := w.f.Write(fb); err != nil {
		w.f.Close()
		return footer, err
	}
	var tail [8]byte
	binary.LittleEndian.PutUint32(tail[0:4], uint32(len(fb)))
	copy(tail[4:8], magic)
	if _, err := w.f.Write(tail[:]); err != nil {
		w.f.Close()
		return footer, err
	}
	return footer, w.f.Close()
}

func unionKeys(rows []map[string]any) []string {
	set := map[string]struct{}{}
	for _, r := range rows {
		for k := range r {
			set[k] = struct{}{}
		}
	}
	names := make([]string, 0, len(set))
	for k := range set {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// encodeRowGroup produces the raw (pre-compression) byte encoding of a
// row group and its per-column statistics.
func encodeRowGroup(rows []map[string]any, colNames []string) ([]byte, map[string]ColStat) {
	var buf bytes.Buffer
	writeU32(&buf, uint32(len(rows)))
	writeU32(&buf, uint32(len(colNames)))

	stats := make(map[string]ColStat, len(colNames))

	for _, name := range colNames {
		colType, present, values, stat := extractColumn(rows, name)
		stats[name] = stat

		writeString(&buf, name)
		buf.WriteByte(colType)

		nullBitmap := packBitmap(present)
		writeU32(&buf, uint32(len(nullBitmap)))
		buf.Write(nullBitmap)

		var dataBuf bytes.Buffer
		encoding := plainEncoding
		if colType == typeString {
			encoding = encodeStringColumn(&dataBuf, values)
		} else if colType == typeNumber {
			for _, v := range values {
				var b [8]byte
				binary.LittleEndian.PutUint64(b[:], f64bits(v.(float64)))
				dataBuf.Write(b[:])
			}
		} else { // bool
			for _, v := range values {
				if v.(bool) {
					dataBuf.WriteByte(1)
				} else {
					dataBuf.WriteByte(0)
				}
			}
		}
		buf.WriteByte(encoding)
		writeU32(&buf, uint32(dataBuf.Len()))
		buf.Write(dataBuf.Bytes())
	}

	return buf.Bytes(), stats
}

// extractColumn pulls the values of one column across all rows in
// declaration order, determines its type family from the first non-nil
// value, computes min/max stats, and returns a present-bitmap plus the
// (possibly coerced) values for present rows only.
func extractColumn(rows []map[string]any, name string) (byte, []bool, []any, ColStat) {
	present := make([]bool, len(rows))
	var colType byte
	typeDecided := false
	values := make([]any, 0, len(rows))

	stat := ColStat{}

	for i, row := range rows {
		raw, ok := row[name]
		if !ok {
			continue
		}
		typ, v, ok := normVal(raw)
		if !ok {
			continue
		}
		if !typeDecided {
			colType = typ
			typeDecided = true
		} else if typ != colType {
			// Type mismatch within a column: coerce to string so the
			// column stays well-formed.
			if colType != typeString {
				colType = typeString
			}
			v = fmt.Sprintf("%v", raw)
		}
		present[i] = true
		values = append(values, v)

		switch colType {
		case typeNumber:
			n := v.(float64)
			if !stat.HasStat || n < stat.MinNum {
				stat.MinNum = n
			}
			if !stat.HasStat || n > stat.MaxNum {
				stat.MaxNum = n
			}
		case typeString:
			s, ok := v.(string)
			if !ok {
				s = fmt.Sprintf("%v", v)
			}
			if !stat.HasStat || s < stat.MinStr {
				stat.MinStr = s
			}
			if !stat.HasStat || s > stat.MaxStr {
				stat.MaxStr = s
			}
		case typeBool:
			b := v.(bool)
			if !stat.HasStat {
				stat.MinBool, stat.MaxBool = b, b
			} else {
				if !b {
					stat.MinBool = false
				}
				if b {
					stat.MaxBool = true
				}
			}
		}
		stat.Count++
		stat.HasStat = true
	}

	// If every value ended up coerced to string (mixed types), values
	// already hold strings for the earlier numeric ones too? No: only
	// the row that triggered the mismatch got coerced. To keep the
	// column self-consistent, re-coerce everything to string when the
	// final decided type is string but some earlier values are not
	// strings.
	if typeDecided && colType == typeString {
		for i, v := range values {
			if _, ok := v.(string); !ok {
				values[i] = fmt.Sprintf("%v", v)
			}
		}
		// Recompute string stats from scratch for correctness.
		stat = ColStat{Type: typeString}
		for _, v := range values {
			s := v.(string)
			if !stat.HasStat || s < stat.MinStr {
				stat.MinStr = s
			}
			if !stat.HasStat || s > stat.MaxStr {
				stat.MaxStr = s
			}
			stat.HasStat = true
			stat.Count++
		}
	}
	stat.Type = colType
	if !typeDecided {
		stat.Type = typeString
	}

	return colType, present, values, stat
}

// encodeStringColumn writes values (all strings) into buf, choosing
// dictionary encoding when cardinality is low. It returns the encoding
// byte used.
func encodeStringColumn(buf *bytes.Buffer, values []any) byte {
	distinct := map[string]int{}
	order := []string{}
	for _, v := range values {
		s := v.(string)
		if _, ok := distinct[s]; !ok {
			distinct[s] = len(order)
			order = append(order, s)
		}
	}
	useDict := len(values) > 0 && len(order) <= 65535 && len(order)*2 < len(values)+1

	if !useDict {
		for _, v := range values {
			writeString(buf, v.(string))
		}
		return plainEncoding
	}

	writeU32(buf, uint32(len(order)))
	for _, s := range order {
		writeString(buf, s)
	}
	for _, v := range values {
		idx := distinct[v.(string)]
		writeU32(buf, uint32(idx))
	}
	return dictEncoding
}

func packBitmap(present []bool) []byte {
	out := make([]byte, (len(present)+7)/8)
	for i, p := range present {
		if p {
			out[i/8] |= 1 << uint(i%8)
		}
	}
	return out
}

func bitmapGet(bitmap []byte, i int) bool {
	byteIdx := i / 8
	if byteIdx >= len(bitmap) {
		return false
	}
	return bitmap[byteIdx]&(1<<uint(i%8)) != 0
}

func writeU32(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}

func writeString(buf *bytes.Buffer, s string) {
	writeU32(buf, uint32(len(s)))
	buf.WriteString(s)
}

func f64bits(f float64) uint64 {
	return math.Float64bits(f)
}

// ---------------------------------------------------------------------
// Reader
// ---------------------------------------------------------------------

// Reader opens an archive file, parses its footer, and can answer
// predicate queries while skipping row groups that cannot match based on
// footer min/max statistics.
type Reader struct {
	f      *os.File
	Footer Footer

	// Populated after each call to Filter for observability/testing.
	DecodedRowGroups int
	SkippedRowGroups int
}

// Open opens path and reads its footer.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if fi.Size() < 8 {
		f.Close()
		return nil, fmt.Errorf("coldstore: file too small")
	}
	tail := make([]byte, 8)
	if _, err := f.ReadAt(tail, fi.Size()-8); err != nil {
		f.Close()
		return nil, err
	}
	footerLen := binary.LittleEndian.Uint32(tail[0:4])
	if string(tail[4:8]) != magic {
		f.Close()
		return nil, fmt.Errorf("coldstore: bad magic")
	}
	footerStart := fi.Size() - 8 - int64(footerLen)
	if footerStart < 0 {
		f.Close()
		return nil, fmt.Errorf("coldstore: corrupt footer length")
	}
	fb := make([]byte, footerLen)
	if _, err := f.ReadAt(fb, footerStart); err != nil {
		f.Close()
		return nil, err
	}
	var footer Footer
	if err := json.Unmarshal(fb, &footer); err != nil {
		f.Close()
		return nil, err
	}
	return &Reader{f: f, Footer: footer}, nil
}

// Close closes the underlying file.
func (r *Reader) Close() error { return r.f.Close() }

// Filter evaluates predicates (AND semantics) against the archive,
// skipping whole row groups whose footer min/max stats prove no row can
// match, and decoding+scanning the rest. It records DecodedRowGroups and
// SkippedRowGroups for observability/testing.
func (r *Reader) Filter(predicates []Predicate) ([]map[string]any, error) {
	r.DecodedRowGroups = 0
	r.SkippedRowGroups = 0
	var out []map[string]any

	for _, rg := range r.Footer.RowGroups {
		if !rowGroupMayMatch(rg, predicates) {
			r.SkippedRowGroups++
			continue
		}
		r.DecodedRowGroups++

		rows, err := r.decodeRowGroup(rg)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if rowMatches(row, predicates) {
				out = append(out, row)
			}
		}
	}
	return out, nil
}

// rowGroupMayMatch decides, using only footer statistics, whether a row
// group could possibly contain a row satisfying every predicate.
func rowGroupMayMatch(rg RowGroupMeta, predicates []Predicate) bool {
	for _, p := range predicates {
		stat, ok := rg.ColStats[p.Column]
		if !ok || !stat.HasStat {
			// Column absent (or all-null) in this row group: only an
			// equality/inequality-style predicate can be proven
			// impossible; "ne" could still match nothing meaningfully,
			// so be conservative and treat as no-match for value
			// comparisons, decode for "ne".
			if p.Op == "ne" {
				continue
			}
			return false
		}
		switch stat.Type {
		case typeNumber:
			val, isNum := toFloat(p.Value)
			if !isNum {
				continue // can't reason about it from stats; be safe
			}
			if !numRangeCouldMatch(stat.MinNum, stat.MaxNum, p.Op, val) {
				return false
			}
		case typeString:
			val, isStr := p.Value.(string)
			if !isStr {
				continue
			}
			if !strRangeCouldMatch(stat.MinStr, stat.MaxStr, p.Op, val) {
				return false
			}
		case typeBool:
			// Bool ranges are trivial; never worth skipping on.
			continue
		}
	}
	return true
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case int32:
		return float64(t), true
	}
	return 0, false
}

func numRangeCouldMatch(min, max float64, op string, v float64) bool {
	switch op {
	case "eq":
		return v >= min && v <= max
	case "gt":
		return max > v
	case "gte":
		return max >= v
	case "lt":
		return min < v
	case "lte":
		return min <= v
	default:
		return true // ne or unknown op: can't skip
	}
}

func strRangeCouldMatch(min, max string, op string, v string) bool {
	switch op {
	case "eq":
		return v >= min && v <= max
	case "gt":
		return max > v
	case "gte":
		return max >= v
	case "lt":
		return min < v
	case "lte":
		return min <= v
	default:
		return true
	}
}

func rowMatches(row map[string]any, predicates []Predicate) bool {
	for _, p := range predicates {
		v, ok := row[p.Column]
		if !ok {
			if p.Op == "ne" {
				continue
			}
			return false
		}
		if !valueMatches(v, p.Op, p.Value) {
			return false
		}
	}
	return true
}

func valueMatches(rowVal any, op string, want any) bool {
	switch a := rowVal.(type) {
	case float64:
		b, ok := toFloat(want)
		if !ok {
			return false
		}
		switch op {
		case "eq":
			return a == b
		case "ne":
			return a != b
		case "gt":
			return a > b
		case "gte":
			return a >= b
		case "lt":
			return a < b
		case "lte":
			return a <= b
		}
	case string:
		b, ok := want.(string)
		if !ok {
			return false
		}
		switch op {
		case "eq":
			return a == b
		case "ne":
			return a != b
		case "gt":
			return a > b
		case "gte":
			return a >= b
		case "lt":
			return a < b
		case "lte":
			return a <= b
		}
	case bool:
		b, ok := want.(bool)
		if !ok {
			return false
		}
		switch op {
		case "eq":
			return a == b
		case "ne":
			return a != b
		}
	}
	return false
}

// decodeRowGroup reads and decodes one row group from disk into rows.
func (r *Reader) decodeRowGroup(rg RowGroupMeta) ([]map[string]any, error) {
	hdr := make([]byte, 8)
	if _, err := r.f.ReadAt(hdr, rg.Offset); err != nil {
		return nil, err
	}
	compLen := binary.LittleEndian.Uint32(hdr[0:4])
	rawLen := binary.LittleEndian.Uint32(hdr[4:8])
	comp := make([]byte, compLen)
	if _, err := r.f.ReadAt(comp, rg.Offset+8); err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(bytes.NewReader(comp))
	if err != nil {
		return nil, err
	}
	raw := make([]byte, rawLen)
	if _, err := io.ReadFull(gz, raw); err != nil {
		return nil, err
	}
	gz.Close()

	return decodeRowGroupBytes(raw)
}

func decodeRowGroupBytes(raw []byte) ([]map[string]any, error) {
	br := bufio.NewReader(bytes.NewReader(raw))
	numRows, err := readU32(br)
	if err != nil {
		return nil, err
	}
	numCols, err := readU32(br)
	if err != nil {
		return nil, err
	}

	rows := make([]map[string]any, numRows)
	for i := range rows {
		rows[i] = map[string]any{}
	}

	for c := uint32(0); c < numCols; c++ {
		name, err := readString(br)
		if err != nil {
			return nil, err
		}
		colType, err := br.ReadByte()
		if err != nil {
			return nil, err
		}
		bitmapLen, err := readU32(br)
		if err != nil {
			return nil, err
		}
		bitmap := make([]byte, bitmapLen)
		if _, err := io.ReadFull(br, bitmap); err != nil {
			return nil, err
		}
		encoding, err := br.ReadByte()
		if err != nil {
			return nil, err
		}
		dataLen, err := readU32(br)
		if err != nil {
			return nil, err
		}
		data := make([]byte, dataLen)
		if _, err := io.ReadFull(br, data); err != nil {
			return nil, err
		}

		if err := decodeColumnInto(rows, name, colType, encoding, bitmap, data, int(numRows)); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func decodeColumnInto(rows []map[string]any, name string, colType, encoding byte, bitmap, data []byte, numRows int) error {
	dr := bytes.NewReader(data)
	switch colType {
	case typeNumber:
		for i := 0; i < numRows; i++ {
			if !bitmapGet(bitmap, i) {
				continue
			}
			var b [8]byte
			if _, err := io.ReadFull(dr, b[:]); err != nil {
				return err
			}
			rows[i][name] = math.Float64frombits(binary.LittleEndian.Uint64(b[:]))
		}
	case typeBool:
		for i := 0; i < numRows; i++ {
			if !bitmapGet(bitmap, i) {
				continue
			}
			b, err := dr.ReadByte()
			if err != nil {
				return err
			}
			rows[i][name] = b != 0
		}
	case typeString:
		if encoding == dictEncoding {
			dictLen, err := readU32r(dr)
			if err != nil {
				return err
			}
			dict := make([]string, dictLen)
			for i := range dict {
				s, err := readStringr(dr)
				if err != nil {
					return err
				}
				dict[i] = s
			}
			for i := 0; i < numRows; i++ {
				if !bitmapGet(bitmap, i) {
					continue
				}
				idx, err := readU32r(dr)
				if err != nil {
					return err
				}
				if int(idx) >= len(dict) {
					return fmt.Errorf("coldstore: dict index out of range")
				}
				rows[i][name] = dict[idx]
			}
		} else {
			for i := 0; i < numRows; i++ {
				if !bitmapGet(bitmap, i) {
					continue
				}
				s, err := readStringr(dr)
				if err != nil {
					return err
				}
				rows[i][name] = s
			}
		}
	default:
		return fmt.Errorf("coldstore: unknown column type %d", colType)
	}
	return nil
}

func readU32(br *bufio.Reader) (uint32, error) {
	var b [4]byte
	if _, err := io.ReadFull(br, b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

func readString(br *bufio.Reader) (string, error) {
	n, err := readU32(br)
	if err != nil {
		return "", err
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(br, b); err != nil {
		return "", err
	}
	return string(b), nil
}

func readU32r(r io.Reader) (uint32, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

func readStringr(r io.Reader) (string, error) {
	n, err := readU32r(r)
	if err != nil {
		return "", err
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}
