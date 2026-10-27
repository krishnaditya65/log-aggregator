// Package ebpf provides kernel-level RED (Rate/Errors/Duration) network
// capture and encrypted-payload (TLS) visibility for the log aggregator.
//
// There are two build-tagged implementations:
//
//   - ebpf_linux.go (//go:build linux): the REAL implementation. It uses
//     github.com/cilium/ebpf to load and attach the eBPF programs
//     documented (as C source, for reference — this package does not
//     compile C) under internal/ebpf/bpf/: kprobes on tcp_sendmsg /
//     tcp_recvmsg for RED metrics, and uprobes on SSL_write / SSL_read for
//     plaintext visibility into encrypted traffic. This file is written to
//     be correct against the cilium/ebpf API, but it has NOT been built or
//     run in this environment: the sandbox is darwin (no Linux kernel, no
//     CAP_BPF, no BPF syscall) so there is no way to compile-check or
//     execute it here. Treat it as reviewed-but-untested code that needs a
//     real Linux CI/dev box (with clang/llvm-strip and kernel headers to
//     build the .o, or a pre-built CO-RE object) before it can be trusted
//     in production.
//
//   - ebpf_fallback.go (//go:build !linux): a REAL, fully working, tested
//     userspace approximation. It cannot see kernel-level socket events or
//     decrypted TLS buffers — there is no eBPF on darwin — so instead it
//     defines the same REDEvent/TLSVisibility surface backed by a
//     pluggable Collector, so the rest of the aggregator (and its tests)
//     can depend on the ebpf package's public API on any platform. This is
//     the code path exercised by `go test` in this sandbox.
//
// Both files produce the same REDEvent shape, so callers can be written
// once against the exported types/interfaces in this package and linked
// against whichever build tag matches the target OS.
package ebpf

// REDEvent is one observed network operation, expressed in RED
// (Rate/Errors/Duration) terms. On Linux this is populated from a
// perf-event-array populated by kprobes on tcp_sendmsg/tcp_recvmsg (see
// bpf/red_probes.c). On other platforms it is synthesized by a Collector.
type REDEvent struct {
	PID        uint32
	Comm       string
	SrcAddr    uint32 // IPv4, network byte order
	DstAddr    uint32 // IPv4, network byte order
	SrcPort    uint16
	DstPort    uint16
	Bytes      uint64
	DurationNS uint64
	IsError    bool
}

// TLSVisibility exposes decrypted (plaintext) payloads observed at the
// point of a SSL_write/SSL_read call, before encryption or after
// decryption respectively. On Linux this is backed by uprobes into
// libssl (see bpf/ssl_uprobes.c); it requires attaching to a process that
// links OpenSSL/BoringSSL-compatible symbols. Elsewhere it is unsupported.
type TLSVisibility interface {
	// OnPlaintext registers a callback invoked for every observed
	// plaintext chunk. direction is "write" (outbound, pre-encryption)
	// or "read" (inbound, post-decryption).
	OnPlaintext(fn func(direction string, data []byte)) error
}

// ErrUnsupportedPlatform is returned by TLSVisibility implementations
// (and other kernel-dependent operations) on platforms without a real
// eBPF-based implementation.
type ErrUnsupportedPlatformType struct{}

func (ErrUnsupportedPlatformType) Error() string {
	return "ebpf: TLS visibility via SSL uprobes is unsupported on this platform (requires Linux + eBPF)"
}

// ErrUnsupportedPlatform is the sentinel value; compare with errors.Is.
var ErrUnsupportedPlatform error = ErrUnsupportedPlatformType{}
