package engine

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// tailnetTimeout bounds the tailnet step of a deployment and a sync_tailnet
// command: image pull, node registration and the wait for the node to come up.
const tailnetTimeout = 4 * time.Minute

// tailnetStep ensures (or removes) the service's tailnet sidecar after the app
// is live. It never fails the deployment: the outcome travels in
// DeployResult.Tailnet and a problem becomes a warning on the result message.
func (d *deploy) tailnetStep(ctx context.Context) {
	spec := d.p.Tailnet
	if spec == nil || d.silent {
		return
	}
	s := *spec
	// The payload is the authority on what the service is.
	s.ServiceID, s.ServiceSlug, s.EnvironmentID = d.p.ServiceID, d.p.ServiceSlug, d.p.EnvironmentID
	s.BlockEgress = d.p.BlockEgress
	label := "Ensuring the tailnet node " + s.Hostname
	if s.Mode == agentprotocol.TailnetRemove {
		label = "Removing the tailnet node"
	}
	d.event(agentprotocol.StepTailnet, agentprotocol.StepStarted, label)
	tctx, cancel := context.WithTimeout(ctx, tailnetTimeout)
	defer cancel()
	var st agentprotocol.ServiceTailnetStatus
	logf := func(line string) { d.logf("%s", line) }
	if s.Mode == agentprotocol.TailnetRemove {
		st = d.e.tn.Remove(tctx, s.ServiceID, logf)
	} else {
		st = d.e.tn.Ensure(tctx, s, d.cmd.Secrets[agentprotocol.SecretServiceTailnetKey], logf)
	}
	d.tailnet = &st
	switch st.State {
	case agentprotocol.ServiceTailnetOnline, agentprotocol.ServiceTailnetRemoved:
		d.event(agentprotocol.StepTailnet, agentprotocol.StepSucceeded, st.FQDN)
	default:
		msg := st.State
		if st.Message != "" {
			msg += ": " + st.Message
		}
		d.event(agentprotocol.StepTailnet, agentprotocol.StepFailed, msg)
		if d.warning == "" {
			d.warning = "tailnet " + msg
		}
	}
}

func tailnetResult(id string, st agentprotocol.ServiceTailnetStatus) agentprotocol.ResultFrame {
	data, _ := json.Marshal(st)
	return agentprotocol.ResultFrame{CommandID: id, Outcome: agentprotocol.OutcomeSucceeded, Data: data}
}

// handleSyncTailnet ensures (or re-provisions) one service's sidecar without
// deploying the app.
func (e *Engine) handleSyncTailnet(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.SyncTailnetPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid sync_tailnet payload: "+err.Error())
	}
	spec := p.Tailnet
	if spec.ServiceID == "" || spec.Mode != agentprotocol.TailnetEnsure {
		return failedResult(cmd.ID, "sync_tailnet needs an ensure spec with a serviceId")
	}
	defer e.lockService(spec.ServiceID)()
	key := cmd.Secrets[agentprotocol.SecretServiceTailnetKey]
	red := newRedactor(cmd.Secrets, nil)
	logf := func(line string) { e.log.Info(red.apply(line), "service_id", spec.ServiceID) }
	tctx, cancel := context.WithTimeout(ctx, tailnetTimeout)
	defer cancel()
	return tailnetResult(cmd.ID, e.tn.Ensure(tctx, spec, key, logf))
}

// handleRemoveTailnet removes one service's sidecar and volumes.
func (e *Engine) handleRemoveTailnet(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.RemoveTailnetPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || p.ServiceID == "" {
		return failedResult(cmd.ID, "invalid remove_tailnet payload")
	}
	defer e.lockService(p.ServiceID)()
	tctx, cancel := context.WithTimeout(ctx, tailnetTimeout)
	defer cancel()
	return tailnetResult(cmd.ID, e.tn.Remove(tctx, p.ServiceID, func(line string) { e.log.Info(line, "service_id", p.ServiceID) }))
}
