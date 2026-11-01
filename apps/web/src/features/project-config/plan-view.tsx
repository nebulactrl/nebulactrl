import { cn } from "cn";
import { ChevronRightIcon } from "lucide-react";
import { type ReactNode, useId, useState } from "react";
import { Badge } from "#/components/ui/badge";
import { DiffLine } from "#/components/ui/diff-line";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import {
	changeRow,
	type DiagnosticLike,
	diagnosticWhere,
	FILE_NOTE,
	type PlanChangeLike,
	type PlanServiceLike,
} from "./model";

/** The changes of one service (or of a shared layer) as rows of diff lines. */
export function ChangeList({
	changes,
	slug,
	label,
}: {
	changes: readonly PlanChangeLike[];
	slug?: string;
	label: string;
}) {
	const rows = changes
		.filter((change) => change.op !== "keep" && change.op !== "release")
		.map((change) => changeRow(change, slug));
	if (rows.length === 0) return null;
	return (
		<ul aria-label={label} className="surface-well rounded-ctl py-1">
			{rows.map((row) => (
				<li key={row.key} className="flex items-center gap-2 pr-3">
					<DiffLine
						sign={row.sign}
						value={row.value}
						note={FILE_NOTE}
						className="min-w-0 flex-1"
					>
						{row.label}
					</DiffLine>
					<span className="flex shrink-0 items-center gap-1.5">
						<Badge
							variant={row.applies === "applies at once" ? "accent" : "outline"}
						>
							{row.applies}
						</Badge>
						{row.reason ? <Badge>{row.reason}</Badge> : null}
					</span>
				</li>
			))}
		</ul>
	);
}

/** A service of the plan: its name and kind, then what the file does to it. */
export function ServiceChanges({ service }: { service: PlanServiceLike }) {
	const changes = service.changes ?? [];
	return (
		<section aria-label={service.slug} className="flex flex-col gap-1.5">
			<h4 className="flex min-w-0 items-center gap-2 font-medium text-[13px] text-ink-1">
				<span className="truncate">{service.slug}</span>
				{service.kind ? <Badge>{service.kind}</Badge> : null}
			</h4>
			<ChangeList
				changes={changes}
				slug={service.slug}
				label={`Changes to ${service.slug}`}
			/>
			{changes.length === 0 ? (
				<p className="text-ink-3 text-xs">
					A new {service.kind ?? ""} service with the file's defaults.
				</p>
			) : null}
		</section>
	);
}

/** A titled group of the review: "Creates", "Updates", "Released". */
export function PlanSection({
	title,
	count,
	children,
}: {
	title: string;
	count?: number;
	children: ReactNode;
}) {
	return (
		<section aria-label={title} className="flex flex-col gap-2">
			<h3 className="type-label">
				{title}
				{count === undefined ? null : (
					<span className="ml-1.5 font-normal text-ink-4">{count}</span>
				)}
			</h3>
			{children}
		</section>
	);
}

/** A group that starts folded: "Not in file · 3". */
export function FoldedSection({
	title,
	count,
	children,
}: {
	title: string;
	count: number;
	children: ReactNode;
}) {
	const [open, setOpen] = useState(false);
	const id = useId();
	return (
		<section aria-label={title} className="flex flex-col gap-2">
			<button
				type="button"
				aria-expanded={open}
				aria-controls={`${id}-panel`}
				id={`${id}-header`}
				onClick={() => setOpen((value) => !value)}
				className={cn(
					"type-label flex w-fit cursor-pointer items-center gap-1.5 rounded-sm outline-none focus-visible:focus-ring",
				)}
			>
				<DisclosureChevron open={open} />
				{title}
				<span className="font-normal text-ink-4">{count}</span>
			</button>
			<DisclosurePanel
				open={open}
				id={`${id}-panel`}
				labelledBy={`${id}-header`}
			>
				{children}
			</DisclosurePanel>
		</section>
	);
}

/** One diagnostic: where it is, what it says, what to do, and "did you mean". */
export function DiagnosticRow({ diagnostic }: { diagnostic: DiagnosticLike }) {
	const where = diagnosticWhere(diagnostic);
	const suggestions = diagnostic.suggestions ?? [];
	return (
		<li className="flex flex-col gap-0.5 text-xs leading-[1.5]">
			<span className="flex min-w-0 flex-wrap items-baseline gap-x-2">
				{where ? <span className="font-mono text-ink-3">{where}</span> : null}
				<span className="text-ink-1">{diagnostic.message}</span>
				<span className="font-mono text-2xs text-ink-4">{diagnostic.code}</span>
			</span>
			{diagnostic.path ? (
				<span className="font-mono text-ink-4">{diagnostic.path}</span>
			) : null}
			{diagnostic.hint ? (
				<span className="text-ink-3">{diagnostic.hint}</span>
			) : null}
			{suggestions.length > 0 ? (
				<span className="text-ink-3">
					Did you mean{" "}
					{suggestions.map((suggestion, index) => (
						<span key={suggestion}>
							{index > 0 ? ", " : ""}
							<span className="font-mono text-ink-2">{suggestion}</span>
						</span>
					))}
					?
				</span>
			) : null}
		</li>
	);
}

/** Paths the file stopped naming: they keep their live value and are no longer managed. */
export function ReleasedList({ paths }: { paths: readonly string[] }) {
	return (
		<ul className="surface-well flex flex-col gap-1 rounded-ctl px-3 py-2">
			{paths.map((path) => (
				<li
					key={path}
					className="flex items-center gap-2 font-mono text-ink-2 text-xs"
				>
					<ChevronRightIcon aria-hidden className="size-3 text-ink-4" />
					<span className="min-w-0 truncate" title={path}>
						{path}
					</span>
				</li>
			))}
		</ul>
	);
}
