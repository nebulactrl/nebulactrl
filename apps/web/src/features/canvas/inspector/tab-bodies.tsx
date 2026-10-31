import { type ComponentType, lazy } from "react";
import type { Service } from "#/api/models";
import { Skeleton } from "#/components/ui/skeleton";
import type { InspectorTab } from "./tab-ids";
import type { ServiceTabProps } from "./tabs/types";

type TabBody = ComponentType<ServiceTabProps>;
type Loader = () => Promise<{ default: TabBody }>;

/**
 * A tab body that loads its chunk on first use. Each body pulls in a whole
 * feature (logs, charts, the settings forms, the deployments timeline), which
 * made the canvas chunk the size of the console; the open inspector mounts one
 * of them, so the rest wait until they are asked for. `preload` starts the
 * download without rendering anything.
 */
function lazyTab(load: Loader): TabBody & { preload: Loader } {
	return Object.assign(lazy(load), { preload: load });
}

const OverviewTab = lazyTab(() =>
	import("./tabs/overview").then((m) => ({ default: m.OverviewTab })),
);
const DeploymentsTab = lazyTab(() =>
	import("./tabs/deployments").then((m) => ({ default: m.DeploymentsTab })),
);
const VariablesTab = lazyTab(() =>
	import("./tabs/variables").then((m) => ({ default: m.VariablesTab })),
);
const MetricsTab = lazyTab(() =>
	import("./tabs/metrics").then((m) => ({ default: m.MetricsTab })),
);
const AnalyticsTab = lazyTab(() =>
	import("./tabs/analytics").then((m) => ({ default: m.AnalyticsTab })),
);
const LogsTab = lazyTab(() =>
	import("./tabs/logs").then((m) => ({ default: m.LogsTab })),
);
const SettingsTab = lazyTab(() =>
	import("./tabs/settings").then((m) => ({ default: m.SettingsTab })),
);
const BackupsTab = lazyTab(() =>
	import("./tabs/backups").then((m) => ({ default: m.BackupsTab })),
);
const RouteOverviewTab = lazyTab(() =>
	import("#/features/proxy/inspector/route-overview").then((m) => ({
		default: m.RouteOverviewTab,
	})),
);
const RouteSettingsTab = lazyTab(() =>
	import("#/features/proxy/inspector/route-settings").then((m) => ({
		default: m.RouteSettingsTab,
	})),
);

export const TAB_BODY: Record<InspectorTab, TabBody & { preload: Loader }> = {
	overview: OverviewTab,
	deployments: DeploymentsTab,
	variables: VariablesTab,
	metrics: MetricsTab,
	analytics: AnalyticsTab,
	logs: LogsTab,
	settings: SettingsTab,
	backups: BackupsTab,
};

/** A route service shows its own Overview and Settings; Deployments is shared. */
export const ROUTE_TAB_BODY: Partial<
	Record<InspectorTab, TabBody & { preload: Loader }>
> = {
	overview: RouteOverviewTab,
	deployments: DeploymentsTab,
	settings: RouteSettingsTab,
};

/** The body for a tab of a service: what the inspector renders. */
export function bodyFor(
	service: Pick<Service, "kind">,
	tab: InspectorTab,
): TabBody {
	return service.kind === "route"
		? (ROUTE_TAB_BODY[tab] ?? RouteOverviewTab)
		: TAB_BODY[tab];
}

/**
 * Starts downloading a service's Overview, the tab the inspector opens on.
 * Called when a card is hovered or focused, and once the canvas is idle, so
 * the first click mounts a body that is already there.
 */
export function preloadOverview(service: Pick<Service, "kind">): void {
	void (service.kind === "route" ? RouteOverviewTab : OverviewTab)
		.preload()
		.catch(() => undefined);
}

/** Downloads every other tab of a service while the browser has nothing to do. */
export function preloadRemainingTabs(service: Pick<Service, "kind">): void {
	const bodies =
		service.kind === "route"
			? [RouteOverviewTab, DeploymentsTab, RouteSettingsTab]
			: Object.values(TAB_BODY);
	for (const body of bodies) void body.preload().catch(() => undefined);
}

/**
 * What stands in the tab's place while its chunk downloads (normally never
 * seen: the chunk is preloaded). Quiet blocks in the Overview's own padding, so
 * nothing moves when the body arrives. Not announced: it is gone in a moment.
 */
export function TabFallback() {
	return (
		<div aria-hidden className="flex flex-col gap-5 px-4 pt-4 pb-6">
			<Skeleton className="h-24 w-full rounded-card" />
			<Skeleton className="h-24 w-full rounded-card" />
			<Skeleton className="h-9 w-full rounded-card" />
		</div>
	);
}
