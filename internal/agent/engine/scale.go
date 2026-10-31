package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/logs"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// handleScale adds or removes replicas of a service's current deployment
// without pulling or building anything: new replicas reuse the image of a
// running replica. New replicas are health-checked before they join the
// upstreams; removed replicas leave the upstreams first, then drain, stop and
// are removed.
func (e *Engine) handleScale(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var sp agentprotocol.ScaleServicePayload
	if err := json.Unmarshal(cmd.Payload, &sp); err != nil {
		return failedResult(cmd.ID, "invalid scale_service payload: "+err.Error())
	}
	switch {
	case sp.ServiceID == "":
		return failedResult(cmd.ID, "invalid scale_service payload: serviceId is required")
	case sp.DeploymentID == "":
		return failedResult(cmd.ID, "invalid scale_service payload: deploymentId is required")
	case sp.Replicas < 1 || sp.Replicas > maxReplicas:
		return failedResult(cmd.ID, fmt.Sprintf("invalid scale_service payload: replicas %d is out of range", sp.Replicas))
	case sp.Deploy.ServiceSlug == "" || sp.Deploy.EnvironmentID == "":
		return failedResult(cmd.ID, "invalid scale_service payload: deploy.serviceSlug and deploy.environmentId are required")
	}
	p := sp.Deploy
	p.ServiceID, p.DeploymentID, p.Replicas = sp.ServiceID, sp.DeploymentID, sp.Replicas
	if len(p.Processes) > 0 || sp.Process != "" {
		projected, err := projectProcessByName(p, sp.Process)
		if err != nil {
			return failedResult(cmd.ID, "invalid scale_service payload: "+err.Error())
		}
		p = projected
		p.Replicas = sp.Replicas
	}

	unlock := e.lockService(p.ServiceID)
	defer unlock()
	if res, fenced := e.fencedResult(cmd.ID, sp.ServiceID); fenced {
		return res
	}

	d := &deploy{
		e: e, cmd: cmd, p: p, red: newRedactor(cmd.Secrets, secretKeysForRedaction(p)),
		silent: true, live: true, scaledAt: sp.ScaledAt,
		log: e.log.With("correlation_id", p.DeploymentID, "service_id", p.ServiceID),
	}
	d.log.Info("scale started", "replicas", p.Replicas)
	res := d.scale(ctx)
	d.log.Info("scale finished", "outcome", res.Outcome, "replicas", p.Replicas)
	return deployResultFrame(cmd.ID, res)
}

// slot is one replica container of the current deployment.
type slot struct {
	idx  int
	name string
	info docker.ContainerInfo
}

func (d *deploy) scale(ctx context.Context) agentprotocol.DeployResult {
	e := d.e
	target := d.p.Replicas
	if target > 1 {
		if pr := d.proc(); pr != nil {
			if len(d.p.Volumes) > 0 || pr.Singleton {
				return d.fail("Replicas need a stateless process: no volumes, and not a singleton")
			}
		} else if len(d.p.Volumes) > 0 || d.p.Port <= 0 {
			return d.fail("Replicas need a stateless service: no volumes and an HTTP port")
		}
	}

	all, err := e.containersOf(ctx, d.p.ServiceID, true)
	if err != nil {
		return d.fail("list containers: " + err.Error())
	}
	var live, stale []slot
	for _, c := range all {
		if c.Labels[agentprotocol.LabelDeployment] != d.p.DeploymentID {
			continue // another deployment: not ours to touch here
		}
		if pr := d.proc(); pr != nil && !inProcess(c, pr.Name) {
			continue // another process of this deployment
		}
		s := slot{idx: replicaIndex(c), name: c.Name, info: c}
		if c.Running {
			live = append(live, s)
		} else {
			stale = append(stale, s)
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].idx < live[j].idx })
	if len(live) == 0 {
		return d.fail("No running replica of deployment " + agentprotocol.Short(d.p.DeploymentID) +
			" on this host; deploy the service first")
	}

	st := &deployState{}
	switch {
	case target > len(live):
		return d.scaleUp(ctx, st, live, stale)
	default:
		return d.scaleDown(ctx, st, live, stale, target)
	}
}

// useSlots makes the given replicas the deploy's final set (names, ids).
func (d *deploy) useSlots(slots []slot) {
	d.names = make([]string, len(slots))
	d.ids = make([]string, len(slots))
	d.index = nil
	for i, s := range slots {
		d.names[i], d.ids[i] = s.name, s.info.ID
	}
}

func (d *deploy) scaleUp(ctx context.Context, st *deployState, live, stale []slot) agentprotocol.DeployResult {
	target := d.p.Replicas
	env := d.cloneEnv
	if env == nil {
		var err error
		if env, err = d.buildEnv(); err != nil {
			return d.fail(err.Error())
		}
	}

	// The lowest free replica indices keep the set contiguous (0..n-1), which
	// is what the control plane's desired container names assume.
	taken := map[int]bool{}
	for _, s := range live {
		taken[s.idx] = true
	}
	var newIdx []int
	for i := 0; len(live)+len(newIdx) < target; i++ {
		if !taken[i] {
			newIdx = append(newIdx, i)
		}
	}
	newNames := make([]string, len(newIdx))
	for i, idx := range newIdx {
		newNames[i] = d.containerName(idx)
	}

	// Containers of this deployment that are not running and sit on a wanted
	// name would collide with the new ones.
	for _, s := range stale {
		for _, n := range newNames {
			if s.name == n {
				d.logf("Removing stopped container %s", s.name)
				d.removeContainer(ctx, s.info.ID)
			}
		}
	}

	d.imageID = live[0].info.ImageID
	d.names, d.index, d.ids = newNames, newIdx, make([]string, len(newNames))
	d.log.Info("scaling up", "from", len(live), "to", target, "new", newNames)

	if failedID, err := d.createAndStartAll(ctx, env, make([]bool, len(newNames))); err != nil {
		return d.scaleAbort(ctx, st, failedID, err.Error())
	}
	if failedID, reason, ok := d.healthPhase(ctx, d.ids); !ok {
		return d.scaleAbort(ctx, st, failedID, reason)
	}

	// Final set, ordered by replica index: the running replicas plus the new ones.
	final := append([]slot(nil), live...)
	for i, idx := range newIdx {
		final = append(final, slot{idx: idx, name: newNames[i], info: docker.ContainerInfo{ID: d.ids[i], Name: newNames[i]}})
	}
	sort.Slice(final, func(i, j int) bool { return final[i].idx < final[j].idx })
	d.useSlots(final)

	if reason, ok := d.scaleRoutePhase(ctx, st); !ok {
		if ctx.Err() != nil {
			return d.fail("agent stopped during scaling")
		}
		return d.scaleAbort(ctx, st, "", reason)
	}
	d.created = nil // the new replicas are now part of the set
	d.recordCurrent()
	return d.result(agentprotocol.OutcomeSucceeded,
		fmt.Sprintf("Scaled from %d to %d replicas", len(live), len(final)), d.firstID())
}

// scaleAbort removes the replicas this attempt created and reports a failed
// scale: the existing replicas keep serving untouched. If restoring the
// previous routes failed, the new replicas may still be referenced and stay.
func (d *deploy) scaleAbort(ctx context.Context, st *deployState, failedID, reason string) agentprotocol.DeployResult {
	cctx, cancel := detached(ctx, 2*time.Minute)
	defer cancel()

	tailID := failedID
	if tailID == "" && len(d.created) > 0 {
		tailID = d.created[0]
	}
	var tail []string
	if tailID != "" {
		tail, _ = logs.Tail(cctx, d.e.cfg.Docker, tailID, failureLogLines)
	}

	var msg strings.Builder
	msg.WriteString(reason)
	if st.routeErr != nil {
		msg.WriteString("\nCould not restore the previous routes: " + st.routeErr.Error() +
			". The new replicas were kept; check `docker ps --filter label=nebulactrl.service=" + d.p.ServiceID + "`.")
	} else {
		d.removeCreated(cctx)
		msg.WriteString("\nNo replicas were added; the existing replicas are still serving.")
	}
	if len(tail) > 0 {
		msg.WriteString(fmt.Sprintf("\n\nLast %d container log lines:\n%s", len(tail), strings.Join(tail, "\n")))
	}
	d.log.Warn("scale failed", "reason", d.red.apply(reason))
	return d.fail(msg.String())
}

func (d *deploy) scaleDown(ctx context.Context, st *deployState, live, stale []slot, target int) agentprotocol.DeployResult {
	e := d.e
	keep, drop := live[:target], live[target:]
	d.useSlots(keep)
	d.log.Info("scaling down", "from", len(live), "to", target)

	// 1. Leave the upstreams. A failed apply or verification leaves every
	// replica running (routePhase has already restored the previous table).
	if reason, ok := d.scaleRoutePhase(ctx, st); !ok {
		if ctx.Err() != nil {
			return d.fail("agent stopped during scaling")
		}
		msg := reason + "\nNo replicas were removed."
		if st.routeErr != nil {
			msg = reason + "\nCould not restore the previous routes: " + st.routeErr.Error()
		}
		return d.fail(msg)
	}

	// 2. Drain requests already routed to the leaving replicas, then stop them.
	if len(drop) > 0 && len(d.verifiableRoutes(st.prevRoutes)) > 0 {
		select {
		case <-time.After(e.cfg.DrainDelay):
		case <-ctx.Done():
		}
	}
	grace := graceOf(d.p.StopGraceSeconds)
	cctx, cancel := detached(ctx, grace+2*time.Minute)
	defer cancel()
	leaving := append(append([]slot(nil), drop...), stale...)
	var (
		errs  []string
		errMu sync.Mutex
	)
	addErr := func(msg string) {
		errMu.Lock()
		errs = append(errs, msg)
		errMu.Unlock()
	}
	parallel(len(leaving), func(i int) {
		s := leaving[i]
		if s.info.Running || s.info.Restarting {
			if err := e.cfg.Docker.ContainerStop(cctx, s.info.ID, &grace); err != nil && !errors.Is(err, docker.ErrNotFound) {
				addErr(fmt.Sprintf("stop %s: %v", s.name, err))
			}
		}
		if err := e.cfg.Docker.ContainerRemove(cctx, s.info.ID, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
			addErr(fmt.Sprintf("remove %s: %v", s.name, err))
		}
	})
	d.recordCurrent()
	if len(errs) > 0 {
		return d.fail("Replicas left the proxy but cleanup was incomplete: " + strings.Join(errs, "; "))
	}
	return d.result(agentprotocol.OutcomeSucceeded,
		fmt.Sprintf("Scaled from %d to %d replicas", len(live), len(keep)), d.firstID())
}
