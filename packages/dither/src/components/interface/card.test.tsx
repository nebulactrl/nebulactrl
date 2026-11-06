import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { DitherCard } from "./card";

const card = () => screen.getByTestId("card");
const finish = () => card().querySelector("[data-slot=card-finish]");
const glow = () => card().querySelector("[data-slot=card-glow]");

describe("DitherCard", () => {
	test("renders children inside the card material and keeps passthrough props", () => {
		render(
			<DitherCard data-testid="card" variant="raised" className="p-4" id="c1">
				Hello
			</DitherCard>,
		);
		expect(card()).toHaveTextContent("Hello");
		expect(card()).toHaveAttribute("data-slot", "card");
		expect(card()).toHaveAttribute("data-variant", "raised");
		expect(card()).toHaveAttribute("id", "c1");
		expect(card()).toHaveClass("p-4", "relative", "isolate");
	});

	test("the finish is decoration: aria-hidden and not clickable", () => {
		render(<DitherCard data-testid="card" />);
		expect(finish()).toHaveAttribute("aria-hidden", "true");
		expect(finish()).toHaveClass("pointer-events-none");
	});

	test("without glow props only the finish is drawn, no glow", () => {
		render(<DitherCard data-testid="card" />);
		expect(glow()).toBeNull();
		expect(card().querySelector("svg")).toBeNull();
	});

	test("finish selects which layers are drawn: all = grain + edge, edge, grain, bloom, none", () => {
		const layers = (
			finishProp: "all" | "edge" | "grain" | "bloom" | "none",
		) => {
			const { unmount } = render(
				<DitherCard data-testid="card" finish={finishProp} />,
			);
			const count = finish()?.children.length;
			unmount();
			return count;
		};
		expect(layers("all")).toBe(2);
		expect(layers("edge")).toBe(1);
		expect(layers("grain")).toBe(1);
		expect(layers("bloom")).toBe(1);
		expect(layers("none")).toBe(0);
	});

	test("the default finish is all", () => {
		render(<DitherCard data-testid="card" />);
		expect(finish()?.children).toHaveLength(2);
	});

	test("the bloom finish puts accent dither in the right half", () => {
		render(<DitherCard data-testid="card" finish="bloom" />);
		expect(finish()?.firstElementChild).toHaveClass("right-0", "w-1/2");
	});

	test("any glow-related prop turns the glow on, defaulting to the top-left corner", () => {
		for (const props of [
			{ glow: "top-left" as const },
			{ interactive: true },
			{ reach: 100 },
			{ strength: 0.3 },
			{ color: "blue" },
			{ fill: "dotted" as const },
			{ matrix: "bayer8" as const },
		]) {
			const { unmount } = render(<DitherCard data-testid="card" {...props} />);
			expect(glow()).not.toBeNull();
			unmount();
		}
	});

	test("interactive={false} still counts as setting the glow", () => {
		render(<DitherCard data-testid="card" interactive={false} />);
		expect(glow()).not.toBeNull();
		// but nothing grows on hover
		expect(card().querySelectorAll("[data-slot=card-glow] > svg")).toHaveLength(
			1,
		);
	});

	test("a corner glow is concentric circles at the corner, a rimmed stack of six rings", () => {
		render(<DitherCard data-testid="card" glow="bottom-right" />);
		const circles = card().querySelectorAll(
			"[data-slot=card-glow] > svg circle",
		);
		expect(circles).toHaveLength(6);
		expect(card().querySelector("ellipse")).toBeNull();
		for (const circle of circles) {
			expect(circle).toHaveAttribute("cx", "100%");
			expect(circle).toHaveAttribute("cy", "100%");
		}
		// Largest first, so the denser rings sit on top.
		const radii = Array.from(circles, (c) => Number(c.getAttribute("r")));
		expect(radii).toEqual([...radii].sort((a, b) => b - a));
		expect(radii[0]).toBe(240);
	});

	test("each glow name puts the light at its own corner or edge", () => {
		const origins = {
			"top-left": ["0", "0"],
			"top-right": ["100%", "0"],
			"bottom-left": ["0", "100%"],
			"bottom-right": ["100%", "100%"],
			top: ["50%", "0"],
			bottom: ["50%", "100%"],
		} as const;
		for (const [name, [cx, cy]] of Object.entries(origins)) {
			const { unmount } = render(
				<DitherCard data-testid="card" glow={name as "top"} />,
			);
			const shape = card().querySelector(
				"[data-slot=card-glow] > svg circle, [data-slot=card-glow] > svg ellipse",
			) as Element;
			expect([
				name,
				shape.getAttribute("cx"),
				shape.getAttribute("cy"),
			]).toEqual([name, cx, cy]);
			unmount();
		}
	});

	test("an edge glow is drawn with wide ellipses centred on the edge", () => {
		render(<DitherCard data-testid="card" glow="top" reach={100} />);
		const ellipses = card().querySelectorAll(
			"[data-slot=card-glow] > svg ellipse",
		);
		expect(ellipses).toHaveLength(6);
		expect(
			card().querySelector("[data-slot=card-glow] > svg circle"),
		).toBeNull();
		const first = ellipses[0] as Element;
		expect(first).toHaveAttribute("cx", "50%");
		expect(first).toHaveAttribute("cy", "0");
		expect(first).toHaveAttribute("ry", "100");
		expect(Number(first.getAttribute("rx"))).toBeGreaterThan(100);
	});

	test("reach scales the outermost ring", () => {
		render(<DitherCard data-testid="card" reach={90} />);
		const circle = card().querySelector(
			"[data-slot=card-glow] > svg circle",
		) as Element;
		expect(circle).toHaveAttribute("r", "90");
	});

	test("every ring fills from its own pattern, with a distinct id", () => {
		render(<DitherCard data-testid="card" glow="top-left" />);
		const svg = card().querySelector("[data-slot=card-glow] > svg") as Element;
		const ids = Array.from(svg.querySelectorAll("pattern"), (p) => p.id);
		expect(ids).toHaveLength(6);
		expect(new Set(ids).size).toBe(6);
		for (const id of ids) expect(id).toMatch(/^nc-glow-[a-zA-Z0-9_-]+-\d$/);
		const fills = Array.from(svg.querySelectorAll("circle"), (c) =>
			c.getAttribute("fill"),
		);
		expect(fills).toEqual(ids.map((id) => `url(#${id})`));
	});

	test("two cards never share pattern ids", () => {
		render(
			<>
				<DitherCard data-testid="a" glow="top" />
				<DitherCard data-testid="b" glow="top" />
			</>,
		);
		const ids = Array.from(document.querySelectorAll("pattern"), (p) => p.id);
		expect(new Set(ids).size).toBe(ids.length);
		expect(ids.length).toBe(24);
	});

	test("the glow colour paints the pattern cells", () => {
		render(<DitherCard data-testid="card" color="red" glow="top-left" />);
		const path = card().querySelector("pattern path") as SVGPathElement;
		expect(path.style.fill).toBe("var(--dither-red)");
	});

	test("the default glow ink is the dither ink", () => {
		render(<DitherCard data-testid="card" glow="top-left" />);
		const path = card().querySelector("pattern path") as SVGPathElement;
		expect(path.style.fill).toBe("var(--dither-ink)");
	});

	test("textured fills hold one texture per ring and fade by ring opacity", () => {
		render(<DitherCard data-testid="card" fill="dotted" strength={1} />);
		const circle = card().querySelector(
			"[data-slot=card-glow] > svg circle",
		) as Element;
		// A textured ring contributes a share of the strength, not full opacity.
		expect(Number(circle.getAttribute("opacity"))).toBeLessThan(1);
		render(<DitherCard data-testid="grad" glow="top-left" />);
		const gradientCircle = screen
			.getByTestId("grad")
			.querySelector("[data-slot=card-glow] > svg circle") as Element;
		expect(gradientCircle).toHaveAttribute("opacity", "1");
	});

	test("the rim is on by default and rim={false} removes it", () => {
		const { unmount } = render(<DitherCard data-testid="card" glow="top" />);
		const rim = card().querySelector("[data-slot=card-rim]") as HTMLElement;
		expect(rim).not.toBeNull();
		// The rim is a band as wide as one glow cell.
		expect(rim.style.padding).toBe("2px");
		expect(rim.querySelector("svg")).not.toBeNull();
		unmount();
		render(<DitherCard data-testid="card" glow="top" rim={false} />);
		expect(card().querySelector("[data-slot=card-rim]")).toBeNull();
	});

	test("cell sets the rim thickness", () => {
		render(<DitherCard data-testid="card" glow="top" cell={5} />);
		expect(
			(card().querySelector("[data-slot=card-rim]") as HTMLElement).style
				.padding,
		).toBe("5px");
	});

	test("the rim reaches further than the surface glow", () => {
		render(<DitherCard data-testid="card" glow="top-left" reach={100} />);
		const surface = card().querySelector(
			"[data-slot=card-glow] > svg circle",
		) as Element;
		const rimCircle = card().querySelector(
			"[data-slot=card-rim] svg circle",
		) as Element;
		expect(Number(rimCircle.getAttribute("r"))).toBeGreaterThan(
			Number(surface.getAttribute("r")),
		);
	});

	test("interactive adds a second stack that grows ring by ring on hover and focus", () => {
		render(
			<DitherCard data-testid="card" glow="top-left" interactive rim={false} />,
		);
		const stacks = card().querySelectorAll("[data-slot=card-glow] > svg");
		expect(stacks).toHaveLength(2);
		const rings = stacks[1]?.querySelectorAll(":scope > g") ?? [];
		expect(rings).toHaveLength(6);
		for (const ring of rings) {
			expect(ring).toHaveClass(
				"group-hover/card:opacity-100",
				"group-focus-within/card:opacity-100",
			);
		}
		// Outermost ring last in the stack: delays count down 225, 180, … 0.
		expect(
			Array.from(rings, (g) => (g as SVGGElement).style.transitionDelay),
		).toEqual(["225ms", "180ms", "135ms", "90ms", "45ms", "0ms"]);
		// The hover stack reaches 1.5× further than the idle one.
		const idle = stacks[0]?.querySelector("circle") as Element;
		const grown = stacks[1]?.querySelector("circle") as Element;
		expect(Number(grown.getAttribute("r"))).toBe(
			Number(idle.getAttribute("r")) * 1.5,
		);
	});

	test("radius sets the corner and is overridable by style", () => {
		const { unmount } = render(<DitherCard data-testid="card" radius={20} />);
		expect(card().style.borderRadius).toBe("20px");
		unmount();
		render(
			<DitherCard
				data-testid="card"
				radius={20}
				style={{ borderRadius: 4, opacity: 0.9 }}
			/>,
		);
		expect(card().style.borderRadius).toBe("4px");
		expect(card().style.opacity).toBe("0.9");
	});

	test("without a radius the card keeps its own corner and a passed style", () => {
		render(<DitherCard data-testid="card" style={{ opacity: 0.5 }} />);
		expect(card().style.borderRadius).toBe("");
		expect(card().style.opacity).toBe("0.5");
	});
});
