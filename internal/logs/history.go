package logs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// HistoryParams selects a page of stored runtime log.
type HistoryParams struct {
	From, To time.Time // zero = open
	Query    string
	Limit    int
	Cursor   string
	Order    string // desc (default, newest first) or asc
	// Component limits a stack's history to one component: its lines carry the
	// prefix `<component> | `. Only valid for ServiceHistory of a stack.
	Component string
	// Process limits the history of a service with processes to one process:
	// its lines carry the prefix `<process> | `. Only valid for ServiceHistory,
	// and not together with Component.
	Process string
}

// LogHistoryEntry is one stored line, redacted.
type LogHistoryEntry struct {
	TS           time.Time `json:"ts"`
	Line         string    `json:"line"`
	ServiceID    uuid.UUID `json:"serviceId"`
	ServiceName  string    `json:"serviceName,omitempty"`
	Replica      int       `json:"replica"`
	DeploymentID string    `json:"deploymentId,omitempty"`
	Stream       string    `json:"stream"`
}

// LogHistoryUnavailable names a service whose history could not be read, and why.
type LogHistoryUnavailable struct {
	ServiceID   uuid.UUID `json:"serviceId"`
	ServiceName string    `json:"serviceName"`
	ServerID    uuid.UUID `json:"serverId"`
	ServerName  string    `json:"serverName"`
	Reason      string    `json:"reason" enum:"server_offline,agent_outdated,failed"`
	Message     string    `json:"message"`
}

// LogHistoryPage is a page of stored log lines in time order (newest first unless
// order=asc).
type LogHistoryPage struct {
	Items []LogHistoryEntry `json:"items"`
	// NextCursor continues the search; absent when the range is exhausted.
	NextCursor string `json:"nextCursor,omitempty"`
	// Partial: the agent stopped scanning before the page was full (a rare,
	// selective query over a lot of log); NextCursor continues the search.
	Partial     bool       `json:"partial,omitempty"`
	QueryErrors []string   `json:"queryErrors,omitempty" doc:"The parts of the query that were ignored."`
	Oldest      *time.Time `json:"oldest,omitempty" doc:"The oldest line the server still has."`
	Newest      *time.Time `json:"newest,omitempty"`
	// Capturing is false when history capture is switched off on the server.
	Capturing   bool                    `json:"capturing"`
	Unavailable []LogHistoryUnavailable `json:"unavailable,omitempty" doc:"Services whose history could not be read (their server is offline, for example)."`
}

const (
	defaultHistoryLimit = agentprotocol.LogQueryDefaultLimit
	maxHistoryLimit     = agentprotocol.LogQueryMaxLimit
)

func (p HistoryParams) limit() int {
	switch {
	case p.Limit <= 0:
		return defaultHistoryLimit
	case p.Limit > maxHistoryLimit:
		return maxHistoryLimit
	}
	return p.Limit
}

func (p HistoryParams) order() string {
	if p.Order == agentprotocol.LogOrderAsc {
		return agentprotocol.LogOrderAsc
	}
	return agentprotocol.LogOrderDesc
}

func (p HistoryParams) validate() error {
	if !p.From.IsZero() && !p.To.IsZero() && !p.From.Before(p.To) {
		return bad("from must be before to")
	}
	if len(p.Query) > 2000 {
		return bad("the query is too long (at most 2000 characters)")
	}
	switch p.Order {
	case "", agentprotocol.LogOrderAsc, agentprotocol.LogOrderDesc:
	default:
		return bad("order must be desc or asc")
	}
	return nil
}

// serverOf returns where a service's log lives: its placement, else the server
// of its current deployment.
func (s *Service) serverOf(ctx context.Context, svc db.Service) (uuid.UUID, bool, error) {
	if svc.ServerID != nil {
		return *svc.ServerID, true, nil
	}
	if svc.CurrentDeploymentID == nil {
		return uuid.Nil, false, nil
	}
	deps, err := s.q.ListDeploymentsByIDs(ctx, []uuid.UUID{*svc.CurrentDeploymentID})
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("current deployment: %w", err)
	}
	if len(deps) != 1 || deps[0].ServerID == nil {
		return uuid.Nil, false, nil
	}
	return *deps[0].ServerID, true, nil
}

// queryOne asks the service's server for one page. unavail is set (and the
// result empty) when the server cannot answer.
func (s *Service) queryOne(ctx context.Context, svc db.Service, p HistoryParams, cursor string) (res agentprotocol.LogsQueryResult, unavail *LogHistoryUnavailable, err error) {
	serverID, ok, err := s.serverOf(ctx, svc)
	if err != nil {
		return res, nil, err
	}
	if !ok {
		return agentprotocol.LogsQueryResult{Lines: []agentprotocol.HistoryLine{}, Capturing: true}, nil, nil // never deployed: nothing was written
	}
	name := ""
	if srv, err := s.q.LoadServerRows(ctx, []uuid.UUID{serverID}); err == nil && len(srv) == 1 {
		name = srv[0].Name
	}
	un := func(reason, msg string) *LogHistoryUnavailable {
		return &LogHistoryUnavailable{ServiceID: svc.ID, ServiceName: svc.Name, ServerID: serverID, ServerName: name, Reason: reason, Message: msg}
	}
	q := agentprotocol.LogsQuery{
		ServiceID: svc.ID.String(), ServiceName: svc.Name, From: p.From, To: p.To, Query: p.agentQuery(),
		Limit: p.limit(), Cursor: cursor, Order: p.order(),
	}
	frame, err := s.hub.Request(ctx, serverID, agentprotocol.KindLogsQuery, q, nil, s.QueryTimeout)
	if err != nil {
		if ae, ok := apierr.As(err); ok && ae.Code == "server_offline" {
			return res, un("server_offline", "History is stored on "+orServer(name)+" and is unavailable while it is offline."), nil
		}
		return res, nil, mapRequestError(err)
	}
	if frame.Outcome != agentprotocol.OutcomeSucceeded {
		msg := agentFailure(frame)
		switch {
		case strings.Contains(msg, "not allowed") || strings.Contains(msg, "unsupported command"):
			return res, un("agent_outdated", "The agent on "+orServer(name)+" is too old to read log history. Update it."), nil
		case strings.Contains(msg, "invalid cursor"):
			return res, nil, apierr.BadRequest("invalid_cursor", "the cursor is not valid")
		}
		return res, un("failed", msg), nil
	}
	if err := json.Unmarshal(frame.Data, &res); err != nil {
		return res, nil, fmt.Errorf("decode history result: %w", err)
	}
	return res, nil, nil
}

func orServer(name string) string {
	if name == "" {
		return "its server"
	}
	return name
}

// ServiceHistory returns a page of one service's stored runtime log.
func (s *Service) ServiceHistory(ctx context.Context, serviceID uuid.UUID, p HistoryParams) (LogHistoryPage, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeLogsRead, serviceID)
	if err != nil {
		return LogHistoryPage{}, err
	}
	if err := services.RequireContainer(sc, "runtime logs"); err != nil {
		return LogHistoryPage{}, err
	}
	if err := p.validate(); err != nil {
		return LogHistoryPage{}, err
	}
	if err := services.CheckLogFilter(sc, p.Component, p.Process); err != nil {
		return LogHistoryPage{}, err
	}
	res, un, err := s.queryOne(ctx, sc.Service, p, p.Cursor)
	if err != nil {
		return LogHistoryPage{}, err
	}
	if un != nil {
		status := http.StatusBadGateway
		code := "history_unavailable"
		if un.Reason == "server_offline" {
			status, code = http.StatusServiceUnavailable, "server_offline"
		} else if un.Reason == "agent_outdated" {
			status, code = http.StatusConflict, "agent_outdated"
		}
		return LogHistoryPage{}, apierr.New(status, code, un.Message).WithDetails(map[string]any{"serverId": un.ServerID, "serverName": un.ServerName})
	}
	redact, err := s.redactor(ctx, serviceID)
	if err != nil {
		return LogHistoryPage{}, err
	}
	page := LogHistoryPage{
		Items: make([]LogHistoryEntry, 0, len(res.Lines)), NextCursor: res.NextCursor, Partial: res.Partial,
		QueryErrors: res.QueryErrors, Oldest: res.Oldest, Newest: res.Newest, Capturing: res.Capturing,
	}
	prefix := p.linePrefix()
	for _, l := range res.Lines {
		if prefix != "" && !strings.HasPrefix(l.Text, prefix) {
			continue // the agent's phrase match is a substring test; the prefix is exact
		}
		page.Items = append(page.Items, entryOf(l, serviceID, "", redact))
	}
	sortEntries(page.Items, p.order())
	return page, nil
}

func (s *Service) redactor(ctx context.Context, serviceID uuid.UUID) (func(string) string, error) {
	if s.red == nil {
		return func(l string) string { return l }, nil
	}
	fn, err := s.red.RedactorFor(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("redactor: %w", err)
	}
	return fn, nil
}

func entryOf(l agentprotocol.HistoryLine, id uuid.UUID, name string, redact func(string) string) LogHistoryEntry {
	return LogHistoryEntry{
		TS: l.TS, Line: redact(l.Text), ServiceID: id, ServiceName: name, Replica: l.Replica,
		DeploymentID: l.DeploymentID, Stream: agentprotocol.StreamRuntime,
	}
}

func sortEntries(items []LogHistoryEntry, order string) {
	sort.SliceStable(items, func(i, j int) bool {
		if order == agentprotocol.LogOrderAsc {
			return items[i].TS.Before(items[j].TS)
		}
		return items[i].TS.After(items[j].TS)
	})
}

// ---- environment ---------------------------------------------------------------------

// envCursor remembers, per service, where the next page continues.
type envCursor struct {
	C    map[string]string `json:"c,omitempty"` // service ID → agent cursor
	Done []string          `json:"d,omitempty"` // services with nothing more
}

func (c envCursor) encode() string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeEnvCursor(s string) (envCursor, error) {
	var c envCursor
	if s == "" {
		return c, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(raw, &c) != nil {
		return c, apierr.BadRequest("invalid_cursor", "the cursor is not valid")
	}
	return c, nil
}

type envResult struct {
	svc   db.Service
	res   agentprotocol.LogsQueryResult
	unav  *LogHistoryUnavailable
	err   error
	lines []agentprotocol.HistoryLine
	next  int // index of the next line to take
}

// EnvironmentHistory merges the stored runtime log of every service of an
// environment: each service is asked on its own server, and the pages are
// merged by time. Services whose server is offline are listed in Unavailable.
func (s *Service) EnvironmentHistory(ctx context.Context, envID uuid.UUID, p HistoryParams) (LogHistoryPage, error) {
	env, err := services.LoadEnvironment(ctx, s.q, policy.ScopeLogsRead, envID)
	if err != nil {
		return LogHistoryPage{}, err
	}
	if err := p.validate(); err != nil {
		return LogHistoryPage{}, err
	}
	cur, err := decodeEnvCursor(p.Cursor)
	if err != nil {
		return LogHistoryPage{}, err
	}
	all, err := s.q.ListAllEnvironmentServices(ctx, env.Env.ID)
	if err != nil {
		return LogHistoryPage{}, fmt.Errorf("list services: %w", err)
	}
	var asked []*envResult
	for _, svc := range all {
		if svc.Kind == services.KindRoute || slicesContains(cur.Done, svc.ID.String()) {
			continue
		}
		asked = append(asked, &envResult{svc: svc})
	}
	page := LogHistoryPage{Items: []LogHistoryEntry{}, Capturing: len(asked) == 0}
	if len(asked) == 0 {
		return page, nil
	}
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, r := range asked {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r.res, r.unav, r.err = s.queryOne(ctx, r.svc, p, cur.C[r.svc.ID.String()])
			r.lines = r.res.Lines
		}()
	}
	wg.Wait()

	redactors := map[uuid.UUID]func(string) string{}
	for _, r := range asked {
		if r.err != nil {
			return LogHistoryPage{}, r.err
		}
		if r.unav != nil {
			page.Unavailable = append(page.Unavailable, *r.unav)
			continue
		}
		if r.res.Capturing {
			page.Capturing = true
		}
		page.QueryErrors = r.res.QueryErrors
		if r.res.Partial {
			page.Partial = true
		}
		red, err := s.redactor(ctx, r.svc.ID)
		if err != nil {
			return LogHistoryPage{}, err
		}
		redactors[r.svc.ID] = red
		if r.res.Oldest != nil && (page.Oldest == nil || r.res.Oldest.Before(*page.Oldest)) {
			page.Oldest = r.res.Oldest
		}
		if r.res.Newest != nil && (page.Newest == nil || r.res.Newest.After(*page.Newest)) {
			page.Newest = r.res.Newest
		}
	}

	// k-way merge by time. Each service's lines stay in the order the agent
	// returned them (by sequence), so what is taken from one is always a prefix
	// and its cursor is the sequence number of the last line taken.
	desc := p.order() == agentprotocol.LogOrderDesc
	limit := p.limit()
	taken := map[uuid.UUID]uint64{}
	for len(page.Items) < limit {
		var best *envResult
		for _, r := range asked {
			if r.unav != nil || r.next >= len(r.lines) {
				continue
			}
			if best == nil {
				best = r
				continue
			}
			a, b := r.lines[r.next].TS, best.lines[best.next].TS
			if (desc && a.After(b)) || (!desc && a.Before(b)) {
				best = r
			}
		}
		if best == nil {
			break
		}
		l := best.lines[best.next]
		best.next++
		taken[best.svc.ID] = l.Seq
		page.Items = append(page.Items, entryOf(l, best.svc.ID, best.svc.Name, redactors[best.svc.ID]))
	}

	next := envCursor{C: map[string]string{}}
	more := false
	next.Done = append(next.Done, cur.Done...)
	for _, r := range asked {
		id := r.svc.ID.String()
		if r.unav != nil {
			// Not readable now: keep the position so a later page can still ask.
			if c := cur.C[id]; c != "" {
				next.C[id] = c
			}
			continue
		}
		leftover := r.next < len(r.lines)
		switch {
		case leftover && r.next == 0:
			next.C[id] = cur.C[id] // nothing taken: ask again from where it was
			more = true
		case leftover:
			next.C[id] = fmt.Sprint(taken[r.svc.ID])
			more = true
		case r.res.NextCursor != "":
			next.C[id] = r.res.NextCursor
			more = true
		default:
			next.Done = append(next.Done, id)
		}
	}
	if more {
		page.NextCursor = next.encode()
	}
	return page, nil
}

func slicesContains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// stackLinePrefix follows the component or process name in every line of a
// stack's or a multi-process service's log: `<name> | `.
const stackLinePrefix = " | "

// linePrefix is the prefix the lines of the selected component or process
// carry; "" when none is selected.
func (p HistoryParams) linePrefix() string {
	switch {
	case p.Component != "":
		return p.Component + stackLinePrefix
	case p.Process != "":
		return p.Process + stackLinePrefix
	}
	return ""
}

// agentQuery is the query the agent evaluates: the user's query, narrowed by
// the line prefix of the component or process (a phrase, so the agent skips
// other lines before the page fills; the exact prefix test happens when the
// page arrives).
func (p HistoryParams) agentQuery() string {
	prefix := p.linePrefix()
	if prefix == "" {
		return p.Query
	}
	phrase := `"` + prefix + `"`
	if strings.TrimSpace(p.Query) == "" {
		return phrase
	}
	return "(" + p.Query + ") " + phrase
}
