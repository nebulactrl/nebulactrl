package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const execOutputLimit = 64 << 10

// limitedBuffer keeps the first execOutputLimit bytes written to it.
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := execOutputLimit - b.Len(); room > 0 {
		if len(p) > room {
			b.Buffer.Write(p[:room])
		} else {
			b.Buffer.Write(p)
		}
	}
	return len(p), nil
}

func dbAdminResult(id string, outcome string, res agentprotocol.DBAdminResult, errMsg string) agentprotocol.ResultFrame {
	data, _ := json.Marshal(res)
	return agentprotocol.ResultFrame{CommandID: id, Outcome: outcome, Data: data, Error: errMsg}
}

// handleDBAdmin runs a typed administrative action in a database container.
// The control plane names the action; the argv comes from the engine catalog.
func (e *Engine) handleDBAdmin(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.DBAdminPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid db_admin payload: "+err.Error())
	}
	if p.ServiceID == "" {
		return failedResult(cmd.ID, "serviceId is required")
	}
	spec := engines.Spec{Engine: p.Engine, Version: p.Version, Flavor: p.Flavor, DatabaseName: p.Database, Username: p.User}
	if engines.Lookup(spec.Engine) == nil {
		return failedResult(cmd.ID, "unknown database engine "+p.Engine)
	}
	rd := newRedactor(cmd.Secrets, nil)
	if strings.HasPrefix(p.Action, "repl_") {
		return e.handleReplAdmin(ctx, cmd, p, spec, rd)
	}
	creds := engines.Credentials{
		User: p.User, Database: p.Database,
		Password:     cmd.Secrets[agentprotocol.SecretDBAdminPassword],
		RootPassword: cmd.Secrets[agentprotocol.SecretDBAdminRootPassword],
	}
	var plan engines.ExecPlan
	var err error
	switch p.Action {
	case agentprotocol.DBActionRotatePassword:
		plan, err = engines.RotatePlan(spec, creds, cmd.Secrets[agentprotocol.SecretDBAdminNewPassword])
	case agentprotocol.DBActionInfo:
		plan, err = engines.InfoPlan(spec, creds)
	default:
		return failedResult(cmd.ID, "unsupported db_admin action "+p.Action)
	}
	if err != nil {
		return dbAdminResult(cmd.ID, agentprotocol.OutcomeFailed, agentprotocol.DBAdminResult{Action: p.Action}, rd.apply(err.Error()))
	}

	defer e.lockService(p.ServiceID)()
	reps := e.currentReplicas(ctx, p.ServiceID)
	var target *docker.ContainerInfo
	for i := range reps {
		if reps[i].Running {
			target = &reps[i]
			break
		}
	}
	if target == nil {
		return dbAdminResult(cmd.ID, agentprotocol.OutcomeFailed, agentprotocol.DBAdminResult{Action: p.Action},
			"the database container is not running on this host")
	}

	out, err := e.execPlanIn(ctx, target.ID, plan, rd, 2*time.Minute)
	if err != nil {
		return dbAdminResult(cmd.ID, agentprotocol.OutcomeFailed, agentprotocol.DBAdminResult{Action: p.Action}, err.Error())
	}

	res := agentprotocol.DBAdminResult{Action: p.Action}
	switch p.Action {
	case agentprotocol.DBActionRotatePassword:
		res.Message = "Password changed inside the database."
	case agentprotocol.DBActionInfo:
		info, err := engines.ParseInfo(spec.Engine, out)
		if err != nil {
			return dbAdminResult(cmd.ID, agentprotocol.OutcomeFailed, res, rd.apply(err.Error()))
		}
		res.Info = &agentprotocol.DBInfo{Version: info.Version, SizeBytes: info.SizeBytes, Connections: info.Connections}
	}
	return dbAdminResult(cmd.ID, agentprotocol.OutcomeSucceeded, res, "")
}

const (
	upgradeTimeout   = 2 * time.Hour
	checksumNewOnly  = "does not use data checksums but the new one does"
	checksumOldOnly  = "uses data checksums but the new one does not"
	upgradeLogTail   = 4000
	upgradePollEvery = 500 * time.Millisecond
)

// handleDBUpgrade converts a PostgreSQL volume to a new major version with the
// pg_upgrade helper image. The database is stopped, the old volume is mounted
// as the source and a fresh volume receives the new cluster; the old volume is
// never modified. On failure the old container is started again.
func (e *Engine) handleDBUpgrade(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.DBUpgradePayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid db_upgrade payload: "+err.Error())
	}
	if p.ServiceID == "" || p.OldVolume == "" || p.NewVolume == "" || p.OldVolume == p.NewVolume {
		return failedResult(cmd.ID, "db_upgrade needs a service and distinct old and new volumes")
	}
	if want := engines.PGUpgradeImage(p.FromVersion, p.ToVersion); p.HelperImage != want {
		return failedResult(cmd.ID, "unexpected pg_upgrade helper image "+p.HelperImage)
	}
	defer e.lockService(p.ServiceID)()

	d := e.cfg.Docker
	oldVol := agentprotocol.VolumeName(p.ServiceID, p.OldVolume)
	newVol := agentprotocol.VolumeName(p.ServiceID, p.NewVolume)
	layout := engines.PGUpgradeLayoutFor(p.FromVersion, p.ToVersion)

	// Stop the database; remember what to restart if the upgrade fails.
	reps := e.currentReplicas(ctx, p.ServiceID)
	grace := graceOf(p.StopGraceSeconds)
	var stopped []docker.ContainerInfo
	restart := func() {
		rctx, cancel := detached(ctx, time.Minute)
		defer cancel()
		for _, c := range stopped {
			if err := d.ContainerStart(rctx, c.ID); err != nil {
				e.log.Warn("restart database after failed upgrade", "container", c.Name, "err", err)
			}
		}
	}
	for _, c := range reps {
		if c.Running || c.Restarting {
			if err := d.ContainerStop(ctx, c.ID, &grace); err != nil && !errors.Is(err, docker.ErrNotFound) {
				restart()
				return failedResult(cmd.ID, "stop database "+c.Name+": "+err.Error())
			}
			stopped = append(stopped, c)
		}
	}
	fail := func(msg, logTail string) agentprotocol.ResultFrame {
		restart()
		data, _ := json.Marshal(agentprotocol.DBUpgradeResult{Message: msg, Log: logTail})
		return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeFailed, Data: data, Error: msg}
	}

	if err := d.ImagePull(ctx, p.HelperImage, "", nil); err != nil {
		return fail("pull "+p.HelperImage+": "+err.Error(), "")
	}
	if err := d.EnsureVolume(ctx, oldVol, nil); err != nil {
		return fail("old volume: "+err.Error(), "")
	}

	// initdb must match the old cluster's checksum setting (PostgreSQL 18 turned
	// checksums on by default); retry once with the other setting on the exact
	// pg_upgrade complaint.
	attempts := []string{"", "--no-data-checksums", "--data-checksums"}
	var lastLog string
	tried := 0
	for _, initdbArgs := range attempts {
		if tried > 0 {
			if initdbArgs == "--no-data-checksums" && !strings.Contains(lastLog, checksumNewOnly) {
				continue
			}
			if initdbArgs == "--data-checksums" && !strings.Contains(lastLog, checksumOldOnly) {
				continue
			}
		}
		tried++
		// A fresh target volume per attempt.
		_ = d.RemoveVolume(ctx, newVol, true)
		if err := d.EnsureVolume(ctx, newVol, nil); err != nil {
			return fail("new volume: "+err.Error(), "")
		}
		code, log, err := e.runUpgradeHelper(ctx, p, oldVol, newVol, layout, initdbArgs)
		lastLog = log
		if err != nil {
			_ = d.RemoveVolume(ctx, newVol, true)
			return fail(err.Error(), tail(log))
		}
		if code == 0 {
			data, _ := json.Marshal(agentprotocol.DBUpgradeResult{
				Message: fmt.Sprintf("Converted the data to PostgreSQL %s in volume %s. The previous volume is untouched.", p.ToVersion, p.NewVolume),
				Log:     tail(log),
			})
			e.log.Info("pg_upgrade finished", "service_id", p.ServiceID, "from", p.FromVersion, "to", p.ToVersion)
			return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: data}
		}
	}
	_ = d.RemoveVolume(ctx, newVol, true)
	return fail("pg_upgrade failed; the database was not changed", tail(lastLog))
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > upgradeLogTail {
		return "…" + s[len(s)-upgradeLogTail:]
	}
	return s
}

// runUpgradeHelper runs one pg_upgrade attempt in a throwaway container and
// returns its exit code and output.
func (e *Engine) runUpgradeHelper(ctx context.Context, p agentprotocol.DBUpgradePayload, oldVol, newVol string,
	l engines.PGUpgradeLayout, initdbArgs string) (int, string, error) {
	d := e.cfg.Docker
	env := []string{"PGDATAOLD=" + l.OldData, "PGDATANEW=" + l.NewData}
	if initdbArgs != "" {
		env = append(env, "POSTGRES_INITDB_ARGS="+initdbArgs)
	}
	name := "nc-pgupgrade-" + agentprotocol.Short(p.ServiceID) + "-" + agentprotocol.Short(p.UpgradeID)
	_ = d.ContainerRemove(ctx, name, true, false)
	id, err := d.ContainerCreate(ctx, docker.ContainerSpec{
		Name: name, Image: p.HelperImage, Env: env,
		Labels: map[string]string{agentprotocol.LabelManaged: "pg-upgrade", agentprotocol.LabelService: p.ServiceID},
		Mounts: []docker.VolumeMount{{Volume: oldVol, Target: l.OldMount}, {Volume: newVol, Target: l.NewMount}},
	})
	if err != nil {
		return 0, "", fmt.Errorf("create pg_upgrade container: %w", err)
	}
	defer func() {
		cctx, cancel := detached(ctx, time.Minute)
		defer cancel()
		_ = d.ContainerRemove(cctx, id, true, false)
	}()
	if err := d.ContainerStart(ctx, id); err != nil {
		return 0, "", fmt.Errorf("start pg_upgrade container: %w", err)
	}
	deadline := time.Now().Add(upgradeTimeout)
	for {
		info, err := d.ContainerInspect(ctx, id)
		if err != nil {
			return 0, "", fmt.Errorf("inspect pg_upgrade container: %w", err)
		}
		if !info.Running && !info.Restarting && info.State != "created" {
			log := e.readContainerLog(ctx, id)
			return info.ExitCode, log, nil
		}
		if time.Now().After(deadline) {
			return 0, e.readContainerLog(ctx, id), errors.New("pg_upgrade timed out")
		}
		select {
		case <-ctx.Done():
			return 0, "", ctx.Err()
		case <-time.After(upgradePollEvery):
		}
	}
}

func (e *Engine) readContainerLog(ctx context.Context, id string) string {
	rc, err := e.cfg.Docker.ContainerLogs(ctx, id, docker.LogOptions{Tail: -1})
	if err != nil {
		return ""
	}
	defer rc.Close()
	var b limitedBuffer
	_, _ = b.ReadFrom(rc)
	return b.String()
}
