package core

import "sort"

// MemKV is an in-memory KV, used by tests and by the web app's demo chain.
type MemKV struct{ M map[string][]byte }

// NewMemKV returns an empty MemKV.
func NewMemKV() *MemKV { return &MemKV{M: map[string][]byte{}} }

func (m *MemKV) Get(k string) ([]byte, bool) { v, ok := m.M[k]; return v, ok }
func (m *MemKV) Set(k string, v []byte)      { m.M[k] = append([]byte(nil), v...) }
func (m *MemKV) Del(k string)                { delete(m.M, k) }

// Keys returns keys with the prefix, sorted.
func (m *MemKV) Keys(prefix string) []string {
	out := []string{}
	for k := range m.M {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
