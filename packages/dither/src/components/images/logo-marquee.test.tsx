import { afterEach, beforeEach, describe, expect, spyOn, test } from "bun:test";
import { act, fireEvent, render, waitFor } from "@testing-library/react";
import { type CanvasStub, stubCanvas2d } from "../canvas-stub";
import {
	ditherEdgeMask,
	LogoMarquee,
	type LogoMarqueeProps,
} from "./logo-marquee";
import { stubImages, stubObservers, withMotion } from "./test-env";

let images: ReturnType<typeof stubImages>;
let observers: ReturnType<typeof stubObservers>;
let canvas: CanvasStub;
beforeEach(() => {
	// The logos are not the subject here: their requests are recorded, never settled.
	images = stubImages({ pending: true });
	observers = stubObservers();
	canvas = stubCanvas2d({
		imageData: (w, h) => new Uint8ClampedArray(w * h * 4),
	});
});
afterEach(() => {
	images.restore();
	observers.restore();
	canvas.restore();
});

const logos = [
	{ src: "/m/postgres.svg", alt: "PostgreSQL" },
	{ src: "/m/redis.svg", alt: "Redis", href: "https://redis.io" },
	{ src: "/m/nginx.svg", alt: "Nginx", color: "green" },
];

const marquee = (props: Partial<LogoMarqueeProps> = {}) => (
	<LogoMarquee logos={logos} {...props} />
);

describe("ditherEdgeMask", () => {
	test("fades both ends in four Bayer steps around a solid middle", () => {
		const mask = ditherEdgeMask(96);
		expect(mask.maskSize).toBe("96px 100%, calc(100% - 192px) 100%, 96px 100%");
		expect(mask.maskPosition).toBe("left top, 96px 0, right top");
		expect(mask.maskRepeat).toBe("no-repeat");
		const layers = String(mask.maskImage).split(/, (?=url\(|linear-gradient)/);
		expect(layers).toHaveLength(3);
		expect(layers[1]).toBe("linear-gradient(#000, #000)");
		for (const strip of [layers[0], layers[2]]) {
			const svg = decodeURIComponent(
				(strip as string).replace(/^url\("data:image\/svg\+xml,|"\)$/g, ""),
			);
			expect(svg.match(/<rect /g)).toHaveLength(4);
			expect(svg.match(/<pattern /g)).toHaveLength(4);
			// Each band is a quarter of the fade.
			expect(svg).toContain("<rect x='0' y='0' width='24' height='100%'");
		}
	});

	test("the right strip is the left one mirrored", () => {
		const mask = String(ditherEdgeMask(80).maskImage);
		const [left, , right] = mask.split(/, (?=url\(|linear-gradient)/);
		const xs = (strip: string | undefined) =>
			Array.from(
				decodeURIComponent(strip ?? "").matchAll(/<rect x='([\d.]+)'/g),
				(m) => m[1],
			);
		expect(xs(left)).toEqual(["0", "20", "40", "60"]);
		expect(xs(right)).toEqual(["60", "40", "20", "0"]);
	});

	test("the WebKit prefixed properties are the same", () => {
		const mask = ditherEdgeMask(64);
		expect(mask.WebkitMaskImage).toBe(mask.maskImage);
		expect(mask.WebkitMaskSize).toBe(mask.maskSize);
		expect(mask.WebkitMaskPosition).toBe(mask.maskPosition);
		expect(mask.WebkitMaskRepeat).toBe(mask.maskRepeat);
	});

	test("cell size changes the pattern", () => {
		expect(ditherEdgeMask(96, 2).maskImage).not.toBe(
			ditherEdgeMask(96, 4).maskImage,
		);
	});
});

describe("LogoMarquee wall", () => {
	test("is a still labelled list with one cell per logo", () => {
		const view = render(marquee({ variant: "wall", label: "Our stack" }));
		const list = view.getByRole("list", { name: "Our stack" });
		expect(view.getAllByRole("listitem")).toHaveLength(3);
		expect(list).toHaveClass("grid");
		expect(view.getByRole("img", { name: "Redis" })).toBeInTheDocument();
	});

	test("the label defaults to Logos", () => {
		const view = render(marquee({ variant: "wall" }));
		expect(view.getByRole("list", { name: "Logos" })).toBeInTheDocument();
	});

	test("a logo with an href is a link; one without is not", () => {
		const view = render(marquee({ variant: "wall" }));
		const links = view.getAllByRole("link");
		expect(links).toHaveLength(1);
		expect(links[0]).toHaveAttribute("href", "https://redis.io");
		expect(links[0]).toContainElement(view.getByRole("img", { name: "Redis" }));
		expect(links[0]).not.toHaveAttribute("tabindex");
	});

	test("className and style land on the wrapper", () => {
		const view = render(
			marquee({ variant: "wall", className: "my-8", style: { opacity: 0.5 } }),
		);
		const root = view.container.firstElementChild as HTMLElement;
		expect(root).toHaveClass("my-8");
		expect(root.style.opacity).toBe("0.5");
	});

	test("logoHeight sizes every logo", () => {
		const view = render(marquee({ variant: "wall", logoHeight: 40 }));
		for (const img of view.getAllByRole("img"))
			expect((img as HTMLElement).style.height).toBe("40px");
	});

	test("the logos load at once: a wall never waits for the viewport", async () => {
		// Images are cached by src across tests: use sources nobody loaded yet.
		const fresh = ["/w/a.svg", "/w/b.svg", "/w/c.svg"].map((src, i) => ({
			src,
			alt: `Fresh ${i}`,
		}));
		render(<LogoMarquee variant="wall" logos={fresh} />);
		await waitFor(() => expect(images.loaded).toHaveLength(3));
		expect([...images.loaded].sort()).toEqual([
			"/w/a.svg",
			"/w/b.svg",
			"/w/c.svg",
		]);
	});

	test("two logos with the same identity still render both", () => {
		const view = render(
			<LogoMarquee
				variant="wall"
				logos={[
					{ src: "/same.svg", alt: "A" },
					{ src: "/same.svg", alt: "B" },
				]}
			/>,
		);
		expect(view.getAllByRole("listitem")).toHaveLength(2);
	});
});

describe("LogoMarquee under reduced motion", () => {
	test("a marquee is a still, centred, wrapping row with no scroller or pause button", () => {
		const view = render(marquee());
		const list = view.getByRole("list", { name: "Logos" });
		expect(list).toHaveClass("flex-wrap", "justify-center");
		expect(view.container.querySelector("fieldset")).toBeNull();
		expect(view.queryByRole("button")).toBeNull();
		expect(view.getAllByRole("img")).toHaveLength(3);
	});
});

describe("LogoMarquee scrolling", () => {
	let restoreMotion: () => void;
	let animations: Array<{
		el: Element;
		frames: Keyframe[];
		timing: KeyframeAnimationOptions;
		playbackRate: number;
		cancelled: boolean;
	}>;
	let animate: ReturnType<typeof spyOn>;
	let originalOffsetWidth: PropertyDescriptor | undefined;
	let rafQueue: Array<[number, FrameRequestCallback]>;
	let raf: ReturnType<typeof spyOn>;
	let cancelRaf: ReturnType<typeof spyOn>;
	let rowWidth = 300;

	beforeEach(() => {
		restoreMotion = withMotion();
		rowWidth = 300;
		animations = [];
		animate = spyOn(Element.prototype, "animate").mockImplementation(function (
			this: Element,
			frames: Keyframe[],
			timing: KeyframeAnimationOptions,
		) {
			const animation = {
				el: this,
				frames,
				timing,
				playbackRate: 1,
				cancelled: false,
				cancel() {
					animation.cancelled = true;
				},
				finished: Promise.resolve(),
			};
			animations.push(animation);
			return animation as unknown as Animation;
		} as never);
		originalOffsetWidth = Object.getOwnPropertyDescriptor(
			HTMLElement.prototype,
			"offsetWidth",
		);
		Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
			configurable: true,
			get: () => rowWidth,
		});
		rafQueue = [];
		raf = spyOn(globalThis, "requestAnimationFrame").mockImplementation(((
			cb: FrameRequestCallback,
		) => {
			rafQueue.push([rafQueue.length + 1, cb]);
			return rafQueue.length;
		}) as never);
		cancelRaf = spyOn(globalThis, "cancelAnimationFrame").mockImplementation(
			(() => {}) as never,
		);
	});
	afterEach(() => {
		restoreMotion();
		animate.mockRestore();
		if (originalOffsetWidth)
			Object.defineProperty(
				HTMLElement.prototype,
				"offsetWidth",
				originalOffsetWidth,
			);
		raf.mockRestore();
		cancelRaf.mockRestore();
	});

	const frame = (view: ReturnType<typeof render>) =>
		view.container.querySelector("fieldset") as HTMLFieldSetElement;
	const run = (view: ReturnType<typeof render>) => {
		const row = frame(view).querySelector("ul") as HTMLElement;
		observers.resize(row, rowWidth, 40);
		return row;
	};
	/** Run queued animation frames until the rate settles. */
	const settle = (ms = 60) => {
		for (let i = 0; i < 60 && rafQueue.length > 0; i++) {
			const batch = rafQueue.splice(0);
			act(() => {
				for (const [, cb] of batch) cb(performance.now() + ms * (i + 1));
			});
		}
	};

	test("is a labelled group with the first row as the only list in the accessibility tree", () => {
		const view = render(marquee({ label: "Customers" }));
		expect(frame(view)).toHaveAttribute("aria-label", "Customers");
		const lists = frame(view).querySelectorAll("ul");
		expect(lists[0]).toHaveAttribute("aria-label", "Customers");
		expect(lists.length).toBeGreaterThanOrEqual(2);
		for (const copy of Array.from(lists).slice(1)) {
			expect(copy).toHaveAttribute("aria-hidden", "true");
			expect(copy).toHaveAttribute("inert");
		}
		// Only the first copy's logos are announced.
		expect(view.getAllByRole("img")).toHaveLength(3);
	});

	test("the copies' links are out of the tab order", () => {
		const view = render(marquee());
		const anchors = Array.from(frame(view).querySelectorAll("a"));
		expect(anchors.length).toBeGreaterThanOrEqual(2);
		expect(anchors[0]).not.toHaveAttribute("tabindex");
		for (const copy of anchors.slice(1))
			expect(copy).toHaveAttribute("tabindex", "-1");
	});

	test("repeats the row until it fills the width and slides into one more", () => {
		const view = render(marquee());
		run(view);
		// The frame is 0 wide until measured: two copies.
		expect(frame(view).querySelectorAll("ul")).toHaveLength(2);
		observers.resize(frame(view), 1000, 40);
		// 1000 / 300 → 4 + 1 copies.
		expect(frame(view).querySelectorAll("ul")).toHaveLength(5);
	});

	test("one loop of the row, in a straight line, at the given speed", () => {
		const view = render(marquee({ speed: 50 }));
		run(view);
		const loop = animations.at(-1);
		expect(loop?.frames).toEqual([
			{ transform: "translateX(0)" },
			{ transform: "translateX(-300px)" },
		]);
		expect(loop?.timing).toMatchObject({
			duration: 6000,
			iterations: Number.POSITIVE_INFINITY,
			easing: "linear",
			direction: "normal",
		});
		expect(loop?.el).toBe(
			frame(view).querySelector(".will-change-transform") as Element,
		);
	});

	test("direction='right' plays the loop backwards", () => {
		const view = render(marquee({ direction: "right" }));
		run(view);
		expect(animations.at(-1)?.timing.direction).toBe("reverse");
	});

	test("a changed speed starts a new loop and cancels the old one", () => {
		const view = render(marquee({ speed: 50 }));
		run(view);
		const first = animations.at(-1);
		view.rerender(marquee({ speed: 100 }));
		const second = animations.at(-1);
		expect(second).not.toBe(first);
		expect(first?.cancelled).toBe(true);
		expect(second?.timing.duration).toBe(3000);
	});

	test("a nonsense speed is held to at least 1px/s", () => {
		const view = render(marquee({ speed: 0 }));
		run(view);
		expect(animations.at(-1)?.timing.duration).toBe(300_000);
	});

	test("nothing is animated before the row has a width", () => {
		rowWidth = 0;
		render(marquee());
		expect(animations).toHaveLength(0);
	});

	test("unmounting cancels the loop", () => {
		const view = render(marquee());
		run(view);
		const loop = animations.at(-1);
		view.unmount();
		expect(loop?.cancelled).toBe(true);
	});

	test("the loop is held while the marquee is off screen and runs once it is on screen", () => {
		const view = render(marquee());
		run(view);
		const loop = animations.at(-1);
		expect(loop?.playbackRate).toBe(0);
		observers.intersect(frame(view), true);
		settle();
		expect(loop?.playbackRate).toBe(1);
		observers.intersect(frame(view), false);
		settle();
		expect(loop?.playbackRate).toBe(0);
	});

	test("the rate eases rather than jumping", () => {
		const view = render(marquee());
		run(view);
		const loop = animations.at(-1);
		observers.intersect(frame(view), true);
		const seen: number[] = [];
		for (let i = 0; i < 40 && rafQueue.length > 0; i++) {
			const batch = rafQueue.splice(0);
			act(() => {
				for (const [, cb] of batch) cb(performance.now() + 30 * (i + 1));
			});
			seen.push(loop?.playbackRate ?? -1);
		}
		expect(seen.some((rate) => rate > 0 && rate < 1)).toBe(true);
		expect(seen.at(-1)).toBe(1);
	});

	test("hovering holds it, leaving lets it run; touch does not count as hover", () => {
		const view = render(marquee());
		run(view);
		const loop = animations.at(-1);
		observers.intersect(frame(view), true);
		settle();
		expect(loop?.playbackRate).toBe(1);
		fireEvent.pointerEnter(frame(view), { pointerType: "mouse" });
		settle();
		expect(loop?.playbackRate).toBe(0);
		fireEvent.pointerLeave(frame(view));
		settle();
		expect(loop?.playbackRate).toBe(1);
		fireEvent.pointerEnter(frame(view), { pointerType: "touch" });
		settle();
		expect(loop?.playbackRate).toBe(1);
	});

	test("keyboard focus holds it until focus leaves the marquee", () => {
		const view = render(
			<>
				<button type="button">outside</button>
				{marquee()}
			</>,
		);
		run(view);
		const loop = animations.at(-1);
		observers.intersect(frame(view), true);
		settle();
		const link = view.getAllByRole("link")[0] as HTMLElement;
		fireEvent.focus(link);
		settle();
		expect(loop?.playbackRate).toBe(0);
		// Moving focus to another logo inside keeps it held.
		fireEvent.blur(link, {
			relatedTarget: frame(view).querySelector("button"),
		});
		settle();
		expect(loop?.playbackRate).toBe(0);
		fireEvent.blur(link, { relatedTarget: view.getByText("outside") });
		settle();
		expect(loop?.playbackRate).toBe(1);
	});

	test("the pause button toggles, and says what it will do", () => {
		const view = render(marquee());
		run(view);
		const loop = animations.at(-1);
		observers.intersect(frame(view), true);
		settle();
		const pause = view.getByRole("button", { name: "Pause logo scroll" });
		expect(pause).toHaveAttribute("aria-pressed", "false");
		fireEvent.click(pause);
		settle();
		expect(loop?.playbackRate).toBe(0);
		const play = view.getByRole("button", { name: "Play logo scroll" });
		expect(play).toHaveAttribute("aria-pressed", "true");
		fireEvent.click(play);
		settle();
		expect(loop?.playbackRate).toBe(1);
		expect(
			view.getByRole("button", { name: "Pause logo scroll" }),
		).toBeInTheDocument();
	});

	test("pauseButton={false} has no button", () => {
		const view = render(marquee({ pauseButton: false }));
		expect(view.queryByRole("button")).toBeNull();
	});

	test("a loop created while paused starts held", () => {
		const view = render(marquee());
		run(view);
		observers.intersect(frame(view), true);
		settle();
		fireEvent.click(view.getByRole("button", { name: "Pause logo scroll" }));
		settle();
		view.rerender(marquee({ speed: 90 }));
		expect(animations.at(-1)?.playbackRate).toBe(0);
	});

	test("the ends fade softly by default, in steps of dither, or not at all", () => {
		const fadeOf = (props: Partial<LogoMarqueeProps>) => {
			const view = render(marquee(props));
			const wrapper = frame(view).querySelector(":scope > div") as HTMLElement;
			const out = {
				image: wrapper.style.maskImage,
				size: wrapper.style.maskSize,
			};
			view.unmount();
			return out;
		};
		const soft = fadeOf({});
		expect(soft.image).toContain(
			"linear-gradient(to right, transparent, #000 96px",
		);
		const dither = fadeOf({ fade: "dither", fadeWidth: 60 });
		expect(dither.image).toContain("data:image/svg+xml");
		expect(dither.size).toContain("60px 100%");
		const none = fadeOf({ fade: "none" });
		expect(none.image).toBe("");
		expect(fadeOf({ fadeWidth: 40 }).image).toContain("#000 40px");
	});

	test("logoHeight and gap lay the row out", () => {
		const view = render(marquee({ logoHeight: 36, gap: 20 }));
		const item = frame(view).querySelector("li") as HTMLElement;
		expect(item.style.marginRight).toBe("20px");
		expect((view.getAllByRole("img")[0] as HTMLElement).style.height).toBe(
			"36px",
		);
	});

	test("the wall variant ignores motion", () => {
		const view = render(marquee({ variant: "wall" }));
		expect(frame(view)).toBeNull();
		expect(animations).toHaveLength(0);
	});

	test("className and style land on the frame", () => {
		const view = render(
			marquee({ className: "my-4", style: { opacity: 0.8 } }),
		);
		expect(frame(view)).toHaveClass("my-4");
		expect(frame(view).style.opacity).toBe("0.8");
	});
});
