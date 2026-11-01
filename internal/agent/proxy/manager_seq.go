package proxy

// TableSeqStore persists the number of the newest whole route table the agent
// applied (see the engine's tableseq.go). *store.Store implements it; a
// RouteStore that does not keeps the record in memory only.
type TableSeqStore interface {
	SaveTableSeq(uint64) error
	LoadTableSeq() (uint64, error)
}

// LoadTableSeq returns the persisted record (0 when none).
func (m *Manager) LoadTableSeq() uint64 {
	s, ok := m.cfg.Store.(TableSeqStore)
	if !ok {
		return 0
	}
	n, err := s.LoadTableSeq()
	if err != nil {
		m.log.Error("load route table number", "err", err)
		return 0
	}
	return n
}

// SaveTableSeq persists the record.
func (m *Manager) SaveTableSeq(n uint64) {
	s, ok := m.cfg.Store.(TableSeqStore)
	if !ok {
		return
	}
	if err := s.SaveTableSeq(n); err != nil {
		m.log.Error("persist route table number", "err", err)
	}
}
