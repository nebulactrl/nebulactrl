package conn

import (
	"context"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Handler executes control-plane commands. HandleCommand runs on its own
// goroutine under the client's root context (it survives reconnects) and must
// return a result for every command, including failures; the client fills in
// ResultFrame.CommandID.
//
// Redelivery: a command whose result is stored is answered from the store and
// never re-run. A command that was received but has no stored result (the agent
// restarted mid-command) IS run again, so handlers must be idempotent per
// command ID / deployment ID.
type Handler interface {
	HandleCommand(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame
}

// ResultObserver is optionally implemented by a Handler that wants to act once
// a command's result has been queued for the control plane (frames it sends
// from ResultSent follow the result on the wire).
type ResultObserver interface {
	ResultSent(ctx context.Context, cmd agentprotocol.Command, res agentprotocol.ResultFrame)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame

func (f HandlerFunc) HandleCommand(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	return f(ctx, cmd)
}

// Reconciler reacts to the control plane's desired-state frame, sent after
// every hello. It runs serialized (one reconcile at a time).
type Reconciler interface {
	Reconcile(ctx context.Context, desired agentprotocol.Reconcile) error
}

// FrameObserver is implemented by a Reconciler that must see every reconcile
// frame at once, in arrival order, on the read loop: ObserveFrame runs before
// the (asynchronous) Reconcile of the frame is started and before any command
// that follows it is handled. It must be quick.
type FrameObserver interface {
	ObserveFrame(ctx context.Context, desired agentprotocol.Reconcile)
}

// StreamHandler serves the interactive stream frames (terminal sessions and
// the file browser). A Handler that also implements it is installed for
// streams by Client.SetHandler.
type StreamHandler interface {
	// HandleStream is called on the connection's reader goroutine for every
	// stream frame, in order. It must not block: do the work on goroutines.
	HandleStream(f agentprotocol.Frame)
	// Disconnected is called when the control-plane connection ended. Every
	// stream is over: the handler must end its sessions.
	Disconnected()
}

// Sender emits agent → control-plane frames other than hello/auth/ack/result.
// Implementations are safe for concurrent use. Frames sent while the control
// plane is unreachable are buffered (bounded; oldest dropped first, stats and
// observed frames keep only the latest) and flushed on reconnect.
type Sender interface {
	Event(agentprotocol.EventFrame)
	Log(agentprotocol.LogFrame)
	LogEnd(agentprotocol.LogEnd)
	Stats(agentprotocol.Stats)
	Observed(agentprotocol.Observed)
}

func failed(id, msg string) agentprotocol.ResultFrame {
	return agentprotocol.ResultFrame{CommandID: id, Outcome: agentprotocol.OutcomeFailed, Error: msg}
}
