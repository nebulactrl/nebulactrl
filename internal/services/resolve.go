package services

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// Platform-provided variables. Users cannot define them (any key starting with
// NEBULA_, and PORT, is reserved).
const (
	VarPort            = "PORT"
	VarServiceName     = "NEBULA_SERVICE_NAME"
	VarEnvironment     = "NEBULA_ENVIRONMENT"
	VarPrivateHost     = "NEBULA_PRIVATE_HOST"
	VarPublicDomain    = "NEBULA_PUBLIC_DOMAIN"
	VarPublicURL       = "NEBULA_PUBLIC_URL"
	VarProjectName     = "NEBULA_PROJECT_NAME"
	VarServiceID       = "NEBULA_SERVICE_ID"
	VarEnvironmentName = "NEBULA_ENVIRONMENT_NAME"
	VarGitCommitSHA    = "NEBULA_GIT_COMMIT_SHA"
	VarGitBranch       = "NEBULA_GIT_BRANCH"
	VarImage           = "NEBULA_IMAGE"
	VarDeploymentID    = "NEBULA_DEPLOYMENT_ID"
	VarReleaseID       = "NEBULA_RELEASE_ID"
	VarReplicaIndex    = "NEBULA_REPLICA_INDEX"
	VarReplicaCount    = "NEBULA_REPLICA_COUNT"
	maxRefDepthGuard   = 64
)

// Variable scopes: where a variable is injected.
const (
	ScopeRuntime = "runtime"
	ScopeBuild   = "build"
	ScopeBoth    = "both"
)

// ValidScope reports whether s is a variable scope.
func ValidScope(s string) bool { return s == ScopeRuntime || s == ScopeBuild || s == ScopeBoth }

// Platform variable categories.
const (
	CategoryRelease     = "release"
	CategoryReleaseSelf = "release-self"
	CategoryDeployment  = "deployment"
	CategoryContainer   = "container"
)

// PlatformInfo describes one platform variable.
type PlatformInfo struct {
	Key         string
	Description string
	// Category tells when the value is known: release (resolver, frozen in the
	// release), release-self (known once the source is pinned; only the service
	// itself sees it), deployment (set per deployment, not frozen) or container
	// (set by the agent per replica).
	Category string
	// Referenceable is true when other services (and templates) may reference it.
	Referenceable bool
}

// PlatformVars lists every platform variable in display order.
var PlatformVars = []PlatformInfo{
	{VarPort, "The port the service listens on (omitted when the service has no port).", CategoryRelease, true},
	{VarServiceName, "The service's display name.", CategoryRelease, true},
	{VarEnvironment, "The slug of the environment the service runs in.", CategoryRelease, true},
	{VarPrivateHost, "The service's hostname on the environment's private network.", CategoryRelease, true},
	{VarPublicDomain, "The service's first public domain, or empty.", CategoryRelease, true},
	{VarPublicURL, "https:// plus the first public domain, or empty.", CategoryRelease, true},
	{VarProjectName, "The project's name.", CategoryRelease, true},
	{VarServiceID, "The service's id.", CategoryRelease, true},
	{VarEnvironmentName, "The environment's display name.", CategoryRelease, true},
	{VarGitCommitSHA, "The commit that was built (git services; empty for images). Runtime only; not passed to builds.", CategoryReleaseSelf, false},
	{VarGitBranch, "The configured git branch (git services; empty for images). Runtime only; not passed to builds.", CategoryReleaseSelf, false},
	{VarImage, "The image reference of the release.", CategoryReleaseSelf, false},
	{VarDeploymentID, "The id of the deployment that started the container.", CategoryDeployment, false},
	{VarReleaseID, "The id of the release the container runs.", CategoryDeployment, false},
	{VarReplicaIndex, "The replica's 0-based index.", CategoryContainer, false},
	{VarReplicaCount, "The number of replicas of the service.", CategoryContainer, false},
}

// IsPlatformKey reports whether key is reserved: PORT and everything under the
// NEBULA_ prefix, so platform variables can be added without breaking users.
func IsPlatformKey(key string) bool {
	return key == VarPort || strings.HasPrefix(key, "NEBULA_")
}

// Reserved service slugs: reference namespaces that are not services.
var ReservedSlugs = []string{varexpr.NSShared, varexpr.NSTeam, varexpr.ExtScheme}

// IsReservedSlug reports whether slug is a reserved reference namespace.
func IsReservedSlug(slug string) bool {
	return slug == varexpr.NSShared || slug == varexpr.NSTeam || slug == varexpr.ExtScheme
}

// VarDef is one user-defined variable as written.
type VarDef struct {
	Value  string
	Secret bool
	// Scope is runtime (default), build or both.
	Scope string
}

// SharedDef is one shared variable as seen from the resolver's environment
// (the environment layer already overrides the project layer).
type SharedDef struct {
	Value  string
	Secret bool
	// Layer is "environment", "project" or "team".
	Layer string
}

// ServiceData is what the resolver needs to know about one service.
type ServiceData struct {
	ID          uuid.UUID
	Slug        string
	Name        string
	Kind        string
	Port        int
	Config      Config
	FirstDomain string
	Vars        map[string]VarDef
	// ServerID is the server the service runs on (nil = not placed). With
	// MeshPort it lets a consumer on another server resolve the provider's port
	// (cross-server service links).
	ServerID *uuid.UUID
	// MeshPort is the port of the service's 'service' mesh exposure on its
	// server node (0 = none). A consumer on another server reaches the service at
	// <slug>:MeshPort instead of <slug>:Port.
	MeshPort int
	// Replica is the service's replica-group membership as a replica (nil for a
	// standalone database and for a primary).
	Replica *ReplicaMember
}

// ReplicaMember says that a database service is a read replica of PrimaryID and
// in which lifecycle state (database_replicas.state).
type ReplicaMember struct {
	PrimaryID uuid.UUID
	State     string
}

// ResolverContext is the environment-level context of a resolver.
type ResolverContext struct {
	ProjectName string
	EnvSlug     string
	EnvName     string
	// Shared holds the effective shared variables of the environment.
	Shared map[string]SharedDef
	// Team holds the team's shared variables (${{team.KEY}}).
	Team map[string]SharedDef
	// Integrations names the team's external secret manager integrations. A
	// name is present when the integration exists; its value says whether this
	// project and environment may use it (integration scoping). Nil skips both
	// checks (tests, plain resolvers).
	Integrations map[string]bool
	// IntegrationKinds maps an integration name to its kind (vault, infisical,
	// doppler). Nil skips the check that a reference's scheme matches the kind.
	IntegrationKinds map[string]string
	// Fetch resolves external references to their values. Nil defers them: the
	// value stays a reference (Resolved.Ext) that is fetched at deploy time.
	Fetch func(varexpr.ExtRef) (string, error)
}

// Resolver expands variable templates within one environment.
type Resolver struct {
	rc       ResolverContext
	services map[string]*ServiceData
	byID     map[uuid.UUID]*ServiceData
}

// MeshUse is a remote provider a consumer's variables reach: the provider runs
// on a server other than the consumer's, so the consumer dials it through the
// mesh (cross-server service links). Derived while resolving, in the consumer's
// context.
type MeshUse struct {
	ProviderID   uuid.UUID
	ProviderSlug string
	// ProviderServer is the server the provider runs on.
	ProviderServer uuid.UUID
	// Port is the provider's container port; MeshPort the port of its 'service'
	// exposure (0 = none yet).
	Port, MeshPort int
	// Pending is true while the provider has no exposure on its current server:
	// the consumer would resolve the container port, which is unreachable.
	Pending bool
	// Keys lists the variables of the consumer whose resolution reached the provider.
	Keys []string
}

// EffectivePort is the port a consumer on server consumer dials to reach p: the
// port of p's service exposure when p runs on another server and has one, else
// p's container port. An unplaced consumer (nil) and a provider on the
// consumer's server always use the container port.
func EffectivePort(consumer *uuid.UUID, p ServiceData) int {
	if p.MeshPort > 0 && consumer != nil && p.ServerID != nil && *p.ServerID != *consumer {
		return p.MeshPort
	}
	return p.Port
}

// remoteOf reports whether p is reached through the mesh from consumer.
func remoteOf(consumer *uuid.UUID, p *ServiceData) bool {
	return consumer != nil && p.ServerID != nil && *p.ServerID != *consumer
}

// NewResolver builds a resolver over the services of one environment.
func NewResolver(envSlug string, svcs []ServiceData) *Resolver {
	return NewResolverCtx(ResolverContext{EnvSlug: envSlug, EnvName: envSlug}, svcs)
}

// NewResolverCtx builds a resolver with full environment context.
func NewResolverCtx(rc ResolverContext, svcs []ServiceData) *Resolver {
	m := make(map[string]*ServiceData, len(svcs))
	ids := make(map[uuid.UUID]*ServiceData, len(svcs))
	for i := range svcs {
		m[svcs[i].Slug] = &svcs[i]
		ids[svcs[i].ID] = &svcs[i]
	}
	return &Resolver{rc: rc, services: m, byID: ids}
}

// Resolved is the fully expanded environment of one service.
type Resolved struct {
	// Vars holds every variable (platform and user) after expansion.
	Vars map[string]string
	// Secret marks variables whose value is secret: declared secret, or
	// derived from a secret through a reference.
	Secret map[string]bool
	// Scope holds the scope of user variables whose scope is not runtime.
	Scope map[string]string
	// Ext lists the variables that depend on an external secret manager: the
	// stored template that rebuilds the value at deploy time. Their entries in
	// Vars hold only the reference text (see ExtTpl.Display), never a value.
	Ext map[string]ExtTpl
	// Leaves lists every secret value that was substituted into a variable
	// (pre- and post-transform), so the log redactor also masks a bare password
	// that only ever appears inside a URL or an encoded form.
	Leaves []string
	// MeshUses lists the remote providers (other servers) the resolution of this
	// service reached, sorted by slug.
	MeshUses []MeshUse
}

// Problem is a variable that failed to resolve.
type Problem struct {
	Key     string
	Code    string
	Message string
	Details map[string]any
}

// platform returns the release-scoped platform variables of a service.
func (r *Resolver) platform(sd *ServiceData) map[string]string {
	m := map[string]string{
		VarServiceName:     sd.Name,
		VarEnvironment:     r.rc.EnvSlug,
		VarPrivateHost:     sd.Slug,
		VarPublicDomain:    sd.FirstDomain,
		VarPublicURL:       "",
		VarProjectName:     r.rc.ProjectName,
		VarServiceID:       sd.ID.String(),
		VarEnvironmentName: r.rc.EnvName,
	}
	if sd.FirstDomain != "" {
		m[VarPublicURL] = "https://" + sd.FirstDomain
	}
	if sd.Port > 0 {
		m[VarPort] = strconv.Itoa(sd.Port)
	}
	return m
}

// PlatformValues returns the release-scoped platform variables of the service
// with slug.
func (r *Resolver) PlatformValues(slug string) map[string]string {
	sd := r.services[slug]
	if sd == nil {
		return nil
	}
	return r.platform(sd)
}

// Service returns the resolver's data of a service.
func (r *Resolver) Service(slug string) (ServiceData, bool) {
	sd := r.services[slug]
	if sd == nil {
		return ServiceData{}, false
	}
	return *sd, true
}

// Slugs lists the services of the environment, sorted.
func (r *Resolver) Slugs() []string {
	out := make([]string, 0, len(r.services))
	for s := range r.services {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Shared returns the effective shared variable key.
func (r *Resolver) Shared(key string) (SharedDef, bool) {
	d, ok := r.rc.Shared[key]
	return d, ok
}

type memoVal struct {
	val    string
	secret bool
	leaves []string
	// ext is set when the value depends on an external secret manager and the
	// resolver has no fetcher: val then holds ext.Display().
	ext ExtTpl
}

type resolveState struct {
	r       *Resolver
	memo    map[string]memoVal
	visit   map[string]bool
	visitor []string

	exportMemo map[string]map[string]exportVal

	// consumer is the service whose environment is being resolved and
	// consumerServer its server: every port and export is resolved for that
	// consumer (a provider on another server is reached through the mesh). The
	// memo is per state, so a state never mixes consumers. Nil for states that
	// have no consumer (listings, shared variables).
	consumer       *ServiceData
	consumerServer *uuid.UUID
	uses           map[uuid.UUID]*MeshUse
}

func newState(r *Resolver) *resolveState {
	return &resolveState{r: r, memo: map[string]memoVal{}, visit: map[string]bool{}}
}

// newConsumerState is a state that resolves for the service with the given
// data (nil sd behaves like newState).
func newConsumerState(r *Resolver, sd *ServiceData) *resolveState {
	st := newState(r)
	if sd != nil {
		st.consumer, st.consumerServer = sd, sd.ServerID
	}
	return st
}

// noteUse records that the consumer reaches provider p (through key, a variable
// of the consumer: the root of the current evaluation).
func (st *resolveState) noteUse(p *ServiceData) {
	if st.consumer == nil || p.ID == st.consumer.ID || !remoteOf(st.consumerServer, p) {
		return
	}
	if st.uses == nil {
		st.uses = map[uuid.UUID]*MeshUse{}
	}
	u := st.uses[p.ID]
	if u == nil {
		u = &MeshUse{ProviderID: p.ID, ProviderSlug: p.Slug, ProviderServer: *p.ServerID, Port: p.Port, MeshPort: p.MeshPort, Pending: p.MeshPort == 0}
		st.uses[p.ID] = u
	}
	if len(st.visitor) > 0 {
		_, key, _ := strings.Cut(st.visitor[0], ".")
		for _, k := range u.Keys {
			if k == key {
				return
			}
		}
		u.Keys = append(u.Keys, key)
	}
}

// meshUses returns the recorded uses, sorted by provider slug.
func (st *resolveState) meshUses() []MeshUse {
	if len(st.uses) == 0 {
		return nil
	}
	out := make([]MeshUse, 0, len(st.uses))
	for _, u := range st.uses {
		sort.Strings(u.Keys)
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProviderSlug < out[j].ProviderSlug })
	return out
}

// ResolveService expands every variable of the service with the given slug. The
// first problem fails the whole resolution.
func (r *Resolver) ResolveService(slug string) (Resolved, error) {
	res, probs, err := r.resolveService(slug, false)
	if err != nil {
		return Resolved{}, err
	}
	if len(probs) > 0 {
		return Resolved{}, probs[0].asError()
	}
	return res, nil
}

// ResolveServiceLenient expands what can be expanded and reports the rest as
// problems (variables that fail are omitted).
func (r *Resolver) ResolveServiceLenient(slug string) (Resolved, []Problem, error) {
	return r.resolveService(slug, true)
}

// Diagnose reports every variable of the service that does not resolve.
func (r *Resolver) Diagnose(slug string) ([]Problem, error) {
	_, probs, err := r.resolveService(slug, true)
	return probs, err
}

func (p Problem) asError() error {
	e := apierr.Unprocessable(p.Code, p.Message)
	if p.Details != nil {
		e = e.WithDetails(p.Details)
	}
	return e
}

func (r *Resolver) resolveService(slug string, lenient bool) (Resolved, []Problem, error) {
	sd := r.services[slug]
	if sd == nil {
		return Resolved{}, nil, apierr.NotFound("service")
	}
	st := newConsumerState(r, sd)
	out := Resolved{Vars: map[string]string{}, Secret: map[string]bool{}, Scope: map[string]string{}}
	keys := make([]string, 0, len(sd.Vars))
	for k := range sd.Vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var probs []Problem
	leafSet := map[string]struct{}{}
	for _, k := range keys {
		mv, err := st.lookup(slug, k)
		if err != nil {
			ae, ok := apierr.As(err)
			if !ok {
				return Resolved{}, nil, err
			}
			probs = append(probs, Problem{Key: k, Code: ae.Code, Message: ae.Message, Details: ae.Details})
			if !lenient {
				return out, probs, nil
			}
			continue
		}
		out.Vars[k] = mv.val
		if mv.ext != nil {
			if out.Ext == nil {
				out.Ext = map[string]ExtTpl{}
			}
			out.Ext[k] = mv.ext
		}
		if sc := sd.Vars[k].Scope; sc != "" && sc != ScopeRuntime {
			out.Scope[k] = sc
		}
		if mv.secret {
			out.Secret[k] = true
			leafSet[mv.val] = struct{}{}
			for _, l := range mv.leaves {
				leafSet[l] = struct{}{}
			}
		}
	}
	for k, v := range r.platform(sd) {
		out.Vars[k] = v
		delete(out.Secret, k)
	}
	for l := range leafSet {
		if l != "" {
			out.Leaves = append(out.Leaves, l)
		}
	}
	sort.Strings(out.Leaves)
	out.MeshUses = st.meshUses()
	if lenient {
		for _, u := range out.MeshUses {
			if !u.Pending {
				continue
			}
			key := ""
			if len(u.Keys) > 0 {
				key = u.Keys[0]
			}
			probs = append(probs, Problem{
				Key: key, Code: CodeMeshRoutePending,
				Message: fmt.Sprintf("%s runs on another server and has no mesh route yet: until one exists %s reaches it at its container port, which is not reachable. Deploying %s creates the route.", u.ProviderSlug, slug, slug),
				Details: map[string]any{"service": u.ProviderSlug, "warning": true},
			})
		}
	}
	return out, probs, nil
}

func unresolved(msg string, details map[string]any) error {
	e := apierr.Unprocessable("unresolved_reference", msg)
	if details != nil {
		e = e.WithDetails(details)
	}
	return e
}

// lookup resolves the user variable, platform variable or export `key` of the
// service `slug`.
func (st *resolveState) lookup(slug, key string) (memoVal, error) {
	sd := st.r.services[slug]
	if sd == nil {
		return memoVal{}, unresolved(fmt.Sprintf("reference ${{%s.%s}}: no service %q in this environment", slug, key, slug),
			map[string]any{"service": slug, "key": key})
	}
	if IsPlatformKey(key) {
		v, ok := st.r.platform(sd)[key]
		if !ok {
			msg := fmt.Sprintf("reference ${{%s.%s}}: service %q has no value for %s", slug, key, slug, key)
			if isNonReferenceable(key) {
				msg = fmt.Sprintf("reference ${{%s.%s}}: %s is set per deployment or container and cannot be referenced", slug, key, key)
			}
			return memoVal{}, unresolved(msg, map[string]any{"service": slug, "key": key})
		}
		if key == VarPort || key == VarPrivateHost {
			st.noteUse(sd)
		}
		if key == VarPort {
			v = strconv.Itoa(EffectivePort(st.consumerServer, *sd))
		}
		return memoVal{val: v}, nil
	}
	def, ok := sd.Vars[key]
	if !ok {
		if ex, ok := st.export(sd, key); ok {
			return ex, nil
		}
		return memoVal{}, unresolved(fmt.Sprintf("reference ${{%s.%s}}: service %q has no variable %q", slug, key, slug, key),
			map[string]any{"service": slug, "key": key})
	}
	return st.eval(slug+"."+key, def.Value, def.Secret, slug)
}

func isNonReferenceable(key string) bool {
	for _, p := range PlatformVars {
		if p.Key == key {
			return !p.Referenceable
		}
	}
	return false
}

func (st *resolveState) lookupShared(key string) (memoVal, error) {
	def, ok := st.r.rc.Shared[key]
	if !ok {
		return memoVal{}, unresolved(fmt.Sprintf("reference ${{shared.%s}}: no shared variable %q in this environment or project", key, key),
			map[string]any{"service": varexpr.NSShared, "key": key})
	}
	return st.eval(varexpr.NSShared+"."+key, def.Value, def.Secret, "")
}

// evalExt resolves an external secret manager reference: fetched when the
// resolver has a fetcher, otherwise deferred (the value is only known at
// deploy time and is never stored).
func (st *resolveState) evalExt(ref varexpr.ExtRef) (memoVal, error) {
	details := map[string]any{"service": varexpr.ExtScheme, "key": ref.String(), "integration": ref.Integration}
	if in := st.r.rc.Integrations; in != nil {
		allowed, exists := in[ref.Integration]
		if !exists {
			return memoVal{}, unresolved(fmt.Sprintf("reference ${{%s}}: no secret manager integration named %q (add it under Team settings → Integrations → Secret managers)", ref, ref.Integration), details)
		}
		if !allowed {
			return memoVal{}, unresolved(fmt.Sprintf("reference ${{%s}}: the secret manager integration %q is not available to this project and environment (an admin can allow it under Team settings → Integrations → Secret managers)", ref, ref.Integration), details)
		}
	}
	if kinds := st.r.rc.IntegrationKinds; kinds != nil {
		if kind, ok := kinds[ref.Integration]; ok && !ExtKindMatches(ref, kind) {
			return memoVal{}, unresolved(fmt.Sprintf("reference ${{%s}}: the integration %q is a %s integration, which does not take %s: references", ref, ref.Integration, kind, ref.SchemeName()), details)
		}
	}
	if f := st.r.rc.Fetch; f != nil {
		v, err := f(ref)
		if err != nil {
			return memoVal{}, err
		}
		return memoVal{val: v, secret: true}, nil
	}
	tpl := ExtTpl{{Ref: &ref}}
	return memoVal{val: tpl.Display(), secret: true, ext: tpl}, nil
}

func (st *resolveState) lookupTeam(key string) (memoVal, error) {
	def, ok := st.r.rc.Team[key]
	if !ok {
		return memoVal{}, unresolved(fmt.Sprintf("reference ${{team.%s}}: no team variable %q", key, key),
			map[string]any{"service": varexpr.NSTeam, "key": key})
	}
	return st.eval(varexpr.NSTeam+"."+key, def.Value, def.Secret, "")
}

// eval expands one template. ctxSlug is the service whose bare references
// resolve ("" for shared values, which cannot use bare references).
func (st *resolveState) eval(id, value string, declSecret bool, ctxSlug string) (memoVal, error) {
	if mv, ok := st.memo[id]; ok {
		return mv, nil
	}
	if st.visit[id] || len(st.visitor) > maxRefDepthGuard {
		chain := append(append([]string{}, st.visitor...), id)
		return memoVal{}, apierr.Unprocessable("reference_cycle",
			"variable references form a cycle: "+strings.Join(chain, " → ")).
			WithDetails(map[string]any{"chain": chain})
	}
	st.visit[id] = true
	st.visitor = append(st.visitor, id)
	defer func() {
		delete(st.visit, id)
		st.visitor = st.visitor[:len(st.visitor)-1]
	}()

	tpl, err := varexpr.Parse(value)
	if err != nil {
		return memoVal{}, apierr.Unprocessable("invalid_template", fmt.Sprintf("variable %s: %s", id, err.Error()))
	}
	mv := memoVal{secret: declSecret}
	var b strings.Builder
	var nodes ExtTpl
	deferred := false
	for _, p := range tpl.Parts {
		if p.Expr == nil {
			b.WriteString(p.Text)
			nodes = append(nodes, ExtNode{Lit: p.Text})
			continue
		}
		v, err := st.evalExpr(p.Expr, ctxSlug)
		if err != nil {
			return memoVal{}, err
		}
		if v.secret {
			mv.secret = true
			mv.leaves = append(mv.leaves, v.val)
			mv.leaves = append(mv.leaves, v.leaves...)
		}
		b.WriteString(v.val)
		if v.ext != nil {
			deferred = true
			nodes = append(nodes, v.ext...)
		} else {
			nodes = append(nodes, ExtNode{Lit: v.val})
		}
	}
	mv.val = b.String()
	if deferred {
		mv.ext = nodes.compact()
		mv.val = mv.ext.Display()
	}
	st.memo[id] = mv
	return mv, nil
}

func (st *resolveState) evalExpr(e *varexpr.Expr, ctxSlug string) (memoVal, error) {
	if e.Ext != nil {
		return st.evalExt(*e.Ext)
	}
	if e.Ref != nil {
		return st.evalRef(*e.Ref, ctxSlug)
	}
	return st.evalCall(e.Call, ctxSlug)
}

func (st *resolveState) evalRef(ref varexpr.Ref, ctxSlug string) (memoVal, error) {
	switch ref.NS {
	case "":
		if ctxSlug == "" {
			return memoVal{}, unresolved(fmt.Sprintf("reference ${{%s}}: shared variables can only reference other shared variables (shared.KEY)", ref.Key),
				map[string]any{"key": ref.Key})
		}
		return st.lookup(ctxSlug, ref.Key)
	case varexpr.NSShared:
		return st.lookupShared(ref.Key)
	case varexpr.NSTeam:
		return st.lookupTeam(ref.Key)
	}
	if ctxSlug == "" {
		return memoVal{}, unresolved(fmt.Sprintf("reference ${{%s}}: shared variables cannot reference services", ref.String()),
			map[string]any{"service": ref.NS, "key": ref.Key})
	}
	return st.lookup(ref.NS, ref.Key)
}

func (st *resolveState) evalCall(c *varexpr.Call, ctxSlug string) (memoVal, error) {
	if varexpr.IsGenerator(c.Fn) {
		return memoVal{}, apierr.Unprocessable("ungenerated_function",
			fmt.Sprintf("%s() is a generator: it runs once when the variable is written and cannot appear in a stored value", c.Fn))
	}
	if !varexpr.IsTransform(c.Fn) || len(c.Args) != 1 {
		return memoVal{}, apierr.Unprocessable("invalid_template", "unknown function "+c.Fn)
	}
	a := c.Args[0]
	var in memoVal
	var err error
	switch a.Kind {
	case varexpr.ArgString:
		in = memoVal{val: a.Str}
	case varexpr.ArgRef:
		in, err = st.evalRef(a.Ref, ctxSlug)
	case varexpr.ArgIdent:
		in, err = st.evalRef(varexpr.Ref{Key: a.Str}, ctxSlug)
	case varexpr.ArgCall:
		in, err = st.evalCall(a.Call, ctxSlug)
	case varexpr.ArgExt:
		in, err = st.evalExt(a.Ext)
	default:
		return memoVal{}, apierr.Unprocessable("invalid_template", c.Fn+"() takes a reference or a string")
	}
	if err != nil {
		return memoVal{}, err
	}
	out := in
	if in.ext != nil {
		out.ext = ExtTpl{{Fn: c.Fn, In: in.ext}}
		out.val = out.ext.Display()
		return out, nil
	}
	out.val = applyTransform(c.Fn, in.val)
	if in.secret {
		out.leaves = append(append([]string{}, in.leaves...), in.val)
	}
	return out, nil
}

// References returns every (service slug, key) reference to another service in
// value. Shared, team and same-service references are not included.
func References(value string) [][2]string {
	tpl, err := varexpr.Parse(value)
	if err != nil {
		return nil
	}
	var out [][2]string
	for _, r := range tpl.Refs() {
		if r.NS == "" || r.NS == varexpr.NSShared || r.NS == varexpr.NSTeam {
			continue
		}
		out = append(out, [2]string{r.NS, r.Key})
	}
	return out
}

// SharedReferences returns the shared keys referenced by value.
func SharedReferences(value string) []string {
	tpl, err := varexpr.Parse(value)
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range tpl.Refs() {
		if r.NS == varexpr.NSShared {
			out = append(out, r.Key)
		}
	}
	return out
}

// TeamReferences returns the team variable keys referenced by value.
func TeamReferences(value string) []string {
	tpl, err := varexpr.Parse(value)
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range tpl.Refs() {
		if r.NS == varexpr.NSTeam {
			out = append(out, r.Key)
		}
	}
	return out
}

// EnvInfo identifies an environment for LoadResolver.
type EnvInfo struct {
	WorkspaceID uuid.UUID
	ID          uuid.UUID
	ProjectID   uuid.UUID
	ProjectName string
	Slug        string
	Name        string
}

// EnvInfo returns the environment context of the scope.
func (s Scope) EnvInfo() EnvInfo {
	return EnvInfo{WorkspaceID: s.WorkspaceID, ID: s.EnvironmentID, ProjectID: s.ProjectID, ProjectName: s.ProjectName, Slug: s.EnvSlug, Name: s.EnvName}
}

// LoadEnvInfo returns the environment context for LoadResolver.
func LoadEnvInfo(ctx context.Context, q *db.Queries, envID uuid.UUID) (EnvInfo, error) {
	r, err := q.GetEnvInfo(ctx, envID)
	if err != nil {
		return EnvInfo{}, fmt.Errorf("get environment: %w", err)
	}
	return EnvInfo{WorkspaceID: r.WorkspaceID, ID: r.ID, ProjectID: r.ProjectID, ProjectName: r.ProjectName, Slug: r.Slug, Name: r.Name}, nil
}

// LoadResolver reads every service, variable, shared variable and first domain
// of an environment and returns a resolver over them.
func LoadResolver(ctx context.Context, q *db.Queries, box *secretbox.Box, env EnvInfo) (*Resolver, error) {
	svcs, err := q.ListAllEnvironmentServices(ctx, env.ID)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	vars, err := q.ListEnvironmentVariables(ctx, env.ID)
	if err != nil {
		return nil, fmt.Errorf("list variables: %w", err)
	}
	doms, err := q.ListEnvironmentFirstDomains(ctx, env.ID)
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	shared, err := q.ListEffectiveSharedVariables(ctx, db.ListEffectiveSharedVariablesParams{ProjectID: env.ProjectID, EnvironmentID: &env.ID})
	if err != nil {
		return nil, fmt.Errorf("list shared variables: %w", err)
	}
	data := make([]ServiceData, 0, len(svcs))
	idx := map[uuid.UUID]int{}
	for _, s := range svcs {
		idx[s.ID] = len(data)
		cfg := DecodeConfig(s.Config)
		data = append(data, ServiceData{ID: s.ID, Slug: s.Slug, Name: s.Name, Kind: s.Kind, Port: cfg.Port, Config: cfg, Vars: map[string]VarDef{}, ServerID: s.ServerID})
	}
	for _, v := range vars {
		i, ok := idx[v.ServiceID]
		if !ok {
			continue
		}
		val, err := box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(v.ServiceID.String(), v.Key), v.ValueEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt variable %s: %w", v.Key, err)
		}
		data[i].Vars[v.Key] = VarDef{Value: val, Secret: v.IsSecret, Scope: v.Scope}
	}
	for _, d := range doms {
		if i, ok := idx[d.ServiceID]; ok {
			data[i].FirstDomain = d.Hostname
		}
	}
	// Cross-server service links: the mesh port of a service's 'service'
	// exposure. An exposure that sits on another server than the service runs on
	// is stale (the service moved; the exposure is re-ensured by the next deploy
	// of a consumer or the provider) and counts as none.
	exps, err := q.ListMeshExposuresOfEnvironment(ctx, env.ID)
	if err != nil {
		return nil, fmt.Errorf("list mesh exposures: %w", err)
	}
	for _, e := range exps {
		if e.Purpose != "service" {
			continue
		}
		if i, ok := idx[e.ServiceID]; ok && data[i].ServerID != nil && *data[i].ServerID == e.ServerID {
			data[i].MeshPort = int(e.MeshPort)
		}
	}
	replicas, err := q.ListDatabaseReplicasOfEnvironment(ctx, env.ID)
	if err != nil {
		return nil, fmt.Errorf("list database replicas: %w", err)
	}
	for _, r := range replicas {
		if i, ok := idx[r.ServiceID]; ok {
			data[i].Replica = &ReplicaMember{PrimaryID: r.PrimaryServiceID, State: r.State}
		}
	}
	rc := ResolverContext{ProjectName: env.ProjectName, EnvSlug: env.Slug, EnvName: env.Name, Shared: map[string]SharedDef{}}
	for _, sv := range shared {
		val, err := box.OpenString(secretbox.SharedVariablesValue, sv.ID.String(), sv.ValueEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt shared variable %s: %w", sv.Key, err)
		}
		layer := "project"
		if sv.EnvironmentID != nil {
			layer = "environment"
		}
		rc.Shared[sv.Key] = SharedDef{Value: val, Secret: sv.IsSecret, Layer: layer}
	}
	team, err := q.ListTeamSharedVariables(ctx, env.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("list team variables: %w", err)
	}
	rc.Team = make(map[string]SharedDef, len(team))
	for _, tv := range team {
		val, err := box.OpenString(secretbox.SharedVariablesValue, tv.ID.String(), tv.ValueEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt team variable %s: %w", tv.Key, err)
		}
		rc.Team[tv.Key] = SharedDef{Value: val, Secret: tv.IsSecret, Layer: "team"}
	}
	access, err := q.ListSecretIntegrationAccess(ctx, db.ListSecretIntegrationAccessParams{
		WorkspaceID: env.WorkspaceID, ProjectID: env.ProjectID, EnvironmentID: env.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("list secret integrations: %w", err)
	}
	rc.Integrations = make(map[string]bool, len(access))
	rc.IntegrationKinds = make(map[string]string, len(access))
	for _, a := range access {
		rc.Integrations[a.Name] = a.Allowed
		rc.IntegrationKinds[a.Name] = a.Kind
	}
	return NewResolverCtx(rc, data), nil
}

// WithFetcher returns a resolver that fetches external secret manager
// references instead of deferring them. Use it only where the real values are
// needed (a database credential, a backup hook); the values stay in memory.
func (r *Resolver) WithFetcher(ctx context.Context, f ExtFetcher) *Resolver {
	rc := r.rc
	rc.Fetch = func(ref varexpr.ExtRef) (string, error) { return f.Fetch(ctx, ref) }
	return &Resolver{rc: rc, services: r.services, byID: r.byID}
}

// DiagnoseShared evaluates every shared variable of the environment and
// reports the ones that do not resolve (cycles among shared variables, shared
// values that reference services, missing keys).
func (r *Resolver) DiagnoseShared() []Problem {
	st := newState(r)
	keys := make([]string, 0, len(r.rc.Shared))
	for k := range r.rc.Shared {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Problem
	for _, k := range keys {
		if _, err := st.lookupShared(k); err != nil {
			if ae, ok := apierr.As(err); ok {
				out = append(out, Problem{Key: k, Code: ae.Code, Message: ae.Message, Details: ae.Details})
			}
		}
	}
	tkeys := make([]string, 0, len(r.rc.Team))
	for k := range r.rc.Team {
		tkeys = append(tkeys, k)
	}
	sort.Strings(tkeys)
	for _, k := range tkeys {
		if _, err := st.lookupTeam(k); err != nil {
			if ae, ok := apierr.As(err); ok {
				out = append(out, Problem{Key: "team." + k, Code: ae.Code, Message: ae.Message, Details: ae.Details})
			}
		}
	}
	return out
}

// WithVar returns a resolver with one user variable of a service replaced or
// added (the original is not modified). It powers draft validation.
func (r *Resolver) WithVar(slug, key string, def VarDef) *Resolver {
	cp := &Resolver{rc: r.rc, services: make(map[string]*ServiceData, len(r.services)), byID: make(map[uuid.UUID]*ServiceData, len(r.services))}
	for s, sd := range r.services {
		c := *sd
		if s == slug {
			c.Vars = make(map[string]VarDef, len(sd.Vars)+1)
			for k, v := range sd.Vars {
				c.Vars[k] = v
			}
			c.Vars[key] = def
		}
		cp.services[s] = &c
		cp.byID[c.ID] = &c
	}
	return cp
}

// ResolveDraft resolves one template in the context of a service, as if it were
// the variable key (references, transforms and secrecy are evaluated;
// generators must already be materialised).
func (r *Resolver) ResolveDraft(slug, key, value string, secret bool) (val string, isSecret bool, err error) {
	d := r.WithVar(slug, key, VarDef{Value: value, Secret: secret})
	mv, err := newConsumerState(d, d.services[slug]).lookup(slug, key)
	return mv.val, mv.secret, err
}

// SharedKeys lists the effective shared variable keys, sorted.
func (r *Resolver) SharedKeys() []string {
	keys := make([]string, 0, len(r.rc.Shared))
	for k := range r.rc.Shared {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SharedIsSecret reports whether a shared variable is secret, declared or by
// derivation.
func (r *Resolver) SharedIsSecret(key string) bool {
	mv, err := newState(r).lookupShared(key)
	return err == nil && mv.secret
}

// TeamKeys lists the team variable keys, sorted.
func (r *Resolver) TeamKeys() []string {
	keys := make([]string, 0, len(r.rc.Team))
	for k := range r.rc.Team {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TeamIsSecret reports whether a team variable is secret, declared or by
// derivation.
func (r *Resolver) TeamIsSecret(key string) bool {
	mv, err := newState(r).lookupTeam(key)
	return err == nil && mv.secret
}
