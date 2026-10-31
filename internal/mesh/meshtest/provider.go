package meshtest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/mesh"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Errors a FakeProvider returns for the rules the real Provider enforces.
var (
	ErrUnknownService  = errors.New("meshtest: service not registered (call RegisterService)")
	ErrUnknownLink     = errors.New("meshtest: unknown link")
	ErrUnknownExposure = errors.New("meshtest: unknown exposure")
	ErrEnvMismatch     = errors.New("meshtest: link and exposure belong to different environments")
	ErrPurposeMismatch = errors.New("meshtest: link and exposure have different purposes")
	ErrServerMismatch  = errors.New("meshtest: link does not target the exposure's server")
)

// FakeGrant is one row of mesh_grants in the fake.
type FakeGrant struct {
	LinkID, ExposureID, RefID uuid.UUID
	Source                    string
}

// FakeProvider is an in-memory mesh.Provider for the unit tests of the replica
// and service-link domains. It enforces the real provider's rules that matter
// to callers (mesh port allocation policy, environment and purpose checks of
// Grant, ref-counted release) and records every call, so a test can assert what
// the domain asked for without a database, a tailnet or an agent. It ignores the
// transaction and the queries it is handed.
type FakeProvider struct {
	mu sync.Mutex

	services  map[uuid.UUID]fakeService
	exposures map[uuid.UUID]*mesh.Exposure
	links     map[uuid.UUID]*mesh.Link
	grants    []FakeGrant
	// meshServers are the servers mesh is enabled on; nil means every server.
	meshServers map[uuid.UUID]bool

	// Plan is returned by PlanLink (default: adds one device when the link does
	// not exist yet, no blockers). PlanFor overrides it per call.
	Plan    mesh.LinkPlan
	PlanFor func(env, consumerSrv, providerSrv uuid.UUID, purpose string) mesh.LinkPlan
	// ReadyFor and CheckFor override the default answers (ready; connected).
	ReadyFor func(linkID uuid.UUID) mesh.Readiness
	CheckFor func(linkID, exposureID uuid.UUID) mesh.CheckResult
	// Err, when set, is returned by every mutating method (a coordinator that is
	// down, a paused mesh).
	Err error

	calls []string
}

type fakeService struct {
	env, server uuid.UUID
	slug        string
}

// NewFakeProvider returns an empty provider with mesh enabled on every server.
func NewFakeProvider() *FakeProvider {
	return &FakeProvider{
		services:  map[uuid.UUID]fakeService{},
		exposures: map[uuid.UUID]*mesh.Exposure{},
		links:     map[uuid.UUID]*mesh.Link{},
	}
}

var _ mesh.Provider = (*FakeProvider)(nil)

// RegisterService tells the fake where a service lives (EnsureExposure only
// receives the service id; the real provider reads this from the database).
func (f *FakeProvider) RegisterService(id, env, server uuid.UUID, slug string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.services[id] = fakeService{env: env, server: server, slug: slug}
}

// MoveService changes the server of a registered service.
func (f *FakeProvider) MoveService(id, server uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.services[id]
	s.server = server
	f.services[id] = s
}

// EnableMeshOn restricts mesh to the given servers (default: all).
func (f *FakeProvider) EnableMeshOn(servers ...uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.meshServers = map[uuid.UUID]bool{}
	for _, s := range servers {
		f.meshServers[s] = true
	}
}

func (f *FakeProvider) enabled(s uuid.UUID) bool { return f.meshServers == nil || f.meshServers[s] }

func (f *FakeProvider) record(format string, a ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, a...))
}

// Calls returns the recorded calls in order ("EnsureExposure <service> replication 5432", ...).
func (f *FakeProvider) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// Count returns how many recorded calls start with prefix.
func (f *FakeProvider) Count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if len(c) >= len(prefix) && c[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

// Grants returns a copy of the grant table.
func (f *FakeProvider) Grants() []FakeGrant {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeGrant(nil), f.grants...)
}

// Links returns the links, ordered by id.
func (f *FakeProvider) Links() []mesh.Link {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]mesh.Link, 0, len(f.links))
	for _, l := range f.links {
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID.String() < out[j].ID.String() })
	return out
}

// Exposures returns the exposures, ordered by id.
func (f *FakeProvider) Exposures() []mesh.Exposure {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]mesh.Exposure, 0, len(f.exposures))
	for _, e := range f.exposures {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID.String() < out[j].ID.String() })
	return out
}

// PlanLink implements mesh.Provider.
func (f *FakeProvider) PlanLink(_ context.Context, _ *db.Queries, env, consumerSrv, providerSrv uuid.UUID, purpose string) (mesh.LinkPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("PlanLink %s %s->%s %s", env, consumerSrv, providerSrv, purpose)
	if f.PlanFor != nil {
		return f.PlanFor(env, consumerSrv, providerSrv, purpose), nil
	}
	if len(f.Plan.Blockers) > 0 || f.Plan.DevicesAdded > 0 || f.Plan.LinkExists || len(f.Plan.Warnings) > 0 {
		return f.Plan, nil
	}
	plan := mesh.LinkPlan{Blockers: []mesh.Blocker{}, Warnings: []string{}}
	for _, s := range []uuid.UUID{consumerSrv, providerSrv} {
		if !f.enabled(s) {
			plan.Blockers = append(plan.Blockers, mesh.Blocker{Code: "mesh_server_required", Message: "Enable mesh on server " + s.String()})
		}
	}
	for _, l := range f.links {
		if l.EnvironmentID == env && l.ServerID == consumerSrv && l.RemoteServerID == providerSrv && l.Purpose == purpose {
			plan.LinkExists = true
		}
	}
	if !plan.LinkExists {
		plan.DevicesAdded = 1
	}
	return plan, nil
}

// EnsureExposure implements mesh.Provider with the real allocation policy.
func (f *FakeProvider) EnsureExposure(_ context.Context, _ pgx.Tx, serviceID uuid.UUID, purpose string, containerPort int) (mesh.Exposure, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("EnsureExposure %s %s %d", serviceID, purpose, containerPort)
	if f.Err != nil {
		return mesh.Exposure{}, f.Err
	}
	svc, ok := f.services[serviceID]
	if !ok {
		return mesh.Exposure{}, ErrUnknownService
	}
	for _, e := range f.exposures {
		if e.ServiceID == serviceID && e.Purpose == purpose {
			return *e, nil
		}
	}
	used := map[int]bool{}
	for _, e := range f.exposures {
		if e.ServerID == svc.server {
			used[e.MeshPort] = true
		}
	}
	port := 0
	if purpose == agentprotocol.MeshPurposeService && !used[containerPort] {
		port = containerPort
	}
	for p := agentprotocol.MeshPortMin; port == 0 && p <= agentprotocol.MeshPortMax; p++ {
		if !used[p] {
			port = p
		}
	}
	if port == 0 {
		return mesh.Exposure{}, errors.New("meshtest: no free mesh port")
	}
	e := &mesh.Exposure{
		ID: uuid.New(), ServerID: svc.server, ServiceID: serviceID, EnvironmentID: svc.env,
		Purpose: purpose, ContainerPort: containerPort, MeshPort: port,
	}
	f.exposures[e.ID] = e
	return *e, nil
}

// EnsureLink implements mesh.Provider.
func (f *FakeProvider) EnsureLink(_ context.Context, _ pgx.Tx, env, localSrv, remoteSrv uuid.UUID, purpose string) (mesh.Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("EnsureLink %s %s->%s %s", env, localSrv, remoteSrv, purpose)
	if f.Err != nil {
		return mesh.Link{}, f.Err
	}
	if localSrv == remoteSrv {
		return mesh.Link{}, errors.New("meshtest: a link needs two servers")
	}
	for _, l := range f.links {
		if l.EnvironmentID == env && l.ServerID == localSrv && l.RemoteServerID == remoteSrv && l.Purpose == purpose {
			return *l, nil
		}
	}
	l := &mesh.Link{ID: uuid.New(), EnvironmentID: env, ServerID: localSrv, RemoteServerID: remoteSrv, Purpose: purpose, State: agentprotocol.MeshStateProvisioning}
	f.links[l.ID] = l
	return *l, nil
}

// Grant implements mesh.Provider, including the environment and purpose checks.
func (f *FakeProvider) Grant(_ context.Context, _ pgx.Tx, linkID, exposureID, refID uuid.UUID, source string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Grant %s %s %s %s", linkID, exposureID, refID, source)
	if f.Err != nil {
		return f.Err
	}
	l, ok := f.links[linkID]
	if !ok {
		return ErrUnknownLink
	}
	e, ok := f.exposures[exposureID]
	if !ok {
		return ErrUnknownExposure
	}
	switch {
	case l.EnvironmentID != e.EnvironmentID:
		return ErrEnvMismatch
	case l.Purpose != e.Purpose:
		return ErrPurposeMismatch
	case l.RemoteServerID != e.ServerID:
		return ErrServerMismatch
	}
	g := FakeGrant{LinkID: linkID, ExposureID: exposureID, RefID: refID, Source: source}
	for _, x := range f.grants {
		if x == g {
			return nil
		}
	}
	f.grants = append(f.grants, g)
	return nil
}

// Revoke implements mesh.Provider.
func (f *FakeProvider) Revoke(_ context.Context, _ pgx.Tx, linkID, exposureID, refID uuid.UUID, source string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Revoke %s %s %s %s", linkID, exposureID, refID, source)
	if f.Err != nil {
		return f.Err
	}
	kept := f.grants[:0:0]
	touched := []FakeGrant{}
	for _, g := range f.grants {
		if g == (FakeGrant{LinkID: linkID, ExposureID: exposureID, RefID: refID, Source: source}) {
			touched = append(touched, g)
			continue
		}
		kept = append(kept, g)
	}
	f.grants = kept
	f.collect(touched)
	return nil
}

// Release implements mesh.Provider: drops every grant of ref, then cascades.
func (f *FakeProvider) Release(_ context.Context, _ pgx.Tx, refID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Release %s", refID)
	if f.Err != nil {
		return f.Err
	}
	kept := f.grants[:0:0]
	touched := []FakeGrant{}
	for _, g := range f.grants {
		if g.RefID == refID {
			touched = append(touched, g)
			continue
		}
		kept = append(kept, g)
	}
	f.grants = kept
	f.collect(touched)
	return nil
}

// collect removes the links and exposures of removed grants that no remaining
// grant needs (the ref-counted cascade of Revoke and Release).
func (f *FakeProvider) collect(removed []FakeGrant) {
	usedLinks, usedExp := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	for _, g := range f.grants {
		usedLinks[g.LinkID], usedExp[g.ExposureID] = true, true
	}
	for _, g := range removed {
		if !usedLinks[g.LinkID] {
			delete(f.links, g.LinkID)
		}
		if !usedExp[g.ExposureID] {
			delete(f.exposures, g.ExposureID)
		}
	}
}

// Ready implements mesh.Provider (default: online, ACL applied).
func (f *FakeProvider) Ready(_ context.Context, linkID uuid.UUID) (mesh.Readiness, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Ready %s", linkID)
	if f.ReadyFor != nil {
		return f.ReadyFor(linkID), nil
	}
	if _, ok := f.links[linkID]; !ok {
		return mesh.Readiness{Reason: "unknown link"}, nil
	}
	return mesh.Readiness{Online: true, ACLOK: true}, nil
}

// Check implements mesh.Provider (default: connected).
func (f *FakeProvider) Check(_ context.Context, linkID, exposureID uuid.UUID) (mesh.CheckResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Check %s %s", linkID, exposureID)
	if f.CheckFor != nil {
		return f.CheckFor(linkID, exposureID), nil
	}
	return mesh.CheckResult{Result: agentprotocol.MeshCheckConnected}, nil
}

// RouteOf implements mesh.Provider: the route exists when the provider has a
// service exposure and runs on another server than the consumer, and a granted
// link of the consumer's server reaches it.
func (f *FakeProvider) RouteOf(_ context.Context, _ *db.Queries, consumerSrv, providerServiceID uuid.UUID) (mesh.Route, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("RouteOf %s %s", consumerSrv, providerServiceID)
	svc, ok := f.services[providerServiceID]
	if !ok || svc.server == consumerSrv {
		return mesh.Route{}, false, nil
	}
	for _, e := range f.exposures {
		if e.ServiceID != providerServiceID || e.Purpose != agentprotocol.MeshPurposeService {
			continue
		}
		r := mesh.Route{Host: svc.slug, Port: e.MeshPort, State: agentprotocol.MeshStateProvisioning}
		for _, g := range f.grants {
			if g.ExposureID == e.ID && f.links[g.LinkID] != nil && f.links[g.LinkID].ServerID == consumerSrv {
				r.LinkID, r.State = g.LinkID, f.links[g.LinkID].State
				break
			}
		}
		return r, true, nil
	}
	return mesh.Route{}, false, nil
}

// SetLinkState sets the state a link reports (tests drive provisioning to online).
func (f *FakeProvider) SetLinkState(linkID uuid.UUID, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l := f.links[linkID]; l != nil {
		l.State = state
	}
}
