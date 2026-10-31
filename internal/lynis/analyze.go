package lynis

import (
	"fmt"
	"slices"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Severities of an Item.
const (
	SeverityWarning    = "warning"
	SeveritySuggestion = "suggestion"
)

// Item is one remaining finding with what NebulaCtrl can say about it.
type Item struct {
	TestID   string
	Severity string
	Title    string
	// Detail is the specific value or option the finding is about.
	Detail string
	// FixID is the catalog fix that answers the finding, when there is one;
	// FixState and FixMessage are its state on the host.
	FixID      string
	FixState   string
	FixMessage string
	// Explanation says what the finding means; Manual is what to type;
	// Never says why NebulaCtrl does not change it.
	Explanation string
	Manual      []string
	Never       string
	Link        string
}

// Remaining turns the warnings and suggestions of a report into items,
// matching them to catalog fixes by test ID (and, for KRNL-6000, by sysctl
// key). states are the fix states the agent reported (may be empty).
func Remaining(rep *agentprotocol.HardeningReport, states []agentprotocol.HardeningFixState) []Item {
	if rep == nil {
		return nil
	}
	state := map[string]agentprotocol.HardeningFixState{}
	for _, s := range states {
		state[s.ID] = s
	}
	catalog := Catalog()
	fixFor := func(testID, key string) *Fix {
		for i := range catalog {
			f := &catalog[i]
			if !slices.Contains(f.Tests, testID) {
				continue
			}
			if key != "" && len(f.Keys) > 0 {
				if slices.Contains(f.Keys, key) {
					return f
				}
				continue
			}
			if key == "" && len(f.Keys) > 0 {
				continue // a sysctl fix answers a key, not the whole test
			}
			return f
		}
		return nil
	}
	var items []Item
	expanded := map[string]bool{}
	add := func(severity string, f agentprotocol.HardeningFinding) {
		base := Item{TestID: f.ID, Severity: severity, Title: f.Text, Link: ControlURL(f.ID)}
		g := GuidanceFor(f.ID)
		// Lynis repeats a per-key test once per key (SSH-7408 lists nine times) and
		// the report parser hangs all details on the first finding: the repeats add
		// nothing once the first one is expanded.
		if len(f.Fields) == 0 && expanded[f.ID] {
			return
		}
		// Per-key findings (sysctl values, sshd options) become one item each.
		if len(f.Fields) > 0 && (f.ID == "KRNL-6000" || f.ID == "SSH-7408") {
			for _, d := range f.Fields {
				it := base
				it.Title = fieldTitle(f.ID, d)
				it.Detail = d.Description
				it.Explanation, it.Manual, it.Never = g.Explanation, g.Manual, g.Never
				if kg, ok := KeyGuidanceFor(d.Field); ok {
					it.Explanation, it.Manual, it.Never = kg.Explanation, kg.Manual, kg.Never
				}
				if fx := fixFor(f.ID, d.Field); fx != nil && f.ID == "KRNL-6000" {
					setFix(&it, fx, state)
				}
				if f.ID == "KRNL-6000" && it.Explanation == "" && it.FixID == "" {
					it.Explanation = "Lynis prefers " + d.Preferred + " for this kernel setting. Check what depends on it before changing it."
					// The key and value come from the host's report and end up in a command a
					// person pastes into a root shell: only plain sysctl tokens are quoted into it.
					if want := firstWord(d.Preferred); shellSafe(d.Field) && shellSafe(want) {
						it.Manual = []string{fmt.Sprintf("echo '%s = %s' | sudo tee /etc/sysctl.d/90-%s.conf && sudo sysctl --system", d.Field, want, d.Field)}
					}
				}
				items = append(items, it)
			}
			expanded[f.ID] = true
			return
		}
		it := base
		it.Detail = f.Details
		it.Explanation, it.Manual, it.Never = g.Explanation, g.Manual, g.Never
		if fx := fixFor(f.ID, ""); fx != nil {
			setFix(&it, fx, state)
		}
		if f.Solution != "" && it.Explanation == "" {
			it.Explanation = f.Solution
		}
		items = append(items, it)
	}
	for _, w := range rep.Warnings {
		add(SeverityWarning, w)
	}
	for _, s := range rep.Suggestions {
		add(SeveritySuggestion, s)
	}
	return items
}

func setFix(it *Item, f *Fix, state map[string]agentprotocol.HardeningFixState) {
	it.FixID = f.ID
	if s, ok := state[f.ID]; ok {
		it.FixState, it.FixMessage = s.State, s.Message
	}
}

func fieldTitle(testID string, d agentprotocol.HardeningDetail) string {
	if testID == "SSH-7408" {
		return fmt.Sprintf("sshd option %s: Lynis prefers %s (now %s)", d.Field, orNone(d.Preferred), d.Value)
	}
	return fmt.Sprintf("%s: Lynis prefers %s (now %s)", d.Field, d.Preferred, d.Value)
}

func orNone(s string) string {
	if s == "" {
		return "no explicit value"
	}
	return s
}

func firstWord(s string) string {
	for i := range s {
		if s[i] == ' ' || s[i] == '|' {
			return s[:i]
		}
	}
	return s
}

// shellSafe reports whether s is non-empty and made only of characters that
// are inert inside single quotes and in a file name (letters, digits and . _ : -).
func shellSafe(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == ':', c == '-':
		default:
			return false
		}
	}
	return true
}
