package composites_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/configsync"
	"github.com/nebulactrl/nebulactrl/internal/mcp/composites"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
)

// cfgFake is the nebula.toml use case: it records what the tool passes and
// answers from canned values.
type cfgFake struct {
	project    uuid.UUID
	planReq    configsync.PlanRequest
	plan       configsync.Plan
	planErr    error
	plans      int
	applyReq   configsync.ApplyRequest
	apply      configsync.ApplyResult
	applyErr   error
	applies    int
	exportArgs []any
	export     configsync.ExportResult
}

func (c *cfgFake) Plan(_ context.Context, id uuid.UUID, r configsync.PlanRequest) (configsync.Plan, error) {
	c.project, c.planReq = id, r
	c.plans++
	return c.plan, c.planErr
}

func (c *cfgFake) ApplyGuarded(_ context.Context, id uuid.UUID, r configsync.ApplyRequest) (configsync.ApplyResult, error) {
	c.project, c.applyReq = id, r
	c.applies++
	return c.apply, c.applyErr
}

func (c *cfgFake) Export(_ context.Context, id uuid.UUID, env, base string, all, defaults bool) (configsync.ExportResult, error) {
	c.project, c.exportArgs = id, []any{env, base, all, defaults}
	return c.export, nil
}

// cfgSection is one section ("export", "plan", "apply") of a sync_config result.
func cfgSection(t *testing.T, out map[string]any, name string) map[string]any {
	t.Helper()
	m, ok := out[name].(map[string]any)
	require.True(t, ok, "the %s section is missing in %v", name, out)
	require.Equal(t, name, out["action"])
	for _, other := range []string{"export", "plan", "apply"} {
		if other != name {
			require.NotContains(t, out, other, "only the section of the action is filled")
		}
	}
	return m
}

// withConfig installs the fake and rebuilds the tools.
func (f *fx) withConfig(c *cfgFake) *cfgFake {
	f.deps.ConfigSync = c
	f.tools = map[string]mcpkit.Tool{}
	for _, tl := range composites.Tools(f.deps) {
		f.tools[tl.Name] = tl
	}
	return c
}

const cfgFile = "schema = 1\n\n[services.web]\nimage = \"nginx:1\"\n"

func cfgPlan(f *fx, production bool, immediate bool) configsync.Plan {
	svc := uuid.New()
	env := f.staging
	if production {
		env = f.prod
	}
	immediateCount := 0
	if immediate {
		immediateCount = 1
	}
	changes := []configsync.PlanChange{{Path: "services.web.port", Line: 4, Field: "config.port", Op: configsync.OpSet, Applies: configsync.AppliesStaged, Reason: configsync.ReasonFileChanged, From: "3000", To: "8080"}}
	if immediate {
		changes = append(changes, configsync.PlanChange{Path: "services.web.scale.replicas", Field: "scale.replicas", Op: configsync.OpSet, Applies: configsync.AppliesImmediate, Reason: configsync.ReasonNew, From: "1", To: "2"})
	}
	return configsync.Plan{
		PlanID: "pl_0123456789abcdef0123456789abcdef", Schema: 1, Valid: true,
		Project:     configsync.PlanProject{ID: f.project.ID, Slug: "shop"},
		Environment: configsync.PlanEnvironment{ID: env.ID, Slug: env.Slug, IsProduction: production},
		Source:      configsync.PlanSource{Kind: configsync.SourceInline, FileSHA256: "abc", Bytes: len(cfgFile)},
		Summary:     configsync.PlanSummary{Update: 1, Unchanged: 1, Staged: 1, Immediate: immediateCount, RequiresDeploy: []string{"web"}},
		Services: []configsync.PlanService{{
			Slug: "web", Kind: "git", Action: configsync.ActionUpdate, ServiceID: &svc, Generation: 12, Managed: true, State: configsync.StateFileChanged,
			Changes: changes, Unmanaged: map[string]int{"variables": 6},
		}},
		RequiredScopes: []string{policy.ScopeProjectsWrite},
		Guard:          configsync.Guard{ProductionApproval: configsync.ApprovalNotRequired},
	}
}

func TestSyncConfigIsUnavailableWithoutTheUseCase(t *testing.T) {
	f := newFx(t)
	for action, args := range map[string]map[string]any{
		"export": {"project": "shop"},
		"plan":   {"project": "shop", "environment": "staging", "file": cfgFile},
		"apply":  {"project": "shop", "environment": "staging", "file": cfgFile, "planId": "pl_x"},
	} {
		args["action"] = action
		_, err := f.call(f.admin, "sync_config", args)
		require.Equal(t, mcpkit.CodeUnavailable, errCode(err), action)
	}
}

func TestSyncConfigValidatesItsArguments(t *testing.T) {
	f := newFx(t)
	c := f.withConfig(&cfgFake{})
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"project": "shop"}, "action must be export, plan or apply"},
		{map[string]any{"project": "shop", "action": "validate"}, "action must be export, plan or apply"},
		{map[string]any{"action": "export"}, "project is required"},
		{map[string]any{"project": "shop", "action": "export", "offset": -1}, "offset must not be negative"},
		{map[string]any{"project": "shop", "action": "plan", "file": cfgFile}, "environment is required"},
		{map[string]any{"action": "plan", "environment": "staging", "file": cfgFile}, "project is required"},
		{map[string]any{"project": "shop", "action": "plan", "environment": "staging"}, "file is required"},
		{map[string]any{"project": "shop", "action": "plan", "environment": "staging", "file": strings.Repeat("a", 256<<10+1)}, "limit is"},
		{map[string]any{"project": "shop", "action": "apply", "environment": "staging", "file": cfgFile}, "planId is required"},
		{map[string]any{"project": "shop", "action": "apply", "file": cfgFile, "planId": "pl_x"}, "environment is required"},
		{map[string]any{"project": "shop", "action": "apply", "environment": "staging", "file": cfgFile, "planId": "  "}, "planId is required"},
	}
	for _, tc := range cases {
		_, err := f.call(f.admin, "sync_config", tc.args)
		require.Equal(t, mcpkit.CodeInvalidArgument, errCode(err), "%v", tc.args)
		require.Contains(t, err.Error(), tc.want)
	}
	// Agents cannot ask for deploy or prune: the inputs do not exist.
	for _, extra := range []string{"deploy", "prune"} {
		_, err := f.call(f.admin, "sync_config", map[string]any{"project": "shop", "action": "apply", "environment": "staging", "file": cfgFile, "planId": "pl_x", extra: true})
		require.Equal(t, mcpkit.CodeInvalidArgument, errCode(err), extra)
	}
	_, err := f.call(f.admin, "sync_config", map[string]any{"project": "no-such-project", "action": "plan", "environment": "staging", "file": cfgFile})
	require.Equal(t, mcpkit.CodeNotFound, errCode(err))
	require.Zero(t, c.applies)
	require.Zero(t, c.plans)
}

// apply writes, so it needs projects:write even though the tool is listed to
// readers; export and plan need projects:read only.
func TestSyncConfigApplyNeedsWriteScope(t *testing.T) {
	f := newFx(t)
	c := f.withConfig(&cfgFake{plan: cfgPlan(f, false, false), export: configsync.ExportResult{File: cfgFile}})
	reader := f.principal(policy.ScopeProjectsRead)

	_, err := f.call(reader, "sync_config", map[string]any{"project": "shop", "action": "export"})
	require.NoError(t, err)
	_, err = f.call(reader, "sync_config", map[string]any{"project": "shop", "action": "plan", "environment": "staging", "file": cfgFile})
	require.NoError(t, err)

	_, err = f.call(reader, "sync_config", map[string]any{"project": "shop", "action": "apply", "environment": "staging", "file": cfgFile, "planId": "pl_x"})
	require.Equal(t, mcpkit.CodeForbidden, errCode(err))
	require.Contains(t, err.Error(), "projects:write")
	require.Zero(t, c.applies, "nothing reaches the use case")
}

func TestPlanMapsThePlan(t *testing.T) {
	f := newFx(t)
	c := f.withConfig(&cfgFake{plan: cfgPlan(f, false, true)})
	out, err := f.call(f.admin, "sync_config", map[string]any{
		"project": "shop", "action": "plan", "environment": " staging ", "file": cfgFile, "only": []string{"services.web"},
		"attachments": map[string]string{"a.yml": "x"},
	})
	require.NoError(t, err)
	require.Equal(t, configsync.PlanRequest{
		Environment: "staging", Only: []string{"services.web"}, Prune: false,
		Input: configsync.Input{
			File: cfgFile, Attachments: map[string]string{"a.yml": "x"},
			Source: configsync.SourceInfo{Kind: configsync.SourceInline, Origin: configsync.OriginMCP},
		},
	}, c.planReq, "an agent never plans with prune; its file is inline and says where it came from")
	require.Equal(t, f.project.ID, c.project, "the project selector is resolved")

	plan := cfgSection(t, out, "plan")
	require.Equal(t, true, plan["valid"])
	require.Equal(t, "pl_0123456789abcdef0123456789abcdef", plan["planId"])
	require.Equal(t, false, plan["production"])
	require.Equal(t, false, plan["approvalRequired"])
	svc := plan["services"].([]any)[0].(map[string]any)
	require.Equal(t, "web", svc["slug"])
	require.Equal(t, c.plan.Services[0].ServiceID.String(), svc["serviceId"], "ids are strings, not byte arrays")
	require.EqualValues(t, 12, svc["generation"])
	changes := svc["changes"].([]any)
	require.Len(t, changes, 2)
	require.Equal(t, "immediate", changes[1].(map[string]any)["applies"])
	require.Equal(t, map[string]any{"variables": float64(6)}, svc["unmanaged"])
	require.Equal(t, []any{policy.ScopeProjectsWrite}, plan["requiredScopes"])
	require.Equal(t, []any{}, plan["diagnostics"], "an empty list, never null")
	require.Contains(t, out["summary"], "Plan for shop/staging")
	require.Contains(t, out["summary"], "1 staged and 1 immediate")
	next := out["nextTools"].([]any)[0].(map[string]any)
	require.Equal(t, "sync_config", next["tool"])
	args := next["args"].(map[string]any)
	require.Equal(t, "apply", args["action"])
	require.Equal(t, c.plan.PlanID, args["planId"])
}

func TestPlanSaysWhenApprovalIsNeeded(t *testing.T) {
	f := newFx(t)
	c := f.withConfig(&cfgFake{})
	agent := f.principal(policy.AllScopes...)
	require.True(t, prodguard.Guarded(agent))
	args := map[string]any{"project": "shop", "action": "plan", "environment": "production", "file": cfgFile}

	// Production, a change that takes effect at once, a guarded principal.
	c.plan = cfgPlan(f, true, true)
	out, err := f.call(agent, "sync_config", args)
	require.NoError(t, err)
	require.Equal(t, true, cfgSection(t, out, "plan")["approvalRequired"])
	require.Contains(t, out["summary"], "a person must approve")

	// Staged only: no approval, the same as stage_changes.
	c.plan = cfgPlan(f, true, false)
	out, err = f.call(agent, "sync_config", args)
	require.NoError(t, err)
	require.Equal(t, false, cfgSection(t, out, "plan")["approvalRequired"])
	require.NotContains(t, out["summary"], "approve")

	// A token an owner exempted from approvals is not asked.
	c.plan = cfgPlan(f, true, true)
	exempt := f.principal(policy.AllScopes...)
	exempt.AllowProductionWithoutApproval = true
	out, err = f.call(exempt, "sync_config", args)
	require.NoError(t, err)
	require.Equal(t, false, cfgSection(t, out, "plan")["approvalRequired"])
}

// The plan carries the diagnostics of the file: there is no separate validate.
func TestPlanInvalidReportsDiagnosticsAsDataAndNoChange(t *testing.T) {
	f := newFx(t)
	c := f.withConfig(&cfgFake{})
	args := map[string]any{"project": "shop", "action": "plan", "environment": "staging", "file": cfgFile}

	bad := cfgPlan(f, false, false)
	bad.Valid = false
	bad.Summary.Errors = 2
	bad.Diagnostics = configsync.Diagnostics{
		{Severity: "error", Code: "unknown_server", Message: "no server fra-9", Path: "server", Line: 3},
		{Severity: "error", Code: "unknown_key", Message: `unknown key "healthcheck"`, Path: "services.web.healthcheck", Line: 19, Column: 1, Suggestions: []string{"health_check"}},
	}
	c.plan = bad
	out, err := f.call(f.admin, "sync_config", args)
	require.NoError(t, err, "an invalid plan is a result, not an error")
	plan := cfgSection(t, out, "plan")
	require.Equal(t, false, plan["valid"])
	require.Contains(t, out["summary"], "not valid: 2 errors")
	require.Contains(t, out["summary"], "line 3: server [unknown_server]", "the first error with its place")
	diag := plan["diagnostics"].([]any)[1].(map[string]any)
	require.Equal(t, "unknown_key", diag["code"])
	require.Equal(t, []any{"health_check"}, diag["suggestions"])
	next := out["nextTools"].([]any)[0].(map[string]any)
	require.Equal(t, "sync_config", next["tool"])
	require.Equal(t, "plan", next["args"].(map[string]any)["action"], "fix the file, then plan again")

	clean := cfgPlan(f, false, false)
	clean.Services[0].Changes = nil
	clean.Summary = configsync.PlanSummary{Unchanged: 2, NotInFile: 1}
	c.plan = clean
	out, err = f.call(f.admin, "sync_config", args)
	require.NoError(t, err)
	require.Contains(t, out["summary"], "Nothing to change")
	require.Equal(t, []any{}, cfgSection(t, out, "plan")["services"].([]any)[0].(map[string]any)["changes"], "no null lists")
	require.Empty(t, out["nextTools"], "nothing to apply")
}

func TestPlanFitsTheResultCap(t *testing.T) {
	f := newFx(t)
	big := cfgPlan(f, false, false)
	big.Services = nil
	for i := range 8 {
		id := uuid.New()
		sp := configsync.PlanService{Slug: "svc-" + string(rune('a'+i)), Action: configsync.ActionUpdate, ServiceID: &id, Generation: 1}
		for j := range 120 {
			sp.Changes = append(sp.Changes, configsync.PlanChange{
				Path: "services.x.variables.K", Field: "variables.K", Op: configsync.OpSet, Applies: configsync.AppliesStaged,
				From: strings.Repeat("o", 120), To: strings.Repeat("n", 120+j%3),
			})
		}
		big.Services = append(big.Services, sp)
	}
	f.withConfig(&cfgFake{plan: big})
	out, err := f.call(f.admin, "sync_config", map[string]any{"project": "shop", "action": "plan", "environment": "staging", "file": cfgFile})
	require.NoError(t, err)
	raw, _ := json.Marshal(out)
	require.Less(t, len(raw), mcpkit.MaxResultBytes, "a result over 64 KB would be dropped whole")
	plan := cfgSection(t, out, "plan")
	require.Greater(t, plan["changesOmitted"], float64(0))
	require.Contains(t, out["summary"], "not listed")
	require.Len(t, plan["services"], 8, "every service keeps its action")
	require.Equal(t, big.PlanID, plan["planId"])
}

func TestPlanAndApplyRefusalsCarryACompactPlan(t *testing.T) {
	f := newFx(t)
	bad := cfgPlan(f, false, true)
	bad.Valid = false
	for i := range 30 {
		bad.Diagnostics = append(bad.Diagnostics, configsync.Diagnostic{Severity: "error", Code: "invalid_value", Message: "m", Path: "p" + string(rune('a'+i%26))})
	}
	c := f.withConfig(&cfgFake{planErr: configsync.ErrConfigBlocked(bad), applyErr: configsync.ErrPlanStale(bad)})

	_, err := f.call(f.admin, "sync_config", map[string]any{"project": "shop", "action": "plan", "environment": "staging", "file": cfgFile})
	var te *mcpkit.Error
	require.ErrorAs(t, err, &te)
	require.Equal(t, "config_blocked", te.Code)
	data := te.Data.(map[string]any)["plan"].(map[string]any)
	require.Equal(t, bad.PlanID, data["planId"])
	require.Len(t, data["errors"], 20, "the first twenty errors")
	require.NotContains(t, data, "services", "the whole plan is not repeated in the error")

	applyArgs := map[string]any{"project": "shop", "action": "apply", "environment": "staging", "file": cfgFile, "planId": "pl_old"}
	_, err = f.call(f.admin, "sync_config", applyArgs)
	require.ErrorAs(t, err, &te)
	require.Equal(t, "plan_stale", te.Code)
	require.Equal(t, bad.PlanID, te.Data.(map[string]any)["plan"].(map[string]any)["planId"], "the fresh plan id comes back")

	c.applyErr = configsync.ErrInsufficientScope([]string{"admin"})
	_, err = f.call(f.admin, "sync_config", applyArgs)
	require.ErrorAs(t, err, &te)
	require.Equal(t, "insufficient_scope", te.Code)
	require.Equal(t, []string{"admin"}, te.Data.(map[string]any)["scopes"])
}

func TestApplyStagesAndNeverDeploysOrPrunes(t *testing.T) {
	f := newFx(t)
	plan := cfgPlan(f, false, true)
	svc := *plan.Services[0].ServiceID
	c := f.withConfig(&cfgFake{apply: configsync.ApplyResult{
		PlanID: plan.PlanID, Status: configsync.StatusStaged,
		Steps: []configsync.ApplyStep{
			{Step: "config:web", Status: configsync.StepApplied},
			{Step: "scale:web", Status: configsync.StepApplied},
			{Step: "ownership:web", Status: configsync.StepApplied},
		},
		Services:    []configsync.AppliedService{{Slug: "web", ServiceID: svc, Action: configsync.ActionUpdate, Generation: 13}, {Slug: "worker", ServiceID: uuid.New(), Action: configsync.ActionUnchanged, Generation: 2}},
		Deployments: []uuid.UUID{},
		Plan:        plan,
	}})
	out, err := f.call(f.admin, "sync_config", map[string]any{
		"project": "shop", "action": "apply", "environment": " staging ", "file": cfgFile, "planId": " " + plan.PlanID + " ", "only": []string{"services.web"},
	})
	require.NoError(t, err)
	require.Equal(t, configsync.ApplyRequest{
		PlanRequest: configsync.PlanRequest{
			Environment: "staging", Only: []string{"services.web"},
			Input: configsync.Input{File: cfgFile, Source: configsync.SourceInfo{Kind: configsync.SourceInline, Origin: configsync.OriginMCP}},
		},
		PlanID: plan.PlanID,
	}, c.applyReq, "no deploy, no prune, the planId trimmed")
	require.Equal(t, f.project.ID, c.project)

	ap := cfgSection(t, out, "apply")
	require.Equal(t, "staged", ap["status"])
	require.Equal(t, plan.PlanID, ap["planId"])
	require.Contains(t, out["summary"], "Staged the file on shop/staging: 1 service changed. Nothing is deployed.")
	require.Len(t, ap["steps"], 3)
	require.Empty(t, ap["failed"])
	svcs := ap["services"].([]any)
	require.Equal(t, svc.String(), svcs[0].(map[string]any)["serviceId"])
	require.EqualValues(t, 13, svcs[0].(map[string]any)["generation"])
	next := out["nextTools"].([]any)
	require.Len(t, next, 1, "only changed services are reviewed")
	require.Equal(t, "review_changes", next[0].(map[string]any)["tool"])
	require.Equal(t, svc.String(), next[0].(map[string]any)["args"].(map[string]any)["service"])
	require.Contains(t, ap["next"], "apply_changes")
}

func TestApplyPartialAndNoop(t *testing.T) {
	f := newFx(t)
	plan := cfgPlan(f, false, true)
	svc := *plan.Services[0].ServiceID
	c := f.withConfig(&cfgFake{apply: configsync.ApplyResult{
		PlanID: plan.PlanID, Status: configsync.StatusPartial,
		Steps: []configsync.ApplyStep{
			{Step: "config:web", Status: configsync.StepApplied},
			{Step: "scale:web", Status: configsync.StepFailed, Message: "the server is offline"},
			{Step: "tasks:web:digest", Status: configsync.StepNotAttempted},
		},
		Services: []configsync.AppliedService{{Slug: "web", ServiceID: svc, Action: configsync.ActionUpdate, Generation: 13}}, Plan: plan,
	}})
	args := map[string]any{"project": "shop", "action": "apply", "environment": "staging", "file": cfgFile, "planId": plan.PlanID}
	out, err := f.call(f.admin, "sync_config", args)
	require.NoError(t, err)
	ap := cfgSection(t, out, "apply")
	require.Equal(t, "partial", ap["status"])
	require.Len(t, ap["failed"], 2)
	require.Contains(t, out["summary"], "did not complete (scale:web, tasks:web:digest)")
	require.Equal(t, "scale:web", ap["failed"].([]any)[0].(map[string]any)["step"])

	c.apply = configsync.ApplyResult{PlanID: plan.PlanID, Status: configsync.StatusNoop, Plan: plan}
	out, err = f.call(f.admin, "sync_config", args)
	require.NoError(t, err)
	ap = cfgSection(t, out, "apply")
	require.Equal(t, "noop", ap["status"])
	require.Contains(t, out["summary"], "Nothing to apply")
	require.Equal(t, []any{}, ap["steps"])
	require.Equal(t, []any{}, ap["services"])
	require.Empty(t, out["nextTools"])
}

func TestApplyReturnsTheApprovalRequestUnchanged(t *testing.T) {
	f := newFx(t)
	want := &prodguard.ErrApprovalRequired{Plan: prodguard.ApprovalPlan{Op: "apply_config", Summary: "Apply nebula.toml to shop / production"}, ApprovalID: uuid.New(), URL: "https://nebula.test/approvals/x"}
	f.withConfig(&cfgFake{applyErr: want})
	_, err := f.call(f.admin, "sync_config", map[string]any{"project": "shop", "action": "apply", "environment": "production", "file": cfgFile, "planId": "pl_x"})
	got, ok := prodguard.AsRequired(err)
	require.True(t, ok, "the MCP layer turns this into confirmation_required; wrapping it would hide it: %v", err)
	require.Equal(t, want, got)
}

func TestExportReturnsTheFileInParts(t *testing.T) {
	f := newFx(t)
	var b strings.Builder
	for b.Len() < 100<<10 {
		b.WriteString("[services.web.variables]\nKEY = \"" + strings.Repeat("v", 60) + "\"\n")
	}
	full := b.String()
	c := f.withConfig(&cfgFake{export: configsync.ExportResult{File: full, Warnings: configsync.Diagnostics{{Severity: "warning", Code: "secret_flag_differs", Message: "m"}}}})

	var joined strings.Builder
	offset, parts := 0, 0
	for {
		args := map[string]any{"project": "shop", "action": "export", "environment": "production"}
		if offset > 0 {
			args["offset"] = offset
		}
		out, err := f.call(f.admin, "sync_config", args)
		require.NoError(t, err)
		raw, _ := json.Marshal(out)
		require.Less(t, len(raw), mcpkit.MaxResultBytes)
		ex := cfgSection(t, out, "export")
		require.EqualValues(t, len(full), ex["bytes"])
		part := ex["file"].(string)
		require.True(t, strings.HasSuffix(part, "\n"), "parts end on a line boundary")
		joined.WriteString(part)
		parts++
		next0 := out["nextTools"].([]any)[0].(map[string]any)
		require.Equal(t, "sync_config", next0["tool"])
		if ex["truncated"] != true {
			require.Contains(t, out["summary"], "Exported shop/production")
			require.Equal(t, "plan", next0["args"].(map[string]any)["action"], "edit, then plan")
			break
		}
		require.Contains(t, out["summary"], "Call again with offset=")
		next := int(ex["nextOffset"].(float64))
		require.Equal(t, offset+len(part), next)
		offset = next
		require.Equal(t, "export", next0["args"].(map[string]any)["action"])
		require.EqualValues(t, next, next0["args"].(map[string]any)["offset"])
		require.Less(t, parts, 20)
	}
	require.Equal(t, full, joined.String(), "the parts add up to the file")
	require.Greater(t, parts, 1)
	require.Equal(t, []any{"production", "", false, false}, c.exportArgs)

	_, err := f.call(f.admin, "sync_config", map[string]any{"project": "shop", "action": "export", "offset": len(full) + 1})
	require.Equal(t, mcpkit.CodeInvalidArgument, errCode(err))
}

func TestExportSmallFileAllEnvironments(t *testing.T) {
	f := newFx(t)
	c := f.withConfig(&cfgFake{export: configsync.ExportResult{File: cfgFile}})
	out, err := f.call(f.admin, "sync_config", map[string]any{"project": "shop", "action": "export", "environment": "ALL", "base": "staging"})
	require.NoError(t, err)
	ex := cfgSection(t, out, "export")
	require.Equal(t, cfgFile, ex["file"])
	require.Equal(t, false, ex["truncated"])
	require.Equal(t, []any{"", "staging", true, false}, c.exportArgs)
	require.Equal(t, []any{}, ex["warnings"])
	require.Contains(t, out["summary"], "all environments")
}

func TestSyncConfigToolContract(t *testing.T) {
	// One tool carries the whole file lifecycle, so the tool list stays small.
	// It is listed to readers (export and plan need projects:read); apply is
	// refused without projects:write. It lives in the config toolset.
	var found int
	for _, tl := range composites.Tools(composites.Deps{}) {
		for _, gone := range []string{"validate_config", "plan_config", "apply_config", "export_config"} {
			require.NotEqual(t, gone, tl.Name, "merged into sync_config")
		}
		if tl.Name != "sync_config" {
			continue
		}
		found++
		require.Equal(t, []string{policy.ScopeProjectsRead}, tl.Scopes)
		require.Equal(t, mcpkit.ToolsetConfig, tl.Toolset)
		require.False(t, tl.Hints.ReadOnly, "apply stages")
		require.False(t, tl.Hints.Destructive)
		require.Contains(t, tl.Description, "secret", "the secrets rule is in the description")
		require.Contains(t, tl.Description, "never deploys")
	}
	require.Equal(t, 1, found)
}
