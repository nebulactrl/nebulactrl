package configsync

import (
	"encoding/json"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
)

// scaleOps handles scale.replicas and scale.autoscaling.* (immediate).
type scaleOps struct{}

func (scaleOps) live(_ *planCtx, ls *liveService, id, _ string) (fieldView, bool) {
	var v any
	switch id {
	case "scale.replicas":
		v = int(ls.Row.Replicas)
	case "scale.autoscaling.enabled":
		v = ls.Scale.Enabled
	case "scale.autoscaling.min":
		v = ls.Scale.Min
	case "scale.autoscaling.max":
		v = ls.Scale.Max
	case "scale.autoscaling.targetCpuPercent":
		v = ls.Scale.TargetCPUPercent
	default:
		return fieldView{}, false
	}
	return view(v, display(v)), true
}

func (scaleOps) desired(_ *planCtx, _ *liveService, fv projectconfig.FieldValue) (fieldView, error) {
	if b, ok := fv.Value.(bool); ok {
		return view(b, display(b)), nil
	}
	n, ok := asInt(fv.Value)
	if !ok {
		return fieldView{}, refErr("wrong_type", "%s must be a number", fv.ID)
	}
	return view(n, display(n)), nil
}

// uptimeOps handles uptime.* (immediate; a service without a monitor row has the defaults).
type uptimeOps struct{}

func (uptimeOps) live(_ *planCtx, ls *liveService, id, _ string) (fieldView, bool) {
	enabled, alerts, fail, rec, rem := true, true, 3, 2, 0
	if m := ls.Monitor; m != nil {
		enabled, alerts, fail, rec, rem = m.Enabled, m.Alerts, int(m.FailureThreshold), int(m.RecoveryThreshold), int(m.ReminderMinutes)
	}
	var v any
	switch id {
	case "uptime.enabled":
		v = enabled
	case "uptime.alerts":
		v = alerts
	case "uptime.failureThreshold":
		v = fail
	case "uptime.recoveryThreshold":
		v = rec
	case "uptime.reminderMinutes":
		v = rem
	default:
		return fieldView{}, false
	}
	return view(v, display(v)), true
}

func (uptimeOps) desired(_ *planCtx, _ *liveService, fv projectconfig.FieldValue) (fieldView, error) {
	if b, ok := fv.Value.(bool); ok {
		return view(b, display(b)), nil
	}
	n, ok := asInt(fv.Value)
	if !ok {
		return fieldView{}, refErr("wrong_type", "%s must be a number", fv.ID)
	}
	return view(n, display(n)), nil
}

// gitOps handles git.link, git.auto_deploy and git.watch_paths (immediate).
type gitOps struct{}

func (gitOps) live(pc *planCtx, ls *liveService, id, _ string) (fieldView, bool) {
	g := ls.Git
	if g == nil {
		return fieldView{}, false
	}
	switch id {
	case "git.link":
		return view(map[string]any{"connection": g.ConnectionID.String(), "repo": strings.ToLower(g.RepoFullName)}, ls.GitConn+":"+g.RepoFullName), true
	case "git.auto_deploy":
		return view(g.AutoDeploy, display(g.AutoDeploy)), true
	case "git.watch_paths":
		wp := nonNil(g.WatchPaths)
		return view(wp, display(wp)), true
	}
	return fieldView{}, false
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (gitOps) desired(pc *planCtx, _ *liveService, fv projectconfig.FieldValue) (fieldView, error) {
	switch fv.ID {
	case "git.link":
		l, ok := fv.Value.(projectconfig.GitLink)
		if !ok {
			return fieldView{}, refErr("wrong_type", "git link has an unexpected value")
		}
		c, res := pc.refs().connection(l.ConnectionName)
		switch res {
		case refNone:
			return fieldView{}, refErr("unknown_connection", "no git connection named %q", l.ConnectionName)
		case refAmbiguous:
			return fieldView{}, refErr("ambiguous_reference", "several git connections are named %q", l.ConnectionName)
		}
		return view(map[string]any{"connection": c.ID.String(), "repo": strings.ToLower(l.Repo)}, c.Name+":"+l.Repo), nil
	case "git.auto_deploy":
		b, _ := asBool(fv.Value)
		return view(b, display(b)), nil
	case "git.watch_paths":
		wp, _ := asStrings(fv.Value)
		wp = nonNil(wp)
		return view(wp, display(wp)), nil
	}
	return fieldView{}, refErr("invalid_value", "unknown field %s", fv.ID)
}

// taskOps handles tasks.<name> (immediate).
type taskOps struct{}

func taskValueOf(t liveTask) projectconfig.TaskValue {
	var cmd []string
	_ = json.Unmarshal(t.Command, &cmd)
	return projectconfig.TaskValue{
		Name: t.Name, Schedule: t.Schedule, Timezone: t.Timezone, Command: nonNil(cmd), Enabled: t.Enabled,
		TimeoutSeconds: int(t.TimeoutSeconds), Concurrency: t.Concurrency,
	}
}

func (taskOps) live(_ *planCtx, ls *liveService, id, _ string) (fieldView, bool) {
	t, ok := ls.Tasks[strings.TrimPrefix(id, "tasks.")]
	if !ok {
		return fieldView{}, false
	}
	v := taskValueOf(t)
	return view(v, taskDisplay(v)), true
}

func taskDisplay(v projectconfig.TaskValue) string {
	state := ""
	if !v.Enabled {
		state = " (disabled)"
	}
	return v.Schedule + " " + v.Timezone + " " + display(v.Command) + state
}

func (taskOps) desired(_ *planCtx, _ *liveService, fv projectconfig.FieldValue) (fieldView, error) {
	v, ok := fv.Value.(projectconfig.TaskValue)
	if !ok {
		return fieldView{}, refErr("wrong_type", "task %s has an unexpected value", fv.ID)
	}
	v.Command = nonNil(v.Command)
	if v.Timezone == "" {
		v.Timezone = "UTC"
	}
	return view(v, taskDisplay(v)), nil
}

// backupOps handles backups.<key> (immediate); identity is backup_schedules.config_key.
type backupOps struct{}

func (ls *liveService) backupByKey(key string) (liveBackup, bool) {
	b, ok := ls.backupKeys()[key]
	return b, ok
}

func (backupOps) live(pc *planCtx, ls *liveService, id, _ string) (fieldView, bool) {
	key := strings.TrimPrefix(id, "backups.")
	b, ok := ls.backupByKey(key)
	if !ok {
		return fieldView{}, false
	}
	dest := b.DestinationID.String()
	if d, ok := pc.refs().destinationByID(b.DestinationID); ok {
		dest = d.Name
	}
	v := projectconfig.BackupValue{
		Key: key, DestinationName: strings.ToLower(dest), Cron: b.Cron, Timezone: b.Timezone, Enabled: b.Enabled,
		RetentionCount: int(b.RetentionCount), RetentionDays: int(b.RetentionDays),
	}
	return view(v, backupDisplay(v)), true
}

func backupDisplay(v projectconfig.BackupValue) string {
	return v.Cron + " " + v.Timezone + " → " + v.DestinationName
}

func (backupOps) desired(pc *planCtx, _ *liveService, fv projectconfig.FieldValue) (fieldView, error) {
	v, ok := fv.Value.(projectconfig.BackupValue)
	if !ok {
		return fieldView{}, refErr("wrong_type", "backup %s has an unexpected value", fv.ID)
	}
	d, res := pc.refs().destination(v.DestinationName)
	switch res {
	case refNone:
		return fieldView{}, refErr("unknown_destination", "no backup destination named %q", v.DestinationName)
	case refAmbiguous:
		return fieldView{}, refErr("ambiguous_reference", "several backup destinations are named %q", v.DestinationName)
	}
	v.DestinationName = strings.ToLower(d.Name)
	if v.Timezone == "" {
		v.Timezone = "UTC"
	}
	return view(v, backupDisplay(v)), nil
}
