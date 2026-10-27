package profiling

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"sort"
)

// FlameNode is one node of a reconstructed call tree ("flamegraph"),
// built by decoding a pprof profile's samples and location/function
// tables. SelfNS is time attributed directly to this frame (i.e. it was
// the leaf of some sample stacks); TotalNS is SelfNS plus all
// descendants' TotalNS.
type FlameNode struct {
	Name     string
	SelfNS   int64
	TotalNS  int64
	Children []*FlameNode
}

// pprofFunction mirrors profile.proto's Function message (only the fields
// we need).
type pprofFunction struct {
	id   uint64
	name int64 // string table index
}

// pprofLocation mirrors profile.proto's Location message: an address plus
// zero or more inlined Line entries, each referencing a Function.
type pprofLocation struct {
	id          uint64
	functionIDs []uint64
}

// pprofSample mirrors profile.proto's Sample message.
type pprofSample struct {
	locationIDs []uint64 // leaf-first
	values      []int64
}

// decodedProfile holds the subset of a pprof Profile message this package
// understands.
type decodedProfile struct {
	stringTable []string
	sampleTypes []string // "type/unit" labels, informational
	samples     []pprofSample
	locations   map[uint64]pprofLocation
	functions   map[uint64]pprofFunction
	period      int64
}

// decodePprof gzip-decompresses and protobuf-parses raw pprof bytes (as
// produced by runtime/pprof.StartCPUProfile/StopCPUProfile) into a
// decodedProfile.
func decodePprof(data []byte) (*decodedProfile, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("profiling: gzip open: %w", err)
	}
	defer gz.Close()
	raw, err := io.ReadAll(gz)
	if err != nil {
		return nil, fmt.Errorf("profiling: gzip read: %w", err)
	}

	dp := &decodedProfile{
		locations: make(map[uint64]pprofLocation),
		functions: make(map[uint64]pprofFunction),
	}

	// Top-level Profile message fields:
	//   1 sample_type (ValueType, repeated)
	//   2 sample (Sample, repeated)
	//   3 mapping (Mapping, repeated) — ignored
	//   4 location (Location, repeated)
	//   5 function (Function, repeated)
	//   6 string_table (string, repeated)
	//   12 period (int64)
	var sampleTypeMsgs [][]byte
	err = parseFields(raw, func(f pbField) error {
		switch f.Num {
		case 1:
			sampleTypeMsgs = append(sampleTypeMsgs, f.Bytes)
		case 2:
			s, err := decodeSample(f.Bytes)
			if err != nil {
				return err
			}
			dp.samples = append(dp.samples, s)
		case 4:
			loc, err := decodeLocation(f.Bytes)
			if err != nil {
				return err
			}
			dp.locations[loc.id] = loc
		case 5:
			fn, err := decodeFunction(f.Bytes)
			if err != nil {
				return err
			}
			dp.functions[fn.id] = fn
		case 6:
			dp.stringTable = append(dp.stringTable, string(f.Bytes))
		case 12:
			dp.period = int64(f.Value)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("profiling: parse profile message: %w", err)
	}

	for _, m := range sampleTypeMsgs {
		var typeIdx, unitIdx int64
		_ = parseFields(m, func(f pbField) error {
			switch f.Num {
			case 1:
				typeIdx = int64(f.Value)
			case 2:
				unitIdx = int64(f.Value)
			}
			return nil
		})
		dp.sampleTypes = append(dp.sampleTypes, dp.str(typeIdx)+"/"+dp.str(unitIdx))
	}

	return dp, nil
}

func (dp *decodedProfile) str(idx int64) string {
	if idx < 0 || int(idx) >= len(dp.stringTable) {
		return ""
	}
	return dp.stringTable[idx]
}

func decodeSample(buf []byte) (pprofSample, error) {
	var s pprofSample
	err := parseFields(buf, func(f pbField) error {
		switch f.Num {
		case 1: // location_id, packed repeated uint64
			ids, err := decodePackedVarints(f.Bytes)
			if err != nil {
				return err
			}
			s.locationIDs = append(s.locationIDs, ids...)
		case 2: // value, packed repeated int64
			vals, err := decodePackedVarints(f.Bytes)
			if err != nil {
				return err
			}
			for _, v := range vals {
				s.values = append(s.values, int64(v))
			}
		}
		return nil
	})
	return s, err
}

func decodeLocation(buf []byte) (pprofLocation, error) {
	var loc pprofLocation
	err := parseFields(buf, func(f pbField) error {
		switch f.Num {
		case 1: // id
			loc.id = f.Value
		case 4: // line (repeated Line message: {function_id, line})
			var funcID uint64
			_ = parseFields(f.Bytes, func(lf pbField) error {
				if lf.Num == 1 {
					funcID = lf.Value
				}
				return nil
			})
			loc.functionIDs = append(loc.functionIDs, funcID)
		}
		return nil
	})
	return loc, err
}

func decodeFunction(buf []byte) (pprofFunction, error) {
	var fn pprofFunction
	err := parseFields(buf, func(f pbField) error {
		switch f.Num {
		case 1: // id
			fn.id = f.Value
		case 2: // name (string table index)
			fn.name = int64(f.Value)
		}
		return nil
	})
	return fn, err
}

// funcNameForLocation resolves a location ID to a display name, using its
// (possibly multiple, if inlined) function references; we use the
// outermost (first) line entry as pprof's own tools typically do for a
// single-line summary.
func (dp *decodedProfile) funcNameForLocation(locID uint64) string {
	loc, ok := dp.locations[locID]
	if !ok || len(loc.functionIDs) == 0 {
		return fmt.Sprintf("0x%x", locID)
	}
	fn, ok := dp.functions[loc.functionIDs[0]]
	if !ok {
		return fmt.Sprintf("loc-%d", locID)
	}
	name := dp.str(fn.name)
	if name == "" {
		return fmt.Sprintf("func-%d", fn.id)
	}
	return name
}

// BuildFlameGraph decodes raw pprof-format bytes (gzip'd protobuf, as
// produced by runtime/pprof) into a call tree rooted at a synthetic
// "root" node. valueIndex selects which Sample.value slot to use as the
// duration (for Go CPU profiles this is typically index 1, "cpu
// nanoseconds"; index 0 is the raw sample count). If the profile has
// fewer value slots than valueIndex, index 0 is used instead.
func BuildFlameGraph(data []byte, valueIndex int) (*FlameNode, error) {
	dp, err := decodePprof(data)
	if err != nil {
		return nil, err
	}

	root := &FlameNode{Name: "root"}
	childIndex := map[*FlameNode]map[string]*FlameNode{root: {}}

	for _, sample := range dp.samples {
		if len(sample.values) == 0 {
			continue
		}
		vi := valueIndex
		if vi < 0 || vi >= len(sample.values) {
			vi = 0
		}
		val := sample.values[vi]

		// sample.locationIDs is leaf-first; walk root->leaf by iterating
		// in reverse.
		cur := root
		for i := len(sample.locationIDs) - 1; i >= 0; i-- {
			name := dp.funcNameForLocation(sample.locationIDs[i])
			children, ok := childIndex[cur]
			if !ok {
				children = map[string]*FlameNode{}
				childIndex[cur] = children
			}
			next, ok := children[name]
			if !ok {
				next = &FlameNode{Name: name}
				children[name] = next
				cur.Children = append(cur.Children, next)
				childIndex[next] = map[string]*FlameNode{}
			}
			next.TotalNS += val
			cur = next
		}
		// cur is now the leaf frame for this sample; attribute self time.
		if len(sample.locationIDs) > 0 {
			cur.SelfNS += val
		} else {
			root.SelfNS += val
		}
		root.TotalNS += val
	}

	sortFlameChildren(root)
	return root, nil
}

// sortFlameChildren orders children by descending TotalNS for stable,
// human-friendly output (largest contributors first), recursively.
func sortFlameChildren(n *FlameNode) {
	sort.Slice(n.Children, func(i, j int) bool {
		if n.Children[i].TotalNS != n.Children[j].TotalNS {
			return n.Children[i].TotalNS > n.Children[j].TotalNS
		}
		return n.Children[i].Name < n.Children[j].Name
	})
	for _, c := range n.Children {
		sortFlameChildren(c)
	}
}
