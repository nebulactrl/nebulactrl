import { FileTextIcon, PencilIcon, PlusIcon, Trash2Icon } from "lucide-react";
import { type FormEvent, useRef, useState } from "react";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
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
import { Textarea } from "#/components/ui/textarea";
import { type DraftProps, Section } from "./fields";
import {
	byteLength,
	type FileDraft,
	fileErrors,
	MAX_FILE_BYTES,
	MAX_FILES,
	sizeText,
} from "./files";

const EMPTY_FILE: FileDraft = { path: "", mode: "", content: "" };

/** Shown wherever file contents are edited: they are not encrypted. */
function PlainTextWarning() {
	return (
		<Alert variant="warning" role="note">
			<AlertTitle>Stored in plain text</AlertTitle>
			<AlertDescription>
				File contents are saved unencrypted in the service configuration, and
				appear in its change history. Never put passwords or tokens in a file;
				use variables.
			</AlertDescription>
		</Alert>
	);
}

/**
 * Adds or edits one file. The form works on its own copy and only writes back
 * on Save, after the same checks the control plane runs.
 */
function FileDialog({
	open,
	initial,
	others,
	onSave,
	onClose,
}: {
	open: boolean;
	/** The file being edited; null adds a new one. */
	initial: FileDraft | null;
	others: readonly FileDraft[];
	onSave(file: FileDraft): void;
	onClose(): void;
}) {
	return (
		<Dialog open={open} onOpenChange={(next) => !next && onClose()}>
			<DialogContent width={640} top={8}>
				{/* The popup unmounts after it closes, so each opening starts clean. */}
				<FileForm
					initial={initial}
					others={others}
					onSave={onSave}
					onClose={onClose}
				/>
			</DialogContent>
		</Dialog>
	);
}

function FileForm({
	initial,
	others,
	onSave,
	onClose,
}: {
	initial: FileDraft | null;
	others: readonly FileDraft[];
	onSave(file: FileDraft): void;
	onClose(): void;
}) {
	const [file, setFile] = useState<FileDraft>(initial ?? EMPTY_FILE);
	const [tried, setTried] = useState(false);
	const errors = fileErrors(file, others);
	const invalid = Object.keys(errors).length > 0;
	const bytes = byteLength(file.content);

	const submit = (event: FormEvent) => {
		event.preventDefault();
		setTried(true);
		if (invalid) return;
		onSave({ ...file, path: file.path.trim(), mode: file.mode.trim() });
	};

	return (
		<form onSubmit={submit} className="contents">
			<DialogHeader>
				<DialogTitle>{initial ? "Edit file" : "Add a file"}</DialogTitle>
				<DialogDescription>
					Written into the container before it first starts. A service with
					files runs as a single replica.
				</DialogDescription>
			</DialogHeader>
			<DialogBody>
				<div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_120px]">
					<Field>
						<FieldLabel htmlFor="file-path">Path in the container</FieldLabel>
						<Input
							id="file-path"
							scale="lg"
							autoFocus
							autoComplete="off"
							spellCheck={false}
							className="font-mono"
							placeholder="/etc/app/config.toml"
							value={file.path}
							aria-invalid={tried && errors.path !== undefined}
							onChange={(event) =>
								setFile({ ...file, path: event.target.value })
							}
						/>
						{tried ? <FieldError>{errors.path}</FieldError> : null}
					</Field>
					<Field>
						<FieldLabel htmlFor="file-mode">Mode</FieldLabel>
						<Input
							id="file-mode"
							scale="lg"
							autoComplete="off"
							spellCheck={false}
							className="font-mono"
							placeholder="0644"
							value={file.mode}
							aria-invalid={tried && errors.mode !== undefined}
							onChange={(event) =>
								setFile({ ...file, mode: event.target.value })
							}
						/>
						{tried ? <FieldError>{errors.mode}</FieldError> : null}
					</Field>
				</div>
				<Field>
					<FieldLabel htmlFor="file-content">Contents</FieldLabel>
					<Textarea
						id="file-content"
						spellCheck={false}
						autoComplete="off"
						wrap="off"
						rows={14}
						className="max-h-[46vh] resize-y overflow-auto whitespace-pre font-mono text-[12px]"
						value={file.content}
						aria-invalid={errors.content !== undefined}
						onChange={(event) =>
							setFile({ ...file, content: event.target.value })
						}
					/>
					<FieldDescription className="font-mono tabular-nums">
						{sizeText(bytes)} of {MAX_FILE_BYTES / 1024} KiB
					</FieldDescription>
					<FieldError>{errors.content}</FieldError>
				</Field>
				<PlainTextWarning />
			</DialogBody>
			<DialogFooter>
				<Button type="button" variant="ghost" size="lg" onClick={onClose}>
					Cancel
				</Button>
				<Button type="submit" size="lg" disabled={tried && invalid}>
					{initial ? "Update file" : "Add file"}
				</Button>
			</DialogFooter>
		</form>
	);
}

/**
 * The files mounted into the container: path, mode and size per file, with
 * add, edit and remove. Changes stay in the draft until the form is saved,
 * like every other setting.
 */
export function FilesSection({ draft, errors, set }: DraftProps) {
	const [editing, setEditing] = useState<
		{ index: number | null; file: FileDraft | null } | undefined
	>(undefined);
	const files = draft.files;
	const full = files.length >= MAX_FILES;
	// Keep what was being edited while the dialog animates closed.
	const shown = useRef(editing);
	if (editing !== undefined) shown.current = editing;

	const save = (file: FileDraft) => {
		const index = editing?.index ?? null;
		set(
			"files",
			index === null
				? [...files, file]
				: files.map((current, at) => (at === index ? file : current)),
		);
		setEditing(undefined);
	};

	return (
		<Section
			title="Files"
			description={`Config files written into the container before it starts. At most ${MAX_FILES} files of ${MAX_FILE_BYTES / 1024} KiB each.`}
			aside={
				<span className="font-mono tabular-nums">
					{files.length}/{MAX_FILES}
				</span>
			}
		>
			{files.length === 0 ? (
				<p className="py-1.5 text-[13px] text-ink-3">
					No files. Add one to mount a config file at a fixed path.
				</p>
			) : (
				<ul className="m-0 flex list-none flex-col p-0">
					{files.map((file, index) => (
						<li
							key={file.path}
							className="flex items-center gap-3 border-line-faint border-b py-2 last:border-b-0"
						>
							<FileTextIcon
								aria-hidden
								className="size-4 shrink-0 text-ink-3"
							/>
							<div className="min-w-0 flex-1">
								<div className="truncate font-mono text-[13px] text-ink-1">
									{file.path}
								</div>
								<div className="font-mono text-2xs text-ink-3 tabular-nums">
									mode {file.mode || "0644"} ·{" "}
									{sizeText(byteLength(file.content))}
								</div>
							</div>
							<Button
								variant="ghost"
								size="icon-sm"
								aria-label={`Edit ${file.path}`}
								onClick={() => setEditing({ index, file })}
							>
								<PencilIcon aria-hidden />
							</Button>
							<Button
								variant="ghost"
								size="icon-sm"
								aria-label={`Remove ${file.path}`}
								onClick={() =>
									set(
										"files",
										files.filter((_, at) => at !== index),
									)
								}
							>
								<Trash2Icon aria-hidden />
							</Button>
						</li>
					))}
				</ul>
			)}
			<FieldError>{errors.files}</FieldError>
			<div className="flex flex-col gap-2 py-1.5">
				<div>
					<Button
						variant="secondary"
						size="md"
						disabled={full}
						onClick={() => setEditing({ index: null, file: null })}
					>
						<PlusIcon aria-hidden />
						Add file
					</Button>
				</div>
				<p className="type-secondary">
					Contents are stored in plain text. Never put secrets in a file.
				</p>
			</div>
			<FileDialog
				open={editing !== undefined}
				initial={shown.current?.file ?? null}
				others={files.filter((_, at) => at !== shown.current?.index)}
				onSave={save}
				onClose={() => setEditing(undefined)}
			/>
		</Section>
	);
}
