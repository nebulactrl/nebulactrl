import type {
	Environment,
	PreviewSettings,
	PreviewView,
} from "#/api/gen/types.gen";
import type { StatusKind } from "#/components/ui/status-icon";

export type PreviewStatus = PreviewView["status"];

export const STATUS_LABEL: Record<PreviewStatus, string> = {
	creating: "Creating",
	deploying: "Deploying",
	seeding: "Seeding database",
	ready: "Ready",
	failed: "Failed",
	paused: "Paused",
	deleting: "Deleting",
};

/** The status icon of a preview; the label always accompanies it. */
export const STATUS_KIND: Record<PreviewStatus, StatusKind> = {
	creating: "building",
	deploying: "deploying",
	seeding: "deploying",
	ready: "live",
	failed: "failed",
	paused: "stopped",
	deleting: "offline",
};

/** One line under the status: why it is paused or what failed. */
export function statusDetail(preview: PreviewView): string | undefined {
	if (preview.status === "paused") {
		return "Paused to stay within the limit. A new push to the pull request resumes it.";
	}
	return preview.statusMessage || undefined;
}

function isPreviewEnvironment(environment: Environment): boolean {
	return environment.kind === "preview";
}

interface EnvironmentGroup {
	/** A standard environment. */
	base: Environment;
	/** Its pull-request previews, newest PR first. */
	previews: Environment[];
}

export interface GroupedEnvironments {
	groups: EnvironmentGroup[];
	/** Previews whose base environment is not in the list. */
	detached: Environment[];
}

function byPullRequest(a: Environment, b: Environment): number {
	return (b.prNumber ?? 0) - (a.prNumber ?? 0);
}

/**
 * Groups the environments of a project for the switcher: every standard
 * environment in the order given (production first), each followed by its
 * previews.
 */
export function groupEnvironments(
	environments: readonly Environment[],
): GroupedEnvironments {
	const standard = environments.filter((e) => !isPreviewEnvironment(e));
	const previews = environments.filter(isPreviewEnvironment);
	const known = new Set(standard.map((e) => e.id));
	return {
		groups: standard.map((base) => ({
			base,
			previews: previews
				.filter((p) => p.parentEnvironmentId === base.id)
				.sort(byPullRequest),
		})),
		detached: previews
			.filter(
				(p) => !p.parentEnvironmentId || !known.has(p.parentEnvironmentId),
			)
			.sort(byPullRequest),
	};
}

/** `PR #12 · title`, as the server names the environment. */
export function previewLabel(environment: Environment): string {
	if (environment.name) return environment.name;
	return environment.prNumber === null || environment.prNumber === undefined
		? environment.slug
		: `PR #${environment.prNumber}`;
}

// --- Settings draft ---------------------------------------------------------

export interface SeedDraft {
	/** A key for React lists only. */
	key: string;
	service: string;
	command: string;
	runOn: "create" | "push";
}

export interface SettingsDraft {
	enabled: boolean;
	baseEnvironmentId: string | null;
	forkPrs: boolean;
	maxConcurrent: string;
	idleDays: string;
	excluded: string[];
	seeds: SeedDraft[];
}

let seedKey = 0;
export function newSeed(service = ""): SeedDraft {
	seedKey += 1;
	return { key: `seed-${seedKey}`, service, command: "", runOn: "create" };
}

export function draftFrom(settings: PreviewSettings): SettingsDraft {
	return {
		enabled: settings.enabled,
		baseEnvironmentId: settings.baseEnvironmentId ?? null,
		forkPrs: settings.forkPrs,
		maxConcurrent: String(settings.maxConcurrent),
		idleDays: String(settings.idleDays),
		excluded: [...(settings.excludedServices ?? [])].sort(),
		seeds: (settings.seedCommands ?? []).map((seed) => ({
			...newSeed(seed.service),
			command: seed.command,
			runOn: seed.runOn === "push" ? "push" : "create",
		})),
	};
}

export type DraftErrors = Partial<Record<"base" | "max" | "idle", string>> & {
	seeds?: Record<string, string>;
};

function wholeNumber(text: string): number | null {
	if (!/^\d+$/.test(text.trim())) return null;
	return Number(text);
}

export function validateDraft(draft: SettingsDraft): DraftErrors {
	const errors: DraftErrors = {};
	if (draft.enabled && !draft.baseEnvironmentId) {
		errors.base = "Choose the environment previews are cloned from.";
	}
	const max = wholeNumber(draft.maxConcurrent);
	if (max === null || max < 1 || max > 50) {
		errors.max = "Enter a number from 1 to 50.";
	}
	const idle = wholeNumber(draft.idleDays);
	if (idle === null || idle < 1 || idle > 365) {
		errors.idle = "Enter a number of days from 1 to 365.";
	}
	const seedErrors: Record<string, string> = {};
	for (const seed of draft.seeds) {
		if (!seed.service) {
			seedErrors[seed.key] = "Choose the service that runs it.";
		} else if (draft.excluded.includes(seed.service)) {
			seedErrors[seed.key] = "This service is excluded from previews.";
		} else if (!seed.command.trim()) {
			seedErrors[seed.key] = "Enter the command to run.";
		}
	}
	if (Object.keys(seedErrors).length > 0) errors.seeds = seedErrors;
	return errors;
}

export function hasErrors(errors: DraftErrors): boolean {
	return Object.keys(errors).length > 0;
}

/** The PUT body for a valid draft. */
export function draftToBody(draft: SettingsDraft) {
	return {
		enabled: draft.enabled,
		...(draft.baseEnvironmentId
			? { baseEnvironmentId: draft.baseEnvironmentId }
			: {}),
		forkPrs: draft.forkPrs,
		maxConcurrent: Number(draft.maxConcurrent),
		idleDays: Number(draft.idleDays),
		excludedServices: draft.excluded,
		seedCommands: draft.seeds.map((seed) => ({
			service: seed.service,
			command: seed.command.trim(),
			runOn: seed.runOn,
		})),
	};
}

export function isDirty(a: SettingsDraft, b: SettingsDraft): boolean {
	return JSON.stringify(stable(a)) !== JSON.stringify(stable(b));
}

function stable(draft: SettingsDraft) {
	return {
		...draft,
		maxConcurrent: draft.maxConcurrent.trim(),
		idleDays: draft.idleDays.trim(),
		excluded: [...draft.excluded].sort(),
		seeds: draft.seeds.map(({ service, command, runOn }) => ({
			service,
			command: command.trim(),
			runOn,
		})),
	};
}
