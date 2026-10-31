import { useQuery } from "@tanstack/react-query";
import { meQuery } from "#/api/hooks";

/** The signed-in principal holds the `admin` scope (loopback/link-local policy, Tailscale). */
export function useIsAdmin(): boolean {
	const me = useQuery(meQuery());
	return me.data?.principal.scopes?.includes("admin") ?? false;
}
