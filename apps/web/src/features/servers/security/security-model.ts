import type {
	HardeningFix,
	HardeningRemaining,
	HardeningRun,
	HardeningView,
} from "#/api/gen/types.gen";
import type { StatusKind } from "#/components/ui/status-icon";
import { count } from "#/lib/format";

/** What changed since the first audit, in words; null when it did not move. */
export function sinceFirst(
	view: Pick<HardeningView, "hardeningIndex" | "firstIndex">,
): string | null {
	const { hardeningIndex: now, firstIndex: first } = view;
	if (now === null || first === null || now === first) return null;
	const d = now - first;
	return `${d > 0 ? "+" : "−"}${Math.abs(d)} since the first audit (${first})`;
}

/** The status icon and words of a fix. */
export function fixStatus(fix: Pick<HardeningFix, "state">): {
	kind: StatusKind;
	label: string;
} {
	switch (fix.state) {
		case "applied":
			return { kind: "healthy", label: "Applied" };
		case "compliant":
			return { kind: "healthy", label: "Already in place" };
		case "pending":
			return { kind: "warning", label: "Not applied" };
		case "unavailable":
			return { kind: "stopped", label: "Not available here" };
		default:
			return { kind: "unknown", label: "Not audited yet" };
	}
}

/** Whether a fix can be applied or undone from its row. */
export function fixActions(fix: Pick<HardeningFix, "state">): {
	canApply: boolean;
	canUndo: boolean;
} {
	return {
		canApply: fix.state === "pending" || fix.state === "unknown",
		canUndo: fix.state === "applied",
	};
}

/** Why the buttons are disabled; null when they work. */
export function actionHint(
	view: Pick<HardeningView, "agentSupported" | "running">,
	serverStatus: string,
	canChange: boolean,
): string | null {
	if (!canChange) return "Only Owners and Admins can audit or change a host.";
	if (!view.agentSupported)
		return "This agent is too old to audit the host. Update it first.";
	if (serverStatus === "revoked") return "This server was revoked.";
	if (serverStatus !== "online")
		return "The server is offline. Runs start when it is back online.";
	if (view.running) return "A run is in progress.";
	return null;
}

const ACTION_LABEL: Record<HardeningRun["action"], string> = {
	audit: "Audit",
	apply: "Apply fixes",
	rollback: "Undo fixes",
};

export function runActionLabel(run: Pick<HardeningRun, "action">): string {
	return ACTION_LABEL[run.action];
}

export function triggerText(run: Pick<HardeningRun, "trigger">): string {
	return run.trigger === "scheduled" ? "Weekly" : "By hand";
}

/** One line for a run. */
export function runSummary(run: HardeningRun): string {
	switch (run.status) {
		case "pending":
			return "Running";
		case "failed":
			return "Failed";
		case "skipped":
			return "Lynis is not installed";
		default:
	}
	const parts: string[] = [];
	if (run.action !== "audit") {
		const done = run.outcomes.filter(
			(o) => o.outcome === (run.action === "apply" ? "applied" : "rolled_back"),
		).length;
		const word = run.action === "apply" ? "applied" : "undone";
		parts.push(`${count(done, "fix", "fixes")} ${word}`);
	}
	if (run.hardeningIndex !== null) {
		parts.push(
			run.indexBefore !== null && run.indexBefore !== run.hardeningIndex
				? `index ${run.indexBefore} → ${run.hardeningIndex}`
				: `index ${run.hardeningIndex}`,
		);
	}
	return parts.join(" · ") || "Done";
}

/** What a fix outcome means, in words. */
export function outcomeText(outcome: string): string {
	switch (outcome) {
		case "applied":
			return "Applied";
		case "unchanged":
			return "Already in place";
		case "skipped":
			return "Skipped";
		case "failed":
			return "Failed";
		case "rolled_back":
			return "Undone";
		default:
			return outcome;
	}
}

export interface RemainingGroups {
	/** A safe fix answers it. */
	fixable: HardeningRemaining[];
	/** A person decides: explained, with commands. */
	manual: HardeningRemaining[];
	/** NebulaCtrl deliberately never changes it. */
	never: HardeningRemaining[];
}

/** Warnings first within each group; the groups say who acts. */
export function groupRemaining(
	items: readonly HardeningRemaining[],
): RemainingGroups {
	const out: RemainingGroups = { fixable: [], manual: [], never: [] };
	const sorted = [...items].sort(
		(a, b) =>
			Number(b.severity === "warning") - Number(a.severity === "warning"),
	);
	for (const it of sorted) {
		if (it.never) out.never.push(it);
		else if (it.fixId && it.fixState !== "unavailable") out.fixable.push(it);
		else out.manual.push(it);
	}
	return out;
}

/** The ids "Apply safe fixes" sends: the default fixes that would change something. */
export function safeFixIds(fixes: readonly HardeningFix[]): string[] {
	return fixes
		.filter(
			(f) => f.default && (f.state === "pending" || f.state === "unknown"),
		)
		.map((f) => f.id);
}

/**
 * Whether the weekly audit switch is out of reach. Without Lynis every run
 * fails, so it cannot be turned on until Lynis is there; one that is already
 * on stays reachable so it can be turned off.
 */
export function weeklyAuditLocked(
	view: Pick<HardeningView, "weekly" | "lynisInstalled">,
): boolean {
	return !view.lynisInstalled && !view.weekly;
}

/**
 * The line under the weekly audit switch. Null when the next audit's time
 * is the line (the caller renders it as a relative time).
 */
export function weeklyAuditNote(
	view: Pick<HardeningView, "weekly" | "lynisInstalled" | "nextAuditAt">,
): string | null {
	if (!view.lynisInstalled) {
		return view.weekly
			? "Lynis is not installed, so every weekly audit fails. Turn this off, or install Lynis with “Install Lynis and run audit”."
			: "Needs Lynis. Install it with “Install Lynis and run audit” first, then this can be turned on.";
	}
	if (!view.weekly) return "Off. Audits run only when you start one.";
	return view.nextAuditAt ? null : "On. No audit is scheduled yet.";
}

/** What "Apply safe fixes" does next to the per-row "Apply" buttons. */
export function safeFixesNote(fixes: readonly HardeningFix[]): string {
	const safe = safeFixIds(fixes).length;
	const onRequest = fixes.filter(
		(f) => !f.default && fixActions(f).canApply,
	).length;
	const parts = [
		safe > 0
			? `“Apply safe fixes” applies every recommended fix below that is not applied yet (${safe}), all at once. “Apply” on a row applies only that fix.`
			: "Every recommended fix is applied or not needed here. “Apply” on a row applies only that fix.",
	];
	if (onRequest > 0) {
		parts.push(
			onRequest === 1
				? "One fix is marked On request: the batch leaves it out, so apply it from its own row."
				: `${onRequest} fixes are marked On request: the batch leaves them out, so apply them from their own rows.`,
		);
	}
	return parts.join(" ");
}
