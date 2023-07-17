package formatter

import (
	"strings"
	"unicode/utf8"
)

// StringMapSet is a mapping for case-insensitive keys to case-sensitive
// values.
type StringMapSet map[string]string

func NewStringMapSet(keys ...string) StringMapSet {
	s := make(StringMapSet, len(keys))

	for _, k := range keys {
		s[strings.ToLower(k)] = k
	}

	return s
}

// Get returns the mapping for a key.  If the key is not present,
// returns itself.
func (s StringMapSet) Get(key string) string {
	return s.GetWithFallback(key, key)
}

// Get returns the mapping for a key.  If the key is not present,
// returns the default/fallback value.
func (s StringMapSet) GetWithFallback(key, def string) string {
	if val, ok := s[strings.ToLower(key)]; ok {
		return val
	}

	return def
}

func viewString(s string, begin, end int) string {
	n := len(s)
	if n == 0 {
		return ""
	}

	if begin >= n {
		begin = n - 1
	}

	if end >= n {
		end = n
	}

	return s[begin:end]
}

func viewStringAt(s string, pos int) rune {
	at := viewString(s, pos, pos+1)
	if at == "" {
		return rune(0)
	}

	return []rune(at)[0]
}

// indexFunc returns the index into s of the first Unicode
// code point satisfying f(c) equals truth, or -1 if none do.
func indexFunc(s string, f func(rune) bool, truth bool) int {
	for i, r := range s {
		if f(r) == truth {
			return i
		}
	}

	return -1
}

// lastIndexFunc returns the index into s of the last
// Unicode code point satisfying f(c) equals truth, or -1 if none do.
func lastIndexFunc(s string, f func(rune) bool, truth bool) int {
	for i := len(s); i > 0; {
		r, size := utf8.DecodeLastRuneInString(s[0:i])
		i -= size

		if f(r) == truth {
			return i
		}
	}

	return -1
}
