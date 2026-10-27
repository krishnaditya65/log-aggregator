// Package mcptrace traces Model Context Protocol (MCP) JSON-RPC 2.0
// traffic. It wraps an io.ReadWriter or http.RoundTripper, parses each
// JSON-RPC message it observes, correlates requests with their responses
// by "id", measures latency, and emits a callback with the completed call.
package mcptrace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// MCPCall represents one correlated JSON-RPC request/response exchange.
type MCPCall struct {
	Method    string
	RequestID any
	StartTime int64 // unix nanos, when request was observed
	EndTime   int64 // unix nanos, when response was observed
	Params    json.RawMessage
	Result    json.RawMessage
	Error     *string
}

// Latency returns the round-trip time for the call.
func (c MCPCall) Latency() time.Duration {
	if c.EndTime <= c.StartTime {
		return 0
	}
	return time.Duration(c.EndTime - c.StartTime)
}

// rpcMessage is the wire shape of a JSON-RPC 2.0 request or response.
// Requests carry Method (+ optional Params); responses carry Result or
// Error but never Method. ID is present on both (notifications, which
// have no ID, are ignored by this tracer since they cannot be
// correlated).
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func (m rpcMessage) isRequest() bool {
	return m.Method != ""
}

func (m rpcMessage) isResponse() bool {
	return m.Method == "" && (m.Result != nil || m.Error != nil)
}

func idKey(id json.RawMessage) string {
	return string(bytes.TrimSpace(id))
}

// pendingCall tracks an in-flight request awaiting its response.
type pendingCall struct {
	method    string
	requestID any
	params    json.RawMessage
	startTime int64
}

// Tracer observes JSON-RPC 2.0 traffic and emits correlated MCPCall
// events via OnCall.
type Tracer struct {
	OnCall func(MCPCall)

	mu      sync.Mutex
	pending map[string]pendingCall
	now     func() time.Time
}

// NewTracer constructs a Tracer that invokes onCall for every correlated
// request/response pair it observes.
func NewTracer(onCall func(MCPCall)) *Tracer {
	return &Tracer{
		OnCall:  onCall,
		pending: make(map[string]pendingCall),
		now:     time.Now,
	}
}

// ObserveMessage parses a single raw JSON-RPC message (as would appear on
// one line of a newline-delimited JSON-RPC stream, or one HTTP body) and
// updates tracer state. Requests are stashed as pending; responses are
// matched against pending requests by id and, on match, trigger OnCall.
func (t *Tracer) ObserveMessage(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	var msg rpcMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return fmt.Errorf("mcptrace: invalid JSON-RPC message: %w", err)
	}

	switch {
	case msg.isRequest():
		if len(msg.ID) == 0 {
			// Notification: no ID, cannot correlate a response. Ignore.
			return nil
		}
		var reqID any
		_ = json.Unmarshal(msg.ID, &reqID)
		t.mu.Lock()
		t.pending[idKey(msg.ID)] = pendingCall{
			method:    msg.Method,
			requestID: reqID,
			params:    msg.Params,
			startTime: t.now().UnixNano(),
		}
		t.mu.Unlock()
		return nil

	case msg.isResponse():
		if len(msg.ID) == 0 {
			return nil
		}
		key := idKey(msg.ID)
		t.mu.Lock()
		pc, ok := t.pending[key]
		if ok {
			delete(t.pending, key)
		}
		t.mu.Unlock()

		end := t.now().UnixNano()
		call := MCPCall{
			EndTime: end,
			Result:  msg.Result,
		}
		if ok {
			call.Method = pc.method
			call.RequestID = pc.requestID
			call.StartTime = pc.startTime
			call.Params = pc.params
		} else {
			// Response with no matching request: still report it, with
			// StartTime == EndTime (zero latency, unknown method).
			var respID any
			_ = json.Unmarshal(msg.ID, &respID)
			call.RequestID = respID
			call.StartTime = end
		}
		if len(msg.Error) > 0 {
			errStr := string(msg.Error)
			call.Error = &errStr
		}
		if t.OnCall != nil {
			t.OnCall(call)
		}
		return nil

	default:
		return fmt.Errorf("mcptrace: message is neither a valid request nor response: %s", raw)
	}
}

// StreamReader continuously reads newline-delimited JSON-RPC messages
// from r and feeds them to ObserveMessage until r is exhausted or ctx
// read fails. It blocks; run it in a goroutine. Malformed lines are
// skipped (not fatal) so a single bad frame doesn't kill the tracer.
func (t *Tracer) StreamReader(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		_ = t.ObserveMessage(line) // best-effort; malformed frames ignored
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return fmt.Errorf("mcptrace: stream read: %w", err)
	}
	return nil
}

// Wrap returns an io.ReadWriter that transparently proxies to rw while
// feeding every complete newline-delimited JSON-RPC message written to
// (outbound = requests) and read from (inbound = responses) it into the
// tracer. It assumes one JSON-RPC message per line, matching common MCP
// stdio transport framing.
func (t *Tracer) Wrap(rw io.ReadWriter) io.ReadWriter {
	return &tracedReadWriter{rw: rw, tracer: t}
}

type tracedReadWriter struct {
	rw     io.ReadWriter
	tracer *Tracer
	rbuf   bytes.Buffer
	wbuf   bytes.Buffer
}

func (t *tracedReadWriter) Write(p []byte) (int, error) {
	n, err := t.rw.Write(p)
	t.wbuf.Write(p[:n])
	t.tracer.drainLines(&t.wbuf)
	return n, err
}

func (t *tracedReadWriter) Read(p []byte) (int, error) {
	n, err := t.rw.Read(p)
	if n > 0 {
		t.rbuf.Write(p[:n])
		t.tracer.drainLines(&t.rbuf)
	}
	return n, err
}

// drainLines extracts and observes any complete newline-terminated
// messages currently buffered, leaving a trailing partial line (if any)
// in buf for the next call.
func (t *Tracer) drainLines(buf *bytes.Buffer) {
	for {
		data := buf.Bytes()
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			return
		}
		line := make([]byte, idx)
		copy(line, data[:idx])
		buf.Next(idx + 1)
		_ = t.ObserveMessage(line)
	}
}

// RoundTripper wraps an http.RoundTripper, tracing MCP-over-HTTP traffic:
// the outbound request body is treated as one JSON-RPC request message,
// and the response body as the matching response message.
type roundTripper struct {
	next   http.RoundTripper
	tracer *Tracer
}

// WrapRoundTripper returns an http.RoundTripper that traces MCP JSON-RPC
// calls made over HTTP (request body = request message, response body =
// response message), delegating actual transport to next.
func (t *Tracer) WrapRoundTripper(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &roundTripper{next: next, tracer: t}
}

func (rt *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err == nil {
			reqBody = b
			req.Body = io.NopCloser(bytes.NewReader(b))
		}
	}
	if len(reqBody) > 0 {
		_ = rt.tracer.ObserveMessage(reqBody)
	}

	resp, err := rt.next.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if resp.Body != nil {
		b, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(b))
		if rerr == nil && len(b) > 0 {
			_ = rt.tracer.ObserveMessage(b)
		}
	}
	return resp, nil
}
