import type {
	PatchPlan,
	PatchRun,
	PatchView,
	ServerPatchSummary,
} from "#/api/gen/types.gen";
import type { StatusKind } from "#/components/ui/status-icon";
import { count } from "#/lib/format";

/** The shortest honest summary of the pending updates: "3 updates (1 security)". */
export function updatesText(
	s: Pick<ServerPatchSummary, "known" | "supported" | "updates" | "security">,
): string {
	if (!s.known) return "Updates not checked yet";
	if (!s.supported) return "Updates not managed here";
	if (s.updates === 0) return "Up to date";
	return `${count(s.updates, "update")} (${s.security} security)`;
}

/** The status of the update line: security updates warn, other updates are quiet. */
export function updatesStatus(
	s: Pick<ServerPatchSummary, "known" | "supported" | "updates" | "security">,
): StatusKind {
	if (!s.known || !s.supported) return "unknown";
	if (s.security > 0) return "warning";
	return s.updates > 0 ? "building" : "healthy";
}

/** Whether the server card needs to say anything about patching. */
export function needsAttention(
	s: Pick<
		ServerPatchSummary,
		"known" | "supported" | "updates" | "rebootRequired"
	>,
): boolean {
	return s.known && s.supported && (s.updates > 0 || s.rebootRequired);
}

/** The Updates tab's badge: "Reboot" first, then the security count; null when quiet. */
export function tabBadge(
	s: Pick<
		ServerPatchSummary,
		"known" | "supported" | "security" | "rebootRequired"
	>,
): string | null {
	if (!s.known || !s.supported) return null;
	if (s.rebootRequired) return "Reboot";
	return s.security > 0 ? `${s.security} security` : null;
}

/** Words for the run in flight, or null. */
export function busyText(busy: ServerPatchSummary["busy"]): string | null {
	switch (busy) {
		case "check":
			return "Checking for updates";
		case "apply":
			return "Upgrading packages";
		case "reboot":
			return "Rebooting";
		default:
			return null;
	}
}

/** Why the Check / Apply / Reboot buttons are disabled; null when they work. */
export function patchHint(
	view: Pick<PatchView, "agentSupported" | "busy" | "supported" | "known">,
	serverStatus: string,
	opts: {
		canCheck: boolean;
		instanceAdmin: boolean;
		action: "check" | "apply" | "reboot";
	},
): string | null {
	if (!view.agentSupported)
		return "This agent is too old to manage updates. Update it first.";
	if (serverStatus === "revoked") return "This server was revoked.";
	if (serverStatus !== "online")
		return "The server is offline. Actions start when it is back online.";
	if (view.busy) return `${busyText(view.busy)} is in progress.`;
	if (opts.action === "check") {
		return opts.canCheck ? null : "Only Owners and Admins can check a host.";
	}
	if (!opts.instanceAdmin)
		return "Only the instance admin can upgrade packages or reboot a server.";
	if (opts.action === "apply") {
		if (!view.known) return "Check for updates first.";
		if (!view.supported)
			return "This host has no supported package manager (apt or dnf).";
	}
	return null;
}

const ACTION_LABEL: Record<PatchRun["action"], string> = {
	check: "Check",
	apply: "Upgrade",
	reboot: "Reboot",
};

export function runActionLabel(run: Pick<PatchRun, "action">): string {
	return ACTION_LABEL[run.action];
}

export function triggerText(run: Pick<PatchRun, "trigger">): string {
	return run.trigger === "scheduled" ? "Automatic" : "By hand";
}

/** The status icon and words of a run. */
export function runStatus(run: Pick<PatchRun, "status" | "action">): {
	kind: StatusKind;
	label: string;
} {
	switch (run.status) {
		case "pending":
			return { kind: "building", label: "Running" };
		case "rebooting":
			return { kind: "building", label: "Waiting for the server to come back" };
		case "succeeded":
			return { kind: "healthy", label: "Done" };
		default:
			return { kind: "failed", label: "Failed" };
	}
}

/** One line for a finished run. */
export function runSummary(run: PatchRun): string {
	if (run.status === "failed") return run.error || "Failed";
	if (run.status === "pending") return "Running";
	if (run.status === "rebooting") return run.message || "Rebooting";
	switch (run.action) {
		case "apply": {
			const before = run.updatesBefore;
			const after = run.updatesAfter;
			if (before !== null && after !== null)
				return `${count(before, "update")} before, ${after} left`;
			return "Packages upgraded";
		}
		case "reboot":
			return run.message || "Rebooted";
		default:
			return run.updatesAfter === null
				? "Checked"
				: run.updatesAfter === 0
					? "Up to date"
					: `${count(run.updatesAfter, "update")} pending`;
	}
}

/** A server with no production service and not the control plane needs no acknowledgement. */
export function needsAcknowledgement(
	view: Pick<PatchView, "productionServices" | "controlPlane">,
): boolean {
	return view.productionServices.length > 0 || view.controlPlane;
}

/** "api, worker and 3 more" from the production services on the host. */
export function productionList(
	services: ReadonlyArray<{ name: string; project: string }>,
	max = 4,
): string {
	const names = services.slice(0, max).map((s) => `${s.project}/${s.name}`);
	const rest = services.length - names.length;
	return rest > 0 ? `${names.join(", ")} and ${rest} more` : names.join(", ");
}

/** The warnings a reboot confirmation must show. */
export function rebootWarnings(
	view: Pick<
		PatchView,
		"productionServices" | "controlPlane" | "rebootRequired"
	>,
	serviceCount: number,
): string[] {
	const out: string[] = [];
	out.push(
		serviceCount > 0
			? `${count(serviceCount, "service")} on this server go down while it reboots and start again when Docker is back.`
			: "Anything that runs on this server goes down while it reboots.",
	);
	if (view.productionServices.length > 0) {
		out.push(
			`Production is affected: ${productionList(view.productionServices)}.`,
		);
	}
	if (view.controlPlane) {
		out.push(
			"This server runs NebulaCtrl itself. The dashboard and API are unavailable until it is back.",
		);
	}
	out.push(
		"NebulaCtrl waits for the agent to reconnect on a new boot and reports a failure if that takes more than 20 minutes.",
	);
	return out;
}

/** Reasons the plan cannot be applied, or the packages that need a closer look. */
export function planCautions(plan: PatchPlan): string[] {
	return [
		...plan.risks.map((r) => `${r.package}: ${r.reason}`),
		...plan.warnings,
	];
}
