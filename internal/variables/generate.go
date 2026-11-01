package variables

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// Materialized is a template whose generator calls ran once.
type Materialized = varexpr.Materialized

// Materialize runs the generators of template once and returns the text to
// store. References, transforms and `$${{` escapes are left intact.
func Materialize(template string) (Materialized, error) { return varexpr.Materialize(template) }

// UpsertResult describes an UpsertTx.
type UpsertResult struct {
	// Secret is the stored secrecy (forced on by secret generators).
	Secret bool
	// Generated is true when a generator ran.
	Generated bool
	Warnings  []VariableWarning
}

// UpsertTx writes one variable of serviceID inside the caller's transaction,
// for in-process callers that already authorized the operation (template
// install, database provisioning). value may contain generators, which run
// once; secret generators force isSecret. It records the version, bumps the
// service generation and writes an audit entry (key only). It does not fan out
// to dependents: use it when creating a service's variables.
func (s *Service) UpsertTx(ctx context.Context, tx pgx.Tx, serviceID uuid.UUID, key, value string, isSecret bool) (UpsertResult, error) {
	return s.upsertTx(ctx, tx, serviceID, key, value, isSecret, services.SourceTemplate)
}

func (s *Service) upsertTx(ctx context.Context, tx pgx.Tx, serviceID uuid.UUID, key, value string, isSecret bool, source string) (UpsertResult, error) {
	q := db.New(tx)
	sc, err := services.LoadScopeInternal(ctx, q, serviceID)
	if err != nil {
		return UpsertResult{}, err
	}
	if err := ValidateKey(key); err != nil {
		return UpsertResult{}, err
	}
	secret := isSecret
	if tpl, err := varexpr.Parse(value); err == nil {
		for _, e := range tpl.Exprs() {
			if e.Call != nil && (e.Call.Fn == varexpr.FnSecret || e.Call.Fn == varexpr.FnRandomBytes) {
				secret = true
			}
		}
	}
	pw, err := s.prepareWrite(ctx, q, sc, VariableInput{Key: key, Value: &value, IsSecret: &secret})
	if err != nil {
		return UpsertResult{}, err
	}
	if pw.w.Source == services.SourceManual && source != "" {
		pw.w.Source = source
	}
	res, err := services.WriteServiceVariable(ctx, q, s.box, sc, pw.w)
	if err != nil {
		return UpsertResult{}, err
	}
	out := UpsertResult{Secret: pw.w.Secret, Generated: pw.w.Source == services.SourceGenerated, Warnings: pw.warnings}
	if !res.Changed {
		return out, nil
	}
	if _, err := q.BumpServiceGeneration(ctx, serviceID); err != nil {
		return UpsertResult{}, err
	}
	err = audit.Record(ctx, tx, audit.Entry{
		Action: "variable.set", ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID, WorkspaceID: sc.WorkspaceID,
		Metadata: map[string]any{"service": sc.Service.Slug, "keys": []string{key}, "changes": []changeEntry{s.changeOf(res, pw.w)}},
	})
	return out, err
}
