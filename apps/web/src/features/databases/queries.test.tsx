import { describe, expect, test } from "bun:test";
import { useQuery } from "@tanstack/react-query";
import { waitFor } from "@testing-library/react";
import { serviceQuery } from "#/api/hooks";
import { stubApi } from "#/test/api";
import { renderWithProviders, testQueryClient } from "#/test/render";
import { catalogBody, databaseView, serviceView } from "./database.fixture";
import {
	DATABASE_POLL_MS,
	databaseQuery,
	enginesQuery,
	invalidateDatabase,
	invalidateDatabaseAndServices,
	isManagedDatabase,
} from "./queries";

describe("isManagedDatabase", () => {
	test("a database service with a managed spec is managed", () => {
		expect(
			isManagedDatabase({
				kind: "database",
				config: { database: { engine: "postgres", version: "16" } },
			} as Parameters<typeof isManagedDatabase>[0]),
		).toBe(true);
	});

	test("a database service without a spec is a plain image", () => {
		expect(
			isManagedDatabase({
				kind: "database",
				config: {},
			} as Parameters<typeof isManagedDatabase>[0]),
		).toBe(false);
	});

	test("an app with a leftover database spec is not managed", () => {
		expect(
			isManagedDatabase({
				kind: "app",
				config: { database: { engine: "postgres", version: "16" } },
			} as Parameters<typeof isManagedDatabase>[0]),
		).toBe(false);
	});
});

describe("query options", () => {
	test("the engine catalog is considered fresh for ten minutes", () => {
		expect(enginesQuery().staleTime).toBe(10 * 60_000);
	});

	test("the database view is fetched per service from /services/:id/database", async () => {
		const api = stubApi({
			"GET /services/:id/database": { body: databaseView() },
		});
		const client = testQueryClient();
		const view = await client.fetchQuery(databaseQuery("svc-42"));
		expect(view.image).toBe("postgres:16");
		expect(api.calls.map((call) => `${call.method} ${call.path}`)).toEqual([
			"GET /services/svc-42/database",
		]);
		// A different service is a different cache entry.
		expect(databaseQuery("svc-42").queryKey).not.toEqual(
			databaseQuery("svc-43").queryKey,
		);
	});

	test("the poll interval is three seconds", () => {
		expect(DATABASE_POLL_MS).toBe(3_000);
	});
});

function Views() {
	useQuery(databaseQuery("svc-1"));
	useQuery(enginesQuery());
	return null;
}

function Both() {
	useQuery(databaseQuery("svc-1"));
	useQuery(serviceQuery("svc-1"));
	return null;
}

describe("invalidation", () => {
	test("invalidateDatabase refetches the database views on screen and nothing else", async () => {
		const api = stubApi({
			"GET /services/:id/database": { body: databaseView() },
			"GET /database-engines": { body: catalogBody },
		});
		const queryClient = testQueryClient();
		await renderWithProviders(<Views />, { queryClient });
		await waitFor(() => expect(api.calls).toHaveLength(2));

		await invalidateDatabase(queryClient);
		const paths = api.calls.map((call) => call.path);
		expect(
			paths.filter((path) => path === "/services/svc-1/database"),
		).toHaveLength(2);
		expect(paths.filter((path) => path === "/database-engines")).toHaveLength(
			1,
		);
	});

	test("invalidateDatabaseAndServices also refetches a service on screen", async () => {
		const api = stubApi({
			"GET /services/:id/database": { body: databaseView() },
			"GET /services/:id": { body: serviceView() },
		});
		const queryClient = testQueryClient();
		await renderWithProviders(<Both />, { queryClient });
		await waitFor(() => expect(api.calls).toHaveLength(2));

		await invalidateDatabaseAndServices(queryClient);
		const paths = api.calls.map((call) => call.path);
		expect(
			paths.filter((path) => path === "/services/svc-1/database"),
		).toHaveLength(2);
		expect(paths.filter((path) => path === "/services/svc-1")).toHaveLength(2);
	});
});
