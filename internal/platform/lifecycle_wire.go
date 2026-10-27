package platform

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"custom-log-aggregator/internal/coldstore"
	"custom-log-aggregator/internal/lifecycle"
)

// newColdLifecycle wires internal/lifecycle's policy engine over
// already-archived cold-storage objects (rather than over tsdb/logsdb's
// live segment files, which those engines already retire safely
// themselves via OnExpireSegment - see platform.go). This gives operators
// a second, independently configurable tier: how long archived data stays
// in cold storage before being deleted outright, per service/tenant.
//
// Key convention produced by archiveRawSegment is
// "<engine>/raw/<minTime>-<maxTime>-<basename>"; segmentMetaFromKey parses
// that back into a lifecycle.SegmentMeta.
func newColdLifecycle(store coldstore.Blobstore) *lifecycle.Manager {
	lister := func() []lifecycle.SegmentMeta {
		var out []lifecycle.SegmentMeta
		for _, engine := range []string{"tsdb", "logsdb"} {
			keys, err := store.List(context.Background(), engine+"/raw/")
			if err != nil {
				continue
			}
			for _, k := range keys {
				meta, ok := segmentMetaFromKey(engine, k)
				if ok {
					out = append(out, meta)
				}
			}
		}
		return out
	}

	archive := func(key string) error {
		// Already archived by definition (that's how it entered the
		// Lister) - the "warm" tier here is a no-op; a real deployment
		// could transition storage class (e.g. S3 Standard -> Glacier)
		// in this callback via a storage-class-aware Blobstore.
		return nil
	}

	del := func(key string) error {
		type deleter interface {
			Delete(ctx context.Context, key string) error
		}
		d, ok := store.(deleter)
		if !ok {
			log.Printf("lifecycle: blobstore backend does not support Delete, skipping expiry of %s", key)
			return nil
		}
		return d.Delete(context.Background(), key)
	}

	manager := lifecycle.NewManager(lister, archive, del)
	manager.RegisterPolicy(lifecycle.Policy{
		Selector:       map[string]string{},
		HotRetention:   0, // objects arrive already "warm" (post-archival)
		WarmRetention:  0,
		TotalRetention: 90 * 24 * time.Hour,
	})
	return manager
}

// segmentMetaFromKey parses "<engine>/raw/<minTime>-<maxTime>-<name>.seg"
// back into a SegmentMeta. Falls back to skipping unparseable keys rather
// than guessing.
func segmentMetaFromKey(engine, key string) (lifecycle.SegmentMeta, bool) {
	parts := strings.SplitN(key, "/raw/", 2)
	if len(parts) != 2 {
		return lifecycle.SegmentMeta{}, false
	}
	fields := strings.SplitN(parts[1], "-", 3)
	if len(fields) < 2 {
		return lifecycle.SegmentMeta{}, false
	}
	minTime, err1 := strconv.ParseInt(fields[0], 10, 64)
	maxTime, err2 := strconv.ParseInt(fields[1], 10, 64)
	if err1 != nil || err2 != nil {
		return lifecycle.SegmentMeta{}, false
	}
	return lifecycle.SegmentMeta{
		Path:    key,
		Service: engine,
		MinTime: minTime,
		MaxTime: maxTime,
	}, true
}
