package services

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// ExtFetcher reads one secret from an external secret manager. Implementations
// fetch live, hold the value only in memory and name the reference in every
// error (never the value).
type ExtFetcher interface {
	Fetch(ctx context.Context, ref varexpr.ExtRef) (string, error)
}

// ExtFlusher is implemented by fetchers that keep an access log: Flush writes
// what the operation read (references and service, never values). The code that
// drives a fetcher to completion calls it once, through FlushFetcher.
type ExtFlusher interface {
	Flush(ctx context.Context)
}

// FlushFetcher records the fetcher's access log when it keeps one. A nil fetcher
// is fine.
func FlushFetcher(ctx context.Context, f ExtFetcher) {
	if fl, ok := f.(ExtFlusher); ok {
		fl.Flush(ctx)
	}
}

// Purposes of a fetcher: why the control plane reads external secrets. They
// appear in the access log.
const (
	ExtPurposeDeploy   = "deploy"   // a command's send-time secrets
	ExtPurposeTask     = "task"     // a one-off or scheduled task
	ExtPurposeMask     = "log-mask" // re-fetch to mask logs after a restart
	ExtPurposeDatabase = "database" // a database credential (reveal, rotation, export)
	ExtPurposeBackup   = "backup"   // a backup hook
)

// ExtScope says which service a fetcher reads for: the provider checks the
// integration's project and environment allow-list against it, and the access
// log records it.
type ExtScope struct {
	WorkspaceID   uuid.UUID
	ProjectID     uuid.UUID
	EnvironmentID uuid.UUID
	ServiceID     uuid.UUID
	// Service, Project and Environment are the slugs shown in the access log.
	Service     string
	Project     string
	Environment string
	Purpose     string
	// DeploymentID and StartedBy name the deployment a send-time fetch serves
	// and who started it (a user, a token, "System"); empty when not a deploy.
	DeploymentID string
	StartedBy    string
}

// ExtScope returns the fetcher scope of the service for the given purpose.
func (s Scope) ExtScope(purpose string) ExtScope {
	return ExtScope{
		WorkspaceID: s.WorkspaceID, ProjectID: s.ProjectID, EnvironmentID: s.EnvironmentID, ServiceID: s.Service.ID,
		Service: s.Service.Slug, Project: s.ProjectSlug, Environment: s.EnvSlug, Purpose: purpose,
	}
}

// ExtKindMatches reports whether an integration of the given kind serves ref:
// `doppler:` references need a Doppler integration, `vault:` references a
// Vault / OpenBao or Infisical one.
func ExtKindMatches(ref varexpr.ExtRef, kind string) bool {
	if ref.Doppler() {
		return kind == varexpr.DopplerScheme
	}
	return kind == varexpr.ExtScheme || kind == "infisical"
}

// ExternalSecrets creates the fetchers of a service. One fetcher serves one
// operation (a deploy, a backup run): its cache lives and dies with it.
type ExternalSecrets interface {
	Fetcher(scope ExtScope) ExtFetcher
}

// ExtSecrets is the per-app handle on external secret managers: the provider
// that creates fetchers, and the in-memory record of fetched values used for
// log masking. One app owns one ExtSecrets and passes it to every service that
// resolves references (services, deployments, tasks, databases, backups); two
// apps in one process never share state. A nil *ExtSecrets is valid and means
// "no secret managers": references fail with a clear error.
type ExtSecrets struct {
	provider ExternalSecrets
	vals     *extValues
}

// NewExtSecrets returns the handle over provider (nil: none configured).
func NewExtSecrets(provider ExternalSecrets) *ExtSecrets {
	return &ExtSecrets{provider: provider, vals: newExtValues()}
}

// Fetcher returns a fresh fetcher for the service, or nil when no provider is set.
func (e *ExtSecrets) Fetcher(scope ExtScope) ExtFetcher {
	if e == nil || e.provider == nil {
		return nil
	}
	return e.provider.Fetcher(scope)
}

// Remember registers freshly fetched external values of a service for log
// masking (memory only).
func (e *ExtSecrets) Remember(serviceID uuid.UUID, values []string) {
	if e == nil {
		return
	}
	e.vals.remember(serviceID, values)
}

// ExtNode is one piece of a deferred value: literal text, an external
// reference, or a transform of a nested template.
type ExtNode struct {
	Lit string          `json:"l,omitempty"`
	Ref *varexpr.ExtRef `json:"r,omitempty"`
	Fn  string          `json:"f,omitempty"`
	In  ExtTpl          `json:"i,omitempty"`
}

// ExtTpl is a value that cannot be computed before deploy time because it
// depends on external secrets: the concatenation of its nodes. It holds only
// references and the literal text around them, never an external value.
type ExtTpl []ExtNode

func (t ExtTpl) compact() ExtTpl {
	var out ExtTpl
	for _, n := range t {
		if n.Ref == nil && n.Fn == "" {
			if n.Lit == "" {
				continue
			}
			if l := len(out); l > 0 && out[l-1].Ref == nil && out[l-1].Fn == "" {
				out[l-1].Lit += n.Lit
				continue
			}
		}
		out = append(out, n)
	}
	return out
}

// Display renders the template with references written out, e.g.
// `postgres://app:${{vault:bao/db#pw}}@db`. It is what the stored environment
// shows for the variable; it never contains an external value.
func (t ExtTpl) Display() string {
	var b strings.Builder
	for _, n := range t {
		switch {
		case n.Ref != nil:
			b.WriteString("${{" + n.Ref.String() + "}}")
		case n.Fn != "":
			b.WriteString(n.Fn + "(" + n.In.Display() + ")")
		default:
			b.WriteString(n.Lit)
		}
	}
	return b.String()
}

// Refs lists every external reference of the template.
func (t ExtTpl) Refs() []varexpr.ExtRef {
	var out []varexpr.ExtRef
	for _, n := range t {
		switch {
		case n.Ref != nil:
			out = append(out, *n.Ref)
		case n.Fn != "":
			out = append(out, n.In.Refs()...)
		}
	}
	return out
}

func applyTransform(fn, v string) string {
	switch fn {
	case varexpr.FnURLEncode:
		return url.QueryEscape(v)
	case varexpr.FnBase64:
		return base64.StdEncoding.EncodeToString([]byte(v))
	}
	return v
}

// Eval computes the value, fetching every reference through f. leaves are the
// intermediate secret values (fetched and pre-transform) that a log redactor
// must mask besides the result.
func (t ExtTpl) Eval(ctx context.Context, f ExtFetcher) (val string, leaves []string, err error) {
	var b strings.Builder
	for _, n := range t {
		switch {
		case n.Ref != nil:
			if f == nil {
				return "", nil, fmt.Errorf("reference ${{%s}}: external secret managers are not available", n.Ref)
			}
			v, err := f.Fetch(ctx, *n.Ref)
			if err != nil {
				return "", nil, err
			}
			b.WriteString(v)
			leaves = append(leaves, v)
		case n.Fn != "":
			in, l, err := n.In.Eval(ctx, f)
			if err != nil {
				return "", nil, err
			}
			leaves = append(leaves, l...)
			leaves = append(leaves, in)
			b.WriteString(applyTransform(n.Fn, in))
		default:
			b.WriteString(n.Lit)
		}
	}
	return b.String(), leaves, nil
}

// Materialize returns the environment with every external value fetched
// through f (one fetch per distinct reference). The result lives in memory
// only. leaves are all fetched values, for log masking. Without external
// variables it returns Vars unchanged and never touches f.
func (e EnvSnapshot) Materialize(ctx context.Context, f ExtFetcher) (vars map[string]string, leaves []string, err error) {
	if len(e.Ext) == 0 {
		return e.Vars, nil, nil
	}
	defer FlushFetcher(ctx, f)
	vars = make(map[string]string, len(e.Vars))
	for k, v := range e.Vars {
		vars[k] = v
	}
	keys := make([]string, 0, len(e.Ext))
	for k := range e.Ext {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v, l, err := e.Ext[k].Eval(ctx, f)
		if err != nil {
			return nil, nil, fmt.Errorf("variable %s: %w", k, err)
		}
		vars[k] = v
		leaves = append(leaves, v)
		leaves = append(leaves, l...)
	}
	return vars, leaves, nil
}

// extValues remembers, in memory only, the external values fetched for a
// service so the log redactor can mask them. Nothing is written to disk or the
// database. The newest extKeep batches per service are kept: during a rolling
// deploy the old container still logs the previous values. A batch is dropped
// after extTTL so a fetched secret does not stay in the control plane's memory
// until the next restart: masking then falls back to the lazy re-fetch of the
// current values (SecretValues, at most once a minute per service).
const (
	extKeep = 3
	extTTL  = 30 * time.Minute
)

type extBatch struct {
	at     time.Time
	values []string
}

type extValues struct {
	mu      sync.Mutex
	gen     uint64
	batches map[uuid.UUID][]extBatch
	missed  map[uuid.UUID]time.Time
	now     func() time.Time
}

func newExtValues() *extValues {
	return &extValues{batches: map[uuid.UUID][]extBatch{}, missed: map[uuid.UUID]time.Time{}, now: time.Now}
}

// expireLocked drops batches older than extTTL, zeroing nothing (Go strings
// cannot be wiped) but releasing the references so the collector can free
// them, and bumps the generation so cached mask lists are rebuilt.
func (e *extValues) expireLocked() {
	cutoff := e.now().Add(-extTTL)
	for id, bs := range e.batches {
		keep := bs[:0]
		for _, b := range bs {
			if b.at.After(cutoff) {
				keep = append(keep, b)
			}
		}
		if len(keep) == len(bs) {
			continue
		}
		clear(bs[len(keep):]) // release the dropped batches' references
		e.gen++
		if len(keep) == 0 {
			delete(e.batches, id)
			continue
		}
		e.batches[id] = keep
	}
}

func (e *extValues) remember(serviceID uuid.UUID, values []string) {
	if len(values) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expireLocked()
	if len(e.batches) > 4096 {
		clear(e.batches)
	}
	b := append(e.batches[serviceID], extBatch{at: e.now(), values: append([]string(nil), values...)})
	if len(b) > extKeep {
		clear(b[:len(b)-extKeep])
		b = b[len(b)-extKeep:]
	}
	e.batches[serviceID] = b
	delete(e.missed, serviceID)
	e.gen++
}

func (e *extValues) values(serviceID uuid.UUID) ([]string, uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expireLocked()
	var out []string
	for _, b := range e.batches[serviceID] {
		out = append(out, b.values...)
	}
	return out, e.gen
}

func (e *extValues) generation() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expireLocked()
	return e.gen
}

// shouldRetry throttles the lazy re-fetch after a restart: one attempt per
// service per minute.
func (e *extValues) shouldRetry(serviceID uuid.UUID, now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if t, ok := e.missed[serviceID]; ok && now.Sub(t) < time.Minute {
		return false
	}
	if len(e.missed) > 4096 {
		clear(e.missed)
	}
	e.missed[serviceID] = now
	return true
}

// ResolveService expands the variables of a service with every external secret
// manager reference fetched (for operations that use the real values: database
// credentials, backup hooks). The values stay in memory and are registered for
// log masking. It fails when a reference cannot be fetched or no secret
// manager is available.
func (e *ExtSecrets) ResolveService(ctx context.Context, r *Resolver, scope ExtScope, slug string) (Resolved, error) {
	sd, ok := r.Service(slug)
	if !ok {
		return r.ResolveService(slug)
	}
	if f := e.Fetcher(scope); f != nil {
		defer FlushFetcher(ctx, f)
		r = r.WithFetcher(ctx, f)
	}
	out, err := r.ResolveService(slug)
	if err != nil {
		return Resolved{}, err
	}
	if len(out.Ext) > 0 {
		return Resolved{}, fmt.Errorf("variable %s depends on an external secret manager that is not available", firstKey(out.Ext))
	}
	if r.rc.Fetch != nil && len(out.Leaves) > 0 {
		e.Remember(sd.ID, out.Leaves)
	}
	return out, nil
}

func firstKey(m map[string]ExtTpl) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys[0]
}

func (e *ExtSecrets) generation() uint64 {
	if e == nil {
		return 0
	}
	return e.vals.generation()
}

func (e *ExtSecrets) values(serviceID uuid.UUID) ([]string, uint64) {
	if e == nil {
		return nil, 0
	}
	return e.vals.values(serviceID)
}

func (e *ExtSecrets) shouldRetry(serviceID uuid.UUID, now time.Time) bool {
	if e == nil {
		return false
	}
	return e.vals.shouldRetry(serviceID, now)
}
