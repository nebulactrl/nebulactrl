package terminal

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type serverIDInput struct {
	ID uuid.UUID `path:"serverId"`
}

type hostShellInfoOutput struct{ Body HostShellInfoView }

type hostShellSettingsOutput struct{ Body HostShellSettingsView }

type putHostShellInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body HostShellSettingsView
}

// registerHostShell attaches the host shell's JSON endpoints. The WebSocket is
// a raw route (MountAPI), documented here in prose.
func registerHostShell(h huma.API, svc *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}
	tag := []string{"terminal"}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServerHostShell", Method: http.MethodGet, Path: "/servers/{serverId}/host-shell", Tags: tag,
		Summary: "Host shell availability of a server",
		Description: "Whether the caller can open a shell on the server itself, and why not: the setting is off (the default), the caller's account has no " +
			"two-factor authentication, the server is offline, or it is the server the control plane runs on and the caller is not the instance admin (`restricted`). " +
			"Owners and Admins with a signed-in session only; a token gets 403 `session_required`.\n\n" +
			"**Shell.** `GET /servers/{serverId}/shell/ws` upgrades to a WebSocket with the protocol of the service terminal (see `getServiceTerminal`): the first " +
			"message is JSON `{\"type\":\"open\",\"cols\":80,\"rows\":24,\"confirmProduction\":false}` (no command: it is always the host's login shell). It answers " +
			"403 `host_shell_disabled` when the setting is off, 403 `two_factor_required` when the account has no 2FA and 403 `instance_admin_required` on the control-plane server for anyone but the instance admin, before the upgrade. A server that runs any " +
			"service of a production environment needs `confirmProduction: true`. A person may hold 2 host shells; an idle session (no input for 15 minutes) is " +
			"closed. Sessions are audited (`server.shell.open`, `server.shell.close`: who, server, duration); their I/O is never recorded.",
		Errors: errs,
	}, policy.ScopeServersRead), func(ctx context.Context, in *serverIDInput) (*hostShellInfoOutput, error) {
		v, err := svc.HostShellInfo(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &hostShellInfoOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "setServerHostShell", Method: http.MethodPut, Path: "/servers/{serverId}/host-shell", Tags: tag,
		Summary: "Switch a server's host shell on or off",
		Description: "Owners and Admins only (signed-in session); on the server the control plane runs on, the instance admin only (403 `instance_admin_required`). Off by default. The host shell is a root shell on the server: switching it on is audited " +
			"(`server.shell.enable`, `server.shell.disable`), and switching it off ends the sessions that are open.",
		Errors: errs,
	}, policy.ScopeServersWrite), func(ctx context.Context, in *putHostShellInput) (*hostShellSettingsOutput, error) {
		v, err := svc.SetHostShell(ctx, in.ID, in.Body)
		if err != nil {
			return nil, err
		}
		return &hostShellSettingsOutput{Body: v}, nil
	})
}
