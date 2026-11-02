import { afterEach, beforeEach, describe, expect, spyOn, test } from "bun:test";
import { act, fireEvent, render, waitFor } from "@testing-library/react";
import { createRef } from "react";
import { stubCanvas2d } from "../canvas-stub";
import { coarseLadder, SHARPEN_SHARE } from "./reveal";
import {
	ScrollRevealImage,
	type ScrollRevealImageProps,
} from "./scroll-reveal-image";
import { installImageEnv, withBox, withMotion } from "./test-env";

let env: Awaited<ReturnType<typeof installImageEnv>>;
beforeEach(async () => {
	env = await installImageEnv(stubCanvas2d);
});
afterEach(() => env.restore());

let n = 0;
const src = (name: string) => `/reveal-${name}-${++n}.png`;

const root = (view: ReturnType<typeof render>) =>
	view.container.firstElementChild as HTMLElement;
const photo = (view: ReturnType<typeof render>) =>
	root(view).querySelector(":scope > img") as HTMLImageElement | null;

const reveal = (props: Partial<ScrollRevealImageProps> = {}) => (
	<ScrollRevealImage
		src={src("img")}
		alt="A mountain"
		cell={4}
		palette={["#000000", "#ffffff"]}
		{...props}
	/>
);

describe("ScrollRevealImage with a given progress", () => {
	let restoreMotion: () => void;
	beforeEach(() => {
		restoreMotion = withMotion();
	});
	afterEach(() => restoreMotion());

	test("a described image is an image with its label; alt='' is decorative", () => {
		const view = render(reveal({ progress: 0 }));
		expect(root(view)).toHaveAttribute("role", "img");
		expect(root(view)).toHaveAttribute("aria-label", "A mountain");
		view.unmount();
		const decorative = render(reveal({ progress: 0, alt: "" }));
		expect(root(decorative)).toHaveAttribute("aria-hidden", "true");
		expect(root(decorative)).not.toHaveAttribute("role");
	});

	test("at the start it is the coarse dither: no photo yet", () => {
		const view = render(reveal({ progress: 0 }));
		expect(root(view)).toHaveAttribute("data-stage", "coarse");
		expect(photo(view)).toBeNull();
	});

	test("sharpens through the ladder, reaching fine detail at the end of the sharpen share", () => {
		const view = render(reveal({ progress: 0.3 }));
		expect(root(view)).toHaveAttribute("data-stage", "coarse");
		view.rerender(reveal({ progress: SHARPEN_SHARE }));
		expect(root(view)).toHaveAttribute("data-stage", "fine");
		expect(photo(view)).toBeNull();
	});

	test("then the photo dissolves in through a dither mask sized to the cell", () => {
		const view = render(reveal({ progress: 0.86 }));
		expect(root(view)).toHaveAttribute("data-stage", "fine");
		const img = photo(view) as HTMLImageElement;
		expect(img).not.toBeNull();
		expect(img).toHaveAttribute("aria-hidden", "true");
		expect(img).toHaveAttribute("alt", "");
		expect(img.style.maskImage).toContain("url(");
		expect(img.style.maskSize).toBeTruthy();
	});

	test("a finished reveal shows the whole photo, unmasked", () => {
		const view = render(reveal({ progress: 1 }));
		expect(root(view)).toHaveAttribute("data-stage", "photo");
		const img = photo(view) as HTMLImageElement;
		expect(img.style.maskImage).toBe("");
		expect(img).toHaveClass("object-cover");
	});

	test("the photo respects fit=contain", () => {
		const view = render(reveal({ progress: 1, fit: "contain" }));
		expect(photo(view)).toHaveClass("object-contain");
	});

	test("end='dither' stops at full-detail dither and never shows the photo", () => {
		const view = render(reveal({ progress: 1, end: "dither" }));
		expect(root(view)).toHaveAttribute("data-stage", "fine");
		expect(photo(view)).toBeNull();
	});

	test("progress is clamped and quantized, and reported to onProgressChange", () => {
		const seen: number[] = [];
		const view = render(
			reveal({ progress: 1.7, onProgressChange: (p) => seen.push(p) }),
		);
		expect(seen).toEqual([1]);
		view.rerender(
			reveal({ progress: 0.5001, onProgressChange: (p) => seen.push(p) }),
		);
		expect(seen.at(-1)).toBe(Math.round(0.5001 * 240) / 240);
		view.rerender(
			reveal({ progress: -3, onProgressChange: (p) => seen.push(p) }),
		);
		expect(seen.at(-1)).toBe(0);
	});

	test("an unchanged progress is not reported twice", () => {
		const seen: number[] = [];
		const onProgressChange = (p: number) => seen.push(p);
		const view = render(reveal({ progress: 0.4, onProgressChange }));
		view.rerender(reveal({ progress: 0.4, onProgressChange }));
		expect(seen).toEqual([Math.round(0.4 * 240) / 240]);
	});

	test("coarsest sets how many steps the ladder has", async () => {
		const widths = async (coarsest: number) => {
			const view = render(reveal({ progress: 0, coarsest }));
			env.observers.intersect(root(view), true);
			env.observers.resize(root(view), 240, 120);
			await waitFor(() =>
				expect(root(view).querySelector("canvas")).not.toBeNull(),
			);
			const width = (root(view).querySelector("canvas") as HTMLCanvasElement)
				.width;
			view.unmount();
			return width;
		};
		// Stage 0 draws the coarsest multiplier: cells of 4px × coarsest.
		const [first] = coarseLadder(24);
		expect(await widths(24)).toBe(Math.ceil(240 / (4 * (first as number))));
		const [small] = coarseLadder(4);
		expect(await widths(4)).toBe(Math.ceil(240 / (4 * (small as number))));
	});

	test("waits for the image to come near before loading anything", async () => {
		const s = src("lazy");
		const view = render(reveal({ src: s, progress: 0 }));
		env.observers.resize(root(view), 80, 40);
		await act(async () => {});
		expect(env.images.loaded).not.toContain(s);
		expect(root(view)).toHaveAttribute("data-state", "idle");
		env.observers.intersect(root(view), true);
		await waitFor(() =>
			expect(root(view)).toHaveAttribute("data-state", "ready"),
		);
	});

	test("the image's ratio is reserved once known", async () => {
		const view = render(reveal({ progress: 0 }));
		env.observers.intersect(root(view), true);
		env.observers.resize(root(view), 80, 40);
		await waitFor(() =>
			expect(root(view).style.aspectRatio).toBe(`${400 / 300} / 1`),
		);
		view.unmount();
		expect(
			root(render(reveal({ progress: 0, ratio: 3 }))).style.aspectRatio,
		).toBe("3 / 1");
	});
});

describe("ScrollRevealImage following the scroll", () => {
	let restoreMotion: () => void;
	let raf: FrameRequestCallback[];
	let rafSpy: ReturnType<typeof spyOn>;
	const innerHeight = 1000;

	beforeEach(() => {
		restoreMotion = withMotion();
		raf = [];
		rafSpy = spyOn(globalThis, "requestAnimationFrame").mockImplementation(((
			cb: FrameRequestCallback,
		) => {
			raf.push(cb);
			return raf.length;
		}) as never);
		window.innerHeight = innerHeight;
	});
	afterEach(() => {
		restoreMotion();
		rafSpy.mockRestore();
	});
	const flush = () =>
		act(() => {
			for (const cb of raf.splice(0)) cb(0);
		});

	test("progress follows the box's position in the window: 0 as it enters, 1 at the finish line", () => {
		const seen: number[] = [];
		const view = render(reveal({ onProgressChange: (p) => seen.push(p) }));
		// start=1 → enters at the bottom (top = 1000); finish=0.3 → 300px down.
		const box = withBox(root(view), { top: 1000, width: 100, height: 200 });
		env.observers.intersect(root(view), true);
		expect(seen.at(-1)).toBe(0);
		box.mockReturnValue({
			top: 650,
			left: 0,
			width: 100,
			height: 200,
		} as DOMRect);
		fireEvent.scroll(window);
		flush();
		expect(seen.at(-1)).toBe(0.5);
		box.mockReturnValue({
			top: 300,
			left: 0,
			width: 100,
			height: 200,
		} as DOMRect);
		fireEvent.scroll(window);
		flush();
		expect(seen.at(-1)).toBe(1);
		expect(root(view)).toHaveAttribute("data-stage", "photo");
		box.mockReturnValue({
			top: -400,
			left: 0,
			width: 100,
			height: 200,
		} as DOMRect);
		fireEvent.scroll(window);
		flush();
		expect(seen.at(-1)).toBe(1);
	});

	test("a burst of scroll events measures once per frame", () => {
		const view = render(reveal());
		const box = withBox(root(view), { top: 1000, width: 100, height: 200 });
		env.observers.intersect(root(view), true);
		const measured = box.mock.calls.length;
		for (let i = 0; i < 5; i++) fireEvent.scroll(window);
		expect(raf).toHaveLength(1);
		flush();
		expect(box.mock.calls.length).toBe(measured + 1);
	});

	test("start and finish move the scroll range", () => {
		const seen: number[] = [];
		const view = render(
			reveal({
				start: 0.8,
				finish: 0.2,
				onProgressChange: (p) => seen.push(p),
			}),
		);
		withBox(root(view), { top: 500, width: 100, height: 200 });
		env.observers.intersect(root(view), true);
		// From 800 to 200: top 500 is halfway.
		expect(seen.at(-1)).toBe(0.5);
	});

	test("a scroll root is followed instead of the window, with positions relative to it", () => {
		const scroller = document.createElement("div");
		document.body.appendChild(scroller);
		withBox(scroller, { top: 100, width: 400, height: 400 });
		const ref = createRef<Element>() as { current: Element | null };
		ref.current = scroller;
		const seen: number[] = [];
		const view = render(
			reveal({ scrollRoot: ref, onProgressChange: (p) => seen.push(p) }),
		);
		// Relative top 400 of 400: the entry line (progress 0).
		withBox(root(view), { top: 500, width: 100, height: 100 });
		env.observers.intersect(root(view), true);
		expect(seen.at(-1)).toBe(0);
		withBox(root(view), { top: 300, width: 100, height: 100 });
		fireEvent.scroll(scroller);
		flush();
		// Relative top 200 of 400: from 400 to 120, so (400-200)/280.
		expect(seen.at(-1)).toBe(Math.round((200 / 280) * 240) / 240);
		scroller.remove();
	});

	test("does not follow the scroll while far from the viewport", () => {
		const view = render(reveal());
		const box = withBox(root(view), { top: 1000, width: 100, height: 200 });
		expect(box.mock.calls.length).toBe(0);
		fireEvent.scroll(window);
		expect(raf).toHaveLength(0);
	});

	test("leaving the page stops listening", () => {
		const view = render(reveal());
		const box = withBox(root(view), { top: 1000, width: 100, height: 200 });
		env.observers.intersect(root(view), true);
		view.unmount();
		const measured = box.mock.calls.length;
		fireEvent.scroll(window);
		flush();
		expect(box.mock.calls.length).toBe(measured);
	});

	test("a resize re-measures", () => {
		const seen: number[] = [];
		const view = render(reveal({ onProgressChange: (p) => seen.push(p) }));
		const box = withBox(root(view), { top: 1000, width: 100, height: 200 });
		env.observers.intersect(root(view), true);
		box.mockReturnValue({
			top: 300,
			left: 0,
			width: 100,
			height: 200,
		} as DOMRect);
		fireEvent(window, new Event("resize"));
		flush();
		expect(seen.at(-1)).toBe(1);
	});

	test("a given progress ignores the scroll entirely", () => {
		const view = render(reveal({ progress: 0.25 }));
		const box = withBox(root(view), { top: 300, width: 100, height: 200 });
		env.observers.intersect(root(view), true);
		fireEvent.scroll(window);
		expect(raf).toHaveLength(0);
		expect(box.mock.calls.length).toBe(0);
	});
});

describe("ScrollRevealImage under reduced motion", () => {
	test("shows the finished state at once, whatever the scroll or progress", () => {
		const seen: number[] = [];
		const view = render(
			reveal({ progress: 0, onProgressChange: (p) => seen.push(p) }),
		);
		expect(root(view)).toHaveAttribute("data-stage", "photo");
		expect(seen).toEqual([1]);
		expect(photo(view)).not.toBeNull();
	});
});
