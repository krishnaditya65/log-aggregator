package dbwire

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// PostgresStartupParams holds parsed startup packet key/value pairs
// (e.g. "user", "database").
type PostgresStartupParams map[string]string

// ParsePostgresStartup parses a PostgreSQL startup packet:
//
//	int32 length
//	int32 protocolVersion
//	cstring key, cstring value, ... , terminating \x00
//
// Startup packets have no leading message-type byte (unlike all
// subsequent frontend/backend messages).
func ParsePostgresStartup(r io.Reader) (PostgresStartupParams, error) {
	var length int32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return nil, fmt.Errorf("dbwire/postgres: read startup length: %w", err)
	}
	if length < 8 {
		return nil, fmt.Errorf("dbwire/postgres: implausible startup length %d", length)
	}
	rest := make([]byte, length-4)
	if _, err := io.ReadFull(r, rest); err != nil {
		return nil, fmt.Errorf("dbwire/postgres: read startup body: %w", err)
	}
	if len(rest) < 4 {
		return nil, fmt.Errorf("dbwire/postgres: startup body too short")
	}
	// rest[0:4] = protocol version, ignored here.
	params := make(PostgresStartupParams)
	buf := rest[4:]
	for len(buf) > 0 && buf[0] != 0x00 {
		keyEnd := bytes.IndexByte(buf, 0)
		if keyEnd < 0 {
			return nil, fmt.Errorf("dbwire/postgres: unterminated startup key")
		}
		key := string(buf[:keyEnd])
		buf = buf[keyEnd+1:]
		valEnd := bytes.IndexByte(buf, 0)
		if valEnd < 0 {
			return nil, fmt.Errorf("dbwire/postgres: unterminated startup value")
		}
		val := string(buf[:valEnd])
		buf = buf[valEnd+1:]
		params[key] = val
	}
	return params, nil
}

// ParsePostgresQuery parses a frontend simple-query message:
//
//	'Q' int32 length cstring(query)
//
// length counts itself + the query bytes (including the trailing NUL),
// but excludes the leading 'Q' type byte.
func ParsePostgresQuery(r io.Reader) (Query, error) {
	var msgType [1]byte
	if _, err := io.ReadFull(r, msgType[:]); err != nil {
		return Query{}, fmt.Errorf("dbwire/postgres: read message type: %w", err)
	}
	if msgType[0] != 'Q' {
		return Query{}, fmt.Errorf("dbwire/postgres: expected 'Q' simple-query message, got %q", msgType[0])
	}
	var length int32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return Query{}, fmt.Errorf("dbwire/postgres: read query length: %w", err)
	}
	if length < 4 {
		return Query{}, fmt.Errorf("dbwire/postgres: implausible query length %d", length)
	}
	body := make([]byte, length-4)
	if _, err := io.ReadFull(r, body); err != nil {
		return Query{}, fmt.Errorf("dbwire/postgres: read query body: %w", err)
	}
	text := string(bytes.TrimRight(body, "\x00"))
	return Query{Text: text, Timestamp: nowNanos()}, nil
}

// PostgresBackendMessage is a minimal parsed backend message: type byte
// plus raw payload (excluding the type byte and length prefix).
type PostgresBackendMessage struct {
	Type    byte
	Payload []byte
}

// ParsePostgresBackendMessage parses one backend message frame:
// <type byte><int32 length><payload>, where length includes itself but
// not the type byte. Used to recognize 'C' (CommandComplete) and 'Z'
// (ReadyForQuery), which bound the latency of a preceding simple query.
func ParsePostgresBackendMessage(r io.Reader) (PostgresBackendMessage, error) {
	var msgType [1]byte
	if _, err := io.ReadFull(r, msgType[:]); err != nil {
		return PostgresBackendMessage{}, fmt.Errorf("dbwire/postgres: read backend message type: %w", err)
	}
	var length int32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return PostgresBackendMessage{}, fmt.Errorf("dbwire/postgres: read backend message length: %w", err)
	}
	if length < 4 {
		return PostgresBackendMessage{}, fmt.Errorf("dbwire/postgres: implausible backend message length %d", length)
	}
	payload := make([]byte, length-4)
	if _, err := io.ReadFull(r, payload); err != nil {
		return PostgresBackendMessage{}, fmt.Errorf("dbwire/postgres: read backend message payload: %w", err)
	}
	return PostgresBackendMessage{Type: msgType[0], Payload: payload}, nil
}

// PostgresParser implements ProtoParser for the simple query protocol:
// 'Q' frames are requests; 'C' (CommandComplete) or 'Z' (ReadyForQuery)
// frames are responses that bound query latency.
type PostgresParser struct{}

// ParseRequest recognizes a simple-query ('Q') frontend message.
func (PostgresParser) ParseRequest(frame []byte) (string, bool) {
	if len(frame) == 0 || frame[0] != 'Q' {
		return "", false
	}
	q, err := ParsePostgresQuery(bytes.NewReader(frame))
	if err != nil {
		return "", false
	}
	return q.Text, true
}

// ParseResponse recognizes CommandComplete or ReadyForQuery backend
// messages, either of which marks completion of the preceding query.
func (PostgresParser) ParseResponse(frame []byte) bool {
	if len(frame) == 0 {
		return false
	}
	return frame[0] == 'C' || frame[0] == 'Z'
}
