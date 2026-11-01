package configsync

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/integrations/cloudflare"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/scaling"
	"github.com/nebulactrl/nebulactrl/internal/tasks"
	"github.com/nebulactrl/nebulactrl/internal/uptime"
)

// immStep is one immediate-kind step of phase 2: it runs a use case after the staged part committed
// and, only when it succeeds, records the ownership of the fields it covers.
type immStep struct {
	slug      string
	serviceID uuid.UUID
	name      string
	fields    []*fieldPlan
	drop      []string // owned ids removed by this step
	// pending is the record kept when the step defers: field id -> desired value.
	pending    map[string]json.RawMessage
	clear      []string
	run        func(ctx context.Context) error
	deferrable bool
}

// deferrableCodes are the use-case refusals that only mean "not yet": the service has no deployment
// or no server to act on. The step is recorded as pending and runs when a deployment succeeded.
var deferrableCodes = map[string]bool{
	"service_not_deployed": true, "no_server": true, "server_unavailable": true, "server_offline": true,
	"database_not_running": true, "not_deployed": true,
}

func isDeferrable(err error) bool {
	ae, ok := apierr.As(err)
	return ok && deferrableCodes[ae.Code]
}

// planImmediate lists the immediate steps of a service.
func (a *applier) planImmediate(sp *svcPlan) {
	slug := sp.slug
	id := a.rows[slug].ID
	add := func(st *immStep) {
		st.slug, st.serviceID = slug, id
		a.imm = append(a.imm, st)
	}
	imm := func(prefix string) []*fieldPlan {
		var out []*fieldPlan
		for _, f := range sp.changedWith(prefix) {
			if !phase1Field(f) {
				out = append(out, f)
			}
		}
		return out
	}

	if fs := imm("scale."); len(fs) > 0 {
		add(&immStep{name: stepName("scale", slug), fields: fs, run: func(ctx context.Context) error { return a.runScale(ctx, id, fs) }})
	}
	byProcess := map[string][]*fieldPlan{}
	for _, f := range imm("processes.") {
		if name, _, ok := splitProcessScaleID(f.fv.ID); ok {
			byProcess[name] = append(byProcess[name], f)
		}
	}
	for _, name := range sortedKeys(byProcess) {
		name, fs := name, byProcess[name]
		// A process the running release does not have yet still stores its setting.
		add(&immStep{name: stepName("scale", slug, name), fields: fs, run: func(ctx context.Context) error { return a.runProcessScale(ctx, id, name, fs) }})
	}
	if fs := imm("git."); len(fs) > 0 {
		add(&immStep{name: stepName("git", slug), fields: fs, run: func(ctx context.Context) error { return a.runGit(ctx, sp, id, fs) }})
	}
	if fs := imm("uptime."); len(fs) > 0 {
		add(&immStep{name: stepName("uptime", slug), fields: fs, run: func(ctx context.Context) error { return a.runUptime(ctx, sp, id, fs, false) }})
	}
	if rm := sp.removesWith("uptime."); len(rm) > 0 {
		var ids []string
		for _, f := range rm {
			ids = append(ids, f.fv.ID)
		}
		add(&immStep{name: stepName("uptime", slug, "reset"), drop: ids, run: func(ctx context.Context) error { return a.runUptime(ctx, sp, id, nil, true) }})
	}
	for _, f := range imm("tasks.") {
		f := f
		name := strings.TrimPrefix(f.fv.ID, "tasks.")
		add(&immStep{name: stepName("tasks", slug, name), fields: []*fieldPlan{f}, run: func(ctx context.Context) error { return a.runTask(ctx, sp, id, f) }})
	}
	for _, f := range sp.removesWith("tasks.") {
		name := strings.TrimPrefix(f.fv.ID, "tasks.")
		t, ok := sp.ls.Tasks[name]
		if !ok {
			continue
		}
		add(&immStep{name: stepName("tasks", slug, name), drop: []string{f.fv.ID}, run: func(ctx context.Context) error { return a.s.Tasks.DeleteTask(ctx, t.ID) }})
	}
	for _, f := range imm("backups.") {
		f := f
		key := strings.TrimPrefix(f.fv.ID, "backups.")
		pend := map[string]json.RawMessage{f.fv.ID: jsonOf(f.fv.Value)}
		add(&immStep{name: stepName("backups", slug, key), fields: []*fieldPlan{f}, pending: pend, clear: []string{f.fv.ID}, deferrable: true,
			run: func(ctx context.Context) error { return a.runBackup(ctx, sp, id, f) }})
	}
	for _, f := range sp.removesWith("backups.") {
		key := strings.TrimPrefix(f.fv.ID, "backups.")
		b, ok := sp.ls.backupByKey(key)
		if !ok {
			continue
		}
		add(&immStep{name: stepName("backups", slug, key), drop: []string{f.fv.ID}, clear: []string{f.fv.ID}, run: func(ctx context.Context) error { return a.s.Backups.DeleteSchedule(ctx, b.ID) }})
	}
	for _, f := range imm("domains.") {
		f := f
		host, path := splitDomainID(f.fv.ID)
		add(&immStep{name: stepName("dns", slug, host+path), fields: []*fieldPlan{f}, run: func(ctx context.Context) error { return a.runDNS(ctx, id, f) }})
	}
}

// runImmediate runs the immediate steps in order; each is independent, a failure is reported and its
// ownership is not recorded, so the next plan still shows it.
func (a *applier) runImmediate(ctx context.Context, res *ApplyResult) {
	for _, st := range a.imm {
		err := st.run(ctx)
		switch {
		case err == nil:
			if mErr := a.s.markOwned(ctx, st.serviceID, st.fields, st.drop, nil, st.clear); mErr != nil {
				a.step(st.name, StepFailed, "applied, but its ownership was not recorded: "+mErr.Error())
				continue
			}
			a.changed[st.slug] = true
			a.step(st.name, StepApplied, "")
		case st.deferrable && isDeferrable(err):
			msg := err.Error()
			if mErr := a.s.markOwned(ctx, st.serviceID, nil, nil, st.pending, nil); mErr != nil {
				msg += "; the pending record failed: " + mErr.Error()
			}
			a.p.public.Diagnostics = append(a.p.public.Diagnostics, Diagnostic{
				Severity: projectconfig.SeverityInfo, Code: "deferred", Message: fmt.Sprintf("%s runs after the service is deployed: %s", st.name, err.Error()),
			})
			a.step(st.name, StepDeferred, msg)
		default:
			a.step(st.name, StepFailed, err.Error())
		}
	}
	res.Steps = a.out.Steps
	res.Plan = a.p.public
}

func (a *applier) runScale(ctx context.Context, id uuid.UUID, fs []*fieldPlan) error {
	in := scaling.SetInput{}
	var ap scaling.AutoscalingPatch
	hasAP := false
	for _, f := range fs {
		switch f.fv.ID {
		case "scale.replicas":
			n, _ := asInt(f.fv.Value)
			in.Replicas = &n
		case "scale.autoscaling.enabled":
			b, _ := asBool(f.fv.Value)
			ap.Enabled, hasAP = &b, true
		case "scale.autoscaling.min":
			n, _ := asInt(f.fv.Value)
			ap.Min, hasAP = &n, true
		case "scale.autoscaling.max":
			n, _ := asInt(f.fv.Value)
			ap.Max, hasAP = &n, true
		case "scale.autoscaling.targetCpuPercent":
			n, _ := asInt(f.fv.Value)
			ap.TargetCPUPercent, hasAP = &n, true
		}
	}
	if hasAP {
		in.Autoscaling = &ap
	}
	_, err := a.s.Scaling.Set(ctx, id, in)
	return err
}

func (a *applier) runProcessScale(ctx context.Context, id uuid.UUID, process string, fs []*fieldPlan) error {
	in := scaling.SetInput{}
	var ap scaling.AutoscalingPatch
	hasAP := false
	for _, f := range fs {
		_, key, _ := splitProcessScaleID(f.fv.ID)
		switch key {
		case "replicas":
			n, _ := asInt(f.fv.Value)
			in.Replicas = &n
		case "autoscaling.enabled":
			b, _ := asBool(f.fv.Value)
			ap.Enabled, hasAP = &b, true
		case "autoscaling.min":
			n, _ := asInt(f.fv.Value)
			ap.Min, hasAP = &n, true
		case "autoscaling.max":
			n, _ := asInt(f.fv.Value)
			ap.Max, hasAP = &n, true
		case "autoscaling.targetCpuPercent":
			n, _ := asInt(f.fv.Value)
			ap.TargetCPUPercent, hasAP = &n, true
		}
	}
	if hasAP {
		in.Autoscaling = &ap
	}
	_, err := a.s.Scaling.SetProcess(ctx, id, process, in)
	return err
}

func (a *applier) runGit(ctx context.Context, sp *svcPlan, id uuid.UUID, fs []*fieldPlan) error {
	in := gitproviders.LinkInput{}
	linkSrc := sp.field("git.link")
	switch {
	case linkSrc != nil:
		l, _ := linkSrc.fv.Value.(projectconfig.GitLink)
		c, res := a.p.pc.refs().connection(l.ConnectionName)
		if res != refOK {
			return refErr("unknown_connection", "no git connection named %q", l.ConnectionName)
		}
		in.ConnectionID, in.Repo = c.ID, l.Repo
	case sp.ls != nil && sp.ls.Git != nil:
		in.ConnectionID, in.Repo = sp.ls.Git.ConnectionID, sp.ls.Git.RepoFullName
	default:
		return refErr("invalid_value", "auto_deploy and watch_paths need a git connection and repository")
	}
	if b := sp.field("config.gitBranch"); b != nil {
		in.Branch = asString(b.fv.Value)
	}
	for _, f := range fs {
		switch f.fv.ID {
		case "git.auto_deploy":
			v, _ := asBool(f.fv.Value)
			in.AutoDeploy = &v
		case "git.watch_paths":
			v, _ := asStrings(f.fv.Value)
			v = nonNil(append([]string(nil), v...))
			in.WatchPaths = &v
		}
	}
	_, err := a.s.Git.Link(ctx, id, in)
	return err
}

func (a *applier) runUptime(ctx context.Context, sp *svcPlan, id uuid.UUID, fs []*fieldPlan, reset bool) error {
	v := uptime.MonitorView{Enabled: true, Alerts: true, FailureThreshold: 3, RecoveryThreshold: 2}
	if !reset && sp.ls != nil && sp.ls.Monitor != nil {
		m := sp.ls.Monitor
		v = uptime.MonitorView{Enabled: m.Enabled, Alerts: m.Alerts, FailureThreshold: int(m.FailureThreshold), RecoveryThreshold: int(m.RecoveryThreshold), ReminderMinutes: int(m.ReminderMinutes)}
	}
	if !reset {
		// The monitor row is replaced as a whole: overlay every declared key, not only the changed ones.
		for _, f := range sp.fields {
			if f.skipped || !strings.HasPrefix(f.fv.ID, "uptime.") {
				continue
			}
			n, nok := asInt(f.fv.Value)
			b, bok := asBool(f.fv.Value)
			switch f.fv.ID {
			case "uptime.enabled":
				v.Enabled = b && bok
			case "uptime.alerts":
				v.Alerts = b && bok
			case "uptime.failureThreshold":
				if nok {
					v.FailureThreshold = n
				}
			case "uptime.recoveryThreshold":
				if nok {
					v.RecoveryThreshold = n
				}
			case "uptime.reminderMinutes":
				if nok {
					v.ReminderMinutes = n
				}
			}
		}
	}
	_, err := a.s.Uptime.UpdateMonitor(ctx, id, v)
	return err
}

func (a *applier) runTask(ctx context.Context, sp *svcPlan, id uuid.UUID, f *fieldPlan) error {
	v, ok := f.fv.Value.(projectconfig.TaskValue)
	if !ok {
		return refErr("wrong_type", "task has an unexpected value")
	}
	name := strings.TrimPrefix(f.fv.ID, "tasks.")
	if sp.ls != nil {
		if cur, exists := sp.ls.Tasks[name]; exists {
			_, err := a.s.Tasks.UpdateTask(ctx, cur.ID, tasks.TaskPatch{
				Schedule: &v.Schedule, Timezone: &v.Timezone, Command: &v.Command, Enabled: &v.Enabled,
				TimeoutSeconds: &v.TimeoutSeconds, Concurrency: &v.Concurrency,
			})
			return err
		}
	}
	_, err := a.s.Tasks.CreateTask(ctx, id, tasks.TaskInput{
		Name: name, Schedule: v.Schedule, Timezone: v.Timezone, Command: v.Command, Enabled: &v.Enabled,
		TimeoutSeconds: v.TimeoutSeconds, Concurrency: v.Concurrency,
	})
	return err
}

func (a *applier) runBackup(ctx context.Context, sp *svcPlan, id uuid.UUID, f *fieldPlan) error {
	v, ok := f.fv.Value.(projectconfig.BackupValue)
	if !ok {
		return refErr("wrong_type", "backup has an unexpected value")
	}
	return a.s.applyBackup(ctx, sp.ls, id, v, a.p.pc.refs())
}

// applyBackup creates or updates the schedule with the key of v (also used to replay a pending step).
func (s *Service) applyBackup(ctx context.Context, ls *liveService, serviceID uuid.UUID, v projectconfig.BackupValue, rf *refs) error {
	d, res := rf.destination(v.DestinationName)
	if res != refOK {
		return refErr("unknown_destination", "no backup destination named %q", v.DestinationName)
	}
	if ls != nil {
		if cur, exists := ls.backupByKey(v.Key); exists {
			_, err := s.Backups.UpdateSchedule(ctx, cur.ID, backups.SchedulePatch{
				DestinationID: &d.ID, Cron: &v.Cron, Timezone: &v.Timezone, Enabled: &v.Enabled,
				RetentionCount: &v.RetentionCount, RetentionDays: &v.RetentionDays,
			})
			if err != nil {
				return err
			}
			if cur.ConfigKey == nil { // a schedule made elsewhere that the file adopts under its key
				key := v.Key
				return s.q.SetBackupScheduleConfigKey(ctx, db.SetBackupScheduleConfigKeyParams{ID: cur.ID, ConfigKey: &key})
			}
			return nil
		}
	}
	view, err := s.Backups.CreateSchedule(ctx, serviceID, backups.ScheduleInput{
		DestinationID: d.ID, Cron: v.Cron, Timezone: v.Timezone, Enabled: &v.Enabled,
		RetentionCount: &v.RetentionCount, RetentionDays: &v.RetentionDays,
	})
	if err != nil {
		return err
	}
	key := v.Key
	if err := s.q.SetBackupScheduleConfigKey(ctx, db.SetBackupScheduleConfigKeyParams{ID: view.ID, ConfigKey: &key}); err != nil {
		_ = s.Backups.DeleteSchedule(ctx, view.ID) // an unkeyed schedule would be unmanaged and duplicated by the next apply
		return fmt.Errorf("record the schedule key %q: %w", key, err)
	}
	return nil
}

func (a *applier) runDNS(ctx context.Context, serviceID uuid.UUID, f *fieldPlan) error {
	c, ok := f.fv.Value.(projectconfig.CloudflareRule)
	if !ok {
		return refErr("wrong_type", "cloudflare settings have an unexpected value")
	}
	host, path := splitDomainID(f.fv.ID)
	rows, err := a.s.q.ListServiceDomains(ctx, serviceID)
	if err != nil {
		return err
	}
	for _, d := range rows {
		if strings.EqualFold(d.Hostname, host) && d.PathPrefix == path {
			_, err := a.s.Cloudflare.SetSettings(ctx, d.ID, cloudflare.SettingsInput{Proxied: c.Proxied, OriginLock: c.OriginLock})
			return err
		}
	}
	return refErr("invalid_value", "the domain %s%s does not exist", host, path)
}

// deploy deploys the services this apply touched that now have staged changes.
func (a *applier) deploy(ctx context.Context, res *ApplyResult) {
	if a.s.Deploy == nil {
		a.step("deploy", StepFailed, "this server build cannot deploy")
		res.Steps = a.out.Steps
		return
	}
	var ids []uuid.UUID
	slugs := make([]string, 0, len(a.changed))
	for s := range a.changed {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		row, ok := a.rows[slug]
		if !ok {
			continue
		}
		sc, err := a.s.loadScopeInternal(ctx, row.ID)
		if err != nil {
			continue
		}
		if sc.Service.Generation > sc.Service.AppliedGeneration {
			ids = append(ids, row.ID)
		}
	}
	if len(ids) == 0 {
		a.step("deploy", StepUnchanged, "nothing to deploy")
		res.Steps = a.out.Steps
		return
	}
	out, err := a.s.Deploy.DeployStaged(ctx, a.p.env.Env.ID, ids, "nebula.toml apply")
	if err != nil {
		a.step("deploy", StepFailed, err.Error())
		res.Steps = a.out.Steps
		return
	}
	for _, it := range out.Items {
		res.Deployments = append(res.Deployments, it.ID)
	}
	a.step("deploy", StepApplied, "")
	res.Steps = a.out.Steps
}
