package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path"
	"sync/atomic"

	"github.com/nebulactrl/nebulactrl/internal/agent/build"
	"github.com/nebulactrl/nebulactrl/internal/agent/logs"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Pusher builds a git source and pushes the image to a registry. *build.Builder
// implements it; a Builder without it cannot serve build_image.
type Pusher interface {
	BuildAndPush(ctx context.Context, req build.PushRequest, logf func(string)) (build.PushResult, error)
}

// buildActivity counts the builds running on this agent. Docker cleanup asks
// busy() and then leaves images and build cache alone (Coolify pruned with -af
// in the middle of a build; see docs/operations/docker-cleanup.md).
type buildActivity struct{ n atomic.Int32 }

// begin registers a running build; the returned function ends it.
func (a *buildActivity) begin() func() {
	a.n.Add(1)
	return func() { a.n.Add(-1) }
}

func (a *buildActivity) busy() bool { return a.n.Load() > 0 }

// noProxy is the proxy of a BuildOnly engine: it serves no routes.
type noProxy struct{}

func (noProxy) Apply([]agentprotocol.Route) error { return nil }
func (noProxy) Current() []agentprotocol.Route    { return nil }
func (noProxy) Probe(context.Context, string, string) (int, error) {
	return 0, errors.New("this agent runs no proxy")
}
func (noProxy) SetACMEEmail(string) {}

// decodeRegistryAuth reads the Docker X-Registry-Auth form of a credential
// (base64url JSON); the other base64 alphabets are accepted too.
func decodeRegistryAuth(s string) (*agentprotocol.RegistryAuth, error) {
	if s == "" {
		return nil, nil
	}
	var raw []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.URLEncoding, base64.RawURLEncoding, base64.StdEncoding, base64.RawStdEncoding} {
		if raw, err = enc.DecodeString(s); err == nil {
			break
		}
	}
	if err != nil {
		return nil, errors.New("the registry credential is not valid base64")
	}
	var a agentprotocol.RegistryAuth
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, errors.New("the registry credential is not valid JSON")
	}
	if a.Username == "" && a.Password == "" {
		return nil, nil
	}
	return &a, nil
}

// withRegistrySecrets returns secrets extended with the parts of the registry
// credential the redactor cannot derive from the base64 blob: the password and
// the user:password pair (the docker config's auth value is its base64).
func withRegistrySecrets(secrets map[string]string, a *agentprotocol.RegistryAuth) map[string]string {
	out := make(map[string]string, len(secrets)+2)
	for k, v := range secrets {
		out[k] = v
	}
	if a != nil {
		out["registry_password"] = a.Password
		out["registry_basic"] = a.Username + ":" + a.Password
	}
	return out
}

func buildImageResult(id string, r agentprotocol.BuildImageResult) agentprotocol.ResultFrame {
	data, _ := json.Marshal(r)
	f := agentprotocol.ResultFrame{CommandID: id, Outcome: r.Outcome, Data: data}
	if r.Outcome != agentprotocol.OutcomeSucceeded {
		f.Error = r.Message
	}
	return f
}

// handleBuildImage clones the payload's commit, builds it for one platform and
// pushes it to the registry (see build.Builder.BuildAndPush). It reports the
// steps build and push for the deployment, streams the build output as the
// deployment's build log and returns a BuildImageResult. A cancel_deploy for
// the deployment stops it; nothing needs unwinding (a pushed image is harmless,
// and the workspace and the credential directory are always removed).
func (e *Engine) handleBuildImage(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.BuildImagePayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return buildImageResult(cmd.ID, agentprotocol.BuildImageResult{Outcome: agentprotocol.OutcomeFailed, Message: "invalid build_image payload: " + err.Error()})
	}
	fail := func(msg string) agentprotocol.ResultFrame {
		return buildImageResult(cmd.ID, agentprotocol.BuildImageResult{Outcome: agentprotocol.OutcomeFailed, Message: msg})
	}
	if p.DeploymentID == "" || p.ServiceID == "" {
		return fail("invalid build_image payload: deploymentId and serviceId are required")
	}
	pusher, ok := e.cfg.Builder.(Pusher)
	if !ok || e.cfg.Builder == nil {
		return fail("this agent cannot build and push images (builder not configured)")
	}
	auth, err := decodeRegistryAuth(cmd.Secrets[agentprotocol.SecretRegistryPushAuth])
	if err != nil {
		return fail(err.Error())
	}

	secretKeys := make([]string, 0, len(p.BuildSecretKeys))
	vars := build.Vars{Args: map[string]string{}, Secrets: map[string]string{}}
	for _, k := range p.BuildArgKeys {
		if v, ok := cmd.Secrets[agentprotocol.SecretBuildPrefix+k]; ok {
			vars.Args[k] = v
		}
	}
	for _, k := range p.BuildSecretKeys {
		secretKeys = append(secretKeys, agentprotocol.SecretBuildPrefix+k)
		if v, ok := cmd.Secrets[agentprotocol.SecretBuildPrefix+k]; ok {
			vars.Secrets[k] = v
		}
	}
	red := newRedactor(withRegistrySecrets(cmd.Secrets, auth), secretKeys)

	// Registered before anything else: a cancel can arrive at any moment.
	ctx, ctl, release := e.cancel.start(ctx, p.DeploymentID)
	defer release()
	defer e.builds.begin()()

	log := e.log.With("correlation_id", p.DeploymentID, "service_id", p.ServiceID)
	blog := logs.NewBatcher(agentprotocol.LogFrame{
		DeploymentID: p.DeploymentID, ServiceID: p.ServiceID, Stream: agentprotocol.StreamBuild,
	}, e.cfg.Sender.Log, red.apply)
	defer blog.Close()
	dlog := logs.NewBatcher(agentprotocol.LogFrame{
		DeploymentID: p.DeploymentID, ServiceID: p.ServiceID, Stream: agentprotocol.StreamDeploy,
	}, e.cfg.Sender.Log, red.apply)
	defer dlog.Close()

	event := func(step, status, msg string) {
		if status == agentprotocol.StepStarted && step != agentprotocol.StepCancel {
			ctl.setStep(step)
		}
		msg = red.apply(msg)
		e.cfg.Sender.Event(agentprotocol.EventFrame{DeploymentID: p.DeploymentID, Step: step, Status: status, Message: msg})
		if msg != "" {
			dlog.Add("[" + step + "] " + msg)
		}
	}
	canceledResult := func() agentprotocol.ResultFrame {
		step := ctl.stepName()
		by := ctl.canceledBy()
		if by == "" {
			by = "a user"
		}
		msg := "Canceled by " + by + " during " + stepLabel(step) + ". Nothing was changed on the target."
		event(agentprotocol.StepCancel, agentprotocol.StepSucceeded, msg)
		log.Info("build canceled", "step", step)
		return buildImageResult(cmd.ID, agentprotocol.BuildImageResult{Outcome: agentprotocol.OutcomeCanceled, Message: msg, CanceledAt: step})
	}

	tag := p.Repository + ":" + p.Tag
	event(agentprotocol.StepBuild, agentprotocol.StepStarted,
		"Building "+p.Git.URL+" @ "+shortSHA(p.Git.CommitSHA)+" for "+p.Platform)
	log.Info("build_image started", "repository", p.Repository, "platform", p.Platform, "builder", p.Builder)
	if canceled(ctx) {
		return canceledResult()
	}

	var info agentprotocol.BuildInfo
	req := build.PushRequest{
		Request: build.Request{
			DeploymentID: p.DeploymentID, ServiceID: p.ServiceID, ServiceSlug: path.Base(p.Repository),
			GitURL: p.Git.URL, CommitSHA: p.Git.CommitSHA,
			DockerfilePath: p.Git.DockerfilePath, BuildContext: p.Git.BuildContext, BuildType: p.Git.BuildType,
			NoCache: p.Git.NoCache, Submodules: p.Git.Submodules, CacheKey: p.ProjectID,
			Vars: vars, Info: &info,
		},
		Platform: p.Platform, Repository: p.Repository, Tag: p.Tag, Builder: p.Builder,
		CacheRef: p.CacheRef, Insecure: p.Insecure, Auth: auth,
	}
	if p.Git.HasToken {
		req.Token = cmd.Secrets[agentprotocol.SecretGitToken]
		req.TokenUser = cmd.Secrets[agentprotocol.SecretGitUsername]
	}
	if p.Git.HasSSHKey {
		req.SSHKey = cmd.Secrets[agentprotocol.SecretGitSSHKey]
		req.KnownHosts = p.Git.KnownHosts
	}
	var pushing atomic.Bool
	req.OnPush = func() {
		if pushing.Swap(true) {
			return
		}
		msg := "Built"
		if info.Method != "" {
			msg = "Built with " + buildMethodLabel(info)
		}
		event(agentprotocol.StepBuild, agentprotocol.StepSucceeded, msg)
		event(agentprotocol.StepPush, agentprotocol.StepStarted, "Pushing "+tag)
	}
	failStep := func() string {
		if pushing.Load() {
			return agentprotocol.StepPush
		}
		return agentprotocol.StepBuild
	}

	if err := req.Validate(); err != nil {
		msg := red.apply("invalid build_image payload: " + err.Error())
		event(agentprotocol.StepBuild, agentprotocol.StepFailed, msg)
		return fail(msg)
	}
	res, err := pusher.BuildAndPush(ctx, req, blog.Add)
	if err != nil {
		switch {
		case canceled(ctx):
			return canceledResult()
		case ctx.Err() != nil:
			msg := "agent stopped during the build"
			event(failStep(), agentprotocol.StepFailed, msg)
			return fail(msg)
		}
		msg := red.apply(err.Error())
		event(failStep(), agentprotocol.StepFailed, msg)
		log.Info("build_image failed", "step", failStep())
		return fail(msg)
	}
	req.OnPush()      // a builder that never named the push phase still reports build then push
	if ctl.commit() { // nothing after this can be undone: a late cancel is too late
		event(agentprotocol.StepPush, agentprotocol.StepSucceeded, "Pushed "+res.Ref)
	} else {
		return canceledResult()
	}
	log.Info("build_image finished", "digest", res.Digest)
	out := agentprotocol.BuildImageResult{
		Outcome: agentprotocol.OutcomeSucceeded, Digest: res.Digest,
		Repository: p.Repository, Tag: p.Tag, Platform: p.Platform, SizeBytes: res.SizeBytes,
	}
	if res.Info.Method != "" {
		bi := res.Info
		out.Build = &bi
	}
	return buildImageResult(cmd.ID, out)
}

// handleRegistryCheck reports whether a registry is reachable and accepts the
// command's credential (SecretRegistryPushAuth: push and pull, SecretRegistryAuth:
// pull only, none: anonymous). The result's Data is a RegistryCheckResult; the
// outcome is failed when the registry is unusable, with the reason as Error.
func (e *Engine) handleRegistryCheck(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.RegistryCheckPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || p.Host == "" {
		return failedResult(cmd.ID, "invalid registry_check payload")
	}
	wantPush := true
	raw := cmd.Secrets[agentprotocol.SecretRegistryPushAuth]
	if raw == "" {
		wantPush, raw = false, cmd.Secrets[agentprotocol.SecretRegistryAuth]
	}
	auth, err := decodeRegistryAuth(raw)
	if err != nil {
		return failedResult(cmd.ID, err.Error())
	}
	red := newRedactor(withRegistrySecrets(cmd.Secrets, auth), nil)
	res := build.CheckRegistry(ctx, e.cfg.RegistryHTTP, build.RegistryCheck{
		Host: p.Host, Insecure: p.Insecure, Repository: p.Repository, Auth: auth, WantPush: wantPush,
	})
	res.Error = red.apply(res.Error)
	data, _ := json.Marshal(res)
	f := agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: data}
	if res.Error != "" {
		f.Outcome, f.Error = agentprotocol.OutcomeFailed, res.Error
	}
	e.log.Info("registry check", "host", p.Host, "pull", res.Pull, "push", res.Push, "ok", res.Error == "")
	return f
}
