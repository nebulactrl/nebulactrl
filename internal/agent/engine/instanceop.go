package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// instanceOpTimeout bounds one instance operation. Garbage collection of a large
// registry walks every blob; the control plane's window is far longer than this
// only in the worst case.
const instanceOpTimeout = 2 * time.Hour

// instanceOpLockKey serializes instance operations; no service has this ID.
const instanceOpLockKey = "\x00instance-op"

// instanceOps maps an operation name to the container and argv it runs. The
// control plane sends a name, never an argv: this table is the whole allow-list.
var instanceOps = map[string]struct {
	container string
	argv      []string
}{
	// garbage-collect without --delete-untagged: untagged manifests are deleted
	// by the control plane's retention sweep, never here.
	agentprotocol.InstanceOpRegistryGC: {
		container: agentprotocol.RegistryContainerName,
		argv:      []string{"/bin/registry", "garbage-collect", "/etc/distribution/config.yml"},
	},
}

// handleInstanceOp runs an allow-listed maintenance operation in an instance
// service's container. The control plane enqueues it only for the server it
// runs on; here the fixed container name is the second fence: on any other host
// there is no such container and the command fails.
func (e *Engine) handleInstanceOp(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.InstanceOpPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid instance_op payload: "+err.Error())
	}
	op, ok := instanceOps[p.Op]
	if !ok {
		return failedResult(cmd.ID, fmt.Sprintf("unknown instance operation %q", p.Op))
	}
	info, err := e.cfg.Docker.ContainerInspect(ctx, op.container)
	if errors.Is(err, docker.ErrNotFound) {
		return failedResult(cmd.ID, "the "+op.container+" container does not exist on this host")
	}
	if err != nil {
		return failedResult(cmd.ID, "inspect "+op.container+": "+err.Error())
	}
	if !info.Running {
		return failedResult(cmd.ID, op.container+" is not running")
	}
	unlock := e.lockService(instanceOpLockKey)
	defer unlock()
	octx, cancel := context.WithTimeout(ctx, instanceOpTimeout)
	defer cancel()

	out := &tailBuffer{max: 4096}
	code, err := e.cfg.Docker.ContainerExec(octx, info.ID, op.argv, docker.ExecSpec{}, out, out)
	if err != nil {
		return failedResult(cmd.ID, p.Op+": "+err.Error())
	}
	// A registry nobody pushed to yet has no repositories directory, which the collector reports as an
	// error although there is nothing to collect.
	if code != 0 && p.Op == agentprotocol.InstanceOpRegistryGC && strings.Contains(out.String(), "Path not found: /docker/registry/v2/repositories") {
		code = 0
	}
	if code != 0 {
		return failedResult(cmd.ID, fmt.Sprintf("%s exited with status %d: %s", p.Op, code, out.String()))
	}
	e.log.Info("instance operation finished", "op", p.Op)
	data, _ := json.Marshal(agentprotocol.InstanceOpResult{Op: p.Op, Output: out.String()})
	return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: data}
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.max; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
}
