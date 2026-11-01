import { cn } from "cn";
import {
	type CSSProperties,
	type ReactNode,
	useEffect,
	useRef,
	useState,
} from "react";
import { bayerMatrixName, type DitherMatrix, matrixShaderCode } from "../bayer";
import {
	clampFps,
	DEFAULT_FPS,
	observeVisibility,
	useDitherFrame,
	usePrefersReducedMotion,
} from "../motion";
import {
	BUDGET_MARGIN,
	BUDGET_MARGIN_PX,
	contextBudget,
	proximityOf,
} from "./context-budget";
import {
	computeLayout,
	type GridLayout,
	parseCellValue,
	sameLayout,
} from "./layout";
import {
	cssColorExpr,
	DEFAULT_PALETTE,
	MAX_PALETTE,
	packPalette,
	resolveLevels,
	useResolvedPalette,
} from "./palette";
import { clampPointer, easePointer } from "./pointer";
import {
	DitherRenderer,
	type RendererStatus,
	type TextureInputs,
} from "./renderer";
import {
	describeUniforms,
	type UniformRecord,
	uniformKey,
	uniformValuesKey,
} from "./uniforms";

/** Debounce for ResizeObserver redraws, ms. The first measurement is immediate. */
const RESIZE_DEBOUNCE_MS = 100;

const DEFAULT_POINTER_REST: readonly [number, number] = [0.5, 0.5];

/** How long a shader waits for its first frame before its fallback shows, ms. */
const PENDING_MS = 1200;

/** The first frame fades in; reduced motion skips it. */
const FADE_IN = "opacity 300ms cubic-bezier(0.23, 1, 0.32, 1)";

/**
 * `near` and `visible` from the host's box in the window, the same test the
 * intersection observers make (`BUDGET_MARGIN` vertically, none sideways),
 * without clipping by scrolling ancestors; the observers correct that.
 */
function measureProximity(host: HTMLElement): {
	near: boolean;
	visible: boolean;
} {
	const rect = host.getBoundingClientRect();
	const height = window.innerHeight;
	const inside =
		rect.width > 0 &&
		rect.height > 0 &&
		rect.right > 0 &&
		rect.left < window.innerWidth;
	return {
		near:
			inside &&
			rect.bottom > -BUDGET_MARGIN_PX &&
			rect.top < height + BUDGET_MARGIN_PX,
		visible: inside && rect.bottom > 0 && rect.top < height,
	};
}

export interface DitherShaderProps {
	/** GLSL defining `float field(vec2 uv, float t)` -> tone 0..1. uv is 0..1 with y up; `u_aspect` is width / height. */
	field: string;
	/** Optional GLSL `vec3 colorField(vec2 uv, float t, float tone)` for multi-hue dithers. */
	colorField?: string | undefined;
	/** Each key becomes a GLSL uniform: number -> float, tuples -> vec2/3/4. */
	uniforms?: UniformRecord | undefined;
	/** Single-channel textures exposed to the field as `uniform sampler2D <name>`. */
	textures?: TextureInputs | undefined;
	/** CSS colours (or `--tokens`) ramped low -> high tone. Default paper -> ink. */
	palette?: readonly string[] | undefined;
	/** Dither cell in CSS px. Default `--dither-cell` (3). */
	cell?: number | undefined;
	/** Bayer matrix size. Default 4. Ignored when `matrix` is set. */
	n?: 2 | 4 | 8 | undefined;
	/** Threshold matrix: `bayer2`, `bayer4` (default), `bayer8`, or `blue-noise` (a 64×64 tile, no visible grid). Wins over `n`. */
	matrix?: DitherMatrix | undefined;
	/**
	 * Tone steps before dithering: a number (default 6), `"full"` for every
	 * tone the matrix draws, or `"stops"` for the fewest steps at or above the
	 * default that land exactly on the palette's colours.
	 */
	levels?: number | "full" | "stops" | undefined;
	shape?: "square" | "dot" | undefined;
	/** Frame rate, default 24, capped at 30. */
	fps?: number | undefined;
	/** `false` holds a static frame. Default true. */
	animate?: boolean | undefined;
	/** The moment (seconds) drawn when static or under reduced motion. */
	staticTime?: number | undefined;
	/** Alias of `staticTime`; wins when both are set. */
	stillTime?: number | undefined;
	/** Time multiplier; changing it never jumps the motion. Default 1. */
	speed?: number | undefined;
	/** Expose `u_pointer` (0..1, y up, eased) to the field. */
	pointer?: boolean | undefined;
	/** Where `u_pointer` settles when the pointer leaves the page, the window loses focus or a touch ends. Default centre. */
	pointerRest?: readonly [number, number] | undefined;
	className?: string | undefined;
	style?: CSSProperties | undefined;
	/**
	 * Shown when WebGL2 is unavailable, when the context is lost and not yet
	 * replaced, when the shader is far from the viewport, and when it waited
	 * over a second for its first frame. Until then the host is empty
	 * (`data-state="pending"`) and the first frame fades in. Default: a CSS
	 * gradient of the palette.
	 */
	fallback?: ReactNode;
	"aria-hidden"?: boolean | undefined;
}

function PaletteFallback({ palette }: { palette: readonly string[] }) {
	const stops = palette.slice(0, MAX_PALETTE).map(cssColorExpr).join(", ");
	return (
		<div
			className="absolute inset-0 opacity-55"
			style={{ backgroundImage: `linear-gradient(to top, ${stops})` }}
		/>
	);
}

/**
 * A full-bleed dither drawn on the GPU: your `field()` gives a tone, the engine
 * turns it into ordered-dither cells in the palette. Animation runs on the
 * shared clock and stops when off-screen, in a hidden tab or under reduced
 * motion. The host fills its positioned box; put content in a sibling.
 */
export function DitherShader({
	field,
	colorField,
	uniforms,
	textures,
	palette,
	cell,
	n = 4,
	matrix,
	levels,
	shape = "square",
	fps = DEFAULT_FPS,
	animate = true,
	staticTime: staticTimeProp = 0,
	stillTime,
	speed = 1,
	pointer = false,
	pointerRest = DEFAULT_POINTER_REST,
	className,
	style,
	fallback,
	"aria-hidden": ariaHidden = true,
}: DitherShaderProps) {
	const staticTime = stillTime ?? staticTimeProp;
	const matrixName = matrix ?? bayerMatrixName(n);
	const matrixCode = matrixShaderCode(matrixName);
	const hostRef = useRef<HTMLDivElement>(null);
	const canvasRef = useRef<HTMLCanvasElement>(null);
	const rendererRef = useRef<DitherRenderer | null>(null);
	const [status, setStatus] = useState<RendererStatus>("pending");
	const [unsupported, setUnsupported] = useState(false);
	// Whether the shader is near or on screen (it wants a slot), and whether
	// the budget has granted it one.
	const [wants, setWants] = useState(true);
	const [held, setHeld] = useState(false);
	const colors = useResolvedPalette(hostRef, palette);
	const levelCount = resolveLevels(levels, colors.length, matrixName);
	const [restX, restY] = clampPointer(pointerRest[0], pointerRest[1]);

	// Latest values for callbacks that outlive a render.
	const timeRef = useRef(staticTime);
	const layoutRef = useRef<GridLayout | null>(null);
	const cellRef = useRef(cell);
	cellRef.current = cell;
	const shapeRef = useRef(shape);
	shapeRef.current = shape;
	const speedRef = useRef(speed);
	speedRef.current = speed;
	const pointerOn = useRef(pointer);
	pointerOn.current = pointer;
	const staticTimeRef = useRef(staticTime);
	staticTimeRef.current = staticTime;
	const pointerTarget = useRef({ x: restX, y: restY });
	const pointerNow = useRef({ x: restX, y: restY });
	const restRef = useRef({ x: restX, y: restY });
	restRef.current.x = restX;
	restRef.current.y = restY;
	const resting = useRef(true);
	const paletteScratch = useRef(new Float32Array(MAX_PALETTE * 4));

	const draw = useRef(() => {});
	draw.current = () => {
		rendererRef.current?.draw(
			timeRef.current,
			pointerNow.current.x,
			pointerNow.current.y,
		);
	};

	const measure = useRef(() => {});
	measure.current = () => {
		const host = hostRef.current;
		const renderer = rendererRef.current;
		if (!host || !renderer) return;
		const w = host.clientWidth;
		const h = host.clientHeight;
		if (w === 0 || h === 0) return;
		const cellPx =
			cellRef.current ??
			parseCellValue(getComputedStyle(host).getPropertyValue("--dither-cell"));
		const layout = computeLayout(
			w,
			h,
			cellPx,
			window.devicePixelRatio,
			shapeRef.current,
		);
		if (sameLayout(layoutRef.current, layout)) return;
		layoutRef.current = layout;
		renderer.setLayout(layout);
		draw.current();
	};

	// Renderer lifetime, resizing, and handing the context back while far away.
	useEffect(() => {
		const host = hostRef.current;
		const canvas = canvasRef.current;
		if (!host || !canvas) return;
		// The page-wide context budget (at most MAX_LIVE_CONTEXTS live): the
		// renderer creates no context itself, and holds one only while granted
		// a slot, which it is within BUDGET_MARGIN of the viewport.
		const member = {
			grant: () => {
				renderer.reacquire();
				setHeld(renderer.isHeld);
			},
			revoke: () => {
				renderer.release();
				setHeld(false);
			},
		};
		const renderer = new DitherRenderer(canvas, {
			onStatus: setStatus,
			onRebuilt: () => draw.current(),
			onUnsupported: () => {
				setUnsupported(true);
				setHeld(false);
				contextBudget.remove(member);
			},
			onExternalLoss: () => {
				setHeld(false);
				contextBudget.lose(member);
			},
		});
		rendererRef.current = renderer;
		layoutRef.current = null;

		let timer = 0;
		let first = true;
		const observer = new ResizeObserver(() => {
			if (first) {
				first = false;
				measure.current();
				return;
			}
			window.clearTimeout(timer);
			timer = window.setTimeout(() => measure.current(), RESIZE_DEBOUNCE_MS);
		});
		observer.observe(host);

		// Where the host is right now, measured here: the first grant must not
		// wait for the observers' first report, which can come a frame or more late.
		let { near, visible } = measureProximity(host);
		const start = proximityOf(near, visible);
		setWants(start !== "far");
		contextBudget.add(member, false, start);
		// A renderer without a slot starts on its fallback; one with a slot
		// reported "pending" while it was granted. Either replaces a status the
		// renderer before it (a StrictMode remount) left behind.
		if (!renderer.isHeld) setStatus("fallback");
		const report = () => {
			const proximity = proximityOf(near, visible);
			setWants(proximity !== "far");
			contextBudget.setProximity(member, proximity);
		};
		const stopNear = observeVisibility(host, BUDGET_MARGIN, (value) => {
			near = value;
			report();
		});
		const stopVisible = observeVisibility(host, "0px", (value) => {
			visible = value;
			report();
		});

		return () => {
			window.clearTimeout(timer);
			observer.disconnect();
			stopNear();
			stopVisible();
			contextBudget.remove(member);
			renderer.dispose();
			rendererRef.current = null;
			layoutRef.current = null;
		};
	}, []);

	// Program: recompile only when the GLSL, uniform shape or texture names change.
	const specs = describeUniforms(uniforms);
	const specKey = uniformKey(specs);
	const textureNames = textures ? Object.keys(textures).sort() : [];
	const textureKey = textureNames.join(",");
	// biome-ignore lint/correctness/useExhaustiveDependencies: specKey and textureKey stand for specs and textureNames.
	useEffect(() => {
		const renderer = rendererRef.current;
		if (!renderer) return;
		renderer.setTextures(textures ?? {});
		renderer.setSources({
			field,
			colorField,
			uniforms: specs,
			textures: textureNames,
		});
		draw.current();
	}, [field, colorField, specKey, textureKey]);

	useEffect(() => {
		const renderer = rendererRef.current;
		if (!renderer) return;
		renderer.setTextures(textures ?? {});
		draw.current();
	}, [textures]);

	// Uniform values: no recompile, redraw so static frames follow props.
	const valuesKey = uniformValuesKey(uniforms);
	// biome-ignore lint/correctness/useExhaustiveDependencies: valuesKey stands for uniforms.
	useEffect(() => {
		const renderer = rendererRef.current;
		if (!renderer) return;
		renderer.setUniformValues(uniforms);
		draw.current();
	}, [valuesKey]);

	useEffect(() => {
		const renderer = rendererRef.current;
		if (!renderer) return;
		renderer.setPalette(packPalette(colors, paletteScratch.current));
		draw.current();
	}, [colors]);

	useEffect(() => {
		const renderer = rendererRef.current;
		if (!renderer) return;
		renderer.setParams({ n: matrixCode, levels: levelCount, shape });
		draw.current();
	}, [matrixCode, levelCount, shape]);

	// A new cell size or shape changes the grid.
	// biome-ignore lint/correctness/useExhaustiveDependencies: both are read through refs by measure.
	useEffect(() => {
		measure.current();
	}, [cell, shape]);

	// Pointer: window-level so it works under content that blocks the host.
	// Leaving the page, losing focus or lifting a finger sends the target home.
	useEffect(() => {
		if (!pointer) return;
		const rest = () => {
			resting.current = true;
			pointerTarget.current.x = restRef.current.x;
			pointerTarget.current.y = restRef.current.y;
		};
		const onMove = (event: PointerEvent) => {
			const host = hostRef.current;
			if (!host) return;
			const rect = host.getBoundingClientRect();
			if (rect.width === 0 || rect.height === 0) return;
			resting.current = false;
			const [x, y] = clampPointer(
				(event.clientX - rect.left) / rect.width,
				1 - (event.clientY - rect.top) / rect.height,
			);
			pointerTarget.current.x = x;
			pointerTarget.current.y = y;
		};
		const onRelease = (event: PointerEvent) => {
			if (event.pointerType === "touch") rest();
		};
		const root = document.documentElement;
		window.addEventListener("pointermove", onMove, { passive: true });
		window.addEventListener("pointerup", onRelease);
		window.addEventListener("pointercancel", onRelease);
		window.addEventListener("blur", rest);
		root.addEventListener("pointerleave", rest);
		root.addEventListener("mouseleave", rest);
		return () => {
			window.removeEventListener("pointermove", onMove);
			window.removeEventListener("pointerup", onRelease);
			window.removeEventListener("pointercancel", onRelease);
			window.removeEventListener("blur", rest);
			root.removeEventListener("pointerleave", rest);
			root.removeEventListener("mouseleave", rest);
		};
	}, [pointer]);

	// A new rest point moves the target only while the pointer is away.
	useEffect(() => {
		if (!resting.current) return;
		pointerTarget.current.x = restX;
		pointerTarget.current.y = restY;
	}, [restX, restY]);

	const frameState = useDitherFrame(
		(_time, delta, still) => {
			if (still === "reduced") timeRef.current = staticTimeRef.current;
			else if (!still) timeRef.current += delta * speedRef.current;
			if (pointerOn.current && delta > 0) {
				easePointer(pointerNow.current, pointerTarget.current, delta);
			}
			draw.current();
		},
		{
			fps: clampFps(fps),
			enabled: animate && !unsupported,
			target: hostRef,
			stillTime: staticTime,
		},
	);

	// What the host shows while no frame is drawn: nothing (`pending`) while a
	// context is on its way, the fallback once none is (or after waiting too long).
	const hoping =
		!unsupported &&
		status !== "ready" &&
		(status === "pending" || (wants && !held));
	const [expired, setExpired] = useState(false);
	useEffect(() => {
		if (!hoping) {
			setExpired(false);
			return;
		}
		const timer = window.setTimeout(() => setExpired(true), PENDING_MS);
		return () => window.clearTimeout(timer);
	}, [hoping]);
	const reducedMotion = usePrefersReducedMotion();

	const dataState = unsupported
		? "fallback"
		: status === "ready"
			? frameState
			: hoping && !expired
				? "pending"
				: "fallback";
	const fallbackPalette =
		palette && palette.length > 0 ? palette : DEFAULT_PALETTE;

	return (
		<div
			ref={hostRef}
			data-slot="dither-shader"
			data-state={dataState}
			aria-hidden={ariaHidden}
			className={cn("relative overflow-hidden forced-colors:hidden", className)}
			style={style}
		>
			{/* The renderer may swap the canvas for a fresh one (a lost context), so
			    the fade sits on this wrapper, which React owns. */}
			<div
				className="pointer-events-none absolute inset-0"
				style={{
					opacity: status === "ready" ? 1 : 0,
					transition: status === "ready" && !reducedMotion ? FADE_IN : "none",
				}}
			>
				<canvas
					ref={canvasRef}
					className="absolute top-0 left-0 block [image-rendering:pixelated]"
				/>
			</div>
			{dataState === "fallback" ? (
				<div className="absolute inset-0">
					{fallback ?? <PaletteFallback palette={fallbackPalette} />}
				</div>
			) : null}
		</div>
	);
}
