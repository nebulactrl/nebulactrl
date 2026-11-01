import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import { scanServiceEnvTemplates } from "#/api/gen/sdk.gen";
import type { SuggestedVariable } from "#/api/gen/types.gen";
import { suggestedVariablesQuery } from "#/api/hooks";
import { invalidateServices } from "#/api/invalidate";
import type { Service } from "#/api/models";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import {
	Sheet,
	SheetBody,
	SheetContent,
	SheetDescription,
	SheetFooter,
	SheetHeader,
	SheetTitle,
} from "#/components/ui/sheet";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import {
	bannerText,
	checkedCount,
	emptyCount,
	exampleItems,
	initialChoices,
	toWrites,
} from "./env-example";
import { EnvExampleList } from "./env-example-list";
import { useVariableMutations } from "./use-variable-mutations";

/**
 * "3 keys from .env.example aren't set": the keys of the repository's example
 * files that this service has no variable for. Review opens the sheet to add
 * them in one go; Read again fetches the files from the repository (it also
 * happens on every push to the tracked branch).
 */
export function EnvExampleBanner({ service }: { service: Service }) {
	const git = service.sourceType === "git";
	const suggestions = useQuery({
		...suggestedVariablesQuery(service.id),
		enabled: git,
	});
	const queryClient = useQueryClient();
	const [open, setOpen] = useState(false);
	const reread = useMutation({
		mutationFn: async () => {
			const { data } = await scanServiceEnvTemplates({
				path: { serviceId: service.id },
			});
			return data;
		},
		onSuccess: async (result) => {
			await invalidateServices(queryClient);
			if (result.status === "unavailable") {
				toast.warning("The repository could not be read", result.message);
			} else if (result.status === "none") {
				toast.info("No example file found", result.message);
			}
		},
		onError: (error) =>
			toast.error("Could not read the repository", errorMessage(error)),
	});

	const items = exampleItems(suggestions.data?.items);
	if (!git || items.length === 0) return null;
	const files = [...new Set(items.map((item) => item.file ?? ""))].filter(
		Boolean,
	);
	const origin = [
		suggestions.data?.branch,
		suggestions.data?.commit?.slice(0, 7),
	]
		.filter(Boolean)
		.join(" · ");

	return (
		<>
			<Alert variant="info" data-slot="env-example-banner">
				<AlertTitle>{bannerText(items.length, files)}</AlertTitle>
				<AlertDescription>
					<p>
						Read from your repository{origin ? ` (${origin})` : ""}. Review them
						and add the ones you want; they are staged until the next deploy.
					</p>
					<div className="flex flex-wrap items-center gap-2">
						<Button
							type="button"
							variant="secondary"
							size="sm"
							onClick={() => setOpen(true)}
						>
							Review keys
						</Button>
						<Button
							type="button"
							variant="ghost"
							size="sm"
							disabled={reread.isPending}
							onClick={() => reread.mutate()}
						>
							{reread.isPending ? <Spinner /> : null}
							Read again
						</Button>
					</div>
				</AlertDescription>
			</Alert>
			<EnvExampleSheet
				serviceId={service.id}
				items={items}
				origin={origin}
				open={open}
				onOpenChange={setOpen}
			/>
		</>
	);
}

/** The review sheet: check the keys to add, give the ones that need a value one. */
function EnvExampleSheet({
	serviceId,
	items,
	origin,
	open,
	onOpenChange,
}: {
	serviceId: string;
	items: readonly SuggestedVariable[];
	/** Where the files were read from, e.g. `main · 3f2a9c1`. */
	origin: string;
	open: boolean;
	onOpenChange(open: boolean): void;
}) {
	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="sm:max-w-[560px]">
				<SheetHeader className="pb-3">
					<SheetTitle className="pr-10">Keys from .env.example</SheetTitle>
					<SheetDescription>{origin || "Your repository"}</SheetDescription>
				</SheetHeader>
				{open ? (
					<ReviewBody
						serviceId={serviceId}
						items={items}
						onDone={() => onOpenChange(false)}
					/>
				) : null}
			</SheetContent>
		</Sheet>
	);
}

function ReviewBody({
	serviceId,
	items,
	onDone,
}: {
	serviceId: string;
	items: readonly SuggestedVariable[];
	onDone(): void;
}) {
	const { saveMany } = useVariableMutations(serviceId);
	const [choices, setChoices] = useState(() => initialChoices(items));
	const count = checkedCount(items, choices);
	const empty = emptyCount(items, choices);

	const add = () =>
		saveMany.mutate(toWrites(items, choices), {
			onSuccess: () => {
				toast.success(
					`Added ${count} ${count === 1 ? "variable" : "variables"}`,
					empty > 0
						? `Staged until the next deploy. ${empty} still ${empty === 1 ? "has" : "have"} no value.`
						: "Staged until the next deploy.",
				);
				onDone();
			},
		});

	return (
		<>
			<SheetBody className="flex flex-col gap-3 overscroll-contain">
				<p className="type-secondary">
					Every key starts checked. A key that needs your own value has a field
					for it; leave it empty to add the key now and fill it in later (the
					deploy plan reminds you).
				</p>
				<EnvExampleList
					idPrefix="env-example-sheet"
					items={items}
					choices={choices}
					onChoices={setChoices}
					autoFocus
				/>
				{saveMany.isError ? (
					<Alert variant="destructive">
						<AlertTitle>Could not add the variables</AlertTitle>
						<AlertDescription>{errorMessage(saveMany.error)}</AlertDescription>
					</Alert>
				) : null}
			</SheetBody>
			<SheetFooter className="items-center justify-between gap-3">
				<p className="type-secondary" aria-live="polite">
					{count} of {items.length} selected
					{empty > 0 ? ` · ${empty} without a value` : ""}
				</p>
				<div className="flex items-center gap-2">
					<Button type="button" variant="ghost" onClick={onDone}>
						Cancel
					</Button>
					<Button
						type="button"
						disabled={count === 0 || saveMany.isPending}
						onClick={add}
					>
						{saveMany.isPending ? <Spinner /> : null}
						Add {count} {count === 1 ? "variable" : "variables"}
					</Button>
				</div>
			</SheetFooter>
		</>
	);
}
