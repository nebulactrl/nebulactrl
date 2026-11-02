import { describe, expect, test } from "bun:test";
import type { Deployment } from "#/api/models";
import { deploymentMarkers } from "./deploy-markers";

const deployment = (over: Partial<Deployment> = {}): Deployment =>
	({
		id: "dep_0123456789",
		startedAt: "2025-01-01T00:00:00Z",
		release: null,
		...over,
	}) as Deployment;

const release = (source: Record<string, string>) =>
	({ id: "rel_1", source }) as unknown as Deployment["release"];

describe("deploymentMarkers", () => {
	test("labels a commit deployment with the first seven characters of the sha", () => {
		const [marker] = deploymentMarkers([
			deployment({ release: release({ commitSha: "a1b2c3d4e5f6" }) }),
		]);
		expect(marker).toEqual({
			id: "dep_0123456789",
			at: Date.parse("2025-01-01T00:00:00Z"),
			label: "a1b2c3d",
		});
	});

	test("labels an image deployment with the algorithm and the digest's head", () => {
		const digest = `sha256:${"0123456789abcdef".repeat(4)}`;
		const [marker] = deploymentMarkers([
			deployment({ release: release({ digest }) }),
		]);
		expect(marker?.label).toBe("sha256:01234567");
	});

	test("prefers the commit over the digest when a release has both", () => {
		const [marker] = deploymentMarkers([
			deployment({
				release: release({
					commitSha: "feedfacecafe",
					digest: `sha256:${"a".repeat(64)}`,
				}),
			}),
		]);
		expect(marker?.label).toBe("feedfac");
	});

	test("falls back to the head of the deployment id without a release", () => {
		expect(deploymentMarkers([deployment()])[0]?.label).toBe("dep_012");
		expect(
			deploymentMarkers([deployment({ release: release({}) })])[0]?.label,
		).toBe("dep_012");
	});

	test("skips deployments that never started or carry an unparseable time", () => {
		const markers = deploymentMarkers([
			deployment({ id: "queued", startedAt: null }),
			deployment({ id: "broken", startedAt: "not a date" }),
			deployment({ id: "ok" }),
		]);
		expect(markers.map((m) => m.id)).toEqual(["ok"]);
	});

	test("keeps the order it was given and handles missing input", () => {
		const markers = deploymentMarkers([
			deployment({ id: "b", startedAt: "2025-01-02T00:00:00Z" }),
			deployment({ id: "a", startedAt: "2025-01-01T00:00:00Z" }),
		]);
		expect(markers.map((m) => m.id)).toEqual(["b", "a"]);
		expect(deploymentMarkers(undefined)).toEqual([]);
		expect(deploymentMarkers([])).toEqual([]);
	});
});
