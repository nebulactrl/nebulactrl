import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { DitherSkeleton } from "./skeleton";

const wells = () =>
	Array.from(document.querySelectorAll<HTMLElement>("[data-slot=skeleton]"));
const layers = (well: Element) =>
	Array.from(well.children).filter(
		(child) => child.getAttribute("aria-hidden") === "true",
	) as HTMLElement[];

describe("DitherSkeleton block", () => {
	test("is one well that is decoration only and shimmers by default", () => {
		render(<DitherSkeleton className="h-10 w-40" />);
		const [well] = wells();
		expect(wells()).toHaveLength(1);
		expect(well).toHaveAttribute("data-animation", "sweep");
		expect(well).toHaveClass("h-10", "w-40");
		for (const layer of layers(well as HTMLElement)) {
			expect(layer).toHaveAttribute("aria-hidden", "true");
		}
		expect(screen.queryByRole("progressbar")).toBeNull();
	});

	test("the sweep is a band on top of the resting grain, and gets dropped under reduced motion", () => {
		render(<DitherSkeleton />);
		const [grain, sweep] = layers(wells()[0] as HTMLElement);
		expect(layers(wells()[0] as HTMLElement)).toHaveLength(2);
		expect(grain?.className).toMatch(/nc-dm-\d+/);
		expect(sweep).toHaveClass("animate-dither-sweep", "motion-reduce:hidden");
		expect(sweep?.className).toMatch(/nc-dm-\d+/);
		expect(sweep?.className.match(/nc-dm-\d+/)?.[0]).not.toBe(
			grain?.className.match(/nc-dm-\d+/)?.[0],
		);
	});

	test("shimmer={false} leaves only the grain", () => {
		render(<DitherSkeleton shimmer={false} />);
		expect(wells()[0]).toHaveAttribute("data-animation", "none");
		expect(layers(wells()[0] as HTMLElement)).toHaveLength(1);
	});

	test("animation='none' is the same as shimmer off, and animation wins over shimmer", () => {
		const { unmount } = render(<DitherSkeleton animation="none" />);
		expect(wells()[0]).toHaveAttribute("data-animation", "none");
		unmount();
		render(<DitherSkeleton shimmer={false} animation="pulse" />);
		expect(wells()[0]).toHaveAttribute("data-animation", "pulse");
	});

	test("pulse steps through three nested levels in order", () => {
		render(<DitherSkeleton animation="pulse" />);
		const [grain, ...steps] = layers(wells()[0] as HTMLElement);
		expect(grain).toBeDefined();
		expect(steps).toHaveLength(3);
		steps.forEach((step, index) => {
			expect(step).toHaveClass("nc-if-pulse", `nc-if-p${index + 1}`);
		});
		const masks = steps.map((s) => s.className.match(/nc-dm-\d+/)?.[0]);
		expect(new Set(masks).size).toBe(3);
		expect(steps[0]?.style.getPropertyValue("--nc-if-dur")).toBe("1.8s");
		expect(steps[0]?.style.getPropertyValue("--nc-if-delay")).toBe("0s");
	});

	test("duration and delay set the sweep timing", () => {
		render(<DitherSkeleton duration={3} delay={0.5} />);
		const sweep = layers(wells()[0] as HTMLElement)[1] as HTMLElement;
		expect(sweep.style.animationDuration).toBe("3s");
		expect(sweep.style.animationDelay).toBe("0.5s");
		expect(sweep.style.animationFillMode).toBe("backwards");
	});

	test("without a delay or duration the sweep keeps the stylesheet's timing", () => {
		render(<DitherSkeleton />);
		const sweep = layers(wells()[0] as HTMLElement)[1] as HTMLElement;
		expect(sweep.style.animationDuration).toBe("");
		expect(sweep.style.animationDelay).toBe("");
	});

	test("duration and delay feed the pulse through custom properties", () => {
		render(<DitherSkeleton animation="pulse" duration={2.4} delay={0.3} />);
		const step = layers(wells()[0] as HTMLElement)[1] as HTMLElement;
		expect(step.style.getPropertyValue("--nc-if-dur")).toBe("2.4s");
		expect(step.style.getPropertyValue("--nc-if-delay")).toBe("0.3s");
	});

	test("the pulse keyframes are on the page", () => {
		render(<DitherSkeleton animation="pulse" />);
		expect(
			document.head.querySelector("style[data-nc-interface-keyframes]")
				?.textContent,
		).toContain("@keyframes nc-if-p1");
	});

	test("color tints grain, sweep and pulse layers instead of the muted ink", () => {
		const { unmount } = render(<DitherSkeleton />);
		const [grain, sweep] = layers(wells()[0] as HTMLElement);
		expect(grain).toHaveClass("bg-ink-1/[0.09]");
		expect(sweep).toHaveClass("bg-ink-1/[0.2]");
		unmount();
		render(<DitherSkeleton color="blue" />);
		const [tintGrain, tintSweep] = layers(wells()[0] as HTMLElement);
		expect(tintGrain).not.toHaveClass("bg-ink-1/[0.09]");
		expect(tintSweep).not.toHaveClass("bg-ink-1/[0.2]");
	});

	test("a coloured pulse drops the default ink on each step", () => {
		render(<DitherSkeleton animation="pulse" color="green" />);
		const [grain, ...steps] = layers(wells()[0] as HTMLElement);
		expect(grain).not.toHaveClass("bg-ink-1/[0.09]");
		expect(steps).toHaveLength(3);
		for (const step of steps) expect(step).not.toHaveClass("bg-ink-1/[0.2]");
	});

	test("fill, cell and matrix choose different resting masks", () => {
		render(
			<>
				<DitherSkeleton shimmer={false} />
				<DitherSkeleton shimmer={false} fill="gradient" />
				<DitherSkeleton shimmer={false} fill="dotted" />
				<DitherSkeleton shimmer={false} fill="hatched" />
				<DitherSkeleton shimmer={false} fill="solid" />
				<DitherSkeleton shimmer={false} cell={4} />
				<DitherSkeleton shimmer={false} matrix="blue-noise" />
			</>,
		);
		const masks = wells().map(
			(well) => layers(well)[0]?.className.match(/nc-dm-\d+/)?.[0],
		);
		expect(masks.every(Boolean)).toBe(true);
		expect(new Set(masks).size).toBe(7);
	});

	test("passes props and children through and keeps style", () => {
		render(
			<DitherSkeleton id="s1" style={{ height: 12 }}>
				<span>inside</span>
			</DitherSkeleton>,
		);
		const well = wells()[0] as HTMLElement;
		expect(well).toHaveAttribute("id", "s1");
		expect(well.style.height).toBe("12px");
		expect(screen.getByText("inside").parentElement).toBe(well);
	});
});

describe("DitherSkeleton text", () => {
	test("stacks three lines by default, the last one shorter", () => {
		render(<DitherSkeleton shape="text" />);
		expect(document.querySelector("[data-slot=skeleton-text]")).not.toBeNull();
		expect(wells().map((w) => w.style.width)).toEqual(["100%", "100%", "60%"]);
	});

	test("lines sets the count; a single line is full width; fractions floor and zero clamps to one", () => {
		const widths = (lines: number) => {
			const { unmount } = render(<DitherSkeleton shape="text" lines={lines} />);
			const out = wells().map((w) => w.style.width);
			unmount();
			return out;
		};
		expect(widths(5)).toEqual(["100%", "100%", "100%", "100%", "60%"]);
		expect(widths(1)).toEqual(["100%"]);
		expect(widths(2.9)).toEqual(["100%", "60%"]);
		expect(widths(0)).toEqual(["100%"]);
	});

	test("each line staggers its sweep by 80ms", () => {
		render(<DitherSkeleton shape="text" delay={0.1} />);
		const delays = wells().map(
			(well) => (layers(well)[1] as HTMLElement).style.animationDelay,
		);
		expect(delays).toEqual(["0.1s", "0.18s", "0.26s"]);
	});

	test("the wrapper takes className and style; the lines are thin", () => {
		render(
			<DitherSkeleton
				shape="text"
				className="max-w-xs"
				style={{ margin: 3 }}
			/>,
		);
		const wrapper = document.querySelector(
			"[data-slot=skeleton-text]",
		) as HTMLElement;
		expect(wrapper).toHaveClass("max-w-xs");
		expect(wrapper.style.margin).toBe("3px");
		for (const well of wells()) expect(well).toHaveClass("h-3");
		expect(wells()[0]).not.toHaveClass("max-w-xs");
	});

	test("animation options reach every line", () => {
		render(<DitherSkeleton shape="text" lines={2} animation="pulse" />);
		expect(wells().map((w) => w.getAttribute("data-animation"))).toEqual([
			"pulse",
			"pulse",
		]);
	});
});

describe("DitherSkeleton circle", () => {
	test("is a round well 40px across by default", () => {
		render(<DitherSkeleton shape="circle" />);
		const well = wells()[0] as HTMLElement;
		expect(well).toHaveClass("rounded-full", "shrink-0");
		expect(well.style.width).toBe("40px");
		expect(well.style.height).toBe("40px");
	});

	test("size sets the diameter and style can still add to it", () => {
		render(<DitherSkeleton shape="circle" size={64} style={{ margin: 2 }} />);
		const well = wells()[0] as HTMLElement;
		expect(well.style.width).toBe("64px");
		expect(well.style.margin).toBe("2px");
	});

	test("className merges with the round shape", () => {
		render(<DitherSkeleton shape="circle" className="ring-2" />);
		expect(wells()[0]).toHaveClass("rounded-full", "ring-2");
	});
});
