package registries

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// RegistryCheck is the outcome of the last registry check from one server.
type RegistryCheck struct {
	Ok       bool       `json:"ok"`
	Push     bool       `json:"push"`
	Pull     bool       `json:"pull"`
	Error    string     `json:"error,omitempty"`
	At       time.Time  `json:"at"`
	ServerID *uuid.UUID `json:"serverId,omitempty"`
	// Pending is set on a check response when the agent had not answered yet.
	Pending bool `json:"pending,omitempty" doc:"The agent has not answered within the wait; read the registry again for the result."`
}

// RegistryReferences says what still points at a registry.
type RegistryReferences struct {
	Teams    int `json:"teams" doc:"Team build settings that use it as the default registry."`
	Projects int `json:"projects" doc:"Projects that use it."`
	Services int `json:"services" doc:"Services whose build override uses it."`
}

func (r RegistryReferences) total() int { return r.Teams + r.Projects + r.Services }

// RegistryBuiltinView is what the page shows about the built-in registry's instance service.
type RegistryBuiltinView struct {
	Status        string     `json:"status" enum:"ready,unavailable"`
	StatusMessage string     `json:"statusMessage"`
	GCState       string     `json:"gcState" enum:"idle,readonly,collecting" doc:"readonly: the weekly garbage-collection window has started and pushes are refused; builds wait."`
	LastGCAt      *time.Time `json:"lastGcAt,omitempty"`
	LastGCMessage string     `json:"lastGcMessage,omitempty"`
	LastSweepAt   *time.Time `json:"lastSweepAt,omitempty"`
	// LastSweepDeleted is how many expired images the last sweep deleted.
	LastSweepDeleted int `json:"lastSweepDeleted"`
}

// RegistryView is a registry as the API returns it. Passwords never appear.
type RegistryView struct {
	ID              uuid.UUID                `json:"id"`
	Name            string                   `json:"name"`
	Kind            string                   `json:"kind" enum:"external,builtin"`
	Host            string                   `json:"host" doc:"registry.example.com[:port], no scheme."`
	Namespace       string                   `json:"namespace"`
	HasPushAuth     bool                     `json:"hasPushAuth"`
	PushUsername    string                   `json:"pushUsername,omitempty"`
	HasPullAuth     bool                     `json:"hasPullAuth" doc:"A separate read-only credential is set; targets pull with it. Otherwise they use the push credential."`
	PullUsername    string                   `json:"pullUsername,omitempty"`
	ManageRetention bool                     `json:"manageRetention" doc:"NebulaCtrl deletes images that fall out of the rollback window (always for the built-in registry; opt-in for external)."`
	Insecure        bool                     `json:"insecure" doc:"A loopback host: plain http is used."`
	LastCheck       map[string]RegistryCheck `json:"lastCheck" doc:"Result of the last check, by server id."`
	ImageCount      int                      `json:"imageCount" doc:"Live images."`
	SizeBytes       int64                    `json:"sizeBytes" doc:"Space the live images take."`
	InUse           bool                     `json:"inUse" doc:"A team, project or service setting uses it: it cannot be removed."`
	References      RegistryReferences       `json:"references"`
	Builtin         *RegistryBuiltinView     `json:"builtin,omitempty" doc:"Set for the built-in registry: the state of the instance service."`
	CreatedAt       time.Time                `json:"createdAt"`
	UpdatedAt       time.Time                `json:"updatedAt"`
}

func (s *Service) view(ctx context.Context, r db.Registry) (RegistryView, error) {
	v := RegistryView{
		ID: r.ID, Name: r.Name, Kind: r.Kind, Host: r.Host, Namespace: r.Namespace,
		HasPushAuth: len(r.PushAuthEnc) > 0, HasPullAuth: len(r.PullAuthEnc) > 0, ManageRetention: r.ManageRetention,
		Insecure: IsLoopbackHost(r.Host), LastCheck: map[string]RegistryCheck{},
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if c, err := s.open(secretbox.RegistriesPushAuth, r.ID, r.PushAuthEnc); err == nil {
		v.PushUsername = c.Username
	}
	if c, err := s.open(secretbox.RegistriesPullAuth, r.ID, r.PullAuthEnc); err == nil {
		v.PullUsername = c.Username
	}
	if len(r.LastCheck) > 0 {
		_ = json.Unmarshal(r.LastCheck, &v.LastCheck)
		if v.LastCheck == nil {
			v.LastCheck = map[string]RegistryCheck{}
		}
	}
	stats, err := s.q.RegistryImageStats(ctx, r.ID)
	if err != nil {
		return v, fmt.Errorf("image stats: %w", err)
	}
	v.ImageCount, v.SizeBytes = int(stats.ImageCount), stats.SizeBytes
	refs, err := s.references(ctx, r.ID)
	if err != nil {
		return v, err
	}
	v.References, v.InUse = refs, refs.total() > 0
	if r.Kind == KindBuiltin {
		m, err := s.builtin(ctx)
		if err != nil {
			return v, err
		}
		mv := &RegistryBuiltinView{Status: "ready", StatusMessage: m.message, GCState: "idle"}
		if !m.ready {
			mv.Status = "unavailable"
		}
		switch {
		case m.state.Phase == "collect":
			mv.GCState = "collecting"
		case m.state.ReadOnly:
			mv.GCState = "readonly"
		}
		mv.LastGCAt, mv.LastGCMessage = m.state.LastGCAt, m.state.LastGCMessage
		mv.LastSweepAt, mv.LastSweepDeleted = m.state.LastSweepAt, m.state.LastSweepDeleted
		v.Builtin = mv
	}
	return v, nil
}

func (s *Service) references(ctx context.Context, id uuid.UUID) (RegistryReferences, error) {
	n, err := s.q.CountRegistryReferences(ctx, &id)
	if err != nil {
		return RegistryReferences{}, fmt.Errorf("count registry references: %w", err)
	}
	return RegistryReferences{Teams: int(n.Teams), Projects: int(n.Projects), Services: int(n.Services)}, nil
}

// List returns the team's registries.
func (s *Service) List(ctx context.Context) ([]RegistryView, error) {
	ws, err := s.workspace(ctx, policy.ScopeProjectsRead)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListRegistries(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list registries: %w", err)
	}
	out := make([]RegistryView, 0, len(rows))
	for _, r := range rows {
		v, err := s.view(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Get returns one registry.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (RegistryView, error) {
	ws, err := s.workspace(ctx, policy.ScopeProjectsRead)
	if err != nil {
		return RegistryView{}, err
	}
	r, err := s.load(ctx, ws, id)
	if err != nil {
		return RegistryView{}, err
	}
	return s.view(ctx, r)
}

// CreateInput creates an external registry.
type CreateInput struct {
	Name      string
	Host      string
	Namespace string
	// PushUsername and PushPassword are required (for a registry without
	// authentication, any pair).
	PushUsername string
	PushPassword string
	// PullUsername and PullPassword are an optional read-only credential.
	PullUsername string
	PullPassword string
}

// Create adds an external registry with sealed credentials.
func (s *Service) Create(ctx context.Context, in CreateInput) (RegistryView, error) {
	ws, err := s.workspace(ctx, policy.ScopeAdmin)
	if err != nil {
		return RegistryView{}, err
	}
	name, err := validateName(in.Name)
	if err != nil {
		return RegistryView{}, err
	}
	host, err := NormalizeHost(in.Host)
	if err != nil {
		return RegistryView{}, err
	}
	ns, err := NormalizeNamespace(in.Namespace)
	if err != nil {
		return RegistryView{}, err
	}
	if err := validateCredential("The push credential", in.PushUsername, in.PushPassword); err != nil {
		return RegistryView{}, err
	}
	regID := uuid.New()
	push, err := s.seal(secretbox.RegistriesPushAuth, regID, in.PushUsername, in.PushPassword)
	if err != nil {
		return RegistryView{}, err
	}
	var pull []byte
	if in.PullUsername != "" || in.PullPassword != "" {
		if err := validateCredential("The pull credential", in.PullUsername, in.PullPassword); err != nil {
			return RegistryView{}, err
		}
		if pull, err = s.seal(secretbox.RegistriesPullAuth, regID, in.PullUsername, in.PullPassword); err != nil {
			return RegistryView{}, err
		}
	}
	var row db.Registry
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		row, err = db.New(tx).InsertRegistry(ctx, db.InsertRegistryParams{
			ID: &regID, WorkspaceID: ws, Name: name, Kind: KindExternal, Host: host, Namespace: ns, PushAuthEnc: push, PullAuthEnc: pull,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: AuditCreate, ResourceType: "registry", ResourceID: row.ID.String(),
			Metadata: map[string]any{"name": name, "kind": KindExternal, "host": host, "namespace": ns, "pullCredential": pull != nil},
		})
	})
	if err != nil {
		if isUniqueViolation(err) {
			return RegistryView{}, apierr.Conflict("registry_name_taken", "A registry named "+name+" already exists.")
		}
		return RegistryView{}, fmt.Errorf("create registry: %w", err)
	}
	s.publish(ws, row.ID)
	return s.view(ctx, row)
}

// UpdateInput changes a registry; nil fields stay as they are. A password that
// is not sent keeps the stored one.
type UpdateInput struct {
	Name         *string
	Host         *string
	Namespace    *string
	PushUsername *string
	PushPassword *string
	PullUsername *string
	PullPassword *string
	// ClearPull removes the read-only credential (targets pull with the push one).
	ClearPull       bool
	ManageRetention *bool
}

// Update changes an external registry. The built-in one is read-only (409
// builtin_registry_readonly).
func (s *Service) Update(ctx context.Context, id uuid.UUID, in UpdateInput) (RegistryView, error) {
	ws, err := s.workspace(ctx, policy.ScopeAdmin)
	if err != nil {
		return RegistryView{}, err
	}
	cur, err := s.load(ctx, ws, id)
	if err != nil {
		return RegistryView{}, err
	}
	if cur.Kind == KindBuiltin {
		return RegistryView{}, apierr.Conflict("builtin_registry_readonly", "The built-in registry belongs to the instance. Its name, address, credentials and retention are set by NebulaCtrl.")
	}
	changed := []string{}
	name, host, ns, push, pull, retain := cur.Name, cur.Host, cur.Namespace, cur.PushAuthEnc, cur.PullAuthEnc, cur.ManageRetention
	if in.Name != nil {
		if name, err = validateName(*in.Name); err != nil {
			return RegistryView{}, err
		}
		changed = append(changed, "name")
	}
	if in.ManageRetention != nil {
		retain = *in.ManageRetention
		changed = append(changed, "manageRetention")
	}
	if in.Host != nil {
		if host, err = NormalizeHost(*in.Host); err != nil {
			return RegistryView{}, err
		}
		// The stored credentials would go to the new address: whoever moves the
		// registry must prove they know them, as with a secret manager's address.
		if host != cur.Host {
			if in.PushPassword == nil {
				return RegistryView{}, apierr.Unprocessable("secret_required", "Changing the address sends the credentials to another server: enter the push password again.")
			}
			if len(cur.PullAuthEnc) > 0 && !in.ClearPull && in.PullPassword == nil {
				return RegistryView{}, apierr.Unprocessable("secret_required", "Changing the address sends the credentials to another server: enter the read-only password again, or remove the read-only credential.")
			}
		}
		changed = append(changed, "host")
	}
	if in.Namespace != nil {
		if ns, err = NormalizeNamespace(*in.Namespace); err != nil {
			return RegistryView{}, err
		}
		changed = append(changed, "namespace")
	}
	if in.PushUsername != nil || in.PushPassword != nil {
		old, err := s.open(secretbox.RegistriesPushAuth, id, cur.PushAuthEnc)
		if err != nil {
			return RegistryView{}, err
		}
		u, p := old.Username, old.Password
		if in.PushUsername != nil {
			u = *in.PushUsername
		}
		if in.PushPassword != nil {
			p = *in.PushPassword
		} else if in.PushUsername != nil && u != old.Username {
			return RegistryView{}, apierr.Unprocessable("validation_failed", "Changing the push username needs the password too: passwords are never shown, so enter it again.")
		}
		if err := validateCredential("The push credential", u, p); err != nil {
			return RegistryView{}, err
		}
		if push, err = s.seal(secretbox.RegistriesPushAuth, id, u, p); err != nil {
			return RegistryView{}, err
		}
		changed = append(changed, "pushCredential")
	}
	switch {
	case in.ClearPull:
		if in.PullUsername != nil || in.PullPassword != nil {
			return RegistryView{}, apierr.Unprocessable("validation_failed", "clearPull cannot be combined with a new pull credential")
		}
		pull = nil
		changed = append(changed, "pullCredential")
	case in.PullUsername != nil || in.PullPassword != nil:
		old, err := s.open(secretbox.RegistriesPullAuth, id, cur.PullAuthEnc)
		if err != nil {
			return RegistryView{}, err
		}
		u, p := old.Username, old.Password
		if in.PullUsername != nil {
			u = *in.PullUsername
		}
		if in.PullPassword != nil {
			p = *in.PullPassword
		} else if len(cur.PullAuthEnc) == 0 || (in.PullUsername != nil && u != old.Username) {
			return RegistryView{}, apierr.Unprocessable("validation_failed", "The read-only credential needs a username and a password.")
		}
		if err := validateCredential("The pull credential", u, p); err != nil {
			return RegistryView{}, err
		}
		if pull, err = s.seal(secretbox.RegistriesPullAuth, id, u, p); err != nil {
			return RegistryView{}, err
		}
		changed = append(changed, "pullCredential")
	}
	var row db.Registry
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		row, err = db.New(tx).UpdateRegistry(ctx, db.UpdateRegistryParams{
			ID: id, WorkspaceID: ws, Name: name, Host: host, Namespace: ns, PushAuthEnc: push, PullAuthEnc: pull, ManageRetention: retain,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: AuditUpdate, ResourceType: "registry", ResourceID: id.String(),
			Metadata: map[string]any{"name": name, "changed": changed},
		})
	})
	if err != nil {
		if isUniqueViolation(err) {
			return RegistryView{}, apierr.Conflict("registry_name_taken", "A registry named "+name+" already exists.")
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return RegistryView{}, apierr.NotFound("registry")
		}
		return RegistryView{}, fmt.Errorf("update registry: %w", err)
	}
	s.publish(ws, id)
	return s.view(ctx, row)
}

// errInUse is the 409 of a registry that settings still point at.
func errInUse(name string, refs RegistryReferences) error {
	return apierr.Conflict("registry_in_use",
		fmt.Sprintf("The registry %s is still used (%d team default, %d projects, %d services). Choose another registry there first.", name, refs.Teams, refs.Projects, refs.Services)).
		WithDetails(map[string]any{"teams": refs.Teams, "projects": refs.Projects, "services": refs.Services})
}

// Delete removes an external registry. 409 registry_in_use while a team, project
// or service setting references it. The built-in registry belongs to the
// instance and is refused (409 builtin_registry).
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	ws, err := s.workspace(ctx, policy.ScopeAdmin)
	if err != nil {
		return err
	}
	r, err := s.load(ctx, ws, id)
	if err != nil {
		return err
	}
	if r.Kind == KindBuiltin {
		return apierr.New(http.StatusConflict, "builtin_registry", "The built-in registry belongs to the instance and cannot be removed.")
	}
	return s.remove(ctx, r)
}

// CheckRemovable returns 409 registry_in_use when settings still reference registry id.
func (s *Service) CheckRemovable(ctx context.Context, id uuid.UUID) error {
	r, err := s.q.GetRegistryInternal(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	refs, err := s.references(ctx, id)
	if err != nil {
		return err
	}
	if refs.total() > 0 {
		return errInUse(r.Name, refs)
	}
	return nil
}

func (s *Service) remove(ctx context.Context, r db.Registry) error {
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		// The references are re-counted inside the transaction that deletes the row.
		n, err := q.CountRegistryReferences(ctx, &r.ID)
		if err != nil {
			return err
		}
		refs := RegistryReferences{Teams: int(n.Teams), Projects: int(n.Projects), Services: int(n.Services)}
		if refs.total() > 0 {
			return errInUse(r.Name, refs)
		}
		if _, err := q.DeleteRegistry(ctx, db.DeleteRegistryParams{ID: r.ID, WorkspaceID: r.WorkspaceID}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: AuditDelete, ResourceType: "registry", ResourceID: r.ID.String(),
			Metadata: map[string]any{"name": r.Name, "kind": r.Kind, "host": r.Host},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return err
		}
		return fmt.Errorf("delete registry: %w", err)
	}
	s.publish(r.WorkspaceID, r.ID)
	return nil
}
