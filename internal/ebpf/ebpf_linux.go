//go:build linux

package ebpf

// This file is the REAL Linux implementation, built only with
// //go:build linux. It has NOT been compiled or run in this sandbox
// (darwin, no Linux kernel/CAP_BPF available) — see the package doc
// comment in doc.go for details. It requires:
//
//  1. A pre-built eBPF object file matching bpf/red_probes.c and
//     bpf/ssl_uprobes.c (e.g. produced by `clang -target bpf` + a CO-RE
//     toolchain, or github.com/cilium/ebpf/cmd/bpf2go). This code assumes
//     that object has been embedded/loaded as `progSpec` bytes at
//     `redProbeObjPath` / `sslUprobeObjPath` — wire up the actual path or
//     go:embed directive for your build once you have the compiled .o.
//  2. The external module github.com/cilium/ebpf (NOT currently in
//     go.mod — see package report / go.mod note: add
//     `github.com/cilium/ebpf vX.Y.Z` and run `go mod tidy` on a Linux
//     dev box before this file can build).
//  3. CAP_BPF (or CAP_SYS_ADMIN on older kernels) at runtime, plus kernel
//     BTF for CO-RE relocations.
//
// The loader shape below mirrors the real cilium/ebpf v0.12+ API
// (ebpf.CollectionSpec / link.Kprobe / link.Uprobe / perf.Reader).

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/perf"
)

// redProbeObjPath is the path to the compiled BPF object implementing the
// program documented in bpf/red_probes.c (kprobe/tcp_sendmsg,
// kretprobe/tcp_recvmsg -> perf array "red_events"). Override via
// LinuxCollector's constructor in real deployments.
const redProbeObjPath = "/usr/local/share/log-aggregator/bpf/red_probes.o"

// sslUprobeObjPath is the path to the compiled BPF object implementing
// bpf/ssl_uprobes.c (uprobe/SSL_write, uprobe+uretprobe/SSL_read -> perf
// array "tls_events").
const sslUprobeObjPath = "/usr/local/share/log-aggregator/bpf/ssl_uprobes.o"

// wireREDEvent is the exact byte layout emitted by struct red_event in
// bpf/red_probes.c. Field order and sizes must match the C struct;
// note the C struct is not explicitly packed, so on x86_64/arm64 with
// natural alignment this layout (u32,16 bytes,u32,u32,u16,u16,u64,u64,u8 +
// padding) matches what the compiler emits. In production this should be
// verified against `pahole` output for the target kernel/arch.
type wireREDEvent struct {
	PID        uint32
	Comm       [16]byte
	SAddr      uint32
	DAddr      uint32
	SPort      uint16
	DPort      uint16
	_          [4]byte // padding to align the following u64s
	Bytes      uint64
	DurationNS uint64
	IsError    uint8
	_          [7]byte // trailing struct padding
}

// LinuxCollector implements Collector by loading and attaching the RED
// eBPF program and draining its perf-event-array.
type LinuxCollector struct {
	coll    *ebpf.Collection
	kprobe  link.Link
	kretpb  link.Link
	reader  *perf.Reader
	closeCh chan struct{}
}

// NewLinuxCollector loads redProbeObjPath, attaches the kprobe/kretprobe
// pair on tcp_sendmsg/tcp_recvmsg, and opens a perf reader on the
// "red_events" map. Requires root/CAP_BPF.
func NewLinuxCollector() (*LinuxCollector, error) {
	spec, err := ebpf.LoadCollectionSpec(redProbeObjPath)
	if err != nil {
		return nil, fmt.Errorf("ebpf: load collection spec: %w", err)
	}
	coll, err := ebpf.NewCollection(spec)
	if err != nil {
		return nil, fmt.Errorf("ebpf: new collection: %w", err)
	}

	kp, err := link.Kprobe("tcp_sendmsg", coll.Programs["kprobe__tcp_sendmsg"], nil)
	if err != nil {
		coll.Close()
		return nil, fmt.Errorf("ebpf: attach kprobe tcp_sendmsg: %w", err)
	}
	krp, err := link.Kretprobe("tcp_recvmsg", coll.Programs["kretprobe__tcp_recvmsg"], nil)
	if err != nil {
		kp.Close()
		coll.Close()
		return nil, fmt.Errorf("ebpf: attach kretprobe tcp_recvmsg: %w", err)
	}

	rd, err := perf.NewReader(coll.Maps["red_events"], os.Getpagesize()*8)
	if err != nil {
		krp.Close()
		kp.Close()
		coll.Close()
		return nil, fmt.Errorf("ebpf: open perf reader: %w", err)
	}

	return &LinuxCollector{
		coll:    coll,
		kprobe:  kp,
		kretpb:  krp,
		reader:  rd,
		closeCh: make(chan struct{}),
	}, nil
}

// Collect reads whatever perf-event records are currently available
// (non-blocking-ish: perf.Reader.Read blocks until at least one record or
// Close; callers typically run this in its own goroutine loop rather than
// polling like the fallback collector).
func (l *LinuxCollector) Collect() []REDEvent {
	var out []REDEvent
	for {
		rec, err := l.reader.Read()
		if err != nil {
			// Reader closed or no more immediately available data.
			break
		}
		if rec.LostSamples > 0 {
			continue
		}
		if len(rec.RawSample) < 4 {
			continue
		}
		var w wireREDEvent
		if err := binary.Read(bytes.NewReader(rec.RawSample), binary.LittleEndian, &w); err != nil {
			continue
		}
		out = append(out, REDEvent{
			PID:        w.PID,
			Comm:       cString(w.Comm[:]),
			SrcAddr:    w.SAddr,
			DstAddr:    w.DAddr,
			SrcPort:    w.SPort,
			DstPort:    w.DPort,
			Bytes:      w.Bytes,
			DurationNS: w.DurationNS,
			IsError:    w.IsError != 0,
		})
		// Real deployments would loop with a select on a stop channel;
		// kept simple here since this path is untested in-sandbox.
		if len(out) > 0 {
			break
		}
	}
	return out
}

// Close detaches probes and releases kernel resources.
func (l *LinuxCollector) Close() error {
	close(l.closeCh)
	_ = l.reader.Close()
	_ = l.kretpb.Close()
	_ = l.kprobe.Close()
	l.coll.Close()
	return nil
}

func cString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// linuxTLS implements TLSVisibility via uprobes on SSL_write/SSL_read,
// per bpf/ssl_uprobes.c.
type linuxTLS struct {
	coll     *ebpf.Collection
	uprobes  []link.Link
	reader   *perf.Reader
	execPath string
}

// NewTLSVisibilityFor returns a real Linux TLSVisibility implementation
// that uprobes SSL_write/SSL_read in the given executable (typically a
// resolved libssl.so path). Requires root/CAP_BPF and a binary that
// exports those symbols (or has embedded BTF/DWARF to resolve them).
func NewTLSVisibilityFor(execPath string) (TLSVisibility, error) {
	ex, err := link.OpenExecutable(execPath)
	if err != nil {
		return nil, fmt.Errorf("ebpf: open executable %s: %w", execPath, err)
	}
	spec, err := ebpf.LoadCollectionSpec(sslUprobeObjPath)
	if err != nil {
		return nil, fmt.Errorf("ebpf: load ssl uprobe spec: %w", err)
	}
	coll, err := ebpf.NewCollection(spec)
	if err != nil {
		return nil, fmt.Errorf("ebpf: new ssl collection: %w", err)
	}

	var links []link.Link
	up, err := ex.Uprobe("SSL_write", coll.Programs["uprobe__SSL_write"], nil)
	if err != nil {
		coll.Close()
		return nil, fmt.Errorf("ebpf: attach uprobe SSL_write: %w", err)
	}
	links = append(links, up)

	upR, err := ex.Uprobe("SSL_read", coll.Programs["uprobe__SSL_read"], nil)
	if err != nil {
		for _, l := range links {
			l.Close()
		}
		coll.Close()
		return nil, fmt.Errorf("ebpf: attach uprobe SSL_read: %w", err)
	}
	links = append(links, upR)

	upRet, err := ex.Uretprobe("SSL_read", coll.Programs["uretprobe__SSL_read"], nil)
	if err != nil {
		for _, l := range links {
			l.Close()
		}
		coll.Close()
		return nil, fmt.Errorf("ebpf: attach uretprobe SSL_read: %w", err)
	}
	links = append(links, upRet)

	rd, err := perf.NewReader(coll.Maps["tls_events"], os.Getpagesize()*8)
	if err != nil {
		for _, l := range links {
			l.Close()
		}
		coll.Close()
		return nil, fmt.Errorf("ebpf: open tls perf reader: %w", err)
	}

	return &linuxTLS{coll: coll, uprobes: links, reader: rd, execPath: execPath}, nil
}

// OnPlaintext starts a background-style read loop (caller is expected to
// invoke this once and let it drive fn for the lifetime of the reader; a
// production version would run this in its own goroutine with a stop
// channel/context).
func (t *linuxTLS) OnPlaintext(fn func(direction string, data []byte)) error {
	for {
		rec, err := t.reader.Read()
		if err != nil {
			return fmt.Errorf("ebpf: tls perf read: %w", err)
		}
		if rec.LostSamples > 0 || len(rec.RawSample) < 9 {
			continue
		}
		direction := "write"
		if rec.RawSample[4] == 1 {
			direction = "read"
		}
		length := binary.LittleEndian.Uint32(rec.RawSample[5:9])
		data := rec.RawSample[9:]
		if int(length) < len(data) {
			data = data[:length]
		}
		fn(direction, data)
	}
}
