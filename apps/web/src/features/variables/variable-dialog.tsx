import { cn } from "cn";
import { type FormEvent, type KeyboardEvent, useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import type { Variable, VariableScope } from "#/api/models";
import { Badge } from "#/components/ui/badge";
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
import { Segmented } from "#/components/ui/segmented";
import { Spinner } from "#/components/ui/spinner";
import { Textarea } from "#/components/ui/textarea";
import { toast } from "#/components/ui/toast";
import { DraftFeedback } from "./draft-feedback";
import { GeneratePopover } from "./generate-popover";
import { useNameCombobox, useNameSources } from "./name-combobox";
import { isReservedKey, type NameSuggestion } from "./name-suggestions";
import { ENV_KEY_PATTERN } from "./raw-env";
import { REFERENCE_OPEN } from "./reference-autocomplete";
import {
	useReferenceCandidates,
	useReferenceInput,
} from "./reference-suggestions";
import { useDraftValidation } from "./use-draft-validation";
import { useVariableMutations } from "./use-variable-mutations";

export interface VariableDialogProps {
	serviceId: string;
	/** The service's environment: its other services are what `${{` can reference. */
	environmentId: string;
	/** `"new"` to add, a variable to edit, null when closed. */
	target: Variable | "new" | null;
	existingKeys: readonly string[];
	/** Build-time scopes need a git source: an image is never built here. */
	canBuild: boolean;
	/** Prefills a new variable (an accepted suggestion that needs a value). */
	draft?: { key: string; value: string; isSecret: boolean } | null;
	onClose(): void;
}

// A reference to another service's variable, as typed into a value. It is
// this product's own syntax, not a JavaScript template.
// biome-ignore lint/suspicious/noTemplateCurlyInString: intentional reference syntax
const REFERENCE_EXAMPLE = "${{postgres.NEBULA_PRIVATE_HOST}}";

/**
 * Add or edit one variable. A secret is write-only: editing one shows an
 * empty value field, and leaving it empty keeps the stored value.
 */
export function VariableDialog({
	serviceId,
	environmentId,
	target,
	existingKeys,
	canBuild,
	draft = null,
	onClose,
}: VariableDialogProps) {
	// Keep showing the last target while the dialog animates closed.
	const lastTarget = useRef<Variable | "new">("new");
	if (target !== null) lastTarget.current = target;
	const shown = target ?? lastTarget.current;
	return (
		<Dialog open={target !== null} onOpenChange={(open) => !open && onClose()}>
			<DialogContent>
				<VariableForm
					serviceId={serviceId}
					environmentId={environmentId}
					editing={shown === "new" ? null : shown}
					existingKeys={existingKeys}
					canBuild={canBuild}
					draft={draft}
					onClose={onClose}
				/>
			</DialogContent>
		</Dialog>
	);
}

const SCOPES = [
	{ value: "runtime", label: "Runtime" },
	{ value: "build", label: "Build" },
	{ value: "both", label: "Both" },
] as const;

// What a Dockerfile writes to read a build secret. The `RUN` line is product
// text shown to the user, not code of ours.
const secretMountSnippet = (key: string) =>
	`RUN --mount=type=secret,id=${key},env=${key} <your command>`;
const buildArgSnippet = (key: string) => `ARG ${key}`;

/** The form mounts with the dialog, so every opening starts from its own state. */
function VariableForm({
	serviceId,
	environmentId,
	editing,
	existingKeys,
	canBuild,
	draft,
	onClose,
}: {
	serviceId: string;
	environmentId: string;
	editing: Variable | null;
	existingKeys: readonly string[];
	canBuild: boolean;
	draft: VariableDialogProps["draft"] | undefined;
	onClose(): void;
}) {
	const { save } = useVariableMutations(serviceId);
	const [key, setKey] = useState(editing?.key ?? draft?.key ?? "");
	const [value, setValue] = useState(
		editing && !editing.isSecret ? (editing.value ?? "") : (draft?.value ?? ""),
	);
	const [isSecret, setIsSecret] = useState(
		editing?.isSecret ?? draft?.isSecret ?? false,
	);
	const [scope, setScope] = useState<VariableScope>(
		editing?.scope ?? "runtime",
	);
	const [comment, setComment] = useState(editing?.comment ?? "");
	const [rotateDays, setRotateDays] = useState(
		editing?.rotateEveryDays ? String(editing.rotateEveryDays) : "",
	);

	const trimmedKey = key.trim();
	const duplicate = editing === null && existingKeys.includes(trimmedKey);
	const keyInvalid = trimmedKey !== "" && !ENV_KEY_PATTERN.test(trimmedKey);
	const reserved = isReservedKey(trimmedKey);
	// The first thing wrong with the name; shown only after the field is left
	// or the form is submitted, never while typing.
	const keyError = keyInvalid
		? "Use letters, digits and underscores, not starting with a digit."
		: reserved
			? "PORT and names starting with NEBULA_ are provided by the platform."
			: duplicate
				? `${trimmedKey} already exists; edit it instead.`
				: null;
	const [keyErrorVisible, setKeyErrorVisible] = useState(false);
	const showKeyError = keyErrorVisible && keyError !== null;
	// A reference typed into the name was moved to the value: say why.
	const [movedHint, setMovedHint] = useState(false);
	// What the last picked suggestion filled into the value.
	const [filled, setFilled] = useState<{ value: string; text: string } | null>(
		null,
	);
	// A secret being edited may keep its value by leaving the field empty.
	const keepsSecret = editing?.isSecret === true && isSecret && value === "";
	const candidates = useReferenceCandidates(
		environmentId,
		serviceId,
		value.includes(REFERENCE_OPEN),
	);
	const reference = useReferenceInput<HTMLTextAreaElement>({
		value,
		onValueChange: setValue,
		candidates,
		placement: "inside-bottom",
	});
	const valueField = reference.fieldProps.ref;

	// Moves focus to the value once React has drawn what was just filled in.
	// `select` highlights a filled reference; otherwise the caret goes to the end.
	const focusValue = (select: boolean) =>
		requestAnimationFrame(() => {
			const field = valueField.current;
			if (!field) return;
			field.setSelectionRange(field.value.length, field.value.length);
			field.focus();
			if (select) field.select();
		});

	const changeName = (next: string) => {
		setKeyErrorVisible(false);
		setMovedHint(false);
		if (next.includes(REFERENCE_OPEN)) {
			// A reference is a value, not a name: hand it to the value field and
			// let its autocomplete take over.
			setKey("");
			setValue((current) => current + next);
			setMovedHint(true);
			focusValue(false);
			return;
		}
		setKey(next);
	};

	const pickName = (suggestion: NameSuggestion) => {
		setKeyErrorVisible(false);
		setMovedHint(false);
		setKey(suggestion.name);
		if (suggestion.source === "env-example" && suggestion.secret)
			setIsSecret(true);
		if (suggestion.value === undefined) {
			focusValue(false);
			return;
		}
		setValue(suggestion.value);
		setFilled({
			value: suggestion.value,
			text:
				suggestion.source === "export"
					? `Filled from the ${suggestion.from} service. Edit it, or press Enter to add.`
					: suggestion.source === "shared"
						? `Filled from the ${suggestion.from} shared variables. Edit it, or press Enter to add.`
						: "Proposed by .env.example. Edit it, or press Enter to add.",
		});
		focusValue(true);
	};

	const sources = useNameSources(environmentId, serviceId, editing === null);
	const name = useNameCombobox({
		value: key,
		onValueChange: changeName,
		sources,
		existingKeys,
		enabled: editing === null,
		onPick: pickName,
	});
	const feedback = useDraftValidation(serviceId, trimmedKey, value, isSecret);
	const rotateInvalid = rotateDays !== "" && !/^\d+$/.test(rotateDays.trim());
	const canSave =
		trimmedKey !== "" &&
		!rotateInvalid &&
		feedback?.ok !== false &&
		!save.isPending;
	const secretBuild = isSecret && scope !== "runtime";

	const submit = (event?: FormEvent) => {
		event?.preventDefault();
		setKeyErrorVisible(true);
		if (!canSave) return;
		if (keyError !== null) {
			name.fieldRef.current?.focus();
			return;
		}
		save.mutate(
			{
				key: trimmedKey,
				// Only a user's choice is sent; generators force the flag server-side.
				...(isSecret || editing?.isSecret ? { isSecret } : {}),
				scope,
				comment: comment.trim(),
				...(rotateDays === "" && editing?.rotateEveryDays == null
					? {}
					: { rotateEveryDays: rotateDays === "" ? 0 : Number(rotateDays) }),
				...(keepsSecret ? {} : { value }),
			},
			{
				onSuccess: (data) => {
					const restaged = data?.restaged ?? [];
					toast.success(
						editing ? `Updated ${trimmedKey}` : `Added ${trimmedKey}`,
						restaged.length > 0
							? `Staged until the next deploy. Also restaged: ${restaged.map((s) => s.slug).join(", ")}.`
							: "Staged until the next deploy.",
					);
					for (const warning of data?.warnings ?? []) {
						toast.warning(warning.message);
					}
					onClose();
				},
			},
		);
	};

	// Enter saves, Shift+Enter (or ⌘/Ctrl+Enter) adds a line. The reference list
	// gets Enter first while it is open.
	const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
		reference.fieldProps.onKeyDown(event);
		if (event.defaultPrevented || event.key !== "Enter") return;
		if (event.shiftKey) return;
		event.preventDefault();
		if (event.metaKey || event.ctrlKey) {
			const field = event.currentTarget;
			const start = field.selectionStart;
			const next = `${value.slice(0, start)}\n${value.slice(field.selectionEnd)}`;
			setValue(next);
			requestAnimationFrame(() =>
				field.setSelectionRange(start + 1, start + 1),
			);
			return;
		}
		submit();
	};

	const insertExpression = (expression: string, makesSecret: boolean) => {
		setValue((current) => (current === "" ? expression : current + expression));
		if (makesSecret) setIsSecret(true);
	};

	return (
		<form onSubmit={submit} className="contents">
			<DialogHeader>
				<DialogTitle>
					{editing ? `Edit ${editing.key}` : "New variable"}
				</DialogTitle>
				<DialogDescription>
					References like {REFERENCE_EXAMPLE} resolve to another service in this
					environment when you deploy.
				</DialogDescription>
			</DialogHeader>
			{/* Visible overflow: the reference list hangs inside the value field. */}
			<DialogBody className="overflow-visible">
				<Field>
					<FieldLabel htmlFor="variable-key">Name</FieldLabel>
					<div className="relative">
						<Input
							id="variable-key"
							scale="lg"
							autoFocus={editing === null}
							autoComplete="off"
							spellCheck={false}
							className="font-mono"
							placeholder="e.g. DATABASE_URL"
							value={key}
							readOnly={editing !== null}
							{...(editing === null ? name.fieldProps : {})}
							onBlur={() => {
								name.fieldProps.onBlur();
								setKeyErrorVisible(true);
							}}
							aria-invalid={showKeyError}
						/>
						{name.popup}
					</div>
					{showKeyError ? <FieldError>{keyError}</FieldError> : null}
				</Field>
				<Field>
					<div className="flex items-center justify-between gap-2">
						<FieldLabel htmlFor="variable-value">Value</FieldLabel>
						<GeneratePopover onInsert={insertExpression} />
					</div>
					<div className="relative">
						<Textarea
							id="variable-value"
							autoFocus={editing !== null}
							autoComplete="off"
							spellCheck={false}
							rows={1}
							className={cn(
								"max-h-56 min-h-9 overflow-y-auto overscroll-contain font-mono",
								// Masks a secret as it is typed (where the engine supports it).
								isSecret && value !== "" && "[-webkit-text-security:disc]",
								// A reference filled from a suggestion reads as one.
								filled?.value === value && "text-accent-ink",
							)}
							placeholder={
								editing?.isSecret
									? editing.source === "generated"
										? "Leave empty to keep the generated value"
										: "Leave empty to keep the current secret"
									: "value"
							}
							value={value}
							{...reference.fieldProps}
							onKeyDown={onKeyDown}
						/>
						{reference.popup}
					</div>
					<FieldDescription>
						Type {REFERENCE_OPEN} for references, shared variables and
						functions. Shift+Enter adds a line.
						{editing?.source === "generated" && editing.generated.length > 0
							? ` Generated once as ${editing.generated.map((g) => `${g.fn}(${(g.args ?? []).join(", ")})`).join(" + ")}.`
							: ""}
					</FieldDescription>
					{movedHint ? (
						<FieldDescription role="status">
							References go in the value; the name is the variable your app
							reads.
						</FieldDescription>
					) : null}
					{filled?.value === value ? (
						<FieldDescription role="status">{filled.text}</FieldDescription>
					) : null}
					<DraftFeedback result={feedback} />
				</Field>
				<Field orientation="horizontal">
					<Checkbox
						id="variable-secret"
						checked={isSecret}
						onCheckedChange={(checked) => setIsSecret(checked === true)}
						disabled={editing?.isSecret === true && value === ""}
					/>
					<div className="flex flex-col gap-1">
						<FieldLabel htmlFor="variable-secret">Secret</FieldLabel>
						<FieldDescription>
							Stored encrypted, masked in logs and never shown again after
							saving.
							{editing?.isSecret
								? " To make it plain again, enter a new value."
								: ""}
						</FieldDescription>
					</div>
				</Field>
				{canBuild ? (
					<Field>
						<FieldLabel>Available at</FieldLabel>
						<Segmented
							aria-label="Where the variable is available"
							font="sans"
							density="regular"
							value={scope}
							onValueChange={setScope}
							options={SCOPES}
						/>
						<FieldDescription>
							Runtime reaches the container. Build passes it to the image build
							(a plain value as a build argument, a secret as a BuildKit
							secret); changing a build variable rebuilds the image from the
							first step that uses it.
						</FieldDescription>
						{scope !== "runtime" ? (
							<pre className="surface-well overflow-x-auto rounded-ctl px-2.5 py-2 font-mono text-[11.5px] text-ink-2">
								{secretBuild
									? secretMountSnippet(trimmedKey || "KEY")
									: buildArgSnippet(trimmedKey || "KEY")}
							</pre>
						) : null}
						{secretBuild ? (
							<FieldDescription>
								A secret is never offered as a build argument: it would stay in
								the image history. Mount it in the step that needs it.
							</FieldDescription>
						) : null}
					</Field>
				) : null}
				<Field>
					<FieldLabel htmlFor="variable-comment">Note</FieldLabel>
					<Input
						id="variable-comment"
						autoComplete="off"
						maxLength={1000}
						placeholder="What this is for (optional)"
						value={comment}
						onChange={(event) => setComment(event.target.value)}
					/>
				</Field>
				{isSecret ? (
					<Field>
						<FieldLabel htmlFor="variable-rotate">
							Rotation reminder (days)
						</FieldLabel>
						<Input
							id="variable-rotate"
							inputMode="numeric"
							autoComplete="off"
							className="w-32"
							placeholder="never"
							value={rotateDays}
							onChange={(event) => setRotateDays(event.target.value)}
							aria-invalid={rotateInvalid}
						/>
						<FieldDescription>
							A reminder only: nothing is rotated or deployed automatically.
							{editing?.rotationDue ? (
								<>
									{" "}
									<Badge tone="staged">due</Badge>
								</>
							) : null}
						</FieldDescription>
					</Field>
				) : null}
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
