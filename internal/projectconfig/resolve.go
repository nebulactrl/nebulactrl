package projectconfig

import (
	"path"
	"reflect"
	"sort"
	"strings"
)

// Resolve applies only_in, the merge, unset and server precedence for one
// environment ("" = the base definitions only), inlines attachments and runs
// the static validators on the result. Provenance (Resolved.Pos, FieldValue.Pos)
// is the line that set the winning value.
//
// Parse already reports the diagnostics of the base and of every environment
// block; Resolve returns only the ones Parse did not report (normally none), so
// the caller can concatenate both lists. The returned Resolved is never nil
// for a File built by Parse, even when diagnostics contain errors: services
// whose body could not be decoded are present with an empty Fields map.
func (f *File) Resolve(env string, idx *Index) (*Resolved, Diagnostics) {
	if f == nil || f.meta == nil {
		return nil, nil
	}
	res, ds := f.view(env)
	if idx != nil {
		res.index = idx
	}
	var out Diagnostics
	for _, d := range ds {
		if f.meta.baseKeys[diagKey(d)] {
			continue
		}
		k := diagKey(d)
		if env != "" {
			d.Environment = env
			k += "\x00" + env
		}
		if f.meta.reported[k] {
			continue
		}
		out = append(out, d)
	}
	return res, out
}

// view is one resolution pass: a base or environment view of the document.
type view struct {
	f    *File
	env  string
	s    *sink
	res  *Resolved
	svcs map[string]*svcCtx
	// order of services as resolved (sorted slugs)
	slugs []string
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func orRuntime(p *string) string {
	if p == nil || *p == "" {
		return "runtime"
	}
	return *p
}

func (f *File) view(env string) (*Resolved, Diagnostics) {
	m := f.meta
	s := &sink{pos: func(p string, value bool) Pos { return m.idx.at(p, value) }}
	v := &view{f: f, env: env, s: s, svcs: map[string]*svcCtx{}}
	res := &Resolved{
		Environment: env,
		Services:    map[string]*ResolvedService{},
		Shared:      map[string]Variable{},
		EnvShared:   map[string]Variable{},
		index:       m.idx,
	}
	v.res = res
	res.Project = derefStr(f.Project)
	res.Server = derefStr(f.Server)
	for k, val := range f.Variables {
		res.Shared[k] = val
	}
	if f.Secrets != nil {
		res.SharedSecrets = append([]string(nil), f.Secrets.Keys...)
	}
	res.SharedSecretScope = orRuntime(nil)
	if f.Secrets != nil {
		res.SharedSecretScope = orRuntime(f.Secrets.Scope)
	}
	res.EnvSharedSecretScope = "runtime"
	var block *Environment
	if env != "" {
		block = f.EnvironmentBlocks[env]
	}
	if block != nil {
		if block.Server != nil {
			res.Server = *block.Server
		}
		for k, val := range block.Variables {
			res.EnvShared[k] = val
		}
		if block.Secrets != nil {
			res.EnvSharedSecrets = append([]string(nil), block.Secrets.Keys...)
			res.EnvSharedSecretScope = orRuntime(block.Secrets.Scope)
		}
	}

	v.validateRoot(block)

	// Services: the union of base services and (for an environment) override blocks.
	seen := map[string]bool{}
	var slugs []string
	if base, ok := m.tree["services"].(map[string]any); ok {
		for slug := range base {
			seen[slug] = true
			slugs = append(slugs, slug)
		}
	}
	if env != "" {
		if eb, ok := lookupTree(m.tree, []string{"environments", env, "services"}); ok {
			if em, ok := eb.(map[string]any); ok {
				for slug := range em {
					if !seen[slug] {
						seen[slug] = true
						slugs = append(slugs, slug)
					}
				}
			}
		}
	}
	sort.Strings(slugs)
	if m.overLimit {
		slugs = nil // a document Parse refused for its size or depth is not walked again
	}
	for _, slug := range slugs {
		v.service(slug)
	}
	v.slugs = make([]string, 0, len(v.svcs))
	for slug := range v.svcs {
		v.slugs = append(v.slugs, slug)
	}
	sort.Strings(v.slugs)
	v.crossService()
	return res, s.list
}

func asMap(n any) (map[string]any, bool) {
	m, ok := n.(map[string]any)
	return m, ok
}

// svcCtx carries one service through validation and field assembly.
type svcCtx struct {
	v    *view
	slug string
	def  *Service
	kind string
	prov provMap
	root string // "services.web": the file key path the service is rooted at
	rs   *ResolvedService
	// refs found in variable values, for the cross-service pass
	refs []varRef
}

// abs maps a path relative to the service to the file key path of the line that
// supplied it (nearest known ancestor when the key itself is absent).
func (c *svcCtx) abs(rel ...string) string {
	for n := len(rel); n >= 0; n-- {
		if p, ok := c.prov[PathString(rel[:n]...)]; ok {
			for _, seg := range rel[n:] {
				p = join(p, seg)
			}
			return p
		}
	}
	p := c.root
	for _, seg := range rel {
		p = join(p, seg)
	}
	return p
}

func (c *svcCtx) err(code string, rel []string, format string, a ...any) *Diagnostic {
	return c.v.s.errorf(code, c.abs(rel...), format, a...)
}

func (c *svcCtx) warn(code string, rel []string, format string, a ...any) *Diagnostic {
	return c.v.s.warnf(code, c.abs(rel...), format, a...)
}

func rp(segs ...string) []string { return segs }

func (v *view) service(slug string) {
	m := v.f.meta
	baseN, _ := lookupTree(m.tree, []string{"services", slug})
	bm, bok := asMap(baseN)
	var om map[string]any
	var ook bool
	if v.env != "" {
		overN, _ := lookupTree(m.tree, []string{"environments", v.env, "services", slug})
		om, ook = asMap(overN)
	}
	if !bok && !ook {
		return
	}
	if bok && v.env != "" {
		if oi, ok := bm["only_in"].([]any); ok {
			in := false
			for _, e := range oi {
				if s, _ := e.(string); s == v.env {
					in = true
				}
			}
			if !in {
				return
			}
		}
	}
	mg := &merger{prov: provMap{}}
	var bn, on any
	if bok {
		bn = bm
	}
	if ook {
		on = om
	}
	baseRoot := []string{"services", slug}
	overRoot := []string{"environments", v.env, "services", slug}
	merged, _ := mg.merge(bn, on, nil, baseRoot, overRoot).(map[string]any)
	if merged == nil {
		return
	}
	c := &svcCtx{v: v, slug: slug, prov: mg.prov}
	if bok {
		c.root = PathString(baseRoot...)
	} else {
		c.root = PathString(overRoot...)
	}
	delete(merged, "unset")
	if ook {
		c.applyUnset(om, merged, overRoot)
	}

	def := &Service{}
	ds := &sink{}
	dec := &decoder{s: ds, loc: func(p []string) string { return c.abs(p...) }, logical: []string{"services", slug}, idx: m.idx}
	dec.structInto(reflect.ValueOf(def).Elem(), merged, nil)
	c.def = def
	c.kind = inferKind(def)
	rs := &ResolvedService{Slug: slug, Kind: c.kind, Def: def, Path: c.root, Fields: map[string]FieldValue{}}
	rs.ServerName = derefStr(def.Server)
	if def.Server == nil {
		rs.ServerName = v.res.Server
	}
	c.rs = rs
	v.svcs[slug] = c
	v.res.Services[slug] = rs
	if dec.errs > 0 {
		// The body is not decodable; the diagnostics were reported on the
		// document pass. Skip validation to avoid cascades.
		return
	}

	v.validateService(c)
	c.assembleFields()
}

// applyUnset removes the override's unset paths from the merged tree.
func (c *svcCtx) applyUnset(om, merged map[string]any, overRoot []string) {
	list, ok := om["unset"].([]any)
	if !ok {
		return
	}
	s := c.v.s
	for i, e := range list {
		ap := PathString(append(append([]string(nil), overRoot...), "unset", "["+itoa(i)+"]")...)
		str, ok := e.(string)
		if !ok {
			continue // reported as wrong_type by the document pass
		}
		segs, ok := splitKeyPath(str)
		if !ok {
			s.errorf(CodeInvalidUnsetPath, ap, "%q is not a valid TOML key path", str).Hint = `write dotted key paths and quote segments that contain dots: domains."shop.acme.com"`
			continue
		}
		if segs[0] == "unset" || segs[0] == "only_in" {
			s.errorf(CodeInvalidUnsetPath, ap, "%q cannot be unset", str)
			continue
		}
		if !unsetPathValid(segs) {
			g := s.errorf(CodeInvalidUnsetPath, ap, "%q is not a key of a service", str)
			if sug := Suggest(append([]string{"services", c.slug}, segs[:len(segs)-1]...), segs[len(segs)-1]); len(sug) > 0 {
				g.Suggestions = sug
				g.Hint = "write `" + sug[0] + "`"
			}
			continue
		}
		if !deleteTree(merged, segs) {
			s.warnf(CodeInvalidUnsetPath, ap, "nothing to unset: %q is not set for service %q in this environment", str, c.slug)
		}
	}
}

func inferKind(d *Service) string {
	switch {
	case d.Image != nil:
		return KindImage
	case d.Git != nil:
		return KindGit
	case d.Database != nil:
		return KindDatabase
	case d.Route != nil:
		return KindRoute
	case d.Stack != nil:
		return KindStack
	}
	return ""
}

// lookupAttachment finds an attachment by the path as written, then cleaned.
func lookupAttachment(att Attachments, p string) ([]byte, bool) {
	if b, ok := att[p]; ok {
		return b, true
	}
	c := path.Clean(p)
	if b, ok := att[c]; ok {
		return b, true
	}
	if b, ok := att["./"+c]; ok {
		return b, true
	}
	c = strings.TrimPrefix(c, "./")
	b, ok := att[c]
	return b, ok
}
