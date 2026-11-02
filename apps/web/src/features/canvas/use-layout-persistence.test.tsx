import { afterEach, beforeEach, expect, jest, spyOn, test } from "bun:test";
import { QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { canvasQuery } from "#/api/hooks";
import type { CanvasDocument, CanvasLayout } from "#/api/models";
import { toast } from "#/components/ui/toast";
import { stubApi } from "#/test/api";
import { testQueryClient } from "#/test/render";
import { makeService } from "./lib/fixtures";
import {
	placeNode,
	registerPlacer,
	useLayoutPersistence,
} from "./use-layout-persistence";

const CANVAS = "/projects/p1/environments/e1/canvas";
const CANVAS_ROUTE = "/projects/:p/environments/:e/canvas";

function layout(x: number, extra: Partial<CanvasLayout> = {}): CanvasLayout {
	return { nodes: { a: { x, y: 0 } }, groups: [], viewport: null, ...extra };
}

/** The server's reply to a canvas read or write. */
function canvas(
	version: number,
	nodes: Record<string, { x: number; y: number }> = {},
) {
	return {
		version,
		layout: { nodes, groups: [], viewport: { x: 0, y: 0, zoom: 1 } },
	};
}

function conflict() {
	return Response.json(
		{ status: 412, title: "Precondition Failed", detail: "stale layout" },
		{ status: 412 },
	);
}

afterEach(() => {
	jest.useRealTimers();
});
beforeEach(() => {
	jest.useRealTimers();
});

interface Harness {
	client: ReturnType<typeof testQueryClient>;
	adopted: CanvasLayout[];
	hook: ReturnType<
		typeof renderHook<ReturnType<typeof useLayoutPersistence>, unknown>
	>;
}

function setup(initialVersion = 3): Harness {
	const client = testQueryClient();
	const adopted: CanvasLayout[] = [];
	const hook = renderHook(
		() =>
			useLayoutPersistence({
				projectId: "p1",
				environmentId: "e1",
				initialVersion,
				onAdopt: (next) => adopted.push(next),
			}),
		{
			wrapper: ({ children }: { children: ReactNode }) => (
				<QueryClientProvider client={client}>{children}</QueryClientProvider>
			),
		},
	);
	return { client, adopted, hook };
}

/** PUT replies are scripted; every request's If-Match and body is recorded. */
function canvasApi(
	puts: Array<(request: Request) => Response>,
	latest: number[] = [],
) {
	const ifMatch: Array<string | null> = [];
	const bodies: unknown[] = [];
	let putIndex = 0;
	let getIndex = 0;
	const api = stubApi({
		[`PUT ${CANVAS_ROUTE}`]: async (request) => {
			ifMatch.push(request.headers.get("If-Match"));
			bodies.push(await request.clone().json());
			const reply = puts[Math.min(putIndex, puts.length - 1)];
			putIndex += 1;
			if (!reply) throw new Error("no scripted reply");
			return reply(request);
		},
		[`GET ${CANVAS_ROUTE}`]: () => {
			const version = latest[Math.min(getIndex, latest.length - 1)] ?? 0;
			getIndex += 1;
			return Response.json(canvas(version, { fresh: { x: version, y: 1 } }));
		},
	});
	return { api, ifMatch, bodies };
}

test("a scheduled layout is written once the drag has rested for 500ms, with If-Match", async () => {
	const { ifMatch, bodies, api } = canvasApi([() => Response.json(canvas(4))]);
	const { hook } = setup(3);
	jest.useFakeTimers();
	act(() => hook.result.current.schedule(layout(1)));
	await act(async () => {
		jest.advanceTimersByTime(499);
	});
	expect(api.calls).toEqual([]);
	await act(async () => {
		jest.advanceTimersByTime(1);
	});
	jest.useRealTimers();
	await waitFor(() => expect(api.calls).toHaveLength(1));
	expect(api.calls[0]).toMatchObject({ method: "PUT", path: CANVAS });
	expect(ifMatch).toEqual(['"3"']);
	expect(bodies).toEqual([
		{
			layout: {
				nodes: { a: { x: 1, y: 0 } },
				groups: [],
				// A layout without a saved view is written with the identity view.
				viewport: { x: 0, y: 0, zoom: 1 },
			},
		},
	]);
});

test("scheduling again replaces the queued layout and restarts the delay", async () => {
	const { bodies, api } = canvasApi([() => Response.json(canvas(4))]);
	const { hook } = setup();
	jest.useFakeTimers();
	act(() => hook.result.current.schedule(layout(1)));
	await act(async () => {
		jest.advanceTimersByTime(400);
	});
	act(() => hook.result.current.schedule(layout(2)));
	await act(async () => {
		jest.advanceTimersByTime(400);
	});
	expect(api.calls).toEqual([]);
	await act(async () => {
		jest.advanceTimersByTime(100);
	});
	jest.useRealTimers();
	await waitFor(() => expect(api.calls).toHaveLength(1));
	expect((bodies[0] as { layout: CanvasLayout }).layout.nodes).toEqual({
		a: { x: 2, y: 0 },
	});
});

test("a saved layout updates the cache and the next write uses its version", async () => {
	const { ifMatch } = canvasApi([
		() => Response.json(canvas(4, { a: { x: 1, y: 0 } })),
		() => Response.json(canvas(5)),
	]);
	const { hook, client } = setup(3);
	act(() => hook.result.current.schedule(layout(1)));
	await act(() => hook.result.current.flush());
	const cached = client.getQueryData<CanvasDocument>(
		canvasQuery("p1", "e1").queryKey,
	);
	expect(cached?.version).toBe(4);
	expect(cached?.layout.nodes).toEqual({ a: { x: 1, y: 0 } });
	act(() => hook.result.current.schedule(layout(2)));
	await act(() => hook.result.current.flush());
	expect(ifMatch).toEqual(['"3"', '"4"']);
});

test("flush with nothing queued sends nothing", async () => {
	const { api } = canvasApi([() => Response.json(canvas(4))]);
	const { hook } = setup();
	await act(() => hook.result.current.flush());
	expect(api.calls).toEqual([]);
});

test("flush writes the queued layout now, without waiting for the delay", async () => {
	const { api } = canvasApi([() => Response.json(canvas(4))]);
	const { hook } = setup();
	act(() => hook.result.current.schedule(layout(1)));
	await act(() => hook.result.current.flush());
	expect(api.calls.map((c) => c.method)).toEqual(["PUT"]);
});

test("a stale version (412) refetches and writes the same layout on top of the newest version", async () => {
	const { ifMatch, bodies, api } = canvasApi(
		[conflict, () => Response.json(canvas(10))],
		[9],
	);
	const { hook, adopted, client } = setup(3);
	act(() => hook.result.current.schedule(layout(7)));
	await act(() => hook.result.current.flush());
	expect(api.calls.map((c) => c.method)).toEqual(["PUT", "GET", "PUT"]);
	expect(ifMatch).toEqual(['"3"', '"9"']);
	expect(bodies[1]).toEqual(bodies[0]);
	expect(adopted).toEqual([]);
	expect(
		client.getQueryData<CanvasDocument>(canvasQuery("p1", "e1").queryKey)
			?.version,
	).toBe(10);
});

test("a second conflict gives up: the server's layout is cached and adopted, ours is not forced", async () => {
	const { ifMatch, api } = canvasApi([conflict, conflict], [9, 11]);
	const { hook, adopted, client } = setup(3);
	act(() => hook.result.current.schedule(layout(7)));
	await act(() => hook.result.current.flush());
	expect(api.calls.map((c) => c.method)).toEqual(["PUT", "GET", "PUT", "GET"]);
	expect(ifMatch).toEqual(['"3"', '"9"']);
	expect(adopted).toEqual([
		{
			nodes: { fresh: { x: 11, y: 1 } },
			groups: [],
			viewport: { x: 0, y: 0, zoom: 1 },
		},
	]);
	expect(
		client.getQueryData<CanvasDocument>(canvasQuery("p1", "e1").queryKey)
			?.version,
	).toBe(11);
});

test("after giving up, the next write carries the adopted version", async () => {
	const { ifMatch } = canvasApi(
		[conflict, conflict, () => Response.json(canvas(12))],
		[9, 11],
	);
	const { hook } = setup(3);
	act(() => hook.result.current.schedule(layout(7)));
	await act(() => hook.result.current.flush());
	act(() => hook.result.current.schedule(layout(8)));
	await act(() => hook.result.current.flush());
	expect(ifMatch.at(-1)).toBe('"11"');
});

test("a failed save toasts the reason, once per minute", async () => {
	canvasApi([
		() =>
			Response.json(
				{ status: 500, title: "Internal Server Error", detail: "disk full" },
				{ status: 500 },
			),
	]);
	const error = spyOn(toast, "error").mockImplementation(() => "");
	try {
		const { hook } = setup();
		act(() => hook.result.current.schedule(layout(1)));
		await act(() => hook.result.current.flush());
		expect(error).toHaveBeenCalledTimes(1);
		expect(error.mock.calls[0]?.[0]).toBe("Couldn't save the canvas layout");
		expect(error.mock.calls[0]?.[1]).toBe("disk full");
		// A second failure within the minute stays quiet.
		act(() => hook.result.current.schedule(layout(2)));
		await act(() => hook.result.current.flush());
		expect(error).toHaveBeenCalledTimes(1);
	} finally {
		error.mockRestore();
	}
});

test("unmounting writes what is still queued", async () => {
	const { api, bodies } = canvasApi([() => Response.json(canvas(4))]);
	const { hook } = setup();
	act(() => hook.result.current.schedule(layout(5)));
	hook.unmount();
	await waitFor(() => expect(api.calls).toHaveLength(1));
	expect((bodies[0] as { layout: CanvasLayout }).layout.nodes.a?.x).toBe(5);
});

test("adopt drops the queued write, takes the saved version and hands the layout to the canvas", async () => {
	const { api, ifMatch } = canvasApi([() => Response.json(canvas(21))]);
	const { hook, adopted, client } = setup(3);
	const document: CanvasDocument = { version: 20, layout: layout(9) };
	act(() => hook.result.current.schedule(layout(1)));
	act(() => hook.result.current.adopt(document));
	expect(adopted).toEqual([document.layout]);
	expect(
		client.getQueryData<CanvasDocument>(canvasQuery("p1", "e1").queryKey),
	).toEqual(document);
	await act(() => hook.result.current.flush());
	expect(api.calls).toEqual([]);
	act(() => hook.result.current.schedule(layout(2)));
	await act(() => hook.result.current.flush());
	expect(ifMatch).toEqual(['"20"']);
});

test("the callbacks stay the same across renders", () => {
	canvasApi([() => Response.json(canvas(4))]);
	const { hook } = setup();
	const first = hook.result.current;
	hook.rerender();
	expect(hook.result.current).toBe(first);
});

// --- placing a card from outside the canvas ------------------------------

test("a mounted canvas takes a placement itself, with no request", async () => {
	const { api } = canvasApi([() => Response.json(canvas(4))]);
	const placed: Array<[string, { x: number; y: number }]> = [];
	const release = registerPlacer((id, at) => placed.push([id, at]));
	await placeNode("svc-1", { x: 10, y: 20 });
	expect(placed).toEqual([["svc-1", { x: 10, y: 20 }]]);
	expect(api.calls).toEqual([]);
	release();
});

test("an older canvas letting go does not unregister the newer one", async () => {
	canvasApi([() => Response.json(canvas(4))]);
	const first: string[] = [];
	const second: string[] = [];
	const releaseFirst = registerPlacer((id) => first.push(id));
	const releaseSecond = registerPlacer((id) => second.push(id));
	releaseFirst();
	await placeNode("svc-1", { x: 0, y: 0 });
	expect(second).toEqual(["svc-1"]);
	releaseSecond();
});

function placementApi(puts: Array<() => Response>, versions: number[]) {
	const ifMatch: Array<string | null> = [];
	const bodies: unknown[] = [];
	let putIndex = 0;
	let getIndex = 0;
	const api = stubApi({
		"GET /services/:id": {
			body: makeService({ id: "svc-1", projectId: "p1", environmentId: "e1" }),
		},
		[`GET ${CANVAS_ROUTE}`]: () => {
			const version = versions[Math.min(getIndex, versions.length - 1)] ?? 0;
			getIndex += 1;
			return Response.json(canvas(version, { old: { x: 1, y: 1 } }));
		},
		[`PUT ${CANVAS_ROUTE}`]: async (request) => {
			ifMatch.push(request.headers.get("If-Match"));
			bodies.push(await request.clone().json());
			const reply = puts[Math.min(putIndex, puts.length - 1)];
			putIndex += 1;
			return reply ? reply() : Response.error();
		},
	});
	return { api, ifMatch, bodies };
}

test("without a canvas the saved layout of the service's environment gets the card", async () => {
	const { api, ifMatch, bodies } = placementApi(
		[() => Response.json(canvas(5))],
		[4],
	);
	await placeNode("svc-1", { x: 30, y: 40 });
	expect(api.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
		"GET /services/svc-1",
		`GET ${CANVAS}`,
		`PUT ${CANVAS}`,
	]);
	expect(ifMatch).toEqual(['"4"']);
	expect(bodies).toEqual([
		{
			layout: {
				nodes: { old: { x: 1, y: 1 }, "svc-1": { x: 30, y: 40 } },
				groups: [],
				viewport: { x: 0, y: 0, zoom: 1 },
			},
		},
	]);
});

test("a stale version while placing is retried once against the newest layout", async () => {
	const { ifMatch } = placementApi(
		[conflict, () => Response.json(canvas(8))],
		[4, 7],
	);
	await placeNode("svc-1", { x: 1, y: 2 });
	expect(ifMatch).toEqual(['"4"', '"7"']);
});

test("a second stale version while placing is the caller's error", async () => {
	const { ifMatch } = placementApi([conflict, conflict], [4, 7]);
	await expect(placeNode("svc-1", { x: 1, y: 2 })).rejects.toMatchObject({
		status: 412,
	});
	expect(ifMatch).toHaveLength(2);
});

test("another failure while placing is rethrown without a retry", async () => {
	const { ifMatch } = placementApi(
		[() => Response.json({ status: 500, title: "boom" }, { status: 500 })],
		[4],
	);
	await expect(placeNode("svc-1", { x: 1, y: 2 })).rejects.toMatchObject({
		status: 500,
	});
	expect(ifMatch).toHaveLength(1);
});
