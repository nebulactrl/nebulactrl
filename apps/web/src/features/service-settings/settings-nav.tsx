import { cn } from "cn";
import { CircleAlertIcon, PencilIcon, SearchIcon, XIcon } from "lucide-react";
import { useReducedMotion } from "motion/react";
import {
	type KeyboardEvent,
	type ReactNode,
	type RefObject,
	useEffect,
	useId,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Input } from "#/components/ui/input";
import { count as counted } from "#/lib/format";
import { variablesHint } from "./settings-chrome";
import {
	type Category,
	type CategoryId,
	type SearchResult,
	type SettingsSubject,
	searchSettings,
} from "./settings-nav-model";
import { type StripFade, stripFade, stripMask } from "./strip-fade";

/**
 * The panel width from which categories sit in a rail on the left; narrower
 * than this they are a scrollable strip under the tab bar. Settings opens
 * wide, so the rail is the usual case.
 */
const RAIL_MIN_WIDTH = 720;

const RAIL_WIDTH = 184;

type Layout = "rail" | "strip";
/** What the last input to the rail was: only a pointer click slides the well. */
type Modality = "pointer" | "keyboard";

/** The width of an element, kept current with a ResizeObserver. */
function useElementWidth(ref: RefObject<HTMLElement | null>): number {
	const [width, setWidth] = useState(0);
	// Layout effect: the first paint already has the right layout.
	useLayoutEffect(() => {
		const element = ref.current;
		if (!element) return;
		setWidth(element.clientWidth);
		const observer = new ResizeObserver(([entry]) => {
			if (entry) setWidth(entry.contentRect.width);
		});
		observer.observe(element);
		return () => observer.disconnect();
	}, [ref]);
	return width;
}

const FOCUSABLE =
	'input:not([disabled]),select:not([disabled]),textarea:not([disabled]),button:not([disabled]),[role="switch"],[role="radio"],[role="combobox"],a[href]';

/**
 * Scrolls to a field (or a block with a `data-anchor`) inside `root` and puts
 * focus on it: the control itself, the first control inside a block, or the
 * block. Returns false when the page does not have it right now, e.g. a
 * field that only shows for another build method.
 */
function focusSettingsTarget(root: HTMLElement, target: string): boolean {
	const element =
		document.getElementById(target) ??
		root.querySelector<HTMLElement>(`[data-anchor="${CSS.escape(target)}"]`);
	if (!element || !root.contains(element)) return false;
	// A target inside a closed "More options" is not drawn yet: open the groups
	// around it before measuring, scrolling or focusing.
	for (
		let group = element.closest("details");
		group;
		group = group.parentElement?.closest("details") ?? null
	) {
		group.open = true;
	}
	// Keep the field clear of the sticky strip above and the save bar below.
	element.style.scrollMarginBlock = "80px";
	const tall = element.getBoundingClientRect().height > window.innerHeight / 2;
	element.scrollIntoView({ block: tall ? "start" : "center" });
	const control = element.matches(FOCUSABLE)
		? element
		: element.querySelector<HTMLElement>(FOCUSABLE);
	const focusable = control ?? element;
	if (control === null) focusable.tabIndex = -1;
	focusable.focus({ preventScroll: true });
	return true;
}

const ROW =
	"relative z-10 flex h-8 shrink-0 cursor-pointer items-center gap-2 rounded-ctl px-2.5 text-left font-medium text-[13px] text-ink-3 leading-none outline-none hover:text-ink-1 focus-visible:focus-ring aria-selected:text-ink-1";

/** Unsaved changes in the category: an icon and the word, never colour alone. */
function EditedMarker() {
	return (
		<span className="inline-flex items-center gap-1 font-normal text-[11px] text-accent-ink">
			<PencilIcon aria-hidden className="size-3" />
			edited
		</span>
	);
}

/** A field in the category fails validation, so Save is off until it is fixed. */
function InvalidMarker() {
	return (
		<span className="inline-flex items-center gap-1 font-normal text-[11px] text-status-failed-on">
			<CircleAlertIcon aria-hidden className="size-3" />
			needs fix
		</span>
	);
}

interface CategoryTabsProps {
	layout: Layout;
	categories: readonly Category[];
	active: CategoryId;
	edited: ReadonlySet<CategoryId>;
	invalid: ReadonlySet<CategoryId>;
	staged: Readonly<Record<CategoryId, number>>;
	idBase: string;
	input: RefObject<Modality>;
	onSelect(id: CategoryId): void;
}

/**
 * The categories as a vertical tab list (rail) or a horizontal one (strip).
 * One tab stop: arrows, Home and End move focus, Enter or Space selects.
 * The selected row's well slides to its new place over 150 ms after a
 * pointer click; a keyboard switch and reduced motion jump.
 */
function CategoryTabs({
	layout,
	categories,
	active,
	edited,
	invalid,
	staged,
	idBase,
	input,
	onSelect,
}: CategoryTabsProps) {
	const reduced = useReducedMotion() ?? false;
	const listRef = useRef<HTMLDivElement>(null);
	const barRef = useRef<HTMLSpanElement>(null);
	const placedFor = useRef<CategoryId | null>(null);
	const [fade, setFade] = useState<StripFade>({ start: false, end: false });
	const measureFade = () => {
		const list = listRef.current;
		if (!list) return;
		const next = stripFade(list.scrollLeft, list.clientWidth, list.scrollWidth);
		setFade((current) =>
			current.start === next.start && current.end === next.end ? current : next,
		);
	};
	const [roving, setRoving] = useState(active);
	useEffect(() => setRoving(active), [active]);
	const tabStop = categories.some((c) => c.id === roving) ? roving : active;

	// Markers change a row's width in the strip, so measure after them too.
	// biome-ignore lint/correctness/useExhaustiveDependencies: edited, invalid and staged only change the geometry.
	useLayoutEffect(() => {
		const list = listRef.current;
		const bar = barRef.current;
		const row = list?.querySelector<HTMLElement>('[aria-selected="true"]');
		if (!list || !bar || !row) return;
		const moved = placedFor.current !== null && placedFor.current !== active;
		const slide = moved && input.current === "pointer" && !reduced;
		// The rail is switched all day (a pointer click only, 150ms),
		// so its well slides on the quick clock, not the tabs-sliding 250ms.
		bar.style.transition = slide
			? "transform var(--duration-quick) var(--ease-smooth-out), width var(--duration-quick) var(--ease-smooth-out), height var(--duration-quick) var(--ease-smooth-out)"
			: "none";
		bar.style.width = `${row.offsetWidth}px`;
		bar.style.height = `${row.offsetHeight}px`;
		bar.style.transform = `translate(${row.offsetLeft}px, ${row.offsetTop}px)`;
		bar.style.opacity = "1";
		placedFor.current = active;
		// A strip scrolls sideways: keep the selected category in sight.
		if (layout === "strip") {
			const left = row.offsetLeft - 8;
			const right = row.offsetLeft + row.offsetWidth + 8;
			if (left < list.scrollLeft) list.scrollLeft = left;
			else if (right > list.scrollLeft + list.clientWidth) {
				list.scrollLeft = right - list.clientWidth;
			}
		}
		measureFade();
	}, [active, layout, categories, reduced, input, edited, invalid, staged]);

	// The fade follows the strip's width too, not only its scroll position.
	// biome-ignore lint/correctness/useExhaustiveDependencies: measureFade only reads the list.
	useEffect(() => {
		const list = listRef.current;
		if (!list || layout !== "strip") return;
		const observer = new ResizeObserver(measureFade);
		observer.observe(list);
		return () => observer.disconnect();
	}, [layout]);

	const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
		input.current = "keyboard";
		const rows = Array.from(
			event.currentTarget.querySelectorAll<HTMLElement>('[role="tab"]'),
		);
		const at = rows.indexOf(document.activeElement as HTMLElement);
		const sideways = layout === "strip";
		let next = -1;
		if (event.key === "ArrowDown" || (sideways && event.key === "ArrowRight")) {
			next = (at + 1) % rows.length;
		} else if (
			event.key === "ArrowUp" ||
			(sideways && event.key === "ArrowLeft")
		) {
			next = (at - 1 + rows.length) % rows.length;
		} else if (event.key === "Home") next = 0;
		else if (event.key === "End") next = rows.length - 1;
		if (next < 0) return;
		event.preventDefault();
		rows[next]?.focus();
	};

	return (
		<div
			ref={listRef}
			role="tablist"
			aria-label="Settings categories"
			aria-orientation={layout === "rail" ? "vertical" : "horizontal"}
			onKeyDown={onKeyDown}
			onPointerDown={() => {
				input.current = "pointer";
			}}
			style={layout === "strip" ? { maskImage: stripMask(fade) } : undefined}
			onScroll={layout === "strip" ? measureFade : undefined}
			className={cn(
				"relative flex gap-1",
				layout === "rail"
					? "flex-col"
					: "no-scrollbar -mx-1 snap-x snap-proximity overflow-x-auto scroll-px-3 px-1 py-0.5",
			)}
		>
			<span
				ref={barRef}
				aria-hidden
				className="pointer-events-none absolute top-0 left-0 z-0 rounded-ctl border border-line bg-surface-well opacity-0 shadow-well"
			/>
			{categories.map((category) => {
				const count = staged[category.id];
				return (
					<button
						key={category.id}
						type="button"
						role="tab"
						id={`${idBase}-tab-${category.id}`}
						aria-selected={category.id === active}
						aria-controls={`${idBase}-panel`}
						tabIndex={category.id === tabStop ? 0 : -1}
						onFocus={() => setRoving(category.id)}
						onClick={() => onSelect(category.id)}
						className={cn(ROW, layout === "strip" && "snap-start")}
					>
						<span
							className={cn(
								"min-w-0 flex-1 truncate",
								category.id === "danger" && "text-status-failed-on",
							)}
						>
							{category.label}
						</span>
						{invalid.has(category.id) ? (
							<InvalidMarker />
						) : edited.has(category.id) ? (
							<EditedMarker />
						) : null}
						{count > 0 ? (
							<Badge
								variant="accent"
								shape="tab-count"
								aria-label={counted(count, "staged change")}
								title={counted(count, "staged change")}
							>
								{count}
							</Badge>
						) : null}
					</button>
				);
			})}
		</div>
	);
}

interface SearchProps {
	subject: SettingsSubject;
	idBase: string;
	onPick(result: SearchResult, via: Modality): void;
	/** Shown under the field while it is empty: the category list. */
	children: ReactNode;
}

/**
 * Finds a field by its label or what people call it. ↑/↓ choose a result,
 * Enter jumps to it (the first when none is chosen), Esc clears.
 */
function SettingsSearch({
	subject,
	idBase,
	onPick,
	layout,
	children,
}: SearchProps & { layout: Layout }) {
	const [query, setQuery] = useState("");
	const [chosen, setChosen] = useState(0);
	const results = useMemo(
		() => searchSettings(query, subject),
		[query, subject],
	);
	const searching = query.trim() !== "";
	const hint = searching ? variablesHint(query) : null;
	const listId = `${idBase}-results`;
	const optionId = (index: number) => `${idBase}-result-${index}`;
	const current = Math.min(chosen, Math.max(results.length - 1, 0));

	const pick = (result: SearchResult, via: Modality) => {
		setQuery("");
		setChosen(0);
		onPick(result, via);
	};

	const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
		if (event.key === "ArrowDown" && results.length > 0) {
			event.preventDefault();
			setChosen((current + 1) % results.length);
		} else if (event.key === "ArrowUp" && results.length > 0) {
			event.preventDefault();
			setChosen((current - 1 + results.length) % results.length);
		} else if (event.key === "Enter") {
			const result = results[current];
			if (result) {
				event.preventDefault();
				pick(result, "keyboard");
			}
		} else if (event.key === "Escape") {
			// Clear first; with nothing to clear, let go of the field so the
			// next Esc reaches the inspector.
			event.preventDefault();
			event.stopPropagation();
			if (searching) setQuery("");
			else event.currentTarget.blur();
		}
	};

	return (
		<>
			<div className="relative">
				<SearchIcon
					aria-hidden
					className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-ink-3"
				/>
				<Input
					role="combobox"
					aria-label="Search settings"
					aria-autocomplete="list"
					aria-expanded={searching}
					aria-controls={listId}
					{...(searching && results.length > 0
						? { "aria-activedescendant": optionId(current) }
						: {})}
					autoComplete="off"
					spellCheck={false}
					placeholder="Search settings"
					className={cn("pl-8", searching && "pr-8")}
					value={query}
					onChange={(event) => {
						setQuery(event.target.value);
						setChosen(0);
					}}
					onKeyDown={onKeyDown}
				/>
				{searching ? (
					<Button
						variant="ghost"
						size="icon-xs"
						aria-label="Clear search"
						className="absolute top-1/2 right-1 -translate-y-1/2"
						onClick={() => setQuery("")}
					>
						<XIcon aria-hidden />
					</Button>
				) : null}
			</div>
			{searching ? (
				results.length === 0 ? (
					<p role="status" className="type-secondary px-2.5 py-1">
						No settings match “{query.trim()}”.
					</p>
				) : (
					<div
						id={listId}
						role="listbox"
						aria-label="Matching settings"
						className={cn(
							"flex flex-col gap-0.5 overflow-y-auto overscroll-contain",
							layout === "strip" && "max-h-72",
						)}
					>
						{results.map((result, index) => (
							<button
								key={`${result.entry.section}:${result.entry.label}`}
								type="button"
								role="option"
								id={optionId(index)}
								aria-selected={index === current}
								tabIndex={-1}
								// Keep focus in the field so Enter and the arrows still work.
								onMouseDown={(event) => event.preventDefault()}
								onClick={() => pick(result, "pointer")}
								className={cn(
									"flex min-w-0 cursor-pointer flex-col gap-0.5 rounded-ctl border border-transparent px-2.5 py-1.5 text-left outline-none transition-colors duration-quick ease-out hover:bg-surface-well/60",
									index === current &&
										"border-line bg-surface-well shadow-well",
								)}
							>
								<span className="truncate font-medium text-[13px] text-ink-1 leading-none">
									{result.entry.label}
								</span>
								<span className="truncate text-[11px] text-ink-3 leading-none">
									{result.categoryLabel}
									{result.sectionLabel === result.categoryLabel
										? ""
										: ` · ${result.sectionLabel}`}
								</span>
							</button>
						))}
					</div>
				)
			) : (
				children
			)}
			{hint ? <p className="type-secondary px-2.5 py-1">{hint}</p> : null}
		</>
	);
}

export interface SettingsPagesProps {
	/** Lands on the page root as `data-service-id`. */
	serviceId: string;
	subject: SettingsSubject;
	categories: readonly Category[];
	active: CategoryId;
	onSelect(id: CategoryId): void;
	/** Categories holding unsaved changes in the working copy. */
	edited: ReadonlySet<CategoryId>;
	/** Categories holding a field that fails validation. */
	invalid: ReadonlySet<CategoryId>;
	/** Staged (saved, not yet deployed) changes per category. */
	staged: Readonly<Record<CategoryId, number>>;
	/** Shown above the active category whichever one it is. */
	banner?: ReactNode;
	/** The save bar, for a category that has something to save; null for one that acts at once. */
	footer: ReactNode;
	/** The sections of the active category. */
	children: ReactNode;
}

/**
 * The Settings tab's frame: categories on the left (a rail from 720 px of
 * panel width, a strip below it), the active category's page, and the shared
 * save bar pinned underneath. Only one category is ever in the page.
 */
export function SettingsPages({
	serviceId,
	subject,
	categories,
	active,
	onSelect,
	edited,
	invalid,
	staged,
	banner,
	footer,
	children,
}: SettingsPagesProps) {
	const rootRef = useRef<HTMLDivElement>(null);
	const layout: Layout =
		useElementWidth(rootRef) >= RAIL_MIN_WIDTH ? "rail" : "strip";
	const idBase = useId();
	const input = useRef<Modality>("pointer");
	const [jump, setJump] = useState<{
		category: CategoryId;
		target: string;
	} | null>(null);

	// A search result: switch category, then focus once that page is drawn.
	useEffect(() => {
		if (jump === null || jump.category !== active) return;
		if (rootRef.current) focusSettingsTarget(rootRef.current, jump.target);
		setJump(null);
	}, [jump, active]);

	const current = categories.find((category) => category.id === active);
	const nav = (
		<div
			className={cn(
				"flex min-w-0 flex-col",
				layout === "rail" ? "w-full gap-3" : "gap-1.5",
			)}
		>
			<SettingsSearch
				layout={layout}
				subject={subject}
				idBase={idBase}
				onPick={(result, via) => {
					input.current = via;
					setJump({ category: result.category, target: result.entry.target });
					onSelect(result.category);
				}}
			>
				<CategoryTabs
					layout={layout}
					categories={categories}
					active={active}
					edited={edited}
					invalid={invalid}
					staged={staged}
					idBase={idBase}
					input={input}
					onSelect={onSelect}
				/>
			</SettingsSearch>
		</div>
	);

	return (
		<div
			ref={rootRef}
			data-tab="settings"
			data-service-id={serviceId}
			data-layout={layout}
			className="flex min-h-full flex-col"
		>
			{layout === "strip" ? (
				<nav
					aria-label="Settings"
					className="surface-float sticky top-0 z-20 rounded-none border-line border-x-0 border-t-0 px-4 pt-2 pb-1"
				>
					{nav}
				</nav>
			) : null}
			<div
				className={cn(
					"flex flex-1 p-4",
					layout === "rail" ? "gap-8" : "flex-col gap-4",
				)}
			>
				{layout === "rail" ? (
					<nav
						aria-label="Settings"
						style={{ width: RAIL_WIDTH }}
						className="sticky top-4 shrink-0 self-start"
					>
						{nav}
					</nav>
				) : null}
				<div
					role="tabpanel"
					id={`${idBase}-panel`}
					aria-labelledby={`${idBase}-tab-${active}`}
					className="flex min-w-0 flex-1 flex-col gap-4"
				>
					{banner}
					{current ? (
						<header className="flex flex-col gap-1">
							<h2 className="type-panel-title text-ink-1">{current.label}</h2>
							<p className="type-secondary">{current.description}</p>
						</header>
					) : null}
					{children}
				</div>
			</div>
			{footer}
		</div>
	);
}
