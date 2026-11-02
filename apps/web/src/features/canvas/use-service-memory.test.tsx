import { expect, test } from "bun:test";
import { QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import type { Service } from "#/api/models";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { testQueryClient } from "#/test/render";
import { makeObserved, makeService } from "./lib/fixtures";
import { useServiceMemory } from "./use-service-memory";

const MIB = 1024 * 1024;

function wrapper({ children }: { children: ReactNode }) {
	return (
		<QueryClientProvider client={testQueryClient()}>
			{children}
		</QueryClientProvider>
	);
}

/** Renders the hook and resolves the query client, to wait for a settled query. */
function renderMemory(service: Service) {
	const client = testQueryClient();
	const rendered = renderHook(() => useServiceMemory(service), {
		wrapper: ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={client}>{children}</QueryClientProvider>
		),
	});
	const settled = () =>
		waitFor(() =>
			expect(client.getQueryCache().getAll()[0]?.state.status).toBe("success"),
		);
	return { ...rendered, settled };
}

function running(overrides: Partial<Service> = {}): Service {
	return makeService({
		id: "svc-m",
		currentDeploymentId: "dep-1",
		desiredState: "running",
		observed: makeObserved(),
		...overrides,
	});
}

function point(overrides: Record<string, unknown> = {}) {
	return {
		ts: "2026-01-01T00:00:00Z",
		cpuMax: 0,
		cpuPerReplica: 0,
		cpuPercent: 0,
		memBytes: 100 * MIB,
		memLimitBytes: 512 * MIB,
		memMax: 0,
		replicaCount: 1,
		...overrides,
	};
}

const body = (points: unknown[] | null) => ({
	body: { points, retentionDays: 7, stepSeconds: 5 },
});

test("a running service shows the newest sample against the container limit", async () => {
	const api = stubApi({
		"GET /services/:id/metrics": body([
			point({ memBytes: 10 * MIB }),
			point({ memBytes: 300 * MIB, memLimitBytes: 1024 * MIB }),
		]),
	});
	const { result } = renderHook(() => useServiceMemory(running()), { wrapper });
	await waitFor(() => expect(result.current).not.toBeNull());
	expect(result.current).toEqual({
		usedBytes: 300 * MIB,
		limitBytes: 1024 * MIB,
	});
	expect(api.calls).toHaveLength(1);
	expect(api.calls[0]?.path).toBe("/services/svc-m/metrics");
	expect(api.calls[0]?.query.get("range")).toBe("5m");
});

test("without a measured limit the configured memory is the limit", async () => {
	stubApi({
		"GET /services/:id/metrics": body([point({ memLimitBytes: 0 })]),
	});
	const service = running();
	service.config = { ...service.config, memoryMb: 256 };
	const { result } = renderHook(() => useServiceMemory(service), { wrapper });
	await waitFor(() => expect(result.current).not.toBeNull());
	expect(result.current).toEqual({
		usedBytes: 100 * MIB,
		limitBytes: 256 * MIB,
	});
});

test("no limit anywhere gives limit 0 (the card draws no bar fill ratio)", async () => {
	stubApi({
		"GET /services/:id/metrics": body([point({ memLimitBytes: 0 })]),
	});
	const { result } = renderHook(() => useServiceMemory(running()), { wrapper });
	await waitFor(() => expect(result.current).not.toBeNull());
	expect(result.current?.limitBytes).toBe(0);
});

test("a newest bucket with no replicas has no memory to show", async () => {
	const api = stubApi({
		"GET /services/:id/metrics": body([point({ replicaCount: 0 })]),
	});
	const { result, settled } = renderMemory(running());
	await settled();
	expect(api.calls).toHaveLength(1);
	expect(result.current).toBeNull();
});

test("no points yet gives null", async () => {
	const api = stubApi({ "GET /services/:id/metrics": body(null) });
	const { result, settled } = renderMemory(running());
	await settled();
	expect(api.calls).toHaveLength(1);
	expect(result.current).toBeNull();
});

test.each<[string, Partial<Service>]>([
	["never deployed", { currentDeploymentId: null }],
	["stopped on purpose", { desiredState: "stopped" }],
	[
		"not running on its server",
		{ observed: makeObserved({ state: "exited" }) },
	],
	["not observed yet", { observed: null }],
])(
	"a service that is %s is never asked for samples",
	async (_name, overrides) => {
		const api = stubApi({ "GET /services/:id/metrics": body([point()]) });
		const { result } = renderHook(() => useServiceMemory(running(overrides)), {
			wrapper,
		});
		expect(result.current).toBeNull();
		// Let any (wrongly) started request surface.
		await Promise.resolve();
		expect(api.calls).toEqual([]);
	},
);

test("a failed sample request leaves the card without a bar", async () => {
	const api = stubApi({
		"GET /services/:id/metrics": problem(500),
	});
	const { result } = renderHook(() => useServiceMemory(running()), { wrapper });
	await waitFor(() => expect(api.calls).toHaveLength(1));
	expect(result.current).toBeNull();
});
