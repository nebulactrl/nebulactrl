// Tailnet card logic: the status chip, the mode choices and when each is allowed.

import type { ProxyTailnet } from "#/api/gen/types.gen";
import type { LampTone } from "#/components/ui/lamp";

export type TailnetMode = ProxyTailnet["mode"];

export const TAILNET_MODES: readonly {
	id: TailnetMode;
	label: string;
	hint: string;
}[] = [
	{
		id: "off",
		label: "Off",
		hint: "Routes cannot reach tailnet addresses.",
	},
	{
		id: "managed",
		label: "Managed",
		hint: "NebulaCtrl runs a Tailscale node next to the proxy and registers it with your OAuth client.",
	},
	{
		id: "host",
		label: "Host Tailscale",
		hint: "Use the Tailscale already running on the server; routes may target its 100.x addresses.",
	},
];

export const MAGIC_DNS_CAVEAT =
	"MagicDNS lets routes use tailnet names such as nas.tail1234.ts.net. It points the proxy container's DNS at Tailscale (100.100.100.100), so a name that only your normal DNS knows may stop resolving there.";

export interface TailnetChip {
	tone: LampTone;
	label: string;
}

/** The observed node state as a lamp and one word. */
export function tailnetChip(state: string, mode: TailnetMode): TailnetChip {
	if (mode === "off") return { tone: "off", label: "Off" };
	switch (state) {
		case "online":
			return { tone: "live", label: "Online" };
		case "provisioning":
			return { tone: "deploying", label: "Provisioning" };
		case "needs_attention":
			return { tone: "failed", label: "Needs attention" };
		case "offline":
			return { tone: "degraded", label: "Offline" };
		default:
			return {
				tone: "off",
				label: mode === "host" ? "Host mode" : "Not reported",
			};
	}
}

/**
 * Why a mode cannot be chosen right now, or null. Managed needs the workspace
 * integration; host needs Tailscale detected on the server.
 */
export function modeBlocker(
	mode: TailnetMode,
	tailnet: Pick<ProxyTailnet, "integrationConfigured" | "hostTailscale">,
): string | null {
	if (mode === "managed" && !tailnet.integrationConfigured) {
		return "Connect Tailscale in Team settings → Integrations first.";
	}
	if (mode === "host" && !tailnet.hostTailscale.detected) {
		return "Tailscale was not detected on this server.";
	}
	return null;
}

/** The tailnet device picker only helps when a tailnet is reachable from the server. */
export function canPickTailnetDevice(input: {
	integrationConfigured: boolean;
	mode: TailnetMode;
}): boolean {
	return input.integrationConfigured && input.mode !== "off";
}

/** Machine name rules of the managed node (a DNS label). */
export function validateTailnetHostname(name: string): string | null {
	if (name === "") return null;
	return /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(name)
		? null
		: "Use lower-case letters, digits and dashes (max 63).";
}
