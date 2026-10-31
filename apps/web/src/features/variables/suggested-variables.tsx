import { useQuery } from "@tanstack/react-query";
import { DatabaseIcon, FileTextIcon, PlusIcon } from "lucide-react";
import { useId, useState } from "react";
import type { SuggestedVariable } from "#/api/gen/types.gen";
import { suggestedVariablesQuery } from "#/api/hooks";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import { toast } from "#/components/ui/toast";
import { useVariableMutations } from "./use-variable-mutations";

export interface SuggestionDraft {
	key: string;
	value: string;
	isSecret: boolean;
}

/**
 * Databases of this environment that nothing here references yet. Accepting
 * one is an ordinary staged write. (The keys of the repository's
 * `.env.example` have their own banner and review sheet.)
 */
export function SuggestedVariables({
	serviceId,
	onNeedsInput,
}: {
	serviceId: string;
	onNeedsInput(draft: SuggestionDraft): void;
}) {
	const suggestions = useQuery(suggestedVariablesQuery(serviceId));
	const { save } = useVariableMutations(serviceId);
	const [open, setOpen] = useState(true);
	const listId = useId();
	const items = (suggestions.data?.items ?? []).filter(
		(item) => item.source === "database",
	);
	if (items.length === 0) return null;

	const accept = (item: SuggestedVariable) => {
		if (item.needsInput) {
			onNeedsInput({
				key: item.key,
				value: item.value,
				isSecret: item.proposedSecret,
			});
			return;
		}
		save.mutate(
			{
				key: item.key,
				value: item.value,
				...(item.proposedSecret ? { isSecret: true } : {}),
			},
			{
				onSuccess: () =>
					toast.success(`Added ${item.key}`, "Staged until the next deploy."),
			},
		);
	};

	return (
		<div className="surface-raised rounded-card">
			<button
				type="button"
				aria-expanded={open}
				aria-controls={listId}
				onClick={() => setOpen((value) => !value)}
				className="flex w-full cursor-pointer items-center gap-2 rounded-card px-3 py-2.5 text-left text-[13px] text-ink-1 outline-none focus-visible:focus-ring"
			>
				<DisclosureChevron
					open={open}
					className="size-3.5 shrink-0 text-ink-3"
				/>
				<span className="min-w-0 font-medium">
					{items.length} suggested variable{items.length === 1 ? "" : "s"}
				</span>
				<span className="ml-auto shrink-0 text-ink-3 text-xs">
					{open ? "Hide" : "Show"}
				</span>
			</button>
			<DisclosurePanel open={open} id={listId}>
				<ul
					aria-label="Suggested variables"
					className="divide-y divide-line-faint border-line-faint border-t"
				>
					{items.map((item) => (
						<li key={item.key} className="flex items-start gap-3 px-3 py-2">
							<span className="mt-0.5 shrink-0 text-ink-3">
								{item.source === "database" ? (
									<DatabaseIcon aria-hidden className="size-3.5" />
								) : (
									<FileTextIcon aria-hidden className="size-3.5" />
								)}
							</span>
							<div className="flex min-w-0 flex-1 flex-col gap-0.5">
								<span className="flex flex-wrap items-center gap-2 font-medium font-mono text-[11.5px] text-ink-2">
									{item.key}
									{item.proposedSecret ? <Badge>secret</Badge> : null}
									{item.needsInput ? (
										<Badge tone="warn">needs a value</Badge>
									) : null}
								</span>
								<span
									className="truncate font-mono text-[11.5px] text-ink-3"
									title={item.value}
								>
									{item.value || "—"}
								</span>
								<span className="text-[11px] text-ink-4 leading-snug">
									{item.reason}
								</span>
								{(item.warnings ?? []).map((warning) => (
									<span
										key={warning}
										className="text-[11px] text-warn leading-snug"
									>
										{warning}
									</span>
								))}
							</div>
							<Button
								type="button"
								variant="secondary"
								size="sm"
								disabled={save.isPending}
								onClick={() => accept(item)}
							>
								<PlusIcon aria-hidden />
								{item.needsInput ? "Set…" : "Add"}
							</Button>
						</li>
					))}
				</ul>
			</DisclosurePanel>
		</div>
	);
}
