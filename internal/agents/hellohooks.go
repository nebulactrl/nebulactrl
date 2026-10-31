package agents

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// HelloHook is called after an agent's hello was processed (facts stored, held
// results applied). It runs on its own goroutine, so it may use the database and
// the dispatcher freely; it must not assume the agent is still connected.
type HelloHook func(ctx context.Context, serverID uuid.UUID, hello agentprotocol.Hello)

type helloHooks struct {
	mu    sync.Mutex
	hooks []HelloHook
}

// OnHello registers a HelloHook. Call it during wiring, before agents connect.
func (h *Hub) OnHello(fn HelloHook) {
	h.helloHook.mu.Lock()
	h.helloHook.hooks = append(h.helloHook.hooks, fn)
	h.helloHook.mu.Unlock()
}

func (h *Hub) runHelloHooks(ctx context.Context, serverID uuid.UUID, hello agentprotocol.Hello) {
	h.helloHook.mu.Lock()
	hooks := append([]HelloHook(nil), h.helloHook.hooks...)
	h.helloHook.mu.Unlock()
	for _, fn := range hooks {
		go fn(context.WithoutCancel(ctx), serverID, hello)
	}
}
