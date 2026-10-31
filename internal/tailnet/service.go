// Package tailnet is the control-plane side of per-service Tailscale: one
// sidecar per service fronts all replicas on a Tailscale or Headscale tailnet.
//
// Desired state is services.config.tailnet (staged by the generation counter
// like every setting and applied by a normal deployment). service_tailnet holds
// the observed and applied side: the device, the exposures the agent last
// applied and the node's health. The node key is minted at command send time,
// attached as a send-time secret and never persisted. Rollback never touches
// tailnet state: deployments of an existing release keep (or, on another
// server, re-create) the node exactly as it was last applied.
package tailnet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/tailnet/tailnetcfg"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// keyExpiry is how long a minted node key stays valid. It is consumed on the
// first login; an unused key (a redelivered command re-mints) expires on its own.
const keyExpiry = time.Hour

// observedFresh is how long an unchanged observation is not written again.
const observedFresh = time.Minute

// deployGrace is how long after a deployment wrote the node a missing sidecar
// is not yet reported as offline.
const deployGrace = 2 * time.Minute

// Deps are the collaborators of Service.
type Deps struct {
	Pool       *pgxpool.Pool
	Bus        *events.Bus
	Dispatcher agents.Dispatcher
	Tailscale  *tailscale.Service
	Logger     *slog.Logger
}

// Service implements the per-service tailnet use cases and the hooks of the
// services, deployments and agents domains.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
	disp agents.Dispatcher
	ts   *tailscale.Service
	log  *slog.Logger
	now  func() time.Time
}

// NewService returns a Service (Pool may be nil for OpenAPI generation).
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{pool: d.Pool, q: db.New(d.Pool), bus: d.Bus, disp: d.Dispatcher, ts: d.Tailscale, log: d.Logger.With("component", "tailnet"), now: time.Now}
}

var _ services.TailnetHook = (*Service)(nil)

// SupportsServiceTailnet reports whether the server's agent advertised
// agentprotocol.CapServiceTailnet. A server that has never connected has no
// facts yet and is assumed to enroll with a current agent.
func SupportsServiceTailnet(srv db.Server) bool {
	if len(srv.Facts) == 0 {
		return true
	}
	var f agentprotocol.Facts
	if err := json.Unmarshal(srv.Facts, &f); err != nil || f.OS == "" {
		return true
	}
	return slices.Contains(f.Capabilities, agentprotocol.CapServiceTailnet)
}

func requireAgent(srv db.Server) error {
	if SupportsServiceTailnet(srv) {
		return nil
	}
	return apierr.Unprocessable("agent_too_old", "the agent on "+srv.Name+" is too old to manage tailnet sidecars; update the agent first")
}

func notConfigured() error {
	return apierr.Unprocessable("tailnet_not_configured", "connect Tailscale or Headscale in Team settings → Integrations first")
}

func (s *Service) publish(sc services.Scope) {
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
}

// identity derives the device identity of a desired node: the hostname, the
// tags its key carries and the fingerprint that decides re-provisioning.
func identity(set tailscale.Settings, want tailnetcfg.TailnetConfig, sc services.Scope) (hostname string, tags []string, hash string) {
	hostname = want.EffectiveHostname(sc.Service.Slug, sc.EnvSlug)
	tags = want.Tags(set.DefaultTags)
	return hostname, tags, tailnetcfg.IdentityHash(set.Provider, set.LoginServer, hostname, tags)
}

// checkSettings validates want against the integration (shared by the PATCH
// validation and the deploy plan).
func checkSettings(set tailscale.Settings, want tailnetcfg.TailnetConfig) []services.TailnetProblem {
	var probs []services.TailnetProblem
	for _, t := range want.ExtraTags {
		if !slices.Contains(set.AllowedTags, t) {
			probs = append(probs, services.TailnetProblem{Code: "tailnet_tag_not_allowed", Message: fmt.Sprintf("tag %s is not in the integration's allowed tags", t)})
		}
	}
	if set.Provider == agentprotocol.TailnetProviderHeadscale {
		if want.Funnel {
			probs = append(probs, services.TailnetProblem{Code: "tailnet_funnel_unsupported", Message: "Headscale does not support Funnel"})
		}
		for _, f := range want.TCP {
			if f.TLS {
				probs = append(probs, services.TailnetProblem{Code: "tailnet_tls_unsupported", Message: "Headscale has no HTTPS certificates: TCP ports cannot terminate TLS"})
				break
			}
		}
	} else if want.Web && set.HTTPSEnabled != nil && !*set.HTTPSEnabled {
		probs = append(probs, services.TailnetProblem{Code: "tailnet_https_disabled", Message: "HTTPS certificates are disabled on the tailnet; enable them in the Tailscale admin console (DNS, HTTPS Certificates) or expose TCP ports only"})
	}
	return probs
}

// ValidateConfig implements services.TailnetHook.
func (s *Service) ValidateConfig(ctx context.Context, sc services.Scope, cfg tailnetcfg.TailnetConfig) error {
	set, err := s.ts.Settings(ctx, sc.WorkspaceID)
	if errors.Is(err, tailscale.ErrNotConfigured) {
		return notConfigured()
	}
	if err != nil {
		return err
	}
	if probs := checkSettings(set, cfg); len(probs) > 0 {
		return apierr.Unprocessable(probs[0].Code, probs[0].Message)
	}
	return nil
}

// ConfigChanged implements services.TailnetHook: it audits enabling, disabling
// and Funnel. Names only: nothing here is secret.
func (s *Service) ConfigChanged(ctx context.Context, tx pgx.Tx, sc services.Scope, from, to *tailnetcfg.TailnetConfig) error {
	rec := func(action string, meta map[string]any) error {
		meta["service"] = sc.Service.Slug
		return audit.Record(ctx, tx, audit.Entry{
			Action: action, ResourceType: "service", ResourceID: sc.Service.ID.String(), ProjectID: &sc.ProjectID, Metadata: meta, WorkspaceID: sc.WorkspaceID,
		})
	}
	switch {
	case from == nil && to != nil:
		if err := rec("tailnet.enable", map[string]any{"hostname": to.EffectiveHostname(sc.Service.Slug, sc.EnvSlug), "web": to.Web, "tcpPorts": len(to.TCP), "hidePublic": to.HidePublic}); err != nil {
			return err
		}
	case from != nil && to == nil:
		if err := rec("tailnet.disable", map[string]any{}); err != nil {
			return err
		}
	}
	if to != nil && to.Funnel && (from == nil || !from.Funnel) {
		return rec("tailnet.funnel_enabled", map[string]any{"hostname": to.EffectiveHostname(sc.Service.Slug, sc.EnvSlug)})
	}
	return nil
}

// ServiceDeleted implements services.TailnetHook: the sidecar is removed from
// its server and the device from the tailnet (retried in the background).
func (s *Service) ServiceDeleted(ctx context.Context, tx pgx.Tx, sc services.Scope) error {
	row, err := db.New(tx).DeleteServiceTailnet(ctx, sc.Service.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete service tailnet: %w", err)
	}
	return s.retire(ctx, tx, sc.WorkspaceID, row, nil)
}

// retire removes a node that no longer exists as desired: the sidecar on its
// server (unless that is `current`, where the deploy that triggered this already
// removed it) and the device on the tailnet.
func (s *Service) retire(ctx context.Context, tx pgx.Tx, ws uuid.UUID, row db.ServiceTailnet, current *uuid.UUID) error {
	if row.ServerID != nil && s.disp != nil && (current == nil || *current != *row.ServerID) {
		raw, _ := json.Marshal(agentprotocol.RemoveTailnetPayload{ServiceID: row.ServiceID.String()})
		if _, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: *row.ServerID, Kind: agentprotocol.KindRemoveTailnet, Payload: raw}); err != nil {
			return fmt.Errorf("enqueue remove_tailnet: %w", err)
		}
	}
	if err := s.ts.EnqueueDeleteNode(ctx, tx, ws, tailscale.NodeRef{DeviceID: row.DeviceID, IPs: row.Ips, Hostname: row.Hostname}); err != nil {
		return fmt.Errorf("queue tailnet device removal: %w", err)
	}
	return nil
}

// PlanCheck implements deployments.TailnetHook.
func (s *Service) PlanCheck(ctx context.Context, q *db.Queries, sc services.Scope, cfg services.Config, srv *db.Server) ([]string, []services.TailnetProblem, error) {
	row, err := q.GetServiceTailnet(ctx, sc.Service.ID)
	have := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, fmt.Errorf("get service tailnet: %w", err)
	}
	var warns []string
	var probs []services.TailnetProblem
	want := cfg.Tailnet
	if want == nil {
		if have && row.State != services.TailnetRemoving {
			name := row.Fqdn
			if name == "" {
				name = row.Hostname
			}
			warns = append(warns, fmt.Sprintf("Tailnet: removes device %s from your tailnet. Re-enabling creates a new device with a new tailnet IP.", name))
			if row.AppliedHidePublic {
				warns = append(warns, "Tailnet only is turned off: the service's public domains are routed again.")
			}
			if srv != nil && !SupportsServiceTailnet(*srv) {
				probs = append(probs, services.TailnetProblem{Code: "agent_too_old", Message: "The agent on " + srv.Name + " is too old to remove the tailnet sidecar; update the agent first."})
			}
		}
		return warns, probs, nil
	}
	set, err := s.ts.Settings(ctx, sc.WorkspaceID)
	if errors.Is(err, tailscale.ErrNotConfigured) {
		return nil, []services.TailnetProblem{{Code: "tailnet_not_configured", Message: "Connect Tailscale or Headscale in Team settings → Integrations before putting this service on a tailnet."}}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	probs = append(probs, checkSettings(set, *want)...)
	if srv != nil && !SupportsServiceTailnet(*srv) {
		probs = append(probs, services.TailnetProblem{Code: "agent_too_old", Message: "The agent on " + srv.Name + " is too old to manage tailnet sidecars; update the agent first."})
	}
	hostname, tags, id := identity(set, *want, sc)
	switch {
	case !have:
		warns = append(warns, fmt.Sprintf("Tailnet: creates device %s tagged %s. Access is governed by your tailnet policy.", hostname, strings.Join(tags, ", ")))
	case row.IdentityHash != id || row.NeedsReprovision || (srv != nil && (row.ServerID == nil || *row.ServerID != srv.ID)):
		warns = append(warns, fmt.Sprintf("Tailnet: re-creates device %s (a new tailnet IP, and a new name if it is taken).", hostname))
	}
	if want.Funnel {
		warns = append(warns, "Funnel is on: the service is reachable from the public internet through Tailscale's relays, bypassing NebulaCtrl's proxy (no access logs or traffic analytics). Your tailnet policy must grant the funnel attribute to the tags.")
	}
	if want.HidePublic {
		warns = append(warns, "Tailnet only: the service's public domains are paused while it is on the tailnet.")
	}
	if set.Provider == agentprotocol.TailnetProviderHeadscale && want.Web {
		warns = append(warns, "Headscale has no HTTPS certificates: the web exposure is plain HTTP over WireGuard on port 80.")
	}
	return warns, probs, nil
}

// Spec implements deployments.TailnetHook.
func (s *Service) Spec(ctx context.Context, q *db.Queries, sc services.Scope, cfg services.Config, srv db.Server, keep bool) (*agentprotocol.ServiceTailnetSpec, bool, error) {
	row, err := q.GetServiceTailnet(ctx, sc.Service.ID)
	have := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("get service tailnet: %w", err)
	}

	if keep {
		// Rollback, reconcile redeploy, drain: the node stays as last applied.
		if !have || row.State == services.TailnetRemoving {
			return nil, false, nil
		}
		var spec agentprotocol.ServiceTailnetSpec
		if len(row.Applied) == 0 || json.Unmarshal(row.Applied, &spec) != nil || spec.Mode != agentprotocol.TailnetEnsure {
			return nil, false, nil
		}
		if err := requireAgent(srv); err != nil {
			return nil, false, err
		}
		spec.Provision = row.NeedsReprovision || row.ServerID == nil || *row.ServerID != srv.ID
		if spec.Web {
			spec.Port = cfg.Port
		}
		return &spec, spec.HidePublic, nil
	}

	want := cfg.Tailnet
	if want == nil {
		if !have || row.State == services.TailnetRemoving {
			return nil, false, nil
		}
		if err := requireAgent(srv); err != nil {
			return nil, false, err
		}
		return &agentprotocol.ServiceTailnetSpec{Mode: agentprotocol.TailnetRemove, ServiceID: sc.Service.ID.String()}, false, nil
	}

	set, err := s.ts.Settings(ctx, sc.WorkspaceID)
	if errors.Is(err, tailscale.ErrNotConfigured) {
		return nil, false, notConfigured()
	}
	if err != nil {
		return nil, false, err
	}
	if probs := checkSettings(set, *want); len(probs) > 0 {
		return nil, false, apierr.Unprocessable(probs[0].Code, probs[0].Message)
	}
	if err := requireAgent(srv); err != nil {
		return nil, false, err
	}
	hostname, tags, id := identity(set, *want, sc)
	spec := &agentprotocol.ServiceTailnetSpec{
		Mode: agentprotocol.TailnetEnsure, ServiceID: sc.Service.ID.String(), ServiceSlug: sc.Service.Slug, EnvironmentID: sc.EnvironmentID.String(),
		Provider: set.Provider, LoginServer: set.LoginServer, Hostname: hostname, Tags: tags,
		Web: want.Web, Funnel: want.Funnel && set.Provider == agentprotocol.TailnetProviderTailscale, HidePublic: want.HidePublic,
		IdentityHash: id,
	}
	if want.Web {
		spec.Port = cfg.Port
	}
	for _, f := range want.TCP {
		spec.TCP = append(spec.TCP, agentprotocol.TailnetTCP{Listen: f.Listen, Target: f.Target, TLS: f.TLS && set.Provider == agentprotocol.TailnetProviderTailscale})
	}
	spec.Provision = !have || row.ServerID == nil || *row.ServerID != srv.ID || row.IdentityHash != id || row.NeedsReprovision
	return spec, want.HidePublic, nil
}

// Secrets implements deployments.TailnetHook: it mints the node key of a
// provisioning spec. The previous device (a moved service, a changed identity, a
// re-provision) is deleted first so the new one keeps its name instead of
// becoming "<name>-1". The key is returned for the outgoing frame only.
func (s *Service) Secrets(ctx context.Context, serviceID uuid.UUID, spec agentprotocol.ServiceTailnetSpec) (map[string]string, error) {
	if spec.Mode != agentprotocol.TailnetEnsure || !spec.Provision {
		return nil, nil
	}
	sc, err := services.LoadScopeInternal(ctx, s.q, serviceID)
	if err != nil {
		return nil, fmt.Errorf("load service: %w", err)
	}
	row, err := s.q.GetServiceTailnet(ctx, serviceID)
	have := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("get service tailnet: %w", err)
	}
	if have {
		if err := s.ts.DeleteNode(ctx, sc.WorkspaceID, tailscale.NodeRef{DeviceID: row.DeviceID, IPs: row.Ips, Hostname: row.Hostname}); err != nil {
			return nil, fmt.Errorf("remove the previous tailnet device: %w", err)
		}
		if err := s.q.ClearServiceTailnetDevice(ctx, serviceID); err != nil {
			return nil, fmt.Errorf("forget the previous tailnet device: %w", err)
		}
		if row.AuthKeyID != "" {
			if err := s.ts.RevokeNodeKey(ctx, sc.WorkspaceID, row.AuthKeyID); err != nil {
				s.log.WarnContext(ctx, "could not revoke the previous node key", "service_id", serviceID.String(), "err", err)
			}
		}
	}
	key, err := s.ts.MintNodeKey(ctx, sc.WorkspaceID, tailscale.MintRequest{
		Tags: spec.Tags, Expiry: keyExpiry, Description: fmt.Sprintf("nebulactrl %s/%s/%s", sc.ProjectSlug, sc.EnvSlug, sc.Service.Slug),
	})
	if errors.Is(err, tailscale.ErrNotConfigured) {
		return nil, notConfigured()
	}
	if err != nil {
		return nil, fmt.Errorf("mint a tailnet auth key: %w", err)
	}
	if have && key.ID != "" {
		if err := s.q.SetServiceTailnetAuthKeyID(ctx, db.SetServiceTailnetAuthKeyIDParams{ServiceID: serviceID, AuthKeyID: key.ID}); err != nil {
			s.log.WarnContext(ctx, "could not remember the node key id", "service_id", serviceID.String(), "err", err)
		}
	}
	return map[string]string{agentprotocol.SecretServiceTailnetKey: key.Key}, nil
}

// RecordDeploy implements deployments.TailnetHook.
func (s *Service) RecordDeploy(ctx context.Context, tx pgx.Tx, workspaceID uuid.UUID, dep db.Deployment, st *agentprotocol.ServiceTailnetStatus) error {
	depID := dep.ID
	cmd, err := db.New(tx).GetDeployCommandByDeployment(ctx, &depID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get deploy command: %w", err)
	}
	var p agentprotocol.DeployPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || p.Tailnet == nil {
		return nil
	}
	return s.apply(ctx, tx, workspaceID, dep.ServerID, *p.Tailnet, st)
}

func knownState(st string) string {
	switch st {
	case services.TailnetProvisioning, services.TailnetOnline, services.TailnetOffline, services.TailnetNeedsAttn:
		return st
	}
	return services.TailnetProvisioning
}

// apply stores what an agent reported for a spec it processed.
func (s *Service) apply(ctx context.Context, tx pgx.Tx, ws uuid.UUID, serverID *uuid.UUID, spec agentprotocol.ServiceTailnetSpec, st *agentprotocol.ServiceTailnetStatus) error {
	q := db.New(tx)
	svcID, err := uuid.Parse(spec.ServiceID)
	if err != nil {
		return nil
	}
	if spec.Mode == agentprotocol.TailnetRemove {
		if !st.Removed {
			if st.Message != "" {
				_, err := q.SetServiceTailnetObserved(ctx, db.SetServiceTailnetObservedParams{
					ServiceID: svcID, ServerID: serverID, State: services.TailnetNeedsAttn, Message: st.Message, Ips: []string{},
				})
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
			}
			return nil
		}
		row, err := q.DeleteServiceTailnet(ctx, svcID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return s.retire(ctx, tx, ws, row, serverID)
	}
	applied, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	ips := st.IPs
	if ips == nil {
		ips = []string{}
	}
	state := knownState(st.State)
	_, err = q.UpsertServiceTailnetApplied(ctx, db.UpsertServiceTailnetAppliedParams{
		ServiceID: svcID, WorkspaceID: ws, ServerID: serverID, Provider: spec.Provider, Hostname: spec.Hostname, IdentityHash: spec.IdentityHash,
		DeviceID: st.DeviceID, Fqdn: st.FQDN, Ips: ips, State: state, Message: st.Message, Applied: applied,
		AppliedHidePublic: spec.HidePublic, Funnel: spec.Funnel, NeedsReprovision: state == services.TailnetNeedsAttn, Provisioned: st.Provisioned,
	})
	if err != nil {
		return fmt.Errorf("record service tailnet: %w", err)
	}
	return nil
}

// Handles reports whether kind is a command of this domain.
func Handles(kind string) bool {
	return kind == agentprotocol.KindSyncTailnet || kind == agentprotocol.KindRemoveTailnet
}

// SecretsForCommand attaches the send-time node key of a sync_tailnet command.
func (s *Service) SecretsForCommand(ctx context.Context, kind string, payload json.RawMessage) (map[string]string, error) {
	if kind != agentprotocol.KindSyncTailnet {
		return nil, nil
	}
	var p agentprotocol.SyncTailnetPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("decode sync_tailnet payload: %w", err)
	}
	id, err := uuid.Parse(p.Tailnet.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("sync_tailnet service id: %w", err)
	}
	return s.Secrets(ctx, id, p.Tailnet)
}

// HandleResult applies the result of a sync_tailnet or remove_tailnet command.
func (s *Service) HandleResult(ctx context.Context, serverID, commandID uuid.UUID, res agentprotocol.ResultFrame) error {
	cmd, err := s.q.GetAgentCommand(ctx, commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get command: %w", err)
	}
	if cmd.Kind != agentprotocol.KindSyncTailnet {
		return nil // remove_tailnet: the node was forgotten when it was enqueued
	}
	var p agentprotocol.SyncTailnetPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil
	}
	svcID, err := uuid.Parse(p.Tailnet.ServiceID)
	if err != nil {
		return nil
	}
	sc, err := services.LoadScopeInternal(ctx, s.q, svcID)
	if err != nil {
		return nil // service deleted meanwhile
	}
	var st agentprotocol.ServiceTailnetStatus
	if len(res.Data) > 0 {
		_ = json.Unmarshal(res.Data, &st)
	}
	if res.Outcome != agentprotocol.OutcomeSucceeded || st.State == "" {
		msg := res.Error
		if msg == "" {
			msg = "The tailnet sidecar could not be provisioned."
		}
		_, err := s.q.SetServiceTailnetObserved(ctx, db.SetServiceTailnetObservedParams{
			ServiceID: svcID, ServerID: &serverID, State: services.TailnetNeedsAttn, Message: msg, Ips: []string{},
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		s.publish(sc)
		return nil
	}
	if err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return s.apply(ctx, tx, sc.WorkspaceID, &serverID, p.Tailnet, &st)
	}); err != nil {
		return err
	}
	s.publish(sc)
	return nil
}

// HandleObserved records the sidecar state the agent reports with its observed
// services (every 15 seconds).
func (s *Service) HandleObserved(ctx context.Context, serverID uuid.UUID, obs agentprotocol.Observed) error {
	var firstErr error
	for _, o := range obs.Services {
		if err := s.observeOne(ctx, serverID, o); err != nil {
			s.log.ErrorContext(ctx, "handle observed tailnet", "service_id", o.ServiceID, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (s *Service) observeOne(ctx context.Context, serverID uuid.UUID, o agentprotocol.ObservedService) error {
	svcID, err := uuid.Parse(o.ServiceID)
	if err != nil {
		return nil
	}
	row, err := s.q.GetServiceTailnet(ctx, svcID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if row.ServerID == nil || *row.ServerID != serverID || row.State == services.TailnetRemoving {
		return nil
	}
	st := agentprotocol.ServiceTailnetStatus{State: services.TailnetOffline, Message: "The tailnet sidecar is not running on this server."}
	if o.Tailnet != nil {
		st = *o.Tailnet
		st.State = knownState(st.State)
	} else if s.now().Sub(row.UpdatedAt) < deployGrace {
		return nil // a deployment just wrote the node
	}
	ips := st.IPs
	if ips == nil {
		ips = []string{}
	}
	same := row.State == st.State && row.Message == st.Message && (st.FQDN == "" || st.FQDN == row.Fqdn) && (len(ips) == 0 || slices.Equal(ips, row.Ips))
	if same && row.ObservedAt != nil && s.now().Sub(*row.ObservedAt) < observedFresh {
		return nil
	}
	if _, err := s.q.SetServiceTailnetObserved(ctx, db.SetServiceTailnetObservedParams{
		ServiceID: svcID, ServerID: &serverID, State: st.State, Message: st.Message, Fqdn: st.FQDN, Ips: ips, DeviceID: st.DeviceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if !same {
		if sc, err := services.LoadScopeInternal(ctx, s.q, svcID); err == nil {
			s.publish(sc)
		}
	}
	return nil
}

// ReprovisionResult is the response of Reprovision.
type ReprovisionResult struct {
	CommandID uuid.UUID `json:"commandId"`
}

// Reprovision re-creates the service's tailnet node: a fresh key, a fresh state
// volume and a new device (the previous one is deleted first). It is the fix for
// a node that needs attention (device removed in the admin console, key
// rejected).
func (s *Service) Reprovision(ctx context.Context, serviceID uuid.UUID) (ReprovisionResult, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeDeploymentsWrite, serviceID)
	if err != nil {
		return ReprovisionResult{}, err
	}
	if err := services.RequireNotStack(sc); err != nil {
		return ReprovisionResult{}, err
	}
	if err := sc.Guard(ctx, "reprovisionServiceTailnet", fmt.Sprintf("Re-create the tailnet device of %s in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), false,
		[]string{"The service leaves the tailnet under its old identity and joins again; its tailnet address changes."}); err != nil {
		return ReprovisionResult{}, err
	}
	var id uuid.UUID
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		row, err := q.GetServiceTailnet(ctx, serviceID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.State == services.TailnetRemoving) {
			return apierr.Unprocessable("not_applicable", "the service has no tailnet node to re-provision")
		}
		if err != nil {
			return err
		}
		if row.ServerID == nil {
			return apierr.Unprocessable("no_server", "the service's tailnet node has no server")
		}
		srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *row.ServerID})
		if err != nil {
			return fmt.Errorf("load server: %w", err)
		}
		if srv.Status == "revoked" {
			return apierr.Unprocessable("server_revoked", "the server was revoked; deploy the service to another server")
		}
		if err := requireAgent(srv); err != nil {
			return err
		}
		var spec agentprotocol.ServiceTailnetSpec
		if len(row.Applied) == 0 || json.Unmarshal(row.Applied, &spec) != nil || spec.Mode != agentprotocol.TailnetEnsure {
			return apierr.Unprocessable("not_applicable", "the service's tailnet node was never applied; deploy the service first")
		}
		spec.Provision = true
		if _, err := q.MarkServiceTailnetReprovision(ctx, serviceID); err != nil {
			return err
		}
		raw, err := json.Marshal(agentprotocol.SyncTailnetPayload{Tailnet: spec})
		if err != nil {
			return err
		}
		if id, err = s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: *row.ServerID, Kind: agentprotocol.KindSyncTailnet, Payload: raw}); err != nil {
			return fmt.Errorf("enqueue sync_tailnet: %w", err)
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "tailnet.reprovision", ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "hostname": spec.Hostname}, WorkspaceID: sc.WorkspaceID,
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return ReprovisionResult{}, err
		}
		return ReprovisionResult{}, fmt.Errorf("reprovision tailnet: %w", err)
	}
	s.publish(sc)
	return ReprovisionResult{CommandID: id}, nil
}

// HandleUndelivered marks the node of a sync_tailnet command that never reached
// its agent as needing attention (the command expired or was canceled).
func (s *Service) HandleUndelivered(ctx context.Context, commandID uuid.UUID, msg string) error {
	cmd, err := s.q.GetAgentCommand(ctx, commandID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && cmd.Kind != agentprotocol.KindSyncTailnet) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get command: %w", err)
	}
	var p agentprotocol.SyncTailnetPayload
	if json.Unmarshal(cmd.Payload, &p) != nil {
		return nil
	}
	svcID, err := uuid.Parse(p.Tailnet.ServiceID)
	if err != nil {
		return nil
	}
	_, err = s.q.SetServiceTailnetObserved(ctx, db.SetServiceTailnetObservedParams{
		ServiceID: svcID, ServerID: &cmd.ServerID, State: services.TailnetNeedsAttn, Message: msg, Ips: []string{},
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if sc, err := services.LoadScopeInternal(ctx, s.q, svcID); err == nil {
		s.publish(sc)
	}
	return nil
}
