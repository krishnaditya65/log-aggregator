package tsdb

import (
	"encoding/gob"
	"os"
	"sync"
)

// LabelIndex is an in-memory inverted index from label name=value pairs to
// the set of series IDs carrying that pair, plus the reverse mapping from
// series ID to its full label set. It is persisted to disk (via gob) so it
// can be rebuilt without rescanning every segment. It is the structure
// Query() consults first to resolve label matchers into a candidate series
// list before touching any segment data.
type LabelIndex struct {
	mu sync.RWMutex
	// inverted[name][value] = set of series IDs with labels[name]==value.
	inverted map[string]map[string]map[SeriesID]struct{}
	// labels[id] = the full label set for that series.
	labels map[SeriesID]map[string]string
}

// NewLabelIndex creates an empty index.
func NewLabelIndex() *LabelIndex {
	return &LabelIndex{
		inverted: make(map[string]map[string]map[SeriesID]struct{}),
		labels:   make(map[SeriesID]map[string]string),
	}
}

// Add registers a series' labels in the index. It is idempotent.
func (idx *LabelIndex) Add(id SeriesID, labels map[string]string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if _, exists := idx.labels[id]; !exists {
		cpy := make(map[string]string, len(labels))
		for k, v := range labels {
			cpy[k] = v
		}
		idx.labels[id] = cpy
	}
	for name, value := range labels {
		vals, ok := idx.inverted[name]
		if !ok {
			vals = make(map[string]map[SeriesID]struct{})
			idx.inverted[name] = vals
		}
		set, ok := vals[value]
		if !ok {
			set = make(map[SeriesID]struct{})
			vals[value] = set
		}
		set[id] = struct{}{}
	}
}

// Remove drops a series from the index entirely (used by compaction /
// tombstone / expiry paths).
func (idx *LabelIndex) Remove(id SeriesID) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	labels, ok := idx.labels[id]
	if !ok {
		return
	}
	delete(idx.labels, id)
	for name, value := range labels {
		if vals, ok := idx.inverted[name]; ok {
			if set, ok := vals[value]; ok {
				delete(set, id)
				if len(set) == 0 {
					delete(vals, value)
				}
			}
			if len(vals) == 0 {
				delete(idx.inverted, name)
			}
		}
	}
}

// Labels returns the stored label set for a series ID, if known.
func (idx *LabelIndex) Labels(id SeriesID) (map[string]string, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	l, ok := idx.labels[id]
	if !ok {
		return nil, false
	}
	cpy := make(map[string]string, len(l))
	for k, v := range l {
		cpy[k] = v
	}
	return cpy, true
}

// Resolve returns the set of series IDs that satisfy all given matchers.
// Exact matchers (no leading "~") are resolved directly via the inverted
// index; regex matchers ("~pattern") require scanning the known values for
// that label name. An empty matcher set resolves to every known series.
func (idx *LabelIndex) Resolve(matchers map[string]string) []SeriesID {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if len(matchers) == 0 {
		out := make([]SeriesID, 0, len(idx.labels))
		for id := range idx.labels {
			out = append(out, id)
		}
		return out
	}

	var candidate map[SeriesID]struct{}
	for name, want := range matchers {
		matchSet := make(map[SeriesID]struct{})
		vals := idx.inverted[name]
		if len(want) > 0 && want[0] == '~' {
			re, err := compileCached(want[1:])
			if err == nil {
				for value, set := range vals {
					if re.MatchString(value) {
						for id := range set {
							matchSet[id] = struct{}{}
						}
					}
				}
			}
		} else if set, ok := vals[want]; ok {
			for id := range set {
				matchSet[id] = struct{}{}
			}
		}

		if candidate == nil {
			candidate = matchSet
		} else {
			// intersect
			for id := range candidate {
				if _, ok := matchSet[id]; !ok {
					delete(candidate, id)
				}
			}
		}
		if len(candidate) == 0 {
			return nil
		}
	}

	out := make([]SeriesID, 0, len(candidate))
	for id := range candidate {
		out = append(out, id)
	}
	return out
}

// Cardinality returns the number of distinct values observed for a label
// name.
func (idx *LabelIndex) Cardinality(name string) int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.inverted[name])
}

// gobIndex is the on-disk representation used for persistence.
type gobIndex struct {
	Labels map[SeriesID]map[string]string
}

// Save persists the index (series -> labels; the inverted structure is
// cheaply rebuilt from this on Load) to path using gob encoding.
func (idx *LabelIndex) Save(path string) error {
	idx.mu.RLock()
	snapshot := make(map[SeriesID]map[string]string, len(idx.labels))
	for id, l := range idx.labels {
		cpy := make(map[string]string, len(l))
		for k, v := range l {
			cpy[k] = v
		}
		snapshot[id] = cpy
	}
	idx.mu.RUnlock()

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return gob.NewEncoder(f).Encode(gobIndex{Labels: snapshot})
}

// LoadLabelIndex reconstructs an index previously written by Save. A
// missing file is not an error; it yields an empty index.
func LoadLabelIndex(path string) (*LabelIndex, error) {
	idx := NewLabelIndex()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return idx, nil
		}
		return nil, err
	}
	defer f.Close()

	var g gobIndex
	if err := gob.NewDecoder(f).Decode(&g); err != nil {
		return nil, err
	}
	for id, labels := range g.Labels {
		idx.Add(id, labels)
	}
	return idx, nil
}
