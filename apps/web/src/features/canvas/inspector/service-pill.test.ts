import { describe, expect, test } from "bun:test";
import type { Service } from "#/api/models";
import { repoName, servicePill, sourceLine } from "./service-pill";

const base = {
	sourceType: "image",
	config: { image: "postgres:17" },
	domains: [],
	desiredState: "running",
	currentDeploymentId: "d1",
	latestDeployment: { status: "succeeded", finishedAt: null },
	server: { status: "online", name: "agent1", lastSeenAt: null },
	observed: { state: "running", health: "healthy" },
} as unknown as Service;

function with_(patch: Record<string, unknown>): Service {
	return { ...base, ...patch } as unknown as Service;
}

describe("repoName", () => {
	test("reads owner/repo from https and scp-style URLs", () => {
		expect(repoName("https://github.com/acme/shop-api.git")).toBe(
			"acme/shop-api",
		);
		expect(repoName("git@github.com:acme/shop-api.git")).toBe("acme/shop-api");
		expect(repoName("https://git.example.com")).toBe("git.example.com");
	});
});

describe("sourceLine", () => {
	test("an image with domains", () => {
		const line = sourceLine(
			with_({ domains: [{ hostname: "a.dev" }, { hostname: "b.dev" }] }),
		);
		expect(line.kind).toBe("image");
		expect(line.parts).toEqual(["postgres:17"]);
		expect(line.domain).toEqual({ hostname: "a.dev", more: 1 });
	});
	test("a git repo with branch", () => {
		const line = sourceLine(
			with_({
				sourceType: "git",
				config: {
					gitUrl: "https://github.com/acme/shop-api",
					gitBranch: "main",
				},
				domains: [{ hostname: "api.acme.dev" }],
			}),
		);
		expect(line.parts).toEqual(["acme/shop-api", "main"]);
		expect(line.domain).toEqual({ hostname: "api.acme.dev", more: 0 });
	});
	test("no domain, no link", () => {
		expect(sourceLine(base).domain).toBeNull();
	});
});

describe("servicePill", () => {
	test("a running healthy service is Live", () => {
		expect(servicePill(base).label).toBe("Live");
	});
	test("stopped, offline and degraded outrank Live", () => {
		expect(servicePill(with_({ desiredState: "stopped" })).label).toBe(
			"Stopped",
		);
		expect(
			servicePill(with_({ server: { status: "offline", name: "x" } })).label,
		).toBe("Offline");
		expect(
			servicePill(with_({ observed: { state: "running", health: "degraded" } }))
				.label,
		).toBe("Degraded");
	});
	test("an in-flight build spins", () => {
		const pill = servicePill(
			with_({ latestDeployment: { status: "building", finishedAt: null } }),
		);
		expect(pill).toEqual({ kind: "building", label: "Building" });
	});
});
