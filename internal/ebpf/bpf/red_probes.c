// red_probes.c — intended eBPF C source (documentation only, not compiled by
// this Go package). This file describes the kernel-side program that
// ebpf_linux.go is designed to load via github.com/cilium/ebpf on a real
// Linux host with CAP_BPF/CAP_SYS_ADMIN. It is NOT built by `go build` or
// `go test` — it exists purely as a reference for what the real eBPF
// bytecode would do, since we cannot compile/attach BPF programs in this
// darwin sandbox.
//
// Intended toolchain: clang -O2 -target bpf -c red_probes.c -o red_probes.o
// then loaded with cilium/ebpf's bpf2go or a hand-built collection spec
// (see ebpf_linux.go for the loader shape that mirrors this program).
//
// +build ignore

#include <linux/bpf.h>
#include <linux/ptrace.h>
#include <bpf/bpf_helpers.h>

// REDEvent mirrors the Go struct ebpf.REDEvent — field order/sizes must
// match exactly since it is read back via a perf-event-array reader.
struct red_event {
    __u32 pid;
    char  comm[16];
    __u32 saddr;
    __u32 daddr;
    __u16 sport;
    __u16 dport;
    __u64 bytes;
    __u64 duration_ns;
    __u8  is_error;
};

// Perf event array used to stream events to userspace.
struct {
    __uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
    __uint(key_size, sizeof(int));
    __uint(value_size, sizeof(int));
} red_events SEC(".maps");

// Hash map tracking in-flight send/recv start timestamps, keyed by
// (pid_tgid), so tcp_sendmsg -> kfree_skb / return probes can compute
// a duration for RED (Rate/Errors/Duration) metrics.
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 10240);
    __type(key, __u64);
    __type(value, __u64);
} start_ns SEC(".maps");

// kprobe on tcp_sendmsg: record start timestamp and emit a partial RED
// event with byte count taken from the `size_t size` argument (arg3 on
// x86_64 System V ABI: rdi=sock, rsi=msghdr, rdx=size).
SEC("kprobe/tcp_sendmsg")
int kprobe__tcp_sendmsg(struct pt_regs *ctx) {
    __u64 pid_tgid = bpf_get_current_pid_tgid();
    __u64 ts = bpf_ktime_get_ns();
    bpf_map_update_elem(&start_ns, &pid_tgid, &ts, BPF_ANY);

    struct red_event evt = {};
    evt.pid = pid_tgid >> 32;
    bpf_get_current_comm(&evt.comm, sizeof(evt.comm));
    evt.bytes = (__u64)PT_REGS_PARM3(ctx);
    evt.duration_ns = 0; // filled on completion probe in a fuller impl
    evt.is_error = 0;

    bpf_perf_event_output(ctx, &red_events, BPF_F_CURRENT_CPU, &evt, sizeof(evt));
    return 0;
}

// kretprobe on tcp_recvmsg: compute duration from start_ns and emit a
// completion event; a negative return value maps to is_error=1.
SEC("kretprobe/tcp_recvmsg")
int kretprobe__tcp_recvmsg(struct pt_regs *ctx) {
    __u64 pid_tgid = bpf_get_current_pid_tgid();
    __u64 *start = bpf_map_lookup_elem(&start_ns, &pid_tgid);
    long ret = PT_REGS_RC(ctx);

    struct red_event evt = {};
    evt.pid = pid_tgid >> 32;
    bpf_get_current_comm(&evt.comm, sizeof(evt.comm));
    if (start) {
        evt.duration_ns = bpf_ktime_get_ns() - *start;
        bpf_map_delete_elem(&start_ns, &pid_tgid);
    }
    evt.bytes = ret > 0 ? (__u64)ret : 0;
    evt.is_error = ret < 0 ? 1 : 0;

    bpf_perf_event_output(ctx, &red_events, BPF_F_CURRENT_CPU, &evt, sizeof(evt));
    return 0;
}

char _license[] SEC("license") = "GPL";
