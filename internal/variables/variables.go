// Package variables implements the environment variables API: encrypted
// storage, write-only secrets, generators that run once at write
// time, shared variables, version history, rotation plans, suggestions and the
// restaging of dependents. VariableReference expansion lives in services.Resolver and
// template parsing in varexpr.
package variables

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

const (
	maxKeyLen   = 255
	maxValueLen = varexpr.MaxValueLen
	maxPerPut   = 500
	maxAuditStr = 256
)

// VariableReference points at a variable a value refers to.
type VariableReference struct {
	NS  string `json:"ns,omitempty" doc:"Service slug, shared, or empty for the same service"`
	Key string `json:"key"`
}

// Variable is the API view of a variable. A secret's value is never returned.
type Variable struct {
	Key   string  `json:"key"`
	Value *string `json:"value,omitempty" doc:"Omitted for secrets"`
	// IsSecret is the declared flag; ComputedSecret is also true for a variable
	// that is only secret because it embeds a secret through a reference.
	IsSecret       bool                    `json:"isSecret"`
	ComputedSecret bool                    `json:"computedSecret"`
	Set            bool                    `json:"set"`
	Source         string                  `json:"source" enum:"manual,generated,imported,suggested,template"`
	Scope          string                  `json:"scope" enum:"runtime,build,both"`
	Version        int                     `json:"version"`
	Comment        string                  `json:"comment"`
	Generated      []varexpr.GeneratorCall `json:"generated" nullable:"true" doc:"Generator descriptors of a generated variable (never the template text)"`
	References     []VariableReference     `json:"references"`
	RotateEvery    *int                    `json:"rotateEveryDays" nullable:"true"`
	AgeDays        int                     `json:"ageDays"`
	RotationDue    bool                    `json:"rotationDue"`
	Problem        *string                 `json:"problem,omitempty" doc:"Why the variable does not resolve right now"`
	UpdatedAt      time.Time               `json:"updatedAt"`
}

// PlatformVar is a reserved variable provided by NebulaCtrl.
type PlatformVar struct {
	Key           string `json:"key"`
	Value         string `json:"value"`
	Description   string `json:"description"`
	Category      string `json:"category" enum:"release,release-self,deployment,container"`
	Referenceable bool   `json:"referenceable"`
}

// ExportVar is a virtual variable other services can reference.
type ExportVar struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	Secret      bool   `json:"secret"`
}

// VariableWarning is a non-blocking problem with a write.
type VariableWarning struct {
	Key     string `json:"key,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RestagedServiceRef is a service whose resolved environment changed because a
// variable it references changed; it now has staged changes.
type RestagedServiceRef struct {
	ServiceID uuid.UUID `json:"serviceId"`
	Slug      string    `json:"slug"`
}

// VariableList is the response of listing or writing a service's variables.
type VariableList struct {
	Items    []Variable           `json:"items"`
	Platform []PlatformVar        `json:"platform"`
	Exports  []ExportVar          `json:"exports"`
	Warnings []VariableWarning    `json:"warnings"`
	Restaged []RestagedServiceRef `json:"restaged"`
}

// VariableInput is one variable of a bulk upsert.
type VariableInput struct {
	Key   string  `json:"key" minLength:"1" maxLength:"255"`
	Value *string `json:"value,omitempty" doc:"Omit to keep the stored value (secrets are write-only). May contain generators such as ${{secret(32)}}: they run once, and the same text again keeps the value."`
	// IsSecret nil keeps the stored flag (false for a new variable); an explicit
	// false is rejected for generators that produce secrets.
	IsSecret        *bool   `json:"isSecret,omitempty"`
	Scope           *string `json:"scope,omitempty" enum:"runtime,build,both"`
	Comment         *string `json:"comment,omitempty" maxLength:"1000"`
	RotateEveryDays *int    `json:"rotateEveryDays,omitempty" minimum:"0" doc:"Reminder only; 0 clears it. Rotation is never automatic."`
	// Source "suggested" marks a variable added from a suggestion (a key of the
	// repository's .env.example). A suggested variable still empty at deploy
	// time is listed in the deploy plan's warnings; writing a value through any
	// other path makes it a manual variable.
	Source *string `json:"source,omitempty" enum:"suggested"`
}

// Service implements the variable use cases.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
	box  *secretbox.Box

	tpl envTemplates
}

// NewService returns a Service (arguments may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, bus *events.Bus, box *secretbox.Box) *Service {
	return &Service{pool: pool, q: db.New(pool), bus: bus, box: box}
}

// ValidateKey checks a variable name.
func ValidateKey(key string) error {
	if len(key) > maxKeyLen || !varexpr.IsKey(key) {
		return apierr.Unprocessable("invalid_variable_key", "variable names match ^[A-Za-z_][A-Za-z0-9_]*$ (max 255 characters)")
	}
	if services.IsPlatformKey(key) {
		return apierr.Unprocessable("reserved_variable", key+" is provided by the platform (PORT and NEBULA_* are reserved) and cannot be set")
	}
	return nil
}

// templateError converts a parse/validation error to a 422.
func templateError(key string, err error) error {
	var se *varexpr.SyntaxError
	if errors.As(err, &se) {
		return apierr.Unprocessable("invalid_template", fmt.Sprintf("variable %s: %s", key, se.Error())).
			WithDetails(map[string]any{"key": key, "offset": se.Offset, "message": se.Message})
	}
	return apierr.Unprocessable("invalid_template", fmt.Sprintf("variable %s: %s", key, err.Error()))
}

func references(value string) []VariableReference {
	tpl, err := varexpr.Parse(value)
	if err != nil {
		return []VariableReference{}
	}
	out := []VariableReference{}
	seen := map[VariableReference]bool{}
	for _, r := range tpl.Refs() {
		ref := VariableReference{NS: r.NS, Key: r.Key}
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}

func truncate(s string) string {
	if len(s) <= maxAuditStr {
		return s
	}
	return s[:maxAuditStr] + "…"
}

// hygiene warns about characters that make a value look right and behave wrong.
func hygiene(key, value string) []VariableWarning {
	if value == "" {
		return nil
	}
	var out []VariableWarning
	if strings.TrimSpace(value) != value {
		out = append(out, VariableWarning{Key: key, Code: "whitespace", Message: "The value has leading or trailing whitespace."})
	}
	for _, r := range value {
		switch {
		case r == '\u200b' || r == '\u200c' || r == '\u200d' || r == '\ufeff' || r == '\u2060':
			return append(out, VariableWarning{Key: key, Code: "invisible_character", Message: "The value contains a zero-width character."})
		case r == '\u2013' || r == '\u2014' || r == '\u2212':
			return append(out, VariableWarning{Key: key, Code: "non_ascii_dash", Message: "The value contains a typographic dash (–) where a hyphen (-) is probably meant."})
		case r == '\u00a0':
			return append(out, VariableWarning{Key: key, Code: "invisible_character", Message: "The value contains a non-breaking space."})
		case unicode.Is(unicode.Cf, r):
			return append(out, VariableWarning{Key: key, Code: "invisible_character", Message: "The value contains an invisible formatting character."})
		}
	}
	return out
}

// envSignature fingerprints a resolved environment (values, secrecy, scope and
// resolution problems) so before/after states can be compared without keeping
// two copies of every value around.
func envSignature(r services.Resolved, probs []services.Problem) string {
	h := sha256.New()
	keys := make([]string, 0, len(r.Vars))
	for k := range r.Vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "%d:%s=%d:%s|%t|%s\n", len(k), k, len(r.Vars[k]), r.Vars[k], r.Secret[k], r.Scope[k])
	}
	for _, p := range probs {
		fmt.Fprintf(h, "!%s:%s\n", p.Key, p.Code)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// envSnapshot is the signature of every service of an environment.
type envSnapshot struct {
	info services.EnvInfo
	sigs map[string]sigEntry
}

type sigEntry struct {
	id  uuid.UUID
	sig string
}

func (s *Service) snapshotEnv(ctx context.Context, q *db.Queries, info services.EnvInfo) (*envSnapshot, error) {
	res, err := services.LoadResolver(ctx, q, s.box, info)
	if err != nil {
		return nil, err
	}
	snap := &envSnapshot{info: info, sigs: map[string]sigEntry{}}
	for _, slug := range res.Slugs() {
		r, probs, err := res.ResolveServiceLenient(slug)
		if err != nil {
			return nil, err
		}
		sd, _ := res.Service(slug)
		snap.sigs[slug] = sigEntry{id: sd.ID, sig: envSignature(r, probs)}
	}
	return snap, nil
}

// restage bumps the generation of every service (other than skip) whose
// resolved environment differs from before, and returns them.
func (s *Service) restage(ctx context.Context, q *db.Queries, before []*envSnapshot, skip uuid.UUID) ([]RestagedServiceRef, error) {
	out := []RestagedServiceRef{}
	for _, b := range before {
		after, err := s.snapshotEnv(ctx, q, b.info)
		if err != nil {
			return nil, err
		}
		slugs := make([]string, 0, len(after.sigs))
		for slug := range after.sigs {
			slugs = append(slugs, slug)
		}
		sort.Strings(slugs)
		for _, slug := range slugs {
			a := after.sigs[slug]
			if a.id == skip {
				continue
			}
			if old, ok := b.sigs[slug]; ok && old.sig == a.sig {
				continue
			}
			if _, err := q.BumpServiceGeneration(ctx, a.id); err != nil {
				return nil, err
			}
			out = append(out, RestagedServiceRef{ServiceID: a.id, Slug: slug})
		}
	}
	return out, nil
}

// checkCycles rejects the write when any variable of the environment now
// forms a reference cycle. Unresolved references only warn (providers may be
// created later); they are returned as warnings for the written keys.
func (s *Service) checkCycles(ctx context.Context, q *db.Queries, info services.EnvInfo, written map[string]bool, slug string) ([]VariableWarning, error) {
	res, err := services.LoadResolver(ctx, q, s.box, info)
	if err != nil {
		return nil, err
	}
	var warns []VariableWarning
	for _, sl := range res.Slugs() {
		probs, err := res.Diagnose(sl)
		if err != nil {
			return nil, err
		}
		for _, p := range probs {
			switch p.Code {
			case "reference_cycle":
				e := apierr.Unprocessable("reference_cycle", p.Message)
				if p.Details != nil {
					e = e.WithDetails(p.Details)
				}
				return nil, e
			case "unresolved_reference":
				if sl == slug && written[p.Key] {
					warns = append(warns, VariableWarning{Key: p.Key, Code: p.Code, Message: p.Message})
				}
			}
		}
	}
	sharedProbs := res.DiagnoseShared()
	for _, p := range sharedProbs {
		if p.Code == "reference_cycle" {
			return nil, apierr.Unprocessable("reference_cycle", p.Message).WithDetails(p.Details)
		}
	}
	return warns, nil
}

type changeEntry struct {
	Key           string `json:"key"`
	Op            string `json:"op"`
	Version       int32  `json:"version"`
	From          string `json:"from,omitempty"`
	To            string `json:"to,omitempty"`
	FingerprintFr string `json:"fingerprintFrom,omitempty"`
	FingerprintTo string `json:"fingerprintTo,omitempty"`
}

func (s *Service) toVariable(row db.ServiceVariable, plain string, computed bool, now time.Time) Variable {
	v := Variable{
		Key: row.Key, IsSecret: row.IsSecret, ComputedSecret: row.IsSecret || computed, Set: true, Source: row.Source, Scope: row.Scope,
		Version: int(row.Version), Comment: row.Comment, References: []VariableReference{}, UpdatedAt: row.UpdatedAt,
		AgeDays: int(now.Sub(row.UpdatedAt).Hours() / 24),
	}
	if row.RotateEveryDays != nil {
		n := int(*row.RotateEveryDays)
		v.RotateEvery = &n
		v.RotationDue = v.AgeDays >= n
	}
	if !row.IsSecret {
		v.Value = &plain
	}
	if row.Source == services.SourceGenerated && len(row.GenCalls) > 0 {
		var calls []varexpr.GeneratorCall
		if err := jsonUnmarshal(row.GenCalls, &calls); err == nil {
			v.Generated = calls
		}
	}
	// References come from the stored text; for generated secrets that text is
	// the literal secret, which has none unless the template mixed refs in.
	v.References = references(plain)
	return v
}

// List returns the service's variables, the platform variables and the exports
// of the service.
func (s *Service) List(ctx context.Context, serviceID uuid.UUID) (VariableList, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return VariableList{}, err
	}
	return s.list(ctx, s.q, sc)
}

func (s *Service) list(ctx context.Context, q *db.Queries, sc services.Scope) (VariableList, error) {
	rows, err := q.ListServiceVariables(ctx, sc.Service.ID)
	if err != nil {
		return VariableList{}, fmt.Errorf("list variables: %w", err)
	}
	res, err := services.LoadResolver(ctx, q, s.box, sc.EnvInfo())
	if err != nil {
		return VariableList{}, err
	}
	resolved, probs, err := res.ResolveServiceLenient(sc.Service.Slug)
	if err != nil {
		return VariableList{}, err
	}
	probByKey := map[string]string{}
	for _, p := range probs {
		probByKey[p.Key] = p.Message
	}
	now := time.Now()
	out := VariableList{Items: make([]Variable, 0, len(rows)), Platform: []PlatformVar{}, Exports: []ExportVar{}, Warnings: []VariableWarning{}, Restaged: []RestagedServiceRef{}}
	for _, r := range rows {
		plain, err := s.box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(r.ServiceID.String(), r.Key), r.ValueEnc)
		if err != nil {
			return VariableList{}, fmt.Errorf("decrypt variable %s: %w", r.Key, err)
		}
		v := s.toVariable(r, plain, resolved.Secret[r.Key], now)
		if msg, ok := probByKey[r.Key]; ok {
			v.Problem = &msg
		}
		out.Items = append(out.Items, v)
	}
	vals := res.PlatformValues(sc.Service.Slug)
	for _, p := range services.PlatformVars {
		out.Platform = append(out.Platform, PlatformVar{Key: p.Key, Value: vals[p.Key], Description: p.Description, Category: p.Category, Referenceable: p.Referenceable})
	}
	for _, e := range res.Exports(sc.Service.Slug) {
		out.Exports = append(out.Exports, ExportVar{Key: e.Key, Description: e.Description, Secret: e.Secret})
	}
	return out, nil
}

// credentialManaged rejects edits of a deployed managed database's credentials:
// the database image reads them only when it initialises its data, so the
// database panel (rotate credentials) owns them. A database that was never
// deployed stays freely editable.
func credentialManaged(sc services.Scope, key string) error {
	cfg := sc.Config()
	if cfg.Database != nil && sc.Service.CurrentDeploymentID != nil && cfg.Database.IsCredentialKey(key) {
		return apierr.Conflict("credential_managed", key+" belongs to the managed database; use Rotate credentials in the database panel")
	}
	return nil
}

// preparedWrite is one input turned into a write.
type preparedWrite struct {
	w        services.VarWrite
	warnings []VariableWarning
}

// prepareWrite decides what to store for one input: it validates and
// materialises templates (generators run exactly once) and keeps the stored
// value when the same template text is written again.
func (s *Service) prepareWrite(ctx context.Context, q *db.Queries, sc services.Scope, in VariableInput) (preparedWrite, error) {
	var pw preparedWrite
	if err := credentialManaged(sc, in.Key); err != nil {
		return pw, err
	}
	cur, err := q.GetServiceVariable(ctx, db.GetServiceVariableParams{ServiceID: sc.Service.ID, Key: in.Key})
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return pw, err
	}
	scope := ""
	if in.Scope != nil {
		if !services.ValidScope(*in.Scope) {
			return pw, apierr.Unprocessable("invalid_scope", "scope must be runtime, build or both")
		}
		scope = *in.Scope
	}
	if scope == "" && exists {
		scope = cur.Scope
	}
	if (scope == services.ScopeBuild || scope == services.ScopeBoth) && sc.Service.SourceType != services.SourceGit {
		return pw, apierr.Unprocessable("build_scope_requires_git", "build-time variables only apply to services built from a git source")
	}
	secret := exists && cur.IsSecret
	if in.IsSecret != nil {
		secret = *in.IsSecret
	}
	w := services.VarWrite{Key: in.Key, Scope: scope, Comment: in.Comment, RotateEveryDays: in.RotateEveryDays, Source: ""}
	var oldValue, oldTemplate string
	if exists {
		if oldValue, err = s.box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(cur.ServiceID.String(), cur.Key), cur.ValueEnc); err != nil {
			return pw, fmt.Errorf("decrypt %s: %w", in.Key, err)
		}
		if len(cur.TemplateEnc) > 0 {
			if oldTemplate, err = s.box.OpenString(secretbox.ServiceVariablesTemplate, secretbox.RowID(cur.ServiceID.String(), cur.Key), cur.TemplateEnc); err != nil {
				return pw, fmt.Errorf("decrypt template of %s: %w", in.Key, err)
			}
		}
	}
	switch {
	case in.Value == nil && !exists:
		return pw, apierr.Unprocessable("value_required", "variable "+in.Key+" needs a value")
	case in.Value == nil:
		if cur.IsSecret && in.IsSecret != nil && !*in.IsSecret {
			return pw, apierr.Unprocessable("value_required", "provide a value to make secret "+in.Key+" non-secret")
		}
		w.Value, w.Secret, w.Source, w.Template = oldValue, secret, cur.Source, oldTemplate
		w.GenCalls = decodeCalls(cur.GenCalls)
	default:
		val := *in.Value
		if len(val) > maxValueLen {
			return pw, apierr.Unprocessable("value_too_large", "variable "+in.Key+" exceeds 64 KiB")
		}
		tpl, err := varexpr.Parse(val)
		if err != nil {
			return pw, templateError(in.Key, err)
		}
		if !tpl.HasGenerators() {
			if _, err := varexpr.Validate(tpl); err != nil {
				return pw, templateError(in.Key, err)
			}
			w.Value, w.Secret, w.Source = val, secret, services.SourceManual
			if in.Source != nil && *in.Source == services.SourceSuggested && (!exists || cur.Source == services.SourceSuggested) {
				w.Source = services.SourceSuggested
			}
			if exists && cur.Source == services.SourceImported {
				w.Source = services.SourceImported
			}
			pw.warnings = append(pw.warnings, hygiene(in.Key, val)...)
			break
		}
		// Idempotent replay: the same generator text keeps the stored value.
		if exists && cur.Source == services.SourceGenerated && oldTemplate == val {
			w.Value, w.Secret, w.Source, w.Template = oldValue, secret || cur.IsSecret, services.SourceGenerated, oldTemplate
			w.GenCalls = decodeCalls(cur.GenCalls)
			break
		}
		m, err := varexpr.MaterializeTemplate(tpl)
		if err != nil {
			return pw, templateError(in.Key, err)
		}
		if m.Secret && in.IsSecret != nil && !*in.IsSecret {
			return pw, apierr.Unprocessable("secret_required", "variable "+in.Key+" uses secret()/randomBytes(): generated secrets are always secret")
		}
		w.Value, w.Source, w.Template, w.GenCalls = m.Stored, services.SourceGenerated, val, m.Calls
		w.Secret = secret || m.Secret
		for _, mw := range m.Warnings {
			pw.warnings = append(pw.warnings, VariableWarning{Key: in.Key, Code: mw.Code, Message: mw.Message})
		}
	}
	pw.w = w
	return pw, nil
}

func decodeCalls(raw []byte) []varexpr.GeneratorCall {
	var calls []varexpr.GeneratorCall
	if len(raw) == 0 || jsonUnmarshal(raw, &calls) != nil {
		return nil
	}
	return calls
}

func checkInputs(in []VariableInput) error {
	if len(in) > maxPerPut {
		return apierr.Unprocessable("too_many_variables", fmt.Sprintf("at most %d variables per request", maxPerPut))
	}
	seen := map[string]bool{}
	for _, v := range in {
		if err := ValidateKey(v.Key); err != nil {
			return err
		}
		if seen[v.Key] {
			return apierr.Unprocessable("duplicate_variable", "variable "+v.Key+" appears twice")
		}
		seen[v.Key] = true
		if v.Value != nil && len(*v.Value) > maxValueLen {
			return apierr.Unprocessable("value_too_large", "variable "+v.Key+" exceeds 64 KiB")
		}
	}
	return nil
}

// VariableChange summarises one effective variable write. It never carries a
// value.
type VariableChange struct {
	Key     string `json:"key"`
	Op      string `json:"op"`
	Version int32  `json:"version"`
}

// PutResult is what PutTx did. Changes lists the effective writes (no-op
// writes are absent) sorted by key; Restaged lists the other services whose
// resolved environment changed as a consequence.
type PutResult struct {
	Changes  []VariableChange
	Warnings []VariableWarning
	Restaged []RestagedServiceRef
}

// Put upserts variables. Existing values are kept when a Value is omitted. The
// service generation advances once if anything changed, and every service
// whose resolved environment changed as a consequence is restaged.
func (s *Service) Put(ctx context.Context, serviceID uuid.UUID, in []VariableInput) (VariableList, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return VariableList{}, err
	}
	if err := checkInputs(in); err != nil {
		return VariableList{}, err
	}
	var (
		changes  []changeEntry
		warnings []VariableWarning
		restaged []RestagedServiceRef
	)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		changes, warnings, restaged, err = s.putCore(ctx, tx, sc, in)
		return err
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return VariableList{}, err
		}
		return VariableList{}, fmt.Errorf("put variables: %w", err)
	}
	if len(changes) > 0 {
		s.publishChange(sc, restaged)
	}
	out, err := s.List(ctx, serviceID)
	if err != nil {
		return VariableList{}, err
	}
	out.Warnings, out.Restaged = append(warnings, out.Warnings...), restaged
	return out, nil
}

// PutTx is Put inside the caller's transaction. sc must have been loaded with
// services.LoadScope(ctx, db.New(tx), policy.ScopeProjectsWrite, id) on the
// same tx. The generation of the service advances once if anything changed
// and other services whose resolved environment changed are restaged; both are
// part of the transaction. The returned closure publishes the change events
// and must run after the transaction commits (it is never nil).
func (s *Service) PutTx(ctx context.Context, tx pgx.Tx, sc services.Scope, in []VariableInput) (PutResult, func(context.Context), error) {
	noop := func(context.Context) {}
	if err := checkInputs(in); err != nil {
		return PutResult{}, noop, err
	}
	changes, warnings, restaged, err := s.putCore(ctx, tx, sc, in)
	if err != nil {
		return PutResult{}, noop, err
	}
	res := PutResult{Warnings: warnings, Restaged: restaged}
	for _, c := range changes {
		res.Changes = append(res.Changes, VariableChange{Key: c.Key, Op: c.Op, Version: c.Version})
	}
	if len(changes) == 0 {
		return res, noop, nil
	}
	return res, func(context.Context) { s.publishChange(sc, restaged) }, nil
}

// putCore is the body of Put on tx: it locks the service, writes the inputs,
// rejects reference cycles, bumps the generation when anything changed,
// restages dependents and audits.
func (s *Service) putCore(ctx context.Context, tx pgx.Tx, sc services.Scope, in []VariableInput) ([]changeEntry, []VariableWarning, []RestagedServiceRef, error) {
	q := db.New(tx)
	serviceID := sc.Service.ID
	if _, err := q.LockServiceRow(ctx, serviceID); err != nil {
		return nil, nil, nil, apierr.NotFound("service")
	}
	before, err := s.snapshotEnv(ctx, q, sc.EnvInfo())
	if err != nil {
		return nil, nil, nil, err
	}
	written := map[string]bool{}
	changes, warnings, err := s.writeMany(ctx, q, sc, in, services.OpSet, written)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(changes) == 0 {
		return nil, warnings, nil, nil
	}
	ws, err := s.checkCycles(ctx, q, sc.EnvInfo(), written, sc.Service.Slug)
	if err != nil {
		return nil, nil, nil, err
	}
	warnings = append(warnings, ws...)
	if _, err := q.BumpServiceGeneration(ctx, serviceID); err != nil {
		return nil, nil, nil, err
	}
	restaged, err := s.restage(ctx, q, []*envSnapshot{before}, serviceID)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := s.auditSet(ctx, tx, sc, "variable.set", changes, restaged); err != nil {
		return nil, nil, nil, err
	}
	return changes, warnings, restaged, nil
}

func (s *Service) publishChange(sc services.Scope, restaged []RestagedServiceRef) {
	services.Publish(s.bus, services.EventVariableUpdated, sc.ProjectID, sc.EnvironmentID, sc.Service.ID)
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
	for _, r := range restaged {
		services.Publish(s.bus, services.EventServiceUpdated, sc.ProjectID, sc.EnvironmentID, r.ServiceID)
	}
}

// writeMany applies inputs inside the caller's transaction and returns the
// effective changes (no-op writes are skipped).
func (s *Service) writeMany(ctx context.Context, q *db.Queries, sc services.Scope, in []VariableInput, op string, written map[string]bool) ([]changeEntry, []VariableWarning, error) {
	var changes []changeEntry
	var warnings []VariableWarning
	for _, v := range in {
		pw, err := s.prepareWrite(ctx, q, sc, v)
		if err != nil {
			return nil, nil, err
		}
		pw.w.Op = op
		res, err := services.WriteServiceVariable(ctx, q, s.box, sc, pw.w)
		if err != nil {
			return nil, nil, err
		}
		warnings = append(warnings, pw.warnings...)
		if !res.Changed {
			continue
		}
		written[v.Key] = true
		changes = append(changes, s.changeOf(res, pw.w))
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Key < changes[j].Key })
	return changes, warnings, nil
}

func (s *Service) changeOf(res services.VarWriteResult, w services.VarWrite) changeEntry {
	op := w.Op
	if op == "" {
		op = services.OpSet
	}
	c := changeEntry{Key: w.Key, Op: op, Version: res.Row.Version, FingerprintTo: res.NewFP}
	if res.HadOld {
		c.FingerprintFr = res.OldFP
	}
	if w.Secret || res.OldSecret {
		if w.Source == services.SourceGenerated && res.NewFP != res.OldFP {
			c.To = "generated " + services.GenDescriptor(res.Row.GenCalls)
		}
		return c
	}
	if res.HadOld {
		c.From = truncate(res.OldValue)
	}
	c.To = truncate(w.Value)
	return c
}

func (s *Service) auditSet(ctx context.Context, tx pgx.Tx, sc services.Scope, action string, changes []changeEntry, restaged []RestagedServiceRef) error {
	keys := make([]string, len(changes))
	for i, c := range changes {
		keys[i] = c.Key
	}
	affected := make([]string, len(restaged))
	for i, r := range restaged {
		affected[i] = r.Slug
	}
	// Keys, versions, fingerprints and non-secret values only; never a secret value.
	return audit.Record(ctx, tx, audit.Entry{
		Action: action, ResourceType: "service", ResourceID: sc.Service.ID.String(), ProjectID: &sc.ProjectID, WorkspaceID: sc.WorkspaceID,
		Metadata: map[string]any{"service": sc.Service.Slug, "keys": keys, "changes": changes, "affected": affected},
	})
}

// Delete removes a variable.
func (s *Service) Delete(ctx context.Context, serviceID uuid.UUID, key string) error {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return err
	}
	var restaged []RestagedServiceRef
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		found, r, err := s.deleteCore(ctx, tx, sc, key)
		if err != nil {
			return err
		}
		if !found {
			return apierr.NotFound("variable")
		}
		restaged = r
		return nil
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return err
		}
		return fmt.Errorf("delete variable: %w", err)
	}
	s.publishChange(sc, restaged)
	return nil
}

// DeleteTx is Delete inside the caller's transaction (see PutTx for how sc is
// loaded). A missing variable yields found=false and no changes instead of a
// not-found error. The returned closure publishes the change events and must
// run after commit (never nil).
func (s *Service) DeleteTx(ctx context.Context, tx pgx.Tx, sc services.Scope, key string) (bool, []RestagedServiceRef, func(context.Context), error) {
	noop := func(context.Context) {}
	found, restaged, err := s.deleteCore(ctx, tx, sc, key)
	if err != nil || !found {
		return false, nil, noop, err
	}
	return true, restaged, func(context.Context) { s.publishChange(sc, restaged) }, nil
}

// deleteCore is the body of Delete on tx. found=false means nothing existed
// and nothing was written.
func (s *Service) deleteCore(ctx context.Context, tx pgx.Tx, sc services.Scope, key string) (bool, []RestagedServiceRef, error) {
	q := db.New(tx)
	serviceID := sc.Service.ID
	if _, err := q.LockServiceRow(ctx, serviceID); err != nil {
		return false, nil, apierr.NotFound("service")
	}
	if err := credentialManaged(sc, key); err != nil {
		return false, nil, err
	}
	before, err := s.snapshotEnv(ctx, q, sc.EnvInfo())
	if err != nil {
		return false, nil, err
	}
	row, ok, err := services.DeleteServiceVariableVersioned(ctx, q, s.box, sc, key)
	if err != nil {
		return false, nil, err
	}
	if !ok {
		return false, nil, nil
	}
	if _, err := q.BumpServiceGeneration(ctx, serviceID); err != nil {
		return false, nil, err
	}
	restaged, err := s.restage(ctx, q, []*envSnapshot{before}, serviceID)
	if err != nil {
		return false, nil, err
	}
	if err := s.auditSet(ctx, tx, sc, "variable.delete", []changeEntry{{Key: key, Op: services.OpDelete, Version: row.Version + 1}}, restaged); err != nil {
		return false, nil, err
	}
	return true, restaged, nil
}

var (
	jsonUnmarshal = json.Unmarshal
	jsonMarshal   = json.Marshal
)
