import { type QueryClient, queryOptions } from "@tanstack/react-query";
import {
	getServerOptions,
	getServerTunnelOptions,
	listServersOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { getServer, listServers } from "#/api/gen/sdk.gen";
import type { TunnelView } from "#/api/gen/types.gen";
import { invalidateOperations } from "#/api/invalidate";
import { toServerView } from "./model";
import { tunnelBusy } from "./proxy/tunnel-state";

// The shared server queries in `#/api/hooks` cache the mapped `Server` under
// the generated key, which has no per-server settings. These views cache a
// `ServerView` instead, so they take a key of their own: the generated head
// (SSE invalidation finds it by `_id`) plus a "view" tag. Sharing the key
// would let whichever query ran first decide the cached shape: the list
// came out empty when the sidebar had loaded it as a plain `Server[]`.

export const serverViewsQuery = () => {
	const base = listServersOptions({ query: { limit: 200 } });
	return queryOptions({
		queryKey: [...base.queryKey, "view"] as const,
		queryFn: async ({ signal }) => {
			const { data } = await listServers({
				...base.queryKey[0],
				signal,
				throwOnError: true,
			});
			return (data.items ?? []).map(toServerView);
		},
	});
};

export const serverViewQuery = (serverId: string) => {
	const base = getServerOptions({ path: { serverId } });
	return queryOptions({
		queryKey: [...base.queryKey, "view"] as const,
		queryFn: async ({ signal }) => {
			const { data } = await getServer({
				...base.queryKey[0],
				signal,
				throwOnError: true,
			});
			return toServerView(data);
		},
	});
};

/** The server's Cloudflare Tunnel; polls while the connector is connecting or being removed. */
export const serverTunnelQuery = (serverId: string) => ({
	...getServerTunnelOptions({ path: { serverId } }),
	refetchInterval: (query: { state: { data: TunnelView | undefined } }) =>
		tunnelBusy(query.state.data) ? 2_000 : false,
});

/** After a tunnel change: the tunnel card, the server views and the proxy routes. */
export function invalidateTunnel(queryClient: QueryClient): Promise<void> {
	return invalidateOperations(queryClient, [
		"getServerTunnel",
		"getServer",
		"listServers",
		"getServerProxy",
		"listServerProxyRoutes",
	]);
}
