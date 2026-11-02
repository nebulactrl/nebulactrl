// Test-only: gives happy-dom the pieces of layout and timing that the tab/segment
// glide code reads (element boxes, ResizeObserver, animation frames, scrollTo,
// matchMedia). Install in `beforeEach`, `restore()` in `afterEach`.
import { act } from "@testing-library/react";

interface Box {
	offsetLeft: number;
	offsetTop: number;
	offsetWidth: number;
	offsetHeight: number;
	clientWidth: number;
	scrollWidth: number;
	/** `getBoundingClientRect().left` / `.width`. */
	rectLeft: number;
	rectWidth: number;
}

type BoxProp = keyof Box;

class FakeResizeObserver {
	static all: FakeResizeObserver[] = [];
	observed = new Set<Element>();
	disconnected = false;
	constructor(private readonly callback: () => void) {
		FakeResizeObserver.all.push(this);
	}
	observe(el: Element) {
		this.observed.add(el);
	}
	unobserve(el: Element) {
		this.observed.delete(el);
	}
	disconnect() {
		this.disconnected = true;
		this.observed.clear();
	}
	fire() {
		this.callback();
	}
}

export interface LayoutEnv {
	/** Decide an element's box; unset props are 0. */
	layout: (el: Element) => Partial<Box> | undefined;
	/** `window.matchMedia("(prefers-reduced-motion: reduce)").matches`. */
	reducedMotion: boolean;
	/** Every `scrollTo` call: the element and its options. */
	scrollCalls: { el: Element; options: ScrollToOptions }[];
	/** Number of animation frames waiting. */
	pendingFrames(): number;
	/** Runs queued animation frames (and the DOM mutations that queue them) until quiet. */
	flush(): Promise<void>;
	/** Every live ResizeObserver, to trigger or to inspect what it watches. */
	resizeObservers(): FakeResizeObserver[];
	restore(): void;
}

const BOX_PROPS: BoxProp[] = [
	"offsetLeft",
	"offsetTop",
	"offsetWidth",
	"offsetHeight",
	"clientWidth",
	"scrollWidth",
];

export function installLayoutEnv(): LayoutEnv {
	const g = globalThis as Record<string, unknown>;
	const saved = {
		raf: g.requestAnimationFrame,
		caf: g.cancelAnimationFrame,
		ro: g.ResizeObserver,
		matchMedia: window.matchMedia,
		scrollTo: Element.prototype.scrollTo,
		rect: Element.prototype.getBoundingClientRect,
	};
	const protoDescriptors = BOX_PROPS.map(
		(prop) =>
			[
				prop,
				Object.getOwnPropertyDescriptor(HTMLElement.prototype, prop),
			] as const,
	);
	const elementClientWidth = Object.getOwnPropertyDescriptor(
		Element.prototype,
		"clientWidth",
	);
	const elementScrollWidth = Object.getOwnPropertyDescriptor(
		Element.prototype,
		"scrollWidth",
	);

	let nextFrame = 1;
	const frames = new Map<number, FrameRequestCallback>();
	g.requestAnimationFrame = (cb: FrameRequestCallback) => {
		const id = nextFrame++;
		frames.set(id, cb);
		return id;
	};
	g.cancelAnimationFrame = (id: number) => {
		frames.delete(id);
	};
	FakeResizeObserver.all = [];
	g.ResizeObserver = FakeResizeObserver;

	const env: LayoutEnv = {
		layout: () => undefined,
		reducedMotion: false,
		scrollCalls: [],
		pendingFrames: () => frames.size,
		resizeObservers: () => FakeResizeObserver.all,
		async flush() {
			for (let round = 0; round < 6; round++) {
				await act(async () => {
					await Promise.resolve();
					await Promise.resolve();
					const batch = [...frames.entries()];
					frames.clear();
					for (const [, cb] of batch) cb(0);
					await Promise.resolve();
				});
				if (frames.size === 0) {
					// A final turn lets MutationObservers fired by the last frame queue theirs.
					await act(async () => {
						await Promise.resolve();
					});
					if (frames.size === 0) return;
				}
			}
		},
		restore() {
			g.requestAnimationFrame = saved.raf;
			g.cancelAnimationFrame = saved.caf;
			g.ResizeObserver = saved.ro;
			window.matchMedia = saved.matchMedia;
			Element.prototype.scrollTo = saved.scrollTo;
			Element.prototype.getBoundingClientRect = saved.rect;
			for (const [prop, desc] of protoDescriptors) {
				if (desc) Object.defineProperty(HTMLElement.prototype, prop, desc);
				else
					delete (HTMLElement.prototype as unknown as Record<string, unknown>)[
						prop
					];
			}
			if (elementClientWidth)
				Object.defineProperty(
					Element.prototype,
					"clientWidth",
					elementClientWidth,
				);
			if (elementScrollWidth)
				Object.defineProperty(
					Element.prototype,
					"scrollWidth",
					elementScrollWidth,
				);
		},
	};

	for (const prop of BOX_PROPS) {
		for (const proto of [HTMLElement.prototype, Element.prototype]) {
			Object.defineProperty(proto, prop, {
				configurable: true,
				get(this: Element) {
					return env.layout(this)?.[prop] ?? 0;
				},
			});
		}
	}
	Element.prototype.getBoundingClientRect = function (this: Element) {
		const box = env.layout(this);
		const left = box?.rectLeft ?? 0;
		const width = box?.rectWidth ?? 0;
		return {
			x: left,
			y: 0,
			left,
			top: 0,
			width,
			height: 0,
			right: left + width,
			bottom: 0,
			toJSON() {},
		} as DOMRect;
	};
	Element.prototype.scrollTo = function (
		this: Element,
		options?: ScrollToOptions | number,
	) {
		env.scrollCalls.push({
			el: this,
			options: typeof options === "object" ? options : {},
		});
	} as typeof Element.prototype.scrollTo;
	window.matchMedia = ((query: string) => ({
		matches: env.reducedMotion && query.includes("prefers-reduced-motion"),
		media: query,
		onchange: null,
		addEventListener() {},
		removeEventListener() {},
		addListener() {},
		removeListener() {},
		dispatchEvent: () => false,
	})) as typeof window.matchMedia;

	return env;
}
