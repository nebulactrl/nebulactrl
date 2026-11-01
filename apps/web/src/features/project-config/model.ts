// What the nebula.toml screens decide, as plain functions: which chip a
// service carries, what the banner says, how a plan reads as review rows, and
// what an apply result means. The shapes below are the minimum the screens
// read; the generated API types (src/api/gen) satisfy them structurally, so a
// field that goes missing or changes type fails to compile at the call site.

import { SECRET_MASK } from "#/features/staged/staged";
import { formatBytes } from "#/lib/format";

export { SECRET_MASK };

/** A generated value is described, never shown: `generated(secret(48))`. */
const GENERATED_PREFIX = "generated(";

export type ConfigOrigin = "git" | "cli" | "api" | "mcp" | "ui" | (string & {});

/** One thing the validator, planner or applier has to say; see "Diagnostics" in the reference. */
export interface DiagnosticLike {
	severity: string;
	code: string;
	message: string;
	path?: string | undefined;
	line?: number | undefined;
	column?: number | undefined;
	hint?: string | undefined;
	suggestions?: readonly string[] | null | undefined;
	environment?: string | undefined;
}

export interface PlanChangeLike {
	path: string;
	line?: number | undefined;
	field: string;
	op: string;
	applies: string;
	reason: string;
	from: string;
	to: string;
	masked: boolean;
}

export interface PlanServiceLike {
	slug: string;
	kind?: string | undefined;
	action: string;
	managed?: boolean | undefined;
	state?: string | undefined;
	note?: string | undefined;
	changes?: readonly PlanChangeLike[] | null | undefined;
	released?: readonly string[] | null | undefined;
}

export interface PlanLike {
	planId: string;
	valid: boolean;
	source: {
		kind: string;
		repo?: string | undefined;
		branch?: string | undefined;
		path?: string | undefined;
		commit?: string | undefined;
		bytes?: number | undefined;
	};
	summary: {
		create: number;
		update: number;
		unchanged: number;
		notInFile: number;
		staged: number;
		immediate: number;
		deferred: number;
		pruned?: number | undefined;
		errors: number;
		warnings: number;
		requiresDeploy?: readonly string[] | null | undefined;
	};
	diagnostics?: readonly DiagnosticLike[] | null | undefined;
	services?: readonly PlanServiceLike[] | null | undefined;
	shared?:
		| {
				project?: readonly PlanChangeLike[] | null | undefined;
				environment?: readonly PlanChangeLike[] | null | undefined;
		  }
		| null
		| undefined;
	guard?:
		| { productionApproval?: string | undefined; reason?: string | undefined }
		| null
		| undefined;
}

interface ApplyStepLike {
	step: string;
	status: string;
	message?: string | undefined;
}

export interface ApplyResultLike {
	planId?: string | undefined;
	status: string;
	steps?: readonly ApplyStepLike[] | null | undefined;
	deployments?: readonly string[] | null | undefined;
	plan?: PlanLike | null | undefined;
}

export interface ServiceSourceLike {
	managed: boolean;
	state?: string | undefined;
	origin?: ConfigOrigin | undefined;
	repo?: string | undefined;
	branch?: string | undefined;
	path?: string | undefined;
	commit?: string | undefined;
	fields?: readonly { field: string; state: string }[] | null | undefined;
	pending?: readonly string[] | null | undefined;
}

export interface EnvironmentSourceLike {
	mode: string;
	connectionId?: string | undefined;
	repo?: string | undefined;
	branch?: string | undefined;
	path?: string | undefined;
	resolvedPath?: string | undefined;
	autoApply?: boolean | undefined;
	status?: string | undefined;
	statusDetail?: string | undefined;
	commit?: string | undefined;
	checkedAt?: string | null | undefined;
	services?:
		| readonly { slug: string; state: string; changes?: number | undefined }[]
		| null
		| undefined;
}

// --- Chips and marks --------------------------------------------------------

/** The field and service states of the three-way comparison, worst last. */
const STATE_RANK: Record<string, number> = {
	in_sync: 0,
	file_changed: 1,
	drift: 2,
	conflict: 3,
};

/** A state of an owned field or service (a file that was applied at least once). */
function isManagedState(state: string | undefined): boolean {
	return state !== undefined && Object.hasOwn(STATE_RANK, state);
}

/** The worst of several states: `conflict` > `drift` > `file_changed` > `in_sync`. */
export function worstState(states: readonly string[]): string | undefined {
	let worst: string | undefined;
	for (const state of states) {
		const rank = Object.hasOwn(STATE_RANK, state)
			? STATE_RANK[state]
			: undefined;
		if (rank === undefined) continue;
		if (worst === undefined || rank > (STATE_RANK[worst] ?? -1)) worst = state;
	}
	return worst;
}

export type ChipKind = "differs" | "edited" | "managed";

export interface ConfigChip {
	kind: ChipKind;
	/** The words on the chip. */
	label: string;
	/** The sentence in its tooltip. */
	detail: string;
}

/**
 * The chip a service carries. Amber "nebula.toml differs" when the file has a
 * newer value or both sides changed, "edited here" when only the dashboard
 * moved, neutral "from nebula.toml" when in sync. An unmanaged service, or one
 * the file only plans to create or adopt, carries none.
 */
export function configChip(
	managed: boolean,
	state: string | undefined,
): ConfigChip | null {
	if (!managed || !isManagedState(state)) return null;
	switch (state) {
		case "file_changed":
			return {
				kind: "differs",
				label: "nebula.toml differs",
				detail:
					"The file has newer values than this service. Review the file to stage them.",
			};
		case "conflict":
			return {
				kind: "differs",
				label: "nebula.toml differs",
				detail:
					"The file and the dashboard both changed since the last apply. Applying the file overwrites the dashboard edit.",
			};
		case "drift":
			return {
				kind: "edited",
				label: "edited here",
				detail:
					"Changed in the dashboard since the last apply of nebula.toml. Applying the file again overwrites it.",
			};
		default:
			return {
				kind: "managed",
				label: "from nebula.toml",
				detail: "Set by nebula.toml and in sync with it.",
			};
	}
}

export function shortSha(sha: string | undefined): string {
	return (sha ?? "").slice(0, 7);
}

const ORIGIN_WORDS: Record<string, string> = {
	cli: "applied with the CLI",
	api: "applied through the API",
	mcp: "applied by an agent",
	ui: "applied from the dashboard",
};

/** "acme/shop@3f9c2a1", or how the file got here when it came from nowhere versioned. */
export function originLabel(source: {
	repo?: string | undefined;
	commit?: string | undefined;
	origin?: ConfigOrigin | undefined;
}): string {
	const sha = shortSha(source.commit);
	if (source.repo) return sha ? `${source.repo}@${sha}` : source.repo;
	if (source.origin && ORIGIN_WORDS[source.origin]) {
		return ORIGIN_WORDS[source.origin] ?? "";
	}
	return "applied from a file";
}

/** The tooltip of a field mark. */
export function markTooltip(source: ServiceSourceLike): string {
	return `Set by nebula.toml (${originLabel(source)}). Editing here is allowed and shows as drift until the file changes or you re-apply.`;
}

export interface FieldMarkModel {
	/** The state of the field, or the worst of the fields under it. */
	state: string;
	/** Drifted: the dashboard value differs from what the file would write. */
	drifted: boolean;
	/** The owned field ids the mark stands for: what Stop managing releases. */
	fields: string[];
}

/**
 * The mark of one control. `field` names an owned field exactly
 * (`config.port`) or a whole group (`scale`, `variables`), in which case the
 * worst state among the fields under it counts. Null when the file does not
 * own it. With `exact` only that one field counts (a hostname is not a group).
 */
export function fieldMark(
	source: ServiceSourceLike | undefined,
	field: string,
	options: { exact?: boolean } = {},
): FieldMarkModel | null {
	if (!source?.managed) return null;
	const owned = (source.fields ?? []).filter(
		(entry) =>
			// `variables.KEY#secret` is the secret flag of the same variable.
			entry.field === field ||
			(options.exact !== true &&
				(entry.field.startsWith(`${field}.`) ||
					entry.field.startsWith(`${field}#`))),
	);
	const state = worstState(owned.map((entry) => entry.state));
	if (state === undefined) return null;
	return {
		state,
		drifted: state === "drift" || state === "conflict",
		fields: owned.map((entry) => entry.field),
	};
}

// --- The environment banner ----------------------------------------------------

export interface BannerModel {
	kind: "differs" | "invalid";
	/** "nebula.toml @3f9c2a1 differs — 3 services". */
	title: string;
	/** For an invalid file: the first error as the control plane reported it. */
	detail?: string;
	/** The line of that error in the file, when the report names one. */
	line?: number;
	commit: string;
	/** Services that differ. */
	count: number;
}

const DIFFERING: Record<string, true> = {
	file_changed: true,
	drift: true,
	conflict: true,
	new: true,
	adopt: true,
};

/** The first `file:line` an error report names: "nebula.toml:19:1: error[…]". */
export function firstErrorLine(detail: string | undefined): number | undefined {
	if (!detail) return undefined;
	const match = /(?:^|[\s(])[\w./@-]*\.toml:(\d+)(?::\d+)?/.exec(detail);
	const line = match?.[1] === undefined ? Number.NaN : Number(match[1]);
	return Number.isInteger(line) && line > 0 ? line : undefined;
}

/**
 * What the banner above the canvas says. Only "differs" and "invalid" speak:
 * an environment with no file, an unreachable provider or an in-sync file
 * shows nothing.
 */
export function configBanner(
	source: EnvironmentSourceLike | undefined,
): BannerModel | null {
	if (!source || source.mode === "none") return null;
	const commit = shortSha(source.commit);
	const name = commit ? `nebula.toml @${commit}` : "nebula.toml";
	if (source.status === "differs") {
		const count = (source.services ?? []).filter(
			(service) => DIFFERING[service.state] === true,
		).length;
		const noun = count === 1 ? "service" : "services";
		return {
			kind: "differs",
			title:
				count > 0 ? `${name} differs — ${count} ${noun}` : `${name} differs`,
			commit,
			count,
		};
	}
	if (source.status === "invalid") {
		const detail = (source.statusDetail ?? "").split("\n")[0]?.trim() ?? "";
		const line = firstErrorLine(detail);
		return {
			kind: "invalid",
			title: `${name} is invalid`,
			...(detail ? { detail } : {}),
			...(line === undefined ? {} : { line }),
			commit,
			count: 0,
		};
	}
	return null;
}

export interface FileUrlInput {
	provider: string;
	/** The connection's instance URL; the provider's public address when empty. */
	baseUrl?: string | undefined;
	repo: string;
	/** A commit SHA or a branch. */
	ref: string;
	path: string;
	line?: number | undefined;
}

const PUBLIC_HOSTS: Record<string, string> = {
	github: "https://github.com",
	gitlab: "https://gitlab.com",
	bitbucket: "https://bitbucket.org",
};

/** Where the provider shows `path` at `ref`, anchored to `line`. Undefined without enough to build it. */
export function fileUrl(input: FileUrlInput): string | undefined {
	const base = (input.baseUrl || PUBLIC_HOSTS[input.provider] || "").replace(
		/\/+$/,
		"",
	);
	// The link is opened in a new tab: only http(s) bases, never a javascript: URL.
	if (!/^https?:\/\//i.test(base) || !input.repo || !input.ref || !input.path) {
		return undefined;
	}
	const path = input.path.split("/").map(encodeURIComponent).join("/");
	const line = input.line;
	switch (input.provider) {
		case "gitlab":
			return `${base}/${input.repo}/-/blob/${input.ref}/${path}${line ? `#L${line}` : ""}`;
		case "gitea":
		case "forgejo":
			return `${base}/${input.repo}/src/commit/${input.ref}/${path}${line ? `#L${line}` : ""}`;
		case "bitbucket":
			return `${base}/${input.repo}/src/${input.ref}/${path}${line ? `#lines-${line}` : ""}`;
		default:
			return `${base}/${input.repo}/blob/${input.ref}/${path}${line ? `#L${line}` : ""}`;
	}
}

// --- The plan as review rows -----------------------------------------------------

type DiffSign = "+" | "−" | "~";

export interface ChangeRow {
	key: string;
	sign: DiffSign;
	/** The key path under its service: `port`, `variables.NODE_ENV`. */
	label: string;
	/** The value, or `from → to`; secrets masked, generators described. */
	value: string;
	/** The line in the file that sets it. */
	line?: number;
	/** "staged", "applies at once" or "deferred". */
	applies: string;
	/** Why it is in the plan; null when "new" says it already. */
	reason: string | null;
	masked: boolean;
}

const APPLIES_WORDS: Record<string, string> = {
	staged: "staged",
	immediate: "applies at once",
	deferred: "deferred",
};

const REASON_WORDS: Record<string, string> = {
	file_changed: "file changed",
	drift: "edited in dashboard",
	conflict: "both changed",
	adopt: "differs from live",
};

function shown(value: string, masked: boolean): string {
	if (!masked || value === "" || value.startsWith(GENERATED_PREFIX)) {
		return value;
	}
	return SECRET_MASK;
}

/** A change as one diff row. `slug` shortens `services.<slug>.port` to `port`. */
export function changeRow(change: PlanChangeLike, slug?: string): ChangeRow {
	const prefix = slug === undefined ? "" : `services.${slug}.`;
	const label =
		prefix !== "" && change.path.startsWith(prefix)
			? change.path.slice(prefix.length)
			: change.path;
	const from = shown(change.from, change.masked);
	const to = shown(change.to, change.masked);
	let sign: DiffSign;
	let value: string;
	if (change.op === "add") {
		sign = "+";
		value = to;
	} else if (change.op === "remove") {
		sign = "−";
		value = from;
	} else if (from === "" && to !== "") {
		sign = "+";
		value = to;
	} else {
		sign = "~";
		value = `${from} → ${to}`;
	}
	return {
		key: `${change.path}:${change.field}`,
		sign,
		label,
		value,
		...(change.line ? { line: change.line } : {}),
		applies: APPLIES_WORDS[change.applies] ?? change.applies,
		reason: REASON_WORDS[change.reason] ?? null,
		masked: change.masked,
	};
}

export interface PlanView {
	/** Errors: any one of them blocks the whole environment. */
	blockers: DiagnosticLike[];
	warnings: DiagnosticLike[];
	infos: DiagnosticLike[];
	creates: PlanServiceLike[];
	updates: PlanServiceLike[];
	/** Paths the file no longer names: unmanaged now, kept as they are. */
	released: string[];
	notInFile: PlanServiceLike[];
	shared: { scope: "project" | "environment"; changes: PlanChangeLike[] }[];
	unchanged: number;
	/** Changes that will be recorded for deploy, and changes that run at once. */
	staged: number;
	immediate: number;
	hasChanges: boolean;
	/** Valid, nothing blocking, and something to write. */
	canStage: boolean;
	requiresDeploy: string[];
}

function visible(
	changes: readonly PlanChangeLike[] | null | undefined,
): PlanChangeLike[] {
	return (changes ?? []).filter(
		(change) => change.op !== "keep" && change.op !== "release",
	);
}

/** The plan, grouped as the review dialog lists it. */
export function planView(plan: PlanLike): PlanView {
	const diagnostics = plan.diagnostics ?? [];
	const blockers = diagnostics.filter((d) => d.severity === "error");
	const services = plan.services ?? [];
	const released: string[] = [];
	for (const service of services) {
		released.push(...(service.released ?? []));
		for (const change of service.changes ?? []) {
			if (change.op === "release") released.push(change.path);
		}
	}
	const shared = (
		[
			["project", visible(plan.shared?.project)],
			["environment", visible(plan.shared?.environment)],
		] as const
	)
		.filter(([, changes]) => changes.length > 0)
		.map(([scope, changes]) => ({ scope, changes }));
	const creates = services.filter((service) => service.action === "create");
	const updates = services.filter((service) => service.action === "update");
	const hasChanges =
		creates.length > 0 ||
		updates.length > 0 ||
		shared.length > 0 ||
		released.length > 0;
	return {
		blockers,
		warnings: diagnostics.filter((d) => d.severity === "warning"),
		infos: diagnostics.filter((d) => d.severity === "info"),
		creates,
		updates,
		released,
		notInFile: services.filter((service) => service.action === "not_in_file"),
		shared,
		unchanged: plan.summary.unchanged,
		staged: plan.summary.staged,
		immediate: plan.summary.immediate,
		hasChanges,
		canStage: plan.valid && blockers.length === 0 && hasChanges,
		requiresDeploy: [...(plan.summary.requiresDeploy ?? [])],
	};
}

/** "3 updates, 1 create · 4 staged, 1 applies at once". */
export function summaryLine(plan: PlanLike): string {
	const { summary } = plan;
	const parts: string[] = [];
	const count = (n: number, one: string, many: string) =>
		n > 0 ? parts.push(`${n} ${n === 1 ? one : many}`) : 0;
	count(summary.create, "create", "creates");
	count(summary.update, "update", "updates");
	count(summary.unchanged, "unchanged", "unchanged");
	count(summary.notInFile, "not in file", "not in file");
	const where: string[] = [];
	if (summary.staged > 0) where.push(`${summary.staged} staged`);
	if (summary.immediate > 0) where.push(`${summary.immediate} applies at once`);
	if (summary.deferred > 0) where.push(`${summary.deferred} deferred`);
	const head = parts.join(", ");
	if (head === "") return "Nothing to change";
	return where.length > 0 ? `${head} · ${where.join(", ")}` : head;
}

/** The dialog's footer: where the file came from, its size and the plan it was judged by. */
export function planFooter(plan: PlanLike): string[] {
	const { source } = plan;
	const sha = shortSha(source.commit);
	const origin =
		source.kind === "git"
			? source.repo
				? `${source.repo}${sha ? `@${sha}` : ""}`
				: "git"
			: "pasted file";
	const parts = [origin];
	if (source.path) parts.push(source.path);
	if (source.bytes !== undefined && source.bytes > 0) {
		parts.push(formatBytes(source.bytes));
	}
	const id = plan.planId;
	parts.push(id.length > 12 ? `${id.slice(0, 11)}…` : id);
	return parts;
}

/**
 * The changes a plan makes to one field of one service, and to the fields
 * under it unless `exact`: what "Revert to file" applies (`only` takes each
 * change's file key path). Empty when the file and the field already agree.
 */
export function changesForField(
	plan: PlanLike,
	slug: string,
	field: string,
	exact = false,
): PlanChangeLike[] {
	const service = (plan.services ?? []).find((entry) => entry.slug === slug);
	return (service?.changes ?? []).filter(
		(change) =>
			change.op !== "keep" &&
			change.op !== "release" &&
			(change.field === field ||
				(!exact &&
					(change.field.startsWith(`${field}.`) ||
						change.field.startsWith(`${field}#`)))),
	);
}

/** Said beside a staged line the file set: it is read from nebula.toml. */
export const FILE_NOTE = "set by nebula.toml";

/**
 * The staged lines of one service with `set by nebula.toml` added to the ones
 * whose field the file owns. A line that already has a note keeps it, first.
 */
export function withFileNotes<T extends { field: string; note?: string }>(
	source: ServiceSourceLike | undefined,
	lines: readonly T[],
): T[] {
	if (!source?.managed) return [...lines];
	return lines.map((line) => {
		if (fieldMark(source, line.field) === null) return line;
		return {
			...line,
			note: line.note ? `${line.note} · ${FILE_NOTE}` : FILE_NOTE,
		};
	});
}

/** Whether the plan asks a person to approve production changes. */
export function needsApproval(plan: PlanLike): boolean {
	return plan.guard?.productionApproval === "required";
}

/** A `file:line:col` heading for a diagnostic: "nebula.toml:19:1", or empty. */
export function diagnosticWhere(
	diagnostic: DiagnosticLike,
	fileName = "nebula.toml",
): string {
	if (!diagnostic.line) return "";
	return `${fileName}:${diagnostic.line}${diagnostic.column ? `:${diagnostic.column}` : ""}`;
}

// --- The result of an apply -------------------------------------------------------

export interface ApplyOutcome {
	tone: "success" | "warning" | "error" | "info";
	title: string;
	detail: string;
	failed: ApplyStepLike[];
	deferred: ApplyStepLike[];
	deployed: number;
}

/** What to tell the reader after an apply: staged, partly applied, failed, or nothing to do. */
export function applyOutcome(result: ApplyResultLike): ApplyOutcome {
	const steps = result.steps ?? [];
	const failed = steps.filter((step) => step.status === "failed");
	const deferred = steps.filter((step) => step.status === "deferred");
	const deployed = (result.deployments ?? []).length;
	const summary = result.plan?.summary;
	const base = { failed, deferred, deployed };
	switch (result.status) {
		case "noop":
			return {
				...base,
				tone: "info",
				title: "Already in sync",
				detail: "Nothing in nebula.toml differs from this environment.",
			};
		case "partial":
			return {
				...base,
				tone: "warning",
				title: "Partly applied",
				detail: `${failed.length} ${failed.length === 1 ? "step" : "steps"} failed. What was staged stays staged; apply again to retry the rest.`,
			};
		case "failed":
			return {
				...base,
				tone: "error",
				title: "Nothing was applied",
				detail: "Every step failed or was skipped. Nothing changed.",
			};
		default: {
			const staged = summary?.staged ?? 0;
			const immediate = summary?.immediate ?? 0;
			const bits: string[] = [];
			if (staged > 0) bits.push(`${staged} staged`);
			if (immediate > 0) bits.push(`${immediate} applied at once`);
			if (deferred.length > 0) bits.push(`${deferred.length} deferred`);
			const tail =
				deployed > 0
					? `Deploying ${deployed} ${deployed === 1 ? "service" : "services"}.`
					: "Nothing is deployed yet: review the staged changes, then deploy.";
			return {
				...base,
				tone: "success",
				title:
					deployed > 0 ? "Staged and deploying" : "Staged from nebula.toml",
				detail: `${bits.length > 0 ? `${bits.join(", ")}. ` : ""}${tail}`,
			};
		}
	}
}

/**
 * The fresh plan a `plan_stale` answer carries in `details.plan`, if it is
 * there. The dialog shows it in place of the one the reader was reviewing.
 */
export function stalePlan(
	details: Record<string, unknown> | undefined,
): PlanLike | undefined {
	const plan = details?.plan;
	if (
		typeof plan === "object" &&
		plan !== null &&
		"planId" in plan &&
		"summary" in plan
	) {
		return plan as PlanLike;
	}
	return undefined;
}

// --- Export -------------------------------------------------------------------------

export interface ExportWarning {
	code: string;
	message: string;
}

/** A warning of an export, which may be a diagnostic or a bare sentence. */
export function exportWarning(warning: unknown): ExportWarning {
	if (typeof warning === "string") return { code: "", message: warning };
	if (typeof warning === "object" && warning !== null) {
		return {
			code:
				"code" in warning && typeof warning.code === "string"
					? warning.code
					: "",
			message:
				"message" in warning && typeof warning.message === "string"
					? warning.message
					: "",
		};
	}
	return { code: "", message: "" };
}

export const EXPORT_FILE_NAME = "nebula.toml";

// --- "Config from git" -----------------------------------------------------------------

/** The Auto-apply switch: usable outside production only. */
export function autoApplySwitch(input: {
	isProduction: boolean;
	bound: boolean;
}): { disabled: boolean; reason: string | undefined } {
	if (input.isProduction) {
		return {
			disabled: true,
			reason:
				"Not available in production: a person reviews and applies changes there.",
		};
	}
	if (!input.bound) {
		return {
			disabled: true,
			reason: "Link a repository first: there is no file to apply.",
		};
	}
	return { disabled: false, reason: undefined };
}

const STATUS_WORDS: Record<string, string> = {
	in_sync: "In sync",
	differs: "Differs",
	invalid: "Invalid",
	not_found: "No file found",
	unreachable: "Could not read",
	error: "Check failed",
	unknown: "Not checked yet",
};

export function statusWords(status: string | undefined): string {
	return STATUS_WORDS[status ?? "unknown"] ?? "Not checked yet";
}

const MODE_WORDS: Record<string, string> = {
	explicit: "Set here",
	derived: "From the environment's git services",
	none: "Not linked",
};

export function modeWords(mode: string): string {
	return MODE_WORDS[mode] ?? mode;
}
