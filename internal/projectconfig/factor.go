package projectconfig

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Factor turns one flat resolved view per environment into a single file: the
// base environment's view is the base file, and every other environment
// becomes an [environments.<slug>] block holding only what differs from it
// (merge rules: tables merge by key, scalars and arrays replace as a
// whole, leaves replace as a unit; `unset` lists the key paths the base has
// and the environment lacks).
//
// Services that exist in only some environments get `only_in`; their base
// definition is taken from the base view when it has the service, else from
// the first environment (by slug) that has it. The environment layer of the
// shared variables and secrets is written as is. A default server that
// differs from the base's is written as the environment's `server`; when an
// environment has no server while the base has one, the root `server` is
// dropped and every environment states its own.
//
// The property: for every environment e, Resolve(Parse(Factor(envs, base)), e)
// equals envs[e]. Factor does not mutate its inputs.
func Factor(envs map[string]*Resolved, base string, o RenderOptions) ([]byte, error) {
	if len(envs) == 0 {
		return nil, errors.New("projectconfig: factor: no environments")
	}
	slugs := make([]string, 0, len(envs))
	for slug := range envs {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		if envs[slug] == nil {
			return nil, fmt.Errorf("projectconfig: factor: environment %q has no resolved view", slug)
		}
	}
	b, ok := envs[base]
	if !ok {
		return nil, fmt.Errorf("projectconfig: factor: base environment %q is not among the environments (%s)", base, strings.Join(slugs, ", "))
	}

	one := SchemaVersion
	f := &File{Schema: &one}
	if b.Project != "" {
		f.Project = &b.Project
	}
	rootServer := b.Server
	if rootServer != "" {
		for _, e := range slugs {
			if envs[e].Server == "" {
				rootServer = ""
				break
			}
		}
	}
	if rootServer != "" {
		f.Server = &rootServer
	}
	if len(b.Shared) > 0 {
		f.Variables = b.Shared
	}
	f.Secrets = secretsDecl(b.SharedSecrets, b.SharedSecretScope)

	svcSet := map[string]bool{}
	for _, e := range slugs {
		for s, rs := range envs[e].Services {
			if rs != nil {
				svcSet[s] = true
			}
		}
	}
	svcSlugs := make([]string, 0, len(svcSet))
	for s := range svcSet {
		svcSlugs = append(svcSlugs, s)
	}
	sort.Strings(svcSlugs)

	overrides := map[string]*emitTable{}
	if len(svcSlugs) > 0 {
		f.Services = make(map[string]*Service, len(svcSlugs))
	}
	for _, s := range svcSlugs {
		var members []string
		for _, e := range slugs {
			if envs[e].Services[s] != nil {
				members = append(members, e)
			}
		}
		defEnv := members[0]
		if envs[base].Services[s] != nil {
			defEnv = base
		}
		def := serviceDefOf(envs[defEnv].Services[s])
		cp := *def
		cp.Unset = nil
		cp.OnlyIn = nil
		if len(members) < len(slugs) {
			cp.OnlyIn = append([]string(nil), members...)
		}
		f.Services[s] = &cp

		t0 := factorTree(def)
		for _, e := range members {
			ov := factorOverride(t0, factorTree(serviceDefOf(envs[e].Services[s])))
			if ov == nil {
				continue
			}
			if overrides[e] == nil {
				overrides[e] = newEmitTable()
			}
			overrides[e].set(s, ov)
		}
	}

	root := toTree(f)
	envTable := newEmitTable()
	for _, e := range slugs {
		r := envs[e]
		block := newEmitTable()
		if r.Server != "" && r.Server != rootServer {
			block.set("server", r.Server)
		}
		if len(r.EnvShared) > 0 {
			if n, ok := encodeNode(reflect.ValueOf(r.EnvShared)); ok {
				block.set("variables", n)
			}
		}
		if sec := secretsDecl(r.EnvSharedSecrets, r.EnvSharedSecretScope); sec != nil {
			block.set("secrets", toTree(sec))
		}
		if svc := overrides[e]; svc != nil {
			block.set("services", svc)
		}
		if len(block.keys) > 0 {
			envTable.set(e, block)
		}
	}
	if len(envTable.keys) > 0 {
		root.set("environments", envTable)
	}
	return emitDocument(root, o), nil
}

// factorTree is the tree of a service definition without the keys that are
// file semantics (only_in, unset) rather than configuration.
func factorTree(def *Service) *emitTable {
	t := toTree(def)
	t.remove("only_in")
	t.remove("unset")
	return t
}

// factorKeyRank is the position of every service key in the model, used to
// place `unset` where the model has it.
var factorKeyRank = func() map[string]int {
	rt := reflect.TypeOf(Service{})
	m := make(map[string]int, rt.NumField())
	for i := range rt.NumField() {
		if k := emitKeyOf(rt.Field(i)); k != "" {
			m[k] = i
		}
	}
	return m
}()

// factorOverride returns the override table that turns the service tree base
// into env under the merge rules, or nil when they are equal.
func factorOverride(base, env *emitTable) *emitTable {
	out := newEmitTable()
	var unset []string
	factorDiff(base, env, nil, out, &unset)
	if len(unset) > 0 {
		sort.Strings(unset)
		list := make([]any, len(unset))
		for i, u := range unset {
			list[i] = u
		}
		pos := len(out.keys)
		for i, k := range out.keys {
			if factorKeyRank[k] > factorKeyRank["unset"] {
				pos = i
				break
			}
		}
		out.insertAt(pos, "unset", list)
	}
	if len(out.keys) == 0 {
		return nil
	}
	return out
}

// factorDiff writes into out the keys of env whose value differs from base
// and appends to unset the paths of base keys that env lacks. rel is the key
// path of base and env relative to the service root.
func factorDiff(base, env *emitTable, rel []string, out *emitTable, unset *[]string) {
	for _, k := range env.keys {
		ev := env.vals[k]
		bv, ok := base.vals[k]
		if !ok {
			out.set(k, ev)
			continue
		}
		crel := childPath(rel, k)
		et, eok := ev.(*emitTable)
		bt, bok := bv.(*emitTable)
		if eok && bok && !et.inline && !bt.inline && !isMergeLeaf(crel) {
			sub := newEmitTable()
			factorDiff(bt, et, crel, sub, unset)
			if len(sub.keys) > 0 {
				out.set(k, sub)
			}
			continue
		}
		if !treesEqual(bv, ev) {
			out.set(k, ev)
		}
	}
	for _, k := range base.keys {
		if _, ok := env.vals[k]; !ok {
			*unset = append(*unset, PathString(childPath(rel, k)...))
		}
	}
}

// treesEqual compares two tree nodes; the key order of tables is irrelevant.
func treesEqual(a, b any) bool {
	switch x := a.(type) {
	case *emitTable:
		y, ok := b.(*emitTable)
		if !ok || x.inline != y.inline || len(x.keys) != len(y.keys) {
			return false
		}
		for _, k := range x.keys {
			yv, ok := y.vals[k]
			if !ok || !treesEqual(x.vals[k], yv) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !treesEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	return a == b
}
