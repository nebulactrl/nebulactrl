package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/buildplacement"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ActionSourceChanged is the audit action of a staged change of a service's
// source type (image ↔ git).
const ActionSourceChanged = "service.source_changed"

// FieldSource is the pending-changes field of a source type change. It stands
// for the image, repository and build fields the change carries.
const FieldSource = "source"

// GitLinkHook is told when a service's repository link was removed because the
// service stopped building from git, so the provider webhook of a repository
// nothing else uses can be dropped. It runs after the change committed.
type GitLinkHook interface {
	RepoUnlinked(ctx context.Context, connectionID uuid.UUID, repo string)
}

// SetGitLinkHook installs the hook (wire time only).
func (s *Service) SetGitLinkHook(h GitLinkHook) { s.gitLinkHook = h }

// SourceChangeInput stages a move of a service between an image and a git
// repository. The target's required fields must be present: Image for an
// image; GitURL and GitBranch for git. The git connection (a connected
// account) is linked afterwards with the repository link endpoint.
type SourceChangeInput struct {
	// SourceType is the target: SourceImage or SourceGit.
	SourceType string

	// Image and RegistryAuth (optional, write-only) configure an image source.
	Image        string
	RegistryAuth *RegistryAuth

	// GitURL, GitBranch and GitToken (optional, write-only) configure a git
	// source; the build fields keep their defaults when empty.
	GitURL, GitBranch, GitToken                         string
	BuildType, DockerfilePath, BuildContext, Submodules string

	// Config, when set, is the whole target config (the project config file
	// computes it) and replaces the field-wise input above. The source fields
	// of the other type are cleared from it all the same.
	Config *Config
}

// switchable reports a source type that can move to the other one.
func switchable(t string) bool { return t == SourceImage || t == SourceGit }

// SourceSwitched reports whether going from the release's source type to the
// service's staged one is an image ↔ git change.
func SourceSwitched(from, to string) bool {
	return from != to && switchable(from) && switchable(to)
}

// sourceConfigFields are the config fields only one source type uses: the
// image of an image source, the repository and its build settings of a git
// source (dotted JSON names, as DiffConfig keys them without the prefix).
var sourceConfigFields = []string{
	"image", "gitUrl", "gitBranch", "dockerfilePath", "buildContext", "buildType", "submodules",
	"buildLocation", "buildServerId", "registryId",
}

// withoutSourceFields drops the config changes that belong to a source
// switch, which a single "source" change reports.
func withoutSourceFields(changes []FieldChange) []FieldChange {
	out := changes[:0:0]
	for _, c := range changes {
		name, ok := strings.CutPrefix(c.Field, "config.")
		if ok {
			drop := false
			for _, f := range sourceConfigFields {
				if name == f {
					drop = true
					break
				}
			}
			if drop {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// ForSource returns the config moved to the target source type: the fields
// only the other type uses are cleared (they stay in the history of the
// releases that used them). Everything else is kept, the processes among it
// (ProcessName, Singleton and Processes survive a source switch). Defaults are not applied.
func (c Config) ForSource(target string) Config {
	switch target {
	case SourceGit:
		c.Image = ""
	case SourceImage:
		c.GitURL, c.GitBranch, c.DockerfilePath, c.BuildContext = "", "", "", ""
		c.BuildType, c.Submodules, c.BuildLocation = "", "", ""
		c.BuildServerID, c.RegistryID = nil, nil
	}
	return c
}

// BuilderLabel names the build method of a git source ("Railpack").
func BuilderLabel(buildType string) string {
	switch {
	case buildType == "" || buildType == agentprotocol.BuildAuto:
		return "Auto"
	case buildType == agentprotocol.BuildDockerfile:
		return "Dockerfile"
	case buildType == agentprotocol.BuildRailpack:
		return "Railpack"
	}
	if lang, ok := strings.CutPrefix(buildType, agentprotocol.BuildStarterPrefix); ok {
		return "Starter (" + lang + ")"
	}
	return buildType
}

// RepoLabel is a clone URL as people write it: host and path, without the
// scheme, credentials or ".git" ("github.com/acme/shop").
func RepoLabel(raw string) string {
	s := strings.TrimSpace(raw)
	scheme := false
	if i := strings.Index(s, "://"); i >= 0 {
		s, scheme = s[i+3:], true
	}
	slash := strings.Index(s, "/")
	if at := strings.Index(s, "@"); at >= 0 && (slash < 0 || at < slash) {
		s = s[at+1:]
	}
	if !scheme {
		// scp-like: host:org/repo
		if colon := strings.Index(s, ":"); colon >= 0 && (strings.Index(s, "/") < 0 || colon < strings.Index(s, "/")) {
			s = s[:colon] + "/" + s[colon+1:]
		}
	}
	return strings.TrimSuffix(strings.TrimRight(s, "/"), ".git")
}

// SourceText describes a source for a staged change: "image", or with detail
// "image (nginx:1.27)"; "Git", or "Git (github.com/acme/shop, main, Railpack)".
func SourceText(sourceType string, c Config, detail bool) string {
	switch sourceType {
	case SourceImage:
		if detail && c.Image != "" {
			return "image (" + c.Image + ")"
		}
		return "image"
	case SourceGit:
		if detail {
			return "Git (" + RepoLabel(c.GitURL) + ", " + c.GitBranch + ", " + BuilderLabel(c.BuildType) + ")"
		}
		return "Git"
	}
	return sourceType
}

// StageSourceChange moves the service to another source type as a staged
// change: the next deployment ships it (a git source builds first).
// expected is the generation the caller read.
func (s *Service) StageSourceChange(ctx context.Context, id uuid.UUID, expected int64, in SourceChangeInput) (ServiceView, error) {
	var (
		sc    Scope
		row   db.Service
		after func(context.Context)
	)
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if sc, err = LoadScope(ctx, db.New(tx), policy.ScopeProjectsWrite, id); err != nil {
			return err
		}
		row, after, err = s.StageSourceChangeTx(ctx, tx, sc, expected, in)
		return err
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return ServiceView{}, err
		}
		return ServiceView{}, fmt.Errorf("stage source change: %w", err)
	}
	sc.Service = row
	PublishService(s.bus, EventServiceUpdated, sc)
	after(ctx)
	return s.ViewFor(ctx, sc)
}

// StageSourceChangeTx is StageSourceChange inside the caller's transaction. sc
// is the service's scope, loaded through the same transaction with
// policy.ScopeProjectsWrite. It returns the updated row and a function the
// caller runs once the transaction committed (it tells the git source hook and
// drops the webhook of a repository no service uses any more); the function is
// never nil. It publishes no event.
func (s *Service) StageSourceChangeTx(ctx context.Context, tx pgx.Tx, sc Scope, expected int64, in SourceChangeInput) (db.Service, func(context.Context), error) {
	noop := func(context.Context) {}
	q := db.New(tx)
	cur := sc.Service
	id := cur.ID
	target := in.SourceType
	if (cur.Kind != KindApp && cur.Kind != KindWorker) || !switchable(cur.SourceType) {
		return db.Service{}, noop, apierr.Unprocessable("not_applicable", "only an app or worker built from an image or a git repository can change its source")
	}
	switch {
	case !switchable(target):
		return db.Service{}, noop, apierr.Unprocessable("not_applicable", "a service can switch between an image and a git repository only")
	case target == cur.SourceType:
		return db.Service{}, noop, apierr.Unprocessable("source_unchanged", "the service already uses "+SourceText(target, Config{}, false)+" as its source; edit it in the service's Source settings")
	case cur.Generation != expected:
		return db.Service{}, noop, apierr.PreconditionFailed("generation_conflict", "the service changed since you loaded it").
			WithDetails(map[string]any{"currentGeneration": cur.Generation})
	}

	oldCfg := sc.Config()
	cfg := oldCfg
	if in.Config != nil {
		cfg = *in.Config
	}
	cfg = cfg.ForSource(target)
	if in.Config == nil {
		switch target {
		case SourceImage:
			cfg.Image = strings.TrimSpace(in.Image)
		case SourceGit:
			cfg.GitURL, cfg.GitBranch = strings.TrimSpace(in.GitURL), strings.TrimSpace(in.GitBranch)
			cfg.BuildType, cfg.DockerfilePath = strings.TrimSpace(in.BuildType), strings.TrimSpace(in.DockerfilePath)
			cfg.BuildContext, cfg.Submodules = strings.TrimSpace(in.BuildContext), strings.TrimSpace(in.Submodules)
		}
	}
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(target); err != nil {
		return db.Service{}, noop, ConfigError(err)
	}
	if cfg.BuildServerID != nil || cfg.RegistryID != nil {
		if err := buildplacement.ValidateTargets(ctx, q, sc.WorkspaceID, cfg.BuildServerID, cfg.RegistryID); err != nil {
			return db.Service{}, noop, err
		}
	}

	// The credentials of the old source type are cleared; the new type's are
	// the ones given here.
	var gitTok, regAuth []byte
	var err error
	switch target {
	case SourceGit:
		if in.GitToken != "" {
			if gitTok, err = s.box.SealString(secretbox.ServicesGitToken, id.String(), in.GitToken); err != nil {
				return db.Service{}, noop, fmt.Errorf("seal git token: %w", err)
			}
		}
	case SourceImage:
		if regAuth, err = s.sealRegistryAuth(id, in.RegistryAuth); err != nil {
			return db.Service{}, noop, fmt.Errorf("seal registry auth: %w", err)
		}
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return db.Service{}, noop, err
	}
	if err := q.SetServiceSourceType(ctx, db.SetServiceSourceTypeParams{ID: id, SourceType: target}); err != nil {
		return db.Service{}, noop, fmt.Errorf("set source type: %w", err)
	}
	row, err := q.UpdateServiceCAS(ctx, db.UpdateServiceCASParams{
		ID: id, ExpectedGeneration: expected, Name: cur.Name, Config: cfgJSON, ServerID: cur.ServerID,
		GitTokenEnc: gitTok, RegistryAuthEnc: regAuth,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Service{}, noop, apierr.PreconditionFailed("generation_conflict", "the service changed since you loaded it")
	}
	if err != nil {
		return db.Service{}, noop, err
	}

	meta := map[string]any{"from": cur.SourceType, "to": target, "generation": row.Generation}
	var unlinked *db.ServiceGit
	switch target {
	case SourceImage:
		meta["image"] = cfg.Image
		meta["clearedGit"] = true
		if unlinked, err = ClearGitSource(ctx, q, id); err != nil {
			return db.Service{}, noop, err
		}
		if unlinked != nil {
			meta["unlinkedRepo"] = unlinked.RepoFullName
		}
	case SourceGit:
		meta["repo"], meta["branch"], meta["builder"] = RepoLabel(cfg.GitURL), cfg.GitBranch, BuilderLabel(cfg.BuildType)
		meta["clearedRegistryAuth"] = len(cur.RegistryAuthEnc) > 0
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		Action: ActionSourceChanged, ResourceType: "service", ResourceID: id.String(), ProjectID: &sc.ProjectID, Metadata: meta,
	}); err != nil {
		return db.Service{}, noop, err
	}

	newSc := sc
	newSc.Service = row
	after := func(ctx context.Context) {
		s.RepoUnlinked(ctx, unlinked)
		s.gitSourceChanged(ctx, newSc, nil, nil, false)
	}
	return row, after, nil
}

// ClearGitSource removes what only a git source has besides its config: the
// repository link, the SSH deploy key and its pinned host keys. It returns the
// link it removed (nil when there was none); pass it to RepoUnlinked once the
// transaction committed.
func ClearGitSource(ctx context.Context, q *db.Queries, serviceID uuid.UUID) (*db.ServiceGit, error) {
	var removed *db.ServiceGit
	sg, err := q.GetServiceGit(ctx, serviceID)
	switch {
	case err == nil:
		if err := q.DeleteServiceGit(ctx, serviceID); err != nil {
			return nil, fmt.Errorf("unlink repository: %w", err)
		}
		removed = &sg
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("load repository link: %w", err)
	}
	if _, err := q.DeleteServiceSSHKey(ctx, serviceID); err != nil {
		return nil, fmt.Errorf("delete deploy key: %w", err)
	}
	if err := q.DeleteServiceSSHHosts(ctx, serviceID); err != nil {
		return nil, fmt.Errorf("delete pinned hosts: %w", err)
	}
	return removed, nil
}

// RepoUnlinked tells the git link hook that a link ClearGitSource removed is
// gone (a nil link is ignored), so an unused provider webhook can be dropped.
func (s *Service) RepoUnlinked(ctx context.Context, removed *db.ServiceGit) {
	if removed != nil && s.gitLinkHook != nil {
		s.gitLinkHook.RepoUnlinked(ctx, removed.ConnectionID, removed.RepoFullName)
	}
}

// WithSourceOf returns c with the source fields (image, repository and build
// settings) of from: the staged source change is undone without touching
// anything else (the processes stay as c has them).
func (c Config) WithSourceOf(from Config) Config {
	c.Image, c.GitURL, c.GitBranch, c.DockerfilePath, c.BuildContext = from.Image, from.GitURL, from.GitBranch, from.DockerfilePath, from.BuildContext
	c.BuildType, c.Submodules, c.BuildLocation = from.BuildType, from.Submodules, from.BuildLocation
	c.BuildServerID, c.RegistryID = from.BuildServerID, from.RegistryID
	return c
}
