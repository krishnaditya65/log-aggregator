package vectorsearch

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"testing"
)

func TestHashingEmbedderSimilarityOrdering(t *testing.T) {
	corpus := []string{
		"the database connection timed out after 30 seconds",
		"database connection timeout error on primary node",
		"the cat sat quietly on the warm windowsill",
		"a fluffy cat slept on the sunny windowsill all afternoon",
	}
	e := NewHashingEmbedder(256, corpus)

	query := e.Embed("database connection timeout on the primary node")
	dbRelated := e.Embed("database connection timed out after 30 seconds")
	catRelated := e.Embed("a fluffy cat slept on the sunny windowsill all afternoon")

	simDB := CosineSimilarity(query, dbRelated)
	simCat := CosineSimilarity(query, catRelated)

	if simDB <= simCat {
		t.Fatalf("expected DB-related similarity (%f) > cat-related similarity (%f)", simDB, simCat)
	}
	if simDB <= 0.2 {
		t.Fatalf("expected meaningfully positive similarity for related text, got %f", simDB)
	}
}

func TestHashingEmbedderDeterministic(t *testing.T) {
	e := NewHashingEmbedder(128, nil)
	v1 := e.Embed("service payment-api returned 500 errors")
	v2 := e.Embed("service payment-api returned 500 errors")
	if len(v1) != 128 {
		t.Fatalf("expected dim 128, got %d", len(v1))
	}
	for i := range v1 {
		if v1[i] != v2[i] {
			t.Fatalf("embedding not deterministic at index %d: %f vs %f", i, v1[i], v2[i])
		}
	}
}

func TestHashingEmbedderEmptyText(t *testing.T) {
	e := NewHashingEmbedder(64, nil)
	v := e.Embed("")
	for i, x := range v {
		if x != 0 {
			t.Fatalf("expected zero vector for empty text, got nonzero at %d: %f", i, x)
		}
	}
}

func TestCosineSimilarityBasics(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	c := []float32{0, 1, 0}
	if got := CosineSimilarity(a, b); got < 0.999 {
		t.Fatalf("expected ~1.0 for identical vectors, got %f", got)
	}
	if got := CosineSimilarity(a, c); got > 0.001 || got < -0.001 {
		t.Fatalf("expected ~0.0 for orthogonal vectors, got %f", got)
	}
}

func TestHNSWBasicInsertSearch(t *testing.T) {
	h := NewHNSW(Config{Dim: 8, M: 8, EfConstruction: 50, EfSearch: 20})
	vecs := map[string][]float32{
		"a": {1, 0, 0, 0, 0, 0, 0, 0},
		"b": {0.9, 0.1, 0, 0, 0, 0, 0, 0},
		"c": {0, 0, 0, 0, 0, 0, 0, 1},
	}
	for id, v := range vecs {
		h.Insert(id, v)
	}
	if h.Len() != 3 {
		t.Fatalf("expected 3 nodes, got %d", h.Len())
	}

	results := h.Search([]float32{1, 0, 0, 0, 0, 0, 0, 0}, 2)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != "a" {
		t.Fatalf("expected nearest match 'a', got %q", results[0].ID)
	}
}

// bruteForceSearch computes exact top-k nearest neighbors by cosine
// similarity, used as ground truth for the HNSW recall test.
func bruteForceSearch(vectors map[string][]float32, query []float32, k int) []string {
	type scored struct {
		id  string
		sim float32
	}
	all := make([]scored, 0, len(vectors))
	for id, v := range vectors {
		all = append(all, scored{id, CosineSimilarity(query, v)})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].sim > all[j].sim })
	if len(all) > k {
		all = all[:k]
	}
	out := make([]string, len(all))
	for i, s := range all {
		out[i] = s.id
	}
	return out
}

func randomUnitVector(rng *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	var sumSq float64
	for i := range v {
		x := rng.NormFloat64()
		v[i] = float32(x)
		sumSq += x * x
	}
	norm := float32(1.0)
	if sumSq > 0 {
		norm = float32(1.0 / sqrtApprox(sumSq))
	}
	for i := range v {
		v[i] *= norm
	}
	return v
}

func sqrtApprox(x float64) float64 {
	return math.Sqrt(x)
}

func TestHNSWRecallVsBruteForce(t *testing.T) {
	const (
		dim       = 32
		numPoints = 500
		numTrials = 20
		k         = 1
	)
	rng := rand.New(rand.NewSource(7))

	vectors := make(map[string][]float32, numPoints)
	h := NewHNSW(Config{Dim: dim, M: 16, EfConstruction: 200, EfSearch: 64, Seed: 7})

	ids := make([]string, 0, numPoints)
	for i := 0; i < numPoints; i++ {
		id := fmt.Sprintf("doc-%d", i)
		v := randomUnitVector(rng, dim)
		vectors[id] = v
		h.Insert(id, v)
		ids = append(ids, id)
	}

	hits := 0
	for trial := 0; trial < numTrials; trial++ {
		// Query with a slightly perturbed copy of an existing point so
		// there's a well-defined expected nearest neighbor, similar to
		// realistic "find similar to this" queries.
		baseID := ids[rng.Intn(len(ids))]
		base := vectors[baseID]
		query := make([]float32, dim)
		copy(query, base)
		for i := range query {
			query[i] += float32(rng.NormFloat64()) * 0.01
		}

		want := bruteForceSearch(vectors, query, k)
		got := h.Search(query, k)

		if len(got) > 0 && len(want) > 0 && got[0].ID == want[0] {
			hits++
		}
	}

	recall := float64(hits) / float64(numTrials)
	if recall < 0.9 {
		t.Fatalf("HNSW top-1 recall vs brute force = %.2f (%d/%d), want >= 0.90", recall, hits, numTrials)
	}
}

func TestSemanticSearchEndToEnd(t *testing.T) {
	corpus := []string{
		"payment service timeout connecting to database",
		"checkout API failing with connection reset",
		"weather forecast shows sunny skies tomorrow",
		"recipe for chocolate chip cookies",
	}
	embedder := NewHashingEmbedder(128, corpus)
	idx := NewSemanticIndex(embedder, Config{Dim: 128, M: 8, EfConstruction: 100, EfSearch: 32})

	for i, doc := range corpus {
		idx.Index(fmt.Sprintf("doc-%d", i), doc)
	}

	results := idx.SemanticSearch("database connection timeout in payment", 2)
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	text, ok := idx.Text(results[0].ID)
	if !ok {
		t.Fatalf("expected text for top result %s", results[0].ID)
	}
	if text != corpus[0] && text != corpus[1] {
		t.Fatalf("expected top result to be a service/timeout related doc, got %q", text)
	}
}
