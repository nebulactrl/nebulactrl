import { describe, expect, test } from "bun:test";
import { deleteDescription, holdLabel } from "./danger-copy";

const production = { name: "Production", isProduction: true };
const preview = { name: "Staging", isProduction: false };

describe("deleteDescription", () => {
	test("names production when the service lives there", () => {
		const text = deleteDescription({
			serviceName: "api",
			isRoute: false,
			environment: production,
		});
		expect(text).toStartWith("api is in production (Production). ");
		expect(text).toContain("stops the container");
	});

	test("says nothing extra elsewhere, or before the environment is known", () => {
		for (const environment of [preview, undefined]) {
			const text = deleteDescription({
				serviceName: "api",
				isRoute: false,
				environment,
			});
			expect(text).toStartWith("Deleting api stops the container");
			expect(text).not.toContain("production");
		}
	});

	test("a route says what happens to the proxy", () => {
		expect(
			deleteDescription({
				serviceName: "docs",
				isRoute: true,
				environment: production,
			}),
		).toContain("removes its route from the proxy");
	});
});

describe("holdLabel", () => {
	test("production is in the label", () => {
		expect(holdLabel({ isRoute: false, environment: production })).toBe(
			"Hold to delete service from production",
		);
		expect(holdLabel({ isRoute: true, environment: production })).toBe(
			"Hold to delete route from production",
		);
	});

	test("other environments keep the plain label", () => {
		expect(holdLabel({ isRoute: false, environment: preview })).toBe(
			"Hold to delete service",
		);
		expect(holdLabel({ isRoute: true, environment: undefined })).toBe(
			"Hold to delete route",
		);
	});
});
