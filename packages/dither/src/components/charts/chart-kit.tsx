import {
	type CSSProperties,
	type ReactNode,
	type RefObject,
	useEffect,
	useId,
	useLayoutEffect,
	useMemo,
	useState,
} from "react";
import { useOnScreen, useReducedMotionPref } from "../../lib/prefs";
import { DIMMED_OPACITY } from "./cartesian-math";
import { bloomOpacity, bloomRadius, type ChartBloom } from "./shared";

/**
 * Behaviour the chart family shares: the entrance that plays once when a
 * chart first scrolls into view, the glow behind a fill, the legend
 * spotlight and the screen-reader live region. Everything here is
 * transform / opacity / clip only and switches off under reduced motion.
 */

/* ---------- entrance ---------- */

export interface EntrancePlan {
	/** Entrance motion is on (asked for, and no reduced-motion preference). */
	armed: boolean;
	/** The chart has been seen: play (once). */
	go: boolean;
}

/**
 * Entrance state for a chart: `armed` while motion is allowed, `go` from the
 * first time the root intersects the viewport. Under reduced motion (or
 * `animate={false}`) nothing is ever hidden.
 */
export function useEntrance(
	root: RefObject<Element | null>,
	animate: boolean,
): EntrancePlan {
	const reduced = useReducedMotionPref();
	const visible = useOnScreen(root);
	const [seen, setSeen] = useState(false);
	useEffect(() => {
		if (visible) setSeen(true);
	}, [visible]);
	const armed = animate && !reduced;
	const go = armed && seen;
	return useMemo(() => ({ armed, go }), [armed, go]);
}

/** How a mark enters: a left-to-right wipe, stepped fade-up, or a rise from a baseline. */
export type EntranceKind = "wipe" | "fade" | "rise";

const EASE_OUT = "cubic-bezier(0.22, 1, 0.36, 1)";

function keyframesFor(kind: EntranceKind): {
	frames: Keyframe[];
	easing: string;
} {
	if (kind === "wipe") {
		return {
			frames: [
				{ clipPath: "inset(-30% 100% -30% -2%)" },
				{ clipPath: "inset(-30% -2% -30% -2%)" },
			],
			easing: EASE_OUT,
		};
	}
	if (kind === "rise") {
		return {
			frames: [{ transform: "scaleY(0)" }, { transform: "scaleY(1)" }],
			easing: EASE_OUT,
		};
	}
	return {
		frames: [{ opacity: 0 }, { opacity: 1 }],
		// Whole steps through the dither levels instead of a smooth fade.
		easing: "steps(6, end)",
	};
}

/**
 * Plays an entrance on a group the first time `plan.go` flips. Until then the
 * group is hidden (when armed), so there is no flash of the finished chart.
 * Once the animation ends the element returns to its plain style.
 */
export function Entrance({
	plan,
	kind,
	delay = 0,
	duration = 700,
	originY,
	children,
}: {
	plan: EntrancePlan;
	kind: EntranceKind;
	/** ms before the entrance starts. */
	delay?: number;
	/** ms. */
	duration?: number;
	/** `rise` only: the y the group grows from (the baseline). */
	originY?: number;
	children: ReactNode;
}) {
	const [el, setEl] = useState<SVGGElement | null>(null);
	useLayoutEffect(() => {
		if (!plan.go || !el || typeof el.animate !== "function") return;
		const { frames, easing } = keyframesFor(kind);
		const animation = el.animate(frames, {
			duration,
			delay,
			easing,
			fill: "backwards",
		});
		return () => animation.cancel();
	}, [plan.go, el, kind, delay, duration]);
	return (
		<g
			ref={setEl}
			style={{
				...(plan.armed && !plan.go ? { opacity: 0 } : null),
				...(kind === "rise" && originY !== undefined
					? { transformOrigin: `0px ${originY}px` }
					: null),
			}}
		>
			{children}
		</g>
	);
}

/**
 * Reveals its children with a clockwise sweep from 12 o'clock (a donut's
 * slices) the first time `plan.go` flips. The mask is dropped once it has
 * played, so a glow around the shapes is never clipped afterwards.
 */
export function Sweep({
	plan,
	cx,
	cy,
	radius,
	duration = 900,
	children,
}: {
	plan: EntrancePlan;
	cx: number;
	cy: number;
	/** Outer radius of what is revealed. */
	radius: number;
	duration?: number;
	children: ReactNode;
}) {
	const id = `nc-sweep-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
	const [el, setEl] = useState<SVGCircleElement | null>(null);
	const [done, setDone] = useState(false);
	const ring = radius / 2;
	const circumference = 2 * Math.PI * ring;
	useLayoutEffect(() => {
		if (!plan.go || !el || typeof el.animate !== "function") return;
		const animation = el.animate(
			[{ strokeDashoffset: circumference }, { strokeDashoffset: 0 }],
			{ duration, easing: EASE_OUT, fill: "backwards" },
		);
		animation.onfinish = () => setDone(true);
		return () => animation.cancel();
	}, [plan.go, el, circumference, duration]);
	if (!plan.armed || done) return <g>{children}</g>;
	return (
		<>
			<mask id={id} maskUnits="userSpaceOnUse">
				<circle
					ref={setEl}
					cx={cx}
					cy={cy}
					r={ring}
					fill="none"
					stroke="white"
					strokeWidth={radius + 2}
					strokeDasharray={`${circumference} ${circumference}`}
					transform={`rotate(-90 ${cx} ${cy})`}
				/>
			</mask>
			<g mask={`url(#${id})`} style={plan.go ? undefined : { opacity: 0 }}>
				{children}
			</g>
		</>
	);
}

/** Grows its children out from a centre point (a radar's shapes) the first time `plan.go` flips. */
export function Grow({
	plan,
	cx,
	cy,
	delay = 0,
	duration = 800,
	children,
}: {
	plan: EntrancePlan;
	cx: number;
	cy: number;
	delay?: number;
	duration?: number;
	children: ReactNode;
}) {
	const [el, setEl] = useState<SVGGElement | null>(null);
	useLayoutEffect(() => {
		if (!plan.go || !el || typeof el.animate !== "function") return;
		const animation = el.animate(
			[
				{ transform: "scale(0)", opacity: 0 },
				{ transform: "scale(1)", opacity: 1 },
			],
			{ duration, delay, easing: EASE_OUT, fill: "backwards" },
		);
		return () => animation.cancel();
	}, [plan.go, el, delay, duration]);
	return (
		<g
			ref={setEl}
			style={{
				transformOrigin: `${cx}px ${cy}px`,
				...(plan.armed && !plan.go ? { opacity: 0 } : null),
			}}
		>
			{children}
		</g>
	);
}

/* ---------- bloom ---------- */

/**
 * A soft glow behind a fill: pass the same shapes drawn solid in the
 * series' colour as `children`; they are blurred and laid at
 * `--dither-bloom`-scaled opacity. Renders nothing for `off`, and nothing
 * under forced colours. `box` is the filter region (the chart's size).
 */
export function Bloom({
	bloom,
	box,
	children,
}: {
	bloom: ChartBloom | undefined;
	box: { width: number; height: number };
	children: ReactNode;
}) {
	const id = `nc-bloom-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
	const opacity = bloomOpacity(bloom);
	if (!opacity) return null;
	const radius = bloomRadius(bloom);
	const pad = radius * 4;
	return (
		<g
			aria-hidden
			pointerEvents="none"
			className="forced-colors:hidden"
			filter={`url(#${id})`}
			style={{ opacity }}
		>
			<defs>
				<filter
					id={id}
					filterUnits="userSpaceOnUse"
					x={-pad}
					y={-pad}
					width={box.width + pad * 2}
					height={box.height + pad * 2}
				>
					<feGaussianBlur stdDeviation={radius} />
				</filter>
			</defs>
			{children}
		</g>
	);
}

/* ---------- legend spotlight ---------- */

export interface Spotlight {
	/** The spotlit label: hovered in the legend, else locked. */
	focus: string | null;
	locked: string | null;
	hover: (label: string | null) => void;
	lock: (label: string | null) => void;
	/** Let go of everything (Escape). */
	clear: () => void;
}

/** Legend spotlight state: hover lights a series, click locks it, Escape lets go. */
export function useSpotlight(): Spotlight {
	const [hovered, setHovered] = useState<string | null>(null);
	const [locked, setLocked] = useState<string | null>(null);
	return {
		focus: hovered ?? locked,
		locked,
		hover: setHovered,
		lock: setLocked,
		clear: () => {
			setHovered(null);
			setLocked(null);
		},
	};
}

/** The opacity transition every spotlight-dimmed series shares. */
export const dimStyle = (opacity: number): CSSProperties => ({
	opacity,
	transition: "opacity 150ms ease-out",
});

/**
 * Legend behaviour for the chart family. `spotlight` (default): hovering or
 * focusing an entry lights that series and dims the rest, clicking locks it,
 * Escape lets go. `toggle`: clicking an entry hides or shows its series.
 */
export function useLegendState(
	seriesCount: number,
	action: "spotlight" | "toggle" = "spotlight",
) {
	const spot = useSpotlight();
	const [hidden, setHidden] = useState<ReadonlySet<string>>(new Set());
	const toggle = (label: string) =>
		setHidden((cur) => {
			const next = new Set(cur);
			if (next.has(label)) next.delete(label);
			else if (next.size < seriesCount - 1) next.add(label);
			return next;
		});
	return {
		/** Hidden labels (`toggle` mode only). */
		hidden,
		/** Spread onto `ChartLegend`. */
		legendProps:
			action === "toggle"
				? { hidden, onToggle: toggle }
				: {
						focus: spot.focus,
						locked: spot.locked,
						onHover: spot.hover,
						onLock: spot.lock,
					},
		/** The spotlit label, or null. */
		focus: spot.focus,
		/** 1, or dimmed while another series is spotlit. */
		opacityOf: (label: string) =>
			spot.focus === null || spot.focus === label ? 1 : DIMMED_OPACITY,
		/** Escape: let go of the spotlight. */
		clear: spot.clear,
	};
}

/* ---------- screen readers ---------- */

/**
 * The visually hidden key help (referenced by `aria-describedby={id}`) and a
 * polite live region that reads the scrubbed datum at each stop.
 */
export function ChartLive({ id, text }: { id: string; text: string }) {
	return (
		<>
			<p id={id} className="sr-only">
				Use the arrow keys to move through the data, Home and End to jump to the
				ends, Escape to clear.
			</p>
			<div aria-live="polite" aria-atomic="true" className="sr-only">
				{text}
			</div>
		</>
	);
}

/** A DOM-safe id fragment from `useId`. */
export function useChartUid(): string {
	return `nc-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
}
