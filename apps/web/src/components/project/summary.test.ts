import { describe, expect, test } from "bun:test";
import type { Service } from "#/api/models";
import { lastDeployAt, summarizeEnvironment } from "./summary";

const env = {
	id: "e1",
	slug: "production",
	name: "production",
	isProduction: true,
};

function service(
	latest: { status: string; at: string } | null,
	serving = true,
): Service {
	return {
		id: "s",
		name: "s",
		sourceType: "image",
		config: { image: "x" },
		currentDeploymentId: serving ? "d" : null,
		server: null,
		observed: null,
		latestDeployment: latest && {
			id: "d",
			status: latest.status,
			createdAt: latest.at,
			finishedAt: latest.at,
		},
	} as unknown as Service;
}

describe("summarizeEnvironment", () => {
	test("no services is an unlit lamp saying so", () => {
		const s = summarizeEnvironment(env, []);
		expect(s.tone).toBe("off");
		expect(s.label).toBe("No services");
	});

	test("a failed service decides the lamp", () => {
		const s = summarizeEnvironment(env, [
			service({ status: "succeeded", at: "2026-01-01T00:00:00Z" }),
			service({ status: "failed", at: "2026-01-02T00:00:00Z" }),
		]);
		expect(s.tone).toBe("failed");
		expect(s.label).toBe("1 failed");
		expect(s.serviceCount).toBe(2);
	});

	test("an in-flight deployment blinks", () => {
		const s = summarizeEnvironment(env, [
			service({ status: "building", at: "2026-01-02T00:00:00Z" }),
		]);
		expect(s.pulse).toBe(true);
		expect(s.label).toBe("Deploying");
	});

	test("all succeeded reads as live", () => {
		const s = summarizeEnvironment(env, [
			service({ status: "succeeded", at: "2026-01-02T00:00:00Z" }),
		]);
		expect(s.tone).toBe("live");
		expect(s.label).toBe("Live");
	});
});

describe("lastDeployAt", () => {
	test("is the newest deployment time, null with none", () => {
		expect(lastDeployAt([service(null)])).toBeNull();
		expect(
			lastDeployAt([
				service({ status: "succeeded", at: "2026-01-01T00:00:00Z" }),
				service({ status: "failed", at: "2026-01-03T00:00:00Z" }),
			]),
		).toBe("2026-01-03T00:00:00Z");
	});
});
