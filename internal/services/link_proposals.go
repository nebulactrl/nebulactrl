package services

import (
	"context"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// linkKeys are the variable names a consumer would hold the references under.
type linkKeys struct{ host, port, url, user, name, password string }

func slugPrefix(slug string) string {
	return strings.ToUpper(strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, slug)) + "_"
}

// LinkRefs lists the references a consumer can hold to reach the service with
// the given slug, kind and config. It is the one source of both the link
// review (POST/GET links) and the MCP add_database composite, so the
// recommended keys never drift apart.
//
// A managed database offers its connection URL, host and port as the
// recommended set (the URL is a derived export, so consumers follow credential
// rotation) and the user, database name and password as optional extras.
// Any other service offers its private host and port (recommended), an
// internal URL and its public domain (optional).
//
// prefix, when set, replaces the default key names: "<prefix>_HOST", "_PORT",
// "_URL", "_USER", "_NAME" and "_PASSWORD". Results are ordered: recommended
// first, URL leading for a database.
func LinkRefs(slug, kind string, cfg Config, prefix string) []ProposedVariable {
	ref := func(key string) string { return "${{" + slug + "." + key + "}}" }
	if cfg.Database != nil && kind == KindDatabase {
		return databaseLinkRefs(ref, cfg.Database.Engine, prefix)
	}
	p := slugPrefix(slug)
	if prefix != "" {
		p = prefix + "_"
	}
	out := []ProposedVariable{{
		Key: p + "HOST", Value: ref(VarPrivateHost), Recommended: true,
		Description: "The service's hostname on the environment's private network.",
	}}
	if cfg.Port > 0 {
		out = append(out, ProposedVariable{
			Key: p + "PORT", Value: ref(VarPort), Recommended: true,
			Description: "The port the service listens on.",
		})
		out = append(out, ProposedVariable{
			Key:         p + "URL",
			Value:       "http://" + ref(VarPrivateHost) + ":" + ref(VarPort),
			Description: "Private address as a URL.",
		})
	}
	out = append(out, ProposedVariable{
		Key: p + "DOMAIN", Value: ref(VarPublicDomain),
		Description: "The service's first public domain, empty when it has none.",
	})
	return out
}

func databaseLinkRefs(ref func(string) string, engine, prefix string) []ProposedVariable {
	lp := engines.LinkPrefix(engine)
	k := linkKeys{host: lp + "HOST", port: lp + "PORT", url: engines.PrimaryExport(engine), user: lp + "USER", name: lp + "NAME", password: lp + "PASSWORD"}
	if prefix != "" {
		k = linkKeys{
			host: prefix + "_HOST", port: prefix + "_PORT", url: prefix + "_URL",
			user: prefix + "_USER", name: prefix + "_NAME", password: prefix + "_PASSWORD",
		}
	}
	exports := map[string]engines.Export{}
	for _, e := range engines.ExportList(engine) {
		exports[e.Key] = e
	}
	primary := engines.PrimaryExport(engine)
	var out []ProposedVariable
	add := func(key, value string, recommended bool, e engines.Export) {
		out = append(out, ProposedVariable{Key: key, Value: value, Recommended: recommended, Description: e.Description, Secret: e.Secret})
	}
	add(k.url, ref(primary), true, exports[primary])
	add(k.host, ref(VarPrivateHost), true, engines.Export{Description: "Private hostname of the database."})
	// The database port is published as an export (DATABASE_PORT / REDIS_PORT).
	if e, ok := exports[lp+"PORT"]; ok {
		add(k.port, ref(lp+"PORT"), true, e)
	}
	for _, extra := range []struct{ key, export string }{{k.user, lp + "USER"}, {k.name, lp + "NAME"}, {k.password, lp + "PASSWORD"}} {
		if e, ok := exports[extra.export]; ok {
			add(extra.key, ref(extra.export), false, e)
		}
	}
	return out
}

// ProposeLinkVariables lists the references a service needs to reach the
// target service: the recommended set first, then the optional ones.
func ProposeLinkVariables(slug, kind string, cfg Config) []ProposedVariable {
	return LinkRefs(slug, kind, cfg, "")
}

// RecommendedLinkRefs is the preselected subset of LinkRefs.
func RecommendedLinkRefs(slug, kind string, cfg Config, prefix string) []ProposedVariable {
	var out []ProposedVariable
	for _, p := range LinkRefs(slug, kind, cfg, prefix) {
		if p.Recommended {
			out = append(out, p)
		}
	}
	return out
}

// LinkPreview is what linking one service to another would propose, without
// creating anything: the connect popover reads it before the reader decides.
type LinkPreview struct {
	// ProposedVariables is the full list with each row's status on the source.
	ProposedVariables []ProposedVariable `json:"proposedVariables"`
	Warnings          []string           `json:"warnings"`
	// ExistingKeys are the variable names the source already has, sorted, so a
	// renamed row can be checked for a clash.
	ExistingKeys []string `json:"existingKeys"`
	// Linked reports an edge from the source to the target already exists, by
	// hand or through references.
	Linked bool `json:"linked"`
	// Mesh is what the edge would need from the mesh when the two services run
	// on different servers: links, devices added, blockers.
	Mesh MeshPlan `json:"mesh"`
}

// PreviewLink proposes the references the consumer fromID would hold to reach
// toID, marks which of them the consumer already has, and writes nothing.
func (s *Service) PreviewLink(ctx context.Context, envID, fromID, toID uuid.UUID) (LinkPreview, error) {
	if _, err := LoadEnvironment(ctx, s.q, policy.ScopeProjectsWrite, envID); err != nil {
		return LinkPreview{}, err
	}
	if fromID == toID {
		return LinkPreview{}, apierr.Unprocessable("invalid_link", "a service cannot link to itself")
	}
	from, err := LoadScope(ctx, s.q, policy.ScopeProjectsWrite, fromID)
	if err != nil {
		return LinkPreview{}, err
	}
	to, err := LoadScope(ctx, s.q, policy.ScopeProjectsWrite, toID)
	if err != nil {
		return LinkPreview{}, err
	}
	if from.EnvironmentID != envID || to.EnvironmentID != envID {
		return LinkPreview{}, apierr.Unprocessable("invalid_link", "both services must belong to this environment")
	}
	raw, err := LoadRawVars(ctx, s.q, s.box, fromID)
	if err != nil {
		return LinkPreview{}, err
	}
	out := LinkPreview{
		ProposedVariables: ProposeLinkVariables(to.Service.Slug, to.Service.Kind, to.Config()),
		Warnings:          []string{},
		ExistingKeys:      make([]string, 0, len(raw)),
	}
	for k := range raw {
		out.ExistingKeys = append(out.ExistingKeys, k)
	}
	sort.Strings(out.ExistingKeys)
	for i := range out.ProposedVariables {
		p := &out.ProposedVariables[i]
		switch cur, ok := raw[p.Key]; {
		case !ok:
			p.Status = "new"
		case cur.Value == p.Value:
			p.Status = "unchanged"
		default:
			p.Status = "replaces"
		}
	}
	out.Mesh = EmptyMeshPlan()
	if s.mesh != nil {
		plan, err := s.mesh.PlanEdge(ctx, envID, fromID, toID)
		if err != nil {
			return LinkPreview{}, err
		}
		out.Mesh = plan
	}
	links, err := s.ListLinks(ctx, envID)
	if err != nil {
		return LinkPreview{}, err
	}
	for _, l := range links {
		if l.FromServiceID == fromID && l.ToServiceID == toID {
			out.Linked = true
			break
		}
	}
	return out, nil
}
