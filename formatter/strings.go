package formatter

import (
	"strings"
)

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

func viewString(s string, begin, end int) string {
	n := len(s)
	if n == 0 || begin < 0 || begin > end {
		return ""
	}

	if begin >= n {
		return ""
	}

	if end > n {
		end = n
	}

	return s[begin:end]
}

func viewStringAt(s string, pos int) rune {
	if pos < 0 || pos >= len(s) {
		return rune(0)
	}

	at := viewString(s, pos, pos+1)
	if at == "" {
		return rune(0)
	}

	return []rune(at)[0]
}

// indexFunc returns the byte index into s of the first Unicode code point
// satisfying f(c) == truth, or -1 if none do.
func indexFunc(s string, f func(rune) bool, truth bool) int {
	for i, r := range s {
		if f(r) == truth {
			return i
		}
	}

	return -1
}
