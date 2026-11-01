package registries

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

const (
	// sweepBatch bounds the digests one registry's sweep deletes per run.
	sweepBatch = 200
	// sweepEvery is how often the retention sweep runs. It only looks at rows the
	// deployment flow already marked expired, so a run with nothing to do is one query.
	sweepEvery = time.Hour
	// deleteTokenTTL is the lifetime of the delete token the sweep signs itself.
	deleteTokenTTL = 5 * time.Minute
	// UnrecordedImageDays is how long an image that NebulaCtrl did not build
	// (a manifest pushed by hand) stays in the built-in registry. Pushing the
	// same digest again starts the period over.
	UnrecordedImageDays int32 = 14
)

// SweepArgs drives the retention sweep (periodic).
type SweepArgs struct{}

// Kind implements river.JobArgs.
func (SweepArgs) Kind() string { return "registry_sweep" }

type sweepWorker struct {
	river.WorkerDefaults[SweepArgs]
	s *Service
}

func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
	_, err := w.s.Sweep(ctx)
	return err
}

// RegisterJobs registers the retention sweep (hourly).
func RegisterJobs(jm *jobs.Manager, s *Service) {
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &sweepWorker{s: s}) })
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(sweepEvery),
		func() (river.JobArgs, *river.InsertOpts) { return SweepArgs{}, &river.InsertOpts{MaxAttempts: 1} },
		&river.PeriodicJobOpts{ID: "registry_sweep"},
	))
}

// SweepResult is what one registry's sweep did.
type SweepResult struct {
	Deleted int
	Failed  int
	// Skipped is set when the sweep did not run (registry not ready, or in its
	// garbage-collection window), with the reason.
	Skipped string
}

// Sweep runs the retention sweep of every built-in registry that manages its
// retention (one per team). It returns the number of digests deleted.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	regs, err := s.q.ListBuiltinRetentionRegistries(ctx)
	if err != nil {
		return 0, fmt.Errorf("list built-in registries: %w", err)
	}
	total := 0
	var errs []error
	for _, r := range regs {
		res, err := s.SweepRegistry(ctx, r.ID)
		total += res.Deleted
		if err != nil {
			errs = append(errs, fmt.Errorf("registry %s: %w", r.Name, err))
		}
	}
	return total, errors.Join(errs...)
}

// SweepRegistry deletes the expired digests of one built-in registry through the
// registry's own API and marks them deleted. Only rows in state expired are
// touched: a live image, one that never was in the control plane's inventory, or
// one a new push revived is never deleted. The blobs stay on disk until the next
// garbage-collection window.
//
// Each digest is claimed (expired to deleted) before the DELETE and put back to
// expired when the registry could not delete it, so a failed delete is retried at
// the next run and a concurrent revival (a live row) is never overwritten.
func (s *Service) SweepRegistry(ctx context.Context, id uuid.UUID) (SweepResult, error) {
	var res SweepResult
	r, err := s.q.GetRegistryInternal(ctx, id)
	if err != nil {
		return res, err
	}
	if r.Kind != KindBuiltin || !r.ManageRetention {
		res.Skipped = "retention is not managed"
		return res, nil
	}
	m, err := s.builtin(ctx)
	if err != nil {
		return res, err
	}
	if s.BuiltinURL == "" {
		res.Skipped = "the built-in registry is not configured"
		return res, nil
	}
	switch {
	case !m.ready:
		res.Skipped = m.message
		return res, nil
	case m.state.ReadOnly:
		res.Skipped = "the registry is in its garbage-collection window"
		return res, nil
	}
	// Images NebulaCtrl did not build (pushed by hand through the forwarder and
	// recorded there) are not in anyone's retention set: they expire after
	// UnrecordedImageDays, so a team cannot park data in the shared disk for good.
	if _, err := s.q.ExpireUnrecordedRegistryImages(ctx, db.ExpireUnrecordedRegistryImagesParams{RegistryID: r.ID, OlderThanDays: UnrecordedImageDays}); err != nil {
		return res, fmt.Errorf("expire unrecorded images: %w", err)
	}
	rows, err := s.q.ListExpiredRegistryImages(ctx, db.ListExpiredRegistryImagesParams{RegistryID: r.ID, Limit: sweepBatch})
	if err != nil {
		return res, fmt.Errorf("list expired images: %w", err)
	}
	base := strings.TrimRight(s.BuiltinURL, "/")
	var errs []error
	for _, img := range rows {
		claimed, err := s.q.ClaimExpiredRegistryImage(ctx, img.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // revived or already handled
		}
		if err != nil {
			return res, fmt.Errorf("claim image: %w", err)
		}
		repo := strings.TrimPrefix(claimed.Repository, r.Host+"/")
		if err := s.deleteManifest(ctx, base, repo, claimed.Digest); err != nil {
			res.Failed++
			errs = append(errs, fmt.Errorf("delete %s@%s: %w", repo, claimed.Digest, err))
			// Put it back so the next run retries; it never becomes live again.
			if rerr := s.q.SetRegistryImageState(ctx, db.SetRegistryImageStateParams{ID: claimed.ID, State: ImageExpired}); rerr != nil {
				errs = append(errs, fmt.Errorf("restore image state: %w", rerr))
			}
			var de *deleteDisabledError
			if errors.As(err, &de) {
				break // every further delete would fail the same way
			}
			continue
		}
		res.Deleted++
	}
	if res.Deleted > 0 {
		err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
			return audit.Record(ctx, tx, audit.Entry{
				Action: AuditSweep, ResourceType: "registry", ResourceID: r.ID.String(), WorkspaceID: r.WorkspaceID,
				Metadata: map[string]any{"name": r.Name, "deleted": res.Deleted},
			})
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("audit sweep: %w", err))
		}
	}
	if err := s.PatchManagedState(ctx, map[string]any{"lastSweepAt": s.Now().UTC(), "lastSweepDeleted": res.Deleted}); err != nil {
		errs = append(errs, err)
	}
	s.publish(r.WorkspaceID, r.ID)
	return res, errors.Join(errs...)
}

// deleteDisabledError is the registry refusing every delete (405): its
// storage.delete.enabled is off.
type deleteDisabledError struct{ status int }

func (e *deleteDisabledError) Error() string {
	return fmt.Sprintf("the registry refused the delete (status %d): storage deletes are not enabled", e.status)
}

// deleteManifest removes a manifest by digest with a delete token the control
// plane signs itself (it is the registry's only token issuer).
func (s *Service) deleteManifest(ctx context.Context, base, repo, digest string) error {
	tok, err := s.signToken(ctx, deleteTokenTTL, RepoAccess{Name: repo, Actions: []string{ActionDelete}})
	if err != nil {
		return err
	}
	u := base + "/v2/" + (&url.URL{Path: repo}).EscapedPath() + "/manifests/" + url.PathEscape(digest)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusAccepted, http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return nil // 404: already gone
	case http.StatusMethodNotAllowed:
		return &deleteDisabledError{status: resp.StatusCode}
	case http.StatusUnauthorized, http.StatusForbidden:
		return apierr.New(resp.StatusCode, "registry_denied", "the registry refused the control plane's delete token")
	}
	return fmt.Errorf("registry answered %d", resp.StatusCode)
}
