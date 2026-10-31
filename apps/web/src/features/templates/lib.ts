import type {
	CatalogTemplate,
	TemplateCategory,
	TemplateInput,
	TemplateInstance,
	TemplatePlanCheck,
	TemplatePlanInput,
	TemplateService,
	TemplateSummary,
	TemplateUpdate,
	TemplateUpdateChange,
} from "#/api/gen/types.gen";
import type { Server } from "#/api/models";
import { isSchedulable } from "#/features/fleet/model";

export type { TemplateInput, TemplateService };

// --- search and categories --------------------------------------------------

const words = (query: string): string[] =>
	query.trim().toLowerCase().split(/\s+/).filter(Boolean);

function haystack(item: TemplateSummary): string {
	return [
		item.name,
		item.id,
		item.summary,
		item.category,
		item.license,
		...(item.tags ?? []),
		...(item.databases ?? []),
	]
		.join(" ")
		.toLowerCase();
}

/** Lower is a better match: name first, then tags, then anywhere else. */
function rank(item: TemplateSummary, terms: readonly string[]): number {
	const name = item.name.toLowerCase();
	if (terms.every((term) => name.startsWith(term) || name.includes(` ${term}`)))
		return 0;
	if (terms.every((term) => name.includes(term))) return 1;
	const tags = (item.tags ?? []).join(" ").toLowerCase();
	if (terms.every((term) => tags.includes(term))) return 2;
	return 3;
}

/** Templates whose name, summary, tags, category, licence or engines match every word. */
export function searchTemplates(
	items: readonly TemplateSummary[],
	query: string,
): TemplateSummary[] {
	const terms = words(query);
	if (terms.length === 0) return [...items];
	return items
		.map((item, index) => ({ item, index }))
		.filter(({ item }) => {
			const text = haystack(item);
			return terms.every((term) => text.includes(term));
		})
		.sort(
			(a, b) => rank(a.item, terms) - rank(b.item, terms) || a.index - b.index,
		)
		.map(({ item }) => item);
}

/** The licence filter: `open` is the OSI-approved licences, permissive and copyleft together. */
export type LicenseFilter = "open" | "source-available" | "proprietary";

export const LICENSE_FILTERS: readonly {
	id: LicenseFilter;
	label: string;
	/** What the chip means, for its tooltip. */
	hint: string;
}[] = [
	{
		id: "open",
		label: "OSI-approved",
		hint: "OSI-approved licence (permissive or copyleft)",
	},
	{
		id: "source-available",
		label: "Source-available",
		hint: "The source is public, but the licence is not OSI-approved",
	},
	{
		id: "proprietary",
		label: "Proprietary",
		hint: "Closed software: only its container image runs here",
	},
];

export function matchesLicense(
	item: Pick<TemplateSummary, "licenseClass">,
	license: LicenseFilter | null,
): boolean {
	if (license === null) return true;
	if (license === "open") {
		return (
			item.licenseClass === "permissive" || item.licenseClass === "copyleft"
		);
	}
	return item.licenseClass === license;
}

export function filterTemplates(
	items: readonly TemplateSummary[],
	query: string,
	category: string | null,
	license: LicenseFilter | null = null,
): TemplateSummary[] {
	const found = searchTemplates(items, query).filter((item) =>
		matchesLicense(item, license),
	);
	return category === null
		? found
		: found.filter((item) => item.category === category);
}

export interface CategoryChip {
	/** `null` is the "All" chip. */
	id: string | null;
	label: string;
	count: number;
}

/**
 * The chips above the grid: "All" plus the server's categories, in the
 * server's order, each counting what the current search and licence filter
 * leave in it.
 */
export function categoryChips(
	categories: readonly Pick<TemplateCategory, "id" | "label">[],
	items: readonly TemplateSummary[],
	query: string,
	license: LicenseFilter | null = null,
): CategoryChip[] {
	const found = searchTemplates(items, query).filter((item) =>
		matchesLicense(item, license),
	);
	const counts = new Map<string, number>();
	for (const item of found) {
		counts.set(item.category, (counts.get(item.category) ?? 0) + 1);
	}
	return [
		{ id: null, label: "All", count: found.length },
		...categories.map((category) => ({
			id: category.id,
			label: category.label,
			count: counts.get(category.id) ?? 0,
		})),
	];
}

/** The licence chips: each counts what the search leaves under it. */
export function licenseChips(
	items: readonly TemplateSummary[],
	query: string,
	category: string | null,
): { id: LicenseFilter; label: string; hint: string; count: number }[] {
	const found = searchTemplates(items, query).filter(
		(item) => category === null || item.category === category,
	);
	return LICENSE_FILTERS.map((f) => ({
		...f,
		count: found.filter((item) => matchesLicense(item, f.id)).length,
	}));
}

// --- card facts -------------------------------------------------------------

/** 512 → "512 MB", 2048 → "2 GB", 1536 → "1.5 GB". */
export function memoryHint(mb: number): string {
	if (mb < 1024) return `${mb} MB`;
	const gb = mb / 1024;
	return `${Number.isInteger(gb) ? gb : gb.toFixed(1)} GB`;
}

export type LicenseTone = "open" | "flagged";

/** Source-available and proprietary software gets a badge; OSI-approved licences stay a quiet chip. */
export function licenseTone(licenseClass: string): LicenseTone {
	return licenseClass === "source-available" || licenseClass === "proprietary"
		? "flagged"
		: "open";
}

export function licenseClassLabel(licenseClass: string): string {
	switch (licenseClass) {
		case "source-available":
			return "Source-available";
		case "proprietary":
			return "Proprietary";
		case "copyleft":
			return "Copyleft";
		default:
			return "Permissive";
	}
}

/** What the badge's tooltip says; the licence link is rendered next to it. */
export function licenseExplanation(
	item: Pick<TemplateSummary, "license" | "licenseClass">,
): string {
	switch (item.licenseClass) {
		case "source-available":
			return `Licensed ${item.license}: the source is public, but this is not an OSI-approved licence. Check its terms before commercial use.`;
		case "proprietary":
			return `Proprietary software (${item.license}). Only its container image runs here.`;
		default:
			return `Licensed ${item.license}.`;
	}
}

/** The letter on a card whose template has no brand mark. */
export function templateInitial(name: string): string {
	return name.trim().charAt(0).toUpperCase() || "?";
}

const ENGINE_LABELS: Record<string, string> = {
	postgres: "PostgreSQL",
	mysql: "MySQL",
	mariadb: "MariaDB",
	valkey: "Valkey",
	redis: "Redis",
	mongodb: "MongoDB",
	clickhouse: "ClickHouse",
};

/** `postgres` → "PostgreSQL"; `postgres 17` → "PostgreSQL 17". */
export function databaseLabel(database: string): string {
	const [engine = "", ...version] = database.split(" ");
	return [ENGINE_LABELS[engine] ?? engine, ...version].join(" ");
}

// --- the install form -------------------------------------------------------

export type InputValues = Readonly<Record<string, string>>;

const GENERATOR = /\$\{\{\s*[a-zA-Z]+\(/;

/** The input's default is a generator (`${{secret(20)}}`): left empty, one is made at install. */
export function isGenerated(input: Pick<TemplateInput, "default">): boolean {
	return GENERATOR.test(input.default ?? "");
}

/** What the field shows: what the reader typed, else the literal default. */
export function inputValue(input: TemplateInput, typed: InputValues): string {
	const value = typed[input.key];
	if (value !== undefined) return value;
	return isGenerated(input) ? "" : (input.default ?? "");
}

/** The `inputs` body of plan and install: only what has a value; the rest falls back server-side. */
export function installInputs(
	inputs: readonly TemplateInput[],
	typed: InputValues,
): Record<string, string> {
	const body: Record<string, string> = {};
	for (const input of inputs) {
		const value = (typed[input.key] ?? "").trim();
		if (value !== "") body[input.key] = value;
	}
	return body;
}

/** Keys of required inputs that would be empty at install. */
export function missingInputs(
	inputs: readonly TemplateInput[],
	typed: InputValues,
): string[] {
	return inputs
		.filter(
			(input) =>
				input.required &&
				!isGenerated(input) &&
				inputValue(input, typed).trim() === "",
		)
		.map((input) => input.key);
}

/** The line under a field: why it may stay empty. */
export function inputHint(input: TemplateInput): string | null {
	if (isGenerated(input)) {
		return input.secret
			? "Generated and stored encrypted if left empty."
			: "Generated if left empty.";
	}
	if (input.secret && !input.required) return "Optional. Stored encrypted.";
	if (input.secret) return "Stored encrypted.";
	return null;
}

// --- preflight --------------------------------------------------------------

/** Rows with a place of their own in the dialog (licence callout, secrets and proxy notices). */
const NOTICE_CHECKS: Record<string, true> = {
	license: true,
	secrets: true,
	"proxy-restart": true,
};

export interface ChecksSummary {
	fail: number;
	warn: number;
	ok: number;
	/** The rows to list: failures first, then warnings, then passes. */
	rows: TemplatePlanCheck[];
}

const ORDER: Record<string, number> = { fail: 0, warn: 1, ok: 2 };

export function summarizeChecks(
	checks: readonly TemplatePlanCheck[],
): ChecksSummary {
	const rows = checks
		.filter((check) => NOTICE_CHECKS[check.id] !== true)
		.map((check, index) => ({ check, index }))
		.sort(
			(a, b) =>
				(ORDER[a.check.status] ?? 3) - (ORDER[b.check.status] ?? 3) ||
				a.index - b.index,
		)
		.map(({ check }) => check);
	const count = (status: string) =>
		rows.filter((row) => row.status === status).length;
	return { fail: count("fail"), warn: count("warn"), ok: count("ok"), rows };
}

/** "N secrets will be generated and stored encrypted", or null for none. */
export function secretsNotice(count: number): string | null {
	if (count === 0) return null;
	return `${count} ${count === 1 ? "secret" : "secrets"} will be generated or stored encrypted. They are never shown again.`;
}

export interface InstallFailure {
	title: string;
	body: string;
	/** The plan is stale (something was taken meanwhile): run it again. */
	replan: boolean;
}

/** What the dialog says when installing fails. */
export function installFailure(
	failure: { status: number | undefined; message: string },
	templateName: string,
): InstallFailure {
	switch (failure.status) {
		case 409:
			return {
				title: "The plan changed while installing",
				body: `${failure.message} Nothing was added.`,
				replan: true,
			};
		case 403:
			return {
				title: "You are not allowed to install this",
				body: failure.message,
				replan: false,
			};
		case 422:
			return {
				title: `${templateName} cannot be installed as configured`,
				body: `${failure.message} Nothing was added.`,
				replan: true,
			};
		default:
			return {
				title: `Could not add ${templateName}`,
				body: failure.message,
				replan: false,
			};
	}
}

/** "3 services added, staged" / "Deploying 3 services". */
export function installedTitle(services: number, deploying: boolean): string {
	const noun = services === 1 ? "service" : "services";
	return deploying
		? `Deploying ${services} ${noun}`
		: `${services} ${noun} added, staged`;
}

// --- the services a template creates ---------------------------------------

export interface ServiceCardModel {
	key: string;
	name: string;
	/** `app`, `worker` or `database`. */
	kind: string;
	/** Managed database engine and major version, e.g. `postgres 17`. */
	database: string | null;
	/** The image reference without its digest. */
	image: string | null;
	httpPort: number | null;
	ports: ReadonlyArray<{
		port: number;
		protocol: string;
		publish: boolean;
	}>;
	volumes: number;
	oneshot: boolean;
	memoryMb: number | null;
}

export function serviceCards(
	template: Pick<CatalogTemplate, "services">,
): ServiceCardModel[] {
	return (template.services ?? []).map((service) => ({
		key: service.key,
		name: service.name,
		kind: service.database ? "database" : service.kind || "app",
		database: service.database
			? `${service.database.engine} ${service.database.version}`
			: null,
		image: service.image ?? null,
		httpPort: service.port ? service.port : null,
		ports: (service.ports ?? []).map((port) => ({
			port: port.port,
			protocol: port.protocol,
			publish: port.publish,
		})),
		volumes: (service.volumes ?? []).length,
		oneshot: service.oneshot === true,
		memoryMb: service.resources?.memoryMb ?? null,
	}));
}

/** "tcp/25565 · udp/19132", published ports only. */
export function publishedPortsText(card: ServiceCardModel): string {
	return card.ports
		.filter((port) => port.publish)
		.map((port) => `${port.protocol}/${port.port}`)
		.join(" · ");
}

// --- the server picker ------------------------------------------------------

/** Docs page and anchor of the per-server unrestricted switch. */
export const UNRESTRICTED_DOCS =
	"operations/docker-options#the-unrestricted-switch-per-server";

/** A template that uses a Docker option only an unrestricted server accepts. */
export function needsUnrestricted(item: {
	requiresUnrestricted?: boolean | undefined;
}): boolean {
	return item.requiresUnrestricted === true;
}

/** Why a restricted server cannot take a template that needs the tier. */
export const RESTRICTED_SERVER_REASON =
	"Restricted: this template needs an unrestricted server";

/**
 * The servers a template cannot be installed on, by id, with the reason.
 * Empty unless the template needs an unrestricted server; then every server
 * that is not unrestricted is listed.
 */
export function serverRestrictions(
	servers: readonly Server[],
	requiresUnrestricted: boolean,
): Map<string, string> {
	const out = new Map<string, string>();
	if (!requiresUnrestricted) return out;
	for (const server of servers) {
		if (!server.unrestricted) out.set(server.id, RESTRICTED_SERVER_REASON);
	}
	return out;
}

/**
 * The template needs an unrestricted server and none of the servers offered
 * is one: only an instance admin can change that.
 */
export function lacksUnrestrictedServer(
	servers: readonly Server[],
	requiresUnrestricted: boolean,
): boolean {
	return requiresUnrestricted && !servers.some((server) => server.unrestricted);
}

/**
 * The first online, schedulable server, else the first schedulable one. For a
 * template that needs the unrestricted tier only unrestricted servers count,
 * so the default never lands on one that would refuse the install.
 */
export function defaultServerId(
	servers: readonly Server[],
	requiresUnrestricted = false,
): string {
	const usable = servers.filter(
		(server) =>
			server.status !== "revoked" &&
			server.status !== "pending" &&
			(!requiresUnrestricted || server.unrestricted),
	);
	return (
		usable.find((s) => s.status === "online" && isSchedulable(s))?.id ??
		usable.find(isSchedulable)?.id ??
		""
	);
}

// --- stacks -----------------------------------------------------------------

export function instanceOfGroup(
	instances: readonly TemplateInstance[],
	groupId: string,
): TemplateInstance | undefined {
	return instances.find((instance) => instance.groupId === groupId);
}

export function instanceOfService(
	instances: readonly TemplateInstance[],
	serviceId: string,
): TemplateInstance | undefined {
	return instances.find((instance) =>
		(instance.services ?? []).some((service) => service.id === serviceId),
	);
}

// --- template updates ---------------------------------------------------------

export interface UpdateGroups {
	apply: TemplateUpdateChange[];
	kept: TemplateUpdateChange[];
	manual: TemplateUpdateChange[];
	applied: TemplateUpdateChange[];
}

/** The diff split by what the update does with each change, in server order. */
export function updateGroups(
	changes: readonly TemplateUpdateChange[],
): UpdateGroups {
	const groups: UpdateGroups = { apply: [], kept: [], manual: [], applied: [] };
	for (const change of changes) groups[change.status].push(change);
	return groups;
}

/** "Revision 1 → 2 · 3.4.0 → 3.4.1": the version part only when it moves. */
export function versionStep(
	update: Pick<
		TemplateUpdate,
		| "installedRevision"
		| "currentRevision"
		| "installedVersion"
		| "currentVersion"
	>,
): string {
	const revision = `Revision ${update.installedRevision} → ${update.currentRevision}`;
	const from = update.installedVersion;
	const to = update.currentVersion;
	if (!to || from === to) return revision;
	return `${revision} · ${from ? `${from} → ${to}` : to}`;
}

/** One sentence on what staging will do. */
export function updateSummary(
	update: Pick<TemplateUpdate, "apply" | "kept" | "manual">,
): string {
	const parts = [
		`${update.apply} ${update.apply === 1 ? "change" : "changes"} will be staged`,
	];
	if (update.kept > 0) parts.push(`${update.kept} kept as you changed them`);
	if (update.manual > 0) {
		parts.push(`${update.manual} left for you`);
	}
	return `${parts.join(", ")}. Nothing is deployed until you press Deploy.`;
}

// Only its shape matters: the form reads "has a generator" off the default.
const GENERATED_DEFAULT = "$" + "{{secret(32)}}";

/**
 * The inputs a new revision added, in the form's own shape. A generated
 * secret keeps a generator as its default, so the form says it is generated.
 */
export function updateInputFields(
	inputs: readonly TemplatePlanInput[],
): TemplateInput[] {
	return inputs.map((input) => ({
		key: input.key,
		label: input.label,
		required: input.required,
		secret: input.secret,
		default: input.generated ? GENERATED_DEFAULT : (input.value ?? ""),
		...(input.description ? { description: input.description } : {}),
		...(input.options ? { options: input.options } : {}),
		...(input.format ? { format: input.format } : {}),
	}));
}
