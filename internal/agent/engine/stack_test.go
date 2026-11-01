package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/compose"
	"github.com/nebulactrl/nebulactrl/internal/agent/compose/composetest"
	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/netpool"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
	"github.com/nebulactrl/nebulactrl/pkg/composestack"
)

const (
	stackSecret  = "tok-supersecret-123"
	stackProject = "nc-5e5e5e5e"
	stackHost    = "stack.localhost"

	stackCompose = "services:\n  api:\n    image: traefik/whoami:latest\n    environment:\n      TOKEN: ${API_TOKEN}\n  worker:\n    image: busybox:1\n"
	stackModel   = `{"name":"nc-5e5e5e5e","services":{"api":{"image":"traefik/whoami:latest","ports":[{"mode":"ingress","target":80,"protocol":"tcp"}]},"worker":{"image":"busybox:1"}}}`
)

var stackImages = map[string]string{"api": "traefik/whoami:latest", "worker": "busybox:1", "migrate": "busybox:1", "bad": "busybox:1"}

// hookDocker lets a test run code when the engine inspects an image (which a
// stack deploy does after its point of no return).
type hookDocker struct {
	*fakeDocker
	onImageInspect func()
}

func (h *hookDocker) ImageInspect(ctx context.Context, ref string) (docker.ImageInfo, error) {
	if h.onImageInspect != nil {
		h.onImageInspect()
	}
	return h.fakeDocker.ImageInspect(ctx, ref)
}

// stackRig is an engine with a fake docker daemon, proxy and compose runner.
type stackRig struct {
	*rig
	cr  *composetest.Fake
	hs  *hookSender
	hd  *hookDocker
	dir string

	mu sync.Mutex
	// upBehaviour decides what the fake `docker compose up` does: failComp
	// names a component whose container exits 1 (with a log line), err is what
	// up returns. n counts up calls from 1; dep is the override's deployment.
	upBehaviour func(n int, dep string) (failComp string, err error)
	ups         int
}

func newStackRig(t *testing.T) *stackRig {
	t.Helper()
	s := newStackRigWith(t, nil)
	return s
}

func newStackRigWith(t *testing.T, mutate func(*Config)) *stackRig {
	t.Helper()
	r := &rig{t: t, dk: newFakeDocker(), px: &fakeProxy{}, snd: &fakeSender{}, bld: &fakeBuilder{}}
	s := &stackRig{rig: r, cr: &composetest.Fake{ConfigJSON: stackModel}, hs: &hookSender{fakeSender: r.snd}, dir: t.TempDir()}
	s.hd = &hookDocker{fakeDocker: r.dk}
	cfg := Config{
		Docker: s.hd, Proxy: r.px, Sender: s.hs, Health: healthy,
		DrainDelay: time.Millisecond, VerifyInterval: time.Millisecond,
		Networks: hermeticPool(t, netpool.DefaultPool, netpool.DefaultPrefixLen),
		Compose:  s.cr, StackDir: s.dir,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	e, err := New(cfg)
	require.NoError(t, err)
	r.eng = e
	s.cr.UpFn = s.fakeUp
	return s
}

var overrideDeployment = regexp.MustCompile(`for deployment ([0-9A-Za-z-]+)`)

// fakeUp does what `docker compose up -d --wait` does to the daemon: it
// replaces the stack's containers with ones of the override's deployment.
func (s *stackRig) fakeUp(_ context.Context, c composetest.Call, out compose.Output) error {
	s.mu.Lock()
	s.ups++
	n := s.ups
	behaviour := s.upBehaviour
	s.mu.Unlock()

	override := c.Files[c.Project.Files[1]]
	m := overrideDeployment.FindStringSubmatch(override)
	if m == nil {
		return fmt.Errorf("override has no deployment: %q", override)
	}
	dep := m[1]
	failComp, err := "", error(nil)
	if behaviour != nil {
		failComp, err = behaviour(n, dep)
	}
	if out != nil {
		out("Container " + stackProject + "-api  Started, TOKEN=" + stackSecret)
	}
	s.replaceStack(dep, failComp)
	return err
}

// replaceStack removes the stack's containers and creates one per component of
// the model for deployment dep; failComp's container has exited with code 1.
func (s *stackRig) replaceStack(dep, failComp string) {
	s.dk.mu.Lock()
	for id, c := range s.dk.containers {
		if c.Labels[agentprotocol.LabelService] == svcID {
			delete(s.dk.containers, id)
		}
	}
	s.dk.mu.Unlock()
	for i, name := range []string{"api", "worker"} {
		if name == failComp {
			s.spawn(name, i, dep, "exited", 1, 0)
			s.dk.mu.Lock()
			s.dk.logText[agentprotocol.StackContainerName(svcID, name)] = "boom: cannot connect to the database\nlast words"
			s.dk.mu.Unlock()
			continue
		}
		s.spawn(name, i, dep, "running", 0, 0)
	}
}

// spawn adds a stack container to the fake daemon.
func (s *stackRig) spawn(component string, index int, dep, state string, exitCode int, age time.Duration) {
	dk := s.dk
	dk.mu.Lock()
	defer dk.mu.Unlock()
	dk.seq++
	image := stackImages[component]
	repo := image[:strings.IndexByte(image, ':')]
	imageID := "sha256:img-" + image
	dk.images[image] = docker.ImageInfo{ID: imageID, RepoDigests: []string{repo + "@sha256:abc123"}}
	c := &docker.ContainerInfo{
		ID: fmt.Sprintf("stk%03d", dk.seq), Name: agentprotocol.StackContainerName(svcID, component),
		Image: image, ImageID: imageID, Created: time.Now().Add(-age),
		Labels: map[string]string{
			agentprotocol.LabelManaged: "true", agentprotocol.LabelService: svcID, agentprotocol.LabelDeployment: dep,
			agentprotocol.LabelEnvironment: envID, agentprotocol.LabelComponent: component,
			agentprotocol.LabelReplica: fmt.Sprint(index),
		},
	}
	switch state {
	case "running":
		c.State, c.Running, c.StartedAt = "running", true, time.Now().Add(-time.Hour)
	default:
		c.State, c.ExitCode = "exited", exitCode
	}
	dk.containers[c.ID] = c
}

func (s *stackRig) components() []agentprotocol.StackComponent {
	p, err := composestack.Parse([]byte(stackModel))
	require.NoError(s.t, err)
	return p.Components()
}

func stackRoutes() []agentprotocol.Route {
	return []agentprotocol.Route{{
		ServiceID: agentprotocol.StackRouteID(svcID, "api", 80), Stack: svcID, Hosts: []string{stackHost},
		Upstreams: []string{agentprotocol.StackContainerName(svcID, "api") + ":80"},
		Networks:  []string{agentprotocol.EnvNetwork(envID)},
	}}
}

func (s *stackRig) payload(dep string) agentprotocol.DeployPayload {
	return agentprotocol.DeployPayload{
		DeploymentID: dep, ReleaseID: "rel-" + dep[:2], ServiceID: svcID, ServiceSlug: "stack", EnvironmentID: envID,
		Strategy: agentprotocol.StrategyRecreate, Replicas: 1, StopGraceSeconds: 5,
		EnvKeys: []string{"API_TOKEN"}, SecretEnvKeys: []string{"API_TOKEN"},
		Routes: []agentprotocol.Route{{ServiceID: "other", Hosts: []string{"other.localhost"}, Upstreams: []string{"nc-other-aaaaaaaa-0:80"}}},
		Stack:  &agentprotocol.StackSpec{Compose: stackCompose, Components: s.components(), Routes: stackRoutes(), WaitSeconds: 60},
	}
}

func (s *stackRig) deployStack(p agentprotocol.DeployPayload) (agentprotocol.ResultFrame, agentprotocol.DeployResult) {
	s.t.Helper()
	return s.deploy(p, map[string]string{"env:API_TOKEN": stackSecret})
}

func (s *stackRig) stackDir(name string) string {
	return filepath.Join(s.dir, agentprotocol.Short(svcID), name)
}

func (s *stackRig) meta(dir string) stackMeta {
	s.t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.stackDir(dir), stackMetaFile))
	require.NoError(s.t, err)
	var m stackMeta
	require.NoError(s.t, json.Unmarshal(raw, &m))
	return m
}

func (s *stackRig) liveDeployments() map[string]bool {
	out := map[string]bool{}
	s.dk.mu.Lock()
	defer s.dk.mu.Unlock()
	for _, c := range s.dk.containers {
		if c.Labels[agentprotocol.LabelService] == svcID {
			out[c.Labels[agentprotocol.LabelDeployment]] = true
		}
	}
	return out
}

func stackRouteIn(table []agentprotocol.Route) (agentprotocol.Route, bool) {
	for _, r := range table {
		if r.Stack == svcID {
			return r, true
		}
	}
	return agentprotocol.Route{}, false
}

// ---- deploy ----

func TestStackDeploySucceeds(t *testing.T) {
	s := newStackRig(t)
	s.px.Apply([]agentprotocol.Route{{ServiceID: "other", Hosts: []string{"other.localhost"}, Upstreams: []string{"nc-other-aaaaaaaa-0:80"}}})
	s.px.applies = nil

	res, dr := s.deployStack(s.payload(depV1))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, stackProject)

	// What ran: version, normalise, pull, up with --wait and orphan removal.
	require.Equal(t, []string{"version", "config", "pull", "up"}, s.cr.Ops())
	up := s.cr.Of("up")[0]
	require.Equal(t, compose.UpOptions{Wait: true, WaitTimeout: 60 * time.Second, RemoveOrphans: true}, up.Up)
	require.Equal(t, stackProject, up.Project.Name)
	require.Equal(t, filepath.Join(s.dir, "5e5e5e5e"), up.Project.Dir)
	require.Equal(t, []string{"API_TOKEN=" + stackSecret}, up.Project.Env)
	next := s.stackDir(stackNextDir)
	require.Equal(t, []string{filepath.Join(next, stackComposeFile), filepath.Join(next, stackOverrideFile)}, up.Project.Files)

	// While it ran the files were in next/, mode 0600, and the compose file is verbatim.
	require.Equal(t, stackCompose, up.Files[up.Project.Files[0]])
	for name, mode := range up.Modes {
		require.Equal(t, fs.FileMode(0o600), mode, name)
	}
	override := up.Files[up.Project.Files[1]]
	for _, want := range []string{agentprotocol.LabelManaged, agentprotocol.LabelService, svcID, agentprotocol.LabelDeployment, depV1,
		agentprotocol.LabelComponent, agentprotocol.StackContainerName(svcID, "api"), agentprotocol.EnvNetwork(envID)} {
		require.Contains(t, override, want)
	}

	// Rotated: next -> current, never a secret on disk.
	require.NoDirExists(t, next)
	cur := s.stackDir(stackCurrentDir)
	for _, f := range []string{stackComposeFile, stackOverrideFile, stackMetaFile} {
		st, err := os.Stat(filepath.Join(cur, f))
		require.NoError(t, err)
		require.Equal(t, fs.FileMode(0o600), st.Mode().Perm(), f)
	}
	for _, d := range []string{filepath.Join(s.dir, "5e5e5e5e"), cur} {
		st, err := os.Stat(d)
		require.NoError(t, err)
		require.Equal(t, fs.FileMode(0o700), st.Mode().Perm(), d)
	}
	require.Equal(t, depV1, s.meta(stackCurrentDir).DeploymentID)
	require.NoError(t, filepath.WalkDir(s.dir, func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		if !d.IsDir() {
			b, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NotContains(t, string(b), stackSecret, path)
		}
		return nil
	}))
	// Nor in the logs, events or the result; the echoed value is masked.
	require.NotContains(t, s.snd.allLogText(), stackSecret)
	require.Contains(t, s.snd.allLogText(), redactMark)
	for _, e := range s.snd.events {
		require.NotContains(t, e.Message, stackSecret)
	}
	require.NotContains(t, string(res.Data), stackSecret)

	// Route: the stack's own route joins the others and was verified.
	table := s.px.Current()
	require.Len(t, table, 2)
	own, ok := stackRouteIn(table)
	require.True(t, ok)
	require.Equal(t, agentprotocol.StackRouteID(svcID, "api", 80), own.ServiceID)
	require.Equal(t, 1, s.px.nProbe)
	require.Equal(t, []string{"pull:started", "pull:succeeded", "create:started", "create:succeeded", "start:started", "start:succeeded",
		"health:started", "health:succeeded", "route:started", "route:succeeded", "verify:started", "verify:succeeded"}, s.snd.eventSteps())

	// Result: images per component, one observed replica per component.
	require.Equal(t, map[string]string{"api": "traefik/whoami@sha256:abc123", "worker": "busybox@sha256:abc123"}, dr.Stack.Images)
	require.Len(t, dr.Stack.Components, 2)
	require.Equal(t, "api", dr.Stack.Components[0].Component)
	require.Equal(t, 0, dr.Stack.Components[0].Index)
	require.Equal(t, "worker", dr.Stack.Components[1].Component)
	require.Equal(t, 1, dr.Stack.Components[1].Index)
	require.Equal(t, agentprotocol.StateRunning, dr.Stack.Components[0].State)
	require.Equal(t, dr.Stack.Components[0].ContainerID, dr.ContainerID)
	require.Equal(t, map[string]string{"api": "traefik/whoami@sha256:abc123", "worker": "busybox@sha256:abc123"}, s.meta(stackCurrentDir).Images)

	// The desired state records a stack, found by label.
	ent := s.eng.desiredSnapshot()[svcID]
	require.True(t, ent.Stack)
	require.Equal(t, depV1, ent.DeploymentID)
	require.Equal(t, 2, ent.Replicas)
	require.Empty(t, ent.ContainerNames)
	require.Len(t, ent.Routes, 1)
}

func TestStackRedeployRotatesFilesAndReplacesStackRoutes(t *testing.T) {
	s := newStackRig(t)
	res, dr := s.deployStack(s.payload(depV1))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	_, ok := stackRouteIn(s.px.Current())
	require.True(t, ok)

	// v2 drops the route: the table must not keep the stale one.
	p := s.payload(depV2)
	p.Stack.Routes = nil
	res, dr = s.deployStack(p)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	_, ok = stackRouteIn(s.px.Current())
	require.False(t, ok, "stale stack route removed")
	require.Equal(t, depV2, s.meta(stackCurrentDir).DeploymentID)
	require.Equal(t, depV1, s.meta(stackPreviousDir).DeploymentID)
	require.NoDirExists(t, s.stackDir(stackNextDir))
	require.Equal(t, map[string]bool{depV2: true}, s.liveDeployments())
}

func TestStackDeployUpFailureRollsBackToThePreviousFiles(t *testing.T) {
	s := newStackRig(t)
	_, dr := s.deployStack(s.payload(depV1))
	require.Equal(t, agentprotocol.OutcomeSucceeded, dr.Outcome, dr.Message)
	firstOverride, err := os.ReadFile(filepath.Join(s.stackDir(stackCurrentDir), stackOverrideFile))
	require.NoError(t, err)
	routesBefore := s.px.Current()

	s.upBehaviour = func(_ int, dep string) (string, error) {
		if dep == depV2 {
			return "api", errors.New("docker compose up: exit status 1: container nc-5e5e5e5e-api is unhealthy")
		}
		return "", nil
	}
	res, dr := s.deployStack(s.payload(depV2))
	require.Equal(t, agentprotocol.OutcomeRolledBack, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "unhealthy")
	require.Contains(t, dr.Message, "Rolled back to the previous release")
	require.Contains(t, dr.Message, "api | boom: cannot connect to the database", "last log lines of the unhealthy component")
	require.NotContains(t, dr.Message, "worker |", "healthy components are not tailed")

	// The restore re-applied current/ (the files of v1), with --wait.
	ups := s.cr.Of("up")
	require.Len(t, ups, 3)
	restore := ups[2]
	cur := s.stackDir(stackCurrentDir)
	require.Equal(t, []string{filepath.Join(cur, stackComposeFile), filepath.Join(cur, stackOverrideFile)}[0], restore.Project.Files[0])
	require.Equal(t, filepath.Join(cur, stackOverrideFile), restore.Project.Files[1])
	require.Equal(t, string(firstOverride), restore.Files[restore.Project.Files[1]])
	require.True(t, restore.Up.Wait)
	require.Contains(t, restore.Project.Files[2], stackPinFile, "the restore pins the images v1 ran")
	require.Contains(t, restore.Files[restore.Project.Files[2]], `image: "traefik/whoami@sha256:abc123"`)
	require.Empty(t, s.cr.Of("down"))

	// State: v1 is current, next/ is gone, v1's containers run, routes untouched.
	require.Equal(t, depV1, s.meta(stackCurrentDir).DeploymentID)
	require.NoDirExists(t, s.stackDir(stackNextDir))
	require.Equal(t, map[string]bool{depV1: true}, s.liveDeployments())
	require.Equal(t, routesBefore, s.px.Current())
	require.Equal(t, depV1, s.eng.desiredSnapshot()[svcID].DeploymentID)
	require.NotContains(t, dr.Message+s.snd.allLogText(), stackSecret)
}

func TestStackDeployRestoreFailureIsRollbackFailed(t *testing.T) {
	s := newStackRig(t)
	_, dr := s.deployStack(s.payload(depV1))
	require.Equal(t, agentprotocol.OutcomeSucceeded, dr.Outcome, dr.Message)

	s.upBehaviour = func(int, string) (string, error) { return "api", errors.New("exit status 1") }
	res, dr := s.deployStack(s.payload(depV2))
	require.Equal(t, agentprotocol.OutcomeRollbackFailed, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "Automatic rollback failed")
	require.Contains(t, dr.Message, "could not re-apply the previous release")
	require.Contains(t, dr.Message, "docker compose -p "+stackProject)
	require.Len(t, s.cr.Of("up"), 3)
}

func TestStackFirstDeployFailureRemovesTheContainersAndKeepsVolumes(t *testing.T) {
	s := newStackRig(t)
	s.upBehaviour = func(int, string) (string, error) { return "api", errors.New("exit status 1") }

	res, dr := s.deployStack(s.payload(depV1))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, dr.Message)
	require.Equal(t, []string{"version", "config", "pull", "up", "down"}, s.cr.Ops())
	down := s.cr.Of("down")[0]
	require.False(t, down.Down.RemoveVolumes, "volumes are kept")
	require.Equal(t, stackProject, down.Project.Name)
	require.Empty(t, down.Project.Files)
	require.Contains(t, dr.Message, "volumes were kept")
	require.Contains(t, dr.Message, "api | boom")
	require.NoDirExists(t, s.stackDir(stackNextDir))
	require.NoDirExists(t, s.stackDir(stackCurrentDir))
	_, desired := s.eng.desiredSnapshot()[svcID]
	require.False(t, desired)
}

func TestStackUnhealthyComponentAfterUpRollsBack(t *testing.T) {
	s := newStackRig(t)
	_, dr := s.deployStack(s.payload(depV1))
	require.Equal(t, agentprotocol.OutcomeSucceeded, dr.Outcome, dr.Message)

	// up --wait returned success but a component is not running.
	s.upBehaviour = func(_ int, dep string) (string, error) {
		if dep == depV2 {
			return "worker", nil
		}
		return "", nil
	}
	res, dr := s.deployStack(s.payload(depV2))
	require.Equal(t, agentprotocol.OutcomeRolledBack, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "worker: exited with code 1")
	require.Equal(t, map[string]bool{depV1: true}, s.liveDeployments())
	require.Contains(t, s.snd.eventSteps(), "health:failed")
}

func TestStackCompletedOneShotComponentIsHealthy(t *testing.T) {
	s := newStackRig(t)
	s.cr.UpFn = func(_ context.Context, c composetest.Call, _ compose.Output) error {
		dep := overrideDeployment.FindStringSubmatch(c.Files[c.Project.Files[1]])[1]
		s.spawn("api", 0, dep, "running", 0, 0)
		s.spawn("worker", 1, dep, "exited", 0, 0) // finished cleanly
		return nil
	}
	res, dr := s.deployStack(s.payload(depV1))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, agentprotocol.StateExited, dr.Stack.Components[1].State)
	require.Equal(t, agentprotocol.HealthHealthy, dr.Stack.Components[1].Health)
}

// ---- policy ----

func TestStackAllowListViolationFailsBeforeAnyUp(t *testing.T) {
	for name, api := range map[string]string{
		"privileged":    `{"image":"traefik/whoami:latest","privileged":true}`,
		"docker socket": `{"image":"traefik/whoami:latest","volumes":[{"type":"bind","source":"/var/run/docker.sock","target":"/var/run/docker.sock"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newStackRig(t)
			s.cr.ConfigJSON = `{"name":"nc-5e5e5e5e","services":{"api":` + api + `,"worker":{"image":"busybox:1"}}}`
			res, dr := s.deployStack(s.payload(depV1))
			require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
			require.Contains(t, dr.Message, "service api")
			require.Contains(t, dr.Message, name[:4])
			require.Equal(t, []string{"version", "config"}, s.cr.Ops(), "neither pull nor up ran")
			require.NoDirExists(t, s.stackDir(stackNextDir))
			require.Empty(t, s.dk.created)
			require.Contains(t, s.snd.eventSteps(), "pull:failed")
		})
	}
}

func TestStackUnrestrictedLiftsTheAllowListButNotBuild(t *testing.T) {
	s := newStackRig(t)
	s.cr.ConfigJSON = `{"name":"nc-5e5e5e5e","services":{"api":{"image":"traefik/whoami:latest","privileged":true},"worker":{"image":"busybox:1"}}}`
	p := s.payload(depV1)
	p.Stack.Unrestricted = true
	res, dr := s.deployStack(p)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	s = newStackRig(t)
	s.cr.ConfigJSON = `{"name":"nc-5e5e5e5e","services":{"api":{"image":"traefik/whoami:latest","build":{"context":"/tmp/x"}},"worker":{"image":"busybox:1"}}}`
	p = s.payload(depV1)
	p.Stack.Unrestricted = true
	res, dr = s.deployStack(p)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, dr.Message, "build")
	require.Equal(t, []string{"version", "config"}, s.cr.Ops())
}

func TestStackComponentMismatchIsRefused(t *testing.T) {
	s := newStackRig(t)
	p := s.payload(depV1)
	p.Stack.Components = append(p.Stack.Components, agentprotocol.StackComponent{Name: "cache", Index: 2})
	res, dr := s.deployStack(p)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, dr.Message, "the stack's services changed; redeploy")
	require.Contains(t, dr.Message, "Removed: cache")
	require.Equal(t, []string{"version", "config"}, s.cr.Ops())
}

func TestStackInvalidComposeFileFailsWithoutChangingAnything(t *testing.T) {
	s := newStackRig(t)
	s.cr.ConfigFn = func(_ context.Context, _ composetest.Call, out compose.Output) ([]byte, error) {
		out("required variable API_TOKEN is missing a value")
		return nil, errors.New("docker compose config: exit status 15: required variable API_TOKEN is missing a value")
	}
	res, dr := s.deployStack(s.payload(depV1))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, dr.Message, "The compose file is not valid")
	require.Contains(t, dr.Message, "required variable API_TOKEN")
	require.NoDirExists(t, s.stackDir(stackNextDir))
}

func TestStackWithoutComposeRunnerFails(t *testing.T) {
	const msg = "Docker Compose is not available on this server (needs the plugin, 2.24 or newer)"
	s := newStackRigWith(t, func(c *Config) { c.Compose = nil })
	res, dr := s.deployStack(s.payload(depV1))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Equal(t, msg, dr.Message)

	// A runner whose plugin is missing or too old fails the same way, before anything runs.
	for name, set := range map[string]func(*composetest.Fake){
		"missing":  func(f *composetest.Fake) { f.VersionErr = errors.New("docker: 'compose' is not a docker command") },
		"too old":  func(f *composetest.Fake) { f.VersionOut = "2.23.0" },
		"no stack": nil,
	} {
		t.Run(name, func(t *testing.T) {
			s := newStackRig(t)
			if set == nil {
				s = newStackRigWith(t, func(c *Config) { c.StackDir = "" })
			} else {
				set(s.cr)
			}
			res, dr := s.deployStack(s.payload(depV1))
			require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
			require.Equal(t, msg, dr.Message)
			require.NotContains(t, s.cr.Ops(), "config")
		})
	}
}

func TestStackPayloadValidation(t *testing.T) {
	s := newStackRig(t)
	for name, mutate := range map[string]func(*agentprotocol.DeployPayload){
		"no compose":   func(p *agentprotocol.DeployPayload) { p.Stack.Compose = " " },
		"image set":    func(p *agentprotocol.DeployPayload) { p.Image = &agentprotocol.ImageSource{Ref: "x"} },
		"bad name":     func(p *agentprotocol.DeployPayload) { p.Stack.Components[0].Name = "Bad Name" },
		"bad wait":     func(p *agentprotocol.DeployPayload) { p.Stack.WaitSeconds = 99999 },
		"bad service":  func(p *agentprotocol.DeployPayload) { p.ServiceID = "../../etc" },
		"route no id":  func(p *agentprotocol.DeployPayload) { p.Stack.Routes[0].ServiceID = "" },
		"no component": func(p *agentprotocol.DeployPayload) { p.Stack.Components = nil },
	} {
		p := s.payload(depV1)
		mutate(&p)
		res, _ := s.deploy(p, nil)
		require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, name)
		require.Contains(t, res.Error, "invalid deploy payload", name)
	}
	require.Empty(t, s.cr.Ops())
}

// ---- verify and routes ----

func TestStackVerifyFailureRestoresRoutesAndTheStack(t *testing.T) {
	s := newStackRig(t)
	_, dr := s.deployStack(s.payload(depV1))
	require.Equal(t, agentprotocol.OutcomeSucceeded, dr.Outcome, dr.Message)
	routesBefore := s.px.Current()

	s.px.mu.Lock()
	s.px.probes, s.px.nProbe = []int{502}, 0
	s.px.mu.Unlock()
	p := s.payload(depV2)
	p.Stack.Routes[0].Hosts = []string{"new." + stackHost}
	res, dr := s.deployStack(p)
	require.Equal(t, agentprotocol.OutcomeRolledBack, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "Verification through the proxy failed: HTTP 502")
	require.Equal(t, routesBefore, s.px.Current(), "the previous routes are back")
	require.Equal(t, map[string]bool{depV1: true}, s.liveDeployments())
	require.Len(t, s.cr.Of("up"), 3, "the previous release was re-applied")
	require.Equal(t, depV1, s.meta(stackCurrentDir).DeploymentID)
	require.Equal(t, depV1, s.eng.desiredSnapshot()[svcID].DeploymentID)
}

func TestStackFirstDeployVerifyFailureRemovesRoutesAndContainers(t *testing.T) {
	s := newStackRig(t)
	s.px.probes = []int{500}
	res, dr := s.deployStack(s.payload(depV1))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, dr.Message)
	_, ok := stackRouteIn(s.px.Current())
	require.False(t, ok, "no route to a stack that was taken down")
	require.Len(t, s.cr.Of("down"), 1)
	require.NoDirExists(t, s.stackDir(stackCurrentDir))
}

func TestStackWithoutRoutesSkipsVerification(t *testing.T) {
	s := newStackRig(t)
	p := s.payload(depV1)
	p.Stack.Routes = nil
	res, dr := s.deployStack(p)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Zero(t, s.px.nProbe)
}

func TestOwnRoutesIncludesStackRoutes(t *testing.T) {
	table := []agentprotocol.Route{
		{ServiceID: "other", Hosts: []string{"o.localhost"}, Upstreams: []string{"x:80"}},
		{ServiceID: "route-1", Stack: svcID, Hosts: []string{"a.localhost"}, Upstreams: []string{"nc-5e5e5e5e-api:80"}},
		{ServiceID: "route-2", Stack: svcID, Hosts: []string{"b.localhost"}, Upstreams: []string{"nc-5e5e5e5e-web:8080"}},
		{ServiceID: "route-3", Stack: "another-stack", Hosts: []string{"c.localhost"}, Upstreams: []string{"y:80"}},
		{ServiceID: "route-4", Stack: svcID, Protocol: agentprotocol.ProtoTCP, ListenPort: 5432, Upstreams: []string{"nc-5e5e5e5e-db:5432"}},
	}
	own := ownRoutes(table, svcID)
	require.Len(t, own, 2)
	require.Equal(t, "route-1", own[0].ServiceID)
	require.Equal(t, "route-2", own[1].ServiceID)
	require.Empty(t, ownRoutes(table, ""), "an empty id owns nothing")
}

func TestStackVerifyProbesTheFirstServingRoute(t *testing.T) {
	s := newStackRig(t)
	p := s.payload(depV1)
	redirectOnly := agentprotocol.Route{
		ServiceID: agentprotocol.StackRouteID(svcID, "worker", 9000), Stack: svcID, Hosts: []string{"redir.localhost"},
		Upstreams: []string{agentprotocol.StackContainerName(svcID, "worker") + ":9000"},
		RouteExt:  agentprotocol.RouteExt{Domains: []agentprotocol.DomainRule{{ID: "dddddddd-0000-4000-8000-000000000001", Host: "redir.localhost", Kind: agentprotocol.DomainRedirect}}},
	}
	p.Stack.Routes = append([]agentprotocol.Route{redirectOnly}, p.Stack.Routes...)
	var hosts []string
	px := &probeHostProxy{fakeProxy: s.px, hosts: &hosts}
	s.eng.cfg.Proxy = px
	res, dr := s.deployStack(p)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, []string{stackHost}, hosts)
}

type probeHostProxy struct {
	*fakeProxy
	hosts *[]string
}

func (p *probeHostProxy) Probe(ctx context.Context, host, path string) (int, error) {
	*p.hosts = append(*p.hosts, host)
	return p.fakeProxy.Probe(ctx, host, path)
}

// ---- cancel ----

func TestStackCancelDuringPullChangesNothing(t *testing.T) {
	s := newStackRig(t)
	cancelWhen(s.rig, s.hs, agentprotocol.StepPull)
	res, dr := s.deployStack(s.payload(depV2))
	require.Equal(t, agentprotocol.OutcomeCanceled, res.Outcome, dr.Message)
	require.Equal(t, agentprotocol.StepPull, dr.CanceledAt)
	require.Contains(t, dr.Message, "Canceled by alice during the image pull")
	require.Contains(t, dr.Message, "Nothing was changed")
	require.NotContains(t, s.cr.Ops(), "up")
	require.NotContains(t, s.cr.Ops(), "down")
	require.NoDirExists(t, s.stackDir(stackNextDir))
	require.Contains(t, s.snd.eventSteps(), "cancel:succeeded")
}

func TestStackCancelDuringUpRestoresThePreviousRelease(t *testing.T) {
	s := newStackRig(t)
	_, dr := s.deployStack(s.payload(depV1))
	require.Equal(t, agentprotocol.OutcomeSucceeded, dr.Outcome, dr.Message)
	routesBefore := s.px.Current()

	cancelWhen(s.rig, s.hs, agentprotocol.StepCreate)
	res, dr := s.deployStack(s.payload(depV2))
	require.Equal(t, agentprotocol.OutcomeCanceled, res.Outcome, dr.Message)
	require.Equal(t, agentprotocol.StepCreate, dr.CanceledAt)
	require.Contains(t, dr.Message, "The previous release was started again.")
	require.Equal(t, map[string]bool{depV1: true}, s.liveDeployments())
	require.Equal(t, depV1, s.meta(stackCurrentDir).DeploymentID)
	require.NoDirExists(t, s.stackDir(stackNextDir))
	require.Equal(t, routesBefore, s.px.Current())
	require.Empty(t, s.cr.Of("down"))
	require.Contains(t, s.snd.eventSteps(), "cancel:succeeded")
}

func TestStackCancelDuringFirstUpRemovesTheNewContainers(t *testing.T) {
	s := newStackRig(t)
	cancelWhen(s.rig, s.hs, agentprotocol.StepCreate)
	res, dr := s.deployStack(s.payload(depV2))
	require.Equal(t, agentprotocol.OutcomeCanceled, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "The new containers were removed")
	down := s.cr.Of("down")
	require.Len(t, down, 1)
	require.False(t, down[0].Down.RemoveVolumes)
}

func TestStackCancelDuringVerifyWithdrawsTheRoutes(t *testing.T) {
	s := newStackRig(t)
	_, dr := s.deployStack(s.payload(depV1))
	require.Equal(t, agentprotocol.OutcomeSucceeded, dr.Outcome, dr.Message)
	routesBefore := s.px.Current()

	cancelWhen(s.rig, s.hs, agentprotocol.StepVerify)
	p := s.payload(depV2)
	p.Stack.Routes[0].Hosts = []string{"new." + stackHost}
	res, dr := s.deployStack(p)
	require.Equal(t, agentprotocol.OutcomeCanceled, res.Outcome, dr.Message)
	require.Equal(t, agentprotocol.StepVerify, dr.CanceledAt)
	require.Contains(t, dr.Message, "The previous routes were restored.")
	require.Contains(t, dr.Message, "The previous release was started again.")
	require.Equal(t, routesBefore, s.px.Current())
	require.Equal(t, map[string]bool{depV1: true}, s.liveDeployments())
}

func TestStackCancelWithFailedRestoreIsRollbackFailed(t *testing.T) {
	s := newStackRig(t)
	_, dr := s.deployStack(s.payload(depV1))
	require.Equal(t, agentprotocol.OutcomeSucceeded, dr.Outcome, dr.Message)

	s.upBehaviour = func(n int, _ string) (string, error) {
		if n >= 3 {
			return "", errors.New("exit status 1") // the restore fails
		}
		return "", nil
	}
	cancelWhen(s.rig, s.hs, agentprotocol.StepHealth)
	res, dr := s.deployStack(s.payload(depV2))
	require.Equal(t, agentprotocol.OutcomeRollbackFailed, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "could not be restored")
	require.Contains(t, s.snd.eventSteps(), "cancel:failed")
}

func TestStackCancelAfterTheNewStackIsLiveIsTooLate(t *testing.T) {
	s := newStackRig(t)
	var answer agentprotocol.ResultFrame
	var once sync.Once
	// The image inspection that records the release runs after the point of no return.
	s.hd.onImageInspect = func() {
		once.Do(func() { answer = s.cancelDeployment(depV2, "alice") })
	}
	res, dr := s.deployStack(s.payload(depV2))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Contains(t, string(answer.Data), "too late")
	require.Equal(t, map[string]bool{depV2: true}, s.liveDeployments())
}

func TestStackAgentShutdownDuringUpLeavesStateForTheRedelivery(t *testing.T) {
	s := newStackRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.cr.UpFn = func(ctx context.Context, _ composetest.Call, _ compose.Output) error {
		cancel()
		return ctx.Err()
	}
	raw, err := json.Marshal(s.payload(depV1))
	require.NoError(t, err)
	res := s.eng.HandleCommand(ctx, agentprotocol.Command{
		ID: "cmd-1", Kind: agentprotocol.KindDeploy, Payload: raw, Secrets: map[string]string{"env:API_TOKEN": stackSecret},
	})
	var dr agentprotocol.DeployResult
	require.NoError(t, json.Unmarshal(res.Data, &dr))
	require.Equal(t, agentprotocol.OutcomeFailed, dr.Outcome)
	require.Equal(t, "agent stopped during deployment", dr.Message)
	require.Empty(t, s.cr.Of("down"), "nothing is torn down: the redelivered command continues")
}

// ---- actions ----

func (s *stackRig) action(kind string, p agentprotocol.ServiceActionPayload) agentprotocol.ResultFrame {
	s.t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(s.t, err)
	return s.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "act-1", Kind: kind, Payload: raw})
}

func TestStackRestartAndStopGoToComposeByProjectName(t *testing.T) {
	s := newStackRig(t)
	_, dr := s.deployStack(s.payload(depV1))
	require.Equal(t, agentprotocol.OutcomeSucceeded, dr.Outcome, dr.Message)

	res := s.action(agentprotocol.KindRestartService, agentprotocol.ServiceActionPayload{ServiceID: svcID, Stack: true, StopGraceSeconds: 7})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	restart := s.cr.Of("restart")
	require.Len(t, restart, 1)
	require.Equal(t, stackProject, restart[0].Project.Name)
	require.Empty(t, restart[0].Project.Files, "no files: compose finds the project by name")
	require.Equal(t, 7*time.Second, restart[0].Grace)

	res = s.action(agentprotocol.KindStopService, agentprotocol.ServiceActionPayload{ServiceID: svcID, Stack: true})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	stop := s.cr.Of("stop")
	require.Len(t, stop, 1)
	require.Equal(t, stackProject, stop[0].Project.Name)
	require.Equal(t, 30*time.Second, stop[0].Grace)
	// No container-level stop: compose did it.
	require.Empty(t, s.dk.stopped)
}

func TestStackRestartReportsACrashedComponent(t *testing.T) {
	s := newStackRig(t)
	s.spawn("api", 0, depV1, "running", 0, 0)
	s.spawn("worker", 1, depV1, "exited", 2, 0)
	res := s.action(agentprotocol.KindRestartService, agentprotocol.ServiceActionPayload{ServiceID: svcID, Stack: true})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "worker: exited with code 2")

	s.cr.RestartFn = func(context.Context, composetest.Call, compose.Output) error { return errors.New("exit status 1") }
	res = s.action(agentprotocol.KindRestartService, agentprotocol.ServiceActionPayload{ServiceID: svcID, Stack: true})
	require.Contains(t, res.Error, "restart the stack")
}

func TestStackRemoveDropsContainersRoutesFilesAndState(t *testing.T) {
	s := newStackRig(t)
	_, dr := s.deployStack(s.payload(depV1))
	require.Equal(t, agentprotocol.OutcomeSucceeded, dr.Outcome, dr.Message)
	_, ok := stackRouteIn(s.px.Current())
	require.True(t, ok)

	res := s.action(agentprotocol.KindRemoveService, agentprotocol.ServiceActionPayload{ServiceID: svcID, Stack: true, DeleteVolumes: true})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	down := s.cr.Of("down")
	require.Len(t, down, 1)
	require.True(t, down[0].Down.RemoveVolumes, "deleteVolumes -> down --volumes")
	require.Equal(t, stackProject, down[0].Project.Name)
	require.Empty(t, down[0].Project.Files)
	require.Empty(t, s.liveDeployments(), "leftover containers are removed too")
	table := s.px.Current()
	_, ok = stackRouteIn(table)
	require.False(t, ok)
	require.Len(t, table, 1, "other services' routes stay")
	require.NoDirExists(t, filepath.Join(s.dir, "5e5e5e5e"))
	_, desired := s.eng.desiredSnapshot()[svcID]
	require.False(t, desired)
}

func TestStackRemoveKeepsVolumesByDefaultAndKeepsFilesOnFailure(t *testing.T) {
	s := newStackRig(t)
	_, dr := s.deployStack(s.payload(depV1))
	require.Equal(t, agentprotocol.OutcomeSucceeded, dr.Outcome, dr.Message)
	s.cr.DownFn = func(context.Context, composetest.Call, compose.Output) error { return errors.New("exit status 1") }

	res := s.action(agentprotocol.KindRemoveService, agentprotocol.ServiceActionPayload{ServiceID: svcID, Stack: true})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "remove incomplete")
	require.False(t, s.cr.Of("down")[0].Down.RemoveVolumes)
	require.DirExists(t, s.stackDir(stackCurrentDir), "files stay so the removal can be retried")
}

func TestStackRemoveWithoutComposeRemovesContainersOnly(t *testing.T) {
	s := newStackRigWith(t, func(c *Config) { c.Compose = nil })
	s.spawn("api", 0, depV1, "running", 0, 0)
	res := s.action(agentprotocol.KindRemoveService, agentprotocol.ServiceActionPayload{ServiceID: svcID, Stack: true, DeleteVolumes: true})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "Docker Compose is not available")
	require.NotEmpty(t, s.liveDeployments(), "nothing was removed")

	res = s.action(agentprotocol.KindRemoveService, agentprotocol.ServiceActionPayload{ServiceID: svcID, Stack: true})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Empty(t, s.liveDeployments())
}

// ---- observed, stats, sweep, logs, terminal ----

func (s *stackRig) observed() agentprotocol.ObservedService {
	s.t.Helper()
	s.eng.reportObserved(context.Background())
	s.snd.mu.Lock()
	defer s.snd.mu.Unlock()
	require.NotEmpty(s.t, s.snd.observed)
	obs := s.snd.observed[len(s.snd.observed)-1]
	for _, o := range obs.Services {
		if o.ServiceID == svcID {
			return o
		}
	}
	s.t.Fatal("stack not observed")
	return agentprotocol.ObservedService{}
}

func (s *stackRig) desire(dep string, replicas int) {
	s.eng.mu.Lock()
	s.eng.desired[svcID] = desiredEntry{DeploymentID: dep, Replicas: replicas, Stack: true}
	s.eng.mu.Unlock()
}

func TestObserveStackReportsOneReplicaPerComponent(t *testing.T) {
	s := newStackRig(t)
	s.spawn("api", 0, depV1, "running", 0, 0)
	s.spawn("migrate", 1, depV1, "exited", 0, 0) // a one-shot that completed
	s.spawn("worker", 2, "d0d0d0d0-0000-4000-8000-000000000000", "running", 0, 0)
	s.desire(depV1, 2)

	o := s.observed()
	require.Equal(t, depV1, o.DeploymentID)
	require.Len(t, o.Replicas, 2, "components of other deployments are not observed")
	require.Equal(t, "api", o.Replicas[0].Component)
	require.Equal(t, 0, o.Replicas[0].Index)
	require.Equal(t, agentprotocol.StateRunning, o.Replicas[0].State)
	require.Equal(t, "migrate", o.Replicas[1].Component)
	require.Equal(t, 1, o.Replicas[1].Index)
	require.Equal(t, agentprotocol.StateExited, o.Replicas[1].State)
	require.Equal(t, agentprotocol.HealthHealthy, o.Replicas[1].Health, "exited 0 is completed, healthy")
	require.Equal(t, 2, o.ReplicasDesired)
	require.Equal(t, 2, o.ReplicasReady)
	require.Equal(t, agentprotocol.StateRunning, o.State)
	require.Equal(t, agentprotocol.HealthHealthy, o.Health)

	// A component that crashed makes the stack degraded.
	s.spawn("bad", 3, depV1, "exited", 3, 0)
	o = s.observed()
	require.Len(t, o.Replicas, 3)
	require.Equal(t, agentprotocol.HealthUnhealthy, o.Replicas[2].Health)
	require.Equal(t, agentprotocol.HealthDegraded, o.Health)
}

func TestObserveStackWithoutContainersIsMissing(t *testing.T) {
	s := newStackRig(t)
	s.desire(depV1, 2)
	o := s.observed()
	require.Equal(t, agentprotocol.StateMissing, o.State)
	require.Empty(t, o.Replicas)
}

func TestReportStatsGivesOneEntryPerComponentContainer(t *testing.T) {
	s := newStackRig(t)
	s.spawn("api", 0, depV1, "running", 0, 0)
	s.spawn("worker", 1, depV1, "running", 0, 0)
	s.spawn("migrate", 2, depV1, "exited", 0, 0) // not running: no sample
	s.desire(depV1, 3)

	s.eng.reportStats(context.Background())
	require.Len(t, s.snd.stats, 1)
	cs := s.snd.stats[0].Containers
	require.Len(t, cs, 2)
	require.Equal(t, svcID, cs[0].ServiceID)
	require.Equal(t, []int{0, 1}, []int{cs[0].Replica, cs[1].Replica}, "Replica is the component index")
}

func TestSweepLeavesStackContainersAlone(t *testing.T) {
	s := newStackRig(t)
	s.px.Apply(nil)
	// Old containers of the desired stack (one still labelled with an older deployment) and an orphan of another service.
	s.spawn("api", 0, depV1, "running", 0, 2*time.Hour)
	s.spawn("worker", 1, "d0d0d0d0-0000-4000-8000-000000000000", "running", 0, 2*time.Hour)
	s.dk.seed("nc-ghost-aaaaaaaa-0", "99999999-0000-4000-8000-000000000009", "dep-ghost", true, 2*time.Hour)

	err := s.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Services: []agentprotocol.DesiredService{{
		ServiceID: svcID, DeploymentID: depV1, Replicas: 2, Stack: true, Routes: stackRoutes(),
	}}})
	require.NoError(t, err)
	_, apiAlive := s.dk.state(agentprotocol.StackContainerName(svcID, "api"))
	_, workerAlive := s.dk.state(agentprotocol.StackContainerName(svcID, "worker"))
	_, ghostAlive := s.dk.state("nc-ghost-aaaaaaaa-0")
	require.True(t, apiAlive)
	require.True(t, workerAlive, "a component of a desired stack is never an orphan")
	require.False(t, ghostAlive, "the sweep still removes real orphans")

	// The desired stack's routes are applied as sent.
	own, ok := stackRouteIn(s.px.Current())
	require.True(t, ok)
	require.Equal(t, agentprotocol.StackRouteID(svcID, "api", 80), own.ServiceID)
	require.True(t, s.eng.desiredSnapshot()[svcID].Stack)
	require.Empty(t, s.cr.Ops(), "reconcile never runs compose")
}

func TestSweepRemovesContainersOfUndesiredStackButKeepsItsFiles(t *testing.T) {
	s := newStackRig(t)
	files := filepath.Join(s.dir, "5e5e5e5e", stackCurrentDir)
	require.NoError(t, os.MkdirAll(files, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(files, stackComposeFile), []byte(stackCompose), 0o600))
	s.spawn("api", 0, depV1, "running", 0, 2*time.Hour)

	require.NoError(t, s.eng.Reconcile(context.Background(), agentprotocol.Reconcile{}))
	require.Empty(t, s.liveDeployments(), "containers of a stack nobody wants are swept")
	require.FileExists(t, filepath.Join(files, stackComposeFile), "files are left alone")
}

func TestStackIsNotConverged(t *testing.T) {
	s := newStackRig(t)
	s.spawn("api", 0, depV1, "running", 0, 0)
	s.eng.startConvergence(context.Background(), map[string]desiredEntry{
		svcID: {DeploymentID: depV1, Replicas: 3, Stack: true, ContainerNames: []string{"nc-5e5e5e5e-api"}},
	})
	s.eng.bg.Wait()
	require.Empty(t, s.dk.created, "no replica is created for a stack")
}

func (s *stackRig) logTextOf(stream string) string {
	s.snd.mu.Lock()
	defer s.snd.mu.Unlock()
	var sb strings.Builder
	for _, f := range s.snd.logs {
		if f.StreamID == stream {
			for _, l := range f.Lines {
				sb.WriteString(l.Text + "\n")
			}
		}
	}
	return sb.String()
}

func (s *stackRig) subscribe(stream, component string) {
	s.t.Helper()
	raw, err := json.Marshal(agentprotocol.LogsSubscribe{StreamID: stream, ServiceID: svcID, Tail: 10, Component: component})
	require.NoError(s.t, err)
	res := s.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "log-" + stream, Kind: agentprotocol.KindLogsSubscribe, Payload: raw})
	require.Equal(s.t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
}

func TestStackLogsInterleaveComponentsWithPrefixAndFilter(t *testing.T) {
	s := newStackRig(t)
	s.spawn("api", 0, depV1, "running", 0, 0)
	s.spawn("worker", 1, depV1, "running", 0, 0)
	s.dk.mu.Lock()
	s.dk.logText[agentprotocol.StackContainerName(svcID, "api")] = "listening on :80"
	s.dk.logText[agentprotocol.StackContainerName(svcID, "worker")] = "queue ready"
	s.dk.mu.Unlock()

	s.subscribe("all", "")
	require.Eventually(t, func() bool {
		txt := s.logTextOf("all")
		return strings.Contains(txt, "api | listening on :80") && strings.Contains(txt, "worker | queue ready")
	}, 3*time.Second, 10*time.Millisecond)

	s.subscribe("one", "worker")
	require.Eventually(t, func() bool { return strings.Contains(s.logTextOf("one"), "worker | queue ready") }, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		s.snd.mu.Lock()
		defer s.snd.mu.Unlock()
		for _, e := range s.snd.logEnds {
			if e.StreamID == "one" {
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond)
	require.NotContains(t, s.logTextOf("one"), "api |")

	raw, _ := json.Marshal(agentprotocol.LogsSubscribe{StreamID: "none", ServiceID: svcID, Component: "nope"})
	res := s.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "log-none", Kind: agentprotocol.KindLogsSubscribe, Payload: raw})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, `no component "nope"`)
}

func TestStackReplicaIndexResolvesTheComponentContainer(t *testing.T) {
	s := newStackRig(t)
	s.spawn("api", 0, depV1, "running", 0, 0)
	s.spawn("worker", 1, depV1, "running", 0, 0)
	c, err := s.eng.replicaContainer(context.Background(), svcID, "", 1)
	require.NoError(t, err)
	require.Equal(t, agentprotocol.StackContainerName(svcID, "worker"), c.Name)
	c, err = s.eng.replicaContainer(context.Background(), svcID, "", 0)
	require.NoError(t, err)
	require.Equal(t, agentprotocol.StackContainerName(svcID, "api"), c.Name)
}

func TestStackAttachmentsPutTheSharedAliasSuffixOnSharedNetworks(t *testing.T) {
	plan := planOfDeploy(agentprotocol.DeployPayload{
		ServiceSlug: "stack", EnvironmentID: envID, EnvHasEgressBlocked: true,
		SharedNetworks: []agentprotocol.SharedNetworkAttachment{{Name: "nebulactrl-shared-aaaaaaaa", Alias: "stack.team"}},
	})
	require.Equal(t, []composestack.Attachment{
		{Name: agentprotocol.EnvNetwork(envID)}, {Name: agentprotocol.EnvInternalNetwork(envID)},
		{Name: "nebulactrl-shared-aaaaaaaa", AliasSuffix: "team"},
	}, stackAttachments(plan))
}

func TestStackRootStaysBelowTheStackDirectory(t *testing.T) {
	s := newStackRig(t)
	root, err := s.eng.stackRoot(svcID)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(s.dir, agentprotocol.Short(svcID)), root)

	for _, bad := range []string{"", "../../..", "../../../etc/passwd", "/etc/passwd-xx", "abcdefgh/../..", "short", "a b c d e f g h"} {
		_, err := s.eng.stackRoot(bad)
		require.Error(t, err, bad)
	}
	noDir := newStackRigWith(t, func(c *Config) { c.StackDir = "" })
	_, err = noDir.eng.stackRoot(svcID)
	require.Error(t, err)
}

func TestStackActionsRejectAServiceIDThatIsNotAPlainIdentifier(t *testing.T) {
	s := newStackRig(t)
	for _, kind := range []string{agentprotocol.KindRestartService, agentprotocol.KindStopService, agentprotocol.KindRemoveService} {
		res := s.action(kind, agentprotocol.ServiceActionPayload{ServiceID: "../../..", Stack: true})
		require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, kind)
		require.Contains(t, res.Error, "plain identifier", kind)
	}
	require.Empty(t, s.cr.Ops(), "compose was never reached")
}
