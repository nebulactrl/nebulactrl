package mesh

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/mesh/acl"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ServerMeshPlan is the dry run of enabling the mesh on a server.
type ServerMeshPlan struct {
	DevicesAdded int       `json:"devicesAdded" doc:"Tailnet devices the action adds (tagged devices may be billed by Tailscale)"`
	TunDevice    bool      `json:"tunDevice" doc:"The host has /dev/net/tun. Server nodes do not need it; cross-server links (their consumer side) do"`
	Warnings     []string  `json:"warnings"`
	Blockers     []Blocker `json:"blockers"`
}

// planServer judges enabling the mesh on a server.
func (sn *snapshot) planServer(srv serverInfo) ServerMeshPlan {
	p := ServerMeshPlan{TunDevice: srv.Facts.TunDevice, Warnings: []string{}, Blockers: []Blocker{}}
	block := func(e *apierr.Error) { p.Blockers = append(p.Blockers, Blocker{Code: e.Code, Message: e.Message}) }
	if _, on := sn.nodes[srv.ID]; on {
		return p
	}
	p.DevicesAdded = 1
	switch {
	case !sn.haveInteg:
		block(apierr.MeshNotConfigured())
	case sn.set.Paused:
		block(apierr.MeshPaused(sn.set.PausedReason))
	case sn.set.CoordinatorOk != nil && !*sn.set.CoordinatorOk:
		block(apierr.MeshCoordinatorDown())
	}
	if !capable(srv.Facts) {
		block(apierr.MeshAgentTooOld(srv.Name))
	}
	if sn.haveInteg && len(sn.integ.AllowedTags) > 0 && !contains(sn.integ.AllowedTags, sn.meshTag()) {
		block(apierr.Unprocessable("mesh_tag_not_allowed", fmt.Sprintf("the mesh tag %s is not in the integration's allowed tags", sn.meshTag())))
	}
	if srv.Facts.OS != "" && !srv.Facts.TunDevice {
		p.Warnings = append(p.Warnings, "This host has no /dev/net/tun: it can join the mesh and be reached by other servers, but its own services cannot reach services on other servers (link nodes need the tun device).")
	}
	switch sn.set.AclMode {
	case ModeAdvisory:
		p.Warnings = append(p.Warnings, "ACLs are advisory: NebulaCtrl shows the access rules but does not write your tailnet policy.")
	case ModeOff:
		p.Warnings = append(p.Warnings, "ACL management is off: the tailnet's own policy decides what the mesh may reach.")
	}
	if sn.haveInteg && sn.integ.Provider == agentprotocol.TailnetProviderHeadscale && sn.integ.Credential == tailscale.HeadscalePreauth {
		p.Warnings = append(p.Warnings, "The Headscale credential is a pre-auth key: NebulaCtrl cannot delete devices when a node is removed, and the ACL is advisory.")
	}
	p.Warnings = append(p.Warnings, fmt.Sprintf("Adds 1 device; this team uses %d tagged mesh devices.", sn.devices()))
	return p
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ServerMeshInput changes the mesh of one server.
type ServerMeshInput struct {
	Enabled bool
	DryRun  bool
	// AcceptMeshChanges confirms that the action adds tailnet devices.
	AcceptMeshChanges bool
}

// ServerMeshResult is the answer of SetServerMesh.
type ServerMeshResult struct {
	Plan ServerMeshPlan `json:"plan"`
	// Applied is false for a dry run.
	Applied bool `json:"applied"`
	Enabled bool `json:"enabled"`
}

// SetServerMesh enables or disables the mesh on a server (dry run: only the
// plan). Enabling adds one tagged device and needs acceptMeshChanges.
func (s *Service) SetServerMesh(ctx context.Context, serverID uuid.UUID, in ServerMeshInput) (ServerMeshResult, error) {
	srv, err := s.loadServer(ctx, s.q, policy.ScopeServersWrite, serverID)
	if err != nil {
		return ServerMeshResult{}, err
	}
	sn, err := s.load(ctx, s.q, srv.WorkspaceID)
	if err != nil {
		return ServerMeshResult{}, err
	}
	info := serverInfo{ID: srv.ID, Name: srv.Name, Status: srv.Status, Facts: factsOf(srv)}
	_, on := sn.nodes[serverID]
	if in.Enabled {
		plan := sn.planServer(info)
		res := ServerMeshResult{Plan: plan, Enabled: on}
		if in.DryRun {
			res.Enabled = on
			return res, nil
		}
		if on {
			res.Applied = true
			return res, nil
		}
		if len(plan.Blockers) > 0 {
			b := plan.Blockers[0]
			return ServerMeshResult{}, apierr.Unprocessable(b.Code, b.Message)
		}
		if !in.AcceptMeshChanges {
			return ServerMeshResult{}, apierr.MeshConfirmationRequired(plan.DevicesAdded)
		}
		if err := s.enableServer(ctx, sn, srv); err != nil {
			return ServerMeshResult{}, err
		}
		res.Applied, res.Enabled = true, true
		return res, nil
	}
	// Disable.
	res := ServerMeshResult{Plan: ServerMeshPlan{TunDevice: info.Facts.TunDevice, Warnings: []string{}, Blockers: []Blocker{}}, Enabled: on}
	if !on {
		res.Applied = !in.DryRun
		return res, nil
	}
	if why := sn.inUse(serverID); why != "" {
		e := apierr.Conflict("mesh_server_in_use", why)
		if in.DryRun {
			res.Plan.Blockers = append(res.Plan.Blockers, Blocker{Code: e.Code, Message: e.Message})
			return res, nil
		}
		return ServerMeshResult{}, e
	}
	if in.DryRun {
		return res, nil
	}
	if err := s.disableServer(ctx, sn, srv); err != nil {
		return ServerMeshResult{}, err
	}
	res.Applied, res.Enabled = true, false
	return res, nil
}

// inUse says why a server's mesh cannot be turned off ("" when it can): links
// and exposures that replicas or cross-server links depend on.
func (sn *snapshot) inUse(serverID uuid.UUID) string {
	n := 0
	for _, l := range sn.links {
		if l.ServerID == serverID || l.RemoteServerID == serverID {
			n++
		}
	}
	e := 0
	for _, x := range sn.exposures {
		if x.ServerID == serverID {
			e++
		}
	}
	if n+e == 0 {
		return ""
	}
	return fmt.Sprintf("%d link(s) and %d exposed service(s) use this server's mesh node; remove the replicas and cross-server references that need them first", n, e)
}

// enableServer creates the server node and sends the first sync (with a key).
func (s *Service) enableServer(ctx context.Context, sn *snapshot, srv db.Server) error {
	host := serverHostname(srv.Name)
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if !sn.set.Enabled {
			// Settings reads return the table defaults when no row exists
			// yet, so the first write creates the row before updating it.
			if err := q.EnsureMeshSettings(ctx, srv.WorkspaceID); err != nil {
				return err
			}
			if _, err := q.UpdateMeshSettings(ctx, db.UpdateMeshSettingsParams{WorkspaceID: srv.WorkspaceID, Enabled: new(true)}); err != nil {
				return err
			}
			if err := audit.Record(ctx, tx, audit.Entry{Action: "mesh.enable", ResourceType: "mesh", ResourceID: srv.WorkspaceID.String(), WorkspaceID: srv.WorkspaceID, Metadata: map[string]any{}}); err != nil {
				return err
			}
		}
		if _, err := q.UpsertServerMeshRow(ctx, db.UpsertServerMeshRowParams{
			ServerID: srv.ID, WorkspaceID: srv.WorkspaceID, Hostname: host,
			IdentityHash: identityHash(sn.integ.Provider, sn.integ.LoginServer, host, sn.meshTag()),
		}); err != nil {
			return err
		}
		fresh, err := s.load(ctx, q, srv.WorkspaceID)
		if err != nil {
			return err
		}
		if err := s.enqueueSync(ctx, tx, srv.ID, fresh.desiredNodes(srv.ID, true)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "mesh.node.provision", ResourceType: "server", ResourceID: srv.ID.String(), WorkspaceID: srv.WorkspaceID,
			Metadata: map[string]any{"server": srv.Name, "hostname": host},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return err
		}
		return fmt.Errorf("enable server mesh: %w", err)
	}
	s.publish(srv.WorkspaceID, srv.ID)
	return nil
}

// disableServer removes the server node and, through the complete empty set the
// agent receives, every mesh container of the server.
func (s *Service) disableServer(ctx context.Context, sn *snapshot, srv db.Server) error {
	n := sn.nodes[srv.ID]
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.DeleteServerMesh(ctx, srv.ID); err != nil {
			return err
		}
		if err := s.enqueueSync(ctx, tx, srv.ID, nil); err != nil {
			return err
		}
		if err := s.ts.EnqueueDeleteNode(ctx, tx, srv.WorkspaceID, tailscale.NodeRef{DeviceID: n.DeviceID, IPs: n.Ips, Hostname: n.Hostname}); err != nil {
			return err
		}
		s.nudgeACL(ctx, tx, srv.WorkspaceID)
		return audit.Record(ctx, tx, audit.Entry{
			Action: "mesh.node.remove", ResourceType: "server", ResourceID: srv.ID.String(), WorkspaceID: srv.WorkspaceID,
			Metadata: map[string]any{"server": srv.Name},
		})
	})
	if err != nil {
		return fmt.Errorf("disable server mesh: %w", err)
	}
	if n.AuthKeyID != "" {
		if err := s.ts.RevokeNodeKey(ctx, srv.WorkspaceID, n.AuthKeyID); err != nil {
			s.log.WarnContext(ctx, "could not revoke the key of a removed mesh node", "server_id", srv.ID.String(), "err", err)
		}
	}
	s.publish(srv.WorkspaceID, srv.ID)
	return nil
}

// ReprovisionServer re-creates the server node: a fresh single-use key, a fresh
// state volume and a new device with a new tailnet IP (the ACL regenerates).
func (s *Service) ReprovisionServer(ctx context.Context, serverID uuid.UUID) error {
	srv, err := s.loadServer(ctx, s.q, policy.ScopeServersWrite, serverID)
	if err != nil {
		return err
	}
	if srv.Status == "revoked" {
		return apierr.Unprocessable("server_revoked", "the server was revoked")
	}
	sn, err := s.load(ctx, s.q, srv.WorkspaceID)
	if err != nil {
		return err
	}
	if _, ok := sn.nodes[serverID]; !ok {
		return apierr.Unprocessable("not_applicable", "the server has no mesh node to re-provision")
	}
	if sn.set.Paused {
		return apierr.MeshPaused(sn.set.PausedReason)
	}
	if !capable(factsOf(srv)) {
		return apierr.MeshAgentTooOld(srv.Name)
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := q.SetServerMeshProvisioning(ctx, serverID); err != nil {
			return err
		}
		fresh, err := s.load(ctx, q, srv.WorkspaceID)
		if err != nil {
			return err
		}
		if err := s.enqueueSync(ctx, tx, serverID, fresh.desiredNodes(serverID, true)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "mesh.node.provision", ResourceType: "server", ResourceID: serverID.String(), WorkspaceID: srv.WorkspaceID,
			Metadata: map[string]any{"server": srv.Name, "reprovision": true},
		})
	})
	if err != nil {
		return fmt.Errorf("reprovision server mesh: %w", err)
	}
	s.publish(srv.WorkspaceID, serverID)
	return nil
}

// ReprovisionLink re-creates a link node (a new device, a new tailnet IP).
func (s *Service) ReprovisionLink(ctx context.Context, linkID uuid.UUID) error {
	if err := policy.Require(ctx, policy.ScopeServersWrite, nil); err != nil {
		return err
	}
	l, err := s.q.GetMeshLink(ctx, db.GetMeshLinkParams{ID: linkID, WorkspaceID: policy.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return apierr.NotFound("mesh link")
	}
	if err != nil {
		return err
	}
	es, err := services.LoadEnvironment(ctx, s.q, policy.ScopeServersWrite, l.EnvironmentID)
	if err != nil {
		return err
	}
	if err := es.Guard(ctx, "reprovisionMeshLink", fmt.Sprintf("Re-create a mesh link node of the production environment %s", es.Env.Name), false,
		[]string{"The link node leaves the tailnet and joins again with a new device and tailnet IP; traffic over it is interrupted until it is back and the access rules regenerate."}, linkID); err != nil {
		return err
	}
	set, err := s.settings(ctx, s.q, l.WorkspaceID)
	if err != nil {
		return err
	}
	if set.Paused {
		return apierr.MeshPaused(set.PausedReason)
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := q.SetMeshLinkProvisioning(ctx, linkID); err != nil {
			return err
		}
		fresh, err := s.load(ctx, q, l.WorkspaceID)
		if err != nil {
			return err
		}
		if err := s.enqueueSync(ctx, tx, l.ServerID, fresh.desiredNodes(l.ServerID, true)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "mesh.node.provision", ResourceType: "mesh_link", ResourceID: linkID.String(), WorkspaceID: l.WorkspaceID,
			Metadata: map[string]any{"hostname": l.Hostname, "reprovision": true},
		})
	})
	if err != nil {
		return fmt.Errorf("reprovision mesh link: %w", err)
	}
	s.publish(l.WorkspaceID, l.ServerID)
	return nil
}

// ServerCheckResult is one line of a server connectivity check.
type ServerCheckResult struct {
	PeerServerID *uuid.UUID `json:"peerServerId,omitempty"`
	Outcome      string     `json:"outcome" enum:"connected,refused,timeout,no_path"`
	Message      string     `json:"message,omitempty"`
	LatencyMs    int        `json:"latencyMs,omitempty"`
}

// CheckServer asks a server's agent whether its node reaches the peers' nodes
// (every other mesh server, or the one named).
func (s *Service) CheckServer(ctx context.Context, serverID uuid.UUID, peer *uuid.UUID) ([]ServerCheckResult, error) {
	srv, err := s.loadServer(ctx, s.q, policy.ScopeServersWrite, serverID)
	if err != nil {
		return nil, err
	}
	sn, err := s.load(ctx, s.q, srv.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if _, ok := sn.nodes[serverID]; !ok {
		return nil, apierr.MeshServerRequired(srv.Name)
	}
	var peers []uuid.UUID
	if peer != nil {
		if _, ok := sn.nodes[*peer]; !ok {
			return nil, apierr.NotFound("peer server mesh node")
		}
		peers = []uuid.UUID{*peer}
	} else {
		for id := range sn.nodes {
			if id != serverID {
				peers = append(peers, id)
			}
		}
		sortUUIDs(peers)
	}
	out := []ServerCheckResult{}
	if len(peers) == 0 {
		res, err := s.runCheck(ctx, serverID, agentprotocol.MeshCheckPayload{})
		if err != nil {
			return nil, err
		}
		return append(out, ServerCheckResult{Outcome: res.Result, Message: res.Message, LatencyMs: res.LatencyMs}), nil
	}
	for _, p := range peers {
		res, err := s.runCheck(ctx, serverID, agentprotocol.MeshCheckPayload{PeerNodeID: p.String()})
		if err != nil {
			return nil, err
		}
		pid := p
		out = append(out, ServerCheckResult{PeerServerID: &pid, Outcome: res.Result, Message: res.Message, LatencyMs: res.LatencyMs})
	}
	return out, nil
}

func sortUUIDs(ids []uuid.UUID) {
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
}

// SettingsInput changes the team's mesh settings. nil leaves a field alone.
type SettingsInput struct {
	Enabled  *bool
	AutoJoin *bool
	ACLMode  *string
	MeshTag  *string
}

// UpdateSettings changes the team's mesh settings. Turning the mesh off is
// refused while any server still has a node. Changing the ACL mode or the mesh
// tag needs the admin scope; switching Tailscale to managed needs the OAuth
// client's policy_file scope and is audited.
func (s *Service) UpdateSettings(ctx context.Context, in SettingsInput) (View, error) {
	need := policy.ScopeServersWrite
	if in.ACLMode != nil || in.MeshTag != nil {
		need = policy.ScopeAdmin
	}
	if err := policy.Require(ctx, need, nil); err != nil {
		return View{}, err
	}
	ws := policy.WorkspaceID(ctx)
	sn, err := s.load(ctx, s.q, ws)
	if err != nil {
		return View{}, err
	}
	if in.Enabled != nil && !*in.Enabled && (len(sn.nodes) > 0 || len(sn.links) > 0) {
		return View{}, apierr.Conflict("mesh_in_use", "servers still have mesh nodes; turn the mesh off on each server first")
	}
	if in.MeshTag != nil {
		t := *in.MeshTag
		if t == "" {
			t = DefaultMeshTag
		}
		if !tailscaleTag(t) {
			return View{}, apierr.Unprocessable("invalid_tag", "meshTag must be tag:name with letters, digits and hyphens")
		}
		if len(sn.nodes) > 0 || len(sn.links) > 0 {
			return View{}, apierr.Conflict("mesh_in_use", "the mesh tag cannot change while nodes exist; re-provisioning would be needed for every node")
		}
		if sn.haveInteg && !contains(sn.integ.AllowedTags, t) {
			return View{}, apierr.Unprocessable("mesh_tag_not_allowed", fmt.Sprintf("the mesh tag %s is not in the integration's allowed tags", t))
		}
		in.MeshTag = &t
	}
	enableManagedTS := false
	if in.ACLMode != nil {
		switch *in.ACLMode {
		case ModeManaged, ModeAdvisory, ModeOff:
		default:
			return View{}, apierr.BadRequest("invalid_acl_mode", "aclMode must be managed, advisory or off")
		}
		if *in.ACLMode == ModeManaged {
			if !sn.haveInteg {
				return View{}, apierr.MeshNotConfigured()
			}
			if !sn.canWritePolicy() {
				return View{}, apierr.Unprocessable(apierr.CodeMeshACLModeUnsupported, "the Headscale credential is a pre-auth key: it cannot write the policy; connect an API key")
			}
			if shared, err := s.ts.SharedTailnet(ctx, ws); err == nil && shared {
				return View{}, apierr.Conflict(apierr.CodeMeshACLSharedTailnet, "another team uses this tailnet: its policy cannot be managed from here")
			}
			if !sn.isHeadscale() && sn.set.AclMode != ModeManaged {
				// Opt-in: prove the OAuth client can read and write the policy before
				// anything is stored. The read is also the first step of every push.
				api, err := s.ts.PolicyAPI(ctx, ws)
				if err != nil {
					return View{}, err
				}
				cur, err := api.Get(ctx)
				if err != nil {
					return View{}, s.errPolicy(err, sn)
				}
				next, perr := acl.PatchHuJSON([]byte(cur.Raw), acl.Generate(sn.aclState()), sn.meshTag(), tailscaleTagOwners, sn.meshIPs())
				if perr != nil {
					return View{}, apierr.Unprocessable("invalid_policy", perr.Error())
				}
				// Validation needs only policy_file:read, so it does not prove the push
				// would be allowed. Save the document just read again, guarded by its
				// ETag (identical content: nothing changes): a missing write scope is
				// refused now instead of at the first push.
				if err := api.Check(ctx, next); err != nil {
					return View{}, s.errPolicy(err, sn)
				}
				if _, err := api.Set(ctx, []byte(cur.Raw), cur.Version); err != nil {
					if errors.Is(err, tailscale.ErrPolicyConflict) {
						return View{}, apierr.Conflict("policy_changed", "the tailnet policy changed while it was checked; try again")
					}
					return View{}, s.errPolicy(err, sn)
				}
				enableManagedTS = true
			}
		}
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := q.EnsureMeshSettings(ctx, ws); err != nil {
			return err
		}
		if _, err := q.UpdateMeshSettings(ctx, db.UpdateMeshSettingsParams{WorkspaceID: ws, Enabled: in.Enabled, AutoJoin: in.AutoJoin, MeshTag: in.MeshTag, AclMode: in.ACLMode}); err != nil {
			return err
		}
		meta := map[string]any{}
		if in.Enabled != nil {
			meta["enabled"] = *in.Enabled
		}
		if in.AutoJoin != nil {
			meta["autoJoin"] = *in.AutoJoin
		}
		if in.ACLMode != nil {
			meta["aclMode"] = *in.ACLMode
		}
		if in.MeshTag != nil {
			meta["meshTag"] = *in.MeshTag
		}
		action := "mesh.settings.update"
		switch {
		case in.Enabled != nil && *in.Enabled && !sn.set.Enabled:
			action = "mesh.enable"
		case in.Enabled != nil && !*in.Enabled && sn.set.Enabled:
			action = "mesh.disable"
		}
		if err := audit.Record(ctx, tx, audit.Entry{Action: action, ResourceType: "mesh", ResourceID: ws.String(), Metadata: meta}); err != nil {
			return err
		}
		if enableManagedTS {
			if err := audit.Record(ctx, tx, audit.Entry{Action: "mesh.acl.managed.enable", ResourceType: "mesh", ResourceID: ws.String(), Metadata: map[string]any{"provider": "tailscale"}}); err != nil {
				return err
			}
		}
		if in.ACLMode != nil {
			s.nudgeACL(ctx, tx, ws)
		}
		return nil
	})
	if err != nil {
		return View{}, fmt.Errorf("update mesh settings: %w", err)
	}
	s.publish(ws)
	return s.View(ctx)
}
