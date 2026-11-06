import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { installChartEnv } from "../charts/test-env";
import { installCanvasEnv } from "./canvas-env";
import { autoCell, DitherText } from "./dither-text";

installChartEnv({ width: 480 });
const canvas = installCanvasEnv();

const canvasOf = (c: HTMLElement) =>
	c.querySelector("canvas") as HTMLCanvasElement | null;
const lastImage = () => {
	const img = canvas.puts.at(-1);
	if (!img) throw new Error("nothing was painted");
	return img;
};
const pixel = (i: number) => [...lastImage().data.slice(i * 4, i * 4 + 4)];
const alphas = () => {
	const { data } = lastImage();
	return Array.from({ length: data.length / 4 }, (_, i) => data[i * 4 + 3]);
};

describe("autoCell", () => {
	test("is about 1/24 of the font size, at least 2", () => {
		expect(autoCell(12)).toBe(2);
		expect(autoCell(48)).toBe(2);
		expect(autoCell(96)).toBe(4);
		expect(autoCell(120)).toBe(5);
	});

	test("an unusable font size gives the fallback 2", () => {
		expect(autoCell(Number.NaN)).toBe(2);
		expect(autoCell(Number.POSITIVE_INFINITY)).toBe(2);
	});
});

describe("DitherText element", () => {
	test("the headline stays real text, in a pre-wrapped inline element in the display font", () => {
		render(<DitherText data-testid="h">Ship it</DitherText>);
		const el = screen.getByTestId("h");
		expect(el).toHaveTextContent("Ship it");
		expect(el.tagName).toBe("SPAN");
		expect(el.style.fontFamily).toBe("var(--font-display)");
	});

	test("as picks the element and className, style and props reach it", () => {
		render(
			<DitherText
				as="h1"
				id="t"
				className="big"
				style={{ color: "red" }}
				aria-label="Launch"
			>
				Launch
			</DitherText>,
		);
		const h = screen.getByRole("heading", { level: 1, name: "Launch" });
		expect(h).toHaveAttribute("id", "t");
		expect(h.className).toContain("big");
		expect(h.style.color).toBe("red");
		// A caller's style does not lose the display font.
		expect(h.style.fontFamily).toBe("var(--font-display)");
	});

	test("the caller's style can override the display font", () => {
		render(
			<DitherText data-testid="h" style={{ fontFamily: "Georgia" }}>
				Hi
			</DitherText>,
		);
		expect(screen.getByTestId("h").style.fontFamily).toBe("Georgia");
	});
});

describe("DitherText painting", () => {
	test("rasterises the text onto a canvas of cols x rows cells sized in CSS px", () => {
		const { container } = render(<DitherText cell={4}>Hi</DitherText>);
		const el = canvasOf(container) as HTMLCanvasElement;
		// 480 x 200 box at 4px cells and dpr 1.
		expect(el.width).toBe(120);
		expect(el.height).toBe(50);
		expect(el.style.width).toBe("480px");
		expect(el.style.height).toBe("200px");
		expect(el.style.imageRendering).toBe("pixelated");
		expect(
			canvas.contexts.some((c) =>
				c.callsOf("fillText").some((a) => a[0] === "Hi"),
			),
		).toBe(true);
	});

	test("once the canvas is drawn the real letters are transparent so only the dither shows", () => {
		const { container } = render(<DitherText>Hi</DitherText>);
		expect(canvasOf(container)).not.toBeNull();
		const letters = container.querySelector("span > span") as HTMLElement;
		expect(letters.className).toContain("text-transparent");
		expect(letters.style.maskImage).toBe("");
	});

	test("a solid fill inks every covered cell in the ink colour; uncovered cells stay transparent", () => {
		canvas.coverage = (i, cols) => (i % cols < cols / 2 ? 255 : 0);
		render(
			<DitherText fill="solid" color="#336699" cell={8}>
				Hi
			</DitherText>,
		);
		const img = lastImage();
		expect([img.width, img.height]).toEqual([60, 25]);
		expect(pixel(0)).toEqual([0x33, 0x66, 0x99, 255]);
		expect(pixel(29)).toEqual([0x33, 0x66, 0x99, 255]);
		// The right half had no glyph there.
		expect(pixel(30)[3]).toBe(0);
		expect(pixel(59)[3]).toBe(0);
	});

	test("a gradient is dense on its starting side and sparse on the other", () => {
		render(
			<DitherText fill="gradient" direction="down" from={1} to={0} cell={8}>
				Hi
			</DitherText>,
		);
		const cols = 60;
		const a = alphas();
		const rowInk = (row: number) =>
			a.slice(row * cols, (row + 1) * cols).filter((v) => v === 255).length;
		expect(rowInk(0)).toBeGreaterThan(rowInk(24));
		expect(rowInk(0)).toBeGreaterThan(cols * 0.8);
		expect(rowInk(24)).toBeLessThan(cols * 0.2);
	});

	test("direction flips which side is dense", () => {
		render(
			<DitherText fill="gradient" direction="up" from={1} to={0} cell={8}>
				Hi
			</DitherText>,
		);
		const cols = 60;
		const a = alphas();
		const rowInk = (row: number) =>
			a.slice(row * cols, (row + 1) * cols).filter((v) => v === 255).length;
		expect(rowInk(24)).toBeGreaterThan(rowInk(0));
	});

	test("dotted and hatched keep the letters readable through a ghost alpha", () => {
		const dotted = render(
			<DitherText fill="dotted" cell={8}>
				Hi
			</DitherText>,
		);
		expect(new Set(alphas())).toEqual(new Set([255, 51]));
		dotted.unmount();
		const hatched = render(
			<DitherText fill="hatched" ghost={0.5} cell={8}>
				Hi
			</DitherText>,
		);
		expect(new Set(alphas())).toEqual(new Set([255, 128]));
		hatched.unmount();
		render(
			<DitherText fill="dotted" ghost={0} cell={8}>
				Hi
			</DitherText>,
		);
		expect(new Set(alphas())).toEqual(new Set([255, 0]));
	});

	test("gradients have no ghost unless asked for one", () => {
		render(
			<DitherText fill="gradient" from={1} to={0.4} cell={8}>
				Hi
			</DitherText>,
		);
		expect(new Set(alphas())).toEqual(new Set([255, 0]));
	});

	test("a palette paints a multi-tone ramp and its darkest colour is the ink", () => {
		render(
			<DitherText
				fill="solid"
				palette={["#000000", "#00ff00"]}
				color="#ff0000"
				cell={8}
			>
				Hi
			</DitherText>,
		);
		expect(pixel(0)).toEqual([0, 255, 0, 255]);
	});

	test("a ramp over a dotted fill uses the first colour between dots and the last on them", () => {
		render(
			<DitherText fill="dotted" palette={["#101010", "#ffffff"]} cell={8}>
				Hi
			</DitherText>,
		);
		const colours = new Set(
			Array.from({ length: 60 * 25 }, (_, i) => pixel(i).join(",")),
		);
		expect(colours).toEqual(new Set(["16,16,16,255", "255,255,255,255"]));
	});

	test("palette names resolve through the theme palette", () => {
		render(
			<DitherText fill="solid" color="green" cell={8}>
				Hi
			</DitherText>,
		);
		// The fallback `green` of the dither palette.
		expect(pixel(0)).toEqual([0x41, 0xd6, 0x86, 255]);
	});

	test("a transparent ramp colour leaves the gaps between dots empty", () => {
		render(
			<DitherText fill="dotted" palette={["transparent", "#ff0000"]} cell={8}>
				Hi
			</DitherText>,
		);
		expect(new Set(alphas())).toEqual(new Set([0, 255]));
		expect(pixel(0)).toEqual([255, 0, 0, 255]);
	});

	test("the cell prop sets the grid; unset it follows the font size, never below 2px", () => {
		const sized = render(<DitherText cell={10}>Hi</DitherText>);
		expect(canvasOf(sized.container)?.width).toBe(48);
		sized.unmount();
		const auto = render(<DitherText>Hi</DitherText>);
		// 16px default font -> autoCell 2 -> 240 columns.
		expect(canvasOf(auto.container)?.width).toBe(240);
	});

	test("changing the text rasterises again", () => {
		const { rerender } = render(<DitherText cell={8}>One</DitherText>);
		rerender(<DitherText cell={8}>Two</DitherText>);
		const drawn = canvas.contexts.flatMap((c) =>
			c.callsOf("fillText").map((a) => a[0]),
		);
		expect(drawn).toContain("One");
		expect(drawn.at(-1)).toBe("Two");
	});

	test("new colours repaint without a new raster", () => {
		const { rerender } = render(
			<DitherText fill="solid" color="#112233" cell={8}>
				Hi
			</DitherText>,
		);
		const rasters = canvas.contexts.flatMap((c) =>
			c.callsOf("fillText"),
		).length;
		rerender(
			<DitherText fill="solid" color="#445566" cell={8}>
				Hi
			</DitherText>,
		);
		expect(pixel(0)).toEqual([0x44, 0x55, 0x66, 255]);
		expect(canvas.contexts.flatMap((c) => c.callsOf("fillText")).length).toBe(
			rasters,
		);
	});

	test("multi-line text is drawn line by line", () => {
		render(<DitherText cell={8}>{"one\ntwo"}</DitherText>);
		const drawn = canvas.contexts.flatMap((c) =>
			c.callsOf("fillText").map((a) => a[0]),
		);
		expect(drawn).toEqual(expect.arrayContaining(["one", "two"]));
	});
});

describe("DitherText fallback", () => {
	test("without a 2D canvas the letters show through a plain dither mask in the ink colour", () => {
		canvas.available = false;
		const { container } = render(
			<DitherText color="#336699" from={1} to={0.4}>
				Hi
			</DitherText>,
		);
		expect(canvasOf(container)).toBeNull();
		const letters = container.querySelector("span > span") as HTMLElement;
		expect(letters.style.color).toBe("#336699");
		expect(letters.style.maskImage).toContain("data:image/svg+xml");
		expect(letters.className).not.toContain("text-transparent");
	});

	test("a solid fill needs no mask", () => {
		canvas.available = false;
		const { container } = render(<DitherText fill="solid">Hi</DitherText>);
		const letters = container.querySelector("span > span") as HTMLElement;
		expect(letters.style.maskImage).toBe("");
		expect(letters.style.color).toBe("var(--dither-ink)");
	});

	test("the fallback colour is the palette's darkest tone", () => {
		canvas.available = false;
		const { container } = render(
			<DitherText palette={["#eeeeee", "#ff0000"]}>Hi</DitherText>,
		);
		expect(
			(container.querySelector("span > span") as HTMLElement).style.color,
		).toBe("#ff0000");
	});

	test("the fallback mask follows the cell and the mid tone", () => {
		canvas.available = false;
		const a = render(<DitherText cell={2}>Hi</DitherText>);
		const sizeA = (a.container.querySelector("span > span") as HTMLElement)
			.style.maskSize;
		a.unmount();
		const b = render(<DitherText cell={6}>Hi</DitherText>);
		const sizeB = (b.container.querySelector("span > span") as HTMLElement)
			.style.maskSize;
		expect(sizeA).toBe("8px");
		expect(sizeB).toBe("24px");
	});
});

describe("DitherText transparent colour", () => {
	const painted = (props: {
		color: string;
		fill?: "solid" | "gradient" | "dotted";
	}) => {
		const { unmount } = render(
			<DitherText fill={props.fill ?? "solid"} color={props.color} cell={8}>
				Hi
			</DitherText>,
		);
		const a = alphas();
		unmount();
		return a;
	};

	test("the transparent keyword paints no ink, whatever the fill", () => {
		for (const fill of ["solid", "gradient", "dotted"] as const) {
			expect(new Set(painted({ color: "transparent", fill }))).toEqual(
				new Set([0]),
			);
		}
	});

	test("a colour with alpha 0 paints no ink either", () => {
		for (const color of [
			"#00000000",
			"rgba(10, 20, 30, 0)",
			"rgb(0 0 0 / 0%)",
		]) {
			expect(new Set(painted({ color }))).toEqual(new Set([0]));
		}
	});

	test("a transparent colour casts no glow", () => {
		const { container } = render(
			<DitherText bloom="high" fill="solid" color="transparent" cell={8}>
				Hi
			</DitherText>,
		);
		expect(canvasOf(container)?.style.filter ?? "").toBe("");
	});

	test("a partly transparent colour scales the ink alpha", () => {
		expect(new Set(painted({ color: "#ff000080" }))).toEqual(new Set([128]));
	});
});

describe("DitherText bloom", () => {
	const filter = (bloom: boolean | "off" | "low" | "high") => {
		const { container, unmount } = render(
			<DitherText bloom={bloom} fill="solid" color="#336699" cell={8}>
				Hi
			</DitherText>,
		);
		const f = canvasOf(container)?.style.filter ?? "";
		unmount();
		return f;
	};

	test("off draws no glow", () => {
		expect(filter("off")).toBe("");
		expect(filter(false)).toBe("");
	});

	test("low and high add a drop shadow in the ink colour, scaled to the box height", () => {
		// Box height 200: low = 200/10 = 20px at 0.3, high = 200/6 = 33px at 0.45.
		expect(filter("low")).toBe("drop-shadow(0 0 20px rgb(51 102 153 / 0.3))");
		expect(filter("high")).toBe("drop-shadow(0 0 33px rgb(51 102 153 / 0.45))");
	});

	test("true is the strong glow", () => {
		expect(filter(true)).toBe(filter("high"));
	});

	test("the glow takes the palette's darkest colour", () => {
		const { container } = render(
			<DitherText bloom="low" palette={["#000000", "#00ff00"]} cell={8}>
				Hi
			</DitherText>,
		);
		expect(canvasOf(container)?.style.filter).toContain("rgb(0 255 0 / 0.3)");
	});
});
