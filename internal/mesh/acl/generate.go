// Package acl turns the mesh tables (server nodes, link nodes, exposures and
// grants) into a tailnet access policy. Everything here is pure and
// deterministic: the same state, in any order, yields the same bytes. Nothing
// here talks to a coordinator (internal/mesh applies the result).
//
// Model: one rule per link node. The source is the link's tailnet
// IPv4, the destinations are "<remote server node IPv4>:<mesh port>" for the
// exposures its grants reach. Environment isolation is structural: a grant can
// only join a link and an exposure of the same environment (mesh.Grant checks
// it, Generate re-checks it), so a link of one environment can never be the
// source of a rule that opens another environment's port. The default is deny:
// the policy always carries an explicit acls array. IPv4 only.
package acl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// Node is a server node: the tailnet identity of a mesh-enabled server.
type Node struct {
	ServerID string
	IPs      []string
}

// Link is a link node of (environment, ServerID -> RemoteServerID, purpose).
type Link struct {
	ID             string
	EnvironmentID  string
	ServerID       string
	RemoteServerID string
	Purpose        string
	IPs            []string
	// Label is a human name used in comments and the view ("web: A -> B").
	Label string
}

// Exposure is a service port on a server node.
type Exposure struct {
	ID            string
	ServerID      string
	ServiceID     string
	EnvironmentID string
	Purpose       string
	MeshPort      int
}

// Grant lets Link reach Exposure on behalf of RefID.
type Grant struct {
	LinkID     string
	ExposureID string
	RefID      string
	Source     string
}

// State is everything the generator reads.
type State struct {
	Nodes     []Node
	Links     []Link
	Exposures []Exposure
	Grants    []Grant
}

// Rule is one ACL entry in the legacy "acls" syntax (accepted by Tailscale and
// every Headscale >= 0.23).
type Rule struct {
	Action string   `json:"action"`
	Src    []string `json:"src"`
	Dst    []string `json:"dst"`
}

// Target is one destination of a rule and why it exists.
type Target struct {
	Dst        string // "<ip>:<port>"
	ServerID   string
	Port       int
	ExposureID string
	ServiceID  string
	Purpose    string
	// Grants are the (ref, source) pairs that need this destination.
	Grants []Grant
}

// Detail explains one rule: the link it belongs to and its targets.
type Detail struct {
	LinkID  string
	Label   string
	Src     string
	Targets []Target
}

// Policy is the generated rule set. Rules and Details have the same length and
// order.
type Policy struct {
	Rules   []Rule
	Details []Detail
}

// firstIPv4 returns the first IPv4 address of ips ("" when none).
func firstIPv4(ips []string) string {
	for _, s := range ips {
		a, err := netip.ParseAddr(strings.TrimSpace(s))
		if err == nil && a.Is4() {
			return a.String()
		}
	}
	return ""
}

// Generate builds the policy. Links without an IPv4 or without a reachable
// grant produce no rule; a grant whose exposure is on another server than the
// link's remote, or in another environment, or of another purpose is ignored
// (defence in depth: mesh.Grant rejects them too).
func Generate(st State) Policy {
	nodeIP := map[string]string{}
	for _, n := range st.Nodes {
		if ip := firstIPv4(n.IPs); ip != "" {
			nodeIP[n.ServerID] = ip
		}
	}
	links := map[string]Link{}
	for _, l := range st.Links {
		links[l.ID] = l
	}
	exps := map[string]Exposure{}
	for _, e := range st.Exposures {
		exps[e.ID] = e
	}

	type key struct{ link, exposure string }
	byPair := map[key][]Grant{}
	for _, g := range st.Grants {
		l, okL := links[g.LinkID]
		e, okE := exps[g.ExposureID]
		if !okL || !okE {
			continue
		}
		if e.ServerID != l.RemoteServerID || e.EnvironmentID != l.EnvironmentID || e.Purpose != l.Purpose {
			continue
		}
		k := key{g.LinkID, g.ExposureID}
		byPair[k] = append(byPair[k], g)
	}

	var details []Detail
	byLink := map[string]int{}
	for k, gs := range byPair {
		l, e := links[k.link], exps[k.exposure]
		src := firstIPv4(l.IPs)
		dstIP := nodeIP[l.RemoteServerID]
		if src == "" || dstIP == "" {
			continue
		}
		sort.Slice(gs, func(i, j int) bool {
			if gs[i].Source != gs[j].Source {
				return gs[i].Source < gs[j].Source
			}
			return gs[i].RefID < gs[j].RefID
		})
		idx, ok := byLink[l.ID]
		if !ok {
			details = append(details, Detail{LinkID: l.ID, Label: l.Label, Src: src})
			idx = len(details) - 1
			byLink[l.ID] = idx
		}
		d := &details[idx]
		d.Targets = append(d.Targets, Target{
			Dst: dstIP + ":" + strconv.Itoa(e.MeshPort), ServerID: l.RemoteServerID, Port: e.MeshPort,
			ExposureID: e.ID, ServiceID: e.ServiceID, Purpose: e.Purpose, Grants: gs,
		})
	}
	for i := range details {
		sort.Slice(details[i].Targets, func(a, b int) bool {
			ta, tb := details[i].Targets[a], details[i].Targets[b]
			if ta.Dst != tb.Dst {
				return ta.Dst < tb.Dst
			}
			return ta.ExposureID < tb.ExposureID
		})
	}
	sort.Slice(details, func(i, j int) bool {
		if details[i].Src != details[j].Src {
			return details[i].Src < details[j].Src
		}
		return details[i].LinkID < details[j].LinkID
	})

	p := Policy{Rules: []Rule{}, Details: details}
	for _, d := range details {
		var dst []string
		seen := map[string]bool{}
		for _, t := range d.Targets {
			if !seen[t.Dst] {
				seen[t.Dst] = true
				dst = append(dst, t.Dst)
			}
		}
		sort.Strings(dst)
		p.Rules = append(p.Rules, Rule{Action: "accept", Src: []string{d.Src}, Dst: dst})
	}
	return p
}

// Base is the part of a managed policy that is not generated from the mesh
// tables: the tag owners and the fixed rules that come before the generated ones.
type Base struct {
	// TagOwners maps a tag to its owners (users, groups or tags).
	TagOwners map[string][]string
	// Rules come first, in order (for the platform Headscale: members reach the
	// service tag).
	Rules []Rule
}

type document struct {
	TagOwners map[string][]string `json:"tagOwners"`
	ACLs      []Rule              `json:"acls"`
}

// Document renders the complete policy NebulaCtrl owns (managed Headscale).
// Output is indented JSON, which is valid HuJSON, with a trailing newline.
func (p Policy) Document(b Base) ([]byte, error) {
	doc := document{TagOwners: map[string][]string{}, ACLs: make([]Rule, 0, len(b.Rules)+len(p.Rules))}
	for t, o := range b.TagOwners {
		owners := append([]string{}, o...)
		sort.Strings(owners)
		doc.TagOwners[t] = owners
	}
	doc.ACLs = append(doc.ACLs, b.Rules...)
	doc.ACLs = append(doc.ACLs, p.Rules...)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// Hash is the fingerprint of a rendered document (stored as acl_hash).
func Hash(doc []byte) string {
	sum := sha256.Sum256(doc)
	return hex.EncodeToString(sum[:])
}

// Snippet renders the generated part for a person to paste into the tailnet
// policy (HuJSON with comments): the advisory mode of Tailscale and file-mode
// Headscale. owners are the tagOwners entry the mesh tag needs.
func (p Policy) Snippet(meshTag string, owners []string) string {
	var b strings.Builder
	b.WriteString("// NebulaCtrl mesh rules. Add the tagOwners entry once, and keep the acls\n")
	b.WriteString("// below in sync when NebulaCtrl shows new rules. Default is deny: nothing\n")
	b.WriteString("// else is opened.\n")
	b.WriteString("{\n")
	o, _ := json.Marshal(append([]string{}, owners...))
	fmt.Fprintf(&b, "  \"tagOwners\": {%q: %s},\n", meshTag, o)
	b.WriteString("  \"acls\": [\n")
	for i, d := range p.Details {
		fmt.Fprintf(&b, "    // %s\n", oneLine(d.Label, d.LinkID))
		r := p.Rules[i]
		raw, _ := json.Marshal(r)
		b.Write([]byte("    "))
		b.Write(raw)
		if i < len(p.Rules)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("  ]\n}\n")
	return b.String()
}

func oneLine(label, fallback string) string {
	if label == "" {
		label = fallback
	}
	label = strings.NewReplacer("\n", " ", "\r", " ").Replace(label)
	return label
}
