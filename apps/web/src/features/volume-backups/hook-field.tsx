import { useId } from "react";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Segmented } from "#/components/ui/segmented";
import { SettingRow } from "#/features/service-settings/fields";
import { HOOK_TEMPLATES, type HookDraft } from "./model";

export interface HookFieldProps {
	/** "Before backup" or "After backup". */
	label: string;
	description: string;
	draft: HookDraft;
	onChange: (next: HookDraft) => void;
	error?: string | undefined;
	timeoutError?: string | undefined;
	/** Offer the copy-paste templates (before the backup, where a consistent file is made). */
	templates?: boolean;
}

/**
 * One command that runs inside the service's container: the command line, who
 * runs it, how long it may take and what a failure does. The templates only
 * fill the command line; nothing runs until a backup does.
 */
export function HookField({
	label,
	description,
	draft,
	onChange,
	error,
	timeoutError,
	templates = false,
}: HookFieldProps) {
	const id = useId();
	const has = draft.command.trim() !== "";
	const set = (patch: Partial<HookDraft>) => onChange({ ...draft, ...patch });
	return (
		<SettingRow label={label} description={description} htmlFor={`${id}-cmd`}>
			<Input
				id={`${id}-cmd`}
				className="font-mono"
				autoComplete="off"
				spellCheck={false}
				placeholder="No command"
				aria-invalid={error ? true : undefined}
				value={draft.command}
				onChange={(e) => set({ command: e.target.value })}
			/>
			{error ? <FieldError>{error}</FieldError> : null}
			{templates ? (
				<div className="flex flex-col gap-1">
					<span className="flex flex-wrap items-center gap-1.5">
						<span className="text-ink-4 text-xs">Templates</span>
						{HOOK_TEMPLATES.map((t) => (
							<Button
								key={t.id}
								type="button"
								variant="secondary"
								size="2xs"
								title={`${t.command}\n${t.note}`}
								onClick={() => set({ command: t.command })}
							>
								{t.label}
							</Button>
						))}
					</span>
					<span className="text-ink-4 text-xs">
						A template only fills the command line; edit paths to match your
						volume. It runs when a backup does, not before.
					</span>
				</div>
			) : null}
			{has ? (
				<div className="grid grid-cols-1 gap-2 @md/section:grid-cols-[1fr_96px]">
					<div className="flex flex-col gap-1">
						<label htmlFor={`${id}-user`} className="type-label">
							Run as user
						</label>
						<Input
							id={`${id}-user`}
							className="font-mono"
							autoComplete="off"
							spellCheck={false}
							placeholder="Container default"
							value={draft.user}
							onChange={(e) => set({ user: e.target.value })}
						/>
					</div>
					<div className="flex flex-col gap-1">
						<label htmlFor={`${id}-timeout`} className="type-label">
							Timeout (s)
						</label>
						<Input
							id={`${id}-timeout`}
							inputMode="numeric"
							className="font-mono"
							aria-invalid={timeoutError ? true : undefined}
							value={draft.timeout}
							onChange={(e) => set({ timeout: e.target.value })}
						/>
					</div>
				</div>
			) : null}
			{timeoutError ? <FieldError>{timeoutError}</FieldError> : null}
			{has ? (
				<div className="flex flex-wrap items-center gap-2">
					<span className="type-label">If it fails</span>
					<Segmented<"stop" | "continue">
						aria-label={`${label}: if it fails`}
						font="sans"
						value={draft.continueOnError ? "continue" : "stop"}
						onValueChange={(v) => set({ continueOnError: v === "continue" })}
						options={[
							{ value: "stop", label: "Stop the backup" },
							{ value: "continue", label: "Continue anyway" },
						]}
					/>
				</div>
			) : null}
		</SettingRow>
	);
}
