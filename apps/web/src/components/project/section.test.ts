import { describe, expect, test } from "bun:test";
import { envSection, envSectionRoute } from "./section";

describe("envSection", () => {
	test("keeps the page being read across a switch", () => {
		expect(envSection("/p/abc/production/observability")).toBe("observability");
		expect(envSection("/p/abc/staging/deployments/123")).toBe("deployments");
		expect(envSectionRoute("variables")).toBe(
			"/p/$projectId/$envSlug/variables",
		);
	});

	test("the canvas and pages that belong to one service open the canvas", () => {
		expect(envSection("/p/abc/production")).toBeUndefined();
		expect(envSection("/p/abc/production/canvas")).toBeUndefined();
		expect(envSection("/settings")).toBeUndefined();
		// /services and /builds are no environment page: no section.
		expect(envSection("/p/abc/production/services")).toBeUndefined();
		expect(envSection("/p/abc/production/builds")).toBeUndefined();
		expect(envSectionRoute(undefined)).toBe("/p/$projectId/$envSlug");
	});
});
