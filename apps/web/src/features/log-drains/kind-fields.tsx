import { useState } from "react";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Segmented } from "#/components/ui/segmented";
import {
	applyPreset,
	type Draft,
	HTTP_PRESETS,
	type HttpFormat,
	type HttpMethod,
	type SyslogProtocol,
} from "./model";
import { RowsEditor } from "./rows-editor";

export type SetDraft = <K extends keyof Draft>(key: K, value: Draft[K]) => void;

interface FieldsProps {
	draft: Draft;
	set: SetDraft;
}

function TextField({
	id,
	label,
	value,
	onChange,
	placeholder,
	description,
	mono = true,
}: {
	id: string;
	label: string;
	value: string;
	onChange(next: string): void;
	placeholder?: string;
	description?: string;
	mono?: boolean;
}) {
	return (
		<Field>
			<FieldLabel htmlFor={id}>{label}</FieldLabel>
			<Input
				id={id}
				autoComplete="off"
				spellCheck={false}
				className={mono ? "font-mono" : undefined}
				{...(placeholder ? { placeholder } : {})}
				value={value}
				onChange={(e) => onChange(e.target.value)}
			/>
			{description ? <FieldDescription>{description}</FieldDescription> : null}
		</Field>
	);
}

function LokiFields({ draft, set }: FieldsProps) {
	return (
		<>
			<TextField
				id="drain-loki-url"
				label="Push URL"
				value={draft.lokiUrl}
				onChange={(v) => set("lokiUrl", v)}
				placeholder="https://logs-prod.grafana.net/loki/api/v1/push"
				description="The full push URL, ending in /loki/api/v1/push."
			/>
			<TextField
				id="drain-loki-tenant"
				label="Tenant ID (optional)"
				value={draft.lokiTenant}
				onChange={(v) => set("lokiTenant", v)}
				placeholder="Sent as X-Scope-OrgID"
				description="Only for a Loki with several tenants."
			/>
			<Field>
				<FieldLabel>Labels (optional)</FieldLabel>
				<RowsEditor
					label="Labels"
					rows={draft.lokiLabels}
					onChange={(rows) => set("lokiLabels", rows)}
					namePlaceholder="label"
					addLabel="Add label"
				/>
				<FieldDescription>
					Added to every stream this drain sends.
				</FieldDescription>
			</Field>
		</>
	);
}

function OtlpFields({ draft, set }: FieldsProps) {
	return (
		<>
			<TextField
				id="drain-otlp-url"
				label="Logs URL"
				value={draft.otlpUrl}
				onChange={(v) => set("otlpUrl", v)}
				placeholder="https://otel.example.com/v1/logs"
				description="The full OTLP/HTTP logs URL, ending in /v1/logs."
			/>
			<Field>
				<FieldLabel>Resource attributes (optional)</FieldLabel>
				<RowsEditor
					label="Resource attributes"
					rows={draft.otlpAttributes}
					onChange={(rows) => set("otlpAttributes", rows)}
					namePlaceholder="deployment.environment"
					addLabel="Add attribute"
				/>
				<FieldDescription>
					Attached to the resource of every batch, for example the environment
					or region.
				</FieldDescription>
			</Field>
		</>
	);
}

const PROTOCOLS: { value: SyslogProtocol; label: string }[] = [
	{ value: "udp", label: "UDP" },
	{ value: "tcp", label: "TCP" },
	{ value: "tcp+tls", label: "TCP + TLS" },
];

const PROTOCOL_HELP: Record<SyslogProtocol, string> = {
	udp: "Fast and unacknowledged: a lost datagram is not retried.",
	tcp: "Reliable, but not encrypted: lines cross the network in clear text.",
	"tcp+tls": "Reliable and encrypted. Use this over the internet.",
};

function SyslogFields({ draft, set }: FieldsProps) {
	return (
		<>
			<TextField
				id="drain-syslog-address"
				label="Address"
				value={draft.syslogAddress}
				onChange={(v) => set("syslogAddress", v)}
				placeholder="logs.example.com:6514"
				description="host:port of the syslog receiver."
			/>
			<Field>
				<FieldLabel id="drain-syslog-protocol">Protocol</FieldLabel>
				<div>
					<Segmented<SyslogProtocol>
						aria-label="Syslog protocol"
						font="sans"
						density="regular"
						value={draft.syslogProtocol}
						onValueChange={(v) => set("syslogProtocol", v)}
						options={PROTOCOLS}
					/>
				</div>
				<FieldDescription>
					{PROTOCOL_HELP[draft.syslogProtocol]}
				</FieldDescription>
			</Field>
			<div className="grid gap-3 sm:grid-cols-2">
				<TextField
					id="drain-syslog-app"
					label="App name (optional)"
					value={draft.syslogAppName}
					onChange={(v) => set("syslogAppName", v)}
					placeholder="nebulactrl"
				/>
				<Field>
					<FieldLabel htmlFor="drain-syslog-facility">
						Facility (optional)
					</FieldLabel>
					<Input
						id="drain-syslog-facility"
						type="number"
						min={0}
						max={23}
						inputMode="numeric"
						className="font-mono"
						placeholder="0 to 23"
						value={draft.syslogFacility}
						onChange={(e) => set("syslogFacility", e.target.value)}
					/>
				</Field>
			</div>
			{draft.syslogProtocol === "tcp+tls" ? (
				<>
					<TextField
						id="drain-syslog-sni"
						label="TLS server name (optional)"
						value={draft.syslogTlsServerName}
						onChange={(v) => set("syslogTlsServerName", v)}
						placeholder="Defaults to the host in the address"
						description="The name the receiver's certificate must carry."
					/>
					<CheckLabel
						checked={draft.syslogTlsInsecure}
						onCheckedChange={(v) => set("syslogTlsInsecure", v)}
					>
						Accept any certificate (the connection stays encrypted, but the
						receiver's identity is not checked)
					</CheckLabel>
					{draft.syslogTlsInsecure ? (
						<Alert variant="warning">
							<AlertTitle>Identity not checked</AlertTitle>
							<AlertDescription>
								Anyone who can intercept the connection could read your logs.
								Use this only for a receiver with a self-signed certificate on a
								network you trust.
							</AlertDescription>
						</Alert>
					) : null}
				</>
			) : null}
		</>
	);
}

const METHODS: { value: HttpMethod; label: string }[] = [
	{ value: "POST", label: "POST" },
	{ value: "PUT", label: "PUT" },
];

const FORMATS: { value: HttpFormat; label: string }[] = [
	{ value: "json_array", label: "JSON array" },
	{ value: "ndjson", label: "One JSON per line" },
];

function HttpFields({
	draft,
	set,
	onDraft,
}: FieldsProps & { onDraft(next: Draft): void }) {
	// Applying a preset replaces several fields at once, so it goes through
	// the whole draft rather than one key at a time.
	const [presetId, setPresetId] = useState<string | null>(null);
	const preset = HTTP_PRESETS.find((p) => p.id === presetId);
	return (
		<>
			<Field>
				<FieldLabel id="drain-http-presets">Start from</FieldLabel>
				<fieldset
					aria-labelledby="drain-http-presets"
					className="m-0 flex min-w-0 flex-wrap gap-2 border-0 p-0"
				>
					{HTTP_PRESETS.map((p) => (
						<Button
							key={p.id}
							type="button"
							variant="secondary"
							size="sm"
							aria-pressed={presetId === p.id}
							onClick={() => {
								setPresetId(p.id);
								onDraft(applyPreset(draft, p));
							}}
						>
							{p.label}
						</Button>
					))}
				</fieldset>
				<FieldDescription>
					{preset
						? preset.hint
						: "A preset fills the URL and the field names. It never fills a key or token."}
				</FieldDescription>
			</Field>
			<TextField
				id="drain-http-url"
				label="URL"
				value={draft.httpUrl}
				onChange={(v) => set("httpUrl", v)}
				placeholder="https://logs.example.com/ingest"
			/>
			<div className="grid gap-3 sm:grid-cols-2">
				<Field>
					<FieldLabel id="drain-http-method">Method</FieldLabel>
					<div>
						<Segmented<HttpMethod>
							aria-label="HTTP method"
							density="regular"
							value={draft.httpMethod}
							onValueChange={(v) => set("httpMethod", v)}
							options={METHODS}
						/>
					</div>
				</Field>
				<Field>
					<FieldLabel id="drain-http-format">Body</FieldLabel>
					<div>
						<Segmented<HttpFormat>
							aria-label="Body format"
							font="sans"
							density="regular"
							value={draft.httpFormat}
							onValueChange={(v) => set("httpFormat", v)}
							options={FORMATS}
						/>
					</div>
				</Field>
			</div>
			<div className="grid gap-3 sm:grid-cols-2">
				<TextField
					id="drain-http-ts"
					label="Time field (optional)"
					value={draft.httpTimestampField}
					onChange={(v) => set("httpTimestampField", v)}
					placeholder="timestamp"
				/>
				<TextField
					id="drain-http-message"
					label="Message field (optional)"
					value={draft.httpMessageField}
					onChange={(v) => set("httpMessageField", v)}
					placeholder="message"
				/>
			</div>
			<Field>
				<FieldLabel>Extra fields (optional)</FieldLabel>
				<RowsEditor
					label="Extra fields"
					rows={draft.httpExtraFields}
					onChange={(rows) => set("httpExtraFields", rows)}
					namePlaceholder="ddsource"
					addLabel="Add field"
				/>
				<FieldDescription>
					Added to every line this drain sends.
				</FieldDescription>
			</Field>
		</>
	);
}

/** The fields of the drain's kind: where it ships to and how it is formatted. */
export function KindFields({
	draft,
	set,
	onDraft,
}: FieldsProps & { onDraft(next: Draft): void }) {
	switch (draft.kind) {
		case "loki":
			return <LokiFields draft={draft} set={set} />;
		case "otlp":
			return <OtlpFields draft={draft} set={set} />;
		case "syslog":
			return <SyslogFields draft={draft} set={set} />;
		case "http":
			return <HttpFields draft={draft} set={set} onDraft={onDraft} />;
	}
}
