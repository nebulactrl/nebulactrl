import { SearchIcon, XIcon } from "lucide-react";
import { useEffect, useRef } from "react";
import { Kbd } from "#/components/ui/kbd";
import { matchSummary } from "./lib/filter";

/**
 * ⌘F's field. Typing dims the cards that do not match; Enter fits the view to
 * the ones that do; Esc clears and closes. It floats at the top centre of the
 * part of the canvas the inspector leaves free; when that part is too narrow
 * to hold it between the toolbar and Create (`inline`), it is a row of the
 * toolbar column instead. Its count is the only one on screen: the filter
 * chips leave theirs out while a search is typed.
 */
export function CanvasSearch({
	query,
	onQueryChange,
	matchCount,
	total,
	inline,
	rightInset,
	onSubmit,
	onClose,
}: {
	query: string;
	onQueryChange(query: string): void;
	matchCount: number;
	total: number;
	/** In the chrome column, not floating at the top centre. */
	inline: boolean;
	/** The inspector's width: the floating field centres in what is left. */
	rightInset: number;
	onSubmit(): void;
	onClose(): void;
}) {
	const inputRef = useRef<HTMLInputElement>(null);
	useEffect(() => {
		inputRef.current?.focus();
		inputRef.current?.select();
	}, []);
	const active = query.trim() !== "";
	const field = (
		<search
			data-canvas-chrome
			className="surface-float pointer-events-auto flex h-9 w-[min(420px,100%)] items-center gap-2 rounded-ctl pr-1.5 pl-3"
		>
			<SearchIcon
				aria-hidden
				className="size-4 shrink-0 text-muted-foreground"
			/>
			<input
				ref={inputRef}
				type="search"
				value={query}
				aria-label="Search services"
				placeholder="Search services, domains, images"
				onChange={(event) => onQueryChange(event.target.value)}
				onKeyDown={(event) => {
					event.stopPropagation();
					if (event.key === "Enter") {
						event.preventDefault();
						onSubmit();
					}
					if (event.key === "Escape") {
						event.preventDefault();
						onClose();
					}
				}}
				className="min-w-0 flex-1 bg-transparent text-[13px] text-foreground outline-none placeholder:text-subtle [&::-webkit-search-cancel-button]:hidden"
			/>
			{active ? (
				<span
					aria-live="polite"
					className="shrink-0 font-mono text-[11px] text-muted-foreground"
				>
					{matchSummary(matchCount, total)}
				</span>
			) : (
				<Kbd>esc</Kbd>
			)}
			<button
				type="button"
				aria-label="Close search"
				onClick={onClose}
				className="pressable grid size-6 shrink-0 cursor-pointer place-items-center rounded-sm text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:focus-ring pointer-coarse:size-9"
			>
				<XIcon aria-hidden className="size-3.5" />
			</button>
		</search>
	);
	if (inline) return field;
	return (
		<div
			className="pointer-events-none absolute top-3 left-3 z-10 flex justify-center"
			style={{ right: rightInset + 12 }}
		>
			{field}
		</div>
	);
}
