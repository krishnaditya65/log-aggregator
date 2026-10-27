package ingestbuffer

import (
	"encoding/binary"
	"fmt"
	"sync"
	"time"
)

// envelope wraps the caller's payload with the metadata (tenant, priority,
// timestamp) needed to reconstruct a full Message on replay/subscribe,
// since the WAL only knows about opaque byte payloads.
//
// Wire format: [2B tenantLen][tenant][2B priorityLen][priority][8B unixNano][payload...]
func encodeEnvelope(tenantID, priority string, ts time.Time, payload []byte) []byte {
	buf := make([]byte, 2+len(tenantID)+2+len(priority)+8+len(payload))
	i := 0
	binary.BigEndian.PutUint16(buf[i:], uint16(len(tenantID)))
	i += 2
	i += copy(buf[i:], tenantID)
	binary.BigEndian.PutUint16(buf[i:], uint16(len(priority)))
	i += 2
	i += copy(buf[i:], priority)
	binary.BigEndian.PutUint64(buf[i:], uint64(ts.UnixNano()))
	i += 8
	copy(buf[i:], payload)
	return buf
}

func decodeEnvelope(b []byte) (tenantID, priority string, ts time.Time, payload []byte, err error) {
	if len(b) < 2 {
		return "", "", time.Time{}, nil, fmt.Errorf("ingestbuffer: envelope too short")
	}
	i := 0
	tl := int(binary.BigEndian.Uint16(b[i:]))
	i += 2
	if len(b) < i+tl+2 {
		return "", "", time.Time{}, nil, fmt.Errorf("ingestbuffer: corrupt envelope")
	}
	tenantID = string(b[i : i+tl])
	i += tl
	pl := int(binary.BigEndian.Uint16(b[i:]))
	i += 2
	if len(b) < i+pl+8 {
		return "", "", time.Time{}, nil, fmt.Errorf("ingestbuffer: corrupt envelope")
	}
	priority = string(b[i : i+pl])
	i += pl
	nanos := int64(binary.BigEndian.Uint64(b[i:]))
	i += 8
	ts = time.Unix(0, nanos).UTC()
	payload = append([]byte(nil), b[i:]...)
	return tenantID, priority, ts, payload, nil
}

// partition owns one WAL, its replicas, and live subscribers.
type partition struct {
	id          int
	wal         *WAL
	replicators []Replicator
	quorum      int // including the primary WAL write itself

	mu              sync.Mutex
	committedOffset int64 // highest offset acknowledged consumed (Commit)
	subs            []chan Message
}

func newPartition(id int, wal *WAL, replicators []Replicator) *partition {
	total := 1 + len(replicators)
	quorum := total/2 + 1
	return &partition{
		id:              id,
		wal:             wal,
		replicators:     replicators,
		quorum:          quorum,
		committedOffset: -1,
	}
}

// publish appends+replicates a message, returning its offset once a
// replica quorum has durably written it, and fans it out to live
// subscribers.
func (p *partition) publish(tenantID, priority string, payload []byte) (int64, error) {
	envelope := encodeEnvelope(tenantID, priority, time.Now(), payload)
	offset, rec, err := p.wal.Append(envelope)
	if err != nil {
		return 0, fmt.Errorf("ingestbuffer: wal append: %w", err)
	}

	acks := 1 // the primary WAL write above counts as the first ack
	if len(p.replicators) > 0 {
		type result struct{ err error }
		results := make(chan result, len(p.replicators))
		for _, r := range p.replicators {
			r := r
			go func() {
				results <- result{err: r.Replicate(rec)}
			}()
		}
		for range p.replicators {
			res := <-results
			if res.err == nil {
				acks++
			}
		}
	}
	if acks < p.quorum {
		return offset, ErrQuorumFailed
	}

	msg := Message{Partition: p.id, Offset: offset, TenantID: tenantID, Priority: priority, Payload: payload, Timestamp: time.Now()}
	p.broadcast(msg)
	return offset, nil
}

func (p *partition) broadcast(msg Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ch := range p.subs {
		select {
		case ch <- msg:
		default:
			// Slow subscriber: drop for live delivery. It can always catch
			// up via a fresh Subscribe(partition, fromOffset) replay.
		}
	}
}

// depth is the current backlog size: how many published-but-not-yet
// committed messages this partition holds.
func (p *partition) depth() int64 {
	p.mu.Lock()
	committed := p.committedOffset
	p.mu.Unlock()
	next := p.wal.NextOffset()
	backlog := next - (committed + 1)
	if backlog < 0 {
		return 0
	}
	return backlog
}

func (p *partition) commit(offset int64) {
	p.mu.Lock()
	if offset > p.committedOffset {
		p.committedOffset = offset
	}
	p.mu.Unlock()
}

func (p *partition) committed() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.committedOffset
}

// subscribe registers a new live-delivery channel and, in a background
// goroutine, replays every persisted entry from fromOffset before any live
// messages are guaranteed to have started flowing (there can be a small
// overlap/duplicate window right at the replay/live boundary by design -
// same "at-least-once" tradeoff any commit-log style consumer makes;
// downstream consumers should treat offset as their de-dup key).
func (p *partition) subscribe(fromOffset int64, bufSize int) (<-chan Message, error) {
	if bufSize <= 0 {
		bufSize = 64
	}
	ch := make(chan Message, bufSize)
	p.mu.Lock()
	p.subs = append(p.subs, ch)
	p.mu.Unlock()

	go func() {
		entries, err := p.wal.ReadFrom(fromOffset)
		if err != nil {
			return
		}
		for _, e := range entries {
			tenantID, priority, ts, payload, err := decodeEnvelope(e.Payload)
			if err != nil {
				continue
			}
			msg := Message{Partition: p.id, Offset: e.Offset, TenantID: tenantID, Priority: priority, Payload: payload, Timestamp: ts}
			ch <- msg
		}
	}()

	return ch, nil
}

func (p *partition) close() error {
	p.mu.Lock()
	for _, ch := range p.subs {
		close(ch)
	}
	p.subs = nil
	p.mu.Unlock()
	return p.wal.Close()
}
