import { useQuery } from "@tanstack/react-query";
import { cn } from "cn";
import { SearchIcon } from "lucide-react";
import { useMemo } from "react";
import { errorMessage } from "#/api/client-config";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import {
	DialogBody,
	DialogDescription,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import { Input } from "#/components/ui/input";
import { Skeleton } from "#/components/ui/skeleton";
import { categoryChips, filterTemplates, licenseChips } from "./lib";
import { catalogQuery } from "./queries";
import { templatesActions, useTemplatesState } from "./store";
import { TemplateCard } from "./template-card";

function Chip({
	pressed,
	disabled,
	onClick,
	title,
	children,
}: {
	pressed: boolean;
	disabled: boolean;
	onClick(): void;
	title?: string;
	children: React.ReactNode;
}) {
	return (
		<button
			type="button"
			aria-pressed={pressed}
			disabled={disabled}
			onClick={onClick}
			title={title}
			className={cn(
				"flex h-7 shrink-0 cursor-pointer items-center gap-1.5 rounded-full border px-3 font-medium text-[12px] leading-none transition-[color,background-color,border-color,scale] duration-(--press-duration) ease-out focus-visible:focus-ring not-disabled:active:scale-(--press-scale) disabled:cursor-not-allowed disabled:opacity-45 motion-reduce:transition-none",
				pressed
					? "border-accent-ink/40 bg-accent-wash text-ink-1"
					: "border-line bg-transparent text-ink-3 hover:border-line-strong hover:text-ink-1",
			)}
		>
			{children}
		</button>
	);
}

function GridSkeleton() {
	return (
		<div
			aria-hidden
			className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3"
		>
			{Array.from({ length: 6 }, (_, index) => (
				// biome-ignore lint/suspicious/noArrayIndexKey: a fixed run of identical placeholders.
				<Skeleton key={index} className="h-[140px] rounded-card" />
			))}
		</div>
	);
}

/**
 * The gallery step: search, category chips with counts, and the grid. The
 * catalog arrives whole, so every filter is client-side and instant.
 */
export function GalleryView() {
	const { query, category, license } = useTemplatesState();
	const catalog = useQuery(catalogQuery());
	const items = catalog.data?.items;
	const categories = catalog.data?.categories;

	const chips = useMemo(
		() => categoryChips(categories ?? [], items ?? [], query, license),
		[categories, items, query, license],
	);
	const licenses = useMemo(
		() => licenseChips(items ?? [], query, category),
		[items, query, category],
	);
	const shown = useMemo(
		() => filterTemplates(items ?? [], query, category, license),
		[items, query, category, license],
	);

	return (
		<>
			<DialogHeader className="gap-3">
				<div className="flex flex-col gap-1.5">
					<DialogTitle>New from template</DialogTitle>
					<DialogDescription>
						Reviewed, self-hostable apps. Each one becomes ordinary services on
						your canvas, staged until you deploy.
					</DialogDescription>
				</div>
				<div className="relative">
					<SearchIcon
						aria-hidden
						className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-ink-3"
					/>
					<Input
						type="search"
						scale="lg"
						autoFocus
						autoComplete="off"
						spellCheck={false}
						aria-label="Search templates"
						placeholder={`Search ${items ? `${items.length} ` : ""}apps, tags, databases, licences`}
						value={query}
						onChange={(event) => templatesActions.setQuery(event.target.value)}
						className="pl-9 [&::-webkit-search-cancel-button]:hidden"
					/>
				</div>
				{categories ? (
					<fieldset
						aria-label="Category"
						className="m-0 flex min-w-0 flex-wrap items-center gap-1.5 border-0 p-0"
					>
						{chips.map((chip) => (
							<Chip
								key={chip.id ?? "all"}
								pressed={category === chip.id}
								disabled={chip.count === 0 && category !== chip.id}
								onClick={() => templatesActions.setCategory(chip.id)}
							>
								{chip.label}
								<span className="font-mono text-2xs text-ink-4 tabular-nums">
									{chip.count}
								</span>
							</Chip>
						))}
					</fieldset>
				) : null}
				{items ? (
					<fieldset
						aria-label="Licence"
						className="m-0 flex min-w-0 flex-wrap items-center gap-1.5 border-0 p-0"
					>
						<span className="text-2xs text-ink-3">Licence</span>
						{licenses.map((chip) => (
							<Chip
								key={chip.id}
								pressed={license === chip.id}
								title={chip.hint}
								disabled={chip.count === 0 && license !== chip.id}
								onClick={() =>
									templatesActions.setLicense(
										license === chip.id ? null : chip.id,
									)
								}
							>
								{chip.label}
								<span className="font-mono text-2xs text-ink-4 tabular-nums">
									{chip.count}
								</span>
							</Chip>
						))}
					</fieldset>
				) : null}
			</DialogHeader>
			<DialogBody className="min-h-[320px]">
				{catalog.isPending ? (
					<GridSkeleton />
				) : catalog.isError ? (
					<ErrorState
						title="Templates did not load"
						body={`${errorMessage(catalog.error)} Nothing was changed.`}
						retry={() => void catalog.refetch()}
					/>
				) : shown.length === 0 ? (
					<EmptyState
						frame="none"
						illustration="search"
						title={
							query.trim() === ""
								? "No templates here"
								: `Nothing matches “${query.trim()}”`
						}
						body="Try another word, or clear the category and licence filters."
					/>
				) : (
					<ul className="m-0 grid list-none grid-cols-1 gap-3 p-0 sm:grid-cols-2 lg:grid-cols-3">
						{shown.map((item) => (
							<TemplateCard
								key={item.id}
								item={item}
								onOpen={templatesActions.select}
							/>
						))}
					</ul>
				)}
			</DialogBody>
		</>
	);
}
