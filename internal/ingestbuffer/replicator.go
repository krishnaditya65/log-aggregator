package ingestbuffer

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Replicator receives a copy of every WAL entry (already framed) and must
// durably persist it before returning nil. Publish only acks once a
// quorum of configured Replicators (see Config.ReplicaDirs) have done so.
type Replicator interface {
	Replicate(entry []byte) error
}

// dirReplicator is an in-process simulation of a remote replica AZ: it
// appends every entry to its own file under a dedicated directory, fsyncing
// on every write, exactly like the primary WAL does. In a real distributed
// system each dirReplicator would instead be a network client to a
// follower node; the quorum-ack logic in partition.go is agnostic to that
// distinction, which is the whole point of the Replicator interface.
type dirReplicator struct {
	mu   sync.Mutex
	file *os.File
}

func newDirReplicator(dir, name string) (*dirReplicator, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("ingestbuffer: replica dir %s: %w", dir, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("ingestbuffer: open replica file: %w", err)
	}
	return &dirReplicator{file: f}, nil
}

func (r *dirReplicator) Replicate(entry []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.file.Write(entry); err != nil {
		return err
	}
	return r.file.Sync()
}

func (r *dirReplicator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.file.Close()
}
