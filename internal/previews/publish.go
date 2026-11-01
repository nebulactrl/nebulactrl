package previews

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
)

// commentMarker identifies the sticky PR comment of a preview. A PR that
// several projects preview (a monorepo) has one comment per project.
func commentMarker(projectID uuid.UUID) string {
	return "<!-- nebulactrl-preview:" + projectID.String() + " -->"
}

// previewURL is the link to the preview's canvas in the web app.
func (s *Service) previewURL(ctx context.Context, pv db.Preview) string {
	base := ""
	if s.Config != nil {
		base = s.Config.PublicURL
	}
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%s/p/%s/%s", base, pv.ProjectID, s.envSlug(ctx, pv.EnvironmentID))
}

func (s *Service) envSlug(ctx context.Context, envID uuid.UUID) string {
	var slug string
	if err := s.Pool.QueryRow(ctx, "SELECT slug FROM environments WHERE id = $1", envID).Scan(&slug); err != nil {
		return envID.String()
	}
	return slug
}

// publishToGit refreshes the sticky PR comment and the aggregate commit status
// of the preview from its current state.
func (s *Service) publishToGit(ctx context.Context, id uuid.UUID) error {
	pv, err := s.q.GetPreview(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if pv.Status == StatusDeleting {
		return nil // the teardown writes the final comment
	}
	body, err := s.renderComment(ctx, pv, "")
	if err != nil {
		return err
	}
	var errs []error
	if err := s.Git.UpsertPRComment(ctx, pv.ConnectionID, pv.Repo, int(pv.PrNumber), commentMarker(pv.ProjectID), body); err != nil {
		errs = append(errs, fmt.Errorf("pull request comment: %w", err))
	}
	if pv.HeadSha != "" {
		if err := s.Git.SetCommitStatus(ctx, pv.ConnectionID, pv.Repo, pv.HeadSha, s.commitStatus(ctx, pv)); err != nil {
			errs = append(errs, fmt.Errorf("commit status: %w", err))
		}
	}
	return errors.Join(errs...)
}

// commitStatus maps the preview's status to the aggregate commit status.
func (s *Service) commitStatus(ctx context.Context, pv db.Preview) CommitStatus {
	target := s.previewURL(ctx, pv)
	switch pv.Status {
	case StatusReady:
		if urls, _ := s.previewURLs(ctx, pv.EnvironmentID); len(urls) > 0 {
			target = urls[0]
		}
		return CommitStatus{State: "success", TargetURL: target, Description: "Preview is ready", Context: CommitContext}
	case StatusFailed:
		return CommitStatus{State: "failure", TargetURL: target, Description: truncate("Preview failed. "+pv.StatusMessage, 140), Context: CommitContext}
	case StatusPaused:
		return CommitStatus{State: "pending", TargetURL: target, Description: "Preview paused: a new push resumes it", Context: CommitContext}
	}
	return CommitStatus{State: "pending", TargetURL: target, Description: "Deploying the preview", Context: CommitContext}
}

// finalComment updates the sticky comment of a deleted preview (best effort).
func (s *Service) finalComment(ctx context.Context, pv db.Preview, reason string) {
	body, err := s.renderComment(ctx, pv, reason)
	if err == nil {
		err = s.Git.UpsertPRComment(ctx, pv.ConnectionID, pv.Repo, int(pv.PrNumber), commentMarker(pv.ProjectID), body)
	}
	if err != nil {
		s.Logger.WarnContext(ctx, "previews: final pull request comment", "preview", pv.ID.String(), "err", err)
	}
}

// previewURLs lists the public URLs of the preview's services.
func (s *Service) previewURLs(ctx context.Context, envID uuid.UUID) ([]string, error) {
	doms, err := s.q.ListPreviewServiceDomains(ctx, envID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(doms))
	for _, d := range doms {
		out = append(out, "https://"+d.Hostname)
	}
	return out, nil
}

// renderComment builds the markdown of the sticky comment. deletedReason is
// set once the preview is being removed.
func (s *Service) renderComment(ctx context.Context, pv db.Preview, deletedReason string) (string, error) {
	rows, err := s.serviceRows(ctx, pv.EnvironmentID)
	if err != nil {
		return "", err
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	latest, err := s.q.LatestPreviewDeployments(ctx, ids)
	if err != nil {
		return "", err
	}
	byService := map[uuid.UUID]db.Deployment{}
	for _, d := range latest {
		byService[d.ServiceID] = d
	}
	doms, err := s.q.ListPreviewServiceDomains(ctx, pv.EnvironmentID)
	if err != nil {
		return "", err
	}
	host := map[uuid.UUID]string{}
	for _, d := range doms {
		if _, ok := host[d.ServiceID]; !ok {
			host[d.ServiceID] = d.Hostname
		}
	}
	st := decodeState(pv.DeployState)

	var b strings.Builder
	b.WriteString(commentMarker(pv.ProjectID))
	b.WriteString("\n### NebulaCtrl preview")
	if deletedReason != "" {
		b.WriteString(" - deleted\n\n")
		b.WriteString(strings.TrimPrefix(strings.TrimPrefix(deleteMessage(deletedReason), "Deleting: "), "Deleting "))
		b.WriteString("\n")
		return b.String(), nil
	}
	fmt.Fprintf(&b, " - %s\n\n", statusLabel(pv.Status))
	if pv.StatusMessage != "" && (pv.Status == StatusFailed || pv.Status == StatusPaused) {
		fmt.Fprintf(&b, "> %s\n\n", oneLine(pv.StatusMessage))
	}
	b.WriteString("| Service | Status | URL |\n|---|---|---|\n")
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
	for _, r := range rows {
		link := ""
		if h, ok := host[r.ID]; ok {
			link = "https://" + h
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", mdCell(r.Name), serviceStatus(pv.Status, byService[r.ID]), link)
	}
	b.WriteString("\n")
	for _, w := range st.Warnings {
		fmt.Fprintf(&b, "- %s\n", oneLine(w))
	}
	fmt.Fprintf(&b, "Commit `%s`", short(pv.HeadSha))
	if u := s.previewURL(ctx, pv); u != "" {
		fmt.Fprintf(&b, " · [Open in NebulaCtrl](%s)", u)
	}
	b.WriteString("\n")
	return b.String(), nil
}

func statusLabel(status string) string {
	switch status {
	case StatusCreating, StatusDeploying:
		return "deploying"
	case StatusSeeding:
		return "seeding the database"
	case StatusReady:
		return "ready"
	case StatusFailed:
		return "failed"
	case StatusPaused:
		return "paused"
	}
	return status
}

func serviceStatus(pvStatus string, d db.Deployment) string {
	if pvStatus == StatusPaused {
		return "Paused"
	}
	if d.ID == uuid.Nil {
		return "Pending"
	}
	switch s := deployments.Status(d.Status); {
	case s == deployments.Succeeded:
		return "Running"
	case s == deployments.Failed, s == deployments.RolledBack, s == deployments.RollbackFailed:
		return "Failed"
	case s == deployments.Canceled:
		return "Canceled"
	case s == deployments.Building:
		return "Building"
	}
	return "Deploying"
}

func mdCell(s string) string { return strings.ReplaceAll(oneLine(s), "|", "\\|") }

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, n int) string {
	r := []rune(oneLine(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}
