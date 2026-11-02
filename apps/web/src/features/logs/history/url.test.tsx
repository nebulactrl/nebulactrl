import { describe, expect, it } from "bun:test";
import { useRouterState } from "@tanstack/react-router";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "#/test/render";
import type { RangeSelection } from "./model";
import { type LogsMode, useHistoryRange, useLogsMode } from "./url";

function Probe() {
	const [mode, setMode] = useLogsMode();
	const [range, setRange] = useHistoryRange();
	const search = useRouterState({ select: (s) => s.location.searchStr });
	const pick = (next: LogsMode) => () => setMode(next);
	const custom: RangeSelection = {
		kind: "custom",
		from: "2026-10-20T10:00:00.000Z",
		to: undefined,
	};
	return (
		<div>
			<output aria-label="mode">{mode}</output>
			<output aria-label="range">{JSON.stringify(range)}</output>
			<output aria-label="search">{search}</output>
			<button type="button" onClick={pick("history")}>
				history
			</button>
			<button type="button" onClick={pick("central")}>
				central
			</button>
			<button type="button" onClick={pick("live")}>
				live
			</button>
			<button type="button" onClick={() => setRange(custom)}>
				custom
			</button>
			<button
				type="button"
				onClick={() => setRange({ kind: "preset", id: "6h" })}
			>
				preset
			</button>
		</div>
	);
}

const mode = () => screen.getByLabelText("mode");
const range = () =>
	JSON.parse(screen.getByLabelText("range").textContent ?? "");
const search = () => screen.getByLabelText("search");

describe("useLogsMode", () => {
	it("is live when the address has no ?logs=", async () => {
		await renderWithProviders(<Probe />, { path: "/" });
		expect(mode()).toHaveTextContent("live");
	});

	it("reads history and central from the address", async () => {
		const first = await renderWithProviders(<Probe />, {
			path: "/?logs=history",
		});
		expect(mode()).toHaveTextContent("history");
		first.unmount();
		await renderWithProviders(<Probe />, { path: "/?logs=central" });
		expect(mode()).toHaveTextContent("central");
	});

	it("falls back to live for an unknown value", async () => {
		await renderWithProviders(<Probe />, { path: "/?logs=bogus" });
		expect(mode()).toHaveTextContent("live");
	});

	it("writes the mode into the address, keeps other params, and removes it for live", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe />, { path: "/?q=error" });
		await user.click(screen.getByRole("button", { name: "central" }));
		await waitFor(() => expect(mode()).toHaveTextContent("central"));
		expect(search().textContent).toContain("logs=central");
		expect(search().textContent).toContain("q=error");

		await user.click(screen.getByRole("button", { name: "live" }));
		await waitFor(() => expect(mode()).toHaveTextContent("live"));
		expect(search().textContent).not.toContain("logs");
		expect(search().textContent).toContain("q=error");
	});
});

describe("useHistoryRange", () => {
	it("defaults to the last hour", async () => {
		await renderWithProviders(<Probe />, { path: "/" });
		expect(range()).toEqual({ kind: "preset", id: "1h" });
	});

	it("reads a preset", async () => {
		await renderWithProviders(<Probe />, { path: "/?range=7d" });
		expect(range()).toEqual({ kind: "preset", id: "7d" });
	});

	it("reads a custom range, which wins over a preset", async () => {
		await renderWithProviders(<Probe />, {
			path: "/?range=7d&from=2026-10-20T10:00:00Z&to=2026-10-20T12:00:00Z",
		});
		expect(range()).toEqual({
			kind: "custom",
			from: "2026-10-20T10:00:00.000Z",
			to: "2026-10-20T12:00:00.000Z",
		});
	});

	it("writes a custom range, replacing a preset", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe />, { path: "/?range=7d" });
		await user.click(screen.getByRole("button", { name: "custom" }));
		await waitFor(() => expect(range().kind).toBe("custom"));
		expect(search().textContent).toContain("from=");
		expect(search().textContent).not.toContain("range=");
		expect(search().textContent).not.toContain("to=");
	});

	it("writes a preset, dropping a custom range", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe />, {
			path: "/?from=2026-10-20T10:00:00Z",
		});
		await user.click(screen.getByRole("button", { name: "preset" }));
		await waitFor(() => expect(range()).toEqual({ kind: "preset", id: "6h" }));
		expect(search().textContent).toContain("range=6h");
		expect(search().textContent).not.toContain("from=");
	});
});
