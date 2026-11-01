import { useQuery } from "@tanstack/react-query";
import { GlobeIcon } from "lucide-react";
import { planDeployment } from "#/api/gen/sdk.gen";
import {
	deploymentQuery,
	pendingChangesQuery,
	servicesQuery,
} from "#/api/hooks";
import { toPlan } from "#/api/models";
import { CopyButton } from "#/components/common/copy-button";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { useInspectorActions } from "#/features/canvas/inspector/inspector-context";
import { LatestDeployments } from "#/features/canvas/inspector/overview/latest-deployments";
import { OverviewSection } from "#/features/canvas/inspector/overview/section";
import type { ServiceTabProps } from "#/features/canvas/inspector/tabs/types";
import { RecoveryBanner } from "#/features/deployments/recovery-banner";
import { accessSummary, routeBadge, UDP_WARNING } from "../lib/l4";

function Row({
	label,
	children,
}: {
	label: string;
	children: React.ReactNode;
}) {
	return (
		<div className="grid grid-cols-[110px_minmax(0,1fr)] items-baseline gap-3 border-line border-b py-2 text-[13px] last:border-0">
			<dt className="text-ink-3">{label}</dt>
			<dd className="min-w-0 text-ink-1">{children}</dd>
		</div>
	);
}

/**
 * Overview of a route service: what it forwards to, who may reach it, the
 * domains it answers for, the plan's cautions and the latest deployments.
 * No metrics, logs or scaling: a route has no container.
 */
export function RouteOverviewTab({ service }: ServiceTabProps) {
	const { openTab } = useInspectorActions();
	const route = service.config.route;
	const siblings = useQuery(servicesQuery(service.environmentId));
	const nameOf = (id: string) => siblings.data?.find((s) => s.id === id)?.name;
	const latestId = service.latestDeployment?.id;
	const latest = useQuery({
		...deploymentQuery(latestId ?? ""),
		enabled: latestId !== undefined,
	});
	const pending = useQuery({
		...pendingChangesQuery(service.id),
		enabled: service.hasStagedChanges,
	});
	const plan = useQuery({
		queryKey: ["plan", service.id, service.generation, service.serverId],
		queryFn: async ({ signal }) => {
			const { data } = await planDeployment({
				path: { serviceId: service.id },
				signal,
			});
			return toPlan(data);
		},
		enabled: service.serverId !== null,
		refetchInterval: false,
		retry: false,
	});

	const protocol = route?.protocol ?? "http";
	const badge = routeBadge(route);
	const upstreams = (route?.upstreams ?? []).map((upstream) =>
		upstream.serviceId
			? `${nameOf(upstream.serviceId) ?? "managed service"}:${upstream.port ?? ""}`
			: (upstream.address ?? ""),
	);
	const access =
		protocol === "udp"
			? UDP_WARNING
			: protocol === "tcp"
				? accessSummary({
						protocol,
						allowCidrs: route?.allowCidrs,
						denyCidrs: route?.denyCidrs,
					})
				: null;

	return (
		<section
			data-tab="overview"
			data-service-id={service.id}
			aria-label="Overview"
			className="flex min-h-full flex-col gap-5 px-4 pt-4 pb-6"
		>
			{latest.data ? (
				<RecoveryBanner deployment={latest.data} service={service} />
			) : null}

			{service.hasStagedChanges ? (
				<Alert variant="warning" role="status">
					<AlertTitle>
						{service.stagedKind === "new"
							? "Not published yet"
							: "Staged changes"}
					</AlertTitle>
					<AlertDescription>
						Deploy to publish this route
						{(pending.data?.length ?? 0) > 0
							? `: ${pending.data?.length} change${pending.data?.length === 1 ? "" : "s"} waiting`
							: ""}
						. No build runs: the proxy on {service.server?.name ?? "the server"}{" "}
						receives the new route table.
					</AlertDescription>
				</Alert>
			) : null}

			<OverviewSection
				title="Route"
				action={{ label: "Settings", onClick: () => openTab("settings") }}
			>
				<dl>
					<Row label="Type">
						<Badge shape="tag">{badge}</Badge>
						<span className="ml-2 text-ink-3 text-xs">
							{protocol === "http"
								? `${route?.scheme ?? "http"} to the service`
								: `port ${route?.listenPort ?? "?"} on ${service.server?.name ?? "the server"}`}
						</span>
					</Row>
					<Row label={upstreams.length > 1 ? "Upstreams" : "Upstream"}>
						<span className="flex flex-col gap-1">
							{upstreams.map((upstream) => (
								<span key={upstream} className="flex items-center gap-1.5">
									<code className="min-w-0 truncate font-mono text-xs">
										{upstream}
									</code>
									<CopyButton
										value={upstream}
										label={`Copy ${upstream}`}
										variant="ghost"
										size="icon-xs"
									/>
								</span>
							))}
						</span>
					</Row>
					{access ? <Row label="Access">{access}</Row> : null}
					{protocol === "http" && route?.insecureSkipVerify ? (
						<Row label="TLS">
							<span className="text-warning-text">
								Upstream certificate is not verified
							</span>
						</Row>
					) : null}
					{route?.healthPath ? (
						<Row label="Health path">
							<code className="font-mono text-xs">{route.healthPath}</code>
						</Row>
					) : null}
					<Row label="Server">{service.server?.name ?? "Not placed"}</Row>
				</dl>
			</OverviewSection>

			{protocol === "http" ? (
				<OverviewSection title="Domains">
					{service.domains.length === 0 ? (
						<p className="type-secondary">
							No hostname yet: the route has no public address. Add one in
							Settings.
						</p>
					) : (
						<ul className="flex flex-col">
							{service.domains.map((domain) => (
								<li
									key={domain.id}
									className="flex items-center gap-2 border-line border-b py-2 text-[13px] last:border-0"
								>
									<GlobeIcon aria-hidden className="size-3.5 text-ink-3" />
									<a
										href={`https://${domain.hostname}${domain.pathPrefix}`}
										target="_blank"
										rel="noreferrer"
										className="min-w-0 truncate font-mono text-accent-ink hover:underline"
									>
										{domain.hostname}
										{domain.pathPrefix}
									</a>
									{domain.kind === "redirect" ? (
										<span className="font-mono text-ink-3 text-xs">
											→ {domain.redirectTo}
										</span>
									) : null}
									<Badge
										className="ml-auto"
										tone={
											domain.status === "active"
												? "live"
												: domain.status === "error"
													? "failed"
													: "warn"
										}
										lamp
									>
										{domain.status}
									</Badge>
								</li>
							))}
						</ul>
					)}
				</OverviewSection>
			) : null}

			{plan.data &&
			(plan.data.warnings.length > 0 ||
				plan.data.blocking.length > 0 ||
				plan.data.reasons.length > 0) ? (
				<OverviewSection title="Before you deploy">
					<div className="flex flex-col gap-2">
						{plan.data.reasons.length > 0 ? (
							<ul className="flex flex-col gap-1.5">
								{plan.data.reasons.map((reason) => (
									<li key={reason} className="text-ink-2 text-xs leading-[1.5]">
										{reason}
									</li>
								))}
							</ul>
						) : null}
						{plan.data.warnings.map((warning) => (
							<Alert key={warning} variant="warning">
								<AlertDescription>{warning}</AlertDescription>
							</Alert>
						))}
						{plan.data.blocking.map((block) => (
							<Alert key={block.code} variant="destructive">
								<AlertDescription>{block.message}</AlertDescription>
							</Alert>
						))}
					</div>
				</OverviewSection>
			) : null}

			<OverviewSection
				title="Latest deployments"
				action={{ label: "View all", onClick: () => openTab("deployments") }}
			>
				<LatestDeployments service={service} />
			</OverviewSection>
		</section>
	);
}
