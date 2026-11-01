import { describe, expect, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";
import type { RegisteredRouter } from "@tanstack/react-router";
import { getTeamScope, resetTeamScope } from "./team-scope";

function fakeRouter(log: string[]): RegisteredRouter {
	return {
		invalidate: async () => {
			log.push(`invalidate:${String(getTeamScope().switching)}`);
		},
		navigate: async () => {
			log.push(`navigate:${String(getTeamScope().switching)}`);
		},
	} as unknown as RegisteredRouter;
}

describe("resetTeamScope", () => {
	test("keeps the screen suspended until the new team's lists are in", async () => {
		const client = new QueryClient();
		client.setQueryData(["projects"], ["the old team's project"]);
		const log: string[] = [];
		const before = getTeamScope().epoch;

		await resetTeamScope(client, fakeRouter(log), async (cleared) => {
			log.push(`warm:${String(getTeamScope().switching)}`);
			expect(cleared.getQueryData(["projects"])).toBeUndefined();
		});

		expect(log).toEqual(["invalidate:true", "navigate:true", "warm:true"]);
		const after = getTeamScope();
		expect(after.switching).toBe(false);
		// A new epoch remounts the screen, so no observer keeps the old rows.
		expect(after.epoch).toBe(before + 1);
	});

	test("resumes the screen when the reload fails", async () => {
		const client = new QueryClient();
		const failing = {
			invalidate: async () => {
				throw new Error("offline");
			},
			navigate: async () => undefined,
		} as unknown as RegisteredRouter;

		await expect(
			resetTeamScope(client, failing, async () => undefined),
		).rejects.toThrow("offline");
		expect(getTeamScope().switching).toBe(false);
	});
});
