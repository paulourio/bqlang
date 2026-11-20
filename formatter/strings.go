package formatter

import "strings"

// stringMapSet maps case-insensitive keys to canonical case-sensitive values.
type stringMapSet map[string]string

func newStringMapSet(keys ...string) stringMapSet {
	s := make(stringMapSet, len(keys))

	for _, k := range keys {
		s[strings.ToLower(k)] = k
	}

	return s
}

// Get returns the mapping for a key, or key itself if absent.
func (s stringMapSet) Get(key string) string {
	return s.GetWithFallback(key, key)
}

// GetWithFallback returns the mapping for a key, or def if absent.
func (s stringMapSet) GetWithFallback(key, def string) string {
	if s == nil {
		return def
	}

	if val, ok := s[strings.ToLower(key)]; ok {
		return val
	}

	return def
}
