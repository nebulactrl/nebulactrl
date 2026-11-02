import { describe, expect, spyOn, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { LogHistoryEntry } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import type { RangeSelection } from "./model";
import { type HistoryScope, useHistoryLogs } from "./use-history-logs";

const entry = (n: number, line = `line ${n}`): LogHistoryEntry => ({
	line,
	replica: 0,
	serviceId: "svc-1",
	stream: "stdout",
	ts: `2026-10-20T14:00:${String(n).padStart(2, "0")}.000Z`,
});

function Probe({
	scope,
	range = { kind: "preset", id: "1h" },
	query = "",
}: {
	scope: HistoryScope;
	range?: RangeSelection;
	query?: string;
}) {
	const h = useHistoryLogs({ scope, range, query });
	return (
		<div>
			<output aria-label="lines">{h.lines.map((l) => l.line).join("|")}</output>
			<output aria-label="loading">{String(h.loading)}</output>
			<output aria-label="fetching">{String(h.fetching)}</output>
			<output aria-label="error">
				{h.error instanceof Error ? h.error.message : ""}
			</output>
			<output aria-label="capturing">{String(h.capturing)}</output>
			<output aria-label="oldest">{h.oldest ?? ""}</output>
			<output aria-label="unavailable">
				{h.unavailable.map((u) => u.serviceName).join("|")}
			</output>
			<output aria-label="queryErrors">{h.queryErrors.join("|")}</output>
			<output aria-label="continuation">
				{JSON.stringify(h.continuation)}
			</output>
			<output aria-label="loadingOlder">{String(h.loadingOlder)}</output>
			<button type="button" onClick={h.loadOlder}>
				older
			</button>
			<button type="button" onClick={h.refresh}>
				refresh
			</button>
		</div>
	);
}

const out = (name: string) => screen.getByLabelText(name).textContent ?? "";
const lines = () => {
	const text = out("lines");
	return text === "" ? [] : text.split("|");
};
const service: HistoryScope = { kind: "service", id: "svc-1" };

describe("useHistoryLogs", () => {
	test("a service's newest page is asked for the last hour, 500 lines, and shown oldest first", async () => {
		const api = stubApi({
			"GET /services/:id/logs/history": {
				body: {
					capturing: true,
					items: [entry(3), entry(2), entry(1)],
					oldest: "2026-10-01T00:00:00.000Z",
				},
			},
		});
		await renderWithProviders(<Probe scope={service} />);
		expect(out("loading")).toBe("true");
		await waitFor(() =>
			expect(lines()).toEqual(["line 1", "line 2", "line 3"]),
		);
		expect(out("loading")).toBe("false");
		expect(out("capturing")).toBe("true");
		expect(out("oldest")).toBe("2026-10-01T00:00:00.000Z");
		expect(api.calls).toHaveLength(1);
		const call = api.calls[0];
		expect(call?.path).toBe("/services/svc-1/logs/history");
		expect(call?.query.get("limit")).toBe("500");
		expect(call?.query.has("q")).toBe(false);
		expect(call?.query.has("cursor")).toBe(false);
		expect(
			Date.parse(call?.query.get("to") ?? "") -
				Date.parse(call?.query.get("from") ?? ""),
		).toBe(60 * 60_000);
		expect(api.unmatched).toEqual([]);
	});

	test("sends the trimmed search, the component and the process", async () => {
		const api = stubApi({
			"GET /services/:id/logs/history": {
				body: { capturing: true, items: [] },
			},
		});
		await renderWithProviders(
			<Probe
				scope={{ ...service, component: "db", process: "worker" }}
				query="  level:error  "
			/>,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		const query = api.calls[0]?.query;
		expect(query?.get("q")).toBe("level:error");
		expect(query?.get("component")).toBe("db");
		expect(query?.get("process")).toBe("worker");
	});

	test("an environment's history uses its own endpoint and carries no component", async () => {
		const api = stubApi({
			"GET /environments/:id/logs/history": {
				body: {
					capturing: true,
					items: [entry(1)],
					unavailable: [
						{
							message: "offline",
							reason: "server_offline",
							serverId: "srv-1",
							serverName: "hetzner-01",
							serviceId: "svc-2",
							serviceName: "worker",
						},
					],
					queryErrors: ["unknown field foo"],
				},
			},
		});
		await renderWithProviders(
			<Probe scope={{ kind: "environment", id: "env-1", component: "db" }} />,
		);
		await waitFor(() => expect(lines()).toEqual(["line 1"]));
		expect(api.calls[0]?.path).toBe("/environments/env-1/logs/history");
		expect(api.calls[0]?.query.has("component")).toBe(false);
		expect(out("unavailable")).toBe("worker");
		expect(out("queryErrors")).toBe("unknown field foo");
	});

	test("a custom range sends its own instants; an open end is now", async () => {
		const api = stubApi({
			"GET /services/:id/logs/history": {
				body: { capturing: true, items: [] },
			},
		});
		await renderWithProviders(
			<Probe
				scope={service}
				range={{
					kind: "custom",
					from: "2026-10-20T10:00:00.000Z",
					to: undefined,
				}}
			/>,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(api.calls[0]?.query.get("from")).toBe("2026-10-20T10:00:00.000Z");
		expect(api.calls[0]?.query.get("to")).toBeTruthy();
	});

	test("with older lines on the server it offers them and Load older adds the next page behind the first", async () => {
		const api = stubApi({
			"GET /services/:id/logs/history": (request) => {
				const cursor = new URL(request.url).searchParams.get("cursor");
				return Response.json(
					cursor === null
						? {
								capturing: true,
								items: [entry(4), entry(3)],
								nextCursor: "c1",
							}
						: { capturing: true, items: [entry(2), entry(1)] },
				);
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Probe scope={service} />);
		await waitFor(() => expect(lines()).toEqual(["line 3", "line 4"]));
		expect(JSON.parse(out("continuation"))).toEqual({
			kind: "more",
			cursor: "c1",
		});
		await user.click(screen.getByRole("button", { name: "older" }));
		await waitFor(() =>
			expect(lines()).toEqual(["line 1", "line 2", "line 3", "line 4"]),
		);
		expect(api.calls.map((c) => c.query.get("cursor"))).toEqual([null, "c1"]);
		expect(JSON.parse(out("continuation"))).toEqual({ kind: "end" });
	});

	test("a partial scan continues with its cursor", async () => {
		stubApi({
			"GET /services/:id/logs/history": {
				body: { capturing: true, items: [], partial: true, nextCursor: "c9" },
			},
		});
		await renderWithProviders(<Probe scope={service} />);
		await waitFor(() =>
			expect(JSON.parse(out("continuation"))).toEqual({
				kind: "partial",
				cursor: "c9",
			}),
		);
	});

	test("a server that is offline is an error with the server's message, not a retry loop", async () => {
		const api = stubApi({
			"GET /services/:id/logs/history": {
				status: 503,
				body: {
					status: 503,
					title: "Service Unavailable",
					detail: "server is offline",
					code: "server_offline",
				},
			},
		});
		await renderWithProviders(<Probe scope={service} />);
		await waitFor(() => expect(out("error")).toContain("server is offline"));
		expect(out("loading")).toBe("false");
		expect(lines()).toEqual([]);
		expect(api.calls).toHaveLength(1);
	});

	test("Refresh on a range that reaches now asks again from a new now", async () => {
		const start = Date.parse("2026-10-20T14:00:00.000Z");
		let now = start;
		const clock = spyOn(Date, "now").mockImplementation(() => now);
		try {
			const api = stubApi({
				"GET /services/:id/logs/history": {
					body: { capturing: true, items: [entry(1)] },
				},
			});
			const user = userEvent.setup();
			await renderWithProviders(<Probe scope={service} />);
			await waitFor(() => expect(lines()).toEqual(["line 1"]));
			expect(api.calls[0]?.query.get("to")).toBe(new Date(start).toISOString());
			now = start + 5 * 60_000;
			await user.click(screen.getByRole("button", { name: "refresh" }));
			await waitFor(() => expect(api.calls).toHaveLength(2));
			expect(api.calls[1]?.query.get("to")).toBe(new Date(now).toISOString());
			expect(api.calls[1]?.query.get("from")).toBe(
				new Date(now - 60 * 60_000).toISOString(),
			);
		} finally {
			clock.mockRestore();
		}
	});

	test("Refresh on a closed custom range asks for the same range again", async () => {
		const api = stubApi({
			"GET /services/:id/logs/history": {
				body: { capturing: true, items: [entry(1)] },
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<Probe
				scope={service}
				range={{
					kind: "custom",
					from: "2026-10-20T10:00:00.000Z",
					to: "2026-10-20T12:00:00.000Z",
				}}
			/>,
		);
		await waitFor(() => expect(lines()).toEqual(["line 1"]));
		await user.click(screen.getByRole("button", { name: "refresh" }));
		await waitFor(() => expect(api.calls).toHaveLength(2));
		expect(api.calls[1]?.query.toString()).toBe(api.calls[0]?.query.toString());
	});

	test("capture off is reported", async () => {
		stubApi({
			"GET /services/:id/logs/history": {
				body: { capturing: false, items: null },
			},
		});
		await renderWithProviders(<Probe scope={service} />);
		await waitFor(() => expect(out("capturing")).toBe("false"));
		expect(lines()).toEqual([]);
	});
});
