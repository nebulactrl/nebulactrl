import { useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import type { Variable } from "#/api/models";
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
import { FieldError } from "#/components/ui/field";
import { Spinner } from "#/components/ui/spinner";
import { Textarea } from "#/components/ui/textarea";
import { toast } from "#/components/ui/toast";
import { diffEnv, parseEnv, serializeEnv } from "./raw-env";
import { REFERENCE_OPEN } from "./reference-autocomplete";
import {
	useReferenceCandidates,
	useReferenceInput,
} from "./reference-suggestions";
import { useVariableMutations } from "./use-variable-mutations";

export interface RawEnvDialogProps {
	serviceId: string;
	/** The service's environment: its other services are what `${{` can reference. */
	environmentId: string;
	open: boolean;
	variables: readonly Variable[];
	onOpenChange(open: boolean): void;
}

// biome-ignore lint/suspicious/noTemplateCurlyInString: intentional reference syntax
const PLACEHOLDER = "e.g.\nHOST=${{postgres.NEBULA_PRIVATE_HOST}}\nLEVEL=info";

/**
 * The raw `.env` editor. It holds the plain variables only; secrets stay out
 * of the text and survive a save untouched. Type a secret's name here to
 * replace its value.
 */
export function RawEnvDialog({
	serviceId,
	environmentId,
	open,
	variables,
	onOpenChange,
}: RawEnvDialogProps) {
	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="sm:max-w-[640px]">
				<RawEnvForm
					serviceId={serviceId}
					environmentId={environmentId}
					variables={variables}
					onOpenChange={onOpenChange}
				/>
			</DialogContent>
		</Dialog>
	);
}

/** Mounts with the dialog, so each opening starts from the stored variables. */
function RawEnvForm({
	serviceId,
	environmentId,
	variables,
	onOpenChange,
}: Omit<RawEnvDialogProps, "open">) {
	const { applyDiff } = useVariableMutations(serviceId);
	const [text, setText] = useState(() => serializeEnv(variables));
	const candidates = useReferenceCandidates(
		environmentId,
		serviceId,
		text.includes(REFERENCE_OPEN),
	);
	const reference = useReferenceInput<HTMLTextAreaElement>({
		value: text,
		onValueChange: setText,
		candidates,
		placement: "inside-bottom",
	});

	const parsed = useMemo(() => parseEnv(text), [text]);
	const diff = useMemo(
		() => diffEnv(variables, parsed.entries),
		[variables, parsed.entries],
	);
	const secretCount = variables.filter((variable) => variable.isSecret).length;
	const changes = diff.upserts.length + diff.deletes.length;
	const canSave =
		parsed.errors.length === 0 && changes > 0 && !applyDiff.isPending;

	const save = () => {
		if (!canSave) return;
		applyDiff.mutate(diff, {
			onSuccess: () => {
				toast.success(
					`Saved ${changes} variable change${changes === 1 ? "" : "s"}`,
					"Staged until the next deploy.",
				);
				onOpenChange(false);
			},
		});
	};

	return (
		<>
			<DialogHeader>
				<DialogTitle>Raw editor</DialogTitle>
				<DialogDescription>
					One KEY=value per line.{" "}
					{secretCount > 0
						? secretCount === 1
							? "1 secret is not shown and stays as it is."
							: `${secretCount} secrets are not shown and stay as they are.`
						: "Secrets are never shown here."}
				</DialogDescription>
			</DialogHeader>
			<DialogBody>
				<div className="relative">
					<Textarea
						aria-label="Variables in .env format"
						spellCheck={false}
						autoFocus
						rows={14}
						className="min-h-[260px] overscroll-contain font-mono text-[13px]"
						placeholder={PLACEHOLDER}
						value={text}
						{...reference.fieldProps}
					/>
					{reference.popup}
				</div>
				{parsed.errors.length > 0 ? (
					<ul className="flex flex-col gap-1" aria-label="Problems in the text">
						{parsed.errors.slice(0, 5).map((error) => (
							<li key={`${error.line}-${error.message}`}>
								<FieldError>
									Line {error.line}: {error.message}
								</FieldError>
							</li>
						))}
					</ul>
				) : (
					<p className="text-[12px] text-muted-foreground" aria-live="polite">
						{changes === 0
							? "No changes."
							: `${diff.upserts.length} to add or update, ${diff.deletes.length} to delete.`}
					</p>
				)}
				{applyDiff.isError ? (
					<FieldError>{errorMessage(applyDiff.error)}</FieldError>
				) : null}
			</DialogBody>
			<DialogFooter>
				<Button
					type="button"
					variant="ghost"
					onClick={() => onOpenChange(false)}
				>
					Cancel
				</Button>
				<Button type="button" disabled={!canSave} onClick={save}>
					{applyDiff.isPending ? <Spinner /> : null}
					Save variables
				</Button>
			</DialogFooter>
		</>
	);
}
