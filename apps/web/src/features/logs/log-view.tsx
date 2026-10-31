import { useVirtualizer } from "@tanstack/react-virtual";
import { cn } from "cn";
import { type ReactNode, useEffect, useRef } from "react";
import type { LogLine } from "#/api/sse";
import { Badge } from "#/components/ui/badge";
import type { LampTone } from "#/components/ui/lamp";
import { fadeMask, useEdgeFade } from "#/lib/hooks/use-edge-fade";
import { lowerBound, STAMP_LOCALE } from "./history/model";
import type { LogLevel } from "./level";
import { Highlighted } from "./search/highlight";
import { parsedFor } from "./search/line";
import type { HighlightTerm } from "./search/match";

const ROW_HEIGHT = 20;

function timeOf(ts: string | undefined): string {
	if (!ts) return "";
	const date = new Date(ts);
	if (Number.isNaN(date.getTime())) return "";
	return date.toLocaleTimeString(STAMP_LOCALE, { hour12: false });
}

/** "20 Oct 14:02:11": stored history spans days, so its rows say which. */
function dateTimeOf(ts: string | undefined): string {
	if (!ts) return "";
	const date = new Date(ts);
	if (Number.isNaN(date.getTime())) return "";
	const day = date.toLocaleDateString(STAMP_LOCALE, {
		day: "numeric",
		month: "short",
	});
	return `${day} ${date.toLocaleTimeString(STAMP_LOCALE, { hour12: false })}`;
}

/** Colour of the level word. The word itself always stays in the line. */
const LEVEL_CLASS: Record<LogLevel, string> = {
	debug: "text-ink-4",
	info: "text-info",
	warn: "text-status-staged-on",
	error: "font-semibold text-status-failed-on",
};

/** The level as a text tag: the word is the cue, the colour only backs it. */
const LEVEL_TAG: Record<LogLevel, string> = {
	debug: "DEBUG",
	info: "INFO",
	warn: "WARN",
	error: "ERROR",
};

/** Chip tone per stream of a deployment log: the phase it came from. */
const STREAM_TONE: Record<string, LampTone> = {
	build: "building",
	deploy: "deploying",
	runtime: "live",
};

export interface LogViewProps {
	lines: readonly LogLine[];
	/** Marked inside each line; filtering is the caller's job. */
	terms?: readonly HighlightTerm[];
	/** Keep the newest line in view. */
	follow: boolean;
	/** The reader scrolled away from the tail by hand. */
	onLetGo(): void;
	label: string;
	/** Shown instead of rows when there are none. */
	empty: ReactNode;
	/** Tag each row with its stream (`build`, `deploy`, `runtime`). */
	showStream?: boolean;
	/** A level tag column (ERROR, WARN, INFO, DEBUG) when the line states a level. */
	showLevel?: boolean;
	/** The name of the service that wrote the line, with `serviceIcon` before it. */
	showService?: boolean;
	serviceIcon?: (line: LogLine) => ReactNode;
	/** The date before the time on each row, for stored history. */
	showDate?: boolean;
	/**
	 * Scrolls to the line with this id (or the first one after it) each time
	 * `nonce` changes: to the newest line when a history loads, back to where
	 * the reader was when older lines are added in front.
	 */
	reveal?: { id: number; align: "start" | "end"; nonce: number };
	/** Give it the inset: `surface-well rounded-card` plus a height or `absolute inset-0`. */
	className?: string;
}

/**
 * A virtualised log window: only the rows in view exist in the DOM, so a
 * 10,000-line build log costs about as much as a 50-line one. Mono 12px text,
 * timestamps in ink-4, the level word (when the line states one) coloured.
 * Only the reader's own gestures (wheel up, touch, arrow/page keys, the
 * scrollbar) call `onLetGo`; appended lines also fire scroll events and must not.
 */
export function LogView({
	lines,
	terms = [],
	follow,
	onLetGo,
	label,
	empty,
	showStream = false,
	showLevel = false,
	showService = false,
	serviceIcon,
	showDate = false,
	reveal,
	className,
}: LogViewProps) {
	const scrollRef = useRef<HTMLDivElement>(null);
	const virtualizer = useVirtualizer({
		count: lines.length,
		getScrollElement: () => scrollRef.current,
		estimateSize: () => ROW_HEIGHT,
		overscan: 24,
	});

	const revealNonce = reveal?.nonce;
	const latest = useRef({ lines, reveal });
	latest.current = { lines, reveal };
	useEffect(() => {
		const { lines: current, reveal: target } = latest.current;
		if (revealNonce === undefined || !target || current.length === 0) return;
		const index = Math.min(lowerBound(current, target.id), current.length - 1);
		virtualizer.scrollToIndex(index, { align: target.align });
	}, [revealNonce, virtualizer]);

	useEffect(() => {
		if (follow && lines.length > 0) {
			virtualizer.scrollToIndex(lines.length - 1, { align: "end" });
		}
	}, [follow, lines.length, virtualizer]);

	const fade = useEdgeFade(scrollRef);
	const mask = fadeMask(fade);

	return (
		<div
			ref={scrollRef}
			onWheel={(event) => event.deltaY < 0 && onLetGo()}
			onTouchMove={onLetGo}
			onPointerDown={(event) =>
				event.target === event.currentTarget && onLetGo()
			}
			onKeyDown={(event) => {
				if (["ArrowUp", "PageUp", "Home"].includes(event.key)) onLetGo();
			}}
			role="log"
			aria-label={label}
			aria-live="off"
			// A scrollable region must be reachable by keyboard so arrows and
			// PageUp/PageDown can scroll it.
			// biome-ignore lint/a11y/noNoninteractiveTabindex: keyboard access to a scrollable log
			tabIndex={0}
			// A line is never wrapped (a stack trace or a JSON line keeps its
			// shape): the whole list scrolls sideways as one, and the end that
			// continues fades, so a cut line reads as "more this way".
			style={mask ? { maskImage: mask, WebkitMaskImage: mask } : undefined}
			className={cn(
				"@container overflow-auto overscroll-contain py-1.5 font-mono text-[12px] text-ink-2 leading-[20px] outline-none focus-visible:focus-ring",
				className,
			)}
		>
			{lines.length === 0 ? (
				<div className="p-4 font-sans text-[13px] text-ink-3">{empty}</div>
			) : (
				<div
					className="relative w-max min-w-full"
					style={{ height: virtualizer.getTotalSize() }}
				>
					{virtualizer.getVirtualItems().map((row) => {
						const line = lines[row.index];
						if (!line) return null;
						const parsed = parsedFor(line);
						return (
							<div
								key={line.id}
								className="absolute top-0 left-0 flex min-w-full items-center gap-3 px-3 hover:bg-white/5"
								style={{
									height: ROW_HEIGHT,
									transform: `translateY(${row.start}px)`,
								}}
							>
								<span
									className={cn(
										"shrink-0 text-ink-4 tabular-nums",
										showDate ? "w-[18ch]" : "w-[58px]",
									)}
								>
									{showDate ? dateTimeOf(line.ts) : timeOf(line.ts)}
								</span>
								{showService ? (
									<span className="flex w-[96px] shrink-0 items-center gap-1.5 text-ink-3 @max-[520px]:w-[72px]">
										{serviceIcon?.(line)}
										<span className="truncate">
											{line.serviceName ?? "service"}
										</span>
									</span>
								) : null}
								{showStream && line.stream ? (
									<Badge
										tone={STREAM_TONE[line.stream] ?? "off"}
										className="w-[54px] shrink-0 py-0.5"
									>
										{line.stream}
									</Badge>
								) : null}
								{showLevel ? (
									<span
										className={cn(
											"type-micro w-[38px] shrink-0 @max-[520px]:hidden",
											parsed.level && LEVEL_CLASS[parsed.level],
										)}
									>
										{parsed.level ? LEVEL_TAG[parsed.level] : ""}
									</span>
								) : null}
								<span className="whitespace-pre">
									<Highlighted
										text={parsed.text}
										lower={parsed.lower}
										terms={terms}
										levelSpan={parsed.levelSpan}
										levelClass={parsed.level && LEVEL_CLASS[parsed.level]}
									/>
								</span>
							</div>
						);
					})}
				</div>
			)}
		</div>
	);
}
