package dbwire

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// MongoDB standard message header opcodes we care about.
const (
	opMsg = 2013 // OP_MSG: unified request/response opcode since MongoDB 3.6
)

// MongoHeader is MongoDB's standard 16-byte wire protocol message
// header.
type MongoHeader struct {
	MessageLength int32
	RequestID     int32
	ResponseTo    int32
	OpCode        int32
}

// ReadMongoHeader reads and decodes the 16-byte standard message header.
func ReadMongoHeader(r io.Reader) (MongoHeader, error) {
	var raw [16]byte
	if _, err := io.ReadFull(r, raw[:]); err != nil {
		return MongoHeader{}, fmt.Errorf("dbwire/mongo: read header: %w", err)
	}
	return MongoHeader{
		MessageLength: int32(binary.LittleEndian.Uint32(raw[0:4])),
		RequestID:     int32(binary.LittleEndian.Uint32(raw[4:8])),
		ResponseTo:    int32(binary.LittleEndian.Uint32(raw[8:12])),
		OpCode:        int32(binary.LittleEndian.Uint32(raw[12:16])),
	}, nil
}

// ParseMongoOpMsg reads a full MongoDB wire protocol message from r and,
// if it is an OP_MSG (opcode 2013), extracts the top-level command name
// from the first BSON document found in the first section. Wire format:
//
//	[16-byte header]
//	[uint32 flagBits]
//	section: [uint8 kind=0][BSON document]   (kind 0 = body, the common
//	                                           case for commands)
//
// The BSON document's first top-level key is the command name (e.g.
// {find: "coll", filter: {}} -> "find").
func ParseMongoOpMsg(r io.Reader) (Query, error) {
	hdr, err := ReadMongoHeader(r)
	if err != nil {
		return Query{}, err
	}
	if hdr.OpCode != opMsg {
		return Query{}, fmt.Errorf("dbwire/mongo: unsupported opcode %d (only OP_MSG/%d handled)", hdr.OpCode, opMsg)
	}
	remaining := int(hdr.MessageLength) - 16
	if remaining < 4 {
		return Query{}, fmt.Errorf("dbwire/mongo: implausible message length %d", hdr.MessageLength)
	}
	body := make([]byte, remaining)
	if _, err := io.ReadFull(r, body); err != nil {
		return Query{}, fmt.Errorf("dbwire/mongo: read message body: %w", err)
	}
	// body[0:4] = flagBits, ignored (checksum-present bit etc. not
	// needed to locate the first section for our purposes).
	if len(body) < 5 {
		return Query{}, fmt.Errorf("dbwire/mongo: message body too short for a section")
	}
	kind := body[4]
	if kind != 0x00 {
		return Query{}, fmt.Errorf("dbwire/mongo: first section kind %d not supported (expected 0=body)", kind)
	}
	doc := body[5:]
	cmd, err := bsonFirstKey(doc)
	if err != nil {
		return Query{}, fmt.Errorf("dbwire/mongo: parse command document: %w", err)
	}
	return Query{Command: cmd, Timestamp: nowNanos()}, nil
}

// bsonFirstKey performs a minimal BSON parse: it reads the document's
// int32 length prefix, then the first element's type byte and cstring
// name, returning that name. It does not decode the element's value or
// any subsequent elements -- sufficient to identify a MongoDB command
// document's command name, which is always the first key.
func bsonFirstKey(data []byte) (string, error) {
	if len(data) < 5 {
		return "", fmt.Errorf("document too short")
	}
	docLen := int32(binary.LittleEndian.Uint32(data[0:4]))
	if int(docLen) > len(data) {
		return "", fmt.Errorf("declared document length %d exceeds available data %d", docLen, len(data))
	}
	pos := 4
	elemType := data[pos]
	pos++
	if elemType == 0x00 {
		return "", fmt.Errorf("empty document (no elements)")
	}
	nameEnd := bytes.IndexByte(data[pos:], 0x00)
	if nameEnd < 0 {
		return "", fmt.Errorf("unterminated element name")
	}
	name := string(data[pos : pos+nameEnd])
	if name == "" {
		return "", fmt.Errorf("empty element name")
	}
	return name, nil
}

// MongoParser implements ProtoParser for OP_MSG traffic. A client
// request has ResponseTo == 0; a server response has a non-zero
// ResponseTo referencing the request's RequestID. This mirrors the real
// MongoDB wire protocol convention.
type MongoParser struct{}

// ParseRequest recognizes an OP_MSG frame with ResponseTo == 0.
func (MongoParser) ParseRequest(frame []byte) (string, bool) {
	if len(frame) < 16 {
		return "", false
	}
	hdr, err := ReadMongoHeader(bytes.NewReader(frame))
	if err != nil || hdr.OpCode != opMsg || hdr.ResponseTo != 0 {
		return "", false
	}
	q, err := ParseMongoOpMsg(bytes.NewReader(frame))
	if err != nil {
		return "", false
	}
	return q.Command, true
}

// ParseResponse recognizes an OP_MSG frame with a non-zero ResponseTo.
func (MongoParser) ParseResponse(frame []byte) bool {
	if len(frame) < 16 {
		return false
	}
	hdr, err := ReadMongoHeader(bytes.NewReader(frame))
	if err != nil {
		return false
	}
	return hdr.OpCode == opMsg && hdr.ResponseTo != 0
}
