package logs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nebulactrl/nebulactrl/internal/agent/drains/sinks"
	"github.com/nebulactrl/nebulactrl/internal/agent/logstore/query"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func bad(msg string) error { return apierr.Unprocessable("validation_failed", msg) }

// Drain buffer bounds in MB (the API's unit).
const (
	DefaultBufferMB = 256
	MinBufferMB     = 8
	MaxBufferMB     = 5120
)

const mask = "\u2022\u2022\u2022\u2022\u2022\u2022"

// ---- secrets ----------------------------------------------------------------------

// secrets are a drain's credentials, sealed in log_drains.secrets_enc.
type secrets struct {
	Token    string            `json:"token,omitempty"`
	Username string            `json:"username,omitempty"`
	Password string            `json:"password,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
}

func (s *Service) openSecrets(id uuid.UUID, enc []byte) (secrets, error) {
	var out secrets
	if len(enc) == 0 {
		return out, nil
	}
	raw, err := s.box.Open(secretbox.LogDrainsSecrets, id.String(), enc)
	if err != nil {
		return out, fmt.Errorf("decrypt drain secrets: %w", err)
	}
	defer secretbox.Wipe(raw)
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("decode drain secrets: %w", err)
	}
	return out, nil
}

func sealSecrets(box *secretbox.Box, id uuid.UUID, sec secrets) ([]byte, error) {
	if sec.Token == "" && sec.Username == "" && sec.Password == "" && len(sec.Headers) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(sec)
	if err != nil {
		return nil, err
	}
	defer secretbox.Wipe(raw)
	return box.Seal(secretbox.LogDrainsSecrets, id.String(), raw)
}

// send returns the secrets as the agent receives them (Reconcile.Secrets keys).
func (sec secrets) send(drainID string) map[string]string {
	out := map[string]string{}
	put := func(name, v string) {
		if v != "" {
			out[agentprotocol.LogDrainSecretKey(drainID, name)] = v
		}
	}
	put(agentprotocol.LogDrainSecretToken, sec.Token)
	put(agentprotocol.LogDrainSecretUsername, sec.Username)
	put(agentprotocol.LogDrainSecretPassword, sec.Password)
	if len(sec.Headers) > 0 {
		raw, _ := json.Marshal(sec.Headers)
		put(agentprotocol.LogDrainSecretHeaders, string(raw))
	}
	return out
}

// LogDrainSecretsInput sets credentials. Empty fields keep what is stored; Clear names
// the ones to remove (token, username, password, headers). Headers replaces the
// whole set of extra request headers.
type LogDrainSecretsInput struct {
	Token    string            `json:"token,omitempty" doc:"Bearer token (Loki, OTLP, HTTP)."`
	Username string            `json:"username,omitempty" doc:"Basic auth user (Loki, HTTP)."`
	Password string            `json:"password,omitempty" doc:"Basic auth password (Loki, HTTP)."`
	Headers  map[string]string `json:"headers,omitempty" doc:"Extra request headers (an API key header, for example). Replaces the stored set."`
	Clear    []string          `json:"clear,omitempty" doc:"Credentials to remove: token, username, password, headers."`
}

// reservedHeaders are set by the agent itself. Authorization is not listed: a
// destination with its own scheme (an API key, "Splunk <token>") sets it here
// when no token or basic credentials are configured.
var reservedHeaders = map[string]bool{
	"host": true, "content-length": true, "content-type": true, "content-encoding": true,
	"transfer-encoding": true, "connection": true,
}

func validHeaderName(n string) bool {
	if n == "" || len(n) > 100 {
		return false
	}
	for _, r := range n {
		ok := r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return false
		}
	}
	return true
}

func (in LogDrainSecretsInput) merge(cur secrets) (secrets, error) {
	for _, name := range in.Clear {
		switch name {
		case agentprotocol.LogDrainSecretToken:
			cur.Token = ""
		case agentprotocol.LogDrainSecretUsername:
			cur.Username = ""
		case agentprotocol.LogDrainSecretPassword:
			cur.Password = ""
		case agentprotocol.LogDrainSecretHeaders:
			cur.Headers = nil
		default:
			return cur, bad("cannot clear " + name + ": use token, username, password or headers")
		}
	}
	if in.Token != "" {
		cur.Token = in.Token
	}
	if in.Username != "" {
		cur.Username = in.Username
	}
	if in.Password != "" {
		cur.Password = in.Password
	}
	if in.Headers != nil {
		if len(in.Headers) > 20 {
			return cur, bad("at most 20 extra headers")
		}
		h := make(map[string]string, len(in.Headers))
		for k, v := range in.Headers {
			if !validHeaderName(k) {
				return cur, bad("invalid header name " + k)
			}
			if reservedHeaders[strings.ToLower(k)] {
				return cur, bad("the header " + k + " is set by the drain itself")
			}
			if strings.ContainsAny(v, "\r\n\x00") || len(v) > 4096 {
				return cur, bad("invalid value for header " + k)
			}
			if v != "" {
				h[k] = v
			}
		}
		if len(h) == 0 {
			h = nil
		}
		cur.Headers = h
	}
	if (cur.Username == "") != (cur.Password == "") {
		return cur, bad("basic auth needs both a username and a password")
	}
	return cur, nil
}

// guardBuiltinPatch refuses what would change the built-in drain: its name, its
// settings and its credentials belong to the instance. Sending the values it
// already has is fine (a form that submits every field); turning it on or off
// and its scope are the team's.
func guardBuiltinPatch(cur db.LogDrain, p DrainPatch) error {
	refuse := func(what string) error {
		return apierr.Conflict("builtin_drain", "the built-in drain's "+what+" cannot be changed: you can turn it on or off")
	}
	if p.Name != nil && strings.TrimSpace(*p.Name) != cur.Name {
		return refuse("name")
	}
	if p.Config != nil {
		have, err := decodeKindConfig(cur)
		if err != nil {
			return fmt.Errorf("decode drain config: %w", err)
		}
		if !reflect.DeepEqual(*p.Config, have) {
			return refuse("settings")
		}
	}
	if s := p.Secrets; s != nil && (s.Token != "" || s.Username != "" || s.Password != "" || len(s.Headers) > 0 || len(s.Clear) > 0) {
		return refuse("credentials")
	}
	return nil
}

// ---- views --------------------------------------------------------------------------

// LogDrainTestState is the outcome of the last test send.
type LogDrainTestState struct {
	At      *time.Time `json:"at,omitempty"`
	OK      *bool      `json:"ok,omitempty"`
	Message string     `json:"message"`
}

// LogDrainServerStatus is the health of a drain on one server.
type LogDrainServerStatus struct {
	ServerID      uuid.UUID  `json:"serverId"`
	ServerName    string     `json:"serverName"`
	State         string     `json:"state" enum:"ok,retrying,failing,disabled"`
	LastSuccessAt *time.Time `json:"lastSuccessAt,omitempty"`
	LastError     string     `json:"lastError"`
	LastErrorAt   *time.Time `json:"lastErrorAt,omitempty"`
	SentLines     int64      `json:"sentLines"`
	DroppedLines  int64      `json:"droppedLines"`
	BacklogBytes  int64      `json:"backlogBytes"`
	ReportedAt    time.Time  `json:"reportedAt"`
}

// LogDrainView is a drain as the API returns it. Credentials never appear:
// Secrets has a mask for each one that is set, HeaderNames lists the extra
// headers (their values are secret).
type LogDrainView struct {
	ID      uuid.UUID `json:"id"`
	Name    string    `json:"name"`
	Kind    string    `json:"kind" enum:"loki,otlp,syslog,http"`
	Enabled bool      `json:"enabled"`
	Builtin bool      `json:"builtin" doc:"The team's built-in central log drain (the instance's log store). It is off until the team turns it on; it can be switched on and off and tested, but not renamed, reconfigured or deleted."`

	Loki   *agentprotocol.LokiDrainConfig   `json:"loki,omitempty"`
	OTLP   *agentprotocol.OTLPDrainConfig   `json:"otlp,omitempty"`
	Syslog *agentprotocol.SyslogDrainConfig `json:"syslog,omitempty"`
	HTTP   *agentprotocol.HTTPDrainConfig   `json:"http,omitempty"`

	Secrets     map[string]string `json:"secrets" doc:"A mask for each credential that is set (token, username, password); never its value."`
	HeaderNames []string          `json:"headerNames" doc:"Names of the stored extra headers; their values are secret."`

	AllProjects bool        `json:"allProjects"`
	ProjectIDs  []uuid.UUID `json:"projectIds"`
	ServiceIDs  []uuid.UUID `json:"serviceIds"`
	Query       string      `json:"query" doc:"Only lines matching this log search query are shipped (empty: every line)."`
	BufferMB    int         `json:"bufferMb" doc:"Undelivered backlog a drain keeps on each server's disk."`

	Test      LogDrainTestState      `json:"test"`
	Servers   []LogDrainServerStatus `json:"servers" doc:"The drain's health on each server that reported it."`
	CreatedAt time.Time              `json:"createdAt"`
	UpdatedAt time.Time              `json:"updatedAt"`
}

func decodeKindConfig(d db.LogDrain) (c DrainConfig, err error) {
	if len(d.Config) == 0 {
		return c, nil
	}
	err = json.Unmarshal(d.Config, &c)
	return c, err
}

// DrainConfig is the kind-specific settings; only the drain's kind is set.
type DrainConfig struct {
	Loki   *agentprotocol.LokiDrainConfig   `json:"loki,omitempty"`
	OTLP   *agentprotocol.OTLPDrainConfig   `json:"otlp,omitempty"`
	Syslog *agentprotocol.SyslogDrainConfig `json:"syslog,omitempty"`
	HTTP   *agentprotocol.HTTPDrainConfig   `json:"http,omitempty"`
}

func (s *Service) agentDrain(d db.LogDrain) (agentprotocol.LogDrain, error) {
	c, err := decodeKindConfig(d)
	if err != nil {
		return agentprotocol.LogDrain{}, err
	}
	out := agentprotocol.LogDrain{
		ID: d.ID.String(), Name: d.Name, Kind: d.Kind, Enabled: d.Enabled,
		Loki: c.Loki, OTLP: c.OTLP, Syslog: c.Syslog, HTTP: c.HTTP,
		AllProjects: d.AllProjects, Query: d.Query, BufferMaxBytes: d.BufferMaxBytes, CreatedAt: d.CreatedAt,
	}
	for _, id := range d.ProjectIds {
		out.ProjectIDs = append(out.ProjectIDs, id.String())
	}
	for _, id := range d.ServiceIds {
		out.ServiceIDs = append(out.ServiceIDs, id.String())
	}
	return out, nil
}

func (s *Service) drainView(d db.LogDrain, statuses []db.ListLogDrainStatusesRow) (LogDrainView, error) {
	c, err := decodeKindConfig(d)
	if err != nil {
		return LogDrainView{}, fmt.Errorf("decode drain config: %w", err)
	}
	sec, err := s.openSecrets(d.ID, d.SecretsEnc)
	if err != nil {
		return LogDrainView{}, err
	}
	hints := map[string]string{}
	if sec.Token != "" {
		hints[agentprotocol.LogDrainSecretToken] = mask
	}
	if sec.Username != "" {
		hints[agentprotocol.LogDrainSecretUsername] = mask
	}
	if sec.Password != "" {
		hints[agentprotocol.LogDrainSecretPassword] = mask
	}
	names := make([]string, 0, len(sec.Headers))
	for k := range sec.Headers {
		names = append(names, k)
	}
	sort.Strings(names)
	v := LogDrainView{
		ID: d.ID, Name: d.Name, Kind: d.Kind, Enabled: d.Enabled, Builtin: d.Builtin,
		Loki: c.Loki, OTLP: c.OTLP, Syslog: c.Syslog, HTTP: c.HTTP,
		Secrets: hints, HeaderNames: names,
		AllProjects: d.AllProjects, ProjectIDs: nonNil(d.ProjectIds), ServiceIDs: nonNil(d.ServiceIds), Query: d.Query,
		BufferMB: int(d.BufferMaxBytes >> 20),
		Test:     LogDrainTestState{At: d.LastTestAt, OK: d.LastTestOk, Message: d.LastTestMessage},
		Servers:  []LogDrainServerStatus{}, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
	for _, st := range statuses {
		if st.DrainID != d.ID {
			continue
		}
		v.Servers = append(v.Servers, LogDrainServerStatus{
			ServerID: st.ServerID, ServerName: st.ServerName, State: st.State, LastSuccessAt: st.LastSuccessAt,
			LastError: st.LastError, LastErrorAt: st.LastErrorAt, SentLines: st.SentLines, DroppedLines: st.DroppedLines,
			BacklogBytes: st.BacklogBytes, ReportedAt: st.ReportedAt,
		})
	}
	return v, nil
}

func nonNil(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}

func (s *Service) workspace(ctx context.Context) (uuid.UUID, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return uuid.Nil, err
	}
	return policy.WorkspaceID(ctx), nil
}

func (s *Service) drain(ctx context.Context, ws, id uuid.UUID) (db.LogDrain, error) {
	d, err := s.q.GetLogDrain(ctx, db.GetLogDrainParams{ID: id, WorkspaceID: ws})
	if isNoRows(err) {
		return d, apierr.NotFound("log drain")
	}
	if err != nil {
		return d, fmt.Errorf("get log drain: %w", err)
	}
	return d, nil
}

// ListDrains returns the team's drains with their health.
func (s *Service) ListDrains(ctx context.Context) ([]LogDrainView, error) {
	ws, err := s.workspace(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListLogDrains(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list log drains: %w", err)
	}
	sts, err := s.q.ListLogDrainStatuses(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list drain status: %w", err)
	}
	out := make([]LogDrainView, 0, len(rows))
	for _, d := range rows {
		v, err := s.drainView(d, sts)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// GetDrain returns one drain.
func (s *Service) GetDrain(ctx context.Context, id uuid.UUID) (LogDrainView, error) {
	ws, err := s.workspace(ctx)
	if err != nil {
		return LogDrainView{}, err
	}
	return s.drainViewByID(ctx, ws, id)
}

func (s *Service) drainViewByID(ctx context.Context, ws, id uuid.UUID) (LogDrainView, error) {
	d, err := s.drain(ctx, ws, id)
	if err != nil {
		return LogDrainView{}, err
	}
	sts, err := s.q.ListLogDrainStatuses(ctx, ws)
	if err != nil {
		return LogDrainView{}, fmt.Errorf("list drain status: %w", err)
	}
	return s.drainView(d, sts)
}

// DrainInput creates a drain.
type DrainInput struct {
	Name        string
	Kind        string
	Enabled     *bool
	Config      DrainConfig
	Secrets     LogDrainSecretsInput
	AllProjects *bool
	ProjectIDs  []uuid.UUID
	ServiceIDs  []uuid.UUID
	Query       string
	BufferMB    int
}

// DrainPatch updates a drain; nil fields stay as they are.
type DrainPatch struct {
	Name        *string
	Enabled     *bool
	Config      *DrainConfig
	Secrets     *LogDrainSecretsInput
	AllProjects *bool
	ProjectIDs  *[]uuid.UUID
	ServiceIDs  *[]uuid.UUID
	Query       *string
	BufferMB    *int
}

func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 80 {
		return "", bad("name is required (at most 80 characters)")
	}
	return name, nil
}

// normalized holds the validated row values of a drain.
type normalized struct {
	cfgRaw     json.RawMessage
	allProj    bool
	projects   []uuid.UUID
	services   []uuid.UUID
	query      string
	bufferByte int64
}

// normalize validates the drain and returns the row values.
func (s *Service) normalize(ctx context.Context, ws uuid.UUID, kind string, cfg DrainConfig, allProjects bool, projects, services []uuid.UUID, q string, bufferMB int, sec secrets) (normalized, error) {
	var n normalized
	d := agentprotocol.LogDrain{Name: "x", Kind: kind, Enabled: true}
	switch kind {
	case agentprotocol.DrainLoki:
		if cfg.Loki == nil {
			return n, bad("loki settings are required")
		}
		cfg = DrainConfig{Loki: cfg.Loki}
		d.Loki = cfg.Loki
	case agentprotocol.DrainOTLP:
		if cfg.OTLP == nil {
			return n, bad("otlp settings are required")
		}
		cfg = DrainConfig{OTLP: cfg.OTLP}
		d.OTLP = cfg.OTLP
	case agentprotocol.DrainSyslog:
		if cfg.Syslog == nil {
			return n, bad("syslog settings are required")
		}
		cfg = DrainConfig{Syslog: cfg.Syslog}
		d.Syslog = cfg.Syslog
		if sec.Token != "" || sec.Username != "" || sec.Password != "" || len(sec.Headers) > 0 {
			return n, bad("syslog drains take no credentials")
		}
	case agentprotocol.DrainHTTP:
		if cfg.HTTP == nil {
			return n, bad("http settings are required")
		}
		cfg = DrainConfig{HTTP: cfg.HTTP}
		d.HTTP = cfg.HTTP
	default:
		return n, bad("kind must be loki, otlp, syslog or http")
	}
	if err := sinks.Validate(d); err != nil {
		return n, bad(err.Error())
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return n, err
	}
	n.cfgRaw = raw

	n.allProj = allProjects
	if allProjects {
		n.projects = []uuid.UUID{}
	} else {
		ids := slices.Clone(projects)
		slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
		ids = slices.Compact(ids)
		if len(ids) == 0 {
			return n, bad("choose at least one project, or ship the whole team")
		}
		c, err := s.q.NotifCountProjects(ctx, db.NotifCountProjectsParams{WorkspaceID: ws, Ids: ids})
		if err != nil {
			return n, fmt.Errorf("check projects: %w", err)
		}
		if int(c) != len(ids) {
			return n, bad("a selected project does not exist")
		}
		n.projects = ids
	}
	svcs := slices.Clone(services)
	slices.SortFunc(svcs, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	svcs = slices.Compact(svcs)
	if len(svcs) > 0 {
		c, err := s.q.CountWorkspaceServices(ctx, db.CountWorkspaceServicesParams{WorkspaceID: ws, Column2: svcs})
		if err != nil {
			return n, fmt.Errorf("check services: %w", err)
		}
		if int(c) != len(svcs) {
			return n, bad("a selected service does not exist")
		}
	}
	n.services = nonNil(svcs)

	n.query = strings.TrimSpace(q)
	if len(n.query) > 1000 {
		return n, bad("the query is too long (at most 1000 characters)")
	}
	if _, errs := query.Compile(n.query); len(errs) > 0 {
		return n, bad("query: " + errs[0].Message)
	}
	if bufferMB == 0 {
		bufferMB = DefaultBufferMB
	}
	if bufferMB < MinBufferMB || bufferMB > MaxBufferMB {
		return n, bad(fmt.Sprintf("bufferMb must be between %d and %d", MinBufferMB, MaxBufferMB))
	}
	n.bufferByte = int64(bufferMB) << 20
	return n, nil
}

// CreateDrain adds a drain. Admin only: it stores credentials.
func (s *Service) CreateDrain(ctx context.Context, in DrainInput) (LogDrainView, error) {
	ws, err := s.workspace(ctx)
	if err != nil {
		return LogDrainView{}, err
	}
	name, err := checkName(in.Name)
	if err != nil {
		return LogDrainView{}, err
	}
	sec, err := in.Secrets.merge(secrets{})
	if err != nil {
		return LogDrainView{}, err
	}
	allProjects := in.AllProjects == nil || *in.AllProjects
	n, err := s.normalize(ctx, ws, in.Kind, in.Config, allProjects, in.ProjectIDs, in.ServiceIDs, in.Query, in.BufferMB, sec)
	if err != nil {
		return LogDrainView{}, err
	}
	drainID := uuid.New()
	sealed, err := sealSecrets(s.box, drainID, sec)
	if err != nil {
		return LogDrainView{}, fmt.Errorf("seal drain secrets: %w", err)
	}
	enabled := in.Enabled == nil || *in.Enabled
	var row db.LogDrain
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = db.New(tx).InsertLogDrain(ctx, db.InsertLogDrainParams{
			ID: drainID, WorkspaceID: ws, Name: name, Kind: in.Kind, Enabled: enabled, Config: n.cfgRaw, SecretsEnc: sealed,
			AllProjects: n.allProj, ProjectIds: n.projects, ServiceIds: n.services, Query: n.query, BufferMaxBytes: n.bufferByte,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionDrainCreate, ResourceType: "log_drain", ResourceID: row.ID.String(),
			Metadata: map[string]any{"name": row.Name, "kind": row.Kind, "enabled": row.Enabled},
		})
	})
	if isUnique(err) {
		return LogDrainView{}, apierr.Conflict("drain_conflict", "a log drain named "+name+" already exists")
	}
	if err != nil {
		return LogDrainView{}, fmt.Errorf("create log drain: %w", err)
	}
	s.publish(EventDrainUpdated, ws, row.ID)
	s.resyncAll(ctx, ws)
	return s.drainViewByID(ctx, ws, row.ID)
}

// UpdateDrain changes a drain.
func (s *Service) UpdateDrain(ctx context.Context, id uuid.UUID, p DrainPatch) (LogDrainView, error) {
	ws, err := s.workspace(ctx)
	if err != nil {
		return LogDrainView{}, err
	}
	cur, err := s.drain(ctx, ws, id)
	if err != nil {
		return LogDrainView{}, err
	}
	name := cur.Name
	if p.Name != nil {
		if name, err = checkName(*p.Name); err != nil {
			return LogDrainView{}, err
		}
	}
	if cur.Builtin {
		if err := guardBuiltinPatch(cur, p); err != nil {
			return LogDrainView{}, err
		}
	}
	enabled := cur.Enabled
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	cfg, err := decodeKindConfig(cur)
	if err != nil {
		return LogDrainView{}, fmt.Errorf("decode drain config: %w", err)
	}
	if p.Config != nil {
		cfg = *p.Config
	}
	sec, err := s.openSecrets(cur.ID, cur.SecretsEnc)
	if err != nil {
		return LogDrainView{}, err
	}
	changedSecrets := false
	if p.Secrets != nil {
		next, err := p.Secrets.merge(sec)
		if err != nil {
			return LogDrainView{}, err
		}
		changedSecrets = !sameSecrets(next, sec)
		sec = next
	}
	allProjects, projects, services, q, bufMB := cur.AllProjects, cur.ProjectIds, cur.ServiceIds, cur.Query, int(cur.BufferMaxBytes>>20)
	if p.AllProjects != nil {
		allProjects = *p.AllProjects
	}
	if p.ProjectIDs != nil {
		projects = *p.ProjectIDs
	}
	if p.ServiceIDs != nil {
		services = *p.ServiceIDs
	}
	if p.Query != nil {
		q = *p.Query
	}
	if p.BufferMB != nil {
		bufMB = *p.BufferMB
	}
	n, err := s.normalize(ctx, ws, cur.Kind, cfg, allProjects, projects, services, q, bufMB, sec)
	if err != nil {
		return LogDrainView{}, err
	}
	sealed, err := sealSecrets(s.box, id, sec)
	if err != nil {
		return LogDrainView{}, fmt.Errorf("seal drain secrets: %w", err)
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := db.New(tx).UpdateLogDrain(ctx, db.UpdateLogDrainParams{
			ID: id, WorkspaceID: ws, Name: name, Enabled: enabled, Config: n.cfgRaw, SecretsEnc: sealed,
			AllProjects: n.allProj, ProjectIds: n.projects, ServiceIds: n.services, Query: n.query, BufferMaxBytes: n.bufferByte,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionDrainUpdate, ResourceType: "log_drain", ResourceID: id.String(),
			// Credential changes record that they happened, never the values.
			Metadata: map[string]any{"name": row.Name, "kind": row.Kind, "enabled": row.Enabled, "secretsChanged": changedSecrets},
		})
	})
	if isUnique(err) {
		return LogDrainView{}, apierr.Conflict("drain_conflict", "a log drain named "+name+" already exists")
	}
	if err != nil {
		return LogDrainView{}, fmt.Errorf("update log drain: %w", err)
	}
	s.publish(EventDrainUpdated, ws, id)
	s.resyncAll(ctx, ws)
	return s.drainViewByID(ctx, ws, id)
}

func sameSecrets(a, b secrets) bool {
	if a.Token != b.Token || a.Username != b.Username || a.Password != b.Password || len(a.Headers) != len(b.Headers) {
		return false
	}
	for k, v := range a.Headers {
		if b.Headers[k] != v {
			return false
		}
	}
	return true
}

// DeleteDrain removes a drain; agents stop shipping and forget their position.
func (s *Service) DeleteDrain(ctx context.Context, id uuid.UUID) error {
	ws, err := s.workspace(ctx)
	if err != nil {
		return err
	}
	cur, err := s.drain(ctx, ws, id)
	if err != nil {
		return err
	}
	if cur.Builtin {
		return apierr.Conflict("builtin_drain", "the built-in drain cannot be deleted: turn it off instead")
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).DeleteLogDrain(ctx, db.DeleteLogDrainParams{ID: id, WorkspaceID: ws}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionDrainDelete, ResourceType: "log_drain", ResourceID: id.String(),
			Metadata: map[string]any{"name": cur.Name, "kind": cur.Kind},
		})
	})
	if err != nil {
		return fmt.Errorf("delete log drain: %w", err)
	}
	s.publish(EventDrainUpdated, ws, id)
	s.resyncAll(ctx, ws)
	return nil
}

// LogDrainTestResult is the outcome of a test send.
type LogDrainTestResult struct {
	OK         bool      `json:"ok"`
	Message    string    `json:"message"`
	StatusCode int       `json:"statusCode,omitempty"`
	ServerID   uuid.UUID `json:"serverId"`
	ServerName string    `json:"serverName"`
}

// TestDrain sends one test line through the drain from a connected server
// (serverID, or the first online one): delivery happens from the agent's host,
// so that is where reachability and credentials are proved.
func (s *Service) TestDrain(ctx context.Context, id uuid.UUID, serverID *uuid.UUID) (LogDrainTestResult, error) {
	ws, err := s.workspace(ctx)
	if err != nil {
		return LogDrainTestResult{}, err
	}
	d, err := s.drain(ctx, ws, id)
	if err != nil {
		return LogDrainTestResult{}, err
	}
	servers, err := s.q.ListLogCapableServers(ctx, ws)
	if err != nil {
		return LogDrainTestResult{}, fmt.Errorf("list servers: %w", err)
	}
	var target *db.ListLogCapableServersRow
	for i, sv := range servers {
		if serverID != nil && sv.ID != *serverID {
			continue
		}
		if s.hub != nil && s.hub.Online(sv.ID) {
			target = &servers[i]
			break
		}
	}
	if target == nil {
		if serverID != nil {
			return LogDrainTestResult{}, apierr.Conflict("server_offline", "the selected server is not connected")
		}
		return LogDrainTestResult{}, apierr.Conflict("no_online_server", "no server is connected: a drain ships from the servers, so a test needs one")
	}
	ad, err := s.agentDrain(d)
	if err != nil {
		return LogDrainTestResult{}, fmt.Errorf("decode drain: %w", err)
	}
	sec, err := s.openSecrets(d.ID, d.SecretsEnc)
	if err != nil {
		return LogDrainTestResult{}, err
	}
	res, err := s.hub.Request(ctx, target.ID, agentprotocol.KindLogsDrainTest, agentprotocol.LogsDrainTest{Drain: ad}, sec.send(d.ID.String()), 45*time.Second)
	if err != nil {
		return LogDrainTestResult{}, mapRequestError(err)
	}
	out := LogDrainTestResult{ServerID: target.ID, ServerName: target.Name}
	if res.Outcome != agentprotocol.OutcomeSucceeded {
		out.Message = agentFailure(res)
	} else {
		var tr agentprotocol.LogsDrainTestResult
		if err := json.Unmarshal(res.Data, &tr); err != nil {
			return LogDrainTestResult{}, fmt.Errorf("decode test result: %w", err)
		}
		out.OK, out.Message, out.StatusCode = tr.OK, tr.Message, tr.StatusCode
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.New(tx).SetLogDrainTest(ctx, db.SetLogDrainTestParams{ID: id, LastTestOk: &out.OK, LastTestMessage: out.Message}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionDrainTest, ResourceType: "log_drain", ResourceID: id.String(),
			Metadata: map[string]any{"name": d.Name, "ok": out.OK, "server": target.Name},
		})
	})
	if err != nil {
		return out, fmt.Errorf("record test: %w", err)
	}
	s.publish(EventDrainUpdated, ws, id)
	return out, nil
}

func agentFailure(res agentprotocol.ResultFrame) string {
	if res.Error != "" {
		return res.Error
	}
	return "the agent could not run the request"
}

// mapRequestError turns a delivery failure into an API error.
func mapRequestError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return apierr.New(http.StatusGatewayTimeout, "server_timeout", "the server did not answer in time")
	}
	if ae, ok := apierr.As(err); ok {
		return ae
	}
	return apierr.New(http.StatusServiceUnavailable, "server_offline", "the server disconnected before it answered")
}
