import { cn } from "cn";
import type { ReactNode } from "react";
import type { DitherMatrix } from "../../lib/bayer";
import { DitherBackdrop } from "./backdrop";
import { type DitherColor, inkCss } from "./fill";
import {
	type EmptyStateIllustration,
	ILLUSTRATION_HEIGHT,
	ILLUSTRATION_WIDTH,
	ILLUSTRATIONS,
	illustrationPaths,
} from "./illustration";
import { ensureInterfaceStyles } from "./keyframes";
import {
	Empty,
	EmptyContent,
	EmptyDescription,
	EmptyHeader,
	EmptyMedia,
} from "./primitives";

export type { EmptyStateIllustration } from "./illustration";

export interface DitherEmptyStateProps {
	/** What is missing, as an invitation: "Add your first server". */
	title: ReactNode;
	/** The one clear action. */
	action?: ReactNode;
	/** A quieter second way forward, beside the primary action. */
	secondaryAction?: ReactNode;
	/** Shortcut hints under the actions, e.g. `<><Kbd variant="key">N</Kbd> new service</>`. */
	hint?: ReactNode;
	/** `dashed` draws a recess outline (inside an existing panel); `raised` a card; `none` no frame. Default `raised`. */
	frame?: "raised" | "dashed" | "none";
	className?: string;
	/** A 16px icon for the engraved tile above the title. Ignored when there is an `illustration` or `media`. */
	icon?: ReactNode;
	/** Draw the dither backdrop behind the content. Default true; `false` is the plain look. */
	backdrop?: boolean | undefined;
	/** A lit dithered object: `box`, `search`, `folder`, `planet`, `rack`, `radar`, `key`, `network` or `bell`. */
	illustration?: EmptyStateIllustration | undefined;
	/** Your own art in the object's place, for an illustration that is not a dithered object. Wins over `icon`; `illustration` wins over it. */
	media?: ReactNode;
	/** One or two sentences: why it is empty and what to do next. */
	body?: ReactNode;
	/** Same as `body`. */
	description?: ReactNode;
	/** Colour of a dithered object: a palette name, a custom property or any CSS colour. Default `ink`. */
	color?: DitherColor | undefined;
	/** The threshold pattern of a dithered object. Default `bayer4`. */
	matrix?: DitherMatrix | undefined;
	/** Bob the object up and down by one cell, in steps. Default true. */
	animate?: boolean | undefined;
	/** Heading level of the title. Default `h3`. */
	titleAs?: "h2" | "h3" | "h4" | undefined;
	/** Width of a dithered object: `lg` is 336px (3px cells), `md` 224px (2px cells), for a panel or a well. Default `lg`. */
	size?: "md" | "lg" | undefined;
}

function isDitheredObject(
	name: DitherEmptyStateProps["illustration"],
): name is EmptyStateIllustration {
	return (
		name !== undefined && (ILLUSTRATIONS as readonly string[]).includes(name)
	);
}

/**
 * A small lit object in dither cells: faces turned to the light dense, faces
 * turned away sparse, a seam between the parts and a faint halo and floor
 * shadow. Two cached paths, so it costs nothing at runtime; the bob moves it
 * one whole cell in steps, so the dither never smears.
 */
function DitheredIllustration({
	name,
	color,
	matrix,
	animate,
}: {
	name: EmptyStateIllustration;
	color: DitherColor | undefined;
	matrix: DitherMatrix;
	animate: boolean;
}) {
	ensureInterfaceStyles();
	const paths = illustrationPaths(name, matrix);
	return (
		<svg
			aria-hidden
			data-slot="dither-illustration"
			data-illustration={name}
			viewBox={`0 0 ${ILLUSTRATION_WIDTH} ${ILLUSTRATION_HEIGHT}`}
			className="block h-auto w-full forced-colors:hidden"
			style={{ color: inkCss(color) }}
			shapeRendering="crispEdges"
		>
			<path d={paths.ambient} fill="currentColor" opacity={0.4} />
			<g className={animate ? "nc-if-bob" : undefined}>
				<path d={paths.object} fill="currentColor" />
			</g>
		</svg>
	);
}

/**
 * An empty state that invites: an object (a dithered illustration, your own
 * `media`, or an icon tile), a title that names the next step, a
 * sentence of why, one primary and one secondary action, and the shortcuts
 * that do the same. The dither backdrop is on by default; pass
 * `backdrop={false}` for the plain look. The illustration is decoration and
 * hidden from assistive tech; the title is a real heading (`titleAs`).
 */
function DitherEmptyState({
	illustration,
	media,
	icon,
	title,
	body,
	description,
	action,
	secondaryAction,
	hint,
	frame = "raised",
	backdrop = true,
	color,
	matrix = "bayer4",
	animate = true,
	size = "lg",
	titleAs: Title = "h3",
	className,
}: DitherEmptyStateProps) {
	const text = description ?? body;
	return (
		<Empty
			data-slot="empty-state"
			className={cn(
				"py-14",
				backdrop && "relative isolate overflow-hidden",
				frame === "raised" && "surface-raised rounded-card",
				frame === "dashed" &&
					"rounded-card border border-line-strong border-dashed",
				className,
			)}
		>
			{backdrop ? <DitherBackdrop /> : null}
			<EmptyHeader>
				{isDitheredObject(illustration) ? (
					<EmptyMedia
						className={cn(
							"mb-4 w-full",
							size === "md" ? "max-w-[224px]" : "max-w-[336px]",
						)}
					>
						<DitheredIllustration
							name={illustration}
							color={color}
							matrix={matrix}
							animate={animate}
						/>
					</EmptyMedia>
				) : media ? (
					<EmptyMedia className="mb-4 text-ink-3">{media}</EmptyMedia>
				) : icon ? (
					<EmptyMedia variant="icon">{icon}</EmptyMedia>
				) : null}
				<Title
					data-slot="empty-title"
					className="type-section-title text-ink-1"
				>
					{title}
				</Title>
				{text ? <EmptyDescription>{text}</EmptyDescription> : null}
			</EmptyHeader>
			{action || secondaryAction ? (
				<EmptyContent className="flex-row flex-wrap items-center justify-center gap-2">
					{action}
					{secondaryAction}
				</EmptyContent>
			) : null}
			{hint ? (
				<div className="inline-flex items-center gap-1.5 font-mono text-2xs text-ink-4 tracking-[0.04em]">
					{hint}
				</div>
			) : null}
		</Empty>
	);
}

export { DitherEmptyState };
