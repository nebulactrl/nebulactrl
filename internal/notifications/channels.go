package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// ChannelRule says which events a channel receives.
type ChannelRule struct {
	EventTypes  []string    `json:"eventTypes" doc:"Subscribed event types (see GET /notifications/event-types)."`
	MinSeverity string      `json:"minSeverity" enum:"info,warning,error" doc:"Events below this severity are not sent."`
	AllProjects bool        `json:"allProjects" doc:"true: the whole workspace, including events that belong to no project (server alerts). false: only events of projectIds."`
	ProjectIDs  []uuid.UUID `json:"projectIds" doc:"Projects of a project-scoped rule."`
}

// ChannelTestState is the outcome of the last test send.
type ChannelTestState struct {
	At      *time.Time `json:"at,omitempty"`
	OK      *bool      `json:"ok,omitempty"`
	Message string     `json:"message"`
}

// ChannelStats summarizes the last 24 hours of a channel (test sends excluded).
type ChannelStats struct {
	Delivered      int        `json:"delivered24h"`
	Failed         int        `json:"failed24h"`
	LastDeliveryAt *time.Time `json:"lastDeliveryAt,omitempty"`
}

// ChannelView is a channel as the API returns it. Secrets are never included:
// Secrets holds masked hints for the ones that are set.
type ChannelView struct {
	ID        uuid.UUID         `json:"id"`
	Name      string            `json:"name"`
	Kind      string            `json:"kind" enum:"email,slack,discord,telegram,webhook"`
	Enabled   bool              `json:"enabled"`
	Config    ChannelConfig     `json:"config"`
	Secrets   map[string]string `json:"secrets" doc:"Masked hints of the stored secrets, keyed by name (url, botToken, password, signingSecret). A key is present when the secret is set; its value never reveals it."`
	Rule      ChannelRule       `json:"rule"`
	Test      ChannelTestState  `json:"test"`
	Stats     ChannelStats      `json:"stats"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

// ChannelDelivery is one row of the delivery log. It carries no secret.
type ChannelDelivery struct {
	ID            uuid.UUID      `json:"id"`
	ChannelID     uuid.UUID      `json:"channelId"`
	EventType     string         `json:"eventType"`
	Severity      string         `json:"severity" enum:"info,warning,error"`
	Title         string         `json:"title"`
	ProjectID     *uuid.UUID     `json:"projectId,omitempty"`
	Status        string         `json:"status" enum:"pending,retrying,delivered,failed"`
	Attempts      int            `json:"attempts"`
	MaxAttempts   int            `json:"maxAttempts"`
	LastError     string         `json:"lastError" doc:"Why the last attempt failed; credentials are masked."`
	ResponseCode  *int           `json:"responseCode,omitempty" doc:"HTTP status or SMTP reply code of the last attempt."`
	NextAttemptAt *time.Time     `json:"nextAttemptAt,omitempty"`
	DeliveredAt   *time.Time     `json:"deliveredAt,omitempty"`
	IsTest        bool           `json:"isTest"`
	CreatedAt     time.Time      `json:"createdAt"`
	Fields        []MessageField `json:"fields,omitempty"`
	Body          string         `json:"body,omitempty"`
	URL           string         `json:"url,omitempty"`
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *Service) workspace(ctx context.Context) (uuid.UUID, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return uuid.Nil, err
	}
	return policy.WorkspaceID(ctx), nil
}

// ChannelRuleInput is a rule on create or update.
type ChannelRuleInput struct {
	EventTypes  []string    `json:"eventTypes" doc:"Subscribed event types (see GET /notifications/event-types)."`
	MinSeverity string      `json:"minSeverity,omitempty" enum:"info,warning,error" doc:"Events below this severity are not sent. Default info."`
	AllProjects *bool       `json:"allProjects,omitempty" doc:"Default true: the whole workspace. false: only projectIds."`
	ProjectIDs  []uuid.UUID `json:"projectIds,omitempty"`
}

func (r ChannelRuleInput) rule() ChannelRule {
	all := r.AllProjects == nil || *r.AllProjects
	return ChannelRule{EventTypes: r.EventTypes, MinSeverity: r.MinSeverity, AllProjects: all, ProjectIDs: r.ProjectIDs}
}

// DefaultRule is the rule of a channel created without one: failures and
// alerts on, successes off, the whole workspace, every severity.
func DefaultRule() ChannelRule {
	return ChannelRule{EventTypes: DefaultEventTypes(), MinSeverity: SeverityInfo, AllProjects: true, ProjectIDs: []uuid.UUID{}}
}

// normalizeRule validates a rule against the catalogue and the workspace's projects.
func (s *Service) normalizeRule(ctx context.Context, ws uuid.UUID, r ChannelRule) (ChannelRule, error) {
	out := ChannelRule{MinSeverity: r.MinSeverity, AllProjects: r.AllProjects, ProjectIDs: []uuid.UUID{}}
	if out.MinSeverity == "" {
		out.MinSeverity = SeverityInfo
	}
	if NormalizeSeverity(out.MinSeverity) != out.MinSeverity {
		return out, bad("minSeverity must be info, warning or error")
	}
	for _, t := range r.EventTypes {
		if _, ok := lookupType(t); !ok {
			return out, bad("unknown event type " + t)
		}
	}
	for _, e := range catalog { // catalogue order, no duplicates
		if slices.Contains(r.EventTypes, e.Type) {
			out.EventTypes = append(out.EventTypes, e.Type)
		}
	}
	if out.EventTypes == nil {
		out.EventTypes = []string{}
	}
	if !out.AllProjects {
		ids := slices.Clone(r.ProjectIDs)
		slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
		ids = slices.Compact(ids)
		if len(ids) == 0 {
			return out, bad("choose at least one project, or send for the whole workspace")
		}
		n, err := s.q.NotifCountProjects(ctx, db.NotifCountProjectsParams{WorkspaceID: ws, Ids: ids})
		if err != nil {
			return out, fmt.Errorf("check projects: %w", err)
		}
		if int(n) != len(ids) {
			return out, bad("a selected project does not exist")
		}
		out.ProjectIDs = ids
	}
	return out, nil
}

func (s *Service) view(ch db.NotificationChannel, st ChannelStats) (ChannelView, error) {
	sec, err := openSecrets(s.box, ch.ID, ch.SecretsEnc)
	if err != nil {
		return ChannelView{}, err
	}
	var cfg ChannelConfig
	if len(ch.Config) > 0 {
		if err := json.Unmarshal(ch.Config, &cfg); err != nil {
			return ChannelView{}, fmt.Errorf("decode channel config: %w", err)
		}
	}
	ids := ch.ProjectIds
	if ids == nil {
		ids = []uuid.UUID{}
	}
	types := ch.EventTypes
	if types == nil {
		types = []string{}
	}
	return ChannelView{
		ID: ch.ID, Name: ch.Name, Kind: ch.Kind, Enabled: ch.Enabled, Config: cfg, Secrets: sec.hints(),
		Rule:      ChannelRule{EventTypes: types, MinSeverity: ch.MinSeverity, AllProjects: ch.AllProjects, ProjectIDs: ids},
		Test:      ChannelTestState{At: ch.LastTestAt, OK: ch.LastTestOk, Message: ch.LastTestMessage},
		Stats:     st,
		CreatedAt: ch.CreatedAt, UpdatedAt: ch.UpdatedAt,
	}, nil
}

func (s *Service) channel(ctx context.Context, q *db.Queries, ws, id uuid.UUID) (db.NotificationChannel, error) {
	ch, err := q.GetNotificationChannel(ctx, db.GetNotificationChannelParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return ch, apierr.NotFound("notification channel")
	}
	if err != nil {
		return ch, fmt.Errorf("get channel: %w", err)
	}
	return ch, nil
}

func (s *Service) stats(ctx context.Context, ws uuid.UUID) (map[uuid.UUID]ChannelStats, error) {
	rows, err := s.q.NotificationChannelStats(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("channel stats: %w", err)
	}
	out := make(map[uuid.UUID]ChannelStats, len(rows))
	for _, r := range rows {
		last := r.LastAt
		out[r.ChannelID] = ChannelStats{Delivered: int(r.Delivered), Failed: int(r.Failed), LastDeliveryAt: &last}
	}
	return out, nil
}

// EventTypes returns the catalogue for the event matrix.
func (s *Service) EventTypes(ctx context.Context) ([]ChannelEventType, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return nil, err
	}
	return Catalog(), nil
}

// ListChannels returns the workspace's channels.
func (s *Service) ListChannels(ctx context.Context) ([]ChannelView, error) {
	ws, err := s.workspace(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListNotificationChannels(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	st, err := s.stats(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make([]ChannelView, 0, len(rows))
	for _, ch := range rows {
		v, err := s.view(ch, st[ch.ID])
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// GetChannel returns one channel.
func (s *Service) GetChannel(ctx context.Context, id uuid.UUID) (ChannelView, error) {
	ws, err := s.workspace(ctx)
	if err != nil {
		return ChannelView{}, err
	}
	return s.channelView(ctx, ws, id)
}

func (s *Service) channelView(ctx context.Context, ws, id uuid.UUID) (ChannelView, error) {
	ch, err := s.channel(ctx, s.q, ws, id)
	if err != nil {
		return ChannelView{}, err
	}
	st, err := s.stats(ctx, ws)
	if err != nil {
		return ChannelView{}, err
	}
	return s.view(ch, st[ch.ID])
}

// ChannelInput creates a channel.
type ChannelInput struct {
	Name    string
	Kind    string
	Enabled *bool
	Config  ChannelConfig
	Secrets ChannelSecretsInput
	Rule    *ChannelRuleInput // nil: DefaultRule
}

// Created is the result of CreateChannel. SigningSecret is set only when the
// server generated a webhook secret: this is the one time it is shown.
type Created struct {
	Channel       ChannelView
	SigningSecret string
}

func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 80 {
		return "", bad("name is required (at most 80 characters)")
	}
	return name, nil
}

// CreateChannel adds a channel. Admin only: it stores credentials.
func (s *Service) CreateChannel(ctx context.Context, in ChannelInput) (Created, error) {
	ws, err := s.workspace(ctx)
	if err != nil {
		return Created{}, err
	}
	name, err := checkName(in.Name)
	if err != nil {
		return Created{}, err
	}
	cfg, err := normalizeConfig(in.Kind, in.Config)
	if err != nil {
		return Created{}, err
	}
	sec := in.Secrets.merge(secrets{})
	var generated string
	if in.Kind == KindWebhook && sec.SigningSecret == "" {
		if generated, err = GenerateSigningSecret(); err != nil {
			return Created{}, err
		}
		sec.SigningSecret = generated
	}
	if in.Kind != KindWebhook {
		sec.SigningSecret = ""
	}
	if err := validateSecrets(in.Kind, cfg, sec); err != nil {
		return Created{}, err
	}
	rule := DefaultRule()
	if in.Rule != nil {
		rule = in.Rule.rule()
	}
	if rule, err = s.normalizeRule(ctx, ws, rule); err != nil {
		return Created{}, err
	}
	chID := uuid.New()
	sealed, err := sealSecrets(s.box, chID, sec)
	if err != nil {
		return Created{}, fmt.Errorf("seal channel secrets: %w", err)
	}
	rawCfg, _ := json.Marshal(cfg)
	enabled := in.Enabled == nil || *in.Enabled
	var row db.NotificationChannel
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = db.New(tx).InsertNotificationChannel(ctx, db.InsertNotificationChannelParams{
			ID: chID, WorkspaceID: ws, Name: name, Kind: in.Kind, Enabled: enabled, Config: rawCfg, SecretsEnc: sealed,
			EventTypes: rule.EventTypes, MinSeverity: rule.MinSeverity, AllProjects: rule.AllProjects, ProjectIds: rule.ProjectIDs,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionChannelCreate, ResourceType: "notification_channel", ResourceID: row.ID.String(),
			Metadata: map[string]any{"name": row.Name, "kind": row.Kind, "events": len(rule.EventTypes)},
		})
	})
	if isUnique(err) {
		return Created{}, apierr.Conflict("channel_conflict", "a channel named "+name+" already exists")
	}
	if err != nil {
		return Created{}, fmt.Errorf("create channel: %w", err)
	}
	s.publish(EventChannelUpdated, ws, row.ID)
	v, err := s.view(row, ChannelStats{})
	return Created{Channel: v, SigningSecret: generated}, err
}

// ChannelPatch updates a channel; nil fields stay as they are and empty secret
// fields keep the stored credential.
type ChannelPatch struct {
	Name    *string
	Enabled *bool
	Config  *ChannelConfig
	Secrets *ChannelSecretsInput
	Rule    *ChannelRuleInput
}

// UpdateChannel changes a channel.
func (s *Service) UpdateChannel(ctx context.Context, id uuid.UUID, p ChannelPatch) (ChannelView, error) {
	ws, err := s.workspace(ctx)
	if err != nil {
		return ChannelView{}, err
	}
	cur, err := s.channel(ctx, s.q, ws, id)
	if err != nil {
		return ChannelView{}, err
	}
	name := cur.Name
	if p.Name != nil {
		if name, err = checkName(*p.Name); err != nil {
			return ChannelView{}, err
		}
	}
	enabled := cur.Enabled
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	var cfg ChannelConfig
	if err := json.Unmarshal(cur.Config, &cfg); err != nil {
		return ChannelView{}, fmt.Errorf("decode channel config: %w", err)
	}
	if p.Config != nil {
		cfg = *p.Config
	}
	if cfg, err = normalizeConfig(cur.Kind, cfg); err != nil {
		return ChannelView{}, err
	}
	sec, err := openSecrets(s.box, cur.ID, cur.SecretsEnc)
	if err != nil {
		return ChannelView{}, err
	}
	changedSecrets := false
	if p.Secrets != nil {
		next := p.Secrets.merge(sec)
		if cur.Kind != KindWebhook {
			next.SigningSecret = ""
		}
		changedSecrets = next != sec
		sec = next
	}
	if cfg.Username == "" && cur.Kind == KindEmail {
		sec.Password = "" // removing the user name drops the password with it
	}
	if err := validateSecrets(cur.Kind, cfg, sec); err != nil {
		return ChannelView{}, err
	}
	rule := ChannelRule{EventTypes: cur.EventTypes, MinSeverity: cur.MinSeverity, AllProjects: cur.AllProjects, ProjectIDs: cur.ProjectIds}
	if p.Rule != nil {
		rule = p.Rule.rule()
	}
	if rule, err = s.normalizeRule(ctx, ws, rule); err != nil {
		return ChannelView{}, err
	}
	sealed, err := sealSecrets(s.box, cur.ID, sec)
	if err != nil {
		return ChannelView{}, fmt.Errorf("seal channel secrets: %w", err)
	}
	rawCfg, _ := json.Marshal(cfg)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := db.New(tx).UpdateNotificationChannel(ctx, db.UpdateNotificationChannelParams{
			ID: id, WorkspaceID: ws, Name: name, Enabled: enabled, Config: rawCfg, SecretsEnc: sealed,
			EventTypes: rule.EventTypes, MinSeverity: rule.MinSeverity, AllProjects: rule.AllProjects, ProjectIds: rule.ProjectIDs,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionChannelUpdate, ResourceType: "notification_channel", ResourceID: id.String(),
			// Credential changes record that they happened, never the values.
			Metadata: map[string]any{"name": row.Name, "kind": row.Kind, "enabled": row.Enabled, "secretsChanged": changedSecrets, "events": len(rule.EventTypes)},
		})
	})
	if isUnique(err) {
		return ChannelView{}, apierr.Conflict("channel_conflict", "a channel named "+name+" already exists")
	}
	if err != nil {
		return ChannelView{}, fmt.Errorf("update channel: %w", err)
	}
	s.publish(EventChannelUpdated, ws, id)
	return s.channelView(ctx, ws, id)
}

// DeleteChannel removes a channel and its delivery log.
func (s *Service) DeleteChannel(ctx context.Context, id uuid.UUID) error {
	ws, err := s.workspace(ctx)
	if err != nil {
		return err
	}
	ch, err := s.channel(ctx, s.q, ws, id)
	if err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).DeleteNotificationChannel(ctx, db.DeleteNotificationChannelParams{ID: id, WorkspaceID: ws}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionChannelDelete, ResourceType: "notification_channel", ResourceID: id.String(),
			Metadata: map[string]any{"name": ch.Name, "kind": ch.Kind},
		})
	})
	if err != nil {
		return fmt.Errorf("delete channel: %w", err)
	}
	s.publish(EventChannelUpdated, ws, id)
	return nil
}

// RotateSecret replaces a webhook channel's signing secret and returns the new
// one, the only time it is shown.
func (s *Service) RotateSecret(ctx context.Context, id uuid.UUID) (string, error) {
	ws, err := s.workspace(ctx)
	if err != nil {
		return "", err
	}
	cur, err := s.channel(ctx, s.q, ws, id)
	if err != nil {
		return "", err
	}
	if cur.Kind != KindWebhook {
		return "", apierr.Unprocessable("not_a_webhook", "only webhook channels have a signing secret")
	}
	sec, err := openSecrets(s.box, cur.ID, cur.SecretsEnc)
	if err != nil {
		return "", err
	}
	if sec.SigningSecret, err = GenerateSigningSecret(); err != nil {
		return "", err
	}
	sealed, err := sealSecrets(s.box, cur.ID, sec)
	if err != nil {
		return "", fmt.Errorf("seal channel secrets: %w", err)
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).UpdateNotificationChannel(ctx, db.UpdateNotificationChannelParams{
			ID: id, WorkspaceID: ws, Name: cur.Name, Enabled: cur.Enabled, Config: cur.Config, SecretsEnc: sealed,
			EventTypes: cur.EventTypes, MinSeverity: cur.MinSeverity, AllProjects: cur.AllProjects, ProjectIds: cur.ProjectIds,
		}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionChannelRotate, ResourceType: "notification_channel", ResourceID: id.String(),
			Metadata: map[string]any{"name": cur.Name},
		})
	})
	if err != nil {
		return "", fmt.Errorf("rotate signing secret: %w", err)
	}
	s.publish(EventChannelUpdated, ws, id)
	return sec.SigningSecret, nil
}

// TestChannel sends a test message through the channel right now (no queue, no
// retries, no rate limit, no dedupe) and returns the delivery that records the
// result. A disabled channel can be tested too.
func (s *Service) TestChannel(ctx context.Context, id uuid.UUID) (ChannelDelivery, error) {
	ws, err := s.workspace(ctx)
	if err != nil {
		return ChannelDelivery{}, err
	}
	ch, err := s.channel(ctx, s.q, ws, id)
	if err != nil {
		return ChannelDelivery{}, err
	}
	label := "NebulaCtrl"
	if who, ok := principalLabel(ctx); ok {
		label = who
	}
	p := Payload{
		Type: "test", Severity: SeverityInfo, Title: "Test notification from NebulaCtrl",
		Body:   "If you can read this, the channel \"" + ch.Name + "\" works. Real notifications look like this one.",
		URL:    "/settings/notifications",
		Fields: []MessageField{{Name: "Channel", Value: ch.Name}, {Name: "Sent by", Value: label}},
		At:     s.now().UTC().Truncate(time.Second), Test: true,
	}
	raw, _ := json.Marshal(p)
	var d db.NotificationDelivery
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		d, err = db.New(tx).InsertNotificationDelivery(ctx, db.InsertNotificationDeliveryParams{
			WorkspaceID: ws, ChannelID: id, EventType: p.Type, Severity: p.Severity, Title: p.Title, Payload: raw, IsTest: true, Status: StatusPending,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionChannelTest, ResourceType: "notification_channel", ResourceID: id.String(),
			Metadata: map[string]any{"name": ch.Name, "kind": ch.Kind},
		})
	})
	if err != nil {
		return ChannelDelivery{}, fmt.Errorf("record test: %w", err)
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	code, sendErr := s.attemptSend(sendCtx, ch, d)
	var codePtr *int
	if code != 0 {
		codePtr = &code
	}
	ok := sendErr == nil
	msg := "The test message was accepted."
	status, lastErr := StatusDelivered, ""
	if !ok {
		status, lastErr = StatusFailed, sendErr.Error()
		msg = lastErr
	}
	s.record(ctx, d, status, 1, lastErr, codePtr, nil)
	if err := s.q.SetNotificationChannelTest(context.WithoutCancel(ctx), db.SetNotificationChannelTestParams{ID: id, LastTestOk: &ok, LastTestMessage: msg}); err != nil {
		s.log.ErrorContext(ctx, "record channel test", "channel_id", id, "err", err)
	}
	s.publish(EventChannelUpdated, ws, id)
	fresh, err := s.q.GetNotificationDelivery(ctx, d.ID)
	if err != nil {
		return ChannelDelivery{}, fmt.Errorf("reload test delivery: %w", err)
	}
	return deliveryView(fresh), nil
}

func deliveryView(d db.NotificationDelivery) ChannelDelivery {
	v := ChannelDelivery{
		ID: d.ID, ChannelID: d.ChannelID, EventType: d.EventType, Severity: d.Severity, Title: d.Title, ProjectID: d.ProjectID,
		Status: d.Status, Attempts: int(d.Attempts), MaxAttempts: MaxAttempts, LastError: d.LastError, NextAttemptAt: d.NextAttemptAt,
		DeliveredAt: d.DeliveredAt, IsTest: d.IsTest, CreatedAt: d.CreatedAt,
	}
	if d.ResponseCode != nil {
		c := int(*d.ResponseCode)
		v.ResponseCode = &c
	}
	var p Payload
	if json.Unmarshal(d.Payload, &p) == nil {
		v.Body, v.URL, v.Fields = p.Body, p.URL, p.Fields
	}
	return v
}

// ChannelDeliveryPage is a page of the delivery log, newest first. It is not the generic
// api.Page so its schema has a name of its own.
type ChannelDeliveryPage struct {
	Items      []ChannelDelivery `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty" doc:"Present when older deliveries exist"`
}

// ListDeliveries returns a channel's delivery log. status filters ("" = all).
func (s *Service) ListDeliveries(ctx context.Context, channelID uuid.UUID, status, cursor string, limit int) (ChannelDeliveryPage, error) {
	ws, err := s.workspace(ctx)
	if err != nil {
		return ChannelDeliveryPage{}, err
	}
	if _, err := s.channel(ctx, s.q, ws, channelID); err != nil {
		return ChannelDeliveryPage{}, err
	}
	if status != "" && !slices.Contains([]string{StatusPending, StatusRetrying, StatusDelivered, StatusFailed}, status) {
		return ChannelDeliveryPage{}, bad("status must be pending, retrying, delivered or failed")
	}
	before, beforeID, ok, err := api.DecodeTimeCursor(cursor)
	if err != nil {
		return ChannelDeliveryPage{}, err
	}
	if limit <= 0 || limit > api.MaxPageSize {
		limit = api.DefaultPageSize
	}
	params := db.ListNotificationDeliveriesParams{ChannelID: channelID, WorkspaceID: ws, Status: status, RowLimit: int32(limit + 1)}
	if ok {
		params.BeforeCreated, params.BeforeID = &before, &beforeID
	}
	rows, err := s.q.ListNotificationDeliveries(ctx, params)
	if err != nil {
		return ChannelDeliveryPage{}, fmt.Errorf("list deliveries: %w", err)
	}
	page := ChannelDeliveryPage{Items: []ChannelDelivery{}}
	for i, d := range rows {
		if i == limit {
			last := rows[limit-1]
			page.NextCursor = api.EncodeTimeCursor(last.CreatedAt, last.ID)
			break
		}
		page.Items = append(page.Items, deliveryView(d))
	}
	return page, nil
}

// principalLabel names the caller for the test message.
func principalLabel(ctx context.Context) (string, bool) {
	p, ok := auth.PrincipalFrom(ctx)
	if !ok || p.Label == "" {
		return "", false
	}
	return p.Label, true
}
