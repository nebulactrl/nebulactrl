package databases

import (
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// ExportProvider computes the virtual exports of managed databases
// (DATABASE_URL, REDIS_URL, …). Register it with services.RegisterExportProvider.
//
// The service's own exports (DATABASE_URL, DATABASE_HOST, …) always describe the
// service itself; on a replica that is the read-only server. DATABASE_WRITE_*
// and DATABASE_READ_* describe the replica group: the writer is the current
// primary, the readers are the streaming replicas the consumer reaches (those on
// its own server first, then the ones reached through the mesh), the writer when
// there are none. Ports are consumer-aware (services.MemberPort).
func ExportProvider(in services.ExportInput) []services.Export {
	if in.Kind != services.KindDatabase || in.Cfg.Database == nil {
		return nil
	}
	spec := in.Cfg.Database.Normalize()
	get := func(key string) (string, bool) {
		v, _, ok := in.Var(key)
		return v, ok
	}
	exs := engines.Exports(spec, in.Host, in.Port, get)
	if spec.Engine == engines.Postgres && len(in.Group) > 0 {
		exs = withGroup(exs, spec, get, in)
	}
	out := make([]services.Export, len(exs))
	for i, e := range exs {
		out[i] = services.Export{Key: e.Key, Value: e.Value, Secret: e.Secret, Description: e.Description}
	}
	return out
}

// withGroup replaces the standalone writer/reader exports by the group's.
func withGroup(exs []engines.Export, spec engines.Spec, get func(string) (string, bool), in services.ExportInput) []engines.Export {
	w, ok := services.GroupWriter(in.Group)
	if !ok {
		return exs
	}
	writer := engines.GroupTarget{Host: w.Slug, Port: services.MemberPort(in.Consumer, w)}
	var readers []engines.GroupTarget
	for _, m := range services.GroupReaders(in.Consumer, in.Group) {
		readers = append(readers, engines.GroupTarget{Host: m.Slug, Port: services.MemberPort(in.Consumer, m)})
	}
	grp := map[string]engines.Export{}
	for _, e := range engines.GroupExports(spec, get, writer, readers) {
		grp[e.Key] = e
	}
	for i, e := range exs {
		if g, ok := grp[e.Key]; ok {
			exs[i] = g
		}
	}
	return exs
}

// RegisterExports installs ExportProvider as the resolver's export provider.
func RegisterExports() { services.RegisterExportProvider(ExportProvider) }

// exportCatalog lists the exports a spec offers, without values.
func exportCatalog(spec engines.Spec) []engines.Export {
	exs := engines.Exports(spec.Normalize(), "host", spec.Port(), func(string) (string, bool) { return "", true })
	for i := range exs {
		exs[i].Value = ""
	}
	return exs
}
