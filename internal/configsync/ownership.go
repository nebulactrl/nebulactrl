package configsync

import (
	"encoding/json"
	"sort"
	"strings"
)

// classify is the three-way drift rule for one owned field.
//
//	base     the fingerprint the last apply wrote (hasBase=false: never applied)
//	live     the fingerprint of the live value (present=false: absent live)
//	file     the fingerprint of the value the file wants
//
// It returns the state and, for anything that is not in sync, the plan reason
// (equal to the state, except that adopt and new are reasons only).
func classify(base string, hasBase bool, live string, present bool, file string) (state string) {
	switch {
	case present && live == file:
		return StateInSync
	case !present:
		return StateNew
	case !hasBase:
		return StateAdopt
	case live == base && file != base:
		return StateFileChanged
	case live != base && file == base:
		return StateDrift
	default:
		return StateConflict
	}
}

// stateRank orders states by severity for the worst-of rule.
func stateRank(s string) int {
	switch s {
	case StateConflict:
		return 5
	case StateDrift:
		return 4
	case StateFileChanged:
		return 3
	case StateAdopt:
		return 2
	case StateNew:
		return 1
	}
	return 0
}

// worstState returns the more severe of a and b (in_sync is the least).
func worstState(a, b string) string {
	if stateRank(b) > stateRank(a) {
		return b
	}
	if a == "" {
		return StateInSync
	}
	return a
}

// serviceStateOf is the state shown for a managed service: the worst of its
// fields (conflict > drift > file_changed > adopt > new > in_sync).
func serviceStateOf(fieldStates []string) string {
	worst := StateInSync
	for _, s := range fieldStates {
		worst = worstState(worst, s)
	}
	return worst
}

// ownedJSON encodes an owned map for storage (sorted, so rows are stable).
func ownedJSON(owned map[string]string) []byte {
	if owned == nil {
		owned = map[string]string{}
	}
	b, _ := json.Marshal(owned) // map keys are marshalled sorted
	return b
}

// sortedKeys returns the keys of m in ascending order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Collections whose members the file owns individually and may prune.
var prunablePrefixes = []string{"variables.", "domains.", "volumes.", "networks.", "links.", "tasks.", "backups.", "config.processes."}

// prunable reports whether a field id names a member of a prunable collection
// (a declared secret flag, a domain's Cloudflare settings and the whole uptime
// monitor included: uptime resets to its defaults).
func prunable(id string) bool {
	if id == "uptime" || strings.HasPrefix(id, "uptime.") {
		return true
	}
	for _, p := range prunablePrefixes {
		if strings.HasPrefix(id, p) {
			return !strings.HasSuffix(id, "#secret") && !strings.HasSuffix(id, ".cloudflare")
		}
	}
	return false
}

// releasedFields lists the fields the previous apply owned that the file no
// longer mentions, in id order.
func releasedFields(owned map[string]string, present func(id string) bool) []string {
	var out []string
	for _, id := range sortedKeys(owned) {
		if !present(id) {
			out = append(out, id)
		}
	}
	return out
}
