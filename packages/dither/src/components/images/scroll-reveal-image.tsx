import { cn } from "cn";
import {
	type CSSProperties,
	type ReactNode,
	type RefObject,
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useReducedMotionPref } from "../../lib/prefs";
import { maskTiling, maskUrl } from "../text/masks";
import { DitherLayer } from "./dither-layer";
import { boxAspect } from "./grid";
import { useInView } from "./hooks";
import { type DitherLook, IMAGE_PRESETS } from "./look";
import {
	coarseLadder,
	quantizeProgress,
	revealView,
	scrollProgress,
} from "./reveal";
import type { CrossOrigin, Dithered } from "./use-dithered";

export interface ScrollRevealImageProps extends DitherLook {
	src: string;
	/** Describes the image. Pass `""` when it is decorative. */
	alt: string;
	crossOrigin?: CrossOrigin;
	/** Width ÷ height, reserved before the image loads. Default: the image's own ratio. */
	ratio?: number;
	/** Cell multiplier of the first, blockiest step. It sharpens to 1 in nine steps on a log scale. Default 24. */
	coarsest?: number;
	/** Viewport fraction from the top where progress is 0 (1 = the image's top edge entering at the bottom). Default 1. */
	start?: number;
	/** Viewport fraction where progress reaches 1 (0.3 = top edge 30% down). Default 0.3. */
	finish?: number;
	/** End on the full `photo` (dissolves in cell by cell) or stop at the full-detail `dither`. Default `photo`. */
	end?: "photo" | "dither";
	/** Scrolling panel to follow instead of the window. */
	scrollRoot?: RefObject<Element | null>;
	/** Set the progress yourself, 0..1, instead of following the scroll. */
	progress?: number;
	/** Called when the progress changes, 0..1. */
	onProgressChange?: (progress: number) => void;
	className?: string;
	style?: CSSProperties;
}

type LayerState = Pick<Dithered, "status" | "natural"> & { count: number };

/**
 * An image that sharpens from coarse dithered blocks to the full photo as you
 * scroll it into view. Progress follows the scroll position, not a timer: an
 * IntersectionObserver wakes a passive scroll listener only while the image is
 * near the viewport, measured at most once per frame. Each coarseness is
 * dithered once and cached, so scrolling back and forth is free. Reduced
 * motion shows the finished state. `data-stage` is `coarse`, `fine` or
 * `photo`.
 */
export function ScrollRevealImage({
	src,
	alt,
	crossOrigin,
	ratio,
	coarsest = 24,
	start = 1,
	finish = 0.3,
	end = "photo",
	scrollRoot,
	progress: controlled,
	onProgressChange,
	className,
	style,
	...look
}: ScrollRevealImageProps): ReactNode {
	const boxRef = useRef<HTMLDivElement | null>(null);
	const reduced = useReducedMotionPref();
	const near = useInView(boxRef, { rootMargin: "120px 0px" });
	const [scrolled, setScrolled] = useState(0);
	const [layer, setLayer] = useState<LayerState>({
		status: "idle",
		natural: null,
		count: 0,
	});
	const multipliers = useMemo(() => coarseLadder(coarsest), [coarsest]);
	const onState = useCallback((state: LayerState) => setLayer(state), []);

	// Follow the scroll only while the image is near the viewport, once per frame.
	useEffect(() => {
		const box = boxRef.current;
		if (!box || !near || reduced || controlled !== undefined) return;
		const root = scrollRoot?.current ?? null;
		// Capture catches scrolls of any container, not only the page.
		const target: Element | Window = root ?? window;
		let frame = 0;
		const measure = () => {
			frame = 0;
			const rect = box.getBoundingClientRect();
			const bounds = root?.getBoundingClientRect();
			const top = rect.top - (bounds?.top ?? 0);
			const height = bounds?.height ?? window.innerHeight;
			setScrolled(quantizeProgress(scrollProgress(top, height, start, finish)));
		};
		const schedule = () => {
			if (frame === 0) frame = requestAnimationFrame(measure);
		};
		measure();
		target.addEventListener("scroll", schedule, {
			passive: true,
			capture: true,
		});
		window.addEventListener("resize", schedule, { passive: true });
		return () => {
			target.removeEventListener("scroll", schedule, { capture: true });
			window.removeEventListener("resize", schedule);
			cancelAnimationFrame(frame);
		};
	}, [near, reduced, controlled, scrollRoot, start, finish]);

	const progress = reduced ? 1 : quantizeProgress(controlled ?? scrolled);
	const view = revealView(progress, multipliers.length, end);

	const onProgressRef = useRef(onProgressChange);
	onProgressRef.current = onProgressChange;
	useEffect(() => {
		onProgressRef.current?.(progress);
	}, [progress]);

	const cell = look.cell ?? IMAGE_PRESETS[look.preset ?? "duotone"].cell;
	const photoMask =
		end === "photo" && view.photo < 16
			? {
					maskImage: maskUrl(view.photo, cell),
					WebkitMaskImage: maskUrl(view.photo, cell),
					...maskTiling(cell),
				}
			: undefined;
	const stage =
		end === "photo" && view.photo >= 16
			? "photo"
			: view.stage === multipliers.length - 1
				? "fine"
				: "coarse";
	const decorative = alt === "";
	const aspect = boxAspect(ratio, layer.natural, className);

	return (
		<div
			ref={boxRef}
			{...(decorative
				? { "aria-hidden": true }
				: { role: "img", "aria-label": alt })}
			data-state={layer.status}
			data-stage={stage}
			className={cn(
				"relative isolate overflow-hidden bg-surface-well",
				className,
			)}
			style={{
				...(aspect ? { aspectRatio: String(aspect) } : {}),
				...style,
			}}
		>
			<DitherLayer
				src={src}
				look={look}
				boxRef={boxRef}
				crossOrigin={crossOrigin}
				multipliers={multipliers}
				stage={view.stage}
				enabled={near}
				onState={onState}
			/>
			{end === "photo" && view.photo > 0 ? (
				<img
					src={src}
					alt=""
					aria-hidden="true"
					draggable={false}
					decoding="async"
					crossOrigin={crossOrigin}
					className={cn(
						"absolute inset-0 size-full",
						look.fit === "contain" ? "object-contain" : "object-cover",
					)}
					style={photoMask}
				/>
			) : null}
		</div>
	);
}
