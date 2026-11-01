import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PlugZapIcon, RefreshCwIcon, Trash2Icon } from "lucide-react";
import { type FormEvent, useState } from "react";
import { errorMessage, errorStatus } from "#/api/client-config";
import {
	deleteCloudflareIntegration,
	putCloudflareIntegration,
	refreshCloudflareIps,
	testCloudflareIntegration,
} from "#/api/gen/sdk.gen";
import type { CloudflareConnectionView } from "#/api/gen/types.gen";
import { invalidateOperations } from "#/api/invalidate";
import { useConfirm } from "#/components/common/confirm-dialog";
import { CredentialHelp } from "#/components/common/credential-help";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { connectionState, ipListSummary } from "#/features/cloudflare/model";
import {
	CLOUDFLARE_OPERATIONS,
	cloudflareIpsQuery,
	cloudflareQuery,
} from "#/features/cloudflare/queries";
import {
	type DnsCredential,
	dnsCredentialsQuery,
} from "#/features/dns01/queries";
import { cloudflareIntegrationGuide } from "#/lib/credential-links";

/** The "reuse a DNS-01 credential" select's empty choice. */
const NO_CREDENTIAL = "none";

function ConnectionForm({
	view,
	credentials,
}: {
	view: CloudflareConnectionView;
	credentials: DnsCredential[];
}) {
	const queryClient = useQueryClient();
	const [token, setToken] = useState("");
	const [withTunnel, setWithTunnel] = useState(true);
	const [credentialId, setCredentialId] = useState("");
	const [accountId, setAccountId] = useState(view.accountId);

	const accounts = view.accounts ?? [];
	const accountItems = accounts.map((a) => ({
		value: a.id,
		label: a.name || a.id,
	}));
	const credentialItems = [
		{ value: NO_CREDENTIAL, label: "Do not reuse" },
		...credentials.map((c) => ({ value: c.id, label: c.name })),
	];
	const settingsChanged = accountId !== view.accountId && accountId !== "";
	const hasToken = token.trim() !== "" || credentialId !== "";
	// First connection needs a token; afterwards it is optional (settings only).
	const valid = view.configured ? hasToken || settingsChanged : hasToken;

	const save = useMutation({
		mutationFn: async () => {
			await putCloudflareIntegration({
				body: {
					...(token.trim() !== "" ? { token: token.trim() } : {}),
					...(token.trim() === "" && credentialId !== ""
						? { dns01CredentialId: credentialId }
						: {}),
					...(accountId !== "" ? { accountId } : {}),
				},
			});
		},
		onSuccess: async () => {
			setToken("");
			setCredentialId("");
			await invalidateOperations(queryClient, CLOUDFLARE_OPERATIONS);
			toast.success(
				view.configured ? "Cloudflare updated" : "Cloudflare connected",
				"Domains in your zones are being matched.",
			);
		},
	});

	const test = useMutation({
		mutationFn: async () => {
			const { data } = await testCloudflareIntegration();
			return data;
		},
		onSuccess: async (result) => {
			await invalidateOperations(queryClient, CLOUDFLARE_OPERATIONS);
			toast.success(
				"Cloudflare answered",
				`${result.zoneCount} ${result.zoneCount === 1 ? "zone" : "zones"} visible to the token.`,
			);
		},
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (valid && !save.isPending) save.mutate();
	};

	const state = connectionState(view);

	return (
		<form
			onSubmit={submit}
			className="surface-raised flex flex-col gap-4 rounded-card p-4"
		>
			<div className="flex flex-wrap items-center gap-2">
				<Badge tone={state.tone} lamp>
					{state.label}
				</Badge>
				{view.configured ? (
					<span className="text-ink-3 text-xs">
						{view.zoneCount} {view.zoneCount === 1 ? "zone" : "zones"}
						{view.accountName ? ` · ${view.accountName}` : ""}
						{view.zonesSyncedAt ? (
							<>
								{" "}
								· refreshed <Relative iso={view.zonesSyncedAt} />
							</>
						) : null}
					</span>
				) : null}
			</div>
			<div className="grid gap-4 sm:grid-cols-2">
				<div className="flex flex-col gap-3 sm:col-span-2">
					<CheckLabel
						checked={withTunnel}
						onCheckedChange={setWithTunnel}
						labelClassName="text-[13px] text-ink-2"
					>
						Also manage Cloudflare Tunnels
					</CheckLabel>
					<CredentialHelp
						guide={cloudflareIntegrationGuide({ tunnel: withTunnel })}
						fieldIds={["cf-token"]}
					/>
				</div>
				<Field className="sm:col-span-2">
					<FieldLabel htmlFor="cf-token">API token</FieldLabel>
					<Input
						id="cf-token"
						type="password"
						autoComplete="new-password"
						spellCheck={false}
						className="font-mono"
						placeholder={
							view.configured
								? "Stored, leave empty to keep"
								: "Cloudflare API token"
						}
						value={token}
						onChange={(e) => setToken(e.target.value)}
					/>
					<FieldDescription>
						Write-only. It is verified against Cloudflare, encrypted at rest and
						never shown again. Limit it to the zones NebulaCtrl should manage.
					</FieldDescription>
				</Field>
				{credentials.length > 0 && token.trim() === "" ? (
					<Field className="sm:col-span-2">
						<FieldLabel htmlFor="cf-credential">
							Or reuse a DNS-01 credential
						</FieldLabel>
						<Select
							value={credentialId === "" ? NO_CREDENTIAL : credentialId}
							items={credentialItems}
							onValueChange={(next) =>
								setCredentialId(next === NO_CREDENTIAL ? "" : String(next))
							}
						>
							<SelectTrigger id="cf-credential" className="w-full">
								<SelectValue />
							</SelectTrigger>
							<SelectContent alignItemWithTrigger={false} align="start">
								{credentialItems.map((item) => (
									<SelectItem key={item.value} value={item.value}>
										{item.label}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<FieldDescription>
							Copies the token of a Cloudflare credential used for wildcard
							certificates. It needs the permissions listed above as well.
						</FieldDescription>
					</Field>
				) : null}
				{accounts.length > 1 ? (
					<Field>
						<FieldLabel htmlFor="cf-account">Account for tunnels</FieldLabel>
						<Select
							value={accountId}
							items={accountItems}
							onValueChange={(next) => setAccountId(String(next))}
						>
							<SelectTrigger id="cf-account" className="w-full">
								<SelectValue />
							</SelectTrigger>
							<SelectContent alignItemWithTrigger={false} align="start">
								{accountItems.map((item) => (
									<SelectItem key={item.value} value={item.value}>
										{item.label}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<FieldDescription>
							The token sees zones of several accounts.
						</FieldDescription>
					</Field>
				) : null}
			</div>
			<p className="text-ink-3 text-xs leading-[1.5]">
				DNS of domains and status pages in the connected zones is managed
				automatically: records are created, moved with their server and deleted
				with it. Records you created are never changed unless you replace them.
				New names start proxied through Cloudflare; existing ones keep their
				setting. Proxied needs the zone's SSL/TLS mode to be Full or Full
				(strict): Flexible loops.
			</p>
			{(view.warnings ?? []).map((w) => (
				<Alert key={w} variant="warning">
					<AlertDescription>{w}</AlertDescription>
				</Alert>
			))}
			{view.status === "error" ? (
				<Alert variant="destructive">
					<AlertTitle>Cloudflare rejected the token</AlertTitle>
					<AlertDescription>
						{view.statusMessage || "Paste a new token."} DNS records are not
						changed until it works again.
					</AlertDescription>
				</Alert>
			) : null}
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
					{view.configured ? "Save changes" : "Connect Cloudflare"}
				</Button>
				<Button
					type="button"
					variant="secondary"
					disabled={!view.configured || test.isPending}
					onClick={() => test.mutate()}
				>
					{test.isPending ? <Spinner /> : <PlugZapIcon aria-hidden />}
					Test connection
				</Button>
			</div>
		</form>
	);
}

/**
 * The IP ranges the origin lock allows: where the list came from, how many
 * ranges, how old it is and whether fetching works. A job refreshes it every
 * three hours; "Refresh now" does the same on demand.
 */
function IpListCard() {
	const queryClient = useQueryClient();
	const ips = useQuery(cloudflareIpsQuery());
	const refresh = useMutation({
		mutationFn: async () => {
			const { data } = await refreshCloudflareIps();
			return data;
		},
		onSuccess: async (view) => {
			await invalidateOperations(queryClient, CLOUDFLARE_OPERATIONS);
			toast.success(
				"Cloudflare IP ranges refreshed",
				`${view.v4Count + view.v6Count} ranges in use.`,
			);
		},
		onError: async (error) => {
			await invalidateOperations(queryClient, CLOUDFLARE_OPERATIONS);
			toast.error("Could not refresh the IP ranges", errorMessage(error));
		},
	});

	if (ips.isPending) return <Skeleton className="h-32 rounded-card" />;
	if (ips.isError || !ips.data) return null;
	const summary = ipListSummary(ips.data);

	return (
		<section
			aria-label="Cloudflare IP ranges"
			className="surface-raised flex flex-col gap-3 rounded-card p-4"
			data-section="cloudflare-ip-list"
		>
			<div className="flex flex-wrap items-start justify-between gap-3">
				<div className="flex flex-col gap-0.5">
					<h3 className="type-label m-0">Cloudflare IP ranges</h3>
					<p className="text-ink-3 text-xs leading-[1.5]">
						Proxied domains only accept connections from these ranges (the
						origin lock). They are fetched every three hours; a change is pushed
						to the servers without a redeploy.
					</p>
				</div>
				<Button
					type="button"
					variant="secondary"
					size="sm"
					disabled={refresh.isPending}
					onClick={() => refresh.mutate()}
				>
					{refresh.isPending ? <Spinner /> : <RefreshCwIcon aria-hidden />}
					Refresh now
				</Button>
			</div>
			<dl className="m-0 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-[13px]">
				<dt className="text-ink-3">Source</dt>
				<dd className="m-0 text-ink-1">{summary.source}</dd>
				<dt className="text-ink-3">Ranges</dt>
				<dd className="m-0 font-mono text-[12px] text-ink-1">{summary.size}</dd>
				<dt className="text-ink-3">Age</dt>
				<dd className="m-0 text-ink-1">{summary.age}</dd>
				{summary.changed ? (
					<>
						<dt className="text-ink-3">Changed</dt>
						<dd className="m-0 text-ink-1">{summary.changed}</dd>
					</>
				) : null}
			</dl>
			{summary.problem ? (
				<Alert variant="warning">
					<AlertTitle>
						{summary.stale
							? "The IP ranges could not be refreshed for a day"
							: "The last refresh failed"}
					</AlertTitle>
					<AlertDescription>{summary.problem}</AlertDescription>
				</Alert>
			) : null}
		</section>
	);
}

/** Settings → Integrations → Cloudflare: the API token NebulaCtrl keeps DNS records in zones with. */
export function CloudflareSection() {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const integration = useQuery(cloudflareQuery());
	const creds = useQuery(dnsCredentialsQuery());
	const [removeRecords, setRemoveRecords] = useState(false);

	const remove = useMutation({
		mutationFn: async () => {
			await deleteCloudflareIntegration({ query: { removeRecords } });
		},
		onSuccess: async () => {
			setRemoveRecords(false);
			await invalidateOperations(queryClient, CLOUDFLARE_OPERATIONS);
			toast.success(
				"Cloudflare disconnected",
				removeRecords
					? "The records NebulaCtrl created were removed."
					: "Existing records stay in Cloudflare, unmanaged.",
			);
		},
		onError: (error) =>
			toast.error("Could not disconnect Cloudflare", errorMessage(error)),
	});

	if (integration.isPending) return <Skeleton className="h-64 rounded-card" />;
	if (integration.isError || !integration.data) {
		const forbidden = errorStatus(integration.error) === 403;
		return (
			<ErrorState
				frame="raised"
				title={
					forbidden
						? "Only administrators can manage Cloudflare"
						: "Cloudflare did not load"
				}
				body={
					forbidden
						? "Ask an administrator to connect Cloudflare. Domains in its zones then get their DNS records managed."
						: "The control plane did not answer, so nothing was changed."
				}
				{...(forbidden ? {} : { retry: () => void integration.refetch() })}
			/>
		);
	}
	const view = integration.data;
	const credentials = (
		((creds.data as { items?: DnsCredential[] } | undefined)?.items ??
			[]) as DnsCredential[]
	).filter((c) => c.provider === "cloudflare");

	return (
		<div className="flex flex-col gap-4">
			<ConnectionForm
				key={view.configured ? `${view.accountId}:${view.zoneCount}` : "new"}
				view={view}
				credentials={credentials}
			/>
			{view.configured ? <IpListCard /> : null}
			{view.configured ? (
				<div className="flex flex-wrap items-center justify-between gap-3 rounded-card border border-destructive-border p-4">
					<div className="flex min-w-0 flex-1 flex-col gap-2">
						<p className="type-secondary">
							Disconnecting stops DNS management. Records NebulaCtrl created
							stay in Cloudflare unless you remove them with it.
						</p>
						<CheckLabel
							checked={removeRecords}
							onCheckedChange={setRemoveRecords}
							labelClassName="text-ink-2 text-xs"
						>
							Also delete the DNS records NebulaCtrl created
						</CheckLabel>
					</div>
					<Button
						variant="danger"
						onClick={() =>
							void confirm({
								title: "Disconnect Cloudflare?",
								body: removeRecords
									? "NebulaCtrl deletes the DNS records it created, then forgets the token. Records you created are not touched."
									: "NebulaCtrl forgets the token. Its records stay in Cloudflare but are no longer kept in sync.",
								cta: "Disconnect",
								danger: true,
								hold: false,
								onConfirm: async () => {
									await remove.mutateAsync();
								},
							})
						}
					>
						<Trash2Icon aria-hidden />
						Disconnect
					</Button>
				</div>
			) : null}
		</div>
	);
}
