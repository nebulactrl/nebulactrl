package services

import (
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// RuntimeParts are the parts of an agent payload that describe how a
// service's container is set up: the same for a deployment (DeployPayload) and
// a scheduled run of a cron service (RunTaskPayload).
type RuntimeParts struct {
	StartCommand []string
	Docker       *agentprotocol.DockerOptions
	Volumes      []agentprotocol.VolumeMount
	Files        []agentprotocol.FileSpec
}

// RuntimePartsOf maps a release snapshot to its runtime payload parts.
func RuntimePartsOf(serviceID uuid.UUID, snap Snapshot) RuntimeParts {
	parts := RuntimeParts{StartCommand: snap.Config.StartCommand, Docker: snap.Config.Docker}
	for _, v := range snap.Volumes {
		parts.Volumes = append(parts.Volumes, agentprotocol.VolumeMount{Name: v.EffectiveDockerName(serviceID), MountPath: v.MountPath, Owner: v.Owner})
	}
	for _, f := range snap.Config.Files {
		parts.Files = append(parts.Files, agentprotocol.FileSpec{Path: f.Path, Mode: f.Mode, Content: f.Content})
	}
	return parts
}

// SnapshotVolumeOf is the snapshot form of a volume row.
func SnapshotVolumeOf(v db.Volume) SnapshotVolume {
	return SnapshotVolume{Name: v.Name, MountPath: v.MountPath, DockerName: v.DockerName, RestoreLabel: v.RestoreLabel, Owner: v.Owner}
}
