/**
 * The Eclipse: a pixel-dithered ringed planet. Framework-free; React wraps it
 * in `ascii-backdrop.tsx`, the marketing site in `AsciiScene`, the maintenance
 * pages bundle it with `standalone.ts`.
 *
 * Two WebGL2 passes, one full-screen triangle each (see planet.glsl.ts): the
 * field shades one fragment per cell into a cols x rows target, the blit
 * reads it back nearest-neighbour at full resolution. The cell grid is 2 CSS
 * px x the effective DPR; tone 0 is transparent so the page and its halftone
 * floor show. Per frame the main thread sets a handful of uniforms and draws
 * twice: no allocation, no readback.
 *
 * Budgets: DPR capped at 2, rAF time-gated to 30 fps, paused while the tab is
 * hidden or the canvas is off-screen, one static frame under
 * prefers-reduced-motion.
 *
 * The canvas must be sized by CSS (the engine follows `clientWidth/Height`
 * and sets only the backing store).
 */
import {
	type AsciiEngine,
	type AsciiOptions,
	type AsciiPalette,
	type AsciiStats,
	link,
} from "./core";
import {
	dueForFrame,
	effectiveDpr,
	type MaskKind,
	type MaskRegion,
	maskAt,
	maskScale,
	mixRgb,
	type OrbPlacement,
	type RectLike,
	type RGB,
	regionFromRect,
} from "./layout";
import { PLANET_BLIT_FRAG, PLANET_FIELD_FRAG } from "./planet.glsl";
import { resolveScene, sceneFps, scenePlacement } from "./scenes";
import { VERT } from "./shader";

/** A cell is this many CSS px across. */
export const PLANET_CELL_CSS_PX = 2;
/** The light and the cursor weight ease toward their targets by this share per 60 Hz frame. */
export const LIGHT_EASE = 0.08;
export const POINTER_EASE = 0.06;
/** Where the light sits before any pointer has been over the canvas (canvas fractions, y up). */
const REST_CURSOR = [0.7, 0.6] as const;
/** The hard quiet region: rect only, one cell of feather, nothing at all inside. */
const HARD_QUIET_FEATHER_CSS_PX = PLANET_CELL_CSS_PX;
const RESIZE_DEBOUNCE_MS = 120;
const STAT_SLOTS = 900;

/* ───────── pure helpers ───────── */

export interface PlanetTones {
	/** Tone 1 and tone 2, 0..1 per channel. */
	c1: RGB;
	c2: RGB;
}

/**
 * The two drawn tones from the palette: tone 2 is the accent ink, tone 1 the
 * accent mixed 55% toward black (#6d6bff gives a deep indigo). Stars use tone 2.
 */
export function planetTones(palette: AsciiPalette): PlanetTones {
	const unit = (c: RGB): RGB => [c[0] / 255, c[1] / 255, c[2] / 255];
	return {
		c1: unit(mixRgb(palette.accent, [0, 0, 0], 0.55)),
		c2: unit(palette.accentInk),
	};
}

export interface PlanetGrid {
	/** Canvas size in device px. */
	W: number;
	H: number;
	dpr: number;
	/** Cell size in device px (fractional at fractional DPR). */
	cell: number;
	cols: number;
	rows: number;
}

export function planetGrid(
	cssW: number,
	cssH: number,
	dpr: number,
): PlanetGrid {
	const W = Math.max(1, Math.round(cssW * dpr));
	const H = Math.max(1, Math.round(cssH * dpr));
	const cell = PLANET_CELL_CSS_PX * dpr;
	return {
		W,
		H,
		dpr,
		cell,
		cols: Math.ceil(W / cell),
		rows: Math.ceil(H / cell),
	};
}

/**
 * Fills `out` (cols x rows x 2 bytes, row-major, row 0 at the top) with the
 * per-cell mask the shader reads: R from the `soft` regions (the panel's dim,
 * the wordmark's halo), G from the `hard` ones (text itself: 0 inside).
 */
export function buildPlanetMask(
	grid: Pick<PlanetGrid, "cols" | "rows" | "cell">,
	soft: readonly MaskRegion[],
	hard: readonly MaskRegion[],
	out: Uint8Array,
): Uint8Array {
	const { cols, rows, cell } = grid;
	let k = 0;
	for (let j = 0; j < rows; j++) {
		const py = (j + 0.5) * cell;
		for (let i = 0; i < cols; i++) {
			const px = (i + 0.5) * cell;
			out[k++] = Math.round(maskAt(px, py, soft) * 255);
			out[k++] = Math.round(maskAt(px, py, hard) * 255);
		}
	}
	return out;
}

/** The ring's band centres in orb radii (the shader's `rr` bands: inner, outer, faint dust), its squash and tilt (rad). */
export const RING_INNER = 1.6;
export const RING_OUTER = 1.95;
export const RING_DUST = 2.07;
export const RING_SQUASH = 3.4;
export const RING_TILT = 0.38;
/** The dust band reaches this far past its centre line before it fades out, in orb radii. */
const RING_FADE = 0.08;
/** The glow past the limb, in orb radii. */
const GLOW_REACH = 0.12;

/**
 * Half the width and half the height of the planet and its ring, in orb
 * radii: the dust band's outer edge as a tilted ellipse (semi-axes `a` and
 * `a / squash`), never less than the sphere plus its glow.
 */
export function planetExtent(): { halfW: number; halfH: number } {
	const a = RING_DUST + RING_FADE;
	const b = a / RING_SQUASH;
	const c = Math.cos(RING_TILT);
	const s = Math.sin(RING_TILT);
	return {
		halfW: Math.hypot(a * c, b * s),
		halfH: Math.max(1 + GLOW_REACH, Math.hypot(a * s, b * c)),
	};
}

/** Wide canvases: the ring's far tip stops this share of the width short of the edge, and the planet this share of the height short of the top and bottom. */
const EDGE_X = 0.01;
const MARGIN_Y = 0.06;
/** The ring's near tip may slide this share of the width under the copy (the panel mask dims it there). */
const COPY_OVERLAP = 0.12;
/** With no copy, the free side starts here (share of width). */
const FREE_FROM = 0.4;
/** The planet is at most this share of the height, and at least `MIN_RADIUS_OF_HEIGHT` however little room is free. */
const MAX_RADIUS_OF_HEIGHT = 0.26;
const MIN_RADIUS_OF_HEIGHT = 0.16;
const CENTRE_Y = 0.52;
/**
 * Narrow canvases: the planet sits under the panel, sized by the sphere
 * (`NARROW_RADIUS_OF_WIDTH` of the width; the ring tips may run off both side
 * edges), its top `1 - BELOW_PANEL` radii under the panel's bottom edge, so
 * the lit top and the front ring arc show.
 */
const NARROW_RADIUS_OF_WIDTH = 0.34;
const BELOW_PANEL = 0.9;
/** With no room below the panel the planet sits behind it instead: margins kept clear of the edge (share of width, share of height) and the field strength there. */
const NARROW_MARGIN_X = 0.04;
const NARROW_MARGIN_Y = 0.08;
const BEHIND_PANEL_DIM = 0.6;

/**
 * Where the planet sits, device px, y down. Sized from the ring, not the
 * sphere. Wide canvases: the span runs from `COPY_OVERLAP` of the width under
 * the copy to 1% short of the canvas edge, on the wider side of the copy
 * (`FREE_FROM` of the width onward with none), and
 *
 *     r = min( 0.26 * H,  span / (2 * halfW),  H * (1 - 2 * MARGIN_Y) / (2 * halfH) )
 *
 * with the centre in the middle of the span and at 52% of the height. The
 * far ring tip nearly reaches the edge; the near one may slide under the
 * copy. `narrow` canvases have no free side: the planet sits under the
 * panel, `r = 0.34 * W`, `cx = W / 2`, `cy = panelBottom + 0.9 r` at full
 * strength (the sphere's top just under the panel; the ring tips may run off
 * the side edges). When none of it would show (`cy - r >= H`) it falls back
 * to the whole ring inside the width (4% margins) behind the panel, as far as
 * an 8% top and bottom margin allows, at 0.6.
 */
export function planetOrb(input: {
	W: number;
	H: number;
	rect: RectLike | null;
	quiet: readonly RectLike[];
	narrow: boolean;
}): OrbPlacement {
	const { W, H, rect, quiet, narrow } = input;
	const { halfW, halfH } = planetExtent();
	const text = rect ? [rect, ...quiet] : quiet;
	if (narrow) {
		const r = NARROW_RADIUS_OF_WIDTH * W;
		const panelBottom = Math.max(0, ...text.map((t) => t.bottom));
		const cy = panelBottom + BELOW_PANEL * r;
		if (cy - r < H) return { cx: W / 2, cy, r, dim: 1 };
		const fallback = Math.min(
			(W * (1 - 2 * NARROW_MARGIN_X)) / (2 * halfW),
			(H * (1 - 2 * NARROW_MARGIN_Y)) / (2 * halfH),
		);
		const lo = H * NARROW_MARGIN_Y + halfH * fallback;
		const hi = Math.max(lo, H * (1 - NARROW_MARGIN_Y) - halfH * fallback);
		const want = rect ? (rect.top + rect.bottom) / 2 : H / 2;
		return {
			cx: W / 2,
			cy: Math.min(Math.max(want, lo), hi),
			r: fallback,
			dim: BEHIND_PANEL_DIM,
		};
	}
	const fit = (span: number) =>
		Math.min(span / (2 * halfW), (H * (1 - 2 * MARGIN_Y)) / (2 * halfH));
	let left = W * FREE_FROM;
	let right = W * (1 - EDGE_X);
	if (text.length > 0) {
		const textLeft = Math.min(...text.map((t) => t.left));
		const textRight = Math.max(...text.map((t) => t.right));
		const rightFree = W * (1 - EDGE_X) - textRight;
		const leftFree = textLeft - W * EDGE_X;
		if (rightFree >= leftFree) {
			left = textRight - COPY_OVERLAP * W;
		} else {
			left = W * EDGE_X;
			right = textLeft + COPY_OVERLAP * W;
		}
	}
	const r = Math.max(
		Math.min(fit(right - left), MAX_RADIUS_OF_HEIGHT * H),
		MIN_RADIUS_OF_HEIGHT * H,
	);
	return { cx: (left + right) / 2, cy: H * CENTRE_Y, r, dim: 1 };
}

/**
 * `cur` moved toward `target` the way a per-frame `cur += (target - cur) * rate`
 * does at 60 Hz, whatever the real frame time `dt` (s).
 */
export function easeToward(
	cur: number,
	target: number,
	rate: number,
	dt: number,
): number {
	return cur + (target - cur) * (1 - (1 - rate) ** (dt * 60));
}

/**
 * The pointer as fractions of `rect` (x right, y up), written to `out`;
 * `true` when it is inside the rect.
 */
export function pointerFraction(
	rect: Pick<DOMRect, "left" | "top" | "width" | "height">,
	clientX: number,
	clientY: number,
	out: [number, number],
): boolean {
	if (rect.width <= 0 || rect.height <= 0) return false;
	const x = (clientX - rect.left) / rect.width;
	const y = 1 - (clientY - rect.top) / rect.height;
	out[0] = x;
	out[1] = y;
	return x >= 0 && x <= 1 && y >= 0 && y <= 1;
}

/* ───────── GL ───────── */

type Loc = WebGLUniformLocation | null;

interface Gpu {
	field: WebGLProgram;
	blit: WebGLProgram;
	vao: WebGLVertexArrayObject;
	fRes: Loc;
	fCell: Loc;
	fGrid: Loc;
	fT: Loc;
	fOrb: Loc;
	fDim: Loc;
	fCur: Loc;
	fCurW: Loc;
	fMask: Loc;
	bRes: Loc;
	bCell: Loc;
	bGrid: Loc;
	bField: Loc;
	bC1: Loc;
	bC2: Loc;
}

/** Texture units, fixed for the engine's life so the frame loop never rebinds them. */
const UNIT_MASK = 0;
const UNIT_FIELD = 1;

function initGpu(gl: WebGL2RenderingContext): Gpu {
	const field = link(gl, VERT, PLANET_FIELD_FRAG);
	const blit = link(gl, VERT, PLANET_BLIT_FRAG);
	const f = (n: string) => gl.getUniformLocation(field, n);
	const b = (n: string) => gl.getUniformLocation(blit, n);
	return {
		field,
		blit,
		vao: gl.createVertexArray(),
		fRes: f("uRes"),
		fCell: f("uCell"),
		fGrid: f("uGrid"),
		fT: f("uT"),
		fOrb: f("uOrb"),
		fDim: f("uDim"),
		fCur: f("uCur"),
		fCurW: f("uCurW"),
		fMask: f("uMask"),
		bRes: b("uRes"),
		bCell: b("uCell"),
		bGrid: b("uGrid"),
		bField: b("uField"),
		bC1: b("uC1"),
		bC2: b("uC2"),
	};
}

/* ───────── engine ───────── */

/**
 * Creates the Eclipse on `canvas`. Returns `null` when WebGL2 is unavailable
 * or the shader fails, so the caller keeps its non-GL floor.
 */
export function createPlanet(
	canvas: HTMLCanvasElement,
	opts: Omit<AsciiOptions, "scene">,
): AsciiEngine | null {
	const spec = resolveScene("eclipse");
	const context = canvas.getContext("webgl2", {
		alpha: true,
		premultipliedAlpha: true,
		antialias: false,
		depth: false,
		stencil: false,
		powerPreference: "low-power",
		preserveDrawingBuffer: false,
	});
	if (!context) return null;
	// A const alias keeps the non-null type inside the closures below.
	const gl: WebGL2RenderingContext = context;
	let gpu: Gpu;
	try {
		gpu = initGpu(gl);
	} catch (error) {
		console.warn("ascii backdrop disabled:", error);
		return null;
	}

	const tones = planetTones(opts.palette);
	const staticT = opts.staticTime ?? spec.staticTime;
	const dprCap = opts.dprCap ?? 2;
	const interval = 1000 / sceneFps(spec, opts.fps);
	const motionQuery =
		opts.reducedMotion === undefined && typeof matchMedia === "function"
			? matchMedia("(prefers-reduced-motion: reduce)")
			: null;
	const reduced = () => opts.reducedMotion ?? motionQuery?.matches ?? false;

	let grid: PlanetGrid | null = null;
	let builtCssW = 0;
	let builtCssH = 0;
	let tMask: WebGLTexture | null = null;
	let tField: WebGLTexture | null = null;
	let fbo: WebGLFramebuffer | null = null;
	let maskBytes = new Uint8Array(0);

	let wanted = false;
	let frozenT: number | null = null;
	let running = false;
	let raf = 0;
	let lastDraw = 0;
	let lastNow = 0;
	let tAnim = staticT;
	let curT = staticT;
	let visible = true;
	let lost = false;
	let destroyed = false;
	let isReady = false;
	let resizeTimer = 0;
	let dprQuery: MediaQueryList | null = null;

	// The light: the eased cursor (x, y up, canvas fractions) and its weight.
	let curX: number = REST_CURSOR[0];
	let curY: number = REST_CURSOR[1];
	let curW = 0;
	let wantW = 0;
	const want: [number, number] = [REST_CURSOR[0], REST_CURSOR[1]];

	const record = opts.stats === true;
	const cost = new Float64Array(record ? STAT_SLOTS : 0);
	const at = new Float64Array(record ? STAT_SLOTS : 0);
	let nSamples = 0;

	/* ---------- build: grid, mask, static uniforms ---------- */

	function regions(
		g: PlanetGrid,
		card: RectLike | null,
		quiet: readonly RectLike[],
	): { soft: MaskRegion[]; hard: MaskRegion[] } {
		const origin = canvas.getBoundingClientRect();
		const shift = (r: RectLike): RectLike => ({
			left: r.left - origin.left,
			top: r.top - origin.top,
			right: r.right - origin.left,
			bottom: r.bottom - origin.top,
		});
		const scale = maskScale(canvas.clientWidth);
		const hardKind: MaskKind = {
			min: 0,
			feather: HARD_QUIET_FEATHER_CSS_PX,
			pad: 0,
		};
		const soft: MaskRegion[] = [];
		const hard: MaskRegion[] = [];
		if (card) soft.push(regionFromRect(shift(card), g.dpr, spec.panel, scale));
		for (const q of quiet) {
			soft.push(regionFromRect(shift(q), g.dpr, spec.quiet, scale));
			hard.push(regionFromRect(shift(q), g.dpr, hardKind));
		}
		return { soft, hard };
	}

	function newTexture(unit: number): WebGLTexture {
		const t = gl.createTexture();
		gl.activeTexture(gl.TEXTURE0 + unit);
		gl.bindTexture(gl.TEXTURE_2D, t);
		gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST);
		gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST);
		gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
		gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
		return t;
	}

	function build(): void {
		if (destroyed || lost) return;
		const cssW = canvas.clientWidth;
		const cssH = canvas.clientHeight;
		if (cssW === 0 || cssH === 0) {
			grid = null;
			return;
		}
		const dpr = effectiveDpr(window.devicePixelRatio, dprCap);
		const g = planetGrid(cssW, cssH, dpr);
		grid = g;
		builtCssW = cssW;
		builtCssH = cssH;
		canvas.width = g.W;
		canvas.height = g.H;

		const origin = canvas.getBoundingClientRect();
		const toDevice = (r: RectLike): RectLike => ({
			left: (r.left - origin.left) * dpr,
			top: (r.top - origin.top) * dpr,
			right: (r.right - origin.left) * dpr,
			bottom: (r.bottom - origin.top) * dpr,
		});
		const card = opts.maskRect?.() ?? null;
		const quiet = opts.quietRects?.() ?? [];
		const base = scenePlacement("eclipse", {
			W: g.W,
			H: g.H,
			dpr,
			rect: card ? toDevice(card) : null,
			quiet: quiet.map(toDevice),
			narrowBelow: opts.narrowBelow,
		}).orb;
		const orb = planetOrb({
			W: g.W,
			H: g.H,
			rect: card ? toDevice(card) : null,
			quiet: quiet.map(toDevice),
			narrow: base.dim < 1,
		});

		const { soft, hard } = regions(g, card, quiet);
		const size = g.cols * g.rows * 2;
		if (maskBytes.length !== size) maskBytes = new Uint8Array(size);
		buildPlanetMask(g, soft, hard, maskBytes);
		for (const t of [tMask, tField]) if (t) gl.deleteTexture(t);
		if (fbo) gl.deleteFramebuffer(fbo);
		gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1);
		tMask = newTexture(UNIT_MASK);
		gl.texImage2D(
			gl.TEXTURE_2D,
			0,
			gl.RG8,
			g.cols,
			g.rows,
			0,
			gl.RG,
			gl.UNSIGNED_BYTE,
			maskBytes,
		);
		// The field: one texel per cell, holding the tone index / 2.
		tField = newTexture(UNIT_FIELD);
		gl.texImage2D(
			gl.TEXTURE_2D,
			0,
			gl.R8,
			g.cols,
			g.rows,
			0,
			gl.RED,
			gl.UNSIGNED_BYTE,
			null,
		);
		fbo = gl.createFramebuffer();
		gl.bindFramebuffer(gl.FRAMEBUFFER, fbo);
		gl.framebufferTexture2D(
			gl.FRAMEBUFFER,
			gl.COLOR_ATTACHMENT0,
			gl.TEXTURE_2D,
			tField,
			0,
		);
		gl.bindFramebuffer(gl.FRAMEBUFFER, null);

		gl.useProgram(gpu.field);
		gl.uniform2f(gpu.fRes, g.W, g.H);
		gl.uniform1f(gpu.fCell, g.cell);
		gl.uniform2f(gpu.fGrid, g.cols, g.rows);
		gl.uniform3f(gpu.fOrb, orb.cx, orb.cy, orb.r);
		gl.uniform1f(gpu.fDim, orb.dim);
		gl.uniform1i(gpu.fMask, UNIT_MASK);
		gl.useProgram(gpu.blit);
		gl.uniform2f(gpu.bRes, g.W, g.H);
		gl.uniform1f(gpu.bCell, g.cell);
		gl.uniform2f(gpu.bGrid, g.cols, g.rows);
		gl.uniform1i(gpu.bField, UNIT_FIELD);
		gl.uniform3f(gpu.bC1, tones.c1[0], tones.c1[1], tones.c1[2]);
		gl.uniform3f(gpu.bC2, tones.c2[0], tones.c2[1], tones.c2[2]);
		gl.bindVertexArray(gpu.vao);
		gl.disable(gl.BLEND);
		watchDpr();
		paint(curT, false);
	}

	function watchDpr() {
		dprQuery?.removeEventListener("change", scheduleRebuild);
		dprQuery = null;
		if (typeof matchMedia !== "function") return;
		dprQuery = matchMedia(`(resolution: ${window.devicePixelRatio}dppx)`);
		dprQuery.addEventListener("change", scheduleRebuild, { once: true });
	}

	/** One draw. Everything else was bound in build(). Allocates nothing. */
	function paint(t: number, live: boolean): void {
		const g = grid;
		if (!g || lost) return;
		curT = t;
		gl.bindFramebuffer(gl.FRAMEBUFFER, fbo);
		gl.viewport(0, 0, g.cols, g.rows);
		gl.useProgram(gpu.field);
		gl.uniform1f(gpu.fT, t);
		gl.uniform2f(
			gpu.fCur,
			live ? curX : REST_CURSOR[0],
			live ? curY : REST_CURSOR[1],
		);
		gl.uniform1f(gpu.fCurW, live ? curW : 0);
		gl.drawArrays(gl.TRIANGLES, 0, 3);
		gl.bindFramebuffer(gl.FRAMEBUFFER, null);
		gl.viewport(0, 0, g.W, g.H);
		gl.useProgram(gpu.blit);
		gl.drawArrays(gl.TRIANGLES, 0, 3);
	}

	/* ---------- loop: rAF time-gated to the fps cap ---------- */

	function tick(now: number): void {
		raf = 0;
		if (!running) return;
		raf = requestAnimationFrame(tick);
		if (!dueForFrame(now, lastDraw, interval)) return;
		lastDraw = now;
		const t0 = record ? performance.now() : 0;
		const dt = Math.min((now - lastNow) / 1000, 0.25);
		tAnim += dt;
		lastNow = now;
		curX = easeToward(curX, want[0], LIGHT_EASE, dt);
		curY = easeToward(curY, want[1], LIGHT_EASE, dt);
		curW = easeToward(curW, wantW, POINTER_EASE, dt);
		paint(tAnim, true);
		if (record) {
			const slot = nSamples % STAT_SLOTS;
			cost[slot] = performance.now() - t0;
			at[slot] = t0;
			nSamples++;
		}
	}

	function run(): void {
		if (running) return;
		running = true;
		// No time jump after a pause.
		lastNow = lastDraw = performance.now();
		raf = requestAnimationFrame(tick);
	}

	function halt(): void {
		running = false;
		if (raf) cancelAnimationFrame(raf);
		raf = 0;
	}

	/** One place decides between static frame, loop and idle. */
	function sync(): void {
		if (destroyed || lost || !isReady) return;
		if (reduced() || frozenT !== null) {
			halt();
			paint(frozenT ?? staticT, false);
		} else if (wanted && visible && !document.hidden) {
			run();
		} else {
			halt();
		}
	}

	function scheduleRebuild(): void {
		clearTimeout(resizeTimer);
		resizeTimer = window.setTimeout(() => {
			if (destroyed) return;
			build();
			sync();
		}, RESIZE_DEBOUNCE_MS);
	}

	/* ---------- observers and lifecycle ---------- */

	const onPointer = (event: PointerEvent) => {
		if (reduced()) return;
		wantW = pointerFraction(
			canvas.getBoundingClientRect(),
			event.clientX,
			event.clientY,
			want,
		)
			? 1
			: 0;
	};
	const onPointerLeave = () => {
		wantW = 0;
	};
	const onLost = (event: Event) => {
		event.preventDefault();
		lost = true;
		halt();
		opts.onStatus?.("lost");
	};
	const onRestored = () => {
		lost = false;
		tMask = tField = null;
		fbo = null;
		try {
			gpu = initGpu(gl);
			build();
			sync();
			opts.onStatus?.("ready");
		} catch (error) {
			console.warn("ascii backdrop could not recover:", error);
			lost = true;
			opts.onStatus?.("lost");
		}
	};

	const sizeObserver =
		typeof ResizeObserver === "function"
			? new ResizeObserver(() => {
					// The first callback fires on observe(); only react to real changes.
					if (
						canvas.clientWidth !== builtCssW ||
						canvas.clientHeight !== builtCssH
					) {
						scheduleRebuild();
					}
				})
			: null;
	const viewObserver =
		typeof IntersectionObserver === "function"
			? new IntersectionObserver((entries) => {
					const last = entries[entries.length - 1];
					if (last) visible = last.isIntersecting;
					sync();
				})
			: null;

	sizeObserver?.observe(canvas);
	viewObserver?.observe(canvas);
	motionQuery?.addEventListener("change", sync);
	document.addEventListener("visibilitychange", sync);
	document.documentElement.addEventListener("pointerleave", onPointerLeave, {
		passive: true,
	});
	window.addEventListener("pointermove", onPointer, { passive: true });
	canvas.addEventListener("webglcontextlost", onLost);
	canvas.addEventListener("webglcontextrestored", onRestored);

	// Deferred one microtask so the caller can attach its own state before the first paint.
	const ready = Promise.resolve()
		.then(() => {
			if (destroyed) return;
			isReady = true;
			build();
			sync();
			if (grid) opts.onStatus?.("ready");
		})
		.catch((error: unknown) => {
			console.warn("ascii backdrop failed to start:", error);
			halt();
			opts.onStatus?.("lost");
		});

	return {
		start() {
			wanted = true;
			frozenT = null;
			sync();
		},
		stop() {
			wanted = false;
			sync();
		},
		resize: scheduleRebuild,
		renderStatic(t) {
			wanted = false;
			frozenT = t;
			sync();
		},
		destroy() {
			if (destroyed) return;
			destroyed = true;
			halt();
			clearTimeout(resizeTimer);
			sizeObserver?.disconnect();
			viewObserver?.disconnect();
			motionQuery?.removeEventListener("change", sync);
			dprQuery?.removeEventListener("change", scheduleRebuild);
			document.removeEventListener("visibilitychange", sync);
			document.documentElement.removeEventListener(
				"pointerleave",
				onPointerLeave,
			);
			window.removeEventListener("pointermove", onPointer);
			canvas.removeEventListener("webglcontextlost", onLost);
			canvas.removeEventListener("webglcontextrestored", onRestored);
			if (!lost) {
				for (const t of [tMask, tField]) if (t) gl.deleteTexture(t);
				if (fbo) gl.deleteFramebuffer(fbo);
				gl.deleteProgram(gpu.field);
				gl.deleteProgram(gpu.blit);
				gl.deleteVertexArray(gpu.vao);
				gl.getExtension("WEBGL_lose_context")?.loseContext();
			}
		},
		ready,
		stats(): AsciiStats | null {
			const n = Math.min(nSamples, STAT_SLOTS);
			if (!record || n < 3 || !grid) return null;
			const sorted = Array.from(cost.subarray(0, n)).sort((a, b) => a - b);
			let first = Number.POSITIVE_INFINITY;
			let last = 0;
			let sum = 0;
			for (let i = 0; i < n; i++) {
				first = Math.min(first, at[i] ?? 0);
				last = Math.max(last, at[i] ?? 0);
				sum += cost[i] ?? 0;
			}
			return {
				frames: n,
				fps: (n - 1) / ((last - first) / 1000),
				meanMs: sum / n,
				p95Ms: sorted[Math.floor(n * 0.95)] ?? 0,
				maxMs: sorted[n - 1] ?? 0,
				grid: `${grid.cols}x${grid.rows}`,
				cell: `${grid.cell}x${grid.cell}@${grid.dpr}`,
			};
		},
	};
}
