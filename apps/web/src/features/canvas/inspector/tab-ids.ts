export const INSPECTOR_TABS = [
	"overview",
	"deployments",
	"variables",
	"backups",
	"metrics",
	"analytics",
	"logs",
	"settings",
] as const;

export type InspectorTab = (typeof INSPECTOR_TABS)[number];

export const DEFAULT_TAB: InspectorTab = "overview";

export const TAB_LABEL: Record<InspectorTab, string> = {
	overview: "Overview",
	deployments: "Deployments",
	variables: "Variables",
	backups: "Backups",
	metrics: "Metrics",
	analytics: "Analytics",
	logs: "Logs",
	settings: "Settings",
};

/** A tab from the URL, or the default when it is missing or unknown. */
export function parseTab(value: string | undefined): InspectorTab {
	return INSPECTOR_TABS.find((tab) => tab === value) ?? DEFAULT_TAB;
}

/** The key that follows `g` to jump to a tab: g o, g d, g v, g m, g a, g l, g s. */
export const TAB_CHORD_KEY: Record<InspectorTab, string> = {
	overview: "o",
	deployments: "d",
	variables: "v",
	metrics: "m",
	analytics: "a",
	backups: "b",
	logs: "l",
	settings: "s",
};

/** The tab a chord's second key names, or undefined. */
export function tabForChordKey(key: string): InspectorTab | undefined {
	const wanted = key.toLowerCase();
	return INSPECTOR_TABS.find((tab) => TAB_CHORD_KEY[tab] === wanted);
}

/** A route service has no container: nothing to log, graph, scale or put variables on. */
const ROUTE_TABS: readonly InspectorTab[] = [
	"overview",
	"deployments",
	"settings",
];

/** What `tabsFor` reads of a service. */
export interface TabService {
	kind: string;
	config: { database?: unknown };
	/** The service's volumes; one or more makes it eligible for volume backups. */
	volumes?: readonly unknown[];
}

/**
 * Backups shows for managed databases (database backups) and for every other
 * service that has at least one volume (volume backups). A route never has
 * one. An unmanaged database has neither.
 */
export function hasBackupsTab(service: TabService): boolean {
	// A stack's volumes are declared in its compose file; volume backups do not cover them.
	if (service.kind === "route" || service.kind === "stack") return false;
	if (service.kind === "database") return service.config.database !== undefined;
	return (service.volumes?.length ?? 0) > 0;
}

/** A stack shows what the agent can report for it: no Backups, and Analytics (per-service proxy traffic) only for apps. */
const STACK_TABS: readonly InspectorTab[] = [
	"overview",
	"deployments",
	"variables",
	"metrics",
	"logs",
	"settings",
];

/**
 * The tabs a service shows: a route shows Overview, Deployments and Settings,
 * and Backups needs a managed database or a service with a volume. A tab the
 * service does not have (from a stale URL or a chord) resolves to the default.
 */
export function tabsFor(service: TabService): readonly InspectorTab[] {
	if (service.kind === "route") return ROUTE_TABS;
	if (service.kind === "stack") return STACK_TABS;
	return hasBackupsTab(service)
		? INSPECTOR_TABS
		: INSPECTOR_TABS.filter((tab) => tab !== "backups");
}

export function resolveTab(
	tab: InspectorTab,
	service: TabService,
): InspectorTab {
	return tabsFor(service).includes(tab) ? tab : DEFAULT_TAB;
}
