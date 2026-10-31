import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { getTemplateUpdateOptions } from "#/api/gen/@tanstack/react-query.gen";
import { applyTemplateUpdate } from "#/api/gen/sdk.gen";
import type {
	TemplateInstance,
	TemplateUpdate,
	TemplateUpdateChange,
} from "#/api/gen/types.gen";
import { invalidateOperations, invalidateServices } from "#/api/invalidate";
import { ErrorState } from "#/components/common/error-state";
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
import { DiffLine } from "#/components/ui/diff-line";
import { SectionLabel } from "#/components/ui/section-label";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { InputsForm } from "./inputs-form";
import {
	type InputValues,
	installInputs,
	missingInputs,
	updateGroups,
	updateInputFields,
	updateSummary,
	versionStep,
} from "./lib";
import { templatesActions, useTemplatesState } from "./store";

function ChangeRow({ change }: { change: TemplateUpdateChange }) {
	const what = change.field.replace(/^env\./, "").replace(/^file:/, "");
	const kind = change.field.startsWith("env.")
		? "variable "
		: change.field.startsWith("file:")
			? "file "
			: change.field.startsWith("volume:")
				? "volume "
				: "";
	const text = change.secret
		? "secret"
		: change.from && change.to
			? `${change.from} → ${change.to}`
			: change.to
				? change.to
				: change.from;
	const sign = !change.from ? "+" : !change.to ? "−" : "~";
	return (
		<div>
			<DiffLine sign={sign} value={text} wrap>
				{kind}
				{what.replace(/^volume:/, "")}
			</DiffLine>
			{change.reason ? (
				<p className="px-3 pb-1.5 pl-[34px] text-2xs text-ink-4 leading-[1.45]">
					{change.reason}
				</p>
			) : null}
		</div>
	);
}

function Group({
	title,
	hint,
	changes,
}: {
	title: string;
	hint?: string;
	changes: readonly TemplateUpdateChange[];
}) {
	if (changes.length === 0) return null;
	return (
		<section className="flex flex-col gap-1.5">
			<SectionLabel
				aside={
					hint ? <span className="text-2xs text-ink-4">{hint}</span> : undefined
				}
			>
				{title} ({changes.length})
			</SectionLabel>
			<div className="flex flex-col divide-y divide-line overflow-hidden rounded-card border border-line">
				{changes.map((change) => (
					<ChangeRow
						key={`${change.service}|${change.field}`}
						change={change}
					/>
				))}
			</div>
		</section>
	);
}

function UpdateBody({
	update,
	typed,
	onTyped,
}: {
	update: TemplateUpdate;
	typed: InputValues;
	onTyped(key: string, value: string): void;
}) {
	const groups = updateGroups(update.changes ?? []);
	const inputs = updateInputFields(update.inputs ?? []);
	if (update.blocked) {
		return <p className="text-ink-2 text-sm leading-[1.5]">{update.blocked}</p>;
	}
	if (!update.available) {
		return (
			<p className="text-ink-2 text-sm leading-[1.5]">
				This stack matches the template's current revision. Nothing to update.
			</p>
		);
	}
	return (
		<>
			<p className="text-ink-3 text-sm leading-[1.5]">
				{updateSummary(update)}
			</p>
			<Group
				title="Will be staged"
				hint="Nothing deploys until you press Deploy"
				changes={groups.apply}
			/>
			<Group
				title="Kept as you changed them"
				hint="The update leaves your value"
				changes={groups.kept}
			/>
			<Group
				title="Not done by an update"
				hint="Services, databases and routes are yours to change"
				changes={groups.manual}
			/>
			<Group title="Already applied" changes={groups.applied} />
			{inputs.length > 0 ? (
				<section className="flex flex-col gap-2">
					<SectionLabel>New inputs</SectionLabel>
					<InputsForm
						idPrefix="template-update"
						inputs={inputs}
						typed={typed}
						onChange={onTyped}
					/>
				</section>
			) : null}
			{(update.notes ?? []).length > 0 ? (
				<section className="flex flex-col gap-1">
					<SectionLabel>Notes for this revision</SectionLabel>
					<ul className="m-0 list-disc pl-5 text-ink-3 text-sm leading-[1.5]">
						{(update.notes ?? []).map((note) => (
							<li key={note}>{note}</li>
						))}
					</ul>
				</section>
			) : null}
		</>
	);
}

/**
 * "Update available": the diff between the template as installed and the
 * catalog's current revision, and one button that stages it. Fields you
 * changed are listed as kept, never overwritten; nothing is deployed.
 */
export function UpdateStackDialog() {
	const queryClient = useQueryClient();
	const { updating } = useTemplatesState();
	// Keep the stack while the dialog animates closed.
	const last = useRef<TemplateInstance | null>(updating);
	if (updating) last.current = updating;
	const instance = last.current;
	const [typed, setTyped] = useState<InputValues>({});
	const [pending, setPending] = useState(false);
	const query = useQuery({
		...getTemplateUpdateOptions({
			path: { instanceId: instance?.id ?? "" },
		}),
		enabled: updating !== null,
		retry: false,
		staleTime: 0,
	});
	if (!instance) return null;

	const update = query.data;
	const inputs = updateInputFields(update?.inputs ?? []);
	const missing = missingInputs(inputs, typed);
	const stageable =
		update !== undefined &&
		!update.blocked &&
		update.available &&
		missing.length === 0;

	const close = () => {
		templatesActions.cancelUpdate();
		setTyped({});
	};

	const stage = async () => {
		if (!update) return;
		setPending(true);
		try {
			const { data } = await applyTemplateUpdate({
				path: { instanceId: instance.id },
				body: {
					revision: update.currentRevision,
					inputs: installInputs(inputs, typed),
				},
			});
			toast.success(
				`Staged the ${instance.name} update`,
				`${data.staged} ${data.staged === 1 ? "change" : "changes"} staged. Review and deploy them from the staged bar.`,
			);
			close();
			void Promise.all([
				invalidateServices(queryClient),
				invalidateOperations(queryClient, [
					"listTemplateInstances",
					"getTemplateUpdate",
					"listPendingChanges",
				]),
			]);
		} catch (error) {
			toast.error("The update was not staged", errorMessage(error));
		} finally {
			setPending(false);
		}
	};

	return (
		<Dialog
			open={updating !== null}
			onOpenChange={(next) => {
				if (!next) close();
			}}
		>
			<DialogContent width={720} top={8}>
				<DialogHeader>
					<DialogTitle>Update {instance.name}</DialogTitle>
					<DialogDescription>
						{update ? versionStep(update) : "Comparing with the catalog…"}
					</DialogDescription>
				</DialogHeader>
				<DialogBody className="min-h-[160px]">
					{query.isPending ? (
						<div className="flex flex-col gap-2">
							<Skeleton className="h-5 w-48" />
							<Skeleton className="h-24 w-full" />
						</div>
					) : query.isError || !update ? (
						<ErrorState
							title="The update did not load"
							body={`${errorMessage(query.error)} Nothing was changed.`}
							retry={() => void query.refetch()}
						/>
					) : (
						<UpdateBody
							update={update}
							typed={typed}
							onTyped={(key, value) =>
								setTyped((current) => ({ ...current, [key]: value }))
							}
						/>
					)}
				</DialogBody>
				<DialogFooter>
					<Button variant="ghost" onClick={close}>
						{stageable ? "Cancel" : "Close"}
					</Button>
					{update?.available && !update.blocked ? (
						<Button
							disabled={!stageable || pending}
							onClick={() => void stage()}
						>
							{pending ? <Spinner className="size-3.5" /> : null}
							{update.apply > 0
								? `Stage ${update.apply} ${update.apply === 1 ? "change" : "changes"}`
								: "Mark as reviewed"}
						</Button>
					) : null}
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
