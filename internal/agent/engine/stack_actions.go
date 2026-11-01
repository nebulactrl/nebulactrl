package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/compose"
	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// stackLogSep follows the component name in the lines of a stack's log stream
// ("<component> | <line>"), live and stored.
const stackLogSep = " | "

// stackProject is the compose project of a stack for commands that act by
// project name. The working directory is the stack's directory when it exists
// (compose looks for a compose file in the working directory when none is
// given; the stack directory holds none at its top level).
func (e *Engine) stackProject(serviceID string) compose.Project {
	p := compose.Project{Name: agentprotocol.StackProject(serviceID)}
	var dirs []string
	if root, err := e.stackRoot(serviceID); err == nil {
		dirs = append(dirs, root)
	}
	dirs = append(dirs, e.cfg.StackDir)
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			p.Dir = dir
			break
		}
	}
	return p
}

// composeLines collects the output of a compose command into the engine's log.
func (e *Engine) composeLines(serviceID string) compose.Output {
	log := e.log.With("service_id", serviceID)
	return func(line string) { log.Debug("docker compose", "line", line) }
}

func (e *Engine) handleStackRestart(ctx context.Context, cmd agentprotocol.Command, p agentprotocol.ServiceActionPayload) agentprotocol.ResultFrame {
	if e.cfg.Compose == nil {
		return failedResult(cmd.ID, errComposeUnavailable)
	}
	if !stackIDPattern.MatchString(p.ServiceID) {
		return failedResult(cmd.ID, errStackServiceID)
	}
	proj := e.stackProject(p.ServiceID)
	e.log.Info("restarting stack", "service_id", p.ServiceID, "project", proj.Name)
	if err := e.cfg.Compose.Restart(ctx, proj, graceOf(p.StopGraceSeconds), e.composeLines(p.ServiceID)); err != nil {
		return failedResult(cmd.ID, "restart the stack: "+err.Error())
	}
	// Give an instantly crashing component a moment to show itself.
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
	}
	conts, err := e.stackContainers(ctx, p.ServiceID, "")
	if err != nil {
		return failedResult(cmd.ID, "list the stack's containers: "+err.Error())
	}
	if len(conts) == 0 {
		return failedResult(cmd.ID, "the stack has no container on this host; deploy it first")
	}
	var bad []string
	for _, c := range conts {
		after, err := e.cfg.Docker.ContainerInspect(ctx, c.ID)
		if err != nil {
			return failedResult(cmd.ID, "inspect container "+c.Name+": "+err.Error())
		}
		// A health check that is starting again is fine; a container that is
		// gone or crashing is not.
		switch {
		case after.Restarting:
			bad = append(bad, after.Name+": restarting")
		case !after.Running && !(after.State == "exited" && after.ExitCode == 0):
			bad = append(bad, fmt.Sprintf("%s: exited with code %d", after.Name, after.ExitCode))
		}
	}
	if len(bad) > 0 {
		return failedResult(cmd.ID, "components failed right after restart: "+strings.Join(bad, "; "))
	}
	return okResult(cmd.ID, conts[0].ID, fmt.Sprintf("Restarted stack %s (%d component(s))", proj.Name, len(conts)))
}

func (e *Engine) handleStackStop(ctx context.Context, cmd agentprotocol.Command, p agentprotocol.ServiceActionPayload) agentprotocol.ResultFrame {
	if e.cfg.Compose == nil {
		return failedResult(cmd.ID, errComposeUnavailable)
	}
	if !stackIDPattern.MatchString(p.ServiceID) {
		return failedResult(cmd.ID, errStackServiceID)
	}
	proj := e.stackProject(p.ServiceID)
	if err := e.cfg.Compose.Stop(ctx, proj, graceOf(p.StopGraceSeconds), e.composeLines(p.ServiceID)); err != nil {
		return failedResult(cmd.ID, "stop the stack: "+err.Error())
	}
	conts, _ := e.stackContainers(ctx, p.ServiceID, "")
	return okResult(cmd.ID, firstContainerID(conts), fmt.Sprintf("Stopped stack %s (%d component(s))", proj.Name, len(conts)))
}

func (e *Engine) handleStackRemove(ctx context.Context, cmd agentprotocol.Command, p agentprotocol.ServiceActionPayload) agentprotocol.ResultFrame {
	if !stackIDPattern.MatchString(p.ServiceID) {
		return failedResult(cmd.ID, errStackServiceID)
	}
	grace := graceOf(p.StopGraceSeconds)
	proj := e.stackProject(p.ServiceID)
	var errs []string
	switch {
	case e.cfg.Compose != nil:
		if err := e.cfg.Compose.Down(ctx, proj, compose.DownOptions{RemoveVolumes: p.DeleteVolumes, Grace: grace}, e.composeLines(p.ServiceID)); err != nil {
			errs = append(errs, "docker compose down: "+err.Error())
		}
	case p.DeleteVolumes:
		return failedResult(cmd.ID, errComposeUnavailable+": the stack's volumes cannot be removed")
	}

	// Whatever compose left (or everything, without the plugin): remove the
	// stack's containers by label. Networks and volumes need compose.
	cs, err := e.containersOf(ctx, p.ServiceID, true)
	if err != nil {
		errs = append(errs, "list containers: "+err.Error())
	}
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

	// Drop the stack's routes.
	e.routeMu.Lock()
	cur := e.cfg.Proxy.Current()
	kept := make([]agentprotocol.Route, 0, len(cur))
	for _, r := range cur {
		if r.ServiceID != p.ServiceID && r.Stack != p.ServiceID {
			kept = append(kept, r)
		}
	}
	if len(kept) != len(cur) {
		if err := e.cfg.Proxy.Apply(kept); err != nil {
			errs = append(errs, "update routes: "+err.Error())
		}
	}
	e.routeMu.Unlock()

	if len(errs) == 0 && e.cfg.StackDir != "" {
		root, err := e.stackRoot(p.ServiceID)
		if err == nil {
			err = os.RemoveAll(root)
		}
		if err != nil {
			errs = append(errs, "remove the stack's files: "+err.Error())
		}
	}

	e.mu.Lock()
	delete(e.local, p.ServiceID)
	delete(e.desired, p.ServiceID)
	e.mu.Unlock()

	if len(errs) > 0 {
		return failedResult(cmd.ID, "remove incomplete: "+strings.Join(errs, "; "))
	}
	msg := "Removed stack " + proj.Name
	if e.cfg.Compose == nil {
		msg += "; Docker Compose is not available, so its networks were left in place"
	}
	return okResult(cmd.ID, "", msg)
}
