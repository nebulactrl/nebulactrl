import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { cn } from "cn";
import { KeyRoundIcon, PlusIcon, Trash2Icon } from "lucide-react";
import { useId, useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	createServerWildcardCert,
	deleteDnsCredential,
	deleteServerDns01Resolver,
	deleteWildcardCert,
	setServerDns01Resolver,
	updateWildcardCert,
} from "#/api/gen/sdk.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { DocsLink } from "#/components/docs/docs-link";
import {
	Alert,
	AlertAction,
	AlertDescription,
	AlertTitle,
} from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import {
	type DnsCredential,
	type DnsProvider,
	type DnsProviderSpec,
	dnsCredentialsQuery,
	dnsProvidersQuery,
	expiryLabel,
	expiryTone,
	resolverLabel,
	type ServerDns01,
	serverDns01Query,
	type WildcardCertView,
} from "#/features/dns01/queries";
import { invalidateProxy } from "#/features/proxy/queries";
import { useIsAdmin } from "#/features/proxy/use-admin";
import { CredentialForm } from "./credential-form";

const CHANGE_TITLE = {
	add: "Staged: added on the next Deploy proxy",
	update: "Staged: changed on the next Deploy proxy",
	remove: "Staged: removed on the next Deploy proxy",
} as const;

function StagedBadge({
	change,
}: {
	change: keyof typeof CHANGE_TITLE | "none";
}) {
	if (change === "none") return null;
	return (
		<Badge tone="staged" title={CHANGE_TITLE[change]}>
			{change === "add"
				? "Staged"
				: change === "remove"
					? "Removal staged"
					: "Change staged"}
		</Badge>
	);
}

function useRefresh() {
	const queryClient = useQueryClient();
	return async () => {
		await invalidateProxy(queryClient);
		await queryClient.invalidateQueries({
			predicate: (q) =>
				JSON.stringify(q.queryKey).includes("Dns01") ||
				JSON.stringify(q.queryKey).includes("DnsCredentials"),
		});
	};
}

const WILDCARD_KIND: Record<WildcardCertView["status"], StatusKind> = {
	active: "live",
	error: "failed",
	pending: "pending",
};

function WildcardRow({
	cert,
	onRemove,
	onToggle,
	disabled,
}: {
	cert: WildcardCertView;
	onRemove(): void;
	onToggle(patch: { coverGenerated?: boolean; default?: boolean }): void;
	disabled: boolean;
}) {
	const tone = expiryTone(cert.daysToExpiry);
	const rowId = useId();
	return (
		<li className="grid gap-2">
			<div className="flex flex-wrap items-center gap-2">
				<span className="font-medium font-mono text-ink-1 text-sm">
					{cert.names.join(", ")}
				</span>
				<span className="inline-flex items-center gap-1.5 text-ink-2 text-xs">
					<StatusIcon kind={WILDCARD_KIND[cert.status]} label="" />
					{cert.status === "active"
						? "Issued"
						: cert.status === "error"
							? "Problem"
							: "Waiting for certificate"}
				</span>
				<StagedBadge change={cert.change} />
				{cert.default ? <Badge shape="tag">default certificate</Badge> : null}
				<span className="flex-1" />
				<Button
					variant="ghost"
					size="icon-sm"
					aria-label={`Remove *.${cert.domain}`}
					disabled={disabled}
					onClick={onRemove}
				>
					<Trash2Icon aria-hidden />
				</Button>
			</div>
			{cert.status === "active" && cert.notAfter ? (
				<p
					className={cn(
						"text-xs tabular-nums",
						tone === "ok"
							? "text-ink-2"
							: tone === "warn"
								? "text-warning-text"
								: "text-destructive-text",
					)}
				>
					{cert.issuer ? `${cert.issuer} · ` : ""}
					expires {cert.notAfter.slice(0, 10)} ({expiryLabel(cert.daysToExpiry)}
					)
				</p>
			) : null}
			{cert.statusMessage ? (
				<p
					className={cn(
						"text-xs",
						cert.status === "error" ? "text-destructive-text" : "text-ink-3",
					)}
				>
					{cert.statusMessage}
				</p>
			) : null}
			<div className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-ink-2">
				<label
					htmlFor={`${rowId}-generated`}
					className="flex items-center gap-2"
				>
					<Switch
						id={`${rowId}-generated`}
						checked={cert.coverGenerated}
						disabled={disabled}
						onCheckedChange={(coverGenerated) => onToggle({ coverGenerated })}
					/>
					Also cover generated domains
				</label>
				<label htmlFor={`${rowId}-default`} className="flex items-center gap-2">
					<Switch
						id={`${rowId}-default`}
						checked={cert.default}
						disabled={disabled}
						onCheckedChange={(next) => onToggle({ default: next })}
					/>
					Serve as the default certificate
				</label>
			</div>
		</li>
	);
}

/**
 * DNS-01 certificates of a server: provider credentials, the resolver per
 * provider and wildcard certificates. Resolvers are static proxy
 * configuration, so everything here stages until Deploy proxy, which recreates
 * the proxy container (the deploy bar says so).
 */
export function Dns01Card({
	serverId,
	agentTooOld,
	onOpenSettings,
}: {
	serverId: string;
	agentTooOld: boolean;
	/** Opens the server's settings, where the ACME email lives. */
	onOpenSettings?: () => void;
}) {
	const refresh = useRefresh();
	const confirm = useConfirm();
	const admin = useIsAdmin();
	const state = useQuery(serverDns01Query(serverId));
	const creds = useQuery(dnsCredentialsQuery(serverId));
	const providers = useQuery(dnsProvidersQuery());
	const [adding, setAdding] = useState(false);
	const [domain, setDomain] = useState("");
	const [wildProvider, setWildProvider] = useState<DnsProvider | "">("");
	const [resolverProvider, setResolverProvider] = useState<DnsProvider | "">(
		"",
	);
	const [resolverCredential, setResolverCredential] = useState("");
	const [apex, setApex] = useState(true);
	const baseId = useId();

	const data = state.data as ServerDns01 | undefined;
	const credentials = ((creds.data as { items?: DnsCredential[] } | undefined)
		?.items ?? []) as DnsCredential[];
	const specs = ((providers.data as { items?: DnsProviderSpec[] } | undefined)
		?.items ?? []) as DnsProviderSpec[];

	const setResolver = useMutation({
		mutationFn: async (v: {
			provider: DnsProvider;
			credentialId: string;
			delaySeconds?: number;
			staging?: boolean;
		}) => {
			await setServerDns01Resolver({
				path: { serverId, provider: v.provider },
				body: {
					credentialId: v.credentialId,
					delaySeconds: v.delaySeconds ?? 0,
					staging: v.staging ?? false,
				},
			});
		},
		onSuccess: async () => {
			await refresh();
			toast.success(
				"Resolver staged",
				"The proxy container is recreated on the next Deploy proxy.",
			);
		},
	});
	const removeResolver = useMutation({
		mutationFn: async (provider: DnsProvider) => {
			await deleteServerDns01Resolver({ path: { serverId, provider } });
		},
		onSuccess: refresh,
	});
	const addWildcard = useMutation({
		mutationFn: async () => {
			await createServerWildcardCert({
				path: { serverId },
				body: {
					domain: domain.trim(),
					provider: wildProvider as DnsProvider,
					includeApex: apex,
				},
			});
		},
		onSuccess: async () => {
			setDomain("");
			await refresh();
			toast.success(
				"Wildcard certificate staged",
				"Requested from Let's Encrypt after the next Deploy proxy.",
			);
		},
	});
	const patchWildcard = useMutation({
		mutationFn: async (v: {
			id: string;
			patch: { coverGenerated?: boolean; default?: boolean };
		}) => {
			await updateWildcardCert({ path: { wildcardId: v.id }, body: v.patch });
		},
		onSuccess: refresh,
	});
	const removeWildcard = useMutation({
		mutationFn: async (id: string) => {
			await deleteWildcardCert({ path: { wildcardId: id } });
		},
		onSuccess: refresh,
	});
	const removeCredential = useMutation({
		mutationFn: async (id: string) => {
			await deleteDnsCredential({ path: { credentialId: id } });
		},
		onSuccess: refresh,
	});

	if (!data)
		return <p className="text-ink-3 text-sm">Loading DNS-01 settings…</p>;
	const providerName = (code: string) =>
		specs.find((s) => s.code === code)?.name ??
		data.resolvers.find((r) => r.provider === code)?.providerName ??
		credentials.find((c) => c.provider === code)?.providerName ??
		code;
	const resolverProviders = data.resolvers
		.filter((r) => r.change !== "remove")
		.map((r) => r.provider);
	const error =
		setResolver.error ??
		addWildcard.error ??
		removeResolver.error ??
		patchWildcard.error ??
		removeWildcard.error ??
		removeCredential.error;
	const usable = (provider: DnsProvider) =>
		credentials.filter(
			(c) =>
				c.provider === provider &&
				(c.serverId === null || c.serverId === serverId),
		);
	// A resolver can be added for a curated provider (the select says when it
	// has no credential yet) and for any lego provider that has a credential.
	const choices = [
		...specs.map((s) => ({ code: s.code, name: s.name })),
		...credentials
			.filter((c) => !specs.some((s) => s.code === c.provider))
			.map((c) => ({ code: c.provider, name: c.providerName })),
	].filter(
		(c, i, all) =>
			!resolverProviders.includes(c.code) &&
			all.findIndex((x) => x.code === c.code) === i,
	);

	return (
		<div className="flex flex-col gap-4">
			<p className="type-secondary">
				Wildcard and private-network certificates through your DNS provider.
				Adding or changing a resolver recreates the proxy container on the next
				Deploy proxy (about a second of dropped connections). Credentials are
				sealed, written to the proxy container's environment at deploy time and
				never shown again.{" "}
				<DocsLink to="operations/dns01-wildcard-certificates" />
			</p>
			{data.acmeEmail === "" ? (
				<Alert variant="warning">
					<AlertTitle>An ACME email is needed first</AlertTitle>
					<AlertDescription>
						Let's Encrypt needs a contact address before it can issue
						certificates. Set one in this server's settings.
					</AlertDescription>
					{onOpenSettings ? (
						<AlertAction>
							<Button variant="secondary" size="sm" onClick={onOpenSettings}>
								Open server settings
							</Button>
						</AlertAction>
					) : null}
				</Alert>
			) : null}
			{error ? <FieldError>{errorMessage(error)}</FieldError> : null}

			<section aria-label="Credentials" className="flex flex-col gap-2">
				<div className="flex items-center gap-2">
					<h4 className="type-label">Credentials</h4>
					<span className="flex-1" />
					<Button
						variant="secondary"
						size="sm"
						disabled={!admin || agentTooOld || specs.length === 0}
						title={
							admin ? undefined : "Only an administrator can add credentials"
						}
						onClick={() => setAdding(true)}
					>
						<PlusIcon aria-hidden />
						Add credential
					</Button>
				</div>
				{adding ? (
					<CredentialForm
						serverId={serverId}
						providers={specs}
						onSaved={refresh}
						onDone={() => setAdding(false)}
					/>
				) : null}
				{credentials.length === 0 ? (
					<p className="text-ink-3 text-sm">No credentials yet.</p>
				) : (
					<ul className="rule-rows">
						{credentials.map((c) => (
							<li
								key={c.id}
								className="flex flex-wrap items-center gap-2 text-sm"
							>
								<KeyRoundIcon aria-hidden className="size-4 text-ink-3" />
								<span className="font-medium text-ink-1">{c.name}</span>
								<Badge shape="tag">{c.providerName}</Badge>
								<Badge shape="tag">
									{c.scope === "team" ? "team" : "this server"}
								</Badge>
								<span className="flex-1" />
								<Button
									variant="ghost"
									size="icon-sm"
									aria-label={`Delete credential ${c.name}`}
									disabled={!admin || c.usedBy.length > 0}
									title={
										c.usedBy.length > 0
											? `Used by ${c.usedBy.map((u) => u.serverName).join(", ")}`
											: undefined
									}
									onClick={() =>
										void confirm({
											title: `Delete ${c.name}?`,
											body: "The sealed values are deleted. Resolvers that still run with it must be removed first.",
											cta: "Delete credential",
											glyph: "trash",
											danger: true,
											hold: false,
											onConfirm: async () => {
												await removeCredential.mutateAsync(c.id);
											},
										})
									}
								>
									<Trash2Icon aria-hidden />
								</Button>
							</li>
						))}
					</ul>
				)}
			</section>

			<section aria-label="Resolvers" className="flex flex-col gap-2">
				<h4 className="type-label">Resolvers on this server</h4>
				<ul className="rule-rows">
					{data.resolvers.map((r) => (
						<li
							key={r.provider}
							className="flex flex-wrap items-center gap-2 text-sm"
						>
							<span className="font-medium text-ink-1">{resolverLabel(r)}</span>
							{r.automatic ? null : (
								<span className="text-ink-3">
									uses {r.credentialName || "(removed)"}
								</span>
							)}
							{r.staging ? (
								<Badge
									shape="tag"
									title="Certificates are not trusted by browsers"
								>
									staging CA
								</Badge>
							) : null}
							{r.rotated ? <Badge shape="tag">credential rotated</Badge> : null}
							<StagedBadge change={r.change} />
							<span className="flex-1" />
							{r.change !== "remove" && !r.automatic ? (
								<>
									<label
										htmlFor={`${baseId}-staging-${r.provider}`}
										className="flex items-center gap-1.5 text-ink-2 text-xs"
									>
										<Switch
											id={`${baseId}-staging-${r.provider}`}
											checked={r.staging}
											disabled={agentTooOld}
											onCheckedChange={(staging) =>
												setResolver.mutate({
													provider: r.provider,
													credentialId: r.credentialId,
													delaySeconds: r.delaySeconds,
													staging,
												})
											}
										/>
										Let's Encrypt staging
									</label>
									<Button
										variant="ghost"
										size="icon-sm"
										aria-label={`Remove the ${r.providerName} resolver`}
										onClick={() => removeResolver.mutate(r.provider)}
									>
										<Trash2Icon aria-hidden />
									</Button>
								</>
							) : null}
						</li>
					))}
				</ul>
				{choices.length > 0 ? (
					<div className="flex flex-wrap items-center gap-2">
						<Select
							value={resolverProvider || null}
							items={choices.map((c) => ({ value: c.code, label: c.name }))}
							onValueChange={(next) => {
								const provider = (next ?? "") as DnsProvider | "";
								setResolverProvider(provider);
								setResolverCredential(
									provider ? (usable(provider)[0]?.id ?? "") : "",
								);
							}}
						>
							<SelectTrigger
								size="sm"
								aria-label="Provider of the new resolver"
								className="w-44"
								disabled={agentTooOld || data.acmeEmail === ""}
							>
								<SelectValue placeholder="Add resolver…" />
							</SelectTrigger>
							<SelectContent alignItemWithTrigger={false} align="start">
								{choices.map((c) => (
									<SelectItem
										key={c.code}
										value={c.code}
										disabled={usable(c.code).length === 0}
									>
										{c.name}
										{usable(c.code).length === 0 ? " · no credential" : ""}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						{resolverProvider ? (
							<Select
								value={resolverCredential || null}
								items={usable(resolverProvider).map((c) => ({
									value: c.id,
									label: c.name,
								}))}
								onValueChange={(next) => setResolverCredential(next ?? "")}
							>
								<SelectTrigger
									size="sm"
									aria-label="Credential for the new resolver"
									className="w-48"
								>
									<SelectValue placeholder="Choose credential" />
								</SelectTrigger>
								<SelectContent alignItemWithTrigger={false} align="start">
									{usable(resolverProvider).map((c) => (
										<SelectItem key={c.id} value={c.id}>
											{c.name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						) : null}
						<Button
							variant="secondary"
							size="sm"
							disabled={
								agentTooOld ||
								data.acmeEmail === "" ||
								!resolverProvider ||
								!resolverCredential ||
								setResolver.isPending
							}
							onClick={() => {
								if (!resolverProvider || !resolverCredential) return;
								setResolver.mutate(
									{
										provider: resolverProvider,
										credentialId: resolverCredential,
									},
									{
										onSuccess: () => {
											setResolverProvider("");
											setResolverCredential("");
										},
									},
								);
							}}
						>
							{setResolver.isPending ? <Spinner /> : <PlusIcon aria-hidden />}
							Add resolver
						</Button>
					</div>
				) : null}
			</section>

			<section
				aria-label="Wildcard certificates"
				className="flex flex-col gap-2"
			>
				<h4 className="type-label">Wildcard certificates</h4>
				{data.wildcards.length === 0 ? (
					<p className="text-ink-3 text-sm">
						None. A wildcard covers every subdomain with one certificate.
					</p>
				) : (
					<ul>
						{data.wildcards.map((w) => (
							<WildcardRow
								key={w.id}
								cert={w}
								disabled={agentTooOld}
								onToggle={(patch) => patchWildcard.mutate({ id: w.id, patch })}
								onRemove={() => removeWildcard.mutate(w.id)}
							/>
						))}
					</ul>
				)}
				{data.baseDomain ? (
					<p className="text-ink-3 text-xs">
						Generated domains of this server live under{" "}
						<span className="font-mono">{data.baseDomain}</span>; a wildcard for
						that base can cover them.
					</p>
				) : null}
				<form
					className="flex flex-wrap items-center gap-2"
					onSubmit={(event) => {
						event.preventDefault();
						if (wildProvider && domain.trim() !== "") addWildcard.mutate();
					}}
				>
					<Input
						aria-label="Base domain of the wildcard certificate"
						className="w-56 font-mono"
						placeholder="example.com"
						autoComplete="off"
						spellCheck={false}
						value={domain}
						onChange={(event) => setDomain(event.target.value)}
					/>
					<Select
						value={wildProvider || null}
						items={resolverProviders.map((p) => ({
							value: p,
							label: providerName(p),
						}))}
						onValueChange={(next) =>
							setWildProvider((next ?? "") as DnsProvider | "")
						}
					>
						<SelectTrigger
							size="sm"
							aria-label="Resolver for the wildcard certificate"
							className="w-44"
							disabled={resolverProviders.length === 0}
						>
							<SelectValue placeholder="Choose resolver" />
						</SelectTrigger>
						<SelectContent alignItemWithTrigger={false} align="start">
							{resolverProviders.map((p) => (
								<SelectItem key={p} value={p}>
									{providerName(p)}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
					<label
						htmlFor={`${baseId}-apex`}
						className="flex items-center gap-1.5 text-ink-2 text-xs"
					>
						<Switch
							id={`${baseId}-apex`}
							checked={apex}
							onCheckedChange={setApex}
						/>
						Include the base domain
					</label>
					<Button
						type="submit"
						variant="secondary"
						size="sm"
						disabled={
							agentTooOld ||
							!wildProvider ||
							domain.trim() === "" ||
							addWildcard.isPending
						}
					>
						{addWildcard.isPending ? <Spinner /> : <PlusIcon aria-hidden />}
						Add wildcard
					</Button>
				</form>
			</section>
		</div>
	);
}
