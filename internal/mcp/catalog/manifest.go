package catalog

import (
	"fmt"
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
