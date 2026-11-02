import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, render, waitFor } from "@testing-library/react";
import { stubCanvas2d } from "../canvas-stub";
import { DitherImage, type DitherImageProps } from "./dither-image";
import { installImageEnv, stubImages, withMotion } from "./test-env";

let env: Awaited<ReturnType<typeof installImageEnv>>;
beforeEach(async () => {
	env = await installImageEnv(stubCanvas2d);
});
afterEach(() => env.restore());

let n = 0;
const src = (name: string) => `/photo-${name}-${++n}.png`;
const look = { cell: 4, palette: ["#000000", "#ffffff"] } as const;

const box = (view: ReturnType<typeof render>) =>
	view.container.firstElementChild as HTMLElement;
const show = (view: ReturnType<typeof render>, width = 80, height = 40) => {
	env.observers.intersect(box(view), true);
	env.observers.resize(box(view), width, height);
};

const image = (props: Partial<DitherImageProps> & { src: string }) => (
	<DitherImage alt="A mountain" {...look} {...props} />
);

describe("DitherImage accessibility and state", () => {
	test("a described picture is an image with its label", () => {
		const view = render(image({ src: src("a"), alt: "Sunset over the hills" }));
		expect(box(view)).toHaveAttribute("role", "img");
		expect(box(view)).toHaveAttribute("aria-label", "Sunset over the hills");
		expect(box(view)).not.toHaveAttribute("aria-hidden");
	});

	test("alt='' makes it decorative: hidden from assistive technology, no role", () => {
		const view = render(image({ src: src("b"), alt: "" }));
		expect(box(view)).toHaveAttribute("aria-hidden", "true");
		expect(box(view)).not.toHaveAttribute("role");
		expect(box(view)).not.toHaveAttribute("aria-label");
	});

	test("is idle and shows a placeholder grain before it comes near the viewport", async () => {
		const s = src("idle");
		const view = render(image({ src: s }));
		await act(async () => {});
		expect(box(view)).toHaveAttribute("data-state", "idle");
		expect(box(view)).not.toHaveAttribute("aria-busy");
		expect(box(view).querySelector("canvas")).toBeNull();
		expect(box(view).querySelector("div[aria-hidden=true]")).not.toBeNull();
		expect(env.images.loaded).not.toContain(s);
	});

	test("near the viewport it loads, is busy meanwhile, and ends ready with a canvas and no placeholder", async () => {
		const view = render(image({ src: src("ready") }));
		show(view);
		await waitFor(() =>
			expect(box(view)).toHaveAttribute("data-state", "ready"),
		);
		expect(box(view)).not.toHaveAttribute("aria-busy");
		expect(box(view).querySelector("canvas")).not.toBeNull();
		expect(box(view).querySelector("div[aria-hidden=true]")).toBeNull();
	});

	test("the image's own ratio is reserved once it is known; ratio wins", async () => {
		const view = render(image({ src: src("ratio") }));
		expect(box(view).style.aspectRatio).toBe("");
		show(view);
		await waitFor(() =>
			expect(box(view).style.aspectRatio).toBe(`${400 / 300} / 1`),
		);
		view.unmount();
		const fixed = render(image({ src: src("ratio2"), ratio: 2 }));
		expect(box(fixed).style.aspectRatio).toBe("2 / 1");
	});

	test("a class and a style are applied; style can override the reserved ratio", async () => {
		const view = render(
			image({
				src: src("style"),
				className: "rounded-lg",
				style: { aspectRatio: "1" },
				ratio: 2,
			}),
		);
		expect(box(view)).toHaveClass("rounded-lg", "isolate");
		expect(box(view).style.aspectRatio).toBe("1 / 1");
	});
});

describe("DitherImage failure", () => {
	beforeEach(() => {
		env.images.restore();
		env.images = stubImages({ fail: () => true });
	});

	test("an unreadable image shows the plain picture instead, decoratively", async () => {
		const s = src("broken");
		const view = render(image({ src: s }));
		show(view);
		await waitFor(() =>
			expect(box(view)).toHaveAttribute("data-state", "error"),
		);
		const img = box(view).querySelector("img") as HTMLImageElement;
		expect(img).toHaveAttribute("src", s);
		expect(img).toHaveAttribute("alt", "");
		expect(img).toHaveAttribute("aria-hidden", "true");
		expect(box(view).querySelector("canvas")).toBeNull();
		// No grain placeholder over the error state.
		expect(box(view).querySelector("div[aria-hidden=true]")).toBeNull();
	});

	test("a custom fallback replaces the plain picture", async () => {
		const view = render(
			image({ src: src("broken2"), fallback: <p>No photo</p> }),
		);
		show(view);
		await waitFor(() => expect(view.getByText("No photo")).toBeInTheDocument());
		expect(box(view).querySelector("img")).toBeNull();
	});
});

describe("DitherImage canvas handoff", () => {
	test("onCanvas receives the drawn canvas, and null when it unmounts", async () => {
		const seen: Array<HTMLCanvasElement | null> = [];
		const view = render(
			image({ src: src("export"), onCanvas: (c) => seen.push(c) }),
		);
		show(view);
		await waitFor(() => expect(seen.at(-1)).toBeInstanceOf(HTMLCanvasElement));
		view.unmount();
		expect(seen.at(-1)).toBeNull();
	});
});

describe("DitherImage resolve", () => {
	test("under reduced motion it goes straight to full detail, one frame", async () => {
		const states: string[] = [];
		const view = render(image({ src: src("reduced"), revealStep: 1 }));
		const watch = new MutationObserver(() =>
			states.push(box(view).getAttribute("data-state") ?? ""),
		);
		watch.observe(box(view), {
			attributes: true,
			attributeFilter: ["data-state"],
		});
		show(view);
		await waitFor(() =>
			expect(box(view)).toHaveAttribute("data-state", "ready"),
		);
		watch.disconnect();
		expect(states).not.toContain("resolving");
		const canvas = box(view).querySelector("canvas") as HTMLCanvasElement;
		expect(canvas.width).toBe(20);
	});

	test("with motion it steps from 16× blocks down to full detail, then settles", async () => {
		const restore = withMotion();
		try {
			const widths: number[] = [];
			const view = render(image({ src: src("steps"), revealStep: 5 }));
			const watch = new MutationObserver(() => {
				const canvas = box(view).querySelector("canvas");
				if (canvas && widths.at(-1) !== canvas.width) widths.push(canvas.width);
			});
			watch.observe(box(view), {
				attributes: true,
				childList: true,
				subtree: true,
			});
			show(view);
			await waitFor(() =>
				expect(box(view)).toHaveAttribute("data-state", "ready"),
			);
			watch.disconnect();
			await waitFor(() => {
				expect(
					(box(view).querySelector("canvas") as HTMLCanvasElement).width,
				).toBe(20);
			});
			// The coarse frames are 16, 8, 4, 2 and 1 cells of 4px over 80px.
			expect(box(view).querySelector("canvas")?.width).toBe(20);
			expect(widths.length).toBeGreaterThan(0);
			for (let i = 1; i < widths.length; i++) {
				expect(widths[i] as number).toBeGreaterThan(widths[i - 1] as number);
			}
		} finally {
			restore();
		}
	});

	test("while resolving the state is resolving and the box is busy", async () => {
		const restore = withMotion();
		try {
			const view = render(image({ src: src("busy"), revealStep: 300 }));
			show(view);
			await waitFor(() =>
				expect(box(view)).toHaveAttribute("data-state", "resolving"),
			);
			expect(box(view)).toHaveAttribute("aria-busy", "true");
			await waitFor(
				() => expect(box(view)).toHaveAttribute("data-state", "ready"),
				{
					timeout: 10_000,
				},
			);
			expect(box(view)).not.toHaveAttribute("aria-busy");
		} finally {
			restore();
		}
	});

	test("reveal={false} and resolve={false} skip the resolve; resolve wins over reveal", async () => {
		const restore = withMotion();
		try {
			const run = async (props: Partial<DitherImageProps>) => {
				const view = render(
					image({ src: src("skip"), revealStep: 400, ...props }),
				);
				show(view);
				await waitFor(() =>
					expect(box(view).querySelector("canvas")).not.toBeNull(),
				);
				const state = box(view).getAttribute("data-state");
				view.unmount();
				return state;
			};
			expect(await run({ reveal: false })).toBe("ready");
			expect(await run({ resolve: false })).toBe("ready");
			expect(await run({ reveal: false, resolve: true })).toBe("resolving");
		} finally {
			restore();
		}
	});

	test("a later redraw of the same src does not resolve again", async () => {
		const restore = withMotion();
		try {
			const s = src("again");
			const view = render(image({ src: s, revealStep: 5 }));
			show(view);
			await waitFor(() =>
				expect(box(view)).toHaveAttribute("data-state", "ready"),
			);
			await waitFor(() =>
				expect(
					(box(view).querySelector("canvas") as HTMLCanvasElement).width,
				).toBe(20),
			);
			env.observers.resize(box(view), 120, 60);
			await waitFor(() =>
				expect(
					(box(view).querySelector("canvas") as HTMLCanvasElement).width,
				).toBe(30),
			);
			expect(box(view)).toHaveAttribute("data-state", "ready");
		} finally {
			restore();
		}
	});
});
