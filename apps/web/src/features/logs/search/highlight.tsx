import { cn } from "cn";
import { Fragment, memo } from "react";
import { findHighlights, type HighlightTerm } from "./match";

interface HighlightedProps {
	text: string;
	/** The line lower-cased (`ParsedLine.lower`); ranges are looked up in it. */
	lower: string;
	terms: readonly HighlightTerm[];
	/** The level word inside `text`, drawn with `levelClass`. */
	levelSpan?: { start: number; end: number } | undefined;
	levelClass?: string | undefined;
}

interface Piece {
	start: number;
	end: number;
	mark: boolean;
	level: boolean;
}

function pieces(
	length: number,
	ranges: readonly (readonly [number, number])[],
	levelSpan: HighlightedProps["levelSpan"],
): Piece[] {
	const cuts = new Set<number>([0, length]);
	for (const [start, end] of ranges) {
		cuts.add(Math.min(start, length));
		cuts.add(Math.min(end, length));
	}
	if (levelSpan) {
		cuts.add(levelSpan.start);
		cuts.add(levelSpan.end);
	}
	const points = [...cuts].sort((a, b) => a - b);
	const out: Piece[] = [];
	for (let i = 0; i < points.length - 1; i++) {
		const start = points[i] as number;
		const end = points[i + 1] as number;
		out.push({
			start,
			end,
			mark: ranges.some(([from, to]) => start >= from && end <= to),
			level:
				levelSpan !== undefined &&
				start >= levelSpan.start &&
				end <= levelSpan.end,
		});
	}
	return out;
}

/**
 * A log line with the search matches marked and its level word coloured.
 * Marks are a background, the text underneath is unchanged, so selecting and
 * copying a line gives the line.
 */
export const Highlighted = memo(function Highlighted({
	text,
	lower,
	terms,
	levelSpan,
	levelClass,
}: HighlightedProps) {
	const ranges = terms.length === 0 ? [] : findHighlights(terms, lower);
	if (ranges.length === 0 && !levelSpan) return <>{text}</>;
	return (
		<>
			{pieces(text.length, ranges, levelSpan).map((piece) => {
				const slice = text.slice(piece.start, piece.end);
				const inner = piece.mark ? (
					<mark
						key={piece.start}
						className="rounded-xs bg-accent-ink/30 text-ink-1"
					>
						{slice}
					</mark>
				) : (
					slice
				);
				return piece.level ? (
					<span key={piece.start} className={cn(levelClass)}>
						{inner}
					</span>
				) : (
					<Fragment key={piece.start}>{inner}</Fragment>
				);
			})}
		</>
	);
});
