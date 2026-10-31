package tailscale

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// TailnetDeleteDeviceArgs removes a device from the tailnet after its server or
// service stopped using it. The job retries with River's backoff and does
// nothing when the integration was removed in the meantime or the device is
// already gone. Tailscale deletes by DeviceID; Headscale finds the node by IPs,
// else Hostname.
type TailnetDeleteDeviceArgs struct {
	WorkspaceID uuid.UUID `json:"workspaceId"`
	DeviceID    string    `json:"deviceId,omitempty"`
	IPs         []string  `json:"ips,omitempty"`
	Hostname    string    `json:"hostname,omitempty"`
}

// Kind implements river.JobArgs.
func (TailnetDeleteDeviceArgs) Kind() string { return "tailnet_delete_device" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (TailnetDeleteDeviceArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 10}
}

type deleteDeviceWorker struct {
	river.WorkerDefaults[TailnetDeleteDeviceArgs]
	svc *Service
}

func (w *deleteDeviceWorker) Work(ctx context.Context, job *river.Job[TailnetDeleteDeviceArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return w.svc.DeleteNode(ctx, job.Args.WorkspaceID, NodeRef{DeviceID: job.Args.DeviceID, IPs: job.Args.IPs, Hostname: job.Args.Hostname})
}

// RegisterJobs registers the device-removal worker.
func RegisterJobs(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &deleteDeviceWorker{svc: svc})
	})
}
