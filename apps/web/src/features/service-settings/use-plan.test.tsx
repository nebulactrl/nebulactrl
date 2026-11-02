import { describe, expect, test } from "bun:test";
import { QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { makeService } from "#/features/canvas/lib/fixtures";
import { stubApi } from "#/test/api";
import { testQueryClient } from "#/test/render";
import { usePlan } from "./use-plan";

const planBody = (overrides: Record<string, unknown> = {}) => ({
	blocking: null,
	changes: null,
	mesh: { blockers: null, crossServer: false, devicesAdded: 0, links: null },
	placement: {
		activeBuilds: 0,
		buildServerId: "",
		buildServerName: "",
		emulated: false,
		fallback: false,
		location: "target",
		maxConcurrent: 1,
		platform: "linux/amd64",
		reason: "",
		registryHost: "",
		registryId: "",
		reuse: false,
		sticky: false,
		warnings: null,
	},
	processes: null,
	reasons: null,
	server: {
		id: "server-1",
		name: "hetzner-01",
		status: "online",
		lastSeenAt: null,
	},
	source: { type: "image", image: "nginx" },
	strategy: "rolling",
	warnings: null,
	...overrides,
});

function wrapper() {
	const queryClient = testQueryClient();
	return ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
	);
}

describe("usePlan", () => {
	test("asks the control plane to plan the service and returns the plan", async () => {
		const api = stubApi({
			"POST /services/s1/deployments/plan": {
				body: planBody({
					strategy: "recreate",
					reasons: ["It has a volume."],
					changes: [{ field: "config.port", from: "80", to: "81" }],
				}),
			},
		});
		const { result } = renderHook(() => usePlan(makeService({ id: "s1" })), {
			wrapper: wrapper(),
		});
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.method).toBe("POST");
		expect(api.calls[0]?.path).toBe("/services/s1/deployments/plan");
		expect(result.current.data?.strategy).toBe("recreate");
		expect(result.current.data?.reasons).toEqual(["It has a volume."]);
		expect(result.current.data?.changes).toEqual([
			{ field: "config.port", from: "80", to: "81" },
		]);
	});

	test("lists the server omits come back as empty arrays", async () => {
		stubApi({ "POST /services/s1/deployments/plan": { body: planBody() } });
		const { result } = renderHook(() => usePlan(makeService({ id: "s1" })), {
			wrapper: wrapper(),
		});
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data?.reasons).toEqual([]);
		expect(result.current.data?.warnings).toEqual([]);
		expect(result.current.data?.changes).toEqual([]);
		expect(result.current.data?.blocking).toEqual([]);
		expect(result.current.data?.server).toEqual({
			id: "server-1",
			name: "hetzner-01",
			status: "online",
			lastSeenAt: null,
		});
	});

	test("a failed plan is an error at once: no retries", async () => {
		const api = stubApi({
			"POST /services/s1/deployments/plan": {
				status: 500,
				body: { code: "internal", detail: "Planner crashed.", status: 500 },
			},
		});
		const { result } = renderHook(() => usePlan(makeService({ id: "s1" })), {
			wrapper: wrapper(),
		});
		await waitFor(() => expect(result.current.isError).toBe(true));
		expect(result.current.error?.message).toBe("Planner crashed.");
		expect(result.current.data).toBeUndefined();
		expect(api.calls).toHaveLength(1);
	});

	test("a new generation of the service is planned again", async () => {
		let strategy = "rolling";
		const api = stubApi({
			"POST /services/s1/deployments/plan": () =>
				Response.json(planBody({ strategy })),
		});
		const { result, rerender } = renderHook(
			({ generation }) => usePlan(makeService({ id: "s1", generation })),
			{ wrapper: wrapper(), initialProps: { generation: 1 } },
		);
		await waitFor(() => expect(result.current.data?.strategy).toBe("rolling"));
		strategy = "recreate";
		rerender({ generation: 2 });
		await waitFor(() => expect(result.current.data?.strategy).toBe("recreate"));
		expect(api.calls).toHaveLength(2);
	});

	test("moving the service to another server is planned again", async () => {
		const api = stubApi({
			"POST /services/s1/deployments/plan": { body: planBody() },
		});
		const { result, rerender } = renderHook(
			({ serverId }) => usePlan(makeService({ id: "s1", serverId })),
			{ wrapper: wrapper(), initialProps: { serverId: "server-1" } },
		);
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		rerender({ serverId: "server-2" });
		await waitFor(() => expect(api.calls).toHaveLength(2));
	});

	test("the same service rendered again does not plan twice", async () => {
		const api = stubApi({
			"POST /services/s1/deployments/plan": { body: planBody() },
		});
		const service = makeService({ id: "s1" });
		const { result, rerender } = renderHook(() => usePlan(service), {
			wrapper: wrapper(),
		});
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		rerender();
		expect(api.calls).toHaveLength(1);
	});
});
