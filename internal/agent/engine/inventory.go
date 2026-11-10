package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// maxInventoryBytes bounds the inventory result frame: the control plane reads
// at most 8 MiB from an agent.
const maxInventoryBytes = 7 << 20

// volumeLister is the part of the Docker backend that lists volumes (the SDK
// engine has it; the cleanup package states the same method).
type volumeLister interface {
	VolumeList(ctx context.Context) ([]docker.VolumeInfo, error)
}

// handleWorkloadInventory lists the managed service containers, volumes and
// live routes of the host for the adoption review. The result carries the
// containers' environment values: it is neither logged nor stored (the kind is
// ephemeral).
func (e *Engine) handleWorkloadInventory(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	fail := func(msg string) agentprotocol.ResultFrame {
		return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeFailed, Error: msg}
	}
	cs, err := e.managedContainers(ctx)
	if err != nil {
		return fail("list containers: " + err.Error())
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })

	inv := agentprotocol.WorkloadInventory{
		Containers: make([]agentprotocol.InventoryContainer, 0, len(cs)),
		Volumes:    []string{},
		Routes:     []agentprotocol.Route{},
	}
	images := map[string]docker.ImageInfo{} // by image ID; an image that is gone stays empty
	for _, c := range cs {
		img, seen := images[c.ImageID]
		if !seen {
			img, _ = e.cfg.Docker.ImageInspect(ctx, c.ImageID)
			images[c.ImageID] = img
		}
		ic := agentprotocol.InventoryContainer{
			ID: c.ID, Name: c.Name, Image: c.Image, ImageID: c.ImageID, State: c.State, Health: c.Health,
			RestartMode: c.RestartMode, Running: c.Running, Created: c.Created, Labels: c.Labels,
			Env: nonNil(c.Env), Cmd: nonNil(c.Cmd),
			ImageRepoTags: nonNil(img.RepoTags), ImageRepoDigests: nonNil(img.RepoDigests),
			ImageEnv: nonNil(img.Env), ImageCmd: nonNil(img.Cmd),
			Networks: make([]string, 0, len(c.Networks)),
			Mounts:   make([]agentprotocol.InventoryMount, 0, len(c.Mounts)),
		}
		for name := range c.Networks {
			ic.Networks = append(ic.Networks, name)
		}
		sort.Strings(ic.Networks)
		for _, m := range c.Mounts {
			ic.Mounts = append(ic.Mounts, agentprotocol.InventoryMount{Type: m.Type, Name: m.Name, Source: m.Source, Destination: m.Destination})
		}
		inv.Containers = append(inv.Containers, ic)
	}
	if vl, ok := e.cfg.Docker.(volumeLister); ok {
		vols, err := vl.VolumeList(ctx)
		if err != nil {
			return fail("list volumes: " + err.Error())
		}
		for _, v := range vols {
			if strings.HasPrefix(v.Name, agentprotocol.VolumePrefix) {
				inv.Volumes = append(inv.Volumes, v.Name)
			}
		}
		sort.Strings(inv.Volumes)
	}
	if e.cfg.Proxy != nil {
		if routes := e.cfg.Proxy.Current(); routes != nil {
			inv.Routes = routes
		}
	}

	data, err := json.Marshal(inv)
	if err != nil {
		return fail("encode inventory: " + err.Error())
	}
	if len(data) > maxInventoryBytes {
		return fail(fmt.Sprintf("inventory too large: %d containers", len(inv.Containers)))
	}
	e.log.Info("workload inventory collected", "containers", len(inv.Containers), "volumes", len(inv.Volumes), "routes", len(inv.Routes))
	return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: data}
}

// nonNil keeps an empty list a JSON array.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
