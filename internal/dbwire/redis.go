package dbwire

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ParseRESP reads one Redis RESP2 command array from r and reconstructs
// it as a space-joined query string. Expected wire format:
//
//	*<argc>\r\n
//	$<len>\r\n<arg>\r\n   (repeated argc times)
//
// e.g. `*3\r\n$3\r\nSET\r\n$3\r\nfoo\r\n$3\r\nbar\r\n` -> "SET foo bar".
func ParseRESP(r io.Reader) (Query, error) {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(r)
	}

	line, err := readRESPLine(br)
	if err != nil {
		return Query{}, fmt.Errorf("dbwire/redis: read array header: %w", err)
	}
	if len(line) == 0 || line[0] != '*' {
		return Query{}, fmt.Errorf("dbwire/redis: expected array header '*', got %q", line)
	}
	argc, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil {
		return Query{}, fmt.Errorf("dbwire/redis: invalid argc %q: %w", line[1:], err)
	}
	if argc < 0 {
		return Query{}, fmt.Errorf("dbwire/redis: negative argc %d", argc)
	}

	args := make([]string, 0, argc)
	for i := 0; i < argc; i++ {
		bulkHeader, err := readRESPLine(br)
		if err != nil {
			return Query{}, fmt.Errorf("dbwire/redis: read bulk header %d: %w", i, err)
		}
		if len(bulkHeader) == 0 || bulkHeader[0] != '$' {
			return Query{}, fmt.Errorf("dbwire/redis: expected bulk header '$', got %q", bulkHeader)
		}
		blen, err := strconv.Atoi(strings.TrimSpace(bulkHeader[1:]))
		if err != nil {
			return Query{}, fmt.Errorf("dbwire/redis: invalid bulk length %q: %w", bulkHeader[1:], err)
		}
		if blen < 0 {
			args = append(args, "") // null bulk string
			continue
		}
		buf := make([]byte, blen+2) // +2 for trailing \r\n
		if _, err := io.ReadFull(br, buf); err != nil {
			return Query{}, fmt.Errorf("dbwire/redis: read bulk arg %d: %w", i, err)
		}
		args = append(args, string(buf[:blen]))
	}

	return Query{Text: strings.Join(args, " "), Timestamp: nowNanos()}, nil
}

// readRESPLine reads a single CRLF-terminated line, returning it without
// the trailing \r\n.
func readRESPLine(br *bufio.Reader) (string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return line, nil
}

// RedisParser implements ProtoParser for RESP2 traffic: any well-formed
// command array (`*...`) is a request; any other top-level RESP type
// (+simple string, -error, :integer, $bulk string, or a nested array
// used as a reply) is treated as a response.
type RedisParser struct{}

// ParseRequest reports whether frame is a RESP2 command array and, if
// so, reconstructs it into a space-joined op string.
func (RedisParser) ParseRequest(frame []byte) (string, bool) {
	if len(frame) == 0 || frame[0] != '*' {
		return "", false
	}
	q, err := ParseRESP(newByteReader(frame))
	if err != nil {
		return "", false
	}
	return q.Text, true
}

// ParseResponse reports whether frame looks like a RESP2 reply (any type
// other than an array-of-bulk-strings command frame): simple string,
// error, integer, or bulk string.
func (RedisParser) ParseResponse(frame []byte) bool {
	if len(frame) == 0 {
		return false
	}
	switch frame[0] {
	case '+', '-', ':', '$':
		return true
	case '*':
		// An array reply (e.g. from MGET) also counts as a response;
		// distinguish from a request heuristically: RESP2 requests sent
		// by clients are always arrays of bulk strings, but so can array
		// replies be. Callers needing precise distinction should track
		// direction (client->server vs server->client) at the transport
		// level; here we conservatively treat top-level arrays as
		// requests, consistent with ParseRequest.
		return false
	default:
		return false
	}
}

func newByteReader(b []byte) io.Reader {
	return &sliceReader{b: b}
}

type sliceReader struct {
	b   []byte
	pos int
}

func (s *sliceReader) Read(p []byte) (int, error) {
	if s.pos >= len(s.b) {
		return 0, io.EOF
	}
	n := copy(p, s.b[s.pos:])
	s.pos += n
	return n, nil
}
