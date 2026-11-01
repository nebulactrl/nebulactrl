package deployments

import (
	"fmt"
	"sort"
	"strings"
)

// maxListedKeys bounds the keys a plan warning names.
const maxListedKeys = 8

// emptySuggestedWarnings reports the variables that were added from the
// repository's .env.example (source "suggested") and still have no value: the
// app will start without them. A warning, never a block: an empty value is
// often right (an optional key).
func emptySuggestedWarnings(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	listed := sorted
	more := ""
	if len(listed) > maxListedKeys {
		more = fmt.Sprintf(" and %d more", len(listed)-maxListedKeys)
		listed = listed[:maxListedKeys]
	}
	noun, verb := "variables", "have"
	if len(keys) == 1 {
		noun, verb = "variable", "has"
	}
	return []string{fmt.Sprintf("%d %s from .env.example %s no value: %s%s. Set them in the Variables tab, or deploy if they are optional.", len(keys), noun, verb, strings.Join(listed, ", "), more)}
}
