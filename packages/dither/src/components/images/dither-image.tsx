import { cn } from "cn";
import {
	type CSSProperties,
	type ReactNode,
	useCallback,
	useEffect,
	useRef,
	useState,
} from "react";
import { ditherMaskStyle } from "../../lib/pattern";
import { useReducedMotionPref } from "../../lib/prefs";
import { DitherLayer } from "./dither-layer";
import { useInView } from "./hooks";
import type { DitherLook } from "./look";
import type { CrossOrigin, Dithered } from "./use-dithered";

export interface DitherImageProps extends DitherLook {
	src: string;
	/** Describes the picture. Pass `""` when the image is purely decorative: it is then hidden from assistive technology. */
	alt: string;
	/** Needed to read pixels of an image served from another origin. */
	crossOrigin?: CrossOrigin;
	/** Width ÷ height, reserved before the image loads. Default: the image's own ratio, once it loads. A height in `className` wins. */
	ratio?: number;
	/** Resolve from coarse blocks to full detail when the image first appears. Default true. */
	reveal?: boolean;
	/** Same as `reveal`. Wins over it when both are set. */
	resolve?: boolean;
	/** Delay between resolve steps, ms. Default 70. */
	revealStep?: number;
	/** Shown instead when the image cannot be read (CORS, 404). Default: the plain image. */
	fallback?: ReactNode;
	/** Receives the canvas once it is drawn, e.g. to hand to `exportPNG`. */
	onCanvas?: (canvas: HTMLCanvasElement | null) => void;
	className?: string;
	style?: CSSProperties;
}

/** Cell multipliers of the first resolve: 16× blocks down to full detail. */
const REVEAL_MULTIPLIERS: readonly number[] = [16, 8, 4, 2, 1];
const STILL_MULTIPLIERS: readonly number[] = [1];

type LayerState = Pick<Dithered, "status" | "natural"> & { count: number };

const NOT_LOADED: LayerState = { status: "idle", natural: null, count: 0 };

/**
 * Any photo dithered to a preset (`ink`, `duotone`, `newsprint`, `gameboy`,
 * `riso`) or your own palette, with crisp whole-pixel cells. Ordered Bayer and
 * blue noise, and error diffusion (Floyd–Steinberg, Atkinson, Sierra Lite) are
 * available; big images dither in a Web Worker and results are cached per
 * src + options. The image fills its box like `object-fit: cover`: give the
 * box a size or a `ratio`. It loads within 300px of the viewport, resolves
 * from coarse blocks to full detail the first time it appears, and redraws on
 * resize or theme change without resolving again. Reduced motion skips the
 * resolve. `data-state` is `idle`, `loading`, `resolving`, `ready` or `error`.
 */
export function DitherImage({
	src,
	alt,
	crossOrigin,
	ratio,
	reveal = true,
	resolve,
	revealStep = 70,
	fallback,
	onCanvas,
	className,
	style,
	...look
}: DitherImageProps): ReactNode {
	const boxRef = useRef<HTMLDivElement | null>(null);
	const reduced = useReducedMotionPref();
	const animated = (resolve ?? reveal) && !reduced;
	const near = useInView(boxRef, { rootMargin: "300px 0px", once: true });
	const [stage, setStage] = useState(0);
	const [layer, setLayer] = useState<LayerState>(NOT_LOADED);
	const resolvedFor = useRef<string | null>(null);
	const { count } = layer;

	const onState = useCallback((state: LayerState) => setLayer(state), []);

	// Step from coarse to fine with plain timers; no frame loop of our own.
	// Only the first appearance of a `src` resolves: later redraws show full detail.
	useEffect(() => {
		if (count === 0) {
			setStage(0);
			return;
		}
		if (!animated || resolvedFor.current === src) {
			resolvedFor.current = src;
			setStage(count - 1);
			return;
		}
		resolvedFor.current = src;
		setStage(0);
		let step = 0;
		const timer = setInterval(() => {
			step += 1;
			setStage(step);
			if (step >= count - 1) clearInterval(timer);
		}, revealStep);
		return () => clearInterval(timer);
	}, [animated, count, revealStep, src]);

	const failed = layer.status === "error";
	const resolving = layer.status === "ready" && count > 0 && stage < count - 1;
	const state = resolving ? "resolving" : layer.status;
	const decorative = alt === "";
	const natural = layer.natural;
	const aspectRatio =
		ratio ?? (natural ? natural.width / natural.height : undefined);

	return (
		<div
			ref={boxRef}
			{...(decorative
				? { "aria-hidden": true }
				: { role: "img", "aria-label": alt })}
			aria-busy={layer.status === "loading" || resolving ? true : undefined}
			data-state={state}
			className={cn(
				"relative isolate overflow-hidden bg-surface-well",
				className,
			)}
			style={{
				...(aspectRatio ? { aspectRatio: String(aspectRatio) } : {}),
				...style,
			}}
		>
			{count === 0 && !failed ? (
				<div
					aria-hidden="true"
					className="absolute inset-0 bg-[var(--dither-ink)] opacity-[0.16]"
					style={ditherMaskStyle({ tone: 0.14, cell: 3 })}
				/>
			) : null}
			<DitherLayer
				src={src}
				look={look}
				boxRef={boxRef}
				crossOrigin={crossOrigin}
				multipliers={animated ? REVEAL_MULTIPLIERS : STILL_MULTIPLIERS}
				stage={stage}
				enabled={near}
				onState={onState}
				onCanvas={onCanvas}
			/>
			{failed
				? (fallback ?? (
						<img
							src={src}
							alt=""
							aria-hidden="true"
							className="absolute inset-0 size-full object-cover"
						/>
					))
				: null}
		</div>
	);
}
