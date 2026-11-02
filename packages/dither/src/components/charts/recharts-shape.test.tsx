import { afterEach, describe, expect, spyOn, test } from "bun:test";
import { render } from "@testing-library/react";
import type { AreaRevealShapeProps } from "recharts";
import { DitherAreaShape } from "./recharts-shape";

type Pts = AreaRevealShapeProps["points"];

const pts = (...xy: ([number, number] | null)[]) =>
	xy.map((p) =>
		p ? { x: p[0], y: p[1] } : { x: null, y: null },
	) as unknown as Pts;

const draw = (props: Record<string, unknown>) =>
	render(
		<svg>
			<title>t</title>
			<DitherAreaShape
				{...({
					points: pts([0, 40], [50, 10], [100, 30]),
					baseLine: 100,
				} as object)}
				{...props}
			/>
		</svg>,
	);

const lines = (c: HTMLElement) => [...c.querySelectorAll("path[fill='none']")];

let spy: { mockRestore: () => void } | undefined;
afterEach(() => spy?.mockRestore());

describe("DitherAreaShape", () => {
	test("draws a crisp line over the dithered fill in the given colour", () => {
		const { container } = draw({ color: "#00aa00" });
		const [line] = lines(container);
		expect(line).toHaveAttribute("stroke", "#00aa00");
		expect(line).toHaveAttribute("stroke-width", "1.5");
		expect(line?.getAttribute("d")?.startsWith("M0 40")).toBe(true);
		expect(container.querySelector("g[aria-hidden] clipPath")).not.toBeNull();
	});

	test("falls back to the Area's stroke, then to the dither ink", () => {
		const withStroke = draw({ stroke: "#123456", strokeWidth: 3 });
		expect(lines(withStroke.container)[0]).toHaveAttribute("stroke", "#123456");
		expect(lines(withStroke.container)[0]).toHaveAttribute("stroke-width", "3");
		withStroke.unmount();
		const bare = draw({});
		expect(lines(bare.container)[0]).toHaveAttribute(
			"stroke",
			"var(--dither-ink)",
		);
	});

	test("a gap in the data splits the fill and the line into two runs", () => {
		const { container } = draw({
			points: pts([0, 40], [20, 30], null, [60, 20], [80, 25], [100, 10]),
		});
		expect(lines(container)).toHaveLength(2);
		expect(container.querySelectorAll("g[aria-hidden]")).toHaveLength(2);
	});

	test("no points draws nothing", () => {
		const { container } = draw({ points: pts() });
		expect(lines(container)).toHaveLength(0);
	});

	test("a stacked series fills between its own upper and lower edge", () => {
		const { container } = draw({
			points: pts([0, 40], [100, 30]),
			baseLine: pts([0, 80], [100, 75]),
		});
		const d = container.querySelector("clipPath path")?.getAttribute("d") ?? "";
		expect(d).toContain("100 75");
		expect(d).toContain("0 80");
	});

	test("linear and step types join points with straight segments", () => {
		const smooth = draw({ type: "monotone" });
		const smoothD = lines(smooth.container)[0]?.getAttribute("d") ?? "";
		smooth.unmount();
		const linear = draw({ type: "linear" });
		const linearD = lines(linear.container)[0]?.getAttribute("d") ?? "";
		linear.unmount();
		const step = draw({ type: "step" });
		expect(lines(step.container)[0]?.getAttribute("d")).toBe(linearD);
		expect(linearD).not.toBe(smoothD);
	});

	test("forced colours use a plain tint instead of a dither pattern", () => {
		spy = spyOn(window, "matchMedia").mockImplementation(
			(q: string) =>
				({
					matches: q === "(forced-colors: active)",
					media: q,
					addEventListener: () => {},
					removeEventListener: () => {},
				}) as unknown as MediaQueryList,
		);
		const { container } = draw({ color: "#abcdef" });
		const fills = [...container.querySelectorAll("g[clip-path] path")].map(
			(p) => p.getAttribute("fill"),
		);
		expect(fills).toEqual(["color-mix(in oklab, #abcdef 26%, transparent)"]);
	});

	test("variant and bands reach the fill", () => {
		const { container } = draw({ bands: 3 });
		expect(container.querySelectorAll("g[clip-path] path")).toHaveLength(3);
		const flat = draw({ variant: "hatched" });
		expect(flat.container.querySelectorAll("g[clip-path] path")).toHaveLength(
			1,
		);
	});
});
