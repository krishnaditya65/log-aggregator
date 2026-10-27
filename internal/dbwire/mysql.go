package dbwire

import (
	"bytes"
	"fmt"
	"io"
)

const comQuery byte = 0x03

// MySQLPacketHeader is the standard 4-byte MySQL protocol packet header:
// a 3-byte little-endian payload length followed by a 1-byte sequence
// number.
type MySQLPacketHeader struct {
	Length   uint32
	Sequence byte
}

// ReadMySQLPacketHeader reads and decodes a 4-byte MySQL packet header.
func ReadMySQLPacketHeader(r io.Reader) (MySQLPacketHeader, error) {
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return MySQLPacketHeader{}, fmt.Errorf("dbwire/mysql: read packet header: %w", err)
	}
	length := uint32(buf[0]) | uint32(buf[1])<<8 | uint32(buf[2])<<16
	return MySQLPacketHeader{Length: length, Sequence: buf[3]}, nil
}

// ParseMySQLQuery reads one MySQL packet and, if its payload is a
// COM_QUERY command (0x03 followed by the SQL text), returns the parsed
// query. Wire format:
//
//	[3-byte length LE][1-byte seq][0x03][SQL text bytes...]
func ParseMySQLQuery(r io.Reader) (Query, error) {
	hdr, err := ReadMySQLPacketHeader(r)
	if err != nil {
		return Query{}, err
	}
	if hdr.Length == 0 {
		return Query{}, fmt.Errorf("dbwire/mysql: empty packet payload")
	}
	payload := make([]byte, hdr.Length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Query{}, fmt.Errorf("dbwire/mysql: read packet payload: %w", err)
	}
	if payload[0] != comQuery {
		return Query{}, fmt.Errorf("dbwire/mysql: not a COM_QUERY packet (command byte 0x%02x)", payload[0])
	}
	return Query{Text: string(payload[1:]), Timestamp: nowNanos()}, nil
}

// MySQLParser implements ProtoParser for MySQL's client/server protocol.
// Requests are packets whose payload starts with COM_QUERY (0x03).
// Responses are recognized heuristically: an OK packet (first payload
// byte 0x00) or an ERR packet (0xff), which are the terminal packets for
// a simple query's result set (or immediate failure). This does not
// attempt to track full result-set streaming (column defs + row data +
// EOF), only the OK/ERR terminal markers, which is sufficient to bound
// latency for non-resultset statements and error responses.
type MySQLParser struct{}

// ParseRequest recognizes a COM_QUERY packet.
func (MySQLParser) ParseRequest(frame []byte) (string, bool) {
	q, err := ParseMySQLQuery(bytes.NewReader(frame))
	if err != nil {
		return "", false
	}
	return q.Text, true
}

// ParseResponse recognizes an OK (0x00) or ERR (0xff) terminal packet.
func (MySQLParser) ParseResponse(frame []byte) bool {
	hdr, err := ReadMySQLPacketHeader(bytes.NewReader(frame))
	if err != nil || hdr.Length == 0 {
		return false
	}
	if len(frame) < 5 {
		return false
	}
	marker := frame[4]
	return marker == 0x00 || marker == 0xff
}
