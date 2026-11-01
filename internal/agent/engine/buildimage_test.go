package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/build"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	biDigest   = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	biGitToken = "ghp_secrettoken123"
	biRegPass  = "hunter2-password"
	biNPM      = "npm-secret-value"
)

// fakePusher stands in for *build.Builder's BuildAndPush.
type fakePusher struct {
	*fakeBuilder
	mu   sync.Mutex
	reqs []build.PushRequest
	fn   func(ctx context.Context, req build.PushRequest, logf func(string)) (build.PushResult, error)
}

func (p *fakePusher) BuildAndPush(ctx context.Context, req build.PushRequest, logf func(string)) (build.PushResult, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	if p.fn != nil {
		return p.fn(ctx, req, logf)
	}
	return build.PushResult{Digest: biDigest, Ref: req.Repository + "@" + biDigest}, nil
}

func newBuildRig(t *testing.T) (*rig, *fakePusher) {
	t.Helper()
	r := newRig(t, nil)
	p := &fakePusher{fakeBuilder: r.bld}
	r.eng.cfg.Builder = p
	return r, p
}

func buildImagePayload() agentprotocol.BuildImagePayload {
	return agentprotocol.BuildImagePayload{
		DeploymentID: depV2, ServiceID: svcID, ProjectID: "p-1",
		Git: agentprotocol.GitSource{
			URL: "https://example.com/r.git", CommitSHA: "0123456789abcdef0123456789abcdef01234567",
			DockerfilePath: "Dockerfile", BuildContext: ".", HasToken: true,
		},
		Platform: "linux/arm64", Repository: "reg.example.com/acme/web", Tag: "r-1a2b3c4d",
		Builder:         "nebulactrl-buildkit-0a1b2c3d",
		BuildArgKeys:    []string{"MODE"},
		BuildSecretKeys: []string{"NPM"},
	}
}

func buildImageSecrets() map[string]string {
	raw, _ := json.Marshal(agentprotocol.RegistryAuth{Username: "robot", Password: biRegPass, ServerAddress: "reg.example.com"})
	return map[string]string{
		agentprotocol.SecretGitToken:         biGitToken,
		agentprotocol.SecretRegistryPushAuth: base64.URLEncoding.EncodeToString(raw),
		"build:NPM":                          biNPM,
		"build:MODE":                         "production",
	}
}

func (r *rig) buildImage(p agentprotocol.BuildImagePayload, secrets map[string]string) (agentprotocol.ResultFrame, agentprotocol.BuildImageResult) {
	r.t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(r.t, err)
	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{
		ID: "build-1", Kind: agentprotocol.KindBuildImage, Payload: raw, Secrets: secrets,
	})
	var out agentprotocol.BuildImageResult
	require.NoError(r.t, json.Unmarshal(res.Data, &out))
	require.Equal(r.t, res.Outcome, out.Outcome)
	return res, out
}

func (r *rig) eventTexts() string {
	r.snd.mu.Lock()
	defer r.snd.mu.Unlock()
	var sb strings.Builder
	for _, e := range r.snd.events {
		sb.WriteString(e.Step + ":" + e.Status + " " + e.Message + "\n")
	}
	return sb.String()
}

func (r *rig) logsOf(stream string) string {
	r.snd.mu.Lock()
	defer r.snd.mu.Unlock()
	var sb strings.Builder
	for _, f := range r.snd.logs {
		if f.Stream != stream {
			continue
		}
		require.Equal(r.t, depV2, f.DeploymentID, "frames carry the deployment id")
		for _, l := range f.Lines {
			sb.WriteString(l.Text + "\n")
		}
	}
	return sb.String()
}

func TestBuildImageSucceedsWithBuildThenPushEventsAndADigest(t *testing.T) {
	r, p := newBuildRig(t)
	p.fn = func(_ context.Context, req build.PushRequest, logf func(string)) (build.PushResult, error) {
		logf("#1 RUN make")
		*req.Info = agentprotocol.BuildInfo{Method: agentprotocol.MethodDockerfile, Dockerfile: "Dockerfile"}
		req.OnPush()
		logf("#9 pushing layers")
		return build.PushResult{Digest: biDigest, Ref: req.Repository + "@" + biDigest, SizeBytes: 4242,
			Info: agentprotocol.BuildInfo{Method: agentprotocol.MethodDockerfile, Dockerfile: "Dockerfile"}}, nil
	}
	res, out := r.buildImage(buildImagePayload(), buildImageSecrets())

	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	require.Empty(t, res.Error)
	require.Equal(t, biDigest, out.Digest)
	require.Equal(t, "reg.example.com/acme/web", out.Repository)
	require.Equal(t, "r-1a2b3c4d", out.Tag)
	require.Equal(t, "linux/arm64", out.Platform)
	require.EqualValues(t, 4242, out.SizeBytes)
	require.NotNil(t, out.Build)
	require.Equal(t, "dockerfile", out.Build.Method)

	require.Equal(t, []string{"build:started", "build:succeeded", "push:started", "push:succeeded"}, r.snd.eventSteps())
	events := r.eventTexts()
	require.Contains(t, events, "build:started Building https://example.com/r.git @ 0123456 for linux/arm64")
	require.Contains(t, events, "build:succeeded Built with Dockerfile")
	require.Contains(t, events, "push:started Pushing reg.example.com/acme/web:r-1a2b3c4d")
	require.Contains(t, events, "push:succeeded Pushed reg.example.com/acme/web@"+biDigest)
	require.Contains(t, r.logsOf(agentprotocol.StreamBuild), "#1 RUN make")
	require.Contains(t, r.logsOf(agentprotocol.StreamDeploy), "[push] Pushing reg.example.com/acme/web:r-1a2b3c4d")

	// What the builder received.
	require.Len(t, p.reqs, 1)
	req := p.reqs[0]
	require.Equal(t, "linux/arm64", req.Platform)
	require.Equal(t, "nebulactrl-buildkit-0a1b2c3d", req.Builder)
	require.Equal(t, biGitToken, req.Token)
	require.Equal(t, "robot", req.Auth.Username)
	require.Equal(t, biRegPass, req.Auth.Password)
	require.Equal(t, map[string]string{"MODE": "production"}, req.Vars.Args)
	require.Equal(t, map[string]string{"NPM": biNPM}, req.Vars.Secrets)
	require.Equal(t, "web", req.ServiceSlug, "named after the repository, for logs only")
	require.False(t, req.Insecure)
}

func TestBuildImageFailureBeforeAndAfterThePushStarts(t *testing.T) {
	r, p := newBuildRig(t)
	p.fn = func(context.Context, build.PushRequest, func(string)) (build.PushResult, error) {
		return build.PushResult{}, errors.New("docker buildx build: exit status 1: failed to solve")
	}
	res, out := r.buildImage(buildImagePayload(), buildImageSecrets())
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Equal(t, out.Message, res.Error)
	require.Contains(t, out.Message, "failed to solve")
	require.Empty(t, out.Digest)
	require.Equal(t, []string{"build:started", "build:failed"}, r.snd.eventSteps())

	r2, p2 := newBuildRig(t)
	p2.fn = func(_ context.Context, req build.PushRequest, _ func(string)) (build.PushResult, error) {
		req.OnPush()
		return build.PushResult{}, errors.New("docker buildx build: exit status 1: denied: requested access to the resource is denied")
	}
	res, out = r2.buildImage(buildImagePayload(), buildImageSecrets())
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, out.Message, "denied")
	require.Equal(t, []string{"build:started", "build:succeeded", "push:started", "push:failed"}, r2.snd.eventSteps())
}

func TestBuildImageRejectsInvalidPayloadWithoutBuilding(t *testing.T) {
	r, p := newBuildRig(t)
	bad := buildImagePayload()
	bad.Platform = "linux/arm64 --privileged"
	res, out := r.buildImage(bad, buildImageSecrets())
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, out.Message, "invalid platform")
	require.Empty(t, p.reqs)
	require.Equal(t, []string{"build:started", "build:failed"}, r.snd.eventSteps())

	bad = buildImagePayload()
	bad.Git.DockerfilePath = "../../etc/passwd"
	_, out = r.buildImage(bad, buildImageSecrets())
	require.Contains(t, out.Message, "dockerfilePath")
	require.Empty(t, p.reqs)

	raw := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "x", Kind: agentprotocol.KindBuildImage, Payload: json.RawMessage(`{`)})
	require.Equal(t, agentprotocol.OutcomeFailed, raw.Outcome)
	raw = r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "x", Kind: agentprotocol.KindBuildImage, Payload: json.RawMessage(`{"deploymentId":"d"}`)})
	require.Contains(t, raw.Error, "serviceId")
}

func TestBuildImageNeedsAPusher(t *testing.T) {
	r := newRig(t, nil) // the plain fake builder cannot push
	res, out := r.buildImage(buildImagePayload(), buildImageSecrets())
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, out.Message, "cannot build and push")
}

func TestBuildImageCancelMidBuildReportsCanceledAndCleansUpNothingElse(t *testing.T) {
	r, p := newBuildRig(t)
	started := make(chan struct{})
	p.fn = func(ctx context.Context, _ build.PushRequest, _ func(string)) (build.PushResult, error) {
		close(started)
		<-ctx.Done()
		return build.PushResult{}, context.Cause(ctx)
	}
	done := make(chan agentprotocol.ResultFrame, 1)
	go func() {
		res, _ := r.buildImage(buildImagePayload(), buildImageSecrets())
		done <- res
	}()
	<-started
	cres := r.cancelDeployment(depV2, "alice")
	require.Equal(t, agentprotocol.OutcomeSucceeded, cres.Outcome)

	select {
	case res := <-done:
		require.Equal(t, agentprotocol.OutcomeCanceled, res.Outcome)
		var out agentprotocol.BuildImageResult
		require.NoError(t, json.Unmarshal(res.Data, &out))
		require.Equal(t, agentprotocol.StepBuild, out.CanceledAt)
		require.Contains(t, out.Message, "Canceled by alice during the build")
		require.Equal(t, out.Message, res.Error)
	case <-time.After(5 * time.Second):
		t.Fatal("the build did not stop after cancel_deploy")
	}
	steps := strings.Join(r.snd.eventSteps(), ",")
	require.Contains(t, steps, "cancel:started")
	require.Contains(t, steps, "cancel:succeeded")
	require.False(t, r.eng.builds.busy(), "the build is no longer registered")
	require.Empty(t, r.dk.stopped, "the agent's containers were not touched")
}

func TestBuildImageCancelDuringPushIsLabeledAsPush(t *testing.T) {
	r, p := newBuildRig(t)
	started := make(chan struct{})
	p.fn = func(ctx context.Context, req build.PushRequest, _ func(string)) (build.PushResult, error) {
		req.OnPush()
		close(started)
		<-ctx.Done()
		return build.PushResult{}, context.Cause(ctx)
	}
	done := make(chan agentprotocol.BuildImageResult, 1)
	go func() {
		_, out := r.buildImage(buildImagePayload(), buildImageSecrets())
		done <- out
	}()
	<-started
	r.cancelDeployment(depV2, "bob")
	out := <-done
	require.Equal(t, agentprotocol.OutcomeCanceled, out.Outcome)
	require.Equal(t, agentprotocol.StepPush, out.CanceledAt)
	require.Contains(t, out.Message, "during the image push")
}

func TestBuildHoldsTheNoPruneLockWhileItRuns(t *testing.T) {
	r, p := newBuildRig(t)
	var busyDuring bool
	p.fn = func(_ context.Context, req build.PushRequest, _ func(string)) (build.PushResult, error) {
		busyDuring = r.eng.builds.busy()
		return build.PushResult{Digest: biDigest}, nil
	}
	require.False(t, r.eng.builds.busy())
	r.buildImage(buildImagePayload(), buildImageSecrets())
	require.True(t, busyDuring)
	require.False(t, r.eng.builds.busy(), "released afterwards")

	// A failing build releases it too.
	p.fn = func(context.Context, build.PushRequest, func(string)) (build.PushResult, error) {
		return build.PushResult{}, errors.New("boom")
	}
	r.buildImage(buildImagePayload(), buildImageSecrets())
	require.False(t, r.eng.builds.busy())
}

func TestBuildImageNeverLeaksSecretsIntoLogsEventsOrResults(t *testing.T) {
	r, p := newBuildRig(t)
	p.fn = func(_ context.Context, req build.PushRequest, logf func(string)) (build.PushResult, error) {
		logf("git token " + biGitToken + " registry password " + biRegPass + " npm " + biNPM)
		basic := base64.StdEncoding.EncodeToString([]byte("robot:" + biRegPass))
		logf("docker config auth " + basic)
		return build.PushResult{}, errors.New("push to reg.example.com failed for robot:" + biRegPass + " with token " + biGitToken)
	}
	res, out := r.buildImage(buildImagePayload(), buildImageSecrets())

	all := r.logsOf(agentprotocol.StreamBuild) + r.logsOf(agentprotocol.StreamDeploy) + r.eventTexts() + out.Message + res.Error + string(res.Data)
	for _, s := range []string{biGitToken, biRegPass, biNPM, base64.StdEncoding.EncodeToString([]byte("robot:" + biRegPass))} {
		require.NotContains(t, all, s)
	}
	require.Contains(t, r.logsOf(agentprotocol.StreamBuild), redactMark)
	require.Contains(t, out.Message, redactMark)
	// A plain build arg is not a secret.
	require.NotContains(t, r.logsOf(agentprotocol.StreamBuild), "production-secret")
}

func TestRegistryCheckCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, pw, ok := r.BasicAuth(); ok && u == "robot" && pw == biRegPass {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Www-Authenticate", `Basic realm="x"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	r := newRig(t, nil)
	check := func(secrets map[string]string) (agentprotocol.ResultFrame, agentprotocol.RegistryCheckResult) {
		raw, _ := json.Marshal(agentprotocol.RegistryCheckPayload{Host: host, Insecure: true})
		res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "rc", Kind: agentprotocol.KindRegistryCheck, Payload: raw, Secrets: secrets})
		var out agentprotocol.RegistryCheckResult
		require.NoError(t, json.Unmarshal(res.Data, &out))
		return res, out
	}
	enc := func(user, pass string) string {
		raw, _ := json.Marshal(agentprotocol.RegistryAuth{Username: user, Password: pass})
		return base64.URLEncoding.EncodeToString(raw)
	}

	res, out := check(map[string]string{agentprotocol.SecretRegistryPushAuth: enc("robot", biRegPass)})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	require.Equal(t, agentprotocol.RegistryCheckResult{Push: true, Pull: true}, out)

	// A pull credential (SecretRegistryAuth) proves pull only.
	res, out = check(map[string]string{agentprotocol.SecretRegistryAuth: enc("robot", biRegPass)})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	require.Equal(t, agentprotocol.RegistryCheckResult{Pull: true}, out)

	res, out = check(map[string]string{agentprotocol.SecretRegistryPushAuth: enc("robot", "wrong-password-1")})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, out.Error, "rejected the credential")
	require.Equal(t, out.Error, res.Error)
	require.NotContains(t, string(res.Data), "wrong-password-1")

	res, out = check(nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, out.Error, "requires authentication")

	bad := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "rc", Kind: agentprotocol.KindRegistryCheck, Payload: json.RawMessage(`{}`)})
	require.Equal(t, agentprotocol.OutcomeFailed, bad.Outcome)
}

func TestBuildOnlyEngineRunsNoProxyAndNeverSweepsTheDaemon(t *testing.T) {
	dk := newFakeDocker()
	snd := &fakeSender{}
	_, err := New(Config{Docker: dk, Sender: snd})
	require.ErrorContains(t, err, "Proxy")

	eng, err := New(Config{Docker: dk, Sender: snd, BuildOnly: true, Builder: &fakePusher{fakeBuilder: &fakeBuilder{}}})
	require.NoError(t, err)

	// A container of another agent on the same daemon (older than the orphan
	// sweep's grace) must survive a reconcile of the build-only agent.
	other := dk.seed("nc-web-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	_ = other
	eng.cfg.OrphanMinAge = time.Nanosecond
	require.NoError(t, eng.Reconcile(context.Background(), agentprotocol.Reconcile{ACMEEmail: "ops@example.com"}))
	require.True(t, running(t, dk, "nc-web-d1d1d1d1-0"), "no orphan sweep on a build-only agent")
	require.Empty(t, snd.observed, "no observed report either")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	// It still builds.
	res := eng.HandleCommand(context.Background(), agentprotocol.Command{
		ID: "b", Kind: agentprotocol.KindBuildImage, Payload: mustJSON(t, buildImagePayload()), Secrets: buildImageSecrets(),
	})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}

func TestBuildImageKindsAreAllowed(t *testing.T) {
	require.True(t, agentprotocol.AllowedKinds[agentprotocol.KindBuildImage])
	require.True(t, agentprotocol.AllowedKinds[agentprotocol.KindRegistryCheck])
}
