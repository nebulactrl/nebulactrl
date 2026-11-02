import { describe, expect, test } from "bun:test";
import { QueryClient, QueryObserver } from "@tanstack/react-query";
import {
	invalidateOperations,
	invalidateServices,
	SERVICE_OPERATIONS,
} from "./invalidate";

const isInvalidated = (queryClient: QueryClient, key: unknown[]) =>
	queryClient.getQueryState(key)?.isInvalidated;

describe("invalidateOperations", () => {
	test("marks queries of the named operations stale and leaves the others fresh", async () => {
		const queryClient = new QueryClient();
		queryClient.setQueryData([{ _id: "listServices" }], 1);
		queryClient.setQueryData([{ _id: "listServers" }], 2);
		queryClient.setQueryData([{ _id: "getMe" }], 3);
		queryClient.setQueryData(["plain-key"], 4);
		queryClient.setQueryData([null], 5);
		queryClient.setQueryData([{ noId: true }], 6);

		await invalidateOperations(queryClient, ["listServices", "listServers"]);

		expect(isInvalidated(queryClient, [{ _id: "listServices" }])).toBe(true);
		expect(isInvalidated(queryClient, [{ _id: "listServers" }])).toBe(true);
		expect(isInvalidated(queryClient, [{ _id: "getMe" }])).toBe(false);
		expect(isInvalidated(queryClient, ["plain-key"])).toBe(false);
		expect(isInvalidated(queryClient, [null])).toBe(false);
		expect(isInvalidated(queryClient, [{ noId: true }])).toBe(false);
	});

	test("matches the operation id whatever else the key carries (path and query params)", async () => {
		const queryClient = new QueryClient();
		const a = [{ _id: "getService", path: { serviceId: "a" } }];
		const b = [{ _id: "getService", path: { serviceId: "b" } }];
		queryClient.setQueryData(a, 1);
		queryClient.setQueryData(b, 2);
		await invalidateOperations(queryClient, ["getService"]);
		expect(isInvalidated(queryClient, a)).toBe(true);
		expect(isInvalidated(queryClient, b)).toBe(true);
	});

	test("resolves only after the on-screen queries have refetched", async () => {
		const queryClient = new QueryClient();
		const queryKey = [{ _id: "listServices" }];
		let calls = 0;
		const watcher = new QueryObserver(queryClient, {
			queryKey,
			queryFn: async () => {
				calls++;
				await Promise.resolve();
				return calls;
			},
			staleTime: Number.POSITIVE_INFINITY,
		});
		const unsubscribe = watcher.subscribe(() => {});
		await watcher.refetch();
		expect(calls).toBe(1);

		await invalidateOperations(queryClient, ["listServices"]);

		expect(calls).toBe(2);
		expect(queryClient.getQueryData(queryKey)).toBe(2);
		unsubscribe();
	});

	test("does not refetch a query nobody is looking at", async () => {
		const queryClient = new QueryClient();
		const queryKey = [{ _id: "listServices" }];
		let calls = 0;
		await queryClient.fetchQuery({
			queryKey,
			queryFn: () => ++calls,
		});
		await invalidateOperations(queryClient, ["listServices"]);
		expect(calls).toBe(1);
		expect(isInvalidated(queryClient, queryKey)).toBe(true);
	});
});

describe("invalidateServices", () => {
	test("invalidates every service-related operation and nothing else", async () => {
		const queryClient = new QueryClient();
		for (const id of SERVICE_OPERATIONS) {
			queryClient.setQueryData([{ _id: id }], 1);
		}
		queryClient.setQueryData([{ _id: "getMe" }], 1);
		queryClient.setQueryData([{ _id: "listProjects" }], 1);

		await invalidateServices(queryClient);

		for (const id of SERVICE_OPERATIONS) {
			expect(isInvalidated(queryClient, [{ _id: id }])).toBe(true);
		}
		expect(isInvalidated(queryClient, [{ _id: "getMe" }])).toBe(false);
		expect(isInvalidated(queryClient, [{ _id: "listProjects" }])).toBe(false);
	});

	test("covers the queries that draw a service, its links and its servers", () => {
		expect(SERVICE_OPERATIONS).toEqual(
			expect.arrayContaining([
				"listServices",
				"getService",
				"listLinks",
				"listServers",
				"listPendingChanges",
				"setProcessScaling",
			]),
		);
	});
});
