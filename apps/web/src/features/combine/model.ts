import type { Service } from "#/api/models";
import {
	DEFAULT_PROCESS_NAME,
	MAX_PROCESSES,
	PROCESS_NAME_PATTERN,
} from "#/features/processes/model";

// The pure side of "Combine into one service": which services can be merged,
// what the plan asks of the reader (a target, process names, a choice for
// every variable that differs) and how the saga's progress reads. The control
// plane stays authoritative: the pre-check here only spares a round trip and
// explains the menu item that is disabled.

/** The services a pre-check reads. */
export type CombineCandidate = Pick<
	Service,
	| "id"
	| "slug"
	| "name"
	| "kind"
	| "sourceType"
	| "environmentId"
	| "domains"
	| "latestDeployment"
	| "processes"
> & {
	config: Pick<
		Service["config"],
		| "port"
		| "gitUrl"
		| "gitBranch"
		| "buildType"
		| "dockerfilePath"
		| "buildContext"
		| "submodules"
		| "buildLocation"
	>;
};

/** The most services one combine takes, the target included. */
export const MAX_COMBINE = MAX_PROCESSES;

/** Lower-cased host and path without credentials, ".git" or trailing slashes. */
export function normalizeGitUrl(url: string): string {
	let value = url.trim();
	try {
		const parsed = new URL(value);
		value = `${parsed.protocol}//${parsed.host.toLowerCase()}${parsed.pathname}`;
	} catch {
		// Not a URL (scp-style); compare it as written.
	}
	return value.replace(/\/+$/, "").replace(/\.git$/, "");
}

/** The build settings every combined service must share, with the sentence that names them. */
const SOURCE_FIELDS: {
	label: string;
	read(config: CombineCandidate["config"]): string;
}[] = [
	{
		label: "repository",
		read: (config) => normalizeGitUrl(config.gitUrl ?? ""),
	},
	{ label: "branch", read: (config) => config.gitBranch ?? "" },
	{ label: "build type", read: (config) => config.buildType ?? "auto" },
	{
		label: "Dockerfile path",
		read: (config) => config.dockerfilePath ?? "Dockerfile",
	},
	{ label: "build context", read: (config) => config.buildContext ?? "." },
	{
		label: "submodules setting",
		read: (config) => config.submodules ?? "auto",
	},
	{ label: "build location", read: (config) => config.buildLocation ?? "" },
];

const ACTIVE_DEPLOYMENT: Record<string, true> = {
	created: true,
	queued: true,
	preparing: true,
	building: true,
	releasing: true,
	verifying: true,
};

export type CombineCheck = { ok: true } | { ok: false; reason: string };

/**
 * Whether the selected services can be combined, and if not the first reason
 * in words that say what to change. Mirrors the control plane's source rules
 * (same repository, branch and build settings; app or worker; no processes
 * yet; nothing deploying); everything else comes back from the plan.
 */
export function canCombine(
	services: readonly CombineCandidate[],
): CombineCheck {
	const no = (reason: string): CombineCheck => ({ ok: false, reason });
	if (services.length < 2)
		return no("Select at least two services to combine.");
	if (services.length > MAX_COMBINE) {
		return no(`Combine at most ${MAX_COMBINE} services at once.`);
	}
	const first = services[0] as CombineCandidate;
	for (const service of services) {
		if (service.environmentId !== first.environmentId) {
			return no(
				"These services are in different environments. Combine services of one environment.",
			);
		}
	}
	for (const service of services) {
		if (service.kind !== "app" && service.kind !== "worker") {
			return no(
				`${service.name} is not an app or worker. Only apps and workers can be combined.`,
			);
		}
		if (service.sourceType !== "git") {
			return no(
				`${service.name} deploys an image, not a Git repository. Only services built from the same repository can be combined.`,
			);
		}
		if (service.processes.length > 0) {
			return no(
				`${service.name} already has several processes. Combine services that each run one process.`,
			);
		}
	}
	for (const field of SOURCE_FIELDS) {
		const expected = field.read(first.config);
		const other = services.find(
			(service) => field.read(service.config) !== expected,
		);
		if (other) {
			return no(
				`${first.name} and ${other.name} use a different ${field.label}. Make them match to combine.`,
			);
		}
	}
	const busy = services.find(
		(service) =>
			service.latestDeployment !== null &&
			ACTIVE_DEPLOYMENT[service.latestDeployment.status] === true,
	);
	if (busy) {
		return no(
			`${busy.name} is deploying. Wait for it to finish, then combine.`,
		);
	}
	return { ok: true };
}

/**
 * The service that stays: the one that already serves traffic (has a domain),
 * else the one with a port, else the first selected.
 */
export function defaultTarget(services: readonly CombineCandidate[]): string {
	const served = services.find((service) => service.domains.length > 0);
	const ported = services.find((service) => (service.config.port ?? 0) > 0);
	return (served ?? ported ?? services[0])?.id ?? "";
}

// --- process names ----------------------------------------------------------

/** A slug as a process name: lowercase DNS label of at most 31 characters. */
function asProcessName(value: string): string {
	return value
		.toLowerCase()
		.replace(/[^a-z0-9-]+/g, "-")
		.replace(/^-+|-+$/g, "")
		.slice(0, 31)
		.replace(/-+$/g, "");
}

/**
 * The process a service becomes: its slug without the target's slug as a
 * prefix ("shop-worker" next to "shop" becomes "worker"), else the slug.
 */
export function suggestProcessName(slug: string, targetSlug: string): string {
	const prefix = `${targetSlug}-`;
	const stripped = slug.startsWith(prefix) ? slug.slice(prefix.length) : slug;
	return asProcessName(stripped) || asProcessName(slug) || "worker";
}

/** The names for every non-target service, kept unique with "-2", "-3" when two suggestions collide. */
export function suggestProcessNames(
	services: readonly Pick<CombineCandidate, "id" | "slug">[],
	targetId: string,
	defaultName = DEFAULT_PROCESS_NAME,
): Record<string, string> {
	const target = services.find((service) => service.id === targetId);
	const used = new Set([defaultName]);
	const names: Record<string, string> = {};
	for (const service of services) {
		if (service.id === targetId) continue;
		const base = suggestProcessName(service.slug, target?.slug ?? "");
		let name = base;
		for (let n = 2; used.has(name); n++) name = `${base.slice(0, 28)}-${n}`;
		used.add(name);
		names[service.id] = name;
	}
	return names;
}

/** What is wrong with each typed process name, by service id; empty when all are fine. */
export function processNameProblems(
	names: Readonly<Record<string, string>>,
	defaultName = DEFAULT_PROCESS_NAME,
): Record<string, string> {
	const problems: Record<string, string> = {};
	const counts = new Map<string, number>();
	for (const name of Object.values(names)) {
		counts.set(name, (counts.get(name) ?? 0) + 1);
	}
	for (const [id, name] of Object.entries(names)) {
		if (name === "") problems[id] = "Name is required.";
		else if (!PROCESS_NAME_PATTERN.test(name)) {
			problems[id] =
				"Use 1 to 31 lowercase letters, digits or hyphens, starting and ending with a letter or digit.";
		} else if (name === defaultName) {
			problems[id] =
				`${defaultName} is the name of the default process. Pick another name.`;
		} else if ((counts.get(name) ?? 0) > 1) {
			problems[id] =
				`Two services would both become ${name}. Use different names.`;
		}
	}
	return problems;
}

// --- the plan ---------------------------------------------------------------

export interface PlanMember {
	serviceId: string;
	slug: string;
	name: string;
	port: number;
	command: readonly string[] | null;
	domains: readonly { id: string; hostname: string }[] | null;
	hasStagedChanges: boolean;
	suggestedProcessName: string;
	isTarget: boolean;
}

export interface PlanConflictOption {
	serviceId: string;
	slug: string;
	/** The value; empty for a secret, whose value never leaves the server. */
	preview: string;
	sameAs: readonly string[] | null;
}

export interface PlanConflict {
	key: string;
	secret: boolean;
	scope: string;
	options: readonly PlanConflictOption[] | null;
}

export interface PlanLike {
	fingerprint: string;
	targetServiceId: string;
	members: readonly PlanMember[] | null;
	blocking:
		| readonly { code: string; serviceId?: string; message: string }[]
		| null;
	warnings: readonly string[] | null;
	variables: {
		sameCount: number;
		onlyIn:
			| readonly { key: string; serviceId: string; secret: boolean }[]
			| null;
		conflicts: readonly PlanConflict[] | null;
	};
	settingsIgnored:
		| readonly { serviceId: string; field: string; note: string }[]
		| null;
	domainMoves:
		| readonly {
				domainId: string;
				hostname: string;
				fromServiceId: string;
				process: string;
		  }[]
		| null;
	taskMoves:
		| readonly { taskId: string; name: string; fromServiceId: string }[]
		| null;
}

export function planConflicts(plan: PlanLike): readonly PlanConflict[] {
	return plan.variables.conflicts ?? [];
}

/** How to fix each blocker the plan can return; shown after the server's own message. */
const BLOCKING_FIX: Record<string, string> = {
	not_combinable: "Only apps and workers built from Git can be combined.",
	source_mismatch:
		"Make the repository and build settings match, then try again.",
	server_mismatch: "Move the services to the same server, then try again.",
	agent_too_old:
		"Update the agent on that server, then try again. No changes were made.",
	deployment_in_progress: "Wait for the deployment to finish, then try again.",
	combine_in_progress: "Wait for the other combine to finish, or abort it.",
	source_has_volumes:
		"Keep the service with the volume as the one that stays, or detach its volume first.",
	already_has_processes: "Combine services that each run one process.",
	port_conflict: "Change one service's port so every process has its own.",
	task_name_conflict: "Rename one of the tasks, then try again.",
	source_referenced:
		"Point the services that reference it at the service that stays first.",
	plan_stale: "Open the dialog again to review the current plan.",
};

/** A blocker as one message: what the server said, then what to do. */
export function blockerText(blocker: {
	code: string;
	message: string;
}): string {
	const fix = BLOCKING_FIX[blocker.code];
	if (!fix || blocker.message.includes(fix)) return blocker.message;
	return `${blocker.message} ${fix}`;
}

export function planBlockers(plan: PlanLike): string[] {
	return (plan.blocking ?? []).map(blockerText);
}

// --- what the reader chooses ------------------------------------------------

/** The dialog's working state; everything the request needs beyond the plan. */
export interface CombineChoices {
	targetServiceId: string;
	/** Process name per non-target service id, as typed. */
	processNames: Record<string, string>;
	/** Chosen service id per conflicting variable key. */
	variableChoices: Record<string, string>;
	stopSourcesFirst: boolean;
}

/** Conflicting keys that still have no choice (or whose choice left the plan). */
export function unresolvedConflicts(
	plan: PlanLike,
	choices: Readonly<Record<string, string>>,
): string[] {
	return planConflicts(plan)
		.filter((conflict) => {
			const chosen = choices[conflict.key];
			return !(conflict.options ?? []).some(
				(option) => option.serviceId === chosen,
			);
		})
		.map((conflict) => conflict.key);
}

/** Picks one service's value for every conflict it has an option in; the others are left alone. */
export function chooseFor(
	plan: PlanLike,
	serviceId: string,
	choices: Readonly<Record<string, string>>,
): Record<string, string> {
	const next = { ...choices };
	for (const conflict of planConflicts(plan)) {
		if (
			(conflict.options ?? []).some((option) => option.serviceId === serviceId)
		) {
			next[conflict.key] = serviceId;
		}
	}
	return next;
}

/** The label of one option of a conflict: the value, or "differs" for a secret. */
export function conflictOptionLabel(
	conflict: Pick<PlanConflict, "secret">,
	option: Pick<PlanConflictOption, "slug" | "preview" | "sameAs">,
	slugOf: (serviceId: string) => string = (id) => id,
): string {
	const also = (option.sameAs ?? []).map(slugOf);
	const who =
		also.length > 0 ? `${option.slug}, ${also.join(", ")}` : option.slug;
	const value = conflict.secret ? "differs" : option.preview;
	return value === "" ? who : `${who} · ${value}`;
}

/** Keys that exist in one service only, grouped by the service that has them: they are kept as they are. */
export function keptAsIs(plan: PlanLike): Map<string, string[]> {
	const byService = new Map<string, string[]>();
	for (const entry of plan.variables.onlyIn ?? []) {
		byService.set(entry.serviceId, [
			...(byService.get(entry.serviceId) ?? []),
			entry.key,
		]);
	}
	return byService;
}

export interface ReviewProcess {
	name: string;
	/** The service the process comes from. */
	fromSlug: string;
	isDefault: boolean;
}

export interface ReviewDomainMove {
	hostname: string;
	process: string;
	fromSlug: string;
}

export interface CombineReview {
	/** The processes the target will have, the default one first. */
	processes: ReviewProcess[];
	domains: ReviewDomainMove[];
	tasks: { name: string; fromSlug: string }[];
	/** Settings of the other services that are not carried over: the target's win. */
	ignored: { fromSlug: string; field: string; note: string }[];
	warnings: string[];
	blockers: string[];
}

/** What step 3 lists: the result of combining with the names and target the reader chose. */
export function reviewOf(
	plan: PlanLike,
	choices: Pick<CombineChoices, "targetServiceId" | "processNames">,
	defaultName = DEFAULT_PROCESS_NAME,
): CombineReview {
	const members = plan.members ?? [];
	const slugOf = (id: string) =>
		members.find((member) => member.serviceId === id)?.slug ?? id;
	const processes: ReviewProcess[] = [
		{
			name: defaultName,
			fromSlug: slugOf(choices.targetServiceId),
			isDefault: true,
		},
	];
	for (const member of members) {
		if (member.serviceId === choices.targetServiceId) continue;
		processes.push({
			name:
				choices.processNames[member.serviceId] ?? member.suggestedProcessName,
			fromSlug: member.slug,
			isDefault: false,
		});
	}
	return {
		processes,
		domains: (plan.domainMoves ?? []).map((move) => ({
			hostname: move.hostname,
			// A domain keeps the process its service becomes.
			process: choices.processNames[move.fromServiceId] ?? move.process,
			fromSlug: slugOf(move.fromServiceId),
		})),
		tasks: (plan.taskMoves ?? []).map((move) => ({
			name: move.name,
			fromSlug: slugOf(move.fromServiceId),
		})),
		ignored: (plan.settingsIgnored ?? []).map((entry) => ({
			fromSlug: slugOf(entry.serviceId),
			field: entry.field,
			note: entry.note,
		})),
		warnings: [...(plan.warnings ?? [])],
		blockers: planBlockers(plan),
	};
}

/** The request body of `combineServices` for the plan and the reader's choices. */
export function buildRequest(
	plan: PlanLike,
	choices: CombineChoices,
	defaultName: string,
	message = "",
): {
	fingerprint: string;
	targetServiceId: string;
	serviceIds: string[];
	processNames: Record<string, string>;
	defaultProcessName?: string;
	variableChoices: Record<string, string>;
	stopSourcesFirst: boolean;
	message?: string;
} {
	const serviceIds = (plan.members ?? []).map((member) => member.serviceId);
	const processNames: Record<string, string> = {};
	for (const id of serviceIds) {
		if (id === choices.targetServiceId) continue;
		processNames[id] = (choices.processNames[id] ?? "").trim();
	}
	const variableChoices: Record<string, string> = {};
	for (const conflict of planConflicts(plan)) {
		const chosen = choices.variableChoices[conflict.key];
		if (chosen !== undefined) variableChoices[conflict.key] = chosen;
	}
	return {
		fingerprint: plan.fingerprint,
		targetServiceId: choices.targetServiceId,
		serviceIds,
		processNames,
		...(defaultName !== DEFAULT_PROCESS_NAME
			? { defaultProcessName: defaultName }
			: {}),
		variableChoices,
		stopSourcesFirst: choices.stopSourcesFirst,
		...(message.trim() !== "" ? { message: message.trim() } : {}),
	};
}

// --- steps ------------------------------------------------------------------

export type CombineStepId = "services" | "variables" | "review" | "progress";

/** The dialog's steps: Variables appears only when the plan has conflicts. */
export function combineSteps(
	plan: PlanLike | undefined,
): { id: CombineStepId; label: string }[] {
	const steps: { id: CombineStepId; label: string }[] = [
		{ id: "services", label: "Services" },
	];
	if (plan && planConflicts(plan).length > 0) {
		steps.push({ id: "variables", label: "Variables" });
	}
	steps.push(
		{ id: "review", label: "Review" },
		{ id: "progress", label: "Progress" },
	);
	return steps;
}

// --- progress ---------------------------------------------------------------

export type CombineState =
	| "staged"
	| "deploying"
	| "cutover"
	| "done"
	| "failed"
	| "aborted";

export interface CombineViewLike {
	state: string;
	error?: string | undefined;
	deploymentId?: string | null | undefined;
}

export type ProgressStepState = "done" | "current" | "pending" | "failed";

export interface ProgressStep {
	id: "staged" | "deploying" | "cutover" | "done";
	label: string;
	state: ProgressStepState;
}

const PROGRESS_ORDER = ["staged", "deploying", "cutover", "done"] as const;
const PROGRESS_LABEL: Record<(typeof PROGRESS_ORDER)[number], string> = {
	staged: "Staged",
	deploying: "Deploying",
	cutover: "Cutover",
	done: "Done",
};

/**
 * The four stages of a combine. `failed` stops at the stage the combine
 * reached: staging happens before the deployment, so a failure with no
 * deployment is a staging failure; with one, the deployment failed.
 */
export function progressSteps(view: CombineViewLike): ProgressStep[] {
	const failed = view.state === "failed";
	const reached = (() => {
		switch (view.state) {
			case "staged":
				return 0;
			case "deploying":
				return 1;
			case "cutover":
				return 2;
			case "done":
				return 3;
			case "failed":
				return view.deploymentId ? 1 : 0;
			default:
				return 0;
		}
	})();
	return PROGRESS_ORDER.map((id, index) => {
		let state: ProgressStepState = "pending";
		if (view.state === "done" || index < reached) state = "done";
		else if (index === reached) state = failed ? "failed" : "current";
		return { id, label: PROGRESS_LABEL[id], state };
	});
}

/** Done, failed and aborted end the polling; the others are still moving. */
export function isSettled(view: Pick<CombineViewLike, "state">): boolean {
	return (
		view.state === "done" || view.state === "failed" || view.state === "aborted"
	);
}

/** How often the dialog asks for progress while the combine runs. */
export const COMBINE_POLL_MS = 2000;
