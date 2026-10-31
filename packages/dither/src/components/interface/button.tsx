import { cn } from "cn";
import * as React from "react";
import type { DitherMatrix } from "../../lib/bayer";
import { type BandMask, bandMaskClass, rampStops } from "./bands";
import {
	bloomShadow,
	type DitherBloom,
	type DitherColor,
	type DitherFillVariant,
	inkCss,
	mix,
} from "./fill";
import { Button, type ButtonVariant } from "./primitives";

// Light falls from above: a highlight dithered out from the top edge, and a
// shade dithered up from the bottom. Eight bands, so the gradient is the
// dither density itself, not an alpha ramp.
const HIGHLIGHT_STOPS = [...rampStops(0.6, 0.1, 5), 0, 0, 0];
const SHADE_STOPS = [0, 0, 0, 0, ...rampStops(0.1, 0.5, 4)];
// `solid` keeps only a thin dithered rim along the bottom edge.
const RIM_STOPS = [0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0.5, 0.9];
// An outline button is a tint with its own colour dithered up from the bottom.
const OUTLINE_STOPS = [0, 0, 0, 0, 0, 0.08, 0.2, 0.35, 0.5];

type ButtonProps = React.ComponentProps<typeof Button>;

/** The `Button` variants, plus `filled` (the accent key). `outline` is `Button`'s own. */
export type DitherButtonVariant = ButtonVariant | "filled";

export type DitherButtonProps = Omit<ButtonProps, "variant"> & {
	variant?: DitherButtonVariant | undefined;
	/** Texture of the shade: a Bayer ramp, a dot lattice, diagonals, or a thin rim. Default `gradient`. */
	fill?: DitherFillVariant | undefined;
	/** Colours the key: a palette name (`"blue"`), a custom property (`"--chart-1"`) or any CSS colour. Default: the variant's own. */
	color?: DitherColor | undefined;
	/** A soft glow in the key's colour. */
	bloom?: DitherBloom | undefined;
	/** Size of one dither cell in CSS px. Default 2. */
	cell?: number | undefined;
	/** Busy: keeps the label and the width, steps three cells before the label, ignores clicks and sets `aria-busy`. */
	loading?: boolean | undefined;
	/** Render your own single child (a link) with the button's look and finish. */
	asChild?: boolean | undefined;
	/** The threshold pattern of the finish. Default `bayer4`. */
	matrix?: DitherMatrix | undefined;
};

const FILLED: Record<string, true> = {
	primary: true,
	default: true,
	accent: true,
	filled: true,
	"danger-solid": true,
	"destructive-solid": true,
};
const OUTLINE: Record<string, true> = { outline: true, secondary: true };

type Family = "plain" | "filled" | "outline" | "text";

function familyOf(variant: string | undefined, coloured: boolean): Family {
	if (!coloured) return "plain";
	if (variant === undefined || variant in FILLED) return "filled";
	if (variant in OUTLINE) return "outline";
	return "text";
}

/** The colour of a coloured key: a lit face, a darker edge, a label that contrasts with it. */
function faceStyle(family: Family, hue: string): React.CSSProperties {
	if (family === "filled") {
		return {
			background: `linear-gradient(180deg, ${mix(hue, "white", 88)}, ${hue})`,
			borderColor: mix(hue, "black", 70),
			color: "var(--color-surface-base)",
			textShadow: "none",
			boxShadow: "var(--shadow-tile), 0 1px 2px 0 rgb(2 4 10 / 0.5)",
		};
	}
	if (family === "outline") {
		return {
			background: mix(hue, "var(--color-surface-float)", 12),
			borderColor: mix(hue, "transparent", 45),
			color: hue,
			boxShadow: "none",
		};
	}
	return { color: hue };
}

function withStyle(
	own: React.CSSProperties,
	style: ButtonProps["style"],
): NonNullable<ButtonProps["style"]> {
	if (typeof style === "function") {
		return (state) => ({ ...own, ...style(state) });
	}
	return { ...own, ...style };
}

/**
 * `Button`, finished in dither. It is not a second button system: every
 * variant, size and prop is `Button`'s. The finish is two layers behind the
 * label: a highlight that thins out from the top, a shade that thins out from
 * the bottom. Hover brightens the highlight; press drops it, flips the shade
 * to the top edge (the key sinks) and scales to `--press-scale` (0.96) through `Button`'s own
 * `pressable`.
 *
 * On top of that: `fill` picks the shade's texture, `color` paints the key,
 * `bloom` adds a glow, `loading` is a busy state and `asChild` lends the look
 * to a link.
 */
function DitherButton({
	className,
	children,
	variant,
	fill = "gradient",
	color,
	bloom,
	cell = 2,
	matrix,
	loading = false,
	asChild = false,
	style,
	onClick,
	type,
	nativeButton,
	render,
	...props
}: DitherButtonProps) {
	const hue = color === undefined ? undefined : inkCss(color);
	const family = familyOf(variant, hue !== undefined);
	const outlined = family === "outline";
	const texture = fill === "solid" ? "gradient" : fill;

	const masks = React.useMemo(() => {
		const highlight: BandMask = {
			axis: "y",
			stops: HIGHLIGHT_STOPS,
			cell,
			...(matrix ? { matrix } : {}),
		};
		const shade: BandMask = {
			axis: "y",
			stops: outlined
				? OUTLINE_STOPS
				: fill === "solid"
					? RIM_STOPS
					: SHADE_STOPS,
			cell,
			...(texture !== "gradient" || matrix ? { texture } : {}),
			...(matrix ? { matrix } : {}),
		};
		return { highlight: bandMaskClass(highlight), shade: bandMaskClass(shade) };
	}, [cell, matrix, fill, texture, outlined]);

	const child =
		asChild && React.isValidElement<{ children?: React.ReactNode }>(children)
			? children
			: undefined;
	const label = child ? child.props.children : children;
	const glow = bloomShadow(hue ?? "var(--dither-ink)", bloom);

	// Props that must not be passed as `undefined` (exactOptionalPropertyTypes).
	const host: Pick<ButtonProps, "render" | "nativeButton" | "type"> = {};
	if (child) {
		host.render = child;
		host.nativeButton = false;
	} else {
		if (render) host.render = render;
		if (nativeButton !== undefined) host.nativeButton = nativeButton;
		if (type) host.type = type;
		else if (!render && nativeButton !== false) host.type = "button";
	}

	const handleClick: NonNullable<ButtonProps["onClick"]> = (event) => {
		if (loading) {
			event.preventDefault();
			event.stopPropagation();
			return;
		}
		onClick?.(event);
	};

	const face = family === "plain" ? {} : faceStyle(family, hue ?? "");

	return (
		<Button
			data-finish="dither"
			data-loading={loading ? "" : undefined}
			aria-busy={loading || undefined}
			className={cn(
				"relative isolate data-loading:cursor-progress data-loading:opacity-45",
				className,
			)}
			variant={variant === "filled" ? "primary" : variant}
			style={withStyle(face, style)}
			onClick={handleClick}
			{...host}
			{...props}
		>
			{glow ? (
				<span
					aria-hidden
					data-slot="button-bloom"
					className="pointer-events-none absolute inset-0 -z-10 rounded-[inherit] forced-colors:hidden"
					style={{ boxShadow: glow }}
				/>
			) : null}
			<span
				aria-hidden
				data-slot="button-finish"
				className="pointer-events-none absolute inset-0 -z-10 overflow-hidden rounded-[inherit] group-disabled/button:hidden group-aria-disabled/button:hidden forced-colors:hidden"
			>
				{outlined ? null : (
					<span
						className={cn(
							"absolute inset-0 bg-white/40 opacity-60 transition-opacity duration-150 ease-out group-hover/button:opacity-100 group-active/button:opacity-15 group-data-loading/button:animate-dither-cell group-data-loading/button:motion-reduce:animate-none",
							masks.highlight,
						)}
					/>
				)}
				<span
					className={cn(
						"absolute inset-0",
						outlined
							? "opacity-60 transition-opacity duration-150 ease-out group-hover/button:opacity-100 group-active/button:opacity-100"
							: "bg-black/45 opacity-80 transition-[opacity,transform] duration-150 ease-out group-hover/button:opacity-60 group-active/button:-scale-y-100 group-active/button:opacity-100",
						masks.shade,
					)}
					style={outlined ? { backgroundColor: hue } : undefined}
				/>
			</span>
			{loading ? (
				<span
					aria-hidden
					data-slot="button-loading"
					className="relative w-0 shrink-0 self-stretch forced-colors:hidden"
				>
					<span className="absolute top-1/2 right-1 flex -translate-y-1/2 gap-px">
						{[0, 1, 2].map((index) => (
							<i
								key={index}
								className="animate-dither-cell bg-current motion-reduce:animate-none motion-reduce:opacity-60"
								style={{
									width: cell,
									height: cell,
									animationDelay: `${index * 160}ms`,
								}}
							/>
						))}
					</span>
				</span>
			) : null}
			{label}
		</Button>
	);
}

export { DitherButton };
