import { useMutation, useQuery } from "@tanstack/react-query";
import {
	ExternalLinkIcon,
	KeyRoundIcon,
	PlusIcon,
	Trash2Icon,
} from "lucide-react";
import { type FormEvent, useId, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { createDnsCredential } from "#/api/gen/sdk.gen";
import { CredentialHelp } from "#/components/common/credential-help";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Segmented } from "#/components/ui/segmented";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import {
	type DnsProviderSpec,
	legoProvidersQuery,
} from "#/features/dns01/queries";
import {
	credentialProblem,
	isSecretVariable,
	rowsToValues,
	unknownVariables,
	type VariableRow,
} from "#/features/dns01/variables";
import { dns01Guide } from "#/lib/credential-links";

/** The provider select's last choice: any lego provider, by code. */
const OTHER = "__lego__";

let rowCounter = 0;
const newRow = (): VariableRow => ({
	id: `row-${++rowCounter}`,
	name: "",
	value: "",
});

/**
 * A credential for a DNS provider: write-only values, per team or for this
 * server. The curated providers have a form of their own; "Any other lego
 * provider" takes the provider's lego code and name/value pairs, checked against
 * lego's catalog (the control plane refuses a variable lego does not read for
 * the code, so a credential can never set anything else in the proxy's
 * environment).
 */
export function CredentialForm({
	serverId,
	providers,
	onSaved,
	onDone,
}: {
	serverId: string;
	providers: readonly DnsProviderSpec[];
	onSaved(): Promise<void>;
	onDone(): void;
}) {
	const listId = useId();
	const [choice, setChoice] = useState<string>(
		providers[0]?.code ?? "cloudflare",
	);
	const [legoCode, setLegoCode] = useState("");
	const [name, setName] = useState("");
	const [scope, setScope] = useState<"team" | "server">("team");
	const [values, setValues] = useState<Record<string, string>>({});
	const [rows, setRows] = useState<VariableRow[]>([newRow()]);
	const generic = choice === OTHER;

	const lego = useQuery({ ...legoProvidersQuery(), enabled: generic });
	const legoSpecs = ((lego.data as { items?: DnsProviderSpec[] } | undefined)
		?.items ?? []) as DnsProviderSpec[];
	const typed = legoCode.trim().toLowerCase();
	// lego also accepts a few older names (rfc2136, linodev4); a credential
	// uses the canonical code.
	const spec = generic
		? legoSpecs.find((p) => p.code === typed || p.aliases?.includes(typed))
		: providers.find((p) => p.code === choice);
	const code = generic ? (spec?.code ?? typed) : choice;

	const sent = generic
		? rowsToValues(rows)
		: Object.fromEntries(
				Object.entries(values).filter(([, v]) => v.trim() !== ""),
			);
	const problem = spec ? credentialProblem(spec, sent) : null;
	const unknown =
		spec && generic ? unknownVariables(spec, Object.keys(sent)) : [];
	const fieldIds = generic
		? rows.map((r) => `dns-${r.id}`)
		: (spec?.fields ?? []).map((f) => `dns-${f.env}`);

	const create = useMutation({
		mutationFn: async () => {
			await createDnsCredential({
				body: {
					name: name.trim(),
					provider: code,
					...(scope === "server" ? { serverId } : {}),
					values: sent,
				},
			});
		},
		onSuccess: async () => {
			await onSaved();
			toast.success(
				"Credential saved",
				"Its values are sealed and never shown again.",
			);
			onDone();
		},
	});
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (!create.isPending && spec && !problem && unknown.length === 0)
			create.mutate();
	};
	const updateRow = (id: string, patch: Partial<VariableRow>) =>
		setRows(rows.map((r) => (r.id === id ? { ...r, ...patch } : r)));

	return (
		<form
			onSubmit={submit}
			className="surface-well flex flex-col gap-3 rounded-ctl p-3"
		>
			<Select
				value={choice}
				items={[
					...providers.map((p) => ({ value: p.code, label: p.name })),
					{ value: OTHER, label: "Any other lego provider" },
				]}
				onValueChange={(next) => {
					setChoice(next ?? OTHER);
					setValues({});
					setRows([newRow()]);
				}}
			>
				<SelectTrigger size="sm" aria-label="DNS provider" className="w-full">
					<SelectValue placeholder="DNS provider" />
				</SelectTrigger>
				<SelectContent alignItemWithTrigger={false} align="start">
					{providers.map((p) => (
						<SelectItem key={p.code} value={p.code}>
							{p.name}
						</SelectItem>
					))}
					<SelectItem value={OTHER}>Any other lego provider</SelectItem>
				</SelectContent>
			</Select>
			{generic ? (
				<div className="flex flex-col gap-1">
					<label
						htmlFor={`${listId}-code`}
						className="font-medium text-ink-2 text-xs"
					>
						lego provider code
					</label>
					<Input
						id={`${listId}-code`}
						list={`${listId}-codes`}
						placeholder="e.g. pdns, acmedns, cloudns"
						autoComplete="off"
						spellCheck={false}
						className="font-mono"
						value={legoCode}
						onChange={(event) => {
							setLegoCode(event.target.value);
							setRows([newRow()]);
						}}
					/>
					<datalist id={`${listId}-codes`}>
						{legoSpecs.map((p) => (
							<option key={p.code} value={p.code}>
								{p.name}
							</option>
						))}
					</datalist>
					{lego.isPending ? (
						<span className="text-ink-3 text-xs">
							Loading lego's providers…
						</span>
					) : spec ? (
						<span className="flex flex-wrap items-center gap-x-2 text-ink-2 text-xs">
							{spec.name}
							<a
								href={spec.docs}
								target="_blank"
								rel="noopener noreferrer"
								className="inline-flex items-center gap-1 text-accent-ink underline-offset-2 hover:underline"
							>
								lego docs for {spec.code}
								<ExternalLinkIcon aria-hidden className="size-3" />
							</a>
						</span>
					) : legoCode.trim() !== "" ? (
						<span className="text-ink-3 text-xs">
							Not a lego DNS provider code. Start typing to see the{" "}
							{legoSpecs.length} that Traefik's ACME client supports.
						</span>
					) : (
						<span className="text-ink-3 text-xs">
							Traefik's ACME client (lego) supports {legoSpecs.length || "more"}{" "}
							other DNS providers. Pick one by its code.
						</span>
					)}
				</div>
			) : spec?.help ? (
				<p className="text-ink-3 text-xs">{spec.help}</p>
			) : null}
			<Input
				aria-label="Credential name"
				placeholder="Name, e.g. cloudflare-prod"
				autoComplete="off"
				value={name}
				onChange={(event) => setName(event.target.value)}
			/>
			{spec ? (
				<CredentialHelp
					guide={dns01Guide(code, sent, spec.name)}
					fieldIds={fieldIds}
				/>
			) : null}
			{spec && !generic
				? spec.fields.map((f) => (
						<div key={f.env} className="flex flex-col gap-1">
							<label
								htmlFor={`dns-${f.env}`}
								className="font-medium text-ink-2 text-xs"
							>
								{f.label}
								{f.required ? "" : " (optional)"}{" "}
								<span className="font-mono text-ink-4">{f.env}</span>
							</label>
							<Input
								id={`dns-${f.env}`}
								type={f.secret ? "password" : "text"}
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
								value={values[f.env] ?? ""}
								onChange={(event) =>
									setValues({ ...values, [f.env]: event.target.value })
								}
							/>
							{f.help ? (
								<span className="text-ink-3 text-2xs">{f.help}</span>
							) : null}
						</div>
					))
				: null}
			{spec && generic ? (
				<fieldset className="m-0 flex flex-col gap-2 border-0 p-0">
					<legend className="mb-1 p-0 font-medium text-ink-2 text-xs">
						Variables
					</legend>
					{rows.map((row) => (
						<div key={row.id} className="flex items-center gap-2">
							<Input
								aria-label="Variable name"
								list={`${listId}-vars`}
								placeholder="NAME"
								autoComplete="off"
								spellCheck={false}
								className="w-60 font-mono"
								value={row.name}
								onChange={(event) =>
									updateRow(row.id, { name: event.target.value })
								}
							/>
							<Input
								id={`dns-${row.id}`}
								aria-label={`Value of ${row.name || "the variable"}`}
								type={
									isSecretVariable(spec, row.name.trim()) ? "password" : "text"
								}
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
								value={row.value}
								onChange={(event) =>
									updateRow(row.id, { value: event.target.value })
								}
							/>
							<Button
								type="button"
								variant="ghost"
								size="icon-sm"
								aria-label="Remove variable"
								disabled={
									rows.length === 1 && row.name === "" && row.value === ""
								}
								onClick={() =>
									setRows(
										rows.length === 1
											? [newRow()]
											: rows.filter((r) => r.id !== row.id),
									)
								}
							>
								<Trash2Icon aria-hidden />
							</Button>
						</div>
					))}
					<datalist id={`${listId}-vars`}>
						{spec.fields.map((f) => (
							<option key={f.env} value={f.env}>
								{f.label}
							</option>
						))}
					</datalist>
					<div>
						<Button
							type="button"
							variant="secondary"
							size="sm"
							onClick={() => setRows([...rows, newRow()])}
						>
							<PlusIcon aria-hidden />
							Add variable
						</Button>
					</div>
					<details className="text-ink-3 text-xs">
						<summary className="cursor-pointer text-ink-2">
							Variables lego reads for {spec.code}
						</summary>
						<ul className="m-0 mt-1 flex list-none flex-col gap-0.5 p-0">
							{spec.fields.map((f) => (
								<li key={f.env}>
									<span className="font-mono text-ink-2">{f.env}</span>{" "}
									{f.label}
								</li>
							))}
						</ul>
					</details>
					{unknown.length > 0 ? (
						<FieldError>
							{unknown.join(", ")} {unknown.length === 1 ? "is" : "are"} not
							read by lego for {spec.code}.
						</FieldError>
					) : null}
				</fieldset>
			) : null}
			{problem && Object.keys(sent).length > 0 ? (
				<p className="text-ink-3 text-xs">{problem}</p>
			) : null}
			<Segmented
				aria-label="Who can use it"
				font="sans"
				value={scope}
				onValueChange={(next) => setScope(next as "team" | "server")}
				options={[
					{ value: "team", label: "Whole team" },
					{ value: "server", label: "This server only" },
				]}
			/>
			{create.isError ? (
				<FieldError>{errorMessage(create.error)}</FieldError>
			) : null}
			<div className="flex justify-end gap-2">
				<Button type="button" variant="ghost" size="sm" onClick={onDone}>
					Cancel
				</Button>
				<Button
					type="submit"
					size="sm"
					disabled={
						create.isPending ||
						name.trim() === "" ||
						!spec ||
						problem !== null ||
						unknown.length > 0
					}
				>
					{create.isPending ? <Spinner /> : <KeyRoundIcon aria-hidden />}
					Save credential
				</Button>
			</div>
		</form>
	);
}
