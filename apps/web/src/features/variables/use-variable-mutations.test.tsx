import { describe, expect, it } from "bun:test";
import { useQuery } from "@tanstack/react-query";
import { act, screen, waitFor } from "@testing-library/react";
import { variablesQuery } from "#/api/hooks";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import type { EnvDiff } from "./raw-env";
import { useVariableMutations } from "./use-variable-mutations";

type Mutations = ReturnType<typeof useVariableMutations>;

const emptyList = make("VariableList");

function Harness({ hook }: { hook: { current: Mutations | undefined } }) {
	hook.current = useVariableMutations("svc-1");
	const list = useQuery(variablesQuery("svc-1"));
	return <p>{list.isSuccess ? "loaded" : "loading"}</p>;
}

/** Mounts the hook next to a live variables query, the way the tab uses it. */
async function mountHook(routes: Parameters<typeof stubApi>[0]) {
	const api = stubApi({
		"GET /services/:id/variables": { body: emptyList },
		...routes,
	});
	const hook: { current: Mutations | undefined } = { current: undefined };
	await renderWithProviders(<Harness hook={hook} />);
	await screen.findByText("loaded");
	const mutations = () => {
		if (!hook.current) throw new Error("hook did not mount");
		return hook.current;
	};
	return { api, mutations };
}

const listCalls = (api: { calls: { method: string; path: string }[] }) =>
	api.calls.filter(
		(call) =>
			call.method === "GET" && call.path === "/services/svc-1/variables",
	).length;

/** Runs a mutation inside act and lets the query cache's batched notifications land in it. */
async function run(action: () => Promise<unknown>) {
	await act(async () => {
		await action();
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
}

describe("useVariableMutations", () => {
	it("save PUTs one variable and returns the server's answer", async () => {
		const { api, mutations } = await mountHook({
			"PUT /services/:id/variables": {
				body: make("VariableList", {
					restaged: [{ serviceId: "s2", slug: "web" }],
				}),
			},
		});
		let result:
			| Awaited<ReturnType<Mutations["save"]["mutateAsync"]>>
			| undefined;
		await run(async () => {
			result = await mutations().save.mutateAsync({
				key: "API_KEY",
				value: "abc",
				isSecret: true,
				scope: "both",
				comment: "note",
				rotateEveryDays: 30,
			});
		});
		const put = api.calls.find((call) => call.method === "PUT");
		expect(put?.path).toBe("/services/svc-1/variables");
		expect(put?.body).toEqual({
			variables: [
				{
					key: "API_KEY",
					value: "abc",
					isSecret: true,
					scope: "both",
					comment: "note",
					rotateEveryDays: 30,
				},
			],
		});
		expect(result?.restaged).toEqual([{ serviceId: "s2", slug: "web" }]);
	});

	it("saveMany PUTs every variable in one request", async () => {
		const { api, mutations } = await mountHook({
			"PUT /services/:id/variables": { body: emptyList },
		});
		await run(async () => {
			await mutations().saveMany.mutateAsync([
				{ key: "A", value: "1" },
				{ key: "B", value: "2", source: "suggested" },
			]);
		});
		const puts = api.calls.filter((call) => call.method === "PUT");
		expect(puts).toHaveLength(1);
		expect(puts[0]?.body).toEqual({
			variables: [
				{ key: "A", value: "1" },
				{ key: "B", value: "2", source: "suggested" },
			],
		});
	});

	it("remove DELETEs the variable by key", async () => {
		const { api, mutations } = await mountHook({
			"DELETE /services/:id/variables/:key": { status: 204 },
		});
		await run(async () => {
			await mutations().remove.mutateAsync("OLD_KEY");
		});
		const del = api.calls.find((call) => call.method === "DELETE");
		expect(del?.path).toBe("/services/svc-1/variables/OLD_KEY");
	});

	it("applyDiff upserts in one PUT, then deletes each removed key", async () => {
		const { api, mutations } = await mountHook({
			"PUT /services/:id/variables": { body: emptyList },
			"DELETE /services/:id/variables/:key": { status: 204 },
		});
		const diff: EnvDiff = {
			upserts: [
				{ key: "NEW", value: "1" },
				{ key: "CHANGED", value: "2" },
			],
			deletes: ["GONE_A", "GONE_B"],
		};
		await run(async () => {
			await mutations().applyDiff.mutateAsync(diff);
		});
		const writes = api.calls
			.filter((call) => call.method !== "GET")
			.map((call) => `${call.method} ${call.path}`);
		expect(writes).toEqual([
			"PUT /services/svc-1/variables",
			"DELETE /services/svc-1/variables/GONE_A",
			"DELETE /services/svc-1/variables/GONE_B",
		]);
		expect(api.calls.find((call) => call.method === "PUT")?.body).toEqual({
			variables: diff.upserts,
		});
	});

	it("applyDiff skips the PUT when there is nothing to upsert", async () => {
		const { api, mutations } = await mountHook({
			"DELETE /services/:id/variables/:key": { status: 204 },
		});
		await run(async () => {
			await mutations().applyDiff.mutateAsync({ upserts: [], deletes: ["X"] });
		});
		expect(api.calls.filter((call) => call.method === "PUT")).toEqual([]);
		expect(api.calls.filter((call) => call.method === "DELETE")).toHaveLength(
			1,
		);
		expect(api.unmatched).toEqual([]);
	});

	it("applyDiff stops at the first failed delete and reports it", async () => {
		const { api, mutations } = await mountHook({
			"DELETE /services/:id/variables/:key": (request) =>
				new URL(request.url).pathname.endsWith("/A")
					? Response.json(
							problem(409, {
								code: "conflict",
								title: "Locked",
								detail: "A is managed",
							}).body,
							{ status: 409 },
						)
					: new Response(null, { status: 204 }),
		});
		await run(async () => {
			await mutations()
				.applyDiff.mutateAsync({ upserts: [], deletes: ["A", "B"] })
				.catch(() => undefined);
		});
		await waitFor(() => expect(mutations().applyDiff.isError).toBe(true));
		expect(
			api.calls.filter((call) => call.method === "DELETE").map((c) => c.path),
		).toEqual(["/services/svc-1/variables/A"]);
	});

	it("discard drops the staged change of one variable", async () => {
		const { api, mutations } = await mountHook({
			"DELETE /services/:id/pending-changes/:field": {
				body: make("ServiceView"),
			},
		});
		await run(async () => {
			await mutations().discard.mutateAsync("DB_URL");
		});
		const del = api.calls.find((call) => call.method === "DELETE");
		expect(del?.path).toBe("/services/svc-1/pending-changes/variables.DB_URL");
	});

	it("rotate stages a new value, not a dry run", async () => {
		const plan = make("VariableRotationPlan", {
			key: "SECRET",
			kind: "app-secret",
			staged: true,
			affected: [],
			blockers: [],
			deployOrder: ["api"],
			warnings: [],
		});
		const { api, mutations } = await mountHook({
			"POST /services/:id/variables/:key/rotate": { body: plan },
		});
		let result: unknown;
		await run(async () => {
			result = await mutations().rotate.mutateAsync("SECRET");
		});
		const post = api.calls.find((call) => call.method === "POST");
		expect(post?.path).toBe("/services/svc-1/variables/SECRET/rotate");
		expect(post?.body).toEqual({ dryRun: false });
		expect(result).toEqual(plan);
	});

	it("restore writes the chosen version again", async () => {
		const { api, mutations } = await mountHook({
			"POST /services/:id/variables/:key/history/:version/restore": {
				body: emptyList,
			},
		});
		await run(async () => {
			await mutations().restore.mutateAsync({ key: "LEVEL", version: 3 });
		});
		const post = api.calls.find((call) => call.method === "POST");
		expect(post?.path).toBe(
			"/services/svc-1/variables/LEVEL/history/3/restore",
		);
	});

	it("refetches the variables after a write and settles only once they arrived", async () => {
		const refetch = Promise.withResolvers<void>();
		let lists = 0;
		const { api, mutations } = await mountHook({
			"GET /services/:id/variables": async () => {
				lists += 1;
				if (lists > 1) await refetch.promise;
				return Response.json(emptyList);
			},
			"PUT /services/:id/variables": { body: emptyList },
		});
		expect(listCalls(api)).toBe(1);

		let settled = false;
		let pending: Promise<unknown> | undefined;
		await run(async () => {
			pending = mutations()
				.save.mutateAsync({ key: "K", value: "v" })
				.then(() => {
					settled = true;
				});
		});
		await waitFor(() => expect(listCalls(api)).toBe(2));
		// The PUT is done and the list is refetching: the write is not reported yet.
		expect(api.calls.some((call) => call.method === "PUT")).toBe(true);
		expect(settled).toBe(false);

		await run(async () => {
			refetch.resolve();
			await pending;
		});
		expect(settled).toBe(true);
	});

	it("does not refetch and exposes the error when the write fails", async () => {
		const { api, mutations } = await mountHook({
			"PUT /services/:id/variables": problem(422, {
				code: "invalid",
				title: "Invalid",
				detail: "bad key",
			}),
		});
		await run(async () => {
			await mutations()
				.save.mutateAsync({ key: "bad key" })
				.catch(() => undefined);
		});
		await waitFor(() => expect(mutations().save.isError).toBe(true));
		expect(listCalls(api)).toBe(1);
	});
});
