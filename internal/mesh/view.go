package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/mesh/acl"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// View is the Team settings → Server mesh page.
type View struct {
	Enabled              bool            `json:"enabled" doc:"At least one server joined the mesh (turned on by the first server)"`
	AutoJoin             bool            `json:"autoJoin" doc:"Online servers with a current agent join the mesh automatically (each adds one device)"`
	Paused               bool            `json:"paused" doc:"Mesh changes wait (the platform Headscale is being migrated); established connections keep working"`
	PausedReason         string          `json:"pausedReason"`
	Provider             string          `json:"provider" enum:"tailscale,headscale,none"`
	LoginServer          string          `json:"loginServer"`
	MeshTag              string          `json:"meshTag" doc:"The one tag every mesh node of the team carries; it needs a tagOwners entry on Tailscale"`
	ACLMode              string          `json:"aclMode" enum:"managed,advisory,off"`
	ACLState             string          `json:"aclState" enum:"unknown,applied,pending,drift,advisory,error"`
	ACLMessage           string          `json:"aclMessage"`
	ManagedPushAvailable bool            `json:"managedPushAvailable" doc:"The credential can write the tailnet policy (Tailscale OAuth with the policy_file scope, or a Headscale API key)"`
	ACLManagedWarning    string          `json:"aclManagedWarning,omitempty" doc:"Shown while managed mode is on for Tailscale"`
	Coordinator          CoordinatorView `json:"coordinator"`
	Nodes                []NodeView      `json:"nodes"`
	Links                []MeshLinkView  `json:"links"`
	Rules                []RuleView      `json:"rules" doc:"The access rules NebulaCtrl generates (read-only)"`
	Devices              DevicesView     `json:"devices"`
	Warnings             []string        `json:"warnings"`
	Snippet              string          `json:"snippet,omitempty" doc:"The rules to add to the tailnet policy (advisory mode)"`
}

// CoordinatorView is the health of the tailnet coordinator.
type CoordinatorView struct {
	OK        *bool      `json:"ok" doc:"null until checked"`
	CheckedAt *time.Time `json:"checkedAt"`
}

// DevicesView counts tailnet devices.
type DevicesView struct {
	Used int `json:"used" doc:"Tagged devices the mesh uses: one per server node and one per link node"`
}

// PeerView is a mesh peer as seen from a server node.
type PeerView struct {
	Name      string   `json:"name"`
	IPs       []string `json:"ips"`
	Direct    bool     `json:"direct"`
	Relay     string   `json:"relay,omitempty"`
	LatencyMs *int     `json:"latencyMs,omitempty"`
	Online    bool     `json:"online"`
}

// NodeExposureView is a service port reachable on a server node.
type NodeExposureView struct {
	ServiceID   uuid.UUID `json:"serviceId"`
	ServiceName string    `json:"serviceName"`
	Purpose     string    `json:"purpose" enum:"replication,service"`
	MeshPort    int       `json:"meshPort"`
}

// NodeView is the mesh node of one server.
type NodeView struct {
	ServerID         uuid.UUID          `json:"serverId"`
	ServerName       string             `json:"serverName"`
	State            string             `json:"state" enum:"provisioning,online,offline,needs_attention,unknown"`
	Message          string             `json:"message"`
	Hostname         string             `json:"hostname"`
	FQDN             string             `json:"fqdn"`
	IPs              []string           `json:"ips"`
	NeedsReprovision bool               `json:"needsReprovision"`
	Peers            []PeerView         `json:"peers"`
	Exposures        []NodeExposureView `json:"exposures"`
	Devices          int                `json:"devices" doc:"This node plus the link nodes that run on this server"`
	LastSeenAt       *time.Time         `json:"lastSeenAt"`
	TunDevice        bool               `json:"tunDevice"`
	AgentCapable     bool               `json:"agentCapable"`
}

// MeshGrantView is what a link may reach on behalf of whom. (Named apart from the
// grant and link views of services and meshlinks: huma derives schema names from type names.)
type MeshGrantView struct {
	ExposureID   uuid.UUID `json:"exposureId"`
	ProviderSlug string    `json:"providerSlug"`
	RefID        uuid.UUID `json:"refId"`
	RefLabel     string    `json:"refLabel"`
	Source       string    `json:"source" enum:"reference,edge,replica"`
	MeshPort     int       `json:"meshPort"`
}

// MeshLinkView is a link node.
type MeshLinkView struct {
	ID               uuid.UUID       `json:"id"`
	EnvironmentID    uuid.UUID       `json:"environmentId"`
	EnvironmentName  string          `json:"environmentName"`
	ProjectName      string          `json:"projectName"`
	ServerID         uuid.UUID       `json:"serverId"`
	ServerName       string          `json:"serverName"`
	RemoteServerID   uuid.UUID       `json:"remoteServerId"`
	RemoteServerName string          `json:"remoteServerName"`
	Purpose          string          `json:"purpose" enum:"replication,service"`
	State            string          `json:"state" enum:"provisioning,online,degraded,offline,needs_attention,removing,unknown"`
	Message          string          `json:"message"`
	Hostname         string          `json:"hostname"`
	IPs              []string        `json:"ips"`
	NeedsReprovision bool            `json:"needsReprovision"`
	Grants           []MeshGrantView `json:"grants"`
}

// RuleView is one derived access rule: a link may dial one exposure port.
type RuleView struct {
	Src     string    `json:"src"`
	Dst     string    `json:"dst" doc:"<remote server node IPv4>:<mesh port>"`
	Port    int       `json:"port"`
	LinkID  uuid.UUID `json:"linkId"`
	Because string    `json:"because"`
}

// fresh reports whether an observation is recent enough to call a node online.
func (s *Service) fresh(t *time.Time) bool { return t != nil && s.now().Sub(*t) < observedStale }

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func decodePeers(raw json.RawMessage) []PeerView {
	var ps []agentprotocol.MeshPeer
	out := []PeerView{}
	if len(raw) == 0 || json.Unmarshal(raw, &ps) != nil {
		return out
	}
	for _, p := range ps {
		out = append(out, PeerView{Name: p.Host, IPs: []string{p.IP}, Direct: p.Direct, Relay: p.Relay, LatencyMs: p.LatencyMs, Online: p.Online})
	}
	return out
}

func (s *Service) nodeView(sn *snapshot, n db.ServerMesh) NodeView {
	srv := sn.servers[n.ServerID]
	state := n.State
	if state == "online" && !s.fresh(n.ObservedAt) {
		state = "unknown"
	}
	v := NodeView{
		ServerID: n.ServerID, ServerName: srv.Name, State: state, Message: n.Message, Hostname: n.Hostname, FQDN: n.Fqdn,
		IPs: orEmpty(n.Ips), NeedsReprovision: n.NeedsReprovision, Peers: decodePeers(n.Peers), Exposures: []NodeExposureView{}, Devices: 1,
		LastSeenAt: n.ObservedAt, TunDevice: srv.Facts.TunDevice, AgentCapable: capable(srv.Facts),
	}
	for _, l := range sn.links {
		if l.ServerID == n.ServerID {
			v.Devices++
		}
	}
	for _, e := range sn.exposures {
		if e.ServerID == n.ServerID {
			v.Exposures = append(v.Exposures, NodeExposureView{ServiceID: e.ServiceID, ServiceName: sn.svc[e.ServiceID].Name, Purpose: e.Purpose, MeshPort: int(e.MeshPort)})
		}
	}
	return v
}

func (s *Service) linkViews(ctx context.Context, sn *snapshot) ([]MeshLinkView, error) {
	envIDs := map[uuid.UUID]bool{}
	for _, l := range sn.links {
		envIDs[l.EnvironmentID] = true
	}
	envs := map[uuid.UUID]db.ListMeshEnvironmentsByIDsRow{}
	if len(envIDs) > 0 {
		ids := make([]uuid.UUID, 0, len(envIDs))
		for id := range envIDs {
			ids = append(ids, id)
		}
		rows, err := s.q.ListMeshEnvironmentsByIDs(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("list environments: %w", err)
		}
		for _, r := range rows {
			envs[r.ID] = r
		}
	}
	exp := map[uuid.UUID]db.MeshExposure{}
	for _, e := range sn.exposures {
		exp[e.ID] = e
	}
	out := []MeshLinkView{}
	for _, l := range sn.links {
		state := l.State
		if state == "online" && !s.fresh(l.ObservedAt) {
			state = "unknown"
		}
		v := MeshLinkView{
			ID: l.ID, EnvironmentID: l.EnvironmentID, EnvironmentName: envs[l.EnvironmentID].Name, ProjectName: envs[l.EnvironmentID].ProjectName,
			ServerID: l.ServerID, ServerName: sn.serverName(l.ServerID), RemoteServerID: l.RemoteServerID, RemoteServerName: sn.serverName(l.RemoteServerID),
			Purpose: l.Purpose, State: state, Message: l.Message, Hostname: l.Hostname, IPs: orEmpty(l.Ips), NeedsReprovision: l.NeedsReprovision,
			Grants: []MeshGrantView{},
		}
		for _, g := range sn.grants {
			if g.LinkID != l.ID {
				continue
			}
			e := exp[g.ExposureID]
			v.Grants = append(v.Grants, MeshGrantView{
				ExposureID: g.ExposureID, ProviderSlug: sn.svc[e.ServiceID].Slug, RefID: g.RefID, RefLabel: sn.svc[g.RefID].Name,
				Source: g.Source, MeshPort: int(e.MeshPort),
			})
		}
		sort.Slice(v.Grants, func(i, j int) bool {
			if v.Grants[i].MeshPort != v.Grants[j].MeshPort {
				return v.Grants[i].MeshPort < v.Grants[j].MeshPort
			}
			return v.Grants[i].RefID.String() < v.Grants[j].RefID.String()
		})
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hostname < out[j].Hostname })
	return out, nil
}

// ruleViews flattens the generated policy into one line per (link, destination).
func (sn *snapshot) ruleViews(pol acl.Policy) []RuleView {
	out := []RuleView{}
	for _, d := range pol.Details {
		lid, _ := uuid.Parse(d.LinkID)
		for _, t := range d.Targets {
			var why []string
			provider := sn.svc[uuid.MustParse(t.ServiceID)].Slug
			for _, g := range t.Grants {
				ref := uuid.MustParse(g.RefID)
				name := sn.svc[ref].Slug
				switch g.Source {
				case "replica":
					why = append(why, fmt.Sprintf("replica %s of %s", name, provider))
				case "edge":
					why = append(why, fmt.Sprintf("canvas link %s -> %s", name, provider))
				default:
					why = append(why, fmt.Sprintf("%s references %s", name, provider))
				}
			}
			out = append(out, RuleView{Src: d.Src, Dst: t.Dst, Port: t.Port, LinkID: lid, Because: strings.Join(why, "; ")})
		}
	}
	return out
}

func (sn *snapshot) providerName() string {
	switch {
	case !sn.haveInteg:
		return "none"
	case sn.isHeadscale():
		return "headscale"
	}
	return "tailscale"
}

// View returns the mesh page (servers:read).
func (s *Service) View(ctx context.Context) (View, error) {
	if err := policy.Require(ctx, policy.ScopeServersRead, nil); err != nil {
		return View{}, err
	}
	sn, err := s.load(ctx, s.q, policy.WorkspaceID(ctx))
	if err != nil {
		return View{}, err
	}
	links, err := s.linkViews(ctx, sn)
	if err != nil {
		return View{}, err
	}
	pol := acl.Generate(sn.aclState())
	v := View{
		Enabled: sn.set.Enabled, AutoJoin: sn.set.AutoJoin, Paused: sn.set.Paused, PausedReason: sn.set.PausedReason,
		Provider: sn.providerName(), LoginServer: sn.integ.LoginServer, MeshTag: sn.meshTag(),
		ACLMode: sn.set.AclMode, ACLState: sn.set.AclState, ACLMessage: sn.set.AclMessage,
		ManagedPushAvailable: sn.canWritePolicy(),
		Coordinator:          CoordinatorView{OK: sn.set.CoordinatorOk, CheckedAt: sn.set.CoordinatorCheckedAt},
		Nodes:                []NodeView{}, Links: links, Rules: sn.ruleViews(pol), Devices: DevicesView{Used: sn.devices()}, Warnings: []string{},
	}
	if sn.haveInteg && !sn.isHeadscale() && sn.set.AclMode == ModeManaged {
		v.ACLManagedWarning = "Managed mode lets NebulaCtrl's OAuth token (scope policy_file) rewrite the whole tailnet policy. A compromised control plane could do the same. NebulaCtrl only replaces its own rules and keeps comments, but the token itself can write anything."
	}
	for _, n := range sn.nodes {
		v.Nodes = append(v.Nodes, s.nodeView(sn, n))
	}
	sort.Slice(v.Nodes, func(i, j int) bool { return v.Nodes[i].ServerName < v.Nodes[j].ServerName })
	if sn.set.AclMode != ModeManaged && sn.haveInteg && len(pol.Rules) > 0 {
		owners := tailscaleTagOwners
		if sn.isHeadscale() {
			owners = s.baseFor(ctx, sn).TagOwners[sn.meshTag()]
		}
		v.Snippet = pol.Snippet(sn.meshTag(), owners)
	}
	for _, l := range links {
		if l.Purpose == agentprotocol.MeshPurposeService && sn.set.AclMode != ModeManaged {
			v.Warnings = append(v.Warnings, "The tailnet policy is not managed by NebulaCtrl: with Tailscale's default allow-all policy a link can reach any exposed port on its remote server node. The connectivity test proves the link works, not that nothing else is reachable.")
			break
		}
	}
	if sn.haveInteg && sn.isHeadscale() {
		v.Warnings = append(v.Warnings, "Without a direct path, traffic falls back to Tailscale's public DERP relays (end-to-end encrypted: they see ciphertext and endpoint addresses). An embedded DERP server is not built.")
	}
	return v, nil
}

// ServerMeshView is the mesh card of a server.
type ServerMeshView struct {
	Enabled      bool           `json:"enabled"`
	AgentCapable bool           `json:"agentCapable"`
	TunDevice    bool           `json:"tunDevice"`
	Paused       bool           `json:"paused"`
	Node         *NodeView      `json:"node"`
	Links        []MeshLinkView `json:"links" doc:"Link nodes that run on this server"`
}

// GetServerMesh returns the mesh card of one server (servers:read).
func (s *Service) GetServerMesh(ctx context.Context, serverID uuid.UUID) (ServerMeshView, error) {
	srv, err := s.loadServer(ctx, s.q, policy.ScopeServersRead, serverID)
	if err != nil {
		return ServerMeshView{}, err
	}
	sn, err := s.load(ctx, s.q, srv.WorkspaceID)
	if err != nil {
		return ServerMeshView{}, err
	}
	f := factsOf(srv)
	v := ServerMeshView{AgentCapable: capable(f), TunDevice: f.TunDevice, Paused: sn.set.Paused, Links: []MeshLinkView{}}
	if n, ok := sn.nodes[serverID]; ok {
		nv := s.nodeView(sn, n)
		v.Node, v.Enabled = &nv, true
	}
	all, err := s.linkViews(ctx, sn)
	if err != nil {
		return ServerMeshView{}, err
	}
	for _, l := range all {
		if l.ServerID == serverID {
			v.Links = append(v.Links, l)
		}
	}
	return v, nil
}

// PreviewACL returns the rules NebulaCtrl derives, the document or snippet that
// goes with them and, when the stored policy can be read, what would change
// (servers:read).
func (s *Service) PreviewACL(ctx context.Context) (ACLPreview, error) {
	if err := policy.Require(ctx, policy.ScopeServersRead, nil); err != nil {
		return ACLPreview{}, err
	}
	sn, err := s.load(ctx, s.q, policy.WorkspaceID(ctx))
	if err != nil {
		return ACLPreview{}, err
	}
	if !sn.haveInteg {
		return ACLPreview{}, apierr.MeshNotConfigured()
	}
	pol := acl.Generate(sn.aclState())
	out := ACLPreview{Rules: sn.ruleViews(pol)}
	switch {
	case sn.isHeadscale() && sn.set.AclMode == ModeManaged:
		doc, err := pol.Document(s.baseFor(ctx, sn))
		if err != nil {
			return ACLPreview{}, err
		}
		out.Policy = string(doc)
	default:
		owners := tailscaleTagOwners
		if sn.isHeadscale() {
			owners = s.baseFor(ctx, sn).TagOwners[sn.meshTag()]
		}
		out.Policy = pol.Snippet(sn.meshTag(), owners)
	}
	out.Diff = s.diffFor(ctx, sn, pol)
	return out, nil
}

// ApplyACLNow applies the ACL right away (admin) and returns the page.
func (s *Service) ApplyACLNow(ctx context.Context) (View, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return View{}, err
	}
	ws := policy.WorkspaceID(ctx)
	sn, err := s.load(ctx, s.q, ws)
	if err != nil {
		return View{}, err
	}
	if !sn.haveInteg {
		return View{}, apierr.MeshNotConfigured()
	}
	if sn.set.Paused {
		return View{}, apierr.MeshPaused(sn.set.PausedReason)
	}
	if err := s.ApplyACL(ctx, ws); err != nil {
		var ae *apierr.Error
		if errors.As(err, &ae) {
			return View{}, err
		}
		s.log.WarnContext(ctx, "apply mesh acl", "err", err)
	}
	if err := s.audit(ctx, "mesh.acl.apply", ws); err != nil {
		return View{}, err
	}
	return s.View(ctx)
}

// ACLCheck is the answer of CheckACL.
type ACLCheck struct {
	// Readable is false when the stored policy could not be read (a missing
	// policy_file:read scope): the connectivity test is then the only proof.
	Readable bool       `json:"readable"`
	Message  string     `json:"message"`
	Missing  []acl.Rule `json:"missing" doc:"Generated rules absent from the stored policy"`
	Stale    []acl.Rule `json:"stale" doc:"Rules NebulaCtrl owns in the stored policy that are no longer generated"`
}

// CheckACL compares the generated rules with the stored policy without writing
// anything (servers:read). It cannot prove that no broader rule exists.
func (s *Service) CheckACL(ctx context.Context) (ACLCheck, error) {
	if err := policy.Require(ctx, policy.ScopeServersRead, nil); err != nil {
		return ACLCheck{}, err
	}
	sn, err := s.load(ctx, s.q, policy.WorkspaceID(ctx))
	if err != nil {
		return ACLCheck{}, err
	}
	if !sn.haveInteg {
		return ACLCheck{}, apierr.MeshNotConfigured()
	}
	out := ACLCheck{Missing: []acl.Rule{}, Stale: []acl.Rule{}}
	api, err := s.ts.PolicyAPI(ctx, sn.ws)
	if noPolicyAccess(err) {
		out.Message = "This credential cannot read the tailnet policy."
		return out, nil
	}
	if err != nil {
		return ACLCheck{}, fmt.Errorf("policy api: %w", err)
	}
	cur, err := api.Get(ctx)
	switch {
	case errors.Is(err, tailscale.ErrPolicyNotFound):
		cur = tailscale.Policy{Raw: "{}"}
	case errors.Is(err, tailscale.ErrAuth):
		out.Message = scopeMessage(sn, false)
		return out, nil
	case err != nil:
		out.Message = "Could not read the tailnet policy: " + shorten(err)
		return out, nil
	}
	pol := acl.Generate(sn.aclState())
	if out.Missing, err = acl.ContainsRules([]byte(cur.Raw), pol); err != nil {
		out.Message = shorten(err)
		return out, nil
	}
	if out.Stale, err = acl.StaleRules([]byte(cur.Raw), pol, sn.meshIPs()); err != nil {
		out.Message = shorten(err)
		return out, nil
	}
	out.Readable = true
	if out.Missing == nil {
		out.Missing = []acl.Rule{}
	}
	if out.Stale == nil {
		out.Stale = []acl.Rule{}
	}
	switch {
	case len(out.Missing) == 0 && len(out.Stale) == 0:
		out.Message = "The stored policy holds exactly the generated rules."
	default:
		out.Message = fmt.Sprintf("%d generated rule(s) missing, %d outdated NebulaCtrl rule(s) present.", len(out.Missing), len(out.Stale))
	}
	return out, nil
}
