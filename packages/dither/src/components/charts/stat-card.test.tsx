import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { DitherStatCard } from "./stat-card";
import { installChartEnv } from "./test-env";

installChartEnv();

const chip = (c: HTMLElement) =>
	c.querySelector<HTMLElement>("[data-slot='badge']");

describe("DitherStatCard", () => {
	test("label, formatted headline number", () => {
		render(<DitherStatCard label="Requests" value={1234567} />);
		expect(screen.getByText("Requests")).toBeInTheDocument();
		expect(screen.getByText("1,234,567")).toBeInTheDocument();
	});

	test("a string value is shown as given", () => {
		render(<DitherStatCard label="Uptime" value="99.99%" />);
		expect(screen.getByText("99.99%")).toBeInTheDocument();
	});

	test("valueNode replaces the plain number", () => {
		render(<DitherStatCard label="X" value={5} valueNode={<b>five</b>} />);
		expect(screen.getByText("five")).toBeInTheDocument();
		expect(screen.queryByText("5")).toBeNull();
	});

	test("no delta: no badge", () => {
		const { container } = render(<DitherStatCard label="X" value={1} />);
		expect(chip(container)).toBeNull();
	});

	test("an improving delta is good, with the arrow in words and spoken form", () => {
		const { container } = render(
			<DitherStatCard
				label="Requests"
				value={10}
				delta={8.1}
				deltaLabel="vs last month"
			/>,
		);
		const c = chip(container);
		expect(c).toHaveAttribute("data-tone", "ok");
		expect(c).toHaveTextContent("8.1%");
		expect(c?.title).toBe("vs last month");
		expect(screen.getByText("Up 8.1% vs last month")).toHaveClass("sr-only");
		// The visible words repeat the change; the arrow is an icon.
		expect(screen.getByText("8.1%")).toHaveAttribute("aria-hidden", "true");
		expect(c?.querySelector("svg path")?.getAttribute("d")).toBe(
			"m5 12 7-7 7 7M12 19V5",
		);
		expect(screen.getAllByText("vs last month")).toHaveLength(1);
	});

	test("a rise is bad when down is good (latency, error rates)", () => {
		const { container } = render(
			<DitherStatCard label="p99" value={10} delta={5} good="down" />,
		);
		expect(chip(container)).toHaveAttribute("data-tone", "bad");
		expect(screen.getByText("Up 5%")).toBeInTheDocument();
	});

	test("a fall shows the down arrow; good when down is good", () => {
		const { container } = render(
			<DitherStatCard label="p99" value={10} delta={-3.04} good="down" />,
		);
		expect(chip(container)).toHaveAttribute("data-tone", "ok");
		expect(screen.getByText("Down 3%")).toBeInTheDocument();
		expect(chip(container)?.querySelector("svg path")?.getAttribute("d")).toBe(
			"M12 5v14m7-7-7 7-7-7",
		);
	});

	test("a change that rounds to zero is neutral and flat", () => {
		const { container } = render(
			<DitherStatCard
				label="X"
				value={1}
				delta={0.01}
				deltaLabel="vs last week"
			/>,
		);
		expect(chip(container)).toHaveAttribute("data-tone", "off");
		expect(screen.getByText("No change vs last week")).toBeInTheDocument();
		expect(chip(container)?.querySelector("svg path")?.getAttribute("d")).toBe(
			"M5 12h14",
		);
	});

	test("a non-finite delta reads as no change", () => {
		const { container } = render(
			<DitherStatCard label="X" value={1} delta={Number.NaN} />,
		);
		expect(chip(container)).toHaveAttribute("data-tone", "off");
	});

	test("a custom delta object gives text, tone and title", () => {
		const { container } = render(
			<DitherStatCard
				label="X"
				value={1}
				delta={{ text: "▼ 12 ms", tone: "bad", title: "p50 latency" }}
			/>,
		);
		const c = chip(container);
		expect(c).toHaveAttribute("data-tone", "bad");
		expect(c?.title).toBe("p50 latency");
		expect(c).toHaveTextContent("12 ms");
		expect(c).not.toHaveTextContent("▼");
		expect(c?.querySelector("svg path")?.getAttribute("d")).toBe(
			"M12 5v14m7-7-7 7-7-7",
		);
	});

	test("a custom delta without a glyph keeps its words and has no arrow, except a flat neutral", () => {
		const plain = render(
			<DitherStatCard
				label="X"
				value={1}
				delta={{ text: "new", tone: "good" }}
			/>,
		);
		expect(chip(plain.container)?.querySelector("svg")).toBeNull();
		expect(chip(plain.container)).toHaveTextContent("new");
		plain.unmount();
		const flat = render(
			<DitherStatCard
				label="X"
				value={1}
				delta={{ text: "same", tone: "neutral" }}
			/>,
		);
		expect(
			chip(flat.container)?.querySelector("svg path")?.getAttribute("d"),
		).toBe("M5 12h14");
		flat.unmount();
		const dash = render(
			<DitherStatCard
				label="X"
				value={1}
				delta={{ text: "–", tone: "neutral" }}
			/>,
		);
		expect(chip(dash.container)?.querySelector("svg")).toBeNull();
	});

	test("deltaLabel shows only with a delta; hint always shows", () => {
		const { rerender } = render(
			<DitherStatCard
				label="X"
				value={1}
				deltaLabel="vs last week"
				hint="per UTC day"
			/>,
		);
		expect(screen.queryByText("vs last week")).toBeNull();
		expect(screen.getByText("per UTC day")).toBeInTheDocument();
		rerender(
			<DitherStatCard
				label="X"
				value={1}
				delta={2}
				deltaLabel="vs last week"
			/>,
		);
		expect(screen.getByText("vs last week")).toBeInTheDocument();
	});

	test("children render inside the card", () => {
		render(
			<DitherStatCard label="X" value={1}>
				<p>extra</p>
			</DitherStatCard>,
		);
		expect(screen.getByText("extra")).toBeInTheDocument();
	});

	test("a trend of two or more values draws a labelled sparkline", () => {
		render(<DitherStatCard label="Requests" value={1} trend={[1, 2, 3]} />);
		expect(
			screen.getByRole("img", { name: /^Requests trend: latest 3/ }),
		).toBeInTheDocument();
	});

	test("trendLabel names the trend", () => {
		render(
			<DitherStatCard
				label="Requests"
				value={1}
				trend={[1, 2]}
				trendLabel="Requests, 24h"
			/>,
		);
		expect(
			screen.getByRole("img", { name: /^Requests, 24h: latest 2/ }),
		).toBeInTheDocument();
	});

	test("fewer than two values draw no trend", () => {
		const one = render(<DitherStatCard label="X" value={1} trend={[5]} />);
		expect(screen.queryByRole("img")).toBeNull();
		one.unmount();
		const none = render(<DitherStatCard label="X" value={1} trend={null} />);
		expect(screen.queryByRole("img")).toBeNull();
		none.unmount();
		render(<DitherStatCard label="X" value={1} />);
		expect(screen.queryByRole("img")).toBeNull();
	});

	test("trend colour, fill and bloom pass through to the sparkline", () => {
		const { container } = render(
			<DitherStatCard
				label="X"
				value={1}
				trend={[1, 5, 2]}
				color="#123456"
				fill="solid"
				bloom="low"
			/>,
		);
		expect(container.querySelector("path[stroke='#123456']")).not.toBeNull();
		expect(container.querySelector("feGaussianBlur")).not.toBeNull();
		expect(
			container.querySelector("clipPath + g path")?.getAttribute("fill"),
		).toMatch(/^color-mix/);
	});

	test("the trend has no latest-value dot", () => {
		const { container } = render(
			<DitherStatCard label="X" value={1} trend={[1, 5, 2]} />,
		);
		expect(container.querySelector("circle")).toBeNull();
	});
});
