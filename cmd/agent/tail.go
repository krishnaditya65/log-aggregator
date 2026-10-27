package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
)

const fingerprintSize = 64

// TailFile follows path like `tail -f`: it reads the file's existing
// content once, then keeps polling for appended lines, emitting each
// complete line as a Record on out.
//
// Rotation is detected three ways, any of which triggers a reopen from
// the start of the (new) file at path:
//   - the inode backing path changed (classic logrotate rename+create),
//   - the file's size dropped below our last-read offset (truncate),
//   - the file's leading bytes no longer match a "fingerprint" we took
//     of the bytes we've already read (handles copytruncate: truncate
//     followed immediately by a rewrite that grows the file back past
//     our old offset before we get a chance to poll in between — a
//     plain size comparison alone would miss that transient shrink).
//
// TailFile blocks until ctx is cancelled.
func TailFile(ctx context.Context, path string, pollInterval time.Duration, out chan<- Record, warn func(format string, args ...any)) error {
	var f *os.File
	var reader *bufio.Reader
	var ino uint64
	var pos int64
	var fingerprint []byte

	open := func() error {
		ff, err := os.Open(path)
		if err != nil {
			return err
		}
		fi, err := ff.Stat()
		if err != nil {
			ff.Close()
			return err
		}
		f = ff
		ino = inodeOf(fi)
		reader = bufio.NewReader(f)
		pos = 0
		fingerprint = nil
		return nil
	}

	updateFingerprint := func() {
		n := fingerprintSize
		if int64(n) > pos {
			n = int(pos)
		}
		if n <= len(fingerprint) {
			return // already have at least this much of a baseline
		}
		hdr, err := readHeader(path, n)
		if err == nil && len(hdr) == n {
			fingerprint = hdr
		}
	}

	// Wait for the file to exist if it doesn't yet.
	for {
		if err := open(); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	defer f.Close()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	drain := func() {
		for {
			line, err := reader.ReadString('\n')
			if len(line) > 0 {
				if strings.HasSuffix(line, "\n") {
					pos += int64(len(line))
					out <- Record{
						Type:      "log",
						Timestamp: time.Now(),
						Source:    path,
						Line:      strings.TrimRight(line, "\n"),
					}
				} else {
					// Partial line at EOF: don't advance pos past it,
					// and push the bytes back in front of the reader so
					// the next drain re-reads them once more data
					// (including the rest of the line) arrives.
					reader = bufio.NewReader(io.MultiReader(strings.NewReader(line), reader))
				}
			}
			if err != nil {
				return
			}
		}
	}

	drain()
	updateFingerprint()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			fi, err := os.Stat(path)
			if err != nil {
				if os.IsNotExist(err) {
					continue // wait for it to reappear
				}
				warn("tail %s: stat: %v", path, err)
				continue
			}
			rotated := false
			if newIno := inodeOf(fi); newIno != ino {
				rotated = true
			} else if fi.Size() < pos {
				rotated = true
			} else if len(fingerprint) > 0 {
				hdr, err := readHeader(path, len(fingerprint))
				if err == nil && !bytes.Equal(hdr, fingerprint) {
					rotated = true
				}
			}
			if rotated {
				warn("tail %s: rotation detected, reopening", path)
				f.Close()
				if err := open(); err != nil {
					if !os.IsNotExist(err) {
						warn("tail %s: reopen failed: %v", path, err)
					}
					continue
				}
			}
			drain()
			updateFingerprint()
		}
	}
}

// readHeader reads up to n bytes from the start of path via a fresh,
// independent file handle (does not disturb the tailer's own read
// position).
func readHeader(path string, n int) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	read, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf[:read], nil
}

func inodeOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
