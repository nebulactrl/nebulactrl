import type { Team, TeamDeletion } from "#/api/gen/types.gen";
import {
	isStalled,
	isWaitingForRestart,
	type UpdateInfo,
	type UpdateRun,
} from "#/features/settings/update-model";

/** The steps of a team deletion, in the order the job runs them. */
const DELETION_STEPS = [
	{ id: "projects", label: "Deleting projects and their services" },
	{ id: "servers", label: "Emptying and removing servers" },
	{
		id: "integrations",
		label: "Removing status pages, Cloudflare and Tailscale",
	},
	{ id: "team", label: "Deleting the team" },
] as const satisfies readonly {
	id: NonNullable<TeamDeletion["step"]>;
	label: string;
}[];

type DeletionStepId = (typeof DELETION_STEPS)[number]["id"];

/** The words for the step a deletion is at; "Working" before the job reports one. */
export function deletionStepLabel(step: TeamDeletion["step"]): string {
	return DELETION_STEPS.find((s) => s.id === step)?.label ?? "Working";
}

export interface DeletionStepRow {
	id: DeletionStepId;
	label: string;
	state: "done" | "current" | "upcoming";
}

/**
 * The step list of a running deletion. Before the job names a step the first
 * is current: it starts there.
 */
export function deletionStepRows(
	step: TeamDeletion["step"],
): DeletionStepRow[] {
	const at = Math.max(
		0,
		DELETION_STEPS.findIndex((s) => s.id === step),
	);
	return DELETION_STEPS.map((s, i) => ({
		id: s.id,
		label: s.label,
		state: i < at ? "done" : i === at ? "current" : "upcoming",
	}));
}

/** The team the console acts in, when its deletion is running. */
export function deletingCurrentTeam(teams: readonly Team[]): Team | undefined {
	return teams.find((t) => t.current && t.deletion?.state === "deleting");
}

/**
 * What became of a team whose deletion this page watched: still `running`,
 * `gone` once the list no longer has it, or `stopped` when it is listed and
 * not deleting (the deletion failed, so the inline state takes over).
 */
export function deletionOutcome(
	teamId: string,
	teams: readonly Team[],
): "running" | "gone" | "stopped" {
	const team = teams.find((t) => t.id === teamId);
	if (!team) return "gone";
	return team.deletion?.state === "deleting" ? "running" : "stopped";
}

/** A full-screen threshold view of the console, in place of the app. */
export type ThresholdView =
	| {
			kind: "update";
			run: UpdateRun;
			/** The control plane does not answer: it is restarting. */
			restarting: boolean;
	  }
	| { kind: "team-deletion"; team: Team };

/**
 * Which full-screen view the console shows, if any. The control-plane update
 * wins: while it runs nothing else works. It hands back to the app when the
 * run ends, or once it has outlived the give-up window (the Version card
 * carries that warning). A team deletion takes the screen only when it is the
 * current team that is going; any other, and a failed one, stay inline.
 */
export function thresholdView({
	update,
	updateErrorStatus,
	now,
	teams,
}: {
	update: UpdateInfo | undefined;
	updateErrorStatus: number | undefined;
	now: number;
	teams: readonly Team[];
}): ThresholdView | null {
	const run = update?.updating;
	if (run?.state === "running" && !isStalled(run, now)) {
		return {
			kind: "update",
			run,
			restarting: isWaitingForRestart(update, updateErrorStatus),
		};
	}
	const team = deletingCurrentTeam(teams);
	return team ? { kind: "team-deletion", team } : null;
}
