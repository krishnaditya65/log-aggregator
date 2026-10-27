package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func collectLines(t *testing.T, ch <-chan Record, n int, timeout time.Duration) []string {
	t.Helper()
	var lines []string
	deadline := time.After(timeout)
	for len(lines) < n {
		select {
		case rec := <-ch:
			lines = append(lines, rec.Line)
		case <-deadline:
			t.Fatalf("timed out waiting for %d lines, got %d: %v", n, len(lines), lines)
		}
	}
	return lines
}

func TestTailFileBasicAndAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	if err := os.WriteFile(path, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan Record, 100)
	go func() {
		_ = TailFile(ctx, path, 30*time.Millisecond, out, t.Logf)
	}()

	got := collectLines(t, out, 2, 3*time.Second)
	if got[0] != "line1" || got[1] != "line2" {
		t.Fatalf("unexpected initial lines: %v", got)
	}

	// Append more lines.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("line3\nline4\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got2 := collectLines(t, out, 2, 3*time.Second)
	if got2[0] != "line3" || got2[1] != "line4" {
		t.Fatalf("unexpected appended lines: %v", got2)
	}
}

func TestTailFileHandlesRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rotate.log")

	if err := os.WriteFile(path, []byte("before-rotate-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan Record, 100)
	go func() {
		_ = TailFile(ctx, path, 20*time.Millisecond, out, t.Logf)
	}()

	got := collectLines(t, out, 1, 3*time.Second)
	if got[0] != "before-rotate-1" {
		t.Fatalf("unexpected line: %v", got)
	}

	// Simulate log rotation: truncate + rewrite with new (smaller)
	// content, same path, same inode (truncate in place) — should be
	// detected via size shrink.
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("after-rotate-1\nafter-rotate-2\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got2 := collectLines(t, out, 2, 3*time.Second)
	if got2[0] != "after-rotate-1" || got2[1] != "after-rotate-2" {
		t.Fatalf("unexpected post-rotation lines: %v", got2)
	}
}

func TestTailFileHandlesInodeRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rotate2.log")

	if err := os.WriteFile(path, []byte("old-file-line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan Record, 100)
	go func() {
		_ = TailFile(ctx, path, 20*time.Millisecond, out, t.Logf)
	}()

	got := collectLines(t, out, 1, 3*time.Second)
	if got[0] != "old-file-line" {
		t.Fatalf("unexpected line: %v", got)
	}

	// Classic logrotate-style rotation: rename the old file away and
	// create a brand new file at the same path (different inode).
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new-file-line-1\nnew-file-line-2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got2 := collectLines(t, out, 2, 3*time.Second)
	if got2[0] != "new-file-line-1" || got2[1] != "new-file-line-2" {
		t.Fatalf("unexpected post-rename-rotation lines: %v", got2)
	}
}
