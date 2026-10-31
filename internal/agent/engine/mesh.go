package engine

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// meshTimeout bounds a sync_mesh command and a hello-driven convergence: image
// pull, node registration and the wait for new nodes to come up (nodes are
// ensured in parallel).
const meshTimeout = 5 * time.Minute

// meshKeys extracts the single-use node keys from a command's or reconcile's
// secrets. They are never stored, logged or echoed.
func meshKeys(secrets map[string]string) map[string]string {
	keys := map[string]string{}
	for k, v := range secrets {
		if strings.HasPrefix(k, agentprotocol.SecretMeshKeyPrefix) {
			keys[k] = v
		}
	}
	return keys
}

func meshResult(id string, st agentprotocol.MeshStatus) agentprotocol.ResultFrame {
	data, _ := json.Marshal(st)
	return agentprotocol.ResultFrame{CommandID: id, Outcome: agentprotocol.OutcomeSucceeded, Data: data}
}

// handleSyncMesh applies the COMPLETE desired mesh set of this server. A node
// problem never fails the command: it is the node's reported state.
func (e *Engine) handleSyncMesh(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.SyncMeshPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid sync_mesh payload: "+err.Error())
	}
	red := newRedactor(cmd.Secrets, nil)
	logf := func(line string) { e.log.Info(red.apply(line), "component", "mesh") }
	mctx, cancel := context.WithTimeout(ctx, meshTimeout)
	defer cancel()
	return meshResult(cmd.ID, e.mesh.Sync(mctx, p, meshKeys(cmd.Secrets), logf))
}

// handleRemoveMesh removes the named nodes (containers and volumes).
func (e *Engine) handleRemoveMesh(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.RemoveMeshPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || len(p.NodeIDs) == 0 {
		return failedResult(cmd.ID, "invalid remove_mesh payload")
	}
	mctx, cancel := context.WithTimeout(ctx, meshTimeout)
	defer cancel()
	return meshResult(cmd.ID, e.mesh.Remove(mctx, p.NodeIDs, func(line string) { e.log.Info(line, "component", "mesh") }))
}

// handleMeshCheck dials a mesh endpoint from the agent and reports whether the
// path works.
func (e *Engine) handleMeshCheck(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.MeshCheckPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid mesh_check payload: "+err.Error())
	}
	res, err := e.mesh.Check(ctx, p)
	if err != nil {
		return failedResult(cmd.ID, "invalid mesh_check payload: "+err.Error())
	}
	data, _ := json.Marshal(res)
	return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: data}
}

// reconcileMesh converges the mesh nodes to the desired set that rides every
// hello, in the background: an expired sync_mesh command never leaves the
// server unconverged, and a control-plane restart never removes a node (a
// frame without a mesh set changes nothing). The convergence outlives the
// connection that triggered it.
func (e *Engine) reconcileMesh(ctx context.Context, desired agentprotocol.Reconcile) {
	if desired.Mesh == nil {
		return
	}
	p, keys := *desired.Mesh, meshKeys(desired.Secrets)
	red := newRedactor(desired.Secrets, nil)
	logf := func(line string) { e.log.Info(red.apply(line), "component", "mesh") }
	mctx, cancel := e.background(ctx, meshTimeout)
	e.bg.Add(1)
	go func() {
		defer e.bg.Done()
		defer cancel()
		e.mesh.Sync(mctx, p, keys, logf)
	}()
}

// refreshMesh re-evaluates the serve targets of the exposed services after a
// deployment (the nc-svc alias may now be there), in the background.
func (e *Engine) refreshMesh(ctx context.Context) {
	mctx, cancel := e.background(ctx, time.Minute)
	e.bg.Add(1)
	go func() {
		defer e.bg.Done()
		defer cancel()
		e.mesh.Refresh(mctx)
	}()
}
