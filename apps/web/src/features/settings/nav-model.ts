import type { Role } from "#/features/teams/roles";

// The settings pages of every area, as plain data: the shell draws the list
// from it, ⌘K registers one command per page, and the filter field narrows it.
// Nothing here touches React, so what each role may see is tested directly.

export type SettingsArea = "account" | "team" | "admin" | "project" | "server";

/** The parts of the signed-in person that decide which pages they may open. */
export interface SettingsViewer {
	instanceAdmin: boolean;
}

export interface SettingsPage {
	area: SettingsArea;
	/** The last URL segment ("members"), and the id of the page's section. */
	section: string;
	/** The h3 above the page in the section list; absent for the ungrouped first group. */
	group?: string;
	title: string;
	/** One line: what the page is for. */
	description: string;
	/** Words that find the page in the filter and in ⌘K without being drawn. */
	keywords: readonly string[];
	/** The docs page its "Learn more" opens (slug with optional anchor). */
	docs?: string;
	/** Whether this person can use the page. Pages they cannot use are not listed. */
	visible: (me: SettingsViewer, role: Role | undefined) => boolean;
}

const everyone = () => true;
const manages = (_me: SettingsViewer, role: Role | undefined) =>
	role === "owner" || role === "admin";
const instanceAdmin = (me: SettingsViewer) => me.instanceAdmin;

/** Owners and admins manage these pages; everyone else is not offered them. */
export const TEAM_MANAGE_SENTENCE = "Owners and admins manage this.";

export const SETTINGS_PAGES: readonly SettingsPage[] = [
	// Account: you, in every team.
	{
		area: "account",
		section: "profile",
		title: "Profile",
		description: "Your name and password.",
		keywords: ["name", "password", "email", "me", "user"],
		visible: everyone,
	},
	{
		area: "account",
		section: "security",
		title: "Security",
		description: "Two-step verification and linked sign-in accounts.",
		keywords: [
			"2fa",
			"two-step",
			"totp",
			"passkey",
			"authenticator",
			"recovery codes",
			"sso",
			"password",
		],
		docs: "security/login-methods",
		visible: everyone,
	},
	{
		area: "account",
		section: "tokens",
		title: "API tokens",
		description: "Credentials for the CLI, CI and AI clients.",
		keywords: ["api", "token", "cli", "ci", "credentials", "key", "pat"],
		docs: "operations/teams#api-tokens",
		visible: everyone,
	},
	{
		area: "account",
		section: "mcp-clients",
		title: "MCP clients",
		description: "Connect an AI client to this control plane.",
		keywords: ["mcp", "ai", "agent", "model context protocol", "endpoint"],
		visible: everyone,
	},

	// Team settings: this team, for everyone in it.
	{
		area: "team",
		section: "general",
		title: "General",
		description: "The team's name and two-step requirement.",
		keywords: [
			"team name",
			"rename",
			"slug",
			"two-step",
			"2fa",
			"require",
			"leave",
			"delete team",
			"danger",
		],
		visible: everyone,
	},
	{
		area: "team",
		section: "members",
		group: "People",
		title: "Members",
		description: "Who is in this team and what they can do.",
		keywords: [
			"people",
			"invite",
			"role",
			"owner",
			"admin",
			"developer",
			"viewer",
		],
		docs: "operations/teams",
		visible: everyone,
	},
	{
		area: "team",
		section: "audit-log",
		group: "People",
		title: "Audit log",
		description: "Who changed what, newest first.",
		keywords: ["audit", "history", "activity", "changes", "events"],
		visible: manages,
	},
	{
		area: "team",
		section: "build-location",
		group: "Build and ship",
		title: "Build location",
		description: "Where this team's services are built by default.",
		keywords: ["builds", "build server", "builder", "default"],
		docs: "operations/build-servers",
		visible: manages,
	},
	{
		area: "team",
		section: "image-registries",
		group: "Build and ship",
		title: "Image registries",
		description: "Where built images are stored.",
		keywords: ["registry", "registries", "docker", "images", "ghcr", "ecr"],
		docs: "operations/build-servers",
		visible: manages,
	},
	{
		area: "team",
		section: "integrations",
		group: "Build and ship",
		title: "Integrations",
		description: "Connect git, secret managers, Tailscale and Cloudflare.",
		keywords: [
			"git",
			"github",
			"gitlab",
			"deploy on push",
			"secrets",
			"vault",
			"tailscale",
			"cloudflare",
			"dns",
		],
		docs: "operations/git-providers",
		visible: manages,
	},
	{
		area: "team",
		section: "private-networks",
		group: "Servers",
		title: "Private networks",
		description: "Private links between services of different projects.",
		keywords: ["networks", "network", "link", "internal"],
		docs: "operations/networking",
		visible: everyone,
	},
	{
		area: "team",
		section: "server-mesh",
		group: "Servers",
		title: "Server mesh",
		description: "Let services and replicas reach each other across servers.",
		keywords: ["mesh", "tailnet", "headscale", "acl", "replicas", "devices"],
		docs: "operations/mesh",
		visible: everyone,
	},
	{
		area: "team",
		section: "headscale",
		group: "Servers",
		title: "Private network (Headscale)",
		description: "A tailnet control server that NebulaCtrl runs for this team.",
		keywords: [
			"headscale",
			"tailnet",
			"tailscale",
			"vpn",
			"private network",
			"set up for me",
		],
		docs: "operations/platform-services",
		visible: manages,
	},
	{
		area: "team",
		section: "backup-storage",
		group: "Data and alerts",
		title: "Backup storage",
		description: "Where database backups are stored.",
		keywords: [
			"backups",
			"s3",
			"bucket",
			"destination",
			"database",
			"built-in",
			"quota",
		],
		docs: "operations/database-backups",
		visible: manages,
	},
	{
		area: "team",
		section: "notifications",
		group: "Data and alerts",
		title: "Notifications",
		description: "Alerts about deployments, servers, backups and certificates.",
		keywords: ["alerts", "email", "slack", "webhook", "channels"],
		docs: "operations/notifications",
		visible: manages,
	},
	{
		area: "team",
		section: "log-forwarding",
		group: "Data and alerts",
		title: "Log forwarding",
		description: "Send your services' logs to another system.",
		keywords: [
			"logs",
			"drains",
			"drain",
			"loki",
			"syslog",
			"otel",
			"ship",
			"central logs",
		],
		docs: "operations/logs",
		visible: manages,
	},

	// Admin: this installation. Instance admin only.
	{
		area: "admin",
		section: "updates",
		group: "Install",
		title: "Updates",
		description: "The installed version and new releases.",
		keywords: ["version", "upgrade", "release", "release notes", "update"],
		visible: instanceAdmin,
	},
	{
		area: "admin",
		section: "backup",
		group: "Install",
		title: "Control-plane backup",
		description: "Back up the control plane's own database.",
		keywords: [
			"backup",
			"secret key",
			"restore",
			"database",
			"instance",
			"schedule",
		],
		visible: instanceAdmin,
	},
	{
		area: "admin",
		section: "built-in-services",
		group: "Install",
		title: "Built-in services",
		description:
			"Backup storage, image registry and central logs that every team shares, and each team's quota.",
		keywords: [
			"s3",
			"storage",
			"backups",
			"quota",
			"registry",
			"images",
			"loki",
			"logs",
			"slices",
			"purge",
			"instance",
		],
		docs: "operations/platform-services",
		visible: instanceAdmin,
	},
	{
		area: "admin",
		section: "sign-in",
		group: "Access",
		title: "Sign-in and teams",
		description: "How people sign in, and who may create teams.",
		keywords: [
			"sso",
			"single sign-on",
			"oidc",
			"providers",
			"google",
			"okta",
			"sign-up",
			"create teams",
			"login",
		],
		docs: "security/sso-setup",
		visible: instanceAdmin,
	},
	{
		area: "admin",
		section: "dashboard-access",
		group: "Access",
		title: "Dashboard access",
		description: "Which addresses may open the dashboard, and its certificate.",
		keywords: [
			"ip",
			"allow list",
			"allowlist",
			"cidr",
			"certificate",
			"tls",
			"dashboard",
		],
		visible: instanceAdmin,
	},
	{
		area: "admin",
		section: "email-relay",
		group: "Features",
		title: "Email relay",
		description: "Send invite emails through your own mail server.",
		keywords: ["smtp", "mail", "email", "invites", "relay"],
		visible: instanceAdmin,
	},
	{
		area: "admin",
		section: "mcp",
		group: "Features",
		title: "MCP",
		description: "Let AI clients connect to this installation.",
		keywords: ["mcp", "ai", "agent", "model context protocol", "switch"],
		visible: instanceAdmin,
	},

	// Server: one host's own pages, under its Settings tab.
	{
		area: "server",
		section: "general",
		title: "General",
		description: "The server's name, public IP, base domain and ACME email.",
		keywords: [
			"name",
			"rename",
			"ip",
			"public ip",
			"domain",
			"base domain",
			"acme",
			"email",
			"certificate",
		],
		visible: manages,
	},
	{
		area: "server",
		section: "scheduling",
		title: "Scheduling",
		description: "Which workloads run here: apps, builds or both.",
		keywords: [
			"role",
			"workloads",
			"accept",
			"builds",
			"build server",
			"placement",
			"previews",
			"emulation",
		],
		docs: "operations/build-servers",
		visible: manages,
	},
	{
		area: "server",
		section: "monitoring",
		title: "Monitoring",
		description: "Metrics, disk alerts, traffic analytics and geolocation.",
		keywords: [
			"metrics",
			"retention",
			"alerts",
			"disk",
			"threshold",
			"traffic",
			"analytics",
			"geolocation",
			"geoip",
			"maxmind",
			"origins",
		],
		docs: "operations/monitoring",
		visible: manages,
	},
	{
		area: "server",
		section: "logs-and-disk",
		title: "Logs and disk",
		description: "Stored logs, space used and Docker cleanup.",
		keywords: [
			"logs",
			"stored logs",
			"retention",
			"space",
			"disk",
			"docker",
			"cleanup",
			"prune",
			"images",
			"volumes",
		],
		docs: "operations/logs",
		visible: manages,
	},
	{
		area: "server",
		section: "mesh",
		title: "Mesh",
		description: "Whether this server joins the private mesh.",
		keywords: ["tailscale", "tailnet", "private network", "vpn", "mesh"],
		visible: manages,
	},
	{
		area: "server",
		section: "host-shell",
		title: "Host shell",
		description: "A root shell on this machine, from the browser.",
		keywords: ["terminal", "ssh", "root", "shell", "console"],
		visible: manages,
	},
	{
		area: "server",
		section: "danger",
		title: "Danger zone",
		description: "Unrestricted mode, drain, revoke and remove.",
		keywords: [
			"unrestricted",
			"drain",
			"evacuate",
			"revoke",
			"remove",
			"delete",
			"forget",
			"destroy",
		],
		visible: manages,
	},

	// Project: one project's own page.
	{
		area: "project",
		section: "general",
		title: "General",
		description: "The name, description and identifiers.",
		keywords: ["name", "rename", "description", "slug", "id"],
		visible: everyone,
	},
	{
		area: "project",
		section: "environments",
		title: "Environments",
		description:
			"Each environment has its own services, variables and domains.",
		keywords: ["production", "staging", "preview", "environment"],
		visible: everyone,
	},
	{
		area: "project",
		section: "build-location",
		title: "Build location",
		description: "Where this project's services are built.",
		keywords: ["builds", "build server", "builder", "registry"],
		docs: "operations/build-servers",
		visible: manages,
	},
	{
		area: "project",
		section: "danger",
		title: "Danger zone",
		description: "Delete this project.",
		keywords: ["delete", "remove", "destroy"],
		visible: everyone,
	},
];

/**
 * Team sections that were renamed: the old address opens the page that holds
 * the same thing now. "Built-in services" became Admin's page, and what stayed
 * per team is the Headscale page.
 */
const TEAM_SECTION_ALIASES: Readonly<Record<string, string>> = {
	"built-in-services": "headscale",
};

/** The team section an address names, with a renamed one mapped to its new name. */
export function teamSectionAlias(section: string): string {
	return Object.hasOwn(TEAM_SECTION_ALIASES, section)
		? (TEAM_SECTION_ALIASES[section] ?? section)
		: section;
}

/** The pages of one area this person can use, in list order. */
export function pagesFor(
	area: SettingsArea,
	me: SettingsViewer,
	role: Role | undefined,
): SettingsPage[] {
	return SETTINGS_PAGES.filter(
		(page) => page.area === area && page.visible(me, role),
	);
}

/** The page an area opens on, and where an unknown section lands. */
export function firstPage(area: SettingsArea): SettingsPage {
	const page = SETTINGS_PAGES.find((candidate) => candidate.area === area);
	if (!page) throw new Error(`No settings pages for ${area}`);
	return page;
}

export function findPage(
	area: SettingsArea,
	section: string,
): SettingsPage | undefined {
	return SETTINGS_PAGES.find(
		(page) => page.area === area && page.section === section,
	);
}

/** Pages under their group headers, in the order the groups first appear. */
export function groupPages(
	pages: readonly SettingsPage[],
): { group: string | undefined; pages: SettingsPage[] }[] {
	const groups: { group: string | undefined; pages: SettingsPage[] }[] = [];
	for (const page of pages) {
		const found = groups.find((entry) => entry.group === page.group);
		if (found) found.pages.push(page);
		else groups.push({ group: page.group, pages: [page] });
	}
	return groups;
}

/**
 * The pages a typed query narrows to: every word must appear in the title or
 * a keyword (any case). An empty query keeps them all. List order is kept.
 */
export function filterPages(
	pages: readonly SettingsPage[],
	query: string,
): SettingsPage[] {
	const words = query.toLowerCase().split(/\s+/).filter(Boolean);
	if (words.length === 0) return [...pages];
	return pages.filter((page) => {
		const haystack = [page.title, page.section, ...page.keywords]
			.join(" ")
			.toLowerCase();
		return words.every((word) => haystack.includes(word));
	});
}

/**
 * Where a word that matches nothing in this area may live: on the person's
 * own Account pages, which every area can send to ("tok" in Team settings
 * finds nothing, and API tokens are Account settings → API tokens). Undefined
 * when the area is Account itself or no Account page answers.
 */
export function settingsElsewhere(
	area: SettingsArea,
	query: string,
): { what: string; where: string; href: string } | undefined {
	if (area === "account") return undefined;
	const [page] = filterPages(
		SETTINGS_PAGES.filter((candidate) => candidate.area === "account"),
		query,
	);
	if (!page) return undefined;
	return {
		what: page.title,
		where: `Account settings → ${page.title}`,
		href: settingsHref("account", page.section),
	};
}

/**
 * The address of a page. A project's sections are hashes on its one page; a
 * server's are pages under its Settings tab.
 */
export function settingsHref(
	area: SettingsArea,
	section: string,
	opts?: { projectId?: string; serverId?: string },
): string {
	switch (area) {
		case "account":
			return `/account/${section}`;
		case "team":
			return `/settings/${section}`;
		case "admin":
			return `/admin/${section}`;
		case "project":
			return opts?.projectId ? `/p/${opts.projectId}/settings#${section}` : "/";
		case "server":
			return opts?.serverId
				? `/servers/${opts.serverId}/settings/${section}`
				: "/servers";
	}
}

/** The query parameters the git connection flows come back with. */
export interface SettingsSearch {
	git_connected?: string;
	git_notice?: string;
	git_error?: string;
}

const SEARCH_KEYS = ["git_connected", "git_notice", "git_error"] as const;

/** Keeps the flow parameters of a settings address and drops the rest. */
export function pickSettingsSearch(
	search: Record<string, unknown>,
): SettingsSearch {
	const picked: SettingsSearch = {};
	for (const key of SEARCH_KEYS) {
		const value = search[key];
		if (typeof value === "string" && value !== "") picked[key] = value;
		else if (typeof value === "number") picked[key] = String(value);
	}
	return picked;
}

// The old `/settings#id` and `/settings?section=id` addresses, which mail and
// webhooks the server already sent still carry. Kept for good.
const LEGACY: Readonly<Record<string, string>> = {
	profile: "/account/profile",
	security: "/account/security",
	tokens: "/account/tokens",
	mcp: "/account/mcp-clients",
	members: "/settings/members",
	audit: "/settings/audit-log",
	builds: "/settings/build-location",
	registries: "/settings/image-registries",
	integrations: "/settings/integrations",
	networks: "/settings/private-networks",
	mesh: "/settings/server-mesh",
	platform: "/settings/headscale",
	backups: "/settings/backup-storage",
	notifications: "/settings/notifications",
	"log-drains": "/settings/log-forwarding",
	instance: "/admin/sign-in",
	sso: "/admin/sign-in",
	version: "/admin/updates",
};

/**
 * Where an old settings address now lives. The hash wins over `?section=`;
 * an unknown or missing one opens the team's first page. Every other query
 * parameter travels along (`git_connected`, `git_notice`, `git_error`).
 */
export function legacySettingsRedirect(
	hash: string,
	search: URLSearchParams,
): { to: string; search: Record<string, string> } {
	const id = (hash.replace(/^#/, "") || search.get("section") || "").trim();
	const to =
		Object.hasOwn(LEGACY, id) && LEGACY[id]
			? LEGACY[id]
			: settingsHref("team", firstPage("team").section);
	const kept: Record<string, string> = {};
	for (const [key, value] of search) {
		if (key !== "section") kept[key] = value;
	}
	return { to, search: kept };
}
