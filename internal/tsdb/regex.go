package tsdb

import (
	"regexp"
	"sync"
)

// regexCache memoizes compiled regular expressions used by label matchers
// so repeated queries with the same pattern don't recompile it.
var regexCache = struct {
	mu sync.RWMutex
	m  map[string]*regexp.Regexp
}{m: make(map[string]*regexp.Regexp)}

func compileCached(pattern string) (*regexp.Regexp, error) {
	regexCache.mu.RLock()
	re, ok := regexCache.m[pattern]
	regexCache.mu.RUnlock()
	if ok {
		return re, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	regexCache.mu.Lock()
	regexCache.m[pattern] = re
	regexCache.mu.Unlock()
	return re, nil
}
