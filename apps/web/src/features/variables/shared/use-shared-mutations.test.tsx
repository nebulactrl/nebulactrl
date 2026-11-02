import { describe, expect, it } from "bun:test";
import { QueryClientProvider, useQuery } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { servicesQuery } from "#/api/hooks";
import { type ApiStub, stubApi } from "#/test/api";
import { testQueryClient } from "#/test/render";
import { type SharedOwner, useSharedMutations } from "./use-shared-mutations";

const OWNERS: { owner: SharedOwner; base: string }[] = [
	{ owner: { layer: "team" }, base: "/team/shared-variables" },
	{
		owner: { layer: "project", id: "p1" },
		base: "/projects/p1/shared-variables",
	},
	{
		owner: { layer: "environment", id: "e1" },
		base: "/environments/e1/shared-variables",
	},
];

const EMPTY_LIST = { items: [], restaged: [], warnings: [] };

/** Routes for every layer, answering each write with an empty list. */
function stubWrites(): ApiStub {
	const routes: Record<string, { body: unknown }> = {
		"GET /environments/e1/services": { body: { items: [] } },
	};
	for (const { base } of OWNERS) {
		routes[`PUT ${base}`] = { body: EMPTY_LIST };
		routes[`DELETE ${base}/:key`] = { body: EMPTY_LIST };
		routes[`POST ${base}/:key/share`] = { body: EMPTY_LIST };
		routes[`POST ${base}/:key/rotate`] = {
			body: {
				key: "K",
				kind: "app-secret",
				affected: [],
				deployOrder: [],
				warnings: [],
				blockers: [],
				staged: false,
			},
		};
	}
	return stubApi(routes);
}

function mount(owner: SharedOwner) {
	const queryClient = testQueryClient();
	const wrapper = ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
	);
	// A mounted service list stands for the services on screen: every write that
	// can restage services must refetch it.
	return renderHook(
		() => {
			useQuery(servicesQuery("e1"));
			return useSharedMutations(owner);
		},
		{ wrapper },
	);
}

const serviceListCalls = (api: ApiStub) =>
	api.calls.filter((call) => call.path === "/environments/e1/services").length;

describe("useSharedMutations", () => {
	for (const { owner, base } of OWNERS) {
		describe(`${owner.layer} owner`, () => {
			it("saves one variable with PUT to its own layer", async () => {
				const api = stubWrites();
				const { result } = mount(owner);
				await result.current.save.mutateAsync({
					key: "SESSION",
					value: "abc",
					isSecret: true,
					comment: "cookie signing",
				});
				const put = api.calls.find((call) => call.method === "PUT");
				expect(put?.path).toBe(base);
				expect(put?.body).toEqual({
					variables: [
						{
							key: "SESSION",
							value: "abc",
							isSecret: true,
							comment: "cookie signing",
						},
					],
				});
				expect(api.unmatched).toEqual([]);
			});

			it("deletes by key", async () => {
				const api = stubWrites();
				const { result } = mount(owner);
				await result.current.remove.mutateAsync("OLD_KEY");
				const del = api.calls.find((call) => call.method === "DELETE");
				expect(del?.path).toBe(`${base}/OLD_KEY`);
				expect(api.unmatched).toEqual([]);
			});

			it("shares to the chosen service ids", async () => {
				const api = stubWrites();
				const { result } = mount(owner);
				await result.current.share.mutateAsync({
					key: "REGION",
					serviceIds: ["s1", "s2"],
				});
				const post = api.calls.find((call) => call.path.endsWith("/share"));
				expect(post?.method).toBe("POST");
				expect(post?.path).toBe(`${base}/REGION/share`);
				expect(post?.body).toEqual({ serviceIds: ["s1", "s2"] });
				expect(api.unmatched).toEqual([]);
			});

			it("rotates with the dryRun flag it was given", async () => {
				const api = stubWrites();
				const { result } = mount(owner);
				await result.current.rotate.mutateAsync({ key: "K", dryRun: true });
				await result.current.rotate.mutateAsync({ key: "K", dryRun: false });
				const posts = api.calls.filter((call) => call.path.endsWith("/rotate"));
				expect(posts.map((call) => call.path)).toEqual([
					`${base}/K/rotate`,
					`${base}/K/rotate`,
				]);
				expect(posts.map((call) => call.body)).toEqual([
					{ dryRun: true },
					{ dryRun: false },
				]);
				expect(api.unmatched).toEqual([]);
			});
		});
	}

	it("refetches the service queries after a write that can restage services", async () => {
		const api = stubWrites();
		const { result } = mount({ layer: "project", id: "p1" });
		await waitFor(() => expect(serviceListCalls(api)).toBe(1));

		await result.current.save.mutateAsync({ key: "A", value: "1" });
		await waitFor(() => expect(serviceListCalls(api)).toBe(2));
		await result.current.remove.mutateAsync("A");
		await waitFor(() => expect(serviceListCalls(api)).toBe(3));
		await result.current.share.mutateAsync({ key: "A", serviceIds: ["s1"] });
		await waitFor(() => expect(serviceListCalls(api)).toBe(4));
		await result.current.rotate.mutateAsync({ key: "A", dryRun: false });
		await waitFor(() => expect(serviceListCalls(api)).toBe(5));
	});

	it("leaves the service queries alone after a dry-run rotation", async () => {
		const api = stubWrites();
		const { result } = mount({ layer: "project", id: "p1" });
		await waitFor(() => expect(serviceListCalls(api)).toBe(1));
		await result.current.rotate.mutateAsync({ key: "A", dryRun: true });
		expect(serviceListCalls(api)).toBe(1);
	});

	it("reports a failed write through the mutation's error without refetching", async () => {
		const api = stubApi({
			"GET /environments/e1/services": { body: { items: [] } },
			"PUT /projects/p1/shared-variables": {
				status: 409,
				body: {
					status: 409,
					code: "conflict",
					title: "Conflict",
					detail: "key is reserved",
				},
			},
		});
		const { result } = mount({ layer: "project", id: "p1" });
		await waitFor(() => expect(serviceListCalls(api)).toBe(1));
		await expect(
			result.current.save.mutateAsync({ key: "PORT", value: "1" }),
		).rejects.toBeDefined();
		await waitFor(() => expect(result.current.save.isError).toBe(true));
		expect(serviceListCalls(api)).toBe(1);
	});
});
