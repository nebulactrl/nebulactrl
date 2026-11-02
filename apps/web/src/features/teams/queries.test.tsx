import { describe, expect, test } from "bun:test";
import { QueryClientProvider } from "@tanstack/react-query";
import type { RegisteredRouter } from "@tanstack/react-router";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import type { InstanceSettings, Team } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { testQueryClient } from "#/test/render";
import { canCreateTeam, leaveTeamState, useCurrentTeam } from "./queries";
import { getTeamScope } from "./team-scope";

function team(over: Partial<Team> = {}): Team {
	return {
		id: "t-1",
		name: "Acme",
		slug: "acme",
		role: "admin",
		current: true,
		memberCount: 2,
		createdAt: "2026-01-01T00:00:00Z",
		...over,
	};
}

function wrapper(client = testQueryClient()) {
	return ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={client}>{children}</QueryClientProvider>
	);
}

describe("useCurrentTeam", () => {
	test("is pending until the teams arrive, then picks the current team and its role", async () => {
		stubApi({
			"GET /teams": {
				body: {
					items: [
						team({ id: "t-0", name: "Other", current: false, role: "viewer" }),
						team(),
					],
				},
			},
		});
		const { result } = renderHook(() => useCurrentTeam(), {
			wrapper: wrapper(),
		});

		expect(result.current.pending).toBe(true);
		expect(result.current.team).toBeUndefined();
		expect(result.current.teams).toEqual([]);

		await waitFor(() => expect(result.current.pending).toBe(false));
		expect(result.current.team?.id).toBe("t-1");
		expect(result.current.role).toBe("admin");
		expect(result.current.teams.map((t) => t.id)).toEqual(["t-0", "t-1"]);
	});

	test("no current team means no team and no role", async () => {
		stubApi({ "GET /teams": { body: { items: [team({ current: false })] } } });
		const { result } = renderHook(() => useCurrentTeam(), {
			wrapper: wrapper(),
		});
		await waitFor(() => expect(result.current.pending).toBe(false));
		expect(result.current.team).toBeUndefined();
		expect(result.current.role).toBeUndefined();
		expect(result.current.teams).toHaveLength(1);
	});

	test("a role the console does not know yields no role", async () => {
		stubApi({
			// A function reply: the contract allows four roles, and the console must
			// still cope with one it does not know.
			"GET /teams": () =>
				Response.json({ items: [{ ...team(), role: "auditor" }] }),
		});
		const { result } = renderHook(() => useCurrentTeam(), {
			wrapper: wrapper(),
		});
		await waitFor(() => expect(result.current.team?.id).toBe("t-1"));
		expect(result.current.role).toBeUndefined();
	});

	test("a failed request is not pending and has no team", async () => {
		stubApi({
			"GET /teams": {
				status: 500,
				body: { status: 500, code: "internal", title: "Boom" },
			},
		});
		const { result } = renderHook(() => useCurrentTeam(), {
			wrapper: wrapper(),
		});
		await waitFor(() => expect(result.current.pending).toBe(false));
		expect(result.current.team).toBeUndefined();
	});
});

describe("canCreateTeam", () => {
	const base: InstanceSettings = {
		allowTeamCreation: false,
		canManage: false,
		smtpConfigured: false,
	};
	test.each([
		["not loaded", undefined, false],
		["closed for a plain user", base, false],
		["open to everyone", { ...base, allowTeamCreation: true }, true],
		[
			"closed but the user runs the instance",
			{ ...base, canManage: true },
			true,
		],
	] as const)("%s", (_label, instance, expected) => {
		expect(canCreateTeam(instance)).toBe(expected);
	});
});

describe("leaveTeamState", () => {
	test("empties the cache, goes home and fetches the new team's teams and projects", async () => {
		const api = stubApi({
			"GET /teams": { body: { items: [team({ name: "New" })] } },
			"GET /projects": { body: { items: [] } },
		});
		const client = testQueryClient();
		client.setQueryData(["old"], "previous team's data");
		const calls: string[] = [];
		const router = {
			invalidate: async () => {
				calls.push("invalidate");
				expect(client.getQueryData(["old"])).toBeUndefined();
			},
			navigate: async (opts: { to: string }) => {
				calls.push(`navigate ${opts.to}`);
			},
		} as unknown as RegisteredRouter;
		const before = getTeamScope().epoch;

		await leaveTeamState(client, router);

		expect(calls).toEqual(["invalidate", "navigate /"]);
		expect(api.calls.map((c) => c.path).sort()).toEqual([
			"/projects",
			"/teams",
		]);
		expect(
			api.calls.find((c) => c.path === "/projects")?.query.get("limit"),
		).toBe("200");
		expect(client.getQueryData(["old"])).toBeUndefined();
		expect(getTeamScope()).toEqual({ epoch: before + 1, switching: false });
	});

	test("a failing lookup does not leave the screen suspended", async () => {
		stubApi({
			"GET /teams": {
				status: 500,
				body: { status: 500, code: "x", title: "x" },
			},
			"GET /projects": {
				status: 500,
				body: { status: 500, code: "x", title: "x" },
			},
		});
		const router = {
			invalidate: async () => undefined,
			navigate: async () => undefined,
		} as unknown as RegisteredRouter;
		await leaveTeamState(testQueryClient(), router);
		expect(getTeamScope().switching).toBe(false);
	});
});
