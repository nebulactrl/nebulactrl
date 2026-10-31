package mesh

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/mesh/acl"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type serverInfo struct {
	ID     uuid.UUID
	Name   string
	Status string
	Facts  agentprotocol.Facts
}

type svcInfo struct {
	ID          uuid.UUID
	Slug        string
	Name        string
	EnvID       uuid.UUID
	ServerID    *uuid.UUID
	BlockEgress bool
}

// snapshot is the mesh state of one workspace, read once and then only
// interpreted (no database access after load).
type snapshot struct {
	ws        uuid.UUID
	set       db.MeshSetting
	integ     tailscale.Settings
	haveInteg bool
	servers   map[uuid.UUID]serverInfo
	nodes     map[uuid.UUID]db.ServerMesh
	links     []db.MeshLink
	exposures []db.MeshExposure
	grants    []db.MeshGrant
	svc       map[uuid.UUID]svcInfo
	// internalNet says, per link id, that its environment has the -int network on
	// the link's server (a service there blocks egress).
	internalNet map[uuid.UUID]bool
}

// load reads the whole mesh state of ws.
func (s *Service) load(ctx context.Context, q *db.Queries, ws uuid.UUID) (*snapshot, error) {
	sn := &snapshot{
		ws: ws, servers: map[uuid.UUID]serverInfo{}, nodes: map[uuid.UUID]db.ServerMesh{},
		svc: map[uuid.UUID]svcInfo{}, internalNet: map[uuid.UUID]bool{},
	}
	var err error
	if sn.set, err = s.settings(ctx, q, ws); err != nil {
		return nil, err
	}
	if in, err := s.ts.Settings(ctx, ws); err == nil {
		sn.integ, sn.haveInteg = in, true
	}
	srvs, err := q.ListMeshServerRowsOnline(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	for _, r := range srvs {
		sn.servers[r.ID] = serverInfo{ID: r.ID, Name: r.Name, Status: r.Status, Facts: factsRaw(r.Facts)}
	}
	nodes, err := q.ListServerMesh(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list server mesh: %w", err)
	}
	for _, n := range nodes {
		sn.nodes[n.ServerID] = n
	}
	if sn.links, err = q.ListMeshLinks(ctx, ws); err != nil {
		return nil, fmt.Errorf("list mesh links: %w", err)
	}
	for _, l := range sn.links {
		n, err := q.CountMeshServicesOnServerBlockedEgress(ctx, db.CountMeshServicesOnServerBlockedEgressParams{EnvironmentID: l.EnvironmentID, ServerID: &l.ServerID})
		if err != nil {
			return nil, fmt.Errorf("count egress-blocked services: %w", err)
		}
		sn.internalNet[l.ID] = n > 0
	}
	if sn.exposures, err = q.ListMeshExposures(ctx, ws); err != nil {
		return nil, fmt.Errorf("list mesh exposures: %w", err)
	}
	if sn.grants, err = q.ListMeshGrants(ctx, ws); err != nil {
		return nil, fmt.Errorf("list mesh grants: %w", err)
	}
	ids := map[uuid.UUID]bool{}
	for _, e := range sn.exposures {
		ids[e.ServiceID] = true
	}
	for _, g := range sn.grants {
		ids[g.RefID] = true
	}
	if len(ids) > 0 {
		list := make([]uuid.UUID, 0, len(ids))
		for id := range ids {
			list = append(list, id)
		}
		rows, err := q.ListMeshServicesBrief(ctx, list)
		if err != nil {
			return nil, fmt.Errorf("list services: %w", err)
		}
		for _, r := range rows {
			var c struct {
				BlockEgress bool `json:"blockEgress"`
			}
			_ = json.Unmarshal(r.Config, &c)
			sn.svc[r.ID] = svcInfo{ID: r.ID, Slug: r.Slug, Name: r.Name, EnvID: r.EnvironmentID, ServerID: r.ServerID, BlockEgress: c.BlockEgress}
		}
	}
	return sn, nil
}

func (sn *snapshot) meshTag() string {
	if sn.set.MeshTag == "" {
		return DefaultMeshTag
	}
	return sn.set.MeshTag
}

func (sn *snapshot) serverName(id uuid.UUID) string {
	if s, ok := sn.servers[id]; ok {
		return s.Name
	}
	return id.String()[:8]
}

// aclState builds the generator input from the tables.
func (sn *snapshot) aclState() acl.State {
	var st acl.State
	for _, n := range sn.nodes {
		st.Nodes = append(st.Nodes, acl.Node{ServerID: n.ServerID.String(), IPs: n.Ips})
	}
	for _, l := range sn.links {
		st.Links = append(st.Links, acl.Link{
			ID: l.ID.String(), EnvironmentID: l.EnvironmentID.String(), ServerID: l.ServerID.String(),
			RemoteServerID: l.RemoteServerID.String(), Purpose: l.Purpose, IPs: l.Ips,
			Label: fmt.Sprintf("%s: %s -> %s (%s)", agentprotocol.Short(l.EnvironmentID.String()), sn.serverName(l.ServerID), sn.serverName(l.RemoteServerID), l.Purpose),
		})
	}
	for _, e := range sn.exposures {
		st.Exposures = append(st.Exposures, acl.Exposure{
			ID: e.ID.String(), ServerID: e.ServerID.String(), ServiceID: e.ServiceID.String(),
			EnvironmentID: e.EnvironmentID.String(), Purpose: e.Purpose, MeshPort: int(e.MeshPort),
		})
	}
	for _, g := range sn.grants {
		st.Grants = append(st.Grants, acl.Grant{LinkID: g.LinkID.String(), ExposureID: g.ExposureID.String(), RefID: g.RefID.String(), Source: g.Source})
	}
	return st
}

// meshIPs is every IPv4 a mesh node currently has.
func (sn *snapshot) meshIPs() map[string]bool {
	out := map[string]bool{}
	for _, n := range sn.nodes {
		for _, ip := range n.Ips {
			out[ip] = true
		}
	}
	for _, l := range sn.links {
		for _, ip := range l.Ips {
			out[ip] = true
		}
	}
	return out
}

// devices is the number of tailnet devices the mesh uses: one per server node
// and one per link node.
func (sn *snapshot) devices() int { return len(sn.nodes) + len(sn.links) }

// desiredNodes derives the complete node set of one server from the tables.
// Provision is set only when provision is true and the node asked to be
// (re)provisioned; a link node whose target server node has no tailnet IP yet
// is not desired yet.
func (sn *snapshot) desiredNodes(serverID uuid.UUID, provision bool) []agentprotocol.MeshNodeSpec {
	out := []agentprotocol.MeshNodeSpec{}
	provider, login := sn.integ.Provider, sn.integ.LoginServer
	tag := sn.meshTag()
	if n, ok := sn.nodes[serverID]; ok {
		spec := agentprotocol.MeshNodeSpec{
			NodeID: serverID.String(), Kind: agentprotocol.MeshKindServer, Provider: provider, LoginServer: login,
			Hostname: n.Hostname, Tags: []string{tag}, IdentityHash: n.IdentityHash,
			Provision: provision && n.NeedsReprovision,
		}
		for _, e := range sn.exposures {
			if e.ServerID != serverID {
				continue
			}
			sv := sn.svc[e.ServiceID]
			spec.Exposures = append(spec.Exposures, agentprotocol.MeshExposure{
				ServiceID: e.ServiceID.String(), ServiceSlug: sv.Slug, EnvironmentID: e.EnvironmentID.String(), Purpose: e.Purpose,
				ContainerPort: int(e.ContainerPort), MeshPort: int(e.MeshPort), BlockEgress: sv.BlockEgress,
			})
		}
		sort.Slice(spec.Exposures, func(i, j int) bool { return spec.Exposures[i].MeshPort < spec.Exposures[j].MeshPort })
		out = append(out, spec)
	}
	for _, l := range sn.links {
		if l.ServerID != serverID {
			continue
		}
		remote, ok := sn.nodes[l.RemoteServerID]
		target := firstIPv4(remote.Ips)
		aliases := sn.linkAliases(l)
		if !ok || target == "" || len(aliases) == 0 {
			continue // not reachable yet, or no grant needs the link (the agent requires an alias)
		}
		out = append(out, agentprotocol.MeshNodeSpec{
			NodeID: l.ID.String(), Kind: agentprotocol.MeshKindLink, Provider: provider, LoginServer: login,
			Hostname: l.Hostname, Tags: []string{tag}, IdentityHash: l.IdentityHash, Provision: provision && l.NeedsReprovision,
			EnvironmentID: l.EnvironmentID.String(), RemoteServerID: l.RemoteServerID.String(), TargetIP: target,
			Aliases: aliases, HasInternalNet: sn.internalNet[l.ID],
		})
	}
	return out
}

// linkAliases are the names a link node answers to on the environment networks,
// derived from its grants: the provider slugs of a service link, or
// mesh-<remoteServerId8> of a replication link (only while a grant needs it).
func (sn *snapshot) linkAliases(l db.MeshLink) []string {
	exp := map[uuid.UUID]db.MeshExposure{}
	for _, e := range sn.exposures {
		exp[e.ID] = e
	}
	set := map[string]bool{}
	for _, g := range sn.grants {
		if g.LinkID != l.ID {
			continue
		}
		e, ok := exp[g.ExposureID]
		if !ok {
			continue
		}
		switch l.Purpose {
		case agentprotocol.MeshPurposeReplication:
			set[agentprotocol.MeshReplicationAlias(l.RemoteServerID.String())] = true
		default:
			if sv, ok := sn.svc[e.ServiceID]; ok && sv.Slug != "" {
				set[sv.Slug] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}
