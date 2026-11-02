import { afterEach, beforeEach, expect, jest, test } from "bun:test";
import { QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { testQueryClient } from "#/test/render";
import { makeDeployment, makeService } from "./lib/fixtures";
import { useInspectorPrefetch } from "./use-inspector-prefetch";

const routes = {
	"GET /services/:id/deployments": { body: { items: [] } },
	"GET /services/:id/variables": {
		body: make("VariableList", { items: [] }),
	},
	"GET /deployments/:id": { body: make("DeploymentView") },
	"GET /services/:id/pending-changes": { body: { items: [] } },
	"GET /services/:id/metrics": {
		body: { points: [], retentionDays: 7, stepSeconds: 5 },
	},
};

function setup(service = makeService({ id: "svc-p" })) {
	const client = testQueryClient();
	const wrapper = ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={client}>{children}</QueryClientProvider>
	);
	return renderHook(() => useInspectorPrefetch(service), { wrapper });
}

const paths = (api: ReturnType<typeof stubApi>) =>
	api.calls.map((call) => `${call.method} ${call.path}`).sort();

test("focusing a card prefetches the always-needed inspector requests at once", async () => {
	const api = stubApi(routes);
	const { result } = setup();
	act(() => result.current.onFocus());
	await waitFor(() => expect(api.calls).toHaveLength(2));
	expect(paths(api)).toEqual([
		"GET /services/svc-p/deployments",
		"GET /services/svc-p/variables",
	]);
	expect(api.unmatched).toEqual([]);
});

test("a service with a latest deployment, staged changes and a release prefetches those too", async () => {
	const api = stubApi(routes);
	const { result } = setup(
		makeService({
			id: "svc-p",
			latestDeployment: makeDeployment("succeeded", { id: "dep-9" }),
			hasStagedChanges: true,
			currentDeploymentId: "dep-9",
		}),
	);
	act(() => result.current.onFocus());
	await waitFor(() => expect(api.calls).toHaveLength(5));
	expect(paths(api)).toEqual([
		"GET /deployments/dep-9",
		"GET /services/svc-p/deployments",
		"GET /services/svc-p/metrics",
		"GET /services/svc-p/pending-changes",
		"GET /services/svc-p/variables",
	]);
	const metrics = api.calls.find((call) => call.path.endsWith("/metrics"));
	expect(metrics?.query.get("range")).toBe("1h");
});

test("a pointer that rests on the card prefetches after the intent delay, not before", async () => {
	jest.useFakeTimers();
	try {
		const api = stubApi(routes);
		const { result } = setup();
		act(() => result.current.onPointerEnter());
		await act(async () => {
			jest.advanceTimersByTime(79);
		});
		expect(api.calls).toEqual([]);
		await act(async () => {
			jest.advanceTimersByTime(1);
		});
		jest.useRealTimers();
		await waitFor(() => expect(api.calls).toHaveLength(2));
	} finally {
		jest.useRealTimers();
	}
});

test("passing over a card on the way elsewhere starts nothing", async () => {
	jest.useFakeTimers();
	try {
		const api = stubApi(routes);
		const { result } = setup();
		act(() => result.current.onPointerEnter());
		await act(async () => {
			jest.advanceTimersByTime(40);
		});
		act(() => result.current.onPointerLeave());
		await act(async () => {
			jest.advanceTimersByTime(500);
		});
		expect(api.calls).toEqual([]);
	} finally {
		jest.useRealTimers();
	}
});

test("re-entering restarts the delay: one prefetch, not two", async () => {
	jest.useFakeTimers();
	try {
		const api = stubApi(routes);
		const { result } = setup();
		act(() => result.current.onPointerEnter());
		await act(async () => {
			jest.advanceTimersByTime(50);
		});
		act(() => result.current.onPointerEnter());
		await act(async () => {
			jest.advanceTimersByTime(50);
		});
		expect(api.calls).toEqual([]);
		await act(async () => {
			jest.advanceTimersByTime(30);
		});
		jest.useRealTimers();
		await waitFor(() => expect(api.calls).toHaveLength(2));
	} finally {
		jest.useRealTimers();
	}
});

test("unmounting while the pointer rests cancels the pending prefetch", async () => {
	jest.useFakeTimers();
	try {
		const api = stubApi(routes);
		const { result, unmount } = setup();
		act(() => result.current.onPointerEnter());
		unmount();
		await act(async () => {
			jest.advanceTimersByTime(500);
		});
		expect(api.calls).toEqual([]);
	} finally {
		jest.useRealTimers();
	}
});

test("a hover repeated within ten seconds is served from the cache", async () => {
	const api = stubApi(routes);
	const { result } = setup();
	act(() => result.current.onFocus());
	await waitFor(() => expect(api.calls).toHaveLength(2));
	act(() => result.current.onFocus());
	await act(async () => {
		await Promise.resolve();
	});
	expect(api.calls).toHaveLength(2);
});

afterEach(() => {
	jest.useRealTimers();
});
beforeEach(() => {
	jest.useRealTimers();
});
