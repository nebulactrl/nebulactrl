// Cloudflare Tunnel card logic: the status chip, why a tunnel cannot be enabled
// and what each state means for the operator.

import type { TunnelView } from "#/api/gen/types.gen";
import type { StatusKind } from "#/components/ui/status-icon";

export type TunnelState = TunnelView["state"];

export interface TunnelChip {
	/** The status icon: a spinner while the connector is on its way. */
	kind: StatusKind;
	label: string;
}

/** The observed connector state as a status icon and one word. */
export function tunnelChip(state: TunnelState): TunnelChip {
	switch (state) {
		case "online":
			return { kind: "live", label: "Connected" };
		case "provisioning":
			return { kind: "deploying", label: "Connecting" };
		case "needs_attention":
			return { kind: "failed", label: "Needs attention" };
		case "offline":
			return { kind: "degraded", label: "Offline" };
		case "removing":
			return { kind: "deploying", label: "Removing" };
		default:
			return { kind: "stopped", label: "Off" };
	}
}

/** The connection count in words: "4 connections to Cloudflare", "1 connection". */
export function connectionsText(connections: number): string {
	if (connections <= 0) return "No connection to Cloudflare yet";
	return `${connections} ${connections === 1 ? "connection" : "connections"} to Cloudflare`;
}

/**
 * Why a tunnel cannot be enabled right now, or null. The Cloudflare connection
 * and its account come first (the tunnel lives in the account), then the agent.
 */
export function enableBlocker(tunnel: TunnelView): string | null {
	if (!tunnel.cloudflareConnected) {
		return "Cloudflare is not connected.";
	}
	if (!tunnel.accountReady) {
		return "The Cloudflare connection has no account selected; tunnels live in an account.";
	}
	if (!tunnel.agentSupported) {
		return "This server's agent is too old to run the Cloudflare connector. Update the agent first.";
	}
	return null;
}

/** True while the connector's state is still moving: the card polls then. */
export function tunnelBusy(tunnel: TunnelView | undefined): boolean {
	return tunnel?.state === "provisioning" || tunnel?.state === "removing";
}

/**
 * What the operator can rely on in each state. The ports note is the point of
 * a tunnel: nothing inbound is needed once the connector is connected.
 */
export function tunnelNote(tunnel: TunnelView): string | null {
	switch (tunnel.state) {
		case "online":
			return tunnel.active
				? "Domains in your connected Cloudflare zones point at the tunnel. Ports 80 and 443 may stay closed."
				: "Connected. Domains switch to the tunnel in a moment.";
		case "provisioning":
			return "The connector is starting. Domains keep their current DNS until it has connected.";
		case "offline":
			return "The connector is not running. Domains already on the tunnel are unreachable until it is back; NebulaCtrl restarts it while the server is online.";
		case "needs_attention":
			return "The connector could not start or keeps restarting. Re-sync to try again.";
		case "removing":
			return "Domains are being pointed back at the server. The tunnel is deleted at Cloudflare afterwards.";
		default:
			return null;
	}
}

/** "1 hostname", "3 hostnames". */
export function hostnamesText(count: number): string {
	return `${count} ${count === 1 ? "hostname" : "hostnames"}`;
}
