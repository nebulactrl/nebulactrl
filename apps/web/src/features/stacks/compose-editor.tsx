import { UploadIcon } from "lucide-react";
import { useId, useRef, useState } from "react";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Textarea } from "#/components/ui/textarea";
import { StackCheckPanel } from "./check-panel";
import type { StackCheckState } from "./model";

/** The API accepts up to 256 KB per text field. */
const COMPOSE_MAX_BYTES = 262144;

const PLACEHOLDER = `services:
  web:
    image: nginx:1.27
    ports:
      - "80"
  worker:
    image: busybox
    command: ["sleep", "infinity"]`;

/**
 * The compose editor: a monospace surface (the same one the Compose import
 * uses) with an Upload button, and the live check under it. The check state
 * comes from `useStackCheck`; the editor only draws it.
 */
export function ComposeEditor({
	value,
	onChange,
	state,
	serverName,
	label = "docker-compose.yml",
	autoFocus = false,
	minRows = 14,
	error,
	disabled = false,
	id,
}: {
	value: string;
	onChange(next: string): void;
	state: StackCheckState;
	serverName?: string | undefined;
	label?: string;
	autoFocus?: boolean;
	minRows?: number;
	/** A problem the form found (size, empty), shown under the field. */
	error?: string | undefined;
	disabled?: boolean;
	/** The textarea's id, for a settings search to jump to. */
	id?: string;
}) {
	const generated = useId();
	const textareaId = id ?? `${generated}-compose`;
	const input = useRef<HTMLInputElement>(null);
	const [problem, setProblem] = useState<string | null>(null);
	const size = new Blob([value]).size;
	const tooBig = size > COMPOSE_MAX_BYTES;

	const take = async (file: File | undefined) => {
		if (!file) return;
		if (file.size > COMPOSE_MAX_BYTES) {
			setProblem(`${file.name} is larger than 256 KB and was not added.`);
			return;
		}
		setProblem(null);
		onChange(await file.text());
	};

	return (
		<div className="flex flex-col gap-3">
			<div className="flex flex-col gap-1.5">
				<div className="flex items-center gap-2">
					<label
						htmlFor={textareaId}
						className="font-medium text-[13px] text-ink-1"
					>
						{label}
					</label>
					<Button
						variant="ghost"
						size="xs"
						className="ml-auto"
						disabled={disabled}
						onClick={() => input.current?.click()}
					>
						<UploadIcon aria-hidden />
						Upload file
					</Button>
					<input
						ref={input}
						type="file"
						accept=".yml,.yaml,text/yaml,text/plain"
						className="sr-only"
						tabIndex={-1}
						aria-hidden
						onChange={(event) => {
							void take(event.target.files?.[0]);
							event.target.value = "";
						}}
					/>
				</div>
				<Textarea
					id={textareaId}
					autoFocus={autoFocus}
					spellCheck={false}
					autoComplete="off"
					disabled={disabled}
					rows={minRows}
					placeholder={PLACEHOLDER}
					value={value}
					aria-invalid={tooBig || error !== undefined || undefined}
					onChange={(event) => {
						setProblem(null);
						onChange(event.target.value);
					}}
					className="max-h-[420px] min-h-64 overflow-y-auto overscroll-contain whitespace-pre font-mono text-xs leading-[1.5]"
				/>
				{tooBig ? (
					<FieldError>The Compose file is larger than 256 KB.</FieldError>
				) : null}
				{problem ? <FieldError>{problem}</FieldError> : null}
				{error ? <FieldError>{error}</FieldError> : null}
			</div>
			<StackCheckPanel state={state} serverName={serverName} />
		</div>
	);
}
