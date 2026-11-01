// Package agents implements the control-plane side of the agent protocol:
// enrollment, the WebSocket hub, durable command dispatch and log routing.
package agents

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// CommandTTL is how long a pending/sent command may wait for delivery before it
// expires. Acked commands never expire.
const CommandTTL = 15 * time.Minute

// NewCommand is a command to persist and deliver to an agent.
type NewCommand struct {
	ServerID       uuid.UUID
	DeploymentID   *uuid.UUID // optional
	Kind           string     // agentprotocol.Kind*
	Payload        json.RawMessage
	IdempotencyKey string // unique; defaults to the command ID when empty
}

// Dispatcher persists commands and delivers them to connected agents.
type Dispatcher interface {
	// Enqueue inserts the command (status pending) inside tx. Delivery is
	// attempted after the transaction commits.
	Enqueue(ctx context.Context, tx pgx.Tx, cmd NewCommand) (uuid.UUID, error)
	// SubscribeLogs asks the agent for runtime logs. The returned channel is
	// closed at log_end or when cancel is called.
	SubscribeLogs(ctx context.Context, serverID uuid.UUID, req agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error)
}

// SecretSource resolves the send-time secrets for a command. Implemented by
// the services domain; never persisted.
type SecretSource interface {
	SecretsForCommand(ctx context.Context, commandID uuid.UUID, kind string, payload json.RawMessage) (map[string]string, error)
}

// ResultHandler applies command results and deploy step events to domain state.
type ResultHandler interface {
	HandleResult(ctx context.Context, serverID uuid.UUID, commandID uuid.UUID, deploymentID *uuid.UUID, res agentprotocol.ResultFrame) error
	// HandleUndelivered marks the deployment of an expired or canceled command
	// failed with msg (e.g. "Not delivered: server offline. No changes were made.").
	// It is called only for commands that were never acked.
	HandleUndelivered(ctx context.Context, serverID uuid.UUID, commandID uuid.UUID, deploymentID *uuid.UUID, msg string) error
	HandleEvent(ctx context.Context, serverID uuid.UUID, ev agentprotocol.EventFrame) error
	HandleObserved(ctx context.Context, serverID uuid.UUID, obs agentprotocol.Observed) error
	// DesiredState returns the reconcile frame for a server (sent on hello).
	DesiredState(ctx context.Context, serverID uuid.UUID) (agentprotocol.Reconcile, error)
}

// TrafficSink stores the traffic analytics buckets an agent reports. Implemented
// by internal/analytics. A nil error means the buckets are durable and the agent
// may forget them; duplicates (resends after a reconnect) must be ignored.
type TrafficSink interface {
	IngestTraffic(ctx context.Context, serverID uuid.UUID, t agentprotocol.Traffic) error
}

// ProbeSink stores the uptime probe results an agent reports. Implemented by
// internal/uptime. A nil error means the results are durable and the agent may
// forget them; duplicates (resends after a reconnect) must be ignored.
type ProbeSink interface {
	IngestProbes(ctx context.Context, serverID uuid.UUID, p agentprotocol.Probes) error
}

// TaskLogSink stores the output frames of task runs (frames with a taskRunId).
// Implemented by internal/tasks, which redacts, caps and persists the lines.
type TaskLogSink interface {
	HandleTaskLog(ctx context.Context, serverID uuid.UUID, lf agentprotocol.LogFrame) error
}

// Redactor masks secret values in log lines for a deployment or service.
type Redactor interface {
	RedactorFor(ctx context.Context, serviceID uuid.UUID) (func(string) string, error)
}
