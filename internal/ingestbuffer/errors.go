package ingestbuffer

import "errors"

var (
	// ErrBackpressure is returned by Publish when the target partition is
	// over its configured depth threshold and the buffer's backpressure
	// policy is PolicyReject (the default). Callers should back off/retry.
	ErrBackpressure = errors.New("ingestbuffer: backpressure: partition over threshold")

	// ErrDropped is returned by Publish when a message was deliberately
	// dropped under backpressure because it carried a low priority (e.g.
	// "debug") - see Buffer's priority-aware shedding policy.
	ErrDropped = errors.New("ingestbuffer: message dropped under backpressure (low priority)")

	// ErrRateLimited is returned by Publish when the tenant's token bucket
	// has no tokens available.
	ErrRateLimited = errors.New("ingestbuffer: rate limited")

	// ErrQuorumFailed is returned when Publish could not get durable acks
	// from a quorum of replicas.
	ErrQuorumFailed = errors.New("ingestbuffer: failed to reach replica quorum")

	// ErrClosed is returned by operations on a closed Buffer.
	ErrClosed = errors.New("ingestbuffer: buffer is closed")

	// ErrUnknownPartition is returned when a partition index is out of range.
	ErrUnknownPartition = errors.New("ingestbuffer: unknown partition")
)
