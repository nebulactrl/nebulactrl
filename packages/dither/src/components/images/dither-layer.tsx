import {
	type CSSProperties,
	memo,
	type RefObject,
	useEffect,
	useLayoutEffect,
	useRef,
} from "react";
import type { DitherLook } from "./look";
import type { CrossOrigin, Dithered } from "./use-dithered";
import { paintFrame, useDithered } from "./use-dithered";

export interface DitherLayerProps {
	src: string;
	look: DitherLook;
	/** The box this layer fills; the grid is sized from it. */
	boxRef: RefObject<Element | null>;
	crossOrigin?: CrossOrigin | undefined;
	/** Cell multipliers to compute, coarse to fine. Default `[1]`. */
	multipliers?: readonly number[] | undefined;
	/** Which multiplier's frame to show. Default: the last (finest). */
	stage?: number | undefined;
	/** Skip loading until true (not yet on screen). Default true. */
	enabled?: boolean | undefined;
	/** Called as loading progresses, and when the frames are ready. */
	onState?:
		| ((
				state: Pick<Dithered, "status" | "natural"> & { count: number },
		  ) => void)
		| undefined;
	/** The canvas, for export. */
	onCanvas?: ((canvas: HTMLCanvasElement | null) => void) | undefined;
	className?: string | undefined;
	style?: CSSProperties | undefined;
}

/**
 * An absolutely positioned canvas that shows `src` dithered onto the cell
 * grid of its box. Pixelated scaling and whole-device-pixel cells keep edges
 * crisp. Decoration: it carries no text, so the parent labels the image.
 */
export const DitherLayer = memo(function DitherLayer({
	src,
	look,
	boxRef,
	crossOrigin,
	multipliers,
	stage,
	enabled,
	onState,
	onCanvas,
	className,
	style,
}: DitherLayerProps) {
	const canvasRef = useRef<HTMLCanvasElement | null>(null);
	const dithered = useDithered({
		src,
		look,
		boxRef,
		...(crossOrigin ? { crossOrigin } : {}),
		...(multipliers ? { multipliers } : {}),
		...(enabled === undefined ? {} : { enabled }),
	});
	const onStateRef = useRef(onState);
	onStateRef.current = onState;

	useEffect(() => {
		onStateRef.current?.({
			status: dithered.status,
			natural: dithered.natural,
			count: dithered.frames.length,
		});
	}, [dithered.status, dithered.natural, dithered.frames.length]);

	const frame =
		dithered.frames[
			Math.min(stage ?? Number.MAX_SAFE_INTEGER, dithered.frames.length - 1)
		];
	const ready = dithered.status === "ready" && frame !== undefined;
	useLayoutEffect(() => {
		const canvas = canvasRef.current;
		if (canvas && frame) paintFrame(canvas, frame);
	}, [frame]);

	// Hand the canvas to the parent whenever it mounts or unmounts: it only
	// exists while `ready`, so the effect re-runs with it.
	// biome-ignore lint/correctness/useExhaustiveDependencies: ready gates whether the canvas element exists
	useEffect(() => {
		onCanvas?.(canvasRef.current);
		return () => onCanvas?.(null);
	}, [onCanvas, ready]);

	if (!ready || !dithered.cssSize) return null;
	return (
		<canvas
			ref={canvasRef}
			className={className}
			style={{
				position: "absolute",
				left: 0,
				top: 0,
				width: dithered.cssSize.width,
				height: dithered.cssSize.height,
				imageRendering: "pixelated",
				...style,
			}}
		/>
	);
});
