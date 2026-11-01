package services

import (
	"sort"
	"strings"

	"github.com/google/uuid"
)

// Cross-server service links. A consumer on one server
// reaches a provider on another through the mesh: the provider's slug is the
// alias of a link node on the consumer's environment network and the provider's
// port becomes the mesh port of its exposure (EffectivePort). The helpers here
// are the one place that decides which members of a replica group an export
// reaches, so the export provider (internal/databases) and the grant derivation
// (internal/meshlinks) never drift apart.

// Error and warning codes of the mesh link domain.
const (
	CodeMeshRoutePending         = "mesh_route_pending"
	CodeMeshNotExposable         = "mesh_not_exposable"
	CodeMeshLinkNotReady         = "mesh_link_not_ready"
	CodeMeshConfirmationRequired = "mesh_confirmation_required"
	CodeMeshServerRequired       = "mesh_server_required"
	CodeMeshPaused               = "mesh_paused"
)

// Replica lifecycle states (database_replicas.state) that serve reads.
const (
	replicaStreaming = "streaming"
	replicaLagging   = "lagging"
)

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// servesReads reports whether a group member can answer read queries: the
// primary always, a replica while it is streaming or lagging.
func (m GroupMember) servesReads() bool {
	return !m.IsPrimary && (m.State == replicaStreaming || m.State == replicaLagging)
}

// GroupWriter returns the primary of a replica group.
func GroupWriter(group []GroupMember) (GroupMember, bool) {
	for _, m := range group {
		if m.IsPrimary {
			return m, true
		}
	}
	return GroupMember{}, false
}

// GroupReaders returns the replicas a consumer on server consumer reads from:
// the streaming or lagging replicas on the consumer's server when there are any,
// otherwise the ones on other servers (reached through the mesh). Sorted by
// slug. Empty when no replica serves reads: the caller falls back to the writer.
func GroupReaders(consumer *uuid.UUID, group []GroupMember) []GroupMember {
	var local, other []GroupMember
	for _, m := range group {
		if !m.servesReads() {
			continue
		}
		if consumer != nil && m.ServerID != nil && *m.ServerID == *consumer {
			local = append(local, m)
		} else {
			other = append(other, m)
		}
	}
	out := local
	if len(out) == 0 {
		out = other
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// MemberPort is the port a consumer on server consumer dials to reach the group
// member m: its mesh port when it runs on another server and has an exposure,
// else its container port (EffectivePort for a group member).
func MemberPort(consumer *uuid.UUID, m GroupMember) int {
	if m.MeshPort > 0 && consumer != nil && m.ServerID != nil && *m.ServerID != *consumer {
		return m.MeshPort
	}
	return m.Port
}

// Reader and writer export key prefixes (DATABASE_READ_URL, DATABASE_WRITE_HOST, ...).
const (
	ExportReadPrefix  = "DATABASE_READ_"
	ExportWritePrefix = "DATABASE_WRITE_"
)

// ExportTargets lists the services the export key of the service self reaches
// for a consumer on server consumer: the group's readers (the writer when there
// are none) for DATABASE_READ_*, the group's primary for DATABASE_WRITE_*, and
// self for every other export.
func ExportTargets(key string, consumer *uuid.UUID, group []GroupMember, self uuid.UUID) []uuid.UUID {
	switch {
	case strings.HasPrefix(key, ExportReadPrefix) && len(group) > 0:
		if rs := GroupReaders(consumer, group); len(rs) > 0 {
			ids := make([]uuid.UUID, len(rs))
			for i, m := range rs {
				ids[i] = m.ID
			}
			return ids
		}
		if w, ok := GroupWriter(group); ok {
			return []uuid.UUID{w.ID}
		}
	case strings.HasPrefix(key, ExportWritePrefix) && len(group) > 0:
		if w, ok := GroupWriter(group); ok {
			return []uuid.UUID{w.ID}
		}
	}
	return []uuid.UUID{self}
}

// groupOf assembles the replica group of sd from the resolver's services: the
// primary and every replica of it, the primary first, then by slug. A service
// that is neither a replica nor has replicas has no group (nil).
func (r *Resolver) groupOf(sd *ServiceData) []GroupMember {
	primaryID := sd.ID
	if sd.Replica != nil {
		primaryID = sd.Replica.PrimaryID
	}
	var members []GroupMember
	for _, o := range r.services {
		switch {
		case o.ID == primaryID:
			members = append(members, groupMember(o, true))
		case o.Replica != nil && o.Replica.PrimaryID == primaryID:
			members = append(members, groupMember(o, false))
		}
	}
	if len(members) < 2 {
		return nil
	}
	sort.Slice(members, func(i, j int) bool {
		if members[i].IsPrimary != members[j].IsPrimary {
			return members[i].IsPrimary
		}
		return members[i].Slug < members[j].Slug
	})
	return members
}

func groupMember(o *ServiceData, primary bool) GroupMember {
	m := GroupMember{ID: o.ID, Slug: o.Slug, IsPrimary: primary, Port: o.Port, ServerID: o.ServerID, MeshPort: o.MeshPort}
	if o.Replica != nil {
		m.State = o.Replica.State
	}
	return m
}
