package variables

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// ValidateVariableInput is a draft value to check.
type ValidateVariableInput struct {
	Key      string `json:"key,omitempty" doc:"Name of the variable being edited (defaults to a placeholder)"`
	Value    string `json:"value"`
	IsSecret bool   `json:"isSecret,omitempty"`
}

// SyntaxProblem locates a template error.
type SyntaxProblem struct {
	Offset  int    `json:"offset"`
	Message string `json:"message"`
}

// RefCheck is the dry-run result of one reference of a draft.
type RefCheck struct {
	NS       string `json:"ns,omitempty"`
	Key      string `json:"key"`
	Resolved bool   `json:"resolved"`
	Secret   bool   `json:"secret"`
	Message  string `json:"message,omitempty"`
}

// ValidateVariableResult is the outcome of ValidateDraft. It never contains a secret
// value or the output of a generator.
type ValidateVariableResult struct {
	OK             bool                    `json:"ok"`
	Error          *SyntaxProblem          `json:"error,omitempty"`
	Code           string                  `json:"code,omitempty"`
	Message        string                  `json:"message,omitempty"`
	Warnings       []VariableWarning       `json:"warnings"`
	References     []RefCheck              `json:"references"`
	Generators     []varexpr.GeneratorCall `json:"generators"`
	ContainsSecret bool                    `json:"containsSecret" doc:"The value is or becomes secret (declared, a secret generator, or a secret reference)"`
	Preview        *string                 `json:"preview,omitempty" doc:"The resolved value for non-secret drafts"`
}

// ValidateDraft parses and dry-run resolves a draft value in the context of a
// service, without writing anything.
func (s *Service) ValidateDraft(ctx context.Context, serviceID uuid.UUID, in ValidateVariableInput) (ValidateVariableResult, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return ValidateVariableResult{}, err
	}
	out := ValidateVariableResult{Warnings: []VariableWarning{}, References: []RefCheck{}, Generators: []varexpr.GeneratorCall{}}
	fail := func(err error) (ValidateVariableResult, error) {
		var se *varexpr.SyntaxError
		if errors.As(err, &se) {
			out.Error = &SyntaxProblem{Offset: se.Offset, Message: se.Message}
			out.Code, out.Message = "invalid_template", se.Error()
			return out, nil
		}
		if ae, ok := apierr.As(err); ok {
			out.Code, out.Message = ae.Code, ae.Message
			return out, nil
		}
		return ValidateVariableResult{}, err
	}
	key := in.Key
	if key == "" {
		key = "DRAFT"
	} else if err := ValidateKey(key); err != nil {
		return fail(err)
	}
	if len(in.Value) > maxValueLen {
		return fail(apierr.Unprocessable("value_too_large", "the value exceeds 64 KiB"))
	}
	tpl, err := varexpr.Parse(in.Value)
	if err != nil {
		return fail(err)
	}
	m, err := varexpr.MaterializeTemplate(tpl)
	if err != nil {
		return fail(err)
	}
	for _, w := range m.Warnings {
		out.Warnings = append(out.Warnings, VariableWarning{Key: in.Key, Code: w.Code, Message: w.Message})
	}
	out.Warnings = append(out.Warnings, hygiene(in.Key, in.Value)...)
	out.Generators = append(out.Generators, m.Calls...)
	secret := in.IsSecret || m.Secret
	res, err := services.LoadResolver(ctx, s.q, s.box, sc.EnvInfo())
	if err != nil {
		return ValidateVariableResult{}, err
	}
	stored := m.Stored
	for _, r := range tpl.Refs() {
		rc := RefCheck{NS: r.NS, Key: r.Key, Resolved: true}
		_, sec, err := res.ResolveDraft(sc.Service.Slug, "DRAFT_REF", "${{"+r.String()+"}}", false)
		if err != nil {
			rc.Resolved = false
			if ae, ok := apierr.As(err); ok {
				rc.Message = ae.Message
			} else {
				rc.Message = err.Error()
			}
		}
		rc.Secret = sec
		out.References = append(out.References, rc)
	}
	val, sec, err := res.ResolveDraft(sc.Service.Slug, key, stored, secret)
	if err != nil {
		ae, ok := apierr.As(err)
		if ok && ae.Code == "reference_cycle" {
			return fail(err)
		}
		// Unresolved references are a warning at write time: providers may be created later.
		if ok {
			out.Warnings = append(out.Warnings, VariableWarning{Key: in.Key, Code: ae.Code, Message: ae.Message})
		}
		out.OK, out.ContainsSecret = true, secret
		return out, nil
	}
	out.OK, out.ContainsSecret = true, sec
	if !sec && !m.Generated() {
		out.Preview = &val
	}
	return out, nil
}

// VariableHistoryEntry is one version of a variable. Secrets carry a fingerprint only.
type VariableHistoryEntry struct {
	Version     int                     `json:"version"`
	Op          string                  `json:"op" enum:"set,delete,rotate,restore"`
	IsSecret    bool                    `json:"isSecret"`
	Source      string                  `json:"source"`
	Scope       string                  `json:"scope"`
	Value       *string                 `json:"value,omitempty" doc:"Non-secret values only"`
	Fingerprint string                  `json:"fingerprint" doc:"8 hex characters of an HMAC of the value: equal fingerprints mean equal values"`
	Generated   []varexpr.GeneratorCall `json:"generated" nullable:"true"`
	ActorType   string                  `json:"actorType"`
	ActorID     string                  `json:"actorId,omitempty"`
	ReleaseID   *uuid.UUID              `json:"releaseId" nullable:"true" doc:"The release that first shipped this version"`
	CreatedAt   time.Time               `json:"createdAt"`
	Restorable  bool                    `json:"restorable"`
}

// History lists the versions of a service variable, newest first.
func (s *Service) History(ctx context.Context, serviceID uuid.UUID, key string) ([]VariableHistoryEntry, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return nil, err
	}
	return s.history(ctx, s.q, services.OwnerService, sc.Service.ID, key)
}

func (s *Service) history(ctx context.Context, q *db.Queries, kind string, owner uuid.UUID, key string) ([]VariableHistoryEntry, error) {
	rows, err := q.ListVariableVersions(ctx, db.ListVariableVersionsParams{OwnerKind: kind, OwnerID: owner, Key: key, Limit: 50})
	if err != nil {
		return nil, err
	}
	out := make([]VariableHistoryEntry, 0, len(rows))
	for _, r := range rows {
		e := VariableHistoryEntry{
			Version: int(r.Version), Op: r.Op, IsSecret: r.IsSecret, Source: r.Source, Scope: r.Scope, Fingerprint: r.Fingerprint,
			Generated: decodeCalls(r.GenCalls), ActorType: r.ActorType, ReleaseID: r.ReleaseID, CreatedAt: r.CreatedAt,
			Restorable: !r.IsSecret && r.Op != services.OpDelete && len(r.ValueEnc) > 0,
		}
		if r.ActorID != nil {
			e.ActorID = *r.ActorID
		}
		if len(r.ValueEnc) > 0 {
			v, err := s.box.OpenString(secretbox.VariableVersionsValue, versionRowID(r), r.ValueEnc)
			if err != nil {
				return nil, err
			}
			e.Value = &v
		}
		out = append(out, e)
	}
	return out, nil
}

// versionRowID is the row id variable_versions values are sealed with.
func versionRowID(r db.VariableVersion) string {
	return secretbox.RowID(r.OwnerKind, r.OwnerID.String(), r.Key, strconv.Itoa(int(r.Version)))
}

// Restore writes an old non-secret version again as a new version. Secrets
// cannot be restored from history (use a release rollback or re-enter them).
func (s *Service) Restore(ctx context.Context, serviceID uuid.UUID, key string, version int) (VariableList, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return VariableList{}, err
	}
	var (
		changes  []changeEntry
		restaged []RestagedServiceRef
	)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.LockServiceRow(ctx, serviceID); err != nil {
			return apierr.NotFound("service")
		}
		row, err := q.GetVariableVersion(ctx, db.GetVariableVersionParams{OwnerKind: services.OwnerService, OwnerID: serviceID, Key: key, Version: int32(version)})
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("variable version")
		}
		if err != nil {
			return err
		}
		if row.IsSecret || len(row.ValueEnc) == 0 || row.Op == services.OpDelete {
			return apierr.Unprocessable("restore_unsupported", "secret values are not kept in history; re-enter the value or roll back to a release")
		}
		val, err := s.box.OpenString(secretbox.VariableVersionsValue, versionRowID(row), row.ValueEnc)
		if err != nil {
			return err
		}
		if err := credentialManaged(sc, key); err != nil {
			return err
		}
		before, err := s.snapshotEnv(ctx, q, sc.EnvInfo())
		if err != nil {
			return err
		}
		w := services.VarWrite{Key: key, Value: val, Secret: false, Scope: row.Scope, Source: services.SourceManual, Op: services.OpRestore}
		res, err := services.WriteServiceVariable(ctx, q, s.box, sc, w)
		if err != nil {
			return err
		}
		if !res.Changed {
			return nil
		}
		changes = []changeEntry{s.changeOf(res, w)}
		if _, err := s.checkCycles(ctx, q, sc.EnvInfo(), map[string]bool{key: true}, sc.Service.Slug); err != nil {
			return err
		}
		if _, err := q.BumpServiceGeneration(ctx, serviceID); err != nil {
			return err
		}
		if restaged, err = s.restage(ctx, q, []*envSnapshot{before}, serviceID); err != nil {
			return err
		}
		return s.auditSet(ctx, tx, sc, "variable.restore", changes, restaged)
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return VariableList{}, err
		}
		return VariableList{}, fmt.Errorf("restore variable: %w", err)
	}
	if len(changes) > 0 {
		s.publishChange(sc, restaged)
	}
	out, err := s.List(ctx, serviceID)
	if err != nil {
		return VariableList{}, err
	}
	out.Restaged = restaged
	return out, nil
}

// SharedHistory lists the versions of a shared variable.
func (s *Service) SharedHistory(ctx context.Context, kind string, id uuid.UUID, key string) ([]VariableHistoryEntry, error) {
	t, err := s.loadTarget(ctx, s.q, policy.ScopeProjectsRead, kind, id)
	if err != nil {
		return nil, err
	}
	return s.history(ctx, s.q, t.ownerKind(), t.ownerID, key)
}
