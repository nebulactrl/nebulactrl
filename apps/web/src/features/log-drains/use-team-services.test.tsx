import { describe, expect, test } from "bun:test";
import { QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { testQueryClient } from "#/test/render";
import {
	environment,
	project,
	serviceView,
	TEAM_ROUTES,
} from "./test-fixtures";
import { useTeamServices } from "./use-team-services";

function setup(enabled: boolean) {
	const client = testQueryClient();
	const wrapper = ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={client}>{children}</QueryClientProvider>
	);
	return renderHook(({ on }) => useTeamServices(on), {
		wrapper,
		initialProps: { on: enabled },
	});
}

describe("useTeamServices", () => {
	test("walks projects → environments → services, tagging each with where it lives", async () => {
		const api = stubApi(TEAM_ROUTES);
		const { result } = setup(true);
		expect(result.current.pending).toBe(true);
		await waitFor(() => expect(result.current.services).toHaveLength(3));
		await waitFor(() => expect(result.current.pending).toBe(false));
		expect(result.current.services).toEqual([
			{
				id: "svc-cron",
				name: "cron",
				projectId: "p-ops",
				projectName: "Ops",
				environmentName: "Staging",
			},
			{
				id: "svc-api",
				name: "api",
				projectId: "p-web",
				projectName: "Web",
				environmentName: "Production",
			},
			{
				id: "svc-web",
				name: "web",
				projectId: "p-web",
				projectName: "Web",
				environmentName: "Production",
			},
		]);
		expect(api.unmatched).toEqual([]);
	});

	test("sorts by project, then environment, then service name", async () => {
		stubApi({
			"GET /projects": {
				body: { items: [project("p1", "B"), project("p2", "A")] },
			},
			"GET /projects/p1/environments": {
				body: {
					items: [
						environment("e1", "p1", "Staging"),
						environment("e2", "p1", "Production"),
					],
				},
			},
			"GET /projects/p2/environments": {
				body: { items: [environment("e3", "p2", "Z")] },
			},
			"GET /environments/e1/services": {
				body: {
					items: [
						serviceView("s1", "zeta", "p1", "e1"),
						serviceView("s2", "alpha", "p1", "e1"),
					],
				},
			},
			"GET /environments/e2/services": {
				body: { items: [serviceView("s3", "mid", "p1", "e2")] },
			},
			"GET /environments/e3/services": {
				body: { items: [serviceView("s4", "only", "p2", "e3")] },
			},
		});
		const { result } = setup(true);
		await waitFor(() => expect(result.current.services).toHaveLength(4));
		expect(
			result.current.services.map(
				(s) => `${s.projectName}/${s.environmentName}/${s.name}`,
			),
		).toEqual([
			"A/Z/only",
			"B/Production/mid",
			"B/Staging/alpha",
			"B/Staging/zeta",
		]);
	});

	test("fetches nothing and is not pending while disabled", async () => {
		const api = stubApi(TEAM_ROUTES);
		const { result } = setup(false);
		await new Promise((r) => setTimeout(r, 0));
		expect(result.current).toEqual({ services: [], pending: false });
		expect(api.calls).toEqual([]);
	});

	test("starts fetching when enabled turns on", async () => {
		const api = stubApi(TEAM_ROUTES);
		const { result, rerender } = setup(false);
		expect(api.calls).toEqual([]);
		rerender({ on: true });
		await waitFor(() => expect(result.current.services).toHaveLength(3));
		expect(api.calls.map((c) => `${c.method} ${c.path}`)).toContain(
			"GET /projects",
		);
	});

	test("an empty team has no services and stops pending", async () => {
		stubApi({ "GET /projects": { body: { items: [] } } });
		const { result } = setup(true);
		await waitFor(() => expect(result.current.pending).toBe(false));
		expect(result.current.services).toEqual([]);
	});

	test("a project whose environments fail contributes nothing but the others still list", async () => {
		stubApi({
			...TEAM_ROUTES,
			"GET /projects/p-ops/environments": problem(500, { title: "boom" }),
		});
		const { result } = setup(true);
		await waitFor(() => expect(result.current.services).toHaveLength(2));
		expect(result.current.services.map((s) => s.name)).toEqual(["api", "web"]);
	});
});
