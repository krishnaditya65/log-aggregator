// Package dbwire implements real byte-level parsers for common database
// wire protocols (Redis RESP2, PostgreSQL, MySQL, MongoDB), extracting
// query text/command and timing information directly from protocol
// frames, plus a generic request/response Correlator for bounding
// per-query latency on a connection.
package dbwire

import "time"

// Query is the normalized result of parsing a single request frame from
// any supported wire protocol.
type Query struct {
	// Text holds the reconstructed query/command text for protocols that
	// carry raw SQL/commands (Redis, PostgreSQL, MySQL).
	Text string
	// Command holds the top-level command name for protocols that are
	// structured/document-based (MongoDB).
	Command string
	// Timestamp is when the frame was observed, unix nanos.
	Timestamp int64
}

func nowNanos() int64 { return time.Now().UnixNano() }

// ProtoParser recognizes request and response frames for a specific wire
// protocol, without necessarily understanding full message semantics.
// It's the minimal contract the Correlator needs to pair requests with
// responses on a connection.
type ProtoParser interface {
	// ParseRequest reports whether frame is a client request, and if so
	// returns a short operation label (e.g. a SQL query, command name,
	// or opcode name).
	ParseRequest(frame []byte) (op string, ok bool)
	// ParseResponse reports whether frame is a server response that
	// completes a pending request.
	ParseResponse(frame []byte) (ok bool)
}

// PendingOp describes a request awaiting its response on a connection.
type PendingOp struct {
	Op    string
	Start time.Time
}

// CorrelatedOp is a completed request/response pair with measured
// latency.
type CorrelatedOp struct {
	ConnID   string
	Op       string
	Start    time.Time
	End      time.Time
	Duration time.Duration
}

// Correlator pairs request and response frames observed on the same
// connection (identified by an opaque connID string), using a
// protocol-specific ProtoParser to recognize frame roles. Requests are
// matched to responses FIFO per connection, which is correct for
// strictly synchronous protocols (Postgres simple query, MySQL) and a
// reasonable approximation for pipelined ones (Redis, Mongo) as long as
// responses arrive in request order.
type Correlator struct {
	parser  ProtoParser
	pending map[string][]PendingOp
}

// NewCorrelator constructs a Correlator using the given protocol parser.
func NewCorrelator(parser ProtoParser) *Correlator {
	return &Correlator{parser: parser, pending: make(map[string][]PendingOp)}
}

// Observe feeds one frame observed at time t on connection connID into
// the correlator. If frame is a request, it is queued. If frame is a
// response and there is a pending request on that connection, the two
// are paired and returned as a completed CorrelatedOp (ok=true).
func (c *Correlator) Observe(connID string, frame []byte, t time.Time) (op CorrelatedOp, ok bool) {
	if reqOp, isReq := c.parser.ParseRequest(frame); isReq {
		c.pending[connID] = append(c.pending[connID], PendingOp{Op: reqOp, Start: t})
		return CorrelatedOp{}, false
	}
	if c.parser.ParseResponse(frame) {
		q := c.pending[connID]
		if len(q) == 0 {
			return CorrelatedOp{}, false
		}
		p := q[0]
		c.pending[connID] = q[1:]
		return CorrelatedOp{
			ConnID:   connID,
			Op:       p.Op,
			Start:    p.Start,
			End:      t,
			Duration: t.Sub(p.Start),
		}, true
	}
	return CorrelatedOp{}, false
}

// PendingCount returns the number of unresolved requests on a
// connection.
func (c *Correlator) PendingCount(connID string) int {
	return len(c.pending[connID])
}
