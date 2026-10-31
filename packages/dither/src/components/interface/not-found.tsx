import { cn } from "cn";
import type * as React from "react";
import type { DitherMatrix } from "../../lib/bayer";
import { DitherBackdrop } from "./backdrop";
import { BlockCode } from "./block-code";
import type { DitherBloom, DitherColor, DitherFillVariant } from "./fill";
import {
	Empty,
	EmptyContent,
	EmptyDescription,
	EmptyHeader,
} from "./primitives";

export interface DitherNotFoundProps {
	/** The status shown big. Default "404". */
	code?: string;
	title?: React.ReactNode;
	body?: React.ReactNode;
	/** Same as `body`. */
	description?: React.ReactNode;
	/** A short mono line under the text: the requested path, or an error reference. */
	detail?: React.ReactNode;
	/** Buttons or links: "Go to projects", "Go back". */
	actions?: React.ReactNode;
	/** Heading level of the title. Default `h1`; use `h2` when the page has its own. */
	titleAs?: "h1" | "h2" | undefined;
	/** Colour of the digits: a palette name, a custom property or any CSS colour. Default `ink`. */
	color?: DitherColor | undefined;
	/** Texture of the digit faces. Default `gradient`. */
	fill?: DitherFillVariant | undefined;
	/** A soft glow in the code's colour. */
	bloom?: DitherBloom | undefined;
	/** Size of one dither cell in CSS px. Default 4. */
	cell?: number | undefined;
	/** Depth of the extruded side in cells. 0 gives flat digits. Default 4. */
	depth?: number | undefined;
	/** The threshold pattern. Default `bayer4`. */
	matrix?: DitherMatrix | undefined;
	/** Print the digits in on mount and sweep light across them. Default true. */
	animate?: boolean | undefined;
	/** Put the drifting dither field behind the block. Off by default: it is a WebGL context behind a card that already carries the printed digits. */
	backdrop?: boolean;
	className?: string;
}

/**
 * The "nothing at this address" page, composed like the empty state: a
 * raised card, block digits that print in and catch the light, a title that
 * names what happened, one sentence of why, a detail line, the way back. The
 * digits and the backdrop are decoration around crisp text: the digits are
 * `aria-hidden` and the code is spoken as "Error 404" before the title. The
 * animated backdrop is opt-in (`backdrop`).
 */
function DitherNotFound({
	code = "404",
	title = "Nothing at this address",
	body = "It may have been moved or deleted. Check the address, or go back to your projects.",
	description,
	detail,
	actions,
	titleAs: Title = "h1",
	color,
	fill = "gradient",
	bloom,
	cell = 4,
	depth = 4,
	matrix = "bayer4",
	animate = true,
	backdrop = false,
	className,
}: DitherNotFoundProps) {
	const text = description ?? body;
	return (
		<Empty
			data-slot="not-found"
			className={cn(
				"surface-raised relative isolate overflow-hidden rounded-card py-14",
				className,
			)}
		>
			{backdrop ? <DitherBackdrop /> : null}
			<BlockCode
				code={code}
				color={color}
				fill={fill}
				bloom={bloom}
				cell={cell}
				depth={depth}
				animate={animate}
				matrix={matrix}
			/>
			<EmptyHeader>
				<Title
					data-slot="empty-title"
					className="type-section-title text-ink-1"
				>
					<span className="sr-only">Error {code}. </span>
					{title}
				</Title>
				<EmptyDescription>{text}</EmptyDescription>
				{detail ? (
					<p
						data-slot="not-found-detail"
						className="mt-1 font-mono text-2xs text-ink-4 tracking-[0.04em]"
					>
						{detail}
					</p>
				) : null}
			</EmptyHeader>
			{actions ? (
				<EmptyContent className="flex-row flex-wrap items-center justify-center gap-2">
					{actions}
				</EmptyContent>
			) : null}
		</Empty>
	);
}

export { DitherNotFound };
