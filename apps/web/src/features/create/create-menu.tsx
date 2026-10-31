import { useQuery } from "@tanstack/react-query";
import { cn } from "cn";
import {
	ArrowLeftIcon,
	ChevronRightIcon,
	PlusIcon,
	SearchIcon,
} from "lucide-react";
import { type KeyboardEvent, useEffect, useId, useMemo, useState } from "react";
import type { EngineView } from "#/api/gen/types.gen";
import { useProjectContext } from "#/components/shell/use-project-context";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Kbd } from "#/components/ui/kbd";
import {
	Popover,
	PopoverContent,
	PopoverTrigger,
} from "#/components/ui/popover";
import { enginesQuery } from "#/features/databases/queries";
import { EntryMark, openCreateEntry } from "./entry";
import { type MenuEntry, type MenuEntryId, visibleEntries } from "./lib";
import { createActions, useCreateState } from "./store";

const NO_ENGINES: EngineView[] = [];

/**
 * The command-style list inside the Create popover: a filter field on top,
 * the entries under it, all driven from the field so typing never leaves it
 * (arrows move, Enter picks, → opens Database, ← or Backspace on an empty
 * field goes back). A query searches every entry at once, so "post" reaches
 * PostgreSQL without opening Database first.
 */
function CreateList() {
	const listId = useId();
	const [query, setQuery] = useState("");
	const [submenu, setSubmenu] = useState<"database" | null>(null);
	const [active, setActive] = useState(0);
	const catalog = useQuery(enginesQuery());
	const loadingEngines =
		catalog.isPending && (submenu !== null || query.trim() !== "");
	const entries = useMemo(
		() => visibleEntries(query, submenu, catalog.data?.engines ?? NO_ENGINES),
		[query, submenu, catalog.data],
	);
	const activeIndex = Math.min(active, Math.max(entries.length - 1, 0));
	const searching = query.trim() !== "";

	const { menu } = useCreateState();
	const pick = (entry: MenuEntry) => {
		if (entry.id === "database") {
			setSubmenu("database");
			setQuery("");
			setActive(0);
			return;
		}
		if (entry.engine) {
			createActions.choose("database", entry.engine);
			return;
		}
		openCreateEntry(entry.id as MenuEntryId, menu?.options ?? {});
	};

	const back = () => {
		setSubmenu(null);
		setQuery("");
		setActive(0);
	};

	const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
		const count = entries.length;
		switch (event.key) {
			case "ArrowDown":
				event.preventDefault();
				if (count > 0) setActive((activeIndex + 1) % count);
				break;
			case "ArrowUp":
				event.preventDefault();
				if (count > 0) setActive((activeIndex - 1 + count) % count);
				break;
			case "Enter": {
				event.preventDefault();
				const entry = entries[activeIndex];
				if (entry) pick(entry);
				break;
			}
			case "ArrowRight": {
				const entry = entries[activeIndex];
				if (entry?.id === "database" && query === "") {
					event.preventDefault();
					pick(entry);
				}
				break;
			}
			case "ArrowLeft":
				if (submenu && query === "") {
					event.preventDefault();
					back();
				}
				break;
			case "Backspace":
				if (submenu && query === "") {
					event.preventDefault();
					back();
				}
				break;
		}
	};

	return (
		<div className="flex flex-col">
			<div className="flex items-center gap-2 px-2.5 pt-1.5 pb-1">
				{submenu && !searching ? (
					<button
						type="button"
						aria-label="Back to all sources"
						onClick={back}
						className="pressable grid size-5 shrink-0 cursor-pointer place-items-center rounded-sm text-ink-3 outline-none hover:bg-white/8 hover:text-ink-1 focus-visible:focus-ring"
					>
						<ArrowLeftIcon aria-hidden className="size-3.5" />
					</button>
				) : (
					<SearchIcon aria-hidden className="size-3.5 shrink-0 text-ink-3" />
				)}
				<input
					type="text"
					role="combobox"
					aria-expanded
					aria-controls={listId}
					aria-activedescendant={
						entries[activeIndex]
							? `${listId}-${entries[activeIndex].id}`
							: undefined
					}
					aria-autocomplete="list"
					aria-label={
						submenu && !searching ? "Filter databases" : "Filter sources"
					}
					autoComplete="off"
					spellCheck={false}
					placeholder={
						submenu && !searching ? "Database…" : "What are you creating?"
					}
					value={query}
					onChange={(event) => {
						setQuery(event.target.value);
						setActive(0);
					}}
					onKeyDown={onKeyDown}
					className="h-7 min-w-0 flex-1 bg-transparent text-[13px] text-ink-1 outline-none placeholder:text-ink-4"
				/>
			</div>
			<div className="mx-1 my-1 h-px bg-line-faint" aria-hidden />
			<div
				id={listId}
				role="listbox"
				aria-label="Create a service"
				className="flex flex-col gap-px"
			>
				{entries.map((entry, index) => {
					const isActive = index === activeIndex;
					return (
						// biome-ignore lint/a11y/useFocusableInteractive: options are never focused; the filter input drives them through aria-activedescendant.
						// biome-ignore lint/a11y/useKeyWithClickEvents: the filter input handles arrows and Enter for every option; the click is the pointer path to the same pick.
						<div
							key={entry.id}
							id={`${listId}-${entry.id}`}
							role="option"
							aria-selected={isActive}
							onPointerMove={() => setActive(index)}
							onClick={() => pick(entry)}
							className={cn(
								"flex min-h-11 cursor-pointer select-none items-center gap-2.5 rounded-md px-2 py-1.5 text-ink-2",
								isActive && "bg-white/7 text-ink-1",
							)}
						>
							<EntryMark
								id={entry.engine ? "database" : (entry.id as MenuEntryId)}
								engine={entry.engine}
								size={22}
							/>
							{/* Label above, its hint below in the quiet sans: the hint
							    never squeezes beside the label, so nothing truncates
							    at a different spot on every row. */}
							<span className="flex min-w-0 flex-1 flex-col gap-0.5">
								<span className="truncate text-[13px] leading-tight">
									{entry.label}
								</span>
								{entry.hint ? (
									<span
										className="truncate text-2xs text-ink-4 leading-snug"
										title={entry.hint}
									>
										{entry.hint}
									</span>
								) : null}
							</span>
							{entry.chip ? (
								<Badge variant="warning" className="shrink-0">
									{entry.chip}
								</Badge>
							) : null}
							{entry.id === "database" ? (
								<ChevronRightIcon
									aria-hidden
									className="size-3.5 shrink-0 text-ink-3"
								/>
							) : null}
						</div>
					);
				})}
				{entries.length === 0 ? (
					<div role="presentation" className="px-2 py-3 text-[13px] text-ink-3">
						{loadingEngines
							? "Loading engines…"
							: catalog.isError && submenu
								? "The engine list did not load."
								: `Nothing matches “${query.trim()}”.`}
					</div>
				) : null}
			</div>
			<div className="mx-1 mt-1 flex items-center gap-3 border-line-faint border-t px-1.5 pt-1.5 pb-0.5 text-2xs text-ink-4">
				<span className="flex items-center gap-1">
					<Kbd>↑↓</Kbd> move
				</span>
				<span className="flex items-center gap-1">
					<Kbd>↵</Kbd> choose
				</span>
				<span className="flex items-center gap-1">
					<Kbd>esc</Kbd> close
				</span>
			</div>
		</div>
	);
}

/**
 * The raised "+ Create" key. It opens a command-style menu of what a service
 * can start from: a Git repository, a Docker image or a database. The same
 * menu opens from the palette or a canvas context menu via
 * `useCreateService().open()`.
 */
export function CreateButton({
	className,
	variant = "secondary",
	size = "md",
}: {
	className?: string;
	/** `primary` for the empty canvas, where Create is the one action. */
	variant?: "primary" | "secondary";
	size?: "md" | "lg";
}) {
	const environment = useProjectContext()?.environment;
	const { menu } = useCreateState();
	useEffect(() => createActions.registerButton(), []);

	return (
		<Popover open={menu !== null} onOpenChange={createActions.setMenuOpen}>
			<PopoverTrigger
				render={
					<Button
						variant={variant}
						size={size}
						disabled={environment === undefined}
						className={className}
					/>
				}
			>
				<PlusIcon aria-hidden />
				Create
			</PopoverTrigger>
			<PopoverContent
				align="end"
				sideOffset={8}
				className="w-[420px] max-w-[calc(100vw-24px)] gap-0 rounded-menu p-1"
			>
				<CreateList />
			</PopoverContent>
		</Popover>
	);
}
