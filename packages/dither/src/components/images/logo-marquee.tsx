import { cn } from "cn";
import {
	type CSSProperties,
	type ReactNode,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { svgToDataUri } from "../../lib/export";
import { ditherFillId, ditherPatternSvg } from "../../lib/pattern";
import { useReducedMotionPref } from "../../lib/prefs";
import { DitheredLogo, type DitheredLogoProps } from "./dithered-logo";
import { useBoxSize, useInView } from "./hooks";
import { loopCopies, nextRate } from "./marquee-math";

export interface MarqueeLogo {
	/** A brand of the host's catalog (`setBrandCatalog`). */
	brand?: string;
	/** Or any SVG/PNG URL. */
	src?: string;
	alt: string;
	/** Width ÷ height; reserves the logo's width before it loads. */
	ratio?: number;
	/** Makes the logo a link. */
	href?: string;
	/** Overrides the row's `color` for this logo. */
	color?: string;
}

type DrawProps = Pick<
	DitheredLogoProps,
	| "color"
	| "tone"
	| "fill"
	| "floor"
	| "outline"
	| "matrix"
	| "cell"
	| "effect"
	| "period"
>;

export interface LogoMarqueeProps extends DrawProps {
	logos: readonly MarqueeLogo[];
	/** Names the list for screen readers. Default "Logos". */
	label?: string;
	/** A scrolling `marquee` row or a still `wall`. Default `marquee`. */
	variant?: "marquee" | "wall";
	/** Scroll speed in CSS px per second. Default 32. */
	speed?: number;
	direction?: "left" | "right";
	/** How the ends fade: a smooth `soft` ramp, `dither` steps through Bayer levels, or `none`. Default `soft`. */
	fade?: "soft" | "dither" | "none";
	/** Width of each end's fade in CSS px. Default 96. */
	fadeWidth?: number;
	/** Show a pause button on hover and keyboard focus. Default true. */
	pauseButton?: boolean;
	/** Logo height in CSS px. Default 28. */
	logoHeight?: number;
	/** Space between logos in CSS px. Default 64. */
	gap?: number;
	className?: string;
	style?: CSSProperties;
}

const BAND_TONES = [0.2, 0.45, 0.7, 0.9] as const;

/** A CSS mask that fades both ends out in four Bayer steps; the middle stays solid. */
export function ditherEdgeMask(fadeWidth: number, cell = 3): CSSProperties {
	const band = fadeWidth / BAND_TONES.length;
	const fills = BAND_TONES.map(
		(tone) => ({ tone, color: "#000", cell }) as const,
	);
	const defs = fills.map((fill) => ditherPatternSvg(fill)).join("");
	const rects = (mirror: boolean) =>
		fills
			.map((fill, i) => {
				const x = (mirror ? BAND_TONES.length - 1 - i : i) * band;
				return `<rect x="${x}" y="0" width="${band}" height="100%" fill="url(#${ditherFillId(fill)})"/>`;
			})
			.join("");
	const strip = (mirror: boolean) =>
		`url("${svgToDataUri(`<svg xmlns="http://www.w3.org/2000/svg" width="100%" height="100%"><defs>${defs}</defs>${rects(mirror)}</svg>`)}")`;
	const image = `${strip(false)}, linear-gradient(#000, #000), ${strip(true)}`;
	const size = `${fadeWidth}px 100%, calc(100% - ${fadeWidth * 2}px) 100%, ${fadeWidth}px 100%`;
	const position = `left top, ${fadeWidth}px 0, right top`;
	return {
		maskImage: image,
		maskSize: size,
		maskPosition: position,
		maskRepeat: "no-repeat",
		WebkitMaskImage: image,
		WebkitMaskSize: size,
		WebkitMaskPosition: position,
		WebkitMaskRepeat: "no-repeat",
	};
}

function softEdgeMask(fadeWidth: number): CSSProperties {
	const image = `linear-gradient(to right, transparent, #000 ${fadeWidth}px, #000 calc(100% - ${fadeWidth}px), transparent)`;
	return { maskImage: image, WebkitMaskImage: image };
}

/**
 * Stable React keys from the logo's own identity. Two logos with the same
 * identity get a numbered suffix in list order, so keys stay unique.
 */
function keyLogos(
	logos: readonly MarqueeLogo[],
): Array<{ key: string; logo: MarqueeLogo }> {
	const seen = new Map<string, number>();
	return logos.map((logo) => {
		const base = logo.brand ?? logo.src ?? logo.alt;
		const occurrence = seen.get(base) ?? 0;
		seen.set(base, occurrence + 1);
		return { key: occurrence === 0 ? base : `${base}~${occurrence}`, logo };
	});
}

interface ItemProps extends DrawProps {
	logo: MarqueeLogo;
	height: number;
	/** False for the copies that only fill the loop: out of the tab order. */
	focusable: boolean;
	/** Where in the effect's sweep this logo starts, so a shimmer passes through a row in turn. */
	phase: number;
}

function Item({ logo, height, focusable, phase, color, ...draw }: ItemProps) {
	const mark = (
		<DitheredLogo
			{...(logo.src ? { src: logo.src } : {})}
			{...(logo.brand ? { brand: logo.brand } : {})}
			alt={logo.alt}
			{...(logo.ratio ? { ratio: logo.ratio } : {})}
			color={logo.color ?? color ?? "ink"}
			{...draw}
			phase={phase}
			// Logos in a clipped, moving row only intersect once they slide in.
			lazy={false}
			className="block"
			style={{ height }}
		/>
	);
	if (!logo.href) return mark;
	return (
		<a
			href={logo.href}
			tabIndex={focusable ? undefined : -1}
			className="block rounded-sm outline-none focus-visible:focus-ring"
		>
			{mark}
		</a>
	);
}

/**
 * A looping row, or a still wall, of logos in one dithered treatment. The row
 * repeats as often as it needs to fill the width and loops without a seam; it
 * pauses on hover, on focus, off-screen and with the pause button. Under
 * reduced motion it is a still, centred, wrapping row. Only the first copy is
 * in the accessibility tree.
 */
export function LogoMarquee({
	logos,
	label = "Logos",
	variant = "marquee",
	speed = 32,
	direction = "left",
	fade = "soft",
	fadeWidth = 96,
	pauseButton = true,
	logoHeight = 28,
	gap = 64,
	className,
	style,
	...draw
}: LogoMarqueeProps): ReactNode {
	const frameRef = useRef<HTMLFieldSetElement | null>(null);
	const trackRef = useRef<HTMLDivElement | null>(null);
	const rowRef = useRef<HTMLUListElement | null>(null);
	const animation = useRef<Animation | null>(null);
	const reduced = useReducedMotionPref();
	const box = useBoxSize(frameRef);
	const onScreen = useInView(frameRef);
	const [rowWidth, setRowWidth] = useState(0);
	const [hovered, setHovered] = useState(false);
	const [paused, setPaused] = useState(false);
	const moving = variant === "marquee" && !reduced;
	const keyed = useMemo(() => keyLogos(logos), [logos]);
	const idle = paused || hovered || !onScreen;
	// The loop is recreated when its inputs change; a new one starts playing,
	// so it reads the current idle state here instead of waiting for a rerun.
	const idleRef = useRef(idle);
	idleRef.current = idle;

	// The ResizeObserver reports every change to the row's width, which is
	// what logos, logoHeight and gap change, so only `moving` restarts it.
	useEffect(() => {
		const row = rowRef.current;
		if (!row || !moving) return;
		const observer = new ResizeObserver(() => setRowWidth(row.offsetWidth));
		observer.observe(row);
		setRowWidth(row.offsetWidth);
		return () => observer.disconnect();
	}, [moving]);

	useEffect(() => {
		const track = trackRef.current;
		if (!track || !moving || rowWidth <= 0) return;
		const loop = track.animate(
			[
				{ transform: "translateX(0)" },
				{ transform: `translateX(${-rowWidth}px)` },
			],
			{
				duration: (rowWidth / Math.max(1, speed)) * 1000,
				iterations: Number.POSITIVE_INFINITY,
				easing: "linear",
				direction: direction === "right" ? "reverse" : "normal",
			},
		);
		loop.playbackRate = idleRef.current ? 0 : 1;
		animation.current = loop;
		return () => {
			loop.cancel();
			animation.current = null;
		};
	}, [moving, rowWidth, speed, direction]);

	// Coast to a stop, and back up to speed, instead of halting dead.
	useEffect(() => {
		const loop = animation.current;
		if (!loop) return;
		const target = idle ? 0 : 1;
		let frame = 0;
		let last = performance.now();
		const step = (now: number) => {
			const delta = Math.min(0.1, (now - last) / 1000);
			last = now;
			loop.playbackRate = nextRate(loop.playbackRate, target, delta);
			if (loop.playbackRate !== target) frame = requestAnimationFrame(step);
		};
		frame = requestAnimationFrame(step);
		return () => cancelAnimationFrame(frame);
	}, [idle]);

	const items = (focusable: boolean) =>
		keyed.map(({ key, logo }, index) => (
			<li key={key} className="shrink-0" style={{ marginRight: gap }}>
				<Item
					logo={logo}
					height={logoHeight}
					focusable={focusable}
					phase={index / Math.max(1, keyed.length)}
					{...draw}
				/>
			</li>
		));

	if (variant === "wall") {
		return (
			<div className={cn("@container", className)} style={style}>
				<ul
					aria-label={label}
					className="grid grid-cols-2 gap-px overflow-hidden rounded-lg border border-line-faint bg-line-faint @2xl:grid-cols-4"
				>
					{keyed.map(({ key, logo }, index) => (
						<li
							key={key}
							className="flex items-center justify-center bg-surface-base px-6 py-8"
						>
							<Item
								logo={logo}
								height={logoHeight}
								focusable
								phase={index / Math.max(1, keyed.length)}
								{...draw}
							/>
						</li>
					))}
				</ul>
			</div>
		);
	}

	if (!moving) {
		return (
			<div className={className} style={style}>
				<ul
					aria-label={label}
					className="flex flex-wrap items-center justify-center gap-x-12 gap-y-6"
				>
					{keyed.map(({ key, logo }, index) => (
						<li key={key}>
							<Item
								logo={logo}
								height={logoHeight}
								focusable
								phase={index / Math.max(1, keyed.length)}
								{...draw}
							/>
						</li>
					))}
				</ul>
			</div>
		);
	}

	const copies = loopCopies(box?.width ?? 0, rowWidth);
	const edge =
		fade === "soft"
			? softEdgeMask(fadeWidth)
			: fade === "dither"
				? ditherEdgeMask(fadeWidth)
				: undefined;

	return (
		<fieldset
			ref={frameRef}
			aria-label={label}
			className={cn(
				"group/marquee relative m-0 min-w-0 border-0 p-0",
				className,
			)}
			style={style}
			onPointerEnter={(event) => {
				if (event.pointerType !== "touch") setHovered(true);
			}}
			onPointerLeave={() => setHovered(false)}
			onFocus={() => setHovered(true)}
			onBlur={(event) => {
				if (!event.currentTarget.contains(event.relatedTarget))
					setHovered(false);
			}}
		>
			{/* The ends fade through a mask on this wrapper, so the pause button is never faded. */}
			<div className="overflow-hidden" style={edge}>
				<div
					ref={trackRef}
					className="flex w-max items-center will-change-transform"
				>
					<ul
						ref={rowRef}
						aria-label={label}
						className="flex shrink-0 items-center"
					>
						{items(true)}
					</ul>
					{Array.from({ length: copies - 1 }, (_, copy) => (
						<ul
							// biome-ignore lint/suspicious/noArrayIndexKey: a fixed count of identical, never-reordered loop copies
							key={copy}
							aria-hidden="true"
							inert
							className="flex shrink-0 items-center"
						>
							{items(false)}
						</ul>
					))}
				</div>
			</div>
			{pauseButton ? (
				<button
					type="button"
					aria-pressed={paused}
					aria-label={paused ? "Play logo scroll" : "Pause logo scroll"}
					onClick={() => setPaused((p) => !p)}
					className="absolute top-1/2 right-2 grid size-7 -translate-y-1/2 place-items-center rounded-full border border-line-strong bg-surface-float text-ink-2 opacity-0 outline-none transition-opacity duration-150 ease-out hover:text-ink-1 focus-visible:focus-ring focus-visible:opacity-100 group-hover/marquee:opacity-100"
				>
					<svg
						viewBox="0 0 24 24"
						width="14"
						height="14"
						fill="currentColor"
						aria-hidden="true"
					>
						{paused ? (
							<path d="M8 5v14l11-7z" />
						) : (
							<path d="M7 5h4v14H7zM13 5h4v14h-4z" />
						)}
					</svg>
				</button>
			) : null}
		</fieldset>
	);
}
