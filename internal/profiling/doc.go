// Package profiling implements continuous CPU profiling for the log
// aggregator, using only the Go standard library (runtime/pprof,
// compress/gzip). It is real and cross-platform: profiles are captured by
// runtime/pprof.StartCPUProfile/StopCPUProfile over short sampling
// windows, tagged with service/host/time, and retained in memory.
//
// In addition to storing raw pprof-format bytes, this package hand-decodes
// the pprof wire format (a gzip-compressed protobuf, see
// https://github.com/google/pprof/blob/main/proto/profile.proto) well
// enough to reconstruct a call-tree ("flamegraph") of function names and
// sample durations, without depending on google/pprof or any protobuf
// library. The protobuf reader in pprofdecode.go implements just the
// subset of the wire format (varint/length-delimited field parsing) that
// profile.proto requires.
package profiling
