package vectorsearch

import (
	"container/heap"
	"math"
	"math/rand"
	"sort"
	"sync"
)

// Result is one hit returned by a search: an indexed ID and its
// similarity score (cosine similarity; higher is more similar, range
// roughly [-1, 1]).
type Result struct {
	ID    string
	Score float32
}

// hnswNode is one point in the index, present on layers [0, level].
type hnswNode struct {
	id    string
	vec   []float32
	level int
	// neighbors[l] holds this node's neighbor IDs on layer l.
	neighbors [][]string
}

// HNSW is a from-scratch Hierarchical Navigable Small World approximate
// nearest-neighbor index (Malkov & Yashunin, 2016/2018). It supports
// online insertion and greedy multi-layer search. It is simplified
// (no deletion, no parallel construction, no SIMD distance kernels) but
// implements the real algorithm: probabilistic layer assignment,
// layer-local greedy candidate expansion bounded by efConstruction /
// efSearch, and degree-bounded neighbor lists (M / Mmax0) — not a
// brute-force scan.
type HNSW struct {
	mu sync.RWMutex

	dim            int
	m              int // max neighbors per node per layer (layers >= 1)
	mMax0          int // max neighbors per node at layer 0 (conventionally 2*M)
	efConstruction int
	efSearch       int
	levelMult      float64

	rng *rand.Rand

	nodes      map[string]*hnswNode
	entryPoint *hnswNode
	maxLevel   int
}

// Config controls HNSW construction parameters.
type Config struct {
	Dim            int
	M              int // default 16
	EfConstruction int // default 200
	EfSearch       int // default 64
	Seed           int64
}

// NewHNSW builds an empty index. Zero-valued fields in cfg are replaced
// with sane defaults (M=16, EfConstruction=200, EfSearch=64).
func NewHNSW(cfg Config) *HNSW {
	if cfg.M <= 0 {
		cfg.M = 16
	}
	if cfg.EfConstruction <= 0 {
		cfg.EfConstruction = 200
	}
	if cfg.EfSearch <= 0 {
		cfg.EfSearch = 64
	}
	seed := cfg.Seed
	if seed == 0 {
		seed = 42
	}
	return &HNSW{
		dim:            cfg.Dim,
		m:              cfg.M,
		mMax0:          cfg.M * 2,
		efConstruction: cfg.EfConstruction,
		efSearch:       cfg.EfSearch,
		levelMult:      1.0 / math.Log(float64(cfg.M)),
		rng:            rand.New(rand.NewSource(seed)),
		nodes:          make(map[string]*hnswNode),
		maxLevel:       -1,
	}
}

// SetEfSearch adjusts the search-time candidate list size (accuracy vs.
// speed trade-off) without rebuilding the index.
func (h *HNSW) SetEfSearch(ef int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ef > 0 {
		h.efSearch = ef
	}
}

// randomLevel draws a layer for a new node from an exponential
// distribution, per the HNSW paper: level = floor(-ln(U(0,1)) * mL).
func (h *HNSW) randomLevel() int {
	u := h.rng.Float64()
	for u == 0 {
		u = h.rng.Float64()
	}
	return int(math.Floor(-math.Log(u) * h.levelMult))
}

func distance(a, b []float32) float64 {
	// Cosine distance = 1 - cosine similarity; lower is closer.
	return float64(1 - CosineSimilarity(a, b))
}

// Insert adds (or replaces) a vector under the given ID.
func (h *HNSW) Insert(id string, vec []float32) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if existing, ok := h.nodes[id]; ok {
		// Simplification: replace in place by removing all edges to it
		// and re-inserting fresh (deletion is out of scope for a
		// "simplified" HNSW per the package doc; this keeps Insert
		// idempotent-ish for callers that re-index a document).
		h.removeNodeLocked(existing)
	}

	level := h.randomLevel()
	newNode := &hnswNode{
		id:        id,
		vec:       vec,
		level:     level,
		neighbors: make([][]string, level+1),
	}
	h.nodes[id] = newNode

	if h.entryPoint == nil {
		h.entryPoint = newNode
		h.maxLevel = level
		return
	}

	ep := h.entryPoint
	curMax := h.maxLevel

	// Phase 1: descend from top layer to level+1 with ef=1 to find a good
	// entry point for the layers we'll actually insert into.
	for lc := curMax; lc > level; lc-- {
		nearest := h.searchLayer(vec, []*hnswNode{ep}, 1, lc)
		if len(nearest) > 0 {
			ep = nearest[0].node
		}
	}

	// Phase 2: for each layer from min(level,curMax) down to 0, find
	// efConstruction candidates and connect.
	for lc := min(level, curMax); lc >= 0; lc-- {
		candidates := h.searchLayer(vec, []*hnswNode{ep}, h.efConstruction, lc)
		selected := selectNeighbors(candidates, h.m)

		newNode.neighbors[lc] = make([]string, 0, len(selected))
		for _, c := range selected {
			newNode.neighbors[lc] = append(newNode.neighbors[lc], c.node.id)
			h.connect(c.node, newNode, lc)
		}
		if len(selected) > 0 {
			ep = selected[0].node
		}
	}

	if level > h.maxLevel {
		h.maxLevel = level
		h.entryPoint = newNode
	}
}

// connect adds a bidirectional edge between a and b at layer lc, pruning
// a's (and b's) neighbor list back down to the layer's max degree if it
// grows too large, keeping the closest neighbors.
func (h *HNSW) connect(a, b *hnswNode, lc int) {
	maxDeg := h.m
	if lc == 0 {
		maxDeg = h.mMax0
	}

	if lc >= len(a.neighbors) {
		grown := make([][]string, lc+1)
		copy(grown, a.neighbors)
		a.neighbors = grown
	}
	a.neighbors[lc] = append(a.neighbors[lc], b.id)
	if len(a.neighbors[lc]) > maxDeg {
		a.neighbors[lc] = h.pruneNeighbors(a, lc, maxDeg)
	}
}

func (h *HNSW) pruneNeighbors(a *hnswNode, lc, maxDeg int) []string {
	type scored struct {
		id   string
		dist float64
	}
	all := make([]scored, 0, len(a.neighbors[lc]))
	for _, nid := range a.neighbors[lc] {
		n, ok := h.nodes[nid]
		if !ok {
			continue
		}
		all = append(all, scored{id: nid, dist: distance(a.vec, n.vec)})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].dist < all[j].dist })
	if len(all) > maxDeg {
		all = all[:maxDeg]
	}
	out := make([]string, len(all))
	for i, s := range all {
		out[i] = s.id
	}
	return out
}

// removeNodeLocked strips a node's edges from its neighbors' lists and
// deletes it from the index, in preparation for re-insertion. Caller
// holds h.mu.
func (h *HNSW) removeNodeLocked(n *hnswNode) {
	for lc, neighbors := range n.neighbors {
		for _, nid := range neighbors {
			other, ok := h.nodes[nid]
			if !ok || lc >= len(other.neighbors) {
				continue
			}
			filtered := other.neighbors[lc][:0]
			for _, id := range other.neighbors[lc] {
				if id != n.id {
					filtered = append(filtered, id)
				}
			}
			other.neighbors[lc] = filtered
		}
	}
	delete(h.nodes, n.id)
	if h.entryPoint == n {
		h.entryPoint = nil
		h.maxLevel = -1
		for _, other := range h.nodes {
			if h.entryPoint == nil || other.level > h.maxLevel {
				h.entryPoint = other
				h.maxLevel = other.level
			}
		}
	}
}

// candidateDist pairs a node with its distance to some query, used both
// as search-layer output and as heap elements.
type candidateDist struct {
	node *hnswNode
	dist float64
}

// searchLayer implements the core HNSW layer-local greedy search
// (algorithm 2 in Malkov & Yashunin): explore outward from entryPoints,
// maintaining a bounded candidate frontier (min-heap, closest first) and
// a bounded result set (max-heap, farthest first so it can be trimmed to
// ef), returning up to ef nearest nodes found on layer lc, sorted nearest
// first.
func (h *HNSW) searchLayer(query []float32, entryPoints []*hnswNode, ef int, lc int) []candidateDist {
	visited := make(map[string]bool)
	candidates := &minDistHeap{}
	results := &maxDistHeap{}

	for _, ep := range entryPoints {
		d := distance(query, ep.vec)
		visited[ep.id] = true
		heap.Push(candidates, candidateDist{ep, d})
		heap.Push(results, candidateDist{ep, d})
	}

	for candidates.Len() > 0 {
		c := heap.Pop(candidates).(candidateDist)
		if results.Len() > 0 && c.dist > (*results)[0].dist {
			break
		}
		if c.node.level < lc || lc >= len(c.node.neighbors) {
			continue
		}
		for _, nid := range c.node.neighbors[lc] {
			if visited[nid] {
				continue
			}
			visited[nid] = true
			neighbor, ok := h.nodes[nid]
			if !ok {
				continue
			}
			d := distance(query, neighbor.vec)
			if results.Len() < ef || d < (*results)[0].dist {
				heap.Push(candidates, candidateDist{neighbor, d})
				heap.Push(results, candidateDist{neighbor, d})
				if results.Len() > ef {
					heap.Pop(results)
				}
			}
		}
	}

	out := make([]candidateDist, results.Len())
	copy(out, *results)
	sort.Slice(out, func(i, j int) bool { return out[i].dist < out[j].dist })
	return out
}

// selectNeighbors picks up to m candidates, closest first (simple
// heuristic variant of the paper's neighbor-selection algorithm; the
// candidates passed in are already the ef-nearest from searchLayer).
func selectNeighbors(candidates []candidateDist, m int) []candidateDist {
	if len(candidates) <= m {
		return candidates
	}
	return candidates[:m]
}

// Search returns the k approximate nearest neighbors to vec, by cosine
// similarity, using the standard multi-layer HNSW query algorithm
// (greedy ef=1 descent through upper layers, then an ef=efSearch search
// at layer 0).
func (h *HNSW) Search(vec []float32, k int) []Result {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.entryPoint == nil || k <= 0 {
		return nil
	}

	ep := h.entryPoint
	for lc := h.maxLevel; lc > 0; lc-- {
		nearest := h.searchLayer(vec, []*hnswNode{ep}, 1, lc)
		if len(nearest) > 0 {
			ep = nearest[0].node
		}
	}

	ef := h.efSearch
	if ef < k {
		ef = k
	}
	found := h.searchLayer(vec, []*hnswNode{ep}, ef, 0)

	if len(found) > k {
		found = found[:k]
	}
	out := make([]Result, len(found))
	for i, c := range found {
		out[i] = Result{ID: c.node.id, Score: CosineSimilarity(vec, c.node.vec)}
	}
	return out
}

// Len returns the number of indexed vectors.
func (h *HNSW) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.nodes)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// minDistHeap is a min-heap of candidateDist by distance (closest first),
// used to drive frontier expansion in searchLayer.
type minDistHeap []candidateDist

func (h minDistHeap) Len() int            { return len(h) }
func (h minDistHeap) Less(i, j int) bool  { return h[i].dist < h[j].dist }
func (h minDistHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *minDistHeap) Push(x interface{}) { *h = append(*h, x.(candidateDist)) }
func (h *minDistHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// maxDistHeap is a max-heap of candidateDist by distance (farthest
// first), used to hold the current best-ef result set so the farthest
// member can be evicted in O(log ef) when a closer candidate is found.
type maxDistHeap []candidateDist

func (h maxDistHeap) Len() int            { return len(h) }
func (h maxDistHeap) Less(i, j int) bool  { return h[i].dist > h[j].dist }
func (h maxDistHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *maxDistHeap) Push(x interface{}) { *h = append(*h, x.(candidateDist)) }
func (h *maxDistHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
