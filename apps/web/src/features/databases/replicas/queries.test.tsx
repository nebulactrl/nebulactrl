import { describe, expect, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";
import { screen, waitFor } from "@testing-library/react";
import {
	getDatabasePublicPortQueryKey,
	getDatabaseQueryKey,
	getDatabaseReplicationQueryKey,
	listServicesQueryKey,
} from "#/api/gen/@tanstack/react-query.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { invalidateReplication, useReplication } from "./queries";
import { member, replication } from "./replicas.fixture";

const REPLICATION = "GET /services/:id/database/replication";

function Probe({ enabled }: { enabled?: boolean }) {
	const view = useReplication("svc-1", enabled);
	if (view.isError) return <p>failed</p>;
	if (view.data) return <p>role: {view.data.role}</p>;
	return <p>idle</p>;
}

/** The refetch interval the mounted hook asked the query cache for. */
function pollInterval(queryClient: QueryClient): unknown {
	const query = queryClient.getQueryCache().find({
		queryKey: getDatabaseReplicationQueryKey({
			path: { serviceId: "svc-1" },
		}),
	});
	const options = query?.observers[0]?.options;
	const interval = options?.refetchInterval;
	return typeof interval === "function" ? interval(query as never) : interval;
}

describe("useReplication", () => {
	test("reads the replication view of the service", async () => {
		const api = stubApi({
			[REPLICATION]: { body: replication({ role: "primary" }) },
		});
		await renderWithProviders(<Probe />);
		expect(await screen.findByText("role: primary")).toBeInTheDocument();
		expect(api.calls[0]?.path).toBe("/services/svc-1/database/replication");
	});

	test("asks nothing while disabled", async () => {
		const api = stubApi({ [REPLICATION]: { body: replication() } });
		await renderWithProviders(<Probe enabled={false} />);
		expect(screen.getByText("idle")).toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("does not retry a failure, so the panel can tell an old control plane at once", async () => {
		const api = stubApi({
			[REPLICATION]: {
				status: 404,
				body: { status: 404, title: "x", detail: "no", code: "not_found" },
			},
		});
		// A client with the library's default retries: only the hook's own option stops them.
		await renderWithProviders(<Probe />, { queryClient: new QueryClient() });
		expect(await screen.findByText("failed")).toBeInTheDocument();
		expect(api.calls).toHaveLength(1);
	});

	test("polls every 15 s while nothing moves", async () => {
		stubApi({
			[REPLICATION]: {
				body: replication({ role: "primary", replicas: [member()] }),
			},
		});
		const { queryClient } = await renderWithProviders(<Probe />);
		await screen.findByText("role: primary");
		expect(pollInterval(queryClient)).toBe(15_000);
	});

	test("polls every 3 s while a replica is being made", async () => {
		stubApi({
			[REPLICATION]: {
				body: replication({
					role: "primary",
					replicas: [member({ state: "cloning" })],
				}),
			},
		});
		const { queryClient } = await renderWithProviders(<Probe />);
		await screen.findByText("role: primary");
		expect(pollInterval(queryClient)).toBe(3_000);
	});

	test("polls every 3 s while a promote is running", async () => {
		stubApi({
			[REPLICATION]: {
				body: replication({
					role: "replica",
					promotion: {
						id: "p1",
						status: "running",
						mode: "switchover",
						step: "freeze",
						message: "",
						startedAt: "2026-01-01T00:00:00Z",
					},
				}),
			},
		});
		const { queryClient } = await renderWithProviders(<Probe />);
		await screen.findByText("role: replica");
		expect(pollInterval(queryClient)).toBe(3_000);
	});
});

describe("invalidateReplication", () => {
	test("marks the replication, database and service views stale, and nothing else", async () => {
		const queryClient = new QueryClient();
		const keys = {
			replication: getDatabaseReplicationQueryKey({ path: { serviceId: "s" } }),
			database: getDatabaseQueryKey({ path: { serviceId: "s" } }),
			services: listServicesQueryKey({ path: { environmentId: "e" } }),
			publicPort: getDatabasePublicPortQueryKey({ path: { serviceId: "s" } }),
		};
		for (const key of Object.values(keys)) queryClient.setQueryData(key, {});
		await invalidateReplication(queryClient);
		const stale = (key: readonly unknown[]) =>
			queryClient.getQueryState(key)?.isInvalidated;
		expect(stale(keys.replication)).toBe(true);
		expect(stale(keys.database)).toBe(true);
		expect(stale(keys.services)).toBe(true);
		expect(stale(keys.publicPort)).toBe(false);
	});

	test("refetches a replication view that is on screen", async () => {
		const api = stubApi({
			[REPLICATION]: { body: replication({ role: "primary" }) },
		});
		const { queryClient } = await renderWithProviders(<Probe />);
		await screen.findByText("role: primary");
		expect(api.calls).toHaveLength(1);
		await invalidateReplication(queryClient);
		await waitFor(() => expect(api.calls).toHaveLength(2));
	});
});
