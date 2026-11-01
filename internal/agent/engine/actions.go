package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/logs"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// containersOf lists the managed containers of a service.
func (e *Engine) containersOf(ctx context.Context, serviceID string, all bool) ([]docker.ContainerInfo, error) {
	return e.cfg.Docker.ContainerList(ctx, docker.ListFilter{
		All: all,
		Labels: map[string]string{
			agentprotocol.LabelManaged: "true",
			agentprotocol.LabelService: serviceID,
		},
	})
}

// replicaIndex is the replica index of a managed container: its replica label,
// else the numeric suffix of its name, else 0 (a container named without a replica index).
func replicaIndex(c docker.ContainerInfo) int {
	if v, ok := c.Labels[agentprotocol.LabelReplica]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	if i := strings.LastIndexByte(c.Name, '-'); i >= 0 {
		if n, err := strconv.Atoi(c.Name[i+1:]); err == nil && n >= 0 && n < agentprotocol.MaxReplicas {
			return n
		}
	}
	return 0
}

func sortByReplica(cs []docker.ContainerInfo) {
	sort.SliceStable(cs, func(i, j int) bool { return replicaIndex(cs[i]) < replicaIndex(cs[j]) })
}

// currentReplicas returns the replica containers of the service's current
// deployment: the ones named by the latest local deploy or the control plane's
// desired state, otherwise those of the newest running (then newest)
// deployment carrying the service label. A multi-process service yields the
// union of its processes, process by process (the entry's order), each ordered
// by replica index.
func (e *Engine) currentReplicas(ctx context.Context, serviceID string) []docker.ContainerInfo {
	e.mu.Lock()
	var procs []desiredProcess
	if l, ok := e.local[serviceID]; ok {
		procs = l.procs()
	} else if d, ok := e.desired[serviceID]; ok {
		procs = d.procs()
	}
	e.mu.Unlock()
	var out []docker.ContainerInfo
	for _, pr := range procs {
		var group []docker.ContainerInfo
		for _, n := range pr.ContainerNames {
			if info, err := e.cfg.Docker.ContainerInspect(ctx, n); err == nil {
				group = append(group, info)
			}
		}
		sortByReplica(group)
		out = append(out, group...)
	}
	if len(out) > 0 {
		return out
	}
	cs, err := e.containersOf(ctx, serviceID, true)
	if err != nil || len(cs) == 0 {
		return nil
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Running != cs[j].Running {
			return cs[i].Running
		}
		return cs[i].Created.After(cs[j].Created)
	})
	dep := cs[0].Labels[agentprotocol.LabelDeployment]
	for _, c := range cs {
		if c.Labels[agentprotocol.LabelDeployment] == dep {
			out = append(out, c)
		}
	}
	sortByProcessReplica(out)
	return out
}

// currentProcessReplicas is currentReplicas narrowed to one process; "" is the
// default process, which also matches containers that carry no process label.
func (e *Engine) currentProcessReplicas(ctx context.Context, serviceID, process string) []docker.ContainerInfo {
	var out []docker.ContainerInfo
	for _, c := range e.currentReplicas(ctx, serviceID) {
		if inProcess(c, process) {
			out = append(out, c)
		}
	}
	return out
}

func (e *Engine) parseAction(cmd agentprotocol.Command) (agentprotocol.ServiceActionPayload, error) {
	var p agentprotocol.ServiceActionPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return p, fmt.Errorf("invalid %s payload: %w", cmd.Kind, err)
	}
	if p.ServiceID == "" {
		return p, errors.New("serviceId is required")
	}
	return p, nil
}

func (e *Engine) handleRestart(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	p, err := e.parseAction(cmd)
	if err != nil {
		return failedResult(cmd.ID, err.Error())
	}
	defer e.lockService(p.ServiceID)()
	if p.Stack {
		return e.handleStackRestart(ctx, cmd, p)
	}
	if res, fenced := e.fencedResult(cmd.ID, p.ServiceID); fenced {
		return res
	}

	reps := e.currentReplicas(ctx, p.ServiceID)
	if len(reps) == 0 {
		return failedResult(cmd.ID, "service has no container on this host; deploy it first")
	}
	grace := graceOf(p.StopGraceSeconds)
	e.log.Info("restarting service", "service_id", p.ServiceID, "replicas", len(reps))
	// One replica at a time: with several replicas the others keep serving.
	for _, c := range reps {
		if c.Running || c.Restarting {
			if err := e.cfg.Docker.ContainerStop(ctx, c.ID, &grace); err != nil {
				return failedResult(cmd.ID, "stop container "+c.Name+": "+err.Error())
			}
		}
		if err := e.cfg.Docker.ContainerStart(ctx, c.ID); err != nil {
			return failedResult(cmd.ID, "start container "+c.Name+": "+err.Error())
		}
		// Give an instantly crashing container a moment to show itself.
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
		after, err := e.cfg.Docker.ContainerInspect(ctx, c.ID)
		if err != nil {
			return failedResult(cmd.ID, "inspect container "+c.Name+": "+err.Error())
		}
		if !after.Running {
			return failedResult(cmd.ID, fmt.Sprintf("container %s exited with code %d right after restart", c.Name, after.ExitCode))
		}
	}
	if len(reps) == 1 {
		return okResult(cmd.ID, reps[0].ID, "Restarted "+reps[0].Name)
	}
	return okResult(cmd.ID, reps[0].ID, fmt.Sprintf("Restarted %d replicas", len(reps)))
}

func (e *Engine) handleStop(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	p, err := e.parseAction(cmd)
	if err != nil {
		return failedResult(cmd.ID, err.Error())
	}
	defer e.lockService(p.ServiceID)()
	if p.Stack {
		return e.handleStackStop(ctx, cmd, p)
	}

	cs, err := e.containersOf(ctx, p.ServiceID, false)
	if err != nil {
		return failedResult(cmd.ID, "list containers: "+err.Error())
	}
	grace := graceOf(p.StopGraceSeconds)
	var errs []string
	first := ""
	for _, c := range cs {
		if first == "" {
			first = c.ID
		}
		if err := e.cfg.Docker.ContainerStop(ctx, c.ID, &grace); err != nil && !errors.Is(err, docker.ErrNotFound) {
			errs = append(errs, fmt.Sprintf("%s: %v", c.Name, err))
		}
	}
	if len(errs) > 0 {
		return failedResult(cmd.ID, "stop failed: "+strings.Join(errs, "; "))
	}
	return okResult(cmd.ID, first, fmt.Sprintf("Stopped %d container(s)", len(cs)))
}

func (e *Engine) handleRemove(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	p, err := e.parseAction(cmd)
	if err != nil {
		return failedResult(cmd.ID, err.Error())
	}
	defer e.lockService(p.ServiceID)()
	if p.Stack {
		return e.handleStackRemove(ctx, cmd, p)
	}

	cs, err := e.containersOf(ctx, p.ServiceID, true)
	if err != nil {
		return failedResult(cmd.ID, "list containers: "+err.Error())
	}
	grace := graceOf(p.StopGraceSeconds)
	var errs []string
	for _, c := range cs {
		if c.Running || c.Restarting {
			if err := e.cfg.Docker.ContainerStop(ctx, c.ID, &grace); err != nil && !errors.Is(err, docker.ErrNotFound) {
				errs = append(errs, fmt.Sprintf("stop %s: %v", c.Name, err))
			}
		}
		if err := e.cfg.Docker.ContainerRemove(ctx, c.ID, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
			errs = append(errs, fmt.Sprintf("remove %s: %v", c.Name, err))
		}
	}
	// The service's tailnet sidecar (if any) goes with it; the control plane
	// deletes the device.
	if st := e.tn.Remove(ctx, p.ServiceID, nil); st.State == agentprotocol.ServiceTailnetNeedsAttn {
		errs = append(errs, st.Message)
	}

	// Drop the service's routes.
	e.routeMu.Lock()
	cur := e.cfg.Proxy.Current()
	kept := make([]agentprotocol.Route, 0, len(cur))
	for _, r := range cur {
		if r.ServiceID != p.ServiceID && r.Stack != p.ServiceID { // a named process' route belongs to the service too
			kept = append(kept, r)
		}
	}
	if len(kept) != len(cur) {
		if err := e.cfg.Proxy.Apply(kept); err != nil {
			errs = append(errs, "update routes: "+err.Error())
		}
	}
	e.routeMu.Unlock()

	if p.DeleteVolumes {
		for _, v := range p.VolumeNames {
			if !strings.HasPrefix(v, agentprotocol.VolumePrefix) {
				errs = append(errs, "refusing to remove non-managed volume "+v)
				continue
			}
			if err := e.cfg.Docker.RemoveVolume(ctx, v, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
				errs = append(errs, fmt.Sprintf("remove volume %s: %v", v, err))
			}
		}
	}

	e.mu.Lock()
	delete(e.local, p.ServiceID)
	delete(e.desired, p.ServiceID)
	e.mu.Unlock()

	if len(errs) > 0 {
		return failedResult(cmd.ID, "remove incomplete: "+strings.Join(errs, "; "))
	}
	return okResult(cmd.ID, "", fmt.Sprintf("Removed %d container(s)", len(cs)))
}

// setRoutes refreshes the remembered per-service routes after a table change.
func (e *Engine) setRoutes(table []agentprotocol.Route) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, m := range []map[string]desiredEntry{e.local, e.desired} {
		for id, ent := range m {
			ent.Routes = nil
			for _, r := range table {
				if r.ServiceID == id || r.Stack == id {
					ent.Routes = append(ent.Routes, r)
				}
			}
			m[id] = ent
		}
	}
}

func (e *Engine) handleLogsSubscribe(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.LogsSubscribe
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid logs_subscribe payload: "+err.Error())
	}
	if p.StreamID == "" || p.ServiceID == "" {
		return failedResult(cmd.ID, "streamId and serviceId are required")
	}
	reps := e.currentReplicas(ctx, p.ServiceID)
	if len(reps) == 0 {
		msg := "service has no container on this host"
		e.cfg.Sender.LogEnd(agentprotocol.LogEnd{StreamID: p.StreamID, Error: msg})
		return failedResult(cmd.ID, msg)
	}
	// Replicas of one process need telling apart in a merged stream; the count
	// is per process, whatever the other processes run.
	perProcess := map[string]int{}
	for _, c := range reps {
		perProcess[c.Labels[agentprotocol.LabelProcess]]++
	}
	srcs := make([]logs.Source, 0, len(reps))
	for _, c := range reps {
		src := logs.Source{ContainerID: c.ID}
		switch proc := c.Labels[agentprotocol.LabelProcess]; {
		case c.Labels[agentprotocol.LabelComponent] != "": // a stack's component: lines carry "<component> | "
			comp := c.Labels[agentprotocol.LabelComponent]
			if p.Component != "" && comp != p.Component {
				continue
			}
			src.Prefix = comp + stackLogSep
		case p.Process != "": // one process: its lines need no process prefix
			if name, isDefault := processOf(c); (p.Default && !isDefault) || (!p.Default && name != p.Process) {
				continue
			}
			if perProcess[proc] > 1 {
				src.Prefix = fmt.Sprintf("[r%d] ", replicaIndex(c))
			}
		case proc != "": // every process of the service: lines carry "<process> | "
			src.Prefix = proc + stackLogSep
			if perProcess[proc] > 1 {
				src.Prefix += fmt.Sprintf("[r%d] ", replicaIndex(c))
			}
		case len(reps) > 1: // merged stream: tell the replicas apart
			src.Prefix = fmt.Sprintf("[r%d] ", replicaIndex(c))
		}
		srcs = append(srcs, src)
	}
	if len(srcs) == 0 {
		msg := fmt.Sprintf("the stack has no component %q on this host", p.Component)
		if p.Process != "" {
			msg = fmt.Sprintf("the service has no process %q on this host", p.Process)
		}
		e.cfg.Sender.LogEnd(agentprotocol.LogEnd{StreamID: p.StreamID, Error: msg})
		return failedResult(cmd.ID, msg)
	}
	if err := e.subs.Start(ctx, srcs, p); err != nil {
		e.cfg.Sender.LogEnd(agentprotocol.LogEnd{StreamID: p.StreamID, Error: err.Error()})
		return failedResult(cmd.ID, "open log stream: "+err.Error())
	}
	return okResult(cmd.ID, reps[0].ID, "")
}

func (e *Engine) handleLogsUnsubscribe(cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.LogsUnsubscribe
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid logs_unsubscribe payload: "+err.Error())
	}
	e.subs.Stop(p.StreamID)
	return okResult(cmd.ID, "", "")
}
