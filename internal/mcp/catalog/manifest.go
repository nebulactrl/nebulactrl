package catalog

import (
	"fmt"
	"sort"
)

// Manifest decides every operationId of the API. It is the union of
// curatedEntries (curated.go), genericEntries (generic.go) and denyEntries
// (deny.go), keyed by operationId; each Entry value is a Curated, Generic or
// Deny (not a pointer) whose Op field equals its key. An operationId that
// appears in two of the source tables panics at start-up; one missing from the
// OpenAPI spec (or present in the spec but missing here) fails
// TestManifestCoversSpec.
var Manifest = buildManifest()

func buildManifest() map[string]Entry {
	m := make(map[string]Entry, len(curatedEntries)+len(genericEntries)+len(denyEntries))
	put := func(op string, e Entry) {
		if prev, dup := m[op]; dup {
			panic(fmt.Sprintf("catalog: operation %q is decided twice (%s and %s)", op, prev.Kind(), e.Kind()))
		}
		m[op] = e
	}
	for op, e := range curatedEntries {
		e.Op = op
		put(op, e)
	}
	for op, e := range genericEntries {
		e.Op = op
		put(op, e)
	}
	for op, e := range denyEntries {
		e.Op = op
		put(op, e)
	}
	for op, e := range meshGenericEntries {
		e.Op = op
		put(op, e)
	}
	for op, e := range meshDenyEntries {
		e.Op = op
		put(op, e)
	}
	for op, e := range replicaGenericEntries {
		e.Op = op
		put(op, e)
	}
	for op, e := range replicaDenyEntries {
		e.Op = op
		put(op, e)
	}
	for op, e := range statusPageGenericEntries {
		e.Op = op
		put(op, e)
	}
	for op, e := range configGenericEntries {
		e.Op = op
		put(op, e)
	}
	for op, e := range configDenyEntries {
		e.Op = op
		put(op, e)
	}
	for op, e := range statusPageDenyEntries {
		e.Op = op
		put(op, e)
	}
	return m
}

// Lookup returns the entry deciding operationID.
func Lookup(operationID string) (Entry, bool) {
	e, ok := Manifest[operationID]
	return e, ok
}

// All returns every entry ordered by operationId.
func All() []Entry {
	ids := make([]string, 0, len(Manifest))
	for id := range Manifest {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Entry, len(ids))
	for i, id := range ids {
		out[i] = Manifest[id]
	}
	return out
}

// CuratedTools returns the curated entries ordered by tool name.
func CuratedTools() []Curated {
	var out []Curated
	for _, e := range Manifest {
		if c, ok := e.(Curated); ok {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LookupTool returns the curated entry named name.
func LookupTool(name string) (Curated, bool) {
	for _, c := range CuratedTools() {
		if c.Name == name {
			return c, true
		}
	}
	return Curated{}, false
}

// Allowed reports whether operationID may be called over MCP (curated or
// generic). Unknown operations are not allowed.
func Allowed(operationID string) bool {
	e, ok := Manifest[operationID]
	return ok && e.Kind() != KindDeny
}

// Counts summarizes the manifest.
type Counts struct {
	Total, Curated, Generic, Denied int
	Native                          int
	// Toolsets counts curated plus generic entries per toolset.
	Toolsets map[Toolset]int
	// CuratedToolsets counts curated entries per toolset.
	CuratedToolsets map[Toolset]int
	// DenyReasons counts denied operations per reason.
	DenyReasons map[DenyReason]int
}

// Count tallies the manifest.
func Count() Counts {
	c := Counts{
		Toolsets:        map[Toolset]int{},
		CuratedToolsets: map[Toolset]int{},
		DenyReasons:     map[DenyReason]int{},
	}
	for _, e := range Manifest {
		c.Total++
		switch t := e.(type) {
		case Curated:
			c.Curated++
			c.Toolsets[t.Toolset]++
			c.CuratedToolsets[t.Toolset]++
			if t.Native {
				c.Native++
			}
		case Generic:
			c.Generic++
			c.Toolsets[t.Toolset]++
		case Deny:
			c.Denied++
			c.DenyReasons[t.Reason]++
		}
	}
	return c
}
