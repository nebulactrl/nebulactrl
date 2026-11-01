import { describe, expect, test } from "bun:test";
import { type BudgetMember, ContextBudget } from "./context-budget";
import { DitherRenderer, type RendererCallbacks } from "./renderer";

/** Contexts alive across every fake canvas of one test. */
interface Pool {
	alive: number;
	peak: number;
	created: number;
}

function flush(): Promise<void> {
	const { promise, resolve } = Promise.withResolvers<void>();
	queueMicrotask(resolve);
	return promise;
}

const CONSTANT = /^[A-Z][A-Z0-9_]*$/;

/**
 * A WebGL2 stand-in: every method answers with an object, every constant with
 * 1, `WEBGL_lose_context` only while the context is alive (as in browsers),
 * and loss events arrive asynchronously.
 */
class FakeGl {
	lost = false;
	/** `restoreContext()` works only on a context lost through the extension. */
	lostByExtension = false;
	readonly proxy: WebGL2RenderingContext;

	private readonly canvas: FakeCanvas;
	private readonly pool: Pool;

	constructor(canvas: FakeCanvas, pool: Pool) {
		this.canvas = canvas;
		this.pool = pool;
		const extension = {
			loseContext: () => {
				if (this.lost) return;
				this.lostByExtension = true;
				this.markLost();
				queueMicrotask(() => this.canvas.dispatch("webglcontextlost"));
			},
			restoreContext: () => {
				if (!this.lost || !this.lostByExtension) return;
				this.lost = false;
				this.pool.alive += 1;
				this.pool.peak = Math.max(this.pool.peak, this.pool.alive);
				queueMicrotask(() => this.canvas.dispatch("webglcontextrestored"));
			},
		};
		this.proxy = new Proxy({} as Record<string, unknown>, {
			get: (_target, prop) => {
				if (prop === "isContextLost") return () => this.lost;
				if (prop === "getExtension") {
					return (name: string) =>
						name === "WEBGL_lose_context" && !this.lost ? extension : null;
				}
				if (prop === "getShaderParameter" || prop === "getProgramParameter") {
					return () => true;
				}
				if (typeof prop === "string" && CONSTANT.test(prop)) return 1;
				return () => ({});
			},
		}) as unknown as WebGL2RenderingContext;
	}

	private markLost(): void {
		this.lost = true;
		this.pool.alive -= 1;
	}

	/** The browser drops the context; the event is queued. */
	dropByBrowser(dispatch = true): void {
		this.lostByExtension = false;
		this.markLost();
		if (dispatch) {
			queueMicrotask(() => this.canvas.dispatch("webglcontextlost"));
		}
	}
}

class FakeCanvas {
	contextRequests = 0;
	width = 0;
	height = 0;
	style: Record<string, string> = {};
	isConnected = true;
	replacedBy: FakeCanvas | null = null;
	private current: FakeGl | null = null;
	private readonly listeners = new Map<string, Set<(event: Event) => void>>();

	private readonly pool: Pool;
	private readonly webgl2: boolean;

	constructor(pool: Pool, webgl2 = true) {
		this.pool = pool;
		this.webgl2 = webgl2;
	}

	get context(): FakeGl | null {
		return this.current;
	}

	getContext(): WebGL2RenderingContext | null {
		this.contextRequests += 1;
		if (!this.webgl2) return null;
		if (!this.current) {
			this.current = new FakeGl(this, this.pool);
			this.pool.created += 1;
			this.pool.alive += 1;
			this.pool.peak = Math.max(this.pool.peak, this.pool.alive);
		}
		return this.current.proxy;
	}

	addEventListener(type: string, listener: (event: Event) => void): void {
		const set = this.listeners.get(type) ?? new Set();
		set.add(listener);
		this.listeners.set(type, set);
	}

	removeEventListener(type: string, listener: (event: Event) => void): void {
		this.listeners.get(type)?.delete(listener);
	}

	dispatch(type: string): void {
		for (const listener of [...(this.listeners.get(type) ?? [])]) {
			listener({ preventDefault() {} } as Event);
		}
	}

	listenerCount(): number {
		let count = 0;
		for (const set of this.listeners.values()) count += set.size;
		return count;
	}

	cloneNode(): FakeCanvas {
		return new FakeCanvas(this.pool, this.webgl2);
	}

	replaceWith(next: FakeCanvas): void {
		this.replacedBy = next;
		this.isConnected = false;
	}
}

function setup(webgl2 = true) {
	const pool: Pool = { alive: 0, peak: 0, created: 0 };
	const canvas = new FakeCanvas(pool, webgl2);
	const log = {
		statuses: [] as string[],
		rebuilt: 0,
		unsupported: 0,
		externalLoss: 0,
	};
	const callbacks: RendererCallbacks = {
		onStatus: (status) => log.statuses.push(status),
		onRebuilt: () => {
			log.rebuilt += 1;
		},
		onUnsupported: () => {
			log.unsupported += 1;
		},
		onExternalLoss: () => {
			log.externalLoss += 1;
		},
	};
	const renderer = new DitherRenderer(
		canvas as unknown as HTMLCanvasElement,
		callbacks,
	);
	return { pool, canvas, log, renderer, callbacks };
}

describe("DitherRenderer context lifecycle", () => {
	test("creates no context until a slot is granted", () => {
		const { pool, canvas, log, renderer } = setup();
		expect(canvas.contextRequests).toBe(0);
		expect(pool.created).toBe(0);
		renderer.reacquire();
		expect(canvas.contextRequests).toBe(1);
		expect(pool.alive).toBe(1);
		expect(log.rebuilt).toBe(1);
		expect(renderer.isHeld).toBe(true);
	});

	test("without WebGL2 the first grant reports it and holds nothing", () => {
		const { log, renderer } = setup(false);
		renderer.reacquire();
		expect(log.unsupported).toBe(1);
		expect(renderer.isHeld).toBe(false);
		renderer.reacquire();
		expect(log.unsupported).toBe(1);
	});

	test("a released context comes back on the same canvas", async () => {
		const { pool, canvas, log, renderer } = setup();
		renderer.reacquire();
		renderer.release();
		expect(pool.alive).toBe(0);
		await flush();
		renderer.reacquire();
		await flush();
		expect(canvas.replacedBy).toBeNull();
		expect(pool.alive).toBe(1);
		expect(pool.created).toBe(1);
		expect(log.rebuilt).toBe(2);
	});

	test("a grant right after release restores once the loss event arrives", async () => {
		const { pool, canvas, log, renderer } = setup();
		renderer.reacquire();
		renderer.release();
		renderer.reacquire();
		await flush();
		await flush();
		expect(canvas.replacedBy).toBeNull();
		expect(pool.alive).toBe(1);
		expect(log.rebuilt).toBe(2);
	});

	// The browser drops the oldest contexts past its own cap; the canvas keeps
	// the dead one for good and restoreContext() does not apply to it.
	test("a context the browser dropped is replaced on the next grant", async () => {
		const { pool, canvas, log, renderer } = setup();
		renderer.reacquire();
		canvas.context?.dropByBrowser();
		await flush();
		expect(log.externalLoss).toBe(1);
		expect(renderer.isHeld).toBe(false);

		renderer.reacquire();
		const next = canvas.replacedBy;
		expect(next).not.toBeNull();
		expect(next?.contextRequests).toBe(1);
		expect(pool.alive).toBe(1);
		expect(log.rebuilt).toBe(2);
		// The dead canvas is no longer listened to.
		expect(canvas.listenerCount()).toBe(0);
		expect(next?.listenerCount()).toBe(2);
	});

	test("a loss the budget hands back before the event arrives is replaced too", () => {
		const { pool, canvas, log, renderer } = setup();
		renderer.reacquire();
		canvas.context?.dropByBrowser(false);
		// The budget takes the slot while the lost event is still queued.
		expect(renderer.release()).toBe(true);
		renderer.reacquire();
		expect(canvas.replacedBy).not.toBeNull();
		expect(pool.alive).toBe(1);
		expect(log.rebuilt).toBe(2);
	});

	test("a context the browser restores behind the budget's back is given up", async () => {
		const { pool, canvas, renderer } = setup();
		renderer.reacquire();
		const context = canvas.context as FakeGl;
		context.dropByBrowser();
		await flush();
		// The browser brings it back on its own while no slot is granted.
		context.lost = false;
		pool.alive += 1;
		canvas.dispatch("webglcontextrestored");
		await flush();
		expect(pool.alive).toBe(0);
		renderer.reacquire();
		await flush();
		expect(canvas.replacedBy).toBeNull();
		expect(pool.alive).toBe(1);
	});

	test("canvases are not replaced without end when every context dies", async () => {
		const { canvas, renderer } = setup();
		renderer.reacquire();
		let current: FakeCanvas | null = canvas;
		for (let i = 0; i < 6 && current; i++) {
			current.context?.dropByBrowser();
			await flush();
			renderer.reacquire();
			current = current.replacedBy;
		}
		let depth = 0;
		for (let c = canvas.replacedBy; c; c = c.replacedBy) depth += 1;
		expect(depth).toBe(3);
	});
});

describe("context budget with renderers", () => {
	function member(renderer: DitherRenderer) {
		return {
			grant: () => renderer.reacquire(),
			revoke: () => {
				renderer.release();
			},
		};
	}

	// Thirteen shaders on screen at once, as on the Engine tab with the twelve
	// switched on: no more than eight contexts ever exist at once.
	test("shaders beyond the cap never create a ninth live context", () => {
		const pool: Pool = { alive: 0, peak: 0, created: 0 };
		const budget = new ContextBudget();
		const noop = () => {};
		for (let i = 0; i < 13; i++) {
			const canvas = new FakeCanvas(pool);
			const renderer = new DitherRenderer(
				canvas as unknown as HTMLCanvasElement,
				{
					onStatus: noop,
					onRebuilt: noop,
					onUnsupported: noop,
					onExternalLoss: noop,
				},
			);
			budget.add(member(renderer), false, "visible");
			expect(pool.alive).toBeLessThanOrEqual(budget.max);
		}
		expect(pool.peak).toBe(8);
		expect(pool.created).toBeLessThan(13 + 1);
		expect(budget.live).toBe(8);
	});

	test("a context the browser dropped is replaced when the budget grants a slot again", async () => {
		const pool: Pool = { alive: 0, peak: 0, created: 0 };
		const budget = new ContextBudget();
		const canvas = new FakeCanvas(pool);
		const holder: { member?: BudgetMember } = {};
		const renderer = new DitherRenderer(
			canvas as unknown as HTMLCanvasElement,
			{
				onStatus: () => {},
				onRebuilt: () => {},
				onUnsupported: () => {},
				onExternalLoss: () => {
					if (holder.member) budget.lose(holder.member);
				},
			},
		);
		holder.member = member(renderer);
		budget.add(holder.member, false, "visible");
		expect(budget.live).toBe(1);

		canvas.context?.dropByBrowser();
		await flush();
		// The budget saw the loss and granted the free slot at once.
		expect(budget.live).toBe(1);
		expect(canvas.replacedBy?.contextRequests).toBe(1);
		expect(pool.alive).toBe(1);
		expect(renderer.isHeld).toBe(true);
	});
});
