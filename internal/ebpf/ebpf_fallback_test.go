//go:build !linux

package ebpf

import (
	"errors"
	"testing"
	"time"
)

func TestSyntheticCollectorPushCollect(t *testing.T) {
	c := NewSyntheticCollector()
	if got := c.Collect(); len(got) != 0 {
		t.Fatalf("expected empty collect, got %d", len(got))
	}

	evt := NewLoopbackEvent(123, "curl", 5000, 443, 2048, 15*time.Millisecond, false)
	c.Push(evt)

	got := c.Collect()
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	if got[0].PID != 123 || got[0].Comm != "curl" {
		t.Fatalf("unexpected event: %+v", got[0])
	}
	if got[0].SrcPort != 5000 || got[0].DstPort != 443 {
		t.Fatalf("unexpected ports: %+v", got[0])
	}
	if got[0].Bytes != 2048 {
		t.Fatalf("unexpected bytes: %+v", got[0])
	}
	if got[0].DurationNS != uint64(15*time.Millisecond) {
		t.Fatalf("unexpected duration: %+v", got[0])
	}

	// Collect drains; second call empty.
	if got := c.Collect(); len(got) != 0 {
		t.Fatalf("expected drained collect, got %d", len(got))
	}
}

func TestRecorderPollAndEviction(t *testing.T) {
	c := NewSyntheticCollector()
	r := NewRecorder(c, 3)

	for i := 0; i < 5; i++ {
		c.Push(NewLoopbackEvent(uint32(i), "svc", 1000, 80, 100, time.Millisecond, i%2 == 0))
		if n := r.Poll(); n != 1 {
			t.Fatalf("poll %d: expected 1 new event, got %d", i, n)
		}
	}

	events := r.Events()
	if len(events) != 3 {
		t.Fatalf("expected ring capped at 3, got %d", len(events))
	}
	// Oldest 2 (PID 0,1) should have been evicted; remaining should be PID 2,3,4.
	wantPIDs := []uint32{2, 3, 4}
	for i, e := range events {
		if e.PID != wantPIDs[i] {
			t.Fatalf("event[%d].PID = %d, want %d", i, e.PID, wantPIDs[i])
		}
	}
}

func TestRecorderRunStopsOnDone(t *testing.T) {
	c := NewSyntheticCollector()
	c.Push(NewLoopbackEvent(1, "svc", 1, 2, 3, time.Millisecond, false))
	r := NewRecorder(c, 10)

	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		r.Run(2*time.Millisecond, done)
		close(finished)
	}()

	time.Sleep(20 * time.Millisecond)
	close(done)

	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after done was closed")
	}

	if len(r.Events()) == 0 {
		t.Fatal("expected at least one event to have been polled before stop")
	}
}

func TestTLSVisibilityUnsupportedOnFallback(t *testing.T) {
	tv := NewTLSVisibility()
	err := tv.OnPlaintext(func(direction string, data []byte) {
		t.Fatal("callback should never be invoked on fallback platform")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("expected ErrUnsupportedPlatform, got %v", err)
	}
}

func TestNewLoopbackEventAddresses(t *testing.T) {
	evt := NewLoopbackEvent(1, "test", 1, 2, 3, time.Second, true)
	// 127.0.0.1 big-endian == 0x7F000001
	const loopback = 0x7F000001
	if evt.SrcAddr != loopback || evt.DstAddr != loopback {
		t.Fatalf("expected loopback addr 0x%X, got src=0x%X dst=0x%X", loopback, evt.SrcAddr, evt.DstAddr)
	}
	if !evt.IsError {
		t.Fatal("expected IsError true")
	}
}
