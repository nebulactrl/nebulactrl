import { describe, expect, test } from "bun:test";
import { render, screen, waitFor } from "@testing-library/react";
import type { ClearBox } from "../../lib/gl/clear-box";
import { DitherNebula, type DitherNebulaProps } from "./nebula";

// happy-dom has no WebGL, so the real shader settles on its fallback: the
// nebula's washes of gas colour. These tests read that fallback.

async function fallbackOf(props: DitherNebulaProps = {}): Promise<HTMLElement> {
	const { container } = render(<DitherNebula {...props} />);
	const host = container.firstElementChild as HTMLElement;
	await waitFor(() => expect(host).toHaveAttribute("data-state", "fallback"));
	return host;
}

/** The fallback layer under the (invisible) canvas host. */
function layer(host: HTMLElement): HTMLElement {
	const el = host.children[1];
	if (!(el instanceof HTMLElement)) throw new Error("no fallback layer");
	return el;
}

const box = (over: Partial<ClearBox> = {}): ClearBox => ({
	x: 10,
	y: 20,
	w: 30,
	h: 40,
	radius: 4,
	feather: 8,
	...over,
});

describe("DitherNebula without WebGL", () => {
	test("the host is a decorative shader that settles on its fallback", async () => {
		const host = await fallbackOf({ className: "inset-0 absolute" });
		expect(host).toHaveAttribute("data-slot", "dither-shader");
		expect(host).toHaveAttribute("aria-hidden", "true");
		expect(host).toHaveClass("inset-0", "absolute");
	});

	test("with no focus, three soft washes in the gas colours fill the box", async () => {
		const gas = layer(await fallbackOf()).firstElementChild as HTMLElement;
		expect(gas).toHaveClass("absolute", "inset-0");
		const image = gas.style.backgroundImage;
		expect(image.match(/radial-gradient/g)).toHaveLength(3);
		expect(image).toContain(
			"radial-gradient(60% 55% at 28% 38%, color-mix(in oklch, #6d6bff 34%, transparent)",
		);
		expect(image).toContain(
			"radial-gradient(50% 45% at 72% 62%, color-mix(in oklch, #ff5cc6 24%, transparent)",
		);
		expect(image).toContain(
			"radial-gradient(70% 60% at 55% 30%, color-mix(in oklch, #3a36b0 40%, transparent)",
		);
		expect(gas.style.maskImage).toBe("");
	});

	test("the palette's gas slots colour the washes: mid, bright, then thin gas", async () => {
		// happy-dom drops `var()` inside colour-mix, so the colours are literal here.
		const gas = layer(
			await fallbackOf({
				palette: ["#000000", "#111111", "#222222", "#333333"],
			}),
		).firstElementChild as HTMLElement;
		const image = gas.style.backgroundImage;
		expect(image).toContain("color-mix(in oklch, #222222 34%, transparent)");
		expect(image).toContain("color-mix(in oklch, #333333 24%, transparent)");
		expect(image).toContain("color-mix(in oklch, #111111 40%, transparent)");
	});

	test("a focus confines the gas to an ellipse with a smaller core", async () => {
		const gas = layer(
			await fallbackOf({ focus: { x: 0.5, y: 0.4, rx: 0.3, ry: 0.2 } }),
		).firstElementChild as HTMLElement;
		const image = gas.style.backgroundImage;
		expect(image.match(/radial-gradient/g)).toHaveLength(2);
		expect(image).toContain(
			"radial-gradient(30% 20% at 50% 40%, color-mix(in oklch, #6d6bff 26%, transparent)",
		);
		expect(image).toContain(
			"radial-gradient(15% 10% at 50% 40%, color-mix(in oklch, #3a36b0 30%, transparent)",
		);
		expect(gas.style.maskImage).toBe("");
	});

	test("`from` and `to` fade the gas in from the left", async () => {
		const gas = layer(
			await fallbackOf({
				focus: { x: 0.5, y: 0.5, rx: 0.5, ry: 0.5, from: 0.1, to: 0.4 },
			}),
		).firstElementChild as HTMLElement;
		expect(gas.style.maskImage).toBe(
			"linear-gradient(to right, transparent 10%, #000 40%)",
		);
		expect(gas.style.maskComposite).toBe("intersect");
	});

	test("`from` alone does nothing, as the shader needs both ends", async () => {
		const gas = layer(
			await fallbackOf({
				focus: { x: 0.5, y: 0.5, rx: 0.5, ry: 0.5, from: 0.1 },
			}),
		).firstElementChild as HTMLElement;
		expect(gas.style.maskImage).toBe("");
	});

	test("`floor` and `floorFade` fade the gas out towards the bottom", async () => {
		const gas = layer(
			await fallbackOf({
				focus: { x: 0.5, y: 0.5, rx: 0.5, ry: 0.5, floor: 0.8, floorFade: 0.2 },
			}),
		).firstElementChild as HTMLElement;
		expect(gas.style.maskImage).toBe(
			"linear-gradient(to bottom, #000 60%, transparent 80%)",
		);
	});

	test("a floor without a fade is a hard edge, and both masks combine", async () => {
		const gas = layer(
			await fallbackOf({
				focus: {
					x: 0.5,
					y: 0.5,
					rx: 0.5,
					ry: 0.5,
					floor: 0.5,
					from: 0,
					to: 0.3,
				},
			}),
		).firstElementChild as HTMLElement;
		expect(gas.style.maskImage).toBe(
			"linear-gradient(to right, transparent 0%, #000 30%), linear-gradient(to bottom, #000 50%, transparent 50%)",
		);
	});

	test("no clear boxes (or an empty list) adds no wrappers", async () => {
		const empty = await fallbackOf({ clear: [] });
		expect(layer(empty).children).toHaveLength(1);
		expect(layer(empty).firstElementChild?.children).toHaveLength(0);
	});

	test("a clear box wraps the gas in a feathered mask of its own", async () => {
		const wrapper = layer(await fallbackOf({ clear: [box()] }))
			.firstElementChild as HTMLElement;
		expect(wrapper).toHaveClass("absolute", "inset-0");
		expect(wrapper.style.maskComposite).toBe("add");
		expect(wrapper.style.maskImage).toBe(
			"linear-gradient(to right, rgba(0,0,0,1) 2px, rgba(0,0,0,0.84375) 4px, rgba(0,0,0,0.5) 6px, rgba(0,0,0,0.15625) 8px, rgba(0,0,0,0) 10px, rgba(0,0,0,0) 40px, rgba(0,0,0,0.15625) 42px, rgba(0,0,0,0.5) 44px, rgba(0,0,0,0.84375) 46px, rgba(0,0,0,1) 48px), " +
				"linear-gradient(to bottom, rgba(0,0,0,1) 12px, rgba(0,0,0,0.84375) 14px, rgba(0,0,0,0.5) 16px, rgba(0,0,0,0.15625) 18px, rgba(0,0,0,0) 20px, rgba(0,0,0,0) 60px, rgba(0,0,0,0.15625) 62px, rgba(0,0,0,0.5) 64px, rgba(0,0,0,0.84375) 66px, rgba(0,0,0,1) 68px)",
		);
		// The gas sits inside the wrapper.
		expect(
			(wrapper.firstElementChild as HTMLElement).style.backgroundImage,
		).toContain("radial-gradient");
	});

	test("a box with no feather clears with a hard edge", async () => {
		const wrapper = layer(await fallbackOf({ clear: [box({ feather: 0 })] }))
			.firstElementChild as HTMLElement;
		const horizontal = wrapper.style.maskImage.split("), ")[0] ?? "";
		expect(horizontal).toContain("rgba(0,0,0,1) 10px");
		expect(horizontal).toContain("rgba(0,0,0,0) 10px, rgba(0,0,0,0) 40px");
		expect(horizontal).toContain("rgba(0,0,0,1) 40px");
	});

	test("several boxes nest in order and the fourth is the last", async () => {
		const boxes = [0, 1, 2, 3, 4].map((i) => box({ x: i * 100, w: 10 }));
		const outer = layer(await fallbackOf({ clear: boxes }))
			.firstElementChild as HTMLElement;
		const chain: HTMLElement[] = [];
		let node = outer as HTMLElement | null;
		while (node && node.style.maskComposite === "add") {
			chain.push(node);
			node = node.firstElementChild as HTMLElement | null;
		}
		// Four wrappers (the fifth box is dropped), then the gas.
		expect(chain).toHaveLength(4);
		expect(node?.style.backgroundImage).toContain("radial-gradient");
		// Outermost is the first box: its clearing starts at x = 0 - 8 (feather).
		expect(chain[0]?.style.maskImage).toContain("rgba(0,0,0,1) -8px");
		expect(chain[3]?.style.maskImage).toContain("rgba(0,0,0,1) 292px");
	});

	test("a custom fallback replaces the washes", async () => {
		const host = await fallbackOf({
			fallback: <i data-testid="custom">sky</i>,
			clear: [box()],
		});
		expect(screen.getByTestId("custom")).toHaveTextContent("sky");
		expect(
			host.querySelector("[class~='inset-0'] [style*='radial-gradient']"),
		).toBeNull();
	});
});
