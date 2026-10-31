import { useQuery } from "@tanstack/react-query";
import { LockIcon } from "lucide-react";
import { variablesQuery } from "#/api/hooks";
import type { Service, Variable } from "#/api/models";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { useInspectorActions } from "../inspector-context";

const PREVIEW_COUNT = 4;
const MASK = "••••••••••••••••";

/**
 * The first few variables as ruled rows: keys in mono, plain values
 * truncated, secrets masked behind a lock chip. A secret's value is never
 * fetched, so there is nothing here to reveal.
 */
export function VariablesPreview({ service }: { service: Service }) {
	const variables = useQuery(variablesQuery(service.id));
	return (
		<SkeletonReveal
			loading={variables.isPending}
			className="flex flex-col gap-2"
			skeleton={
				<>
					<Skeleton className="h-5 w-full" />
					<Skeleton className="h-5 w-full" />
				</>
			}
		>
			{() => (
				<VariablesList
					failed={variables.isError}
					items={variables.data?.items ?? []}
				/>
			)}
		</SkeletonReveal>
	);
}

function VariablesList({
	failed,
	items,
}: {
	failed: boolean;
	items: readonly Variable[];
}) {
	const { openTab } = useInspectorActions();
	if (failed) {
		return <p className="py-2 text-ink-3 text-xs">Variables did not load.</p>;
	}
	if (items.length === 0) {
		return (
			<p className="py-2 text-ink-3 text-xs">
				No variables yet.{" "}
				<button
					type="button"
					onClick={() => openTab("variables")}
					className="cursor-pointer text-accent-ink hover:text-ink-1 focus-visible:focus-ring"
				>
					Add one
				</button>
			</p>
		);
	}
	const shown = items.slice(0, PREVIEW_COUNT);
	const hidden = items.length - shown.length;

	return (
		<ul className="rule-rows">
			{shown.map((variable) => (
				<li
					key={variable.key}
					className="grid grid-cols-[minmax(0,5fr)_minmax(0,6fr)_auto] items-center gap-3 font-mono text-xs leading-none"
				>
					<span title={variable.key} className="truncate text-ink-1">
						{variable.key}
					</span>
					<span
						title={variable.isSecret ? undefined : variable.value}
						className="truncate text-ink-3"
					>
						{variable.isSecret ? MASK : variable.value}
					</span>
					{variable.isSecret ? (
						<span className="flex h-[18px] items-center gap-1 rounded-[5px] border border-line-faint bg-white/5 px-1.5 font-medium font-sans text-[10px] text-ink-3">
							<LockIcon aria-hidden className="size-2.5" />
							secret
						</span>
					) : (
						<span aria-hidden />
					)}
				</li>
			))}
			{hidden > 0 ? (
				<li className="text-[10.5px] text-ink-3">+{hidden} more</li>
			) : null}
		</ul>
	);
}
