// Cloudflare slice: the team's connection, its zones and the live DNS status of
// a domain. The generated client is the transport.

import {
	getCloudflareIntegrationOptions,
	getCloudflareIpsOptions,
	getDomainDnsOptions,
	listCloudflareZonesOptions,
} from "#/api/gen/@tanstack/react-query.gen";

export const cloudflareQuery = () => getCloudflareIntegrationOptions();

export const cloudflareZonesQuery = () => listCloudflareZonesOptions();

/** The IP ranges the origin lock allows: source, size, age and refresh state. */
export const cloudflareIpsQuery = () => getCloudflareIpsOptions();

/** Live from Cloudflare: reads the zone, so it is only asked for while a domain is open. */
export const domainDnsQuery = (domainId: string) =>
	getDomainDnsOptions({ path: { domainId } });

/** Everything a connection or DNS change makes stale. */
export const CLOUDFLARE_OPERATIONS = [
	"getCloudflareIntegration",
	"getCloudflareIps",
	"listCloudflareZones",
	"getDomainDns",
	"listDomains",
	"listServices",
	"getService",
] as const;
