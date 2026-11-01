package apierr

// Mesh error codes. They are shared by the mesh, mesh-links and
// replica domains so one condition always has one code and one message.
const (
	CodeMeshNotConfigured        = "mesh_not_configured"
	CodeMeshAgentTooOld          = "mesh_agent_too_old"
	CodeMeshNoTun                = "mesh_no_tun"
	CodeMeshCoordinatorDown      = "mesh_coordinator_unreachable"
	CodeMeshPaused               = "mesh_paused"
	CodeMeshServerRequired       = "mesh_server_required"
	CodeMeshNotExposable         = "mesh_not_exposable"
	CodeMeshRoutePending         = "mesh_route_pending"
	CodeMeshLinkNotReady         = "mesh_link_not_ready"
	CodeMeshConfirmationRequired = "mesh_confirmation_required"
	CodeMeshPolicyScopeMissing   = "mesh_policy_scope_missing"
	CodeMeshACLSharedTailnet     = "mesh_acl_shared_tailnet"
	CodeMeshACLModeUnsupported   = "mesh_acl_mode_unsupported"
	CodeMeshEnvironmentMismatch  = "mesh_environment_mismatch"
	CodeMeshPurposeMismatch      = "mesh_purpose_mismatch"
	CodeIntegrationInUse         = "integration_in_use"
)

// MeshNotConfigured is a 422: no Tailscale or Headscale integration exists.
func MeshNotConfigured() *Error {
	return Unprocessable(CodeMeshNotConfigured, "connect Tailscale or Headscale in Team settings → Integrations before using the mesh")
}

// MeshAgentTooOld is a 422: the server's agent does not manage mesh nodes.
func MeshAgentTooOld(server string) *Error {
	return Unprocessable(CodeMeshAgentTooOld, "the agent on "+server+" is too old to manage mesh nodes; update the agent first")
}

// MeshNoTun is a 422: link nodes need /dev/net/tun on their host.
func MeshNoTun(server string) *Error {
	return Unprocessable(CodeMeshNoTun, server+" has no /dev/net/tun, which a mesh link node needs; load the tun kernel module on the host (same-server replicas do not need it)")
}

// MeshCoordinatorDown is a 422: the tailnet coordinator cannot be reached.
func MeshCoordinatorDown() *Error {
	return Unprocessable(CodeMeshCoordinatorDown, "the mesh coordinator is unreachable: existing connections keep working, but joining and ACL changes wait")
}

// MeshPaused is a 409: mesh changes wait while the mesh is paused.
func MeshPaused(reason string) *Error {
	msg := "the mesh is paused"
	if reason != "" {
		msg += ": " + reason
	}
	return Conflict(CodeMeshPaused, msg+". Established connections keep working; changes resume afterwards")
}

// MeshServerRequired is a 422: mesh is not enabled on a server the action needs.
func MeshServerRequired(server string) *Error {
	return Unprocessable(CodeMeshServerRequired, "enable the mesh on "+server+" first")
}

// MeshNotExposable is a 422: the service cannot be exposed over the mesh.
func MeshNotExposable(why string) *Error {
	return Unprocessable(CodeMeshNotExposable, "the service cannot be exposed over the mesh: "+why)
}

// MeshConfirmationRequired is a 409: the action adds tailnet devices and the
// caller did not accept that.
func MeshConfirmationRequired(devices int) *Error {
	return Conflict(CodeMeshConfirmationRequired, "this adds tailnet devices (tagged devices may be billed by Tailscale); repeat the request with acceptMeshChanges=true to confirm").
		WithDetails(map[string]any{"devicesAdded": devices})
}

// IntegrationInUse is a 409: the tailnet integration is used by the mesh.
func IntegrationInUse(msg string) *Error { return Conflict(CodeIntegrationInUse, msg) }
