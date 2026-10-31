package fleet

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Blocking codes of a plan move.
const (
	BlockNoTarget      = "no_target"
	BlockTargetOffline = "target_offline"
	// BlockRequiresUnrestricted: the service uses Docker options only an
	// unrestricted server allows and the target is not one.
	BlockRequiresUnrestricted = services.CodeRequiresUnrestricted
)

// sourceRows lists the services placed on server, stateless ones first so a
// failure stops before any data-bearing service is touched.
func (sn *snapshot) sourceRows(server uuid.UUID) []db.ListFleetServicesRow {
	var rows []db.ListFleetServicesRow
	for _, r := range sn.services {
		if r.ServerID != nil && *r.ServerID == server {
			rows = append(rows, r)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return !stateful(rows[i]) && stateful(rows[j]) })
	return rows
}

// demandOf is the memory a service will need on its target.
func (sn *snapshot) demandOf(r db.ListFleetServicesRow) (Demand, bool) {
	if l := memLimit(r.Config); l > 0 {
		return Demand{Bytes: l, Limited: true}, true
	}
	if smp, ok := sn.samples[r.ID]; ok {
		return Demand{Bytes: smp.MemBytes}, true
	}
	return Demand{}, false
}

// checkTarget validates an explicitly chosen target server.
func (s *Service) checkTarget(sn *snapshot, source, target uuid.UUID) (*hostInfo, error) {
	h, ok := sn.hosts[target]
	switch {
	case !ok:
		return nil, apierr.Unprocessable("server_not_found", "the selected target server does not exist")
	case target == source:
		return nil, apierr.Unprocessable("invalid_target", "a server cannot be drained onto itself")
	case h.srv.Status == "revoked":
		return nil, apierr.Unprocessable("server_unavailable", fmt.Sprintf("server %q is revoked", h.srv.Name))
	case h.srv.DrainState == services.DrainStateDraining || h.srv.DrainState == services.DrainStateDrained:
		return nil, services.ServerDrainingError(h.srv.Name)
	case !h.srv.AcceptWorkloads:
		return nil, services.ServerNotAcceptingError(h.srv.Name)
	}
	return h, nil
}

// Plan proposes where every service of the server goes. With a target all
// services go there; without one each goes to the online, schedulable server
// with the most free memory.
func (s *Service) Plan(ctx context.Context, serverID uuid.UUID, target *uuid.UUID) (DrainPlan, error) {
	if _, err := requireFleetWide(ctx, policy.ScopeServersRead, policy.ScopeProjectsRead); err != nil {
		return DrainPlan{}, err
	}
	src, err := s.loadServer(ctx, policy.ScopeServersRead, serverID)
	if err != nil {
		return DrainPlan{}, err
	}
	sn, err := s.load(ctx, src.WorkspaceID)
	if err != nil {
		return DrainPlan{}, err
	}
	if target != nil {
		if _, err := s.checkTarget(sn, serverID, *target); err != nil {
			return DrainPlan{}, err
		}
	}
	return s.plan(sn, serverID, target, nil), nil
}

// plan builds the plan; fixed (service → target) overrides automatic choice and
// the explicit target.
func (s *Service) plan(sn *snapshot, serverID uuid.UUID, target *uuid.UUID, fixed map[uuid.UUID]uuid.UUID) DrainPlan {
	hosts := s.capacityHosts(sn, serverID)
	byID := map[uuid.UUID]*Host{}
	for _, h := range hosts {
		byID[h.ID] = h
	}
	srcInfo := sn.hosts[serverID]
	out := DrainPlan{Moves: []PlanMove{}}
	for _, r := range sn.sourceRows(serverID) {
		cfgDemand, haveDemand := sn.demandOf(r)
		m := PlanMove{
			ServiceID: r.ID, ServiceName: r.Name, ProjectName: r.ProjectName, EnvironmentName: r.EnvironmentName,
			Stateful: stateful(r), Warnings: []string{}, Blocking: []deployments.PlanBlocking{},
		}
		var host *Host
		dockerFields := services.DecodeConfig(r.Config).Docker.UnrestrictedFields()
		needsUnrestricted := len(dockerFields) > 0
		explicit := target
		if t, ok := fixed[r.ID]; ok {
			explicit = &t
		}
		switch {
		case explicit != nil:
			host = byID[*explicit]
			if host != nil {
				host.Reserve(cfgDemand)
				if !host.Eligible {
					m.Blocking = append(m.Blocking, deployments.PlanBlocking{
						Code: BlockTargetOffline, Message: fmt.Sprintf("%s is offline; a deployment to it could not start.", host.Name),
					})
				}
			}
		default:
			candidates := hosts
			if needsUnrestricted {
				// Options that hand a container the host only run on an unrestricted server.
				candidates = candidates[:0:0]
				for _, h := range hosts {
					if sn.hosts[h.ID].srv.Unrestricted {
						candidates = append(candidates, h)
					}
				}
			}
			host = PickTarget(candidates, cfgDemand)
			if host != nil {
				if host.HasData {
					m.Warnings = append(m.Warnings, fmt.Sprintf("%s was chosen automatically: it has the most free memory (%s free after this move).", host.Name, FormatBytes(host.Headroom())))
				} else {
					m.Warnings = append(m.Warnings, fmt.Sprintf("%s was chosen automatically: no online server reported memory yet, so capacity is unknown.", host.Name))
				}
			}
		}
		if host == nil {
			m.Blocking = append(m.Blocking, deployments.PlanBlocking{
				Code: BlockNoTarget, Message: fmt.Sprintf("No online server can take %s. Enroll or bring another server online first.", r.Name),
			})
			out.Moves = append(out.Moves, m)
			continue
		}
		id := host.ID
		if needsUnrestricted && !sn.hosts[id].srv.Unrestricted {
			m.Blocking = append(m.Blocking, deployments.PlanBlocking{
				Code: BlockRequiresUnrestricted,
				Message: fmt.Sprintf("%s uses Docker options (%s) that need an unrestricted server, and %s is not one. An instance admin can switch it, or remove the options.",
					r.Name, strings.Join(dockerFields, ", "), host.Name),
			})
		}
		m.TargetServerID, m.TargetServerName = &id, host.Name
		m.Warnings = append(m.Warnings, s.moveWarnings(r, srcInfo, sn.hosts[id], host, cfgDemand, haveDemand)...)
		out.Moves = append(out.Moves, m)
	}
	fits, reasons := Fit(hosts)
	out.Capacity = Capacity{TargetFits: fits, Reasons: reasons}
	return out
}

func (s *Service) moveWarnings(r db.ListFleetServicesRow, src, dst *hostInfo, _ *Host, d Demand, haveDemand bool) []string {
	var w []string
	if r.VolumeCount > 0 {
		w = append(w, fmt.Sprintf("Volumes are not copied. %s will start with empty volumes on %s; move data first.", r.Name, dst.srv.Name))
	} else if r.Kind == "database" {
		w = append(w, fmt.Sprintf("%s is a database. Its data is not copied: it will start empty on %s; move data first.", r.Name, dst.srv.Name))
	}
	switch {
	case r.CurrentDeploymentID == nil:
		w = append(w, fmt.Sprintf("%s has not been deployed yet: only its placement changes, nothing is started on %s.", r.Name, dst.srv.Name))
	case r.Generation > r.AppliedGeneration:
		w = append(w, fmt.Sprintf("%s has staged changes; they are deployed together with the move.", r.Name))
	}
	if r.CurrentDeploymentID != nil {
		if !haveDemand {
			w = append(w, fmt.Sprintf("%s has no memory limit and no usage data, so the capacity check cannot account for it.", r.Name))
		} else if !d.Limited {
			w = append(w, fmt.Sprintf("%s has no memory limit; its current usage (%s) was used for the capacity check.", r.Name, FormatBytes(d.Bytes)))
		}
	}
	if isActiveStatus(r.LatestStatus) {
		w = append(w, fmt.Sprintf("A deployment of %s is in progress; the drain waits for it to finish.", r.Name))
	}
	if r.DomainCount > 0 && dst.srv.PublicIp != nil && *dst.srv.PublicIp != "" && (src.srv.PublicIp == nil || *src.srv.PublicIp != *dst.srv.PublicIp) {
		w = append(w, fmt.Sprintf("%s has %d domain(s): point their A records to %s (%s) after the move.", r.Name, r.DomainCount, dst.srv.Name, *dst.srv.PublicIp))
	}
	if src.status != "online" && r.CurrentDeploymentID != nil {
		w = append(w, fmt.Sprintf("%s is %s: its old container cannot be stopped until the agent reconnects.", src.srv.Name, src.status))
	}
	return w
}

func isActiveStatus(s string) bool {
	switch s {
	case "created", "queued", "preparing", "building", "releasing", "verifying":
		return true
	}
	return false
}

// joinNames renders "a, b and c".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
