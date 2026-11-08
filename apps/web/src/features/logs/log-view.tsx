import { useVirtualizer } from "@tanstack/react-virtual";
import { cn } from "cn";
import {
	type KeyboardEvent,
	type ReactNode,
	useEffect,
	useRef,
	useState,
} from "react";
import type { LogLine } from "#/api/sse";
import { Badge } from "#/components/ui/badge";
import type { LampTone } from "#/components/ui/lamp";
import {
	RequestLine,
	requestFields,
} from "#/features/logs-page/explorer/request-line";
import {
	ControlMark,
	controlSegments,
} from "#/features/logs-page/explorer/safe-text";
import { fadeMask, useEdgeFade } from "#/lib/hooks/use-edge-fade";
import { lowerBound, STAMP_LOCALE } from "./history/model";
import type { LogLevel } from "./level";
import { Highlighted } from "./search/highlight";
import { type ParsedLine, parsedFor } from "./search/line";
import type { HighlightTerm } from "./search/match";

const ROW_HEIGHT = 20;
/** What PageUp and PageDown move the active row by. */
const PAGE_ROWS = 10;

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

/** How close to the end of the list still counts as being at the bottom: about a row. */
const BOTTOM_SLACK = 24;

/**
 * What identifies a line across reloads: the history rebuilds every line
 * object (and its id) when older pages are added, and a live merge may
 * restart, so the selected row is found by what the line says.
 */
function lineKey(line: LogLine): string {
	return `${line.serviceId ?? ""}|${line.ts ?? ""}|${line.line}`;
}

/**
 * The text of a line that is not a request, with its matches marked and its
 * level word coloured. What the program wrote must not steer how the row is
 * drawn, so control and bidi characters are cut out and shown as escapes;
 * the text between them is marked on its own, at the offsets it had in the
 * line. A tab stays: it hides and reorders nothing, and stack traces use it.
 */
function LineText({
	parsed,
	terms,
	levelClass,
}: {
	parsed: ParsedLine;
	terms: readonly HighlightTerm[];
	levelClass: string | undefined;
}) {
	const { text, lower, levelSpan } = parsed;
	const segments = controlSegments(text, { keepTabs: true });
	const first = segments[0];
	if (segments.length === 0 || (segments.length === 1 && !first?.control)) {
		return (
			<Highlighted
				text={text}
				lower={lower}
				terms={terms}
				levelSpan={levelSpan}
				levelClass={levelClass}
			/>
		);
	}
	return (
		<>
			{segments.map(({ start, end, control }) => {
				if (control)
					return <ControlMark key={start} char={text[start] ?? ""} />;
				const inside =
					levelSpan && levelSpan.start >= start && levelSpan.end <= end
						? { start: levelSpan.start - start, end: levelSpan.end - start }
						: undefined;
				return (
					<Highlighted
						key={start}
						text={text.slice(start, end)}
						lower={lower.slice(start, end)}
						terms={terms}
						levelSpan={inside}
						levelClass={levelClass}
					/>
				);
			})}
		</>
	);
}

export interface LogViewProps {
	lines: readonly LogLine[];
	/** Marked inside each line; filtering is the caller's job. */
	terms?: readonly HighlightTerm[];
	/** Keep the newest line in view. */
	follow: boolean;
	/** The reader scrolled away from the tail by hand. */
	onLetGo(): void;
	/**
	 * Each time the list scrolls: whether it is within a row of its end. The
	 * owner resumes following when the reader scrolls back down.
	 */
	onAtBottomChange?: (atBottom: boolean) => void;
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
	/** The host before the path of a request row. Default on; off when every row has the same one. */
	showHost?: boolean;
	/** The date before the time on each row, for stored history. */
	showDate?: boolean;
	/**
	 * Scrolls to the line with this id (or the first one after it) each time
	 * `nonce` changes: to the newest line when a history loads, back to where
	 * the reader was when older lines are added in front.
	 */
	reveal?: { id: number; align: "start" | "end"; nonce: number };
	/** The line whose detail is open: its row is filled and marked. */
	selected?: LogLine | null;
	/**
	 * Makes each row a button: a click, Enter or Space opens the line, and the
	 * arrow keys move between rows. Without it the rows are plain text.
	 */
	onSelect?: (line: LogLine) => void;
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
	onAtBottomChange,
	label,
	empty,
	showStream = false,
	showLevel = false,
	showService = false,
	showHost = true,
	serviceIcon,
	showDate = false,
	reveal,
	selected,
	onSelect,
	className,
}: LogViewProps) {
	const scrollRef = useRef<HTMLDivElement>(null);
	const selectedKey = selected ? lineKey(selected) : null;
	const virtualizer = useVirtualizer({
		count: lines.length,
		getScrollElement: () => scrollRef.current,
		estimateSize: () => ROW_HEIGHT,
		overscan: 24,
	});

	// Keyboard: rows are tab stops in turn (roving tabindex). The arrows,
	// PageUp/PageDown, Home and End move the active row; it is focused once it
	// is in the DOM, which a long jump needs a scroll to bring about.
	const [activeId, setActiveId] = useState<number | null>(null);
	const pendingFocus = useRef(false);
	useEffect(() => {
		if (!pendingFocus.current) return;
		const row = scrollRef.current?.querySelector<HTMLElement>(
			`[data-line-id="${activeId}"]`,
		);
		if (!row) return;
		pendingFocus.current = false;
		row.focus({ preventScroll: true });
	});
	const rowToward = (key: string): number | null => {
		const last = lines.length - 1;
		if (last < 0) return null;
		const at = activeId === null ? -1 : lowerBound(lines, activeId);
		const current = lines[at]?.id === activeId ? at : -1;
		const first = virtualizer.range?.startIndex ?? 0;
		const bottom = virtualizer.range?.endIndex ?? last;
		const clamp = (index: number) => Math.min(last, Math.max(0, index));
		switch (key) {
			case "ArrowDown":
				return clamp(current === -1 ? first : current + 1);
			case "ArrowUp":
				return clamp(current === -1 ? bottom : current - 1);
			case "PageDown":
				return clamp((current === -1 ? first : current) + PAGE_ROWS);
			case "PageUp":
				return clamp((current === -1 ? bottom : current) - PAGE_ROWS);
			case "Home":
				return 0;
			case "End":
				return last;
			default:
				return null;
		}
	};
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
			onScroll={(event) => {
				const { scrollHeight, scrollTop, clientHeight } = event.currentTarget;
				onAtBottomChange?.(
					scrollHeight - scrollTop - clientHeight <= BOTTOM_SLACK,
				);
			}}
			onKeyDown={(event) => {
				// Moving between rows by key is the reader's own navigation: following
				// would scroll the active row out of the DOM and drop focus.
				const to = onSelect ? rowToward(event.key) : null;
				if (["ArrowUp", "PageUp", "Home"].includes(event.key) || to !== null)
					onLetGo();
				if (to === null) return;
				event.preventDefault();
				pendingFocus.current = true;
				setActiveId(lines[to]?.id ?? null);
				virtualizer.scrollToIndex(to, { align: "auto" });
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
						const request =
							line.stream === "requests" ? requestFields(parsed) : null;
						const picked =
							selectedKey !== null && lineKey(line) === selectedKey;
						return (
							<div
								key={line.id}
								className={cn(
									"absolute top-0 left-0 flex min-w-full items-center gap-3 px-3 hover:bg-white/5",
									onSelect &&
										"cursor-pointer outline-none focus-visible:bg-white/10 focus-visible:shadow-[inset_0_0_0_1px_var(--color-accent-ink)]",
									picked &&
										"bg-white/10 shadow-[inset_2px_0_0_var(--color-accent-ink)]",
								)}
								style={{
									height: ROW_HEIGHT,
									transform: `translateY(${row.start}px)`,
								}}
								{...(onSelect
									? {
											role: "button",
											tabIndex: line.id === activeId ? 0 : -1,
											"data-line-id": line.id,
											"aria-pressed": picked,
											onFocus: () => {
												setActiveId(line.id);
												onLetGo();
											},
											onClick: () => onSelect(line),
											onKeyDown: (event: KeyboardEvent) => {
												if (event.key !== "Enter" && event.key !== " ") return;
												event.preventDefault();
												onSelect(line);
											},
										}
									: {})}
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
								{request ? (
									<RequestLine
										fields={request}
										terms={terms}
										showHost={showHost}
									/>
								) : (
									<span className="whitespace-pre">
										<LineText
											parsed={parsed}
											terms={terms}
											levelClass={parsed.level && LEVEL_CLASS[parsed.level]}
										/>
									</span>
								)}
							</div>
						);
					})}
				</div>
			)}
		</div>
	);
}
