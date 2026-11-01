import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	CheckIcon,
	MinusIcon,
	PlugZapIcon,
	Trash2Icon,
	XIcon,
} from "lucide-react";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage, errorStatus } from "#/api/client-config";
import {
	deleteTailscaleIntegration,
	putTailscaleIntegration,
	testTailscaleIntegration,
} from "#/api/gen/sdk.gen";
import type {
	TailscaleCheck,
	TailscaleIntegration,
	TailscaleTestResult,
} from "#/api/gen/types.gen";
import { invalidateOperations } from "#/api/invalidate";
import { useConfirm } from "#/components/common/confirm-dialog";
import { CopyButton } from "#/components/common/copy-button";
import { CredentialHelp } from "#/components/common/credential-help";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
	FieldTitle,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Segmented } from "#/components/ui/segmented";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import { toast } from "#/components/ui/toast";
import { SetUpHeadscaleButton } from "#/features/platform/set-up-for-me-button";
import {
	DEFAULT_TAG,
	HEADSCALE_CREDENTIALS,
	type HeadscaleCredential,
	headscalePolicySnippet,
	type IntegrationProvider,
	keepsStoredSecret,
	parseTags,
	policySnippet,
	primaryAddress,
	REQUIRED_OAUTH_SCOPES,
	validateLoginServer,
	validateTags,
} from "#/features/proxy/lib/tailscale";
import {
	tailscaleDevicesQuery,
	tailscaleQuery,
} from "#/features/proxy/queries";
import {
	headscaleApiKeyGuide,
	headscalePreauthGuide,
	tailscaleOAuthGuide,
} from "#/lib/credential-links";

const TAILSCALE_OPERATIONS = [
	"getTailscaleIntegration",
	"listTailscaleDevices",
	"getServerProxy",
] as const;

/** One line per check: ticked, failed or skipped, each with its words. */
function CheckList({ result }: { result: TailscaleTestResult }) {
	const checks: TailscaleCheck[] = result.checks ?? [];
	return (
		<ul
			aria-label="Connection checks"
			className="surface-well flex flex-col gap-2 rounded-card p-3"
		>
			{checks.map((check) => (
				<li key={check.id} className="flex items-start gap-2.5 text-sm">
					{check.skipped ? (
						<MinusIcon
							aria-hidden
							className="mt-0.5 size-4 shrink-0 text-ink-3"
						/>
					) : check.ok ? (
						<CheckIcon aria-hidden className="mt-0.5 size-4 shrink-0 text-ok" />
					) : (
						<XIcon aria-hidden className="mt-0.5 size-4 shrink-0 text-bad" />
					)}
					<span className="flex min-w-0 flex-col">
						<span className="text-ink-1">
							{check.id}
							<span className="sr-only">
								{check.skipped ? " skipped" : check.ok ? " passed" : " failed"}
							</span>
						</span>
						<span className="text-ink-3 text-xs">{check.message}</span>
					</span>
				</li>
			))}
			<li
				className={`border-line border-t pt-2 font-medium text-xs ${result.ok ? "text-ok" : "text-bad"}`}
			>
				{result.ok
					? "Everything NebulaCtrl needs works."
					: "Something failed: fix it and test again."}
			</li>
		</ul>
	);
}

function PolicyPanel({
	tags,
	provider,
	user,
}: {
	tags: readonly string[];
	provider: IntegrationProvider;
	user: string;
}) {
	const headscale = provider === "headscale";
	const snippet = headscale
		? headscalePolicySnippet(tags, user)
		: policySnippet(tags);
	return (
		<section
			aria-label={headscale ? "Headscale policy" : "Tailnet policy"}
			className="surface-raised flex flex-col gap-3 rounded-card p-4"
		>
			<div className="flex items-start gap-3">
				<div className="min-w-0 flex-1">
					<h4 className="font-semibold text-ink-1 text-sm">
						{headscale ? "Headscale policy" : "Tailnet policy"}
					</h4>
					{headscale ? (
						<p className="type-secondary mt-1">
							Add this to your Headscale policy file so the Headscale user may
							own the service tags (tagOwners) and members can reach tagged
							nodes. NebulaCtrl never edits your policy.
						</p>
					) : (
						<p className="type-secondary mt-1">
							Paste this into the Tailscale admin console under Access controls.
							NebulaCtrl never edits your policy. Create the OAuth client with
							the scopes{" "}
							{REQUIRED_OAUTH_SCOPES.map((scope, index) => (
								<span key={scope}>
									{index > 0 ? " and " : ""}
									<code className="type-mono text-ink-2">{scope}</code>
								</span>
							))}
							, owned by{" "}
							<code className="type-mono text-ink-2">tag:nebula</code>.
						</p>
					)}
				</div>
				<CopyButton variant="chip" value={snippet} className="shrink-0">
					Copy policy
				</CopyButton>
			</div>
			<pre className="surface-well max-h-72 overflow-auto overscroll-contain rounded-ctl p-3 font-mono text-2xs text-ink-2 leading-[1.6]">
				{snippet}
			</pre>
			<p className="text-ink-3 text-xs">
				A new tailnet allows every member to reach every device. Tighten the ACL
				before you rely on tags to keep service nodes private.
			</p>
		</section>
	);
}

function DevicesTable() {
	const devices = useQuery(tailscaleDevicesQuery());
	if (devices.isPending) return <Skeleton className="h-24 rounded-card" />;
	if (devices.isError) {
		return (
			<p className="type-secondary">
				Devices did not load: {errorMessage(devices.error)}
			</p>
		);
	}
	const items = devices.data.items ?? [];
	if (items.length === 0) {
		return (
			<p className="type-secondary">
				No devices yet. Managed nodes appear here once a server's tailnet is
				provisioned.
			</p>
		);
	}
	return (
		<div className="overflow-hidden border-line border-y">
			<Table stack>
				<TableHeader>
					<TableRow>
						<TableHead>Device</TableHead>
						<TableHead>Address</TableHead>
						<TableHead>Tags</TableHead>
						<TableHead>State</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					{items.map((device) => (
						<TableRow key={device.id}>
							<TableCell pin="start">
								<span className="flex flex-col">
									<span className="font-medium text-ink-1">
										{device.hostname}
									</span>
									<span className="font-mono text-2xs text-ink-3">
										{device.name}
									</span>
								</span>
							</TableCell>
							<TableCell
								label="Address"
								className="font-mono text-ink-2 text-xs"
							>
								{primaryAddress(device.addresses)}
							</TableCell>
							<TableCell label="Tags" className="text-ink-3 text-xs">
								{(device.tags ?? []).join(", ") || "none"}
							</TableCell>
							<TableCell label="State">
								<Badge tone={device.online ? "live" : "off"} lamp>
									{device.online ? "Online" : "Offline"}
								</Badge>
								{device.online ? null : (
									<span className="ml-2 text-ink-3 text-xs">
										seen <Relative iso={device.lastSeen} />
									</span>
								)}
							</TableCell>
						</TableRow>
					))}
				</TableBody>
			</Table>
		</div>
	);
}

function ConnectionForm({
	integration,
	onTestResult,
}: {
	integration: TailscaleIntegration;
	onTestResult(result: TailscaleTestResult | null): void;
}) {
	const queryClient = useQueryClient();
	const configured = integration.configured;
	const [provider, setProvider] = useState<IntegrationProvider>(
		integration.provider,
	);
	const [clientId, setClientId] = useState(integration.clientId);
	const [loginServer, setLoginServer] = useState(integration.loginServer);
	const [headscaleUser, setHeadscaleUser] = useState(integration.headscaleUser);
	const [credential, setCredential] = useState<HeadscaleCredential>(
		integration.headscaleCredential,
	);
	const [secret, setSecret] = useState("");
	const [tailnet, setTailnet] = useState(
		integration.tailnet === "-" ? "" : integration.tailnet,
	);
	const [defaults, setDefaults] = useState(
		(integration.defaultTags ?? [DEFAULT_TAG]).join(", "),
	);
	const [allowed, setAllowed] = useState(
		(integration.allowedTags ?? []).join(", "),
	);
	// Follow the server after a save or a refetch, never while typing.
	const [touched, setTouched] = useState(false);
	useEffect(() => {
		if (touched) return;
		setProvider(integration.provider);
		setClientId(integration.clientId);
		setLoginServer(integration.loginServer);
		setHeadscaleUser(integration.headscaleUser);
		setCredential(integration.headscaleCredential);
		setTailnet(integration.tailnet === "-" ? "" : integration.tailnet);
		setDefaults((integration.defaultTags ?? [DEFAULT_TAG]).join(", "));
		setAllowed((integration.allowedTags ?? []).join(", "));
	}, [integration, touched]);

	const headscale = provider === "headscale";
	const defaultTags = parseTags(defaults);
	const allowedTags = parseTags(allowed);
	const tagErrors = validateTags(defaultTags, allowedTags);
	const loginServerError =
		headscale && loginServer.trim() !== ""
			? validateLoginServer(loginServer)
			: null;
	const keepsSecret = keepsStoredSecret(
		{ provider, clientId, loginServer, credential },
		integration,
	);
	const needsSecret = !keepsSecret && secret === "";
	const secretLabel = !headscale
		? "Client secret"
		: credential === "apikey"
			? "API key"
			: "Pre-auth key";
	const connectionFilled = headscale
		? loginServer.trim() !== "" && loginServerError === null
		: clientId.trim() !== "";
	const valid =
		connectionFilled &&
		!needsSecret &&
		tagErrors.defaultTags === undefined &&
		tagErrors.allowedTags === undefined;

	const save = useMutation({
		mutationFn: async () => {
			const tags = {
				defaultTags,
				allowedTags: allowedTags.length > 0 ? allowedTags : null,
			};
			const secretField = secret !== "" ? { clientSecret: secret } : {};
			await putTailscaleIntegration({
				body: headscale
					? {
							provider,
							loginServer: loginServer.trim(),
							headscaleCredential: credential,
							...(credential === "apikey"
								? { headscaleUser: headscaleUser.trim() }
								: {}),
							...secretField,
							...tags,
						}
					: {
							provider,
							clientId: clientId.trim(),
							tailnet: tailnet.trim() === "" ? "-" : tailnet.trim(),
							...secretField,
							...tags,
						},
			});
		},
		onSuccess: async () => {
			setSecret("");
			setTouched(false);
			onTestResult(null);
			await invalidateOperations(queryClient, TAILSCALE_OPERATIONS);
			toast.success(
				headscale ? "Headscale saved" : "Tailscale saved",
				"Run the connection test to check it.",
			);
		},
	});

	const test = useMutation({
		mutationFn: async () => {
			const { data } = await testTailscaleIntegration();
			return data;
		},
		onSuccess: async (result) => {
			onTestResult(result);
			await invalidateOperations(queryClient, TAILSCALE_OPERATIONS);
		},
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (valid && !save.isPending) save.mutate();
	};

	const touch =
		<T,>(set: (value: T) => void) =>
		(value: T) => {
			setTouched(true);
			set(value);
		};

	return (
		<form
			onSubmit={submit}
			className="surface-raised flex flex-col gap-4 rounded-card p-4"
		>
			<div className="flex flex-wrap items-center gap-2">
				{configured ? (
					<Badge tone="live" lamp>
						Connected
					</Badge>
				) : (
					<Badge tone="off" lamp>
						Not connected
					</Badge>
				)}
				{integration.lastTest.ok === false ? (
					<Badge tone="bad" lamp>
						Last test failed
					</Badge>
				) : null}
				{integration.lastTest.at ? (
					<span className="text-ink-3 text-xs">
						tested <Relative iso={integration.lastTest.at} />
					</span>
				) : null}
			</div>
			<div className="flex flex-col gap-2">
				<Segmented
					aria-label="Provider"
					font="sans"
					density="regular"
					value={provider}
					onValueChange={touch(setProvider)}
					options={[
						{ value: "tailscale_oauth", label: "Tailscale (OAuth)" },
						{ value: "headscale", label: "Headscale" },
					]}
				/>
				{headscale ? (
					<>
						<p className="text-ink-3 text-xs">
							Headscale has no Serve HTTPS or Funnel: services expose plain HTTP
							and raw TCP over WireGuard.
						</p>
						<div>
							<SetUpHeadscaleButton label="Set up Headscale for me" />
						</div>
					</>
				) : null}
			</div>
			<div className="grid gap-4 sm:grid-cols-2">
				{headscale ? null : (
					<CredentialHelp
						className="sm:col-span-2"
						guide={tailscaleOAuthGuide({ defaultTags })}
						fieldIds={["ts-client-id", "ts-client-secret"]}
					/>
				)}
				{headscale ? (
					<>
						<Field className="sm:col-span-2">
							<FieldLabel htmlFor="ts-login-server">
								Login server URL
							</FieldLabel>
							<Input
								id="ts-login-server"
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
								placeholder="https://headscale.example.com"
								aria-invalid={loginServerError !== null}
								value={loginServer}
								onChange={(e) => touch(setLoginServer)(e.target.value)}
							/>
							<FieldDescription>
								Where nodes register and where NebulaCtrl reaches the Headscale
								API.
							</FieldDescription>
							<FieldError>{loginServerError}</FieldError>
						</Field>
						<Field className="sm:col-span-2">
							<FieldTitle>Credential kind</FieldTitle>
							<Segmented
								aria-label="Credential kind"
								font="sans"
								density="regular"
								value={credential}
								onValueChange={touch(setCredential)}
								options={HEADSCALE_CREDENTIALS.map((kind) => ({
									value: kind.id,
									label: kind.label,
								}))}
							/>
							<FieldDescription>
								{HEADSCALE_CREDENTIALS.find((kind) => kind.id === credential)
									?.hint ?? ""}
							</FieldDescription>
						</Field>
						{credential === "apikey" ? (
							<Field>
								<FieldLabel htmlFor="ts-headscale-user">
									Headscale user
								</FieldLabel>
								<Input
									id="ts-headscale-user"
									autoComplete="off"
									spellCheck={false}
									className="font-mono"
									placeholder="optional"
									value={headscaleUser}
									onChange={(e) => touch(setHeadscaleUser)(e.target.value)}
								/>
								<FieldDescription>
									Optional. Empty creates tags-only keys.
								</FieldDescription>
							</Field>
						) : null}
					</>
				) : (
					<Field>
						<FieldLabel htmlFor="ts-client-id">Client ID</FieldLabel>
						<Input
							id="ts-client-id"
							placeholder="Tailscale OAuth client ID"
							autoComplete="off"
							spellCheck={false}
							className="font-mono"
							value={clientId}
							onChange={(e) => touch(setClientId)(e.target.value)}
						/>
					</Field>
				)}
				{headscale ? (
					<CredentialHelp
						className="sm:col-span-2"
						guide={
							credential === "apikey"
								? headscaleApiKeyGuide()
								: headscalePreauthGuide({ defaultTags })
						}
						fieldIds={["ts-client-secret"]}
					/>
				) : null}
				<Field>
					<FieldLabel htmlFor="ts-client-secret">{secretLabel}</FieldLabel>
					<Input
						id="ts-client-secret"
						type="password"
						autoComplete="new-password"
						spellCheck={false}
						className="font-mono"
						placeholder={
							keepsSecret
								? "Stored, leave empty to keep"
								: headscale
									? credential === "apikey"
										? "Headscale API key"
										: "Reusable tagged pre-auth key"
									: "tskey-client-…"
						}
						value={secret}
						onChange={(e) => touch(setSecret)(e.target.value)}
					/>
					<FieldDescription>
						Write-only. It is encrypted at rest and never shown again.
						{integration.secretSet && !keepsSecret
							? " The stored secret belongs to the previous connection: enter the new one."
							: ""}
					</FieldDescription>
				</Field>
				<Field>
					<FieldLabel htmlFor="ts-default-tags">Default tags</FieldLabel>
					<Input
						id="ts-default-tags"
						autoComplete="off"
						spellCheck={false}
						className="font-mono"
						aria-invalid={tagErrors.defaultTags !== undefined}
						value={defaults}
						onChange={(e) => touch(setDefaults)(e.target.value)}
					/>
					<FieldDescription>
						Every key NebulaCtrl mints carries these tags.
					</FieldDescription>
					<FieldError>{tagErrors.defaultTags}</FieldError>
				</Field>
				<Field>
					<FieldLabel htmlFor="ts-allowed-tags">Allowed tags</FieldLabel>
					<Input
						id="ts-allowed-tags"
						autoComplete="off"
						spellCheck={false}
						className="font-mono"
						placeholder="same as the default tags"
						aria-invalid={tagErrors.allowedTags !== undefined}
						value={allowed}
						onChange={(e) => touch(setAllowed)(e.target.value)}
					/>
					<FieldDescription>
						Optional. Tags a server may use; must include every default tag.
					</FieldDescription>
					<FieldError>{tagErrors.allowedTags}</FieldError>
				</Field>
				{headscale ? null : (
					<Field>
						<FieldLabel htmlFor="ts-tailnet">Tailnet</FieldLabel>
						<Input
							id="ts-tailnet"
							autoComplete="off"
							spellCheck={false}
							className="font-mono"
							placeholder="- (the OAuth client's own tailnet)"
							value={tailnet}
							onChange={(e) => touch(setTailnet)(e.target.value)}
						/>
					</Field>
				)}
			</div>
			{save.isError ? (
				<FieldError>{errorMessage(save.error)}</FieldError>
			) : null}
			{test.isError ? (
				<FieldError>{errorMessage(test.error)}</FieldError>
			) : null}
			<div className="flex flex-wrap items-center gap-2">
				<Button
					type="submit"
					variant={valid ? "primary" : "secondary"}
					disabled={!valid || save.isPending}
				>
					{save.isPending ? <Spinner /> : null}
					{configured
						? "Save changes"
						: headscale
							? "Connect Headscale"
							: "Connect Tailscale"}
				</Button>
				<Button
					type="button"
					variant="secondary"
					disabled={!configured || test.isPending || (touched && valid)}
					title={
						touched
							? "Save your changes first: the test uses the saved connection"
							: undefined
					}
					onClick={() => test.mutate()}
				>
					{test.isPending ? <Spinner /> : <PlugZapIcon aria-hidden />}
					Test connection
				</Button>
			</div>
		</form>
	);
}

/** Settings → Integrations → Tailscale: the Tailscale OAuth client or Headscale server NebulaCtrl creates node keys with. */
export function TailscaleSection() {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const integration = useQuery(tailscaleQuery());
	const [result, setResult] = useState<TailscaleTestResult | null>(null);

	const remove = useMutation({
		mutationFn: async () => {
			await deleteTailscaleIntegration();
		},
		onSuccess: async () => {
			setResult(null);
			await invalidateOperations(queryClient, TAILSCALE_OPERATIONS);
			toast.success(
				"Tailscale removed",
				"Servers keep the tailnet nodes they already have.",
			);
		},
		onError: (error) =>
			toast.error("Could not remove Tailscale", errorMessage(error)),
	});

	if (integration.isPending) return <Skeleton className="h-64 rounded-card" />;
	if (integration.isError || !integration.data) {
		const forbidden = errorStatus(integration.error) === 403;
		return (
			<ErrorState
				frame="raised"
				title={
					forbidden
						? "Only administrators can manage Tailscale"
						: "Tailscale did not load"
				}
				body={
					forbidden
						? "Ask an administrator to connect the tailnet. Servers can then use it from their Proxy tab."
						: "The control plane did not answer, so nothing was changed."
				}
				{...(forbidden ? {} : { retry: () => void integration.refetch() })}
			/>
		);
	}
	const data = integration.data;
	const tags = data.defaultTags ?? [DEFAULT_TAG];

	return (
		<div className="flex flex-col gap-4">
			{data.configured &&
			data.provider === "tailscale_oauth" &&
			data.httpsEnabled === false ? (
				<Alert variant="warning">
					<AlertTitle>HTTPS certificates are off for this tailnet</AlertTitle>
					<AlertDescription>
						Routes to tailnet devices over plain HTTP still work; enable HTTPS
						in the Tailscale admin console for TLS names.
					</AlertDescription>
				</Alert>
			) : null}
			<ConnectionForm
				key={
					data.configured
						? `${data.provider}:${data.clientId}:${data.loginServer}`
						: "new"
				}
				integration={data}
				onTestResult={setResult}
			/>
			{result ? (
				<CheckList result={result} />
			) : data.lastTest.ok === false && data.lastTest.error ? (
				<Alert variant="destructive">
					<AlertTitle>The last test failed</AlertTitle>
					<AlertDescription>{data.lastTest.error}</AlertDescription>
				</Alert>
			) : null}
			<PolicyPanel
				tags={tags}
				provider={data.provider}
				user={data.headscaleUser}
			/>
			{data.configured &&
			(data.provider !== "headscale" ||
				data.headscaleCredential === "apikey") ? (
				<section aria-label="Devices" className="flex flex-col gap-3">
					<h4 className="font-semibold text-ink-1 text-sm">
						Devices with your tags
					</h4>
					<DevicesTable />
				</section>
			) : null}
			{data.configured ? (
				<div className="flex items-center justify-between gap-3 rounded-card border border-destructive-border p-4">
					<p className="type-secondary">
						Removing the integration stops new keys from being minted. Existing
						nodes stay on their servers until you turn the tailnet off there.
					</p>
					<Button
						variant="danger"
						onClick={() =>
							void confirm({
								title: "Remove the Tailscale integration?",
								body: "NebulaCtrl forgets the stored credentials. Per-service tailnet nodes keep running, but nothing can be created, changed or re-provisioned until you connect again.",
								cta: "Remove integration",
								danger: true,
								hold: false,
								onConfirm: async () => {
									await remove.mutateAsync();
								},
							})
						}
					>
						<Trash2Icon aria-hidden />
						Remove
					</Button>
				</div>
			) : null}
		</div>
	);
}
