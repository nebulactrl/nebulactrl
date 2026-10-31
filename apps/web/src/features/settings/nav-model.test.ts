import { describe, expect, test } from "bun:test";
import type { Role } from "#/features/teams/roles";
import {
	filterPages,
	findPage,
	firstPage,
	groupPages,
	legacySettingsRedirect,
	pagesFor,
	pickSettingsSearch,
	SETTINGS_PAGES,
	type SettingsArea,
	settingsElsewhere,
	settingsHref,
	teamSectionAlias,
} from "./nav-model";

const redirectOf = (hash: string, query = "") =>
	legacySettingsRedirect(hash, new URLSearchParams(query));

const titles = (
	area: SettingsArea,
	role: Role | undefined,
	instanceAdmin: boolean,
) => pagesFor(area, { instanceAdmin }, role).map((page) => page.title);

describe("legacySettingsRedirect", () => {
	const cases: [string, string][] = [
		["profile", "/account/profile"],
		["security", "/account/security"],
		["tokens", "/account/tokens"],
		["mcp", "/account/mcp-clients"],
		["members", "/settings/members"],
		["audit", "/settings/audit-log"],
		["builds", "/settings/build-location"],
		["registries", "/settings/image-registries"],
		["integrations", "/settings/integrations"],
		["networks", "/settings/private-networks"],
		["mesh", "/settings/server-mesh"],
		["platform", "/settings/headscale"],
		["backups", "/settings/backup-storage"],
		["notifications", "/settings/notifications"],
		["log-drains", "/settings/log-forwarding"],
		["instance", "/admin/sign-in"],
		["sso", "/admin/sign-in"],
		["version", "/admin/updates"],
	];

	test.each(cases)("#%s opens %s", (hash, to) => {
		expect(redirectOf(hash).to).toBe(to);
		expect(redirectOf(`#${hash}`).to).toBe(to);
		expect(redirectOf("", `section=${hash}`).to).toBe(to);
	});

	test("every target is a real page", () => {
		for (const [, to] of cases) {
			const [, area, section] = to.split("/");
			const known = SETTINGS_PAGES.some(
				(page) =>
					settingsHref(page.area, page.section) === `/${area}/${section}`,
			);
			expect(known).toBe(true);
		}
	});

	test("no hash opens the team's first page", () => {
		expect(redirectOf("").to).toBe("/settings/general");
	});

	test("an unknown hash opens the team's first page", () => {
		expect(redirectOf("nonsense").to).toBe("/settings/general");
		expect(redirectOf("", "section=nonsense").to).toBe("/settings/general");
		expect(redirectOf("constructor").to).toBe("/settings/general");
	});

	test("the hash wins over ?section=", () => {
		expect(redirectOf("members", "section=audit").to).toBe("/settings/members");
	});

	test("the git flow parameters travel along, ?section= does not", () => {
		const result = redirectOf(
			"integrations",
			"git_connected=github&git_notice=Installed&section=mesh&git_error=denied",
		);
		expect(result.to).toBe("/settings/integrations");
		expect(result.search).toEqual({
			git_connected: "github",
			git_notice: "Installed",
			git_error: "denied",
		});
	});

	test("a bare address with only flow parameters keeps them", () => {
		const result = redirectOf("", "git_connected=gitlab");
		expect(result).toEqual({
			to: "/settings/general",
			search: { git_connected: "gitlab" },
		});
	});
});

describe("teamSectionAlias", () => {
	test("the old Built-in services address opens the Headscale page", () => {
		expect(teamSectionAlias("built-in-services")).toBe("headscale");
		expect(
			findPage("team", teamSectionAlias("built-in-services")),
		).toBeDefined();
	});

	test("every other section is left as it is", () => {
		expect(teamSectionAlias("members")).toBe("members");
		expect(teamSectionAlias("constructor")).toBe("constructor");
	});
});

describe("pickSettingsSearch", () => {
	test("keeps the three flow keys and drops the rest", () => {
		expect(
			pickSettingsSearch({
				git_connected: "github",
				git_notice: 3,
				git_error: "",
				other: "x",
			}),
		).toEqual({ git_connected: "github", git_notice: "3" });
	});
});

describe("settingsHref", () => {
	test("each area has its own path", () => {
		expect(settingsHref("account", "tokens")).toBe("/account/tokens");
		expect(settingsHref("team", "members")).toBe("/settings/members");
		expect(settingsHref("admin", "updates")).toBe("/admin/updates");
	});

	test("a project's sections are hashes on its page", () => {
		expect(settingsHref("project", "environments", { projectId: "p1" })).toBe(
			"/p/p1/settings#environments",
		);
	});
});

describe("pages", () => {
	test("sections are unique within an area", () => {
		const keys = SETTINGS_PAGES.map((page) => `${page.area}/${page.section}`);
		expect(new Set(keys).size).toBe(keys.length);
	});

	test("every page has a one-line description and keywords or a title", () => {
		for (const page of SETTINGS_PAGES) {
			expect(page.description.endsWith(".")).toBe(true);
			expect(page.description.includes("\n")).toBe(false);
		}
	});

	test("firstPage and findPage", () => {
		expect(firstPage("account").section).toBe("profile");
		expect(firstPage("team").section).toBe("general");
		expect(firstPage("admin").section).toBe("updates");
		expect(findPage("team", "audit-log")?.title).toBe("Audit log");
		expect(findPage("team", "nope")).toBeUndefined();
	});
});

describe("visibility", () => {
	const roles: Role[] = ["owner", "admin", "developer", "viewer"];

	test("everyone gets the account pages", () => {
		for (const role of roles) {
			for (const instanceAdmin of [true, false]) {
				expect(titles("account", role, instanceAdmin)).toEqual([
					"Profile",
					"Security",
					"API tokens",
					"MCP clients",
				]);
			}
		}
	});

	test("owners and admins get every team page", () => {
		for (const role of ["owner", "admin"] as const) {
			expect(titles("team", role, false)).toEqual([
				"General",
				"Members",
				"Audit log",
				"Build location",
				"Image registries",
				"Integrations",
				"Private networks",
				"Server mesh",
				"Private network (Headscale)",
				"Backup storage",
				"Notifications",
				"Log forwarding",
			]);
		}
	});

	test("developers and viewers get only the pages they can use", () => {
		for (const role of ["developer", "viewer"] as const) {
			expect(titles("team", role, false)).toEqual([
				"General",
				"Members",
				"Private networks",
				"Server mesh",
			]);
		}
	});

	test("a role that is not known yet sees the pages for everyone", () => {
		expect(titles("team", undefined, false)).toEqual(
			titles("team", "viewer", false),
		);
	});

	test("the instance admin's team role does not matter for Admin", () => {
		for (const role of roles) {
			expect(titles("admin", role, true)).toEqual([
				"Updates",
				"Control-plane backup",
				"Built-in services",
				"Sign-in and teams",
				"Dashboard access",
				"Email relay",
				"MCP",
			]);
		}
	});

	test("an owner who is not the instance admin gets no Admin pages", () => {
		expect(titles("admin", "owner", false)).toEqual([]);
	});
});

describe("server settings", () => {
	const sections = [
		"general",
		"scheduling",
		"monitoring",
		"logs-and-disk",
		"mesh",
		"host-shell",
		"danger",
	];

	test("owners and admins get every section, in the owner's order", () => {
		for (const role of ["owner", "admin"] as const) {
			for (const instanceAdmin of [true, false]) {
				expect(titles("server", role, instanceAdmin)).toEqual([
					"General",
					"Scheduling",
					"Monitoring",
					"Logs and disk",
					"Mesh",
					"Host shell",
					"Danger zone",
				]);
			}
		}
	});

	test("developers, viewers and a role not known yet get none", () => {
		for (const role of ["developer", "viewer", undefined] as const) {
			expect(titles("server", role, false)).toEqual([]);
			expect(titles("server", role, true)).toEqual([]);
		}
	});

	test("each section has its own page under the server's Settings tab", () => {
		expect(
			SETTINGS_PAGES.filter((page) => page.area === "server"),
		).toHaveLength(sections.length);
		for (const section of sections) {
			expect(findPage("server", section)).toBeDefined();
			expect(settingsHref("server", section, { serverId: "s1" })).toBe(
				`/servers/s1/settings/${section}`,
			);
		}
		expect(firstPage("server").section).toBe("general");
	});
});

describe("groupPages", () => {
	test("team groups keep their order, General first and ungrouped", () => {
		const groups = groupPages(
			pagesFor("team", { instanceAdmin: false }, "owner"),
		);
		expect(groups.map((entry) => entry.group)).toEqual([
			undefined,
			"People",
			"Build and ship",
			"Servers",
			"Data and alerts",
		]);
	});

	test("admin groups", () => {
		const groups = groupPages(
			pagesFor("admin", { instanceAdmin: true }, "owner"),
		);
		expect(groups.map((entry) => entry.group)).toEqual([
			"Install",
			"Access",
			"Features",
		]);
	});

	test("a developer's groups drop the ones left empty", () => {
		const groups = groupPages(
			pagesFor("team", { instanceAdmin: false }, "developer"),
		);
		expect(groups.map((entry) => entry.group)).toEqual([
			undefined,
			"People",
			"Servers",
		]);
	});
});

describe("filterPages", () => {
	const owner = pagesFor("team", { instanceAdmin: false }, "owner");

	test("an empty query keeps every page", () => {
		expect(filterPages(owner, "  ")).toEqual(owner);
	});

	test("matches the title", () => {
		expect(filterPages(owner, "audit").map((page) => page.section)).toEqual([
			"audit-log",
		]);
	});

	test("matches a keyword", () => {
		expect(filterPages(owner, "s3").map((page) => page.section)).toEqual([
			"backup-storage",
		]);
	});

	test("every word has to match", () => {
		expect(filterPages(owner, "log forwarding").map((p) => p.section)).toEqual([
			"log-forwarding",
		]);
		expect(filterPages(owner, "log zzz")).toEqual([]);
	});

	test("tokens are found among the account pages", () => {
		const pages = pagesFor("account", { instanceAdmin: false }, "viewer");
		expect(filterPages(pages, "token").map((page) => page.section)).toEqual([
			"tokens",
		]);
	});

	test("s3 finds the built-in services in Admin", () => {
		const pages = pagesFor("admin", { instanceAdmin: true }, "owner");
		expect(filterPages(pages, "quota").map((page) => page.section)).toEqual([
			"built-in-services",
		]);
	});

	test("smtp finds the email relay in Admin", () => {
		const pages = pagesFor("admin", { instanceAdmin: true }, "owner");
		expect(filterPages(pages, "SMTP").map((page) => page.section)).toEqual([
			"email-relay",
		]);
	});
});

describe("settingsElsewhere", () => {
	test("points a word that is not in Team settings to its Account page", () => {
		expect(settingsElsewhere("team", "tok")).toEqual({
			what: "API tokens",
			where: "Account settings → API tokens",
			href: "/account/tokens",
		});
	});

	test("says nothing inside Account itself or for a word that is nowhere", () => {
		expect(settingsElsewhere("account", "tok")).toBeUndefined();
		expect(settingsElsewhere("team", "zzzz")).toBeUndefined();
	});
});
