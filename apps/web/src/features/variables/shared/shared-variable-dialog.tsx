import { type FormEvent, useMemo, useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import type { SharedVariableView } from "#/api/gen/types.gen";
import { Button } from "#/components/ui/button";
import { Checkbox } from "#/components/ui/checkbox";
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
import { Spinner } from "#/components/ui/spinner";
import { Textarea } from "#/components/ui/textarea";
import { toast } from "#/components/ui/toast";
import { GeneratePopover } from "../generate-popover";
import { ENV_KEY_PATTERN } from "../raw-env";
import { buildCandidates } from "../reference-autocomplete";
import { useReferenceInput } from "../reference-suggestions";
import { type SharedOwner, useSharedMutations } from "./use-shared-mutations";

/**
 * Add or edit one shared variable. A shared value may reference only other
 * shared variables (`shared.KEY`), generators and transforms: it is evaluated
 * in every environment it reaches.
 */
export function SharedVariableDialog({
	owner,
	ownerName,
	target,
	siblings,
	onClose,
}: {
	owner: SharedOwner;
	ownerName: string;
	/** `"new"` to add, a variable to edit, null when closed. */
	target: SharedVariableView | "new" | null;
	/** The other shared variables, for `${{shared.` autocomplete. */
	siblings: readonly SharedVariableView[];
	onClose(): void;
}) {
	const last = useRef<SharedVariableView | "new">("new");
	if (target !== null) last.current = target;
	const shown = target ?? last.current;
	return (
		<Dialog open={target !== null} onOpenChange={(open) => !open && onClose()}>
			<DialogContent>
				<SharedForm
					owner={owner}
					ownerName={ownerName}
					editing={shown === "new" ? null : shown}
					siblings={siblings}
					onClose={onClose}
				/>
			</DialogContent>
		</Dialog>
	);
}

function SharedForm({
	owner,
	ownerName,
	editing,
	siblings,
	onClose,
}: {
	owner: SharedOwner;
	ownerName: string;
	editing: SharedVariableView | null;
	siblings: readonly SharedVariableView[];
	onClose(): void;
}) {
	const { save } = useSharedMutations(owner);
	const [key, setKey] = useState(editing?.key ?? "");
	const [value, setValue] = useState(
		editing && !editing.isSecret ? (editing.value ?? "") : "",
	);
	const [isSecret, setIsSecret] = useState(editing?.isSecret ?? false);
	const [comment, setComment] = useState(editing?.comment ?? "");
	const trimmed = key.trim();
	const invalid = trimmed !== "" && !ENV_KEY_PATTERN.test(trimmed);
	const keepsSecret = editing?.isSecret === true && isSecret && value === "";
	const candidates = useMemo(
		() =>
			buildCandidates([], "", {
				shared: siblings
					.filter((s) => s.key !== editing?.key)
					.map((s) => ({
						key: s.key,
						isSecret: s.computedSecret,
						layer: s.layer,
					})),
				functions: true,
			}).filter((c) => c.kind === "shared" || c.kind === "function"),
		[siblings, editing?.key],
	);
	const reference = useReferenceInput<HTMLTextAreaElement>({
		value,
		onValueChange: setValue,
		candidates,
		placement: "inside-bottom",
	});
	const canSave = trimmed !== "" && !invalid && !save.isPending;

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (!canSave) return;
		save.mutate(
			{
				key: trimmed,
				...(isSecret || editing?.isSecret ? { isSecret } : {}),
				comment: comment.trim(),
				...(keepsSecret ? {} : { value }),
			},
			{
				onSuccess: (data) => {
					const restaged = data?.restaged ?? [];
					toast.success(
						editing ? `Updated ${trimmed}` : `Added ${trimmed}`,
						restaged.length > 0
							? `Restaged: ${restaged.map((s) => s.slug).join(", ")}. Deploy them to apply.`
							: "No service changed yet: add a reference where it is needed.",
					);
					onClose();
				},
			},
		);
	};

	return (
		<form onSubmit={submit} className="contents">
			<DialogHeader>
				<DialogTitle>
					{editing ? `Edit ${editing.key}` : "New shared variable"}
				</DialogTitle>
				<DialogDescription>
					Shared with {ownerName}. A service only receives it through a variable
					such as KEY=$&#123;&#123;
					{owner.layer === "team" ? "team" : "shared"}.KEY&#125;&#125;; nothing
					is injected implicitly.
				</DialogDescription>
			</DialogHeader>
			<DialogBody className="overflow-visible">
				<Field>
					<FieldLabel htmlFor="shared-key">Name</FieldLabel>
					<Input
						id="shared-key"
						scale="lg"
						autoFocus={editing === null}
						autoComplete="off"
						spellCheck={false}
						className="font-mono"
						placeholder="SESSION_SECRET"
						value={key}
						readOnly={editing !== null}
						onChange={(event) => setKey(event.target.value)}
						aria-invalid={invalid}
					/>
					{invalid ? (
						<FieldError>
							Use letters, digits and underscores, not starting with a digit.
						</FieldError>
					) : null}
				</Field>
				<Field>
					<div className="flex items-center justify-between gap-2">
						<FieldLabel htmlFor="shared-value">Value</FieldLabel>
						<GeneratePopover
							onInsert={(expression, makesSecret) => {
								setValue((current) => current + expression);
								if (makesSecret) setIsSecret(true);
							}}
						/>
					</div>
					<div className="relative">
						<Textarea
							id="shared-value"
							autoComplete="off"
							spellCheck={false}
							rows={1}
							className="max-h-56 min-h-9 overflow-y-auto overscroll-contain font-mono"
							placeholder={
								editing?.isSecret
									? "Leave empty to keep the current value"
									: "value"
							}
							value={value}
							{...reference.fieldProps}
						/>
						{reference.popup}
					</div>
				</Field>
				<Field orientation="horizontal">
					<Checkbox
						id="shared-secret"
						checked={isSecret}
						onCheckedChange={(checked) => setIsSecret(checked === true)}
						disabled={editing?.isSecret === true && value === ""}
					/>
					<div className="flex flex-col gap-1">
						<FieldLabel htmlFor="shared-secret">Secret</FieldLabel>
						<FieldDescription>
							Stored encrypted and never shown again after saving.
						</FieldDescription>
					</div>
				</Field>
				<Field>
					<FieldLabel htmlFor="shared-comment">Note</FieldLabel>
					<Input
						id="shared-comment"
						autoComplete="off"
						maxLength={1000}
						placeholder="What this is for (optional)"
						value={comment}
						onChange={(event) => setComment(event.target.value)}
					/>
				</Field>
				{save.isError ? (
					<FieldError>{errorMessage(save.error)}</FieldError>
				) : null}
			</DialogBody>
			<DialogFooter>
				<Button type="button" variant="ghost" onClick={onClose}>
					Cancel
				</Button>
				<Button type="submit" disabled={!canSave}>
					{save.isPending ? <Spinner /> : null}
					{editing ? "Save variable" : "Add variable"}
				</Button>
			</DialogFooter>
		</form>
	);
}
