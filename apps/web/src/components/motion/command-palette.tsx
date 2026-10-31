"use client";

// beui.dev/components/blocks/command-palette
//
// Restyled to the neo-skeuomorphic system: a `surface-modal` panel, the
// prompt on an engraved well, 36px rows with a flat `bg-white/7` highlight,
// and keycaps for every shortcut. The design's ungrouped list with a group
// tag on the right of each row, a `›` prompt instead of a search icon.
// The highlight never animates: it follows the arrow keys instantly.
//
// Beyond upstream: a row can open a second level (→, or Enter on a row
// that only opens one; Backspace on an empty prompt goes back, and the
// levels open so far read as a breadcrumb), the query is a fuzzy match
// with the matched letters drawn emphasised, and the host can add rows
// that depend on what was typed (a pasted URL, a setting's name).

import { cn } from "cn";
import type { LucideIcon } from "lucide-react";
import { ChevronRightIcon } from "lucide-react";
import {
	Fragment,
	type ReactNode,
	useCallback,
	useEffect,
	useId,
	useMemo,
	useRef,
	useState,
} from "react";
import { createPortal } from "react-dom";
import { Kbd } from "#/components/ui/kbd";
import { labelMatchPositions, searchCommands } from "#/lib/command-search";
import { useOnOpen } from "#/lib/hooks/use-on-open";
import { useRowCursor } from "#/lib/hooks/use-row-cursor";
import { useTouchCapable } from "#/lib/hooks/use-touch-capable";

/** A second level under a row; its id names it in the breadcrumb path. */
export interface CommandLevel {
	/** Unique among the rows that can open a level. */
	id: string;
	/** The breadcrumb: "checkout". */
	title: string;
	/** The prompt while the level is open. */
	placeholder?: string;
	/** Asked for only while the level is open, so it can read live data. */
	items: () => CommandItem[];
}

interface CommandItemBase {
	id: string;
	label: string;
	group?: string;
	hint?: string;
	/** Words that find the row without being drawn. */
	keywords?: readonly string[];
	icon?: LucideIcon;
	badge?: ReactNode;
	/** A single-key shortcut for the same action, drawn as a key chip. */
	shortcut?: string;
	/**
	 * Only for rows from `queryItems`: the row leads the results instead of
	 * following the ones the typed text matched.
	 */
	pin?: boolean;
}

/**
 * A row runs something (`onSelect`, on Enter or a click), opens a level
 * (`next`, on → or a click on its chevron), or both. A row with only a
 * level opens it on Enter too.
 */
export type CommandItem = CommandItemBase &
	(
		| { onSelect: () => void; next?: CommandLevel }
		| { onSelect?: undefined; next: CommandLevel }
	);

/** How many results the palette draws at once. */
const MAX_ROWS = 10;

export interface CommandPaletteProps {
	items: CommandItem[];
	/** Optional empty-query ordering (recent, suggested, then commands). */
	initialItems?: CommandItem[];
	/**
	 * Rows that depend on the typed text, asked for at the top level on every
	 * query: the ones marked `pin` lead the results, the rest follow what the
	 * text matched in `items`. They are not filtered again.
	 */
	queryItems?: (query: string) => CommandItem[];
	/** Row ids, most recently used first; recent rows win ties between similar matches. */
	recent?: readonly string[];
	/** Opens with Cmd/Ctrl + this key. Default: "k" */
	shortcut?: string;
	placeholder?: string;
	emptyMessage?: string;
	open?: boolean;
	onOpenChange?: (open: boolean) => void;
	/** Runs before a closed palette opens, including through Cmd/Ctrl+K. */
	onBeforeOpen?: () => void;
	/** The ids of the levels open, outermost first; `[]` at the top and while closed. */
	onLevelChange?: (path: readonly string[]) => void;
}

/** The levels `path` names, resolved against the live items, and the rows of the last. */
function resolveLevels(
	items: readonly CommandItem[],
	path: readonly string[],
): { levels: CommandLevel[]; rows: readonly CommandItem[] } {
	const levels: CommandLevel[] = [];
	let rows = items;
	for (const id of path) {
		const next = rows.find((row) => row.next?.id === id)?.next;
		// A level whose row has left the list (the service was deleted while
		// its menu was open) ends the path there.
		if (!next) break;
		levels.push(next);
		rows = next.items();
	}
	return { levels, rows };
}

function EmphasisedLabel({
	label,
	positions,
}: {
	label: string;
	positions: readonly number[];
}) {
	if (positions.length === 0) return label;
	const hit = new Set(positions);
	const pieces: Array<{ text: string; hit: boolean }> = [];
	for (let i = 0; i < label.length; i++) {
		const isHit = hit.has(i);
		const last = pieces.at(-1);
		if (last && last.hit === isHit) last.text += label.charAt(i);
		else pieces.push({ text: label.charAt(i), hit: isHit });
	}
	let offset = 0;
	return pieces.map((piece) => {
		const key = offset;
		offset += piece.text.length;
		return piece.hit ? (
			<span key={key} className="text-accent-ink">
				{piece.text}
			</span>
		) : (
			<Fragment key={key}>{piece.text}</Fragment>
		);
	});
}

/** One footer hint: a keycap and what it does. */
function Hint({ keys, children }: { keys: string; children: ReactNode }) {
	return (
		<span className="inline-flex items-center gap-1.5">
			<Kbd>{keys}</Kbd>
			{children}
		</span>
	);
}

export function CommandPalette({
	items,
	initialItems,
	queryItems,
	recent,
	shortcut = "k",
	placeholder = "Type a command or search…",
	emptyMessage = "No results found.",
	open: controlledOpen,
	onOpenChange,
	onBeforeOpen,
	onLevelChange,
}: CommandPaletteProps) {
	const [internalOpen, setInternalOpen] = useState(false);
	const controlled = controlledOpen !== undefined;
	const open = controlled ? controlledOpen : internalOpen;
	const setOpen = useCallback(
		(v: boolean) => {
			if (v && !open) onBeforeOpen?.();
			if (!controlled) setInternalOpen(v);
			onOpenChange?.(v);
		},
		[controlled, onBeforeOpen, onOpenChange, open],
	);

	const [query, setQuery] = useState("");
	const [path, setPath] = useState<readonly string[]>([]);
	// Portal target only exists client-side; render nothing during SSR/hydration.
	const [mounted, setMounted] = useState(false);
	useEffect(() => setMounted(true), []);
	const uid = useId();
	const canTouch = useTouchCapable();
	const inputRef = useRef<HTMLInputElement>(null);
	const listRef = useRef<HTMLDivElement>(null);
	// Both open and close are instant (see the portal below).

	useEffect(() => {
		const onKey = (e: KeyboardEvent) => {
			if (
				(e.metaKey || e.ctrlKey) &&
				e.key.toLowerCase() === shortcut.toLowerCase()
			) {
				e.preventDefault();
				setOpen(!open);
				return;
			}
			if (e.key === "Escape" && open) {
				e.preventDefault();
				setOpen(false);
			}
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, [open, shortcut, setOpen]);

	useEffect(() => {
		if (!open) return;
		const root = document.documentElement;
		const previousRootOverflow = root.style.overflow;
		const previousBodyOverflow = document.body.style.overflow;
		root.style.overflow = "hidden";
		document.body.style.overflow = "hidden";
		return () => {
			root.style.overflow = previousRootOverflow;
			document.body.style.overflow = previousBodyOverflow;
		};
	}, [open]);

	// The levels open, resolved against the live items on every render so a
	// level that loads data fills in as it arrives.
	const { levels, rows: levelRows } = useMemo(
		() => resolveLevels(items, path),
		[items, path],
	);
	const nested = levels.length > 0;
	const levelPath = useMemo(() => levels.map((level) => level.id), [levels]);
	const levelKey = levelPath.join("\u0000");
	useEffect(() => {
		if (!onLevelChange) return;
		onLevelChange(open && levelKey !== "" ? levelKey.split("\u0000") : []);
	}, [open, levelKey, onLevelChange]);
	const level = levels.at(-1);

	// Querying is fuzzy over the displayed fields and the row's keywords,
	// best match first, recent rows first among equals. The empty prompt may
	// supply its recent/suggested ordering separately, at the top level only.
	// Only the first MAX_ROWS are drawn; the rest are counted so the footer
	// can say results are hidden instead of dropping them silently.
	const matches = useMemo(() => {
		if (!query.trim()) return nested ? levelRows : (initialItems ?? items);
		const found = searchCommands(levelRows, query, recent ? { recent } : {});
		if (nested || !queryItems) return found;
		const extra = queryItems(query);
		const taken = new Set<string>();
		const unique = (rows: readonly CommandItem[]) =>
			rows.filter((row) => {
				if (taken.has(row.id)) return false;
				taken.add(row.id);
				return true;
			});
		const lead = unique(extra.filter((row) => row.pin));
		const middle = unique(found);
		return [...lead, ...middle, ...unique(extra.filter((row) => !row.pin))];
	}, [initialItems, items, levelRows, nested, query, queryItems, recent]);
	const rows = useMemo(() => matches.slice(0, MAX_ROWS), [matches]);
	const hiddenCount = matches.length - rows.length;

	// Reserve the icon column only when at least one item brings an icon, so
	// icon-less lists don't render a dead gap before every label.
	const hasIcons = useMemo(() => rows.some((it) => it.icon), [rows]);
	// Likewise the chevron column: with one row opening a level, every row
	// leaves the room, so the group tags stay in line.
	const hasLevels = useMemo(() => rows.some((it) => it.next), [rows]);

	const { activeIndex: active, moveTo, moveActive } = useRowCursor(rows, query);

	// Clearing the query would drop the cursor on its own, but only if it had
	// changed; `moveTo(null)` covers reopening on an already-empty query.
	useOnOpen(open, () => {
		setQuery("");
		setPath([]);
		moveTo(null);
	});

	useEffect(() => {
		if (!open) return;
		const frame = requestAnimationFrame(() => inputRef.current?.focus());
		return () => cancelAnimationFrame(frame);
	}, [open]);

	const goTo = (next: readonly string[]) => {
		setPath(next);
		setQuery("");
		moveTo(null);
		inputRef.current?.focus();
	};

	const descend = (row: CommandItem) => {
		if (row.next) goTo([...levelPath, row.next.id]);
	};

	const choose = (row: CommandItem) => {
		if (row.onSelect) {
			row.onSelect();
			setOpen(false);
		} else descend(row);
	};

	const onKeyDown = (e: React.KeyboardEvent) => {
		if (e.nativeEvent.isComposing) return;
		const input = inputRef.current;
		if (e.key === "ArrowDown") {
			e.preventDefault();
			moveActive(1);
		} else if (e.key === "ArrowUp") {
			e.preventDefault();
			moveActive(-1);
		} else if (e.key === "Enter") {
			e.preventDefault();
			const it = rows[active];
			if (it) choose(it);
		} else if (e.key === "ArrowRight" && e.target === input) {
			// → opens the row's level, unless the person is still editing the
			// text: with the caret short of the end it moves the caret.
			const it = rows[active];
			const atEnd =
				input !== null &&
				input.selectionStart === input.value.length &&
				input.selectionEnd === input.value.length;
			if (it?.next && atEnd) {
				e.preventDefault();
				descend(it);
			}
		} else if (e.key === "Backspace" && nested && query === "") {
			e.preventDefault();
			goTo(levelPath.slice(0, -1));
		}
	};

	useEffect(() => {
		if (!open) return;
		const el = listRef.current?.querySelector<HTMLButtonElement>(
			`[data-index="${active}"]`,
		);
		el?.scrollIntoView({ block: "nearest" });
	}, [active, open]);

	if (!mounted) return null;

	const activeRow = rows[active];

	// Portaled to <body> so transforms and stacking contexts in the shell
	// cannot trap the viewport scrim. The palette opens and closes with no
	// motion at all: it is summoned dozens of times a day, mostly by key, and
	// a command surface that waits on an animation is a slower one.
	return createPortal(
		open ? (
			<>
				<button
					type="button"
					aria-label="Close command palette"
					onClick={() => setOpen(false)}
					className="fixed inset-0 z-[100] bg-scrim"
				/>
				<div className="pointer-events-none fixed inset-x-4 top-[13vh] z-[100] flex justify-center">
					<div
						role="dialog"
						aria-modal="true"
						aria-label="Command palette"
						onKeyDown={onKeyDown}
						className="surface-modal pointer-events-auto w-full max-w-[620px] origin-center overflow-hidden rounded-dialog text-ink-1"
					>
						<div className="border-line border-b p-2">
							<div className="surface-well flex h-11 items-center gap-2.5 rounded-ctl pr-2.5 pl-3">
								<span
									aria-hidden
									className="font-medium font-mono text-[15px] text-accent-ink leading-none"
								>
									›
								</span>
								{levels.map((crumb, index) => (
									<Fragment key={crumb.id}>
										<button
											type="button"
											tabIndex={-1}
											aria-label={`Back to ${crumb.title}`}
											onClick={() => goTo(levelPath.slice(0, index + 1))}
											className="max-w-[40%] shrink-0 cursor-pointer truncate rounded-xs border border-line-strong bg-white/6 px-1.5 py-1 font-medium font-mono text-2xs text-ink-2 leading-none outline-none"
										>
											{crumb.title}
										</button>
										<span
											aria-hidden
											className="font-medium font-mono text-[15px] text-ink-4 leading-none"
										>
											›
										</span>
									</Fragment>
								))}
								<input
									ref={inputRef}
									value={query}
									onChange={(e) => setQuery(e.target.value)}
									placeholder={level?.placeholder ?? placeholder}
									autoComplete="off"
									autoCorrect="off"
									spellCheck={false}
									role="combobox"
									aria-label={
										level ? `Search ${level.title} commands` : "Search commands"
									}
									// The field only exists while the palette is open.
									aria-expanded="true"
									aria-controls={`${uid}-list`}
									aria-activedescendant={
										rows.length > 0 ? `${uid}-opt-${active}` : undefined
									}
									aria-autocomplete="list"
									className={cn(
										// No focus ring: the palette is the focused surface and the well
										// around the field is its frame; a ring on the field inside it
										// read as a second box around the text.
										"h-full min-w-0 flex-1 text-ellipsis bg-transparent px-0.5 text-[15px] text-ink-1 leading-none outline-none placeholder:text-ink-4 focus-visible:shadow-none",
										// The palette focuses this field the moment it opens, and iOS
										// zooms the page in on a focused field under 16px: the fixed
										// overlay is magnified off-center — clipped leading edge, half
										// an icon column — and the zoom outlives the palette. 16px on
										// touch keeps the page at scale 1; pointer devices keep 14px.
										canTouch && "text-base",
									)}
								/>
								{canTouch ? null : <Kbd className="shrink-0">esc</Kbd>}
							</div>
						</div>
						<div
							ref={listRef}
							id={`${uid}-list`}
							role="listbox"
							aria-label={level ? level.title : "Commands"}
							className="relative max-h-[400px] overflow-y-auto overscroll-contain p-1.5"
						>
							{rows.length === 0 ? (
								<p className="break-words px-3 py-6 text-ink-3 text-sm leading-normal">
									{query ? (
										<>Nothing matches &ldquo;{query}&rdquo;.</>
									) : (
										emptyMessage
									)}
								</p>
							) : (
								rows.map((it, index) => {
									const isActive = index === active;
									const ItemIcon = it.icon;
									return (
										<div key={it.id} role="presentation" className="relative">
											<button
												type="button"
												id={`${uid}-opt-${index}`}
												role="option"
												aria-selected={isActive}
												aria-haspopup={it.next ? "listbox" : undefined}
												data-index={index}
												onClick={() => choose(it)}
												onMouseEnter={() => moveTo(it.id)}
												className={cn(
													"flex h-9 w-full cursor-pointer items-center gap-3 rounded-ctl px-3 text-left outline-none",
													isActive && "bg-white/7",
													hasLevels && "pr-10",
												)}
											>
												{ItemIcon ? (
													<ItemIcon
														className={cn(
															"size-4 shrink-0",
															isActive ? "text-ink-1" : "text-ink-3",
														)}
													/>
												) : hasIcons ? (
													<span className="size-4 shrink-0" />
												) : null}
												<span className="min-w-[40%] max-w-[70%] flex-[1_0_auto] truncate font-medium text-[14px] text-ink-1 leading-none">
													<EmphasisedLabel
														label={it.label}
														positions={
															query.trim()
																? labelMatchPositions(it.label, query)
																: []
														}
													/>
												</span>
												{it.hint ? (
													<span className="hidden min-w-0 shrink truncate whitespace-nowrap font-mono text-ink-3 text-xs leading-none sm:block">
														{it.hint}
													</span>
												) : null}
												{it.shortcut ? (
													<Kbd className="shrink-0">{it.shortcut}</Kbd>
												) : null}
												{it.badge ? (
													<span className="shrink-0">{it.badge}</span>
												) : null}
												{it.group ? (
													<span className="type-micro shrink-0 whitespace-nowrap text-right sm:min-w-[74px]">
														{it.group}
													</span>
												) : null}
											</button>
											{it.next ? (
												<button
													type="button"
													tabIndex={-1}
													aria-label={`Open ${it.next.title} commands`}
													onMouseEnter={() => moveTo(it.id)}
													onClick={() => descend(it)}
													className="absolute inset-y-0 right-1.5 z-10 my-auto flex size-7 cursor-pointer items-center justify-center rounded-md text-ink-4 outline-none transition-[color,background-color] duration-quick hover:bg-white/6 hover:text-ink-1"
												>
													<ChevronRightIcon aria-hidden className="size-3.5" />
												</button>
											) : null}
										</div>
									);
								})
							)}
						</div>
						{hiddenCount > 0 ? (
							<p className="border-line border-t px-4 py-2 text-ink-4 text-xs leading-none">
								{hiddenCount} more · keep typing to narrow
							</p>
						) : null}
						{canTouch ? null : (
							<div className="flex gap-4 border-line border-t px-4 py-2.5 text-2xs text-ink-3 leading-none">
								<Hint keys="↑↓">navigate</Hint>
								<Hint keys="↵">
									{activeRow && !activeRow.onSelect ? "or → to open" : "open"}
								</Hint>
								{activeRow?.onSelect && activeRow.next ? (
									<Hint keys="→">more</Hint>
								) : null}
								{nested ? <Hint keys="⌫">back</Hint> : null}
								<Hint keys={shortcut === "k" ? "⌘K" : shortcut}>toggle</Hint>
							</div>
						)}
					</div>
				</div>
			</>
		) : null,
		document.body,
	);
}
