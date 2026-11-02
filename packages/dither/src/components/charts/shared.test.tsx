import { describe, expect, mock, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
	bloomOpacity,
	bloomRadius,
	ChartLegend,
	ChartReadout,
	COLOR_ALIASES,
	flatFill,
	inkFill,
	makeFormat,
	matrixProp,
	read,
	resolveColor,
	seriesColor,
	seriesCss,
	trackTint,
} from "./shared";

describe("colours", () => {
	test("seriesColor cycles through eight tokens", () => {
		expect(seriesColor(0)).toBe("var(--dither-series-1)");
		expect(seriesColor(7)).toBe("var(--dither-series-8)");
		expect(seriesColor(8)).toBe("var(--dither-series-1)");
	});

	test("resolveColor: default by index, aliases to tokens, other names through the palette, raw CSS passes", () => {
		expect(resolveColor(undefined, 2)).toBe("var(--dither-series-3)");
		for (const [name, css] of Object.entries(COLOR_ALIASES)) {
			expect(resolveColor(name)).toBe(css);
		}
		expect(resolveColor("blue")).toBe("var(--dither-blue)");
		expect(resolveColor("--chart-1")).toBe("var(--chart-1)");
		expect(resolveColor("#123456")).toBe("#123456");
		expect(resolveColor("rgb(1 2 3)")).toBe("rgb(1 2 3)");
	});

	test("seriesCss: own colour, then chart colours by position, then the default", () => {
		expect(seriesCss("#111", ["#222"], 0)).toBe("#111");
		expect(seriesCss(undefined, ["#222", "#333"], 1)).toBe("#333");
		expect(seriesCss(undefined, ["#222"], 1)).toBe("var(--dither-series-2)");
		expect(seriesCss(undefined, undefined, 0)).toBe("var(--dither-series-1)");
	});
});

describe("fills", () => {
	test("matrixProp is empty without a matrix", () => {
		expect(matrixProp(undefined)).toEqual({});
		expect(matrixProp("bayer8")).toEqual({ matrix: "bayer8" });
	});

	test("inkFill carries tone, colour, cell and the matrix when set", () => {
		expect(inkFill(0.5, "red", 3)).toEqual({
			tone: 0.5,
			color: "red",
			cell: 3,
		});
		expect(inkFill(0.5, "red", 3, "bayer2")).toEqual({
			tone: 0.5,
			color: "red",
			cell: 3,
			matrix: "bayer2",
		});
	});

	test("flatFill: dotted is sparse dots, hatched denser hatching", () => {
		expect(flatFill("dotted", "red", 4)).toEqual({
			tone: 0.3,
			color: "red",
			cell: 4,
			shape: "dot",
		});
		expect(flatFill("hatched", "red", 4, "bayer8")).toEqual({
			tone: 0.4,
			color: "red",
			cell: 4,
			shape: "hatch",
			matrix: "bayer8",
		});
	});

	test("bloom opacity scales with --dither-bloom; off is null; radius grows with strength", () => {
		expect(bloomOpacity("off")).toBeNull();
		expect(bloomOpacity(undefined)).toBeNull();
		expect(bloomOpacity("low")).toBe("calc(var(--dither-bloom, 0.35) * 1.1)");
		expect(bloomOpacity("high")).toBe("calc(var(--dither-bloom, 0.35) * 2.2)");
		expect(bloomRadius("low")).toBe(5);
		expect(bloomRadius("high")).toBe(9);
		expect(bloomRadius(undefined)).toBe(5);
	});

	test("trackTint mixes the colour into the well", () => {
		expect(trackTint("red")).toBe(
			"color-mix(in oklab, red 16%, var(--color-surface-well))",
		);
		expect(trackTint("red", 30)).toContain("red 30%");
	});
});

describe("read and makeFormat", () => {
	test("read by key or by function", () => {
		const d = { a: 3, b: "x" };
		expect(read(d, "a")).toBe(3);
		expect(read(d, (row) => row.b.toUpperCase())).toBe("X");
	});

	test("makeFormat: fallback, function, and Intl options", () => {
		const fallback = (v: number) => `f${v}`;
		expect(makeFormat(undefined, fallback)(1)).toBe("f1");
		expect(makeFormat((v) => `${v}!`, fallback)(2)).toBe("2!");
		expect(makeFormat({ style: "percent" }, fallback)(0.25)).toBe("25%");
		expect(makeFormat({ minimumFractionDigits: 2 }, fallback)(1234.5)).toBe(
			"1,234.50",
		);
	});
});

describe("ChartLegend", () => {
	const items = [
		{ label: "Reads", color: "#111", value: "12" },
		{ label: "Writes", color: "#222", dashed: true },
	];

	test("lists the items with their figures as a labelled list", () => {
		render(<ChartLegend items={items} />);
		const list = screen.getByRole("list", { name: "Legend" });
		const rows = within(list).getAllByRole("listitem");
		expect(rows.map((r) => r.textContent)).toEqual(["Reads12", "Writes"]);
		expect(within(list).queryAllByRole("button")).toHaveLength(0);
	});

	test("dashed items repeat the dash in the swatch", () => {
		const { container } = render(<ChartLegend items={items} />);
		const swatches = container.querySelectorAll("li span[aria-hidden]");
		expect((swatches[0] as HTMLElement).style.background).not.toContain(
			"repeating",
		);
		expect((swatches[1] as HTMLElement).style.background).toContain(
			"repeating-linear-gradient",
		);
	});

	test("onToggle makes pressable buttons reflecting hidden items", async () => {
		const user = userEvent.setup();
		const onToggle = mock((_label: string) => {});
		render(
			<ChartLegend
				items={items}
				hidden={new Set(["Writes"])}
				onToggle={onToggle}
			/>,
		);
		const reads = screen.getByRole("button", { name: /Reads/ });
		const writes = screen.getByRole("button", { name: /Writes/ });
		expect(reads).toHaveAttribute("aria-pressed", "true");
		expect(writes).toHaveAttribute("aria-pressed", "false");
		await user.click(writes);
		expect(onToggle).toHaveBeenCalledWith("Writes");
	});

	test("onLock makes spotlight buttons: hover and focus call onHover, click locks, click again unlocks", async () => {
		const user = userEvent.setup();
		const onHover = mock((_l: string | null) => {});
		const onLock = mock((_l: string | null) => {});
		const { rerender } = render(
			<ChartLegend
				items={items}
				onHover={onHover}
				onLock={onLock}
				locked={null}
				focus={null}
			/>,
		);
		const reads = screen.getByRole("button", { name: "Spotlight Reads" });
		await user.hover(reads);
		expect(onHover).toHaveBeenLastCalledWith("Reads");
		await user.unhover(reads);
		expect(onHover).toHaveBeenLastCalledWith(null);
		reads.focus();
		expect(onHover).toHaveBeenLastCalledWith("Reads");
		reads.blur();
		expect(onHover).toHaveBeenLastCalledWith(null);
		await user.click(reads);
		expect(onLock).toHaveBeenLastCalledWith("Reads");
		rerender(
			<ChartLegend
				items={items}
				onHover={onHover}
				onLock={onLock}
				locked="Reads"
				focus="Reads"
			/>,
		);
		expect(
			screen.getByRole("button", { name: "Spotlight Reads" }),
		).toHaveAttribute("aria-pressed", "true");
		await user.click(screen.getByRole("button", { name: "Spotlight Reads" }));
		expect(onLock).toHaveBeenLastCalledWith(null);
	});

	test("Escape on a spotlight chip lets go", async () => {
		const user = userEvent.setup();
		const onLock = mock((_l: string | null) => {});
		render(<ChartLegend items={items} onLock={onLock} locked="Reads" />);
		screen.getByRole("button", { name: "Spotlight Reads" }).focus();
		await user.keyboard("{Escape}");
		expect(onLock).toHaveBeenCalledWith(null);
		await user.keyboard("a");
		expect(onLock).toHaveBeenCalledTimes(1);
	});

	test("non-focused chips dim while one is spotlit", () => {
		render(<ChartLegend items={items} onLock={() => {}} focus="Reads" />);
		expect(
			screen.getByRole("button", { name: "Spotlight Writes" }).className,
		).toContain("opacity-45");
		expect(
			screen.getByRole("button", { name: "Spotlight Reads" }).className,
		).not.toContain("opacity-45");
	});

	test("a static legend dims non-focused items too", () => {
		const { container } = render(<ChartLegend items={items} focus="Reads" />);
		const spans = container.querySelectorAll("li > span");
		expect(spans[1]?.className).toContain("opacity-45");
	});
});

describe("ChartReadout", () => {
	test("a status card with the title and one row per series", () => {
		render(
			<ChartReadout
				title="Tue"
				rows={[
					{ label: "Reads", color: "#111", value: "40" },
					{ label: "Writes", color: "#222", value: "10" },
				]}
				style={{ left: 12 }}
			/>,
		);
		const card = screen.getByRole("status");
		expect(card).toHaveTextContent("Tue");
		expect(card).toHaveTextContent("Reads40");
		expect(card).toHaveTextContent("Writes10");
		expect(card.style.left).toBe("12px");
	});
});
