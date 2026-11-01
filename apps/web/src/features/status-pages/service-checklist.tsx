import { useMemo, useState } from "react";
import type { StatusPageView } from "#/api/gen/types.gen";
import { CheckLabel } from "#/components/ui/check-label";
import { Input } from "#/components/ui/input";
import { filterServices, type ServiceOption, sectionServices } from "./model";

/** A list this long gets a search box. */
const SEARCH_FROM = 6;

/**
 * Services of every project and environment, searchable, under "Project ·
 * environment" headings, each with a checkbox. Services in `exclude` are not
 * offered (they are already chosen elsewhere).
 */
export function ServiceChecklist<T extends ServiceOption>({
	services,
	pending,
	selected,
	onToggle,
	label,
	exclude,
	emptyText = "No services yet. Create one on a project's canvas first.",
	maxHeight = "max-h-64",
}: {
	services: readonly T[];
	pending: boolean;
	selected: readonly string[];
	onToggle: (service: T, on: boolean) => void;
	/** Names the list for assistive tech: "Services to add". */
	label: string;
	exclude?: ReadonlySet<string>;
	emptyText?: string;
	maxHeight?: string;
}) {
	const [query, setQuery] = useState("");
	const offered = useMemo(
		() => (exclude ? services.filter((s) => !exclude.has(s.id)) : services),
		[services, exclude],
	);
	const sections = useMemo(
		() => sectionServices(filterServices(offered, query)),
		[offered, query],
	);
	return (
		<div className="flex flex-col gap-2">
			{offered.length >= SEARCH_FROM || query !== "" ? (
				<Input
					type="search"
					aria-label={`Search ${label.toLowerCase()}`}
					autoComplete="off"
					placeholder="Search by service, project or environment"
					value={query}
					onChange={(event) => setQuery(event.target.value)}
				/>
			) : null}
			<fieldset
				aria-label={label}
				className={`surface-well m-0 flex min-w-0 flex-col gap-3 overflow-y-auto overscroll-contain rounded-card p-3 ${maxHeight}`}
			>
				{sections.length === 0 ? (
					<p className="m-0 text-ink-3 text-xs">
						{pending
							? "Loading services…"
							: query.trim() !== ""
								? "No service matches the search."
								: offered.length === 0 && services.length > 0
									? "Every service is already on the page."
									: emptyText}
					</p>
				) : (
					sections.map((section) => (
						<fieldset
							key={section.key}
							className="m-0 flex min-w-0 flex-col gap-2 border-0 p-0"
						>
							<legend className="m-0 mb-2 p-0 type-micro">
								{section.label}
							</legend>
							{section.services.map((service) => (
								<CheckLabel
									key={service.id}
									className="gap-2.5 text-sm"
									checked={selected.includes(service.id)}
									onCheckedChange={(on) => onToggle(service, on)}
								>
									{service.name}
								</CheckLabel>
							))}
						</fieldset>
					))
				)}
			</fieldset>
		</div>
	);
}

/** The team's status pages with a checkbox each: where an incident or a window is posted. */
export function PageChecklist({
	pages,
	pending,
	selected,
	onToggle,
	label,
}: {
	pages: readonly StatusPageView[];
	pending: boolean;
	selected: readonly string[];
	onToggle: (pageId: string, on: boolean) => void;
	label: string;
}) {
	return (
		<fieldset
			aria-label={label}
			className="surface-well m-0 flex max-h-40 min-w-0 flex-col gap-2 overflow-y-auto overscroll-contain rounded-card p-3"
		>
			{pages.length === 0 ? (
				<p className="m-0 text-ink-3 text-xs">
					{pending ? "Loading pages…" : "No status pages yet."}
				</p>
			) : (
				pages.map((page) => (
					<CheckLabel
						key={page.id}
						className="gap-2.5 text-sm"
						checked={selected.includes(page.id)}
						onCheckedChange={(on) => onToggle(page.id, on)}
					>
						{page.title}
						<span className="ml-2 font-mono text-2xs text-ink-4">
							{page.hostname ?? "no hostname yet"}
							{page.published ? "" : " · draft"}
						</span>
					</CheckLabel>
				))
			)}
		</fieldset>
	);
}
