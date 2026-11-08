import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { RangeSelection } from "#/features/logs/history/model";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import type { LogSource } from "./sources";
import { LIVE_FACETS_REFRESH_MS, useLogFacets } from "./use-log-facets";

const facetsBody = (matched: number) => ({
	matched,
	scanned: matched,
	partial: false,
	facets: [
		{
			key: "level",
			count: matched,
			values: [{ value: "error", count: matched }],
		},
	],
	attrs: [],
	histogram: [],
	capturing: true,
	requestsCapturing: true,
});

function Probe({
	range = { kind: "preset", id: "1h" },
	query = "",
	sources = ["runtime", "requests"],
	buckets,
	live,
	paused,
	enabled,
}: {
	range?: RangeSelection;
	query?: string;
	sources?: readonly LogSource[];
	buckets?: number;
	live?: boolean;
	paused?: boolean;
	enabled?: boolean;
}) {
	const facets = useLogFacets({
		environmentId: "env-1",
		range,
		query,
		sources,
		buckets,
		live,
		paused,
		enabled,
	});
	return (
		<div>
			<output aria-label="matched">{facets.data?.matched ?? "none"}</output>
			<output aria-label="fetching">{String(facets.isFetching)}</output>
			<output aria-label="loading">{String(facets.isLoading)}</output>
			<output aria-label="error">
				{facets.error instanceof Error ? facets.error.message : ""}
			</output>
			<button type="button" onClick={facets.refresh}>
				refresh
			</button>
		</div>
	);
}

function Search() {
	const [query, setQuery] = useState("");
	return (
		<>
			<Probe query={query} />
			<button type="button" onClick={() => setQuery("level:error")}>
				search
			</button>
		</>
	);
}

function Toggled() {
	const [enabled, setEnabled] = useState(false);
	return (
		<>
			<Probe enabled={enabled} />
			<button type="button" onClick={() => setEnabled(true)}>
				open history
			</button>
		</>
	);
}

const pause = (ms: number) => {
	const { promise, resolve } = Promise.withResolvers<void>();
	setTimeout(resolve, ms);
	return promise;
};

const out = (name: string) => screen.getByLabelText(name).textContent ?? "";

describe("useLogFacets", () => {
	test("history asks for the resolved range, the trimmed query, the sources and the bucket count", async () => {
		const api = stubApi({
			"GET /environments/:id/logs/facets": { body: facetsBody(7) },
		});
		await renderWithProviders(<Probe query="  level:error  " buckets={30} />);
		await waitFor(() => expect(out("matched")).toBe("7"));
		expect(api.calls).toHaveLength(1);
		const call = api.calls[0];
		expect(call?.path).toBe("/environments/env-1/logs/facets");
		expect(call?.query.get("q")).toBe("level:error");
		expect(call?.query.get("sources")).toBe("runtime,requests");
		expect(call?.query.get("buckets")).toBe("30");
		expect(
			Date.parse(call?.query.get("to") ?? "") -
				Date.parse(call?.query.get("from") ?? ""),
		).toBe(60 * 60_000);
		expect(api.unmatched).toEqual([]);
	});

	test("a custom range sends its own instants and no query when there is none", async () => {
		const api = stubApi({
			"GET /environments/:id/logs/facets": { body: facetsBody(1) },
		});
		await renderWithProviders(
			<Probe
				range={{
					kind: "custom",
					from: "2026-10-20T10:00:00.000Z",
					to: "2026-10-20T12:00:00.000Z",
				}}
				sources={["requests"]}
			/>,
		);
		await waitFor(() => expect(out("matched")).toBe("1"));
		const query = api.calls[0]?.query;
		expect(query?.get("from")).toBe("2026-10-20T10:00:00.000Z");
		expect(query?.get("to")).toBe("2026-10-20T12:00:00.000Z");
		expect(query?.has("q")).toBe(false);
		expect(query?.has("buckets")).toBe(false);
		expect(query?.get("sources")).toBe("requests");
	});

	test("live names no range, so the server's last 15 minutes roll, and asks again every 10 s", async () => {
		const api = stubApi({
			"GET /environments/:id/logs/facets": { body: facetsBody(3) },
		});
		const { queryClient } = await renderWithProviders(<Probe live />);
		await waitFor(() => expect(out("matched")).toBe("3"));
		const query = api.calls[0]?.query;
		expect(query?.has("from")).toBe(false);
		expect(query?.has("to")).toBe(false);
		const observer = queryClient.getQueryCache().getAll()[0]?.observers[0];
		expect(observer?.options.refetchInterval).toBe(LIVE_FACETS_REFRESH_MS);
		expect(LIVE_FACETS_REFRESH_MS).toBe(10_000);
	});

	test("a pinned range is not asked for again by itself", async () => {
		stubApi({
			"GET /environments/:id/logs/facets": { body: facetsBody(3) },
		});
		const { queryClient } = await renderWithProviders(<Probe />);
		await waitFor(() => expect(out("matched")).toBe("3"));
		const observer = queryClient.getQueryCache().getAll()[0]?.observers[0];
		expect(observer?.options.refetchInterval).toBe(false);
	});

	test("disabled asks for nothing", async () => {
		const api = stubApi({
			"GET /environments/:id/logs/facets": { body: facetsBody(3) },
		});
		await renderWithProviders(<Probe enabled={false} />);
		const { promise, resolve } = Promise.withResolvers<void>();
		setTimeout(resolve, 20);
		await promise;
		expect(api.calls).toHaveLength(0);
		expect(out("matched")).toBe("none");
	});

	test("a new query keeps the previous counts up while it loads", async () => {
		const gate = Promise.withResolvers<void>();
		stubApi({
			"GET /environments/:id/logs/facets": async (request) => {
				if (new URL(request.url).searchParams.has("q")) {
					await gate.promise;
					return Response.json(facetsBody(2));
				}
				return Response.json(facetsBody(5));
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Search />);
		await waitFor(() => expect(out("matched")).toBe("5"));
		await user.click(screen.getByRole("button", { name: "search" }));
		await waitFor(() => expect(out("fetching")).toBe("true"));
		expect(out("matched")).toBe("5");
		gate.resolve();
		await waitFor(() => expect(out("matched")).toBe("2"));
	});

	test("an error is reported and not retried", async () => {
		const api = stubApi({
			"GET /environments/:id/logs/facets": () =>
				Response.json(
					{
						status: 500,
						title: "Internal Server Error",
						detail: "the scan failed",
					},
					{ status: 500 },
				),
		});
		await renderWithProviders(<Probe />);
		await waitFor(() => expect(out("error")).toContain("the scan failed"));
		expect(api.calls).toHaveLength(1);
	});
});

describe("useLogFacets live and refresh", () => {
	const countsCalls = (api: { calls: { path: string }[] }) =>
		api.calls.filter((c) => c.path.endsWith("/logs/facets")).length;

	test("a paused live view stops asking on the timer", async () => {
		stubApi({
			"GET /environments/:id/logs/facets": { body: facetsBody(3) },
		});
		const { queryClient } = await renderWithProviders(<Probe live paused />);
		await waitFor(() => expect(out("matched")).toBe("3"));
		const options = queryClient.getQueryCache().getAll()[0]
			?.observers[0]?.options;
		expect(options?.refetchInterval).toBe(false);
		expect(options?.refetchOnWindowFocus).toBe(false);
	});

	test("a history that was waiting behind the live view asks from the moment it opens", async () => {
		const api = stubApi({
			"GET /environments/:id/logs/facets": { body: facetsBody(3) },
		});
		const user = userEvent.setup();
		const mounted = Date.now();
		await renderWithProviders(<Toggled />);
		await pause(30);
		await user.click(screen.getByRole("button", { name: "open history" }));
		await waitFor(() => expect(countsCalls(api)).toBe(1));
		const to = Date.parse(api.calls[0]?.query.get("to") ?? "");
		expect(to).toBeGreaterThanOrEqual(mounted + 30);
	});

	test("refresh asks a range that reaches now from a new now", async () => {
		const api = stubApi({
			"GET /environments/:id/logs/facets": { body: facetsBody(3) },
		});
		const user = userEvent.setup();
		await renderWithProviders(<Probe />);
		await waitFor(() => expect(out("matched")).toBe("3"));
		await pause(10);
		await user.click(screen.getByRole("button", { name: "refresh" }));
		await waitFor(() => expect(countsCalls(api)).toBe(2));
		const [first, second] = api.calls.map((c) =>
			Date.parse(c.query.get("to") ?? ""),
		);
		expect(second).toBeGreaterThan(first as number);
	});

	test("refresh after an error asks again", async () => {
		let calls = 0;
		stubApi({
			"GET /environments/:id/logs/facets": () => {
				calls += 1;
				return calls === 1
					? Response.json(
							{ status: 500, title: "Internal Server Error", detail: "boom" },
							{ status: 500 },
						)
					: Response.json(facetsBody(4));
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Probe />);
		await waitFor(() => expect(out("error")).toContain("boom"));
		await user.click(screen.getByRole("button", { name: "refresh" }));
		await waitFor(() => expect(out("matched")).toBe("4"));
		expect(out("error")).toBe("");
	});

	test("isLoading is true until the first counts arrive", async () => {
		const gate = Promise.withResolvers<void>();
		stubApi({
			"GET /environments/:id/logs/facets": async () => {
				await gate.promise;
				return Response.json(facetsBody(2));
			},
		});
		await renderWithProviders(<Probe />);
		await waitFor(() => expect(out("loading")).toBe("true"));
		gate.resolve();
		await waitFor(() => expect(out("loading")).toBe("false"));
	});
});
