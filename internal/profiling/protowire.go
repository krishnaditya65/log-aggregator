package profiling

import "fmt"

// protowire implements just enough of the protobuf wire format (see
// https://protobuf.dev/programming-guides/encoding/) to walk a pprof
// profile.proto message: varints, length-delimited fields (used for
// strings, bytes, and embedded messages), and packed-repeated varints.
// It intentionally does not aim to be a general-purpose protobuf library.

// wireType constants per the protobuf spec.
const (
	wireVarint          = 0
	wireFixed64         = 1
	wireLengthDelimited = 2
	wireStartGroup      = 3 // unused by profile.proto, handled defensively
	wireEndGroup        = 4
	wireFixed32         = 5
)

// pbField is one decoded (field number, wire type, payload) triple from a
// protobuf message. For varint/fixed types Value holds the numeric value;
// for length-delimited types Bytes holds the raw payload.
type pbField struct {
	Num   int
	Wire  int
	Value uint64
	Bytes []byte
}

// decodeVarint reads a base-128 varint starting at buf[pos], returning the
// value and the position just past it.
func decodeVarint(buf []byte, pos int) (uint64, int, error) {
	var result uint64
	var shift uint
	for {
		if pos >= len(buf) {
			return 0, 0, fmt.Errorf("protowire: truncated varint")
		}
		b := buf[pos]
		pos++
		result |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			return result, pos, nil
		}
		shift += 7
		if shift >= 64 {
			return 0, 0, fmt.Errorf("protowire: varint too long")
		}
	}
}

// parseFields walks all top-level fields of a protobuf message encoded in
// buf, invoking visit for each. Returns an error on malformed input.
func parseFields(buf []byte, visit func(pbField) error) error {
	pos := 0
	for pos < len(buf) {
		tag, next, err := decodeVarint(buf, pos)
		if err != nil {
			return err
		}
		pos = next
		fieldNum := int(tag >> 3)
		wireType := int(tag & 0x7)

		var f pbField
		f.Num = fieldNum
		f.Wire = wireType

		switch wireType {
		case wireVarint:
			v, next, err := decodeVarint(buf, pos)
			if err != nil {
				return err
			}
			pos = next
			f.Value = v
		case wireFixed64:
			if pos+8 > len(buf) {
				return fmt.Errorf("protowire: truncated fixed64")
			}
			var v uint64
			for i := 0; i < 8; i++ {
				v |= uint64(buf[pos+i]) << (8 * i)
			}
			pos += 8
			f.Value = v
		case wireFixed32:
			if pos+4 > len(buf) {
				return fmt.Errorf("protowire: truncated fixed32")
			}
			var v uint64
			for i := 0; i < 4; i++ {
				v |= uint64(buf[pos+i]) << (8 * i)
			}
			pos += 4
			f.Value = v
		case wireLengthDelimited:
			length, next, err := decodeVarint(buf, pos)
			if err != nil {
				return err
			}
			pos = next
			end := pos + int(length)
			if end < pos || end > len(buf) {
				return fmt.Errorf("protowire: truncated length-delimited field")
			}
			f.Bytes = buf[pos:end]
			pos = end
		case wireStartGroup, wireEndGroup:
			// Not used by profile.proto; skip defensively rather than
			// erroring so unknown/future fields don't break decoding.
			continue
		default:
			return fmt.Errorf("protowire: unsupported wire type %d", wireType)
		}

		if err := visit(f); err != nil {
			return err
		}
	}
	return nil
}

// decodePackedVarints interprets buf as a packed-repeated varint field
// (used for Sample.location_id and Sample.value, both `[packed=true]`).
func decodePackedVarints(buf []byte) ([]uint64, error) {
	var out []uint64
	pos := 0
	for pos < len(buf) {
		v, next, err := decodeVarint(buf, pos)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		pos = next
	}
	return out, nil
}

// zigzagDecode converts a protobuf sint64 zigzag-encoded value back to a
// signed int64. profile.proto declares its int64 fields as plain int64
// (not sintN), so callers should generally just int64(v); this helper is
// kept for completeness/documentation of the alternative encoding.
func zigzagDecode(v uint64) int64 {
	return int64(v>>1) ^ -int64(v&1)
}
