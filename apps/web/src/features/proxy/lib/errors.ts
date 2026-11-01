// Backend error codes of the proxy slice, mapped to where the form shows them.
// An unmapped code falls through to the form's general error line.

import { errorCode, errorMessage, errorStatus } from "#/api/client-config";
import { type OptionGroup, optionsErrorTarget } from "./options";

/** Field keys the forms use for inline errors. */
export type ProxyField =
	| "hostname"
	| "pathPrefix"
	| "addPrefix"
	| "stripPrefix"
	| "redirectTo"
	| "redirectCode"
	| "tlsMode"
	| "certificateId"
	| "basicAuth"
	| "upstream"
	| "listenPort"
	| "allowCidrs"
	| "openToInternet"
	| "hostTailscale"
	| "tailnet"
	| "options";

const CODE_FIELD: Record<string, ProxyField> = {
	invalid_path_prefix: "pathPrefix",
	invalid_redirect: "redirectTo",
	redirect_loop: "redirectTo",
	invalid_kind: "redirectTo",
	invalid_tls_mode: "tlsMode",
	invalid_certificate: "certificateId",
	certificate_mismatch: "certificateId",
	basic_auth_needs_users: "basicAuth",
	invalid_options: "options",
	upstream_denied: "upstream",
	upstream_not_deployed: "upstream",
	port_in_use: "listenPort",
	invalid_listen_port: "listenPort",
	tailnet_not_configured: "tailnet",
	host_tailscale_not_detected: "hostTailscale",
};

/** The field an API error belongs to, or undefined for the general line. */
export function errorField(error: unknown): ProxyField | undefined {
	const code = errorCode(error);
	return code === undefined ? undefined : CODE_FIELD[code];
}

/** An error as an inline message when it points at `field`, else undefined. */
export function fieldError(
	error: unknown,
	field: ProxyField,
): string | undefined {
	return errorField(error) === field ? errorMessage(error) : undefined;
}

/** The server's agent predates proxy v2: staging answers 422 `agent_too_old`. */
export function isAgentTooOld(error: unknown): boolean {
	return errorCode(error) === "agent_too_old";
}

/** A 412 from `If-Match`: someone else changed the service. */
export function isGenerationConflict(error: unknown): boolean {
	return (
		errorCode(error) === "generation_conflict" || errorStatus(error) === 412
	);
}

/** The command that upgrades an agent, from the control plane's own origin. */
export function agentUpgradeCommand(origin: string): string {
	return `curl -fsSL ${origin}/install-agent.sh | sudo sh -s -- --url ${origin}`;
}

export interface GroupServerErrors {
	/** Inline errors keyed like the form's own (`allowCidrs.1`, `redirects.0.regex`, `certificate`). */
	fields: Record<string, string>;
	/** What is left over: shown under the group's footer. */
	general: string | undefined;
}

/**
 * The control plane's 422 for a group save, as inline field errors when the
 * code or message says where, else as one general line. `agent_too_old` is
 * neither: it has its own banner.
 */
export function groupServerErrors(
	error: unknown,
	group: OptionGroup,
): GroupServerErrors {
	const none: GroupServerErrors = { fields: {}, general: undefined };
	if (error === undefined || error === null || isAgentTooOld(error))
		return none;
	if (isGenerationConflict(error)) return none;
	const message = errorMessage(error);
	switch (errorCode(error)) {
		case "basic_auth_needs_users":
			return group === "access"
				? { fields: { basicAuth: message }, general: undefined }
				: none;
		case "certificate_mismatch":
		case "invalid_certificate":
		case "invalid_tls_mode":
			return group === "tls"
				? { fields: { certificate: message }, general: undefined }
				: none;
		case "invalid_options": {
			const target = optionsErrorTarget(message);
			if (target.group !== group) return none;
			return target.path === ""
				? { fields: {}, general: message }
				: { fields: { [target.path]: message }, general: undefined };
		}
		default:
			return { fields: {}, general: message };
	}
}
