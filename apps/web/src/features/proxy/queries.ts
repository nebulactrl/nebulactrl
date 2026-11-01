// Queries and invalidation for the proxy slice. The generated options already
// carry the `_id` SSE invalidation looks up, so these are thin wrappers that
// add what the generator cannot know: which ones poll while a change is in
// flight.

import type { QueryClient } from "@tanstack/react-query";
import {
	getServerProxyOptions,
	getTailscaleIntegrationOptions,
	listCertificatesOptions,
	listServerPortsOptions,
	listServerProxyRoutesOptions,
	listTailscaleDevicesOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import type { ServerProxy } from "#/api/gen/types.gen";
import { invalidateOperations, invalidateServices } from "#/api/invalidate";

/** The proxy's observed and staged state; polls while a deploy or provisioning is under way. */
export const serverProxyQuery = (serverId: string) => ({
	...getServerProxyOptions({ path: { serverId } }),
	refetchInterval: (query: { state: { data: ServerProxy | undefined } }) => {
		const data = query.state.data;
		if (!data) return false;
		const busy =
			data.lastDeploy?.status === "pending" ||
			data.lastDeploy?.status === "sent" ||
			data.lastDeploy?.status === "acked" ||
			data.tailnet.status?.state === "provisioning";
		return busy ? 2_000 : false;
	},
});

export const serverCertificatesQuery = (serverId: string) =>
	listCertificatesOptions({ path: { serverId } });

export const proxyRoutesQuery = (serverId: string) =>
	listServerProxyRoutesOptions({ path: { serverId } });

/** Ports (TCP/UDP) already published or reserved on a server. */
export const serverPortsQuery = (serverId: string) =>
	listServerPortsOptions({ path: { serverId } });

export const tailscaleQuery = () => getTailscaleIntegrationOptions();

export const tailscaleDevicesQuery = () => listTailscaleDevicesOptions();

const PROXY_OPERATIONS = [
	"getServerProxy",
	"listCertificates",
	"listServerProxyRoutes",
	"listServerPorts",
	"getServer",
	"listServers",
	"getServerDns01",
	"listDnsCredentials",
] as const;

/** After a staged or applied server-level change: the proxy card, certificates, routes, ports. */
export function invalidateProxy(queryClient: QueryClient): Promise<void> {
	return invalidateOperations(queryClient, PROXY_OPERATIONS);
}

/** After a route or domain change: services, plus the server's route and port tables. */
export async function invalidateRoutes(
	queryClient: QueryClient,
): Promise<void> {
	await Promise.all([
		invalidateServices(queryClient),
		invalidateProxy(queryClient),
	]);
}
