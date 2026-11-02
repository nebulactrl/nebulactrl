import { describe, expect, test } from "bun:test";
import { QueryObserver } from "@tanstack/react-query";
import {
	getEnvironmentMeshOptions,
	getMeshOptions,
	getServerMeshOptions,
	getServiceMeshOptions,
	listProjectsOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { testQueryClient } from "#/test/render";
import { invalidateMesh } from "./queries";

describe("invalidateMesh", () => {
	test("refetches the mesh view on screen and marks the other mesh views stale, leaving unrelated queries alone", async () => {
		const api = stubApi({
			"GET /mesh": { body: make("View", { provider: "tailscale" }) },
			"GET /servers/:id/mesh": { body: make("ServerMeshView") },
			"GET /environments/:id/mesh": { body: make("EnvironmentMeshView") },
			"GET /services/:id/mesh": { body: make("ServiceMeshView") },
			"GET /projects": { body: { items: [] } },
		});
		const queryClient = testQueryClient();
		const meshOptions = getMeshOptions();
		const serverOptions = getServerMeshOptions({ path: { serverId: "s1" } });
		const envOptions = getEnvironmentMeshOptions({
			path: { environmentId: "e1" },
		});
		const serviceOptions = getServiceMeshOptions({
			path: { serviceId: "svc1" },
		});
		const projectsOptions = listProjectsOptions();
		await Promise.all(
			[serverOptions, envOptions, serviceOptions, projectsOptions].map(
				(options) => queryClient.fetchQuery(options as never),
			),
		);
		// Only the project-wide mesh is on screen (has an observer).
		const observer = new QueryObserver(queryClient, meshOptions);
		const unsubscribe = observer.subscribe(() => {});
		await queryClient.fetchQuery(meshOptions);
		const before = api.calls.filter((call) => call.path === "/mesh").length;

		await invalidateMesh(queryClient);

		expect(api.calls.filter((call) => call.path === "/mesh").length).toBe(
			before + 1,
		);
		for (const options of [serverOptions, envOptions, serviceOptions]) {
			expect(queryClient.getQueryState(options.queryKey)?.isInvalidated).toBe(
				true,
			);
		}
		expect(
			queryClient.getQueryState(projectsOptions.queryKey)?.isInvalidated,
		).toBe(false);
		// Inactive mesh views are not refetched, only marked.
		expect(
			api.calls.filter((call) => call.path === "/servers/s1/mesh"),
		).toHaveLength(1);
		unsubscribe();
		expect(api.unmatched).toEqual([]);
	});
});
