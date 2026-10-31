import { useQuery } from "@tanstack/react-query";
import { cn } from "cn";
import { ShieldCheckIcon } from "lucide-react";
import { useId } from "react";
import { errorMessage } from "#/api/client-config";
import type { Service, ServiceDomain } from "#/api/models";
import { Badge } from "#/components/ui/badge";
import { FieldError } from "#/components/ui/field";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { StatusIcon } from "#/components/ui/status-icon";
import {
	certificateLine,
	type DnsProvider,
	expiryLabel,
	expiryTone,
	isWildcardHost,
	resolverLabel,
	type ServerDns01,
	serverDns01Query,
} from "#/features/dns01/queries";
import {
	LOCAL_CERTIFICATE_NOTE,
	SAVES_STAGED,
	showsCertificateStatus,
} from "./domain-copy";
import { useDomainSave } from "./use-domain-patch";

/** The "no resolver" choice: HTTP-01, or nothing chosen yet for a wildcard. */
const NO_RESOLVER = "__http01__";

/**
 * Where a domain's certificate comes from: HTTP-01 by default, a covering
 * wildcard certificate of the server, or one DNS-01 resolver the server runs.
 * Shows the observed status and expiry. Choosing a resolver stages like every
 * domain change.
 */
export function CertificateSection({
	service,
	domain,
	disabled,
}: {
	service: Service;
	domain: ServiceDomain;
	disabled: boolean;
}) {
	const save = useDomainSave(service);
	const resolverId = useId();
	const serverId = service.serverId;
	const dns = useQuery({
		...serverDns01Query(serverId ?? ""),
		enabled: serverId !== null,
	});
	const state = dns.data as ServerDns01 | undefined;
	const resolvers = (state?.resolvers ?? []).filter(
		(r) => r.change !== "remove",
	);
	const cert = domain.cert ?? null;
	const wildcardHost = isWildcardHost(domain.hostname);
	const current = domain.dnsResolver ?? "";
	const tone = expiryTone(cert?.daysToExpiry ?? null);
	const line = cert ? certificateLine(cert) : null;
	const tlsOff = domain.tlsMode !== "auto";

	if (!showsCertificateStatus(domain.hostname, cert, !tlsOff)) {
		return (
			<div className="flex flex-col gap-2" data-section="certificate">
				<div className="flex flex-wrap items-center gap-2">
					<ShieldCheckIcon aria-hidden className="size-4 text-ink-3" />
					<span className="font-medium text-[13px] text-ink-1">
						Certificate
					</span>
				</div>
				<p className="text-ink-3 text-xs">{LOCAL_CERTIFICATE_NOTE}</p>
			</div>
		);
	}

	return (
		<div className="flex flex-col gap-2" data-section="certificate">
			<div className="flex flex-wrap items-center gap-2">
				<ShieldCheckIcon aria-hidden className="size-4 text-ink-3" />
				<span className="font-medium text-[13px] text-ink-1">Certificate</span>
				{cert && line ? (
					<>
						<Badge tone={line.badge.tone}>
							<StatusIcon kind={line.badge.kind} label="" />
							{line.badge.label}
						</Badge>
						<span className="text-ink-3 text-xs">{line.source}</span>
					</>
				) : null}
			</div>
			{cert?.status === "active" && cert.notAfter ? (
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
					{cert.issuer ? `${cert.issuer} · ` : ""}expires{" "}
					{cert.notAfter.slice(0, 10)} ({expiryLabel(cert.daysToExpiry)})
				</p>
			) : null}
			{cert?.message ? (
				<p
					className={cn(
						"text-xs",
						cert.status === "error" ? "text-destructive-text" : "text-ink-3",
					)}
				>
					{cert.message}
				</p>
			) : null}
			{cert?.warning ? (
				<p className="text-warning-text text-xs" data-testid="http01-warning">
					{cert.warning}
				</p>
			) : null}
			{line?.reason ? (
				<p className="text-ink-3 text-xs" data-testid="cloudflare-dns01-reason">
					{line.reason}
				</p>
			) : null}
			{wildcardHost &&
			current === "" &&
			cert?.source !== "wildcard" &&
			!line?.automatic &&
			!tlsOff ? (
				<p className="text-warning-text text-xs">
					A wildcard hostname needs a DNS-01 resolver or a wildcard certificate
					of the server; HTTP-01 cannot validate it.
				</p>
			) : null}
			{tlsOff || cert?.source === "wildcard" || line?.automatic ? null : (
				<div className="flex flex-wrap items-center gap-2 text-ink-2 text-xs">
					<span id={`${resolverId}-label`}>Obtain it through</span>
					<Select
						value={current === "" ? NO_RESOLVER : current}
						items={[
							{
								value: NO_RESOLVER,
								label: wildcardHost
									? "Choose a resolver…"
									: "HTTP-01 (automatic)",
							},
							...resolvers.map((r) => ({
								value: r.provider,
								label: `DNS-01 · ${resolverLabel(r)}${r.change === "add" ? " (staged)" : ""}`,
							})),
						]}
						disabled={disabled || save.isPending || serverId === null}
						onValueChange={(next) => {
							if (next === null) return;
							save.mutate({
								domainId: domain.id,
								body: {
									dnsResolver: (next === NO_RESOLVER ? "" : next) as
										| DnsProvider
										| "",
								},
							});
						}}
					>
						<SelectTrigger
							id={resolverId}
							aria-labelledby={`${resolverId}-label`}
							className="min-w-[14rem]"
						>
							<SelectValue />
						</SelectTrigger>
						<SelectContent alignItemWithTrigger={false}>
							<SelectItem value={NO_RESOLVER}>
								{wildcardHost ? "Choose a resolver…" : "HTTP-01 (automatic)"}
							</SelectItem>
							{resolvers.map((r) => (
								<SelectItem key={r.provider} value={r.provider}>
									DNS-01 · {resolverLabel(r)}
									{r.change === "add" ? " (staged)" : ""}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
					<span className="text-ink-3">{SAVES_STAGED}</span>
					{resolvers.length === 0 ? (
						<span className="text-ink-3">
							Add a resolver under Servers, Proxy, DNS-01 certificates.
						</span>
					) : null}
				</div>
			)}
			{save.isError ? (
				<FieldError>{errorMessage(save.error)}</FieldError>
			) : null}
		</div>
	);
}
