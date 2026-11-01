// The Settings form of a route service: its working copy, what is wrong with
// it, and the PATCH body of what changed. Pure, so the same rules the control
// plane applies (services/route.go) are tested here.

import type { UpdateRouteInputBody } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import {
	checkRules,
	cleanList,
	sameNetworks,
} from "#/features/access-rules/rules";
import { checkPort, type L4Protocol } from "./l4";
import { validateListenPort, validateUpstreamAddress } from "./validate";

type UpstreamRow =
	| { kind: "address"; address: string }
	| { kind: "service"; serviceId: string; port: string };

export interface RouteForm {
	name: string;
	scheme: "http" | "https";
	insecureSkipVerify: boolean;
	passHost: boolean;
	verifySkip: boolean;
	healthPath: string;
	upstreams: UpstreamRow[];
	listenPort: string;
	/** Allowed networks of a tcp route. */
	cidrs: string[];
	/** Denied networks of a tcp route, refused before the allow list is read. */
	denyCidrs: string[];
	openToInternet: boolean;
	proxyProtocol: 0 | 1 | 2;
}

export const MAX_UPSTREAMS = 20;

export function toRouteForm(service: Service): RouteForm {
	const route = service.config.route;
	return {
		name: service.name,
		scheme: route?.scheme === "https" ? "https" : "http",
		insecureSkipVerify: route?.insecureSkipVerify ?? false,
		passHost: route?.passHost ?? true,
		verifySkip: route?.verifySkip ?? false,
		healthPath: route?.healthPath ?? "",
		upstreams: (route?.upstreams ?? []).map((upstream) =>
			upstream.serviceId
				? {
						kind: "service",
						serviceId: upstream.serviceId,
						port: String(upstream.port ?? ""),
					}
				: { kind: "address", address: upstream.address ?? "" },
		),
		listenPort: route?.listenPort ? String(route.listenPort) : "",
		cidrs: [...(route?.allowCidrs ?? [])],
		denyCidrs: [...(route?.denyCidrs ?? [])],
		openToInternet: route?.openToInternet ?? false,
		proxyProtocol:
			route?.proxyProtocol === 1 || route?.proxyProtocol === 2
				? route.proxyProtocol
				: 0,
	};
}

const wireUpstreams = (rows: readonly UpstreamRow[]) =>
	rows.map((row) =>
		row.kind === "service"
			? { serviceId: row.serviceId, port: Number(row.port) }
			: { address: row.address.trim() },
	);

export interface PortContext {
	taken: readonly {
		protocol: string;
		port: number;
		serviceId: string;
		serviceName: string;
	}[];
	selfServiceId: string;
}

export type RouteFormErrors = Record<string, string>;

/** `upstreams.<i>`, `listenPort`, `cidrs.<i>`, `healthPath`, `name`, `access` and `upstreams` (the list). */
export function validateRouteForm(
	form: RouteForm,
	protocol: "http" | L4Protocol,
	ports?: PortContext,
): RouteFormErrors {
	const errors: RouteFormErrors = {};
	if (form.name.trim() === "") errors.name = "Give the route a name.";
	if (form.upstreams.length === 0) {
		errors.upstreams = "A route needs at least one upstream.";
	} else if (form.upstreams.length > MAX_UPSTREAMS) {
		errors.upstreams = `At most ${MAX_UPSTREAMS} upstreams.`;
	}
	form.upstreams.forEach((row, index) => {
		if (row.kind === "address") {
			const problem = validateUpstreamAddress(row.address);
			if (problem) errors[`upstreams.${index}`] = problem;
		} else {
			const port = Number(row.port);
			if (!(Number.isInteger(port) && port >= 1 && port <= 65535)) {
				errors[`upstreams.${index}`] = "Use a port from 1 to 65535.";
			}
		}
	});
	if (protocol === "http") {
		if (
			form.healthPath.trim() !== "" &&
			!form.healthPath.trim().startsWith("/")
		) {
			errors.healthPath = "Start with /, e.g. /health.";
		}
		return errors;
	}
	const listen = Number(form.listenPort);
	const portProblem = validateListenPort(listen);
	if (portProblem) errors.listenPort = portProblem;
	else if (ports) {
		const check = checkPort(
			protocol,
			listen,
			null,
			ports.taken,
			ports.selfServiceId,
		);
		if (check.state !== "ok") errors.listenPort = check.message;
	}
	if (protocol === "tcp") {
		const check = checkRules(form.cidrs, form.denyCidrs, {
			allowKey: "cidrs",
			allowRequired: !form.openToInternet,
		});
		Object.assign(errors, check.entries);
		if (check.deny) errors.denyCidrs = check.deny;
		if (check.rules) errors.accessRules = check.rules;
		if (check.allow && cleanList(form.cidrs).length > 0) {
			errors.access = check.allow;
		} else if (cleanList(form.cidrs).length === 0 && !form.openToInternet) {
			errors.access =
				"Add an allowed network, or open the port to the internet on purpose.";
		}
	}
	return errors;
}

/** Only what changed, shaped for `PATCH /routes/{id}`. */
export function buildRoutePatch(
	service: Service,
	form: RouteForm,
	protocol: "http" | L4Protocol,
): UpdateRouteInputBody {
	const before = toRouteForm(service);
	const patch: UpdateRouteInputBody = {};
	if (form.name.trim() !== before.name) patch.name = form.name.trim();
	if (
		JSON.stringify(wireUpstreams(form.upstreams)) !==
		JSON.stringify(wireUpstreams(before.upstreams))
	) {
		patch.upstreams = wireUpstreams(form.upstreams);
	}
	if (protocol === "http") {
		if (form.scheme !== before.scheme) patch.scheme = form.scheme;
		if (form.insecureSkipVerify !== before.insecureSkipVerify) {
			patch.insecureSkipVerify = form.insecureSkipVerify;
		}
		if (form.passHost !== before.passHost) patch.passHost = form.passHost;
		if (form.verifySkip !== before.verifySkip)
			patch.verifySkip = form.verifySkip;
		if (form.healthPath.trim() !== before.healthPath) {
			patch.healthPath = form.healthPath.trim();
		}
		return patch;
	}
	if (form.listenPort.trim() !== before.listenPort) {
		patch.listenPort = Number(form.listenPort);
	}
	if (protocol === "tcp") {
		const cidrs = cleanList(form.cidrs);
		if (!sameNetworks(cidrs, before.cidrs)) patch.allowCidrs = cidrs;
		const deny = cleanList(form.denyCidrs);
		if (!sameNetworks(deny, before.denyCidrs)) patch.denyCidrs = deny;
		const open = form.openToInternet && cidrs.length === 0;
		if (open !== before.openToInternet) patch.openToInternet = open;
		if (form.proxyProtocol !== before.proxyProtocol) {
			patch.proxyProtocol = form.proxyProtocol;
		}
	}
	return patch;
}

export function routeDirty(
	service: Service,
	form: RouteForm,
	protocol: "http" | L4Protocol,
): boolean {
	return Object.keys(buildRoutePatch(service, form, protocol)).length > 0;
}
