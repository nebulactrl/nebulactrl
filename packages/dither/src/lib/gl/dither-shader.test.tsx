import {
	afterAll,
	afterEach,
	beforeEach,
	describe,
	expect,
	mock,
	spyOn,
	test,
} from "bun:test";
import { act, cleanup, render } from "@testing-library/react";
import type { DitherShaderProps } from "./dither-shader";
import { computeLayout } from "./layout";

// motion.ts caches the reduced-motion media query the first time anything asks
// for it, and bun shares modules across test files, so a shared copy may be
// stuck on another file's answer. This file gives DitherShader a private copy
// of motion.ts (and its own frame scheduler), and puts the real one back after.
const copyTag = "dither-shader-test";
const realMotion = await import("../motion");
const ownMotion: typeof realMotion = await import(`../motion?${copyTag}`);
mock.module("../motion", () => ({ ...ownMotion }));
const { DitherShader }: typeof import("./dither-shader") = await import(
	`./dither-shader?${copyTag}`
);
afterAll(() => {
	mock.module("../motion", () => ({ ...realMotion }));
});

// -- a WebGL2 that records uniform uploads -------------------------------------

interface FakeGl {
	gl: WebGL2RenderingContext;
	/** Values uploaded to a uniform, in order. */
	uploads(type: string, name: string): unknown[][];
	/** Frames drawn (two passes each). */
	draws(): number;
	programs(): number;
	calls(name: string): number;
}

const CONSTANT = /^[A-Z][A-Z0-9_]*$/;
function makeGl(canvas: HTMLCanvasElement): FakeGl {
	const log: { name: string; args: unknown[] }[] = [];
	let id = 0;
	let lost = false;
	// Losing and restoring through the extension raise the canvas events, as a browser does.
	const extension = {
		loseContext() {
			log.push({ name: "loseContext", args: [] });
			lost = true;
			canvas.dispatchEvent(new Event("webglcontextlost", { cancelable: true }));
		},
		restoreContext() {
			log.push({ name: "restoreContext", args: [] });
			lost = false;
			canvas.dispatchEvent(new Event("webglcontextrestored"));
		},
	};
	const gl = new Proxy({} as Record<string, unknown>, {
		get(_t, prop) {
			if (typeof prop !== "string") return undefined;
			if (CONSTANT.test(prop)) return prop === "TEXTURE0" ? 33984 : 1;
			return (...args: unknown[]) => {
				log.push({ name: prop, args });
				switch (prop) {
					case "isContextLost":
						return lost;
					case "getShaderParameter":
					case "getProgramParameter":
						return true;
					case "checkFramebufferStatus":
						return 1;
					case "getUniformLocation":
						return { uniform: args[1] };
					case "getExtension":
						return args[0] === "WEBGL_lose_context" && !lost ? extension : null;
					case "getShaderInfoLog":
					case "getProgramInfoLog":
						return "";
					case "createShader":
					case "createProgram":
					case "createTexture":
					case "createFramebuffer":
						return { id: ++id };
					default:
						return undefined;
				}
			};
		},
	}) as unknown as WebGL2RenderingContext;
	return {
		gl,
		uploads: (type, name) =>
			log
				.filter(
					(c) =>
						c.name === type &&
						(c.args[0] as { uniform?: string } | null)?.uniform === name,
				)
				.map((c) => c.args.slice(1)),
		/** Frames drawn: each is two passes. */
		draws: () => log.filter((c) => c.name === "drawArrays").length / 2,
		programs: () => log.filter((c) => c.name === "createProgram").length,
		calls: (name) => log.filter((c) => c.name === name).length,
	};
}

// -- the browser around it ----------------------------------------------------

const REDUCED = "(prefers-reduced-motion: reduce)";

// One list object for every test: the private motion copy caches the first it sees.
const reducedState = { value: true };
const reducedListeners = new Set<() => void>();
const reducedList = {
	get matches() {
		return reducedState.value;
	},
	media: "(prefers-reduced-motion: reduce)",
	addEventListener: (_: string, fn: () => void) => reducedListeners.add(fn),
	removeEventListener: (_: string, fn: () => void) =>
		reducedListeners.delete(fn),
} as unknown as MediaQueryList;

// The private motion copy shares one IntersectionObserver per margin for the
// whole file, so the records must outlive a single test too.
const observerRecords: Env["observers"] = [];
class FakeIntersectionObserver {
	record: Env["observers"][number];
	constructor(
		callback: IntersectionObserverCallback,
		options?: IntersectionObserverInit,
	) {
		this.record = { margin: options?.rootMargin, callback, targets: [] };
		observerRecords.push(this.record);
	}
	observe(el: Element) {
		this.record.targets.push(el);
	}
	unobserve(el: Element) {
		this.record.targets = this.record.targets.filter((t) => t !== el);
	}
	disconnect() {}
}

interface Env {
	gls: FakeGl[];
	/** Box of the host in the window and its layout size. */
	box: { width: number; height: number; top: number };
	/** What the host reports as its layout size (`clientWidth` / `clientHeight`). */
	client: { width: number; height: number };
	resizeCallbacks: (() => void)[];
	observers: {
		margin: string | undefined;
		callback: IntersectionObserverCallback;
		targets: Element[];
	}[];
	timers: { fn: () => void; ms: number; cleared: boolean }[];
	frame(ms: number): void;
	rafPending(): number;
	reportIntersection(margin: string, isIntersecting: boolean): void;
	setReducedMotion(reduce: boolean): void;
	restore(): void;
}

function installEnv(): Env {
	const g = globalThis as unknown as Record<string, unknown> & {
		requestAnimationFrame: typeof requestAnimationFrame;
		cancelAnimationFrame: typeof cancelAnimationFrame;
		ResizeObserver: typeof ResizeObserver;
		IntersectionObserver: typeof IntersectionObserver;
	};
	const restores: (() => void)[] = [];
	const swap = <K extends keyof typeof g>(key: K, value: (typeof g)[K]) => {
		const real = g[key];
		g[key] = value;
		restores.push(() => {
			g[key] = real;
		});
	};

	const env: Env = {
		gls: [],
		box: { width: 120, height: 60, top: 100 },
		client: { width: 120, height: 60 },
		resizeCallbacks: [],
		observers: [],
		timers: [],
		frame: () => {},
		rafPending: () => 0,
		reportIntersection: () => {},
		setReducedMotion: () => {},
		restore: () => {
			for (const r of restores.reverse()) r();
		},
	};

	// WebGL2 context per canvas.
	const contexts = new WeakMap<object, FakeGl>();
	const getContext = spyOn(
		HTMLCanvasElement.prototype,
		"getContext",
	).mockImplementation(function (this: HTMLCanvasElement) {
		let fake = contexts.get(this);
		if (!fake) {
			fake = makeGl(this);
			contexts.set(this, fake);
			env.gls.push(fake);
		}
		return fake.gl;
	} as never);
	restores.push(() => getContext.mockRestore());

	// Host geometry.
	for (const [prop, read] of [
		["clientWidth", () => env.client.width],
		["clientHeight", () => env.client.height],
	] as const) {
		const original = Object.getOwnPropertyDescriptor(
			HTMLElement.prototype,
			prop,
		);
		Object.defineProperty(HTMLElement.prototype, prop, {
			configurable: true,
			get: read,
		});
		restores.push(() => {
			if (original)
				Object.defineProperty(HTMLElement.prototype, prop, original);
			else
				delete (HTMLElement.prototype as unknown as Record<string, unknown>)[
					prop
				];
		});
	}
	const rect = spyOn(
		Element.prototype,
		"getBoundingClientRect",
	).mockImplementation((() => ({
		left: 0,
		top: env.box.top,
		right: env.box.width,
		bottom: env.box.top + env.box.height,
		width: env.box.width,
		height: env.box.height,
		x: 0,
		y: env.box.top,
		toJSON() {},
	})) as never);
	restores.push(() => rect.mockRestore());

	// Observers.
	swap(
		"ResizeObserver",
		class {
			constructor(callback: () => void) {
				env.resizeCallbacks.push(callback);
			}
			observe() {}
			unobserve() {}
			disconnect() {}
		} as unknown as typeof ResizeObserver,
	);
	swap(
		"IntersectionObserver",
		FakeIntersectionObserver as unknown as typeof IntersectionObserver,
	);
	env.observers = observerRecords;
	env.reportIntersection = (margin, isIntersecting) => {
		act(() => {
			for (const observer of env.observers) {
				if (observer.margin !== margin) continue;
				observer.callback(
					observer.targets.map(
						(target) =>
							({ target, isIntersecting }) as IntersectionObserverEntry,
					),
					{} as IntersectionObserver,
				);
			}
		});
	};

	// Frames.
	let now = 0;
	let next = 1;
	const queue = new Map<number, FrameRequestCallback>();
	swap("requestAnimationFrame", ((cb: FrameRequestCallback) => {
		queue.set(next, cb);
		return next++;
	}) as typeof requestAnimationFrame);
	swap("cancelAnimationFrame", ((h: number) => {
		queue.delete(h);
	}) as typeof cancelAnimationFrame);
	env.frame = (ms) => {
		now += ms;
		const pending = [...queue.values()];
		queue.clear();
		act(() => {
			for (const cb of pending) cb(now);
		});
	};
	env.rafPending = () => queue.size;

	// Only the shader's 1.2 s "waiting for a frame" timer is captured.
	const realSetTimeout = window.setTimeout.bind(window);
	const realClearTimeout = window.clearTimeout.bind(window);
	const setTimeoutSpy = spyOn(window, "setTimeout").mockImplementation(((
		fn: () => void,
		ms?: number,
	) => {
		if (ms !== 1200) return realSetTimeout(fn, ms);
		env.timers.push({ fn, ms, cleared: false });
		return -env.timers.length;
	}) as never);
	const clearTimeoutSpy = spyOn(window, "clearTimeout").mockImplementation(((
		handle?: number,
	) => {
		if (typeof handle === "number" && handle < 0) {
			const timer = env.timers[-handle - 1];
			if (timer) timer.cleared = true;
			return;
		}
		realClearTimeout(handle);
	}) as never);
	restores.push(
		() => setTimeoutSpy.mockRestore(),
		() => clearTimeoutSpy.mockRestore(),
	);

	// Reduced motion: the shared preload pins it on, tests may turn it off.
	const originalMatchMedia = window.matchMedia;
	window.matchMedia = ((query: string) =>
		query.replace(/\s/g, "") === REDUCED.replace(/\s/g, "")
			? reducedList
			: originalMatchMedia.call(window, query)) as typeof window.matchMedia;
	restores.push(() => {
		window.matchMedia = originalMatchMedia;
		reducedState.value = true;
	});
	env.setReducedMotion = (value) => {
		reducedState.value = value;
		act(() => {
			for (const fn of reducedListeners) fn();
		});
	};
	return env;
}

let env: Env;
beforeEach(() => {
	env = installEnv();
});
afterEach(() => {
	cleanup();
	env.restore();
});

const FIELD = "float field(vec2 uv, float t) { return uv.x; }";
const NEAR = "200px 0px";
const HOST = '[data-slot="dither-shader"]';

const mount = (props: Partial<DitherShaderProps> = {}) =>
	render(<DitherShader field={FIELD} {...props} />);
const host = (view: ReturnType<typeof mount>) =>
	view.container.querySelector(HOST) as HTMLElement;

/** The ResizeObserver's first report measures at once; later ones are debounced. */
function measureHost() {
	act(() => {
		for (const cb of env.resizeCallbacks) cb();
	});
}

const lastGl = () => env.gls[env.gls.length - 1] as FakeGl;
const lastUpload = (type: string, name: string) => {
	const uploads = lastGl().uploads(type, name);
	return uploads[uploads.length - 1];
};

describe("DitherShader without WebGL2", () => {
	test("shows the palette gradient fallback and no canvas content", () => {
		const getContext = spyOn(
			HTMLCanvasElement.prototype,
			"getContext",
		).mockImplementation((() => null) as never);
		try {
			const view = mount({ palette: ["#000000", "#ff0000"] });
			expect(host(view)).toHaveAttribute("data-state", "fallback");
			const gradient = host(view).querySelector(
				"[style*='linear-gradient']",
			) as HTMLElement;
			expect(gradient.style.backgroundImage).toBe(
				"linear-gradient(to top, #000000, #ff0000)",
			);
		} finally {
			getContext.mockRestore();
		}
	});

	test("a custom fallback replaces the gradient", () => {
		const getContext = spyOn(
			HTMLCanvasElement.prototype,
			"getContext",
		).mockImplementation((() => null) as never);
		try {
			const view = mount({ fallback: <p>no webgl here</p> });
			expect(view.getByText("no webgl here")).toBeInTheDocument();
			expect(host(view).querySelector("[style*='linear-gradient']")).toBeNull();
		} finally {
			getContext.mockRestore();
		}
	});
});

describe("DitherShader host", () => {
	test("is hidden from assistive tech by default and is not when asked", () => {
		const hidden = mount();
		expect(host(hidden)).toHaveAttribute("aria-hidden", "true");
		cleanup();
		const shown = mount({ "aria-hidden": false });
		expect(host(shown)).toHaveAttribute("aria-hidden", "false");
	});

	test("passes className and style through to the host", () => {
		const view = mount({ className: "hero-bg", style: { height: 40 } });
		expect(host(view)).toHaveClass("hero-bg");
		expect(host(view).style.height).toBe("40px");
	});
});

describe("DitherShader drawing", () => {
	test("is pending until the host has a size, then draws and sizes the canvas to the grid", () => {
		env.client.width = 0;
		env.client.height = 0;
		const view = mount();
		expect(host(view)).toHaveAttribute("data-state", "pending");
		expect(
			view.container.querySelector("[style*='linear-gradient']"),
		).toBeNull();
		expect(lastGl().draws()).toBe(0);

		env.client.width = 120;
		env.client.height = 60;
		measureHost();
		const layout = computeLayout(120, 60, 3, window.devicePixelRatio, "square");
		const canvas = view.container.querySelector("canvas") as HTMLCanvasElement;
		expect(canvas.width).toBe(layout.canvasW);
		expect(canvas.height).toBe(layout.canvasH);
		expect(canvas.style.width).toBe(`${layout.cssW}px`);
		expect(lastGl().draws()).toBeGreaterThan(0);
		// Drawn: the host leaves "pending" (reduced motion in tests: a still frame).
		expect(host(view)).toHaveAttribute("data-state", "static");
	});

	test("a host with no size draws nothing yet", () => {
		env.client.width = 0;
		env.client.height = 0;
		const view = mount();
		measureHost();
		expect(lastGl()?.draws() ?? 0).toBe(0);
		expect(host(view)).toHaveAttribute("data-state", "pending");
	});

	test("the cell prop sets the grid; --dither-cell is the default", () => {
		const view = mount({ cell: 6 });
		measureHost();
		const canvas = view.container.querySelector("canvas") as HTMLCanvasElement;
		const coarse = computeLayout(120, 60, 6, window.devicePixelRatio, "square");
		expect(canvas.width).toBe(coarse.canvasW);
		cleanup();

		const styled = mount({ style: { "--dither-cell": "4px" } as never });
		measureHost();
		const fine = computeLayout(120, 60, 4, window.devicePixelRatio, "square");
		expect(
			(styled.container.querySelector("canvas") as HTMLCanvasElement).width,
		).toBe(fine.canvasW);
	});

	test("a changed cell re-lays-out and redraws", () => {
		const view = mount({ cell: 6 });
		measureHost();
		const draws = lastGl().draws();
		view.rerender(<DitherShader field={FIELD} cell={3} />);
		const canvas = view.container.querySelector("canvas") as HTMLCanvasElement;
		expect(canvas.width).toBe(
			computeLayout(120, 60, 3, window.devicePixelRatio, "square").canvasW,
		);
		expect(lastGl().draws()).toBeGreaterThan(draws);
	});

	test("uploads the matrix, levels and shape it was given", () => {
		mount({ matrix: "bayer8", levels: 9, shape: "dot" });
		measureHost();
		expect(lastUpload("uniform1f", "u_n")).toEqual([8]);
		expect(lastUpload("uniform1f", "u_levels")).toEqual([9]);
		expect(lastUpload("uniform1f", "u_shape")).toEqual([1]);
	});

	test("the `n` prop picks the Bayer size and `matrix` wins over it", () => {
		mount({ n: 2 });
		measureHost();
		expect(lastUpload("uniform1f", "u_n")).toEqual([2]);
		cleanup();
		mount({ n: 2, matrix: "blue-noise" });
		measureHost();
		expect(lastUpload("uniform1f", "u_n")).toEqual([64]);
	});

	test('levels="full" uses every tone the matrix draws', () => {
		mount({ matrix: "bayer2", levels: "full" });
		measureHost();
		// bayer2 draws five tones.
		expect(lastUpload("uniform1f", "u_levels")).toEqual([5]);
	});

	test("user uniforms reach the field and follow prop changes without recompiling", () => {
		const view = mount({ uniforms: { gain: 0.5 } });
		measureHost();
		expect(lastUpload("uniform1f", "gain")).toEqual([0.5]);
		const programs = lastGl().programs();

		view.rerender(<DitherShader field={FIELD} uniforms={{ gain: 0.9 }} />);
		expect(lastUpload("uniform1f", "gain")).toEqual([0.9]);
		expect(lastGl().programs()).toBe(programs);
	});

	test("a new field source recompiles the program", () => {
		const view = mount();
		measureHost();
		const programs = lastGl().programs();
		view.rerender(
			<DitherShader field="float field(vec2 uv, float t) { return uv.y; }" />,
		);
		expect(lastGl().programs()).toBe(programs + 1);
	});

	test("a palette is resolved to colours and uploaded", () => {
		mount({ palette: ["#000000", "#ff0000", "#ffffff"] });
		measureHost();
		expect(lastUpload("uniform1i", "u_palN")).toEqual([3]);
		const palette = lastUpload("uniform4fv", "u_pal")?.[0] as Float32Array;
		expect(Array.from(palette.slice(4, 8))).toEqual([1, 0, 0, 1]);
	});

	test("a texture is uploaded and bound for the field", () => {
		const data = new Uint8Array([0, 255, 255, 0]);
		mount({
			field: "float field(vec2 uv, float t) { return texture(u_mask, uv).r; }",
			textures: { u_mask: { width: 2, height: 2, data } },
		});
		measureHost();
		expect(lastUpload("uniform1i", "u_mask")).toEqual([1]);
	});
});

describe("DitherShader motion", () => {
	beforeEach(() => env.setReducedMotion(false));

	test("runs once the host is near the viewport and time follows the clock", () => {
		const view = mount();
		measureHost();
		// Drawn once, but nothing has said the element is on screen yet.
		expect(host(view)).toHaveAttribute("data-state", "paused");
		expect(env.rafPending()).toBe(0);

		env.reportIntersection("64px 0px", true);
		expect(host(view)).toHaveAttribute("data-state", "running");
		env.frame(16);
		env.frame(50);
		const time = lastUpload("uniform1f", "u_time")?.[0] as number;
		expect(time).toBeCloseTo(0.05, 5);
	});

	test("speed scales the clock", () => {
		mount({ speed: 2 });
		measureHost();
		env.reportIntersection("64px 0px", true);
		env.frame(16);
		env.frame(50);
		expect(lastUpload("uniform1f", "u_time")?.[0] as number).toBeCloseTo(
			0.1,
			5,
		);
	});

	test("fps caps how often it draws", () => {
		mount({ fps: 10 });
		measureHost();
		env.reportIntersection("64px 0px", true);
		env.frame(16);
		const before = lastGl().draws();
		env.frame(50);
		expect(lastGl().draws()).toBe(before);
		env.frame(60);
		expect(lastGl().draws()).toBe(before + 1);
	});

	test("leaving the viewport pauses the loop", () => {
		const view = mount();
		measureHost();
		env.reportIntersection("64px 0px", true);
		expect(env.rafPending()).toBe(1);
		env.reportIntersection("64px 0px", false);
		expect(host(view)).toHaveAttribute("data-state", "paused");
		expect(env.rafPending()).toBe(0);
	});

	test("animate={false} holds a static frame at staticTime", () => {
		const view = mount({ animate: false, staticTime: 3 });
		measureHost();
		env.reportIntersection("64px 0px", true);
		expect(host(view)).toHaveAttribute("data-state", "static");
		expect(env.rafPending()).toBe(0);
		expect(lastUpload("uniform1f", "u_time")).toEqual([3]);
	});

	test("stillTime wins over staticTime", () => {
		mount({ animate: false, staticTime: 3, stillTime: 7 });
		measureHost();
		expect(lastUpload("uniform1f", "u_time")).toEqual([7]);
	});

	test("reduced motion draws one still frame at staticTime and never ticks", () => {
		env.setReducedMotion(true);
		const view = mount({ staticTime: 2 });
		measureHost();
		env.reportIntersection("64px 0px", true);
		expect(host(view)).toHaveAttribute("data-state", "static");
		expect(env.rafPending()).toBe(0);
		expect(lastUpload("uniform1f", "u_time")).toEqual([2]);
	});
});

describe("DitherShader pointer", () => {
	beforeEach(() => env.setReducedMotion(false));

	const pointer = () => lastUpload("uniform2f", "u_pointer") as number[];
	function run() {
		env.reportIntersection("64px 0px", true);
		env.frame(16);
	}
	const move = (clientX: number, clientY: number) =>
		act(() => {
			window.dispatchEvent(
				new PointerEvent("pointermove", { clientX, clientY }),
			);
		});

	test("starts at the rest point and the field sees it", () => {
		mount({ pointer: true, pointerRest: [0.2, 0.8] });
		measureHost();
		run();
		expect(pointer()).toEqual([0.2, 0.8]);
	});

	test("eases toward the pointer with y flipped to point up", () => {
		mount({ pointer: true });
		measureHost();
		run();
		// Host spans x 0..120 and y 100..160: the point is 3/4 across, 1/4 down
		// from the top, so y (up) is 0.75.
		move(90, 115);
		env.frame(50);
		const [x, y] = pointer() as [number, number];
		expect(x).toBeGreaterThan(0.5);
		expect(x).toBeLessThan(0.75);
		expect(y).toBeGreaterThan(0.5);
		expect(y).toBeLessThan(0.75);
		for (let i = 0; i < 40; i++) env.frame(50);
		const [fx, fy] = pointer() as [number, number];
		expect(fx).toBeCloseTo(0.75, 3);
		expect(fy).toBeCloseTo(0.75, 3);
	});

	test("a point outside the host is clamped to its edge", () => {
		mount({ pointer: true });
		measureHost();
		run();
		move(1000, -500);
		for (let i = 0; i < 60; i++) env.frame(50);
		const [x, y] = pointer() as [number, number];
		expect(x).toBeCloseTo(1, 3);
		expect(y).toBeCloseTo(1, 3);
	});

	test("leaving the page, blur and a lifted touch send it home; a lifted mouse does not", () => {
		mount({ pointer: true, pointerRest: [0.5, 0.5] });
		measureHost();
		run();
		const settle = () => {
			for (let i = 0; i < 60; i++) env.frame(50);
			return pointer() as [number, number];
		};
		const away = () => {
			move(0, 100);
			return settle();
		};
		expect(away()[0]).toBeCloseTo(0, 3);

		act(() => {
			window.dispatchEvent(new Event("blur"));
		});
		expect(settle()[0]).toBeCloseTo(0.5, 3);

		away();
		act(() => {
			document.documentElement.dispatchEvent(new Event("mouseleave"));
		});
		expect(settle()[0]).toBeCloseTo(0.5, 3);

		away();
		act(() => {
			document.documentElement.dispatchEvent(new Event("pointerleave"));
		});
		expect(settle()[0]).toBeCloseTo(0.5, 3);

		away();
		act(() => {
			window.dispatchEvent(
				new PointerEvent("pointerup", { pointerType: "mouse" }),
			);
		});
		expect(settle()[0]).toBeCloseTo(0, 3);

		act(() => {
			window.dispatchEvent(
				new PointerEvent("pointerup", { pointerType: "touch" }),
			);
		});
		expect(settle()[0]).toBeCloseTo(0.5, 3);

		away();
		act(() => {
			window.dispatchEvent(
				new PointerEvent("pointercancel", { pointerType: "touch" }),
			);
		});
		expect(settle()[0]).toBeCloseTo(0.5, 3);
	});

	test("a new rest point is followed only while the pointer is away", () => {
		const view = mount({ pointer: true, pointerRest: [0.5, 0.5] });
		measureHost();
		run();
		view.rerender(
			<DitherShader field={FIELD} pointer pointerRest={[0.1, 0.1]} />,
		);
		for (let i = 0; i < 60; i++) env.frame(50);
		expect(pointer()[0]).toBeCloseTo(0.1, 3);

		move(60, 130);
		for (let i = 0; i < 60; i++) env.frame(50);
		view.rerender(
			<DitherShader field={FIELD} pointer pointerRest={[0.9, 0.9]} />,
		);
		for (let i = 0; i < 60; i++) env.frame(50);
		// The pointer is on the host: the rest point does not move it.
		expect(pointer()[0]).toBeCloseTo(0.5, 3);
	});

	test("without the pointer prop the host ignores the mouse", () => {
		mount({ pointer: false });
		measureHost();
		run();
		move(90, 115);
		for (let i = 0; i < 20; i++) env.frame(50);
		expect(pointer()).toEqual([0.5, 0.5]);
	});

	test("a collapsed host ignores pointer moves", () => {
		mount({ pointer: true });
		measureHost();
		run();
		env.box.width = 0;
		env.box.height = 0;
		move(90, 115);
		env.box.width = 120;
		env.box.height = 60;
		for (let i = 0; i < 20; i++) env.frame(50);
		expect(pointer()).toEqual([0.5, 0.5]);
	});

	test("listeners are removed on unmount", () => {
		const view = mount({ pointer: true });
		const removed: string[] = [];
		const spy = spyOn(window, "removeEventListener").mockImplementation(((
			type: string,
		) => {
			removed.push(type);
		}) as never);
		try {
			view.unmount();
			expect(removed).toEqual(
				expect.arrayContaining([
					"pointermove",
					"pointerup",
					"pointercancel",
					"blur",
				]),
			);
		} finally {
			spy.mockRestore();
		}
	});
});

describe("DitherShader waiting for a frame and the context budget", () => {
	/** Move the host out of (or into) the viewport and its 200px margin. */
	const scroll = (onScreen: boolean) => {
		env.reportIntersection(NEAR, onScreen);
		env.reportIntersection("0px", onScreen);
	};

	test("the fallback appears only after waiting over a second with no frame", () => {
		env.client.width = 0;
		env.client.height = 0;
		const view = mount({ fallback: <p>still loading</p> });
		expect(host(view)).toHaveAttribute("data-state", "pending");
		expect(view.queryByText("still loading")).toBeNull();
		const timer = env.timers.find((t) => !t.cleared);
		expect(timer?.ms).toBe(1200);

		act(() => timer?.fn());
		expect(host(view)).toHaveAttribute("data-state", "fallback");
		expect(view.getByText("still loading")).toBeInTheDocument();
	});

	test("a first frame cancels the wait", () => {
		const view = mount({ fallback: <p>still loading</p> });
		const timer = env.timers[0];
		measureHost();
		expect(host(view)).not.toHaveAttribute("data-state", "pending");
		expect(timer?.cleared).toBe(true);
		expect(view.queryByText("still loading")).toBeNull();
	});

	test("a shader far from the viewport holds no context and shows its fallback", () => {
		env.box.top = 5000;
		const view = mount({ fallback: <p>far away</p> });
		expect(env.gls).toHaveLength(0);
		expect(host(view)).toHaveAttribute("data-state", "fallback");
		expect(view.getByText("far away")).toBeInTheDocument();
	});

	test("coming near takes a context and replaces the fallback with the shader", () => {
		env.box.top = 5000;
		const view = mount({ fallback: <p>far away</p> });
		expect(env.gls).toHaveLength(0);

		env.box.top = 100;
		scroll(true);
		expect(env.gls).toHaveLength(1);
		expect(lastGl().draws()).toBeGreaterThan(0);
		expect(host(view)).toHaveAttribute("data-state", "static");
		expect(view.queryByText("far away")).toBeNull();
	});

	test("scrolling far away hands the context back and shows the fallback", () => {
		const view = mount({ fallback: <p>far away</p> });
		expect(host(view)).toHaveAttribute("data-state", "static");
		expect(view.queryByText("far away")).toBeNull();

		scroll(false);
		expect(lastGl().calls("loseContext")).toBe(1);
		expect(host(view)).toHaveAttribute("data-state", "fallback");
		expect(view.getByText("far away")).toBeInTheDocument();
	});

	test("scrolling back restores the same context and draws again", () => {
		const view = mount({ fallback: <p>far away</p> });
		scroll(false);
		const before = lastGl().draws();

		scroll(true);
		expect(env.gls).toHaveLength(1);
		expect(lastGl().calls("restoreContext")).toBe(1);
		expect(lastGl().draws()).toBeGreaterThan(before);
		expect(host(view)).toHaveAttribute("data-state", "static");
		expect(view.queryByText("far away")).toBeNull();
	});

	test("unmounting leaves the budget and stops observing", () => {
		const view = mount();
		measureHost();
		view.unmount();
		expect(env.observers.every((o) => o.targets.length === 0)).toBe(true);
	});
});
