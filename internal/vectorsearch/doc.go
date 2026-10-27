// Package vectorsearch implements semantic-ish text search entirely with
// the standard library: a deterministic local embedding (hashing-trick,
// TF-IDF-weighted bag-of-words folded into a fixed-size vector) and a
// from-scratch HNSW (Hierarchical Navigable Small World) approximate
// nearest-neighbor index.
//
// Neither piece calls out to an external model or service: the embedder
// is a classic "hashing trick" (see Weinberger et al., "Feature Hashing
// for Large Scale Multitask Learning") combined with simple TF-IDF term
// weighting over a corpus seen so far, so it is fully deterministic and
// requires no network access or model weights. It is not a learned
// semantic embedding (it has no notion of synonyms/word meaning beyond
// shared tokens/n-grams), but it does produce vectors where
// lexically-similar sentences score higher cosine similarity than
// unrelated ones, which is what the tests in this package verify.
//
// The HNSW index (hnsw.go) implements the real multi-layer graph
// algorithm from Malkov & Yashunin, "Efficient and robust approximate
// nearest neighbor search using Hierarchical Navigable Small World
// graphs": probabilistic layer assignment, greedy layered search
// descending from the top layer, and neighbor-heuristic pruning at
// insertion time. It is simplified/unoptimized (no SIMD, no product
// quantization, no parallel construction) but is a genuine HNSW, not a
// brute-force scan dressed up as one — see hnsw_test.go for a recall
// benchmark against brute force.
package vectorsearch
