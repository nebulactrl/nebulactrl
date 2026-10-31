package meshlinks

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Mode says what a reconciliation may do.
type Mode string

const (
	// ModeAdd creates the links, exposures and grants the selected wants need.
	// It only runs from explicit actions: the deploy of a consumer, a canvas edge
	// the user confirmed, a provider that moved server, Promote, replica add, or
	// the "reconcile now" endpoint with acceptMeshChanges.
	ModeAdd Mode = "add"
	// ModeRepair only removes grants whose source is gone (after ReferenceGrace;
	// an edge's grant at once) and re-creates rows lost by a crash for pairs
	// whose link already exists. It never adds a link, so it never adds a device.
	ModeRepair Mode = "repair"
)

// Valid reports whether m is a known mode.
func (m Mode) Valid() bool { return m == ModeAdd || m == ModeRepair }

// Options select which wants an ModeAdd reconciliation acts on (ModeRepair
// ignores them: it looks at every want, but only of existing links).
type Options struct {
	// All adds every want of the environment ("reconcile now").
	All bool
	// Consumers adds the wants of these services as consumers (their deploy).
	Consumers map[uuid.UUID]bool
	// Providers re-adds, for these services (their deploy moved them to another
	// server), the wants of the consumers that held a grant on their old server.
	Providers map[uuid.UUID]bool
	// Edge adds only the want of the canvas edge from -> to.
	Edge *[2]uuid.UUID
}

// ForConsumers is Options for the deploy of the given services.
func ForConsumers(ids ...uuid.UUID) Options {
	set := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return Options{Consumers: set, Providers: set}
}

// GrantRef describes one grant added or removed.
type GrantRef struct {
	ConsumerID, ProviderID     uuid.UUID
	ConsumerSlug, ProviderSlug string
	Source                     string
	FromServerID, ToServerID   uuid.UUID
}

// Result is what a reconciliation did.
type Result struct {
	Mode    Mode
	Added   []GrantRef
	Removed []GrantRef
	// Waiting counts the orphaned grants kept for their grace period.
	Waiting int
	// DevicesAdded is the number of tailnet devices (links) the run added.
	DevicesAdded int
	// Blockers are the reasons selected wants could not be served.
	Blockers []services.MeshBlocker
	// Restaged are the consumers whose resolved addresses changed: their
	// generation was bumped so the staged bar shows the change.
	Restaged []uuid.UUID

	env services.EnvInfo
}

// Publish announces what a committed reconciliation changed.
func (s *Service) Publish(r Result) {
	if s.bus == nil || r.env.ID == uuid.Nil {
		return
	}
	if len(r.Added) > 0 || len(r.Removed) > 0 {
		services.Publish(s.bus, services.EventLinkUpdated, r.env.ProjectID, r.env.ID, r.env.ID)
	}
	for _, id := range r.Restaged {
		services.Publish(s.bus, services.EventServiceUpdated, r.env.ProjectID, r.env.ID, id)
	}
}

type grantKey struct {
	link, exposure, ref uuid.UUID
	source              string
}

func keyOf(g db.MeshGrant) grantKey { return grantKey{g.LinkID, g.ExposureID, g.RefID, g.Source} }

func (k grantKey) String() string {
	return strings.Join([]string{k.link.String(), k.exposure.String(), k.ref.String(), k.source}, "|")
}

// state is the mesh tables of one environment.
type state struct {
	exposures    []db.MeshExposure
	exposureByID map[uuid.UUID]db.MeshExposure
	links        map[uuid.UUID]db.MeshLink
	linkByKey    map[linkKey]db.MeshLink // purpose service only
	grants       map[grantKey]db.MeshGrant
	replicas     map[uuid.UUID]bool
}

func loadState(ctx context.Context, q *db.Queries, envID uuid.UUID) (*state, error) {
	st := &state{
		exposureByID: map[uuid.UUID]db.MeshExposure{}, links: map[uuid.UUID]db.MeshLink{},
		linkByKey: map[linkKey]db.MeshLink{}, grants: map[grantKey]db.MeshGrant{}, replicas: map[uuid.UUID]bool{},
	}
	var err error
	if st.exposures, err = q.ListMeshExposuresOfEnvironment(ctx, envID); err != nil {
		return nil, fmt.Errorf("list exposures: %w", err)
	}
	for _, e := range st.exposures {
		st.exposureByID[e.ID] = e
	}
	links, err := q.ListMeshLinksOfEnvironment(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	for _, l := range links {
		st.links[l.ID] = l
		if l.Purpose == purposeService {
			st.linkByKey[linkKey{l.ServerID, l.RemoteServerID}] = l
		}
	}
	grants, err := q.ListMeshGrantsOfEnvironment(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("list grants: %w", err)
	}
	for _, g := range grants {
		st.grants[keyOf(g)] = g
	}
	refs, err := q.ListMeshReplicaRefs(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("list replicas: %w", err)
	}
	for _, r := range refs {
		st.replicas[r.ServiceID] = true
	}
	return st, nil
}

func (st *state) grantsOfExposure(id uuid.UUID) []db.MeshGrant {
	var out []db.MeshGrant
	for _, g := range st.grants {
		if g.ExposureID == id {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return keyOf(out[i]).String() < keyOf(out[j]).String() })
	return out
}

// exposureOfService returns the service exposure of a provider.
func (st *state) exposureOfService(id uuid.UUID) (db.MeshExposure, bool) {
	for _, e := range st.exposures {
		if e.ServiceID == id && e.Purpose == purposeService {
			return e, true
		}
	}
	return db.MeshExposure{}, false
}

// lockEnvironment serializes reconciliations of one environment.
func lockEnvironment(ctx context.Context, tx pgx.Tx, envID uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('meshlinks:' || $1::text, 0))`, envID.String())
	return err
}

// Reconcile brings the grants of an environment in line with what its services
// need, inside the caller's transaction. See Mode and Options. Grants of a
// provider that moved server (or changed port) are always dropped first: the
// consumers are restaged, and a later ModeAdd run for them re-creates the route
// on the new server.
func (s *Service) Reconcile(ctx context.Context, tx pgx.Tx, envID uuid.UUID, mode Mode, opts Options) (Result, error) {
	if !mode.Valid() {
		return Result{}, apierr.Unprocessable("validation_failed", "mode must be add or repair")
	}
	q := db.New(tx)
	env, err := services.LoadEnvInfo(ctx, q, envID)
	if err != nil {
		return Result{}, err
	}
	res := Result{Mode: mode, Added: []GrantRef{}, Removed: []GrantRef{}, Blockers: []services.MeshBlocker{}, Restaged: []uuid.UUID{}, env: env}
	if err := lockEnvironment(ctx, tx, envID); err != nil {
		return Result{}, fmt.Errorf("lock environment: %w", err)
	}
	d, err := Derive(ctx, q, s.box, env)
	if err != nil {
		return Result{}, err
	}
	st, err := loadState(ctx, q, envID)
	if err != nil {
		return Result{}, err
	}
	if s.mesh == nil {
		if mode == ModeAdd {
			for _, w := range d.Wants {
				if opts.selects(w, nil) {
					res.Blockers = append(res.Blockers, services.MeshBlocker{Code: apierr.CodeMeshNotConfigured, Message: apierr.MeshNotConfigured().Message})
					break
				}
			}
		}
		return res, nil
	}

	restage := map[uuid.UUID]bool{}
	stale, err := s.dropStale(ctx, tx, d, st, &res, restage)
	if err != nil {
		return Result{}, err
	}
	if err := s.addWants(ctx, tx, q, d, st, mode, opts, stale, &res, restage); err != nil {
		return Result{}, err
	}
	if mode == ModeRepair {
		if err := s.removeOrphans(ctx, tx, d, st, &res); err != nil {
			return Result{}, err
		}
	}
	for id := range restage {
		if _, err := q.BumpServiceGeneration(ctx, id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return Result{}, fmt.Errorf("restage consumer: %w", err)
		}
		res.Restaged = append(res.Restaged, id)
	}
	sort.Slice(res.Restaged, func(i, j int) bool { return res.Restaged[i].String() < res.Restaged[j].String() })
	return res, nil
}

// selects reports whether an add run acts on w. stale holds, per provider, the
// consumers that had a grant on its previous exposure.
func (o Options) selects(w Want, stale map[uuid.UUID]map[uuid.UUID]bool) bool {
	switch {
	case o.All:
		return true
	case o.Edge != nil:
		return w.Source == SourceEdge && w.ConsumerID == o.Edge[0] && w.ProviderID == o.Edge[1]
	}
	return o.Consumers[w.ConsumerID] || (o.Providers[w.ProviderID] && stale[w.ProviderID][w.ConsumerID])
}

// dropStale revokes every grant on a service exposure that no longer matches
// its provider: the provider runs on another server, its port changed or it is
// gone. It returns, per provider, the consumers that held a grant.
func (s *Service) dropStale(ctx context.Context, tx pgx.Tx, d Derived, st *state, res *Result, restage map[uuid.UUID]bool) (map[uuid.UUID]map[uuid.UUID]bool, error) {
	stale := map[uuid.UUID]map[uuid.UUID]bool{}
	for _, ex := range st.exposures {
		if ex.Purpose != purposeService {
			continue
		}
		p, ok := d.Services[ex.ServiceID]
		if ok && p.ServerID != nil && *p.ServerID == ex.ServerID && (services.DecodeConfig(p.Config).Port == int(ex.ContainerPort)) {
			continue
		}
		for _, g := range st.grantsOfExposure(ex.ID) {
			if err := s.mesh.Revoke(ctx, tx, g.LinkID, g.ExposureID, g.RefID, g.Source); err != nil {
				return nil, fmt.Errorf("revoke grant of moved provider: %w", err)
			}
			delete(st.grants, keyOf(g))
			res.Removed = append(res.Removed, s.refOf(d, st, g))
			if g.Source == SourceReference || g.Source == SourceEdge {
				if stale[ex.ServiceID] == nil {
					stale[ex.ServiceID] = map[uuid.UUID]bool{}
				}
				stale[ex.ServiceID][g.RefID] = true
				restage[g.RefID] = true
			}
		}
		delete(st.exposureByID, ex.ID)
	}
	kept := st.exposures[:0:0]
	for _, e := range st.exposures {
		if _, ok := st.exposureByID[e.ID]; ok {
			kept = append(kept, e)
		}
	}
	st.exposures = kept
	return stale, nil
}

func (s *Service) refOf(d Derived, st *state, g db.MeshGrant) GrantRef {
	r := GrantRef{ConsumerID: g.RefID, Source: g.Source}
	if c, ok := d.Services[g.RefID]; ok {
		r.ConsumerSlug = c.Slug
	}
	if ex, ok := st.exposureByID[g.ExposureID]; ok {
		r.ProviderID = ex.ServiceID
		r.ToServerID = ex.ServerID
		if p, ok := d.Services[ex.ServiceID]; ok {
			r.ProviderSlug = p.Slug
		}
	}
	if l, ok := st.links[g.LinkID]; ok {
		r.FromServerID, r.ToServerID = l.ServerID, l.RemoteServerID
	}
	return r
}

// addWants creates what the selected wants need.
func (s *Service) addWants(ctx context.Context, tx pgx.Tx, q *db.Queries, d Derived, st *state, mode Mode, opts Options,
	stale map[uuid.UUID]map[uuid.UUID]bool, res *Result, restage map[uuid.UUID]bool) error {
	type pairResult struct {
		blocked bool
		devices int
	}
	pairs := map[linkKey]*pairResult{}
	newExposure := map[uuid.UUID]bool{}
	seenBlock := map[string]bool{}
	for _, w := range d.Wants {
		_, linkExists := st.linkByKey[w.linkKey()]
		if mode == ModeRepair {
			if !linkExists || w.NotExposable != "" {
				continue
			}
		} else if !opts.selects(w, stale) {
			continue
		}
		if w.NotExposable != "" {
			b := services.MeshBlocker{Code: apierr.CodeMeshNotExposable,
				Message: fmt.Sprintf("%s cannot be reached over the mesh: %s", w.ProviderSlug, w.NotExposable)}
			if !seenBlock[b.Message] {
				seenBlock[b.Message] = true
				res.Blockers = append(res.Blockers, b)
			}
			continue
		}
		pr := pairs[w.linkKey()]
		if pr == nil {
			pr = &pairResult{}
			pairs[w.linkKey()] = pr
			plan, err := s.mesh.PlanLink(ctx, q, d.Env.ID, w.ConsumerServer, w.ProviderServer, purposeService)
			if err != nil {
				return err
			}
			pr.devices = plan.DevicesAdded
			if len(plan.Blockers) > 0 {
				pr.blocked = true
				if mode == ModeAdd {
					for _, b := range plan.Blockers {
						if !seenBlock[b.Code+b.Message] {
							seenBlock[b.Code+b.Message] = true
							res.Blockers = append(res.Blockers, services.MeshBlocker{Code: b.Code, Message: b.Message})
						}
					}
				}
			}
		}
		if pr.blocked {
			continue
		}
		_, hadExposure := st.exposureOfService(w.ProviderID)
		ex, err := s.mesh.EnsureExposure(ctx, tx, w.ProviderID, purposeService, w.ProviderPort)
		if err != nil {
			return err
		}
		link, err := s.mesh.EnsureLink(ctx, tx, d.Env.ID, w.ConsumerServer, w.ProviderServer, purposeService)
		if err != nil {
			return err
		}
		if !linkExists && pr.devices > 0 {
			res.DevicesAdded += pr.devices
			pr.devices = 0
		}
		if !hadExposure {
			newExposure[w.ProviderID] = true
			st.exposures = append(st.exposures, db.MeshExposure{
				ID: ex.ID, ServerID: ex.ServerID, ServiceID: ex.ServiceID, EnvironmentID: ex.EnvironmentID,
				Purpose: ex.Purpose, ContainerPort: int32(ex.ContainerPort), MeshPort: int32(ex.MeshPort),
			})
			st.exposureByID[ex.ID] = st.exposures[len(st.exposures)-1]
		}
		k := grantKey{link.ID, ex.ID, w.ConsumerID, w.Source}
		if _, ok := st.grants[k]; ok {
			continue
		}
		if err := s.mesh.Grant(ctx, tx, link.ID, ex.ID, w.ConsumerID, w.Source); err != nil {
			return err
		}
		st.grants[k] = db.MeshGrant{LinkID: k.link, ExposureID: k.exposure, RefID: k.ref, Source: k.source}
		res.Added = append(res.Added, GrantRef{
			ConsumerID: w.ConsumerID, ProviderID: w.ProviderID, ConsumerSlug: w.ConsumerSlug, ProviderSlug: w.ProviderSlug,
			Source: w.Source, FromServerID: w.ConsumerServer, ToServerID: w.ProviderServer,
		})
	}
	// A provider that got its route now resolves differently for every consumer
	// that references it: they show the change as staged, they are not deployed.
	for _, w := range d.Wants {
		if newExposure[w.ProviderID] && !opts.Consumers[w.ConsumerID] {
			restage[w.ConsumerID] = true
		}
	}
	return nil
}

// removeOrphans drops the grants no source asks for any more.
func (s *Service) removeOrphans(ctx context.Context, tx pgx.Tx, d Derived, st *state, res *Result) error {
	type desiredKey struct {
		from, to           uuid.UUID
		consumer, provider uuid.UUID
		source             string
	}
	desired := map[desiredKey]bool{}
	for _, w := range d.Wants {
		if w.NotExposable == "" {
			desired[desiredKey{w.ConsumerServer, w.ProviderServer, w.ConsumerID, w.ProviderID, w.Source}] = true
		}
	}
	now := s.now()
	prefix := d.Env.ID.String() + "/"
	seen := map[string]bool{}
	keys := make([]grantKey, 0, len(st.grants))
	for k := range st.grants {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	for _, k := range keys {
		g := st.grants[k]
		link, lok := st.links[g.LinkID]
		ex, eok := st.exposureByID[g.ExposureID]
		immediate := false
		keep := false
		switch g.Source {
		case SourceReplica:
			keep = st.replicas[g.RefID]
		case SourceReference, SourceEdge:
			immediate = g.Source == SourceEdge
			switch {
			case !lok || !eok:
				keep = false
			case link.Purpose != purposeService:
				keep = true // not a service grant: another domain owns it
			default:
				keep = desired[desiredKey{link.ServerID, link.RemoteServerID, g.RefID, ex.ServiceID, g.Source}]
			}
		default:
			keep = true
		}
		if keep {
			continue
		}
		mark := prefix + k.String()
		seen[mark] = true
		if !immediate && s.orphanAge(mark, now) < s.grace {
			res.Waiting++
			continue
		}
		if err := s.mesh.Revoke(ctx, tx, g.LinkID, g.ExposureID, g.RefID, g.Source); err != nil {
			return fmt.Errorf("revoke orphaned grant: %w", err)
		}
		delete(st.grants, k)
		res.Removed = append(res.Removed, s.refOf(d, st, g))
	}
	s.mu.Lock()
	for mark := range s.orphans {
		if strings.HasPrefix(mark, prefix) && !seen[mark] {
			delete(s.orphans, mark)
		}
	}
	s.mu.Unlock()
	return nil
}

// orphanAge returns how long the grant has been without a source (and starts
// the clock the first time).
func (s *Service) orphanAge(mark string, now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	first, ok := s.orphans[mark]
	if !ok {
		s.orphans[mark] = now
		return 0
	}
	return now.Sub(first)
}

// ReconcileEnv is Reconcile in its own transaction; the result is published
// after the commit.
func (s *Service) ReconcileEnv(ctx context.Context, envID uuid.UUID, mode Mode, opts Options) (Result, error) {
	var res Result
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		res, err = s.Reconcile(ctx, tx, envID, mode, opts)
		return err
	})
	if err != nil {
		return Result{}, err
	}
	s.Publish(res)
	return res, nil
}
