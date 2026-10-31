import { describe, expect, test } from "bun:test";
import { teamCaption } from "./team-caption";

describe("teamCaption", () => {
	test("names the role, the projects and the servers", () => {
		expect(teamCaption({ role: "Owner", projects: 4, servers: 2 })).toBe(
			"Owner · 4 projects · 2 servers",
		);
	});

	test("singular counts", () => {
		expect(teamCaption({ role: "Admin", projects: 1, servers: 1 })).toBe(
			"Admin · 1 project · 1 server",
		);
	});

	test("leaves out zero servers and counts still loading", () => {
		expect(teamCaption({ role: "Owner", projects: 0, servers: 0 })).toBe(
			"Owner · 0 projects",
		);
		expect(
			teamCaption({ role: "Owner", projects: undefined, servers: undefined }),
		).toBe("Owner");
	});
});
