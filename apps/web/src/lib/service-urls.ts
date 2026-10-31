import type { Service, ServiceDomain } from "#/api/models";

/** What the URL of a domain depends on: its host, path, TLS mode and observed certificate. */
export type DomainUrlFields = Pick<ServiceDomain, "hostname"> &
	Partial<Pick<ServiceDomain, "pathPrefix" | "tlsMode">> & {
		cert?: { source: string } | null;
	};

/** `http` when TLS is off for the domain or it has no certificate, `https` otherwise. */
export function domainScheme(domain: DomainUrlFields): "https" | "http" {
	return domain.tlsMode === "none" || domain.cert?.source === "none"
		? "http"
		: "https";
}

/**
 * Where a domain is served. The URL parser normalises the host, so an IDN
 * (`bücher.example`) becomes its punycode form and a punycode host stays as it
 * is; a host the parser rejects is returned as typed.
 */
export function domainUrl(domain: DomainUrlFields): string {
	const raw = `${domainScheme(domain)}://${domain.hostname}${domain.pathPrefix ?? ""}`;
	try {
		return new URL(raw).href;
	} catch {
		return raw;
	}
}

/** A place a service answers in a browser. */
export interface OpenTarget {
	/** The host as shown: the domain as typed, or the tailnet node's host. */
	host: string;
	url: string;
	/** Reachable only from the tailnet. */
	tailnetOnly: boolean;
}

/**
 * Every address of a service worth opening: its public domains in order, then
 * its tailnet node's web address while the node is online. With "Tailnet only"
 * on, the public domains are not routed, so only the tailnet address is left.
 */
export function openTargets(
	service: Pick<Service, "domains"> & { tailnet?: Service["tailnet"] },
): OpenTarget[] {
	const tailnet = service.tailnet ?? null;
	const targets: OpenTarget[] = [];
	if (!tailnet?.hidePublic) {
		for (const domain of service.domains) {
			// A wildcard (`*.example.com`) names no single page to open.
			if (domain.hostname === "" || domain.hostname.includes("*")) continue;
			targets.push({
				host: domain.hostname,
				url: domainUrl(domain),
				tailnetOnly: false,
			});
		}
	}
	if (tailnet?.state === "online" && tailnet.url !== "") {
		try {
			const url = new URL(tailnet.url);
			if (url.protocol === "https:" || url.protocol === "http:") {
				targets.push({ host: url.host, url: url.href, tailnetOnly: true });
			}
		} catch {
			// A node address that does not parse is not offered.
		}
	}
	return targets;
}
