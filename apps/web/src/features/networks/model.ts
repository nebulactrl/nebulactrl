import type { NetworkRef } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";

/**
 * Why a service cannot join shared networks right now, or null when it can.
 * The control plane enforces the same rules (422 `service_no_server`,
 * `egress_blocked`, `network_wrong_server`); this keeps the UI from offering
 * what would be refused.
 */
export function attachBlockedReason(
	service: Pick<Service, "serverId" | "kind" | "config">,
): string | null {
	if (service.kind === "route") {
		return "A route has no container, so it has no networks.";
	}
	if (service.config.blockEgress === true) {
		return "A service that blocks outbound internet cannot join shared networks. Allow outbound internet first.";
	}
	if (!service.serverId) {
		return "Place the service on a server first: a shared network lives on one server.";
	}
	return null;
}

/** The label of the chip on a canvas card: the name for one network, a count for several. */
export function networkChipLabel(networks: readonly NetworkRef[]): string {
	const [first] = networks;
	if (!first) return "";
	return networks.length === 1 ? first.name : `${networks.length} networks`;
}

/** The chip's tooltip: every network with the hostname the service has on it. */
export function networkChipTitle(networks: readonly NetworkRef[]): string {
	return networks
		.map((network) => `${network.name}: ${network.alias}`)
		.join("\n");
}

/** The caption at the canvas floor. Sentence case, the environment name as it is written. */
export function privateNetworkCaption(environmentName: string): string {
	return `Private network · ${environmentName}`;
}
