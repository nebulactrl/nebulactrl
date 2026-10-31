package mesh

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type viewOutput struct{ Body View }

type settingsInput struct {
	Body struct {
		Enabled  *bool   `json:"enabled,omitempty" doc:"Turning the mesh off is refused (409 mesh_in_use) while servers still have nodes"`
		AutoJoin *bool   `json:"autoJoin,omitempty" doc:"Online servers with a current agent join the mesh automatically (each adds one device)"`
		ACLMode  *string `json:"aclMode,omitempty" enum:"managed,advisory,off" doc:"managed: NebulaCtrl writes the policy (Headscale with an API key; Tailscale only as an opt-in that needs the OAuth scope policy_file and replaces only NebulaCtrl's own rules). advisory: the rules are shown, never written. off: no rules. Needs the admin scope."`
		MeshTag  *string `json:"meshTag,omitempty" doc:"The tag every mesh node carries (default tag:nebula-mesh); it must be in the integration's allowed tags. Needs the admin scope; fixed while nodes exist."`
	}
}

type serverPathInput struct {
	ID uuid.UUID `path:"serverId"`
}

type serverMeshOutput struct{ Body ServerMeshView }

type setServerMeshInput struct {
	ID     uuid.UUID `path:"serverId"`
	DryRun bool      `query:"dryRun" doc:"Return the plan (devices added, blockers, warnings) without changing anything"`
	Body   struct {
		Enabled           bool `json:"enabled"`
		AcceptMeshChanges bool `json:"acceptMeshChanges,omitempty" doc:"Confirms that enabling adds tailnet devices (tagged devices may be billed by Tailscale)"`
	}
}

type setServerMeshOutput struct {
	Status int
	Body   ServerMeshResult
}

type acceptedOutput struct {
	Body struct {
		Accepted bool `json:"accepted"`
	}
}

type linkPathInput struct {
	ID uuid.UUID `path:"linkId"`
}

type checkServerInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		PeerServerID *uuid.UUID `json:"peerServerId,omitempty" doc:"Check only this peer (default: every other server with a mesh node)"`
	}
}

type checkServerOutput struct {
	Body struct {
		Results []ServerCheckResult `json:"results"`
	}
}

type previewOutput struct{ Body ACLPreview }

type aclCheckOutput struct{ Body ACLCheck }

// Register attaches the mesh endpoints.
func Register(h huma.API, svc *Service) {
	tags := []string{"mesh"}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getMesh", Method: http.MethodGet, Path: "/mesh", Tags: tags,
		Summary:     "Server mesh of the team",
		Description: "The managed tailnet mesh: coordinator health, the ACL mode and state, the server nodes, the link nodes, the access rules NebulaCtrl derives (read-only) and the tagged device count. Servers join the mesh one by one; replicas and cross-server service links use it.",
		Errors:      errs[:3],
	}, policy.ScopeServersRead), func(ctx context.Context, _ *struct{}) (*viewOutput, error) {
		v, err := svc.View(ctx)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateMeshSettings", Method: http.MethodPut, Path: "/mesh/settings", Tags: tags,
		Summary:     "Change the mesh settings",
		Description: "Auto-join, the mesh tag and the ACL mode. Switching Tailscale to `managed` is an opt-in: it needs the OAuth client's `policy_file` scope, refuses when another team uses the same tailnet (409 `mesh_acl_shared_tailnet`) and answers 422 `mesh_policy_scope_missing` when the scope is missing. The token can then rewrite the whole tailnet policy; NebulaCtrl itself replaces only its own rules and keeps comments. Changing the ACL mode or the tag needs the admin scope.",
		Errors:      errs,
	}, policy.ScopeServersWrite), func(ctx context.Context, in *settingsInput) (*viewOutput, error) {
		v, err := svc.UpdateSettings(ctx, SettingsInput{Enabled: in.Body.Enabled, AutoJoin: in.Body.AutoJoin, ACLMode: in.Body.ACLMode, MeshTag: in.Body.MeshTag})
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "previewMeshACL", Method: http.MethodPost, Path: "/mesh/acl/preview", Tags: tags,
		Summary:     "Preview the generated access rules",
		Description: "The rules derived from the mesh tables, the policy document (managed Headscale) or the snippet to paste (advisory), and, when the stored policy can be read, what would be added or removed. Nothing is written.",
		Errors:      errs[:3],
	}, policy.ScopeServersRead), func(ctx context.Context, _ *struct{}) (*previewOutput, error) {
		p, err := svc.PreviewACL(ctx)
		if err != nil {
			return nil, err
		}
		return &previewOutput{Body: p}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "applyMeshACL", Method: http.MethodPost, Path: "/mesh/acl/apply", Tags: tags,
		Summary:     "Apply the access rules now",
		Description: "Regenerates and applies the policy according to the ACL mode (managed writes it, advisory only verifies it). It normally runs by itself a few seconds after every change.",
		Errors:      errs,
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*viewOutput, error) {
		v, err := svc.ApplyACLNow(ctx)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "checkMeshACL", Method: http.MethodPost, Path: "/mesh/acl/check", Tags: tags,
		Summary:     "Compare the generated rules with the stored policy",
		Description: "Read-only drift check: which generated rules are missing from the tailnet policy and which outdated NebulaCtrl rules it still holds. It cannot prove that no broader rule exists; the connectivity test is the ground truth.",
		Errors:      errs[:3],
	}, policy.ScopeServersRead), func(ctx context.Context, _ *struct{}) (*aclCheckOutput, error) {
		c, err := svc.CheckACL(ctx)
		if err != nil {
			return nil, err
		}
		return &aclCheckOutput{Body: c}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServerMesh", Method: http.MethodGet, Path: "/servers/{serverId}/mesh", Tags: []string{"servers", "mesh"},
		Summary:     "Mesh node of a server",
		Description: "Whether the server joined the mesh, its node (state, tailnet addresses, peers, exposed services) and the link nodes that run on it.",
		Errors:      errs[:3],
	}, policy.ScopeServersRead), func(ctx context.Context, in *serverPathInput) (*serverMeshOutput, error) {
		v, err := svc.GetServerMesh(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &serverMeshOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "setServerMesh", Method: http.MethodPut, Path: "/servers/{serverId}/mesh", Tags: []string{"servers", "mesh"},
		Summary:       "Turn the mesh on or off for a server",
		DefaultStatus: http.StatusAccepted,
		Description:   "Enabling adds one tagged device to the team's tailnet (it may be billed by Tailscale) and needs `acceptMeshChanges`; with `?dryRun=true` it returns the plan instead (devices added, whether the host has /dev/net/tun, warnings and blockers). Disabling is refused (409 `mesh_server_in_use`) while replicas or cross-server links use the server's node. The node key is minted when the command is sent and never stored.",
		Errors:        errs,
	}), policy.ScopeServersWrite), func(ctx context.Context, in *setServerMeshInput) (*setServerMeshOutput, error) {
		r, err := svc.SetServerMesh(ctx, in.ID, ServerMeshInput{Enabled: in.Body.Enabled, DryRun: in.DryRun, AcceptMeshChanges: in.Body.AcceptMeshChanges})
		if err != nil {
			return nil, err
		}
		status := http.StatusAccepted
		if in.DryRun {
			status = http.StatusOK
		}
		return &setServerMeshOutput{Status: status, Body: r}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "reprovisionServerMesh", Method: http.MethodPost, Path: "/servers/{serverId}/mesh/reprovision", Tags: []string{"servers", "mesh"},
		Summary:       "Re-provision the mesh node of a server",
		DefaultStatus: http.StatusAccepted,
		Description:   "Deletes the node's device from the tailnet and re-creates it with a fresh single-use key and a fresh state volume. The node gets a new tailnet IP and the access rules regenerate. Use it when the node needs attention.",
		Errors:        errs,
	}), policy.ScopeServersWrite), func(ctx context.Context, in *serverPathInput) (*acceptedOutput, error) {
		if err := svc.ReprovisionServer(ctx, in.ID); err != nil {
			return nil, err
		}
		out := &acceptedOutput{}
		out.Body.Accepted = true
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "checkServerMesh", Method: http.MethodPost, Path: "/servers/{serverId}/mesh/check", Tags: []string{"servers", "mesh"},
		Summary:     "Test the mesh connectivity of a server",
		Description: "The agent checks whether the server's node reaches the other servers' nodes (connected, refused, timeout, no_path). This is the ground truth for advisory ACL modes.",
		Errors:      errs,
	}, policy.ScopeServersWrite), func(ctx context.Context, in *checkServerInput) (*checkServerOutput, error) {
		res, err := svc.CheckServer(ctx, in.ID, in.Body.PeerServerID)
		if err != nil {
			return nil, err
		}
		out := &checkServerOutput{}
		out.Body.Results = res
		return out, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "reprovisionMeshLink", Method: http.MethodPost, Path: "/mesh/links/{linkId}/reprovision", Tags: tags,
		Summary:       "Re-provision a link node",
		DefaultStatus: http.StatusAccepted,
		Description:   "Re-creates a link node with a fresh single-use key (a new device and tailnet IP; the access rules regenerate).",
		Errors:        errs,
	}), policy.ScopeServersWrite), func(ctx context.Context, in *linkPathInput) (*acceptedOutput, error) {
		if err := svc.ReprovisionLink(ctx, in.ID); err != nil {
			return nil, err
		}
		out := &acceptedOutput{}
		out.Body.Accepted = true
		return out, nil
	})
}
