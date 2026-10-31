package projectconfig

import (
	"reflect"
	"sort"
)

// isMergeLeaf reports whether the node at rel (a key path relative to a
// service root) replaces as a unit during a merge even though it is a table
// (a merge leaf): a variable entry, a domain's options, a file and a stack
// resource entry.
func isMergeLeaf(rel []string) bool {
	switch len(rel) {
	case 2:
		return rel[0] == "variables" || rel[0] == "files"
	case 3:
		return (rel[0] == "domains" && rel[2] == "options") || (rel[0] == "stack" && rel[1] == "resources")
	}
	return false
}

// provMap maps a path relative to the service root (PathString form) to the
// absolute file key path of the node that supplied the winning value.
type provMap map[string]string

type merger struct {
	prov provMap
}

func deepCopy(n any) any {
	switch t := n.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, v := range t {
			out[k] = deepCopy(v)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, v := range t {
			out[i] = deepCopy(v)
		}
		return out
	}
	return n
}

func appendSeg(p []string, s string) []string {
	out := make([]string, len(p)+1)
	copy(out, p)
	out[len(p)] = s
	return out
}

// take copies n and records its provenance under rel/abs.
func (m *merger) take(n any, rel, abs []string) any {
	m.record(n, rel, abs)
	return deepCopy(n)
}

func (m *merger) record(n any, rel, abs []string) {
	m.prov[PathString(rel...)] = PathString(abs...)
	switch t := n.(type) {
	case map[string]any:
		for k, v := range t {
			m.record(v, appendSeg(rel, k), appendSeg(abs, k))
		}
	case []any:
		for i, v := range t {
			idx := "[" + itoa(i) + "]"
			m.record(v, appendSeg(rel, idx), appendSeg(abs, idx))
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}

// merge merges over onto base. Either side may be nil (absent).
func (m *merger) merge(base, over any, rel, baseAbs, overAbs []string) any {
	if over == nil {
		return m.take(base, rel, baseAbs)
	}
	if base == nil {
		return m.take(over, rel, overAbs)
	}
	bm, bok := base.(map[string]any)
	om, ook := over.(map[string]any)
	if !bok || !ook || isMergeLeaf(rel) {
		return m.take(over, rel, overAbs)
	}
	m.prov[PathString(rel...)] = PathString(baseAbs...)
	out := make(map[string]any, len(bm)+len(om))
	keys := make([]string, 0, len(bm)+len(om))
	for k := range bm {
		keys = append(keys, k)
	}
	for k := range om {
		if _, ok := bm[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		out[k] = m.merge(bm[k], om[k], appendSeg(rel, k), appendSeg(baseAbs, k), appendSeg(overAbs, k))
	}
	return out
}

// lookupTree returns the node at segs of a tree of nested maps.
func lookupTree(n any, segs []string) (any, bool) {
	cur := n
	for _, s := range segs {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[s]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// deleteTree removes the node at segs of a service tree, reporting whether it
// existed. A collection (a map keyed by name: variables, domains, files,
// tasks, …) the removal leaves empty is removed too, so unsetting the last
// member does not leave an empty (owning) collection behind. A table that is
// a section (health_check, scale, …) or a member of a collection (a domain)
// stays, even with no key left: such a table exists in its own right, and
// without that rule an environment could not state "this section/domain is
// present with its defaults" over a base that fills it.
func deleteTree(n map[string]any, segs []string) bool {
	return deleteIn(n, segs, reflect.TypeOf(Service{}))
}

// deleteIn is deleteTree for the table n of Go type t.
func deleteIn(n map[string]any, segs []string, t reflect.Type) bool {
	if len(segs) == 0 {
		return false
	}
	child, ok := n[segs[0]]
	if !ok {
		return false
	}
	if len(segs) == 1 {
		delete(n, segs[0])
		return true
	}
	cm, ok := child.(map[string]any)
	if !ok {
		return false
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	var ct reflect.Type
	switch t.Kind() {
	case reflect.Struct:
		fi, ok := infoOf(t).byKey[segs[0]]
		if !ok {
			return false
		}
		ct = t.Field(fi).Type
	case reflect.Map:
		ct = t.Elem()
	default:
		return false
	}
	if !deleteIn(cm, segs[1:], ct) {
		return false
	}
	for ct.Kind() == reflect.Ptr {
		ct = ct.Elem()
	}
	if len(cm) == 0 && ct.Kind() == reflect.Map {
		delete(n, segs[0])
	}
	return true
}

// unsetPathValid reports whether segs name a key path that exists in the
// Service model: struct keys by their toml name, one dynamic key segment for
// every map, and no segments below scalars or leaves of the model.
func unsetPathValid(segs []string) bool {
	t := reflect.TypeOf(Service{})
	for i := 0; i < len(segs); i++ {
		for t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		switch t.Kind() {
		case reflect.Struct:
			if t == variableType {
				return false
			}
			fi, ok := infoOf(t).byKey[segs[i]]
			if !ok {
				return false
			}
			t = t.Field(fi).Type
		case reflect.Map:
			t = t.Elem()
		default:
			return false
		}
	}
	return true
}
