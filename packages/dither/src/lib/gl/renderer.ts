/**
 * The WebGL2 side of DitherShader: two programs, one cells texture, no
 * per-frame allocation. It creates no context of its own: the context budget
 * grants one (`reacquire()`) and takes it back (`release()`). Survives context
 * loss (rebuilds every resource on restore, or on a fresh canvas when the
 * browser dropped the context).
 */
import { BLUE_NOISE_SIDE, blueNoiseBytes } from "../blue-noise";
import { errorOnce, warnOnce } from "./dev";
import type { GridLayout } from "./layout";
import { MAX_PALETTE, type PackedPalette } from "./palette";
import {
	assembleFieldFragment,
	DITHER_FRAGMENT,
	describeFieldCompileFailure,
	type FieldProgramSources,
	findPreludeRedefinitions,
	VERTEX_SOURCE,
} from "./shader-source";
import { applyUniforms, type UniformRecord } from "./uniforms";

export interface TextureInput {
	width: number;
	height: number;
	/** Single channel, one byte per texel, row 0 = top row. */
	data: Uint8Array;
	/** Filter linearly instead of nearest. */
	linear?: boolean | undefined;
}

export type TextureInputs = Readonly<Record<string, TextureInput>>;

export interface RenderParams {
	n: number;
	levels: number;
	shape: "square" | "dot";
}

/**
 * `ready` after a successful draw, `pending` while it holds a context but has
 * not drawn yet, `fallback` while nothing can be drawn (no context, lost, or
 * a program that did not compile).
 */
export type RendererStatus = "ready" | "pending" | "fallback";

export interface RendererCallbacks {
	/** The renderer's `RendererStatus`, on every change. */
	onStatus(status: RendererStatus): void;
	/** Resources were rebuilt after a context restore: draw again. */
	onRebuilt(): void;
	/** WebGL2 is unavailable: the first grant could not create a context. */
	onUnsupported(): void;
	/** The context was lost outside the budget (the browser dropped it). */
	onExternalLoss(): void;
}

interface FieldProgram {
	program: WebGLProgram;
	uRes: WebGLUniformLocation | null;
	uAspect: WebGLUniformLocation | null;
	uTime: WebGLUniformLocation | null;
	uPointer: WebGLUniformLocation | null;
	uCell: WebGLUniformLocation | null;
	uPalN: WebGLUniformLocation | null;
	uPal: WebGLUniformLocation | null;
	user: Array<WebGLUniformLocation | null>;
	samplers: Array<WebGLUniformLocation | null>;
}

interface DitherProgram {
	program: WebGLProgram;
	uCells: WebGLUniformLocation | null;
	uGrid: WebGLUniformLocation | null;
	uSub: WebGLUniformLocation | null;
	uLevels: WebGLUniformLocation | null;
	uN: WebGLUniformLocation | null;
	uBlue: WebGLUniformLocation | null;
	uShape: WebGLUniformLocation | null;
	uHasColor: WebGLUniformLocation | null;
	uPalN: WebGLUniformLocation | null;
	uPal: WebGLUniformLocation | null;
}

interface GpuTexture {
	tex: WebGLTexture;
	input: TextureInput;
}

const CONTEXT_ATTRIBUTES: WebGLContextAttributes = {
	alpha: true,
	antialias: false,
	depth: false,
	stencil: false,
	premultipliedAlpha: true,
	preserveDrawingBuffer: false,
	powerPreference: "low-power",
};

/** Fresh canvases in a row (no draw between) before the renderer stays on its fallback. */
const MAX_REPLACEMENTS = 3;

export class DitherRenderer {
	private gl: WebGL2RenderingContext | null = null;
	private fieldProgram: FieldProgram | null = null;
	private ditherProgram: DitherProgram | null = null;
	private cellsTexture: WebGLTexture | null = null;
	private blueTexture: WebGLTexture | null = null;
	private framebuffer: WebGLFramebuffer | null = null;
	private cellsSize = { w: 0, h: 0 };

	private sources: FieldProgramSources | null = null;
	private textureInputs: TextureInputs = {};
	private gpuTextures: Array<GpuTexture | null> = [];
	private layout: GridLayout | null = null;
	/** `n` is the matrix: 2, 4 or 8 for Bayer, 64 for the blue-noise tile. */
	private params: RenderParams = { n: 4, levels: 6, shape: "square" };
	private readonly palette = new Float32Array(MAX_PALETTE * 4);
	private paletteCount = 2;
	private values: UniformRecord | undefined;

	private lost = false;
	private broken = false;
	private unsupported = false;
	private reported: RendererStatus = "fallback";
	/** The budget granted this renderer a slot (it may hold a context). */
	private held = false;
	/** Our own `release()` lost the context, so `restoreContext()` can bring it back. */
	private lostByUs = false;
	private pendingRestore = false;
	/** Fresh canvases since the last successful draw; bounds replacement loops. */
	private replacements = 0;
	/** The extension `release()` lost the context with; restore() needs the same object. */
	private loseExt: WEBGL_lose_context | null = null;
	private disposed = false;

	private readonly onLost = (event: Event) => {
		event.preventDefault();
		this.lost = true;
		this.dropResources();
		this.report("fallback");
		if (this.lostByUs) {
			if (this.pendingRestore && this.held) {
				this.pendingRestore = false;
				this.restore();
			}
			return;
		}
		// Lost outside our control (the browser dropped it): the slot it took is
		// gone with it, and only a fresh grant brings the context back.
		this.held = false;
		this.pendingRestore = false;
		this.callbacks.onExternalLoss();
	};

	private readonly onRestored = () => {
		this.lost = false;
		if (this.disposed) return;
		if (!this.held) {
			// The browser restored a context the budget took back: give it up again.
			const ext = this.gl?.getExtension("WEBGL_lose_context");
			if (ext) {
				this.loseExt = ext;
				this.lostByUs = true;
				ext.loseContext();
				return;
			}
		}
		this.lostByUs = false;
		this.report("pending");
		this.buildResources();
		this.callbacks.onRebuilt();
	};

	private canvas: HTMLCanvasElement;
	private readonly callbacks: RendererCallbacks;

	/**
	 * Creates no context: a context is the budget's to give, so the renderer
	 * takes one only when `reacquire()` is called with a grant.
	 */
	constructor(canvas: HTMLCanvasElement, callbacks: RendererCallbacks) {
		this.canvas = canvas;
		this.callbacks = callbacks;
	}

	// -- configuration --------------------------------------------------------

	setSources(sources: FieldProgramSources): void {
		this.sources = sources;
		for (const name of findPreludeRedefinitions(
			`${sources.field}\n${sources.colorField ?? ""}`,
		)) {
			warnOnce(
				`prelude:${name}`,
				`[dither] the field declares ${name}(), which the engine already provides; a matching signature fails to compile. Rename it or drop it.`,
			);
		}
		this.compileField();
	}

	setTextures(inputs: TextureInputs): void {
		this.textureInputs = inputs;
		this.uploadTextures();
	}

	setPalette(packed: PackedPalette): void {
		this.palette.set(packed.data);
		this.paletteCount = packed.count;
	}

	setParams(params: RenderParams): void {
		this.params = params;
	}

	setUniformValues(values: UniformRecord | undefined): void {
		this.values = values;
	}

	/** Size the canvas and the cells texture. Cheap when the layout is unchanged. */
	setLayout(layout: GridLayout): void {
		const sizeChanged =
			this.canvas.width !== layout.canvasW ||
			this.canvas.height !== layout.canvasH;
		if (sizeChanged) {
			this.canvas.width = layout.canvasW;
			this.canvas.height = layout.canvasH;
		}
		this.canvas.style.width = `${layout.cssW}px`;
		this.canvas.style.height = `${layout.cssH}px`;
		this.layout = layout;
		this.allocateCells();
	}

	// -- drawing --------------------------------------------------------------

	/** Draw one frame. Returns false when nothing could be drawn. */
	draw(time: number, pointerX: number, pointerY: number): boolean {
		const gl = this.gl;
		const layout = this.layout;
		const fp = this.fieldProgram;
		const dp = this.ditherProgram;
		if (!gl || !layout || !fp || !dp || !this.framebuffer || !this.cellsTexture)
			return false;
		if (this.lost || this.broken || gl.isContextLost()) return false;

		// Stage 1: the field, one pixel per cell.
		gl.bindFramebuffer(gl.FRAMEBUFFER, this.framebuffer);
		gl.viewport(0, 0, layout.cols, layout.rows);
		gl.useProgram(fp.program);
		gl.uniform2f(fp.uRes, layout.cols, layout.rows);
		gl.uniform1f(fp.uAspect, layout.aspect);
		gl.uniform1f(fp.uTime, time);
		gl.uniform2f(fp.uPointer, pointerX, pointerY);
		gl.uniform1f(fp.uCell, layout.cellCss);
		gl.uniform1i(fp.uPalN, this.paletteCount);
		gl.uniform4fv(fp.uPal, this.palette);
		if (this.sources)
			applyUniforms(gl, this.sources.uniforms, fp.user, this.values);
		for (let i = 0; i < this.gpuTextures.length; i++) {
			const gpu = this.gpuTextures[i];
			if (!gpu) continue;
			gl.activeTexture(gl.TEXTURE1 + i);
			gl.bindTexture(gl.TEXTURE_2D, gpu.tex);
		}
		gl.drawArrays(gl.TRIANGLES, 0, 3);

		// Stage 2: Bayer threshold, levels, shape, palette.
		gl.bindFramebuffer(gl.FRAMEBUFFER, null);
		gl.viewport(0, 0, layout.canvasW, layout.canvasH);
		gl.useProgram(dp.program);
		gl.activeTexture(gl.TEXTURE0);
		gl.bindTexture(gl.TEXTURE_2D, this.cellsTexture);
		gl.uniform1i(dp.uCells, 0);
		gl.uniform2f(dp.uGrid, layout.cols, layout.rows);
		gl.uniform1f(dp.uSub, layout.sub);
		gl.uniform1f(dp.uLevels, this.params.levels);
		gl.uniform1f(dp.uN, this.params.n);
		if (this.params.n >= 12 && this.ensureBlue(gl)) {
			gl.activeTexture(gl.TEXTURE1);
			gl.bindTexture(gl.TEXTURE_2D, this.blueTexture);
			gl.uniform1i(dp.uBlue, 1);
		} else {
			gl.uniform1i(dp.uBlue, 0);
		}
		gl.uniform1f(dp.uShape, this.params.shape === "dot" ? 1 : 0);
		gl.uniform1f(dp.uHasColor, this.sources?.colorField !== undefined ? 1 : 0);
		gl.uniform1i(dp.uPalN, this.paletteCount);
		gl.uniform4fv(dp.uPal, this.palette);
		gl.drawArrays(gl.TRIANGLES, 0, 3);

		this.report("ready");
		return true;
	}

	// -- context lifecycle ----------------------------------------------------

	/**
	 * The budget granted a slot: take a context. The first grant creates it
	 * (and finds out whether WebGL2 exists at all); after `release()` the lost
	 * context is restored; after a loss outside our control (the browser
	 * dropped it, so `restoreContext()` does not apply) the canvas is replaced
	 * and a fresh context created on it.
	 */
	reacquire(): void {
		if (this.held || this.disposed || this.unsupported) return;
		this.held = true;
		const gl = this.gl;
		if (!gl) {
			this.create();
		} else if (this.lostByUs) {
			if (this.lost) this.restore();
			else this.pendingRestore = true;
		} else if (this.lost || gl.isContextLost()) {
			this.replaceCanvas();
		}
	}

	/**
	 * The budget took the slot back: hand the GPU context over. A context
	 * already lost has nothing to hand back; the next grant restores it.
	 */
	release(): boolean {
		if (!this.held) return false;
		this.held = false;
		this.pendingRestore = false;
		const gl = this.gl;
		if (!gl) return true;
		if (this.lostByUs || this.lost || gl.isContextLost()) return true;
		const ext = gl.getExtension("WEBGL_lose_context");
		if (!ext) return false;
		// Kept for restore(): a lost context returns null from getExtension, so
		// the extension cannot be fetched again once the context is lost.
		this.loseExt = ext;
		this.lostByUs = true;
		ext.loseContext();
		return true;
	}

	/** Whether a slot is granted (the renderer may hold a context). */
	get isHeld(): boolean {
		return this.held;
	}

	dispose(): void {
		this.disposed = true;
		this.held = false;
		this.canvas.removeEventListener("webglcontextlost", this.onLost);
		this.canvas.removeEventListener("webglcontextrestored", this.onRestored);
		const gl = this.gl;
		this.gl = null;
		if (!gl || gl.isContextLost()) return;
		this.dropResources(gl);
		// Free the GPU context only when the canvas really left the page. In
		// StrictMode the same canvas is reused at once, and a lost context
		// would come back dead.
		const ext = gl.getExtension("WEBGL_lose_context");
		const canvas = this.canvas;
		setTimeout(() => {
			if (!canvas.isConnected) ext?.loseContext();
		}, 0);
	}

	private create(): void {
		const gl = this.canvas.getContext("webgl2", CONTEXT_ATTRIBUTES);
		if (!gl) {
			this.unsupported = true;
			this.held = false;
			this.callbacks.onUnsupported();
			return;
		}
		this.gl = gl;
		this.lost = false;
		this.lostByUs = false;
		this.canvas.addEventListener("webglcontextlost", this.onLost);
		this.canvas.addEventListener("webglcontextrestored", this.onRestored);
		if (gl.isContextLost()) {
			// A canvas hands back the context it already has, even a dead one.
			this.replaceCanvas();
			return;
		}
		this.report("pending");
		this.buildResources();
		this.callbacks.onRebuilt();
	}

	/** Bring back a context our own `release()` lost. */
	private restore(): void {
		const ext = this.loseExt;
		if (ext) {
			try {
				ext.restoreContext();
				return;
			} catch {
				// Fall through to a fresh canvas.
			}
		}
		this.replaceCanvas();
	}

	/**
	 * A canvas keeps its lost context for good, and `restoreContext()` only
	 * works on a context lost through the extension. Swap in a copy of the
	 * canvas (same class, style and size) and create a fresh context on it.
	 * At most `MAX_REPLACEMENTS` times in a row without a successful draw, so a
	 * GPU that keeps losing contexts cannot spin.
	 */
	private replaceCanvas(): void {
		if (this.replacements >= MAX_REPLACEMENTS) return;
		this.replacements += 1;
		const old = this.canvas;
		old.removeEventListener("webglcontextlost", this.onLost);
		old.removeEventListener("webglcontextrestored", this.onRestored);
		this.dropResources(null);
		const next = old.cloneNode(false) as HTMLCanvasElement;
		old.replaceWith(next);
		this.canvas = next;
		this.gl = null;
		this.lost = false;
		this.lostByUs = false;
		this.pendingRestore = false;
		this.loseExt = null;
		this.create();
	}

	private report(status: RendererStatus): void {
		if (status === "ready") this.replacements = 0;
		if (this.reported === status) return;
		this.reported = status;
		this.callbacks.onStatus(status);
	}

	// -- resources ------------------------------------------------------------

	private buildResources(): void {
		const gl = this.gl;
		if (!gl || this.lost) return;
		this.ditherProgram = this.linkDither(gl);
		this.compileField();
		this.cellsSize = { w: 0, h: 0 };
		this.allocateCells();
		this.uploadTextures(true);
	}

	private dropResources(gl: WebGL2RenderingContext | null = this.gl): void {
		// After a loss the handles are already invalid; deleting is harmless but pointless.
		if (gl && !gl.isContextLost()) {
			if (this.fieldProgram) gl.deleteProgram(this.fieldProgram.program);
			if (this.ditherProgram) gl.deleteProgram(this.ditherProgram.program);
			if (this.cellsTexture) gl.deleteTexture(this.cellsTexture);
			if (this.blueTexture) gl.deleteTexture(this.blueTexture);
			if (this.framebuffer) gl.deleteFramebuffer(this.framebuffer);
			for (const gpu of this.gpuTextures) if (gpu) gl.deleteTexture(gpu.tex);
		}
		this.fieldProgram = null;
		this.ditherProgram = null;
		this.cellsTexture = null;
		this.blueTexture = null;
		this.framebuffer = null;
		this.cellsSize = { w: 0, h: 0 };
		this.gpuTextures = [];
	}

	private compile(
		gl: WebGL2RenderingContext,
		type: number,
		source: string,
		describeFailure?: (log: string) => string,
	): WebGLShader | null {
		const shader = gl.createShader(type);
		if (!shader) return null;
		gl.shaderSource(shader, source);
		gl.compileShader(shader);
		if (!gl.getShaderParameter(shader, gl.COMPILE_STATUS)) {
			const log = gl.getShaderInfoLog(shader) ?? "";
			const message =
				describeFailure?.(log) ?? `[dither] shader compile failed\n${log}`;
			errorOnce(message, message);
			gl.deleteShader(shader);
			return null;
		}
		return shader;
	}

	private link(
		gl: WebGL2RenderingContext,
		fragment: string,
		describeFailure?: (log: string) => string,
	): WebGLProgram | null {
		const vs = this.compile(gl, gl.VERTEX_SHADER, VERTEX_SOURCE);
		const fs = this.compile(gl, gl.FRAGMENT_SHADER, fragment, describeFailure);
		const program = gl.createProgram();
		if (!vs || !fs || !program) {
			if (vs) gl.deleteShader(vs);
			if (fs) gl.deleteShader(fs);
			if (program) gl.deleteProgram(program);
			return null;
		}
		gl.attachShader(program, vs);
		gl.attachShader(program, fs);
		gl.linkProgram(program);
		gl.deleteShader(vs);
		gl.deleteShader(fs);
		if (!gl.getProgramParameter(program, gl.LINK_STATUS)) {
			const log = gl.getProgramInfoLog(program) ?? "";
			const message = `[dither] program link failed\n${log}`;
			errorOnce(message, message);
			gl.deleteProgram(program);
			return null;
		}
		return program;
	}

	private linkDither(gl: WebGL2RenderingContext): DitherProgram | null {
		const program = this.link(gl, DITHER_FRAGMENT);
		if (!program) {
			this.broken = true;
			this.report("fallback");
			return null;
		}
		const at = (name: string) => gl.getUniformLocation(program, name);
		return {
			program,
			uCells: at("u_cells"),
			uGrid: at("u_grid"),
			uSub: at("u_sub"),
			uLevels: at("u_levels"),
			uN: at("u_n"),
			uBlue: at("u_blue"),
			uShape: at("u_shape"),
			uHasColor: at("u_hasColor"),
			uPalN: at("u_palN"),
			uPal: at("u_pal"),
		};
	}

	private compileField(): void {
		const gl = this.gl;
		const sources = this.sources;
		if (!gl || !sources || this.lost) return;
		if (!this.ditherProgram) this.ditherProgram = this.linkDither(gl);
		const fragment = assembleFieldFragment(sources);
		const program = this.link(gl, fragment.source, (log) =>
			describeFieldCompileFailure(log, fragment),
		);
		if (!program) {
			this.broken = true;
			this.report("fallback");
			return;
		}
		if (this.fieldProgram) gl.deleteProgram(this.fieldProgram.program);
		const at = (name: string) => gl.getUniformLocation(program, name);
		this.fieldProgram = {
			program,
			uRes: at("u_res"),
			uAspect: at("u_aspect"),
			uTime: at("u_time"),
			uPointer: at("u_pointer"),
			uCell: at("u_cell"),
			uPalN: at("u_palN"),
			uPal: at("u_pal"),
			user: sources.uniforms.map((spec) => at(spec.name)),
			samplers: sources.textures.map((name) => at(name)),
		};
		gl.useProgram(program);
		sources.textures.forEach((_, i) => {
			gl.uniform1i(this.fieldProgram?.samplers[i] ?? null, 1 + i);
		});
		this.broken = this.ditherProgram === null;
		this.uploadTextures(true);
	}

	/** Upload the 64×64 blue-noise tile on first use (and again after a context restore). */
	private ensureBlue(gl: WebGL2RenderingContext): boolean {
		if (this.blueTexture) return true;
		const tex = gl.createTexture();
		if (!tex) return false;
		gl.activeTexture(gl.TEXTURE1);
		gl.bindTexture(gl.TEXTURE_2D, tex);
		gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1);
		gl.texImage2D(
			gl.TEXTURE_2D,
			0,
			gl.R8,
			BLUE_NOISE_SIDE,
			BLUE_NOISE_SIDE,
			0,
			gl.RED,
			gl.UNSIGNED_BYTE,
			blueNoiseBytes(),
		);
		gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST);
		gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST);
		gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.REPEAT);
		gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.REPEAT);
		this.blueTexture = tex;
		return true;
	}

	private allocateCells(): void {
		const gl = this.gl;
		const layout = this.layout;
		if (!gl || !layout || this.lost) return;
		if (
			this.cellsTexture &&
			this.cellsSize.w === layout.cols &&
			this.cellsSize.h === layout.rows
		) {
			return;
		}
		if (!this.cellsTexture) {
			this.cellsTexture = gl.createTexture();
			this.framebuffer = gl.createFramebuffer();
		}
		gl.activeTexture(gl.TEXTURE0);
		gl.bindTexture(gl.TEXTURE_2D, this.cellsTexture);
		gl.texImage2D(
			gl.TEXTURE_2D,
			0,
			gl.RGBA8,
			layout.cols,
			layout.rows,
			0,
			gl.RGBA,
			gl.UNSIGNED_BYTE,
			null,
		);
		gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST);
		gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST);
		gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
		gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
		gl.bindFramebuffer(gl.FRAMEBUFFER, this.framebuffer);
		gl.framebufferTexture2D(
			gl.FRAMEBUFFER,
			gl.COLOR_ATTACHMENT0,
			gl.TEXTURE_2D,
			this.cellsTexture,
			0,
		);
		if (gl.checkFramebufferStatus(gl.FRAMEBUFFER) !== gl.FRAMEBUFFER_COMPLETE) {
			console.error("[dither] framebuffer incomplete");
			this.broken = true;
			this.report("fallback");
		}
		gl.bindFramebuffer(gl.FRAMEBUFFER, null);
		this.cellsSize = { w: layout.cols, h: layout.rows };
	}

	/** Upload user textures in the order the program declares them. */
	private uploadTextures(force = false): void {
		const gl = this.gl;
		const sources = this.sources;
		if (!gl || !sources || this.lost) return;
		const next: Array<GpuTexture | null> = [];
		sources.textures.forEach((name, i) => {
			const input = this.textureInputs[name];
			const existing = this.gpuTextures[i];
			if (!input) {
				next.push(null);
				return;
			}
			if (!force && existing && existing.input === input) {
				next.push(existing);
				return;
			}
			const tex = existing?.tex ?? gl.createTexture();
			if (!tex) {
				next.push(null);
				return;
			}
			gl.activeTexture(gl.TEXTURE1 + i);
			gl.bindTexture(gl.TEXTURE_2D, tex);
			gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1);
			gl.pixelStorei(gl.UNPACK_FLIP_Y_WEBGL, true);
			gl.texImage2D(
				gl.TEXTURE_2D,
				0,
				gl.R8,
				input.width,
				input.height,
				0,
				gl.RED,
				gl.UNSIGNED_BYTE,
				input.data,
			);
			gl.pixelStorei(gl.UNPACK_FLIP_Y_WEBGL, false);
			const filter = input.linear ? gl.LINEAR : gl.NEAREST;
			gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, filter);
			gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, filter);
			gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
			gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
			next.push({ tex, input });
		});
		// Drop textures for names that are no longer declared.
		for (let i = sources.textures.length; i < this.gpuTextures.length; i++) {
			const stale = this.gpuTextures[i];
			if (stale) gl.deleteTexture(stale.tex);
		}
		this.gpuTextures = next;
	}
}
