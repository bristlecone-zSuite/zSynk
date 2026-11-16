package vector

import (
	"encoding/json"
	"sort"
)

// Vector is a version vector keyed by device ID.
// A dominates B when A is greater-or-equal in every component and strictly
// greater in at least one. Concurrent updates (neither dominates) are conflicts.
type Vector map[string]uint64

func New() Vector {
	return Vector{}
}

func Clone(v Vector) Vector {
	if v == nil {
		return Vector{}
	}
	out := make(Vector, len(v))
	for k, n := range v {
		out[k] = n
	}
	return out
}

func (v Vector) Get(id string) uint64 {
	if v == nil {
		return 0
	}
	return v[id]
}

func (v Vector) Bump(id string) Vector {
	out := Clone(v)
	out[id] = out[id] + 1
	return out
}

func Merge(a, b Vector) Vector {
	out := Clone(a)
	for k, n := range b {
		if n > out[k] {
			out[k] = n
		}
	}
	return out
}

func Equal(a, b Vector) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	keys := map[string]struct{}{}
	for k := range a {
		keys[k] = struct{}{}
	}
	for k := range b {
		keys[k] = struct{}{}
	}
	for k := range keys {
		if a.Get(k) != b.Get(k) {
			return false
		}
	}
	return true
}

// Dominates reports whether a is a strict descendant of b.
func Dominates(a, b Vector) bool {
	if Equal(a, b) {
		return false
	}
	keys := map[string]struct{}{}
	for k := range a {
		keys[k] = struct{}{}
	}
	for k := range b {
		keys[k] = struct{}{}
	}
	for k := range keys {
		if a.Get(k) < b.Get(k) {
			return false
		}
	}
	return true
}

func Concurrent(a, b Vector) bool {
	if Equal(a, b) {
		return false
	}
	return !Dominates(a, b) && !Dominates(b, a)
}

func (v Vector) MarshalJSON() ([]byte, error) {
	keys := make([]string, 0, len(v))
	for k := range v {
		if v[k] == 0 {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	m := make(map[string]uint64, len(keys))
	for _, k := range keys {
		m[k] = v[k]
	}
	return json.Marshal(m)
}

func (v *Vector) UnmarshalJSON(data []byte) error {
	var m map[string]uint64
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	*v = Vector(m)
	if *v == nil {
		*v = Vector{}
	}
	return nil
}
