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

// HistoryParams selects a page of stored log.
type HistoryParams struct {
	From, To time.Time // zero = open
	Query    string
	Limit    int
	Cursor   string
	Order    string // desc (default, newest first) or asc
	// Sources is a comma-separated subset of runtime and requests: the stores
	// to read (default runtime).
	Sources string
	// Component limits a stack's history to one component: its lines carry the
	// prefix `<component> | `. Only valid for ServiceHistory of a stack.
	Component string
	// Process limits the history of a service with processes to one process:
	// its lines carry the prefix `<process> | `. Only valid for ServiceHistory,
	// and not together with Component.
	Process string
}

// LogHistoryEntry is one stored line, redacted. Stream is the source the line
// was read from: runtime or requests.
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

// logSources are the stores a query can read, in the order they are asked.
var logSources = []string{agentprotocol.LogSourceRuntime, agentprotocol.LogSourceRequests}

// parseSources turns a `sources` value (a comma-separated subset of runtime
// and requests; empty means runtime) into its distinct sources in the order of
// logSources.
func parseSources(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{agentprotocol.LogSourceRuntime}, nil
	}
	want := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if !slicesContains(logSources, part) {
			return nil, bad(fmt.Sprintf("sources must be a comma-separated list of %s (got %q)", strings.Join(logSources, ", "), part))
		}
		want[part] = true
	}
	out := make([]string, 0, len(want))
	for _, source := range logSources {
		if want[source] {
			out = append(out, source)
		}
	}
	return out, nil
}

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
	_, err := parseSources(p.Sources)
	return err
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

// askAgent sends one command to the server that stores the service's log and
// decodes the answer into R. never is the answer for a service that was never
// deployed: nothing was written; answered is false for it, true when an agent
// replied. unavail is set (and the result empty) when the server cannot
// answer, including a requests question for an agent that does not advertise
// agentprotocol.CapRequestLogs (it is not asked).
func askAgent[R any](ctx context.Context, s *Service, svc db.Service, source, kind string, payload any, never R) (res R, answered bool, unavail *LogHistoryUnavailable, err error) {
	serverID, ok, err := s.serverOf(ctx, svc)
	if err != nil {
		return res, false, nil, err
	}
	if !ok {
		return never, false, nil, nil
	}
	var srv *db.Server
	if rows, err := s.q.LoadServerRows(ctx, []uuid.UUID{serverID}); err == nil && len(rows) == 1 {
		srv = &rows[0]
	}
	name := ""
	if srv != nil {
		name = srv.Name
	}
	un := func(reason, msg string) *LogHistoryUnavailable {
		return &LogHistoryUnavailable{ServiceID: svc.ID, ServiceName: svc.Name, ServerID: serverID, ServerName: name, Reason: reason, Message: msg}
	}
	if source == agentprotocol.LogSourceRequests && srv != nil {
		// Unknown facts (never connected) count as current.
		if has, known := services.ServerHasCapability(*srv, agentprotocol.CapRequestLogs); known && !has {
			return res, false, un("agent_outdated", "The agent on "+orServer(name)+" is too old to read request logs. Update it."), nil
		}
	}
	frame, err := s.hub.Request(ctx, serverID, kind, payload, nil, s.QueryTimeout)
	if err != nil {
		if ae, ok := apierr.As(err); ok && ae.Code == "server_offline" {
			return res, false, un("server_offline", "History is stored on "+orServer(name)+" and is unavailable while it is offline."), nil
		}
		return res, false, nil, mapRequestError(err)
	}
	if frame.Outcome != agentprotocol.OutcomeSucceeded {
		msg := agentFailure(frame)
		switch {
		case strings.Contains(msg, "not allowed") || strings.Contains(msg, "unsupported command"):
			return res, false, un("agent_outdated", "The agent on "+orServer(name)+" is too old to read log history. Update it."), nil
		case strings.Contains(msg, "invalid cursor"):
			return res, false, nil, apierr.BadRequest("invalid_cursor", "the cursor is not valid")
		}
		return res, false, un("failed", msg), nil
	}
	if err := json.Unmarshal(frame.Data, &res); err != nil {
		return res, false, nil, fmt.Errorf("decode %s result: %w", kind, err)
	}
	return res, true, nil, nil
}

// queryOne asks the service's server for one page of one source. unavail is
// set (and the result empty) when the server cannot answer.
func (s *Service) queryOne(ctx context.Context, svc db.Service, source string, p HistoryParams, cursor string) (agentprotocol.LogsQueryResult, *LogHistoryUnavailable, error) {
	q := agentprotocol.LogsQuery{
		ServiceID: svc.ID.String(), ServiceName: svc.Name, Source: source, From: p.From, To: p.To, Query: p.agentQuery(source),
		Limit: p.limit(), Cursor: cursor, Order: p.order(),
	}
	never := agentprotocol.LogsQueryResult{Lines: []agentprotocol.HistoryLine{}, Capturing: true}
	res, _, un, err := askAgent(ctx, s, svc, source, agentprotocol.KindLogsQuery, q, never)
	return res, un, err
}

func orServer(name string) string {
	if name == "" {
		return "its server"
	}
	return name
}

// unavailableError is the failure of a single-service request whose server
// cannot answer.
func unavailableError(un LogHistoryUnavailable) error {
	status := http.StatusBadGateway
	code := "history_unavailable"
	if un.Reason == "server_offline" {
		status, code = http.StatusServiceUnavailable, "server_offline"
	} else if un.Reason == "agent_outdated" {
		status, code = http.StatusConflict, "agent_outdated"
	}
	return apierr.New(status, code, un.Message).WithDetails(map[string]any{"serverId": un.ServerID, "serverName": un.ServerName})
}

// appendUnavailable lists a service once, however many of its sources failed.
func appendUnavailable(list []LogHistoryUnavailable, un LogHistoryUnavailable) []LogHistoryUnavailable {
	for _, have := range list {
		if have.ServiceID == un.ServiceID {
			return list
		}
	}
	return append(list, un)
}

// appendUnique appends the messages not yet in list.
func appendUnique(list, add []string) []string {
	for _, m := range add {
		if !slicesContains(list, m) {
			list = append(list, m)
		}
	}
	return list
}

// ServiceHistory returns a page of one service's stored log: its runtime
// output, its request lines, or both merged by time (Sources).
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
	sources, err := parseSources(p.Sources)
	if err != nil {
		return LogHistoryPage{}, err
	}
	if err := services.CheckLogFilter(sc, p.Component, p.Process); err != nil {
		return LogHistoryPage{}, err
	}
	// The line prefix is the release's process name, which a staged rename or
	// a just-staged process leaves different from the requested one.
	if p.Process, _, _, err = services.ResolveServiceProcessFilter(ctx, s.q, sc.Service, p.Process); err != nil {
		return LogHistoryPage{}, err
	}
	if len(sources) > 1 {
		page, err := s.mergeHistory(ctx, []db.Service{sc.Service}, sources, p)
		if err != nil {
			return LogHistoryPage{}, err
		}
		if len(page.Unavailable) > 0 {
			return LogHistoryPage{}, unavailableError(page.Unavailable[0])
		}
		return page, nil
	}
	source := sources[0]
	res, un, err := s.queryOne(ctx, sc.Service, source, p, p.Cursor)
	if err != nil {
		return LogHistoryPage{}, err
	}
	if un != nil {
		return LogHistoryPage{}, unavailableError(*un)
	}
	redact, err := s.redactor(ctx, serviceID)
	if err != nil {
		return LogHistoryPage{}, err
	}
	page := LogHistoryPage{
		Items: make([]LogHistoryEntry, 0, len(res.Lines)), NextCursor: res.NextCursor, Partial: res.Partial,
		QueryErrors: res.QueryErrors, Oldest: res.Oldest, Newest: res.Newest, Capturing: res.Capturing,
	}
	for _, l := range keepLines(res.Lines, p.prefixOf(source)) {
		page.Items = append(page.Items, entryOf(l, serviceID, "", source, redact))
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

func entryOf(l agentprotocol.HistoryLine, id uuid.UUID, name, source string, redact func(string) string) LogHistoryEntry {
	return LogHistoryEntry{
		TS: l.TS, Line: redact(l.Text), ServiceID: id, ServiceName: name, Replica: l.Replica,
		DeploymentID: l.DeploymentID, Stream: source,
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

// keepLines drops the lines that do not start with prefix (all stay when
// prefix is empty): the agent's phrase match is a substring test, the prefix
// is exact.
func keepLines(lines []agentprotocol.HistoryLine, prefix string) []agentprotocol.HistoryLine {
	if prefix == "" {
		return lines
	}
	kept := make([]agentprotocol.HistoryLine, 0, len(lines))
	for _, l := range lines {
		if strings.HasPrefix(l.Text, prefix) {
			kept = append(kept, l)
		}
	}
	return kept
}

// maxParallelAsks caps how many agents one request asks at once.
const maxParallelAsks = 8

// forEachParallel runs fn for every item, at most maxParallelAsks at a time,
// and returns when all are done.
func forEachParallel[T any](items []T, fn func(T)) {
	sem := make(chan struct{}, maxParallelAsks)
	var wg sync.WaitGroup
	for _, item := range items {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fn(item)
		}()
	}
	wg.Wait()
}

// ---- environment ---------------------------------------------------------------------

// cursorKey names one (service, source) pair in an envCursor.
func cursorKey(serviceID uuid.UUID, source string) string {
	return serviceID.String() + "/" + source
}

// envCursor remembers, per (service, source) pair, where the next page continues.
type envCursor struct {
	C    map[string]string `json:"c,omitempty"` // "<serviceId>/<source>" → agent cursor
	Done []string          `json:"d,omitempty"` // pairs with nothing more
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
	// A cursor of the time before sources named bare service IDs: they read
	// the runtime store.
	if len(c.C) > 0 {
		upgraded := make(map[string]string, len(c.C))
		for key, v := range c.C {
			upgraded[upgradeCursorKey(key)] = v
		}
		c.C = upgraded
	}
	for i, key := range c.Done {
		c.Done[i] = upgradeCursorKey(key)
	}
	return c, nil
}

// upgradeCursorKey names the runtime pair of a bare service ID, the key of a
// cursor from before sources existed; other keys stay.
func upgradeCursorKey(key string) string {
	if strings.Contains(key, "/") {
		return key
	}
	return key + "/" + agentprotocol.LogSourceRuntime
}

// envResult is the answer for one (service, source) pair.
type envResult struct {
	svc    db.Service
	source string
	key    string // cursorKey of the pair
	res    agentprotocol.LogsQueryResult
	unav   *LogHistoryUnavailable
	err    error
	lines  []agentprotocol.HistoryLine
	next   int    // index of the next line to take
	taken  uint64 // sequence number of the last line taken
}

// EnvironmentHistory merges the stored log of every service of an environment:
// each service is asked on its own server, once per source, and the pages are
// merged by time. Services whose server is offline are listed in Unavailable.
func (s *Service) EnvironmentHistory(ctx context.Context, envID uuid.UUID, p HistoryParams) (LogHistoryPage, error) {
	env, err := services.LoadEnvironment(ctx, s.q, policy.ScopeLogsRead, envID)
	if err != nil {
		return LogHistoryPage{}, err
	}
	if err := p.validate(); err != nil {
		return LogHistoryPage{}, err
	}
	sources, err := parseSources(p.Sources)
	if err != nil {
		return LogHistoryPage{}, err
	}
	all, err := s.q.ListAllEnvironmentServices(ctx, env.Env.ID)
	if err != nil {
		return LogHistoryPage{}, fmt.Errorf("list services: %w", err)
	}
	svcs := make([]db.Service, 0, len(all))
	for _, svc := range all {
		if svc.Kind != services.KindRoute {
			svcs = append(svcs, svc)
		}
	}
	return s.mergeHistory(ctx, svcs, sources, p)
}

// mergeHistory asks the server of every service for every source and merges
// the pages by time. The cursor covers every (service, source) pair.
func (s *Service) mergeHistory(ctx context.Context, svcs []db.Service, sources []string, p HistoryParams) (LogHistoryPage, error) {
	cur, err := decodeEnvCursor(p.Cursor)
	if err != nil {
		return LogHistoryPage{}, err
	}
	var asked []*envResult
	for _, svc := range svcs {
		for _, source := range sources {
			key := cursorKey(svc.ID, source)
			if slicesContains(cur.Done, key) {
				continue
			}
			asked = append(asked, &envResult{svc: svc, source: source, key: key})
		}
	}
	page := LogHistoryPage{Items: []LogHistoryEntry{}, Capturing: len(asked) == 0}
	if len(asked) == 0 {
		return page, nil
	}
	forEachParallel(asked, func(r *envResult) {
		r.res, r.unav, r.err = s.queryOne(ctx, r.svc, r.source, p, cur.C[r.key])
		r.lines = keepLines(r.res.Lines, p.prefixOf(r.source))
	})

	redactors := map[uuid.UUID]func(string) string{}
	for _, r := range asked {
		if r.err != nil {
			return LogHistoryPage{}, r.err
		}
		if r.unav != nil {
			page.Unavailable = appendUnavailable(page.Unavailable, *r.unav)
			continue
		}
		if r.res.Capturing {
			page.Capturing = true
		}
		page.QueryErrors = appendUnique(page.QueryErrors, r.res.QueryErrors)
		if r.res.Partial {
			page.Partial = true
		}
		if _, ok := redactors[r.svc.ID]; !ok {
			red, err := s.redactor(ctx, r.svc.ID)
			if err != nil {
				return LogHistoryPage{}, err
			}
			redactors[r.svc.ID] = red
		}
		if r.res.Oldest != nil && (page.Oldest == nil || r.res.Oldest.Before(*page.Oldest)) {
			page.Oldest = r.res.Oldest
		}
		if r.res.Newest != nil && (page.Newest == nil || r.res.Newest.After(*page.Newest)) {
			page.Newest = r.res.Newest
		}
	}

	// k-way merge by time. Each pair's lines stay in the order the agent
	// returned them (by sequence), so what is taken from one is always a prefix
	// and its cursor is the sequence number of the last line taken.
	desc := p.order() == agentprotocol.LogOrderDesc
	limit := p.limit()
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
		best.taken = l.Seq
		page.Items = append(page.Items, entryOf(l, best.svc.ID, best.svc.Name, best.source, redactors[best.svc.ID]))
	}

	next := envCursor{C: map[string]string{}}
	more := false
	next.Done = append(next.Done, cur.Done...)
	for _, r := range asked {
		if r.unav != nil {
			// Not readable now: keep the position so a later page can still ask.
			if c := cur.C[r.key]; c != "" {
				next.C[r.key] = c
			}
			continue
		}
		leftover := r.next < len(r.lines)
		switch {
		case leftover && r.next == 0:
			next.C[r.key] = cur.C[r.key] // nothing taken: ask again from where it was
			more = true
		case leftover:
			next.C[r.key] = fmt.Sprint(r.taken)
			more = true
		case r.res.NextCursor != "":
			next.C[r.key] = r.res.NextCursor
			more = true
		default:
			next.Done = append(next.Done, r.key)
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

// prefixOf is the exact prefix the lines of a source must carry: only runtime
// lines are prefixed (request lines carry their process and component as
// attributes).
func (p HistoryParams) prefixOf(source string) string {
	if source == agentprotocol.LogSourceRequests {
		return ""
	}
	return p.linePrefix()
}

// quoteTerm writes v as a quoted value of the log search language.
func quoteTerm(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}

// requestTerm is the query term that selects the request lines of the
// component or process: they carry both as attributes.
func (p HistoryParams) requestTerm() string {
	switch {
	case p.Component != "":
		return "@component:" + quoteTerm(p.Component)
	case p.Process != "":
		return "@process:" + quoteTerm(p.Process)
	}
	return ""
}

// agentQuery is the query the agent evaluates for a source: the user's query,
// narrowed by the component or process. Runtime lines are narrowed by their
// line prefix, as a phrase, so the agent skips other lines before the page
// fills (the exact prefix test happens when the page arrives); request lines
// by their @component or @process attribute.
func (p HistoryParams) agentQuery(source string) string {
	term := ""
	if source == agentprotocol.LogSourceRequests {
		term = p.requestTerm()
	} else if prefix := p.linePrefix(); prefix != "" {
		term = `"` + prefix + `"`
	}
	if term == "" {
		return p.Query
	}
	if strings.TrimSpace(p.Query) == "" {
		return term
	}
	return "(" + p.Query + ") " + term
}
