package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/gitssh"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	// envScanWait bounds how long a scan waits for the agent: a depth-1 fetch is
	// quick, and a create dialog is waiting for it.
	envScanWait     = 75 * time.Second
	envScanPollTick = 300 * time.Millisecond
	// envScanTokenTTL is how long the pasted token of a preview stays
	// available for the command's delivery (a little over the command TTL).
	envScanTokenTTL = agents.CommandTTL + 5*time.Minute
)

// envScanTokens holds the pasted git token of a preview between enqueueing the
// command and its delivery. A preview has no service to read credentials from
// and commands never persist secrets, so the token lives in memory only, keyed
// by the random scan id in the payload, and is dropped as soon as the scan ends.
var envScanTokens = &tokenStash{m: map[string]stashedToken{}}

type stashedToken struct {
	token   string
	expires time.Time
}

type tokenStash struct {
	mu sync.Mutex
	m  map[string]stashedToken
}

func (t *tokenStash) put(token string) string {
	id := uuid.NewString()
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, v := range t.m {
		if now.After(v.expires) {
			delete(t.m, k)
		}
	}
	t.m[id] = stashedToken{token: token, expires: now.Add(envScanTokenTTL)}
	return id
}

func (t *tokenStash) get(id string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.m[id]
	return v.token, ok && time.Now().Before(v.expires)
}

func (t *tokenStash) drop(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.m, id)
}

// ScanEnvTemplates asks the agent of the service's server to fetch the
// service's branch head and report its template env files. sc must already be
// authorized by the caller. It waits for the agent, so it fails with 409
// server_offline instead of queueing for an offline server.
func (s *Service) ScanEnvTemplates(ctx context.Context, sc services.Scope) (agentprotocol.EnvScanResult, error) {
	return s.scanService(ctx, sc, "", nil)
}

// ScanConfigFiles is ScanEnvTemplates for project config files: the agent of
// the service's build host fetches the head of branch (empty: the service's
// own branch) and returns the files at paths (at most
// agentprotocol.ScanMaxConfigPaths, each agentprotocol.ValidConfigPath) that
// exist there, together with the commit it read. The service supplies the
// repository and its credentials; sc must already be authorized by the caller.
func (s *Service) ScanConfigFiles(ctx context.Context, sc services.Scope, branch string, paths []string) (agentprotocol.EnvScanResult, error) {
	if len(paths) == 0 || len(paths) > agentprotocol.ScanMaxConfigPaths {
		return agentprotocol.EnvScanResult{}, apierr.Unprocessable("validation_failed", fmt.Sprintf("read between 1 and %d config files at once", agentprotocol.ScanMaxConfigPaths))
	}
	for _, p := range paths {
		if !agentprotocol.ValidConfigPath(p) {
			return agentprotocol.EnvScanResult{}, apierr.Unprocessable("validation_failed", fmt.Sprintf("%q is not a repository-relative file path", p))
		}
	}
	return s.scanService(ctx, sc, branch, paths)
}

// scanService runs an env_scan of a service's repository on its build host.
// With configPaths the command reads those files only.
func (s *Service) scanService(ctx context.Context, sc services.Scope, branch string, configPaths []string) (agentprotocol.EnvScanResult, error) {
	if sc.Service.SourceType != services.SourceGit {
		return agentprotocol.EnvScanResult{}, apierr.Unprocessable("not_git_source", "Template env files are read only from a git source.")
	}
	if sc.Service.ServerID == nil {
		return agentprotocol.EnvScanResult{}, apierr.Unprocessable("no_server", "Assign the service to a server first: the repository is read on the server that will build it.")
	}
	target, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *sc.Service.ServerID})
	if err != nil {
		return agentprotocol.EnvScanResult{}, fmt.Errorf("load server: %w", err)
	}
	cfg := sc.Config()
	// The repository is read where it is built: on the build server when the
	// service's effective build location is one.
	srv, err := s.buildHost(ctx, s.q, sc, cfg, target)
	if err != nil {
		return agentprotocol.EnvScanResult{}, err
	}
	if branch == "" {
		branch = cfg.GitBranch
	}
	payload := agentprotocol.EnvScanPayload{
		ServiceID: sc.Service.ID.String(), GitURL: cfg.GitURL, Branch: branch, BuildContext: cfg.BuildContext,
		ConfigPaths: configPaths, ConfigOnly: len(configPaths) > 0,
	}
	creds, err := s.sshCreds(ctx, sc.Service.ID, cfg.GitURL)
	if err != nil {
		return agentprotocol.EnvScanResult{}, apierr.Unprocessable("git_ssh_unavailable", s.sshCredsMessage(ctx, sc.Service.ID, err))
	}
	if creds != nil {
		payload.HasSSHKey, payload.KnownHosts = true, creds.KnownHosts
	} else {
		payload.HasToken = s.hasGitAuth(ctx, sc.Service)
	}
	return s.runEnvScan(ctx, srv, payload)
}

// EnvPreviewInput is the repository of a service that does not exist yet.
type EnvPreviewInput struct {
	ServerID     uuid.UUID
	GitURL       string
	Branch       string
	BuildContext string
	// Token is the pasted access token of a private repository (never stored).
	Token string
}

// PreviewEnvTemplates is ScanEnvTemplates for a service that does not exist
// yet: the repository comes from the request and the pasted token is held in
// memory for the scan. The caller must have authorized the environment. An SSH
// URL cannot be previewed: its deploy key is generated per service.
func (s *Service) PreviewEnvTemplates(ctx context.Context, in EnvPreviewInput) (agentprotocol.EnvScanResult, error) {
	branch := in.Branch
	if branch == "" {
		branch = "main" // only for validation: the scan reads the repository's default branch
	}
	cfg := services.Config{GitURL: in.GitURL, GitBranch: branch, BuildContext: in.BuildContext}.WithDefaults()
	if err := cfg.Validate(services.SourceGit); err != nil {
		return agentprotocol.EnvScanResult{}, services.ConfigError(err)
	}
	if gitssh.LooksLikeSSH(cfg.GitURL) {
		return agentprotocol.EnvScanResult{}, apierr.Unprocessable("git_ssh_needs_key", "An SSH repository is read once the service exists and has a deploy key.")
	}
	ws := policy.WorkspaceID(ctx)
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: in.ServerID, WorkspaceID: &ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return agentprotocol.EnvScanResult{}, apierr.Unprocessable("server_not_found", "the selected server does not exist")
	}
	if err != nil {
		return agentprotocol.EnvScanResult{}, fmt.Errorf("load server: %w", err)
	}
	payload := agentprotocol.EnvScanPayload{GitURL: cfg.GitURL, Branch: in.Branch, BuildContext: in.BuildContext}
	if in.Token != "" {
		payload.HasToken, payload.ScanID = true, envScanTokens.put(in.Token)
		defer envScanTokens.drop(payload.ScanID)
	}
	return s.runEnvScan(ctx, srv, payload)
}

// runEnvScan enqueues the command on srv and waits for its result.
func (s *Service) runEnvScan(ctx context.Context, srv db.Server, payload agentprotocol.EnvScanPayload) (agentprotocol.EnvScanResult, error) {
	switch {
	case srv.Status == "revoked":
		return agentprotocol.EnvScanResult{}, apierr.Conflict("server_revoked", "server revoked: "+srv.Name+" was revoked; assign the service to another server.")
	case srv.Status != "online":
		return agentprotocol.EnvScanResult{}, apierr.Conflict("server_offline", srv.Name+" is offline; the repository is read on the server that will build it.")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return agentprotocol.EnvScanResult{}, err
	}
	var cmdID uuid.UUID
	if err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		cmdID, err = s.Dispatcher.Enqueue(ctx, tx, agents.NewCommand{ServerID: srv.ID, Kind: agentprotocol.KindEnvScan, Payload: raw})
		return err
	}); err != nil {
		return agentprotocol.EnvScanResult{}, fmt.Errorf("enqueue env scan: %w", err)
	}
	return s.awaitEnvScan(ctx, cmdID, srv.Name)
}

// awaitEnvScan polls the command row until the agent reports. A command the
// agent never took is canceled so it cannot run later.
func (s *Service) awaitEnvScan(ctx context.Context, id uuid.UUID, serverName string) (agentprotocol.EnvScanResult, error) {
	deadline := time.NewTimer(envScanWait)
	defer deadline.Stop()
	tick := time.NewTicker(envScanPollTick)
	defer tick.Stop()
	giveUp := func(msg string) (agentprotocol.EnvScanResult, error) {
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = s.q.CancelUnackedAgentCommand(bg, db.CancelUnackedAgentCommandParams{ID: id, Error: msg})
		return agentprotocol.EnvScanResult{}, apierr.New(http.StatusGatewayTimeout, "env_scan_timeout", msg)
	}
	for {
		cmd, err := s.q.GetAgentCommand(ctx, id)
		if err != nil {
			return agentprotocol.EnvScanResult{}, fmt.Errorf("load env scan command: %w", err)
		}
		switch cmd.Status {
		case "succeeded":
			var frame agentprotocol.ResultFrame
			var res agentprotocol.EnvScanResult
			if err := json.Unmarshal(cmd.Result, &frame); err != nil || json.Unmarshal(frame.Data, &res) != nil {
				return agentprotocol.EnvScanResult{}, fmt.Errorf("decode env scan result of command %s", id)
			}
			return res, nil
		case "failed":
			msg := "Reading the repository failed on " + serverName + "."
			if cmd.Error != "" {
				msg = cmd.Error
			}
			return agentprotocol.EnvScanResult{}, apierr.Unprocessable("env_scan_failed", msg)
		case "expired", "canceled":
			return agentprotocol.EnvScanResult{}, apierr.Conflict("server_offline", serverName+" did not take the request; try again when it is online.")
		}
		select {
		case <-ctx.Done():
			return giveUp("The request was canceled.")
		case <-deadline.C:
			return giveUp("Reading the repository took too long on " + serverName + ".")
		case <-tick.C:
		}
	}
}

// envScanSecrets attaches the send-time secrets of an env_scan command: the git
// token or deploy key of the service, or the pasted token of a preview.
func (s *Service) envScanSecrets(ctx context.Context, payload json.RawMessage) (map[string]string, error) {
	var p agentprotocol.EnvScanPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("decode env_scan payload: %w", err)
	}
	out := map[string]string{}
	if p.ServiceID == "" {
		if p.HasToken {
			tok, ok := envScanTokens.get(p.ScanID)
			if !ok {
				return nil, errors.New("the pasted access token is no longer available; start the preview again")
			}
			out[agentprotocol.SecretGitToken] = tok
		}
		return out, nil
	}
	svcID, err := uuid.Parse(p.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("env_scan payload service id: %w", err)
	}
	sc, err := services.LoadScopeInternal(ctx, s.q, svcID)
	if err != nil {
		return nil, fmt.Errorf("load service: %w", err)
	}
	if p.HasToken {
		c, err := s.gitCreds(ctx, sc.Service, p.GitURL)
		if err != nil {
			return nil, err
		}
		c.attach(out)
	}
	if p.HasSSHKey {
		creds, err := s.sshCreds(ctx, sc.Service.ID, p.GitURL)
		if err != nil {
			return nil, fmt.Errorf("deploy key: %w", err)
		}
		if creds == nil {
			return nil, errors.New("deploy key: the git url is no longer an SSH URL")
		}
		out[agentprotocol.SecretGitSSHKey] = string(creds.PrivateKeyPEM)
	}
	return out, nil
}
