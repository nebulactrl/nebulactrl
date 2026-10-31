package composites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	pathpkg "path"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretgen"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
	"github.com/nebulactrl/nebulactrl/internal/variables"
)

// StageChangesInput is a sparse patch of one service. Everything staged takes
// effect only when apply_changes deploys it.
type StageChangesInput struct {
	Service   string            `json:"service" jsonschema:"service selector: id, project/env/service, hostname or a unique slug"`
	IfMatch   *int64            `json:"ifMatch,omitempty" jsonschema:"the service generation you last saw (from review_changes or a previous stage_changes); a different current generation fails with generation_conflict so you never overwrite someone else's staged edit"`
	Config    *StageConfigPatch `json:"config,omitempty" jsonschema:"sparse config patch: only the fields you give change; unknown fields are rejected"`
	Variables []StageVariable   `json:"variables,omitempty" jsonschema:"variables to set or delete, by key"`
	Domains   *StageDomains     `json:"domains,omitempty"`
	Volumes   *StageVolumes     `json:"volumes,omitempty"`
	Detail    string            `json:"detail,omitempty" jsonschema:"concise (default) or full"`
}

// StageConfigPatch lists the config fields stage_changes can patch. A field
// that is absent stays as it is; an empty string, 0 or [] clears it.
type StageConfigPatch struct {
	Name                     *string           `json:"name,omitempty" jsonschema:"display name of the service"`
	ServerID                 *string           `json:"serverId,omitempty" jsonschema:"move the service to this server (uuid)"`
	Image                    *string           `json:"image,omitempty" jsonschema:"container image of an image service, e.g. ghcr.io/acme/app:1.4"`
	GitURL                   *string           `json:"gitUrl,omitempty"`
	GitBranch                *string           `json:"gitBranch,omitempty"`
	DockerfilePath           *string           `json:"dockerfilePath,omitempty"`
	BuildContext             *string           `json:"buildContext,omitempty"`
	BuildType                *string           `json:"buildType,omitempty" jsonschema:"auto, dockerfile, railpack or starter:<node|python|go|ruby|php|static>"`
	Submodules               *string           `json:"submodules,omitempty" jsonschema:"git submodules: auto (default), on or off"`
	BuildLocation            *string           `json:"buildLocation,omitempty" jsonschema:"inherit, target or build_server"`
	Port                     *int              `json:"port,omitempty" jsonschema:"HTTP port the container listens on; 0 = no HTTP"`
	StartCommand             *[]string         `json:"startCommand,omitempty" jsonschema:"argv, no shell"`
	Healthcheck              *StageHealthcheck `json:"healthcheck,omitempty"`
	StopGraceSeconds         *int              `json:"stopGraceSeconds,omitempty"`
	MemoryMB                 *int              `json:"memoryMb,omitempty"`
	CPUs                     *float64          `json:"cpus,omitempty"`
	ImageRetention           *int              `json:"imageRetention,omitempty"`
	OneShot                  *bool             `json:"oneshot,omitempty"`
	PreDeployCommand         *[]string         `json:"preDeployCommand,omitempty" jsonschema:"argv run before the new release starts (migrations)"`
	PreDeployTimeoutSeconds  *int              `json:"preDeployTimeoutSeconds,omitempty"`
	PostDeployCommand        *[]string         `json:"postDeployCommand,omitempty"`
	PostDeployTimeoutSeconds *int              `json:"postDeployTimeoutSeconds,omitempty"`
	BlockEgress              *bool             `json:"blockEgress,omitempty" jsonschema:"block outbound internet"`
	ProcessName              *string           `json:"processName,omitempty" jsonschema:"name of the default process (the service's own port, startCommand and healthcheck); default web. Only meaningful with processes"`
	Singleton                *bool             `json:"singleton,omitempty" jsonschema:"the default process never runs two instances at once (stop old, then start new)"`
	Processes                *[]StageProcess   `json:"processes,omitempty" jsonschema:"the additional named processes of the service, all running the same image with the same variables and volumes (name, startCommand, port, healthcheck, stopGraceSeconds, singleton, memoryMb, cpus). Replaces the whole list: send every process to keep; [] removes them all"`
}

// StageProcess is one additional process of a service, as the REST API takes it.
type StageProcess = services.ProcessInput

// StageHealthcheck patches the health check; absent fields stay.
type StageHealthcheck struct {
	Type               *string   `json:"type,omitempty" jsonschema:"http, tcp, docker, exec or none"`
	Path               *string   `json:"path,omitempty"`
	Port               *int      `json:"port,omitempty"`
	IntervalSeconds    *int      `json:"intervalSeconds,omitempty"`
	TimeoutSeconds     *int      `json:"timeoutSeconds,omitempty"`
	StartPeriodSeconds *int      `json:"startPeriodSeconds,omitempty"`
	DeadlineSeconds    *int      `json:"deadlineSeconds,omitempty"`
	SuccessThreshold   *int      `json:"successThreshold,omitempty"`
	Command            *[]string `json:"command,omitempty"`
}

// StageVariable sets or deletes one variable. Values are never returned.
type StageVariable struct {
	Key    string  `json:"key" jsonschema:"variable name (letters, digits, underscore; PORT and NEBULA_* are reserved)"`
	Value  *string `json:"value,omitempty" jsonschema:"the value; may reference others with ${{service.KEY}}. Omit to keep the stored value"`
	Secret *bool   `json:"secret,omitempty" jsonschema:"mark as secret: the value is write-only and masked everywhere"`
	// Generate makes the control plane create a random secret value once.
	Generate *StageGenerate `json:"generate,omitempty" jsonschema:"generate a random secret instead of giving a value (also marks it secret)"`
	Scope    *string        `json:"scope,omitempty" jsonschema:"runtime (default), build or both (build and both need a git service)"`
	Delete   bool           `json:"delete,omitempty" jsonschema:"delete the variable (no other field allowed)"`
}

// StageGenerate describes a generated secret.
type StageGenerate struct {
	Length  int    `json:"length,omitempty" jsonschema:"1..1024, default 32"`
	Charset string `json:"charset,omitempty" jsonschema:"alnum (default), hex, base64, base64url, digits or lower"`
}

// StageDomains edits hostnames.
type StageDomains struct {
	Add      []StageDomainAdd `json:"add,omitempty"`
	Generate bool             `json:"generate,omitempty" jsonschema:"attach a generated hostname on the server's base domain (skipped when the service already has one)"`
	Remove   []string         `json:"remove,omitempty" jsonschema:"hostnames to detach"`
}

// StageDomainAdd attaches one hostname.
type StageDomainAdd struct {
	Hostname   string `json:"hostname"`
	TargetPort int    `json:"targetPort,omitempty" jsonschema:"container port to route to; default is the port of the process the domain routes to (the service port without processes)"`
	Process    string `json:"process,omitempty" jsonschema:"services with several processes only: the process the domain routes to; omit it when only one process has an HTTP port"`
}

// StageVolumes edits volumes.
type StageVolumes struct {
	Add    []StageVolumeAdd `json:"add,omitempty"`
	Remove []string         `json:"remove,omitempty" jsonschema:"names of volumes to detach (the data stays on the host)"`
}

// StageVolumeAdd attaches one named volume.
type StageVolumeAdd struct {
	Name      string `json:"name" jsonschema:"lowercase letters, digits, '.', '_' and '-' (max 63)"`
	MountPath string `json:"mountPath" jsonschema:"absolute path in the container"`
}

// StageStep is the outcome of one piece of a stage_changes call.
type StageStep struct {
	Step    string `json:"step" jsonschema:"e.g. config, variables.set, variables.delete:KEY, domains.add:HOST, volumes.add:NAME"`
	Status  string `json:"status" jsonschema:"staged, unchanged (already in place), failed or not_attempted"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// StageChangesOutput is the result of stage_changes.
type StageChangesOutput struct {
	Envelope
	Service   string `json:"service"`
	ServiceID string `json:"serviceId"`
	// Staged is true when every requested piece is staged (or already was).
	// Nothing is deployed: use review_changes, then apply_changes.
	Staged            bool          `json:"staged"`
	Generation        int64         `json:"generation" jsonschema:"pass as ifMatch to the next stage_changes and as expectedGeneration to apply_changes"`
	AppliedGeneration int64         `json:"appliedGeneration"`
	Production        bool          `json:"production"`
	Steps             []StageStep   `json:"steps"`
	Failed            []StageStep   `json:"failed" jsonschema:"steps that failed or were not attempted; earlier steps stay staged (no rollback)"`
	Changes           []ChangeEntry `json:"changes" jsonschema:"the pending field changes the touched paths now have against the running release (secrets masked, variable values never shown)"`
	ChangesTruncated  bool          `json:"changesTruncated,omitempty"`
	PendingCount      int           `json:"pendingCount" jsonschema:"all pending field changes of the service, not only the touched ones"`
	Next              string        `json:"next"`
}

var (
	stageVolumeNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)
	stageMaxVariables = 100
	stageMaxDomains   = 20
	stageMaxVolumes   = 20
	stageMaxValueLen  = 64 * 1024
)

func (c *composites) stageChanges() mcpkit.Tool {
	return mcpkit.Tool{
		Name:  "stage_changes",
		Title: "Stage changes to a service",
		Description: "Edit one service in one call: a sparse config patch, variables (set / generate a random secret / delete), domains (add, generate, remove) and volumes (add, remove). " +
			"Everything is only STAGED (the service's generation goes up); nothing is deployed and nothing running changes. " +
			"All input is validated before the first write; if a later step still fails, the result lists what was staged and what was not (steps, failed) because there is no rollback. " +
			"Pass ifMatch (the generation you last saw) to refuse the edit when someone else changed the service meanwhile (generation_conflict). " +
			"Variable values are never returned or logged: only keys, and secrets are masked. Use `generate` for passwords and keys instead of inventing values. " +
			"Repeating the same call is safe. Next: review_changes to see the plan, then apply_changes with the generation returned here. " +
			"This tool does not deploy, restart or apply anything.",
		Toolset: mcpkit.ToolsetCore,
		Scopes:  []string{policy.ScopeProjectsWrite},
		Hints:   mcpkit.Hints{ReadOnly: false, Destructive: false, Idempotent: true, OpenWorld: false},
		Input:   StageChangesInput{},
		Output:  StageChangesOutput{},
		Call: func(ctx context.Context, p *auth.Principal, raw json.RawMessage) (any, error) {
			in, err := mcpkit.Decode[StageChangesInput](raw)
			if err != nil {
				return nil, err
			}
			return c.stage(ctx, in)
		},
	}
}

// stageOp is one write of a stage_changes call.
type stageOp struct {
	step string
	run  func(ctx context.Context) (message string, err error)
}

// stagePlan is the validated request.
type stagePlan struct {
	sc      services.Scope
	steps   []StageStep // unchanged pieces, decided while validating
	ops     []stageOp
	touched []string
	prefix  []string // touched with plain prefix matching (domains)
}

func (c *composites) stage(ctx context.Context, in StageChangesInput) (StageChangesOutput, error) {
	detail, err := mcpkit.ParseDetail(in.Detail)
	if err != nil {
		return StageChangesOutput{}, err
	}
	if strings.TrimSpace(in.Service) == "" {
		return StageChangesOutput{}, invalid("service is required")
	}
	if in.Config == nil && len(in.Variables) == 0 && in.Domains == nil && in.Volumes == nil {
		return StageChangesOutput{}, invalid("nothing to stage: give config, variables, domains or volumes")
	}
	sv, err := c.Resolver.Service(ctx, in.Service)
	if err != nil {
		return StageChangesOutput{}, err
	}
	pl, err := c.planStage(ctx, sv.ID, in)
	if err != nil {
		return StageChangesOutput{}, changesErr(err)
	}

	out := StageChangesOutput{
		Service: selectorOf(pl.sc), ServiceID: sv.ID.String(), Production: pl.sc.IsProduction,
		Steps: append([]StageStep{}, pl.steps...), Failed: []StageStep{}, Changes: []ChangeEntry{},
	}
	staged := 0
	var firstErr error
	for i, op := range pl.ops {
		msg, err := op.run(ctx)
		if err != nil {
			if isApproval(err) {
				return out, err
			}
			if staged == 0 {
				return out, changesErr(err)
			}
			se := sectionError(op.step, err)
			failed := StageStep{Step: op.step, Status: "failed", Code: se.Code, Message: se.Message}
			out.Steps = append(out.Steps, failed)
			out.Failed = append(out.Failed, failed)
			for _, rest := range pl.ops[i+1:] {
				ns := StageStep{Step: rest.step, Status: "not_attempted"}
				out.Steps = append(out.Steps, ns)
				out.Failed = append(out.Failed, ns)
			}
			firstErr = err
			break
		}
		staged++
		out.Steps = append(out.Steps, StageStep{Step: op.step, Status: "staged", Message: msg})
	}
	out.Staged = firstErr == nil
	if firstErr != nil {
		out.Errors = []SectionError{sectionError(out.Failed[0].Step, firstErr)}
	} else {
		out.Errors = []SectionError{}
	}

	// Current state: generation, pending count and the touched changes.
	sc, err := c.scope(ctx, sv.ID)
	if err != nil {
		return out, err
	}
	out.Generation, out.AppliedGeneration = sc.Service.Generation, sc.Service.AppliedGeneration
	limit, valClip := 50, reviewValueClipConcise
	if detail.Full() {
		limit, valClip = reviewChangesFull, reviewValueClipFull
	}
	changes, err := c.Deployments.PendingChanges(ctx, sv.ID)
	if err != nil {
		out.Errors = append(out.Errors, sectionError("changes", err))
	}
	out.PendingCount = len(changes)
	for _, fc := range changes {
		if !pl.touches(fc.Field) {
			continue
		}
		if len(out.Changes) == limit {
			out.ChangesTruncated = true
			break
		}
		out.Changes = append(out.Changes, changeEntryOf(fc, valClip))
	}
	c.stageSummary(&out, staged, len(pl.steps))
	return out, nil
}

func (pl *stagePlan) touches(field string) bool {
	for _, t := range pl.touched {
		if field == t || strings.HasPrefix(field, t+".") {
			return true
		}
	}
	for _, t := range pl.prefix {
		if strings.HasPrefix(field, t) {
			return true
		}
	}
	return false
}

func (c *composites) stageSummary(out *StageChangesOutput, staged, unchanged int) {
	var sum string
	switch {
	case !out.Staged:
		sum = fmt.Sprintf("Partially staged %s: %d staged, %d failed or not attempted (no rollback; staged pieces stay staged). Nothing is deployed.",
			out.Service, staged, len(out.Failed))
	case staged == 0:
		sum = fmt.Sprintf("Nothing to stage on %s: everything requested was already in place.", out.Service)
	default:
		sum = fmt.Sprintf("Staged %s on %s (generation %d, applied %d). Nothing is deployed.", plural(staged, "change", "changes"), out.Service, out.Generation, out.AppliedGeneration)
	}
	out.Summary = sum
	out.Next = fmt.Sprintf("Run review_changes for %s, then apply_changes with expectedGeneration %d.", out.Service, out.Generation)
	out.NextTools = []NextTool{
		{Tool: "review_changes", Args: map[string]any{"service": out.Service}, Why: "See the deploy plan, warnings and blocking problems before applying."},
	}
	if out.PendingCount > 0 && out.Staged {
		why := "Deploy the staged changes; fails if the service changed since."
		if out.Production {
			why += " Production: a human must approve."
		}
		out.NextTools = append(out.NextTools, NextTool{
			Tool: "apply_changes",
			Args: map[string]any{"services": []any{map[string]any{"service": out.Service, "expectedGeneration": out.Generation}}},
			Why:  why,
		})
	}
}

// planStage validates the whole request and builds the ordered writes. It
// performs no write.
func (c *composites) planStage(ctx context.Context, serviceID uuid.UUID, in StageChangesInput) (*stagePlan, error) {
	sc, err := services.LoadScope(ctx, c.q(), policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return nil, err
	}
	if in.IfMatch != nil && *in.IfMatch != sc.Service.Generation {
		return nil, generationConflict(selectorOf(sc), *in.IfMatch, sc.Service.Generation)
	}
	pl := &stagePlan{sc: sc}

	var domainRemoves, domainAdds, volRemoves, volAdds, cfgOps, varOps []stageOp
	var domainGen []stageOp

	if in.Domains != nil {
		if domainRemoves, domainAdds, domainGen, err = c.planDomains(ctx, pl, in.Domains); err != nil {
			return nil, err
		}
	}
	if in.Volumes != nil {
		if volRemoves, volAdds, err = c.planVolumes(ctx, pl, in.Volumes); err != nil {
			return nil, err
		}
	}
	if in.Config != nil {
		if cfgOps, err = c.planConfig(pl, in.Config); err != nil {
			return nil, err
		}
	}
	if len(in.Variables) > 0 {
		if varOps, err = c.planVariables(ctx, pl, in.Variables); err != nil {
			return nil, err
		}
	}
	// Order: removals first (they may need a production approval, and nothing
	// is staged yet when that is requested), then config, adds, variables.
	pl.ops = append(pl.ops, domainRemoves...)
	pl.ops = append(pl.ops, volRemoves...)
	pl.ops = append(pl.ops, cfgOps...)
	pl.ops = append(pl.ops, volAdds...)
	pl.ops = append(pl.ops, varOps...)
	pl.ops = append(pl.ops, domainAdds...)
	pl.ops = append(pl.ops, domainGen...)
	return pl, nil
}

func generationConflict(sel string, got, current int64) error {
	e := mcpkit.Errf("generation_conflict",
		"%s is at generation %d, not %d: the change set moved since you looked. Run review_changes and retry with ifMatch %d.", sel, current, got, current)
	e.Data = map[string]any{"currentGeneration": current, "ifMatch": got}
	return e
}

// ---- config ----

func (c *composites) planConfig(pl *stagePlan, patch *StageConfigPatch) ([]stageOp, error) {
	sc := pl.sc
	cur := sc.Service
	old := sc.Config()
	hasConfig := patch.hasConfigFields()
	if sc.Service.Kind == services.KindRoute && (hasConfig || patch.ServerID != nil) {
		return nil, mcpkit.Errf("not_applicable", "route services take only a name change here; edit their upstreams and ports with the route operations")
	}
	cand := old
	patch.apply(&cand)
	var newName *string
	if patch.Name != nil {
		n := strings.TrimSpace(*patch.Name)
		if n == "" {
			return nil, invalid("config.name must not be empty")
		}
		if n != cur.Name {
			newName = &n
		}
	}
	var newServer *uuid.UUID
	if patch.ServerID != nil {
		id, err := parseUUID("config.serverId", *patch.ServerID)
		if err != nil {
			return nil, err
		}
		if cur.ServerID == nil || *cur.ServerID != id {
			newServer = &id
			pl.touched = append(pl.touched, "server")
		}
	}

	var cfgPtr *services.Config
	if hasConfig {
		// Mirror services.Update's normalization so the validation and the diff
		// here equal what will be staged.
		n := cand.WithDefaults()
		n.Database = old.Database
		n = n.WithDefaults()
		n.BlockEgress = old.BlockEgress
		if patch.BlockEgress != nil {
			n.BlockEgress = *patch.BlockEgress
		}
		if err := n.Validate(cur.SourceType); err != nil {
			return nil, services.ConfigError(err)
		}
		diff := services.DiffConfig("config", old, n)
		for _, d := range diff {
			pl.touched = append(pl.touched, d.Field)
		}
		if len(diff) > 0 {
			cfgPtr = &cand
		}
	}
	if cfgPtr == nil && newName == nil && newServer == nil {
		pl.steps = append(pl.steps, StageStep{Step: "config", Status: "unchanged", Message: "the config already has these values"})
		return nil, nil
	}
	var blockEgress *bool
	if cfgPtr != nil {
		blockEgress = patch.BlockEgress
	}
	return []stageOp{{step: "config", run: func(ctx context.Context) (string, error) {
		// Earlier steps of this call bumped the generation: read the current one.
		fresh, err := services.LoadScope(ctx, c.q(), policy.ScopeProjectsWrite, cur.ID)
		if err != nil {
			return "", err
		}
		_, err = c.Services.Update(ctx, cur.ID, fresh.Service.Generation, services.UpdateInput{
			Name: newName, Config: cfgPtr, ServerID: newServer, BlockEgress: blockEgress,
		})
		if err != nil {
			return "", err
		}
		var fields []string
		if newName != nil {
			fields = append(fields, "name")
		}
		if newServer != nil {
			fields = append(fields, "server")
		}
		if cfgPtr != nil {
			fields = append(fields, "config")
		}
		return strings.Join(fields, ", "), nil
	}}}, nil
}

func (p *StageConfigPatch) hasConfigFields() bool {
	q := *p
	q.Name, q.ServerID = nil, nil
	return q != StageConfigPatch{}
}

func (p *StageConfigPatch) apply(c *services.Config) {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	seti := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
		}
	}
	setv := func(dst *[]string, v *[]string) {
		if v != nil {
			*dst = append([]string(nil), (*v)...)
		}
	}
	set(&c.Image, p.Image)
	set(&c.GitURL, p.GitURL)
	set(&c.GitBranch, p.GitBranch)
	set(&c.DockerfilePath, p.DockerfilePath)
	set(&c.BuildContext, p.BuildContext)
	set(&c.BuildType, p.BuildType)
	set(&c.Submodules, p.Submodules)
	set(&c.BuildLocation, p.BuildLocation)
	seti(&c.Port, p.Port)
	setv(&c.StartCommand, p.StartCommand)
	seti(&c.StopGraceSeconds, p.StopGraceSeconds)
	seti(&c.MemoryMB, p.MemoryMB)
	if p.CPUs != nil {
		c.CPUs = *p.CPUs
	}
	seti(&c.ImageRetention, p.ImageRetention)
	if p.OneShot != nil {
		c.OneShot = *p.OneShot
	}
	setv(&c.PreDeployCommand, p.PreDeployCommand)
	seti(&c.PreDeployTimeoutSeconds, p.PreDeployTimeoutSeconds)
	setv(&c.PostDeployCommand, p.PostDeployCommand)
	seti(&c.PostDeployTimeoutSeconds, p.PostDeployTimeoutSeconds)
	if h := p.Healthcheck; h != nil {
		set(&c.Healthcheck.Type, h.Type)
		set(&c.Healthcheck.Path, h.Path)
		seti(&c.Healthcheck.Port, h.Port)
		seti(&c.Healthcheck.IntervalSeconds, h.IntervalSeconds)
		seti(&c.Healthcheck.TimeoutSeconds, h.TimeoutSeconds)
		seti(&c.Healthcheck.StartPeriodSeconds, h.StartPeriodSeconds)
		seti(&c.Healthcheck.DeadlineSeconds, h.DeadlineSeconds)
		seti(&c.Healthcheck.SuccessThreshold, h.SuccessThreshold)
		setv(&c.Healthcheck.Command, h.Command)
	}
	if p.BlockEgress != nil {
		c.BlockEgress = *p.BlockEgress
	}
	if p.ProcessName != nil {
		c.ProcessName = *p.ProcessName
	}
	if p.Singleton != nil {
		c.Singleton = *p.Singleton
	}
	if p.Processes != nil {
		// A full-list replace, like the REST PATCH: a process left out is removed.
		c.Processes = nil
		for _, in := range *p.Processes {
			c.Processes = append(c.Processes, in.ToProcess())
		}
	}
}

// ---- variables ----

// stageGenerator renders the template of a generated secret.
func stageGenerator(g *StageGenerate) (string, error) {
	n := g.Length
	if n == 0 {
		n = 32
	}
	if n < 1 || n > secretgen.MaxSecretLen {
		return "", invalid("generate.length must be 1..%d", secretgen.MaxSecretLen)
	}
	if g.Charset == "" {
		return fmt.Sprintf("${{secret(%d)}}", n), nil
	}
	if _, ok := secretgen.Preset(g.Charset); !ok {
		return "", invalid("generate.charset must be one of %s", strings.Join(secretgen.PresetNames(), ", "))
	}
	return fmt.Sprintf("${{secret(%d, %s)}}", n, g.Charset), nil
}

func (c *composites) planVariables(ctx context.Context, pl *stagePlan, vars []StageVariable) ([]stageOp, error) {
	if len(vars) > stageMaxVariables {
		return nil, invalid("at most %d variables per call", stageMaxVariables)
	}
	existing := map[string]bool{}
	list, err := c.Variables.List(ctx, pl.sc.Service.ID)
	if err != nil {
		return nil, err
	}
	for _, v := range list.Items {
		existing[v.Key] = true
	}

	seen := map[string]bool{}
	var sets []variables.VariableInput
	var setKeys, secretKeys, deletes []string
	for _, v := range vars {
		if err := variables.ValidateKey(v.Key); err != nil {
			return nil, err
		}
		if seen[v.Key] {
			return nil, invalid("variable %s appears twice", v.Key)
		}
		seen[v.Key] = true
		if v.Delete {
			if v.Value != nil || v.Secret != nil || v.Generate != nil || v.Scope != nil {
				return nil, invalid("variable %s: delete cannot be combined with value, secret, generate or scope", v.Key)
			}
			deletes = append(deletes, v.Key)
			continue
		}
		in := variables.VariableInput{Key: v.Key, IsSecret: v.Secret, Scope: v.Scope}
		switch {
		case v.Generate != nil && v.Value != nil:
			return nil, invalid("variable %s: give a value or generate, not both", v.Key)
		case v.Generate != nil:
			tpl, err := stageGenerator(v.Generate)
			if err != nil {
				return nil, invalid("variable %s: %s", v.Key, strings.TrimPrefix(err.Error(), "invalid_argument: "))
			}
			t := true
			in.Value, in.IsSecret = &tpl, &t
		case v.Value != nil:
			if len(*v.Value) > stageMaxValueLen {
				return nil, invalid("variable %s exceeds 64 KiB", v.Key)
			}
			if err := stageCheckTemplate(v.Key, *v.Value, v.Secret != nil && *v.Secret); err != nil {
				return nil, err
			}
			in.Value = v.Value
		case v.Secret == nil && v.Scope == nil:
			return nil, invalid("variable %s: give a value, generate, secret, scope or delete", v.Key)
		case !existing[v.Key]:
			return nil, invalid("variable %s does not exist yet: give a value or generate", v.Key)
		}
		if v.Scope != nil && !services.ValidScope(*v.Scope) {
			return nil, invalid("variable %s: scope must be runtime, build or both", v.Key)
		}
		sets = append(sets, in)
		setKeys = append(setKeys, v.Key)
		if in.IsSecret != nil && *in.IsSecret {
			secretKeys = append(secretKeys, v.Key)
		}
		pl.touched = append(pl.touched, "variables."+v.Key)
	}

	var ops []stageOp
	if len(sets) > 0 {
		sort.Strings(setKeys)
		sort.Strings(secretKeys)
		keys, secrets := setKeys, secretKeys
		ops = append(ops, stageOp{step: "variables.set", run: func(ctx context.Context) (string, error) {
			res, err := c.Variables.Put(ctx, pl.sc.Service.ID, sets)
			if err != nil {
				return "", err
			}
			msg := "keys: " + strings.Join(keys, ", ")
			if len(secrets) > 0 {
				msg += "; secret: " + strings.Join(secrets, ", ")
			}
			for _, w := range res.Warnings {
				msg += "; warning " + w.Code
				if w.Key != "" {
					msg += " (" + w.Key + ")"
				}
			}
			return clip(msg, 500), nil
		}})
	}
	for _, key := range deletes {
		if !existing[key] {
			pl.steps = append(pl.steps, StageStep{Step: "variables.delete:" + key, Status: "unchanged", Message: "no such variable"})
			continue
		}
		pl.touched = append(pl.touched, "variables."+key)
		ops = append(ops, stageOp{step: "variables.delete:" + key, run: func(ctx context.Context) (string, error) {
			return "", c.Variables.Delete(ctx, pl.sc.Service.ID, key)
		}})
	}
	return ops, nil
}

// stageCheckTemplate pre-validates a variable value without echoing it.
func stageCheckTemplate(key, val string, secret bool) error {
	tpl, err := varexpr.Parse(val)
	if err == nil && !tpl.HasGenerators() {
		_, err = varexpr.Validate(tpl)
	}
	if err == nil {
		return nil
	}
	var se *varexpr.SyntaxError
	if errors.As(err, &se) {
		if secret {
			return mcpkit.Errf("invalid_template", "variable %s: invalid ${{ }} template at offset %d (the value is not shown)", key, se.Offset)
		}
		return mcpkit.Errf("invalid_template", "variable %s: %s", key, se.Error())
	}
	return mcpkit.Errf("invalid_template", "variable %s: invalid template", key)
}

// ---- domains ----

func (c *composites) planDomains(ctx context.Context, pl *stagePlan, d *StageDomains) (removes, adds, gen []stageOp, err error) {
	if len(d.Add)+len(d.Remove) > stageMaxDomains {
		return nil, nil, nil, invalid("at most %d domain edits per call", stageMaxDomains)
	}
	cur, err := c.Domains.List(ctx, pl.sc.Service.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	byHost := map[string][]services.DomainView{}
	generated := false
	for _, v := range cur {
		h := strings.ToLower(v.Hostname)
		byHost[h] = append(byHost[h], v)
		generated = generated || v.Generated
	}
	adding := map[string]bool{}
	for _, a := range d.Add {
		host, err := domains.ValidateDomainHost(a.Hostname)
		if err != nil {
			return nil, nil, nil, err
		}
		if a.TargetPort < 0 || a.TargetPort > 65535 {
			return nil, nil, nil, invalid("domain %s: targetPort must be 0..65535", host)
		}
		if adding[host] {
			return nil, nil, nil, invalid("domain %s appears twice in add", host)
		}
		adding[host] = true
		if ex := byHost[host]; len(ex) > 0 {
			if a.TargetPort != 0 && ex[0].TargetPort != a.TargetPort {
				return nil, nil, nil, mcpkit.Errf("domain_conflict", "%s is already attached to this service with target port %d; remove it first to change the port", host, ex[0].TargetPort)
			}
			pl.steps = append(pl.steps, StageStep{Step: "domains.add:" + host, Status: "unchanged", Message: "already attached"})
			continue
		}
		pl.prefix = append(pl.prefix, "domains."+host)
		port, process := a.TargetPort, strings.TrimSpace(a.Process)
		adds = append(adds, stageOp{step: "domains.add:" + host, run: func(ctx context.Context) (string, error) {
			dv, err := c.Domains.CreateForComponent(ctx, pl.sc.Service.ID, host, port, process, domains.RuleInput{})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("target port %d", dv.TargetPort), nil
		}})
	}
	for _, r := range d.Remove {
		host, err := domains.ValidateDomainHost(r)
		if err != nil {
			return nil, nil, nil, err
		}
		if adding[host] {
			return nil, nil, nil, invalid("domain %s is in both add and remove", host)
		}
		ex := byHost[host]
		if len(ex) == 0 {
			pl.steps = append(pl.steps, StageStep{Step: "domains.remove:" + host, Status: "unchanged", Message: "not attached"})
			continue
		}
		pl.prefix = append(pl.prefix, "domains."+host)
		ids := make([]uuid.UUID, len(ex))
		for i, e := range ex {
			ids[i] = e.ID
		}
		removes = append(removes, stageOp{step: "domains.remove:" + host, run: func(ctx context.Context) (string, error) {
			for _, id := range ids {
				if err := c.Domains.Delete(ctx, id); err != nil {
					return "", err
				}
			}
			return "", nil
		}})
	}
	if d.Generate {
		if generated {
			pl.steps = append(pl.steps, StageStep{Step: "domains.generate", Status: "unchanged", Message: "the service already has a generated hostname"})
		} else {
			pl.prefix = append(pl.prefix, "domains.")
			gen = append(gen, stageOp{step: "domains.generate", run: func(ctx context.Context) (string, error) {
				dv, err := c.Domains.Generate(ctx, pl.sc.Service.ID, "")
				if err != nil {
					return "", err
				}
				return dv.Hostname, nil
			}})
		}
	}
	return removes, adds, gen, nil
}

// ---- volumes ----

func (c *composites) planVolumes(ctx context.Context, pl *stagePlan, v *StageVolumes) (removes, adds []stageOp, err error) {
	if len(v.Add)+len(v.Remove) > stageMaxVolumes {
		return nil, nil, invalid("at most %d volume edits per call", stageMaxVolumes)
	}
	if len(v.Add) > 0 {
		if err := services.RequireContainer(pl.sc, "volumes"); err != nil {
			return nil, nil, err
		}
		if err := services.RequireNotStack(pl.sc); err != nil {
			return nil, nil, err
		}
	}
	cur, err := c.Services.ListVolumes(ctx, pl.sc.Service.ID)
	if err != nil {
		return nil, nil, err
	}
	byName := map[string]services.VolumeView{}
	byMount := map[string]services.VolumeView{}
	for _, x := range cur {
		byName[x.Name] = x
		byMount[x.MountPath] = x
	}
	adding := map[string]bool{}
	for _, a := range v.Add {
		name := strings.TrimSpace(a.Name)
		if !stageVolumeNameRe.MatchString(name) {
			return nil, nil, mcpkit.Errf("invalid_volume_name", "volume names are lowercase letters, digits, '.', '_' and '-' (max 63 characters)")
		}
		mount := pathpkg.Clean(strings.TrimSpace(a.MountPath))
		if !strings.HasPrefix(mount, "/") || mount == "/" || strings.Contains(mount, ":") {
			return nil, nil, mcpkit.Errf("invalid_mount_path", "volume %s: mountPath must be an absolute path other than /", name)
		}
		if adding[name] {
			return nil, nil, invalid("volume %s appears twice in add", name)
		}
		adding[name] = true
		if ex, ok := byName[name]; ok {
			if ex.MountPath == mount {
				pl.steps = append(pl.steps, StageStep{Step: "volumes.add:" + name, Status: "unchanged", Message: "already attached at " + mount})
				continue
			}
			return nil, nil, mcpkit.Errf("volume_exists", "volume %s already exists at %s", name, ex.MountPath)
		}
		if ex, ok := byMount[mount]; ok {
			return nil, nil, mcpkit.Errf("mount_path_in_use", "volume %s is already mounted at %s", ex.Name, mount)
		}
		pl.touched = append(pl.touched, "volumes."+name)
		adds = append(adds, stageOp{step: "volumes.add:" + name, run: func(ctx context.Context) (string, error) {
			_, err := c.Services.AddVolume(ctx, pl.sc.Service.ID, name, mount)
			return "mounted at " + mount, err
		}})
	}
	for _, r := range v.Remove {
		name := strings.TrimSpace(r)
		if adding[name] {
			return nil, nil, invalid("volume %s is in both add and remove", name)
		}
		ex, ok := byName[name]
		if !ok {
			pl.steps = append(pl.steps, StageStep{Step: "volumes.remove:" + name, Status: "unchanged", Message: "not attached"})
			continue
		}
		pl.touched = append(pl.touched, "volumes."+name)
		id := ex.ID
		removes = append(removes, stageOp{step: "volumes.remove:" + name, run: func(ctx context.Context) (string, error) {
			return "detached; the data stays on the host", c.Services.DeleteVolume(ctx, id)
		}})
	}
	return removes, adds, nil
}
