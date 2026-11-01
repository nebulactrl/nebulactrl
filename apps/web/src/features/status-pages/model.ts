// The pure model of the status pages feature: the component list the editor
// edits, the forms and their validation, the incident vocabulary, the
// maintenance window's local-time handling and the page's overall state. The
// components only draw it. Limits and rules mirror internal/statuspages.

import type {
	ComponentBody,
	CreateIncidentInputBody,
	CreateMaintenanceInputBody,
	CreatePageInputBody,
	MaintenanceWindowView,
	StatusIncidentView,
	StatusPageComponentView,
	StatusPageView,
	UpdateIncidentInputBody,
	UpdateMaintenanceInputBody,
	UpdatePageInputBody,
} from "#/api/gen/types.gen";
import type { StatusKind } from "#/components/ui/status-icon";
import {
	type FixAction,
	REPLACE_LABEL,
	type RecordText,
	replaceConfirm,
} from "#/features/cloudflare/model";
import { validateHostname } from "#/features/platform/model";
import { NEW_NAME_NOTE } from "#/lib/dns-new-name";

export {
	isNewName,
	NEW_NAME_NOTE,
} from "#/lib/dns-new-name";

export const MAX_COMPONENTS = 100;
export const MAX_PAGE_TITLE = 100;
export const MAX_SLUG = 50;
export const MAX_DESCRIPTION = 500;
export const MAX_FOOTER = 500;
export const MAX_DISPLAY_NAME = 60;
export const MAX_GROUP_NAME = 60;
const MAX_URL = 300;
export const MAX_INCIDENT_TITLE = 120;
export const MAX_UPDATE_BODY = 4000;
export const MAX_WINDOW_DESCRIPTION = 2000;

// --- Components --------------------------------------------------------------

/** One service on a page, as the editor holds it. */
export interface ComponentRow {
	serviceId: string;
	/** The public name; blank means the service's own name. */
	displayName: string;
	/** Blank means ungrouped. */
	groupName: string;
	/** Blank means no link; otherwise an absolute https URL. */
	url: string;
	serviceName: string;
	projectName: string;
	environmentName: string;
}

/** A service the picker can offer. */
export interface ServiceOption {
	id: string;
	name: string;
	projectName: string;
	environmentName: string;
}

/** A run of rows under one group name; `name` is "" for the ungrouped run. */
export interface RowGroup {
	name: string;
	rows: ComponentRow[];
}

const trimmed = (value: string): string => value.trim();

/**
 * The order a public page shows: ungrouped first, then each group in the
 * order its first member appears, members in their own order. The editor
 * keeps rows normalised, so a group is always one contiguous run.
 */
export function groupRows(rows: readonly ComponentRow[]): RowGroup[] {
	const ungrouped: ComponentRow[] = [];
	const named = new Map<string, ComponentRow[]>();
	for (const row of rows) {
		const name = trimmed(row.groupName);
		if (name === "") {
			ungrouped.push(row);
			continue;
		}
		const run = named.get(name);
		if (run) run.push(row);
		else named.set(name, [row]);
	}
	const out: RowGroup[] = [];
	if (ungrouped.length > 0) out.push({ name: "", rows: ungrouped });
	for (const [name, run] of named) out.push({ name, rows: run });
	return out;
}

/** The flat, ordered list of `groupRows`. */
function normalizeRows(rows: readonly ComponentRow[]): ComponentRow[] {
	return groupRows(rows).flatMap((group) => group.rows);
}

export function rowsFromPage(
	components: readonly StatusPageComponentView[] | null | undefined,
): ComponentRow[] {
	return normalizeRows(
		(components ?? []).map((c) => ({
			serviceId: c.serviceId,
			displayName: c.displayName,
			groupName: c.groupName,
			url: c.url,
			serviceName: c.serviceName,
			projectName: c.projectName,
			environmentName: c.environmentName,
		})),
	);
}

/** A new row for a service: its own name as the public name, no group, no link. */
export function rowForService(service: ServiceOption): ComponentRow {
	return {
		serviceId: service.id,
		displayName: service.name.slice(0, MAX_DISPLAY_NAME),
		groupName: "",
		url: "",
		serviceName: service.name,
		projectName: service.projectName,
		environmentName: service.environmentName,
	};
}

/** Appends services that are not on the page yet, up to the page's limit. */
export function addServices(
	rows: readonly ComponentRow[],
	services: readonly ServiceOption[],
): ComponentRow[] {
	const have = new Set(rows.map((row) => row.serviceId));
	const next = [...rows];
	for (const service of services) {
		if (have.has(service.id) || next.length >= MAX_COMPONENTS) continue;
		have.add(service.id);
		next.push(rowForService(service));
	}
	return normalizeRows(next);
}

export function removeRow(
	rows: readonly ComponentRow[],
	serviceId: string,
): ComponentRow[] {
	return rows.filter((row) => row.serviceId !== serviceId);
}

export type RowEdit = Partial<
	Pick<ComponentRow, "displayName" | "groupName" | "url">
>;

/**
 * Edits one row. Giving a row a group moves it to the end of that group
 * (a new group goes last), so the list stays grouped.
 */
export function editRow(
	rows: readonly ComponentRow[],
	serviceId: string,
	edit: RowEdit,
): ComponentRow[] {
	const current = rows.find((row) => row.serviceId === serviceId);
	if (!current) return [...rows];
	const next: ComponentRow = { ...current, ...edit };
	if (edit.groupName === undefined || sameGroup(current, next)) {
		return rows.map((row) => (row.serviceId === serviceId ? next : row));
	}
	return normalizeRows([
		...rows.filter((row) => row.serviceId !== serviceId),
		next,
	]);
}

function sameGroup(a: ComponentRow, b: ComponentRow): boolean {
	return trimmed(a.groupName) === trimmed(b.groupName);
}

/** Moves a row up (-1) or down (1) among the members of its own group. */
export function moveRow(
	rows: readonly ComponentRow[],
	serviceId: string,
	direction: -1 | 1,
): ComponentRow[] {
	const flat = normalizeRows(rows);
	const index = flat.findIndex((row) => row.serviceId === serviceId);
	const row = flat[index];
	const neighbour = flat[index + direction];
	if (!row || !neighbour || !sameGroup(neighbour, row)) return flat;
	const next = [...flat];
	next[index] = neighbour;
	next[index + direction] = row;
	return next;
}

/** Whether a row can move that way inside its group (for disabled buttons). */
export function canMoveRow(
	rows: readonly ComponentRow[],
	serviceId: string,
	direction: -1 | 1,
): boolean {
	const flat = normalizeRows(rows);
	const index = flat.findIndex((row) => row.serviceId === serviceId);
	const row = flat[index];
	const neighbour = flat[index + direction];
	return (
		row !== undefined && neighbour !== undefined && sameGroup(neighbour, row)
	);
}

/** The named groups in page order; the ungrouped run is not a group. */
export function groupNames(rows: readonly ComponentRow[]): string[] {
	return groupRows(rows)
		.map((group) => group.name)
		.filter((name) => name !== "");
}

/** Moves a named group before or after its neighbouring named group. */
export function moveGroup(
	rows: readonly ComponentRow[],
	name: string,
	direction: -1 | 1,
): ComponentRow[] {
	const groups = groupRows(rows);
	const index = groups.findIndex((group) => group.name === name);
	const target = index + direction;
	const there = groups[target];
	if (index < 0 || name === "" || !there || there.name === "") {
		return groups.flatMap((group) => group.rows);
	}
	const next = [...groups];
	next[index] = there;
	next[target] = groups[index] as RowGroup;
	return next.flatMap((group) => group.rows);
}

export function canMoveGroup(
	rows: readonly ComponentRow[],
	name: string,
	direction: -1 | 1,
): boolean {
	const groups = groupRows(rows);
	const index = groups.findIndex((group) => group.name === name);
	const there = groups[index + direction];
	return name !== "" && index >= 0 && there !== undefined && there.name !== "";
}

/** Renames a group; renaming onto an existing group merges the two. */
export function renameGroup(
	rows: readonly ComponentRow[],
	from: string,
	to: string,
): ComponentRow[] {
	const target = trimmed(to);
	return normalizeRows(
		rows.map((row) =>
			trimmed(row.groupName) === from ? { ...row, groupName: target } : row,
		),
	);
}

/** The request body: the full ordered list; blank optional fields are left out. */
export function componentsBody(rows: readonly ComponentRow[]): ComponentBody[] {
	return normalizeRows(rows).map((row) => {
		const body: ComponentBody = { serviceId: row.serviceId };
		const displayName = trimmed(row.displayName);
		const groupName = trimmed(row.groupName);
		const url = trimmed(row.url);
		if (displayName !== "") body.displayName = displayName;
		if (groupName !== "") body.groupName = groupName;
		if (url !== "") body.url = url;
		return body;
	});
}

/** Whether two lists would save differently (order, names, groups or links). */
export function rowsDiffer(
	a: readonly ComponentRow[],
	b: readonly ComponentRow[],
): boolean {
	if (a.length !== b.length) return true;
	return a.some((row, index) => {
		const other = b[index];
		return (
			other === undefined ||
			row.serviceId !== other.serviceId ||
			trimmed(row.displayName) !== trimmed(other.displayName) ||
			trimmed(row.groupName) !== trimmed(other.groupName) ||
			trimmed(row.url) !== trimmed(other.url)
		);
	});
}

/**
 * The optional link of a component: an absolute https URL with a host and no
 * credentials, up to 300 characters. Blank is fine (no link).
 */
export function validateComponentUrl(value: string): string | null {
	const raw = trimmed(value);
	if (raw === "") return null;
	if (raw.length > MAX_URL) {
		return `A link is at most ${MAX_URL} characters.`;
	}
	if (
		Array.from(raw).some(
			(ch) => ch.charCodeAt(0) <= 32 || ch.charCodeAt(0) === 127,
		)
	) {
		return "A link cannot contain spaces.";
	}
	const example = "Use an absolute https link, like https://example.com/docs.";
	if (!/^https:\/\//i.test(raw)) return example;
	let url: URL;
	try {
		url = new URL(raw);
	} catch {
		return example;
	}
	if (url.protocol !== "https:" || url.hostname === "") return example;
	if (url.username !== "" || url.password !== "") {
		return "A link cannot contain a user name or password.";
	}
	return null;
}

function validateDisplayName(value: string): string | null {
	return trimmed(value).length > MAX_DISPLAY_NAME
		? `A name is at most ${MAX_DISPLAY_NAME} characters.`
		: null;
}

function validateGroupName(value: string): string | null {
	return trimmed(value).length > MAX_GROUP_NAME
		? `A group name is at most ${MAX_GROUP_NAME} characters.`
		: null;
}

export interface RowProblems {
	displayName?: string;
	groupName?: string;
	url?: string;
}

/** The problems of each row, keyed by service id; rows without any are absent. */
export function rowProblems(
	rows: readonly ComponentRow[],
): Record<string, RowProblems> {
	const out: Record<string, RowProblems> = {};
	for (const row of rows) {
		const problems: RowProblems = {};
		const displayName = validateDisplayName(row.displayName);
		if (displayName) problems.displayName = displayName;
		const groupName = validateGroupName(row.groupName);
		if (groupName) problems.groupName = groupName;
		const url = validateComponentUrl(row.url);
		if (url) problems.url = url;
		if (Object.keys(problems).length > 0) out[row.serviceId] = problems;
	}
	return out;
}

// --- The service picker --------------------------------------------------------

/** Services whose name, project or environment contains every word of `query`. */
export function filterServices<T extends ServiceOption>(
	services: readonly T[],
	query: string,
): T[] {
	const words = query.toLowerCase().split(/\s+/).filter(Boolean);
	if (words.length === 0) return [...services];
	return services.filter((service) => {
		const haystack =
			`${service.name} ${service.projectName} ${service.environmentName}`.toLowerCase();
		return words.every((word) => haystack.includes(word));
	});
}

export interface ServiceSection<T extends ServiceOption> {
	key: string;
	label: string;
	services: T[];
}

/** Services under "Project · environment" headings, in the order they come. */
export function sectionServices<T extends ServiceOption>(
	services: readonly T[],
): ServiceSection<T>[] {
	const sections = new Map<string, ServiceSection<T>>();
	for (const service of services) {
		const key = `${service.projectName}\u0000${service.environmentName}`;
		const section = sections.get(key);
		if (section) section.services.push(service);
		else {
			sections.set(key, {
				key,
				label: `${service.projectName} · ${service.environmentName}`,
				services: [service],
			});
		}
	}
	return [...sections.values()];
}

// --- The page form -------------------------------------------------------------

export type Theme = "auto" | "light" | "dark";

export const THEMES: readonly { value: Theme; label: string }[] = [
	{ value: "auto", label: "Auto" },
	{ value: "light", label: "Light" },
	{ value: "dark", label: "Dark" },
];

export interface PageForm {
	title: string;
	slug: string;
	description: string;
	footer: string;
	theme: Theme;
	showUptime: boolean;
	hostname: string;
}

export const EMPTY_PAGE_FORM: PageForm = {
	title: "",
	slug: "",
	description: "",
	footer: "",
	theme: "auto",
	showUptime: true,
	hostname: "",
};

export function formFromPage(page: StatusPageView): PageForm {
	return {
		title: page.title,
		slug: page.slug,
		description: page.description,
		footer: page.footer,
		theme: page.theme,
		showUptime: page.showUptime,
		hostname: page.hostname ?? "",
	};
}

/** Lowercase, no surrounding space, no trailing dot: how the control plane stores it. */
export function normalizeHostname(value: string): string {
	return value.trim().toLowerCase().replace(/\.$/, "");
}

/** "API status" becomes "api-status", as the control plane derives a slug. */
export function slugify(title: string): string {
	const slug = title
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-+|-+$/g, "")
		.slice(0, MAX_SLUG)
		.replace(/-+$/g, "");
	return slug === "" ? "status" : slug;
}

const SLUG_RE = /^[a-z0-9]+(-[a-z0-9]+)*$/;

/** A slug: lowercase letters and digits with single hyphens. Blank is not valid here. */
export function validateSlug(value: string): string | null {
	const slug = trimmed(value);
	if (slug === "") return "Enter a short name.";
	if (slug.length > MAX_SLUG)
		return `A short name is at most ${MAX_SLUG} characters.`;
	if (!SLUG_RE.test(slug)) {
		return "Use lowercase letters and digits, with single hyphens between them.";
	}
	return null;
}

export type PageFormErrors = Partial<
	Record<"title" | "slug" | "description" | "footer" | "hostname", string>
>;

/**
 * The form's problems. `slug` may be blank only on create, where the control
 * plane derives one from the title.
 */
export function validatePageForm(
	form: PageForm,
	mode: "create" | "edit",
): PageFormErrors {
	const errors: PageFormErrors = {};
	const title = trimmed(form.title);
	if (title === "") errors.title = "Enter a title.";
	else if (title.length > MAX_PAGE_TITLE) {
		errors.title = `A title is at most ${MAX_PAGE_TITLE} characters.`;
	}
	if (!(mode === "create" && trimmed(form.slug) === "")) {
		const slug = validateSlug(form.slug);
		if (slug) errors.slug = slug;
	}
	if (form.description.length > MAX_DESCRIPTION) {
		errors.description = `A description is at most ${MAX_DESCRIPTION} characters.`;
	}
	if (form.footer.length > MAX_FOOTER) {
		errors.footer = `A footer is at most ${MAX_FOOTER} characters.`;
	}
	const hostname = validateHostname(form.hostname);
	if (hostname) errors.hostname = hostname;
	return errors;
}

export function hasErrors(errors: object): boolean {
	return Object.keys(errors).length > 0;
}

/** Whether the form differs from the saved page (hostname compared as stored). */
export function pageFormDirty(form: PageForm, saved: PageForm): boolean {
	return (
		trimmed(form.title) !== trimmed(saved.title) ||
		trimmed(form.slug) !== trimmed(saved.slug) ||
		form.description.trim() !== saved.description.trim() ||
		form.footer.trim() !== saved.footer.trim() ||
		form.theme !== saved.theme ||
		form.showUptime !== saved.showUptime ||
		normalizeHostname(form.hostname) !== normalizeHostname(saved.hostname)
	);
}

/** Only what changed; an empty hostname removes it. */
export function pagePatch(
	form: PageForm,
	saved: PageForm,
): UpdatePageInputBody {
	const body: UpdatePageInputBody = {};
	if (trimmed(form.title) !== trimmed(saved.title)) {
		body.title = trimmed(form.title);
	}
	if (trimmed(form.slug) !== trimmed(saved.slug))
		body.slug = trimmed(form.slug);
	if (form.description.trim() !== saved.description.trim()) {
		body.description = form.description.trim();
	}
	if (form.footer.trim() !== saved.footer.trim()) {
		body.footer = form.footer.trim();
	}
	if (form.theme !== saved.theme) body.theme = form.theme;
	if (form.showUptime !== saved.showUptime) body.showUptime = form.showUptime;
	if (normalizeHostname(form.hostname) !== normalizeHostname(saved.hostname)) {
		body.hostname = normalizeHostname(form.hostname);
	}
	return body;
}

/**
 * The request body of a new page. `fromProjectId` is the "Start from project"
 * choice ("" is none): the control plane then prefills the components.
 */
export function pageCreateBody(
	form: PageForm,
	fromProjectId = "",
): CreatePageInputBody {
	const body: CreatePageInputBody = {
		title: trimmed(form.title),
		theme: form.theme,
		showUptime: form.showUptime,
	};
	if (trimmed(form.slug) !== "") body.slug = trimmed(form.slug);
	if (form.description.trim() !== "")
		body.description = form.description.trim();
	if (form.footer.trim() !== "") body.footer = form.footer.trim();
	if (normalizeHostname(form.hostname) !== "") {
		body.hostname = normalizeHostname(form.hostname);
	}
	const projectId = trimmed(fromProjectId);
	if (projectId !== "") body.fromProjectId = projectId;
	return body;
}

/** Where an API refusal belongs on the page. */
export type ProblemField =
	| "title"
	| "slug"
	| "description"
	| "footer"
	| "hostname"
	| "project"
	| "publish";

export interface PageProblem {
	field: ProblemField;
	message: string;
}

/**
 * Turns the control plane's error code into a sentence at the field it is
 * about. Unknown codes return null so the caller shows the server's message.
 */
export function pageProblem(code: string | undefined): PageProblem | null {
	switch (code) {
		case "publish_needs_hostname":
			return {
				field: "publish",
				message: "Add a hostname before publishing the page.",
			};
		case "publish_needs_components":
			return {
				field: "publish",
				message: "Add at least one component before publishing the page.",
			};
		case "slug_conflict":
			return {
				field: "slug",
				message: "Another status page already uses this short name.",
			};
		case "invalid_slug":
			return {
				field: "slug",
				message:
					"Use lowercase letters and digits, with single hyphens between them.",
			};
		case "hostname_conflict":
			return {
				field: "hostname",
				message:
					"Another status page or a service domain already serves this hostname.",
			};
		case "hostname_reserved":
			return {
				field: "hostname",
				message:
					"That is the dashboard's own address. Use a different hostname for the public page.",
			};
		case "invalid_hostname":
			return {
				field: "hostname",
				message: "Enter a valid hostname, like status.example.com.",
			};
		case "invalid_title":
			return { field: "title", message: "Enter a title." };
		case "unknown_project":
			return {
				field: "project",
				message: "That project is not in this team. Pick another one.",
			};
		default:
			return null;
	}
}

/** The address of a published page. */
export function publicUrl(hostname: string): string {
	return `https://${hostname}/`;
}

/**
 * Why a page cannot be previewed yet, or null when its public address serves
 * it. The control plane renders a page only on its own hostname and only once
 * it is published, so a draft has no preview to open.
 */
export function previewBlocker(page: {
	hostname: string | null;
	published: boolean;
}): string | null {
	if (!page.hostname) {
		return "A draft is not served anywhere. Add a hostname and publish the page, and Preview opens it.";
	}
	if (!page.published) {
		return `A draft is not served yet. Publish the page and Preview opens it on ${page.hostname}.`;
	}
	return null;
}

/**
 * The sentence for a refused save. Taking components or the hostname away from
 * a page that is published is refused with the publish codes, which would read
 * wrongly as "add one": here they say to unpublish first.
 */
export function savePageProblem(
	code: string | undefined,
	published: boolean,
): PageProblem | null {
	if (published && code === "publish_needs_components") {
		return {
			field: "publish",
			message:
				"A published page needs at least one component. Unpublish it first, or keep one.",
		};
	}
	if (published && code === "publish_needs_hostname") {
		return {
			field: "hostname",
			message:
				"A published page needs a hostname. Unpublish it first, or keep the hostname.",
		};
	}
	return pageProblem(code);
}

// --- The DNS check -------------------------------------------------------------

type DnsStatus = "ok" | "mismatch" | "missing" | "unknown";

/** What the DNS check says: a title, its tone, and the sentence that says what to do. */
export interface DnsNotice {
	title: string;
	variant: "success" | "warning" | "info";
	summary: string;
	/** Whether the server's own message adds something to the summary. */
	showMessage: boolean;
	/** Whether to add the quiet new-name line under the result (never when the summary already says it). */
	headsUp: boolean;
}

/** What the check found, as the server reports it. */
export interface DnsFinding {
	status: DnsStatus;
	/** The hostname resolves to Cloudflare's addresses and the record is proxied. */
	proxied: boolean;
	/** Cloudflare's API confirmed the page's record exists. */
	cloudflareRecord: boolean;
}

/** Said when Cloudflare has the record and public DNS has not caught up. */
export const CLOUDFLARE_LAG_NOTE =
	"Cloudflare has the record. Some DNS caches can take up to 30 minutes to see a new name.";

/**
 * The copy for a DNS check. `managed` is true when the Cloudflare panel above
 * manages the records: the page then never tells the user to create records at
 * a DNS provider, because NebulaCtrl creates them.
 */
export function dnsNotice(
	finding: DnsFinding,
	managed: boolean,
	isNew = false,
): DnsNotice {
	const { status, proxied, cloudflareRecord } = finding;
	if (status === "ok" && proxied) {
		return {
			title: "Resolves through Cloudflare",
			variant: "success",
			summary:
				"The hostname resolves to Cloudflare, which forwards visitors to this control plane.",
			showMessage: false,
			headsUp: isNew,
		};
	}
	if (status === "ok") {
		return {
			title: "DNS is ready",
			variant: "success",
			summary: "The hostname points at this control plane.",
			showMessage: false,
			headsUp: isNew,
		};
	}
	if (status === "unknown") {
		return {
			title: "DNS could not be checked",
			variant: "info",
			summary:
				"The check could not tell. DNS may still be changing; check again in a minute.",
			showMessage: true,
			headsUp: false,
		};
	}
	if (cloudflareRecord) {
		return {
			title: "Cloudflare has the record",
			variant: "info",
			// One line, never two: a new name gets the longer note in the same sentence.
			summary: isNew
				? `Cloudflare has the record. ${NEW_NAME_NOTE}`
				: CLOUDFLARE_LAG_NOTE,
			showMessage: false,
			headsUp: false,
		};
	}
	const missing = status === "missing";
	if (managed) {
		return {
			title: missing ? "DNS has no records yet" : "DNS points elsewhere",
			variant: "warning",
			summary: missing
				? "Cloudflare does not have the record yet. The Cloudflare section above creates it."
				: "The record in Cloudflare points somewhere else. The Cloudflare section above fixes it.",
			showMessage: true,
			headsUp: false,
		};
	}
	return {
		title: missing ? "DNS has no records yet" : "DNS points elsewhere",
		variant: "warning",
		summary: missing
			? "The hostname has no records yet. Create the ones below at your DNS provider."
			: "The hostname points somewhere else. Change its records to the ones below.",
		showMessage: true,
		headsUp: false,
	};
}

/** One record as a line to copy: "status.example.com  A  203.0.113.10". */
export function recordLine(record: {
	name: string;
	type: string;
	value: string;
}): string {
	return `${record.name}  ${record.type}  ${record.value}`;
}

// --- The Cloudflare DNS panel --------------------------------------------------

/** The zone states in which the hostname sits in a connected Cloudflare zone. */
const ZONE_STATES: readonly string[] = [
	"managed",
	"present",
	"conflict",
	"wrong_target",
	"missing",
];

export type DnsPanelMode = "manual" | "cloudflare";

/**
 * Which DNS panel a page shows: the Cloudflare controls when the hostname is
 * in a connected zone, otherwise the records to create by hand. No answer yet
 * (or a refused read) is the manual panel too.
 */
export function dnsPanelMode(
	status: { configured: boolean; status: string } | null | undefined,
): DnsPanelMode {
	if (!status?.configured) return "manual";
	return ZONE_STATES.includes(status.status) ? "cloudflare" : "manual";
}

/** The origin lock of a page, as the panel reads it. */
export interface PageLock {
	enabled: boolean;
	optIn: boolean;
	aop: string;
	reason: string;
}

export interface PageLockInfo {
	label: string;
	kind: StatusKind;
	/** Why the page is not locked, in a short sentence; "" when it is. */
	reason: string;
	/** "Authenticated Origin Pulls: on" while locked; null otherwise. */
	aop: string | null;
}

const LOCK_REASON: Record<string, string> = {
	opted_out: "You turned the lock off for this page.",
	not_proxied: "The lock applies while the record is proxied.",
	not_in_zone: "The hostname is in no connected zone.",
	pending: "Waiting for the record to become proxied.",
	not_published: "The page is not published yet. The lock applies once it is.",
};

/** The headline, tone and short reason of a page's origin lock. */
export function pageLockInfo(lock: PageLock): PageLockInfo {
	if (lock.enabled) {
		return {
			label: "Locked",
			kind: "live",
			reason: "",
			aop: `Authenticated Origin Pulls: ${lock.aop}`,
		};
	}
	const reason =
		LOCK_REASON[lock.reason] ?? "The page is open to every address.";
	if (lock.reason === "opted_out") {
		return { label: "Open", kind: "warning", reason, aop: null };
	}
	if (lock.reason === "pending" || lock.reason === "not_published") {
		return { label: "Pending", kind: "pending", reason, aop: null };
	}
	return { label: "Not locked", kind: "stopped", reason, aop: null };
}

/** What the panel's primary button does for a live status. */
export type PageFixAction = FixAction;

export interface PageFixInput {
	configured: boolean;
	status: string;
	error: string;
	records?: readonly (RecordText & { owned: boolean })[] | null;
	desiredRecords?: readonly RecordText[] | null;
	effectiveProxied?: boolean;
}

/** "203.0.113.10, 2001:db8::1": where the records should point. */
export function desiredTargets(
	records: readonly { content: string }[] | null | undefined,
): string {
	const targets = (records ?? []).map((record) => record.content);
	return targets.length > 0 ? targets.join(", ") : "the server";
}

/**
 * The one action that fixes a zone state: create or correct NebulaCtrl's
 * records; adopt a right record the team created, or replace a conflicting
 * one (both after asking); nothing when the records are right.
 */
export function pageFixAction(status: PageFixInput): PageFixAction {
	if (!status.configured || !ZONE_STATES.includes(status.status)) {
		return { kind: "none" };
	}
	switch (status.status) {
		case "conflict":
			return {
				kind: "adopt",
				label: REPLACE_LABEL,
				confirm: replaceConfirm(status),
			};
		case "present":
			return {
				kind: "adopt",
				label: "Adopt record",
				confirm: `The existing record already points at ${desiredTargets(status.desiredRecords)}. NebulaCtrl will mark it as managed and keep it in step. A proxied page is then locked to Cloudflare.`,
			};
		case "managed":
			return status.error
				? { kind: "fix", label: "Retry sync" }
				: { kind: "none" };
		case "missing":
			return { kind: "fix", label: "Create records" };
		default:
			return { kind: "fix", label: "Fix DNS" };
	}
}

/**
 * The sentence for a refused fix. Unknown codes return null so the caller
 * shows the server's message.
 */
export function fixProblem(code: string | undefined): string | null {
	switch (code) {
		case "adoption_required":
			return "A record NebulaCtrl did not create is in the way. Replace it with NebulaCtrl's record to continue.";
		case "not_configured":
			return "Cloudflare is not connected for this team.";
		case "not_in_zone":
			return "The hostname is in no connected Cloudflare zone.";
		default:
			return null;
	}
}

// --- Incidents -----------------------------------------------------------------

export const INCIDENT_STATUSES = [
	"investigating",
	"identified",
	"monitoring",
	"resolved",
] as const;
export type IncidentStatus = (typeof INCIDENT_STATUSES)[number];

/** The statuses of an incident that is still open; resolving is its own action. */
export const OPEN_INCIDENT_STATUSES = INCIDENT_STATUSES.filter(
	(status): status is Exclude<IncidentStatus, "resolved"> =>
		status !== "resolved",
);

export const INCIDENT_IMPACTS = ["none", "minor", "major", "critical"] as const;
export type IncidentImpact = (typeof INCIDENT_IMPACTS)[number];

export const STATUS_LABEL: Record<IncidentStatus, string> = {
	investigating: "Investigating",
	identified: "Identified",
	monitoring: "Monitoring",
	resolved: "Resolved",
};

export const STATUS_HINT: Record<IncidentStatus, string> = {
	investigating: "We know something is wrong and are looking into it.",
	identified: "We found the cause and are working on a fix.",
	monitoring: "A fix is in place; we are watching to be sure.",
	resolved: "It is over. The page returns to its live state.",
};

export const IMPACT_LABEL: Record<IncidentImpact, string> = {
	none: "No impact",
	minor: "Minor",
	major: "Major",
	critical: "Critical",
};

/** What each impact does to the page's overall state while the incident is open. */
export const IMPACT_HINT: Record<IncidentImpact, string> = {
	none: "The page's overall state does not change.",
	minor: "The page shows degraded performance.",
	major: "The page shows a partial outage.",
	critical: "The page shows a major outage.",
};

/** Where a status sits in an incident's life: investigating is 0, resolved is 3. */
export function statusRank(status: IncidentStatus): number {
	return INCIDENT_STATUSES.indexOf(status);
}

/** How bad an impact is: none is 0, critical is 3. */
function impactRank(impact: IncidentImpact): number {
	return INCIDENT_IMPACTS.indexOf(impact);
}

/** The status a follow-up update most likely moves to: the next one, or resolved again. */
export function nextStatus(current: IncidentStatus): IncidentStatus {
	const next = INCIDENT_STATUSES[statusRank(current) + 1];
	return next ?? current;
}

/**
 * What a new update most likely says: the next status, or, on a resolved
 * incident, a reopening (any other status reopens it).
 */
export function suggestedStatus(current: IncidentStatus): IncidentStatus {
	return current === "resolved" ? "investigating" : nextStatus(current);
}

export function isOpen(incident: Pick<StatusIncidentView, "status">): boolean {
	return incident.status !== "resolved";
}

/**
 * Open incidents first, the worst impact first, then the most recently
 * updated; resolved incidents after them, newest resolution first.
 */
export function compareIncidents(
	a: StatusIncidentView,
	b: StatusIncidentView,
): number {
	const openA = isOpen(a);
	const openB = isOpen(b);
	if (openA !== openB) return openA ? -1 : 1;
	if (openA) {
		const worse = impactRank(b.impact) - impactRank(a.impact);
		if (worse !== 0) return worse;
		return b.updatedAt.localeCompare(a.updatedAt);
	}
	return (b.resolvedAt ?? b.updatedAt).localeCompare(
		a.resolvedAt ?? a.updatedAt,
	);
}

/** The timeline, newest first, however it came. */
export function timelineOf(
	incident: Pick<StatusIncidentView, "updates">,
): NonNullable<StatusIncidentView["updates"]> {
	return [...(incident.updates ?? [])].sort((a, b) =>
		b.createdAt.localeCompare(a.createdAt),
	);
}

export interface IncidentForm {
	title: string;
	impact: IncidentImpact;
	status: IncidentStatus;
	/** The first update; only used when the incident is created. */
	body: string;
	pageIds: string[];
	serviceIds: string[];
}

export function emptyIncidentForm(pageIds: string[] = []): IncidentForm {
	return {
		title: "",
		impact: "minor",
		status: "investigating",
		body: "",
		pageIds,
		serviceIds: [],
	};
}

export function incidentForm(incident: StatusIncidentView): IncidentForm {
	return {
		title: incident.title,
		impact: incident.impact,
		status: incident.status,
		body: "",
		pageIds: [...(incident.pageIds ?? [])],
		serviceIds: [...(incident.serviceIds ?? [])],
	};
}

export type IncidentFormErrors = Partial<Record<"title" | "body", string>>;

export function validateIncidentForm(
	form: IncidentForm,
	mode: "create" | "edit",
): IncidentFormErrors {
	const errors: IncidentFormErrors = {};
	const title = trimmed(form.title);
	if (title === "") errors.title = "Enter a title.";
	else if (title.length > MAX_INCIDENT_TITLE) {
		errors.title = `A title is at most ${MAX_INCIDENT_TITLE} characters.`;
	}
	if (mode === "create") {
		const body = validateUpdateBody(form.body);
		if (body) errors.body = body;
	}
	return errors;
}

/** The text of a timeline entry: required, at most 4000 characters. */
export function validateUpdateBody(value: string): string | null {
	const body = value.trim();
	if (body === "") return "Write what people should know.";
	if (body.length > MAX_UPDATE_BODY) {
		return `An update is at most ${MAX_UPDATE_BODY} characters.`;
	}
	return null;
}

export function incidentCreateBody(
	form: IncidentForm,
): CreateIncidentInputBody {
	return {
		title: trimmed(form.title),
		impact: form.impact,
		status: form.status,
		body: form.body.trim(),
		pageIds: form.pageIds,
		serviceIds: form.serviceIds,
	};
}

const sameSet = (a: readonly string[], b: readonly string[]): boolean =>
	a.length === b.length && a.every((id) => b.includes(id));

/** Only what changed: title, impact, pages and services. */
export function incidentPatch(
	form: IncidentForm,
	incident: StatusIncidentView,
): UpdateIncidentInputBody {
	const body: UpdateIncidentInputBody = {};
	if (trimmed(form.title) !== incident.title) body.title = trimmed(form.title);
	if (form.impact !== incident.impact) body.impact = form.impact;
	if (!sameSet(form.pageIds, incident.pageIds ?? []))
		body.pageIds = form.pageIds;
	if (!sameSet(form.serviceIds, incident.serviceIds ?? [])) {
		body.serviceIds = form.serviceIds;
	}
	return body;
}

/** Adds or removes one id from a selection, keeping the order it was picked in. */
export function toggleId(
	ids: readonly string[],
	id: string,
	on: boolean,
): string[] {
	const without = ids.filter((x) => x !== id);
	return on ? [...without, id] : without;
}

// --- Maintenance windows ---------------------------------------------------------

export interface WindowForm {
	title: string;
	description: string;
	/** `datetime-local` values, in the browser's own time zone. */
	startsAt: string;
	endsAt: string;
	pageIds: string[];
	serviceIds: string[];
}

const pad = (n: number): string => String(n).padStart(2, "0");

/** An instant as a `datetime-local` value in the browser's time zone; "" if invalid. */
export function toLocalInput(iso: string): string {
	const at = new Date(iso);
	if (Number.isNaN(at.getTime())) return "";
	return `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}T${pad(at.getHours())}:${pad(at.getMinutes())}`;
}

/** A `datetime-local` value (browser time zone) as an ISO instant; null if invalid. */
export function fromLocalInput(value: string): string | null {
	if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value)) return null;
	const at = new Date(value);
	return Number.isNaN(at.getTime()) ? null : at.toISOString();
}

/** "2026-10-29 14:00 UTC" for an instant. */
export function utcLabel(iso: string): string {
	const at = new Date(iso);
	if (Number.isNaN(at.getTime())) return "";
	return `${at.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** The UTC reading of a `datetime-local` value, or "" while it is incomplete. */
export function utcLabelOfLocal(value: string): string {
	const iso = fromLocalInput(value);
	return iso ? utcLabel(iso) : "";
}

/** The next full hour, for one hour. */
export function defaultWindow(now: Date): { startsAt: string; endsAt: string } {
	const start = new Date(now);
	start.setMinutes(0, 0, 0);
	start.setHours(start.getHours() + 1);
	const end = new Date(start);
	end.setHours(end.getHours() + 1);
	return {
		startsAt: toLocalInput(start.toISOString()),
		endsAt: toLocalInput(end.toISOString()),
	};
}

export function emptyWindowForm(now: Date, pageIds: string[] = []): WindowForm {
	return {
		title: "",
		description: "",
		...defaultWindow(now),
		pageIds,
		serviceIds: [],
	};
}

export function windowForm(window: MaintenanceWindowView): WindowForm {
	return {
		title: window.title,
		description: window.description,
		startsAt: toLocalInput(window.startsAt),
		endsAt: toLocalInput(window.endsAt),
		pageIds: [...(window.pageIds ?? [])],
		serviceIds: [...(window.serviceIds ?? [])],
	};
}

export type WindowFormErrors = Partial<
	Record<"title" | "description" | "startsAt" | "endsAt", string>
>;

/**
 * The window's problems: a title, two real times, the end after the start and
 * the end still ahead. A window that already started may keep its start.
 */
export function validateWindowForm(
	form: WindowForm,
	now: Date,
): WindowFormErrors {
	const errors: WindowFormErrors = {};
	const title = trimmed(form.title);
	if (title === "") errors.title = "Enter a title.";
	else if (title.length > MAX_INCIDENT_TITLE) {
		errors.title = `A title is at most ${MAX_INCIDENT_TITLE} characters.`;
	}
	if (form.description.length > MAX_WINDOW_DESCRIPTION) {
		errors.description = `A description is at most ${MAX_WINDOW_DESCRIPTION} characters.`;
	}
	const start = fromLocalInput(form.startsAt);
	const end = fromLocalInput(form.endsAt);
	if (!start) errors.startsAt = "Pick a start date and time.";
	if (!end) errors.endsAt = "Pick an end date and time.";
	if (start && end) {
		if (new Date(end).getTime() <= new Date(start).getTime()) {
			errors.endsAt = "The end must be after the start.";
		} else if (new Date(end).getTime() <= now.getTime()) {
			errors.endsAt = "The window must end in the future.";
		}
	}
	return errors;
}

export function windowCreateBody(form: WindowForm): CreateMaintenanceInputBody {
	return {
		title: trimmed(form.title),
		description: form.description.trim(),
		startsAt: fromLocalInput(form.startsAt) ?? "",
		endsAt: fromLocalInput(form.endsAt) ?? "",
		pageIds: form.pageIds,
		serviceIds: form.serviceIds,
	};
}

/** Only what changed. */
export function windowPatch(
	form: WindowForm,
	window: MaintenanceWindowView,
): UpdateMaintenanceInputBody {
	const body: UpdateMaintenanceInputBody = {};
	if (trimmed(form.title) !== window.title) body.title = trimmed(form.title);
	if (form.description.trim() !== window.description.trim()) {
		body.description = form.description.trim();
	}
	const start = fromLocalInput(form.startsAt);
	const end = fromLocalInput(form.endsAt);
	if (
		start &&
		new Date(start).getTime() !== new Date(window.startsAt).getTime()
	) {
		body.startsAt = start;
	}
	if (end && new Date(end).getTime() !== new Date(window.endsAt).getTime()) {
		body.endsAt = end;
	}
	if (!sameSet(form.pageIds, window.pageIds ?? [])) body.pageIds = form.pageIds;
	if (!sameSet(form.serviceIds, window.serviceIds ?? [])) {
		body.serviceIds = form.serviceIds;
	}
	return body;
}

export interface WindowGroups {
	active: MaintenanceWindowView[];
	upcoming: MaintenanceWindowView[];
	past: MaintenanceWindowView[];
}

/** Active (ending soonest first), upcoming (starting soonest first), past (latest first). */
export function groupWindows(
	windows: readonly MaintenanceWindowView[],
): WindowGroups {
	const by = (
		phase: (w: MaintenanceWindowView) => boolean,
		order: (a: MaintenanceWindowView, b: MaintenanceWindowView) => number,
	) => windows.filter(phase).sort(order);
	return {
		active: by(
			(w) => w.phase === "active",
			(a, b) => a.endsAt.localeCompare(b.endsAt),
		),
		upcoming: by(
			(w) => w.phase === "upcoming",
			(a, b) => a.startsAt.localeCompare(b.startsAt),
		),
		past: by(
			(w) => w.phase === "ended" || w.phase === "canceled",
			(a, b) => b.startsAt.localeCompare(a.startsAt),
		),
	};
}

/** A window can still be edited or canceled until it ends or is canceled. */
export function windowIsLive(
	window: Pick<MaintenanceWindowView, "phase">,
): boolean {
	return window.phase === "upcoming" || window.phase === "active";
}

/** "1 h 30 min", "45 min", "2 days 1 h" between two instants. */
export function windowLength(startsAt: string, endsAt: string): string {
	const minutes = Math.round(
		(new Date(endsAt).getTime() - new Date(startsAt).getTime()) / 60_000,
	);
	if (!Number.isFinite(minutes) || minutes <= 0) return "";
	const days = Math.floor(minutes / 1440);
	const hours = Math.floor((minutes % 1440) / 60);
	const rest = minutes % 60;
	const parts: string[] = [];
	if (days > 0) parts.push(`${days} ${days === 1 ? "day" : "days"}`);
	if (hours > 0) parts.push(`${hours} h`);
	if (rest > 0 && days === 0) parts.push(`${rest} min`);
	return parts.join(" ");
}

export const PHASE_LABEL: Record<MaintenanceWindowView["phase"], string> = {
	upcoming: "Upcoming",
	active: "In progress",
	ended: "Ended",
	canceled: "Canceled",
};

// --- Overall state -----------------------------------------------------------------

export type PageState =
	| "operational"
	| "degraded"
	| "partial_outage"
	| "major_outage"
	| "maintenance"
	| "unknown";

/** A monitored service's uptime state as the public page names it. */
export function componentState(uptime: string | undefined): PageState {
	switch (uptime) {
		case "up":
			return "operational";
		case "degraded":
			return "degraded";
		case "down":
			return "major_outage";
		case "maintenance":
			return "maintenance";
		default:
			return "unknown";
	}
}

const STATE_RANK: Record<PageState, number> = {
	unknown: 0,
	operational: 1,
	maintenance: 2,
	degraded: 3,
	partial_outage: 4,
	major_outage: 5,
};

const IMPACT_STATE: Record<IncidentImpact, PageState> = {
	none: "operational",
	minor: "degraded",
	major: "partial_outage",
	critical: "major_outage",
};

/**
 * The state of a whole page, as the public page computes it: the worst of the
 * components (some down is a partial outage, all down a major one) and of the
 * open incidents' impacts; maintenance only when nothing is worse; unknown
 * when no component has data.
 */
export function overallState(
	components: readonly PageState[],
	impacts: readonly IncidentImpact[],
	windowActive: boolean,
): PageState {
	let down = 0;
	let degraded = 0;
	let maintenance = 0;
	let known = 0;
	for (const state of components) {
		if (state === "major_outage") down += 1;
		else if (state === "degraded") degraded += 1;
		else if (state === "maintenance") maintenance += 1;
		if (state !== "unknown") known += 1;
	}
	let worst: PageState = "operational";
	const raise = (state: PageState) => {
		if (STATE_RANK[state] > STATE_RANK[worst]) worst = state;
	};
	if (down > 0 && down === components.length) raise("major_outage");
	else if (down > 0) raise("partial_outage");
	if (degraded > 0) raise("degraded");
	for (const impact of impacts) raise(IMPACT_STATE[impact]);
	if (STATE_RANK[worst] > STATE_RANK.maintenance) return worst;
	if (maintenance > 0 || windowActive) return "maintenance";
	if (known === 0) return "unknown";
	return worst;
}

export const STATE_LABEL: Record<PageState, string> = {
	operational: "Operational",
	degraded: "Degraded performance",
	partial_outage: "Partial outage",
	major_outage: "Major outage",
	maintenance: "Under maintenance",
	unknown: "No data yet",
};

export const STATE_KIND: Record<PageState, StatusKind> = {
	operational: "live",
	degraded: "degraded",
	partial_outage: "degraded",
	major_outage: "failed",
	maintenance: "stopped",
	unknown: "unknown",
};

/** The overall state of one page from what the dashboard knows. */
export function pageOverall(
	page: Pick<StatusPageView, "id" | "components">,
	serviceStates: ReadonlyMap<string, string>,
	incidents: readonly StatusIncidentView[],
	windows: readonly MaintenanceWindowView[],
): PageState {
	const states = (page.components ?? []).map((c) =>
		componentState(serviceStates.get(c.serviceId)),
	);
	const impacts = incidents
		.filter((i) => isOpen(i) && (i.pageIds ?? []).includes(page.id))
		.map((i) => i.impact);
	const windowActive = windows.some(
		(w) => w.phase === "active" && (w.pageIds ?? []).includes(page.id),
	);
	return overallState(states, impacts, windowActive);
}
