import { expect, test } from "bun:test";
import { QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { testQueryClient } from "#/test/render";
import { useLinks, useServers, useServices } from "./data";

function wrapper({ children }: { children: ReactNode }) {
	return (
		<QueryClientProvider client={testQueryClient()}>
			{children}
		</QueryClientProvider>
	);
}

test("useServices lists the environment's services as service models", async () => {
	const api = stubApi({
		"GET /environments/:id/services": {
			body: make("PageServiceView", {
				items: [make("ServiceView", { id: "s1", name: "Web" })],
			}),
		},
	});
	const { result } = renderHook(() => useServices("env-9"), { wrapper });
	await waitFor(() => expect(result.current.data).toHaveLength(1));
	expect(result.current.data?.[0]?.name).toBe("Web");
	expect(api.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
		"GET /environments/env-9/services",
	]);
});

test("useServices waits for an environment: no request without an id", async () => {
	const api = stubApi({});
	const { result } = renderHook(() => useServices(undefined), { wrapper });
	expect(result.current.fetchStatus).toBe("idle");
	expect(result.current.data).toBeUndefined();
	expect(api.calls).toEqual([]);
	expect(api.unmatched).toEqual([]);
});

test("useLinks reads the environment's links", async () => {
	const api = stubApi({
		"GET /environments/:id/links": {
			body: make("LinkListOutputBody", {
				items: [
					make("LinkView", {
						id: "l1",
						source: "manual",
						fromServiceId: "a",
						toServiceId: "b",
					}),
				],
			}),
		},
	});
	const { result } = renderHook(() => useLinks("env-1"), { wrapper });
	await waitFor(() => expect(result.current.data).toHaveLength(1));
	expect(result.current.data?.[0]?.id).toBe("l1");
	expect(api.calls[0]?.path).toBe("/environments/env-1/links");
});

test("useLinks does not fetch without an environment", () => {
	const api = stubApi({});
	const { result } = renderHook(() => useLinks(undefined), { wrapper });
	expect(result.current.fetchStatus).toBe("idle");
	expect(api.calls).toEqual([]);
});

test("a failing link list is not retried, so the canvas is never held up", async () => {
	const api = stubApi({
		"GET /environments/:id/links": problem(500, { title: "boom" }),
	});
	// The shared test client already disables retries; this hook must too.
	const client = testQueryClient();
	client.setDefaultOptions({ queries: { retry: 3, retryDelay: 1 } });
	const { result } = renderHook(() => useLinks("env-1"), {
		wrapper: ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={client}>{children}</QueryClientProvider>
		),
	});
	await waitFor(() => expect(result.current.isError).toBe(true));
	expect(api.calls).toHaveLength(1);
});

test("useServers lists the servers", async () => {
	const api = stubApi({
		"GET /servers": {
			body: make("PageServer", {
				items: [make("Server", { id: "srv-1", name: "hetzner-01" })],
			}),
		},
	});
	const { result } = renderHook(() => useServers(), { wrapper });
	await waitFor(() => expect(result.current.data).toHaveLength(1));
	expect(result.current.data?.[0]).toMatchObject({
		id: "srv-1",
		name: "hetzner-01",
	});
	expect(api.unmatched).toEqual([]);
});

test("useServers does not retry a failing list", async () => {
	const api = stubApi({
		"GET /servers": problem(403),
	});
	const client = testQueryClient();
	client.setDefaultOptions({ queries: { retry: 3, retryDelay: 1 } });
	const { result } = renderHook(() => useServers(), {
		wrapper: ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={client}>{children}</QueryClientProvider>
		),
	});
	await waitFor(() => expect(result.current.isError).toBe(true));
	expect(api.calls).toHaveLength(1);
});
