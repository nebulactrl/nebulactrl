package logs

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// CentralHint is shown instead of lines while the team's built-in log drain is
// off (or the log store is not set up).
const CentralHint = "Central logs are off. Turn on Log forwarding in Team settings to collect logs here."

// Central log query bounds.
const (
	CentralDefaultWindow = time.Hour
	CentralMaxWindow     = 7 * 24 * time.Hour
	CentralDefaultLimit  = 200
	CentralMaxLimit      = 1000
	centralMaxQueryRunes = 500
)

// CentralEntry is one stored line of the central log store.
type CentralEntry struct {
	Time time.Time
	Line string
}

// CentralStream is the lines of one Loki label set.
type CentralStream struct {
	Labels  map[string]string
	Entries []CentralEntry
}

// CentralLogs reads a team's lines from the built-in log store (Loki). The
// tenant is the team's id and logql is built here; the signature is that of
// lokiadmin.Backend.QueryRange, adapted in internal/app because this package
// does not import the backend. Implementations return newest lines first, at
// most limit lines.
type CentralLogs interface {
	QueryRange(ctx context.Context, tenant, logql string, from, to time.Time, limit int) ([]CentralStream, error)
}

// SetCentralLogs wires the central log store; nil leaves central logs off.
func (s *Service) SetCentralLogs(c CentralLogs) { s.central = c }

// CentralParams selects the central lines of one service.
type CentralParams struct {
	// Query keeps the lines that contain this text (a plain substring).
	Query string
	// Process keeps the lines of one process of a service with processes: they
	// carry the prefix `<process> | `.
	Process string
	// Since is the start of the window: a duration (15m, 2h, 3d) back from To,
	// or an RFC 3339 time. Ignored when From is set; From and Since together are refused.
	Since    string
	From, To time.Time // zero = open: To defaults to now, From to To minus one hour
	Limit    int
}

// CentralLogLine is one line of the central log, newest first.
type CentralLogLine struct {
	TS     time.Time `json:"ts"`
	Server string    `json:"server" doc:"Name of the server the line came from."`
	Stream string    `json:"stream" doc:"stdout or stderr."`
	Line   string    `json:"line"`
}

// CentralLogPage is the answer of the central log view.
type CentralLogPage struct {
	Enabled bool             `json:"enabled" doc:"False while the team's built-in log drain is off: nothing is collected, and hint says how to turn it on."`
	Hint    string           `json:"hint" doc:"One line to show when enabled is false; empty otherwise."`
	Lines   []CentralLogLine `json:"lines" doc:"Newest first."`
}

// logqlString quotes s as a LogQL string literal. LogQL strings are Go-style
// double-quoted, so a quote, backslash or control character cannot end the
// literal or start another expression.
func logqlString(s string) string { return strconv.Quote(strings.ToValidUTF8(s, "\uFFFD")) }

// centralLogQL selects a service's lines, narrowed to the ones containing q
// and, for a process, the ones that start with its `<process> | ` prefix (a
// line filter here; the exact prefix test happens when the lines arrive).
// The stream selector is the service id the agent's Loki sink sets as a label;
// the team is the tenant, so no other team's lines can match.
func centralLogQL(serviceID uuid.UUID, q, process string) string {
	sel := "{service_id=" + logqlString(serviceID.String()) + "}"
	if q != "" {
		sel += " |= " + logqlString(q)
	}
	if process != "" {
		sel += " |= " + logqlString(process+stackLinePrefix)
	}
	return sel
}

// parseSince reads a duration back from now (15m, 2h, 3d) or an RFC 3339 time.
func parseSince(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	var d time.Duration
	if n, ok := strings.CutSuffix(raw, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days <= 0 || days > 3650 {
			return time.Time{}, bad("since must be a duration such as 15m, 2h or 3d, or an RFC 3339 time")
		}
		d = time.Duration(days) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(raw); err != nil || d <= 0 {
			return time.Time{}, bad("since must be a duration such as 15m, 2h or 3d, or an RFC 3339 time")
		}
	}
	return now.Add(-d), nil
}

// centralWindow resolves the time range: to defaults to now and never lies in
// the future, from to an hour before to; a longer window than a week is cut to
// its newest week.
func centralWindow(p CentralParams, now time.Time) (from, to time.Time, err error) {
	if !p.From.IsZero() && strings.TrimSpace(p.Since) != "" {
		return from, to, bad("use either since or from, not both")
	}
	to = p.To
	if to.IsZero() || to.After(now) {
		to = now
	}
	switch {
	case !p.From.IsZero():
		from = p.From
	case strings.TrimSpace(p.Since) != "":
		if from, err = parseSince(p.Since, to); err != nil {
			return from, to, err
		}
	default:
		from = to.Add(-CentralDefaultWindow)
	}
	if !from.Before(to) {
		return from, to, bad("from must be before to")
	}
	if to.Sub(from) > CentralMaxWindow {
		from = to.Add(-CentralMaxWindow)
	}
	return from, to, nil
}

func centralLimit(n int) int {
	switch {
	case n <= 0:
		return CentralDefaultLimit
	case n > CentralMaxLimit:
		return CentralMaxLimit
	}
	return n
}

func (p CentralParams) validate() error {
	if utf8.RuneCountInString(p.Query) > centralMaxQueryRunes {
		return bad(fmt.Sprintf("the query is too long (at most %d characters)", centralMaxQueryRunes))
	}
	return nil
}

// centralEnabled reports whether the team collects central logs: the log store
// is wired and the team's built-in drain exists and is on.
func (s *Service) centralEnabled(ctx context.Context, ws uuid.UUID) (bool, error) {
	if s.central == nil {
		return false, nil
	}
	d, err := s.q.GetBuiltinDrain(ctx, ws)
	if isNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get built-in drain: %w", err)
	}
	return d.Enabled, nil
}

// ServiceCentralLogs reads a service's lines from the central log store. It is
// authorized like the other service log reads (logs:read on the service's
// project) and reads only the caller's team: the team's id is the Loki tenant.
// While the team's built-in drain is off it returns no lines and CentralHint.
func (s *Service) ServiceCentralLogs(ctx context.Context, serviceID uuid.UUID, p CentralParams) (CentralLogPage, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeLogsRead, serviceID)
	if err != nil {
		return CentralLogPage{}, err
	}
	if err := services.RequireContainer(sc, "runtime logs"); err != nil {
		return CentralLogPage{}, err
	}
	if err := services.CheckLogProcess(sc, p.Process); err != nil {
		return CentralLogPage{}, err
	}
	if err := p.validate(); err != nil {
		return CentralLogPage{}, err
	}
	from, to, err := centralWindow(p, s.now())
	if err != nil {
		return CentralLogPage{}, err
	}
	on, err := s.centralEnabled(ctx, sc.WorkspaceID)
	if err != nil {
		return CentralLogPage{}, err
	}
	page := CentralLogPage{Lines: []CentralLogLine{}}
	if !on {
		page.Hint = CentralHint
		return page, nil
	}
	page.Enabled = true
	limit := centralLimit(p.Limit)
	qctx, cancel := context.WithTimeout(ctx, s.QueryTimeout)
	defer cancel()
	streams, err := s.central.QueryRange(qctx, sc.WorkspaceID.String(), centralLogQL(serviceID, strings.TrimSpace(p.Query), p.Process), from, to, limit)
	if err != nil {
		s.log.Warn("central log query failed", "service", serviceID, "err", err)
		return CentralLogPage{}, apierr.New(http.StatusBadGateway, "central_logs_unavailable", "the built-in log store did not answer")
	}
	redact, err := s.redactor(ctx, serviceID)
	if err != nil {
		return CentralLogPage{}, err
	}
	page.Lines = centralLines(streams, redact, limit, p.linePrefix())
	return page, nil
}

// linePrefix is the prefix the selected process' lines carry; "" for none.
func (p CentralParams) linePrefix() string {
	if p.Process == "" {
		return ""
	}
	return p.Process + stackLinePrefix
}

// centralLines merges the streams, newest first, and keeps limit lines. A
// non-empty prefix keeps only the lines that start with it.
func centralLines(streams []CentralStream, redact func(string) string, limit int, prefix string) []CentralLogLine {
	out := []CentralLogLine{}
	for _, st := range streams {
		for _, e := range st.Entries {
			if prefix != "" && !strings.HasPrefix(e.Line, prefix) {
				continue
			}
			out = append(out, CentralLogLine{TS: e.Time, Server: st.Labels["server"], Stream: st.Labels["stream"], Line: redact(e.Line)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS.After(out[j].TS) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
