package coldstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"time"
)

// Archiver encodes batches of rows into the columnar archive format and
// uploads them to a Blobstore, and answers predicate queries by listing
// candidate objects, downloading only the likely matches, and applying
// row-group skipping on each.
type Archiver struct {
	Store   Blobstore
	Service string // used in the key convention: service/YYYY/MM/DD/segment-min-max.col

	// TimeField is the row key consulted to determine each row's
	// timestamp (unix seconds) for computing segment min/max and the
	// date used in the key. Defaults to "timestamp" if empty.
	TimeField string
}

func (a *Archiver) timeField() string {
	if a.TimeField != "" {
		return a.TimeField
	}
	return "timestamp"
}

// rowTime extracts a unix-seconds timestamp from a row, trying a few
// common representations.
func rowTime(row map[string]any, field string) (int64, bool) {
	v, ok := row[field]
	if !ok {
		return 0, false
	}
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case float64:
		return int64(t), true
	case string:
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return n, true
		}
		if ts, err := time.Parse(time.RFC3339, t); err == nil {
			return ts.Unix(), true
		}
	}
	return 0, false
}

// segmentKey builds the "service/2026/01/15/segment-<min>-<max>.col" key
// for a batch spanning [minTime, maxTime], dated off minTime.
func segmentKey(service string, minTime, maxTime int64) string {
	t := time.Unix(minTime, 0).UTC()
	return fmt.Sprintf("%s/%04d/%02d/%02d/segment-%d-%d.col",
		service, t.Year(), int(t.Month()), t.Day(), minTime, maxTime)
}

// ArchiveSegment encodes rows into the columnar format and uploads the
// result to the blobstore under the service/date/segment key convention.
// path identifies the source hot-tier segment being archived (used only
// for error messages/logging); the rows themselves are supplied directly
// so the archiver never needs to know the hot-tier's on-disk layout.
func (a *Archiver) ArchiveSegment(ctx context.Context, path string, rows []map[string]any) error {
	if len(rows) == 0 {
		return fmt.Errorf("coldstore: no rows to archive for %s", path)
	}

	tmp, err := os.CreateTemp("", "coldstore-archive-*.col")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	w, err := NewWriter(tmpPath)
	if err != nil {
		return err
	}
	if err := w.WriteBatch(rows); err != nil {
		w.Close()
		return err
	}
	if _, err := w.Close(); err != nil {
		return err
	}

	var minTime, maxTime int64
	haveTime := false
	tf := a.timeField()
	for _, r := range rows {
		if t, ok := rowTime(r, tf); ok {
			if !haveTime || t < minTime {
				minTime = t
			}
			if !haveTime || t > maxTime {
				maxTime = t
			}
			haveTime = true
		}
	}
	if !haveTime {
		now := time.Now().Unix()
		minTime, maxTime = now, now
	}

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return err
	}
	key := segmentKey(a.Service, minTime, maxTime)
	if err := a.Store.Put(ctx, key, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("coldstore: upload %s (from %s): %w", key, path, err)
	}
	return nil
}

var segmentKeyRe = regexp.MustCompile(`segment-(-?\d+)-(-?\d+)\.col$`)

// QueryPushdown lists archived objects under prefix, uses the min/max
// embedded in each object's key to skip objects that cannot overlap any
// predicate on the archiver's time field, downloads the remaining
// candidates, and applies row-group-level predicate pushdown within
// each.
func (a *Archiver) QueryPushdown(ctx context.Context, prefix string, predicates []Predicate) ([]map[string]any, error) {
	keys, err := a.Store.List(ctx, prefix)
	if err != nil {
		return nil, err
	}

	tf := a.timeField()
	var timeLo, timeHi *float64
	for _, p := range predicates {
		if p.Column != tf {
			continue
		}
		v, ok := toFloat(p.Value)
		if !ok {
			continue
		}
		switch p.Op {
		case "eq":
			lo, hi := v, v
			timeLo, timeHi = &lo, &hi
		case "gt", "gte":
			lo := v
			timeLo = &lo
		case "lt", "lte":
			hi := v
			timeHi = &hi
		}
	}

	var results []map[string]any
	for _, key := range keys {
		m := segmentKeyRe.FindStringSubmatch(key)
		if m != nil {
			segMin, _ := strconv.ParseInt(m[1], 10, 64)
			segMax, _ := strconv.ParseInt(m[2], 10, 64)
			if timeHi != nil && float64(segMin) > *timeHi {
				continue // segment starts after the query window ends
			}
			if timeLo != nil && float64(segMax) < *timeLo {
				continue // segment ends before the query window starts
			}
		}

		rows, err := a.downloadAndFilter(ctx, key, predicates)
		if err != nil {
			return nil, err
		}
		results = append(results, rows...)
	}
	return results, nil
}

func (a *Archiver) downloadAndFilter(ctx context.Context, key string, predicates []Predicate) ([]map[string]any, error) {
	rc, err := a.Store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	tmp, err := os.CreateTemp("", "coldstore-query-*.col")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, rc); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()

	r, err := Open(tmpPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	return r.Filter(predicates)
}
