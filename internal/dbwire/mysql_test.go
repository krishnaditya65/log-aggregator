package dbwire

import (
	"bytes"
	"testing"
)

// buildMySQLComQuery builds a raw MySQL packet carrying a COM_QUERY
// command: [3-byte length LE][1-byte seq][0x03][sql].
func buildMySQLComQuery(seq byte, sql string) []byte {
	payload := append([]byte{comQuery}, []byte(sql)...)
	var out bytes.Buffer
	length := len(payload)
	out.WriteByte(byte(length & 0xff))
	out.WriteByte(byte((length >> 8) & 0xff))
	out.WriteByte(byte((length >> 16) & 0xff))
	out.WriteByte(seq)
	out.Write(payload)
	return out.Bytes()
}

func TestParseMySQLQuery(t *testing.T) {
	raw := buildMySQLComQuery(0, "SELECT * FROM orders")
	q, err := ParseMySQLQuery(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ParseMySQLQuery: %v", err)
	}
	if q.Text != "SELECT * FROM orders" {
		t.Fatalf("unexpected query text: %q", q.Text)
	}
}

func TestParseMySQLQueryHeaderFields(t *testing.T) {
	raw := buildMySQLComQuery(7, "SELECT 1")
	hdr, err := ReadMySQLPacketHeader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadMySQLPacketHeader: %v", err)
	}
	if hdr.Sequence != 7 {
		t.Fatalf("expected sequence 7, got %d", hdr.Sequence)
	}
	if hdr.Length != uint32(1+len("SELECT 1")) {
		t.Fatalf("unexpected length: %d", hdr.Length)
	}
}

func TestParseMySQLQueryWrongCommand(t *testing.T) {
	// 0x01 = COM_QUIT rather than COM_QUERY.
	payload := []byte{0x01}
	var out bytes.Buffer
	out.WriteByte(byte(len(payload)))
	out.WriteByte(0)
	out.WriteByte(0)
	out.WriteByte(0)
	out.Write(payload)

	_, err := ParseMySQLQuery(bytes.NewReader(out.Bytes()))
	if err == nil {
		t.Fatalf("expected error for non-COM_QUERY packet")
	}
}

func TestMySQLParserProtoParser(t *testing.T) {
	var mp MySQLParser
	req := buildMySQLComQuery(0, "UPDATE t SET x=1")
	op, ok := mp.ParseRequest(req)
	if !ok || op != "UPDATE t SET x=1" {
		t.Fatalf("expected request recognized, got %q ok=%v", op, ok)
	}

	// OK packet: header + payload starting with 0x00.
	okPacket := []byte{5, 0, 0, 1, 0x00, 0, 0, 0, 0}
	if !mp.ParseResponse(okPacket) {
		t.Fatalf("expected OK packet recognized as response")
	}

	// ERR packet: payload starting with 0xff.
	errPacket := []byte{3, 0, 0, 1, 0xff, 0, 0}
	if !mp.ParseResponse(errPacket) {
		t.Fatalf("expected ERR packet recognized as response")
	}

	if mp.ParseResponse(req) {
		t.Fatalf("did not expect COM_QUERY request classified as response")
	}
}

func TestMySQLCorrelatorLatency(t *testing.T) {
	corr := NewCorrelator(MySQLParser{})
	req := buildMySQLComQuery(0, "SELECT SLEEP(0.02)")
	okPacket := []byte{5, 0, 0, 1, 0x00, 1, 0, 0, 0}

	if _, ok := corr.Observe("c1", req, fixedTime(0)); ok {
		t.Fatalf("did not expect completion on request")
	}
	completed, ok := corr.Observe("c1", okPacket, fixedTime(20_000_000))
	if !ok {
		t.Fatalf("expected completion on OK packet")
	}
	if completed.Duration.Milliseconds() != 20 {
		t.Fatalf("expected 20ms, got %v", completed.Duration)
	}
}
