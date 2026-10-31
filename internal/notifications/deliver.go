package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"golang.org/x/time/rate"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// Delivery statuses.
const (
	StatusPending   = "pending"
	StatusRetrying  = "retrying"
	StatusDelivered = "delivered"
	StatusFailed    = "failed"
)

// DeliverArgs sends one delivery.
type DeliverArgs struct {
	DeliveryID uuid.UUID `json:"deliveryId"`
}

// Kind implements river.JobArgs.
func (DeliverArgs) Kind() string { return "notification_deliver" }

// PruneArgs deletes old deliveries (hourly).
type PruneArgs struct{}

// Kind implements river.JobArgs.
func (PruneArgs) Kind() string { return "notification_prune" }

type deliverWorker struct {
	river.WorkerDefaults[DeliverArgs]
	s *Service
}

// Timeout bounds one attempt (SMTP is the slowest at 45 seconds).
func (w *deliverWorker) Timeout(*river.Job[DeliverArgs]) time.Duration { return 90 * time.Second }

// NextRetry spaces attempts 1, 3, 8, 15 and 30 minutes apart.
func (w *deliverWorker) NextRetry(job *river.Job[DeliverArgs]) time.Time {
	return w.s.now().Add(retryDelay(job.Attempt))
}

func (w *deliverWorker) Work(ctx context.Context, job *river.Job[DeliverArgs]) error {
	out, err := w.s.process(ctx, job.Args.DeliveryID, job.Attempt, job.MaxAttempts)
	switch out.kind {
	case outcomeSnooze:
		return river.JobSnooze(out.wait)
	case outcomeRetry:
		return err
	case outcomeFailed:
		return river.JobCancel(err)
	}
	return nil
}

type pruneWorker struct {
	river.WorkerDefaults[PruneArgs]
	s *Service
}

func (w *pruneWorker) Work(ctx context.Context, _ *river.Job[PruneArgs]) error {
	n, err := w.s.q.PruneNotificationDeliveries(ctx, w.s.now().Add(-DeliveryRetention))
	if err != nil {
		return fmt.Errorf("prune notification deliveries: %w", err)
	}
	if n > 0 {
		w.s.log.InfoContext(ctx, "pruned notification deliveries", "count", n)
	}
	return nil
}

// RegisterJobs adds the delivery worker and the hourly log prune.
func RegisterJobs(jm *jobs.Manager, s *Service) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &deliverWorker{s: s})
		river.AddWorker(w, &pruneWorker{s: s})
	})
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return PruneArgs{}, &river.InsertOpts{MaxAttempts: 1} },
		&river.PeriodicJobOpts{ID: "notification_prune"},
	))
}

type outcomeKind int

const (
	outcomeDone   outcomeKind = iota // delivered, or nothing left to do
	outcomeRetry                     // failed; River schedules the next attempt
	outcomeFailed                    // gave up
	outcomeSnooze                    // rate limited; try again later without using an attempt
)

type outcome struct {
	kind outcomeKind
	wait time.Duration
}

// limiter returns the channel's token bucket.
func (s *Service) limiter(id uuid.UUID) *rate.Limiter {
	s.limMu.Lock()
	defer s.limMu.Unlock()
	l, ok := s.limiters[id]
	if !ok {
		l = rate.NewLimiter(rate.Every(s.rateEvery), s.rateBurst)
		s.limiters[id] = l
	}
	return l
}

// process makes one attempt at a queued delivery and stores the outcome.
// attempt is River's 1-based attempt number, max its attempt limit.
func (s *Service) process(ctx context.Context, id uuid.UUID, attempt, maxAttempts int) (outcome, error) {
	d, err := s.q.GetNotificationDelivery(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return outcome{}, nil // channel deleted
	}
	if err != nil {
		return outcome{kind: outcomeRetry}, fmt.Errorf("load delivery: %w", err)
	}
	if d.Status == StatusDelivered || d.Status == StatusFailed {
		return outcome{}, nil
	}
	ch, err := s.q.GetNotificationChannelInternal(ctx, d.ChannelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return outcome{}, nil
	}
	if err != nil {
		return outcome{kind: outcomeRetry}, fmt.Errorf("load channel: %w", err)
	}
	if !ch.Enabled {
		msg := "The channel was disabled before this was sent."
		s.record(ctx, d, StatusFailed, attempt, msg, nil, nil)
		return outcome{kind: outcomeFailed}, errors.New(msg)
	}
	if r := s.limiter(ch.ID).Reserve(); r.Delay() > 0 {
		r.Cancel()
		return outcome{kind: outcomeSnooze, wait: max(r.Delay(), time.Second)}, nil
	}
	code, sendErr := s.attemptSend(ctx, ch, d)
	if sendErr == nil {
		s.record(ctx, d, StatusDelivered, attempt, "", &code, nil)
		return outcome{}, nil
	}
	var se *sendError
	permanent := errors.As(sendErr, &se) && se.Permanent
	var codePtr *int
	if code != 0 {
		codePtr = &code
	}
	if permanent || attempt >= maxAttempts {
		s.record(ctx, d, StatusFailed, attempt, sendErr.Error(), codePtr, nil)
		return outcome{kind: outcomeFailed}, sendErr
	}
	next := s.now().Add(retryDelay(attempt))
	s.record(ctx, d, StatusRetrying, attempt, sendErr.Error(), codePtr, &next)
	return outcome{kind: outcomeRetry}, sendErr
}

// attemptSend renders the stored payload and sends it. The returned error is
// scrubbed of every secret of the channel.
func (s *Service) attemptSend(ctx context.Context, ch db.NotificationChannel, d db.NotificationDelivery) (int, error) {
	sec, err := openSecrets(s.box, ch.ID, ch.SecretsEnc)
	if err != nil {
		return 0, &sendError{Message: "the channel's stored credentials could not be decrypted (was the secret key changed?)", Permanent: true}
	}
	var cfg ChannelConfig
	if err := json.Unmarshal(ch.Config, &cfg); err != nil {
		return 0, &sendError{Message: "the channel's settings are unreadable", Permanent: true}
	}
	var p Payload
	if err := json.Unmarshal(d.Payload, &p); err != nil {
		return 0, &sendError{Message: "the stored event is unreadable", Permanent: true}
	}
	code, err := s.send.send(ctx, ch.Kind, cfg, sec, message(p, d.ID, s.publicURL), s.now())
	if err == nil {
		return code, nil
	}
	var se *sendError
	if errors.As(err, &se) {
		if code == 0 {
			code = se.Code // SMTP failures carry their reply code here
		}
		return code, &sendError{Message: scrubError(se.Message, sec), Code: se.Code, Permanent: se.Permanent}
	}
	return code, &sendError{Message: scrubError(err.Error(), sec)}
}

// record stores the outcome of an attempt and tells the UI.
func (s *Service) record(ctx context.Context, d db.NotificationDelivery, status string, attempt int, lastErr string, code *int, next *time.Time) {
	var c *int32
	if code != nil {
		v := int32(*code)
		c = &v
	}
	err := s.q.RecordNotificationAttempt(context.WithoutCancel(ctx), db.RecordNotificationAttemptParams{
		ID: d.ID, Status: status, Attempts: int32(attempt), LastError: lastErr, ResponseCode: c, NextAttemptAt: next,
	})
	if err != nil {
		s.log.ErrorContext(ctx, "record notification attempt", "delivery_id", d.ID, "err", err)
		return
	}
	s.publish(EventDeliveryUpdated, d.WorkspaceID, d.ChannelID)
}
