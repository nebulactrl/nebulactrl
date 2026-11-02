import { spyOn } from "bun:test";
import { act } from "@testing-library/react";

/**
 * Test helpers for the image components. happy-dom neither loads images nor
 * fires observers, so these give a test control of both: an `Image` that
 * settles when told to, and Intersection/Resize observers a test triggers.
 */

export interface FakeImageOptions {
	width?: number;
	height?: number;
	/** `true` fails the load for this src. */
	fail?: (src: string) => boolean;
	/** Requests are recorded but never settle: for tests that do not care about the picture. */
	pending?: boolean;
}

/** Replaces the global `Image`: it settles (load or error) a microtask after `src` is set. */
export function stubImages(options: FakeImageOptions = {}) {
	const original = globalThis.Image;
	const loaded: string[] = [];
	class FakeImage {
		onload: (() => void) | null = null;
		onerror: (() => void) | null = null;
		crossOrigin: string | null = null;
		decoding = "";
		naturalWidth = options.width ?? 96;
		naturalHeight = options.height ?? 60;
		#src = "";
		get src() {
			return this.#src;
		}
		set src(value: string) {
			this.#src = value;
			loaded.push(value);
			if (options.pending) return;
			queueMicrotask(() => {
				if (options.fail?.(value)) this.onerror?.();
				else this.onload?.();
			});
		}
	}
	globalThis.Image = FakeImage as unknown as typeof Image;
	return {
		/** Every src that was requested, in order. */
		loaded,
		restore() {
			globalThis.Image = original;
		},
	};
}

type IOCallback = (entries: IntersectionObserverEntry[]) => void;

/** Replaces IntersectionObserver and ResizeObserver with ones a test drives. */
export function stubObservers() {
	const originalIO = globalThis.IntersectionObserver;
	const originalRO = globalThis.ResizeObserver;
	const io: {
		cb: IOCallback;
		el: Element;
		options: IntersectionObserverInit;
		live: boolean;
	}[] = [];
	const ro: { cb: ResizeObserverCallback; el: Element; live: boolean }[] = [];

	class FakeIO {
		#cb: IOCallback;
		#options: IntersectionObserverInit;
		constructor(cb: IOCallback, options: IntersectionObserverInit = {}) {
			this.#cb = cb;
			this.#options = options;
		}
		observe(el: Element) {
			io.push({ cb: this.#cb, el, options: this.#options, live: true });
		}
		unobserve(el: Element) {
			for (const r of io) if (r.el === el && r.cb === this.#cb) r.live = false;
		}
		disconnect() {
			for (const r of io) if (r.cb === this.#cb) r.live = false;
		}
	}
	class FakeRO {
		#cb: ResizeObserverCallback;
		constructor(cb: ResizeObserverCallback) {
			this.#cb = cb;
		}
		observe(el: Element) {
			ro.push({ cb: this.#cb, el, live: true });
		}
		unobserve(el: Element) {
			for (const r of ro) if (r.el === el && r.cb === this.#cb) r.live = false;
		}
		disconnect() {
			for (const r of ro) if (r.cb === this.#cb) r.live = false;
		}
	}
	globalThis.IntersectionObserver =
		FakeIO as unknown as typeof IntersectionObserver;
	globalThis.ResizeObserver = FakeRO as unknown as typeof ResizeObserver;

	return {
		io,
		ro,
		/** Report `el` as intersecting (or not) to every live observer watching it. */
		intersect(el: Element, isIntersecting: boolean) {
			act(() => {
				for (const r of io) {
					if (r.live && r.el === el) {
						r.cb([{ target: el, isIntersecting } as IntersectionObserverEntry]);
					}
				}
			});
		},
		/** Report a content size of `el` to every live resize observer watching it. */
		resize(el: Element, width: number, height: number) {
			act(() => {
				for (const r of ro) {
					if (r.live && r.el === el) {
						r.cb(
							[
								{
									target: el,
									contentRect: { width, height },
								} as ResizeObserverEntry,
							],
							r as unknown as ResizeObserver,
						);
					}
				}
			});
		},
		restore() {
			globalThis.IntersectionObserver = originalIO;
			globalThis.ResizeObserver = originalRO;
		},
	};
}

/** A deterministic RGBA gradient: red grows left to right, green top to bottom, opaque. */
export function gradientRgba(width: number, height: number): Uint8ClampedArray {
	const data = new Uint8ClampedArray(width * height * 4);
	for (let y = 0; y < height; y++) {
		for (let x = 0; x < width; x++) {
			const i = (y * width + x) * 4;
			data[i] = Math.round((x / Math.max(1, width - 1)) * 255);
			data[i + 1] = Math.round((y / Math.max(1, height - 1)) * 255);
			data[i + 2] = 128;
			data[i + 3] = 255;
		}
	}
	return data;
}

/** Give an element a layout box: happy-dom has none. */
export function withBox(
	el: Element,
	rect: { left?: number; top?: number; width: number; height: number },
) {
	const { left = 0, top = 0, width, height } = rect;
	return spyOn(el, "getBoundingClientRect").mockReturnValue({
		left,
		top,
		width,
		height,
		right: left + width,
		bottom: top + height,
		x: left,
		y: top,
		toJSON: () => ({}),
	} as DOMRect);
}

/** Everything the image components need from the browser, installed together. */
export async function installImageEnv(
	stubCanvas2d: typeof import("../canvas-stub").stubCanvas2d,
	options: FakeImageOptions = { width: 400, height: 300 },
) {
	const images = stubImages(options);
	const observers = stubObservers();
	const reads: Array<[number, number]> = [];
	const canvas = stubCanvas2d({
		imageData: (w, h) => {
			reads.push([w, h]);
			return gradientRgba(w, h);
		},
	});
	return {
		images,
		observers,
		canvas,
		reads,
		restore() {
			images.restore();
			observers.restore();
			canvas.restore();
		},
	};
}

/** Lift the preload's reduced-motion pin: returns the restore function. */
export function withMotion(): () => void {
	const original = window.matchMedia;
	window.matchMedia = ((query: string) => {
		if (query.replace(/\s/g, "") === "(prefers-reduced-motion:reduce)") {
			return {
				matches: false,
				media: query,
				addEventListener: () => {},
				removeEventListener: () => {},
			} as unknown as MediaQueryList;
		}
		return original.call(window, query);
	}) as typeof window.matchMedia;
	return () => {
		window.matchMedia = original;
	};
}
