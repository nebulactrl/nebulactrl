// What "Deploy proxy" tells the operator before it runs: the staged changes,
// why the proxy restarts, and the one change that deletes something outside
// NebulaCtrl (the managed Tailscale device).

import type { ProxyPlan } from "#/api/gen/types.gen";

export interface PlanHighlights {
	/** `field: from → to`, one per staged change. */
	changes: string[];
	/** Why the proxy container restarts (empty when it does not). */
	restart: string[];
	/** Cautions: policy, access, and the tailnet device deletion. */
	warnings: string[];
	/** The managed tailnet node is deleted from the tailnet. */
	deletesTailnetDevice: boolean;
}

export const TAILNET_DELETE_WARNING =
	"The managed Tailscale device is deleted from your tailnet. Routes that use tailnet addresses stop until a new node is registered.";

export function planHighlights(plan: ProxyPlan): PlanHighlights {
	const diff = plan.diff ?? [];
	const deletesTailnetDevice = diff.some(
		(change) =>
			(change.field === "tailnet.mode" &&
				change.from === "managed" &&
				change.to !== "managed") ||
			change.field === "tailnet.provision",
	);
	const warnings = [...(plan.warnings ?? [])];
	if (
		deletesTailnetDevice &&
		!warnings.some(
			(warning) => /tailnet|tailscale/i.test(warning) && /delet/i.test(warning),
		)
	) {
		warnings.push(TAILNET_DELETE_WARNING);
	}
	return {
		changes: diff.map(
			(change) =>
				`${change.field}: ${change.from || "-"} → ${change.to || "-"}`,
		),
		restart: plan.restartRequired ? [...(plan.reasons ?? [])] : [],
		warnings,
		deletesTailnetDevice,
	};
}

/** Whether Deploy proxy needs the extra "are you sure" weight. */
export function planIsDisruptive(plan: ProxyPlan): boolean {
	return plan.restartRequired || planHighlights(plan).deletesTailnetDevice;
}
