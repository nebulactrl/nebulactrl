package engine

import (
	"sync"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The control plane numbers every whole route table it builds for this server
// (SyncRoutesPayload.TableSeq and its siblings; higher is built from newer
// state). Tables are applied by concurrent command handlers, so an old table
// that waited for routeMu behind a deploy's verification can be applied after a
// newer one and silently undo what the newer one changed: a maintenance
// window, a domain, a certificate. The engine therefore records the highest
// number it has applied and applies no table below it.
//
//   - 0 means unnumbered (an older control plane, a table the agent built
//     itself): always applied, and it does not move the record. The agent's own
//     deploy, scale and remove tables are built from the live table and never
//     numbered.
//   - The record is persisted by the proxy manager (tableSeqKeeper), so a
//     restarted agent keeps refusing tables older than the one it restored.
//   - A hello reconcile frame replaces the record with its own number, whatever
//     it was: the control plane is the authority, also after its database was
//     restored from a backup and its numbers went back (ObserveFrame).

// tableSeqKeeper is implemented by a proxy that persists the record
// (*proxy.Manager).
type tableSeqKeeper interface {
	LoadTableSeq() uint64
	SaveTableSeq(uint64)
}

// tableSeqState is the record. Its mutex is separate from routeMu so that the
// read loop can reset it on a hello frame while a deploy holds routeMu.
type tableSeqState struct {
	mu     sync.Mutex
	n      uint64
	loaded bool
}

// loadTableSeqLocked reads the persisted record once (tabSeq.mu held).
func (e *Engine) loadTableSeqLocked() {
	if e.tabSeq.loaded {
		return
	}
	e.tabSeq.loaded = true
	if k, ok := e.cfg.Proxy.(tableSeqKeeper); ok {
		e.tabSeq.n = k.LoadTableSeq()
	}
}

func (e *Engine) saveTableSeqLocked() {
	if k, ok := e.cfg.Proxy.(tableSeqKeeper); ok {
		k.SaveTableSeq(e.tabSeq.n)
	}
}

// tableSuperseded reports whether a table numbered seq is older than a table
// the engine has applied. Callers hold routeMu, so the answer holds until they
// have applied (or not) the table.
func (e *Engine) tableSuperseded(seq uint64) (superseded bool, newest uint64) {
	e.tabSeq.mu.Lock()
	defer e.tabSeq.mu.Unlock()
	e.loadTableSeqLocked()
	return seq != 0 && seq < e.tabSeq.n, e.tabSeq.n
}

// noteTable records an applied table. Called with routeMu held.
func (e *Engine) noteTable(seq uint64) {
	if seq == 0 {
		return
	}
	e.tabSeq.mu.Lock()
	defer e.tabSeq.mu.Unlock()
	e.loadTableSeqLocked()
	if seq > e.tabSeq.n {
		e.tabSeq.n = seq
		e.saveTableSeqLocked()
	}
}

// observeTableSeq takes the number of a hello frame as the record, lower or
// higher. Called on the connection's read loop, before any command that
// follows the frame is read.
func (e *Engine) observeTableSeq(desired agentprotocol.Reconcile) {
	if !desired.Hello {
		return
	}
	e.tabSeq.mu.Lock()
	defer e.tabSeq.mu.Unlock()
	e.loadTableSeqLocked()
	if e.tabSeq.n != desired.TableSeq {
		e.log.Info("route table numbering follows the control plane", "was", e.tabSeq.n, "now", desired.TableSeq)
		e.tabSeq.n = desired.TableSeq
		e.saveTableSeqLocked()
	}
}
