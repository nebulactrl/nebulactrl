import type { ReactNode } from "react";
import type { LogDrainView } from "#/api/gen/types.gen";
import { Button } from "#/components/ui/button";
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import type { SetDraft } from "./kind-fields";
import { credentialFields, type Draft, newRow, type SecretName } from "./model";
import { RowsEditor } from "./rows-editor";

/**
 * A write-only credential. Stored, it shows a mask in the empty field and
 * keeps the value unless something is typed (replace) or Remove is pressed
 * (clear); the value itself never comes back from the server.
 */
function SecretField({
	id,
	label,
	value,
	onChange,
	stored,
	cleared,
	onClear,
	onKeep,
	description,
	secret = true,
	placeholder,
}: {
	id: string;
	label: string;
	value: string;
	onChange(next: string): void;
	/** The mask of the stored credential, when there is one. */
	stored: string | undefined;
	cleared: boolean;
	onClear(): void;
	onKeep(): void;
	description?: ReactNode;
	secret?: boolean;
	placeholder?: string;
}) {
	if (cleared) {
		return (
			<Field>
				<FieldLabel htmlFor={id}>{label}</FieldLabel>
				<div className="surface-well flex items-center justify-between gap-2 rounded-ctl px-2.5 py-1.5 text-ink-2 text-xs">
					<span>Removed when you save.</span>
					<Button type="button" variant="ghost" size="2xs" onClick={onKeep}>
						Keep it
					</Button>
				</div>
			</Field>
		);
	}
	return (
		<Field>
			<div className="flex items-center justify-between gap-2">
				<FieldLabel htmlFor={id}>
					{label}
					{stored ? " (leave empty to keep)" : ""}
				</FieldLabel>
				{stored ? (
					<Button type="button" variant="ghost" size="2xs" onClick={onClear}>
						Remove
					</Button>
				) : null}
			</div>
			<Input
				id={id}
				type={secret ? "password" : "text"}
				autoComplete={secret ? "new-password" : "off"}
				spellCheck={false}
				className="font-mono"
				placeholder={stored ? stored : placeholder}
				value={value}
				onChange={(e) => onChange(e.target.value)}
			/>
			{description ? <FieldDescription>{description}</FieldDescription> : null}
		</Field>
	);
}

function StoredHeaders({
	draft,
	set,
	names,
}: {
	draft: Draft;
	set: SetDraft;
	names: readonly string[];
}) {
	if (draft.headersMode === "keep") {
		return (
			<div className="surface-well flex flex-col gap-2 rounded-card p-3 text-xs">
				<span className="text-ink-2">
					Stored headers:{" "}
					<span className="font-mono text-ink-1">{names.join(", ")}</span>.
					Their values are never shown again.
				</span>
				<span className="flex flex-wrap gap-2">
					<Button
						type="button"
						variant="secondary"
						size="sm"
						onClick={() => {
							set("headersMode", "replace");
							set("headers", [newRow()]);
						}}
					>
						Replace headers
					</Button>
					<Button
						type="button"
						variant="ghost"
						size="sm"
						onClick={() => set("headersMode", "clear")}
					>
						Remove all headers
					</Button>
				</span>
			</div>
		);
	}
	if (draft.headersMode === "clear") {
		return (
			<div className="surface-well flex items-center justify-between gap-2 rounded-ctl px-2.5 py-1.5 text-ink-2 text-xs">
				<span>All stored headers are removed when you save.</span>
				<Button
					type="button"
					variant="ghost"
					size="2xs"
					onClick={() => set("headersMode", "keep")}
				>
					Keep them
				</Button>
			</div>
		);
	}
	return (
		<>
			<RowsEditor
				label="Headers"
				secret
				rows={draft.headers}
				onChange={(rows) => set("headers", rows)}
				namePlaceholder="DD-API-KEY"
				valuePlaceholder="value"
				addLabel="Add header"
			/>
			<FieldDescription>
				Saving replaces every stored header ({names.join(", ")}), so enter all
				of them again.{" "}
				<Button
					type="button"
					variant="link"
					size="2xs"
					onClick={() => set("headersMode", "keep")}
				>
					Keep the stored headers
				</Button>
			</FieldDescription>
		</>
	);
}

const AUTH_HELP = {
	loki: "Basic auth (a user name and a password or API key, as Grafana Cloud uses) or a bearer token. Use whichever your Loki asks for.",
	otlp: "A bearer token, extra headers, or both.",
	http: "Whatever the receiver asks for: a bearer token, basic auth, or an API key header.",
	syslog: "",
} as const;

/**
 * The credentials of the kind: basic auth, a bearer token and extra request
 * headers (an API key header, for example). All of them are write-only and
 * encrypted at rest; an edit shows what is set as a mask and a header's name.
 */
export function CredentialFields({
	draft,
	set,
	drain,
}: {
	draft: Draft;
	set: SetDraft;
	drain: LogDrainView | null;
}) {
	const fields = credentialFields(draft.kind);
	if (!fields.basic && !fields.token && !fields.headers) return null;
	const stored = drain?.secrets ?? {};
	const names = drain?.headerNames ?? [];
	const cleared = (name: SecretName) => draft.clear.includes(name);
	const clear = (name: SecretName) => set("clear", [...draft.clear, name]);
	const keep = (name: SecretName) =>
		set(
			"clear",
			draft.clear.filter((n) => n !== name),
		);
	return (
		<fieldset className="m-0 flex min-w-0 flex-col gap-3 border-0 p-0">
			<legend className="type-label mb-1">Authentication</legend>
			<p className="m-0 text-ink-3 text-xs leading-snug">
				{AUTH_HELP[draft.kind]} Credentials are stored encrypted and never shown
				again.
			</p>
			{fields.basic ? (
				<div className="grid gap-3 sm:grid-cols-2">
					<SecretField
						id="drain-username"
						label="User name"
						secret={false}
						value={draft.username}
						onChange={(v) => set("username", v)}
						stored={stored.username}
						cleared={cleared("username")}
						onClear={() => clear("username")}
						onKeep={() => keep("username")}
					/>
					<SecretField
						id="drain-password"
						label="Password"
						value={draft.password}
						onChange={(v) => set("password", v)}
						stored={stored.password}
						cleared={cleared("password")}
						onClear={() => clear("password")}
						onKeep={() => keep("password")}
					/>
				</div>
			) : null}
			{fields.token ? (
				<SecretField
					id="drain-token"
					label="Bearer token"
					value={draft.token}
					onChange={(v) => set("token", v)}
					stored={stored.token}
					cleared={cleared("token")}
					onClear={() => clear("token")}
					onKeep={() => keep("token")}
					description="Sent as Authorization: Bearer <token>."
				/>
			) : null}
			{fields.headers ? (
				<Field>
					<FieldLabel>Extra headers (optional)</FieldLabel>
					{names.length > 0 ? (
						<StoredHeaders draft={draft} set={set} names={names} />
					) : (
						<>
							<RowsEditor
								label="Headers"
								secret
								rows={draft.headers}
								onChange={(rows) => set("headers", rows)}
								namePlaceholder="DD-API-KEY"
								valuePlaceholder="value"
								addLabel="Add header"
							/>
							<FieldDescription>
								Header values are secret: only their names are shown after
								saving.
							</FieldDescription>
						</>
					)}
				</Field>
			) : null}
		</fieldset>
	);
}
