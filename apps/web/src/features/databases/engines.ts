import type {
	DatabaseView,
	EngineView,
	FlavorView,
	VersionView,
} from "#/api/gen/types.gen";

/**
 * Pure helpers over the engine catalog (`GET /database-engines`). The catalog
 * is the single source of truth for what can be created: nothing here lists
 * an engine, a version or a licence by name.
 */

export type { EngineView, FlavorView, VersionView };

const DATABASE_NAME_PATTERN = /^[a-z_][a-z0-9_]{0,62}$/;

/** What the server falls back to when the field is left empty. */
export const DEFAULT_DATABASE_NAME = "app";

export const CATEGORY_LABEL: Record<EngineView["category"], string> = {
	relational: "Relational",
	keyvalue: "Key-value",
	document: "Document",
	analytics: "Analytics",
};

export const LICENSE_CLASS_LABEL: Record<EngineView["licenseClass"], string> = {
	permissive: "Permissive licence",
	copyleft: "Copyleft licence",
	"source-available": "Source-available licence",
};

/**
 * The order the picker lists engines in: the catalog's own order, except that
 * the default key-value engine (Valkey) leads.
 */
export function sortEngines(engines: readonly EngineView[]): EngineView[] {
	const lead = (engine: EngineView) =>
		engine.default && engine.category === "keyvalue" ? 0 : 1;
	return [...engines].sort((a, b) => lead(a) - lead(b));
}

/**
 * The engine a database dialog opens on: the one asked for, else the default
 * key-value engine (Valkey), else the first the catalog lists.
 */
export function initialEngine(
	engines: readonly EngineView[],
	wanted?: string,
): EngineView | undefined {
	const sorted = sortEngines(engines);
	return (
		sorted.find((engine) => engine.id === wanted) ??
		sorted.find((engine) => engine.default && engine.category === "keyvalue") ??
		sorted.find((engine) => engine.default) ??
		sorted[0]
	);
}

/** Source-available (and any opt-in) engines need an explicit licence review. */
export function needsLicenseAck(engine: EngineView): boolean {
	return engine.optIn || engine.licenseClass === "source-available";
}

/** Key-value engines have no named database; every other engine does. */
export function takesDatabaseName(engine: EngineView): boolean {
	return engine.category !== "keyvalue";
}

export function isValidDatabaseName(name: string): boolean {
	return DATABASE_NAME_PATTERN.test(name);
}

/** The flavor of an engine by id, when it has it. */
export function flavorOf(
	engine: EngineView,
	flavorId: string | undefined,
): FlavorView | undefined {
	if (!flavorId) return undefined;
	return (engine.flavors ?? []).find((flavor) => flavor.id === flavorId);
}

/** The versions offered for an engine, narrowed to a flavor's when one is chosen. */
export function versionsFor(
	engine: EngineView,
	flavorId?: string,
): VersionView[] {
	const all = engine.versions ?? [];
	const flavor = flavorOf(engine, flavorId);
	if (!flavor) return all;
	const allowed = new Set(flavor.versions ?? []);
	return all.filter((version) => allowed.has(version.id));
}

/**
 * The preselected version: the engine's default, or, when a flavor does not
 * offer that one, the newest the flavor does.
 */
export function defaultVersion(
	engine: EngineView,
	flavorId?: string,
): string | undefined {
	const offered = versionsFor(engine, flavorId);
	const preferred = offered.find((version) => version.default);
	if (preferred) return preferred.id;
	const newest = [...offered].sort((a, b) => compareVersions(b.id, a.id))[0];
	return newest?.id;
}

/** Dotted numeric comparison: 8.4 < 9.7 < 18, 9 < 10. Non-numeric parts sort last. */
export function compareVersions(a: string, b: string): number {
	const left = a.split(".");
	const right = b.split(".");
	const length = Math.max(left.length, right.length);
	for (let index = 0; index < length; index++) {
		const x = Number(left[index] ?? 0);
		const y = Number(right[index] ?? 0);
		if (Number.isNaN(x) || Number.isNaN(y)) {
			const diff = (left[index] ?? "").localeCompare(right[index] ?? "");
			if (diff !== 0) return diff;
			continue;
		}
		if (x !== y) return x < y ? -1 : 1;
	}
	return 0;
}

/** The versions a database on `current` can be upgraded to: strictly newer ones. */
export function upgradeTargets(
	engine: EngineView,
	current: string,
	flavorId?: string,
): VersionView[] {
	return versionsFor(engine, flavorId)
		.filter((version) => compareVersions(version.id, current) > 0)
		.sort((a, b) => compareVersions(a.id, b.id));
}

/** The label of a version id, falling back to the id itself. */
export function versionLabel(engine: EngineView, id: string): string {
	return (engine.versions ?? []).find((v) => v.id === id)?.label ?? id;
}

export interface CreateDatabaseDraft {
	engine: EngineView | undefined;
	version: string | undefined;
	databaseName: string;
	serverId: string;
	name: string;
	licenseAccepted: boolean;
	/** Names of the services already in the environment. */
	taken?: readonly string[];
}

const sameName = (a: string, b: string) =>
	a.trim().toLowerCase() === b.trim().toLowerCase();

/** Whether a service in the environment already has this name. */
export function nameTaken(name: string, taken: readonly string[]): boolean {
	return taken.some((other) => sameName(other, name));
}

/** `base`, or `base-2`, `base-3`… for the first name nobody has. */
export function freeName(base: string, taken: readonly string[]): string {
	if (!nameTaken(base, taken)) return base;
	for (let n = 2; ; n++) {
		const candidate = `${base}-${n}`;
		if (!nameTaken(candidate, taken)) return candidate;
	}
}

/**
 * Why Create is disabled, or null when it is not. A licence-gated engine
 * stays blocked until its licence is ticked.
 */
export function createBlocker(draft: CreateDatabaseDraft): string | null {
	const { engine } = draft;
	if (!engine) return "Choose an engine.";
	if (!draft.version) return "Choose a version.";
	if (draft.name.trim() === "") return "Name the database.";
	if (nameTaken(draft.name, draft.taken ?? [])) {
		return `A service named ${draft.name.trim()} already exists here.`;
	}
	if (takesDatabaseName(engine) && draft.databaseName !== "") {
		if (!isValidDatabaseName(draft.databaseName)) {
			return "The database name is not valid.";
		}
	}
	if (draft.serverId === "") return "Choose a server.";
	if (needsLicenseAck(engine) && !draft.licenseAccepted) {
		return "Confirm that you have reviewed the licence.";
	}
	return null;
}

/** What `getDatabase` returns narrowed to the fields the UI keys on. */
export function specLabel(
	database: Pick<DatabaseView, "engine" | "spec">,
): string {
	const { engine, spec } = database;
	const version = versionLabel(engine, spec.version);
	const label = version.startsWith(engine.label)
		? version
		: `${engine.label} ${version}`;
	const flavor = flavorOf(engine, spec.flavor);
	return flavor ? `${label} · ${flavor.label}` : label;
}
