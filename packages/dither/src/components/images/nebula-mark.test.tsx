import { describe, expect, test } from "bun:test";
import { render } from "@testing-library/react";
import {
	NEBULA_MARK_PETALS,
	NEBULA_MARK_STARS,
	NEBULA_MARK_VIEWBOX,
	nebulaMarkSvg,
} from "./nebula-mark";
import { markTier, NEBULA_MARK_DITHER_MIN, NebulaMark } from "./nebula-mark-g";
import { MARK_G_TIERS, MARK_G_WINDOW } from "./nebula-mark-g.generated";

const colors = {
	from: "#111111",
	mid: "#222222",
	to: "#333333",
	star: "#444444",
};

describe("nebulaMarkSvg", () => {
	const doc = new DOMParser().parseFromString(
		nebulaMarkSvg(colors, 120),
		"image/svg+xml",
	);

	test("is a square svg of the requested size with the mark's viewBox", () => {
		const svg = doc.documentElement;
		expect(svg.getAttribute("width")).toBe("120");
		expect(svg.getAttribute("height")).toBe("120");
		expect(svg.getAttribute("viewBox")).toBe(NEBULA_MARK_VIEWBOX);
	});

	test("the default size is the mark's own 340", () => {
		expect(nebulaMarkSvg(colors)).toContain('width="340" height="340"');
	});

	test("every petal is painted with the one gradient, from-mid-to in order", () => {
		const paths = Array.from(doc.querySelectorAll("path"));
		expect(paths.map((p) => p.getAttribute("d"))).toEqual([
			...NEBULA_MARK_PETALS,
		]);
		for (const path of paths) expect(path.getAttribute("fill")).toBe("url(#g)");
		const stops = Array.from(doc.querySelectorAll("stop"));
		expect(stops.map((s) => s.getAttribute("stop-color"))).toEqual([
			"#111111",
			"#222222",
			"#333333",
		]);
		expect(stops[1]?.getAttribute("offset")).toBe(".55");
	});

	test("the four stars sit where the geometry says, in the star colour", () => {
		const circles = Array.from(doc.querySelectorAll("circle"));
		expect(circles).toHaveLength(NEBULA_MARK_STARS.length);
		circles.forEach((circle, i) => {
			expect(Number(circle.getAttribute("cx"))).toBe(
				NEBULA_MARK_STARS[i]?.cx as number,
			);
			expect(Number(circle.getAttribute("cy"))).toBe(
				NEBULA_MARK_STARS[i]?.cy as number,
			);
			expect(circle.getAttribute("fill")).toBe("#444444");
		});
	});
});

describe("markTier", () => {
	test("picks the tier whose size is nearest", () => {
		const sizes = MARK_G_TIERS.map((t) => t.size);
		for (const tier of MARK_G_TIERS)
			expect(markTier(tier.size).size).toBe(tier.size);
		expect(markTier(0).size).toBe(Math.min(...sizes));
		expect(markTier(10_000).size).toBe(Math.max(...sizes));
		const [a, b] = sizes;
		if (a !== undefined && b !== undefined) {
			expect(markTier(a + (b - a) * 0.4).size).toBe(a);
			expect(markTier(a + (b - a) * 0.6).size).toBe(b);
		}
	});
});

describe("NebulaMark", () => {
	test("is decorative without a label", () => {
		const { container } = render(<NebulaMark />);
		const svg = container.querySelector("svg") as SVGElement;
		expect(svg).toHaveAttribute("aria-hidden", "true");
		expect(svg).not.toHaveAttribute("role");
		expect(svg.querySelector("title")).toBeNull();
	});

	test("a label makes it a named image", () => {
		const { getByRole } = render(<NebulaMark label="NebulaCtrl" />);
		const img = getByRole("img", { name: "NebulaCtrl" });
		expect(img).not.toHaveAttribute("aria-hidden");
		expect(img.querySelector("title")).toHaveTextContent("NebulaCtrl");
	});

	test("the viewBox is the generated drawing window and the size is the display size", () => {
		const { container } = render(<NebulaMark size={48} />);
		const svg = container.querySelector("svg") as SVGElement;
		const { x, y, size } = MARK_G_WINDOW;
		expect(svg).toHaveAttribute("viewBox", `${x} ${y} ${size} ${size}`);
		expect(svg).toHaveAttribute("width", "48");
		expect(svg).toHaveAttribute("height", "48");
	});

	test("from the dither threshold up it has the dithered shadow and the bevel", () => {
		const { container } = render(<NebulaMark size={NEBULA_MARK_DITHER_MIN} />);
		const shadow = container.querySelector("path[shape-rendering=crispEdges]");
		expect(shadow).not.toBeNull();
		expect(shadow?.getAttribute("d")).toBe(
			markTier(NEBULA_MARK_DITHER_MIN).shadow,
		);
		expect(container.querySelectorAll("mask")).toHaveLength(2);
		expect(container.querySelector("clipPath")).not.toBeNull();
	});

	test("below it the mark is the plain gradient: no shadow, no bevel layers", () => {
		const { container } = render(
			<NebulaMark size={NEBULA_MARK_DITHER_MIN - 1} />,
		);
		expect(
			container.querySelector("path[shape-rendering=crispEdges]"),
		).toBeNull();
		expect(container.querySelectorAll("mask")).toHaveLength(0);
		expect(container.querySelector("clipPath")).toBeNull();
		// The petals are still there, filled with the gradient.
		const gradient = container.querySelector("linearGradient")?.id as string;
		expect(container.querySelector("g[fill]")).toHaveAttribute(
			"fill",
			`url(#${gradient})`,
		);
		expect(container.querySelectorAll("use")).toHaveLength(
			NEBULA_MARK_PETALS.length,
		);
	});

	test("the gradient stops read the brand tokens, with the brand colours as fallback", () => {
		const { container } = render(<NebulaMark />);
		const stops = Array.from(container.querySelectorAll("stop"));
		expect(stops.map((s) => (s as SVGStopElement).style.stopColor)).toEqual([
			"var(--brand-mark-from, #9a6bff)",
			"var(--brand-mark-mid, #6d6bff)",
			"var(--brand-mark-to, #4fa8ff)",
		]);
	});

	test("two marks never share ids", () => {
		const { container } = render(
			<>
				<NebulaMark />
				<NebulaMark />
			</>,
		);
		const ids = Array.from(container.querySelectorAll("[id]"), (el) => el.id);
		expect(new Set(ids).size).toBe(ids.length);
	});

	test("at rest nothing carries the splay classes and there are no stars", () => {
		const { container } = render(<NebulaMark />);
		expect(container.querySelector(".nc-mark")).toBeNull();
		expect(container.querySelector(".nc-mark-petal")).toBeNull();
		expect(container.querySelector(".nc-mark-star")).toBeNull();
	});

	test("hover opts into the splay: numbered petals, four stars flying to their rest points", () => {
		const { container } = render(<NebulaMark hover />);
		expect(container.querySelector("svg")).toHaveClass("nc-mark");
		const petals = Array.from(container.querySelectorAll(".nc-mark-petal"));
		expect(petals).toHaveLength(NEBULA_MARK_PETALS.length);
		petals.forEach((petal, i) => {
			expect(petal).toHaveClass(`nc-mark-petal-${i + 1}`);
		});
		const stars = Array.from(
			container.querySelectorAll<SVGCircleElement>(".nc-mark-star"),
		);
		expect(stars).toHaveLength(4);
		stars.forEach((star, i) => {
			expect(star.style.getPropertyValue("--nc-mark-star-to")).toBe(
				`${NEBULA_MARK_STARS[i]?.cx}px, ${NEBULA_MARK_STARS[i]?.cy}px`,
			);
			expect(star).toHaveAttribute("r", String(NEBULA_MARK_STARS[i]?.r));
		});
		// The emboss and shadow fade while the mark moves.
		expect(container.querySelectorAll(".nc-mark-rest")).toHaveLength(2);
	});

	test("className lands on the svg", () => {
		const { container } = render(<NebulaMark className="size-8" />);
		expect(container.querySelector("svg")).toHaveClass("size-8", "block");
	});
});
