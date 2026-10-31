package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	// buildPlanWait bounds how long the preview request waits for the agent
	// (clone, planner container, version resolution on a cold host).
	buildPlanWait     = 150 * time.Second
	buildPlanPollTick = 400 * time.Millisecond
)

// buildPlanKeys lists the build-time variables (plain and secret) a build of
// the service would receive right now. Keys only: values are attached at send
// time (SecretsForCommand).
func buildPlanKeys(in *inputs) (args, secrets []string) {
	if in.resolveErr != nil {
		return nil, nil
	}
	for k, sc := range in.resolved.Scope {
		if sc != services.ScopeBuild && sc != services.ScopeBoth {
			continue
		}
		args, secrets = classifyBuildVar(args, secrets, k, in.resolved.Secret[k])
	}
	sort.Strings(args)
	sort.Strings(secrets)
	return args, secrets
}

// BuildPlan reports how the service's git source would be built: the build
// method that applies and why, and for Railpack the detected providers,
// packages, steps and start command. The agent on the service's server clones
// the branch head and plans it; nothing is built or deployed. It waits for the
// agent, so it fails with 409 server_offline instead of queueing for an
// offline server.
func (s *Service) BuildPlan(ctx context.Context, serviceID uuid.UUID) (agentprotocol.BuildPlanResult, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeDeploymentsWrite, serviceID)
	if err != nil {
		return agentprotocol.BuildPlanResult{}, err
	}
	if sc.Service.SourceType != services.SourceGit {
		return agentprotocol.BuildPlanResult{}, apierr.Unprocessable("not_git_source", "Build plans exist only for services with a git source.")
	}
	in, err := s.gather(ctx, s.q, sc)
	if err != nil {
		return agentprotocol.BuildPlanResult{}, err
	}
	if err := in.cfg.Validate(services.SourceGit); err != nil {
		return agentprotocol.BuildPlanResult{}, apierr.Unprocessable("invalid_config", err.Error())
	}
	if in.server == nil {
		return agentprotocol.BuildPlanResult{}, apierr.Unprocessable("no_server", "Assign the service to a server first: the plan is made on the server that will build it.")
	}
	// The plan is made where the build runs: the build server when the
	// service's effective build location is one, else the target.
	host, err := s.buildHost(ctx, s.q, sc, in.cfg, *in.server)
	if err != nil {
		return agentprotocol.BuildPlanResult{}, err
	}
	switch {
	case host.Status == "revoked":
		return agentprotocol.BuildPlanResult{}, apierr.Conflict("server_revoked", "server revoked: "+host.Name+" was revoked; assign the service to another server.")
	case host.Status != "online":
		return agentprotocol.BuildPlanResult{}, apierr.Conflict("server_offline", host.Name+" is offline; the plan is made on the server that will build it.")
	}
	sha, err := s.resolveGit(ctx, sc.Service, in.cfg)
	if err != nil {
		if errors.Is(err, errGitToken) {
			return agentprotocol.BuildPlanResult{}, apierr.Unprocessable("git_token_unreadable", "The stored git token could not be decrypted; set it again.")
		}
		return agentprotocol.BuildPlanResult{}, apierr.Unprocessable("git_unreachable", "Could not resolve the git source: "+err.Error())
	}
	argKeys, secretKeys := buildPlanKeys(in)
	payload := agentprotocol.BuildPlanPayload{
		ServiceID: sc.Service.ID.String(), ServiceSlug: sc.Service.Slug,
		GitURL: in.cfg.GitURL, CommitSHA: sha, BuildType: in.cfg.BuildType,
		DockerfilePath: in.cfg.DockerfilePath, BuildContext: in.cfg.BuildContext,
		BuildArgKeys: argKeys, BuildSecretKeys: secretKeys, Submodules: in.cfg.Submodules,
	}
	creds, err := s.sshCreds(ctx, sc.Service.ID, in.cfg.GitURL)
	if err != nil {
		return agentprotocol.BuildPlanResult{}, apierr.Unprocessable("git_ssh_unavailable", s.sshCredsMessage(ctx, sc.Service.ID, err))
	}
	if creds != nil {
		payload.HasSSHKey, payload.KnownHosts = true, creds.KnownHosts
	} else {
		payload.HasToken = s.hasGitAuth(ctx, sc.Service)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return agentprotocol.BuildPlanResult{}, err
	}
	var cmdID uuid.UUID
	if err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		cmdID, err = s.Dispatcher.Enqueue(ctx, tx, agents.NewCommand{ServerID: host.ID, Kind: agentprotocol.KindBuildPlan, Payload: raw})
		return err
	}); err != nil {
		return agentprotocol.BuildPlanResult{}, fmt.Errorf("enqueue build plan: %w", err)
	}
	return s.awaitBuildPlan(ctx, cmdID, host.Name)
}

// awaitBuildPlan polls the command row until the agent reports. A command the
// agent never took is canceled so it cannot run later.
func (s *Service) awaitBuildPlan(ctx context.Context, id uuid.UUID, serverName string) (agentprotocol.BuildPlanResult, error) {
	deadline := time.NewTimer(buildPlanWait)
	defer deadline.Stop()
	tick := time.NewTicker(buildPlanPollTick)
	defer tick.Stop()
	giveUp := func(msg string) (agentprotocol.BuildPlanResult, error) {
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = s.q.CancelUnackedAgentCommand(bg, db.CancelUnackedAgentCommandParams{ID: id, Error: msg})
		return agentprotocol.BuildPlanResult{}, apierr.New(http.StatusGatewayTimeout, "build_plan_timeout", msg)
	}
	for {
		cmd, err := s.q.GetAgentCommand(ctx, id)
		if err != nil {
			return agentprotocol.BuildPlanResult{}, fmt.Errorf("load build plan command: %w", err)
		}
		switch cmd.Status {
		case "succeeded":
			var frame agentprotocol.ResultFrame
			var res agentprotocol.BuildPlanResult
			if err := json.Unmarshal(cmd.Result, &frame); err != nil || json.Unmarshal(frame.Data, &res) != nil {
				return agentprotocol.BuildPlanResult{}, fmt.Errorf("decode build plan result for command %s", id)
			}
			return res, nil
		case "failed":
			msg := "The build plan failed on " + serverName + "."
			if cmd.Error != "" {
				msg = cmd.Error
			}
			return agentprotocol.BuildPlanResult{}, apierr.Unprocessable("build_plan_failed", msg)
		case "expired", "canceled":
			return agentprotocol.BuildPlanResult{}, apierr.Conflict("server_offline", serverName+" did not take the request; try again when it is online.")
		}
		select {
		case <-ctx.Done():
			return giveUp("The request was canceled.")
		case <-deadline.C:
			return giveUp("The plan took too long on " + serverName + "; the first plan on a server downloads Railpack's tooling, try again shortly.")
		case <-tick.C:
		}
	}
}

// buildPlanSecrets attaches the send-time secrets of a build_plan command: the
// git token and the current values of the build-time variables it lists.
func (s *Service) buildPlanSecrets(ctx context.Context, payload json.RawMessage) (map[string]string, error) {
	var p agentprotocol.BuildPlanPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("decode build_plan payload: %w", err)
	}
	svcID, err := uuid.Parse(p.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("build_plan payload service id: %w", err)
	}
	sc, err := services.LoadScopeInternal(ctx, s.q, svcID)
	if err != nil {
		return nil, fmt.Errorf("load service: %w", err)
	}
	out := map[string]string{}
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
	if len(p.BuildArgKeys)+len(p.BuildSecretKeys) > 0 {
		in, err := s.gather(ctx, s.q, sc)
		if err != nil {
			return nil, err
		}
		if in.resolveErr == nil {
			for _, k := range append(append([]string(nil), p.BuildArgKeys...), p.BuildSecretKeys...) {
				if v, ok := in.resolved.Vars[k]; ok {
					out[agentprotocol.SecretBuildPrefix+k] = v
				}
			}
		}
	}
	return out, nil
}

type buildPlanOutput struct{ Body agentprotocol.BuildPlanResult }

func registerBuildPlan(h huma.API, svc *Service) {
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "previewBuildPlan", Method: http.MethodPost, Path: "/services/{serviceId}/build-plan", Tags: []string{"deployments"},
		Summary: "Preview how a git service would be built",
		Description: "Asks the agent of the service's server to clone the branch head and report the build method (the repository's " +
			"Dockerfile, Railpack or a generated starter Dockerfile), why it applies, and for Railpack the detected providers, packages, " +
			"steps and start command. Nothing is built or deployed. The request waits for the agent (up to 150 s): 409 `server_offline` " +
			"when the server is offline, 504 `build_plan_timeout` when it does not answer. Requires `deployments:write` because it makes " +
			"the server clone the repository and run the planner.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusGatewayTimeout},
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *serviceIDInput) (*buildPlanOutput, error) {
		res, err := svc.BuildPlan(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &buildPlanOutput{Body: res}, nil
	})
}
