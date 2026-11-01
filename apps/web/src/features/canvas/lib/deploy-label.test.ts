import { describe, expect, test } from "bun:test";
import { deployLabel } from "./deploy-label";
import { makeService } from "./fixtures";

describe("deployLabel", () => {
	test("a service that is running its release is redeployed", () => {
		expect(
			deployLabel(makeService({ kind: "app", currentDeploymentId: "dep-1" })),
		).toBe("Redeploy");
		expect(
			deployLabel(
				makeService({ kind: "database", currentDeploymentId: "dep-1" }),
			),
		).toBe("Redeploy");
	});

	test("one that never ran is deployed", () => {
		expect(
			deployLabel(makeService({ kind: "app", currentDeploymentId: null })),
		).toBe("Deploy");
	});

	test("a route is always deployed", () => {
		expect(
			deployLabel(makeService({ kind: "route", currentDeploymentId: "dep-1" })),
		).toBe("Deploy");
	});
});
