import { afterEach, beforeEach, describe, expect, spyOn, test } from "bun:test";
import { BLUE_NOISE_SIDE, blueNoiseBytes } from "../blue-noise";
import { resetLogOnce } from "./dev";
import { computeLayout } from "./layout";
import { MAX_PALETTE, packPalette } from "./palette";
import { DitherRenderer, type RendererStatus } from "./renderer";
import type { FieldProgramSources } from "./shader-source";

type Call = { name: string; args: unknown[] };
const FRAMEBUFFER_COMPLETE = 36053;

/** What the fake driver should do; mutate between steps to simulate failures. */
interface Behaviour {
	/** Compile result per shader, by the shader source it received. */
	compileOk: (source: string) => boolean;
	linkOk: boolean;
	framebufferComplete: boolean;
	createShader: boolean;
	loseExtension: boolean;
	restoreThrows: boolean;
}

/**
 * A WebGL2 that records every call. Constants get distinct numbers (with the
 * real TEXTURE0), handles are tagged objects, uniform locations carry their
 * name so tests can say which uniform a value went to.
 */
function fakeGl(behaviour: Behaviour) {
	const log: Call[] = [];
	const state = { lost: false };
	const constants = new Map<string, number>([
		["TEXTURE0", 33984],
		["TEXTURE1", 33985],
		["FRAMEBUFFER_COMPLETE", FRAMEBUFFER_COMPLETE],
	]);
	let handle = 0;
	const shaderSources = new Map<object, string>();
	const extension = {
		loseContext: () => log.push({ name: "loseContext", args: [] }),
		restoreContext: () => {
			log.push({ name: "restoreContext", args: [] });
			if (behaviour.restoreThrows) throw new Error("restore failed");
		},
	};
	const methods: Record<string, (...args: unknown[]) => unknown> = {
		isContextLost: () => state.lost,
		getExtension: (name) =>
			name === "WEBGL_lose_context" && behaviour.loseExtension
				? extension
				: null,
		createShader: (type) =>
			behaviour.createShader ? { kind: "shader", type, id: ++handle } : null,
		shaderSource: (shader, source) => {
			shaderSources.set(shader as object, source as string);
		},
		getShaderParameter: (shader) =>
			behaviour.compileOk(shaderSources.get(shader as object) ?? ""),
		getShaderInfoLog: () => "0:7: 'bad' : syntax error",
		createProgram: () => ({ kind: "program", id: ++handle }),
		getProgramParameter: () => behaviour.linkOk,
		getProgramInfoLog: () => "link says no",
		getUniformLocation: (_program, name) => ({ uniform: name }),
		createTexture: () => ({ kind: "texture", id: ++handle }),
		createFramebuffer: () => ({ kind: "framebuffer", id: ++handle }),
		checkFramebufferStatus: () =>
			behaviour.framebufferComplete ? FRAMEBUFFER_COMPLETE : 0,
	};
	const gl = new Proxy({} as Record<string, unknown>, {
		get(_target, prop) {
			if (typeof prop !== "string") return undefined;
			if (/^[A-Z][A-Z0-9_]*$/.test(prop)) {
				if (!constants.has(prop)) constants.set(prop, 1000 + constants.size);
				return constants.get(prop);
			}
			return (...args: unknown[]) => {
				log.push({ name: prop, args });
				return methods[prop]?.(...args);
			};
		},
	}) as unknown as WebGL2RenderingContext;
	const constant = (name: string) => gl[name as keyof WebGL2RenderingContext];
	return {
		gl,
		log,
		state,
		constant,
		shaderSources,
		calls: (name: string) => log.filter((c) => c.name === name),
		uniform: (type: string, uniformName: string) =>
			log
				.filter(
					(c) =>
						c.name === type &&
						(c.args[0] as { uniform?: string } | null)?.uniform === uniformName,
				)
				.map((c) => c.args.slice(1)),
	};
}

class Canvas {
	width = 0;
	height = 0;
	style: Record<string, string> = {};
	isConnected = true;
	context: ReturnType<typeof fakeGl> | null = null;
	listeners = new Map<string, Set<(e: Event) => void>>();
	getContext = () => this.context?.gl ?? null;
	addEventListener(type: string, fn: (e: Event) => void) {
		if (!this.listeners.has(type)) this.listeners.set(type, new Set());
		this.listeners.get(type)?.add(fn);
	}
	removeEventListener(type: string, fn: (e: Event) => void) {
		this.listeners.get(type)?.delete(fn);
	}
	dispatch(type: string) {
		for (const fn of [...(this.listeners.get(type) ?? [])])
			fn({ preventDefault() {} } as Event);
	}
	listenerCount() {
		let n = 0;
		for (const set of this.listeners.values()) n += set.size;
		return n;
	}
}

const FIELD = "float field(vec2 uv, float t) { return uv.x; }";
const sources = (
	over: Partial<FieldProgramSources> = {},
): FieldProgramSources => ({
	field: FIELD,
	colorField: undefined,
	uniforms: [],
	textures: [],
	...over,
});

let behaviour: Behaviour;
beforeEach(() => {
	resetLogOnce();
	behaviour = {
		compileOk: () => true,
		linkOk: true,
		framebufferComplete: true,
		createShader: true,
		loseExtension: true,
		restoreThrows: false,
	};
});
afterEach(() => resetLogOnce());

function setup() {
	const canvas = new Canvas();
	const gl = fakeGl(behaviour);
	canvas.context = gl;
	const statuses: RendererStatus[] = [];
	const events = { rebuilt: 0, unsupported: 0, externalLoss: 0 };
	const renderer = new DitherRenderer(canvas as unknown as HTMLCanvasElement, {
		onStatus: (s) => statuses.push(s),
		onRebuilt: () => {
			events.rebuilt += 1;
		},
		onUnsupported: () => {
			events.unsupported += 1;
		},
		onExternalLoss: () => {
			events.externalLoss += 1;
		},
	});
	return { canvas, gl, renderer, statuses, events };
}

const layout = computeLayout(120, 60, 3, 1, "square");

/** A renderer that holds a context and is ready to draw. */
function ready(src: FieldProgramSources = sources()) {
	const ctx = setup();
	ctx.renderer.setSources(src);
	ctx.renderer.setLayout(layout);
	ctx.renderer.reacquire();
	return ctx;
}

describe("DitherRenderer building", () => {
	test("sizes the canvas to the layout's buffer and CSS box", () => {
		const { canvas, renderer } = setup();
		renderer.setLayout(layout);
		expect(canvas.width).toBe(layout.canvasW);
		expect(canvas.height).toBe(layout.canvasH);
		expect(canvas.style.width).toBe(`${layout.cssW}px`);
		expect(canvas.style.height).toBe(`${layout.cssH}px`);
	});

	test("links the dither pass and the field pass with the author's GLSL", () => {
		const { gl, statuses } = ready();
		expect(gl.calls("createProgram")).toHaveLength(2);
		const fragments = [...gl.shaderSources.values()].filter((s) =>
			s.includes("precision highp float"),
		);
		expect(fragments.some((s) => s.includes("uv.x"))).toBe(true);
		expect(fragments.some((s) => s.includes("u_cells"))).toBe(true);
		expect(statuses).toEqual(["pending"]);
	});

	test("a field recompile replaces the old program", () => {
		const { gl, renderer } = ready();
		const before = gl.calls("deleteProgram").length;
		renderer.setSources(
			sources({ field: "float field(vec2 uv, float t) { return uv.y; }" }),
		);
		expect(gl.calls("createProgram")).toHaveLength(3);
		expect(gl.calls("deleteProgram").length).toBe(before + 1);
	});

	test("allocates the cells texture once and again only when the grid changes", () => {
		const { gl, renderer } = ready();
		const allocations = () =>
			gl.calls("texImage2D").filter((c) => c.args[3] === layout.cols).length;
		expect(allocations()).toBe(1);
		renderer.setLayout(layout);
		expect(allocations()).toBe(1);
		const bigger = computeLayout(240, 120, 3, 1, "square");
		renderer.setLayout(bigger);
		const sizes = gl.calls("texImage2D").map((c) => [c.args[3], c.args[4]]);
		expect(sizes).toContainEqual([bigger.cols, bigger.rows]);
	});

	test("an incomplete framebuffer is reported and nothing draws", () => {
		behaviour.framebufferComplete = false;
		const error = spyOn(console, "error").mockImplementation(() => {});
		try {
			const { renderer, statuses } = ready();
			expect(error).toHaveBeenCalledWith("[dither] framebuffer incomplete");
			expect(statuses).toContain("fallback");
			expect(renderer.draw(0, 0, 0)).toBe(false);
		} finally {
			error.mockRestore();
		}
	});

	test("a field that does not compile falls back and logs the compiler's complaint once", () => {
		behaviour.compileOk = (src) => !src.includes("uv.x");
		const error = spyOn(console, "error").mockImplementation(() => {});
		try {
			const { renderer, statuses } = ready();
			expect(statuses).toContain("fallback");
			expect(renderer.draw(0, 0, 0)).toBe(false);
			expect(error).toHaveBeenCalledTimes(1);
			expect(String(error.mock.calls[0]?.[0])).toContain("syntax error");
			// Recompiling the same broken field logs nothing new.
			renderer.setSources(sources());
			expect(error).toHaveBeenCalledTimes(1);
		} finally {
			error.mockRestore();
		}
	});

	test("a field that compiles after a broken one clears the failure", () => {
		behaviour.compileOk = (src) => !src.includes("uv.x");
		const error = spyOn(console, "error").mockImplementation(() => {});
		try {
			const { renderer } = ready();
			expect(renderer.draw(0, 0, 0)).toBe(false);
			renderer.setSources(
				sources({ field: "float field(vec2 uv, float t) { return 0.5; }" }),
			);
			expect(renderer.draw(0, 0, 0)).toBe(true);
		} finally {
			error.mockRestore();
		}
	});

	test("a failed dither pass falls back for good", () => {
		behaviour.compileOk = (src) => !src.includes("u_cells");
		const error = spyOn(console, "error").mockImplementation(() => {});
		try {
			const { renderer, statuses } = ready();
			expect(statuses).toContain("fallback");
			expect(renderer.draw(0, 0, 0)).toBe(false);
		} finally {
			error.mockRestore();
		}
	});

	test("a program that fails to link is reported", () => {
		behaviour.linkOk = false;
		const error = spyOn(console, "error").mockImplementation(() => {});
		try {
			const { renderer, statuses } = ready();
			expect(statuses).toContain("fallback");
			expect(renderer.draw(0, 0, 0)).toBe(false);
			expect(String(error.mock.calls[0]?.[0])).toContain("link says no");
		} finally {
			error.mockRestore();
		}
	});

	test("without a shader object nothing links and the renderer falls back", () => {
		behaviour.createShader = false;
		const { renderer, statuses, gl } = ready();
		expect(statuses).toContain("fallback");
		expect(gl.calls("linkProgram")).toHaveLength(0);
		expect(renderer.draw(0, 0, 0)).toBe(false);
	});

	test("warns once when the field redefines a function the engine provides", () => {
		const warn = spyOn(console, "warn").mockImplementation(() => {});
		try {
			const { renderer } = setup();
			const redefining = sources({
				field: `float hash21(vec2 p) { return 0.5; }\n${FIELD}`,
			});
			renderer.setSources(redefining);
			renderer.setSources(redefining);
			expect(warn).toHaveBeenCalledTimes(1);
			expect(String(warn.mock.calls[0]?.[0])).toContain("hash21");
		} finally {
			warn.mockRestore();
		}
	});
});

describe("DitherRenderer draw", () => {
	test("draws nothing before it has a context, a layout or sources", () => {
		const bare = setup();
		expect(bare.renderer.draw(0, 0, 0)).toBe(false);
		bare.renderer.setSources(sources());
		bare.renderer.setLayout(layout);
		expect(bare.renderer.draw(0, 0, 0)).toBe(false);
	});

	test("stage 1 renders the field one pixel per cell with time, pointer and palette", () => {
		const { gl, renderer, statuses } = ready();
		renderer.setPalette(
			packPalette([
				[0, 0, 0, 1],
				[0.5, 0.5, 0.5, 1],
				[1, 1, 1, 1],
			]),
		);
		gl.log.length = 0;
		expect(renderer.draw(2.5, 0.25, 0.75)).toBe(true);

		const names = gl.log.map((c) => c.name);
		const firstViewport = gl.calls("viewport")[0];
		expect(firstViewport?.args).toEqual([0, 0, layout.cols, layout.rows]);
		expect(gl.uniform("uniform2f", "u_res")).toEqual([
			[layout.cols, layout.rows],
		]);
		expect(gl.uniform("uniform1f", "u_aspect")).toEqual([[layout.aspect]]);
		expect(gl.uniform("uniform1f", "u_time")).toEqual([[2.5]]);
		expect(gl.uniform("uniform2f", "u_pointer")).toEqual([[0.25, 0.75]]);
		expect(gl.uniform("uniform1f", "u_cell")).toEqual([[layout.cellCss]]);
		// Field pass and dither pass both see the three-colour palette.
		expect(gl.uniform("uniform1i", "u_palN")).toEqual([[3], [3]]);
		const pal = gl.uniform("uniform4fv", "u_pal")[0]?.[0] as Float32Array;
		expect(pal).toHaveLength(MAX_PALETTE * 4);
		expect(Array.from(pal.slice(4, 8))).toEqual([0.5, 0.5, 0.5, 1]);
		expect(gl.calls("drawArrays")).toHaveLength(2);
		// The field pass goes to the framebuffer, the dither pass to the screen.
		const binds = gl.calls("bindFramebuffer").map((c) => c.args[1]);
		expect(binds.at(-1)).toBeNull();
		expect(binds[binds.length - 2]).not.toBeNull();
		expect(names.indexOf("drawArrays")).toBeLessThan(
			names.lastIndexOf("drawArrays"),
		);
		expect(statuses.at(-1)).toBe("ready");
	});

	test("stage 2 dithers at the canvas size with the levels, matrix and shape", () => {
		const { gl, renderer } = ready();
		renderer.setParams({ n: 8, levels: 9, shape: "dot" });
		gl.log.length = 0;
		renderer.draw(0, 0, 0);
		const viewports = gl.calls("viewport").map((c) => c.args);
		expect(viewports[1]).toEqual([0, 0, layout.canvasW, layout.canvasH]);
		expect(gl.uniform("uniform2f", "u_grid")).toEqual([
			[layout.cols, layout.rows],
		]);
		expect(gl.uniform("uniform1f", "u_sub")).toEqual([[layout.sub]]);
		expect(gl.uniform("uniform1f", "u_levels")).toEqual([[9]]);
		expect(gl.uniform("uniform1f", "u_n")).toEqual([[8]]);
		expect(gl.uniform("uniform1f", "u_shape")).toEqual([[1]]);
		expect(gl.uniform("uniform1i", "u_blue")).toEqual([[0]]);
		expect(gl.uniform("uniform1f", "u_hasColor")).toEqual([[0]]);
	});

	test("a colour field turns the colour flag on and squares turn the shape flag off", () => {
		const { gl, renderer } = ready(
			sources({
				colorField:
					"vec3 colorField(vec2 uv, float t, float tone) { return vec3(tone); }",
			}),
		);
		gl.log.length = 0;
		renderer.draw(0, 0, 0);
		expect(gl.uniform("uniform1f", "u_hasColor")).toEqual([[1]]);
		expect(gl.uniform("uniform1f", "u_shape")).toEqual([[0]]);
	});

	test("the blue-noise matrix uploads its tile once and binds it to unit 1", () => {
		const { gl, renderer } = ready();
		renderer.setParams({ n: 64, levels: 6, shape: "square" });
		gl.log.length = 0;
		renderer.draw(0, 0, 0);
		renderer.draw(1, 0, 0);

		const uploads = gl
			.calls("texImage2D")
			.filter((c) => c.args[3] === BLUE_NOISE_SIDE);
		expect(uploads).toHaveLength(1);
		expect(uploads[0]?.args[8]).toEqual(blueNoiseBytes());
		expect(gl.calls("createTexture")).toHaveLength(1);
		expect(gl.uniform("uniform1i", "u_blue")).toEqual([[1], [1]]);
		expect(gl.calls("pixelStorei")[0]?.args[1]).toBe(1);
	});

	test("switching back to Bayer stops binding the noise tile", () => {
		const { gl, renderer } = ready();
		renderer.setParams({ n: 64, levels: 6, shape: "square" });
		renderer.draw(0, 0, 0);
		renderer.setParams({ n: 4, levels: 6, shape: "square" });
		gl.log.length = 0;
		renderer.draw(0, 0, 0);
		expect(gl.uniform("uniform1i", "u_blue")).toEqual([[0]]);
	});

	test("user uniforms are uploaded to their locations", () => {
		const { gl, renderer } = ready(
			sources({
				uniforms: [
					{ name: "speed", size: 1 },
					{ name: "center", size: 2 },
				],
			}),
		);
		renderer.setUniformValues({ speed: 0.75, center: [0.1, 0.9] });
		gl.log.length = 0;
		renderer.draw(0, 0, 0);
		expect(gl.uniform("uniform1f", "speed")).toEqual([[0.75]]);
		expect(gl.uniform("uniform2f", "center")).toEqual([[0.1, 0.9]]);
	});

	test("no uniform values means no user uniform uploads", () => {
		const { gl, renderer } = ready(
			sources({ uniforms: [{ name: "speed", size: 1 }] }),
		);
		renderer.setUniformValues(undefined);
		gl.log.length = 0;
		renderer.draw(0, 0, 0);
		expect(gl.uniform("uniform1f", "speed")).toEqual([]);
	});

	test("a lost context cannot draw", () => {
		const { gl, renderer } = ready();
		gl.state.lost = true;
		expect(renderer.draw(0, 0, 0)).toBe(false);
	});

	test("reports ready on the first successful draw only once", () => {
		const { renderer, statuses } = ready();
		renderer.draw(0, 0, 0);
		renderer.draw(1, 0, 0);
		expect(statuses).toEqual(["pending", "ready"]);
	});
});

describe("DitherRenderer textures", () => {
	const texSources = () => sources({ textures: ["u_mask", "u_height"] });
	const input = (over: Record<string, unknown> = {}) => ({
		width: 2,
		height: 2,
		data: new Uint8Array([0, 64, 128, 255]),
		...over,
	});
	const uploads = (gl: ReturnType<typeof fakeGl>) =>
		gl
			.calls("texImage2D")
			.filter((c) => c.args[2] === gl.constant("R8") && c.args[3] === 2);

	test("declared textures upload flipped as single-channel bytes", () => {
		const { gl, renderer } = ready(texSources());
		const mask = input();
		renderer.setTextures({ u_mask: mask });
		const [upload] = uploads(gl);
		expect(upload?.args.slice(3, 5)).toEqual([2, 2]);
		expect(upload?.args[8]).toBe(mask.data);
		// Flip Y only for the duration of the upload.
		const flips = gl
			.calls("pixelStorei")
			.filter((c) => c.args[0] === gl.constant("UNPACK_FLIP_Y_WEBGL"))
			.map((c) => c.args[1]);
		expect(flips).toEqual([true, false]);
		// Sampler units start at 1; unit 0 is the cells texture.
		expect(gl.uniform("uniform1i", "u_mask")).toEqual([[1]]);
		expect(gl.uniform("uniform1i", "u_height")).toEqual([[2]]);
	});

	test("the same input object is not uploaded twice, a new one is", () => {
		const { gl, renderer } = ready(texSources());
		const mask = input();
		renderer.setTextures({ u_mask: mask });
		renderer.setTextures({ u_mask: mask });
		expect(uploads(gl)).toHaveLength(1);
		renderer.setTextures({ u_mask: input() });
		expect(uploads(gl)).toHaveLength(2);
	});

	test("linear inputs filter linearly and others nearest", () => {
		const { gl, renderer } = ready(texSources());
		renderer.setTextures({
			u_mask: input({ linear: true }),
			u_height: input(),
		});
		const filters = gl
			.calls("texParameteri")
			.filter((c) => c.args[1] === gl.constant("TEXTURE_MIN_FILTER"))
			.map((c) => c.args[2]);
		// Cells texture (nearest), then mask (linear), then height (nearest).
		expect(filters.slice(-2)).toEqual([
			gl.constant("LINEAR"),
			gl.constant("NEAREST"),
		]);
	});

	test("textures the field does not declare are ignored; missing ones leave a gap", () => {
		const { gl, renderer } = ready(texSources());
		renderer.setTextures({ u_other: input(), u_height: input() });
		expect(uploads(gl)).toHaveLength(1);
		gl.log.length = 0;
		renderer.draw(0, 0, 0);
		// Only the declared, present texture is bound, on its own unit.
		const active = gl.calls("activeTexture").map((c) => c.args[0]);
		expect(active).toContain(33985 + 1);
		expect(active).not.toContain(33985);
	});

	test("drawing binds each declared texture to its unit", () => {
		const { gl, renderer } = ready(texSources());
		renderer.setTextures({ u_mask: input(), u_height: input() });
		gl.log.length = 0;
		renderer.draw(0, 0, 0);
		const active = gl.calls("activeTexture").map((c) => c.args[0]);
		expect(active.slice(0, 2)).toEqual([33985, 33985 + 1]);
	});

	test("a field that drops a texture deletes its GPU copy", () => {
		const { gl, renderer } = ready(texSources());
		renderer.setTextures({ u_mask: input(), u_height: input() });
		const before = gl.calls("deleteTexture").length;
		renderer.setSources(sources({ textures: ["u_mask"] }));
		expect(gl.calls("deleteTexture").length).toBe(before + 1);
	});

	test("a context restore uploads the textures again", () => {
		const { gl, renderer, canvas } = ready(texSources());
		renderer.setTextures({ u_mask: input() });
		expect(uploads(gl)).toHaveLength(1);
		renderer.release();
		canvas.dispatch("webglcontextlost");
		renderer.reacquire();
		canvas.dispatch("webglcontextrestored");
		// The old GPU textures died with the context: the same input is sent again.
		expect(uploads(gl).length).toBeGreaterThan(1);
	});
});

describe("DitherRenderer lifecycle details", () => {
	test("release without a grant does nothing", () => {
		const { renderer } = setup();
		expect(renderer.release()).toBe(false);
	});

	test("a grant takes a slot and listens for loss; release gives the slot back", () => {
		const { renderer, canvas } = setup();
		renderer.reacquire();
		expect(renderer.isHeld).toBe(true);
		expect(canvas.listenerCount()).toBe(2);
		expect(renderer.release()).toBe(true);
		expect(renderer.isHeld).toBe(false);
	});

	test("a context that cannot be handed back refuses release", () => {
		behaviour.loseExtension = false;
		const { renderer } = ready();
		expect(renderer.release()).toBe(false);
	});

	test("release loses the context through the extension; a second release is idle", () => {
		const { renderer, gl } = ready();
		expect(renderer.release()).toBe(true);
		expect(gl.calls("loseContext")).toHaveLength(1);
		expect(renderer.release()).toBe(false);
	});

	test("a grant after release restores through the extension", () => {
		const { renderer, canvas, gl, events } = ready();
		renderer.release();
		canvas.dispatch("webglcontextlost");
		renderer.reacquire();
		expect(gl.calls("restoreContext")).toHaveLength(1);
		canvas.dispatch("webglcontextrestored");
		expect(events.rebuilt).toBe(2);
		expect(renderer.draw(0, 0, 0)).toBe(true);
	});

	test("when restoreContext throws, a fresh canvas takes over", () => {
		behaviour.restoreThrows = true;
		const { renderer, canvas } = ready();
		let replaced: unknown = null;
		(canvas as unknown as { cloneNode: () => Canvas }).cloneNode = () => {
			const next = new Canvas();
			next.context = fakeGl(behaviour);
			replaced = next;
			return next;
		};
		(canvas as unknown as { replaceWith: (c: Canvas) => void }).replaceWith =
			() => {};
		renderer.release();
		canvas.dispatch("webglcontextlost");
		renderer.reacquire();
		expect(replaced).not.toBeNull();
		expect((replaced as Canvas).listenerCount()).toBe(2);
	});

	test("a context that is already dead when handed over is replaced", () => {
		const canvas = new Canvas();
		const dead = fakeGl(behaviour);
		dead.state.lost = true;
		canvas.context = dead;
		const fresh = new Canvas();
		fresh.context = fakeGl(behaviour);
		(canvas as unknown as { cloneNode: () => Canvas }).cloneNode = () => fresh;
		(canvas as unknown as { replaceWith: (c: Canvas) => void }).replaceWith =
			() => {};
		const rebuilt: number[] = [];
		const renderer = new DitherRenderer(
			canvas as unknown as HTMLCanvasElement,
			{
				onStatus: () => {},
				onRebuilt: () => rebuilt.push(1),
				onUnsupported: () => {},
				onExternalLoss: () => {},
			},
		);
		renderer.reacquire();
		expect(fresh.listenerCount()).toBe(2);
		expect(canvas.listenerCount()).toBe(0);
		expect(rebuilt).toEqual([1]);
	});

	test("dispose detaches listeners, frees resources and ignores later grants", () => {
		const timers: (() => void)[] = [];
		const setTimeoutSpy = spyOn(globalThis, "setTimeout").mockImplementation(((
			fn: () => void,
		) => {
			timers.push(fn);
			return 0;
		}) as never);
		try {
			const { renderer, canvas, gl } = ready();
			renderer.dispose();
			expect(canvas.listenerCount()).toBe(0);
			expect(gl.calls("deleteProgram")).toHaveLength(2);
			expect(gl.calls("deleteFramebuffer")).toHaveLength(1);
			expect(renderer.isHeld).toBe(false);
			expect(renderer.draw(0, 0, 0)).toBe(false);
			renderer.reacquire();
			expect(renderer.isHeld).toBe(false);

			// The context is freed only if the canvas really left the page.
			expect(gl.calls("loseContext")).toHaveLength(0);
			for (const run of timers) run();
			expect(gl.calls("loseContext")).toHaveLength(0);
		} finally {
			setTimeoutSpy.mockRestore();
		}
	});

	test("dispose frees the GPU context when the canvas was removed", () => {
		const timers: (() => void)[] = [];
		const setTimeoutSpy = spyOn(globalThis, "setTimeout").mockImplementation(((
			fn: () => void,
		) => {
			timers.push(fn);
			return 0;
		}) as never);
		try {
			const { renderer, canvas, gl } = ready();
			renderer.dispose();
			canvas.isConnected = false;
			for (const run of timers) run();
			expect(gl.calls("loseContext")).toHaveLength(1);
		} finally {
			setTimeoutSpy.mockRestore();
		}
	});

	test("dispose on a lost context releases nothing more", () => {
		const { renderer, gl } = ready();
		gl.state.lost = true;
		renderer.dispose();
		expect(gl.calls("deleteProgram")).toHaveLength(0);
	});
});
