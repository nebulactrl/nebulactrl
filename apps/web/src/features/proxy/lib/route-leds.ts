// A route service has no build and no container: its deploy publishes a route
// table (Route) and, for http routes, checks it answers (Verify). The canvas
// card shows those as its LEDs instead of Build / Release / Verify.

import type { PhaseState, PhaseStates } from "#/features/canvas/lib/status";

export interface RouteLed {
	label: string;
	state: PhaseState;
}

/**
 * The LEDs of a route card from the three-phase states of `cardStatus`.
 * Queued and preparing (the build phase of a regular deploy) count as Route.
 * TCP/UDP routes have nothing to verify, so they get one LED.
 */
export function routeLeds(
	phases: PhaseStates,
	protocol: "http" | "tcp" | "udp",
): RouteLed[] {
	const [first, second, third] = phases;
	const route: PhaseState =
		first === "live" || first === "fail" ? first : second;
	if (protocol !== "http") {
		const state: PhaseState = phases.includes("fail")
			? "fail"
			: phases.includes("live")
				? "live"
				: third;
		return [{ label: "Route", state }];
	}
	return [
		{ label: "Route", state: route },
		{ label: "Verify", state: third },
	];
}
