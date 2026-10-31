import { cn } from "cn";
import type * as React from "react";
import type { DitherMatrix } from "../../lib/bayer";
import { type BandMask, bandMaskClass } from "./bands";
import { type DitherColor, type DitherFillVariant, inkCss } from "./fill";
import { type RuleSide, type RuleVariant, resolveRule } from "./rule";

export interface DitherDividerProps
	extends Omit<React.ComponentProps<"div">, "children" | "color"> {
	orientation?: "horizontal" | "vertical";
	/** `fade` dissolves at the ends, `solid` holds its density, `dots` is a perforation line. */
	variant?: RuleVariant;
	/** Short text set in the middle of a horizontal rule. Horizontal only. */
	label?: React.ReactNode;
	/** Thickness in cells. Default: the variant's own. */
	thickness?: number | undefined;
	/** Share of each half that fades out, 0..1. Setting it (or `strength`, `fill`) gives the ten-step fade. Default 0.6. */
	fade?: number | undefined;
	/** Densest dither level, in the middle of the rule, 0..1. Default 0.6. */
	strength?: number | undefined;
	/** Rule colour: a palette name, a custom property or any CSS colour. Default: the muted ink. */
	color?: DitherColor | undefined;
	/** Texture of the rule. Default `gradient`. */
	fill?: DitherFillVariant | undefined;
	/** The threshold pattern. Default `bayer4`. */
	matrix?: DitherMatrix | undefined;
	/** Size of one dither cell in CSS px. Default 2 (3 for `dots`). */
	cell?: number | undefined;
	/** Decoration only (default). `false` gives an unlabelled rule `role="separator"`. */
	decorative?: boolean | undefined;
}

interface RuleProps {
	orientation: "horizontal" | "vertical";
	variant: RuleVariant;
	side: RuleSide;
	options: Pick<
		DitherDividerProps,
		"thickness" | "fade" | "strength" | "fill" | "matrix" | "cell"
	>;
	hue: string | undefined;
	className?: string | undefined;
	/** The one rule per divider that is announced as the separator. */
	separator?: boolean;
}

function Rule({
	orientation,
	variant,
	side,
	options,
	hue,
	className,
	separator = false,
}: RuleProps) {
	const rule = resolveRule({ variant, side, ...options });
	const mask: BandMask = {
		// The bands run along the rule, so the fade is lengthwise.
		axis: orientation === "horizontal" ? "x" : "y",
		stops: rule.stops,
		cell: rule.cell,
		...(rule.texture ? { texture: rule.texture } : {}),
		...(rule.matrix ? { matrix: rule.matrix } : {}),
	};
	const shared = {
		"data-slot": "divider-rule",
		className: cn(
			"m-0 shrink-0 border-0 forced-colors:bg-[CanvasText]",
			hue ? "bg-(--nc-rule)" : "bg-ink-4/70",
			orientation === "horizontal" ? "w-full" : "self-stretch",
			bandMaskClass(mask),
			className,
		),
		style: {
			...(orientation === "horizontal"
				? { height: rule.thickness }
				: { width: rule.thickness }),
			...(hue ? { "--nc-rule": hue } : null),
		} as React.CSSProperties,
	};
	if (separator) return <hr aria-orientation={orientation} {...shared} />;
	return <span aria-hidden {...shared} />;
}

/**
 * A rule drawn in dither. `fade` dissolves at both ends, `solid` holds its
 * density, `dots` is a perforation line. With a `label` the rule runs to both
 * sides of it and fades away from the text; the text stays crisp, only the
 * rule is dithered. Decorative unless `decorative={false}`.
 */
function DitherDivider({
	orientation = "horizontal",
	variant = "fade",
	label,
	thickness,
	fade,
	strength,
	color,
	fill,
	matrix,
	cell,
	decorative = true,
	className,
	...props
}: DitherDividerProps) {
	const labelled = orientation === "horizontal" && label != null;
	const hue = color === undefined ? undefined : inkCss(color);
	const options = {
		...(thickness !== undefined ? { thickness } : {}),
		...(fade !== undefined ? { fade } : {}),
		...(strength !== undefined ? { strength } : {}),
		...(fill !== undefined ? { fill } : {}),
		...(matrix !== undefined ? { matrix } : {}),
		...(cell !== undefined ? { cell } : {}),
	};
	return (
		<div
			data-slot="divider"
			className={cn(
				orientation === "horizontal"
					? "flex w-full items-center gap-3"
					: "flex self-stretch",
				className,
			)}
			{...props}
		>
			<Rule
				separator={!decorative}
				orientation={orientation}
				variant={variant}
				side={labelled ? "start" : "both"}
				options={options}
				hue={hue}
				className={labelled ? "flex-1" : undefined}
			/>
			{labelled ? (
				<>
					<span className="shrink-0 font-mono text-2xs text-ink-4 uppercase tracking-[0.08em]">
						{label}
					</span>
					<Rule
						orientation="horizontal"
						variant={variant}
						side="end"
						options={options}
						hue={hue}
						className="flex-1"
					/>
				</>
			) : null}
		</div>
	);
}

export { DitherDivider };
