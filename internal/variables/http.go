package variables

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type serviceIDInput struct {
	ID uuid.UUID `path:"serviceId"`
}

type listOutput struct{ Body VariableList }

type putInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		Variables []VariableInput `json:"variables" maxItems:"500"`
	}
}

type deleteInput struct {
	ID  uuid.UUID `path:"serviceId"`
	Key string    `path:"key"`
}

type keyInput struct {
	ID  uuid.UUID `path:"serviceId"`
	Key string    `path:"key"`
}

type validateInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body ValidateVariableInput
}

type validateOutput struct{ Body ValidateVariableResult }

type historyOutput struct {
	Body struct {
		Items []VariableHistoryEntry `json:"items"`
	}
}

type restoreInput struct {
	ID      uuid.UUID `path:"serviceId"`
	Key     string    `path:"key"`
	Version int       `path:"version"`
}

type VariableRotateBody struct {
	DryRun bool `json:"dryRun" doc:"true returns the plan and changes nothing; false stages the new value (nothing is deployed)"`
}

type rotateInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Key  string    `path:"key"`
	Body VariableRotateBody
}

type rotateOutput struct{ Body VariableRotationPlan }

type suggestOutput struct{ Body SuggestedVariableList }

// Shared variable inputs: one set per owner kind (project / environment).
type projectIDInput struct {
	ID uuid.UUID `path:"projectId"`
}

type envIDInput struct {
	ID uuid.UUID `path:"environmentId"`
}

type sharedList struct{ Body SharedVariableList }

type SharedVariablesPutBody struct {
	Variables []SharedVariableInput `json:"variables" maxItems:"500"`
}

type projectSharedPut struct {
	ID   uuid.UUID `path:"projectId"`
	Body SharedVariablesPutBody
}

type envSharedPut struct {
	ID   uuid.UUID `path:"environmentId"`
	Body SharedVariablesPutBody
}

type projectSharedKey struct {
	ID  uuid.UUID `path:"projectId"`
	Key string    `path:"key"`
}

type envSharedKey struct {
	ID  uuid.UUID `path:"environmentId"`
	Key string    `path:"key"`
}

type projectSharedRotate struct {
	ID   uuid.UUID `path:"projectId"`
	Key  string    `path:"key"`
	Body VariableRotateBody
}

type envSharedRotate struct {
	ID   uuid.UUID `path:"environmentId"`
	Key  string    `path:"key"`
	Body VariableRotateBody
}

type projectSharedShare struct {
	ID   uuid.UUID `path:"projectId"`
	Key  string    `path:"key"`
	Body ShareSharedInput
}

type envSharedShare struct {
	ID   uuid.UUID `path:"environmentId"`
	Key  string    `path:"key"`
	Body ShareSharedInput
}

// Team shared variables have no path id: the team is the caller's current one.
type teamSharedPut struct {
	Body SharedVariablesPutBody
}

type teamSharedKey struct {
	Key string `path:"key"`
}

type teamSharedRotate struct {
	Key  string `path:"key"`
	Body VariableRotateBody
}

type teamSharedShare struct {
	Key  string `path:"key"`
	Body ShareSharedInput
}

type usageInput struct {
	ID  uuid.UUID `path:"environmentId"`
	NS  string    `query:"ns" doc:"Namespace: shared (default), team or a service slug"`
	Key string    `query:"key" required:"true"`
}

type usageOutput struct {
	Body struct {
		Items []VariableUsage `json:"items"`
	}
}

// Register attaches the variable endpoints.
func Register(h huma.API, svc *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}
	errs422 := append(append([]int{}, errs...), http.StatusUnprocessableEntity)
	tags := []string{"variables"}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listVariables", Method: http.MethodGet, Path: "/services/{serviceId}/variables", Tags: tags,
		Summary: "List a service's variables",
		Description: "Secret values are never returned: they appear as `{key, isSecret:true, set:true}`. " +
			"`computedSecret` is also true for a variable that embeds a secret through a reference. " +
			"`platform` lists the reserved variables NebulaCtrl provides; `exports` the virtual variables other services can reference.",
		Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceIDInput) (*listOutput, error) {
		l, err := svc.List(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &listOutput{Body: l}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "putVariables", Method: http.MethodPut, Path: "/services/{serviceId}/variables", Tags: tags,
		Summary: "Upsert variables (staged until deployed)",
		Description: "Bulk upsert; variables not listed are untouched. Omitting a value keeps the stored one. " +
			"Values may reference other services (`${{service-slug.KEY}}`), the same service (`${{KEY}}`) and shared variables (`${{shared.KEY}}`), " +
			"and may contain generators (`${{secret(32)}}`, `${{randomInt(1,6)}}`, `${{uuid()}}`, `${{randomBytes(32,hex)}}`) that run once: " +
			"writing the same text again keeps the generated value. `$${{` writes a literal `${{`. " +
			"Variables whose resolved value changes in other services are restaged (`restaged`). " +
			"Platform variable names (`PORT`, `NEBULA_*`) are rejected with `reserved_variable`.",
		Errors: errs422,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *putInput) (*listOutput, error) {
		l, err := svc.Put(ctx, in.ID, in.Body.Variables)
		if err != nil {
			return nil, err
		}
		return &listOutput{Body: l}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteVariable", Method: http.MethodDelete, Path: "/services/{serviceId}/variables/{key}", Tags: tags,
		Summary: "Delete a variable (staged until deployed)", DefaultStatus: http.StatusNoContent, Errors: errs,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *deleteInput) (*struct{}, error) {
		return nil, svc.Delete(ctx, in.ID, in.Key)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "validateVariable", Method: http.MethodPost, Path: "/services/{serviceId}/variables/validate", Tags: tags,
		Summary:     "Validate a draft value",
		Description: "Parses the draft, checks generator arguments and dry-run resolves its references in the service's environment. Nothing is written and no generated value is returned.",
		Errors:      errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *validateInput) (*validateOutput, error) {
		r, err := svc.ValidateDraft(ctx, in.ID, in.Body)
		if err != nil {
			return nil, err
		}
		return &validateOutput{Body: r}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getVariableHistory", Method: http.MethodGet, Path: "/services/{serviceId}/variables/{key}/history", Tags: tags,
		Summary:     "Version history of a variable",
		Description: "Newest first. Non-secret versions include their value; secrets carry only a fingerprint (equal fingerprints mean equal values).",
		Errors:      errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *keyInput) (*historyOutput, error) {
		items, err := svc.History(ctx, in.ID, in.Key)
		if err != nil {
			return nil, err
		}
		out := &historyOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "restoreVariableVersion", Method: http.MethodPost, Path: "/services/{serviceId}/variables/{key}/history/{version}/restore", Tags: tags,
		Summary:     "Restore an old non-secret version as a new version",
		Description: "Secrets cannot be restored (`restore_unsupported`); use a release rollback or re-enter the value.",
		Errors:      errs422,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *restoreInput) (*listOutput, error) {
		l, err := svc.Restore(ctx, in.ID, in.Key, in.Version)
		if err != nil {
			return nil, err
		}
		return &listOutput{Body: l}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "rotateVariable", Method: http.MethodPost, Path: "/services/{serviceId}/variables/{key}/rotate", Tags: tags,
		Summary: "Rotate a generated variable (plan or stage)",
		Description: "Re-runs the generators of a generated variable. `dryRun:true` returns the plan (affected services, deploy order, warnings, blockers) and changes nothing; " +
			"`dryRun:false` stages the new value and restages the services that consume it. Nothing is deployed. " +
			"Database credentials and volume-backed passwords are blocked with `rotation_unsupported` once deployed.",
		Errors: errs422,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *rotateInput) (*rotateOutput, error) {
		p, err := svc.RotateVariable(ctx, in.ID, in.Key, in.Body.DryRun)
		if err != nil {
			return nil, err
		}
		return &rotateOutput{Body: p}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listSuggestedVariables", Method: http.MethodGet, Path: "/services/{serviceId}/suggested-variables", Tags: tags,
		Summary: "Suggested variables",
		Description: "References to databases of the environment that nothing references yet, and the keys of the repository's `.env.example`-style files " +
			"that are not set yet (read before the first deploy, see `POST /services/{serviceId}/env-template-scan`, and again after every push to the tracked branch and at every build). " +
			"Accepting a suggestion is a normal staged write; send `source: \"suggested\"` for an `.env.example` key so a value left empty is reported by the deploy plan.",
		Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceIDInput) (*suggestOutput, error) {
		r, err := svc.Suggestions(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &suggestOutput{Body: r}, nil
	})

	registerEnvTemplates(h, svc, errs422)
	registerShared(h, svc, errs, errs422)

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getVariableUsage", Method: http.MethodGet, Path: "/environments/{environmentId}/variable-usage", Tags: tags,
		Summary: "Which variables reference a variable", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *usageInput) (*usageOutput, error) {
		items, err := svc.VariableUsage(ctx, in.ID, in.NS, in.Key)
		if err != nil {
			return nil, err
		}
		out := &usageOutput{}
		out.Body.Items = items
		return out, nil
	})
}

func registerShared(h huma.API, svc *Service, errs, errs422 []int) {
	tags := []string{"shared-variables"}
	desc := "Shared variables reach a service only through an explicit `KEY=${{shared.KEY}}` variable (see share). " +
		"An environment value overrides the project value for that environment. Shared values may only reference other shared variables, team variables, external secrets, generators and transforms."

	huma.Register(h, api.WithScopes(huma.Operation{OperationID: "listProjectSharedVariables", Method: http.MethodGet, Path: "/projects/{projectId}/shared-variables", Tags: tags,
		Summary: "List a project's shared variables", Description: desc, Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *projectIDInput) (*sharedList, error) {
		l, err := svc.ListShared(ctx, LayerProject, in.ID)
		return &sharedList{Body: l}, err
	})
	huma.Register(h, api.WithScopes(huma.Operation{OperationID: "putProjectSharedVariables", Method: http.MethodPut, Path: "/projects/{projectId}/shared-variables", Tags: tags,
		Summary: "Upsert project shared variables (restages consumers in every environment)", Errors: errs422,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *projectSharedPut) (*sharedList, error) {
		l, err := svc.PutShared(ctx, LayerProject, in.ID, in.Body.Variables)
		return &sharedList{Body: l}, err
	})
	huma.Register(h, api.WithScopes(huma.Operation{OperationID: "deleteProjectSharedVariable", Method: http.MethodDelete, Path: "/projects/{projectId}/shared-variables/{key}", Tags: tags,
		Summary: "Delete a project shared variable", Errors: errs,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *projectSharedKey) (*sharedList, error) {
		l, err := svc.DeleteShared(ctx, LayerProject, in.ID, in.Key)
		return &sharedList{Body: l}, err
	})
	huma.Register(h, api.WithScopes(huma.Operation{OperationID: "rotateProjectSharedVariable", Method: http.MethodPost, Path: "/projects/{projectId}/shared-variables/{key}/rotate", Tags: tags,
		Summary: "Rotate a generated project shared variable (plan or stage)", Errors: errs422,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *projectSharedRotate) (*rotateOutput, error) {
		p, err := svc.RotateShared(ctx, LayerProject, in.ID, in.Key, in.Body.DryRun)
		return &rotateOutput{Body: p}, err
	})
	huma.Register(h, api.WithScopes(huma.Operation{OperationID: "shareProjectSharedVariable", Method: http.MethodPost, Path: "/projects/{projectId}/shared-variables/{key}/share", Tags: tags,
		Summary: "Stage KEY=${{shared.KEY}} on services", Errors: errs422,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *projectSharedShare) (*sharedList, error) {
		l, err := svc.ShareShared(ctx, LayerProject, in.ID, in.Key, in.Body)
		return &sharedList{Body: l}, err
	})
	huma.Register(h, api.WithScopes(huma.Operation{OperationID: "getProjectSharedVariableHistory", Method: http.MethodGet, Path: "/projects/{projectId}/shared-variables/{key}/history", Tags: tags,
		Summary: "Version history of a project shared variable", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *projectSharedKey) (*historyOutput, error) {
		items, err := svc.SharedHistory(ctx, LayerProject, in.ID, in.Key)
		out := &historyOutput{}
		out.Body.Items = items
		return out, err
	})

	huma.Register(h, api.WithScopes(huma.Operation{OperationID: "listEnvironmentSharedVariables", Method: http.MethodGet, Path: "/environments/{environmentId}/shared-variables", Tags: tags,
		Summary: "List an environment's shared variables (own values plus inherited project values)", Description: desc, Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *envIDInput) (*sharedList, error) {
		l, err := svc.ListShared(ctx, LayerEnvironment, in.ID)
		return &sharedList{Body: l}, err
	})
	huma.Register(h, api.WithScopes(huma.Operation{OperationID: "putEnvironmentSharedVariables", Method: http.MethodPut, Path: "/environments/{environmentId}/shared-variables", Tags: tags,
		Summary: "Upsert environment shared variables (restages consumers)", Errors: errs422,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *envSharedPut) (*sharedList, error) {
		l, err := svc.PutShared(ctx, LayerEnvironment, in.ID, in.Body.Variables)
		return &sharedList{Body: l}, err
	})
	huma.Register(h, api.WithScopes(huma.Operation{OperationID: "deleteEnvironmentSharedVariable", Method: http.MethodDelete, Path: "/environments/{environmentId}/shared-variables/{key}", Tags: tags,
		Summary: "Delete an environment shared variable (falls back to the project value)", Errors: errs,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *envSharedKey) (*sharedList, error) {
		l, err := svc.DeleteShared(ctx, LayerEnvironment, in.ID, in.Key)
		return &sharedList{Body: l}, err
	})
	huma.Register(h, api.WithScopes(huma.Operation{OperationID: "rotateEnvironmentSharedVariable", Method: http.MethodPost, Path: "/environments/{environmentId}/shared-variables/{key}/rotate", Tags: tags,
		Summary: "Rotate a generated environment shared variable (plan or stage)", Errors: errs422,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *envSharedRotate) (*rotateOutput, error) {
		p, err := svc.RotateShared(ctx, LayerEnvironment, in.ID, in.Key, in.Body.DryRun)
		return &rotateOutput{Body: p}, err
	})
	huma.Register(h, api.WithScopes(huma.Operation{OperationID: "shareEnvironmentSharedVariable", Method: http.MethodPost, Path: "/environments/{environmentId}/shared-variables/{key}/share", Tags: tags,
		Summary: "Stage KEY=${{shared.KEY}} on services", Errors: errs422,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *envSharedShare) (*sharedList, error) {
		l, err := svc.ShareShared(ctx, LayerEnvironment, in.ID, in.Key, in.Body)
		return &sharedList{Body: l}, err
	})
	huma.Register(h, api.WithScopes(huma.Operation{OperationID: "getEnvironmentSharedVariableHistory", Method: http.MethodGet, Path: "/environments/{environmentId}/shared-variables/{key}/history", Tags: tags,
		Summary: "Version history of an environment shared variable", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *envSharedKey) (*historyOutput, error) {
		items, err := svc.SharedHistory(ctx, LayerEnvironment, in.ID, in.Key)
		out := &historyOutput{}
		out.Body.Items = items
		return out, err
	})

	teamDesc := "Team variables apply to every project of the team and are referenced as `${{team.KEY}}`. " +
		"Like shared variables they reach a service only through an explicit `KEY=${{team.KEY}}` variable (see share). " +
		"Only owners and admins may change them; a principal restricted to some projects has no access. " +
		"Team values may only reference other team variables, external secrets (`${{vault:integration/path#key}}`), generators and transforms."
	huma.Register(h, api.WithScopes(huma.Operation{OperationID: "listTeamSharedVariables", Method: http.MethodGet, Path: "/team/shared-variables", Tags: tags,
		Summary: "List the team's shared variables", Description: teamDesc, Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, _ *struct{}) (*sharedList, error) {
		l, err := svc.ListShared(ctx, LayerTeam, uuid.Nil)
		return &sharedList{Body: l}, err
	})
	huma.Register(h, api.WithScopes(huma.Operation{OperationID: "putTeamSharedVariables", Method: http.MethodPut, Path: "/team/shared-variables", Tags: tags,
		Summary: "Upsert team shared variables (restages consumers in every project)", Errors: errs422,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *teamSharedPut) (*sharedList, error) {
		l, err := svc.PutShared(ctx, LayerTeam, uuid.Nil, in.Body.Variables)
		return &sharedList{Body: l}, err
	})
	huma.Register(h, api.WithScopes(huma.Operation{OperationID: "deleteTeamSharedVariable", Method: http.MethodDelete, Path: "/team/shared-variables/{key}", Tags: tags,
		Summary: "Delete a team shared variable", Errors: errs,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *teamSharedKey) (*sharedList, error) {
		l, err := svc.DeleteShared(ctx, LayerTeam, uuid.Nil, in.Key)
		return &sharedList{Body: l}, err
	})
	huma.Register(h, api.WithScopes(huma.Operation{OperationID: "rotateTeamSharedVariable", Method: http.MethodPost, Path: "/team/shared-variables/{key}/rotate", Tags: tags,
		Summary: "Rotate a generated team shared variable (plan or stage)", Errors: errs422,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *teamSharedRotate) (*rotateOutput, error) {
		p, err := svc.RotateShared(ctx, LayerTeam, uuid.Nil, in.Key, in.Body.DryRun)
		return &rotateOutput{Body: p}, err
	})
	huma.Register(h, api.WithScopes(huma.Operation{OperationID: "shareTeamSharedVariable", Method: http.MethodPost, Path: "/team/shared-variables/{key}/share", Tags: tags,
		Summary: "Stage KEY=${{team.KEY}} on services", Errors: errs422,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *teamSharedShare) (*sharedList, error) {
		l, err := svc.ShareShared(ctx, LayerTeam, uuid.Nil, in.Key, in.Body)
		return &sharedList{Body: l}, err
	})
	huma.Register(h, api.WithScopes(huma.Operation{OperationID: "getTeamSharedVariableHistory", Method: http.MethodGet, Path: "/team/shared-variables/{key}/history", Tags: tags,
		Summary: "Version history of a team shared variable", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *teamSharedKey) (*historyOutput, error) {
		items, err := svc.SharedHistory(ctx, LayerTeam, uuid.Nil, in.Key)
		out := &historyOutput{}
		out.Body.Items = items
		return out, err
	})
}
