package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func validPurpose(p string) bool {
	return p == agentprotocol.MeshPurposeReplication || p == agentprotocol.MeshPurposeService
}

// sourceFits reports whether a grant source belongs to a link purpose: replicas
// use replication links, references and canvas edges use service links.
func sourceFits(source, purpose string) bool {
	switch source {
	case "replica":
		return purpose == agentprotocol.MeshPurposeReplication
	case "reference", "edge":
		return purpose == agentprotocol.MeshPurposeService
	}
	return false
}

func exposureView(e db.MeshExposure) Exposure {
	return Exposure{ID: e.ID, ServerID: e.ServerID, ServiceID: e.ServiceID, EnvironmentID: e.EnvironmentID, Purpose: e.Purpose, ContainerPort: int(e.ContainerPort), MeshPort: int(e.MeshPort)}
}

func linkView(l db.MeshLink) Link {
	return Link{ID: l.ID, EnvironmentID: l.EnvironmentID, ServerID: l.ServerID, RemoteServerID: l.RemoteServerID, Purpose: l.Purpose, State: l.State}
}

// lockServer serializes the mesh-port allocation of one server node.
func lockServer(ctx context.Context, tx pgx.Tx, serverID uuid.UUID) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "mesh:"+serverID.String())
	return err
}

// PlanLink implements Provider.
func (s *Service) PlanLink(ctx context.Context, q *db.Queries, env, consumerSrv, providerSrv uuid.UUID, purpose string) (LinkPlan, error) {
	plan := LinkPlan{Blockers: []Blocker{}, Warnings: []string{}}
	if !validPurpose(purpose) {
		return plan, apierr.BadRequest("invalid_purpose", "purpose must be replication or service")
	}
	e, err := q.GetMeshEnvironment(ctx, env)
	if err != nil {
		return plan, fmt.Errorf("get environment: %w", err)
	}
	sn, err := s.load(ctx, q, e.WorkspaceID)
	if err != nil {
		return plan, err
	}
	block := func(code, msg string) { plan.Blockers = append(plan.Blockers, Blocker{Code: code, Message: msg}) }
	if !sn.haveInteg {
		block(apierr.CodeMeshNotConfigured, apierr.MeshNotConfigured().Message)
	}
	if sn.set.Paused {
		block(apierr.CodeMeshPaused, apierr.MeshPaused(sn.set.PausedReason).Message)
	}
	if sn.set.CoordinatorOk != nil && !*sn.set.CoordinatorOk {
		block(apierr.CodeMeshCoordinatorDown, apierr.MeshCoordinatorDown().Message)
	}
	for _, id := range []uuid.UUID{consumerSrv, providerSrv} {
		si, ok := sn.servers[id]
		if !ok {
			block("not_found", "server not found")
			continue
		}
		if !capable(si.Facts) {
			block(apierr.CodeMeshAgentTooOld, apierr.MeshAgentTooOld(si.Name).Message)
		}
		if _, ok := sn.nodes[id]; !ok {
			block(apierr.CodeMeshServerRequired, apierr.MeshServerRequired(si.Name).Message)
		}
	}
	if si, ok := sn.servers[consumerSrv]; ok && !si.Facts.TunDevice && si.Facts.OS != "" {
		block(apierr.CodeMeshNoTun, apierr.MeshNoTun(si.Name).Message)
	}
	for _, l := range sn.links {
		if l.EnvironmentID == env && l.ServerID == consumerSrv && l.RemoteServerID == providerSrv && l.Purpose == purpose {
			plan.LinkExists = true
		}
	}
	if !plan.LinkExists {
		plan.DevicesAdded = 1
		if sn.set.AclMode == ModeAdvisory {
			plan.Warnings = append(plan.Warnings, "The tailnet policy is advisory: add the generated access rule to it, or the link stays unreachable.")
		}
	}
	return plan, nil
}

// EnsureExposure implements Provider.
func (s *Service) EnsureExposure(ctx context.Context, tx pgx.Tx, serviceID uuid.UUID, purpose string, containerPort int) (Exposure, error) {
	q := db.New(tx)
	if !validPurpose(purpose) {
		return Exposure{}, apierr.BadRequest("invalid_purpose", "purpose must be replication or service")
	}
	sc, err := q.GetMeshServiceScope(ctx, serviceID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && sc.DeletedAt != nil) {
		return Exposure{}, apierr.NotFound("service")
	}
	if err != nil {
		return Exposure{}, fmt.Errorf("get service: %w", err)
	}
	switch {
	case sc.Kind == "route" || sc.Kind == "stack":
		return Exposure{}, apierr.MeshNotExposable("route and Compose stack services have no single TCP port")
	case containerPort < 1 || containerPort > 65535:
		return Exposure{}, apierr.MeshNotExposable("the service has no TCP port")
	case sc.ServerID == nil:
		return Exposure{}, apierr.MeshNotExposable("the service is not placed on a server yet")
	}
	server := *sc.ServerID
	if err := lockServer(ctx, tx, server); err != nil {
		return Exposure{}, err
	}
	if _, err := q.GetServerMesh(ctx, server); errors.Is(err, pgx.ErrNoRows) {
		name := server.String()[:8]
		if srv, e := q.GetServerByID(ctx, server); e == nil {
			name = srv.Name
		}
		return Exposure{}, apierr.MeshServerRequired(name)
	} else if err != nil {
		return Exposure{}, err
	}
	existing, err := q.GetMeshExposureByService(ctx, db.GetMeshExposureByServiceParams{ServiceID: serviceID, Purpose: purpose})
	switch {
	case err == nil && existing.ServerID == server:
		return exposureView(existing), nil
	case err == nil:
		// The service moved: release the old exposure (its grants go with it).
		if _, err := q.DeleteMeshExposure(ctx, existing.ID); err != nil {
			return Exposure{}, err
		}
		s.nudgeSync(ctx, tx, existing.ServerID, true)
	case !errors.Is(err, pgx.ErrNoRows):
		return Exposure{}, err
	}
	used, err := q.ListMeshExposuresUsedPorts(ctx, server)
	if err != nil {
		return Exposure{}, fmt.Errorf("list used mesh ports: %w", err)
	}
	port, ok := allocPort(purpose, containerPort, used)
	if !ok {
		return Exposure{}, apierr.MeshNotExposable("no mesh port is free on the server node")
	}
	row, err := q.InsertMeshExposure(ctx, db.InsertMeshExposureParams{
		WorkspaceID: sc.WorkspaceID, ServerID: server, ServiceID: serviceID, EnvironmentID: sc.EnvironmentID, Purpose: purpose,
		ContainerPort: int32(containerPort), MeshPort: int32(port),
	})
	if err != nil {
		return Exposure{}, fmt.Errorf("insert mesh exposure: %w", err)
	}
	s.nudgeSync(ctx, tx, server, true)
	return exposureView(row), nil
}

// allocPort chooses the mesh port: a service exposure keeps the container port
// when it is free on the node, everything else takes the first free port of
// 15000-15999.
func allocPort(purpose string, containerPort int, used []int32) (int, bool) {
	taken := map[int]bool{}
	for _, u := range used {
		taken[int(u)] = true
	}
	if purpose == agentprotocol.MeshPurposeService && !taken[containerPort] {
		return containerPort, true
	}
	for p := agentprotocol.MeshPortMin; p <= agentprotocol.MeshPortMax; p++ {
		if !taken[p] {
			return p, true
		}
	}
	return 0, false
}

// EnsureLink implements Provider.
func (s *Service) EnsureLink(ctx context.Context, tx pgx.Tx, env, localSrv, remoteSrv uuid.UUID, purpose string) (Link, error) {
	q := db.New(tx)
	if !validPurpose(purpose) {
		return Link{}, apierr.BadRequest("invalid_purpose", "purpose must be replication or service")
	}
	if localSrv == remoteSrv {
		return Link{}, apierr.BadRequest("invalid_link", "a link joins two different servers")
	}
	e, err := q.GetMeshEnvironment(ctx, env)
	if errors.Is(err, pgx.ErrNoRows) {
		return Link{}, apierr.NotFound("environment")
	}
	if err != nil {
		return Link{}, err
	}
	if existing, err := q.GetMeshLinkByKey(ctx, db.GetMeshLinkByKeyParams{EnvironmentID: env, ServerID: localSrv, RemoteServerID: remoteSrv, Purpose: purpose}); err == nil {
		return linkView(existing), nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Link{}, err
	}
	var names [2]string
	for i, id := range []uuid.UUID{localSrv, remoteSrv} {
		srv, err := q.GetServerByID(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && srv.WorkspaceID != e.WorkspaceID) {
			return Link{}, apierr.NotFound("server")
		}
		if err != nil {
			return Link{}, err
		}
		if _, err := q.GetServerMesh(ctx, id); errors.Is(err, pgx.ErrNoRows) {
			return Link{}, apierr.MeshServerRequired(srv.Name)
		} else if err != nil {
			return Link{}, err
		}
		names[i] = srv.Name
	}
	set, err := s.settings(ctx, q, e.WorkspaceID)
	if err != nil {
		return Link{}, err
	}
	if set.Paused {
		return Link{}, apierr.MeshPaused(set.PausedReason)
	}
	integ, err := s.integration(ctx, e.WorkspaceID)
	if err != nil {
		return Link{}, err
	}
	host := linkHostname(env.String(), names[0], names[1])
	row, err := q.InsertMeshLink(ctx, db.InsertMeshLinkParams{
		WorkspaceID: e.WorkspaceID, EnvironmentID: env, ServerID: localSrv, RemoteServerID: remoteSrv, Purpose: purpose, Hostname: host,
		IdentityHash: identityHash(integ.Provider, integ.LoginServer, host, set.MeshTag),
	})
	if err != nil {
		return Link{}, fmt.Errorf("insert mesh link: %w", err)
	}
	if err := q.SetMeshLinkProvisioning(ctx, row.ID); err != nil {
		return Link{}, err
	}
	row.State = "provisioning"
	s.nudgeSync(ctx, tx, localSrv, false)
	return linkView(row), nil
}

// Grant implements Provider.
func (s *Service) Grant(ctx context.Context, tx pgx.Tx, linkID, exposureID, refID uuid.UUID, source string) error {
	q := db.New(tx)
	l, err := q.GetMeshLinkByID(ctx, linkID)
	if errors.Is(err, pgx.ErrNoRows) {
		return apierr.NotFound("mesh link")
	}
	if err != nil {
		return err
	}
	e, err := q.GetMeshExposureByID(ctx, exposureID)
	if errors.Is(err, pgx.ErrNoRows) {
		return apierr.NotFound("mesh exposure")
	}
	if err != nil {
		return err
	}
	switch {
	case l.EnvironmentID != e.EnvironmentID:
		return apierr.Unprocessable(apierr.CodeMeshEnvironmentMismatch, "a link of one environment can only reach exposures of the same environment")
	case l.Purpose != e.Purpose || !sourceFits(source, l.Purpose) || e.ServerID != l.RemoteServerID:
		return apierr.Unprocessable(apierr.CodeMeshPurposeMismatch, "the link and the exposure do not belong together (purpose or server)")
	}
	if err := q.InsertMeshGrant(ctx, db.InsertMeshGrantParams{LinkID: linkID, ExposureID: exposureID, RefID: refID, Source: source}); err != nil {
		return fmt.Errorf("insert mesh grant: %w", err)
	}
	s.nudgeSync(ctx, tx, l.ServerID, false) // the link's aliases changed
	s.nudgeACL(ctx, tx, l.WorkspaceID)
	return nil
}

// Revoke implements Provider.
func (s *Service) Revoke(ctx context.Context, tx pgx.Tx, linkID, exposureID, refID uuid.UUID, source string) error {
	q := db.New(tx)
	if _, err := q.DeleteMeshGrant(ctx, db.DeleteMeshGrantParams{LinkID: linkID, ExposureID: exposureID, RefID: refID, Source: source}); err != nil {
		return fmt.Errorf("delete mesh grant: %w", err)
	}
	return s.cascade(ctx, tx, linkID, exposureID)
}

// Release implements Provider.
func (s *Service) Release(ctx context.Context, tx pgx.Tx, refID uuid.UUID) error {
	q := db.New(tx)
	rows, err := q.DeleteMeshGrantsOfRef(ctx, refID)
	if err != nil {
		return fmt.Errorf("delete mesh grants of ref: %w", err)
	}
	for _, r := range rows {
		if err := s.cascade(ctx, tx, r.LinkID, r.ExposureID); err != nil {
			return err
		}
	}
	return nil
}

// cascade releases a link without grants (node and device go) and an exposure
// without grants (the serve forward goes), then regenerates the ACL.
func (s *Service) cascade(ctx context.Context, tx pgx.Tx, linkID, exposureID uuid.UUID) error {
	q := db.New(tx)
	var ws uuid.UUID
	if l, err := q.GetMeshLinkByID(ctx, linkID); err == nil {
		ws = l.WorkspaceID
		n, err := q.CountMeshGrantsOfLink(ctx, linkID)
		if err != nil {
			return err
		}
		if n == 0 {
			if err := s.dropLink(ctx, tx, l); err != nil {
				return err
			}
		} else {
			s.nudgeSync(ctx, tx, l.ServerID, false)
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if e, err := q.GetMeshExposureByID(ctx, exposureID); err == nil {
		ws = e.WorkspaceID
		n, err := q.CountMeshGrantsOfExposure(ctx, exposureID)
		if err != nil {
			return err
		}
		if n == 0 {
			if _, err := q.DeleteMeshExposure(ctx, exposureID); err != nil {
				return err
			}
			s.nudgeSync(ctx, tx, e.ServerID, true)
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if ws != uuid.Nil {
		s.nudgeACL(ctx, tx, ws)
	}
	return nil
}

// dropLink forgets a link, removes its node from the agent (the forced sync
// removes the stray) and its device from the tailnet.
func (s *Service) dropLink(ctx context.Context, tx pgx.Tx, l db.MeshLink) error {
	q := db.New(tx)
	if _, err := q.DeleteMeshLink(ctx, l.ID); err != nil {
		return fmt.Errorf("delete mesh link: %w", err)
	}
	if err := s.ts.EnqueueDeleteNode(ctx, tx, l.WorkspaceID, tailscale.NodeRef{DeviceID: l.DeviceID, IPs: l.Ips, Hostname: l.Hostname}); err != nil {
		return fmt.Errorf("queue link device removal: %w", err)
	}
	s.nudgeSync(ctx, tx, l.ServerID, true)
	return nil
}

// aclOK reports whether the ACL state lets traffic through: applied, advisory
// (the connectivity check decides), or ACL management off.
func aclOK(set db.MeshSetting) bool {
	return set.AclMode == ModeOff || set.AclState == StateApplied || set.AclState == StateAdvisory
}

// Ready implements Provider.
func (s *Service) Ready(ctx context.Context, linkID uuid.UUID) (Readiness, error) {
	l, err := s.q.GetMeshLinkByID(ctx, linkID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Readiness{Reason: "the link does not exist"}, nil
	}
	if err != nil {
		return Readiness{}, err
	}
	set, err := s.settings(ctx, s.q, l.WorkspaceID)
	if err != nil {
		return Readiness{}, err
	}
	r := Readiness{Online: l.State == "online" && l.ObservedAt != nil && s.now().Sub(*l.ObservedAt) < observedStale, ACLOK: aclOK(set)}
	switch {
	case set.Paused:
		r.Reason = "the mesh is paused: " + set.PausedReason
	case !r.Online && l.State == "online":
		r.Reason = "the link node has not reported for a while"
	case !r.Online:
		r.Reason = "the link node is " + l.State
		if l.Message != "" {
			r.Reason += ": " + l.Message
		}
	case !r.ACLOK:
		r.Reason = "the access rules are not applied yet (" + set.AclState + ")"
		if set.AclMessage != "" {
			r.Reason += ": " + set.AclMessage
		}
	}
	return r, nil
}

// checkTimeout bounds a connectivity check.
const checkTimeout = 30 * time.Second

// Check implements Provider.
func (s *Service) Check(ctx context.Context, linkID, exposureID uuid.UUID) (CheckResult, error) {
	l, err := s.q.GetMeshLinkByID(ctx, linkID)
	if errors.Is(err, pgx.ErrNoRows) {
		return CheckResult{}, apierr.NotFound("mesh link")
	}
	if err != nil {
		return CheckResult{}, err
	}
	e, err := s.q.GetMeshExposureByID(ctx, exposureID)
	if errors.Is(err, pgx.ErrNoRows) {
		return CheckResult{}, apierr.NotFound("mesh exposure")
	}
	if err != nil {
		return CheckResult{}, err
	}
	res, err := s.runCheck(ctx, l.ServerID, agentprotocol.MeshCheckPayload{LinkID: linkID.String(), Port: int(e.MeshPort)})
	if err != nil {
		return CheckResult{}, err
	}
	return CheckResult{Result: res.Result, Message: res.Message}, nil
}

// runCheck sends a mesh_check command and waits for its result.
func (s *Service) runCheck(ctx context.Context, serverID uuid.UUID, p agentprotocol.MeshCheckPayload) (agentprotocol.MeshCheckResult, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return agentprotocol.MeshCheckResult{}, err
	}
	var id uuid.UUID
	if err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		id, err = s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: serverID, Kind: agentprotocol.KindMeshCheck, Payload: raw})
		return err
	}); err != nil {
		return agentprotocol.MeshCheckResult{}, fmt.Errorf("enqueue mesh_check: %w", err)
	}
	cmd, err := s.awaitCommand(ctx, id, checkTimeout)
	if err != nil {
		return agentprotocol.MeshCheckResult{Result: agentprotocol.MeshCheckTimeout, Message: err.Error()}, nil
	}
	var fr agentprotocol.ResultFrame
	_ = json.Unmarshal(cmd.Result, &fr)
	if fr.Outcome != agentprotocol.OutcomeSucceeded {
		msg := fr.Error
		if msg == "" {
			msg = cmd.Error
		}
		return agentprotocol.MeshCheckResult{Result: agentprotocol.MeshCheckNoPath, Message: msg}, nil
	}
	var res agentprotocol.MeshCheckResult
	if err := json.Unmarshal(fr.Data, &res); err != nil {
		return agentprotocol.MeshCheckResult{Result: agentprotocol.MeshCheckNoPath, Message: "unreadable result"}, nil
	}
	return res, nil
}

// awaitCommand polls a command until it finished, expired or was canceled.
func (s *Service) awaitCommand(ctx context.Context, id uuid.UUID, timeout time.Duration) (db.AgentCommand, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(s.awaitPoll)
	defer tick.Stop()
	for {
		cmd, err := s.q.GetAgentCommand(ctx, id)
		if err != nil {
			return db.AgentCommand{}, err
		}
		if slices.Contains([]string{"succeeded", "failed", "expired", "canceled"}, cmd.Status) {
			if cmd.Status != "succeeded" && cmd.Status != "failed" {
				return cmd, fmt.Errorf("the check was not delivered: the server is offline")
			}
			return cmd, nil
		}
		select {
		case <-ctx.Done():
			return db.AgentCommand{}, ctx.Err()
		case <-deadline.C:
			return db.AgentCommand{}, errors.New("the agent did not answer in time")
		case <-tick.C:
		}
	}
}

// RouteOf implements Provider.
func (s *Service) RouteOf(ctx context.Context, q *db.Queries, consumerSrv, providerServiceID uuid.UUID) (Route, bool, error) {
	sc, err := q.GetMeshServiceScope(ctx, providerServiceID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (sc.DeletedAt != nil || sc.ServerID == nil)) {
		return Route{}, false, nil
	}
	if err != nil {
		return Route{}, false, err
	}
	if *sc.ServerID == consumerSrv {
		return Route{}, false, nil
	}
	e, err := q.GetMeshExposureByService(ctx, db.GetMeshExposureByServiceParams{ServiceID: providerServiceID, Purpose: agentprotocol.MeshPurposeService})
	if errors.Is(err, pgx.ErrNoRows) {
		return Route{}, false, nil
	}
	if err != nil {
		return Route{}, false, err
	}
	r := Route{Host: sc.Slug, Port: int(e.MeshPort), State: "pending"}
	l, err := q.GetMeshLinkByKey(ctx, db.GetMeshLinkByKeyParams{EnvironmentID: sc.EnvironmentID, ServerID: consumerSrv, RemoteServerID: *sc.ServerID, Purpose: agentprotocol.MeshPurposeService})
	switch {
	case err == nil:
		r.LinkID, r.State = l.ID, l.State
	case !errors.Is(err, pgx.ErrNoRows):
		return Route{}, false, err
	}
	return r, true, nil
}
