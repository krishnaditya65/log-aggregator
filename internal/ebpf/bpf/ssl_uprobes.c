// ssl_uprobes.c — intended eBPF C source for encrypted-payload visibility
// (documentation only; not compiled by this Go package). Describes uprobes
// attached to libssl's SSL_write/SSL_read to observe plaintext application
// data *before* encryption / *after* decryption, i.e. without needing a
// TLS MITM proxy. Real-world equivalent of what tools like Pixie/Beyla do.
//
// Intended toolchain: same as red_probes.c, attached via
// (*ebpf.Program).AttachUprobe against the resolved path+offset of
// SSL_write/SSL_read in libssl.so (or a statically-linked binary), matched
// with cilium/ebpf's link.OpenExecutable + Uprobe().
//
// +build ignore

#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>

#define MAX_CHUNK 256

struct tls_event {
    __u32 pid;
    __u8  direction; // 0 = write (outbound plaintext), 1 = read (inbound plaintext)
    __u32 len;
    char  data[MAX_CHUNK];
};

struct {
    __uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
    __uint(key_size, sizeof(int));
    __uint(value_size, sizeof(int));
} tls_events SEC(".maps");

// Map from pid_tgid -> buffer pointer captured at uprobe entry, consumed at
// the paired uretprobe once the real byte count is known (SSL_read's
// buffer is only populated *after* the call returns).
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 4096);
    __type(key, __u64);
    __type(value, __u64);
} buf_ptrs SEC(".maps");

// uprobe/SSL_write: args (SSL *ssl, const void *buf, int num) — x86_64:
// rdi=ssl, rsi=buf, rdx=num. Plaintext is already in `buf` at entry.
SEC("uprobe/SSL_write")
int uprobe__SSL_write(struct pt_regs *ctx) {
    struct tls_event evt = {};
    __u64 pid_tgid = bpf_get_current_pid_tgid();
    evt.pid = pid_tgid >> 32;
    evt.direction = 0;
    void *buf = (void *)PT_REGS_PARM2(ctx);
    int num = (int)PT_REGS_PARM3(ctx);
    evt.len = num > MAX_CHUNK ? MAX_CHUNK : (num < 0 ? 0 : num);
    bpf_probe_read_user(&evt.data, evt.len, buf);
    bpf_perf_event_output(ctx, &tls_events, BPF_F_CURRENT_CPU, &evt, sizeof(evt));
    return 0;
}

// uprobe/SSL_read (entry): stash buf pointer for the uretprobe.
SEC("uprobe/SSL_read")
int uprobe__SSL_read(struct pt_regs *ctx) {
    __u64 pid_tgid = bpf_get_current_pid_tgid();
    __u64 buf = (__u64)PT_REGS_PARM2(ctx);
    bpf_map_update_elem(&buf_ptrs, &pid_tgid, &buf, BPF_ANY);
    return 0;
}

// uretprobe/SSL_read (return): now the decrypted plaintext has been
// written into the buffer captured at entry; return value is byte count.
SEC("uretprobe/SSL_read")
int uretprobe__SSL_read(struct pt_regs *ctx) {
    __u64 pid_tgid = bpf_get_current_pid_tgid();
    __u64 *bufp = bpf_map_lookup_elem(&buf_ptrs, &pid_tgid);
    long n = PT_REGS_RC(ctx);
    if (!bufp || n <= 0) return 0;

    struct tls_event evt = {};
    evt.pid = pid_tgid >> 32;
    evt.direction = 1;
    evt.len = n > MAX_CHUNK ? MAX_CHUNK : n;
    bpf_probe_read_user(&evt.data, evt.len, (void *)*bufp);
    bpf_perf_event_output(ctx, &tls_events, BPF_F_CURRENT_CPU, &evt, sizeof(evt));
    bpf_map_delete_elem(&buf_ptrs, &pid_tgid);
    return 0;
}

char _license[] SEC("license") = "GPL";
