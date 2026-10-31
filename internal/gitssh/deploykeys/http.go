package deploykeys

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type serviceInput struct {
	ID uuid.UUID `path:"serviceId"`
}

type statusOutput struct{ Body StatusView }

type setKeyInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		PrivateKey string `json:"privateKey,omitempty" maxLength:"16384" doc:"Write-only. An unencrypted OpenSSH or PEM private key to use instead of a generated one; omit to generate an Ed25519 key"`
		Rotate     bool   `json:"rotate,omitempty" doc:"Replace the existing key. Without it an existing key is a 409 ssh_key_exists"`
	}
}

type acceptInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		Fingerprint string `json:"fingerprint" minLength:"1" doc:"The SHA256 fingerprint of the host key you reviewed; the key is pinned only if the server still presents it"`
	}
}

type testOutput struct{ Body TestResult }

// Register attaches the deploy-key endpoints.
func Register(h huma.API, svc *Service) {
	tags := []string{"git"}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServiceSshKey", Method: http.MethodGet, Path: "/services/{serviceId}/ssh", Tags: tags,
		Summary: "Get a git service's SSH deploy key and pinned host keys",
		Description: "The public key and fingerprint, the provider's \"add deploy key\" link when the host is a known public instance, " +
			"and each pinned host key (with the refused key when the host's key changed). The private key is never returned. 422 `not_git_source`.",
		Errors: append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceInput) (*statusOutput, error) {
		v, err := svc.Status(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &statusOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "setServiceSshKey", Method: http.MethodPost, Path: "/services/{serviceId}/ssh/key", Tags: tags,
		Summary: "Generate, upload or rotate the SSH deploy key",
		Description: "Without `privateKey` an Ed25519 key pair is generated; the public key goes on the repository as a deploy key. " +
			"With `privateKey` your own unencrypted key is stored (write-only). An existing key is replaced only with `rotate: true`; otherwise 409 `ssh_key_exists`. " +
			"Needs `projects:write`. Takes effect on the next deployment; nothing is staged.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *setKeyInput) (*statusOutput, error) {
		v, err := svc.SetKey(ctx, in.ID, SetKeyInput{PrivateKey: in.Body.PrivateKey, Rotate: in.Body.Rotate})
		if err != nil {
			return nil, err
		}
		return &statusOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteServiceSshKey", Method: http.MethodDelete, Path: "/services/{serviceId}/ssh/key", Tags: tags,
		Summary: "Remove the SSH deploy key and the pinned host keys", DefaultStatus: http.StatusNoContent,
		Description: "Needs `projects:write`. A service whose git URL is an SSH URL cannot be deployed until a key is set again.",
		Errors:      append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *serviceInput) (*struct{}, error) {
		return nil, svc.DeleteKey(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "acceptServiceSshHostKey", Method: http.MethodPost, Path: "/services/{serviceId}/ssh/host-keys/accept", Tags: tags,
		Summary: "Accept the SSH host key the git host presents now",
		Description: "Replaces the pinned host key of the service's git host after you reviewed the fingerprint of the refused key. " +
			"409 `host_key_changed` when the server presents yet another key, 502 `host_unreachable` when it cannot be reached. Needs `projects:write`.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusBadGateway),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *acceptInput) (*statusOutput, error) {
		v, err := svc.AcceptHostKey(ctx, in.ID, in.Body.Fingerprint)
		if err != nil {
			return nil, err
		}
		return &statusOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "testServiceSshConnection", Method: http.MethodPost, Path: "/services/{serviceId}/ssh/test", Tags: tags,
		Summary: "Test the SSH connection to the repository",
		Description: "Fetches the configured branch over SSH with the deploy key. The first connection pins the host key; a changed key is refused and recorded. " +
			"Always 200: `ok` and `code` (`no_key`, `not_ssh_url`, `host_key_changed`, `git_unreachable`) say what happened. Needs `projects:write`.",
		Errors: append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *serviceInput) (*testOutput, error) {
		v, err := svc.Test(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &testOutput{Body: v}, nil
	})
}
