/**
 * ASCII scene engine. Framework-free: React wraps it in `ascii-backdrop.tsx`;
 * the maintenance pages bundle it with `standalone.ts`.
 *
 * Two WebGL2 passes (see shader.ts), no dependency, one scene per canvas
 * (`opts.scene`: eclipse, dial-out, lost-signal, dome, band; default
 * eclipse). The glyph atlas is baked once per size on a 2D canvas. Per frame
 * the main thread does a handful of GL calls and nothing else: no allocation,
 * no readback.
 *
 * Budgets, shared by every scene: <= 7.5k cells, DPR capped at 2, rAF
 * time-gated to the scene's rate (never above 24 fps; 20 at 60 Hz), paused
 * while the tab is hidden or the canvas is off-screen, one static frame under
 * prefers-reduced-motion.
 *
 * The canvas must be sized by CSS (the engine follows `clientWidth/Height`
 * and sets only the backing store).
 */
import {
	buildMask,
	computeGrid,
	dueForFrame,
	effectiveDpr,
	type GridSpec,
	MAX_CELLS,
	type MaskRegion,
	maskScale,
	mixRgb,
	type RectLike,
	type RGB,
	regionFromRect,
	selectRampVariants,
} from "./layout";
import {
	type FocusPoint,
	resolveScene,
	type SceneName,
	sceneFps,
	scenePlacement,
} from "./scenes";
import { BLIT_FRAG, fieldFrag, VERT } from "./shader";

export type { RGB } from "./layout";
export type { FocusPoint, SceneName } from "./scenes";

export interface AsciiPalette {
	/** Dimmest glyph of the slate hue. */
	dim: RGB;
	/** Brightest glyph of the slate hue. */
	ink: RGB;
	/** Accent (indigo): mixed into the lit limb and the ring's inner lip. */
	accent: RGB;
	accentInk: RGB;
	/** Star white: the four orbiting sparks. */
	star: RGB;
}

export interface AsciiOptions {
	/** The scene to draw. Default "eclipse". */
	scene?: SceneName;
	/** Dial-out only: where the signals converge, as fractions of the canvas. Default: the free side of `maskRect`. */
	focus?: FocusPoint;
	palette: AsciiPalette;
	/** Frame cap. The effective rate is a divisor of the display refresh (24 -> 20 at 60 Hz). Default: the scene's own rate; never above 24. */
	fps?: number;
	/** Highest device-pixel ratio used. Default 2. */
	dprCap?: number;
	/**
	 * Upper bound for cols x rows, or a function of the canvas size (CSS px)
	 * re-evaluated on every rebuild, so a small framed canvas gets a small
	 * budget. Default 7500.
	 */
	maxCells?: number | ((cssW: number, cssH: number) => number);
	/**
	 * Canvas width (CSS px) under which the orb is centred behind the panel
	 * instead of sitting right of centre. Default 720; a framed preview passes 0.
	 */
	narrowBelow?: number;
	/** The panel over the field: a soft dim around it. Re-read on every resize. */
	maskRect?: () => RectLike | null;
	/** Text drawn straight over the field (the wordmark): dimmed hard. Re-read on every resize. */
	quietRects?: () => readonly RectLike[];
	/** Fixed value, or omit to follow `prefers-reduced-motion` live. */
	reducedMotion?: boolean;
	/** Monospace stack for the glyphs. Default IBM Plex Mono with system fallbacks. */
	fontFamily?: string;
	/** Shader time (s) painted for reduced motion. Default: the scene's own still (`SceneSpec.staticTime`). */
	staticTime?: number;
	/** "ready" after the first frame is painted (and after a context restore); "lost" on context loss. */
	onStatus?: (status: "ready" | "lost") => void;
	/** Record per-frame cost into preallocated ring buffers; read with `stats()`. */
	stats?: boolean;
}

export interface AsciiStats {
	frames: number;
	fps: number;
	meanMs: number;
	p95Ms: number;
	maxMs: number;
	grid: string;
	cell: string;
}

export interface AsciiEngine {
	/** Begin (or resume) animating. Honours reduced motion, hidden tabs and off-screen. */
	start(): void;
	stop(): void;
	/** Re-measure the canvas and the mask rects (debounced 120 ms). */
	resize(): void;
	/** Stop, free every GL object and release the context. The canvas cannot be reused. */
	destroy(): void;
	/** Stop the loop and paint one frame at shader time `t` seconds. */
	renderStatic(t: number): void;
	/** Resolves once the font is loaded and the first frame is painted. */
	readonly ready: Promise<void>;
	/** Frame statistics, only when `opts.stats` is set. */
	stats(): AsciiStats | null;
}

export const DEFAULT_FONT_FAMILY =
	'"IBM Plex Mono", ui-monospace, "SFMono-Regular", Menlo, monospace';

const RESIZE_DEBOUNCE_MS = 120;
const FONT_WAIT_MS = 2500;
const STAT_SLOTS = 900;

type Loc = WebGLUniformLocation | null;

interface Gpu {
	field: WebGLProgram;
	blit: WebGLProgram;
	vao: WebGLVertexArrayObject;
	fT: Loc;
	fRes: Loc;
	fGrid: Loc;
	fCell: Loc;
	fOrb: Loc;
	fFocus: Loc;
	fMaskAlpha: Loc;
	fMask: Loc;
	bRes: Loc;
	bGrid: Loc;
	bCell: Loc;
	bField: Loc;
	bAtlas: Loc;
	bLevels: Loc;
	bVariants: Loc;
	bExtraBase: Loc;
	bCut: Loc;
	bDither: Loc;
}

function compile(
	gl: WebGL2RenderingContext,
	type: number,
	source: string,
): WebGLShader {
	const shader = gl.createShader(type);
	if (!shader) throw new Error("createShader failed");
	gl.shaderSource(shader, source);
	gl.compileShader(shader);
	if (!gl.getShaderParameter(shader, gl.COMPILE_STATUS)) {
		throw new Error(gl.getShaderInfoLog(shader) ?? "shader compile failed");
	}
	return shader;
}

function link(
	gl: WebGL2RenderingContext,
	vert: string,
	frag: string,
): WebGLProgram {
	const program = gl.createProgram();
	if (!program) throw new Error("createProgram failed");
	const vs = compile(gl, gl.VERTEX_SHADER, vert);
	const fs = compile(gl, gl.FRAGMENT_SHADER, frag);
	gl.attachShader(program, vs);
	gl.attachShader(program, fs);
	gl.linkProgram(program);
	gl.deleteShader(vs);
	gl.deleteShader(fs);
	if (!gl.getProgramParameter(program, gl.LINK_STATUS)) {
		throw new Error(gl.getProgramInfoLog(program) ?? "program link failed");
	}
	return program;
}

function initGpu(gl: WebGL2RenderingContext, body: string): Gpu {
	const field = link(gl, VERT, fieldFrag(body));
	const blit = link(gl, VERT, BLIT_FRAG);
	const vao = gl.createVertexArray();
	const f = (n: string) => gl.getUniformLocation(field, n);
	const b = (n: string) => gl.getUniformLocation(blit, n);
	return {
		field,
		blit,
		vao,
		fT: f("uT"),
		fRes: f("uRes"),
		fGrid: f("uGrid"),
		fCell: f("uCell"),
		fOrb: f("uOrb"),
		fFocus: f("uFocus"),
		fMaskAlpha: f("uMaskAlpha"),
		fMask: f("uMask"),
		bRes: b("uRes"),
		bGrid: b("uGrid"),
		bCell: b("uCell"),
		bField: b("uField"),
		bAtlas: b("uAtlas"),
		bLevels: b("uLevels"),
		bVariants: b("uVariants"),
		bExtraBase: b("uExtraBase"),
		bCut: b("uCut"),
		bDither: b("uDither"),
	};
}

/** Texture units, fixed for the engine's life so the frame loop never rebinds them. */
const UNIT_FIELD = 0;
const UNIT_ATLAS = 1;
const UNIT_MASK = 2;

/** The GLSL field body of each scene a bundle ships (see shader.ts for the contract). */
export type SceneFields = Readonly<Partial<Record<SceneName, string>>>;

/**
 * Creates the backdrop on `canvas` for `opts.scene`, using the field bodies
 * `fields` ships; `engine.ts` ships every scene, the maintenance pages'
 * bundle only the Eclipse. Returns `null` when WebGL2 is unavailable, the
 * scene is not in `fields` or the shaders fail, so the caller keeps its
 * non-GL floor.
 */
export function createSceneEngine(
	canvas: HTMLCanvasElement,
	opts: AsciiOptions,
	fields: SceneFields,
): AsciiEngine | null {
	const spec = resolveScene(opts.scene);
	const body = fields[spec.name];
	if (body === undefined) {
		console.warn(`ascii scene "${spec.name}" is not in this bundle`);
		return null;
	}
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
		gpu = initGpu(gl, body);
	} catch (error) {
		console.warn("ascii backdrop disabled:", error);
		return null;
	}

	const family = opts.fontFamily ?? DEFAULT_FONT_FAMILY;
	const staticT = opts.staticTime ?? spec.staticTime;
	const dprCap = opts.dprCap ?? 2;
	const cellBudget = (cssW: number, cssH: number): number =>
		typeof opts.maxCells === "function"
			? opts.maxCells(cssW, cssH)
			: (opts.maxCells ?? MAX_CELLS);
	const interval = 1000 / sceneFps(spec, opts.fps);
	const motionQuery =
		opts.reducedMotion === undefined && typeof matchMedia === "function"
			? matchMedia("(prefers-reduced-motion: reduce)")
			: null;
	const reduced = () => opts.reducedMotion ?? motionQuery?.matches ?? false;

	let grid: GridSpec | null = null;
	let builtCssW = 0;
	let builtCssH = 0;
	let ramp: string[] | null = null;
	let atlasCanvas: HTMLCanvasElement | null = null;
	let tField: WebGLTexture | null = null;
	let tAtlas: WebGLTexture | null = null;
	let tMask: WebGLTexture | null = null;
	let fbo: WebGLFramebuffer | null = null;

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

	const record = opts.stats === true;
	const cost = new Float64Array(record ? STAT_SLOTS : 0);
	const at = new Float64Array(record ? STAT_SLOTS : 0);
	let nSamples = 0;

	/* ---------- build: grid, atlas, textures, static uniforms ---------- */

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

	/**
	 * Picks the ramp glyphs by measured ink coverage in the loaded font, then
	 * appends the scene's extra symbols. Runs once. The array is the atlas's
	 * column order: levels x variants, then extras.
	 */
	function calibrate(): string[] {
		const S = 40;
		const c = document.createElement("canvas");
		c.width = c.height = S;
		const x = c.getContext("2d", { willReadFrequently: true });
		const extras = Array.from(spec.extras);
		if (!x) {
			// Without a 2D context nothing can be measured: take the candidates in order.
			const pool = Array.from(spec.candidates);
			const ramped = Array.from(
				{ length: (spec.levels - 1) * spec.variants },
				(_, i) => pool[i % pool.length] as string,
			);
			return [
				...Array.from({ length: spec.variants }, () => " "),
				...ramped,
				...extras,
			];
		}
		x.font = `400 ${S * 0.8}px ${family}`;
		x.textAlign = "center";
		x.textBaseline = "middle";
		x.fillStyle = "#fff";
		const coverage: [string, number][] = [];
		for (const ch of spec.candidates) {
			x.clearRect(0, 0, S, S);
			x.fillText(ch, S / 2, S / 2);
			const d = x.getImageData(0, 0, S, S).data;
			let sum = 0;
			for (let i = 3; i < d.length; i += 4) sum += d[i] ?? 0;
			coverage.push([ch, sum / 255 / (S * S)]);
		}
		return [
			...selectRampVariants(coverage, spec.levels, spec.variants),
			...extras,
		];
	}

	/** Glyph x hue atlas at device-pixel cell size, so glyphs are crisp on retina. */
	function bakeAtlas(
		g: GridSpec,
		glyphs: readonly string[],
	): HTMLCanvasElement {
		const p = opts.palette;
		const hues: readonly { dim: RGB; bright: RGB }[] = [
			{ dim: p.dim, bright: p.ink },
			{
				dim: mixRgb(p.dim, p.accent, 0.55),
				bright: mixRgb(p.ink, p.accentInk, 0.7),
			},
			{ dim: p.ink, bright: p.star },
		];
		const canvasEl = atlasCanvas ?? document.createElement("canvas");
		atlasCanvas = canvasEl;
		canvasEl.width = g.cw * glyphs.length;
		canvasEl.height = g.ch * hues.length;
		const a = canvasEl.getContext("2d");
		if (!a) return canvasEl;
		const fpx = Math.round(g.fontPx * g.dpr);
		a.font = `400 ${fpx}px ${family}`;
		a.textBaseline = "alphabetic";
		const m = a.measureText("M");
		const asc = m.fontBoundingBoxAscent || fpx * 1.03;
		const desc = m.fontBoundingBoxDescent || fpx * 0.27;
		const base = Math.round((g.ch - (asc + desc)) / 2 + asc);
		const ramped = glyphs.length - spec.extras.length;
		const last = ramped / spec.variants - 1;
		for (let h = 0; h < hues.length; h++) {
			const hue = hues[h] as { dim: RGB; bright: RGB };
			for (let k = 0; k < glyphs.length; k++) {
				// Column k holds level floor(k / variants); level 0 stays blank.
				const level = Math.floor(k / spec.variants);
				if (k < ramped && level === 0) continue;
				const tone = k < ramped ? (level / last) ** spec.gamma : spec.extraTone;
				const col = mixRgb(hue.dim, hue.bright, tone);
				a.fillStyle = `rgb(${col[0] | 0},${col[1] | 0},${col[2] | 0})`;
				const glyph = glyphs[k] as string;
				a.fillText(
					glyph,
					k * g.cw + Math.round((g.cw - a.measureText(glyph).width) / 2),
					h * g.ch + base,
				);
			}
		}
		return canvasEl;
	}

	function regions(g: GridSpec): MaskRegion[] {
		const origin = canvas.getBoundingClientRect();
		const shift = (r: RectLike): RectLike => ({
			left: r.left - origin.left,
			top: r.top - origin.top,
			right: r.right - origin.left,
			bottom: r.bottom - origin.top,
		});
		const out: MaskRegion[] = [];
		const card = opts.maskRect?.();
		const scale = maskScale(canvas.clientWidth);
		if (card) out.push(regionFromRect(shift(card), g.dpr, spec.panel, scale));
		for (const q of opts.quietRects?.() ?? []) {
			out.push(regionFromRect(shift(q), g.dpr, spec.quiet, scale));
		}
		return out;
	}

	function watchDpr() {
		dprQuery?.removeEventListener("change", scheduleRebuild);
		dprQuery = null;
		if (typeof matchMedia !== "function") return;
		dprQuery = matchMedia(`(resolution: ${window.devicePixelRatio}dppx)`);
		dprQuery.addEventListener("change", scheduleRebuild, { once: true });
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
		const probe = document.createElement("canvas").getContext("2d");
		const g = computeGrid(
			cssW,
			cssH,
			dpr,
			(fontDevicePx) => {
				if (!probe) return fontDevicePx * 0.6;
				probe.font = `400 ${fontDevicePx}px ${family}`;
				return probe.measureText("M").width;
			},
			cellBudget(cssW, cssH),
		);
		grid = g;
		builtCssW = cssW;
		builtCssH = cssH;
		canvas.width = g.W;
		canvas.height = g.H;
		ramp ??= calibrate();
		const glyphs = ramp;
		const levels = (glyphs.length - spec.extras.length) / spec.variants;
		const atlas = bakeAtlas(g, glyphs);

		for (const t of [tField, tAtlas, tMask]) if (t) gl.deleteTexture(t);
		if (fbo) gl.deleteFramebuffer(fbo);

		const regs = regions(g);
		const origin = canvas.getBoundingClientRect();
		const toDevice = (r: RectLike): RectLike => ({
			left: (r.left - origin.left) * dpr,
			top: (r.top - origin.top) * dpr,
			right: (r.right - origin.left) * dpr,
			bottom: (r.bottom - origin.top) * dpr,
		});
		const card = opts.maskRect?.() ?? null;
		const place = scenePlacement(spec.name, {
			W: g.W,
			H: g.H,
			dpr,
			rect: card ? toDevice(card) : null,
			quiet: (opts.quietRects?.() ?? []).map(toDevice),
			narrowBelow: opts.narrowBelow,
			focus: opts.focus,
		});

		gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1);
		tMask = newTexture(UNIT_MASK);
		gl.texImage2D(
			gl.TEXTURE_2D,
			0,
			gl.R8,
			g.cols,
			g.rows,
			0,
			gl.RED,
			gl.UNSIGNED_BYTE,
			buildMask(g, regs, new Uint8Array(g.cols * g.rows), place.orb.dim),
		);
		tField = newTexture(UNIT_FIELD);
		gl.texImage2D(
			gl.TEXTURE_2D,
			0,
			gl.RGBA8,
			g.cols,
			g.rows,
			0,
			gl.RGBA,
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
		tAtlas = newTexture(UNIT_ATLAS);
		gl.pixelStorei(gl.UNPACK_PREMULTIPLY_ALPHA_WEBGL, true);
		gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA8, gl.RGBA, gl.UNSIGNED_BYTE, atlas);
		gl.pixelStorei(gl.UNPACK_PREMULTIPLY_ALPHA_WEBGL, false);

		gl.useProgram(gpu.field);
		gl.uniform2f(gpu.fRes, g.W, g.H);
		gl.uniform2f(gpu.fGrid, g.cols, g.rows);
		gl.uniform2f(gpu.fCell, g.cw, g.ch);
		gl.uniform3f(gpu.fOrb, place.orb.cx, place.orb.cy, place.orb.r);
		gl.uniform4f(gpu.fFocus, ...place.focus);
		gl.uniform1f(gpu.fMaskAlpha, spec.maskMode === "alpha" ? 1 : 0);
		gl.uniform1i(gpu.fMask, UNIT_MASK);
		gl.useProgram(gpu.blit);
		gl.uniform2f(gpu.bRes, g.W, g.H);
		gl.uniform2f(gpu.bGrid, g.cols, g.rows);
		gl.uniform2f(gpu.bCell, g.cw, g.ch);
		gl.uniform1i(gpu.bField, UNIT_FIELD);
		gl.uniform1i(gpu.bAtlas, UNIT_ATLAS);
		gl.uniform1f(gpu.bLevels, levels);
		gl.uniform1f(gpu.bVariants, spec.variants);
		gl.uniform1f(gpu.bExtraBase, levels * spec.variants);
		gl.uniform1f(gpu.bCut, spec.cutoff);
		gl.uniform1f(gpu.bDither, spec.dither);
		gl.bindVertexArray(gpu.vao);
		gl.disable(gl.BLEND);
		watchDpr();
		paint(curT);
	}

	/** Two draws. Everything else was bound in build(). Allocates nothing. */
	function paint(t: number): void {
		const g = grid;
		if (!g || lost) return;
		curT = t;
		gl.bindFramebuffer(gl.FRAMEBUFFER, fbo);
		gl.viewport(0, 0, g.cols, g.rows);
		gl.useProgram(gpu.field);
		gl.uniform1f(gpu.fT, t);
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
		tAnim += Math.min((now - lastNow) / 1000, 0.25);
		lastNow = now;
		paint(tAnim);
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
			paint(frozenT ?? staticT);
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

	const onVisibility = () => sync();
	const onMotion = () => sync();
	const onLost = (event: Event) => {
		event.preventDefault();
		lost = true;
		halt();
		opts.onStatus?.("lost");
	};
	const onRestored = () => {
		lost = false;
		tField = tAtlas = tMask = null;
		fbo = null;
		try {
			gpu = initGpu(gl, body);
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
	motionQuery?.addEventListener("change", onMotion);
	document.addEventListener("visibilitychange", onVisibility);
	canvas.addEventListener("webglcontextlost", onLost);
	canvas.addEventListener("webglcontextrestored", onRestored);

	async function init(): Promise<void> {
		// Optional: pages without `document.fonts` (or without the font) just use the fallback stack.
		if (document.fonts) {
			let timer = 0;
			try {
				await Promise.race([
					document.fonts.load(
						`400 16px ${family}`,
						spec.candidates + spec.extras,
					),
					new Promise<void>((resolve) => {
						timer = window.setTimeout(resolve, FONT_WAIT_MS);
					}),
				]);
			} catch {
				// A failed font load still leaves the fallback stack.
			}
			clearTimeout(timer);
		}
		if (destroyed) return;
		isReady = true;
		build();
		sync();
		if (grid) opts.onStatus?.("ready");
	}
	const ready = init().catch((error: unknown) => {
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
			motionQuery?.removeEventListener("change", onMotion);
			dprQuery?.removeEventListener("change", scheduleRebuild);
			document.removeEventListener("visibilitychange", onVisibility);
			canvas.removeEventListener("webglcontextlost", onLost);
			canvas.removeEventListener("webglcontextrestored", onRestored);
			if (!lost) {
				for (const t of [tField, tAtlas, tMask]) if (t) gl.deleteTexture(t);
				if (fbo) gl.deleteFramebuffer(fbo);
				gl.deleteProgram(gpu.field);
				gl.deleteProgram(gpu.blit);
				gl.deleteVertexArray(gpu.vao);
				gl.getExtension("WEBGL_lose_context")?.loseContext();
			}
		},
		ready,
		stats() {
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
				cell: `${grid.cw}x${grid.ch}@${grid.dpr}`,
			};
		},
	};
}
