package services

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// NetworkRef is a team shared network a service is attached to.
type NetworkRef struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	ServerID uuid.UUID `json:"serverId"`
	// Alias is the hostname the service answers to on the network:
	// <service-slug>.<env-slug>.<project-slug>.
	Alias string `json:"alias"`
}

// SnapshotNetwork is a shared-network attachment frozen into a release.
type SnapshotNetwork struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Alias string    `json:"alias"`
}

// Attachment is the agent-side form of the attachment.
func (n SnapshotNetwork) Attachment() agentprotocol.SharedNetworkAttachment {
	return agentprotocol.SharedNetworkAttachment{Name: agentprotocol.SharedNetworkName(n.ID.String()), Alias: n.Alias}
}

// SnapshotNetworks converts the staged attachments into their snapshot form.
func SnapshotNetworks(refs []NetworkRef) []SnapshotNetwork {
	var out []SnapshotNetwork
	for _, r := range refs {
		out = append(out, SnapshotNetwork{ID: r.ID, Name: r.Name, Alias: r.Alias})
	}
	return out
}

// NetworkAttachments converts snapshot attachments to the agent protocol.
func NetworkAttachments(nets []SnapshotNetwork) []agentprotocol.SharedNetworkAttachment {
	var out []agentprotocol.SharedNetworkAttachment
	for _, n := range nets {
		out = append(out, n.Attachment())
	}
	return out
}

// RouteNetworks lists the networks the proxy must join to reach a service's
// containers: the environment network, or its internal network when the
// service blocks outbound internet.
func RouteNetworks(environmentID uuid.UUID, blockEgress bool) []string {
	if blockEgress {
		return []string{agentprotocol.EnvInternalNetwork(environmentID.String())}
	}
	return []string{agentprotocol.EnvNetwork(environmentID.String())}
}

// ReleaseBlocksEgress reports whether a release snapshot blocks outbound
// internet (false for an empty or undecodable snapshot).
func ReleaseBlocksEgress(releaseConfig json.RawMessage) bool {
	if len(releaseConfig) == 0 {
		return false
	}
	return DecodeSnapshot(releaseConfig).Config.BlockEgress
}

// EnvHasEgressBlocked reports whether another live service of the environment
// on the server blocks outbound internet, in its staged config or in its
// current release; every service of the environment then also joins the
// internal network. Counting the staged config keeps the payloads of one
// deploy batch consistent whatever the deploy order. self is excluded; pass
// the service's own desired BlockEgress as selfBlocks.
func EnvHasEgressBlocked(ctx context.Context, q *db.Queries, environmentID, serverID, self uuid.UUID, selfBlocks bool) (bool, error) {
	if selfBlocks {
		return true, nil
	}
	n, err := q.CountEnvEgressBlockedServices(ctx, db.CountEnvEgressBlockedServicesParams{
		EnvironmentID: environmentID, ServerID: &serverID, ExcludeServiceID: self,
	})
	if err != nil {
		return false, fmt.Errorf("count egress-blocked services: %w", err)
	}
	return n > 0, nil
}

// ListNetworks returns the shared networks the services are attached to
// (staged state), keyed by service.
func ListNetworks(ctx context.Context, q *db.Queries, serviceIDs []uuid.UUID) (map[uuid.UUID][]NetworkRef, error) {
	out := map[uuid.UUID][]NetworkRef{}
	if len(serviceIDs) == 0 {
		return out, nil
	}
	rows, err := q.ListServicesNetworks(ctx, serviceIDs)
	if err != nil {
		return nil, fmt.Errorf("list service networks: %w", err)
	}
	for _, r := range rows {
		out[r.ServiceID] = append(out[r.ServiceID], NetworkRef{ID: r.NetworkID, Name: r.NetworkName, ServerID: r.NetworkServerID, Alias: r.Alias})
	}
	return out, nil
}

// NetworkConflict is the 422 returned when a service with shared-network
// attachments would block outbound internet: a shared bridge would give it
// internet access again, so the two settings are exclusive.
func NetworkConflict() *apierr.Error {
	return apierr.Unprocessable("egress_blocked", "a service that blocks outbound internet cannot join shared networks: detach its shared networks first, or allow outbound internet")
}

// WrongServer is the 422 returned when a service and a shared network are on different servers.
func WrongServer(serviceName, networkName string) *apierr.Error {
	return apierr.Unprocessable("network_wrong_server",
		fmt.Sprintf("%s is not on the server of the shared network %q; shared networks only span one server", serviceName, networkName))
}

// syncNetworks keeps the shared-network attachments of a service consistent
// with a staged config or server change, inside the update transaction: moving
// a service to another server detaches the networks of the old one (a shared
// network spans one server; the removal shows as a pending change), and a
// service that blocks outbound internet cannot keep any attachment. It returns
// the names of the networks it detached.
func syncNetworks(ctx context.Context, q *db.Queries, serviceID uuid.UUID, cfg Config, server *uuid.UUID, serverChanged bool) ([]string, error) {
	atts, err := q.ListServicesNetworks(ctx, []uuid.UUID{serviceID})
	if err != nil {
		return nil, fmt.Errorf("list service networks: %w", err)
	}
	var detached []string
	remaining := 0
	for _, a := range atts {
		if serverChanged && (server == nil || a.NetworkServerID != *server) {
			if _, err := q.DetachServiceNetwork(ctx, db.DetachServiceNetworkParams{ServiceID: serviceID, NetworkID: a.NetworkID}); err != nil {
				return nil, fmt.Errorf("detach network: %w", err)
			}
			detached = append(detached, a.NetworkName)
			continue
		}
		remaining++
	}
	if cfg.BlockEgress && remaining > 0 {
		return nil, NetworkConflict()
	}
	return detached, nil
}

// decorateNetworks fills ServiceView.Networks for every row (out is parallel to rows).
func decorateNetworks(ctx context.Context, q *db.Queries, rows []db.Service, out []ServiceView) error {
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	nets, err := ListNetworks(ctx, q, ids)
	if err != nil {
		return err
	}
	for i, r := range rows {
		out[i].Networks = nets[r.ID]
		if out[i].Networks == nil {
			out[i].Networks = []NetworkRef{}
		}
	}
	return nil
}
