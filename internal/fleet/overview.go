package fleet

import (
	"context"
	"encoding/json"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// serviceStatus condenses the latest deployment and the observed container
// state into one label.
func serviceStatus(r db.ListFleetServicesRow, serverOnline bool) string {
	switch {
	case r.LatestStatus == "":
		return "undeployed"
	case isActiveStatus(r.LatestStatus):
		return "deploying"
	case r.LatestStatus == "failed" || r.LatestStatus == "rollback_failed":
		return "failed"
	}
	// succeeded, canceled or rolled back: the previous or new release is what runs.
	if r.CurrentDeploymentID == nil {
		return "undeployed"
	}
	if !serverOnline {
		return "unknown"
	}
	var obs struct {
		State  string `json:"state"`
		Health string `json:"health"`
	}
	if len(r.Observed) == 0 || json.Unmarshal(r.Observed, &obs) != nil || obs.State == "" {
		return "unknown"
	}
	switch obs.State {
	case agentprotocol.StateRunning:
		if obs.Health == agentprotocol.HealthUnhealthy {
			return "unhealthy"
		}
		return "running"
	case agentprotocol.StateMissing:
		return "missing"
	case agentprotocol.StateRestarting:
		return "unhealthy"
	default:
		return "stopped"
	}
}

// Overview returns every server with its capacity and the services placed on
// it. Server numbers always cover all services; the service rows are limited to
// the projects the caller may see.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	if err := policy.Require(ctx, policy.ScopeServersRead, nil); err != nil {
		return Overview{}, err
	}
	p, err := policy.Authenticated(ctx)
	if err != nil {
		return Overview{}, err
	}
	sn, err := s.load(ctx, policy.WorkspaceID(ctx))
	if err != nil {
		return Overview{}, err
	}
	limits, unlimited := sn.committed()

	out := Overview{Servers: make([]FleetServer, 0, len(sn.order))}
	idx := map[string]int{}
	for _, id := range sn.order {
		h := sn.hosts[id]
		fs := FleetServer{
			ID: id, Name: h.srv.Name, Status: h.status, DrainState: h.srv.DrainState, DrainMessage: h.srv.DrainMessage,
			CPUs: h.cpus, MemTotalBytes: h.memTotal(), DiskTotalBytes: h.diskTotal(),
			CommittedMemBytes: limits[id], UnlimitedServices: unlimited[id], Services: []FleetService{},
		}
		if h.latest != nil {
			fs.CPUPercent, fs.MemUsedBytes, fs.DiskUsedBytes = h.latest.CpuPercent, h.latest.MemBytes, h.latest.DiskUsedBytes
		}
		fs.Overcommitted = Overcommitted(fs.CommittedMemBytes, fs.MemTotalBytes)
		idx[id.String()] = len(out.Servers)
		out.Servers = append(out.Servers, fs)
		out.Totals.Servers++
		out.Totals.MemTotalBytes += fs.MemTotalBytes
		out.Totals.MemUsedBytes += fs.MemUsedBytes
		out.Totals.CommittedMemBytes += fs.CommittedMemBytes
	}
	for _, r := range sn.services {
		if r.ServerID == nil || !p.CanSeeProject(r.ProjectID) {
			continue
		}
		i, ok := idx[r.ServerID.String()]
		if !ok {
			continue
		}
		fsvc := FleetService{
			ID: r.ID, Name: r.Name, Slug: r.Slug, ProjectID: r.ProjectID, ProjectName: r.ProjectName,
			EnvironmentID: r.EnvironmentID, EnvironmentName: r.EnvironmentName, EnvironmentSlug: r.EnvironmentSlug, Kind: r.Kind,
			Status: serviceStatus(r, sn.hosts[*r.ServerID].status == "online"), MemLimitBytes: memLimit(r.Config), Stateful: stateful(r),
		}
		if smp, ok := sn.samples[r.ID]; ok {
			fsvc.CPUPercent, fsvc.MemBytes = smp.CpuPercent, smp.MemBytes
		}
		out.Servers[i].Services = append(out.Servers[i].Services, fsvc)
		out.Totals.Services++
	}
	return out, nil
}
