package ingestbuffer

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sync"
)

// WAL record framing:
//
//	[8 bytes offset (big-endian uint64)]
//	[4 bytes payload length (big-endian uint32)]
//	[payload...]
//	[4 bytes CRC32 (IEEE) of payload, big-endian]
//
// Every Append fsyncs before returning, giving durability semantics: once
// Append returns nil, the record survives a process crash (though not
// necessarily a full disk failure - that's what the Replicator quorum is
// for).
const walHeaderSize = 8 + 4
const walTrailerSize = 4

// encodeWALRecord frames a single record for offset `offset`.
func encodeWALRecord(offset int64, payload []byte) []byte {
	buf := make([]byte, walHeaderSize+len(payload)+walTrailerSize)
	binary.BigEndian.PutUint64(buf[0:8], uint64(offset))
	binary.BigEndian.PutUint32(buf[8:12], uint32(len(payload)))
	copy(buf[12:], payload)
	crc := crc32.ChecksumIEEE(payload)
	binary.BigEndian.PutUint32(buf[12+len(payload):], crc)
	return buf
}

// walEntry is one decoded record read back off disk.
type walEntry struct {
	Offset  int64
	Payload []byte
}

// decodeWALStream reads every well-formed record from r until EOF or the
// first corrupt/truncated record (which - since this is an append-only
// log that only ever gets extended by whole-record writes protected by
// fsync - can only happen if the tail write was interrupted mid-flight;
// we simply stop there, which is the standard WAL recovery strategy).
func decodeWALStream(r io.Reader) ([]walEntry, error) {
	var entries []walEntry
	hdr := make([]byte, walHeaderSize)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if err == io.EOF {
				return entries, nil
			}
			return entries, nil // truncated header: stop, keep what we have
		}
		offset := int64(binary.BigEndian.Uint64(hdr[0:8]))
		length := binary.BigEndian.Uint32(hdr[8:12])
		payload := make([]byte, length)
		if _, err := io.ReadFull(r, payload); err != nil {
			return entries, nil // truncated payload
		}
		trailer := make([]byte, walTrailerSize)
		if _, err := io.ReadFull(r, trailer); err != nil {
			return entries, nil // truncated trailer
		}
		wantCRC := binary.BigEndian.Uint32(trailer)
		gotCRC := crc32.ChecksumIEEE(payload)
		if wantCRC != gotCRC {
			// Corrupt tail record (e.g. a crash mid-write). Stop reading;
			// everything before this point is trustworthy.
			return entries, nil
		}
		entries = append(entries, walEntry{Offset: offset, Payload: payload})
	}
}

// WAL is the primary (leader) append-only, fsync-on-write log for a single
// partition.
type WAL struct {
	mu         sync.Mutex
	file       *os.File
	nextOffset int64
}

// openWAL opens (creating if necessary) the WAL file at path, replaying it
// to determine the next offset to assign.
func openWAL(path string) (*WAL, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("ingestbuffer: open wal %s: %w", path, err)
	}
	entries, err := decodeWALStream(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	next := int64(0)
	if len(entries) > 0 {
		next = entries[len(entries)-1].Offset + 1
	}
	// Truncate any trailing corrupt/partial bytes so future appends land
	// cleanly (recompute the valid length from the entries we trust).
	validLen := int64(0)
	for _, e := range entries {
		validLen += int64(walHeaderSize + len(e.Payload) + walTrailerSize)
	}
	if err := f.Truncate(validLen); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return nil, err
	}
	return &WAL{file: f, nextOffset: next}, nil
}

// Append writes and fsyncs a new record, assigning it the next sequential
// offset for this partition.
func (w *WAL) Append(payload []byte) (int64, []byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	offset := w.nextOffset
	rec := encodeWALRecord(offset, payload)
	if _, err := w.file.Write(rec); err != nil {
		return 0, nil, err
	}
	if err := w.file.Sync(); err != nil {
		return 0, nil, err
	}
	w.nextOffset++
	return offset, rec, nil
}

// ReadFrom returns every entry with Offset >= fromOffset, for replay.
func (w *WAL) ReadFrom(fromOffset int64) ([]walEntry, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	all, err := decodeWALStream(w.file)
	if _, serr := w.file.Seek(0, io.SeekEnd); serr != nil && err == nil {
		err = serr
	}
	if err != nil {
		return nil, err
	}
	out := all[:0:0]
	for _, e := range all {
		if e.Offset >= fromOffset {
			out = append(out, e)
		}
	}
	return out, nil
}

// NextOffset returns the offset that will be assigned to the next Append.
func (w *WAL) NextOffset() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.nextOffset
}

// Close closes the underlying file.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}
