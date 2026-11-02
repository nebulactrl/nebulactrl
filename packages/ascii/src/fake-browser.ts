// Test-only: the slice of a browser the scene engines touch (a WebGL2
// context, canvases, rAF, timers, observers, media queries and the document),
// faked so the engines run under `bun test` with no DOM and no GPU. Every GL
// call is recorded; time moves only when a test moves it.

interface GlCall {
	name: string;
	args: unknown[];
}

interface FakeLocation {
	name: string;
	program: object;
}

export interface FakeGlOptions {
	/** Whether shaders compile and programs link. Default true. */
	compiles?: boolean;
}

export interface FakeGl {
	gl: WebGL2RenderingContext;
	calls: GlCall[];
	/** Every call of `name`, in order. */
	called(name: string): unknown[][];
	/** The values of the last `uniform*` call on the uniform `name`. */
	uniform(name: string): unknown[] | undefined;
	/** All `uniform1f` values written to `name`, in order. */
	uniformHistory(name: string): unknown[][];
	setCompiles(ok: boolean): void;
}

/** A WebGL2 context that records its calls and hands out tokens for GL objects. */
export function createFakeGl({ compiles = true }: FakeGlOptions = {}): FakeGl {
	const calls: GlCall[] = [];
	const constants = new Map<string, number>();
	let ok = compiles;
	let objects = 0;
	const token = (kind: string) => ({ kind, id: ++objects });

	const behaviour: Record<string, (...args: unknown[]) => unknown> = {
		createShader: () => token("shader"),
		createProgram: () => token("program"),
		createTexture: () => token("texture"),
		createFramebuffer: () => token("framebuffer"),
		createVertexArray: () => token("vao"),
		getShaderParameter: () => ok,
		getProgramParameter: () => ok,
		getShaderInfoLog: () => "fake compile error",
		getProgramInfoLog: () => "fake link error",
		getUniformLocation: (program, name) => ({
			name: String(name),
			program: program as object,
		}),
		getExtension: () => ({ loseContext: () => record("loseContext", []) }),
	};

	function record(name: string, args: unknown[]): void {
		calls.push({ name, args });
	}

	const gl = new Proxy(
		{},
		{
			get(_target, property) {
				if (typeof property !== "string") return undefined;
				if (/^[A-Z0-9_]+$/.test(property)) {
					if (!constants.has(property)) {
						// Spaced out so `TEXTURE0 + unit` never lands on another constant.
						constants.set(property, (constants.size + 1) * 64);
					}
					return constants.get(property);
				}
				return (...args: unknown[]) => {
					record(property, args);
					return behaviour[property]?.(...args);
				};
			},
		},
	) as unknown as WebGL2RenderingContext;

	const called = (name: string) =>
		calls.filter((c) => c.name === name).map((c) => c.args);
	const uniformCalls = (name: string) =>
		calls.filter(
			(c) =>
				c.name.startsWith("uniform") &&
				(c.args[0] as FakeLocation | null)?.name === name,
		);

	return {
		gl,
		calls,
		called,
		uniform: (name) => uniformCalls(name).at(-1)?.args.slice(1),
		uniformHistory: (name) => uniformCalls(name).map((c) => c.args.slice(1)),
		setCompiles: (value) => {
			ok = value;
		},
	};
}

/** A canvas 2D context good enough for measuring and baking glyphs. */
export interface Fake2d {
	filled: string[];
	font: string;
	[key: string]: unknown;
}

function createFake2d(): Fake2d {
	const filled: string[] = [];
	let last = "";
	return {
		filled,
		font: "",
		fillStyle: "",
		textAlign: "",
		textBaseline: "",
		measureText: (text: string) => ({
			width: String(text) === "M" ? 8 : 7,
			fontBoundingBoxAscent: 10,
			fontBoundingBoxDescent: 3,
		}),
		clearRect: () => {},
		fillRect: () => {},
		fillText: (text: string) => {
			last = text;
			filled.push(text);
		},
		// Coverage grows with the glyph's character code: a deterministic "ink" order.
		getImageData: (_x: number, _y: number, w: number, h: number) => {
			const alpha = last ? Math.min(255, last.charCodeAt(0) % 256) : 0;
			const data = new Uint8ClampedArray(w * h * 4);
			for (let i = 3; i < data.length; i += 4) data[i] = alpha;
			return { data };
		},
	};
}

export interface FakeCanvasInit {
	width?: number;
	height?: number;
	/** Whether a 2D context exists. Default true. */
	has2d?: boolean;
}

/** A canvas that is also its own event target. */
export class FakeCanvas extends EventTarget {
	clientWidth: number;
	clientHeight: number;
	width = 0;
	height = 0;
	rect = { left: 0, top: 0, width: 0, height: 0, right: 0, bottom: 0 };
	readonly gl: FakeGl;
	readonly twoD: Fake2d | null;
	/** What getContext("webgl2") returns; null simulates a browser without it. */
	hasWebgl2 = true;
	readonly contextRequests: unknown[][] = [];

	constructor(
		{ width = 800, height = 600, has2d = true }: FakeCanvasInit = {},
		gl: FakeGl = createFakeGl(),
	) {
		super();
		this.clientWidth = width;
		this.clientHeight = height;
		this.rect = {
			left: 0,
			top: 0,
			width,
			height,
			right: width,
			bottom: height,
		};
		this.gl = gl;
		this.twoD = has2d ? createFake2d() : null;
	}

	getContext(kind: string, options?: unknown): unknown {
		this.contextRequests.push([kind, options]);
		if (kind === "webgl2") return this.hasWebgl2 ? this.gl.gl : null;
		if (kind === "2d") return this.twoD;
		return null;
	}

	getBoundingClientRect() {
		return this.rect;
	}

	resizeTo(width: number, height: number): void {
		this.clientWidth = width;
		this.clientHeight = height;
		this.rect = {
			left: 0,
			top: 0,
			width,
			height,
			right: width,
			bottom: height,
		};
	}

	/** The canvas the engine passes around, as the DOM type. */
	asElement(): HTMLCanvasElement {
		return this as unknown as HTMLCanvasElement;
	}
}

interface FakeObserver {
	callback: (entries: unknown[]) => void;
	observed: unknown[];
	disconnected: boolean;
}

interface Timer {
	id: number;
	at: number;
	run: () => void;
}

export interface FakeBrowser {
	/** The canvas `document.createElement("canvas")` hands out (atlas, probes). */
	created: FakeCanvas[];
	document: FakeDocument;
	/** Frames waiting for the next rAF, oldest first. */
	frames: Array<(now: number) => void>;
	/** Runs the next queued animation frame at time `now`. */
	frame(now: number): void;
	/** Moves the clock forward and runs the timers that come due. */
	advance(ms: number): void;
	/** Timers still waiting, by delay. */
	pendingTimers(): number[];
	resizeObservers: FakeObserver[];
	intersectionObservers: FakeObserver[];
	mediaQueries: Map<string, FakeMediaQuery>;
	media(query: string): FakeMediaQuery;
	window: FakeWindow;
	restore(): void;
}

class FakeMediaQuery extends EventTarget {
	matches: boolean;
	constructor(matches: boolean) {
		super();
		this.matches = matches;
	}
	set(matches: boolean): void {
		this.matches = matches;
		this.dispatchEvent(new Event("change"));
	}
}

class FakeDocument extends EventTarget {
	hidden = false;
	documentElement = new EventTarget();
	fonts: { load: (font: string, text: string) => Promise<unknown> } | undefined;
	created: FakeCanvas[] = [];
	has2d = true;

	createElement(tag: string): unknown {
		if (tag !== "canvas")
			throw new Error(`fake document cannot create <${tag}>`);
		const canvas = new FakeCanvas({ has2d: this.has2d });
		this.created.push(canvas);
		return canvas;
	}

	hide(hidden: boolean): void {
		this.hidden = hidden;
		this.dispatchEvent(new Event("visibilitychange"));
	}
}

class FakeWindow extends EventTarget {
	devicePixelRatio = 1;
	setTimeout: (fn: () => void, ms?: number) => number = () => 0;
}

const GLOBALS = [
	"document",
	"window",
	"requestAnimationFrame",
	"cancelAnimationFrame",
	"setTimeout",
	"clearTimeout",
	"matchMedia",
	"ResizeObserver",
	"IntersectionObserver",
] as const;

export interface FakeBrowserOptions {
	reducedMotion?: boolean;
	dpr?: number;
}

/**
 * Installs the fakes as globals and returns the controls. Call `restore()` when
 * the test ends. Time starts at 0 and moves only through `advance`.
 */
export function installFakeBrowser({
	reducedMotion = false,
	dpr = 1,
}: FakeBrowserOptions = {}): FakeBrowser {
	const saved = new Map<string, PropertyDescriptor | undefined>();
	for (const name of GLOBALS) {
		saved.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
	}
	const set = (name: string, value: unknown) =>
		Object.defineProperty(globalThis, name, {
			configurable: true,
			writable: true,
			value,
		});

	const document = new FakeDocument();
	const window = new FakeWindow();
	window.devicePixelRatio = dpr;

	const frames: Array<(now: number) => void> = [];
	const timers: Timer[] = [];
	let clock = 0;
	let nextId = 1;
	const mediaQueries = new Map<string, FakeMediaQuery>();
	const resizeObservers: FakeObserver[] = [];
	const intersectionObservers: FakeObserver[] = [];

	const media = (query: string): FakeMediaQuery => {
		let found = mediaQueries.get(query);
		if (!found) {
			found = new FakeMediaQuery(
				query === "(prefers-reduced-motion: reduce)" ? reducedMotion : false,
			);
			mediaQueries.set(query, found);
		}
		return found;
	};

	const addTimer = (fn: () => void, ms = 0): number => {
		const timer = { id: nextId++, at: clock + ms, run: fn };
		timers.push(timer);
		return timer.id;
	};
	const dropTimer = (id?: number) => {
		const at = timers.findIndex((t) => t.id === id);
		if (at >= 0) timers.splice(at, 1);
	};
	window.setTimeout = addTimer;

	class Observer {
		state: FakeObserver;
		constructor(callback: (entries: unknown[]) => void, list: FakeObserver[]) {
			this.state = { callback, observed: [], disconnected: false };
			list.push(this.state);
		}
		observe(target: unknown) {
			this.state.observed.push(target);
		}
		disconnect() {
			this.state.disconnected = true;
		}
	}
	class FakeResizeObserver extends Observer {
		constructor(callback: (entries: unknown[]) => void) {
			super(callback, resizeObservers);
		}
	}
	class FakeIntersectionObserver extends Observer {
		constructor(callback: (entries: unknown[]) => void) {
			super(callback, intersectionObservers);
		}
	}

	set("document", document);
	set("window", window);
	set("requestAnimationFrame", (fn: (now: number) => void) => {
		frames.push(fn);
		return frames.length;
	});
	set("cancelAnimationFrame", () => {
		frames.length = 0;
	});
	set("setTimeout", addTimer);
	set("clearTimeout", dropTimer);
	set("matchMedia", (query: string) => media(query));
	set("ResizeObserver", FakeResizeObserver);
	set("IntersectionObserver", FakeIntersectionObserver);

	return {
		created: document.created,
		document,
		frames,
		frame(now) {
			const next = frames.shift();
			if (!next) throw new Error("no animation frame is queued");
			next(now);
		},
		advance(ms) {
			const until = clock + ms;
			for (;;) {
				const due = timers
					.filter((t) => t.at <= until)
					.sort((a, b) => a.at - b.at)[0];
				if (!due) break;
				timers.splice(timers.indexOf(due), 1);
				clock = due.at;
				due.run();
			}
			clock = until;
		},
		pendingTimers: () => timers.map((t) => t.at - clock),
		resizeObservers,
		intersectionObservers,
		mediaQueries,
		media,
		window,
		restore() {
			for (const [name, descriptor] of saved) {
				if (descriptor) Object.defineProperty(globalThis, name, descriptor);
				else Reflect.deleteProperty(globalThis, name);
			}
		},
	};
}
