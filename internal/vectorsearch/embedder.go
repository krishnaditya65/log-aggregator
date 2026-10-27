package vectorsearch

import (
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// Embedder turns text into a fixed-size vector suitable for cosine
// similarity comparison / HNSW indexing.
type Embedder interface {
	Embed(text string) []float32
}

// DefaultDims is the default embedding dimensionality used by
// NewHashingEmbedder when dims<=0 is passed.
const DefaultDims = 128

// HashingEmbedder is a deterministic, dependency-free text embedder using
// the "hashing trick": each token is hashed into one of `dims` buckets
// (with a second hash selecting a +1/-1 sign, reducing systematic bias
// from hash collisions), weighted by TF-IDF, and the resulting vector is
// L2-normalized. It requires no model weights or network access.
//
// This is a bag-of-words/lexical embedding, not a learned semantic one:
// it has no notion of synonyms. It does reliably place lexically similar
// text (shared vocabulary) closer together than unrelated text under
// cosine similarity, which is the property this package's tests rely on.
type HashingEmbedder struct {
	dims int
	// idf maps a token to its inverse-document-frequency weight, fit from
	// an optional corpus at construction time. Tokens never seen in the
	// fitting corpus fall back to defaultIDF.
	idf        map[string]float64
	defaultIDF float64
}

// NewHashingEmbedder builds a HashingEmbedder with the given output
// dimensionality (defaults to DefaultDims if <= 0). If corpus is
// non-empty, per-token IDF weights are fit from it (classic
// log(N/(1+df)) smoothing); otherwise every token gets a uniform weight,
// which degrades gracefully to plain TF weighting.
func NewHashingEmbedder(dims int, corpus []string) *HashingEmbedder {
	if dims <= 0 {
		dims = DefaultDims
	}
	e := &HashingEmbedder{dims: dims, idf: map[string]float64{}, defaultIDF: 1.0}
	if len(corpus) == 0 {
		return e
	}

	df := map[string]int{}
	for _, doc := range corpus {
		seen := map[string]bool{}
		for _, tok := range tokenize(doc) {
			if !seen[tok] {
				seen[tok] = true
				df[tok]++
			}
		}
	}
	n := float64(len(corpus))
	for tok, count := range df {
		e.idf[tok] = math.Log(1+n/float64(1+count)) + 1e-9
	}
	return e
}

// Embed implements Embedder.
func (e *HashingEmbedder) Embed(text string) []float32 {
	tokens := tokenize(text)
	vec := make([]float64, e.dims)

	tf := map[string]int{}
	for _, tok := range tokens {
		tf[tok]++
	}

	for tok, count := range tf {
		weight := float64(count) * e.idfFor(tok)
		bucket, sign := hashToken(tok, e.dims)
		vec[bucket] += sign * weight
	}

	// Also hash adjacent-token bigrams to capture a little word order /
	// phrase signal beyond pure bag-of-words.
	for i := 0; i+1 < len(tokens); i++ {
		bigram := tokens[i] + "_" + tokens[i+1]
		weight := e.idfFor(bigram)
		bucket, sign := hashToken(bigram, e.dims)
		vec[bucket] += sign * weight * 0.5 // down-weight vs unigrams
	}

	out := make([]float32, e.dims)
	norm := l2Norm(vec)
	if norm == 0 {
		return out
	}
	for i, v := range vec {
		out[i] = float32(v / norm)
	}
	return out
}

func (e *HashingEmbedder) idfFor(tok string) float64 {
	if w, ok := e.idf[tok]; ok {
		return w
	}
	return e.defaultIDF
}

// tokenize lowercases and splits on runs of non-letter/non-digit
// characters, dropping empty tokens.
func tokenize(text string) []string {
	var toks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return toks
}

// hashToken deterministically maps a token to a bucket index in [0,dims)
// and a +1/-1 sign, using two independent FNV-1a hashes (seeded
// differently) so bucket and sign are not correlated.
func hashToken(tok string, dims int) (int, float64) {
	h1 := fnv.New32a()
	h1.Write([]byte(tok))
	bucket := int(h1.Sum32() % uint32(dims))

	h2 := fnv.New32a()
	h2.Write([]byte("sign:" + tok))
	sign := 1.0
	if h2.Sum32()%2 == 0 {
		sign = -1.0
	}
	return bucket, sign
}

func l2Norm(v []float64) float64 {
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	return math.Sqrt(sum)
}

// CosineSimilarity computes cosine similarity between two equal-length
// vectors. Returns 0 if either vector is all-zero or lengths mismatch.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		fa, fb := float64(a[i]), float64(b[i])
		dot += fa * fb
		na += fa * fa
		nb += fb * fb
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}
