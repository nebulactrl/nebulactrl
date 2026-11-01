package services

import (
	"sort"
	"sync"

	"github.com/google/uuid"
)

// Exports are virtual variables a service offers to references
// (`${{postgres.DATABASE_URL}}`): computed by the resolver, never stored,
// never injected into the exporting service and never a staged change.

// Export is one virtual variable of a service.
type Export struct {
	Key         string
	Value       string
	Secret      bool
	Description string
}

// ExportInput is what an ExportProvider sees of a service.
type ExportInput struct {
	Slug string
	Kind string
	Cfg  Config
	// Host is the service's private host and Port its port (0 when none).
	Host string
	Port int
	// Var returns a resolved user variable of the service (platform variables
	// are not included). secret reports whether the value is secret.
	Var func(key string) (val string, secret, ok bool)
	// Consumer is the server of the service that references the export (nil when
	// unknown or unplaced). A provider on another server is reached through its
	// mesh port, see GroupMember.MeshPort.
	Consumer *uuid.UUID
	// Group lists the replica group the service belongs to (primary and
	// replicas, including the service itself); empty for a standalone service.
	Group []GroupMember
}

// GroupMember is one member of a replica group as an export provider sees it.
type GroupMember struct {
	ID        uuid.UUID
	Slug      string
	IsPrimary bool
	// State is the replica lifecycle state (database_replicas.state); empty for the primary.
	State string
	// Port is the member's container port; ServerID its server; MeshPort the port
	// of its 'service' mesh exposure (0 = none), used by consumers on other servers.
	Port     int
	ServerID *uuid.UUID
	MeshPort int
}

// ExportProvider computes the exports of one service; it returns nil for
// services that export nothing. It must be pure and cheap.
type ExportProvider func(in ExportInput) []Export

var (
	exportMu       sync.RWMutex
	exportProvider ExportProvider
)

// RegisterExportProvider installs the export provider (the last registration
// wins) and returns the one it replaced (nil when none), so a test can restore
// it. Pass nil to remove it.
func RegisterExportProvider(fn ExportProvider) (prev ExportProvider) {
	exportMu.Lock()
	prev, exportProvider = exportProvider, fn
	exportMu.Unlock()
	return prev
}

func currentExportProvider() ExportProvider {
	exportMu.RLock()
	defer exportMu.RUnlock()
	return exportProvider
}

// export resolves the export `key` of sd for a reference.
func (st *resolveState) export(sd *ServiceData, key string) (memoVal, bool) {
	exs := st.exportsOf(sd)
	ex, ok := exs[key]
	if !ok {
		return memoVal{}, false
	}
	if st.consumer != nil && st.consumerServer != nil {
		// The consumer reaches whichever group members the export points at.
		for _, id := range ExportTargets(key, st.consumerServer, st.r.groupOf(sd), sd.ID) {
			if t := st.r.byID[id]; t != nil {
				st.noteUse(t)
			}
		}
	}
	return memoVal{val: ex.val, secret: ex.secret, leaves: ex.leaves}, true
}

type exportVal struct {
	val    string
	secret bool
	leaves []string
	desc   string
}

func (st *resolveState) exportsOf(sd *ServiceData) map[string]exportVal {
	if m, ok := st.exportMemo[sd.Slug]; ok {
		return m
	}
	if st.exportMemo == nil {
		st.exportMemo = map[string]map[string]exportVal{}
	}
	st.exportMemo[sd.Slug] = nil // guards against an export that reads its own exports
	fn := currentExportProvider()
	if fn == nil {
		return nil
	}
	var leaves []string
	in := ExportInput{
		Slug: sd.Slug, Kind: sd.Kind, Cfg: sd.Config, Host: sd.Slug, Port: EffectivePort(st.consumerServer, *sd),
		Consumer: st.consumerServer, Group: st.r.groupOf(sd),
		Var: func(key string) (string, bool, bool) {
			if _, ok := sd.Vars[key]; !ok || IsPlatformKey(key) {
				return "", false, false
			}
			mv, err := st.lookup(sd.Slug, key)
			if err != nil {
				return "", false, false
			}
			if mv.secret {
				leaves = append(leaves, mv.val)
				leaves = append(leaves, mv.leaves...)
			}
			return mv.val, mv.secret, true
		},
	}
	list := fn(in)
	if len(list) == 0 {
		return nil
	}
	m := make(map[string]exportVal, len(list))
	for _, ex := range list {
		ev := exportVal{val: ex.Value, secret: ex.Secret, desc: ex.Description}
		if ex.Secret {
			ev.leaves = append([]string(nil), leaves...)
		}
		m[ex.Key] = ev
	}
	st.exportMemo[sd.Slug] = m
	return m
}

// ExportInfo describes an export for listings (no value for secrets).
type ExportInfo struct {
	Key         string
	Description string
	Secret      bool
}

// Exports lists the exports of the service with slug, sorted by key.
func (r *Resolver) Exports(slug string) []ExportInfo {
	sd := r.services[slug]
	if sd == nil {
		return nil
	}
	st := newState(r)
	var out []ExportInfo
	for k, v := range st.exportsOf(sd) {
		out = append(out, ExportInfo{Key: k, Description: v.desc, Secret: v.secret})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
