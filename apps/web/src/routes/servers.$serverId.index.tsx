import { createFileRoute, redirect } from "@tanstack/react-router";
import { OverviewTab } from "#/features/servers/overview/overview-tab";
import { legacyServerRedirect } from "#/features/servers/tabs-model";

// `/servers/<id>` is the Overview. The one page this used to be had its tabs
// and sections in the hash (`#proxy`, `#settings`, `#mesh`) and `?tab=`; mail,
// webhooks and bookmarks still carry them, so read them here and move to the
// address that holds that tab now.
export const Route = createFileRoute("/servers/$serverId/")({
	beforeLoad: ({ location, params }) => {
		const legacy = legacyServerRedirect(
			location.hash,
			new URLSearchParams(location.searchStr),
		);
		if (!legacy) return;
		const { serverId } = params;
		const search = legacy.drain ? { drain: true as const } : {};
		switch (legacy.tab) {
			case "traffic":
				throw redirect({
					to: "/servers/$serverId/traffic",
					params: { serverId },
					search,
					replace: true,
				});
			case "proxy":
				throw redirect({
					to: "/servers/$serverId/proxy",
					params: { serverId },
					search,
					replace: true,
				});
			case "security":
				throw redirect({
					to: "/servers/$serverId/security",
					params: { serverId },
					search,
					replace: true,
				});
			case "updates":
				throw redirect({
					to: "/servers/$serverId/updates",
					params: { serverId },
					search,
					replace: true,
				});
			case "settings":
				throw redirect({
					to: "/servers/$serverId/settings/$section",
					params: { serverId, section: legacy.section ?? "general" },
					search,
					replace: true,
				});
			default:
				throw redirect({
					to: "/servers/$serverId",
					params: { serverId },
					search,
					replace: true,
				});
		}
	},
	component: OverviewTab,
});
