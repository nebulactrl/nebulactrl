import { cn } from "cn";
import { SearchIcon, XIcon } from "lucide-react";
import {
	type KeyboardEvent,
	useCallback,
	useEffect,
	useId,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { ToneIcon } from "#/components/common/tone-icon";
import { Input } from "#/components/ui/input";
import { Kbd } from "#/components/ui/kbd";
import type { FacetStats } from "./facets";
import { highlightSegments, type ParsedQuery, type SegmentKind } from "./query";
import {
	applySuggestion,
	type Suggestion,
	type SuggestionKind,
	suggest,
} from "./suggest";

const SEGMENT_CLASS: Record<SegmentKind, string> = {
	plain: "",
	text: "text-ink-2",
	phrase: "text-ink-1",
	key: "text-accent-ink",
	attr: "text-info",
	value: "text-ink-1",
	operator: "text-status-staged-on",
	invalid:
		"text-status-failed-on underline decoration-status-failed decoration-wavy underline-offset-4",
};

const KIND_LABEL: Record<SuggestionKind, string> = {
	key: "facet",
	attr: "attribute",
	value: "value",
	operator: "operator",
	recent: "recent",
};

export interface SearchInputProps {
	value: string;
	onChange(next: string): void;
	/** `value` parsed, for the colours. */
	parsed: ParsedQuery;
	/** What is wrong with the query, once it has settled. */
	error?: string | undefined;
	/** The buffer's counts: where values and attributes for suggestions come from. */
	stats: Pick<FacetStats, "facets" | "attrs">;
	/** Changes whenever the counts do. */
	statsVersion: number;
	recent: readonly string[];
	/** Enter with no suggestion chosen, or leaving the box. */
	onSubmit(): void;
	label: string;
	placeholder?: string;
	className?: string;
	/** `/` focuses the box when nothing is being typed into. Default on. */
	hotkey?: boolean;
}

function isEditable(target: EventTarget | null): boolean {
	if (!(target instanceof HTMLElement)) return false;
	return (
		target.isContentEditable ||
		target instanceof HTMLInputElement ||
		target instanceof HTMLTextAreaElement ||
		target instanceof HTMLSelectElement
	);
}

/**
 * The search box: an input whose text is drawn twice, once by the input
 * (transparent, so the caret and selection stay native) and once by an overlay
 * that colours the tokens. Suggestions follow the ARIA combobox pattern (an
 * editable combobox with a list popup): focus stays in the input, the active
 * option is `aria-activedescendant`, and nothing animates.
 */
export function SearchInput({
	value,
	onChange,
	parsed,
	error,
	stats,
	statsVersion,
	recent,
	onSubmit,
	label,
	placeholder,
	className,
	hotkey = true,
}: SearchInputProps) {
	const id = useId();
	const listId = `${id}-list`;
	const errorId = `${id}-error`;
	const optionId = (index: number) => `${id}-option-${index}`;
	const inputRef = useRef<HTMLInputElement>(null);
	const overlayRef = useRef<HTMLSpanElement>(null);
	const [focused, setFocused] = useState(false);
	const [open, setOpen] = useState(false);
	const [caret, setCaret] = useState(value.length);
	const [active, setActive] = useState(-1);
	// Where the caret goes once the value has been replaced by an accepted suggestion.
	const pendingCaret = useRef<number | null>(null);

	// biome-ignore lint/correctness/useExhaustiveDependencies: statsVersion stands for the mutable counts.
	const items = useMemo<Suggestion[]>(
		() =>
			open
				? suggest(value, caret, {
						facets: stats.facets,
						attrs: stats.attrs,
						recent,
					})
				: [],
		[open, value, caret, stats, statsVersion, recent],
	);
	const expanded = open && items.length > 0;
	const segments = useMemo(() => highlightSegments(parsed), [parsed]);

	const syncScroll = useCallback(() => {
		const input = inputRef.current;
		const overlay = overlayRef.current;
		if (input && overlay)
			overlay.style.transform = `translateX(${-input.scrollLeft}px)`;
	}, []);

	// biome-ignore lint/correctness/useExhaustiveDependencies: runs after every value change
	useLayoutEffect(() => {
		const input = inputRef.current;
		if (input && pendingCaret.current !== null) {
			input.setSelectionRange(pendingCaret.current, pendingCaret.current);
			pendingCaret.current = null;
		}
		syncScroll();
	}, [value, syncScroll]);

	useEffect(() => {
		if (!hotkey) return;
		const onKey = (event: globalThis.KeyboardEvent) => {
			if (event.key !== "/" || event.metaKey || event.ctrlKey || event.altKey)
				return;
			if (event.defaultPrevented || isEditable(event.target)) return;
			event.preventDefault();
			inputRef.current?.focus();
		};
		document.addEventListener("keydown", onKey);
		return () => document.removeEventListener("keydown", onKey);
	}, [hotkey]);

	const accept = (suggestion: Suggestion) => {
		const next = applySuggestion(value, suggestion);
		pendingCaret.current = next.caret;
		onChange(next.value);
		setCaret(next.caret);
		setActive(-1);
		// A recent query is a whole search: close. A key or value leads to the next list.
		setOpen(suggestion.kind !== "recent");
	};

	const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
		const count = items.length;
		switch (event.key) {
			case "ArrowDown":
				event.preventDefault();
				if (!open) setOpen(true);
				else if (count > 0) setActive((index) => (index + 1) % count);
				break;
			case "ArrowUp":
				event.preventDefault();
				if (!open) setOpen(true);
				else if (count > 0)
					setActive((index) => (index <= 0 ? count - 1 : index - 1));
				break;
			case "Enter": {
				const chosen = expanded ? items[active] : undefined;
				event.preventDefault();
				if (chosen) accept(chosen);
				else {
					setOpen(false);
					onSubmit();
				}
				break;
			}
			case "Tab": {
				if (event.shiftKey || !expanded) break;
				const chosen = items[Math.max(active, 0)];
				if (chosen) {
					event.preventDefault();
					accept(chosen);
				}
				break;
			}
			case "Escape":
				// Closing the list must not also close the panel the box sits in.
				if (open) {
					event.preventDefault();
					event.stopPropagation();
					setOpen(false);
					setActive(-1);
				}
				break;
		}
	};

	return (
		<div className={cn("relative min-w-0", className)}>
			<div className="relative">
				<SearchIcon
					aria-hidden
					className="pointer-events-none absolute top-1/2 left-2.5 z-10 size-3.5 -translate-y-1/2 text-ink-3"
				/>
				<Input
					ref={inputRef}
					type="text"
					role="combobox"
					aria-label={label}
					aria-autocomplete="list"
					aria-expanded={expanded}
					aria-controls={listId}
					aria-activedescendant={
						expanded && active >= 0 ? optionId(active) : undefined
					}
					aria-invalid={error ? true : undefined}
					aria-describedby={error ? errorId : undefined}
					autoComplete="off"
					autoCapitalize="off"
					autoCorrect="off"
					spellCheck={false}
					placeholder={placeholder}
					value={value}
					className={cn(
						"pr-16 pl-8 font-mono text-transparent text-xs caret-ink-1 selection:bg-accent-ink/30 placeholder:text-ink-4 forced-colors:text-[CanvasText]",
						error && "border-status-failed",
					)}
					onChange={(event) => {
						onChange(event.target.value);
						setCaret(event.target.selectionStart ?? event.target.value.length);
						setOpen(true);
						setActive(-1);
						requestAnimationFrame(syncScroll);
					}}
					onSelect={(event) => {
						const at = event.currentTarget.selectionStart;
						if (at !== null) setCaret(at);
					}}
					onScroll={syncScroll}
					onKeyDown={onKeyDown}
					onFocus={() => {
						setFocused(true);
						setOpen(true);
					}}
					onBlur={() => {
						setFocused(false);
						setOpen(false);
						setActive(-1);
						onSubmit();
					}}
				/>
				<div
					aria-hidden
					className="pointer-events-none absolute inset-0 overflow-hidden pr-16 pl-[33px] font-mono text-xs forced-colors:hidden"
				>
					<span
						ref={overlayRef}
						className="flex h-full items-center whitespace-pre will-change-transform"
					>
						<span>
							{segments.map((segment) => (
								<span
									key={segment.start}
									className={SEGMENT_CLASS[segment.kind]}
								>
									{value.slice(segment.start, segment.end)}
								</span>
							))}
						</span>
					</span>
				</div>
				{value !== "" ? (
					<button
						type="button"
						aria-label="Clear search"
						className="absolute top-1/2 right-1.5 z-10 grid size-6 -translate-y-1/2 place-items-center rounded-tip text-ink-3 outline-none hover:text-ink-1 focus-visible:focus-ring"
						// Keep focus in the box; the click then clears and refocuses.
						onMouseDown={(event) => event.preventDefault()}
						onClick={() => {
							onChange("");
							setCaret(0);
							inputRef.current?.focus();
						}}
					>
						<XIcon aria-hidden className="size-3.5" />
					</button>
				) : focused || !hotkey ? null : (
					<Kbd
						className="absolute top-1/2 right-2 -translate-y-1/2"
						aria-hidden
					>
						/
					</Kbd>
				)}
			</div>

			{expanded ? (
				<div
					id={listId}
					role="listbox"
					aria-label="Search suggestions"
					className="surface-pop absolute top-full right-0 left-0 z-30 mt-1 max-h-[320px] overflow-auto overscroll-contain rounded-menu p-1"
				>
					{items.map((item, index) => (
						// Options are never focused: the input keeps focus and names the
						// active option with aria-activedescendant (ARIA editable combobox),
						// and Up/Down/Enter/Tab on the input are the keyboard path.
						// biome-ignore lint/a11y/useFocusableInteractive: active option is aria-activedescendant
						// biome-ignore lint/a11y/useKeyWithClickEvents: keys are handled on the combobox input
						<div
							key={`${item.kind}:${item.label}`}
							id={optionId(index)}
							role="option"
							aria-selected={index === active}
							// The mouse must not move focus out of the box.
							onMouseDown={(event) => event.preventDefault()}
							onClick={() => accept(item)}
							onMouseMove={() => active !== index && setActive(index)}
							className={cn(
								"flex cursor-default items-center gap-2.5 rounded-ctl border-l-2 border-transparent px-2 py-1 font-mono text-xs",
								index === active
									? "border-accent-ink bg-white/10 text-ink-1"
									: "text-ink-2",
							)}
						>
							<span className="type-micro w-[58px] shrink-0">
								{KIND_LABEL[item.kind]}
							</span>
							<span className="min-w-0 flex-1 truncate">{item.label}</span>
							{item.detail ? (
								<span className="shrink-0 text-ink-4 tabular-nums">
									{item.detail}
								</span>
							) : null}
						</div>
					))}
				</div>
			) : null}

			{error ? (
				<p
					id={errorId}
					className="mt-1.5 flex items-center gap-1.5 text-status-failed-on text-xs"
				>
					<ToneIcon tone="danger" className="size-3.5" />
					{error}
				</p>
			) : null}
		</div>
	);
}
