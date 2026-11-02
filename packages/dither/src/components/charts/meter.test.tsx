import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import { DitherCapacityStat, DitherMeter } from "./meter";

describe("DitherMeter", () => {
	test("shows the label and the percentage; one decimal under 10, whole above", () => {
		const { rerender } = render(<DitherMeter label="Disk" percent={42.4} />);
		expect(screen.getByText("42%")).toBeInTheDocument();
		rerender(<DitherMeter label="Disk" percent={7.26} />);
		expect(screen.getByText("7.3%")).toBeInTheDocument();
		rerender(<DitherMeter label="Disk" percent={0} />);
		expect(screen.getByText("0.0%")).toBeInTheDocument();
	});

	test("the bar is a progressbar named for the label with the percentage as its value", () => {
		render(<DitherMeter label="Disk" percent={42} />);
		const bar = screen.getByRole("progressbar", { name: "Disk" });
		expect(bar).toHaveAttribute("aria-valuenow", "42");
		expect(bar).toHaveAttribute("aria-valuetext", "42%");
	});

	test("not measured: a dash, an empty groove and 'not measured' for screen readers", () => {
		render(<DitherMeter label="Disk" percent={null} />);
		expect(screen.getByText("–")).toBeInTheDocument();
		const bar = screen.getByRole("progressbar", { name: "Disk" });
		expect(bar).toHaveAttribute("aria-valuetext", "not measured");
		expect(bar).toHaveAttribute("aria-valuenow", "0");
	});

	test("detail sits beside the percentage", () => {
		render(<DitherMeter label="Mem" percent={50} detail="4 GiB / 8 GiB" />);
		expect(screen.getByText(/4 GiB \/ 8 GiB/)).toBeInTheDocument();
	});

	test("bare hides the label row but keeps the accessible name", () => {
		render(<DitherMeter label="Disk" percent={42} bare />);
		expect(screen.queryByText("42%")).toBeNull();
		expect(
			screen.getByRole("progressbar", { name: "Disk" }),
		).toBeInTheDocument();
	});

	test("crossing warnAt or badAt adds the state word and highlights the percentage", () => {
		const { rerender } = render(
			<DitherMeter label="Disk" percent={50} warnAt={70} badAt={90} />,
		);
		expect(screen.getByRole("progressbar")).toHaveAttribute(
			"aria-valuetext",
			"50%",
		);
		expect(screen.getByText("50%").className).toContain("text-ink-1");
		rerender(<DitherMeter label="Disk" percent={75} warnAt={70} badAt={90} />);
		expect(screen.getByRole("progressbar")).toHaveAttribute(
			"aria-valuetext",
			"75%, high",
		);
		expect(screen.getByText("75%").className).toContain("text-warning-text");
		rerender(<DitherMeter label="Disk" percent={95} warnAt={70} badAt={90} />);
		expect(screen.getByRole("progressbar")).toHaveAttribute(
			"aria-valuetext",
			"95%, critical",
		);
	});

	test("an unmeasured meter is never flagged", () => {
		render(<DitherMeter label="Disk" percent={null} warnAt={0} badAt={0} />);
		expect(screen.getByText("–").className).toContain("text-ink-1");
	});

	test("threshold and marker become ticks with titles; a warn marker is amber", () => {
		const { container } = render(
			<DitherMeter
				label="Disk"
				percent={50}
				threshold={80}
				marker={{ percent: 60, title: "Committed 60%", warn: true }}
			/>,
		);
		const ticks = [...container.querySelectorAll<HTMLElement>("[title]")];
		expect(ticks.map((t) => t.title)).toEqual([
			"Alert at 80%",
			"Committed 60%",
		]);
		expect(ticks[1]?.className).toContain("bg-warn");
		expect(ticks[0]?.className).not.toContain("bg-warn");
	});

	test("tones pick the fill colour; warnAt overrides it", () => {
		const colorOf = (props: object) => {
			const { container, unmount } = render(
				<DitherMeter label="D" percent={50} animate={false} {...props} />,
			);
			const c = container.querySelector<HTMLElement>(
				"[data-dither-edge] [data-dither-fill]",
			)?.style.backgroundColor;
			unmount();
			return c;
		};
		expect(colorOf({ tone: "warn" })).toBe("var(--color-warn)");
		expect(colorOf({ tone: "bad" })).toBe("var(--color-bad)");
		expect(colorOf({})).toBe("var(--dither-ink)");
		expect(colorOf({ warnAt: 40, badAt: 90 })).toBe("var(--color-warn)");
	});

	test("stale dims the bar", () => {
		const { container } = render(
			<DitherMeter label="Disk" percent={50} stale />,
		);
		expect(container.querySelector("[class*='opacity-50']")).not.toBeNull();
	});
});

describe("DitherCapacityStat", () => {
	test("a big number and a % over the micro-label, with the meter under it", () => {
		const { container } = render(
			<DitherCapacityStat label="cpu" percent={12.34} detail="2 of 16 cores" />,
		);
		expect(container.textContent).toContain("12");
		expect(screen.getByText("%")).toBeInTheDocument();
		expect(screen.getByText("2 of 16 cores")).toBeInTheDocument();
		const bar = screen.getByRole("progressbar", { name: "cpu" });
		expect(bar).toHaveAttribute("aria-valuenow", "12.34");
	});

	test("under 10 keeps a decimal", () => {
		render(<DitherCapacityStat label="cpu" percent={3.14} />);
		expect(screen.getByText("3.1")).toBeInTheDocument();
	});

	test("not measured reads a dash with no percent sign", () => {
		render(<DitherCapacityStat label="cpu" percent={null} />);
		expect(screen.getByText("–")).toBeInTheDocument();
		expect(screen.queryByText("%")).toBeNull();
		expect(screen.getByRole("progressbar")).toHaveAttribute(
			"aria-valuetext",
			"not measured",
		);
	});

	test("renderValue receives the number text and replaces it", () => {
		render(
			<DitherCapacityStat
				label="cpu"
				percent={50}
				renderValue={(t) => <b data-testid="v">{`~${t}`}</b>}
			/>,
		);
		expect(screen.getByTestId("v")).toHaveTextContent("~50");
	});

	test("crossing a threshold highlights the number", () => {
		const { container } = render(
			<DitherCapacityStat label="cpu" percent={95} warnAt={80} />,
		);
		expect(container.querySelector(".type-metric")?.className).toContain(
			"text-warning-text",
		);
		const calm = render(
			<DitherCapacityStat label="mem" percent={10} warnAt={80} />,
		);
		expect(calm.container.querySelector(".type-metric")?.className).toContain(
			"text-ink-1",
		);
	});

	test("stale dims the whole stat but not twice", () => {
		const { container } = render(
			<DitherCapacityStat label="cpu" percent={50} stale />,
		);
		expect((container.firstChild as HTMLElement).className).toContain(
			"opacity-50",
		);
		const inner = within(container)
			.getByRole("progressbar")
			.closest("div[class*='flex-col']");
		expect(inner?.className).not.toContain("opacity-50");
	});

	test("the meter is bare: the label is not repeated as a row", () => {
		render(<DitherCapacityStat label="cpu" percent={50} />);
		expect(screen.getAllByText("cpu")).toHaveLength(1);
	});
});
