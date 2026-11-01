import { CornerDownRightIcon, GlobeIcon, Trash2Icon } from "lucide-react";
import { useId, useState } from "react";
import type { Service, ServiceDomain } from "#/api/models";
import { NewNameNote } from "#/components/common/new-name-note";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import { type LampTone, statusKindOfTone } from "#/components/ui/lamp";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";
import { dnsHeader, dnsStateInfo } from "#/features/cloudflare/model";
import { domainProcessName, hasProcesses } from "#/features/processes/model";
import { FieldMark } from "#/features/project-config";
import { type RuleContext, redirectFamily } from "#/features/proxy/lib/rule";
import type { DomainStaged } from "#/features/proxy/lib/staged";
import { domainTarget } from "#/features/stacks/model";
import { isNewName } from "#/lib/dns-new-name";
import { domainUrl } from "#/lib/service-urls";
import { CertificateSection } from "./certificate-section";
import { CloudflareDnsSection } from "./cloudflare-dns-section";
import { DomainOptions } from "./domain-options";
import { RuleSection } from "./rule-section";

const DOMAIN_STATE: Record<
	ServiceDomain["status"],
	{ tone: LampTone; kind: StatusKind }
> = {
	active: { tone: "ok", kind: "healthy" },
	pending: { tone: "warn", kind: "pending" },
	error: { tone: "bad", kind: "failed" },
};

/**
 * One domain of a service as a card: the hostname and its states on top, and
 * when opened its rule (where it points, or the redirect it answers with)
 * and the collapsible option groups. Removing a domain stages the removal.
 */
export function DomainEditor({
	service,
	domain,
	publicIp,
	staged,
	context,
	agentTooOld,
	onRemove,
	removing,
}: {
	service: Service;
	domain: ServiceDomain;
	publicIp: string | null;
	staged: DomainStaged;
	context: RuleContext;
	agentTooOld: boolean;
	onRemove(): void;
	removing: boolean;
}) {
	const [open, setOpen] = useState(false);
	const bodyId = useId();
	const dns = dnsHeader(domain, publicIp);
	const cloudflare = dnsStateInfo(domain.dns?.status);
	const redirect = domain.kind === "redirect";
	const title = `${domain.hostname}${domain.pathPrefix}`;
	const anyStaged = staged.added || staged.buckets.size > 0;

	return (
		<li className="surface-raised flex flex-col rounded-card">
			<div className="flex items-start gap-2 px-3 py-2.5">
				<button
					type="button"
					aria-expanded={open}
					aria-controls={bodyId}
					aria-label={`${open ? "Collapse" : "Edit"} ${title}`}
					onClick={() => setOpen(!open)}
					className="mt-0.5 grid size-6 shrink-0 cursor-pointer place-items-center rounded-sm text-ink-3 outline-none hover:text-ink-1 focus-visible:focus-ring"
				>
					<DisclosureChevron open={open} className="size-4" />
				</button>
				<div className="min-w-0 flex-1">
					<div className="flex flex-wrap items-center gap-2">
						{redirect ? (
							<CornerDownRightIcon
								aria-hidden
								className="size-4 shrink-0 text-ink-3"
							/>
						) : (
							<GlobeIcon aria-hidden className="size-4 shrink-0 text-ink-3" />
						)}
						<a
							href={domainUrl(domain)}
							target="_blank"
							rel="noopener noreferrer"
							className="truncate rounded-sm font-mono text-[13px] text-accent-ink hover:underline focus-visible:focus-ring"
						>
							{title}
						</a>
						<FieldMark field={`domains.${title}`} exact />
						{redirect ? (
							<span className="font-mono text-[13px] text-ink-3">
								→ {domain.redirectTo}
							</span>
						) : null}
						<Badge tone={DOMAIN_STATE[domain.status].tone}>
							<StatusIcon kind={DOMAIN_STATE[domain.status].kind} label="" />
							{domain.status}
						</Badge>
						<Badge tone={dns.tone}>
							<StatusIcon kind={dns.kind} label="" />
							{dns.label}
						</Badge>
						{cloudflare && domain.dns?.status !== "conflict" ? (
							<Badge
								tone={cloudflare.tone}
								title={domain.dns?.message || cloudflare.hint}
							>
								<StatusIcon kind={statusKindOfTone(cloudflare.tone)} label="" />
								Cloudflare · {cloudflare.label}
							</Badge>
						) : null}
						{domain.generated ? <Badge>generated</Badge> : null}
						{domainTarget(domain) ? (
							<Badge title="The component and container port this domain routes to">
								{domainTarget(domain)}
							</Badge>
						) : null}
						{hasProcesses(service) ? (
							<Badge title="The process this domain routes to">
								serves {domainProcessName(service, domain.process)}
							</Badge>
						) : null}
						{redirect ? (
							<Badge>{redirectFamily(domain.redirectCode)} redirect</Badge>
						) : null}
						{domain.wwwRedirect ? (
							<Badge title="The www/apex counterpart is routed and redirects here">
								+ {domain.wwwPartner} redirect
							</Badge>
						) : null}
						{domain.wwwRedirectFor ? (
							<Badge title="Kept by the www redirect option of the target domain">
								www redirect
							</Badge>
						) : null}
						{domain.tlsMode !== "auto" ? (
							<Badge>tls {domain.tlsMode}</Badge>
						) : null}
						{anyStaged ? (
							<Badge
								tone="staged"
								title={`Staged: generation ${service.generation}, deployed ${service.appliedGeneration}`}
							>
								{staged.added ? "New" : "Staged"}
							</Badge>
						) : null}
					</div>
					<p
						className={`mt-1 text-xs leading-[1.5] ${dns.tone === "warn" ? "text-warning-ink" : "text-ink-3"}`}
					>
						{dns.line}
					</p>
					{dns.tone === "ok" &&
					!domain.generated &&
					isNewName(domain.createdAt, Date.now()) ? (
						<div className="mt-1">
							<NewNameNote />
						</div>
					) : null}
				</div>
				<Button
					variant="ghost"
					size="icon-sm"
					aria-label={`Remove ${title}`}
					disabled={removing}
					onClick={onRemove}
				>
					<Trash2Icon aria-hidden />
				</Button>
			</div>
			<DisclosurePanel open={open} id={bodyId}>
				<div className="flex flex-col gap-4 border-line border-t px-3 py-3.5">
					<RuleSection
						service={service}
						domain={domain}
						context={context}
						disabled={agentTooOld}
					/>
					<CloudflareDnsSection domain={domain} />
					{redirect ? null : (
						<CertificateSection
							service={service}
							domain={domain}
							disabled={agentTooOld}
						/>
					)}
					{redirect ? (
						<p className="text-ink-3 text-xs leading-[1.5]">
							A redirect answers every request itself, so it has no access
							rules, headers or limits.
						</p>
					) : (
						<DomainOptions
							service={service}
							domain={domain}
							staged={staged}
							disabled={agentTooOld}
						/>
					)}
				</div>
			</DisclosurePanel>
		</li>
	);
}
