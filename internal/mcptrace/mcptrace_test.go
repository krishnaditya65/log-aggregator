package mcptrace

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestObserveMessageBasicRequestResponse(t *testing.T) {
	var mu sync.Mutex
	var calls []MCPCall
	tr := NewTracer(func(c MCPCall) {
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
	})

	req := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"cursor":null}}`
	if err := tr.ObserveMessage([]byte(req)); err != nil {
		t.Fatalf("ObserveMessage(request): %v", err)
	}

	resp := `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`
	if err := tr.ObserveMessage([]byte(resp)); err != nil {
		t.Fatalf("ObserveMessage(response): %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call emitted, got %d", len(calls))
	}
	c := calls[0]
	if c.Method != "tools/list" {
		t.Fatalf("expected method tools/list, got %q", c.Method)
	}
	// JSON numbers unmarshal to float64.
	if fmt.Sprintf("%v", c.RequestID) != "1" {
		t.Fatalf("expected request id 1, got %v", c.RequestID)
	}
	if c.Error != nil {
		t.Fatalf("expected no error, got %v", *c.Error)
	}
	if string(c.Result) != `{"tools":[]}` {
		t.Fatalf("unexpected result: %s", c.Result)
	}
	if c.EndTime < c.StartTime {
		t.Fatalf("expected EndTime >= StartTime")
	}
}

func TestObserveMessageErrorResponse(t *testing.T) {
	var calls []MCPCall
	tr := NewTracer(func(c MCPCall) { calls = append(calls, c) })

	_ = tr.ObserveMessage([]byte(`{"jsonrpc":"2.0","id":"abc","method":"resources/read","params":{"uri":"file:///x"}}`))
	_ = tr.ObserveMessage([]byte(`{"jsonrpc":"2.0","id":"abc","error":{"code":-32602,"message":"invalid params"}}`))

	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Error == nil {
		t.Fatalf("expected error to be set")
	}
	if !strings.Contains(*calls[0].Error, "invalid params") {
		t.Fatalf("expected error message content, got %s", *calls[0].Error)
	}
	if calls[0].RequestID != "abc" {
		t.Fatalf("expected string request id abc, got %v", calls[0].RequestID)
	}
}

func TestObserveMessageNotificationIgnored(t *testing.T) {
	var calls []MCPCall
	tr := NewTracer(func(c MCPCall) { calls = append(calls, c) })

	// Notification: no "id" field.
	err := tr.ObserveMessage([]byte(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":50}}`))
	if err != nil {
		t.Fatalf("unexpected error for notification: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected notifications not to be correlated, got %d calls", len(calls))
	}
}

func TestObserveMessageLatencyMeasured(t *testing.T) {
	tr := NewTracer(nil)
	var got MCPCall
	tr.OnCall = func(c MCPCall) { got = c }

	fixed := time.Unix(0, 1_000_000_000)
	tr.now = func() time.Time { return fixed }
	_ = tr.ObserveMessage([]byte(`{"jsonrpc":"2.0","id":5,"method":"ping"}`))

	fixed = time.Unix(0, 1_050_000_000) // +50ms
	tr.now = func() time.Time { return fixed }
	_ = tr.ObserveMessage([]byte(`{"jsonrpc":"2.0","id":5,"result":{}}`))

	if got.Latency() != 50*time.Millisecond {
		t.Fatalf("expected 50ms latency, got %v", got.Latency())
	}
}

func TestObserveMessageMalformed(t *testing.T) {
	tr := NewTracer(nil)
	if err := tr.ObserveMessage([]byte(`not json`)); err == nil {
		t.Fatalf("expected error for malformed JSON")
	}
}

func TestStreamReaderMultipleMessages(t *testing.T) {
	var calls []MCPCall
	tr := NewTracer(func(c MCPCall) { calls = append(calls, c) })

	stream := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo"}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"content":"hi"}}`,
	}, "\n")

	if err := tr.StreamReader(strings.NewReader(stream)); err != nil {
		t.Fatalf("StreamReader: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 correlated calls, got %d", len(calls))
	}
	if calls[0].Method != "initialize" || calls[1].Method != "tools/call" {
		t.Fatalf("unexpected methods: %v", calls)
	}
}

// pipeReadWriter adapts two io.Pipe halves into an io.ReadWriter for
// testing Wrap.
type pipeReadWriter struct {
	io.Reader
	io.Writer
}

func TestWrapReadWriter(t *testing.T) {
	var mu sync.Mutex
	var calls []MCPCall
	tr := NewTracer(func(c MCPCall) {
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
	})

	var backing bytes.Buffer
	wrapped := tr.Wrap(&pipeReadWriter{Reader: &backing, Writer: &backing})

	// Write a request line through the wrapper.
	_, err := wrapped.Write([]byte(`{"jsonrpc":"2.0","id":9,"method":"list"}` + "\n"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	// Reset backing so the "response" can be read back out separately.
	backing.Reset()
	backing.WriteString(`{"jsonrpc":"2.0","id":9,"result":{"n":3}}` + "\n")
	buf := make([]byte, 1024)
	if _, err := wrapped.Read(buf); err != nil && err != io.EOF {
		t.Fatalf("read: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("expected 1 correlated call via Wrap, got %d", len(calls))
	}
	if calls[0].Method != "list" {
		t.Fatalf("expected method 'list', got %q", calls[0].Method)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWrapRoundTripper(t *testing.T) {
	var calls []MCPCall
	tr := NewTracer(func(c MCPCall) { calls = append(calls, c) })

	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"jsonrpc":"2.0","id":42,"result":{"status":"done"}}`
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})

	client := &http.Client{Transport: tr.WrapRoundTripper(inner)}
	reqBody := `{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"x"}}`
	req, _ := http.NewRequest("POST", "http://example.invalid/mcp", strings.NewReader(reqBody))

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "done") {
		t.Fatalf("expected response body preserved, got %s", respBody)
	}

	if len(calls) != 1 {
		t.Fatalf("expected 1 correlated call, got %d", len(calls))
	}
	if calls[0].Method != "tools/call" {
		t.Fatalf("expected method tools/call, got %q", calls[0].Method)
	}
}
