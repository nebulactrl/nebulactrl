import type { QueryClient } from "@tanstack/react-query";
import { invalidateOperations } from "#/api/invalidate";

/** Operation ids a mesh change can make stale (the live feed in `api/sse.ts` uses the same ids). */
const MESH_OPERATIONS = [
	"getMesh",
	"getServerMesh",
	"getEnvironmentMesh",
	"getServiceMesh",
] as const;

/** After a change to the mesh: refetch every mesh view that is on screen. */
export function invalidateMesh(queryClient: QueryClient): Promise<void> {
	return invalidateOperations(queryClient, MESH_OPERATIONS);
}
