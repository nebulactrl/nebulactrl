import type { ProxyL4Port, ProxyRoute } from "#/api/gen/types.gen";
import { Badge } from "#/components/ui/badge";
import { StatusIcon } from "#/components/ui/status-icon";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";

/** The ports the proxy listens on besides 80 and 443, and who holds each. */
export function L4PortsTable({ ports }: { ports: readonly ProxyL4Port[] }) {
	if (ports.length === 0) {
		return (
			<p className="type-secondary">
				No TCP or UDP ports are published. Add one from Create → TCP/UDP port.
			</p>
		);
	}
	return (
		<div className="overflow-hidden border-line border-y">
			<Table stack>
				<TableHeader>
					<TableRow>
						<TableHead>Protocol</TableHead>
						<TableHead>Port</TableHead>
						<TableHead>Service</TableHead>
						<TableHead>State</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					{ports.map((port) => (
						<TableRow key={`${port.protocol}-${port.port}`}>
							<TableCell pin="start">
								<Badge shape="tag">{port.protocol.toUpperCase()}</Badge>
							</TableCell>
							<TableCell
								label="Port"
								className="font-mono text-ink-1 tabular-nums"
							>
								{port.port}
							</TableCell>
							<TableCell label="Service" className="text-ink-2">
								{port.serviceName || "another project"}
							</TableCell>
							<TableCell label="State">
								{port.deployed ? (
									<span className="inline-flex items-center gap-1.5 text-ink-1 text-xs">
										<StatusIcon kind="live" label="" />
										Listening
									</span>
								) : (
									<span
										className="inline-flex items-center gap-1.5 text-ink-2 text-xs"
										title="Reserved; the proxy opens it when the route is deployed"
									>
										<StatusIcon kind="pending" label="" />
										Reserved
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

function routeTarget(route: ProxyRoute): string {
	const hosts = route.hosts ?? [];
	if (route.protocol !== "http") {
		return `${route.protocol} :${route.listenPort ?? "?"}`;
	}
	const first = hosts[0];
	if (!first) return "no hostname";
	const path = first.pathPrefix || "";
	return hosts.length > 1
		? `${first.host}${path} +${hosts.length - 1}`
		: `${first.host}${path}`;
}

/** Per-host policy chips of an http route: what protects it, what it redirects to. */
function HostChips({ route }: { route: ProxyRoute }) {
	const hosts = route.hosts ?? [];
	if (route.protocol !== "http") {
		const allowed = route.allowCidrs?.length ?? 0;
		const denied = route.denyCidrs?.length ?? 0;
		return (
			<span className="flex flex-wrap gap-1">
				{route.protocol === "udp" ? (
					<Badge tone="warn" shape="tag">
						no IP filter
					</Badge>
				) : allowed === 0 ? (
					<Badge tone="warn" shape="tag">
						open to the internet
					</Badge>
				) : (
					<Badge tone="ok" shape="tag">
						{allowed} allowed
					</Badge>
				)}
				{route.protocol !== "udp" && denied > 0 ? (
					<Badge shape="tag">{denied} denied</Badge>
				) : null}
			</span>
		);
	}
	const chips = new Set<string>();
	for (const host of hosts) {
		if (host.kind === "redirect") chips.add(`→ ${host.redirectTo ?? "?"}`);
		if (host.basicAuth) chips.add("basic auth");
		if (host.accessList) chips.add("IP list");
		if (host.rateLimit) chips.add("rate limit");
		if (host.tlsMode !== "auto") chips.add(`tls ${host.tlsMode}`);
	}
	if (
		route.kind === "external" &&
		!hosts.some((h) => h.basicAuth || h.accessList)
	) {
		chips.add("no access control");
	}
	return (
		<span className="flex flex-wrap gap-1">
			{[...chips].map((chip) => (
				<Badge
					key={chip}
					shape="tag"
					{...(chip === "no access control" ? { tone: "warn" as const } : {})}
				>
					{chip}
				</Badge>
			))}
		</span>
	);
}

/** The route table the proxy serves: one row per service or external route. */
export function RoutesTable({ routes }: { routes: readonly ProxyRoute[] }) {
	if (routes.length === 0) {
		return (
			<p className="type-secondary">
				Nothing is routed through this proxy yet.
			</p>
		);
	}
	return (
		<div className="overflow-hidden border-line border-y">
			<Table stack>
				<TableHeader>
					<TableRow>
						<TableHead>Route</TableHead>
						<TableHead>Listens on</TableHead>
						<TableHead>Forwards to</TableHead>
						<TableHead>Policy</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					{routes.map((route) => (
						<TableRow
							key={`${route.serviceId}-${route.protocol}-${route.listenPort ?? ""}`}
						>
							<TableCell pin="start">
								<span className="flex items-center gap-2">
									<span className="font-medium text-ink-1">
										{route.serviceName}
									</span>
									<Badge shape="tag">{route.protocol.toUpperCase()}</Badge>
									{route.kind === "external" ? (
										<Badge shape="tag">external</Badge>
									) : null}
								</span>
							</TableCell>
							<TableCell
								label="Listens on"
								className="font-mono text-ink-2 text-xs"
							>
								{routeTarget(route)}
							</TableCell>
							<TableCell
								label="Forwards to"
								className="font-mono text-ink-3 text-xs"
							>
								{(route.upstreams ?? []).join(", ") || "—"}
							</TableCell>
							<TableCell label="Policy">
								<HostChips route={route} />
							</TableCell>
						</TableRow>
					))}
				</TableBody>
			</Table>
		</div>
	);
}
