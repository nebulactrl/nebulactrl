package cloudflare

import (
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// MarkerPrefix starts the comment of every record NebulaCtrl owns:
// "nebulactrl:<ownerId>", the id of the service domain or status page the
// record belongs to. A record without it is never modified or deleted.
const MarkerPrefix = "nebulactrl:"

// MarkerTag is the tag added to owned records where the plan supports tags.
const MarkerTag = "nebulactrl:managed"

// Marker returns the ownership comment of an owner's records.
func Marker(ownerID uuid.UUID) string { return MarkerPrefix + ownerID.String() }

// ParseMarker extracts the owner id from an ownership comment.
func ParseMarker(comment string) (uuid.UUID, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(comment), MarkerPrefix)
	if !ok {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(rest)
	return id, err == nil
}

// DNS status of a domain.
const (
	StatusManaged     = "managed"      // a record NebulaCtrl created points to the right target
	StatusPresent     = "present"      // a record NebulaCtrl did not create points to the right target
	StatusConflict    = "conflict"     // a record NebulaCtrl did not create differs from the wanted one or blocks it
	StatusWrongTarget = "wrong_target" // a record of NebulaCtrl points elsewhere (the next sync corrects it)
	StatusMissing     = "missing"      // no record exists
	StatusNotInZone   = "not_in_zone"  // the hostname is in none of the connected zones
)

// Target is a record an owner should have.
type Target struct {
	Type    string // A, AAAA or CNAME
	Content string
	Proxied bool
}

// TargetForIP returns the A or AAAA target of an IP address.
func TargetForIP(ip string, proxied bool) (Target, bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil || a.IsUnspecified() {
		return Target{}, false
	}
	a = a.Unmap()
	if a.Is4() {
		return Target{Type: "A", Content: a.String(), Proxied: proxied}, true
	}
	return Target{Type: "AAAA", Content: a.String(), Proxied: proxied}, true
}

// TargetsForIPs returns the A and AAAA targets of a server's public_ip column:
// one address, or several separated by commas, semicolons or spaces. Anything
// unparseable is skipped; repeats are dropped; the order is kept.
func TargetsForIPs(list string, proxied bool) []Target {
	var out []Target
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' }) {
		t, ok := TargetForIP(f, proxied)
		if !ok || seen[t.Content] {
			continue
		}
		seen[t.Content] = true
		out = append(out, t)
	}
	return out
}

// TargetForCNAME returns the CNAME target of a tunnel. Tunnel records are
// always proxied: <id>.cfargotunnel.com does not resolve to anything else.
func TargetForCNAME(cname string) Target {
	return Target{Type: "CNAME", Content: normContent("CNAME", cname), Proxied: true}
}

// IsPrivate reports whether an A/AAAA target is not routable from Cloudflare.
func (t Target) IsPrivate() bool {
	if t.Type == "CNAME" {
		return false
	}
	a, err := netip.ParseAddr(t.Content)
	if err != nil {
		return false
	}
	return a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast()
}

func normContent(typ, content string) string {
	content = strings.TrimSpace(content)
	switch typ {
	case "CNAME":
		return strings.ToLower(strings.TrimSuffix(content, "."))
	case "A", "AAAA":
		if a, err := netip.ParseAddr(content); err == nil {
			return a.Unmap().String()
		}
	}
	return content
}

// Existing is a record found in the zone and whether NebulaCtrl owns it.
type Existing struct {
	Record
	Owned bool
}

// ActionKind is what a plan does to one record.
type ActionKind string

// Action kinds.
const (
	ActionCreate ActionKind = "create"
	ActionUpdate ActionKind = "update"
	ActionDelete ActionKind = "delete"
)

// Action is one change to the zone. Adopt marks an action of an explicit
// replace: an update that takes over a record NebulaCtrl did not create, or the
// delete of one that stood in the way.
type Action struct {
	Kind   ActionKind
	Record Record      // the existing record (update, delete)
	Input  RecordInput // the desired record (create, update)
	Adopt  bool
}

// PlanInput is everything BuildPlan decides on.
type PlanInput struct {
	OwnerID uuid.UUID // the service domain or status page the records belong to
	Host    string
	Want    []Target // empty: nothing can be created (no public IP, no tunnel)
	Records []Existing
	Adopt   bool // replace the records NebulaCtrl did not create with its own
}

// Plan is the classification of a hostname and the changes that would make it
// match. Current is the status of the zone as it is, After the status once the
// actions ran.
type Plan struct {
	Current string
	After   string
	Message string
	// NeedsAdopt is set when records NebulaCtrl did not create differ from the
	// wanted ones or block them: only an explicit replace (Adopt) changes them.
	NeedsAdopt bool
	Actions    []Action
}

// BuildPlan classifies the records of a hostname against the wanted targets and
// lists the changes. The targets are the records the name needs: a CNAME, or
// one A or AAAA record per public address. Records marked as NebulaCtrl's own
// are kept in step. A record NebulaCtrl did not create is never touched unless
// Adopt is set (the explicit replace): then the ones that already point to a
// target are marked as NebulaCtrl's, the others of a wanted type are pointed at
// the targets or deleted, and the ones that cannot sit next to the wanted type
// (a CNAME next to an address record) are deleted.
func BuildPlan(in PlanInput) Plan {
	var ours, others []Existing
	for _, r := range in.Records {
		if r.Owned {
			ours = append(ours, r)
		} else {
			others = append(others, r)
		}
	}
	if len(in.Want) == 0 {
		return planWithoutTarget(ours, others)
	}
	wants := normWants(in.Want)
	desired := make([]RecordInput, len(wants))
	types := map[string]bool{}
	for i, w := range wants {
		types[w.Type] = true
		desired[i] = RecordInput{
			Type: w.Type, Name: in.Host, Content: w.Content, Proxied: w.Proxied, TTL: 1,
			Comment: Marker(in.OwnerID), Tags: []string{MarkerTag},
		}
	}
	// Unmarked records that matter: those of a wanted type, and those that
	// cannot coexist with it (a CNAME next to an address record, either way).
	var relevant, blockers []Existing
	for _, o := range others {
		inTypes := types[o.Type]
		if inTypes || conflictsAny(types, o.Type) {
			relevant = append(relevant, o)
		}
		if !inTypes && conflictsAny(types, o.Type) {
			blockers = append(blockers, o) // relevant records of another type
		}
	}

	if len(ours) > 0 {
		return planOwned(in, wants, desired, ours, blockers)
	}
	if len(relevant) == 0 {
		p := Plan{Current: StatusMissing, After: StatusManaged, Message: fmt.Sprintf("No record for %s yet: %s", in.Host, describeTargets(wants))}
		for _, d := range desired {
			p.Actions = append(p.Actions, Action{Kind: ActionCreate, Input: d})
		}
		return p
	}
	// Only records NebulaCtrl does not own exist.
	if len(blockers) == 0 && covered(wants, relevant) && !surplus(wants, relevant) {
		if in.Adopt {
			// Adopting records that already point here only marks them as
			// NebulaCtrl's (and applies the proxy setting): that is what lets
			// the name be kept in step and, when proxied, locked to Cloudflare.
			p := Plan{
				Current: StatusPresent, After: StatusManaged,
				Message: fmt.Sprintf("The record for %s already points to %s; adopting marks it as managed by NebulaCtrl.", in.Host, contentList(wants)),
			}
			p.Actions = adoptActions(wants, desired, relevant)
			return p
		}
		return Plan{Current: StatusPresent, After: StatusPresent, Message: fmt.Sprintf("A record that NebulaCtrl did not create already points to %s. It is left untouched.", contentList(wants))}
	}
	if len(blockers) == 0 && covered(wants, relevant) {
		// Every target has a record and more of a type exist: a deliberate set
		// (round robin, for example) that already points here.
		return Plan{Current: StatusPresent, After: StatusPresent, Message: fmt.Sprintf("Records that NebulaCtrl did not create already point to %s. They are left untouched.", contentList(wants))}
	}
	p := Plan{
		Current: StatusConflict, After: StatusConflict, NeedsAdopt: true,
		Message: fmt.Sprintf("%s already has %s, which NebulaCtrl did not create; it should point to %s. The record stays as it is until you replace it with NebulaCtrl's record.", in.Host, describeExisting(relevant), contentList(wants)),
	}
	if !in.Adopt {
		return p
	}
	p.After = StatusManaged
	p.Actions = replaceActions(wants, desired, relevant, blockers)
	return p
}

// replaceActions is the explicit replace of unmarked records: the ones that
// cannot coexist are deleted first (a CNAME must go before an address record
// can be created), then the records of the wanted types are adopted for the
// targets and the surplus ones deleted.
func replaceActions(wants []Target, desired []RecordInput, relevant, blockers []Existing) []Action {
	var typed []Existing
	for _, r := range relevant {
		if !slices.ContainsFunc(blockers, func(b Existing) bool { return b.ID == r.ID }) {
			typed = append(typed, r)
		}
	}
	adopt := adoptActions(wants, desired, typed)
	taken := map[string]bool{}
	for _, a := range adopt {
		if a.Kind == ActionUpdate {
			taken[a.Record.ID] = true
		}
	}
	var acts []Action
	for _, b := range blockers {
		acts = append(acts, Action{Kind: ActionDelete, Record: b.Record, Adopt: true})
	}
	for _, r := range typed {
		if !taken[r.ID] {
			acts = append(acts, Action{Kind: ActionDelete, Record: r.Record, Adopt: true})
		}
	}
	return append(acts, adopt...)
}

// normWants copies the targets with normalized content and without repeats.
func normWants(in []Target) []Target {
	out := make([]Target, 0, len(in))
	for _, w := range in {
		w.Content = normContent(w.Type, w.Content)
		dup := false
		for _, o := range out {
			if o.Type == w.Type && o.Content == w.Content {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, w)
		}
	}
	return out
}

// covered reports whether every target has an unmarked record of the same type and content.
func covered(wants []Target, recs []Existing) bool {
	for _, w := range wants {
		found := false
		for _, r := range recs {
			if r.Type == w.Type && normContent(r.Type, r.Content) == w.Content {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// surplus reports whether there are more unmarked records of a wanted type than targets of it.
func surplus(wants []Target, recs []Existing) bool {
	for _, t := range sortedTypes(typeSet(wants)) {
		if len(sameType(recs, t)) > countType(wants, t) {
			return true
		}
	}
	return false
}

// adoptActions takes over unmarked records: one that already has a target's
// content is updated for that target, the rest of a type go to the targets of
// that type that have none, and a target left without a record is created.
func adoptActions(wants []Target, desired []RecordInput, recs []Existing) []Action {
	used := make([]bool, len(recs))
	pick := make([]int, len(wants))
	for wi, w := range wants {
		pick[wi] = -1
		for i, r := range recs {
			if !used[i] && r.Type == w.Type && normContent(r.Type, r.Content) == w.Content {
				used[i], pick[wi] = true, i
				break
			}
		}
	}
	for wi, w := range wants {
		if pick[wi] >= 0 {
			continue
		}
		for i, r := range recs {
			if !used[i] && r.Type == w.Type {
				used[i], pick[wi] = true, i
				break
			}
		}
	}
	acts := make([]Action, 0, len(wants))
	for wi := range wants {
		if pick[wi] < 0 {
			acts = append(acts, Action{Kind: ActionCreate, Input: desired[wi]})
			continue
		}
		acts = append(acts, Action{Kind: ActionUpdate, Record: recs[pick[wi]].Record, Input: desired[wi], Adopt: true})
	}
	return acts
}

// planOwned reconciles records NebulaCtrl owns.
func planOwned(in PlanInput, wants []Target, desired []RecordInput, ours, blockers []Existing) Plan {
	fits := func(r Record, w Target) bool {
		return r.Type == w.Type && normContent(r.Type, r.Content) == w.Content && r.Proxied == w.Proxied
	}
	used := make([]bool, len(ours))
	keep := make([]int, len(wants)) // the owned record that already is the target, -1 when none
	allKept := true
	for wi, w := range wants {
		keep[wi] = -1
		for i, o := range ours {
			if !used[i] && fits(o.Record, w) {
				used[i], keep[wi] = true, i
				break
			}
		}
		allKept = allKept && keep[wi] >= 0
	}
	if allKept && len(ours) == len(wants) && len(blockers) == 0 {
		return Plan{Current: StatusManaged, After: StatusManaged, Message: fmt.Sprintf("%s points to %s through a record managed by NebulaCtrl.", in.Host, contentList(wants))}
	}
	if len(blockers) > 0 && !in.Adopt {
		// A record NebulaCtrl did not create stands in the way of the one it
		// manages: it goes only when the person replaces it.
		return Plan{
			Current: StatusConflict, After: StatusConflict, NeedsAdopt: true,
			Message: fmt.Sprintf("%s has %s, which NebulaCtrl did not create and which cannot sit next to the record it manages. The record stays as it is until you replace it with NebulaCtrl's record.", in.Host, describeExisting(blockers)),
		}
	}
	p := Plan{Current: StatusWrongTarget, After: StatusWrongTarget}
	if allKept {
		p.Current = StatusManaged
		p.After = StatusManaged
	}
	p.Message = fmt.Sprintf("%s has %s (managed by NebulaCtrl); it should be %s.", in.Host, describeExisting(ours), describeTargets(wants))
	for _, b := range blockers {
		p.Actions = append(p.Actions, Action{Kind: ActionDelete, Record: b.Record, Adopt: true})
	}
	// Reuse a record of the right type, delete the others, create when none fits.
	reuse := slices.Clone(keep)
	for wi, w := range wants {
		if reuse[wi] >= 0 {
			continue
		}
		for i, o := range ours {
			if !used[i] && o.Type == w.Type {
				used[i], reuse[wi] = true, i
				break
			}
		}
	}
	for i, o := range ours {
		if !used[i] {
			p.Actions = append(p.Actions, Action{Kind: ActionDelete, Record: o.Record})
		}
	}
	for wi := range wants {
		switch {
		case keep[wi] >= 0:
		case reuse[wi] < 0:
			p.Actions = append(p.Actions, Action{Kind: ActionCreate, Input: desired[wi]})
		default:
			p.Actions = append(p.Actions, Action{Kind: ActionUpdate, Record: ours[reuse[wi]].Record, Input: desired[wi]})
		}
	}
	p.After = StatusManaged
	return p
}

func planWithoutTarget(ours, others []Existing) Plan {
	const hint = "The server has no public IP and no tunnel, so there is nothing to point the record to."
	switch {
	case len(ours) > 0:
		return Plan{Current: StatusManaged, After: StatusManaged, Message: hint + " The existing record is left as it is."}
	case len(others) > 0:
		return Plan{Current: StatusPresent, After: StatusPresent, Message: hint + " Existing records are left as they are."}
	}
	return Plan{Current: StatusMissing, After: StatusMissing, Message: hint + " Set the server's public IP to create a record."}
}

// conflicts reports whether records of type b cannot sit next to type a at one name.
func conflicts(a, b string) bool {
	if a == b {
		return false
	}
	return a == "CNAME" || b == "CNAME"
}

// conflictsAny reports whether a record of type b cannot sit next to a record of any of types.
func conflictsAny(types map[string]bool, b string) bool {
	for t := range types {
		if conflicts(t, b) {
			return true
		}
	}
	return false
}

func typeSet(wants []Target) map[string]bool {
	out := map[string]bool{}
	for _, w := range wants {
		out[w.Type] = true
	}
	return out
}

func sortedTypes(types map[string]bool) []string {
	out := make([]string, 0, len(types))
	for t := range types {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func countType(wants []Target, typ string) int {
	n := 0
	for _, w := range wants {
		if w.Type == typ {
			n++
		}
	}
	return n
}

func sameType(rs []Existing, typ string) []Existing {
	var out []Existing
	for _, r := range rs {
		if r.Type == typ {
			out = append(out, r)
		}
	}
	return out
}

func describeTarget(t Target) string {
	s := t.Type + " " + t.Content
	if t.Proxied {
		s += " (proxied)"
	}
	return s
}

func describeTargets(ts []Target) string {
	parts := make([]string, 0, len(ts))
	for _, t := range ts {
		parts = append(parts, describeTarget(t))
	}
	return strings.Join(parts, ", ")
}

// contentList names where the targets point to ("203.0.113.10", "203.0.113.10, 2001:db8::1").
func contentList(ts []Target) string {
	parts := make([]string, 0, len(ts))
	for _, t := range ts {
		parts = append(parts, t.Content)
	}
	return strings.Join(parts, ", ")
}

func describeExisting(rs []Existing) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		s := r.Type + " " + r.Content
		if r.Proxied {
			s += " (proxied)"
		}
		parts = append(parts, s)
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}
