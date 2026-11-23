package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Kinds of FirewallRuleCount.
const (
	// FirewallKindStandard is a rule of the standard protection (domains.StandardRules).
	FirewallKindStandard = "standard"
	// FirewallKindRule is a rule of the service's own firewall.
	FirewallKindRule = "rule"
	// FirewallKindRemoved is a rule id the service no longer has: its blocks are
	// still in the window, the rule was deleted since.
	FirewallKindRemoved = "removed"
)

// FirewallRuleCount is the number of requests one firewall rule blocked.
type FirewallRuleCount struct {
	ID   string `json:"id" doc:"The rule's id; ids starting with std belong to the standard protection"`
	Name string `json:"name" doc:"The rule's name; the id for a removed rule"`
	Kind string `json:"kind" enum:"standard,rule,removed"`
	// Action is the rule's current action (absent for a removed rule).
	Action   string `json:"action,omitempty" enum:"block,allow"`
	Requests int64  `json:"requests"`
}

// FirewallPoint is the number of blocked requests in one step of the chart.
type FirewallPoint struct {
	TS      time.Time `json:"ts"`
	Blocked int64     `json:"blocked"`
}

// FirewallReport says how many requests the service firewall blocked in the
// window. The counts come from the agent's access-log aggregates, so they exist
// only while traffic analytics is on for the server and only from the moment
// the agent first reported them (nothing is backfilled).
type FirewallReport struct {
	Blocked int64               `json:"blocked" doc:"Requests blocked by the firewall (own rules and standard protection); they are also part of requests and of the 4xx count"`
	ByRule  []FirewallRuleCount `json:"byRule" doc:"Blocked requests per rule, largest first. Service reports name every rule; server and workspace reports name only the standard protection and list the other rules by id"`
	Series  []FirewallPoint     `json:"series" doc:"Blocked requests per chart step, on the same x axis as the report's series"`
}

type ruleInfo struct{ name, kind, action string }

// ruleBook resolves the rule ids blocked requests are counted under. It holds
// the standard protection and, for a service, its own rules.
type ruleBook map[string]ruleInfo

// newRuleBook returns the book of a service whose firewall column is raw (NULL
// for none); a nil raw gives the standard protection alone.
func newRuleBook(raw json.RawMessage) ruleBook {
	book := ruleBook{}
	for _, s := range domains.StandardRules() {
		book[s.Rule.ID] = ruleInfo{s.Rule.Name, FirewallKindStandard, s.Rule.Action}
	}
	// Requests the edge guard blocked for a banned address carry the reserved
	// rule id autoban; no rule of the service can have it.
	book[agentprotocol.FirewallRuleAutoBan] = ruleInfo{"Auto-ban", FirewallKindRule, agentprotocol.FirewallBlock}
	if fw := domains.DecodeFirewall(raw); fw != nil {
		for _, r := range fw.Rules {
			name := r.Name
			if name == "" {
				name = r.ID
			}
			book[r.ID] = ruleInfo{name, FirewallKindRule, r.Action}
		}
	}
	return book
}

// counts names the per-rule counts, largest first. exact says the book holds
// every rule of the counted services: an unknown id is then a removed rule;
// otherwise it is a rule of some service, listed by its id.
func (b ruleBook) counts(by map[string]int64, exact bool) []FirewallRuleCount {
	out := make([]FirewallRuleCount, 0, len(by))
	for id, n := range by {
		if n <= 0 {
			continue
		}
		c := FirewallRuleCount{ID: id, Name: id, Kind: FirewallKindRule, Requests: n}
		switch info, ok := b[id]; {
		case ok:
			c.Name, c.Kind, c.Action = info.name, info.kind, info.action
		case exact || domains.IsStandardRuleID(id):
			c.Kind = FirewallKindRemoved
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// firewallReport builds the report section from the window total and the
// chart steps. book is nil for a report over several services.
func firewallReport(total *sum, steps []*sum, first time.Time, step time.Duration, book ruleBook) *FirewallReport {
	exact := book != nil
	if book == nil {
		book = newRuleBook(nil)
	}
	f := &FirewallReport{Blocked: total.Blocked, ByRule: book.counts(total.blocked, exact), Series: make([]FirewallPoint, len(steps))}
	for i, s := range steps {
		f.Series[i] = FirewallPoint{TS: first.Add(time.Duration(i) * step), Blocked: s.Blocked}
	}
	return f
}

// BlocksLogsQuery finds a service's blocked requests in the Logs explorer's
// query language; the explorer scopes it to the service. Its facets then
// answer which paths, client networks (/24, /48), countries and user agents
// were blocked, from the host's request store.
const BlocksLogsQuery = "stream:requests @blocked_by:*"

// FirewallBlocks is the blocks view of one service.
type FirewallBlocks struct {
	Range string    `json:"range" enum:"24h,7d,30d"`
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
	// TrafficAnalytics is false when the service's server does not count
	// traffic (or the service has no server): nothing is counted then, however
	// many requests the firewall blocks.
	TrafficAnalytics bool                `json:"trafficAnalytics"`
	Total            int64               `json:"total" doc:"Requests blocked in the window"`
	ByRule           []FirewallRuleCount `json:"byRule" doc:"Blocked requests per rule, largest first"`
	LogsQuery        string              `json:"logsQuery" doc:"Logs explorer query for the blocked request lines (kept on the host, only the matching page or counts come back). Its facets list the blocked paths, client networks, countries and user agents; request logs must be on for the server"`
}

// ServiceFirewallBlocks counts the requests the firewall of a service blocked
// in the last 24 hours, 7 or 30 days, per rule (scope projects:read).
func (s *Service) ServiceFirewallBlocks(ctx context.Context, serviceID uuid.UUID, rng string) (FirewallBlocks, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return FirewallBlocks{}, err
	}
	if rng != "7d" && rng != "30d" {
		rng = "24h"
	}
	serverIDs, err := s.workspaceServers(ctx)
	if err != nil {
		return FirewallBlocks{}, err
	}
	var own []uuid.UUID
	if sc.Service.ServerID != nil {
		own = []uuid.UUID{*sc.Service.ServerID}
	}
	ret, err := s.retentionFor(ctx, own)
	if err != nil {
		return FirewallBlocks{}, err
	}
	plan, _, err := planRange(rng, ret)
	if err != nil {
		return FirewallBlocks{}, err
	}
	now := s.now().UTC()
	from := now.Add(-plan.window)
	t := target{serverIDs: serverIDs, serviceIDs: []uuid.UUID{serviceID}}
	rows, err := s.fetch(ctx, plan.tier, t, from.Truncate(plan.tier.Width()), now.Add(time.Minute))
	if err != nil {
		return FirewallBlocks{}, err
	}
	by := map[string]int64{}
	var total int64
	for i := range rows {
		for id, n := range rows[i].Blocked {
			by[id] += n
			total += n
		}
	}
	out := FirewallBlocks{
		Range: rng, From: from, To: now, Total: total, LogsQuery: BlocksLogsQuery,
		ByRule: newRuleBook(sc.Service.Firewall).counts(by, true),
	}
	if len(own) > 0 {
		geo, err := s.geoSettingsFor(ctx, own)
		if err != nil {
			return FirewallBlocks{}, fmt.Errorf("traffic settings: %w", err)
		}
		out.TrafficAnalytics = geo.reason != GeoReasonTrafficDisabled
	}
	return out, nil
}
