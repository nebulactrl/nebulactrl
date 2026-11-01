package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// Variable sources.
const (
	SourceManual    = "manual"
	SourceGenerated = "generated"
	SourceImported  = "imported"
	SourceSuggested = "suggested"
	SourceTemplate  = "template"
)

// Variable version operations.
const (
	OpSet     = "set"
	OpDelete  = "delete"
	OpRotate  = "rotate"
	OpRestore = "restore"
)

// Owner kinds of a variable.
const (
	OwnerService     = "service"
	OwnerEnvironment = "environment"
	OwnerProject     = "project"
	OwnerTeam        = "team"
)

// VarWrite is one variable write. Value is the final stored text: generators
// have already run (varexpr.Materialize).
type VarWrite struct {
	Key    string
	Value  string
	Secret bool
	// Scope and Source default to the existing row's, else runtime / manual.
	Scope  string
	Source string
	// Template is the authored template text of a generated variable; it is
	// stored encrypted so the variable can be re-materialised (rotated).
	Template string
	GenCalls []varexpr.GeneratorCall
	// Comment and RotateEveryDays keep the existing value when nil.
	Comment         *string
	RotateEveryDays *int
	// Op is the history operation (default set).
	Op string
}

// VarWriteResult describes what a write did.
type VarWriteResult struct {
	Row     db.ServiceVariable
	Created bool
	// Changed is false when the write was a no-op.
	Changed bool
	// HadOld, OldValue and OldSecret describe the replaced row.
	HadOld    bool
	OldValue  string
	OldSecret bool
	OldFP     string
	NewFP     string
}

// VarActor describes who made a change.
func varActor(ctx context.Context) (typ, id string) {
	if p, ok := auth.PrincipalFrom(ctx); ok {
		switch p.Type {
		case auth.TypeUser:
			typ = "user"
		case auth.TypeMCP:
			typ = "mcp"
		case auth.TypeSystem:
			return "system", ""
		default:
			typ = "token"
		}
		return typ, p.ID().String()
	}
	return "system", ""
}

func marshalCalls(calls []varexpr.GeneratorCall) json.RawMessage {
	if len(calls) == 0 {
		return nil
	}
	b, _ := json.Marshal(calls)
	return b
}

// WriteServiceVariable upserts one variable of sc's service inside q's
// transaction and appends a history row. It does not bump the generation, fan
// out to dependents or audit: the caller does, once per request.
func WriteServiceVariable(ctx context.Context, q *db.Queries, box *secretbox.Box, sc Scope, w VarWrite) (VarWriteResult, error) {
	var res VarWriteResult
	cur, err := q.GetServiceVariable(ctx, db.GetServiceVariableParams{ServiceID: sc.Service.ID, Key: w.Key})
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return res, err
	}
	scope, source := w.Scope, w.Source
	comment, rot := "", (*int32)(nil)
	var curTemplate string
	if exists {
		res.HadOld, res.OldSecret = true, cur.IsSecret
		if res.OldValue, err = box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(sc.Service.ID.String(), w.Key), cur.ValueEnc); err != nil {
			return res, fmt.Errorf("decrypt %s: %w", w.Key, err)
		}
		res.OldFP = box.Fingerprint(res.OldValue)
		if len(cur.TemplateEnc) > 0 {
			if curTemplate, err = box.OpenString(secretbox.ServiceVariablesTemplate, secretbox.RowID(sc.Service.ID.String(), w.Key), cur.TemplateEnc); err != nil {
				return res, fmt.Errorf("decrypt template of %s: %w", w.Key, err)
			}
		}
		if scope == "" {
			scope = cur.Scope
		}
		if source == "" {
			source = cur.Source
		}
		comment, rot = cur.Comment, cur.RotateEveryDays
	}
	if scope == "" {
		scope = ScopeRuntime
	}
	if source == "" {
		source = SourceManual
	}
	if w.Comment != nil {
		comment = *w.Comment
	}
	if w.RotateEveryDays != nil {
		rot = nil
		if *w.RotateEveryDays > 0 {
			v := int32(*w.RotateEveryDays)
			rot = &v
		}
	}
	res.NewFP = box.Fingerprint(w.Value)

	same := exists && res.OldValue == w.Value && cur.IsSecret == w.Secret && cur.Scope == scope && cur.Source == source &&
		curTemplate == w.Template && cur.Comment == comment && eqInt32(cur.RotateEveryDays, rot)
	if same {
		res.Row = cur
		return res, nil
	}
	valueChanged := !exists || res.OldValue != w.Value
	op := w.Op
	if op == "" {
		op = OpSet
	}
	varRow := secretbox.RowID(sc.Service.ID.String(), w.Key)
	enc, err := box.SealString(secretbox.ServiceVariablesValue, varRow, w.Value)
	if err != nil {
		return res, err
	}
	var tmplEnc []byte
	if w.Template != "" {
		if tmplEnc, err = box.SealString(secretbox.ServiceVariablesTemplate, varRow, w.Template); err != nil {
			return res, err
		}
	}
	next, err := q.NextVariableVersion(ctx, db.NextVariableVersionParams{OwnerKind: OwnerService, OwnerID: sc.Service.ID, Key: w.Key})
	if err != nil {
		return res, err
	}
	var genAt *time.Time
	if source == SourceGenerated {
		if valueChanged || cur.GeneratedAt == nil {
			now := time.Now()
			genAt = &now
		} else {
			genAt = cur.GeneratedAt
		}
	}
	row, err := q.PutServiceVariable(ctx, db.PutServiceVariableParams{
		ServiceID: sc.Service.ID, Key: w.Key, ValueEnc: enc, IsSecret: w.Secret, Scope: scope, Source: source,
		TemplateEnc: tmplEnc, GenCalls: marshalCalls(w.GenCalls), Version: next, GeneratedAt: genAt, RotateEveryDays: rot, Comment: comment,
	})
	if err != nil {
		return res, err
	}
	if err := insertVersion(ctx, q, box, sc, OwnerService, sc.Service.ID, w.Key, next, op, w.Secret, source, scope, w.Value, res.NewFP, marshalCalls(w.GenCalls)); err != nil {
		return res, err
	}
	res.Row, res.Created, res.Changed = row, !exists, true
	return res, nil
}

func eqInt32(a, b *int32) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func insertVersion(ctx context.Context, q *db.Queries, box *secretbox.Box, sc Scope, ownerKind string, ownerID uuid.UUID, key string, version int32,
	op string, secret bool, source, scope, value, fp string, calls json.RawMessage) error {
	var enc []byte
	if !secret && op != OpDelete {
		var err error
		if enc, err = box.SealString(secretbox.VariableVersionsValue, secretbox.RowID(ownerKind, ownerID.String(), key, strconv.Itoa(int(version))), value); err != nil {
			return err
		}
	}
	at, aid := varActor(ctx)
	var actorID *string
	if aid != "" {
		actorID = &aid
	}
	var projectID *uuid.UUID
	if sc.ProjectID != uuid.Nil { // team variables belong to no project
		projectID = &sc.ProjectID
	}
	return q.InsertVariableVersion(ctx, db.InsertVariableVersionParams{
		WorkspaceID: sc.WorkspaceID, ProjectID: projectID, OwnerKind: ownerKind, OwnerID: ownerID, Key: key, Version: version, Op: op,
		IsSecret: secret, Source: source, Scope: scope, ValueEnc: enc, Fingerprint: fp, GenCalls: calls, ActorType: at, ActorID: actorID,
	})
}

// DeleteServiceVariableVersioned removes a variable and appends a delete row to
// its history. It returns the deleted row (ok=false when none existed).
func DeleteServiceVariableVersioned(ctx context.Context, q *db.Queries, box *secretbox.Box, sc Scope, key string) (db.ServiceVariable, bool, error) {
	cur, err := q.GetServiceVariable(ctx, db.GetServiceVariableParams{ServiceID: sc.Service.ID, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.ServiceVariable{}, false, nil
	}
	if err != nil {
		return db.ServiceVariable{}, false, err
	}
	val, err := box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(sc.Service.ID.String(), key), cur.ValueEnc)
	if err != nil {
		return db.ServiceVariable{}, false, fmt.Errorf("decrypt %s: %w", key, err)
	}
	if _, err := q.DeleteServiceVariable(ctx, db.DeleteServiceVariableParams{ServiceID: sc.Service.ID, Key: key}); err != nil {
		return db.ServiceVariable{}, false, err
	}
	next, err := q.NextVariableVersion(ctx, db.NextVariableVersionParams{OwnerKind: OwnerService, OwnerID: sc.Service.ID, Key: key})
	if err != nil {
		return db.ServiceVariable{}, false, err
	}
	err = insertVersion(ctx, q, box, sc, OwnerService, sc.Service.ID, key, next, OpDelete, cur.IsSecret, cur.Source, cur.Scope, val, box.Fingerprint(val), nil)
	return cur, true, err
}

// RecordSharedVersion appends a history row for a shared variable.
func RecordSharedVersion(ctx context.Context, q *db.Queries, box *secretbox.Box, workspaceID, projectID uuid.UUID, ownerKind string, ownerID uuid.UUID,
	key string, version int32, op string, secret bool, source, value string, calls json.RawMessage) error {
	sc := Scope{WorkspaceID: workspaceID, ProjectID: projectID}
	return insertVersion(ctx, q, box, sc, ownerKind, ownerID, key, version, op, secret, source, ScopeRuntime, value, box.Fingerprint(value), calls)
}

// GenDescriptor renders stored generator call descriptors as `secret(32)`
// (several calls are joined with " + ").
func GenDescriptor(raw json.RawMessage) string {
	var calls []varexpr.GeneratorCall
	if len(raw) == 0 || json.Unmarshal(raw, &calls) != nil || len(calls) == 0 {
		return ""
	}
	parts := make([]string, len(calls))
	for i, c := range calls {
		parts[i] = c.String()
	}
	return strings.Join(parts, " + ")
}
