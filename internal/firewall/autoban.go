package firewall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Audit actions of auto-ban. A ban the engine decides is not audited one by
// one (the bans list is its record).
const (
	ActionAutoBanUpdate = "service.autoban.update"
	ActionAutoBanUnban  = "service.autoban.unban"
)

// Ban list states (the state query parameter).
const (
	BanStateActive = "active"
	BanStateEnded  = "ended"
)

// Warnings of AutoBanView.Warnings.
const (
	// WarnCloudflareProxiedNotLocked: a domain of the service is proxied
	// through Cloudflare but not locked to it (or tunnelled), so the proxy
	// only sees Cloudflare's addresses and cannot tell visitors apart.
	WarnCloudflareProxiedNotLocked = "cloudflare_proxied_not_locked"
)

// Retention and timing of the ban records.
const (
	// EndedBanRetention is how long an ended ban stays listed (prefix only).
	EndedBanRetention = 90 * 24 * time.Hour
	// expiredGrace is how long an active ban may run past its expiry before the
	// control plane ends it itself (the agent reports the end, normally at once).
	expiredGrace = 10 * time.Minute
	// DigestInterval is how often bans are announced: one notification per
	// service per interval.
	DigestInterval = 10 * time.Minute
)

// AutoBanView is a service's auto-ban policy and where it applies.
type AutoBanView struct {
	Policy agentprotocol.AutoBan `json:"policy" doc:"The service's policy; the defaults (off) when it has none"`
	// Supported is false when the server's agent is too old for auto-ban.
	Supported bool `json:"supported"`
	// Server is the server that runs the service (empty without one).
	Server string `json:"server"`
	// Analytics tells whether traffic analytics is on for that server: auto-ban
	// reads the proxy's access log, which only exists then.
	Analytics bool `json:"analytics" doc:"Traffic analytics is on for the service's server; auto-ban needs it"`
	// Warnings name conditions that keep auto-ban from working for some visitors.
	Warnings []string `json:"warnings" doc:"cloudflare_proxied_not_locked: a Cloudflare-proxied domain that is not locked to Cloudflare shows only Cloudflare's addresses, so its visitors cannot be banned"`
	// ActiveBans counts the bans in force now.
	ActiveBans int `json:"activeBans"`
	// Hosts are the domains the policy covers.
	Hosts []string `json:"hosts"`
}

// BanView is one ban of a service.
type BanView struct {
	ID        uuid.UUID      `json:"id"`
	Prefix    string         `json:"prefix" doc:"The banned network: the address as /32 (IPv4) or its /64 (IPv6)"`
	Address   *string        `json:"address" doc:"The banned address; kept only while the ban is active, null afterwards"`
	StartedAt time.Time      `json:"startedAt"`
	ExpiresAt time.Time      `json:"expiresAt"`
	EndedAt   *time.Time     `json:"endedAt"`
	EndReason string         `json:"endReason,omitempty" enum:"expired,unbanned,lifted,resync" doc:"Why the ban ended: expired, unbanned (by a person), lifted (policy switched off, allow-listed or route removed) or resync (the agent no longer held it)"`
	Strikes   int            `json:"strikes"`
	Rules     map[string]int `json:"rules" doc:"Blocked requests that counted toward the ban, per firewall rule id"`
	Source    string         `json:"source" enum:"auto"`
}

// BansPage is a page of bans.
type BansPage = api.Page[BanView]

// UnbanView is the answer of an unban request.
type UnbanView struct {
	Status string `json:"status" enum:"queued" doc:"The agent was told to lift the ban; the ban ends when it confirms"`
}

// SetDispatcher wires the command dispatcher the unban uses (wire.go).
func (s *Service) SetDispatcher(d agents.Dispatcher) { s.disp = d }

// SetNotifier wires the notification emitter of the ban digest (wire.go).
func (s *Service) SetNotifier(e notifications.Emitter) { s.notify = e }

// supportsAutoBan reports whether the server's agent advertised
// agentprotocol.CapAutoBan. A server that never connected is assumed to enroll
// with a current agent.
func supportsAutoBan(srv db.Server) bool {
	if len(srv.Facts) == 0 {
		return true
	}
	var f agentprotocol.Facts
	if err := json.Unmarshal(srv.Facts, &f); err != nil || f.OS == "" {
		return true
	}
	return slices.Contains(f.Capabilities, agentprotocol.CapAutoBan)
}

// CheckAutoBanPlacement reports why an enabled policy cannot run on srv, with
// the errors PutAutoBan answers with: agent_too_old when the agent predates
// auto-ban, analytics_required when traffic analytics is off. A disabled
// policy runs anywhere. The config planner calls it so the plan is blocked
// instead of the apply failing.
func CheckAutoBanPlacement(srv db.Server, a agentprotocol.AutoBan) error {
	if !a.Enabled {
		return nil
	}
	if !supportsAutoBan(srv) {
		return apierr.Unprocessable("agent_too_old", "the agent on "+srv.Name+" is too old for auto-ban; update the agent first")
	}
	if !srv.TrafficAnalyticsEnabled {
		return apierr.Unprocessable("analytics_required", "Auto-ban reads the proxy's access log: turn on traffic analytics in the settings of "+srv.Name+" first.")
	}
	return nil
}

// GetAutoBan returns the auto-ban policy of a service (scope projects:read).
func (s *Service) GetAutoBan(ctx context.Context, id uuid.UUID) (AutoBanView, error) {
	t, err := s.load(ctx, policy.ScopeProjectsRead, id)
	if err != nil {
		return AutoBanView{}, err
	}
	return s.autoBanView(ctx, t)
}

func (s *Service) autoBanView(ctx context.Context, t target) (AutoBanView, error) {
	v := AutoBanView{Policy: domains.StoredAutoBan(t.sc.Service.Autoban), Supported: true, Warnings: []string{}, Hosts: []string{}}
	doms, err := s.q.ListServiceDomains(ctx, t.sc.Service.ID)
	if err != nil {
		return AutoBanView{}, fmt.Errorf("list domains: %w", err)
	}
	for _, d := range doms {
		if !slices.Contains(v.Hosts, d.Hostname) {
			v.Hosts = append(v.Hosts, d.Hostname)
		}
	}
	if t.server != nil {
		v.Server = t.server.Name
		v.Analytics = t.server.TrafficAnalyticsEnabled
		v.Supported = supportsAutoBan(*t.server)
		if len(doms) > 0 {
			rules, err := domains.PrepareDeployRules(ctx, s.q, t.server.ID, t.sc.Service.ID, doms)
			if err != nil {
				return AutoBanView{}, err
			}
			locked := map[string]bool{}
			for _, r := range rules.Rules {
				locked[r.Host] = r.Tunnel || r.OriginLocked
			}
			for _, d := range doms {
				if d.CfProxied && !locked[d.Hostname] {
					v.Warnings = append(v.Warnings, WarnCloudflareProxiedNotLocked)
					break
				}
			}
		}
	}
	n, err := s.q.CountActiveServiceBans(ctx, t.sc.Service.ID)
	if err != nil {
		return AutoBanView{}, fmt.Errorf("count bans: %w", err)
	}
	v.ActiveBans = int(n)
	return v, nil
}

// PutAutoBan replaces a service's auto-ban policy (scopes projects:write and
// proxy:write). A policy equal to the default (off, default limits, no allow
// list) removes it. 422 invalid_autoban for a policy out of its limits,
// agent_too_old and analytics_required when it cannot run on the server.
func (s *Service) PutAutoBan(ctx context.Context, id uuid.UUID, in agentprotocol.AutoBan) (AutoBanView, error) {
	t, err := s.load(ctx, policy.ScopeProjectsWrite, id)
	if err != nil {
		return AutoBanView{}, err
	}
	sc := t.sc
	if err := policy.Require(ctx, policy.ScopeProxyWrite, &sc.ProjectID); err != nil {
		return AutoBanView{}, err
	}
	if err := services.RequireNotStack(sc); err != nil {
		return AutoBanView{}, err
	}
	if !httpService(sc) {
		return AutoBanView{}, apierr.Unprocessable("autoban_unsupported", "auto-ban only applies to HTTP services, not tcp/udp routes")
	}
	a := in.Normalize()
	if err := a.Validate(); err != nil {
		return AutoBanView{}, apierr.Unprocessable("invalid_autoban", err.Error())
	}
	if t.server != nil {
		if err := CheckAutoBanPlacement(*t.server, a); err != nil {
			return AutoBanView{}, err
		}
	}
	var raw json.RawMessage
	if !domains.AutoBanIsDefaultOff(a) {
		if raw, err = json.Marshal(a); err != nil {
			return AutoBanView{}, err
		}
	}
	// The column is jsonb, which returns the document in its own layout: compare
	// the stored policy as the document we would write.
	var current json.RawMessage
	if old := domains.DecodeAutoBan(sc.Service.Autoban); old != nil && !domains.AutoBanIsDefaultOff(*old) {
		if current, err = json.Marshal(old.Normalize()); err != nil {
			return AutoBanView{}, err
		}
	}
	if string(raw) == string(current) {
		return s.autoBanView(ctx, t)
	}
	if err := sc.Guard(ctx, "set_autoban",
		fmt.Sprintf("Change the auto-ban of %s in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), false,
		[]string{fmt.Sprintf("Auto-ban is %s: %d blocked requests in %ds ban an address for %ds.", onOff(a.Enabled), a.Threshold, a.WindowSeconds, a.DurationSeconds),
			"Switching it off, or allow-listing an address, lifts its bans at once."},
		string(raw)); err != nil {
		return AutoBanView{}, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := db.New(tx).SetServiceAutoBan(ctx, db.SetServiceAutoBanParams{ID: id, Autoban: raw})
		if err != nil {
			return fmt.Errorf("store auto-ban: %w", err)
		}
		sc.Service = row
		if err := audit.Record(ctx, tx, audit.Entry{
			Action: ActionAutoBanUpdate, ResourceType: "service", ResourceID: id.String(), ProjectID: &sc.ProjectID, WorkspaceID: sc.WorkspaceID,
			Metadata: map[string]any{
				"slug": row.Slug, "enabled": a.Enabled, "threshold": a.Threshold, "windowSeconds": a.WindowSeconds,
				"durationSeconds": a.DurationSeconds, "allow": len(a.Allow),
			},
		}); err != nil {
			return err
		}
		if !t.deployed || s.routes == nil {
			return nil
		}
		return s.routes.PushRoutes(ctx, tx, t.server.ID)
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return AutoBanView{}, err
		}
		return AutoBanView{}, fmt.Errorf("set auto-ban: %w", err)
	}
	t.sc = sc
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
	return s.autoBanView(ctx, t)
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// ListBans returns a page of a service's bans, newest first (scope
// projects:read). state is "active" (the bans in force, with their address) or
// "ended" (prefix only).
func (s *Service) ListBans(ctx context.Context, id uuid.UUID, state string, pq api.PageQuery) (BansPage, error) {
	t, err := s.load(ctx, policy.ScopeProjectsRead, id)
	if err != nil {
		return BansPage{}, err
	}
	afterAt, afterID, hasAfter, err := api.DecodeTimeCursor(pq.Cursor)
	if err != nil {
		return BansPage{}, err
	}
	size := pq.Size()
	rows, err := s.q.ListServiceBans(ctx, db.ListServiceBansParams{
		ServiceID: t.sc.Service.ID, Active: state != BanStateEnded,
		After: hasAfter, AfterStartedAt: afterAt, AfterID: afterID, PageLimit: int32(size + 1),
	})
	if err != nil {
		return BansPage{}, fmt.Errorf("list bans: %w", err)
	}
	page := BansPage{Items: []BanView{}}
	if len(rows) > size {
		rows = rows[:size]
		last := rows[len(rows)-1]
		page.NextCursor = api.EncodeTimeCursor(last.StartedAt, last.ID)
	}
	for _, r := range rows {
		page.Items = append(page.Items, banView(r))
	}
	return page, nil
}

func banView(r db.ServiceBan) BanView {
	v := BanView{
		ID: r.ID, Prefix: r.Prefix, Address: r.Address, StartedAt: r.StartedAt, ExpiresAt: r.ExpiresAt, EndedAt: r.EndedAt,
		Strikes: int(r.Strikes), Rules: map[string]int{}, Source: r.Source,
	}
	if r.EndReason != nil {
		v.EndReason = *r.EndReason
	}
	_ = json.Unmarshal(r.Rules, &v.Rules)
	if v.Rules == nil {
		v.Rules = map[string]int{}
	}
	return v
}

// Unban asks the agent to lift an active ban (scopes projects:write and
// proxy:write). The ban ends when the agent confirms (HandleResult or its end
// frame); a ban that already ended answers 409 ban_not_active.
func (s *Service) Unban(ctx context.Context, serviceID, banID uuid.UUID) (UnbanView, error) {
	t, err := s.load(ctx, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return UnbanView{}, err
	}
	sc := t.sc
	if err := policy.Require(ctx, policy.ScopeProxyWrite, &sc.ProjectID); err != nil {
		return UnbanView{}, err
	}
	if s.disp == nil {
		return UnbanView{}, fmt.Errorf("unban: no dispatcher wired")
	}
	ban, err := s.q.GetServiceBan(ctx, db.GetServiceBanParams{ID: banID, ServiceID: sc.Service.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return UnbanView{}, apierr.NotFound("ban")
	}
	if err != nil {
		return UnbanView{}, fmt.Errorf("load ban: %w", err)
	}
	if ban.EndedAt != nil {
		return UnbanView{}, apierr.Conflict("ban_not_active", "this ban has already ended")
	}
	payload, err := json.Marshal(agentprotocol.AutoBanUnbanPayload{ServiceID: sc.Service.ID.String(), BanID: ban.ID.String()})
	if err != nil {
		return UnbanView{}, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: ban.ServerID, Kind: agentprotocol.KindAutoBanUnban, Payload: payload}); err != nil {
			return fmt.Errorf("enqueue autoban_unban: %w", err)
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionAutoBanUnban, ResourceType: "service", ResourceID: sc.Service.ID.String(), ProjectID: &sc.ProjectID, WorkspaceID: sc.WorkspaceID,
			Metadata: map[string]any{"slug": sc.Service.Slug, "banId": ban.ID.String(), "prefix": ban.Prefix},
		})
	})
	if err != nil {
		return UnbanView{}, err
	}
	return UnbanView{Status: "queued"}, nil
}

// HandleResult ends the ban an autoban_unban command lifted. Commands of other
// kinds, and failed ones, are ignored (a failed unban leaves the ban active).
// It is idempotent: the agent may redeliver a result.
func (s *Service) HandleResult(ctx context.Context, serverID, commandID uuid.UUID, res agentprotocol.ResultFrame) error {
	cmd, err := s.q.GetAgentCommand(ctx, commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load command: %w", err)
	}
	if cmd.Kind != agentprotocol.KindAutoBanUnban || cmd.ServerID != serverID || res.Outcome != agentprotocol.OutcomeSucceeded {
		return nil
	}
	var p agentprotocol.AutoBanUnbanPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		s.log.WarnContext(ctx, "autoban_unban payload unreadable", "command_id", commandID, "err", err)
		return nil
	}
	banID, err := uuid.Parse(p.BanID)
	if err != nil {
		s.log.WarnContext(ctx, "autoban_unban payload has no valid ban id", "command_id", commandID, "err", err)
		return nil
	}
	if _, err := s.q.EndServiceBan(ctx, db.EndServiceBanParams{
		ID: banID, ServerID: serverID, EndedAt: new(time.Now()), EndReason: new(agentprotocol.BanEndUnbanned),
	}); err != nil {
		return fmt.Errorf("end ban: %w", err)
	}
	return nil
}

// IngestBans stores the ban events an agent reports and applies a snapshot
// (agents.BanSink). A nil error means the frame is durable and the agent may
// forget it; a redelivered frame changes nothing: a ban is keyed by the
// agent's id, and ending an ended ban is a no-op.
func (s *Service) IngestBans(ctx context.Context, serverID uuid.UUID, f agentprotocol.BanFrame) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		listed := map[uuid.UUID]bool{}
		for _, ev := range f.Events {
			id, err := uuid.Parse(ev.Ban.ID)
			if err != nil {
				continue
			}
			switch ev.Kind {
			case agentprotocol.BanEventBan:
				svc, err := uuid.Parse(ev.Ban.ServiceID)
				if err != nil {
					continue
				}
				addr, ok := banAddress(ev.Ban.Prefix)
				if !ok {
					continue
				}
				rules, err := json.Marshal(ev.Ban.Rules)
				if err != nil || string(rules) == "null" {
					rules = []byte("{}")
				}
				if err := q.InsertServiceBan(ctx, db.InsertServiceBanParams{
					ID: id, ServerID: serverID, ServiceID: svc, RouteID: ev.Ban.RouteID, Prefix: ev.Ban.Prefix, Address: &addr,
					StartedAt: ev.Ban.StartedAt, ExpiresAt: ev.Ban.ExpiresAt, Strikes: int32(ev.Ban.Strikes), Rules: rules,
				}); err != nil {
					return fmt.Errorf("store ban: %w", err)
				}
				listed[id] = true
			case agentprotocol.BanEventEnd:
				at := ev.At
				if at.IsZero() {
					at = time.Now()
				}
				if _, err := q.EndServiceBan(ctx, db.EndServiceBanParams{ID: id, ServerID: serverID, EndedAt: &at, EndReason: new(endReason(ev.Reason))}); err != nil {
					return fmt.Errorf("end ban: %w", err)
				}
			}
		}
		if !f.Snapshot {
			return nil
		}
		var svcs []uuid.UUID
		for _, sid := range f.Services {
			if id, err := uuid.Parse(sid); err == nil {
				svcs = append(svcs, id)
			}
		}
		if len(svcs) == 0 {
			return nil
		}
		keep := make([]uuid.UUID, 0, len(listed))
		for id := range listed {
			keep = append(keep, id)
		}
		now := time.Now()
		if _, err := q.EndServiceBansNotIn(ctx, db.EndServiceBansNotInParams{ServerID: serverID, ServiceIds: svcs, KeepIds: keep, EndedAt: &now}); err != nil {
			return fmt.Errorf("reconcile bans: %w", err)
		}
		return nil
	})
}

// endReason maps what an agent reports to a stored reason; anything the
// database does not know counts as lifted.
func endReason(r string) string {
	switch r {
	case agentprotocol.BanEndExpired, agentprotocol.BanEndUnbanned, agentprotocol.BanEndLifted:
		return r
	}
	return agentprotocol.BanEndLifted
}

// banAddress is the address of a ban prefix ("203.0.113.7/32" gives
// "203.0.113.7"; "2001:db8:1:2::/64" its network address).
func banAddress(prefix string) (string, bool) {
	p, err := netip.ParsePrefix(prefix)
	if err != nil {
		return "", false
	}
	return p.Addr().String(), true
}

// ---- jobs ----

// DigestArgs announces the bans of the last interval (every DigestInterval).
type DigestArgs struct{}

// Kind implements river.JobArgs.
func (DigestArgs) Kind() string { return "autoban_digest" }

// PruneBansArgs ends bans that ran out unreported and drops old ended ones.
type PruneBansArgs struct{}

// Kind implements river.JobArgs.
func (PruneBansArgs) Kind() string { return "autoban_prune" }

// PruneBans ends the active bans that expired more than a grace period ago
// without an end frame and deletes ended bans older than EndedBanRetention.
func (s *Service) PruneBans(ctx context.Context, now time.Time) (ended, pruned int64, err error) {
	if ended, err = s.q.ExpireServiceBans(ctx, now.Add(-expiredGrace)); err != nil {
		return 0, 0, fmt.Errorf("expire bans: %w", err)
	}
	if pruned, err = s.q.PruneServiceBans(ctx, new(now.Add(-EndedBanRetention))); err != nil {
		return ended, 0, fmt.Errorf("prune bans: %w", err)
	}
	return ended, pruned, nil
}

// Digest sends one notification per service for the bans not announced yet.
// It carries counts and the rule that struck most, never an address. Bans of a
// service that is gone are dropped without a message.
func (s *Service) Digest(ctx context.Context, now time.Time) (int, error) {
	if _, err := s.q.ClearNotifiedServiceBans(ctx, now); err != nil {
		return 0, fmt.Errorf("clear bans: %w", err)
	}
	rows, err := s.q.ListUnnotifiedServiceBans(ctx, now)
	if err != nil {
		return 0, fmt.Errorf("list unnotified bans: %w", err)
	}
	type group struct {
		row   db.ListUnnotifiedServiceBansRow
		ids   []uuid.UUID
		rules map[string]int
	}
	var order []uuid.UUID
	groups := map[uuid.UUID]*group{}
	for _, r := range rows {
		g := groups[r.ServiceID]
		if g == nil {
			g = &group{row: r, rules: map[string]int{}}
			groups[r.ServiceID] = g
			order = append(order, r.ServiceID)
		}
		g.ids = append(g.ids, r.ID)
		var rules map[string]int
		_ = json.Unmarshal(r.Rules, &rules)
		for id, n := range rules {
			g.rules[id] += n
		}
	}
	for _, sid := range order {
		g := groups[sid]
		if err := s.q.MarkServiceBansNotified(ctx, g.ids); err != nil {
			return 0, fmt.Errorf("mark bans notified: %w", err)
		}
		s.announce(ctx, g.row, len(g.ids), g.rules)
	}
	return len(order), nil
}

func (s *Service) announce(ctx context.Context, r db.ListUnnotifiedServiceBansRow, n int, rules map[string]int) {
	noun := "addresses"
	if n == 1 {
		noun = "address"
	}
	title := fmt.Sprintf("%d %s banned on %s in the last %d minutes", n, noun, r.ServiceName, int(DigestInterval/time.Minute))
	fields := map[string]string{"Banned": fmt.Sprint(n)}
	body := "Visitors that kept tripping the firewall are blocked for a while."
	if top := topRule(r.Firewall, rules); top != "" {
		body = "Most blocked requests hit: " + top + "."
		fields["Top rule"] = top
	}
	pid, sid := r.ProjectID, r.ServiceID
	notifications.Emit(ctx, s.notify, notifications.Event{
		Type: notifications.TypeServiceAutoBan, ProjectID: &pid, ServiceID: &sid, Title: title, Body: body, Fields: fields,
	})
}

// topRule names the rule with the most strikes: a user rule by its name, a
// standard protection by its name, "a removed rule" when neither is known.
func topRule(firewallRaw json.RawMessage, rules map[string]int) string {
	best, bestN := "", 0
	ids := make([]string, 0, len(rules))
	for id := range rules {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if rules[id] > bestN {
			best, bestN = id, rules[id]
		}
	}
	if best == "" {
		return ""
	}
	for _, sr := range domains.StandardRules() {
		if sr.Rule.ID == best {
			return sr.Rule.Name
		}
	}
	if fw := domains.DecodeFirewall(firewallRaw); fw != nil {
		for _, r := range fw.Rules {
			if r.ID == best && r.Name != "" {
				return r.Name
			}
		}
	}
	return "a removed rule"
}

type digestWorker struct {
	river.WorkerDefaults[DigestArgs]
	svc *Service
}

func (w *digestWorker) Work(ctx context.Context, _ *river.Job[DigestArgs]) error {
	_, err := w.svc.Digest(ctx, time.Now())
	return err
}

type pruneBansWorker struct {
	river.WorkerDefaults[PruneBansArgs]
	svc *Service
	jm  *jobs.Manager
}

func (w *pruneBansWorker) Work(ctx context.Context, _ *river.Job[PruneBansArgs]) error {
	ended, pruned, err := w.svc.PruneBans(ctx, time.Now())
	if err != nil {
		return err
	}
	if ended+pruned > 0 {
		w.jm.Logger().InfoContext(ctx, "auto-ban records", "expired", ended, "pruned", pruned)
	}
	return nil
}

// RegisterJobs adds the ban digest (every DigestInterval) and the hourly prune.
func RegisterJobs(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &digestWorker{svc: svc})
		river.AddWorker(w, &pruneBansWorker{svc: svc, jm: jm})
	})
	periodic := func(id string, every time.Duration, args river.JobArgs) {
		jm.AddPeriodic(river.NewPeriodicJob(
			river.PeriodicInterval(every),
			func() (river.JobArgs, *river.InsertOpts) { return args, &river.InsertOpts{MaxAttempts: 1} },
			&river.PeriodicJobOpts{ID: id, RunOnStart: false},
		))
	}
	periodic("autoban_digest", DigestInterval, DigestArgs{})
	periodic("autoban_prune", time.Hour, PruneBansArgs{})
}
