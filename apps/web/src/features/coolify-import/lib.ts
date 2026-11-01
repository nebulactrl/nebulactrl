import type {
	CoolifyApplyRequest,
	CoolifyApplyResult,
	CoolifyCheck,
	CoolifyChoices,
	CoolifyEnvChoice,
	CoolifyNote,
	CoolifyPlan,
	CoolifyPlanEnv,
	CoolifyPlanProject,
	CoolifyPlanRequest,
	CoolifyPlanResource,
	CoolifyPlanRewrite,
	CoolifyPlanSource,
	CoolifyProjectChoice,
	CoolifyResourceChoice,
	CoolifyTotals,
} from "#/api/gen/types.gen";

export { parsePortField } from "#/features/compose-import/lib";

// --- what the reader typed ---------------------------------------------------
//
// The draft holds only what the reader changed: an unset field means "what the
// plan says". Strings are kept as typed; `buildChoices` trims them and leaves
// out whatever is empty. Values (a variable's value, a registry password) live
// here, in component state, and nowhere else.

export interface ProjectDraft {
	skip?: boolean | undefined;
	name?: string | undefined;
	/** A NebulaCtrl project to merge into; "" or unset creates a new one. */
	existingProjectId?: string | undefined;
}

export interface EnvDraft {
	secret?: boolean | undefined;
	skip?: boolean | undefined;
	/** Keep Coolify host names, ports and credentials: no reference rewrite. */
	keepCoolify?: boolean | undefined;
	/** A value for a variable Coolify did not return; write-only. */
	value?: string | undefined;
}

export interface DomainDraft {
	enabled?: boolean | undefined;
	host?: string | undefined;
}

export interface ResourceDraft {
	include?: boolean | undefined;
	name?: string | undefined;
	/** `0` makes an application a worker without a port. */
	port?: number | undefined;
	kind?: "app" | "worker" | undefined;
	engineVersion?: string | undefined;
	/** By the host name Coolify uses. */
	domains?: Record<string, DomainDraft> | undefined;
	env?: Record<string, EnvDraft> | undefined;
}

export interface RegistryDraft {
	/** Unset: the username the plan suggests. */
	username?: string | undefined;
	password?: string | undefined;
}

export interface ImportDraft {
	/** By the Coolify project uuid. */
	projects: Record<string, ProjectDraft>;
	/** By the Coolify resource uuid. */
	resources: Record<string, ResourceDraft>;
	/** By registry host. */
	registries: Record<string, RegistryDraft>;
	gitConnectionId: string;
}

export const EMPTY_DRAFT: ImportDraft = {
	projects: {},
	resources: {},
	registries: {},
	gitConnectionId: "",
};

type Patch<T> = { [K in keyof T]?: T[K] | undefined };

/** Sets fields of one entry; an `undefined` field is removed, and an empty entry goes away. */
function patchEntry<T extends object>(
	record: Readonly<Record<string, T>>,
	key: string,
	patch: Patch<T>,
): Record<string, T> {
	const next: Record<string, unknown> = {
		...(record[key] ?? {}),
		...patch,
	};
	for (const field of Object.keys(next)) {
		if (next[field] === undefined) delete next[field];
	}
	const out = { ...record };
	if (Object.keys(next).length === 0) delete out[key];
	else out[key] = next as T;
	return out;
}

export function patchProject(
	draft: ImportDraft,
	uuid: string,
	patch: Patch<ProjectDraft>,
): ImportDraft {
	return { ...draft, projects: patchEntry(draft.projects, uuid, patch) };
}

export function patchResource(
	draft: ImportDraft,
	uuid: string,
	patch: Patch<ResourceDraft>,
): ImportDraft {
	return { ...draft, resources: patchEntry(draft.resources, uuid, patch) };
}

/** Sets fields of one entry of a nested record; the record is dropped when it empties. */
function patchNested<T extends object>(
	draft: ImportDraft,
	uuid: string,
	field: "domains" | "env",
	key: string,
	patch: Patch<T>,
): ImportDraft {
	const current = (draft.resources[uuid]?.[field] ?? {}) as Record<string, T>;
	const next = patchEntry(current, key, patch);
	return patchResource(draft, uuid, {
		[field]: Object.keys(next).length === 0 ? undefined : next,
	});
}

export function patchEnv(
	draft: ImportDraft,
	uuid: string,
	key: string,
	patch: Patch<EnvDraft>,
): ImportDraft {
	return patchNested<EnvDraft>(draft, uuid, "env", key, patch);
}

export function patchDomain(
	draft: ImportDraft,
	uuid: string,
	coolifyHost: string,
	patch: Patch<DomainDraft>,
): ImportDraft {
	return patchNested<DomainDraft>(draft, uuid, "domains", coolifyHost, patch);
}

export function patchRegistry(
	draft: ImportDraft,
	host: string,
	patch: Patch<RegistryDraft>,
): ImportDraft {
	return { ...draft, registries: patchEntry(draft.registries, host, patch) };
}

// --- the request ---------------------------------------------------------------

function projectChoice(draft: ProjectDraft): CoolifyProjectChoice | undefined {
	if (draft.skip === true) return { skip: true };
	const out: CoolifyProjectChoice = {};
	const existing = draft.existingProjectId?.trim() ?? "";
	const name = draft.name?.trim() ?? "";
	if (existing !== "") out.existingProjectId = existing;
	else if (name !== "") out.name = name;
	return Object.keys(out).length === 0 ? undefined : out;
}

function envChoice(draft: EnvDraft): CoolifyEnvChoice | undefined {
	const out: CoolifyEnvChoice = {};
	if (draft.secret !== undefined) out.secret = draft.secret;
	if (draft.skip !== undefined) out.skip = draft.skip;
	if (draft.keepCoolify === true) out.rewrite = false;
	// Never an empty value: that would blank a variable instead of leaving it alone.
	if (draft.value !== undefined && draft.value !== "") out.value = draft.value;
	return Object.keys(out).length === 0 ? undefined : out;
}

function resourceChoice(
	draft: ResourceDraft,
): CoolifyResourceChoice | undefined {
	const out: CoolifyResourceChoice = {};
	if (draft.include !== undefined) out.include = draft.include;
	const name = draft.name?.trim() ?? "";
	if (name !== "") out.name = name;
	if (draft.port !== undefined && !Number.isNaN(draft.port)) {
		out.port = draft.port;
	}
	if (draft.kind !== undefined) out.kind = draft.kind;
	const version = draft.engineVersion?.trim() ?? "";
	if (version !== "") out.engineVersion = version;

	const domains: NonNullable<CoolifyResourceChoice["domains"]> = {};
	for (const [host, domain] of Object.entries(draft.domains ?? {})) {
		const entry: { enabled?: boolean; host?: string } = {};
		if (domain.enabled !== undefined) entry.enabled = domain.enabled;
		const replacement = domain.host?.trim() ?? "";
		if (replacement !== "") entry.host = replacement;
		if (Object.keys(entry).length > 0) domains[host] = entry;
	}
	if (Object.keys(domains).length > 0) out.domains = domains;

	const env: NonNullable<CoolifyResourceChoice["env"]> = {};
	for (const [key, variable] of Object.entries(draft.env ?? {})) {
		const entry = envChoice(variable);
		if (entry) env[key] = entry;
	}
	if (Object.keys(env).length > 0) out.env = env;

	return Object.keys(out).length === 0 ? undefined : out;
}

/**
 * The decisions as the API takes them, from what the reader typed: empty
 * entries and empty values are left out, a registry is sent only with both a
 * username and a password. `usernameHints` are the usernames the plan
 * suggested per registry host; an untouched username field is that hint.
 */
export function buildChoices(
	draft: ImportDraft,
	usernameHints: Readonly<Record<string, string>> = {},
): CoolifyChoices {
	const out: CoolifyChoices = {};

	const projects: NonNullable<CoolifyChoices["projects"]> = {};
	for (const [uuid, project] of Object.entries(draft.projects)) {
		const choice = projectChoice(project);
		if (choice) projects[uuid] = choice;
	}
	if (Object.keys(projects).length > 0) out.projects = projects;

	const resources: NonNullable<CoolifyChoices["resources"]> = {};
	for (const [uuid, resource] of Object.entries(draft.resources)) {
		const choice = resourceChoice(resource);
		if (choice) resources[uuid] = choice;
	}
	if (Object.keys(resources).length > 0) out.resources = resources;

	const registries: NonNullable<CoolifyChoices["registries"]> = {};
	for (const [host, registry] of Object.entries(draft.registries)) {
		const username = (registry.username ?? usernameHints[host] ?? "").trim();
		const password = registry.password ?? "";
		if (username !== "" && password !== "") {
			registries[host] = { username, password };
		}
	}
	if (Object.keys(registries).length > 0) out.registries = registries;

	const git = draft.gitConnectionId.trim();
	if (git !== "") out.gitConnectionId = git;
	return out;
}

/** What the Connect step holds. The token is only ever read from here, into `source`. */
export interface SourceDraft {
	url: string;
	token: string;
}

export const EMPTY_SOURCE: SourceDraft = { url: "", token: "" };

export function sourceReady(source: SourceDraft): boolean {
	return source.url.trim() !== "" && source.token.trim() !== "";
}

/** The Coolify address and token: the only place the token goes in a request. */
function requestSource(source: SourceDraft) {
	return { url: source.url.trim(), token: source.token.trim() };
}

export function buildPlanRequest(
	source: SourceDraft,
	choices: CoolifyChoices,
	serverId: string,
): CoolifyPlanRequest {
	return {
		source: requestSource(source),
		choices,
		...(serverId !== "" ? { serverId } : {}),
	};
}

export function buildApplyRequest(
	source: SourceDraft,
	choices: CoolifyChoices,
	serverId: string,
	deploy: boolean,
): CoolifyApplyRequest {
	return {
		source: requestSource(source),
		choices,
		serverId,
		...(deploy ? { deploy: true } : {}),
	};
}

/**
 * Whether two plan requests ask for the same plan. It stands for the request
 * wherever a comparison is needed (debounce, "is the plan on screen current")
 * and leaves the token out on purpose: the token is fixed for a plan, and
 * nothing that compares requests needs to hold it.
 */
export function requestFingerprint(request: CoolifyPlanRequest): string {
	return JSON.stringify({
		url: request.source.url,
		serverId: request.serverId ?? "",
		choices: request.choices,
	});
}

/** Takes secrets out of text before it is shown: an error must never echo one. */
export function redact(text: string, secrets: readonly string[]): string {
	let out = text;
	for (const secret of secrets) {
		const trimmed = secret.trim();
		if (trimmed !== "") out = out.split(trimmed).join("•••");
	}
	return out;
}

/**
 * Every secret the reader has typed: the token, the values supplied for
 * variables and the registry passwords. What an error message is scrubbed of.
 */
export function draftSecrets(
	source: SourceDraft,
	draft: ImportDraft,
): string[] {
	const out = [source.token];
	for (const resource of Object.values(draft.resources)) {
		for (const variable of Object.values(resource.env ?? {})) {
			if (variable.value) out.push(variable.value);
		}
	}
	for (const registry of Object.values(draft.registries)) {
		if (registry.password) out.push(registry.password);
	}
	return out.filter((secret) => secret.trim() !== "");
}

// --- what a plan says ---------------------------------------------------------

type Severity = "danger" | "warning" | "info";

const SEVERITY_RANK: Record<Severity, number> = {
	danger: 0,
	warning: 1,
	info: 2,
};

/** Danger first, then warning, then info; the order inside a severity stays. */
export function sortNotes(
	notes: readonly CoolifyNote[] | null | undefined,
): CoolifyNote[] {
	return [...(notes ?? [])]
		.map((note, index) => ({ note, index }))
		.sort(
			(a, b) =>
				(SEVERITY_RANK[a.note.severity] ?? 1) -
					(SEVERITY_RANK[b.note.severity] ?? 1) || a.index - b.index,
		)
		.map(({ note }) => note);
}

export interface NotesBySeverity {
	danger: CoolifyNote[];
	warning: CoolifyNote[];
	info: CoolifyNote[];
}

export function notesBySeverity(
	notes: readonly CoolifyNote[] | null | undefined,
): NotesBySeverity {
	const out: NotesBySeverity = { danger: [], warning: [], info: [] };
	for (const note of notes ?? [])
		(out[note.severity] ?? out.warning).push(note);
	return out;
}

export interface ChecksSummary {
	fail: number;
	warn: number;
	ok: number;
	/** Failures first, then warnings, then passes. */
	rows: CoolifyCheck[];
}

const CHECK_RANK: Record<CoolifyCheck["status"], number> = {
	fail: 0,
	warn: 1,
	ok: 2,
};

export function summarizeChecks(
	checks: readonly CoolifyCheck[] | null | undefined,
): ChecksSummary {
	const rows = [...(checks ?? [])]
		.map((check, index) => ({ check, index }))
		.sort(
			(a, b) =>
				(CHECK_RANK[a.check.status] ?? 1) - (CHECK_RANK[b.check.status] ?? 1) ||
				a.index - b.index,
		)
		.map(({ check }) => check);
	return {
		fail: rows.filter((check) => check.status === "fail").length,
		warn: rows.filter((check) => check.status === "warn").length,
		ok: rows.filter((check) => check.status === "ok").length,
		rows,
	};
}

function plural(count: number, one: string, many: string): string {
	return `${count} ${count === 1 ? one : many}`;
}

/** "2 projects · 5 services · 1 database · 14 variables (3 secret) · 4 domains". */
export function totalsText(totals: CoolifyTotals): string {
	return [
		plural(totals.projects, "project", "projects"),
		plural(totals.services, "service", "services"),
		plural(totals.databases, "database", "databases"),
		`${plural(totals.variables, "variable", "variables")}${
			totals.secrets > 0 ? ` (${totals.secrets} secret)` : ""
		}`,
		plural(totals.domains, "domain", "domains"),
	].join(" · ");
}

export function isProjectSkipped(
	project: CoolifyPlanProject,
	draft: ImportDraft,
): boolean {
	return draft.projects[project.uuid]?.skip ?? project.action === "skip";
}

/** The reader's choice over the plan's default. */
export function isIncluded(
	resource: Pick<CoolifyPlanResource, "uuid" | "include">,
	draft: ImportDraft,
): boolean {
	return draft.resources[resource.uuid]?.include ?? resource.include;
}

export interface EnvRowState {
	secret: boolean;
	skipped: boolean;
	keepCoolify: boolean;
	/** The reader typed a value for it. */
	supplied: boolean;
	needsValue: boolean;
}

/** What a variable row shows: the reader's choice over the plan's answer. */
export function envRowState(
	row: CoolifyPlanEnv,
	draft: EnvDraft | undefined,
): EnvRowState {
	const supplied = (draft?.value ?? "") !== "";
	return {
		secret: draft?.secret ?? row.secret,
		skipped: draft?.skip ?? row.skipped ?? false,
		keepCoolify: draft?.keepCoolify === true,
		supplied,
		needsValue: (row.needsValue ?? false) && !supplied,
	};
}

/** The resources of a project, environment by environment. */
function projectResources(project: CoolifyPlanProject): CoolifyPlanResource[] {
	return (project.environments ?? []).flatMap(
		(environment) => environment.resources ?? [],
	);
}

export interface PlanSummary {
	projects: number;
	apps: number;
	workers: number;
	databases: number;
	/** Resources that are in the plan but left out. */
	excluded: number;
	variables: number;
	secrets: number;
	domains: number;
	/** Variables that still wait for a value. */
	needValue: number;
	rewrites: number;
}

/**
 * Counts for the line above the cards, from what is on screen: the plan with
 * the reader's include, skip and value choices laid over it. A skipped
 * project, and what is in it, adds nothing.
 */
export function summarizePlan(
	plan: CoolifyPlan,
	draft: ImportDraft = EMPTY_DRAFT,
): PlanSummary {
	const out: PlanSummary = {
		projects: 0,
		apps: 0,
		workers: 0,
		databases: 0,
		excluded: 0,
		variables: 0,
		secrets: 0,
		domains: 0,
		needValue: 0,
		rewrites: (plan.rewrites ?? []).length,
	};
	for (const project of plan.projects ?? []) {
		if (isProjectSkipped(project, draft)) continue;
		out.projects++;
		for (const environment of project.environments ?? []) {
			if (environment.action === "skip") continue;
			for (const resource of environment.resources ?? []) {
				if (!isIncluded(resource, draft)) {
					out.excluded++;
					continue;
				}
				if (resource.kind === "database") out.databases++;
				else if (resource.kind === "worker") out.workers++;
				else out.apps++;
				for (const row of resource.env ?? []) {
					const state = envRowState(
						row,
						draft.resources[resource.uuid]?.env?.[row.key],
					);
					if (state.skipped) continue;
					out.variables++;
					if (state.secret) out.secrets++;
					if (state.needsValue) out.needValue++;
				}
				for (const domain of resource.domains ?? []) {
					const choice = draft.resources[resource.uuid]?.domains;
					if (domain.enabled || choice?.[domain.host]?.enabled === true) {
						out.domains++;
					}
				}
			}
		}
	}
	return out;
}

/** "3 services, 1 database included, 2 left out". */
export function includedText(summary: PlanSummary): string {
	const services = summary.apps + summary.workers;
	const parts = [
		plural(services, "service", "services"),
		plural(summary.databases, "database", "databases"),
	];
	const text = `${parts.join(", ")} included`;
	return summary.excluded > 0 ? `${text}, ${summary.excluded} left out` : text;
}

/**
 * The included resources that use Docker options only an unrestricted server
 * accepts, as "name: option, option".
 */
export function unrestrictedNeeds(
	plan: CoolifyPlan,
	draft: ImportDraft = EMPTY_DRAFT,
): string[] {
	const out: string[] = [];
	for (const project of plan.projects ?? []) {
		if (isProjectSkipped(project, draft)) continue;
		for (const resource of projectResources(project)) {
			const needs = resource.needsUnrestricted ?? [];
			if (needs.length > 0 && isIncluded(resource, draft)) {
				out.push(`${resource.name}: ${needs.join(", ")}`);
			}
		}
	}
	return out;
}

const SECRET_REASON: Record<string, string> = {
	locked: "locked in Coolify",
	name: "by its name",
	value: "by its value",
	reference: "references a secret",
	choice: "your choice",
};

/** Why a variable is a secret, in words; "" when the plan gave no reason. */
export function secretReasonText(reason: string | undefined): string {
	if (!reason) return "";
	return SECRET_REASON[reason] ?? reason;
}

/** The rewritten variables of a resource, by key, with the reference each became. */
export function rewritesFor(
	rewrites: readonly CoolifyPlanRewrite[] | null | undefined,
	resourceName: string,
): Map<string, CoolifyPlanRewrite> {
	const out = new Map<string, CoolifyPlanRewrite>();
	for (const rewrite of rewrites ?? []) {
		if (rewrite.resource === resourceName) out.set(rewrite.key, rewrite);
	}
	return out;
}

export interface RewriteGroup {
	resource: string;
	items: CoolifyPlanRewrite[];
}

/** The rewritten references per resource, resources in the order they first appear. */
export function groupRewrites(
	rewrites: readonly CoolifyPlanRewrite[] | null | undefined,
): RewriteGroup[] {
	const groups = new Map<string, RewriteGroup>();
	for (const rewrite of rewrites ?? []) {
		const group = groups.get(rewrite.resource) ?? {
			resource: rewrite.resource,
			items: [],
		};
		group.items.push(rewrite);
		groups.set(rewrite.resource, group);
	}
	return [...groups.values()];
}

const ENGINE_LABEL: Record<string, string> = {
	postgres: "PostgreSQL",
	mysql: "MySQL",
	mariadb: "MariaDB",
	redis: "Redis",
	valkey: "Valkey",
	mongodb: "MongoDB",
	clickhouse: "ClickHouse",
};

function engineLabel(engine: string): string {
	return ENGINE_LABEL[engine] ?? engine;
}

/** The image reference as the row shows it: no digest. */
function shortImage(image: string): string {
	return (image.split("@")[0] ?? image).replace(
		/^(docker\.io|registry-1\.docker\.io)\//,
		"",
	);
}

/** One line for where a resource comes from: image, git url@branch (build), or managed engine. */
export function sourceLine(source: CoolifyPlanSource): string {
	if (source.type === "git" && source.git) {
		const { url, branch, buildType } = source.git;
		const at = branch !== "" ? `${url}@${branch}` : url;
		return buildType !== "" ? `${at} (${buildType})` : at;
	}
	if (source.type === "managed" && source.managed) {
		const { engine, version, flavor } = source.managed;
		return [
			"Managed",
			engineLabel(engine),
			version,
			flavor ? `(${flavor})` : "",
		]
			.filter((part) => part !== "")
			.join(" ");
	}
	return source.image ? shortImage(source.image) : "image";
}

// --- the Coolify names the plan no longer carries -----------------------------

/**
 * What the first plan, made with no choices, knew: the Coolify names of the
 * projects and the host names of each resource's domains. A later plan shows a
 * renamed or merged project under its new name and a changed domain under its
 * new host, but the choices are keyed by what Coolify calls them.
 */
export interface Baseline {
	projectNames: Record<string, string>;
	domainHosts: Record<string, string[]>;
}

const COLLISION_SUFFIX = " (Coolify)";

export function baselineOf(plan: CoolifyPlan): Baseline {
	const out: Baseline = { projectNames: {}, domainHosts: {} };
	for (const project of plan.projects ?? []) {
		out.projectNames[project.uuid] =
			project.collision && project.name.endsWith(COLLISION_SUFFIX)
				? project.name.slice(0, -COLLISION_SUFFIX.length)
				: project.name;
		for (const resource of projectResources(project)) {
			out.domainHosts[resource.uuid] = (resource.domains ?? []).map(
				(domain) => domain.host,
			);
		}
	}
	return out;
}

/**
 * The host name Coolify uses for the nth domain of a resource, or undefined
 * when the list no longer lines up with the first plan (then it cannot be
 * edited, rather than editing the wrong domain).
 */
export function coolifyHostAt(
	baseline: Baseline | undefined,
	resourceUuid: string,
	index: number,
	count: number,
): string | undefined {
	const hosts = baseline?.domainHosts[resourceUuid];
	if (!hosts || hosts.length !== count) return undefined;
	return hosts[index];
}

export function coolifyProjectName(
	baseline: Baseline | undefined,
	project: CoolifyPlanProject,
): string {
	return baseline?.projectNames[project.uuid] ?? project.name;
}

/** Why a new project is called what it is: the name was taken. */
export function collisionHint(
	project: CoolifyPlanProject,
	draft: ProjectDraft | undefined,
): string | null {
	if (project.action !== "create" || !project.collision) return null;
	if ((draft?.name?.trim() ?? "") !== "") {
		return `That name is taken. The project is created as ${project.name}, slug ${project.slug}.`;
	}
	return `Name taken → will be created as ${project.name}.`;
}

/** The usernames the plan suggests per registry host. */
export function usernameHints(
	plan: CoolifyPlan | undefined,
): Record<string, string> {
	const out: Record<string, string> = {};
	for (const registry of plan?.registries ?? []) {
		if (registry.username) out[registry.host] = registry.username;
	}
	return out;
}

// --- when Import is available --------------------------------------------------

/** Why Import is not available yet, or null when it is. */
export function importBlocker(input: {
	plan: CoolifyPlan | undefined;
	serverId: string;
	/** The plan on screen answers the choices on screen, and no request is in flight. */
	fresh: boolean;
	/** The latest plan request failed. */
	failed: boolean;
}): string | null {
	if (!input.plan) return "Read Coolify first.";
	if (input.serverId === "") return "Choose a server on the first step.";
	if (input.failed)
		return "The plan could not be updated. Change a choice to try again.";
	if (!input.fresh) return "Checking the plan…";
	if (!input.plan.installable) {
		return (
			input.plan.blockers?.[0] ?? "The plan has problems; fix them to import."
		);
	}
	if (input.plan.totals.services + input.plan.totals.databases === 0) {
		return "Nothing is included.";
	}
	return null;
}

// --- the result ------------------------------------------------------------------

export interface OpenTarget {
	projectId: string;
	envSlug: string;
	name: string;
}

/**
 * The project the "Open" button goes to: the project of the first imported
 * service (with its environment), else the first project that was created or
 * merged into, with its first environment.
 */
export function firstTarget(result: CoolifyApplyResult): OpenTarget | null {
	const projects = result.projects ?? [];
	const service = (result.services ?? [])[0];
	if (service) {
		const project = projects.find((p) => p.id === service.projectId);
		return {
			projectId: service.projectId,
			envSlug: service.envSlug,
			name: project?.name ?? service.projectSlug,
		};
	}
	for (const project of projects) {
		const environment = (project.environments ?? [])[0];
		if (environment) {
			return {
				projectId: project.id,
				envSlug: environment.slug,
				name: project.name,
			};
		}
	}
	return null;
}

/** "Created 2 projects, 7 services; 1 deploying". */
export function resultText(result: CoolifyApplyResult): string {
	const projects = result.projects ?? [];
	const created = projects.filter((p) => p.created).length;
	const merged = projects.length - created;
	const parts = [
		created > 0 ? plural(created, "project", "projects") : null,
		merged > 0 ? `${plural(merged, "project", "projects")} merged into` : null,
		plural((result.services ?? []).length, "service", "services"),
	].filter((part): part is string => part !== null);
	const deploying = (result.deployments ?? []).length;
	return `${parts.join(", ")}${deploying > 0 ? `; ${deploying} deploying` : "; nothing deployed"}`;
}

export type FollowupSegment = { kind: "text" | "code"; text: string };

// Shell commands the importer puts inside its follow-up sentences: a command
// in parentheses (the parentheses belong to the sentence, not the command), one
// after "run: ", one after "with ". Group 1 is the command; `wrapped` patterns
// are cut out whole, parentheses included.
const COMMAND_PATTERNS: ReadonlyArray<{ pattern: RegExp; wrapped: boolean }> = [
	{
		pattern:
			/\((?:for example )?((?:docker|mysqldump|mongodump|mongorestore|nebulactrl) [^)]*[^\s)])\)/g,
		wrapped: true,
	},
	{ pattern: /\brun: (nebulactrl [^()]*?)(?= \(see |\.?$)/g, wrapped: false },
	{ pattern: /\bwith (nebulactrl [^.]*?)(?=\.?$)/g, wrapped: false },
];

/**
 * A follow-up sentence cut into prose and commands, so the commands can be
 * shown as code and copied. A command that stood in parentheses loses them,
 * and a piece of prose loses the comma or space the cut left at its edge. A
 * sentence with no command is one text segment.
 */
export function followupSegments(text: string): FollowupSegment[] {
	const spans: Array<{ start: number; end: number; command: string }> = [];
	for (const { pattern, wrapped } of COMMAND_PATTERNS) {
		for (const match of text.matchAll(pattern)) {
			const command = match[1];
			if (command === undefined) continue;
			const inner = match.index + match[0].indexOf(command);
			spans.push({
				start: wrapped ? match.index : inner,
				end: wrapped ? match.index + match[0].length : inner + command.length,
				command,
			});
		}
	}
	spans.sort((a, b) => a.start - b.start);
	const out: FollowupSegment[] = [];
	const prose = (piece: string) => {
		const cleaned = piece.replace(/^[,;]\s*/, "").trim();
		if (cleaned !== "") out.push({ kind: "text", text: cleaned });
	};
	let cursor = 0;
	for (const span of spans) {
		if (span.start < cursor) continue;
		prose(text.slice(cursor, span.start));
		out.push({ kind: "code", text: span.command });
		cursor = span.end;
	}
	prose(text.slice(cursor));
	return out;
}
