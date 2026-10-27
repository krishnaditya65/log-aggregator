package vectorsearch

import "sync"

// SemanticIndex ties an Embedder and an HNSW index together into a simple
// text-in/text-out semantic search API, plus a document store so results
// can carry back their original text.
type SemanticIndex struct {
	embedder Embedder
	index    *HNSW

	mu   sync.RWMutex
	docs map[string]string // id -> original text
}

// NewSemanticIndex builds a SemanticIndex using the given embedder and
// HNSW config.
func NewSemanticIndex(embedder Embedder, cfg Config) *SemanticIndex {
	return &SemanticIndex{
		embedder: embedder,
		index:    NewHNSW(cfg),
		docs:     make(map[string]string),
	}
}

// Index embeds and inserts a document under the given ID, retaining its
// original text for later retrieval.
func (s *SemanticIndex) Index(id, text string) {
	vec := s.embedder.Embed(text)
	s.index.Insert(id, vec)
	s.mu.Lock()
	s.docs[id] = text
	s.mu.Unlock()
}

// SemanticSearch embeds the query and returns the k nearest indexed
// documents by cosine similarity.
func (s *SemanticIndex) SemanticSearch(query string, k int) []Result {
	vec := s.embedder.Embed(query)
	return s.index.Search(vec, k)
}

// Text returns the original text indexed under id, if present.
func (s *SemanticIndex) Text(id string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.docs[id]
	return t, ok
}

// Len returns the number of indexed documents.
func (s *SemanticIndex) Len() int {
	return s.index.Len()
}
