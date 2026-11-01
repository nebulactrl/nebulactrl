package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/tailnet/tailnetcfg"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Tailnet node states of TailnetView.State (mirrors service_tailnet.state).
const (
	TailnetProvisioning = "provisioning"
	TailnetOnline       = "online"
	TailnetOffline      = "offline"
	TailnetNeedsAttn    = "needs_attention"
	TailnetRemoving     = "removing"
)

// TailnetProblem is a problem found by the tailnet domain: a blocking one
// prevents the deployment, a warning is shown in the plan.
type TailnetProblem struct {
	Code    string
	Message string
}

// TailnetView is the observed side of a service's tailnet node: the device the
// sidecar registered, its address and health. It is null until a deployment
// provisioned the node. The desired side is config.tailnet.
type TailnetView struct {
	State    string   `json:"state" enum:"provisioning,online,offline,needs_attention,removing"`
	Message  string   `json:"message"`
	Provider string   `json:"provider" enum:"tailscale,headscale"`
	Hostname string   `json:"hostname"`
	FQDN     string   `json:"fqdn" doc:"MagicDNS name read back from the node (never assumed)"`
	URL      string   `json:"url" doc:"Web URL of the node (https on Tailscale, http on Headscale); empty without a web exposure or name"`
	IPs      []string `json:"ips"`
	// Web, TCP and Funnel are the exposures the agent last applied.
	Web    bool                    `json:"web"`
	TCP    []tailnetcfg.TCPForward `json:"tcp"`
	Funnel bool                    `json:"funnel"`
	// HidePublic is true while the service's public domains are not routed
	// ("Tailnet only"); it follows the applied configuration.
	HidePublic bool `json:"hidePublic"`
	// NeedsReprovision is set when the node must be re-created (a removed
	// device or a rejected key): the next deployment, or "Re-provision", does it.
	NeedsReprovision bool       `json:"needsReprovision"`
	ObservedAt       *time.Time `json:"observedAt"`
}

// TailnetViewOf renders a service_tailnet row.
func TailnetViewOf(r db.ServiceTailnet) TailnetView {
	v := TailnetView{
		State: r.State, Message: r.Message, Provider: r.Provider, Hostname: r.Hostname, FQDN: r.Fqdn,
		IPs: r.Ips, TCP: []tailnetcfg.TCPForward{}, Funnel: r.Funnel, HidePublic: r.AppliedHidePublic,
		NeedsReprovision: r.NeedsReprovision, ObservedAt: r.ObservedAt,
	}
	if v.IPs == nil {
		v.IPs = []string{}
	}
	var spec agentprotocol.ServiceTailnetSpec
	if len(r.Applied) > 0 && json.Unmarshal(r.Applied, &spec) == nil {
		v.Web = spec.Web
		for _, f := range spec.TCP {
			v.TCP = append(v.TCP, tailnetcfg.TCPForward{Listen: f.Listen, Target: f.Target, TLS: f.TLS})
		}
	}
	if v.Web && r.Fqdn != "" {
		scheme := "https"
		if r.Provider == agentprotocol.TailnetProviderHeadscale {
			scheme = "http"
		}
		v.URL = scheme + "://" + r.Fqdn
	}
	return v
}

// decorateTailnet fills ServiceView.Tailnet for every row (out is parallel to rows).
func decorateTailnet(ctx context.Context, q *db.Queries, rows []db.Service, out []ServiceView) error {
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	trs, err := q.ListServiceTailnetForServices(ctx, ids)
	if err != nil {
		return fmt.Errorf("list service tailnets: %w", err)
	}
	if len(trs) == 0 {
		return nil
	}
	by := make(map[uuid.UUID]db.ServiceTailnet, len(trs))
	for _, t := range trs {
		by[t.ServiceID] = t
	}
	for i, r := range rows {
		if t, ok := by[r.ID]; ok {
			v := TailnetViewOf(t)
			out[i].Tailnet = &v
		}
	}
	return nil
}

// validateTailnet checks the static part of config.tailnet (the integration
// dependent checks run in TailnetHook.ValidateConfig).
func (c Config) validateTailnet() error {
	if c.Tailnet == nil {
		return nil
	}
	if c.Route != nil {
		return invalid("tailnet does not apply to route services")
	}
	if c.OneShot {
		return invalid("a one-shot service cannot be on a tailnet")
	}
	if err := c.Tailnet.Validate(c.Port); err != nil {
		return invalid("tailnet: %s", err)
	}
	return nil
}

// TailnetHook is implemented by the tailnet domain (internal/tailnet). It keeps
// the services package free of the integration, provider and agent details.
type TailnetHook interface {
	// ValidateConfig checks a changed config.tailnet against the workspace's
	// integration: the integration exists, the extra tags are allowed and the
	// features (Funnel, TLS ports, HTTPS certificates) exist for its provider.
	ValidateConfig(ctx context.Context, sc Scope, cfg tailnetcfg.TailnetConfig) error
	// ConfigChanged runs inside the update transaction after config.tailnet
	// changed (from/to are nil when the service is not on a tailnet); it records
	// the audit entries.
	ConfigChanged(ctx context.Context, tx pgx.Tx, sc Scope, from, to *tailnetcfg.TailnetConfig) error
	// ServiceDeleted runs inside the delete transaction: it queues the removal
	// of the node's device and forgets the node.
	ServiceDeleted(ctx context.Context, tx pgx.Tx, sc Scope) error
}

// SetTailnetHook installs the hook (wire time only).
func (s *Service) SetTailnetHook(h TailnetHook) { s.tailnetHook = h }

// tailnetChanged reports whether the desired tailnet differs.
func tailnetChanged(a, b *tailnetcfg.TailnetConfig) bool {
	if a == nil || b == nil {
		return a != b
	}
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) != string(bj)
}
