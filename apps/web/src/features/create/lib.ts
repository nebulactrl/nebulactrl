import type { EngineView } from "#/api/gen/types.gen";
import type { Server } from "#/api/models";
import {
	CATEGORY_LABEL,
	needsLicenseAck,
	sortEngines,
} from "#/features/databases/engines";

/** The words a service name is taken from: a repository or an image. */
export function suggestName(source: {
	kind: "image" | "git";
	image: string;
	gitUrl: string;
}): string {
	const raw = source.kind === "image" ? source.image : source.gitUrl;
	return nameFromReference(raw.trim(), source.kind);
}

function nameFromReference(raw: string, kind: "image" | "git"): string {
	if (raw === "") return "";
	let path = raw;
	if (kind === "git") {
		// `git@github.com:acme/shop.git` and `https://host/acme/shop.git/`.
		path = path.replace(/[?#].*$/, "").replace(/\/+$/, "");
		path = path.replace(/\.git$/, "");
	} else {
		path = path.split("@")[0] ?? path;
	}
	const last =
		path
			.split(kind === "image" ? "/" : /[/:]/)
			.filter(Boolean)
			.pop() ?? "";
	// An image's tag is not part of its name: `whoami:latest` -> `whoami`.
	const base = kind === "image" ? (last.split(":")[0] ?? last) : last;
	return base
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-+|-+$/g, "");
}

export interface Capacity {
	cpu: number | null;
	mem: number | null;
	disk: number | null;
}

const percent = (used: number, total: number): number | null =>
	total > 0 ? Math.min(100, Math.max(0, (used / total) * 100)) : null;

/**
 * What is in use on a host, from its latest heartbeat sample; null before the
 * first sample, so a bar is never drawn for a number nobody measured.
 */
export function capacityOf(server: Pick<Server, "latest">): Capacity | null {
	const latest = server.latest;
	if (!latest) return null;
	return {
		cpu: Math.min(100, Math.max(0, latest.cpuPercent)),
		mem: percent(latest.memUsed, latest.memTotal),
		disk: percent(latest.diskUsed, latest.diskTotal),
	};
}

/** A port field's value: a number, undefined when empty, NaN when invalid. */
export function parsePort(text: string): number | undefined {
	const trimmed = text.trim();
	if (trimmed === "") return undefined;
	const value = Number(trimmed);
	return Number.isInteger(value) && value >= 1 && value <= 65535
		? value
		: Number.NaN;
}

export const REPLICAS_MAX = 10;

/**
 * Replicas need a stateless service: an HTTP port and no volume. A database
 * always has a volume. (The server enforces the same rule; this only decides
 * whether the field is worth showing.)
 */
export function replicasEligible(input: { port: number | undefined }): boolean {
	return input.port !== undefined && !Number.isNaN(input.port);
}

export type MenuEntryId =
	| "git"
	| "image"
	| "database"
	| "templates"
	| "compose"
	| "coolify"
	| "stack"
	| "external"
	| "port";

/** A database engine's row, keyed `engine:<id>` (ids come from the catalog). */
type EngineEntryId = `engine:${string}`;

export interface MenuEntry {
	id: MenuEntryId | EngineEntryId;
	label: string;
	hint: string;
	keywords: readonly string[];
	/** Entries under Database; they show in the root list only when filtering. */
	parent?: "database";
	/** Engine rows: the catalog id the database dialog opens on. */
	engine?: string;
	/** Engine rows: a short licence warning shown as a chip. */
	chip?: string;
}

const MENU_ENTRIES: readonly MenuEntry[] = [
	{
		id: "git",
		label: "Git repository",
		hint: "Dockerfile or Railpack build",
		keywords: [
			"github",
			"git",
			"repo",
			"repository",
			"gitlab",
			"dockerfile",
			"railpack",
		],
	},
	{
		id: "image",
		label: "Docker image",
		hint: "from a registry",
		keywords: ["docker", "image", "container", "registry"],
	},
	{
		id: "database",
		label: "Database",
		hint: "Valkey, PostgreSQL, MySQL…",
		keywords: ["database", "db", "sql", "data"],
	},
	{
		id: "templates",
		label: "Templates",
		hint: "one-click apps and games",
		keywords: [
			"template",
			"templates",
			"app",
			"apps",
			"stack",
			"install",
			"one-click",
			"game",
			"homelab",
		],
	},
	{
		id: "compose",
		label: "Import Docker Compose",
		hint: "Paste or upload a compose file",
		keywords: [
			"compose",
			"docker-compose",
			"docker compose",
			"yaml",
			"yml",
			"import",
			"stack",
			"paste",
		],
	},
	{
		id: "coolify",
		label: "Import from Coolify",
		hint: "projects, apps and databases",
		keywords: [
			"coolify",
			"import",
			"migrate",
			"migration",
			"move",
			"paas",
			"projects",
			"applications",
		],
	},
	{
		id: "stack",
		label: "Compose stack",
		hint: "run a compose file as one service",
		keywords: [
			"compose",
			"docker-compose",
			"docker compose",
			"stack",
			"yaml",
			"yml",
			"multi-container",
			"services",
		],
	},
	{
		id: "external",
		label: "External service",
		hint: "route to an address",
		keywords: [
			"external",
			"route",
			"proxy",
			"upstream",
			"lan",
			"home assistant",
			"nas",
			"tailscale",
			"tailnet",
			"http",
		],
	},
	{
		id: "port",
		label: "TCP/UDP port",
		hint: "expose a port",
		keywords: [
			"tcp",
			"udp",
			"port",
			"expose",
			"route",
			"game",
			"minecraft",
			"smtp",
			"proxy",
		],
	},
];

/** The ways to add a service, in menu order: the Create button and the canvas menu list the same. */
export const ROOT_ENTRIES: readonly MenuEntry[] = MENU_ENTRIES.filter(
	(entry) => !entry.parent,
);

/** What a category is also called, so "cache" and "kv" reach key-value engines. */
const CATEGORY_KEYWORDS: Record<EngineView["category"], readonly string[]> = {
	relational: ["sql", "relational"],
	keyvalue: ["cache", "kv", "key-value", "keyvalue"],
	document: ["nosql", "document"],
	analytics: ["olap", "analytics", "column"],
};

/**
 * The rows under Database: one per engine of the catalog, in the order the
 * catalog UI uses (the default key-value engine, Valkey, first).
 */
function engineEntries(engines: readonly EngineView[]): readonly MenuEntry[] {
	return sortEngines(engines).map((engine) => ({
		id: `engine:${engine.id}` as const,
		label: engine.label,
		hint: engine.default
			? `${CATEGORY_LABEL[engine.category]} · default`
			: CATEGORY_LABEL[engine.category],
		keywords: [
			engine.id,
			"database",
			...CATEGORY_KEYWORDS[engine.category],
			...engine.description
				.toLowerCase()
				.split(/[^a-z0-9-]+/)
				.filter(Boolean),
			...(engine.flavors ?? []).map((flavor) => flavor.id),
		],
		parent: "database" as const,
		engine: engine.id,
		...(needsLicenseAck(engine) ? { chip: "licence" } : {}),
	}));
}

/**
 * The rows the menu shows. Empty query: the root list (Database stands for
 * its engines) or, inside Database, the engines. A query searches every
 * entry at once, engines included, so "post" reaches PostgreSQL directly.
 * `engines` is the catalog; before it loads, Database has no children.
 */
export function visibleEntries(
	query: string,
	submenu: "database" | null,
	engines: readonly EngineView[] = [],
): MenuEntry[] {
	const entries = [...MENU_ENTRIES, ...engineEntries(engines)];
	const text = query.trim().toLowerCase();
	if (text === "") {
		return entries.filter((entry) =>
			submenu ? entry.parent === submenu : entry.parent === undefined,
		);
	}
	const words = text.split(/\s+/);
	return entries.filter((entry) => {
		if (!matches(entry, words)) return false;
		// Database stands for its engines; when an engine itself matches
		// ("post"), the engine is the answer and the parent only adds a step.
		if (entry.id !== "database") return true;
		const label = entry.label.toLowerCase();
		if (words.every((word) => label.includes(word))) return true;
		return !entries.some(
			(child) => child.parent === "database" && matches(child, words),
		);
	});
}

function matches(entry: MenuEntry, words: readonly string[]): boolean {
	const haystack = [entry.label, ...entry.keywords].join(" ").toLowerCase();
	return words.every((word) => haystack.includes(word));
}
