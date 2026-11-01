import { describe, expect, test } from "bun:test";
import type { Role } from "#/features/teams/roles";
import { settingsCommandEntries } from "./settings-commands";

const ids = (
	role: Role | undefined,
	instanceAdmin: boolean,
	projectId?: string,
) =>
	settingsCommandEntries({ instanceAdmin }, role, projectId).map(
		(entry) => entry.id,
	);

describe("settingsCommandEntries", () => {
	test("a viewer can open the account pages and the open team pages", () => {
		expect(ids("viewer", false)).toEqual([
			"settings:account:profile",
			"settings:account:security",
			"settings:account:tokens",
			"settings:account:mcp-clients",
			"settings:team:general",
			"settings:team:members",
			"settings:team:private-networks",
			"settings:team:server-mesh",
		]);
	});

	test("an owner also gets the pages owners and admins manage", () => {
		const owner = ids("owner", false);
		expect(owner).toContain("settings:team:audit-log");
		expect(owner).toContain("settings:team:backup-storage");
		expect(owner).toContain("settings:team:log-forwarding");
		expect(owner.some((id) => id.startsWith("settings:admin:"))).toBe(false);
	});

	test("only the instance admin gets the Admin pages", () => {
		for (const role of ["owner", "admin", "developer", "viewer"] as const) {
			expect(
				ids(role, false).some((id) => id.startsWith("settings:admin:")),
			).toBe(false);
		}
		const admin = ids("developer", true).filter((id) =>
			id.startsWith("settings:admin:"),
		);
		expect(admin).toEqual([
			"settings:admin:updates",
			"settings:admin:backup",
			"settings:admin:built-in-services",
			"settings:admin:sign-in",
			"settings:admin:dashboard-access",
			"settings:admin:email-relay",
			"settings:admin:mcp",
		]);
	});

	test("project pages appear only with a project open", () => {
		expect(
			ids("owner", false).some((id) => id.startsWith("settings:project:")),
		).toBe(false);
		const open = settingsCommandEntries(
			{ instanceAdmin: false },
			"owner",
			"p1",
		);
		expect(open.filter((entry) => entry.area === "project")).toHaveLength(4);
		expect(
			open.find((entry) => entry.id === "settings:project:environments")?.href,
		).toBe("/p/p1/settings#environments");
	});

	test("a server's pages are not in the palette: it has no server open", () => {
		const entries = settingsCommandEntries(
			{ instanceAdmin: true },
			"owner",
			"p1",
		);
		expect(entries.some((entry) => entry.area === "server")).toBe(false);
	});

	test("ids are unique and each row can be found by its keywords", () => {
		const entries = settingsCommandEntries(
			{ instanceAdmin: true },
			"owner",
			"p1",
		);
		expect(new Set(entries.map((entry) => entry.id)).size).toBe(entries.length);
		const byId = (id: string) => entries.find((entry) => entry.id === id);
		expect(byId("settings:admin:email-relay")?.keywords).toContain("smtp");
		expect(byId("settings:admin:sign-in")?.keywords).toContain("sso");
		expect(byId("settings:team:backup-storage")?.keywords).toContain("s3");
		expect(byId("settings:account:tokens")?.keywords).toContain("token");
	});

	test("labels name the area, so General is not ambiguous", () => {
		const entries = settingsCommandEntries(
			{ instanceAdmin: false },
			"owner",
			"p1",
		);
		const labels = entries.map((entry) => entry.label);
		expect(labels).toContain("Team settings: General");
		expect(labels).toContain("Project settings: General");
		expect(new Set(labels).size).toBe(labels.length);
	});
});
