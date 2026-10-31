package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/compose"
	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/logs"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
	"github.com/nebulactrl/nebulactrl/pkg/composestack"
)

// Compose stacks (see pkg/agentprotocol/stack.go). A stack is one service whose
// containers `docker compose` creates from the user's compose file plus a
// generated override (pkg/composestack). The agent keeps the applied files per
// stack:
//
//	<StackDir>/<serviceId8>/current/   compose.yaml, override.yaml, meta.json (applied)
//	                       /next/      the files of the deploy in progress
//	                       /previous/  the files that were current before the last deploy
//
// Files are 0600 in 0700 directories. compose.yaml is the user's file verbatim
// (placeholders, never values): variables only reach compose in its
// environment. A deploy rotates next -> current -> previous only after the new
// stack is live and verified; a failed deploy re-applies current/ (the release
// that was running), or removes the new containers on a first deploy.

const (
	stackComposeFile  = "compose.yaml"
	stackOverrideFile = "override.yaml"
	stackMetaFile     = "meta.json"
	// stackPinFile pins components to the images of a release when its files
	// are re-applied (a tag may have moved since). It is scratch, never rotated.
	stackPinFile = "images.yaml"

	stackCurrentDir  = "current"
	stackNextDir     = "next"
	stackPreviousDir = "previous"

	// errComposeUnavailable is the failure of a stack deploy on a host without
	// a usable Docker Compose plugin.
	errComposeUnavailable = "Docker Compose is not available on this server (needs the plugin, 2.24 or newer)"

	// stackRestoreSlack is added to the wait timeout for a restore.
	stackRestoreSlack = 2 * time.Minute
)

var stackIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

// stackMeta is current/meta.json: what was applied.
type stackMeta struct {
	DeploymentID string            `json:"deploymentId"`
	ReleaseID    string            `json:"releaseId,omitempty"`
	Components   []string          `json:"components"`
	Images       map[string]string `json:"images,omitempty"`
	Unrestricted bool              `json:"unrestricted,omitempty"`
	AppliedAt    time.Time         `json:"appliedAt"`
}

// stackRun is one stack deploy.
type stackRun struct {
	d    *deploy
	spec *agentprotocol.StackSpec
	root string // <StackDir>/<serviceId8>
	proj compose.Project
	wait time.Duration

	started bool // docker compose up ran: containers may have changed
	hadPrev bool // a release was applied before (restore target exists)
	notes   []string
}

// stackRoot is the directory of a stack's files.
func (e *Engine) stackRoot(serviceID string) string {
	return filepath.Join(e.cfg.StackDir, agentprotocol.Short(serviceID))
}

func validateStackPayload(p agentprotocol.DeployPayload) error {
	s := p.Stack
	switch {
	case p.DeploymentID == "":
		return errors.New("deploymentId is required")
	case !stackIDPattern.MatchString(p.ServiceID):
		return errors.New("serviceId is required and must be a plain identifier")
	case p.EnvironmentID == "":
		return errors.New("environmentId is required")
	case p.Image != nil || p.Git != nil:
		return errors.New("a stack deploy has no image or git source")
	case strings.TrimSpace(s.Compose) == "":
		return errors.New("stack.compose is required")
	case len(s.Components) == 0:
		return errors.New("stack.components is required")
	case s.WaitSeconds < 0 || s.WaitSeconds > 3600:
		return fmt.Errorf("stack.waitSeconds %d is out of range (0..3600)", s.WaitSeconds)
	case len(s.Compose) > composestack.MaxComposeBytes:
		return errors.New("stack.compose is too large")
	}
	for _, c := range s.Components {
		if !agentprotocol.ComponentNamePattern.MatchString(c.Name) {
			return fmt.Errorf("invalid component name %q", c.Name)
		}
	}
	for i, r := range s.Routes {
		if r.ServiceID == "" {
			return fmt.Errorf("stack route %d has no id", i)
		}
	}
	return nil
}

// handleStackDeploy runs a deploy command whose payload carries a StackSpec.
func (e *Engine) handleStackDeploy(ctx context.Context, cmd agentprotocol.Command, p agentprotocol.DeployPayload) agentprotocol.ResultFrame {
	if err := validateStackPayload(p); err != nil {
		return failedResult(cmd.ID, "invalid deploy payload: "+err.Error())
	}
	// Registered before the service lock: a cancel can reach a deploy that
	// still waits for another operation on the service.
	ctx, ctl, release := e.cancel.start(ctx, p.DeploymentID)
	defer release()
	unlock := e.lockService(p.ServiceID)
	defer unlock()

	red := newRedactor(cmd.Secrets, secretKeysForRedaction(p))
	d := &deploy{e: e, cmd: cmd, p: p, red: red, ctl: ctl}
	d.dlog = logs.NewBatcher(agentprotocol.LogFrame{
		DeploymentID: p.DeploymentID, ServiceID: p.ServiceID, Stream: agentprotocol.StreamDeploy,
	}, e.cfg.Sender.Log, red.apply)
	defer d.dlog.Close()
	d.dbg = newDebugLog(p, e.cfg.Sender.Log, red.apply)
	defer d.dbg.Close()
	d.log = e.log.With("correlation_id", p.DeploymentID, "service_id", p.ServiceID)
	d.log.Info("stack deploy started", "components", len(p.Stack.Components))
	d.debugf("command %s: compose stack deploy of %s (release %s), %d component(s), wait %ds",
		cmd.ID, p.ServiceSlug, shortID(p.ReleaseID), len(p.Stack.Components), p.Stack.WaitSeconds)

	s := &stackRun{d: d, spec: p.Stack, wait: time.Duration(p.Stack.WaitSeconds) * time.Second}
	if s.wait <= 0 {
		s.wait = agentprotocol.DefaultStackWaitSeconds * time.Second
	}
	var res agentprotocol.DeployResult
	if canceled(ctx) {
		res = s.abortCanceled(ctx, &deployState{}) // canceled before it started
	} else {
		res = s.run(ctx)
	}
	d.log.Info("stack deploy finished", "outcome", res.Outcome)
	return deployResultFrame(cmd.ID, res)
}

func (s *stackRun) dir(name string) string { return filepath.Join(s.root, name) }

func (s *stackRun) files(dir string) []string {
	return []string{filepath.Join(dir, stackComposeFile), filepath.Join(dir, stackOverrideFile)}
}

// early ends the deploy before anything changed on the host.
func (s *stackRun) early(msg string) agentprotocol.DeployResult {
	_ = os.RemoveAll(s.dir(stackNextDir))
	return s.d.fail(msg)
}

// stopped reports whether ctx ended and, if so, the result: a person's cancel
// is unwound, anything else (the agent shutting down) leaves state for the
// redelivered command.
func (s *stackRun) stopped(ctx context.Context, st *deployState) (agentprotocol.DeployResult, bool) {
	if ctx.Err() == nil {
		return agentprotocol.DeployResult{}, false
	}
	if canceled(ctx) {
		return s.abortCanceled(ctx, st), true
	}
	return s.d.fail("agent stopped during deployment"), true
}

func (s *stackRun) run(ctx context.Context) agentprotocol.DeployResult {
	d, e := s.d, s.d.e
	p := d.p
	st := &deployState{recreate: true}

	if e.cfg.Compose == nil || e.cfg.StackDir == "" {
		return d.fail(errComposeUnavailable)
	}
	if v, err := e.cfg.Compose.Version(ctx); err != nil || !compose.AtLeastMin(v) {
		if res, done := s.stopped(ctx, st); done {
			return res
		}
		if err != nil {
			d.logf("docker compose version: %v", err)
		} else {
			d.logf("docker compose %s is older than %s", v, compose.MinVersion)
		}
		return d.fail(errComposeUnavailable)
	}
	s.root = e.stackRoot(p.ServiceID)

	env, err := d.buildEnv()
	if err != nil {
		return d.fail(err.Error())
	}
	s.proj = compose.Project{Name: agentprotocol.StackProject(p.ServiceID), Dir: s.root, Env: env}
	_, s.hadPrev = s.restoreDir()

	// 1. Validate and normalise the compose file, build the override, pull.
	d.event(agentprotocol.StepPull, agentprotocol.StepStarted, "Checking the compose file")
	next := s.dir(stackNextDir)
	if err := s.prepareNext(next); err != nil {
		d.event(agentprotocol.StepPull, agentprotocol.StepFailed, err.Error())
		return s.early(err.Error())
	}
	composePath := filepath.Join(next, stackComposeFile)
	s.proj.Files = []string{composePath}
	raw, err := e.cfg.Compose.Config(ctx, s.proj, d.logf0)
	if err != nil {
		if res, done := s.stopped(ctx, st); done {
			return res
		}
		msg := "The compose file is not valid: " + err.Error()
		d.event(agentprotocol.StepPull, agentprotocol.StepFailed, msg)
		return s.early(msg)
	}
	model, err := composestack.Parse(raw)
	if err != nil {
		msg := "The normalised compose model could not be read: " + err.Error()
		d.event(agentprotocol.StepPull, agentprotocol.StepFailed, msg)
		return s.early(msg)
	}
	if msg := componentMismatch(s.spec.Components, model.Components()); msg != "" {
		d.event(agentprotocol.StepPull, agentprotocol.StepFailed, msg)
		return s.early(msg)
	}
	if vs := composestack.Check(model, composestack.Policy{
		Unrestricted: s.spec.Unrestricted, Project: s.proj.Name,
	}); len(vs) > 0 {
		msg := composestack.Summary(vs)
		d.event(agentprotocol.StepPull, agentprotocol.StepFailed, msg)
		return s.early(msg)
	}
	plan := planOfDeploy(p)
	if ign := plan.ignoredShared(); len(ign) > 0 {
		d.logf("Warning: %d shared network(s) not joined: a service that blocks outbound internet stays on the internal network only", len(ign))
	}
	ov, err := composestack.BuildOverride(model, composestack.OverrideInput{
		ServiceID: p.ServiceID, DeploymentID: p.DeploymentID, ReleaseID: p.ReleaseID, EnvironmentID: p.EnvironmentID,
		Attachments: stackAttachments(plan), Isolate: p.BlockEgress, Resources: s.spec.Resources, Images: s.spec.Images,
	})
	if err != nil {
		msg := "Could not build the compose override: " + err.Error()
		d.event(agentprotocol.StepPull, agentprotocol.StepFailed, msg)
		return s.early(msg)
	}
	s.notes = ov.Notes
	for _, n := range ov.Notes {
		d.logf("Note: %s", n)
	}
	if err := writeStackFile(filepath.Join(next, stackOverrideFile), ov.YAML); err != nil {
		return s.early("write the override: " + err.Error())
	}
	if err := s.writeMeta(next, nil); err != nil {
		return s.early("write the stack metadata: " + err.Error())
	}
	s.proj.Files = s.files(next)
	if err := plan.ensure(ctx, e.ensureNetwork); err != nil {
		if res, done := s.stopped(ctx, st); done {
			return res
		}
		msg := "ensure networks: " + err.Error()
		d.event(agentprotocol.StepCreate, agentprotocol.StepFailed, msg)
		return s.early(msg)
	}
	if res, done := s.stopped(ctx, st); done {
		return res
	}
	if err := e.cfg.Compose.Pull(ctx, s.proj, d.logf0); err != nil {
		if res, done := s.stopped(ctx, st); done {
			return res
		}
		msg := "pull failed: " + err.Error()
		d.event(agentprotocol.StepPull, agentprotocol.StepFailed, msg)
		return s.early(msg)
	}
	d.event(agentprotocol.StepPull, agentprotocol.StepSucceeded, "Compose file checked, images pulled")
	if res, done := s.stopped(ctx, st); done {
		return res
	}

	// 2. docker compose up -d --wait.
	d.event(agentprotocol.StepCreate, agentprotocol.StepStarted, "Running docker compose up")
	s.started = true
	uctx, ucancel := context.WithTimeout(ctx, s.wait+5*time.Minute)
	err = e.cfg.Compose.Up(uctx, s.proj, compose.UpOptions{Wait: true, WaitTimeout: s.wait, RemoveOrphans: true}, d.logf0)
	ucancel()
	if err != nil {
		if res, done := s.stopped(ctx, st); done {
			return res
		}
		msg := "docker compose up failed: " + err.Error()
		d.event(agentprotocol.StepCreate, agentprotocol.StepFailed, msg)
		return s.abort(ctx, st, msg)
	}
	if res, done := s.stopped(ctx, st); done {
		return res // still in the create step
	}
	d.event(agentprotocol.StepCreate, agentprotocol.StepSucceeded, "")
	d.event(agentprotocol.StepStart, agentprotocol.StepStarted, "")
	d.event(agentprotocol.StepStart, agentprotocol.StepSucceeded, "")
	if res, done := s.stopped(ctx, st); done {
		return res
	}

	// 3. Every component runs (or completed).
	d.event(agentprotocol.StepHealth, agentprotocol.StepStarted, "Checking the components")
	conts, bad, err := s.components(ctx)
	if err != nil {
		if res, done := s.stopped(ctx, st); done {
			return res
		}
		msg := "list the stack's containers: " + err.Error()
		d.event(agentprotocol.StepHealth, agentprotocol.StepFailed, msg)
		return s.abort(ctx, st, msg)
	}
	if len(bad) > 0 {
		msg := "Components are not healthy: " + strings.Join(bad, "; ")
		d.event(agentprotocol.StepHealth, agentprotocol.StepFailed, msg)
		return s.abort(ctx, st, msg)
	}
	d.event(agentprotocol.StepHealth, agentprotocol.StepSucceeded, stackSummary(conts))
	if res, done := s.stopped(ctx, st); done {
		return res
	}

	// 4. Routes and verification. The end of verification is the point of no
	// return for a cancel (routePhase commits).
	if reason, ok := d.routePhase(ctx, st); !ok {
		if res, done := s.stopped(ctx, st); done {
			return res
		}
		return s.abort(ctx, st, reason)
	}

	// 5. The new stack is live: record it.
	images := s.resolveImages(ctx, conts)
	var warning string
	if err := s.writeMeta(next, images); err != nil {
		warning = "could not update the stack metadata: " + err.Error()
	} else if err := s.rotate(); err != nil {
		warning = "could not store the applied files: " + err.Error()
	}
	if warning != "" {
		d.logf("Warning: %s", warning)
	}
	s.record(conts)
	comps := observedComponents(conts)

	msg := fmt.Sprintf("Deployed stack %s: %d component(s) (%s)", s.proj.Name, len(comps), strings.Join(componentLabels(conts), ", "))
	if warning != "" {
		msg += ". Warning: " + warning
	}
	d.log.Info("stack deploy succeeded", "project", s.proj.Name, "components", len(comps))
	res := d.result(agentprotocol.OutcomeSucceeded, msg, firstContainerID(conts))
	res.Stack = &agentprotocol.StackResult{Images: images, Components: comps, Notes: s.notes}
	return res
}

// componentMismatch compares the control plane's components with the ones the
// agent derived from the normalised model.
func componentMismatch(want, got []agentprotocol.StackComponent) string {
	names := func(cs []agentprotocol.StackComponent) []string {
		out := make([]string, len(cs))
		for i, c := range cs {
			out[i] = c.Name
		}
		sort.Strings(out)
		return out
	}
	w, g := names(want), names(got)
	if strings.Join(w, "\x00") == strings.Join(g, "\x00") {
		return ""
	}
	in := func(list []string, n string) bool {
		for _, x := range list {
			if x == n {
				return true
			}
		}
		return false
	}
	var added, removed []string
	for _, n := range g {
		if !in(w, n) {
			added = append(added, n)
		}
	}
	for _, n := range w {
		if !in(g, n) {
			removed = append(removed, n)
		}
	}
	msg := "Deploy refused: the stack's services changed; redeploy."
	if len(added) > 0 {
		msg += " Added: " + strings.Join(added, ", ") + "."
	}
	if len(removed) > 0 {
		msg += " Removed: " + strings.Join(removed, ", ") + "."
	}
	return msg
}

// stackAttachments turns the service's network plan into override attachments:
// the first one is the primary network (the one Traefik routes on). A team
// shared network carries the component under "<component>.<suffix>", the suffix
// being the part of the plan's alias after its first dot.
func stackAttachments(plan netPlan) []composestack.Attachment {
	var out []composestack.Attachment
	for _, a := range plan.Attachments() {
		att := composestack.Attachment{Name: a.Network}
		if a.Network != plan.envNet() && a.Network != plan.intNet() && len(a.Aliases) > 0 {
			if _, after, ok := strings.Cut(a.Aliases[0], "."); ok {
				att.AliasSuffix = after
			}
		}
		out = append(out, att)
	}
	return out
}

// ---- files ----

func writeStackFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600) // the umask may have narrowed, never widened
}

// prepareNext clears next/ (left over from a dead attempt) and writes the
// user's compose file into it.
func (s *stackRun) prepareNext(next string) error {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return fmt.Errorf("create the stack directory: %w", err)
	}
	if err := os.Chmod(s.root, 0o700); err != nil {
		return err
	}
	if err := os.RemoveAll(next); err != nil {
		return fmt.Errorf("clear %s: %w", next, err)
	}
	if err := os.Mkdir(next, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", next, err)
	}
	return writeStackFile(filepath.Join(next, stackComposeFile), []byte(s.spec.Compose))
}

func (s *stackRun) writeMeta(dir string, images map[string]string) error {
	names := make([]string, len(s.spec.Components))
	for i, c := range s.spec.Components {
		names[i] = c.Name
	}
	data, err := json.MarshalIndent(stackMeta{
		DeploymentID: s.d.p.DeploymentID, ReleaseID: s.d.p.ReleaseID, Components: names, Images: images,
		Unrestricted: s.spec.Unrestricted, AppliedAt: time.Now().UTC(),
	}, "", "  ")
	if err != nil {
		return err
	}
	return writeStackFile(filepath.Join(dir, stackMetaFile), data)
}

// rotate makes next/ the current files and current/ the previous ones.
func (s *stackRun) rotate() error {
	next, cur, prev := s.dir(stackNextDir), s.dir(stackCurrentDir), s.dir(stackPreviousDir)
	if _, err := os.Stat(cur); err == nil {
		if err := os.RemoveAll(prev); err != nil {
			return err
		}
		if err := os.Rename(cur, prev); err != nil {
			return err
		}
	}
	return os.Rename(next, cur)
}

// restoreDir is the directory to re-apply when a deploy fails: the release
// that is running (current/), or previous/ when a crash left no current/.
func (s *stackRun) restoreDir() (string, bool) {
	for _, name := range []string{stackCurrentDir, stackPreviousDir} {
		dir := s.dir(name)
		if _, err := os.Stat(filepath.Join(dir, stackComposeFile)); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, stackOverrideFile)); err != nil {
			continue
		}
		return dir, true
	}
	return "", false
}

// pinFile writes the scratch override that pins the restore target's
// components to the images it ran with; "" when none is recorded.
func (s *stackRun) pinFile(src string) string {
	data, err := os.ReadFile(filepath.Join(src, stackMetaFile))
	if err != nil {
		return ""
	}
	var m stackMeta
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	names := make([]string, 0, len(m.Images))
	for name, ref := range m.Images {
		if strings.Contains(ref, "@sha256:") && agentprotocol.ComponentNamePattern.MatchString(name) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# Generated by NebulaCtrl: the images of the release being restored.\nservices:\n")
	for _, name := range names {
		b.WriteString("  " + name + ":\n    image: " + strconv.Quote(m.Images[name]) + "\n    pull_policy: missing\n")
	}
	path := filepath.Join(s.dir(stackNextDir), stackPinFile)
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		return ""
	}
	if writeStackFile(path, []byte(b.String())) != nil {
		return ""
	}
	return path
}

// ---- components ----

// stackContainers lists the containers of one deployment of the stack.
func (e *Engine) stackContainers(ctx context.Context, serviceID, deploymentID string) ([]docker.ContainerInfo, error) {
	all, err := e.containersOf(ctx, serviceID, true)
	if err != nil {
		return nil, err
	}
	var out []docker.ContainerInfo
	for _, c := range all {
		if c.Labels[agentprotocol.LabelComponent] != "" && (deploymentID == "" || c.Labels[agentprotocol.LabelDeployment] == deploymentID) {
			out = append(out, c)
		}
	}
	sortByReplica(out)
	return out, nil
}

// stackContainerOK judges one component container right after `up --wait`:
// running (and not unhealthy), or finished with exit code 0 ("completed").
func stackContainerOK(c docker.ContainerInfo) (bool, string) {
	switch {
	case c.Restarting:
		return false, "restarting"
	case c.Running && (c.Health == "" || c.Health == "healthy"):
		return true, ""
	case c.Running:
		return false, "health check " + c.Health
	case c.State == "exited" && c.ExitCode == 0:
		return true, ""
	case c.State == "exited":
		return false, fmt.Sprintf("exited with code %d", c.ExitCode)
	}
	return false, "state " + c.State
}

// components checks the containers of this deployment against the spec. bad
// lists "<component>: <what>" for every component that does not run.
func (s *stackRun) components(ctx context.Context) (conts []docker.ContainerInfo, bad []string, err error) {
	d := s.d
	conts, err = d.e.stackContainers(ctx, d.p.ServiceID, d.p.DeploymentID)
	if err != nil {
		return nil, nil, err
	}
	have := map[string]bool{}
	for _, c := range conts {
		name := c.Labels[agentprotocol.LabelComponent]
		have[name] = true
		if ok, why := stackContainerOK(c); !ok {
			bad = append(bad, name+": "+why)
		}
	}
	for _, c := range s.spec.Components {
		if !have[c.Name] && len(c.Profiles) == 0 {
			bad = append(bad, c.Name+": no container was created")
		}
	}
	return conts, bad, nil
}

func componentLabels(cs []docker.ContainerInfo) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Labels[agentprotocol.LabelComponent])
	}
	return out
}

func stackSummary(cs []docker.ContainerInfo) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		state := "running"
		if !c.Running {
			state = "completed"
		}
		parts = append(parts, c.Labels[agentprotocol.LabelComponent]+" "+state)
	}
	return strings.Join(parts, ", ")
}

func firstContainerID(cs []docker.ContainerInfo) string {
	if len(cs) == 0 {
		return ""
	}
	return cs[0].ID
}

// observedComponents maps component containers to observed replicas.
func observedComponents(cs []docker.ContainerInfo) []agentprotocol.ObservedReplica {
	out := make([]agentprotocol.ObservedReplica, 0, len(cs))
	for _, c := range cs {
		out = append(out, replicaObserved(replicaIndex(c), c))
	}
	return out
}

// resolveImages names the image each component runs as repo@sha256:digest (the
// local image ID when it has no repo digest). A component pinned by the
// payload keeps its pin.
func (s *stackRun) resolveImages(ctx context.Context, cs []docker.ContainerInfo) map[string]string {
	eng := s.d.e.cfg.Docker
	out := map[string]string{}
	for _, c := range cs {
		name := c.Labels[agentprotocol.LabelComponent]
		if pin := s.spec.Images[name]; pin != "" {
			out[name] = pin
			continue
		}
		info, err := eng.ImageInspect(ctx, c.ImageID)
		if err != nil {
			if info, err = eng.ImageInspect(ctx, c.Image); err != nil {
				s.d.logf("Warning: inspect image of %s: %v", name, err)
				if c.ImageID != "" {
					out[name] = c.ImageID
				}
				continue
			}
		}
		out[name] = pickDigest(c.Image, info)
	}
	return out
}

// record remembers the live stack as the desired state of the service.
func (s *stackRun) record(cs []docker.ContainerInfo) {
	d := s.d
	ent := desiredEntry{
		DeploymentID: d.p.DeploymentID, Replicas: len(cs), Stack: true,
		Routes: ownRoutes(d.e.cfg.Proxy.Current(), d.p.ServiceID),
	}
	d.e.mu.Lock()
	d.e.local[d.p.ServiceID] = ent
	d.e.desired[d.p.ServiceID] = ent
	d.e.mu.Unlock()
}

// ---- failure handling ----

// failureTail collects the last lines of the components that are not healthy
// (every component when none stands out), each line prefixed with its component.
func (s *stackRun) failureTail(ctx context.Context) []string {
	d := s.d
	conts, err := d.e.stackContainers(ctx, d.p.ServiceID, d.p.DeploymentID)
	if err != nil || len(conts) == 0 {
		return nil
	}
	var pick []docker.ContainerInfo
	for _, c := range conts {
		if ok, _ := stackContainerOK(c); !ok {
			pick = append(pick, c)
		}
	}
	if len(pick) == 0 {
		pick = conts
	}
	var out []string
	for _, c := range pick {
		lines, _ := logs.Tail(ctx, d.e.cfg.Docker, c.ID, failureLogLines)
		for _, l := range lines {
			out = append(out, c.Labels[agentprotocol.LabelComponent]+" | "+l)
		}
	}
	return out
}

// undo reverts the host to what ran before this deploy: it re-applies the
// release that was current, or - for a first deploy - removes the new
// containers (volumes stay). what says what it did.
func (s *stackRun) undo(ctx context.Context) (what string, err error) {
	d, e := s.d, s.d.e
	if src, ok := s.restoreDir(); ok {
		d.logf("Re-applying the previous release")
		proj := s.proj
		proj.Files = s.files(src)
		if pin := s.pinFile(src); pin != "" {
			proj.Files = append(proj.Files, pin)
		}
		if err := e.cfg.Compose.Up(ctx, proj, compose.UpOptions{Wait: true, WaitTimeout: s.wait, RemoveOrphans: true}, d.logf0); err != nil {
			return "", fmt.Errorf("could not re-apply the previous release: %w", err)
		}
		return "The previous release was started again.", nil
	}
	d.logf("First deploy of the stack: removing the new containers (volumes are kept)")
	if err := e.cfg.Compose.Down(ctx, compose.Project{Name: s.proj.Name, Dir: s.proj.Dir}, compose.DownOptions{}, d.logf0); err != nil {
		return "", fmt.Errorf("could not remove the new containers: %w", err)
	}
	return "The new containers were removed; volumes were kept.", nil
}

// abort undoes a failed deploy (the context is still live) and derives the
// outcome: rolled_back, rollback_failed, or failed.
func (s *stackRun) abort(ctx context.Context, st *deployState, reason string) agentprotocol.DeployResult {
	d := s.d
	cctx, cancel := detached(ctx, s.wait+stackRestoreSlack+time.Minute)
	defer cancel()
	tail := s.failureTail(cctx)

	var problems []string
	restoreFailed := st.routeErr != nil
	if st.routeErr != nil {
		problems = append(problems, "could not restore previous routes: "+st.routeErr.Error())
	}
	var what string
	if st.routeErr == nil {
		var err error
		if what, err = s.undo(cctx); err != nil {
			restoreFailed = true
			problems = append(problems, err.Error())
		}
	}
	_ = os.RemoveAll(s.dir(stackNextDir))

	outcome := agentprotocol.OutcomeFailed
	var msg strings.Builder
	msg.WriteString(reason)
	switch {
	case restoreFailed:
		outcome = agentprotocol.OutcomeRollbackFailed
		msg.WriteString("\nAutomatic rollback failed: " + strings.Join(problems, "; ") +
			". Manual recovery is required: check `docker compose -p " + s.proj.Name + " ps` and redeploy.")
	case s.hadPrev:
		outcome = agentprotocol.OutcomeRolledBack
		msg.WriteString("\nRolled back to the previous release.")
	default:
		msg.WriteString("\n" + what)
	}
	if len(tail) > 0 {
		d.logf("--- last container log lines ---")
		for _, l := range tail {
			d.logf("%s", l)
		}
		msg.WriteString(fmt.Sprintf("\n\nLast container log lines:\n%s", strings.Join(tail, "\n")))
	}
	d.log.Warn("stack deploy failed", "outcome", outcome, "reason", d.red.apply(reason))
	return d.result(outcome, msg.String(), "")
}

// abortCanceled unwinds a deploy a person canceled and reports `canceled`; when
// the previous release or its routes cannot be restored the outcome is
// rollback_failed, never canceled.
func (s *stackRun) abortCanceled(ctx context.Context, st *deployState) agentprotocol.DeployResult {
	d := s.d
	cctx, cancel := detached(ctx, s.wait+stackRestoreSlack+time.Minute)
	defer cancel()

	step := d.ctl.stepName()
	by := d.ctl.canceledBy()
	if by == "" {
		by = "a user"
	}
	d.logf("Canceled by %s during %s; undoing what was done", by, stepLabel(step))

	var problems []string
	if st.routeErr != nil {
		problems = append(problems, "could not restore previous routes: "+st.routeErr.Error())
	}
	restoreFailed := st.routeErr != nil
	var what string
	if st.routeErr == nil && s.started {
		var err error
		if what, err = s.undo(cctx); err != nil {
			restoreFailed = true
			problems = append(problems, err.Error())
		}
	}
	if s.root != "" {
		_ = os.RemoveAll(s.dir(stackNextDir))
	}

	head := "Canceled by " + by + " during " + stepLabel(step) + "."
	outcome := agentprotocol.OutcomeCanceled
	var msg strings.Builder
	if restoreFailed {
		outcome = agentprotocol.OutcomeRollbackFailed
		msg.WriteString(strings.TrimSuffix(head, ".") + ", but the previous release could not be restored: " + strings.Join(problems, "; ") +
			". Manual recovery is required: check `docker compose -p " + agentprotocol.StackProject(d.p.ServiceID) + " ps` and redeploy.")
	} else {
		msg.WriteString(head)
		if st.routesRestored {
			msg.WriteString(" The previous routes were restored.")
		}
		if what != "" {
			msg.WriteString(" " + what)
		} else if !st.routesRestored {
			msg.WriteString(" Nothing was changed.")
		}
	}
	status := agentprotocol.StepSucceeded
	if outcome != agentprotocol.OutcomeCanceled {
		status = agentprotocol.StepFailed
	}
	d.event(agentprotocol.StepCancel, status, msg.String())
	d.log.Info("stack deploy canceled", "outcome", outcome, "step", step)
	res := d.result(outcome, msg.String(), "")
	res.CanceledAt = step
	return res
}

// stackRouteTable merges the other services' routes with the stack's own: the
// live table (the proxy's current state under the route lock) minus every
// route the stack owned, plus Stack.Routes. The payload's Routes only seed a
// proxy that has no state yet (a fresh agent).
func (d *deploy) stackRouteTable(live []agentprotocol.Route) []agentprotocol.Route {
	base := live
	if len(base) == 0 {
		base = d.p.Routes
	}
	table := make([]agentprotocol.Route, 0, len(base)+len(d.p.Stack.Routes))
	for _, r := range base {
		if r.ServiceID != d.p.ServiceID && r.Stack != d.p.ServiceID {
			table = append(table, r)
		}
	}
	for _, r := range d.p.Stack.Routes {
		r.Stack = d.p.ServiceID
		table = append(table, r)
	}
	return table
}

// observeStack reports a stack: one replica per component container of the
// desired deployment (a component that finished with exit code 0 is healthy,
// "completed"), aggregated like the replicas of any service.
func (e *Engine) observeStack(ctx context.Context, o agentprotocol.ObservedService, serviceID string, ent desiredEntry) agentprotocol.ObservedService {
	conts, err := e.stackContainers(ctx, serviceID, ent.DeploymentID)
	if err != nil {
		e.log.Warn("observe: list stack containers", "service_id", serviceID, "err", err)
		return o
	}
	if len(conts) == 0 {
		return o
	}
	first := conts[0]
	for _, c := range conts[1:] {
		if c.Running && !first.Running {
			first = c
		}
	}
	o.ImageID = first.ImageID
	aggregateObserved(&o, observedComponents(conts))
	return o
}
