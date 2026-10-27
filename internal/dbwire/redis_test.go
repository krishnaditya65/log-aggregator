package dbwire

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseRESPSimpleCommand(t *testing.T) {
	raw := "*3\r\n$3\r\nSET\r\n$3\r\nfoo\r\n$3\r\nbar\r\n"
	q, err := ParseRESP(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("ParseRESP: %v", err)
	}
	if q.Text != "SET foo bar" {
		t.Fatalf("expected 'SET foo bar', got %q", q.Text)
	}
	if q.Timestamp == 0 {
		t.Fatalf("expected non-zero timestamp")
	}
}

func TestParseRESPGetCommand(t *testing.T) {
	raw := "*2\r\n$3\r\nGET\r\n$3\r\nfoo\r\n"
	q, err := ParseRESP(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("ParseRESP: %v", err)
	}
	if q.Text != "GET foo" {
		t.Fatalf("expected 'GET foo', got %q", q.Text)
	}
}

func TestParseRESPEmptyArray(t *testing.T) {
	raw := "*0\r\n"
	q, err := ParseRESP(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("ParseRESP: %v", err)
	}
	if q.Text != "" {
		t.Fatalf("expected empty text for 0-arg command, got %q", q.Text)
	}
}

func TestParseRESPMalformedHeader(t *testing.T) {
	_, err := ParseRESP(strings.NewReader("+OK\r\n"))
	if err == nil {
		t.Fatalf("expected error for non-array header")
	}
}

func TestParseRESPTruncated(t *testing.T) {
	_, err := ParseRESP(strings.NewReader("*2\r\n$3\r\nGET\r\n"))
	if err == nil {
		t.Fatalf("expected error for truncated command")
	}
}

func TestRedisParserProtoParser(t *testing.T) {
	var rp RedisParser
	reqFrame := []byte("*2\r\n$4\r\nINCR\r\n$3\r\nctr\r\n")
	op, ok := rp.ParseRequest(reqFrame)
	if !ok {
		t.Fatalf("expected request recognized")
	}
	if op != "INCR ctr" {
		t.Fatalf("expected 'INCR ctr', got %q", op)
	}

	respFrame := []byte(":42\r\n")
	if !rp.ParseResponse(respFrame) {
		t.Fatalf("expected integer reply recognized as response")
	}

	simpleOK := []byte("+OK\r\n")
	if !rp.ParseResponse(simpleOK) {
		t.Fatalf("expected simple string reply recognized as response")
	}

	errReply := []byte("-ERR unknown command\r\n")
	if !rp.ParseResponse(errReply) {
		t.Fatalf("expected error reply recognized as response")
	}

	if rp.ParseResponse(reqFrame) {
		t.Fatalf("did not expect request frame to be misclassified as response")
	}
}

func TestParseRESPMultipleArgs(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("*4\r\n$4\r\nMSET\r\n$1\r\na\r\n$1\r\n1\r\n$1\r\nb\r\n")
	// Missing final value intentionally removed to check clean multi-arg first;
	// use a valid complete command below instead.
	raw := "*4\r\n$4\r\nHSET\r\n$3\r\nkey\r\n$5\r\nfield\r\n$5\r\nvalue\r\n"
	q, err := ParseRESP(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("ParseRESP: %v", err)
	}
	if q.Text != "HSET key field value" {
		t.Fatalf("expected 'HSET key field value', got %q", q.Text)
	}
}
