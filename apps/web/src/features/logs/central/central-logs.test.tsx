import { afterEach, beforeEach, describe, expect, spyOn, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Service } from "#/api/models";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { stubLayout } from "../test-layout";
import { CentralLogs } from "./central-logs";

const service = { id: "svc-1", name: "api" } as Service;

let restoreLayout: () => void;
beforeEach(() => {
	restoreLayout = stubLayout();
});
afterEach(() => restoreLayout());

const entry = (n: number, server = "hetzner-01", line = `line ${n}`) => ({
	line,
	server,
	stream: "stdout",
	ts: `2026-10-20T14:02:${String(n).padStart(2, "0")}.000Z`,
});

const page = (lines: ReturnType<typeof entry>[]) => ({
	body: { enabled: true, hint: "", lines },
});

const toggle = <div>mode toggle</div>;

async function show(props: Partial<Parameters<typeof CentralLogs>[0]> = {}) {
	return renderWithProviders(
		<CentralLogs service={service} modeToggle={toggle} {...props} />,
	);
}

const allRows = () => {
	const log = screen.getByRole("log", { name: "api central logs" });
	return [...log.querySelectorAll("span.whitespace-pre")].map(
		(el) => el.textContent,
	);
};
/** The newest `n` rows, oldest first. */
const newest = (n: number) => allRows().slice(-n);

describe("CentralLogs", () => {
	test("shows a loading state first, with the mode toggle on its own row", async () => {
		stubApi({
			"GET /services/:id/logs/central": () => new Promise<Response>(() => {}),
		});
		await show();
		expect(screen.getByText("mode toggle")).toBeInTheDocument();
		// The loader waits 300 ms before it shows anything.
		expect(await screen.findByText("Loading central logs")).toBeInTheDocument();
	});

	test("asks for the last hour of the service, 1000 lines, and draws the newest at the bottom", async () => {
		const api = stubApi({
			// The API answers newest first.
			"GET /services/:id/logs/central": page([entry(3), entry(2), entry(1)]),
		});
		await show();
		await waitFor(() => expect(newest(2)).toEqual(["line 2", "line 3"]));
		expect(api.calls).toHaveLength(1);
		const call = api.calls[0];
		expect(call?.path).toBe("/services/svc-1/logs/central");
		expect(call?.query.get("limit")).toBe("1000");
		const from = call?.query.get("from") ?? "";
		const to = call?.query.get("to") ?? "";
		expect(Date.parse(to) - Date.parse(from)).toBe(60 * 60_000);
		expect(call?.query.has("process")).toBe(false);
		expect(api.unmatched).toEqual([]);
	});

	test("names the servers the lines came from", async () => {
		stubApi({
			"GET /services/:id/logs/central": page([
				entry(2, "hetzner-02"),
				entry(1, "hetzner-01"),
			]),
		});
		await show();
		expect(
			await screen.findByText("From servers hetzner-02, hetzner-01."),
		).toBeInTheDocument();
	});

	test("narrows to one process when given", async () => {
		const api = stubApi({
			"GET /services/:id/logs/central": page([entry(2), entry(1)]),
		});
		await show({ process: "worker", chips: <div>process chips</div> });
		await screen.findByRole("log", { name: "api central logs" });
		expect(api.calls[0]?.query.get("process")).toBe("worker");
		expect(screen.getByText("process chips")).toBeInTheDocument();
	});

	test("says so when the newest 1000 lines fill the window", async () => {
		stubApi({
			"GET /services/:id/logs/central": page(
				Array.from({ length: 1000 }, (_, i) =>
					entry(i % 60, "hetzner-01", `l${i}`),
				),
			),
		});
		await show();
		expect(
			await screen.findByText(/Showing the newest 1000 lines of the range/),
		).toBeInTheDocument();
	});

	test("an empty range says so", async () => {
		stubApi({ "GET /services/:id/logs/central": page([]) });
		await show();
		expect(
			await screen.findByText("No lines in this range."),
		).toBeInTheDocument();
		expect(
			screen.getByText("Lines from every server this service runs on."),
		).toBeInTheDocument();
	});

	test("while central logs are off it shows the hint and links to Log forwarding", async () => {
		stubApi({
			"GET /services/:id/logs/central": {
				body: make("CentralLogPage", {
					enabled: false,
					hint: "Turn on forwarding first.",
					lines: null,
				}),
			},
		});
		await show();
		const status = await screen.findByRole("status");
		expect(
			within(status).getByText("Turn on forwarding first."),
		).toBeInTheDocument();
		expect(
			within(status).getByText("Open Log forwarding").closest("a"),
		).toHaveAttribute("href", "/settings/log-forwarding");
		expect(screen.getByText("mode toggle")).toBeInTheDocument();
		expect(screen.queryByRole("log")).not.toBeInTheDocument();
	});

	test("off without a hint falls back to the standard sentence", async () => {
		stubApi({
			"GET /services/:id/logs/central": {
				body: make("CentralLogPage", { enabled: false, hint: "", lines: null }),
			},
		});
		await show();
		expect(
			await screen.findByText(/Turn on Log forwarding in Team settings/),
		).toBeInTheDocument();
	});

	test("a 404 means an older control plane, not a missing service", async () => {
		stubApi({
			"GET /services/:id/logs/central": problem(404, {
				status: 404,
				title: "Not Found",
				detail: "route not found",
			}),
		});
		await show();
		expect(
			await screen.findByText("Central logs did not load"),
		).toBeInTheDocument();
		expect(
			screen.getByText("Central logs aren't available on this control plane."),
		).toBeInTheDocument();
		expect(screen.queryByText("route not found")).not.toBeInTheDocument();
	});

	test("another failure shows its message and Try again asks again", async () => {
		let attempt = 0;
		const api = stubApi({
			"GET /services/:id/logs/central": () => {
				attempt += 1;
				return attempt === 1
					? Response.json(
							{ status: 500, title: "Error", detail: "log store is down" },
							{ status: 500 },
						)
					: Response.json({
							enabled: true,
							hint: "",
							lines: [entry(2), entry(1)],
						});
			},
		});
		const user = userEvent.setup();
		await show();
		expect(await screen.findByText("log store is down")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Try again" }));
		await waitFor(() => expect(newest(1)).toEqual(["line 2"]));
		expect(api.calls.length).toBeGreaterThanOrEqual(2);
	});

	test("Refresh asks the log store again from a new now", async () => {
		const realNow = Date.now();
		let now = realNow;
		const clock = spyOn(Date, "now").mockImplementation(() => now);
		try {
			const api = stubApi({
				"GET /services/:id/logs/central": page([entry(2), entry(1)]),
			});
			const user = userEvent.setup();
			await show();
			await waitFor(() => expect(newest(1)).toEqual(["line 2"]));
			expect(api.calls[0]?.query.get("to")).toBe(
				new Date(realNow).toISOString(),
			);
			now = realNow + 10 * 60_000;
			await user.click(screen.getByRole("button", { name: "Refresh" }));
			await waitFor(() =>
				expect(api.calls.at(-1)?.query.get("to")).toBe(
					new Date(now).toISOString(),
				),
			);
			expect(api.calls.at(-1)?.query.get("from")).toBe(
				new Date(now - 60 * 60_000).toISOString(),
			);
		} finally {
			clock.mockRestore();
		}
	});

	test("picking another range asks for it", async () => {
		const api = stubApi({
			"GET /services/:id/logs/central": page([entry(2), entry(1)]),
		});
		const user = userEvent.setup();
		await show();
		await waitFor(() => expect(newest(1)).toEqual(["line 2"]));
		await user.click(screen.getByRole("button", { name: "Time range" }));
		await user.click(
			await screen.findByRole("button", { name: "Last 7 days" }),
		);
		await waitFor(() => expect(api.calls).toHaveLength(2));
		const last = api.calls[1];
		expect(
			Date.parse(last?.query.get("to") ?? "") -
				Date.parse(last?.query.get("from") ?? ""),
		).toBe(7 * 24 * 60 * 60_000);
	});

	test("searching filters the lines locally and a search that matches nothing offers to clear", async () => {
		const api = stubApi({
			"GET /services/:id/logs/central": page([
				entry(3, "s", "ERROR timeout"),
				entry(2, "s", "INFO started"),
				entry(1, "s", "ERROR disk full"),
			]),
		});
		const user = userEvent.setup();
		await show();
		await waitFor(() => expect(allRows().length).toBeGreaterThanOrEqual(2));
		const box = screen.getByRole("combobox", { name: "Search central logs" });
		await user.type(box, "timeout");
		await waitFor(() => expect(allRows()).toEqual(["ERROR timeout"]));
		await user.clear(box);
		await user.type(box, "nonexistent");
		expect(await screen.findByText(/No lines match/)).toBeInTheDocument();
		await user.click(screen.getByText("Clear search"));
		await waitFor(() => expect(allRows().length).toBeGreaterThanOrEqual(2));
		// Search is local: no second request.
		expect(api.calls).toHaveLength(1);
	});
});
