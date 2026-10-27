package dbwire

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildPostgresStartup builds a raw startup packet with the given params.
func buildPostgresStartup(params map[string]string) []byte {
	var body bytes.Buffer
	// protocol version 3.0 = 196608
	var verBuf [4]byte
	binary.BigEndian.PutUint32(verBuf[:], 196608)
	body.Write(verBuf[:])
	for k, v := range params {
		body.WriteString(k)
		body.WriteByte(0)
		body.WriteString(v)
		body.WriteByte(0)
	}
	body.WriteByte(0) // terminator

	var out bytes.Buffer
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(4+body.Len()))
	out.Write(lenBuf[:])
	out.Write(body.Bytes())
	return out.Bytes()
}

func TestParsePostgresStartup(t *testing.T) {
	raw := buildPostgresStartup(map[string]string{"user": "alice", "database": "app"})
	params, err := ParsePostgresStartup(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ParsePostgresStartup: %v", err)
	}
	if params["user"] != "alice" || params["database"] != "app" {
		t.Fatalf("unexpected params: %+v", params)
	}
}

// buildPostgresQuery builds a raw 'Q' simple-query message.
func buildPostgresQuery(sql string) []byte {
	var out bytes.Buffer
	out.WriteByte('Q')
	payload := append([]byte(sql), 0x00)
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(4+len(payload)))
	out.Write(lenBuf[:])
	out.Write(payload)
	return out.Bytes()
}

func TestParsePostgresQuery(t *testing.T) {
	raw := buildPostgresQuery("SELECT * FROM users WHERE id = 1")
	q, err := ParsePostgresQuery(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ParsePostgresQuery: %v", err)
	}
	if q.Text != "SELECT * FROM users WHERE id = 1" {
		t.Fatalf("unexpected query text: %q", q.Text)
	}
}

func TestParsePostgresQueryWrongType(t *testing.T) {
	raw := []byte{'X', 0, 0, 0, 4}
	_, err := ParsePostgresQuery(bytes.NewReader(raw))
	if err == nil {
		t.Fatalf("expected error for non-'Q' message type")
	}
}

// buildPostgresBackendMessage builds a generic backend message frame.
func buildPostgresBackendMessage(msgType byte, payload []byte) []byte {
	var out bytes.Buffer
	out.WriteByte(msgType)
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(4+len(payload)))
	out.Write(lenBuf[:])
	out.Write(payload)
	return out.Bytes()
}

func TestParsePostgresBackendMessageCommandComplete(t *testing.T) {
	raw := buildPostgresBackendMessage('C', append([]byte("SELECT 1"), 0x00))
	msg, err := ParsePostgresBackendMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ParsePostgresBackendMessage: %v", err)
	}
	if msg.Type != 'C' {
		t.Fatalf("expected type 'C', got %q", msg.Type)
	}
}

func TestPostgresParserProtoParser(t *testing.T) {
	var pp PostgresParser
	q := buildPostgresQuery("SELECT 1")
	op, ok := pp.ParseRequest(q)
	if !ok || op != "SELECT 1" {
		t.Fatalf("expected request recognized as 'SELECT 1', got %q ok=%v", op, ok)
	}

	commandComplete := buildPostgresBackendMessage('C', append([]byte("SELECT 1"), 0))
	if !pp.ParseResponse(commandComplete) {
		t.Fatalf("expected CommandComplete recognized as response")
	}

	readyForQuery := buildPostgresBackendMessage('Z', []byte{'I'})
	if !pp.ParseResponse(readyForQuery) {
		t.Fatalf("expected ReadyForQuery recognized as response")
	}

	if pp.ParseResponse(q) {
		t.Fatalf("did not expect request frame classified as response")
	}
}

func TestPostgresLatencyBoundingViaCorrelator(t *testing.T) {
	corr := NewCorrelator(PostgresParser{})
	reqFrame := buildPostgresQuery("SELECT pg_sleep(0.01)")
	respFrame := buildPostgresBackendMessage('C', append([]byte("SELECT 1"), 0))

	start := fixedTime(0)
	if _, ok := corr.Observe("conn1", reqFrame, start); ok {
		t.Fatalf("did not expect completion on request frame")
	}
	end := fixedTime(10_000_000) // +10ms
	completed, ok := corr.Observe("conn1", respFrame, end)
	if !ok {
		t.Fatalf("expected completion on response frame")
	}
	if completed.Duration.Milliseconds() != 10 {
		t.Fatalf("expected 10ms latency, got %v", completed.Duration)
	}
	if completed.Op != "SELECT pg_sleep(0.01)" {
		t.Fatalf("unexpected op: %q", completed.Op)
	}
}
