// Package notifications tells people what happened: channels (email, Slack,
// Discord, Telegram, signed webhook), one subscription rule per channel,
// asynchronous delivery with retries and a per-channel delivery log.
//
// Other domains call Emit (see emit.go). Emit never fails and never waits for a
// channel: it records one delivery per matching channel and queues a River job
// for it; the job renders the stored payload, sends it and retries with
// backoff. Credentials are sealed with secretbox, masked on every read and
// scrubbed from stored errors.
//
// The package imports only low-level packages so every domain may import it.
package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"golang.org/x/time/rate"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// Delivery policy.
const (
	// MaxAttempts is how often a delivery is tried: the first send plus five
	// retries, 1, 3, 8, 15 and 30 minutes apart (about an hour in all).
	MaxAttempts = 6
	// DedupeWindow is how long an identical event is not sent again to a channel.
	DedupeWindow = 5 * time.Minute
	// DeliveryRetention is how long the delivery log is kept.
	DeliveryRetention = 30 * 24 * time.Hour
)

// retryDelays[i] is the wait after attempt i+1 failed.
var retryDelays = [...]time.Duration{time.Minute, 3 * time.Minute, 8 * time.Minute, 15 * time.Minute, 30 * time.Minute}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	return retryDelays[min(attempt, len(retryDelays))-1]
}

// Live-update event types.
const (
	EventChannelUpdated  = "notification.channel.updated"
	EventDeliveryUpdated = "notification.delivery.updated"
)

// Audit actions.
const (
	ActionChannelCreate = "notification_channel.create"
	ActionChannelUpdate = "notification_channel.update"
	ActionChannelDelete = "notification_channel.delete"
	ActionChannelTest   = "notification_channel.test"
	ActionChannelRotate = "notification_channel.rotate_secret"
)

// Deps are the collaborators of a Service. Pool, Jobs and Box may be nil for
// OpenAPI generation only.
type Deps struct {
	Pool      *pgxpool.Pool
	Bus       *events.Bus
	Jobs      *jobs.Manager
	Box       *secretbox.Box
	PublicURL string // NEBULACTRL_PUBLIC_URL: the base of links in messages
	Logger    *slog.Logger
}

// Service implements channels, rules, emission and delivery.
type Service struct {
	pool      *pgxpool.Pool
	q         *db.Queries
	bus       *events.Bus
	jm        *jobs.Manager
	box       *secretbox.Box
	publicURL string
	log       *slog.Logger
	send      *senders
	now       func() time.Time

	// Per-channel send rate: burst messages, then one per rateEvery.
	rateEvery time.Duration
	rateBurst int
	limMu     sync.Mutex
	limiters  map[uuid.UUID]*rate.Limiter
}

var _ Emitter = (*Service)(nil)

// NewService returns a Service.
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	s := &Service{
		pool: d.Pool, bus: d.Bus, jm: d.Jobs, box: d.Box, publicURL: strings.TrimRight(d.PublicURL, "/"),
		log: d.Logger.With("component", "notifications"), send: newSenders(d.PublicURL), now: time.Now,
		rateEvery: 3 * time.Second, rateBurst: 10, limiters: map[uuid.UUID]*rate.Limiter{},
	}
	if d.Pool != nil {
		s.q = db.New(d.Pool)
	}
	return s
}

func (s *Service) publish(typ string, workspaceID, resource uuid.UUID) {
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: typ, ResourceID: resource.String(), WorkspaceID: workspaceID.String()})
	}
}

// --- Emit ----------------------------------------------------------------------

// subject is what Emit learned about an event's subjects.
type subject struct {
	workspace uuid.UUID
	project   *uuid.UUID
	fields    map[string]string
	link      string
}

// resolve looks up names and the workspace of an event's subjects. A subject
// that no longer exists is skipped: the event is still worth sending.
func (s *Service) resolve(ctx context.Context, ev Event) subject {
	// A URL starting with "?" is a query added to the service's own link
	// ("?tab=deployments" opens the inspector on that tab).
	suffix := ""
	link := ev.URL
	if strings.HasPrefix(link, "?") {
		suffix, link = "&"+link[1:], ""
	}
	sub := subject{workspace: ev.WorkspaceID, project: ev.ProjectID, fields: map[string]string{}, link: link}
	setDefault := func(k, v string) {
		if _, ok := ev.Fields[k]; !ok && v != "" {
			sub.fields[k] = v
		}
	}
	for k, v := range ev.Fields {
		sub.fields[k] = v
	}
	if ev.ServiceID != nil {
		if b, err := s.q.NotifServiceBrief(ctx, *ev.ServiceID); err == nil {
			sub.workspace = b.WorkspaceID
			p := b.ProjectID
			sub.project = &p
			setDefault("Project", b.ProjectName)
			setDefault("Environment", b.EnvName)
			setDefault("Service", b.ServiceName)
			if sub.link == "" {
				sub.link = "/p/" + b.ProjectID.String() + "/" + b.EnvSlug + "?service=" + b.ServiceID.String() + suffix
			}
		}
	}
	if sub.project != nil && sub.fields["Project"] == "" {
		if b, err := s.q.NotifProjectBrief(ctx, *sub.project); err == nil {
			if sub.workspace == uuid.Nil {
				sub.workspace = b.WorkspaceID
			}
			setDefault("Project", b.Name)
		}
	}
	if ev.ServerID != nil {
		if b, err := s.q.NotifServerBrief(ctx, *ev.ServerID); err == nil {
			if sub.workspace == uuid.Nil {
				sub.workspace = b.WorkspaceID
			}
			setDefault("Server", b.Name)
			if sub.link == "" && ev.ServiceID == nil {
				sub.link = "/servers/" + b.ID.String()
			}
		}
	}
	if sub.workspace == uuid.Nil {
		sub.workspace = policy.WorkspaceID(ctx)
	}
	if sub.workspace == uuid.Nil {
		if w, err := s.q.FirstWorkspace(ctx); err == nil {
			sub.workspace = w.ID
		}
	}
	if sub.link == "" {
		sub.link = "/"
	}
	return sub
}

// matches reports whether a channel's rule subscribes to the event.
func matches(ch db.NotificationChannel, p Payload, project *uuid.UUID) bool {
	if !slices.Contains(ch.EventTypes, p.Type) || severityRank(p.Severity) < severityRank(ch.MinSeverity) {
		return false
	}
	if ch.AllProjects {
		return true
	}
	return project != nil && slices.Contains(ch.ProjectIds, *project)
}

// Emit implements Emitter. It records one delivery per channel whose rule
// matches and queues it; failures are logged, never returned.
func (s *Service) Emit(ctx context.Context, ev Event) {
	if s == nil || s.pool == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if _, err := s.emit(ctx, ev); err != nil {
		s.log.WarnContext(ctx, "notification not queued", "type", ev.Type, "err", err)
	}
}

// emitResult counts what one Emit did.
type emitResult struct {
	Queued  int
	Deduped int
}

func (s *Service) emit(ctx context.Context, ev Event) (emitResult, error) {
	var res emitResult
	ev.Type = strings.TrimSpace(ev.Type)
	et, known := lookupType(ev.Type)
	if !known {
		return res, fmt.Errorf("unknown event type %q", ev.Type)
	}
	sev := NormalizeSeverity(ev.Severity)
	if sev == "" {
		sev = et.Severity
	}
	sub := s.resolve(ctx, ev)
	if sub.workspace == uuid.Nil {
		return res, errors.New("no workspace for the event")
	}
	p := newPayload(ev, sub.fields, sev, sub.link, s.now())
	if sub.project != nil {
		p.ProjectID = sub.project.String()
	}
	chans, err := s.q.ListEnabledNotificationChannels(ctx, sub.workspace)
	if err != nil {
		return res, fmt.Errorf("list channels: %w", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return res, err
	}
	key := dedupeKey(p)
	var errs []error
	for _, ch := range chans {
		if !matches(ch, p, sub.project) {
			continue
		}
		queued := false
		err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			// Serialize identical events to one channel so two concurrent Emits
			// can not both pass the duplicate check.
			if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", ch.ID.String()+key); err != nil {
				return err
			}
			q := db.New(tx)
			dup, err := q.NotificationDedupeExists(ctx, db.NotificationDedupeExistsParams{ChannelID: ch.ID, DedupeKey: key, CreatedAt: s.now().Add(-DedupeWindow)})
			if err != nil {
				return err
			}
			if dup {
				return nil
			}
			row, err := q.InsertNotificationDelivery(ctx, db.InsertNotificationDeliveryParams{
				WorkspaceID: sub.workspace, ChannelID: ch.ID, EventType: p.Type, Severity: p.Severity, Title: p.Title,
				ProjectID: sub.project, Payload: raw, DedupeKey: key, Status: "pending",
			})
			if err != nil {
				return err
			}
			queued = true
			return s.jm.InsertTx(ctx, tx, DeliverArgs{DeliveryID: row.ID}, &river.InsertOpts{MaxAttempts: MaxAttempts})
		})
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("channel %s: %w", ch.ID, err))
		case queued:
			res.Queued++
			s.publish(EventDeliveryUpdated, sub.workspace, ch.ID)
		default:
			res.Deduped++
		}
	}
	return res, errors.Join(errs...)
}
