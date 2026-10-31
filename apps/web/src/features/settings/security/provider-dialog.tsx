import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLinkIcon } from "lucide-react";
import { type FormEvent, useEffect, useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	getSystemOptions,
	listAuthProvidersQueryKey,
} from "#/api/gen/@tanstack/react-query.gen";
import {
	createAuthProvider,
	testAuthProvider,
	updateAuthProvider,
} from "#/api/gen/sdk.gen";
import type { ProviderBody } from "#/api/gen/types.gen";
import { CopyButton } from "#/components/common/copy-button";
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
import { OptionCard, OptionCardGroup } from "#/components/ui/option-card";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import {
	EMPTY_PROVIDER_FORM,
	expectedRedirectUri,
	formState,
	newProviderId,
	type ProviderFormState,
	providerBody,
	providerProblem,
} from "./provider-form";
import {
	applyPreset,
	consoleUrlFor,
	firstPlaceholderRange,
	issuerParts,
	issuerWarning,
	PRESETS,
	type PresetId,
	type ProviderPreset,
	placeholderHints,
	placeholdersIn,
	presetById,
	presetForProvider,
} from "./provider-presets";

const LINK = "text-accent-ink underline underline-offset-2";

function SwitchRow({
	id,
	label,
	description,
	checked,
	onChange,
}: {
	id: string;
	label: string;
	description: string;
	checked: boolean;
	onChange: (checked: boolean) => void;
}) {
	return (
		<div className="flex items-start justify-between gap-4">
			<div className="flex min-w-0 flex-col gap-0.5">
				<label htmlFor={id} className="font-medium text-ink-1 text-sm">
					{label}
				</label>
				<span className="text-pretty text-ink-3 text-xs leading-relaxed">
					{description}
				</span>
			</div>
			<Switch id={id} checked={checked} onCheckedChange={onChange} />
		</div>
	);
}

/** An issuer pattern with its `{placeholders}` highlighted. */
function IssuerPattern({ text }: { text: string }) {
	return (
		<code className="break-all font-mono text-ink-2 text-xs">
			{issuerParts(text).map((part) =>
				part.placeholder ? (
					<mark
						key={part.start}
						className="rounded-sm bg-accent-wash px-0.5 text-accent-ink"
					>
						{part.text}
					</mark>
				) : (
					<span key={part.start}>{part.text}</span>
				),
			)}
		</code>
	);
}

function RedirectUri({
	uri,
	field,
	pending,
	derived,
}: {
	uri: string;
	/** The provider's name for the field that takes it. */
	field: string;
	/** True while the provider is not saved: its ID is already reserved. */
	pending: boolean;
	/** True when the control plane has no public URL, so the address is the one this page is on. */
	derived: boolean;
}) {
	return (
		<Field>
			<FieldLabel>Redirect URI</FieldLabel>
			<div className="surface-well flex items-center gap-3 rounded-ctl py-2 pr-2 pl-3">
				<code className="min-w-0 flex-1 select-all break-all font-mono text-ink-1 text-xs leading-[1.7]">
					{uri}
				</code>
				<CopyButton variant="chip" value={uri} className="shrink-0" />
			</div>
			<FieldDescription>
				Register it at the provider in the field <strong>{field}</strong>.
				{pending
					? " This provider's ID is reserved now, so the address will not change when you save."
					: null}
				{derived
					? " NEBULACTRL_PUBLIC_URL is not set, so this is the address you are on; set it so sign-ins always use one address."
					: null}
			</FieldDescription>
		</Field>
	);
}

/** What to create at the provider, with the links to do it. */
function PresetGuide({
	preset,
	issuer,
}: {
	preset: ProviderPreset;
	issuer: string;
}) {
	const consoleUrl = consoleUrlFor(preset, issuer);
	return (
		<div className="surface-well flex flex-col gap-2 rounded-ctl px-3.5 py-3">
			<p className="text-pretty text-ink-2 text-xs leading-relaxed">
				{preset.note}
			</p>
			<p className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
				{consoleUrl ? (
					<a
						className={`${LINK} inline-flex items-center gap-1`}
						href={consoleUrl}
						target="_blank"
						rel="noreferrer noopener"
					>
						Open {preset.label} to create the client
						<ExternalLinkIcon aria-hidden="true" className="size-3" />
					</a>
				) : null}
				<a
					className={`${LINK} inline-flex items-center gap-1`}
					href={preset.docsUrl}
					target="_blank"
					rel="noreferrer noopener"
				>
					{preset.label} documentation
					<ExternalLinkIcon aria-hidden="true" className="size-3" />
				</a>
			</p>
		</div>
	);
}

/**
 * Add or change a single-sign-on provider. The secret is write-only: editing
 * with the field empty keeps the stored one. A new provider is given its ID
 * here, before saving, so the dialog can show the exact redirect URI to
 * register at the identity provider first.
 */
export function ProviderDialog({
	open,
	onOpenChange,
	provider,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	/** The provider being edited; absent for a new one. */
	provider?: ProviderBody | undefined;
}) {
	const queryClient = useQueryClient();
	const editing = provider !== undefined;
	const system = useQuery({
		...getSystemOptions(),
		refetchInterval: false,
		staleTime: 60_000,
	});
	const [form, setForm] = useState<ProviderFormState>(EMPTY_PROVIDER_FORM);
	const [presetId, setPresetId] = useState<PresetId>("oidc");
	const [providerId, setProviderId] = useState(newProviderId);
	const [created, setCreated] = useState<ProviderBody | null>(null);
	const [touched, setTouched] = useState(false);
	const issuerRef = useRef<HTMLInputElement>(null);
	useEffect(() => {
		if (!open) return;
		setForm(provider ? formState(provider) : EMPTY_PROVIDER_FORM);
		setPresetId("oidc");
		setProviderId(newProviderId());
		setCreated(null);
		setTouched(false);
	}, [open, provider]);

	const preset = editing
		? presetForProvider(provider.kind, provider.issuerUrl)
		: presetById(presetId);

	const set = <K extends keyof ProviderFormState>(
		key: K,
		value: ProviderFormState[K],
	) => setForm((current) => ({ ...current, [key]: value }));

	const test = useMutation({
		mutationFn: async (issuerUrl: string) =>
			(await testAuthProvider({ body: { issuerUrl } })).data,
	});

	const choosePreset = (id: PresetId) => {
		const next = applyPreset(form, presetById(id));
		setPresetId(id);
		setForm(next);
		test.reset();
		// Select the first {placeholder} so typing replaces it.
		const range = firstPlaceholderRange(next.issuerUrl);
		if (range) {
			requestAnimationFrame(() => {
				const input = issuerRef.current;
				if (!input) return;
				input.focus();
				input.setSelectionRange(range[0], range[1]);
			});
		}
	};

	const save = useMutation({
		mutationFn: async () => {
			if (provider) {
				return (
					await updateAuthProvider({
						path: { id: provider.id },
						body: providerBody(form),
					})
				).data;
			}
			return (
				await createAuthProvider({ body: providerBody(form, providerId) })
			).data;
		},
		onSuccess: async (data) => {
			await queryClient.invalidateQueries({
				queryKey: listAuthProvidersQueryKey(),
			});
			if (editing) {
				toast.success(`Saved ${data.name}`);
				onOpenChange(false);
			} else {
				setCreated(data);
			}
		},
	});

	const problem = providerProblem(form, editing, preset);
	const submit = (event: FormEvent) => {
		event.preventDefault();
		setTouched(true);
		if (problem || save.isPending) return;
		save.mutate();
	};

	const github = form.kind === "github";
	const issuer = form.issuerUrl.trim();
	const unreplaced = placeholdersIn(issuer).length > 0;
	const warning = editing ? undefined : issuerWarning(presetId, issuer);
	const origin = (system.data?.publicUrl || window.location.origin).replace(
		/\/+$/,
		"",
	);
	const redirectUri = provider
		? provider.redirectUri
		: expectedRedirectUri(origin, providerId);

	if (created) {
		return (
			<Dialog open={open} onOpenChange={onOpenChange}>
				<DialogContent size="md">
					<DialogHeader>
						<DialogTitle>{created.name} is ready</DialogTitle>
						<DialogDescription>
							People can sign in with it now. If you have not yet, register this
							redirect URI at the provider in the field {preset.redirectField}.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						<div className="surface-well flex items-center gap-3 rounded-ctl py-2.5 pr-2.5 pl-3.5">
							<code className="min-w-0 flex-1 select-all break-all font-mono text-ink-1 text-xs leading-[1.7]">
								{created.redirectUri}
							</code>
							<CopyButton
								variant="chip"
								value={created.redirectUri}
								className="shrink-0"
							/>
						</div>
					</DialogBody>
					<DialogFooter>
						<Button variant="primary" onClick={() => onOpenChange(false)}>
							Done
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>
		);
	}

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="lg">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>
							{provider ? `Edit ${provider.name}` : "Add a sign-in provider"}
						</DialogTitle>
						<DialogDescription>
							People sign in with their account at the provider; NebulaCtrl
							matches them to a user by verified email.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						{editing ? null : (
							<>
								<Field>
									<FieldLabel id="provider-preset-label">Provider</FieldLabel>
									<OptionCardGroup
										aria-labelledby="provider-preset-label"
										value={presetId}
										onValueChange={choosePreset}
										className="grid grid-cols-2 sm:grid-cols-3"
									>
										{PRESETS.map((item) => (
											<OptionCard
												key={item.id}
												value={item.id}
												title={item.label}
												className={
													item.id === "oidc"
														? "col-span-full min-w-0"
														: "min-w-0"
												}
											/>
										))}
									</OptionCardGroup>
								</Field>
								<PresetGuide preset={preset} issuer={form.issuerUrl} />
							</>
						)}
						<RedirectUri
							uri={redirectUri}
							field={preset.redirectField}
							pending={!editing}
							derived={!editing && system.data?.publicUrl === ""}
						/>
						<Field>
							<FieldLabel htmlFor="provider-name">Name</FieldLabel>
							<Input
								id="provider-name"
								scale="lg"
								autoComplete="off"
								placeholder={github ? "GitHub" : "Company SSO"}
								value={form.name}
								onChange={(event) => set("name", event.target.value)}
							/>
							<FieldDescription>
								Shown on the sign-in button: “Continue with {form.name || "…"}”.
							</FieldDescription>
						</Field>
						<Field>
							<FieldLabel htmlFor="provider-issuer">
								{github ? "GitHub Enterprise Server URL" : "Issuer URL"}
							</FieldLabel>
							<Input
								id="provider-issuer"
								ref={issuerRef}
								scale="lg"
								type="url"
								autoComplete="off"
								spellCheck={false}
								placeholder={
									github
										? "Leave empty for github.com"
										: "https://login.example.com/realms/main"
								}
								value={form.issuerUrl}
								onChange={(event) => {
									set("issuerUrl", event.target.value);
									test.reset();
								}}
							/>
							{unreplaced ? (
								<FieldDescription>
									Replace the highlighted part of{" "}
									<IssuerPattern text={issuer} />.
									{placeholderHints(issuer, preset).map(({ name, hint }) =>
										hint ? (
											<span key={name}>
												{" "}
												<code className="font-mono">{`{${name}}`}</code> is{" "}
												{hint}.
											</span>
										) : null,
									)}
								</FieldDescription>
							) : github ? null : (
								<FieldDescription>
									NebulaCtrl reads{" "}
									<code>/.well-known/openid-configuration</code> from it before
									saving.
								</FieldDescription>
							)}
							{warning ? (
								<p className="flex items-start gap-1.5 text-warning-text text-xs leading-relaxed">
									<StatusIcon kind="warning" label="Warning" />
									{warning}
								</p>
							) : null}
							{github ? null : (
								<div className="flex flex-col gap-2">
									<div>
										<Button
											type="button"
											variant="secondary"
											size="sm"
											disabled={issuer === "" || unreplaced || test.isPending}
											onClick={() => test.mutate(issuer)}
										>
											{test.isPending ? <Spinner /> : null}
											Test connection
										</Button>
									</div>
									<div role="status" aria-live="polite" className="text-xs">
										{test.isPending ? (
											<span className="text-ink-3">
												Reading the OpenID configuration…
											</span>
										) : test.isError ? (
											<span className="flex items-start gap-1.5 text-ink-2">
												<StatusIcon kind="failed" label="Failed" />
												{errorMessage(test.error)}
											</span>
										) : test.data?.ok ? (
											<span className="flex items-start gap-1.5 text-ink-2">
												<StatusIcon kind="healthy" label="Works" />
												<span className="min-w-0 break-all">
													Found{" "}
													<code className="font-mono">{test.data.issuer}</code>.
													People will be sent to{" "}
													<code className="font-mono">
														{test.data.authorizationEndpoint}
													</code>
													.
												</span>
											</span>
										) : test.data ? (
											<span className="flex items-start gap-1.5 text-ink-2">
												<StatusIcon kind="failed" label="Failed" />
												{test.data.message ?? "The issuer cannot be used."}
											</span>
										) : null}
									</div>
								</div>
							)}
						</Field>
						<div className="grid gap-4 sm:grid-cols-2">
							<Field>
								<FieldLabel htmlFor="provider-client-id">Client ID</FieldLabel>
								<Input
									id="provider-client-id"
									scale="lg"
									autoComplete="off"
									spellCheck={false}
									value={form.clientId}
									onChange={(event) => set("clientId", event.target.value)}
								/>
							</Field>
							<Field>
								<FieldLabel htmlFor="provider-client-secret">
									Client secret
								</FieldLabel>
								<Input
									id="provider-client-secret"
									scale="lg"
									type="password"
									autoComplete="new-password"
									spellCheck={false}
									placeholder={editing ? "Unchanged" : ""}
									value={form.clientSecret}
									onChange={(event) => set("clientSecret", event.target.value)}
								/>
							</Field>
						</div>
						{github ? null : (
							<Field>
								<FieldLabel htmlFor="provider-scopes">Extra scopes</FieldLabel>
								<Input
									id="provider-scopes"
									scale="lg"
									autoComplete="off"
									spellCheck={false}
									placeholder="groups"
									value={form.scopes}
									onChange={(event) => set("scopes", event.target.value)}
								/>
								<FieldDescription>
									Added to openid, email and profile, which are always
									requested. Separate with spaces.
								</FieldDescription>
							</Field>
						)}
						<Field>
							<FieldLabel htmlFor="provider-domains">
								Allowed email domains
							</FieldLabel>
							<Input
								id="provider-domains"
								scale="lg"
								autoComplete="off"
								spellCheck={false}
								placeholder="example.com, corp.example"
								value={form.allowedDomains}
								onChange={(event) => set("allowedDomains", event.target.value)}
							/>
							<FieldDescription>
								{form.allowedDomains.trim() === ""
									? "Empty: any verified email may sign in."
									: "Only verified emails in these domains may sign in or sign up."}
							</FieldDescription>
						</Field>
						{github ? null : (
							<SwitchRow
								id="provider-trust-mfa"
								label="Trust the provider's MFA"
								description="Skip NebulaCtrl's own code step when the provider says the person used multi-factor authentication. It also satisfies a team's 2FA requirement. Off by default."
								checked={form.trustMfa}
								onChange={(on) => set("trustMfa", on)}
							/>
						)}
						<SwitchRow
							id="provider-enabled"
							label="Enabled"
							description="Show the button on the sign-in page."
							checked={form.enabled}
							onChange={(on) => set("enabled", on)}
						/>
						{touched && problem ? <FieldError>{problem}</FieldError> : null}
						{save.isError ? (
							<FieldError>{errorMessage(save.error)}</FieldError>
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
						<Button type="submit" variant="primary" disabled={save.isPending}>
							{save.isPending ? <Spinner /> : null}
							{editing ? "Save" : "Add provider"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
