import { describe, expect, test } from "bun:test";
import type * as Gen from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { testQueryClient } from "#/test/render";
import {
	deploymentCommandsQuery,
	deploymentReleaseQuery,
	toDebugCommands,
	toDebugRelease,
} from "./queries";

const wireRelease = (
	overrides: Partial<Gen.ReleaseDetailView> = {},
): Gen.ReleaseDetailView => ({
	id: "rel-1",
	deploymentId: "dep-1",
	serviceId: "svc-1",
	generation: 3,
	createdAt: "2026-10-31T10:00:00Z",
	imageRef: "registry/api:abc",
	config: { port: 8080 },
	source: { type: "git", commitSha: "abc1234" },
	build: { method: "railpack" },
	variables: [
		{
			key: "A",
			scope: "runtime",
			secret: false,
			reference: false,
			platform: false,
			external: false,
		},
	],
	variablesPruned: false,
	placement: {
		strategy: "single",
		targetServerId: "srv-1",
		targetServerName: "edge-1",
		buildServerId: "srv-b",
		buildServerName: "build-1",
		registryId: "reg-1",
		registryRef: "registry/api",
		platform: "linux/amd64",
	},
	...overrides,
});

describe("toDebugRelease", () => {
	test("keeps a complete release as it is", () => {
		const release = toDebugRelease(wireRelease());
		expect(release.build).toEqual({ method: "railpack" });
		expect(release.variables.map((v) => v.key)).toEqual(["A"]);
		expect(release.placement).toMatchObject({
			targetServerId: "srv-1",
			buildServerId: "srv-b",
			registryId: "reg-1",
			targetServerName: "edge-1",
		});
	});

	test("spells out Go's nulls: no build, no variables", () => {
		const release = toDebugRelease(
			wireRelease({
				build: null,
				variables: null,
			}),
		);
		expect(release.build).toBeNull();
		expect(release.variables).toEqual([]);
	});

	test("an unplaced release keeps its null server and registry ids", () => {
		const release = toDebugRelease(
			wireRelease({
				placement: {
					strategy: "single",
					targetServerId: null,
					targetServerName: "",
					buildServerId: null,
					buildServerName: "",
					registryId: null,
					registryRef: "",
					platform: "",
				},
			}),
		);
		expect(release.placement.targetServerId).toBeNull();
		expect(release.placement.buildServerId).toBeNull();
		expect(release.placement.registryId).toBeNull();
	});
});

describe("toDebugCommands", () => {
	test("returns the items", () => {
		const items = [{ id: "c1" }] as Gen.CommandView[];
		expect(toDebugCommands({ items } as Gen.CommandsOutputBody)).toBe(items);
	});

	test("a null list is empty", () => {
		expect(
			toDebugCommands({ items: null } as unknown as Gen.CommandsOutputBody),
		).toEqual([]);
	});
});

describe("query options", () => {
	test("a running deployment's release and commands poll every 3 s; a finished one does not poll", () => {
		expect(deploymentReleaseQuery("dep-1", true).refetchInterval).toBe(3_000);
		expect(deploymentReleaseQuery("dep-1", false).refetchInterval).toBe(false);
		expect(deploymentCommandsQuery("dep-1", true).refetchInterval).toBe(3_000);
		expect(deploymentCommandsQuery("dep-1", false).refetchInterval).toBe(false);
	});

	test("a release 404 is not retried", () => {
		expect(deploymentReleaseQuery("dep-1", false).retry).toBe(false);
	});

	test("the release query asks for that deployment's release", async () => {
		const api = stubApi({
			"GET /deployments/:id/release": { body: wireRelease() },
		});
		const options = deploymentReleaseQuery("dep-77", false);
		const data = await testQueryClient().fetchQuery(options);
		expect(api.calls[0]?.path).toBe("/deployments/dep-77/release");
		expect(data.id).toBe("rel-1");
	});
});
