import { describe, expect, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { saveTeamBuildSettings, teamBuildSettingsQuery } from "./api";

const run = (client: QueryClient) =>
	client.fetchQuery(teamBuildSettingsQuery());

describe("teamBuildSettingsQuery", () => {
	test("reads the team's location and registry", async () => {
		const api = stubApi({
			"GET /team/build-settings": {
				body: {
					buildLocation: "build_server",
					registryId: "reg-1",
					updatedAt: null,
				},
			},
		});
		expect(await run(new QueryClient())).toEqual({
			buildLocation: "build_server",
			registryId: "reg-1",
		});
		expect(api.unmatched).toEqual([]);
	});

	test("an empty registry id means no registry", async () => {
		stubApi({
			"GET /team/build-settings": {
				body: { buildLocation: "target", registryId: "", updatedAt: null },
			},
		});
		expect(await run(new QueryClient())).toEqual({
			buildLocation: "target",
			registryId: null,
		});
	});

	test("a failed read rejects so the query reports the error", async () => {
		stubApi({
			"GET /team/build-settings": problem(403, {
				title: "Forbidden",
				status: 403,
			}),
		});
		await expect(run(new QueryClient())).rejects.toBeDefined();
	});
});

describe("saveTeamBuildSettings", () => {
	test("PUTs the location and the registry and returns the saved settings", async () => {
		const api = stubApi({
			"PUT /team/build-settings": {
				body: {
					buildLocation: "build_server",
					registryId: "reg-2",
					updatedAt: "2026-01-01T00:00:00Z",
				},
			},
		});
		const saved = await saveTeamBuildSettings({
			buildLocation: "build_server",
			registryId: "reg-2",
		});
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]).toMatchObject({
			method: "PUT",
			path: "/team/build-settings",
			body: { buildLocation: "build_server", registryId: "reg-2" },
		});
		expect(saved).toEqual({
			buildLocation: "build_server",
			registryId: "reg-2",
		});
	});

	test("leaves registryId out of the body when there is no registry", async () => {
		const api = stubApi({
			"PUT /team/build-settings": {
				body: { buildLocation: "target", registryId: "", updatedAt: null },
			},
		});
		const saved = await saveTeamBuildSettings({
			buildLocation: "target",
			registryId: null,
		});
		expect(api.calls[0]?.body).toEqual({ buildLocation: "target" });
		expect(saved.registryId).toBeNull();
	});

	test("rejects when the server refuses the save", async () => {
		stubApi({
			"PUT /team/build-settings": problem(403, {
				title: "Forbidden",
				status: 403,
			}),
		});
		await expect(
			saveTeamBuildSettings({ buildLocation: "target", registryId: null }),
		).rejects.toBeDefined();
	});
});
