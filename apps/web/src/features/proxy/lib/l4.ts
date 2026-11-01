// TCP/UDP exposure: the sentences and summaries shown around a port route.

import type { RouteConfig } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";

export type L4Protocol = "tcp" | "udp";

export const RESTART_WARNING =
	"Adding a new port briefly restarts the proxy on this server (about a second of dropped connections).";

export const UDP_WARNING =
	"UDP cannot be restricted by IP: neither an allow list nor a deny list is enforced. Use a firewall or VPN.";

const TCP_OPEN_WARNING =
	"Open to the internet: anyone who can reach this server can connect to this port.";

/** Docker publishes ports around ufw/iptables rules on the host. */
export const FIREWALL_NOTE =
	"Docker publishes ports past a host firewall such as ufw. Restrict access with an allow-list, a cloud firewall or a VPN.";

export interface L4Input {
	protocol: L4Protocol;
	listenPort: number;
	/** The port is not served by this server's proxy yet. */
	newPort: boolean;
	serverName: string;
	allowCidrs: readonly string[];
	denyCidrs: readonly string[];
	openToInternet: boolean;
}

/** The lines the create dialog and the deploy confirmation show for a port exposure. */
export function l4PlanLines(input: L4Input): string[] {
	const lines: string[] = [];
	const label = `${input.protocol.toUpperCase()} ${input.listenPort}`;
	if (input.newPort) {
		lines.push(
			`New ${label}: the proxy on ${input.serverName} restarts once to open it (about a second of dropped connections).`,
		);
	}
	if (input.protocol === "udp") {
		lines.push(UDP_WARNING);
	} else {
		if (input.allowCidrs.length > 0) {
			lines.push(
				`${label} accepts ${input.allowCidrs.length} allowed network${input.allowCidrs.length === 1 ? "" : "s"} only.`,
			);
		} else if (input.openToInternet) {
			lines.push(TCP_OPEN_WARNING);
		}
		if (input.denyCidrs.length > 0) {
			lines.push(
				`${input.denyCidrs.length} denied network${input.denyCidrs.length === 1 ? " is" : "s are"} refused first.`,
			);
		}
	}
	return lines;
}

export type PortCheck =
	| { state: "ok" }
	| { state: "invalid"; message: string }
	| { state: "taken"; message: string };

/**
 * Whether `port` can be listened on, given the server's current ports (the
 * route that already holds it, if any). `selfServiceId` is the route being
 * edited, whose own port does not conflict with itself.
 */
export function checkPort(
	protocol: L4Protocol,
	port: number,
	invalid: string | null,
	taken: readonly {
		protocol: string;
		port: number;
		serviceId: string;
		serviceName: string;
	}[],
	selfServiceId?: string,
): PortCheck {
	if (invalid) return { state: "invalid", message: invalid };
	const holder = taken.find(
		(entry) =>
			entry.protocol === protocol &&
			entry.port === port &&
			entry.serviceId !== selfServiceId,
	);
	if (holder) {
		return {
			state: "taken",
			message: holder.serviceName
				? `${protocol.toUpperCase()} ${port} is already published by ${holder.serviceName}.`
				: `${protocol.toUpperCase()} ${port} is already in use on this server.`,
		};
	}
	return { state: "ok" };
}

/** `192.168.1.20:8123` or `192.168.1.20:8123 +2`, from an http route's upstreams. */
function upstreamSummary(
	route: Pick<RouteConfig, "upstreams">,
	nameOf: (serviceId: string) => string | undefined,
): string {
	const items = (route.upstreams ?? []).map((upstream) => {
		if (upstream.serviceId) {
			const name = nameOf(upstream.serviceId) ?? "service";
			return upstream.port ? `${name}:${upstream.port}` : name;
		}
		return upstream.address ?? "";
	});
	const first = items[0] ?? "";
	return items.length > 1 ? `${first} +${items.length - 1}` : first;
}

/** The canvas card's subtitle: `192.168.1.20:8123` or `tcp :25565 -> minecraft:25565`. */
export function routeSubtitle(
	route: RouteConfig | undefined,
	nameOf: (serviceId: string) => string | undefined,
	fallback = "external route",
): string {
	if (!route) return fallback;
	const target = upstreamSummary(route, nameOf);
	if (route.protocol === "tcp" || route.protocol === "udp") {
		const port = route.listenPort ? ` :${route.listenPort}` : "";
		return `${route.protocol}${port} -> ${target || "no target"}`;
	}
	return target || fallback;
}

/** The protocol badge on a route node. */
export function routeBadge(
	route: RouteConfig | undefined,
): "HTTP" | "TCP" | "UDP" {
	if (route?.protocol === "tcp") return "TCP";
	if (route?.protocol === "udp") return "UDP";
	return "HTTP";
}

/** `10.0.0.0/8` or `10.0.0.0/8 +2`: the first network and how many more. */
function networkList(cidrs: readonly string[]): string {
	const first = cidrs[0] ?? "";
	return cidrs.length > 1 ? `${first} +${cidrs.length - 1}` : first;
}

/**
 * Who may reach a port, in one line: "Allow 10.0.0.0/8, deny 10.1.0.0/16",
 * "Open to the internet, deny 203.0.113.0/24", "Open to the internet", or the
 * UDP caveat (UDP cannot enforce either list).
 */
export function accessSummary(input: {
	protocol: L4Protocol;
	allowCidrs?: readonly string[] | null | undefined;
	denyCidrs?: readonly string[] | null | undefined;
}): string {
	if (input.protocol === "udp") return "No IP filter (UDP)";
	const allow = input.allowCidrs ?? [];
	const deny = input.denyCidrs ?? [];
	const head =
		allow.length > 0 ? `Allow ${networkList(allow)}` : "Open to the internet";
	return deny.length > 0 ? `${head}, deny ${networkList(deny)}` : head;
}

export interface Exposure {
	route: Service;
	protocol: L4Protocol;
	listenPort: number;
	targetPort: number;
	/** `accessSummary` of the route. */
	access: string;
}

/** The route services in `siblings` that publish a port of `serviceId`. */
export function exposuresOf(
	serviceId: string,
	siblings: readonly Service[],
): Exposure[] {
	const out: Exposure[] = [];
	for (const route of siblings) {
		const config = route.config.route;
		if (route.kind !== "route" || !config) continue;
		if (config.protocol !== "tcp" && config.protocol !== "udp") continue;
		const upstream = (config.upstreams ?? []).find(
			(candidate) => candidate.serviceId === serviceId,
		);
		if (!upstream) continue;
		out.push({
			route,
			protocol: config.protocol,
			listenPort: config.listenPort ?? 0,
			targetPort: upstream.port ?? 0,
			access: accessSummary({
				protocol: config.protocol,
				allowCidrs: config.allowCidrs,
				denyCidrs: config.denyCidrs,
			}),
		});
	}
	return out.sort((a, b) => a.listenPort - b.listenPort);
}
