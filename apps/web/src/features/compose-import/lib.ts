import type {
	ComposeCheck,
	ComposeChoices,
	ComposeEnvChoice,
	ComposeImportRequest,
	ComposeNote,
	ComposePlan,
	ComposePlanBind,
	ComposePlanEnv,
	ComposePlanHealth,
	ComposePlanPort,
	ComposePlanRequest,
	ComposePlanService,
	ComposeRepoRef,
	ComposeRewrite,
	ComposeServiceChoice,
	ComposeSource,
	ComposeStackComponent,
	ComposeStackPlan,
} from "#/api/gen/types.gen";
import { type ChecksSummary, databaseLabel } from "#/features/templates/lib";

// Everything here is pure: the wizard components only draw what these return.
// The plan is stateless on the server, so the client keeps two things, the
// source text and the choices, and sends both again after every change.

// --- the source ---------------------------------------------------------------

export type SourceMode = "paste" | "upload" | "git";

/** A file picked on the Upload tab that is not the Compose file or the .env. */
export interface UploadedFile {
	id: string;
	/** The file's own name, e.g. `nginx.conf`. */
	name: string;
	/** The path as written in the Compose file; only used once the reader edited it. */
	path: string;
	/** The reader typed the path, so it is never replaced by a guess. */
	pathEdited: boolean;
	content: string;
}

export type GitSourceMode = "account" | "url";

export interface GitDraft {
	mode: GitSourceMode;
	connectionId: string;
	/** owner/name of a connected repository. */
	repo: string;
	/** A public github.com or gitlab.com repository. */
	url: string;
	branch: string;
	/** The Compose file in the repository; empty finds the default. */
	path: string;
}

/** What the import makes of the file: one service per Compose service, or one stack. */
export type ImportMode = "split" | "stack";
export interface SourceDraft {
	mode: SourceMode;
	compose: string;
	/** Name of the uploaded Compose file, for the Upload tab. */
	composeName: string;
	/** The contents of `.env`. */
	env: string;
	envName: string;
	uploads: UploadedFile[];
	/** Needed files the reader pasted, by the path written in the Compose file. */
	files: Record<string, string>;
	/** The group title; empty lets the server name the group. */
	name: string;
	git: GitDraft;
	/** Split into one service per Compose service, or run the file as one stack. */
	importMode: ImportMode;
}

export function emptySource(): SourceDraft {
	return {
		mode: "paste",
		compose: "",
		composeName: "",
		env: "",
		envName: "",
		uploads: [],
		files: {},
		importMode: "split",
		name: "",
		git: {
			mode: "account",
			connectionId: "",
			repo: "",
			url: "",
			branch: "",
			path: "",
		},
	};
}

/** Whether Continue can run the first plan. */
export function sourceReady(draft: SourceDraft): boolean {
	if (draft.mode === "git") {
		return draft.git.mode === "url"
			? draft.git.url.trim() !== ""
			: draft.git.connectionId !== "" && draft.git.repo.trim() !== "";
	}
	return draft.compose.trim() !== "";
}

/** What picking a file on the Upload tab does with it. */
export type UploadRole = "compose" | "env" | "extra";

const COMPOSE_NAMES = /^(docker-)?compose(\.[\w-]+)?\.ya?ml$/i;

/**
 * `docker-compose.yml`, `compose.yaml` and the override variants are the
 * Compose file; `.env` is the interpolation file; a lone `.yml` is taken as
 * the Compose file when none was chosen yet; everything else is a file the
 * Compose file references.
 */
export function classifyUpload(name: string, hasCompose: boolean): UploadRole {
	const base = basename(name);
	if (base === ".env") return "env";
	if (COMPOSE_NAMES.test(base)) return "compose";
	if (!hasCompose && /\.ya?ml$/i.test(base)) return "compose";
	return "extra";
}

export function basename(path: string): string {
	const trimmed = path.replace(/\/+$/, "");
	return trimmed.slice(trimmed.lastIndexOf("/") + 1);
}

/** `./nginx.conf` for a file that was not matched to anything. */
export function defaultUploadPath(name: string): string {
	return `./${basename(name)}`;
}

/**
 * Every path as written in the Compose file the plan has mentioned so far:
 * needed files and files already attached. It only grows, so an upload that
 * was matched to a needed file stays matched once the plan stops asking for it.
 */
export function mergeKnownPaths(
	known: readonly string[],
	plan: Pick<ComposePlan, "neededFiles" | "services"> | undefined,
): string[] {
	if (!plan) return [...known];
	const next = new Set(known);
	for (const needed of plan.neededFiles ?? []) next.add(needed.path);
	for (const service of plan.services ?? []) {
		for (const file of service.files ?? []) next.add(file.source);
	}
	return next.size === known.length ? [...known] : [...next];
}

/** Where an uploaded file goes: the reader's path, else a needed path with the same file name. */
export function uploadPath(
	upload: UploadedFile,
	knownPaths: readonly string[],
): string {
	if (upload.pathEdited) return upload.path;
	const name = basename(upload.name);
	return (
		knownPaths.find((path) => basename(path) === name) ??
		defaultUploadPath(upload.name)
	);
}

/** Uploaded files by path, with what was pasted for a needed file on top. */
export function effectiveFiles(
	draft: Pick<SourceDraft, "uploads" | "files">,
	knownPaths: readonly string[],
): Record<string, string> {
	const files: Record<string, string> = {};
	for (const upload of draft.uploads) {
		const path = uploadPath(upload, knownPaths).trim();
		if (path !== "") files[path] = upload.content;
	}
	for (const [path, content] of Object.entries(draft.files)) {
		if (content !== "") files[path] = content;
	}
	return files;
}

function repoRef(git: GitDraft): ComposeRepoRef {
	const ref: ComposeRepoRef = {};
	if (git.mode === "url") ref.url = git.url.trim();
	else {
		ref.connectionId = git.connectionId;
		ref.repo = git.repo.trim();
	}
	if (git.branch.trim() !== "") ref.branch = git.branch.trim();
	if (git.path.trim() !== "") ref.path = git.path.trim();
	return ref;
}

/**
 * The `source` of a plan or import request. The only text that leaves the
 * browser is what the reader gave: the Compose file, the .env and the files.
 */
export function buildSource(
	draft: SourceDraft,
	knownPaths: readonly string[],
): ComposeSource {
	const files = effectiveFiles(draft, knownPaths);
	const source: ComposeSource = {};
	if (draft.mode === "git") source.repo = repoRef(draft.git);
	else source.compose = draft.compose;
	if (draft.env.trim() !== "") source.env = draft.env;
	if (Object.keys(files).length > 0) source.files = files;
	if (draft.name.trim() !== "") source.name = draft.name.trim();
	return source;
}

export function buildPlanRequest(
	draft: SourceDraft,
	choices: ComposeChoices,
	serverId: string,
	knownPaths: readonly string[],
): ComposePlanRequest {
	return {
		source: buildSource(draft, knownPaths),
		choices,
		...(serverId !== "" ? { serverId } : {}),
		...(draft.importMode === "stack" ? { mode: "stack" as const } : {}),
	};
}

export function buildImportRequest(
	draft: SourceDraft,
	choices: ComposeChoices,
	serverId: string,
	knownPaths: readonly string[],
	deploy: boolean,
): ComposeImportRequest {
	return {
		source: buildSource(draft, knownPaths),
		choices,
		serverId,
		...(draft.importMode === "stack" ? { mode: "stack" as const } : {}),
		...(deploy ? { deploy: true } : {}),
	};
}

// --- choices ------------------------------------------------------------------

export const NO_CHOICES: ComposeChoices = {};

type ServiceChoices = Record<string, ComposeServiceChoice>;

/** Drops unset fields and empty maps; an empty list is kept where it means "none". */
function prune(choice: ComposeServiceChoice): ComposeServiceChoice {
	const next: Record<string, unknown> = {};
	for (const [key, value] of Object.entries(choice)) {
		if (value === undefined) continue;
		if (
			key === "env" &&
			value !== null &&
			typeof value === "object" &&
			Object.keys(value).length === 0
		) {
			continue;
		}
		next[key] = value;
	}
	return next as ComposeServiceChoice;
}

function patchService(
	choices: ComposeChoices,
	key: string,
	change: (current: ComposeServiceChoice) => ComposeServiceChoice,
): ComposeChoices {
	const next = prune(change({ ...(choices.services?.[key] ?? {}) }));
	const services: ServiceChoices = { ...(choices.services ?? {}) };
	if (Object.keys(next).length === 0) delete services[key];
	else services[key] = next;
	return Object.keys(services).length === 0 ? {} : { services };
}

/** `{ a: 1, b: undefined }` → `{ a: 1 }`, typed for an optional field. */
function without<T extends object>(value: T, key: keyof T): T {
	const copy = { ...value };
	delete copy[key];
	return copy;
}

export function setInclude(
	choices: ComposeChoices,
	key: string,
	include: boolean,
): ComposeChoices {
	return patchService(choices, key, (c) => ({ ...c, include }));
}

export function setServiceName(
	choices: ComposeChoices,
	key: string,
	name: string,
): ComposeChoices {
	return patchService(choices, key, (c) =>
		name.trim() === "" ? without(c, "name") : { ...c, name },
	);
}

export function setPort(
	choices: ComposeChoices,
	key: string,
	port: number | undefined,
): ComposeChoices {
	return patchService(choices, key, (c) =>
		port === undefined ? without(c, "port") : { ...c, port },
	);
}

export function setSourceKind(
	choices: ComposeChoices,
	key: string,
	source: "image" | "git" | undefined,
): ComposeChoices {
	return patchService(choices, key, (c) =>
		source === undefined ? without(c, "source") : { ...c, source },
	);
}

export function setImage(
	choices: ComposeChoices,
	key: string,
	image: string,
): ComposeChoices {
	return patchService(choices, key, (c) =>
		image.trim() === "" ? without(c, "image") : { ...c, image },
	);
}

export interface GitChoice {
	gitUrl?: string;
	gitBranch?: string;
	connectionId?: string;
	repo?: string;
}

/**
 * Where a service is built from. The URL and the connection plus repository
 * are alternatives, so setting one clears the other.
 */
export function setServiceGit(
	choices: ComposeChoices,
	key: string,
	git: GitChoice,
): ComposeChoices {
	return patchService(choices, key, (c) => {
		const next = { ...c };
		delete next.gitUrl;
		delete next.gitBranch;
		delete next.connectionId;
		delete next.repo;
		if (git.gitUrl && git.gitUrl !== "") next.gitUrl = git.gitUrl;
		if (git.connectionId && git.connectionId !== "") {
			next.connectionId = git.connectionId;
		}
		if (git.repo && git.repo !== "") next.repo = git.repo;
		if (git.gitBranch && git.gitBranch !== "") next.gitBranch = git.gitBranch;
		return next;
	});
}

export function setManaged(
	choices: ComposeChoices,
	key: string,
	managedDatabase: boolean,
): ComposeChoices {
	return patchService(choices, key, (c) => ({ ...c, managedDatabase }));
}

export function setDomain(
	choices: ComposeChoices,
	key: string,
	domain: { enabled: boolean; host?: string } | undefined,
): ComposeChoices {
	return patchService(choices, key, (c) => {
		if (domain === undefined) return without(c, "domain");
		const host = domain.host?.trim() ?? "";
		return {
			...c,
			domain:
				domain.enabled && host !== ""
					? { enabled: true, host }
					: { enabled: domain.enabled },
		};
	});
}

/** `5432/tcp`: the container port of a published port, as the API names it. */
export function portKey(port: Pick<ComposePlanPort, "port" | "protocol">) {
	return `${port.port}/${port.protocol}`;
}

/** Ports that can become a TCP/UDP route: everything but the HTTP port behind a domain. */
export function routablePorts(service: ComposePlanService): ComposePlanPort[] {
	return (service.ports ?? []).filter((port) => port.role !== "http");
}

/** The ports published now: the reader's list, else what the plan chose. */
export function publishedKeys(
	service: ComposePlanService,
	choices: ComposeChoices,
): string[] {
	return (
		choices.services?.[service.key]?.publishPorts ??
		routablePorts(service)
			.filter((port) => port.publish)
			.map(portKey)
	);
}

export function setPublish(
	choices: ComposeChoices,
	service: ComposePlanService,
	key: string,
	on: boolean,
): ComposeChoices {
	const current = publishedKeys(service, choices).filter((k) => k !== key);
	const publishPorts = on ? [...current, key] : current;
	return patchService(choices, service.key, (c) => ({ ...c, publishPorts }));
}

/** The directory bind mounts that become named volumes now. */
export function convertedTargets(
	service: ComposePlanService,
	choices: ComposeChoices,
): string[] {
	return (
		choices.services?.[service.key]?.convertBinds ??
		(service.binds ?? [])
			.filter((bind) => bind.status === "converted")
			.map((bind) => bind.target)
	);
}

export function setConvert(
	choices: ComposeChoices,
	service: ComposePlanService,
	target: string,
	on: boolean,
): ComposeChoices {
	const current = convertedTargets(service, choices).filter(
		(t) => t !== target,
	);
	const convertBinds = on ? [...current, target] : current;
	return patchService(choices, service.key, (c) => ({ ...c, convertBinds }));
}

export function setEnvChoice(
	choices: ComposeChoices,
	key: string,
	envKey: string,
	patch: {
		secret?: boolean | null;
		skip?: boolean | null;
		rewrite?: boolean | null;
	},
): ComposeChoices {
	return patchService(choices, key, (c) => {
		const current: ComposeEnvChoice = { ...(c.env?.[envKey] ?? {}) };
		const next: ComposeEnvChoice = { ...current };
		// null removes the choice, so the plan's own default applies again.
		if (patch.secret === null) delete next.secret;
		else if (patch.secret !== undefined) next.secret = patch.secret;
		if (patch.skip === null || patch.skip === false) delete next.skip;
		else if (patch.skip !== undefined) next.skip = patch.skip;
		if (patch.rewrite === null || patch.rewrite === true) delete next.rewrite;
		else if (patch.rewrite !== undefined) next.rewrite = patch.rewrite;
		const env = { ...(c.env ?? {}) };
		if (Object.keys(next).length === 0) delete env[envKey];
		else env[envKey] = next;
		return { ...c, env };
	});
}

/** What an environment row shows: the reader's choice over the plan's default. */
export interface EnvRowState {
	secret: boolean;
	skipped: boolean;
	/** The server skipped it on its own; the reader cannot turn it back on. */
	skippedByPlan: boolean;
	rewritten: boolean;
	/** Offer "Keep original host names": the row was rewritten, or the reader turned it off. */
	canKeepHosts: boolean;
	keepHosts: boolean;
}

export function envRowState(
	env: ComposePlanEnv,
	choice: ComposeEnvChoice | undefined,
): EnvRowState {
	const keepHosts = choice?.rewrite === false;
	return {
		secret: choice?.secret ?? env.secret,
		skipped: choice?.skip ?? env.skipped,
		skippedByPlan: env.skipped && choice?.skip !== true,
		rewritten: env.rewritten,
		canKeepHosts: env.rewritten || keepHosts,
		keepHosts,
	};
}

export function isIncluded(
	service: ComposePlanService,
	choices: ComposeChoices,
): boolean {
	return choices.services?.[service.key]?.include ?? service.include;
}

export function isManagedOn(
	service: ComposePlanService,
	choices: ComposeChoices,
): boolean {
	return (
		choices.services?.[service.key]?.managedDatabase ??
		service.managed?.enabled ??
		false
	);
}

export function domainEnabled(
	service: ComposePlanService,
	choices: ComposeChoices,
): boolean {
	return (
		choices.services?.[service.key]?.domain?.enabled ??
		(service.domains ?? []).some((domain) => domain.enabled)
	);
}

// --- reading a plan -----------------------------------------------------------

export interface NotesBySeverity {
	danger: ComposeNote[];
	warning: ComposeNote[];
	info: ComposeNote[];
}

export function notesBySeverity(
	notes: readonly ComposeNote[] | null | undefined,
): NotesBySeverity {
	const out: NotesBySeverity = { danger: [], warning: [], info: [] };
	for (const note of notes ?? []) {
		(out[note.severity] ?? out.info).push(note);
	}
	return out;
}

export interface RewriteGroup {
	/** The Compose service key. */
	service: string;
	items: ComposeRewrite[];
}

/** The rewritten references per service, services in the order they first appear. */
export function groupRewrites(
	rewrites: readonly ComposeRewrite[] | null | undefined,
): RewriteGroup[] {
	const groups = new Map<string, RewriteGroup>();
	for (const rewrite of rewrites ?? []) {
		const group = groups.get(rewrite.service);
		if (group) group.items.push(rewrite);
		else
			groups.set(rewrite.service, {
				service: rewrite.service,
				items: [rewrite],
			});
	}
	return [...groups.values()];
}

export interface PlanSummary {
	services: number;
	total: number;
	rewrites: number;
	warnings: number;
	blockers: number;
}

/** Counts for the line above the cards; only included services add to the warnings. */
export function summarizePlan(
	plan: ComposePlan,
	choices: ComposeChoices = NO_CHOICES,
): PlanSummary {
	const all = plan.services ?? [];
	const included = all.filter((service) => isIncluded(service, choices));
	const noteWarnings = (notes: readonly ComposeNote[] | null) =>
		(notes ?? []).filter((note) => note.severity !== "info").length;
	return {
		services: included.length,
		total: all.length,
		rewrites: (plan.rewrites ?? []).length,
		warnings:
			(plan.warnings ?? []).length +
			noteWarnings(plan.notes) +
			included.reduce((sum, service) => sum + noteWarnings(service.notes), 0),
		blockers: (plan.blockers ?? []).length,
	};
}

const plural = (count: number, one: string, many: string) =>
	`${count} ${count === 1 ? one : many}`;

/** "3 services, 2 rewritten references, 1 warning". */
export function summaryText(summary: PlanSummary): string {
	return [
		plural(summary.services, "service", "services"),
		plural(summary.rewrites, "rewritten reference", "rewritten references"),
		plural(summary.warnings, "warning", "warnings"),
	].join(", ");
}

/** Failures first, then warnings, then passes; every check has a row. */
export function summarizeChecks(
	checks: readonly ComposeCheck[] | null | undefined,
): ChecksSummary {
	const order: Record<string, number> = { fail: 0, warn: 1, ok: 2 };
	const rows = (checks ?? [])
		.map((check, index) => ({ check, index }))
		.sort(
			(a, b) =>
				(order[a.check.status] ?? 3) - (order[b.check.status] ?? 3) ||
				a.index - b.index,
		)
		.map(({ check }) => check);
	const count = (status: string) =>
		rows.filter((row) => row.status === status).length;
	return { fail: count("fail"), warn: count("warn"), ok: count("ok"), rows };
}

/** "Stage 3 services". */
export function stageLabel(count: number): string {
	return `Stage ${plural(count, "service", "services")}`;
}

export function stagedMessage(count: number): string {
	return `Staged ${plural(count, "service", "services")}. Deploy them from the staged bar.`;
}

/** Why Stage is not available yet, or null when it is. */
export function stageBlocker(input: {
	plan: ComposePlan | undefined;
	included: number;
	serverId: string;
	fresh: boolean;
}): string | null {
	const { plan } = input;
	if (!plan) return "Review the plan first.";
	if (input.serverId === "") return "Choose a server.";
	if (!input.fresh) return "Checking the plan.";
	if (input.included === 0) return "Include at least one service.";
	if (!plan.installable) {
		const first = (plan.blockers ?? [])[0];
		return first ?? "Fix what blocks the import to continue.";
	}
	return null;
}

// --- one service card ---------------------------------------------------------

/** The image reference as the card shows it: no registry host, no digest. */
export function shortImage(image: string): string {
	const withoutDigest = image.split("@")[0] ?? image;
	return withoutDigest.replace(/^(ghcr\.io|docker\.io|quay\.io)\//, "");
}

export interface SourceChip {
	kind: "managed" | "git" | "image";
	text: string;
}

/** "Managed PostgreSQL 16", "Build from git" or the image tag. */
export function sourceChip(
	service: ComposePlanService,
	choices: ComposeChoices = NO_CHOICES,
): SourceChip {
	if (isManagedOn(service, choices) && service.managed) {
		const engine = [service.managed.engine, service.managed.version]
			.filter(Boolean)
			.join(" ");
		return { kind: "managed", text: `Managed ${databaseLabel(engine)}`.trim() };
	}
	const choice = choices.services?.[service.key];
	if (choice?.source === "image" || service.source.type === "image") {
		const image = choice?.image ?? service.source.image ?? "";
		return {
			kind: "image",
			text: image === "" ? "No image" : shortImage(image),
		};
	}
	return { kind: "git", text: "Build from git" };
}

/** The service takes a git URL (or connection) from the reader. */
export function needsGit(service: ComposePlanService): boolean {
	return (service.needs ?? []).includes("gitUrl");
}

/** Show the git/image switch: the service builds from git, or the reader switched it to an image. */
export function canSwitchSource(
	service: ComposePlanService,
	choices: ComposeChoices,
): boolean {
	return (
		service.source.type === "git" ||
		choices.services?.[service.key]?.source === "image"
	);
}

/** "http-get /health every 10s": what the check does, in one line. */
export function healthSummary(
	health: ComposePlanHealth | undefined,
): string | null {
	if (!health || health.type === "" || health.type === "none") return null;
	const command = (health.command ?? []).join(" ");
	const parts: string[] = [health.type];
	if (command !== "") parts.push(command);
	if (health.intervalSeconds) parts.push(`every ${health.intervalSeconds}s`);
	return parts.join(" ");
}

/** "512 MB · 0.5 CPU · 2 replicas", only what the Compose file set. */
export function limitsText(service: ComposePlanService): string | null {
	const parts: string[] = [];
	if (service.memoryMb) {
		parts.push(
			service.memoryMb >= 1024
				? `${Number((service.memoryMb / 1024).toFixed(1))} GB`
				: `${service.memoryMb} MB`,
		);
	}
	if (service.cpus) parts.push(`${service.cpus} CPU`);
	if (service.replicas > 1) parts.push(`${service.replicas} replicas`);
	return parts.length > 0 ? parts.join(" · ") : null;
}

export interface BindGroups {
	/** Rejected, missing or ignored: say why. */
	attention: ComposePlanBind[];
	/** Directory mounts the reader can turn into a named volume. */
	convertible: ComposePlanBind[];
}

export function groupBinds(
	binds: readonly ComposePlanBind[] | null | undefined,
): BindGroups {
	const out: BindGroups = { attention: [], convertible: [] };
	for (const bind of binds ?? []) {
		if (bind.canConvert) out.convertible.push(bind);
		else if (bind.status !== "file" && bind.status !== "converted") {
			out.attention.push(bind);
		}
	}
	return out;
}

/** A byte count as "1.2 KB". */
export function formatBytes(bytes: number): string {
	if (bytes < 1024) return `${bytes} B`;
	if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
	return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

/** Parses a port field: undefined when empty, NaN when it is not 0 to 65535. */
export function parsePortField(text: string): number | undefined {
	const trimmed = text.trim();
	if (trimmed === "") return undefined;
	if (!/^\d+$/.test(trimmed)) return Number.NaN;
	const value = Number(trimmed);
	return value > 65535 ? Number.NaN : value;
}

/** One short phrase per container option carried over from the Compose file; empty when none. */
export function dockerOptionTexts(
	docker: ComposePlanService["docker"],
): string[] {
	if (!docker) return [];
	const out: string[] = [];
	if ((docker.capAdd ?? []).length > 0) {
		out.push(`cap_add ${(docker.capAdd ?? []).join(", ")}`);
	}
	for (const limit of docker.ulimits ?? []) {
		out.push(`ulimit ${limit.name} ${limit.soft}:${limit.hard}`);
	}
	if (docker.shmSizeMb) out.push(`shm ${docker.shmSizeMb} MB`);
	if ((docker.extraHosts ?? []).length > 0) {
		out.push(`extra hosts ${(docker.extraHosts ?? []).join(", ")}`);
	}
	if (docker.init) out.push("init");
	if (docker.stopSignal) out.push(`stop signal ${docker.stopSignal}`);
	if ((docker.entrypoint ?? []).length > 0) {
		out.push(`entrypoint ${(docker.entrypoint ?? []).join(" ")}`);
	}
	for (const mount of docker.tmpfs ?? []) {
		out.push(`tmpfs ${mount.path}${mount.sizeMb ? ` ${mount.sizeMb} MB` : ""}`);
	}
	return out;
}

export interface ImportFailure {
	title: string;
	body: string;
	/** The plan is stale or blocked: review it again. */
	replan: boolean;
}

/** What the Stage step says when the import is refused. */
export function importFailure(failure: {
	status: number | undefined;
	code: string | undefined;
	message: string;
	details?: Record<string, unknown> | undefined;
}): ImportFailure {
	const blockers = Array.isArray(failure.details?.blockers)
		? (failure.details.blockers as unknown[]).filter(
				(b): b is string => typeof b === "string",
			)
		: [];
	switch (failure.code) {
		case "import_blocked":
			return {
				title: "Something still blocks the import",
				body: `${blockers.length > 0 ? blockers.join(" ") : failure.message} Nothing was staged.`,
				replan: true,
			};
		case "preflight_failed":
			return {
				title: "A check failed on the server",
				body: `${failure.message} Nothing was staged.`,
				replan: true,
			};
		case "import_conflict":
			return {
				title: "The plan changed while staging",
				body: `${failure.message} Nothing was staged.`,
				replan: true,
			};
		case "invalid_compose":
			return {
				title: "The Compose file cannot be imported",
				body: `${failure.message} Nothing was staged.`,
				replan: true,
			};
	}
	if (failure.status === 403) {
		return {
			title: "You are not allowed to import this",
			body: failure.message,
			replan: false,
		};
	}
	return {
		title: "Could not stage the import",
		body: `${failure.message} Nothing was staged.`,
		replan: false,
	};
}

// --- Run as stack ---------------------------------------------------------------

/** The stack half of a plan, or undefined for a split plan. */
export function stackPlanOf(
	plan: ComposePlan | undefined,
): ComposeStackPlan | undefined {
	return plan?.mode === "stack" ? plan.stack : undefined;
}

/** Records a variable choice of a stack import (`choices.variables`), keeping the plan's default when unset. */
export function setVariableChoice(
	choices: ComposeChoices,
	key: string,
	patch: { secret?: boolean | null; skip?: boolean | null },
): ComposeChoices {
	const next: ComposeEnvChoice = { ...(choices.variables?.[key] ?? {}) };
	if (patch.secret === null) delete next.secret;
	else if (patch.secret !== undefined) next.secret = patch.secret;
	if (patch.skip === null || patch.skip === false) delete next.skip;
	else if (patch.skip !== undefined) next.skip = patch.skip;
	const variables = { ...(choices.variables ?? {}) };
	if (Object.keys(next).length === 0) delete variables[key];
	else variables[key] = next;
	const { variables: _drop, ...rest } = choices;
	return Object.keys(variables).length === 0 ? rest : { ...rest, variables };
}

/** A stack variable row as shown: the reader's choice over the plan's default. */
export function stackVariableState(
	variable: ComposePlanEnv,
	choice: ComposeEnvChoice | undefined,
): { secret: boolean; skipped: boolean; lockedByPlan: boolean } {
	return {
		secret: choice?.secret ?? variable.secret,
		skipped: choice?.skip ?? variable.skipped,
		// COMPOSE_ and DOCKER_ keys never reach compose; the plan skips them for good.
		lockedByPlan: variable.skipped && choice?.skip !== true,
	};
}

/** A component's domain as the reader set it, else the plan's suggestion. */
export function componentDomain(
	component: ComposeStackComponent,
	choices: ComposeChoices,
): { enabled: boolean; host: string } {
	const chosen = choices.services?.[component.name]?.domain;
	const suggested = (component.domains ?? [])[0];
	return {
		enabled: chosen?.enabled ?? suggested?.enabled ?? false,
		host: chosen?.host ?? suggested?.host ?? "",
	};
}

/** "3 components, 1 problem, 2 variables". */
export function stackSummaryText(stack: ComposeStackPlan): string {
	const problems = (stack.violations ?? []).filter(
		(violation) => !(stack.unrestricted && violation.kind === "restricted"),
	).length;
	return [
		plural((stack.components ?? []).length, "component", "components"),
		plural(problems, "problem", "problems"),
		plural(
			(stack.variables ?? []).filter((variable) => !variable.skipped).length,
			"variable",
			"variables",
		),
	].join(", ");
}

/** What the Stage button of a stack import says. */
export const STAGE_STACK_LABEL = "Stage stack";

export function stagedStackMessage(name: string): string {
	return `Staged the stack ${name}. Deploy it from the staged bar.`;
}
