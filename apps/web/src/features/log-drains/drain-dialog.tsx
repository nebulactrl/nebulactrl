import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { createLogDrain, updateLogDrain } from "#/api/gen/sdk.gen";
import type { LogDrainView } from "#/api/gen/types.gen";
import { projectsQuery } from "#/api/hooks";
import { Button } from "#/components/ui/button";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { OptionCard, OptionCardGroup } from "#/components/ui/option-card";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { CredentialFields } from "./credential-fields";
import { DrainIcon } from "./drain-icon";
import { KindFields, type SetDraft } from "./kind-fields";
import {
	createBody,
	DEFAULT_BUFFER_MB,
	DRAIN_KINDS,
	type Draft,
	type DrainKind,
	draftFor,
	draftProblem,
	kindLabel,
	MAX_BUFFER_MB,
	MIN_BUFFER_MB,
	patchBody,
	visibleServiceIds,
} from "./model";
import { invalidateLogDrains } from "./queries";
import { ScopeFields } from "./scope-fields";
import { useTeamServices } from "./use-team-services";

/**
 * Create or edit a log drain. The kind is chosen when it is created and then
 * stays. Credentials are write-only: an edit shows a mask and a header's name,
 * an empty field keeps what is stored, typing replaces it and Remove clears it.
 * The server validates again; its message (422) is shown under the form.
 */
export function DrainDialog({
	open,
	onOpenChange,
	drain,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	drain: LogDrainView | null;
}) {
	const queryClient = useQueryClient();
	const editing = drain !== null;
	const projects = useQuery({ ...projectsQuery(), enabled: open });
	const team = useTeamServices(open);

	const [draft, setDraft] = useState<Draft>(() => draftFor(drain));
	const [error, setError] = useState<string | null>(null);

	useEffect(() => {
		if (!open) return;
		setDraft(draftFor(drain));
		setError(null);
	}, [open, drain]);

	const set: SetDraft = (key, value) =>
		setDraft((prev) => ({ ...prev, [key]: value }));

	const save = useMutation({
		mutationFn: async () => {
			// A service whose project was unticked, or that was deleted, is not sent.
			const effective: Draft =
				team.services.length > 0
					? {
							...draft,
							serviceIds: visibleServiceIds(
								draft.serviceIds,
								team.services,
								draft.allProjects,
								draft.projectIds,
							),
						}
					: draft;
			if (drain) {
				await updateLogDrain({
					path: { drainId: drain.id },
					body: patchBody(effective),
					throwOnError: true,
				});
			} else {
				await createLogDrain({
					body: createBody(effective),
					throwOnError: true,
				});
			}
		},
		onSuccess: async () => {
			await invalidateLogDrains(queryClient);
			toast.success(
				editing ? "Log drain saved" : "Log drain added",
				editing
					? "Connected servers pick up the change shortly."
					: "Each server starts shipping from now. Test the drain to confirm lines arrive.",
			);
			onOpenChange(false);
		},
		onError: (e) => setError(errorMessage(e)),
	});

	const problem = draftProblem(draft, editing);
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (problem || save.isPending) return;
		setError(null);
		save.mutate();
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="lg">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>
							{drain ? `Edit ${drain.name}` : "Add log drain"}
						</DialogTitle>
						<DialogDescription>
							Ship your services' runtime logs to a log platform you already
							use. Credentials are stored encrypted and never shown again.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						{drain ? (
							<p className="m-0 flex items-center gap-2.5 text-ink-2 text-xs">
								<DrainIcon kind={drain.kind} size={24} />
								{kindLabel(drain.kind)}. The kind cannot be changed; add a new
								drain to ship somewhere else.
							</p>
						) : (
							<Field>
								<FieldLabel id="drain-kind-label">Type</FieldLabel>
								<OptionCardGroup<DrainKind>
									value={draft.kind}
									onValueChange={(next) => set("kind", next)}
									aria-labelledby="drain-kind-label"
									className="grid grid-cols-1 sm:grid-cols-2"
								>
									{DRAIN_KINDS.map((k) => (
										<OptionCard
											key={k.kind}
											value={k.kind}
											title={
												<span className="flex items-center gap-2.5">
													<DrainIcon kind={k.kind} size={24} />
													{k.label}
												</span>
											}
											description={k.blurb}
											className="min-w-0"
										/>
									))}
								</OptionCardGroup>
							</Field>
						)}

						<Field>
							<FieldLabel htmlFor="drain-name">Name</FieldLabel>
							<Input
								id="drain-name"
								autoFocus
								autoComplete="off"
								placeholder={`${kindLabel(draft.kind)} drain`}
								value={draft.name}
								onChange={(e) => set("name", e.target.value)}
							/>
						</Field>

						<KindFields draft={draft} set={set} onDraft={setDraft} />
						<CredentialFields draft={draft} set={set} drain={drain} />
						<ScopeFields
							draft={draft}
							set={set}
							projects={projects.data ?? []}
							projectsPending={projects.isPending}
							services={team.services}
							servicesPending={team.pending}
						/>

						<details className="group/advanced">
							<summary className="cursor-pointer select-none rounded-sm text-ink-2 text-sm outline-none hover:text-ink-1 focus-visible:focus-ring">
								Advanced
							</summary>
							<div className="pt-3">
								<Field className="max-w-[200px]">
									<FieldLabel htmlFor="drain-buffer">Buffer (MB)</FieldLabel>
									<Input
										id="drain-buffer"
										type="number"
										min={MIN_BUFFER_MB}
										max={MAX_BUFFER_MB}
										inputMode="numeric"
										className="font-mono"
										placeholder={String(DEFAULT_BUFFER_MB)}
										value={draft.bufferMb}
										onChange={(e) => set("bufferMb", e.target.value)}
									/>
									<FieldDescription>
										How much undelivered log each server keeps on its disk, per
										drain. {MIN_BUFFER_MB} to {MAX_BUFFER_MB} MB. When it is
										full, the oldest waiting lines are dropped.
									</FieldDescription>
								</Field>
							</div>
						</details>

						<p className="surface-well m-0 rounded-card p-3 text-ink-3 text-xs leading-relaxed">
							Each server ships its own logs from its own host, starting when
							the drain is saved. Lines that cannot be delivered wait on that
							server's disk (up to the buffer size) and are retried.
						</p>

						{error ? <FieldError>{error}</FieldError> : null}
					</DialogBody>
					<DialogFooter>
						<Button
							type="button"
							variant="ghost"
							onClick={() => onOpenChange(false)}
						>
							Cancel
						</Button>
						<Button
							type="submit"
							variant={problem ? "secondary" : "primary"}
							disabled={problem !== null || save.isPending}
							title={problem ?? undefined}
						>
							{save.isPending ? <Spinner /> : null}
							{editing ? "Save" : "Add drain"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
