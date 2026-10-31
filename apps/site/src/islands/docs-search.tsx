import {
	createIndex,
	type SearchIndex,
	searchDocs,
	unpackSearch,
} from "@nebulactrl/docs/search";
import type { SearchData, SearchHit } from "@nebulactrl/docs/types";
import { docHref } from "@nebulactrl/docs/types";
import { HashIcon } from "@phosphor-icons/react/dist/csr/Hash";
import { MagnifyingGlassIcon as SearchIcon } from "@phosphor-icons/react/dist/csr/MagnifyingGlass";
import { useEffect, useId, useRef, useState } from "react";

let loaded: Promise<SearchIndex> | null = null;

/** The index is its own file (/search.json), fetched the first time the search opens. */
function loadIndex(): Promise<SearchIndex> {
	loaded ??= fetch("/search.json")
		.then((response) => {
			if (!response.ok) throw new Error(`search index: ${response.status}`);
			return response.json() as Promise<SearchData>;
		})
		.then((data) => createIndex(unpackSearch(data)))
		.catch((error) => {
			loaded = null;
			throw error;
		});
	return loaded;
}

const isMac = () => /Mac|iPhone|iPad/.test(navigator.platform);

export default function DocsSearch() {
	const [open, setOpen] = useState(false);
	const [query, setQuery] = useState("");
	const [index, setIndex] = useState<SearchIndex | null>(null);
	const [error, setError] = useState(false);
	const [active, setActive] = useState(0);
	// The server cannot know the platform: it prints Ctrl K and a Mac swaps in the command key.
	const [mac, setMac] = useState(false);
	useEffect(() => setMac(isMac()), []);
	const opener = useRef<HTMLButtonElement>(null);
	const input = useRef<HTMLInputElement>(null);
	const listId = useId();

	// Cmd/Ctrl+K opens the search from anywhere in the docs.
	useEffect(() => {
		const onKey = (event: KeyboardEvent) => {
			if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
				event.preventDefault();
				setOpen(true);
			}
		};
		document.addEventListener("keydown", onKey);
		return () => document.removeEventListener("keydown", onKey);
	}, []);

	useEffect(() => {
		if (!open) return;
		setError(false);
		loadIndex().then(setIndex, () => setError(true));
		input.current?.focus();
		const previous = document.body.style.overflow;
		document.body.style.overflow = "hidden";
		return () => {
			document.body.style.overflow = previous;
		};
	}, [open]);

	const hits: SearchHit[] =
		index && query.trim() ? searchDocs(index, query, { limit: 8 }) : [];

	const close = () => {
		setOpen(false);
		setQuery("");
		setActive(0);
		opener.current?.focus();
	};
	const go = (hit: SearchHit) => {
		window.location.href = docHref(hit.slug, hit.anchor);
	};

	const onKeyDown = (event: React.KeyboardEvent) => {
		if (event.key === "Escape") {
			event.preventDefault();
			close();
		} else if (event.key === "ArrowDown") {
			event.preventDefault();
			setActive((a) => Math.min(a + 1, Math.max(hits.length - 1, 0)));
		} else if (event.key === "ArrowUp") {
			event.preventDefault();
			setActive((a) => Math.max(a - 1, 0));
		} else if (event.key === "Enter" && hits[active]) {
			event.preventDefault();
			go(hits[active] as SearchHit);
		}
	};

	return (
		<>
			<button
				ref={opener}
				type="button"
				onClick={() => setOpen(true)}
				aria-label="Search the docs"
				aria-haspopup="dialog"
				className="btn-secondary pressable flex h-10 w-full max-w-md cursor-pointer items-center gap-2 rounded-lg px-3 text-left text-ink-3 text-sm"
			>
				<SearchIcon aria-hidden className="size-4 shrink-0" />
				<span className="min-w-0 flex-1 truncate">Search the docs</span>
				<kbd className="keycap hidden sm:pointer-fine:inline-flex">
					{mac ? "⌘ K" : "Ctrl K"}
				</kbd>
			</button>
			{open ? (
				<div className="fixed inset-0 z-50 flex items-start justify-center px-4 pt-[12vh]">
					<button
						type="button"
						tabIndex={-1}
						aria-label="Close search"
						onClick={close}
						className="absolute inset-0 cursor-default bg-black/60"
					/>
					<div
						role="dialog"
						aria-modal="true"
						aria-label="Search the docs"
						onKeyDown={onKeyDown}
						className="surface-float relative w-full max-w-xl overflow-hidden rounded-2xl"
					>
						<div className="flex items-center gap-2 border-line border-b px-4">
							<SearchIcon aria-hidden className="size-4 shrink-0 text-ink-3" />
							<input
								ref={input}
								type="text"
								role="combobox"
								aria-expanded={hits.length > 0}
								aria-controls={listId}
								aria-activedescendant={
									hits[active] ? `${listId}-${active}` : undefined
								}
								aria-label="Search the docs"
								autoComplete="off"
								spellCheck={false}
								placeholder="Search titles, headings and text"
								value={query}
								onChange={(event) => {
									setQuery(event.target.value);
									setActive(0);
								}}
								className="h-14 min-w-0 flex-1 bg-transparent text-base text-ink-1 outline-none placeholder:text-ink-4"
							/>
							<kbd className="keycap hidden pointer-fine:inline-flex">Esc</kbd>
						</div>
						<div
							id={listId}
							role="listbox"
							aria-label="Results"
							className="max-h-[50vh] overflow-y-auto overscroll-contain p-2"
						>
							{hits.map((hit, i) => (
								<a
									key={`${hit.slug}#${hit.anchor}`}
									id={`${listId}-${i}`}
									role="option"
									aria-selected={i === active}
									href={docHref(hit.slug, hit.anchor)}
									onMouseMove={() => setActive(i)}
									className={`flex flex-col gap-0.5 rounded-lg px-3 py-2 no-underline ${i === active ? "bg-surface-well" : ""}`}
								>
									<span className="flex items-center gap-2 text-ink-1 text-sm">
										{hit.heading ? (
											<HashIcon
												aria-hidden
												className="size-3 shrink-0 text-ink-4"
											/>
										) : null}
										{hit.heading
											? `${hit.pageTitle}: ${hit.heading}`
											: hit.pageTitle}
									</span>
									<span className="text-ink-3 text-xs">{hit.section}</span>
								</a>
							))}
						</div>
						<p
							className="m-0 border-line border-t px-4 py-3 text-ink-3 text-xs"
							role="status"
						>
							{error
								? "The search index did not load. Check your connection and open the search again."
								: !index
									? "Loading the search index"
									: query.trim() === ""
										? "Type to search every docs page."
										: hits.length === 0
											? `No page matches "${query.trim()}". Try fewer or different words.`
											: `${hits.length} ${hits.length === 1 ? "result" : "results"}`}
						</p>
					</div>
				</div>
			) : null}
		</>
	);
}
