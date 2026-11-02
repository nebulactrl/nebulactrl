import { describe, expect, spyOn, test } from "bun:test";
import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DitherSparkline, type DitherSparklineProps } from "./sparkline";
import { installChartEnv } from "./test-env";

const env = installChartEnv();

const spark = (props: Partial<DitherSparklineProps> = {}) => (
	<DitherSparkline label="Requests" data={[2, 8, 4, 10]} {...props} />
);
const svgOf = (c: HTMLElement) => c.querySelector("svg") as SVGSVGElement;
const line = (c: HTMLElement) =>
	c.querySelector("path[stroke-width='1.5']") as SVGPathElement;
const nums = (d: string | null) =>
	d?.match(/-?\d+(\.\d+)?/g)?.map(Number) ?? [];

describe("DitherSparkline summary", () => {
	test("a labelled image naming the latest, low and high values", () => {
		render(spark());
		expect(
			screen.getByRole("img", { name: "Requests: latest 10, low 2, high 10" }),
		).toBeInTheDocument();
	});

	test("format shapes the summary", () => {
		render(spark({ format: (v) => `${v} rps` }));
		expect(
			screen.getByRole("img", {
				name: "Requests: latest 10 rps, low 2 rps, high 10 rps",
			}),
		).toBeInTheDocument();
		const intl = render(
			spark({ label: "Rate", data: [0.25, 0.5], format: { style: "percent" } }),
		);
		expect(intl.container.firstElementChild).toHaveAttribute(
			"aria-label",
			"Rate: latest 50%, low 25%, high 50%",
		);
	});

	test("no data is said so, and nothing is drawn", () => {
		const { container } = render(spark({ data: [] }));
		expect(
			screen.getByRole("img", { name: "Requests: no data" }),
		).toBeInTheDocument();
		expect(container.querySelector("svg")).toBeNull();
	});

	test("default format rounds to two decimals", () => {
		render(spark({ data: [1.23456, 2.98765] }));
		expect(screen.getByRole("img")).toHaveAttribute(
			"aria-label",
			"Requests: latest 2.99, low 1.23, high 2.99",
		);
	});
});

describe("DitherSparkline geometry", () => {
	test("a single value draws no line", () => {
		const { container } = render(spark({ data: [5] }));
		expect(container.querySelector("svg")).toBeNull();
	});

	test("no svg until the element is at least 8px wide; it follows resizes", () => {
		env.width = 6;
		const { container } = render(spark());
		expect(container.querySelector("svg")).toBeNull();
		env.resize(100);
		expect(svgOf(container)).toHaveAttribute("width", "100");
	});

	test("height sets the svg height", () => {
		const { container } = render(spark({ height: 60 }));
		expect(svgOf(container)).toHaveAttribute("height", "60");
		expect(container.firstElementChild).toHaveStyle({ height: "60px" });
	});

	test("the line starts and ends 3px inside the box; values map to height (0 to max by default)", () => {
		const { container } = render(spark({ data: [0, 10], height: 32 }));
		const [x0, y0, ...rest] = nums(line(container).getAttribute("d"));
		expect(x0).toBe(3);
		expect(y0).toBe(29);
		expect(rest.at(-2)).toBe(477);
		expect(rest.at(-1)).toBe(3);
	});

	test("the default domain keeps a flat low line flat; domain='data' stretches to the range", () => {
		const y = (domain?: DitherSparklineProps["domain"]) => {
			const { container, unmount } = render(
				spark({ data: [100, 101], ...(domain ? { domain } : {}) }),
			);
			const ys = nums(line(container).getAttribute("d"));
			unmount();
			return [ys[1], ys.at(-1)];
		};
		const [a, b] = y();
		expect(Math.abs((a ?? 0) - (b ?? 0))).toBeLessThan(1);
		const [c, d] = y("data");
		expect(c).toBe(29);
		expect(d).toBe(3);
	});

	test("an explicit domain clamps values outside it", () => {
		const { container } = render(spark({ data: [-50, 500], domain: [0, 100] }));
		const ys = nums(line(container).getAttribute("d"));
		expect(ys[1]).toBe(29);
		expect(ys.at(-1)).toBe(3);
	});

	test("constant data with domain=data does not produce NaN", () => {
		const { container } = render(spark({ data: [5, 5, 5], domain: "data" }));
		expect(container.innerHTML).not.toContain("NaN");
	});

	test("the area fill can be switched off", () => {
		const on = render(spark());
		expect(on.container.querySelector("clipPath")).not.toBeNull();
		on.unmount();
		const { container } = render(spark({ area: false }));
		expect(container.querySelector("clipPath")).toBeNull();
		expect(line(container)).not.toBeNull();
	});

	test("fill variants and tiny cells", () => {
		const fills = (props: Partial<DitherSparklineProps>) => {
			const { container, unmount } = render(spark(props));
			const out = [...container.querySelectorAll("g[clip-path] > path")].map(
				(p) => p.getAttribute("fill"),
			);
			unmount();
			return out;
		};
		expect(fills({})).toHaveLength(5);
		expect(fills({ fill: "dotted" })).toHaveLength(1);
		expect(fills({ fill: "solid" })[0]).toMatch(/^color-mix/);
		expect(fills({ cell: 1 })[0]).toMatch(/^color-mix/);
	});

	test("color tints the line; bloom adds a glow", () => {
		const { container } = render(spark({ color: "#abcdef", bloom: "high" }));
		expect(line(container)).toHaveAttribute("stroke", "#abcdef");
		expect(container.querySelector("feGaussianBlur")).not.toBeNull();
	});

	test("the latest value gets a dot, unless turned off", () => {
		const on = render(spark({ data: [1, 2], color: "#112233" }));
		const dot = on.container.querySelector("circle");
		expect(dot).toHaveAttribute("fill", "#112233");
		expect(Number(dot?.getAttribute("cx"))).toBe(477);
		on.unmount();
		const off = render(spark({ dot: false }));
		expect(off.container.querySelector("circle")).toBeNull();
	});
});

describe("DitherSparkline scrubbing", () => {
	test("by default it is a static image: no focus stop, no readout", () => {
		const { container } = render(spark());
		expect(screen.getByRole("img")).not.toHaveAttribute("tabindex");
		fireEvent.pointerMove(screen.getByRole("img"), { clientX: 200 });
		expect(container.querySelector(".surface-pop")).toBeNull();
		expect(container.querySelector("[aria-live]")).toBeNull();
	});

	test("interactive is a focusable group; focus shows the latest value", async () => {
		const user = userEvent.setup();
		const { container } = render(spark({ interactive: true }));
		const group = screen.getByRole("group", { name: "Requests" });
		expect(group).toHaveAttribute("tabindex", "0");
		expect(
			document.getElementById(group.getAttribute("aria-describedby") ?? ""),
		).toHaveTextContent("Use the arrow keys");
		await user.tab();
		expect(container.querySelector(".surface-pop")).toHaveTextContent("10");
		expect(container.querySelector("[aria-live='polite']")).toHaveTextContent(
			"Requests: 10",
		);
	});

	test("arrows, Home, End and Escape move the marker and update the label", async () => {
		const user = userEvent.setup();
		const { container } = render(
			spark({ interactive: true, format: (v) => `${v}!` }),
		);
		const label = () => container.querySelector(".surface-pop");
		await user.tab();
		await user.keyboard("{ArrowLeft}");
		expect(label()).toHaveTextContent("4!");
		await user.keyboard("{Home}");
		expect(label()).toHaveTextContent("2!");
		expect(container.querySelector("[aria-live='polite']")).toHaveTextContent(
			"Requests: 2!",
		);
		await user.keyboard("{ArrowRight}");
		expect(label()).toHaveTextContent("8!");
		await user.keyboard("{End}");
		expect(label()).toHaveTextContent("10!");
		// The scrub marker replaces the latest-value dot.
		expect(container.querySelectorAll("circle")).toHaveLength(1);
		expect(container.querySelector("circle")).toHaveAttribute("r", "3");
		await user.keyboard("{Escape}");
		expect(label()).toBeNull();
		expect(container.querySelector("circle")).toHaveAttribute("r", "2.5");
	});

	test("an unrelated key does nothing", async () => {
		const user = userEvent.setup();
		const { container } = render(spark({ interactive: true }));
		await user.tab();
		await user.keyboard("{Escape}");
		await user.keyboard("x");
		expect(container.querySelector(".surface-pop")).toBeNull();
	});

	test("pointer position picks the nearest value, clamped to the ends; leaving clears", () => {
		const { container } = render(spark({ interactive: true }));
		const group = screen.getByRole("group");
		fireEvent.pointerMove(group, { clientX: 3 });
		expect(container.querySelector(".surface-pop")).toHaveTextContent("2");
		fireEvent.pointerMove(group, { clientX: 480 });
		expect(container.querySelector(".surface-pop")).toHaveTextContent("10");
		// 3 + (474 / 3) * 1 = 161 -> index 1
		fireEvent.pointerMove(group, { clientX: 161 });
		expect(container.querySelector(".surface-pop")).toHaveTextContent("8");
		fireEvent.pointerLeave(group);
		expect(container.querySelector(".surface-pop")).toBeNull();
	});

	test("pointer moves before there is a drawing are ignored", () => {
		env.width = 4;
		const { container } = render(spark({ interactive: true }));
		fireEvent.pointerMove(screen.getByRole("group"), { clientX: 2 });
		expect(container.querySelector(".surface-pop")).toBeNull();
	});

	test("blur clears the scrub", async () => {
		const user = userEvent.setup();
		const { container } = render(spark({ interactive: true }));
		await user.tab();
		await user.tab();
		expect(container.querySelector(".surface-pop")).toBeNull();
	});

	test("with no data the group is not focusable", () => {
		render(spark({ interactive: true, data: [] }));
		expect(screen.getByRole("group")).not.toHaveAttribute("tabindex");
	});

	test("the label stays inside the box at the ends", () => {
		const { container } = render(spark({ interactive: true, data: [1, 2, 3] }));
		const group = screen.getByRole("group");
		fireEvent.pointerMove(group, { clientX: 3 });
		expect(
			Number.parseFloat(
				(container.querySelector(".surface-pop") as HTMLElement).style.left,
			),
		).toBeGreaterThanOrEqual(0);
		fireEvent.pointerMove(group, { clientX: 480 });
		const left = Number.parseFloat(
			(container.querySelector(".surface-pop") as HTMLElement).style.left,
		);
		expect(left).toBeLessThanOrEqual(480);
	});
});

describe("DitherSparkline entrance", () => {
	const fullMotion = () =>
		spyOn(window, "matchMedia").mockImplementation(
			(q: string) =>
				({
					matches: false,
					media: q,
					addEventListener: () => {},
					removeEventListener: () => {},
				}) as unknown as MediaQueryList,
		);

	test("the line is hidden until seen when motion is allowed", () => {
		const mm = fullMotion();
		const { container } = render(spark());
		const wrap = () => line(container).parentElement as unknown as SVGGElement;
		expect(wrap().style.opacity).toBe("0");
		act(() => env.intersect(true));
		expect(wrap().style.opacity).toBe("");
		mm.mockRestore();
	});

	test("animate={false} and reduced motion never hide it", () => {
		const mm = fullMotion();
		const off = render(spark({ animate: false }));
		expect(
			(line(off.container).parentElement as unknown as SVGGElement).style
				.opacity,
		).toBe("");
		off.unmount();
		mm.mockRestore();
		const reduced = render(spark());
		expect(
			(line(reduced.container).parentElement as unknown as SVGGElement).style
				.opacity,
		).toBe("");
	});
});
