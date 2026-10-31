package proxyserver

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
)

// CertExpiryArgs checks every certificate's expiry (daily).
type CertExpiryArgs struct{}

// Kind implements river.JobArgs.
func (CertExpiryArgs) Kind() string { return "certificate_expiry" }

type certExpiryWorker struct {
	river.WorkerDefaults[CertExpiryArgs]
	jm *jobs.Manager
	em notifications.Emitter
}

func (w *certExpiryWorker) Work(ctx context.Context, _ *river.Job[CertExpiryArgs]) error {
	n, err := CheckCertificateExpiry(ctx, w.jm.Pool(), time.Now(), w.em)
	if err != nil {
		return err
	}
	if n > 0 {
		w.jm.Logger().InfoContext(ctx, "certificates near expiry", "count", n)
	}
	return nil
}

// Expiry warning thresholds in days.
const (
	expiryWarnDays     = 30
	expiryCriticalDays = 7
)

const expiringPrefix = "expires in "

// CheckCertificateExpiry updates the status message of certificates within 30
// days of expiry ("expires in N days"), records the audit event
// `certificate.expiring` once per threshold (30, 7) and marks expired ones
// status error. It returns how many certificates are within the warning window.
// Idempotent; also clears a stale "expires in" message after the window.
func CheckCertificateExpiry(ctx context.Context, pool *pgxpool.Pool, now time.Time, em notifications.Emitter) (int, error) {
	q := db.New(pool)
	rows, err := q.ListCertificatesForExpiry(ctx)
	if err != nil {
		return 0, fmt.Errorf("list certificates: %w", err)
	}
	near := 0
	for _, c := range rows {
		days := daysUntil(c.NotAfter, now)
		threshold := 0
		status, msg := c.Status, c.StatusMessage
		switch {
		case !c.NotAfter.After(now):
			status, msg, threshold = "error", fmt.Sprintf("expired on %s", c.NotAfter.UTC().Format("2006-01-02")), -1
		case days <= expiryCriticalDays:
			msg, threshold = fmt.Sprintf("%s%d days", expiringPrefix, days), expiryCriticalDays
		case days <= expiryWarnDays:
			msg, threshold = fmt.Sprintf("%s%d days", expiringPrefix, days), expiryWarnDays
		default:
			if len(msg) >= len(expiringPrefix) && msg[:len(expiringPrefix)] == expiringPrefix {
				msg = ""
			}
		}
		if threshold != 0 {
			near++
		}
		// A load error reported by the proxy outranks the countdown message.
		if status == "error" && threshold != -1 {
			continue
		}
		if status == c.Status && msg == c.StatusMessage && threshold == 0 {
			continue
		}
		fresh := false // the audit event is new: this threshold was not announced yet
		err := db.InTx(ctx, pool, func(tx pgx.Tx) error {
			q := db.New(tx)
			if status != c.Status || msg != c.StatusMessage {
				if err := q.SetCertificateStatus(ctx, db.SetCertificateStatusParams{ID: c.ID, Status: status, StatusMessage: msg}); err != nil {
					return err
				}
			}
			if threshold == 0 {
				return nil
			}
			th := fmt.Sprint(max(threshold, 0))
			done, err := q.CertificateExpiryAuditExists(ctx, db.CertificateExpiryAuditExistsParams{ResourceID: c.ID.String(), ThresholdDays: th})
			if err != nil || done {
				return err
			}
			fresh = true
			return audit.Record(ctx, tx, audit.Entry{
				WorkspaceID: c.WorkspaceID, Action: "certificate.expiring", ResourceType: "certificate", ResourceID: c.ID.String(),
				Metadata: map[string]any{"name": c.Name, "thresholdDays": max(threshold, 0), "daysToExpiry": days, "serverId": c.ServerID.String()},
			})
		})
		if err != nil {
			return near, fmt.Errorf("update certificate %s: %w", c.ID, err)
		}
		if fresh {
			notifyExpiring(ctx, em, c, days, threshold)
		}
	}
	return near, nil
}

// RegisterJobs adds the daily CertExpiryJob.
func RegisterJobs(jm *jobs.Manager, em notifications.Emitter) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &certExpiryWorker{jm: jm, em: em})
	})
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(24*time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return CertExpiryArgs{}, &river.InsertOpts{MaxAttempts: 3} },
		&river.PeriodicJobOpts{ID: "certificate_expiry", RunOnStart: true},
	))
}
