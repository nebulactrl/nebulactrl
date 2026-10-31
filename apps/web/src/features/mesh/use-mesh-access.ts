import { useQuery } from "@tanstack/react-query";
import { meQuery } from "#/api/hooks";

/**
 * What the signed-in principal may do with the mesh: server changes need
 * `servers:write`; the access policy (ACL mode, applying it) needs `admin`.
 * The server enforces both; the screens only offer what can succeed.
 */
export function useMeshAccess(): { canWrite: boolean; isAdmin: boolean } {
	const me = useQuery(meQuery());
	const scopes = me.data?.principal.scopes ?? [];
	const isAdmin = scopes.includes("admin");
	return { isAdmin, canWrite: isAdmin || scopes.includes("servers:write") };
}
