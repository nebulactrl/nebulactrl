import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { LagBar } from "./lag-bar";

const MIB = 1024 * 1024;

describe("LagBar", () => {
	test("a lag that was never measured is only a sentence, with no bar", () => {
		render(<LagBar lag={{}} stale={false} />);
		expect(screen.getByText("Lag not measured yet")).toBeInTheDocument();
		expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
	});

	test("a measured lag draws a bar named for the lag and spells the figures out", () => {
		render(
			<LagBar lag={{ lagBytes: 16 * MIB, replayLagMs: 400 }} stale={false} />,
		);
		const bar = screen.getByRole("progressbar", { name: "Replication lag" });
		// 16 MiB of the 64 MiB limit.
		expect(bar).toHaveAttribute("aria-valuenow", "25");
		expect(bar.getAttribute("aria-valuetext")).toStartWith(
			"16 MiB behind · 400 ms",
		);
		expect(
			screen.getAllByText("16 MiB behind · 400 ms").length,
		).toBeGreaterThan(0);
	});

	test("a replica in sync draws an empty bar and says so", () => {
		render(<LagBar lag={{ lagBytes: 0, replayLagMs: 12 }} stale={false} />);
		const bar = screen.getByRole("progressbar");
		expect(bar).toHaveAttribute("aria-valuenow", "0");
		expect(bar.getAttribute("aria-valuetext")).toBe("In sync");
	});

	test("a lag beyond the limit fills the bar completely and never past it", () => {
		render(<LagBar lag={{ lagBytes: 500 * MIB }} stale={false} />);
		expect(screen.getByRole("progressbar")).toHaveAttribute(
			"aria-valuenow",
			"100",
		);
	});

	test("a stale reading dims the figures", () => {
		const { rerender } = render(
			<LagBar lag={{ lagBytes: 1 * MIB }} stale={false} />,
		);
		const fresh = screen.getAllByText("1 MiB behind").at(-1);
		expect(fresh).toHaveClass("text-ink-2");
		rerender(<LagBar lag={{ lagBytes: 1 * MIB }} stale />);
		expect(screen.getAllByText("1 MiB behind").at(-1)).toHaveClass(
			"text-ink-3",
		);
	});
});
