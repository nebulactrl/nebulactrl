import { errorCode } from "#/api/client-config";

// Friendly copy for the 422 codes a tailnet change can come back with, from
// the settings PATCH as well as from plan and deploy.

const COPY: Record<string, string> = {
	tailnet_not_configured:
		"Connect Tailscale or Headscale in Team settings → Integrations first.",
	tailnet_tag_not_allowed:
		"One of the extra tags is not allowed by the integration. Pick tags from the allowed list, or allow the tag in Team settings → Integrations.",
	tailnet_https_disabled:
		"HTTPS certificates are turned off for your tailnet. Enable HTTPS in the Tailscale admin console, or turn the web exposure off.",
	tailnet_funnel_unsupported:
		"Funnel needs Tailscale. Headscale cannot publish a service on the internet.",
	tailnet_tls_unsupported:
		"TLS on a TCP port needs Tailscale with HTTPS certificates. Headscale cannot terminate TLS.",
	agent_too_old:
		"This server's agent is too old for tailnet services. Re-run the installer from this control plane to upgrade it.",
};

/** The friendly sentence for a tailnet error code, or undefined for any other. */
export function tailnetErrorCopy(code: string | undefined): string | undefined {
	return code === undefined ? undefined : COPY[code];
}

/** Like `tailnetErrorCopy`, from a thrown error. */
export function tailnetErrorMessage(error: unknown): string | undefined {
	return tailnetErrorCopy(errorCode(error));
}
