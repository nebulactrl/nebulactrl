package acl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/tailscale/hujson"
)

// cgnat is the address range of tailnet nodes (Tailscale always, Headscale by
// default).
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// hostOf strips the port of a "host:port" destination ("*" ports included).
func hostOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, ":"); i > 0 && !strings.Contains(s[:i], ":") {
		return s[:i]
	}
	return s
}

// rawRule is an acls entry as found in a policy file. Extra holds every key
// other than action, src and dst: a rule with extras is never ours.
type rawRule struct {
	Action string
	Src    []string
	Dst    []string
	Extra  map[string]json.RawMessage
}

func parseRules(entries []json.RawMessage) []rawRule {
	out := make([]rawRule, len(entries))
	for i, e := range entries {
		var all map[string]json.RawMessage
		if json.Unmarshal(e, &all) != nil {
			out[i] = rawRule{Extra: map[string]json.RawMessage{"?": nil}}
			continue
		}
		r := rawRule{Extra: map[string]json.RawMessage{}}
		for k, v := range all {
			switch k {
			case "action":
				_ = json.Unmarshal(v, &r.Action)
			case "src":
				if json.Unmarshal(v, &r.Src) != nil {
					r.Extra[k] = v
				}
			case "dst":
				if json.Unmarshal(v, &r.Dst) != nil {
					r.Extra[k] = v
				}
			default:
				r.Extra[k] = v
			}
		}
		out[i] = r
	}
	return out
}

// isOurs reports whether an acls entry belongs to NebulaCtrl: every src and dst
// host is a mesh-node IP (an address in known, or any literal IPv4 of the
// tailnet range, which covers the rules of nodes that were re-provisioned and
// whose old address is no longer in the database), the action is accept and the
// rule carries no other key (no proto, no posture). Anything else is the
// user's and is never touched.
func isOurs(r rawRule, known map[string]bool) bool {
	if r.Action != "accept" || len(r.Extra) > 0 || len(r.Src) == 0 || len(r.Dst) == 0 {
		return false
	}
	isMeshIP := func(h string) bool {
		if known[h] {
			return true
		}
		a, err := netip.ParseAddr(h)
		return err == nil && a.Is4() && cgnat.Contains(a)
	}
	for _, s := range r.Src {
		if !isMeshIP(s) {
			return false
		}
	}
	for _, d := range r.Dst {
		if !isMeshIP(hostOf(d)) {
			return false
		}
	}
	return true
}

func pointerEscape(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

// PatchHuJSON returns the tailnet policy current with NebulaCtrl's rules
// replaced by p and the mesh tag owned, without touching comments, formatting
// or any other rule of the user. current may be empty (a new policy).
//
//   - rules for which IsOurs holds are removed, the rules of p are appended;
//   - tagOwners[meshTag] is added with owners when it is missing (an existing
//     entry is the user's decision and stays).
func PatchHuJSON(current []byte, p Policy, meshTag string, owners []string, known map[string]bool) ([]byte, error) {
	if strings.TrimSpace(string(current)) == "" {
		current = []byte("{}")
	}
	v, err := hujson.Parse(current)
	if err != nil {
		return nil, fmt.Errorf("the tailnet policy is not valid HuJSON: %w", err)
	}
	// Standardize works in place: it gets its own copy so the parsed value keeps
	// its comments.
	std, err := hujson.Standardize(bytes.Clone(current))
	if err != nil {
		return nil, fmt.Errorf("the tailnet policy is not valid HuJSON: %w", err)
	}
	var doc struct {
		TagOwners map[string]json.RawMessage `json:"tagOwners"`
		ACLs      []json.RawMessage          `json:"acls"`
	}
	if err := json.Unmarshal(std, &doc); err != nil {
		return nil, fmt.Errorf("read the tailnet policy: %w", err)
	}
	var top map[string]json.RawMessage
	_ = json.Unmarshal(std, &top)
	_, hasACLs := top["acls"]
	_, hasOwners := top["tagOwners"]

	type op struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value any    `json:"value,omitempty"`
	}
	var ops []op
	if !hasOwners {
		ops = append(ops, op{Op: "add", Path: "/tagOwners", Value: map[string][]string{meshTag: owners}})
	} else if _, ok := doc.TagOwners[meshTag]; !ok {
		ops = append(ops, op{Op: "add", Path: "/tagOwners/" + pointerEscape(meshTag), Value: owners})
	}

	if !hasACLs {
		ops = append(ops, op{Op: "add", Path: "/acls", Value: p.Rules})
	} else {
		rules := parseRules(doc.ACLs)
		var drop []int
		for i, r := range rules {
			if isOurs(r, known) {
				drop = append(drop, i)
			}
		}
		sort.Sort(sort.Reverse(sort.IntSlice(drop)))
		for _, i := range drop {
			ops = append(ops, op{Op: "remove", Path: fmt.Sprintf("/acls/%d", i)})
		}
		for _, r := range p.Rules {
			ops = append(ops, op{Op: "add", Path: "/acls/-", Value: r})
		}
	}
	if len(ops) > 0 {
		patch, err := json.Marshal(ops)
		if err != nil {
			return nil, err
		}
		if err := v.Patch(patch); err != nil {
			return nil, fmt.Errorf("patch the tailnet policy: %w", err)
		}
	}
	v.Format()
	return v.Pack(), nil
}

// ContainsRules reports whether the policy current holds every rule of p
// verbatim (action accept, the same src and dst entries), the drift check of the
// advisory mode. It cannot prove that no broader rule exists.
func ContainsRules(current []byte, p Policy) (missing []Rule, err error) {
	std, err := hujson.Standardize(bytes.Clone(current))
	if err != nil {
		return nil, fmt.Errorf("the tailnet policy is not valid HuJSON: %w", err)
	}
	var doc struct {
		ACLs []json.RawMessage `json:"acls"`
	}
	if err := json.Unmarshal(std, &doc); err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, r := range parseRules(doc.ACLs) {
		if r.Action != "accept" || len(r.Extra) > 0 {
			continue
		}
		have[ruleKey(Rule{Action: r.Action, Src: r.Src, Dst: r.Dst})] = true
	}
	for _, r := range p.Rules {
		if !have[ruleKey(r)] {
			missing = append(missing, r)
		}
	}
	return missing, nil
}

func ruleKey(r Rule) string {
	src := append([]string{}, r.Src...)
	dst := append([]string{}, r.Dst...)
	sort.Strings(src)
	sort.Strings(dst)
	return strings.Join(src, ",") + "->" + strings.Join(dst, ",")
}

// StaleRules returns the rules of current that NebulaCtrl owns (IsOurs) but that
// are not part of p: the ones a managed push would remove.
func StaleRules(current []byte, p Policy, known map[string]bool) ([]Rule, error) {
	std, err := hujson.Standardize(bytes.Clone(current))
	if err != nil {
		return nil, fmt.Errorf("the tailnet policy is not valid HuJSON: %w", err)
	}
	var doc struct {
		ACLs []json.RawMessage `json:"acls"`
	}
	if err := json.Unmarshal(std, &doc); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, r := range p.Rules {
		want[ruleKey(r)] = true
	}
	var out []Rule
	for _, r := range parseRules(doc.ACLs) {
		if !isOurs(r, known) {
			continue
		}
		rule := Rule{Action: r.Action, Src: r.Src, Dst: r.Dst}
		if !want[ruleKey(rule)] {
			out = append(out, rule)
		}
	}
	return out, nil
}
