package previews

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// errDeleting asks River to retry later: the PR's previous preview is still
// being torn down.
var errDeleting = errors.New("the previous preview of this pull request is still being deleted")

func prKey(projectID, connectionID uuid.UUID, repo string, number int) string {
	return fmt.Sprintf("pr:%s:%s:%s:%d", projectID, connectionID, strings.ToLower(repo), number)
}

// HandlePullRequest receives a verified PR event from the git providers. It
// only enqueues a durable job, so it never blocks the webhook.
func (s *Service) HandlePullRequest(ctx context.Context, ev PullRequest) {
	switch ev.Action {
	case ActionOpened, ActionSynchronize, ActionReopened, ActionClosed, ActionMerged:
	default:
		return
	}
	if err := s.Jobs.Insert(context.WithoutCancel(ctx), PRArgs{Event: ev}, &river.InsertOpts{MaxAttempts: 8}); err != nil {
		s.Logger.ErrorContext(ctx, "previews: enqueue PR event", "repo", ev.Repo, "pr", ev.Number, "action", ev.Action, "err", err)
	}
}

// ProcessPullRequest applies one PR event: create or update the preview on
// opened / reopened / synchronize, delete it on closed / merged.
func (s *Service) ProcessPullRequest(ctx context.Context, ev PullRequest) error {
	if ev.Action == ActionClosed || ev.Action == ActionMerged {
		return s.handleClosed(ctx, ev)
	}
	ids, err := s.Git.ServicesForRepo(ctx, ev.ConnectionID, ev.Repo)
	if err != nil {
		return fmt.Errorf("services for %s: %w", ev.Repo, err)
	}
	if len(ids) == 0 {
		return nil
	}
	targets, err := s.q.ListPreviewTargets(ctx, ids)
	if err != nil {
		return fmt.Errorf("list preview targets: %w", err)
	}
	matching := map[uuid.UUID]bool{}
	for _, id := range ids {
		matching[id] = true
	}
	var errs []error
	for _, t := range targets {
		if t.BaseEnvironmentID == nil {
			continue
		}
		if ev.FromFork && !t.ForkPrs {
			s.Logger.InfoContext(ctx, "previews: fork pull request ignored (fork previews are off)", "project", t.ProjectSlug, "repo", ev.Repo, "pr", ev.Number)
			continue
		}
		if err := s.upsert(ctx, t, matching, ev); err != nil {
			errs = append(errs, fmt.Errorf("project %s: %w", t.ProjectSlug, err))
		}
	}
	return errors.Join(errs...)
}

// upsert creates the preview of the PR in the project, or starts a new deploy
// wave for the existing one.
func (s *Service) upsert(ctx context.Context, t db.ListPreviewTargetsRow, matching map[uuid.UUID]bool, ev PullRequest) error {
	var (
		pv      db.Preview
		created bool
		resumed bool
		started bool
	)
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := s.lock(ctx, tx, prKey(t.ProjectID, ev.ConnectionID, ev.Repo, ev.Number)); err != nil {
			return err
		}
		cur, err := q.GetPreviewByPR(ctx, db.GetPreviewByPRParams{ProjectID: t.ProjectID, ConnectionID: ev.ConnectionID, Repo: ev.Repo, PrNumber: int32(ev.Number)})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			pv, err = s.create(ctx, tx, t, matching, ev)
			created, started = true, true
			return err
		case err != nil:
			return err
		}
		if cur.Status == StatusDeleting {
			return errDeleting
		}
		if ev.HeadSHA == cur.HeadSha && ev.HeadSHA != "" {
			pv = cur // duplicate delivery: nothing new to deploy
			return nil
		}
		pv, resumed, err = s.newWave(ctx, tx, cur, ev)
		started = true
		return err
	})
	if err != nil {
		return err
	}
	if !started {
		return nil
	}
	s.publish(pv)
	if created {
		s.emit(ctx, notifications.Event{
			Type: notifications.TypePreviewCreated, ProjectID: &pv.ProjectID, Severity: notifications.SeverityInfo,
			Title:  fmt.Sprintf("Preview created for PR #%d", pv.PrNumber),
			Body:   fmt.Sprintf("%s: %s", pv.Repo, pv.PrTitle),
			URL:    s.previewURL(ctx, pv),
			Fields: map[string]string{"Pull request": fmt.Sprintf("#%d", pv.PrNumber), "Commit": short(pv.HeadSha)},
		})
	}
	if err := s.enqueuePublish(ctx, pv.ID); err != nil {
		s.Logger.ErrorContext(ctx, "previews: enqueue publish", "preview", pv.ID.String(), "err", err)
	}
	if created || resumed {
		s.enforceLimit(ctx, pv.ProjectID, pv.ID)
	}
	return nil
}

// create clones the base environment and starts the first deploy wave.
func (s *Service) create(ctx context.Context, tx pgx.Tx, t db.ListPreviewTargetsRow, matching map[uuid.UUID]bool, ev PullRequest) (db.Preview, error) {
	q := db.New(tx)
	base, err := q.GetEnvironmentScoped(ctx, db.GetEnvironmentScopedParams{ID: *t.BaseEnvironmentID, WorkspaceID: t.WorkspaceID})
	if err != nil {
		return db.Preview{}, fmt.Errorf("base environment: %w", err)
	}
	slug, err := uniqueEnvSlug(ctx, q, t.ProjectID, ev.Number)
	if err != nil {
		return db.Preview{}, err
	}
	env, err := q.InsertPreviewEnvironment(ctx, db.InsertPreviewEnvironmentParams{
		ProjectID: t.ProjectID, Name: envName(ev.Number, ev.Title), Slug: slug, ParentEnvironmentID: &base.ID,
		PrNumber: new(int32(ev.Number)), PrTitle: ev.Title, ConnectionID: &ev.ConnectionID,
	})
	if err != nil {
		return db.Preview{}, fmt.Errorf("insert preview environment: %w", err)
	}
	es := services.EnvScope{Env: env, WorkspaceID: t.WorkspaceID, ProjectSlug: t.ProjectSlug}
	cl, err := s.clone(ctx, tx, es, base, t, matching, ev)
	if err != nil {
		return db.Preview{}, err
	}
	pv, err := q.InsertPreview(ctx, db.InsertPreviewParams{
		WorkspaceID: t.WorkspaceID, ProjectID: t.ProjectID, EnvironmentID: env.ID, BaseEnvironmentID: base.ID,
		ConnectionID: ev.ConnectionID, Provider: ev.Provider, Repo: ev.Repo, PrNumber: int32(ev.Number), PrTitle: ev.Title,
		PrAuthor: ev.Author, PrUrl: ev.HTMLURL, HeadBranch: ev.HeadBranch, HeadSha: ev.HeadSHA, BaseBranch: ev.BaseBranch,
		FromFork: ev.FromFork, AffectedServiceIds: nonNil(cl.Affected),
	})
	if err != nil {
		return db.Preview{}, fmt.Errorf("insert preview: %w", err)
	}
	st := deployState{Seq: 1, SHA: ev.HeadSHA, Mode: "create", Started: s.Now(), Targets: cl.All, Warnings: cl.Warnings}
	if pv, err = q.StartWave(ctx, db.StartWaveParams{
		ID: pv.ID, State: encodeState(st), HeadSha: ev.HeadSHA, HeadBranch: ev.HeadBranch, PrTitle: ev.Title,
	}); err != nil {
		return db.Preview{}, err
	}
	if err := s.Jobs.InsertTx(ctx, tx, WaveArgs{PreviewID: pv.ID}, nil); err != nil {
		return db.Preview{}, fmt.Errorf("enqueue wave: %w", err)
	}
	err = audit.Record(systemCtx(ctx, t.WorkspaceID, t.ProjectID), tx, audit.Entry{
		Action: "preview.create", ResourceType: "environment", ResourceID: env.ID.String(), ProjectID: &t.ProjectID,
		Metadata: map[string]any{"repo": ev.Repo, "pr": ev.Number, "fork": ev.FromFork, "services": len(cl.All)}, WorkspaceID: t.WorkspaceID,
	})
	return pv, err
}

// newWave starts a deploy wave for a push to an existing preview: the services
// that build the PR's repository redeploy at the new head commit; a paused
// preview resumes (its stopped services start again).
func (s *Service) newWave(ctx context.Context, tx pgx.Tx, cur db.Preview, ev PullRequest) (pv db.Preview, resumed bool, err error) {
	q := db.New(tx)
	locked, err := q.LockPreview(ctx, cur.ID)
	if err != nil {
		return db.Preview{}, false, err
	}
	if locked.Status == StatusDeleting {
		return db.Preview{}, false, errDeleting
	}
	old := decodeState(locked.DeployState)
	rows, err := q.ListAllEnvironmentServices(ctx, locked.EnvironmentID)
	if err != nil {
		return db.Preview{}, false, err
	}
	affected := map[uuid.UUID]bool{}
	for _, id := range locked.AffectedServiceIds {
		affected[id] = true
	}
	resumed = locked.Status == StatusPaused
	st := deployState{Seq: old.Seq + 1, SHA: ev.HeadSHA, Mode: "push", Started: s.Now(), Warnings: old.Warnings}
	if resumed {
		st.Mode = "resume"
	}
	for _, r := range rows {
		switch {
		case affected[r.ID], r.CurrentDeploymentID == nil:
			st.Targets = append(st.Targets, r.ID)
		case resumed:
			st.Restart = append(st.Restart, r.ID)
		}
	}
	title := ev.Title
	if title == "" {
		title = locked.PrTitle
	}
	branch := ev.HeadBranch
	if branch == "" {
		branch = locked.HeadBranch
	}
	pv, err = q.StartWave(ctx, db.StartWaveParams{ID: locked.ID, State: encodeState(st), HeadSha: ev.HeadSHA, HeadBranch: branch, PrTitle: title})
	if err != nil {
		return db.Preview{}, false, err
	}
	if title != locked.PrTitle {
		if err := q.UpdatePreviewEnvironmentTitle(ctx, db.UpdatePreviewEnvironmentTitleParams{ID: locked.EnvironmentID, Name: envName(int(locked.PrNumber), title), PrTitle: title}); err != nil {
			return db.Preview{}, false, err
		}
	}
	if err := s.Jobs.InsertTx(ctx, tx, WaveArgs{PreviewID: pv.ID}, nil); err != nil {
		return db.Preview{}, false, fmt.Errorf("enqueue wave: %w", err)
	}
	err = audit.Record(systemCtx(ctx, pv.WorkspaceID, pv.ProjectID), tx, audit.Entry{
		Action: "preview.deploy", ResourceType: "environment", ResourceID: pv.EnvironmentID.String(), ProjectID: &pv.ProjectID,
		Metadata: map[string]any{"pr": pv.PrNumber, "sha": short(ev.HeadSHA), "mode": st.Mode}, WorkspaceID: pv.WorkspaceID,
	})
	return pv, resumed, err
}

// handleClosed deletes the previews of a closed or merged PR.
func (s *Service) handleClosed(ctx context.Context, ev PullRequest) error {
	rows, err := s.q.ListPreviewsForPR(ctx, db.ListPreviewsForPRParams{ConnectionID: ev.ConnectionID, Repo: ev.Repo, PrNumber: int32(ev.Number)})
	if err != nil {
		return fmt.Errorf("list previews: %w", err)
	}
	reason := "closed"
	if ev.Action == ActionMerged {
		reason = "merged"
	}
	var errs []error
	for _, pv := range rows {
		if err := s.beginDelete(ctx, pv.ID, reason); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// beginDelete marks the preview deleting and enqueues the teardown. reason is
// closed, merged, idle or manual.
func (s *Service) beginDelete(ctx context.Context, id uuid.UUID, reason string) error {
	var pv db.Preview
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.GetPreview(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.lock(ctx, tx, prKey(cur.ProjectID, cur.ConnectionID, cur.Repo, int(cur.PrNumber))); err != nil {
			return err
		}
		if cur, err = q.LockPreview(ctx, id); err != nil {
			return err
		}
		if cur.Status == StatusDeleting {
			pv = cur
			return nil
		}
		if pv, err = q.UpdatePreviewStatus(ctx, db.UpdatePreviewStatusParams{ID: id, Status: StatusDeleting, StatusMessage: deleteMessage(reason)}); err != nil {
			return err
		}
		return s.Jobs.InsertTx(ctx, tx, DeleteArgs{PreviewID: id, Reason: reason}, &river.InsertOpts{MaxAttempts: 25})
	})
	if err != nil {
		return err
	}
	if pv.ID != uuid.Nil {
		s.publish(pv)
	}
	return nil
}

func deleteMessage(reason string) string {
	switch reason {
	case "merged":
		return "Deleting: the pull request was merged."
	case "closed":
		return "Deleting: the pull request was closed."
	case "idle":
		return "Deleting: no push for the configured idle period."
	}
	return "Deleting the preview."
}

// serviceRows lists the live services of a preview environment.
func (s *Service) serviceRows(ctx context.Context, envID uuid.UUID) ([]db.Service, error) {
	return s.q.ListAllEnvironmentServices(ctx, envID)
}

func nonNil(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func isCode(err error, code string) bool {
	var ae *apierr.Error
	return errors.As(err, &ae) && ae.Code == code
}
