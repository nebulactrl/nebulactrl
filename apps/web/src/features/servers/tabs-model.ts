// The server page's tabs and its old addresses, as plain data. Every tab has
// its own path under `/servers/<id>`; the page used to be one route whose tab
// lived in the hash. Nothing here touches React, so the map is tested directly.

const SERVER_TABS = [
	"overview",
	"traffic",
	"proxy",
	"security",
	"updates",
	"settings",
] as const;

export type ServerTab = (typeof SERVER_TABS)[number];

/** The tab a path belongs to: `/servers/<id>/proxy` is Proxy, anything unknown is Overview. */
export function serverTabOf(pathname: string, serverId: string): ServerTab {
	const prefix = `/servers/${serverId}`;
	if (!pathname.startsWith(prefix)) return "overview";
	const [segment] = pathname.slice(prefix.length).split("/").filter(Boolean);
	return (
		SERVER_TABS.find((tab) => tab === segment && tab !== "overview") ??
		"overview"
	);
}

/** Where an old server address now lives: a tab and, for Settings, a section. */
export interface ServerTarget {
	tab: ServerTab;
	/** The Settings section, when `tab` is `settings`. */
	section?: string;
}

// The old single page had one route and these hash ids: the four tabs (`#proxy`,
// `#security`, `#updates`; Overview had none) and the anchors of its sections.
// `?tab=` names the same ids. Kept for good: mail, webhooks and bookmarks
// already carry them.
const LEGACY: Readonly<Record<string, ServerTarget>> = {
	overview: { tab: "overview" },
	host: { tab: "overview" },
	charts: { tab: "overview" },
	services: { tab: "overview" },
	traffic: { tab: "traffic" },
	proxy: { tab: "proxy" },
	security: { tab: "security" },
	updates: { tab: "updates" },
	settings: { tab: "settings", section: "general" },
	mesh: { tab: "settings", section: "mesh" },
};

/**
 * Where an old server address now lives, or `null` for an address that is not
 * an old one (no hash and no `?tab=`, or an anchor the page never had). The
 * hash wins over `?tab=`. `drain` travels along: it opens the drain sheet on
 * whichever tab the person lands on.
 */
export function legacyServerRedirect(
	hash: string,
	search: URLSearchParams,
): (ServerTarget & { drain: boolean }) | null {
	const id = (hash.replace(/^#/, "") || search.get("tab") || "").trim();
	if (!Object.hasOwn(LEGACY, id)) return null;
	const target = LEGACY[id];
	if (!target) return null;
	const drain = search.get("drain");
	return { ...target, drain: drain === "1" || drain === "true" };
}
